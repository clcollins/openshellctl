package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Upload copies local (a path within the client's source fs) to dest inside the
// sandbox. dest is optional (empty → the SSH session cwd "."). When gitignore is
// true and local is a directory, gitignored files are excluded. Messages mirror
// run.rs:5744-5777 and ssh.rs:766-814.
//
// This method is the thin I/O shell: it stats the source, delegates every
// decision to the pure planUpload/upload-filter helpers, then streams the tar
// over SSH. The logic worth testing lives in those helpers and in
// writeUploadArchive; the SSH/tar streaming here is exercised via the gateway
// seam.
func (c *client) Upload(ctx context.Context, workspace, sandbox, local, dest string, gitignore bool, report func(string)) error {
	info, err := lstatFS(c.fsys, local)
	if err != nil {
		if isNotExist(err) {
			return fmt.Errorf("local path does not exist: %s", local)
		}
		return fmt.Errorf("failed to inspect local upload path: %s: %w", local, err)
	}

	reportf(report, "Uploading %s -> sandbox:%s", local, displayDest(dest))

	fileLike := info.Mode()&fs.ModeSymlink != 0 || info.Mode().IsRegular()
	entries, destDir, err := planUpload(local, dest, fileLike)
	if err != nil {
		return err
	}

	// Directory + gitignore: read the repo's ignore rules (fs I/O) and let the
	// pure decision below choose filtered vs unfiltered.
	if !fileLike && gitignore {
		filtered, ok, ferr := c.gitFilteredEntries(local, directoryUploadPrefix(local))
		if ferr == nil && ok {
			chosen, warn := chooseUploadEntries(entries, filtered)
			if warn {
				reportf(report, "⚠ .gitignore filtering excluded all files in %s; uploading unfiltered", local)
			}
			entries = chosen
		}
	}

	if err := c.runUploadTar(ctx, workspace, sandbox, destDir, entries); err != nil {
		return err
	}
	reportf(report, "✓ Upload complete")
	return nil
}

// runUploadTar dials the sandbox and streams the tar into the extract command.
// It is thin I/O glue over the gateway SSH tunnel and archive/tar.
func (c *client) runUploadTar(ctx context.Context, workspace, sandbox, destDir string, entries []uploadEntry) error {
	cli, _, err := c.dialSSH(ctx, workspace, sandbox)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	cmd := uploadExtractCommand(destDir)
	res, err := runRemoteStdin(cli, cmd, func(w io.Writer) error {
		return writeUploadArchive(w, c.fsys, entries)
	}, nil, nil)
	if err != nil {
		return err
	}
	if res.exitStatus != 0 {
		return fmt.Errorf("ssh tar extract exited with status %d", res.exitStatus)
	}
	return nil
}

// uploadExtractCommand builds the remote command "mkdir -p <d> && cat | tar xf -
// -C <d>" with <d> shell-escaped (ssh.rs:784-786). Pure.
func uploadExtractCommand(destDir string) string {
	escaped := ShellEscape(destDir)
	return fmt.Sprintf("mkdir -p %s && cat | tar xf - -C %s", escaped, escaped)
}

// planUpload is the pure tar-name / destination decision (sandbox_sync_up,
// ssh.rs:1005-1094). Given the source path, the requested dest, and whether the
// source is file-like (a regular file or symlink), it returns the archive
// entries to write and the remote directory to extract into. It performs no I/O
// and no gitignore filtering.
func planUpload(local, dest string, fileLike bool) ([]uploadEntry, string, error) {
	// File-like with an explicit dest not ending in "/": treat dest as a file
	// path unless its parent is "/" (the sandbox user cannot write "/").
	if fileLike && dest != "" && !strings.HasSuffix(dest, "/") {
		parent, target := splitSandboxPath(dest)
		if parent != "/" {
			return []uploadEntry{{fsPath: local, archivePath: target}}, parent, nil
		}
		// parent == "/": fall through to basename-in-dest semantics.
	}

	if fileLike {
		name := path.Base(local)
		if name == "." || name == "/" || name == "" {
			return nil, "", fmt.Errorf("path has no file name")
		}
		return []uploadEntry{{fsPath: local, archivePath: name}}, destOrDot(dest), nil
	}

	// Directory upload: extracts under <dest>/<basename>/… (scp -r semantics);
	// "." or "/" flatten to no prefix.
	prefix := directoryUploadPrefix(local)
	return []uploadEntry{{fsPath: local, archivePath: prefix}}, destOrDot(dest), nil
}

// chooseUploadEntries decides between the gitignore-filtered entry list and the
// unfiltered fallback: an empty filtered list means "everything was ignored" —
// fall back to unfiltered and signal a warning. Pure.
func chooseUploadEntries(unfiltered, filtered []uploadEntry) (chosen []uploadEntry, warnEmpty bool) {
	if len(filtered) == 0 {
		return unfiltered, true
	}
	return filtered, false
}

// directoryUploadPrefix returns the archive prefix for a directory upload: the
// basename, or "." for "." and "/". Mirrors directory_upload_prefix (ssh.rs:1077).
func directoryUploadPrefix(local string) string {
	base := path.Base(local)
	if local == "." || local == "/" || base == "." || base == "/" || base == "" {
		return "."
	}
	return base
}

func destOrDot(dest string) string {
	if dest == "" {
		return "."
	}
	return dest
}

func displayDest(dest string) string {
	if dest == "" {
		return "~"
	}
	return dest
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func reportf(report func(string), format string, args ...any) {
	if report != nil {
		report(fmt.Sprintf(format, args...))
	}
}

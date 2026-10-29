package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Download copies remote (a path inside the sandbox) to dest on the local host.
// dest is optional (empty → "."). Mirrors ssh.rs:1199-1382 and main.rs:3125-3145.
func (c *client) Download(ctx context.Context, workspace, sandbox, remote, dest string, report func(string)) error {
	reportf(report, "Downloading sandbox:%s -> %s", remote, destOrDot(dest))

	cli, _, err := c.dialSSH(ctx, workspace, sandbox)
	if err != nil {
		return err
	}
	defer func() { _ = cli.Close() }()

	resolved, err := resolveSandboxSourcePath(cli, remote)
	if err != nil {
		return err
	}

	kind, err := probeSandboxSourceKind(cli, resolved)
	if err != nil {
		return err
	}

	switch kind {
	case sourceFile:
		if err := downloadFile(cli, resolved, dest); err != nil {
			return err
		}
	case sourceDir:
		if err := downloadDir(cli, resolved, destOrDot(dest)); err != nil {
			return err
		}
	}

	reportf(report, "✓ Download complete")
	return nil
}

type sourceKind int

const (
	sourceFile sourceKind = iota
	sourceDir
)

// sourceProbeCommand builds the "pwd -P && realpath -e -- <p>" probe command
// with <p> shell-escaped (ssh.rs:909-912). Pure.
func sourceProbeCommand(remote string) string {
	return fmt.Sprintf("pwd -P && realpath -e -- %s", ShellEscape(remote))
}

// resolveSandboxSourcePath runs the resolve probe and validates the result. It
// is the thin SSH shell around the pure parseAndValidateSourcePath. Mirrors
// ssh.rs:905-938.
func resolveSandboxSourcePath(cli *ssh.Client, remote string) (string, error) {
	out, res, err := runRemoteCapture(cli, sourceProbeCommand(remote))
	if err != nil {
		return "", err
	}
	return parseAndValidateSourcePath(remote, res.exitStatus == 0, out)
}

// parseAndValidateSourcePath is the pure validation of a resolve-probe result:
// given the requested path, whether the probe succeeded, and its raw stdout, it
// returns the resolved absolute path or a verbatim upstream error. It rejects
// paths outside the workspace both lexically (before realpath) and after realpath
// (closing the symlink-escape gap). Mirrors ssh.rs:905-938.
func parseAndValidateSourcePath(remote string, probeOK bool, stdout []byte) (string, error) {
	if !probeOK {
		// realpath -e fails when the path does not exist.
		return "", fmt.Errorf("sandbox source path '%s' does not exist", remote)
	}

	text := decodeProbeStdout(stdout)
	nl := strings.IndexByte(text, '\n')
	if nl < 0 {
		return "", fmt.Errorf("unexpected response while resolving sandbox source path '%s'", remote)
	}
	root := text[:nl]
	resolved := text[nl+1:]
	if strings.ContainsRune(resolved, '\n') {
		return "", fmt.Errorf("unexpected response while resolving sandbox source path '%s'", remote)
	}

	if err := validateWorkspaceRoot(root); err != nil {
		return "", err
	}

	requested := remote
	if !strings.HasPrefix(requested, "/") {
		requested = root + "/" + requested
	}
	cleaned, ok := lexicalCleanAbsolutePath(requested)
	if !ok {
		return "", fmt.Errorf("sandbox source path is invalid (got '%s')", remote)
	}
	if !pathIsOrUnder(cleaned, root) {
		return "", fmt.Errorf("sandbox source path '%s' is outside the sandbox workspace (%s)", remote, root)
	}
	if resolved == "" {
		return "", fmt.Errorf("sandbox source path '%s' does not exist", remote)
	}
	if !pathIsOrUnder(resolved, root) {
		return "", fmt.Errorf("sandbox source path '%s' resolves to '%s', outside the sandbox workspace (%s)", remote, resolved, root)
	}
	return resolved, nil
}

func validateWorkspaceRoot(root string) error {
	cleaned, ok := lexicalCleanAbsolutePath(root)
	if !ok {
		return fmt.Errorf("remote workspace must be an absolute path")
	}
	if cleaned == "/" {
		return fmt.Errorf("remote workspace resolved to the container root")
	}
	if cleaned != root {
		return fmt.Errorf("remote workspace '%s' is not a canonical absolute path", root)
	}
	return nil
}

// typeProbeCommand builds the "[ -d ] / [ -e ]" type probe (ssh.rs:1167-1170).
// Pure.
func typeProbeCommand(p string) string {
	esc := ShellEscape(p)
	return fmt.Sprintf("if [ -d %s ]; then printf dir; elif [ -e %s ]; then printf file; else printf missing; fi", esc, esc)
}

// probeSandboxSourceKind runs the type probe (thin SSH shell over parseSourceKind).
func probeSandboxSourceKind(cli *ssh.Client, p string) (sourceKind, error) {
	out, res, err := runRemoteCapture(cli, typeProbeCommand(p))
	if err != nil {
		return 0, err
	}
	return parseSourceKind(p, res.exitStatus, out)
}

// parseSourceKind is the pure interpretation of the type-probe result.
func parseSourceKind(p string, exitStatus int, stdout []byte) (sourceKind, error) {
	if exitStatus != 0 {
		return 0, fmt.Errorf("ssh probe exited with status %d", exitStatus)
	}
	switch strings.TrimSpace(string(stdout)) {
	case "dir":
		return sourceDir, nil
	case "file":
		return sourceFile, nil
	case "missing":
		return 0, fmt.Errorf("sandbox source path '%s' does not exist", p)
	default:
		return 0, fmt.Errorf("unexpected probe output for sandbox source path '%s': '%s'", p, strings.TrimSpace(string(stdout)))
	}
}

// downloadFile streams "tar cf - -C <parent> -- <name>" into a staging dir then
// renames the entry into place. Mirrors ssh.rs:1282-1355.
func downloadFile(cli *ssh.Client, remote, dest string) error {
	parent, name := splitSandboxPath(remote)
	finalPath := resolveFileDownloadTarget(dest, name)

	stagingParent := filepath.Dir(finalPath)
	if stagingParent == "" {
		return fmt.Errorf("destination '%s' has no parent directory", finalPath)
	}
	if err := os.MkdirAll(stagingParent, 0o755); err != nil {
		return fmt.Errorf("failed to create local destination directory '%s': %w", stagingParent, err)
	}
	staging, err := os.MkdirTemp(stagingParent, ".openshell-dl-")
	if err != nil {
		return fmt.Errorf("failed to create download staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := streamTarInto(cli, singleFileTarCommand(parent, name), staging); err != nil {
		return err
	}

	staged := filepath.Join(staging, name)
	info, err := os.Lstat(staged)
	if err != nil {
		return fmt.Errorf("downloaded archive did not contain the expected entry")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("downloaded entry '%s' is not a regular file", name)
	}
	if di, derr := os.Stat(finalPath); derr == nil && di.IsDir() {
		return fmt.Errorf("cannot overwrite directory '%s' with downloaded file", finalPath)
	}
	if err := os.Rename(staged, finalPath); err != nil {
		return fmt.Errorf("failed to place downloaded file at '%s': %w", finalPath, err)
	}
	return nil
}

// resolveFileDownloadTarget mirrors ssh.rs:950-962: dest ending "/" or an
// existing directory → dest/basename; else dest is the exact file path. Empty
// dest → basename in ".".
func resolveFileDownloadTarget(dest, name string) string {
	if dest == "" {
		return name
	}
	if strings.HasSuffix(dest, "/") {
		return filepath.Join(dest, name)
	}
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		return filepath.Join(dest, name)
	}
	return dest
}

// downloadDir streams "tar cf - -C <path> ." unpacked into dest. Mirrors
// ssh.rs:1357-1382.
func downloadDir(cli *ssh.Client, remote, dest string) error {
	if info, err := os.Stat(dest); err == nil && !info.IsDir() {
		return fmt.Errorf("cannot extract directory '%s' over non-directory destination '%s'", remote, dest)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("failed to create local destination directory '%s': %w", dest, err)
	}
	return streamTarInto(cli, dirTarCommand(remote), dest)
}

// singleFileTarCommand builds "tar cf - -C <parent> -- <name>" (ssh.rs:1274). The
// "--" is load-bearing for names beginning with "-". Pure.
func singleFileTarCommand(parent, name string) string {
	return fmt.Sprintf("tar cf - -C %s -- %s", ShellEscape(parent), ShellEscape(name))
}

// dirTarCommand builds "tar cf - -C <path> ." (ssh.rs:1380). Pure.
func dirTarCommand(path string) string {
	return fmt.Sprintf("tar cf - -C %s .", ShellEscape(path))
}

// streamTarInto runs cmd and extracts its stdout tar into dir, rejecting entries
// with ".." components or absolute paths (archive/tar does no such filtering).
//
// The OpenShell SSH server may not close the SSH data channel or send an
// exit-status after the remote tar command finishes. The Rust CLI avoids this
// because it shells out to the ssh binary, which exits after stdout EOF. Our
// in-process ssh.Session hangs on both io.Copy (waiting for channel close) and
// sess.Wait (waiting for exit-status). To work around this, the tar extractor
// runs first; once it has consumed all archive entries it closes the session
// to unblock the stuck io.Copy.
func streamTarInto(cli *ssh.Client, cmd, dir string) error {
	sess, err := cli.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()

	stdout, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	sess.Stderr = io.Discard
	if err := sess.Start(cmd); err != nil {
		return err
	}

	extractErr := extractTar(stdout, dir)

	// Tear down the session. If the server already closed the channel,
	// this is a no-op. If it didn't (the common OpenShell case), this
	// unblocks any lingering reads.
	_ = sess.Close()

	return extractErr
}

// extractTar unpacks a tar stream into dir, guarding against path traversal.
//
// All filesystem operations go through os.Root, which refuses to follow
// symlinks that leave the root directory. Symlink entries are additionally
// validated: absolute targets and ".." escapes are rejected outright.
func extractTar(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("failed to open extraction root %q: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := sanitizeTarName(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, os.FileMode(hdr.Mode)&os.ModePerm|0o700); err != nil {
				return err
			}
		case tar.TypeSymlink:
			parent := filepath.Dir(name)
			if err := validateSymlinkTarget(hdr.Linkname, parent); err != nil {
				return fmt.Errorf("refusing symlink %q: %w", name, err)
			}
			if parent != "." {
				if err := root.MkdirAll(parent, 0o755); err != nil {
					return err
				}
			}
			if info, err := root.Lstat(name); err == nil && !info.IsDir() {
				if err := root.Remove(name); err != nil {
					return err
				}
			}
			if err := root.Symlink(hdr.Linkname, name); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := extractRegularFile(root, tr, name, hdr.Mode); err != nil {
				return err
			}
		default:
			// Skip other entry types (fifo, device, etc.).
		}
	}
}

func extractRegularFile(root *os.Root, tr *tar.Reader, name string, mode int64) error {
	parent := filepath.Dir(name)
	if parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode)&os.ModePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, tr); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// sanitizeTarName validates and cleans an archive entry name, rejecting
// absolute paths and ".." escapes.
func sanitizeTarName(name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("refusing to extract absolute path %q", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to extract path outside destination %q", name)
	}
	return clean, nil
}

// validateSymlinkTarget rejects symlink targets that are absolute or that,
// when resolved relative to the link's own directory, escape the extraction
// root. linkdir is the directory containing the link (filepath.Dir of the
// archive entry name); it must be a clean relative path inside the root.
func validateSymlinkTarget(target, linkdir string) error {
	if filepath.IsAbs(target) || strings.HasPrefix(target, "/") {
		return fmt.Errorf("absolute symlink target %q", target)
	}
	resolved := filepath.Clean(filepath.Join(linkdir, target))
	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink target %q (from %q) escapes destination", target, linkdir)
	}
	return nil
}

// decodeProbeStdout strips one trailing \n then one trailing \r (ssh.rs:1124).
func decodeProbeStdout(b []byte) string {
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	return string(b)
}

package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

const binaryName = "openshellctl"

// ExtractBinary opens the .tar.gz at archivePath, locates the regular-file
// entry named "openshellctl" (or "./openshellctl"), copies it to dst under a
// size cap of maxSize bytes, and returns an error if:
//   - the entry is missing → ErrBinaryEntryMissing
//   - the entry is a symlink or directory → ErrBinaryEntryMissing (rejected)
//   - the entry exceeds maxSize → error
//
// The destination file is opened for writing before extraction begins, so any
// path problem is caught early. The gzip layer is only opened after the digest
// has been verified by the caller (DownloadAndVerify); the caller must pass an
// already-verified archive.
func ExtractBinary(archivePath, dst string, maxSize int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		// Accept exactly "openshellctl" or "./openshellctl"; reject any subdir path.
		if hdr.Name != binaryName && hdr.Name != "./"+binaryName {
			continue
		}
		// Only regular files; reject symlinks and directories.
		if hdr.Typeflag != tar.TypeReg {
			return &ErrBinaryEntryMissing{}
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			return err
		}
		limited := io.LimitReader(tr, maxSize+1)
		n, copyErr := io.Copy(out, limited)
		closeErr := out.Close()
		if copyErr != nil {
			os.Remove(dst)
			return fmt.Errorf("extracting binary: %w", copyErr)
		}
		if closeErr != nil {
			os.Remove(dst)
			return closeErr
		}
		if n > maxSize {
			os.Remove(dst)
			return fmt.Errorf("binary entry exceeds size limit of %d bytes", maxSize)
		}
		return nil
	}
	return &ErrBinaryEntryMissing{}
}

package selfupdate

import (
	"errors"
	"os"
)

// ResolveExecutable resolves the path to the real on-disk binary, following
// any symbolic links. getExe is typically os.Executable; evalSymlinks is
// typically filepath.EvalSymlinks. Both are injected so tests can use
// t.TempDir() without touching the real filesystem outside it.
func ResolveExecutable(
	getExe func() (string, error),
	evalSymlinks func(string) (string, error),
) (string, error) {
	exe, err := getExe()
	if err != nil {
		return "", err
	}
	real, err := evalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return real, nil
}

// Replace atomically replaces exePath with newFile via os.Rename. newFile and
// exePath must be on the same filesystem (os.Rename is atomic only within one
// device). Returns ErrReplaceDenied when the rename fails due to a permission
// error so the caller can produce a human-readable hint.
func Replace(newFile, exePath string) error {
	// Preserve the existing binary's mode on the replacement file.
	if info, err := os.Stat(exePath); err == nil {
		_ = os.Chmod(newFile, info.Mode())
	}

	if err := os.Rename(newFile, exePath); err != nil {
		if isPermissionError(err) {
			return &ErrReplaceDenied{Path: exePath}
		}
		return err
	}
	return nil
}

func isPermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

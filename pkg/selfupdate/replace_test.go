package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExecutable_DirectPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "openshellctl")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveExecutable(
		func() (string, error) { return exe, nil },
		filepath.EvalSymlinks,
	)
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	if got != exe {
		t.Errorf("got %q, want %q", got, exe)
	}
}

func TestResolveExecutable_FollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "openshellctl")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin", "openshellctl")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveExecutable(
		func() (string, error) { return link, nil },
		filepath.EvalSymlinks,
	)
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	if got != real {
		t.Errorf("got %q, want %q (real path after symlink resolution)", got, real)
	}
}

func TestReplace_AtomicRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "openshellctl")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, ".openshellctl-new")
	if err := os.WriteFile(newBin, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := Replace(newBin, target); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}

	// newBin must be gone after the rename
	if _, err := os.Stat(newBin); !os.IsNotExist(err) {
		t.Errorf("newBin %q still exists after Replace", newBin)
	}
}

func TestReplace_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission check is meaningless")
	}
	dir := t.TempDir()
	// Make dir read-only so rename fails
	target := filepath.Join(dir, "openshellctl")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, ".openshellctl-new")
	if err := os.WriteFile(newBin, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := Replace(newBin, target)
	var denied *ErrReplaceDenied
	if !errors.As(err, &denied) {
		t.Errorf("Replace error = %T(%v), want *ErrReplaceDenied", err, err)
	}
	if denied != nil && denied.Path != target {
		t.Errorf("ErrReplaceDenied.Path = %q, want %q", denied.Path, target)
	}
}

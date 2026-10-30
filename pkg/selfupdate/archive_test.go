package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractBinary_HappyPath(t *testing.T) {
	want := []byte("binary content here")
	archivePath := makeTar(t, []tarEntry{
		{name: "LICENSE", content: []byte("Apache 2.0"), typeFlag: tar.TypeReg},
		{name: "README.md", content: []byte("readme"), typeFlag: tar.TypeReg},
		{name: "openshellctl", content: want, typeFlag: tar.TypeReg},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	if err := ExtractBinary(archivePath, dst, 1<<20); err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading binary: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("binary content mismatch")
	}
}

func TestExtractBinary_DotPrefixEntry(t *testing.T) {
	want := []byte("binary content")
	archivePath := makeTar(t, []tarEntry{
		{name: "./openshellctl", content: want, typeFlag: tar.TypeReg},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	if err := ExtractBinary(archivePath, dst, 1<<20); err != nil {
		t.Fatalf("ExtractBinary with ./openshellctl entry: %v", err)
	}
}

func TestExtractBinary_SymlinkEntryRejected(t *testing.T) {
	archivePath := makeTar(t, []tarEntry{
		{name: "openshellctl", content: nil, typeFlag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	err := ExtractBinary(archivePath, dst, 1<<20)
	var missing *ErrBinaryEntryMissing
	if !errors.As(err, &missing) {
		t.Errorf("expected ErrBinaryEntryMissing for symlink entry, got %T(%v)", err, err)
	}
}

func TestExtractBinary_DirEntryRejected(t *testing.T) {
	archivePath := makeTar(t, []tarEntry{
		{name: "openshellctl", content: nil, typeFlag: tar.TypeDir},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	err := ExtractBinary(archivePath, dst, 1<<20)
	var missing *ErrBinaryEntryMissing
	if !errors.As(err, &missing) {
		t.Errorf("expected ErrBinaryEntryMissing for dir entry, got %T(%v)", err, err)
	}
}

func TestExtractBinary_MissingEntry(t *testing.T) {
	archivePath := makeTar(t, []tarEntry{
		{name: "README.md", content: []byte("readme"), typeFlag: tar.TypeReg},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	err := ExtractBinary(archivePath, dst, 1<<20)
	var missing *ErrBinaryEntryMissing
	if !errors.As(err, &missing) {
		t.Errorf("expected ErrBinaryEntryMissing, got %T(%v)", err, err)
	}
}

func TestExtractBinary_SubdirPathRejected(t *testing.T) {
	// "foo/openshellctl" should not match — only bare "openshellctl" or "./openshellctl"
	// guard against a crafted archive extracting to an unexpected path.
	archivePath := makeTar(t, []tarEntry{
		{name: "foo/openshellctl", content: []byte("binary"), typeFlag: tar.TypeReg},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	err := ExtractBinary(archivePath, dst, 1<<20)
	var missing *ErrBinaryEntryMissing
	if !errors.As(err, &missing) {
		t.Errorf("expected ErrBinaryEntryMissing for subdir entry, got %T(%v)", err, err)
	}
}

func TestExtractBinary_SizeCap(t *testing.T) {
	content := make([]byte, 200)
	archivePath := makeTar(t, []tarEntry{
		{name: "openshellctl", content: content, typeFlag: tar.TypeReg},
	})
	dir := t.TempDir()
	dst := filepath.Join(dir, "openshellctl")
	err := ExtractBinary(archivePath, dst, 50)
	if err == nil {
		t.Error("expected error for oversized binary entry, got nil")
	}
}

// tarEntry describes one file to include in a test tar.gz archive.
type tarEntry struct {
	name     string
	content  []byte
	typeFlag byte
	linkname string
}

// makeTar creates a temporary .tar.gz containing the given entries and returns
// its path. The caller's t.TempDir() is reused as the destination.
func makeTar(t *testing.T, entries []tarEntry) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeFlag,
			Linkname: e.linkname,
			Size:     int64(len(e.content)),
			Mode:     0o755,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write content: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}
	return path
}

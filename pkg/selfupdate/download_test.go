package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDownloadAndVerify_HappyPath(t *testing.T) {
	content := []byte("hello archive")
	h := sha256.Sum256(content)
	want := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst, err := DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/archive.tar.gz", dir, want, 1<<20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(dst)

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading result: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch")
	}
}

func TestDownloadAndVerify_ChecksumMismatch(t *testing.T) {
	content := []byte("hello archive")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst, err := DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/archive.tar.gz", dir, "deadbeef", 1<<20)
	var mm *ErrChecksumMismatch
	if !errors.As(err, &mm) {
		t.Errorf("error = %T(%v), want *ErrChecksumMismatch", err, err)
	}
	// Temp file must be removed on mismatch
	if dst != "" {
		if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
			t.Errorf("temp file %q still exists after checksum mismatch", dst)
		}
	}
}

func TestDownloadAndVerify_SizeCap(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/archive.tar.gz", dir, "ignored", 50)
	if err == nil {
		t.Error("expected error for oversize download, got nil")
	}
}

func TestDownloadAndVerify_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	_, err := DownloadAndVerify(context.Background(), srv.Client(), srv.URL+"/archive.tar.gz", dir, "deadbeef", 1<<20)
	if err == nil {
		t.Error("expected error for HTTP 5xx, got nil")
	}
}

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// maxChecksumFileSize caps how much of a checksums file we will read (64 KiB).
const maxChecksumFileSize = 64 << 10

// DownloadAndVerify downloads the resource at url into a temp file created
// under dir, streaming the content through a SHA-256 hasher simultaneously.
// After the download completes the digest is compared to wantHex (lowercase
// hex); a mismatch removes the temp file and returns ErrChecksumMismatch.
// The download is aborted if more than maxBytes bytes would be written.
// Returns the path of the verified temp file on success.
func DownloadAndVerify(ctx context.Context, client *http.Client, url, dir, wantHex string, maxBytes int64) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}

	tmp, err := os.CreateTemp(dir, ".openshellctl-download-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cleanup()
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		cleanup()
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}

	h := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	if n > maxBytes {
		cleanup()
		return "", fmt.Errorf("download from %s exceeded size limit of %d bytes", url, maxBytes)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}

	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != strings.ToLower(wantHex) {
		os.Remove(tmpPath)
		return "", &ErrChecksumMismatch{Want: wantHex, Got: gotHex}
	}

	return tmpPath, nil
}

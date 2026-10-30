package selfupdate

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseChecksums parses a goreleaser checksums file (SHA-256) and returns a
// map of filename → lowercase hex digest. Both the standard two-space text
// format ("hex  filename") and the binary-mode star format ("hex *filename")
// are accepted. Blank lines and CRLF line endings are tolerated. A line with
// a malformed hex string or a missing filename is an error.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(r)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		// Expected: "<hex>  <filename>" or "<hex> *<filename>"
		idx := strings.IndexByte(line, ' ')
		if idx < 0 {
			return nil, fmt.Errorf("line %d: expected \"<hex>  <filename>\", got %q", lineNum, line)
		}
		hexStr := line[:idx]
		rest := line[idx+1:]
		// Strip leading space (text mode) or '*' (binary mode)
		rest = strings.TrimLeft(rest, " ")
		rest = strings.TrimPrefix(rest, "*")
		filename := rest
		if filename == "" {
			return nil, fmt.Errorf("line %d: empty filename", lineNum)
		}
		// Validate hex string: must be valid hex and exactly 64 characters (SHA-256).
		if len(hexStr) != sha256.Size*2 {
			return nil, fmt.Errorf("line %d: hex digest must be %d characters, got %d", lineNum, sha256.Size*2, len(hexStr))
		}
		if _, err := hex.DecodeString(hexStr); err != nil {
			return nil, fmt.Errorf("line %d: invalid hex %q: %w", lineNum, hexStr, err)
		}
		out[filename] = strings.ToLower(hexStr)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyFile reads the file at path and returns an error if its SHA-256
// digest does not match wantHex (lowercase hex).
func VerifyFile(path, wantHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != strings.ToLower(wantHex) {
		return &ErrChecksumMismatch{Want: wantHex, Got: gotHex}
	}
	return nil
}

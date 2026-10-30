package selfupdate

import (
	"errors"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	tests := []struct {
		goos    string
		goarch  string
		want    string
		wantErr bool
	}{
		{"linux", "amd64", "openshellctl_Linux_x86_64.tar.gz", false},
		{"linux", "arm64", "openshellctl_Linux_arm64.tar.gz", false},
		{"darwin", "amd64", "openshellctl_Darwin_x86_64.tar.gz", false},
		{"darwin", "arm64", "openshellctl_Darwin_arm64.tar.gz", false},
		{"windows", "amd64", "", true},
		{"linux", "386", "", true},
		{"freebsd", "amd64", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			got, err := AssetName(tt.goos, tt.goarch)
			if tt.wantErr {
				var e *ErrUnsupportedPlatform
				if !errors.As(err, &e) {
					t.Errorf("AssetName(%q, %q) error = %v, want ErrUnsupportedPlatform", tt.goos, tt.goarch, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("AssetName(%q, %q) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
			}
		})
	}
}

func TestChecksumAssetName(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"v0.1.5", "openshellctl_0.1.5_checksums.txt"},
		{"0.1.5", "openshellctl_0.1.5_checksums.txt"},
		{"v0.1.5-rc.1", "openshellctl_0.1.5-rc.1_checksums.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := ChecksumAssetName(tt.tag)
			if got != tt.want {
				t.Errorf("ChecksumAssetName(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

// validHex64 is a canonical 64-character lowercase hex string (valid SHA-256 digest).
const (
	validHex64a = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	validHex64b = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func TestParseChecksums(t *testing.T) {
	t.Run("two-space separator", func(t *testing.T) {
		input := validHex64a + "  openshellctl_Linux_x86_64.tar.gz\n" +
			validHex64b + "  openshellctl_Darwin_arm64.tar.gz\n"
		got, err := ParseChecksums(strings.NewReader(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["openshellctl_Linux_x86_64.tar.gz"] != validHex64a {
			t.Errorf("Linux amd64 = %q, want %q", got["openshellctl_Linux_x86_64.tar.gz"], validHex64a)
		}
		if got["openshellctl_Darwin_arm64.tar.gz"] != validHex64b {
			t.Errorf("Darwin arm64 = %q, want %q", got["openshellctl_Darwin_arm64.tar.gz"], validHex64b)
		}
	})

	t.Run("binary-mode star separator", func(t *testing.T) {
		// shasum -b produces "<hex> *<filename>"
		input := validHex64a + " *openshellctl_Linux_x86_64.tar.gz\n"
		got, err := ParseChecksums(strings.NewReader(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["openshellctl_Linux_x86_64.tar.gz"] != validHex64a {
			t.Errorf("got %q, want %q", got["openshellctl_Linux_x86_64.tar.gz"], validHex64a)
		}
	})

	t.Run("blank lines and CRLF tolerated", func(t *testing.T) {
		input := "\r\n" + validHex64a + "  foo.tar.gz\r\n\r\n"
		got, err := ParseChecksums(strings.NewReader(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["foo.tar.gz"] != validHex64a {
			t.Errorf("got %q, want %q", got["foo.tar.gz"], validHex64a)
		}
	})

	t.Run("malformed hex rejected", func(t *testing.T) {
		input := "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz  foo.tar.gz\n"
		_, err := ParseChecksums(strings.NewReader(input))
		if err == nil {
			t.Error("expected error for malformed hex, got nil")
		}
	})

	t.Run("short hex rejected", func(t *testing.T) {
		// Valid hex chars but only 62 characters — not a SHA-256 digest.
		input := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"[:62] + "  foo.tar.gz\n"
		_, err := ParseChecksums(strings.NewReader(input))
		if err == nil {
			t.Error("expected error for short hex (62 chars), got nil")
		}
	})

	t.Run("empty reader returns empty map", func(t *testing.T) {
		got, err := ParseChecksums(strings.NewReader(""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty map", got)
		}
	})

	t.Run("missing filename rejected", func(t *testing.T) {
		input := validHex64a + "\n"
		_, err := ParseChecksums(strings.NewReader(input))
		if err == nil {
			t.Error("expected error for missing filename, got nil")
		}
	})
}

func TestFindAsset(t *testing.T) {
	rel := Release{Assets: []Asset{
		{Name: "openshellctl_Linux_x86_64.tar.gz", BrowserDownloadURL: "https://github.com/example/v1"},
		{Name: "openshellctl_0.1.5_checksums.txt", BrowserDownloadURL: "https://github.com/example/cs"},
	}}

	t.Run("found", func(t *testing.T) {
		a, ok := FindAsset(rel, "openshellctl_Linux_x86_64.tar.gz")
		if !ok {
			t.Fatal("FindAsset returned not found")
		}
		if a.BrowserDownloadURL != "https://github.com/example/v1" {
			t.Errorf("URL = %q", a.BrowserDownloadURL)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, ok := FindAsset(rel, "nonexistent.tar.gz")
		if ok {
			t.Error("FindAsset returned ok for nonexistent asset")
		}
	})
}

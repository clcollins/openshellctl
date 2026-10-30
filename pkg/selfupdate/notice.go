package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cache is the on-disk structure of the update-check cache file.
type Cache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

// CheckInputs holds all the pure inputs to ShouldCheck.
type CheckInputs struct {
	// Command is the name of the subcommand being run.
	Command string
	// NoUpdateCheck is true when --no-update-check or OPENSHELL_NO_UPDATE_CHECK
	// is set.
	NoUpdateCheck bool
	// StdoutIsTTY and StderrIsTTY report whether stdout/stderr are terminals.
	StdoutIsTTY bool
	StderrIsTTY bool
	// CI is the value of the CI environment variable; non-empty means CI mode.
	CI string
	// CurrentVersion is the classification of the running binary's version.
	CurrentVersion Current
	// CacheAge is how long ago the last check was performed. The caller sets
	// this to time.Since(cache.CheckedAt), or to a large value when the cache
	// is absent or unreadable.
	CacheAge time.Duration
}

// skipCommands are the command names that never trigger the notice.
var skipCommands = map[string]bool{
	"update":     true,
	"version":    true,
	"completion": true,
	"__complete": true,
	"help":       true,
}

// NoticeCacheTTL is how long a cached update-check result is considered fresh.
// Exported so the CLI wiring can use the same constant for the goroutine's
// cache-read path without duplicating the value.
const NoticeCacheTTL = 24 * time.Hour

// ShouldCheck reports whether the background update notice goroutine should
// fire a network request for this invocation. All inputs are pure values;
// no I/O is performed.
func ShouldCheck(in CheckInputs) bool {
	if in.NoUpdateCheck {
		return false
	}
	if skipCommands[in.Command] {
		return false
	}
	if !in.StdoutIsTTY || !in.StderrIsTTY {
		return false
	}
	if in.CI != "" {
		return false
	}
	if in.CurrentVersion.Unknown {
		return false
	}
	if in.CacheAge < NoticeCacheTTL {
		return false
	}
	return true
}

// NoticeText returns a one-line notice to print to stderr when latestTag is
// newer than currentTag. Returns "" when no notice should be shown.
// noColor is currently unused (reserved for future ANSI stripping).
func NoticeText(currentTag, latestTag string, noColor bool) string {
	if !semverIsValid(currentTag) || !semverIsValid(latestTag) {
		return ""
	}
	if semverCompare(latestTag, currentTag) <= 0 {
		return ""
	}
	return fmt.Sprintf(
		`A newer openshellctl is available: %s → %s. Run "openshellctl update" to install it.`,
		currentTag, latestTag,
	)
}

// LoadCache reads and decodes the cache file at path. Returns an error on
// any I/O or parse failure; the caller should treat both as "cache absent".
func LoadCache(path string) (Cache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, err
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, err
	}
	return c, nil
}

// SaveCache atomically writes c to path, creating parent directories with mode
// 0700 and the file with mode 0600.
func SaveCache(path string, c Cache) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	// Atomic write: temp file → rename.
	tmp, err := os.CreateTemp(dir, ".update-check-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil {
		os.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// CacheDir returns the XDG-compliant directory for the update-check cache.
// Callers should use this to compute the cache file path.
func CacheDir(getenv func(string) string) string {
	if xdg := getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "openshellctl")
	}
	home := getenv("HOME")
	return filepath.Join(home, ".cache", "openshellctl")
}

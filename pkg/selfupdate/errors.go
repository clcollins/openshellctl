package selfupdate

import "fmt"

// ErrNoRelease is returned when no release matching the selection criteria exists.
type ErrNoRelease struct {
	AllowPrerelease bool
}

func (e *ErrNoRelease) Error() string {
	if e.AllowPrerelease {
		return "no qualifying release found"
	}
	return "no stable release published; use --prerelease or --version to select a pre-release"
}

// ErrTagNotFound is returned by GetRelease when the requested tag does not
// exist (HTTP 404). Distinct from ErrNoRelease so callers can provide a
// targeted error message (e.g. "release v1.2.3 not found").
type ErrTagNotFound struct{ Tag string }

func (e *ErrTagNotFound) Error() string {
	return fmt.Sprintf("release %s not found", e.Tag)
}

// ErrAssetMissing is returned when a required asset is not present in the release.
type ErrAssetMissing struct{ Name string }

func (e *ErrAssetMissing) Error() string {
	return fmt.Sprintf("release asset %q not found", e.Name)
}

// ErrChecksumMismatch is returned when the downloaded archive's digest does not
// match the checksum file.
type ErrChecksumMismatch struct{ Want, Got string }

func (e *ErrChecksumMismatch) Error() string {
	return fmt.Sprintf("checksum mismatch: want %s, got %s", e.Want, e.Got)
}

// ErrChecksumEntryMissing is returned when the checksum file does not contain
// an entry for the expected archive name.
type ErrChecksumEntryMissing struct{ Name string }

func (e *ErrChecksumEntryMissing) Error() string {
	return fmt.Sprintf("no checksum entry for %q in checksums file", e.Name)
}

// ErrUnsupportedPlatform is returned when no archive exists for the current OS/arch.
type ErrUnsupportedPlatform struct{ GOOS, GOARCH string }

func (e *ErrUnsupportedPlatform) Error() string {
	return fmt.Sprintf("self-update is not supported on %s/%s", e.GOOS, e.GOARCH)
}

// ErrManagedInstall is returned when the binary was installed via a package
// manager or container image that manages its own updates.
type ErrManagedInstall struct {
	Method string
	Hint   string
}

func (e *ErrManagedInstall) Error() string {
	return fmt.Sprintf("self-update is not available for %s installs: %s", e.Method, e.Hint)
}

// ErrReplaceDenied is returned when the running executable cannot be replaced
// due to insufficient permissions.
type ErrReplaceDenied struct{ Path string }

func (e *ErrReplaceDenied) Error() string {
	return fmt.Sprintf("cannot replace %s: permission denied — re-run with sufficient permissions to write %s, or reinstall", e.Path, e.Path)
}

// ErrRateLimited is returned when the GitHub API returns HTTP 403 (rate limit).
type ErrRateLimited struct{}

func (e *ErrRateLimited) Error() string {
	return "GitHub API rate limit exceeded"
}

// ErrBinaryEntryMissing is returned when the tar archive does not contain an
// entry named "openshellctl".
type ErrBinaryEntryMissing struct{}

func (e *ErrBinaryEntryMissing) Error() string {
	return "archive does not contain an openshellctl binary entry"
}

package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Options configures a single invocation of Run.
type Options struct {
	// GOOS and GOARCH identify the platform for asset selection.
	GOOS   string
	GOARCH string

	// Current is the version classification of the running binary.
	Current Current

	// TargetTag, when non-empty, bypasses release selection and fetches this
	// exact tag. Accepts "vX.Y.Z" or "X.Y.Z" (normalised to "vX.Y.Z").
	TargetTag string

	// AllowPrerelease allows the default release selection to pick pre-releases.
	// Ignored when TargetTag is set.
	AllowPrerelease bool

	// CheckOnly resolves and reports the target version but does not download
	// or replace the binary.
	CheckOnly bool

	// InstallMethod, when non-empty, causes Run to return ErrManagedInstall
	// immediately. Set from internal/version.InstallMethod.
	InstallMethod string

	// testAllowedHosts extends the download URL allow-list beyond the default
	// github.com and objects.githubusercontent.com. Only for use by in-package
	// tests (e.g. to allow httptest.Server URLs); leave nil in production.
	testAllowedHosts []string

	// Fetcher is used to resolve releases.
	Fetcher Fetcher

	// HTTPClient is used for downloading archives and checksum files.
	// Defaults to http.DefaultClient when nil.
	HTTPClient *http.Client

	// WorkDir is the directory used for temporary download files. It must be
	// on the same filesystem as ExePath so os.Rename is atomic.
	WorkDir string

	// ExePath is the absolute path to the binary to replace.
	ExePath string

	// MaxBytes caps the size of the downloaded archive (default: 256 MiB).
	MaxBytes int64
}

// Result summarises the outcome of Run.
type Result struct {
	// TargetTag is the tag of the resolved target release.
	TargetTag string
	// CurrentTag is the tag of the running binary (or "" if Unknown).
	CurrentTag string
	// Updated is true if the binary was actually replaced.
	Updated bool
	// AlreadyAtTarget is true if target == current and nothing was done.
	AlreadyAtTarget bool
	// Ahead is true when the running binary is newer than the latest available
	// release under default resolution (no explicit --version). This happens
	// when a dev build or a pre-release binary is newer than the latest stable
	// release; the binary is not downgraded and nothing is installed.
	Ahead bool
	// Downgrade is true if TargetTag < CurrentTag.
	Downgrade bool
}

const maxBytesDefault = 256 << 20 // 256 MiB

// Run executes the full update sequence: resolve target release, compare
// versions, download and verify the archive, extract the binary, and replace
// the running executable.
func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.InstallMethod != "" {
		return Result{}, &ErrManagedInstall{
			Method: opts.InstallMethod,
			Hint:   managedInstallHint(opts.InstallMethod),
		}
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = maxBytesDefault
	}

	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	// Normalise TargetTag: accept "X.Y.Z" or "vX.Y.Z".
	targetTag := opts.TargetTag
	if targetTag != "" && targetTag[0] != 'v' {
		targetTag = "v" + targetTag
	}

	// 1. Resolve target release.
	var rel Release
	if targetTag != "" {
		var err error
		rel, err = opts.Fetcher.GetRelease(ctx, targetTag)
		if err != nil {
			return Result{}, err
		}
	} else {
		rels, err := opts.Fetcher.ListReleases(ctx)
		if err != nil {
			return Result{}, err
		}
		allowPre := opts.AllowPrerelease || opts.Current.IsPrerelease
		var ok bool
		rel, ok = SelectRelease(rels, allowPre)
		if !ok {
			return Result{}, &ErrNoRelease{AllowPrerelease: allowPre}
		}
		targetTag = rel.TagName
	}

	currentTag := opts.Current.Tag
	res := Result{TargetTag: targetTag, CurrentTag: currentTag}

	// 2. Compare current vs target (skip comparison when current is Unknown).
	if !opts.Current.Unknown && currentTag != "" {
		cmp := semverCompare(currentTag, targetTag)
		if cmp == 0 {
			res.AlreadyAtTarget = true
			return res, nil
		}
		if cmp > 0 {
			if opts.TargetTag != "" {
				// Explicit --version target: downgrade is intentional.
				res.Downgrade = true
			} else {
				// Default resolution never downgrades (plan §2.5). Signal that
				// the running binary is already ahead of the latest release so
				// the caller can print a more accurate message than "already at".
				res.Ahead = true
				return res, nil
			}
		}
	}

	if opts.CheckOnly {
		return res, nil
	}

	// 3. Determine asset names and locate them in the release.
	archiveName, err := AssetName(opts.GOOS, opts.GOARCH)
	if err != nil {
		return Result{}, err
	}
	checksumName := ChecksumAssetName(targetTag)

	archiveAsset, ok := FindAsset(rel, archiveName)
	if !ok {
		return Result{}, &ErrAssetMissing{Name: archiveName}
	}
	checksumAsset, ok := FindAsset(rel, checksumName)
	if !ok {
		return Result{}, &ErrAssetMissing{Name: checksumName}
	}

	workDir := opts.WorkDir
	if workDir == "" {
		workDir = filepath.Dir(opts.ExePath)
	}

	// 4. Download checksum file (no digest gate — it IS the gate).
	if err := validateDownloadURL(checksumAsset.BrowserDownloadURL, opts.testAllowedHosts); err != nil {
		return Result{}, err
	}
	if err := validateDownloadURL(archiveAsset.BrowserDownloadURL, opts.testAllowedHosts); err != nil {
		return Result{}, err
	}
	csPath, err := fetchToTemp(ctx, client, checksumAsset.BrowserDownloadURL, workDir, maxChecksumFileSize)
	if csPath != "" {
		defer os.Remove(csPath)
	}
	if err != nil {
		return Result{}, fmt.Errorf("downloading checksum file: %w", err)
	}

	csFile, err := os.Open(csPath)
	if err != nil {
		return Result{}, err
	}
	checksums, parseErr := ParseChecksums(csFile)
	csFile.Close()
	if parseErr != nil {
		return Result{}, fmt.Errorf("parsing checksums: %w", parseErr)
	}

	wantHex, ok := checksums[archiveName]
	if !ok {
		return Result{}, &ErrChecksumEntryMissing{Name: archiveName}
	}

	// 5. Download archive and verify its SHA-256 against the checksum file.
	archivePath, err := DownloadAndVerify(ctx, client, archiveAsset.BrowserDownloadURL, workDir, wantHex, maxBytes)
	if archivePath != "" {
		defer os.Remove(archivePath)
	}
	if err != nil {
		return Result{}, fmt.Errorf("downloading archive: %w", err)
	}

	// 6. Extract the binary to a temp file in workDir.
	binTmp, err := os.CreateTemp(workDir, ".openshellctl-update-*")
	if err != nil {
		return Result{}, err
	}
	binTmpPath := binTmp.Name()
	binTmp.Close()
	defer os.Remove(binTmpPath)

	if err := ExtractBinary(archivePath, binTmpPath, maxBytes); err != nil {
		return Result{}, err
	}

	// 7. Atomic replace.
	if err := Replace(binTmpPath, opts.ExePath); err != nil {
		return Result{}, err
	}

	res.Updated = true
	return res, nil
}

// validateDownloadURL returns an error if rawURL does not have a host in the
// production allow-list (github.com, objects.githubusercontent.com) or in the
// caller-supplied extraHosts list (used by in-package tests for httptest URLs).
func validateDownloadURL(rawURL string, extraHosts []string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "github.com", "objects.githubusercontent.com":
		return nil
	}
	for _, h := range extraHosts {
		if host == strings.ToLower(h) {
			return nil
		}
	}
	return fmt.Errorf("download URL has unexpected host %q: must be github.com or objects.githubusercontent.com", host)
}

// managedInstallHint returns a per-method instruction for the user.
func managedInstallHint(method string) string {
	switch method {
	case "container":
		return "rebuild the image with a newer OPENSHELLCTL_VERSION"
	default:
		return fmt.Sprintf("update via the %s package manager instead", method)
	}
}

// fetchToTemp downloads url into a temp file under dir without digest
// verification. Used for files that are themselves the integrity reference
// (the checksums.txt). Returns the temp-file path on success; the caller
// must remove it.
func fetchToTemp(ctx context.Context, client *http.Client, url, dir string, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}

	tmp, err := os.CreateTemp(dir, ".openshellctl-cs-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()

	n, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		os.Remove(path)
		return "", copyErr
	}
	if closeErr != nil {
		os.Remove(path)
		return "", closeErr
	}
	if n > maxBytes {
		os.Remove(path)
		return "", fmt.Errorf("%s exceeded size limit of %d bytes", url, maxBytes)
	}
	return path, nil
}

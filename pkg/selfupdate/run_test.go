package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// srvHosts extracts the hostname from srv.URL for use in testAllowedHosts.
func srvHosts(srv *httptest.Server) []string {
	u, _ := url.Parse(srv.URL)
	return []string{u.Hostname()}
}

// fakeFetcher implements Fetcher for tests.
type fakeFetcher struct {
	releases []Release
	byTag    map[string]Release
	listErr  error
	getErr   error
}

func (f *fakeFetcher) ListReleases(_ context.Context) ([]Release, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.releases, nil
}

func (f *fakeFetcher) GetRelease(_ context.Context, tag string) (Release, error) {
	if f.getErr != nil {
		return Release{}, f.getErr
	}
	if rel, ok := f.byTag[tag]; ok {
		return rel, nil
	}
	return Release{}, &ErrTagNotFound{Tag: tag}
}

// testReleaseServer builds a tiny valid release archive and checksum file,
// serves them via httptest.Server, and returns a Release whose asset URLs
// point at that server plus the server itself (caller must Close it).
func testReleaseServer(t *testing.T, tag, goos, goarch string) (Release, *httptest.Server) {
	t.Helper()

	archiveName, err := AssetName(goos, goarch)
	if err != nil {
		t.Fatalf("AssetName: %v", err)
	}
	checksumName := ChecksumAssetName(tag)

	// Build a minimal tar.gz containing "openshellctl"
	var archiveBuf []byte
	{
		content := []byte("fake binary " + tag)
		var buf []byte
		tmpF, _ := os.CreateTemp(t.TempDir(), "*.tar.gz")
		gw := gzip.NewWriter(tmpF)
		tw := tar.NewWriter(gw)
		hdr := &tar.Header{Name: "openshellctl", Typeflag: tar.TypeReg, Size: int64(len(content)), Mode: 0o755}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(content)
		tw.Close()
		gw.Close()
		tmpF.Close()
		buf, _ = os.ReadFile(tmpF.Name())
		archiveBuf = buf
	}
	h := sha256.Sum256(archiveBuf)
	digest := hex.EncodeToString(h[:])
	checksumContent := digest + "  " + archiveName + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/"+archiveName, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archiveBuf)
	})
	mux.HandleFunc("/"+checksumName, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(checksumContent))
	})
	srv := httptest.NewServer(mux)

	rel := Release{
		TagName:    tag,
		Draft:      false,
		Prerelease: false,
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: checksumName, BrowserDownloadURL: srv.URL + "/" + checksumName},
		},
	}
	return rel, srv
}

func TestRun_HappyPath(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.6", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("old binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("v0.1.5"),
		Fetcher:          &fakeFetcher{releases: []Release{rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Updated {
		t.Error("result.Updated = false, want true")
	}
	if result.TargetTag != "v0.1.6" {
		t.Errorf("result.TargetTag = %q, want v0.1.6", result.TargetTag)
	}
}

func TestRun_CheckOnlyNoDownload(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.6", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("old binary"), 0o755)

	opts := Options{
		GOOS:       "linux",
		GOARCH:     "amd64",
		Current:    ParseCurrent("v0.1.5"),
		Fetcher:    &fakeFetcher{releases: []Release{rel}},
		WorkDir:    dir,
		ExePath:    exePath,
		MaxBytes:   256 << 20,
		CheckOnly:  true,
		HTTPClient: srv.Client(),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run --check: %v", err)
	}
	if result.Updated {
		t.Error("Updated should be false for --check mode")
	}
	if result.TargetTag != "v0.1.6" {
		t.Errorf("TargetTag = %q, want v0.1.6", result.TargetTag)
	}
	got, _ := os.ReadFile(exePath)
	if string(got) != "old binary" {
		t.Error("binary should not be replaced in --check mode")
	}
}

func TestRun_SameVersionNoOp(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.5", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("same binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("v0.1.5"),
		Fetcher:          &fakeFetcher{releases: []Release{rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run same version: %v", err)
	}
	if result.Updated {
		t.Error("Updated should be false for same-version")
	}
	if !result.AlreadyAtTarget {
		t.Error("AlreadyAtTarget should be true for same-version")
	}
}

func TestRun_DowngradeViaVersionFlag(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.4", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("newer binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("v0.1.5"),
		Fetcher:          &fakeFetcher{byTag: map[string]Release{"v0.1.4": rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		TargetTag:        "v0.1.4",
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run downgrade: %v", err)
	}
	if !result.Updated {
		t.Error("Updated should be true for downgrade via --version")
	}
}

func TestRun_DefaultResolutionNeverDowngrades(t *testing.T) {
	// Default resolution (no --version) should return Ahead when the best
	// available release is older than the running binary (plan §2.5).
	rel, srv := testReleaseServer(t, "v0.1.4", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("newer binary"), 0o755)

	opts := Options{
		GOOS:     "linux",
		GOARCH:   "amd64",
		Current:  ParseCurrent("v0.1.5"),
		Fetcher:  &fakeFetcher{releases: []Release{rel}},
		WorkDir:  dir,
		ExePath:  exePath,
		MaxBytes: 256 << 20,
		// TargetTag intentionally empty — default resolution path.
		HTTPClient: srv.Client(),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Updated {
		t.Error("Updated should be false: default resolution must not downgrade")
	}
	if !result.Ahead {
		t.Errorf("Ahead should be true when best release < current; got AlreadyAtTarget=%v Downgrade=%v Updated=%v",
			result.AlreadyAtTarget, result.Downgrade, result.Updated)
	}
}

func TestRun_ManagedInstallRefused(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		GOOS:          "linux",
		GOARCH:        "amd64",
		Current:       ParseCurrent("v0.1.5"),
		Fetcher:       &fakeFetcher{},
		WorkDir:       dir,
		ExePath:       filepath.Join(dir, "openshellctl"),
		MaxBytes:      256 << 20,
		InstallMethod: "container",
	}
	_, err := Run(context.Background(), opts)
	var managed *ErrManagedInstall
	if !errors.As(err, &managed) {
		t.Errorf("Run managed install error = %T(%v), want *ErrManagedInstall", err, err)
	}
}

func TestRun_UnknownCurrentWithTargetTag(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.5", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("dev binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("dev"),
		Fetcher:          &fakeFetcher{byTag: map[string]Release{"v0.1.5": rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		TargetTag:        "v0.1.5",
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run from dev with --version: %v", err)
	}
	if !result.Updated {
		t.Error("should update from dev to explicit --version")
	}
}

func TestRun_AssetMissing(t *testing.T) {
	// Release exists but has no assets at all.
	rel := Release{TagName: "v0.1.6", Draft: false}

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("old binary"), 0o755)

	opts := Options{
		GOOS:       "linux",
		GOARCH:     "amd64",
		Current:    ParseCurrent("v0.1.5"),
		Fetcher:    &fakeFetcher{releases: []Release{rel}},
		WorkDir:    dir,
		ExePath:    exePath,
		MaxBytes:   256 << 20,
		HTTPClient: &http.Client{},
	}
	_, err := Run(context.Background(), opts)
	var missing *ErrAssetMissing
	if !errors.As(err, &missing) {
		t.Errorf("Run with no assets: error = %T(%v), want *ErrAssetMissing", err, err)
	}
}

func TestRun_ChecksumEntryMissing(t *testing.T) {
	// Release has the archive asset but the checksum file omits the entry for it.
	tag := "v0.1.6"
	archiveName, _ := AssetName("linux", "amd64")
	checksumName := ChecksumAssetName(tag)
	archiveContent := []byte("fake archive")

	mux := http.NewServeMux()
	mux.HandleFunc("/"+archiveName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveContent)
	})
	// Checksum file intentionally has no entry for the archive.
	mux.HandleFunc("/"+checksumName, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  other_file.tar.gz\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rel := Release{
		TagName: tag,
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: checksumName, BrowserDownloadURL: srv.URL + "/" + checksumName},
		},
	}

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("old binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("v0.1.5"),
		Fetcher:          &fakeFetcher{releases: []Release{rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	_, err := Run(context.Background(), opts)
	var missing *ErrChecksumEntryMissing
	if !errors.As(err, &missing) {
		t.Errorf("Run with missing checksum entry: error = %T(%v), want *ErrChecksumEntryMissing", err, err)
	}
}

func TestRun_UnsupportedPlatform(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		GOOS:       "windows",
		GOARCH:     "amd64",
		Current:    ParseCurrent("v0.1.5"),
		Fetcher:    &fakeFetcher{releases: []Release{{TagName: "v0.1.6"}}},
		WorkDir:    dir,
		ExePath:    filepath.Join(dir, "openshellctl"),
		MaxBytes:   256 << 20,
		HTTPClient: &http.Client{},
	}
	_, err := Run(context.Background(), opts)
	var unsupported *ErrUnsupportedPlatform
	if !errors.As(err, &unsupported) {
		t.Errorf("Run with windows/amd64: error = %T(%v), want *ErrUnsupportedPlatform", err, err)
	}
}

func TestRun_TempFileCleanup(t *testing.T) {
	rel, srv := testReleaseServer(t, "v0.1.6", "linux", "amd64")
	defer srv.Close()

	dir := t.TempDir()
	exePath := filepath.Join(dir, "openshellctl")
	_ = os.WriteFile(exePath, []byte("old binary"), 0o755)

	opts := Options{
		GOOS:             "linux",
		GOARCH:           "amd64",
		Current:          ParseCurrent("v0.1.5"),
		Fetcher:          &fakeFetcher{releases: []Release{rel}},
		WorkDir:          dir,
		ExePath:          exePath,
		MaxBytes:         256 << 20,
		HTTPClient:       srv.Client(),
		testAllowedHosts: srvHosts(srv),
	}
	_, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	entries, _ := fs.ReadDir(os.DirFS(dir), ".")
	for _, e := range entries {
		if e.Name() != "openshellctl" {
			t.Errorf("unexpected file left in WorkDir: %q", e.Name())
		}
	}
}

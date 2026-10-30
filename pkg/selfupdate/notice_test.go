package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldCheck_AllConditionsMet(t *testing.T) {
	in := CheckInputs{
		Command:        "sandbox list",
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CI:             "",
		CurrentVersion: ParseCurrent("v0.1.5"),
		CacheAge:       25 * time.Hour, // older than 24 h → should check
	}
	if !ShouldCheck(in) {
		t.Error("ShouldCheck = false, want true when all conditions met")
	}
}

func TestShouldCheck_NoUpdateCheckFlag(t *testing.T) {
	in := CheckInputs{
		Command:        "sandbox list",
		NoUpdateCheck:  true,
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CurrentVersion: ParseCurrent("v0.1.5"),
		CacheAge:       25 * time.Hour,
	}
	if ShouldCheck(in) {
		t.Error("ShouldCheck should be false when --no-update-check is set")
	}
}

func TestShouldCheck_UpdateCommandExcluded(t *testing.T) {
	for _, cmd := range []string{"update", "version", "completion", "__complete", "help"} {
		in := CheckInputs{
			Command:        cmd,
			StdoutIsTTY:    true,
			StderrIsTTY:    true,
			CurrentVersion: ParseCurrent("v0.1.5"),
			CacheAge:       25 * time.Hour,
		}
		if ShouldCheck(in) {
			t.Errorf("ShouldCheck should be false for command %q", cmd)
		}
	}
}

func TestShouldCheck_NotATTY(t *testing.T) {
	tests := []struct {
		stdout, stderr bool
		want           bool
	}{
		{true, true, true},
		{true, false, false},
		{false, true, false},
		{false, false, false},
	}
	for _, tt := range tests {
		in := CheckInputs{
			Command:        "sandbox list",
			StdoutIsTTY:    tt.stdout,
			StderrIsTTY:    tt.stderr,
			CurrentVersion: ParseCurrent("v0.1.5"),
			CacheAge:       25 * time.Hour,
		}
		got := ShouldCheck(in)
		if got != tt.want {
			t.Errorf("ShouldCheck(stdout=%v stderr=%v) = %v, want %v", tt.stdout, tt.stderr, got, tt.want)
		}
	}
}

func TestShouldCheck_CIEnvSkips(t *testing.T) {
	in := CheckInputs{
		Command:        "sandbox list",
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CI:             "true",
		CurrentVersion: ParseCurrent("v0.1.5"),
		CacheAge:       25 * time.Hour,
	}
	if ShouldCheck(in) {
		t.Error("ShouldCheck should be false when CI env is set")
	}
}

func TestShouldCheck_UnknownVersionSkips(t *testing.T) {
	in := CheckInputs{
		Command:        "sandbox list",
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CurrentVersion: ParseCurrent("dev"),
		CacheAge:       25 * time.Hour,
	}
	if ShouldCheck(in) {
		t.Error("ShouldCheck should be false for Unknown version (dev)")
	}
}

func TestShouldCheck_CacheWithin24h(t *testing.T) {
	in := CheckInputs{
		Command:        "sandbox list",
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CurrentVersion: ParseCurrent("v0.1.5"),
		CacheAge:       12 * time.Hour, // within 24 h → do not check again
	}
	if ShouldCheck(in) {
		t.Error("ShouldCheck should be false when cache is within 24 h")
	}
}

func TestShouldCheck_CacheExact24h(t *testing.T) {
	// At exactly 24h it is stale.
	in := CheckInputs{
		Command:        "sandbox list",
		StdoutIsTTY:    true,
		StderrIsTTY:    true,
		CurrentVersion: ParseCurrent("v0.1.5"),
		CacheAge:       24 * time.Hour,
	}
	if !ShouldCheck(in) {
		t.Error("ShouldCheck should be true when cache is exactly 24 h old")
	}
}

func TestNoticeText_NewVersionAvailable(t *testing.T) {
	txt := NoticeText("v0.1.5", "v0.1.6", false)
	if txt == "" {
		t.Error("NoticeText returned empty string for newer version")
	}
	for _, want := range []string{"v0.1.5", "v0.1.6", "openshellctl update"} {
		if !strings.Contains(txt, want) {
			t.Errorf("NoticeText missing %q: %s", want, txt)
		}
	}
}

func TestNoticeText_SameVersion(t *testing.T) {
	txt := NoticeText("v0.1.5", "v0.1.5", false)
	if txt != "" {
		t.Errorf("NoticeText should be empty when versions match, got %q", txt)
	}
}

func TestNoticeText_OlderLatest(t *testing.T) {
	// If the cached latest is older than current, no notice.
	txt := NoticeText("v0.1.5", "v0.1.4", false)
	if txt != "" {
		t.Errorf("NoticeText should be empty when latest < current, got %q", txt)
	}
}

func TestCacheRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")

	c := Cache{
		CheckedAt: time.Now().UTC().Truncate(time.Second),
		LatestTag: "v0.1.6",
	}
	if err := SaveCache(path, c); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	// Verify file permissions.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cache file perm = %o, want 0600", info.Mode().Perm())
	}

	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got.LatestTag != "v0.1.6" {
		t.Errorf("LatestTag = %q, want v0.1.6", got.LatestTag)
	}
}

func TestLoadCache_CorruptTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	_ = os.WriteFile(path, []byte("not json"), 0o600)

	_, err := LoadCache(path)
	if err == nil {
		t.Error("expected error for corrupt cache, got nil")
	}
}

func TestLoadCache_AbsentReturnsZero(t *testing.T) {
	_, err := LoadCache("/nonexistent/path/update-check.json")
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist, got %v", err)
	}
}

func TestSaveCache_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "update-check.json")

	c := Cache{CheckedAt: time.Now(), LatestTag: "v0.1.5"}
	if err := SaveCache(path, c); err != nil {
		t.Fatalf("SaveCache with nested dir: %v", err)
	}
	// Verify parent dir exists with 0700 permission
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("cache dir perm = %o, want 0700", info.Mode().Perm())
	}
}

func TestSaveCache_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")

	// Write initial content so we can verify it's replaced atomically.
	initial := Cache{CheckedAt: time.Now(), LatestTag: "v0.1.5"}
	_ = SaveCache(path, initial)

	updated := Cache{CheckedAt: time.Now(), LatestTag: "v0.1.6"}
	if err := SaveCache(path, updated); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var got Cache
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LatestTag != "v0.1.6" {
		t.Errorf("LatestTag = %q after overwrite, want v0.1.6", got.LatestTag)
	}
}

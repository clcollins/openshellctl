package sandbox

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// fakeFileInfo implements fs.FileInfo for stat stubs.
type fakeFileInfo struct{ dir bool }

func (f fakeFileInfo) Name() string       { return "" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

func TestResolveImage(t *testing.T) {
	// statSet: paths that exist (value=isDir).
	mkStat := func(paths map[string]bool) func(string) (fs.FileInfo, error) {
		return func(p string) (fs.FileInfo, error) {
			if dir, ok := paths[p]; ok {
				return fakeFileInfo{dir: dir}, nil
			}
			return nil, fs.ErrNotExist
		}
	}

	t.Run("dockerfile file → unsupported", func(t *testing.T) {
		_, err := ResolveImage("./Dockerfile", "", mkStat(map[string]bool{"./Dockerfile": false}))
		var e *ErrLocalBuildUnsupported
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want ErrLocalBuildUnsupported", err)
		}
	})
	t.Run("dir with Dockerfile → unsupported", func(t *testing.T) {
		_, err := ResolveImage("mydir", "", mkStat(map[string]bool{"mydir": true, "mydir/Dockerfile": false}))
		var e *ErrLocalBuildUnsupported
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want ErrLocalBuildUnsupported", err)
		}
	})
	t.Run("dir without Dockerfile → no dockerfile", func(t *testing.T) {
		_, err := ResolveImage("mydir", "", mkStat(map[string]bool{"mydir": true}))
		var e *ErrNoDockerfile
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want ErrNoDockerfile", err)
		}
	})
	t.Run("local path missing", func(t *testing.T) {
		_, err := ResolveImage("./nope", "", mkStat(nil))
		var e *ErrLocalPathMissing
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want ErrLocalPathMissing", err)
		}
	})
	t.Run("verbatim image ref", func(t *testing.T) {
		img, err := ResolveImage("ghcr.io/x/y:latest", "", mkStat(nil))
		if err != nil || img != "ghcr.io/x/y:latest" {
			t.Fatalf("img=%q err=%v", img, err)
		}
	})
	t.Run("bare community name", func(t *testing.T) {
		img, err := ResolveImage("python", "", mkStat(nil))
		if err != nil {
			t.Fatal(err)
		}
		if img != DefaultCommunityRegistry+"/python:latest" {
			t.Errorf("img = %q", img)
		}
	})
	t.Run("custom registry", func(t *testing.T) {
		img, _ := ResolveImage("python", "reg.example/sb", mkStat(nil))
		if img != "reg.example/sb/python:latest" {
			t.Errorf("img = %q", img)
		}
	})
}

func TestValidateCPU(t *testing.T) {
	ok := []string{"2", "0.5", "500m", "1"}
	for _, v := range ok {
		if err := ValidateCPU(v); err != nil {
			t.Errorf("ValidateCPU(%q) = %v, want nil", v, err)
		}
	}
	bad := map[string]string{
		"":    "--cpu must not be empty",
		"abc": "invalid --cpu value 'abc'",
		"0":   "--cpu must be greater than zero",
		"0m":  "--cpu must be greater than zero",
		"-1":  "--cpu must be greater than zero",
	}
	for v, sub := range bad {
		err := ValidateCPU(v)
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Errorf("ValidateCPU(%q) = %v, want containing %q", v, err, sub)
		}
	}
}

func TestValidateMemory(t *testing.T) {
	ok := []string{"512Mi", "4Gi", "8G", "1024", "1Ki"}
	for _, v := range ok {
		if err := ValidateMemory(v); err != nil {
			t.Errorf("ValidateMemory(%q) = %v, want nil", v, err)
		}
	}
	bad := map[string]string{
		"":      "--memory must not be empty",
		"abc":   "invalid --memory value 'abc'",
		"0":     "--memory must be greater than zero",
		"0Mi":   "--memory must be greater than zero",
		"1.5Gi": "invalid --memory value",
	}
	for v, sub := range bad {
		err := ValidateMemory(v)
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Errorf("ValidateMemory(%q) = %v, want containing %q", v, err, sub)
		}
	}
}

func TestResourcesStruct(t *testing.T) {
	if ResourcesStruct("", "") != nil {
		t.Error("both empty should be nil")
	}
	got := ResourcesStruct("2", "512Mi")
	limits := got["limits"].(map[string]any)
	if limits["cpu"] != "2" || limits["memory"] != "512Mi" {
		t.Errorf("got %v", got)
	}
	cpuOnly := ResourcesStruct("2", "")
	if _, ok := cpuOnly["limits"].(map[string]any)["memory"]; ok {
		t.Error("memory should be omitted when empty")
	}
}

func TestParseEnvPairs(t *testing.T) {
	got, err := ParseEnvPairs([]string{"FOO=bar", "BAZ=a=b"})
	if err != nil {
		t.Fatal(err)
	}
	if got["FOO"] != "bar" || got["BAZ"] != "a=b" {
		t.Errorf("got %v", got)
	}
	bad := map[string]string{
		"noequals":      "--env expects KEY=VALUE",
		"=v":            "--env key cannot be empty",
		"1BAD=v":        "must match",
		"OPENSHELL_X=v": "reserved",
	}
	for item, sub := range bad {
		_, err := ParseEnvPairs([]string{item})
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Errorf("ParseEnvPairs(%q) = %v, want %q", item, err, sub)
		}
	}
}

func TestParseLabels(t *testing.T) {
	got, err := ParseLabels([]string{"  key =val"})
	if err != nil {
		t.Fatal(err)
	}
	if got["  key "] != "val" {
		t.Errorf("labels should not trim: got %v", got)
	}
	if _, err := ParseLabels([]string{"noequals"}); err == nil {
		t.Error("expected error for missing =")
	}
}

func TestCredentialLikeKeys(t *testing.T) {
	env := map[string]string{
		"MY_ACCESS_KEY":          "x", // yes (ACCESS KEY window)
		"PRIMARY_KEY":            "x", // no
		"TOKENIZERS_PARALLELISM": "x", // no
		"API_KEY":                "x", // yes
		"GITHUB_TOKEN":           "x", // yes (builtin profile + TOKEN)
		"HARMLESS":               "x", // no
	}
	got := CredentialLikeKeys(env)
	found := map[string]bool{}
	for _, w := range got {
		found[w.Key] = true
	}
	if !found["MY_ACCESS_KEY"] || !found["API_KEY"] || !found["GITHUB_TOKEN"] {
		t.Errorf("missing expected credential keys: %v", found)
	}
	if found["PRIMARY_KEY"] || found["TOKENIZERS_PARALLELISM"] || found["HARMLESS"] {
		t.Errorf("false positive: %v", found)
	}
	// Sorted by key.
	for i := 1; i < len(got); i++ {
		if got[i-1].Key > got[i].Key {
			t.Errorf("not sorted: %v", got)
		}
	}
}

func TestFormatCredentialWarning(t *testing.T) {
	plain := FormatCredentialWarning(CredentialWarning{Key: "SECRET_TOKEN"})
	if !strings.Contains(plain, "SECRET_TOKEN") || !strings.Contains(plain, "use a provider instead of --env") {
		t.Errorf("plain warning = %q", plain)
	}
	withSugg := FormatCredentialWarning(CredentialWarning{
		Key:         "GITHUB_TOKEN",
		Suggestions: []CredentialSuggestion{{Type: "github", Key: "GITHUB_TOKEN"}},
	})
	if !strings.Contains(withSugg, "provider create --name my-github") {
		t.Errorf("suggestion warning = %q", withSugg)
	}
}

func TestNormalizeProviderType(t *testing.T) {
	cases := map[string]string{
		"claude":      "claude-code",
		"claude_code": "claude-code",
		"gh":          "github",
		"glab":        "gitlab",
		"vertex":      "google-vertex-ai",
		"gcp-vertex":  "google-vertex-ai",
		"gcp":         "google-cloud",
		"  OpenAI  ":  "openai",
	}
	for in, want := range cases {
		got, ok := NormalizeProviderType(in)
		if !ok || got != want {
			t.Errorf("NormalizeProviderType(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeProviderType("nonsense"); ok {
		t.Error("nonsense should not normalize")
	}
}

func TestResolveProviders(t *testing.T) {
	known := []*types.Provider{
		{Name: "my-openai", Type: "openai"},
		{Name: "gh-1", Type: "github"},
	}
	t.Run("existing name kept", func(t *testing.T) {
		res, err := ResolveProviders(known, []string{"my-openai"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Names) != 1 || res.Names[0] != "my-openai" {
			t.Errorf("names = %v", res.Names)
		}
	})
	t.Run("type alias → missing", func(t *testing.T) {
		res, err := ResolveProviders(known, []string{"anthropic"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MissingTypes) != 1 || res.MissingTypes[0] != "anthropic" {
			t.Errorf("missing = %v", res.MissingTypes)
		}
	})
	t.Run("unknown → error", func(t *testing.T) {
		_, err := ResolveProviders(known, []string{"bogus"}, nil)
		var e *ErrProviderNotFound
		if !errors.As(err, &e) {
			t.Fatalf("err = %v, want ErrProviderNotFound", err)
		}
	})
	t.Run("inferred type resolves to existing name", func(t *testing.T) {
		res, err := ResolveProviders(known, nil, []string{"openai"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Names) != 1 || res.Names[0] != "my-openai" {
			t.Errorf("names = %v", res.Names)
		}
	})
	t.Run("inferred type unresolved → missing", func(t *testing.T) {
		res, err := ResolveProviders(known, nil, []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.MissingTypes) != 1 || res.MissingTypes[0] != "codex" {
			t.Errorf("missing = %v", res.MissingTypes)
		}
	})
}

func TestParseForwardSpec(t *testing.T) {
	cases := []struct {
		in   string
		bind string
		port uint16
	}{
		{"8080", "127.0.0.1", 8080},
		{"0.0.0.0:9090", "0.0.0.0", 9090},
		{":3000", "127.0.0.1", 3000},
	}
	for _, tc := range cases {
		got, err := ParseForwardSpec(tc.in)
		if err != nil {
			t.Fatalf("ParseForwardSpec(%q): %v", tc.in, err)
		}
		if got.Bind != tc.bind || got.Port != tc.port {
			t.Errorf("ParseForwardSpec(%q) = %+v, want bind=%s port=%d", tc.in, got, tc.bind, tc.port)
		}
	}
	for _, bad := range []string{"abc", "0", "127.0.0.1:0", "70000"} {
		if _, err := ParseForwardSpec(bad); err == nil {
			t.Errorf("ParseForwardSpec(%q) should error", bad)
		}
	}
}

func TestForwardAccessURL(t *testing.T) {
	f := ForwardSpec{Bind: "0.0.0.0", Port: 8080}
	if f.AccessURL() != "http://localhost:8080/" {
		t.Errorf("AccessURL = %q", f.AccessURL())
	}
}

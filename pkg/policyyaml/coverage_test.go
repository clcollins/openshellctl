package policyyaml

import (
	"strings"
	"testing"
	"testing/fstest"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

func TestLoad(t *testing.T) {
	fsys := fstest.MapFS{
		"policy.yaml": {Data: []byte("version: 1\n")},
	}
	// Explicit path.
	p, ok, err := Load("policy.yaml", nil, fsys)
	if err != nil || !ok || p.Version != 1 {
		t.Fatalf("Load(path): p=%v ok=%v err=%v", p, ok, err)
	}

	// Env fallback.
	env := func(k string) string {
		if k == "OPENSHELL_SANDBOX_POLICY" {
			return "policy.yaml"
		}
		return ""
	}
	p, ok, err = Load("", env, fsys)
	if err != nil || !ok || p.Version != 1 {
		t.Fatalf("Load(env): p=%v ok=%v err=%v", p, ok, err)
	}

	// None configured.
	if p, ok, err := Load("", func(string) string { return "" }, fsys); p != nil || ok || err != nil {
		t.Fatalf("Load(none): p=%v ok=%v err=%v", p, ok, err)
	}

	// Missing file.
	if _, _, err := Load("missing.yaml", nil, fsys); err == nil || !strings.Contains(err.Error(), "failed to read sandbox policy from missing.yaml") {
		t.Fatalf("Load(missing) err = %v", err)
	}
}

func TestQueryMatcher_MarshalRoundTrip(t *testing.T) {
	glob := QueryMatcher{Glob: "NVIDIA/*"}
	b, _ := glob.MarshalJSON()
	if string(b) != `"NVIDIA/*"` {
		t.Errorf("glob marshal = %s", b)
	}
	any := QueryMatcher{IsAny: true, Any: []string{"a", "b"}}
	b, _ = any.MarshalJSON()
	if string(b) != `{"any":["a","b"]}` {
		t.Errorf("any marshal = %s", b)
	}
	// Round-trip decode.
	var back QueryMatcher
	if err := back.UnmarshalJSON(b); err != nil || !back.IsAny || len(back.Any) != 2 {
		t.Errorf("round-trip: %+v err=%v", back, err)
	}
}

func TestQueryMatcher_UnknownKeyRejected(t *testing.T) {
	var m QueryMatcher
	if err := m.UnmarshalJSON([]byte(`{"nope":[]}`)); err == nil {
		t.Error("expected rejection of unknown key in {any}")
	}
}

func TestParamMatcher_MarshalRoundTrip(t *testing.T) {
	// Nested object.
	obj := ParamMatcher{Object: map[string]ParamMatcher{
		"path": {Matcher: &QueryMatcher{Glob: "/tmp/*"}},
	}}
	b, err := obj.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back ParamMatcher
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	if back.Object == nil || back.Object["path"].Matcher.Glob != "/tmp/*" {
		t.Errorf("param round-trip: %+v", back)
	}
}

func TestParamMatcher_EmptyMarshalErrors(t *testing.T) {
	var empty ParamMatcher
	if _, err := empty.MarshalJSON(); err == nil {
		t.Error("empty ParamMatcher should not marshal")
	}
}

func TestToSDK_FullEndpointFields(t *testing.T) {
	y := `
version: 1
filesystem_policy:
  include_workdir: true
  read_only: ["/etc"]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
network_policies:
  p:
    endpoints:
      - host: h
        port: 443
        protocol: rest
        tls: terminate
        enforcement: enforce
        allowed_ips: ["10.0.0.1"]
        allow_encoded_slash: true
        credential_signing: sigv4
        signing_service: s3
        signing_region: us-east-1
        credential_binding:
          provider: my-provider
        deny_rules:
          - method: DELETE
            path: /admin
`
	p, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if p.Filesystem == nil || !p.Filesystem.IncludeWorkdir {
		t.Error("filesystem not converted")
	}
	if p.Landlock == nil || p.Landlock.Compatibility != "best_effort" {
		t.Error("landlock not converted")
	}
	if p.Process == nil || p.Process.RunAsUser != "sandbox" {
		t.Error("process not converted")
	}
	e := p.NetworkPolicies["p"].Endpoints[0]
	if e.TLS != types.NetworkTLSModeTerminate || e.Enforcement != types.NetworkEnforcementModeEnforce || !e.AllowEncodedSlash {
		t.Errorf("endpoint scalars wrong: TLS=%v Enforcement=%v", e.TLS, e.Enforcement)
	}
	if e.CredentialBinding == nil || e.CredentialBinding.Provider != "my-provider" {
		t.Error("credential_binding not converted")
	}
	if len(e.DenyRules) != 1 || e.DenyRules[0].Method != "DELETE" {
		t.Errorf("deny rules wrong: %+v", e.DenyRules)
	}
	if len(e.AllowedIPs) != 1 || e.AllowedIPs[0] != "10.0.0.1" {
		t.Errorf("allowed_ips wrong: %v", e.AllowedIPs)
	}
}

func TestTLSModeConversion(t *testing.T) {
	cases := []struct {
		str  string
		want types.NetworkTLSMode
	}{
		{"", types.NetworkTLSModeUnspecified},
		{"skip", types.NetworkTLSModeSkip},
		{"terminate", types.NetworkTLSModeTerminate},
		{"passthrough", types.NetworkTLSModePassthrough},
		{"unknown", types.NetworkTLSModeUnspecified},
	}
	for _, tc := range cases {
		if got := tlsModeToSDK(tc.str); got != tc.want {
			t.Errorf("tlsModeToSDK(%q) = %v, want %v", tc.str, got, tc.want)
		}
	}
	for _, tc := range cases {
		if tc.str == "unknown" {
			continue
		}
		if got := tlsModeFromSDK(tc.want); got != tc.str {
			t.Errorf("tlsModeFromSDK(%v) = %q, want %q", tc.want, got, tc.str)
		}
	}
}

func TestEnforcementModeConversion(t *testing.T) {
	cases := []struct {
		str  string
		want types.NetworkEnforcementMode
	}{
		{"", types.NetworkEnforcementModeUnspecified},
		{"enforce", types.NetworkEnforcementModeEnforce},
		{"audit", types.NetworkEnforcementModeAudit},
		{"unknown", types.NetworkEnforcementModeUnspecified},
	}
	for _, tc := range cases {
		if got := enforcementToSDK(tc.str); got != tc.want {
			t.Errorf("enforcementToSDK(%q) = %v, want %v", tc.str, got, tc.want)
		}
	}
	for _, tc := range cases {
		if tc.str == "unknown" {
			continue
		}
		if got := enforcementFromSDK(tc.want); got != tc.str {
			t.Errorf("enforcementFromSDK(%v) = %q, want %q", tc.want, got, tc.str)
		}
	}
}

func TestAccessPresetConversion(t *testing.T) {
	cases := []struct {
		str  string
		want types.NetworkAccessPreset
	}{
		{"", types.NetworkAccessPresetUnspecified},
		{"read_only", types.NetworkAccessPresetReadOnly},
		{"read_write", types.NetworkAccessPresetReadWrite},
		{"full", types.NetworkAccessPresetFull},
		{"unknown", types.NetworkAccessPresetUnspecified},
	}
	for _, tc := range cases {
		if got := accessPresetToSDK(tc.str); got != tc.want {
			t.Errorf("accessPresetToSDK(%q) = %v, want %v", tc.str, got, tc.want)
		}
	}
	for _, tc := range cases {
		if tc.str == "unknown" {
			continue
		}
		if got := accessPresetFromSDK(tc.want); got != tc.str {
			t.Errorf("accessPresetFromSDK(%v) = %q, want %q", tc.want, got, tc.str)
		}
	}
}

func TestToSDK_Middleware(t *testing.T) {
	y := `
version: 1
network_middlewares:
  guard:
    middleware: content-guard
    order: 10
    config:
      mode: redact
    on_error: fail_closed
    endpoints:
      include: ["httpbin.org"]
`
	p, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	mw := p.NetworkMiddlewares["guard"]
	if mw.Name != "guard" {
		t.Errorf("name should default from key, got %q", mw.Name)
	}
	if mw.Middleware != "content-guard" || mw.Order != 10 || mw.OnError != "fail_closed" {
		t.Errorf("middleware fields wrong: %+v", mw)
	}
	if mw.Config["mode"] != "redact" {
		t.Errorf("config wrong: %v", mw.Config)
	}
	if mw.Endpoints == nil || len(mw.Endpoints.Include) != 1 {
		t.Errorf("endpoints selector wrong: %+v", mw.Endpoints)
	}
}

func TestToSDK_Nil(t *testing.T) {
	if p, err := ToSDK(nil); p != nil || err != nil {
		t.Errorf("ToSDK(nil) = %v, %v", p, err)
	}
}

func TestParse_InlineJSONError(t *testing.T) {
	// A non-serializable inline map (channel) → marshal error path.
	_, err := ParseInline(map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Error("expected marshal error for non-JSON value")
	}
}

func TestLint_PathTooLong(t *testing.T) {
	long := "/" + strings.Repeat("a", 5000)
	p := &types.SandboxPolicy{Filesystem: &types.FilesystemPolicy{ReadOnly: []string{long}}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "path exceeds maximum length") {
		t.Errorf("missing length error: %v", errs)
	}
}

func TestLint_SelectorPatternInvalid(t *testing.T) {
	p := &types.SandboxPolicy{NetworkMiddlewares: map[string]types.NetworkMiddlewareConfig{
		"mw": {Name: "mw", Middleware: "m", Endpoints: &types.MiddlewareEndpointSelector{
			Include: []string{"bad host", "{a,b}.com"},
		}},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "must not contain whitespace") {
		t.Errorf("missing whitespace error: %v", errs)
	}
	if !hasErrContaining(errs, "must not contain brace alternates") {
		t.Errorf("missing brace error: %v", errs)
	}
}

func TestLint_MiddlewareBadOnError(t *testing.T) {
	p := &types.SandboxPolicy{NetworkMiddlewares: map[string]types.NetworkMiddlewareConfig{
		"mw": {Name: "mw", Middleware: "m", OnError: "explode", Endpoints: &types.MiddlewareEndpointSelector{Include: []string{"h"}}},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "invalid on_error 'explode'") {
		t.Errorf("missing on_error error: %v", errs)
	}
}

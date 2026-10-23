package gatewayconfig

import (
	"testing"
	"testing/fstest"
)

func resolvedWithVaultAuth(content string) *Resolved {
	return &Resolved{
		Name: "rosa",
		FS:   fstest.MapFS{"vault_auth.json": {Data: []byte(content)}},
	}
}

func TestLoadVaultAuthConfig_Match(t *testing.T) {
	r := resolvedWithVaultAuth(`{"mount":"osd-sre","path":"rosa-agent","field_prefix":"hypershell"}`)
	cfg, ok := LoadVaultAuthConfig(r)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.Mount != "osd-sre" || cfg.Path != "rosa-agent" || cfg.FieldPrefix != "hypershell" {
		t.Errorf("got %+v, want {osd-sre rosa-agent hypershell}", cfg)
	}
}

func TestLoadVaultAuthConfig_NoPrefix(t *testing.T) {
	r := resolvedWithVaultAuth(`{"mount":"osd-sre","path":"rosa-agent"}`)
	cfg, ok := LoadVaultAuthConfig(r)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.Mount != "osd-sre" || cfg.Path != "rosa-agent" || cfg.FieldPrefix != "" {
		t.Errorf("got %+v, want {osd-sre rosa-agent \"\"}", cfg)
	}
}

func TestLoadVaultAuthConfig_MissingFile(t *testing.T) {
	r := &Resolved{Name: "rosa", FS: fstest.MapFS{}}
	if _, ok := LoadVaultAuthConfig(r); ok {
		t.Error("expected ok=false for a missing file")
	}
}

func TestLoadVaultAuthConfig_NilResolvedOrFS(t *testing.T) {
	if _, ok := LoadVaultAuthConfig(nil); ok {
		t.Error("expected ok=false for nil Resolved")
	}
	if _, ok := LoadVaultAuthConfig(&Resolved{Name: "rosa"}); ok {
		t.Error("expected ok=false for nil FS")
	}
}

func TestLoadVaultAuthConfig_InvalidJSON(t *testing.T) {
	r := resolvedWithVaultAuth("not json")
	if _, ok := LoadVaultAuthConfig(r); ok {
		t.Error("expected ok=false for unparseable content")
	}
}

func TestLoadVaultAuthConfig_MissingMountOrPath(t *testing.T) {
	for _, content := range []string{
		`{"path":"rosa-agent"}`,
		`{"mount":"osd-sre"}`,
		`{}`,
	} {
		r := resolvedWithVaultAuth(content)
		if _, ok := LoadVaultAuthConfig(r); ok {
			t.Errorf("content %q: expected ok=false when mount or path is missing", content)
		}
	}
}

func TestSaveVaultAuthConfig_RoundTrips(t *testing.T) {
	w := newMemWriter()
	cfg := VaultAuthConfig{Mount: "osd-sre", Path: "rosa-agent", FieldPrefix: "hypershell"}
	if err := SaveVaultAuthConfig(w, "rosa", cfg); err != nil {
		t.Fatal(err)
	}
	data, ok := w.files["gateways/rosa/vault_auth.json"]
	if !ok {
		t.Fatal("expected gateways/rosa/vault_auth.json to be written")
	}
	r := &Resolved{Name: "rosa", FS: fstest.MapFS{"vault_auth.json": {Data: data}}}
	got, ok := LoadVaultAuthConfig(r)
	if !ok {
		t.Fatal("expected ok=true reading back what was just saved")
	}
	if got != cfg {
		t.Errorf("round trip = %+v, want %+v", got, cfg)
	}
}

func TestSaveVaultAuthConfig_NoFieldPrefixOmitsField(t *testing.T) {
	w := newMemWriter()
	if err := SaveVaultAuthConfig(w, "rosa", VaultAuthConfig{Mount: "osd-sre", Path: "rosa-agent"}); err != nil {
		t.Fatal(err)
	}
	got := string(w.files["gateways/rosa/vault_auth.json"])
	if got != `{"mount":"osd-sre","path":"rosa-agent"}` {
		t.Errorf("saved content = %q, want field_prefix omitted", got)
	}
}

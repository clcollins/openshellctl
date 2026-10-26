package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
)

func TestLoadManifest_FileAndStdin(t *testing.T) {
	manifest := `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-1
spec:
  image: ghcr.io/x/y:latest
`
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	m, err := loadManifest(cmd, path)
	if err != nil {
		t.Fatalf("loadManifest(file): %v", err)
	}
	if m.Metadata.Name != "sb-1" {
		t.Errorf("name = %q", m.Metadata.Name)
	}

	// Stdin path.
	cmd2 := &cobra.Command{}
	cmd2.SetIn(strings.NewReader(manifest))
	m2, err := loadManifest(cmd2, "-")
	if err != nil {
		t.Fatalf("loadManifest(stdin): %v", err)
	}
	if m2.Spec.Image != "ghcr.io/x/y:latest" {
		t.Errorf("image = %q", m2.Spec.Image)
	}
}

func TestLoadManifest_InvalidReturnsUsage(t *testing.T) {
	manifest := `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb
spec:
  approvalMode: bogus
`
	dir := t.TempDir()
	path := filepath.Join(dir, "m.yaml")
	_ = os.WriteFile(path, []byte(manifest), 0o600)
	_, err := loadManifest(&cobra.Command{}, path)
	if err == nil || exitCodeFor(err) != ExitUsage {
		t.Fatalf("err = %v exit = %d, want usage", err, exitCodeFor(err))
	}
}

func TestBuildCreateFlags_ImageResolution(t *testing.T) {
	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{from: "python"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Image != "ghcr.io/nvidia/openshell-community/sandboxes/python:latest" {
		t.Errorf("image = %q", f.Image)
	}
}

func TestBuildCreateFlags_BadLabel(t *testing.T) {
	_, err := buildCreateFlags(&cobra.Command{}, createFlagInput{labels: []string{"noequals"}})
	if err == nil || exitCodeFor(err) != ExitUsage {
		t.Fatalf("err = %v exit = %d, want usage", err, exitCodeFor(err))
	}
}

func TestRenderSandbox_Formats(t *testing.T) {
	s := &types.Sandbox{ID: "id", Name: "sb", Status: types.SandboxStatus{Phase: types.SandboxReady}}
	for _, format := range []string{"table", "json", "yaml"} {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := renderSandbox(cmd, s, format); err != nil {
			t.Fatalf("renderSandbox(%s): %v", format, err)
		}
		if !strings.Contains(out.String(), "sb") {
			t.Errorf("%s output missing name: %q", format, out.String())
		}
	}
}

func TestRenderSandboxSlice(t *testing.T) {
	sandboxes := []*types.Sandbox{
		{Name: "a", Status: types.SandboxStatus{Phase: types.SandboxReady}},
		{Name: "b", Status: types.SandboxStatus{Phase: types.SandboxReady}},
	}
	cmd := &cobra.Command{}
	var j bytes.Buffer
	cmd.SetOut(&j)
	if err := renderSandboxSliceJSON(cmd, sandboxes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(j.String(), `"a"`) || !strings.Contains(j.String(), `"b"`) {
		t.Errorf("json slice = %q", j.String())
	}

	cmd2 := &cobra.Command{}
	var y bytes.Buffer
	cmd2.SetOut(&y)
	if err := renderSandboxSliceYAML(cmd2, sandboxes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(y.String(), "name: a") {
		t.Errorf("yaml slice = %q", y.String())
	}
}

func TestGPURequestFlag(t *testing.T) {
	g := &gpuRequestFlag{}
	if err := g.Set("bare"); err != nil {
		t.Fatal(err)
	}
	if !g.set || g.gpu == nil || g.gpu.Count != nil {
		t.Errorf("bare gpu: set=%v gpu=%+v", g.set, g.gpu)
	}
	if g.Type() != "gpuRequest" || g.String() != "bare" {
		t.Errorf("Type=%q String=%q", g.Type(), g.String())
	}

	g2 := &gpuRequestFlag{}
	if err := g2.Set("4"); err != nil {
		t.Fatal(err)
	}
	if g2.gpu.Count == nil || *g2.gpu.Count != 4 {
		t.Errorf("gpu count = %v", g2.gpu.Count)
	}
	if g2.String() != "4" {
		t.Errorf("String = %q", g2.String())
	}

	g3 := &gpuRequestFlag{}
	if err := g3.Set("0"); err == nil {
		t.Error("gpu count 0 should error")
	}
	if err := g3.Set("notanum"); err == nil {
		t.Error("non-numeric gpu should error")
	}
}

func TestBuildCreateFlags_Forward(t *testing.T) {
	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{forward: "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Forward == nil || f.Forward.Port != 8080 || f.Forward.Bind != "127.0.0.1" {
		t.Errorf("forward = %+v", f.Forward)
	}
}

func TestBuildCreateFlags_BadForward(t *testing.T) {
	_, err := buildCreateFlags(&cobra.Command{}, createFlagInput{forward: "notaport"})
	if err == nil || exitCodeFor(err) != ExitUsage {
		t.Fatalf("err = %v exit = %d, want usage", err, exitCodeFor(err))
	}
}

func TestBuildCreateFlags_Upload(t *testing.T) {
	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{
		uploads:     []string{"./local:/remote", "justlocal"},
		noGitIgnore: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Uploads) != 2 {
		t.Fatalf("uploads len = %d", len(f.Uploads))
	}
	if f.Uploads[0].Local != "./local" || f.Uploads[0].Dest != "/remote" {
		t.Errorf("upload[0] = %+v", f.Uploads[0])
	}
	if f.Uploads[1].Local != "justlocal" || f.Uploads[1].Dest != "" {
		t.Errorf("upload[1] = %+v", f.Uploads[1])
	}
	// --no-git-ignore → GitIgnore=false
	if f.Uploads[0].GitIgnore == nil || *f.Uploads[0].GitIgnore {
		t.Errorf("gitignore should be false with --no-git-ignore")
	}
}

func TestBuildCreateFlags_BadDriverConfigJSON(t *testing.T) {
	_, err := buildCreateFlags(&cobra.Command{}, createFlagInput{driverConfigJSON: "not json"})
	if err == nil || exitCodeFor(err) != ExitUsage {
		t.Fatalf("err = %v exit = %d, want usage", err, exitCodeFor(err))
	}
}

func TestBuildCreateFlags_DriverConfigJSON(t *testing.T) {
	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{driverConfigJSON: `{"key":"val"}`})
	if err != nil {
		t.Fatal(err)
	}
	if f.DriverConfig == nil || f.DriverConfig["key"] != "val" {
		t.Errorf("driverConfig = %v", f.DriverConfig)
	}
}

func TestCommandArgs(t *testing.T) {
	// Build a command that records ArgsLenAtDash via parsing.
	cmd := &cobra.Command{Use: "create", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().String("name", "", "")
	cmd.SetArgs([]string{"--name", "x", "--", "echo", "hi"})
	var got []string
	cmd.RunE = func(c *cobra.Command, args []string) error {
		got = commandArgs(c, args)
		return nil
	}
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "echo" || got[1] != "hi" {
		t.Errorf("commandArgs = %v, want [echo hi]", got)
	}
}

func TestLoadPolicy_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(path, []byte("version: 1\nfilesystem_policy:\n  include_workdir: true\n"), 0o600)

	p, err := loadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Version != 1 {
		t.Errorf("policy = %+v", p)
	}
	if p.Filesystem == nil || !p.Filesystem.IncludeWorkdir {
		t.Error("filesystem_policy not parsed")
	}
}

func TestLoadPolicy_MissingFile(t *testing.T) {
	_, err := loadPolicy("/nonexistent/policy.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("missing file should be UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "failed to read policy file") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadPolicy_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(path, []byte("not: valid: yaml: ["), 0o600)

	_, err := loadPolicy(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("err should be UsageError, got %T: %v", err, err)
	}
}

func TestResolveManifestPolicy_Inline(t *testing.T) {
	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{
			Policy: map[string]any{
				"version":           float64(1),
				"filesystem_policy": map[string]any{"include_workdir": true},
			},
		},
	}
	p, err := resolveManifestPolicy(m, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Version != 1 {
		t.Errorf("policy = %+v", p)
	}
	if p.Filesystem == nil || !p.Filesystem.IncludeWorkdir {
		t.Error("inline policy not parsed")
	}
}

func TestResolveManifestPolicy_File(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "strict.yaml")
	_ = os.WriteFile(policyPath, []byte("version: 1\n"), 0o600)
	manifestPath := filepath.Join(dir, "manifest.yaml")

	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: "strict.yaml"},
	}
	p, err := resolveManifestPolicy(m, manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Version != 1 {
		t.Errorf("policy = %+v", p)
	}
}

func TestResolveManifestPolicy_FileAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "abs.yaml")
	_ = os.WriteFile(policyPath, []byte("version: 1\n"), 0o600)

	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: policyPath},
	}
	p, err := resolveManifestPolicy(m, "/some/other/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Version != 1 {
		t.Errorf("policy = %+v", p)
	}
}

func TestResolveManifestPolicy_Nil(t *testing.T) {
	m := &v1alpha1.Sandbox{Spec: v1alpha1.SandboxSpec{Image: "img"}}
	p, err := resolveManifestPolicy(m, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Errorf("expected nil policy, got %+v", p)
	}
}

func TestResolveManifestPolicy_StdinAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	_ = os.WriteFile(policyPath, []byte("version: 1\n"), 0o600)

	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: policyPath},
	}
	p, err := resolveManifestPolicy(m, "-")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.Version != 1 {
		t.Errorf("policy = %+v", p)
	}
}

func TestResolveManifestPolicy_StdinRelativePathErrors(t *testing.T) {
	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: "relative.yaml"},
	}
	_, err := resolveManifestPolicy(m, "-")
	if err == nil {
		t.Fatal("expected error for relative policyFile with stdin manifest")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("err should be UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "must be an absolute path when the manifest is read from stdin") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveManifestPolicy_InlineInvalid(t *testing.T) {
	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{
			Policy: map[string]any{
				"version":       float64(1),
				"unknown_field": "should fail strict decode",
			},
		},
	}
	_, err := resolveManifestPolicy(m, "manifest.yaml")
	if err == nil {
		t.Fatal("expected error for unknown field in inline policy")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("err should be UsageError, got %T: %v", err, err)
	}
}

func TestResolveCreatePolicy(t *testing.T) {
	dir := t.TempDir()
	flagPath := filepath.Join(dir, "flag.yaml")
	_ = os.WriteFile(flagPath, []byte("version: 1\nfilesystem_policy:\n  include_workdir: true\n"), 0o600)
	envPath := filepath.Join(dir, "env.yaml")
	_ = os.WriteFile(envPath, []byte("version: 1\n"), 0o600)
	manifestPolicyPath := filepath.Join(dir, "manifest-policy.yaml")
	_ = os.WriteFile(manifestPolicyPath, []byte("version: 1\nlandlock:\n  compatibility: best_effort\n"), 0o600)
	manifestPath := filepath.Join(dir, "manifest.yaml")

	flagPolicy, _ := loadPolicy(flagPath)
	envFunc := func(k string) string {
		if k == "OPENSHELL_SANDBOX_POLICY" {
			return envPath
		}
		return ""
	}
	noEnv := func(string) string { return "" }

	manifestWithInline := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{
			Policy: map[string]any{"version": float64(1), "landlock": map[string]any{"compatibility": "best_effort"}},
		},
	}
	manifestWithFile := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: "manifest-policy.yaml"},
	}
	manifestNone := &v1alpha1.Sandbox{Spec: v1alpha1.SandboxSpec{Image: "img"}}

	tests := []struct {
		name         string
		flagPolicy   *types.SandboxPolicy
		flagExplicit bool
		manifest     *v1alpha1.Sandbox
		env          func(string) string
		wantErr      string
		wantNil      bool
		checkPolicy  func(*testing.T, *types.SandboxPolicy)
	}{
		{
			name:         "flag only",
			flagPolicy:   flagPolicy,
			flagExplicit: true,
			env:          noEnv,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Filesystem == nil || !p.Filesystem.IncludeWorkdir {
					t.Error("flag policy should have filesystem")
				}
			},
		},
		{
			name: "env only",
			env:  envFunc,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Version != 1 {
					t.Errorf("version = %d", p.Version)
				}
			},
		},
		{
			name:     "manifest inline only",
			manifest: manifestWithInline,
			env:      noEnv,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Landlock == nil || p.Landlock.Compatibility != "best_effort" {
					t.Error("manifest inline policy should have landlock")
				}
			},
		},
		{
			name:     "manifest file only",
			manifest: manifestWithFile,
			env:      noEnv,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Landlock == nil || p.Landlock.Compatibility != "best_effort" {
					t.Error("manifest file policy should have landlock")
				}
			},
		},
		{
			name:         "flag + manifest inline = error",
			flagPolicy:   flagPolicy,
			flagExplicit: true,
			manifest:     manifestWithInline,
			env:          noEnv,
			wantErr:      "mutually exclusive",
		},
		{
			name:         "flag + manifest file = error",
			flagPolicy:   flagPolicy,
			flagExplicit: true,
			manifest:     manifestWithFile,
			env:          noEnv,
			wantErr:      "mutually exclusive",
		},
		{
			name:     "env + manifest inline = manifest wins",
			manifest: manifestWithInline,
			env:      envFunc,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Landlock == nil || p.Landlock.Compatibility != "best_effort" {
					t.Error("manifest policy should win over env")
				}
				if p.Filesystem != nil {
					t.Error("should not have filesystem from env policy")
				}
			},
		},
		{
			name:     "env + manifest file = manifest wins",
			manifest: manifestWithFile,
			env:      envFunc,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Landlock == nil || p.Landlock.Compatibility != "best_effort" {
					t.Error("manifest file policy should win over env")
				}
			},
		},
		{
			name:     "env + manifest no policy = env used",
			manifest: manifestNone,
			env:      envFunc,
			checkPolicy: func(t *testing.T, p *types.SandboxPolicy) {
				if p.Version != 1 {
					t.Errorf("env policy should be used: version = %d", p.Version)
				}
			},
		},
		{
			name:    "none",
			env:     noEnv,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := resolveCreatePolicy(tt.flagPolicy, tt.flagExplicit, tt.manifest, manifestPath, tt.env)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNil {
				if p != nil {
					t.Errorf("expected nil, got %+v", p)
				}
				return
			}
			if p == nil {
				t.Fatal("expected non-nil policy")
			}
			if tt.checkPolicy != nil {
				tt.checkPolicy(t, p)
			}
		})
	}
}

func TestBuildCreateFlags_PolicyFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flag-policy.yaml")
	_ = os.WriteFile(path, []byte("version: 1\nfilesystem_policy:\n  include_workdir: true\n"), 0o600)

	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{policyFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy == nil || f.Policy.Filesystem == nil || !f.Policy.Filesystem.IncludeWorkdir {
		t.Errorf("flag policy = %+v", f.Policy)
	}
}

func TestBuildCreateFlags_NoPolicy(t *testing.T) {
	f, err := buildCreateFlags(&cobra.Command{}, createFlagInput{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Policy != nil {
		t.Errorf("policy should be nil when no flag and no env: %+v", f.Policy)
	}
}

// TestBuildCreateFlags_ApprovalModeDefault_DoesNotOverrideManifest verifies
// that the cobra default "manual" for --approval-mode is NOT propagated when
// the user did not explicitly pass the flag. Without this check, the flag's
// default silently overwrites a manifest's approval-mode via
// MergeManifestAndFlags's "flag non-empty wins" rule.
func TestBuildCreateFlags_ApprovalModeDefault_DoesNotOverrideManifest(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("approval-mode", "manual", "")
	// Flag registered but NOT set — simulates cobra after parsing with no
	// --approval-mode argument. cmd.Flags().Changed("approval-mode") == false.

	f, err := buildCreateFlags(cmd, createFlagInput{approvalMode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if f.ApprovalMode != "" {
		t.Errorf("ApprovalMode = %q, want empty when flag not explicitly set", f.ApprovalMode)
	}
}

// TestBuildCreateFlags_ApprovalModeExplicit verifies that an explicit
// --approval-mode flag value IS propagated, so it wins over a manifest.
func TestBuildCreateFlags_ApprovalModeExplicit(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("approval-mode", "manual", "")
	// Simulate the user passing --approval-mode=auto.
	if err := cmd.Flags().Set("approval-mode", "auto"); err != nil {
		t.Fatal(err)
	}

	f, err := buildCreateFlags(cmd, createFlagInput{approvalMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if f.ApprovalMode != "auto" {
		t.Errorf("ApprovalMode = %q, want %q when flag explicitly set", f.ApprovalMode, "auto")
	}
}

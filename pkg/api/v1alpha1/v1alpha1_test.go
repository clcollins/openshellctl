package v1alpha1

import (
	"errors"
	"strings"
	"testing"
)

func TestDecode_Valid(t *testing.T) {
	yaml := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-1
  workspace: default
spec:
  image: ghcr.io/x/y:latest
  env:
    FOO: bar
`
	s, err := Decode(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Metadata.Name != "sb-1" || s.Spec.Image != "ghcr.io/x/y:latest" {
		t.Errorf("decoded = %+v", s)
	}
}

func TestDecode_UnknownFieldRejected(t *testing.T) {
	yaml := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-1
spec:
  bogusField: 1
`
	if _, err := Decode(strings.NewReader(yaml)); err == nil {
		t.Error("expected strict decode to reject unknown field")
	}
}

func TestDecode_WrongAPIVersion(t *testing.T) {
	yaml := "apiVersion: wrong/v1\nkind: Sandbox\nmetadata: {}\nspec: {}\n"
	_, err := Decode(strings.NewReader(yaml))
	if !errors.Is(err, ErrWrongAPIVersion) {
		t.Fatalf("err = %v, want ErrWrongAPIVersion", err)
	}
}

func TestDecode_WrongKind(t *testing.T) {
	yaml := "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Widget\nmetadata: {}\nspec: {}\n"
	_, err := Decode(strings.NewReader(yaml))
	if !errors.Is(err, ErrWrongKind) {
		t.Fatalf("err = %v, want ErrWrongKind", err)
	}
}

func validSandbox() *Sandbox {
	return &Sandbox{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindSandbox},
		Metadata: ObjectMeta{Name: "sb", Workspace: "default"},
	}
}

func TestValidate_OK(t *testing.T) {
	if errs := validSandbox().Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_Rules(t *testing.T) {
	u32 := func(n uint32) *uint32 { return &n }
	tests := []struct {
		name    string
		mutate  func(*Sandbox)
		wantSub string
	}{
		{"bad env key", func(s *Sandbox) { s.Spec.Env = map[string]string{"1BAD": "x"} }, "spec.env"},
		{"reserved env key", func(s *Sandbox) { s.Spec.Env = map[string]string{"OPENSHELL_X": "x"} }, "spec.env"},
		{"empty label key", func(s *Sandbox) { s.Metadata.Labels = map[string]string{"": "x"} }, "metadata.labels"},
		{"gpu count zero", func(s *Sandbox) { s.Spec.Resources = &Resources{GPU: &GPU{Count: u32(0)}} }, "gpu.count"},
		{"driverConfig not object", func(s *Sandbox) { s.Spec.DriverConfig = map[string]any{"d": "scalar"} }, "spec.driverConfig"},
		{"policy and policyFile", func(s *Sandbox) {
			s.Spec.Policy = map[string]any{"version": 1}
			s.Spec.PolicyFile = "p.yaml"
		}, "mutually exclusive"},
		{"bad approvalMode", func(s *Sandbox) { s.Spec.ApprovalMode = "sometimes" }, "spec.approvalMode"},
		{"bad sessionOpts approvalMode", func(s *Sandbox) {
			s.Spec.SessionOpts = &SessionOpts{ApprovalMode: "yolo"}
		}, "spec.sessionOpts.approvalMode"},
		{"bad sessionOpts output", func(s *Sandbox) {
			s.Spec.SessionOpts = &SessionOpts{Output: "xml"}
		}, "spec.sessionOpts.output"},
		{"empty upload local", func(s *Sandbox) { s.Spec.Upload = []Upload{{Local: ""}} }, "spec.upload[0].local"},
		{"empty providerRef", func(s *Sandbox) { s.Spec.ProviderRefs = []ProviderRef{{Name: ""}} }, "providerRefs[0].name"},
		{"dup providerRef", func(s *Sandbox) {
			s.Spec.ProviderRefs = []ProviderRef{{Name: "p"}, {Name: "p"}}
		}, "duplicate"},
		{"replace without name", func(s *Sandbox) {
			s.Metadata.Name = ""
			s.Spec.SessionOpts = &SessionOpts{Replace: true}
		}, "spec.sessionOpts.replace"},
		{"malformed replaceTimeout", func(s *Sandbox) {
			s.Spec.SessionOpts = &SessionOpts{ReplaceTimeout: "not-a-duration"}
		}, "spec.sessionOpts.replaceTimeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSandbox()
			tt.mutate(s)
			errs := s.Validate()
			if len(errs) == 0 {
				t.Fatalf("expected an error containing %q", tt.wantSub)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), tt.wantSub) {
					found = true
				}
			}
			if !found {
				t.Errorf("errors %v do not contain %q", errs, tt.wantSub)
			}
		})
	}
}

func TestDecode_FullManifest(t *testing.T) {
	y := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: full-test
  workspace: staging
  labels:
    team: sre
spec:
  image: quay.io/org/image:v1
  command: ["claude", "/job-sop-improve"]
  tty: true
  env:
    FOO: bar
    BAZ: qux
  providerRefs:
    - name: vertex
    - name: github
  resources:
    cpu: "2"
    memory: 4Gi
    gpu:
      count: 1
  upload:
    - local: ./src
      dest: /workspace
    - local: ./data
  sessionOpts:
    noKeep: true
    detach: false
    forward: "8080"
    approvalMode: auto
    output: json
`
	s, err := Decode(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if s.Metadata.Name != "full-test" {
		t.Errorf("name = %q", s.Metadata.Name)
	}
	if s.Metadata.Workspace != "staging" {
		t.Errorf("workspace = %q", s.Metadata.Workspace)
	}
	if s.Metadata.Labels["team"] != "sre" {
		t.Errorf("labels = %v", s.Metadata.Labels)
	}
	if s.Spec.Image != "quay.io/org/image:v1" {
		t.Errorf("image = %q", s.Spec.Image)
	}
	if len(s.Spec.Command) != 2 || s.Spec.Command[0] != "claude" || s.Spec.Command[1] != "/job-sop-improve" {
		t.Errorf("command = %v", s.Spec.Command)
	}
	if s.Spec.TTY == nil || !*s.Spec.TTY {
		t.Errorf("tty = %v", s.Spec.TTY)
	}
	if s.Spec.Env["FOO"] != "bar" || s.Spec.Env["BAZ"] != "qux" {
		t.Errorf("env = %v", s.Spec.Env)
	}
	if len(s.Spec.ProviderRefs) != 2 || s.Spec.ProviderRefs[0].Name != "vertex" || s.Spec.ProviderRefs[1].Name != "github" {
		t.Errorf("providerRefs = %v", s.Spec.ProviderRefs)
	}
	if s.Spec.Resources == nil || s.Spec.Resources.CPU != "2" || s.Spec.Resources.Memory != "4Gi" {
		t.Errorf("resources = %+v", s.Spec.Resources)
	}
	if s.Spec.Resources.GPU == nil || s.Spec.Resources.GPU.Count == nil || *s.Spec.Resources.GPU.Count != 1 {
		t.Errorf("gpu = %+v", s.Spec.Resources.GPU)
	}
	if len(s.Spec.Upload) != 2 || s.Spec.Upload[0].Local != "./src" || s.Spec.Upload[0].Dest != "/workspace" {
		t.Errorf("upload = %v", s.Spec.Upload)
	}
	if s.Spec.Upload[1].Local != "./data" || s.Spec.Upload[1].Dest != "" {
		t.Errorf("upload[1] = %v", s.Spec.Upload[1])
	}

	so := s.Spec.SessionOpts
	if so == nil {
		t.Fatal("sessionOpts is nil")
		return // unreachable; makes the nil check provable to static analysis (SA5011)
	}
	if !so.NoKeep {
		t.Error("sessionOpts.noKeep should be true")
	}
	if so.Detach {
		t.Error("sessionOpts.detach should be false")
	}
	if so.Forward != "8080" {
		t.Errorf("sessionOpts.forward = %q", so.Forward)
	}
	if so.ApprovalMode != "auto" {
		t.Errorf("sessionOpts.approvalMode = %q", so.ApprovalMode)
	}
	if so.Output != "json" {
		t.Errorf("sessionOpts.output = %q", so.Output)
	}
}

func TestDecode_SessionOptsMinimal(t *testing.T) {
	y := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: minimal
spec:
  image: test:latest
  sessionOpts:
    noKeep: true
`
	s, err := Decode(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Spec.SessionOpts == nil || !s.Spec.SessionOpts.NoKeep {
		t.Errorf("sessionOpts.noKeep not parsed")
	}
	if s.Spec.SessionOpts.Detach {
		t.Error("detach should default false")
	}
	if s.Spec.SessionOpts.Forward != "" {
		t.Errorf("forward should be empty: %q", s.Spec.SessionOpts.Forward)
	}
}

func TestDecode_NoSessionOpts(t *testing.T) {
	y := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: basic
spec:
  image: test:latest
`
	s, err := Decode(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Spec.SessionOpts != nil {
		t.Errorf("sessionOpts should be nil when omitted, got %+v", s.Spec.SessionOpts)
	}
}

func TestDecode_DeprecatedFlatFields(t *testing.T) {
	y := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: compat
spec:
  image: test:latest
  keep: false
  detach: true
  forward: "9090"
  approvalMode: auto
`
	s, err := Decode(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if s.Spec.Keep == nil || *s.Spec.Keep != false {
		t.Errorf("deprecated keep = %v", s.Spec.Keep)
	}
	if !s.Spec.Detach {
		t.Error("deprecated detach should be true")
	}
	if s.Spec.Forward != "9090" {
		t.Errorf("deprecated forward = %q", s.Spec.Forward)
	}
	if s.Spec.ApprovalMode != "auto" {
		t.Errorf("deprecated approvalMode = %q", s.Spec.ApprovalMode)
	}
}

func TestDecode_CommandOnly(t *testing.T) {
	y := `
apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: cmd-test
spec:
  image: python:3
  command: ["python3", "-c", "print('hello')"]
`
	s, err := Decode(strings.NewReader(y))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(s.Spec.Command) != 3 || s.Spec.Command[0] != "python3" || s.Spec.Command[2] != "print('hello')" {
		t.Errorf("command = %v", s.Spec.Command)
	}
}

func TestValidate_SessionOptsValid(t *testing.T) {
	s := validSandbox()
	s.Spec.SessionOpts = &SessionOpts{
		NoKeep:       true,
		Detach:       true,
		Forward:      "8080",
		ApprovalMode: "auto",
		Output:       "json",
	}
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidate_SessionOptsEmptyIsValid(t *testing.T) {
	s := validSandbox()
	s.Spec.SessionOpts = &SessionOpts{}
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

// TestValidate_ReplaceWithName confirms Replace is valid when metadata.name
// is set (validSandbox() already sets Name: "sb") — the companion case to
// the "replace without name" row in TestValidate_Rules above.
func TestValidate_ReplaceWithName(t *testing.T) {
	s := validSandbox()
	s.Spec.SessionOpts = &SessionOpts{Replace: true, ReplaceTimeout: "90s"}
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestDeepCopy_Independent(t *testing.T) {
	tru := true
	cnt := uint32(2)
	orig := &Sandbox{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindSandbox},
		Metadata: ObjectMeta{Name: "sb", Labels: map[string]string{"a": "1"}},
		Spec: SandboxSpec{
			Command:      []string{"bash"},
			Env:          map[string]string{"FOO": "bar"},
			TTY:          &tru,
			Resources:    &Resources{CPU: "1", GPU: &GPU{Count: &cnt}},
			DriverConfig: map[string]any{"d": map[string]any{"k": "v"}},
			ProviderRefs: []ProviderRef{{Name: "p"}},
			Upload:       []Upload{{Local: "f", GitIgnore: &tru}},
		},
	}
	cp := orig.DeepCopy()

	// Mutate the copy; original must be unaffected.
	cp.Metadata.Labels["a"] = "2"
	cp.Spec.Env["FOO"] = "changed"
	cp.Spec.Command[0] = "sh"
	*cp.Spec.TTY = false
	*cp.Spec.Resources.GPU.Count = 9
	cp.Spec.DriverConfig["d"].(map[string]any)["k"] = "w"
	cp.Spec.ProviderRefs[0].Name = "q"

	if orig.Metadata.Labels["a"] != "1" {
		t.Error("labels not deep-copied")
	}
	if orig.Spec.Env["FOO"] != "bar" {
		t.Error("env not deep-copied")
	}
	if orig.Spec.Command[0] != "bash" {
		t.Error("command not deep-copied")
	}
	if *orig.Spec.TTY != true {
		t.Error("TTY pointer not deep-copied")
	}
	if *orig.Spec.Resources.GPU.Count != 2 {
		t.Error("GPU count not deep-copied")
	}
	if orig.Spec.DriverConfig["d"].(map[string]any)["k"] != "v" {
		t.Error("driverConfig not deep-copied")
	}
	if orig.Spec.ProviderRefs[0].Name != "p" {
		t.Error("providerRefs not deep-copied")
	}
}

func TestDeepCopy_SessionOpts(t *testing.T) {
	orig := &Sandbox{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindSandbox},
		Metadata: ObjectMeta{Name: "sb"},
		Spec: SandboxSpec{
			SessionOpts: &SessionOpts{
				NoKeep:       true,
				Detach:       true,
				Forward:      "8080",
				ApprovalMode: "auto",
				Output:       "json",
			},
		},
	}
	cp := orig.DeepCopy()

	cp.Spec.SessionOpts.NoKeep = false
	cp.Spec.SessionOpts.Forward = "9090"
	cp.Spec.SessionOpts.ApprovalMode = "manual"

	if !orig.Spec.SessionOpts.NoKeep {
		t.Error("sessionOpts.noKeep not deep-copied")
	}
	if orig.Spec.SessionOpts.Forward != "8080" {
		t.Error("sessionOpts.forward not deep-copied")
	}
	if orig.Spec.SessionOpts.ApprovalMode != "auto" {
		t.Error("sessionOpts.approvalMode not deep-copied")
	}
}

func TestDeepCopy_NilSessionOpts(t *testing.T) {
	orig := &Sandbox{
		TypeMeta: TypeMeta{APIVersion: APIVersion, Kind: KindSandbox},
		Metadata: ObjectMeta{Name: "sb"},
		Spec:     SandboxSpec{},
	}
	cp := orig.DeepCopy()
	if cp.Spec.SessionOpts != nil {
		t.Error("nil sessionOpts should stay nil after DeepCopy")
	}
}

func TestDeepCopy_Nil(t *testing.T) {
	var s *Sandbox
	if s.DeepCopy() != nil {
		t.Error("nil.DeepCopy() should be nil")
	}
}

package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"github.com/spf13/cobra"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

// templatedManifest is the motivating example from ROSAENG-74244: a
// hardcoded metadata.name (which collides on reuse) and a per-run PROMPT,
// both pulled out as ${KEY} references.
const templatedManifest = `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: ${NAME}
spec:
  image: python
  command:
    - claude
    - --dangerously-skip-permissions
    - '${PROMPT}'
`

func writeManifestFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sandbox.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSandboxCreate_Param_SubstitutesIntoCommand is the card's first
// acceptance criterion end to end: -p NAME=X -p PROMPT="a, b: c" creates a
// sandbox named X whose command's third element is exactly "a, b: c" — a
// value containing both a comma and a colon, which a StringSlice flag or raw
// text substitution would have corrupted.
func TestSandboxCreate_Param_SubstitutesIntoCommand(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	var gotCommand []string
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "X", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, _, name string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
			gotCommand = spec.Command
			return &types.Sandbox{Name: name}, nil
		})

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", path, "-p", "NAME=X", "-p", "PROMPT=a, b: c", "--detach", "--no-auto-providers", "-o", "json")
	if err != nil {
		t.Fatalf("sandbox create: %v\noutput:\n%s", err, out)
	}
	if len(gotCommand) != 3 || gotCommand[2] != "a, b: c" {
		t.Errorf("command = %#v, want third element exactly %q", gotCommand, "a, b: c")
	}
}

// TestSandboxCreate_Param_PartialSubstitution_FailsBeforeGatewayCall is the
// card's second acceptance criterion: the same manifest with only -p NAME=X
// fails before any gateway call, naming PROMPT as unresolved, with a
// non-zero exit code.
func TestSandboxCreate_Param_PartialSubstitution_FailsBeforeGatewayCall(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	// No CreateSandbox expectation — gomock fails the test if it's called.

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", path, "-p", "NAME=X", "--detach")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitCodeFor(err) == ExitOK {
		t.Error("exit = 0, want non-zero")
	}
	if !strings.Contains(err.Error(), "PROMPT") {
		t.Errorf("error should name PROMPT as unresolved, got: %v", err)
	}
}

// TestSandboxCreate_Param_UnknownParameter_Errors is the card's third
// acceptance criterion: -p EXTRA=1 against a manifest that does not
// reference EXTRA fails unless --ignore-unknown-parameters is set.
func TestSandboxCreate_Param_UnknownParameter_Errors(t *testing.T) {
	path := writeManifestFile(t, "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Sandbox\nmetadata:\n  name: sb\nspec:\n  image: python\n")

	_, err := runCmd(t, "sandbox", "create", "-f", path, "-p", "EXTRA=1", "--detach")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "EXTRA") {
		t.Errorf("error should name EXTRA, got: %v", err)
	}

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{Name: "sb"}, nil)
	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", path, "-p", "EXTRA=1", "--ignore-unknown-parameters", "--detach", "-o", "json")
	if err != nil {
		t.Fatalf("with --ignore-unknown-parameters: %v\noutput:\n%s", err, out)
	}
}

// TestSandboxCreate_Parameters_PrintsNamesAndExits is the card's fourth
// acceptance criterion: --parameters -f sandbox.yaml prints NAME and PROMPT,
// each on its own line, and makes no gateway call.
func TestSandboxCreate_Parameters_PrintsNamesAndExits(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	// No expectations set — any call fails the test.

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create", "-f", path, "--parameters")
	if err != nil {
		t.Fatalf("--parameters: %v\noutput:\n%s", err, out)
	}
	if strings.TrimSpace(out) != "NAME\nPROMPT" {
		t.Errorf("output = %q, want \"NAME\\nPROMPT\"", out)
	}
}

// TestSandboxCreate_ParametersWithParam_UsageError: --parameters combined
// with --param/--param-file is a usage error, matching oc process (the
// convention this card's flags mirror) rather than silently ignoring the
// supplied values — a user who passed both almost certainly meant one or
// the other, not a request where half the input is quietly discarded.
func TestSandboxCreate_ParametersWithParam_UsageError(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	// No expectations set — any call fails the test.

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", path, "--parameters", "-p", "NAME=X")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "--parameters") {
		t.Errorf("error should mention --parameters, got: %v", err)
	}
}

// TestSandboxCreate_ParamFile_OverriddenByParam_WarnsOnStderr is the card's
// fifth acceptance criterion: --param-file values are overridden by --param
// for the same key, with a stderr warning.
func TestSandboxCreate_ParamFile_OverriddenByParam_WarnsOnStderr(t *testing.T) {
	manifestPath := writeManifestFile(t, "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Sandbox\nmetadata:\n  name: ${NAME}\nspec:\n  image: python\n")
	dir := t.TempDir()
	paramFile := filepath.Join(dir, "params.txt")
	if err := os.WriteFile(paramFile, []byte("NAME=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "fromflag", gomock.Any(), gomock.Any()).
		Return(&types.Sandbox{Name: "fromflag"}, nil)

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", manifestPath, "--param-file", paramFile, "-p", "NAME=fromflag", "--detach", "-o", "json")
	if err != nil {
		t.Fatalf("sandbox create: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "Warning: --param NAME overrides value from --param-file") {
		t.Errorf("expected an override warning on stderr, got:\n%s", out)
	}
}

// TestNeedsTemplating is the authoritative proof for acceptance criterion 6
// ("a manifest with no ${...} references produces an identical create
// request to the current release"): it exercises the actual decision
// function loadManifestWithParams branches on, directly, rather than
// inferring the skip from downstream output equality (which a round trip
// could still coincidentally produce for a semantically-simple manifest).
func TestNeedsTemplating(t *testing.T) {
	tests := []struct {
		name string
		data string
		tf   templateFlags
		want bool
	}{
		{"no refs, no flags: the golden no-op case", "metadata:\n  name: sb\n", templateFlags{}, false},
		{"a real reference with no flags still needs templating", "metadata:\n  name: ${NAME}\n", templateFlags{}, true},
		{"--param alone forces templating even with no ${ text", "metadata:\n  name: sb\n", templateFlags{params: []string{"X=1"}}, true},
		{"--param-file alone forces templating", "metadata:\n  name: sb\n", templateFlags{paramFiles: []string{"p.txt"}}, true},
		{"--parameters alone forces templating", "metadata:\n  name: sb\n", templateFlags{printParameters: true}, true},
		{"an escaped $${KEY} still trips the cheap pre-filter (loadManifestWithParams falls back to a direct decode from there)", "metadata:\n  name: $${ESCAPED}\n", templateFlags{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsTemplating([]byte(tt.data), tt.tf); got != tt.want {
				t.Errorf("needsTemplating(%q, %+v) = %v, want %v", tt.data, tt.tf, got, tt.want)
			}
		})
	}
}

// TestSandboxCreate_NoReferences_IdenticalToUntemplatedDecode is the card's
// sixth acceptance criterion end to end: a manifest with no ${...}
// references, and no -p flags, decodes identically whether or not the
// templating machinery exists. TestNeedsTemplating above is the direct proof
// that loadManifestWithParams's round-trip-skip branch is actually taken for
// this input; this test additionally confirms the full decoded manifest
// (not just two fields) matches decodeManifestBytes's output.
func TestSandboxCreate_NoReferences_IdenticalToUntemplatedDecode(t *testing.T) {
	const manifest = "apiVersion: openshell.managed.openshift.io/v1alpha1\n" +
		"kind: Sandbox\n" +
		"metadata:\n  name: sb\n" +
		"spec:\n  image: python\n"
	path := writeManifestFile(t, manifest)

	direct, err := decodeManifestBytes([]byte(manifest))
	if err != nil {
		t.Fatalf("decodeManifestBytes: %v", err)
	}

	cmd := &cobra.Command{}
	viaLoader, printedOnly, err := loadManifestWithParams(cmd, osFS{}, path, templateFlags{})
	if err != nil {
		t.Fatalf("loadManifestWithParams: %v", err)
	}
	if printedOnly {
		t.Fatal("printedOnly = true, want false")
	}
	if !reflect.DeepEqual(viaLoader, direct) {
		t.Errorf("loadManifestWithParams() = %+v, want identical to decodeManifestBytes() = %+v", viaLoader, direct)
	}
}

// TestSandboxCreate_ParamWithoutFile_UsageError confirms --param/--param-file/
// --parameters/--ignore-unknown-parameters require -f, since there is no
// manifest to substitute into otherwise.
func TestSandboxCreate_ParamWithoutFile_UsageError(t *testing.T) {
	_, err := runCmd(t, "sandbox", "create", "--from", "python", "-p", "NAME=X", "--detach")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "-f") && !strings.Contains(err.Error(), "--file") {
		t.Errorf("error should mention -f/--file, got: %v", err)
	}
}

// TestSandboxCreate_MalformedParam_UsageError confirms a malformed --param
// entry (no '=') surfaces as a usage error — and, since there is no '=' to
// split on, the entry is not echoed back (see
// TestSandboxCreate_MalformedParam_NeverLeaksValue): it could just as
// easily be an accidentally-bare secret value as a mistyped key.
func TestSandboxCreate_MalformedParam_UsageError(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)
	_, err := runCmd(t, "sandbox", "create", "-f", path, "-p", "NOTKEYVALUE", "--detach")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "--param") {
		t.Errorf("error should name the --param source, got: %v", err)
	}
}

// TestSandboxCreate_MalformedParam_NeverLeaksValue is the ticket's explicit
// security requirement end to end: a --param entry with a syntactically
// invalid key must never echo its VALUE half into any output the command
// produces, at any verbosity.
func TestSandboxCreate_MalformedParam_NeverLeaksValue(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)
	out, err := runCmd(t, "sandbox", "create", "-f", path, "-v", "-p", "1BAD=supersecretvalue", "--detach")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if strings.Contains(out, "supersecretvalue") {
		t.Errorf("output leaks the --param value: %q", out)
	}
	if strings.Contains(err.Error(), "supersecretvalue") {
		t.Errorf("error leaks the --param value: %v", err)
	}
	if !strings.Contains(err.Error(), "1BAD") {
		t.Errorf("error should still name the invalid key, got: %v", err)
	}
}

// TestSandboxCreate_ParamFile_UnreadablePath_UsageError confirms a missing
// --param-file path surfaces as a usage error rather than a raw os.Open
// failure reaching the user unwrapped.
func TestSandboxCreate_ParamFile_UnreadablePath_UsageError(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)
	_, err := runCmd(t, "sandbox", "create", "-f", path, "--param-file", "/no/such/file.txt", "--detach")
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
}

// TestLoadManifestWithParams_UsesInjectedFS confirms the orchestrator reads
// --param-file through the fs.FS it is given (a seam the CLI's production
// osFS production path never exercises in-test) rather than the real
// filesystem.
func TestLoadManifestWithParams_UsesInjectedFS(t *testing.T) {
	path := writeManifestFile(t, "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Sandbox\nmetadata:\n  name: ${NAME}\nspec:\n  image: python\n")
	fsys := fstest.MapFS{
		"params.txt": {Data: []byte("NAME=fromfs\n")},
	}
	cmd := &cobra.Command{}
	var out, errw strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errw)

	m, printedOnly, err := loadManifestWithParams(cmd, fsys, path, templateFlags{paramFiles: []string{"params.txt"}})
	if err != nil {
		t.Fatalf("loadManifestWithParams: %v", err)
	}
	if printedOnly {
		t.Fatal("printedOnly = true, want false")
	}
	if m.Metadata.Name != "fromfs" {
		t.Errorf("Metadata.Name = %q, want %q", m.Metadata.Name, "fromfs")
	}
}

// manifestWithIncidentalDollarBrace is NOT a template: its "${HOME}" is
// ordinary shell-expansion text that happens to share ${KEY}'s syntax. Every
// -f consumer other than `sandbox create` must keep decoding it exactly as
// before this card, since templating is scoped to `sandbox create` alone
// (ROSAENG-74244's "Out of scope: any command other than sandbox create").
const manifestWithIncidentalDollarBrace = `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: from-file
spec:
  image: test:latest
  command: ["bash", "-c", "echo ${HOME}/workspace"]
`

// TestDelete_FileWithIncidentalDollarBrace_NotTreatedAsTemplate is the
// regression test for the scope leak a pre-PR review caught: loadManifest
// (shared by doctor and every namesFromManifest caller — get/connect/
// upload/download/ssh-config/logs/stop/start/delete/provider) must never
// route through the paramtemplate pipeline, or this manifest's incidental
// "${HOME}" would turn into a hard "unresolved parameter reference" error
// for a command that was never given, and never asked for, -p/--param-file.
func TestDelete_FileWithIncidentalDollarBrace_NotTreatedAsTemplate(t *testing.T) {
	path := writeManifestFile(t, manifestWithIncidentalDollarBrace)
	// Same assertion shape as TestDelete_FileExtractsName: -f should parse
	// fine and fail on the gateway dial (no mock injected here), never on
	// manifest/param validation.
	_, err := runCmd(t, "sandbox", "delete", "--file", path)
	if err == nil {
		t.Fatal("expected a gateway error, not success")
	}
	if exitCodeFor(err) == ExitUsage {
		t.Fatalf("should not be a usage error — ${HOME} in a non-create manifest must not be treated as an unresolved template parameter: %v", err)
	}
}

// TestDoctor_FileWithIncidentalDollarBrace_NotTreatedAsTemplate is the same
// regression for doctor -f, which also calls loadManifest directly.
func TestDoctor_FileWithIncidentalDollarBrace_NotTreatedAsTemplate(t *testing.T) {
	path := writeManifestFile(t, manifestWithIncidentalDollarBrace)
	out, err := runCmd(t, "doctor", "--file", path, "-o", "json")
	if err != nil && strings.Contains(err.Error(), "unresolved parameter") {
		t.Fatalf("doctor -f treated incidental ${HOME} as a template reference: %v\noutput:\n%s", err, out)
	}
}

// TestSandboxCreate_RealReference_NoFlags_ErrorsAsUnresolved documents the
// flip side of the two tests above: for `sandbox create` itself, a manifest
// using ${KEY} syntax IS treated as a template regardless of whether any
// -p/--param-file flag was given — oc process makes the identical tradeoff
// for any ${PARAMETER} with no declared default. This is intentional, not a
// bug: the feature's only point is that such a reference must resolve
// before the manifest is ever validated or sent to the gateway.
func TestSandboxCreate_RealReference_NoFlags_ErrorsAsUnresolved(t *testing.T) {
	path := writeManifestFile(t, templatedManifest)
	_, err := runCmd(t, "sandbox", "create", "-f", path, "--detach")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exit = %d, want ExitUsage (2)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "NAME") || !strings.Contains(err.Error(), "PROMPT") {
		t.Errorf("error should name both NAME and PROMPT as unresolved, got: %v", err)
	}
}

// TestLoadManifestWithParams_NoRefs_SkipsRender confirms the refinement a
// pre-PR review asked for: when the manifest's only "${"-looking text turns
// out to be escaped ($${KEY}) — i.e. CollectReferences finds nothing — the
// function decodes the original bytes directly rather than running them
// through Substitute/Render, so a manifest's original formatting
// (indentation, comments, key order) is never silently rewritten when there
// is nothing to substitute. Proven the same way as
// TestSandboxCreate_NoReferences_IdenticalToUntemplatedDecode: full
// structural equality against decodeManifestBytes called directly on the
// untouched source bytes.
func TestLoadManifestWithParams_NoRefs_SkipsRender(t *testing.T) {
	const manifest = "apiVersion: openshell.managed.openshift.io/v1alpha1\n" +
		"kind: Sandbox\n" +
		"metadata:\n" +
		"  name: $${ESCAPED}\n" + // literal text, not a reference — CollectReferences finds nothing
		"  # a comment that Render would not reproduce\n" +
		"spec:\n  image: python\n"
	path := writeManifestFile(t, manifest)

	direct, err := decodeManifestBytes([]byte(manifest))
	if err != nil {
		t.Fatalf("decodeManifestBytes: %v", err)
	}

	cmd := &cobra.Command{}
	got, printedOnly, err := loadManifestWithParams(cmd, fstest.MapFS{}, path, templateFlags{})
	if err != nil {
		t.Fatalf("loadManifestWithParams: %v", err)
	}
	if printedOnly {
		t.Fatal("printedOnly = true, want false")
	}
	if !reflect.DeepEqual(got, direct) {
		t.Errorf("loadManifestWithParams() = %+v, want identical to decodeManifestBytes() = %+v", got, direct)
	}
	if got.Metadata.Name != "$${ESCAPED}" {
		t.Errorf("Metadata.Name = %q, want the literal %q (never substituted)", got.Metadata.Name, "$${ESCAPED}")
	}
}

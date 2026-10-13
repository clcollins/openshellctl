package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"github.com/spf13/cobra"
	"go.uber.org/mock/gomock"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
	"github.com/openshift-online/openshellctl/pkg/gateway/mock"
)

// manifestURLServer returns an httptest.Server serving body verbatim with
// status 200 for every request — the hermetic stand-in for a real manifest
// host (git repo raw content, object store) per ROSAENG-74491's acceptance
// criteria.
func manifestURLServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("server received unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLoadManifest_URL is loadManifest's regression/acceptance test for the
// new source: an http:// URL (httptest.Server, no real network) decodes
// identically to the same bytes read from a local file.
func TestLoadManifest_URL(t *testing.T) {
	const manifest = `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-url
spec:
  image: ghcr.io/x/y:latest
`
	srv := manifestURLServer(t, manifest)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	m, err := loadManifest(cmd, srv.URL)
	if err != nil {
		t.Fatalf("loadManifest(url): %v", err)
	}
	if m.Metadata.Name != "sb-url" {
		t.Errorf("name = %q", m.Metadata.Name)
	}
	if m.Spec.Image != "ghcr.io/x/y:latest" {
		t.Errorf("image = %q", m.Spec.Image)
	}
}

// TestLoadManifest_URL_NotFound confirms a 404 is a clear, typed error
// naming the URL and status, mapped to ExitError — never a parsed-as-YAML
// error page.
func TestLoadManifest_URL_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	_, err := loadManifest(cmd, srv.URL)
	if err == nil {
		t.Fatal("expected an error for a 404 manifest URL")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error should name the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should name the status code: %v", err)
	}
	if exitCodeFor(err) != ExitError {
		t.Errorf("exitCodeFor = %d, want ExitError", exitCodeFor(err))
	}
}

// TestSandboxCreate_ManifestFromURL_ByteForByteParityWithFile is the card's
// first acceptance criterion: `sandbox create -f <url>` (against an
// httptest.Server) creates a sandbox identically to the same manifest read
// from a local file.
func TestSandboxCreate_ManifestFromURL_ByteForByteParityWithFile(t *testing.T) {
	const manifest = `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-parity
spec:
  image: python
  command: ["echo", "hi"]
`
	srv := manifestURLServer(t, manifest)
	filePath := writeManifestFile(t, manifest)

	run := func(t *testing.T, fileArg string) *types.SandboxSpec {
		t.Helper()
		ctrl := gomock.NewController(t)
		gw := mock.NewMockGateway(ctrl)
		var gotSpec *types.SandboxSpec
		gw.EXPECT().CreateSandbox(gomock.Any(), "default", "sb-parity", gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ interface{}, _, name string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
				gotSpec = spec
				return &types.Sandbox{Name: name}, nil
			})
		out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
			"-f", fileArg, "--detach", "--no-auto-providers", "-o", "json")
		if err != nil {
			t.Fatalf("sandbox create -f %s: %v\noutput:\n%s", fileArg, err, out)
		}
		return gotSpec
	}

	fromFile := run(t, filePath)
	fromURL := run(t, srv.URL)

	if !reflect.DeepEqual(fromFile, fromURL) {
		t.Errorf("spec from URL differs from spec from file:\nfile: %+v\nurl:  %+v", fromFile, fromURL)
	}
}

// TestSandboxCreate_ManifestFromURL_WithParams is the operator's own
// end-to-end scenario: `sandbox create -f <url> --param NAME=... --param
// PROMPT=...` against one of templates/{interactive,noninteractive}.yaml
// (ROSAENG-74244/#50) — URL support must reach sandbox create's templating
// path (loadManifestWithParams), not just the plain loadManifest chokepoint.
func TestSandboxCreate_ManifestFromURL_WithParams(t *testing.T) {
	srv := manifestURLServer(t, templatedManifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	var gotCommand []string
	gw.EXPECT().CreateSandbox(gomock.Any(), "default", "foo", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ interface{}, _, name string, spec *types.SandboxSpec, _ map[string]string) (*types.Sandbox, error) {
			gotCommand = spec.Command
			return &types.Sandbox{Name: name}, nil
		})

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", srv.URL, "--param", "NAME=foo", "--param", "PROMPT=bar", "--detach", "--no-auto-providers", "-o", "json")
	if err != nil {
		t.Fatalf("sandbox create -f <url> --param: %v\noutput:\n%s", err, out)
	}
	if len(gotCommand) != 3 || gotCommand[2] != "bar" {
		t.Errorf("command = %#v, want third element %q", gotCommand, "bar")
	}
}

// TestSandboxCreate_ManifestFromURL_NotFound_NoGatewayCall confirms a 404
// manifest URL fails before any gateway call (no CreateSandbox expectation —
// gomock fails the test if it's invoked) with a non-zero exit.
func TestSandboxCreate_ManifestFromURL_NotFound_NoGatewayCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl) // no EXPECT() calls at all

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", srv.URL, "--detach")
	if err == nil {
		t.Fatal("expected an error for a 404 manifest URL")
	}
	if exitCodeFor(err) == ExitOK {
		t.Error("expected a non-zero exit code")
	}
}

// TestSandboxCreate_ManifestFromURL_OversizedBody_NoGatewayCall confirms a
// response exceeding the size cap is a clear error before any gateway call.
func TestSandboxCreate_ManifestFromURL_OversizedBody_NoGatewayCall(t *testing.T) {
	oversized := strings.Repeat("a", manifestURLSizeLimit+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(oversized))
	}))
	defer srv.Close()

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl) // no EXPECT() calls at all

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", srv.URL, "--detach")
	if err == nil {
		t.Fatal("expected a size-cap error")
	}
	if exitCodeFor(err) == ExitOK {
		t.Error("expected a non-zero exit code")
	}
}

// TestSandboxCreate_ManifestFromURL_Userinfo_UsageError confirms a -f URL
// with userinfo is rejected with ExitUsage before any fetch — manifests.invalid
// resolves nowhere, so if the request were actually sent, the test would
// fail with a DNS/connection error instead of returning a UsageError.
func TestSandboxCreate_ManifestFromURL_Userinfo_UsageError(t *testing.T) {
	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl) // no EXPECT() calls at all

	_, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "create",
		"-f", "https://user:pass@manifests.invalid/sb.yaml", "--detach")
	if err == nil {
		t.Fatal("expected a usage error for userinfo in the manifest URL")
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exitCodeFor = %d, want ExitUsage", exitCodeFor(err))
	}
}

// TestSandboxGet_ManifestFromURL confirms URL support reaches a command
// other than sandbox create — here sandbox get -f <url>, via the shared
// namesFromManifest -> loadManifest chokepoint.
func TestSandboxGet_ManifestFromURL(t *testing.T) {
	const manifest = `apiVersion: openshell.managed.openshift.io/v1alpha1
kind: Sandbox
metadata:
  name: sb-get
spec:
  image: python
`
	srv := manifestURLServer(t, manifest)

	ctrl := gomock.NewController(t)
	gw := mock.NewMockGateway(ctrl)
	gw.EXPECT().GetSandbox(gomock.Any(), "default", "sb-get").
		Return(&types.Sandbox{Name: "sb-get"}, nil)

	out, err := runCmdWithGateway(t, cliDeps{Gateway: gw}, "sandbox", "get", "-f", srv.URL, "-o", "json")
	if err != nil {
		t.Fatalf("sandbox get -f <url>: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "sb-get") {
		t.Errorf("expected sandbox name in output, got:\n%s", out)
	}
}

// TestResolveManifestPolicy_URLRelativePathErrors mirrors
// TestResolveManifestPolicy_StdinRelativePathErrors: a relative
// spec.policyFile has no local directory to resolve against when the
// manifest itself came from a URL, so it must error the same way the stdin
// case does, not silently resolve against the CLI's CWD.
func TestResolveManifestPolicy_URLRelativePathErrors(t *testing.T) {
	m := &v1alpha1.Sandbox{
		Spec: v1alpha1.SandboxSpec{PolicyFile: "relative.yaml"},
	}
	_, err := resolveManifestPolicy(m, "https://example.com/sb.yaml")
	if err == nil {
		t.Fatal("expected error for relative policyFile with a URL manifest")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("err should be UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "must be an absolute path when the manifest is read from a URL") {
		t.Errorf("err = %v", err)
	}
}

package policyyaml

import (
	"strings"
	"testing"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

func lintErrs(t *testing.T, p *types.SandboxPolicy) []string {
	t.Helper()
	var out []string
	for _, e := range Lint(p) {
		out = append(out, e.Error())
	}
	return out
}

func hasErrContaining(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestLint_Nil(t *testing.T) {
	if Lint(nil) != nil {
		t.Error("Lint(nil) should be nil")
	}
}

func TestLint_ProcessUID(t *testing.T) {
	p := &types.SandboxPolicy{Process: &types.ProcessPolicy{RunAsUser: "0"}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "run_as_user must be 'sandbox' or a numeric UID/GID in range [1, 4294967294], got '0'") {
		t.Errorf("errs = %v", errs)
	}

	// "sandbox" and valid numeric are fine.
	ok := &types.SandboxPolicy{Process: &types.ProcessPolicy{RunAsUser: "sandbox", RunAsGroup: "1000"}}
	if errs := lintErrs(t, ok); len(errs) != 0 {
		t.Errorf("valid process should lint clean, got %v", errs)
	}
}

func TestLint_FilesystemPaths(t *testing.T) {
	p := &types.SandboxPolicy{Filesystem: &types.FilesystemPolicy{
		ReadOnly:  []string{"relative/path", "/ok", "/has/../traversal"},
		ReadWrite: []string{"/"},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "path must be absolute (start with '/'): relative/path") {
		t.Errorf("missing absolute-path error: %v", errs)
	}
	if !hasErrContaining(errs, "path contains '..' traversal component: /has/../traversal") {
		t.Errorf("missing traversal error: %v", errs)
	}
	if !hasErrContaining(errs, "read-write path is overly broad: /") {
		t.Errorf("missing overly-broad error: %v", errs)
	}
}

func TestLint_MiddlewareSelector(t *testing.T) {
	p := &types.SandboxPolicy{NetworkMiddlewares: map[string]types.NetworkMiddlewareConfig{
		"mw": {Name: "mw", Middleware: "content-guard", Order: 1}, // no endpoints selector
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "endpoint selector is required") {
		t.Errorf("missing selector-required error: %v", errs)
	}
}

func TestLint_MiddlewareDuplicateOrder(t *testing.T) {
	sel := &types.MiddlewareEndpointSelector{Include: []string{"h"}}
	p := &types.SandboxPolicy{NetworkMiddlewares: map[string]types.NetworkMiddlewareConfig{
		"a": {Name: "a", Middleware: "m", Order: 5, Endpoints: sel},
		"b": {Name: "b", Middleware: "m", Order: 5, Endpoints: sel},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "duplicate order 5") {
		t.Errorf("missing duplicate-order error: %v", errs)
	}
}

func TestLint_TCPWithAccess(t *testing.T) {
	p := &types.SandboxPolicy{NetworkPolicies: map[string]types.NetworkPolicyRule{
		"p": {Name: "p", Endpoints: []types.PolicyNetworkEndpoint{
			{Host: "h", Port: 443, Protocol: "tcp", Access: types.NetworkAccessPresetFull},
		}},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "protocol tcp does not support access") {
		t.Errorf("missing tcp+access error: %v", errs)
	}
}

func TestLint_EndpointNoPort(t *testing.T) {
	p := &types.SandboxPolicy{NetworkPolicies: map[string]types.NetworkPolicyRule{
		"p": {Name: "p", Endpoints: []types.PolicyNetworkEndpoint{{Host: "h", Protocol: "rest"}}},
	}}
	errs := lintErrs(t, p)
	if !hasErrContaining(errs, "must declare at least one port") {
		t.Errorf("missing no-port error: %v", errs)
	}
}

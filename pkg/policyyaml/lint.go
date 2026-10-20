package policyyaml

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// validate_sandbox_policy limits (lib.rs, Appendix B.2B).
const (
	minSandboxUID                 = 1
	maxSandboxUID                 = 4294967294
	maxFilesystemPaths            = 256
	maxPathLength                 = 4096
	maxMiddlewareConfigs          = 10
	maxMiddlewareSelectorPatterns = 32
)

// Lint reimplements the server-side validate_sandbox_policy checks (Appendix
// B.2B) so `openshellctl policy lint` can surface unsafe content before the
// gateway rejects it. The create path does NOT run Lint — gateway behaviour
// stays identical to the CLI. Returns all findings (empty when clean).
//
// This implements the high-value subset (process uid/gid, filesystem paths,
// middleware selector limits, tcp/L7 basics); it is the client-side lint aid,
// not an authoritative validator (the gateway remains authoritative). Messages
// match upstream verbatim where implemented.
func Lint(p *types.SandboxPolicy) []error {
	if p == nil {
		return nil
	}
	var errs []error
	add := func(msg string) { errs = append(errs, fmt.Errorf("%s", msg)) }

	lintProcess(p.Process, add)
	lintFilesystem(p.Filesystem, add)
	lintMiddlewares(p.NetworkMiddlewares, add)
	lintEndpoints(p.NetworkPolicies, add)

	return errs
}

func lintProcess(proc *types.ProcessPolicy, add func(string)) {
	if proc == nil {
		return
	}
	check := func(field, value string) {
		if value == "" || value == "sandbox" {
			return
		}
		if n, err := strconv.ParseUint(value, 10, 64); err != nil || n < minSandboxUID || n > maxSandboxUID {
			add(fmt.Sprintf("%s must be 'sandbox' or a numeric UID/GID in range [1, 4294967294], got '%s'", field, value))
		}
	}
	check("run_as_user", proc.RunAsUser)
	check("run_as_group", proc.RunAsGroup)
}

func lintFilesystem(fsp *types.FilesystemPolicy, add func(string)) {
	if fsp == nil {
		return
	}
	all := append(append([]string{}, fsp.ReadOnly...), fsp.ReadWrite...)
	if len(all) > maxFilesystemPaths {
		add(fmt.Sprintf("too many filesystem paths (%d > %d)", len(all), maxFilesystemPaths))
	}
	for _, path := range all {
		if len(path) > maxPathLength {
			trunc := path
			if len(trunc) > 77 {
				trunc = trunc[:77]
			}
			add(fmt.Sprintf("path exceeds maximum length (%d > %d): %s...", len(path), maxPathLength, trunc))
			continue
		}
		if !strings.HasPrefix(path, "/") {
			add(fmt.Sprintf("path must be absolute (start with '/'): %s", path))
		}
		if strings.Contains(path, "..") {
			add(fmt.Sprintf("path contains '..' traversal component: %s", path))
		}
	}
	for _, path := range fsp.ReadWrite {
		if path == "/" {
			add(fmt.Sprintf("read-write path is overly broad: %s", path))
		}
	}
}

func lintMiddlewares(mws map[string]types.NetworkMiddlewareConfig, add func(string)) {
	if len(mws) == 0 {
		return
	}
	if len(mws) > maxMiddlewareConfigs {
		add(fmt.Sprintf("too many middleware configs (%d > %d)", len(mws), maxMiddlewareConfigs))
	}
	keys := make([]string, 0, len(mws))
	for k := range mws {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	orders := map[int32]string{}
	for _, k := range keys {
		mw := mws[k]
		if mw.Name == "" {
			add("middleware config '' is invalid: name must not be empty")
		}
		if mw.Middleware == "" {
			add(fmt.Sprintf("middleware config '%s' is invalid: middleware must not be empty", mw.Name))
		}
		switch mw.OnError {
		case "", "fail_closed", "fail_open":
		default:
			add(fmt.Sprintf("middleware config '%s' is invalid: invalid on_error '%s'", mw.Name, mw.OnError))
		}
		if prev, dup := orders[mw.Order]; dup {
			a, b := prev, mw.Name
			if a > b {
				a, b = b, a
			}
			add(fmt.Sprintf("middleware configs '%s' and '%s' use duplicate order %d", a, b, mw.Order))
		} else {
			orders[mw.Order] = mw.Name
		}
		lintMiddlewareSelector(mw, add)
	}
}

func lintMiddlewareSelector(mw types.NetworkMiddlewareConfig, add func(string)) {
	if mw.Endpoints == nil {
		add(fmt.Sprintf("middleware config '%s' is invalid: endpoint selector is required", mw.Name))
		return
	}
	patterns := append(append([]string{}, mw.Endpoints.Include...), mw.Endpoints.Exclude...)
	if len(patterns) == 0 {
		add(fmt.Sprintf("middleware config '%s' is invalid: endpoint selector must include at least one host pattern", mw.Name))
	}
	if len(patterns) > maxMiddlewareSelectorPatterns {
		add(fmt.Sprintf("middleware config '%s' has too many selector patterns (%d > %d)", mw.Name, len(patterns), maxMiddlewareSelectorPatterns))
	}
	for _, pat := range patterns {
		if reason := selectorPatternError(pat); reason != "" {
			add(fmt.Sprintf("middleware config '%s' is invalid: endpoint selector pattern '%s' is invalid: %s", mw.Name, pat, reason))
		}
	}
}

func selectorPatternError(pat string) string {
	switch {
	case pat == "":
		return "host pattern must not be empty"
	case strings.ContainsAny(pat, " \t"):
		return "host pattern must not contain whitespace"
	case strings.ContainsAny(pat, "{}"):
		return "host pattern must not contain brace alternates; list each host pattern separately"
	case strings.Contains(pat, ".."):
		return "host pattern must not contain empty DNS labels"
	default:
		return ""
	}
}

func lintEndpoints(policies map[string]types.NetworkPolicyRule, add func(string)) {
	if len(policies) == 0 {
		return
	}
	keys := make([]string, 0, len(policies))
	for k := range policies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rule := policies[k]
		name := rule.Name
		if name == "" {
			name = k
		}
		for _, e := range rule.Endpoints {
			lintEndpoint(name, e, add)
		}
	}
}

func lintEndpoint(policyName string, e types.PolicyNetworkEndpoint, add func(string)) {
	// ports presence
	if e.Port == 0 && len(e.Ports) == 0 {
		add(fmt.Sprintf("network policy '%s': endpoint '%s' must declare at least one port", policyName, e.Host))
	}
	// tcp + L7 access preset
	if e.Protocol == "tcp" && e.Access != types.NetworkAccessPresetUnspecified {
		add(fmt.Sprintf("network policy '%s': endpoint %d has invalid L7 configuration: protocol tcp does not support access, rules, or deny_rules; remove those L7 fields", policyName, 0))
	}
}

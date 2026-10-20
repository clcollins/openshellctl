package policyyaml

import (
	"encoding/json"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"sigs.k8s.io/yaml"
)

// FromSDK converts an SDK SandboxPolicy back to a PolicyFile (from_proto
// semantics, lib.rs:833). Reverse of ToSDK: an all-empty process collapses to
// nil; params["name"] is restored to `tool` for MCP; flat dotted params are
// re-nested. (The tool/param reverse transforms are applied in Serialize's JSON
// view; FromSDK produces the structural PolicyFile.)
func FromSDK(p *types.SandboxPolicy) *PolicyFile {
	if p == nil {
		return nil
	}
	pf := &PolicyFile{Version: p.Version}
	if p.Filesystem != nil {
		pf.FilesystemPolicy = &FilesystemDef{
			IncludeWorkdir: p.Filesystem.IncludeWorkdir,
			ReadOnly:       p.Filesystem.ReadOnly,
			ReadWrite:      p.Filesystem.ReadWrite,
		}
	}
	if p.Landlock != nil {
		pf.Landlock = &LandlockDef{Compatibility: p.Landlock.Compatibility}
	}
	if p.Process != nil && (p.Process.RunAsUser != "" || p.Process.RunAsGroup != "") {
		pf.Process = &ProcessDef{RunAsUser: p.Process.RunAsUser, RunAsGroup: p.Process.RunAsGroup}
	}
	if len(p.NetworkPolicies) > 0 {
		pf.NetworkPolicies = make(map[string]NetworkPolicyRuleDef, len(p.NetworkPolicies))
		for k, rule := range p.NetworkPolicies {
			pf.NetworkPolicies[k] = ruleFromSDK(rule)
		}
	}
	if len(p.NetworkMiddlewares) > 0 {
		pf.NetworkMiddlewares = make(map[string]NetworkMiddlewareConfigDef, len(p.NetworkMiddlewares))
		for k, mw := range p.NetworkMiddlewares {
			def := NetworkMiddlewareConfigDef{
				Name:       mw.Name,
				Middleware: mw.Middleware,
				Order:      mw.Order,
				Config:     mw.Config,
				OnError:    mw.OnError,
			}
			if mw.Endpoints != nil {
				def.Endpoints = &MiddlewareEndpointSelectorDef{Include: mw.Endpoints.Include, Exclude: mw.Endpoints.Exclude}
			}
			pf.NetworkMiddlewares[k] = def
		}
	}
	return pf
}

func ruleFromSDK(rule types.NetworkPolicyRule) NetworkPolicyRuleDef {
	out := NetworkPolicyRuleDef{Name: rule.Name}
	for _, e := range rule.Endpoints {
		out.Endpoints = append(out.Endpoints, endpointFromSDK(e))
	}
	for _, b := range rule.Binaries {
		out.Binaries = append(out.Binaries, NetworkBinaryDef{Path: b.Path})
	}
	return out
}

func endpointFromSDK(e types.PolicyNetworkEndpoint) NetworkEndpointDef {
	out := NetworkEndpointDef{
		Host:                         e.Host,
		Path:                         e.Path,
		Protocol:                     e.Protocol,
		TLS:                          tlsModeFromSDK(e.TLS),
		Enforcement:                  enforcementFromSDK(e.Enforcement),
		Access:                       accessPresetFromSDK(e.Access),
		AllowedIPs:                   e.AllowedIPs,
		AllowEncodedSlash:            e.AllowEncodedSlash,
		WebsocketCredentialRewrite:   e.WebsocketCredentialRewrite,
		RequestBodyCredentialRewrite: e.RequestBodyCredentialRewrite,
		AllowUninspectedCredentials:  e.AllowUninspectedCredentials,
		PersistedQueries:             e.PersistedQueries,
		GraphqlMaxBodyBytes:          e.GraphqlMaxBodyBytes,
		CredentialSigning:            e.CredentialSigning,
		SigningService:               e.SigningService,
		SigningRegion:                e.SigningRegion,
	}
	// ports/port: emit ports when >1, else port when non-zero (B.6).
	if len(e.Ports) > 1 {
		out.Ports = make([]uint16, len(e.Ports))
		for i, p := range e.Ports {
			out.Ports[i] = clampU16(p)
		}
	} else if e.Port > 0 {
		out.Port = clampU16(e.Port)
	}
	for _, r := range e.Rules {
		if r.Allow != nil {
			out.Rules = append(out.Rules, L7RuleDef{Allow: allowFromSDK(*r.Allow)})
		}
	}
	for _, d := range e.DenyRules {
		out.DenyRules = append(out.DenyRules, denyFromSDK(d))
	}
	if e.CredentialBinding != nil {
		out.CredentialBinding = &NetworkCredentialBindingDef{Provider: e.CredentialBinding.Provider}
	}
	if len(e.GraphqlPersistedQueries) > 0 {
		out.GraphqlPersistedQueries = make(map[string]GraphqlOperationDef, len(e.GraphqlPersistedQueries))
		for k, v := range e.GraphqlPersistedQueries {
			out.GraphqlPersistedQueries[k] = GraphqlOperationDef{OperationType: v.OperationType, OperationName: v.OperationName, Fields: v.Fields}
		}
	}
	if e.Mcp != nil {
		out.MCP = &MCPConfigDef{StrictToolNames: e.Mcp.StrictToolNames, AllowAllKnownMcpMethods: e.Mcp.AllowAllKnownMcpMethods}
	}
	return out
}

func allowFromSDK(a types.L7Allow) L7AllowDef {
	return L7AllowDef{
		Method:        a.Method,
		Path:          a.Path,
		Command:       a.Command,
		Query:         queryMapFromSDK(a.Query),
		OperationType: a.OperationType,
		OperationName: a.OperationName,
		Fields:        a.Fields,
		Params:        paramsFromSDK(a.Params),
	}
}

func denyFromSDK(d types.L7DenyRule) L7DenyRuleDef {
	return L7DenyRuleDef{
		Method:        d.Method,
		Path:          d.Path,
		Command:       d.Command,
		Query:         queryMapFromSDK(d.Query),
		OperationType: d.OperationType,
		OperationName: d.OperationName,
		Fields:        d.Fields,
		Params:        paramsFromSDK(d.Params),
	}
}

func queryMapFromSDK(q map[string]types.L7QueryMatcher) map[string]QueryMatcher {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]QueryMatcher, len(q))
	for k, v := range q {
		out[k] = matcherFromSDK(v)
	}
	return out
}

func matcherFromSDK(m types.L7QueryMatcher) QueryMatcher {
	if len(m.Any) > 0 {
		return QueryMatcher{IsAny: true, Any: m.Any}
	}
	return QueryMatcher{Glob: m.Glob}
}

// paramsFromSDK re-nests flat dotted keys into a ParamMatcher tree. Flat keys
// with no '.' become leaf matchers; dotted keys nest.
func paramsFromSDK(flat map[string]types.L7QueryMatcher) map[string]ParamMatcher {
	if len(flat) == 0 {
		return nil
	}
	root := map[string]ParamMatcher{}
	for key, m := range flat {
		insertNested(root, key, matcherFromSDK(m))
	}
	return root
}

func insertNested(m map[string]ParamMatcher, dottedKey string, leaf QueryMatcher) {
	head := dottedKey
	rest := ""
	if i := indexByte(dottedKey, '.'); i >= 0 {
		head = dottedKey[:i]
		rest = dottedKey[i+1:]
	}
	if rest == "" {
		lm := leaf
		m[head] = ParamMatcher{Matcher: &lm}
		return
	}
	child, ok := m[head]
	if !ok || child.Object == nil {
		child = ParamMatcher{Object: map[string]ParamMatcher{}}
	}
	insertNested(child.Object, rest, leaf)
	m[head] = child
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func clampU16(v uint32) uint16 {
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

// Serialize renders an SDK policy as YAML (from_proto → YAML). It uses
// sigs.k8s.io/yaml (2-space, sorted keys). NOTE: this produces indented block
// sequences; the CLI's `--policy-only` uses libyaml indentless sequences. Exact
// byte-parity requires a post-pass pinned against a live capture (Appendix B.6)
// and is a documented follow-up; the structural content is correct.
func Serialize(p *types.SandboxPolicy) ([]byte, error) {
	pf := FromSDK(p)
	if pf == nil {
		return nil, nil
	}
	return yaml.Marshal(pf)
}

// ToJSONValue returns the policy as a JSON-compatible map for `get -o json|yaml`.
func ToJSONValue(p *types.SandboxPolicy) map[string]any {
	pf := FromSDK(p)
	if pf == nil {
		return nil
	}
	data, err := json.Marshal(pf)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

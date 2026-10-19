package policyyaml

import (
	"fmt"
	"sort"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// ToSDK converts a parsed PolicyFile to the SDK's types.SandboxPolicy, applying
// to_proto post-processing (lib.rs:707, Appendix B.2A): name defaulting from map
// keys, port/ports normalization, json_rpc_max_body_bytes derivation (mcp
// precedence), McpOptions only-when-set, tool→params["name"], nested params
// dot-flattening, harness dropped, provider_credentialed/advisor_proposed false.
func ToSDK(pf *PolicyFile) (*types.SandboxPolicy, error) {
	if pf == nil {
		return nil, nil
	}
	out := &types.SandboxPolicy{Version: pf.Version}

	if pf.FilesystemPolicy != nil {
		out.Filesystem = &types.FilesystemPolicy{
			IncludeWorkdir: pf.FilesystemPolicy.IncludeWorkdir,
			ReadOnly:       pf.FilesystemPolicy.ReadOnly,
			ReadWrite:      pf.FilesystemPolicy.ReadWrite,
		}
	}
	if pf.Landlock != nil {
		out.Landlock = &types.LandlockPolicy{Compatibility: pf.Landlock.Compatibility}
	}
	if pf.Process != nil {
		out.Process = &types.ProcessPolicy{
			RunAsUser:  pf.Process.RunAsUser,
			RunAsGroup: pf.Process.RunAsGroup,
		}
	}

	if len(pf.NetworkPolicies) > 0 {
		out.NetworkPolicies = make(map[string]types.NetworkPolicyRule, len(pf.NetworkPolicies))
		for _, key := range sortedKeys(pf.NetworkPolicies) {
			rule := pf.NetworkPolicies[key]
			r, err := networkPolicyToSDK(key, rule)
			if err != nil {
				return nil, err
			}
			out.NetworkPolicies[key] = r
		}
	}

	if len(pf.NetworkMiddlewares) > 0 {
		mws, err := middlewaresToSDK(pf.NetworkMiddlewares)
		if err != nil {
			return nil, fmt.Errorf("failed to convert network middleware config: %w", err)
		}
		out.NetworkMiddlewares = mws
	}

	return out, nil
}

func networkPolicyToSDK(key string, rule NetworkPolicyRuleDef) (types.NetworkPolicyRule, error) {
	name := rule.Name
	if name == "" {
		name = key
	}
	r := types.NetworkPolicyRule{Name: name}
	for _, e := range rule.Endpoints {
		ep, err := endpointToSDK(e)
		if err != nil {
			return types.NetworkPolicyRule{}, fmt.Errorf("rule %s: %w", name, err)
		}
		r.Endpoints = append(r.Endpoints, ep)
	}
	for _, b := range rule.Binaries {
		r.Binaries = append(r.Binaries, types.PolicyNetworkBinary{Path: b.Path})
	}
	return r, nil
}

func endpointToSDK(e NetworkEndpointDef) (types.PolicyNetworkEndpoint, error) {
	port, ports := normalizePorts(e.Port, e.Ports)
	tls, err := tlsModeToSDK(e.TLS)
	if err != nil {
		return types.PolicyNetworkEndpoint{}, fmt.Errorf("endpoint %s:%d: %w", e.Host, e.Port, err)
	}
	enforcement, err := enforcementToSDK(e.Enforcement)
	if err != nil {
		return types.PolicyNetworkEndpoint{}, fmt.Errorf("endpoint %s:%d: %w", e.Host, e.Port, err)
	}
	access, err := accessPresetToSDK(e.Access)
	if err != nil {
		return types.PolicyNetworkEndpoint{}, fmt.Errorf("endpoint %s:%d: %w", e.Host, e.Port, err)
	}
	out := types.PolicyNetworkEndpoint{
		Host:                         e.Host,
		Path:                         e.Path,
		Port:                         port,
		Ports:                        ports,
		Protocol:                     e.Protocol,
		TLS:                          tls,
		Enforcement:                  enforcement,
		Access:                       access,
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
		JSONRPCMaxBodyBytes:          jsonRPCMaxBodyBytes(e.JSONRPC, e.MCP),
		ProviderCredentialed:         false,
		AdvisorProposed:              false,
	}
	for _, r := range e.Rules {
		out.Rules = append(out.Rules, types.L7Rule{Allow: allowToSDK(r.Allow)})
	}
	for _, d := range e.DenyRules {
		out.DenyRules = append(out.DenyRules, denyToSDK(d))
	}
	if len(e.GraphqlPersistedQueries) > 0 {
		out.GraphqlPersistedQueries = make(map[string]types.GraphqlOperation, len(e.GraphqlPersistedQueries))
		for k, v := range e.GraphqlPersistedQueries {
			out.GraphqlPersistedQueries[k] = types.GraphqlOperation{
				OperationType: v.OperationType,
				OperationName: v.OperationName,
				Fields:        v.Fields,
			}
		}
	}
	if e.CredentialBinding != nil {
		out.CredentialBinding = &types.NetworkCredentialBinding{Provider: e.CredentialBinding.Provider}
	}
	if opts := mcpOptions(e.MCP); opts != nil {
		out.Mcp = opts
	}
	return out, nil
}

// normalizePorts: ports (non-empty) wins → (ports[0], ports); else port>0 →
// ([port], port); else (0, nil). (lib.rs:731-742)
func normalizePorts(port uint16, ports []uint16) (uint32, []uint32) {
	if len(ports) > 0 {
		out := make([]uint32, len(ports))
		for i, p := range ports {
			out[i] = uint32(p)
		}
		return out[0], out
	}
	if port > 0 {
		return uint32(port), []uint32{uint32(port)}
	}
	return 0, nil
}

// jsonRPCMaxBodyBytes: if mcp stanza present, use mcp.max_body_bytes (even 0);
// else json_rpc.max_body_bytes; else 0. (lib.rs:574-581)
func jsonRPCMaxBodyBytes(jsonRPC *JSONRPCConfigDef, mcp *MCPConfigDef) uint32 {
	if mcp != nil {
		return mcp.MaxBodyBytes
	}
	if jsonRPC != nil {
		return jsonRPC.MaxBodyBytes
	}
	return 0
}

// mcpOptions builds McpOptions only when at least one bool sub-field is set
// (lib.rs:592-599). max_body_bytes alone does not create it.
func mcpOptions(mcp *MCPConfigDef) *types.McpOptions {
	if mcp == nil {
		return nil
	}
	if mcp.StrictToolNames == nil && mcp.AllowAllKnownMcpMethods == nil {
		return nil
	}
	return &types.McpOptions{
		StrictToolNames:         mcp.StrictToolNames,
		AllowAllKnownMcpMethods: mcp.AllowAllKnownMcpMethods,
	}
}

func allowToSDK(a L7AllowDef) *types.L7Allow {
	return &types.L7Allow{
		Method:        a.Method,
		Path:          a.Path,
		Command:       a.Command,
		Query:         queryMapToSDK(a.Query),
		OperationType: a.OperationType,
		OperationName: a.OperationName,
		Fields:        a.Fields,
		Params:        paramsToSDK(a.Params, a.Tool),
	}
}

func denyToSDK(d L7DenyRuleDef) types.L7DenyRule {
	return types.L7DenyRule{
		Method:        d.Method,
		Path:          d.Path,
		Command:       d.Command,
		Query:         queryMapToSDK(d.Query),
		OperationType: d.OperationType,
		OperationName: d.OperationName,
		Fields:        d.Fields,
		Params:        paramsToSDK(d.Params, d.Tool),
	}
}

func queryMapToSDK(q map[string]QueryMatcher) map[string]types.L7QueryMatcher {
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]types.L7QueryMatcher, len(q))
	for k, v := range q {
		out[k] = matcherToSDK(v)
	}
	return out
}

func matcherToSDK(m QueryMatcher) types.L7QueryMatcher {
	if m.IsAny {
		return types.L7QueryMatcher{Any: m.Any}
	}
	return types.L7QueryMatcher{Glob: m.Glob}
}

// paramsToSDK applies the tool shorthand (→ params["name"] unless present) then
// dot-flattens nested params. (lib.rs:520-533, 424-452)
func paramsToSDK(params map[string]ParamMatcher, tool *QueryMatcher) map[string]types.L7QueryMatcher {
	merged := map[string]ParamMatcher{}
	for k, v := range params {
		merged[k] = v
	}
	if tool != nil {
		if _, exists := merged["name"]; !exists {
			t := *tool
			merged["name"] = ParamMatcher{Matcher: &t}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out := map[string]types.L7QueryMatcher{}
	for k, v := range merged {
		flattenParam(k, v, out)
	}
	return out
}

func flattenParam(key string, pm ParamMatcher, out map[string]types.L7QueryMatcher) {
	if pm.Matcher != nil {
		out[key] = matcherToSDK(*pm.Matcher)
		return
	}
	for childKey, child := range pm.Object {
		flattenParam(key+"."+childKey, child, out)
	}
}

func middlewaresToSDK(mws map[string]NetworkMiddlewareConfigDef) (map[string]types.NetworkMiddlewareConfig, error) {
	out := make(map[string]types.NetworkMiddlewareConfig, len(mws))
	for _, key := range sortedKeys(mws) {
		mw := mws[key]
		name := mw.Name
		if name == "" {
			name = key
		}
		cfg := types.NetworkMiddlewareConfig{
			Name:       name,
			Middleware: mw.Middleware,
			Order:      mw.Order,
			OnError:    mw.OnError,
			Config:     mw.Config, // always present (empty map ok)
		}
		if cfg.Config == nil {
			cfg.Config = map[string]any{}
		}
		if mw.Endpoints != nil {
			cfg.Endpoints = &types.MiddlewareEndpointSelector{
				Include: mw.Endpoints.Include,
				Exclude: mw.Endpoints.Exclude,
			}
		}
		out[key] = cfg
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func tlsModeToSDK(s string) (types.NetworkTLSMode, error) {
	switch s {
	case "":
		return types.NetworkTLSModeUnspecified, nil
	case "skip":
		return types.NetworkTLSModeSkip, nil
	case "terminate":
		return types.NetworkTLSModeTerminate, nil
	case "passthrough":
		return types.NetworkTLSModePassthrough, nil
	default:
		return 0, fmt.Errorf("unknown TLS mode %q (expected skip, terminate, or passthrough)", s)
	}
}

func tlsModeFromSDK(m types.NetworkTLSMode) string {
	switch m {
	case types.NetworkTLSModeSkip:
		return "skip"
	case types.NetworkTLSModeTerminate:
		return "terminate"
	case types.NetworkTLSModePassthrough:
		return "passthrough"
	default:
		return ""
	}
}

func enforcementToSDK(s string) (types.NetworkEnforcementMode, error) {
	switch s {
	case "":
		return types.NetworkEnforcementModeUnspecified, nil
	case "enforce":
		return types.NetworkEnforcementModeEnforce, nil
	case "audit":
		return types.NetworkEnforcementModeAudit, nil
	default:
		return 0, fmt.Errorf("unknown enforcement mode %q (expected enforce or audit)", s)
	}
}

func enforcementFromSDK(m types.NetworkEnforcementMode) string {
	switch m {
	case types.NetworkEnforcementModeEnforce:
		return "enforce"
	case types.NetworkEnforcementModeAudit:
		return "audit"
	default:
		return ""
	}
}

func accessPresetToSDK(s string) (types.NetworkAccessPreset, error) {
	switch s {
	case "":
		return types.NetworkAccessPresetUnspecified, nil
	case "read-only":
		return types.NetworkAccessPresetReadOnly, nil
	case "read-write":
		return types.NetworkAccessPresetReadWrite, nil
	case "full":
		return types.NetworkAccessPresetFull, nil
	default:
		return 0, fmt.Errorf("unknown access preset %q (expected read-only, read-write, or full)", s)
	}
}

func accessPresetFromSDK(m types.NetworkAccessPreset) string {
	switch m {
	case types.NetworkAccessPresetReadOnly:
		return "read-only"
	case types.NetworkAccessPresetReadWrite:
		return "read-write"
	case types.NetworkAccessPresetFull:
		return "full"
	default:
		return ""
	}
}

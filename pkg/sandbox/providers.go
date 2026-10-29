package sandbox

import (
	"fmt"
	"strings"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

// providerAliases is the full normalize_provider_type table (Appendix A.4,
// crates/openshell-providers/src/lib.rs:188-207 + core/inference.rs:219-231).
// Keys are already lowercased; input is trimmed+lowercased before lookup.
var providerAliases = map[string]string{
	"openai":           "openai",
	"anthropic":        "anthropic",
	"nvidia":           "nvidia",
	"deepinfra":        "deepinfra",
	"aws-bedrock":      "aws-bedrock",
	"google-vertex-ai": "google-vertex-ai",
	"vertex":           "google-vertex-ai",
	"vertex-ai":        "google-vertex-ai",
	"google-vertex":    "google-vertex-ai",
	"gcp-vertex":       "google-vertex-ai",
	"claude":           "claude-code",
	"claude-code":      "claude-code",
	"claude_code":      "claude-code",
	"codex":            "codex",
	"copilot":          "copilot",
	"opencode":         "opencode",
	"gcp":              "google-cloud",
	"google-cloud":     "google-cloud",
	"generic":          "generic",
	"gitlab":           "gitlab",
	"glab":             "gitlab",
	"github":           "github",
	"gh":               "github",
	"outlook":          "outlook",
}

// NormalizeProviderType maps an input to its canonical provider type. The bool
// is false when the input is not a recognised alias.
func NormalizeProviderType(s string) (string, bool) {
	canon, ok := providerAliases[strings.ToLower(strings.TrimSpace(s))]
	return canon, ok
}

// ProviderResolution is the outcome of resolving requested/inferred providers.
type ProviderResolution struct {
	Names        []string // existing provider names to attach
	MissingTypes []string // recognised types with no existing provider (auto-create candidates)
}

const providerNotFoundMsg = "provider '%s' not found and '%s' is not a recognized provider type. Create it first with `openshell provider create --type <type> --name <name>`"

// ErrProviderNotFound reports a requested provider that is neither an existing
// name nor a recognised type alias.
type ErrProviderNotFound struct{ Name string }

func (e *ErrProviderNotFound) Error() string {
	return fmt.Sprintf(providerNotFoundMsg, e.Name, e.Name)
}

// ResolveProviders resolves requested names and inferred types against the known
// providers (ensure_required_providers, run.rs:2561-2675). Requested names that
// exist are kept; a name that is a type alias but not an existing name becomes a
// MissingType (auto-create candidate); anything else errors. Inferred types are
// resolved via a first-seen lowercase-type→name map; unresolved inferred types
// become MissingTypes. Results are de-duplicated.
func ResolveProviders(known []*types.Provider, requested, inferredTypes []string) (ProviderResolution, error) {
	nameSet := map[string]bool{}
	typeToName := map[string]string{} // first-seen type → name
	for _, p := range known {
		nameSet[p.Name] = true
		lt := strings.ToLower(p.Type)
		if _, seen := typeToName[lt]; !seen {
			typeToName[lt] = p.Name
		}
	}

	var res ProviderResolution
	addedName := map[string]bool{}
	addedType := map[string]bool{}
	addName := func(n string) {
		if !addedName[n] {
			addedName[n] = true
			res.Names = append(res.Names, n)
		}
	}
	addMissing := func(tp string) {
		if !addedType[tp] {
			addedType[tp] = true
			res.MissingTypes = append(res.MissingTypes, tp)
		}
	}

	for _, req := range requested {
		if nameSet[req] {
			addName(req)
			continue
		}
		if canon, ok := NormalizeProviderType(req); ok {
			addMissing(canon)
			continue
		}
		return ProviderResolution{}, &ErrProviderNotFound{Name: req}
	}

	for _, inf := range inferredTypes {
		canon, ok := NormalizeProviderType(inf)
		if !ok {
			continue
		}
		if name, exists := typeToName[strings.ToLower(canon)]; exists {
			addName(name)
			continue
		}
		addMissing(canon)
	}

	return res, nil
}

const autoProviderUnsupportedBase = "missing required provider '%s'. Create it first with `openshell provider create --type %s --name %s --from-existing`, pass --auto-providers to auto-create, or set it up manually from inside the sandbox"

// ErrAutoProviderUnsupported reports a missing provider type that openshellctl
// cannot auto-create (it has no local credential access). Message is the
// upstream non-interactive text plus an openshellctl clarification (§5.5).
type ErrAutoProviderUnsupported struct{ Type string }

func (e *ErrAutoProviderUnsupported) Error() string {
	return fmt.Sprintf(autoProviderUnsupportedBase, e.Type, e.Type, e.Type) +
		" (openshellctl cannot auto-create providers: it has no access to local credential files)"
}

// SkipProviderMessage is the stderr line printed for a skipped provider under
// --no-auto-providers.
func SkipProviderMessage(providerType string) string {
	return fmt.Sprintf("! Skipping provider '%s' (--no-auto-providers)", providerType)
}

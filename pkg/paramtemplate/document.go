package paramtemplate

import (
	"fmt"
	"regexp"

	yaml "go.yaml.in/yaml/v3"
)

// paramRefRe matches a ${KEY} reference, KEY per paramKeyRe's grammar.
// Whether a given match is an actual reference or an escaped "$${KEY}"
// literal depends on the character preceding it — checked by callers via
// isEscaped, since RE2 (Go's regexp) has no look-behind.
var paramRefRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// ParseDocument parses manifest YAML into a *yaml.Node document, the shape
// CollectReferences and Substitute operate on. Parsing (not substituting on
// raw file text) is what lets a PROMPT value containing a quote, a colon, or
// a newline pass through safely: substitution below acts on the decoded Go
// string value of each scalar, not on the surrounding YAML syntax.
func ParseDocument(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &doc, nil
}

// Render re-serializes a document (after Substitute) back to YAML bytes for
// v1alpha1.Decode.
func Render(doc *yaml.Node) ([]byte, error) {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("render manifest: %w", err)
	}
	return out, nil
}

// CollectReferences returns the sorted, deduplicated set of parameter keys
// referenced anywhere in doc's string scalars (map keys excluded; see
// walkStringScalars), or nil if there are none.
func CollectReferences(doc *yaml.Node) []string {
	refs := map[string]bool{}
	walkStringScalars(doc, func(n *yaml.Node) {
		for _, ref := range scanRefs(n.Value) {
			refs[ref.key] = true
		}
	})
	return sortedKeys(refs)
}

// paramRef is one unescaped ${KEY} occurrence within a string, by byte
// offset into that string.
type paramRef struct {
	start, end int
	key        string
}

// scanRefs finds every unescaped ${KEY} occurrence in s. A match
// immediately preceded by a literal '$' (i.e. part of "$${KEY}") is treated
// as a literal, not a reference, and excluded — this card's only escape
// shape (see the package doc comment on Substitute for the full rule).
func scanRefs(s string) []paramRef {
	var refs []paramRef
	for _, loc := range paramRefRe.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && s[start-1] == '$' {
			continue
		}
		refs = append(refs, paramRef{start: start, end: end, key: s[start+2 : end-1]})
	}
	return refs
}

// walkStringScalars calls visit on every "!!str"-tagged scalar node reachable
// from n, excluding mapping keys — "every string scalar anywhere in the
// document (metadata.*, spec.*, map keys excluded)" per the card. Sequence
// and mapping *values* are walked; mapping *keys* (Content[i] for even i) are
// not.
func walkStringScalars(n *yaml.Node, visit func(*yaml.Node)) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			walkStringScalars(c, visit)
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			walkStringScalars(n.Content[i], visit)
		}
	case yaml.ScalarNode:
		if n.Tag == "!!str" {
			visit(n)
		}
	}
}

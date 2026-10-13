package paramtemplate

import (
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Substitute resolves every ${KEY} reference in doc's string scalars (map
// keys excluded, same walk as CollectReferences) against values, mutating
// doc's scalar nodes in place. It returns the sorted, deduplicated set of
// referenced keys that values had no entry for, or nil if everything
// resolved — the caller must treat any non-nil result as fatal: "never send
// a literal ${KEY} to the gateway" means an unresolved manifest must not be
// rendered/decoded any further.
//
// A "$${KEY}" occurrence is left exactly as written — not substituted, and
// never reported as missing even when KEY has no value — since it is a
// literal by this card's rule, not a reference.
func Substitute(doc *yaml.Node, values map[string]string) []string {
	missing := map[string]bool{}
	walkStringScalars(doc, func(n *yaml.Node) {
		n.Value = substituteString(n.Value, values, missing)
	})
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for k := range missing {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// substituteString replaces every unescaped ${KEY} in s with values[KEY],
// recording KEY in missing (and leaving the literal "${KEY}" text in place)
// when values has no entry for it.
func substituteString(s string, values map[string]string, missing map[string]bool) string {
	refs := scanRefs(s)
	if len(refs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, ref := range refs {
		b.WriteString(s[last:ref.start])
		if v, ok := values[ref.key]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[ref.start:ref.end])
			missing[ref.key] = true
		}
		last = ref.end
	}
	b.WriteString(s[last:])
	return b.String()
}

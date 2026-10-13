package paramtemplate

import "sort"

// MergeParams merges values from --param-file sources, in the order given
// (a later file overrides an earlier one for the same key), then overlays
// --param flag values, in the order given (a later --param overrides an
// earlier one). A --param value always wins over any file-sourced value for
// the same key. It returns the merged values and the sorted, deduplicated
// set of keys where a --param value overrode a file-sourced value, so the
// caller can warn about it.
func MergeParams(fileSources []map[string]string, flagParams []Param) (map[string]string, []string) {
	values := map[string]string{}
	for _, m := range fileSources {
		for k, v := range m {
			values[k] = v
		}
	}
	fromFile := make(map[string]bool, len(values))
	for k := range values {
		fromFile[k] = true
	}

	overriddenSet := map[string]bool{}
	for _, p := range flagParams {
		if fromFile[p.Key] {
			overriddenSet[p.Key] = true
		}
		values[p.Key] = p.Value
	}

	return values, sortedKeys(overriddenSet)
}

// UnknownKeys returns the sorted, deduplicated keys of values that do not
// appear in refs — the set of supplied parameters the manifest never
// references.
func UnknownKeys(values map[string]string, refs []string) []string {
	referenced := make(map[string]bool, len(refs))
	for _, r := range refs {
		referenced[r] = true
	}
	unknown := map[string]bool{}
	for k := range values {
		if !referenced[k] {
			unknown[k] = true
		}
	}
	return sortedKeys(unknown)
}

// sortedKeys returns the sorted keys of a set (map[string]bool with all-true
// values), or nil for an empty set — matching reflect.DeepEqual's
// nil-vs-empty-slice comparison used throughout this package's tests.
func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

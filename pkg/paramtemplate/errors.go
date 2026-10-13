package paramtemplate

import (
	"fmt"
	"strings"
)

// MalformedParamError reports a --param or --param-file entry that is not
// valid KEY=VALUE: either there is no '=', or KEY does not match
// [A-Za-z_][A-Za-z0-9_]*. Source is "--param" for a flag entry, or the
// --param-file path for a file entry (with Line set to its 1-based line
// number).
//
// The entry's VALUE is deliberately never captured here, in either shape:
// param values must never be logged, including in error output (the card's
// explicit security requirement) — only the key half of a well-formed-but-
// invalid "KEY=..." may appear in Error(), and when there is no '=' at all
// there is no reliable key/value split to begin with, so nothing from the
// raw entry is echoed.
type MalformedParamError struct {
	Key       string // the (invalid) key, set only when HasEquals is true
	HasEquals bool   // whether an '=' was found in the entry at all
	Source    string
	Line      int
}

func (e *MalformedParamError) Error() string {
	if !e.HasEquals {
		return fmt.Sprintf("%s: malformed parameter entry, expected KEY=VALUE (no '=' found)", e.location())
	}
	return fmt.Sprintf("%s: invalid parameter key %q, expected KEY matching [A-Za-z_][A-Za-z0-9_]*", e.location(), e.Key)
}

// location formats Source (with Line, for a --param-file entry) the same way
// both branches of Error() need it.
func (e *MalformedParamError) location() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d", e.Source, e.Line)
	}
	return e.Source
}

// ParamFileUnreadableError wraps a failure to open or read a --param-file.
type ParamFileUnreadableError struct {
	Path string
	Err  error
}

func (e *ParamFileUnreadableError) Error() string {
	return fmt.Sprintf("reading --param-file %q: %v", e.Path, e.Err)
}

func (e *ParamFileUnreadableError) Unwrap() error { return e.Err }

// UnresolvedParamsError lists every ${KEY} reference left unresolved after
// substitution (sorted, deduplicated) — a manifest must never be sent to the
// gateway with a literal ${KEY} in it.
type UnresolvedParamsError struct{ Keys []string }

func (e *UnresolvedParamsError) Error() string {
	return fmt.Sprintf("unresolved parameter reference(s): %s", strings.Join(e.Keys, ", "))
}

// UnknownParamsError lists --param/--param-file keys the manifest does not
// reference (sorted) — triggered unless --ignore-unknown-parameters is set.
type UnknownParamsError struct{ Keys []string }

func (e *UnknownParamsError) Error() string {
	return fmt.Sprintf("parameter(s) not referenced by manifest: %s (use --ignore-unknown-parameters to allow)",
		strings.Join(e.Keys, ", "))
}

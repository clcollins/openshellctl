// Package paramtemplate implements oc-process-style parameter substitution
// for `sandbox create -f` manifests (ROSAENG-74244): parsing KEY=VALUE
// parameters from --param and --param-file, merging them by precedence,
// collecting ${KEY} references from a parsed YAML document, and
// substituting them before the manifest is decoded/validated.
//
// Every decision here is a pure function — no I/O, no globals — except
// LoadParamFile, a deliberately tiny shell around an injected fs.FS.
package paramtemplate

import (
	"regexp"
)

// paramKeyRe is the KEY grammar for both --param and --param-file entries:
// an identifier, matching oc process's PARAMETER_NAME convention.
var paramKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Param is one resolved KEY=VALUE parameter.
type Param struct {
	Key   string
	Value string
}

// ParseParam parses one "KEY=VALUE" entry from a --param flag. Split is on
// the first '=' only, so a VALUE may itself contain '=' or ','; KEY must
// match [A-Za-z_][A-Za-z0-9_]*. Returns a *MalformedParamError (Source:
// "--param") when raw has no '=' or KEY is invalid. The error never carries
// the VALUE half of raw — see MalformedParamError's doc comment.
func ParseParam(raw string) (Param, error) {
	return parseParamEntry(raw, "--param", 0)
}

// parseParamEntry is the shared KEY=VALUE parser for both the --param flag
// form and each line of a --param-file; source/line identify the entry in
// any resulting error. It deliberately never puts raw's VALUE half into the
// Param-less error path — a malformed entry's error must report only a key,
// or nothing at all when there is no '=' to split on.
func parseParamEntry(raw, source string, line int) (Param, error) {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '=' {
			key, value := raw[:i], raw[i+1:]
			if !paramKeyRe.MatchString(key) {
				return Param{}, &MalformedParamError{Key: key, HasEquals: true, Source: source, Line: line}
			}
			return Param{Key: key, Value: value}, nil
		}
	}
	return Param{}, &MalformedParamError{HasEquals: false, Source: source, Line: line}
}

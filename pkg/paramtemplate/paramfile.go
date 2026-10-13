package paramtemplate

import (
	"io/fs"
	"strings"
)

// ParseParamFileContent parses a --param-file's content: one KEY=VALUE per
// line, '#'-prefixed lines and blank/whitespace-only lines ignored. path
// identifies the file in any resulting *MalformedParamError. A later
// duplicate key within the same file overwrites an earlier one.
func ParseParamFileContent(path string, data []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := parseParamEntry(line, path, i+1)
		if err != nil {
			return nil, err
		}
		out[p.Key] = p.Value
	}
	return out, nil
}

// LoadParamFile reads and parses one --param-file via fsys — the thin I/O
// shell around the pure ParseParamFileContent, so the CLI layer never
// touches the filesystem directly and tests inject fstest.MapFS instead of
// a real one. A read failure (missing file, permission denied, ...) is
// wrapped in a *ParamFileUnreadableError naming path.
func LoadParamFile(fsys fs.FS, path string) (map[string]string, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, &ParamFileUnreadableError{Path: path, Err: err}
	}
	return ParseParamFileContent(path, data)
}

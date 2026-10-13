package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/pkg/api/v1alpha1"
	"github.com/openshift-online/openshellctl/pkg/paramtemplate"
)

// templateFlags groups sandbox create's manifest-templating flags
// (ROSAENG-74244): -p/--param, --param-file, --parameters,
// --ignore-unknown-parameters.
type templateFlags struct {
	params                  []string
	paramFiles              []string
	printParameters         bool
	ignoreUnknownParameters bool
}

// osFS is the fs.FS used to read --param-file paths in production. It opens
// via os.Open directly rather than os.DirFS, so it accepts the same absolute
// or relative paths --param-file users pass today, with none of
// os.DirFS/fs.ValidPath's rooted-filesystem restrictions. Tests inject
// fstest.MapFS instead (pkg/paramtemplate's own tests, and
// sandbox_create_params_test.go's CLI-layer tests).
type osFS struct{}

func (osFS) Open(name string) (fs.File, error) { return os.Open(name) }

// needsTemplating is the pure decision behind loadManifestWithParams's
// round-trip skip: a manifest with no "${" substring at all and no template
// flags needs no templating, full stop — decodeManifestBytes(data) runs on
// the original bytes and nothing else in this file is ever called. This
// substring check is deliberately a cheap pre-filter, not the real
// reference scan (CollectReferences, which needs a parsed document); it
// only has to be conservative (never say "no" when a real ${KEY} reference
// is present), not precise (it may say "yes" for an escaped $${KEY} or a
// manifest where --param was given but is unreferenced — the no-refs
// fallback below returns raw unchanged in exactly those cases).
func needsTemplating(data []byte, tf templateFlags) bool {
	return tf.printParameters || len(tf.params) > 0 || len(tf.paramFiles) > 0 || bytes.Contains(data, []byte("${"))
}

// loadManifestWithParams is sandbox create's one orchestration function for
// -f (AGENTS.md's N+1 rule: RunE calls this, and this calls only pure
// functions — paramtemplate.* and decodeManifestBytes below). It owns all
// the I/O: reading the manifest (path, or "-" for stdin), reading each
// --param-file through fsys, and writing --parameters output/override
// warnings to cmd's stdout/stderr.
//
// When templating isn't needed at all (needsTemplating is false), it
// decodes the original bytes unchanged — byte-for-byte identical to
// today's behavior, no YAML round-trip, so no reformatting drift for a
// manifest that never uses this feature. Otherwise it parses the manifest,
// collects its ${KEY} references, resolves --param/--param-file values
// (warning on stderr about any --param that overrides a --param-file
// value), rejects unknown parameters unless --ignore-unknown-parameters is
// set, substitutes, and decodes the result. When refs turns out to be empty
// after all (the substring pre-filter can't distinguish a real ${KEY} from
// an escaped $${KEY} or bare $KEY), it falls back to decoding the original
// bytes too, for the same no-reformatting reason.
//
// --parameters short-circuits right after collecting references: it writes
// the manifest's parameter names to cmd's stdout and returns (nil, true,
// nil) — the caller must create nothing and exit 0. Combining --parameters
// with --param/--param-file is a usage error (oc process, the convention
// this card's flags mirror, rejects the same combination) rather than
// silently ignoring the values, since a user who passed both almost
// certainly intended one or the other.
//
// Only `sandbox create`'s own RunE calls this — every other -f consumer
// (doctor, namesFromManifest for get/connect/upload/download/ssh-config/
// logs/stop/start/delete/provider) goes through the plain loadManifest
// below, by design: ROSAENG-74244 is scoped to `sandbox create` only, and a
// manifest with a non-template "${...}"-shaped string (e.g. a shell command
// embedding "${HOME}") being treated as a required, now-unresolved
// parameter is an intentional consequence of adopting template syntax (the
// same tradeoff oc process makes) that must stay contained to the one
// command that opts into it.
func loadManifestWithParams(cmd *cobra.Command, fsys fs.FS, path string, tf templateFlags) (*v1alpha1.Sandbox, bool, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = readAll(cmd.InOrStdin())
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, false, err
	}

	if !needsTemplating(data, tf) {
		m, derr := decodeManifestBytes(data)
		return m, false, derr
	}

	doc, err := paramtemplate.ParseDocument(data)
	if err != nil {
		return nil, false, err
	}
	refs := paramtemplate.CollectReferences(doc)

	if tf.printParameters {
		if len(tf.params) > 0 || len(tf.paramFiles) > 0 {
			return nil, false, &UsageError{Err: fmt.Errorf(
				"--parameters cannot be combined with --param/--param-file")}
		}
		for _, k := range refs {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), k)
		}
		return nil, true, nil
	}

	flagParams := make([]paramtemplate.Param, 0, len(tf.params))
	for _, raw := range tf.params {
		p, perr := paramtemplate.ParseParam(raw)
		if perr != nil {
			return nil, false, &UsageError{Err: perr}
		}
		flagParams = append(flagParams, p)
	}

	fileSources := make([]map[string]string, 0, len(tf.paramFiles))
	for _, p := range tf.paramFiles {
		m, ferr := paramtemplate.LoadParamFile(fsys, p)
		if ferr != nil {
			return nil, false, &UsageError{Err: ferr}
		}
		fileSources = append(fileSources, m)
	}

	values, overridden := paramtemplate.MergeParams(fileSources, flagParams)
	for _, k := range overridden {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: --param %s overrides value from --param-file\n", k)
	}

	if !tf.ignoreUnknownParameters {
		if unknown := paramtemplate.UnknownKeys(values, refs); len(unknown) > 0 {
			return nil, false, &UsageError{Err: &paramtemplate.UnknownParamsError{Keys: unknown}}
		}
	}

	if len(refs) == 0 {
		m, derr := decodeManifestBytes(data)
		return m, false, derr
	}

	if missing := paramtemplate.Substitute(doc, values); len(missing) > 0 {
		return nil, false, &UsageError{Err: &paramtemplate.UnresolvedParamsError{Keys: missing}}
	}

	substituted, err := paramtemplate.Render(doc)
	if err != nil {
		return nil, false, err
	}
	m, err := decodeManifestBytes(substituted)
	return m, false, err
}

// decodeManifestBytes is loadManifest's original tail end: decode+validate,
// shared by the templating and non-templating paths so both produce
// identical errors for the same (possibly substituted) manifest bytes.
func decodeManifestBytes(data []byte) (*v1alpha1.Sandbox, error) {
	m, err := v1alpha1.Decode(bytesReader(data))
	if err != nil {
		return nil, err
	}
	if errs := m.Validate(); len(errs) > 0 {
		return nil, &UsageError{Err: fmt.Errorf("manifest invalid: %v", errs[0])}
	}
	return m, nil
}

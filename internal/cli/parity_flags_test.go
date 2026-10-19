package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// flagEntry is one documented flag in either parity JSON file. Only Long is
// used for presence-checking — the schema's other fields (type, default,
// conflicts, ...) are informational/for humans, not verified against cobra's
// actual registration here.
type flagEntry struct {
	Long string `json:"long"`
}

// parityDoc is the shared shape of both hack/parity/*.json files: a flat map
// of subcommand name -> its documented flags.
type parityDoc struct {
	Subcommands map[string][]flagEntry `json:"subcommands"`
}

func loadParityDoc(t *testing.T, path string) parityDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc parityDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return doc
}

// documentedFlagNames returns the set of flag long-names documented for
// subcommand across both parity files.
func documentedFlagNames(t *testing.T, subcommand string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, doc := range []parityDoc{
		loadParityDoc(t, "../../hack/parity/sandbox_flags_v0.1.3.json"),
		loadParityDoc(t, "../../hack/parity/openshellctl_extensions.json"),
	} {
		for _, f := range doc.Subcommands[subcommand] {
			names[f.Long] = true
		}
	}
	return names
}

// actualFlagNames returns every flag registered directly on cmd (not
// inherited from a parent command, e.g. --gateway/--workspace), by long name.
func actualFlagNames(cmd *cobra.Command) map[string]bool {
	names := map[string]bool{}
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		names[f.Name] = true
	})
	return names
}

// TestParity_CreateFlagsFullyDocumented is the "flag diff" parity_test.go's
// own doc comment on implementedSubcommands anticipates: for every
// subcommand listed there, every flag cobra actually registers must appear
// in the upstream parity JSON or openshellctl_extensions.json (and vice
// versa, catching stale entries) — so a future flag added to an
// implemented subcommand without being recorded fails this test, not a
// silent documentation gap.
func TestParity_CreateFlagsFullyDocumented(t *testing.T) {
	if !implementedSubcommands["create"] {
		t.Fatal("this test assumes \"create\" is in implementedSubcommands")
	}
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := NewRootCommand()
	create, ok := directChild(root, "sandbox", "create")
	if !ok {
		t.Fatal("sandbox create command missing")
	}

	documented := documentedFlagNames(t, "create")
	actual := actualFlagNames(create)

	for name := range actual {
		if !documented[name] {
			t.Errorf("flag --%s is registered on `sandbox create` but not documented in "+
				"hack/parity/sandbox_flags_v0.1.3.json or hack/parity/openshellctl_extensions.json", name)
		}
	}
	for name := range documented {
		if !actual[name] {
			t.Errorf("hack/parity/*.json documents --%s for create, but cobra does not register it — stale entry?", name)
		}
	}
}

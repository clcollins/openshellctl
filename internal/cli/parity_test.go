package cli

import (
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// the13Subcommands is the set of leaf subcommands openshellctl must expose for
// parity with `openshell` at v0.1.3 (spec §6). "provider" counts as one entry
// here (its list/attach/detach children are checked separately below); "logs"
// is top-level. Total = 13.
var the13Subcommands = []string{
	"create", "get", "list", "delete", "stop", "start",
	"exec", "connect", "upload", "download", "ssh-config",
	"provider", "logs",
}

// implementedSubcommands lists subcommands whose full flag-parity is enforced.
// As commands land, move their name here from the TODO list and the flag diff
// (TestParity_CreateFlagsFullyDocumented, parity_flags_test.go) applies.
// "create" is the first — ROSAENG-68832.
var implementedSubcommands = map[string]bool{"create": true}

func collectCommandNames(root *cobra.Command) map[string]*cobra.Command {
	names := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			names[child.Name()] = child
			walk(child)
		}
	}
	walk(root)
	return names
}

// directChild walks a specific path of direct children from parent (e.g.
// directChild(root, "sandbox", "create"), or a single-segment
// directChild(root, "provider")), unlike collectCommandNames' flat
// whole-tree map keyed by leaf name alone. Use this whenever two
// subcommands can share a leaf name at different depths — the top-level
// `provider` CRUD command vs. `sandbox`'s own `provider` child, or
// `sandbox create` vs. a hypothetical future `provider create` — where a
// flat lookup by leaf name alone would silently pick whichever one cobra
// happens to walk last.
func directChild(parent *cobra.Command, path ...string) (*cobra.Command, bool) {
	cur := parent
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func TestParity_AllThirteenSubcommandsExist(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := NewRootCommand()
	names := collectCommandNames(root)

	for _, want := range the13Subcommands {
		if _, ok := names[want]; !ok {
			t.Errorf("subcommand %q is missing from the command tree", want)
		}
	}

	if len(the13Subcommands) != 13 {
		t.Fatalf("expected exactly 13 parity subcommands, listed %d", len(the13Subcommands))
	}
}

func TestParity_SandboxProviderHasListAttachDetach(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := NewRootCommand()

	sb, ok := directChild(root, "sandbox")
	if !ok {
		t.Fatal("sandbox command missing")
	}
	prov, ok := directChild(sb, "provider")
	if !ok {
		t.Fatal("sandbox provider command missing")
	}
	children := map[string]bool{}
	for _, c := range prov.Commands() {
		children[c.Name()] = true
	}
	for _, want := range []string{"list", "attach", "detach"} {
		if !children[want] {
			t.Errorf("sandbox provider is missing child %q", want)
		}
	}
}

// TestParity_TopLevelProviderHasCRUD guards the new top-level `provider`
// command's own subtree — collectCommandNames' flat map can't distinguish it
// from sandbox's same-named `provider` child (see directChild), so this
// resolves it unambiguously via root's direct children instead.
func TestParity_TopLevelProviderHasCRUD(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := NewRootCommand()

	prov, ok := directChild(root, "provider")
	if !ok {
		t.Fatal("top-level provider command missing")
	}
	children := map[string]bool{}
	for _, c := range prov.Commands() {
		children[c.Name()] = true
	}
	for _, want := range []string{"create", "get", "list", "update", "delete"} {
		if !children[want] {
			t.Errorf("provider is missing child %q", want)
		}
	}
}

func TestParity_SandboxHasSbAlias(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	root := NewRootCommand()
	names := collectCommandNames(root)
	sb, ok := names["sandbox"]
	if !ok {
		t.Fatal("sandbox command missing")
	}
	found := false
	for _, a := range sb.Aliases {
		if a == "sb" {
			found = true
		}
	}
	if !found {
		t.Errorf("sandbox is missing the 'sb' alias, got %v", sb.Aliases)
	}
}

// TestParity_UnimplementedTODO records which subcommands still need flag-parity
// wiring. It is informational (t.Log) and fails only if the list drifts out of
// sync with what is claimed implemented, so the TODO cannot silently rot.
func TestParity_UnimplementedTODO(t *testing.T) {
	var todo []string
	for _, name := range the13Subcommands {
		if !implementedSubcommands[name] {
			todo = append(todo, name)
		}
	}
	sort.Strings(todo)
	t.Logf("TODO: flag-parity not yet enforced for: %v", todo)

	for name := range implementedSubcommands {
		found := false
		for _, s := range the13Subcommands {
			if s == name {
				found = true
			}
		}
		if !found {
			t.Errorf("implementedSubcommands lists %q which is not a parity subcommand", name)
		}
	}
}

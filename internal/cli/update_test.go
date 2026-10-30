package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/viper"
)

func TestUpdateCommand_UnknownFlagExitsUsage(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--unknown-flag"})

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for unknown flag, got nil")
	}
	code := exitCodeFor(err)
	if code != ExitUsage {
		t.Errorf("exitCodeFor = %d, want %d (ExitUsage)", code, ExitUsage)
	}
}

func TestUpdateCommand_CheckFlagAccepted(t *testing.T) {
	// Verify --check is a recognised flag (parse-only; RunE is not called
	// by Traverse + ParseFlags, which is the same approach as TestDocsExamplesParse).
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	cmd, remaining, err := root.Traverse([]string{"update", "--check"})
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if cmd.Name() != "update" {
		t.Fatalf("command = %q, want update", cmd.Name())
	}
	if err := cmd.ParseFlags(remaining); err != nil {
		t.Errorf("ParseFlags(--check): %v", err)
	}
}

func TestUpdateCommand_VersionFlagAccepted(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	cmd, remaining, err := root.Traverse([]string{"update", "--version", "v0.1.5"})
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if err := cmd.ParseFlags(remaining); err != nil {
		t.Errorf("ParseFlags(--version v0.1.5): %v", err)
	}
}

func TestUpdateCommand_PrereleaseFlagAccepted(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	cmd, remaining, err := root.Traverse([]string{"update", "--prerelease"})
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if err := cmd.ParseFlags(remaining); err != nil {
		t.Errorf("ParseFlags(--prerelease): %v", err)
	}
}

func TestUpdateCommand_HelpText(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	cmd, _, err := root.Traverse([]string{"update"})
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}

	help := cmd.UsageString()
	for _, want := range []string{"--version", "--check", "--prerelease"} {
		if !bytes.Contains([]byte(help), []byte(want)) {
			t.Errorf("help text missing %q", want)
		}
	}
}

func TestUpdateCommand_InRootCommandTree(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	root := NewRootCommand()
	names := collectCommandNames(root)
	if _, ok := names["update"]; !ok {
		t.Error("update command not found in root command tree")
	}
}

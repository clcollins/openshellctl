package version

import (
	"runtime/debug"
	"testing"
)

func TestGet_UsesInjectedValues(t *testing.T) {
	old := Version
	oldC := Commit
	oldP := OpenShellPin
	t.Cleanup(func() { Version, Commit, OpenShellPin = old, oldC, oldP })

	Version = "v1.2.3"
	Commit = "abc123"
	OpenShellPin = "v0.0.116"

	got := Get()
	if got.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", got.Version)
	}
	if got.Commit != "abc123" {
		t.Errorf("Commit = %q, want abc123", got.Commit)
	}
	if got.OpenShellPin != "v0.0.116" {
		t.Errorf("OpenShellPin = %q, want v0.0.116", got.OpenShellPin)
	}
}

func TestGet_FallsBackToMainVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() {
		Version = old
		readBuildInfo = debug.ReadBuildInfo
	})
	Version = "dev"
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.2.1"}}, true
	}

	got := Get()
	if got.Version != "v0.2.1" {
		t.Errorf("Version = %q, want v0.2.1 (from build info)", got.Version)
	}
}

func TestGet_KeepsDevWhenMainIsDevel(t *testing.T) {
	old := Version
	t.Cleanup(func() {
		Version = old
		readBuildInfo = debug.ReadBuildInfo
	})
	Version = "dev"
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}

	got := Get()
	if got.Version != "dev" {
		t.Errorf("Version = %q, want dev (devel is not valid semver)", got.Version)
	}
}

func TestGet_KeepsDevWhenNoBuildInfo(t *testing.T) {
	old := Version
	t.Cleanup(func() {
		Version = old
		readBuildInfo = debug.ReadBuildInfo
	})
	Version = "dev"
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }

	got := Get()
	if got.Version != "dev" {
		t.Errorf("Version = %q, want dev when no build info", got.Version)
	}
}

func TestSet_InstallMethod(t *testing.T) {
	old := InstallMethod
	t.Cleanup(func() { InstallMethod = old })

	Set("", "", "", "container")
	if InstallMethod != "container" {
		t.Errorf("InstallMethod = %q, want container", InstallMethod)
	}
}

func TestSet_EmptyInstallMethodIgnored(t *testing.T) {
	old := InstallMethod
	t.Cleanup(func() { InstallMethod = old })

	InstallMethod = "container"
	Set("", "", "", "")
	if InstallMethod != "container" {
		t.Errorf("InstallMethod = %q, want container (empty should not overwrite)", InstallMethod)
	}
}

func TestGet_InstallMethodInInfo(t *testing.T) {
	old := InstallMethod
	t.Cleanup(func() { InstallMethod = old })

	InstallMethod = "container"
	got := Get()
	if got.InstallMethod != "container" {
		t.Errorf("Info.InstallMethod = %q, want container", got.InstallMethod)
	}
}

func TestSDKVersion_FoundInDeps(t *testing.T) {
	fake := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Deps: []*debug.Module{
			{Path: "github.com/spf13/cobra", Version: "v1.10.2"},
			{Path: sdkModulePath, Version: "v0.0.0-20260828082717-d1155aa70042"},
		}}, true
	}
	if got := sdkVersion(fake); got != "v0.0.0-20260828082717-d1155aa70042" {
		t.Errorf("sdkVersion = %q, want the SDK pseudo-version", got)
	}
}

func TestSDKVersion_NoBuildInfo(t *testing.T) {
	fake := func() (*debug.BuildInfo, bool) { return nil, false }
	if got := sdkVersion(fake); got != "unknown" {
		t.Errorf("sdkVersion = %q, want unknown", got)
	}
}

func TestSDKVersion_DepAbsent(t *testing.T) {
	fake := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Deps: []*debug.Module{
			{Path: "github.com/spf13/cobra", Version: "v1.10.2"},
		}}, true
	}
	if got := sdkVersion(fake); got != "unknown" {
		t.Errorf("sdkVersion = %q, want unknown", got)
	}
}

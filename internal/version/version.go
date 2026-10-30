// Package version exposes build metadata for openshellctl. The concrete values
// are injected at build time via -ldflags (see the Makefile); the SDK
// pseudo-version is read from the embedded build info at runtime.
package version

import (
	"regexp"
	"runtime/debug"
)

// These hold the resolved build metadata. Defaults are used for `go run` and
// tests where no ldflags are supplied; main.Set overrides them from the
// linker-injected main-package symbols (spec §4).
var (
	// Version is the release version (e.g. "v0.1.0") or "dev".
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// OpenShellPin is the upstream OpenShell tag this build targets. It defaults
	// to the pin baked into the source so `go run` and tests report it without
	// ldflags; an empty ldflag value does not clobber the default.
	OpenShellPin = defaultOpenShellPin
	// InstallMethod is non-empty for managed installs (e.g. "container") and
	// causes `openshellctl update` to refuse with a targeted hint. Injected via
	// -X main.installMethod in the Containerfile; empty for goreleaser builds
	// and `go install`.
	InstallMethod = ""
)

// defaultOpenShellPin is the compiled-in fallback pin, kept in sync with
// hack/openshell-pin (enforced by make verify-pin).
const defaultOpenShellPin = "v0.1.5"

// semverRE matches a canonical semver string with a leading "v" — the minimal
// shape produced by debug.BuildInfo.Main.Version for module-aware builds. Used
// only to distinguish a real release tag from "(devel)" or a git-describe string;
// it does not need to be exhaustive.
var semverRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]`)

// Set applies linker-injected build metadata. Empty values are ignored so an
// unset -ldflags target does not overwrite a sensible default.
func Set(version, commit, openshellPin, installMethod string) {
	if version != "" {
		Version = version
	}
	if commit != "" {
		Commit = commit
	}
	if openshellPin != "" {
		OpenShellPin = openshellPin
	}
	if installMethod != "" {
		InstallMethod = installMethod
	}
}

// sdkModulePath is the module path of the upstream Go SDK dependency.
const sdkModulePath = "github.com/NVIDIA/OpenShell/sdk/go"

// Info is the resolved build metadata.
type Info struct {
	Version       string
	Commit        string
	OpenShellPin  string
	SDKVersion    string
	InstallMethod string
}

// Get resolves the current build metadata. When Version is "dev" and the module
// build info reports a real semver tag (e.g. from `go install …@v0.1.5`), that
// tag is used instead so the version notice works for module-installed builds.
// The SDK version is looked up in the embedded build info; it is "unknown" when
// build info is unavailable (e.g. under `go test` without module info).
func Get() Info {
	v := Version
	if v == "dev" {
		if bi, ok := readBuildInfo(); ok && semverRE.MatchString(bi.Main.Version) {
			v = bi.Main.Version
		}
	}
	return Info{
		Version:       v,
		Commit:        Commit,
		OpenShellPin:  OpenShellPin,
		SDKVersion:    sdkVersion(readBuildInfo),
		InstallMethod: InstallMethod,
	}
}

// readBuildInfo is a seam for testing.
var readBuildInfo = debug.ReadBuildInfo

func sdkVersion(read func() (*debug.BuildInfo, bool)) string {
	bi, ok := read()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == sdkModulePath {
			return dep.Version
		}
	}
	return "unknown"
}

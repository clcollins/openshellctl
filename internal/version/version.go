// Package version exposes build metadata for openshellctl. The concrete values
// are injected at build time via -ldflags (see the Makefile); the SDK
// pseudo-version is read from the embedded build info at runtime.
package version

import "runtime/debug"

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
)

// defaultOpenShellPin is the compiled-in fallback pin, kept in sync with
// hack/openshell-pin (enforced by make verify-pin).
const defaultOpenShellPin = "v0.1.3"

// Set applies linker-injected build metadata. Empty values are ignored so an
// unset -ldflags target does not overwrite a sensible default.
func Set(version, commit, openshellPin string) {
	if version != "" {
		Version = version
	}
	if commit != "" {
		Commit = commit
	}
	if openshellPin != "" {
		OpenShellPin = openshellPin
	}
}

// sdkModulePath is the module path of the upstream Go SDK dependency.
const sdkModulePath = "github.com/NVIDIA/OpenShell/sdk/go"

// Info is the resolved build metadata.
type Info struct {
	Version      string
	Commit       string
	OpenShellPin string
	SDKVersion   string
}

// Get resolves the current build metadata. The SDK version is looked up in the
// embedded build info; it is "unknown" when build info is unavailable (e.g.
// under `go test` without module info).
func Get() Info {
	return Info{
		Version:      Version,
		Commit:       Commit,
		OpenShellPin: OpenShellPin,
		SDKVersion:   sdkVersion(readBuildInfo),
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

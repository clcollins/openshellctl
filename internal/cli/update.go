package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/internal/version"
	"github.com/openshift-online/openshellctl/pkg/selfupdate"
)

// cleanTransport is captured at package-init time, before PersistentPreRunE
// can call applyGatewayInsecureTransport and mutate http.DefaultTransport.
// Update and notice HTTP clients must always verify TLS, regardless of
// --gateway-insecure.
var cleanTransport = func() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}()

func newUpdateCommand() *cobra.Command {
	var (
		targetVersion   string
		allowPrerelease bool
		checkOnly       bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update openshellctl to the latest (or specified) release",
		Long: `Update downloads and installs a new openshellctl release.

By default the latest stable release is selected. Use --prerelease to
also consider pre-releases, or --version to install a specific tag.
--check resolves and reports the target version without downloading.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get()
			current := selfupdate.ParseCurrent(info.Version)

			realExe, err := selfupdate.ResolveExecutable(os.Executable, filepath.EvalSymlinks)
			if err != nil {
				realExe, _ = os.Executable()
			}

			apiTimeout := 15 * time.Second
			dlTimeout := 120 * time.Second
			apiClient := &http.Client{
				Timeout:   apiTimeout,
				Transport: freshTransport(),
			}
			dlClient := &http.Client{
				Timeout:   dlTimeout,
				Transport: freshTransport(),
			}

			fetcher := &selfupdate.GitHubFetcher{
				Client:  apiClient,
				Owner:   "openshift-online",
				Repo:    "openshellctl",
				Version: info.Version,
				Token:   os.Getenv("GITHUB_TOKEN"),
			}

			opts := selfupdate.Options{
				GOOS:            runtime.GOOS,
				GOARCH:          runtime.GOARCH,
				Current:         current,
				TargetTag:       targetVersion,
				AllowPrerelease: allowPrerelease,
				CheckOnly:       checkOnly,
				InstallMethod:   info.InstallMethod,
				Fetcher:         fetcher,
				HTTPClient:      dlClient,
				ExePath:         realExe,
				MaxBytes:        256 << 20,
			}

			result, err := selfupdate.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}

			switch {
			case result.AlreadyAtTarget:
				fmt.Fprintf(cmd.ErrOrStderr(), "openshellctl is already at %s\n", result.TargetTag)
			case result.Ahead:
				fmt.Fprintf(cmd.ErrOrStderr(), "openshellctl %s is ahead of the latest release (%s)\n",
					result.CurrentTag, result.TargetTag)
			case checkOnly:
				if result.CurrentTag != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "openshellctl %s → %s available\n", result.CurrentTag, result.TargetTag)
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "openshellctl update available: %s\n", result.TargetTag)
				}
			case result.Downgrade:
				fmt.Fprintf(cmd.ErrOrStderr(), "downgraded openshellctl %s → %s (%s)\n",
					result.CurrentTag, result.TargetTag, opts.ExePath)
			default:
				var fromStr string
				if result.CurrentTag != "" {
					fromStr = result.CurrentTag + " → "
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "updated openshellctl %s%s (%s)\n",
					fromStr, result.TargetTag, opts.ExePath)
				fmt.Fprintf(cmd.ErrOrStderr(), "note: release signatures are not yet verified (checksum only)\n")
			}

			// Refresh the notice cache so the next command doesn't re-announce
			// the version we just installed.
			if result.Updated {
				cacheDir := selfupdate.CacheDir(os.Getenv)
				cachePath := filepath.Join(cacheDir, "update-check.json")
				_ = selfupdate.SaveCache(cachePath, selfupdate.Cache{
					CheckedAt: time.Now().UTC(),
					LatestTag: result.TargetTag,
				})
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&targetVersion, "version", "", "release tag to install (e.g. v0.1.5 or 0.1.5)")
	cmd.Flags().BoolVar(&allowPrerelease, "prerelease", false, "allow selection of pre-release versions")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "resolve and report only; do not download or replace")

	return cmd
}

// freshTransport returns a clone of the transport captured before
// applyGatewayInsecureTransport can modify http.DefaultTransport.
func freshTransport() http.RoundTripper {
	return cleanTransport.Clone()
}

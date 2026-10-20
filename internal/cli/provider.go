package cli

import (
	"fmt"
	"strings"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/output"
)

// newProviderCommand builds the top-level `provider` command: workspace-wide
// provider CRUD. Distinct from `sandbox provider` (sandbox.go), which manages
// providers attached to one sandbox.
func newProviderCommand() *cobra.Command {
	p := &cobra.Command{
		Use:   "provider",
		Short: "Manage provider configuration",
	}
	p.AddCommand(
		newProviderCreateCommand(),
		newProviderGetCommand(),
		newProviderListCommand(),
		newProviderUpdateCommand(),
		newProviderDeleteCommand(),
		newProviderNotImplementedCommand("list-profiles"),
		newProviderNotImplementedCommand("profile"),
		newProviderNotImplementedCommand("refresh"),
	)
	return p
}

func newProviderGetCommand() *cobra.Command {
	var format string
	c := &cobra.Command{
		Use:   "get NAME",
		Short: "Fetch a provider by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withGateway(cmd, func(gw gateway.Gateway) error {
				p, err := gw.GetProvider(cmd.Context(), workspace(), args[0])
				if err != nil {
					return err
				}
				return output.RenderProvider(cmd.OutOrStdout(), p, output.Format(format))
			})
		},
	}
	c.Flags().StringVarP(&format, "output", "o", "table", "output format: table|json|yaml")
	return c
}

func newProviderListCommand() *cobra.Command {
	var (
		format        string
		limit         int
		names         bool
		allWorkspaces bool
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "List providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withGateway(cmd, func(gw gateway.Gateway) error {
				opts := types.ListOptions{PageSize: limit, AllWorkspaces: allWorkspaces}
				providers, err := gw.ListProviders(cmd.Context(), workspace(), opts)
				if err != nil {
					return err
				}
				if names {
					return output.RenderProviderNames(cmd.OutOrStdout(), providers)
				}
				return output.RenderProviders(cmd.OutOrStdout(), providers, output.Format(format))
			})
		},
	}
	f := c.Flags()
	f.StringVarP(&format, "output", "o", "table", "output format: table|json|yaml")
	f.IntVar(&limit, "limit", 100, "page size for provider listing")
	f.BoolVar(&names, "names", false, "print only provider names, one per line")
	f.BoolVar(&allWorkspaces, "all-workspaces", false, "list providers across all workspaces")
	return c
}

func newProviderDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete NAME...",
		Short: "Delete providers by name",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withGateway(cmd, func(gw gateway.Gateway) error {
				ws := workspace()
				for _, name := range args {
					if err := gw.DeleteProvider(cmd.Context(), ws, name); err != nil {
						return fmt.Errorf("delete provider %q: %w", name, err)
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✓ Deleted provider %s\n", name)
				}
				return nil
			})
		},
	}
}

// newProviderNotImplementedCommand builds a stub for an upstream `provider`
// subtree openshellctl doesn't implement natively yet (list-profiles,
// profile, refresh). It accepts any args so a fully-formed upstream
// invocation (e.g. `provider profile export foo`) reaches the message
// instead of failing on cobra's own "unknown command" first.
func newProviderNotImplementedCommand(name string) *cobra.Command {
	return &cobra.Command{
		Use:                name,
		Short:              "Not yet implemented in openshellctl",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			upstream := strings.TrimSpace("provider " + name + " " + strings.Join(args, " "))
			return fmt.Errorf("provider %s is not yet implemented in openshellctl; run `openshell %s` directly", name, upstream)
		},
	}
}

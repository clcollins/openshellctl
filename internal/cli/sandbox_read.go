package cli

import (
	"fmt"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/output"
)

func newSandboxGetCommand() *cobra.Command {
	var (
		format string
		file   string
	)
	c := &cobra.Command{
		Use:   "get [NAME]",
		Short: "Get a sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" && len(args) == 0 {
				return &UsageError{Err: fmt.Errorf("a sandbox name is required")}
			}
			if err := checkFileArgConflict(file, args); err != nil {
				return err
			}
			return withGatewayTarget(cmd, func(gw gateway.Gateway, target *gatewayconfig.Target) error {
				ws := workspace()
				sbName, err := resolveNameFromFileOrArgs(cmd, file, args, target, ws)
				if err != nil {
					return err
				}
				sb, err := gw.GetSandbox(cmd.Context(), ws, sbName)
				if err != nil {
					return err
				}
				return renderSandbox(cmd, sb, format)
			})
		},
	}
	c.Flags().StringVarP(&format, "output", "o", "table", "output format: table|json|yaml")
	c.Flags().StringVarP(&file, "file", "f", "", "manifest file to read sandbox name from (- for stdin)")
	return c
}

func newSandboxListCommand() *cobra.Command {
	var (
		format        string
		limit         int
		ids           bool
		names         bool
		selector      string
		allWorkspaces bool
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ids && names {
				return &UsageError{Err: fmt.Errorf("--ids and --names are mutually exclusive")}
			}
			return withGateway(cmd, func(gw gateway.Gateway) error {
				opts := types.ListOptions{
					PageSize:      limit,
					LabelSelector: selector,
					AllWorkspaces: allWorkspaces,
				}
				sandboxes, err := gw.ListSandboxes(cmd.Context(), workspace(), opts)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				switch {
				case ids:
					return output.RenderSandboxIDs(out, sandboxes)
				case names:
					return output.RenderSandboxNames(out, sandboxes, allWorkspaces)
				case format == "json":
					return renderSandboxSliceJSON(cmd, sandboxes)
				case format == "yaml":
					return renderSandboxSliceYAML(cmd, sandboxes)
				default:
					return output.RenderSandboxList(out, sandboxes, output.ListOptions{AllWorkspaces: allWorkspaces})
				}
			})
		},
	}
	f := c.Flags()
	f.StringVarP(&format, "output", "o", "table", "output format: table|json|yaml")
	f.IntVar(&limit, "limit", 100, "page size for sandbox listing")
	f.BoolVar(&ids, "ids", false, "print ids only")
	f.BoolVar(&names, "names", false, "print names only")
	f.StringVar(&selector, "selector", "", "label selector")
	f.BoolVar(&allWorkspaces, "all-workspaces", false, "list across all workspaces")
	return c
}

// renderSandbox renders one sandbox in the requested format.
func renderSandbox(cmd *cobra.Command, sb *types.Sandbox, format string) error {
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		return output.RenderSandboxJSON(out, sb)
	case "yaml":
		return output.RenderSandboxYAML(out, sb)
	default:
		return output.RenderSandboxGetTable(out, sb)
	}
}

func renderSandboxSliceJSON(cmd *cobra.Command, sandboxes []*types.Sandbox) error {
	out := cmd.OutOrStdout()
	for _, s := range sandboxes {
		if err := output.RenderSandboxJSON(out, s); err != nil {
			return err
		}
	}
	return nil
}

func renderSandboxSliceYAML(cmd *cobra.Command, sandboxes []*types.Sandbox) error {
	out := cmd.OutOrStdout()
	for _, s := range sandboxes {
		if err := output.RenderSandboxYAML(out, s); err != nil {
			return err
		}
	}
	return nil
}

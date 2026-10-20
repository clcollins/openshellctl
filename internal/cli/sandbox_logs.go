package cli

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
)

func newLogsCommand() *cobra.Command {
	var (
		lines   uint32
		tail    bool
		since   string
		sources []string
		level   string
		file    string
	)
	c := &cobra.Command{
		Use:     "logs [NAME]",
		Aliases: []string{"lg"},
		Short:   "View sandbox logs",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// --since parsing (verbatim errors) happens before dialing.
			var sinceMs int64
			hasSince := cmd.Flags().Changed("since") && since != ""
			if hasSince {
				durMs, err := sandbox.ParseDurationToMs(since)
				if err != nil {
					return &UsageError{Err: err}
				}
				sinceMs = sandbox.ComputeSinceMs(time.Now().UnixMilli(), durMs, true)
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

				req := &sandbox.LogsRequest{
					Workspace: ws,
					Name:      sbName,
					Lines:     lines,
					Tail:      tail,
					SinceMs:   sinceMs,
					Sources:   sandbox.FilterSources(sources),
					Level:     strings.ToUpper(level),
				}

				return sandbox.Logs(cmd.Context(), sandbox.LogsDeps{
					GW:     gw,
					Stdout: cmd.OutOrStdout(),
					Stderr: cmd.ErrOrStderr(),
				}, req)
			})
		},
	}
	f := c.Flags()
	f.Uint32VarP(&lines, "n", "n", 200, "number of log lines to return")
	f.BoolVar(&tail, "tail", false, "stream live logs")
	f.StringVar(&since, "since", "", "only show logs from this duration ago (e.g. 5m, 1h, 30s)")
	f.StringSliceVar(&sources, "source", []string{"all"}, "filter by source: gateway, sandbox, or all")
	f.StringVar(&level, "level", "", "minimum log level: error, warn, info, debug, trace")
	f.StringVarP(&file, "file", "f", "", "manifest file to read sandbox name from (- for stdin)")
	return c
}

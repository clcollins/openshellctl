package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/sandbox"
)

func newSandboxExecCommand() *cobra.Command {
	var (
		name    string
		file    string
		workdir string
		timeout uint32
		envs    []string
	)
	ttyState := &ttyTriState{}
	c := &cobra.Command{
		Use:                "exec [--name NAME] -- COMMAND...",
		Short:              "Run a command in a sandbox",
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			command := commandArgs(cmd, args)
			if len(command) == 0 {
				return &UsageError{Err: fmt.Errorf("a command is required (use -- COMMAND...)")}
			}
			envMap, err := sandbox.ParseEnvPairs(envs)
			if err != nil {
				return &UsageError{Err: err}
			}

			// --tty / --no-tty use clap `overrides_with` semantics: last one wins
			// (not mutually exclusive). The shared TTYTriState value records order.
			ttyOverride := ttyState.Value()

			if err := checkFileArgConflict(file, argsFromName(name)); err != nil {
				return err
			}
			return withGatewayTarget(cmd, func(gw gateway.Gateway, target *gatewayconfig.Target) error {
				ws := workspace()
				sbName, err := resolveNameFromFileOrArgs(cmd, file, argsFromName(name), target, ws)
				if err != nil {
					return err
				}

				stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
				stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
				useTTY := sandbox.ResolveExecTTY(ttyOverride, stdinTTY, stdoutTTY)

				// last_sandbox is saved regardless of the exec outcome (A.8).
				defer saveLastSandbox(target, ws, sbName)

				req := &sandbox.ExecRequest{
					Workspace: ws,
					Name:      sbName,
					Command:   command,
					Workdir:   workdir,
					Timeout:   timeout,
					Env:       envMap,
					TTY:       useTTY,
				}

				// Interactive path only when --tty forced AND stdin is a real terminal.
				if ttyOverride != nil && *ttyOverride && stdinTTY {
					return runExecInteractive(cmd, gw, req)
				}
				return runExecStreaming(cmd, gw, req, stdinTTY)
			})
		},
	}
	f := c.Flags()
	f.StringVarP(&name, "name", "n", "", "sandbox name (defaults to last-used)")
	f.StringVar(&workdir, "workdir", "", "working directory inside the sandbox")
	f.Uint32Var(&timeout, "timeout", 0, "timeout in seconds (0 = none)")
	// --tty and --no-tty are two spellings of the same boolean flag with
	// last-wins (clap overrides_with) semantics, implemented by a shared
	// TTYTriState value.
	f.Var(ttyStateFlag{ttyState, true}, "tty", "force a pseudo-terminal")
	f.Var(ttyStateFlag{ttyState, false}, "no-tty", "disable pseudo-terminal allocation")
	f.Lookup("tty").NoOptDefVal = "true"
	f.Lookup("no-tty").NoOptDefVal = "true"
	f.StringSliceVar(&envs, "env", nil, "env KEY=VALUE (repeatable)")
	f.StringVarP(&file, "file", "f", "", "manifest file to read sandbox name from (- for stdin)")
	return c
}

// runExecStreaming runs the one-shot exec, reading stdin (capped) when not a TTY.
func runExecStreaming(cmd *cobra.Command, gw gateway.Gateway, req *sandbox.ExecRequest, stdinTTY bool) error {
	if !stdinTTY {
		data, err := readStdinCapped(cmd.InOrStdin())
		if err != nil {
			return err
		}
		req.Stdin = data
	}
	code, err := sandbox.Exec(cmd.Context(), sandbox.ExecDeps{
		GW:     gw,
		Stdout: cmd.OutOrStdout(),
		Stderr: cmd.ErrOrStderr(),
	}, req)
	if err != nil {
		return err
	}
	return exitCodeError(code)
}

// runExecInteractive runs the bidi interactive exec with the local terminal in
// raw mode and SIGWINCH forwarding.
func runExecInteractive(cmd *cobra.Command, gw gateway.Gateway, req *sandbox.ExecRequest) error {
	sb, err := gw.GetSandbox(cmd.Context(), req.Workspace, req.Name)
	if err != nil {
		return err
	}
	if sb == nil {
		return fmt.Errorf("sandbox not found")
	}
	if !isReady(sb) {
		return sandbox.ErrSandboxNotReady(req.Name, sb.Status.Phase)
	}

	cols, rows := terminalSize(int(os.Stdout.Fd()))
	req.Cols, req.Rows = cols, rows

	restore, resizes, stop := enterRawWithResize(int(os.Stdin.Fd()), int(os.Stdout.Fd()))
	defer restore()
	defer stop()

	code, err := sandbox.ExecInteractive(cmd.Context(), sandbox.ExecInteractiveDeps{
		GW:      gw,
		Stdin:   os.Stdin,
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
		Resizes: resizes,
	}, req)
	if err != nil {
		return err
	}
	return exitCodeError(code)
}

// readStdinCapped reads stdin up to MaxStdinPayload+1 bytes and errors if the cap
// is exceeded (A.8).
func readStdinCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, sandbox.MaxStdinPayload+1))
	if err != nil {
		return nil, err
	}
	if err := sandbox.CheckStdinSize(len(data)); err != nil {
		return nil, &UsageError{Err: err}
	}
	return data, nil
}

// argsFromName turns a --name flag value into the positional-arg slice
// resolveSandboxName expects (empty → no positional).
func argsFromName(name string) []string {
	if name == "" {
		return nil
	}
	return []string{name}
}

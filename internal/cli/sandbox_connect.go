package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/openshift-online/openshellctl/pkg/gateway"
	"github.com/openshift-online/openshellctl/pkg/gatewayconfig"
	"github.com/openshift-online/openshellctl/pkg/transfer"
)

func newSandboxConnectCommand() *cobra.Command {
	var (
		editor string
		file   string
	)
	c := &cobra.Command{
		Use:   "connect [NAME]",
		Short: "Connect to a sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if editor != "" {
				return &UsageError{Err: fmt.Errorf("--editor is not supported by openshellctl")}
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
				defer saveLastSandbox(target, ws, sbName)

				stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
				stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
				useTTY := stdinTTY && stdoutTTY

				tc := transfer.New(gw, nil, wallClock{})
				t := &cliTerminal{
					stdinFd:  int(os.Stdin.Fd()),
					stdoutFd: int(os.Stdout.Fd()),
					stdin:    os.Stdin,
					stdout:   cmd.OutOrStdout(),
					stderr:   cmd.ErrOrStderr(),
					isTTY:    stdinTTY,
				}
				code, err := tc.ConnectMainProcess(cmd.Context(), ws, sbName, useTTY, t)
				if err != nil {
					return err
				}
				return exitCodeError(code)
			})
		},
	}
	c.Flags().StringVar(&editor, "editor", "", "(unsupported)")
	c.Flags().StringVarP(&file, "file", "f", "", "manifest file to read sandbox name from (- for stdin)")
	return c
}

// cliTerminal adapts the local terminal for transfer.Connect.
type cliTerminal struct {
	stdinFd, stdoutFd int
	stdin             io.Reader
	stdout, stderr    io.Writer
	isTTY             bool
}

func (t *cliTerminal) MakeRaw() (func(), bool) {
	if !t.isTTY {
		return func() {}, false
	}
	state, err := term.MakeRaw(t.stdinFd)
	if err != nil {
		return func() {}, false
	}
	return func() { _ = term.Restore(t.stdinFd, state) }, true
}

func (t *cliTerminal) Size() (int, int) {
	w, h, err := term.GetSize(t.stdoutFd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

func (t *cliTerminal) Stdin() io.Reader  { return t.stdin }
func (t *cliTerminal) Stdout() io.Writer { return t.stdout }
func (t *cliTerminal) Stderr() io.Writer { return t.stderr }

func (t *cliTerminal) Resizes() (<-chan struct{}, func()) {
	if !t.isTTY {
		return nil, func() {}
	}
	ch, stop := sigwinchChannel(t.stdoutFd)
	return ch, stop
}

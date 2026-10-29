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
	restore := func() { _ = term.Restore(t.stdinFd, state) }
	return func() { restoreWith(restore, t.stdout, t.isTTY) }, true
}

// terminalResetSequence returns ANSI/VT100 escape sequences that restore a
// VTE/xterm-family terminal to a clean state after an interactive attach ends.
// Deliberately sent even after a clean exit (every sequence is idempotent) to
// undo any modes the remote program enabled: mouse reporting, focus events,
// bracketed paste, alternate screen, and hidden cursor.
// This is an intentional difference from the Rust CLI and ssh(1), which do not
// send a reset; see docs/deliberate-differences.md.
func terminalResetSequence() []byte {
	return []byte(
		"\033[?1000l" + // disable X10 mouse button events
			"\033[?1002l" + // disable button-event mouse tracking
			"\033[?1003l" + // disable any-event mouse tracking
			"\033[?1006l" + // disable SGR extended mouse encoding
			"\033[?1004l" + // disable focus-in/focus-out reporting
			"\033[?2004l" + // disable bracketed-paste mode
			"\033[?1049h\033[?1049l" + // enter-then-exit alternate screen: if no
			//   alternate screen was active, ?1049h saves the current cursor and
			//   ?1049l restores it (net no-op, no viewport jump); if a remote
			//   program left the alternate screen active, ?1049l exits it.
			"\033[?25h", //   show cursor
	)
}

// restoreWith calls restoreTTY and, when isTTY is true, writes the terminal
// reset sequence to out. Extracted from cliTerminal.MakeRaw so it can be
// tested without a real TTY file descriptor.
func restoreWith(restoreTTY func(), out io.Writer, isTTY bool) {
	restoreTTY()
	if isTTY {
		_, _ = out.Write(terminalResetSequence())
	}
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

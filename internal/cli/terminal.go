package cli

import (
	"os"
	"os/signal"
	"syscall"

	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	"golang.org/x/term"
)

// isReady reports whether a sandbox is in the Ready phase.
func isReady(sb *types.Sandbox) bool {
	return sb.Status.Phase == types.SandboxReady
}

// isTerminal reports whether f is connected to a terminal.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// terminalSize returns the terminal (cols, rows) for fd, defaulting to 80x24
// when it cannot be determined.
func terminalSize(fd int) (cols, rows uint32) {
	w, h, err := term.GetSize(fd)
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return uint32(w), uint32(h)
}

// enterRawWithResize puts stdinFd into raw mode and starts a SIGWINCH watcher
// that emits {cols, rows} on the returned channel. It returns a restore func
// (undoes raw mode), the resize channel, and a stop func (tears down the watcher
// and closes the channel). When stdinFd is not a terminal, raw mode is skipped
// and a nil resize channel is returned.
func enterRawWithResize(stdinFd, stdoutFd int) (restore func(), resizes <-chan [2]uint32, stop func()) {
	restore = func() {}
	if term.IsTerminal(stdinFd) {
		if state, err := term.MakeRaw(stdinFd); err == nil {
			restore = func() { _ = term.Restore(stdinFd, state) }
		}
	}

	ch := make(chan [2]uint32, 1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigCh:
				cols, rows := terminalSize(stdoutFd)
				select {
				case ch <- [2]uint32{cols, rows}:
				default:
				}
			case <-done:
				return
			}
		}
	}()
	stop = func() {
		signal.Stop(sigCh)
		close(done)
		close(ch)
	}
	return restore, ch, stop
}

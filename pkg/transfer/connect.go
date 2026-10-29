package transfer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrNoExitStatus is the sentinel matched by errors.Is for AttachClosedError.
// Per the supervisor contract (ssh.rs:1391-1427), every supervisor-initiated
// channel close carries an exit-status request; absence therefore means the
// transport underneath the SSH channel was lost while the main process may
// still be running. Gateway-side ephemeral cleanup does not fire in this case.
var ErrNoExitStatus = errors.New("ssh channel closed without exit-status")

// AttachClosedError is returned by ConnectMainProcess when the SSH channel
// closed without delivering an exit-status request. Tunnel holds the first
// error the gateway tunnel reported (if any), enabling callers to distinguish
// a killed process from a broken network path.
type AttachClosedError struct {
	Tunnel error
}

func (e *AttachClosedError) Error() string {
	if e.Tunnel != nil {
		return "ssh channel closed without exit-status; tunnel: " + e.Tunnel.Error()
	}
	return "ssh channel closed without exit-status"
}

// Is makes errors.Is(e, ErrNoExitStatus) return true so existing callers that
// check for ErrNoExitStatus continue to work without changes.
func (e *AttachClosedError) Is(target error) bool { return target == ErrNoExitStatus }

// Unwrap exposes the tunnel error for errors.Is/errors.As traversal.
func (e *AttachClosedError) Unwrap() error { return e.Tunnel }

// detach chord bytes: Ctrl-P (0x10) then Ctrl-Q (0x11).
const (
	ctrlP = 0x10
	ctrlQ = 0x11
)

// stdinChunkSize bounds one local read from stdin. The SSH layer turns every
// Write into one CHANNEL_DATA packet, so this controls the maximum packet
// size and the gateway gRPC frame size. 32 KiB matches OpenSSH's send_data
// buffer and is cited in the README and in TestPumpDetach_CoalescesWrites.
const stdinChunkSize = 32 * 1024

// SSH keepalive constants matching the Rust CLI's ServerAliveInterval /
// ServerAliveCountMax (emitted in ssh-config).
const (
	keepaliveInterval = 15 // seconds between keepalive@openssh.com requests
	keepaliveMaxCount = 3  // consecutive failures before closing the connection
)

// Terminal abstracts the local terminal for an interactive connect: putting the
// terminal into raw mode, reporting its size, and delivering resize events. A
// non-interactive caller passes NopTerminal.
type Terminal interface {
	// MakeRaw switches the terminal to raw mode, returning a restore func. When
	// the local endpoint is not a terminal it returns a no-op restore and false.
	MakeRaw() (restore func(), ok bool)
	// Size returns the current terminal size (cols, rows). Defaults 80x24.
	Size() (cols, rows int)
	// Stdin/Stdout/Stderr are the local streams to pump.
	Stdin() io.Reader
	Stdout() io.Writer
	Stderr() io.Writer
	// Resizes returns a channel that fires (best-effort) on terminal resize; nil
	// when unsupported. The returned stop func releases any resources.
	Resizes() (ch <-chan struct{}, stop func())
}

// ptyRequestMsg is the RFC 4254 §6.2 pty-req payload.
type ptyRequestMsg struct {
	Term     string
	Columns  uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

type (
	setenvRequest struct{ Name, Value string }
	winchMsg      struct{ Columns, Rows, Width, Height uint32 }
	execMsg       struct{ Command string }
)

type subsystemMsg struct{ Subsystem string }

// sendSubsystemRequest sends an SSH subsystem request on the channel.
func sendSubsystemRequest(ch ssh.Channel, subsystem string) error {
	ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(&subsystemMsg{Subsystem: subsystem}))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("server rejected subsystem %q request", subsystem)
	}
	return nil
}

// sendSessionRequest sends either an exec request (when command is non-empty)
// or a shell request (when command is empty) on the SSH channel. Exec sends the
// shell-escaped command string matching the Rust CLI's `ssh sandbox "cmd"`.
func sendSessionRequest(ch ssh.Channel, command []string) error {
	if len(command) > 0 {
		parts := make([]string, len(command))
		for i, arg := range command {
			parts[i] = ShellEscape(arg)
		}
		cmdStr := strings.Join(parts, " ")
		ok, err := ch.SendRequest("exec", true, ssh.Marshal(&execMsg{Command: cmdStr}))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("server rejected exec request")
		}
		return nil
	}
	ok, err := ch.SendRequest("shell", true, nil)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("server rejected shell request")
	}
	return nil
}

// Connect attaches an interactive session to the sandbox and returns the remote
// exit code. When command is non-empty, an SSH exec request runs the
// shell-escaped command (matching the Rust CLI's `ssh -tt sandbox "cmd"`);
// when empty, a shell request gives the default interactive session. The
// server's shell handler connects to the sandbox's main process; the exec
// handler runs the specified command. We drive a raw session channel (rather
// than ssh.Session) so the exit status is directly observable.
func (c *client) Connect(ctx context.Context, workspace, sandbox string, tty bool, term Terminal, command ...string) (int, error) {
	cli, _, err := c.dialSSH(ctx, workspace, sandbox)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cli.Close() }()

	ch, reqs, err := cli.OpenChannel("session", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = ch.Close() }()

	// Collect the exit status from the channel's request stream. reqsDone closes
	// when the request stream ends (channel torn down), guaranteeing any
	// exit-status request has already been observed.
	exitCode := 0
	exitSet := false
	reqsDone := make(chan struct{})
	go func() {
		defer close(reqsDone)
		for req := range reqs {
			if req.Type == "exit-status" && len(req.Payload) >= 4 {
				exitCode = int(binary.BigEndian.Uint32(req.Payload))
				exitSet = true
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()

	// Start SSH keepalive loop matching the Rust CLI's ServerAliveInterval 15 /
	// ServerAliveCountMax 3. Sends keepalive@openssh.com global requests on the
	// underlying SSH connection. On 3 consecutive failures the connection is
	// closed, causing the I/O pumps to unblock.
	keepaliveDead := make(chan struct{})
	if c.clock != nil {
		tickCh, tickStop := c.clock.NewTicker(time.Duration(keepaliveInterval) * time.Second)
		go func() {
			defer tickStop()
			failures := 0
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-tickCh:
					if !ok {
						return
					}
					_, _, err := cli.SendRequest("keepalive@openssh.com", true, nil)
					if err != nil {
						failures++
						if failures >= keepaliveMaxCount {
							close(keepaliveDead)
							_ = cli.Close()
							return
						}
					} else {
						failures = 0
					}
				}
			}
		}()
	}

	if tty {
		cols, rows := termSize(term)
		payload := ssh.Marshal(&ptyRequestMsg{Term: "xterm-256color", Columns: uint32(cols), Rows: uint32(rows)}) //nolint:gosec // terminal dimensions are small non-negative values
		if _, err := ch.SendRequest("pty-req", true, payload); err != nil {
			return 0, err
		}
		if restore, ok := term.MakeRaw(); ok {
			defer restore()
		}
		if rc, stop := term.Resizes(); rc != nil {
			defer stop()
			go func() {
				for range rc {
					cols, rows := termSize(term)
					_, _ = ch.SendRequest("window-change", false, ssh.Marshal(&winchMsg{Columns: uint32(cols), Rows: uint32(rows)})) //nolint:gosec // small non-negative dimensions
				}
			}()
		}
	}

	// Mirror `-o SetEnv=TERM=xterm-256color`.
	_, _ = ch.SendRequest("env", false, ssh.Marshal(&setenvRequest{Name: "TERM", Value: "xterm-256color"}))

	if err := sendSessionRequest(ch, command); err != nil {
		return 0, err
	}

	// Pump remote stdout/stderr → local.
	outDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(term.Stdout(), ch)
		close(outDone)
	}()
	go func() { _, _ = io.Copy(term.Stderr(), ch.Stderr()) }()

	// Pump local stdin → remote, watching for the detach chord. On detach we tear
	// the whole channel down (mirroring the CLI's Ctrl-P Ctrl-Q disconnect) so the
	// session ends immediately and its exit status is abandoned; otherwise we just
	// signal EOF to the remote.
	detachCh := make(chan struct{}, 1)
	go func() {
		if pumpDetach(term.Stdin(), ch) {
			detachCh <- struct{}{}
			_ = ch.Close()
			return
		}
		_ = ch.CloseWrite()
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-keepaliveDead:
		return 0, fmt.Errorf("ssh keepalive timeout: %d consecutive failures", keepaliveMaxCount)
	case <-detachCh:
		return 0, nil
	case <-outDone:
		// The remote closed its stdout. The exit-status request is delivered before
		// the channel is torn down, so wait for the request stream to drain (it ends
		// when the peer closes the channel). The deferred ch.Close handles cleanup.
		<-reqsDone
		if exitSet {
			return exitCode, nil
		}
		return 0, nil
	}
}

func termSize(term Terminal) (cols, rows int) {
	cols, rows = term.Size()
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return cols, rows
}

// ConnectMainProcess attaches to the sandbox's main process via the SSH subsystem
// "openshell-main". This is the correct attach path for 0.1.x gateways: using a
// shell request starts a new login shell, and using exec re-runs the command
// (double-executing spec.command). The subsystem request targets the process the
// gateway already launched at boot from OPENSHELL_MAIN_PROCESS_SPEC.
func (c *client) ConnectMainProcess(ctx context.Context, workspace, sandbox string, tty bool, term Terminal) (int, error) {
	cli, tc, err := c.dialSSH(ctx, workspace, sandbox)
	if err != nil {
		return 0, err
	}
	defer func() { _ = cli.Close() }()

	ch, reqs, err := cli.OpenChannel("session", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = ch.Close() }()

	exitCode := 0
	exitSet := false
	reqsDone := make(chan struct{})
	go func() {
		defer close(reqsDone)
		for req := range reqs {
			if req.Type == "exit-status" && len(req.Payload) >= 4 {
				exitCode = int(binary.BigEndian.Uint32(req.Payload))
				exitSet = true
			}
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}()

	keepaliveDead := make(chan struct{})
	if c.clock != nil {
		tickCh, tickStop := c.clock.NewTicker(time.Duration(keepaliveInterval) * time.Second)
		go func() {
			defer tickStop()
			failures := 0
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-tickCh:
					if !ok {
						return
					}
					_, _, err := cli.SendRequest("keepalive@openssh.com", true, nil)
					if err != nil {
						failures++
						if failures >= keepaliveMaxCount {
							close(keepaliveDead)
							_ = cli.Close()
							return
						}
					} else {
						failures = 0
					}
				}
			}
		}()
	}

	if tty {
		cols, rows := termSize(term)
		payload := ssh.Marshal(&ptyRequestMsg{Term: "xterm-256color", Columns: uint32(cols), Rows: uint32(rows)}) //nolint:gosec // terminal dimensions are small non-negative values
		if _, err := ch.SendRequest("pty-req", true, payload); err != nil {
			return 0, err
		}
		if restore, ok := term.MakeRaw(); ok {
			defer restore()
		}
		if rc, stop := term.Resizes(); rc != nil {
			defer stop()
			go func() {
				for range rc {
					cols, rows := termSize(term)
					_, _ = ch.SendRequest("window-change", false, ssh.Marshal(&winchMsg{Columns: uint32(cols), Rows: uint32(rows)})) //nolint:gosec // small non-negative dimensions
				}
			}()
		}
	}

	_, _ = ch.SendRequest("env", false, ssh.Marshal(&setenvRequest{Name: "TERM", Value: "xterm-256color"}))

	if err := sendSubsystemRequest(ch, "openshell-main"); err != nil {
		return 0, err
	}

	outDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(term.Stdout(), ch)
		close(outDone)
	}()
	go func() { _, _ = io.Copy(term.Stderr(), ch.Stderr()) }()

	detachCh := make(chan struct{}, 1)
	go func() {
		if pumpDetach(term.Stdin(), ch) {
			detachCh <- struct{}{}
			_ = ch.Close()
			return
		}
		_ = ch.CloseWrite()
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-keepaliveDead:
		return 0, fmt.Errorf("ssh keepalive timeout: %d consecutive failures", keepaliveMaxCount)
	case <-detachCh:
		return 0, nil
	case <-outDone:
		<-reqsDone
		return outDoneResult(exitSet, exitCode, keepaliveDead, tc.firstErr())
	}
}

// outDoneResult decides what to return when the main process output stream ends.
// exitSet is checked first: a delivered exit-status is authoritative regardless
// of keepalive state (the keepalive goroutine may accumulate failures while the
// process is exiting normally, so checking keepaliveDead first would mask the
// real exit code). When no exit-status was delivered, keepaliveDead is checked
// next: the keepalive goroutine closes it before calling cli.Close() which
// causes outDone, making the keepalive timeout the more specific diagnosis.
func outDoneResult(exitSet bool, exitCode int, keepaliveDead <-chan struct{}, tunnelErr error) (int, error) {
	if exitSet {
		return exitCode, nil
	}
	select {
	case <-keepaliveDead:
		return 0, fmt.Errorf("ssh keepalive timeout: %d consecutive failures", keepaliveMaxCount)
	default:
	}
	return 0, &AttachClosedError{Tunnel: tunnelErr}
}

// filterDetach processes one chunk of input data against the Ctrl-P Ctrl-Q
// detach chord state machine. pending tracks whether a Ctrl-P was seen at the
// end of the previous chunk. Returns the bytes to forward and detach=true when
// the full chord is detected. The Ctrl-P byte is never forwarded when followed
// by Ctrl-Q; consecutive Ctrl-P causes one to be forwarded.
func filterDetach(data []byte, pending *bool) (forward []byte, detach bool) {
	out := make([]byte, 0, len(data))
	for _, b := range data {
		switch {
		case *pending && b == ctrlQ:
			*pending = false
			return out, true
		case *pending && b == ctrlP:
			out = append(out, ctrlP)
			// *pending stays true
		case *pending:
			out = append(out, ctrlP, b)
			*pending = false
		case b == ctrlP:
			*pending = true
		default:
			out = append(out, b)
		}
	}
	return out, false
}

// pumpDetach copies src→dst, returning true if the Ctrl-P Ctrl-Q chord was seen
// (the chord bytes are not forwarded). Each Read produces at most one Write: the
// SSH layer turns every Write into one CHANNEL_DATA packet, which the gateway
// relay turns into one gRPC frame. Coalescing to 32 KiB per Read matches the
// behaviour of OpenSSH's ssh(1) and avoids queue pressure in the supervisor's
// 64-slot mpsc input channel on large pastes.
func pumpDetach(src io.Reader, dst io.Writer) bool {
	buf := make([]byte, stdinChunkSize)
	var pending bool
	for {
		n, err := src.Read(buf)
		if n > 0 {
			fwd, detach := filterDetach(buf[:n], &pending)
			if len(fwd) > 0 {
				if _, werr := dst.Write(fwd); werr != nil {
					return false
				}
			}
			if detach {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

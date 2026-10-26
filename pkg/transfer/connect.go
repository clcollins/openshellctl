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

// detach chord bytes: Ctrl-P (0x10) then Ctrl-Q (0x11).
const (
	ctrlP = 0x10
	ctrlQ = 0x11
)

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
	cli, err := c.dialSSH(ctx, workspace, sandbox)
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
	cli, err := c.dialSSH(ctx, workspace, sandbox)
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
		if exitSet {
			return exitCode, nil
		}
		return 0, nil
	}
}

// pumpDetach copies src→dst, returning true if the Ctrl-P Ctrl-Q chord was seen
// (the chord bytes are not forwarded). Byte-at-a-time is acceptable for
// interactive input volumes.
func pumpDetach(src io.Reader, dst io.Writer) bool {
	buf := make([]byte, 1)
	sawCtrlP := false
	for {
		n, err := src.Read(buf)
		if n > 0 {
			b := buf[0]
			switch {
			case sawCtrlP && b == ctrlQ:
				return true
			case sawCtrlP && b == ctrlP:
				// consecutive Ctrl-P: forward one, keep armed.
				if _, werr := dst.Write([]byte{ctrlP}); werr != nil {
					return false
				}
			case sawCtrlP:
				// Ctrl-P not followed by Ctrl-Q: forward the held Ctrl-P then this byte.
				if _, werr := dst.Write([]byte{ctrlP, b}); werr != nil {
					return false
				}
				sawCtrlP = false
			case b == ctrlP:
				sawCtrlP = true
			default:
				if _, werr := dst.Write([]byte{b}); werr != nil {
					return false
				}
			}
		}
		if err != nil {
			return false
		}
	}
}

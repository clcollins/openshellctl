package transfer

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshDialTimeout matches the CLI's ssh Timeout (ssh.rs client config).
const sshDialTimeout = 30 * time.Second

// dialSSH opens the sandbox SSH tunnel and completes the SSH handshake over it.
// It returns the SSH client, the underlying tunnelConn (whose firstErr method
// yields the first transport error seen on the wire), and any error.
//
// No auth methods are configured: crypto/ssh attempts the "none" method first,
// which the sandbox server accepts unconditionally
// (crates/openshell-supervisor-process/src/ssh.rs:506-508).
func (c *client) dialSSH(ctx context.Context, workspace, sandbox string) (*ssh.Client, *tunnelConn, error) {
	rwc, err := c.gw.SSHTunnel(ctx, workspace, sandbox)
	if err != nil {
		return nil, nil, err
	}
	tc := &tunnelConn{ReadWriteCloser: rwc}
	cfg := &ssh.ClientConfig{
		User:            "sandbox",
		Auth:            nil,                         // "none" auth (attempted automatically)
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // ephemeral per-sandbox host key; tunnel is authorised by the gateway session token (matches CLI StrictHostKeyChecking=no)
		Timeout:         sshDialTimeout,
	}
	conn, chans, reqs, err := ssh.NewClientConn(tc, "sandbox", cfg)
	if err != nil {
		_ = rwc.Close()
		return nil, nil, fmt.Errorf("ssh handshake failed: %w", err)
	}
	return ssh.NewClient(conn, chans, reqs), tc, nil
}

// tunnelConn adapts an io.ReadWriteCloser (the gateway SSH tunnel) to net.Conn
// as required by ssh.NewClientConn. It records the first Read or Write error so
// callers can distinguish a clean channel close from a transport-level failure.
// The deadline methods are no-ops (the SSH handshake Timeout is enforced by the
// ClientConfig, and RPC context cancellation tears the tunnel down).
type tunnelConn struct {
	io.ReadWriteCloser
	mu  sync.Mutex // guards err; first non-nil write wins
	err error
}

func (tc *tunnelConn) Read(p []byte) (int, error) {
	n, err := tc.ReadWriteCloser.Read(p)
	if err != nil {
		tc.mu.Lock()
		if tc.err == nil {
			tc.err = err
		}
		tc.mu.Unlock()
	}
	return n, err
}

func (tc *tunnelConn) Write(p []byte) (int, error) {
	n, err := tc.ReadWriteCloser.Write(p)
	if err != nil {
		tc.mu.Lock()
		if tc.err == nil {
			tc.err = err
		}
		tc.mu.Unlock()
	}
	return n, err
}

// firstErr returns the first transport error recorded by Read or Write, or nil.
func (tc *tunnelConn) firstErr() error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.err
}

// rwcConn wraps an io.ReadWriteCloser as a net.Conn; used by tests.
func rwcConn(rwc io.ReadWriteCloser) net.Conn { return &tunnelConn{ReadWriteCloser: rwc} }

func (*tunnelConn) LocalAddr() net.Addr                { return tunnelAddr{} }
func (*tunnelConn) RemoteAddr() net.Addr               { return tunnelAddr{} }
func (*tunnelConn) SetDeadline(_ time.Time) error      { return nil }
func (*tunnelConn) SetReadDeadline(_ time.Time) error  { return nil }
func (*tunnelConn) SetWriteDeadline(_ time.Time) error { return nil }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "openshell-tunnel" }
func (tunnelAddr) String() string  { return "sandbox" }

// remoteResult is the outcome of a remote command run.
type remoteResult struct {
	exitStatus int
}

// runRemoteStdin runs cmd on cli, feeding feed(stdin) into the remote stdin and
// copying remote stdout to out (nil discards). Remote stderr is copied to
// errOut (nil discards). It waits for completion and returns the exit status.
func runRemoteStdin(cli *ssh.Client, cmd string, feed func(io.Writer) error, out, errOut io.Writer) (remoteResult, error) {
	sess, err := cli.NewSession()
	if err != nil {
		return remoteResult{}, err
	}
	defer func() { _ = sess.Close() }()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return remoteResult{}, err
	}
	if out != nil {
		sess.Stdout = out
	}
	if errOut != nil {
		sess.Stderr = errOut
	}
	if err := sess.Start(cmd); err != nil {
		return remoteResult{}, err
	}

	var feedErr error
	if feed != nil {
		feedErr = feed(stdin)
	}
	_ = stdin.Close()

	waitErr := sess.Wait()
	if feedErr != nil {
		return remoteResult{}, feedErr
	}
	return interpretWait(waitErr)
}

// runRemoteCapture runs cmd with no stdin and returns captured stdout bytes.
func runRemoteCapture(cli *ssh.Client, cmd string) ([]byte, remoteResult, error) {
	sess, err := cli.NewSession()
	if err != nil {
		return nil, remoteResult{}, err
	}
	defer func() { _ = sess.Close() }()

	out, err := sess.Output(cmd)
	if err == nil {
		return out, remoteResult{exitStatus: 0}, nil
	}
	res, ierr := interpretWait(err)
	if ierr != nil {
		return nil, remoteResult{}, ierr
	}
	return out, res, nil
}

// interpretWait maps an ssh session Wait error to a remoteResult. A clean exit
// (nil) or an *ssh.ExitError yields a result; anything else is a transport error.
func interpretWait(err error) (remoteResult, error) {
	if err == nil {
		return remoteResult{exitStatus: 0}, nil
	}
	if exitErr, ok := err.(*ssh.ExitError); ok {
		return remoteResult{exitStatus: exitErr.ExitStatus()}, nil
	}
	return remoteResult{}, err
}

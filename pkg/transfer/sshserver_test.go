package transfer

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// fakeGateway is a gateway.Gateway whose SSHTunnel returns a client-side pipe
// connected to an in-process test SSH server rooted at a temp directory. All
// other methods panic (unused by transfer tests).
type fakeGateway struct {
	gateway.Gateway
	root               string // the remote "workspace" root (absolute, on the real FS)
	hostKey            ssh.Signer
	mu                 sync.Mutex
	tunnels            int
	lastMain           string   // last payload written by a Connect session
	mainReply          string   // canned bytes the shell session streams back
	mainExit           int      // exit code the shell session returns
	mainHold           bool     // when true, the session holds its write side open until the client tears the channel down (models a live session that only ends on detach)
	failExtract        bool     // when true, the upload extract command reports non-zero
	lastReqType        string   // tracks the request type used for the interactive session ("shell", "exec", or "subsystem")
	lastExecCmd        string   // last exec command received
	lastSubsystem      string   // last subsystem name requested
	rejectShell        bool     // when true, the server rejects shell requests (models a broken server)
	noExitStatus       bool     // when true, the server closes the channel without sending exit-status
	keepaliveCount     int      // number of keepalive@openssh.com global requests received
	rejectKeepalive    bool     // when true, server replies false to keepalive requests (models unresponsive server)
	tunnelErr          error    // when non-nil, the client-side tunnel's Read returns this instead of any error (including EOF) from the underlying conn
	dropAfterMainReply bool     // when true, close the raw server-side conn after sending mainReply (simulates mid-session transport loss)
	rawServerConn      net.Conn // raw server-side conn; set by SSHTunnel, closed by runMain when dropAfterMainReply
}

func newFakeGateway(t *testing.T, root string) *fakeGateway {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return &fakeGateway{root: root, hostKey: signer}
}

func (f *fakeGateway) SSHTunnel(_ context.Context, _, _ string) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	f.tunnels++
	tunnelErr := f.tunnelErr
	f.mu.Unlock()
	cconn, sconn := bufferedConnPair()
	f.mu.Lock()
	f.rawServerConn = sconn
	f.mu.Unlock()
	go f.serve(sconn)
	if tunnelErr != nil {
		return &subErrConn{ReadWriteCloser: cconn, err: tunnelErr}, nil
	}
	return cconn, nil
}

// subErrConn wraps an io.ReadWriteCloser and substitutes its own err for any
// error (including io.EOF) returned by Read. Used to simulate transport drops.
type subErrConn struct {
	io.ReadWriteCloser
	err error
}

func (s *subErrConn) Read(p []byte) (int, error) {
	n, err := s.ReadWriteCloser.Read(p)
	if err != nil {
		return n, s.err
	}
	return n, nil
}

// serve runs a minimal SSH server on the server side of the pipe.
func (f *fakeGateway) serve(nc net.Conn) {
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(f.hostKey)
	sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go f.handleGlobalRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go f.handleSession(ch, chReqs)
	}
	_ = sconn.Close()
}

func (f *fakeGateway) handleGlobalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type == "keepalive@openssh.com" {
			f.mu.Lock()
			f.keepaliveCount++
			reject := f.rejectKeepalive
			f.mu.Unlock()
			if req.WantReply {
				_ = req.Reply(!reject, nil)
			}
			continue
		}
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
	}
}

func (f *fakeGateway) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req", "env", "window-change":
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "exec":
			cmd := decodeString(req.Payload)
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			f.mu.Lock()
			f.lastReqType = "exec"
			f.lastExecCmd = cmd
			noExit := f.noExitStatus
			f.mu.Unlock()
			if f.isTarCommand(cmd) {
				status := f.runShell(ch, cmd)
				if !noExit {
					sendExit(ch, status)
				}
			} else {
				f.runMain(ch)
				if !noExit {
					sendExit(ch, f.mainExit)
				}
			}
			_ = ch.Close()
			return
		case "shell":
			f.mu.Lock()
			f.lastReqType = "shell"
			reject := f.rejectShell
			noExit := f.noExitStatus
			f.mu.Unlock()
			if req.WantReply {
				_ = req.Reply(!reject, nil)
			}
			if !reject {
				f.runMain(ch)
				if !noExit {
					sendExit(ch, f.mainExit)
				}
			}
			_ = ch.Close()
			return
		case "subsystem":
			name := decodeString(req.Payload)
			f.mu.Lock()
			f.lastReqType = "subsystem"
			f.lastSubsystem = name
			noExit := f.noExitStatus
			f.mu.Unlock()
			accepted := name == "sftp" || name == "openshell-main"
			if req.WantReply {
				_ = req.Reply(accepted, nil)
			}
			if name == "openshell-main" {
				f.runMain(ch)
				if !noExit {
					sendExit(ch, f.mainExit)
				}
			}
			_ = ch.Close()
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// runMain drains stdin (recording it), writes the canned reply, and returns.
// When mainHold is set it does not close its write side, so the session only
// ends when the client tears the channel down (e.g. on detach); ReadAll then
// returns and runMain unblocks.
func (f *fakeGateway) runMain(ch ssh.Channel) {
	done := make(chan struct{})
	go func() {
		b, _ := io.ReadAll(ch)
		f.mu.Lock()
		f.lastMain = string(b)
		f.mu.Unlock()
		close(done)
	}()
	if f.mainReply != "" {
		_, _ = io.WriteString(ch, f.mainReply)
	}
	// Simulate a mid-session transport drop after mainReply is buffered.
	// Closing the raw server-side conn causes the client to read EOF after
	// the buffered data, which subErrConn then converts to the sentinel error.
	if f.dropAfterMainReply {
		f.mu.Lock()
		rc := f.rawServerConn
		f.mu.Unlock()
		if rc != nil {
			_ = rc.Close()
		}
	}
	if !f.mainHold {
		_ = ch.CloseWrite()
	}
	<-done
}

// runShell interprets the specific command shapes transfer emits, implementing
// them in Go against a temp-dir "remote" filesystem under f.root. No host shell
// or tar binary is invoked (fully hermetic); Go's archive/tar provides faithful
// tar semantics.
func (f *fakeGateway) runShell(ch ssh.Channel, cmd string) int {
	switch {
	case strings.HasPrefix(cmd, "mkdir -p ") && strings.Contains(cmd, "tar xf - -C "):
		return f.cmdUploadExtract(ch, cmd)
	case strings.HasPrefix(cmd, "pwd -P && realpath -e -- "):
		return f.cmdResolve(ch, cmd)
	case strings.HasPrefix(cmd, "if [ -d "):
		return f.cmdTypeProbe(ch, cmd)
	case strings.HasPrefix(cmd, "tar cf - -C ") && strings.HasSuffix(cmd, " ."):
		return f.cmdTarDir(ch, cmd)
	case strings.HasPrefix(cmd, "tar cf - -C ") && strings.Contains(cmd, " -- "):
		return f.cmdTarFile(ch, cmd)
	default:
		_, _ = fmt.Fprintf(ch.Stderr(), "unrecognized command: %s\n", cmd)
		return 127
	}
}

// cmdUploadExtract handles "mkdir -p <d> && cat | tar xf - -C <d>": create the
// dest dir (relative to root) and extract the tar arriving on stdin into it.
func (f *fakeGateway) cmdUploadExtract(ch ssh.Channel, cmd string) int {
	// Extract the -C argument (dest dir).
	dest := afterLast(cmd, "tar xf - -C ")
	dest = unescapeShell(strings.TrimSpace(dest))
	if f.failExtract {
		_, _ = io.Copy(io.Discard, ch) // drain the tar stream first
		return 3
	}
	abs := f.resolveRemote(dest)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		_, _ = fmt.Fprintln(ch.Stderr(), err)
		return 1
	}
	if err := extractTar(ch, abs); err != nil {
		_, _ = fmt.Fprintln(ch.Stderr(), err)
		return 2
	}
	return 0
}

// remoteWorkspaceRoot is the sandbox workspace root reported by the fake `pwd`.
// It maps onto f.root on the real filesystem. It must not be "/" (the client
// rejects a container-root workspace).
const remoteWorkspaceRoot = "/ws"

// cmdResolve handles "pwd -P && realpath -e -- <p>".
func (f *fakeGateway) cmdResolve(ch ssh.Channel, cmd string) int {
	arg := unescapeShell(strings.TrimSpace(afterLast(cmd, "realpath -e -- ")))
	abs := f.resolveRemote(arg)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return 1 // realpath -e fails for a missing path
	}
	rel := filepath.ToSlash(mustRel(f.root, resolved))
	remoteResolved := remoteWorkspaceRoot
	if rel != "." {
		remoteResolved = remoteWorkspaceRoot + "/" + rel
	}
	_, _ = fmt.Fprintf(ch, "%s\n%s", remoteWorkspaceRoot, remoteResolved)
	return 0
}

// cmdTypeProbe handles the "[ -d ] / [ -e ]" probe.
func (f *fakeGateway) cmdTypeProbe(ch ssh.Channel, cmd string) int {
	// The path appears twice; take the first bracketed arg.
	start := strings.Index(cmd, "[ -d ") + len("[ -d ")
	end := strings.Index(cmd[start:], " ]")
	arg := unescapeShell(cmd[start : start+end])
	abs := f.resolveRemote(arg)
	info, err := os.Stat(abs)
	switch {
	case err == nil && info.IsDir():
		_, _ = fmt.Fprint(ch, "dir")
	case err == nil:
		_, _ = fmt.Fprint(ch, "file")
	default:
		_, _ = fmt.Fprint(ch, "missing")
	}
	return 0
}

// cmdTarDir handles "tar cf - -C <path> ." — stream a tar of the dir contents.
func (f *fakeGateway) cmdTarDir(ch ssh.Channel, cmd string) int {
	mid := strings.TrimSuffix(strings.TrimPrefix(cmd, "tar cf - -C "), " .")
	dir := f.resolveRemote(unescapeShell(strings.TrimSpace(mid)))
	tw := tar.NewWriter(ch)
	if err := tarDirInto(tw, dir, "."); err != nil {
		return 2
	}
	_ = tw.Close()
	return 0
}

// cmdTarFile handles "tar cf - -C <parent> -- <name>".
func (f *fakeGateway) cmdTarFile(ch ssh.Channel, cmd string) int {
	body := strings.TrimPrefix(cmd, "tar cf - -C ")
	i := strings.Index(body, " -- ")
	parent := f.resolveRemote(unescapeShell(strings.TrimSpace(body[:i])))
	name := unescapeShell(strings.TrimSpace(body[i+len(" -- "):]))
	tw := tar.NewWriter(ch)
	full := filepath.Join(parent, name)
	info, err := os.Lstat(full)
	if err != nil {
		return 2
	}
	hdr, _ := tar.FileInfoHeader(info, "")
	hdr.Name = name
	_ = tw.WriteHeader(hdr)
	if info.Mode().IsRegular() {
		fdata, _ := os.ReadFile(full)
		_, _ = tw.Write(fdata)
	}
	_ = tw.Close()
	return 0
}

func (f *fakeGateway) isTarCommand(cmd string) bool {
	return strings.HasPrefix(cmd, "mkdir -p ") ||
		strings.HasPrefix(cmd, "pwd -P && realpath") ||
		strings.HasPrefix(cmd, "if [ -d ") ||
		strings.HasPrefix(cmd, "tar cf -")
}

func (f *fakeGateway) resolveRemote(p string) string {
	p = strings.TrimPrefix(p, remoteWorkspaceRoot)
	p = strings.TrimPrefix(p, "/")
	return filepath.Join(f.root, filepath.FromSlash(p))
}

func tarDirInto(tw *tar.Writer, dir, prefix string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		full := filepath.Join(dir, e.Name())
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		name := prefix + "/" + e.Name()
		switch {
		case info.IsDir():
			hdr, _ := tar.FileInfoHeader(info, "")
			hdr.Name = name + "/"
			_ = tw.WriteHeader(hdr)
			if err := tarDirInto(tw, full, name); err != nil {
				return err
			}
		default:
			hdr, _ := tar.FileInfoHeader(info, "")
			hdr.Name = name
			_ = tw.WriteHeader(hdr)
			if info.Mode().IsRegular() {
				b, _ := os.ReadFile(full)
				_, _ = tw.Write(b)
			}
		}
	}
	return nil
}

func afterLast(s, sep string) string {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return ""
	}
	return s[i+len(sep):]
}

// unescapeShell reverses shellEscape for the simple cases the test emits.
func unescapeShell(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2 {
		inner := s[1 : len(s)-1]
		return strings.ReplaceAll(inner, `'"'"'`, "'")
	}
	return s
}

func mustRel(base, target string) string {
	r, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return r
}

func decodeString(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	n := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if n < 0 || 4+n > len(payload) {
		return ""
	}
	return string(payload[4 : 4+n])
}

func sendExit(ch ssh.Channel, status int) {
	msg := struct{ Status uint32 }{uint32(status)} //nolint:gosec // small non-negative status
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&msg))
}

// --- helpers shared by tests ---

// fakeClock is a controllable Clock for keepalive tests. Callers push ticks
// into tickCh to drive the keepalive loop deterministically without wall-clock
// waits.
type fakeClock struct {
	tickCh chan time.Time
	stopCh chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		tickCh: make(chan time.Time, 16),
		stopCh: make(chan struct{}, 1),
	}
}

func (f *fakeClock) Now() time.Time { return time.Now() }
func (f *fakeClock) NewTicker(_ time.Duration) (<-chan time.Time, func()) {
	return f.tickCh, func() {
		select {
		case f.stopCh <- struct{}{}:
		default:
		}
	}
}

func newTestClient(t *testing.T, srcRoot, remoteRoot string) (*client, *fakeGateway) {
	t.Helper()
	gw := newFakeGateway(t, remoteRoot)
	return &client{gw: gw, fsys: OSFS(srcRoot), clock: newFakeClock()}, gw
}

// bufferedConnPair returns two connected net.Conns with internal buffering, so
// the SSH version-exchange (where both sides write before reading) does not
// deadlock the way an unbuffered net.Pipe would. Fully in-process (no sockets).
func bufferedConnPair() (net.Conn, net.Conn) {
	a2b := newByteChan()
	b2a := newByteChan()
	return &bufConn{r: b2a, w: a2b}, &bufConn{r: a2b, w: b2a}
}

// byteChan is an unbounded in-memory byte buffer with blocking reads.
type byteChan struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newByteChan() *byteChan {
	c := &byteChan{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *byteChan) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	c.buf = append(c.buf, p...)
	c.cond.Broadcast()
	return len(p), nil
}

func (c *byteChan) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 && !c.closed {
		c.cond.Wait()
	}
	if len(c.buf) == 0 && c.closed {
		return 0, io.EOF
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *byteChan) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.cond.Broadcast()
	return nil
}

type bufConn struct {
	r *byteChan
	w *byteChan
}

func (b *bufConn) Read(p []byte) (int, error)  { return b.r.Read(p) }
func (b *bufConn) Write(p []byte) (int, error) { return b.w.Write(p) }
func (b *bufConn) Close() error {
	_ = b.w.Close()
	return nil
}
func (b *bufConn) LocalAddr() net.Addr                { return tunnelAddr{} }
func (b *bufConn) RemoteAddr() net.Addr               { return tunnelAddr{} }
func (b *bufConn) SetDeadline(_ time.Time) error      { return nil }
func (b *bufConn) SetReadDeadline(_ time.Time) error  { return nil }
func (b *bufConn) SetWriteDeadline(_ time.Time) error { return nil }

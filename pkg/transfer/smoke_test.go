package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openshift-online/openshellctl/pkg/gateway"
)

// neverReader blocks forever on Read (until the goroutine is torn down).
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) {
	select {}
}

// These are end-to-end smoke tests over an in-process SSH server reached through
// the gateway.Gateway seam. They give confidence that the SSH/tar wiring holds
// together; the per-branch logic is covered by the pure tests. One happy-path per
// operation, plus the gateway-error path (which never touches SSH), is enough.

func TestSmokeUploadRoundTrip(t *testing.T) {
	src := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(src, "proj", "a.txt"), "A")
	writeFile(t, filepath.Join(src, "proj", "sub", "b.txt"), "B")

	c, _ := newTestClient(t, src, remote)
	var reports []string
	if err := c.Upload(context.Background(), "default", "sb", "proj", "", false, func(s string) {
		reports = append(reports, s)
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	for rel, want := range map[string]string{"proj/a.txt": "A", "proj/sub/b.txt": "B"} {
		got, err := readFileAt(remote, rel)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q err %v, want %q", rel, got, err, want)
		}
	}
	if len(reports) < 2 || !strings.HasPrefix(reports[0], "Uploading ") || reports[len(reports)-1] != "✓ Upload complete" {
		t.Errorf("reports = %v", reports)
	}
}

func TestSmokeDownloadRoundTrip(t *testing.T) {
	src := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(remote, "d", "x.txt"), "X")
	writeFile(t, filepath.Join(remote, "d", "sub", "y.txt"), "Y")

	c, _ := newTestClient(t, src, remote)
	dest := filepath.Join(t.TempDir(), "out")
	if err := c.Download(context.Background(), "default", "sb", "/ws/d", dest, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	for rel, want := range map[string]string{"x.txt": "X", "sub/y.txt": "Y"} {
		got, err := readFileAt(dest, rel)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q err %v want %q", rel, got, err, want)
		}
	}
}

func TestSmokeDownloadFile(t *testing.T) {
	src := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(remote, "out.txt"), "downloaded")

	c, _ := newTestClient(t, src, remote)
	dest := filepath.Join(t.TempDir(), "local.txt")
	var reports []string
	if err := c.Download(context.Background(), "default", "sb", "/ws/out.txt", dest, func(s string) {
		reports = append(reports, s)
	}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, err := readFileAt(filepath.Dir(dest), filepath.Base(dest)); err != nil || string(got) != "downloaded" {
		t.Errorf("dest = %q err %v", got, err)
	}
	if len(reports) < 2 || reports[len(reports)-1] != "✓ Download complete" {
		t.Errorf("reports = %v", reports)
	}
}

func TestSmokeDownloadMissing(t *testing.T) {
	c, _ := newTestClient(t, t.TempDir(), t.TempDir())
	err := c.Download(context.Background(), "default", "sb", "/ws/nope", filepath.Join(t.TempDir(), "x"), nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want does-not-exist error, got %v", err)
	}
}

func TestSmokeDownloadDirOverFileFails(t *testing.T) {
	src := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(remote, "d", "x.txt"), "X")

	c, _ := newTestClient(t, src, remote)
	dest := filepath.Join(t.TempDir(), "afile")
	writeFile(t, dest, "existing")
	err := c.Download(context.Background(), "default", "sb", "/ws/d", dest, nil)
	if err == nil || !strings.Contains(err.Error(), "non-directory destination") {
		t.Fatalf("want non-directory error, got %v", err)
	}
}

func TestSmokeDownloadFileOverDirFails(t *testing.T) {
	src := t.TempDir()
	remote := t.TempDir()
	writeFile(t, filepath.Join(remote, "f.txt"), "content")

	c, _ := newTestClient(t, src, remote)
	destDir := t.TempDir()
	// A directory already occupies the final file path → cannot overwrite.
	writeFile(t, filepath.Join(destDir, "f.txt", "child"), "x")
	err := c.Download(context.Background(), "default", "sb", "/ws/f.txt", destDir, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot overwrite directory") {
		t.Fatalf("want overwrite-directory error, got %v", err)
	}
}

func TestSmokeConnectExitCode(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "hello from sandbox\n"
	gw.mainExit = 7

	var out bytes.Buffer
	code, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if !strings.Contains(out.String(), "hello from sandbox") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestSmokeConnectDetach(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainExit = 42   // would be the code if we didn't detach first
	gw.mainHold = true // keep the session live so the detach chord wins deterministically

	code, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: bytes.NewReader([]byte{ctrlP, ctrlQ}), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if code != 0 {
		t.Errorf("detach exit code = %d, want 0", code)
	}
}

// tunnelErrGateway fails every SSHTunnel — exercises the gateway-error path that
// each operation must surface before any SSH work.
type tunnelErrGateway struct{ gateway.Gateway }

func (tunnelErrGateway) SSHTunnel(context.Context, string, string) (io.ReadWriteCloser, error) {
	return nil, errors.New("tunnel refused")
}

func TestSmokeGatewayTunnelError(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "x")
	c := &client{gw: tunnelErrGateway{}, fsys: OSFS(src)}
	ctx := context.Background()

	if err := c.Upload(ctx, "default", "sb", "a.txt", "", false, nil); err == nil || !strings.Contains(err.Error(), "tunnel refused") {
		t.Errorf("upload tunnel err = %v", err)
	}
	if err := c.Download(ctx, "default", "sb", "/ws/a.txt", filepath.Join(t.TempDir(), "o"), nil); err == nil || !strings.Contains(err.Error(), "tunnel refused") {
		t.Errorf("download tunnel err = %v", err)
	}
	if _, err := c.Connect(ctx, "default", "sb", false, NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "tunnel refused") {
		t.Errorf("connect tunnel err = %v", err)
	}
}

func TestConnect_UsesShellRequest(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "shell output\n"

	var out bytes.Buffer
	code, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "shell output") {
		t.Errorf("stdout = %q, want shell output", out.String())
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	gw.mu.Unlock()
	if reqType != "shell" {
		t.Errorf("request type = %q, want \"shell\"", reqType)
	}
}

func TestConnect_ShellNotSubsystem(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "data\n"

	_, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	gw.mu.Unlock()
	if reqType == "subsystem" {
		t.Error("Connect must send a shell request, not a subsystem request; the server only accepts sftp as a subsystem")
	}
}

func TestConnect_ServerRejectsShell(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.rejectShell = true

	_, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected error when server rejects shell request")
	}
}

func TestConnect_ExecWithCommand(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "command output\n"
	gw.mainExit = 3

	var out bytes.Buffer
	code, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}},
		"claude", "/job-sop-improve")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(out.String(), "command output") {
		t.Errorf("stdout = %q, want command output", out.String())
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	execCmd := gw.lastExecCmd
	gw.mu.Unlock()
	if reqType != "exec" {
		t.Errorf("request type = %q, want \"exec\"", reqType)
	}
	if execCmd != "claude /job-sop-improve" {
		t.Errorf("exec command = %q, want \"claude /job-sop-improve\"", execCmd)
	}
}

func TestConnect_ExecEscapesArgs(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "ok\n"

	_, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}},
		"echo", "hello world", "it's fine")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	gw.mu.Lock()
	execCmd := gw.lastExecCmd
	gw.mu.Unlock()
	if !strings.Contains(execCmd, "'hello world'") {
		t.Errorf("exec command should shell-escape args with spaces: %q", execCmd)
	}
	if !strings.Contains(execCmd, "'it'\"'\"'s fine'") {
		t.Errorf("exec command should shell-escape args with quotes: %q", execCmd)
	}
}

func TestConnect_NoCommandUsesShell(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "shell\n"

	_, err := c.Connect(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	gw.mu.Unlock()
	if reqType != "shell" {
		t.Errorf("no-command should use shell request, got %q", reqType)
	}
}

func TestConnect_ContextCancellation(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainHold = true

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := c.Connect(ctx, "default", "sb", false,
		NopTerminal{In: neverReader{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got %v", err)
	}
}

func TestConnect_ShellWithTTY(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "tty output\n"

	var out bytes.Buffer
	code, err := c.Connect(context.Background(), "default", "sb", true,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	gw.mu.Unlock()
	if reqType != "shell" {
		t.Errorf("request type = %q, want \"shell\" even with TTY", reqType)
	}
}

func TestConnect_KeepalivesSent(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainHold = true

	fc := c.clock.(*fakeClock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Connect(ctx, "default", "sb", false,
			NopTerminal{In: neverReader{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	}()

	// Push 3 ticks to drive keepalive pings.
	for i := 0; i < 3; i++ {
		fc.tickCh <- time.Now()
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done

	gw.mu.Lock()
	count := gw.keepaliveCount
	gw.mu.Unlock()
	if count < 3 {
		t.Errorf("keepalive count = %d, want >= 3", count)
	}
}

func TestConnectMainProcess_UsesSubsystem(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "main process output\n"
	gw.mainExit = 5

	var out bytes.Buffer
	code, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("ConnectMainProcess: %v", err)
	}
	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	if !strings.Contains(out.String(), "main process output") {
		t.Errorf("stdout = %q, want main process output", out.String())
	}

	gw.mu.Lock()
	reqType := gw.lastReqType
	subsystem := gw.lastSubsystem
	gw.mu.Unlock()
	if reqType != "subsystem" {
		t.Errorf("ConnectMainProcess request type = %q, want \"subsystem\"", reqType)
	}
	if subsystem != "openshell-main" {
		t.Errorf("subsystem = %q, want \"openshell-main\"", subsystem)
	}
}

func TestConnectMainProcess_ExitCodeReturned(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainExit = 42

	code, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("ConnectMainProcess: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestConnectMainProcess_DetachWorks(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainExit = 7
	gw.mainHold = true

	code, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: bytes.NewReader([]byte{ctrlP, ctrlQ}), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("ConnectMainProcess: %v", err)
	}
	if code != 0 {
		t.Errorf("detach exit code = %d, want 0", code)
	}
}

// TestConnectMainProcess_NoExitStatus_ReturnsError verifies that when the SSH
// channel closes without sending an exit-status request, ConnectMainProcess
// returns an error rather than (0, nil). A killed or crashed main process may
// not deliver exit-status; silently treating that as exit 0 would cause cron
// jobs to record false success.
func TestConnectMainProcess_NoExitStatus_ReturnsError(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "some output\n"
	gw.noExitStatus = true // server closes channel without sending exit-status

	var out bytes.Buffer
	_, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &out, Err: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("ConnectMainProcess must return an error when no exit-status was received")
	}
	if !errors.Is(err, ErrNoExitStatus) {
		t.Errorf("error should be ErrNoExitStatus, got: %v", err)
	}
}

var errSyntheticDrop = errors.New("synthetic transport drop")

// TestConnectMainProcess_TransportLoss_ReportsTunnelError verifies that when the
// SSH channel closes without exit-status AND the underlying tunnel recorded an
// error, ConnectMainProcess returns an *AttachClosedError whose Tunnel field
// wraps that error — so callers can distinguish a killed process from a broken
// network path.
func TestConnectMainProcess_TransportLoss_ReportsTunnelError(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainReply = "some output\n"
	gw.dropAfterMainReply = true    // close raw conn after mainReply → transport dies mid-session
	gw.tunnelErr = errSyntheticDrop // EOF from closed conn becomes this sentinel

	_, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrNoExitStatus) {
		t.Errorf("errors.Is(err, ErrNoExitStatus) = false, got: %v", err)
	}
	if !errors.Is(err, errSyntheticDrop) {
		t.Errorf("errors.Is(err, errSyntheticDrop) = false, got: %v", err)
	}
}

func TestConnectMainProcess_GatewayError(t *testing.T) {
	c := &client{gw: tunnelErrGateway{}}
	_, err := c.ConnectMainProcess(context.Background(), "default", "sb", false,
		NopTerminal{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "tunnel refused") {
		t.Errorf("ConnectMainProcess tunnel err = %v, want tunnel refused", err)
	}
}

func TestConnect_KeepaliveRejectionDoesNotTimeout(t *testing.T) {
	c, gw := newTestClient(t, t.TempDir(), t.TempDir())
	gw.mainHold = true
	gw.rejectKeepalive = true

	fc := c.clock.(*fakeClock)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := c.Connect(ctx, "default", "sb", false,
			NopTerminal{In: neverReader{}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}})
		done <- err
	}()

	// Push 4 rejected keepalive ticks — should NOT cause timeout because the
	// server replied (even though it rejected). Only transport errors count.
	for i := 0; i < 4; i++ {
		fc.tickCh <- time.Now()
		time.Sleep(50 * time.Millisecond)
	}

	// If we get here without the connection dying, the fix is working.
	// Cancel to clean up.
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled or nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after context cancel")
	}
}

// TestSmokeUploadLocalMissing covers the pre-SSH stat error (a real branch in
// the Upload shell that the pure tests can't reach).
func TestSmokeUploadLocalMissing(t *testing.T) {
	c, _ := newTestClient(t, t.TempDir(), t.TempDir())
	err := c.Upload(context.Background(), "default", "sb", "nope.txt", "", false, nil)
	if err == nil || err.Error() != "local path does not exist: nope.txt" {
		t.Fatalf("want local-missing error, got %v", err)
	}
}

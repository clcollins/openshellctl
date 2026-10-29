package transfer

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests exercise pkg/transfer's pure logic directly: the shell-quoting,
// path validation, upload/download command construction, tar entry shaping, the
// tar-extraction traversal guard, and the detach-chord state machine. The SSH
// and os glue is covered separately by a single mock-gateway smoke test.

func TestShellEscape(t *testing.T) {
	cases := map[string]string{
		"":           "''",
		"simple.txt": "simple.txt",
		"a/b-c_d.e":  "a/b-c_d.e",
		"/abs/path":  "/abs/path",
		"has space":  "'has space'",
		"semi;colon": "'semi;colon'",
		"star*":      "'star*'",
		"it's":       `'it'"'"'s'`,
	}
	for in, want := range cases {
		if got := ShellEscape(in); got != want {
			t.Errorf("ShellEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitSandboxPath(t *testing.T) {
	cases := []struct{ in, parent, name string }{
		{"foo.txt", ".", "foo.txt"},
		{"/foo.txt", "/", "foo.txt"},
		{"a/b/c.txt", "a/b", "c.txt"},
		{"/a/b/c.txt", "/a/b", "c.txt"},
	}
	for _, c := range cases {
		p, n := splitSandboxPath(c.in)
		if p != c.parent || n != c.name {
			t.Errorf("splitSandboxPath(%q) = (%q,%q), want (%q,%q)", c.in, p, n, c.parent, c.name)
		}
	}
}

func TestLexicalCleanAbsolutePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/a/b/../c", "/a/c", true},
		{"/a//b/./c/", "/a/b/c", true},
		{"/", "/", true},
		{"/..", "/", true},
		{"/a/../..", "/", true},
		{"relative", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := lexicalCleanAbsolutePath(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("lexicalClean(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPathIsOrUnder(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/ws", "/ws", true},
		{"/ws/a/b", "/ws", true},
		{"/ws/", "/ws", true},
		{"/wsX", "/ws", false},
		{"/other", "/ws", false},
	}
	for _, c := range cases {
		if got := pathIsOrUnder(c.p, c.root); got != c.want {
			t.Errorf("pathIsOrUnder(%q,%q) = %v, want %v", c.p, c.root, got, c.want)
		}
	}
}

func TestSanitizeTarName(t *testing.T) {
	for _, bad := range []string{"../escape", "/abs", "a/../../esc", ".."} {
		if _, err := sanitizeTarName(bad); err == nil {
			t.Errorf("sanitizeTarName(%q) should be rejected", bad)
		}
	}
	if got, err := sanitizeTarName("a/b.txt"); err != nil || got != filepath.Clean("a/b.txt") {
		t.Errorf("sanitizeTarName ok case: got %q err %v", got, err)
	}
}

func TestValidateSymlinkTarget(t *testing.T) {
	cases := []struct {
		name     string
		linkname string
		linkdir  string
		wantErr  bool
	}{
		{"absolute target", "/etc/passwd", ".", true},
		{"dot-dot escapes from root", "../../escape", ".", true},
		{"bare dot-dot from root", "..", ".", true},
		{"relative sibling", "f.txt", ".", false},
		{"relative in subdir", "sub/f.txt", ".", false},
		{"dot target", ".", ".", false},
		{"deep relative stays inside from subdir", "../../pkg/bin/tool", "node_modules/.bin", false},
		{"relative up from nested dir stays inside", "../lib/mod.py", "venv/lib64", false},
		{"relative up from root escapes", "../outside", ".", true},
		{"deep escape from subdir", "../../../escape", "sub/deep", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSymlinkTarget(tc.linkname, tc.linkdir)
			if tc.wantErr && err == nil {
				t.Errorf("validateSymlinkTarget(%q, %q) should have been rejected", tc.linkname, tc.linkdir)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSymlinkTarget(%q, %q) unexpected error: %v", tc.linkname, tc.linkdir, err)
			}
		})
	}
}

func TestPlanUpload(t *testing.T) {
	cases := []struct {
		name        string
		local, dest string
		fileLike    bool
		wantArchive string
		wantDestDir string
		wantErr     bool
	}{
		{"file to bare name in cwd", "hello.txt", "dest", true, "dest", ".", false},
		{"file to dir-slash dest", "note.txt", "sub/", true, "note.txt", "sub/", false},
		{"file to nested path", "note.txt", "out/renamed.txt", true, "renamed.txt", "out", false},
		{"file no dest", "a.txt", "", true, "a.txt", ".", false},
		{"file dest parent is root falls through", "a.txt", "/x", true, "a.txt", "/x", false},
		{"dir named", "proj", "", false, "proj", ".", false},
		{"dir with dest", "proj", "target", false, "proj", "target", false},
		{"dir dot flattens", ".", "", false, ".", ".", false},
		{"file with no basename", "/", "", true, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entries, destDir, err := planUpload(c.local, c.dest, c.fileLike)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got entries=%v", entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(entries) != 1 || entries[0].archivePath != c.wantArchive {
				t.Errorf("entries = %v, want archivePath %q", entries, c.wantArchive)
			}
			if destDir != c.wantDestDir {
				t.Errorf("destDir = %q, want %q", destDir, c.wantDestDir)
			}
		})
	}
}

func TestChooseUploadEntries(t *testing.T) {
	unfiltered := []uploadEntry{{fsPath: "d", archivePath: "d"}}
	filtered := []uploadEntry{{fsPath: "d/a.txt", archivePath: "d/a.txt"}}

	if got, warn := chooseUploadEntries(unfiltered, filtered); warn || len(got) != 1 || got[0].archivePath != "d/a.txt" {
		t.Errorf("non-empty filtered should be used: got=%v warn=%v", got, warn)
	}
	if got, warn := chooseUploadEntries(unfiltered, nil); !warn || len(got) != 1 || got[0].archivePath != "d" {
		t.Errorf("empty filtered should fall back with warning: got=%v warn=%v", got, warn)
	}
}

func TestDirectoryUploadPrefix(t *testing.T) {
	cases := map[string]string{"proj": "proj", "a/b/proj": "proj", ".": ".", "/": "."}
	for in, want := range cases {
		if got := directoryUploadPrefix(in); got != want {
			t.Errorf("directoryUploadPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDestHelpers(t *testing.T) {
	if displayDest("") != "~" || displayDest("x") != "x" {
		t.Error("displayDest")
	}
	if destOrDot("") != "." || destOrDot("d") != "d" {
		t.Error("destOrDot")
	}
}

func TestUploadExtractCommand(t *testing.T) {
	if got := uploadExtractCommand("."); got != "mkdir -p . && cat | tar xf - -C ." {
		t.Errorf("dot dest = %q", got)
	}
	if got := uploadExtractCommand("my dir"); got != "mkdir -p 'my dir' && cat | tar xf - -C 'my dir'" {
		t.Errorf("escaped dest = %q", got)
	}
}

func TestSourceProbeCommand(t *testing.T) {
	if got := sourceProbeCommand("/ws/a.txt"); got != "pwd -P && realpath -e -- /ws/a.txt" {
		t.Errorf("got %q", got)
	}
	if got := sourceProbeCommand("has space"); got != "pwd -P && realpath -e -- 'has space'" {
		t.Errorf("escaped got %q", got)
	}
}

func TestTypeProbeCommand(t *testing.T) {
	want := "if [ -d /ws/d ]; then printf dir; elif [ -e /ws/d ]; then printf file; else printf missing; fi"
	if got := typeProbeCommand("/ws/d"); got != want {
		t.Errorf("got %q", got)
	}
}

func TestTarCommands(t *testing.T) {
	if got := singleFileTarCommand("/ws", "-weird"); got != "tar cf - -C /ws -- -weird" {
		t.Errorf("single = %q", got)
	}
	if got := dirTarCommand("/ws/d"); got != "tar cf - -C /ws/d ." {
		t.Errorf("dir = %q", got)
	}
}

func TestParseAndValidateSourcePath(t *testing.T) {
	cases := []struct {
		name    string
		remote  string
		ok      bool
		stdout  string
		want    string
		wantErr string
	}{
		{"absolute in workspace", "/ws/a.txt", true, "/ws\n/ws/a.txt", "/ws/a.txt", ""},
		{"relative resolved under root", "a.txt", true, "/ws\n/ws/a.txt", "/ws/a.txt", ""},
		{"probe failed", "/ws/nope", false, "", "", "does not exist"},
		{"no newline", "/ws/a", true, "just-root", "", "unexpected response"},
		{"extra newline in resolved", "/ws/a", true, "/ws\n/ws/a\nextra", "", "unexpected response"},
		{"workspace root is /", "/ws/a", true, "/\n/a", "", "container root"},
		{"requested outside workspace", "/etc/passwd", true, "/ws\n/ws/a", "", "is outside the sandbox workspace"},
		{"resolves outside workspace (symlink escape)", "/ws/link", true, "/ws\n/etc/passwd", "", "resolves to '/etc/passwd', outside"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAndValidateSourcePath(c.remote, c.ok, []byte(c.stdout))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestValidateWorkspaceRoot(t *testing.T) {
	cases := []struct{ root, wantErr string }{
		{"/ws", ""},
		{"relative", "must be an absolute path"},
		{"/", "container root"},
		{"/a/../b", "canonical"},
	}
	for _, c := range cases {
		err := validateWorkspaceRoot(c.root)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("validateWorkspaceRoot(%q) = %v, want nil", c.root, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("validateWorkspaceRoot(%q) = %v, want %q", c.root, err, c.wantErr)
		}
	}
}

func TestParseSourceKind(t *testing.T) {
	cases := []struct {
		name       string
		exitStatus int
		stdout     string
		want       sourceKind
		wantErr    string
	}{
		{"dir", 0, "dir", sourceDir, ""},
		{"file", 0, "file", sourceFile, ""},
		{"missing", 0, "missing", 0, "does not exist"},
		{"probe nonzero", 1, "", 0, "ssh probe exited with status 1"},
		{"garbage", 0, "weird", 0, "unexpected probe output"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSourceKind("/ws/x", c.exitStatus, []byte(c.stdout))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %v err %v, want %v", got, err, c.want)
			}
		})
	}
}

func TestResolveFileDownloadTarget(t *testing.T) {
	dir := t.TempDir()
	if got := resolveFileDownloadTarget("", "a.txt"); got != "a.txt" {
		t.Errorf("empty dest = %q", got)
	}
	if got := resolveFileDownloadTarget("out/", "a.txt"); got != filepath.Join("out", "a.txt") {
		t.Errorf("slash dest = %q", got)
	}
	if got := resolveFileDownloadTarget(dir, "a.txt"); got != filepath.Join(dir, "a.txt") {
		t.Errorf("existing-dir dest = %q", got)
	}
	if got := resolveFileDownloadTarget("plain.txt", "a.txt"); got != "plain.txt" {
		t.Errorf("file dest = %q", got)
	}
}

func TestDecodeProbeStdout(t *testing.T) {
	cases := map[string]string{
		"/ws\n/ws/a\n":   "/ws\n/ws/a", // strips one trailing \n
		"/ws\n/ws/a\r\n": "/ws\n/ws/a", // strips one \n then one \r
		"/ws\n/ws/a\r":   "/ws\n/ws/a", // strips the trailing \r
		"plain":          "plain",
	}
	for in, want := range cases {
		if got := decodeProbeStdout([]byte(in)); got != want {
			t.Errorf("decodeProbeStdout(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitignorePatternToRepoRel(t *testing.T) {
	cases := []struct{ line, dir, want string }{
		{"# comment", "", ""},
		{"   ", "sub", ""},
		{"*.log", "", "*.log"},
		{"*.log", "sub", "sub/**/*.log"},
		{"/build", "sub", "sub/build"},
		{"!keep.txt", "sub", "!sub/**/keep.txt"},
		{"trailing\r", "", "trailing"},
	}
	for _, c := range cases {
		if got := gitignorePatternToRepoRel(c.line, c.dir); got != c.want {
			t.Errorf("gitignorePatternToRepoRel(%q,%q) = %q, want %q", c.line, c.dir, got, c.want)
		}
	}
}

func TestIsUnderGitDir(t *testing.T) {
	if !isUnderGitDir(".git") || !isUnderGitDir(".git/config") {
		t.Error("should be under .git")
	}
	if isUnderGitDir(".gitignore") {
		t.Error(".gitignore is not under .git")
	}
}

func TestTrimPrefixSlash(t *testing.T) {
	cases := []struct{ child, base, want string }{
		{"a/b", ".", "a/b"},
		{"repo/x", "repo", "x"},
		{"repo", "repo", ""},
		{"repo/a/b", "repo", "a/b"},
	}
	for _, c := range cases {
		if got := trimPrefixSlash(c.child, c.base); got != c.want {
			t.Errorf("trimPrefixSlash(%q,%q) = %q, want %q", c.child, c.base, got, c.want)
		}
	}
}

func TestFilterDetach(t *testing.T) {
	cases := []struct {
		name        string
		pending     bool // initial pending state
		data        []byte
		want        []byte
		detach      bool
		wantPending bool // expected pending state after call
	}{
		{
			name:        "plain bytes pass through",
			data:        []byte("hello"),
			want:        []byte("hello"),
			wantPending: false,
		},
		{
			name:        "chord in one chunk",
			data:        []byte{ctrlP, ctrlQ},
			want:        []byte{},
			detach:      true,
			wantPending: false,
		},
		{
			name:        "bytes before chord forwarded, chord bytes dropped",
			data:        []byte{'a', ctrlP, ctrlQ},
			want:        []byte{'a'},
			detach:      true,
			wantPending: false,
		},
		{
			name:        "chord split: first chunk ends with ctrl-p",
			data:        []byte{'x', ctrlP},
			want:        []byte{'x'},
			detach:      false,
			wantPending: true,
		},
		{
			name:        "chord split: second chunk starts with ctrl-q",
			pending:     true,
			data:        []byte{ctrlQ, 'y'},
			want:        []byte{},
			detach:      true,
			wantPending: false,
		},
		{
			name:        "consecutive ctrl-p forwards one and stays armed",
			data:        []byte{ctrlP, ctrlP},
			want:        []byte{ctrlP},
			detach:      false,
			wantPending: true,
		},
		{
			name:        "ctrl-p then non-chord byte forwards both",
			pending:     true,
			data:        []byte{'x'},
			want:        []byte{ctrlP, 'x'},
			detach:      false,
			wantPending: false,
		},
		{
			name:        "trailing ctrl-p held not forwarded",
			data:        []byte{'a', ctrlP},
			want:        []byte{'a'},
			detach:      false,
			wantPending: true,
		},
		{
			name:        "empty input with pending keeps pending",
			pending:     true,
			data:        []byte{},
			want:        []byte{},
			detach:      false,
			wantPending: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pending := c.pending
			got, detach := filterDetach(c.data, &pending)
			if detach != c.detach {
				t.Errorf("detach=%v, want %v", detach, c.detach)
			}
			if pending != c.wantPending {
				t.Errorf("pending=%v, want %v", pending, c.wantPending)
			}
			want := c.want
			if want == nil {
				want = []byte{}
			}
			if !bytes.Equal(got, want) {
				t.Errorf("forward=%v, want %v", got, want)
			}
		})
	}
}

func TestPumpDetach(t *testing.T) {
	t.Run("chord detaches without forwarding", func(t *testing.T) {
		var dst bytes.Buffer
		if !pumpDetach(bytes.NewReader([]byte{ctrlP, ctrlQ}), &dst) {
			t.Error("expected detach")
		}
		if dst.Len() != 0 {
			t.Errorf("chord bytes should not be forwarded, got %v", dst.Bytes())
		}
	})
	t.Run("lone ctrl-p is forwarded", func(t *testing.T) {
		var dst bytes.Buffer
		if pumpDetach(bytes.NewReader([]byte{'a', 'b', ctrlP, 'c'}), &dst) {
			t.Error("should not detach")
		}
		if !bytes.Equal(dst.Bytes(), []byte{'a', 'b', ctrlP, 'c'}) {
			t.Errorf("forwarded = %v", dst.Bytes())
		}
	})
	t.Run("double ctrl-p forwards one and stays armed", func(t *testing.T) {
		var dst bytes.Buffer
		if !pumpDetach(bytes.NewReader([]byte{ctrlP, ctrlP, ctrlQ}), &dst) {
			t.Error("expected detach after second armed ctrl-p")
		}
		if !bytes.Equal(dst.Bytes(), []byte{ctrlP}) {
			t.Errorf("forwarded = %v, want single ctrl-p", dst.Bytes())
		}
	})
}

// countingWriter records how many Write calls it receives and the total bytes.
type countingWriter struct {
	buf    bytes.Buffer
	writes int
	total  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.total += len(p)
	return w.buf.Write(p)
}

func TestPumpDetach_CoalescesWrites(t *testing.T) {
	// 12,070 non-control bytes fit in a single stdinChunkSize Read.
	// bytes.Reader returns (n, nil) on the data read then (0, io.EOF) on the
	// next read, and the EOF read produces no Write — so exactly 1 Write.
	const size = 12070
	data := bytes.Repeat([]byte{'a'}, size)
	var wc countingWriter
	pumpDetach(bytes.NewReader(data), &wc)
	if wc.total != size {
		t.Errorf("pumpDetach forwarded %d bytes, want %d", wc.total, size)
	}
	if wc.writes != 1 {
		t.Errorf("pumpDetach made %d Write calls for %d bytes; want 1 (single 32 KiB read, no write on EOF read)", wc.writes, size)
	}
}

// chunkedReader delivers data in exact slices, one per Read call.
type chunkedReader struct {
	chunks [][]byte
	idx    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}

func TestPumpDetach_ChordSplitAcrossReads(t *testing.T) {
	// First chunk ends with Ctrl-P; second chunk starts with Ctrl-Q.
	src := &chunkedReader{chunks: [][]byte{
		{'d', 'a', 't', 'a', ctrlP},
		{ctrlQ, 'x'},
	}}
	var dst bytes.Buffer
	if !pumpDetach(src, &dst) {
		t.Error("expected detach when chord split across two Read calls")
	}
	if !bytes.Equal(dst.Bytes(), []byte("data")) {
		t.Errorf("forwarded = %v, want %q", dst.Bytes(), "data")
	}
}

func TestAttachClosedError_IsErrNoExitStatus(t *testing.T) {
	ace := &AttachClosedError{}
	if !errors.Is(ace, ErrNoExitStatus) {
		t.Errorf("errors.Is(&AttachClosedError{}, ErrNoExitStatus) = false, want true")
	}
}

func TestAttachClosedError_UnwrapsTunnelError(t *testing.T) {
	tunnelErr := errors.New("tunnel gone")
	ace := &AttachClosedError{Tunnel: tunnelErr}
	if !errors.Is(ace, tunnelErr) {
		t.Errorf("errors.Is(&AttachClosedError{Tunnel: tunnelErr}, tunnelErr) = false, want true")
	}
}

func TestAttachClosedError_ErrorText(t *testing.T) {
	t.Run("without tunnel error", func(t *testing.T) {
		got := (&AttachClosedError{}).Error()
		want := "ssh channel closed without exit-status"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
	t.Run("with tunnel error", func(t *testing.T) {
		got := (&AttachClosedError{Tunnel: errors.New("network lost")}).Error()
		want := "ssh channel closed without exit-status; tunnel: network lost"
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
}

func TestOutDoneResult_KeepaliveDeadFirst(t *testing.T) {
	kd := make(chan struct{})
	close(kd) // keepalive already dead — must take priority over exit-status absence
	_, err := outDoneResult(false, 0, kd, nil)
	if err == nil || !strings.Contains(err.Error(), "keepalive timeout") {
		t.Errorf("expected keepalive timeout error, got %v", err)
	}
}

func TestOutDoneResult_ExitSet(t *testing.T) {
	kd := make(chan struct{}) // keepalive alive
	code, err := outDoneResult(true, 42, kd, nil)
	if code != 42 || err != nil {
		t.Errorf("outDoneResult(exitSet=true, 42, ...) = (%d, %v), want (42, nil)", code, err)
	}
}

func TestOutDoneResult_ExitSetWinsOverKeepaliveDead(t *testing.T) {
	// Both exitSet=true AND keepaliveDead closed: a delivered exit-status is
	// authoritative; the keepalive goroutine may have accumulated failures while
	// the process was exiting normally. Exit code must win.
	kd := make(chan struct{})
	close(kd)
	code, err := outDoneResult(true, 7, kd, nil)
	if err != nil || code != 7 {
		t.Errorf("outDoneResult(exitSet=true, 7, kd=closed, nil) = (%d, %v), want (7, nil)", code, err)
	}
}

func TestOutDoneResult_NoExitStatus(t *testing.T) {
	kd := make(chan struct{}) // keepalive alive
	_, err := outDoneResult(false, 0, kd, nil)
	if !errors.Is(err, ErrNoExitStatus) {
		t.Errorf("outDoneResult(exitSet=false, ...) error %v should be ErrNoExitStatus", err)
	}
}

// errRWC is a fake io.ReadWriteCloser that returns sentinel errors on demand.
type errRWC struct {
	readErr  error
	writeErr error
}

func (e *errRWC) Read(_ []byte) (int, error)  { return 0, e.readErr }
func (e *errRWC) Write(_ []byte) (int, error) { return 0, e.writeErr }
func (e *errRWC) Close() error                { return nil }

var (
	errRead1  = errors.New("read error 1")
	errRead2  = errors.New("read error 2")
	errWrite1 = errors.New("write error 1")
)

func TestTunnelConn_RecordsFirstReadError(t *testing.T) {
	tc := rwcConn(&errRWC{readErr: errRead1}).(*tunnelConn)
	buf := make([]byte, 4)
	tc.Read(buf) //nolint:errcheck
	if tc.firstErr() != errRead1 {
		t.Errorf("firstErr() = %v, want %v", tc.firstErr(), errRead1)
	}
}

func TestTunnelConn_RecordsFirstWriteError(t *testing.T) {
	tc := rwcConn(&errRWC{readErr: io.EOF, writeErr: errWrite1}).(*tunnelConn)
	buf := []byte("hi")
	tc.Write(buf) //nolint:errcheck
	if tc.firstErr() != errWrite1 {
		t.Errorf("firstErr() = %v, want %v", tc.firstErr(), errWrite1)
	}
}

func TestTunnelConn_FirstErrorWins(t *testing.T) {
	rwc := &errRWC{readErr: errRead1}
	tc := rwcConn(rwc).(*tunnelConn)
	buf := make([]byte, 4)
	tc.Read(buf) //nolint:errcheck
	// Swap to a different error; firstErr should still return the first.
	rwc.readErr = errRead2
	tc.Read(buf) //nolint:errcheck
	if tc.firstErr() != errRead1 {
		t.Errorf("firstErr() = %v, want first error %v", tc.firstErr(), errRead1)
	}
}

func TestTunnelConn_ConcurrentSafe(t *testing.T) {
	tc := rwcConn(&errRWC{readErr: errRead1, writeErr: errWrite1}).(*tunnelConn)
	var wg sync.WaitGroup
	buf := make([]byte, 4)
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); tc.Read(buf) }()  //nolint:errcheck
		go func() { defer wg.Done(); tc.Write(buf) }() //nolint:errcheck
	}
	wg.Wait()
	// firstErr must be one of the two known errors (not nil, not a data race).
	err := tc.firstErr()
	if err != errRead1 && err != errWrite1 {
		t.Errorf("unexpected firstErr() = %v", err)
	}
}

func TestTunnelConn_NilBeforeAnyError(t *testing.T) {
	// A fresh tunnelConn must return nil from firstErr() before any Read or Write.
	tc := rwcConn(&errRWC{}).(*tunnelConn)
	if got := tc.firstErr(); got != nil {
		t.Errorf("firstErr() before any Read/Write = %v, want nil", got)
	}
}

func TestWriteUploadArchive_FileDirSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "d", "b.txt"), "B")
	writeFile(t, filepath.Join(root, "d", "a.txt"), "A")
	if err := symlinkOrSkip(t, "a.txt", filepath.Join(root, "d", "link")); err != nil {
		return
	}

	var buf bytes.Buffer
	if err := writeUploadArchive(&buf, OSFS(root), []uploadEntry{{fsPath: "d", archivePath: "d"}}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	names := readTarNames(t, &buf)
	// Directory entry, then sorted children (a.txt, b.txt, link).
	want := []string{"d/|5|", "d/a.txt|0|", "d/b.txt|0|", "d/link|2|a.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v\nwant  = %v", names, want)
	}
}

func TestWriteUploadArchive_UnsupportedType(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "d", "pipe")
	writeFile(t, filepath.Join(root, "d", "keep.txt"), "k")
	if err := mkfifoOrSkip(t, fifo); err != nil {
		return
	}
	var buf bytes.Buffer
	err := writeUploadArchive(&buf, OSFS(root), []uploadEntry{{fsPath: "d", archivePath: "d"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported file type for upload") {
		t.Fatalf("want unsupported-type error, got %v", err)
	}
}

func TestExtractTar_TraversalGuard(t *testing.T) {
	dst := t.TempDir()
	for _, name := range []string{"../escape.txt", "/abs.txt", "a/../../esc.txt"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
		_, _ = tw.Write([]byte("bad"))
		_ = tw.Close()
		if err := extractTar(&buf, dst); err == nil {
			t.Errorf("extractTar(%q) should reject traversal", name)
		}
	}
}

func TestExtractTar_DirFileSymlink(t *testing.T) {
	dst := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "sub/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "sub/f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.WriteHeader(&tar.Header{Name: "sub/link", Typeflag: tar.TypeSymlink, Linkname: "f.txt"})
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/bin/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "pkg/bin/tool", Typeflag: tar.TypeReg, Size: 4, Mode: 0o755})
	_, _ = tw.Write([]byte("exec"))
	_ = tw.WriteHeader(&tar.Header{Name: "node_modules/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "node_modules/.bin/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "node_modules/.bin/tool", Typeflag: tar.TypeSymlink, Linkname: "../../pkg/bin/tool"})
	_ = tw.Close()

	if err := extractTar(&buf, dst); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if b, err := readFileAt(dst, "sub/f.txt"); err != nil || string(b) != "hi" {
		t.Errorf("file = %q err %v", b, err)
	}
	target, err := readlinkAt(dst, "sub/link")
	if err != nil || target != "f.txt" {
		t.Errorf("symlink target = %q err %v", target, err)
	}
	target, err = readlinkAt(dst, "node_modules/.bin/tool")
	if err != nil || target != "../../pkg/bin/tool" {
		t.Errorf("relative-up symlink target = %q err %v", target, err)
	}
}

func TestExtractTar_SymlinkTraversal(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dst, outside string)
		entries func(outside string) []tar.Header
		data    map[string]string // hdr.Name → content for TypeReg entries
	}{
		{
			name: "absolute linkname targets outside",
			entries: func(outside string) []tar.Header {
				return []tar.Header{
					{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: outside},
				}
			},
		},
		{
			name: "dot-dot linkname escapes to sibling",
			entries: func(_ string) []tar.Header {
				return []tar.Header{
					{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
				}
			},
		},
		{
			name: "regular entry through same-archive symlink",
			entries: func(_ string) []tar.Header {
				return []tar.Header{
					{Name: "evil", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
					{Name: "evil/pwned", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644},
				}
			},
			data: map[string]string{"evil/pwned": "bad"},
		},
		{
			name: "regular entry through pre-existing symlink in dest",
			setup: func(t *testing.T, dst, outside string) {
				t.Helper()
				if err := os.Symlink(outside, filepath.Join(dst, "escape")); err != nil {
					t.Fatal(err)
				}
			},
			entries: func(_ string) []tar.Header {
				return []tar.Header{
					{Name: "escape/pwned.txt", Typeflag: tar.TypeReg, Size: 6, Mode: 0o644},
				}
			},
			data: map[string]string{"escape/pwned.txt": "gotcha"},
		},
		{
			name: "symlink replaces existing directory",
			setup: func(t *testing.T, dst, outside string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(dst, "mydir", "child"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			entries: func(outside string) []tar.Header {
				return []tar.Header{
					{Name: "mydir", Typeflag: tar.TypeSymlink, Linkname: outside},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dst := filepath.Join(parent, "dst")
			outside := filepath.Join(parent, "outside")
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}

			if tc.setup != nil {
				tc.setup(t, dst, outside)
			}

			headers := tc.entries(outside)
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, hdr := range headers {
				h := hdr
				if content, ok := tc.data[h.Name]; ok {
					h.Size = int64(len(content))
				}
				if err := tw.WriteHeader(&h); err != nil {
					t.Fatal(err)
				}
				if content, ok := tc.data[h.Name]; ok {
					if _, err := tw.Write([]byte(content)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}

			err := extractTar(&buf, dst)
			if err == nil {
				t.Fatal("extractTar should have returned an error for symlink traversal attack")
			}

			entries, _ := os.ReadDir(outside)
			if len(entries) > 0 {
				t.Errorf("files written outside destination: %v", entries)
			}
		})
	}
}

func TestExtractTar_ReExtract(t *testing.T) {
	mkArchive := func() bytes.Buffer {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "sub/", Typeflag: tar.TypeDir, Mode: 0o755})
		_ = tw.WriteHeader(&tar.Header{Name: "sub/f.txt", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644})
		_, _ = tw.Write([]byte("v1"))
		_ = tw.WriteHeader(&tar.Header{Name: "sub/link", Typeflag: tar.TypeSymlink, Linkname: "f.txt"})
		_ = tw.Close()
		return buf
	}

	t.Run("second extract into same dest succeeds", func(t *testing.T) {
		dst := t.TempDir()
		buf1 := mkArchive()
		if err := extractTar(&buf1, dst); err != nil {
			t.Fatalf("first extract: %v", err)
		}

		buf2 := mkArchive()
		if err := extractTar(&buf2, dst); err != nil {
			t.Fatalf("second extract should succeed: %v", err)
		}

		if b, err := readFileAt(dst, "sub/f.txt"); err != nil || string(b) != "v1" {
			t.Errorf("file after re-extract = %q err %v", b, err)
		}
		target, err := readlinkAt(dst, "sub/link")
		if err != nil || target != "f.txt" {
			t.Errorf("symlink after re-extract = %q err %v", target, err)
		}
	})

	t.Run("symlink over existing directory still errors", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dst, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		_ = tw.WriteHeader(&tar.Header{Name: "sub", Typeflag: tar.TypeSymlink, Linkname: "other"})
		_ = tw.Close()

		if err := extractTar(&buf, dst); err == nil {
			t.Fatal("replacing a directory with a symlink should error")
		}
	})
}

func TestOSFSRejectsInvalidPaths(t *testing.T) {
	fsys := OSFS(t.TempDir()).(osDirFS)
	for _, bad := range []string{"../x", "/abs"} {
		if _, err := fsys.Open(bad); err == nil {
			t.Errorf("Open(%q) should reject", bad)
		}
		if _, err := fsys.Stat(bad); err == nil {
			t.Errorf("Stat(%q) should reject", bad)
		}
		if _, err := fsys.Lstat(bad); err == nil {
			t.Errorf("Lstat(%q) should reject", bad)
		}
		if _, err := fsys.Readlink(bad); err == nil {
			t.Errorf("Readlink(%q) should reject", bad)
		}
		if _, err := fsys.ReadDir(bad); err == nil {
			t.Errorf("ReadDir(%q) should reject", bad)
		}
	}
}

func TestReadlinkFSFallback(t *testing.T) {
	if _, err := readlinkFS(plainFS{}, "x"); err == nil {
		t.Error("readlinkFS on a plain fs.FS should error")
	}
}

type plainFS struct{}

func (plainFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

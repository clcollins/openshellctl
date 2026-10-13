package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestClassifyManifestSource(t *testing.T) {
	tests := []struct {
		name string
		path string
		want manifestSourceKind
	}{
		{"dash is stdin", "-", sourceStdin},
		{"http url", "http://example.com/sandbox.yaml", sourceURL},
		{"https url", "https://example.com/sandbox.yaml", sourceURL},
		{"uppercase scheme", "HTTPS://example.com/sandbox.yaml", sourceURL},
		{"mixed-case scheme", "HtTp://example.com/sandbox.yaml", sourceURL},
		{"relative path", "sandbox.yaml", sourceFile},
		{"absolute path", "/tmp/sandbox.yaml", sourceFile},
		{"ftp scheme is not a url source", "ftp://example.com/sandbox.yaml", sourceFile},
		{"file scheme is not a url source (out of scope: no behavioral difference from a local path)", "file:///tmp/sandbox.yaml", sourceFile},
		{"empty string", "", sourceFile},
		{"windows-drive-shaped path stays a file", `C:\sandbox.yaml`, sourceFile},
		{"opaque scheme-colon-no-slashes is a legal local filename, not a URL", "http:foo.yaml", sourceFile},
		{"opaque https-colon filename is also a local file", "https:notes.txt", sourceFile},
		{"malformed but unambiguously http-prefixed string still routes to the URL fetch path, for a clear error instead of a confusing file-not-found", "https://example.com/\x7f", sourceURL},
		{"malformed uppercase-prefixed string also routes to URL", "HTTPS://example.com/\x7f", sourceURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyManifestSource(tt.path); got != tt.want {
				t.Errorf("classifyManifestSource(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips presigned query string",
			raw:  "https://bucket.s3.amazonaws.com/sandbox.yaml?X-Amz-Signature=secret&X-Amz-Expires=900",
			want: "https://bucket.s3.amazonaws.com/sandbox.yaml",
		},
		{
			name: "strips token query param",
			raw:  "https://internal.example.com/manifests/sb.yaml?token=abc123",
			want: "https://internal.example.com/manifests/sb.yaml",
		},
		{
			name: "strips userinfo",
			raw:  "https://user:pass@example.com/sb.yaml",
			want: "https://example.com/sb.yaml",
		},
		{
			name: "plain url unchanged",
			raw:  "https://example.com/sb.yaml",
			want: "https://example.com/sb.yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := redactURL(u); got != tt.want {
				t.Errorf("redactURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRedactRawURLBestEffort(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "truncates at query string",
			raw:  "https://example.com/sb.yaml?token=secret",
			want: "https://example.com/sb.yaml",
		},
		{
			name: "strips userinfo, never leaking the password",
			raw:  "https://user:pass@%zzbadhost/x",
			want: "https://%zzbadhost/x",
		},
		{
			name: "query stripped before userinfo scan, and userinfo-shaped text in the query never counted as real userinfo",
			raw:  "https://example.com/sb.yaml?token=secret@evil",
			want: "https://example.com/sb.yaml",
		},
		{
			name: "no query or userinfo is unchanged",
			raw:  "https://example.com/bad",
			want: "https://example.com/bad",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactRawURLBestEffort(tt.raw)
			if got != tt.want {
				t.Errorf("redactRawURLBestEffort(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if strings.Contains(got, "pass") {
				t.Errorf("redactRawURLBestEffort(%q) = %q, must never contain the password", tt.raw, got)
			}
		})
	}
}

// fakeDoer is a hermetic manifestHTTPDoer: it never touches the network.
type fakeDoer struct {
	resp *http.Response
	err  error
	// capturedReq records the last request passed to Do, so tests can assert
	// on headers (e.g. no Authorization header, correct User-Agent).
	capturedReq *http.Request
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.capturedReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newBodyResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestFetchManifestURL_Success(t *testing.T) {
	const manifest = "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Sandbox\n"
	doer := &fakeDoer{resp: newBodyResponse(http.StatusOK, manifest)}

	data, err := fetchManifestURL(context.Background(), doer, "https://example.com/sb.yaml")
	if err != nil {
		t.Fatalf("fetchManifestURL: %v", err)
	}
	if string(data) != manifest {
		t.Errorf("data = %q, want %q", data, manifest)
	}
	if doer.capturedReq.Header.Get("Authorization") != "" {
		t.Error("manifest fetch request must never carry an Authorization header")
	}
	if ua := doer.capturedReq.Header.Get("User-Agent"); !strings.HasPrefix(ua, "openshellctl/") {
		t.Errorf("User-Agent = %q, want openshellctl/* prefix", ua)
	}
}

func TestFetchManifestURL_NonCredentialHeaders(t *testing.T) {
	// Explicit acceptance-criterion test: a manifest-fetch request's headers
	// contain no Authorization or other credential-bearing header, no matter
	// what the response is.
	doer := &fakeDoer{resp: newBodyResponse(http.StatusOK, "kind: Sandbox\n")}
	if _, err := fetchManifestURL(context.Background(), doer, "https://example.com/sb.yaml"); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Vault-Token"} {
		if v := doer.capturedReq.Header.Get(h); v != "" {
			t.Errorf("request header %s = %q, want empty (no credential leakage)", h, v)
		}
	}
}

func TestFetchManifestURL_NonHTTPStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"404", http.StatusNotFound},
		{"500", http.StatusInternalServerError},
		{"301 treated as non-2xx when returned directly (no live redirect-follow here)", http.StatusMovedPermanently},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doer := &fakeDoer{resp: newBodyResponse(tt.status, "<html>not a manifest</html>")}
			_, err := fetchManifestURL(context.Background(), doer, "https://example.com/sb.yaml?token=shh")
			if err == nil {
				t.Fatal("expected an error for a non-2xx response")
			}
			var fe *manifestFetchError
			if !errors.As(err, &fe) {
				t.Fatalf("err = %T, want *manifestFetchError", err)
			}
			if fe.Status != tt.status {
				t.Errorf("Status = %d, want %d", fe.Status, tt.status)
			}
			if strings.Contains(err.Error(), "token=shh") {
				t.Errorf("error must not leak the query string: %v", err)
			}
			if !strings.Contains(err.Error(), "example.com") {
				t.Errorf("error should name the (redacted) URL: %v", err)
			}
			if exitCodeFor(err) != ExitError {
				t.Errorf("exitCodeFor = %d, want ExitError", exitCodeFor(err))
			}
		})
	}
}

func TestFetchManifestURL_SizeCapExceeded(t *testing.T) {
	oversized := strings.Repeat("a", manifestURLSizeLimit+1)
	doer := &fakeDoer{resp: newBodyResponse(http.StatusOK, oversized)}

	_, err := fetchManifestURL(context.Background(), doer, "https://example.com/huge.yaml")
	if err == nil {
		t.Fatal("expected a size-cap error")
	}
	var se *manifestURLSizeError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T, want *manifestURLSizeError", err)
	}
	if se.Limit != manifestURLSizeLimit {
		t.Errorf("Limit = %d, want %d", se.Limit, manifestURLSizeLimit)
	}
	if !strings.Contains(err.Error(), "exceeds the") || !strings.Contains(err.Error(), "example.com") {
		t.Errorf("err.Error() = %q, want it to name the (redacted) URL and the limit", err.Error())
	}
	if exitCodeFor(err) != ExitError {
		t.Errorf("exitCodeFor = %d, want ExitError", exitCodeFor(err))
	}
}

func TestFetchManifestURL_SizeCapExactlyAtLimitSucceeds(t *testing.T) {
	exact := strings.Repeat("a", manifestURLSizeLimit)
	doer := &fakeDoer{resp: newBodyResponse(http.StatusOK, exact)}

	data, err := fetchManifestURL(context.Background(), doer, "https://example.com/exact.yaml")
	if err != nil {
		t.Fatalf("exactly-at-limit body should succeed: %v", err)
	}
	if len(data) != manifestURLSizeLimit {
		t.Errorf("len(data) = %d, want %d", len(data), manifestURLSizeLimit)
	}
}

func TestFetchManifestURL_RejectsUserinfo(t *testing.T) {
	doer := &fakeDoer{} // must never be called
	_, err := fetchManifestURL(context.Background(), doer, "https://user:pass@example.com/sb.yaml")
	if err == nil {
		t.Fatal("expected an error for a URL with userinfo")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T, want *UsageError", err)
	}
	if doer.capturedReq != nil {
		t.Error("doer.Do must never be called when the URL has userinfo")
	}
	if strings.Contains(err.Error(), "pass") {
		t.Errorf("error must not leak the password: %v", err)
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exitCodeFor = %d, want ExitUsage", exitCodeFor(err))
	}
}

func TestFetchManifestURL_MalformedURL(t *testing.T) {
	doer := &fakeDoer{}
	// A control character makes url.Parse itself fail.
	_, err := fetchManifestURL(context.Background(), doer, "https://example.com/\x7f")
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T, want *UsageError", err)
	}
	if exitCodeFor(err) != ExitUsage {
		t.Errorf("exitCodeFor = %d, want ExitUsage", exitCodeFor(err))
	}
}

func TestFetchManifestURL_NetworkError(t *testing.T) {
	doer := &fakeDoer{err: errors.New("connection refused")}
	_, err := fetchManifestURL(context.Background(), doer, "https://example.com/sb.yaml")
	if err == nil {
		t.Fatal("expected a network error")
	}
	var fe *manifestFetchError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %T, want *manifestFetchError", err)
	}
	if exitCodeFor(err) != ExitError {
		t.Errorf("exitCodeFor = %d, want ExitError", exitCodeFor(err))
	}
}

func TestRejectHTTPSDowngrade(t *testing.T) {
	mustURL := func(t *testing.T, raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	tests := []struct {
		name    string
		via     []string
		next    string
		wantErr error // nil means no error
	}{
		{
			name:    "https to http is rejected",
			via:     []string{"https://a.example.com/x"},
			next:    "http://a.example.com/x",
			wantErr: errManifestRedirectDowngrade,
		},
		{
			name: "https to https on a different host is fine",
			via:  []string{"https://a.example.com/x"},
			next: "https://b.example.com/x",
		},
		{
			name: "http to https is fine",
			via:  []string{"http://a.example.com/x"},
			next: "https://a.example.com/x",
		},
		{
			name: "http to http is fine",
			via:  []string{"http://a.example.com/x"},
			next: "http://a.example.com/x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			via := make([]*http.Request, len(tt.via))
			for i, v := range tt.via {
				via[i] = &http.Request{URL: mustURL(t, v)}
			}
			req := &http.Request{URL: mustURL(t, tt.next)}
			err := rejectHTTPSDowngrade(req, via)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("rejectHTTPSDowngrade() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("rejectHTTPSDowngrade() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRejectHTTPSDowngrade_RedirectCap(t *testing.T) {
	u, _ := url.Parse("https://example.com/x")
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = &http.Request{URL: u}
	}
	if err := rejectHTTPSDowngrade(&http.Request{URL: u}, via); err == nil {
		t.Error("expected an error after 10 redirects")
	}
}

// TestFetchManifestURL_HTTPSRedirectDowngrade_Integration exercises the real
// production doer (newManifestHTTPClient) end to end: an https:// server
// (httptest.NewTLSServer) that 302s to a plain http:// URL must be refused
// before any body is read, never silently followed. Uses
// httptest.NewTLSServer + srv.Client() so the test client trusts the test
// server's certificate without fighting http.DefaultTransport's real CA
// pool.
func TestFetchManifestURL_HTTPSRedirectDowngrade_Integration(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("kind: Sandbox\n"))
	}))
	defer httpSrv.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL, http.StatusFound)
	}))
	defer tlsSrv.Close()

	client := tlsSrv.Client()
	client.CheckRedirect = rejectHTTPSDowngrade

	_, err := fetchManifestURL(context.Background(), client, tlsSrv.URL)
	if err == nil {
		t.Fatal("expected the https->http redirect to be rejected")
	}
	if !errors.Is(err, errManifestRedirectDowngrade) {
		t.Errorf("err = %v, want errManifestRedirectDowngrade", err)
	}
	if exitCodeFor(err) != ExitError {
		t.Errorf("exitCodeFor = %d, want ExitError", exitCodeFor(err))
	}
}

// TestFetchManifestURL_TLS_Integration confirms a real TLS fetch through the
// production path (an *http.Client, not a fakeDoer) succeeds end to end
// against an httptest.NewTLSServer, using the server's own client so the
// test doesn't depend on (or weaken) the real CA pool.
func TestFetchManifestURL_TLS_Integration(t *testing.T) {
	const manifest = "apiVersion: openshell.managed.openshift.io/v1alpha1\nkind: Sandbox\nmetadata:\n  name: sb-tls\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("server received unexpected Authorization header: %q", got)
		}
		_, _ = w.Write([]byte(manifest))
	}))
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = rejectHTTPSDowngrade

	data, err := fetchManifestURL(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("fetchManifestURL: %v", err)
	}
	if string(data) != manifest {
		t.Errorf("data = %q, want %q", data, manifest)
	}
}

func TestReadManifestBytes_DispatchesByClassification(t *testing.T) {
	t.Run("stdin", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.SetIn(strings.NewReader("stdin-content"))
		data, err := readManifestBytes(cmd, &fakeDoer{}, "-")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "stdin-content" {
			t.Errorf("data = %q", data)
		}
	})

	t.Run("url", func(t *testing.T) {
		doer := &fakeDoer{resp: newBodyResponse(http.StatusOK, "url-content")}
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		data, err := readManifestBytes(cmd, doer, "https://example.com/sb.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "url-content" {
			t.Errorf("data = %q", data)
		}
	})

	t.Run("file", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/sb.yaml"
		if err := writeFileHelper(path, "file-content"); err != nil {
			t.Fatal(err)
		}
		cmd := &cobra.Command{}
		data, err := readManifestBytes(cmd, &fakeDoer{}, path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "file-content" {
			t.Errorf("data = %q", data)
		}
	})
}

// TestReadManifestBytes_MalformedURLReachesFetchManifestURL confirms, through
// the real chokepoint (not by calling fetchManifestURL directly), that a
// string url.Parse itself rejects but which is still unambiguously
// http(s)-prefixed gets the clear, typed "invalid manifest URL" UsageError —
// never a confusing os.ReadFile "no such file or directory" from being
// misclassified as a local path (correctness review: classifyManifestSource
// and fetchManifestURL must agree on what counts as a URL, or the malformed-
// URL error path is dead code unreachable from the real CLI).
func TestReadManifestBytes_MalformedURLReachesFetchManifestURL(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	doer := &fakeDoer{} // must never be called: fetchManifestURL rejects before any Do
	_, err := readManifestBytes(cmd, doer, "https://example.com/\x7f")
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T, want *UsageError (classifyManifestSource must route this to fetchManifestURL, not os.ReadFile)", err)
	}
	if doer.capturedReq != nil {
		t.Error("doer.Do must never be called for a malformed URL")
	}
}

func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestManifestUserAgent(t *testing.T) {
	ua := manifestUserAgent()
	if !strings.HasPrefix(ua, "openshellctl/") {
		t.Errorf("manifestUserAgent() = %q, want openshellctl/* prefix", ua)
	}
}

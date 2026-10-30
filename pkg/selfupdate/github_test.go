package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubFetcher_ListReleases(t *testing.T) {
	releases := []Release{
		{TagName: "v0.1.5", Draft: false, Prerelease: false},
		{TagName: "v0.1.5-rc.1", Draft: false, Prerelease: true},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != "/repos/openshift-online/openshellctl/releases" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.URL.Query().Get("per_page") != "30" {
			t.Errorf("per_page = %q, want 30", r.URL.Query().Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	f := newTestFetcher(srv)
	got, err := f.ListReleases(context.Background())
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d releases, want 2", len(got))
	}
}

func TestGitHubFetcher_GetRelease(t *testing.T) {
	rel := Release{TagName: "v0.1.5", Draft: false, Prerelease: false}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertHeaders(t, r)
		if r.URL.Path != "/repos/openshift-online/openshellctl/releases/tags/v0.1.5" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rel)
	}))
	defer srv.Close()

	f := newTestFetcher(srv)
	got, err := f.GetRelease(context.Background(), "v0.1.5")
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.TagName != "v0.1.5" {
		t.Errorf("TagName = %q, want v0.1.5", got.TagName)
	}
}

func TestGitHubFetcher_404ReturnsErrTagNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	f := newTestFetcher(srv)
	_, err := f.GetRelease(context.Background(), "v9.9.9")
	var notFound *ErrTagNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("GetRelease 404 error = %T(%v), want *ErrTagNotFound", err, err)
	}
	if notFound != nil && notFound.Tag != "v9.9.9" {
		t.Errorf("ErrTagNotFound.Tag = %q, want v9.9.9", notFound.Tag)
	}
}

func TestGitHubFetcher_403ReturnsErrRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	f := newTestFetcher(srv)
	_, err := f.ListReleases(context.Background())
	var rl *ErrRateLimited
	if !errors.As(err, &rl) {
		t.Errorf("ListReleases 403 error = %T(%v), want *ErrRateLimited", err, err)
	}
}

func TestGitHubFetcher_NonJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	f := newTestFetcher(srv)
	_, err := f.ListReleases(context.Background())
	if err == nil {
		t.Error("expected error for non-JSON body, got nil")
	}
}

func TestGitHubFetcher_NoAuthorizationForNotice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization header must not be set for notice fetcher, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Release{})
	}))
	defer srv.Close()

	// fetcher with no token must not send Authorization
	f := &GitHubFetcher{
		Client:  srv.Client(),
		BaseURL: srv.URL,
		Owner:   "openshift-online",
		Repo:    "openshellctl",
	}
	_, _ = f.ListReleases(context.Background())
}

func TestGitHubFetcher_TokenSentWhenPresent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]Release{})
	}))
	defer srv.Close()

	f := &GitHubFetcher{
		Client:  srv.Client(),
		BaseURL: srv.URL,
		Owner:   "openshift-online",
		Repo:    "openshellctl",
		Token:   "mytoken",
	}
	_, _ = f.ListReleases(context.Background())
	if gotAuth != "Bearer mytoken" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer mytoken")
	}
}

func TestGitHubFetcher_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never respond — client should cancel via context
		select {}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	f := newTestFetcher(srv)
	_, err := f.ListReleases(ctx)
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

// assertHeaders checks required request headers per the plan (Accept, User-Agent).
func assertHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", r.Header.Get("Accept"))
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "openshellctl/") {
		t.Errorf("User-Agent = %q, want prefix openshellctl/", r.Header.Get("User-Agent"))
	}
	if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", r.Header.Get("X-GitHub-Api-Version"))
	}
}

func newTestFetcher(srv *httptest.Server) *GitHubFetcher {
	return &GitHubFetcher{
		Client:  srv.Client(),
		BaseURL: srv.URL,
		Owner:   "openshift-online",
		Repo:    "openshellctl",
	}
}

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Fetcher retrieves release information from a GitHub-compatible API.
type Fetcher interface {
	ListReleases(ctx context.Context) ([]Release, error)
	GetRelease(ctx context.Context, tag string) (Release, error)
}

// GitHubFetcher implements Fetcher against the GitHub REST API.
// BaseURL defaults to https://api.github.com when empty.
// Token, when non-empty, is sent as "Authorization: Bearer <token>" only for
// the update command; the notice goroutine must use a fetcher with an empty
// Token to avoid sending or leaking credentials.
type GitHubFetcher struct {
	Client  *http.Client
	BaseURL string
	Owner   string
	Repo    string
	Token   string
	// Version is the running binary version, used in the User-Agent header.
	Version string
}

const defaultGitHubAPIBase = "https://api.github.com"

func (f *GitHubFetcher) baseURL() string {
	if f.BaseURL != "" {
		return f.BaseURL
	}
	return defaultGitHubAPIBase
}

func (f *GitHubFetcher) httpClient() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return http.DefaultClient
}

// ListReleases fetches up to 30 releases ordered by creation date descending.
func (f *GitHubFetcher) ListReleases(ctx context.Context) ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=30", f.baseURL(), f.Owner, f.Repo)
	var out []Release
	if err := f.get(ctx, url, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRelease fetches the release for the given tag. Returns ErrTagNotFound on
// HTTP 404 (the tag does not exist).
func (f *GitHubFetcher) GetRelease(ctx context.Context, tag string) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", f.baseURL(), f.Owner, f.Repo, tag)
	var out Release
	if err := f.get(ctx, url, &out); err != nil {
		var noRel *ErrNoRelease
		if errors.As(err, &noRel) {
			return Release{}, &ErrTagNotFound{Tag: tag}
		}
		return Release{}, err
	}
	return out, nil
}

func (f *GitHubFetcher) get(ctx context.Context, url string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	v := f.Version
	if v == "" {
		v = "dev"
	}
	req.Header.Set("User-Agent", "openshellctl/"+v)
	if f.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.Token)
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return &ErrNoRelease{}
	case http.StatusForbidden:
		return &ErrRateLimited{}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

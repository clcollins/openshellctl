package selfupdate

import (
	"fmt"
	"strings"
)

// Release is a GitHub release as returned by the REST API.
type Release struct {
	TagName     string  `json:"tag_name"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Asset is a single file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Current holds the classification of the running binary's version string.
type Current struct {
	Tag          string
	IsPrerelease bool
	Unknown      bool
}

// ParseCurrent classifies a version string reported by the binary.
//
// Only canonical goreleaser release tags are comparable: vMAJOR.MINOR.PATCH and
// vMAJOR.MINOR.PATCH-PRERELEASE where each dot-separated prerelease identifier
// contains no embedded hyphen. This rejects git-describe strings (e.g.
// v0.1.3-rc.1-12-g4ea8271-dirty, whose second identifier is "1-12-g4ea8271-dirty")
// and Go pseudo-versions (e.g. v0.0.0-20261020123705-bdb956f2cd65), both of
// which would produce wrong comparisons against real release tags.
func ParseCurrent(v string) Current {
	if !semverIsValid(v) {
		return Current{Unknown: true}
	}
	pre := semverPrerelease(v)
	if pre != "" {
		for _, id := range strings.Split(pre[1:], ".") { // pre[1:] strips the leading '-'
			if strings.Contains(id, "-") {
				return Current{Unknown: true}
			}
		}
	}
	return Current{
		Tag:          v,
		IsPrerelease: semverIsPrerelease(v),
	}
}

// SelectRelease picks the target release for the update or notice.
//
// When allowPrerelease is false, only non-draft releases whose tag is a
// canonical stable semver (no pre-release segment) are candidates — regardless
// of whether the GitHub release is flagged as a pre-release. This guards
// against releases like v0.0.116-rc.1 that were published without the GitHub
// pre-release flag set.
//
// When allowPrerelease is true, non-draft releases of any semver shape are
// candidates.
//
// The winning release is the one with the highest semver tag among candidates.
// The input slice order and the releases' published_at timestamps are ignored.
func SelectRelease(rels []Release, allowPrerelease bool) (Release, bool) {
	var best Release
	var bestSet bool
	for _, r := range rels {
		if r.Draft {
			continue
		}
		if !semverIsValid(r.TagName) {
			continue
		}
		if !allowPrerelease && semverIsPrerelease(r.TagName) {
			continue
		}
		if !bestSet || semverCompare(r.TagName, best.TagName) > 0 {
			best = r
			bestSet = true
		}
	}
	return best, bestSet
}

// AssetName returns the archive asset name for the given GOOS/GOARCH pair,
// matching the goreleaser name_template in .goreleaser.yaml.
// Returns ErrUnsupportedPlatform for any pair not built by goreleaser.
func AssetName(goos, goarch string) (string, error) {
	var osName, arch string
	switch goos {
	case "linux":
		osName = "Linux"
	case "darwin":
		osName = "Darwin"
	default:
		return "", &ErrUnsupportedPlatform{GOOS: goos, GOARCH: goarch}
	}
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", &ErrUnsupportedPlatform{GOOS: goos, GOARCH: goarch}
	}
	return fmt.Sprintf("openshellctl_%s_%s.tar.gz", osName, arch), nil
}

// ChecksumAssetName returns the name of the goreleaser checksum file for a
// given release tag. The tag may include or omit the leading "v"; the returned
// name always uses the version without the leading "v" to match goreleaser's
// default template "{{ .ProjectName }}_{{ .Version }}_checksums.txt".
func ChecksumAssetName(tag string) string {
	ver := tag
	if len(ver) > 0 && ver[0] == 'v' {
		ver = ver[1:]
	}
	return fmt.Sprintf("openshellctl_%s_checksums.txt", ver)
}

// FindAsset returns the first asset whose name matches name, or false if none.
func FindAsset(rel Release, name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

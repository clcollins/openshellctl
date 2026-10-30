package selfupdate

import (
	"testing"
)

func TestParseCurrent(t *testing.T) {
	tests := []struct {
		v           string
		wantTag     string
		wantUnknown bool
		wantPrerel  bool
	}{
		{"v0.1.5", "v0.1.5", false, false},
		{"v0.1.5-rc.1", "v0.1.5-rc.1", false, true},
		{"v0.1.5-rc.2", "v0.1.5-rc.2", false, true},
		{"v1.0.0", "v1.0.0", false, false},
		{"dev", "", true, false},
		{"", "", true, false},
		{"4ea8271", "", true, false},
		{"v0.1.3-rc.1-12-g4ea8271-dirty", "", true, false},
		// pseudo-version from go install → treat as unknown (non-canonical semver)
		{"v0.0.0-20261020123705-bdb956f2cd65", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.v, func(t *testing.T) {
			got := ParseCurrent(tt.v)
			if got.Unknown != tt.wantUnknown {
				t.Errorf("ParseCurrent(%q).Unknown = %v, want %v", tt.v, got.Unknown, tt.wantUnknown)
			}
			if !tt.wantUnknown {
				if got.Tag != tt.wantTag {
					t.Errorf("ParseCurrent(%q).Tag = %q, want %q", tt.v, got.Tag, tt.wantTag)
				}
				if got.IsPrerelease != tt.wantPrerel {
					t.Errorf("ParseCurrent(%q).IsPrerelease = %v, want %v", tt.v, got.IsPrerelease, tt.wantPrerel)
				}
			}
		})
	}
}

func TestSelectRelease_StableOnly(t *testing.T) {
	rels := []Release{
		{TagName: "v0.1.5", Draft: false, Prerelease: false},
		{TagName: "v0.1.5-rc.1", Draft: false, Prerelease: true},
		{TagName: "v0.1.4", Draft: false, Prerelease: false},
	}
	got, ok := SelectRelease(rels, false)
	if !ok {
		t.Fatal("SelectRelease returned no result, want v0.1.5")
	}
	if got.TagName != "v0.1.5" {
		t.Errorf("tag = %q, want v0.1.5", got.TagName)
	}
}

func TestSelectRelease_PrereleaseAllowed(t *testing.T) {
	rels := []Release{
		{TagName: "v0.1.5-rc.2", Draft: false, Prerelease: true},
		{TagName: "v0.1.5-rc.1", Draft: false, Prerelease: true},
	}
	got, ok := SelectRelease(rels, true)
	if !ok {
		t.Fatal("SelectRelease returned no result, want v0.1.5-rc.2")
	}
	if got.TagName != "v0.1.5-rc.2" {
		t.Errorf("tag = %q, want v0.1.5-rc.2", got.TagName)
	}
}

func TestSelectRelease_DraftsSkipped(t *testing.T) {
	rels := []Release{
		{TagName: "v0.1.6", Draft: true, Prerelease: false},
		{TagName: "v0.1.5", Draft: false, Prerelease: false},
	}
	got, ok := SelectRelease(rels, false)
	if !ok {
		t.Fatal("SelectRelease returned no result, want v0.1.5")
	}
	if got.TagName != "v0.1.5" {
		t.Errorf("tag = %q, want v0.1.5", got.TagName)
	}
}

func TestSelectRelease_MisFlaggedRCFilteredBySemver(t *testing.T) {
	// v0.0.116-rc.1 is NOT flagged prerelease on GitHub but IS a semver prerelease.
	rels := []Release{
		{TagName: "v0.0.116-rc.1", Draft: false, Prerelease: false},
		{TagName: "v0.1.3", Draft: false, Prerelease: false},
	}
	got, ok := SelectRelease(rels, false)
	if !ok {
		t.Fatal("SelectRelease returned no result, want v0.1.3")
	}
	if got.TagName != "v0.1.3" {
		t.Errorf("tag = %q, want v0.1.3 (mis-flagged rc must be skipped by semver check)", got.TagName)
	}
}

func TestSelectRelease_EmptyList(t *testing.T) {
	_, ok := SelectRelease(nil, false)
	if ok {
		t.Error("SelectRelease(nil, false) = ok, want not ok")
	}
}

func TestSelectRelease_NoStableRelease(t *testing.T) {
	rels := []Release{
		{TagName: "v0.1.5-rc.1", Draft: false, Prerelease: true},
	}
	_, ok := SelectRelease(rels, false)
	if ok {
		t.Error("SelectRelease with only rc should return not ok when allowPrerelease=false")
	}
}

func TestSelectRelease_OrderBySemverNotPublishedAt(t *testing.T) {
	// published_at order is reverse of semver order — the function must sort by semver.
	rels := []Release{
		{TagName: "v0.1.4", Draft: false, Prerelease: false, PublishedAt: "2026-10-29T12:00:00Z"},
		{TagName: "v0.1.5", Draft: false, Prerelease: false, PublishedAt: "2026-10-28T12:00:00Z"},
	}
	got, ok := SelectRelease(rels, false)
	if !ok {
		t.Fatal("SelectRelease returned no result")
	}
	if got.TagName != "v0.1.5" {
		t.Errorf("tag = %q, want v0.1.5 (should pick highest semver, not latest published_at)", got.TagName)
	}
}

func TestSelectRelease_InvalidTagsIgnored(t *testing.T) {
	rels := []Release{
		{TagName: "latest", Draft: false, Prerelease: false},
		{TagName: "v0.1.5", Draft: false, Prerelease: false},
	}
	got, ok := SelectRelease(rels, false)
	if !ok {
		t.Fatal("SelectRelease returned no result, want v0.1.5")
	}
	if got.TagName != "v0.1.5" {
		t.Errorf("tag = %q, want v0.1.5", got.TagName)
	}
}

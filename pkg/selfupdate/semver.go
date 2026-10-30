// Copyright (c) 2009 The Go Authors. All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:
//
//   - Redistributions of source code must retain the above copyright
//     notice, this list of conditions and the following disclaimer.
//   - Redistributions in binary form must reproduce the above copyright
//     notice, this list of conditions and the following disclaimer in the
//     documentation and/or other materials provided with the distribution.
//   - Neither the name of Google Inc. nor the names of its contributors
//     may be used to endorse or promote products derived from this software
//     without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

// Package selfupdate implements openshellctl's self-update and update-notice
// commands.
//
// This file contains semver parsing and comparison logic adapted from
// golang.org/x/mod/semver (the Go toolchain's own vendor copy). If
// golang.org/x/mod is promoted to a direct dependency, this file can be
// deleted and callers updated to import semver from that package directly.
package selfupdate

// semverParsed is the parsed form of a "vMAJOR.MINOR.PATCH[-PRERELEASE]" string.
type semverParsed struct {
	major      string
	minor      string
	patch      string
	prerelease string
}

// semverParse parses v and reports success. v must begin with 'v'.
// Only the canonical three-part form used by goreleaser is supported
// (vMAJOR.MINOR.PATCH and vMAJOR.MINOR.PATCH-PRERELEASE); build metadata (+...)
// is rejected to keep comparisons unambiguous.
func semverParse(v string) (semverParsed, bool) {
	if v == "" || v[0] != 'v' {
		return semverParsed{}, false
	}
	var p semverParsed
	var ok bool
	rest := v[1:]
	p.major, rest, ok = semverParseInt(rest)
	if !ok {
		return semverParsed{}, false
	}
	if rest == "" || rest[0] != '.' {
		return semverParsed{}, false
	}
	p.minor, rest, ok = semverParseInt(rest[1:])
	if !ok {
		return semverParsed{}, false
	}
	if rest == "" || rest[0] != '.' {
		return semverParsed{}, false
	}
	p.patch, rest, ok = semverParseInt(rest[1:])
	if !ok {
		return semverParsed{}, false
	}
	if len(rest) > 0 && rest[0] == '-' {
		p.prerelease, rest, ok = semverParsePrerelease(rest)
		if !ok {
			return semverParsed{}, false
		}
	}
	if rest != "" {
		return semverParsed{}, false
	}
	return p, true
}

func semverParseInt(v string) (tok, rest string, ok bool) {
	if v == "" || v[0] < '0' || v[0] > '9' {
		return
	}
	i := 1
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if v[0] == '0' && i != 1 {
		return // leading zero
	}
	return v[:i], v[i:], true
}

func semverParsePrerelease(v string) (tok, rest string, ok bool) {
	if v == "" || v[0] != '-' {
		return
	}
	i := 1
	start := 1
	for i < len(v) && v[i] != '+' {
		if !semverIsIdentChar(v[i]) && v[i] != '.' {
			return
		}
		if v[i] == '.' {
			if start == i || semverIsBadNum(v[start:i]) {
				return
			}
			start = i + 1
		}
		i++
	}
	if start == i || semverIsBadNum(v[start:i]) {
		return
	}
	return v[:i], v[i:], true
}

func semverIsIdentChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func semverIsBadNum(v string) bool {
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	return i == len(v) && i > 1 && v[0] == '0'
}

func semverIsNum(v string) bool {
	for _, c := range []byte(v) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(v) > 0
}

// semverIsValid reports whether v is a valid semver string (with 'v' prefix).
func semverIsValid(v string) bool {
	_, ok := semverParse(v)
	return ok
}

// semverPrerelease returns the prerelease portion of v (e.g. "-rc.1"), or "".
func semverPrerelease(v string) string {
	p, ok := semverParse(v)
	if !ok {
		return ""
	}
	return p.prerelease
}

// semverCompare compares two semver strings: -1 if v < w, 0 if equal, +1 if v > w.
// Invalid versions sort below valid ones; two invalid versions are equal.
func semverCompare(v, w string) int {
	pv, ok1 := semverParse(v)
	pw, ok2 := semverParse(w)
	if !ok1 && !ok2 {
		return 0
	}
	if !ok1 {
		return -1
	}
	if !ok2 {
		return +1
	}
	if c := semverCompareInt(pv.major, pw.major); c != 0 {
		return c
	}
	if c := semverCompareInt(pv.minor, pw.minor); c != 0 {
		return c
	}
	if c := semverCompareInt(pv.patch, pw.patch); c != 0 {
		return c
	}
	return semverComparePrerelease(pv.prerelease, pw.prerelease)
}

func semverCompareInt(x, y string) int {
	if x == y {
		return 0
	}
	if len(x) < len(y) {
		return -1
	}
	if len(x) > len(y) {
		return +1
	}
	if x < y {
		return -1
	}
	return +1
}

func semverComparePrerelease(x, y string) int {
	if x == y {
		return 0
	}
	if x == "" {
		return +1 // no prerelease > prerelease
	}
	if y == "" {
		return -1
	}
	for x != "" && y != "" {
		x = x[1:] // skip leading '-' or '.'
		y = y[1:]
		var dx, dy string
		dx, x = semverNextIdent(x)
		dy, y = semverNextIdent(y)
		if dx != dy {
			ix := semverIsNum(dx)
			iy := semverIsNum(dy)
			if ix != iy {
				if ix {
					return -1 // numeric < alpha
				}
				return +1
			}
			if ix {
				// both numeric: compare by length then lexicographically
				if len(dx) < len(dy) {
					return -1
				}
				if len(dx) > len(dy) {
					return +1
				}
			}
			if dx < dy {
				return -1
			}
			return +1
		}
	}
	if x == "" {
		return -1
	}
	return +1
}

func semverNextIdent(v string) (ident, rest string) {
	i := 0
	for i < len(v) && v[i] != '.' {
		i++
	}
	return v[:i], v[i:]
}

// semverIsPrerelease reports whether v has a pre-release suffix.
func semverIsPrerelease(v string) bool {
	return semverPrerelease(v) != ""
}

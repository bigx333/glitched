package cliupdate

import (
	"cmp"
	"strconv"
	"strings"
)

// version is a semver version: MAJOR.MINOR.PATCH and its prerelease
// identifiers. Build metadata takes no part in precedence, so it isn't
// kept.
type version struct {
	core [3]uint64
	pre  []string
}

// parse reads s as semver, with a leading "v" or none.
func parse(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	s, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && !identifiers(build) {
		return version{}, false
	}
	s, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return version{}, false
		}
		v.core[i] = n
	}
	if !hasPre {
		return v, true
	}
	if !identifiers(pre) {
		return version{}, false
	}
	v.pre = strings.Split(pre, ".")
	for _, id := range v.pre {
		if numeric(id) {
			if _, ok := number(id); !ok {
				return version{}, false
			}
		}
	}
	return v, true
}

// number is a numeric identifier: digits, and no leading zero.
func number(s string) (uint64, bool) {
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// numeric is whether id is all digits, a number by semver's precedence
// however large: one past uint64 is a version Glitched can't order.
func numeric(id string) bool {
	return !strings.ContainsFunc(id, func(r rune) bool { return r < '0' || r > '9' })
}

// identifiers is whether s is dot-separated identifiers, each of ASCII
// letters, digits and hyphens, none empty.
func identifiers(s string) bool {
	for id := range strings.SplitSeq(s, ".") {
		if id == "" || strings.ContainsFunc(id, func(r rune) bool {
			return !('0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || r == '-')
		}) {
			return false
		}
	}
	return true
}

// compare orders a and b by semver precedence: -1, 0 or 1.
func compare(a, b version) int {
	for i := range a.core {
		if c := cmp.Compare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	// A release comes after its prereleases.
	if len(a.pre) == 0 || len(b.pre) == 0 {
		return cmp.Compare(len(b.pre), len(a.pre))
	}
	for i := range min(len(a.pre), len(b.pre)) {
		if c := compareIdentifier(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a.pre), len(b.pre))
}

// compareIdentifier orders two prerelease identifiers: numbers by value
// and before any word, words in ASCII order.
func compareIdentifier(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return cmp.Compare(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// Newer is whether latest is a newer version than installed. ok is false
// when either doesn't parse, and then nothing is known.
func Newer(installed, latest string) (newer, ok bool) {
	i, iok := parse(installed)
	l, lok := parse(latest)
	if !iok || !lok {
		return false, false
	}
	return compare(l, i) > 0, true
}

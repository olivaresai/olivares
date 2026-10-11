// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"fmt"
	"strconv"
	"strings"
)

// Release versions are bare MAJOR.MINOR. Numeric comparison keeps 1.10 newer
// than 1.9; patch numbers, prefixes and suffixes are not release identities.
// Unstamped builds remain unknown on the upgrade path: callers must establish
// IsUnstamped before ordering or evaluating a minimum version.

// Version is a parsed semantic version. Zero value is the lowest possible version.
type Version struct {
	Major, Minor, Patch int
	// Pre is the dot-separated prerelease identifier set ("rc.1" -> ["rc","1"]);
	// empty means a normal (higher-precedence) release.
	Pre []string
	// Raw is the original string, preserved for display/audit.
	Raw string
}

// IsUnstamped reports whether s names a build that carries NO version stamp ("" or
// "dev"). Such a build is UNKNOWN, not "very old": it has no position in the release
// ordering, so anti-rollback and min_version cannot be evaluated against it and must
// refuse rather than compare. This is the single predicate the upgrade path
// shares between its two ways of not knowing — an unstamped build asked about
// itself, and a target binary whose exec-probe could not run — so both reach the
// same refusal instead of two guards that could drift apart.
func IsUnstamped(s string) bool {
	t := strings.TrimPrefix(strings.TrimSpace(s), "v")
	return t == "" || t == "dev"
}

// ParseVersion parses a semantic version. "dev" (or empty) is the zero Version —
// which is a PARSE result, not an ordering claim: see IsUnstamped and the header.
func ParseVersion(s string) (Version, error) {
	if IsUnstamped(s) {
		return Version{Raw: s}, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return Version{}, fmt.Errorf("release: version %q must be MAJOR.MINOR", s)
	}
	nums := [2]int{}
	for i, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return Version{}, fmt.Errorf("release: version %q has a non-numeric component %q", s, part)
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return Version{}, fmt.Errorf("release: version %q: %w", s, err)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Raw: s}, nil
}

// Compare returns -1 if a<b, 0 if equal (in precedence), +1 if a>b. It follows
// SemVer precedence: numeric core first, then a version WITH a prerelease is LOWER
// than the same core without one; prerelease identifiers compare field-by-field
// (numeric fields numerically, alphanumeric lexically; numeric < alphanumeric;
// a shorter prerelease prefix is lower when all preceding fields are equal).
func Compare(a, b Version) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	// Equal core: no-prerelease outranks any prerelease.
	if len(a.Pre) == 0 && len(b.Pre) == 0 {
		return 0
	}
	if len(a.Pre) == 0 {
		return 1
	}
	if len(b.Pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := cmpPreField(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Pre), len(b.Pre)) // the longer set is higher when a prefix
}

// Newer reports whether target has strictly higher precedence than current.
//
// It answers only the PRECEDENCE question and knows nothing about stamping: called with
// the zero Version an unstamped build parses to, it will happily report that every real
// release is newer. That is the reading which, applied to the min-version gate, inverted
// its meaning. On the upgrade path, establish IsUnstamped first; this is the
// comparison you may run once you know you have two positions to compare.
func (current Version) Newer(target Version) bool { return Compare(target, current) > 0 }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// cmpPreField compares one prerelease identifier: both-numeric compare numerically,
// numeric is lower than alphanumeric, otherwise ASCII lexical.
func cmpPreField(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return cmpInt(an, bn)
	case aErr == nil: // a numeric, b not -> a lower
		return -1
	case bErr == nil: // b numeric, a not -> a higher
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

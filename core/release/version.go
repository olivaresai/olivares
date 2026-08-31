// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"fmt"
	"strconv"
	"strings"
)

// version.go is a small, dependency-free semantic-version comparator for the OTA
// updater. It is on the anti-rollback trust path — "is this manifest newer
// than the binary I am running?" decides whether an upgrade is a legitimate
// forward step or a downgrade that must be an explicit, audited --force-rollback —
// so it is deliberately self-contained (no x/mod/semver): fewer moving parts on a
// security-relevant decision, and it accepts the exact shape our releases produce.
//
// Accepted forms: "MAJOR.MINOR.PATCH" with an optional leading "v" and an optional
// "-prerelease" suffix (e.g. "26.7.0", "v26.7.1", "26.8.0-rc.1"). Build metadata
// ("+meta") is ignored for ordering, per SemVer. A version WITH a prerelease sorts
// BEFORE the same version without one (26.8.0-rc.1 < 26.8.0).
//
// UNSTAMPED BUILDS ARE NOT ORDERABLE. "" and "dev" still parse to the zero
// Version so nothing panics, but that zero is NOT a position in the ordering and no
// guard may read it as one. This paragraph used to say the opposite — "any real
// release is newer than a dev binary, so a dev build can always take a signed
// upgrade" — and that sentence was the SEED of a real defect, not a victim of it:
// it reasoned about anti-rollback (where zero does mean "everything is forward")
// and forgot that min_version reads the SAME datum, where zero means the exact
// opposite. MinTooOld is Compare(current, min) < 0, so a zero current is BELOW every
// minimum: the build the comment promised could "always upgrade" was in fact the one
// build that could never take any release declaring a min_version. One datum feeding
// two guards of opposite intent is how a fail-open and a fail-closed bug ship
// together. Callers on the upgrade path therefore ask IsUnstamped FIRST and refuse
// with a named cause and a named way out (--current-version), rather than compare.
// Released artifacts are unaffected: goreleaser stamps -X main.version
// (.goreleaser.yaml:102 and :151), so only a build from source is unstamped.

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
	raw := strings.TrimSpace(s)
	v := Version{Raw: raw}
	t := strings.TrimPrefix(raw, "v")
	if t == "" || t == "dev" {
		// The zero Version, so callers that only display or store it keep working. It is
		// NOT a position in the ordering — ask IsUnstamped before comparing.
		return Version{Raw: raw}, nil
	}
	// Drop build metadata (+...) which does not affect ordering.
	if i := strings.IndexByte(t, '+'); i >= 0 {
		t = t[:i]
	}
	// Split off the prerelease (-...).
	core := t
	if i := strings.IndexByte(t, '-'); i >= 0 {
		core = t[:i]
		pre := t[i+1:]
		if pre == "" {
			return Version{}, fmt.Errorf("release: version %q has an empty prerelease", raw)
		}
		v.Pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("release: version %q is not MAJOR.MINOR.PATCH", raw)
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("release: version %q has a non-numeric component %q", raw, p)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
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

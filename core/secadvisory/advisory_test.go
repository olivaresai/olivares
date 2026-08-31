// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secadvisory

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/sigbundle"
)

const productModule = "github.com/olivaresai/olivares/cmd/olivares"

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func advisory(id, introduced, fixed string) Advisory {
	return Advisory{
		SchemaVersion: "1.6.0",
		ID:            id,
		Modified:      "2026-07-09T00:00:00Z",
		Summary:       "test advisory " + id,
		Severity:      []Severity{{Type: SeverityCVSS3, Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
		Affected: []Affected{{
			Package: Package{Ecosystem: EcosystemGo, Name: productModule},
			Ranges:  []Range{{Type: RangeSemver, Events: []Event{{Introduced: introduced}, {Fixed: fixed}}}},
		}},
		References: []Ref{{Type: "ADVISORY", URL: "https://example.test/" + id}},
	}
}

func feed(t *testing.T, advs ...Advisory) Feed {
	t.Helper()
	return NewFeed("security@olivares.ai", time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC), advs)
}

// TestSignVerifyRoundTrip: a signed feed verifies with the same key.
func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	fb, sig, err := feed(t, advisory("GHSA-aaaa-bbbb-cccc", "0", "26.7.1")).Sign(priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := VerifyFeed(fb, sig, pub)
	if err != nil {
		t.Fatalf("VerifyFeed: %v", err)
	}
	if len(got.Advisories) != 1 || got.Advisories[0].ID != "GHSA-aaaa-bbbb-cccc" {
		t.Fatalf("round-trip lost the advisory: %+v", got)
	}
}

// TestTamperedFeedRejected is a DoD item: a tampered advisories feed must be refused.
func TestTamperedFeedRejected(t *testing.T) {
	pub, priv := testKey(t)
	fb, sig, err := feed(t, advisory("GHSA-x", "0", "26.7.1")).Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), fb...)
	// Flip a byte in the middle of the JSON.
	tampered[len(tampered)/2] ^= 0xff
	if _, err := VerifyFeed(tampered, sig, pub); err == nil {
		t.Fatal("a tampered feed verified clean")
	}
}

// TestWrongDomainRejected: an advisories signature must not be forgeable from another
// domain (the sigbundle domain-tag guarantee, exercised through this API).
func TestWrongKeyRejected(t *testing.T) {
	_, priv := testKey(t)
	otherPub, _ := testKey(t)
	fb, sig, _ := feed(t, advisory("GHSA-x", "0", "26.7.1")).Sign(priv)
	if _, err := VerifyFeed(fb, sig, otherPub); err == nil {
		t.Fatal("feed verified under the wrong key")
	}
}

func TestNilKeyFailsClosed(t *testing.T) {
	_, priv := testKey(t)
	fb, sig, _ := feed(t, advisory("GHSA-x", "0", "26.7.1")).Sign(priv)
	if _, err := VerifyFeed(fb, sig, nil); err != sigbundle.ErrNoKey {
		t.Fatalf("nil key: err=%v, want ErrNoKey", err)
	}
}

// TestCheckAffectedAndNot: the self-check core. An older version is affected; the fixed
// version and newer are not.
func TestCheckAffectedAndNot(t *testing.T) {
	f := feed(t, advisory("GHSA-old", "0", "26.7.1"))

	cases := []struct {
		version string
		want    bool
	}{
		{"26.6.0", true},  // before fixed
		{"26.7.0", true},  // before fixed
		{"26.7.1", false}, // exactly fixed -> safe
		{"26.8.0", false}, // after fixed
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			got, err := f.Check(productModule, tc.version)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			affected := len(got.Findings) > 0
			if affected != tc.want {
				t.Fatalf("version %s affected=%v, want %v (findings=%+v)", tc.version, affected, tc.want, got)
			}
			if affected {
				if got.Findings[0].FixedIn != "26.7.1" {
					t.Errorf("FixedIn = %q, want 26.7.1", got.Findings[0].FixedIn)
				}
				if got.Findings[0].Severity == "" {
					t.Errorf("Severity not surfaced")
				}
			}
		})
	}
}

// TestCheckIntroducedRange: an advisory introduced at a specific version does not affect
// earlier versions.
func TestCheckIntroducedRange(t *testing.T) {
	f := feed(t, advisory("GHSA-mid", "26.7.0", "26.7.3"))
	for _, v := range []string{"26.6.9"} {
		got, _ := f.Check(productModule, v)
		if len(got.Findings) != 0 {
			t.Errorf("version %s should be before the introduced range, got affected", v)
		}
	}
	for _, v := range []string{"26.7.0", "26.7.2"} {
		got, _ := f.Check(productModule, v)
		if len(got.Findings) != 1 {
			t.Errorf("version %s should be inside [26.7.0, 26.7.3), got %d findings", v, len(got.Findings))
		}
	}
}

// TestCheckOtherModuleIgnored: an advisory for a different module never affects us.
func TestCheckOtherModuleIgnored(t *testing.T) {
	a := advisory("GHSA-other", "0", "99.0.0")
	a.Affected[0].Package.Name = "github.com/some/other"
	f := feed(t, a)
	got, _ := f.Check(productModule, "26.7.0")
	if len(got.Findings) != 0 {
		t.Fatalf("an advisory for another module affected us: %+v", got)
	}
	// It must not land in Unevaluable either: an advisory about SOMEONE ELSE'S module is
	// not an open question about ours, and counting it as one would make every mixed feed
	// indeterminate forever.
	if len(got.Unevaluable) != 0 {
		t.Fatalf("an advisory for another module was recorded as unevaluable for us: %+v", got.Unevaluable)
	}
}

// TestCheckMultipleFindingsSorted: several matching advisories come back sorted by id.
func TestCheckMultipleFindingsSorted(t *testing.T) {
	f := feed(t, advisory("GHSA-zzzz", "0", "26.8.0"), advisory("GHSA-aaaa", "0", "26.8.0"))
	got, _ := f.Check(productModule, "26.7.0")
	if len(got.Findings) != 2 || got.Findings[0].ID != "GHSA-aaaa" || got.Findings[1].ID != "GHSA-zzzz" {
		t.Fatalf("findings not sorted by id: %+v", got)
	}
}

// TestCheckOpenRange: an advisory with no fixed event affects every version at/after
// introduced (a not-yet-patched vulnerability).
func TestCheckOpenRange(t *testing.T) {
	a := Advisory{
		SchemaVersion: "1.6.0", ID: "GHSA-open", Modified: "2026-07-09T00:00:00Z",
		Affected: []Affected{{
			Package: Package{Ecosystem: EcosystemGo, Name: productModule},
			Ranges:  []Range{{Type: RangeSemver, Events: []Event{{Introduced: "0"}}}},
		}},
	}
	f := feed(t, a)
	got, _ := f.Check(productModule, "26.7.0")
	if len(got.Findings) != 1 {
		t.Fatalf("open range should affect us with no fix yet, got %d", len(got.Findings))
	}
	if got.Findings[0].FixedIn != "" {
		t.Errorf("FixedIn = %q, want empty (no fix published)", got.Findings[0].FixedIn)
	}
}

// --- an unstamped build is UNKNOWN, never a verdict --------------------

// TestCheckRefusesUnstampedBuild is the root-cause pin. A build that carries
// no version stamp ("dev", "", "v") has NO position in the release ordering, so no OSV
// range can be evaluated against it. Before the zero Version silently played the
// part of a real one and Check returned a VERDICT — which one depended on the CATALOG,
// not on the binary: "introduced":"0" made it affected, a real "introduced" made it
// clean. Both are fabrications. Check must refuse instead, so no caller (CLI, console,
// or a future one) can mint an answer nobody measured.
func TestCheckRefusesUnstampedBuild(t *testing.T) {
	for _, v := range []string{"dev", "", "  ", "v", "vdev"} {
		t.Run("version="+v, func(t *testing.T) {
			// The "introduced":"0" catalog — the shape that fabricated AFFECTED.
			zero := feed(t, advisory("GHSA-zero", "0", "26.7.2"))
			got, err := zero.Check(productModule, v)
			if err == nil {
				t.Fatalf("unstamped %q got a verdict (%d finding(s)) instead of a refusal", v, len(got.Findings))
			}
			if !errors.Is(err, ErrVersionNotCheckable) {
				t.Errorf("error does not carry ErrVersionNotCheckable: %v", err)
			}
			if len(got.Findings) != 0 || len(got.Unevaluable) != 0 {
				t.Errorf("a refusal must carry NO findings, got %+v", got)
			}

			// The real-"introduced" catalog — the shape that fabricated CLEAN, and the
			// one an operator reads as "I am safe". Same refusal, same reason.
			real := feed(t, advisory("GHSA-real", "26.5.0", "26.7.2"))
			got, err = real.Check(productModule, v)
			if err == nil {
				t.Fatalf("unstamped %q got a CLEAN verdict (%d finding(s)); that is the fabricated verdict", v, len(got.Findings))
			}
			if !errors.Is(err, ErrVersionNotCheckable) {
				t.Errorf("error does not carry ErrVersionNotCheckable: %v", err)
			}
		})
	}
}

// TestCheckRefusesUnorderableVersion: a stamp that is not a semantic version (the shape
// `task build:bin` produces when `git describe --tags --always` finds no reachable tag —
// a bare commit SHA) is exactly as unorderable as "dev". It already failed rather than
// fabricating, but it failed as a generic parse error; it now carries the same typed
// refusal, so ONE caller-side branch covers every "I cannot evaluate this".
func TestCheckRefusesUnorderableVersion(t *testing.T) {
	f := feed(t, advisory("GHSA-zero", "0", "26.7.2"))
	for _, v := range []string{"15f2fb57a", "26.7", "banana", "26.7.0.1"} {
		got, err := f.Check(productModule, v)
		if err == nil {
			t.Fatalf("%q is not orderable but Check returned a verdict: %+v", v, got)
		}
		if !errors.Is(err, ErrVersionNotCheckable) {
			t.Errorf("%q: error does not carry ErrVersionNotCheckable: %v", v, err)
		}
	}
}

// TestCheckStillAnswersForAStampedVersion is the counterweight: the refusal must not
// swallow the real answers. A stamped, orderable version keeps getting a MEASURED
// verdict in both directions — this is the test a guard that over-fires dies on.
func TestCheckStillAnswersForAStampedVersion(t *testing.T) {
	f := feed(t, advisory("GHSA-zero", "0", "26.7.2"))
	got, err := f.Check(productModule, "26.7.0")
	if err != nil {
		t.Fatalf("26.7.0 is a real version and must be checkable, got: %v", err)
	}
	if len(got.Findings) != 1 || got.Findings[0].ID != "GHSA-zero" {
		t.Fatalf("26.7.0 is inside [0,26.7.2) and must be reported affected, got %+v", got)
	}
	// And the clean direction, so "always refuse" and "always affected" both die here.
	safe := feed(t, advisory("GHSA-old", "0", "26.6.0"))
	got, err = safe.Check(productModule, "26.7.0")
	if err != nil {
		t.Fatalf("26.7.0 past the fix must still be checkable, got: %v", err)
	}
	if len(got.Findings) != 0 {
		t.Fatalf("26.7.0 is past the 26.6.0 fix and must be clean, got %+v", got)
	}
}

// TestUnparseableRangeIsUnevaluableNotClean is the second half of the class, found
// by sweeping this package rather than by the brief. An advisory ABOUT this product whose
// SEMVER range carries a version we cannot order used to be dropped on the floor and
// counted as "does not match" — so a SIGNED, VERIFIED feed produced "no known advisory
// affects this version" while an advisory in it had never been read. The old comment on
// inRange claimed the caller compensated by logging; no caller ever did.
func TestUnparseableRangeIsUnevaluableNotClean(t *testing.T) {
	for _, bad := range []struct{ introduced, fixed string }{
		{"26.5", "26.7.2"},    // introduced is not MAJOR.MINOR.PATCH
		{"0", "twenty-six"},   // fixed is not a version
		{"v26.5.x", "26.9.0"}, // introduced has a non-numeric component
	} {
		f := feed(t, advisory("GHSA-bad-range", bad.introduced, bad.fixed))
		got, err := f.Check(productModule, "26.7.0")
		if err != nil {
			t.Fatalf("a malformed range is a per-advisory problem, not a whole-check failure: %v", err)
		}
		if len(got.Findings) != 0 {
			t.Errorf("{%s,%s}: an unreadable range must not manufacture a finding either: %+v", bad.introduced, bad.fixed, got.Findings)
		}
		if got.Determined() {
			t.Fatalf("{%s,%s}: report claims a complete answer while an advisory went unread", bad.introduced, bad.fixed)
		}
		if len(got.Unevaluable) != 1 || got.Unevaluable[0].ID != "GHSA-bad-range" {
			t.Fatalf("{%s,%s}: unevaluable advisory not named: %+v", bad.introduced, bad.fixed, got.Unevaluable)
		}
		if got.Unevaluable[0].Reason == "" {
			t.Errorf("{%s,%s}: unevaluable advisory carries no reason", bad.introduced, bad.fixed)
		}
	}
}

// TestUnknownRangeTypeIsUnevaluable: OSV also defines ECOSYSTEM and GIT ranges. This build
// orders SEMVER only, so any other type is UNREAD, not "does not apply" — the same
// distinction, reached by a different door.
func TestUnknownRangeTypeIsUnevaluable(t *testing.T) {
	a := advisory("GHSA-git-range", "0", "26.7.2")
	a.Affected[0].Ranges[0].Type = "GIT"
	got, err := feed(t, a).Check(productModule, "26.7.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got.Determined() || len(got.Unevaluable) != 1 {
		t.Fatalf("a GIT range was silently treated as not-applicable: %+v", got)
	}
}

// TestDefiniteMatchOutranksAnUnevaluableSibling: when one range says "affected" and
// another is unreadable, the answer for that advisory is KNOWN and it is the urgent one.
// It must be a finding, not an abstention — an abstention here would bury a confirmed hit.
func TestDefiniteMatchOutranksAnUnevaluableSibling(t *testing.T) {
	a := advisory("GHSA-mixed", "0", "26.7.2") // this range matches 26.7.0
	a.Affected[0].Ranges = append(a.Affected[0].Ranges, Range{
		Type: RangeSemver, Events: []Event{{Introduced: "not-a-version"}},
	})
	got, err := feed(t, a).Check(productModule, "26.7.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got.Findings) != 1 || got.Findings[0].ID != "GHSA-mixed" {
		t.Fatalf("a confirmed hit was lost behind an unreadable sibling range: %+v", got)
	}
	if len(got.Unevaluable) != 0 {
		t.Errorf("the advisory was decided, so it must not ALSO be reported unread: %+v", got.Unevaluable)
	}
}

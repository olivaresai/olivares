// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secadvisory"
)

// `security check` used to certify "no known advisory affects this version" for a
// binary whose version was never stamped. That was not a measurement — it was an artifact
// of release.ParseVersion("dev") yielding the ZERO version, which sits below every real
// "introduced". The verdict therefore flipped on the CATALOG, not on the binary: an
// advisory with "introduced":"0" reported AFFECTED, one with a real "introduced" reported
// CLEAN. The tests below pin the abstention that replaced it, and they are deliberately
// SEPARATE assertions per wrong outcome, so a regression names which wrong thing came
// back rather than only that something did.

// writeSignedFeedRange signs a one-advisory feed over an explicit [introduced, fixed)
// range — the parameter the fabricated verdict hinged on — and returns the feed path plus
// the base64 public key. (writeSignedFeed in cmd_security_test.go hardcodes "introduced":"0",
// which is exactly the half of the defect that reported AFFECTED.)
func writeSignedFeedRange(t *testing.T, dir, id, introduced, fixed string) (feedPath, pubB64 string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	feed := secadvisory.NewFeed("security@olivares.ai", time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), []secadvisory.Advisory{{
		SchemaVersion: "1.6.0", ID: id, Modified: "2026-08-07T00:00:00Z",
		Summary:  "advisory fixture",
		Severity: []secadvisory.Severity{{Type: secadvisory.SeverityCVSS3, Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
		Affected: []secadvisory.Affected{{
			Package: secadvisory.Package{Ecosystem: secadvisory.EcosystemGo, Name: productModule},
			Ranges:  []secadvisory.Range{{Type: secadvisory.RangeSemver, Events: []secadvisory.Event{{Introduced: introduced}, {Fixed: fixed}}}},
		}},
		References: []secadvisory.Ref{{Type: "ADVISORY", URL: "https://example.test/" + id}},
	}})
	fb, sig, err := feed.Sign(priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	feedPath = filepath.Join(dir, "advisories.json")
	if err := os.WriteFile(feedPath, fb, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedPath+".sig", sig, 0o600); err != nil {
		t.Fatal(err)
	}
	return feedPath, base64.StdEncoding.EncodeToString(pub)
}

// runCheckProcess runs `security check` in a REAL subprocess (the TestMain trampoline
// re-execs this binary as the CLI) and returns its output and its true exit code.
//
// Two reasons it must be a subprocess and not an in-process cobra call. First, the exit
// code is the contract a fleet sweep branches on, and only a process has one — asserting
// on a Go error would prove the mapping in main.go by assumption. Second, the test binary
// is itself built WITHOUT -X main.version, so the child is a genuinely unstamped build:
// the defect's own conditions, not a simulation of them.
func runCheckProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	// #nosec G204 -- exe is os.Executable() (this test binary); args are fixed test flags.
	process := exec.Command(exe, append([]string{"security", "check"}, args...)...)
	process.Env = append(os.Environ(), "OLIVARES_CLI_TRAMPOLINE=1")
	out, err := process.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("run %v: %v\n%s", args, err, out)
	return "", -1
}

// TestUnstampedBuildIsNeverReportedClean is death #1: the original P0. An unstamped build
// checked against an advisory with a REAL "introduced" used to print "no known advisory
// affects this version" and exit 0 — the sentence an operator pastes into an audit. The
// feed here is the one that produced it (introduced 26.5.0, above the zero version), so a
// mutant that re-fabricates the CLEAN half dies HERE and names that half.
func TestUnstampedBuildIsNeverReportedClean(t *testing.T) {
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-clean", "26.5.0", "26.7.2")
	out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub)

	if strings.Contains(out, "no known advisory affects this version") {
		t.Fatalf("an unstamped build was certified CLEAN — the fabricated verdict is back (exit %d):\n%s", code, out)
	}
	if code == exitcode.OK {
		t.Fatalf("an unstamped build exited 0 (reads as safe to any gate); want %d:\n%s", exitcode.Indeterminate, out)
	}
	if code != exitcode.Indeterminate {
		t.Fatalf("exit code %d, want %d (indeterminate):\n%s", code, exitcode.Indeterminate, out)
	}
}

// TestUnstampedBuildIsNeverReportedAffected is death #2, the OTHER half of the same
// fabrication and the reason the two are separate tests: with "introduced":"0" the very
// same binary and the very same code path used to print AFFECTED and exit 7. Nothing
// about the binary changed between this test and the one above — only the catalog did,
// which is precisely what made the old verdict meaningless. A mutant that lets the zero
// version fall through to the range compare dies HERE naming the AFFECTED half.
func TestUnstampedBuildIsNeverReportedAffected(t *testing.T) {
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-affected", "0", "26.7.2")
	out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub)

	if strings.Contains(out, "AFFECTED") {
		t.Fatalf("an unstamped build was declared AFFECTED by a range it was never compared against (exit %d):\n%s", code, out)
	}
	if code == exitcode.Degraded {
		t.Fatalf("an unstamped build exited %d (affected); a build with no version cannot be affected OR clean:\n%s", exitcode.Degraded, out)
	}
	if code != exitcode.Indeterminate {
		t.Fatalf("exit code %d, want %d (indeterminate):\n%s", code, exitcode.Indeterminate, out)
	}
}

// TestStampedBuildStillGetsARealVerdict is death #3, the counterweight: the abstention
// must not eat the answers it exists to protect. A guard that over-fires — refusing for
// every version instead of only unorderable ones — is just the same silence wearing an
// honest label, so both verdict directions are pinned here.
func TestStampedBuildStillGetsARealVerdict(t *testing.T) {
	t.Run("affected", func(t *testing.T) {
		feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-hit", "0", "26.7.2")
		out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub, "--product-version", "26.7.0")
		if code == exitcode.Indeterminate {
			t.Fatalf("26.7.0 is a real, orderable version and must get a verdict, not an abstention:\n%s", out)
		}
		if code != exitcode.Degraded || !strings.Contains(out, "AFFECTED") {
			t.Fatalf("26.7.0 is inside [0,26.7.2) and must report AFFECTED with exit %d, got %d:\n%s", exitcode.Degraded, code, out)
		}
	})
	t.Run("clean", func(t *testing.T) {
		feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-miss", "0", "26.6.0")
		out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub, "--product-version", "26.7.0")
		if code == exitcode.Indeterminate {
			t.Fatalf("26.7.0 is a real, orderable version and must get a verdict, not an abstention:\n%s", out)
		}
		if code != exitcode.OK || !strings.Contains(out, "no known advisory affects this version") {
			t.Fatalf("26.7.0 is past the 26.6.0 fix and must report CLEAN with exit 0, got %d:\n%s", code, out)
		}
	})
}

// TestAbstentionNamesItsCauseAndItsWayOut: an abstention that does not say what to do is
// a dead end an operator routes around — most likely by ignoring the command. It has to
// carry the cause and the escape, and the escape has to be the flag this command actually
// has (--product-version; there is no --current-version here, whatever the upgrade path
// calls its own).
func TestAbstentionNamesItsCauseAndItsWayOut(t *testing.T) {
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-msg", "26.5.0", "26.7.2")
	out, _ := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub)

	for _, want := range []string{
		"CANNOT DETERMINE",              // the verdict-shaped headline, greppable
		"declares no version",           // the cause
		"--product-version",             // the way out
		"the feed itself verified fine", // NOT a feed problem: don't send them key-hunting
	} {
		if !strings.Contains(out, want) {
			t.Errorf("abstention does not carry %q:\n%s", want, out)
		}
	}
	// The flag named as the way out must exist, or the message sends the operator into a
	// usage error. Ask cobra, so a rename cannot leave this passing on a stale string.
	if newSecurityCheckCmd().Flags().Lookup("product-version") == nil {
		t.Error("the abstention points at --product-version, which this command does not define")
	}
}

// TestQuietCannotSilenceTheAbstention: --quiet means "say nothing when UNAFFECTED", and an
// abstention is not that. This is the sharp edge of the whole defect — `security check
// --quiet` on an unstamped build in a cron used to print NOTHING and exit 0, the purest
// form of "silence reads as clean". Exit 8 alone is not enough: whoever reads the log has
// to find a sentence there.
func TestQuietCannotSilenceTheAbstention(t *testing.T) {
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-quiet", "26.5.0", "26.7.2")
	out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub, "--quiet")

	if code != exitcode.Indeterminate {
		t.Fatalf("--quiet changed the exit code to %d, want %d:\n%s", code, exitcode.Indeterminate, out)
	}
	if !strings.Contains(out, "CANNOT DETERMINE") {
		t.Fatalf("--quiet silenced the abstention; an empty log reads as clean:\n%s", out)
	}
	// And --quiet must still do its actual job on a real, unaffected version.
	safe, safePub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-quiet-ok", "0", "26.6.0")
	out, code = runCheckProcess(t, "--feed", safe, "--pubkey", safePub, "--product-version", "26.7.0", "--quiet")
	if code != exitcode.OK || strings.TrimSpace(out) != "" {
		t.Fatalf("--quiet on an unaffected real version should print nothing and exit 0, got %d:\n%s", code, out)
	}
}

// TestNonSemverStampAbstainsButATypedOneIsAUsageError separates two things that both used
// to exit 1 with a message about semver. `task build:bin` stamps -X main.version with
// `git describe --tags --always`, which yields a bare COMMIT SHA when no tag is reachable
// — a build that cannot be checked, exactly like "dev", so it abstains with exit 8. A
// non-version the operator TYPED is a different thing: a malformed question, which is
// what exit 2 has always meant. Collapsing them would make a fleet sweep read an operator
// typo as an uncheckable build.
func TestNonSemverStampAbstainsButATypedOneIsAUsageError(t *testing.T) {
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-sha", "0", "26.7.2")
	out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub, "--product-version", "banana")
	if code != exitcode.Usage {
		t.Errorf("a typed non-version should be a usage error (exit %d), got %d:\n%s", exitcode.Usage, code, out)
	}

	// The stamp path needs the stamp itself, which a subprocess cannot be given without
	// relinking; drive the command in-process with the package version var instead — the
	// same var the RunE reads when --product-version is absent.
	prev := version
	version = "15f2fb57a" // measured: `git describe --tags --always` with no reachable tag
	t.Cleanup(func() { version = prev })

	gotOut, err := runSecurityCheck(t, "--feed", feedPath, "--pubkey", pub)
	if err == nil {
		t.Fatalf("a SHA-stamped build got a verdict instead of an abstention:\n%s", gotOut)
	}
	if got := exitcode.From(err); got != exitcode.Indeterminate {
		t.Errorf("SHA-stamped build exit code %d, want %d (indeterminate):\n%s", got, exitcode.Indeterminate, gotOut)
	}
	if !strings.Contains(gotOut, "CANNOT DETERMINE") || !strings.Contains(gotOut, "is not a semantic version") {
		t.Errorf("abstention does not name the SHA stamp as the cause:\n%s", gotOut)
	}
}

// TestUnreadableAdvisoryBlocksACleanVerdict is the second half of the class, found by
// sweeping core/secadvisory rather than from the brief: an advisory ABOUT this product
// whose range this build cannot order used to be dropped silently, so a SIGNED, VERIFIED
// feed produced "no known advisory affects this version" with exit 0 while an advisory in
// it had never been read. Same lie as the unstamped build, one level down — the command
// reporting a measurement it did not make.
func TestUnreadableAdvisoryBlocksACleanVerdict(t *testing.T) {
	// "26.5" is not MAJOR.MINOR.PATCH, so the range cannot be ordered.
	feedPath, pub := writeSignedFeedRange(t, t.TempDir(), "GHSA-fixture-unreadable", "26.5", "26.7.2")
	out, code := runCheckProcess(t, "--feed", feedPath, "--pubkey", pub, "--product-version", "26.7.0")

	if strings.Contains(out, "no known advisory affects this version") {
		t.Fatalf("an advisory that was never read was counted as checked (exit %d):\n%s", code, out)
	}
	if code != exitcode.Indeterminate {
		t.Fatalf("exit code %d, want %d (indeterminate):\n%s", code, exitcode.Indeterminate, out)
	}
	for _, want := range []string{"CANNOT DETERMINE", "GHSA-fixture-unreadable", "not a version this build can order", "signature verified"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not carry %q:\n%s", want, out)
		}
	}
}

// TestUnreadableAdvisoryDoesNotHideAConfirmedHit: when something DOES match, the finding
// is the headline and still exits 7 — an abstention there would bury a confirmed
// vulnerability behind a catalog defect. The incompleteness is a caveat, not a verdict.
func TestUnreadableAdvisoryDoesNotHideAConfirmedHit(t *testing.T) {
	dir := t.TempDir()
	hit := writeMixedFeed(t, dir)
	out, code := runCheckProcess(t, "--feed", hit.path, "--pubkey", hit.pub, "--product-version", "26.7.0")

	if code != exitcode.Degraded {
		t.Fatalf("a confirmed hit must still exit %d, got %d:\n%s", exitcode.Degraded, code, out)
	}
	if !strings.Contains(out, "AFFECTED") || !strings.Contains(out, "GHSA-fixture-hit-real") {
		t.Fatalf("the confirmed finding was lost:\n%s", out)
	}
	if !strings.Contains(out, "may be incomplete") || !strings.Contains(out, "GHSA-fixture-unread") {
		t.Errorf("the unread advisory was not disclosed alongside the finding:\n%s", out)
	}
}

type mixedFeed struct{ path, pub string }

// writeMixedFeed signs a feed with one advisory that definitely matches 26.7.0 and one
// whose range cannot be ordered — the case where the two answers must coexist.
func writeMixedFeed(t *testing.T, dir string) mixedFeed {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	mk := func(id, introduced, fixed string) secadvisory.Advisory {
		return secadvisory.Advisory{
			SchemaVersion: "1.6.0", ID: id, Modified: "2026-08-07T00:00:00Z", Summary: "advisory fixture",
			Affected: []secadvisory.Affected{{
				Package: secadvisory.Package{Ecosystem: secadvisory.EcosystemGo, Name: productModule},
				Ranges:  []secadvisory.Range{{Type: secadvisory.RangeSemver, Events: []secadvisory.Event{{Introduced: introduced}, {Fixed: fixed}}}},
			}},
		}
	}
	feed := secadvisory.NewFeed("security@olivares.ai", time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		[]secadvisory.Advisory{mk("GHSA-fixture-hit-real", "0", "26.7.2"), mk("GHSA-fixture-unread", "26.5", "26.9.0")})
	fb, sig, err := feed.Sign(priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	p := filepath.Join(dir, "mixed.json")
	if err := os.WriteFile(p, fb, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p+".sig", sig, 0o600); err != nil {
		t.Fatal(err)
	}
	return mixedFeed{path: p, pub: base64.StdEncoding.EncodeToString(pub)}
}

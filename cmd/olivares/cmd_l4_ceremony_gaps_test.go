// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/release"
)

// VER-06 lot L4 — the four places the lot's own witnesses left open, each one
// found by a mutant that survived the whole L4 suite AND a broad re-run of every
// Upgrade/Security/Release/RulePack test in this package.
//
// They are together in one file because they are one class: a contract that is
// STATED in a comment and asserted only on the value where it cannot be wrong.
//
//   - the INSTALLED text pane was checked with three `strings.Contains`, so its
//     last two lines and its whole --enterprise arm had no witness at all;
//   - the --quiet exemption was witnessed on ONE of the two indeterminate arms,
//     and both arms carry the same written decision;
//   - `max_freshness_window` was pinned only at the DEFAULT, which is the one
//     value it reports correctly even when it ignores the operator entirely;
//   - `warnings` said it collected EVERY warning and dropped the last one a run
//     can print (fixed in the commit before this file landed).
//
// THE COUNT: SIX mutants, not five. The commit that introduced this file said
// five, and the number was one measurement short of its own claim — the two
// --quiet mutants (silence both panes; silence only the document) are two, and so
// are the two install-pane ones. Both --quiet mutants and the `warnings` one were
// re-measured against the tree WITHOUT this file afterwards, and all three passed
// green there, which is what makes "survived" a reading rather than an inference.
// Six survivors, six kills, each on the assertion that names its guard.

// l4gBackup normalises the rollback path's `<unix>-<random>` suffix, which is the
// only part of the installed pane that cannot be constant between two runs.
var l4gBackup = regexp.MustCompile(`\.bak-\d+-\d+`)

// l4gSHA normalises the 12 hex characters of the artifact digest line. The digest
// is over a gzip stream, so it is a fixture value and not a contract; the LINE is
// the contract, and it is compared raw around this one substitution.
var l4gSHA = regexp.MustCompile(`sha256 [0-9a-f]{12}`)

// TestL4UpgradeInstalledTextPaneIsPinned compares the whole stdout of the one
// desenlace that MUTATES the box to a constant, in both its arms.
//
// WHY A CONSTANT AND NOT `Contains`: the lot's own witness asserts three
// substrings, and two mutants walked straight through it — one deleting the
// `next: restart the service …` pair, one deleting the entire `--enterprise`
// line. Both survived the L4 suite and a broad re-run. Those lines are the whole
// operator instruction after a swap: the first says the new binary is not running
// yet, the second says the add-ons are still off. An upgrade that silently stops
// saying so is exactly the "no cambies el texto" break this lot promised not to
// commit, and `Contains` cannot see a deletion.
//
// The refactor that added the JSON pane is what makes this worth pinning now: the
// three lines moved from a flat sequence of Fprintln into a closure with an
// error-returning branch, so a wrong early return there deletes them silently.
func TestL4UpgradeInstalledTextPaneIsPinned(t *testing.T) {
	run := func(t *testing.T, enterprise bool) (string, string, string, error) {
		t.Helper()
		target := writeTarget(t, l4Script("26.0.0"))
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		b := l4Bundle(t, "26.8.0", nil, l4Script("26.8.0"))
		args := []string{"upgrade", "--bundle", b.dir, "--pubkey", b.pubB64, "--target", target,
			"--data-dir", dataDir, "--os", "linux", "--arch", "amd64", "--yes"}
		if enterprise {
			args = append(args, "--enterprise")
		}
		out, errOut, err := runLeafCLI(t, args...)
		// The premise, asserted rather than assumed: the swap really happened, or the
		// desenlace whose text is pinned below was never reached.
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("the target was not swapped: %s still reports %q", target, got)
		}
		// Order matters: the backup path CONTAINS the target path, so the target is
		// substituted first and the suffix regexp runs on what is left.
		norm := l4Norm(out, [2]string{target, "<TARGET>"}, [2]string{b.dir, "<BUNDLE>"})
		norm = l4gBackup.ReplaceAllString(norm, ".bak-<EPOCH>-<RAND>")
		norm = l4gSHA.ReplaceAllString(norm, "sha256 <SHA12>")
		return norm, errOut, b.artifact, err
	}

	const head = "OTA key: --pubkey (fingerprint <FP>)\n" +
		"source: air-gap bundle <BUNDLE>\n" +
		"channel:   stable\n" +
		"current:   26.0.0\n" +
		"available: 26.8.0 (released <DATE>)\n" +
		"status:    upgrade available\n" +
		"downloading 26.8.0 linux/amd64 ...\n"
	const tail = "\ninstalled: <TARGET> is now olivares 26.8.0 (commit t, built t, L4 witness)\n" +
		"rollback: the previous binary is backed up at <TARGET>.bak-<EPOCH>-<RAND> (restore it to revert)\n" +
		"next: restart the service to run the new binary — for zero downtime use a drain +\n" +
		"      handover (single node) or a rolling restart (HA); see docs/UPGRADE-AND-ROLLBACK.md.\n"

	t.Run("community", func(t *testing.T) {
		got, errOut, artifact, err := run(t, false)
		wantRC(t, "upgrade (installed, text)", err, 0)
		if errOut != "" {
			t.Fatalf("upgrade wrote to stderr on the text pane: %q", errOut)
		}
		wantText(t, "upgrade (installed)", got,
			head+"artifact: "+artifact+" verified (sha256 <SHA12>…)\n"+tail)
	})

	t.Run("--enterprise adds its line and changes nothing else", func(t *testing.T) {
		// The contrafactual for the arm above: same leaf, same swap, one flag. Without
		// it the community constant could pass because the branch never runs, and
		// without the community constant this one could pass with the branch always on.
		got, errOut, artifact, err := run(t, true)
		wantRC(t, "upgrade (installed, --enterprise, text)", err, 0)
		if errOut != "" {
			t.Fatalf("upgrade --enterprise wrote to stderr on the text pane: %q", errOut)
		}
		wantText(t, "upgrade (installed, --enterprise)", got,
			head+"artifact: "+artifact+" verified (sha256 <SHA12>…)\n"+tail+
				"      then `olivares enterprise enable <preset>` to activate the add-ons.\n")
	})
}

// TestL4SecurityCheckQuietDoesNotSilenceTheFeedArm is the missing HALF of the
// --quiet exemption.
//
// `security check` has TWO indeterminate arms and one written decision covering
// both (cmd_security.go:118-127): --quiet means "say nothing when UNAFFECTED",
// and an abstention is not that — an operator who redirects stdout into a ticket
// must not receive an empty file, because an empty file reads as clean. Only the
// UNSTAMPED-BUILD arm had a witness. A mutant that returns early on --quiet in
// the FEED arm — the one an air-gapped fleet actually hits, because it fires on a
// feed carrying a range this build cannot order — passed the whole suite green.
//
// Both panes, because the exemption has to survive the pane that was added: a
// flag honored on one and ignored on the other is worse than either rule.
func TestL4SecurityCheckQuietDoesNotSilenceTheFeedArm(t *testing.T) {
	// "26.5" is not MAJOR.MINOR.PATCH, so the advisory is unevaluable and the
	// catalog is incomplete: no findings, no verdict, exit 8.
	feed, pub := l4SignedFeed(t, l4Advisory("26.5"))
	args := []string{"security", "check", "--feed", feed, "--pubkey", pub,
		"--product-version", "26.6.0", "--quiet"}

	out, _, err := runLeafCLI(t, args...)
	wantRC(t, "security check (quiet+unevaluable, text)", err, 8)
	wantText(t, "security check (quiet+unevaluable)", l4Norm(out),
		"olivares 26.6.0: CANNOT DETERMINE whether any advisory affects this version.\n"+
			"  cause:   1 of the 1 advisory(ies) in this feed could not be evaluated, so\n"+
			"           \"not affected\" would be a claim about advisories this build never\n"+
			"           read:\n"+
			"             - OLIVARES-L4-0001: \"introduced\":\"26.5\" is not a version this build can order: "+
			"release: version \"26.5\" is not MAJOR.MINOR.PATCH\n"+
			"  way out: this is a FEED problem, not a key problem — the signature verified.\n"+
			"           Take it to the advisory publisher, or upgrade to a build that\n"+
			"           understands these ranges.\n")

	jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
	wantRC(t, "security check (quiet+unevaluable, json)", jerr, 8)
	if jout == "" {
		t.Fatal("--quiet silenced the indeterminate verdict on the -o json pane: stdout was empty, " +
			"and an empty document is what a sweep reads as clean")
	}
	doc, keys := leafJSONKeys(t, jout)
	wantKeys(t, "security check (quiet+unevaluable)", keys,
		[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
	wantBool(t, "security check (quiet+unevaluable)", "determined", doc, false)
	wantNull(t, "security check (quiet+unevaluable)", "affected", doc)
	if u, _ := doc["unevaluable"].([]any); len(u) != 1 {
		t.Fatalf("security check --quiet -o json unevaluable = %#v, want the advisory nobody could order — "+
			"it is the CAUSE, and a document without it explains nothing", doc["unevaluable"])
	}

	// The contrafactual, so the assertions above cannot be satisfied by a --quiet
	// that does nothing at all: on the UNAFFECTED desenlace of the same command,
	// --quiet still silences BOTH panes.
	clean, cleanPub := l4SignedFeed(t, l4Advisory("26.5.0"))
	silent, _, serr := runLeafCLI(t, "security", "check", "--feed", clean, "--pubkey", cleanPub,
		"--product-version", "26.7.1", "--quiet")
	wantRC(t, "security check (quiet+clean, text)", serr, 0)
	wantText(t, "security check (quiet+clean)", silent, "")
}

// TestL4VerifyManifestReportsTheBoundItActuallyUsed pins `max_freshness_window`
// at a value the operator CHOSE.
//
// The field's own comment says why it is in the document: "a gate that reported
// the verdict without the bound it used would let a relaxed run read as a strict
// one". The lot asserted it only against release.DefaultMaxFreshnessWindow — the
// one value a hard-wired default still reports correctly — so a mutant that
// replaced the bound actually used with the package default survived green.
//
// Asserted on BOTH panes and in BOTH directions: the default run (witnessed in
// cmd_l4_ceremony_json_test.go) and this overridden one must not report the same
// string, which is what makes either assertion mean anything.
func TestL4VerifyManifestReportsTheBoundItActuallyUsed(t *testing.T) {
	f := l4Manifest(t, "", "")
	// The fixture's freshness window is 2160h, so any bound above it still passes
	// the policy check: this measures what the document REPORTS, not whether a
	// stricter bound refuses.
	const override = "3000h"
	const overrideText = "3000h0m0s"
	if overrideText == release.DefaultMaxFreshnessWindow.String() {
		t.Fatalf("fixture premise broken: the override %s is the default, so this witness measures nothing", overrideText)
	}
	args := []string{"release", "verify-manifest", "--manifest", f.manifest,
		"--checksums", f.checksums, "--max-expires-in", override}

	out, _, err := runLeafCLI(t, args...)
	wantRC(t, "release verify-manifest (--max-expires-in, text)", err, 0)
	// Compared WITHOUT l4Norm's duration substitution on purpose: normalising the
	// duration here would erase the only value under test.
	if !strings.Contains(out, "policy:    within plausibility bounds (max freshness window "+overrideText+")\n") {
		t.Fatalf("the text pane did not report the bound it ran under; stdout was:\n%s", out)
	}
	if strings.Contains(out, release.DefaultMaxFreshnessWindow.String()) {
		t.Fatalf("the text pane reported the DEFAULT bound while running under --max-expires-in %s:\n%s", override, out)
	}

	jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
	wantRC(t, "release verify-manifest (--max-expires-in, json)", jerr, 0)
	doc, _ := leafJSONKeys(t, jout)
	wantString(t, "release verify-manifest (--max-expires-in)", "max_freshness_window", doc, overrideText)
}

// TestL4UpgradeForcedRollbackAuditFailureReachesBothPanes covers the ONE WARNING
// a run can print after its document has been built.
//
// `upgradeResult.Warnings` says it collects every WARNING: line the run printed.
// It did not: the audit line for a forced downgrade is emitted after the swap,
// past the point where the document is assembled, so a `--force-rollback` that
// went through with NO audit record handed a machine consumer `warnings: []` —
// the same answer as a clean run. The record exists to prove the downgrade was
// deliberate; a run that failed to write it is the last run whose document should
// read clean.
//
// Both directions, because a warnings list that is always non-empty proves as
// little as one that is always empty: the audit sink is broken in the first case
// and writable in the second, and only the first may report a warning.
func TestL4UpgradeForcedRollbackAuditFailureReachesBothPanes(t *testing.T) {
	const warning = "rollback done but audit record failed: "

	run := func(t *testing.T, breakAudit bool) (string, string, error) {
		t.Helper()
		target := writeTarget(t, l4Script("26.9.0"))
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		if breakAudit {
			// A DIRECTORY where the append-only log belongs: os.OpenFile refuses it with
			// EISDIR, which is a real filesystem answer and not a stubbed error.
			if err := os.MkdirAll(filepath.Join(dataDir, "upgrade-audit.log"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		b := l4Bundle(t, "25.1.0", nil, l4Script("25.1.0"))
		out, errOut, err := runLeafCLI(t, "-o", "json", "upgrade", "--bundle", b.dir,
			"--pubkey", b.pubB64, "--target", target, "--data-dir", dataDir,
			"--os", "linux", "--arch", "amd64", "--yes", "--force-rollback")
		// The premise, asserted rather than assumed: the DOWNGRADE really happened, or
		// the audit branch under test was never reached.
		if got := runsVersion(t, target); !strings.Contains(got, "25.1.0") {
			t.Fatalf("the target was not rolled back: %s still reports %q", target, got)
		}
		return out, errOut, err
	}

	t.Run("a broken audit sink is reported on BOTH panes", func(t *testing.T) {
		out, errOut, err := run(t, true)
		wantRC(t, "upgrade (forced rollback, audit broken)", err, 0)
		// The stderr line is the pre-existing contract and is not moved by the document.
		if !strings.Contains(errOut, "WARNING: "+warning) {
			t.Fatalf("the audit-failure WARNING left stderr; stderr was:\n%s", errOut)
		}
		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "upgrade (forced rollback)", keys, l4UpgradeKeys)
		wantString(t, "upgrade (forced rollback)", "status", doc, upgradeStatusDowngrade)
		wantString(t, "upgrade (forced rollback)", "action", doc, upgradeActionInstalled)
		warnings, _ := doc["warnings"].([]any)
		found := false
		for _, raw := range warnings {
			if s, _ := raw.(string); strings.HasPrefix(s, warning) {
				found = true
			}
		}
		if !found {
			t.Fatalf("upgrade -o json warnings = %#v, want the audit failure — without it a forced downgrade "+
				"that left NO audit record reports the same warnings list as a clean run", doc["warnings"])
		}
	})

	t.Run("a writable audit sink leaves the list empty", func(t *testing.T) {
		out, errOut, err := run(t, false)
		wantRC(t, "upgrade (forced rollback, audit written)", err, 0)
		if !strings.Contains(errOut, "AUDIT: forced rollback 26.9.0 -> 25.1.0 recorded.") {
			t.Fatalf("the audit record line is missing; stderr was:\n%s", errOut)
		}
		if strings.Contains(errOut, warning) {
			t.Fatalf("a writable sink still reported the audit as failed; stderr was:\n%s", errOut)
		}
		doc, _ := leafJSONKeys(t, out)
		if warnings, _ := doc["warnings"].([]any); len(warnings) != 0 {
			t.Fatalf("upgrade -o json warnings = %#v on a run that warned about nothing", doc["warnings"])
		}
	})
}

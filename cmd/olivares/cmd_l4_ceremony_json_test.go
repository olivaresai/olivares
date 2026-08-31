// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/secadvisory"
)

// VER-06 lot L4 — the PSIRT leaves and the release ceremony.
//
// Every leaf is asserted in BOTH directions, because a JSON pane that quietly
// reshapes the text pane is not an added contract, it is a broken one:
//
//	(a) with -o json the leaf emits ONE parseable document whose top-level key
//	    set is pinned BY NAME, so a renamed, dropped or retyped key fails; and
//	(b) WITHOUT -o the stdout bytes are compared to a constant, character for
//	    character, after the normalisation named in l4Norm and nothing else.
//
// Direction (b) has a second, stronger proof that does not live in Go: the same
// 8 leaves were driven through 29 invocations by a shell harness against a
// binary built at HEAD and against this tree, comparing stdout, stderr AND the
// exit code separately — 93 files, zero differing. These tests are what keeps
// that true from here on; that run is what established it.
//
// The exit codes are asserted too, and on the JSON pane as well. `security
// check` is the only command in the tree with documented 7 (degraded) and 8
// (indeterminate) codes (main.go:96-104), and a pane that changed them would
// break every fleet sweep while every string assertion still passed.

// ---------------------------------------------------------------------------
// normalisation
// ---------------------------------------------------------------------------

var (
	l4RFC3339     = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)
	l4Date        = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	l4GoDuration  = regexp.MustCompile(`\d+h\d+m\d+(\.\d+)?s|\d+m\d+(\.\d+)?s|\d+(\.\d+)?(ns|µs|ms|s)\b`)
	l4Fingerprint = regexp.MustCompile(`fingerprint [0-9a-f]{8}`)
)

// l4Norm replaces the values that CANNOT be constant between two runs of the
// same binary, and NOTHING else — every other byte is compared raw.
//
// The list is short on purpose, and each entry is a value the command reads from
// the clock, the filesystem or a freshly generated key rather than from its own
// logic: an RFC3339 stamp, a bare date, a Go duration (the policy block prints
// "(in 2160h0m0s)", recomputed against now on every run), an OTA key fingerprint
// (the fixture key is generated per test), and the caller-supplied paths passed
// in as subs. Normalising anything else would be normalising away the contract.
func l4Norm(s string, subs ...[2]string) string {
	for _, sub := range subs {
		if sub[0] != "" {
			s = strings.ReplaceAll(s, sub[0], sub[1])
		}
	}
	s = l4Fingerprint.ReplaceAllString(s, "fingerprint <FP>")
	s = l4RFC3339.ReplaceAllString(s, "<TS>")
	s = l4GoDuration.ReplaceAllString(s, "<DUR>")
	s = l4Date.ReplaceAllString(s, "<DATE>")
	return s
}

// wantText compares a normalised stdout to a constant, and prints both when they
// differ so the failure names the moved byte rather than "output changed".
func wantText(t *testing.T, what, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	t.Fatalf("%s: the TEXT pane moved.\n--- got ---\n%s\n--- want ---\n%s", what, got, want)
}

// l4ExitCode is the code the process would exit with for this error, read the way
// runMain() reads it — which is NOT what exitcode.From alone answers, and the
// difference is the whole exit contract of this lot:
//
//   - a nil error is 0, while exitcode.From(nil) is 1 (it cannot tell "classified
//     as the generic failure" from "not classified"); and
//   - errAffected carries NO code of its own. The 7 that `security check` exits on
//     an affected version is applied by main.go:55, which special-cases the
//     sentinel so the process exits quietly instead of printing an empty
//     "Error:". A witness that asked exitcode.From here would have measured 1,
//     and the tempting "fix" would have been to wrap the sentinel in a coded
//     error — changing the exit path of the one command in the tree whose codes
//     are documented at the root.
func l4ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errAffected) {
		return exitcode.Degraded
	}
	return exitcode.From(err)
}

// wantRC asserts the exit code the CLI wrapper would return for this error, so a
// witness cannot pass on an error that classifies differently than the process
// would exit. It is asserted on the JSON pane too: `security check` is the only
// command in the tree with documented 7 and 8, and a pane that moved them would
// break every fleet sweep while every string assertion still passed.
func wantRC(t *testing.T, what string, err error, want int) {
	t.Helper()
	if got := l4ExitCode(err); got != want {
		t.Fatalf("%s: exit code %d, want %d (err=%v)", what, got, want, err)
	}
}

// wantNull asserts a key is present and JSON null. `_, ok := doc[key]` is the
// load-bearing half: a key that VANISHED and a key that is null both decode to a
// nil interface, and only one of them is the contract.
func wantNull(t *testing.T, what, key string, doc map[string]any) {
	t.Helper()
	raw, present := doc[key]
	if !present {
		t.Fatalf("%s -o json %q is ABSENT, want the JSON null — a consumer that probes for the key before reading it "+
			"cannot tell an unanswered check from a tool that forgot to answer", what, key)
	}
	if raw != nil {
		t.Fatalf("%s -o json %q = %#v, want the JSON null. A false here is the sentence withdrawn from the prose: "+
			"\"not affected\" manufactured out of a check that reached no verdict", what, key, raw)
	}
}

// ---------------------------------------------------------------------------
// security check
// ---------------------------------------------------------------------------

// l4Advisory is the witness advisory: it affects this product from 26.5.0 and is
// fixed in 26.7.1, so 26.6.0 is AFFECTED and 26.7.1 is CLEAN off one feed.
func l4Advisory(introduced string) secadvisory.Advisory {
	return secadvisory.Advisory{
		SchemaVersion: "1.6.0",
		ID:            "OLIVARES-L4-0001",
		Modified:      "2026-08-01T00:00:00Z",
		Published:     "2026-08-01T00:00:00Z",
		Summary:       "L4 witness advisory",
		Severity:      []secadvisory.Severity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}},
		Affected: []secadvisory.Affected{{
			Package: secadvisory.Package{Ecosystem: "Go", Name: productModule},
			Ranges: []secadvisory.Range{{Type: "SEMVER", Events: []secadvisory.Event{
				{Introduced: introduced}, {Fixed: "26.7.1"},
			}}},
		}},
		References: []secadvisory.Ref{{Type: "ADVISORY", URL: "https://olivares.ai/psirt/L4-0001"}},
	}
}

// l4SignedFeed writes a signed advisory feed and returns its path and the base64
// public key. The feed is produced by the SAME core/secadvisory writer the
// product's `security advisories` uses, so the witness verifies real bytes.
func l4SignedFeed(t *testing.T, advisories ...secadvisory.Advisory) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate feed key: %v", err)
	}
	feed := secadvisory.NewFeed("psirt@olivares.ai", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), advisories)
	body, sig, err := feed.Sign(priv)
	if err != nil {
		t.Fatalf("sign feed: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "advisories.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", sig, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, base64.StdEncoding.EncodeToString(pub)
}

func TestL4SecurityCheckBothPanes(t *testing.T) {
	feed, pub := l4SignedFeed(t, l4Advisory("26.5.0"))
	unevalFeed, unevalPub := l4SignedFeed(t, l4Advisory("26.5"))

	t.Run("affected exits 7 on both panes", func(t *testing.T) {
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub, "--product-version", "26.6.0"}

		out, errOut, err := runLeafCLI(t, args...)
		wantRC(t, "security check (affected, text)", err, 7)
		if errOut != "" {
			t.Fatalf("security check wrote to stderr on the text pane: %q", errOut)
		}
		wantText(t, "security check (affected)", l4Norm(out, [2]string{feed, "<FEED>"}),
			"olivares 26.6.0 is AFFECTED by 1 advisory(ies):\n"+
				"  - OLIVARES-L4-0001 [CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H]: L4 witness advisory (fixed in 26.7.1)\n"+
				"      https://olivares.ai/psirt/L4-0001\n"+
				"\nRun `olivares upgrade` to move to a patched, signed release.\n")

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (affected, json)", jerr, 7)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "security check", keys,
			[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
		wantString(t, "security check", "version", doc, "26.6.0")
		wantBool(t, "security check", "determined", doc, true)
		wantBool(t, "security check", "affected", doc, true)
		wantString(t, "security check", "cause", doc, "")
		wantNumber(t, "security check", "feed_advisories", doc, 1)
		findings, ok := doc["findings"].([]any)
		if !ok || len(findings) != 1 {
			t.Fatalf("security check -o json findings = %#v, want one entry", doc["findings"])
		}
		f, _ := findings[0].(map[string]any)
		for key, want := range map[string]string{
			"id":        "OLIVARES-L4-0001",
			"summary":   "L4 witness advisory",
			"severity":  "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
			"fixed_in":  "26.7.1",
			"reference": "https://olivares.ai/psirt/L4-0001",
		} {
			if got, _ := f[key].(string); got != want {
				t.Fatalf("security check -o json findings[0].%s = %q, want %q — a script reads these BY NAME", key, got, want)
			}
		}
		if uneval, _ := doc["unevaluable"].([]any); len(uneval) != 0 {
			t.Fatalf("security check -o json unevaluable = %#v, want [] on a fully determined feed", doc["unevaluable"])
		}
	})

	t.Run("clean exits 0 and affected is FALSE, not null", func(t *testing.T) {
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub, "--product-version", "26.7.1"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (clean, text)", err, 0)
		wantText(t, "security check (clean)", l4Norm(out),
			"olivares 26.7.1: no known advisory affects this version (feed verified, 1 advisories).\n")

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (clean, json)", jerr, 0)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "security check", keys,
			[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
		wantBool(t, "security check", "determined", doc, true)
		// The POSITIVE control for the null assertions below: `affected` must be a
		// real false here, so "it is null" cannot pass by the field never being set.
		wantBool(t, "security check", "affected", doc, false)
	})

	t.Run("an unstamped build exits 8 and affected is NULL", func(t *testing.T) {
		// No --product-version: the check falls back to main.version, which in a test
		// binary is the unstamped default — the desenlace.
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (unstamped, text)", err, 8)
		wantText(t, "security check (unstamped)", l4Norm(out, [2]string{feed, "<FEED>"}),
			"olivares "+version+": CANNOT DETERMINE whether any advisory affects this build.\n"+
				"  cause:   this binary declares no version (built from source without -X main.version).\n"+
				"           With no version it has no position in the release ordering, so no\n"+
				"           advisory range can be evaluated against it. Reporting \"not affected\"\n"+
				"           here would be an artifact of comparing against version zero, not a\n"+
				"           measurement.\n"+
				"  way out: name the version to check —\n"+
				"             olivares security check --feed <FEED> --product-version <MAJOR.MINOR.PATCH>\n"+
				"           A released binary carries its own stamp; only a build from source does not.\n"+
				"  the feed itself verified fine (1 advisories) — it is the VERSION that is unknown.\n")

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (unstamped, json)", jerr, 8)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "security check", keys,
			[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
		wantBool(t, "security check", "determined", doc, false)
		wantNull(t, "security check (unstamped)", "affected", doc)
		wantString(t, "security check", "cause", doc,
			"this binary declares no version (built from source without -X main.version)")
		wantNumber(t, "security check", "feed_advisories", doc, 1)
	})

	t.Run("an unevaluable range exits 8 and affected is NULL", func(t *testing.T) {
		args := []string{"security", "check", "--feed", unevalFeed, "--pubkey", unevalPub, "--product-version", "26.6.0"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (unevaluable, text)", err, 8)
		wantText(t, "security check (unevaluable)", l4Norm(out),
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
		wantRC(t, "security check (unevaluable, json)", jerr, 8)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "security check", keys,
			[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
		wantBool(t, "security check", "determined", doc, false)
		wantNull(t, "security check (unevaluable)", "affected", doc)
		// The FEED is the cause here, and `unevaluable` carries it — so `cause` stays
		// empty, and the list must not be.
		wantString(t, "security check", "cause", doc, "")
		uneval, _ := doc["unevaluable"].([]any)
		if len(uneval) != 1 {
			t.Fatalf("security check -o json unevaluable = %#v, want the one advisory nobody could evaluate", doc["unevaluable"])
		}
		row, _ := uneval[0].(map[string]any)
		if got, _ := row["id"].(string); got != "OLIVARES-L4-0001" {
			t.Fatalf("security check -o json unevaluable[0].id = %q, want OLIVARES-L4-0001", got)
		}
		if reason, _ := row["reason"].(string); !strings.Contains(reason, "not MAJOR.MINOR.PATCH") {
			t.Fatalf("security check -o json unevaluable[0].reason = %q, want the ordering failure it names", reason)
		}
	})

	t.Run("affected AND incomplete keeps the verdict and says the list may be short", func(t *testing.T) {
		// The fourth text branch, and the only one where the two lists are BOTH
		// non-empty. It is the interesting case for the three-state: findings present
		// is a verdict (exit 7) even though the catalog was not fully evaluated, so
		// `affected` must be true while `determined` is false. Reading either field
		// alone here gives half the answer, which is why both are in the document.
		both := secadvisory.Advisory{
			SchemaVersion: "1.6.0",
			ID:            "OLIVARES-L4-0002",
			Modified:      "2026-08-02T00:00:00Z",
			Summary:       "L4 witness advisory nobody can order",
			Affected: []secadvisory.Affected{{
				Package: secadvisory.Package{Ecosystem: "Go", Name: productModule},
				Ranges: []secadvisory.Range{{Type: "SEMVER", Events: []secadvisory.Event{
					{Introduced: "26.5"}, {Fixed: "26.7.1"},
				}}},
			}},
		}
		mixedFeed, mixedPub := l4SignedFeed(t, l4Advisory("26.5.0"), both)
		args := []string{"security", "check", "--feed", mixedFeed, "--pubkey", mixedPub, "--product-version", "26.6.0"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (affected+incomplete, text)", err, 7)
		wantText(t, "security check (affected+incomplete)", l4Norm(out),
			"olivares 26.6.0 is AFFECTED by 1 advisory(ies):\n"+
				"  - OLIVARES-L4-0001 [CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H]: L4 witness advisory (fixed in 26.7.1)\n"+
				"      https://olivares.ai/psirt/L4-0001\n"+
				"\n1 further advisory(ies) could not be evaluated, so this list may be incomplete:\n"+
				"  - OLIVARES-L4-0002: \"introduced\":\"26.5\" is not a version this build can order: "+
				"release: version \"26.5\" is not MAJOR.MINOR.PATCH\n"+
				"\nRun `olivares upgrade` to move to a patched, signed release.\n")

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (affected+incomplete, json)", jerr, 7)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "security check", keys,
			[]string{"affected", "cause", "determined", "feed_advisories", "findings", "unevaluable", "version"})
		wantBool(t, "security check (affected+incomplete)", "affected", doc, true)
		wantBool(t, "security check (affected+incomplete)", "determined", doc, false)
		wantNumber(t, "security check (affected+incomplete)", "feed_advisories", doc, 2)
		if f, _ := doc["findings"].([]any); len(f) != 1 {
			t.Fatalf("security check -o json findings = %#v, want the one advisory that WAS evaluable", doc["findings"])
		}
		if u, _ := doc["unevaluable"].([]any); len(u) != 1 {
			t.Fatalf("security check -o json unevaluable = %#v, want the one nobody could order — dropping it would make "+
				"the finding count read as exhaustive", doc["unevaluable"])
		}
	})

	t.Run("--quiet silences BOTH panes when unaffected", func(t *testing.T) {
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub, "--product-version", "26.7.1", "--quiet"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (quiet, text)", err, 0)
		wantText(t, "security check (quiet)", out, "")

		// A flag honored on one pane and ignored on the other is worse than either
		// rule: --quiet means "do not report the unaffected result", not "report it
		// in prose only". Exit 0 with no output is the answer on both panes.
		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (quiet, json)", jerr, 0)
		if jout != "" {
			t.Fatalf("--quiet was ignored on the -o json pane; stdout was:\n%s", jout)
		}
	})

	t.Run("--quiet does NOT silence an indeterminate verdict on either pane", func(t *testing.T) {
		// The exemption the prose documents must survive the added pane: an
		// operator who redirects stdout into a ticket must not receive an empty file,
		// because an empty file reads as "clean".
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub, "--quiet"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "security check (quiet+unstamped, text)", err, 8)
		if !strings.Contains(out, "CANNOT DETERMINE") {
			t.Fatalf("--quiet silenced the indeterminate verdict on the text pane; stdout was %q", out)
		}

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "security check (quiet+unstamped, json)", jerr, 8)
		doc, _ := leafJSONKeys(t, jout)
		wantNull(t, "security check (quiet+unstamped)", "affected", doc)
	})

	t.Run("an explicit -o text is byte-identical to no flag", func(t *testing.T) {
		args := []string{"security", "check", "--feed", feed, "--pubkey", pub, "--product-version", "26.6.0"}
		plain, plainErr, err1 := runLeafCLI(t, args...)
		explicit, explicitErr, err2 := runLeafCLI(t, append([]string{"-o", "text"}, args...)...)
		if plain != explicit || plainErr != explicitErr {
			t.Fatalf("-o text is not the default pane.\n--- default ---\n%s%s\n--- -o text ---\n%s%s",
				plain, plainErr, explicit, explicitErr)
		}
		if l4ExitCode(err1) != l4ExitCode(err2) {
			t.Fatalf("-o text changed the exit code: %d vs %d", l4ExitCode(err1), l4ExitCode(err2))
		}
	})
}

// ---------------------------------------------------------------------------
// security drill
// ---------------------------------------------------------------------------

func TestL4SecurityDrillBothPanes(t *testing.T) {
	t.Setenv("OLIVARES_CLI_TRAMPOLINE", "1")
	wantSteps := []string{"produce", "affected", "patched", "below-introduced", "tamper", "wrong-key"}

	t.Run("the text pane keeps its progress lines on stdout", func(t *testing.T) {
		out, errOut, err := runLeafCLI(t, "security", "drill")
		wantRC(t, "security drill (text)", err, 0)
		if errOut != "" {
			t.Fatalf("security drill wrote to stderr on the text pane: %q", errOut)
		}
		want := ""
		for _, s := range wantSteps {
			want += "ok " + s + " <DUR>\n"
		}
		want += "security drill PASSED — advisory pipeline proven end to end\nmeasured end-to-end time: <DUR>\n"
		wantText(t, "security drill", l4Norm(out), want)
	})

	t.Run("the json pane is ONE document and the narration moves to stderr", func(t *testing.T) {
		out, errOut, err := runLeafCLI(t, "-o", "json", "security", "drill")
		wantRC(t, "security drill (json)", err, 0)

		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "security drill", keys, []string{"artifacts", "duration_ms", "passed", "steps"})
		wantBool(t, "security drill", "passed", doc, true)
		wantString(t, "security drill", "artifacts", doc, "")
		total, ok := doc["duration_ms"].(float64)
		if !ok {
			t.Fatalf("security drill -o json duration_ms = %#v, want a NUMBER", doc["duration_ms"])
		}

		steps, _ := doc["steps"].([]any)
		if len(steps) != len(wantSteps) {
			t.Fatalf("security drill -o json steps = %#v, want the %d pipeline steps", doc["steps"], len(wantSteps))
		}
		sum := 0.0
		for i, raw := range steps {
			row, _ := raw.(map[string]any)
			name, _ := row["name"].(string)
			if name != wantSteps[i] {
				t.Fatalf("security drill -o json steps[%d].name = %q, want %q — the ORDER is the pipeline", i, name, wantSteps[i])
			}
			d, ok := row["duration_ms"].(float64)
			if !ok {
				t.Fatalf("security drill -o json steps[%d].duration_ms = %#v, want a NUMBER: a consumer that compares "+
					"drill times must not have to parse Go's duration spelling", i, row["duration_ms"])
			}
			sum += d
		}
		// The document must report the SAME run the prose measured, not a second
		// timing: the steps are all inside the total, so their sum cannot exceed it
		// (allowing one millisecond of rounding per step, which is the unit).
		if sum > total+float64(len(steps)) {
			t.Fatalf("security drill -o json step durations sum to %v ms but the total is %v ms — the two panes are timing different runs", sum, total)
		}

		// The narration is preserved, on stderr, so a failing drill still hands the
		// operator its transcript while stdout stays parseable.
		for _, s := range wantSteps {
			if !strings.Contains(errOut, "ok "+s+" ") {
				t.Fatalf("the %s step line was LOST under -o json; stderr was:\n%s", s, errOut)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// security advisories / rulepack sign+verify
// ---------------------------------------------------------------------------

// l4SigningKey writes a 32-byte Ed25519 seed for --sign-key and returns the
// @file argument plus the derived base64 public key.
func l4SigningKey(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seed.b64")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "@" + path, base64.StdEncoding.EncodeToString(pub)
}

func TestL4SecurityAdvisoriesBothPanes(t *testing.T) {
	key, expectPub := l4SigningKey(t)
	draft, err := json.Marshal(advisoryDraft{
		Author:     "psirt@olivares.ai",
		Advisories: []secadvisory.Advisory{l4Advisory("26.5.0")},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(in, draft, 0o600); err != nil {
		t.Fatal(err)
	}

	textOut := filepath.Join(dir, "feed-text.json")
	out, _, err := runLeafCLI(t, "security", "advisories", "--in", in, "--out", textOut, "--sign-key", key, "--expect-pubkey", expectPub)
	wantRC(t, "security advisories (text)", err, 0)
	wantText(t, "security advisories", l4Norm(out, [2]string{textOut, "<OUT>"}),
		"wrote <OUT> + <OUT>.sig (1 advisory(ies); verify with the embedded release key)\n")

	jsonOut := filepath.Join(dir, "feed-json.json")
	jout, _, jerr := runLeafCLI(t, "-o", "json", "security", "advisories", "--in", in, "--out", jsonOut, "--sign-key", key, "--expect-pubkey", expectPub)
	wantRC(t, "security advisories (json)", jerr, 0)
	doc, keys := leafJSONKeys(t, jout)
	wantKeys(t, "security advisories", keys, []string{"advisories", "feed", "signature"})
	wantString(t, "security advisories", "feed", doc, jsonOut)
	wantString(t, "security advisories", "signature", doc, jsonOut+".sig")
	wantNumber(t, "security advisories", "advisories", doc, 1)
	// A producer's report is only worth reading if the paths it names EXIST.
	for _, key := range []string{"feed", "signature"} {
		p, _ := doc[key].(string)
		if _, serr := os.Stat(p); serr != nil {
			t.Fatalf("security advisories -o json %q names %s, which is not there: %v", key, p, serr)
		}
	}
}

func TestL4RulePackBothPanes(t *testing.T) {
	key, pub := l4SigningKey(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "rulepack-draft.json")
	if err := os.WriteFile(in, []byte(`{"version":7,"issued_at":"2026-08-01T00:00:00Z",`+
		`"indicators":[{"type":"domain","value":"exfil.example.invalid"}],`+
		`"blocked_mcp":["evil-mcp"],`+
		`"patterns":[{"id":"L4-P1","match":"ignore all previous instructions"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// --- sign -------------------------------------------------------------
	textPack := filepath.Join(dir, "pack-text.json")
	out, _, err := runLeafCLI(t, "security", "rulepack", "sign", "--in", in, "--out", textPack, "--sign-key", key, "--expect-pubkey", pub)
	wantRC(t, "rulepack sign (text)", err, 0)
	wantText(t, "rulepack sign", l4Norm(out, [2]string{textPack, "<OUT>"}),
		"wrote <OUT> + <OUT>.sig (v7: 1 indicators, 1 patterns, 1 blocked MCP)\n")

	jsonPack := filepath.Join(dir, "pack-json.json")
	jout, _, jerr := runLeafCLI(t, "-o", "json", "security", "rulepack", "sign", "--in", in, "--out", jsonPack, "--sign-key", key, "--expect-pubkey", pub)
	wantRC(t, "rulepack sign (json)", jerr, 0)
	signDoc, signKeys := leafJSONKeys(t, jout)
	wantKeys(t, "rulepack sign", signKeys,
		[]string{"blocked_mcp", "indicators", "pack", "patterns", "signature", "version"})
	wantString(t, "rulepack sign", "pack", signDoc, jsonPack)
	wantString(t, "rulepack sign", "signature", signDoc, jsonPack+".sig")
	wantNumber(t, "rulepack sign", "version", signDoc, 7)
	wantNumber(t, "rulepack sign", "indicators", signDoc, 1)
	wantNumber(t, "rulepack sign", "patterns", signDoc, 1)
	wantNumber(t, "rulepack sign", "blocked_mcp", signDoc, 1)

	// --- verify, against the pack the JSON pane just reported --------------
	vout, _, verr := runLeafCLI(t, "security", "rulepack", "verify", "--in", jsonPack, "--pubkey", pub)
	wantRC(t, "rulepack verify (text)", verr, 0)
	wantText(t, "rulepack verify", l4Norm(vout),
		"OK: rule-pack v7 (issued <TS>) — 1 indicators, 1 patterns, 1 blocked MCP\n")

	jvout, _, jverr := runLeafCLI(t, "-o", "json", "security", "rulepack", "verify", "--in", jsonPack, "--pubkey", pub)
	wantRC(t, "rulepack verify (json)", jverr, 0)
	verifyDoc, verifyKeys := leafJSONKeys(t, jvout)
	wantKeys(t, "rulepack verify", verifyKeys,
		[]string{"blocked_mcp", "indicators", "issued_at", "patterns", "version"})
	wantString(t, "rulepack verify", "issued_at", verifyDoc, "2026-08-01T00:00:00Z")

	// THE UNIFORMITY CLAIM, asserted rather than described: the producer and its
	// verifier report the same four facts under the same four names. If either side
	// renames one, a consumer that reads a verify result stops being able to read a
	// sign receipt — which is the mapping this whole unit exists to delete.
	for _, shared := range []string{"version", "indicators", "patterns", "blocked_mcp"} {
		sv, sok := signDoc[shared]
		vv, vok := verifyDoc[shared]
		if !sok || !vok {
			t.Fatalf("rulepack sign and verify disagree about the key %q (sign has it: %t, verify has it: %t)", shared, sok, vok)
		}
		if fmt.Sprint(sv) != fmt.Sprint(vv) {
			t.Fatalf("rulepack sign %q = %v but verify %q = %v for the same pack", shared, sv, shared, vv)
		}
	}
}

// ---------------------------------------------------------------------------
// release sign-manifest / verify-manifest
// ---------------------------------------------------------------------------

type l4ManifestFixture struct {
	dir       string
	manifest  string
	sig       string
	checksums string
	pubB64    string
	signKey   string
	artifacts []string
	sizes     []int
}

// l4Manifest builds a real release directory: two archives, a checksums.txt in
// sha256sum format, and a signed manifest whose bytes parse under the SAME
// validator the client runs. Everything time-dependent is pinned relative to
// now, so the policy bounds are satisfied deterministically.
func l4Manifest(t *testing.T, minVersion, notes string) *l4ManifestFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &l4ManifestFixture{dir: dir, pubB64: base64.StdEncoding.EncodeToString(pub)}

	seedPath := filepath.Join(dir, "ota-seed.b64")
	if err := os.WriteFile(seedPath, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.signKey = "@" + seedPath

	released := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	expires := released.Add(2160 * time.Hour)
	var arts []release.Artifact
	var sums strings.Builder
	for _, plat := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}} {
		name := release.ExpectedArtifactName("26.8.0", plat[0], plat[1], "")
		body := []byte("L4 witness archive for " + plat[0] + "/" + plat[1] + "\n")
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		arts = append(arts, release.Artifact{
			OS: plat[0], Arch: plat[1], Filename: name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
		})
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		f.artifacts = append(f.artifacts, name)
		f.sizes = append(f.sizes, len(body))
	}
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       "26.8.0",
		MinVersion:    minVersion,
		ReleasedAt:    released,
		Expires:       &expires,
		Notes:         notes,
		Artifacts:     arts,
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mb = append(mb, '\n')
	// The premise, asserted rather than assumed: these bytes must parse under the
	// validator the client uses, or every assertion below is about a manifest the
	// product would refuse anyway.
	if _, perr := release.ParseManifest(mb); perr != nil {
		t.Fatalf("fixture premise broken: the witness manifest does not validate: %v", perr)
	}
	f.manifest = filepath.Join(dir, "manifest.json")
	f.sig = f.manifest + ".sig"
	f.checksums = filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(f.manifest, mb, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sig, []byte(base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.checksums, []byte(sums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// l4PolicyRows is the POLICY block both ceremony leaves print, as the twelve rows
// their documents must carry. Pinned by NAME and ORDER: the block exists so a
// custodian reads every field the signature covers, and a field that silently
// stopped being listed is the failure it was built against.
var l4PolicyRows = []string{
	"schema_version", "channel", "version", "released_at", "min_version",
	"expires", "rollout", "security", "advisories", "revoked", "notes", "artifacts",
}

// wantPolicyBlock asserts a document's `policy` array is the whole block, in
// order, and returns the names whose `alert` is true.
func wantPolicyBlock(t *testing.T, what string, doc map[string]any) []string {
	t.Helper()
	rows, ok := doc["policy"].([]any)
	if !ok || len(rows) != len(l4PolicyRows) {
		t.Fatalf("%s -o json policy = %#v, want the %d rows of the review block", what, doc["policy"], len(l4PolicyRows))
	}
	var alerts []string
	for i, raw := range rows {
		row, _ := raw.(map[string]any)
		name, _ := row["name"].(string)
		if name != l4PolicyRows[i] {
			t.Fatalf("%s -o json policy[%d].name = %q, want %q", what, i, name, l4PolicyRows[i])
		}
		if _, isString := row["value"].(string); !isString {
			t.Fatalf("%s -o json policy[%d].value = %#v, want a string", what, i, row["value"])
		}
		alert, isBool := row["alert"].(bool)
		if !isBool {
			t.Fatalf("%s -o json policy[%d].alert = %#v, want a BOOLEAN — this is the `!!` marker, and a wrapper "+
				"refuses on it", what, i, row["alert"])
		}
		if alert {
			alerts = append(alerts, name)
		}
	}
	return alerts
}

func TestL4ReleaseSignManifestBothPanes(t *testing.T) {
	// min_version and notes are set so the block has ALERT rows and CheckPolicy
	// produces a warning: an empty warnings array proves nothing about a field that
	// carries warnings.
	f := l4Manifest(t, "26.0.0", "L4 witness release")
	subs := [][2]string{{f.checksums, "<CHECKSUMS>"}, {f.manifest, "<MANIFEST>"}, {f.dir, "<DIR>"}}

	wantBlock := "\n=== POLICY THE SIGNATURE WILL COVER — READ EVERY LINE BEFORE SIGNING ===\n" +
		"    (these fields are NOT bound by checksums.txt; only your review binds them)\n" +
		"   schema_version: 1\n" +
		"   channel:        stable\n" +
		"   version:        26.8.0\n" +
		"   released_at:    <TS>\n" +
		"!! min_version:    26.0.0  <- deployments BELOW this are refused the upgrade\n" +
		"   expires:        <TS> (in <DUR>)\n" +
		"   rollout:        100% (omitted — the whole fleet)\n" +
		"   security:       false\n" +
		"   advisories:     none\n" +
		"   revoked:        none\n" +
		"!! notes:          \"L4 witness release\"  <- operator-visible free text, verified by NOTHING\n" +
		"   artifacts:      2 — darwin/arm64=" + f.artifacts[0] + ", linux/amd64=" + f.artifacts[1] + "\n" +
		"=======================================================================\n" +
		"WARNING:   notes is operator-visible free text and is verified by NOTHING — read it: \"L4 witness release\"\n" +
		"digests:   all 2 manifest artifact(s) match <CHECKSUMS>\n"

	t.Run("the review block stays on stdout for a reader", func(t *testing.T) {
		sigOut := filepath.Join(t.TempDir(), "text.sig")
		out, _, err := runLeafCLI(t, "release", "sign-manifest", "--manifest", f.manifest,
			"--checksums", f.checksums, "--out", sigOut, "--sign-key", f.signKey)
		wantRC(t, "release sign-manifest (text)", err, 0)
		wantText(t, "release sign-manifest", l4Norm(out, append(subs, [2]string{sigOut, "<SIG>"})...),
			wantBlock+"wrote <SIG> (dedicated OTA key fingerprint <FP>)\n")
	})

	t.Run("the json pane carries the block as data and the block still reaches a reader", func(t *testing.T) {
		sigOut := filepath.Join(t.TempDir(), "json.sig")
		out, errOut, err := runLeafCLI(t, "-o", "json", "release", "sign-manifest", "--manifest", f.manifest,
			"--checksums", f.checksums, "--out", sigOut, "--sign-key", f.signKey)
		wantRC(t, "release sign-manifest (json)", err, 0)

		// -o json must NOT be the quiet way to sign without reviewing: the block is
		// still printed, on stderr, byte for byte.
		wantText(t, "release sign-manifest (stderr narration)", l4Norm(errOut, subs...), wantBlock)

		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "release sign-manifest", keys, []string{
			"artifacts_matched", "channel", "checksums", "cross_checked", "key_fingerprint",
			"manifest", "policy", "signature", "version", "warnings",
		})
		wantString(t, "release sign-manifest", "manifest", doc, f.manifest)
		wantString(t, "release sign-manifest", "signature", doc, sigOut)
		wantString(t, "release sign-manifest", "channel", doc, "stable")
		wantString(t, "release sign-manifest", "version", doc, "26.8.0")
		wantString(t, "release sign-manifest", "checksums", doc, f.checksums)
		wantBool(t, "release sign-manifest", "cross_checked", doc, true)
		wantNumber(t, "release sign-manifest", "artifacts_matched", doc, 2)
		if fp, _ := doc["key_fingerprint"].(string); len(fp) != 8 {
			t.Fatalf("release sign-manifest -o json key_fingerprint = %q, want the 8-char OTA fingerprint", fp)
		}
		alerts := wantPolicyBlock(t, "release sign-manifest", doc)
		if strings.Join(alerts, ",") != "min_version,notes" {
			t.Fatalf("release sign-manifest -o json alert rows = %v, want [min_version notes] — the `!!` markers are "+
				"the whole reason the block is worth having as data", alerts)
		}
		warnings, _ := doc["warnings"].([]any)
		if len(warnings) != 1 || !strings.Contains(fmt.Sprint(warnings[0]), "verified by NOTHING") {
			t.Fatalf("release sign-manifest -o json warnings = %#v, want the free-text warning the prose printed", doc["warnings"])
		}
		if _, serr := os.Stat(sigOut); serr != nil {
			t.Fatalf("release sign-manifest -o json named a signature that is not there: %v", serr)
		}
	})

	t.Run("--unsafe-no-crosscheck reports itself instead of an empty review", func(t *testing.T) {
		sigOut := filepath.Join(t.TempDir(), "unsafe.sig")
		out, _, err := runLeafCLI(t, "-o", "json", "release", "sign-manifest", "--manifest", f.manifest,
			"--unsafe-no-crosscheck", "--out", sigOut, "--sign-key", f.signKey)
		wantRC(t, "release sign-manifest (unsafe, json)", err, 0)
		doc, keys := leafJSONKeys(t, out)
		// SAME key set: the shape does not change with the mode, so a consumer reads
		// cross_checked rather than probing for keys.
		wantKeys(t, "release sign-manifest (unsafe)", keys, []string{
			"artifacts_matched", "channel", "checksums", "cross_checked", "key_fingerprint",
			"manifest", "policy", "signature", "version", "warnings",
		})
		wantBool(t, "release sign-manifest (unsafe)", "cross_checked", doc, false)
		wantString(t, "release sign-manifest (unsafe)", "checksums", doc, "")
		wantNumber(t, "release sign-manifest (unsafe)", "artifacts_matched", doc, 0)
		if rows, _ := doc["policy"].([]any); len(rows) != 0 {
			t.Fatalf("release sign-manifest --unsafe-no-crosscheck reported a reviewed policy it never reviewed: %#v", doc["policy"])
		}
	})

	t.Run("a REFUSAL stays an error and never becomes a document", func(t *testing.T) {
		// The manifest is honest; the checksums are not the release's.
		bad := filepath.Join(t.TempDir(), "checksums.txt")
		if err := os.WriteFile(bad, []byte(strings.Repeat("0", 64)+"  "+f.artifacts[0]+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, err := runLeafCLI(t, "-o", "json", "release", "sign-manifest", "--manifest", f.manifest,
			"--checksums", bad, "--out", filepath.Join(t.TempDir(), "no.sig"), "--sign-key", f.signKey)
		if err == nil {
			t.Fatalf("release sign-manifest signed a manifest the checksums contradict; stdout was:\n%s", out)
		}
		if strings.Contains(out, "{") {
			t.Fatalf("a REFUSAL came back as a document on stdout — a pipeline would read it as a result:\n%s", out)
		}
	})
}

func TestL4ReleaseVerifyManifestBothPanes(t *testing.T) {
	f := l4Manifest(t, "", "")
	subs := [][2]string{{f.checksums, "<CHECKSUMS>"}, {f.manifest, "<MANIFEST>"}, {f.dir, "<DIR>"}}

	commonKeys := []string{
		"artifacts_matched", "bytes_verified", "channel", "checksums", "key_fingerprint",
		"key_source", "manifest", "max_freshness_window", "policy", "signature_checked",
		"version", "warnings",
	}

	t.Run("pre-ceremony, no --sig, no --dir", func(t *testing.T) {
		out, _, err := runLeafCLI(t, "release", "verify-manifest", "--manifest", f.manifest, "--checksums", f.checksums)
		wantRC(t, "release verify-manifest (text)", err, 0)
		if !strings.Contains(out, "signature: not checked (no --sig") {
			t.Fatalf("release verify-manifest did not say the signature was unchecked:\n%s", out)
		}

		jout, errOut, jerr := runLeafCLI(t, "-o", "json", "release", "verify-manifest",
			"--manifest", f.manifest, "--checksums", f.checksums)
		wantRC(t, "release verify-manifest (json)", jerr, 0)
		if !strings.Contains(errOut, "signature: not checked (no --sig") {
			t.Fatalf("the narration was lost under -o json; stderr was:\n%s", errOut)
		}
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "release verify-manifest", keys, commonKeys)
		wantBool(t, "release verify-manifest", "signature_checked", doc, false)
		wantString(t, "release verify-manifest", "key_source", doc, "")
		wantString(t, "release verify-manifest", "key_fingerprint", doc, "")
		wantString(t, "release verify-manifest", "channel", doc, "stable")
		wantString(t, "release verify-manifest", "version", doc, "26.8.0")
		wantString(t, "release verify-manifest", "max_freshness_window", doc,
			release.DefaultMaxFreshnessWindow.String())
		wantNumber(t, "release verify-manifest", "artifacts_matched", doc, 2)
		if b, _ := doc["bytes_verified"].([]any); len(b) != 0 {
			t.Fatalf("release verify-manifest reported re-hashed bytes without --dir: %#v", doc["bytes_verified"])
		}
		wantPolicyBlock(t, "release verify-manifest", doc)
	})

	t.Run("post-ceremony, --sig and --dir bind the published bytes", func(t *testing.T) {
		args := []string{"release", "verify-manifest", "--manifest", f.manifest, "--sig", f.sig,
			"--pubkey", f.pubB64, "--checksums", f.checksums, "--dir", f.dir,
			"--expect-channel", "stable", "--expect-version", "26.8.0"}

		out, _, err := runLeafCLI(t, args...)
		wantRC(t, "release verify-manifest (signed, text)", err, 0)
		wantText(t, "release verify-manifest (signed)", l4Norm(out, subs...),
			"signature: OK (OTA key --pubkey, fingerprint <FP>)\n"+
				"\n=== POLICY THE SIGNATURE WILL COVER — READ EVERY LINE BEFORE SIGNING ===\n"+
				"    (these fields are NOT bound by checksums.txt; only your review binds them)\n"+
				"   schema_version: 1\n"+
				"   channel:        stable\n"+
				"   version:        26.8.0\n"+
				"   released_at:    <TS>\n"+
				"   min_version:    none (any version may jump directly to this release)\n"+
				"   expires:        <TS> (in <DUR>)\n"+
				"   rollout:        100% (omitted — the whole fleet)\n"+
				"   security:       false\n"+
				"   advisories:     none\n"+
				"   revoked:        none\n"+
				"   notes:          none\n"+
				"   artifacts:      2 — darwin/arm64="+f.artifacts[0]+", linux/amd64="+f.artifacts[1]+"\n"+
				"=======================================================================\n"+
				"policy:    within plausibility bounds (max freshness window <DUR>)\n"+
				"digests:   all 2 manifest artifact(s) match <CHECKSUMS>\n"+
				fmt.Sprintf("bytes:     %s (%d B) re-hashed and bound\n", f.artifacts[0], f.sizes[0])+
				fmt.Sprintf("bytes:     %s (%d B) re-hashed and bound\n", f.artifacts[1], f.sizes[1])+
				"OK: stable manifest for 26.8.0 is bound to the signed checksums and to the published bytes, and its policy is within bounds.\n"+
				"    STILL YOURS TO CONFIRM (no machine can): that the POLICY block above is the policy you intended —\n"+
				"    min_version, rollout, expires, security/advisories and notes. `OK:` means plausible, not intended.\n")

		jout, _, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
		wantRC(t, "release verify-manifest (signed, json)", jerr, 0)
		doc, keys := leafJSONKeys(t, jout)
		wantKeys(t, "release verify-manifest (signed)", keys, commonKeys)
		wantBool(t, "release verify-manifest (signed)", "signature_checked", doc, true)
		wantString(t, "release verify-manifest (signed)", "key_source", doc, "--pubkey")
		if fp, _ := doc["key_fingerprint"].(string); len(fp) != 8 {
			t.Fatalf("release verify-manifest -o json key_fingerprint = %q, want the 8-char OTA fingerprint", fp)
		}
		bytesVerified, _ := doc["bytes_verified"].([]any)
		if len(bytesVerified) != 2 {
			t.Fatalf("release verify-manifest -o json bytes_verified = %#v, want both published archives — this is the "+
				"step that makes the proof non-vacuous", doc["bytes_verified"])
		}
		for i, raw := range bytesVerified {
			row, _ := raw.(map[string]any)
			if name, _ := row["filename"].(string); name != f.artifacts[i] {
				t.Fatalf("release verify-manifest -o json bytes_verified[%d].filename = %q, want %q", i, name, f.artifacts[i])
			}
			if size, ok := row["size"].(float64); !ok || size <= 0 {
				t.Fatalf("release verify-manifest -o json bytes_verified[%d].size = %#v, want the byte count as a NUMBER", i, row["size"])
			}
		}
	})

	t.Run("a REFUSAL stays an error and never becomes a document", func(t *testing.T) {
		out, _, err := runLeafCLI(t, "-o", "json", "release", "verify-manifest",
			"--manifest", f.manifest, "--checksums", f.checksums, "--expect-channel", "security")
		if err == nil {
			t.Fatalf("release verify-manifest accepted the wrong channel; stdout was:\n%s", out)
		}
		if strings.Contains(out, "{") {
			t.Fatalf("a REFUSAL came back as a document on stdout:\n%s", out)
		}
	})
}

// ---------------------------------------------------------------------------
// upgrade
// ---------------------------------------------------------------------------

// l4Script is a stub "binary" that answers the exec-probe. A shell script rather
// than a compiled stub ON PURPOSE: the neighboring OTA suite needs `go build`
// and SKIPS without a toolchain, and a skip in the montage is a silent pass. This
// needs no toolchain, so these witnesses cannot quietly not run.
func l4Script(version string) []byte {
	return []byte("#!/bin/sh\necho \"olivares " + version + " (commit t, built t, L4 witness)\"\n")
}

type l4BundleFixture struct {
	dir      string
	pubB64   string
	artifact string
}

// l4Bundle writes an air-gap bundle for `upgrade`: manifest.json, its detached
// signature, and the artifact archive. rollout is a pointer so the caller can
// pin a cohort of 0 and reach the out-of-cohort desenlace.
func l4Bundle(t *testing.T, version string, rollout *int, bin []byte) *l4BundleFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := tarGzBinary(t, bin)
	name := release.ExpectedArtifactName(version, "linux", "amd64", "")
	sum := sha256.Sum256(archive)
	released := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	expires := released.Add(2160 * time.Hour)
	m := release.Manifest{
		SchemaVersion: release.ManifestSchemaVersion,
		Channel:       release.ChannelStable,
		Version:       version,
		ReleasedAt:    released,
		Expires:       &expires,
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Filename: name,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive))}},
	}
	m.Rollout.Percentage = rollout
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mb = append(mb, '\n')
	if _, perr := release.ParseManifest(mb); perr != nil {
		t.Fatalf("fixture premise broken: the witness bundle manifest does not validate: %v", perr)
	}
	write := func(p string, b []byte) {
		if werr := os.WriteFile(p, b, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	write(filepath.Join(dir, "manifest.json"), mb)
	write(filepath.Join(dir, "manifest.json.sig"),
		[]byte(base64.StdEncoding.EncodeToString(release.SignManifest(mb, priv))+"\n"))
	write(filepath.Join(dir, name), archive)
	return &l4BundleFixture{dir: dir, pubB64: base64.StdEncoding.EncodeToString(pub), artifact: name}
}

// l4UpgradeKeys is the key set every upgrade-plan desenlace shares. The point of
// pinning it once is that `action` distinguishes the desenlaces and the SHAPE
// does not: a consumer never branches before it can read.
var l4UpgradeKeys = []string{
	"action", "advisories", "available", "backup", "channel", "crl_recorded", "current",
	"current_declared", "eligible", "eol_at", "installed", "min_version", "notes",
	"ota_key", "ota_key_fingerprint", "released_at", "security", "source", "status",
	"target", "warnings",
}

func TestL4UpgradeReadOnlyBothPanes(t *testing.T) {
	target := writeTarget(t, l4Script("26.0.0"))
	dataDir := t.TempDir()

	cases := []struct {
		name       string
		version    string
		rollout    *int
		extraArgs  []string
		wantAction string
		wantStatus string
		wantFinal  string
		eligible   bool
	}{
		{
			name: "check", version: "26.8.0", wantAction: upgradeActionChecked,
			wantStatus: upgradeStatusAvailable, eligible: true,
			wantFinal: "\n--check OK: manifest verifies and an upgrade is available. Re-run without --check to install.\n",
		},
		{
			name: "up to date", version: "26.0.0", wantAction: upgradeActionUpToDate,
			wantStatus: upgradeStatusUpToDate, eligible: true,
			wantFinal: "\nalready on 26.0.0 (channel stable) — nothing to do.\n",
		},
		{
			name: "out of cohort", version: "26.8.0", rollout: func() *int { z := 0; return &z }(),
			extraArgs: []string{"--if-eligible"}, wantAction: upgradeActionNotInCohor,
			wantStatus: upgradeStatusAvailable, eligible: false,
			wantFinal: "\nnot in the staged-rollout cohort for 26.8.0 yet — skipping (this is expected during a partial rollout).\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := l4Bundle(t, tc.version, tc.rollout, l4Script(tc.version))
			args := append([]string{"upgrade", "--bundle", b.dir, "--check", "--pubkey", b.pubB64,
				"--target", target, "--data-dir", dataDir, "--os", "linux", "--arch", "amd64"}, tc.extraArgs...)

			out, errOut, err := runLeafCLI(t, args...)
			wantRC(t, "upgrade "+tc.name+" (text)", err, 0)
			if errOut != "" {
				t.Fatalf("upgrade wrote to stderr on the text pane: %q", errOut)
			}
			// The whole narration is on STDOUT for a reader, and the terminal sentence
			// is the last line of it.
			normalized := l4Norm(out, [2]string{b.dir, "<BUNDLE>"}, [2]string{target, "<TARGET>"})
			cohortLine := ""
			if !tc.eligible {
				cohortLine = "rollout:   this node is NOT yet in the staged-rollout cohort\n"
			}
			wantText(t, "upgrade "+tc.name, normalized,
				"OTA key: --pubkey (fingerprint <FP>)\n"+
					"source: air-gap bundle <BUNDLE>\n"+
					"channel:   stable\n"+
					"current:   26.0.0\n"+
					"available: "+tc.version+" (released <DATE>)\n"+
					"status:    "+map[bool]string{true: "up to date", false: "upgrade available"}[tc.wantStatus == upgradeStatusUpToDate]+"\n"+
					cohortLine+tc.wantFinal)

			jout, jerrOut, jerr := runLeafCLI(t, append([]string{"-o", "json"}, args...)...)
			wantRC(t, "upgrade "+tc.name+" (json)", jerr, 0)
			// Under -o json the SAME narration is on stderr, minus the terminal
			// sentence, which is now the document.
			if !strings.Contains(jerrOut, "OTA key: --pubkey") || !strings.Contains(jerrOut, "channel:   stable") {
				t.Fatalf("the upgrade narration was lost under -o json; stderr was:\n%s", jerrOut)
			}
			doc, keys := leafJSONKeys(t, jout)
			wantKeys(t, "upgrade "+tc.name, keys, l4UpgradeKeys)
			wantString(t, "upgrade "+tc.name, "action", doc, tc.wantAction)
			wantString(t, "upgrade "+tc.name, "status", doc, tc.wantStatus)
			wantString(t, "upgrade "+tc.name, "channel", doc, "stable")
			wantString(t, "upgrade "+tc.name, "current", doc, "26.0.0")
			wantString(t, "upgrade "+tc.name, "available", doc, tc.version)
			wantString(t, "upgrade "+tc.name, "ota_key", doc, "--pubkey")
			wantString(t, "upgrade "+tc.name, "source", doc, "air-gap bundle "+b.dir)
			wantString(t, "upgrade "+tc.name, "target", doc, target)
			wantString(t, "upgrade "+tc.name, "installed", doc, "")
			wantString(t, "upgrade "+tc.name, "backup", doc, "")
			wantBool(t, "upgrade "+tc.name, "eligible", doc, tc.eligible)
			// MEASURED, not declared: the target was exec-probed, so the audit-facing
			// distinction exists for must read false here.
			wantBool(t, "upgrade "+tc.name, "current_declared", doc, false)
		})
	}
}

func TestL4UpgradeDeclaredCurrentIsMarkedAsAClaim(t *testing.T) {
	// The contrafactual for current_declared: the SAME leaf, the same version, and
	// only the way it was learned differs. Without this the false above could be a
	// field that is never true.
	b := l4Bundle(t, "26.8.0", nil, l4Script("26.8.0"))
	dataDir := t.TempDir()
	out, _, err := runLeafCLI(t, "-o", "json", "upgrade", "--bundle", b.dir, "--check",
		"--pubkey", b.pubB64, "--target", "/bin/false", "--current-version", "26.0.0",
		"--data-dir", dataDir, "--os", "linux", "--arch", "amd64")
	wantRC(t, "upgrade (declared, json)", err, 0)
	doc, _ := leafJSONKeys(t, out)
	wantString(t, "upgrade (declared)", "current", doc, "26.0.0")
	wantBool(t, "upgrade (declared)", "current_declared", doc, true)
}

func TestL4UpgradeInstalledBothPanes(t *testing.T) {
	// The one desenlace that MUTATES the box. It runs through the air-gap route, so
	// it is license-gated: the dev license is installed first, which is what the
	// gate C02-20 added checks.
	run := func(t *testing.T, jsonPane bool) (string, string, error) {
		t.Helper()
		target := writeTarget(t, l4Script("26.0.0"))
		dataDir := t.TempDir()
		installDevLicense(t, dataDir)
		b := l4Bundle(t, "26.8.0", nil, l4Script("26.8.0"))
		args := []string{"upgrade", "--bundle", b.dir, "--pubkey", b.pubB64, "--target", target,
			"--data-dir", dataDir, "--os", "linux", "--arch", "amd64", "--yes"}
		if jsonPane {
			args = append([]string{"-o", "json"}, args...)
		}
		out, errOut, err := runLeafCLI(t, args...)
		// The swap really happened, or the desenlace under test was never reached.
		if got := runsVersion(t, target); !strings.Contains(got, "26.8.0") {
			t.Fatalf("the target was not swapped: %s still reports %q", target, got)
		}
		return out, errOut, err
	}

	t.Run("text", func(t *testing.T) {
		out, _, err := run(t, false)
		wantRC(t, "upgrade (installed, text)", err, 0)
		for _, want := range []string{"installed: ", " is now olivares 26.8.0", "rollback: the previous binary is backed up at "} {
			if !strings.Contains(out, want) {
				t.Fatalf("upgrade (installed) text pane lost %q:\n%s", want, out)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		out, errOut, err := run(t, true)
		wantRC(t, "upgrade (installed, json)", err, 0)
		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "upgrade (installed)", keys, l4UpgradeKeys)
		wantString(t, "upgrade (installed)", "action", doc, upgradeActionInstalled)
		wantString(t, "upgrade (installed)", "status", doc, upgradeStatusAvailable)
		if installed, _ := doc["installed"].(string); !strings.Contains(installed, "26.8.0") {
			t.Fatalf("upgrade (installed) -o json installed = %q, want the version the swap probed", installed)
		}
		backup, _ := doc["backup"].(string)
		if backup == "" {
			t.Fatalf("upgrade (installed) -o json reported no backup path — that path IS the rollback")
		}
		// The document is only worth reading if the path it names exists: a rollback
		// instruction pointing at nothing is worse than none.
		if _, serr := os.Stat(backup); serr != nil {
			t.Fatalf("upgrade (installed) -o json backup names %s, which is not there: %v", backup, serr)
		}
		// The download narration moved, it did not vanish.
		if !strings.Contains(errOut, "artifact: ") || !strings.Contains(errOut, " verified (sha256 ") {
			t.Fatalf("the artifact-verification line was lost under -o json; stderr was:\n%s", errOut)
		}
	})
}

func TestL4UpgradeInstallTimerBothPanes(t *testing.T) {
	bin := "/usr/local/bin/olivares"
	dataDir := t.TempDir()

	t.Run("--timer-dir writes the units and names both paths", func(t *testing.T) {
		dir := t.TempDir()
		out, _, err := runLeafCLI(t, "-o", "json", "upgrade", "--install-timer",
			"--timer-dir", dir, "--data-dir", dataDir, "--target", bin)
		wantRC(t, "upgrade --install-timer (json)", err, 0)
		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "upgrade --install-timer", keys, []string{
			"action", "channel", "exec_start", "schedule", "service_path", "service_unit",
			"timer_path", "timer_unit",
		})
		wantString(t, "upgrade --install-timer", "action", doc, upgradeTimerActionWrote)
		wantString(t, "upgrade --install-timer", "channel", doc, "stable")
		wantString(t, "upgrade --install-timer", "schedule", doc, "Sun *-*-* 03:00:00")
		for _, key := range []string{"service_path", "timer_path"} {
			p, _ := doc[key].(string)
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Fatalf("upgrade --install-timer -o json %q names %s, which is not there: %v", key, p, rerr)
			}
			// The unit TEXT in the document must be the unit that was WRITTEN, or the
			// document is a description of something else.
			unit := "service_unit"
			if key == "timer_path" {
				unit = "timer_unit"
			}
			if got, _ := doc[unit].(string); got != string(body) {
				t.Fatalf("upgrade --install-timer -o json %q is not the bytes at %s", unit, p)
			}
		}
		if exec, _ := doc["exec_start"].(string); !strings.HasPrefix(exec, bin+" upgrade --channel stable --if-eligible --yes") {
			t.Fatalf("upgrade --install-timer -o json exec_start = %q, want the unattended invocation the unit runs", exec)
		}
	})

	t.Run("without --timer-dir the units are the product and carry no paths", func(t *testing.T) {
		out, _, err := runLeafCLI(t, "-o", "json", "upgrade", "--install-timer", "--data-dir", dataDir, "--target", bin)
		wantRC(t, "upgrade --install-timer print (json)", err, 0)
		doc, keys := leafJSONKeys(t, out)
		wantKeys(t, "upgrade --install-timer print", keys, []string{
			"action", "channel", "exec_start", "schedule", "service_path", "service_unit",
			"timer_path", "timer_unit",
		})
		wantString(t, "upgrade --install-timer print", "action", doc, upgradeTimerActionPrinted)
		wantString(t, "upgrade --install-timer print", "service_path", doc, "")
		wantString(t, "upgrade --install-timer print", "timer_path", doc, "")
		for _, key := range []string{"service_unit", "timer_unit"} {
			body, _ := doc[key].(string)
			if !strings.Contains(body, "[Unit]") {
				t.Fatalf("upgrade --install-timer -o json %q is not a systemd unit: %q", key, body)
			}
		}

		// The text pane still prints the two blocks framed for a human, unchanged.
		tout, _, terr := runLeafCLI(t, "upgrade", "--install-timer", "--data-dir", dataDir, "--target", bin)
		wantRC(t, "upgrade --install-timer print (text)", terr, 0)
		for _, want := range []string{
			"# ---- /etc/systemd/system/olivares-upgrade.service ----",
			"# ---- /etc/systemd/system/olivares-upgrade.timer ----",
			"# (Or write the files directly with:  olivares upgrade --install-timer --timer-dir <dir>)",
		} {
			if !strings.Contains(tout, want) {
				t.Fatalf("upgrade --install-timer text pane lost %q:\n%s", want, tout)
			}
		}
	})
}

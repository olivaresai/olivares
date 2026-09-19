// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Measured 2026-09-18: `provider test` exited 0 on a REFUSED
// credential, exactly as on an accepted one. The prose was right and a script
// cannot read prose, so the next step in a first-hour script was to bind a
// credential the provider had already thrown out.

func providerTestJSON(state, detail string) string {
	return `{"provider_ref":"prv_fixture","kind":"anthropic","display_name":"P",` +
		`"key_hint":"…ABCD","state":"active","probe_state":"` + state + `",` +
		`"probe_detail":"` + detail + `","probe_latency_ms":113}`
}

func providerTestArgs(server string) []string {
	return []string{"provider", "test", "prv_fixture",
		"--server", server, "--token", "test-token", "--tenant", "tenant-a"}
}

func TestProviderTestExitCodeCarriesTheOutcome(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  int
		why   string
	}{
		{"ok", exitcode.OK, "the provider accepted the credential"},
		{"refused", exitcode.Degraded, "the command worked and the verdict is bad"},
		{"unreachable", exitcode.Indeterminate, "no verdict about the credential at all"},
		{"", exitcode.Indeterminate, "a test that measured nothing is not a working credential"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			p := newProviderProbeServer(t, http.StatusOK, providerTestJSON(tc.state, "the provider answered 401"))
			t.Setenv("HOME", t.TempDir())
			_, _, err := execRoot(t, providerTestArgs(p.URL)...)
			got := exitcode.OK
			if err != nil {
				got = exitcode.From(err)
			}
			if got != tc.want {
				t.Fatalf("probe_state %q exited %d, want %d (%s)", tc.state, got, tc.want, tc.why)
			}
		})
	}
}

// TestProviderTestRefusalOffersTheCommandThatFixesIt is the smaller half of the
// same defect: after naming the refusal, the CLI offered `provider bind` — the one
// step that cannot help, because it binds the credential just rejected.
func TestProviderTestRefusalOffersTheCommandThatFixesIt(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, providerTestJSON("refused", "the provider answered 401"))
	t.Setenv("HOME", t.TempDir())

	out, _, err := execRoot(t, providerTestArgs(p.URL)...)
	if err == nil {
		t.Fatal("a refused credential exited 0")
	}
	if strings.Contains(out, "next: olivares provider bind") {
		t.Fatalf("the refusal screen still walks the operator into binding it:\n%s", out)
	}
	if !strings.Contains(out, "next: olivares provider rotate") {
		t.Fatalf("the refusal screen does not offer the command that fixes it:\n%s", out)
	}
	// And the machine-readable outcome is the field it always was.
	if !strings.Contains(out, "REFUSED") {
		t.Fatalf("the prose no longer names the refusal:\n%s", out)
	}
}

func TestProviderTestUnreachableDoesNotJudgeTheCredential(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, providerTestJSON("unreachable", "dial tcp: timeout"))
	t.Setenv("HOME", t.TempDir())

	out, _, err := execRoot(t, providerTestArgs(p.URL)...)
	if err == nil || exitcode.From(err) != exitcode.Indeterminate {
		t.Fatalf("an unreachable endpoint must be indeterminate, got %v", err)
	}
	if strings.Contains(out, "next: olivares provider rotate") {
		t.Fatalf("an unreachable endpoint must not send the operator to replace a credential nobody judged:\n%s", out)
	}
	if !strings.Contains(out, "says nothing about the credential") {
		t.Fatalf("the sentence must keep saying what was NOT measured:\n%s", out)
	}
}

// TestProviderGetDoesNotExitOnAStoredRefusal: reading a stored verdict is not the
// same act as measuring one, so `get` and `ls` keep exiting 0 — a script that lists
// providers is not asking a question about any one of them.
func TestProviderGetDoesNotExitOnAStoredRefusal(t *testing.T) {
	p := newProviderProbeServer(t, http.StatusOK, providerTestJSON("refused", "the provider answered 401"))
	t.Setenv("HOME", t.TempDir())

	if _, _, err := execRoot(t, "provider", "get", "prv_fixture",
		"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
		t.Fatalf("provider get exited non-zero for a stored refusal: %v", err)
	}
	if p.lastMethod() != "GET" {
		t.Fatalf("method=%q", p.lastMethod())
	}
}

// TestProviderTestDocumentsItsExitCodesOnEveryPublishedPage measures the second
// half of the same defect: the contract EXISTS (probeExit) and was documented
// NOWHERE a reader can reach.
//
// ⛔ WHY THE SUMMARY AND NOT THE LONG HELP. The generated CLI reference renders a
// command's Short — into the index table and into the command's own block — and
// never its Long (scripts/cli-ref-docs/render.go). `provider test`'s Long has
// carried a full account of 7 and 8 all along, and not one of the seven
// published pages said either number, in any of the six languages: the generator
// re-derives each page from the binary's own strings, so it reported no lag
// because the string it reads was silent too. `recording sessions verify` is the
// pattern this follows.
//
// The pages are asserted as well as the string, because a Short nobody
// regenerated from documents nothing.
func TestProviderTestDocumentsItsExitCodesOnEveryPublishedPage(t *testing.T) {
	var short string
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		if c.CommandPath() == "olivares provider test" {
			short = c.Short
		}
	})
	if short == "" {
		t.Fatal("olivares provider test is not in the command tree")
	}
	for _, want := range []string{"exit 7", "8"} {
		if !strings.Contains(short, want) {
			t.Fatalf("the summary of `provider test` does not name %q, so no published page "+
				"can carry it (only Short reaches the reference): %q", want, short)
		}
	}
	// Polarity: 7 is a measured bad verdict, 8 is the absence of one. A summary
	// that swapped them would publish the opposite contract and still contain both
	// numbers.
	refused, unreachable := strings.Index(short, "7"), strings.Index(short, "8")
	if refused < 0 || unreachable < 0 || refused > unreachable {
		t.Fatalf("the summary does not name 7 before 8: %q", short)
	}
	if !strings.Contains(strings.ToLower(short), "refus") ||
		!strings.Contains(strings.ToLower(short), "unreachable") {
		t.Fatalf("the summary names the codes without their outcomes: %q", short)
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	// BOTH places the generator writes a summary: the index table AND the
	// command's own block. Asserting "the page contains it somewhere" is not
	// enough — a page regenerated in one of the two still leaves a reader who
	// scrolls to the command with no exit code in front of them. Measured while
	// writing this test: blanking the index row alone left the assertion green.
	heading := "#### Command: olivares provider test"
	assertPage := func(rel string) {
		t.Helper()
		path := filepath.Join(root, "docs-site/src/content/docs", rel)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		page := string(body)
		if n := strings.Count(page, short); n < 2 {
			t.Fatalf("%s carries the exit codes of `provider test` %d times, want the index row "+
				"AND the command's own block; regenerate with "+
				"`bash scripts/check-cli-ref-docs.sh --write`", rel, n)
		}
		block := strings.Index(page, heading)
		if block < 0 {
			t.Fatalf("%s has no block for `provider test`", rel)
		}
		if !strings.Contains(page[block:min(block+len(heading)+len(short)+64, len(page))], short) {
			t.Fatalf("%s documents the exit codes in its index but not in the block a reader "+
				"scrolls to; regenerate with `bash scripts/check-cli-ref-docs.sh --write`", rel)
		}
	}

	// English first, because it needs no roster: whatever happens below, the page
	// an English reader opens is measured.
	assertPage("reference/cli.md")
	// And then every PUBLISHED locale, derived from the declaration the generator
	// itself reads. A literal list here is the silent-green this repository has
	// already paid for: a seventh published locale would go unasserted while the
	// test reported six pages in sync.
	for _, lang := range publishedLocales(t, root) {
		assertPage(lang + "/reference/cli.md")
	}
}

// localeRoster is the JSON scripts/cli-ref-docs/published-locales.mjs writes
// after IMPORTING docs-site/src/site-locales.mjs — the same roster the generator
// consumes (scripts/cli-ref-docs/main.go, -locales-file).
type localeRoster struct {
	Schema    string   `json:"schema"`
	Published []string `json:"published"`
}

// publishedLocales derives the published locale directories the way the docs
// gate does: one node process, an ESM import of the single declaration, no
// second parser of that file anywhere.
//
// Without node the roster cannot be derived, and this SKIPS rather than guesses:
// the English assertion above has already run, and a skip says which half went
// unmeasured instead of a green that means nothing. `check-cli-ref-docs.sh`
// makes the same call ("CANNOT LOOK — no node on PATH") in the gate that always
// has one.
func publishedLocales(t *testing.T, root string) []string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH, so the published locale roster cannot be derived from " +
			"docs-site/src/site-locales.mjs: the locale pages are NOT measured by this run")
	}
	out, err := exec.Command(node, filepath.Join(root, "scripts/cli-ref-docs/published-locales.mjs"), root).Output()
	if err != nil {
		t.Fatalf("published-locales.mjs could not derive the roster: %v", err)
	}
	var roster localeRoster
	if err := json.Unmarshal(out, &roster); err != nil {
		t.Fatalf("the locale roster is not readable JSON: %v", err)
	}
	// An unrecognised schema is CANNOT LOOK, not "read it as the old one" — the
	// rule the generator states for the same document.
	if roster.Schema != "olivares.cli-ref-locales/1" {
		t.Fatalf("the locale roster declares schema %q, which this test does not speak", roster.Schema)
	}
	if len(roster.Published) == 0 {
		t.Fatal("the locale roster is empty, so this test would assert nothing")
	}
	return roster.Published
}

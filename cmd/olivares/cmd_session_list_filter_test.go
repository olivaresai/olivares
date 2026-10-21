// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// `session ls --state` and `--account` are the engine's filters: the CLI sends them
// and prints what the engine answers, instead of cutting the newest 500 rows itself.
func TestSessionListSendsStateAndAccountToTheEngine(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r-a-run", "name": "a-run", "state": "running", "provider_profile_ref": "ppf_a"},
		{"run_ref": "r-b-run", "name": "b-run", "state": "running", "provider_profile_ref": "ppf_b"},
		{"run_ref": "r-b-stop", "name": "b-stop", "state": "stopped", "provider_profile_ref": "ppf_b"},
	}
	f.profiles = []map[string]any{{"profile_ref": "ppf_b", "driver": "claude", "state": "active"}}
	f.accounts = []map[string]any{
		{"account_ref": "ppf_a", "name": "claude-a", "driver": "claude"},
		{"account_ref": "ppf_b", "name": "claude-b", "driver": "claude"},
	}

	// The account is named by its name or by its reference; the engine is always
	// asked by the reference, which is what a run stores.
	for _, account := range []string{"ppf_b", "claude-b"} {
		out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "--state", "running", "--account", account, "-o", "json"}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("--account %s: %v", account, err)
		}
		if got := listedRunRefs(t, out); got != "r-b-run" {
			t.Fatalf("ls --state running --account %s = %q, want only r-b-run", account, got)
		}
		last := f.runQueries[len(f.runQueries)-1]
		if last.Get("state") != "running" || last.Get("provider_profile_ref") != "ppf_b" {
			t.Fatalf("--account %s: engine query = %v, want state=running and provider_profile_ref=ppf_b", account, last)
		}
	}
}

// An engine older than the filters answers every run. A list that says "this
// account, running" must still hold only that, so the CLI checks the answer.
func TestSessionListChecksTheAnswerOfAnEngineWithoutTheFilters(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.ignoresRunFilters = true
	f.profiles = []map[string]any{{"profile_ref": "ppf_b", "driver": "claude", "state": "active"}}
	f.runs = []map[string]any{
		{"run_ref": "r-a-run", "name": "a-run", "state": "running", "provider_profile_ref": "ppf_a"},
		{"run_ref": "r-b-run", "name": "b-run", "state": "running", "provider_profile_ref": "ppf_b"},
		{"run_ref": "r-b-stop", "name": "b-stop", "state": "stopped", "provider_profile_ref": "ppf_b"},
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "--state", "running", "--account", "ppf_b", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := listedRunRefs(t, out); got != "r-b-run" {
		t.Fatalf("ls against an engine that ignores the filters = %q, want only r-b-run", got)
	}
}

// An account that does not resolve is an error, never an empty list that reads as
// "nothing is running there".
func TestSessionListRefusesAnUnknownAccountName(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.accounts = []map[string]any{{"account_ref": "ppf_a", "name": "claude-a", "driver": "claude"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "--account", "nobody"}, sessionCreds(f.URL)...)...)
	if err == nil {
		t.Fatal("ls --account nobody succeeded, want an error naming the account")
	}
	if len(f.runQueries) != 0 {
		t.Fatalf("the runs were listed (%v) although the account did not resolve", f.runQueries)
	}
}

func listedRunRefs(t *testing.T, out string) string {
	t.Helper()
	var shown []map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	refs := make([]string, 0, len(shown))
	for _, r := range shown {
		refs = append(refs, str(r, "run_ref"))
	}
	return strings.Join(refs, ",")
}

// A narrowed list keeps only the pins whose rows match the narrowing: a pinned row
// the filter keeps comes first, and a pinned row the filter drops is not shown even
// when an engine older than the filters sent it (the answer the CLI re-checks).
func TestSessionListNarrowedAnswerKeepsOnlyMatchingPins(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.ignoresRunFilters = true
	f.profiles = []map[string]any{{"profile_ref": "ppf_b", "driver": "claude", "state": "active"}}
	f.runs = []map[string]any{
		{"run_ref": "r-new", "name": "new", "state": "stopped", "provider_profile_ref": "ppf_b"},
		{"run_ref": "r-old-pin", "name": "old-pin", "state": "stopped", "provider_profile_ref": "ppf_b",
			"pinned": true, "pinned_at": "2026-10-14T10:00:01Z"},
		// stopped like the filter asks, but another account's: only the account
		// re-check drops it (a running row would be dropped twice, proving nothing).
		{"run_ref": "r-other-pin", "name": "other-pin", "state": "stopped", "provider_profile_ref": "ppf_a",
			"pinned": true, "pinned_at": "2026-10-14T10:00:02Z"},
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "--state", "stopped", "--account", "ppf_b", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := listedRunRefs(t, out); got != "r-old-pin,r-new" {
		t.Fatalf("ls --state stopped --account ppf_b = %q, want the matching pinned row first, then the newest, and the filtered-out pin gone", got)
	}
}

// Without the flags the request is the one it always was: limit only, so the
// released CLI against any engine keeps its behavior.
func TestSessionListWithoutFiltersAsksForNoneAndHidesCleaned(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "kept", "state": "stopped"},
		{"run_ref": "r2", "name": "gone", "state": "cleaned"},
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "gone") || !strings.Contains(out, "kept") {
		t.Fatalf("output = %s", out)
	}
	q := f.runQueries[len(f.runQueries)-1]
	if q.Get("state") != "" || q.Get("provider_profile_ref") != "" || q.Get("limit") != "500" {
		t.Fatalf("engine query = %v, want limit=500 only", q)
	}
}

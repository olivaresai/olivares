// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// Family tests for `olivares sandbox`.

func TestSandboxVerbsReachTheRoutesTheEngineRegisters(t *testing.T) {
	stepsFile := lot3WriteTempJSON(t, `[{"id":"s1"}]`)
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"sandbox", "scenarios", "ls"}, "GET", "/v1/m/sandbox/scenarios"},
		{[]string{"sandbox", "scenarios", "get", "sc-1"}, "GET", "/v1/m/sandbox/scenarios/sc-1"},
		{[]string{"sandbox", "scenarios", "create", "--name", "n", "--steps-file", stepsFile}, "POST", "/v1/m/sandbox/scenarios"},
		{[]string{"sandbox", "scenarios", "archive", "sc-1", "--yes"}, "POST", "/v1/m/sandbox/scenarios/sc-1/archive"},
		{[]string{"sandbox", "scenarios", "run", "sc-1"}, "POST", "/v1/m/sandbox/scenarios/sc-1/run"},
		{[]string{"sandbox", "replay", "--session-ref", "sess-1"}, "POST", "/v1/m/sandbox/replay"},
		{[]string{"sandbox", "runs", "ls"}, "GET", "/v1/m/sandbox/runs"},
		{[]string{"sandbox", "runs", "get", "run-1"}, "GET", "/v1/m/sandbox/runs/run-1"},
		{[]string{"sandbox", "runs", "outputs", "run-1"}, "GET", "/v1/m/sandbox/runs/run-1/outputs"},
		{[]string{"sandbox", "compare", "--scenario-ref", "sc-1", "--baseline-variant", "a", "--candidate-variant", "b"}, "POST", "/v1/m/sandbox/compare"},
		{[]string{"sandbox", "comparisons", "ls"}, "GET", "/v1/m/sandbox/comparisons"},
		{[]string{"sandbox", "comparisons", "get", "cmp-1"}, "GET", "/v1/m/sandbox/comparisons/cmp-1"},
	} {
		t.Run(strings.Join(tc.argv, "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false,"id":"x"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// TestSandboxCompareDemandsExactlyOneSourceOfSteps. Both sources present, or
// neither, is an ambiguous request: the engine would pick one and score a
// comparison the operator did not ask for.
func TestSandboxCompareDemandsExactlyOneSourceOfSteps(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"cmp-1","verdict":"pass"}`))
	for _, bad := range [][]string{
		{"sandbox", "compare", "--baseline-variant", "a", "--candidate-variant", "b"},
		{"sandbox", "compare", "--scenario-ref", "sc-1", "--session-ref", "sess-1",
			"--baseline-variant", "a", "--candidate-variant", "b"},
	} {
		_, _, err := execRoot(t, lot3Args(srv.URL, bad...)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Fatalf("%v must exit %d, got %v", bad, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d ambiguous comparison(s) were sent", n)
	}

	// THE CONTROL: exactly one source is accepted, in both spellings.
	for _, good := range [][]string{
		{"sandbox", "compare", "--scenario-ref", "sc-1", "--baseline-variant", "a", "--candidate-variant", "b"},
		{"sandbox", "compare", "--session-ref", "sess-1", "--baseline-variant", "a", "--candidate-variant", "b"},
	} {
		if _, _, err := execRoot(t, lot3Args(srv.URL, good...)...); err != nil {
			t.Fatalf("%v must be accepted, got %v", good, err)
		}
	}
	if n := srv.calls.Load(); n != 2 {
		t.Fatalf("the two valid comparisons made %d requests, want 2", n)
	}
}

// TestSandboxScenarioCreateRefusesTwoReadersOnOneStdin. Only one of the two file
// flags can read stdin; the second would get an already-drained reader and record
// a fixture with an EMPTY mock list — a scenario that runs and proves nothing.
func TestSandboxScenarioCreateRefusesTwoReadersOnOneStdin(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"sc-1"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "scenarios", "create",
		"--name", "n", "--steps-file", "-", "--mocks-file", "-")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("two stdin readers must exit %d, got %v", exitcode.Usage, err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent with two stdin readers", n)
	}

	// THE CONTROL: two FILES are fine, and both reach the engine.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "scenarios", "create",
		"--name", "n",
		"--steps-file", lot3WriteTempJSON(t, `[{"id":"s1"}]`),
		"--mocks-file", lot3WriteTempJSON(t, `[{"tool":"m1"}]`))...); err != nil {
		t.Fatalf("two file paths must be accepted, got %v", err)
	}
	body := srv.lastBody()
	if !strings.Contains(body, `"s1"`) || !strings.Contains(body, `"m1"`) {
		t.Fatalf("both documents must reach the engine, body was: %s", body)
	}
}

// TestSandboxArchiveIsTheOnlyDestructiveVerbAndItIsAPost pins the census finding
// this family exists to demonstrate: counting DELETEs here finds ZERO, and the
// irreversible verb is a POST.
func TestSandboxArchiveIsTheOnlyDestructiveVerbAndItIsAPost(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"id":"sc-1","status":"archived"}`))
	_, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "scenarios", "archive", "sc-1")...)
	if err == nil || exitcode.From(err) != exitcode.Usage {
		t.Fatalf("archive without --yes must exit %d, got %v", exitcode.Usage, err)
	}
	if !strings.Contains(err.Error(), "archive sandbox scenario sc-1") {
		t.Errorf("the prompt must name the exact target, got: %v", err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the server before consent", n)
	}

	// THE CONTROL: the neighboring non-destructive verb on the SAME resource is
	// not gated. Without this, "everything needs --yes" would pass too.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "scenarios", "run", "sc-1")...); err != nil {
		t.Fatalf("running a scenario must not need a ceremony, got %v", err)
	}
	if got, _ := srv.method.Load().(string); got != "POST" {
		t.Errorf("method = %s, want POST", got)
	}
}

// TestSandboxRunFiltersReachTheEngine: a filter the CLI accepts but never sends
// silently widens the answer, and an operator reads someone else's runs as theirs.
func TestSandboxRunFiltersReachTheEngine(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"items":[],"has_more":false}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL,
		"sandbox", "runs", "ls", "--kind", "replay", "--scenario-ref", "sc-1")...); err != nil {
		t.Fatalf("the filtered list must succeed, got %v", err)
	}
	q := srv.lastQuery()
	if !strings.Contains(q, "kind=replay") || !strings.Contains(q, "scenario_ref=sc-1") {
		t.Fatalf("the filters did not reach the engine, query was %q", q)
	}

	// THE CONTROL: an unset filter is ABSENT from the URL, not present and empty
	// — several handlers treat an empty value as a real filter value.
	if _, _, err := execRoot(t, lot3Args(srv.URL, "sandbox", "runs", "ls")...); err != nil {
		t.Fatalf("the unfiltered list must succeed, got %v", err)
	}
	if q := srv.lastQuery(); strings.Contains(q, "kind=") || strings.Contains(q, "scenario_ref=") {
		t.Fatalf("an unset filter was sent as empty, query was %q", q)
	}
}

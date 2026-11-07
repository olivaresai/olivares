// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func askHoldsAll(auth.Permission) bool { return true }

// The table is the whole of the agent's power: no tool may carry an admin verb, and in
// particular none may decide an approval (design D1, test T1).
func TestToolTableHasNoAdminVerb(t *testing.T) {
	for _, tl := range askToolTable {
		if strings.HasSuffix(string(tl.Permission), ":admin") {
			t.Errorf("tool %s carries admin permission %s", tl.Name, tl.Permission)
		}
		if strings.Contains(tl.Path, "decisions") {
			t.Errorf("tool %s reaches a decision route %s", tl.Name, tl.Path)
		}
	}
}

// T4/T3: an injected "approve" call names a tool that is not in the table. It is refused,
// not forwarded, and the refusal is the evidence of the attempt.
func TestPlanRefusesUnknownTool(t *testing.T) {
	for _, out := range []string{
		`{"tool":"approvals.decide","args":{"id":"x"}}`,
		`{"tool":"","args":{}}`,
		`{"tool":"runs.list","args":{"bogus":1}}`,
	} {
		got := askPlan(out, askHoldsAll, nil)
		if got.Kind != askOutcomeRefused {
			t.Errorf("askPlan(%s) = %s, want refused", out, got.Kind)
		}
		if got.Call != nil {
			t.Errorf("askPlan(%s) returned a call for a refused tool", out)
		}
	}
}

// D1: the person's own current permission is the ceiling, so a tool the person does not
// hold is refused even though it is in the table (test T7), and so is one the caller did
// not allow for this turn.
func TestPlanRefusesToolOutsideCeiling(t *testing.T) {
	deny := func(p auth.Permission) bool { return p != askPermRunsRead }
	if got := askPlan(`{"tool":"runs.list","args":{}}`, deny, nil); got.Kind != askOutcomeRefused {
		t.Fatalf("a tool the person lacks was %s, want refused", got.Kind)
	}
	allow := map[auth.Permission]bool{"governance:approval:read": true}
	if got := askPlan(`{"tool":"runs.list","args":{}}`, askHoldsAll, allow); got.Kind != askOutcomeRefused {
		t.Fatalf("a tool outside --allow was %s, want refused", got.Kind)
	}
}

// D2: a read is a call to run at once; a write is only a proposal for a person to approve.
func TestPlanSeparatesReadFromWrite(t *testing.T) {
	r := askPlan(`{"tool":"runs.list","args":{}}`, askHoldsAll, nil)
	if r.Kind != askOutcomeCall || r.Call == nil || r.Call.Method != "GET" || r.Call.Path != "/v1/m/sessions/runs" {
		t.Fatalf("read = %+v", r)
	}
	if r.Call.Command != "olivares session ls" {
		t.Errorf("read command = %q", r.Call.Command)
	}
	w := askPlan("```json\n{\"tool\":\"run.start\",\"args\":{\"name\":\"fix tests\"}}\n```", askHoldsAll, nil)
	if w.Kind != askOutcomeProposal || w.Call == nil || w.Call.Method != "POST" {
		t.Fatalf("write = %+v", w)
	}
	if want := `olivares session start --name 'fix tests'`; w.Call.Command != want {
		t.Errorf("write command = %q, want %q", w.Call.Command, want)
	}
	if string(w.Call.Body) != `{"name":"fix tests","transport":"stream-json"}` {
		t.Errorf("write body = %s", w.Call.Body)
	}
}

// Anything that is not a tool call is the answer, shown as text.
func TestPlanTreatsOtherOutputAsAnswer(t *testing.T) {
	got := askPlan("You have 2 sessions. {not json", askHoldsAll, nil)
	if got.Kind != askOutcomeAnswer || got.Text != "You have 2 sessions. {not json" {
		t.Fatalf("plan = %+v", got)
	}
}

// An argument that cannot be quoted safely into the printed command is refused, so the
// command shown to the person is exactly the action proposed.
func TestPlanRefusesOversizedOrControlArgument(t *testing.T) {
	long := `{"tool":"run.start","args":{"name":"` + strings.Repeat("a", 200) + `"}}`
	if got := askPlan(long, askHoldsAll, nil); got.Kind != askOutcomeRefused {
		t.Errorf("oversized name = %s, want refused", got.Kind)
	}
	if got := askPlan(`{"tool":"run.start","args":{"name":"a\nb"}}`, askHoldsAll, nil); got.Kind != askOutcomeRefused {
		t.Errorf("newline in name = %s, want refused", got.Kind)
	}
}

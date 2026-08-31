// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestBatterySuiteSelection(t *testing.T) {
	tests := []struct {
		suite string
		want  int
	}{
		{"", len(injectionProbes()) + len(jailbreakProbes()) + len(exfilProbes()) + len(toolPoisonProbes())},
		{"all", len(injectionProbes()) + len(jailbreakProbes()) + len(exfilProbes()) + len(toolPoisonProbes())},
		{familyInjection, len(injectionProbes())},
		{familyJailbreak, len(jailbreakProbes())},
		{familyExfil, len(exfilProbes())},
		{familyToolPoisoning, len(toolPoisonProbes())},
		{"unknown", 0},
	}
	for _, tt := range tests {
		t.Run(tt.suite, func(t *testing.T) {
			got := battery(tt.suite)
			if len(got) != tt.want {
				t.Fatalf("battery(%q) = %d probes, want %d", tt.suite, len(got), tt.want)
			}
			for _, p := range got {
				if tt.suite != "" && tt.suite != "all" && p.Family != tt.suite {
					t.Fatalf("battery(%q) included %s family %q", tt.suite, p.ID, p.Family)
				}
			}
		})
	}
}

func TestRunProbesContinuesAfterSandboxErrorAndNormalizesEmptyOutcome(t *testing.T) {
	sb := scriptedSandbox{
		results: map[string]ProbeResult{
			"empty": {Executed: true},
			"pass":  {Executed: true, Outcome: OutcomeBlocked, Reason: "blocked"},
		},
		errs: map[string]error{"err": errors.New(strings.Repeat("x", 240))},
	}
	m := New(WithSandbox(sb))
	out := m.runProbes(context.Background(), model.TenantID("tenant"), Target{AgentRef: "agent"}, []Probe{
		{ID: "err"},
		{ID: "empty"},
		{ID: "pass"},
	})
	if len(out) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(out))
	}
	if out[0].result.Outcome != OutcomeError || len([]rune(out[0].result.Reason)) > 201 {
		t.Fatalf("error outcome = %+v, want clamped error", out[0].result)
	}
	if out[1].result.Outcome != OutcomeError {
		t.Fatalf("empty outcome normalized to %q, want %q", out[1].result.Outcome, OutcomeError)
	}
	if out[2].result.Outcome != OutcomeBlocked {
		t.Fatalf("pass outcome = %q, want blocked", out[2].result.Outcome)
	}
}

type scriptedSandbox struct {
	results map[string]ProbeResult
	errs    map[string]error
}

func (s scriptedSandbox) Execute(_ context.Context, _ model.TenantID, _ Target, p Probe) (ProbeResult, error) {
	if err := s.errs[p.ID]; err != nil {
		return ProbeResult{}, err
	}
	return s.results[p.ID], nil
}

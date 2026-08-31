// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestOutcomePassFailClassification(t *testing.T) {
	tests := []struct {
		out      Outcome
		wantPass bool
		wantFail bool
	}{
		{OutcomeBlocked, true, false},
		{OutcomeRefused, true, false},
		{OutcomeComplied, false, true},
		{OutcomeLeaked, false, true},
		{OutcomeError, false, false},
		{OutcomeSkipped, false, false},
	}
	for _, tt := range tests {
		if tt.out.pass() != tt.wantPass || tt.out.fail() != tt.wantFail {
			t.Fatalf("%s pass/fail = %v/%v, want %v/%v", tt.out, tt.out.pass(), tt.out.fail(), tt.wantPass, tt.wantFail)
		}
	}
}

func TestOfflineSandboxDenyClosedDegradedPosture(t *testing.T) {
	got, err := offlineSandbox{}.Execute(context.Background(), model.TenantID("t"), Target{AgentRef: "a"}, Probe{ID: "p"})
	if err != nil {
		t.Fatalf("offline sandbox error: %v", err)
	}
	if got.Executed || got.Outcome != OutcomeSkipped {
		t.Fatalf("offline result = %+v, want not executed/skipped", got)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestSuggestTier(t *testing.T) {
	tests := []struct {
		name     string
		signals  agentRiskSignals
		expected string
	}{
		{
			name:     "no signals → low",
			signals:  agentRiskSignals{},
			expected: "low",
		},
		{
			name:     "some edges → low",
			signals:  agentRiskSignals{TotalEdges: 3, RWEdges: 1},
			expected: "low",
		},
		{
			name:     "many RW edges → medium",
			signals:  agentRiskSignals{TotalEdges: 10, RWEdges: 7},
			expected: "medium",
		},
		{
			name:     "scheduled → medium",
			signals:  agentRiskSignals{TotalEdges: 2, Scheduled: true},
			expected: "medium",
		},
		{
			name:     "high finding → high",
			signals:  agentRiskSignals{TotalEdges: 5, HighFindings: 1},
			expected: "high",
		},
		{
			name:     "autonomous + many RW → high",
			signals:  agentRiskSignals{TotalEdges: 20, RWEdges: 15, Autonomous: true},
			expected: "high",
		},
		{
			name:     "critical finding → critical",
			signals:  agentRiskSignals{TotalEdges: 5, CritFindings: 1},
			expected: "critical",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := suggestTier(tt.signals)
			if got != tt.expected {
				t.Errorf("suggestTier(%+v) = %q, want %q", tt.signals, got, tt.expected)
			}
		})
	}
}

// TestGatherAgentRiskSignalsCriticalBeyondFirstPage (D-03) reproduces the
// enforcement-path truncation: an agent with more than one page (listCap) of
// findings whose single CRITICAL finding sorts onto a LATER page. Before the
// keyset-drain fix, gatherAgentRiskSignals read only the first page, missed the
// critical, and the classifier silently suggested a LOWER tier — so the tier-floor
// never applied the critical immediate-stop.
func TestGatherAgentRiskSignalsCriticalBeyondFirstPage(t *testing.T) {
	f := newGuardianFixture(t)
	ctx := context.Background()
	agentID := model.NewID()

	// listCap low findings created FIRST (earlier UUIDv7 ids ⇒ page 1), then ONE
	// critical LAST (a later id ⇒ the last page). Also give the agent >listCap
	// read/write access edges so the edge scan is exercised past its first page too.
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		for i := 0; i < listCap; i++ {
			if _, err := sc.Findings().Create(ctx, model.Finding{
				Kind: "guardrail", Severity: model.SeverityLow, Status: model.FindingOpen, Source: "test",
				SubjectKind: "agent", SubjectID: agentID, Title: "noise",
				OccurredAt: model.NewTimestamp(intBase),
			}); err != nil {
				return err
			}
		}
		if _, err := sc.Findings().Create(ctx, model.Finding{
			Kind: "guardrail", Severity: model.SeverityCritical, Status: model.FindingOpen, Source: "test",
			SubjectKind: "agent", SubjectID: agentID, Title: "the critical one",
			OccurredAt: model.NewTimestamp(intBase),
		}); err != nil {
			return err
		}
		for i := 0; i < listCap+5; i++ {
			if _, err := sc.AccessEdges().Create(ctx, model.AccessEdge{
				OriginKind: "agent", OriginID: agentID, ResourceID: model.NewID(),
				Mode: sdkmodel.ModeReadWrite, Observed: true,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var sig agentRiskSignals
	if err := f.st.View(ctx, f.tenant, func(sc store.Scope) error {
		var e error
		sig, e = f.m.gatherAgentRiskSignals(ctx, sc, agentID)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if sig.CritFindings == 0 {
		t.Fatalf("critical finding beyond the first page was truncated: CritFindings=%d", sig.CritFindings)
	}
	if sig.TotalEdges <= listCap {
		t.Fatalf("edge scan truncated at one page: TotalEdges=%d, want > %d", sig.TotalEdges, listCap)
	}
	if got := suggestTier(sig); got != string(RiskTierCritical) {
		t.Fatalf("suggestTier = %q, want critical (a critical finding on a later page must still classify critical)", got)
	}
}

// TestSuggestTierTruncatedFailsSafe (D-03) locks the fail-safe: when the
// signal scan could not be completed (Truncated), the classifier must NOT emit a
// tier below critical — an unseen finding must never be classified away.
func TestSuggestTierTruncatedFailsSafe(t *testing.T) {
	// Signals that would otherwise classify LOW, but the scan was truncated.
	got := suggestTier(agentRiskSignals{TotalEdges: 1, Truncated: true})
	if got != string(RiskTierCritical) {
		t.Fatalf("suggestTier(truncated) = %q, want critical (fail-safe: never lower on truncation)", got)
	}
}

func TestEffectiveTier(t *testing.T) {
	tests := []struct {
		operator  string
		suggested string
		expected  string
	}{
		{"", "", ""},
		{"", "low", "low"},
		{"high", "low", "high"},
		{"", "critical", "critical"},
		{"medium", "critical", "medium"},
	}
	for _, tt := range tests {
		got := effectiveTier(tt.operator, tt.suggested)
		if got != tt.expected {
			t.Errorf("effectiveTier(%q, %q) = %q, want %q", tt.operator, tt.suggested, got, tt.expected)
		}
	}
}

func TestTierRank(t *testing.T) {
	tests := []struct {
		tier string
		rank int
	}{
		{"", 0},
		{"unknown", 0},
		{"low", 1},
		{"medium", 2},
		{"high", 3},
		{"critical", 4},
		{"HIGH", 3},
		{"  Critical  ", 4},
	}
	for _, tt := range tests {
		got := tierRank(tt.tier)
		if got != tt.rank {
			t.Errorf("tierRank(%q) = %d, want %d", tt.tier, got, tt.rank)
		}
	}
}

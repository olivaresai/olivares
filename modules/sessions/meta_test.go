// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// identityEdge is the connector's session→agent attribution edge (OBS-09): origin
// session, resource identity.agent, ref the agent name, no tool.
func identityEdge(session, agent string) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: "session", OriginRef: session, ResourceKind: "identity.agent", ResourceRef: agent,
		Mode: sdkmodel.ModeUnknown, Source: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed,
		ObservedAt: baseTime,
	}
}

// TestAgentRefFromIdentityEdge proves the previously-dead agent_ref column is now
// written from the connector's identity attribution — never guessed.
func TestAgentRefFromIdentityEdge(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	if err := m.onEdge(ctx, tenant.String(), identityEdge("sess-1", "backend-agent")); err != nil {
		t.Fatal(err)
	}
	rec, ok := getLive(t, m, st, tenant, "sess-1")
	if !ok {
		t.Fatal("no live row")
	}
	if dto := m.toLiveDTO(rec); dto.AgentRef != "backend-agent" {
		t.Fatalf("agent_ref = %q, want backend-agent", dto.AgentRef)
	}
}

// TestSummaryFromCompactionFinding proves the previously-dead summary column is now
// written from the cooperative close/compaction metadata (a forensic-continuity
// finding), bounded and non-sensitive — never an LLM-fabricated summary.
func TestSummaryFromCompactionFinding(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	const title = "Claude Code context compaction (transcript summarized)"
	if err := m.onFinding(ctx, tenant.String(), sdkmodel.FindingReport{
		Kind: "forensic", SubjectKind: "session", SubjectRef: "sess-1", Title: title, OccurredAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	rec, ok := getLive(t, m, st, tenant, "sess-1")
	if !ok {
		t.Fatal("no live row")
	}
	if dto := m.toLiveDTO(rec); dto.Summary != title {
		t.Fatalf("summary = %q, want %q", dto.Summary, title)
	}
}

// TestMetadataEmptyWithoutSignal proves deny-closed/minimal-data: a session with only
// tool activity (no identity attribution, no close metadata, no prompt opt-in) leaves
// agent_ref, goal and summary EMPTY — populated, not null-broken, never a placeholder.
func TestMetadataEmptyWithoutSignal(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/a.txt", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
		t.Fatal(err)
	}
	rec, ok := getLive(t, m, st, tenant, "sess-1")
	if !ok {
		t.Fatal("no live row")
	}
	dto := m.toLiveDTO(rec)
	if dto.AgentRef != "" || dto.Goal != "" || dto.Summary != "" {
		t.Fatalf("metadata should be empty without a signal, got agent_ref=%q goal=%q summary=%q", dto.AgentRef, dto.Goal, dto.Summary)
	}
}

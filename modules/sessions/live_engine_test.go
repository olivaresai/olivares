// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// live_engine_test.go pins the SG-01 rule that /sessions must not paint two different
// classes of session identically.
//
// Before this, the live view had no engine at all: the provider rode the wire and was
// dropped at the fold, and the emitting connector's name rode the bus envelope and was
// dropped too. With Codex able to be ENFORCED on PreToolUse and only OBSERVED on
// PostToolUse, showing both the same way asserts a control that in one case does not exist.

// liveRow reads the live record for a session, failing the test when there is none — the
// absence of a row is itself a defect in every case here.
func liveRow(t *testing.T, m *Module, st store.Store, tenant model.TenantID, ref string) model.Record {
	t.Helper()
	rec, ok := getLive(t, m, st, tenant, ref)
	if !ok {
		t.Fatalf("no live row for %s: the session never became visible", ref)
	}
	return rec
}

func codexEdge(sid, tool, posture string) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind:   "session",
		OriginRef:    sid,
		ResourceKind: "shell",
		ResourceRef:  "echo hi",
		Mode:         sdkmodel.AccessMode("write"),
		Source:       sdkmodel.SignalSource("codex_hook"),
		Confidence:   sdkmodel.ConfidenceAttributed,
		ToolRef:      tool,
		ObservedAt:   baseTime,
		Labels:       map[string]string{labelEngine: "codex", labelPosture: posture},
	}
}

// TestEngineAndPostureReachTheLiveView is the visibility requirement: a Codex session's
// action must land in the live view NAMED as Codex.
func TestEngineAndPostureReachTheLiveView(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	if err := m.onEdge(ctx, tenant.String(), codexEdge("osn_live_1", "Bash", "enforced")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}

	rec := liveRow(t, m, st, tenant, "osn_live_1")
	if got := rec.String(colEngine); got != "codex" {
		t.Errorf("the live row must name the engine, got %q", got)
	}
	if got := rec.String(colPosture); got != "enforced" {
		t.Errorf("the live row must carry the posture, got %q", got)
	}
	dto := m.toLiveDTO(rec)
	if dto.Engine != "codex" || dto.Posture != "enforced" {
		t.Errorf("the DTO the console reads must carry both, got engine=%q posture=%q", dto.Engine, dto.Posture)
	}
}

// TestPostureTakesTheWeakestValue: one merely observed action makes the session not fully
// enforced. Rounding it up would be exactly the overstatement this column exists to stop.
func TestPostureTakesTheWeakestValue(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	// Enforced first, then a merely observed action.
	if err := m.onEdge(ctx, tenant.String(), codexEdge("osn_live_2", "Bash", "enforced")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if err := m.onEdge(ctx, tenant.String(), codexEdge("osn_live_2", "Bash", "observed")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if got := liveRow(t, m, st, tenant, "osn_live_2").String(colPosture); got != "observed" {
		t.Errorf("a session with an unenforceable action is not enforced, got %q", got)
	}

	// …and the weaker value is not then upgraded back by a later enforced action.
	if err := m.onEdge(ctx, tenant.String(), codexEdge("osn_live_2", "Bash", "enforced")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if got := liveRow(t, m, st, tenant, "osn_live_2").String(colPosture); got != "observed" {
		t.Errorf("posture must not be upgraded once something was merely observed, got %q", got)
	}
}

// TestUnlabelledEdgeDoesNotEraseTheEngine: a connector that declares nothing must not wipe
// what another already established. An absent label is "no information", not "none".
func TestUnlabelledEdgeDoesNotEraseTheEngine(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	if err := m.onEdge(ctx, tenant.String(), codexEdge("osn_live_3", "Bash", "enforced")); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	bare := codexEdge("osn_live_3", "Bash", "enforced")
	bare.Labels = nil
	if err := m.onEdge(ctx, tenant.String(), bare); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	if got := liveRow(t, m, st, tenant, "osn_live_3").String(colEngine); got != "codex" {
		t.Errorf("an unlabelled edge must not erase a known engine, got %q", got)
	}
}

// TestEngineIsAbsentNotDefaulted: a session nothing has declared for shows no engine. The
// console renders nothing, which is true; defaulting to a value would be a claim.
func TestEngineIsAbsentNotDefaulted(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	e := codexEdge("osn_live_4", "Bash", "")
	e.Labels = nil
	if err := m.onEdge(context.Background(), tenant.String(), e); err != nil {
		t.Fatalf("onEdge: %v", err)
	}
	dto := m.toLiveDTO(liveRow(t, m, st, tenant, "osn_live_4"))
	if dto.Engine != "" || dto.Posture != "" {
		t.Errorf("an undeclared session must show no engine and no posture, got %q/%q", dto.Engine, dto.Posture)
	}
}

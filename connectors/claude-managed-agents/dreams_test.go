// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk/model"
)

// testDream builds the canonical completed dream fixture: one input store, two
// transcript sessions, one output store, a pipeline session and final usage.
func testDream() Dream {
	d := Dream{
		ID:        "drm_1",
		Status:    "completed",
		SessionID: "sesn_dream",
		CreatedAt: testTime.Add(-2 * time.Hour).Format(time.RFC3339),
		EndedAt:   testTime.Add(-time.Hour).Format(time.RFC3339),
		Inputs: []DreamInput{
			{Type: "memory_store", MemoryStoreID: "memstore_in"},
			{Type: "sessions", SessionIDs: []string{"sesn_1", "sesn_2"}},
		},
		Outputs: []DreamOutput{{Type: "memory_store", MemoryStoreID: "memstore_out"}},
	}
	d.Model.ID = "claude-opus-4-8"
	d.Usage.InputTokens = 1200
	d.Usage.OutputTokens = 300
	return d
}

func TestDreamObservationsProvenance(t *testing.T) {
	d := testDream()
	created := testTime.Add(-2 * time.Hour)
	obs := dreamObservations(d, "ws_1", map[string]bool{}, testTime)

	var edges []model.EdgeObservation
	var findings []model.FindingReport
	var costs []model.CostSample
	for _, o := range obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		case model.CostSample:
			costs = append(costs, v)
		}
	}

	// The full provenance: workspace→dream, workspace→output store, and the pipeline
	// session's read of the input store + both transcripts and write of the output.
	type key struct {
		origin, kind, ref string
		mode              model.AccessMode
	}
	got := map[key]model.EdgeObservation{}
	for _, e := range edges {
		got[key{e.OriginRef, e.ResourceKind, e.ResourceRef, e.Mode}] = e
		if !e.ObservedAt.Equal(created) {
			t.Errorf("dream edges must carry the dream created_at for stable de-dup, got %v", e.ObservedAt)
		}
	}
	want := []key{
		{"ws_1", kindDream, "drm_1", model.ModeRead},
		{"ws_1", kindMemoryStore, "memstore_out", model.ModeRead},
		{"sesn_dream", kindMemoryStore, "memstore_in", model.ModeRead},
		{"sesn_dream", kindManagedAgent, "sesn_1", model.ModeRead},
		{"sesn_dream", kindManagedAgent, "sesn_2", model.ModeRead},
		{"sesn_dream", kindMemoryStore, "memstore_out", model.ModeWrite},
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing provenance edge %+v (have %+v)", k, edges)
		}
	}
	for _, e := range edges {
		if e.OriginRef == "sesn_dream" && e.ToolRef != "drm_1" {
			t.Errorf("session-origin provenance edges must carry the dream id as ToolRef, got %+v", e)
		}
	}

	// Admission: pending (Medium, ASI06) on the unadmitted output store, stamped with
	// the dream's ended_at.
	var pending *model.FindingReport
	for i := range findings {
		if strings.Contains(findings[i].Title, "awaiting HITL admission") {
			pending = &findings[i]
		}
	}
	if pending == nil {
		t.Fatalf("missing pending-admission finding: %+v", findings)
	}
	if pending.Severity != model.SeverityMedium || pending.SubjectKind != kindMemoryStore || pending.SubjectRef != "memstore_out" {
		t.Errorf("pending admission shape wrong: %+v", *pending)
	}
	if len(pending.OWASPASI) != 1 || pending.OWASPASI[0] != asiMemoryPoison {
		t.Errorf("pending admission must carry ASI06, got %v", pending.OWASPASI)
	}
	if !pending.OccurredAt.Equal(testTime.Add(-time.Hour)) {
		t.Errorf("pending admission must be stamped at ended_at for stable de-dup, got %v", pending.OccurredAt)
	}

	// Cost: one terminal sample, segmented by CostType.
	if len(costs) != 1 || costs[0].CostType != dreamCostType || costs[0].ModelRef != "claude-opus-4-8" ||
		costs[0].InputTokens != 1200 || costs[0].OutputTokens != 300 || costs[0].SessionRef != "sesn_dream" {
		t.Errorf("dream cost sample wrong: %+v", costs)
	}
	if costs[0].Provenance != model.ProvenanceEstimated || costs[0].CostMicroUSD != 0 {
		t.Errorf("dream cost is unpriced+estimated (module XI applies list pricing), got %+v", costs[0])
	}
}

func TestDreamObservationsAdmitted(t *testing.T) {
	obs := dreamObservations(testDream(), "ws_1", map[string]bool{"memstore_out": true}, testTime)
	for _, o := range obs {
		if f, ok := o.(model.FindingReport); ok {
			if strings.Contains(f.Title, "awaiting HITL admission") {
				t.Errorf("an admitted output must not re-emit pending admission: %+v", f)
			}
			if strings.Contains(f.Title, "ADMITTED") && f.Severity != model.SeverityInfo {
				t.Errorf("the admission acknowledgment is Info ledger evidence, got %+v", f)
			}
		}
	}
}

func TestDreamObservationsPendingDream(t *testing.T) {
	// A pending dream has no session and no outputs yet: only the inventory edge, no
	// cost (usage not final), no admission findings.
	d := Dream{ID: "drm_p", Status: "pending", CreatedAt: testTime.Format(time.RFC3339)}
	d.Usage.InputTokens = 10 // moving usage must NOT emit before terminal
	obs := dreamObservations(d, "ws_1", map[string]bool{}, testTime)
	if len(obs) != 1 {
		t.Fatalf("a pending dream emits only its inventory edge, got %+v", obs)
	}
	e, ok := obs[0].(model.EdgeObservation)
	if !ok || e.ResourceKind != kindDream {
		t.Errorf("expected the workspace→dream inventory edge, got %+v", obs[0])
	}
}

func TestDreamFailedFinding(t *testing.T) {
	d := testDream()
	d.Status = "failed"
	d.Error.Type = "input_memory_store_unavailable"
	obs := dreamObservations(d, "ws_1", map[string]bool{}, testTime)
	var failed bool
	for _, o := range obs {
		if f, ok := o.(model.FindingReport); ok && f.SubjectKind == kindDream {
			failed = true
			if f.Severity != model.SeverityLow || !strings.Contains(f.Title, "input_memory_store_unavailable") {
				t.Errorf("failed-dream finding wrong: %+v", f)
			}
		}
	}
	if !failed {
		t.Error("a failed dream must emit a failure finding")
	}
}

func TestUnadmittedAttachFinding(t *testing.T) {
	created := testTime.Add(-time.Hour)
	s := Session{ID: "sesn_other", Status: "running", CreatedAt: created.Format(time.RFC3339)}
	f := unadmittedAttachFinding("drm_1", "memstore_out", s, testTime)
	if f.Severity != model.SeverityHigh || f.SubjectKind != kindMemoryStore || f.SubjectRef != "memstore_out" {
		t.Fatalf("unadmitted attach must be a HIGH memory-store finding (so it persists in the security view), got %+v", f)
	}
	if len(f.OWASPASI) != 1 || f.OWASPASI[0] != asiMemoryPoison {
		t.Errorf("unadmitted attach must carry ASI06, got %v", f.OWASPASI)
	}
	if !f.OccurredAt.Equal(created) {
		t.Errorf("attach finding must be stamped at the session created_at for stable de-dup, got %v", f.OccurredAt)
	}
}

func TestDreamsBetaHeader(t *testing.T) {
	if got := dreamsBeta(defaultBetaHeader); got != defaultBetaHeader+","+dreamsBetaSuffix {
		t.Errorf("dreamsBeta = %q", got)
	}
	// Idempotent when the operator already included the gate; tolerant of empty.
	if got := dreamsBeta(defaultBetaHeader + "," + dreamsBetaSuffix); strings.Count(got, dreamsBetaSuffix) != 1 {
		t.Errorf("dreamsBeta must not duplicate the gate: %q", got)
	}
	if got := dreamsBeta(""); got != dreamsBetaSuffix {
		t.Errorf("dreamsBeta(\"\") = %q", got)
	}
}

func TestDreamsGatedClassification(t *testing.T) {
	if !dreamsGated(&httpx.StatusError{Path: "/v1/dreams", Status: 403}) {
		t.Error("403 is the gated-preview signal")
	}
	if !dreamsGated(&httpx.StatusError{Path: "/v1/dreams", Status: 404}) {
		t.Error("404 is the gated-preview signal (endpoints unpublished without access)")
	}
	if dreamsGated(&httpx.StatusError{Path: "/v1/dreams", Status: 401}) {
		t.Error("401 is a credential fault, not gating")
	}
	if dreamsGated(errors.New("dial tcp: connection refused")) {
		t.Error("a transport error is not gating")
	}
}

// TestEnrichIdledSessionRoutesHITL proves the webhook GET-back recovers the
// requires_action signal from the session EVENT list (the session resource carries no
// stop_reason) and routes it as the pending-confirmation finding + policy edge.
func TestEnrichIdledSessionRoutesHITL(t *testing.T) {
	created := testTime.Add(-time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sessions/sesn_h":
			_, _ = w.Write([]byte(`{"id":"sesn_h","status":"idle","created_at":"` + created + `",
				"resources":[{"type":"memory_store","memory_store_id":"memstore_1","access":"read_only"}]}`))
		case "/v1/sessions/sesn_h/events":
			_, _ = w.Write([]byte(`{"data":[
				{"type":"agent.message"},
				{"type":"session.status_idle","stop_reason":{"type":"requires_action","event_ids":["evt_1","evt_2"]}}
			]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{cfgAPIKey: "sk-ant-test", cfgBaseURL: srv.URL})
	obs := s.enrichIdledSession(context.Background(), "sesn_h", testTime)

	var hitl *model.FindingReport
	var gateEdge, mountEdge bool
	for _, o := range obs {
		switch v := o.(type) {
		case model.FindingReport:
			if strings.Contains(v.Title, "awaiting human confirmation") {
				hitl = &v
			}
		case model.EdgeObservation:
			if v.ResourceKind == kindPermPolicy {
				gateEdge = true
			}
			if v.ResourceKind == kindMemoryStore && v.Mode == model.ModeRead {
				mountEdge = true
			}
		}
	}
	if hitl == nil || hitl.SubjectKind != kindManagedAgent || hitl.Kind != findingGovernance {
		t.Fatalf("missing/wrong HITL pending finding: %+v", obs)
	}
	if !gateEdge || !mountEdge {
		t.Errorf("expected the always_ask gate edge and the read_only mount edge, got %+v", obs)
	}
}

// TestAwaitingConfirmationNewestFirst pins the ordering contract: the CMA event list
// is newest-first, so the FIRST status_idle in the page decides — a stale older
// requires_action must not resurrect after a newer end_turn idle, and vice versa.
func TestAwaitingConfirmationNewestFirst(t *testing.T) {
	cases := []struct {
		name     string
		events   string
		awaiting bool
		blocking int
	}{
		{
			name: "newer end_turn supersedes an older requires_action",
			events: `{"data":[
				{"type":"session.status_idle","stop_reason":{"type":"end_turn"}},
				{"type":"session.status_idle","stop_reason":{"type":"requires_action","event_ids":["evt_old"]}}
			]}`,
			awaiting: false,
		},
		{
			name: "newest requires_action decides over an older end_turn",
			events: `{"data":[
				{"type":"session.status_idle","stop_reason":{"type":"requires_action","event_ids":["evt_1","evt_2"]}},
				{"type":"session.status_idle","stop_reason":{"type":"end_turn"}}
			]}`,
			awaiting: true,
			blocking: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.events))
			}))
			defer srv.Close()
			s := openTestSource(t, map[string]string{cfgAPIKey: "sk-ant-test", cfgBaseURL: srv.URL})
			blocking, awaiting, err := s.cl.fetchAwaitingConfirmation(context.Background(), "sesn_o")
			if err != nil {
				t.Fatalf("fetchAwaitingConfirmation: %v", err)
			}
			if awaiting != tc.awaiting || blocking != tc.blocking {
				t.Errorf("awaiting=%v blocking=%d, want %v/%d", awaiting, blocking, tc.awaiting, tc.blocking)
			}
		})
	}
}

// TestEnrichIdledSessionWithoutRequiresAction proves an ordinary end_turn idle emits
// session state but NO HITL finding (the gate is not firing).
func TestEnrichIdledSessionWithoutRequiresAction(t *testing.T) {
	created := testTime.Add(-time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sessions/sesn_e":
			_, _ = w.Write([]byte(`{"id":"sesn_e","status":"idle","created_at":"` + created + `"}`))
		case "/v1/sessions/sesn_e/events":
			_, _ = w.Write([]byte(`{"data":[{"type":"session.status_idle","stop_reason":{"type":"end_turn"}}]}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{cfgAPIKey: "sk-ant-test", cfgBaseURL: srv.URL})
	for _, o := range s.enrichIdledSession(context.Background(), "sesn_e", testTime) {
		if f, ok := o.(model.FindingReport); ok && strings.Contains(f.Title, "awaiting human confirmation") {
			t.Errorf("end_turn idle must not raise a HITL finding: %+v", f)
		}
	}
}

// TestEnrichDegradesHonestly proves a failed GET-back yields the self-audit degrade
// observation instead of silence (or a fabricated session state).
func TestEnrichDegradesHonestly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := openTestSource(t, map[string]string{cfgAPIKey: "sk-ant-test", cfgBaseURL: srv.URL})
	obs := s.enrichIdledSession(context.Background(), "sesn_x", testTime)
	if len(obs) != 1 {
		t.Fatalf("expected exactly the degrade finding, got %+v", obs)
	}
	f, ok := obs[0].(model.FindingReport)
	if !ok || f.Kind != findingSelfAudit || f.SubjectKind != connectorSubject {
		t.Errorf("degrade finding wrong: %+v", obs[0])
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// refUUID is an external session reference that IS a UUID, so core findings
// attributed via the parseIDOrZero convention join back to it.
const refUUID = "11111111-1111-4111-8111-111111111111"

// seedCoreFinding persists a core Finding the way every producer does (SubjectID =
// the EXTERNAL session ref parsed as a UUID — modules/security/findings.go).
func seedCoreFinding(t *testing.T, st store.Store, tenant model.TenantID, ref string, sev model.Severity) {
	t.Helper()
	id, err := model.ParseID(ref)
	if err != nil {
		t.Fatalf("ref %q is not a UUID: %v", ref, err)
	}
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Findings().Create(context.Background(), model.Finding{
			Kind: "guardrail", Severity: sev, Status: model.FindingOpen, Source: "test",
			SubjectKind: "session", SubjectID: id, Title: "seed",
			OccurredAt: model.NewTimestamp(baseTime),
		})
		return err
	}); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
}

func TestSampleLiveSignals(t *testing.T) {
	t.Parallel()

	m, st, tenant, clk := newSess(t)
	ctx := context.Background()

	// Session A (UUID ref): a tool action + a cost sample, plus TWO core findings
	// attributed by the external-UUID convention (high beats medium in the max).
	if err := m.onEdge(ctx, tenant.String(), sessEdge(refUUID, "file", "/a.txt", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
		t.Fatal(err)
	}
	if err := m.onCost(ctx, tenant.String(), sdkmodel.CostSample{
		SessionRef: refUUID, ModelRef: "claude-opus-4-8", InputTokens: 100, OutputTokens: 40, CostMicroUSD: 7777,
		OccurredAt: baseTime.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	seedCoreFinding(t, st, tenant, refUUID, model.SeverityMedium)
	seedCoreFinding(t, st, tenant, refUUID, model.SeverityHigh)

	// Session B (non-UUID ref): later activity + an anti-evasion finding. Its core
	// findings are NOT attributable by id (the ref is not a UUID) ⇒ 0/"" honestly.
	if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-b", "file", "/b", sdkmodel.ModeWrite, "Edit", baseTime.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := m.onFinding(ctx, tenant.String(), sdkmodel.FindingReport{
		Kind: "anti_evasion", Severity: sdkmodel.SeverityHigh, SubjectKind: "session", SubjectRef: "sess-b",
		Title: "went silent", OccurredAt: baseTime.Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	clk.set(baseTime.Add(time.Minute))
	samples, err := m.SampleLive(ctx, tenant, LiveSampleQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	// Newest first: B's last event (the finding at +3s) is after A's (+1s).
	b, a := samples[0], samples[1]
	if b.SessionRef != "sess-b" || a.SessionRef != refUUID {
		t.Fatalf("order = %q, %q; want sess-b, %s", b.SessionRef, a.SessionRef, refUUID)
	}
	if a.CCState != ccActive {
		t.Errorf("A cc_state = %q, want %q", a.CCState, ccActive)
	}
	if a.Findings != 2 || a.MaxSeverity != string(model.SeverityHigh) {
		t.Errorf("A findings = %d/%q, want 2/high", a.Findings, a.MaxSeverity)
	}
	if a.InputTokens != 100 || a.OutputTokens != 40 || a.CostMicroUSD != 7777 {
		t.Errorf("A tokens/cost = %d/%d/%d", a.InputTokens, a.OutputTokens, a.CostMicroUSD)
	}
	if a.ModelRef != "claude-opus-4-8" {
		t.Errorf("A model_ref = %q", a.ModelRef)
	}
	if a.LastEventAt.IsZero() {
		t.Error("A last_event_at is zero")
	}
	if b.CCState != ccEvasion {
		t.Errorf("B cc_state = %q, want %q", b.CCState, ccEvasion)
	}
	if b.Findings != 0 || b.MaxSeverity != "" {
		t.Errorf("B findings = %d/%q, want 0/\"\" (non-UUID ref is not attributable)", b.Findings, b.MaxSeverity)
	}
}

func TestSampleLiveFilters(t *testing.T) {
	t.Parallel()

	m, st, tenant, clk := newSess(t)
	ctx := context.Background()

	// Three sessions: s1 attributed to agent-x, s2 to agent-y, s3 old (beyond the window).
	_ = m.onEdge(ctx, tenant.String(), sessEdge("s1", resIdentityAgent, "agent-x", sdkmodel.ModeUnknown, "", baseTime))
	_ = m.onEdge(ctx, tenant.String(), sessEdge("s2", resIdentityAgent, "agent-y", sdkmodel.ModeUnknown, "", baseTime.Add(time.Second)))
	_ = m.onEdge(ctx, tenant.String(), sessEdge("s3", "file", "/old", sdkmodel.ModeRead, "Read", baseTime.Add(-2*time.Hour)))
	_ = st // silence unused when assertions change

	clk.set(baseTime.Add(2 * time.Second))

	byAgent, err := m.SampleLive(ctx, tenant, LiveSampleQuery{AgentRef: "agent-x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].SessionRef != "s1" {
		t.Fatalf("agent filter = %+v, want only s1", byAgent)
	}

	byRef, err := m.SampleLive(ctx, tenant, LiveSampleQuery{SessionRef: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRef) != 1 || byRef[0].SessionRef != "s2" {
		t.Fatalf("session filter = %+v, want only s2", byRef)
	}

	windowed, err := m.SampleLive(ctx, tenant, LiveSampleQuery{Window: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(windowed) != 2 {
		t.Fatalf("windowed = %d sessions, want 2 (s3 is older than the window)", len(windowed))
	}
	for _, s := range windowed {
		if s.SessionRef == "s3" {
			t.Error("window did not exclude the stale session")
		}
	}

	capped, err := m.SampleLive(ctx, tenant, LiveSampleQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 1 || capped[0].SessionRef != "s2" {
		t.Fatalf("limit=1 = %+v, want only the most recent (s2)", capped)
	}
}

func TestExportFailsClosedWithoutData(t *testing.T) {
	t.Parallel()

	m := New() // no UseData
	if _, err := m.SampleLive(context.Background(), "t", LiveSampleQuery{}); err == nil {
		t.Error("SampleLive without a data handle should error")
	}
	if _, _, err := m.ReplayTimeline(context.Background(), "t", "ref", 0); err == nil {
		t.Error("ReplayTimeline without a data handle should error")
	}
}

func TestReplayTimelineOrderedActions(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()

	// tool → mcp → cost → finding → tool. Only the three ACTIONS replay, in order.
	_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/a.txt", sdkmodel.ModeRead, "Read", baseTime))
	_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "mcp.server", "github-mcp", sdkmodel.ModeReadWrite, "", baseTime.Add(time.Second)))
	_ = m.onCost(ctx, tenant.String(), sdkmodel.CostSample{SessionRef: "sess-1", InputTokens: 1, OccurredAt: baseTime.Add(2 * time.Second)})
	_ = m.onFinding(ctx, tenant.String(), sdkmodel.FindingReport{
		Kind: "guardrail", SubjectKind: "session", SubjectRef: "sess-1", Title: "x", OccurredAt: baseTime.Add(3 * time.Second),
	})
	_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "shell", "git", sdkmodel.ModeUnknown, "Bash", baseTime.Add(4*time.Second)))

	events, truncated, err := m.ReplayTimeline(ctx, tenant, "sess-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (cost/finding are not replayable)", len(events))
	}
	if events[0].Kind != tlTool || events[0].ToolRef != "Read" || events[0].ResourceRef != "/a.txt" || events[0].Mode != string(sdkmodel.ModeRead) {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Kind != tlMCP || events[1].ResourceRef != "github-mcp" {
		t.Errorf("event[1] = %+v", events[1])
	}
	if events[2].ToolRef != "Bash" || events[2].ResourceRef != "git" {
		t.Errorf("event[2] = %+v", events[2])
	}
	if events[0].At.IsZero() {
		t.Error("event[0].At is zero")
	}

	// A LATE-ARRIVING event carrying an EARLIER observed-at replays in INGESTION
	// position (the time-ordered row id), not chronological position — the order
	// contract is id ASC, and a timestamp sort would break replay stability on ties.
	_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/late-arrival", sdkmodel.ModeRead, "Read", baseTime.Add(-time.Hour)))
	withLate, _, err := m.ReplayTimeline(ctx, tenant, "sess-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(withLate) != 4 || withLate[3].ResourceRef != "/late-arrival" {
		t.Errorf("late arrival not in ingestion position: %+v", withLate)
	}

	// Unknown session: an honest empty, never an error.
	none, truncated, err := m.ReplayTimeline(ctx, tenant, "ghost", 0)
	if err != nil || truncated || len(none) != 0 {
		t.Errorf("ghost = %d events, trunc=%v, err=%v; want empty/false/nil", len(none), truncated, err)
	}
}

func TestReplayTimelineBound(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", fmt.Sprintf("/f-%d", i), sdkmodel.ModeRead, "Read", baseTime.Add(time.Duration(i)*time.Second)))
	}

	bounded, truncated, err := m.ReplayTimeline(ctx, tenant, "sess-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded) != 3 || !truncated {
		t.Fatalf("max=3 over 5 actions = %d events, trunc=%v; want 3/true", len(bounded), truncated)
	}
	if bounded[2].ResourceRef != "/f-2" {
		t.Errorf("bounded prefix out of order: %+v", bounded[2])
	}

	exact, truncated, err := m.ReplayTimeline(ctx, tenant, "sess-1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact) != 5 || truncated {
		t.Fatalf("max==len = %d events, trunc=%v; want 5/false", len(exact), truncated)
	}
}

func TestTimelineByCredential_EmptyCredential(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	ref, tl, cursor, more, err := m.TimelineByCredential(ctx, tenant, "", 100, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "" || len(tl) != 0 || cursor != "" || more {
		t.Fatalf("expected empty result for empty cred, got ref=%q tl=%d cursor=%q more=%v", ref, len(tl), cursor, more)
	}
}

func TestTimelineByCredential_NoMatchingRun(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	ref, tl, cursor, more, err := m.TimelineByCredential(ctx, tenant, "nonexistent-cred", 100, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != "" || len(tl) != 0 || cursor != "" || more {
		t.Fatalf("expected empty result for unknown cred, got ref=%q tl=%d cursor=%q more=%v", ref, len(tl), cursor, more)
	}
}

func TestTimelineByCredential_WithRun(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	const credID = "cred-abc-123"
	const sessID = "sess-xyz-456"

	// Seed a run binding the credential to the live session reference.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		row := model.Record{
			colRunRef:          "run-1",
			colTransport:       "http",
			colPermissionMode:  "default",
			colIsolation:       "none",
			colState:           "running",
			colLastEventSeq:    int64(0),
			colCredentialID:    credID,
			colClaudeSessionID: sessID,
		}
		_, err = repo.Create(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// Seed two timeline events for the live session.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := m.appendTimeline(ctx, sc, sessID, baseTime, tlTool, "Read", "/a.txt", "read", "otel", ""); err != nil {
			return err
		}
		return m.appendTimeline(ctx, sc, sessID, baseTime.Add(time.Second), tlMCP, "", "github-mcp", "rw", "otel", "")
	}); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}

	ref, tl, cursor, more, err := m.TimelineByCredential(ctx, tenant, credID, 100, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref != sessID {
		t.Fatalf("sessionRef = %q, want %q", ref, sessID)
	}
	if len(tl) != 2 {
		t.Fatalf("timeline len = %d, want 2", len(tl))
	}
	if cursor != "" || more {
		t.Errorf("unexpected continuation cursor=%q more=%v", cursor, more)
	}
	if tl[0].Kind != tlTool || tl[0].ToolRef != "Read" || tl[0].ResourceRef != "/a.txt" {
		t.Errorf("tl[0] = %+v", tl[0])
	}
	if tl[0].At.IsZero() {
		t.Error("tl[0].At is zero")
	}
	if tl[1].Kind != tlMCP || tl[1].ResourceRef != "github-mcp" {
		t.Errorf("tl[1] = %+v", tl[1])
	}
}

func TestTimelineByCredential_KeysetPagination(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	const (
		credID = "cred-paged"
		sessID = "sess-paged"
	)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		runRepo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		if _, err := runRepo.Create(ctx, model.Record{
			colRunRef:          "run-paged",
			colTransport:       "http",
			colPermissionMode:  "default",
			colIsolation:       "none",
			colState:           "running",
			colLastEventSeq:    int64(0),
			colCredentialID:    credID,
			colClaudeSessionID: sessID,
		}); err != nil {
			return err
		}
		for i := 1; i <= 3; i++ {
			if err := m.appendTimeline(ctx, sc, sessID, baseTime.Add(time.Duration(i)*time.Second),
				tlTool, "Read", fmt.Sprintf("/page-%d", i), "read", "otel", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed paged timeline: %v", err)
	}

	ref, first, cursor, more, err := m.TimelineByCredential(ctx, tenant, credID, 2, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if ref != sessID || len(first) != 2 || first[0].ResourceRef != "/page-1" || first[1].ResourceRef != "/page-2" {
		t.Fatalf("first page = ref %q entries %+v", ref, first)
	}
	if cursor == "" || !more {
		t.Fatalf("first page cursor=%q more=%v, want continuation", cursor, more)
	}

	// Append after obtaining page one. The ID keyset keeps the continuation
	// anchored after /page-2, so the new tail cannot shift or duplicate page one.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return m.appendTimeline(ctx, sc, sessID, baseTime.Add(4*time.Second),
			tlTool, "Edit", "/page-4", "write", "otel", "")
	}); err != nil {
		t.Fatalf("append between pages: %v", err)
	}

	ref, second, next, more, err := m.TimelineByCredential(ctx, tenant, credID, 2, cursor)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if ref != sessID || len(second) != 2 || second[0].ResourceRef != "/page-3" || second[1].ResourceRef != "/page-4" {
		t.Fatalf("second page = ref %q entries %+v", ref, second)
	}
	if next != "" || more {
		t.Fatalf("second page cursor=%q more=%v, want terminal page", next, more)
	}
	for _, left := range first {
		for _, right := range second {
			if left.ResourceRef == right.ResourceRef {
				t.Fatalf("timeline pages overlap at %q", left.ResourceRef)
			}
		}
	}
}

// TestReplayTimelineDrainsPages proves the cursor drain crosses the store's SILENT
// 1000-row page clamp: a 1050-action session reconstructs completely and in order.
func TestReplayTimelineDrainsPages(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()
	const n = 1050
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			at := baseTime.Add(time.Duration(i) * time.Millisecond)
			if err := m.appendTimeline(ctx, sc, "big", at, tlTool, "Read", fmt.Sprintf("/f-%04d", i), "read", "otel", ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	events, truncated, err := m.ReplayTimeline(ctx, tenant, "big", 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	if len(events) != n {
		t.Fatalf("events = %d, want %d (the store page clamp must not truncate)", len(events), n)
	}
	if events[0].ResourceRef != "/f-0000" || events[n-1].ResourceRef != fmt.Sprintf("/f-%04d", n-1) {
		t.Errorf("drain order broken: first=%q last=%q", events[0].ResourceRef, events[n-1].ResourceRef)
	}
}

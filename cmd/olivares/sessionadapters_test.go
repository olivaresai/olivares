// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/sessions"
)

// newSessionsStore provisions a REAL sessions.Module over an in-memory store (the
// budgetgate_test.go idiom): the adapters under test are the PRODUCTION ones, the
// data flows through the module's actual read path.
func newSessionsStore(t *testing.T) (*sessions.Module, store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	ss := sessions.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, ss.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	ss.UseData(api.NewModuleData(st))
	return ss, st, tenant
}

// seedTimeline inserts sessions.timeline rows directly (the module's writers are bus
// handlers, covered by the module's own tests; here we drive the production READ
// path through the adapter deterministically).
func seedTimeline(t *testing.T, st store.Store, tenant model.TenantID, ref string, rows []model.Record) {
	t.Helper()
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.timeline")
		if err != nil {
			return err
		}
		for i, rec := range rows {
			rec["session_ref"] = ref
			rec["at"] = model.NewTimestamp(base.Add(time.Duration(i) * time.Second)).String()
			if _, err := repo.Create(context.Background(), rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}
}

func TestSessionsHistoryAdapterBuildsOrderedSteps(t *testing.T) {
	ss, st, tenant := newSessionsStore(t)
	rows := make([]model.Record, 0, 15)
	// Eleven tool actions (>10 proves zero-padded ordering), one with no resource.
	for i := 0; i < 10; i++ {
		rows = append(rows, model.Record{"kind": "tool", "tool_ref": "Read", "resource_ref": fmt.Sprintf("/f-%02d", i+1)})
	}
	rows = append(rows, model.Record{"kind": "tool", "tool_ref": "Bash"}) // no resource ⇒ input = tool
	rows = append(rows, model.Record{"kind": "mcp", "resource_ref": "github-mcp"})
	rows = append(rows, model.Record{"kind": "cost"})                  // telemetry, not an input
	rows = append(rows, model.Record{"kind": "finding", "title": "x"}) // telemetry, not an input
	seedTimeline(t, st, tenant, "sess-1", rows)

	steps, err := sessionsHistoryAdapter{ss: ss}.Timeline(context.Background(), tenant, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 12 {
		t.Fatalf("steps = %d, want 12 (telemetry rows must not replay)", len(steps))
	}
	keys := make([]string, 0, len(steps))
	for _, s := range steps {
		keys = append(keys, s.Key)
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("keys do not sort in execution order: %v", keys)
	}
	if steps[0].Key != "00001 Read" || steps[0].Input != "/f-01" {
		t.Errorf("step[0] = %+v, want 00001 Read / /f-01", steps[0])
	}
	if steps[10].Key != "00011 Bash" || steps[10].Input != "Bash" {
		t.Errorf("step[10] = %+v, want the tool itself as input when no resource", steps[10])
	}
	if steps[11].Key != "00012 mcp" || steps[11].Input != "github-mcp" {
		t.Errorf("step[11] = %+v, want the kind label and the mcp resource", steps[11])
	}

	// Unknown session: an honest empty, never an error, never fabricated steps.
	none, err := sessionsHistoryAdapter{ss: ss}.Timeline(context.Background(), tenant, "ghost")
	if err != nil || len(none) != 0 {
		t.Errorf("ghost = %d steps, err=%v; want 0/nil", len(none), err)
	}
}

func TestSessionsHistoryAdapterRefusesPartialReplay(t *testing.T) {
	ss, st, tenant := newSessionsStore(t)
	// The bound is injected (max) so the refusal branch is provable with 6 rows
	// instead of a 10001-row seed; the bound/truncation mechanics themselves are
	// covered in modules/sessions (TestReplayTimelineBound).
	rows := make([]model.Record, 0, 6)
	for i := 0; i < 6; i++ {
		rows = append(rows, model.Record{"kind": "tool", "tool_ref": "Read", "resource_ref": fmt.Sprintf("/f-%05d", i)})
	}
	seedTimeline(t, st, tenant, "huge", rows)

	_, err := sessionsHistoryAdapter{ss: ss, max: 5}.Timeline(context.Background(), tenant, "huge")
	if err == nil || !strings.Contains(err.Error(), "refusing a partial replay") {
		t.Fatalf("err = %v, want an explicit partial-replay refusal", err)
	}

	// At the default bound the same session replays fine.
	steps, err := sessionsHistoryAdapter{ss: ss}.Timeline(context.Background(), tenant, "huge")
	if err != nil || len(steps) != 6 {
		t.Fatalf("default bound = %d steps, err=%v; want 6/nil", len(steps), err)
	}
}

func TestSessionsSampleAdapterMapsLiveSignals(t *testing.T) {
	ss, st, tenant := newSessionsStore(t)
	now := time.Now().UTC()
	const refUUID = "11111111-1111-4111-8111-111111111111"

	// Two live rows seeded directly: a recent session (UUID ref, attributed core
	// finding) and a stale one attributed to another agent.
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.live")
		if err != nil {
			return err
		}
		mk := func(ref, agent string, last time.Time, tokens int64) model.Record {
			return model.Record{
				"session_ref": ref, "agent_ref": agent, "model_ref": "claude-opus-4-8",
				"input_tokens": tokens, "output_tokens": tokens / 2, "cost_micro_usd": int64(42),
				"event_count": int64(3), "tool_call_count": int64(2),
				"first_event_at": model.NewTimestamp(last.Add(-time.Minute)).String(),
				"last_event_at":  model.NewTimestamp(last).String(),
			}
		}
		if _, err := repo.Create(context.Background(), mk(refUUID, "agent-x", now, 100)); err != nil {
			return err
		}
		if _, err := repo.Create(context.Background(), mk("sess-old", "agent-y", now.Add(-2*time.Hour), 10)); err != nil {
			return err
		}
		// The canonical core finding, attributed the way every producer does:
		// SubjectID = the EXTERNAL session ref parsed as a UUID.
		id, err := model.ParseID(refUUID)
		if err != nil {
			return err
		}
		_, err = sc.Findings().Create(context.Background(), model.Finding{
			Kind: "guardrail", Severity: model.SeverityHigh, Status: model.FindingOpen, Source: "test",
			SubjectKind: "session", SubjectID: id, Title: "seed", OccurredAt: model.NewTimestamp(now),
		})
		return err
	}); err != nil {
		t.Fatalf("seed live rows: %v", err)
	}

	a := sessionsSampleAdapter{ss: ss, window: 0, log: slog.Default()}
	samples, err := a.Sample(context.Background(), tenant, evals.SampleQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(samples))
	}
	recent := samples[0] // newest first
	if recent.SessionRef != refUUID {
		t.Fatalf("order: first = %q, want the most recent (%s)", recent.SessionRef, refUUID)
	}
	// The sessions clock is the real wall clock (no option to inject from outside
	// the package), so assert the in-flight band (active|idle = within 30min)
	// rather than racing the 2-minute active window on a stalled runner.
	if recent.State != "active" && recent.State != "idle" {
		t.Errorf("recent state = %q, want active|idle (derived now)", recent.State)
	}
	if recent.Findings != 1 || recent.MaxSeverity != "high" {
		t.Errorf("recent findings = %d/%q, want 1/high (core join by external-UUID ref)", recent.Findings, recent.MaxSeverity)
	}
	if recent.InputTokens != 100 || recent.OutputTokens != 50 || recent.CostMicroUSD != 42 {
		t.Errorf("recent tokens/cost = %d/%d/%d", recent.InputTokens, recent.OutputTokens, recent.CostMicroUSD)
	}
	if recent.AgentRef != "agent-x" || recent.ModelRef != "claude-opus-4-8" {
		t.Errorf("recent attribution = %q/%q", recent.AgentRef, recent.ModelRef)
	}
	if samples[1].State != "ended" {
		t.Errorf("stale state = %q, want ended", samples[1].State)
	}

	// The short window excludes the stale session (the freshness posture).
	windowed := sessionsSampleAdapter{ss: ss, window: time.Hour, log: slog.Default()}
	ws, err := windowed.Sample(context.Background(), tenant, evals.SampleQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].SessionRef != refUUID {
		t.Fatalf("windowed = %+v, want only the recent session", ws)
	}

	// An exact single-session probe is NOT window-bounded: naming one subject and
	// silently hiding it for being old would read as "does not exist".
	probe, err := windowed.Sample(context.Background(), tenant, evals.SampleQuery{SubjectKind: "session", SubjectRef: "sess-old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probe) != 1 || probe[0].SessionRef != "sess-old" {
		t.Fatalf("session probe = %+v, want the old session despite the window", probe)
	}

	// Subject filters narrow by module II's own refs.
	byAgent, err := a.Sample(context.Background(), tenant, evals.SampleQuery{SubjectKind: "agent", SubjectRef: "agent-y", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].SessionRef != "sess-old" {
		t.Fatalf("agent filter = %+v, want only sess-old", byAgent)
	}

	// An unsampleable subject kind narrows to the honest empty, never to everything.
	none, err := a.Sample(context.Background(), tenant, evals.SampleQuery{SubjectKind: "prompt", SubjectRef: "p1"})
	if err != nil || len(none) != 0 {
		t.Errorf("unsupported kind = %d samples, err=%v; want 0/nil", len(none), err)
	}
}

func TestLiveQueryFor(t *testing.T) {
	for _, tc := range []struct {
		kind, ref string
		ok        bool
		check     func(sessions.LiveSampleQuery) bool
	}{
		{"", "", true, func(q sessions.LiveSampleQuery) bool {
			return q == sessions.LiveSampleQuery{Window: time.Hour, Limit: 7}
		}},
		{"agent", "a1", true, func(q sessions.LiveSampleQuery) bool { return q.AgentRef == "a1" && q.Window == time.Hour }},
		{"model", "m1", true, func(q sessions.LiveSampleQuery) bool { return q.ModelRef == "m1" && q.Window == time.Hour }},
		// An exact session probe clears the window (explicit lookup, not a sample).
		{"session", "s1", true, func(q sessions.LiveSampleQuery) bool { return q.SessionRef == "s1" && q.Window == 0 }},
		{"agent", "  ", true, func(q sessions.LiveSampleQuery) bool { return q.AgentRef == "" }}, // empty ref ⇒ unfiltered
		{"prompt", "p1", false, nil},
	} {
		q, ok := liveQueryFor(evals.SampleQuery{SubjectKind: tc.kind, SubjectRef: tc.ref, Limit: 7}, time.Hour)
		if ok != tc.ok {
			t.Errorf("liveQueryFor(%q) ok = %v, want %v", tc.kind, ok, tc.ok)
			continue
		}
		if tc.check != nil && !tc.check(q) {
			t.Errorf("liveQueryFor(%q) = %+v", tc.kind, q)
		}
	}
}

func TestLoadEvalsMonitorWindow(t *testing.T) {
	log := slog.Default()
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultEvalsMonitorWindow},
		{"1h", time.Hour},
		{"0", 0}, // explicit: no recency bound
		{"bogus", defaultEvalsMonitorWindow},
		{"-5m", defaultEvalsMonitorWindow},
	} {
		getenv := func(string) string { return tc.raw }
		if got := loadEvalsMonitorWindow(getenv, log); got != tc.want {
			t.Errorf("window(%q) = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

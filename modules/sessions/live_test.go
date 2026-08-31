// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

var baseTime = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

// testClock is a settable clock so the read-time Claude Code state derivation can
// be exercised deterministically.
//
// It is MUTEX-GUARDED on purpose. The module hands this clock to production code that
// reads it from goroutines the test does not join — createRun's bridge calls
// Module.now() on its own goroutine — so a plain field written by the test body is a
// real data race, and the detector reported it on main (race-modules, job 91952955366:
// Now() at live_test.go:24 racing the write at runtime_test.go:478). Advance the clock
// through set/advance; never touch the field.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.now)
}

// get returns the current fake time.
func (c *testClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// set moves the fake clock to an absolute instant.
func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// advance moves the fake clock by d, which may be negative.
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newSess(t *testing.T) (*Module, store.Store, model.TenantID, *testClock) {
	t.Helper()
	m := New()
	clk := &testClock{now: baseTime}
	m.clock = clk
	ctx := context.Background()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open: %v", err)
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
		t.Fatalf("tenant: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	return m, st, tenant, clk
}

func sessEdge(ref, resKind, resRef string, mode sdkmodel.AccessMode, tool string, at time.Time) sdkmodel.EdgeObservation {
	return sdkmodel.EdgeObservation{
		OriginKind: "session", OriginRef: ref, ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Source: sdkmodel.SignalOTEL, Confidence: sdkmodel.ConfidenceAttributed,
		ToolRef: tool, ObservedAt: at,
	}
}

func getLive(t *testing.T, m *Module, st store.Store, tenant model.TenantID, ref string) (model.Record, bool) {
	t.Helper()
	var rec model.Record
	found := false
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colSessionRef, ref)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) > 0 {
			rec, found = recs[0], true
		}
		return nil
	}); err != nil {
		t.Fatalf("getLive: %v", err)
	}
	return rec, found
}

func TestLiveOperation(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()

	// Two tool uses, then a cost sample, then an anti-evasion finding.
	if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/a.txt", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
		t.Fatal(err)
	}
	if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "shell", "git", sdkmodel.ModeUnknown, "Bash", baseTime.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := m.onCost(ctx, tenant.String(), sdkmodel.CostSample{
		SessionRef: "sess-1", ModelRef: "claude-opus-4-8", InputTokens: 100, OutputTokens: 40, CostMicroUSD: 7777, OccurredAt: baseTime.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	rec, ok := getLive(t, m, st, tenant, "sess-1")
	if !ok {
		t.Fatal("no live row")
	}
	dto := m.toLiveDTO(rec)
	if dto.EventCount != 2 {
		t.Errorf("event_count = %d, want 2", dto.EventCount)
	}
	if dto.ToolCallCount != 2 {
		t.Errorf("tool_call_count = %d, want 2", dto.ToolCallCount)
	}
	if dto.CurrentAction != "Bash" {
		t.Errorf("current_action = %q, want Bash", dto.CurrentAction)
	}
	if dto.InputTokens != 100 || dto.OutputTokens != 40 || dto.CostMicroUSD != 7777 {
		t.Errorf("tokens/cost = %d/%d/%d", dto.InputTokens, dto.OutputTokens, dto.CostMicroUSD)
	}
	if dto.ModelRef != "claude-opus-4-8" {
		t.Errorf("model_ref = %q", dto.ModelRef)
	}

	// Timeline recorded every event in order (2 tools + 1 cost).
	tl := timelineOf(t, st, tenant, "sess-1")
	if len(tl) != 3 {
		t.Fatalf("timeline len = %d, want 3", len(tl))
	}
	if tl[0].String(colTLKind) != tlTool || tl[2].String(colTLKind) != tlCost {
		t.Errorf("timeline order wrong: %v / %v", tl[0].String(colTLKind), tl[2].String(colTLKind))
	}
}

func TestAntiEvasionMarksState(t *testing.T) {
	t.Parallel()

	m, st, tenant, clk := newSess(t)
	ctx := context.Background()
	_ = m.onEdge(ctx, tenant.String(), sessEdge("sess-1", "file", "/a", sdkmodel.ModeRead, "Read", baseTime))
	if err := m.onFinding(ctx, tenant.String(), sdkmodel.FindingReport{
		Kind: "anti_evasion", Severity: sdkmodel.SeverityHigh, SubjectKind: "session", SubjectRef: "sess-1",
		Title: "OTEL went silent while hooks fired", OccurredAt: baseTime.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	clk.set(baseTime.Add(2 * time.Minute))
	rec, _ := getLive(t, m, st, tenant, "sess-1")
	if got := m.deriveCC(rec); got != ccEvasion {
		t.Errorf("cc_state = %q, want %q", got, ccEvasion)
	}
}

func TestCCStateDerivation(t *testing.T) {
	t.Parallel()

	m, st, tenant, clk := newSess(t)
	_ = m.onEdge(context.Background(), tenant.String(), sessEdge("sess-1", "file", "/a", sdkmodel.ModeRead, "Read", baseTime))
	rec, _ := getLive(t, m, st, tenant, "sess-1")

	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{baseTime.Add(30 * time.Second), ccActive},
		{baseTime.Add(10 * time.Minute), ccIdle},
		{baseTime.Add(2 * time.Hour), ccEnded},
	} {
		clk.set(tc.at)
		if got := m.deriveCC(rec); got != tc.want {
			t.Errorf("at +%s cc_state = %q, want %q", tc.at.Sub(baseTime), got, tc.want)
		}
	}
}

func TestLiveIgnoresNonSessionOrigin(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	// An agent-origin edge is inventory's concern, not a live session.
	edge := sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "ci-bot", ResourceKind: "file", ResourceRef: "/x",
		Mode: sdkmodel.ModeWrite, Source: sdkmodel.SignalEBPF, ObservedAt: baseTime,
	}
	if err := m.onEdge(context.Background(), tenant.String(), edge); err != nil {
		t.Fatal(err)
	}
	if _, ok := getLive(t, m, st, tenant, "ci-bot"); ok {
		t.Error("an agent-origin edge created a live session row")
	}
}

func TestLiveIdempotentUpsert(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newSess(t)
	ctx := context.Background()
	e := sessEdge("sess-1", "file", "/a", sdkmodel.ModeRead, "Read", baseTime)
	_ = m.onEdge(ctx, tenant.String(), e)
	_ = m.onEdge(ctx, tenant.String(), e)
	// Exactly one live row, two counted events.
	n := 0
	_ = st.View(ctx, tenant, func(sc store.Scope) error {
		repo, _ := sc.Ext(liveKind)
		recs, _, err := repo.List(ctx, model.Query{Limit: 100})
		n = len(recs)
		return err
	})
	if n != 1 {
		t.Fatalf("live rows = %d, want 1", n)
	}
	rec, _ := getLive(t, m, st, tenant, "sess-1")
	if got := m.toLiveDTO(rec).EventCount; got != 2 {
		t.Errorf("event_count = %d, want 2", got)
	}
}

func timelineOf(t *testing.T, st store.Store, tenant model.TenantID, ref string) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(timelineKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colTLSessionRef, ref)}, Limit: 1000})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("timelineOf: %v", err)
	}
	return out
}

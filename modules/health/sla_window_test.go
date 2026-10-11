// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CUTS A2 (2026-10-02): the SLA paths read the trailing window plus ONE anchor
// event, not the subject's lifetime. These tests pin that the answer is
// identical, at the boundaries the window read could get wrong.

// seedEvents writes n transition events for the subject, spaced by step, ending
// at end (the last event carries endState). Direct store writes, one transaction.
func seedEvents(t testing.TB, h *harness, tenant model.TenantID, subjectRef string, n int, step time.Duration, end time.Time, endState string) {
	t.Helper()
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			state := "healthy"
			if i == n-1 {
				state = endState
			} else if i%2 == 1 {
				state = "down"
			}
			at := end.Add(-time.Duration(n-1-i) * step)
			if _, err := repo.Create(context.Background(), model.Record{
				colEvSubjectKind: "mcp", colEvSubjectRef: subjectRef, colEvState: state,
				colEvPrevState: "healthy", colEvLatency: int64(12), colEvCause: "report",
				colEvOccurredAt: model.NewTimestamp(at).String(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed %d events: %v", n, err)
	}
}

// The anchor: a subject down BEFORE the window with no events inside it must
// read as down for the whole window — without the anchor read that history is
// invisible and the fold would answer "no data".
func TestSLAWindowReadsTheAnchor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{
		"subject_kind": "mcp", "subject_ref": "anchor.mcp", "sla_target_ppm": 999000,
	})
	if cr.code != http.StatusCreated {
		t.Fatalf("create check = %d %s", cr.code, cr.raw)
	}

	// One hour of alternating history ending down, ALL of it before the window.
	now := h.clk.Now().Time()
	seedEvents(t, h, tenant, "anchor.mcp", 60, time.Minute, now.Add(-2*time.Hour), "down")

	// The default window is one hour: nothing happened inside it.
	sla := h.do("GET", "/v1/m/health/sla?subject_kind=mcp&subject_ref=anchor.mcp&window_seconds=3600", editor, nil, tenantHdr(tenant))
	if sla.code != http.StatusOK {
		t.Fatalf("sla = %d %s", sla.code, sla.raw)
	}
	if got := intOf(sla.body["downtime_seconds"]); got != 3600 {
		t.Errorf("the pre-window down state must carry through the anchor: downtime = %d, want 3600 (%s)", got, sla.raw)
	}
	if got := intOf(sla.body["uptime_ppm"]); got != 0 {
		t.Errorf("uptime_ppm = %d, want 0 (down all window)", got)
	}
	if !boolOf(sla.body["breaching"]) {
		t.Errorf("down for the whole window must breach; %s", sla.raw)
	}
}

// The window read and the lifetime fold must agree exactly: the same history is
// folded from the full set (the old read) and from window+anchor (the new one).
func TestSLAWindowReadMatchesLifetimeFold(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	now := h.clk.Now().Time()
	windowStart := now.Add(-time.Hour)
	// 3 hours of alternating history: 2 hours before the window, 1 inside.
	seedEvents(t, h, tenant, "fold.mcp", 180, time.Minute, now, "degraded")

	var got reliability
	var want reliability
	h.view(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		lifetime, err := listAll(context.Background(), repo, eq(colEvSubjectKind, "mcp"), eq(colEvSubjectRef, "fold.mcp"))
		if err != nil {
			return err
		}
		want = reliabilityFromEvents(lifetime, windowStart, now)
		windowed, err := slaEventsWindow(context.Background(), repo, "mcp", "fold.mcp", windowStart)
		if err != nil {
			return err
		}
		got = reliabilityFromEvents(windowed, windowStart, now)
		if len(windowed) >= len(lifetime) {
			t.Fatalf("the window read must drop the pre-window bulk: %d rows >= lifetime %d", len(windowed), len(lifetime))
		}
		return nil
	})
	if got != want {
		t.Errorf("window+anchor fold = %+v, lifetime fold = %+v — must be identical", got, want)
	}
}

// The A2 measurement: the SLA fold over a subject with 5,000 pre-window events,
// the lifetime read (the old path, unchanged code) against the window+anchor read
// (the new one), same seed, same run — the before/after pair in the commit body.
func benchStore(b *testing.B) (store.Store, model.TenantID) {
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, m.RegisterSchema)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "bench", Slug: "bench", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		b.Fatalf("provision: %v", err)
	}
	return st, tenant
}

func seedEventsB(b *testing.B, st store.Store, tenant model.TenantID, n int, step time.Duration, end time.Time) {
	ctx := context.Background()
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			state := "healthy"
			if i%2 == 1 {
				state = "down"
			}
			at := end.Add(-time.Duration(n-1-i) * step)
			if _, err := repo.Create(ctx, model.Record{
				colEvSubjectKind: "mcp", colEvSubjectRef: "bench.mcp", colEvState: state,
				colEvPrevState: "healthy", colEvLatency: int64(12), colEvCause: "report",
				colEvOccurredAt: model.NewTimestamp(at).String(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed %d events: %v", n, err)
	}
}

func BenchmarkSLAReadLifetime(b *testing.B) {
	st, tenant := benchStore(b)
	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	seedEventsB(b, st, tenant, 5000, time.Minute, now.Add(-90*time.Minute))
	seedEventsB(b, st, tenant, 5, time.Minute, now)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(eventKind)
			if err != nil {
				return err
			}
			events, err := listAll(ctx, repo, eq(colEvSubjectKind, "mcp"), eq(colEvSubjectRef, "bench.mcp"))
			if err != nil {
				return err
			}
			_ = reliabilityFromEvents(events, windowStart, now)
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSLAReadWindow(b *testing.B) {
	st, tenant := benchStore(b)
	now := time.Now().UTC()
	windowStart := now.Add(-time.Hour)
	seedEventsB(b, st, tenant, 5000, time.Minute, now.Add(-90*time.Minute))
	seedEventsB(b, st, tenant, 5, time.Minute, now)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(eventKind)
			if err != nil {
				return err
			}
			events, err := slaEventsWindow(ctx, repo, "mcp", "bench.mcp", windowStart)
			if err != nil {
				return err
			}
			_ = reliabilityFromEvents(events, windowStart, now)
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// SR5C (2026-10-03): the anchor is THE latest event before the window, and an
// occurred_at tie breaks by id DESC — the winner the lifetime fold reaches
// (id-ascending, last write wins). Two same-timestamp pre-window events with
// different states: the window read must follow the higher id, not whichever
// the sort happened to return first.
func TestSLAWindowAnchorTieBreaksByIDDesc(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{
		"subject_kind": "mcp", "subject_ref": "tie.mcp", "sla_target_ppm": 999000,
	})
	if cr.code != http.StatusCreated {
		t.Fatalf("create check = %d %s", cr.code, cr.raw)
	}

	// Two events at the SAME pre-window instant: healthy first, down second.
	now := h.clk.Now().Time()
	at := now.Add(-2 * time.Hour)
	err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		for _, state := range []string{"healthy", "down"} {
			if _, err := repo.Create(context.Background(), model.Record{
				colEvSubjectKind: "mcp", colEvSubjectRef: "tie.mcp", colEvState: state,
				colEvPrevState: "healthy", colEvLatency: int64(12), colEvCause: "report",
				colEvOccurredAt: model.NewTimestamp(at).String(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed tie events: %v", err)
	}

	// The expected winner: the last event in the store's own id order (that is
	// the fold's last-write-wins).
	var expected string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEvSubjectRef, "tie.mcp")},
			Sort:    []model.Sort{{Column: model.ColID}},
			Limit:   10,
		})
		if err != nil {
			return err
		}
		if len(rows) != 2 {
			t.Fatalf("seeded %d events, want 2", len(rows))
		}
		expected = rows[len(rows)-1][colEvState].(string)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sla := h.do("GET", "/v1/m/health/sla?subject_kind=mcp&subject_ref=tie.mcp&window_seconds=3600", editor, nil, tenantHdr(tenant))
	if sla.code != http.StatusOK {
		t.Fatalf("sla = %d %s", sla.code, sla.raw)
	}
	if expected == "down" {
		if got := intOf(sla.body["downtime_seconds"]); got != 3600 {
			t.Fatalf("the tie's winner is down (higher id): downtime = %d, want 3600 (%s)", got, sla.raw)
		}
	} else if got := intOf(sla.body["downtime_seconds"]); got != 0 {
		t.Fatalf("the tie's winner is healthy (higher id): downtime = %d, want 0 (%s)", got, sla.raw)
	}
}

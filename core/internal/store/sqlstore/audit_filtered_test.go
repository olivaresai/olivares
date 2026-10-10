// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// tickClock steps the store clock one minute per sealed event, so a seeded
// ledger has a known occurred_at per event (the store stamps it at append).
type tickClock struct{ t time.Time }

func (c *tickClock) Now() model.Timestamp    { return model.NewTimestamp(c.t) }
func (c *tickClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// CUTS A3 (2026-10-02): the filtered audit list runs its filter in SQL through
// store.FilteredWalker instead of decoding every ledger row and matching in Go
// (AU2-05 measured 63.7 ms for a needle filter; the SQL model is 4.5 ms). These
// tests pin that the store filter returns exactly the rows the handler's Go
// rules select.

// auditTestSeed seals n events with deterministic variety: two action families,
// two actors, rotating target kinds, one event whose prefix contains a LIKE
// metacharacter, and timestamps one minute apart. Returns the events in order.
func auditTestSeed(t testing.TB, st store.Store, clk *tickClock, tenant model.TenantID, n int) []model.AuditEvent {
	const multibyteAction = "café.deploy"          // language-data: multibyte audit action prefix input
	const uppercaseAction = "deploy.ÉXÉ.started"   // language-data: Unicode audit case folding input
	const multibyteActor = "user:cérebro"          // language-data: multibyte audit actor input
	const lowercaseAction = "deploy.éxé.completed" // language-data: Unicode audit case folding input
	t.Helper()
	var out []model.AuditEvent
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			action := "session.started"
			actor := "user:alice"
			if i%3 == 1 {
				action = "policy.updated"
				actor = "user:bob"
			}
			if i%7 == 6 {
				action = "session_100%.restarted" // the LIKE-metacharacter prefix
			}
			// SR5C: non-ASCII rows — the contract's filters are Go rules, and
			// audit fields are NOT ASCII by construction.
			if i%11 == 10 {
				action = multibyteAction // a multibyte prefix family
			}
			if i%13 == 12 {
				action = uppercaseAction
				actor = multibyteActor
			}
			if i%17 == 16 {
				action = lowercaseAction // the fold target for q
			}
			ev, err := sc.Audit().Append(context.Background(), model.AuditDraft{
				Actor: actor, Action: action,
				TargetKind: model.Kind("core.session"), TargetID: model.ID(fmt.Sprintf("t-%04d", i%5)),
			})
			if err != nil {
				return err
			}
			out = append(out, ev)
			clk.advance(time.Minute)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed %d events: %v", n, err)
	}
	return out
}

// auditGoFilter is the reference: the handler's auditFilters.matches() rules
// restated at their plainest. Both sides of the equality are reviewed line by
// line; the golden handler tests (written against the legacy walk) pin the same
// answers end to end.
func auditGoFilter(ev model.AuditEvent, f store.AuditFilter) bool {
	if f.Actor != "" && ev.Actor != f.Actor {
		return false
	}
	if f.ActionPrefix != "" && !strings.HasPrefix(ev.Action, f.ActionPrefix) {
		return false
	}
	for _, excluded := range f.ExcludeActionPrefixes {
		if strings.HasPrefix(ev.Action, excluded) {
			return false
		}
	}
	if f.TargetKind != "" && string(ev.TargetKind) != f.TargetKind {
		return false
	}
	// The handler renders a zero TargetID as "" (idOrEmpty) — the reference
	// mirrors it exactly, for the exact match and for q.
	targetID := ""
	if !ev.TargetID.IsZero() {
		targetID = ev.TargetID.String()
	}
	if f.TargetID != "" && targetID != f.TargetID {
		return false
	}
	if f.Since != nil || f.Until != nil {
		at := ev.OccurredAt.Time()
		if f.Since != nil && at.Before(*f.Since) {
			return false
		}
		if f.Until != nil && at.After(*f.Until) {
			return false
		}
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.Contains(strings.ToLower(ev.Action), q) &&
			!strings.Contains(strings.ToLower(ev.Actor), q) &&
			!strings.Contains(strings.ToLower(string(ev.TargetKind)), q) &&
			!strings.Contains(strings.ToLower(targetID), q) {
			return false
		}
	}
	return true
}

func TestAuditFilteredWalkMatchesTheGoFilter(t *testing.T) {
	clk := &tickClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Clock: clk}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	auditFilteredEqualitySuite(t, st, clk)
}

// SR5C: the same equality on PostgreSQL 16 — a SQLite-only green hid the instr
// call (no such function on PG) and the ASCII-only lower() fold.
func TestAuditFilteredWalkMatchesTheGoFilterPostgres(t *testing.T) {
	clk := &tickClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	pg := isolatedPG(t)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, Clock: clk,
	}, nil)
	if err != nil {
		t.Fatalf("open PG store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	auditFilteredEqualitySuite(t, st, clk)
}

func auditFilteredEqualitySuite(t *testing.T, st store.Store, clk *tickClock) {
	const multibytePrefix = "café." // language-data: multibyte audit action prefix filter
	const uppercaseQuery = "ÉXÉ"    // language-data: Unicode audit case folding filter
	const lowercaseQuery = "éxé"    // language-data: Unicode audit case folding filter
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: "af", Slug: "af", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	base := clk.t
	auditTestSeed(t, st, clk, tenant, 120)

	since := base.Add(50 * time.Minute)
	until := base.Add(100 * time.Minute)
	filters := map[string]store.AuditFilter{
		"actor exact":                {Actor: "user:bob"},
		"action prefix":              {ActionPrefix: "session."},
		"prefix with metacharacters": {ActionPrefix: "session_100%"},
		"prefix never matched":       {ActionPrefix: "nothing."},
		"exclude a family":           {ExcludeActionPrefixes: []string{"policy."}},
		"exclude two":                {ExcludeActionPrefixes: []string{"policy.", "session."}},
		"target kind":                {TargetKind: "core.session"},
		"target id":                  {TargetID: "t-0002"},
		"q lowercase hit":            {Q: "bob"},
		"q uppercase folded":         {Q: "POLICY.UPD"},
		"q target id":                {Q: "t-0003"},
		"q misses everything":        {Q: "zzz-absent"},
		"prefix multibyte":           {ActionPrefix: multibytePrefix},
		"exclude multibyte":          {ExcludeActionPrefixes: []string{multibytePrefix}},
		"q unicode uppercase folds":  {Q: uppercaseQuery},
		"q unicode lowercase":        {Q: lowercaseQuery},
		"since only":                 {Since: &since},
		"until only":                 {Until: &until},
		"window":                     {Since: &since, Until: &until},
		"combined":                   {Actor: "user:alice", ActionPrefix: "session.", Q: "session", Since: &since},
	}
	for name, f := range filters {
		t.Run(name, func(t *testing.T) {
			// The reference is the legacy read path EXACTLY: Walk the whole ledger
			// (the seeded rows AND any pre-seed row, like the org-creation event)
			// and apply the handler's rules in Go.
			var want []int64
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
					if auditGoFilter(ev, f) {
						want = append(want, ev.Seq)
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			var got []int64
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				fw, ok := sc.Audit().(store.FilteredWalker)
				if !ok {
					t.Fatal("the sqlstore audit log must offer FilteredWalker")
				}
				return fw.WalkFiltered(context.Background(), 0, f, func(ev model.AuditEvent) error {
					got = append(got, ev.Seq)
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("SQL filter = %v, Go filter = %v", got, want)
			}
		})
	}
}

// The A3 measurement pair on one tree: the legacy walk+filter (the old handler
// path, unchanged code) against the filtered store walk, same 20k-row ledger,
// needle filter (one match).
func BenchmarkAuditFilteredNeedle(b *testing.B) {
	clk := &tickClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Clock: clk}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: "ab", Slug: "ab", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		b.Fatal(err)
	}
	auditTestSeed(b, st, clk, tenant, 20000)
	needle := store.AuditFilter{ActionPrefix: "session_100%"}

	b.Run("legacy walk and filter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var matched int
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
					if auditGoFilter(ev, needle) {
						matched++
					}
					return nil
				})
			}); err != nil {
				b.Fatal(err)
			}
			if matched == 0 {
				b.Fatal("the needle must match")
			}
		}
	})
	b.Run("filtered store walk", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var matched int
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().(store.FilteredWalker).WalkFiltered(context.Background(), 0, needle, func(ev model.AuditEvent) error {
					matched++
					return nil
				})
			}); err != nil {
				b.Fatal(err)
			}
			if matched == 0 {
				b.Fatal("the needle must match")
			}
		}
	})
}

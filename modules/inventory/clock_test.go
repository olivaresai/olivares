// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// pinnedClock returns one instant, forever.
type pinnedClock struct{ at time.Time }

func (c pinnedClock) Now() model.Timestamp { return model.NewTimestamp(c.at) }

// TestWithClockStampsLastSeen proves the injected clock REACHES the observation
// stamp, which is the only claim worth making about an injection seam.
//
// Asserting `New(WithClock(c)).clock == c` would prove the option assigns a field
// and nothing about whether the field is ever read. The path that matters is
// materialize.go:33-36: an observation that carries NO ObservedAt is stamped with
// the module's own clock, and that stamp is `last_seen` — the column the staleness
// sweep compares against now (catalog.go:135) and the one the planner's decision on
// orden 31 defines as an OBSERVATION rather than an activity timestamp.
//
// The pinned instant is deliberately YEARS from now: if the option failed to wire,
// the module would fall back to model.SystemClock{} and the assertion would fail by
// half a decade rather than by a tolerance, so no widening of the comparison can
// ever hide it.
func TestWithClockStampsLastSeen(t *testing.T) {
	pinned := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	m, st, tenant := newInv(t, WithClock(pinnedClock{at: pinned}))

	// ObservedAt is the ZERO time on purpose: that is the branch that consults the
	// module clock. An edge carrying its own timestamp would never reach it, and a
	// test written that way would pass with the option unwired.
	m.feed(t, tenant, mkEdge("session", "sess-clock", "file", "/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", time.Time{}))

	got := lastSeenOfKind(t, st, tenant, kindSession)
	if got == "" {
		t.Fatal("no catalog entry for the session, so nothing was stamped")
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("last_seen %q is not a timestamp: %v", got, err)
	}
	if !parsed.UTC().Equal(pinned) {
		t.Errorf("last_seen = %s, want the injected clock's instant %s\n"+
			"  (a value near now means WithClock did not reach materialize.go:35)",
			parsed.UTC(), pinned)
	}
}

// lastSeenOfKind reads the last_seen column of the first catalog entry of a kind.
func lastSeenOfKind(t *testing.T, st store.Store, tenant model.TenantID, kind string) string {
	t.Helper()
	seen := ""
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEntityKind, kind)},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(recs) == 1 {
			seen = recs[0].String(colLastSeen)
		}
		return nil
	}); err != nil {
		t.Fatalf("lastSeenOfKind %s: %v", kind, err)
	}
	return seen
}

// colOf reads one column of the first catalog entry of a kind. It exists so the
// tests below assert on the STORED row rather than on what the code says it wrote.
func colOf(t *testing.T, st store.Store, tenant model.TenantID, kind, col string) string {
	t.Helper()
	got := ""
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(catalogEntryKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colEntityKind, kind)},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(recs) == 1 {
			got = recs[0].String(col)
		}
		return nil
	}); err != nil {
		t.Fatalf("colOf %s/%s: %v", kind, col, err)
	}
	return got
}

// TestLastSeenDoesNotAdvanceUnderAFixedClock is the planner's first mutant for decision A.
//
// last_seen is an OBSERVATION: it answers "when did this platform last see it". With the
// clock pinned, two observations minutes apart in the SOURCE's own reckoning must leave it
// exactly where it was — because nothing about when WE looked has changed. Before the split
// this failed by construction: last_seen took edge.ObservedAt, so it advanced with whatever
// the source sent and measured the source's clock rather than ours.
func TestLastSeenDoesNotAdvanceUnderAFixedClock(t *testing.T) {
	pinned := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	m, st, tenant := newInv(t, WithClock(pinnedClock{at: pinned}))

	m.feed(t, tenant, mkEdge("session", "sess-fixed", "file", "/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", baseTime))
	first := colOf(t, st, tenant, kindSession, colLastSeen)

	// The SAME entity, seen again, with the source claiming a much later instant.
	m.feed(t, tenant, mkEdge("session", "sess-fixed", "file", "/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", baseTime.Add(72*time.Hour)))
	second := colOf(t, st, tenant, kindSession, colLastSeen)

	if first != second {
		t.Errorf("last_seen advanced under a fixed clock: %q -> %q\n"+
			"  (it must track OUR clock, not the instant the source declares)", first, second)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, first); err == nil && !parsed.UTC().Equal(pinned) {
		t.Errorf("last_seen = %s, want the injected clock's instant %s", parsed.UTC(), pinned)
	}
}

// TestOccurredAtStaysAbsentWhenTheSourceDeclaresNone is the planner's second mutant.
//
// A source that declares no instant is INFORMATION — "nobody said when" — and the column
// must keep saying so. Filling it from our clock would manufacture a claim nobody made,
// which is the exact confusion decision A removed from last_seen.
func TestOccurredAtStaysAbsentWhenTheSourceDeclaresNone(t *testing.T) {
	pinned := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	m, st, tenant := newInv(t, WithClock(pinnedClock{at: pinned}))

	m.feed(t, tenant, mkEdge("session", "sess-noclaim", "file", "/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", time.Time{}))

	if got := colOf(t, st, tenant, kindSession, colOccurredAt); got != "" {
		t.Errorf("occurred_at = %q, want empty: the source declared no instant, so the "+
			"column must not invent one (a value equal to the clock means it did)", got)
	}
	// And the observation still happened, so last_seen is NOT empty: the two columns
	// answer different questions and this proves they are not wired to the same source.
	if got := colOf(t, st, tenant, kindSession, colLastSeen); got == "" {
		t.Error("last_seen is empty: the entity was observed, so it must carry our instant")
	}
}

// TestOccurredAtCarriesTheSourceClaim is the other direction: without it the two mutants
// above would both pass on a column that is never written at all.
func TestOccurredAtCarriesTheSourceClaim(t *testing.T) {
	pinned := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	claim := time.Date(2019, 11, 12, 13, 14, 15, 0, time.UTC)
	m, st, tenant := newInv(t, WithClock(pinnedClock{at: pinned}))

	m.feed(t, tenant, mkEdge("session", "sess-claim", "file", "/x",
		sdkmodel.ModeRead, sdkmodel.SignalOTEL, "Read", claim))

	got := colOf(t, st, tenant, kindSession, colOccurredAt)
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("occurred_at %q is not a timestamp: %v", got, err)
	}
	if !parsed.UTC().Equal(claim) {
		t.Errorf("occurred_at = %s, want the source's claim %s", parsed.UTC(), claim)
	}
	// The point of the whole change: the two are DIFFERENT instants and both survive.
	if seen := colOf(t, st, tenant, kindSession, colLastSeen); seen == got {
		t.Errorf("occurred_at and last_seen are identical (%s) — the split did not happen", got)
	}
}

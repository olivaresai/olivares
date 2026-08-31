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

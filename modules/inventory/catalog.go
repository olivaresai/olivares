// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// listCap bounds an internal List page. It matches the store's own maximum; a
// tenant with more rows than this in one sweep is handled across passes.
const listCap = 1000

// upsertCatalogEntry records or refreshes the catalog entry for one materialized
// entity: it bumps last-seen and occurrence, unions the discovering signal
// source (and host, when known), refreshes the denormalized name/ref, and flips
// a previously stale entry back to active because the entity has been seen
// again. It is idempotent — a re-delivered observation finds the existing entry
// and merges — which is what makes discovery safe under at-least-once delivery.
func (m *Module) upsertCatalogEntry(ctx context.Context, sc store.Scope, kind string, id model.ID, name, ref, source, host string, at, occurred time.Time) error {
	repo, err := sc.Ext(catalogEntryKind)
	if err != nil {
		return err
	}
	atTS := model.NewTimestamp(at).String()
	// ⛔ A ZERO OCCURRENCE STAYS ABSENT. The source declaring no instant is INFORMATION —
	// "nobody said when" — and filling it with our clock would erase that by writing a
	// timestamp nobody claimed. That is the exact confusion decision A removed from
	// last_seen; re-introducing it in the new column would be the same defect, moved.
	var occurredTS string
	if !occurred.IsZero() {
		occurredTS = model.NewTimestamp(occurred).String()
	}
	existing, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colEntityKind, Op: model.OpEq, Value: kind},
			{Column: colEntityID, Op: model.OpEq, Value: id.String()},
		},
		Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		_, err := repo.Create(ctx, model.Record{
			colEntityKind:    kind,
			colEntityID:      id.String(),
			colName:          name,
			colRef:           ref,
			colStatus:        statusActive,
			colSignalSources: marshalSet(addToSet(nil, source)),
			colHosts:         marshalSet(addToSet(nil, host)),
			colFirstSeen:     atTS,
			colLastSeen:      atTS,
			colOccurrence:    int64(1),
			colOccurredAt:    occurredTS,
		})
		// A redelivered create can race the unique index; treat a conflict as
		// "already exists" and merge on the next observation (idempotent).
		if err != nil && errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	rec := existing[0]
	rec[colName] = name
	rec[colRef] = ref
	rec[colStatus] = statusActive
	rec[colSignalSources] = marshalSet(addToSet(parseSet(rec.String(colSignalSources)), source))
	rec[colHosts] = marshalSet(addToSet(parseSet(rec.String(colHosts)), host))
	// Fixed-width canonical timestamps sort lexically, so a string compare is a
	// valid chronological "advance only forward" (core/model/time.go).
	if cur := rec.String(colLastSeen); cur == "" || cur < atTS {
		rec[colLastSeen] = atTS
	}
	// The occurrence advances on the same forward-only rule, and only when THIS
	// observation carried one: a later observation that declares nothing must not erase
	// what an earlier one declared.
	if occurredTS != "" {
		if cur := rec.String(colOccurredAt); cur == "" || cur < occurredTS {
			rec[colOccurredAt] = occurredTS
		}
	}
	rec[colOccurrence] = rec.Int(colOccurrence) + 1
	_, err = repo.Update(ctx, rec)
	return err
}

// errUnusableSweepState marks durable progress this module cannot read. It is
// deliberately NOT an empty state: a duplicated row, an instant that will not
// parse, a cursor with no cycle to belong to, or a page that says there is more
// but hands back no frontier are all "I do not know where this cycle is", and
// treating any of them as a finished cycle would silently advance the completed
// cutoff over a catalog nobody swept.
var errUnusableSweepState = errors.New("inventory: unusable durable freshness state")

// sweepLoop runs the staleness sweep on a ticker until stop is closed. The stop
// channel is captured by Start and passed in, so the goroutine never reads the
// mutable m.stop field (which Stop nils under the lock). It uses the system
// clock; the unit-tested entry point is Sweep.
//
// Stop reaches the running pass as an ordinary context cancellation, so "the
// caller cancelled" and "the module is stopping" are ONE mechanism inside Sweep
// rather than two things it has to remember to check. The old 30s deadline over
// the whole pass is gone: Sweep now budgets its enumeration and each tenant turn
// separately (defaultSweepBudget), which is what keeps an early tenant from
// spending the time of every tenant after it.
func (m *Module) sweepLoop(stop chan struct{}) {
	defer m.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if _, err := m.Sweep(ctx, m.clock.Now().Time()); err != nil {
				m.warnf("inventory: the durable freshness sweep did not complete this pass; entity staleness is not advancing for the tenants it names", "err", err)
			}
		}
	}
}

// Sweep gives every tenant of the durable sweep scope one turn, marking the
// catalog entries that have not been seen since the cycle's cutoff as stale. It
// returns how many entries were marked by turns that RETURNED SUCCESSFULLY, plus
// the joined errors of the turns that did not.
//
// The scope comes from SweepScopeSource — the estate's durable directory — which
// is what makes the pass survive a restart: a process that has received no event
// yet still knows which tenants exist. There is no fallback; an absent or
// non-authoritative scope is an error before any mutation.
//
// What a mark MEANS is unchanged and narrow: `stale` says only that THIS platform
// has not observed the entity since the cutoff. It is not `removed`, not
// `missing`, not a denial and not a claim that the entity stopped existing;
// re-seeing it flips the entry straight back to active (upsertCatalogEntry).
//
// Budgets: the enumeration gets its own deadline and each turn gets a fresh one,
// so one wedged tenant costs one turn and not the pass. A turn's timeout is
// local; only the caller's cancellation — which is how Stop arrives — ends the
// pass, and it ends it BETWEEN turns rather than in the middle of one.
//
// It is exported so tests can drive it deterministically with an injected clock.
func (m *Module) Sweep(ctx context.Context, now time.Time) (int, error) {
	if m.data == nil {
		return 0, ErrSweepDataUnavailable
	}
	scope := m.sweepScope()
	if scope == nil {
		return 0, ErrSweepScopeUnavailable
	}
	enumCtx, cancelEnum := context.WithTimeout(ctx, m.sweepBudget)
	tenants, err := scope.ListSweepTenants(enumCtx)
	cancelEnum()
	if err != nil {
		// Rows may have arrived WITH the error (SystemScope.ListOrgs does exactly
		// that). They are discarded: a set the store would not certify is not a
		// set to mutate from.
		return 0, fmt.Errorf("inventory: enumerate the durable freshness sweep scope: %w", err)
	}
	total := 0
	var errs error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			errs = errors.Join(errs, fmt.Errorf(
				"inventory: the durable freshness sweep stopped before every tenant of its snapshot had a turn: %w", err))
			break
		}
		turnCtx, cancelTurn := context.WithTimeout(ctx, m.sweepBudget)
		n, err := m.sweepTenant(turnCtx, tenant, now)
		cancelTurn()
		// Unconditional, and that is the point: sweepTenant already returns zero
		// for a turn whose transaction did not return successfully, so ONE place
		// decides what counts. A second guard here would be redundant, and a
		// redundant guard is a property no test can observe being removed.
		total += n
		if err != nil {
			// The tenant id and nothing else: enough to act on, no business data.
			errs = errors.Join(errs, fmt.Errorf("inventory: durable freshness sweep turn for tenant %s: %w", tenant, err))
		}
	}
	return total, errs
}

// sweepTenant runs ONE turn for one tenant in ONE transaction.
//
// The count is returned only when Mutate itself returned successfully. That is
// the correction this design makes: the previous shape incremented a counter
// inside the callback and returned it even when the transaction failed, so a
// rolled-back page was reported as marked. A commit that fails does not prove a
// rollback either — the outcome is UNCERTAIN — so the only honest number for a
// turn that did not return successfully is zero, and the only honest recovery is
// for the next turn to reread the durable state and continue from whatever it
// finds there.
func (m *Module) sweepTenant(ctx context.Context, tenant model.TenantID, now time.Time) (int, error) {
	marked := 0
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		n, err := m.sweepTurn(ctx, sc, now)
		if err != nil {
			return err
		}
		marked = n
		return nil
	}); err != nil {
		return 0, err
	}
	return marked, nil
}

// sweepTurn reads the durable progress, classifies at most one page, and writes
// the progress back — all inside the caller's single transaction, so the page
// and the cursor commit or roll back together.
//
// There is no extra row lock. Two writers of the SAME tenant are already
// serialized before this callback runs: sqlStore.Mutate takes the lineage write
// gate (core/internal/store/sqlstore/store.go:1838-1844) which on PostgreSQL
// holds the per-tenant advisory lock core.lineage.writer.tenant.v8:<tenant>
// (lineage_writer.go:52-63). Adding a RowLocker here would demand a capability
// GenericRepo does not declare in order to re-establish exclusion this store
// already provides. A different store implementation would have to prove that
// premise for itself rather than inherit it from here.
func (m *Module) sweepTurn(ctx context.Context, sc store.Scope, now time.Time) (int, error) {
	states, err := sc.Ext(freshnessSweepKind)
	if err != nil {
		return 0, err
	}
	// Limit 2 so "more than one" is OBSERVED rather than assumed away by Limit 1.
	rows, _, err := states.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return 0, err
	}
	if len(rows) > 1 {
		return 0, fmt.Errorf("%w: %d progress rows for one tenant where the unique index allows one",
			errUnusableSweepState, len(rows))
	}
	var state model.Record
	if len(rows) == 1 {
		state = rows[0]
	} else {
		// Lazily, on this tenant's first turn: no backfill over the directory.
		// A concurrent first turn loses the unique index as a visible conflict,
		// and the next turn reopens the winning row.
		if state, err = states.Create(ctx, model.Record{
			colCycleCutoffAt: "", colCatalogCursor: "", colLastCompletedCutoffAt: "",
		}); err != nil {
			return 0, err
		}
	}
	cycleCutoff := state.String(colCycleCutoffAt)
	cursor := state.String(colCatalogCursor)
	lastCompleted := state.String(colLastCompletedCutoffAt)
	if err := checkSweepState(cycleCutoff, cursor, lastCompleted); err != nil {
		return 0, err
	}

	// The candidate is what THIS turn would choose if it were starting a cycle.
	// An open cycle keeps its own cutoff instead, so a long cycle judges every
	// page against one instant rather than chasing the clock across pages.
	candidate := model.NewTimestamp(now.Add(-m.staleAfter)).String()
	cutoff := cycleCutoff
	switch {
	case cycleCutoff != "":
		if candidate < cycleCutoff {
			// The clock went back BELOW the cutoff this cycle is committed to.
			// An observation that has just arrived under the retarded clock must
			// not be judged against an instant in its future, so the cycle is
			// abandoned before another page is read. Progress already confirmed
			// is left exactly as it is: nothing is reverted and nothing is
			// classified this turn.
			return 0, writeSweepState(ctx, states, state, "", "", lastCompleted)
		}
	case lastCompleted != "" && candidate <= lastCompleted:
		// No cycle, and the clock has not reached past the last finished one.
		// There is no work — and, in particular, no decrement: a retarded clock
		// between cycles postpones the next cutoff, it never lowers the recorded one.
		return 0, nil
	default:
		cutoff, cursor = candidate, ""
	}

	repo, err := sc.Ext(catalogEntryKind)
	if err != nil {
		return 0, err
	}
	// No custom Sort: the store's keyset cursor is only meaningful for the
	// default id ordering (core/model/filter.go:101-103), and it is the store's
	// cursor we persist, never one of our own.
	stale, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			{Column: colStatus, Op: model.OpEq, Value: statusActive},
			{Column: colLastSeen, Op: model.OpLt, Value: cutoff},
		},
		Limit:  listCap,
		Cursor: cursor,
	})
	if err != nil {
		return 0, err
	}
	if page.HasMore && page.Cursor == "" {
		return 0, fmt.Errorf("%w: the catalog page reports more rows but returned no cursor to resume from",
			errUnusableSweepState)
	}
	n := 0
	for _, rec := range stale {
		rec[colStatus] = statusStale
		if _, err := repo.Update(ctx, rec); err != nil {
			return 0, err
		}
		n++
	}
	if page.HasMore {
		return n, writeSweepState(ctx, states, state, cutoff, page.Cursor, lastCompleted)
	}
	// The cycle reached the end of the catalog. Only now does the completed
	// cutoff move, and it records a finished SWEEP — not that any source was
	// completely enumerated.
	return n, writeSweepState(ctx, states, state, "", "", cutoff)
}

// checkSweepState refuses durable progress this module cannot act on. These are
// checks of ONE row and ONE page, not a validator: an unreadable instant or a
// cursor belonging to no cycle is a state whose next step is unknown, and
// guessing it would either rescan a catalog or skip one.
func checkSweepState(cycleCutoff, cursor, lastCompleted string) error {
	for _, f := range []struct{ col, value string }{
		{colCycleCutoffAt, cycleCutoff},
		{colLastCompletedCutoffAt, lastCompleted},
	} {
		if f.value == "" {
			continue
		}
		if _, err := model.ParseTimestamp(f.value); err != nil {
			return fmt.Errorf("%w: %s is not a readable instant: %v", errUnusableSweepState, f.col, err)
		}
	}
	if cursor != "" && cycleCutoff == "" {
		return fmt.Errorf("%w: a catalog cursor is stored with no open cycle it could resume", errUnusableSweepState)
	}
	return nil
}

// writeSweepState persists the three progress columns on the SAME record the
// turn read, so the store's optimistic-concurrency check applies to it like any
// other row.
func writeSweepState(ctx context.Context, repo store.GenericRepo, state model.Record, cycleCutoff, cursor, lastCompleted string) error {
	state[colCycleCutoffAt] = cycleCutoff
	state[colCatalogCursor] = cursor
	state[colLastCompletedCutoffAt] = lastCompleted
	_, err := repo.Update(ctx, state)
	return err
}

// --- small JSON-set helpers (signal sources / hosts) -------------------------

// parseSet decodes a JSON string array, tolerating empty/invalid input.
func parseSet(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// addToSet appends v to set if v is non-empty and not already present.
func addToSet(set []string, v string) []string {
	if v == "" {
		return set
	}
	for _, e := range set {
		if e == v {
			return set
		}
	}
	return append(set, v)
}

// marshalSet encodes a string set as a JSON array (always non-nil: "[]" empty).
func marshalSet(set []string) string {
	if set == nil {
		set = []string{}
	}
	b, err := json.Marshal(set)
	if err != nil {
		return "[]"
	}
	return string(b)
}

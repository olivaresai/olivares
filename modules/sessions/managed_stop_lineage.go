// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// managed_stop_lineage.go (P2 / W2) — the run's authorization workspace: where it
// comes from, who may write it, and how a legacy row acquires one.
//
// The column exists because a managed Stop must be authorized against a workspace
// the server resolved, not one the caller named. Three rules make that true, and
// each of them is a separate refusal rather than a default:
//
//  1. the value is resolved from the run's OWN session identity, inside the same
//     transaction that creates the run, so it cannot be supplied;
//  2. only the lawful creation mutation and the bounded repair below write it —
//     resume, stop, cleanup and credential-handle writes never assign it;
//  3. an unresolved run keeps NULL, and NULL is HIDDEN under confinement. It is
//     never read as the tenant default, which is the opposite of what the same
//     column name means on sessions.identity.

// errRunWorkspaceUnresolved means the run's lineage cannot be established from
// the identity facts visible in this transaction. It is not "absent" and not
// "default": it is a refusal to invent a workspace.
var errRunWorkspaceUnresolved = errors.New("sessions: run authorization workspace is unresolved")

// resolveRunAuthzWorkspace derives a run's authorization workspace from the
// session identity it is claimed under, using only facts visible in sc.
//
// It is correct under confinement without a second implementation: a confined
// Workspaces() returns only the caller's own workspace and DefaultWorkspace
// answers only a caller confined to the default, so an identity outside the
// boundary is unresolved rather than silently resolved to something else.
func resolveRunAuthzWorkspace(ctx context.Context, sc store.Scope, sid string) (model.ID, error) {
	if sc == nil || sid == "" {
		return "", errRunWorkspaceUnresolved
	}
	snap, err := ReadSessionIdentityInScope(ctx, sc, sid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", errRunWorkspaceUnresolved
		}
		return "", err
	}
	// The reader deliberately does not follow a merge, so a merged identity names
	// no single workspace this function may speak for.
	if snap.MergedInto != "" {
		return "", errRunWorkspaceUnresolved
	}
	if !snap.WorkspaceID.IsZero() {
		ws, err := sc.Workspaces().Get(ctx, snap.WorkspaceID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return "", errRunWorkspaceUnresolved
			}
			return "", err
		}
		return ws.ID, nil
	}
	// The identity's own NULL-means-default rule, applied HERE and stored as an
	// explicit id. That is what keeps the run column's NULL free to mean
	// "unresolved" instead of inheriting the identity's reading.
	def, err := sc.DefaultWorkspace(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", errRunWorkspaceUnresolved
		}
		return "", err
	}
	return def.ID, nil
}

// setRunAuthzWorkspace stamps the resolved lineage onto a run row being created.
// A run claimed under no session names no identity and therefore no workspace: it
// stays NULL and stays hidden.
func setRunAuthzWorkspace(ctx context.Context, sc store.Scope, rec model.Record, sid string) error {
	if sid == "" {
		rec[colRunAuthzWorkspaceID] = nil
		return nil
	}
	ws, err := resolveRunAuthzWorkspace(ctx, sc, sid)
	if err != nil {
		return err
	}
	rec[colRunAuthzWorkspaceID] = ws.String()
	return nil
}

// assertWorkItemSharesRunLineage refuses a work-bound run whose item does not
// live in the workspace the run's lineage names. It runs inside the creating
// transaction, after the lineage was resolved, and reports the existing
// dispatch_conflict: the launch named a work generation this run cannot answer to.
func assertWorkItemSharesRunLineage(
	ctx context.Context,
	sc store.Scope,
	rec model.Record,
	itemID model.ID,
) error {
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return err
	}
	item, err := items.Get(ctx, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return broken(http.StatusConflict, "dispatch_conflict")
	}
	if err != nil {
		return err
	}
	lineage := rec.String(colRunAuthzWorkspaceID)
	if lineage == "" || item.String(colWorkWorkspaceID) != lineage {
		return broken(http.StatusConflict, "dispatch_conflict")
	}
	return nil
}

// ---- the bounded repair operation (the W3 seam) ------------------------

// maxRunLineageRepairPage bounds one page. It is the ratified ceiling, not a
// tuning knob: a pass that read more would hold its transaction proportionally
// longer against every other writer on the tenant.
const maxRunLineageRepairPage = 256

// RunLineageRepairCursor is where a pass resumes. One page returns it and the
// next page presents it, and it CARRIES the pass's captured upper bound: a pass
// that re-captured its bound on every page would chase rows created while it runs
// and never be bounded at all. The zero cursor starts a new pass.
type RunLineageRepairCursor struct {
	// After is the last row id an earlier page of this pass advanced past.
	After model.ID
	// UpperBound is the highest run id that existed when this pass started.
	UpperBound model.ID
}

// RunLineageRepairResult is what ONE page of one pass over one tenant observed.
// The three outcomes are deliberately separate: a row that lost a compare-and-swap
// and a row whose identity cannot be resolved are different facts with different
// remedies, and collapsing either into exhaustion would make a stalled repair look
// like a finished one.
type RunLineageRepairResult struct {
	// Scanned is the number of candidate rows read.
	Scanned int
	// Repaired is the number that gained a lawful lineage in this page.
	Repaired int
	// Conflicts is the number whose version compare-and-swap lost to a concurrent
	// writer. They are neither repaired nor unresolved: a later pass sees them again.
	Conflicts int
	// Unresolved is the number whose identity facts cannot establish a workspace —
	// missing, merged, or naming a workspace this scope cannot read. They stay
	// NULL and stay hidden.
	Unresolved int
	// Exhausted is true when the cursor passed the upper bound captured at the
	// start of the pass. A page of zero updates is NOT completion, and completion
	// is not resolution: unresolved and conflicted rows can remain behind it.
	Exhausted bool
	// Next is the cursor the following page of THIS pass must present. It is the
	// zero cursor once the pass is exhausted, so the next call starts a new pass
	// under a freshly captured bound.
	Next RunLineageRepairCursor
}

// errRunLineageRepairCursor refuses a continuation that has lost its pass.
var errRunLineageRepairCursor = errors.New(
	"sessions: run lineage repair cursor resumes a pass without its captured bound")

// RepairRunLineage advances ONE bounded page of the historical run-lineage
// repair for one tenant and reports exactly what it observed.
//
// ⛔ IT IS NOT A REQUEST-SIDE REPAIR. Nothing in the Stop path or in
// ReadRunLaunch calls it: request handling either sees persisted lawful lineage
// or refuses. It is the module-owned half of the engine's background maintenance
// lifecycle; the leader-gated schedule that drives it belongs to the composition
// root and is not wired here.
//
// The shape is fixed by the contract and every part of it is load-bearing:
//
//   - a STRICTLY ADVANCING stable-id cursor, so a row cannot be revisited inside
//     a pass and a restart can resume without rescanning from the start;
//   - an UPPER BOUND captured once per pass and carried by the cursor, so rows
//     created while the pass runs are left to the next pass;
//   - at most 256 rows per page;
//   - the identity facts re-read and the workspace re-resolved in the SAME
//     transaction that writes, so a resolution can never be older than its write;
//   - an EXACT version compare-and-swap, so a concurrent live-run writer wins and
//     this records a conflict instead of overwriting it.
//
// Crash and restart may repeat a pass safely: a repeated pass re-selects only
// rows that are still NULL, and each write is conditioned on the row's exact
// version. The NULL is a SELECTION predicate, not an UPDATE one — the version
// CAS is what makes a concurrent writer win.
func (m *Module) RepairRunLineage(
	ctx context.Context,
	tenant model.TenantID,
	cursor RunLineageRepairCursor,
) (RunLineageRepairResult, error) {
	out := RunLineageRepairResult{Next: cursor}
	if m == nil || m.data == nil {
		return out, errors.New("sessions: run lineage repair has no data handle")
	}
	if tenant.IsZero() {
		return out, store.ErrNoTenant
	}
	if cursor.UpperBound.IsZero() && !cursor.After.IsZero() {
		// Re-capturing here would silently widen the pass it claims to continue.
		return out, errRunLineageRepairCursor
	}
	if !cursor.UpperBound.IsZero() && cursor.After >= cursor.UpperBound {
		return RunLineageRepairResult{Exhausted: true}, nil
	}
	// The pass runs on the tenant scope the engine lifecycle hands it, unconfined:
	// every row it exists to fix carries a NULL lineage, and NULL is exactly what
	// confinement hides. A confined repair could not see its own candidates.
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// A retried attempt starts from nothing: counters describe ONE committed
		// transaction, never the sum of attempts that rolled back.
		out = RunLineageRepairResult{Next: cursor}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		next := cursor
		if next.UpperBound.IsZero() {
			// Captured ONCE, from the highest existing id, before any row is read.
			upper, err := highestRunID(ctx, repo)
			if err != nil {
				return err
			}
			if upper.IsZero() {
				out.Exhausted = true
				return nil
			}
			next.UpperBound = upper
		}
		rows, err := runLineageCandidates(ctx, repo, next.After, next.UpperBound)
		if err != nil {
			return err
		}
		out.Scanned = len(rows)
		for _, rec := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := model.ID(rec.String(model.ColID))
			if id <= next.After || id > next.UpperBound {
				// A repository that returned a row outside (After, UpperBound] would
				// make this pass non-terminating or unbounded.
				return fmt.Errorf("sessions: run lineage repair read %s outside (%s, %s]",
					id, next.After, next.UpperBound)
			}
			next.After = id
			if err := m.repairOneRunLineage(ctx, sc, repo, rec, &out); err != nil {
				return err
			}
		}
		// Completion is the CURSOR passing the captured bound: a short page means
		// no candidate remains at or below it. A page that changed nothing is not.
		out.Exhausted = len(rows) < maxRunLineageRepairPage || next.After >= next.UpperBound
		out.Next = next
		return nil
	})
	if err != nil {
		return RunLineageRepairResult{Next: cursor}, err
	}
	if out.Exhausted {
		out.Next = RunLineageRepairCursor{}
	}
	return out, nil
}

// repairOneRunLineage resolves and writes one row, or records why it did not.
func (m *Module) repairOneRunLineage(
	ctx context.Context,
	sc store.Scope,
	repo store.GenericRepo,
	rec model.Record,
	out *RunLineageRepairResult,
) error {
	sid := rec.String(colRunClaimSID)
	if sid == "" {
		out.Unresolved++
		return nil
	}
	// Re-read and re-resolve in THIS transaction. The candidate row was selected
	// in the same transaction, but the identity behind it is a separate row and
	// its facts are what the write rests on.
	ws, err := resolveRunAuthzWorkspace(ctx, sc, sid)
	if err != nil {
		if errors.Is(err, errRunWorkspaceUnresolved) {
			out.Unresolved++
			return nil
		}
		return err
	}
	// ⛔ THE ONLY GUARD AGAINST A CONCURRENT WRITER IS THE VERSION CAS BELOW, and
	// re-reading the column off this record would NOT be a second one: rec is this
	// transaction's own snapshot, selected by the IS NULL filter, so it reads empty
	// whatever another transaction has committed since. A check that cannot fail is
	// worse than no check, because it reads like protection.
	rec[colRunAuthzWorkspaceID] = ws.String()
	if _, err := repo.Update(ctx, rec); err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			// A concurrent live-run writer advanced the version, or the row is gone.
			// Either way this pass does not own it; the next one sees it again.
			out.Conflicts++
			return nil
		}
		return err
	}
	out.Repaired++
	return nil
}

// highestRunID reads the largest run id in the tenant. It is the pass's captured
// upper bound and is read before any candidate.
func highestRunID(ctx context.Context, repo store.GenericRepo) (model.ID, error) {
	rows, _, err := repo.List(ctx, model.Query{
		Sort:  []model.Sort{{Column: model.ColID, Desc: true}},
		Limit: 1,
	})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return model.ID(rows[0].String(model.ColID)), nil
}

// runLineageCandidates reads one page of rows that still carry no lineage,
// strictly after the cursor and at or before the captured bound, in id order.
func runLineageCandidates(
	ctx context.Context,
	repo store.GenericRepo,
	after, upper model.ID,
) ([]model.Record, error) {
	filters := []model.Filter{{Column: colRunAuthzWorkspaceID, Op: model.OpIsNull}}
	if after != "" {
		filters = append(filters, model.Filter{Column: model.ColID, Op: model.OpGt, Value: after.String()})
	}
	filters = append(filters, model.Filter{Column: model.ColID, Op: model.OpLte, Value: upper.String()})
	rows, _, err := repo.List(ctx, model.Query{
		Filters: filters,
		Sort:    []model.Sort{{Column: model.ColID}},
		Limit:   maxRunLineageRepairPage,
	})
	return rows, err
}

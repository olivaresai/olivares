// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A person's own SNOOZE and DONE marks on a session (#2480), the two the reference's
// thread list offers beside its pin: snooze takes a session out of THIS person's list
// until a moment they chose, done files it under Settled. They are the caller's own
// interface state exactly as a pin is (run_pin.go), so they take the same rules: kept
// on the server so they follow the person to another browser, never visible to
// anyone else, READ permission, no ledger row.
//
// One row per (tenant, owner, run) holds both marks; a row with neither is deleted,
// so a person who clears what they set leaves nothing behind. A snooze whose moment
// has passed is no snooze: the read paths stop carrying it, which is how every client
// shows the session again without a clock of its own.
const (
	// runMarkKind is one person's snooze and done marks on one operated session.
	runMarkKind model.Kind = "sessions.run_mark"
	// runMarkTable is the mark row's physical table.
	runMarkTable = "sessions_run_mark"
	// runMarkLookupChunk bounds the run refs one lookup binds: a list page is at most
	// runListScanPage rows plus the caller's pins.
	//
	// ponytail: no per-person ceiling on mark rows, unlike pins: a person holds at most
	// one row per run they can read, and the rows go when the run is released or
	// deleted. Add a ceiling if a tenant's mark rows outgrow its runs.
	runMarkLookupChunk = 500
)

// sessions.run_mark columns. run_ref and owner repeat the pin row's names.
const (
	colRunMarkRun          = "run_ref"
	colRunMarkOwner        = "owner"
	colRunMarkSnoozedUntil = "snoozed_until"
	colRunMarkDone         = "done"
)

// registerRunMarkSchema declares the mark entity, the pin entity's sibling: the same
// owner and run columns, the run's workspace lineage so a confined member keeps the
// run list, and a unique index that makes a second row per person and run a conflict.
func (m *Module) registerRunMarkSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  runMarkKind,
		Table: runMarkTable,
		Fields: []model.FieldSpec{
			{Name: colRunMarkRun, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colRunMarkOwner, Kind: model.KindText, Indexed: true, Principal: pdeclActorRef},
			{Name: colRunMarkSnoozedUntil, Kind: model.KindTimestamp, Nullable: true},
			{Name: colRunMarkDone, Kind: model.KindBool},
			{Name: colRunAuthzWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the core workspace id of the marked run's authorization lineage, never an account: runtime_schema.go:357, runtime_schema.go:397-401")},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   colRunAuthzWorkspaceID,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_run_mark_uniq",
			Columns: []string{model.ColTenantID, colRunMarkOwner, colRunMarkRun},
			Unique:  true,
		}},
	})
}

// runMark is what one person's row says about one run, as the read paths answer it:
// snoozedUntil is empty unless the snooze is still to end.
type runMark struct {
	snoozedUntil string
	done         bool
}

// markOf reads a row as of now.
func markOf(rec model.Record, now time.Time) runMark {
	out := runMark{done: rec.Bool(colRunMarkDone)}
	if ts, err := model.ParseTimestamp(rec.String(colRunMarkSnoozedUntil)); err == nil && ts.Time().After(now) {
		out.snoozedUntil = ts.Time().Format(time.RFC3339)
	}
	return out
}

// ownRunMarks reads the caller's marks on the given runs, keyed by run ref.
func ownRunMarks(ctx context.Context, sc store.Scope, caller string, refs []string, now time.Time) (map[string]runMark, error) {
	out := make(map[string]runMark)
	if len(refs) == 0 {
		return out, nil
	}
	repo, err := sc.Ext(runMarkKind)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(refs); start += runMarkLookupChunk {
		chunk := refs[start:min(start+runMarkLookupChunk, len(refs))]
		recs, _, err := repo.List(ctx, model.Query{Limit: len(chunk), Filters: []model.Filter{
			eq(colRunMarkOwner, caller), {Column: colRunMarkRun, Op: model.OpIn, Value: chunk},
		}})
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			out[rec.String(colRunMarkRun)] = markOf(rec, now)
		}
	}
	return out, nil
}

// decorateRunMark marks one DTO with the caller's snooze and done. A released session
// never carries them: cleanup drops the rows, and this is the same rule holding if a
// crash left one behind.
func decorateRunMark(dto *runDTO, rec model.Record, marks map[string]runMark) {
	mark := marks[dto.RunRef]
	if rec.String(colState) == stateCleaned {
		mark = runMark{}
	}
	dto.SnoozedUntil = mark.snoozedUntil
	dto.Done = &mark.done
}

// markRun applies change to the caller's row for run and returns the marks it then
// holds, creating the row on the first mark and deleting it once it holds none.
func markRun(ctx context.Context, sc store.Scope, caller string, run model.Record, now time.Time, change func(model.Record)) (runMark, error) {
	repo, err := sc.Ext(runMarkKind)
	if err != nil {
		return runMark{}, err
	}
	runRef := run.String(colRunRef)
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colRunMarkOwner, caller), eq(colRunMarkRun, runRef)}, Limit: 1,
	})
	if err != nil {
		return runMark{}, err
	}
	row := model.Record{colRunMarkRun: runRef, colRunMarkOwner: caller, colRunMarkDone: false}
	if ws := run.String(colRunAuthzWorkspaceID); ws != "" {
		row[colRunAuthzWorkspaceID] = ws
	}
	if len(recs) == 1 {
		row = recs[0]
	}
	change(row)
	mark := markOf(row, now)
	switch {
	case mark == runMark{} && len(recs) == 1:
		// A row whose snooze ended and that is not done holds nothing either.
		id, err := model.ParseID(row.String(model.ColID))
		if err != nil {
			return runMark{}, err
		}
		return mark, repo.Delete(ctx, id)
	case mark == runMark{}:
		return mark, nil
	case len(recs) == 1:
		_, err = repo.Update(ctx, row)
	default:
		_, err = repo.Create(ctx, row)
	}
	return mark, err
}

// markDTO is the answer of the snooze and done routes: the run and both of the
// caller's marks as they now stand.
type markDTO struct {
	RunRef       string `json:"run_ref"`
	SnoozedUntil string `json:"snoozed_until,omitempty"`
	Done         bool   `json:"done"`
}

// snoozeRequest is the body of POST /runs/{ref}/snooze.
type snoozeRequest struct {
	Until string `json:"until"`
}

// handleMarkRun is the one handler behind all four mark routes: it resolves the run
// inside the caller's transaction (a run that vanished mid-flight refuses as 404),
// refuses a released one when setting a mark, and changes column as of now, the
// instant the snooze route checked its moment against.
func (m *Module) handleMarkRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, now time.Time, column string, value any) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	ref := chi.URLParam(r, "ref")
	set := value != nil && value != false
	var out markDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(r.Context(), repo, ref)
		if err != nil {
			return err
		}
		// A released session has left every list for good; a mark on it would answer
		// set and read as unset.
		if set && rec.String(colState) == stateCleaned {
			return conflictErr("a released session cannot be snoozed or marked done")
		}
		mark, err := markRun(r.Context(), sc, mc.Principal.Actor(), rec, now, func(row model.Record) { row[column] = value })
		if err != nil {
			return err
		}
		if column == colRunMarkDone && mark.done && rec.String(colLimitResumeUser) == mc.Principal.UserID.String() && (rec.String(colRunResumeAt) != "" || rec.Bool(colRunResumeAfterLimit)) {
			rec[colRunResumeCancelled] = true
			rec[colRunResumeAt] = nil
			rec[colRunResumeDismissed] = true
			rec[colRunResumeAfterLimit] = false
			if _, err := repo.Update(r.Context(), rec); err != nil {
				return err
			}
			if _, err := sc.Audit().Append(r.Context(), model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "sessions.run.resume_cancelled", TargetKind: runKind, TargetID: model.ID(rec.String(model.ColID)), Meta: map[string]any{"reason": "marked done"}}); err != nil {
				return err
			}
		}
		out = markDTO{RunRef: ref, SnoozedUntil: mark.snoozedUntil, Done: mark.done}
		return nil
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSnoozeRun snoozes the run for the caller until a moment still to come, or
// moves the moment of a snooze already set.
func (m *Module) handleSnoozeRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body snoozeRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(body.Until))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("until must be an RFC 3339 time, for example 2026-10-18T18:00:00Z"))
		return
	}
	now := m.now()
	if !until.After(now) {
		writeJSON(w, http.StatusBadRequest, errorBody("until must be in the future"))
		return
	}
	stamp := model.NewTimestamp(until).String()
	// Past the year 9999 the stored text does not parse back (core/model/time.go), and
	// the snooze would answer set and read as unset.
	if _, err := model.ParseTimestamp(stamp); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("until must be before the year 10000"))
		return
	}
	m.handleMarkRun(w, r, mc, now, colRunMarkSnoozedUntil, stamp)
}

// handleUnsnoozeRun ends the caller's snooze, idempotently.
func (m *Module) handleUnsnoozeRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleMarkRun(w, r, mc, m.now(), colRunMarkSnoozedUntil, nil)
}

// handleDoneRun marks the run done for the caller, idempotently.
func (m *Module) handleDoneRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	release, err := m.rt.lockRunContext(r.Context(), liveKey(mc.Tenant, chi.URLParam(r, "ref")))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	m.handleMarkRun(w, r, mc, m.now(), colRunMarkDone, true)
}

// handleUndoneRun removes the caller's done mark, idempotently.
func (m *Module) handleUndoneRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.handleMarkRun(w, r, mc, m.now(), colRunMarkDone, false)
}

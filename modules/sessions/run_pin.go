// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A person's own PINNED SESSIONS (#2130): the sessions they keep first in their
// list, the way the reference's thread menu pins a thread. A pin is the caller's
// own interface state, exactly what console favorites are
// (modules/consoleviews/favorites.go): it lives on the server so it follows the
// person to another browser or device, it never changes another person's list,
// and it is not an auditable act on the tenant — so the routes take the module's
// READ permission (every signed-in role keeps its own) and write no ledger row.
//
// One row per (tenant, owner, run): the run's public ref plus the actor that
// pinned it, with the row's created_at as when. The ceiling is the rule the
// console had while it kept pins in the browser: past twenty pins the oldest
// leaves, which is what a person means by pinning a twenty-first session.
const (
	// runPinKind is one person's pin on one operated session.
	runPinKind model.Kind = "sessions.run_pin"
	// runPinTable is the pin row's physical table.
	runPinTable = "sessions_run_pin"
	// maxRunPins is the per-person ceiling, owned here: the console reads the
	// engine's answer and keeps no count of its own.
	maxRunPins = 20
)

// sessions.run_pin columns. run_ref repeats the run table's column name on its
// own table, the way colRunAgentRef does.
const (
	colRunPinRun   = "run_ref"
	colRunPinOwner = "owner"
)

// registerRunPinSchema declares the pin entity. The engine creates the table
// with the base columns (id/tenant_id/created_at/…) and the tenant guard, on
// SQLite and PostgreSQL alike; the unique index makes a second pin of the same
// run by the same person a conflict no caller can reach (the routes are
// idempotent), never a silent duplicate.
func (m *Module) registerRunPinSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  runPinKind,
		Table: runPinTable,
		Fields: []model.FieldSpec{
			{Name: colRunPinRun, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colRunPinOwner, Kind: model.KindText, Indexed: true, Principal: pdeclActorRef},
			// The run's own authorization lineage, copied when the pin is made. Without
			// it a workspace-confined reader is refused this entity before any repo
			// exists, and that refusal would take the run list and get down with it.
			{Name: colRunAuthzWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the core workspace id of the pinned run's authorization lineage, never an account: runtime_schema.go:357, runtime_schema.go:397-401")},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   colRunAuthzWorkspaceID,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_run_pin_uniq",
			Columns: []string{model.ColTenantID, colRunPinOwner, colRunPinRun},
			Unique:  true,
		}},
	})
}

// ownRunPin is one pin row as the read paths want it.
type ownRunPin struct {
	id  string
	run string
	at  string
}

// listOwnRunPins reads the caller's pins oldest-first — the order they were
// pinned, which is the order the pinned rows hold at the top of a list.
func listOwnRunPins(ctx context.Context, sc store.Scope, caller string) ([]ownRunPin, error) {
	repo, err := sc.Ext(runPinKind)
	if err != nil {
		return nil, err
	}
	q := model.Query{Filters: []model.Filter{eq(colRunPinOwner, caller)},
		Limit: maxRunPins + 1, Sort: []model.Sort{{Column: model.ColCreatedAt}}}
	recs, _, err := repo.List(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]ownRunPin, 0, len(recs))
	for _, rec := range recs {
		out = append(out, ownRunPin{
			id: rec.String(model.ColID), run: rec.String(colRunPinRun),
			at: rec.String(model.ColCreatedAt),
		})
	}
	return out, nil
}

// ownRunPinAts is the caller's pins as a run-ref → pinned-at map, for marking
// the rows a list or get already returned.
func ownRunPinAts(ctx context.Context, sc store.Scope, caller string) (map[string]string, error) {
	own, err := listOwnRunPins(ctx, sc, caller)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(own))
	for _, p := range own {
		out[p.run] = p.at
	}
	return out, nil
}

// decorateRunPin marks one DTO with the caller's pin. A released session never
// carries the mark: cleanup drops the pin rows, and this read-side refusal is
// the same rule holding if a crash left a row behind.
func decorateRunPin(dto *runDTO, rec model.Record, pins map[string]string) {
	pinned := false
	if at, ok := pins[dto.RunRef]; ok && rec.String(colState) != stateCleaned {
		pinned = true
		dto.PinnedAt = at
	}
	dto.Pinned = &pinned
}

// pinRun pins the run for the caller, idempotently, keeping the ceiling by
// dropping the oldest pin. The pin row carries the run's workspace lineage.
func pinRun(ctx context.Context, sc store.Scope, caller string, run model.Record) (string, error) {
	runRef := run.String(colRunRef)
	repo, err := sc.Ext(runPinKind)
	if err != nil {
		return "", err
	}
	own, err := listOwnRunPins(ctx, sc, caller)
	if err != nil {
		return "", err
	}
	for _, p := range own {
		if p.run == runRef {
			return p.at, nil
		}
	}
	if len(own) >= maxRunPins {
		if own, err = dropReleasedPins(ctx, sc, repo, own); err != nil {
			return "", err
		}
	}
	if len(own) >= maxRunPins {
		oldest, err := model.ParseID(own[0].id)
		if err != nil {
			return "", err
		}
		if err := repo.Delete(ctx, oldest); err != nil {
			return "", err
		}
	}
	row := model.Record{colRunPinRun: runRef, colRunPinOwner: caller}
	if ws := run.String(colRunAuthzWorkspaceID); ws != "" {
		row[colRunAuthzWorkspaceID] = ws
	}
	created, err := repo.Create(ctx, row)
	if err != nil {
		return "", err
	}
	return created.String(model.ColCreatedAt), nil
}

// dropReleasedPins removes the pin rows whose session is released or gone, which a
// failed cleanup can leave behind (they read as unpinned but still count against
// the ceiling), and returns the pins that remain. It runs only when a person is at
// the ceiling, so it costs at most maxRunPins lookups and only then.
func dropReleasedPins(ctx context.Context, sc store.Scope, pins store.GenericRepo, own []ownRunPin) ([]ownRunPin, error) {
	runs, err := sc.Ext(runKind)
	if err != nil {
		return nil, err
	}
	kept := make([]ownRunPin, 0, len(own))
	for _, p := range own {
		rec, err := findRunRec(ctx, runs, p.run)
		if err == nil && rec.String(colState) != stateCleaned {
			kept = append(kept, p)
			continue
		}
		var re *runErr
		if err != nil && !(errors.As(err, &re) && re.status == http.StatusNotFound) {
			return nil, err
		}
		id, err := model.ParseID(p.id)
		if err != nil {
			return nil, err
		}
		if err := pins.Delete(ctx, id); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

// unpinRun removes the caller's pin. Unpinning what is not pinned is the same
// success, never a 409: the state the caller asked for is the state that holds.
func unpinRun(ctx context.Context, sc store.Scope, caller, runRef string) error {
	repo, err := sc.Ext(runPinKind)
	if err != nil {
		return err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colRunPinOwner, caller), eq(colRunPinRun, runRef)}, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	id, err := model.ParseID(recs[0].String(model.ColID))
	if err != nil {
		return err
	}
	return repo.Delete(ctx, id)
}

// dropPersonalRunState removes EVERY person's pins (#2130), snooze and done
// marks (#2480, run_mark.go) and the messages queued for the session (#3125,
// run_queue.go) on one run: a session that is released or deleted leaves every
// person's list for good. Only a release can reach a queue (a delete needs a released
// session), and its answer carries the queue it discards, so nothing a person typed
// goes without being shown.
func dropPersonalRunState(ctx context.Context, sc store.Scope, runRef string) error {
	// Every kind is tried: a failure on one must not leave the others' rows behind.
	return errors.Join(
		dropRunRows(ctx, sc, runPinKind, runRef),
		dropRunRows(ctx, sc, runMarkKind, runRef),
		dropRunRows(ctx, sc, runQueueKind, runRef),
	)
}

// dropRunRows removes every row of one per-person kind on one run. Both kinds name
// the run in the run_ref column.
func dropRunRows(ctx context.Context, sc store.Scope, kind model.Kind, runRef string) error {
	repo, err := sc.Ext(kind)
	if err != nil {
		return err
	}
	q := model.Query{Filters: []model.Filter{eq(colRunPinRun, runRef)}, Limit: 1000}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			id, err := model.ParseID(rec.String(model.ColID))
			if err != nil {
				return err
			}
			if err := repo.Delete(ctx, id); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.Cursor == "" {
			// An id-ordered page always carries one. Answering success here would
			// leave pin rows for a run whose delete already committed.
			return errors.New("sessions: " + string(kind) + " page has more rows and no cursor")
		}
		q.Cursor = page.Cursor
	}
}

// pinnedRunsOutside returns the caller's pinned runs that a list page did not
// carry, oldest pin first, so a pin on a session older than the page window still
// reaches the clients that sort by it ("first in my list" holds however many
// sessions came after). A pin whose session is released or gone is skipped.
func pinnedRunsOutside(ctx context.Context, sc store.Scope, pins map[string]string, inPage map[string]bool) ([]model.Record, error) {
	repo, err := sc.Ext(runKind)
	if err != nil {
		return nil, err
	}
	var missing []string
	for ref := range pins {
		if !inPage[ref] {
			missing = append(missing, ref)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return pins[missing[i]] < pins[missing[j]] })
	var out []model.Record
	for _, ref := range missing {
		rec, err := findRunRec(ctx, repo, ref)
		var re *runErr
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if rec.String(colState) != stateCleaned {
			out = append(out, rec)
		}
	}
	return out, nil
}

// pinDTO is the answer of both pin routes: the run, the state its caller's pin
// is now in, and when it was pinned.
type pinDTO struct {
	RunRef   string `json:"run_ref"`
	Pinned   bool   `json:"pinned"`
	PinnedAt string `json:"pinned_at,omitempty"`
}

// handlePinRun pins the run for the caller. The entity middleware has already
// resolved the run and concealed an unreadable one as absent; the handler only
// re-reads the row inside its own transaction so a run that vanished mid-flight
// still refuses as 404 rather than pinning a ref with no session.
func (m *Module) handlePinRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	ref := chi.URLParam(r, "ref")
	caller := mc.Principal.Actor()
	var out pinDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(r.Context(), repo, ref)
		if err != nil {
			return err
		}
		// A released session has left every pinned group and never comes back; a
		// pin on it would answer pinned and read as unpinned.
		if rec.String(colState) == stateCleaned {
			return conflictErr("a released session cannot be pinned")
		}
		at, err := pinRun(r.Context(), sc, caller, rec)
		if err != nil {
			return err
		}
		out = pinDTO{RunRef: ref, Pinned: true, PinnedAt: at}
		return nil
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUnpinRun removes the caller's pin, idempotently.
func (m *Module) handleUnpinRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	ref := chi.URLParam(r, "ref")
	caller := mc.Principal.Actor()
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		if _, err := findRunRec(r.Context(), repo, ref); err != nil {
			return err
		}
		return unpinRun(r.Context(), sc, caller, ref)
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pinDTO{RunRef: ref, Pinned: false})
}

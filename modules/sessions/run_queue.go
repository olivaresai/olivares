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

// The messages a person lines up while a session's tool is still working.
//
// The queue lives on the server, one row per message, so it survives a reload and shows
// on every device. When the turn ends the first message goes to the tool by itself, by
// the same input route a direct send uses; a session that stopped, hit a limit or waits
// for an approval keeps the queue and says which (queueHold). Steer sends one message at
// once into the running turn through the tool's own input: Olivares adds no steer of its
// own, and a tool that takes no input into a running turn offers none (queueCanSteer).
//
// Who may queue is who may message the session: the write permission on the run and the
// edition rule on steering a colleague's session (steering_edition.go). The row keeps who
// queued the message; the send itself is the engine's, after the turn.
const (
	// runQueueKind is one message waiting for a session's turn to end.
	runQueueKind model.Kind = "sessions.run_queue"
	// runQueueTable is the queue row's physical table.
	runQueueTable = "sessions_run_queue"
	// maxQueuedMessages bounds one session's queue.
	//
	// ponytail: a fixed 50, enough for a person's follow-ups and a hard stop for a script
	// that never stops queueing. Make it a setting if a real session needs more.
	maxQueuedMessages = 50
)

// sessions.run_queue columns. run_ref repeats the pin row's name, so dropRunRows clears
// the queue with the pins when a session is released or deleted.
const (
	colRunQueueRun  = "run_ref"
	colRunQueueBy   = "queued_by"
	colRunQueueText = "body"
	colRunQueuePos  = "position"
)

// What keeps the first queued message from going (runQueueDTO.Hold). Empty means nothing
// does: it goes at once.
const (
	queueHoldTurn     = "turn"     // the tool is answering; the message goes when it stops
	queueHoldApproval = "approval" // the session waits for the person to approve something
	queueHoldLimit    = "limit"    // the account's limit ended the last turn
	queueHoldStopped  = "stopped"  // stopped, interrupted, failed or released
)

// registerRunQueueSchema declares the queue entity: the run it waits on, who queued it,
// the text, its place in the line, and the run's workspace lineage so a confined member
// keeps the same view of the run as of its queue.
func (m *Module) registerRunQueueSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  runQueueKind,
		Table: runQueueTable,
		Fields: []model.FieldSpec{
			{Name: colRunQueueRun, Kind: model.KindText, Indexed: true, Principal: pdeclNoneRunRef},
			{Name: colRunQueueBy, Kind: model.KindText, Principal: pdeclActorRef},
			{Name: colRunQueueText, Kind: model.KindText, Principal: model.None("a message the person typed for the session's tool, sent as written and never resolved to an account: run_queue.go:104, run_queue.go:304")},
			{Name: colRunQueuePos, Kind: model.KindInt},
			{Name: colRunAuthzWorkspaceID, Kind: model.KindUUID, Nullable: true, Principal: model.None("the core workspace id of the run's authorization lineage, never an account: runtime_schema.go:357, runtime_schema.go:397-401")},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column:   colRunAuthzWorkspaceID,
			Encoding: model.WorkspaceLineageID,
			Unset:    model.WorkspaceUnsetHidden,
		},
	})
}

// queuedMessageDTO is one message in the line. The list is in sending order.
type queuedMessageDTO struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	QueuedBy string `json:"queued_by"`
	QueuedAt string `json:"queued_at"`
}

// runQueueDTO is a session's queue and what it waits for. CanSteer says whether this
// session's tool takes a message into a running turn; Hold is absent when the first
// message goes at once.
type runQueueDTO struct {
	RunRef   string                          `json:"run_ref"`
	Items    api.JSONArray[queuedMessageDTO] `json:"items"`
	Hold     string                          `json:"hold,omitempty"`
	CanSteer bool                            `json:"can_steer"`
}

func queuedDTO(rec model.Record) queuedMessageDTO {
	return queuedMessageDTO{
		ID: rec.String(model.ColID), Text: rec.String(colRunQueueText),
		QueuedBy: rec.String(colRunQueueBy), QueuedAt: rec.String(model.ColCreatedAt),
	}
}

func queuedDTOs(recs []model.Record) api.JSONArray[queuedMessageDTO] {
	out := make(api.JSONArray[queuedMessageDTO], 0, len(recs))
	for _, rec := range recs {
		out = append(out, queuedDTO(rec))
	}
	return out
}

// queueRows reads one session's queue in sending order.
func queueRows(ctx context.Context, sc store.Scope, runRef string) ([]model.Record, error) {
	repo, err := sc.Ext(runQueueKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colRunQueueRun, runRef)},
		Sort:    []model.Sort{{Column: colRunQueuePos}},
		Limit:   maxQueuedMessages + 1,
	})
	return recs, err
}

// queueItem finds one message of the run's queue by id; absent is the concealed 404.
func queueItem(rows []model.Record, id string) (model.Record, error) {
	for _, rec := range rows {
		if rec.String(model.ColID) == id {
			return rec, nil
		}
	}
	return nil, &runErr{http.StatusNotFound, "that message is not in the queue"}
}

// queueCanSteer says whether the run's tool takes a message while its turn runs: Claude
// Code reads its stdin at any time, and the Codex app-server has turn/steer. ACP tools
// have no steer and refuse a second turn (driver_grok.go), so they get none.
func queueCanSteer(rec model.Record) bool {
	switch rec.String(colRunProfileDriver) {
	case "", providerDriverClaude:
		return Transport(rec.String(colTransport)) == TransportStreamJSON
	case providerDriverCodex:
		return true
	}
	return false
}

// queueHold says why the first queued message has not gone, from the run row and its
// live handle: the same facts the run's own state reads. A limit whose reset has passed
// holds nothing.
func (m *Module) queueHold(rec model.Record) string {
	switch rec.String(colState) {
	case stateRunning:
	case stateWaitingApproval:
		return queueHoldApproval
	default:
		return queueHoldStopped
	}
	lr, ok := m.rt.getLive(model.TenantID(rec.String(model.ColTenantID)), rec.String(colRunRef))
	if !ok || lr.launchID.String() != rec.String(colRuntimeLaunchID) {
		return queueHoldStopped
	}
	if limit := storedRunLimit(rec); limit != nil && !m.limitLifted(limit) {
		return queueHoldLimit
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	switch {
	case lr.stopRequested || lr.finalized || lr.launchFailed || lr.turnInterrupted:
		return queueHoldStopped
	case len(lr.pendingApprovals) > 0:
		return queueHoldApproval
	case lr.workInFlightLocked(m.now(), m.rt.idleWindow):
		return queueHoldTurn
	}
	return ""
}

// limitLifted reports a limit whose own reset time is behind us.
//
// ponytail: nothing wakes the queue when a limit lifts; it goes on the next turn end, a
// resume, or the person's Send now. A timer that resumes at the reset belongs to the
// resume-at-reset work.
func (m *Module) limitLifted(l *runLimit) bool {
	at, err := time.Parse(time.RFC3339, l.ResetsAt)
	return err == nil && !at.After(m.now())
}

// queueState is what GET and the reorder answer: the queue and what it waits for.
func (m *Module) queueState(ctx context.Context, sc store.Scope, run model.Record) (runQueueDTO, error) {
	rows, err := queueRows(ctx, sc, run.String(colRunRef))
	if err != nil {
		return runQueueDTO{}, err
	}
	return runQueueDTO{
		RunRef: run.String(colRunRef), Items: queuedDTOs(rows),
		Hold: m.queueHold(run), CanSteer: queueCanSteer(run),
	}, nil
}

// queueSnapshot is what a session's queue holds now, for the answer of a release that is
// about to discard it. A read that fails says so in the log and answers
// nothing: the release itself must not hang on it.
func (m *Module) queueSnapshot(ctx context.Context, tenant model.TenantID, runRef string) []queuedMessageDTO {
	if m.Data == nil {
		return nil
	}
	var out []queuedMessageDTO
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		rows, err := queueRows(ctx, sc, runRef)
		out = queuedDTOs(rows)
		return err
	})
	if err != nil {
		m.warnf("sessions: the queue of a session being released could not be read", "run_ref", runRef, "error", err)
		return nil
	}
	return out
}

// queueLockKey serializes the sends of one session's queue. It is taken before the run's
// own operation lock and never the other way round.
func queueLockKey(tenant model.TenantID, runRef string) string {
	return "queue|" + liveKey(tenant, runRef)
}

// claimQueued takes a message out of the queue before it is sent, so that no second send
// can pick it: the one with this id, or the first when id is empty (nil when the queue
// is empty).
func claimQueued(ctx context.Context, sc store.Scope, runRef, id string) (model.Record, error) {
	rows, err := queueRows(ctx, sc, runRef)
	if err != nil {
		return nil, err
	}
	var pick model.Record
	switch {
	case id != "":
		if pick, err = queueItem(rows, id); err != nil {
			return nil, err
		}
	case len(rows) > 0:
		pick = rows[0]
	default:
		return nil, nil
	}
	repo, err := sc.Ext(runQueueKind)
	if err != nil {
		return nil, err
	}
	rowID, err := model.ParseID(pick.String(model.ColID))
	if err != nil {
		return nil, err
	}
	return pick, repo.Delete(ctx, rowID)
}

// restoreQueued puts a claimed message back where it was, under its own id, when the send
// refused before any byte reached the tool.
func restoreQueued(ctx context.Context, sc store.Scope, claimed model.Record) error {
	repo, err := sc.Ext(runQueueKind)
	if err != nil {
		return err
	}
	id, err := model.ParseID(claimed.String(model.ColID))
	if err != nil {
		return err
	}
	row := model.Record{
		colRunQueueRun: claimed.String(colRunQueueRun), colRunQueueBy: claimed.String(colRunQueueBy),
		colRunQueueText: claimed.String(colRunQueueText), colRunQueuePos: claimed.Int(colRunQueuePos),
	}
	if ws := claimed.String(colRunAuthzWorkspaceID); ws != "" {
		row[colRunAuthzWorkspaceID] = ws
	}
	_, err = repo.CreateWithID(ctx, id, row)
	return err
}

// sendQueued sends one queued message (the first when id is empty) to the session's tool
// through the same input a direct send uses, and reports whether the queue had one. The
// message leaves the queue before the send, so it goes once; a refusal before any byte
// crossed puts it back. A send whose outcome is unknown is not put back: it may have
// arrived, and a second copy would be worse than a missing one the error already names.
// The caller holds the queue lock.
//
// ponytail: the message is deleted before the write, so an engine crash between the two
// loses it (at most once); the child dies with the engine, so a copy kept for the dead
// session would only be sent again after a resume. Keep a sent mark if a queued message
// must survive an engine crash.
func (m *Module) sendQueued(ctx context.Context, tenant model.TenantID, runRef, id string) (bool, error) {
	var claimed model.Record
	if err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) (err error) {
		claimed, err = claimQueued(ctx, sc, runRef, id)
		return err
	}); err != nil || claimed == nil {
		return false, err
	}
	attempted, err := m.sendTextInputAttempt(ctx, tenant, runRef, claimed.String(colRunQueueText))
	if err != nil && !attempted {
		restoreCtx := context.WithoutCancel(ctx)
		if rerr := m.Data.Mutate(restoreCtx, tenant, func(sc store.Scope) error { return restoreQueued(restoreCtx, sc, claimed) }); rerr != nil {
			m.warnf("sessions: a queued message refused by its session could not be put back", "run_ref", runRef, "error", rerr)
		}
	}
	return true, err
}

// drainRunQueue sends the first queued message when nothing holds it. It runs when a turn
// ends and when a message is queued, so a message queued just after the turn ended does
// not wait for a turn that will not come.
//
// ponytail: the send is the engine's and does not ask again whether the person who queued
// the message may still write to the session; the permission is checked when they queue,
// edit or steer. Re-check at send time if a revocation must stop queued messages at once.
func (m *Module) drainRunQueue(ctx context.Context, tenant model.TenantID, runRef string) {
	if ctx == nil || m.Data == nil {
		return
	}
	var queued bool
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		rows, err := queueRows(ctx, sc, runRef)
		queued = len(rows) > 0
		return err
	}); err != nil || !queued {
		return
	}
	release, err := m.rt.lockRunContext(ctx, queueLockKey(tenant, runRef))
	if err != nil {
		return
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil || runHasWorkBinding(rec) || m.queueHold(rec) != "" {
		return
	}
	if _, err := m.sendQueued(ctx, tenant, runRef, ""); err != nil {
		m.warnf("sessions: a queued message was not sent", "run_ref", runRef, "error", err)
	}
}

// queueTurnEnded is the bridge's word that a turn ended. The send runs off the bridge:
// it writes to the child's stdin, and the answer to that arrives through the same loop.
func (m *Module) queueTurnEnded(lr *liveRun) {
	if m.Data == nil || lr.context == nil {
		return
	}
	go m.drainRunQueue(lr.context, lr.tenant, lr.runRef)
}

// queueMutate runs fn in one transaction on the run the path names, after the checks
// every write to the queue shares: the edition rule on steering a colleague's session,
// a session that still takes messages, and one that the queue can serve.
func (m *Module) queueMutate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, fn func(sc store.Scope, run model.Record) error) bool {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return false
	}
	if m.refuseForeignSteering(w, r, mc) {
		return false
	}
	ref := chi.URLParam(r, "ref")
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(r.Context(), repo, ref)
		if err != nil {
			return err
		}
		switch {
		case run.String(colState) == stateCleaned:
			return conflictErr("a released session takes no messages")
		case runHasWorkBinding(run):
			return conflictErr("a session started by a work item takes messages through its work lease")
		}
		return fn(sc, run)
	})
	if err != nil {
		writeRunErr(w, err)
		return false
	}
	return true
}

// queueText is a message the queue may hold: some text, valid and within the bound a
// direct send has.
func queueText(text string) bool {
	return strings.TrimSpace(text) != "" && boundedText(text, 1, maxWorkTextInputBytes)
}

type queueAddRequest struct {
	Text string `json:"text"`
}

type queueOrderRequest struct {
	IDs []string `json:"ids"`
}

// handleRunQueueGet answers the session's queue and what holds it.
func (m *Module) handleRunQueueGet(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	var out runQueueDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(r.Context(), repo, chi.URLParam(r, "ref"))
		if err != nil {
			return err
		}
		out, err = m.queueState(r.Context(), sc, run)
		return err
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunQueueAdd puts a message at the end of the queue, then lets it go at once when
// the session is not busy.
func (m *Module) handleRunQueueAdd(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body queueAddRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !queueText(body.Text) {
		writeJSON(w, http.StatusBadRequest, errorBody("a queued message needs some text"))
		return
	}
	var created model.Record
	if !m.queueMutate(w, r, mc, func(sc store.Scope, run model.Record) error {
		rows, err := queueRows(r.Context(), sc, run.String(colRunRef))
		if err != nil {
			return err
		}
		if len(rows) >= maxQueuedMessages {
			return conflictErr("the queue is full; send or remove a message first")
		}
		pos := int64(1)
		if len(rows) > 0 {
			pos = rows[len(rows)-1].Int(colRunQueuePos) + 1
		}
		row := model.Record{
			colRunQueueRun: run.String(colRunRef), colRunQueueBy: mc.Principal.Actor(),
			colRunQueueText: body.Text, colRunQueuePos: pos,
		}
		if ws := run.String(colRunAuthzWorkspaceID); ws != "" {
			row[colRunAuthzWorkspaceID] = ws
		}
		repo, err := sc.Ext(runQueueKind)
		if err != nil {
			return err
		}
		created, err = repo.Create(r.Context(), row)
		return err
	}) {
		return
	}
	writeJSON(w, http.StatusCreated, queuedDTO(created))
	// After the answer's data is fixed: a send that goes now is not part of the answer.
	m.drainRunQueue(context.WithoutCancel(r.Context()), mc.Tenant, chi.URLParam(r, "ref"))
}

// handleRunQueueEdit changes the text of one queued message.
func (m *Module) handleRunQueueEdit(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body queueAddRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if !queueText(body.Text) {
		writeJSON(w, http.StatusBadRequest, errorBody("a queued message needs some text"))
		return
	}
	var updated model.Record
	if !m.queueMutate(w, r, mc, func(sc store.Scope, run model.Record) error {
		rows, err := queueRows(r.Context(), sc, run.String(colRunRef))
		if err != nil {
			return err
		}
		row, err := queueItem(rows, chi.URLParam(r, "id"))
		if err != nil {
			return err
		}
		repo, err := sc.Ext(runQueueKind)
		if err != nil {
			return err
		}
		row[colRunQueueText] = body.Text
		updated, err = repo.Update(r.Context(), row)
		return err
	}) {
		return
	}
	writeJSON(w, http.StatusOK, queuedDTO(updated))
}

// handleRunQueueOrder sets the sending order: the ids must be exactly the queue's own.
func (m *Module) handleRunQueueOrder(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body queueOrderRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	var out runQueueDTO
	if !m.queueMutate(w, r, mc, func(sc store.Scope, run model.Record) error {
		rows, err := queueRows(r.Context(), sc, run.String(colRunRef))
		if err != nil {
			return err
		}
		if len(body.IDs) != len(rows) {
			return badRequest("the order must name every queued message once")
		}
		byID := make(map[string]model.Record, len(rows))
		for _, row := range rows {
			byID[row.String(model.ColID)] = row
		}
		repo, err := sc.Ext(runQueueKind)
		if err != nil {
			return err
		}
		ordered := make([]model.Record, 0, len(rows))
		for i, id := range body.IDs {
			row, ok := byID[id]
			if !ok {
				return badRequest("the order must name every queued message once")
			}
			delete(byID, id)
			if row.Int(colRunQueuePos) != int64(i+1) {
				row[colRunQueuePos] = int64(i + 1)
				if row, err = repo.Update(r.Context(), row); err != nil {
					return err
				}
			}
			ordered = append(ordered, row)
		}
		out = runQueueDTO{RunRef: run.String(colRunRef), Items: queuedDTOs(ordered), Hold: m.queueHold(run), CanSteer: queueCanSteer(run)}
		return nil
	}) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunQueueRemove takes one message out of the queue without sending it.
func (m *Module) handleRunQueueRemove(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var out runQueueDTO
	if !m.queueMutate(w, r, mc, func(sc store.Scope, run model.Record) error {
		if _, err := claimQueued(r.Context(), sc, run.String(colRunRef), chi.URLParam(r, "id")); err != nil {
			return err
		}
		var err error
		out, err = m.queueState(r.Context(), sc, run)
		return err
	}) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunQueueSteer sends one queued message now: into the running turn through the
// tool's own input when a turn runs, or as the next message when the session waits.
func (m *Module) handleRunQueueSteer(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var run model.Record
	if !m.queueMutate(w, r, mc, func(_ store.Scope, rec model.Record) error {
		run = rec
		return nil
	}) {
		return
	}
	if m.queueHold(run) == queueHoldTurn && !queueCanSteer(run) {
		writeJSON(w, http.StatusConflict, errorBody(toolDisplayName(run.String(colRunProfileDriver))+
			" cannot take a message into a running turn; it goes when the turn ends"))
		return
	}
	ref := chi.URLParam(r, "ref")
	release, err := m.rt.lockRunContext(r.Context(), queueLockKey(mc.Tenant, ref))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	found, err := m.sendQueued(r.Context(), mc.Tenant, ref, chi.URLParam(r, "id"))
	if err != nil {
		if !found {
			writeRunErr(w, err)
			return
		}
		writeRuntimeControlErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

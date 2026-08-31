// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /runs/{id}/stream REPLAYS a persisted run as server-sent events (contract
// §2.4): one `event: case` frame per persisted case_result, then `event: summary`
// (the run aggregate), then `event: done`, then close. The runs are synchronous (no
// live progress to stream), so replay is the honest SSE: a progress reader of an
// already-recorded run. Pinned to the request tenant; the open is audited.

// streamWriteTimeout bounds a single SSE write so a stalled client cannot pin the
// handler indefinitely (mirrors orchestration.stream).
const streamWriteTimeout = 30 * time.Second

// handleStreamRun streams a completed run's per-case results, summary and a done
// sentinel, then closes.
func (m *Module) handleStreamRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid run_id"))
		return
	}

	// Load the run + its results FIRST (so a missing run is a clean 404, not a broken
	// stream), pinned to the request tenant. The open is then audited.
	var run runDTO
	var cases []caseScoreDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		runRepo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, gerr := runRepo.Get(r.Context(), id)
		if gerr != nil {
			if isNotFound(gerr) {
				return nil
			}
			return gerr
		}
		run = toRunDTO(rec)
		found = true
		resRepo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), resRepo, eq(colRunRef, id.String()))
		if err != nil {
			return err
		}
		cases = resultsToCaseScores(recs)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	m.auditStreamOpen(r, mc, id)

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // ask intermediaries not to buffer

	for _, c := range cases {
		if writeFrame(rc, w, sseFrame("case", c)) != nil {
			return
		}
		if r.Context().Err() != nil {
			return
		}
	}
	if writeFrame(rc, w, sseFrame("summary", run)) != nil {
		return
	}
	_ = writeFrame(rc, w, "event: done\ndata: {}\n\n")
}

// sseFrame marshals v and formats one named SSE frame; a marshal failure yields an
// empty-object frame so the stream never carries malformed data.
func sseFrame(event string, v any) string {
	payload, err := json.Marshal(v)
	if err != nil {
		payload = []byte("{}")
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload)
}

// writeFrame arms a finite per-write deadline (so a stalled client cannot pin the
// handler), writes the frame and flushes it. SetWriteDeadline is best-effort.
func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}

// auditStreamOpen records that a principal opened a run's SSE replay — a privileged
// read (docs/SECURITY-HARDENING.md). Best-effort: a failed audit logs but does not deny the stream
// (RBAC already gated access).
func (m *Module) auditStreamOpen(r *http.Request, mc api.ModuleContext, runID model.ID) {
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "evals.run.stream", TargetKind: runKind, TargetID: runID,
			Meta: map[string]any{"run_ref": runID.String()},
		})
		return e
	})
	if err != nil {
		m.debugf("evals: stream-open audit failed", "err", err)
	}
}

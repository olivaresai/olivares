// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

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

// streamWriteTimeout bounds a single SSE write so a stalled client cannot pin the
// goroutine (the server's hardened WriteTimeout cannot apply to a streaming handler).
const streamWriteTimeout = 30 * time.Second

// summaryFrame is the aggregate emitted after the per-step outputs on the stream.
type summaryFrame struct {
	RunID      string `json:"run_id"`
	Kind       string `json:"kind"`
	Runner     string `json:"runner"`
	Isolated   bool   `json:"isolated"`
	Status     string `json:"status"`
	StepsTotal int64  `json:"steps_total"`
	StepsOK    int64  `json:"steps_ok"`
	StepsError int64  `json:"steps_error"`
	Destroyed  bool   `json:"destroyed"`
}

// handleStream REPLAYS a persisted run as server-sent events: one `event: output`
// per persisted step (in step-key order), then `event: summary` (the run aggregate),
// then `event: done`, and closes. It is a one-shot replay of committed evidence (no
// live broker — the run already completed synchronously when it was launched), pinned
// to the request's single authorized tenant. The stream open is a privileged read and
// is audited (docs/SECURITY-HARDENING.md).
func (m *Module) handleStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}

	// Read the run + its outputs first (so a missing run is a clean 404, not a broken
	// stream), then audit the open.
	var run runDTO
	var outputs []outputDTO
	found := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		run = toRunDTO(rec)
		found = true
		items, lerr := loadRunOutputs(r.Context(), sc, id)
		if lerr != nil {
			return lerr
		}
		outputs = items
		return nil
	}); err != nil {
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

	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}

	ctx := r.Context()
	for _, o := range outputs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		payload, err := json.Marshal(o)
		if err != nil {
			continue
		}
		if writeFrame(rc, w, fmt.Sprintf("event: output\ndata: %s\n\n", payload)) != nil {
			return
		}
	}

	summary := summaryFrame{
		RunID: run.ID, Kind: run.Kind, Runner: run.Runner, Isolated: run.Isolated, Status: run.Status,
		StepsTotal: run.StepsTotal, StepsOK: run.StepsOK, StepsError: run.StepsError, Destroyed: run.Destroyed,
	}
	if payload, err := json.Marshal(summary); err == nil {
		if writeFrame(rc, w, fmt.Sprintf("event: summary\ndata: %s\n\n", payload)) != nil {
			return
		}
	}
	_ = writeFrame(rc, w, "event: done\ndata: {}\n\n")
}

// writeFrame arms a finite per-write deadline (so a stalled client cannot pin the
// goroutine), writes the frame and flushes it. SetWriteDeadline is best-effort.
func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}

// auditStreamOpen records that a principal opened a run's output stream — a
// privileged read (docs/SECURITY-HARDENING.md). Best-effort: a failed audit logs but does not deny
// the stream (the per-request RBAC check already gated access).
func (m *Module) auditStreamOpen(r *http.Request, mc api.ModuleContext, runID model.ID) {
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sandbox.run.stream", TargetKind: runKind, TargetID: runID,
		})
		return e
	})
	if err != nil {
		m.debugf("sandbox: stream-open audit failed", "err", err)
	}
}

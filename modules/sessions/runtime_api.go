// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Permission tiers for the OPERATE surface (verb-namespaced so roles grant by
// tier). Read covers list/get/events/attach; write covers create/input/stop/
// resume; admin covers the destructive cleanup/delete.
const (
	permRunRead  auth.Permission = "sessions:run:read"
	permRunWrite auth.Permission = "sessions:run:write"
	permRunAdmin auth.Permission = "sessions:run:admin"
)

// runtimePermissions are the operate permissions, appended to the module's set.
func runtimePermissions() []auth.Permission {
	return []auth.Permission{permRunRead, permRunWrite, permRunAdmin}
}

// runtimeRoutes mounts the operate endpoints under /v1/m/sessions/.
func (m *Module) runtimeRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/runs", permRunWrite, m.handleCreateRun)
	reg.Handle("GET", "/runs", permRunRead, m.handleListRuns)
	reg.Handle("GET", "/runs/{ref}", permRunRead, m.handleGetRun)
	reg.Handle("GET", "/runs/{ref}/events", permRunRead, m.handleRunEvents)
	reg.Handle("GET", "/runs/{ref}/attach", permRunRead, m.handleAttachRun)
	reg.Handle("POST", "/runs/{ref}/input", permRunWrite, m.handleRunInput)
	reg.Handle("POST", "/runs/{ref}/stop", permRunWrite, m.handleStopRun)
	reg.Handle("POST", "/runs/{ref}/resume", permRunWrite, m.handleResumeRun)
	reg.Handle("POST", "/runs/{ref}/cleanup", permRunAdmin, m.handleCleanupRun)
	reg.Handle("DELETE", "/runs/{ref}", permRunAdmin, m.handleDeleteRun)
}

func (m *Module) handleCreateRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	var body createRunRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	dto, err := m.createRun(r.Context(), mc.Tenant, CreateRunParams{
		Name:           body.Name,
		Transport:      Transport(body.Transport),
		PermissionMode: body.PermissionMode,
		Effort:         body.Effort,
		Model:          body.Model,
		WorkspaceRef:   body.WorkspaceRef,
		TemplateID:     body.TemplateID,
		Isolation:      Isolation(body.Isolation),
		EnvAllow:       body.EnvAllow,
		Actor:          mc.Principal.Actor(),
		ActorKind:      mc.Principal.ActorKind(),
		// Server-set from the AUTHENTICATED identity, never from the body: the DTO
		// has no agent_ref field and must not grow one (confused-deputy).
		AgentRef: mc.Principal.AgentIdentity,
	})
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListRuns(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	q.Cursor = ""
	q.Sort = []model.Sort{{Column: model.ColCreatedAt, Desc: true}}
	stateFilter := r.URL.Query().Get("state")
	// which runs DRIVE a given observed session — the answer to "did we launch
	// this, or did we only find it?". The join key is the provider's session id: a
	// run's claude_session_id IS the session_ref of the live/timeline tables (the
	// same identity export.go:253 resolves a recording credential through).
	//
	// It is a STORE filter, not a post-filter over the page like `state` above, and
	// that difference is the whole point. This endpoint pages by RECENCY and ignores
	// the cursor, so a filter applied after the read answers "…among the N most
	// recent runs" while looking like it answered "…among all runs". For a facet over
	// a list the operator is already scrolling, that is a narrowing they can see; for
	// the provenance of ONE session it would be a silent lie — a session launched by
	// a run older than the page would come back with zero rows, and the console would
	// render "discovered" for a session Olivares started itself. Deny-closed applies
	// to answers too: the third answer is "I did not look", never "there is none".
	//
	// A run whose id was never captured (remote-control relays its I/O to Anthropic's
	// cloud, so no init frame reaches the bridge) has NULL here and is matched by no
	// ref — correct: the plane holds no evidence linking it to an observed session.
	if ref := r.URL.Query().Get("claude_session_id"); ref != "" {
		q.Filters = append(q.Filters, eq(colClaudeSessionID, ref))
	}
	out := listResponse[runDTO]{Items: []runDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			dto := m.toRunDTO(rec)
			if stateFilter != "" && dto.State != stateFilter {
				continue
			}
			out.Items = append(out.Items, dto)
		}
		out.HasMore = page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("run ref required"))
		return
	}
	dto, err := m.getRun(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleRunEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("run ref required"))
		return
	}
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colEvRunRef, ref))
	q.Cursor = ""
	q.Sort = []model.Sort{{Column: colEvSeq, Desc: false}}
	out := listResponse[runEventDTO]{Items: []runEventDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(runEventKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toRunEventDTO(rec))
		}
		out.HasMore = page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleRunInput(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	var body inputRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	line := []byte(body.Line)
	if len(body.Message) > 0 {
		line = []byte(compactJSON(body.Message))
	}
	if len(line) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("input requires a non-empty line or message"))
		return
	}
	if body.WorkLeaseFence != nil && *body.WorkLeaseFence <= 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return
	}
	var err error
	if body.WorkLeaseFence != nil {
		err = m.InputForWork(r.Context(), mc.Tenant, ref, *body.WorkLeaseFence, line)
	} else {
		err = m.sendInput(r.Context(), mc.Tenant, ref, line)
	}
	if err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (m *Module) handleStopRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var body stopRunRequest
	if !decodeOptionalJSONBody(w, r, &body) {
		return
	}
	if (body.WorkLeaseFence != nil && *body.WorkLeaseFence <= 0) ||
		(body.WorkLeaseFence == nil && body.Reason != "") {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return
	}
	ref := chi.URLParam(r, "ref")
	if body.WorkLeaseFence != nil {
		if err := m.StopForWork(r.Context(), mc.Tenant, ref, *body.WorkLeaseFence, body.Reason); err != nil {
			writeRuntimeControlErr(w, err)
			return
		}
		dto, err := m.getRun(r.Context(), mc.Tenant, ref)
		if err != nil {
			writeRunErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dto)
		return
	}
	dto, err := m.stopRun(r.Context(), mc.Tenant, ref, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleResumeRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dto, err := m.resumeRun(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), mc.Principal.Actor(), mc.Principal.ActorKind(), mc.Principal.AgentIdentity)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleCleanupRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dto, err := m.cleanupRun(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if err := m.deleteRun(r.Context(), mc.Tenant, chi.URLParam(r, "ref")); err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleAttachRun streams a live session's bridged I/O as server-sent events,
// replaying from an optional ?from=<seq> cursor. It reuses the observe overlay's
// SSE plumbing (per-frame write deadline + heartbeat) but reads from the
// sequenced ring (lossless within retention, with an explicit gap marker) — NOT
// the lossy observe broker.
func (m *Module) handleAttachRun(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("run ref required"))
		return
	}
	// Authorize against an existing row (tenant-pinned) before opening the stream.
	rec, err := m.loadRun(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	m.auditAttach(r, mc, ref)

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}

	lr, live := m.rt.getLive(mc.Tenant, ref)
	if !live {
		_ = writeSSE(rc, w, "notice", map[string]any{
			"type": "notice", "state": m.deriveRunState(rec),
			"detail": "session is not live on this node; no bridged I/O stream",
		})
		return
	}
	if lr.transport == TransportRemoteControl {
		_ = writeSSE(rc, w, "notice", map[string]any{
			"type": "notice", "state": m.deriveRunState(rec),
			"detail": "remote-control: I/O is relayed to Anthropic cloud, not bridged",
		})
		return
	}

	cursor := parseFromSeq(r)
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		wake := lr.ring.wait() // snapshot BEFORE reading (no lost wake-up)
		rd := lr.ring.readFrom(cursor)
		if rd.gap {
			// next_seq is the floor the server resumed serving from (the contract's
			// resync point) — NOT the client's now-unrecoverable stale cursor.
			resumeSeq := rd.next
			if len(rd.frames) > 0 {
				resumeSeq = rd.frames[0].Seq
			}
			if writeSSE(rc, w, "lag", map[string]any{"type": "lag", "dropped": rd.dropped, "next_seq": resumeSeq}) != nil {
				return
			}
		}
		for _, f := range rd.frames {
			if writeSSE(rc, w, "output", attachFrame{Seq: f.Seq, Stream: f.Stream, Line: string(f.Data)}) != nil {
				return
			}
		}
		cursor = rd.next
		if rd.closed {
			_ = writeSSE(rc, w, "end", map[string]any{"type": "end"})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-ticker.C:
			if writeFrame(rc, w, ": ping\n\n") != nil {
				return
			}
		}
	}
}

// attachFrame is the SSE payload for one bridged output frame.
type attachFrame struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

// writeSSE marshals v and writes it as a named SSE event.
func writeSSE(rc *http.ResponseController, w http.ResponseWriter, event string, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil // skip an unencodable frame rather than break the stream
	}
	return writeFrame(rc, w, fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))
}

// auditAttach records that a principal attached to a live session (a privileged
// read of operated I/O). Best-effort: a failed audit logs but never denies the
// attach (RBAC already gated it) — mirroring the observe stream-open audit.
func (m *Module) auditAttach(r *http.Request, mc api.ModuleContext, ref string) {
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "sessions.run.attach", TargetKind: runKind,
			Meta: map[string]any{"run_ref": ref},
		})
		return e
	})
	if err != nil {
		m.debugf("sessions: run-attach audit failed", "err", err)
	}
}

// parseFromSeq reads the ?from=<seq> attach cursor (default 0 = from the start of
// the buffered tail).
func parseFromSeq(r *http.Request) int64 {
	if s := r.URL.Query().Get("from"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// compactJSON renders raw JSON as a single line (no embedded newlines), so a
// {"message": {...}} input body becomes one NDJSON stdin line.
func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// writeRunErr maps a typed runErr to its HTTP status, falling back to the store
// error mapper for anything else. errors.As handles a runErr the store may have
// wrapped (e.g. a precondition rejected inside a Mutate transaction).
func writeRunErr(w http.ResponseWriter, err error) {
	// ⛔ EL 409 QUE LLEVA LA FILA va PRIMERO: es el unico caso en que el cuerpo no es un
	// `errorBody` sino el recurso. Decision 1 de K2 — el codigo dice «esto no es la creacion
	// que pediste» y el cuerpo dice «y esto es lo que hay», en un solo viaje.
	var raced *racedRunErr
	if errors.As(err, &raced) {
		writeJSON(w, http.StatusConflict, raced.dto)
		return
	}
	var re *runErr
	if errors.As(err, &re) {
		writeJSON(w, re.status, errorBody(re.msg))
		return
	}
	writeStoreError(w, err)
}

// writeRuntimeControlErr preserves both error vocabularies crossed by the K2
// adapter: authority/fencing failures are WorkErrors, while a process effect
// can still return the established runErr taxonomy.
func writeRuntimeControlErr(w http.ResponseWriter, err error) {
	if asWorkError(err) != nil {
		writeWorkError(w, err)
		return
	}
	writeRunErr(w, err)
}

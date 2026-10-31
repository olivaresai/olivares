// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// terminalRoutes mounts the browser terminal of a folder under
// /v1/m/sessions/workspaces/{ref}/terminals. The output is server-sent events and the
// input a plain request, as the rest of this module's live routes: one transport, no new
// dependency, and it passes the proxies the console already passes.
//
// Starting needs the folder's write, create and erase rights (a shell can do all three)
// and the deployment's step-up; an API token has no human assurance and never starts
// one. The routes that follow act on the starter's own terminal, which any other
// person, or an ended terminal, answers as not found.
func (m *Module) terminalRoutes(reg api.RouteRegistrar, folder api.EntityRef) {
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals", permWsWrite, folder, m.handleStartTerminal)
	reg.HandleEntity("GET", "/workspaces/{ref}/terminals/{tid}/stream", permWsWrite, folder, m.handleTerminalStream)
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals/{tid}/input", permWsWrite, folder, m.handleTerminalInput)
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals/{tid}/size", permWsWrite, folder, m.handleTerminalSize)
	reg.HandleEntity("DELETE", "/workspaces/{ref}/terminals/{tid}", permWsWrite, folder, m.handleCloseTerminal)
}

// handleStartTerminal opens a shell in the folder, confined as a session in that folder is, and answers with the terminal's id. It needs the folder's write, create and erase rights and the step-up this deployment requires; an API token is refused. The optional body gives the window size in rows and columns. Each person may keep a few terminals open.
func (m *Module) handleStartTerminal(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.terminalAllowed(w, r, mc) {
		return
	}
	var body struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if !decodeOptionalJSONBody(w, r, &body) {
		return
	}
	t, err := m.startTerminal(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), terminalWhoOf(mc), body.Rows, body.Cols)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"terminal":          t.id,
		"confinement":       string(t.confinement.Mode),
		"idle_seconds":      int(terminalIdleTimeout / time.Second),
		"reconnect_seconds": int(terminalReconnectWindow / time.Second),
	})
}

// terminalAllowed applies the same current folder rights and assurance to every
// request, including reconnects. A shell cannot keep a grant that was revoked.
func (m *Module) terminalAllowed(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) bool {
	if !auth.StepUpSatisfied(r.Context(), mc.Principal) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
			"code":    "step_up_required",
			"message": "a terminal needs the step-up this deployment requires; complete it and retry",
		}})
		return false
	}
	for _, right := range []auth.TrusteeRight{auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase} {
		if !m.workspaceAllowed(w, r, mc, permWsWrite, right) {
			return false
		}
	}
	return true
}

func terminalWhoOf(mc api.ModuleContext) terminalWho {
	return terminalWho{Actor: mc.Principal.Actor(), Kind: mc.Principal.ActorKind()}
}

// terminalFrom is the caller's own terminal named by the path, or the answer is written.
func (m *Module) terminalFrom(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) (*terminal, bool) {
	t, err := m.terminalOf(mc.Tenant, chi.URLParam(r, "ref"), chi.URLParam(r, "tid"), terminalWhoOf(mc))
	if err != nil {
		writeRunErr(w, err)
		return nil, false
	}
	if !m.terminalAllowed(w, r, mc) {
		t.close("access_revoked")
		return nil, false
	}
	return t, true
}

// handleTerminalInput types text into the caller's own terminal. Each command line is recorded in the audit ledger before the shell receives its newline, and a line typed at a hidden prompt is recorded as withheld. Another person's terminal, or one that has ended, is not found.
func (m *Module) handleTerminalInput(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	t, ok := m.terminalFrom(w, r, mc)
	if !ok {
		return
	}
	var body struct {
		Data string `json:"data"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := t.send(r.Context(), body.Data); err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleTerminalSize sets the window size, in rows and columns, that the caller's own terminal reports to the shell and what runs in it.
func (m *Module) handleTerminalSize(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	t, ok := m.terminalFrom(w, r, mc)
	if !ok {
		return
	}
	var body struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := t.resize(body.Rows, body.Cols); err != nil {
		writeRunErr(w, &runErr{http.StatusGone, "the terminal has ended"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleCloseTerminal ends the caller's own terminal and everything it started, and records the close in the audit ledger. An idle terminal, or one nobody reconnects to within the reconnect window, ends the same way.
func (m *Module) handleCloseTerminal(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	t, ok := m.terminalFrom(w, r, mc)
	if !ok {
		return
	}
	t.close("closed")
	select {
	case <-t.done():
	case <-time.After(terminalDrainWait + 5*time.Second):
	}
	writeJSON(w, http.StatusOK, map[string]any{"closed": true})
}

// handleTerminalStream serves the shell's output as server-sent events: "out" frames
// carry a chunk and the offset it starts at, "exit" ends the stream. ?from=<offset>
// resumes after a dropped connection; the first frame says where the kept output really
// starts. The stream counts as an attached browser, so the reconnect window only runs
// while no stream is open.
func (m *Module) handleTerminalStream(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	t, ok := m.terminalFrom(w, r, mc)
	if !ok {
		return
	}
	from, err := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
	if r.URL.Query().Get("from") != "" && err != nil {
		writeRunErr(w, badRequest("from must be an output offset"))
		return
	}
	defer t.attach()()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}
	for {
		if !m.terminalAllowed(w, r, mc) {
			t.close("access_revoked")
			return
		}
		readCtx, cancel := context.WithTimeout(r.Context(), heartbeatInterval)
		chunk, err := t.read(readCtx, from)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil:
			if writeFrame(rc, w, ": ping\n\n") != nil {
				return
			}
			continue
		case err != nil:
			return
		case chunk.Exited:
			_ = writeFrame(rc, w, fmt.Sprintf("event: exit\ndata: {\"code\":%d}\n\n", chunk.ExitCode))
			return
		}
		frame := fmt.Sprintf("event: out\ndata: {\"o\":%d,\"d\":%q}\n\n", chunk.Offset, base64.StdEncoding.EncodeToString(chunk.Data))
		if writeFrame(rc, w, frame) != nil {
			return
		}
		from = chunk.Offset + uint64(len(chunk.Data))
	}
}

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
	reg.HandleEntity("GET", "/workspaces/{ref}/terminals", permWsWrite, folder, m.handleListTerminals)
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals", permWsWrite, folder, m.handleStartTerminal)
	reg.HandleEntity("GET", "/workspaces/{ref}/terminals/{tid}/stream", permWsWrite, folder, m.handleTerminalStream)
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals/{tid}/input", permWsWrite, folder, m.handleTerminalInput)
	reg.HandleEntity("POST", "/workspaces/{ref}/terminals/{tid}/size", permWsWrite, folder, m.handleTerminalSize)
	reg.HandleEntity("DELETE", "/workspaces/{ref}/terminals/{tid}", permWsWrite, folder, m.handleCloseTerminal)
}

// handleListTerminals lists the caller's own open terminals in the folder, oldest first, so a browser that lost the ids of its terminals (another browser, cleared site data) can reach them again and close them. It needs the same rights as starting one. Another person's terminals are never listed.
func (m *Module) handleListTerminals(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.terminalAllowed(w, r, mc) {
		return
	}
	items := []map[string]any{}
	for _, t := range m.terminals.listOf(mc.Tenant, chi.URLParam(r, "ref"), terminalWhoOf(mc)) {
		items = append(items, map[string]any{"terminal": t.id, "confinement": string(t.confinement.Mode), "started_at": t.started.UTC().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
	go m.terminalWatch(t, mc)
	writeJSON(w, http.StatusCreated, map[string]any{
		"terminal":    t.id,
		"confinement": string(t.confinement.Mode),
		// Both fields are kept for clients that read them; 0 says there is no such limit.
		"idle_seconds":      0,
		"reconnect_seconds": 0,
	})
}

// terminalRights are the folder rights, each asked under permWsWrite, that a terminal needs. SFTP
// asks the same ones, at its door and at every rights check of a credential.
var terminalRights = []auth.TrusteeRight{auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase}

// terminalAllowed applies the same current folder rights and assurance to every
// request, including reconnects. A request that fails them is refused.
func (m *Module) terminalAllowed(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) bool {
	allowed, _ := m.terminalAccess(w, r, mc)
	return allowed
}

// terminalAccess is terminalAllowed that also says whether a refusal is definite: the
// person no longer holds the folder rights (accessLost). A step-up that went stale, or a check
// that could not be answered, refuses the request and is not definite.
func (m *Module) terminalAccess(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) (allowed, definite bool) {
	if !auth.StepUpSatisfied(r.Context(), mc.Principal) {
		writeJSON(w, http.StatusForbidden, api.ErrorBody("step_up_required",
			"a terminal needs the step-up this deployment requires; complete it and retry"))
		return false, false
	}
	if _, err := m.loadWorkspaceRec(r.Context(), mc.Tenant, chi.URLParam(r, "ref")); err != nil {
		writeRunErr(w, err)
		return false, accessLost(err)
	}
	for _, right := range terminalRights {
		if err := m.authorizeWorkspaceRight(r.Context(), mc, permWsWrite, right, mc.Resource); err != nil {
			writeRunErr(w, err)
			return false, accessLost(err)
		}
	}
	return true, false
}

// accessLost reports whether err proves the person no longer has the access: the
// credential is gone or its tenant admission withdrawn, or the folder or a right was refused
// (403) or is not there (404). Anything else (an unavailable store or evidence, a deadline)
// says nothing about the right and never ends a shell.
func accessLost(err error) bool {
	var refused *runErr
	switch {
	case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrPrincipalScopeAdmissionRequired), errors.Is(err, auth.ErrCredentialBindingCeiling):
		return true
	case errors.As(err, &refused):
		return refused.status == http.StatusForbidden || refused.status == http.StatusNotFound
	}
	return false
}

// currentFolderAccess asks, for the person held in mc, what a terminal asks at every request: the
// person is read again from the credential they signed in with, as a detached operation does, the
// folder is still there and active, and they hold the rights a terminal needs in it. It returns mc
// with the person as they are now.
func (m *Module) currentFolderAccess(ctx context.Context, mc api.ModuleContext, folderRef string) (api.ModuleContext, error) {
	ref, ok := mc.Principal.Ref()
	if !ok || !m.CommunicationAuthority.bound() {
		return mc, errSFTPRefused
	}
	current, err := m.CommunicationAuthority.resolver.ResolvePrincipalScope(ctx, ref, mc.Tenant)
	if err != nil {
		return mc, err
	}
	mc.Principal = current
	rec, err := m.loadWorkspaceRec(ctx, mc.Tenant, folderRef)
	if err != nil {
		return mc, err
	}
	if rec.String(colWsState) == wsDisabled {
		return mc, forbiddenErr("the folder is disabled")
	}
	for _, right := range terminalRights {
		if err := m.authorizeWorkspaceRight(ctx, mc, permWsWrite, right, mc.Resource); err != nil {
			return mc, err
		}
	}
	return mc, nil
}

// terminalUnverifiedGrace is how long a person's standing may stay unprovable before the shell
// ends: the same bound as an operated session's owner check (ownerAccessReadGrace). The core
// answers "evidence unavailable", not "unauthenticated", for a person who was removed from the
// tenant, so a shell kept past a store outage must still end when standing never comes back.
var terminalUnverifiedGrace = ownerAccessReadGrace

// terminalWatch ends t when its person loses access while nobody reads from it, until t ends: at
// once on a proven loss, and when standing could not be proven for terminalUnverifiedGrace. mc holds
// only what the rights check reads, never the request.
func (m *Module) terminalWatch(t *terminal, held api.ModuleContext) {
	mc := api.ModuleContext{Principal: held.Principal, Tenant: held.Tenant, Resource: held.Resource, Admission: held.Admission}
	tick := time.NewTicker(sftpRecheckInterval)
	defer tick.Stop()
	var unverifiedSince time.Time
	for {
		select {
		case <-t.done():
			return
		case <-tick.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := m.currentFolderAccess(ctx, mc, t.folderRef)
		cancel()
		switch {
		case err == nil:
			unverifiedSince = time.Time{}
		case accessLost(err):
			t.close("access_revoked")
			return
		default:
			if unverifiedSince.IsZero() {
				unverifiedSince = time.Now()
			}
			if time.Since(unverifiedSince) >= terminalUnverifiedGrace {
				t.close("access_unverified")
				return
			}
		}
	}
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
	if allowed, definite := m.terminalAccess(w, r, mc); !allowed {
		if definite {
			t.close("access_revoked")
		}
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

// handleCloseTerminal ends the caller's own terminal and everything it started, and records the close in the audit ledger. A terminal has no time limit: it runs until it is closed here, its shell exits, its folder is removed, access to it is revoked or the server stops.
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
// starts. A terminal keeps running, and keeps its output tail, while no stream is open.
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
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if writeFrame(rc, w, ": connected\n\n") != nil {
		return
	}
	for {
		if allowed, definite := m.terminalAccess(w, r, mc); !allowed {
			if definite {
				t.close("access_revoked")
			}
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

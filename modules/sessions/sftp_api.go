// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// sftpRoutes mounts the SFTP and SCP access of a folder under
// /v1/m/sessions/workspaces/{ref}/sftp. Both need the rights a terminal needs, so they are registered
// with the terminal's permission. Both answer 404 where this server does not serve it and 403 to a
// person without those rights, which is how the console knows to show nothing.
func (m *Module) sftpRoutes(reg api.RouteRegistrar, folder api.EntityRef) {
	reg.HandleEntity("GET", "/workspaces/{ref}/sftp", permWsWrite, folder, m.handleSFTPStatus)
	reg.HandleEntity("POST", "/workspaces/{ref}/sftp", permWsWrite, folder, m.handleSFTPConnect)
}

var errSFTPOff = &runErr{http.StatusNotFound, "SFTP and SCP are not available on this server"}

// handleSFTPStatus tells whether this server serves SFTP and SCP for the folder, and where: the user name, the port and the fingerprint of the host key a client should compare. It answers 404 where the server does not serve it, because the listener is off or the host cannot confine a child to the folder, and 403 to a person without the folder's write, create and erase rights.
func (m *Module) handleSFTPStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.sftpAllowed(w, r, mc) {
		return
	}
	s := m.sftp.Load()
	if s == nil {
		writeRunErr(w, errSFTPOff)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": sftpUser, "port": s.port(), "host_key": s.fingerprint()})
}

// handleSFTPConnect issues a short-lived credential to copy files in and out of the folder with sftp or scp, and answers with the private key, shown once and never kept. It needs the folder's read, write, create and erase rights, the ones a terminal needs, and the credential only reads in a folder mounted read-only; the same rights are asked again at every connection and every thirty seconds of an open session, so a revoked right ends it. It needs the deployment's step-up, so an API token is refused, and each credential is recorded in the audit ledger.
func (m *Module) handleSFTPConnect(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.sftpAllowed(w, r, mc) {
		return
	}
	s := m.sftp.Load()
	if s == nil {
		writeRunErr(w, errSFTPOff)
		return
	}
	if !auth.StepUpSatisfied(r.Context(), mc.Principal) {
		writeJSON(w, http.StatusForbidden, api.ErrorBody("step_up_required",
			"SFTP needs the step-up this deployment requires; complete it and retry"))
		return
	}
	ws, err := m.resolveLaunchWorkspace(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	if ws == nil {
		writeRunErr(w, badRequest("SFTP needs a folder"))
		return
	}
	defer ws.closeReadOnlyHandles()
	cred, key, err := s.issue(r.Context(), mc, ws, ws.mountMode == mountRO)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":        sftpUser,
		"port":        s.port(),
		"host_key":    s.fingerprint(),
		"private_key": key,
		"read_only":   cred.readOnly,
		"expires_at":  cred.expires.UTC().Format(time.RFC3339),
		"expires_in":  int(sftpCredentialTTL / time.Second),
	})
}

// sftpAllowed is the door of both routes: the folder's read right and the write, create and erase
// rights a terminal needs, or the answer is written. A person who may only read gets no SFTP:
// sftp-server reads the host's system files (/etc/passwd and /etc/group among them), and its reads
// pass neither the size limit nor the DLP posture the file API applies, so it would read more than
// that person may.
func (m *Module) sftpAllowed(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) bool {
	if !m.workspaceAllowed(w, r, mc, permWsRead, auth.TrusteeRead) {
		return false
	}
	for _, right := range terminalRights {
		if !m.workspaceAllowed(w, r, mc, permWsWrite, right) {
			return false
		}
	}
	return true
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type osAccountBeginInput struct {
	Tenant  model.TenantID `json:"tenant"`
	User    model.ID       `json:"user_id"`
	Account string         `json:"account"`
}
type osAccountCompleteInput struct {
	Ceremony model.ID `json:"ceremony_id"`
	// JSON's byte format is base64; the decoded buffer belongs to this request.
	Password []byte `json:"password"`
}

func (s *Server) osAccountPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	// Only the actual listener handshake qualifies. A forwarded header is no proof.
	if r.TLS == nil || r.URL.RawQuery != "" {
		s.writeError(w, r, errForbidden)
		return auth.Principal{}, false
	}
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, false
	}
	if p.Kind != auth.KindUser {
		s.writeError(w, r, errForbidden)
		return auth.Principal{}, false
	}
	return p, true
}

// handleOSAccountBegin reserves a single-use binding ceremony under the current
// administrator's native user authorization and configured step-up policy. The
// named subject must finish from its own session with fresh native account control.
func (s *Server) handleOSAccountBegin(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	var in osAccountBeginInput
	if err := DecodeRequestBody(w, r, &in, RequestBodySpec{MaxBytes: 8192}); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid OS account binding request"))
		return
	}
	out, err := s.osAccounts.Begin(r.Context(), p, in.Tenant, in.User, in.Account)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOSAccountComplete consumes the ceremony from the subject's own native
// product session, authenticates the exact OS account and validates PAM Account.
// The password buffer is cleared on every return and is never an audit field.
func (s *Server) handleOSAccountComplete(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	var in osAccountCompleteInput
	defer func() { clear(in.Password) }()
	if err := DecodeRequestBody(w, r, &in, RequestBodySpec{MaxBytes: 8192}); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid OS account proof request"))
		return
	}
	out, err := s.osAccounts.Complete(r.Context(), p, in.Ceremony, in.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) osAccountTarget(w http.ResponseWriter, r *http.Request) (auth.Principal, model.TenantID, model.ID, bool) {
	p, ok := s.osAccountPrincipal(w, r)
	if !ok {
		return p, "", "", false
	}
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil {
		s.badRequest(w, r, "invalid user ID")
		return p, "", "", false
	}
	tenant, err := s.resolveTenant(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return p, "", "", false
	}
	return p, tenant, id, true
}

// handleOSAccountRead returns administrative mapping metadata and its audit
// digest. Opaque binding handles and native credential references remain private.
func (s *Server) handleOSAccountRead(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	id := model.ID(mc.Resource.ID)
	out, err := s.osAccounts.Read(r.Context(), p, tenant, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOSAccountRevoke ends current binding authority while retaining the
// immutable account/UID/subject reservation and its existing audited history.
func (s *Server) handleOSAccountRevoke(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	id := model.ID(mc.Resource.ID)
	if err := s.osAccounts.Revoke(r.Context(), p, tenant, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Console user onboarding (FASE X). Unlike POST /v1/users (superadmin-only,
// creates a global principal), these are TENANT-SCOPED: any caller with
// membership:write in the resolved tenant (superadmin, the tenant admin/owner, or
// anyone the RBAC layer grants it) onboards a person INTO that tenant — never as a
// superadmin, with the granted role ceiling-checked, and behind an AAL3 step-up.
// Two modes: admin-set password (active at once) or a single-use email invite the
// invitee redeems to set their own password. The accept leg is unauthenticated
// (the invitee has no session yet); the token is the gate.

// InviteSender optionally delivers an invitation link by email (e.g. over the
// notify module). It is best-effort: when nil, or on a send error, the invite
// token is still returned to the admin once (show-once) so it can be relayed
// out-of-band. The composition root wires it; the API never holds a mailer.
type InviteSender interface {
	SendInvite(ctx context.Context, email, acceptURL string, expiresAt time.Time) error
}

// onboardInput is the POST /v1/onboard payload.
type onboardInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	// Mode is "password" (admin-set initial password) or "invite" (email a
	// single-use token). Empty defaults to "password".
	Mode     string `json:"mode"`
	Password string `json:"password"`
}

// acceptInviteInput is the POST /v1/invites/accept payload (unauthenticated).
type acceptInviteInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleOnboardMember creates-or-reuses an account and grants its tenant
// membership. membership:write + AAL3.
func (s *Server) handleOnboardMember(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "membership:write")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	var in onboardInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Mode != "" && in.Mode != "password" && in.Mode != "invite" {
		s.badRequest(w, r, "mode must be \"password\" or \"invite\"")
		return
	}
	res, err := s.authr.OnboardMember(r.Context(), p, tenant, auth.OnboardInput{
		Email: in.Email, DisplayName: in.DisplayName, Role: in.Role,
		Password: in.Password, Invite: in.Mode == "invite",
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := map[string]any{
		"user":    toUserDTO(res.User),
		"created": res.Created,
		"membership": map[string]any{
			"id": res.Membership.ID.String(), "user_id": res.Membership.UserID.String(),
			"tenant": res.Membership.TargetTenantID.String(), "role": res.Membership.Role,
		},
	}
	if res.InviteToken != "" {
		// Put the bearer token in the URL fragment, which browsers never send in
		// the HTTP request or Referer header. A query token would be captured by
		// ordinary ingress/access logs before the SPA had any chance to scrub it.
		acceptURL := schemeHost(r) + "/accept-invite#token=" + res.InviteToken
		var expStr string
		var expTime time.Time
		if res.ExpiresAt != nil {
			expStr = res.ExpiresAt.String()
			expTime = res.ExpiresAt.Time()
		}
		// Best-effort email delivery; the token is returned regardless (show-once).
		if s.inviteSender != nil {
			if err := s.inviteSender.SendInvite(r.Context(), res.User.Email, acceptURL, expTime); err != nil {
				s.log.Warn("api: invite email delivery failed; token returned for out-of-band relay",
					"err", err, "invite", res.InviteID.String(), "request_id", requestID(r.Context()))
			}
		}
		out["invite"] = map[string]any{
			"id": res.InviteID.String(), "token": res.InviteToken,
			"accept_url": acceptURL, "expires_at": expStr,
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleAcceptInvite redeems an invite token: sets the password, activates the
// account and mints a session. Unauthenticated (the token is the gate).
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var in acceptInviteInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	token, sess, err := s.authr.AcceptInvite(r.Context(), in.Token, in.Password, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "session_id": sess.ID.String(), "expires_at": sess.ExpiresAt.String(),
	})
}

// handleListInvites lists a tenant's pending (unaccepted, unexpired) invitations,
// without any token material. membership:read.
func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.authzTenant(w, r, "membership:read")
	if !ok {
		return
	}
	invites, err := s.authr.ListPendingInvites(r.Context(), tenant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := listResponse[InviteDTO]{Items: []InviteDTO{}}
	for _, inv := range invites {
		out.Items = append(out.Items, toInviteDTO(inv))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeInvite deletes a pending invitation. membership:write.
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "membership:write")
	if !ok {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if err := s.authr.RevokeInvite(r.Context(), p, tenant, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// InviteDTO is the JSON shape of a pending invitation (never any token material).
type InviteDTO struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Tenant    string `json:"tenant"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
}

func toInviteDTO(i model.UserInvite) InviteDTO {
	return InviteDTO{
		ID: i.ID.String(), Email: i.Email, Tenant: i.TargetTenantID.String(), Role: i.Role,
		ExpiresAt: i.ExpiresAt.String(), CreatedAt: i.CreatedAt.String(),
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
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

// InviteSender delivers an invitation by email, to the invitee's address only.
// The sender builds the link itself, from the console address the operator
// declared for the deployment, with token in its fragment; the API never builds
// a link, so no request header can choose where a token is sent. When it is nil,
// invite mode answers 409 invite_delivery_unavailable and writes nothing, unless
// the administrator asks for delivery "link"; a failed send is reported as the
// invitation's delivery state, and a resend with a new secret retries it. The
// composition root wires it; the API never holds a mailer.
type InviteSender interface {
	SendInvite(ctx context.Context, email, token string, expiresAt time.Time) error
}

// inviteDeliveryLink hands an invitation's token to the administrator's own
// session, once, for the link the console shows on the screen: the day-one path
// of a server with no mail. The token takes exactly one path, the mail or this
// response, never a list, a log or the audit. Only a human session past the
// administrative step-up reaches it (requireStepUp refuses every API token).
const inviteDeliveryLink = "link"

// onboardInput is the POST /v1/onboard payload.
type onboardInput struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	// Mode is "password" (admin-set initial password) or "invite" (email a
	// single-use token). Empty defaults to "password".
	Mode     string `json:"mode"`
	Password string `json:"password"`
	// Delivery "link" returns an invite's token in the response instead of
	// mailing it. Empty mails it.
	Delivery string `json:"delivery"`
}

// acceptInviteInput is the POST /v1/invites/accept payload (unauthenticated).
type acceptInviteInput struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleOnboardMember creates-or-reuses an account and grants its tenant
// membership. membership:write + AAL3.
func (s *Server) handleOnboardMember(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	if !s.requireStepUp(w, r, p) {
		return
	}
	var in onboardInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	if in.Mode != "" && in.Mode != "password" && in.Mode != "invite" {
		s.badRequest(w, r, "mode must be \"password\" or \"invite\"")
		return
	}
	if err := auth.ValidateEmail(in.Email); err != nil {
		s.writeError(w, r, err)
		return
	}
	if in.Delivery != "" && (in.Delivery != inviteDeliveryLink || in.Mode != "invite") {
		s.badRequest(w, r, "delivery must be \"link\", with mode \"invite\"")
		return
	}
	if in.Mode == "invite" && in.Delivery == "" && s.inviteSender == nil {
		s.writeError(w, r, auth.ErrInviteDeliveryUnavailable)
		return
	}
	res, err := s.authr.OnboardMember(r.Context(), p, tenant, auth.OnboardInput{
		Email: in.Email, DisplayName: in.DisplayName, Role: in.Role,
		Password: in.Password, Invite: in.Mode == "invite",
	})
	if errors.Is(err, auth.ErrConsentRequired) {
		writeConsentRequired(w)
		return
	}
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
		var expStr string
		if res.ExpiresAt != nil {
			expStr = res.ExpiresAt.String()
		}
		inv := s.handOffInvite(w, r, in.Delivery, res.User.Email, res.InviteID, res.InviteToken, res.ExpiresAt)
		inv["id"], inv["expires_at"] = res.InviteID.String(), expStr
		out["invite"] = inv
	}
	writeJSON(w, http.StatusCreated, out)
}

// handOffInvite sends an invitation's token down its one path and returns the
// response fields that say which: delivery "link" with the token itself (the
// response must then never be cached), or the mail's delivery state.
func (s *Server) handOffInvite(w http.ResponseWriter, r *http.Request, delivery, email string, id model.ID, token string, expiresAt *model.Timestamp) map[string]any {
	if delivery == inviteDeliveryLink {
		w.Header().Set("Cache-Control", "no-store")
		return map[string]any{"delivery": inviteDeliveryLink, "token": token}
	}
	return map[string]any{"delivery": s.deliverInvite(r, email, id, token, expiresAt)}
}

// deliverInvite hands the invitation's token to the mailer for the invitee's
// address and returns the delivery state ("sent" or "failed").
func (s *Server) deliverInvite(r *http.Request, email string, id model.ID, token string, expiresAt *model.Timestamp) string {
	var exp time.Time
	if expiresAt != nil {
		exp = expiresAt.Time()
	}
	if err := s.inviteSender.SendInvite(r.Context(), email, token, exp); err != nil {
		s.log.Warn("api: invitation delivery failed; a resend issues a new secret",
			"err", err, "invite", id.String(), "request_id", requestID(r.Context()))
		return "failed"
	}
	return "sent"
}

// handleResendInvite rotates a pending invitation's secret, which voids the old
// link, and mails the new one to the invitee, or with ?delivery=link returns it
// once to this session. membership:write + AAL3; 409 invite_delivery_unavailable
// without a mailer and without delivery=link.
func (s *Server) handleResendInvite(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	if !s.requireStepUp(w, r, p) {
		return
	}
	delivery := r.URL.Query().Get("delivery")
	if delivery != "" && delivery != inviteDeliveryLink {
		s.badRequest(w, r, "delivery must be \"link\"")
		return
	}
	if delivery == "" && s.inviteSender == nil {
		s.writeError(w, r, auth.ErrInviteDeliveryUnavailable)
		return
	}
	inv, token, err := s.authr.ResendInvite(r.Context(), p, tenant, model.ID(chi.URLParam(r, "id")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	exp := inv.ExpiresAt
	out := s.handOffInvite(w, r, delivery, inv.Email, inv.ID, token, &exp)
	out["id"], out["expires_at"] = inv.ID.String(), inv.ExpiresAt.String()
	writeJSON(w, http.StatusAccepted, out)
}

// handleAcceptInvite redeems an invite token: sets the password, activates the
// account and mints a session. Unauthenticated (the token is the gate).
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in acceptInviteInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	token, sess, err := s.authr.AcceptInvite(r.Context(), in.Token, in.Password, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionEnvelope(w, r, token, sess))
}

// handleListInvites lists a tenant's pending (unaccepted, unexpired) invitations,
// without any token material. membership:read.
func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	tenant := mc.Tenant
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
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
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

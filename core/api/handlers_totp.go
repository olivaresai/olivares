// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/base64"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TOTP second-factor surface . Three callers meet here:
//
//   - an anonymous login holding a pending challenge (mfa_token in the body):
//     challenge/activate/enrol-for-login;
//   - an authenticated session managing its own factor (enrol/activate/
//     status/remove);
//   - an administrator managing another account's factor and the deployment
//     policy (users/{id}/totp, totp/policy).
//
// The wire never carries the seed after enrolment: the enrolment response shows
// it once (secret + otpauth URI + QR), the store keeps it sealed, and every
// later response is the non-secret TOTPStatus shape.

// totpQRSize is the rendered QR's pixel size: fits the identity card at 390 px
// and scans from a laptop screen at arm's length.
const totpQRSize = 240

type totpEnrolInput struct {
	// MFAToken carries the pending-login credential on the policy-forced
	// enrolment path (no session exists yet). Empty on the self-service path.
	MFAToken string `json:"mfa_token"`
}

type totpActivateInput struct {
	MFAToken string `json:"mfa_token"`
	Code     string `json:"code"`
}

type totpChallengeInput struct {
	MFAToken     string `json:"mfa_token"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

type totpPolicyInput struct {
	RequireForAdmins *bool `json:"require_for_admins"`
}

// handleTOTPEnrol starts an enrolment: from the acting session (self-service),
// or from a pending login when the mfa_token is supplied (the policy requires
// a factor and the account has none). The response is shown once.
func (s *Server) handleTOTPEnrol(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in totpEnrolInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body: expected {mfa_token?}"))
		return
	}
	var (
		enrol *auth.TOTPEnrolment
		err   error
	)
	if in.MFAToken != "" {
		enrol, err = s.authr.BeginTOTPEnrolmentForLogin(r.Context(), in.MFAToken, "Olivares AI")
	} else {
		p, ok := s.sessionPrincipal(w, r)
		if !ok {
			return
		}
		enrol, err = s.authr.BeginTOTPEnrolment(r.Context(), p, "Olivares AI")
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	qr, err := auth.TOTPQRPNG(enrol.URI, totpQRSize)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":        enrol.Secret,
		"uri":           enrol.URI,
		"algorithm":     enrol.Algorithm,
		"digits":        enrol.Digits,
		"period":        enrol.Period,
		"qr_png_base64": base64.StdEncoding.EncodeToString(qr),
	})
}

// handleTOTPActivate confirms an enrolment with a code. Self-service: returns
// the recovery codes exactly once. Pending-login (policy-forced): the verified
// code completes the login, so the session response is returned together with
// the recovery codes.
func (s *Server) handleTOTPActivate(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in totpActivateInput
	if err := decodeJSON(w, r, &in); err != nil || auth.ReformatCode(in.Code) == "" {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body: expected {code[, mfa_token]}"))
		return
	}
	code := auth.ReformatCode(in.Code)
	if in.MFAToken != "" {
		token, sess, codes, err := s.authr.FinishTOTPEnrolmentForLogin(r.Context(), in.MFAToken, code, clientIP(r), r.Header.Values("X-Forwarded-For"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out := SessionEnvelope(w, r, token, sess)
		out["recovery_codes"] = codes
		writeJSON(w, http.StatusOK, out)
		return
	}
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	codes, err := s.authr.FinishTOTPEnrolment(r.Context(), p, code)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// handleTOTPChallenge completes a factor-gated login with a TOTP code or a
// recovery code.
func (s *Server) handleTOTPChallenge(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in totpChallengeInput
	if err := decodeJSON(w, r, &in); err != nil || in.MFAToken == "" {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body: expected {mfa_token, code | recovery_code}"))
		return
	}
	var code string
	switch {
	case in.Code != "":
		code = auth.ReformatCode(in.Code)
	case in.RecoveryCode != "":
		code = auth.ReformatCode(in.RecoveryCode)
	default:
		s.badRequest(w, r, "expected {mfa_token, code | recovery_code}")
		return
	}
	token, sess, err := s.authr.CompleteTOTPLogin(r.Context(), in.MFAToken, code, clientIP(r), r.Header.Values("X-Forwarded-For"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionEnvelope(w, r, token, sess))
}

// handleTOTPStatus reports the acting account's factor (non-secret shape).
func (s *Server) handleTOTPStatus(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	status, err := s.authr.TOTPStatusOf(r.Context(), p.UserID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleTOTPRemove deletes the acting account's own factor. AAL3-gated like
// removing a passkey: the second factor cannot be dropped from a plain
// password session.
func (s *Server) handleTOTPRemove(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	if err := s.authr.RemoveTOTP(r.Context(), p); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTOTPPolicyGet reads the require-for-administrators policy.
func (s *Server) handleTOTPPolicyGet(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	on, err := s.authr.RequireTOTPForAdmins(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_for_admins": on})
}

// handleTOTPPolicyPut sets the require-for-administrators policy. AAL3-gated:
// this knob decides who may log in with just a password.
func (s *Server) handleTOTPPolicyPut(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	var in totpPolicyInput
	if err := decodeJSON(w, r, &in); err != nil || in.RequireForAdmins == nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body: expected {require_for_admins: bool}"))
		return
	}
	if err := s.authr.SetRequireTOTPForAdmins(r.Context(), p, *in.RequireForAdmins); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_for_admins": *in.RequireForAdmins})
}

// handleUserTOTPStatus reports another account's factor to an administrator
// (tenant-scoped read, the members-grid permission).
func (s *Server) handleUserTOTPStatus(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	tenant := mc.Tenant
	accountID, ok := totpAccountParam(w, r)
	if !ok {
		return
	}
	status, err := s.authr.TOTPStatusInTenant(r.Context(), tenant, accountID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleUserTOTPReset is the administrator's factor reset: deletes the account's
// factor and recovery codes. Tenant-scoped membership write + AAL3, exactly the
// onboarding gates — the same population that can issue an invite may clear a
// lost factor.
func (s *Server) handleUserTOTPReset(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
	if !s.requireStepUp(w, r, p) {
		return
	}
	accountID, ok := totpAccountParam(w, r)
	if !ok {
		return
	}
	if err := s.authr.ResetTOTPInTenant(r.Context(), p, tenant, accountID); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// totpAccountParam parses and shape-checks the {id} path segment.
func totpAccountParam(w http.ResponseWriter, r *http.Request) (model.ID, bool) {
	id, err := model.ParseID(chi.URLParam(r, "id"))
	if err != nil || id.IsZero() {
		http.Error(w, "invalid account id", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

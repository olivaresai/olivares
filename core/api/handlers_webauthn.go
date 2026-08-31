// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// WebAuthn privileged-login ceremony endpoints — the backend
// of the declared seam:
//
//	POST /v1/auth/webauthn/register/options     (no body) -> {publicKey: ...}
//	POST /v1/auth/webauthn/register             {credential: <attestation>} -> {ok}
//	POST /v1/auth/webauthn/authenticate/options (no body) -> {publicKey: ...}
//	POST /v1/auth/webauthn/authenticate         {credential: <assertion>} -> {ok, aal}
//
// All four require an authenticated SESSION principal: the ceremony registers
// an authenticator for, or elevates, the calling session — there is no
// anonymous leg, so options never reveal whether a user/credential exists.
// Verification lives in core/auth (webauthn.go); the handlers only translate
// the wire envelope. The panel re-reads whoami to lift its gate, so elevation
// must be (and is) persisted on the session, not just echoed here.

// webauthnRP resolves the relying party for this request: the configured RP
// (composition root, env) wins; otherwise it is derived from the proxy-aware
// external URL — the RP ID is the bare hostname and the origin the exact
// scheme://host the browser sees. The library independently verifies the
// client-data origin against it, so a spoofed Host can only fail the ceremony.
func (s *Server) webauthnRP(r *http.Request) auth.WebAuthnRP {
	if s.webauthn.ID != "" {
		return s.webauthn
	}
	origin := schemeHost(r)
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return auth.WebAuthnRP{ID: host, DisplayName: "Olivares AI", Origins: []string{origin}}
}

// sessionPrincipal returns the calling SESSION principal or writes the error:
// the WebAuthn ceremonies bind to a revocable human session (an API token has
// no session to elevate).
func (s *Server) sessionPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return auth.Principal{}, false
	}
	if p.Kind != auth.KindUser {
		s.badRequest(w, r, "webauthn ceremonies apply to session principals")
		return auth.Principal{}, false
	}
	return p, true
}

// credentialEnvelope is the panel's POST body: the encoded browser response
// wrapped in a single "credential" key + an optional user-supplied display name.
type credentialEnvelope struct {
	Credential json.RawMessage `json:"credential"`
	Name       string          `json:"name"`
}

// handleWebAuthnRegisterOptions issues creation options (challenge) to register
// a new authenticator for the calling session's user.
func (s *Server) handleWebAuthnRegisterOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	creation, err := s.authr.BeginWebAuthnRegistration(r.Context(), p, s.webauthnRP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, creation) // marshals as {"publicKey": {...}}
}

// handleWebAuthnRegister verifies the browser's attestation and persists the
// credential. 403 webauthn_verification_failed on any ceremony failure; 409 on
// an already-registered credential id.
func (s *Server) handleWebAuthnRegister(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	var in credentialEnvelope
	if err := decodeJSON(w, r, &in); err != nil || len(in.Credential) == 0 {
		s.badRequest(w, r, "invalid JSON body: expected {credential: ...}")
		return
	}
	rp := s.webauthnRP(r)
	if err := s.authr.FinishWebAuthnRegistration(r.Context(), p, rp, in.Credential, in.Name); err != nil {
		s.logCeremonyFailure("registration", rp, err)
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWebAuthnAuthOptions issues assertion options (challenge) for a step-up
// of the calling session.
func (s *Server) handleWebAuthnAuthOptions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	assertion, err := s.authr.BeginWebAuthnStepUp(r.Context(), p, s.webauthnRP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assertion) // marshals as {"publicKey": {...}}
}

// handleWebAuthnAuthenticate verifies the browser's assertion and elevates the
// calling session to AAL3 (the panel re-reads whoami to lift its gate).
func (s *Server) handleWebAuthnAuthenticate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	var in credentialEnvelope
	if err := decodeJSON(w, r, &in); err != nil || len(in.Credential) == 0 {
		s.badRequest(w, r, "invalid JSON body: expected {credential: ...}")
		return
	}
	rp := s.webauthnRP(r)
	sess, err := s.authr.FinishWebAuthnStepUp(r.Context(), p, rp, in.Credential)
	if err != nil {
		s.logCeremonyFailure("step-up", rp, err)
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "aal": sess.AAL})
}

// logCeremonyFailure leaves an operator-diagnosable trace for a refused
// ceremony: the client only gets the deliberately coarse 403, so the derived
// relying party (the #1 misconfiguration: a proxy not forwarding
// X-Forwarded-Proto makes the origin check fail every ceremony) goes to the
// server log. Never the challenge or any credential material.
func (s *Server) logCeremonyFailure(leg string, rp auth.WebAuthnRP, err error) {
	s.log.Warn("api: webauthn ceremony refused",
		"leg", leg, "rp_id", rp.ID, "origins", rp.Origins, "err", err)
}

// handleWebAuthnList returns the calling user's registered authenticators —
// id, label and registration time only, never key material.
func (s *Server) handleWebAuthnList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := s.authr.ListWebAuthnCredentials(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	type credFlags struct {
		Flags struct {
			BackupEligible bool `json:"backupEligible"`
		} `json:"flags"`
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"id": row.ID.String(), "name": row.Name, "created_at": row.CreatedAt.String(),
		}
		var cf credFlags
		if json.Unmarshal(row.Credential, &cf) == nil {
			item["backup_eligible"] = cf.Flags.BackupEligible
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleWebAuthnDelete unregisters one of the calling user's authenticators
// (lost/stolen-key remediation). AAL3-required and ledgered in core/auth.
func (s *Server) handleWebAuthnDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		s.badRequest(w, r, "invalid credential id")
		return
	}
	if err := s.authr.DeleteWebAuthnCredential(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWebAuthnRename updates the display name of one of the calling user's
// authenticators. Owner-only, no AAL3 required (metadata change), ledgered.
func (s *Server) handleWebAuthnRename(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sessionPrincipal(w, r)
	if !ok {
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		s.badRequest(w, r, "invalid credential id")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.Name == "" {
		s.badRequest(w, r, "name is required")
		return
	}
	if err := s.authr.RenameWebAuthnCredential(r.Context(), p, id, in.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

// Runtime secret-store endpoints: the console/CLI authoring surface for the
// sealed secret store an operator references from connector configs as
// `store:<name>`. V1 governs a single deployment-wide (global) scope, so the
// endpoints are SUPERADMIN-gated — a deployment-wide credential must not be
// editable by a single tenant's admin — and writes additionally require an AAL3
// step-up (secret-bearing, privilege-shaped). The secret VALUE is never returned;
// a read exposes only a non-secret fingerprint hint.

// errSecretStoreUnavailable is returned when no secret store is wired (an
// embedder/test that did not opt in). Mapped to 501 (honest seam), like SSO.
var errSecretStoreUnavailable = errors.New("api: secret store unavailable")

// secretDTO is the read shape: the name, a non-secret hint and metadata. NEVER the
// value.
type secretDTO struct {
	Name        string `json:"name"`
	Hint        string `json:"hint,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// secretsListDTO is the list response, plus whether the sealer is wired (so the
// console can show an honest "secret writes disabled" banner instead of failing
// each write).
type secretsListDTO struct {
	Secrets         []secretDTO `json:"secrets"`
	SealerAvailable bool        `json:"sealer_available"`
}

// secretInput is the PUT/DELETE payload. On PUT a blank value keeps the stored
// sealed value (so editing the description never forces re-entering the secret).
type secretInput struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

func toSecretDTO(v auth.SecretView) secretDTO {
	d := secretDTO{Name: v.Name, Hint: v.Hint, Description: v.Description}
	if !v.CreatedAt.IsZero() {
		d.CreatedAt = v.CreatedAt.String()
	}
	if !v.UpdatedAt.IsZero() {
		d.UpdatedAt = v.UpdatedAt.String()
	}
	return d
}

// secretSvc returns the secret store, or writes 501 when it is not wired.
func (s *Server) secretSvc(w http.ResponseWriter, r *http.Request) (*auth.SecretStore, bool) {
	if s.secretStore == nil {
		s.writeError(w, r, errSecretStoreUnavailable)
		return nil, false
	}
	return s.secretStore, true
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	views, err := svc.List(r.Context(), auth.GlobalSecretScope)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := secretsListDTO{Secrets: make([]secretDTO, 0, len(views)), SealerAvailable: svc.SealerWired()}
	for _, v := range views {
		out.Secrets = append(out.Secrets, toSecretDTO(v))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	view, err := svc.Put(r.Context(), p, auth.GlobalSecretScope, in.Name, in.Value, in.Description)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecretDTO(view))
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	var in secretInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if err := svc.Delete(r.Context(), p, auth.GlobalSecretScope, in.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

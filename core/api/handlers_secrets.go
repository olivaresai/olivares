// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"strings"

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

// The explicit tenant scope serves MCP credential handles (mcp/) and the secrets
// sessions receive as environment variables (env/, modules/sessions
// session_secret_env.go). It cannot inventory or replace provider, federation, or
// deployment-wide secrets.
func (s *Server) secretScope(w http.ResponseWriter, r *http.Request) (auth.Principal, model.TenantID, bool) {
	values, present := r.URL.Query()["scope"]
	if !present {
		p, ok := s.authzSystem(w, r, "system:admin")
		return p, auth.GlobalSecretScope, ok
	}
	if len(values) != 1 || values[0] != "tenant" {
		s.badRequest(w, r, "scope must be tenant or omitted for the global store")
		return auth.Principal{}, model.TenantID(""), false
	}
	p, tenant, ok := s.authzTenant(w, r, "tenant:admin")
	if ok {
		if _, confined := p.ConfinedWorkspaceIn(tenant); confined {
			s.writeError(w, r, errForbidden)
			ok = false
		}
	}
	return p, tenant, ok
}

// tenantSecretName is the tenant scope's namespace: MCP credential handles and
// session environment secrets.
func tenantSecretName(name string) bool {
	return strings.HasPrefix(name, "mcp/") || strings.HasPrefix(name, "env/")
}

func (s *Server) secretNameInScope(w http.ResponseWriter, r *http.Request, scope model.TenantID, name string) bool {
	if scope != auth.GlobalSecretScope && !tenantSecretName(name) {
		s.badRequest(w, r, "tenant secret names must begin with mcp/ or env/")
		return false
	}
	return true
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := s.secretScope(w, r)
	if !ok {
		return
	}
	svc, ok := s.secretSvc(w, r)
	if !ok {
		return
	}
	views, err := svc.List(r.Context(), scope)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := secretsListDTO{Secrets: make([]secretDTO, 0, len(views)), SealerAvailable: svc.SealerWired()}
	for _, v := range views {
		if scope != auth.GlobalSecretScope && !tenantSecretName(v.Name) {
			continue
		}
		out.Secrets = append(out.Secrets, toSecretDTO(v))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutSecret(w http.ResponseWriter, r *http.Request) {
	p, scope, ok := s.secretScope(w, r)
	if !ok {
		return
	}
	if !s.requireStepUp(w, r, p) {
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
	if !s.secretNameInScope(w, r, scope, in.Name) {
		return
	}
	view, err := svc.Put(r.Context(), p, scope, in.Name, in.Value, in.Description)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecretDTO(view))
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	p, scope, ok := s.secretScope(w, r)
	if !ok {
		return
	}
	if !s.requireStepUp(w, r, p) {
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
	if !s.secretNameInScope(w, r, scope, in.Name) {
		return
	}
	if err := svc.Delete(r.Context(), p, scope, in.Name); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func (s *Server) mcpGatewayAdmission(w http.ResponseWriter, r *http.Request, write bool) (auth.Principal, model.TenantID, bool) {
	p, tenant, ok := s.authzTenant(w, r, "tenant:admin")
	if !ok {
		return p, tenant, false
	}
	// These settings govern a whole tenant. A workspace-confined credential
	// cannot configure or inventory the tenant's gateway, even with a read role.
	if _, confined := p.ConfinedWorkspaceIn(tenant); confined {
		s.writeError(w, r, errForbidden)
		return p, tenant, false
	}
	if write && !s.requireAAL3(w, r, p) {
		return p, tenant, false
	}
	if s.mcpGateway == nil {
		s.writeError(w, r, auth.ErrMCPGatewayUnavailable)
		return p, tenant, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return p, tenant, true
}

// handleMCPGateway Read the tenant’s effective gateway source, reference-only upstream inventory and applied governance. Operator-file configuration is read-only and store records remain inactive while it governs.
func (s *Server) handleMCPGateway(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.mcpGatewayAdmission(w, r, false)
	if !ok {
		return
	}
	out, err := s.mcpGateway.Get(r.Context(), tenant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// Configuration writes use the auth ledger. Seal this inventory read in
	// that same ledger before releasing even a reference-only snapshot.
	err = s.st.AuthMutate(r.Context(), func(as store.AuthScope) error {
		event, err := as.Audit().Append(r.Context(), model.AuditDraft{
			Actor: p.Actor(), ActorKind: p.ActorKind(), Action: "mcp_gateway.read",
			TargetKind: "mcp.gateway", TargetID: model.ID(tenant.String()),
			Meta: map[string]any{"tenant_id": tenant.String(), "version": out.Version, "source": out.Source},
		})
		if err == nil && event.Seq == 0 {
			return auth.ErrMCPGatewayUnavailable
		}
		return err
	})
	if err != nil {
		s.writeError(w, r, fmt.Errorf("%w: %w", auth.ErrMCPGatewayUnavailable, err))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePutMCPGatewayServer Add or update a tenant upstream with sealed credential references and public inbound trust. New servers and edited configurations are enabled only by an explicit administrator write after a successful current connection test; discovery grants no tool authority.
func (s *Server) handlePutMCPGatewayServer(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.mcpGatewayAdmission(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version *int64                     `json:"version"`
		Server  auth.MCPGatewayServerInput `json:"server"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Version == nil || *in.Version < 0 {
		s.badRequest(w, r, "provide version and a reference-only server configuration")
		return
	}
	out, err := s.mcpGateway.PutServer(r.Context(), p, tenant, *in.Version, chi.URLParam(r, "id"), in.Server)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handleDeleteMCPGatewayServer Remove a tenant upstream after checking the current configuration version. New requests are refused; its independently managed sealed credential is preserved.
func (s *Server) handleDeleteMCPGatewayServer(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.mcpGatewayAdmission(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version *int64 `json:"version"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Version == nil || *in.Version < 0 {
		s.badRequest(w, r, "provide the current version")
		return
	}
	out, err := s.mcpGateway.DeleteServer(r.Context(), p, tenant, *in.Version, chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTestMCPGatewayServer Initialize an upstream and list its tools within bounded connection and catalog limits. No tools are called; the verdict is audited, and failed tests disable forwarding.
func (s *Server) handleTestMCPGatewayServer(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.mcpGatewayAdmission(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version *int64 `json:"version"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Version == nil || *in.Version < 0 {
		s.badRequest(w, r, "provide the current version")
		return
	}
	out, err := s.mcpGateway.TestServer(r.Context(), p, tenant, *in.Version, chi.URLParam(r, "id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMCPGatewaySessionTools Set the tenant’s default-off session MCP switch after checking its current configuration version. Enabling preserves independently authenticated session purposes, workspace grants and process fencing; it confers no additional authority.
func (s *Server) handleMCPGatewaySessionTools(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.mcpGatewayAdmission(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version *int64 `json:"version"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Enabled == nil || in.Version == nil || *in.Version < 0 {
		s.badRequest(w, r, "provide version and enabled")
		return
	}
	out, err := s.mcpGateway.SetSessionTools(r.Context(), p, tenant, *in.Version, *in.Enabled)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

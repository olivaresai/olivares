// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package api

import "net/http"

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, ErrOrganizationsUnavailable)
}

func (s *Server) handleSetOrgRegion(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, ErrOrganizationsUnavailable)
}

func (s *Server) handleDropOrg(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, ErrOrganizationsUnavailable)
}

func (s *Server) handleSetOrgStatus(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, ErrOrganizationsUnavailable)
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, ErrOrganizationsUnavailable)
}

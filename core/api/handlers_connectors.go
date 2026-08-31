// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// Console connector-onboarding endpoints: the descriptor catalog + the
// sealed-credential CRUD + the connectivity test the console drives to add a
// connector and its credentials in runtime. SUPERADMIN-gated (a deployment-wide
// ingestion change is not a single tenant's to make); writes and the test require
// an AAL3 step-up (secret-bearing). The catalog is non-secret metadata, so it is a
// plain superadmin read (no AAL3), like listing sources. A credential VALUE is never
// returned — the form reads a connector's existing config (references only) and a
// blank secret field keeps the stored sealed value (handlers_connectors.go composes
// the secret store + the source roster; connectors.go documents the seam).

// connectorOnboardingSvc returns the onboarding surface, or writes 501 when it is
// not wired.
func (s *Server) connectorOnboardingSvc(w http.ResponseWriter, r *http.Request) (ConnectorOnboarding, bool) {
	if s.connectorOnboarding == nil {
		s.writeError(w, r, errConnectorOnboardingUnavailable)
		return nil, false
	}
	return s.connectorOnboarding, true
}

func (s *Server) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.connectorOnboardingSvc(w, r)
	if !ok {
		return
	}
	infos, err := svc.ListConnectors(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if infos == nil {
		infos = []ConnectorInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": infos})
}

func (s *Server) handleTestConnector(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.connectorOnboardingSvc(w, r)
	if !ok {
		return
	}
	var in ConnectorOnboardInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if err := svc.TestConnector(r.Context(), p, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePutConnector(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.connectorOnboardingSvc(w, r)
	if !ok {
		return
	}
	var in ConnectorOnboardInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	res, err := svc.PutConnector(r.Context(), p, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteConnector(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.connectorOnboardingSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	res, err := svc.DeleteConnector(r.Context(), p, in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

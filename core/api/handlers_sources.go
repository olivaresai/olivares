// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
)

// Live source-reconfiguration endpoints: the console/CLI authoring surface
// for the durable connector/source roster, plus the reload trigger that
// reconciles it into the running engine WITHOUT a restart. Like the secret
// endpoints these are SUPERADMIN-gated (a deployment-wide ingestion change must
// not be editable by one tenant's admin) and writes/reloads additionally require
// an AAL3 step-up (privilege-shaped). A source row carries only secret
// REFERENCES, never values, so reads are safe to surface.

// errSourceRosterUnavailable is returned when no source roster is wired (an
// embedder/test that did not opt in). Mapped to 501 (honest seam), like SSO.
var errSourceRosterUnavailable = errors.New("api: source roster unavailable")

// sourceRosterSvc returns the roster service, or writes 501 when it is not wired.
func (s *Server) sourceRosterSvc(w http.ResponseWriter, r *http.Request) (SourceRoster, bool) {
	if s.sourceRoster == nil {
		s.writeError(w, r, errSourceRosterUnavailable)
		return nil, false
	}
	return s.sourceRoster, true
}

func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.sourceRosterSvc(w, r)
	if !ok {
		return
	}
	entries, err := svc.ListSources(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if entries == nil {
		entries = []SourceRosterEntry{}
	}
	for i := range entries {
		normalizeRosterEntry(&entries[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": entries})
}

func (s *Server) handlePutSource(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.sourceRosterSvc(w, r)
	if !ok {
		return
	}
	var in SourceRosterInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	res, err := svc.PutSource(r.Context(), p, in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.sourceRosterSvc(w, r)
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
	res, err := svc.DeleteSource(r.Context(), p, in.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleReloadRuntime(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.sourceRosterSvc(w, r)
	if !ok {
		return
	}
	report, err := svc.ReloadSources(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// the runtime reload also re-resolves and hot-applies the license (a file-
	// based `license install` + reload, the GitLab "drop the file" path). Best-effort
	// and never fails the source reload — it logs a transition internally and the
	// console reads the live status separately.
	if s.license != nil {
		s.license.Reconcile(r.Context())
	}
	writeJSON(w, http.StatusOK, report)
}

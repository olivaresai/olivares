// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"
)

// Live edition/license endpoints: the console/CLI surface for installing,
// observing and HOT-APPLYING a commercial license without a restart (the Grafana/
// Elastic in-place model). Like the secret + source endpoints these are
// SUPERADMIN-gated (a deployment-wide edition change must not be editable by one
// tenant's admin) and the WRITES additionally require an AAL3 step-up (privilege-
// shaped). The read carries no secret (a license blob is not a credential — it is a
// signed, public attestation) so the GET needs no step-up.
//
// OPEN-CORE (LICENSING.md): these endpoints persist/observe/hot-apply the license
// artifact; they NEVER gate a feature on it. In the community build the install
// simply stores and displays the attestation (ready for an in-place binary swap);
// the consumer of an attested claim is the closed enterprise build's add-on
// entitlement. User accounts are never part of it — self-hosted users are
// unlimited in every tier (B10, core/auth/seatcap.go).

func (s *Server) licenseSvc(w http.ResponseWriter, r *http.Request) (LicenseService, bool) {
	if s.license == nil {
		s.writeError(w, r, ErrLicenseUnavailable)
		return nil, false
	}
	return s.license, true
}

func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.licenseSvc(w, r)
	if !ok {
		return
	}
	st, err := svc.LicenseStatus(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleInstallLicense(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.licenseSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		License     string `json:"license"`
		Acknowledge bool   `json:"acknowledge"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.License) == "" {
		s.badRequest(w, r, "license blob is required")
		return
	}
	st, err := svc.InstallLicense(r.Context(), in.License, in.Acknowledge)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUninstallLicense(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.licenseSvc(w, r)
	if !ok {
		return
	}
	// acknowledge rides as a query flag so a body-less DELETE (curl/CLI) still works;
	// the console sets it explicitly when removing a license drops below the active estate.
	ack := r.URL.Query().Get("acknowledge") == "true"
	st, err := svc.UninstallLicense(r.Context(), ack)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

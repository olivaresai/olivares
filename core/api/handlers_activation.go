// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// Enterprise activation endpoints: the console surface for enabling the
// licensed edition's add-ons. Like the license + secret endpoints these
// are SUPERADMIN-gated (a deployment-wide activation change is not a per-tenant
// admin's to make) and the WRITE (apply) requires an AAL3 step-up. The reads
// (status/preview) carry no secret, so they need no step-up.
//
// ENTERPRISE-ONLY: the community build wires no service, so every route answers
// 501 (ErrActivationUnavailable) — the console shows the honest "available in the
// enterprise edition" note rather than a broken control.

func (s *Server) activationSvc(w http.ResponseWriter, r *http.Request) (ActivationService, bool) {
	if s.activation == nil {
		s.writeError(w, r, ErrActivationUnavailable)
		return nil, false
	}
	return s.activation, true
}

func (s *Server) handleActivationStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.activationSvc(w, r)
	if !ok {
		return
	}
	st, err := svc.ActivationStatus(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleActivationPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	svc, ok := s.activationSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Preset string `json:"preset"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.Preset) == "" {
		s.badRequest(w, r, "preset is required")
		return
	}
	plan, err := svc.ActivationPreview(r.Context(), in.Preset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleActivationApply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	svc, ok := s.activationSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"`
		Preset string `json:"preset"`
		Addon  string `json:"addon"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	switch in.Action {
	case "enable", "disable":
		if strings.TrimSpace(in.Preset) == "" {
			s.badRequest(w, r, "preset is required for "+in.Action)
			return
		}
	case "promote":
		if strings.TrimSpace(in.Addon) == "" {
			s.badRequest(w, r, "addon is required for promote")
			return
		}
	default:
		s.badRequest(w, r, "action must be enable, disable or promote")
		return
	}
	st, err := svc.ActivationApply(r.Context(), ActivationApplyRequest{
		Action: in.Action, Preset: in.Preset, Addon: in.Addon, Actor: activationActor(p),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// activationActor resolves a display actor from the authenticated principal for
// the manifest provenance + audit event.
func activationActor(p auth.Principal) string {
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	return "console-superadmin"
}

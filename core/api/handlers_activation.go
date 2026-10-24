// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
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

func (s *Server) handleActivationStatus(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
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

func (s *Server) handleActivationPreview(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	svc, ok := s.activationSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Preset string `json:"preset"`
		Family string `json:"family"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	selection, err := activationSelection(in.Preset, in.Family)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	plan, err := svc.ActivationPreview(r.Context(), selection)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleActivationApply(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if !s.requireStepUp(w, r, p) {
		return
	}
	svc, ok := s.activationSvc(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"`
		Preset string `json:"preset"`
		Family string `json:"family"`
		Addon  string `json:"addon"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	selection := in.Preset
	switch in.Action {
	case "enable", "disable":
		var err error
		if selection, err = activationSelection(in.Preset, in.Family); err != nil {
			s.badRequest(w, r, err.Error()+" for "+in.Action)
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
		Action: in.Action, Preset: selection, Addon: in.Addon, Actor: activationActor(p),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// activationSelection is what the service is asked to enable, disable or preview:
// a preset name, or one family as ActivationFamilyPrefix + its key. A request names
// exactly one of them.
func activationSelection(preset, family string) (string, error) {
	preset, family = strings.TrimSpace(preset), strings.TrimSpace(family)
	switch {
	case preset != "" && family != "":
		return "", errors.New("name a preset or a family, not both")
	case family != "":
		// One family: a name, never a list (the selection language splits on commas,
		// plus signs and spaces).
		if strings.IndexFunc(family, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
		}) >= 0 {
			return "", errors.New("a family is named by lowercase letters, digits and hyphens")
		}
		return ActivationFamilyPrefix + family, nil
	case preset != "":
		return preset, nil
	default:
		return "", errors.New("a preset or a family is required")
	}
}

// activationActor resolves a display actor from the authenticated principal for
// the manifest provenance + audit event.
func activationActor(p auth.Principal) string {
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	return "console-superadmin"
}

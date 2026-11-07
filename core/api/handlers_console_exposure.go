// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"github.com/olivaresai/olivares/core/webaddr"
	"net/http"
)

// ConsoleExposure reports local node facts. Addresses are interface observations,
// never evidence that a browser outside the LAN can reach the console.
type ConsoleExposure struct {
	Listen               string   `json:"listen"`
	GRPCListen           string   `json:"grpc_listen"`
	SavedPublicURL       string   `json:"saved_public_url"`
	PublicURL            string   `json:"public_url"`
	TLS                  bool     `json:"tls"`
	AdminStepUp          string   `json:"admin_step_up"`
	ExternalReachability string   `json:"external_reachability"`
	Addresses            []string `json:"addresses"`
	ManagedPublic        bool     `json:"managed_public"`
}

func (s *Server) handleConsoleExposureGet(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.consoleExposure == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "exposure_unavailable", "Console listener facts are unavailable.")
		return
	}
	facts, err := s.consoleExposure()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "exposure_unavailable", "Console listener facts are unavailable.")
		return
	}
	policy, err := s.authr.AdminStepUp(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	managed, err := s.authr.ConsoleExposure(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	facts.AdminStepUp = policy
	facts.ExternalReachability = "not verified"
	facts.ManagedPublic = managed.ManagedPublic()
	facts.SavedPublicURL = managed.PublicURL
	if facts.Addresses == nil {
		facts.Addresses = []string{}
	}
	writeJSON(w, http.StatusOK, facts)
}

func (s *Server) handleConsoleExposurePut(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	var in struct {
		PublicURL *string `json:"public_url"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.PublicURL == nil {
		s.badRequest(w, r, "Expected a public_url HTTPS origin, or an empty string to remove the managed address.")
		return
	}
	if *in.PublicURL != "" {
		addr, err := webaddr.Parse("public_url", *in.PublicURL)
		if err != nil || addr.Scheme != "https" {
			s.badRequest(w, r, "Public console exposure requires a valid HTTPS origin without a path or credentials.")
			return
		}
	}
	if err := s.authr.SetConsolePublicURL(r.Context(), mc.Principal, *in.PublicURL); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_url": *in.PublicURL, "restart_required": true})
}

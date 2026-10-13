//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

func (s *Server) caepReceiveEvents(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	identityBusinessUnavailable(w, "caep_requires_business", "The CAEP receiver requires Business.")
}

func identityBusinessUnavailable(w http.ResponseWriter, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	writeJSON(w, http.StatusNotImplemented, body)
}

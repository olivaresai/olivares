//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

func (*Server) pivVerifierRootsConfigured() bool { return false }

func (s *Server) handlePIVStatus(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, auth.ErrPIVNotConfigured)
}

func (s *Server) handlePIVElevate(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, auth.ErrPIVNotConfigured)
}

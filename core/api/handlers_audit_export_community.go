// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/audit"
)

func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	s.writeError(w, r, audit.ErrBusinessAudit)
}

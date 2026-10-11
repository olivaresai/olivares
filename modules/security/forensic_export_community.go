// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package security

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
)

func (*Module) handleExportCase(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	w.Header().Set("Cache-Control", "no-store")
	writeStoreError(w, audit.ErrBusinessAudit)
}

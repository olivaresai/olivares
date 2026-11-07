//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "github.com/olivaresai/olivares/core/api"

// Community keeps the personal vault and answers the shared-vault routes with 501
// business_required. The grants a Business instance stored keep applying to the vault's
// items (core/api vault_shared.go), so a lapsed licence never locks a team out.
func (m *Module) registerVaultRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/vaults", permVaultRead, businessRequired)
	reg.Handle("POST", "/vaults", permVaultWrite, businessRequired)
	reg.Handle("GET", "/vaults/{id}/shares", permVaultRead, businessRequired)
	reg.Handle("PUT", "/vaults/{id}/shares", permVaultWrite, businessRequired)
	reg.Handle("DELETE", "/vaults/{id}/shares", permVaultWrite, businessRequired)
}

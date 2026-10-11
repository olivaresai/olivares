// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// tenantconfig.go names the ONE business-tenant policy for package main. The rule and
// its rationale live in pepkit.ParseBusinessTenant, which the PEP packages read too.
// Every reader asks it for the POLICY and keeps its own REACTION — refuse to mount,
// skip the entry, or decline to anchor evidence.

import "github.com/olivaresai/olivares/cmd/olivares/internal/pepkit"

// parseBusinessTenant resolves an operator-configured or decision-carried tenant
// reference into a BUSINESS tenant, deny-closed (see pepkit.ParseBusinessTenant).
var parseBusinessTenant = pepkit.ParseBusinessTenant

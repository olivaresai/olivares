// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package provider

import "github.com/hashicorp/terraform-plugin-framework/types"

// dsTenant returns a data source's per-call tenant override: the configured value
// when set, otherwise empty so the client falls back to its provider-level tenant.
func dsTenant(v types.String) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return ""
}

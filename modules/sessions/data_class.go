// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "slices"

var dataClassValues = []string{"", "public", "internal", "confidential", "regulated", "scratch", "tre"}

// Classification metadata has a closed vocabulary. Editions decide its policy.
func validateDataClass(class string) error {
	if slices.Contains(dataClassValues, class) {
		return nil
	}
	return badRequest("data class must be public, internal, confidential, regulated, scratch or tre")
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// NarrowScopesForTest exports narrowScopes for testing.
func NarrowScopesForTest(granted, requested []string) []string {
	return narrowScopes(granted, requested)
}

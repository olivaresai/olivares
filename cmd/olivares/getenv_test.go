// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// getenvFrom returns a getenv backed by a map (no process env mutation).
func getenvFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

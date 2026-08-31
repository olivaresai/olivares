// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Tiny assertion + JSON-shape helpers shared by the E2E suite.

import "testing"

// items2 extracts a named array field of a decoded JSON object as
// []map[string]any (object elements only).
func items2(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if obj, ok := it.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

// assertEq fails the test if got != want (compared as decoded JSON scalars:
// strings, float64, bool).
func assertEq(t *testing.T, what string, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

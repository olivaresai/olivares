// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
)

func TestSessionWIFEnabled(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{{"1", true}, {"true", true}, {"TRUE", true}, {" 1 ", true}, {"", false}, {"0", false}, {"no", false}} {
		if got := sessionWIFEnabled(tc.in); got != tc.want {
			t.Errorf("sessionWIFEnabled(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

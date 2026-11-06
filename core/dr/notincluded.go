// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import "strings"

// NotIncludedLine is the one-line operator statement of Manifest.NotIncluded, or ""
// for a bundle that records none. A bundle taken before the list existed records
// none too: its absence is "not recorded", not "nothing left out".
func (m *Manifest) NotIncludedLine() string {
	if m == nil || len(m.NotIncluded) == 0 {
		return ""
	}
	parts := make([]string, len(m.NotIncluded))
	for i, n := range m.NotIncluded {
		parts[i] = n.Path + " (" + n.Reason + ")"
	}
	return "not included in this bundle: " + strings.Join(parts, "; ")
}

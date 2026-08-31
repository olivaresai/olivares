// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "testing"

// TestSupportConfigKeyStripsExportOnlyWhenItIsTheKeyword pins supportConfigKey,
// which had no coverage at all. It decides on the FIRST rune after "export": the
// keyword form loses its prefix and any leading space, a longer word starting with
// "export" keeps its whole name. That distinction is what keeps a key such as
// EXPORTER_URL out of the public-key allowlist lookup under the wrong name.
func TestSupportConfigKeyStripsExportOnlyWhenItIsTheKeyword(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"LOG_LEVEL", "LOG_LEVEL"},
		{"  LOG_LEVEL  ", "LOG_LEVEL"},
		{"export LOG_LEVEL", "LOG_LEVEL"},
		{"export\tLOG_LEVEL", "LOG_LEVEL"},
		{"export   LOG_LEVEL", "LOG_LEVEL"},
		{"  export LOG_LEVEL", "LOG_LEVEL"},
		// "export" as a prefix of a longer name is NOT the keyword.
		{"exportLOG_LEVEL", "exportLOG_LEVEL"},
		{"export_LOG_LEVEL", "export_LOG_LEVEL"},
		{"EXPORTER_URL", "EXPORTER_URL"},
		// The bare keyword has nothing after it.
		{"export", "export"},
		// A multi-byte first rune must be read as one rune, not one byte: this is the
		// case where reading a byte instead of a rune would answer differently.
		{"export LOG_LEVEL", "LOG_LEVEL"}, // U+00A0 NBSP is a space to unicode.IsSpace
		{"exportñLOG", "exportñLOG"},      // U+00F1 is not a space
	} {
		if got := supportConfigKey(tc.in); got != tc.want {
			t.Errorf("supportConfigKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

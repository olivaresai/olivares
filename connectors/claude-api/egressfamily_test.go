// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import "testing"

// TestServerToolFamily proves the version-robust family resolver the egress gate relies on:
// a recognized dated id resolves exact; a NEWER (not-yet-listed) version of a KNOWN family
// still resolves to its family (recognizedExact=false) so a version bump cannot bypass a
// family-scoped grant; a bare family name resolves; an unknown id resolves to nothing.
func TestServerToolFamily(t *testing.T) {
	cases := []struct {
		id     string
		family string
		exact  bool
	}{
		// recognized dated ids (exact)
		{webSearchToolType, "web_search", true},
		{"web_search_20250305", "web_search", true},
		{webFetchToolType, "web_fetch", true},
		{codeExecToolType, "code_execution", true},
		{advisorToolType, "advisor", true},
		// newer, NOT-yet-listed versions of known families → family resolves, not exact
		{"web_search_20260318", "web_search", false},
		{"web_fetch_20260318", "web_fetch", false},
		{"code_execution_20260521", "code_execution", false},
		// bare family roots resolve (no date)
		{"web_search", "web_search", false},
		{"code_execution", "code_execution", false},
		// whitespace is trimmed
		{"  web_search_20260318  ", "web_search", false},
		// unknown / non-server-tool ids resolve to nothing
		{"bash_20250124", "", false},
		{"computer_20250124", "", false},
		{"custom_tool", "", false},
		{"web_search_2026031", "", false},  // 7-digit suffix is not a dated id and "web_search_2026031" is not a known root
		{"web_search_abcdefgh", "", false}, // non-digit suffix
		{"", "", false},
	}
	for _, tc := range cases {
		fam, exact := ServerToolFamily(tc.id)
		if fam != tc.family || exact != tc.exact {
			t.Errorf("ServerToolFamily(%q) = (%q,%v); want (%q,%v)", tc.id, fam, exact, tc.family, tc.exact)
		}
	}
}

// TestLooksLikeServerToolType proves the deny-closed backstop: the dated "<name>_<YYYYMMDD>"
// shape is detected even for UNKNOWN families, while a non-dated (custom/client) type is not.
func TestLooksLikeServerToolType(t *testing.T) {
	dated := []string{"web_search_20260318", "url_fetch_20260601", "brand_new_20270101", "code_execution_20260521"}
	for _, id := range dated {
		if !LooksLikeServerToolType(id) {
			t.Errorf("LooksLikeServerToolType(%q) = false; want true (dated shape)", id)
		}
	}
	notDated := []string{"web_search", "my_custom_tool", "lookup_user", "", "tool_2026031", "tool_abcdefgh", "_20260101"}
	for _, id := range notDated {
		if LooksLikeServerToolType(id) {
			t.Errorf("LooksLikeServerToolType(%q) = true; want false (not a dated server-tool shape)", id)
		}
	}
}

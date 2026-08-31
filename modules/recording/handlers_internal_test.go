// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import "testing"

func TestLiteralContainsPattern(t *testing.T) {
	const contract = `RECORDINGS_LIKE_LITERAL_CONTRACT: subject_contains must escape %, _ and \\ before adding contains wildcards`

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "ordinary", value: "user:abc", want: "%user:abc%"},
		{name: "percent", value: "100%", want: `%100\%%`},
		{name: "underscore", value: "service_id", want: `%service\_id%`},
		{name: "backslash", value: `domain\user`, want: `%domain\\user%`},
		{name: "ordered mixed escaping", value: `\%_`, want: `%\\\%\_%`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := literalContainsPattern(tt.value); got != tt.want {
				t.Fatalf("%s: literalContainsPattern(%q) = %q, want %q", contract, tt.value, got, tt.want)
			}
		})
	}
}

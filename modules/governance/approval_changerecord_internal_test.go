// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// boundedText is what an external system's label becomes in a sentence and in the ledger:
// printable characters, trimmed, at most maxChangeRecordText bytes, whole runes.
func TestBoundedTextKeepsTheBoundedPrintablePart(t *testing.T) {
	long := strings.Repeat("\u00e9", 40) // 80 bytes
	for name, tc := range map[string]struct{ in, want string }{
		"plain":                {"Scheduled", "Scheduled"},
		"trims the ends":       {"  Scheduled \t", "Scheduled"},
		"control characters":   {"Sched\x00ul\x1bed\n", "Scheduled"},
		"bidi override":        {"Sched‮uled", "Scheduled"},
		"zero width":           {"Sched​uled", "Scheduled"},
		"no-break space":       {"Pending Approval", "PendingApproval"},
		"invalid utf-8":        {"Sched\xffuled", "Scheduled"},
		"empty after the trim": {" ​\n", ""},
		"cut on a rune":        {long, strings.Repeat("\u00e9", 32)},
	} {
		got := boundedText(tc.in)
		if got != tc.want {
			t.Errorf("%s: boundedText(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) || len(got) > maxChangeRecordText {
			t.Errorf("%s: boundedText(%q) = %q is not valid text within %d bytes", name, tc.in, got, maxChangeRecordText)
		}
	}
}

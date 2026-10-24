// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// Only OpenCode's own "stream error" line for this conversation's main call is a
// failed turn: a small background call (the title), another conversation, another
// level or a line of another shape is not.
func TestOpenCodeFailedCallReadsOnlyTheConversationsStreamError(t *testing.T) {
	line := func(session, small, level, message string) string {
		return `timestamp=2026-10-22T12:55:57.836Z level=` + level + ` run=8d38203a message="` + message +
			`" providerID=opencode modelID=glm-5.3-flash-free session.id=` + session + ` small=` + small +
			` agent=build mode=primary error.error="AI_APICallError: Free usage limit reached. Subscribe: https://opencode.ai/go"`
	}
	for _, tc := range []struct {
		name, line, want string
		ok               bool
	}{
		{"the conversation's call", line("ses-a", "false", "ERROR", "stream error"), "Free usage limit reached. Subscribe: https://opencode.ai/go", true},
		{"a small background call", line("ses-a", "true", "ERROR", "stream error"), "", false},
		{"another conversation", line("ses-b", "false", "ERROR", "stream error"), "", false},
		{"another message", line("ses-a", "false", "ERROR", "process error"), "", false},
		{"another level", line("ses-a", "false", "WARN", "stream error"), "", false},
		{"not a log line", "Error: something else", "", false},
		{"a repeated key: the first wins", line("ses-a", "false", "ERROR", "stream error") + " small=true", "Free usage limit reached. Subscribe: https://opencode.ai/go", true},
	} {
		got, ok := openCodeFailedCall(openCodeLogFields(tc.line), "ses-a")
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: %q %v, want %q %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

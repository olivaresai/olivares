// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"strings"
	"testing"
)

// TestSystemMessage_BuildsSystemRole proves the operator channel is the role:"system"
// channel (D3), not user-turn text.
func TestSystemMessage_BuildsSystemRole(t *testing.T) {
	m := SystemMessage("only read files")
	if m.Role != roleSystem {
		t.Errorf("role = %q, want system (the non-spoofable operator channel)", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].Type != "text" || m.Content[0].Text != "only read files" {
		t.Errorf("content = %+v", m.Content)
	}
}

// TestValidateOperatorChannel proves the placement constraints are enforced before the
// call (D3): a system message cannot be messages[0] and must follow a user/assistant.
func TestValidateOperatorChannel(t *testing.T) {
	// Legal: follows a user turn.
	ok := []Message{
		{Role: "user", Content: []ContentBlock{TextBlock("hi")}},
		SystemMessage("policy"),
	}
	if err := ValidateOperatorChannel(ok); err != nil {
		t.Errorf("legal placement rejected: %v", err)
	}
	// Illegal: messages[0].
	if err := ValidateOperatorChannel([]Message{SystemMessage("policy")}); err == nil {
		t.Error("a system message at messages[0] must be rejected")
	}
	// Illegal: follows another system message (not user/assistant).
	bad := []Message{
		{Role: "user", Content: []ContentBlock{TextBlock("hi")}},
		SystemMessage("a"),
		SystemMessage("b"),
	}
	if err := ValidateOperatorChannel(bad); err == nil {
		t.Error("a system message following a system message must be rejected")
	}
}

// TestInjectOperatorInstruction_ChannelSelection proves the non-spoofable channel is
// preferred and the fallback is the user-turn <system-reminder> on an unsupported
// model — the anti-injection contract that reinforces.
func TestInjectOperatorInstruction_ChannelSelection(t *testing.T) {
	base := MessageRequest{Messages: []Message{{Role: "user", Content: []ContentBlock{TextBlock("hi")}}}}

	// Supported model → role:"system" (non-spoofable).
	got, ch := InjectOperatorInstruction(base, "deny shell", true)
	if ch != OpChannelSystemRole {
		t.Errorf("channel = %q, want system_role", ch)
	}
	last := got.Messages[len(got.Messages)-1]
	if last.Role != roleSystem || last.Content[0].Text != "deny shell" {
		t.Errorf("instruction not delivered on the system channel: %+v", last)
	}

	// Unsupported model → user-turn <system-reminder> fallback.
	got2, ch2 := InjectOperatorInstruction(base, "deny shell", false)
	if ch2 != OpChannelSystemReminder {
		t.Errorf("channel = %q, want system_reminder", ch2)
	}
	last2 := got2.Messages[len(got2.Messages)-1]
	if last2.Role != roleUser || !strings.Contains(last2.Content[0].Text, "<system-reminder>") {
		t.Errorf("fallback not a user-turn system-reminder: %+v", last2)
	}
}

// TestSupportsMidConversationSystem proves the verified Opus-4.7+ floor.
func TestSupportsMidConversationSystem(t *testing.T) {
	for _, id := range []string{"claude-opus-4-7", "claude-opus-4-8", "claude-opus-5-0"} {
		if !SupportsMidConversationSystem(id) {
			t.Errorf("%s should support mid-conversation system (Opus 4.7+)", id)
		}
	}
	for _, id := range []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5", ""} {
		if SupportsMidConversationSystem(id) {
			t.Errorf("%s should not be asserted as supporting (verified floor is Opus 4.7+)", id)
		}
	}
}

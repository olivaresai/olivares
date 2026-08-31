// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the mid-conversation operator channel (D3): a {role:"system"}
// message inside messages[], gated by BetaMidConversationSystem. It is a GOVERNANCE
// lever, not a convenience — it is the non-spoofable channel for injecting trusted
// instructions partway through a session, and it REINFORCES (governed-hooks
// PEP) rather than weakening it.
//
// Why it is the safe channel (the anti-prompt-injection contract):
//   - Text placed in a user turn or a tool_result can be FORGED by anything that
//     writes user-visible input — the classic <system-reminder> trick is spoofable
//     by a prompt-injection payload that emits the same bytes.
//   - A {role:"system"} message is, per Anthropic's mid-conversation-system guidance,
//     treated as an OPERATOR-AUTHORITY instruction rather than user text — a channel
//     user/tool content cannot impersonate. It is the Messages-API
//     analog of PreToolUse `additionalContext`: where injects a governed
//     instruction into a Claude Code hook decision, this injects one into a Messages
//     API conversation the product itself drives (the judge, the routing executor, a
//     future agentic loop). The PEP's governed instruction should ride THIS channel,
//     not user-turn text — that is the reinforcement.
//
// Constraints (verified jun-2026, …/prompt-caching § mid-conversation system msgs):
// the system message must FOLLOW a user (or assistant) turn and cannot be messages[0]
// (the initial prompt is the top-level System field); content is text; and it is
// MODEL-GATED — an unsupported model 400s with "role 'system' is not supported on
// this model", so the channel degrades to a user-turn <system-reminder> fallback
// (same caching profile, weaker authority). Phrase the instruction as CONTEXT, not as
// an override command ("ignore the user", "regardless of …"): per Anthropic's guidance
// the model protects users from instructions that work against them, and that
// protection applies to the system role too, so override-style phrasing is both less
// effective and the wrong posture for a governance channel.
package claudeapi

import (
	"errors"
	"fmt"
)

// SystemMessage builds a mid-conversation operator-channel message (D3): a
// {role:"system"} turn carrying a trusted instruction. Append it AFTER a user/assistant
// turn (never as messages[0]); CreateMessage adds BetaMidConversationSystem when it
// sees a system-role message. Use the top-level MessageRequest.System for the INITIAL
// prompt — this is only for instructions that arrive mid-session.
func SystemMessage(text string) Message {
	return Message{Role: roleSystem, Content: []ContentBlock{TextBlock(text)}}
}

// ValidateOperatorChannel checks every {role:"system"} turn in messages[] obeys the
// API constraints (D3) BEFORE the call, so a misuse fails honestly client-side rather
// than as an opaque 400: a system message cannot be messages[0] and must follow a
// user or assistant turn. It is a no-op when no system-role message is present.
func ValidateOperatorChannel(messages []Message) error {
	for i, m := range messages {
		if m.Role != roleSystem {
			continue
		}
		if i == 0 {
			return errors.New("claudeapi: a mid-conversation system message cannot be messages[0]; put the initial prompt in the top-level System field")
		}
		if prev := messages[i-1].Role; prev != roleUser && prev != roleAssistant {
			return fmt.Errorf("claudeapi: a mid-conversation system message must follow a user or assistant turn, not %q", prev)
		}
	}
	return nil
}

// OpChannel names which channel an operator instruction was delivered on.
type OpChannel string

const (
	// OpChannelSystemRole is the non-spoofable role:"system" channel (D3) — the
	// preferred channel, on a supporting model.
	OpChannelSystemRole OpChannel = "system_role"
	// OpChannelSystemReminder is the user-turn <system-reminder> fallback used when the
	// model does not support the system role. It has the same prompt-cache profile but
	// WEAKER authority (it is, in principle, spoofable by user input) — so it is the
	// fallback, never the default, and the caller knows which it got.
	OpChannelSystemReminder OpChannel = "system_reminder"
)

// InjectOperatorInstruction appends a governed operator instruction (e.g. the policy a
// Decider wants injected mid-session) on the strongest available channel and
// reports which it used. On a model that supports the system role it uses the
// non-spoofable role:"system" channel (D3); otherwise it falls back to a user-turn
// <system-reminder> block. The returned request is ready for CreateMessage, which adds
// the beta header when the system-role channel was used.
func InjectOperatorInstruction(req MessageRequest, instruction string, modelSupportsSystemRole bool) (MessageRequest, OpChannel) {
	if modelSupportsSystemRole {
		req.Messages = append(req.Messages, SystemMessage(instruction))
		return req, OpChannelSystemRole
	}
	req.Messages = append(req.Messages, Message{Role: roleUser, Content: []ContentBlock{systemReminderBlock(instruction)}})
	return req, OpChannelSystemReminder
}

// systemReminderBlock wraps an instruction as the user-turn <system-reminder> fallback
// (the older, spoofable convention) — used only when the model lacks the system role.
func systemReminderBlock(text string) ContentBlock {
	return TextBlock("<system-reminder>\n" + text + "\n</system-reminder>")
}

// SupportsMidConversationSystem reports whether a model supports the mid-conversation
// system role (D3). Verified jun-2026: available from Opus 4.7 onward. Older Opus and
// non-Opus families return false here (fail-closed → the caller uses the fallback);
// Sonnet/Haiku support is not asserted (to-confirm) rather than guessed, so the
// channel never silently 400s on an unverified model.
func SupportsMidConversationSystem(modelID string) bool {
	return RejectsSamplingParams(modelID) // both gate on Opus 4.7+ (the verified floor)
}

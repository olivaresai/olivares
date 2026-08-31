// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"strings"
)

// hook.go is the INBOUND half of the Codex hook wire: the event names and the payload
// Codex hands a hook command on stdin.
//
// Every field here was read off the draft-07 JSON Schemas codex-cli 0.145.0 embeds in its
// own binary (titles "<event>.command.input"), and then confirmed by capturing a real
// payload from a real `codex exec` run. It is not modeled from the docs and not guessed.

// The ten hook events codex-cli 0.145.0 declares. Names are the wire values of
// hook_event_name, which are PascalCase even though the hooks.json keys that register them
// are the same PascalCase (the snake_case forms in the binary are the internal serde
// renames, not the wire).
const (
	EventSessionStart     = "SessionStart"
	EventSessionEnd       = "SessionEnd"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventPermissionReq    = "PermissionRequest"
	EventPreCompact       = "PreCompact"
	EventPostCompact      = "PostCompact"
	EventSubagentStart    = "SubagentStart"
	EventSubagentStop     = "SubagentStop"
	EventStop             = "Stop"
)

// knownEvents is the closed set. Anything outside it is deny-closed (see render.go); the
// set is deliberately a map so IsKnownEvent stays O(1) and the deny-closed default cannot
// be widened by accident with an extra || in a chain of comparisons.
var knownEvents = map[string]struct{}{
	EventSessionStart:     {},
	EventSessionEnd:       {},
	EventUserPromptSubmit: {},
	EventPreToolUse:       {},
	EventPostToolUse:      {},
	EventPermissionReq:    {},
	EventPreCompact:       {},
	EventPostCompact:      {},
	EventSubagentStart:    {},
	EventSubagentStop:     {},
	EventStop:             {},
}

// IsKnownEvent reports whether the event is one this connector has a verified wire
// contract for. An unknown event is not an error — it is a DENY, because a Codex release
// that adds an event must not silently acquire an ungoverned path.
func IsKnownEvent(event string) bool {
	_, ok := knownEvents[event]
	return ok
}

// KnownEvents returns the closed set, sorted-insensitive (callers that need order sort it).
// It exists so the composition root can assert coverage without importing the map.
func KnownEvents() []string {
	out := make([]string, 0, len(knownEvents))
	for e := range knownEvents {
		out = append(out, e)
	}
	return out
}

// HookPayload is the union of every field Codex sends across the ten events. Codex sends
// one object per event with only that event's fields; modeling the union (rather than a
// type per event) keeps the PEP's dispatch in one place, and every consumer here checks
// the fields it needs rather than assuming presence.
//
// SessionID is the ONLY field required by all ten schemas together with HookEventName and
// Cwd. It is a Codex-minted UUIDv7 and is NEVER used as our key — see identity.go.
type HookPayload struct {
	HookEventName  string          `json:"hook_event_name"`
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	Cwd            string          `json:"cwd,omitempty"`
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	ToolName       string          `json:"tool_name,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
	// Source is SessionStart's startup|resume|clear|compact. It matters to the session
	// plane: a "resume" is not a new session, and treating it as one would mint a second
	// identity for a session that already has one.
	Source string `json:"source,omitempty"`
	// Trigger is PreCompact's manual|auto.
	Trigger string `json:"trigger,omitempty"`
	// Reason is SessionEnd's; codex-cli 0.145.0 pins it to the single value "other".
	Reason string `json:"reason,omitempty"`
	// AgentID/AgentType are present on the subagent events.
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
}

// maxHookBody bounds an inbound payload. Only tool_input is unbounded in principle, and
// only a few of its fields are ever read.
const maxHookBody = 1 << 20 // 1 MiB

// ParseHookPayload decodes a hook payload. A payload that does not parse still yields the
// event name when it can be recovered, because the deny-closed answer must be rendered in
// the shape THAT event honors — a deny in the wrong shape is ignored in silence, which is
// the failure mode this whole file exists to prevent.
func ParseHookPayload(body []byte) (HookPayload, bool) {
	var p HookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return HookPayload{HookEventName: EventNameOf(body)}, false
	}
	p.HookEventName = strings.TrimSpace(p.HookEventName)
	p.SessionID = strings.TrimSpace(p.SessionID)
	p.ToolUseID = strings.TrimSpace(p.ToolUseID)
	if p.HookEventName == "" || p.SessionID == "" {
		return p, false
	}
	return p, true
}

// EventNameOf recovers hook_event_name from a payload that may not fully parse. It returns
// "" when it cannot, and "" routes to the strictest deny-closed rendering — NOT to a
// convenient default event. Guessing PreToolUse here would emit a permissionDecision that
// a Stop event ignores.
func EventNameOf(body []byte) string {
	var probe struct {
		HookEventName string `json:"hook_event_name"`
	}
	if json.Unmarshal(body, &probe) == nil {
		return strings.TrimSpace(probe.HookEventName)
	}
	return ""
}

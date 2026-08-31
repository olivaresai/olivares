// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"
)

// Hook event names Claude Code passes in the payload's hook_event_name field.
// Claude Code ships ~30 lifecycle hooks (2026); the connector recognizes the set
// below for liveness/governance and ENFORCES on the permission-gating events
// (PreToolUse, PermissionRequest). Every other hook still feeds the silence
// watchdog generically (an unrecognized hook is observed, never dropped).
// Source: https://code.claude.com/docs/en/hooks
const (
	hookPreToolUse        = "PreToolUse"
	hookPostToolUse       = "PostToolUse"
	hookPermissionRequest = "PermissionRequest"
	hookPermissionDenied  = "PermissionDenied"
	hookUserPromptSubmit  = "UserPromptSubmit"
	hookSessionStart      = "SessionStart"
	hookSessionEnd        = "SessionEnd"
	hookStop              = "Stop"
	hookSubagentStop      = "SubagentStop"
	hookPreCompact        = "PreCompact"
	hookNotification      = "Notification"

	// ANT2-10 net-new hook events (CLA-01 enumerated only 2 of the lifecycle).
	// ConfigChange gates a non-policy settings change; InstructionsLoaded audits which
	// CLAUDE.md/managed-memory loaded; PostToolBatch fires after a batch of tool calls;
	// SubagentStart pairs the existing SubagentStop; PostCompact pairs PreCompact;
	// Elicitation fires when the agent asks the user for input.
	hookConfigChange       = "ConfigChange"
	hookInstructionsLoaded = "InstructionsLoaded"
	hookPostToolBatch      = "PostToolBatch"
	hookSubagentStart      = "SubagentStart"
	hookPostCompact        = "PostCompact"
	hookElicitation        = "Elicitation"

	// hookMessageDisplay is the 2.1.17x DISPLAY-ONLY hook event (VERIFIED
	// 2026-06-10, docs.claude.com/en/docs/claude-code/hooks; changelog 2.1.152):
	// it fires "while assistant message text is displayed", supports NO matchers,
	// and has NO decision control — its only output is hookSpecificOutput.
	// displayContent, which replaces the delta ON SCREEN only (the transcript and
	// what Claude sees keep the original). A failing MessageDisplay hook cannot
	// block anything: the original text is displayed.
	hookMessageDisplay = "MessageDisplay"

	// the remaining gating + lifecycle events Claude Code ships (VERIFIED
	// 2026-06-19, code.claude.com/docs/en/hooks). UserPromptExpansion gates a command
	// expansion (top-level decision:"block"); TaskCreated/TaskCompleted gate the
	// TaskCreate lifecycle and TeammateIdle an agent-team teammate (continue:false);
	// PostToolUseFailure/Setup/CwdChanged/FileChanged/StopFailure/WorktreeCreate/
	// WorktreeRemove/ElicitationResult round out the recognized set so the governed PEP
	// classifies — and the SIEM enumerates — the full lifecycle, not a subset.
	hookUserPromptExpansion = "UserPromptExpansion"
	hookPostToolUseFailure  = "PostToolUseFailure"
	hookSetup               = "Setup"
	hookTaskCreated         = "TaskCreated"
	hookTaskCompleted       = "TaskCompleted"
	hookTeammateIdle        = "TeammateIdle"
	hookStopFailure         = "StopFailure"
	hookCwdChanged          = "CwdChanged"
	hookFileChanged         = "FileChanged"
	hookWorktreeCreate      = "WorktreeCreate"
	hookWorktreeRemove      = "WorktreeRemove"
	hookElicitationResult   = "ElicitationResult"
)

// permission-decision values for the permission-gating hooks. PreToolUse additionally
// accepts "defer" (hand the decision back to the normal permission flow). PermissionRequest
// maps these onto its decision.behavior shape (allow→allow, else deny — see
// permissionRequestJSON); the other gating events use top-level decision / continue:false.
const (
	permAllow = "allow"
	permDeny  = "deny"
	permAsk   = "ask"
	permDefer = "defer" // PreToolUse only
)

// isEnforcingHook reports whether a hook is one of the permission-gating events on
// which a returned decision can actually allow/deny/ask/defer a Claude Code tool call.
// Only these carry the hookSpecificOutput.permissionDecision contract.
func isEnforcingHook(event string) bool {
	return event == hookPreToolUse || event == hookPermissionRequest
}

// hookMech is the WIRE mechanism the governed PEP must use to express a deny/block on a
// hook event. Claude Code honors a DIFFERENT output shape per event (VERIFIED 2026-06-19,
// code.claude.com/docs/en/hooks); emitting the wrong shape means the deny is silently
// IGNORED — so the mechanism is part of the security contract, not cosmetic.
type hookMech int

const (
	// mechNeutral: no enforceable block. Context/observe events, AND the INVERTED
	// Stop/SubagentStop (where decision:"block" KEEPS the agent running — the opposite of
	// a safety stop — so a block is never emitted). The PEP answers neutrally (+context).
	mechNeutral hookMech = iota
	// mechPermissionDecision: PreToolUse — hookSpecificOutput.permissionDecision
	// (allow|deny|ask|defer). Also the deny-closed default for an UNKNOWN event.
	mechPermissionDecision
	// mechPermissionBehavior: PermissionRequest — hookSpecificOutput.decision.behavior
	// (allow|deny) (+updatedInput). NOT permissionDecision (the schema changed; verified).
	mechPermissionBehavior
	// mechTopLevelDecision: top-level decision:"block" + reason (UserPromptSubmit,
	// UserPromptExpansion, PreCompact, ConfigChange, PostToolBatch).
	mechTopLevelDecision
	// mechContinueFalse: continue:false + stopReason (TaskCreated, TaskCompleted,
	// TeammateIdle) — these events do not honor a top-level decision:"block".
	mechContinueFalse
	// mechPostToolUse: PostToolUse — block FURTHER processing (decision:"block") on a
	// flagged output (the tool already ran), optionally softened by continue:true.
	mechPostToolUse
)

// hookSpec classifies one recognized hook event: its 3-way taxonomy class, the wire
// mechanism a deny/block uses, and whether a deny verdict yields a REAL block (false for
// the inverted Stop/SubagentStop, for context/observe, and for the v1-unwired Elicitation
// action mechanism). This map is the SINGLE SOURCE OF TRUTH the render switch and the
// composition-root decider both consult (VERIFIED 2026-06-19, code.claude.com/docs/en/hooks).
type hookSpec struct {
	class       string // "gating" | "context" | "observe"
	mech        hookMech
	enforceable bool
}

var hookSpecs = map[string]hookSpec{
	// GATING — a hook return can allow/deny/block the action.
	hookPreToolUse:          {"gating", mechPermissionDecision, true},
	hookPermissionRequest:   {"gating", mechPermissionBehavior, true},
	hookUserPromptSubmit:    {"gating", mechTopLevelDecision, true},
	hookUserPromptExpansion: {"gating", mechTopLevelDecision, true},
	hookPreCompact:          {"gating", mechTopLevelDecision, true},
	hookConfigChange:        {"gating", mechTopLevelDecision, true},
	hookPostToolBatch:       {"gating", mechTopLevelDecision, true},
	hookTaskCreated:         {"gating", mechContinueFalse, true},
	hookTaskCompleted:       {"gating", mechContinueFalse, true},
	hookTeammateIdle:        {"gating", mechContinueFalse, true},
	hookStop:                {"gating", mechNeutral, false}, // INVERTED: block keeps it running → neutral
	hookSubagentStop:        {"gating", mechNeutral, false}, // INVERTED (same)
	hookElicitation:         {"gating", mechNeutral, false}, // action accept/decline; not wired v1 → neutral
	hookElicitationResult:   {"gating", mechNeutral, false}, // (same)
	// CONTEXT — additionalContext / output rewrite, no true block.
	hookPostToolUse:        {"context", mechPostToolUse, true}, // can block FURTHER processing
	hookPostToolUseFailure: {"context", mechNeutral, false},
	hookPermissionDenied:   {"context", mechNeutral, false},
	hookMessageDisplay:     {"context", mechNeutral, false},
	hookSessionStart:       {"context", mechNeutral, false},
	hookSetup:              {"context", mechNeutral, false},
	hookSubagentStart:      {"context", mechNeutral, false},
	hookPostCompact:        {"context", mechNeutral, false},
	hookInstructionsLoaded: {"context", mechNeutral, false},
	// OBSERVE — no decision control.
	hookNotification:   {"observe", mechNeutral, false},
	hookSessionEnd:     {"observe", mechNeutral, false},
	hookStopFailure:    {"observe", mechNeutral, false},
	hookCwdChanged:     {"observe", mechNeutral, false},
	hookFileChanged:    {"observe", mechNeutral, false},
	hookWorktreeCreate: {"observe", mechNeutral, false}, // path-return, not a governance gate
	hookWorktreeRemove: {"observe", mechNeutral, false},
}

// hookSpecFor returns the spec for an event; an UNKNOWN event is a deny-closed permission
// gate (never a silent allow — the only safe default for an event we do not recognize).
func hookSpecFor(event string) hookSpec {
	if s, ok := hookSpecs[event]; ok {
		return s
	}
	return hookSpec{class: "unknown", mech: mechPermissionDecision, enforceable: true}
}

// hookMechFor returns the wire mechanism for an event (the render switch consults it).
func hookMechFor(event string) hookMech { return hookSpecFor(event).mech }

// HookEnforcement is the governed PEP's enforcement contract for a hook event, exported so
// the composition-root decider (which owns the policy + per-event default posture) shares
// ONE classification with the connector's renderer.
type HookEnforcement struct {
	// Class is the 3-way taxonomy: "gating" | "context" | "observe" | "unknown".
	Class string
	// Enforceable reports whether a deny verdict yields a REAL block in the wire. False for
	// the inverted Stop/SubagentStop, every context/observe event, and the v1-unwired
	// Elicitation events — for those the decider returns neutral (no policy, no HITL).
	Enforceable bool
	// ClassicGate marks the PreToolUse/PermissionRequest permission gate (and an UNKNOWN
	// event): when no governed rule matches, the operator's policy DEFAULT applies
	// (deny-closed). For the other gating events the decider applies the per-event SAFE
	// default instead (neutral for UX/lifecycle, deny for state mutation).
	ClassicGate bool
}

// HookEnforcementFor returns the enforcement contract for a hook event.
func HookEnforcementFor(event string) HookEnforcement {
	s := hookSpecFor(event)
	classic := s.mech == mechPermissionDecision || s.mech == mechPermissionBehavior || s.mech == mechPostToolUse || s.class == "unknown"
	return HookEnforcement{Class: s.class, Enforceable: s.enforceable, ClassicGate: classic}
}

// knownHookEvents is the recognized lifecycle hook set (ANT2-10 +). An event NOT in
// it is still observed for liveness (never dropped) and the PEP treats it as a deny-closed
// permission gate; this set is the inventory authoring UI offers and the lifecycle a
// SIEM can enumerate. It is derived from hookSpecs so the two never drift.
var knownHookEvents = sortedHookEvents()

func sortedHookEvents() []string {
	out := make([]string, 0, len(hookSpecs))
	for e := range hookSpecs {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// isDisplayHook reports whether a hook event is DISPLAY-ONLY: it carries no decision
// control. Retained for the render switch's readability; equivalent to mechNeutral on
// MessageDisplay.
func isDisplayHook(event string) bool { return event == hookMessageDisplay }

// isContextFeedbackHook reports whether a hook event's only meaningful PEP output is
// hookSpecificOutput.additionalContext (changelog 2.1.163) — the INVERTED
// Stop/SubagentStop, where a deny-closed "block" would KEEP the agent running (the opposite
// of safe), so the PEP's posture for them is neutral.
func isContextFeedbackHook(event string) bool {
	return event == hookStop || event == hookSubagentStop
}

// KnownHookEvents returns the recognized lifecycle hook events (a copy, so a caller
// cannot mutate package state). It is the lifecycle inventory authors against.
func KnownHookEvents() []string { return append([]string(nil), knownHookEvents...) }

// IsKnownHook reports whether event is a recognized lifecycle hook (ANT2-10).
func IsKnownHook(event string) bool {
	for _, h := range knownHookEvents {
		if h == event {
			return true
		}
	}
	return false
}

// permissionValueValid reports whether a permission-decision value is valid FOR THE
// GIVEN EVENT (ANT2-10): "defer" is accepted only on PreToolUse; allow/deny/ask apply
// to both gating hooks. An unknown value is rejected (fail-closed), so a typo never
// silently becomes an allow.
func permissionValueValid(event, value string) bool {
	switch value {
	case permAllow, permDeny, permAsk:
		return true
	case permDefer:
		return event == hookPreToolUse
	default:
		return false
	}
}

// maxHookBody caps a hook request body so a misconfigured or hostile poster
// cannot exhaust memory (a hook payload is small; tool_input is the only
// unbounded part and we only read a few fields from it).
const maxHookBody = 1 << 20 // 1 MiB

// hookPayload is the subset of the Claude Code PreToolUse/PostToolUse hook JSON
// this connector reads. tool_use_id is accepted when present (the Agent SDK
// supplies it; the CLI stdin payload may omit it, in which case correlation falls
// back to session+tool). tool_input is the resource-bearing field; it is reduced
// to a redacted resource reference at ingest and never stored raw (docs/SECURITY-HARDENING.md).
type hookPayload struct {
	SessionID     string         `json:"session_id"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolUseID     string         `json:"tool_use_id"`
	ToolInput     map[string]any `json:"tool_input"`
}

// hookEvent is the connector's normalized hook signal: who (session), what
// (tool), the correlation id when present, the raw input (consumed immediately by
// the resource resolver, never persisted) and the receive time.
type hookEvent struct {
	sessionID string
	event     string
	toolName  string
	toolUseID string
	input     map[string]any
	at        time.Time
}

// emptyHookResponse is the object Claude Code treats as "no decision" (proceed):
// the cooperative-by-default answer the connector returns unless governed
// enforcement is enabled AND a policy rule matches.
var emptyHookResponse = []byte("{}")

// hookDecision is the connector's verdict for a permission-gating hook (CLA-01).
// The zero value is "no decision", so the handler returns emptyHookResponse and
// the agent proceeds — enforcement is strictly opt-in and additive. A non-empty
// decision is returned synchronously in the hook response to allow/deny/ask the
// tool call, the only programmatic enforcement point Anthropic ships for Claude
// Code.
type hookDecision struct {
	event      string // hook_event_name, echoed in hookSpecificOutput
	permission string // PreToolUse: allow|deny|ask|defer ; PermissionRequest: allow|deny|ask
	reason     string // short, non-sensitive explanation for the user/agent
	// updatedInput is the PreToolUse / PermissionRequest governed REWRITE (CLA-01): a sanitized
	// replacement tool_input Claude Code runs INSTEAD of the original (e.g. forcing
	// --dry-run, narrowing a path). It is emitted in hookSpecificOutput.updatedInput
	// only on PreToolUse, only when the policy asked for a rewrite. Verified field name
	// against code.claude.com/docs/en/hooks (2026-06-09). nil = no rewrite.
	updatedInput map[string]any
	// additionalContext is the optional string Claude Code appends to the model's
	// context alongside the decision (verified hookSpecificOutput field). Empty = none.
	additionalContext string
}

// isEmpty reports whether there is no decision to return.
func (d hookDecision) isEmpty() bool { return d.permission == "" }

// json renders the permission-gating hook decision payload. PreToolUse uses
// hookSpecificOutput.permissionDecision (+ permissionDecisionReason, + the governed
// updatedInput rewrite on allow/ask); PermissionRequest uses the nested
// hookSpecificOutput.decision.behavior shape (VERIFIED 2026-06-19,
// code.claude.com/docs/en/hooks — the schema changed: emitting permissionDecision on a
// PermissionRequest is silently IGNORED, so a deny would not gate). An UNKNOWN event falls
// to the permissionDecision schema (the deny-closed default). An empty/invalid decision (or
// a marshal failure) renders the neutral "{}" so a bug can never wedge the agent.
func (d hookDecision) json() []byte {
	if d.isEmpty() || !permissionValueValid(d.event, d.permission) {
		return emptyHookResponse
	}
	if d.event == hookPermissionRequest {
		return d.permissionRequestJSON()
	}
	// PreToolUse + any UNKNOWN event → permissionDecision schema.
	hso := map[string]any{
		"hookEventName":            d.event,
		"permissionDecision":       d.permission,
		"permissionDecisionReason": d.reason,
	}
	// updatedInput is a PreToolUse governed rewrite: emit it only there, only when
	// the policy supplied a sanitized replacement input and the decision still lets the call
	// proceed (allow/ask — a deny runs nothing, so a rewrite would be moot).
	if d.event == hookPreToolUse && len(d.updatedInput) > 0 && d.permission != permDeny {
		hso["updatedInput"] = d.updatedInput
	}
	if d.additionalContext != "" {
		hso["additionalContext"] = d.additionalContext
	}
	b, err := json.Marshal(map[string]any{"hookSpecificOutput": hso})
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// permissionRequestJSON renders the PermissionRequest decision in its VERIFIED current wire
// shape: hookSpecificOutput.decision.behavior ∈ {allow, deny} (+ updatedInput on allow). An
// "ask" verdict has no PermissionRequest wire form, so it is deny-closed (behavior:deny).
// The reason is NOT emitted in the decision object (the verified schema is behavior +
// updatedInput; the reason is preserved in the governed audit trail, never fabricated onto
// the wire).
func (d hookDecision) permissionRequestJSON() []byte {
	dec := map[string]any{"behavior": "deny"}
	if d.permission == permAllow {
		dec["behavior"] = "allow"
		if len(d.updatedInput) > 0 {
			dec["updatedInput"] = d.updatedInput
		}
	}
	hso := map[string]any{"hookEventName": hookPermissionRequest, "decision": dec}
	if d.additionalContext != "" {
		hso["additionalContext"] = d.additionalContext
	}
	b, err := json.Marshal(map[string]any{"hookSpecificOutput": hso})
	if err != nil {
		return emptyHookResponse
	}
	return b
}

// hookHandler returns an http.Handler that accepts a Claude Code hook payload
// (POST, JSON), optionally returns a governed permission DECISION, and forwards a
// normalized hookEvent to onHook for the telemetry path. It is permissive on
// method/shape (a hook is not a REST API): on any malformed payload it answers 200
// with the neutral empty object so a bad request never blocks the agent.
//
// decide is consulted FIRST because its result IS the response that gates the tool
// call; it returns an empty decision (→ "{}") unless governed enforcement is
// enabled and a policy rule matches (CLA-01). decide may be nil (no enforcement).
func hookHandler(onHook func(hookEvent), decide func(hookEvent) hookDecision, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := emptyHookResponse
		defer func() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(resp)
		}()
		if r.Method != http.MethodPost {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxHookBody))
		if err != nil {
			return
		}
		ev, ok := parseHook(body, now())
		if !ok {
			return
		}
		if decide != nil {
			if d := decide(ev); !d.isEmpty() {
				resp = d.json()
			}
		}
		onHook(ev)
	})
}

// parseHook decodes a hook payload into a hookEvent, stamping receive time. It
// returns false if the JSON is invalid or carries neither a session nor a tool
// name (nothing to attribute).
func parseHook(body []byte, at time.Time) (hookEvent, bool) {
	var p hookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return hookEvent{}, false
	}
	if p.SessionID == "" && p.ToolName == "" {
		return hookEvent{}, false
	}
	return hookEvent{
		sessionID: p.SessionID,
		event:     p.HookEventName,
		toolName:  p.ToolName,
		toolUseID: p.ToolUseID,
		input:     p.ToolInput,
		at:        at,
	}, true
}

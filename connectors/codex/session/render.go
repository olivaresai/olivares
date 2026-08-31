// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
)

// render.go is the OUTBOUND half of the Codex hook wire, and it is the security contract
// of this connector — not a formatting detail.
//
// Codex reads a hook's stdout and honors a DIFFERENT shape per event. Emitting the wrong
// shape is not an error and produces no warning: the verdict is simply IGNORED and the
// agent proceeds. So the mapping below is a closed table with one entry per event, and an
// unknown event falls to the strictest branch rather than to a convenient default.
//
// Every shape here comes from the draft-07 schemas embedded in codex-cli 0.145.0 (titles
// "<event>.command.output"), cross-checked against the official reference, and the
// PreToolUse deny was additionally confirmed END TO END against a live `codex exec`:
//
//	ERROR codex_core::tools::router: error=Command blocked by PreToolUse hook: …
//	hook: PreToolUse Blocked
//
// Two limits of the PROVIDER shape what we can promise, and both are written into the
// behavior rather than into a comment nobody reads:
//
//  1. Of permissionDecision, Codex ACTS ON "deny" only; "allow" and "ask" are parsed and
//     then rejected. So an ALLOW is rendered as NON-INTERFERENCE (the neutral shape), never
//     as a claim of permission we do not actually grant.
//  2. PermissionRequest's wire admits allow|deny and has NO "ask". An ASK verdict therefore
//     resolves to DENY on Codex — declared, because the silent alternative is to let it
//     through.

// mech is the per-event wire mechanism: which shape that event actually honors.
type mech int

const (
	// mechPermissionDecision — PreToolUse. hookSpecificOutput.permissionDecision.
	mechPermissionDecision mech = iota
	// mechPermissionRequest — PermissionRequest. hookSpecificOutput.decision.behavior.
	mechPermissionRequest
	// mechTopLevelBlock — PostToolUse, UserPromptSubmit. Top-level decision:"block".
	mechTopLevelBlock
	// mechInvertedStop — Stop, SubagentStop. Here decision:"block" is INVERTED: it blocks
	// the agent from STOPPING, i.e. it keeps it running. A deny expressed as "block" on
	// these events would be the opposite of failing safe, so this mechanism never emits
	// one; it uses continue:false, which ends the turn.
	mechInvertedStop
	// mechContinueFalse — SessionStart, SubagentStart, PreCompact, PostCompact. These
	// cannot veto an act; the strongest thing they can say is "do not continue".
	mechContinueFalse
	// mechNone — SessionEnd. It is the ONLY event with an input schema and NO output
	// schema: nothing written to stdout can change anything. Observation only.
	mechNone
)

// mechFor maps an event to its wire mechanism. The default branch is the load-bearing
// one: an event this connector does not know gets the strictest mechanism it can express,
// because a Codex release that adds an event must not thereby acquire an ungoverned path.
func mechFor(event string) mech {
	switch event {
	case EventPreToolUse:
		return mechPermissionDecision
	case EventPermissionReq:
		return mechPermissionRequest
	case EventPostToolUse, EventUserPromptSubmit:
		return mechTopLevelBlock
	case EventStop, EventSubagentStop:
		return mechInvertedStop
	case EventSessionStart, EventSubagentStart, EventPreCompact, EventPostCompact:
		return mechContinueFalse
	case EventSessionEnd:
		return mechNone
	default:
		return mechContinueFalse
	}
}

// CanImpede reports whether a DENY on this event can actually prevent the act, as opposed
// to merely ending the turn afterwards. It exists so the session plane can declare an
// honest posture instead of painting every event as enforced: PostToolUse "blocks" a tool
// whose side effects already happened, and SessionEnd cannot block at all.
func CanImpede(event string) bool {
	switch event {
	case EventPreToolUse, EventPermissionReq:
		return true
	default:
		return false
	}
}

// Verdict values. They are the connector's public vocabulary so the AGPL composition root
// can name a decision without importing internals.
const (
	VerdictAllow = "allow"
	VerdictDeny  = "deny"
	VerdictAsk   = "ask"
)

// Decision is the governed verdict for one hook call. Its ZERO VALUE IS A DENY: an empty
// Verdict renders deny-closed, so a decider that returns a zero value on a path it forgot
// still fails closed rather than waving the call through.
type Decision struct {
	Verdict string
	// Reason is short, non-sensitive, and surfaced to the agent and the user.
	Reason string
	// AdditionalContext is optional text appended to the model's conversation. It is only
	// honored on the events whose schema declares it; it is dropped elsewhere rather
	// than emitted into a field that event does not have.
	AdditionalContext string
	// PolicyVersion is recorded on the receipt; it never reaches the wire.
	PolicyVersion string
	// SessionSID is the CANONICAL session identity the governed side resolved for this
	// call (SG-00: "osn_" + UUIDv7). It is empty when nothing was resolved, and an empty
	// value means "do not emit": a session fact with no canonical identity is a row the
	// live view discards, and emitting it anyway would look like a delivered fact.
	//
	// It is returned by the DECIDER rather than resolved here because ResolveSession lives
	// in modules/sessions, which this Apache package may not import.
	SessionSID string
	// Enforced reports whether this verdict could actually PREVENT the act, as opposed to
	// arriving after it. It is what keeps /sessions from painting an observed Codex
	// session identically to an enforced one.
	Enforced bool
}

// blocking reports whether this verdict must impede. ASK counts as blocking because Codex
// has no ask: see the package-level note.
func (d Decision) blocking() bool {
	return d.Verdict != VerdictAllow
}

// Render encodes a decision in the shape the event honors. It never returns an error: a
// rendering that cannot be produced degrades to the strictest shape available, because
// returning an error here would leave the hook writing nothing to stdout, which Codex
// reads as "no objection".
func Render(event string, d Decision) []byte {
	if !d.blocking() {
		return renderAllow(event, d)
	}
	reason := d.Reason
	if reason == "" {
		reason = "denied by governance policy"
	}
	switch mechFor(event) {
	case mechPermissionDecision:
		return mustJSON(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            event,
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		})
	case mechPermissionRequest:
		return mustJSON(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName": EventPermissionReq,
				"decision":      map[string]any{"behavior": "deny"},
			},
		})
	case mechTopLevelBlock:
		// `decision:"block"` and not `continue:false`, deliberately. On PostToolUse the
		// tool has already run, so the useful thing a deny can still do is stop the OUTPUT
		// being processed and hand the reason back so the agent adapts. `continue:false`
		// would end the turn outright — heavier, and it throws away the agent's chance to
		// comply. The stricter form stays available to a caller that wants it; what is not
		// available is choosing it by accident.
		return mustJSON(map[string]any{"decision": "block", "reason": reason})
	case mechInvertedStop, mechContinueFalse:
		return mustJSON(map[string]any{"continue": false, "stopReason": reason})
	case mechNone:
		// SessionEnd cannot be impeded. Emitting a synthetic block here would be a lie
		// told to our own logs: the wire has no field for it and Codex would ignore it.
		return mustJSON(map[string]any{})
	default:
		return mustJSON(map[string]any{"continue": false, "stopReason": reason})
	}
}

// renderAllow renders NON-INTERFERENCE. It deliberately never emits permissionDecision
// "allow": Codex rejects that value, so emitting it would be a claim of a grant that the
// engine does not act on. Where the event's schema declares additionalContext, an allow
// may carry it; everywhere else it is dropped rather than smuggled into a foreign field.
func renderAllow(event string, d Decision) []byte {
	if d.AdditionalContext == "" || !supportsAdditionalContext(event) {
		return mustJSON(map[string]any{})
	}
	return mustJSON(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     event,
			"additionalContext": d.AdditionalContext,
		},
	})
}

// supportsAdditionalContext lists the events whose embedded output schema declares the
// field. Anything else silently discards it, so we do not send it.
func supportsAdditionalContext(event string) bool {
	switch event {
	case EventPreToolUse, EventPostToolUse, EventUserPromptSubmit, EventSessionStart, EventSubagentStart:
		return true
	default:
		return false
	}
}

// DenyClosed renders the verdict this connector emits when it could not obtain a governed
// decision at all — endpoint unset, unreachable, non-2xx, unparsable payload. It routes
// through the SAME table as a real deny, so the fail-closed answer is in the shape each
// event honors rather than in a one-size shape most events ignore.
func DenyClosed(event, reason string) []byte {
	return Render(event, Decision{Verdict: VerdictDeny, Reason: reason})
}

// ExitCodeFor reports the process exit code the hook command should use alongside its
// stdout. Codex documents exit 2 with the reason on stderr as a blocking mechanism, and
// warns explicitly when a hook "exited with code 2 but did not write a blocking reason to
// stderr" — so exit 2 is only ever returned together with a reason.
//
// It is used as BELT AND BRACES on the one branch that needs it: an event this connector
// does not know. There the stdout shape is a guess, so the exit code carries the veto too.
// On known events the stdout shape is the verified mechanism and the exit code stays 0,
// which is how Codex consumes a hook decision.
func ExitCodeFor(event string, d Decision) int {
	if d.blocking() && !IsKnownEvent(event) {
		return 2
	}
	return 0
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Unreachable for the map[string]any shapes above, but a hook that writes nothing
		// is read as "no objection", so even here we emit something that impedes.
		return []byte(`{"continue":false,"stopReason":"governance could not render a decision (deny-closed)"}`)
	}
	return b
}

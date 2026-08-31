// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"testing"
)

// decode is a helper: the wire is JSON, so the assertions are about STRUCTURE, not about
// byte equality of a marshaled map (whose key order Go does not promise).
func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("rendered output is not JSON: %v (%s)", err, b)
	}
	return m
}

func hookSpecific(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	hs, ok := m["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput object, got %+v", m)
	}
	return hs
}

// TestDenyRendersTheMechanismEachEventHonours is the security contract in one table.
// The mapping is not stylistic: emitting the wrong shape produces NO error and NO warning
// from Codex — the verdict is simply ignored and the agent proceeds.
func TestDenyRendersTheMechanismEachEventHonours(t *testing.T) {
	deny := Decision{Verdict: VerdictDeny, Reason: "policy says no"}

	t.Run("PreToolUse uses permissionDecision", func(t *testing.T) {
		hs := hookSpecific(t, decode(t, Render(EventPreToolUse, deny)))
		if hs["permissionDecision"] != "deny" {
			t.Errorf("PreToolUse must deny via permissionDecision, got %+v", hs)
		}
		if hs["hookEventName"] != EventPreToolUse {
			t.Errorf("hookEventName must echo the event, got %v", hs["hookEventName"])
		}
		if hs["permissionDecisionReason"] != "policy says no" {
			t.Errorf("the reason must travel with the deny, got %v", hs["permissionDecisionReason"])
		}
	})

	t.Run("PermissionRequest uses decision.behavior", func(t *testing.T) {
		hs := hookSpecific(t, decode(t, Render(EventPermissionReq, deny)))
		dec, ok := hs["decision"].(map[string]any)
		if !ok || dec["behavior"] != "deny" {
			t.Errorf("PermissionRequest must deny via decision.behavior, got %+v", hs)
		}
	})

	for _, ev := range []string{EventPostToolUse, EventUserPromptSubmit} {
		t.Run(ev+" uses a top-level block", func(t *testing.T) {
			m := decode(t, Render(ev, deny))
			if m["decision"] != "block" || m["reason"] != "policy says no" {
				t.Errorf("%s must block top-level with a reason, got %+v", ev, m)
			}
		})
	}

	for _, ev := range []string{EventSessionStart, EventSubagentStart, EventPreCompact, EventPostCompact} {
		t.Run(ev+" can only refuse to continue", func(t *testing.T) {
			m := decode(t, Render(ev, deny))
			if m["continue"] != false || m["stopReason"] != "policy says no" {
				t.Errorf("%s must express refusal as continue:false, got %+v", ev, m)
			}
		})
	}
}

// TestStopInversionNeverBlocks pins the one place where the obvious rendering is BACKWARDS.
// On Stop and SubagentStop, decision:"block" blocks the agent from STOPPING — it keeps it
// running. A governed deny rendered that way would be the exact opposite of failing safe.
func TestStopInversionNeverBlocks(t *testing.T) {
	for _, ev := range []string{EventStop, EventSubagentStop} {
		m := decode(t, Render(ev, Decision{Verdict: VerdictDeny, Reason: "no"}))
		if m["decision"] == "block" {
			t.Errorf("%s: a deny must NOT render decision:block — that keeps the agent running", ev)
		}
		if m["continue"] != false {
			t.Errorf("%s: a deny must end the turn via continue:false, got %+v", ev, m)
		}
	}
}

// TestSessionEndCannotImpede pins the asymmetry measured in the binary: SessionEnd is the
// only event with an input schema and no output schema. Pretending otherwise would put a
// block in our logs that never existed on the wire.
func TestSessionEndCannotImpede(t *testing.T) {
	if CanImpede(EventSessionEnd) {
		t.Error("SessionEnd has no output schema; it cannot impede")
	}
	m := decode(t, Render(EventSessionEnd, Decision{Verdict: VerdictDeny, Reason: "no"}))
	if len(m) != 0 {
		t.Errorf("SessionEnd must render an empty object, got %+v", m)
	}
}

// TestOnlyPreToolUseAndPermissionRequestCanImpede keeps the posture honest. PostToolUse
// can block further processing, but the tool ALREADY RAN — calling that "enforced" in the
// console would overstate the control.
func TestOnlyPreToolUseAndPermissionRequestCanImpede(t *testing.T) {
	for _, ev := range KnownEvents() {
		want := ev == EventPreToolUse || ev == EventPermissionReq
		if got := CanImpede(ev); got != want {
			t.Errorf("CanImpede(%s) = %v, want %v", ev, got, want)
		}
	}
}

// TestZeroDecisionIsDeny is the fail-closed guard for a decider that returns a zero value
// on a path it forgot.
func TestZeroDecisionIsDeny(t *testing.T) {
	hs := hookSpecific(t, decode(t, Render(EventPreToolUse, Decision{})))
	if hs["permissionDecision"] != "deny" {
		t.Errorf("the zero Decision must render a deny, got %+v", hs)
	}
	if hs["permissionDecisionReason"] == "" || hs["permissionDecisionReason"] == nil {
		t.Error("a deny with no reason must still carry one: Codex surfaces it to the agent")
	}
}

// TestAllowNeverClaimsPermission pins provider limit #1: Codex parses permissionDecision
// "allow" and then REJECTS it. Emitting it would be claiming a grant the engine does not
// act on, and would make our logs disagree with the agent's behavior.
func TestAllowNeverClaimsPermission(t *testing.T) {
	for _, ev := range KnownEvents() {
		b := Render(ev, Decision{Verdict: VerdictAllow})
		m := decode(t, b)
		if hs, ok := m["hookSpecificOutput"].(map[string]any); ok {
			if _, bad := hs["permissionDecision"]; bad {
				t.Errorf("%s: an allow must not emit permissionDecision, got %s", ev, b)
			}
		}
		if m["decision"] != nil || m["continue"] == false {
			t.Errorf("%s: an allow must be non-interference, got %s", ev, b)
		}
	}
}

// TestAskResolvesToDeny pins provider limit #2. PermissionRequest's wire admits only
// allow|deny: there is no ask. Treating our ASK as anything but a deny would let it through.
func TestAskResolvesToDeny(t *testing.T) {
	hs := hookSpecific(t, decode(t, Render(EventPermissionReq, Decision{Verdict: VerdictAsk, Reason: "needs a human"})))
	dec, ok := hs["decision"].(map[string]any)
	if !ok || dec["behavior"] != "deny" {
		t.Errorf("an ASK on Codex must resolve to deny, got %+v", hs)
	}
	pre := hookSpecific(t, decode(t, Render(EventPreToolUse, Decision{Verdict: VerdictAsk, Reason: "needs a human"})))
	if pre["permissionDecision"] != "deny" {
		t.Errorf("an ASK on PreToolUse must resolve to deny (Codex rejects ask), got %+v", pre)
	}
}

// TestUnknownEventIsDenyClosedOnBothChannels pins the belt-and-braces branch. For an event
// this connector has no verified shape for, the stdout shape is a guess — so the exit code
// carries the veto as well.
func TestUnknownEventIsDenyClosedOnBothChannels(t *testing.T) {
	const ev = "SomeFutureCodexEvent"
	if IsKnownEvent(ev) {
		t.Fatal("test premise broken: the event must be unknown")
	}
	d := Decision{Verdict: VerdictDeny, Reason: "unknown event"}
	m := decode(t, Render(ev, d))
	if m["continue"] != false {
		t.Errorf("an unknown event must be deny-closed on stdout, got %+v", m)
	}
	if got := ExitCodeFor(ev, d); got != 2 {
		t.Errorf("an unknown event must also deny via exit 2, got %d", got)
	}
	// And a known event must NOT use exit 2: its stdout shape is the verified mechanism,
	// and exit 2 there would double-report a verdict Codex already honored.
	if got := ExitCodeFor(EventPreToolUse, d); got != 0 {
		t.Errorf("a known event's deny travels in stdout; exit code must stay 0, got %d", got)
	}
	// An allow on an unknown event is still an allow — deny-closed is about failure to
	// decide, not about refusing everything unfamiliar once a decision exists.
	if got := ExitCodeFor(ev, Decision{Verdict: VerdictAllow}); got != 0 {
		t.Errorf("an allow must never exit 2, got %d", got)
	}
}

// TestEveryKnownEventHasAnExplicitMechanism is the coverage guard. Without it, adding an
// event to knownEvents and forgetting mechFor would silently give it the fallback shape —
// which is exactly the class of mistake this file exists to prevent.
func TestEveryKnownEventHasAnExplicitMechanism(t *testing.T) {
	want := map[string]mech{
		EventPreToolUse:       mechPermissionDecision,
		EventPermissionReq:    mechPermissionRequest,
		EventPostToolUse:      mechTopLevelBlock,
		EventUserPromptSubmit: mechTopLevelBlock,
		EventStop:             mechInvertedStop,
		EventSubagentStop:     mechInvertedStop,
		EventSessionStart:     mechContinueFalse,
		EventSubagentStart:    mechContinueFalse,
		EventPreCompact:       mechContinueFalse,
		EventPostCompact:      mechContinueFalse,
		EventSessionEnd:       mechNone,
	}
	for _, ev := range KnownEvents() {
		w, ok := want[ev]
		if !ok {
			t.Errorf("event %q is known but this table does not pin its mechanism — add it deliberately, do not let it fall to the default", ev)
			continue
		}
		if got := mechFor(ev); got != w {
			t.Errorf("mechFor(%s) = %v, want %v", ev, got, w)
		}
	}
	if len(want) != len(KnownEvents()) {
		t.Errorf("the mechanism table has %d entries for %d known events", len(want), len(KnownEvents()))
	}
}

// TestDenyClosedUsesTheSameTable guards against the fail-closed path drifting away from
// the governed path — a deny-closed in a shape the event ignores is an open door.
func TestDenyClosedUsesTheSameTable(t *testing.T) {
	for _, ev := range KnownEvents() {
		a := DenyClosed(ev, "endpoint unreachable")
		b := Render(ev, Decision{Verdict: VerdictDeny, Reason: "endpoint unreachable"})
		if string(a) != string(b) {
			t.Errorf("%s: deny-closed and governed deny must render identically\n  closed:  %s\n  governed:%s", ev, a, b)
		}
	}
}

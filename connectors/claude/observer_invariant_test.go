// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import "testing"

// TestInvariant_S77_ObserverNeverDeny pins both halves of the contract that the
// hookSpecs taxonomy encodes — the SINGLE source of truth the renderer and the PEP
// decider both consult:
//
//  1. every "observe"-class event is never-deny (enforceable=false) BY DESIGN, and
//  2. a deny-capable ("gating") or UNRECOGNIZED event is never silently downgraded
//     to never-deny: an unknown event is a deny-closed permission gate.
//
// A regression that flips an observe event to enforceable, or (the dangerous
// direction) a gating/unknown event to non-enforceable — a silent "observer
// downgrade" that turns a deny-capable surface into never-deny without explicit
// config — must fail here.
func TestInvariant_S77_ObserverNeverDeny(t *testing.T) {
	for event, spec := range hookSpecs {
		if spec.class == "observe" && spec.enforceable {
			t.Errorf("observe event %q must be never-deny (enforceable=false), got enforceable=true", event)
		}
	}

	// A gating event stays deny-capable — it cannot silently relax to observe.
	if s := hookSpecFor(hookPreToolUse); !s.enforceable || s.class != "gating" {
		t.Errorf("PreToolUse must stay a deny-capable gating event, got %+v", s)
	}

	// An UNKNOWN event is deny-closed (a classic permission gate), never a silent observe.
	const bogus = "this-is-not-a-real-hook-event"
	if u := hookSpecFor(bogus); u.class != "unknown" || !u.enforceable {
		t.Errorf("unknown event must be deny-closed (class=unknown, enforceable=true), got %+v", u)
	}
	if he := HookEnforcementFor(bogus); !he.Enforceable || !he.ClassicGate {
		t.Errorf("unknown event enforcement must be a deny-closed classic gate, got %+v", he)
	}
}

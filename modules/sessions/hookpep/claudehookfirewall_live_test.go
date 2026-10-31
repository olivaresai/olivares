// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/model"
)

type denyingHookInspector struct{ calls int }

func (d *denyingHookInspector) Inspect(context.Context, claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	d.calls++
	return claudeapi.ContentInspectionDecision{Forward: false, Block: true, Status: 403, Reason: "secret in tool input"}
}

// The hook firewall follows a settings change made while the engine runs: the Decider is
// built once, and each tool call asks the live source what is in force.
func TestHookFirewallLiveInspectorFollowsSettingsWithoutRebuild(t *testing.T) {
	var live atomic.Pointer[denyingHookInspector]
	d := &Decider{InspectorLive: func() ContentInspector {
		if i := live.Load(); i != nil {
			return i
		}
		return nil
	}}
	run := func() claudeapi.ContentInspectionDecision {
		return d.runHookFirewall(context.Background(), model.TenantID("t_x"), "actor", hookAgent{}, claude.HookDecisionInput{Tool: "Bash", Event: "PreToolUse"})
	}

	if dec := run(); !dec.Forward {
		t.Fatal("no inspector configured: the firewall must be an inert clean pass")
	}
	inspector := &denyingHookInspector{}
	live.Store(inspector)
	if dec := run(); dec.Forward {
		t.Fatal("inspector saved while running must govern the next tool call")
	}
	if inspector.calls != 1 {
		t.Fatalf("inspector calls = %d; want 1", inspector.calls)
	}
	live.Store(nil)
	if dec := run(); !dec.Forward {
		t.Fatal("inspector turned off while running must let the next tool call through")
	}
}

// The live source wins over the static field: a settings change is never shadowed by the
// inspector the engine started with.
func TestHookFirewallLiveSourceWinsOverStaticInspector(t *testing.T) {
	static := &denyingHookInspector{}
	d := &Decider{Inspector: static, InspectorLive: func() ContentInspector { return nil }}
	dec := d.runHookFirewall(context.Background(), model.TenantID("t_x"), "actor", hookAgent{}, claude.HookDecisionInput{Tool: "Bash", Event: "PreToolUse"})
	if !dec.Forward || static.calls != 0 {
		t.Fatalf("static inspector consulted although the live source says none (forward=%v calls=%d)", dec.Forward, static.calls)
	}
}

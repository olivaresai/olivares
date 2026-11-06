// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"testing"
)

// A background task of a Claude Code session (a shell, a monitor, an agent it runs on its
// own) is stopped with Claude Code's own stop_task control request; the session and its
// turn keep running.
func TestClaudeStopTask_ControlRequestStopsOneTaskNotTheSession(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-stop-task"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
	p := fr.lastProc()
	seen := claudeProtocolStub(t, p, "success")
	if _, err := m.stopRunTask(ctx, tenant, dto.RunRef, "bxsdzgtr2", actorU, actorKindU); err != nil {
		t.Fatalf("stop task: %v", err)
	}
	req := <-seen
	r, _ := req["request"].(map[string]any)
	if r["subtype"] != "stop_task" || r["task_id"] != "bxsdzgtr2" {
		t.Fatalf("control request = %v, want stop_task for bxsdzgtr2", req)
	}
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done {
		t.Fatal("stopping one background task stopped the session's process")
	}
	if _, err := m.stopRunTask(ctx, tenant, dto.RunRef, "", actorU, actorKindU); statusOf(err) != 400 {
		t.Fatalf("an empty task id = %v, want 400", err)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// claudeProtocolStub answers the first interrupt control request it sees on the
// fake child's stdin the way Claude Code does, with answer (or not at all when
// answer is ""). It returns the request it saw.
func claudeProtocolStub(t *testing.T, p *fakeProc, answer string) <-chan map[string]any {
	t.Helper()
	seen := make(chan map[string]any, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p.mu.Lock()
			sent := append([][]byte(nil), p.sent...)
			p.mu.Unlock()
			for _, line := range sent {
				var req map[string]any
				if json.Unmarshal(line, &req) != nil || req["type"] != "control_request" {
					continue
				}
				seen <- req
				if answer != "" {
					id, _ := req["request_id"].(string)
					resp := map[string]any{"subtype": answer, "request_id": id}
					if answer == "error" {
						resp["error"] = "no turn is running"
					}
					b, _ := json.Marshal(map[string]any{"type": "control_response", "response": resp})
					p.out <- OutputFrame{Stream: streamStdout, Data: b}
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return seen
}

// Root, 2026-10-01: Claude Code turns could not be interrupted, only stopped. The
// engine now sends Claude Code's own control request on its stream-json stdin and
// waits for its answer; the session stays live.
func TestClaudeInterrupt_ControlRequestStopsTheTurnNotTheSession(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-interrupt"}
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
	got, err := m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	req := <-seen
	if r, _ := req["request"].(map[string]any); r["subtype"] != "interrupt" {
		t.Fatalf("control request = %v, want subtype interrupt", req)
	}
	if id, _ := req["request_id"].(string); id == "" {
		t.Fatalf("control request carries no request_id: %v", req)
	}
	if got.State != stateRunning {
		t.Fatalf("after interrupt the session is %q, want running", got.State)
	}
	p.mu.Lock()
	done := p.done
	p.mu.Unlock()
	if done {
		t.Fatal("interrupting the turn stopped the process")
	}
}

func TestClaudeInterrupt_SaysWhatClaudeAnswered(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-interrupt-refused"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
	claudeProtocolStub(t, fr.lastProc(), "error")
	_, err = m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU)
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("refused interrupt = %v, want 409 with Claude's answer", err)
	}
}

func TestClaudeInterrupt_NoAnswerIsSaidNotAssumed(t *testing.T) {
	old := claudeInterruptWait
	claudeInterruptWait = 200 * time.Millisecond
	t.Cleanup(func() { claudeInterruptWait = old })
	ctx := context.Background()
	fr := &fakeRunner{initSID: "sess-interrupt-silent"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: actorU, ActorKind: actorKindU,
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	waitFor(t, "running", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.ClaudeSessionID != "" })
	claudeProtocolStub(t, fr.lastProc(), "")
	_, err = m.interruptRun(ctx, tenant, dto.RunRef, actorU, actorKindU)
	if statusOf(err) != http.StatusGatewayTimeout {
		t.Fatalf("unanswered interrupt = %v, want 504", err)
	}
}

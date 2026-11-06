// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
	"time"
)

// An approval the person can still see is a question nobody will act on once its
// turn is interrupted or its session closed. The authority's wait is what keeps the
// approval card up, so ending the request must end that wait at once, not when the
// approval deadline expires.
func TestPendingApprovalWaitEndsWithInterruptAndClose(t *testing.T) {
	type driverCase struct {
		name string
		// start opens a turn and sends one permission request. The returned func
		// answers whatever the child must answer for Interrupt to return.
		start func(t *testing.T, approve func(*DriverSessionConfig)) (DriverSession, func())
	}
	drivers := []driverCase{
		{
			name: "codex",
			start: func(t *testing.T, approve func(*DriverSessionConfig)) (DriverSession, func()) {
				peer := newCodexPeer(t, approve)
				if _, err := peer.answerHandshake("thread-1", map[string]any{"type": "apiKey"}, false); err != nil {
					t.Fatalf("handshake: %v", err)
				}
				peer.session.(*codexSession).setActiveTurn("turn-1", "inProgress")
				peer.requestFromServer("srv-wait", codexReqCommandApproval, map[string]any{
					"itemId": "i", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1",
				})
				answerInterrupt := func() {
					for {
						f := peer.next()
						if f["method"] == codexMethodTurnInterrupt {
							id, _ := f["id"].(float64)
							peer.reply(int64(id), map[string]any{})
							return
						}
					}
				}
				return peer.session, answerInterrupt
			},
		},
		{
			name: "opencode",
			start: func(t *testing.T, approve func(*DriverSessionConfig)) (DriverSession, func()) {
				peer := newOpenCodePeer(t, approve)
				if _, err := peer.answerHandshake("ses_wait"); err != nil {
					t.Fatalf("handshake: %v", err)
				}
				go func() { _, _ = peer.session.Input(context.Background(), "tool") }()
				_, _, _ = peer.nextRequest()
				peer.requestFromServer("srv-wait", acpReqRequestPermission, map[string]any{
					"sessionId": "ses_wait",
					"toolCall":  map[string]any{"toolCallId": "c1", "kind": "edit"},
					"options": []any{
						map[string]any{"optionId": "once", "kind": acpPermissionAllowOnce, "name": "Allow once"},
						map[string]any{"optionId": "reject", "kind": acpPermissionRejectOnce, "name": "Reject"},
					},
				})
				return peer.session, func() {}
			},
		},
		{
			name: "grok",
			start: func(t *testing.T, approve func(*DriverSessionConfig)) (DriverSession, func()) {
				peer := newGrokPeer(t, approve)
				if _, err := peer.answerHandshake("sess-wait", grokAllAuthMethods()); err != nil {
					t.Fatalf("handshake: %v", err)
				}
				if _, err := peer.session.Input(context.Background(), "start a turn"); err != nil {
					t.Fatalf("input: %v", err)
				}
				_, _, _ = peer.nextRequest()
				peer.requestFromServer("srv-wait", acpReqRequestPermission, map[string]any{
					"sessionId": "sess-wait",
					"toolCall":  map[string]any{"toolCallId": "call-wait", "kind": "execute"},
					"options": []any{
						map[string]any{"optionId": "allow-once", "kind": acpPermissionAllowOnce},
						map[string]any{"optionId": "reject-once", "kind": acpPermissionRejectOnce},
					},
				})
				return peer.session, func() {}
			},
		},
	}
	for _, d := range drivers {
		for _, end := range []string{"interrupt", "close"} {
			t.Run(d.name+"/"+end, func(t *testing.T) {
				entered := make(chan struct{}, 1)
				waitEnded := make(chan struct{})
				approve := func(cfg *DriverSessionConfig) {
					// Far beyond the test's patience: only the request's end may stop the wait.
					cfg.ApprovalDeadline = time.Hour
					cfg.Approve = func(ctx context.Context, _ ProviderApprovalRequest) (ProviderApprovalDecision, error) {
						entered <- struct{}{}
						defer close(waitEnded)
						<-ctx.Done()
						return ProviderApprovalDecision{}, ctx.Err()
					}
				}
				session, answerInterrupt := d.start(t, approve)
				t.Cleanup(func() { session.Close(nil) })
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("the permission request never reached the authority")
				}
				if end == "interrupt" {
					done := make(chan error, 1)
					go func() {
						_, err := session.Interrupt(context.Background())
						done <- err
					}()
					answerInterrupt()
					if err := <-done; err != nil {
						t.Fatalf("interrupt: %v", err)
					}
				} else {
					session.Close(nil)
				}
				select {
				case <-waitEnded:
				case <-time.After(3 * time.Second):
					t.Fatalf("after %s the authority still waits on the approval until its deadline", end)
				}
			})
		}
	}
}

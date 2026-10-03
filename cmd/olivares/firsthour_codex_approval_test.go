// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

// codexStub plays the Codex app-server side of one session opened on the
// product's Codex driver: it reads what the driver sends and delivers replies
// and server requests the way Codex does.
type codexStub struct {
	t       *testing.T
	frames  chan map[string]any
	session sessions.DriverSession
}

func newCodexStub(t *testing.T, approve func(context.Context, sessions.ProviderApprovalRequest) (sessions.ProviderApprovalDecision, error)) *codexStub {
	t.Helper()
	s := &codexStub{t: t, frames: make(chan map[string]any, 64)}
	s.session = firstHourCodexDriver().OpenSession(sessions.DriverSessionConfig{
		Send: func(_ context.Context, line []byte) error {
			var f map[string]any
			if err := json.Unmarshal(line, &f); err != nil {
				t.Errorf("the driver wrote a line that is not JSON: %s", line)
			}
			s.frames <- f
			return nil
		},
		Warn: func(string, ...any) {}, ClientName: "olivares", ClientVersion: "test",
		CallTimeout: 2 * time.Second, RunRef: "run-codex", ProfileRef: "ppf_codex",
		Approve: approve,
	})
	return s
}

func (s *codexStub) next() map[string]any {
	s.t.Helper()
	select {
	case f := <-s.frames:
		return f
	case <-time.After(3 * time.Second):
		s.t.Fatal("the driver sent nothing")
		return nil
	}
}

func (s *codexStub) request(want string) (float64, map[string]any) {
	s.t.Helper()
	for {
		f := s.next()
		if id, ok := f["id"].(float64); ok && f["method"] == want {
			params, _ := f["params"].(map[string]any)
			return id, params
		}
	}
}

func (s *codexStub) deliver(v any) {
	raw, _ := json.Marshal(v)
	s.session.Deliver(sessions.OutputFrame{Stream: "stdout", Data: raw})
}

func (s *codexStub) replyTo(server string) map[string]any {
	s.t.Helper()
	for {
		f := s.next()
		if f["id"] == server {
			r, _ := f["result"].(map[string]any)
			return r
		}
	}
}

// Root, 2026-10-01 (N1 J5 with a real Codex turn): the product's Codex sessions
// ran with approval "never", so Codex sent no approval requests and no Olivares
// policy could ask about or deny a command. The driver now starts every thread
// asking for approvals, inside the workspace sandbox, and each request reaches
// the session's approval authority, which answers it: allow approves, deny
// declines.
func TestFirstHourCodexAsksBeforeCommandsAndHonoursThePolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
		want  string
	}{
		{"policy allows: approved at once", true, "accept"},
		{"policy denies: declined", false, "decline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := make(chan sessions.ProviderApprovalRequest, 1)
			stub := newCodexStub(t, func(_ context.Context, req sessions.ProviderApprovalRequest) (sessions.ProviderApprovalDecision, error) {
				asked <- req
				return sessions.ProviderApprovalDecision{Allow: tc.allow}, nil
			})
			handshake := make(chan error, 1)
			go func() { _, err := stub.session.Handshake(context.Background()); handshake <- err }()
			id, _ := stub.request("initialize")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"userAgent": "stub", "codexHome": "/stub", "platformFamily": "unix", "platformOs": "linux"}})
			id, _ = stub.request("account/read")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}})
			id, params := stub.request("thread/start")
			if params["approvalPolicy"] != sessions.CodexApprovalUntrusted {
				t.Fatalf("thread/start approvalPolicy = %v, want %q: Codex must ask, so the policy can answer", params["approvalPolicy"], sessions.CodexApprovalUntrusted)
			}
			// Landlock alone never disables the native sandbox: only an actual
			// startup failure can admit that fallback at the runtime seam.
			want := sessions.CodexSandboxWorkspaceWrite
			if params["sandbox"] != want {
				t.Fatalf("thread/start sandbox = %v, want %q", params["sandbox"], want)
			}
			stub.deliver(map[string]any{"id": id, "result": map[string]any{
				"thread": map[string]any{"id": "thread-1", "parentThreadId": nil, "agentRole": nil},
				"model":  "gpt-5.6-sol", "modelProvider": "openai",
			}})
			if err := <-handshake; err != nil {
				t.Fatalf("handshake: %v", err)
			}

			go func() { _, _ = stub.session.Input(context.Background(), "list the files") }()
			id, _ = stub.request("turn/start")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}})
			deadline := time.Now().Add(3 * time.Second)
			for stub.session.ActiveTurn() != "turn-1" && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}

			stub.deliver(map[string]any{"id": "srv-1", "method": "item/commandExecution/requestApproval", "params": map[string]any{
				"itemId": "i1", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1", "command": "ls -la",
			}})
			select {
			case req := <-asked:
				if req.Driver != "codex" || req.TurnID != "turn-1" {
					t.Fatalf("authority asked about %+v", req)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the command approval never reached the session's approval authority")
			}
			if got := stub.replyTo("srv-1"); got["decision"] != tc.want {
				t.Fatalf("Codex was answered %v, want decision %q", got, tc.want)
			}
		})
	}
}

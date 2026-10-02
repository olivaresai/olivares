// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

type mcpLifecycleProcess struct {
	*approvalProjectionProcess
	inputs    atomic.Int32
	failInput atomic.Bool
}

func (p *mcpLifecycleProcess) Send(ctx context.Context, line []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var in struct {
		Type    string `json:"type"`
		ID      string `json:"request_id"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(line, &in) == nil && in.Type == "user" {
		if p.failInput.Load() {
			return errors.New("input not delivered")
		}
		p.inputs.Add(1)
	}
	if in.Type == "control_request" && in.Request.Subtype == "interrupt" {
		p.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q}}`, in.ID))}
	}
	return nil
}

type mcpLifecycleRunner struct{ launched func(*mcpLifecycleProcess) }

func (r mcpLifecycleRunner) Launch(context.Context, sessions.LaunchSpec) (sessions.Process, error) {
	p := &mcpLifecycleProcess{approvalProjectionProcess: &approvalProjectionProcess{output: make(chan sessions.OutputFrame, 16), done: make(chan struct{})}}
	p.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"system","subtype":"init","session_id":"pending-approval-provider"}`)}
	r.launched(p)
	return p, nil
}

// Interrupt and stop use the real session API while the original MCP HTTP call
// stays connected. A late human decision must never authorize abandoned work.
func TestManagedSessionMCPApprovalEndsWithWaitingTurn(t *testing.T) {
	for _, outcome := range []string{"interrupt", "stop", "turn_end", "queued_turn", "failed_input", "process_exit", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			limit := 6 * time.Second
			if outcome == "timeout" {
				limit = time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			req := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{"text":"exact input"}}}`, f.alias))).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+f.token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
			defer func() { cancel(); <-done }()
			ref := f.pendingApproval(ctx, done, w)
			f.waitForRunProjection(ctx, ref)
			switch outcome {
			case "interrupt", "stop":
				if code, body := f.h.req("POST", "/v1/m/sessions/runs/"+f.run+"/"+outcome, f.h.adminToken, f.h.tenantA, nil); code != http.StatusOK {
					t.Fatalf("%s = %d: %s", outcome, code, body)
				}
			case "failed_input":
				f.process.failInput.Store(true)
				line := `{"type":"user","message":{"role":"user","content":"undelivered next turn"}}`
				if code, body := f.h.req("POST", "/v1/m/sessions/runs/"+f.run+"/input", f.h.adminToken, f.h.tenantA, map[string]any{"line": line}); code != http.StatusBadRequest {
					t.Fatalf("failed input = %d: %s", code, body)
				}
				if f.process.inputs.Load() != 0 {
					t.Fatal("fixture delivered the failed input")
				}
				f.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","session_id":"pending-approval-provider"}`)}
			case "queued_turn":
				line := `{"type":"user","message":{"role":"user","content":"queued next turn"}}`
				if code, body := f.h.req("POST", "/v1/m/sessions/runs/"+f.run+"/input", f.h.adminToken, f.h.tenantA, map[string]any{"line": line}); code != http.StatusAccepted {
					t.Fatalf("queued input = %d: %s", code, body)
				}
				f.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","session_id":"pending-approval-provider"}`)}
			case "turn_end":
				f.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","session_id":"pending-approval-provider"}`)}
			case "process_exit":
				f.process.Stop(context.Background())
			case "timeout":
				// The client deadline expires while waiting; no decision has occurred.
				<-ctx.Done()
			}
			var detail struct {
				Status string `json:"status"`
			}
			until := time.Now().Add(time.Second)
			for {
				if code := f.h.reqInto("GET", "/v1/m/governance/approvals/"+ref, f.h.adminToken, f.h.tenantA, nil, &detail); code != http.StatusOK {
					t.Fatalf("approval detail = %d", code)
				}
				if detail.Status != "pending" || outcome == "interrupt" || outcome == "stop" || time.Now().After(until) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if detail.Status != "canceled" && (outcome != "timeout" || detail.Status != "expired") {
				t.Errorf("%s left approval %s, want canceled (or expired at the timeout)", outcome, detail.Status)
			}
			if code, body := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusConflict {
				t.Errorf("late approve = %d: %s, want 409", code, body)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("abandoned MCP call still waits")
				cancel()
				<-done
			}
			if f.calls.Load() != 0 {
				t.Fatalf("abandoned call ran %d times", f.calls.Load())
			}
			var response struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Error) == 0 {
				t.Fatalf("abandoned call did not return a refusal: %s", w.Body.String())
			}
			if outcome == "turn_end" || outcome == "failed_input" {
				// A fresh HTTP request is not a successor user turn.
				nextCtx, nextCancel := context.WithTimeout(t.Context(), time.Second)
				defer nextCancel()
				nextReq := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{"text":"exact input"}}}`, f.alias))).WithContext(nextCtx)
				nextReq.Header.Set("Authorization", "Bearer "+f.token)
				nextReq.Header.Set("Content-Type", "application/json")
				nextW := httptest.NewRecorder()
				f.management.ServeSessionHTTP(nextW, nextReq)
				var history struct {
					Items []json.RawMessage `json:"items"`
				}
				if code := f.h.reqInto("GET", "/v1/m/governance/approvals", f.h.adminToken, f.h.tenantA, nil, &history); code != http.StatusOK || len(history.Items) != 1 {
					t.Fatalf("post-result call created an approval: status %d, history %d", code, len(history.Items))
				}
				if f.calls.Load() != 0 || !strings.Contains(nextW.Body.String(), "turn has ended") {
					t.Fatalf("post-result call was not refused: %s", nextW.Body.String())
				}
			}
			if outcome == "interrupt" {
				// The same child can start another turn, with a fresh human decision.
				line := `{"type":"user","message":{"role":"user","content":"next turn"}}`
				type inputAnswer struct {
					code int
					body string
				}
				inputDone := make(chan inputAnswer, 1)
				go func() {
					code, body := f.h.req("POST", "/v1/m/sessions/runs/"+f.run+"/input", f.h.adminToken, f.h.tenantA, map[string]any{"line": line})
					inputDone <- inputAnswer{code, string(body)}
				}()
				// Claude can acknowledge interrupt before emitting its old result.
				// The next input must not cross until that result is consumed.
				time.Sleep(50 * time.Millisecond)
				if f.process.inputs.Load() != 0 {
					t.Error("successor input crossed before the interrupted turn's result")
				}
				f.process.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"result","subtype":"success","session_id":"pending-approval-provider"}`)}
				select {
				case answer := <-inputDone:
					if answer.code != http.StatusAccepted {
						t.Fatalf("next input = %d: %s", answer.code, answer.body)
					}
				case <-time.After(time.Second):
					t.Fatal("successor input did not resume after the old turn completed")
				}
			}
			if outcome == "interrupt" || outcome == "queued_turn" {
				nextCtx, nextCancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer nextCancel()
				nextReq := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{"text":"exact input"}}}`, f.alias))).WithContext(nextCtx)
				nextReq.Header.Set("Authorization", "Bearer "+f.token)
				nextReq.Header.Set("Content-Type", "application/json")
				nextW := httptest.NewRecorder()
				nextDone := make(chan struct{})
				go func() { defer close(nextDone); f.management.ServeSessionHTTP(nextW, nextReq) }()
				defer func() { nextCancel(); <-nextDone }()
				nextRef := f.pendingApproval(nextCtx, nextDone, nextW)
				if nextRef == ref {
					t.Fatal("successor turn reused the abandoned approval")
				}
				if code, body := f.h.req("POST", "/v1/m/governance/approvals/"+nextRef+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusOK {
					t.Fatalf("next approval = %d: %s", code, body)
				}
				select {
				case <-nextDone:
				case <-nextCtx.Done():
					t.Fatal("successor turn did not complete")
				}
				if nextW.Code != http.StatusOK || f.calls.Load() != 1 {
					t.Fatalf("successor turn = %d, calls %d: %s", nextW.Code, f.calls.Load(), nextW.Body.String())
				}
			}
		})
	}
}

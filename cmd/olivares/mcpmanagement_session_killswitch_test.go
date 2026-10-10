// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// The managed-session path is not wrapped by killSwitchUpstream (that wraps the
// gateway forwards). A stop reaches it through the session itself: the session
// credential's stop epoch (sessionhookcredentials.go) and the runtime StopGate in
// RuntimeCompanion, both checked on admission and again in
// managedSessionUpstream.check before every forward ("during-forward" engages
// the stop after admission, from the upstream's own catalogue read). Nothing
// else stops a new call; for an Ask tool, the stop also cancels a pending
// approval, so a call already waiting for a human ends too.
// Production wiring: sessionStopGate and the governance-backed credentials, as
// in boot.go. Every refusal must arrive before the test deadline, so an Ask call
// merely parked for a human never counts as refused.
func TestManagedSessionMCPKillSwitchRefusesToolCall(t *testing.T) {
	cases := []struct{ policy, when string }{
		{"allow", "before-first-call"}, {"allow", "after-live-call"}, {"allow", "during-forward"},
		{"ask", "before-first-call"}, {"ask", "after-live-call"}, {"ask", "while-awaiting-approval"},
	}
	for _, tc := range cases {
		t.Run(tc.policy+"/"+tc.when, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			f.h.set.sessions.StopGate = sessionStopGate{guard: f.h.set.gov, rec: newStopDenyRecorder(f.h.st, discardLog())}
			if tc.policy == "allow" {
				f.allowWithoutApproval()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if tc.when == "while-awaiting-approval" {
				f.assertStopEndsPendingCall(ctx)
				return
			}
			var ran int32
			if tc.when != "before-first-call" {
				// The cached server works before the stop: the catalogue for both
				// policies, and the call itself when it needs no approval.
				if w := f.serve(ctx, 1, "tools/list", `{}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), f.alias) {
					t.Fatalf("pre-stop tools/list = %d %s", w.Code, w.Body.String())
				}
				if tc.policy == "allow" {
					if w := f.serve(ctx, 2, "tools/call", f.callParams()); !strings.Contains(w.Body.String(), "exact input") || f.calls.Load() != 1 {
						t.Fatalf("pre-stop tools/call = %d %s", w.Code, w.Body.String())
					}
					ran = 1
				}
			}
			if tc.when == "during-forward" {
				// Admitted with no stop; managedSessionUpstream.Forward re-reads the
				// catalogue before it forwards, and the stop lands during that read.
				engage := func() {
					if err := f.engageEstateStop(); err != nil {
						t.Error(err)
					}
				}
				f.onUpstreamList.Store(&engage)
			} else if err := f.engageEstateStop(); err != nil {
				t.Fatal(err)
			}
			f.assertRefused(ctx, f.serve(ctx, 3, "tools/call", f.callParams()), ran)
			if tc.when == "during-forward" && f.onUpstreamList.Load() != nil {
				t.Fatal("the stop was never engaged during the forward")
			}
		})
	}
}

// assertStopEndsPendingCall admits an Ask call before the stop: the stop ends the
// waiting call and its approval, and a late approval cannot revive it.
func (f *managedMCPApprovalFixture) assertStopEndsPendingCall(ctx context.Context) {
	f.t.Helper()
	w := httptest.NewRecorder()
	done := make(chan struct{})
	callCtx, cancelCall := context.WithCancel(ctx)
	req := f.request(callCtx, 1, "tools/call", f.callParams())
	go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
	defer func() { cancelCall(); <-done }()
	ref := f.pendingApproval(ctx, done, w)
	if err := f.engageEstateStop(); err != nil {
		f.t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		f.t.Fatal("a call awaiting approval survived the stop")
	}
	if code, body := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusConflict {
		f.t.Fatalf("late approval after the stop = %d %s, want 409", code, body)
	}
	f.assertRefused(ctx, w, 0)
}

// allowWithoutApproval makes the tested tool an administrator Allow, the policy
// whose calls reach the upstream without a human decision.
func (f *managedMCPApprovalFixture) allowWithoutApproval() {
	f.t.Helper()
	actor, err := f.h.authr.Authenticate(f.t.Context(), f.h.adminToken)
	if err != nil {
		f.t.Fatal(err)
	}
	snapshot, err := f.management.store.Get(f.t.Context(), f.tenant)
	if err != nil {
		f.t.Fatal(err)
	}
	row := snapshot.Servers[0]
	in := row.MCPGatewayServerInput
	in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "write_echo", RequiredScope: "tools:call"}}
	if _, err := f.management.PutServer(f.t.Context(), actor, f.tenant, snapshot.Version, row.ID, in); err != nil {
		f.t.Fatal(err)
	}
}

// engageEstateStop uses the production kill-switch API. It returns an error
// instead of failing so the upstream handler goroutine can call it.
func (f *managedMCPApprovalFixture) engageEstateStop() error {
	var stop struct {
		Status string `json:"status"`
	}
	if code := f.h.reqInto("POST", "/v1/m/governance/killswitch", f.h.adminToken, f.h.tenantA, map[string]any{"scope_kind": "estate", "reason": "managed-session MCP proof"}, &stop); code != http.StatusCreated || stop.Status != "active" {
		return fmt.Errorf("engage kill switch = %d %+v", code, stop)
	}
	return nil
}

func (f *managedMCPApprovalFixture) callParams() string {
	return fmt.Sprintf(`{"name":%q,"arguments":{"text":"exact input"}}`, f.alias)
}

func (f *managedMCPApprovalFixture) request(ctx context.Context, id int, method, params string) *http.Request {
	req := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (f *managedMCPApprovalFixture) serve(ctx context.Context, id int, method, params string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.management.ServeSessionHTTP(w, f.request(ctx, id, method, params))
	return w
}

// assertRefused requires a refusal that the stop caused: it arrived before the
// test deadline, the upstream tool ran no more than the ran calls made before
// the stop, and the answer is a 401/403 admission denial or a JSON-RPC error
// (a forward blocked after admission answers 502 with one).
func (f *managedMCPApprovalFixture) assertRefused(ctx context.Context, w *httptest.ResponseRecorder, ran int32) {
	f.t.Helper()
	if ctx.Err() != nil {
		f.t.Fatalf("the call ended at the test deadline, not at the stop: %d %s", w.Code, w.Body.String())
	}
	if got := f.calls.Load(); got != ran {
		f.t.Fatalf("managed-session tool ran under an active kill switch (%d upstream calls, %d before the stop)", got, ran)
	}
	if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		return
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Error) == 0 || len(response.Result) != 0 {
		f.t.Fatalf("managed-session tool call under a stop was not refused: %d %s", w.Code, w.Body.String())
	}
}

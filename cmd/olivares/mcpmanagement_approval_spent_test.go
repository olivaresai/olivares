// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// The gate contract (connectors/mcp/gate.go) requires an approved decision that
// spent the human approval to report Spent; the round trip refuses one without it.
func TestManagedSessionApprovalGateReportsSpentApproval(t *testing.T) {
	f := newManagedMCPApprovalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p, err := f.management.sessionAuthenticator.Authenticate(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	gate := managedSessionApprovalGate{m: f.management, tenant: f.tenant, principal: p, serverID: "server", serverName: "Approval HTTPS", check: func(context.Context) error { return nil }}
	type result struct {
		decision mcpc.GateDecision
		err      error
	}
	answered := make(chan result, 1)
	go func() {
		decision, err := gate.Authorize(ctx, mcpc.ToolApprovalRequest{
			Tenant: f.tenant.String(), Subject: p.SessionIdentity, RequestedBy: p.SessionIdentity,
			Tool: "write_echo", PlanHash: "plan", Arguments: json.RawMessage(`{"text":"exact input"}`), ConsumerID: "round-trip",
		})
		answered <- result{decision, err}
	}()
	ref := f.pendingApproval(ctx, make(chan struct{}), httptest.NewRecorder())
	f.waitForRunProjection(ctx, ref)
	if code, _ := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": "approve"}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("human decision = %d", code)
	}
	var got result
	select {
	case got = <-answered:
	case <-ctx.Done():
		t.Fatal("gate did not return after the human decision")
	}
	if got.err != nil || !got.decision.Allowed() || got.decision.ApprovalRef != ref || got.decision.PlanHash != "plan" {
		t.Fatalf("decision = %+v, %v", got.decision, got.err)
	}
	if !got.decision.Spent {
		t.Fatal("approved decision consumed the approval but did not report Spent")
	}
	again, err := f.management.eng.engineApprovals.Consume(ctx, f.tenant, ref, "another-consumer", "mcp-toolcall-v1")
	if err != nil || again.Granted || !again.Replay || again.ConsumedBy == "" {
		t.Fatalf("the approval the gate reported spent was not spent: %+v, %v", again, err)
	}
}

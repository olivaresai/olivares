// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
)

// A stored tool-use review asks before acting. Post and lifecycle hooks still
// evaluate their policies and audit, but must not open another tool-use approval.
func TestSessionClaudeApprovalOnlyQueuesPermissionGates(t *testing.T) {
	for _, event := range []string{"PostToolUse", "PostToolUseFailure", "UserPromptSubmit"} {
		t.Run(event, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "review-event-"+event)
			token, err := c.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			createSessionReviewPolicy(t, h, hookActionCapability, "claude.tool")
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: c, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, stops: h.set.gov, stopRec: newStopDenyRecorder(h.st, discardLog()), store: h.st, clock: time.Now, log: discardLog()}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			out, err := d.Decide(ctx, claude.HookDecisionInput{Event: event, Tool: "Write", ResourceKind: "file", ResourceRef: "/tmp/proof", Mode: "write", PlanHash: hexSHA("review-event")}, token)
			if err != nil || out.Permission != "allow" {
				t.Fatalf("%s must finish without a tool-use review: %+v err=%v", event, out, err)
			}
			items, _, err := service.List(context.Background(), tenant, hookActionCapability, "", "")
			if err != nil || len(items) != 0 {
				t.Fatalf("%s opened a tool-use approval: %d requests err=%v", event, len(items), err)
			}
			found := false
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				if ev.meta["event"] == event && ev.meta["session_ref"] == intent.ClaimSID {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s lost its session audit", event)
			}
		})
	}
}

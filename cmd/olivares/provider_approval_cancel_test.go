// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestProviderCanceledWaitClosesItsSessionRequest(t *testing.T) {
	for _, scenario := range []string{"interrupt", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "provider-cancel-"+scenario)
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			bridge := newApprovalBridge(approvalBridgeConfig{}, discardLog())
			bridge.localProposer = service
			limit := 3 * time.Second
			if scenario == "deadline" {
				limit = 350 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			var answer sessions.ProviderApprovalDecision
			var answerErr error
			done := make(chan struct{})
			go func() {
				defer close(done)
				answer, answerErr = (providerApprovalAdapter{bridge: bridge}).Approve(ctx, tenant, sessions.ProviderApprovalRequest{
					Driver: "codex", RunRef: intent.RunRef, Principal: principal, SessionRef: scope.SessionRef,
					TurnID: "turn-cancel", Method: "item/commandExecution/requestApproval", Kind: "command_execution",
					CommandLine: "printf cancellation-proof", FactsComplete: true,
				})
			}()
			defer func() { cancel(); <-done }()
			var pending governance.Approval
			for pending.ID == "" {
				items, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					t.Fatalf("provider ended before human review: %+v %v", answer, answerErr)
				case <-ctx.Done():
					t.Fatal("request did not reach the shared queue")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if pending.RequestedBy != "session:"+scope.SessionRef || pending.SessionRef != scope.SessionRef {
				t.Fatal("request did not name the resolved session")
			}
			if scenario == "interrupt" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled provider wait did not finish")
			}
			if answer.Allow || !errors.Is(answerErr, ctx.Err()) {
				t.Fatalf("canceled wait widened or lost its cause: %+v %v", answer, answerErr)
			}
			wantStatus, wantAudit := "canceled", "governance.approval.cancel"
			if scenario == "deadline" {
				wantStatus, wantAudit = "expired", "governance.approval.expire"
			}
			// A deadline is expiry; interruption remains exact-requester cancellation.
			closed, err := service.Read(t.Context(), tenant, pending.ID)
			if err != nil || closed.Status != wantStatus {
				t.Fatalf("orphaned request: status=%q err=%v", closed.Status, err)
			}
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.approval")
				if err != nil {
					return err
				}
				rec, err := repo.Get(t.Context(), model.ID(pending.ID))
				if err != nil {
					return err
				}
				if rec.String("status") != wantStatus {
					t.Fatalf("stored terminal status=%q, want%q", rec.String("status"), wantStatus)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			items, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
			if err != nil || len(items) != 0 {
				t.Fatalf("pending requests=%d err=%v", len(items), err)
			}
			cancellations := 0
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				if ev.event.Action == wantAudit && ev.event.TargetID == model.ID(pending.ID) {
					if scenario == "deadline" {
						reason, _ := ev.meta["reason"].(string)
						if !strings.Contains(reason, "deadline") || ev.meta["decision"] != "deny" {
							t.Fatal("stored expiry lost its denial reason")
						}
					}
					if scenario == "interrupt" && (ev.event.Actor != "session:"+scope.SessionRef || ev.event.ActorKind != model.ActorAgent) {
						t.Fatal("cancel lost its exact session attribution")
					}
					cancellations++
				}
			}
			if cancellations != 1 {
				t.Fatalf("cancel anchors=%d, want=1", cancellations)
			}
		})
	}
}

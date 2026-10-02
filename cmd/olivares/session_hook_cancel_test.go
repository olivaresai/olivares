// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestSessionClaudeCanceledWaitClosesRequestAndAnchorsDeny(t *testing.T) {
	for _, scenario := range []string{"interrupt", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "cancel-"+scenario)
			intent.PermissionMode = "default"
			token, err := credentials.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			var logs bytes.Buffer
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: slog.New(slog.NewTextHandler(&logs, nil))}
			limit := 3 * time.Second
			if scenario == "deadline" {
				limit = 350 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "printf cancelled-proof"}})
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
			defer func() { cancel(); <-done }()
			var pending governance.Approval
			for pending.ID == "" {
				items, _, err := service.List(t.Context(), tenant, hookActionCapability, "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					t.Fatalf("hook did not wait for review: %s", rec.Body.String())
				case <-ctx.Done():
					t.Fatal("request never reached the queue")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if pending.SessionRef != intent.ClaimSID || pending.RequestedBy != "session:"+intent.ClaimSID {
				t.Fatal("wrong session requested review")
			}
			if scenario == "interrupt" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled wait did not complete")
			}
			var reply struct {
				HookSpecificOutput struct {
					Decision string `json:"permissionDecision"`
					Reason   string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.HookSpecificOutput.Decision != "deny" {
				t.Fatalf("cancel did not produce explicit deny: %d %s", rec.Code, rec.Body.String())
			}
			reason := reply.HookSpecificOutput.Reason
			if scenario == "interrupt" {
				if !strings.Contains(strings.ToLower(reason), "interrupt") || strings.Contains(reason, "deadline") {
					t.Fatalf("interrupt mislabeled: %q", reason)
				}
			} else if !strings.Contains(reason, "deadline") {
				t.Fatalf("deadline mislabeled: %q", reason)
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
			items, _, err := service.List(t.Context(), tenant, hookActionCapability, "pending", "")
			if err != nil || len(items) != 0 {
				t.Fatalf("pending requests=%d err=%v", len(items), err)
			}
			cancelEvents, denyEvents := 0, 0
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				if ev.event.Action == wantAudit && ev.event.TargetID == model.ID(pending.ID) {
					if scenario == "deadline" {
						reason, _ := ev.meta["reason"].(string)
						if !strings.Contains(reason, "deadline") || ev.meta["decision"] != "deny" {
							t.Fatal("stored expiry lost its denial reason")
						}
					}
					cancelEvents++
				}
				if ev.event.Action == "hook.tool.deny" && ev.meta["run_ref"] == intent.RunRef {
					if ev.event.Actor != "session:"+intent.ClaimSID || ev.meta["session_ref"] != intent.ClaimSID {
						t.Fatal("deny lost session attribution")
					}
					denyEvents++
				}
			}
			if cancelEvents != 1 || denyEvents != 1 {
				t.Fatalf("cancel/deny anchors=%d/%d, want 1/1", cancelEvents, denyEvents)
			}
			if strings.Contains(logs.String(), "evidence gap") {
				t.Fatalf("cancel caused an audit gap: %s", logs.String())
			}
		})
	}
}

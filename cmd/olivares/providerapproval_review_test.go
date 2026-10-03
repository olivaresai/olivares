// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/redact"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The actual requester and REST readers must agree on the independently proven
// masked command/paths. No vendor process or account is involved in this test.
func TestProviderApprovalPublishesStructuredReview(t *testing.T) {
	const value = "N1reviewLiteralNoPattern0123456789"
	for _, driver := range []string{"codex", "grok", "opencode"} {
		for _, surface := range []string{"command", "files", "options"} {
			t.Run(driver+"/"+surface, func(t *testing.T) {
				h := newHarness(t)
				tenant := model.TenantID(h.tenantA)
				human, err := h.authr.Authenticate(t.Context(), h.adminToken)
				if err != nil {
					t.Fatal(err)
				}
				credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
				intent := claimHookTestSession(t, h, human, tenant, "structured-"+driver+"-"+surface)
				intent.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/review"}}
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
				policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, redactSecrets: providerSecretTestMask(value, "env/review")}
				req := sessions.ProviderApprovalRequest{Driver: driver, RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal, TurnID: "turn-structured", Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "PASSWORD=" + value + " printf visible", FactsComplete: true, Requested: []string{"allow-once"}}
				wantTool, wantText := "Command", "PASSWORD=[secret env/review] printf visible"
				switch surface {
				case "files":
					req.Kind, req.Method, req.CommandLine = "file_change", "item/fileChange/requestApproval", ""
					req.FilePaths = []string{"/project/" + value + "/one.txt", "/project/" + value + "/two.txt"}
					wantTool, wantText = "File change", "/project/[secret env/review]/one.txt\n/project/[secret env/review]/two.txt"
				case "options":
					// ACP currently forwards offered option IDs without tool-input
					// facts. Show only that known scope; never invent a command.
					req.Kind, req.Method, req.CommandLine = "tool_call_permission", "session/request_permission", ""
					req.Requested = []string{"allow-once", "deny-once"}
					wantTool, wantText = "Provider permission", "allow-once\ndeny-once"
				}
				original, err := json.Marshal(req)
				if err != nil {
					t.Fatal(err)
				}
				bridge := newApprovalBridge(approvalBridgeConfig{}, slog.Default())
				bridge.localProposer = service
				adapter := providerApprovalAdapter{bridge: bridge, reviewFacts: policy.reviewFacts, reviewReason: policy.reviewReason}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				done := make(chan struct{})
				var answer sessions.ProviderApprovalDecision
				var answerErr error
				go func() {
					defer close(done)
					answer, answerErr = adapter.Approve(ctx, tenant, req)
				}()
				defer func() { cancel(); <-done }()
				var ref string
				for ref == "" {
					items, _, err := service.List(ctx, tenant, "sessions.provider.approval", "pending", "")
					if err != nil {
						t.Fatal(err)
					}
					if len(items) != 0 {
						ref = items[0].ID
						if items[0].Review == nil || items[0].Review.Tool != wantTool || items[0].Review.Text != wantText {
							t.Fatal("queue lacks the exact proven structured provider review")
						}
						break
					}
					select {
					case <-done:
						t.Fatalf("provider did not queue: reason=%q error=%v", answer.Reason, answerErr)
					case <-ctx.Done():
						t.Fatal("provider review did not appear")
					case <-time.After(10 * time.Millisecond):
					}
				}
				var detail struct {
					Review *governance.ApprovalReview `json:"review"`
				}
				if code := h.reqInto(http.MethodGet, "/v1/m/governance/approvals/"+ref, h.adminToken, h.tenantA, nil, &detail); code != http.StatusOK || detail.Review == nil || detail.Review.Tool != wantTool || detail.Review.Text != wantText {
					t.Fatal("approval detail changed the proven review")
				}
				var list struct {
					Items []struct {
						ID     string                     `json:"id"`
						Review *governance.ApprovalReview `json:"review"`
					} `json:"items"`
				}
				if code := h.reqInto(http.MethodGet, "/v1/m/governance/approvals?status=pending", h.adminToken, h.tenantA, nil, &list); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != ref || list.Items[0].Review == nil || list.Items[0].Review.Text != wantText {
					t.Fatal("approval list changed the proven review")
				}
				if code, _ := h.decide(t, h.adminToken, ref, "approve"); code != http.StatusOK {
					t.Fatal("one administrator could not approve the native request")
				}
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("native review did not finish")
				}
				if answerErr != nil || !answer.Allow || strings.Join(answer.Granted, "\n") != strings.Join(req.Requested, "\n") {
					t.Fatal("structured display changed the approval's authority")
				}
				current, _ := json.Marshal(req)
				if string(current) != string(original) {
					t.Fatal("structured display changed caller input")
				}
				for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
					data, _ := json.Marshal(ev.meta)
					if strings.Contains(string(data), value) {
						t.Error("structured review exposed a synthetic secret in audit")
					}
				}
			})
		}
	}
}

func TestProviderStructuredReviewRefusesBeforeQueueOnChangedDisplayOrInvalidProvenance(t *testing.T) {
	const value = "N1structuredGuardNoPattern0123456789"
	for _, failure := range []string{"changed-display", "invalid-provenance"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "structured-refusal-"+failure)
			intent.SecretEnv = []sessions.SecretEnvRef{{Env: "EXACT_KEY", Secret: "env/review"}}
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, redactSecrets: providerSecretTestMask(value, "env/review")}
			command := "PASSWORD=" + value + " printf visible"
			mask := func(ctx context.Context, tenant model.TenantID, req sessions.ProviderApprovalRequest, text string) (string, []redact.GeneratedMaskSpan, error) {
				masked, spans, err := policy.reviewReason(ctx, tenant, req, text)
				if text == command && err == nil {
					if failure == "changed-display" {
						masked = strings.Replace(masked, "visible", "changed", 1)
					} else {
						spans = []redact.GeneratedMaskSpan{{Start: 0, End: len(masked)}}
					}
				}
				return masked, spans, err
			}
			service := h.set.gov.EngineApprovals()
			bridge := newApprovalBridge(approvalBridgeConfig{}, slog.Default())
			bridge.localProposer = service
			adapter := providerApprovalAdapter{bridge: bridge, reviewFacts: policy.reviewFacts, reviewReason: mask}
			before := len(canonicalLedgerEventsFrom(t, h.st, tenant, 0))
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			out, err := adapter.Approve(ctx, tenant, sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal, Kind: "command_execution", Method: "item/commandExecution/requestApproval", CommandLine: command, FactsComplete: true})
			if err != nil || out.Allow || out.Reason != "permission scope is not reviewable" {
				t.Error("invalid structured display was not refused before human review")
			}
			items, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "", "")
			if err != nil || len(items) != 0 {
				t.Error("invalid structured display created a queue effect")
			}
			if after := len(canonicalLedgerEventsFrom(t, h.st, tenant, 0)); after != before {
				t.Error("invalid structured display created an audit effect")
			}
		})
	}
}

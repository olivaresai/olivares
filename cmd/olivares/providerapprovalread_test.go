// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// #2890: an OpenCode read goes through the live session policy like any other tool call.
// A read or search that names its files is a read (opencode.tool.use:read), as Claude
// Code's reads are: every preset allows it without a person, read-only included, and it
// is audited as allowed. One that names no file (a web search) is not a read, and an edit
// is never one.
func TestProviderSessionOpenCodeReadsAreReadsForEveryPreset(t *testing.T) {
	for _, preset := range []struct{ name, mode string }{
		{"read-only", "plan"}, {"ask", "default"}, {"edits-only", "acceptEdits"},
	} {
		for _, kind := range []string{"read", "search"} {
			t.Run(preset.name+"/"+kind, func(t *testing.T) {
				h := newHarness(t)
				tenant := model.TenantID(h.tenantA)
				human, err := h.authr.Authenticate(t.Context(), h.adminToken)
				if err != nil {
					t.Fatal(err)
				}
				credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
				intent := claimHookTestSession(t, h, human, tenant, "opencode-read-"+preset.name+"-"+kind)
				intent.PermissionMode = preset.mode
				if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
					t.Fatal(err)
				}
				principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
				if err != nil {
					t.Fatal(err)
				}
				policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h),
					scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
				request := sessions.ProviderApprovalRequest{
					Driver: "opencode", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal,
					TurnID: "read-turn", Kind: "tool_call_permission", Method: "session/request_permission", Requested: []string{"once"},
					ToolKind: kind, FilePaths: []string{"README.md"}, FactsComplete: true,
				}
				decision, err := policy.Decide(t.Context(), tenant, request)
				if err != nil || decision.Disposition != sessions.ProviderApprovalAllow {
					t.Fatalf("an OpenCode %s under the %s preset = %+v %v, want allowed as a read", kind, preset.name, decision, err)
				}
				audited := false
				for _, event := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
					audited = audited || event.event.Action == "opencode.approval.allow"
				}
				if !audited {
					t.Fatal("the read is not in the audit")
				}
				request.FilePaths, request.FactsComplete = nil, false
				if decision, _ := policy.Decide(t.Context(), tenant, request); decision.Disposition == sessions.ProviderApprovalAllow {
					t.Fatalf("a %s that names no file under the %s preset was allowed like a read: %+v", kind, preset.name, decision)
				}
				// A read that also carries a command is not a read (review finding 3).
				request.FilePaths, request.FactsComplete, request.CommandLine = []string{"README.md"}, true, "rm -rf ."
				if decision, _ := policy.Decide(t.Context(), tenant, request); decision.Disposition == sessions.ProviderApprovalAllow && preset.mode == "plan" {
					t.Fatalf("a %s carrying a command under the read-only preset was allowed like a read: %+v", kind, decision)
				}
				request.CommandLine = ""
				request.ToolKind, request.FilePaths, request.FactsComplete = "edit", []string{"README.md"}, true
				if decision, _ := policy.Decide(t.Context(), tenant, request); decision.Disposition == sessions.ProviderApprovalAllow {
					t.Fatalf("an edit under the %s preset was allowed like a read: %+v", preset.name, decision)
				}
			})
		}
	}
}

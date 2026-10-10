// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSR2ProviderFullRequiresCurrentRunAdministratorAuthorization(t *testing.T) {
	for _, driver := range []string{"codex", "grok", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "sr2-full-"+driver)
			intent.PermissionMode = "bypassPermissions"
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil || scope.Preset != sessions.PresetFull {
				t.Fatalf("full launch binding absent: %+v %v", scope, err)
			}
			policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h), scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
			req := sessions.ProviderApprovalRequest{Driver: driver, RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal, TurnID: "full-turn", Kind: "tool_call_permission", Method: "session/request_permission", Requested: []string{"allow-once"}}
			if driver == "codex" {
				req.Kind, req.Method = "command_execution", "item/commandExecution/requestApproval"
				req.CommandLine, req.FactsComplete = "printf sr2-full-proof", true
			}
			initial, err := policy.Decide(t.Context(), tenant, req)
			if err != nil || initial.Disposition != sessions.ProviderApprovalAllow {
				t.Fatalf("initial full action not allowed: %+v %v", initial, err)
			}
			code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "sr2-forbid-current-full", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "sessions:run:admin"}}}})
			if code != 201 {
				t.Fatalf("publish current administration forbid: %d %s", code, raw)
			}
			current, err := h.set.gov.Evaluator().Evaluate(t.Context(), auth.Request{Principal: human, Tenant: tenant, Permission: "sessions:run:admin", Resource: auth.ResourceAttrs{Kind: "session_run", ID: intent.RunRef, WorkspaceID: scope.WorkspaceID}})
			if err != nil || current.Allow {
				t.Fatalf("control: current administration was not forbidden: %+v %v", current, err)
			}
			after, err := policy.Decide(t.Context(), tenant, req)
			if err != nil || after.Disposition != sessions.ProviderApprovalDeny {
				t.Fatalf("Full continued after current administration was forbidden: %+v %v", after, err)
			}
		})
	}
}

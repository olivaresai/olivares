// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A caller cannot turn a deployment, hook or forged provider request into a
// standing grant by supplying the provider action's name or a decision scope.
func TestApprovalSessionScopeRefusesOtherRequestsWithoutRecording(t *testing.T) {
	for _, action := range []string{"deploy", "claude.hook.tool", "sessions.run.launch", "sessions.provider.approval"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "scope-refusal")
			_, editor := h.roleUser(admin, tenant, "requester@x.io", "editor")
			r := h.createApproval(editor, tenant, map[string]any{"action": action, "subject_kind": "session_run", "subject_ref": "run-one"})
			if r.code != http.StatusCreated {
				t.Fatalf("request = %d %s", r.code, r.raw)
			}
			id := r.body["id"].(string)
			decision := h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "scope": "session"}, tenantHdr(tenant))
			if decision.code != http.StatusBadRequest {
				t.Fatalf("session scope on %s = %d %s, want 400", action, decision.code, decision.raw)
			}
			trail := h.do("GET", govPath+"/approvals/"+id+"/decisions", admin, nil, tenantHdr(tenant))
			if rows, _ := trail.body["items"].([]any); len(rows) != 0 {
				t.Fatalf("refused scope recorded decisions: %s", trail.raw)
			}
			if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
				return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
					if e.Action == "governance.approval.decision" && e.TargetID.String() == id {
						t.Error("refused scope recorded a decision in the ledger")
					}
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

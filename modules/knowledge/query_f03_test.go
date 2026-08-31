// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// refAwareGuard models the REAL governance retrieval guard: it resolves clearance
// from the AGENT identity, granting secret clearance only to the named privileged
// agent and denying everyone else. (The production guard, governanceRetrievalGuard,
// looks up the agent row by external_id == agentRef and returns its clearance —
// discarding the actor entirely.)
type refAwareGuard struct{ privileged string }

func (g refAwareGuard) Resolve(_ context.Context, _ model.TenantID, _, agentRef, _ string) (Grants, error) {
	if agentRef == g.privileged {
		return Grants{Allowed: true, Clearance: classSecret}, nil
	}
	return Grants{Allowed: false, Reason: "agent not permitted to read this knowledge base"}, nil
}

// TestF03HumanCannotBorrowAgentClearanceViaBodyAgentRef is the F-03 red repro at
// the real attack surface: the REST route POST /v1/m/knowledge/kbs/{id}/query.
//
// A human editor (no authenticated agent identity) posts the body agent_ref of a
// privileged, secret-clearance agent. Before the handler forwarded that body value
// into Query, which handed it to the guard as the effective identity — so the human
// borrowed the privileged agent's clearance just by naming it (HTTP 200). The body
// agent_ref is now `json:"-"` and Query derives the agent solely from the authenticated
// principal, so the effective agent is empty and the guard denies: HTTP 403.
func TestF03HumanCannotBorrowAgentClearanceViaBodyAgentRef(t *testing.T) {
	const privilegedAgent = "clearance-secret-agent"

	h := newHarnessWith(t, WithRetrievalGuard(refAwareGuard{privileged: privilegedAgent}))
	token := h.adminLogin()
	tenant := h.createOrg(token, "f03http")
	editor := h.roleToken(token, tenant, "human@f03http.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", editor, map[string]any{"name": "f03-http-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	// The human forges the privileged agent's ref in the request body.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{
		"query":     "confidential",
		"agent_ref": privilegedAgent,
	}, hdr)

	if r.code != http.StatusForbidden {
		t.Fatalf("F-03: a human borrowed the privileged agent's clearance via body agent_ref; "+
			"want 403 Forbidden, got %d %s", r.code, r.raw)
	}
}

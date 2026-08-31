// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// fakeScopeGate is a test RetrievalScopeGate: it allows or denies by flag, or errors
// (to exercise the deny-closed posture).
type fakeScopeGate struct {
	allow bool
	err   bool
}

func (g fakeScopeGate) Allowed(_ context.Context, _ model.TenantID, _ auth.Principal, _, _ string) (bool, string, error) {
	if g.err {
		return false, "", errors.New("scope state unreadable")
	}
	return g.allow, "agent out of the knowledge base's scope", nil
}

// TestRetrievalScopeGateDenies: an out-of-scope agent's retrieval is refused (403)
// even when the RetrievalGuard would otherwise permit it — scope is an orthogonal,
// deny-closed axis.
func TestRetrievalScopeGateDenies(t *testing.T) {
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithRetrievalScopeGate(fakeScopeGate{allow: false}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor,
		map[string]any{"query": "anything", "agent_ref": "out-of-scope-bot"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("out-of-scope retrieval must be 403, got %d %s", r.code, r.raw)
	}
}

// TestRetrievalScopeGateErrorIsDenyClosed: a scope-gate error denies the retrieval
// (fail closed) rather than degrading to an allow.
func TestRetrievalScopeGateErrorIsDenyClosed(t *testing.T) {
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithRetrievalScopeGate(fakeScopeGate{err: true}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor,
		map[string]any{"query": "anything", "agent_ref": "bot"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("scope-gate error must be deny-closed (403), got %d %s", r.code, r.raw)
	}
}

// TestRetrievalScopeGateAllowsInScope: an in-scope agent is NOT denied by the scope
// gate (the retrieval proceeds to a normal 200).
func TestRetrievalScopeGateAllowsInScope(t *testing.T) {
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithRetrievalScopeGate(fakeScopeGate{allow: true}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor,
		map[string]any{"query": "anything", "agent_ref": "in-scope-bot"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("in-scope retrieval must succeed (200), got %d %s", r.code, r.raw)
	}
}

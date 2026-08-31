// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// docIDsByTitle returns a title→docID map for the documents ingested into kbID.
func docIDsByTitle(t *testing.T, h *harness, tok string, tenant model.TenantID, kbID string) map[string]string {
	t.Helper()
	hdr := tenantHdr(tenant)
	if r := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", tok, nil, hdr); r.code == http.StatusOK {
		out := map[string]string{}
		for _, it := range r.body["items"].([]any) {
			m := it.(map[string]any)
			out[m["title"].(string)] = m["id"].(string)
		}
		return out
	} else {
		t.Fatalf("list docs = %d %s", r.code, r.raw)
		return nil
	}
}

// TestF05FetchDocumentEnforcesDocumentACL is the F-05 red repro: FetchDocument must apply
// the SAME per-document ACL predicate as retrieval (retrieval.go loadCandidates), not only
// the KB-level discoverGate. An agent that holds KB clearance + scope but is EXCLUDED by a
// document's ACL could otherwise read that document's title/ACL/classification/hash —
// metadata recon of content it can never retrieve. Denial is indistinguishable from
// not-found.
func TestF05FetchDocumentEnforcesDocumentACL(t *testing.T) {
	var allowedKB string
	h := newHarnessWith(t,
		// KB clearance + scope, but the identity's groups do NOT include "security-team".
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret, Groups: []string{"other-team"}}}),
		WithRetrievalScopeGate(kbAllowlistScopeGate{allow: &allowedKB}),
	)
	token := h.adminLogin()
	tenant := h.createOrg(token, "f05acl")
	tok := h.roleToken(token, tenant, "u@f05.io", "editor")
	hdr := tenantHdr(tenant)

	kbID := h.mustKB(tok, tenant, map[string]any{"name": "f05-kb"})
	allowedKB = kbID

	src := newFakeSource([]contentsource.Document{
		{DocID: "d-open", Title: "Open Doc", Body: "public body"},
		{DocID: "d-acl", Title: "ACL Doc", Body: "restricted body", ACL: []string{"security-team"}},
	})
	h.addSource("f05-src", src)
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", tok, map[string]any{"source": "f05-src"}, hdr); r.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	byTitle := docIDsByTitle(t, h, tok, tenant, kbID)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant).WithAgentIdentity("agent-x"),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	// Control: the unrestricted document remains fetchable (the gate must not over-deny).
	if _, err := h.module().FetchDocument(context.Background(), mc, byTitle["Open Doc"]); err != nil {
		t.Fatalf("unrestricted document must stay fetchable, got %v", err)
	}
	// The ACL'd document must be NotFound for an agent whose groups do not intersect its ACL.
	if _, err := h.module().FetchDocument(context.Background(), mc, byTitle["ACL Doc"]); err == nil {
		t.Error("F-05: FetchDocument leaked metadata for a document the agent's ACL excludes")
	} else if qe, ok := IsQueryError(err); !ok || qe.Kind != QueryErrNotFound {
		t.Errorf("FetchDocument error = %v, want QueryErrNotFound", err)
	}

	// And an agent WHOSE groups DO intersect the ACL may fetch it (parity with retrieval).
	inGroup := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant).WithAgentIdentity("agent-x"),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	h.mod.guard = fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret, Groups: []string{"security-team"}}}
	if _, err := h.module().FetchDocument(context.Background(), inGroup, byTitle["ACL Doc"]); err != nil {
		t.Errorf("an in-ACL agent must be able to fetch the document, got %v", err)
	}
}

// TestF05FetchDocumentEnforcesExternalLabel: FetchDocument must also apply the
// external-label predicate (retrieval.go loadCandidates) — a document carrying source-system
// sensitivity labels is discoverable only when the identity's LabelClearances cover a label.
func TestF05FetchDocumentEnforcesExternalLabel(t *testing.T) {
	var allowedKB string
	h := newHarnessWith(t,
		// KB clearance + scope, but NO external-label clearance declared.
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithRetrievalScopeGate(kbAllowlistScopeGate{allow: &allowedKB}),
	)
	token := h.adminLogin()
	tenant := h.createOrg(token, "f05lbl")
	tok := h.roleToken(token, tenant, "u@f05.io", "editor")
	hdr := tenantHdr(tenant)

	kbID := h.mustKB(tok, tenant, map[string]any{"name": "f05-lbl-kb"})
	allowedKB = kbID

	src := newFakeSource([]contentsource.Document{
		{DocID: "d-lbl", Title: "Labeled Doc", Body: "sensitive", ExternalLabels: []string{"purview:confidential"}},
	})
	h.addSource("f05-lbl-src", src)
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", tok, map[string]any{"source": "f05-lbl-src"}, hdr); r.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	byTitle := docIDsByTitle(t, h, tok, tenant, kbID)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant).WithAgentIdentity("agent-x"),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	if _, err := h.module().FetchDocument(context.Background(), mc, byTitle["Labeled Doc"]); err == nil {
		t.Error("F-05: FetchDocument leaked metadata for an externally-labeled document the agent is not cleared for")
	} else if qe, ok := IsQueryError(err); !ok || qe.Kind != QueryErrNotFound {
		t.Errorf("FetchDocument error = %v, want QueryErrNotFound", err)
	}

	// With the matching label clearance the document becomes fetchable.
	h.mod.guard = fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret, LabelClearances: []string{"purview:*"}}}
	if _, err := h.module().FetchDocument(context.Background(), mc, byTitle["Labeled Doc"]); err != nil {
		t.Errorf("a label-cleared agent must be able to fetch the document, got %v", err)
	}
}

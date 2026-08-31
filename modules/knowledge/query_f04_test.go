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
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// kbAllowlistScopeGate is a source-scope gate that admits ONLY the KB whose id equals
// *allow — modeling an agent scoped to a single knowledge base.
type kbAllowlistScopeGate struct{ allow *string }

func (g kbAllowlistScopeGate) Allowed(_ context.Context, _ model.TenantID, _ auth.Principal, _ string, kbRef string) (bool, string, error) {
	if g.allow != nil && kbRef == *g.allow {
		return true, "", nil
	}
	return false, "out of scope", nil
}

// TestF04DiscoveryIsScopedToTheAgent is the F-04 red repro: the MCP discovery
// methods (list_kbs, fetch_document) must apply the SAME clearance/scope boundary as
// retrieval. Before they did a bare tenant-scoped store read, so an agent scoped to
// one KB could enumerate every other KB (name + classification) and fetch any document's
// ACL/classification/content-hash — recon of content it could never retrieve.
func TestF04DiscoveryIsScopedToTheAgent(t *testing.T) {
	var allowedKB string
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
		WithRetrievalScopeGate(kbAllowlistScopeGate{allow: &allowedKB}),
	)
	token := h.adminLogin()
	tenant := h.createOrg(token, "f04")
	tok := h.roleToken(token, tenant, "u@f04.io", "editor")
	hdr := tenantHdr(tenant)

	allowed := h.mustKB(tok, tenant, map[string]any{"name": "in-scope-kb"})
	denied := h.mustKB(tok, tenant, map[string]any{"name": "out-of-scope-kb", "classification": "secret"})
	allowedKB = allowed

	src := newFakeSource([]contentsource.Document{{DocID: "d1", Title: "Secret", Body: "classified body"}})
	h.addSource("f04-src", src)
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+denied+"/ingest", tok, map[string]any{"source": "f04-src"}, hdr); r.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+denied+"/documents", tok, nil, hdr)
	items := docs.body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected a document in the denied KB")
	}
	deniedDocID := items[0].(map[string]any)["id"].(string)

	// An agent scoped (by the gate) only to the in-scope KB.
	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant).WithAgentIdentity("agent-x"),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}

	kbs, err := h.module().ListKBs(context.Background(), mc)
	if err != nil {
		t.Fatalf("ListKBs = %v", err)
	}
	for _, kb := range kbs.KBs {
		if kb.ID == denied {
			t.Errorf("F-04: ListKBs leaked an out-of-scope KB %q (%s)", kb.Name, kb.ID)
		}
	}
	if len(kbs.KBs) != 1 || kbs.KBs[0].ID != allowed {
		t.Errorf("ListKBs = %+v, want only the in-scope KB %s", kbs.KBs, allowed)
	}

	if _, err := h.module().FetchDocument(context.Background(), mc, deniedDocID); err == nil {
		t.Error("F-04: FetchDocument returned metadata for an out-of-scope document")
	} else if qe, ok := IsQueryError(err); !ok || qe.Kind != QueryErrNotFound {
		t.Errorf("FetchDocument error = %v, want QueryErrNotFound", err)
	}
}

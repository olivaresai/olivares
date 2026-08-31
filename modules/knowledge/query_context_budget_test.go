// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

func TestQueryContextBudgetTruncatesAndEmitsFinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ctxbudget")
	editor := h.roleToken(admin, tenant, "editor@ctxbudget.io", "editor")
	kbID := seedContextBudgetCorpus(t, h, tenant, editor, "budget-kb")

	seedContextPolicies(t, h, tenant, contextPolicySeed{
		kind: scopeAgent, ref: "agent-budget", maxTokens: 7, strategy: strategyTruncate,
	})

	result, err := h.module().Query(context.Background(), contextBudgetMC(h, tenant, "agent-budget"), QueryRequest{
		KBID:  kbID,
		Query: "alpha beta",
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Query = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("budgeted query count = %d, want 1", result.Count)
	}
	if !result.ContextTruncated {
		t.Fatal("ContextTruncated = false, want true")
	}
	if result.ContextDroppedChunks <= 0 {
		t.Fatalf("ContextDroppedChunks = %d, want >0", result.ContextDroppedChunks)
	}
	if result.ContextBudgetTokens != 7 {
		t.Fatalf("ContextBudgetTokens = %d, want 7", result.ContextBudgetTokens)
	}
	if result.ContextWinningScope != "agent:agent-budget" {
		t.Fatalf("ContextWinningScope = %q, want agent:agent-budget", result.ContextWinningScope)
	}
	if !h.hasFinding(findingContextTruncated) {
		t.Fatal("expected knowledge_context_truncated finding")
	}
}

func TestQueryContextBudgetUnderLimitReturnsAll(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ctxfit")
	editor := h.roleToken(admin, tenant, "editor@ctxfit.io", "editor")
	kbID := seedContextBudgetCorpus(t, h, tenant, editor, "fit-kb")

	seedContextPolicies(t, h, tenant, contextPolicySeed{
		kind: scopeAgent, ref: "agent-fit", maxTokens: 20, strategy: strategyWindow,
	})

	result, err := h.module().Query(context.Background(), contextBudgetMC(h, tenant, "agent-fit"), QueryRequest{
		KBID:  kbID,
		Query: "alpha beta",
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Query = %v", err)
	}
	if result.Count != 3 {
		t.Fatalf("under-budget query count = %d, want 3", result.Count)
	}
	if result.ContextTruncated {
		t.Fatal("ContextTruncated = true, want false")
	}
	if result.ContextDroppedChunks != 0 {
		t.Fatalf("ContextDroppedChunks = %d, want 0", result.ContextDroppedChunks)
	}
	if result.ContextBudgetTokens != 20 {
		t.Fatalf("ContextBudgetTokens = %d, want 20", result.ContextBudgetTokens)
	}
	if result.ContextStrategy != strategyWindow {
		t.Fatalf("ContextStrategy = %q, want %q", result.ContextStrategy, strategyWindow)
	}
	if h.hasFinding(findingContextTruncated) {
		t.Fatal("unexpected knowledge_context_truncated finding")
	}
}

func TestQueryContextPolicyForbidDeniesAndEmitsFinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ctxdeny")
	editor := h.roleToken(admin, tenant, "editor@ctxdeny.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "deny-kb"})

	seedContextPolicies(t, h, tenant, contextPolicySeed{
		kind: scopeAgent, ref: "agent-deny", effect: effectForbid,
	})

	_, err := h.module().Query(context.Background(), contextBudgetMC(h, tenant, "agent-deny"), QueryRequest{
		KBID:  kbID,
		Query: "anything",
		TopK:  3,
	})
	qe, ok := IsQueryError(err)
	if !ok || qe.Kind != QueryErrDenied {
		t.Fatalf("Query error = %v, want QueryErrDenied", err)
	}
	if !h.hasFinding(findingContextDenied) {
		t.Fatal("expected knowledge_context_denied finding")
	}
}

func TestQueryWithoutContextPolicyLeavesRetrievalUntruncated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ctxnone")
	editor := h.roleToken(admin, tenant, "editor@ctxnone.io", "editor")
	kbID := seedContextBudgetCorpus(t, h, tenant, editor, "none-kb")

	result, err := h.module().Query(context.Background(), contextBudgetMC(h, tenant, "agent-none"), QueryRequest{
		KBID:  kbID,
		Query: "alpha beta",
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Query = %v", err)
	}
	if result.Count != 3 {
		t.Fatalf("no-policy query count = %d, want 3", result.Count)
	}
	if result.ContextTruncated {
		t.Fatal("ContextTruncated = true, want false")
	}
	if result.ContextDroppedChunks != 0 {
		t.Fatalf("ContextDroppedChunks = %d, want 0", result.ContextDroppedChunks)
	}
	if result.ContextBudgetTokens != 0 {
		t.Fatalf("ContextBudgetTokens = %d, want 0", result.ContextBudgetTokens)
	}
}

func seedContextBudgetCorpus(t *testing.T, h *harness, tenant model.TenantID, token, name string) string {
	t.Helper()
	kbID := h.mustKB(token, tenant, map[string]any{"name": name})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", token, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "d1", "title": "One", "body": "alpha bravo charlie delta"},
			{"source_doc_id": "d2", "title": "Two", "body": "beta echo foxtrot golf"},
			{"source_doc_id": "d3", "title": "Three", "body": "alpha hotel india juliet"},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	return kbID
}

// contextBudgetMC builds a module context whose principal carries the given
// authenticated agent identity — the ONLY source of the effective agent axis after
// F-03; a body agent_ref is no longer honored.
func contextBudgetMC(h *harness, tenant model.TenantID, agentRef string) api.ModuleContext {
	return api.ModuleContext{
		Principal: h.scopedPrincipal(tenant).WithAgentIdentity(agentRef),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
}

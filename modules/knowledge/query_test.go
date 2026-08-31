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

func TestQueryProgrammatic(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	tenant := h.createOrg(token, "querytest")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "test-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	src := newFakeSource([]contentsource.Document{
		{DocID: "d1", Title: "Alpha", Body: "The quick brown fox jumps over the lazy dog"},
		{DocID: "d2", Title: "Beta", Body: "Enterprise governance for AI agent systems"},
	})
	h.addSource("test-src", src)

	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", tok, map[string]any{
		"source": "test-src",
	}, hdr)
	if ingest.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingest.code, ingest.raw)
	}

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}

	mod := h.module()

	t.Run("basic query returns results", func(t *testing.T) {
		result, err := mod.Query(context.Background(), mc, QueryRequest{
			KBID:  kbID,
			Query: "governance",
			TopK:  5,
		})
		if err != nil {
			t.Fatalf("Query = %v", err)
		}
		if result.LineageID == "" {
			t.Error("expected non-empty lineage_id")
		}
		if result.Count == 0 {
			t.Error("expected at least one result")
		}
		if result.EmbedModel == "" {
			t.Error("expected non-empty embed_model")
		}
	})

	t.Run("empty query is bad request", func(t *testing.T) {
		_, err := mod.Query(context.Background(), mc, QueryRequest{
			KBID:  kbID,
			Query: "",
		})
		qe, ok := IsQueryError(err)
		if !ok || qe.Kind != QueryErrBadRequest {
			t.Fatalf("expected QueryErrBadRequest, got %v", err)
		}
	})

	t.Run("invalid KB ID is bad request", func(t *testing.T) {
		_, err := mod.Query(context.Background(), mc, QueryRequest{
			KBID:  "",
			Query: "test",
		})
		qe, ok := IsQueryError(err)
		if !ok || qe.Kind != QueryErrBadRequest {
			t.Fatalf("expected QueryErrBadRequest, got %v", err)
		}
	})

	t.Run("nonexistent KB is not found", func(t *testing.T) {
		fakeID := model.NewID()
		_, err := mod.Query(context.Background(), mc, QueryRequest{
			KBID:  fakeID.String(),
			Query: "test",
		})
		qe, ok := IsQueryError(err)
		if !ok || qe.Kind != QueryErrNotFound {
			t.Fatalf("expected QueryErrNotFound, got %v", err)
		}
	})

	t.Run("default topK is 10", func(t *testing.T) {
		result, err := mod.Query(context.Background(), mc, QueryRequest{
			KBID:  kbID,
			Query: "fox",
		})
		if err != nil {
			t.Fatalf("Query = %v", err)
		}
		if result.Count > 10 {
			t.Errorf("expected at most 10 results, got %d", result.Count)
		}
	})
}

func TestQueryDeniedByGuard(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: false, Reason: "test deny",
	}}))
	token := h.adminLogin()
	tenant := h.createOrg(token, "denied")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "denied-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	mod := h.module()

	_, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID:  kbID,
		Query: "test",
	})
	qe, ok := IsQueryError(err)
	if !ok || qe.Kind != QueryErrDenied {
		t.Fatalf("expected QueryErrDenied, got %v", err)
	}
}

func TestQueryDeniedByGuardError(t *testing.T) {
	h := newHarnessWith(t, WithRetrievalGuard(errorGuard{}))
	token := h.adminLogin()
	tenant := h.createOrg(token, "guarderr")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "guarderr-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	mod := h.module()

	_, err := mod.Query(context.Background(), mc, QueryRequest{
		KBID:  kbID,
		Query: "test",
	})
	qe, ok := IsQueryError(err)
	if !ok || qe.Kind != QueryErrDenied {
		t.Fatalf("expected QueryErrDenied on guard error, got %v", err)
	}
}

func TestFetchDocumentProgrammatic(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	tenant := h.createOrg(token, "fetchdoc")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "fetchdoc-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	src := newFakeSource([]contentsource.Document{
		{DocID: "d1", Title: "Document One", Body: "Content for testing document fetch"},
	})
	h.addSource("fetchdoc-src", src)

	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", tok, map[string]any{
		"source": "fetchdoc-src",
	}, hdr)
	if ingest.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingest.code, ingest.raw)
	}

	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", tok, nil, hdr)
	if docs.code != http.StatusOK {
		t.Fatalf("list docs = %d %s", docs.code, docs.raw)
	}
	items := docs.body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("expected at least one document")
	}
	docID := items[0].(map[string]any)["id"].(string)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	mod := h.module()

	t.Run("fetch existing document", func(t *testing.T) {
		result, err := mod.FetchDocument(context.Background(), mc, docID)
		if err != nil {
			t.Fatalf("FetchDocument = %v", err)
		}
		if result.ID != docID {
			t.Errorf("expected id %q, got %q", docID, result.ID)
		}
		if result.KBRef == "" {
			t.Error("expected non-empty kb_ref")
		}
		if result.Title == "" {
			t.Error("expected non-empty title")
		}
	})

	t.Run("nonexistent document is not found", func(t *testing.T) {
		_, err := mod.FetchDocument(context.Background(), mc, model.NewID().String())
		qe, ok := IsQueryError(err)
		if !ok || qe.Kind != QueryErrNotFound {
			t.Fatalf("expected QueryErrNotFound, got %v", err)
		}
	})

	t.Run("invalid document ID is bad request", func(t *testing.T) {
		_, err := mod.FetchDocument(context.Background(), mc, "")
		qe, ok := IsQueryError(err)
		if !ok || qe.Kind != QueryErrBadRequest {
			t.Fatalf("expected QueryErrBadRequest, got %v", err)
		}
	})
}

func TestListKBsProgrammatic(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	tenant := h.createOrg(token, "listkbs")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "kb-one"}, hdr)
	h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "kb-two"}, hdr)

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	mod := h.module()

	result, err := mod.ListKBs(context.Background(), mc)
	if err != nil {
		t.Fatalf("ListKBs = %v", err)
	}
	if len(result.KBs) < 2 {
		t.Fatalf("expected at least 2 KBs, got %d", len(result.KBs))
	}
	names := map[string]bool{}
	for _, kb := range result.KBs {
		names[kb.Name] = true
		if kb.ID == "" {
			t.Error("KB has empty ID")
		}
	}
	if !names["kb-one"] || !names["kb-two"] {
		t.Errorf("expected both KBs, got %v", names)
	}
}

// TestF03QueryAgentAxisIsAuthenticatedIdentityOnly proves the F-03 contract at
// the programmatic seam: the effective agent reference handed to the scope gate / guard
// is SOLELY mc.Principal.AgentIdentity. With an authenticated agent identity it is that
// identity; with none it is empty (no borrowed agent) — QueryRequest carries no
// agent_ref to spoof (the confused-deputy path stays closed).
func TestF03QueryAgentAxisIsAuthenticatedIdentityOnly(t *testing.T) {
	run := func(t *testing.T, agentID, want string) {
		var capturedRef string
		h := newHarnessWith(t,
			WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}),
			WithRetrievalScopeGate(capturingFakeScopeGate{allow: true, capture: &capturedRef}),
		)
		token := h.adminLogin()
		tenant := h.createOrg(token, "f03query"+agentID)
		tok := h.roleToken(token, tenant, "u@f03"+agentID+".io", "editor")
		hdr := tenantHdr(tenant)

		kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "f03-kb"}, hdr)
		if kb.code != 201 {
			t.Fatalf("create KB = %d %s", kb.code, kb.raw)
		}
		kbID := kb.body["id"].(string)

		principal := h.scopedPrincipal(tenant)
		if agentID != "" {
			principal = principal.WithAgentIdentity(agentID)
		}
		mc := api.ModuleContext{Principal: principal, Tenant: tenant, Data: api.NewScopedData(h.st, tenant)}

		_, _ = h.module().Query(context.Background(), mc, QueryRequest{KBID: kbID, Query: "anything"})

		if capturedRef != want {
			t.Errorf("scope gate received agentRef %q, want %q (agent axis must come only from the authenticated identity)", capturedRef, want)
		}
	}

	t.Run("authenticated agent identity is the effective agent", func(t *testing.T) {
		run(t, "authenticated-agent", "authenticated-agent")
	})
	t.Run("no authenticated identity => no borrowed agent", func(t *testing.T) {
		run(t, "", "")
	})
}

// capturingFakeScopeGate stores the agentRef it was called with so tests can
// assert that applied the override.
type capturingFakeScopeGate struct {
	allow   bool
	capture *string
}

func (g capturingFakeScopeGate) Allowed(_ context.Context, _ model.TenantID, _ auth.Principal, agentRef, _ string) (bool, string, error) {
	if g.capture != nil {
		*g.capture = agentRef
	}
	return g.allow, "", nil
}

func TestHandleQueryDelegatesToProgrammaticAPI(t *testing.T) {
	h := newHarness(t)
	token := h.adminLogin()
	tenant := h.createOrg(token, "delegate")
	tok := h.roleToken(token, tenant, "u@x.io", "editor")
	hdr := tenantHdr(tenant)

	kb := h.do("POST", "/v1/m/knowledge/kbs", tok, map[string]any{"name": "delegate-kb"}, hdr)
	if kb.code != http.StatusCreated {
		t.Fatalf("create KB = %d %s", kb.code, kb.raw)
	}
	kbID := kb.body["id"].(string)

	src := newFakeSource([]contentsource.Document{
		{DocID: "d1", Title: "Delegate", Body: "Testing delegation from HTTP handler to Query method"},
	})
	h.addSource("delegate-src", src)

	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", tok, map[string]any{
		"source": "delegate-src",
	}, hdr)
	if ingest.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingest.code, ingest.raw)
	}

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", tok, map[string]any{
		"query":     "delegation",
		"agent_ref": "test-agent",
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	if r.body["lineage_id"] == nil || r.body["lineage_id"] == "" {
		t.Error("expected non-empty lineage_id in REST response")
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/sdk"
)

func TestE2E_GovernedRAGDefaultsLive(t *testing.T) {
	totalStart := time.Now()
	clearEmbeddingEnv(t)
	var embedCalls atomic.Int64
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		embedCalls.Add(1)
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode embeddings request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i, text := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": governedRAGVector(text)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": req.Model, "data": data})
	}))
	t.Cleanup(embedSrv.Close)
	t.Setenv("OLIVARES_EMBEDDINGS_BASE_URL", embedSrv.URL)
	t.Setenv("OLIVARES_EMBEDDINGS_KEY", "fixture-key")
	t.Setenv("OLIVARES_EMBEDDINGS_MODEL", "fixture-governed-rag")
	t.Setenv("OLIVARES_EMBEDDINGS_DIM", "3")
	t.Setenv("OLIVARES_EMBEDDINGS_GEO", "global")

	harnessStart := time.Now()
	h := newHarness(t)
	harnessDur := time.Since(harnessStart)

	status := h.getJSON("", "", "/status")
	if status["embedder_kind"] != "semantic" || status["retrieval_semantic"] != true {
		t.Fatalf("/status semantic fields = kind %v semantic %v, want semantic/true", status["embedder_kind"], status["retrieval_semantic"])
	}

	src := newGovernedRAGLiveFixtureSource()
	h.set.knowledge.AddSource("e2e-live-s3-fixture", src)

	bindHarnessAgentIdentity(t, h, seed.AgentCoder, "agent:nhi:coder", "secret", []string{"group:engineering"})
	bindHarnessAgentIdentity(t, h, seed.AgentReviewer, "agent:nhi:reviewer", "public", []string{"group:engineering"})
	bindHarnessAgentIdentity(t, h, seed.AgentIndexer, "agent:nhi:indexer", "secret", []string{"group:engineering"})

	var kb struct {
		ID         string `json:"id"`
		EmbedModel string `json:"embed_model"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA, map[string]any{
		"name": "e2e-governed-rag", "classification": "public", "embed_policy": "model_backed",
	}, &kb); code != http.StatusCreated || kb.ID == "" {
		t.Fatalf("create governed KB = %d id=%q", code, kb.ID)
	}
	if kb.EmbedModel != "fixture-governed-rag" {
		t.Fatalf("KB embed_model = %q, want fixture-governed-rag", kb.EmbedModel)
	}
	assertGovernedRAGGrants(t, h, seed.AgentCoder, "e2e-governed-rag", "secret", "group:engineering")
	assertGovernedRAGGrants(t, h, seed.AgentReviewer, "e2e-governed-rag", "public", "group:engineering")

	ingestStart := time.Now()
	if code, raw := h.req("POST", "/v1/m/knowledge/kbs/"+kb.ID+"/ingest", h.adminToken, h.tenantA, map[string]any{
		"source": "e2e-live-s3-fixture",
	}); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("ingest live fixture = %d: %s", code, raw)
	}
	ingestDur := time.Since(ingestStart)
	if embedCalls.Load() == 0 {
		t.Fatal("semantic embedding fixture was not called")
	}
	docs := h.getJSON(h.adminToken, h.tenantA, "/v1/m/knowledge/kbs/"+kb.ID+"/documents")
	docItems := items(docs)
	if len(docItems) != 1 || docItems[0]["source_mode"] != "live" {
		t.Fatalf("document provenance = %+v, want one source_mode=live document", docItems)
	}

	createKnowledgeAgentScope(t, h, kb.ID, seed.AgentCoder)
	createKnowledgeAgentScope(t, h, kb.ID, seed.AgentReviewer)

	tenantID, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	ru, err := newRetrievalUpstream(retrievalUpstreamConfig{
		Module: h.set.knowledge,
		Store:  h.st,
		Tenant: tenantID,
		// Keep the synthetic MCP principal agent-scoped, not tenant-wide RBAC, so
		// this test proves the source-scope binding rather than viewer RBAC.
		Role: "agent_retrieval",
		Log:  quietLog(),
	})
	if err != nil {
		t.Fatalf("retrieval upstream: %v", err)
	}

	good, goodErr, goodText, scopeDur := callMCPRetrievalTool[knowledge.QueryResult](t, ru, seed.AgentCoder, "search_kb", map[string]any{
		"kb_id": kb.ID, "query": "quasar runway cedar ledger", "top_k": 5,
	})
	if goodErr {
		t.Fatalf("scoped/cleared search returned MCP error: %s", goodText)
	}
	if good.Count < 1 || len(good.Results) < 1 {
		lineage := h.getJSON(h.adminToken, h.tenantA, "/v1/m/knowledge/lineage/"+good.LineageID)
		t.Fatalf("scoped/cleared search returned no results: %+v lineage=%+v docs=%+v", good, lineage, docItems)
	}
	if good.Results[0].SourceMode != "live" || good.Results[0].SourceKind != "s3" {
		t.Fatalf("query result provenance = kind %q mode %q, want s3/live", good.Results[0].SourceKind, good.Results[0].SourceMode)
	}
	if good.Results[0].Classification != "secret" {
		t.Fatalf("query result classification = %q, want secret", good.Results[0].Classification)
	}

	fetched, fetchErr, fetchText, fetchDur := callMCPRetrievalTool[knowledge.DocumentResult](t, ru, seed.AgentCoder, "fetch_document", map[string]any{
		"document_id": good.Results[0].DocumentID,
	})
	if fetchErr {
		t.Fatalf("fetch_document returned MCP error: %s", fetchText)
	}
	if fetched.SourceMode != "live" {
		t.Fatalf("fetch_document source_mode = %q, want live", fetched.SourceMode)
	}

	noClearance, deniedByClearance, clearanceText, clearanceDur := callMCPRetrievalTool[knowledge.QueryResult](t, ru, seed.AgentReviewer, "search_kb", map[string]any{
		"kb_id": kb.ID, "query": "quasar runway cedar ledger", "top_k": 5,
	})
	if deniedByClearance {
		t.Fatalf("in-scope low-clearance search should filter to zero, got MCP error: %s", clearanceText)
	}
	if noClearance.Count != 0 || len(noClearance.Results) != 0 {
		t.Fatalf("low-clearance agent retrieved restricted chunks: %+v", noClearance.Results)
	}

	_, outOfScopeErr, outOfScopeText, outOfScopeDur := callMCPRetrievalTool[knowledge.QueryResult](t, ru, seed.AgentIndexer, "search_kb", map[string]any{
		"kb_id": kb.ID, "query": "quasar runway cedar ledger", "top_k": 5,
	})
	if !outOfScopeErr || !strings.Contains(outOfScopeText, "out of scope") {
		t.Fatalf("out-of-scope agent = isError %v text %q, want MCP isError out of scope", outOfScopeErr, outOfScopeText)
	}

	t.Logf("GOVERNED_RAG_TIMING harness_boot=%s ingest=%s scope_search=%s fetch=%s deny_no_clearance=%s deny_out_of_scope=%s total=%s",
		harnessDur, ingestDur, scopeDur, fetchDur, clearanceDur, outOfScopeDur, time.Since(totalStart))
}

func governedRAGVector(text string) []float64 {
	text = strings.ToLower(text)
	if strings.Contains(text, "quasar") || strings.Contains(text, "cedar") || strings.Contains(text, "ledger") {
		return []float64{1, 0, 0}
	}
	return []float64{0, 1, 0}
}

type governedRAGLiveFixtureSource struct {
	docs map[string]contentsource.Document
	refs []contentsource.DocRef
}

func newGovernedRAGLiveFixtureSource() *governedRAGLiveFixtureSource {
	modified := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	doc := contentsource.Document{
		Source: contentsource.SourceS3,
		DocID:  "governed-live-bucket/runbooks/ledger-rotation.md",
		Title:  "Ledger rotation",
		Body: "quasar runway cedar ledger rotation runbook. Engineering operations notes " +
			"for the governed live S3 fixture.",
		ContentType:    "text/markdown",
		ACL:            []string{"group:engineering"},
		Classification: "secret",
		SpaceRef:       "s3:governed-live-bucket",
		ModifiedAt:     modified,
		Attributes:     map[string]string{"fixture": "live-simulated", "key": "runbooks/ledger-rotation.md"},
	}
	return &governedRAGLiveFixtureSource{
		docs: map[string]contentsource.Document{doc.DocID: doc},
		refs: []contentsource.DocRef{{
			DocID: doc.DocID, Title: doc.Title, ContentType: doc.ContentType, ModifiedAt: modified,
		}},
	}
}

func (s *governedRAGLiveFixtureSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        "olivares.e2e-governed-rag-live-fixture",
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "E2E governed RAG live fixture",
		Description: "Test-only live-simulated document content source.",
	}
}

func (s *governedRAGLiveFixtureSource) Kind() contentsource.ContentClass {
	return contentsource.ClassDocument
}
func (s *governedRAGLiveFixtureSource) Open(context.Context, sdk.Config) error {
	return nil
}
func (s *governedRAGLiveFixtureSource) List(ctx context.Context, cursor string) ([]contentsource.DocRef, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if cursor != "" {
		return nil, "", nil
	}
	return append([]contentsource.DocRef(nil), s.refs...), "", nil
}
func (s *governedRAGLiveFixtureSource) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	if err := ctx.Err(); err != nil {
		return contentsource.Document{}, err
	}
	doc, ok := s.docs[docID]
	if !ok {
		return contentsource.Document{}, errors.New("fixture document not found")
	}
	return doc, nil
}
func (s *governedRAGLiveFixtureSource) Close(context.Context) error { return nil }
func (s *governedRAGLiveFixtureSource) Mode() string                { return "live" }

func bindHarnessAgentIdentity(t *testing.T, h *harness, agentRef, identityRef, clearance string, groups []string) {
	t.Helper()
	tenant := model.TenantID(h.tenantA)
	identityID := seedIdentity(t, h.st, tenant, identityRef, map[string]any{
		"attr_clearance": clearance,
		"attr_region":    "global",
	})
	if err := h.st.Mutate(context.Background(), tenant, func(scoped store.Scope) error {
		agents, _, err := scoped.Agents().List(context.Background(), eqQuery("external_id", agentRef))
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			_, err = scoped.Agents().Create(context.Background(), model.Agent{
				Name: agentRef, Kind: "claude_code", ExternalID: agentRef, Status: model.StatusActive, IdentityID: identityID,
			})
			return err
		}
		agents[0].IdentityID = identityID
		_, err = scoped.Agents().Update(context.Background(), agents[0])
		return err
	}); err != nil {
		t.Fatalf("bind agent %q identity: %v", agentRef, err)
	}
	for _, group := range groups {
		seedEdge(t, h.st, tenant, identityRef, group, "identity")
	}
}

func createKnowledgeAgentScope(t *testing.T, h *harness, kbID, agentRef string) {
	t.Helper()
	if code, raw := h.req("POST", "/v1/m/sourcescope/bindings", h.adminToken, h.tenantA, map[string]any{
		"source_type": "knowledge",
		"source_ref":  kbID,
		"scope_tree":  "agent",
		"scope_ref":   agentRef,
		"enabled":     true,
	}); code != http.StatusCreated {
		t.Fatalf("source-scope binding %s/%s = %d: %s", kbID, agentRef, code, raw)
	}
}

func assertGovernedRAGGrants(t *testing.T, h *harness, agentRef, kbName, wantClearance, wantGroup string) {
	t.Helper()
	grants, err := h.set.knowledgeGuard.Resolve(context.Background(), model.TenantID(h.tenantA), "user:e2e", agentRef, kbName)
	if err != nil {
		t.Fatalf("resolve retrieval grants for %s: %v", agentRef, err)
	}
	if !grants.Allowed || grants.Clearance != wantClearance || !contains(grants.Groups, wantGroup) {
		t.Fatalf("retrieval grants for %s = %+v, want allowed clearance=%s group=%s", agentRef, grants, wantClearance, wantGroup)
	}
}

func callMCPRetrievalTool[T any](t *testing.T, ru *retrievalUpstream, subject, name string, args map[string]any) (T, bool, string, time.Duration) {
	t.Helper()
	var zero T
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatalf("marshal MCP params: %v", err)
	}
	start := time.Now()
	res, err := ru.Forward(context.Background(), mcp.UpstreamRequest{
		Method: "tools/call", Subject: subject, Params: params,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("MCP forward %s: %v", name, err)
	}
	raw := res.Result
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode MCP envelope: %v (%s)", err, raw)
	}
	text := ""
	if len(env.Content) > 0 {
		text = env.Content[0].Text
	}
	if env.IsError {
		return zero, true, text, elapsed
	}
	var out T
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode MCP tool text: %v (%s)", err, text)
	}
	return out, false, text, elapsed
}

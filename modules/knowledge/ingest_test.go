// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type modeFakeSource struct {
	*fakeSource
	mode string
}

func (s modeFakeSource) Mode() string { return s.mode }

// allChunks reads every chunk record of a KB directly from the store (white-box,
// to assert what was PERSISTED — the authoritative redaction check).
func (h *harness) allChunks(tenant model.TenantID, kbID string) []model.Record {
	h.t.Helper()
	var out []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		out, err = listAll(context.Background(), repo, eq(colKBRef, kbID))
		return err
	}); err != nil {
		h.t.Fatalf("allChunks: %v", err)
	}
	return out
}

// insertKBRaw writes a knowledge_base row directly (bypassing the create gate) so a
// test can construct a KB state — e.g. a local_only KB — that the API create would
// reject, in order to exercise a defensive downstream gate.
func (h *harness) insertKBRaw(tenant model.TenantID, fields model.Record) string {
	h.t.Helper()
	var id string
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), fields)
		if err != nil {
			return err
		}
		id = rec.String(model.ColID)
		return nil
	}); err != nil {
		h.t.Fatalf("insertKBRaw: %v", err)
	}
	return id
}

func TestIngestInlineRedactsBeforeIndexing(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	kbID := h.mustKB(editor, tenant, map[string]any{"name": "handbook", "classification": "internal"})

	body := "The deploy key is AKIAIOSFODNN7EXAMPLE and the token is " +
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.abcdefghij. Contact bob@acme.com."
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "doc1", "title": "Runbook", "body": body}},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	if rc, _ := r.body["redactions_total"].(float64); rc < 3 {
		t.Errorf("expected >=3 redactions, got %v", r.body["redactions_total"])
	}
	if ch, _ := r.body["chunks"].(float64); ch < 1 {
		t.Fatalf("expected >=1 chunk, got %v", r.body["chunks"])
	}
	if eg, _ := r.body["egress"].(bool); eg {
		t.Error("local embedder must report egress=false on ingest")
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	items, _ := docs.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 inline document in list, got %d", len(items))
	}
	if mode := items[0].(map[string]any)["source_mode"]; mode != sourceModeDirect {
		t.Fatalf("inline push source_mode = %v, want %q", mode, sourceModeDirect)
	}

	// The PERSISTED chunks contain none of the secrets (redaction before indexing).
	for _, c := range h.allChunks(tenant, kbID) {
		txt := c.String(colText)
		for _, secret := range []string{"AKIAIOSFODNN7EXAMPLE", "bob@acme.com", "eyJhbGciOiJIUzI1NiJ9"} {
			if strings.Contains(txt, secret) {
				t.Errorf("persisted chunk leaked secret %q: %q", secret, txt)
			}
		}
		if !c.Bool(colIndexed) {
			t.Error("chunk should be indexed (embedded)")
		}
	}
	if !h.hasFinding(findingSecretRedacted) {
		t.Error("expected a knowledge_secret_redacted finding")
	}
}

func TestIngestFromSourceCarriesProvenance(t *testing.T) {
	src := modeFakeSource{fakeSource: newFakeSource([]contentsource.Document{
		{Source: contentsource.SourceConfluence, DocID: "p1", Title: "Runbook", Body: "Restart the api service.",
			ContentType: "text/html", ACL: []string{"anyone"}, Classification: "internal", SpaceRef: "space:ENG"},
		{Source: contentsource.SourceConfluence, DocID: "p2", Title: "Onboarding", Body: "Welcome to the team.",
			ContentType: "text/html", ACL: []string{"group:hr"}, Classification: "public"},
	}), mode: sourceModeExport}
	h := newHarnessWith(t, WithSource("conf", src), WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "wiki"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "conf"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest from source = %d %s", r.code, r.raw)
	}
	if d, _ := r.body["documents"].(float64); d != 2 {
		t.Fatalf("expected 2 documents, got %v", r.body["documents"])
	}

	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	items, _ := docs.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("expected 2 documents in list, got %d", len(items))
	}
	first := items[0].(map[string]any)
	if first["source_kind"] != "confluence" {
		t.Errorf("provenance source_kind = %v", first["source_kind"])
	}
	if first["source_mode"] != sourceModeExport {
		t.Fatalf("pulled document source_mode = %v, want %q", first["source_mode"], sourceModeExport)
	}
	acl, _ := first["acl"].([]any)
	if len(acl) == 0 {
		t.Error("document should carry its source ACL")
	}

	mc := api.ModuleContext{
		Principal: h.scopedPrincipal(tenant),
		Tenant:    tenant,
		Data:      api.NewScopedData(h.st, tenant),
	}
	result, err := h.module().Query(context.Background(), mc, QueryRequest{
		KBID:  kbID,
		Query: "Restart api service",
		TopK:  1,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("query returned %d results, want 1", len(result.Results))
	}
	if result.Results[0].SourceMode != sourceModeExport {
		t.Fatalf("query result source_mode = %q, want %q", result.Results[0].SourceMode, sourceModeExport)
	}
	fetched, err := h.module().FetchDocument(context.Background(), mc, result.Results[0].DocumentID)
	if err != nil {
		t.Fatalf("fetch document: %v", err)
	}
	if fetched.SourceMode != sourceModeExport {
		t.Fatalf("fetch source_mode = %q, want %q", fetched.SourceMode, sourceModeExport)
	}
	lineage := h.do("GET", "/v1/m/knowledge/lineage/"+result.LineageID, editor, nil, tenantHdr(tenant))
	if lineage.code != http.StatusOK {
		t.Fatalf("lineage = %d %s", lineage.code, lineage.raw)
	}
	refs, _ := lineage.body["chunk_refs"].([]any)
	if len(refs) != 1 {
		t.Fatalf("lineage chunk_refs len = %d, want 1", len(refs))
	}
	if mode := refs[0].(map[string]any)["source_mode"]; mode != sourceModeExport {
		t.Fatalf("lineage chunk_ref source_mode = %v, want %q", mode, sourceModeExport)
	}
}

func TestIngestRejectsNonDocumentSource(t *testing.T) {
	src := newFakeSource(nil)
	src.kind = contentsource.ClassAuditLog // an feed, not knowledge
	h := newHarnessWith(t, WithSource("audit", src), WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{"source": "audit"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-document source, got %d %s", r.code, r.raw)
	}
}

func TestEmbedPolicyGateAtCreate(t *testing.T) {
	// With the LOCAL embedder: model_backed is refused (no semantic embedder),
	// local_only is accepted (zero egress).
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	if r := h.createKB(editor, tenant, map[string]any{"name": "mb", "embed_policy": "model_backed"}); r.code != http.StatusBadRequest {
		t.Fatalf("model_backed with local embedder should be rejected, got %d %s", r.code, r.raw)
	}
	if r := h.createKB(editor, tenant, map[string]any{"name": "lo", "embed_policy": "local_only"}); r.code != http.StatusCreated {
		t.Fatalf("local_only with local embedder should be accepted, got %d %s", r.code, r.raw)
	}

	// With an EGRESSING embedder: local_only is refused (would egress); model_backed
	// is accepted.
	he := newHarnessWith(t, WithEmbedder(egressEmbedder{}), WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin2 := he.adminLogin()
	tenant2 := he.createOrg(admin2, "acme")
	editor2 := he.roleToken(admin2, tenant2, "ed@acme.com", "editor")
	if r := he.createKB(editor2, tenant2, map[string]any{"name": "lo", "embed_policy": "local_only"}); r.code != http.StatusBadRequest {
		t.Fatalf("local_only with egressing embedder must be rejected, got %d %s", r.code, r.raw)
	}
	if r := he.createKB(editor2, tenant2, map[string]any{"name": "mb", "embed_policy": "model_backed"}); r.code != http.StatusCreated {
		t.Fatalf("model_backed with egressing embedder should be accepted, got %d %s", r.code, r.raw)
	}
}

// TestIngestRefusedWhenLocalOnlyKBWouldEgress exercises the DEFENSIVE ingest-time
// gate (B3): a local_only KB existing alongside an egressing embedder must refuse
// ingest and emit a finding — the content never leaves the perimeter. The KB is
// inserted directly (the create gate would otherwise forbid this state), simulating
// an embedder that changed under a residency-locked KB.
func TestIngestRefusedWhenLocalOnlyKBWouldEgress(t *testing.T) {
	h := newHarnessWith(t, WithEmbedder(egressEmbedder{}), WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.insertKBRaw(tenant, model.Record{
		colName: "locked", colClassif: "internal", colResidency: "eu", colEmbedPolicy: embedLocalOnly,
		colEmbedModel: "local-hash", colDim: int64(localEmbedDim), colDefaultACL: "[]", colOwnerRef: "test",
		colStatus: kbActive, colDocCount: int64(0), colChunkCount: int64(0),
	})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d1", "body": "secret eu-only content"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("ingest into local_only KB with egressing embedder must be refused (409), got %d %s", r.code, r.raw)
	}
	// No chunks were written — the content never left.
	if n := len(h.allChunks(tenant, kbID)); n != 0 {
		t.Fatalf("no content must be persisted when egress is refused, found %d chunks", n)
	}
	if !h.hasFinding(findingEgressBlocked) {
		t.Error("expected a knowledge_egress_blocked finding")
	}
}

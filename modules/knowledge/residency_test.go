// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// geoEmbedder is an egressing, model-backed embedder that DECLARES the data-
// residency region of the provider it sends text to (the optional Region()
// capability the module reads to enforce the residency↔egress gate). It reuses the
// lexical local embedding so retrieval still works in tests; only egress + geo
// differ from the zero-egress default.
type geoEmbedder struct {
	egressEmbedder
	region string
}

func (g geoEmbedder) Region() string { return g.region }

// TestResidencyEgressGateAtCreate proves a region-locked KB can be declared only
// when embedding it would NOT cross the residency boundary: an in-region egressing
// embedder is fine; an out-of-region or undeclared-region egressing embedder is
// refused; the local zero-egress embedder is always fine (it never leaves); and an
// unrestricted ("global") KB is never gated.
func TestResidencyEgressGateAtCreate(t *testing.T) {
	cases := []struct {
		name   string
		emb    Embedder
		region string
		wantOK bool
	}{
		{"local-zero-egress-eu", LocalHashEmbedder{}, "eu", true},
		{"in-region-eu", geoEmbedder{region: "eu"}, "eu", true},
		{"out-of-region-us-vs-eu", geoEmbedder{region: "us"}, "eu", false},
		{"undeclared-region", egressEmbedder{}, "eu", false},
		{"global-unrestricted", geoEmbedder{region: "us"}, "global", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, WithEmbedder(tc.emb),
				WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
			r := h.createKB(editor, tenant, map[string]any{"name": "kb", "residency_region": tc.region, "embed_policy": "auto"})
			if tc.wantOK && r.code != http.StatusCreated {
				t.Fatalf("expected create OK, got %d %s", r.code, r.raw)
			}
			if !tc.wantOK && r.code != http.StatusBadRequest {
				t.Fatalf("expected create refused (400), got %d %s", r.code, r.raw)
			}
		})
	}
}

// TestResidencyEgressBlocksIngestAndQuery proves the defense-in-depth gate: a
// region-locked KB whose wired embedder egresses to a DIFFERENT region refuses both
// ingest and retrieval — the content and the query never cross the residency
// boundary, even for a KB that predates the (mismatched) embedder. The KB is
// inserted raw to bypass the create-time gate and simulate exactly that drift.
func TestResidencyEgressBlocksIngestAndQuery(t *testing.T) {
	h := newHarnessWith(t, WithEmbedder(geoEmbedder{region: "us"}),
		WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret, Region: "eu"}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.insertKBRaw(tenant, model.Record{
		colName: "eu-kb", colClassif: "internal", colResidency: "eu", colEmbedPolicy: embedModelBacked,
		colEmbedModel: "hosted-embed-model", colDim: int64(localEmbedDim), colDefaultACL: "[]", colOwnerRef: "test",
		colStatus: kbActive, colDocCount: int64(0), colChunkCount: int64(0),
	})

	ing := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d1", "body": "eu-only content"}},
	}, tenantHdr(tenant))
	if ing.code != http.StatusConflict {
		t.Fatalf("ingest into an eu KB with a us embedder must be refused (409), got %d %s", ing.code, ing.raw)
	}
	if n := len(h.allChunks(tenant, kbID)); n != 0 {
		t.Fatalf("no content must persist when egress is refused, found %d chunks", n)
	}
	if !h.hasFinding(findingResidencyViolation) {
		t.Error("expected a knowledge_residency_violation finding")
	}

	q := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{"query": "eu-only content"}, tenantHdr(tenant))
	if q.code != http.StatusConflict {
		t.Fatalf("query into an eu KB with a us embedder must be refused (409), got %d %s", q.code, q.raw)
	}
}

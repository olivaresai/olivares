// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seedTwoDocs ingests an "open" doc (group:eng, internal) and a more-relevant
// "secret" doc (group:hr, confidential) into a fresh KB, returning the KB id. The
// secret doc shares MORE query terms, so if governance did not filter before
// ranking it would rank first.
func seedTwoDocs(t *testing.T, h *harness, token string, tenant model.TenantID) string {
	t.Helper()
	kbID := h.mustKB(token, tenant, map[string]any{"name": "kb", "classification": "internal"})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", token, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "open", "title": "Open", "body": "edge governance retrieval overview",
				"acl": []string{"group:eng"}, "classification": "internal"},
			{"source_doc_id": "secret", "title": "Secret", "body": "edge governance retrieval secret restructuring layoffs plan details",
				"acl": []string{"group:hr"}, "classification": "confidential"},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("seed ingest = %d %s", r.code, r.raw)
	}
	return kbID
}

func TestRetrievalFiltersACLAndClassificationBeforeRanking(t *testing.T) {
	// An engineer (group:eng, internal clearance) must NOT retrieve the HR/
	// confidential doc, even though it is the most lexically similar to the query.
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"group:eng"}, Clearance: classInternal,
	}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := seedTwoDocs(t, h, editor, tenant)

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{
		"query": "edge governance retrieval secret layoffs", "top_k": 10, "agent_ref": "agent-1",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	results, _ := r.body["results"].([]any)
	if len(results) == 0 {
		t.Fatal("expected at least the open document's chunk")
	}
	for _, it := range results {
		res := it.(map[string]any)
		if res["document_id"] == "" {
			continue
		}
		if res["classification"] == "confidential" {
			t.Errorf("confidential chunk leaked to an internal-clearance identity: %v", res)
		}
		// The HR doc's chunk must never appear (filtered before ranking).
		if txt, _ := res["text"].(string); contains(txt, "layoffs") {
			t.Errorf("restricted chunk text leaked into results: %q", txt)
		}
	}
}

func TestRetrievalIdentityACLClassificationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		grants Grants
		want   []string
		deny   []string
	}{
		{
			name:   "public clearance no groups",
			grants: Grants{Allowed: true, Clearance: classPublic},
			want:   []string{"public"},
			deny:   []string{"eng-internal", "hr-confidential", "eng-secret", "external-labeled", "unknown-class", "unknown-acl"},
		},
		{
			name:   "engineering internal",
			grants: Grants{Allowed: true, Groups: []string{"group:eng"}, Clearance: classInternal},
			want:   []string{"public", "eng-internal"},
			deny:   []string{"hr-confidential", "eng-secret", "external-labeled", "unknown-class", "unknown-acl"},
		},
		{
			name:   "hr confidential",
			grants: Grants{Allowed: true, Groups: []string{"group:hr"}, Clearance: classConfidential},
			want:   []string{"public", "hr-confidential"},
			deny:   []string{"eng-internal", "eng-secret", "external-labeled", "unknown-class", "unknown-acl"},
		},
		{
			name:   "engineering secret with external-label clearance",
			grants: Grants{Allowed: true, Groups: []string{"group:eng"}, Clearance: classSecret, LabelClearances: []string{"purview:*"}},
			want:   []string{"public", "eng-internal", "eng-secret", "external-labeled"},
			deny:   []string{"hr-confidential", "unknown-class", "unknown-acl"},
		},
		{
			name:   "secret no groups with external-label clearance",
			grants: Grants{Allowed: true, Clearance: classSecret, LabelClearances: []string{"purview:confidential"}},
			want:   []string{"public", "external-labeled"},
			deny:   []string{"eng-internal", "hr-confidential", "eng-secret", "unknown-class", "unknown-acl"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: tc.grants}))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
			kbID := seedGovernanceMatrix(t, h, editor, tenant)

			got := queryTitleSet(t, h, editor, tenant, kbID)
			for _, title := range tc.want {
				if !got[title] {
					t.Fatalf("expected %q to be retrieved; got titles %v", title, got)
				}
			}
			for _, title := range tc.deny {
				if got[title] {
					t.Fatalf("title %q must be denied under %s; got titles %v", title, tc.name, got)
				}
			}
		})
	}
}

func seedGovernanceMatrix(t *testing.T, h *harness, token string, tenant model.TenantID) string {
	t.Helper()
	kbID := h.mustKB(token, tenant, map[string]any{"name": "matrix", "classification": "public"})
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", token, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "public", "title": "public", "body": "matrix retrieval public", "classification": "public"},
			{"source_doc_id": "eng-internal", "title": "eng-internal", "body": "matrix retrieval eng internal", "acl": []string{"group:eng"}, "classification": "internal"},
			{"source_doc_id": "hr-confidential", "title": "hr-confidential", "body": "matrix retrieval hr confidential", "acl": []string{"group:hr"}, "classification": "confidential"},
			{"source_doc_id": "eng-secret", "title": "eng-secret", "body": "matrix retrieval eng secret", "acl": []string{"group:eng"}, "classification": "secret"},
			{"source_doc_id": "external-labeled", "title": "external-labeled", "body": "matrix retrieval external label", "classification": "public"},
			{"source_doc_id": "unknown-class", "title": "unknown-class", "body": "matrix retrieval unknown class", "classification": "cosmic"},
			{"source_doc_id": "unknown-acl", "title": "unknown-acl", "body": "matrix retrieval unknown acl", "acl": []string{"group:unknown"}, "classification": "public"},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("matrix ingest = %d %s", r.code, r.raw)
	}
	addExternalLabelForTitle(t, h, tenant, kbID, "external-labeled", []string{"purview:confidential"})
	return kbID
}

func addExternalLabelForTitle(t *testing.T, h *harness, tenant model.TenantID, kbID, title string, labels []string) {
	t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		recs, err := listAll(context.Background(), docRepo, eq(colKBRef, kbID), eq(colTitle, title))
		if err != nil {
			return err
		}
		if len(recs) != 1 {
			t.Fatalf("title %q document rows = %d, want 1", title, len(recs))
		}
		return upsertExternalLabel(context.Background(), sc, recs[0].String(model.ColID), kbID, "test", labels)
	}); err != nil {
		t.Fatalf("add external label: %v", err)
	}
}

func queryTitleSet(t *testing.T, h *harness, token string, tenant model.TenantID, kbID string) map[string]bool {
	t.Helper()
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", token, map[string]any{
		"query": "matrix retrieval", "top_k": 20, "agent_ref": "agent-1",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("matrix query = %d %s", r.code, r.raw)
	}
	out := map[string]bool{}
	for _, it := range r.body["results"].([]any) {
		res := it.(map[string]any)
		title, _ := res["title"].(string)
		if title != "" {
			out[title] = true
		}
	}
	return out
}

func TestRetrievalDeniedWhenGuardFails(t *testing.T) {
	// A guard error fails CLOSED (deny) and records a denied lineage row.
	h := newHarnessWith(t, WithRetrievalGuard(errorGuard{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	_ = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d", "body": "anything"}},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{"query": "anything"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("guard error must deny (403), got %d %s", r.code, r.raw)
	}
	// A denied lineage row exists (the forensic record that access was attempted).
	ln := h.do("GET", "/v1/m/knowledge/lineage?decision=denied", editor, nil, tenantHdr(tenant))
	if items, _ := ln.body["items"].([]any); len(items) == 0 {
		t.Error("expected a denied lineage record")
	}
}

func TestRetrievalResidencyMismatchDenied(t *testing.T) {
	// An EU-locked KB cannot be read from a US identity.
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"group:eng"}, Clearance: classSecret, Region: "us",
	}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "eu-kb", "residency_region": "eu"})
	_ = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d", "body": "eu data"}},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{"query": "eu data"}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("residency mismatch must deny (403), got %d %s", r.code, r.raw)
	}
	if !h.hasFinding(findingResidencyViolation) {
		t.Error("expected a knowledge_residency_violation finding")
	}
}

func TestLineageReconstructsOriginToAnswerNoEgress(t *testing.T) {
	h := newHarness(t) // permissive guard, LOCAL embedder
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	_ = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_kind": "notion", "source_doc_id": "n1", "title": "Decisions",
			"body": "We adopted governed retrieval and append-only lineage."}},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{
		"query": "governed retrieval lineage", "agent_ref": "agent-7", "session_ref": "sess-1",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	if eg, _ := r.body["egress"].(bool); eg {
		t.Error("local embedder must yield egress=false")
	}
	lineageID, _ := r.body["lineage_id"].(string)
	if lineageID == "" {
		t.Fatal("query must return a lineage id")
	}
	ln := h.do("GET", "/v1/m/knowledge/lineage/"+lineageID, editor, nil, tenantHdr(tenant))
	if ln.code != http.StatusOK {
		t.Fatalf("get lineage = %d %s", ln.code, ln.raw)
	}
	if ln.body["decision"] != "allowed" {
		t.Errorf("decision = %v", ln.body["decision"])
	}
	if eg, _ := ln.body["egress"].(bool); eg {
		t.Error("lineage.egress must be false for a local embedder (the data did not leave)")
	}
	refs, _ := ln.body["chunk_refs"].([]any)
	if len(refs) == 0 {
		t.Fatal("lineage must record the chunk refs it answered from")
	}
	ref0 := refs[0].(map[string]any)
	if ref0["source_kind"] != "notion" || ref0["doc_ref"] == "" || ref0["content_hash"] == "" {
		t.Errorf("chunk_ref must reconstruct origin (source_kind/doc_ref/content_hash): %v", ref0)
	}
}

func TestRetrievalMultiTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	edA := h.roleToken(admin, tenantA, "a@acme.com", "editor")
	edB := h.roleToken(admin, tenantB, "b@globex.com", "editor")

	kbA := h.mustKB(edA, tenantA, map[string]any{"name": "kb"})
	_ = h.do("POST", "/v1/m/knowledge/kbs/"+kbA+"/ingest", edA, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d", "body": "tenant A confidential"}},
	}, tenantHdr(tenantA))

	// Tenant B, with its own bound token, cannot query tenant A's KB by id.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbA+"/query", edB, map[string]any{"query": "confidential"}, tenantHdr(tenantB))
	if r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant KB query must be 404 (no existence leak), got %d %s", r.code, r.raw)
	}
}

func TestRetrievalEgressRecordedForModelBacked(t *testing.T) {
	// A model_backed KB with an egressing embedder records egress=true + the hashed
	// provider in lineage — the honest record that the query left the perimeter.
	h := newHarnessWith(t, WithEmbedder(egressEmbedder{}), WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"group:eng"}, Clearance: classSecret,
	}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb", "embed_policy": "model_backed"})
	_ = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d", "body": "semantic content here"}},
	}, tenantHdr(tenant))

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{"query": "semantic content"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	if eg, _ := r.body["egress"].(bool); !eg {
		t.Error("model-backed egressing embedder must report egress=true")
	}
	lineageID, _ := r.body["lineage_id"].(string)
	ln := h.do("GET", "/v1/m/knowledge/lineage/"+lineageID, editor, nil, tenantHdr(tenant))
	if eg, _ := ln.body["egress"].(bool); !eg {
		t.Error("lineage.egress must be true for an egressing embedder")
	}
	if ln.body["egress_provider"] == "" || ln.body["egress_provider"] == nil {
		t.Error("lineage must record the (hashed) egress provider")
	}
}

// contains is a tiny substring helper (kept local to the test).
func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
)

// TestSyncToRetrievalE2E is the end-to-end integration test for the full
// sync→retrieval→lineage→OpenLineage flow.
//
// Scenario:
//  1. Ingest 3 docs via a live source:
//     - d1: public (no ACL, no ExternalLabels)
//     - d2: ACL=["group:engineering"] only
//     - d3: ExternalLabels=["purview:confidential"] only
//  2. Query — verify all 3 are returned (identity has group:engineering and
//     LabelClearances=["purview:*"]).
//  3. Configure delta: d2 ACL → ["group:unknown"], d3 deleted.
//  4. Run sync (delta, NOT full reconciliation).
//  5. Query — verify only d1 is returned (d2 excluded by ACL, d3 deleted).
//  6. Verify a distinct lineage row exists for each query.
//  7. Verify the second lineage row records result_count=1.
//
// This test proves the critical invariant: sync changes that tighten ACLs
// and delete documents are immediately reflected in the governed retrieval
// pipeline, and every retrieval is recorded in append-only lineage.
func TestSyncToRetrievalE2E(t *testing.T) {
	initialDocs := []contentsource.Document{
		{
			Source: "e2esrc",
			DocID:  "d1",
			Title:  "Public Doc",
			Body:   "public content e2e overview",
		},
		{
			Source: "e2esrc",
			DocID:  "d2",
			Title:  "Engineering Doc",
			Body:   "engineering content e2e overview",
			ACL:    []string{"group:engineering"},
		},
		{
			Source:         "e2esrc",
			DocID:          "d3",
			Title:          "Purview Doc",
			Body:           "purview confidential content e2e overview",
			ExternalLabels: []string{"purview:confidential"},
		},
	}
	liveSrc := newFakeLiveSource(initialDocs)

	// Wire a guard that grants access to "group:engineering" documents and is
	// cleared for all "purview:*" external labels.
	h := newHarnessWith(t, WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed:         true,
		Groups:          []string{"group:engineering"},
		Clearance:       classSecret,
		LabelClearances: []string{"purview:*"},
	}}))
	// Register the live source AFTER harness construction (AddSource is
	// the post-construction path, same as the composition root).
	h.mod.AddSource("e2esrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-e2e")
	editor := h.roleToken(admin, tenant, "ed@acme-e2e.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "e2e-kb"})

	// ── Step 1: Ingest 3 docs via the live source ──────────────────────────────
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "e2esrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("initial ingest: got %d %s", r.code, r.raw)
	}
	if docs, _ := r.body["documents"].(float64); int(docs) != 3 {
		t.Errorf("initial ingest: want 3 documents, got %v (body: %s)", r.body["documents"], r.raw)
	}

	// ── Step 2: First query — expect all 3 docs returned ──────────────────────
	// d1: no ACL (passes), no ExternalLabels (unrestricted)
	// d2: ACL=["group:engineering"] — identity has group:engineering → passes
	// d3: ExternalLabels=["purview:confidential"] — LabelClearances=["purview:*"] → passes
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor,
		map[string]any{
			"query":     "e2e content overview",
			"top_k":     10,
			"agent_ref": "agent-e2e",
		},
		tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("first query: got %d %s", r.code, r.raw)
	}
	firstCount, _ := r.body["count"].(float64)
	if int(firstCount) != 3 {
		t.Errorf("first query: want count=3 (all docs), got %v (body: %s)", firstCount, r.raw)
	}
	firstLineageID, _ := r.body["lineage_id"].(string)
	if firstLineageID == "" {
		t.Fatal("first query must return a lineage_id")
	}

	// ── Step 3: Configure delta changes ───────────────────────────────────────
	// d2: ACL narrowed to group:unknown (identity no longer matches)
	// d3: deleted from the source
	liveSrc.setACL("d2", contentsource.ACLResult{ACL: []string{"group:unknown"}})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{
					DocRef:     contentsource.DocRef{DocID: "d2"},
					ChangeKind: contentsource.ChangeACL,
				},
				{
					DocRef:     contentsource.DocRef{DocID: "d3"},
					ChangeKind: contentsource.ChangeDeleted,
				},
			},
			ResumeToken: "delta-e2e-1",
		},
	})

	// ── Step 4: Run sync ───────────────────────────────────────────────────────
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "e2esrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}
	if v, _ := r.body["acls_refreshed"].(float64); int(v) != 1 {
		t.Errorf("sync: acls_refreshed = %v, want 1 (d2 ACL change)", v)
	}
	if v, _ := r.body["docs_deleted"].(float64); int(v) != 1 {
		t.Errorf("sync: docs_deleted = %v, want 1 (d3 deleted)", v)
	}
	// Delta sync must NOT trigger full reconciliation.
	if full, _ := r.body["full_reconciliation"].(bool); full {
		t.Error("sync: full_reconciliation must be false for a delta sync")
	}
	if tok, _ := r.body["sync_token_saved"].(bool); !tok {
		t.Error("sync: sync_token_saved must be true")
	}

	// ── Step 5: Second query — expect only d1 ─────────────────────────────────
	// d2: ACL=["group:unknown"] — identity has group:engineering → NO match → excluded
	// d3: deleted by sync → no chunks remain
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor,
		map[string]any{
			"query":     "e2e content overview",
			"top_k":     10,
			"agent_ref": "agent-e2e",
		},
		tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("second query: got %d %s", r.code, r.raw)
	}
	secondCount, _ := r.body["count"].(float64)
	if int(secondCount) != 1 {
		t.Errorf("second query: want count=1 (only d1), got %v (body: %s)", secondCount, r.raw)
	}
	secondLineageID, _ := r.body["lineage_id"].(string)
	if secondLineageID == "" {
		t.Fatal("second query must return a lineage_id")
	}
	if secondLineageID == firstLineageID {
		t.Error("each query must produce a distinct lineage_id")
	}

	// Verify the only result is d1 (the public doc).
	results, _ := r.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("second query: want 1 result item, got %d (body: %s)", len(results), r.raw)
	}
	result0, _ := results[0].(map[string]any)
	if title, _ := result0["title"].(string); title != "Public Doc" {
		t.Errorf("second query result: want title=Public Doc, got %q", title)
	}

	// ── Step 6: Verify lineage rows exist for both queries ────────────────────
	ln1 := h.do("GET", "/v1/m/knowledge/lineage/"+firstLineageID, editor, nil, tenantHdr(tenant))
	if ln1.code != http.StatusOK {
		t.Fatalf("get first lineage: got %d %s", ln1.code, ln1.raw)
	}
	if ln1.body["decision"] != decisionAllowed {
		t.Errorf("first lineage decision = %v, want %q", ln1.body["decision"], decisionAllowed)
	}
	if cnt, _ := ln1.body["result_count"].(float64); int(cnt) != 3 {
		t.Errorf("first lineage result_count = %v, want 3", cnt)
	}
	// First query must reference all 3 docs' chunks.
	if refs, _ := ln1.body["chunk_refs"].([]any); len(refs) != 3 {
		t.Errorf("first lineage chunk_refs len = %d, want 3", len(refs))
	}

	ln2 := h.do("GET", "/v1/m/knowledge/lineage/"+secondLineageID, editor, nil, tenantHdr(tenant))
	if ln2.code != http.StatusOK {
		t.Fatalf("get second lineage: got %d %s", ln2.code, ln2.raw)
	}
	if ln2.body["decision"] != decisionAllowed {
		t.Errorf("second lineage decision = %v, want %q", ln2.body["decision"], decisionAllowed)
	}

	// ── Step 7: Verify second lineage result_count == 1 ───────────────────────
	if cnt, _ := ln2.body["result_count"].(float64); int(cnt) != 1 {
		t.Errorf("second lineage result_count = %v, want 1 (ACL/delete sync enforced)", cnt)
	}
	// Second query references only d1's chunk.
	if refs, _ := ln2.body["chunk_refs"].([]any); len(refs) != 1 {
		t.Errorf("second lineage chunk_refs len = %d, want 1", len(refs))
	}

	// Lineage must record egress=false (local embedder — data did not leave).
	if eg, _ := ln2.body["egress"].(bool); eg {
		t.Error("second lineage egress must be false (local embedder)")
	}
}

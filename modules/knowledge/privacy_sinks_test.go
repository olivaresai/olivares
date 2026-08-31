// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestRetrievalPrivacySinks attacks the retrieval evidence path with raw query PII,
// a recognized secret and returned private content. The public response may contain
// the authorized retrieved marker, but lineage and the signed ledger may retain only
// hashes, classes, references and counts.
func TestRetrievalPrivacySinks(t *testing.T) {
	const (
		queryPII        = "alice.s373-private@example.com"
		rawSecret       = "AKIAIOSFODNN7EXAMPLE"
		retrievedMarker = "AUDIT_PRIVATE_RETRIEVED_CONTENT_CANARY"
	)
	query := "find " + retrievedMarker + " for " + queryPII

	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "privacy-sinks")
	editor := h.roleToken(admin, tenant, "editor@privacy.invalid", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "privacy-sinks"})

	ingest := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{
			"source_doc_id": "private-doc",
			"title":         "Private record",
			"body":          retrievedMarker + " owner " + queryPII + " credential " + rawSecret,
		}},
	}, tenantHdr(tenant))
	if ingest.code != http.StatusOK {
		t.Fatalf("ingest = %d %s", ingest.code, ingest.raw)
	}

	result := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", editor, map[string]any{
		"query": query, "top_k": 5, "agent_ref": "agent-privacy",
	}, tenantHdr(tenant))
	if result.code != http.StatusOK {
		t.Fatalf("query = %d %s", result.code, result.raw)
	}
	if !strings.Contains(result.raw, retrievedMarker) {
		t.Fatalf("test setup did not retrieve the private canary: %s", result.raw)
	}
	lineageID, _ := result.body["lineage_id"].(string)
	if lineageID == "" {
		t.Fatal("query returned no lineage id")
	}

	lineage := h.do("GET", "/v1/m/knowledge/lineage/"+lineageID, editor, nil, tenantHdr(tenant))
	if lineage.code != http.StatusOK {
		t.Fatalf("lineage = %d %s", lineage.code, lineage.raw)
	}
	if got := lineage.body["query_hash"]; got != hashHex(query) {
		t.Fatalf("lineage query_hash = %v, want SHA-256 %s", got, hashHex(query))
	}
	refs, _ := lineage.body["chunk_refs"].([]any)
	if len(refs) == 0 || refs[0].(map[string]any)["content_hash"] == "" {
		t.Fatalf("lineage lacks content-hash provenance: %s", lineage.raw)
	}
	assertNoPrivacyCanary(t, "lineage", lineage.raw, query, queryPII, rawSecret, retrievedMarker)

	ctx := context.Background()
	found := 0
	var sigReport audit.EventSigReport
	err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit log does not expose canonical metadata")
		}
		if err := walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action != "knowledge.retrieval" || ev.TargetID.String() != lineageID {
				return nil
			}
			found++
			blob, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			assertNoPrivacyCanary(t, "audit event", string(blob)+meta, query, queryPII, rawSecret, retrievedMarker)
			if !strings.Contains(meta, hashHex(query)) {
				t.Errorf("audit metadata lacks the query fingerprint: %s", meta)
			}
			if len(ev.Sig) == 0 {
				t.Error("knowledge.retrieval event is not Ed25519-signed")
			}
			return nil
		}); err != nil {
			return err
		}
		var err error
		sigReport, err = audit.VerifyEvents(ctx, sc.Audit(), h.auditPub)
		return err
	})
	if err != nil {
		t.Fatalf("inspect audit ledger: %v", err)
	}
	if found != 1 {
		t.Fatalf("knowledge.retrieval events = %d, want 1", found)
	}
	if !sigReport.OK || sigReport.Events == 0 || sigReport.Events != sigReport.Signed {
		t.Fatalf("signed audit verification failed: %+v", sigReport)
	}
}

func assertNoPrivacyCanary(t *testing.T, sink, got string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(got, canary) {
			t.Fatalf("%s leaked raw privacy canary %q: %s", sink, canary, got)
		}
	}
}

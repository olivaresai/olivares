// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// B-01 — the two context floors the console offered and the engine ignored.
//
// redaction_required was a checkbox the console rendered, the API echoed back as
// `redaction_required: true`, and NOTHING acted on. Three places copied or
// serialized it — the composer ORs it, the query DTO copies it, the REST layer
// marshals it — and a naive grep would have counted all three as consumers.
// excluded_sources was worse: the engine composed it, deduplicated it, returned
// it, and the only three references to it in the module were its own
// construction. The pack loop never asked.
//
// That is worse than not having the controls. The operator ticks the box, the
// console confirms the save, and believes the answer is protected.
//
// These tests assert the floors ACT, and they assert it through the retrieval
// route rather than by calling the composer, because "composed correctly" was
// never the thing that was broken.

// putContextPolicy authors a context policy for a scope.
func (h *harness) putContextPolicy(token string, tenant model.TenantID, body map[string]any) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/knowledge/context-policies", token, body, tenantHdr(tenant))
}

// A tenant-wide policy with redaction_required must redact the text of the items
// a retrieval RETURNS, not merely report the flag back.
// ibanRedactor is a controlled Redactor for the seam: it removes the one shape the
// built-in fallback provably does not, which is what makes the floor observable.
type ibanRedactor struct{}

func (ibanRedactor) Redact(text string) (string, []SensitivityHit) {
	const iban = "ES9121000418450200051332"
	if !strings.Contains(text, iban) {
		return text, nil
	}
	return strings.ReplaceAll(text, iban, "[REDACTED:iban]"),
		[]SensitivityHit{{Class: "pii.financial", Rule: "iban", Count: 1, Severity: "medium"}}
}

func (ibanRedactor) Version() string { return "test.iban.v1" }

func TestRedactionRequiredRedactsTheAnswer(t *testing.T) {
	// newHarness's permissive guard is passed explicitly: newHarnessWith REPLACES the
	// option set, so wiring only the redactor would leave the deny-closed default
	// guard and every retrieval would come back empty — a green-looking test that
	// asserted nothing.
	h := newHarnessWith(t,
		WithRedactor(ibanRedactor{}),
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering", "sre", "product", "hr"}, Clearance: classSecret,
		}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "redfloor")
	editor := h.roleToken(admin, tenant, "ed@redfloor.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	// A chunk written BEFORE the redactor covered this shape — the state that makes
	// the retrieval-side floor worth having at all. With the catalog wired, ingest
	// already minimizes, so a document ingested now carries nothing for the floor to
	// find; what it protects is the corpus already in the store, and that state is
	// reachable only by writing the row directly.
	const iban = "ES9121000418450200051332"
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "payroll", "body": "payroll transfer approved"},
	})
	docs := h.extRecords(tenant, documentKind)
	if len(docs) != 1 {
		t.Fatalf("fixture: want 1 document, got %d", len(docs))
	}
	seed := h.extRecords(tenant, chunkKind)
	if len(seed) != 1 {
		t.Fatalf("fixture: want 1 ingested chunk to copy the embedding from, got %d", len(seed))
	}
	legacy := "payroll legacy transfer " + iban + " approved"
	h.insertExtRaw(tenant, chunkKind, model.Record{
		colKBRef: kbID, colDocRef: docs[0].String(model.ColID), colChunkIndex: int64(1),
		colText: legacy, colTokenCount: int64(len(strings.Fields(legacy))),
		colClassif: "internal", colACL: "[]", colContentHash: "legacy",
		// Reuse the ingested chunk's vector and dimension so the legacy row ranks
		// like a real one: the point of the fixture is its TEXT, not its embedding.
		colEmbedding: seed[0][colEmbedding], colEmbedModel: seed[0].String(colEmbedModel),
		colDim: seed[0].Int(colDim), colIndexed: seed[0][colIndexed],
	})

	// Without the floor the stored text comes back as it is: the control case that
	// proves the fixture carries the value, so a clean answer below is the floor
	// acting and not an empty result.
	before := h.queryKB(editor, tenant, kbID, "payroll")
	if before.code != http.StatusOK {
		t.Fatalf("query = %d %s", before.code, before.raw)
	}
	if !anyContains(resultTexts(before), iban) {
		t.Fatalf("fixture: the stored chunk must carry the IBAN before the floor is authored: %s", before.raw)
	}

	if r := h.putContextPolicy(editor, tenant, map[string]any{
		"scope_kind": "tenant", "scope_ref": tenant.String(), "redaction_required": true,
	}); r.code != http.StatusOK && r.code != http.StatusCreated {
		t.Fatalf("put context policy = %d %s", r.code, r.raw)
	}

	after := h.queryKB(editor, tenant, kbID, "payroll")
	if after.code != http.StatusOK {
		t.Fatalf("query with floor = %d %s", after.code, after.raw)
	}
	texts := resultTexts(after)
	if len(texts) == 0 {
		t.Fatalf("the floor must redact the answer, not empty it: %s", after.raw)
	}
	if anyContains(texts, iban) {
		t.Errorf("redaction_required is authored and the IBAN still came back: %s", after.raw)
	}
	// The effect is reported, so an operator can tell "acted and found nothing"
	// from "did not act" — the distinction these fields lacked entirely.
	if n, _ := after.body["redacted_items"].(float64); n < 1 {
		t.Errorf("the answer must report how many items the floor changed, got %v", after.body["redacted_items"])
	}
}

// excluded_sources must keep the named source out of the answer.
func TestExcludedSourcesKeepsTheSourceOutOfTheAnswer(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "exclfloor")
	editor := h.roleToken(admin, tenant, "ed@exclfloor.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "keepme", "source_kind": "notion", "body": "quarterly revenue summary keepme"},
			{"source_doc_id": "dropme", "source_kind": "confluence", "body": "quarterly revenue summary dropme"},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK && r.code != http.StatusCreated {
		t.Fatalf("ingest = %d %s", r.code, r.raw)
	}

	before := h.queryKB(editor, tenant, kbID, "quarterly")
	if before.code != http.StatusOK {
		t.Fatalf("query = %d %s", before.code, before.raw)
	}
	// Both documents must be present first, or an empty answer later would prove
	// nothing about the floor.
	if !anyContains(resultTexts(before), "dropme") || !anyContains(resultTexts(before), "keepme") {
		t.Fatalf("fixture: both documents must be retrievable before the floor: %s", before.raw)
	}
	// The source the ingest recorded is what the floor names. The two documents
	// come from different sources on purpose: with one shared source, excluding it
	// would correctly remove both and the test could not tell "excluded what I
	// named" from "excluded everything".
	ref := sourceRefOfResultContaining(before, "dropme")
	if ref == "" {
		t.Fatalf("fixture: could not read the source_ref of the document to exclude: %s", before.raw)
	}
	if keep := sourceRefOfResultContaining(before, "keepme"); keep == ref {
		t.Fatalf("fixture: the two documents must come from different sources, both are %q", ref)
	}

	if r := h.putContextPolicy(editor, tenant, map[string]any{
		"scope_kind": "tenant", "scope_ref": tenant.String(),
		"spec": map[string]any{"excluded_sources": []string{ref}},
	}); r.code != http.StatusOK && r.code != http.StatusCreated {
		t.Fatalf("put context policy = %d %s", r.code, r.raw)
	}

	after := h.queryKB(editor, tenant, kbID, "quarterly")
	if after.code != http.StatusOK {
		t.Fatalf("query with floor = %d %s", after.code, after.raw)
	}
	texts := resultTexts(after)
	if anyContains(texts, "dropme") {
		t.Errorf("the excluded source still came back: %s", after.raw)
	}
	// The floor must EXCLUDE, not empty: what was not excluded is still served.
	if !anyContains(texts, "keepme") {
		t.Errorf("the floor removed more than it was asked to: %s", after.raw)
	}
	if n, _ := after.body["excluded_chunks"].(float64); n < 1 {
		t.Errorf("the answer must report how many chunks were excluded, got %v", after.body["excluded_chunks"])
	}
}

// The matcher is exact: an entry that merely looks like a prefix of a source must
// not exclude it. A floor that over-excludes is still doing something nobody
// asked for, and it would be invisible.
func TestExcludedSourceMatchingIsExact(t *testing.T) {
	cases := []struct {
		name     string
		excluded []string
		kind     string
		ref      string
		want     bool
	}{
		{"kind matches", []string{"confluence"}, "confluence", "SPACE-1", true},
		{"ref matches", []string{"SPACE-1"}, "confluence", "SPACE-1", true},
		{"pair matches", []string{"confluence:SPACE-1"}, "confluence", "SPACE-1", true},
		{"other kind does not", []string{"notion"}, "confluence", "SPACE-1", false},
		{"prefix does not", []string{"conf"}, "confluence", "SPACE-1", false},
		{"other space does not", []string{"confluence:SPACE-2"}, "confluence", "SPACE-1", false},
		{"empty entry is ignored", []string{""}, "confluence", "SPACE-1", false},
		{"empty ref is not matched by empty entry", []string{""}, "confluence", "", false},
		{"no floor", nil, "confluence", "SPACE-1", false},
	}
	for _, c := range cases {
		if got := excludedSource(c.excluded, c.kind, c.ref); got != c.want {
			t.Errorf("%s: excludedSource(%v, %q, %q) = %v, want %v", c.name, c.excluded, c.kind, c.ref, got, c.want)
		}
	}
}

// sourceRefOfResultContaining returns the source_ref of the first result whose
// text contains sub, or "".
func sourceRefOfResultContaining(r resp, sub string) string {
	items, _ := r.body["results"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if txt, _ := m["text"].(string); !strings.Contains(txt, sub) {
			continue
		}
		if ref, _ := m["source_ref"].(string); ref != "" {
			return ref
		}
	}
	return ""
}

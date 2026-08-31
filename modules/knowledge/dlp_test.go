// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// ---- DLP test helpers --------------------------------------------------------------

// putDLPRule upserts one DLP rule as the given token and returns the response.
func (h *harness) putDLPRule(token string, tenant model.TenantID, class, action string) resp {
	h.t.Helper()
	return h.do("PUT", "/v1/m/knowledge/dlp/rules", token,
		map[string]any{"class": class, "action": action}, tenantHdr(tenant))
}

// mustPutDLPRule upserts a rule and fails the test unless it was accepted.
func (h *harness) mustPutDLPRule(token string, tenant model.TenantID, class, action string) resp {
	h.t.Helper()
	r := h.putDLPRule(token, tenant, class, action)
	if r.code != http.StatusCreated && r.code != http.StatusOK {
		h.t.Fatalf("put dlp rule %s=%s -> %d %s", class, action, r.code, r.raw)
	}
	return r
}

// queryKB runs a governed retrieval and returns the response.
func (h *harness) queryKB(token string, tenant model.TenantID, kbID, query string) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/query", token,
		map[string]any{"query": query, "top_k": 10}, tenantHdr(tenant))
}

// resultTexts extracts the returned chunk texts of a query response.
func resultTexts(r resp) []string {
	items, _ := r.body["results"].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		txt, _ := m["text"].(string)
		out = append(out, txt)
	}
	return out
}

// anyContains reports whether any string contains sub.
func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ---- rule management ----------------------------------------------------------------

func TestDLPRuleCRUDAndValidation(t *testing.T) {
	h := newHarnessWith(t, permissiveGuardOpt())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")

	// Writing egress policy is admin-tier.
	if r := h.putDLPRule(editor, tenant, "pii.government_id", "deny"); r.code != http.StatusForbidden {
		t.Fatalf("editor PUT dlp rule must be 403, got %d %s", r.code, r.raw)
	}
	if r := h.putDLPRule(viewer, tenant, "pii.government_id", "deny"); r.code != http.StatusForbidden {
		t.Fatalf("viewer PUT dlp rule must be 403, got %d %s", r.code, r.raw)
	}

	// Validation: unknown action and empty class are rejected.
	if r := h.putDLPRule(adminTok, tenant, "pii.government_id", "block"); r.code != http.StatusBadRequest {
		t.Fatalf("action other than allow/deny must be 400, got %d %s", r.code, r.raw)
	}
	if r := h.putDLPRule(adminTok, tenant, "", "deny"); r.code != http.StatusBadRequest {
		t.Fatalf("empty class must be 400, got %d %s", r.code, r.raw)
	}

	// Create (case-insensitive input is normalized) then upsert in place.
	r := h.putDLPRule(adminTok, tenant, "PII.Government_ID", "Deny")
	if r.code != http.StatusCreated {
		t.Fatalf("first put = %d %s, want 201", r.code, r.raw)
	}
	if r.body["class"] != "pii.government_id" || r.body["action"] != "deny" {
		t.Errorf("rule must be normalized lowercase: %s", r.raw)
	}
	ruleID, _ := r.body["id"].(string)

	r2 := h.putDLPRule(adminTok, tenant, "pii.government_id", "allow")
	if r2.code != http.StatusOK {
		t.Fatalf("upsert of the same class = %d %s, want 200", r2.code, r2.raw)
	}
	if id2, _ := r2.body["id"].(string); id2 != ruleID {
		t.Errorf("upsert must update in place, got new id %s (was %s)", id2, ruleID)
	}

	list := h.do("GET", "/v1/m/knowledge/dlp/rules", viewer, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("viewer GET dlp rules = %d %s", list.code, list.raw)
	}
	items := listItems(list)
	if len(items) != 1 || items[0]["action"] != "allow" {
		t.Fatalf("expected exactly 1 rule with action allow after upsert, got %s", list.raw)
	}

	// Delete is admin-tier; removing the rule empties the policy.
	if r := h.do("DELETE", "/v1/m/knowledge/dlp/rules/"+ruleID, editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor DELETE dlp rule must be 403, got %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/knowledge/dlp/rules/"+ruleID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("admin DELETE dlp rule = %d %s, want 204", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/knowledge/dlp/rules", viewer, nil, tenantHdr(tenant)); len(listItems(r)) != 0 {
		t.Errorf("expected no rules after delete, got %s", r.raw)
	}
	if r := h.do("DELETE", "/v1/m/knowledge/dlp/rules/"+ruleID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("deleting a removed rule must be 404, got %d %s", r.code, r.raw)
	}
}

// ---- gate posture -------------------------------------------------------------------

func TestDLPDisabledByDefault(t *testing.T) {
	// NO rules configured => the gate is inert: labeled-PII content retrieves
	// unchanged (a tenant that wants enforcement writes rules).
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "pii", "body": "governed retrieval record SSN-LIKE employee data"},
	})

	r := h.queryKB(editor, tenant, kbID, "governed retrieval employee data")
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	texts := resultTexts(r)
	if len(texts) == 0 || !anyContains(texts, "SSN-LIKE") {
		t.Errorf("with no DLP rules the labeled chunk must return unchanged, got %d results", len(texts))
	}
}

func TestDLPBlocksDeniedClassAtRetrieval(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "pii", "body": "governed retrieval policy SSN-LIKE employee record"},
		{"source_doc_id": "clean", "body": "governed retrieval policy overview for engineering"},
	})

	h.mustPutDLPRule(adminTok, tenant, "pii.government_id", "deny")

	r := h.queryKB(editor, tenant, kbID, "governed retrieval policy")
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	texts := resultTexts(r)
	if len(texts) == 0 {
		t.Fatal("the clean document's chunk must still return")
	}
	if anyContains(texts, "SSN-LIKE") {
		t.Error("a chunk of a denied-class document leaked through the DLP gate")
	}

	// The withholding is evidence: lineage reason + append-only dlp_event (same tx).
	ln := h.do("GET", "/v1/m/knowledge/lineage?kb_id="+kbID, editor, nil, tenantHdr(tenant))
	var lineageRow map[string]any
	for _, it := range listItems(ln) {
		if reason, _ := it["reason"].(string); strings.Contains(reason, "dlp: withheld") {
			lineageRow = it
		}
	}
	if lineageRow == nil {
		t.Fatalf("expected a lineage row with a 'dlp: withheld' reason, got %s", ln.raw)
	}
	if lineageRow["decision"] != "allowed" {
		t.Errorf("a DLP-filtered retrieval is still an ALLOWED retrieval, got %v", lineageRow["decision"])
	}
	if reason, _ := lineageRow["reason"].(string); !strings.Contains(reason, "pii.government_id") {
		t.Errorf("lineage reason must name the triggering classes: %q", reason)
	}

	events := h.extRecords(tenant, dlpEventKind)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 dlp_event row, got %d", len(events))
	}
	ev := events[0]
	if ev.String(colDLPAction) != "filtered" {
		t.Errorf("dlp_event action = %q, want filtered", ev.String(colDLPAction))
	}
	if ev.String(colLineageRef) == "" {
		t.Error("dlp_event must reference the lineage row it annotates")
	}
	if ev.Int(colChunksHeld) < 1 {
		t.Errorf("dlp_event chunks_withheld = %d, want >=1", ev.Int(colChunksHeld))
	}
	if !strings.Contains(ev.String(colDLPClasses), "pii.government_id") {
		t.Errorf("dlp_event classes = %q, want pii.government_id", ev.String(colDLPClasses))
	}

	f := h.mustFinding(findingDLPBlocked)
	if f.Severity != sdkmodel.SeverityMedium {
		t.Errorf("retrieval-side knowledge_dlp_blocked severity = %s, want medium", f.Severity)
	}

	// Flipping the rule to allow restores the chunk (policy, not deletion).
	h.mustPutDLPRule(adminTok, tenant, "pii.government_id", "allow")
	r = h.queryKB(editor, tenant, kbID, "governed retrieval policy")
	if r.code != http.StatusOK {
		t.Fatalf("query after allow = %d %s", r.code, r.raw)
	}
	if !anyContains(resultTexts(r), "SSN-LIKE") {
		t.Error("after an explicit allow rule the labeled chunk must return again")
	}
}

func TestDLPWildcardAndExactPrecedence(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "fin", "body": "payment invoice details IBAN-LIKE account"},
		{"source_doc_id": "gov", "body": "payment invoice details SSN-LIKE holder"},
	})

	// "*" denies any labeled class without an exact rule; the exact allow wins for
	// pii.financial.
	h.mustPutDLPRule(adminTok, tenant, "*", "deny")
	h.mustPutDLPRule(adminTok, tenant, "pii.financial", "allow")

	r := h.queryKB(editor, tenant, kbID, "payment invoice details")
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	texts := resultTexts(r)
	if !anyContains(texts, "IBAN-LIKE") {
		t.Error("the exact allow rule must out-rank the wildcard deny for pii.financial")
	}
	if anyContains(texts, "SSN-LIKE") {
		t.Error("pii.government_id has no exact rule and must fall to the wildcard DENY")
	}
}

func TestDLPDenyClosedForUnscanned(t *testing.T) {
	// NO classifier wired: ingested content has no label. With ANY rule configured
	// the policy is enabled and unscanned content is unprovable => DENIED, and the
	// "*" wildcard deliberately does NOT cover it.
	h := newHarnessWith(t, permissiveGuardOpt())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "governed retrieval content about policies"},
	})

	h.mustPutDLPRule(adminTok, tenant, "*", "allow")
	r := h.queryKB(editor, tenant, kbID, "governed retrieval content")
	if r.code != http.StatusOK {
		t.Fatalf("query = %d %s", r.code, r.raw)
	}
	if n := len(resultTexts(r)); n != 0 {
		t.Fatalf("unscanned content must be withheld even under a '*' allow, got %d chunks", n)
	}

	// Only the explicit "unscanned" opt-out permits it.
	h.mustPutDLPRule(adminTok, tenant, "unscanned", "allow")
	r = h.queryKB(editor, tenant, kbID, "governed retrieval content")
	if r.code != http.StatusOK {
		t.Fatalf("query after unscanned allow = %d %s", r.code, r.raw)
	}
	if n := len(resultTexts(r)); n == 0 {
		t.Error("an explicit {unscanned: allow} rule must restore the chunks")
	}
}

// ---- ingest-time embed-egress gate ---------------------------------------------------

func TestDLPIngestEgressGate(t *testing.T) {
	// An EGRESSING embedder makes the ingest-time embed call an egress: the whole
	// ingest is refused before any content leaves.
	h := newDiscoveryHarness(t, WithEmbedder(egressEmbedder{}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustPutDLPRule(adminTok, tenant, "pii.government_id", "deny")

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "pii", "body": "employee record SSN-LIKE data"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("DLP-denied ingest via an egressing embedder must be 409, got %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "perimeter") {
		t.Errorf("the refusal must state the content never left the perimeter: %s", r.raw)
	}
	if n := len(h.extRecords(tenant, documentKind)); n != 0 {
		t.Errorf("a refused ingest must persist no document rows, found %d", n)
	}
	if n := len(h.extRecords(tenant, chunkKind)); n != 0 {
		t.Errorf("a refused ingest must persist no chunk rows, found %d", n)
	}
	events := h.extRecords(tenant, dlpEventKind)
	if len(events) != 1 || events[0].String(colDLPAction) != "denied_ingest" {
		t.Fatalf("expected exactly 1 denied_ingest dlp_event, got %d", len(events))
	}
	if !strings.Contains(events[0].String(colDLPClasses), "pii.government_id") {
		t.Errorf("dlp_event classes = %q, want pii.government_id", events[0].String(colDLPClasses))
	}
	f := h.mustFinding(findingDLPBlocked)
	if f.Severity != sdkmodel.SeverityHigh {
		t.Errorf("ingest-side knowledge_dlp_blocked severity = %s, want high (content was about to leave)", f.Severity)
	}

	// With the LOCAL (non-egress) embedder the embed call is not an egress: the same
	// ingest succeeds — and the retrieval gate still withholds the denied class.
	h2 := newDiscoveryHarness(t)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "acme")
	adminTok2 := h2.roleToken(admin2, tenant2, "adm@acme.com", "admin")
	editor2 := h2.roleToken(admin2, tenant2, "ed@acme.com", "editor")
	kbID2 := h2.mustKB(editor2, tenant2, map[string]any{"name": "kb"})
	h2.mustPutDLPRule(adminTok2, tenant2, "pii.government_id", "deny")
	h2.mustIngest(editor2, tenant2, kbID2, []map[string]any{
		{"source_doc_id": "pii", "body": "employee record SSN-LIKE data"},
	})
	q := h2.queryKB(editor2, tenant2, kbID2, "employee record data")
	if q.code != http.StatusOK {
		t.Fatalf("query = %d %s", q.code, q.raw)
	}
	if n := len(resultTexts(q)); n != 0 {
		t.Errorf("the retrieval gate must still withhold the denied class locally, got %d chunks", n)
	}
}

func TestDLPIngestRefusedWhenUnclassifiable(t *testing.T) {
	// Egressing embedder + enabled policy + NO classifier: the content cannot be
	// proven safe to egress, so the ingest is refused (deny-closed) — never a
	// silent "could not scan, sent anyway".
	h := newHarnessWith(t, WithEmbedder(egressEmbedder{}), permissiveGuardOpt())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustPutDLPRule(adminTok, tenant, "pii.contact", "deny") // ANY rule enables the gate

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d1", "body": "perfectly ordinary content"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("unclassifiable ingest under an enabled DLP policy must be 409, got %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "unscanned") || !strings.Contains(r.raw, "perimeter") {
		t.Errorf("the refusal must name the unscanned class and the perimeter: %s", r.raw)
	}
	if n := len(h.extRecords(tenant, documentKind)); n != 0 {
		t.Errorf("a refused ingest must persist no document rows, found %d", n)
	}
}

// recordingEgressEmbedder is an egress-declaring embedder that counts Embed
// calls — the probe proving the two-phase ingest never hands ANY content to the
// embedder once a DLP denial occurs anywhere in the batch.
type recordingEgressEmbedder struct {
	local LocalHashEmbedder
	mu    sync.Mutex
	calls int
}

func (e *recordingEgressEmbedder) Embed(ctx context.Context, tenant model.TenantID, texts []string) ([][]float32, string, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.local.Embed(ctx, tenant, texts)
}
func (e *recordingEgressEmbedder) Dim() int           { return e.local.Dim() }
func (e *recordingEgressEmbedder) AllowsEgress() bool { return true }
func (e *recordingEgressEmbedder) ModelRef() string   { return "hosted-embed-model" }
func (e *recordingEgressEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestDLPIngestDenialPrecedesAnyEmbedEgress: the ingest gate is batch-wide and
// concludes BEFORE the first embed call — a denial on the LAST document of a
// batch must leave the embedder untouched (with a per-document gate, documents
// 1..N-1 would already have egressed), so the 409 "no content left the
// perimeter" and the denied_ingest evidence row are literally true.
func TestDLPIngestDenialPrecedesAnyEmbedEgress(t *testing.T) {
	rec := &recordingEgressEmbedder{}
	h := newDiscoveryHarness(t, WithEmbedder(rec))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})
	h.mustPutDLPRule(adminTok, tenant, "pii.government_id", "deny")

	// Doc 1 is clean (allowed); doc 2 carries the denied class. The denial must
	// fire before doc 1 is embedded.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "ok", "body": "perfectly ordinary release notes"},
			{"source_doc_id": "denied", "body": "Employee record SSN-LIKE on file."},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusConflict {
		t.Fatalf("batch with a denied doc must be 409, got %d %s", r.code, r.raw)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("the embedder was called %d time(s) before the DLP denial — content egressed and the refusal message is false", got)
	}
	if n := len(h.extRecords(tenant, documentKind)); n != 0 {
		t.Errorf("a refused ingest must persist no document rows, found %d", n)
	}
	events := h.extRecords(tenant, dlpEventKind, eq(colDLPAction, dlpActionDeniedIngest))
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 denied_ingest dlp_event, got %d", len(events))
	}
}

// TestReingestWithoutClassifierDeletesStaleLabel: a re-ingest that CANNOT
// classify (no classifier wired) must DELETE the document's previous label —
// otherwise a stale "scanned, clean" label keeps vouching for bytes it never
// saw and the deny-closed unscanned rule never applies to the new content.
func TestReingestWithoutClassifierDeletesStaleLabel(t *testing.T) {
	h := newHarnessWith(t, permissiveGuardOpt()) // NO classifier wired
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "first version of the content"},
	})
	if n := len(h.extRecords(tenant, labelKind)); n != 0 {
		t.Fatalf("an unclassified ingest must write no labels, found %d", n)
	}
	docID := h.docIDsBySource(editor, tenant, kbID)["d1"]

	// A stale "scanned, clean" label left behind by an earlier classifier-wired
	// deployment (constructed white-box: the API can no longer produce it).
	h.insertExtRaw(tenant, labelKind, model.Record{
		colSubjectKind: subjectDocument, colSubjectRef: docID, colKBRef: kbID,
		colClasses: "[]", colBasis: basisStored, colContentHash: "stale-fingerprint",
		colDetectorVer: "test.v1", colScannedAt: model.SystemClock{}.Now().String(),
	})

	// DLP enabled; "*" deliberately does NOT cover unscanned content. The stale
	// clean label makes the doc retrievable — that is the hazard.
	h.mustPutDLPRule(adminTok, tenant, "*", "allow")
	if texts := resultTexts(h.queryKB(editor, tenant, kbID, "content")); len(texts) == 0 {
		t.Fatal("premise: with the stale clean label present the chunks are retrievable")
	}

	// Re-ingest the same source_doc_id with NEW content, still unclassifiable:
	// the stale label must be deleted, the doc reverts to unscanned (denied).
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "second version of the content"},
	})
	if n := len(h.extRecords(tenant, labelKind, eq(colSubjectRef, docID))); n != 0 {
		t.Fatalf("the unclassifiable re-ingest must delete the stale label, found %d row(s)", n)
	}
	if texts := resultTexts(h.queryKB(editor, tenant, kbID, "content")); len(texts) != 0 {
		t.Fatalf("unscanned content must be denied at retrieval (deny-closed), got %d chunk(s)", len(texts))
	}
}

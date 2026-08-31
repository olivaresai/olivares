// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// ---- test doubles -----------------------------------------------------------

// markerClassifier is a deterministic SensitivityClassifier over greppable FAKE
// marker substrings (never real PII shapes), so tests can seed and assert classes
// without a raw sensitive value ever appearing in a body, a persisted record or an
// assertion message.
type markerClassifier struct{}

var markerRules = []struct {
	marker, class, rule string
	severity            sdkmodel.Severity
}{
	{"SSN-LIKE", "pii.government_id", "us-ssn", sdkmodel.SeverityMedium},
	{"IBAN-LIKE", "pii.financial", "iban", sdkmodel.SeverityMedium},
	{"SECRET-LIKE", "secret.credential", "key-value-secret", sdkmodel.SeverityHigh},
	{"IP-LIKE", "pii.network", "ipv4", sdkmodel.SeverityLow},
}

func (markerClassifier) Classify(text string) ([]SensitivityHit, error) {
	var hits []SensitivityHit
	for _, mr := range markerRules {
		if n := strings.Count(text, mr.marker); n > 0 {
			hits = append(hits, SensitivityHit{Class: mr.class, Rule: mr.rule, Count: n, Severity: mr.severity})
		}
	}
	// marker-hazard mirrors the REAL catalog's behavior on redaction placeholders:
	// security's key-value-secret rule re-matches "api_key=[REDACTED]" with the
	// placeholder as the value (the premise pinned in cmd/olivares's
	// TestKnowledgeClassifierCatalogFlagsRedactionPlaceholder). Stored chunk text
	// legitimately contains "[REDACTED]" markers, so this rule makes the suite
	// actually exercise neutralizeMarkers: any label produced over stored content
	// must NOT carry this hit — if the module ever classified a marker without
	// neutralizing it first, this rule fires and the label assertions fail.
	if n := strings.Count(text, "[REDACTED"); n > 0 {
		hits = append(hits, SensitivityHit{Class: "secret.credential", Rule: "marker-hazard", Count: n, Severity: sdkmodel.SeverityHigh})
	}
	return hits, nil
}

func (markerClassifier) Version() string { return "test.v1" }

// failingClassifier always errors — the honest-failure path: a scan must fail
// loudly, never silently report "no PII found".
type failingClassifier struct{}

var errClassifierDown = errors.New("classifier backend unavailable")

func (failingClassifier) Classify(string) ([]SensitivityHit, error) {
	return nil, errClassifierDown
}

func (failingClassifier) Version() string { return "test.fail" }

// permissiveGuardOpt grants every group/clearance so retrieval tests exercise ONLY
// the gates, not the classification/ACL filter.
func permissiveGuardOpt() Option {
	return WithRetrievalGuard(fixedGuard{grants: Grants{
		Allowed: true, Groups: []string{"engineering", "hr"}, Clearance: classSecret, Region: "",
	}})
}

// newDiscoveryHarness wires the marker classifier + a permissive guard (plus any
// extra options) — the standard discovery/DLP test rig.
func newDiscoveryHarness(t *testing.T, extra ...Option) *harness {
	t.Helper()
	opts := append([]Option{WithSensitivityClassifier(markerClassifier{}), permissiveGuardOpt()}, extra...)
	return newHarnessWith(t, opts...)
}

// ---- harness helpers --------------------------------------------------------

// waitFindings polls (bus delivery is async) until at least min findings of the
// kind arrived or the deadline passed, returning the matches found.
func (h *harness) waitFindings(kind string, min int) []sdkmodel.FindingReport {
	h.t.Helper()
	var out []sdkmodel.FindingReport
	for i := 0; i < 200; i++ {
		out = out[:0]
		h.findMu.Lock()
		for _, f := range h.findings {
			if f.Kind == kind {
				out = append(out, f)
			}
		}
		h.findMu.Unlock()
		if len(out) >= min {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	return out
}

// mustFinding asserts at least one finding of kind arrived and returns the first.
func (h *harness) mustFinding(kind string) sdkmodel.FindingReport {
	h.t.Helper()
	got := h.waitFindings(kind, 1)
	if len(got) == 0 {
		h.t.Fatalf("expected a %s finding on the bus", kind)
	}
	return got[0]
}

// extRecords reads a module entity's rows white-box — asserting what was PERSISTED.
func (h *harness) extRecords(tenant model.TenantID, kind model.Kind, filters ...model.Filter) []model.Record {
	h.t.Helper()
	var out []model.Record
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		out, err = listAll(context.Background(), repo, filters...)
		return err
	}); err != nil {
		h.t.Fatalf("extRecords(%s): %v", kind, err)
	}
	return out
}

// insertExtRaw writes one module-entity row directly, to construct a state the API
// cannot reach (e.g. a stored corpus under an always-failing classifier).
func (h *harness) insertExtRaw(tenant model.TenantID, kind model.Kind, fields model.Record) string {
	h.t.Helper()
	var id string
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
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
		h.t.Fatalf("insertExtRaw(%s): %v", kind, err)
	}
	return id
}

// mustIngest ingests inline documents and fails the test on a non-200.
func (h *harness) mustIngest(token string, tenant model.TenantID, kbID string, docs []map[string]any) resp {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", token, map[string]any{"documents": docs}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("ingest = %d %s", r.code, r.raw)
	}
	return r
}

// docIDsBySource maps source_doc_id -> document row id via the documents view.
func (h *harness) docIDsBySource(token string, tenant model.TenantID, kbID string) map[string]string {
	h.t.Helper()
	r := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", token, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("list documents = %d %s", r.code, r.raw)
	}
	out := map[string]string{}
	for _, it := range listItems(r) {
		sid, _ := it["source_doc_id"].(string)
		id, _ := it["id"].(string)
		out[sid] = id
	}
	return out
}

// itemWhere returns the first item whose key equals val, or nil.
func itemWhere(items []map[string]any, key, val string) map[string]any {
	for _, it := range items {
		if s, _ := it[key].(string); s == val {
			return it
		}
	}
	return nil
}

// labelClasses extracts the class names of a label DTO, in response order.
func labelClasses(label map[string]any) []string {
	raw, _ := label["classes"].([]any)
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		m, _ := c.(map[string]any)
		cls, _ := m["class"].(string)
		out = append(out, cls)
	}
	return out
}

// ---- discovery at ingest ----------------------------------------------------------

// TestIngestLabelsDocumentsFromStoredBody: ingest classifies the PERSISTED form
// — scrub first, then classify the whole (marker-neutralized) post-scrub body.
// The label's basis is "stored" and its content_hash is the stored-basis
// fingerprint (joined chunk hashes), the SAME derivation the at-rest scan uses,
// so a rescan of unchanged content can never flip the verdict. The marker
// substrings (SSN-LIKE etc.) survive scrub, so they still classify at ingest.
func TestIngestLabelsDocumentsFromStoredBody(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")

	kbID := h.mustKB(editor, tenant, map[string]any{"name": "hr", "classification": "internal"})
	piiBody := "Employee record SSN-LIKE with payroll account IBAN-LIKE on file."
	netBody := "Router IP-LIKE appeared twice in the export: IP-LIKE again."
	cleanBody := "Plain notes about gardening and tea."
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "pii", "title": "Payroll", "body": piiBody},
		{"source_doc_id": "net", "title": "Netlog", "body": netBody},
		{"source_doc_id": "clean", "title": "Garden", "body": cleanBody},
	})
	docIDs := h.docIDsBySource(editor, tenant, kbID)

	r := h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list labels = %d %s", r.code, r.raw)
	}
	items := listItems(r)
	if len(items) != 3 {
		t.Fatalf("expected 3 labels (one per ingested doc, clean included), got %d: %s", len(items), r.raw)
	}

	pii := itemWhere(items, "subject_ref", docIDs["pii"])
	if pii == nil {
		t.Fatal("no label for the PII document")
	}
	if pii["basis"] != "stored" || pii["detector_version"] != "test.v1" || pii["subject_kind"] != "document" {
		t.Errorf("pii label basis/version/kind = %v / %v / %v", pii["basis"], pii["detector_version"], pii["subject_kind"])
	}
	if pii["kb_ref"] != kbID {
		t.Errorf("pii label kb_ref = %v, want %s", pii["kb_ref"], kbID)
	}
	// The fingerprint is the stored-basis hash over the chunk hashes (a short
	// single-paragraph body yields exactly one chunk: its trimmed text) — NOT the
	// raw-body hash. Same unexported derivation the module uses (storedBasisHash).
	if pii["content_hash"] != storedBasisHash([]string{hashHex(strings.TrimSpace(piiBody))}) {
		t.Error("pii label content_hash must be the stored-basis hash of the chunk hashes")
	}
	if pii["content_hash"] == hashHex(piiBody) {
		t.Error("pii label content_hash must not be the raw-body hash (raw basis is source scans only)")
	}
	if got := labelClasses(pii); len(got) != 2 || got[0] != "pii.financial" || got[1] != "pii.government_id" {
		t.Errorf("pii label classes = %v, want [pii.financial pii.government_id] (sorted)", got)
	}
	for _, c := range pii["classes"].([]any) {
		hit := c.(map[string]any)
		if n, _ := hit["count"].(float64); n != 1 {
			t.Errorf("class %v count = %v, want 1", hit["class"], hit["count"])
		}
		if hit["severity"] != "medium" {
			t.Errorf("class %v severity = %v, want medium", hit["class"], hit["severity"])
		}
		if hit["rule"] == "" {
			t.Errorf("class %v must carry its named rule (explainability)", hit["class"])
		}
	}
	if pii["max_severity"] != "medium" {
		t.Errorf("pii label max_severity = %v, want medium", pii["max_severity"])
	}
	if pii["recommended_classification"] != "confidential" {
		t.Errorf("pii label recommended_classification = %v, want confidential", pii["recommended_classification"])
	}

	net := itemWhere(items, "subject_ref", docIDs["net"])
	if net == nil {
		t.Fatal("no label for the network document")
	}
	if got := labelClasses(net); len(got) != 1 || got[0] != "pii.network" {
		t.Errorf("net label classes = %v, want [pii.network]", got)
	}
	if hit := net["classes"].([]any)[0].(map[string]any); hit["count"].(float64) != 2 {
		t.Errorf("pii.network count = %v, want 2 occurrences", hit["count"])
	}
	if net["max_severity"] != "low" || net["recommended_classification"] != "internal" {
		t.Errorf("net label max_severity/recommended = %v / %v, want low / internal",
			net["max_severity"], net["recommended_classification"])
	}

	clean := itemWhere(items, "subject_ref", docIDs["clean"])
	if clean == nil {
		t.Fatal("the scanned-clean document must STILL get a label row (classes=[])")
	}
	if got := labelClasses(clean); len(got) != 0 {
		t.Errorf("clean label classes = %v, want []", got)
	}
	if clean["basis"] != "stored" {
		t.Errorf("clean label basis = %v, want stored", clean["basis"])
	}
	if _, present := clean["max_severity"]; present {
		t.Errorf("clean label must carry no max_severity, got %v", clean["max_severity"])
	}
	if _, present := clean["recommended_classification"]; present {
		t.Errorf("clean label must carry no recommended_classification, got %v", clean["recommended_classification"])
	}

	f := h.mustFinding(findingPIIDiscovered)
	if f.Severity != sdkmodel.SeverityMedium {
		t.Errorf("knowledge_pii_discovered severity = %s, want medium", f.Severity)
	}
	if f.SubjectKind != "knowledge_base" || f.SubjectRef != kbID {
		t.Errorf("finding subject = %s/%s, want knowledge_base/%s", f.SubjectKind, f.SubjectRef, kbID)
	}
}

// ---- at-rest KB scan ---------------------------------------------------------------

func TestScanKBAtRest(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")

	kbID := h.mustKB(editor, tenant, map[string]any{"name": "ops"})
	// Doc 2's secret literal is scrubbed at ingest: the stored text keeps only the
	// "[REDACTED]" marker, so the at-rest scan counts the marker but must NOT label
	// the document secret.credential (the secret no longer exists at rest).
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "pii", "title": "Onboarding", "body": "Employee record SSN-LIKE for onboarding."},
		{"source_doc_id": "secret", "title": "Deploy", "body": "Config api_key=SECRET-LIKE-VALUE-12345 for the deploy."},
	})
	docIDs := h.docIDsBySource(editor, tenant, kbID)

	// Ingest and the at-rest scan now agree BY CONSTRUCTION: both classify the
	// persisted (post-scrub, marker-neutralized) form on the "stored" basis, so
	// the secret doc's ingest label is ALREADY clean of secret.credential — the
	// scrub removed the literal before anything was classified (no raw/stored
	// flip-flop a rescan could exploit). The stored text DOES contain the
	// "[REDACTED]" marker the test classifier's marker-hazard rule flags, so the
	// clean label is also proof the module neutralizes markers before classifying.
	labels := h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant))
	ingestSecret := itemWhere(listItems(labels), "subject_ref", docIDs["secret"])
	if ingestSecret == nil {
		t.Fatal("no ingest label for the secret doc")
	}
	if ingestSecret["basis"] != "stored" {
		t.Errorf("ingest label basis = %v, want stored", ingestSecret["basis"])
	}
	if got := labelClasses(ingestSecret); len(got) != 0 {
		t.Fatalf("ingest label of the secret doc = %v, want [] (the literal was scrubbed before classification)", got)
	}

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("scan = %d %s", r.code, r.raw)
	}
	if sid, _ := r.body["scan_id"].(string); sid == "" {
		t.Error("scan response must carry a scan_id")
	}
	if r.body["scope_kind"] != "kb" || r.body["scope_ref"] != kbID || r.body["basis"] != "stored" {
		t.Errorf("scan scope/basis = %v/%v/%v", r.body["scope_kind"], r.body["scope_ref"], r.body["basis"])
	}
	if n, _ := r.body["docs_scanned"].(float64); n != 2 {
		t.Errorf("docs_scanned = %v, want 2", r.body["docs_scanned"])
	}
	if n, _ := r.body["chunks_scanned"].(float64); n < 2 {
		t.Errorf("chunks_scanned = %v, want >=2", r.body["chunks_scanned"])
	}
	if n, _ := r.body["docs_with_hits"].(float64); n != 1 {
		t.Errorf("docs_with_hits = %v, want 1 (the secret doc is clean at rest)", r.body["docs_with_hits"])
	}
	if n, _ := r.body["redacted_markers"].(float64); n < 1 {
		t.Errorf("redacted_markers = %v, want >=1 (evidence a secret was removed)", r.body["redacted_markers"])
	}
	if r.body["detector_version"] != "test.v1" {
		t.Errorf("detector_version = %v, want test.v1", r.body["detector_version"])
	}
	summary, _ := r.body["hit_summary"].(map[string]any)
	if n, _ := summary["pii.government_id"].(float64); n != 1 {
		t.Errorf("hit_summary = %v, want {pii.government_id: 1}", summary)
	}

	// Labels keep the stored basis; the secret doc's label stays clean of
	// secret.credential — the rescan classified the same persisted form ingest did
	// (and neutralized the "[REDACTED]" marker before classifying).
	labels = h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant))
	items := listItems(labels)
	for _, it := range items {
		if it["basis"] != "stored" {
			t.Errorf("label %v basis = %v, want stored after the at-rest scan", it["subject_ref"], it["basis"])
		}
	}
	storedSecret := itemWhere(items, "subject_ref", docIDs["secret"])
	for _, c := range labelClasses(storedSecret) {
		if c == "secret.credential" {
			t.Error("stored-basis label must not carry secret.credential (the literal was redacted)")
		}
	}
	storedPII := itemWhere(items, "subject_ref", docIDs["pii"])
	if got := labelClasses(storedPII); len(got) != 1 || got[0] != "pii.government_id" {
		t.Errorf("stored-basis pii label classes = %v, want [pii.government_id]", got)
	}

	// The append-only scan evidence is listable (self-audited read).
	scans := h.do("GET", "/v1/m/knowledge/scans", viewer, nil, tenantHdr(tenant))
	if scans.code != http.StatusOK {
		t.Fatalf("list scans = %d %s", scans.code, scans.raw)
	}
	row := itemWhere(listItems(scans), "scope_ref", kbID)
	if row == nil {
		t.Fatal("the scan run must appear in GET /scans")
	}
	if row["scope_kind"] != "kb" || row["basis"] != "stored" || row["detector_version"] != "test.v1" {
		t.Errorf("scan row = %v", row)
	}
	if n, _ := row["redacted_markers"].(float64); n < 1 {
		t.Errorf("scan row redacted_markers = %v, want >=1", row["redacted_markers"])
	}

	// One finding from the ingest (the pii doc's marker persists past scrub) + one
	// from the scan (hits>0).
	if got := h.waitFindings(findingPIIDiscovered, 2); len(got) < 2 {
		t.Errorf("expected 2 knowledge_pii_discovered findings (ingest + scan), got %d", len(got))
	}
}

// ---- source scan without ingest ----------------------------------------------------

func TestScanSourceWithoutIngest(t *testing.T) {
	src := newFakeSource([]contentsource.Document{
		{Source: contentsource.SourceConfluence, DocID: "c1", Title: "Customer", Body: "Customer SSN-LIKE on file."},
		{Source: contentsource.SourceConfluence, DocID: "c2", Title: "Notes", Body: "Plain meeting notes."},
	})
	h := newDiscoveryHarness(t, WithSource("crm", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")

	r := h.do("POST", "/v1/m/knowledge/sources/crm/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("source scan = %d %s", r.code, r.raw)
	}
	if r.body["scope_kind"] != "source" || r.body["scope_ref"] != "crm" || r.body["basis"] != "raw" {
		t.Errorf("scan scope/basis = %v/%v/%v", r.body["scope_kind"], r.body["scope_ref"], r.body["basis"])
	}
	if n, _ := r.body["docs_scanned"].(float64); n != 2 {
		t.Errorf("docs_scanned = %v, want 2", r.body["docs_scanned"])
	}
	if n, _ := r.body["docs_with_hits"].(float64); n != 1 {
		t.Errorf("docs_with_hits = %v, want 1", r.body["docs_with_hits"])
	}

	labels := h.do("GET", "/v1/m/knowledge/labels?subject_kind=source_document", viewer, nil, tenantHdr(tenant))
	items := listItems(labels)
	if len(items) != 2 {
		t.Fatalf("expected 2 source_document labels, got %d: %s", len(items), labels.raw)
	}
	hit := itemWhere(items, "subject_ref", "crm/c1")
	if hit == nil {
		t.Fatal(`source label subject_ref must be "<source>/<doc id>"`)
	}
	if got := labelClasses(hit); len(got) != 1 || got[0] != "pii.government_id" {
		t.Errorf("source label classes = %v, want [pii.government_id]", got)
	}
	if hit["basis"] != "raw" || hit["subject_kind"] != "source_document" {
		t.Errorf("source label basis/kind = %v/%v", hit["basis"], hit["subject_kind"])
	}
	if kb, present := hit["kb_ref"]; present && kb != "" {
		t.Errorf("a source label has no KB; kb_ref = %v", kb)
	}
	if clean := itemWhere(items, "subject_ref", "crm/c2"); clean == nil {
		t.Error("the scanned-clean source doc must still get a label row")
	} else if got := labelClasses(clean); len(got) != 0 {
		t.Errorf("clean source label classes = %v, want []", got)
	}

	scans := h.do("GET", "/v1/m/knowledge/scans?scope_kind=source", viewer, nil, tenantHdr(tenant))
	if row := itemWhere(listItems(scans), "scope_ref", "crm"); row == nil {
		t.Error("the source scan run must appear in GET /scans?scope_kind=source")
	}

	// The scan ingested NOTHING: no document rows, no chunk rows.
	if n := len(h.extRecords(tenant, documentKind)); n != 0 {
		t.Errorf("source scan must not create knowledge_document rows, found %d", n)
	}
	if n := len(h.extRecords(tenant, chunkKind)); n != 0 {
		t.Errorf("source scan must not create knowledge_chunk rows, found %d", n)
	}

	h.mustFinding(findingPIIDiscovered)
}

// ---- refusal paths -----------------------------------------------------------------

func TestScanRefusesWithoutClassifier(t *testing.T) {
	src := newFakeSource([]contentsource.Document{
		{Source: contentsource.SourceConfluence, DocID: "c1", Title: "Doc", Body: "anything"},
	})
	h := newHarnessWith(t, permissiveGuardOpt(), WithSource("crm", src)) // NO classifier
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("KB scan without a classifier must refuse (409), got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/knowledge/sources/crm/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("source scan without a classifier must refuse (409), got %d %s", r.code, r.raw)
	}

	// Ingest still works — but writes NO label rows (unscanned, not "clean").
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "plain ingested content"},
	})
	if n := len(h.extRecords(tenant, labelKind)); n != 0 {
		t.Errorf("ingest without a classifier must persist no label rows, found %d", n)
	}
	if r := h.do("GET", "/v1/m/knowledge/labels", viewer, nil, tenantHdr(tenant)); len(listItems(r)) != 0 {
		t.Errorf("expected no labels via the API either, got %s", r.raw)
	}
}

func TestClassifierErrorFailsScanHonestly(t *testing.T) {
	src := newFakeSource([]contentsource.Document{
		{Source: contentsource.SourceConfluence, DocID: "c1", Title: "Doc", Body: "anything"},
	})
	h := newHarnessWith(t, WithSensitivityClassifier(failingClassifier{}), permissiveGuardOpt(), WithSource("crm", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb"})

	// Seed the stored corpus white-box (the API ingest would hit the failing
	// classifier first; the scan path needs chunks at rest to classify).
	text := "stored chunk text"
	docID := h.insertExtRaw(tenant, documentKind, model.Record{
		colKBRef: kbID, colSourceKind: "inline", colSourceRef: "inline", colSourceDocID: "d1",
		colTitle: "T", colContentType: "text/plain", colClassif: classInternal, colResidency: "",
		colACL: "[]", colContentHash: hashHex(text), colRedactCount: int64(0), colSpaceRef: "",
		colDocChunkCnt: int64(1), colStatus: docIndexed,
	})
	h.insertExtRaw(tenant, chunkKind, model.Record{
		colKBRef: kbID, colDocRef: docID, colChunkIndex: int64(0), colText: text,
		colEmbedModel: LocalHashModelRef, colDim: int64(localEmbedDim), colTokenCount: int64(3),
		colContentHash: hashHex(text), colClassif: classInternal, colACL: "[]", colIndexed: true,
	})

	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusBadGateway {
		t.Fatalf("KB scan with a failing classifier must fail (502), got %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/knowledge/sources/crm/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusBadGateway {
		t.Fatalf("source scan with a failing classifier must fail (502), got %d %s", r.code, r.raw)
	}

	// No scan evidence row was appended and no label was written — never a partial
	// "scan happened" record for a scan that did not.
	if n := len(h.extRecords(tenant, piiScanKind)); n != 0 {
		t.Errorf("a failed scan must persist no knowledge_pii_scan rows, found %d", n)
	}
	if n := len(h.extRecords(tenant, labelKind)); n != 0 {
		t.Errorf("a failed scan must persist no label rows, found %d", n)
	}
}

// ---- whole-document classification (chunk-split evasion) ----------------------------

// TestScanKBDetectsValueStraddlingChunkSplit: chunkText hard-splits an oversized
// paragraph at maxChunkRunes, so a sensitive value can straddle the split — NO
// single chunk contains it. Both ingest (whole-body) and the at-rest scan
// (chunk_index-ordered chunks joined with "") classify the WHOLE document, so the
// rescan never sees less than ingest for unchanged content. Per-chunk
// classification would silently lose this hit.
func TestScanKBDetectsValueStraddlingChunkSplit(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "split"})

	// One single paragraph (no blank lines) of maxChunkRunes+203 runes: the marker
	// starts at rune maxChunkRunes-5, so the hard split at maxChunkRunes leaves
	// "SSN-L" at the tail of chunk 0 and "IKE…" at the head of chunk 1.
	body := strings.Repeat("x", maxChunkRunes-5) + "SSN-LIKE" + strings.Repeat("y", 200)
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "straddle", "title": "Split", "body": body},
	})
	docID := h.docIDsBySource(editor, tenant, kbID)["straddle"]

	// Verify the premise, not just the outcome: 2 chunks, and NEITHER contains the
	// whole marker — otherwise the test stops exercising the straddle.
	chunks := h.extRecords(tenant, chunkKind, eq(colDocRef, docID))
	if len(chunks) != 2 {
		t.Fatalf("expected the oversized paragraph to hard-split into 2 chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if strings.Contains(c.String(colText), "SSN-LIKE") {
			t.Fatal("premise broken: a single chunk contains the whole marker — the body no longer straddles the split")
		}
	}

	// Ingest classified the whole body, so the label carries the straddling hit.
	labels := h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant))
	ingestLabel := itemWhere(listItems(labels), "subject_ref", docID)
	if got := labelClasses(ingestLabel); len(got) != 1 || got[0] != "pii.government_id" {
		t.Fatalf("ingest label classes = %v, want [pii.government_id] (whole-body classification)", got)
	}

	// The at-rest rescan reconstructs the document before classifying: it must
	// still see the straddling value — never less than ingest for unchanged content.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("scan = %d %s", r.code, r.raw)
	}
	if n, _ := r.body["docs_with_hits"].(float64); n != 1 {
		t.Errorf("docs_with_hits = %v, want 1 (the straddling value must not evade the rescan)", r.body["docs_with_hits"])
	}
	labels = h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant))
	scanned := itemWhere(listItems(labels), "subject_ref", docID)
	if got := labelClasses(scanned); len(got) != 1 || got[0] != "pii.government_id" {
		t.Errorf("post-scan label classes = %v, want [pii.government_id] (the rescan saw less than ingest)", got)
	}
}

// ---- KB delete cascade ---------------------------------------------------------------

// TestKBDeleteCascadesLabels: deleting a KB cascades its documents' sensitivity
// labels (current-state metadata goes with its subjects) while the append-only
// pii_scan evidence is retained — discovery HAPPENED, even if the corpus is gone.
func TestKBDeleteCascadesLabels(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	viewer := h.roleToken(admin, tenant, "vw@acme.com", "viewer")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "doomed"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "pii", "body": "Employee record SSN-LIKE on file."},
		{"source_doc_id": "clean", "body": "Plain notes about gardening."},
	})
	if r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/scan", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("scan = %d %s", r.code, r.raw)
	}
	if n := len(listItems(h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant)))); n != 2 {
		t.Fatalf("expected 2 labels before the delete, got %d", n)
	}

	if r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("delete kb = %d %s", r.code, r.raw)
	}

	if r := h.do("GET", "/v1/m/knowledge/labels?kb_id="+kbID, viewer, nil, tenantHdr(tenant)); len(listItems(r)) != 0 {
		t.Errorf("labels must cascade with the KB, got %s", r.raw)
	}
	if n := len(h.extRecords(tenant, labelKind)); n != 0 {
		t.Errorf("expected 0 label rows after the cascade, found %d", n)
	}
	// The scan evidence survives the delete (append-only, deliberately retained).
	scans := h.do("GET", "/v1/m/knowledge/scans", viewer, nil, tenantHdr(tenant))
	if row := itemWhere(listItems(scans), "scope_ref", kbID); row == nil {
		t.Error("the pii_scan evidence row must survive the KB delete")
	}
}

// ---- source-scan boundary ----------------------------------------------------

// auditFeedSource wraps the document fakeSource but declares a NON-document
// content class — an audit-feed double for the boundary check.
type auditFeedSource struct{ contentsource.Source }

func (auditFeedSource) Kind() contentsource.ContentClass { return contentsource.ClassAuditLog }

// TestScanSourceRejectsNonDocumentSource mirrors the ingest-side boundary
// on the scan path: an audit/inventory feed is not knowledge and must not be
// scanned and labeled as source documents.
func TestScanSourceRejectsNonDocumentSource(t *testing.T) {
	src := auditFeedSource{newFakeSource([]contentsource.Document{
		{Source: contentsource.SourceConfluence, DocID: "a1", Title: "Audit", Body: "an audit record"},
	})}
	h := newDiscoveryHarness(t, WithSource("auditfeed", src))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	r := h.do("POST", "/v1/m/knowledge/sources/auditfeed/scan", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("scan of a non-document source = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "not a document content source") || !strings.Contains(r.raw, "inventory feeds") {
		t.Errorf("the refusal must name the boundary: %s", r.raw)
	}
	if n := len(h.extRecords(tenant, labelKind)); n != 0 {
		t.Errorf("a refused source scan must persist no labels, found %d", n)
	}
	if n := len(h.extRecords(tenant, piiScanKind)); n != 0 {
		t.Errorf("a refused source scan must persist no scan rows, found %d", n)
	}
}

// ---- persistScan currency guard (white-box) --------------------------------------------

// pinnedData implements api.ScopedData over the harness store for one tenant —
// the minimal handle persistScan needs when invoked white-box.
type pinnedData struct {
	st     store.Store
	tenant model.TenantID
}

func (d pinnedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.View(ctx, d.tenant, fn)
}

// Export mirrors View: these doubles model a tenant that IS in service, and the
// portability door reaches the same data. Written out rather than panicking so a
// route that legitimately exports keeps working under the double.
func (d pinnedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

func (d pinnedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, fn)
}

// TestPersistScanSkipsStaleLabelUpsert pins the scan↔re-ingest currency guard:
// a KB-scope scan result whose stored-basis hash no longer matches the document's
// CURRENT chunks (the corpus changed while the classification ran outside any
// transaction) must NOT overwrite the fresher label — but the append-only scan
// row still records that the scan happened, and the self-audit counts the skip.
func TestPersistScanSkipsStaleLabelUpsert(t *testing.T) {
	h := newDiscoveryHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "race"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "Employee record SSN-LIKE on file."},
	})
	docID := h.docIDsBySource(editor, tenant, kbID)["d1"]
	before := h.extRecords(tenant, labelKind, eq(colSubjectRef, docID))
	if len(before) != 1 {
		t.Fatalf("expected the ingest label, got %d rows", len(before))
	}

	// A scan outcome classified against chunks that have since been replaced: its
	// recorded content hash mismatches the document's current stored-basis hash.
	m := New(WithSensitivityClassifier(markerClassifier{}))
	outcome := &scanOutcome{docsScanned: 1, chunksScanned: 1}
	outcome.summarize(docID, []classHit{{Class: "pii.financial", Rule: "iban", Count: 1, Severity: "medium"}})
	mc := api.ModuleContext{
		Principal: auth.Principal{Kind: auth.KindUser},
		Tenant:    tenant,
		Data:      pinnedData{st: h.st, tenant: tenant},
	}
	scanID, err := m.persistScan(context.Background(), mc, scanScopeKB, kbID, kbID, basisStored,
		outcome, map[string]string{docID: "stale-mismatched-fingerprint"})
	if err != nil {
		t.Fatalf("persistScan: %v", err)
	}
	if scanID.IsZero() {
		t.Fatal("persistScan must return the appended scan id")
	}

	// The label was NOT overwritten with the stale result.
	after := h.extRecords(tenant, labelKind, eq(colSubjectRef, docID))
	if len(after) != 1 {
		t.Fatalf("expected exactly 1 label row, got %d", len(after))
	}
	if got := after[0].String(colClasses); strings.Contains(got, "pii.financial") {
		t.Errorf("stale scan result overwrote the fresher label: classes = %s", got)
	}
	if got, want := after[0].String(colContentHash), before[0].String(colContentHash); got != want {
		t.Errorf("label content_hash changed: %s -> %s", want, got)
	}

	// The scan row itself WAS appended (the scan happened; evidence is append-only)
	// and the run self-audited. The audit Walk deliberately exposes Meta as nil
	// (sqlstore scanAudit: the canonical string is authoritative), so the skip
	// itself is asserted above via the untouched label — here we only pin that
	// the knowledge.scan event exists.
	scans := h.extRecords(tenant, piiScanKind, eq(colScopeRef, kbID))
	found := false
	for _, s := range scans {
		if s.String(model.ColID) == scanID.String() {
			found = true
		}
	}
	if !found {
		t.Fatal("the stale-skipping scan must still append its evidence row")
	}
	audited := false
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == "knowledge.scan" {
				audited = true
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("audit walk: %v", err)
	}
	if !audited {
		t.Error("the scan must append a knowledge.scan self-audit event")
	}
}

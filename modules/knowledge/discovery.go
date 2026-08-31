// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// PII discovery: minimization (redact-before-store) is not discovery.
// The ingest redactor strips SECRETS + email, so persisted chunk text may
// legitimately carry every other PII shape (SSN, IBAN, cards, NIF/NIE, IPs…).
// Discovery actively scans the data the module governs — at ingest (the only
// moment the RAW body exists), at rest (the stored, redacted chunk text) and
// across configured document-sources BEFORE ingest — and labels each subject
// with explainable sensitivity classes the DLP policy (dlp.go) enforces.
// Everything persisted is minimal-data: classes, named rules, counts, hashes —
// never a matched value (docs/SECURITY-HARDENING.md). A scan is reproducible: the label records
// the basis ("raw" | "stored"), the content hash of that basis and the detector
// catalog version.

// Label subjects (the colSubjectKind column).
const (
	subjectDocument  = "document"        // a stored knowledge_document (ref = row id)
	subjectSourceDoc = "source_document" // a not-ingested source doc (ref = "<source>/<doc id>")
)

// Label bases (the colBasis column).
const (
	basisRaw    = "raw"    // the pre-redaction body (source scan — content not yet minimized)
	basisStored = "stored" // the persisted (post-scrub) form (ingest + KB scan)
)

// Scan scopes (the colScopeKind column on a scan row).
const (
	scanScopeKB     = "kb"
	scanScopeSource = "source"
)

// redactMarkerRe matches the labeled placeholders the ingest redactor leaves
// ("[REDACTED]" / "[REDACTED:rule]"). A scan counts them as evidence that
// secrets WERE found and removed — it must NOT label the document
// secret.credential for them (the secret no longer exists in the stored text;
// over-labeling would deny egress of already-minimized content for no risk).
var redactMarkerRe = regexp.MustCompile(`\[REDACTED(?::[a-z0-9-]+)?\]`)

// neutralizeMarkers replaces every redaction placeholder with a token-breaking
// sentinel (";") before classification. The placeholder cannot simply stay or
// be stripped: "api_key=[REDACTED]" re-matches the catalog's key=value secret
// rule with the placeholder as the value, and removing it outright would let
// the NEXT word be read as the value ("api_key= until…" matches too). ";" is
// excluded from every value charset in the catalog and breaks token adjacency,
// so neither the marker nor its removal can fire a rule.
func neutralizeMarkers(s string) string { return redactMarkerRe.ReplaceAllString(s, ";") }

// classHit is the persisted JSON form of one sensitivity hit on a label row.
type classHit struct {
	Class    string `json:"class"`
	Rule     string `json:"rule"`
	Count    int    `json:"count"`
	Severity string `json:"severity"`
}

// docLabel is the in-memory view of a subject's label the DLP gate consumes.
type docLabel struct {
	classes []string
}

// classify runs the wired sensitivity classifier; a nil classifier returns
// ok=false (the caller refuses or skips honestly — never an empty "no PII").
func (m *Module) classify(text string) (hits []SensitivityHit, ok bool, err error) {
	if m.classifier == nil {
		return nil, false, nil
	}
	h, err := m.classifier.Classify(text)
	if err != nil {
		return nil, true, err
	}
	return h, true, nil
}

// mergeHits accumulates hits into a per-(class|rule) aggregate map.
func mergeHits(into map[string]*classHit, hits []SensitivityHit) {
	for _, h := range hits {
		key := h.Class + "|" + h.Rule
		if cur, ok := into[key]; ok {
			cur.Count += h.Count
			continue
		}
		into[key] = &classHit{Class: h.Class, Rule: h.Rule, Count: h.Count, Severity: string(h.Severity)}
	}
}

// sortedHits flattens an aggregate map deterministically (class, then rule).
func sortedHits(agg map[string]*classHit) []classHit {
	out := make([]classHit, 0, len(agg))
	for _, h := range agg {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// classSet returns the distinct classes of a hit list, sorted.
func classSet(hits []classHit) []string {
	set := map[string]bool{}
	for _, h := range hits {
		set[h.Class] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// maxSeverityOf returns the highest severity among hits ("" for none). Unknown
// severities never out-rank known ones (AtLeast fails closed).
func maxSeverityOf(hits []classHit) string {
	max := ""
	for _, h := range hits {
		s := sdkmodel.Severity(h.Severity)
		if max == "" || s.AtLeast(sdkmodel.Severity(max)) {
			max = h.Severity
		}
	}
	return max
}

// recommendedFor maps detected classes to an ADVISORY classification on the
// existing public<internal<confidential<secret ladder. It is recorded on the
// label and never auto-applied: silently re-classifying content would change
// who can retrieve it behind the operator's back — the DLP policy (explicit,
// audited) is the enforcement path.
func recommendedFor(classes []string) string {
	rec := ""
	for _, c := range classes {
		switch c {
		case "secret.credential", "pii.government_id", "pii.financial":
			return classConfidential
		case "pii.contact", "pii.network":
			rec = classInternal
		}
	}
	return rec
}

// upsertLabel writes the CURRENT sensitivity label of one subject inside the
// caller's transaction (latest scan wins; the basis column says what was
// scanned). hits carries the explainable evidence; an empty list is a real
// result ("scanned, clean") and still writes a row — the DLP gate's unscanned
// rule must not punish verified-clean content.
func (m *Module) upsertLabel(ctx context.Context, sc store.Scope, subjectKind, subjectRef, kbRef, basis, contentHash, version string, hits []classHit) error {
	repo, err := sc.Ext(labelKind)
	if err != nil {
		return err
	}
	classes := classSet(hits)
	fields := model.Record{
		colSubjectKind: subjectKind, colSubjectRef: subjectRef, colKBRef: kbRef,
		colClasses: marshalJSON(hits), colMaxSeverity: maxSeverityOf(hits),
		colRecommended: recommendedFor(classes), colBasis: basis, colContentHash: contentHash,
		colDetectorVer: version, colScannedAt: m.clock.Now().String(),
	}
	existing, ok, err := findOne(ctx, repo, eq(colSubjectKind, subjectKind), eq(colSubjectRef, subjectRef))
	if err != nil {
		return err
	}
	if ok {
		for k, v := range fields {
			existing[k] = v
		}
		_, err = repo.Update(ctx, existing)
		return err
	}
	_, err = repo.Create(ctx, fields)
	return err
}

// deleteLabel removes a subject's sensitivity label inside the caller's
// transaction (idempotent). Used when a subject's content changes WITHOUT being
// classified (re-ingest with no classifier wired): a stale label vouching for
// bytes it never saw must not feed the DLP gate — the subject reverts to
// unscanned (deny-closed under an enabled policy).
func (m *Module) deleteLabel(ctx context.Context, sc store.Scope, subjectKind, subjectRef string) error {
	repo, err := sc.Ext(labelKind)
	if err != nil {
		return err
	}
	existing, ok, err := findOne(ctx, repo, eq(colSubjectKind, subjectKind), eq(colSubjectRef, subjectRef))
	if err != nil || !ok {
		return err
	}
	return repo.Delete(ctx, model.ID(existing.String(model.ColID)))
}

// loadDocLabels reads the document labels of one KB inside an open scope, keyed
// by document id — the map the DLP retrieval gate consumes.
func loadDocLabels(ctx context.Context, sc store.Scope, kbID model.ID) (map[string]docLabel, error) {
	repo, err := sc.Ext(labelKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colSubjectKind, subjectDocument), eq(colKBRef, kbID.String()))
	if err != nil {
		return nil, err
	}
	out := make(map[string]docLabel, len(recs))
	for _, rec := range recs {
		var hits []classHit
		if s := rec.String(colClasses); strings.TrimSpace(s) != "" {
			_ = unmarshalInto(s, &hits)
		}
		out[rec.String(colSubjectRef)] = docLabel{classes: classSet(hits)}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Scan execution.
// ---------------------------------------------------------------------------

// scanOutcome aggregates one scan run for persistence + response.
type scanOutcome struct {
	docsScanned     int
	chunksScanned   int
	docsWithHits    int
	redactedMarkers int
	summary         map[string]int        // class -> total count
	labels          map[string][]classHit // subject_ref -> hits
}

// summarize folds one subject's hits into the run summary.
func (o *scanOutcome) summarize(subjectRef string, hits []classHit) {
	if o.summary == nil {
		o.summary = map[string]int{}
	}
	if o.labels == nil {
		o.labels = map[string][]classHit{}
	}
	o.labels[subjectRef] = hits
	if len(hits) > 0 {
		o.docsWithHits++
	}
	for _, h := range hits {
		o.summary[h.Class] += h.Count
	}
}

// scanResponse is the report a scan endpoint returns (and the shape of the
// appended evidence row): counts + classes, never content.
type scanResponse struct {
	ScanID          string         `json:"scan_id"`
	ScopeKind       string         `json:"scope_kind"`
	ScopeRef        string         `json:"scope_ref"`
	Basis           string         `json:"basis"`
	DocsScanned     int            `json:"docs_scanned"`
	ChunksScanned   int            `json:"chunks_scanned"`
	DocsWithHits    int            `json:"docs_with_hits"`
	HitSummary      map[string]int `json:"hit_summary"`
	RedactedMarkers int            `json:"redacted_markers"`
	DetectorVersion string         `json:"detector_version"`
}

// persistScan writes the labels + the append-only scan row + the self-audit in
// ONE transaction and returns the scan id.
func (m *Module) persistScan(ctx context.Context, mc api.ModuleContext, scopeKind, scopeRef, kbRef, basis string, o *scanOutcome, contentHashes map[string]string) (model.ID, error) {
	var scanID model.ID
	subjectKind := subjectDocument
	if scopeKind == scanScopeSource {
		subjectKind = subjectSourceDoc
	}
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		stale := 0
		for ref, hits := range o.labels {
			if scopeKind == scanScopeKB {
				// Currency guard (scan ↔ re-ingest race): the classification ran
				// outside any transaction, so the document's chunks may have changed
				// since — a concurrent re-ingest already wrote a fresher label, and
				// persisting this result would overwrite newer truth with stale
				// truth. Re-derive the stored-basis hash NOW, in the transaction
				// that writes the label, and skip the subject on mismatch (the scan
				// row still appends — the scan happened; the audit records the skip).
				current, err := currentStoredBasisHash(ctx, sc, ref)
				if err != nil {
					return err
				}
				if current != contentHashes[ref] {
					stale++
					continue
				}
			}
			if err := m.upsertLabel(ctx, sc, subjectKind, ref, kbRef, basis, contentHashes[ref], m.classifier.Version(), hits); err != nil {
				return err
			}
		}
		repo, err := sc.Ext(piiScanKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colScopeKind: scopeKind, colScopeRef: scopeRef, colBasis: basis,
			colDocsScanned: int64(o.docsScanned), colChunksScanned: int64(o.chunksScanned),
			colDocsWithHits: int64(o.docsWithHits), colHitSummary: marshalJSON(o.summary),
			colRedactedSeen: int64(o.redactedMarkers), colDetectorVer: m.classifier.Version(),
			colOccurredAt: m.clock.Now().String(),
		})
		if err != nil {
			return err
		}
		scanID = model.ID(rec.String(model.ColID))
		return auditEvent(ctx, sc, mc, "knowledge.scan", piiScanKind, scanID, map[string]any{
			"scope": scopeKind + ":" + scopeRef, "docs": o.docsScanned, "docs_with_hits": o.docsWithHits,
			"stale_skipped": stale,
		})
	})
	return scanID, err
}

// currentStoredBasisHash re-derives a document's stored-basis fingerprint from
// its CURRENT chunks, inside an open scope (the currency guard above).
func currentStoredBasisHash(ctx context.Context, sc store.Scope, docID string) (string, error) {
	repo, err := sc.Ext(chunkKind)
	if err != nil {
		return "", err
	}
	recs, err := listAll(ctx, repo, eq(colDocRef, docID))
	if err != nil {
		return "", err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Int(colChunkIndex) < recs[j].Int(colChunkIndex) })
	hashes := make([]string, len(recs))
	for i, c := range recs {
		hashes[i] = c.String(colContentHash)
	}
	return storedBasisHash(hashes), nil
}

// emitDiscoveryFinding reports a scan that found persisted/declared PII:
// one finding per scan run, classes + counts hashed into the detail — never a
// value. MEDIUM mirrors the piiShapes severities; a scan finding is a
// data-governance signal, not an active incident.
func (m *Module) emitDiscoveryFinding(ctx context.Context, mc api.ModuleContext, scopeKind, scopeRef string, o *scanOutcome) {
	if o.docsWithHits == 0 {
		return
	}
	m.emitFinding(ctx, mc.Tenant, findingPIIDiscovered, sdkmodel.SeverityMedium, "knowledge_base", scopeRef,
		"PII discovered in governed content ("+scopeKind+" scan)",
		"scope="+scopeKind+":"+scopeRef+" docs_with_hits="+itoa(int64(o.docsWithHits))+" summary="+marshalJSON(o.summary))
}

// handleScanKB runs the at-rest discovery scan over one KB's stored documents:
// classify every chunk's (redacted) text, label each document, count redaction
// markers, append the scan evidence row. Tenant-scoped and bounded by the KB
// chunk ceiling (maxChunksPerKB) — never a cross-tenant sweep.
func (m *Module) handleScanKB(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	if m.classifier == nil {
		writeJSON(w, http.StatusConflict, errorBody("no sensitivity classifier wired; discovery refused (it must never silently report \"no PII\")"))
		return
	}

	// Load the corpus (read): documents + chunks, grouped per document.
	var (
		found     bool
		docs      []model.Record
		chunksByD map[string][]model.Record
	)
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		if _, ok, err := loadKB(r.Context(), sc, id); err != nil || !ok {
			found = ok
			return err
		}
		found = true
		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		if docs, err = listAll(r.Context(), docRepo, eq(colKBRef, id.String())); err != nil {
			return err
		}
		chunkRepo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		chunks, err := listAll(r.Context(), chunkRepo, eq(colKBRef, id.String()))
		if err != nil {
			return err
		}
		chunksByD = make(map[string][]model.Record, len(docs))
		for _, c := range chunks {
			chunksByD[c.String(colDocRef)] = append(chunksByD[c.String(colDocRef)], c)
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	// Classify OUTSIDE any transaction (pure CPU, possibly large corpus). The
	// classification runs per DOCUMENT over its chunk_index-ordered,
	// reconstructed text — never per chunk: a value straddling a chunk split
	// (chunkText hard-splits oversized paragraphs) would evade per-chunk
	// classification, and a rescan must never see LESS than ingest saw for
	// unchanged content. The empty-string join is deliberate: it restores
	// hard-split adjacency exactly; across paragraph-packed boundaries it may
	// create adjacency that never existed, which can only OVER-detect (the safe
	// direction). Markers are counted on the stored text, then neutralized
	// before classification (see neutralizeMarkers).
	outcome := &scanOutcome{docsScanned: len(docs)}
	hashes := make(map[string]string, len(docs))
	for _, doc := range docs {
		docID := doc.String(model.ColID)
		chunks := chunksByD[docID]
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].Int(colChunkIndex) < chunks[j].Int(colChunkIndex) })
		texts := make([]string, len(chunks))
		chunkHashes := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.String(colText)
			chunkHashes[i] = c.String(colContentHash)
			outcome.chunksScanned++
			outcome.redactedMarkers += len(redactMarkerRe.FindAllString(texts[i], -1))
		}
		hits, _, err := m.classify(neutralizeMarkers(strings.Join(texts, "")))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("classification failed: "+err.Error()))
			return
		}
		agg := map[string]*classHit{}
		mergeHits(agg, hits)
		outcome.summarize(docID, sortedHits(agg))
		hashes[docID] = storedBasisHash(chunkHashes)
	}

	scanID, err := m.persistScan(r.Context(), mc, scanScopeKB, id.String(), id.String(), basisStored, outcome, hashes)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitDiscoveryFinding(r.Context(), mc, scanScopeKB, id.String(), outcome)
	writeJSON(w, http.StatusOK, scanResponse{
		ScanID: scanID.String(), ScopeKind: scanScopeKB, ScopeRef: id.String(), Basis: basisStored,
		DocsScanned: outcome.docsScanned, ChunksScanned: outcome.chunksScanned,
		DocsWithHits: outcome.docsWithHits, HitSummary: orEmptySummary(outcome.summary),
		RedactedMarkers: outcome.redactedMarkers, DetectorVersion: m.classifier.Version(),
	})
}

// handleScanSource runs discovery ACROSS a configured document store WITHOUT
// ingesting: it pulls the source's documents (bounded like ingest), classifies
// the RAW bodies in memory and persists labels + the scan evidence — the bodies
// themselves are never stored, logged or embedded (zero egress, minimal data).
func (m *Module) handleScanSource(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if m.classifier == nil {
		writeJSON(w, http.StatusConflict, errorBody("no sensitivity classifier wired; discovery refused (it must never silently report \"no PII\")"))
		return
	}
	src, ok := m.sources[name]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("unknown source: "+name))
		return
	}
	// The boundary, mirrored from ingest: only DOCUMENT content sources
	// are knowledge — an audit/inventory feed must not be scanned and labeled as
	// source documents.
	if src.Kind() != contentsource.ClassDocument {
		writeJSON(w, http.StatusBadRequest, errorBody("source "+name+" is not a document content source (audit / inventory feeds are not knowledge)"))
		return
	}
	docs, msg, code := m.pullFromSource(r.Context(), src)
	if msg != "" {
		writeJSON(w, code, errorBody(msg))
		return
	}

	outcome := &scanOutcome{docsScanned: len(docs)}
	hashes := make(map[string]string, len(docs))
	for _, d := range docs {
		hits, _, err := m.classify(d.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("classification failed: "+err.Error()))
			return
		}
		agg := map[string]*classHit{}
		mergeHits(agg, hits)
		ref := name + "/" + d.DocID
		outcome.summarize(ref, sortedHits(agg))
		hashes[ref] = hashHex(d.Body)
	}

	scanID, err := m.persistScan(r.Context(), mc, scanScopeSource, name, "", basisRaw, outcome, hashes)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitDiscoveryFinding(r.Context(), mc, scanScopeSource, name, outcome)
	writeJSON(w, http.StatusOK, scanResponse{
		ScanID: scanID.String(), ScopeKind: scanScopeSource, ScopeRef: name, Basis: basisRaw,
		DocsScanned: outcome.docsScanned, ChunksScanned: 0,
		DocsWithHits: outcome.docsWithHits, HitSummary: orEmptySummary(outcome.summary),
		DetectorVersion: m.classifier.Version(),
	})
}

// orEmptySummary keeps the response JSON an object (not null) for a clean scan.
func orEmptySummary(s map[string]int) map[string]int {
	if s == nil {
		return map[string]int{}
	}
	return s
}

// ---------------------------------------------------------------------------
// Label + scan views.
// ---------------------------------------------------------------------------

// labelDTO is one subject's current sensitivity label.
type labelDTO struct {
	ID              string     `json:"id"`
	SubjectKind     string     `json:"subject_kind"`
	SubjectRef      string     `json:"subject_ref"`
	KBRef           string     `json:"kb_ref,omitempty"`
	Classes         []classHit `json:"classes"`
	MaxSeverity     string     `json:"max_severity,omitempty"`
	Recommended     string     `json:"recommended_classification,omitempty"`
	Basis           string     `json:"basis"`
	ContentHash     string     `json:"content_hash"`
	DetectorVersion string     `json:"detector_version"`
	ScannedAt       string     `json:"scanned_at"`
}

func toLabelDTO(rec model.Record) labelDTO {
	var hits []classHit
	if s := rec.String(colClasses); strings.TrimSpace(s) != "" {
		_ = unmarshalInto(s, &hits)
	}
	if hits == nil {
		hits = []classHit{}
	}
	return labelDTO{
		ID: rec.String(model.ColID), SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef),
		KBRef: rec.String(colKBRef), Classes: hits, MaxSeverity: rec.String(colMaxSeverity),
		Recommended: rec.String(colRecommended), Basis: rec.String(colBasis),
		ContentHash: rec.String(colContentHash), DetectorVersion: rec.String(colDetectorVer),
		ScannedAt: rec.String(colScannedAt),
	}
}

// handleListLabels lists sensitivity labels, optionally filtered by subject_kind
// and/or kb_id (labels are governance metadata — counts and rules, no content).
func (m *Module) handleListLabels(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("subject_kind")); v != "" {
		q.Filters = append(q.Filters, eq(colSubjectKind, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("kb_id")); v != "" {
		q.Filters = append(q.Filters, eq(colKBRef, v))
	}
	out := listResponse[labelDTO]{Items: []labelDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(labelKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toLabelDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// scanRowDTO is one appended scan evidence row.
type scanRowDTO struct {
	ID              string         `json:"id"`
	ScopeKind       string         `json:"scope_kind"`
	ScopeRef        string         `json:"scope_ref"`
	Basis           string         `json:"basis"`
	DocsScanned     int64          `json:"docs_scanned"`
	ChunksScanned   int64          `json:"chunks_scanned"`
	DocsWithHits    int64          `json:"docs_with_hits"`
	HitSummary      map[string]int `json:"hit_summary"`
	RedactedMarkers int64          `json:"redacted_markers"`
	DetectorVersion string         `json:"detector_version"`
	OccurredAt      string         `json:"occurred_at"`
}

func toScanRowDTO(rec model.Record) scanRowDTO {
	summary := map[string]int{}
	if s := rec.String(colHitSummary); strings.TrimSpace(s) != "" {
		_ = unmarshalInto(s, &summary)
	}
	return scanRowDTO{
		ID: rec.String(model.ColID), ScopeKind: rec.String(colScopeKind), ScopeRef: rec.String(colScopeRef),
		Basis: rec.String(colBasis), DocsScanned: rec.Int(colDocsScanned), ChunksScanned: rec.Int(colChunksScanned),
		DocsWithHits: rec.Int(colDocsWithHits), HitSummary: summary, RedactedMarkers: rec.Int(colRedactedSeen),
		DetectorVersion: rec.String(colDetectorVer), OccurredAt: rec.String(colOccurredAt),
	}
}

// handleListScans lists the append-only discovery scan evidence. Self-audited
// (reading what was scanned and what it found is recon-relevant, the lineage
// precedent).
func (m *Module) handleListScans(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("scope_kind")); v != "" {
		q.Filters = append(q.Filters, eq(colScopeKind, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("scope_ref")); v != "" {
		q.Filters = append(q.Filters, eq(colScopeRef, v))
	}
	out := listResponse[scanRowDTO]{Items: []scanRowDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(piiScanKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toScanRowDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return auditEvent(r.Context(), sc, mc, "knowledge.scan.list", piiScanKind, "", map[string]any{"count": len(out.Items)})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

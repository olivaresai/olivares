// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Document statuses (the colStatus column on a document).
const (
	docIndexed = "indexed" // all chunks embedded and indexed
	docPending = "pending" // chunks written but embedding failed; recoverable via reindex
)

// ingestRequest drives one ingest. Either Source names a registered data connector
// to pull from, or Documents carries inline documents. Both go through the same
// redact → chunk → embed → index pipeline; content is never trusted unredacted.
type ingestRequest struct {
	// Source is the name of a registered contentsource.Source to pull from.
	Source string `json:"source,omitempty"`
	// Documents are inline documents to ingest directly (bounded by maxInlineDocs).
	Documents []inlineDoc `json:"documents,omitempty"`
}

// inlineDoc is one document supplied inline. Its body MAY contain secrets; the
// module redacts before indexing. ACL holds permission references only.
type inlineDoc struct {
	SourceKind     string   `json:"source_kind,omitempty"`
	SourceMode     string   `json:"source_mode,omitempty"`
	SourceDocID    string   `json:"source_doc_id"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body"`
	ContentType    string   `json:"content_type,omitempty"`
	ACL            []string `json:"acl,omitempty"`
	Classification string   `json:"classification,omitempty"`
	SpaceRef       string   `json:"space_ref,omitempty"`
}

// ingestResponse summarizes one ingest run.
type ingestResponse struct {
	Documents       int    `json:"documents"`
	Chunks          int    `json:"chunks"`
	RedactionsTotal int    `json:"redactions_total"`
	Egress          bool   `json:"egress"`
	EmbedModel      string `json:"embed_model"`
}

// preparedDoc is one document after classification + redaction + chunking +
// embedding (all done OUTSIDE the store transaction). Persisting it is pure DB
// work. scanned records whether a sensitivity classifier ran: a scanned
// doc persists a label row even when hits is empty ("scanned, clean"); an
// unscanned doc persists none — under an enabled DLP policy it is then denied
// at retrieval (deny-closed).
type preparedDoc struct {
	doc         contentsource.Document
	sourceMode  string
	classif     string
	acl         []string
	contentHash string // RAW body sha256 (dedup/change detection)
	storedHash  string // stored-basis fingerprint (joined chunk hashes) for the label
	redactions  int
	// removed is what the write-path redactor took OUT (class, rule, count, never a
	// value). It is reported, never fed to the DLP egress gate — see prepareContent.
	removed []SensitivityHit
	scanned bool
	hits    []classHit // classes of the PERSISTED (post-scrub) form
	chunks  []preparedChunk
}

type ingestDocument struct {
	doc        contentsource.Document
	sourceMode string
}

// preparedChunk is one redacted, embedded chunk ready to persist.
type preparedChunk struct {
	index     int
	text      string // already redacted
	embedding []byte // magic-prefixed vector
	tokens    int64
	hash      string
}

// handleIngest ingests content into a knowledge base: it gathers documents (from a
// registered source or inline), REDACTS each body, chunks it, embeds
// the chunks via the wired embedder OUTSIDE the store transaction (the network/
// egress call must never hold the single SQLite writer — the deploy discipline),
// and persists documents + chunks in one transaction. The embed-policy↔egress gate
// (the red line) is re-checked here: a local_only KB will not embed content with an
// egressing embedder — the content never leaves the perimeter.
func (m *Module) handleIngest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req ingestRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// Load the KB (read) to read its policy/defaults before any external work.
	var kb model.Record
	found := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, ok, err := loadKB(r.Context(), sc, id)
		kb, found = rec, ok
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	// RED LINE gate (B3, re-checked at embed-time): a local_only KB must not embed
	// with an egressing embedder, and a model_backed KB must not silently fall back
	// to the non-semantic local embedder. Refuse BEFORE any content leaves.
	policy := kb.String(colEmbedPolicy)
	if policy == embedLocalOnly && m.embedder.AllowsEgress() {
		m.emitFinding(r.Context(), mc.Tenant, findingEgressBlocked, sdkmodel.SeverityHigh, "knowledge_base", id.String(),
			"ingest refused: local_only KB with an egressing embedder", "kb="+id.String()+" embedder="+m.embedder.ModelRef())
		writeJSON(w, http.StatusConflict, errorBody("embed_policy=local_only forbids egress, but the wired embedder sends content out; ingest refused — no content left the perimeter"))
		return
	}
	if policy == embedModelBacked && m.embedder.ModelRef() == LocalHashModelRef {
		writeJSON(w, http.StatusConflict, errorBody("embed_policy=model_backed requires a semantic embedder; only the local-hash fallback is wired; ingest refused"))
		return
	}
	// Residency↔egress gate (B3, defense in depth): a region-locked KB must not embed
	// its content with an out-of-region/undeclared egressing embedder — refuse BEFORE
	// any content leaves the residency boundary.
	if m.residencyEgressForbidden(kb.String(colResidency)) {
		m.emitFinding(r.Context(), mc.Tenant, findingResidencyViolation, sdkmodel.SeverityHigh, "knowledge_base", id.String(),
			"ingest refused: residency-locked KB with an out-of-region egressing embedder",
			"kb="+id.String()+" region="+kb.String(colResidency)+" embedder="+m.embedder.ModelRef())
		writeJSON(w, http.StatusConflict, errorBody(residencyEgressMessage(kb.String(colResidency))+"; ingest refused — no content left the perimeter"))
		return
	}

	// Gather documents OUTSIDE the transaction (a source pull is an external call).
	docs, msg, code := m.gatherDocuments(r.Context(), &req)
	if msg != "" {
		writeJSON(w, code, errorBody(msg))
		return
	}
	if len(docs) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("nothing to ingest: provide documents or a source with content"))
		return
	}

	dataProductID, err := m.enforceDataProductIngest(r.Context(), mc, kb, ingestContentDocuments(docs))
	if err != nil {
		if de, ok := err.(*dataProductHTTPError); ok {
			if de.body != nil {
				writeJSON(w, de.status, de.body)
			} else {
				writeJSON(w, de.status, errorBody(de.msg))
			}
			return
		}
		writeStoreError(w, err)
		return
	}

	// re-ingesting an existing (source, source_doc_id) REPLACES its prior
	// chunks (persistDocuments → replaceChunks) — that replacement is destruction,
	// and an active ("document", <id>) hold vetoes it. Resolve which incoming
	// documents already exist (a read) and gate EACH before any embed call or
	// write: the same two-phase discipline as the DLP gate below — any held
	// document denies the WHOLE request (423) and a gate error denies it (503),
	// both before any side effect. Documents with no prior row are new — nothing
	// is destroyed, no check needed.
	if m.holdGate != nil {
		var existing []string
		if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(documentKind)
			if err != nil {
				return err
			}
			for _, d := range docs {
				rec, ok, err := findOne(r.Context(), repo,
					eq(colKBRef, id.String()), eq(colSourceKind, string(d.doc.Source)), eq(colSourceDocID, d.doc.DocID))
				if err != nil {
					return err
				}
				if ok {
					existing = append(existing, rec.String(model.ColID))
				}
			}
			return nil
		}); err != nil {
			writeStoreError(w, err)
			return
		}
		if m.enforceDocumentHolds(r.Context(), w, mc.Tenant, existing) {
			return
		}
	}

	// Load the tenant's DLP policy (read) BEFORE any embed call: when the wired
	// embedder egresses, embedding IS egress of the content, and the DLP gate
	// must judge it. A policy read failure fails the ingest — the gate
	// never degrades to allow.
	var dlpPol dlpPolicy
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		p, err := loadDLPPolicy(r.Context(), sc)
		dlpPol = p
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	// Phase 1 — prepare EVERY document locally (classify → DLP egress gate →
	// redact → chunk), zero external calls. The batch-wide gate must conclude
	// BEFORE any embed call: with a per-document gate, documents 1..N-1 would
	// already have egressed via the embedder when document N denies, making the
	// refusal message and the append-only evidence false.
	defaultACL := unmarshalStrings(kb, colDefaultACL)
	kbClassif := kb.String(colClassif)
	region := kb.String(colResidency)
	prepared := make([]preparedDoc, 0, len(docs))
	totalChunks, totalRedactions := 0, 0
	for _, d := range docs {
		pd, err := m.prepareContent(d.doc, d.sourceMode, defaultACL, kbClassif, dlpPol)
		if err != nil {
			if de, ok := err.(*dlpDeniedError); ok {
				// DLP egress gate: nothing has been embedded yet — the content never
				// left the perimeter. Record the enforcement (append-only) + finding,
				// then refuse the whole ingest.
				m.recordDLPIngestDenial(r.Context(), mc, id, de)
				writeJSON(w, http.StatusConflict, errorBody("dlp policy denies egress for classes "+strings.Join(de.classes, ",")+"; ingest refused — no content left the perimeter"))
				return
			}
			writeJSON(w, http.StatusBadGateway, errorBody("classification failed: "+err.Error()))
			return
		}
		prepared = append(prepared, pd)
		totalChunks += len(pd.chunks)
		totalRedactions += pd.redactions
	}

	// Phase 2 — embed (the external/egress call), only after every document
	// passed the gates.
	for i := range prepared {
		if err := m.embedPrepared(r.Context(), mc.Tenant, &prepared[i]); err != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("embedding failed: "+err.Error()))
			return
		}
	}

	// Persist documents + chunks in ONE transaction (pure DB work; embedding done).
	if err := m.persistDocuments(r.Context(), mc, id, region, prepared, dataProductID); err != nil {
		if ce, ok := err.(*clientError); ok {
			writeJSON(w, http.StatusBadRequest, errorBody(ce.msg))
			return
		}
		writeStoreError(w, err)
		return
	}

	// AFTER commit: emit the secret-redacted finding ONCE for the whole ingest (B2),
	// carrying only a hashed detail — never the secret (docs/SECURITY-HARDENING.md).
	if totalRedactions > 0 {
		m.emitFinding(r.Context(), mc.Tenant, findingSecretRedacted, sdkmodel.SeverityMedium, "knowledge_base", id.String(),
			"secrets/PII redacted from ingested content before indexing",
			"kb="+id.String()+" redactions="+itoa(int64(totalRedactions)))
	}
	// AFTER commit: report discovered PII that PERSISTS past redaction —
	// the scrub removes secrets+email, the other PII classes remain in the
	// indexed text. One finding per ingest, classes+counts only.
	if persisted := persistedPIIClasses(prepared); len(persisted) > 0 {
		m.emitFinding(r.Context(), mc.Tenant, findingPIIDiscovered, sdkmodel.SeverityMedium, "knowledge_base", id.String(),
			"PII discovered in ingested content (persists in indexed text)",
			"kb="+id.String()+" classes="+strings.Join(persisted, ","))
	}

	writeJSON(w, http.StatusOK, ingestResponse{
		Documents: len(prepared), Chunks: totalChunks, RedactionsTotal: totalRedactions,
		Egress: m.embedder.AllowsEgress(), EmbedModel: m.embedder.ModelRef(),
	})
}

// gatherDocuments resolves the request to a slice of documents to ingest, OUTSIDE
// any transaction. It returns (docs, "", 0) on success or (nil, message, status)
// on a client error.
func (m *Module) gatherDocuments(ctx context.Context, req *ingestRequest) ([]ingestDocument, string, int) {
	if name := strings.TrimSpace(req.Source); name != "" {
		src, ok := m.sources[name]
		if !ok {
			return nil, "unknown source: " + name, http.StatusBadRequest
		}
		// Boundary with the content connectors: only document content is ingested as knowledge.
		if src.Kind() != contentsource.ClassDocument {
			return nil, "source " + name + " is not a document content source (audit / inventory feeds are not knowledge)", http.StatusBadRequest
		}
		docs, msg, code := m.pullFromSource(ctx, src)
		if msg != "" {
			return nil, msg, code
		}
		return ingestDocumentsWithMode(docs, sourceModeForSource(src)), "", 0
	}
	if len(req.Documents) == 0 {
		return nil, "", 0
	}
	if len(req.Documents) > maxInlineDocs {
		return nil, "too many inline documents", http.StatusBadRequest
	}
	docs := make([]ingestDocument, 0, len(req.Documents))
	for _, d := range req.Documents {
		if strings.TrimSpace(d.SourceDocID) == "" {
			return nil, "each document requires source_doc_id", http.StatusBadRequest
		}
		if len(d.Body) > maxBodyBytes {
			return nil, "document body too large", http.StatusBadRequest
		}
		sk := strings.TrimSpace(d.SourceKind)
		if sk == "" {
			sk = "inline"
		}
		docs = append(docs, ingestDocument{
			doc: contentsource.Document{
				Source: contentsource.SourceKind(sk), DocID: d.SourceDocID, Title: d.Title, Body: d.Body,
				ContentType: d.ContentType, ACL: d.ACL, Classification: d.Classification, SpaceRef: d.SpaceRef,
			},
			sourceMode: normalizeSourceMode(d.SourceMode, sourceModeDirect),
		})
	}
	return docs, "", 0
}

func ingestDocumentsWithMode(docs []contentsource.Document, mode string) []ingestDocument {
	out := make([]ingestDocument, len(docs))
	mode = normalizeSourceMode(mode, sourceModeExport)
	for i, doc := range docs {
		out[i] = ingestDocument{doc: doc, sourceMode: mode}
	}
	return out
}

func ingestContentDocuments(docs []ingestDocument) []contentsource.Document {
	out := make([]contentsource.Document, len(docs))
	for i, d := range docs {
		out[i] = d.doc
	}
	return out
}

// pullFromSource drives a data connector's List→Fetch to gather its documents
// (bounded by maxInlineDocs). A registered source is opened/configured by the
// composition root before registration (the module never sees its credentials);
// here the module only reads, honoring ctx. Read-only and external (no tx).
func (m *Module) pullFromSource(ctx context.Context, src contentsource.Source) ([]contentsource.Document, string, int) {
	var docs []contentsource.Document
	cursor := ""
	for len(docs) < maxInlineDocs {
		refs, next, err := src.List(ctx, cursor)
		if err != nil {
			return nil, "source list failed: " + err.Error(), http.StatusBadGateway
		}
		for _, ref := range refs {
			d, err := src.Fetch(ctx, ref.DocID)
			if err != nil {
				// A deterministic per-document skip (binary content, a non-extractable
				// rich document, an over-limit file) is not a batch failure — skip it
				// and keep going. Only a real (transient/operational) fetch error aborts.
				if errors.Is(err, contentsource.ErrSkipDocument) {
					continue
				}
				return nil, "source fetch failed: " + err.Error(), http.StatusBadGateway
			}
			docs = append(docs, d)
			if len(docs) >= maxInlineDocs {
				break
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return docs, "", 0
}

// prepareContent does ALL the local, zero-egress work for one document: resolve
// the effective classification/ACL (inheriting the KB defaults), redact, chunk,
// classify the PERSISTED form and judge the DLP embed gate. No external call
// happens here — handleIngest only starts embedding once EVERY document of the
// batch passed this gate.
//
// The sensitivity classification deliberately runs over the post-scrub body
// (with redaction markers neutralized): that is the only content that ever
// persists or egresses, so it is the basis the DLP gate and the stored label
// must key on. Classifying the RAW body here would (a) deny embed egress for
// classes whose literals scrub provably removes (every business doc carries an
// email) and (b) persist a label that withholds already-minimized chunks at
// retrieval for no risk. What scrub removed is still reported — redactions
// feeds the knowledge_secret_redacted finding.
func (m *Module) prepareContent(d contentsource.Document, sourceMode string, defaultACL []string, kbClassif string, policy dlpPolicy) (preparedDoc, error) {
	classif := normClass(d.Classification)
	if strings.TrimSpace(d.Classification) == "" {
		classif = normClass(kbClassif)
	}
	acl := d.ACL
	if len(acl) == 0 {
		acl = defaultACL
	}
	rawHash := hashHex(d.Body)
	cleanBody, redactions, removedHits := m.scrubWith(d.Body)
	// What the redactor REMOVED is kept as a label, deliberately apart from the
	// stored-basis hits below. Merging them would re-create the very problem the
	// post-scrub basis avoids: the DLP egress gate keys on `hits`, so a document
	// whose IBAN was successfully removed would be denied embed egress for
	// carrying an IBAN it no longer carries. The governance signal is worth
	// keeping ("this document arrived with a card number"); it just must not be
	// mistaken for a residual risk in content that has already been minimized.
	pdRemoved := removedHits

	// Whole-body classification of the persisted form (basis "stored"): the same
	// basis the at-rest scan uses, so a rescan can never flip the gate's verdict
	// for unchanged content. Whole-body (not per-chunk) so a value straddling a
	// chunk split cannot evade detection.
	storedHits, scanned, err := m.classify(neutralizeMarkers(cleanBody))
	if err != nil {
		return preparedDoc{}, err
	}
	agg := map[string]*classHit{}
	mergeHits(agg, storedHits)
	hits := sortedHits(agg)

	// DLP egress gate at embed time: an egressing embedder transmits the chunk
	// texts (the scrubbed body) out of the perimeter. Deny-closed: with DLP
	// enabled, content whose persisted classes the policy denies — or content
	// that CANNOT be classified (no classifier wired) — must not be handed to an
	// egressing embedder.
	if policy.enabled() && m.embedder.AllowsEgress() {
		if !scanned {
			return preparedDoc{}, &dlpDeniedError{classes: []string{dlpClassUnscanned},
				reason: "dlp policy enabled but no sensitivity classifier wired; cannot prove content may egress"}
		}
		if denied := policy.decide(classSet(hits)); len(denied) > 0 {
			return preparedDoc{}, &dlpDeniedError{classes: denied,
				reason: "dlp policy denies egress for detected classes"}
		}
	}

	texts := chunkText(cleanBody)
	pd := preparedDoc{doc: d, sourceMode: normalizeSourceMode(sourceMode, sourceModeExport), classif: classif, acl: acl, contentHash: rawHash,
		redactions: redactions, scanned: scanned, hits: hits, removed: pdRemoved}
	pd.chunks = make([]preparedChunk, len(texts))
	chunkHashes := make([]string, len(texts))
	for i, t := range texts {
		pd.chunks[i] = preparedChunk{index: i, text: t, tokens: tokenCount(t), hash: hashHex(t)}
		chunkHashes[i] = pd.chunks[i].hash
	}
	// The label's content fingerprint is the stored-basis hash — the same
	// derivation the at-rest scan uses (joined chunk hashes), so currency checks
	// compare like with like.
	pd.storedHash = storedBasisHash(chunkHashes)
	return pd, nil
}

// embedPrepared embeds one prepared document's chunk texts (the external/egress
// call, OUTSIDE any store transaction) and attaches the vectors.
func (m *Module) embedPrepared(ctx context.Context, tenant model.TenantID, pd *preparedDoc) error {
	if len(pd.chunks) == 0 {
		return nil // an empty document: a metadata-only row, no chunks
	}
	texts := make([]string, len(pd.chunks))
	for i := range pd.chunks {
		texts[i] = pd.chunks[i].text
	}
	vectors, _, err := m.embedder.Embed(ctx, tenant, texts)
	if err != nil {
		return err
	}
	if len(vectors) != len(texts) {
		return errEmbedCount
	}
	for i := range pd.chunks {
		pd.chunks[i].embedding = encodeEmbedding(vectors[i])
	}
	return nil
}

// storedBasisHash derives the stored-basis content fingerprint of a document
// from its chunks' content hashes (index order). Ingest and the at-rest scan
// MUST share this derivation: it is how a label proves which persisted content
// it describes.
func storedBasisHash(chunkHashes []string) string {
	return hashHex(strings.Join(chunkHashes, ","))
}

// persistDocuments upserts the prepared documents and replaces their chunks in one
// transaction, then advances the KB counts and self-audits. Re-ingesting the same
// (source, source_doc_id) upserts the document and REPLACES its chunks (no
// duplicates). It enforces the honest chunk ceiling of the store-backed index.
func (m *Module) persistDocuments(ctx context.Context, mc api.ModuleContext, kbID model.ID, region string, prepared []preparedDoc, dataProductID model.ID) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		kbRepo, err := sc.Ext(baseKind)
		if err != nil {
			return err
		}
		kb, err := kbRepo.Get(ctx, kbID)
		if err != nil {
			return err
		}
		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		chunkRepo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}

		existingChunks := kb.Int(colChunkCount)
		addedChunks, addedDocs := int64(0), int64(0)
		for _, pd := range prepared {
			docID, replaced, err := m.upsertDocument(ctx, docRepo, kbID, region, pd)
			if err != nil {
				return err
			}
			if !replaced {
				addedDocs++
			}
			// Persist the document's sensitivity label in the SAME transaction
			//: the label reflects the PERSISTED (post-scrub) form this
			// ingest classified — the same basis the at-rest scan writes. An
			// unscanned re-ingest (no classifier wired) DELETES any previous
			// label: the content just changed and the old label would otherwise
			// vouch "clean"/"scanned" for bytes it never saw — the gate must see
			// the doc as unscanned (deny-closed), never as stale-clean.
			if pd.scanned {
				if err := m.upsertLabel(ctx, sc, subjectDocument, docID.String(), kbID.String(),
					basisStored, pd.storedHash, m.classifier.Version(), pd.hits); err != nil {
					return err
				}
			} else if err := m.deleteLabel(ctx, sc, subjectDocument, docID.String()); err != nil {
				return err
			}
			// Persist external sensitivity labels from the source system.
			// A re-ingest that removes labels (empty ExternalLabels) deletes the old
			// row so no stale label restricts the freshly ingested content.
			if len(pd.doc.ExternalLabels) > 0 {
				if err := upsertExternalLabel(ctx, sc, docID.String(), kbID.String(),
					string(pd.doc.Source), pd.doc.ExternalLabels); err != nil {
					return err
				}
			} else if err := deleteExternalLabel(ctx, sc, docID.String()); err != nil {
				return err
			}
			removed, err := m.replaceChunks(ctx, chunkRepo, kbID, docID, region, pd)
			if err != nil {
				return err
			}
			addedChunks += int64(len(pd.chunks)) - removed
			if existingChunks+addedChunks > maxChunksPerKB {
				return &clientError{"knowledge base would exceed the store-backed index ceiling; wire an external vector backend for a corpus this large"}
			}
		}
		kb[colDocCount] = kb.Int(colDocCount) + addedDocs
		kb[colChunkCount] = existingChunks + addedChunks
		if _, err := kbRepo.Update(ctx, kb); err != nil {
			return err
		}
		if err := m.markDataProductIngested(ctx, sc, dataProductID); err != nil {
			return err
		}
		return auditEvent(ctx, sc, mc, "knowledge.ingest", baseKind, kbID, map[string]any{
			"documents": len(prepared), "chunks_added": addedChunks, "embed_model": m.embedder.ModelRef(), "egress": m.embedder.AllowsEgress(),
		})
	})
}

// upsertDocument creates or updates the document row for one prepared document and
// returns its id and whether it replaced an existing row.
func (m *Module) upsertDocument(ctx context.Context, repo store.GenericRepo, kbID model.ID, region string, pd preparedDoc) (model.ID, bool, error) {
	status := docIndexed
	if len(pd.chunks) == 0 && strings.TrimSpace(pd.doc.Body) != "" {
		status = docPending
	}
	fields := model.Record{
		colKBRef: kbID.String(), colSourceKind: string(pd.doc.Source), colSourceRef: string(pd.doc.Source),
		colSourceMode: pd.sourceMode, colSourceDocID: pd.doc.DocID, colTitle: pd.doc.Title, colContentType: defaultContentType(pd.doc.ContentType),
		colClassif: pd.classif, colResidency: region, colACL: marshalStrings(pd.acl),
		colContentHash: pd.contentHash, colRedactCount: int64(pd.redactions), colSpaceRef: pd.doc.SpaceRef,
		colDocChunkCnt: int64(len(pd.chunks)), colStatus: status,
	}
	existing, ok, err := findOne(ctx, repo,
		eq(colKBRef, kbID.String()), eq(colSourceKind, string(pd.doc.Source)), eq(colSourceDocID, pd.doc.DocID))
	if err != nil {
		return "", false, err
	}
	if ok {
		for k, v := range fields {
			existing[k] = v
		}
		if _, err := repo.Update(ctx, existing); err != nil {
			return "", false, err
		}
		return model.ID(existing.String(model.ColID)), true, nil
	}
	rec, err := repo.Create(ctx, fields)
	if err != nil {
		return "", false, err
	}
	return model.ID(rec.String(model.ColID)), false, nil
}

// replaceChunks deletes a document's existing chunks and writes the prepared ones,
// returning how many old chunks were removed (so the KB count can be adjusted).
// DESTRUCTIVE: the caller must have cleared the document hold-gate
// (enforceDocumentHolds in handleIngest) before reaching this tx — an existing
// document's chunks are evidence a hold preserves.
func (m *Module) replaceChunks(ctx context.Context, repo store.GenericRepo, kbID, docID model.ID, region string, pd preparedDoc) (int64, error) {
	old, err := listAll(ctx, repo, eq(colDocRef, docID.String()))
	if err != nil {
		return 0, err
	}
	for _, c := range old {
		if err := repo.Delete(ctx, model.ID(c.String(model.ColID))); err != nil {
			return 0, err
		}
	}
	aclJSON := marshalStrings(pd.acl)
	for _, ch := range pd.chunks {
		if _, err := repo.Create(ctx, model.Record{
			colKBRef: kbID.String(), colDocRef: docID.String(), colChunkIndex: int64(ch.index),
			colText: ch.text, colEmbedding: ch.embedding, colEmbedModel: m.embedder.ModelRef(), colDim: int64(m.embedder.Dim()),
			colTokenCount: ch.tokens, colContentHash: ch.hash, colClassif: pd.classif, colACL: aclJSON, colIndexed: true,
		}); err != nil {
			return 0, err
		}
	}
	return int64(len(old)), nil
}

// handleReindex re-embeds a KB's chunks that have no embedding yet (the recovery
// path for a partial ingest where the embed call failed). It is tenant-scoped
// (a module cannot enumerate tenants for a background sweep), embeds OUTSIDE
// the transaction, and persists the vectors in a short second transaction.
func (m *Module) handleReindex(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	// Load pending chunks (read).
	var pending []model.Record
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colKBRef, id.String()), eq(colIndexed, false))
		pending = recs
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if len(pending) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"reindexed": 0})
		return
	}
	// Embed OUTSIDE the transaction.
	texts := make([]string, len(pending))
	for i, c := range pending {
		texts[i] = c.String(colText)
	}
	vectors, _, err := m.embedder.Embed(r.Context(), mc.Tenant, texts)
	if err != nil || len(vectors) != len(pending) {
		writeJSON(w, http.StatusBadGateway, errorBody("embedding failed"))
		return
	}
	// Persist the vectors (short tx).
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		for i, c := range pending {
			c[colEmbedding] = encodeEmbedding(vectors[i])
			c[colEmbedModel] = m.embedder.ModelRef()
			c[colDim] = int64(m.embedder.Dim())
			c[colIndexed] = true
			if _, err := repo.Update(r.Context(), c); err != nil {
				return err
			}
		}
		return auditEvent(r.Context(), sc, mc, "knowledge.reindex", baseKind, id, map[string]any{"chunks": len(pending)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reindexed": len(pending)})
}

// persistedPIIClasses returns the distinct sensitivity classes detected across
// an ingest. Hits are classified on the post-scrub body, so every class here
// names values that actually PERSIST into the indexed text (what scrub removed
// is reported separately via knowledge_secret_redacted).
func persistedPIIClasses(prepared []preparedDoc) []string {
	set := map[string]bool{}
	for _, pd := range prepared {
		for _, h := range pd.hits {
			set[h.Class] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// recordDLPIngestDenial appends the append-only DLP enforcement event for a
// refused ingest (best-effort — the 409 returns regardless, a lost evidence row
// is surfaced) and emits the HIGH finding: content was about to egress via the
// embedder and the policy stopped it.
func (m *Module) recordDLPIngestDenial(ctx context.Context, mc api.ModuleContext, kbID model.ID, de *dlpDeniedError) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return m.writeDLPEvent(ctx, sc, kbID.String(), dlpActionDeniedIngest, de.classes, 0, "", "", de.reason)
	}); err != nil {
		m.errorf("knowledge: failed to record dlp ingest denial", "kb", kbID.String(), "err", err)
	}
	m.emitFinding(ctx, mc.Tenant, findingDLPBlocked, sdkmodel.SeverityHigh, "knowledge_base", kbID.String(),
		"ingest refused: DLP policy denies egress of detected classes",
		"kb="+kbID.String()+" classes="+strings.Join(de.classes, ","))
}

// errEmbedCount is returned when the embedder returns a different number of
// vectors than texts (a contract violation — never silently dropped).
var errEmbedCount = &clientError{"embedder returned a wrong number of vectors"}

// defaultContentType returns a sane content type when the source did not provide one.
func defaultContentType(ct string) string {
	if strings.TrimSpace(ct) == "" {
		return "text/plain"
	}
	return ct
}

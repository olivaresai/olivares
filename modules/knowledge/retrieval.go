// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Lineage decisions (the colDecision column).
const (
	decisionAllowed = "allowed"
	decisionDenied  = "denied"
)

// queryRequest is the governed retrieval body AND the internal carrier for the
// pipeline's lineage/deny records. The body agent_ref is still ACCEPTED (so existing
// clients that send it are not rejected by the strict decoder) but is deliberately
// IGNORED for authorization: handleQuery never forwards it, and the effective agent is
// taken solely from the authenticated identity (mc.Principal.AgentIdentity). A caller
// must not name a privileged agent and borrow its clearance (F-03). Internally
// query.go reuses this struct as the lineage carrier, setting AgentRef to the
// authenticated agent so the lineage row records the real identity.
type queryRequest struct {
	Query      string `json:"query"`
	TopK       int    `json:"top_k,omitempty"`
	AgentRef   string `json:"agent_ref,omitempty"` // accepted but IGNORED for authz (F-03)
	SessionRef string `json:"session_ref,omitempty"`
}

// queryResult is one retrieved chunk. The text is the REDACTED chunk content
// (the only form that ever existed in the store).
type queryResult struct {
	ChunkID        string  `json:"chunk_id"`
	DocumentID     string  `json:"document_id"`
	SourceKind     string  `json:"source_kind"`
	SourceRef      string  `json:"source_ref"`
	SourceMode     string  `json:"source_mode"`
	Title          string  `json:"title"`
	Text           string  `json:"text"`
	Classification string  `json:"classification"`
	Score          float64 `json:"score"`
}

// queryResponse is the governed retrieval response. embed_model + egress are
// surfaced so a caller always knows whether the vectors are semantic and whether
// the query left the perimeter.
type queryResponse struct {
	LineageID            string        `json:"lineage_id"`
	Results              []queryResult `json:"results"`
	Count                int           `json:"count"`
	EmbedModel           string        `json:"embed_model"`
	Egress               bool          `json:"egress"`
	ContextBudgetTokens  int64         `json:"context_budget_tokens"`
	ContextStrategy      string        `json:"context_strategy"`
	ContextWinningScope  string        `json:"context_winning_scope"`
	ContextTruncated     bool          `json:"context_truncated"`
	ContextDroppedChunks int           `json:"context_dropped_chunks"`
	RedactionRequired    bool          `json:"redaction_required"`
	// B-01: what the two context floors actually DID. Reporting the flag
	// without reporting its effect is how a control that applies nothing looks
	// identical to one that applies something and finds nothing.
	ExcludedSources []string `json:"excluded_sources,omitempty"`
	ExcludedChunks  int      `json:"excluded_chunks"`
	RedactedItems   int      `json:"redacted_items"`
}

// chunkRef is one entry of a lineage record's chunk_refs: enough to reconstruct
// origin→answer in a single read (B5), never any payload/text.
type chunkRef struct {
	ChunkID     string `json:"chunk_id"`
	KBRef       string `json:"kb_ref"`
	DocRef      string `json:"doc_ref"`
	SourceKind  string `json:"source_kind"`
	SourceRef   string `json:"source_ref"`
	SourceMode  string `json:"source_mode"`
	ContentHash string `json:"content_hash"`
}

// candidate bundles a chunk's record with its decoded vector for ranking.
type candidate struct {
	rec model.Record
	vec []float32
}

// handleQuery is the REST handler for governed retrieval; it delegates to Query().
func (m *Module) handleQuery(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := chi.URLParam(r, "id")
	var req queryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := m.Query(r.Context(), mc, QueryRequest{
		KBID: id, Query: req.Query, TopK: req.TopK,
		SessionRef: req.SessionRef,
	})
	if err != nil {
		qe, ok := IsQueryError(err)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
			return
		}
		switch qe.Kind {
		case QueryErrBadRequest:
			writeJSON(w, http.StatusBadRequest, errorBody(qe.Message))
		case QueryErrNotFound:
			writeJSON(w, http.StatusNotFound, errorBody(qe.Message))
		case QueryErrDenied:
			writeJSON(w, http.StatusForbidden, errorBody(qe.Message))
		case QueryErrConflict:
			writeJSON(w, http.StatusConflict, errorBody(qe.Message))
		default:
			writeJSON(w, http.StatusBadGateway, errorBody(qe.Message))
		}
		return
	}
	writeJSON(w, http.StatusOK, queryResponse{
		LineageID: result.LineageID, Results: toInternalResults(result.Results),
		Count: result.Count, EmbedModel: result.EmbedModel, Egress: result.Egress,
		ContextBudgetTokens: result.ContextBudgetTokens, ContextStrategy: result.ContextStrategy,
		ContextWinningScope: result.ContextWinningScope, ContextTruncated: result.ContextTruncated,
		ContextDroppedChunks: result.ContextDroppedChunks, RedactionRequired: result.RedactionRequired,
		ExcludedSources: result.ExcludedSources, ExcludedChunks: result.ExcludedChunks,
		RedactedItems: result.RedactedItems,
	})
}

// toInternalResults converts the exported QueryResultItem to the internal REST DTO.
func toInternalResults(items []QueryResultItem) []queryResult {
	out := make([]queryResult, len(items))
	for i, item := range items {
		out[i] = queryResult{
			ChunkID: item.ChunkID, DocumentID: item.DocumentID, SourceKind: item.SourceKind,
			SourceRef: item.SourceRef, SourceMode: item.SourceMode,
			Title: item.Title, Text: item.Text,
			Classification: item.Classification, Score: item.Score,
		}
	}
	return out
}

// dlpStats records what the DLP gate withheld from one retrieval: the count and
// the classes that triggered (for the lineage reason, the append-only event and
// the finding — classes, never content).
type dlpStats struct {
	withheld int64
	classes  map[string]bool
}

// classList returns the triggering classes, sorted.
func (s dlpStats) classList() []string {
	out := make([]string, 0, len(s.classes))
	for c := range s.classes {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// loadCandidates loads every INDEXED chunk of a KB and the KB's documents, keeping
// only the chunks the identity may see (classification + ACL) AND whose
// sensitivity the DLP policy lets egress, and returns them with decoded
// vectors. This is the governance filter, applied BEFORE ranking: an external
// vector backend never receives a chunk the caller may not retrieve. The DLP
// check is per-document (chunks inherit their document's label, conservatively):
// with the policy enabled, a denied class withholds the chunk and an UNLABELED
// document is unprovable — denied unless the explicit "unscanned" rule allows it.
func (m *Module) loadCandidates(ctx context.Context, sc store.Scope, kbID model.ID, grants Grants, policy dlpPolicy, labels map[string]docLabel, extLabels map[string][]string) ([]candidate, map[string]model.Record, dlpStats, error) {
	stats := dlpStats{classes: map[string]bool{}}
	chunkRepo, err := sc.Ext(chunkKind)
	if err != nil {
		return nil, nil, stats, err
	}
	recs, err := listAll(ctx, chunkRepo, eq(colKBRef, kbID.String()), eq(colIndexed, true))
	if err != nil {
		return nil, nil, stats, err
	}
	out := make([]candidate, 0, len(recs))
	for _, rec := range recs {
		if !classificationAllowed(rec.String(colClassif), grants.Clearance) {
			continue
		}
		if !aclAllows(unmarshalStrings(rec, colACL), grants.Groups) {
			continue
		}
		if policy.enabled() {
			label, scanned := labels[rec.String(colDocRef)]
			if !scanned {
				if policy.unscannedDenied() {
					stats.withheld++
					stats.classes[dlpClassUnscanned] = true
					continue
				}
			} else if denied := policy.decide(label.classes); len(denied) > 0 {
				stats.withheld++
				for _, c := range denied {
					stats.classes[c] = true
				}
				continue
			}
		}
		// External label gate (deny-closed): a chunk whose document carries
		// source-system sensitivity labels is visible only when the identity's
		// LabelClearances cover at least one label. Documents with no external labels
		// are not affected (the gate is opt-in per document, not a required-pass gate).
		if docLabels := extLabels[rec.String(colDocRef)]; len(docLabels) > 0 {
			if !externalLabelsAllowed(docLabels, grants.LabelClearances) {
				continue
			}
		}
		vec, derr := decodeEmbedding(rec.Bytes(colEmbedding))
		if derr != nil {
			continue // a malformed/pending vector is not a candidate (never a panic)
		}
		out = append(out, candidate{rec: rec, vec: vec})
	}
	// Load the KB's documents for result/lineage enrichment.
	docRepo, err := sc.Ext(documentKind)
	if err != nil {
		return nil, nil, stats, err
	}
	docRecs, err := listAll(ctx, docRepo, eq(colKBRef, kbID.String()))
	if err != nil {
		return nil, nil, stats, err
	}
	docByID := make(map[string]model.Record, len(docRecs))
	for _, d := range docRecs {
		docByID[d.String(model.ColID)] = d
	}
	return out, docByID, stats, nil
}

// lineageRow is the data for one append-only lineage record. dlpWithheld /
// dlpClasses carry the enforcement evidence: when >0, writeLineage appends
// the dlp_event row in the same transaction.
type lineageRow struct {
	kbRef                model.ID
	agentRef             string
	sessionRef           string
	queryHash            string
	chunkRefs            []chunkRef
	sourceRefs           []string
	region               string
	decision             string
	reason               string
	egress               bool
	resultCount          int
	dlpWithheld          int64
	dlpClasses           []string
	contextTruncated     bool
	contextDroppedChunks int
	contextBudget        int64
	contextWinningScope  string
}

// writeLineage appends one immutable lineage row + a self-audit in one transaction
// and returns the row id. egress is recorded with the (hashed) provider when the
// query legitimately left the perimeter; for an in-perimeter (local) embedder it is
// hard false — the record that PROVES the customer's data did not leave.
func (m *Module) writeLineage(ctx context.Context, mc api.ModuleContext, lr lineageRow) (model.ID, error) {
	var id model.ID
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(lineageKind)
		if err != nil {
			return err
		}
		egressProvider := ""
		if lr.egress {
			egressProvider = hashHex(m.embedder.ModelRef())
		}
		// The chunk refs are the red-line evidence (origin→answer). Marshal them
		// explicitly and FAIL the lineage write on error rather than silently
		// recording "null" — a governed retrieval without complete lineage must not
		// stand (docs/SECURITY-HARDENING.md).
		chunkRefsJSON, err := json.Marshal(lr.chunkRefs)
		if err != nil {
			return err
		}
		fields := model.Record{
			colKBRef: lr.kbRef.String(), colAgentRef: lr.agentRef, colSessionRef: lr.sessionRef,
			colQueryHash: lr.queryHash, colChunkRefs: string(chunkRefsJSON), colSourceRefs: marshalStrings(lr.sourceRefs),
			colResidency: lr.region, colDecision: lr.decision, colReason: lr.reason,
			colEgress: lr.egress, colEgressProvider: egressProvider, colResultCount: int64(lr.resultCount),
			colOccurredAt: m.clock.Now().String(),
		}
		rec, err := repo.Create(ctx, fields)
		if err != nil {
			return err
		}
		id = model.ID(rec.String(model.ColID))
		// the DLP enforcement evidence rides the SAME transaction as the
		// lineage it annotates — an enforcement without its evidence must not stand.
		if lr.dlpWithheld > 0 {
			if err := m.writeDLPEvent(ctx, sc, lr.kbRef.String(), dlpActionFiltered, lr.dlpClasses,
				lr.dlpWithheld, lr.agentRef, id.String(), lr.reason); err != nil {
				return err
			}
		}
		return auditEvent(ctx, sc, mc, "knowledge.retrieval", lineageKind, id, map[string]any{
			"kb": lr.kbRef.String(), "decision": lr.decision, "results": lr.resultCount, "egress": lr.egress,
			"query_hash": lr.queryHash, "dlp_classes": lr.dlpClasses,
			"context_truncated": lr.contextTruncated, "context_dropped_chunks": lr.contextDroppedChunks,
			"context_budget": lr.contextBudget, "context_winning_scope": lr.contextWinningScope,
		})
	})
	return id, err
}

// denyQuery records a denied retrieval as an append-only lineage row (the forensic
// evidence that a denied access was attempted) and optionally emits a finding. It
// is best-effort on the lineage write: the 403 returns regardless, but a lost
// denial record is surfaced.
func (m *Module) denyQuery(ctx context.Context, mc api.ModuleContext, kbID model.ID, req queryRequest, queryHash, region, reason, finding string, sev sdkmodel.Severity) {
	if _, err := m.writeLineage(ctx, mc, lineageRow{
		kbRef: kbID, agentRef: req.AgentRef, sessionRef: req.SessionRef, queryHash: queryHash,
		region: region, decision: decisionDenied, reason: reason, egress: false, resultCount: 0,
	}); err != nil {
		m.errorf("knowledge: failed to record denied-retrieval lineage", "kb", kbID.String(), "err", err)
	}
	if finding != "" {
		m.emitFinding(ctx, mc.Tenant, finding, sev, "knowledge_base", kbID.String(), "retrieval denied", reason)
	}
}

// docMeta returns a document's source kind, source ref and title (empty for a nil
// record).
func docMeta(doc model.Record) (sourceKind, sourceRef, sourceMode, title string) {
	if doc == nil {
		return "", "", sourceModeExport, ""
	}
	return doc.String(colSourceKind), doc.String(colSourceRef), recordSourceMode(doc), doc.String(colTitle)
}

// isRegionLocked reports whether a residency region restricts retrieval ("" and
// "global" are unrestricted).
func isRegionLocked(region string) bool {
	r := strings.ToLower(strings.TrimSpace(region))
	return r != "" && r != "global"
}

// reasonOr returns reason if non-empty, else the fallback.
func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) != "" {
		return reason
	}
	return fallback
}

// emptyAsNone renders an empty region as "(none)" for a message.
func emptyAsNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// keys returns the keys of a set, for the lineage source_refs list.
func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

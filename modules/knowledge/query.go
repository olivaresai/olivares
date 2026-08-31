// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// QueryRequest is the programmatic entry point for governed retrieval, usable
// without HTTP. The MCP retrieval upstream (cmd/olivares) calls this; the REST
// handler delegates to it. The effective agent identity is deliberately NOT a
// field here: it is taken SOLELY from the authenticated principal
// (mc.Principal.AgentIdentity), never a caller-declared agent_ref — a non-agent
// principal must not be able to name a privileged agent and borrow its clearance
// (F-03).
type QueryRequest struct {
	KBID       string
	Query      string
	TopK       int
	SessionRef string
}

// QueryResultItem is one retrieved chunk (the REDACTED text, classification, and
// similarity score).
type QueryResultItem struct {
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

// QueryResult is the governed retrieval result.
type QueryResult struct {
	LineageID            string            `json:"lineage_id"`
	Results              []QueryResultItem `json:"results"`
	Count                int               `json:"count"`
	EmbedModel           string            `json:"embed_model"`
	Egress               bool              `json:"egress"`
	ContextBudgetTokens  int64             `json:"context_budget_tokens"`
	ContextStrategy      string            `json:"context_strategy"`
	ContextWinningScope  string            `json:"context_winning_scope"`
	ContextTruncated     bool              `json:"context_truncated"`
	ContextDroppedChunks int               `json:"context_dropped_chunks"`
	RedactionRequired    bool              `json:"redaction_required"`
	// ExcludedSources echoes the floor that was APPLIED (B-01). It used to be
	// returned by a field nothing consumed; it is returned now because the caller
	// can see which sources were kept out of its answer.
	ExcludedSources []string `json:"excluded_sources,omitempty"`
	// ExcludedChunks counts the chunks the excluded_sources floor kept out, and
	// RedactedItems the returned items the redaction floor changed. Both exist so
	// an operator can tell "the control acted and found nothing" from "the control
	// did not act" — the exact distinction this pair of fields lacked while they
	// were dead metadata.
	ExcludedChunks int `json:"excluded_chunks"`
	RedactedItems  int `json:"redacted_items"`
}

// QueryError classifies retrieval failures for the caller. The MCP upstream
// translates Kind into an appropriate JSON-RPC or isError response.
type QueryError struct {
	Kind    QueryErrorKind
	Message string
}

func (e *QueryError) Error() string { return e.Message }

// QueryErrorKind distinguishes retrieval failure modes.
type QueryErrorKind int

const (
	QueryErrBadRequest  QueryErrorKind = iota // invalid input (missing query, bad id)
	QueryErrNotFound                          // KB not found
	QueryErrDenied                            // governance denied (guard, scope, residency)
	QueryErrConflict                          // policy conflict (egress, residency)
	QueryErrUnavailable                       // upstream/store/embed failure
)

func queryErr(kind QueryErrorKind, msg string) *QueryError {
	return &QueryError{Kind: kind, Message: msg}
}

// Query runs the governed retrieval pipeline programmatically. It executes the
// EXACT same pipeline as the REST handleQuery: egress/residency gates → data
// product quality gate → grants → scope-gate → residency → embed →
// classification+ACL+DLP filter BEFORE ranking → lineage.
// Every denied retrieval still writes an append-only lineage row.
func (m *Module) Query(ctx context.Context, mc api.ModuleContext, req QueryRequest) (*QueryResult, error) {
	id, ok := idParam(strings.TrimSpace(req.KBID))
	if !ok {
		return nil, queryErr(QueryErrBadRequest, "invalid kb_id")
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, queryErr(QueryErrBadRequest, "query is required")
	}
	if len(query) > maxQueryLen {
		return nil, queryErr(QueryErrBadRequest, "query too long")
	}
	topK := req.TopK
	if topK <= 0 {
		topK = 10
	}
	if topK > maxTopK {
		topK = maxTopK
	}
	// F-03: the effective agent identity is ONLY the authenticated agent
	// (mc.Principal.AgentIdentity) — never a caller-declared agent_ref. A non-agent
	// principal (e.g. a human token on the REST /query route) that named a privileged
	// agent would otherwise borrow that agent's clearance/scope from the retrieval
	// guard (which resolves clearance from the agent row, not the actor). With no
	// authenticated agent identity there is no agent subject, so the guard grants only
	// public, unrestricted content (deny-closed for classified/ACL'd). A verified
	// human-acts-as-agent (act-as/OBO) path is a separate capability.
	agentRef := strings.TrimSpace(mc.Principal.AgentIdentity)

	var kb model.Record
	found := false
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		rec, fnd, err := loadKB(ctx, sc, id)
		kb, found = rec, fnd
		return err
	}); err != nil {
		return nil, queryErr(QueryErrUnavailable, "store error")
	}
	if !found {
		return nil, queryErr(QueryErrNotFound, "knowledge base not found")
	}
	kbName := kb.String(colName)
	region := kb.String(colResidency)
	queryHash := hashHex(query)

	qr := queryRequest{Query: query, TopK: topK, AgentRef: agentRef, SessionRef: req.SessionRef}

	if kb.String(colEmbedPolicy) == embedLocalOnly && m.embedder.AllowsEgress() {
		m.denyQuery(ctx, mc, id, qr, queryHash, region,
			"local_only KB: the query would egress via the wired embedder", findingEgressBlocked, sdkmodel.SeverityHigh)
		return nil, queryErr(QueryErrConflict, "embed_policy=local_only forbids egress, but the wired embedder would send the query out; retrieval refused")
	}

	if m.residencyEgressForbidden(region) {
		m.denyQuery(ctx, mc, id, qr, queryHash, region,
			"residency-locked KB: the query would egress to an out-of-region embedder", findingResidencyViolation, sdkmodel.SeverityHigh)
		return nil, queryErr(QueryErrConflict, residencyEgressMessage(region)+"; retrieval refused")
	}

	dataProductID, dpErr := m.enforceDataProductQualityGate(ctx, mc, kb, qr, queryHash, region)
	if dpErr != nil {
		return nil, dpErr
	}

	grants, gerr := m.guard.Resolve(ctx, mc.Tenant, mc.Principal.Actor(), agentRef, kbName)
	if gerr != nil {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, "retrieval guard unavailable (fail closed)", "", "")
		return nil, queryErr(QueryErrDenied, "retrieval denied: authorization unavailable")
	}
	if !grants.Allowed {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, reasonOr(grants.Reason, "not permitted to read this knowledge base"), "", "")
		return nil, queryErr(QueryErrDenied, "retrieval denied")
	}

	if allowed, reason, serr := m.scopeGate.Allowed(ctx, mc.Tenant, mc.Principal, agentRef, id.String()); serr != nil {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, "source scope unavailable (fail closed)", "", "")
		return nil, queryErr(QueryErrDenied, "retrieval denied: source scope unavailable")
	} else if !allowed {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, reasonOr(reason, "agent is out of the knowledge base's scope"), "", "")
		return nil, queryErr(QueryErrDenied, "retrieval denied: out of scope")
	}

	pol, perr := m.Apply(ctx, mc.Tenant, ContextPolicyQuery{Principal: mc.Principal, AgentRef: agentRef, KBRef: id.String()})
	if perr != nil {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, "context policy unavailable (fail closed)", "", "")
		return nil, queryErr(QueryErrDenied, "retrieval denied: context policy unavailable")
	}
	if pol.Deny {
		m.denyQuery(ctx, mc, id, qr, queryHash, region, pol.DenyReason, findingContextDenied, sdkmodel.SeverityMedium)
		return nil, queryErr(QueryErrDenied, "retrieval denied: "+pol.DenyReason)
	}

	if isRegionLocked(region) && grants.Region != region {
		m.denyQuery(ctx, mc, id, qr, queryHash, region,
			"residency mismatch: KB region "+region+" vs identity region "+emptyAsNone(grants.Region),
			findingResidencyViolation, sdkmodel.SeverityHigh)
		return nil, queryErr(QueryErrDenied, "retrieval denied: data residency")
	}

	vectors, _, err := m.embedder.Embed(ctx, mc.Tenant, []string{query})
	if err != nil || len(vectors) != 1 {
		return nil, queryErr(QueryErrUnavailable, "embedding failed")
	}
	qvec := vectors[0]

	var (
		candidates []candidate
		docByID    map[string]model.Record
		dlp        dlpStats
		extLabels  map[string][]string
	)
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		policy, perr := loadDLPPolicy(ctx, sc)
		if perr != nil {
			return perr
		}
		var labels map[string]docLabel
		if policy.enabled() {
			var lerr error
			if labels, lerr = loadDocLabels(ctx, sc, id); lerr != nil {
				return lerr
			}
		}
		var lerr error
		if extLabels, lerr = loadExternalLabels(ctx, sc, id); lerr != nil {
			return lerr
		}
		cands, docs, stats, cerr := m.loadCandidates(ctx, sc, id, grants, policy, labels, extLabels)
		candidates, docByID, dlp = cands, docs, stats
		return cerr
	}); err != nil {
		return nil, queryErr(QueryErrUnavailable, "store error")
	}

	vc := make([]VectorCandidate, len(candidates))
	idxByID := make(map[string]model.Record, len(candidates))
	for i, c := range candidates {
		cid := c.rec.String(model.ColID)
		vc[i] = VectorCandidate{ChunkID: cid, Vector: c.vec}
		idxByID[cid] = c.rec
	}
	scored, err := m.index.Rank(ctx, qvec, vc, topK)
	if err != nil {
		return nil, queryErr(QueryErrUnavailable, "ranking failed")
	}

	results := make([]QueryResultItem, 0, len(scored))
	refs := make([]chunkRef, 0, len(scored))
	srcSet := map[string]bool{}
	var includedTokens, droppedTokens int64
	contextTruncated := false
	droppedChunks := 0
	// Counted so the two floors are VISIBLE in the answer and in the lineage: a
	// control that acts silently is only marginally better than one that does not
	// act, because nobody can tell the difference from outside.
	excludedChunks := 0
	redactedItems := 0
	for i, s := range scored {
		rec := idxByID[s.ChunkID]
		if rec == nil {
			continue
		}
		chunkTokens := rec.Int(colTokenCount)
		if pol.MaxContextTokens > 0 && includedTokens+chunkTokens > pol.MaxContextTokens {
			if len(results) == 0 {
				contextTruncated = true
			} else {
				contextTruncated = true
				droppedChunks, droppedTokens = droppedContextBudget(scored[i:], idxByID)
				break
			}
		}
		docID := rec.String(colDocRef)
		doc := docByID[docID]
		sk, sref, sourceMode, title := docMeta(doc)
		// B-01: excluded_sources is an operator-authored context floor. It was
		// composed, persisted, returned to the console and read by NOBODY, so an
		// operator who excluded a source kept receiving its chunks. A source is
		// matched by its kind ("confluence"), by its ref, or by the exact
		// "kind:ref" pair, so an operator can exclude a whole connector or one
		// space of it without a second field to author.
		if excludedSource(pol.ExcludedSources, sk, sref) {
			excludedChunks++
			continue
		}
		text := rec.String(colText)
		// B-01: redaction_required was a checkbox the console offered, the API
		// echoed back as true, and nothing acted on. The chunk text is already
		// minimized at INGEST; this floor is the operator asking for a second,
		// stricter pass on what leaves in an ANSWER — so it runs the same catalog
		// over the outgoing text. Unwired it falls back to the built-in shapes,
		// never to nothing.
		if pol.RedactionRequired {
			cleaned, _, hits := m.scrubWith(text)
			if len(hits) > 0 || cleaned != text {
				redactedItems++
			}
			text = cleaned
		}
		results = append(results, QueryResultItem{
			ChunkID: s.ChunkID, DocumentID: docID, SourceKind: sk, SourceRef: sref, SourceMode: sourceMode, Title: title,
			Text: text, Classification: rec.String(colClassif), Score: s.Score,
		})
		refs = append(refs, chunkRef{
			ChunkID: s.ChunkID, KBRef: id.String(), DocRef: docID, SourceKind: sk, SourceRef: sref, SourceMode: sourceMode,
			ContentHash: rec.String(colContentHash),
		})
		srcSet[docID] = true
		includedTokens += chunkTokens
	}
	if contextTruncated && len(results) == 1 && droppedChunks == 0 && len(scored) > 1 {
		droppedChunks, droppedTokens = droppedContextBudget(scored[1:], idxByID)
	}

	reason := ""
	if dlp.withheld > 0 {
		reason = "dlp: withheld " + itoa(dlp.withheld) + " chunk(s); classes: " + strings.Join(dlp.classList(), ",")
	}
	if contextTruncated {
		if reason != "" {
			reason += "; "
		}
		reason += "context: truncated budget=" + itoa(pol.MaxContextTokens) +
			" dropped_chunks=" + itoa(int64(droppedChunks)) +
			" dropped_tokens=" + itoa(droppedTokens) +
			" winning=" + pol.WinningScope
	}

	// scan each result's text as untrusted data BEFORE returning it to
	// the caller. High-severity injection markers → deny-closed block; low/medium
	// → advisory finding only (precedent). This runs after DLP (which
	// already filtered sensitivity labels) so the lineage records BOTH passes.
	if m.contentScanner != nil {
		filtered := make([]QueryResultItem, 0, len(results))
		filteredRefs := make([]chunkRef, 0, len(refs))
		filteredSrc := map[string]bool{}
		var scanBlocked int64
		for i, r := range results {
			v := m.contentScanner.ScanChunk(ctx, r.Text, r.SourceKind, r.SourceRef)
			if v.Blocked {
				scanBlocked++
				continue
			}
			filtered = append(filtered, r)
			if i < len(refs) {
				filteredRefs = append(filteredRefs, refs[i])
			}
			filteredSrc[r.DocumentID] = true
		}
		if scanBlocked > 0 {
			if reason != "" {
				reason += "; "
			}
			reason += "s264-scan: withheld " + itoa(scanBlocked) + " chunk(s)"
			m.emitFinding(ctx, mc.Tenant, findingInjectionBlocked,
				sdkmodel.SeverityHigh, "knowledge_base", id.String(),
				"content scan withheld chunks with injection markers from retrieval",
				"kb="+id.String()+" withheld="+itoa(scanBlocked))
		}
		results = filtered
		refs = filteredRefs
		srcSet = filteredSrc
	}

	lr := lineageRow{
		kbRef: id, agentRef: agentRef, sessionRef: req.SessionRef, queryHash: queryHash,
		chunkRefs: refs, sourceRefs: keys(srcSet), region: region, decision: decisionAllowed,
		reason: reason, egress: m.embedder.AllowsEgress(), resultCount: len(results),
		dlpWithheld: dlp.withheld, dlpClasses: dlp.classList(),
		contextTruncated: contextTruncated, contextDroppedChunks: droppedChunks,
		contextBudget: pol.MaxContextTokens, contextWinningScope: pol.WinningScope,
	}
	lineageID, err := m.writeLineage(ctx, mc, lr)
	if err != nil {
		m.errorf("knowledge: retrieval lineage write failed; refusing to return ungoverned results", "kb", id.String(), "err", err)
		return nil, queryErr(QueryErrUnavailable, "lineage write failed")
	}
	m.emitOpenLineage(ctx, mc, lr, lineageID.String())
	if dlp.withheld > 0 {
		m.emitFinding(ctx, mc.Tenant, findingDLPBlocked, sdkmodel.SeverityMedium, "knowledge_base", id.String(),
			"DLP policy withheld chunks from a retrieval",
			"kb="+id.String()+" withheld="+itoa(dlp.withheld)+" classes="+strings.Join(dlp.classList(), ","))
	}
	if contextTruncated {
		m.emitFinding(ctx, mc.Tenant, findingContextTruncated, sdkmodel.SeverityLow, "knowledge_base", id.String(),
			"Context policy truncated retrieval results",
			"kb="+id.String()+" budget="+itoa(pol.MaxContextTokens)+" dropped="+itoa(int64(droppedChunks))+" winning="+pol.WinningScope)
	}
	if err := m.incrementDataProductUsage(ctx, mc, dataProductID); err != nil {
		m.errorf("knowledge: data product usage update failed", "kb", id.String(), "err", err)
		return nil, queryErr(QueryErrUnavailable, "store error")
	}

	return &QueryResult{
		LineageID: lineageID.String(), Results: results, Count: len(results),
		EmbedModel: m.embedder.ModelRef(), Egress: m.embedder.AllowsEgress(),
		ContextBudgetTokens: pol.MaxContextTokens, ContextStrategy: pol.Strategy,
		ContextWinningScope: pol.WinningScope, ContextTruncated: contextTruncated,
		ContextDroppedChunks: droppedChunks, RedactionRequired: pol.RedactionRequired,
		ExcludedSources: pol.ExcludedSources, ExcludedChunks: excludedChunks, RedactedItems: redactedItems,
	}, nil
}

func droppedContextBudget(scored []ScoredChunk, idxByID map[string]model.Record) (int, int64) {
	var tokens int64
	for _, s := range scored {
		if rec := idxByID[s.ChunkID]; rec != nil {
			tokens += rec.Int(colTokenCount)
		}
	}
	return len(scored), tokens
}

// DocumentResult is the metadata + provenance of one ingested document (no body).
type DocumentResult struct {
	ID             string   `json:"id"`
	KBRef          string   `json:"kb_ref"`
	SourceKind     string   `json:"source_kind"`
	SourceRef      string   `json:"source_ref"`
	SourceMode     string   `json:"source_mode"`
	Title          string   `json:"title"`
	ContentType    string   `json:"content_type"`
	Classification string   `json:"classification"`
	Residency      string   `json:"residency_region"`
	ACL            []string `json:"acl"`
	ContentHash    string   `json:"content_hash"`
	ChunkCount     int64    `json:"chunk_count"`
	Status         string   `json:"status"`
}

// discoverGate reports whether the caller's authenticated agent may DISCOVER the given
// knowledge base's metadata: the SAME clearance/scope/residency boundary Query enforces
// before retrieval. The MCP discovery tools (list_kbs, fetch_document) call the
// programmatic methods below, so without this an agent could enumerate KBs and read a
// document's ACL/classification/content-hash for content it could never retrieve (F-04). The effective agent is ONLY the authenticated identity (F-03). Deny-closed: any
// guard/scope error, a missing grant, an over-classification, a residency mismatch, or an
// out-of-scope verdict denies. It runs OUTSIDE the caller's store view (the guard and
// scope gate open their own). classification/region are the subject's (a document carries
// its own; a KB uses its own).
func (m *Module) discoverGate(ctx context.Context, mc api.ModuleContext, kbID model.ID, kbName, classification, region string) (Grants, bool) {
	agentRef := strings.TrimSpace(mc.Principal.AgentIdentity)
	grants, err := m.guard.Resolve(ctx, mc.Tenant, mc.Principal.Actor(), agentRef, kbName)
	if err != nil || !grants.Allowed {
		return Grants{}, false
	}
	if !classificationAllowed(classification, grants.Clearance) {
		return Grants{}, false
	}
	if isRegionLocked(region) && grants.Region != region {
		return Grants{}, false
	}
	allowed, _, serr := m.scopeGate.Allowed(ctx, mc.Tenant, mc.Principal, agentRef, kbID.String())
	if serr != nil || !allowed {
		return Grants{}, false
	}
	// The resolved grants (Groups + LabelClearances) are returned so a per-DOCUMENT caller
	// (FetchDocument) can apply the same ACL/external-label predicate retrieval applies.
	return grants, true
}

// FetchDocument returns one document's metadata + provenance (no body).
func (m *Module) FetchDocument(ctx context.Context, mc api.ModuleContext, docID string) (*DocumentResult, error) {
	id, ok := idParam(strings.TrimSpace(docID))
	if !ok {
		return nil, queryErr(QueryErrBadRequest, "invalid document_id")
	}
	var out *DocumentResult
	var kbName string
	var extLabels []string
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, rerr := sc.Ext(documentKind)
		if rerr != nil {
			return rerr
		}
		rec, gerr := repo.Get(ctx, id)
		if gerr != nil {
			if isNotFound(gerr) {
				return nil
			}
			return gerr
		}
		out = &DocumentResult{
			ID: rec.String(model.ColID), KBRef: rec.String(colKBRef), SourceKind: rec.String(colSourceKind),
			SourceRef: rec.String(colSourceRef), SourceMode: recordSourceMode(rec), Title: rec.String(colTitle),
			ContentType: rec.String(colContentType), Classification: rec.String(colClassif),
			Residency: rec.String(colResidency), ACL: unmarshalStrings(rec, colACL),
			ContentHash: rec.String(colContentHash), ChunkCount: rec.Int(colDocChunkCnt),
			Status: rec.String(colStatus),
		}
		if kb, found, kerr := loadKB(ctx, sc, model.ID(out.KBRef)); kerr == nil && found {
			kbName = kb.String(colName)
		}
		// The document's external labels, for the external-label discovery gate below.
		all, lerr := loadExternalLabels(ctx, sc, model.ID(out.KBRef))
		if lerr != nil {
			return lerr
		}
		extLabels = all[out.ID]
		return nil
	})
	if err != nil {
		return nil, queryErr(QueryErrUnavailable, "store error")
	}
	if out == nil {
		return nil, queryErr(QueryErrNotFound, "document not found")
	}
	// F-04: gate KB-level metadata discovery by the SAME boundary as retrieval —
	// KB grant + clearance + residency + source scope — an agent that could not reach this
	// KB must not learn a document's ACL/classification/hash.
	grants, ok := m.discoverGate(ctx, mc, model.ID(out.KBRef), kbName, out.Classification, out.Residency)
	if !ok {
		return nil, queryErr(QueryErrNotFound, "document not found")
	}
	// F-05: apply the SAME per-DOCUMENT confidentiality predicate retrieval applies
	// (retrieval.go loadCandidates) before returning metadata — the document ACL
	// (intersected with the identity's groups) and the external-label gate. An agent
	// with KB clearance but excluded from THIS document by its ACL/labels must not read its
	// title/ACL/classification/hash. DLP is deliberately NOT applied here: it is a per-chunk
	// CONTENT-egress policy and FetchDocument returns no chunk body — the metadata
	// discovery boundary is classification + ACL + external-label, exactly the axes the
	// audit's required fix names. Deny-closed to NotFound (indistinguishable from absent).
	if !aclAllows(out.ACL, grants.Groups) {
		return nil, queryErr(QueryErrNotFound, "document not found")
	}
	if !externalLabelsAllowed(extLabels, grants.LabelClearances) {
		return nil, queryErr(QueryErrNotFound, "document not found")
	}
	return out, nil
}

// ListKBsResult is the result of listing knowledge bases.
type ListKBsResult struct {
	KBs []KBSummary `json:"kbs"`
}

// KBSummary is a knowledge base's discoverable metadata (for MCP resource
// discovery — tool descriptions can reference available KBs).
type KBSummary struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Classification string `json:"classification"`
	Status         string `json:"status"`
}

// ListDataProductsResult is the result of listing data products for MCP
// discovery.
type ListDataProductsResult struct {
	DataProducts []DataProductSummary `json:"data_products"`
}

// DataProductSummary is a published data product's discoverable metadata.
type DataProductSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	OwnerRef     string `json:"owner_ref"`
	Status       string `json:"status"`
	KBID         string `json:"kb_id,omitempty"`
	QualityScore int    `json:"quality_score"`
}

// ListKBs returns the knowledge bases the caller's authenticated agent may reach.
func (m *Module) ListKBs(ctx context.Context, mc api.ModuleContext) (*ListKBsResult, error) {
	var recs []model.Record
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, rerr := sc.Ext(baseKind)
		if rerr != nil {
			return rerr
		}
		rs, _, lerr := repo.List(ctx, model.Query{Limit: 200})
		recs = rs
		return lerr
	}); err != nil {
		return nil, queryErr(QueryErrUnavailable, "store error")
	}
	// F-04: filter to the KBs the authenticated agent may actually reach — the
	// same clearance/scope/residency boundary as retrieval — so discovery cannot
	// enumerate KBs (and their classification) the agent could never read. Gating runs
	// outside the store view (the guard/scope gate open their own).
	out := make([]KBSummary, 0, len(recs))
	for _, rec := range recs {
		kbID := model.ID(rec.String(model.ColID))
		// KB summaries carry no per-document ACL/labels, so only the KB-level boundary
		// applies here; the resolved grants are used only for the per-document gate.
		if _, ok := m.discoverGate(ctx, mc, kbID, rec.String(colName), rec.String(colClassif), rec.String(colResidency)); !ok {
			continue
		}
		out = append(out, KBSummary{
			ID:             rec.String(model.ColID),
			Name:           rec.String(colName),
			Classification: rec.String(colClassif),
			Status:         rec.String(colStatus),
		})
	}
	return &ListKBsResult{KBs: out}, nil
}

// ListDataProducts returns the published data products the caller's authenticated agent
// may reach.
func (m *Module) ListDataProducts(ctx context.Context, mc api.ModuleContext) (*ListDataProductsResult, error) {
	var recs []model.Record
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, rerr := sc.Ext(dataProductKind)
		if rerr != nil {
			return rerr
		}
		rs, _, lerr := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colStatus, dpStatusPublished)}, Limit: 200})
		recs = rs
		return lerr
	}); err != nil {
		return nil, queryErr(QueryErrUnavailable, "store error")
	}
	// F-04: a data product bound to a KB is discoverable only when the agent is in
	// that KB's source scope (the reachability axis retrieval enforces). A product with no
	// KB binding carries no content to protect and stays listable. Gating runs outside the
	// store view (the scope gate opens its own).
	agentRef := strings.TrimSpace(mc.Principal.AgentIdentity)
	out := make([]DataProductSummary, 0, len(recs))
	for _, rec := range recs {
		if kbRef := rec.String(colKBRef); kbRef != "" {
			if allowed, _, serr := m.scopeGate.Allowed(ctx, mc.Tenant, mc.Principal, agentRef, kbRef); serr != nil || !allowed {
				continue
			}
		}
		out = append(out, DataProductSummary{
			ID:           rec.String(model.ColID),
			Name:         rec.String(colName),
			OwnerRef:     rec.String(colOwnerRef),
			Status:       rec.String(colStatus),
			KBID:         rec.String(colKBRef),
			QualityScore: int(rec.Int(colQualityScore)),
		})
	}
	return &ListDataProductsResult{DataProducts: out}, nil
}

// IsQueryError reports whether err is a *QueryError and returns it.
func IsQueryError(err error) (*QueryError, bool) {
	var qe *QueryError
	if errors.As(err, &qe) {
		return qe, true
	}
	return nil, false
}

// excludedSource reports whether a chunk's source is excluded by an
// excluded_sources floor. An entry matches the source KIND, the source REF, or
// the exact "kind:ref" pair. Matching is exact and case-sensitive: a prefix or
// fuzzy rule would silently exclude more than the operator wrote, and a floor
// that over-excludes is still a floor that does something nobody asked for.
func excludedSource(excluded []string, kind, ref string) bool {
	if len(excluded) == 0 {
		return false
	}
	pair := kind + ":" + ref
	for _, e := range excluded {
		if e == "" {
			continue
		}
		if e == kind || (ref != "" && e == ref) || e == pair {
			return true
		}
	}
	return false
}

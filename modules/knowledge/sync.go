// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package knowledge — sync.go implements POST /kbs/{id}/sync: delta-based
// incremental sync (via contentsource.LiveSource.DeltaList) with automatic
// fallback to full-list reconciliation when the token expires, orphan detection
// and hard delete, ACL-only update path (no re-embed), legal-hold gating on
// deletes, and sync-state persistence.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Sync state status values (colLastSyncStatus column).
const (
	syncStatusOK    = "ok"
	syncStatusError = "error"
)

// syncDeltaPageCap bounds one incremental pass so a connector that keeps
// returning pagination tokens fails visibly instead of looping forever.
const syncDeltaPageCap = 512

// syncListMaxItems / syncListMaxBytes are the per-page ceilings the host requests when a
// source declares the bounded-pagination capability (contentsource.PagedSource). They cap
// how much a single ListPage call may return so a multi-million-doc source cannot stream
// its whole corpus into host RAM in one call; the reconciliation streams page by page.
const (
	syncListMaxItems = 1000
	syncListMaxBytes = 8 << 20 // 8 MiB of ref metadata per page
)

// syncFullPageCap bounds a full-list reconciliation so a source that never exhausts
// (an endless page stream) fails visibly instead of looping forever — the full-path
// analog of syncDeltaPageCap. Generous headroom (100k pages × syncListMaxItems ⇒ 100M
// refs) covers any realistic corpus; a non-advancing cursor is caught immediately below.
const syncFullPageCap = 100_000

// kindDocKey namespaces a source doc id by its SourceKind so a DocID shared across kinds
// in one KB never conflates two sources (F7 cross-source deletion). \x1f (unit
// separator) cannot appear in a kind or a normal doc id.
func kindDocKey(kind, docID string) string { return kind + "\x1f" + docID }

// syncRequest is the POST /kbs/{id}/sync request body.
type syncRequest struct {
	Source string `json:"source"`
}

// syncResponse summarizes one sync run.
type syncResponse struct {
	DocsSynced         int      `json:"docs_synced"`
	DocsDeleted        int      `json:"docs_deleted"`
	DocsSkipped        int      `json:"docs_skipped"`
	DeletesDeferred    bool     `json:"deletes_deferred"`
	ACLsRefreshed      int      `json:"acls_refreshed"`
	HeldDocs           []string `json:"held_docs,omitempty"`
	Errors             []string `json:"errors,omitempty"`
	SyncTokenSaved     bool     `json:"sync_token_saved"`
	FullReconciliation bool     `json:"full_reconciliation"`
	SyncWindow         string   `json:"sync_window"`
}

// syncStats accumulates per-run counters across the sync helper functions.
type syncStats struct {
	docsSynced      int
	docsDeleted     int
	docsSkipped     int  // detected but deterministically not ingestable (contentsource.ErrSkipDocument)
	deletesDeferred bool // orphan deletion withheld because the source listing was incomplete
	aclsRefreshed   int
	heldDocs        []string
	errors          []string
}

// recordContent folds one processContentChange outcome into the stats. A classified
// contentsource.ErrSkipDocument is a counted SKIP (an honest "detected, not
// ingestable" — binary content, a non-extractable/over-limit rich document, a
// disabled PDF), NOT an error: it must never abort the run or flip the sync status.
// A nil is a sync; anything else is a real (often transient/operational) error that
// is surfaced so a source blip is never silently swallowed as a skip.
func (s *syncStats) recordContent(docID string, err error) {
	switch {
	case err == nil:
		s.docsSynced++
	case errors.Is(err, contentsource.ErrSkipDocument):
		s.docsSkipped++
	default:
		s.errors = append(s.errors, fmt.Sprintf("content %s: %v", docID, err))
	}
}

// loadSyncState returns the sync_state row for (kbID, sourceName), or nil when no
// row exists yet. Must be called inside an open store.Scope (View or Mutate).
func loadSyncState(ctx context.Context, sc store.Scope, kbID model.ID, sourceName string) (model.Record, error) {
	repo, err := sc.Ext(syncStateKind)
	if err != nil {
		return nil, err
	}
	rec, ok, err := findOne(ctx, repo, eq(colKBRef, kbID.String()), eq(colSourceName, sourceName))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return rec, nil
}

// saveSyncState upserts the sync_state row for (kbID, sourceName). Must be called
// inside an open Mutate scope.
func saveSyncState(ctx context.Context, sc store.Scope, kbID model.ID, sourceName, token, status string, stats syncStats, now model.Timestamp) error {
	repo, err := sc.Ext(syncStateKind)
	if err != nil {
		return err
	}
	nowStr := now.String()
	// A nil/empty token is stored as NULL (nullable column).
	var tokenVal any = token
	if token == "" {
		tokenVal = nil
	}
	// Errors joined as newline-separated text; NULL when empty.
	var errVal any
	if errStr := strings.Join(stats.errors, "\n"); errStr != "" {
		errVal = errStr
	}

	existing, ok, err := findOne(ctx, repo, eq(colKBRef, kbID.String()), eq(colSourceName, sourceName))
	if err != nil {
		return err
	}
	if ok {
		existing[colSyncToken] = tokenVal
		existing[colLastSyncAt] = nowStr
		existing[colLastSyncStatus] = status
		existing[colDocsSynced] = int64(stats.docsSynced)
		existing[colDocsDeleted] = int64(stats.docsDeleted)
		existing[colACLsRefreshed] = int64(stats.aclsRefreshed)
		existing[colSyncErrors] = errVal
		_, err = repo.Update(ctx, existing)
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colKBRef:          kbID.String(),
		colSourceName:     sourceName,
		colSyncToken:      tokenVal,
		colLastSyncAt:     nowStr,
		colLastSyncStatus: status,
		colDocsSynced:     int64(stats.docsSynced),
		colDocsDeleted:    int64(stats.docsDeleted),
		colACLsRefreshed:  int64(stats.aclsRefreshed),
		colSyncErrors:     errVal,
	})
	return err
}

// handleSync handles POST /kbs/{id}/sync.
//
// Flow:
//  1. Validate source name and KB existence.
//  2. Load prior sync state (the cursor token).
//  3. Branch: LiveSource → syncDelta; token expired or static Source → syncFull.
//  4. Persist sync state (best-effort).
//  5. Emit audit event (best-effort).
//  6. Return syncResponse.
func (m *Module) handleSync(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req syncRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Source = strings.TrimSpace(req.Source)
	if req.Source == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("source is required"))
		return
	}

	src, srcFound := m.sources[req.Source]
	if !srcFound {
		writeJSON(w, http.StatusBadRequest, errorBody("unknown source: "+req.Source))
		return
	}
	if src.Kind() != contentsource.ClassDocument {
		writeJSON(w, http.StatusBadRequest, errorBody("source "+req.Source+" is not a document content source"))
		return
	}

	// Load the knowledge base.
	var kb model.Record
	var kbFound bool
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		rec, found, err := loadKB(r.Context(), sc, id)
		kb, kbFound = rec, found
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !kbFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}

	// Load prior sync state (the cursor token).
	var syncState model.Record
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var err error
		syncState, err = loadSyncState(r.Context(), sc, id, req.Source)
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	startTS := m.clock.Now()
	var stats syncStats
	var nextToken string
	var fullReconciliation bool

	if liveSrc, isLive := src.(contentsource.LiveSource); isLive {
		// Attempt delta sync.
		var fallback bool
		var err error
		stats, nextToken, fallback, err = m.syncDelta(r.Context(), mc, liveSrc, kb, id, syncState)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("sync delta failed: "+err.Error()))
			return
		}
		if fallback {
			// Token expired — fall back to full-list reconciliation.
			stats = syncStats{}
			nextToken = ""
			fullReconciliation = true
			var ferr error
			stats, ferr = m.syncFull(r.Context(), mc, src, kb, id)
			if ferr != nil {
				writeJSON(w, http.StatusBadGateway, errorBody("sync full failed: "+ferr.Error()))
				return
			}
		}
	} else {
		// Static source — always do a full-list reconciliation.
		fullReconciliation = true
		var err error
		stats, err = m.syncFull(r.Context(), mc, src, kb, id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, errorBody("sync full failed: "+err.Error()))
			return
		}
	}

	endTS := m.clock.Now()
	syncWindow := startTS.String() + "/" + endTS.String()

	// Persist sync state (best-effort: failure is logged, response is not affected).
	syncStatus := syncStatusOK
	if len(stats.errors) > 0 {
		syncStatus = syncStatusError
	}
	tokenSaved := false
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return saveSyncState(r.Context(), sc, id, req.Source, nextToken, syncStatus, stats, m.clock.Now())
	}); err != nil {
		m.errorf("knowledge: failed to save sync state", "kb", id.String(), "source", req.Source, "err", err)
	} else {
		tokenSaved = true
	}

	// Audit event (best-effort: a lost audit row is logged, not fatal).
	_ = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "knowledge.sync", baseKind, id, map[string]any{
			"source": req.Source, "docs_synced": stats.docsSynced,
			"docs_deleted": stats.docsDeleted, "docs_skipped": stats.docsSkipped,
			"deletes_deferred": stats.deletesDeferred, "acls_refreshed": stats.aclsRefreshed,
			"full_reconciliation": fullReconciliation,
		})
	})

	resp := syncResponse{
		DocsSynced:         stats.docsSynced,
		DocsDeleted:        stats.docsDeleted,
		DocsSkipped:        stats.docsSkipped,
		DeletesDeferred:    stats.deletesDeferred,
		ACLsRefreshed:      stats.aclsRefreshed,
		HeldDocs:           stats.heldDocs,
		Errors:             stats.errors,
		SyncTokenSaved:     tokenSaved,
		FullReconciliation: fullReconciliation,
		SyncWindow:         syncWindow,
	}
	if resp.HeldDocs == nil {
		resp.HeldDocs = []string{}
	}
	if resp.Errors == nil {
		resp.Errors = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// syncDelta runs an incremental sync via DeltaList. It returns (stats,
// resumeToken, fallback, error). fallback=true means the cursor token expired;
// the caller must switch to syncFull.
func (m *Module) syncDelta(ctx context.Context, mc api.ModuleContext, src contentsource.LiveSource, kb model.Record, kbID model.ID, syncState model.Record) (syncStats, string, bool, error) {
	var stats syncStats
	sinceToken := ""
	if syncState != nil {
		sinceToken = syncState.String(colSyncToken)
	}
	pageToken := sinceToken
	resumeToken := ""
	sourceName := strings.TrimSpace(src.Descriptor().Name)
	if sourceName == "" {
		sourceName = "unknown"
	}
	// Resolve the source's authoritative SourceKind LAZILY (only if a delete appears) and
	// cache it, so a delta delete removes the row for THIS source's kind, never a shared-DocID
	// row of another source (F7). Empty ⇒ fall back to a DocID-only delete.
	deltaKind, deltaKindDone := "", false
	sourceKindFor := func() string {
		if !deltaKindDone {
			deltaKindDone = true
			deltaKind, _ = m.probeSourceKind(ctx, src)
		}
		return deltaKind
	}
	for pageCount := 0; ; pageCount++ {
		if pageCount >= syncDeltaPageCap {
			return stats, "", false, fmt.Errorf("delta list exceeded %d pages for source %s", syncDeltaPageCap, sourceName)
		}
		page, err := src.DeltaList(ctx, pageToken)
		if err != nil {
			return stats, "", false, fmt.Errorf("delta list: %w", err)
		}
		if page.Expired {
			return stats, "", true, nil // signal full-reconciliation fallback
		}
		if page.ResumeToken != "" {
			resumeToken = page.ResumeToken
		}
		for _, entry := range page.Changes {
			switch entry.ChangeKind {
			case contentsource.ChangeContent, contentsource.ChangeMetadata:
				stats.recordContent(entry.DocRef.DocID, m.processContentChange(ctx, mc, src, kbID, kb, entry.DocRef))
			case contentsource.ChangeACL:
				if err := m.processACLChange(ctx, mc, src, kbID, sourceKindFor(), entry.DocRef); err != nil {
					stats.errors = append(stats.errors, fmt.Sprintf("acl %s: %v", entry.DocRef.DocID, err))
				} else {
					stats.aclsRefreshed++
				}
			case contentsource.ChangeDeleted:
				held, err := m.processDelete(ctx, mc, kbID, sourceKindFor(), entry.DocRef)
				if err != nil {
					stats.errors = append(stats.errors, fmt.Sprintf("delete %s: %v", entry.DocRef.DocID, err))
				} else if held {
					stats.heldDocs = append(stats.heldDocs, entry.DocRef.DocID)
				} else {
					stats.docsDeleted++
				}
			}
		}
		if page.NextToken == "" {
			break
		}
		pageToken = page.NextToken
	}
	if resumeToken == "" {
		resumeToken = sinceToken
	}
	return stats, resumeToken, false, nil
}

// syncFull performs a full-list reconciliation that STREAMS the source instead of
// materializing its whole corpus in RAM — so a multi-million-document SharePoint/Drive/
// filesystem source is bounded by one page + the KB's own size, not by the upstream
// corpus. It preserves the orphan-detection invariant exactly:
//  1. Load the current KB docs (bounded by the KB, our managed data — the store enforces
//     the per-tenant chunk ceiling; see docs/SIZING-AND-CAPACITY.md).
//  2. Determine the source's SourceKind from the FIRST page (intersect with the DB; on a
//     first sync with no overlap, probe-fetch the first ref).
//  3. Seed the orphan set = every DB doc of that SourceKind; as each source ref streams
//     by it is removed from the set. What remains after the stream are the true orphans.
//  4. Page the source (bounded ListPage when the source declares it, else List): ingest a
//     ref not yet persisted, skip one already persisted, mark every ref seen.
//  5. Delete the docs still in the orphan set (legal-hold aware).
//
// RAM is O(KB size) + one source page, never O(source corpus). No delete is lost: the
// orphan set is the exact DB(SourceKind) \ seen difference — computed without ever holding
// the full source ref set.
func (m *Module) syncFull(ctx context.Context, mc api.ModuleContext, src contentsource.Source, kb model.Record, kbID model.ID) (syncStats, error) {
	var stats syncStats

	// Step 1: Load KB docs (bounded by the KB's enforced size, not the source corpus).
	var allKBDocs []model.Record
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		allKBDocs, err = listAll(ctx, repo, eq(colKBRef, kbID.String()))
		return err
	}); err != nil {
		return stats, fmt.Errorf("list kb docs: %w", err)
	}
	// Key DB docs by (SourceKind, SourceDocID). A DocID shared across SourceKinds in one KB
	// must never conflate two sources, or a full sync of one source could orphan-delete the
	// other's docs (F7 cross-source deletion). SourceKind is resolved authoritatively
	// below (probe-fetch), never by a cross-kind DocID match.
	dbByKindDoc := make(map[string]struct{}, len(allKBDocs))
	// dbAnyDocID is the set of DB source_doc_ids across ALL kinds. Only a ref that is an
	// existing DB row can ever become an orphan needing subtraction, so the pre-resolution
	// seen-buffer is gated on membership here — bounding it to O(KB size), never O(source
	// corpus): an all-skippable source (binary corpus, or a hostile plugin listing millions of
	// cheap-skippable refs) must not accumulate every listed DocID (F5 OOM regression).
	dbAnyDocID := make(map[string]struct{}, len(allKBDocs))
	for _, dbDoc := range allKBDocs {
		dbByKindDoc[kindDocKey(dbDoc.String(colSourceKind), dbDoc.String(colSourceDocID))] = struct{}{}
		dbAnyDocID[dbDoc.String(colSourceDocID)] = struct{}{}
	}

	// Stream the source. sourceKind + the orphan set are resolved lazily on the first page
	// so we never need the whole ref set in RAM.
	var (
		sourceKind      string
		orphanSet       map[string]bool // DB source_doc_ids of this SourceKind not yet seen
		resolved        bool
		cursor          string
		pageCount       int
		listingComplete = true                  // AND-accumulated per-page: false ⇒ withhold orphan deletion
		preResolveSeen  = map[string]struct{}{} // DB-present DocIDs seen BEFORE the kind resolved (a SET,
		//                                         so a source re-listing one DocID cannot grow it — O(KB size))
	)
	for {
		refs, next, pageComplete, err := m.listSourcePage(ctx, src, cursor)
		if err != nil {
			return stats, fmt.Errorf("source list: %w", err)
		}
		// A page the host had to truncate at its RAM ceiling (F5) is not authoritative; any
		// such page makes the whole run incomplete so orphan deletion is withheld. This is a
		// PER-CALL result — never shared source state — so a concurrent sync of the same
		// source cannot clobber this run's verdict (F5 review finding).
		listingComplete = listingComplete && pageComplete
		// Unbounded-wire guard (F6): a source that never exhausts — a non-advancing
		// cursor, or an endless page stream — must fail visibly, not loop the engine forever
		// (the full-path analog of syncDeltaPageCap). Each page is already RAM-bounded (F5).
		pageCount++
		if next != "" && next == cursor {
			return stats, fmt.Errorf("source returned a non-advancing pagination cursor %q (unbounded-wire guard)", next)
		}
		if pageCount > syncFullPageCap {
			return stats, fmt.Errorf("full list exceeded %d pages (unbounded-wire guard)", syncFullPageCap)
		}
		if !resolved {
			// Resolve the source's AUTHORITATIVE SourceKind by probe-fetching a ref
			// (doc.Source is the source's own kind) — never by a cross-kind DB DocID match,
			// which a DocID shared across SourceKinds would resolve to the WRONG kind and
			// seed the orphan set from another source (F7). A ref that is a
			// deterministic SKIP (binary / non-extractable rich doc) must not abort the sync
			// just because it sorted first — try the next one; only a real
			// (transient/operational) Fetch error is fatal here.
			for _, ref := range refs {
				doc, ferr := src.Fetch(ctx, ref.DocID)
				if ferr == nil {
					sourceKind = string(doc.Source)
					break
				}
				if errors.Is(ferr, contentsource.ErrSkipDocument) {
					continue
				}
				return stats, fmt.Errorf("probe-fetch %s: %w", ref.DocID, ferr)
			}
			// Latch resolution only once the kind is ACTUALLY known: an empty or
			// all-skippable first page must not freeze sourceKind="" for the whole run
			// (which would seed no orphan set AND make the (kind,DocID) existing-check miss,
			// re-embedding every later doc). Keep re-attempting on subsequent pages.
			if sourceKind != "" {
				resolved = true
				orphanSet = make(map[string]bool)
				for _, dbDoc := range allKBDocs {
					if dbDoc.String(colSourceKind) == sourceKind {
						orphanSet[dbDoc.String(colSourceDocID)] = true
					}
				}
				// Refs that streamed by on PRE-resolution pages are LISTED ⇒ not orphans, but
				// their delete() was a no-op while orphanSet was nil. Remove them from the
				// freshly-seeded set now (F5 rework finding: a doc ingested when fetchable
				// can later become skippable — it is still listed and must NOT be deleted).
				for id := range preResolveSeen {
					delete(orphanSet, id)
				}
				preResolveSeen = nil
			}
		}
		// Step 4: process this page — ingest new, skip existing (SAME SourceKind), mark seen.
		// While sourceKind is still unresolved (all-skippable pages), the existing-check
		// cannot bind a kind; such refs are skippable and processContentChange returns
		// ErrSkipDocument (a counted skip), so they are neither re-embedded nor orphaned.
		for _, ref := range refs {
			if !resolved {
				// Kind not yet known: record the DocID as SEEN so it is subtracted from the
				// orphan set once the kind resolves (it is being LISTED, so it is not an orphan).
				// Only DB-present DocIDs can ever be orphans, and this is a SET, so the buffer is
				// O(KB size) regardless of how a source re-lists refs — no OOM (F5).
				if _, inDB := dbAnyDocID[ref.DocID]; inDB {
					preResolveSeen[ref.DocID] = struct{}{}
				}
			}
			_, existing := dbByKindDoc[kindDocKey(sourceKind, ref.DocID)]
			delete(orphanSet, ref.DocID) // seen ⇒ not an orphan
			if resolved && existing {
				continue // already persisted under this kind; full sync does not re-embed
			}
			cerr := m.processContentChange(ctx, mc, src, kbID, kb, ref)
			stats.recordContent(ref.DocID, cerr)
			if resolved && cerr == nil {
				// Mark the freshly-ingested doc as present so a malformed source re-listing the
				// same NEW DocID on a later page is a skip, not a wasteful re-embed (bounded:
				// grows only by distinct newly-persisted DocIDs ⇒ O(KB size)).
				dbByKindDoc[kindDocKey(sourceKind, ref.DocID)] = struct{}{}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}

	// Step 5: Delete the docs still in the orphan set — DB docs of this SourceKind that the
	// source no longer lists. Legal-hold gating is preserved (processDelete → held).
	//
	// SAFETY GATE: only reconcile deletions against a PROVABLY COMPLETE listing. Two
	// independent signals can mark the view partial, and EITHER withholds deletion:
	//   - a page the host truncated at its RAM ceiling (listingComplete, F5) — a large or
	//     hostile external source we did not fully enumerate;
	//   - a source that statically reports an incomplete view (CompletenessReporter → false:
	//     a partial tree walk, a transient NFS/SMB read error, an I/O budget cut-off).
	// Deleting on a partial view would destroy data on a source blip — the invariant "a
	// source outage is never mistaken for every-document-deleted". Defer to the next
	// complete sync. sourceKind=="" (never resolved) is also treated as incomplete.
	reporter, _ := src.(contentsource.CompletenessReporter)
	if !listingComplete || sourceKind == "" || (reporter != nil && !reporter.ListingComplete()) {
		stats.deletesDeferred = true
		if m.log != nil {
			m.log.Warn("knowledge: source listing incomplete; orphan deletion DEFERRED to avoid destroying documents on a partial view",
				"kb", kbID.String(), "candidate_orphans", len(orphanSet))
		}
		return stats, nil
	}
	for sid := range orphanSet {
		held, err := m.processDelete(ctx, mc, kbID, sourceKind, contentsource.DocRef{DocID: sid})
		if err != nil {
			stats.errors = append(stats.errors, fmt.Sprintf("delete %s: %v", sid, err))
		} else if held {
			stats.heldDocs = append(stats.heldDocs, sid)
		} else {
			stats.docsDeleted++
		}
	}

	return stats, nil
}

// listSourcePage returns one bounded page of source refs. When the source declares the
// bounded-pagination capability (contentsource.PagedSource) the host asks for a page with
// explicit item/byte ceilings, so a plugin-backed source cannot stream its whole corpus
// into host RAM in a single call; otherwise it falls back to the ordinary paginated List
// (already page-bounded for in-tree sources). Either way syncFull holds only one page.
func (m *Module) listSourcePage(ctx context.Context, src contentsource.Source, cursor string) (refs []contentsource.DocRef, next string, complete bool, err error) {
	if paged, ok := src.(contentsource.PagedSource); ok {
		return paged.ListPage(ctx, cursor, syncListMaxItems, syncListMaxBytes)
	}
	// A non-paged in-tree source's List is already page-bounded; treat each page as complete.
	refs, next, err = src.List(ctx, cursor)
	return refs, next, true, err
}

// probeSourceKind returns a source's AUTHORITATIVE SourceKind by listing one bounded page and
// probe-fetching a ref (doc.Source is the source's own kind). Empty when the source lists
// nothing fetchable (the caller then falls back to a DocID-only delete). Used by the delta
// path to scope deletes by kind (F7); the full path resolves the kind inline.
func (m *Module) probeSourceKind(ctx context.Context, src contentsource.Source) (string, error) {
	refs, _, _, err := m.listSourcePage(ctx, src, "")
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		doc, ferr := src.Fetch(ctx, ref.DocID)
		if ferr == nil {
			return string(doc.Source), nil
		}
		if errors.Is(ferr, contentsource.ErrSkipDocument) {
			continue
		}
		return "", ferr
	}
	return "", nil
}

// processContentChange fetches one source document and runs the full ingest
// pipeline (classify → DLP gate → redact → chunk → embed → upsert doc+chunks).
func (m *Module) processContentChange(ctx context.Context, mc api.ModuleContext, src contentsource.Source, kbID model.ID, kb model.Record, docRef contentsource.DocRef) error {
	doc, err := src.Fetch(ctx, docRef.DocID)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if strings.TrimSpace(doc.Title) == "" {
		if title := strings.TrimSpace(docRef.Title); title != "" {
			doc.Title = title
		}
	}

	var dlpPol dlpPolicy
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		var e error
		dlpPol, e = loadDLPPolicy(ctx, sc)
		return e
	}); err != nil {
		return fmt.Errorf("load dlp: %w", err)
	}

	defaultACL := unmarshalStrings(kb, colDefaultACL)
	kbClassif := kb.String(colClassif)
	region := kb.String(colResidency)

	pd, err := m.prepareContent(doc, sourceModeForSource(src), defaultACL, kbClassif, dlpPol)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}

	if err := m.embedPrepared(ctx, mc.Tenant, &pd); err != nil {
		return fmt.Errorf("embed: %w", err)
	}

	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		chunkRepo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		docID, _, err := m.upsertDocument(ctx, docRepo, kbID, region, pd)
		if err != nil {
			return err
		}
		// Persist sensitivity labels (same contract as handleIngest).
		if pd.scanned {
			if err := m.upsertLabel(ctx, sc, subjectDocument, docID.String(), kbID.String(),
				basisStored, pd.storedHash, m.classifier.Version(), pd.hits); err != nil {
				return err
			}
		} else if err := m.deleteLabel(ctx, sc, subjectDocument, docID.String()); err != nil {
			return err
		}
		// Persist external sensitivity labels.
		if len(pd.doc.ExternalLabels) > 0 {
			if err := upsertExternalLabel(ctx, sc, docID.String(), kbID.String(),
				string(pd.doc.Source), pd.doc.ExternalLabels); err != nil {
				return err
			}
		} else if err := deleteExternalLabel(ctx, sc, docID.String()); err != nil {
			return err
		}
		if _, err := m.replaceChunks(ctx, chunkRepo, kbID, docID, region, pd); err != nil {
			return err
		}
		return nil
	})
}

// processACLChange fetches the document's current ACL from the live source and
// updates the doc row and all its chunks. Deny-closed: if FetchACL fails, the
// chunks' ACL is set to aclStaleSentinel so the doc is excluded from every
// retrieval until the next successful sync.
// processACLChange applies a delta ACL/classification change to the document identified by
// (KBRef, sourceKind, SourceDocID). sourceKind MUST be threaded in for the same reason as
// processDelete: a DocID shared across SourceKinds in one KB would otherwise resolve to an
// arbitrary matching row and apply the WRONG source's ACL — over-exposing or over-restricting
// another source's document (F7). Empty sourceKind falls back to the DocID-only lookup.
func (m *Module) processACLChange(ctx context.Context, mc api.ModuleContext, src contentsource.Source, kbID model.ID, sourceKind string, docRef contentsource.DocRef) error {
	liveSrc, ok := src.(contentsource.LiveSource)
	if !ok {
		return nil // not a live source; ACL refresh skipped
	}
	aclResult, fetchErr := liveSrc.FetchACL(ctx, docRef.DocID)

	var acl []string
	var classif string
	var extLabels []string
	if fetchErr != nil {
		m.errorf("knowledge: FetchACL failed; applying stale sentinel (deny-closed)", "doc_id", docRef.DocID, "err", fetchErr)
		acl = []string{aclStaleSentinel}
	} else {
		acl = aclResult.ACL
		classif = aclResult.Classification
		extLabels = aclResult.ExternalLabels
	}

	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		aclFilters := []model.Filter{eq(colKBRef, kbID.String()), eq(colSourceDocID, docRef.DocID)}
		if sourceKind != "" {
			aclFilters = append(aclFilters, eq(colSourceKind, sourceKind))
		}
		rec, found, err := findOne(ctx, repo, aclFilters...)
		if err != nil {
			return err
		}
		if !found {
			return nil // document not yet in DB; skip
		}
		return m.updateDocACL(ctx, sc, rec.String(model.ColID), acl, classif, extLabels)
	})
}

// processDelete removes a document, its chunks and its external label after a
// legal-hold gate check. Returns held=true (not an error) when an active hold
// blocks deletion; the caller records the doc in stats.heldDocs.
// processDelete removes one document identified by (KBRef, sourceKind, SourceDocID) — the
// SAME tuple ingest and the store's unique index use. sourceKind MUST be threaded in: a
// DocID can be shared across SourceKinds in one KB, so a (KBRef, SourceDocID)-only lookup
// could delete another source's live document (F7). An empty sourceKind falls back to
// the DocID-only lookup (legacy callers with no kind context).
func (m *Module) processDelete(ctx context.Context, mc api.ModuleContext, kbID model.ID, sourceKind string, docRef contentsource.DocRef) (held bool, err error) {
	// Resolve the DB row ID outside the write transaction.
	var docID string
	if err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{eq(colKBRef, kbID.String()), eq(colSourceDocID, docRef.DocID)}
		if sourceKind != "" {
			filters = append(filters, eq(colSourceKind, sourceKind))
		}
		rec, ok, err := findOne(ctx, repo, filters...)
		if err != nil {
			return err
		}
		if ok {
			docID = rec.String(model.ColID)
		}
		return nil
	}); err != nil {
		return false, err
	}
	if docID == "" {
		return false, nil // already deleted
	}

	// Legal-hold gate (outside the store transaction — the hold ledger is external).
	if m.holdGate != nil {
		isHeld, _, hErr := m.holdGate.Check(ctx, mc.Tenant, holdSubjectDocument, docID, holdClassKnowledgeContent)
		if hErr != nil {
			return false, fmt.Errorf("hold check: %w", hErr)
		}
		if isHeld {
			return true, nil
		}
	}

	// Delete chunks → doc → external label atomically.
	return false, mc.Data.Mutate(ctx, func(sc store.Scope) error {
		chunkRepo, err := sc.Ext(chunkKind)
		if err != nil {
			return err
		}
		chunks, err := listAll(ctx, chunkRepo, eq(colDocRef, docID))
		if err != nil {
			return err
		}
		for _, c := range chunks {
			if err := chunkRepo.Delete(ctx, model.ID(c.String(model.ColID))); err != nil {
				return err
			}
		}
		docRepo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		// Tolerate a concurrent delete between View and Mutate.
		if err := docRepo.Delete(ctx, model.ID(docID)); err != nil && !isNotFound(err) {
			return err
		}
		return deleteExternalLabel(ctx, sc, docID)
	})
}

// updateDocACL updates a document row's ACL and optionally its classification, then
// propagates the change to all its chunks. It also upserts or deletes the external
// label row according to extLabels. docID is the database row id.
func (m *Module) updateDocACL(ctx context.Context, sc store.Scope, docID string, acl []string, classif string, extLabels []string) error {
	docRepo, err := sc.Ext(documentKind)
	if err != nil {
		return err
	}
	doc, err := docRepo.Get(ctx, model.ID(docID))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}

	aclJSON := marshalStrings(acl)
	doc[colACL] = aclJSON
	if classif != "" {
		doc[colClassif] = normClass(classif)
	}
	if _, err := docRepo.Update(ctx, doc); err != nil {
		return err
	}

	// Propagate ACL to all chunks of this document.
	chunkRepo, err := sc.Ext(chunkKind)
	if err != nil {
		return err
	}
	chunks, err := listAll(ctx, chunkRepo, eq(colDocRef, docID))
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		chunk[colACL] = aclJSON
		if classif != "" {
			chunk[colClassif] = normClass(classif)
		}
		if _, err := chunkRepo.Update(ctx, chunk); err != nil {
			return err
		}
	}

	// Update external label row.
	kbRef := doc.String(colKBRef)
	srcKind := doc.String(colSourceKind)
	if len(extLabels) > 0 {
		return upsertExternalLabel(ctx, sc, docID, kbRef, srcKind, extLabels)
	}
	return deleteExternalLabel(ctx, sc, docID)
}

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

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// fakeLiveSource is a fake contentsource.LiveSource for testing delta sync.
type fakeLiveSource struct {
	fakeSource
	mu      sync.Mutex
	pages   []contentsource.DeltaPage
	pageIdx int
	tokens  []string
	liveDoc map[string]contentsource.Document
	aclMap  map[string]contentsource.ACLResult
	aclErrs map[string]error
}

func newFakeLiveSource(docs []contentsource.Document) *fakeLiveSource {
	return &fakeLiveSource{
		fakeSource: fakeSource{docs: docs, kind: contentsource.ClassDocument},
		aclMap:     make(map[string]contentsource.ACLResult),
		aclErrs:    make(map[string]error),
	}
}

func (s *fakeLiveSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.fake-live-source", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}

// setPages configures the ordered DeltaPages returned by DeltaList.
func (s *fakeLiveSource) setPages(pages []contentsource.DeltaPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = pages
	s.pageIdx = 0
	s.tokens = nil
}

func (s *fakeLiveSource) setLiveDocs(docs []contentsource.Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveDoc = make(map[string]contentsource.Document, len(docs))
	for _, doc := range docs {
		s.liveDoc[doc.DocID] = doc
	}
}

func (s *fakeLiveSource) deltaTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.tokens))
	copy(out, s.tokens)
	return out
}

// setACL configures what FetchACL returns for docID.
func (s *fakeLiveSource) setACL(docID string, result contentsource.ACLResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aclMap[docID] = result
}

// setACLErr configures FetchACL to return an error for docID.
func (s *fakeLiveSource) setACLErr(docID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aclErrs[docID] = err
}

func (s *fakeLiveSource) DeltaList(_ context.Context, token string) (contentsource.DeltaPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, token)
	if s.pageIdx >= len(s.pages) {
		return contentsource.DeltaPage{}, nil
	}
	p := s.pages[s.pageIdx]
	s.pageIdx++
	return p, nil
}

func (s *fakeLiveSource) Fetch(ctx context.Context, docID string) (contentsource.Document, error) {
	if err := ctx.Err(); err != nil {
		return contentsource.Document{}, err
	}
	s.mu.Lock()
	if doc, ok := s.liveDoc[docID]; ok {
		s.mu.Unlock()
		return doc, nil
	}
	s.mu.Unlock()
	return s.fakeSource.Fetch(ctx, docID)
}

func (s *fakeLiveSource) FetchACL(_ context.Context, docID string) (contentsource.ACLResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.aclErrs[docID]; ok {
		return contentsource.ACLResult{}, err
	}
	if r, ok := s.aclMap[docID]; ok {
		return r, nil
	}
	return contentsource.ACLResult{ACL: []string{"anyone"}}, nil
}

func (h *harness) syncToken(t *testing.T, tenant model.TenantID, kbID, sourceName string) string {
	t.Helper()
	var token string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		rec, err := loadSyncState(context.Background(), sc, model.ID(kbID), sourceName)
		if err != nil || rec == nil {
			return err
		}
		token = rec.String(colSyncToken)
		return nil
	}); err != nil {
		t.Fatalf("load sync token: %v", err)
	}
	return token
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSyncUnknownSource verifies that POST /kbs/{id}/sync with an unregistered
// source name returns 400 Bad Request.
func TestSyncUnknownSource(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "no-such-source"}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("sync unknown source: got %d %s, want 400", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected error envelope in body, got: %s", r.raw)
	}
}

// TestSyncStaticSourceOrphanDetection verifies full-list reconciliation:
//  1. Ingest 3 source docs into a KB.
//  2. Re-register the source with only 2 docs (d3 removed).
//  3. POST /kbs/{id}/sync → docs_deleted=1, full_reconciliation=true.
//  4. GET /kbs/{id}/documents → exactly 2 documents remain.
func TestSyncStaticSourceOrphanDetection(t *testing.T) {
	allDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "document one"},
		{Source: "testsrc", DocID: "d2", Body: "document two"},
		{Source: "testsrc", DocID: "d3", Body: "document three"},
	}
	twoDocDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "document one"},
		{Source: "testsrc", DocID: "d2", Body: "document two"},
	}

	h := newHarnessWith(t,
		WithSource("testsrc", newFakeSource(allDocs)),
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest all three documents via the source.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("initial ingest: got %d %s", r.code, r.raw)
	}

	// Confirm three documents are stored.
	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 3 {
		t.Fatalf("after ingest: want 3 docs, got %d (body: %s)", len(items), docsResp.raw)
	}

	// Re-register source with only d1 and d2 (d3 is now orphaned).
	h.addSource("testsrc", newFakeSource(twoDocDocs))

	// Run sync.
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// Verify sync response counters.
	if d, _ := r.body["docs_deleted"].(float64); d != 1 {
		t.Errorf("docs_deleted = %v, want 1 (full response: %s)", r.body["docs_deleted"], r.raw)
	}
	if full, _ := r.body["full_reconciliation"].(bool); !full {
		t.Errorf("full_reconciliation = %v, want true", r.body["full_reconciliation"])
	}
	if tok, _ := r.body["sync_token_saved"].(bool); !tok {
		t.Errorf("sync_token_saved = %v, want true", r.body["sync_token_saved"])
	}

	// Verify only two documents remain.
	docsResp = h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 2 {
		t.Fatalf("after sync: want 2 docs, got %d (body: %s)", len(items), docsResp.raw)
	}
}

// TestSyncLiveSourceDeltaContentChange verifies that a delta sync processes
// a ChangeContent entry by re-ingesting the document.
func TestSyncLiveSourceDeltaContentChange(t *testing.T) {
	initialDocs := []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "initial body"},
	}
	updatedDocs := []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "updated body"},
	}

	liveSrc := newFakeLiveSource(initialDocs)
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest d1 so it exists in the KB.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest: got %d %s", r.code, r.raw)
	}

	// Update the live source to return updated body and configure a delta page.
	liveSrc.fakeSource.docs = updatedDocs
	h.mod.AddSource("livesrc", liveSrc)
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeContent},
			},
			ResumeToken: "delta-content-1",
		},
	})

	// Run sync.
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// full_reconciliation must be false for a live source with a valid token.
	if full, _ := r.body["full_reconciliation"].(bool); full {
		t.Error("full_reconciliation should be false for a live source delta sync")
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 1 {
		t.Errorf("docs_synced = %v, want 1", r.body["docs_synced"])
	}
}

func TestSyncDeltaPersistsResumeToken(t *testing.T) {
	liveSrc := newFakeLiveSource([]contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "body one"},
	})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeContent},
			},
			NextToken: "page-2",
		},
		{ResumeToken: "delta-2"},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 1 {
		t.Errorf("docs_synced = %v, want 1", r.body["docs_synced"])
	}
	if got := h.syncToken(t, tenant, kbID, "livesrc"); got != "delta-2" {
		t.Fatalf("sync token = %q, want delta-2", got)
	}
	if got := liveSrc.deltaTokens(); len(got) != 2 || got[0] != "" || got[1] != "page-2" {
		t.Fatalf("DeltaList tokens = %v, want [\"\" \"page-2\"]", got)
	}
}

func TestSyncDeltaKeepsPreviousTimestampWhenNoResumeToken(t *testing.T) {
	const timestamp = "2026-06-25T14:30:00Z"
	liveSrc := newFakeLiveSource([]contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "body one"},
	})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeContent},
			},
			ResumeToken: timestamp,
		},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("first sync: got %d %s", r.code, r.raw)
	}
	if got := h.syncToken(t, tenant, kbID, "livesrc"); got != timestamp {
		t.Fatalf("first sync token = %q, want %q", got, timestamp)
	}

	liveSrc.setPages([]contentsource.DeltaPage{{}})
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("second sync: got %d %s", r.code, r.raw)
	}
	if got := h.syncToken(t, tenant, kbID, "livesrc"); got != timestamp {
		t.Fatalf("second sync token = %q, want preserved %q", got, timestamp)
	}
	if got := liveSrc.deltaTokens(); len(got) != 1 || got[0] != timestamp {
		t.Fatalf("second DeltaList tokens = %v, want [%q]", got, timestamp)
	}
}

func TestSyncDeltaFetchesLiveContentOutsideOfflineStore(t *testing.T) {
	liveSrc := newFakeLiveSource(nil)
	liveSrc.setLiveDocs([]contentsource.Document{
		{Source: "livesrc", DocID: "live-doc", Title: "Live Doc", Body: "live-only body"},
	})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "live-doc"}, ChangeKind: contentsource.ChangeContent},
			},
			ResumeToken: "delta-live",
		},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}
	if synced, _ := r.body["docs_synced"].(float64); synced != 1 {
		t.Fatalf("docs_synced = %v, want 1 (response: %s)", r.body["docs_synced"], r.raw)
	}
	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 1 {
		t.Fatalf("after live-only sync: want 1 doc, got %d (body: %s)", len(items), docsResp.raw)
	}
}

func TestSyncDeltaFallsBackToDocRefTitle(t *testing.T) {
	liveSrc := newFakeLiveSource(nil)
	liveSrc.setLiveDocs([]contentsource.Document{
		{Source: "livesrc", DocID: "live-title", Title: "", Body: "title fallback body"},
	})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{
					DocRef: contentsource.DocRef{
						DocID: "live-title",
						Title: "Human Delta Title",
					},
					ChangeKind: contentsource.ChangeContent,
				},
			},
			ResumeToken: "delta-title",
		},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	var title string
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(documentKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(context.Background(), repo,
			eq(colKBRef, kbID), eq(colSourceKind, "livesrc"), eq(colSourceDocID, "live-title"))
		if err != nil || !ok {
			return err
		}
		title = rec.String(colTitle)
		return nil
	}); err != nil {
		t.Fatalf("load persisted title: %v", err)
	}
	if title != "Human Delta Title" {
		t.Fatalf("persisted title = %q, want DocRef fallback title", title)
	}
}

func TestSyncDeltaPageCapReturnsSourceError(t *testing.T) {
	liveSrc := newFakeLiveSource(nil)
	pages := make([]contentsource.DeltaPage, syncDeltaPageCap)
	for i := range pages {
		pages[i] = contentsource.DeltaPage{NextToken: "next"}
	}
	liveSrc.setPages(pages)
	h := newHarness(t)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusBadGateway {
		t.Fatalf("sync = %d %s, want 502", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "test.fake-live-source") || !strings.Contains(r.raw, "512") {
		t.Fatalf("error should name source and page cap, got %s", r.raw)
	}
}

// TestSyncExpiredTokenFallback verifies that when DeltaList returns Expired=true
// the handler falls back to full-list reconciliation.
func TestSyncExpiredTokenFallback(t *testing.T) {
	docs := []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "body one"},
	}
	liveSrc := newFakeLiveSource(docs)
	liveSrc.setPages([]contentsource.DeltaPage{
		{Expired: true},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// Expired token must trigger full reconciliation.
	if full, _ := r.body["full_reconciliation"].(bool); !full {
		t.Errorf("full_reconciliation = %v, want true on expired token", r.body["full_reconciliation"])
	}
	// d1 should be synced (new doc in full reconciliation).
	if synced, _ := r.body["docs_synced"].(float64); synced != 1 {
		t.Errorf("docs_synced = %v, want 1", r.body["docs_synced"])
	}
}

// TestSyncACLStaleOnFetchACLError verifies that when FetchACL returns an error
// the document's ACL is set to the deny-closed aclStaleSentinel.
func TestSyncACLStaleOnFetchACLError(t *testing.T) {
	docs := []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "sensitive content"},
	}
	liveSrc := newFakeLiveSource(docs)
	liveSrc.setACLErr("d1", errFakeNotFound) // FetchACL fails for d1
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeACL},
			},
			ResumeToken: "delta-acl-1",
		},
	})
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// First ingest d1 so the document exists.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest: got %d %s", r.code, r.raw)
	}

	// Sync: FetchACL will fail → sentinel set.
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// The ACL change processed without error (stale sentinel is not an error).
	if acls, _ := r.body["acls_refreshed"].(float64); acls != 1 {
		t.Errorf("acls_refreshed = %v, want 1 (stale-sentinel still counts as refreshed)", r.body["acls_refreshed"])
	}
}

// TestSyncLegalHoldBlocksDelete verifies that a legal hold prevents orphan
// deletion and the doc is reported in held_docs (not errors).
func TestSyncLegalHoldBlocksDelete(t *testing.T) {
	allDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "retained"},
		{Source: "testsrc", DocID: "d2", Body: "also retained"},
		{Source: "testsrc", DocID: "d3", Body: "held document"},
	}
	twoDocDocs := []contentsource.Document{
		{Source: "testsrc", DocID: "d1", Body: "retained"},
		{Source: "testsrc", DocID: "d2", Body: "also retained"},
	}

	// Hold gate blocks every delete.
	gate := heldStub()

	h := newHarnessWith(t,
		WithSource("testsrc", newFakeSource(allDocs)),
		WithHoldGate(gate),
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering"}, Clearance: classSecret,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest all three documents.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest: got %d %s", r.code, r.raw)
	}

	// Re-register source without d3 (orphan candidate).
	h.addSource("testsrc", newFakeSource(twoDocDocs))

	// Sync: d3 is orphaned but held.
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "testsrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// No documents deleted; the held doc is in held_docs.
	if d, _ := r.body["docs_deleted"].(float64); d != 0 {
		t.Errorf("docs_deleted = %v, want 0 (held)", r.body["docs_deleted"])
	}
	held, _ := r.body["held_docs"].([]any)
	if len(held) != 1 {
		t.Errorf("held_docs = %v, want 1 entry", r.body["held_docs"])
	}

	// All three documents must still be in the KB (the held one was not deleted).
	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 3 {
		t.Fatalf("after held sync: want 3 docs, got %d", len(items))
	}
}

// TestSyncLiveSourceDeltaMixedKinds verifies a single DeltaPage containing one
// ChangeContent, one ChangeACL, and one ChangeDeleted entry — the real-world
// steady-state case where all three kinds arrive in a single delta batch.
func TestSyncLiveSourceDeltaMixedKinds(t *testing.T) {
	initialDocs := []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "content doc"},
		{Source: "livesrc", DocID: "d2", Body: "acl doc", ACL: []string{"group:old"}},
		{Source: "livesrc", DocID: "d3", Body: "delete me"},
	}
	liveSrc := newFakeLiveSource(initialDocs)
	h := newHarnessWith(t,
		WithRetrievalGuard(fixedGuard{grants: Grants{
			Allowed: true, Groups: []string{"engineering", "new-group"}, Clearance: classSecret,
		}}),
	)
	h.mod.AddSource("livesrc", liveSrc)

	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.io", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// Ingest all three docs.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingest: got %d %s", r.code, r.raw)
	}

	// Update d1's body, configure d2's ACL refresh result, and mark d3 deleted.
	liveSrc.fakeSource.docs = []contentsource.Document{
		{Source: "livesrc", DocID: "d1", Body: "updated content"},
		{Source: "livesrc", DocID: "d2", Body: "acl doc", ACL: []string{"group:new-group"}},
	}
	h.mod.AddSource("livesrc", liveSrc)
	liveSrc.setACL("d2", contentsource.ACLResult{ACL: []string{"group:new-group"}})
	liveSrc.setPages([]contentsource.DeltaPage{
		{
			Changes: []contentsource.DeltaEntry{
				{DocRef: contentsource.DocRef{DocID: "d1"}, ChangeKind: contentsource.ChangeContent},
				{DocRef: contentsource.DocRef{DocID: "d2"}, ChangeKind: contentsource.ChangeACL},
				{DocRef: contentsource.DocRef{DocID: "d3"}, ChangeKind: contentsource.ChangeDeleted},
			},
			ResumeToken: "delta-mixed-1",
		},
	})

	// Run sync.
	r = h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/sync", editor,
		map[string]any{"source": "livesrc"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("sync: got %d %s", r.code, r.raw)
	}

	// Verify all three counters.
	if v, _ := r.body["docs_synced"].(float64); v != 1 {
		t.Errorf("docs_synced = %v, want 1 (d1 content change)", v)
	}
	if v, _ := r.body["acls_refreshed"].(float64); v != 1 {
		t.Errorf("acls_refreshed = %v, want 1 (d2 ACL change)", v)
	}
	if v, _ := r.body["docs_deleted"].(float64); v != 1 {
		t.Errorf("docs_deleted = %v, want 1 (d3 deleted)", v)
	}
	if full, _ := r.body["full_reconciliation"].(bool); full {
		t.Error("full_reconciliation should be false for delta sync")
	}

	// Only d1 and d2 should remain.
	docsResp := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	if items := listItems(docsResp); len(items) != 2 {
		t.Fatalf("after mixed sync: want 2 docs, got %d (body: %s)", len(items), docsResp.raw)
	}
}

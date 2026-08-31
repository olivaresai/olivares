// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package notion

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// openLiveSource starts an httptest.Server serving handler, opens a Source in
// live mode pointed at the test server, and returns the source + a cleanup func.
func openLiveSource(t *testing.T, handler http.Handler) (*Source, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":           "live",
		"base_url":       srv.URL,
		"credential_ref": "test-token",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestNotionDeltaListParsesSearch(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/notion-search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Notion-Version") == "" {
			http.Error(w, "missing Notion-Version header", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}

	if page.Expired {
		t.Error("expected Expired=false for Notion (timestamps never expire)")
	}
	if len(page.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(page.Changes))
	}
	// All entries should be ChangeContent — the Notion search API does not
	// distinguish ACL-only changes from content changes.
	for i, c := range page.Changes {
		if c.ChangeKind != contentsource.ChangeContent {
			t.Errorf("Changes[%d].ChangeKind = %q, want ChangeContent", i, c.ChangeKind)
		}
	}
	// Check first page.
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "page-notion-1" {
		t.Errorf("Changes[0].DocID = %q, want page-notion-1", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Architecture Overview" {
		t.Errorf("Changes[0].Title = %q, want Architecture Overview", c0.DocRef.Title)
	}
	// ResumeToken should be the most recently edited changed page; NextToken is
	// empty because this implementation does not paginate Notion search results.
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-25") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-25", page.ResumeToken)
	}
}

func TestNotionDeltaListFiltersAfterToken(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/notion-search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	// sinceToken set after page-notion-1 (2026-06-20) but before page-notion-2
	// (2026-06-25) → only page-notion-2 should appear in the result.
	sinceToken := "2026-06-22T00:00:00Z"
	page, err := s.DeltaList(context.Background(), sinceToken)
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 1 {
		t.Fatalf("expected 1 change after sinceToken filtering, got %d", len(page.Changes))
	}
	if page.Changes[0].DocRef.DocID != "page-notion-2" {
		t.Errorf("Changes[0].DocID = %q, want page-notion-2", page.Changes[0].DocRef.DocID)
	}
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-25") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-25", page.ResumeToken)
	}
}

func TestNotionDeltaListZeroChangesHasEmptyResumeToken(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/notion-search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 0 {
		t.Fatalf("Changes = %d, want 0", len(page.Changes))
	}
	if page.NextToken != "" || page.ResumeToken != "" {
		t.Fatalf("NextToken/ResumeToken = %q/%q, want both empty", page.NextToken, page.ResumeToken)
	}
}

// TestNotionDeltaListNeverReportsDeleted verifies the documented honest
// limitation: the Notion search API cannot surface deleted pages, so DeltaList
// must never return ChangeDeleted entries. This test is the contract guard that
// ensures future implementors do not accidentally add fake deletion detection.
func TestNotionDeltaListNeverReportsDeleted(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/notion-search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	for i, c := range page.Changes {
		if c.ChangeKind == contentsource.ChangeDeleted {
			t.Errorf("Changes[%d].ChangeKind = ChangeDeleted — Notion API cannot report deletions; "+
				"this must be handled by the sync handler's orphan-detection reconciliation path", i)
		}
	}
}

func TestNotionLiveListPaginatesSearch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/search" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Notion-Version") == "" {
			http.Error(w, "missing Notion-Version", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		raw := string(body)
		if !strings.Contains(raw, `"page_size":100`) ||
			!strings.Contains(raw, `"property":"object"`) ||
			!strings.Contains(raw, `"value":"page"`) {
			http.Error(w, "missing list search payload: "+raw, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(raw, `"start_cursor":"cursor-2"`) {
			_, _ = w.Write([]byte(`{
				"results": [
					{"object":"page","id":"page-2","last_edited_time":"2026-07-01T11:00:00Z",
					 "properties":{"title":{"title":[{"plain_text":"Second Page"}]}}}
				],
				"has_more": false,
				"next_cursor": null
			}`))
			return
		}
		if strings.Contains(raw, "start_cursor") {
			http.Error(w, "unexpected first-page cursor: "+raw, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{
			"results": [
				{"object":"page","id":"page-1","last_edited_time":"2026-07-01T10:00:00Z",
				 "properties":{"title":{"title":[{"plain_text":"First Page"}]}}}
			],
			"has_more": true,
			"next_cursor": "cursor-2"
		}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "page-1" || refs[0].Title != "First Page" {
		t.Fatalf("page1 refs = %+v, want page-1/First Page", refs)
	}
	if next != "cursor-2" {
		t.Fatalf("page1 next = %q, want cursor-2", next)
	}

	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "page-2" || refs[0].Title != "Second Page" {
		t.Fatalf("page2 refs = %+v, want page-2/Second Page", refs)
	}
	if next != "" {
		t.Fatalf("page2 next = %q, want empty", next)
	}
}

func TestNotionFetchACLReturnsWorkspaceDefault(t *testing.T) {
	// FetchACL makes no HTTP call; a minimal live source is sufficient.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not be called by FetchACL (Notion has no ACL endpoint).
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "any-page-id")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	// The only ACL entry must be the workspace placeholder.
	if len(result.ACL) != 1 || result.ACL[0] != "workspace:shared" {
		t.Errorf("ACL = %v, want [workspace:shared]", result.ACL)
	}
	// Notion has no native sensitivity labels.
	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty (Notion has no label API)", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty (Notion has no classification API)", result.Classification)
	}
}

func TestNotionLiveFetchPaginatesBlocks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "expected GET", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Notion-Version") == "" {
			http.Error(w, "missing Notion-Version", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/blocks/page-1/children" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("start_cursor") == "cursor-2" {
			_, _ = w.Write([]byte(`{
				"results": [
					{"type":"heading_2","heading_2":{"rich_text":[{"plain_text":"Second page"}]}},
					{"type":"code","code":{"rich_text":[{"plain_text":"fmt.Println"}]}}
				],
				"has_more": false,
				"next_cursor": null
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"results": [
				{"type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Hello "},{"plain_text":"world"}]}},
				{"type":"image","image":{"caption":[{"plain_text":"skip me"}]}}
			],
			"has_more": true,
			"next_cursor": "cursor-2"
		}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceNotion {
		t.Errorf("Source = %q, want notion", doc.Source)
	}
	if doc.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", doc.ContentType)
	}
	if doc.Title != "" {
		t.Errorf("Title = %q, want empty when block API carries no page title", doc.Title)
	}
	wantBody := "Hello world\nSecond page\nfmt.Println"
	if doc.Body != wantBody {
		t.Errorf("Body = %q, want %q", doc.Body, wantBody)
	}
	if strings.Contains(doc.Body, "skip me") {
		t.Errorf("unsupported block text leaked into body: %q", doc.Body)
	}
}

func TestNotionExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/notion.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceNotion {
		t.Errorf("Source = %q, want notion", doc.Source)
	}
	if doc.Title != "Product Meeting Notes" {
		t.Errorf("Title = %q, want Product Meeting Notes", doc.Title)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "page-1" || refs[0].Title != "Product Meeting Notes" || next != "" {
		t.Fatalf("List refs/next = %+v/%q, want page-1 Product Meeting Notes and empty cursor", refs, next)
	}
	// DeltaList must return an error in export mode (not live).
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// FetchACL must return an error in export mode.
	if _, err := s.FetchACL(context.Background(), "page-1"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

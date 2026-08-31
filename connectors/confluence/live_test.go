// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package confluence

import (
	"context"
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
		"space_key":      "ENG",
		"base_url":       srv.URL,
		"credential_ref": "test-token",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestConfluenceDeltaListParsesPages(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/confluence-pages.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
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
		t.Error("expected Expired=false for Confluence (timestamps never expire)")
	}
	if len(page.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(page.Changes))
	}
	// All entries should be ChangeContent — the Confluence pages API does not
	// distinguish ACL-only changes from content changes.
	for i, c := range page.Changes {
		if c.ChangeKind != contentsource.ChangeContent {
			t.Errorf("Changes[%d].ChangeKind = %q, want ChangeContent", i, c.ChangeKind)
		}
	}
	// First page.
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "page-1" {
		t.Errorf("Changes[0].DocID = %q, want page-1", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Architecture Overview" {
		t.Errorf("Changes[0].Title = %q, want Architecture Overview", c0.DocRef.Title)
	}
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-25") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-25", page.ResumeToken)
	}
}

func TestConfluenceDeltaListFiltersAfterToken(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/confluence-pages.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	// sinceToken set after page-1 (2026-06-20) but before page-2 (2026-06-25)
	// → only page-2 should appear in the result.
	sinceToken := "2026-06-22T00:00:00Z"
	page, err := s.DeltaList(context.Background(), sinceToken)
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 1 {
		t.Fatalf("expected 1 change after sinceToken filtering, got %d", len(page.Changes))
	}
	if page.Changes[0].DocRef.DocID != "page-2" {
		t.Errorf("Changes[0].DocID = %q, want page-2", page.Changes[0].DocRef.DocID)
	}
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-25") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-25", page.ResumeToken)
	}
}

func TestConfluenceLiveListPaginates(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "2" {
			_, _ = w.Write([]byte(`{"results":[{"id":"page-2","title":"Second","spaceId":"space-1","version":{"createdAt":"2026-07-01T11:00:00Z"}}]}`))
			return
		}
		next := "http://" + r.Host + "/wiki/api/v2/spaces/ENG/pages?cursor=2"
		_, _ = w.Write([]byte(`{"results":[{"id":"page-1","title":"First","spaceId":"space-1","version":{"createdAt":"2026-07-01T10:00:00Z"}}],"_links":{"next":"` + next + `"}}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "page-1" || refs[0].Title != "First" || next == "" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "page-2" || refs[0].Title != "Second" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestConfluenceLiveFetchStorageBody(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wiki/rest/api/content/page-1" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if !strings.Contains(r.URL.RawQuery, "body.storage") {
			http.Error(w, "missing expand", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"page-1",
			"title":"Architecture",
			"space":{"key":"ENG"},
			"body":{"storage":{"value":"<p>hello</p>"}},
			"version":{"when":"2026-07-01T10:00:00Z"}
		}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Architecture" || doc.Body != "<p>hello</p>" || doc.ContentType != "text/html" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestConfluenceFetchACLParsesPermissions(t *testing.T) {
	pageFixture := []byte(`{"id":"page-1","title":"Architecture Overview","spaceId":"space-abc","version":{"createdAt":"2026-06-20T09:00:00.000Z"}}`)
	permsFixture, err := os.ReadFile("testdata/delta/confluence-perms.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/permissions") {
			_, _ = w.Write(permsFixture)
		} else {
			_, _ = w.Write(pageFixture)
		}
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "page-1")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	// Only group principals with read permission should appear in ACL:
	//   perm-1: group engineers, read  → included
	//   perm-2: user admin, read       → excluded (not a group)
	//   perm-3: group editors, write   → excluded (not read)
	if got := strings.Join(result.ACL, ","); got != "group:engineers" {
		t.Errorf("ACL = %q, want group:engineers", got)
	}
	// Confluence has no native sensitivity labels.
	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty", result.Classification)
	}
}

func TestConfluenceExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/confluence.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "12345")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceConfluence {
		t.Errorf("Source = %q, want confluence", doc.Source)
	}
	if doc.Title != "Service Restart Runbook" {
		t.Errorf("Title = %q, want Service Restart Runbook", doc.Title)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "12345" || refs[0].Title != "Service Restart Runbook" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	// DeltaList must return an error in export mode (not live).
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// FetchACL must return an error in export mode.
	if _, err := s.FetchACL(context.Background(), "12345"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

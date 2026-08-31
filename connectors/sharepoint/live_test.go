// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sharepoint

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
		"site_id":        "site1",
		"drive_id":       "drive1",
		"credential_ref": "test-token",
		"graph_base":     srv.URL,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestDeltaListParsesGraphResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/graph-delta.json")
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
		t.Error("expected Expired=false")
	}
	if len(page.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(page.Changes))
	}

	// item-1: has permissions + sensitivityLabel → ChangeACL
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "item-1" {
		t.Errorf("Changes[0].DocID = %q, want item-1", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "doc.docx" {
		t.Errorf("Changes[0].Title = %q, want doc.docx", c0.DocRef.Title)
	}
	if c0.ChangeKind != contentsource.ChangeACL {
		t.Errorf("Changes[0].ChangeKind = %q, want %q", c0.ChangeKind, contentsource.ChangeACL)
	}

	// item-2: deleted facet → ChangeDeleted
	c1 := page.Changes[1]
	if c1.DocRef.DocID != "item-2" {
		t.Errorf("Changes[1].DocID = %q, want item-2", c1.DocRef.DocID)
	}
	if c1.ChangeKind != contentsource.ChangeDeleted {
		t.Errorf("Changes[1].ChangeKind = %q, want %q", c1.ChangeKind, contentsource.ChangeDeleted)
	}

	// The fixture is a final Graph delta page: deltaLink is the resume token.
	want := "https://graph.microsoft.com/v1.0/sites/site1/drive/root/delta?token=new-token"
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty final-page pagination token", page.NextToken)
	}
	if page.ResumeToken != want {
		t.Errorf("ResumeToken = %q, want %q", page.ResumeToken, want)
	}
}

func TestDeltaListSplitsNextAndResumeTokens(t *testing.T) {
	var srv *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{
				"@odata.deltaLink": "` + srv.URL + `/delta?token=resume",
				"value": [{"id":"item-2","name":"second.txt"}]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"@odata.nextLink": "` + srv.URL + `/delta?page=2",
			"value": [{"id":"item-1","name":"first.txt"}]
		}`))
	})
	srv = httptest.NewServer(handler)
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":       "live",
		"site_id":    "site1",
		"drive_id":   "drive1",
		"graph_base": srv.URL,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}

	page1, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList page1: %v", err)
	}
	if page1.NextToken != srv.URL+"/delta?page=2" {
		t.Fatalf("page1 NextToken = %q", page1.NextToken)
	}
	if page1.ResumeToken != "" {
		t.Fatalf("page1 ResumeToken = %q, want empty", page1.ResumeToken)
	}

	page2, err := s.DeltaList(context.Background(), page1.NextToken)
	if err != nil {
		t.Fatalf("DeltaList page2: %v", err)
	}
	if page2.NextToken != "" {
		t.Fatalf("page2 NextToken = %q, want empty", page2.NextToken)
	}
	if page2.ResumeToken != srv.URL+"/delta?token=resume" {
		t.Fatalf("page2 ResumeToken = %q", page2.ResumeToken)
	}
}

func TestDeltaListUsesTokenAsDeltaLink(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/graph-delta.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var calledURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledURL = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":       "live",
		"site_id":    "site1",
		"graph_base": srv.URL,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Pass the full test-server URL as sinceToken; it should be used verbatim.
	sinceToken := srv.URL + "/sites/site1/drive/root/delta?token=abc"
	_, err = s.DeltaList(context.Background(), sinceToken)
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if !strings.HasSuffix(calledURL, "?token=abc") {
		t.Errorf("expected request to delta link, got %q", calledURL)
	}
}

func TestLiveListPaginatesAndSkipsDeleted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			if r.URL.Path != "/delta" {
				http.Error(w, "unexpected cursor path "+r.URL.Path, http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{
				"@odata.deltaLink": "ignored-by-list",
				"value": [{"id":"item-3","name":"final.txt","lastModifiedDateTime":"2026-07-01T10:00:00Z"}]
			}`))
			return
		}
		if r.URL.Path != "/sites/site1/drive/root/delta" {
			http.Error(w, "unexpected start path "+r.URL.Path, http.StatusNotFound)
			return
		}
		next := "http://" + r.Host + "/delta?page=2"
		_, _ = w.Write([]byte(`{
			"@odata.nextLink": "` + next + `",
			"value": [
				{"id":"item-1","name":"first.txt","lastModifiedDateTime":"2026-07-01T09:00:00Z"},
				{"id":"item-2","name":"deleted.txt","deleted":{"state":"deleted"}}
			]
		}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "item-1" || refs[0].Title != "first.txt" {
		t.Fatalf("page1 refs = %+v, want item-1/first.txt only", refs)
	}
	if next == "" {
		t.Fatal("page1 next cursor is empty, want Graph nextLink")
	}

	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "item-3" || refs[0].Title != "final.txt" {
		t.Fatalf("page2 refs = %+v, want item-3/final.txt", refs)
	}
	if next != "" {
		t.Fatalf("page2 next = %q, want empty final cursor", next)
	}
}

func TestDeltaListExpiredToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if !page.Expired {
		t.Error("expected Expired=true on HTTP 410")
	}
}

func TestDeltaListNotFoundExpired(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if !page.Expired {
		t.Error("expected Expired=true on HTTP 404")
	}
}

func TestFetchACLParsesPermissions(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/graph-acl.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "item-3")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	if got := strings.Join(result.ACL, ","); got != "group:Members" {
		t.Errorf("ACL = %q, want group:Members", got)
	}
	if got := strings.Join(result.ExternalLabels, ","); got != "purview:confidential" {
		t.Errorf("ExternalLabels = %q, want purview:confidential", got)
	}
	if result.Classification != "confidential" {
		t.Errorf("Classification = %q, want confidential", result.Classification)
	}
}

func TestFetchACLUsesDefaultDriveWhenDriveIDEmpty(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/graph-acl.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var calledPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":       "live",
		"site_id":    "site1",
		"graph_base": srv.URL,
		// drive_id intentionally absent
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := s.FetchACL(context.Background(), "item-3"); err != nil {
		t.Fatalf("FetchACL: %v", err)
	}
	// Should use sites/{siteId}/drive/items/{itemId} path
	if !strings.Contains(calledPath, "/sites/site1/drive/items/item-3") {
		t.Errorf("expected sites/*/drive path, got %q", calledPath)
	}
}

func TestLiveFetchDownloadsContentWithRedirect(t *testing.T) {
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("sharepoint live body"))
	}))
	defer download.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drives/drive1/items/item-9/content" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, download.URL+"/download", http.StatusFound)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "item-9")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceSharePoint {
		t.Errorf("Source = %q, want sharepoint", doc.Source)
	}
	if doc.DocID != "item-9" || doc.Title != "" {
		t.Errorf("DocID/Title = %q/%q, want item-9/empty", doc.DocID, doc.Title)
	}
	if doc.Body != "sharepoint live body" {
		t.Errorf("Body = %q", doc.Body)
	}
	if doc.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("ContentType = %q", doc.ContentType)
	}
}

func TestLiveFetchReturnsHTTPError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	_, err := s.Fetch(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("Fetch error = %v, want HTTP 404", err)
	}
}

func TestExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/sharepoint.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "item-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceSharePoint {
		t.Errorf("Source = %q, want sharepoint", doc.Source)
	}
	if doc.Title != "Leave Policy.docx" {
		t.Errorf("Title = %q", doc.Title)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "item-1" || refs[0].Title != "Leave Policy.docx" || next != "" {
		t.Fatalf("List refs/next = %+v/%q, want item-1 Leave Policy.docx and empty cursor", refs, next)
	}
	// Verify DeltaList returns error in export mode (not live)
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// Verify FetchACL returns error in export mode
	if _, err := s.FetchACL(context.Background(), "item-1"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

func TestExternalLabelsPopulatedFromSensitivityLabel(t *testing.T) {
	// Use existing export testdata; "Confidential" label should produce purview:confidential.
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/sharepoint.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "item-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(doc.ExternalLabels) != 1 || doc.ExternalLabels[0] != "purview:confidential" {
		t.Errorf("ExternalLabels = %v, want [purview:confidential]", doc.ExternalLabels)
	}
	if doc.Classification != "confidential" {
		t.Errorf("Classification = %q, want confidential", doc.Classification)
	}
}

func TestLiveModeRejectsEmptySiteID(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": "live",
		// site_id intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "site_id") {
		t.Fatalf("expected site_id error, got %v", err)
	}
}

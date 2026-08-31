// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gdrive

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

// openLiveGDriveSource starts an httptest.Server serving handler, opens a
// Source in live mode pointed at the test server, and returns the source plus
// a cleanup func.
func openLiveGDriveSource(t *testing.T, handler http.Handler) (*Source, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":           "live",
		"drive_id":       "drive1",
		"credential_ref": "test-token",
		"api_base":       srv.URL,
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestGDriveDeltaListParsesChanges(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/drive-changes.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Dispatch: startPageToken request vs. changes request.
		if strings.HasSuffix(r.URL.Path, "/changes/startPageToken") {
			_, _ = w.Write([]byte(`{"startPageToken":"page-1"}`))
			return
		}
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveGDriveSource(t, handler)
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

	// file-abc: not removed → ChangeContent
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "file-abc" {
		t.Errorf("Changes[0].DocID = %q, want file-abc", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Design Doc.docx" {
		t.Errorf("Changes[0].Title = %q, want Design Doc.docx", c0.DocRef.Title)
	}
	if c0.ChangeKind != contentsource.ChangeContent {
		t.Errorf("Changes[0].ChangeKind = %q, want %q", c0.ChangeKind, contentsource.ChangeContent)
	}

	// file-xyz: removed=true → ChangeDeleted
	c1 := page.Changes[1]
	if c1.DocRef.DocID != "file-xyz" {
		t.Errorf("Changes[1].DocID = %q, want file-xyz", c1.DocRef.DocID)
	}
	if c1.ChangeKind != contentsource.ChangeDeleted {
		t.Errorf("Changes[1].ChangeKind = %q, want %q", c1.ChangeKind, contentsource.ChangeDeleted)
	}

	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty final-page pagination token", page.NextToken)
	}
	if page.ResumeToken != "next-sync-token-456" {
		t.Errorf("ResumeToken = %q, want next-sync-token-456", page.ResumeToken)
	}
}

func TestGDriveDeltaListSplitsNextAndResumeTokens(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/changes/startPageToken") {
			_, _ = w.Write([]byte(`{"startPageToken":"page-1"}`))
			return
		}
		if r.URL.Query().Get("pageToken") == "page-2" {
			_, _ = w.Write([]byte(`{"newStartPageToken":"resume-2","changes":[{"fileId":"file-2","file":{"id":"file-2","name":"second.txt"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"nextPageToken":"page-2","changes":[{"fileId":"file-1","file":{"id":"file-1","name":"first.txt"}}]}`))
	})
	s, cleanup := openLiveGDriveSource(t, handler)
	defer cleanup()

	page1, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList page1: %v", err)
	}
	if page1.NextToken != "page-2" || page1.ResumeToken != "" {
		t.Fatalf("page1 NextToken/ResumeToken = %q/%q, want page-2/empty", page1.NextToken, page1.ResumeToken)
	}
	page2, err := s.DeltaList(context.Background(), page1.NextToken)
	if err != nil {
		t.Fatalf("DeltaList page2: %v", err)
	}
	if page2.NextToken != "" || page2.ResumeToken != "resume-2" {
		t.Fatalf("page2 NextToken/ResumeToken = %q/%q, want empty/resume-2", page2.NextToken, page2.ResumeToken)
	}
}

func TestGDriveLiveListPaginates(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "cursor-2" {
			_, _ = w.Write([]byte(`{"files":[{"id":"file-2","name":"Second.txt","mimeType":"text/plain","modifiedTime":"2026-07-01T11:00:00Z"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"nextPageToken":"cursor-2","files":[{"id":"file-1","name":"First.gdoc","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-07-01T10:00:00Z"}]}`))
	})
	s, cleanup := openLiveGDriveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "file-1" || refs[0].Title != "First.gdoc" || next != "cursor-2" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "file-2" || refs[0].Title != "Second.txt" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestGDriveLiveFetchExportsNativeAndDownloadsBinary(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/files/native-doc" && r.URL.Query().Get("fields") != "":
			_, _ = w.Write([]byte(`{"id":"native-doc","name":"Native Doc","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-07-01T10:00:00Z","parents":["folder-1"]}`))
		case r.URL.Path == "/files/native-doc/export":
			if r.URL.Query().Get("mimeType") != "text/plain" {
				http.Error(w, "missing export mimeType", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("exported text"))
		case r.URL.Path == "/files/binary-doc" && r.URL.Query().Get("fields") != "":
			_, _ = w.Write([]byte(`{"id":"binary-doc","name":"Binary.pdf","mimeType":"application/pdf","modifiedTime":"2026-07-01T11:00:00Z"}`))
		case r.URL.Path == "/files/binary-doc" && r.URL.Query().Get("alt") == "media":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("pdf bytes"))
		default:
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusNotFound)
		}
	})
	s, cleanup := openLiveGDriveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "native-doc")
	if err != nil {
		t.Fatalf("Fetch native: %v", err)
	}
	if doc.Title != "Native Doc" || doc.Body != "exported text" || doc.ContentType != "text/plain" {
		t.Fatalf("native doc = %+v", doc)
	}
	doc, err = s.Fetch(context.Background(), "binary-doc")
	if err != nil {
		t.Fatalf("Fetch binary: %v", err)
	}
	if doc.Title != "Binary.pdf" || doc.Body != "pdf bytes" || doc.ContentType != "application/pdf" {
		t.Fatalf("binary doc = %+v", doc)
	}
}

func TestGDriveDeltaListExpiredToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})
	s, cleanup := openLiveGDriveSource(t, handler)
	defer cleanup()

	// Pass an existing token so DeltaList goes straight to the changes endpoint
	// (skips the startPageToken fetch) and gets the 410 directly.
	page, err := s.DeltaList(context.Background(), "existing-token")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if !page.Expired {
		t.Error("expected Expired=true on HTTP 410")
	}
}

func TestGDriveFetchACLParsesPermissions(t *testing.T) {
	fixture, err := os.ReadFile("testdata/delta/drive-acl.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})
	s, cleanup := openLiveGDriveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "file-pqr")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	// Permissions: group perm-a and user perm-b (opaque IDs, never emails).
	if got := strings.Join(result.ACL, ","); got != "group:perm-a,user:perm-b" {
		t.Errorf("ACL = %q, want group:perm-a,user:perm-b", got)
	}
	// Labels surface as "gdrive:<label-id>".
	if got := strings.Join(result.ExternalLabels, ","); got != "gdrive:sensitivity-lbl" {
		t.Errorf("ExternalLabels = %q, want gdrive:sensitivity-lbl", got)
	}
	for _, a := range result.ACL {
		if strings.Contains(a, "@") {
			t.Errorf("ACL entry must not contain an email (PII): %q", a)
		}
	}
}

func TestGDriveExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/drive.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "1aBcStrategy")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceGDrive {
		t.Errorf("Source = %q, want gdrive", doc.Source)
	}
	if doc.Title != "Q3 Strategy" {
		t.Errorf("Title = %q, want Q3 Strategy", doc.Title)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 2 || refs[0].DocID != "1aBcStrategy" || refs[0].Title != "Q3 Strategy" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	// DeltaList must return an error in export mode (no live client).
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// FetchACL must return an error in export mode.
	if _, err := s.FetchACL(context.Background(), "1aBcStrategy"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

func TestGDriveExternalLabelsFromAppProperties(t *testing.T) {
	// toDocument should populate ExternalLabels when sensitivity_label is present
	// in appProperties, using lower-cased "gdrive:<value>" format.
	df := driveFile{
		ID:   "test-doc",
		Name: "Test Document",
		AppProperties: map[string]string{
			"sensitivity_label": "Internal",
		},
	}
	doc := toDocument(df)
	if len(doc.ExternalLabels) != 1 || doc.ExternalLabels[0] != "gdrive:internal" {
		t.Errorf("ExternalLabels = %v, want [gdrive:internal]", doc.ExternalLabels)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package salesforce

import (
	"context"
	"net/http"
	"net/http/httptest"
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
		"client_id":      "test-client",
		"username":       "test@example.com",
		"sobject_types":  "Knowledge__kav",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestSalesforceDeltaListIncremental(t *testing.T) {
	// Two records: one older, one newer than the sinceToken.
	fixture := []byte(`{
		"records": [
			{
				"attributes": {"type": "Knowledge__kav"},
				"Id": "ka0Dn000000001",
				"Name": "Old Article",
				"Description": "Older content.",
				"SystemModstamp": "2026-05-10T08:00:00.000Z",
				"OwnerId": "005Dn000001AAA"
			},
			{
				"attributes": {"type": "Knowledge__kav"},
				"Id": "ka0Dn000000002",
				"Name": "New Article",
				"Description": "Newer content.",
				"SystemModstamp": "2026-05-20T12:00:00.000Z",
				"OwnerId": "005Dn000001BBB"
			}
		]
	}`)

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

	// Full sync (no sinceToken) should return both records.
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
	for i, c := range page.Changes {
		if c.ChangeKind != contentsource.ChangeContent {
			t.Errorf("Changes[%d].ChangeKind = %q, want ChangeContent", i, c.ChangeKind)
		}
	}

	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-05-20") {
		t.Errorf("ResumeToken = %q, want date containing 2026-05-20", page.ResumeToken)
	}

	// Verify first record fields.
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "ka0Dn000000001" {
		t.Errorf("Changes[0].DocID = %q", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Old Article" {
		t.Errorf("Changes[0].Title = %q", c0.DocRef.Title)
	}
}

func TestSalesforceLiveListPaginates(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/query/01gNEXT") {
			_, _ = w.Write([]byte(`{"done":true,"records":[{"Id":"ka0Dn00000000Ac","Name":"Second","SystemModstamp":"2026-07-01T11:00:00.000Z"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"done":false,"nextRecordsUrl":"/services/data/v59.0/query/01gNEXT","records":[{"Id":"ka0Dn00000000Ab","Name":"First","SystemModstamp":"2026-07-01T10:00:00.000Z"}]}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "ka0Dn00000000Ab" || refs[0].Title != "First" || next == "" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "ka0Dn00000000Ac" || refs[0].Title != "Second" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestSalesforceLiveFetchSerializesRecordJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/sobjects/Knowledge__kav/ka0Dn00000000Ab") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"attributes":{"type":"Knowledge__kav"},"Id":"ka0Dn00000000Ab","Name":"Article","Description":"Body","SystemModstamp":"2026-07-01T10:00:00.000Z","OwnerId":"005OWNER"}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "ka0Dn00000000Ab")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Article" || doc.ContentType != "application/json" || !strings.Contains(doc.Body, `"OwnerId":"005OWNER"`) {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestSalesforceFetchACL(t *testing.T) {
	// sObject REST endpoint returns a single record (not a query result).
	fixture := []byte(`{
		"attributes": {"type": "Knowledge__kav"},
		"Id": "ka0Dn00000000Ab",
		"OwnerId": "005Dn00000OWNER1"
	}`)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		// Verify the request uses the sObject endpoint, not SOQL.
		if !strings.Contains(r.URL.Path, "/sobjects/") {
			http.Error(w, "expected sObject endpoint", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	})

	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "ka0Dn00000000Ab")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	if got := strings.Join(result.ACL, ","); got != "owner:005Dn00000OWNER1" {
		t.Errorf("ACL = %q, want owner:005Dn00000OWNER1", got)
	}

	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty", result.Classification)
	}
}

func TestSalesforceFetchACLRejectsInvalidID(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":          "live",
		"base_url":      "https://example.salesforce.com",
		"sobject_types": "Account",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// SOQL injection attempt
	_, err := s.FetchACL(context.Background(), "' OR '1'='1")
	if err == nil || !strings.Contains(err.Error(), "invalid record ID") {
		t.Fatalf("expected invalid ID rejection, got %v", err)
	}
}

func TestSalesforceExportModeRejectsLiveMethods(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/salesforce.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "ka0Dn000000EXAMPLE")
	if err != nil {
		t.Fatalf("Fetch export: %v", err)
	}
	if doc.Source != contentsource.SourceSalesforce {
		t.Errorf("Source = %q, want salesforce", doc.Source)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List export: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "ka0Dn000000EXAMPLE" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	if _, err := s.FetchACL(context.Background(), "ka0Dn000000EXAMPLE"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

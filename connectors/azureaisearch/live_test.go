// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureaisearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/sdk"
)

// openLiveSource starts an httptest.Server serving handler, opens a Source in
// live mode pointed at the test server, and returns the source + a cleanup func.
func openLiveSource(t *testing.T, handler http.Handler, securityField string) (*Source, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":            "live",
		"endpoint":        srv.URL,
		"index_name":      "policies",
		"credential_ref":  "test-api-key",
		"auth_scheme":     "api_key",
		"security_field":  securityField,
		"timestamp_field": "lastModified",
		"content_field":   "content",
		"title_field":     "title",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestAzureAISearchDeltaListIncremental(t *testing.T) {
	resp := searchResponse{
		Value: []map[string]any{
			{
				"id":           "doc-1",
				"title":        "Policy Alpha",
				"content":      "Alpha content",
				"lastModified": "2026-06-20T09:00:00Z",
			},
			{
				"id":           "doc-2",
				"title":        "Policy Beta",
				"content":      "Beta content",
				"lastModified": "2026-06-25T14:30:00Z",
			},
		},
	}
	respBytes, _ := json.Marshal(resp)

	var receivedFilter string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "test-api-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		// Capture the request body to verify filter.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if f, ok := body["filter"]; ok {
				receivedFilter, _ = f.(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	})
	s, cleanup := openLiveSource(t, handler, "security_filter")
	defer cleanup()

	// First call without sinceToken — should not have a filter.
	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if receivedFilter != "" {
		t.Errorf("expected no filter for empty sinceToken, got %q", receivedFilter)
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
	if page.Changes[0].DocRef.DocID != "doc-1" {
		t.Errorf("Changes[0].DocID = %q, want doc-1", page.Changes[0].DocRef.DocID)
	}
	if page.Changes[1].DocRef.DocID != "doc-2" {
		t.Errorf("Changes[1].DocID = %q, want doc-2", page.Changes[1].DocRef.DocID)
	}
	if page.Changes[1].DocRef.Title != "Policy Beta" {
		t.Errorf("Changes[1].Title = %q, want Policy Beta", page.Changes[1].DocRef.Title)
	}
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-25") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-25", page.ResumeToken)
	}

	// Second call with sinceToken — should include a filter.
	receivedFilter = ""
	_, err = s.DeltaList(context.Background(), "2026-06-22T00:00:00Z")
	if err != nil {
		t.Fatalf("DeltaList with token: %v", err)
	}
	if !strings.Contains(receivedFilter, "lastModified gt 2026-06-22T00:00:00Z") {
		t.Errorf("expected filter with sinceToken, got %q", receivedFilter)
	}
}

func TestAzureAISearchDeltaListEmpty(t *testing.T) {
	resp := searchResponse{Value: []map[string]any{}}
	respBytes, _ := json.Marshal(resp)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	})
	s, cleanup := openLiveSource(t, handler, "")
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(page.Changes))
	}
	if page.NextToken != "" {
		t.Errorf("expected empty NextToken, got %q", page.NextToken)
	}
	if page.ResumeToken != "" {
		t.Errorf("expected empty ResumeToken, got %q", page.ResumeToken)
	}
	if page.Expired {
		t.Error("expected Expired=false")
	}
}

func TestAzureAISearchLiveListPaginates(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "expected GET", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("api-key") != "test-api-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skip") == "1" {
			_, _ = w.Write([]byte(`{"value":[{"id":"doc-2","title":"Second","content":"B","lastModified":"2026-07-01T11:00:00Z"}]}`))
			return
		}
		next := "http://" + r.Host + "/indexes/policies/docs?api-version=2024-07-01&%24skip=1"
		_, _ = w.Write([]byte(`{"value":[{"id":"doc-1","title":"First","content":"A","lastModified":"2026-07-01T10:00:00Z"}],"@odata.nextLink":"` + next + `"}`))
	})
	s, cleanup := openLiveSource(t, handler, "security_filter")
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "doc-1" || refs[0].Title != "First" || next == "" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "doc-2" || refs[0].Title != "Second" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestAzureAISearchLiveFetchSerializesSelectedFields(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/docs/doc-1") {
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("api-key") != "test-api-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"doc-1","title":"Policy","content":"Body","lastModified":"2026-07-01T10:00:00Z","security_filter":["hr"]}`))
	})
	s, cleanup := openLiveSource(t, handler, "security_filter")
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Policy" || doc.ContentType != "application/json" || !strings.Contains(doc.Body, `"security_filter":["hr"]`) {
		t.Fatalf("doc = %+v", doc)
	}
	if got := strings.Join(doc.ACL, ","); got != "principal:hr" {
		t.Fatalf("ACL = %q, want principal:hr", got)
	}
}

func TestAzureAISearchFetchACL(t *testing.T) {
	docResp := map[string]any{
		"id":              "doc-1",
		"security_filter": []any{"hr_team", "all_managers"},
	}
	respBytes, _ := json.Marshal(docResp)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "test-api-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	})
	s, cleanup := openLiveSource(t, handler, "security_filter")
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}
	if got := strings.Join(result.ACL, ","); got != "principal:hr_team,principal:all_managers" {
		t.Errorf("ACL = %q, want principal:hr_team,principal:all_managers", got)
	}
	// Azure AI Search has no native sensitivity labels.
	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty", result.Classification)
	}
}

func TestAzureAISearchFetchACLNoField(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should never be called — no security field configured.
		t.Error("unexpected HTTP request when no security_field configured")
		w.WriteHeader(http.StatusOK)
	})
	s, cleanup := openLiveSource(t, handler, "") // empty security_field
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}
	if len(result.ACL) != 0 {
		t.Errorf("ACL = %v, want empty (no security field)", result.ACL)
	}
}

func TestAzureAISearchExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path":     "testdata/azureaisearch.json",
		"index_name":      "policies",
		"security_field":  "security_filter",
		"timestamp_field": "lastModified",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "doc-policy-remote-work")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceAzureAISearch {
		t.Errorf("Source = %q, want azure_ai_search", doc.Source)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "doc-policy-remote-work" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	// DeltaList must return an error in export mode (not live).
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// FetchACL must return an error in export mode.
	if _, err := s.FetchACL(context.Background(), "doc-policy-remote-work"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

func TestAzureAISearchLiveModeRejectsEmptyEndpoint(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":       "live",
		"index_name": "policies",
		// endpoint intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected endpoint error, got %v", err)
	}
}

func TestAzureAISearchLiveModeRejectsEmptyIndexName(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":     "live",
		"endpoint": "https://example.search.windows.net",
		// index_name intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "index_name") {
		t.Fatalf("expected index_name error, got %v", err)
	}
}

func TestAzureAISearchManagedIdentityAuth(t *testing.T) {
	var receivedAuthHeader string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		resp := searchResponse{Value: []map[string]any{}}
		respBytes, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":           "live",
		"endpoint":       srv.URL,
		"index_name":     "policies",
		"credential_ref": "managed-id-token",
		"auth_scheme":    "managed_identity",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.DeltaList(context.Background(), ""); err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if receivedAuthHeader != "Bearer managed-id-token" {
		t.Errorf("Authorization = %q, want 'Bearer managed-id-token'", receivedAuthHeader)
	}
}

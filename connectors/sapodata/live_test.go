// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sapodata

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
		"service_path":   "/sap/opu/odata4/sap/API_MATERIAL",
		"credential_ref": "dGVzdC1jcmVk",
		"auth_scheme":    "basic",
		"entity_sets":    "Materials",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	return s, srv.Close
}

func TestSAPODataDeltaListIncremental(t *testing.T) {
	deltaResp := `{
		"value": [
			{
				"@odata.id": "Materials('MAT001')",
				"@odata.etag": "W/\"20260420083000\"",
				"Name": "Steel Plate A4",
				"Description": "High-grade steel plate.",
				"LastChangeDateTime": "2026-04-20T08:30:00Z",
				"AuthorizationGroup": "MM_PURCHASER"
			},
			{
				"@odata.id": "Materials('MAT002')",
				"@odata.etag": "W/\"20260421090000\"",
				"Name": "Copper Wire B2",
				"Description": "Industrial copper wire.",
				"LastChangeDateTime": "2026-04-21T09:00:00Z",
				"AuthorizationGroup": "MM_BUYER"
			}
		],
		"@odata.deltaLink": "https://sap.example.com/sap/opu/odata4/sap/API_MATERIAL/Materials?$deltatoken=abc123"
	}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(deltaResp))
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

	// First entity.
	c0 := page.Changes[0]
	if c0.DocRef.DocID != "Materials('MAT001')" {
		t.Errorf("Changes[0].DocID = %q", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Steel Plate A4" {
		t.Errorf("Changes[0].Title = %q", c0.DocRef.Title)
	}
	if c0.ChangeKind != contentsource.ChangeContent {
		t.Errorf("Changes[0].ChangeKind = %q, want content", c0.ChangeKind)
	}

	// Second entity.
	c1 := page.Changes[1]
	if c1.DocRef.DocID != "Materials('MAT002')" {
		t.Errorf("Changes[1].DocID = %q", c1.DocRef.DocID)
	}

	want := "https://sap.example.com/sap/opu/odata4/sap/API_MATERIAL/Materials?$deltatoken=abc123"
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty final-page pagination token", page.NextToken)
	}
	if page.ResumeToken != want {
		t.Errorf("ResumeToken = %q, want %q", page.ResumeToken, want)
	}
}

func TestSAPODataDeltaListSplitsNextAndResumeTokens(t *testing.T) {
	var srv *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skiptoken") == "2" {
			_, _ = w.Write([]byte(`{
				"value":[{"@odata.id":"Materials('MAT002')","Name":"Second","Description":"Body","LastChangeDateTime":"2026-07-01T11:00:00Z"}],
				"@odata.deltaLink":"` + srv.URL + `/sap/opu/odata4/sap/API_MATERIAL/Materials?$deltatoken=resume"
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"value":[{"@odata.id":"Materials('MAT001')","Name":"First","Description":"Body","LastChangeDateTime":"2026-07-01T10:00:00Z"}],
			"@odata.nextLink":"` + srv.URL + `/sap/opu/odata4/sap/API_MATERIAL/Materials?$skiptoken=2"
		}`))
	})
	srv = httptest.NewServer(handler)
	defer srv.Close()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":         "live",
		"base_url":     srv.URL,
		"service_path": "/sap/opu/odata4/sap/API_MATERIAL",
		"entity_sets":  "Materials",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	page1, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList page1: %v", err)
	}
	if page1.NextToken == "" || page1.ResumeToken != "" {
		t.Fatalf("page1 NextToken/ResumeToken = %q/%q", page1.NextToken, page1.ResumeToken)
	}
	page2, err := s.DeltaList(context.Background(), page1.NextToken)
	if err != nil {
		t.Fatalf("DeltaList page2: %v", err)
	}
	if page2.NextToken != "" || !strings.Contains(page2.ResumeToken, "$deltatoken=resume") {
		t.Fatalf("page2 NextToken/ResumeToken = %q/%q", page2.NextToken, page2.ResumeToken)
	}
}

func TestSAPODataLiveListPaginates(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("$skiptoken") == "2" {
			_, _ = w.Write([]byte(`{"value":[{"@odata.id":"Materials('MAT002')","Name":"Second","Description":"Body","LastChangeDateTime":"2026-07-01T11:00:00Z"}]}`))
			return
		}
		next := "http://" + r.Host + "/sap/opu/odata4/sap/API_MATERIAL/Materials?$skiptoken=2"
		_, _ = w.Write([]byte(`{"value":[{"@odata.id":"Materials('MAT001')","Name":"First","Description":"Body","LastChangeDateTime":"2026-07-01T10:00:00Z"}],"@odata.nextLink":"` + next + `"}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "Materials('MAT001')" || next == "" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "Materials('MAT002')" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestSAPODataLiveFetchSerializesEntityJSON(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/Materials('MAT001')") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"@odata.id":"Materials('MAT001')","Name":"Steel","Description":"Body","LastChangeDateTime":"2026-07-01T10:00:00Z","AuthorizationGroup":"MM"}`))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "Materials('MAT001')")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Steel" || doc.ContentType != "application/json" || !strings.Contains(doc.Body, `"AuthorizationGroup":"MM"`) {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestSAPODataDeltaListExpired(t *testing.T) {
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

func TestSAPODataFetchACL(t *testing.T) {
	aclResp := `{
		"@odata.id": "Materials('MAT001')",
		"AuthorizationGroup": "MM_PURCHASER"
	}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(aclResp))
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "MAT001")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	if got := strings.Join(result.ACL, ","); got != "role:MM_PURCHASER" {
		t.Errorf("ACL = %q, want role:MM_PURCHASER", got)
	}
	// SAP OData has no native sensitivity labels.
	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty", result.Classification)
	}
}

func TestSAPODataDeltaListRejectsSSRF(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not have been sent")
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	// A crafted sinceToken pointing at a different host must be rejected.
	_, err := s.DeltaList(context.Background(), "https://evil.internal/steal-creds")
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected host mismatch error, got %v", err)
	}
}

func TestSAPODataLiveModeRejectsEmptyBaseURL(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":         "live",
		"service_path": "/sap/opu/odata4/sap/API_MATERIAL",
		// base_url intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("expected base_url error, got %v", err)
	}
}

func TestSAPODataLiveModeRejectsEmptyServicePath(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":     "live",
		"base_url": "https://sap.example.com",
		// service_path intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "service_path") {
		t.Fatalf("expected service_path error, got %v", err)
	}
}

func TestSAPODataOAuth2BTPRequiresTokenURL(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":         "live",
		"base_url":     "https://sap.example.com",
		"service_path": "/sap/opu/odata4/sap/API_MATERIAL",
		"auth_scheme":  "oauth2_btp",
		// token_url intentionally absent
	}})
	if err == nil || !strings.Contains(err.Error(), "token_url") {
		t.Fatalf("expected token_url error, got %v", err)
	}
}

func TestSAPODataExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/sapodata.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "Materials('MAT001')")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceSAPOData {
		t.Errorf("Source = %q, want sap_odata", doc.Source)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "Materials('MAT001')" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	// DeltaList must return an error in export mode (not live).
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	// FetchACL must return an error in export mode.
	if _, err := s.FetchACL(context.Background(), "Materials('MAT001')"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

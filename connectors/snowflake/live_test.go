// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflake

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
func openLiveSource(t *testing.T, handler http.Handler) (*Source, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"mode":             "live",
		"account":          "xy12345.us-east-1",
		"user":             "TEST_USER",
		"warehouse":        "COMPUTE_WH",
		"database":         "ANALYTICS",
		"schema_name":      "PUBLIC",
		"tables":           "PRODUCTS",
		"timestamp_column": "LAST_MODIFIED",
		"credential_ref":   "test-jwt-token",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	// Override baseURL to point at the test server.
	s.live.baseURL = srv.URL
	return s, srv.Close
}

func TestSnowflakeDeltaListIncremental(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v2/statements") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		resp := sfAPIResponse{
			ResultSetMetaData: sfResultSetMetaData{
				NumRows: 2,
				RowType: []sfRowType{
					{Name: "ID"},
					{Name: "TITLE"},
					{Name: "CONTENT"},
					{Name: "LAST_MODIFIED"},
				},
			},
			Data: [][]string{
				{"ROW_1", "Product Alpha", "Alpha description", "2026-06-15T10:00:00Z"},
				{"ROW_2", "Product Beta", "Beta description", "2026-06-20T14:30:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "2026-06-10T00:00:00Z")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}

	if page.Expired {
		t.Error("expected Expired=false")
	}
	if len(page.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(page.Changes))
	}

	c0 := page.Changes[0]
	if c0.DocRef.DocID != "ROW_1" {
		t.Errorf("Changes[0].DocID = %q, want ROW_1", c0.DocRef.DocID)
	}
	if c0.DocRef.Title != "Product Alpha" {
		t.Errorf("Changes[0].Title = %q, want Product Alpha", c0.DocRef.Title)
	}
	if c0.ChangeKind != contentsource.ChangeContent {
		t.Errorf("Changes[0].ChangeKind = %q, want ChangeContent", c0.ChangeKind)
	}

	c1 := page.Changes[1]
	if c1.DocRef.DocID != "ROW_2" {
		t.Errorf("Changes[1].DocID = %q, want ROW_2", c1.DocRef.DocID)
	}

	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if !strings.Contains(page.ResumeToken, "2026-06-20") {
		t.Errorf("ResumeToken = %q, want date containing 2026-06-20", page.ResumeToken)
	}
}

func TestSnowflakeDeltaListEmpty(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := sfAPIResponse{
			ResultSetMetaData: sfResultSetMetaData{
				NumRows: 0,
				RowType: []sfRowType{
					{Name: "ID"},
					{Name: "TITLE"},
					{Name: "CONTENT"},
					{Name: "LAST_MODIFIED"},
				},
			},
			Data: [][]string{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	page, err := s.DeltaList(context.Background(), "")
	if err != nil {
		t.Fatalf("DeltaList: %v", err)
	}
	if len(page.Changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(page.Changes))
	}
	if page.NextToken != "" {
		t.Errorf("NextToken = %q, want empty", page.NextToken)
	}
	if page.ResumeToken != "" {
		t.Errorf("ResumeToken = %q, want empty", page.ResumeToken)
	}
	if page.Expired {
		t.Error("expected Expired=false")
	}
}

func TestSnowflakeLiveListAndFetch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sfStatementRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := sfAPIResponse{
			ResultSetMetaData: sfResultSetMetaData{
				NumRows: 1,
				RowType: []sfRowType{
					{Name: "ID"},
					{Name: "TITLE"},
					{Name: "CONTENT"},
					{Name: "LAST_MODIFIED"},
				},
			},
			Data: [][]string{{"ROW_1", "Product Alpha", "Alpha description", "2026-06-15T10:00:00Z"}},
		}
		if strings.Contains(req.Statement, "WHERE ID = 'ROW_1'") {
			resp.Data = [][]string{{"ROW_1", "Product Alpha", "Alpha JSON", "2026-06-15T10:00:00Z"}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "ROW_1" || refs[0].Title != "Product Alpha" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	doc, err := s.Fetch(context.Background(), "ROW_1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Title != "Product Alpha" || doc.ContentType != "application/json" || !strings.Contains(doc.Body, `"CONTENT":"Alpha JSON"`) {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestSnowflakeFetchACL(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := sfAPIResponse{
			ResultSetMetaData: sfResultSetMetaData{
				NumRows: 3,
				RowType: []sfRowType{
					{Name: "created_on"},
					{Name: "privilege"},
					{Name: "granted_on"},
					{Name: "name"},
					{Name: "granted_to"},
					{Name: "grantee_name"},
					{Name: "grant_option"},
					{Name: "granted_by"},
				},
			},
			Data: [][]string{
				{"2026-01-01", "SELECT", "TABLE", "PRODUCTS", "ROLE", "ANALYST_ROLE", "false", "SYSADMIN"},
				{"2026-01-01", "INSERT", "TABLE", "PRODUCTS", "ROLE", "ETL_ROLE", "false", "SYSADMIN"},
				{"2026-01-01", "OWNERSHIP", "TABLE", "PRODUCTS", "ROLE", "DATA_OWNER", "true", "SYSADMIN"},
				{"2026-01-01", "SELECT", "TABLE", "PRODUCTS", "ROLE", "DATA_ENGINEER", "false", "SYSADMIN"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	s, cleanup := openLiveSource(t, handler)
	defer cleanup()

	result, err := s.FetchACL(context.Background(), "PRODUCTS_ROW_42")
	if err != nil {
		t.Fatalf("FetchACL: %v", err)
	}

	// Expected: ANALYST_ROLE (SELECT), DATA_OWNER (OWNERSHIP), DATA_ENGINEER (SELECT).
	// ETL_ROLE has INSERT only, so excluded.
	got := strings.Join(result.ACL, ",")
	if !strings.Contains(got, "role:ANALYST_ROLE") {
		t.Errorf("ACL missing role:ANALYST_ROLE, got %q", got)
	}
	if !strings.Contains(got, "role:DATA_OWNER") {
		t.Errorf("ACL missing role:DATA_OWNER, got %q", got)
	}
	if !strings.Contains(got, "role:DATA_ENGINEER") {
		t.Errorf("ACL missing role:DATA_ENGINEER, got %q", got)
	}
	if strings.Contains(got, "role:ETL_ROLE") {
		t.Errorf("ACL should not contain role:ETL_ROLE (INSERT only), got %q", got)
	}

	// Snowflake has no native sensitivity labels.
	if len(result.ExternalLabels) != 0 {
		t.Errorf("ExternalLabels = %v, want empty", result.ExternalLabels)
	}
	if result.Classification != "" {
		t.Errorf("Classification = %q, want empty", result.Classification)
	}
}

func TestSnowflakeLiveRequiresAccountAndUser(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": "live",
		"user": "TEST_USER",
	}})
	if err == nil || !strings.Contains(err.Error(), "account is required") {
		t.Fatalf("expected account required error, got %v", err)
	}

	s2 := New()
	err = s2.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":    "live",
		"account": "xy12345",
	}})
	if err == nil || !strings.Contains(err.Error(), "user is required") {
		t.Fatalf("expected user required error, got %v", err)
	}
}

func TestSnowflakeExportModeRejectsLiveMethods(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"export_path": "testdata/snowflake.json",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	doc, err := s.Fetch(context.Background(), "PRODUCTS_ROW_42")
	if err != nil {
		t.Fatalf("Fetch export: %v", err)
	}
	if doc.Source != contentsource.SourceSnowflake {
		t.Errorf("Source = %q, want snowflake", doc.Source)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List export: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "PRODUCTS_ROW_42" || next != "" {
		t.Fatalf("List refs/next = %+v/%q, want PRODUCTS_ROW_42/empty", refs, next)
	}
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Error("expected DeltaList to return error in export mode")
	}
	if _, err := s.FetchACL(context.Background(), "PRODUCTS_ROW_42"); err == nil {
		t.Error("expected FetchACL to return error in export mode")
	}
}

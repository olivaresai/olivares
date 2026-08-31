// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testToken  = "tok_secret_123"
	testTenant = "11111111-2222-3333-4444-555555555555"
)

// sampleDTO is a representative AgentDTO returned by the fake server.
func sampleDTO(id string) map[string]any {
	return map[string]any{
		"id":          id,
		"tenant_id":   testTenant,
		"name":        "billing-agent",
		"kind":        "worker",
		"external_id": "ext-42",
		"status":      "active",
		"labels":      map[string]any{"team": "payments"},
		"metadata":    map[string]any{"region": "eu"},
		"created_at":  "2026-06-03T10:00:00Z",
		"updated_at":  "2026-06-03T10:00:00Z",
		"version":     float64(1),
	}
}

// assertCommonHeaders verifies auth + tenant + User-Agent headers on every
// request. The tests in this package build clients without an explicit
// Version, so the expected UA carries New's "dev" fallback; the explicit
// version path is covered by TestUserAgentCarriesVersion.
func assertCommonHeaders(t *testing.T, r *http.Request, wantTenant string) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+testToken)
	}
	if got := r.Header.Get("X-Olivares-Tenant"); got != wantTenant {
		t.Errorf("X-Olivares-Tenant = %q, want %q", got, wantTenant)
	}
	if got := r.Header.Get("User-Agent"); got != "terraform-provider-olivares/dev" {
		t.Errorf("User-Agent = %q, want terraform-provider-olivares/dev", got)
	}
}

// TestUserAgentCarriesVersion pins the UA format the API stability policy
// keys deprecation telemetry on: terraform-provider-olivares/<version>.
func TestUserAgentCarriesVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "terraform-provider-olivares/1.4.2" {
			t.Errorf("User-Agent = %q, want terraform-provider-olivares/1.4.2", got)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant, Version: "1.4.2"})
	if _, err := c.GetAgent(context.Background(), "", "agt_001"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
}

func TestCreateAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/agents" {
			t.Errorf("path = %s, want /v1/agents", r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		// Only writable fields should be present.
		for _, k := range []string{"name", "kind", "external_id", "status"} {
			if _, ok := body[k]; !ok {
				t.Errorf("request body missing %q", k)
			}
		}
		if body["name"] != "billing-agent" {
			t.Errorf("body name = %v, want billing-agent", body["name"])
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.CreateAgent(context.Background(), "", Agent{
		Name:       "billing-agent",
		Kind:       "worker",
		ExternalID: "ext-42",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if got.ID != "agt_001" {
		t.Errorf("ID = %q, want agt_001", got.ID)
	}
	if got.TenantID != testTenant {
		t.Errorf("TenantID = %q, want %q", got.TenantID, testTenant)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.Labels["team"] != "payments" {
		t.Errorf("Labels[team] = %v, want payments", got.Labels["team"])
	}
}

func TestGetAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/agents/agt_001" {
			t.Errorf("path = %s, want /v1/agents/agt_001", r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.GetAgent(context.Background(), "", "agt_001")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Name != "billing-agent" {
		t.Errorf("Name = %q, want billing-agent", got.Name)
	}
}

func TestGetAgentNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "not_found", "message": "no such agent"},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	_, err := c.GetAgent(context.Background(), "", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAgent 404 error = %v, want ErrNotFound", err)
	}
}

func TestUpdateAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/agents/agt_001" {
			t.Errorf("path = %s, want /v1/agents/agt_001", r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["status"] != "paused" {
			t.Errorf("body status = %v, want paused", body["status"])
		}

		dto := sampleDTO("agt_001")
		dto["status"] = "paused"
		dto["version"] = float64(2)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(dto)
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.UpdateAgent(context.Background(), "", "agt_001", Agent{
		Name:   "billing-agent",
		Kind:   "worker",
		Status: "paused",
	})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if got.Status != "paused" {
		t.Errorf("Status = %q, want paused", got.Status)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
}

func TestDeleteAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		if r.URL.Path != "/v1/agents/agt_001" {
			t.Errorf("path = %s, want /v1/agents/agt_001", r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if err := c.DeleteAgent(context.Background(), "", "agt_001"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
}

func TestDeleteAgentNotFoundIsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if err := c.DeleteAgent(context.Background(), "", "gone"); err != nil {
		t.Fatalf("DeleteAgent on 404 = %v, want nil", err)
	}
}

func TestTenantOverride(t *testing.T) {
	const override = "99999999-0000-0000-0000-000000000000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Per-call override must win over the client-level tenant.
		assertCommonHeaders(t, r, override)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if _, err := c.GetAgent(context.Background(), override, "agt_001"); err != nil {
		t.Fatalf("GetAgent with override: %v", err)
	}
}

func TestNoTenantHeaderWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tenant-bound token: neither client nor call sets a tenant, so the
		// header must be absent (engine resolves the tenant from the token).
		if _, ok := r.Header["X-Olivares-Tenant"]; ok {
			t.Errorf("X-Olivares-Tenant present, want absent")
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken})
	if _, err := c.GetAgent(context.Background(), "", "agt_001"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
}

func TestErrorEnvelopeSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "invalid_kind", "message": "kind must be worker|router"},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	_, err := c.CreateAgent(context.Background(), "", Agent{Name: "x", Kind: "bogus"})
	if err == nil {
		t.Fatal("CreateAgent error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid_kind") || !strings.Contains(err.Error(), "kind must be worker|router") {
		t.Errorf("error %q does not surface envelope code+message", err.Error())
	}
}

func TestEndpointTrailingSlashTrimmed(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL + "/", APIToken: testToken, Tenant: testTenant})
	if _, err := c.GetAgent(context.Background(), "", "agt_001"); err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if gotPath != "/v1/agents/agt_001" {
		t.Errorf("path = %q, want /v1/agents/agt_001 (no double slash)", gotPath)
	}
}

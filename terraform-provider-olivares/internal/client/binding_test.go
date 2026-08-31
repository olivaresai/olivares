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
	"testing"
)

func TestBindAgentIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/agents/agt-1/identity" {
			t.Errorf("got %s %s, want POST /v1/m/governance/agents/agt-1/identity", r.Method, r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["identity_ref"] != "agent:acme" {
			t.Errorf("bind body identity_ref = %v, want agent:acme", body["identity_ref"])
		}
		// mint/identity_id must be omitted when binding by ref.
		if _, ok := body["mint"]; ok {
			t.Error("mint should be omitted when binding by ref")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_id": "agt-1", "identity_id": "id-9", "identity_ref": "agent:acme",
			"minted": false, "shared": true, "agent_count": float64(2),
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.BindAgentIdentity(context.Background(), "", "agt-1", "", "agent:acme", false, false)
	if err != nil {
		t.Fatalf("BindAgentIdentity: %v", err)
	}
	if got.IdentityID != "id-9" || !got.Shared || got.AgentCount != 2 {
		t.Errorf("unexpected binding: %+v", got)
	}
}

func TestBindAgentIdentityMint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["mint"] != true {
			t.Errorf("bind body mint = %v, want true", body["mint"])
		}
		// The engine may omit agent_id on the response; the client must backfill it.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"identity_id": "id-minted", "minted": true, "shared": false, "agent_count": float64(1),
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.BindAgentIdentity(context.Background(), "", "agt-1", "", "", true, false)
	if err != nil {
		t.Fatalf("BindAgentIdentity mint: %v", err)
	}
	if got.AgentID != "agt-1" {
		t.Errorf("AgentID = %q, want agt-1 (backfilled)", got.AgentID)
	}
	if !got.Minted {
		t.Error("expected minted=true")
	}
}

func TestUnbindAgentIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/m/governance/agents/agt-1/identity" {
			t.Errorf("got %s %s, want DELETE .../agt-1/identity", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if err := c.UnbindAgentIdentity(context.Background(), "", "agt-1"); err != nil {
		t.Fatalf("UnbindAgentIdentity: %v", err)
	}
}

func TestGetBindingFoundAndMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/governance/bindings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []any{
				map[string]any{"agent_id": "agt-1", "agent_name": "billing", "identity_id": "id-9", "identity_ref": "agent:acme", "shared": false, "agent_count": float64(1)},
			},
			"has_more": false,
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.GetBinding(context.Background(), "", "agt-1")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if got.IdentityID != "id-9" {
		t.Errorf("IdentityID = %q, want id-9", got.IdentityID)
	}
	// An agent with no binding must surface as ErrNotFound (resource removed).
	if _, err := c.GetBinding(context.Background(), "", "agt-unbound"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBinding(missing) = %v, want ErrNotFound", err)
	}
}

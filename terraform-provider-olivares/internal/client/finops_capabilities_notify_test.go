// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBudgetRoundTripAndPagination verifies the budget create body carries the
// typed fields and that ListBudgets follows the cursor across pages.
func TestBudgetRoundTripAndPagination(t *testing.T) {
	ctx := context.Background()

	t.Run("create round-trip", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assertCommonHeaders(t, r, testTenant)
			if r.URL.Path != "/v1/m/finops/budgets" || r.Method != http.MethodPost {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["dimension"] != "model" || in["key"] != "claude-opus" {
				t.Errorf("budget body missing dimension/key: %v", in)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "bud-1", "name": "opus-cap", "enabled": true, "dimension": "model",
				"key": "claude-opus", "limit_micro_usd": 1_000_000, "period": "monthly",
				"currency": "USD", "action": "alert",
			})
		}))
		defer srv.Close()

		c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
		got, err := c.CreateBudget(ctx, "", Budget{
			Name: "opus-cap", Enabled: true, Dimension: "model", Key: "claude-opus",
			LimitMicroUSD: 1_000_000, Period: "monthly", Currency: "USD", Action: "alert",
		})
		if err != nil {
			t.Fatalf("CreateBudget: %v", err)
		}
		if got.ID != "bud-1" || got.Key != "claude-opus" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("list pagination", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("cursor") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items":  []map[string]any{{"id": "b1", "name": "a", "limit_micro_usd": 1}},
					"cursor": "next", "has_more": true,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":    []map[string]any{{"id": "b2", "name": "b", "limit_micro_usd": 2}},
				"has_more": false,
			})
		}))
		defer srv.Close()

		c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
		all, err := c.ListBudgets(ctx, "")
		if err != nil {
			t.Fatalf("ListBudgets: %v", err)
		}
		if len(all) != 2 || all[0].ID != "b1" || all[1].ID != "b2" {
			t.Errorf("pagination failed: %+v", all)
		}
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
		if _, err := c.GetBudget(ctx, "", "nope"); err == nil {
			t.Error("expected ErrNotFound")
		}
	})
}

// TestNotificationRouteSendsEnabledPointer verifies enabled is always sent
// explicitly (so an update cannot silently flip it to the server default).
func TestNotificationRouteSendsEnabledPointer(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "\"enabled\":false") {
			t.Errorf("enabled must be sent explicitly, body=%s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "rt-1", "name": "r", "enabled": false, "destination": "d",
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.CreateNotificationRoute(ctx, "", NotificationRoute{Name: "r", Enabled: false, Destination: "d"})
	if err != nil {
		t.Fatalf("CreateNotificationRoute: %v", err)
	}
	if got.Enabled {
		t.Error("expected enabled=false echoed back")
	}
}

// TestCapabilityConfigSecretRefsRoundTrip verifies secret refs are sent and read
// back, and that the client carries the locator (never a value) verbatim.
func TestCapabilityConfigSecretRefsRoundTrip(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "secret/data/x#k") {
			t.Errorf("secret ref locator not sent, body=%s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cfg-1", "server_ref": "s", "transport": "stdio", "enabled": true, "revision": 1,
			"secret_refs": []map[string]any{{"name": "k", "ref_kind": "vault", "ref": "secret/data/x#k"}},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.CreateCapabilityConfig(ctx, "", CapabilityConfig{
		ServerRef: "s", Transport: "stdio", Enabled: true,
		SecretRefs: []SecretRef{{Name: "k", RefKind: "vault", Ref: "secret/data/x#k"}},
	})
	if err != nil {
		t.Fatalf("CreateCapabilityConfig: %v", err)
	}
	if len(got.SecretRefs) != 1 || got.SecretRefs[0].Ref != "secret/data/x#k" {
		t.Errorf("secret refs round-trip failed: %+v", got.SecretRefs)
	}
}

// TestInventoryEntitiesFilterAndSummary verifies the kind/status filter is sent
// and the summary roll-up decodes.
func TestInventoryEntitiesFilterAndSummary(t *testing.T) {
	ctx := context.Background()

	t.Run("entities filter", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("kind") != "agent" {
				t.Errorf("kind filter not sent: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":    []map[string]any{{"kind": "agent", "entity_id": "a1", "name": "x", "status": "active", "signal_sources": []string{"docker"}}},
				"has_more": false,
			})
		}))
		defer srv.Close()
		c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
		got, err := c.ListInventoryEntities(ctx, "", "agent", "")
		if err != nil {
			t.Fatalf("ListInventoryEntities: %v", err)
		}
		if len(got) != 1 || got[0].EntityID != "a1" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("summary", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"by_kind":   map[string]any{"agent": map[string]any{"total": 3}},
				"by_source": map[string]any{"docker": 2}, "total": 3,
			})
		}))
		defer srv.Close()
		c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
		got, err := c.GetInventorySummary(ctx, "")
		if err != nil {
			t.Fatalf("GetInventorySummary: %v", err)
		}
		if got.Total != 3 || got.ByKind["agent"].Total != 3 || got.BySource["docker"] != 2 {
			t.Errorf("summary decode failed: %+v", got)
		}
	})
}

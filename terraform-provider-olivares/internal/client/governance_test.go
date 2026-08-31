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

// abacSpec is a representative ABAC policy spec used across the policy tests.
const abacSpec = `{"rules":[{"deny":true,"verb":"write","resource":"agent"}]}`

func TestCreatePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/m/governance/policies" {
			t.Errorf("got %s %s, want POST /v1/m/governance/policies", r.Method, r.URL.Path)
		}
		assertCommonHeaders(t, r, testTenant)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "deny-agent-write" || body["kind"] != "abac" || body["enabled"] != true {
			t.Errorf("unexpected create body: %v", body)
		}
		if body["spec"] == nil {
			t.Error("create body missing spec")
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "pol-1", "name": "deny-agent-write", "kind": "abac", "enabled": true,
			"spec": map[string]any{"rules": []any{map[string]any{"deny": true, "verb": "write", "resource": "agent"}}},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.CreatePolicy(context.Background(), "", Policy{
		Name: "deny-agent-write", Kind: "abac", Enabled: true, Spec: json.RawMessage(abacSpec),
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if got.ID != "pol-1" || got.Kind != "abac" || !got.Enabled {
		t.Errorf("unexpected policy: %+v", got)
	}
	if len(got.Spec) == 0 {
		t.Error("response spec should carry the canonical spec")
	}
}

// TestGetPolicyDriftSignal proves Read surfaces the engine's canonical spec, which
// is the drift signal: an out-of-band edit returns a changed canonical spec.
func TestGetPolicyDriftSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/m/governance/policies/pol-1" {
			t.Errorf("got %s %s, want GET /v1/m/governance/policies/pol-1", r.Method, r.URL.Path)
		}
		// The stored spec was edited out of band (an extra rule), so Read returns a
		// different canonical spec than what was originally applied.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "pol-1", "name": "deny-agent-write", "kind": "abac", "enabled": false,
			"spec": map[string]any{"rules": []any{
				map[string]any{"deny": true, "verb": "write", "resource": "agent"},
				map[string]any{"deny": true, "verb": "admin", "resource": "agent"},
			}},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.GetPolicy(context.Background(), "", "pol-1")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.Enabled {
		t.Error("expected enabled=false from the out-of-band edit (drift)")
	}
	if !strings.Contains(string(got.Spec), "admin") {
		t.Errorf("canonical spec should reflect the drifted rule, got %s", got.Spec)
	}
}

func TestGetPolicyNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if _, err := c.GetPolicy(context.Background(), "", "gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPolicy 404 = %v, want ErrNotFound", err)
	}
}

func TestUpdateAndDeletePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			if r.URL.Path != "/v1/m/governance/policies/pol-1" {
				t.Errorf("PUT path = %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "pol-1", "name": "n", "kind": "abac", "enabled": false, "spec": map[string]any{}})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	if _, err := c.UpdatePolicy(context.Background(), "", "pol-1", Policy{Name: "n", Kind: "abac", Spec: json.RawMessage(abacSpec)}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	if err := c.DeletePolicy(context.Background(), "", "pol-1"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
}

func TestListPoliciesPaginatesAndFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kind") != "abac" {
			t.Errorf("kind filter = %q, want abac", r.URL.Query().Get("kind"))
		}
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items":    []any{map[string]any{"id": "pol-1", "name": "a", "kind": "abac", "enabled": true, "spec": map[string]any{}}},
				"cursor":   "next",
				"has_more": true,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":    []any{map[string]any{"id": "pol-2", "name": "b", "kind": "abac", "enabled": true, "spec": map[string]any{}}},
			"has_more": false,
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.ListPolicies(context.Background(), "", "abac")
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(got) != 2 || got[0].ID != "pol-1" || got[1].ID != "pol-2" {
		t.Errorf("expected 2 paginated policies, got %+v", got)
	}
}

func TestListIdentities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/m/governance/identities" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []any{map[string]any{
				"id": "id-1", "ref": "agent:acme", "name": "nhi:acme", "kind": "agent_nhi",
				"source": "governance", "principal_type": "nhi", "disabled": false,
			}},
			"has_more": false,
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.ListIdentities(context.Background(), "")
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(got) != 1 || got[0].PrincipalType != "nhi" || got[0].Ref != "agent:acme" {
		t.Errorf("unexpected identities: %+v", got)
	}
}

func TestListAccessEdgesAndDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/access-edges":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{
					"id": "edge-1", "origin_kind": "agent", "origin_id": "a1", "resource_id": "r1",
					"mode": "read", "signal_source": "pg_audit", "confidence": "attributed",
					"permitted": false, "observed": true, "occurrence_count": float64(7),
					"first_seen": "2026-06-01T00:00:00Z", "last_seen": "2026-06-02T00:00:00Z",
				}},
				"has_more": false,
			})
		case "/v1/m/accessmap/drift":
			// The RECONCILED module III envelope (two arrays + counts, no cursor) —
			// NOT the removed raw {items,cursor} shape (C2).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"unexpected_accesses": []any{
					map[string]any{
						"kind": "unexpected_access", "reconciliation_pending": false,
						"edge": map[string]any{"id": "edge-1", "origin_kind": "agent", "origin_id": "a1", "resource_id": "r1",
							"mode": "read", "signal_source": "pg_audit", "confidence": "attributed",
							"permitted": false, "observed": true, "occurrence_count": float64(7),
							"first_seen": "2026-06-01T00:00:00Z", "last_seen": "2026-06-02T00:00:00Z"},
					},
					map[string]any{
						"kind": "unexpected_access", "reconciliation_pending": true,
						"edge": map[string]any{"id": "edge-2", "origin_kind": "agent", "origin_id": "a2", "resource_id": "r2",
							"mode": "write", "signal_source": "pg_audit", "confidence": "approximate",
							"permitted": false, "observed": true, "occurrence_count": float64(1),
							"first_seen": "2026-06-01T00:00:00Z", "last_seen": "2026-06-02T00:00:00Z"},
					},
				},
				"unused_grants": []any{map[string]any{
					"kind": "unused_grant",
					"edge": map[string]any{"id": "edge-3", "origin_kind": "identity", "origin_id": "i1", "resource_id": "r3",
						"mode": "read", "signal_source": "policy", "confidence": "attributed",
						"permitted": true, "observed": false, "occurrence_count": float64(0),
						"first_seen": "2026-06-01T00:00:00Z", "last_seen": "2026-06-02T00:00:00Z"},
				}},
				"unexpected_count": float64(2), "unused_count": float64(1),
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	edges, err := c.ListAccessEdges(context.Background(), "")
	if err != nil {
		t.Fatalf("ListAccessEdges: %v", err)
	}
	if len(edges) != 1 || edges[0].Mode != "read" || edges[0].OccurrenceCount != 7 {
		t.Errorf("unexpected edges: %+v", edges)
	}
	drift, err := c.ListDrift(context.Background(), "")
	if err != nil {
		t.Fatalf("ListDrift: %v", err)
	}
	// The reconciled envelope's two arrays are flattened into one list, each entry
	// keeping its kind and the honest reconciliation_pending flag.
	if len(drift) != 3 {
		t.Fatalf("want 3 reconciled drift entries (2 unexpected + 1 unused); got %d: %+v", len(drift), drift)
	}
	byID := map[string]DriftEdge{}
	for _, d := range drift {
		byID[d.Edge.ID] = d
	}
	if d := byID["edge-1"]; d.Kind != DriftUnexpectedAccess || d.ReconciliationPending {
		t.Errorf("edge-1 want unexpected_access, pending=false; got kind=%q pending=%v", d.Kind, d.ReconciliationPending)
	}
	if d := byID["edge-2"]; d.Kind != DriftUnexpectedAccess || !d.ReconciliationPending {
		t.Errorf("edge-2 want unexpected_access, pending=true; got kind=%q pending=%v", d.Kind, d.ReconciliationPending)
	}
	if d := byID["edge-3"]; d.Kind != DriftUnusedGrant || d.Edge.Mode != "read" {
		t.Errorf("edge-3 want unused_grant; got kind=%q mode=%q", d.Kind, d.Edge.Mode)
	}
}

func TestGetServerInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/server-info" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "1.2.3", "engine": "sqlite", "setup_required": false,
			"license": map[string]any{"status": "active", "licensee": "acme"},
		})
	}))
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	got, err := c.GetServerInfo(context.Background(), "")
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	if got.Version != "1.2.3" || got.License.Status != "active" || got.License.Licensee != "acme" {
		t.Errorf("unexpected server info: %+v", got)
	}
}

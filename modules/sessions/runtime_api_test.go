// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"
	"testing"
)

// TestRuntimeAPI_HTTP drives the operate endpoints through the REAL api.Server
// (auth + tenant resolution + RBAC + route mounting), proving the wiring rather
// than the lifecycle logic (which the white-box tests cover).
func TestRuntimeAPI_HTTP(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-h"}
	m := New(WithRunner(fr), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	// Register a workspace (admin tier), then launch against it (ref→path).
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "name": "ws-http",
	}, tenantHdr(tenantA))
	if wr.code != http.StatusCreated {
		t.Fatalf("workspace create = %d %s", wr.code, wr.raw)
	}
	wsRef, _ := wr.body["workspace_ref"].(string)
	if wsRef == "" {
		t.Fatalf("workspace_ref missing: %s", wr.raw)
	}

	// Create (write tier).
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"workspace_ref": wsRef, "name": "via-http",
	}, tenantHdr(tenantA))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	ref, _ := r.body["run_ref"].(string)
	if ref == "" || r.body["state"] != stateRunning {
		t.Fatalf("create response: %s", r.raw)
	}

	// Get (read tier).
	if r := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}

	// Lifecycle ledger is queryable.
	r = h.do("GET", "/v1/m/sessions/runs/"+ref+"/events", admin, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("events = %d %s", r.code, r.raw)
	}
	if items, _ := r.body["items"].([]any); len(items) < 2 {
		t.Fatalf("events items = %d, want >=2 (created, launched): %s", len(items), r.raw)
	}

	// Validation: a bad permission mode is rejected at the API.
	if r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "nonsense", "isolation": "native",
	}, tenantHdr(tenantA)); r.code != http.StatusBadRequest {
		t.Errorf("bad permission_mode = %d, want 400", r.code)
	}

	// Tenant isolation: the same ref does not exist under another tenant.
	if r := h.do("GET", "/v1/m/sessions/runs/"+ref, admin, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", r.code)
	}

	// Unauthenticated.
	if r := h.do("GET", "/v1/m/sessions/runs", "", tenantHdr(tenantA)); r.code != http.StatusUnauthorized {
		t.Errorf("no-auth = %d, want 401", r.code)
	}

	// Stop (write tier) → terminal.
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenantA)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}

	// Attach AFTER stop returns the buffered tail + an end marker and completes
	// (the ring is closed, so the SSE handler does not block).
	r = h.do("GET", "/v1/m/sessions/runs/"+ref+"/attach", admin, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("attach = %d", r.code)
	}
	if !strings.Contains(r.raw, "event: end") {
		t.Errorf("attach stream did not terminate with an end event: %q", r.raw)
	}
}

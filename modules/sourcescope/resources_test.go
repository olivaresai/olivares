// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestListResourceTreeNavigation exercises the folder/subtree picker's data source:
// a lazy tree read — roots, then a node's direct children, then a whole subtree.
func TestListResourceTreeNavigation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "payments")

	root := h.createFolder(tenant, "docs", model.ID(""), ws)
	h.createFolder(tenant, "policies", root.ID, ws)
	h.createFolder(tenant, "handbooks", root.ID, ws)

	// No anchor → the tree roots (exactly the one root; the two children are NOT roots).
	rr := h.do("GET", "/v1/m/sourcescope/resources", admin, nil, tenantHdr(tenant))
	if rr.code != http.StatusOK {
		t.Fatalf("roots = %d %s", rr.code, rr.raw)
	}
	roots := items(rr)
	if len(roots) != 1 {
		t.Fatalf("want 1 root, got %d: %s", len(roots), rr.raw)
	}
	node := roots[0].(map[string]any)
	if node["name"] != "docs" || node["id"] != root.ID.String() || node["kind"] != "folder" {
		t.Errorf("unexpected root node: %s", rr.raw)
	}
	// Minimal-data: the node DTO must NOT leak the sensitive/free-form columns.
	for _, leak := range []string{"uri", "owner", "metadata"} {
		if _, ok := node[leak]; ok {
			t.Errorf("resource node leaks %q (minimal-data violated): %s", leak, rr.raw)
		}
	}

	// ?parent=<root> → the two DIRECT children.
	cr := h.do("GET", "/v1/m/sourcescope/resources?parent="+root.ID.String(), admin, nil, tenantHdr(tenant))
	if cr.code != http.StatusOK || len(items(cr)) != 2 {
		t.Fatalf("children = %d (n=%d) %s", cr.code, len(items(cr)), cr.raw)
	}

	// ?subtree=<root> → root + all descendants (3).
	sr := h.do("GET", "/v1/m/sourcescope/resources?subtree="+root.ID.String(), admin, nil, tenantHdr(tenant))
	if sr.code != http.StatusOK || len(items(sr)) != 3 {
		t.Fatalf("subtree = %d (n=%d) %s", sr.code, len(items(sr)), sr.raw)
	}
}

// TestListResourcesKindFilter: the picker can restrict to a kind (e.g. only folders).
func TestListResourcesKindFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	ws := h.createWorkspace(tenant, "payments")
	h.createFolder(tenant, "docs", model.ID(""), ws)

	ok := h.do("GET", "/v1/m/sourcescope/resources?kind=folder", admin, nil, tenantHdr(tenant))
	if ok.code != http.StatusOK || len(items(ok)) != 1 {
		t.Fatalf("kind=folder = %d (n=%d) %s", ok.code, len(items(ok)), ok.raw)
	}
	none := h.do("GET", "/v1/m/sourcescope/resources?kind=postgres.table", admin, nil, tenantHdr(tenant))
	if none.code != http.StatusOK || len(items(none)) != 0 {
		t.Errorf("kind=postgres.table must be empty, got %d (n=%d) %s", none.code, len(items(none)), none.raw)
	}
}

// TestListResourcesTenantIsolation: the tree is tenant-pinned — one tenant NEVER sees
// another's nodes, and asking for a cross-tenant subtree root is 404 (deny-closed).
func TestListResourcesTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	acme := h.createOrg(admin, "acme")
	globex := h.createOrg(admin, "globex")
	wsA := h.createWorkspace(acme, "a")
	h.createFolder(acme, "acme-root", model.ID(""), wsA)
	wsB := h.createWorkspace(globex, "b")
	bRoot := h.createFolder(globex, "globex-root", model.ID(""), wsB)

	// Scoped to acme, we must see acme's node and NOT globex's.
	rr := h.do("GET", "/v1/m/sourcescope/resources", admin, nil, tenantHdr(acme))
	if rr.code != http.StatusOK {
		t.Fatalf("list = %d %s", rr.code, rr.raw)
	}
	for _, it := range items(rr) {
		if it.(map[string]any)["name"] == "globex-root" {
			t.Fatalf("tenant leak: acme scope returned a globex node: %s", rr.raw)
		}
	}
	// Asking for globex's subtree root while scoped to acme → 404, never a leak.
	sr := h.do("GET", "/v1/m/sourcescope/resources?subtree="+bRoot.ID.String(), admin, nil, tenantHdr(acme))
	if sr.code != http.StatusNotFound {
		t.Errorf("cross-tenant subtree root must be 404, got %d %s", sr.code, sr.raw)
	}
}

// TestListResourcesBadID: a malformed parent/subtree id is a clean 400, not a store hit.
func TestListResourcesBadID(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if r := h.do("GET", "/v1/m/sourcescope/resources?parent=not-a-real-id", admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("malformed parent id must be 400, got %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/sourcescope/resources?subtree=not-a-real-id", admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("malformed subtree id must be 400, got %d %s", r.code, r.raw)
	}
}

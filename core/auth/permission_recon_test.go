// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "testing"

// TestIsAccessGraphReconPerm pins the tenant-wide access-matrix recon set a workspace-confined
// principal must be forbidden (F2). It must cover EVERY route that exposes the access
// graph — the core /v1/access-edges, the access-map module's graph/drift/attack-path reads, and
// the authz reverse query — so the scoped-engine forbid closes the disclosure everywhere.
func TestIsAccessGraphReconPerm(t *testing.T) {
	forbidden := []Permission{
		"accessgraph:read",     // core GET /v1/access-edges
		"accessmap:graph:read", // access-map /graph, /neighbors, /attack-paths/*
		"accessmap:drift:read", // access-map /drift
		"authz:read",           // Who-can-access reverse query
	}
	for _, p := range forbidden {
		if !IsAccessGraphReconPerm(p) {
			t.Errorf("%q must be an access-graph recon perm (a confined operator could read the access matrix via its route)", p)
		}
	}
	// Non-recon perms must NOT be swept in (that would over-deny confined operators).
	for _, p := range []Permission{"agent:read", "tenant:read", "adoption:developer:read", "security:observed:read", "governance:policy:admin"} {
		if IsAccessGraphReconPerm(p) {
			t.Errorf("%q must NOT be an access-graph recon perm (over-denial of a confined operator)", p)
		}
	}
}

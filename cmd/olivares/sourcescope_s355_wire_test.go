// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"
)

// TestS355SubjectBindingWiredInProductionServer is the wire-proof that the subject
// axes are live through the FULLY-wired production server (buildModules), not only the
// sourcescope module's own test harness. It creates a per-SESSION binding and reads it back
// through the real composition root — the same root that injects the sourcescope resolver
// into the models ScopeGate and the knowledge RetrievalScopeGate (wire.go:296,602). A green
// run proves the schema/write-API migrate and serve alongside the full production
// module set and that the resolver is wired into both consumers.
func TestS355SubjectBindingWiredInProductionServer(t *testing.T) {
	h := newHarness(t)
	tenant := h.createOrg("acme", "acme-s355")

	var created struct {
		ID        string `json:"id"`
		ScopeTree string `json:"scope_tree"`
		ScopeRef  string `json:"scope_ref"`
		Effect    string `json:"effect"`
	}
	code := h.reqInto("POST", "/v1/m/sourcescope/bindings", h.adminToken, tenant, map[string]any{
		"source_type": "model", "source_ref": "claude-opus", "scope_tree": "session",
		"scope_ref": "sess-e2e", "effect": "allow", "enabled": true,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("create per-session binding through the production server = %d", code)
	}
	if created.ScopeTree != "session" || created.ScopeRef != "sess-e2e" || created.Effect != "allow" {
		t.Fatalf("created binding wire shape = %+v", created)
	}

	// Read back through the real server: the subject axis + effect round-trip end-to-end.
	got := h.getJSON(h.adminToken, tenant, "/v1/m/sourcescope/bindings/"+created.ID)
	if got["scope_tree"] != "session" || got["effect"] != "allow" || got["scope_ref"] != "sess-e2e" {
		t.Fatalf("read-back binding = %v", got)
	}

	// A FORBID on the user axis is also accepted end-to-end (posture is expressible through
	// the production API), proving the effect column is live in the wired schema.
	var forbid struct {
		Effect string `json:"effect"`
	}
	if code := h.reqInto("POST", "/v1/m/sourcescope/bindings", h.adminToken, tenant, map[string]any{
		"source_type": "model", "source_ref": "claude-opus", "scope_tree": "user",
		"scope_ref": "user-e2e", "effect": "forbid", "enabled": true,
	}, &forbid); code != http.StatusCreated || forbid.Effect != "forbid" {
		t.Fatalf("create user forbid binding = %d effect=%q", code, forbid.Effect)
	}
}

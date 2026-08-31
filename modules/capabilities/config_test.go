// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func validConfig() map[string]any {
	return map[string]any{
		"server_ref": "github",
		"transport":  "stdio",
		"endpoint":   "npx -y @modelcontextprotocol/server-github",
		"scope":      "team-a",
		"enabled":    true,
		"secret_refs": []map[string]any{
			{"name": "GITHUB_TOKEN", "ref_kind": "env", "ref": "$GITHUB_TOKEN", "hint": "ghp_…aB12"},
		},
	}
}

// TestConfigLifecycleAndVersioning exercises the audited config CRUD with secret
// references (never values) and the append-only version history.
func TestConfigLifecycleAndVersioning(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	// Create (revision 1).
	r := h.do("POST", "/v1/m/capabilities/configs", editor, validConfig(), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create config = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["revision"].(float64) != 1 {
		t.Errorf("revision = %v, want 1", r.body["revision"])
	}
	// The persisted secret ref is a reference, never a value: no value field exists.
	refs := r.body["secret_refs"].([]any)
	if len(refs) != 1 {
		t.Fatalf("secret_refs = %v", refs)
	}
	ref0 := refs[0].(map[string]any)
	if _, leaked := ref0["value"]; leaked {
		t.Error("secret ref leaked a value field")
	}
	if ref0["ref"] != "$GITHUB_TOKEN" {
		t.Errorf("secret ref = %v", ref0["ref"])
	}

	// Update bumps the revision.
	upd := validConfig()
	upd["scope"] = "team-b"
	r = h.do("PUT", "/v1/m/capabilities/configs/"+id, editor, upd, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update config = %d %s", r.code, r.raw)
	}
	if r.body["revision"].(float64) != 2 {
		t.Errorf("revision = %v, want 2", r.body["revision"])
	}
	if r.body["scope"] != "team-b" {
		t.Errorf("scope = %v, want team-b", r.body["scope"])
	}

	// Version history (newest first).
	r = h.do("GET", "/v1/m/capabilities/configs/"+id+"/revisions", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	revs := items(r)
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	if revs[0].(map[string]any)["revision"].(float64) != 2 {
		t.Errorf("newest revision = %v, want 2", revs[0].(map[string]any)["revision"])
	}

	// Delete records a final immutable revision snapshot and removes the config.
	if r := h.do("DELETE", "/v1/m/capabilities/configs/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete config = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/capabilities/configs/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("get deleted config = %d, want 404", r.code)
	}
	// The append-only history survives the deletion (create + update + delete = 3).
	count := 0
	_ = h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("capabilities.config_revision")
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Limit: 100})
		count = len(recs)
		return err
	})
	if count != 3 {
		t.Errorf("config_revision rows = %d, want 3 (create+update+delete)", count)
	}

	// The privileged config changes are self-audited to the real principal.
	r = h.do("GET", "/v1/audit?limit=100", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("audit = %d %s", r.code, r.raw)
	}
	for _, want := range []string{"capabilities.mcp_config.create", "capabilities.mcp_config.update", "capabilities.mcp_config.delete"} {
		if !strings.Contains(r.raw, want) {
			t.Errorf("audit ledger missing action %q", want)
		}
	}
}

// TestConfigRejectsInlineCredentials proves the minimal-data guardrails: an
// endpoint or a reference that smuggles a credential is rejected, and an over-long
// hint is rejected.
func TestConfigRejectsInlineCredentials(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	cases := []struct {
		name   string
		mutate func(c map[string]any)
	}{
		{"basic-auth endpoint", func(c map[string]any) { c["transport"] = "http"; c["endpoint"] = "https://user:pass@mcp.example.com" }},
		{"token in endpoint query", func(c map[string]any) {
			c["transport"] = "http"
			c["endpoint"] = "https://mcp.example.com?token=abc123"
		}},
		{"credential in secret ref locator", func(c map[string]any) {
			c["secret_refs"] = []map[string]any{{"name": "T", "ref_kind": "env", "ref": "token=sk-live-xyz"}}
		}},
		{"oversized hint", func(c map[string]any) {
			c["secret_refs"] = []map[string]any{{"name": "T", "ref_kind": "env", "ref": "$T", "hint": strings.Repeat("x", 200)}}
		}},
		{"bad transport", func(c map[string]any) { c["transport"] = "carrier-pigeon" }},
		{"bad ref_kind", func(c map[string]any) {
			c["secret_refs"] = []map[string]any{{"name": "T", "ref_kind": "plaintext", "ref": "$T"}}
		}},
	}
	for _, tc := range cases {
		c := validConfig()
		tc.mutate(c)
		r := h.do("POST", "/v1/m/capabilities/configs", editor, c, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400 (%s)", tc.name, r.code, r.raw)
		}
	}
}

// TestConfigRecreateAfterDelete proves a server's config can be re-created after
// deletion: the append-only revision history outlives the config, so a fresh
// create must continue the revision counter past the surviving history rather than
// colliding with revision 1 (which would make the server permanently
// unconfigurable). Regression for the create→delete→re-create defect.
func TestConfigRecreateAfterDelete(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	r := h.do("POST", "/v1/m/capabilities/configs", editor, validConfig(), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create#1 = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("DELETE", "/v1/m/capabilities/configs/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	// Re-create for the same server_ref MUST succeed (not 409), with a revision
	// past the surviving history.
	r = h.do("POST", "/v1/m/capabilities/configs", editor, validConfig(), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("re-create = %d %s (server became permanently unconfigurable)", r.code, r.raw)
	}
	if r.body["revision"].(float64) <= 1 {
		t.Errorf("re-create revision = %v, want > 1 (continues past the immutable history)", r.body["revision"])
	}
}

// TestConfigUpdateRejectsInvalid proves the validation path inside the update
// transaction maps to 400 (not 500): the deferred validationError must propagate
// verbatim through Mutate.
func TestConfigUpdateRejectsInvalid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	r := h.do("POST", "/v1/m/capabilities/configs", editor, validConfig(), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	bad := validConfig()
	bad["transport"] = "carrier-pigeon"
	if r := h.do("PUT", "/v1/m/capabilities/configs/"+id, editor, bad, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("update with bad transport = %d, want 400 (%s)", r.code, r.raw)
	}
}

// TestConfigRBAC proves a viewer can read configs but cannot write them.
func TestConfigRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	if r := h.do("GET", "/v1/m/capabilities/configs", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer list configs = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/capabilities/configs", viewer, validConfig(), tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create config = %d, want 403", r.code)
	}
}

// TestConfigTenantIsolation proves a config created in one tenant is invisible to
// another (the engine's tenant pinning is the backstop).
func TestConfigTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	editorA := h.roleToken(admin, tenantA, "a@acme.com", auth.RoleEditor)
	editorB := h.roleToken(admin, tenantB, "b@globex.com", auth.RoleEditor)

	r := h.do("POST", "/v1/m/capabilities/configs", editorA, validConfig(), tenantHdr(tenantA))
	if r.code != http.StatusCreated {
		t.Fatalf("create in A = %d %s", r.code, r.raw)
	}
	idA := r.body["id"].(string)

	if r := h.do("GET", "/v1/m/capabilities/configs", editorB, nil, tenantHdr(tenantB)); r.code != http.StatusOK || len(items(r)) != 0 {
		t.Fatalf("B sees %d configs, want 0 (code %d)", len(items(r)), r.code)
	}
	if r := h.do("GET", "/v1/m/capabilities/configs/"+idA, editorB, nil, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Fatalf("B reads A's config = %d, want 404", r.code)
	}
}

// TestListRevisionsDeclaresItsTruncation pins the half of the answer the handler used
// to drop on the floor.
//
// ⛔ WHY IT MATTERS. GET /configs/{id}/revisions asks the store for listCap rows and
// used to bind the page to `_`, so `has_more` was ALWAYS false. A client that read it
// concluded it held the entire append-only history; above listCap that was simply
// untrue, and nothing on the wire said otherwise. The evidence that this was an
// oversight and not a decision sits in the same file: handleListConfigs propagates the
// page, and nextRevision walks the cursor in a loop until !page.HasMore over this very
// kind and filter.
//
// ⛔ THE PAIR IS THE POINT, and the non-triggering half comes FIRST: a handler that
// hardcoded `has_more: true` would satisfy the truncated case on its own. Only the two
// together say the field tracks reality.
//
// The expected 1000 is a LITERAL on purpose. listCap is unexported and this is the
// black-box package, but even with access the literal is the right oracle: deriving the
// expectation from the constant under test would let a change to it stay green while the
// wire contract moved.
func TestListRevisionsDeclaresItsTruncation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	r := h.do("POST", "/v1/m/capabilities/configs", editor, validConfig(), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create config = %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	if id == "" {
		t.Fatalf("no id in %s", r.raw)
	}
	path := "/v1/m/capabilities/configs/" + id + "/revisions"

	// (a) NON-TRIGGERING FIRST: a short history must NOT claim to be truncated.
	r = h.do("GET", path, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	if got := r.body["has_more"]; got != false {
		t.Fatalf("a one-revision history reports has_more = %v, want false", got)
	}

	// Seed past listCap by cloning the existing snapshot. Cloning rather than building a
	// record by hand keeps this test from re-encoding the revision schema, which would
	// make it pass or fail for reasons that have nothing to do with truncation.
	const listCapMirror = 1000 // the ceiling the handler asks the store for
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("capabilities.config_revision")
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 10})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			t.Fatalf("no revision to clone: the create did not record one")
		}
		for i := 0; i < listCapMirror+1; i++ {
			clone := model.Record{}
			for k, v := range recs[0] {
				clone[k] = v
			}
			delete(clone, "id")
			clone["revision"] = int64(1000 + i)
			if _, err := repo.Create(ctx, clone); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding revisions: %v", err)
	}

	// (b) TRIGGERING: now the page is not the whole history, and the response says so.
	r = h.do("GET", path, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	if got := r.body["has_more"]; got != true {
		t.Fatalf("a truncated history reports has_more = %v, want true", got)
	}
	if n := len(items(r)); n != listCapMirror {
		t.Errorf("page carries %d revisions, want the ceiling %d", n, listCapMirror)
	}
}

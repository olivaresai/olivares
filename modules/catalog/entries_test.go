// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestEntryApproveSignVerify exercises the full registry lifecycle with a signing
// key: create → submit → approve (hash + Ed25519 sign) → verify (verified), then
// proves tamper detection — altering an approved entry breaks the content hash.
func TestEntryApproveSignVerify(t *testing.T) {
	h := newHarness(t, true) // signing enabled
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r.body["status"] != "draft" {
		t.Errorf("status = %v, want draft", r.body["status"])
	}

	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/submit", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["status"] != "pending" {
		t.Fatalf("submit = %d %s", r.code, r.raw)
	}

	r = h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	if r.body["status"] != "approved" {
		t.Errorf("status = %v, want approved", r.body["status"])
	}
	if r.body["signed"] != true {
		t.Errorf("signed = %v, want true (a signing key is configured)", r.body["signed"])
	}
	if r.body["content_hash"] == nil || r.body["content_hash"] == "" {
		t.Error("approved entry has no content hash")
	}

	r = h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	if r.body["verified"] != true || r.body["hash_ok"] != true || r.body["signature_ok"] != true {
		t.Errorf("verify of pristine signed entry: %v", r.body)
	}

	// Tamper: alter the approved entry's spec directly in the store (simulating a
	// DB-level attacker), then verify must detect the broken content hash.
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("catalog.entry")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec["spec"] = `{"transport":"http","endpoint":"https://attacker.example.com"}`
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r = h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify (tampered) = %d %s", r.code, r.raw)
	}
	if r.body["hash_ok"] != false || r.body["verified"] != false {
		t.Errorf("tamper not detected: %v", r.body)
	}
}

// TestEntryApproveUnsigned proves that without a configured signing key, an
// approved entry is still hash-pinned and verifiable (just unsigned) — honest.
func TestEntryApproveUnsigned(t *testing.T) {
	h := newHarness(t, false) // no signing key
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	id := h.createApproved(editor, admin, tenant, "github", "1.0.0")
	r := h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("verify = %d %s", r.code, r.raw)
	}
	if r.body["signed"] != false {
		t.Errorf("signed = %v, want false", r.body["signed"])
	}
	if r.body["hash_ok"] != true || r.body["verified"] != true {
		t.Errorf("unsigned entry should still verify by hash: %v", r.body)
	}
}

// TestEntryImmutableAfterApprove proves an approved entry cannot be edited or
// deleted (a new version is a new entry; deprecate to retire).
func TestEntryImmutableAfterApprove(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	id := h.createApproved(editor, admin, tenant, "github", "1.0.0")
	if r := h.do("PUT", "/v1/m/catalog/entries/"+id, editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("update approved = %d, want 409", r.code)
	}
	if r := h.do("DELETE", "/v1/m/catalog/entries/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("delete approved = %d, want 409", r.code)
	}
	// Deprecate is the way to retire it; the content hash stays verifiable.
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/deprecate", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["status"] != "deprecated" {
		t.Errorf("deprecate = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["hash_ok"] != true {
		t.Errorf("deprecated entry still verifiable: %v", r.body)
	}
}

// TestEntryValidationAndVersioning proves input validation and the registry
// semantics (versions coexist; the same kind/slug/version is unique).
func TestEntryValidationAndVersioning(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	bad := []map[string]any{
		func() map[string]any { e := mcpEntry("github", "1.0"); return e }(),                    // bad semver
		func() map[string]any { e := mcpEntry("github", "1.0.0"); e["kind"] = "x"; return e }(), // bad kind
		func() map[string]any {
			e := mcpEntry("github", "1.0.0")
			e["spec"] = map[string]any{"endpoint": "https://user:pass@host"} // inline credential
			return e
		}(),
		func() map[string]any { e := mcpEntry("BadSlug!", "1.0.0"); return e }(), // bad slug
	}
	for i, b := range bad {
		if r := h.do("POST", "/v1/m/catalog/entries", editor, b, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Errorf("bad entry #%d code = %d, want 400 (%s)", i, r.code, r.raw)
		}
	}

	// Two versions of the same slug coexist as distinct registry artifacts.
	if r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("v1 = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "2.0.0"), tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("v2 = %d %s", r.code, r.raw)
	}
	// The same kind/slug/version is a duplicate.
	if r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("duplicate version = %d, want 409", r.code)
	}
}

// TestEntryRBAC proves the verb tiers: a viewer cannot create, an editor can
// create but cannot approve (admin-tier), an admin can approve.
func TestEntryRBAC(t *testing.T) {
	h := newHarness(t, false)
	root := h.adminLogin()
	tenant := h.createOrg(root, "acme")
	viewer := h.roleToken(root, tenant, "v@acme.com", auth.RoleViewer)
	editor := h.roleToken(root, tenant, "e@acme.com", auth.RoleEditor)
	adminRole := h.roleToken(root, tenant, "a@acme.com", auth.RoleAdmin)

	if r := h.do("POST", "/v1/m/catalog/entries", viewer, mcpEntry("github", "1.0.0"), tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", r.code)
	}
	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("editor create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor approve = %d, want 403 (approve is admin-tier)", r.code)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", adminRole, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("admin approve = %d %s", r.code, r.raw)
	}
}

// TestDeprecateRequiresApproved proves a draft or pending entry cannot jump
// straight to deprecated (which would present an unpinned artifact as retired).
func TestDeprecateRequiresApproved(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	adminRole := h.roleToken(admin, tenant, "a@acme.com", auth.RoleAdmin)

	r := h.do("POST", "/v1/m/catalog/entries", editor, mcpEntry("github", "1.0.0"), tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	// draft → deprecate is rejected.
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/deprecate", adminRole, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("deprecate draft = %d, want 409", r.code)
	}
	// pending → deprecate is also rejected.
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/submit", editor, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("submit = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/deprecate", adminRole, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("deprecate pending = %d, want 409", r.code)
	}
}

// TestEmptySpecVerifies proves an entry with no spec is still hash-pinned and
// verifies (the nil/empty-spec round-trip is deterministic).
func TestEmptySpecVerifies(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	body := map[string]any{"kind": "skill", "name": "Bare Skill", "slug": "bare", "version": "1.0.0"} // no spec
	r := h.do("POST", "/v1/m/catalog/entries", editor, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/catalog/entries/"+id+"/approve", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("approve = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["hash_ok"] != true || r.body["verified"] != true {
		t.Errorf("empty-spec entry must verify by hash: %d %v", r.code, r.body)
	}
}

// TestNameTamperDetected proves the display name is covered by the content hash:
// renaming an approved entry in the store breaks verification.
func TestNameTamperDetected(t *testing.T) {
	h := newHarness(t, false)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	id := h.createApproved(editor, admin, tenant, "github", "1.0.0")
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("catalog.entry")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec["name"] = "Trusted Vendor MCP" // relabel to impersonate a trusted entry
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["hash_ok"] != false || r.body["verified"] != false {
		t.Errorf("name tamper not detected: %v", r.body)
	}
}

// TestSignatureDowngradeDetected proves that, with a catalog signing key
// configured, stripping the signature off an approved entry is reported as NOT
// verified (a downgrade), not as a legitimately-unsigned entry.
func TestSignatureDowngradeDetected(t *testing.T) {
	h := newHarness(t, true) // signing enabled
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)

	id := h.createApproved(editor, admin, tenant, "github", "1.0.0")
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("catalog.entry")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, model.ID(id))
		if err != nil {
			return err
		}
		rec["signature"] = "" // strip the signature, leaving the (still-matching) hash
		rec["sig_alg"] = ""
		rec["signed_by"] = ""
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("GET", "/v1/m/catalog/entries/"+id+"/verify", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["verified"] != false {
		t.Errorf("signature downgrade not detected: %v", r.body)
	}
}

// TestPubkey reports signing status honestly in both modes.
func TestPubkey(t *testing.T) {
	hOff := newHarness(t, false)
	admin := hOff.adminLogin()
	tenant := hOff.createOrg(admin, "acme")
	viewer := hOff.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)
	if r := hOff.do("GET", "/v1/m/catalog/pubkey", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["signing_enabled"] != false {
		t.Errorf("pubkey (off) = %d %v", r.code, r.body)
	}

	hOn := newHarness(t, true)
	admin2 := hOn.adminLogin()
	tenant2 := hOn.createOrg(admin2, "acme")
	viewer2 := hOn.roleToken(admin2, tenant2, "v@acme.com", auth.RoleViewer)
	r := hOn.do("GET", "/v1/m/catalog/pubkey", viewer2, nil, tenantHdr(tenant2))
	if r.code != http.StatusOK || r.body["signing_enabled"] != true || r.body["public_key"] == nil {
		t.Errorf("pubkey (on) = %d %v", r.code, r.body)
	}
}

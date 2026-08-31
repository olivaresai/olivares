// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// tenantScopedGETs are the console's first calls after login. Every one of them
// resolves a tenant, so every one of them is a 400 "tenant required" on an
// install whose setup produced no organization.
var tenantScopedGETs = []string{"/v1/workspaces", "/v1/agents", "/v1/members", "/v1/audit"}

// TestFirstBootIsUsableWithoutManualProvisioning walks the WHOLE first-boot path
// the way the console does — setup, login, whoami, then tenant-scoped reads with
// the tenant whoami handed back — and requires 200 at the end.
//
// This is the regression test for an install that came up unusable: setup created
// a superadmin and nothing else, so whoami answered "grants":[], the console had
// no tenant to select and therefore sent no X-Olivares-Tenant, and every
// tenant-scoped route answered 400 {"error":{"code":"bad_request","message":
// "tenant required"}}. The reads below are deliberately issued with whatever
// tenant the grants produced (none, if the bug is back) and the failures are
// reported rather than fatal, so a regression prints the real 400 body.
func TestFirstBootIsUsableWithoutManualProvisioning(t *testing.T) {
	h := newHarness(t)

	// 1. Setup. Its answer must name the organization it created — the console
	//    cannot be asked to guess the tenant id.
	sr := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", sr.code, sr.raw)
	}
	orgBody, _ := sr.body["organization"].(map[string]any)
	setupTenant, _ := orgBody["tenant_id"].(string)
	if setupTenant == "" {
		t.Errorf("setup response does not name the organization it created (the console cannot select a tenant): %s", sr.raw)
	}
	if id, _ := orgBody["id"].(string); id != setupTenant {
		t.Errorf("organization id %q != tenant_id %q: an org's id IS its tenant id", id, setupTenant)
	}

	// 2. Login.
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	token, _ := lr.body["token"].(string)

	// 3. whoami — the console builds its tenant switcher from these grants.
	wr := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	if wr.code != http.StatusOK {
		t.Fatalf("whoami = %d %s", wr.code, wr.raw)
	}
	grants, _ := wr.body["grants"].([]any)
	tenant := ""
	role := ""
	if len(grants) == 1 {
		g, _ := grants[0].(map[string]any)
		tenant, _ = g["tenant"].(string)
		role, _ = g["role"].(string)
	}
	if len(grants) != 1 || tenant != setupTenant || role != auth.RoleOwner {
		t.Errorf("whoami grants = %v, want exactly one owner grant on %s", wr.body["grants"], setupTenant)
	}

	// 4. The tenant-scoped reads, with the tenant the console would have picked.
	hdr := map[string]string{}
	if tenant != "" {
		hdr["X-Olivares-Tenant"] = tenant
	}
	for _, path := range tenantScopedGETs {
		r := h.do("GET", path, token, nil, hdr)
		if r.code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200: a fresh install must be usable with no manual provisioning", path, r.code, r.raw)
		}
	}

	// 5. The tenant provisioned by setup is a fully-formed tenant: it carries the
	//    "Default" workspace CreateOrg seeds, like every tenant created later.
	wsr := h.do("GET", "/v1/workspaces", token, nil, hdr)
	items, _ := wsr.body["items"].([]any)
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["slug"] == "default" {
			found = true
		}
	}
	if !found {
		t.Errorf("first organization has no default workspace: %s", wsr.raw)
	}
}

// TestSetupUsesTheDefaultOrganizationNameWhenNoneGiven pins the neutral default:
// a setup body with no organization still produces one, named and slugged
// obviously rather than with an invented brand.
func TestSetupUsesTheDefaultOrganizationNameWhenNoneGiven(t *testing.T) {
	h := newHarness(t)
	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	org, _ := r.body["organization"].(map[string]any)
	if org["name"] != "Default Organization" || org["slug"] != "default" {
		t.Fatalf("default organization = %v, want name %q slug %q", org, "Default Organization", "default")
	}
	if org["status"] != string(model.StatusActive) {
		t.Fatalf("first organization status = %v, want %q", org["status"], model.StatusActive)
	}
}

// TestSetupAcceptsAnOrganizationName covers the optional name in the setup body,
// including the slug folded out of it.
func TestSetupAcceptsAnOrganizationName(t *testing.T) {
	h := newHarness(t)
	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
		"organization": "  Acme Robotics, S.L.  ",
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	org, _ := r.body["organization"].(map[string]any)
	if org["name"] != "Acme Robotics, S.L." {
		t.Fatalf("organization name = %v, want the trimmed operator-supplied name", org["name"])
	}
	if org["slug"] != "acme-robotics-s-l" {
		t.Fatalf("organization slug = %v, want %q", org["slug"], "acme-robotics-s-l")
	}
}

// TestSetupRollsBackItsTenantWhenTheSuperadminCannotBeCreated exercises the
// compensating drop. A setup that provisions a tenant and then fails to create
// its superadmin must not leave that tenant behind: it has no owner, and setup is
// still open.
//
// The failure is produced honestly, not by injection: setup consumes its token,
// so the token is re-minted and setup is driven a SECOND time under a different
// organization name. The tenant is provisioned, BootstrapSuperadminOwning then
// refuses with ErrSetupComplete (a user exists), and the rollback must remove it.
func TestSetupRollsBackItsTenantWhenTheSuperadminCannotBeCreated(t *testing.T) {
	h := newHarness(t)
	first := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if first.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", first.code, first.raw)
	}
	admin := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	adminTok, _ := admin.body["token"].(string)

	// Re-mint the consumed one-time token so the second attempt reaches the
	// provisioning path instead of being turned away at the door.
	plaintext, created, err := h.setupTokFile.Ensure()
	if err != nil || !created {
		t.Fatalf("re-mint setup token: created=%v err=%v", created, err)
	}
	second := h.do("POST", "/v1/setup", "", map[string]any{
		"token": plaintext, "email": "other@x.io", "password": "supersecret2",
		"organization": "Second Attempt",
	}, nil)
	if second.code != http.StatusConflict {
		t.Fatalf("second setup = %d %s, want 409 setup_complete", second.code, second.raw)
	}

	lr := h.do("GET", "/v1/system/orgs", adminTok, nil, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("list orgs = %d %s", lr.code, lr.raw)
	}
	items, _ := lr.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("orgs after a failed second setup = %d, want 1 (the tenant it provisioned must be rolled back): %s", len(items), lr.raw)
	}
	if m, _ := items[0].(map[string]any); m["slug"] != "default" {
		t.Fatalf("surviving org = %v, want the one the FIRST setup created", items[0])
	}
}

// TestSetupNeverDropsATenantThatAlreadyHasAnOwner is the other half of the
// rollback rule. When a losing setup adopts the tenant the winner already owns,
// the compensation must keep its hands off it: dropping it would delete the
// working install.
func TestSetupNeverDropsATenantThatAlreadyHasAnOwner(t *testing.T) {
	h := newHarness(t)
	first := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if first.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", first.code, first.raw)
	}
	org, _ := first.body["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)

	plaintext, created, err := h.setupTokFile.Ensure()
	if err != nil || !created {
		t.Fatalf("re-mint setup token: created=%v err=%v", created, err)
	}
	// Same (default) organization name → the losing attempt ADOPTS the winner's
	// tenant, then fails to bootstrap. It must not roll back what it adopted.
	if r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": plaintext, "email": "other@x.io", "password": "supersecret2",
	}, nil); r.code != http.StatusConflict {
		t.Fatalf("second setup = %d %s, want 409 setup_complete", r.code, r.raw)
	}

	// The original superadmin is still fully operational on its tenant. The
	// assertions below are the ones that actually see a wrongful drop: DropTenant
	// purges the org row, the tenant's workspaces AND the memberships pointing at
	// it, while login (a system-tenant credential) keeps working and a listing of
	// an emptied tenant still answers 200. A 200 alone would therefore prove
	// nothing.
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login after the failed second setup = %d %s", lr.code, lr.raw)
	}
	token, _ := lr.body["token"].(string)

	olr := h.do("GET", "/v1/system/orgs", token, nil, nil)
	items, _ := olr.body["items"].([]any)
	stillThere := false
	for _, it := range items {
		if m, _ := it.(map[string]any); m["tenant_id"] == tenant {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("the owned tenant %s was dropped by the losing setup: %s", tenant, olr.raw)
	}

	wr := h.do("GET", "/v1/auth/whoami", token, nil, nil)
	grants, _ := wr.body["grants"].([]any)
	if len(grants) != 1 {
		t.Fatalf("owner grants after the losing setup = %v, want the original one intact", wr.body["grants"])
	}

	hdr := map[string]string{"X-Olivares-Tenant": tenant}
	for _, path := range tenantScopedGETs {
		if r := h.do("GET", path, token, nil, hdr); r.code != http.StatusOK {
			t.Fatalf("GET %s = %d %s after a losing setup adopted the tenant", path, r.code, r.raw)
		}
	}
	wsr := h.do("GET", "/v1/workspaces", token, nil, hdr)
	wss, _ := wsr.body["items"].([]any)
	if len(wss) != 1 {
		t.Fatalf("workspaces after the losing setup = %d, want the seeded Default intact: %s", len(wss), wsr.raw)
	}
}

// TestSetupAdoptsAnOwnerlessOrganization covers the residue case named in
// firstOrg: a tenant left behind by an attempt whose rollback could not run. Its
// slug is unique, so a retry that created a fresh one would collide forever;
// setup adopts it instead and the install completes.
func TestSetupAdoptsAnOwnerlessOrganization(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var orphan model.TenantID
	if err := h.st.System(ctx, func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(ctx, model.Org{Name: "Default Organization", Slug: "default", Status: model.StatusActive})
		orphan = o.TenantID
		return err
	}); err != nil {
		t.Fatalf("seed the ownerless organization: %v", err)
	}

	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("setup over an ownerless organization = %d %s", r.code, r.raw)
	}
	org, _ := r.body["organization"].(map[string]any)
	if org["tenant_id"] != orphan.String() {
		t.Fatalf("setup created a second organization %v instead of adopting %s", org["tenant_id"], orphan)
	}

	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	token, _ := lr.body["token"].(string)
	olr := h.do("GET", "/v1/system/orgs", token, nil, nil)
	items, _ := olr.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("orgs after adoption = %d, want 1: %s", len(items), olr.raw)
	}
	if r := h.do("GET", "/v1/workspaces", token, nil, map[string]string{"X-Olivares-Tenant": orphan.String()}); r.code != http.StatusOK {
		t.Fatalf("GET /v1/workspaces on the adopted tenant = %d %s", r.code, r.raw)
	}
}

// TestSetupProvisionsNothingWhenTheRequestIsRejected keeps the pre-flight checks
// IN FRONT of provisioning: a request already known to be unusable must not mint
// a tenant and lean on the rollback to remove it again.
//
// A surviving-tenant count cannot see that (the rollback would have cleaned up
// either way) — the evidence ledger can: DropTenant records tenant.drop on the
// system chain, so a provision-then-roll-back leaves a permanent trace of a
// tenant that should never have existed.
func TestSetupProvisionsNothingWhenTheRequestIsRejected(t *testing.T) {
	h := newHarness(t)
	if r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": "olst_wrong", "email": "a@b.c", "password": "longenough1",
	}, nil); r.code != http.StatusForbidden {
		t.Fatalf("bad-token setup = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "a@b.c", "password": "short",
	}, nil); r.code != http.StatusBadRequest {
		t.Fatalf("weak-password setup = %d %s, want 400", r.code, r.raw)
	}
	ctx := context.Background()
	var count int
	if err := h.st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		for _, o := range orgs {
			if !o.TenantID.IsSystem() {
				count++
			}
		}
		return err
	}); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if count != 0 {
		t.Fatalf("rejected setups provisioned %d tenant(s), want 0", count)
	}

	// Setup still works afterwards, and the system chain shows no tenant was ever
	// created and thrown away.
	if r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("setup after the rejected attempts = %d %s", r.code, r.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	token, _ := lr.body["token"].(string)
	ar := h.do("GET", "/v1/audit/system", token, nil, nil)
	if ar.code != http.StatusOK {
		t.Fatalf("system audit = %d %s", ar.code, ar.raw)
	}
	events, _ := ar.body["items"].([]any)
	for _, it := range events {
		if m, _ := it.(map[string]any); m["action"] == "tenant.drop" {
			t.Fatalf("a rejected setup provisioned a tenant and rolled it back (tenant.drop on the system chain): %v", m)
		}
	}
}

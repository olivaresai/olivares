// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// enumerationBlindStore is the deployment the operator actually has on a first
// boot: Postgres opened on the application pool alone, no --admin-dsn. It is a
// decorator rather than a hand-written fake so every other path keeps the REAL
// sqlstore behavior, and ListOrgs returns the error the real engine returns —
// the sentinel WRAPPED in the operator remedy (sqlstore/system.go), not a naked
// sentinel this double could satisfy and production could not.
type enumerationBlindStore struct {
	store.Store
	// blind is a pointer so a test can complete setup on a working store and only
	// THEN lose the admin pool — the second REST path (GET /v1/system/orgs) is
	// unreachable on a first boot, and is reached exactly this way: an install that
	// was configured once and later restarted without --admin-dsn.
	blind *atomic.Bool
}

func (s enumerationBlindStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return s.Store.System(ctx, func(sys store.SystemScope) error {
		return fn(enumerationBlindSystem{SystemScope: sys, blind: s.blind})
	})
}

type enumerationBlindSystem struct {
	store.SystemScope
	blind *atomic.Bool
}

func (e enumerationBlindSystem) ListOrgs(ctx context.Context) ([]model.Org, error) {
	if e.blind != nil && !e.blind.Load() {
		return e.SystemScope.ListOrgs(ctx)
	}
	return nil, fmt.Errorf("%w: engine %q holds no BYPASSRLS admin pool, so this System read is RLS-limited to the cleared tenant GUC and returned %d row(s) that CANNOT be read as the whole estate; provision a NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql) and pass --admin-dsn",
		store.ErrEnumerationNotAuthoritative, "postgres", 0)
}

// blindFromTheStart is the first-boot deployment: the enumeration never worked.
func blindFromTheStart() *atomic.Bool {
	b := &atomic.Bool{}
	b.Store(true)
	return b
}

// First setup needs one organization, not full-estate enumeration. The default
// application pool must support setup and a usable owner sign-in on its own.
func TestFirstBootWithoutAdminPoolCreatesTheOrganization(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
	})
	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("setup = %d %s, want 201 without an admin pool", r.code, r.raw)
	}
	org, _ := r.body["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	if tenant == "" {
		t.Fatalf("setup did not return its organization: %s", r.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("sign-in = %d %s", login.code, login.raw)
	}
	token, _ := login.body["token"].(string)
	workspaces := h.do("GET", "/v1/workspaces", token, nil, map[string]string{"X-Olivares-Tenant": tenant})
	if workspaces.code != http.StatusOK || !strings.Contains(workspaces.raw, `"slug":"default"`) {
		t.Fatalf("first organization is not usable: %d %s", workspaces.code, workspaces.raw)
	}
}

// A failed estate read does not mean the requested slug is free. The database
// constraint must protect an unseen tenant without creating a first user.
func TestFirstBootWithoutAdminPoolDoesNotAdoptAnUnseenOrganization(t *testing.T) {
	var underlying store.Store
	h := newHarnessOpts(t, func(o *api.Options) {
		underlying = o.Store
		o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
	})
	ctx := context.Background()
	var tenant model.TenantID
	if err := underlying.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Existing", Slug: "default", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if r.code != http.StatusNotImplemented || !strings.Contains(r.raw, "cross_tenant_admin_pool_not_configured") {
		t.Fatalf("setup over an unseen organization = %d %s, want the authoritative-lookup remedy", r.code, r.raw)
	}
	if has, err := h.authr.HasAnyUser(ctx); err != nil || has {
		t.Fatalf("refused setup created a user: has=%t err=%v", has, err)
	}
	if err := underlying.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.GetOrg(ctx, tenant)
		if err == nil && org.Name != "Existing" {
			t.Fatalf("unseen organization was changed: %+v", org)
		}
		return err
	}); err != nil {
		t.Fatalf("unseen organization was lost: %v", err)
	}
}

// THE SECOND REST PATH, END TO END (finding 4 from the external contrast). The
// unit tests pin statusFor and writeError, and TestDeliberateRefusalsAreNotCacheable
// puts this URL on a synthetic request — but it calls writeError directly, so it
// would pass even if handleListOrgs swallowed the error or answered 200.
//
// This is also the only path where the CACHE finding actually bites: a GET that a
// private cache may store, on a deployment whose whole problem is that it is about
// to be reconfigured. The install is set up on a working store first, because this
// path is unreachable on a first boot — it needs a superadmin, which needs setup to
// have completed. That is the real shape: configured once, restarted without
// --admin-dsn.
func TestListOrgsWithoutAdminPoolRefusesLegiblyAndIsNotCacheable(t *testing.T) {
	blind := &atomic.Bool{} // starts working, so setup and login succeed
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blind}
	})

	sr := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("setup = %d %s", sr.code, sr.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}
	token, _ := lr.body["token"].(string)

	// The restart that lost the admin pool.
	blind.Store(true)

	r := h.do("GET", "/v1/system/orgs", token, nil, nil)
	if r.code == http.StatusInternalServerError {
		t.Fatalf("the org list still fails MUTE: %d %s", r.code, r.raw)
	}
	if r.code != http.StatusNotImplemented {
		t.Fatalf("GET /v1/system/orgs = %d %s, want %d", r.code, r.raw, http.StatusNotImplemented)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("response is not the standard error envelope: %s", r.raw)
	}
	if code, _ := errObj["code"].(string); code != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("error.code = %q", code)
	}
	msg, _ := errObj["message"].(string)
	if msg == "internal error" || !strings.Contains(msg, "--install-directory-inventory") {
		t.Errorf("the operator gets no remedy from the org list: %q", msg)
	}
	if strings.Contains(msg, "RLS-limited") {
		t.Errorf("the store's own error text reached the client: %q", msg)
	}
}

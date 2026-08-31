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

// THE WHOLE PATH, THROUGH THE REAL ROUTER. The unit tests next door pin
// statusFor and writeError; this one pins what an operator receives, because the
// defect was only ever visible from there.
//
// Reproduced against Postgres 16.14 on 2026-08-08 before the fix, on a throwaway
// database whose only role was NOSUPERUSER NOBYPASSRLS:
//
//	POST /v1/setup → 500 {"error":{"code":"internal","message":"internal error"}}
//
// Note the envelope: the body is nested under "error". The report this session was
// given quoted a flat {"code":…,"message":…}, which is not what the server has ever
// sent (core/api/errors.go, errorBody) — asserting the flat shape here would have
// passed against a body that does not exist.
func TestFirstBootWithoutAdminPoolTellsTheOperatorWhatToProvision(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Store = enumerationBlindStore{Store: o.Store, blind: blindFromTheStart()}
	})

	r := h.do("POST", "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)

	if r.code == http.StatusInternalServerError {
		t.Fatalf("first boot still fails MUTE: %d %s", r.code, r.raw)
	}
	if r.code != http.StatusNotImplemented {
		t.Fatalf("setup = %d %s, want %d", r.code, r.raw, http.StatusNotImplemented)
	}

	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("response is not the standard error envelope: %s", r.raw)
	}
	code, _ := errObj["code"].(string)
	msg, _ := errObj["message"].(string)
	if code != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("error.code = %q, want cross_tenant_admin_pool_not_configured", code)
	}
	if msg == "internal error" {
		t.Fatalf("the operator is still told %q — the diagnosis stayed in the server log", msg)
	}
	for _, want := range []string{"BYPASSRLS", "olivares db init", "--admin-role", "--admin-dsn"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the reply does not tell the operator about %q: %q", want, msg)
		}
	}
	// The remedy is a constant keyed on the code, so nothing the store wrapped into
	// its error can ride out on it.
	if strings.Contains(msg, "postgres://") || strings.Contains(msg, "RLS-limited") {
		t.Errorf("the store's own error text reached the client: %q", msg)
	}
}

// THE SECOND REST PATH, END TO END (F4 from the external contrast). The
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
	if msg == "internal error" || !strings.Contains(msg, "--admin-dsn") {
		t.Errorf("the operator gets no remedy from the org list: %q", msg)
	}
	if strings.Contains(msg, "RLS-limited") {
		t.Errorf("the store's own error text reached the client: %q", msg)
	}
}

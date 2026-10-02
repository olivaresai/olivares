// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// Real PostgreSQL setup on the documented application-role-only topology, with
// an administrative pool as the positive control for estate-wide operations.

// openFirstBootPostgres provisions an isolated database and opens the engine over
// it, with or without the administrative pool. It returns a store with no user in
// it: a first boot.
func openFirstBootPostgres(t *testing.T, withAdminPool bool) store.Store {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured: set OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to run the first-boot readiness leg")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 8,
	}
	if withAdminPool {
		cfg.AdminDSN = dsns.Admin
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := sqlstore.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open postgres (admin pool: %t): %v", withAdminPool, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// LEADERSHIP IS PART OF THE FIXTURE ON POSTGRES, and leaving it out is how the
	// first run of this file failed: the pgElector is armed by Run, so a store that
	// was merely opened reports IsLeader()==false and /readyz answers the standby
	// drain before it ever reaches a first-boot read. SQLite hides this — its
	// elector is the leader for its whole lifetime — so the two engines only test
	// the same thing when this runs.
	//
	// It is the production sequence from cmd/olivares/boot.go: the system-tenant
	// bootstrap is the OnPromote callback, and Run fires it while holding the
	// advisory lock. Resign releases that lock before the isolated database is
	// dropped.
	st.Leader().OnPromote(func(promoteCtx context.Context) error {
		return st.System(promoteCtx, func(sys store.SystemScope) error {
			_, err := sys.EnsureSystemTenant(promoteCtx)
			return err
		})
	})
	if err := st.Leader().Run(ctx); err != nil {
		t.Fatalf("acquire leadership: %v", err)
	}
	t.Cleanup(func() { _ = st.Leader().Resign(context.Background()) })
	if !st.Leader().IsLeader() {
		t.Fatal("the fixture did not become the active writer; a standby answers /readyz before any first-boot read")
	}
	return st
}

// The documented default uses the application role alone. Readiness, setup,
// owner sign-in and tenant-scoped reads must work without an administrative DSN.
func TestReadyzFirstBootOnPostgresWithoutAdministrativePool(t *testing.T) {
	st := openFirstBootPostgres(t, false)
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, nil)
	r := probeReadyz(t, h)
	if r.code != http.StatusOK || r.body["setup_required"] != true {
		t.Fatalf("/readyz = %d %s, want 200 setup_required=true", r.code, r.raw)
	}
	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s, want 201 on the app pool", sr.code, sr.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("sign-in = %d %s", login.code, login.raw)
	}
	token, _ := login.body["token"].(string)
	org, _ := sr.body["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	for _, path := range tenantScopedGETs {
		read := h.do("GET", path, token, nil, map[string]string{"X-Olivares-Tenant": tenant})
		if read.code != http.StatusOK {
			t.Errorf("GET %s = %d %s", path, read.code, read.raw)
		}
	}
	// Full-estate enumeration remains deliberately unavailable.
	list := h.do("GET", "/v1/system/orgs", token, nil, nil)
	if list.code != http.StatusNotImplemented {
		t.Fatalf("GET /v1/system/orgs = %d %s, want 501", list.code, list.raw)
	}
	after := probeReadyz(t, h)
	if after.code != http.StatusOK || after.body["setup_required"] != false {
		t.Fatalf("/readyz after setup = %d %s", after.code, after.raw)
	}
}

// TestReadyzFirstBootOnPostgresWithAdministrativePool is the other half, and it is
// what makes the first half mean something: the SAME engine, the SAME provisioning,
// with the administrative pool wired, walks ready → setup → ready.
func TestReadyzFirstBootOnPostgresWithAdministrativePool(t *testing.T) {
	st := openFirstBootPostgres(t, true)
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, nil)

	before := probeReadyz(t, h)
	if before.code != http.StatusOK {
		t.Fatalf("/readyz on a fresh PostgreSQL install WITH an admin pool = %d %s, want 200", before.code, before.raw)
	}
	if before.body["setup_required"] != true || before.body["status"] != "ok" {
		t.Errorf("/readyz body = %s, want status=ok setup_required=true", before.raw)
	}

	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusCreated {
		t.Fatalf("POST /v1/setup = %d %s, want 201 — readiness promised this would work", sr.code, sr.raw)
	}

	after := probeReadyz(t, h)
	if after.code != http.StatusOK || after.body["setup_required"] != false {
		t.Fatalf("/readyz after setup = %d %s, want 200 setup_required=false", after.code, after.raw)
	}
}

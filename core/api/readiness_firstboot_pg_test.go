// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// QA05 on the REAL engine. The decorator next door reproduces the error the store
// returns; only a real PostgreSQL reproduces the CONFIGURATION that produces it —
// FORCE ROW LEVEL SECURITY against a NOSUPERUSER NOBYPASSRLS application role whose
// System transaction has cleared its tenant GUC, so the cross-tenant read matches
// nothing and the store refuses to call that an empty estate.
//
// The two subtests differ in EXACTLY ONE input: whether store.Config.AdminDSN names
// the provisioned NOSUPERUSER BYPASSRLS role. Same cluster, same provisioning, same
// schema, same isolated database shape — so the difference in the readiness answer
// is attributable to the administrative pool and to nothing else.

// openFirstBootPostgres provisions an isolated database and opens the engine over
// it, with or without the administrative pool. It returns a store with no user in
// it: a first boot.
func openFirstBootPostgres(t *testing.T, withAdminPool bool) store.Store {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured: set OLIVARES_TEST_POSTGRES_SUPERUSER_DSN to run the first-boot readiness leg")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 8,
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

// TestReadyzFirstBootOnPostgresWithoutAdministrativePool is the measured defect on
// the engine that has it. The store answers its ping, this node holds the election
// lock, no user exists — every condition the old probe checked is satisfied — and
// the first thing this install must do cannot be done.
func TestReadyzFirstBootOnPostgresWithoutAdministrativePool(t *testing.T) {
	st := openFirstBootPostgres(t, false)
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, nil)

	r := probeReadyz(t, h)
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz on a fresh PostgreSQL install with no admin pool = %d %s, want 503", r.code, r.raw)
	}
	if r.body["status"] != "setup_blocked" || r.body["code"] != "cross_tenant_admin_pool_not_configured" {
		t.Errorf("/readyz body = %s, want status=setup_blocked code=cross_tenant_admin_pool_not_configured", r.raw)
	}
	if r.body["store"] != "up" || r.body["leader"] != true || r.body["setup_required"] != true {
		t.Errorf("/readyz body = %s, want store=up leader=true setup_required=true", r.raw)
	}
	if remedy, _ := r.body["remedy"].(string); !strings.Contains(remedy, "--admin-dsn") {
		t.Errorf("the readiness remedy does not name --admin-dsn: %q", remedy)
	}
	requireNoRawStoreText(t, r.raw)

	// The prediction is redeemed against the real ceremony: the setup readiness
	// refused to promise is the setup that actually refuses.
	sr := h.do(http.MethodPost, "/v1/setup", "", map[string]any{
		"token": h.setupTok, "email": "root@x.io", "password": "supersecret1",
	}, nil)
	if sr.code != http.StatusNotImplemented {
		t.Fatalf("POST /v1/setup on real PostgreSQL with no admin pool = %d %s, want 501", sr.code, sr.raw)
	}
	errObj, _ := sr.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "cross_tenant_admin_pool_not_configured" {
		t.Fatalf("POST /v1/setup answered %s, want the admin-pool refusal", sr.raw)
	}

	// Pod health is a different question and keeps its answer: this replica is
	// healthy, and a missing optional capability must not restart it or wedge a
	// StatefulSet rollout.
	if pod := h.do(http.MethodGet, "/pod-readyz", "", nil, nil); pod.code != http.StatusOK {
		t.Fatalf("/pod-readyz = %d %s, want 200", pod.code, pod.raw)
	}
	if live := h.do(http.MethodGet, "/livez", "", nil, nil); live.code != http.StatusOK {
		t.Fatalf("/livez = %d %s, want 200", live.code, live.raw)
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

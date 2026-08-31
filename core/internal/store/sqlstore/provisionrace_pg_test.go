// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// TestPostgresConcurrentRoleProvisioningDoesNotRace pins the invariant that
// ProvisionPostgres is safe to call from several provisioners at once against the
// SAME cluster.
//
// It calls ProvisionPostgres DIRECTLY rather than through pgtest.Provision, and that
// is the point rather than a shortcut: pgtest takes a harness-side lock, so going
// through it would test the harness. The unprotected door is the one production
// uses — N nodes booting in parallel under the Helm chart's
// podManagementPolicy: Parallel never touch pgtest at all.
//
// Roles are a CLUSTER object. Isolating the database does NOT isolate them, so two
// provisioners contend on the same pg_authid tuple even with unrelated databases,
// and upsertRole is a check-then-act (SELECT EXISTS, then CREATE or ALTER). Without
// the lock the window is wide enough for both to read "absent" and both to CREATE,
// or for both to ALTER and for one to be told `tuple concurrently updated (XX000)`.
// That is not hypothetical: it turned main red on mainline-ci on 2026-08-09.
//
// The mutant this is written to catch is removing the advisory lock from
// ProvisionPostgres, which still compiles.
func TestPostgresConcurrentRoleProvisioningDoesNotRace(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured")
	}
	super := os.Getenv(pgtest.EnvSuperuserDSN)
	if super == "" {
		t.Skipf("this test needs %s", pgtest.EnvSuperuserDSN)
	}

	// One role name, provisioned by everybody at once — the contended tuple. The
	// databases differ so the ONLY shared object is the role, which is what isolates
	// the failure to the thing under test.
	// Names must match the shape pgtest is willing to DROP
	// (^olv_(t_)?(p[0-9]+_)?[0-9a-f]{8,}$) — teardown only ever removes objects of that
	// shape, so a shared production role can never be reached by a test's cleanup. The
	// per-provisioner suffix is therefore a HEX digit, not a letter. Roles carry their own
	// shape (^olv_(to|tx|app|own)_...), which is why this one is olv_app_.
	sfx := pgtest.Suffix(t)
	role := "olv_app_" + sfx
	const provisioners = 8

	dbs := make([]string, provisioners)
	for i := range dbs {
		dbs[i] = "olv_t_" + sfx + string("0123456789abcdef"[i])
	}
	t.Cleanup(func() { pgtest.Drop(t, super, dbs[0], role) })
	for _, d := range dbs[1:] {
		d := d
		t.Cleanup(func() { pgtest.Drop(t, super, d) })
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, provisioners)
	start := make(chan struct{})
	for i := 0; i < provisioners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, so the check-then-act windows overlap
			_, errs[i] = ProvisionPostgres(ctx, super, store.PgProvisionSpec{
				Database: dbs[i],
				App:      store.PgRole{Name: role, Password: "olv-race-" + sfx},
			}, true)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			continue
		}
		// Name the shape explicitly: this is the error the lock exists to prevent, and
		// reporting it as "some error" would hide a regression behind a flake.
		if strings.Contains(err.Error(), "tuple concurrently updated") ||
			strings.Contains(err.Error(), "XX000") ||
			strings.Contains(err.Error(), "already exists") {
			t.Errorf("provisioner %d hit the role race the advisory lock exists to prevent: %v", i, err)
			continue
		}
		t.Errorf("provisioner %d failed: %v", i, err)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

func requireLoginCapabilityPG(t *testing.T) (store.Store, pgSplitDSNs) {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability PostgreSQL legs", pgtest.EnvSuperuserDSN)
	}
	return openAccessEvidencePG(t)
}

func pgSQLState(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code
	}
	return ""
}

func TestPGLoginCapabilitySplitRoleLifecycleAndExactPrivileges(t *testing.T) {
	st, dsns := requireLoginCapabilityPG(t)
	ctx := context.Background()
	owner := openCustodyPGPool(t, dsns.Owner)
	app := openCustodyPGPool(t, dsns.App)

	var twelve, thirteen int
	if err := owner.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE version = 12), count(*) FILTER (WHERE version = 13) FROM public.schema_migrations_core`).
		Scan(&twelve, &thirteen); err != nil {
		t.Fatalf("read tracking: %v", err)
	}
	if twelve != 0 || thirteen != 1 {
		t.Fatalf("tracking v12=%d v13=%d, want 0 and 1", twelve, thirteen)
	}
	if obs := readLoginCapabilityView(t, st); obs.Present {
		t.Fatalf("a fresh PostgreSQL store manufactured history: %+v", obs)
	}
	first := observeLoginCapability(t, st, "artifact-1")
	second := observeLoginCapability(t, st, "artifact-2")
	if first.ObservationCount != 1 || second.ObservationCount != 2 || !second.FirstObservedAt.Equal(first.FirstObservedAt) {
		t.Fatalf("observations first=%+v second=%+v", first, second)
	}

	for name, stmt := range map[string]string{
		"update first_observed_at": `UPDATE public.login_capability_observation SET first_observed_at = now()`,
		"update capability_key":    `UPDATE public.login_capability_observation SET capability_key = capability_key`,
		"delete":                   `DELETE FROM public.login_capability_observation`,
		"truncate":                 `TRUNCATE public.login_capability_observation`,
	} {
		if _, err := app.ExecContext(ctx, stmt); pgSQLState(err) != "42501" {
			t.Errorf("application role %s = %v, want SQLSTATE 42501", name, err)
		}
	}
	if _, err := app.ExecContext(ctx, `UPDATE public.login_capability_observation SET observation_count = observation_count`); err != nil {
		t.Errorf("application role column-level UPDATE was refused: %v", err)
	}

	roles := guardRoles{
		App:             guardRoleFact{Known: true, Role: currentCustodyRole(t, app)},
		Owner:           guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)},
		OwnerConfigured: true,
	}
	verify := func() error {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		return verifyPostgresLoginCapabilityRelation(ctx, tx, roles)
	}
	if err := verify(); err != nil {
		t.Fatalf("the migrated relation failed its own verifier: %v", err)
	}
	// Negative control: an application role with unexpected mutation authority is not a
	// passing split-role witness.
	if _, err := owner.ExecContext(ctx, `GRANT DELETE ON public.login_capability_observation TO `+quoteIdent(roles.App.Role)); err != nil {
		t.Fatalf("grant drift fixture: %v", err)
	}
	if err := verify(); err == nil {
		t.Fatal("an application role holding DELETE passed the verifier")
	}
	if _, err := owner.ExecContext(ctx, `REVOKE DELETE ON public.login_capability_observation FROM `+quoteIdent(roles.App.Role)); err != nil {
		t.Fatalf("revoke drift fixture: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `GRANT SELECT ON public.login_capability_observation TO PUBLIC`); err != nil {
		t.Fatalf("public drift fixture: %v", err)
	}
	if err := verify(); err == nil {
		t.Fatal("a PUBLIC grant passed the verifier")
	}
}

func TestPGLoginCapabilityLockSerializesMissingRowAndBoundsAcquisition(t *testing.T) {
	st, dsns := requireLoginCapabilityPG(t)
	ctx := context.Background()
	owner := openCustodyPGPool(t, dsns.Owner)

	holder, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('core.login.capability', 0))`); err != nil {
		t.Fatalf("hold the capability lock: %v", err)
	}

	// The five-second child cap bounds a blocked acquisition on a missing row.
	start := time.Now()
	var lockErr error
	err = st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, lockErr = as.LoginCapability().Lock(ctx)
		return nil // discard: the retained error must still refuse the commit
	})
	elapsed := time.Since(start)
	if lockErr == nil || err == nil || elapsed < 4500*time.Millisecond || elapsed > 9*time.Second {
		t.Fatalf("blocked Lock: lockErr=%v mutateErr=%v elapsed=%s, want a retained error near 5s", lockErr, err, elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("AuthMutate did not preserve the underlying deadline error: %v", err)
	}

	// An earlier parent deadline wins over the child cap.
	pctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	start = time.Now()
	err = st.AuthMutate(pctx, func(as store.AuthScope) error {
		_, err := as.LoginCapability().Lock(pctx)
		return err
	})
	cancel()
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("parent deadline: err=%v elapsed=%s", err, time.Since(start))
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	if obs := readLoginCapabilityView(t, st); obs.Present {
		t.Fatalf("refused transactions left history: %+v", obs)
	}

	const workers = 6
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- st.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := as.LoginCapability().Lock(ctx); err != nil {
					return err
				}
				_, err := as.LoginCapability().Observe(ctx, "artifact")
				return err
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent observation on a missing row: %v", err)
		}
	}
	if obs := readLoginCapabilityView(t, st); obs.ObservationCount != workers {
		t.Fatalf("observation count = %d, want %d", obs.ObservationCount, workers)
	}
}

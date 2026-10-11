// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgstore_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
)

func TestSchemaMigrationPreservesBucketsOnReopen(t *testing.T) {
	dsn := testDSN(t)
	st, db := openStoreOn(t, dsn)
	ctx := context.Background()
	if _, _, err := st.Take(ctx, reqs("kept", 0, 10, 0, 10)); err != nil {
		t.Fatal(err)
	}
	var applied string
	if err := db.QueryRowContext(ctx, "SELECT applied_at FROM public.schema_migrations_ratelimit WHERE version = 1").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := pgstore.Open(ctx, dsn, pgstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var tokens float64
	if err := db.QueryRowContext(ctx, "SELECT tokens FROM public.ratelimit_buckets WHERE key = 'kept|read'").Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 9 {
		t.Fatalf("tokens after reopen = %v, want 9", tokens)
	}
	var again string
	if err := db.QueryRowContext(ctx, "SELECT applied_at FROM public.schema_migrations_ratelimit WHERE version = 1").Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != applied {
		t.Fatal("rate-limit schema migration replayed")
	}
}

func TestMigratedFunctionDriftRefusesBoot(t *testing.T) {
	dsn := testDSN(t)
	st, db := openStoreOn(t, dsn)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`CREATE OR REPLACE FUNCTION public.olivares_ratelimit_take(p_keys pg_catalog.text[], p_rates pg_catalog.float8[], p_bursts pg_catalog.float8[])
RETURNS TABLE(allowed pg_catalog.bool, tokens pg_catalog.float8)
LANGUAGE plpgsql SECURITY INVOKER SET search_path = pg_catalog
AS $$ BEGIN RETURN QUERY SELECT true, 100::pg_catalog.float8; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	unexpected, err := pgstore.Open(context.Background(), dsn, pgstore.Options{})
	if err == nil {
		unexpected.Close()
		t.Fatal("migrated function drift admitted")
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The default PostgreSQL install has the application and owner roles and no
// BYPASSRLS admin pool. Its cross-tenant org read is authoritative exactly while
// the attested closed directory inventory routine is installed:
//   - absent: ListOrgs refuses, ListOrgsVisible says not authoritative;
//   - installed (on the running store, no reopen): every org, suspended included;
//   - drifted (not closed): ListOrgs refuses naming the attestation failure,
//     ListOrgsVisible falls back to the RLS-limited read without an error.
func TestPostgresListOrgsWithoutAdminPoolUsesTheClosedInventory(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner}
	raw, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open with the application and owner roles: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	tenants := []model.TenantID{provisionTenant(t, raw, "inventory-a"), provisionTenant(t, raw, "inventory-b"), provisionTenant(t, raw, "inventory-c")}
	if err := raw.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.SetOrgStatus(ctx, tenants[1], model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("suspend a tenant: %v", err)
	}
	list := func() ([]model.Org, error) {
		var orgs []model.Org
		err := raw.System(ctx, func(sys store.SystemScope) error {
			var err error
			orgs, err = sys.ListOrgs(ctx)
			return err
		})
		return orgs, err
	}
	visible := func() bool {
		t.Helper()
		var authoritative bool
		if err := raw.System(ctx, func(sys store.SystemScope) error {
			var err error
			_, authoritative, err = sys.ListOrgsVisible(ctx)
			return err
		}); err != nil {
			t.Fatalf("ListOrgsVisible: %v", err)
		}
		return authoritative
	}

	// Absent.
	if _, err := list(); !errors.Is(err, store.ErrEnumerationNotAuthoritative) || !strings.Contains(err.Error(), "no closed directory inventory routine") {
		t.Fatalf("ListOrgs without the routine = %v, want the non-authoritative refusal naming the routine", err)
	}
	if visible() {
		t.Fatal("ListOrgsVisible without the routine says authoritative")
	}

	// Installed, on the running store.
	pgSuper := os.Getenv(pgtest.EnvSuperuserDSN)
	f2aInstallInventoryForConfig(t, cfg, pgSuper)
	orgs, err := list()
	if err != nil {
		t.Fatalf("ListOrgs with the routine: %v", err)
	}
	got := map[model.TenantID]model.LifecycleStatus{}
	for _, o := range orgs {
		got[o.TenantID] = o.Status
	}
	if len(orgs) != 4 || got[model.SystemTenantID] == "" {
		t.Fatalf("ListOrgs with the routine = %d orgs %v, want SYSTEM and the three tenants", len(orgs), got)
	}
	for _, tenant := range tenants {
		if _, ok := got[tenant]; !ok {
			t.Fatalf("ListOrgs with the routine misses tenant %s: %v", tenant, got)
		}
	}
	if got[tenants[1]] != model.StatusSuspended {
		t.Fatalf("suspended tenant status = %q, want suspended", got[tenants[1]])
	}
	if !slices.IsSortedFunc(orgs, func(a, b model.Org) int { return strings.Compare(a.ID.String(), b.ID.String()) }) {
		t.Fatal("ListOrgs with the routine is not ordered by id")
	}
	if !visible() {
		t.Fatal("ListOrgsVisible with the routine says not authoritative")
	}

	// Drifted: the routine is no longer closed.
	var database string
	if err := raw.(*sqlStore).db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	parsed, err := pgx.ParseConfig(pgSuper)
	if err != nil {
		t.Fatal("invalid fixture DSN")
	}
	super, closeDB, err := openOnDatabase(parsed, database)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	if _, err := super.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() TO PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err := list(); !errors.Is(err, store.ErrEnumerationNotAuthoritative) || !strings.Contains(err.Error(), "fails attestation") {
		t.Fatalf("ListOrgs with a drifted routine = %v, want the refusal naming the attestation failure", err)
	}
	if visible() {
		t.Fatal("ListOrgsVisible with a drifted routine says authoritative")
	}
	if _, err := super.ExecContext(ctx, "REVOKE EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err := list(); err != nil {
		t.Fatalf("ListOrgs after the routine is closed again: %v", err)
	}
}

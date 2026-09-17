// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openSQLiteTest opens an in-memory SQLite store with the debug statement guard
// on, registering modules via register (may be nil). It is closed at test end.
func openSQLiteTest(t *testing.T, register func(store.ExtensionRegistry) error) store.Store {
	t.Helper()
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Debug:  true,
	}, register)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// provisionTenant creates a tenant via the System path and returns its id.
//
// It provisions in the order the product boots: the reserved SYSTEM organization
// first, through the same idempotent EnsureSystemTenant the active writer runs at
// promotion (cmd/olivares/boot.go, promote), and only then the business tenant.
// Since core v10 the boot inventory decoder treats a business organization
// without the SYSTEM witness as corruption and refuses to reopen, so a fixture
// that skipped genesis was not a smaller fixture: it was a database no real
// deployment can produce. A test that needs that corrupt state on purpose calls
// provisionTenantWithoutSystemWitness and says so.
func provisionTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	return provisionTenantWith(t, st, slug, true)
}

// provisionTenantWithoutSystemWitness creates a business tenant on a store whose
// SYSTEM organization has deliberately NOT been provisioned. It exists for the
// negative cases that prove a nonempty inventory lacking the SYSTEM witness is
// refused; it is never a shortcut for an ordinary fixture, because the store it
// leaves behind cannot be reopened.
func provisionTenantWithoutSystemWitness(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	return provisionTenantWith(t, st, slug, false)
}

func provisionTenantWith(t *testing.T, st store.Store, slug string, systemFirst bool) model.TenantID {
	t.Helper()
	ctx := context.Background()
	var org model.Org
	err := st.System(ctx, func(sys store.SystemScope) error {
		if systemFirst {
			if _, err := sys.EnsureSystemTenant(ctx); err != nil {
				return fmt.Errorf("ensure SYSTEM tenant: %w", err)
			}
		}
		o, err := sys.CreateOrg(ctx, model.Org{
			Name: slug, Slug: slug, Status: model.StatusActive,
		})
		org = o
		return err
	})
	if err != nil {
		t.Fatalf("provision tenant %q: %v", slug, err)
	}
	if org.TenantID.IsZero() || org.ID.String() != org.TenantID.String() {
		t.Fatalf("provision tenant %q: bad org id/tenant: id=%s tenant=%s", slug, org.ID, org.TenantID)
	}
	return org.TenantID
}

// mustCreateAgent creates an agent in tenant and returns it.
func mustCreateAgent(t *testing.T, st store.Store, tenant model.TenantID, name string) model.Agent {
	t.Helper()
	var got model.Agent
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: name, Kind: "claude-code", Status: model.StatusActive,
		})
		got = a
		return err
	})
	if err != nil {
		t.Fatalf("create agent %q: %v", name, err)
	}
	return got
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
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
func provisionTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var org model.Org
	err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(context.Background(), model.Org{
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

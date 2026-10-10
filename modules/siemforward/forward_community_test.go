// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package siemforward

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

func TestCommunityLedgerForwardingPreservesCursor(t *testing.T) {
	ctx := context.Background()
	// Keep the same schema and real eventing engine that Business resumes.
	evt := eventing.New(eventing.WithSinkRenderer(NewRenderer()))
	m := New(evt)
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if err := evt.RegisterSchema(reg); err != nil {
			return err
		}
		return m.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := api.NewModuleData(st)
	evt.UseData(data)
	m.UseData(data)
	if err := data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(cursorKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{colCurLastForwarded: int64(41)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := m.ForwardDue(ctx, tenant); n != 0 || !errors.Is(err, audit.ErrBusinessAudit) {
		t.Fatalf("forward=%d,%v", n, err)
	}
	if NewRenderer() != nil {
		t.Fatal("Community has a SIEM renderer")
	}
	if err := data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(cursorKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Int(colCurLastForwarded) != 41 {
			t.Fatalf("saved cursor changed: %v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

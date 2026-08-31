// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// widgetDescriptor is a fixture module entity used to exercise the extension
// registry end to end. It is audited so the module-audit-in-same-transaction
// path is covered too.
var widgetDescriptor = model.EntityDescriptor{
	Kind:    "rrw.widget",
	Table:   "rrw_widget",
	Audited: true,
	Fields: []model.FieldSpec{
		{Name: "label", Kind: model.KindText},
		{Name: "count", Kind: model.KindInt},
	},
}

func registerWidget(reg store.ExtensionRegistry) error { return reg.Register(widgetDescriptor) }

func TestModuleEntityRoundTripAndIsolation(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerWidget)
	tenantA := provisionTenant(t, st, "alpha")
	tenantB := provisionTenant(t, st, "bravo")

	var widgetID model.ID
	if err := st.Mutate(ctx, tenantA, func(sc store.Scope) error {
		repo, err := sc.Ext("rrw.widget")
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{"label": "w1", "count": int64(7)})
		if err != nil {
			return err
		}
		widgetID = model.ID(rec.String("id"))
		if rec.String(model.ColTenantID) != tenantA.String() {
			t.Fatalf("module create: tenant = %s, want %s", rec.String(model.ColTenantID), tenantA)
		}
		// The audit chain got the provisioning event + the module create.
		rep, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !rep.OK || rep.Checked != 2 {
			t.Fatalf("module audit verify = %+v, want OK/2", rep)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant A module ops: %v", err)
	}

	// Tenant B cannot see A's module row.
	if err := st.View(ctx, tenantB, func(sc store.Scope) error {
		repo, err := sc.Ext("rrw.widget")
		if err != nil {
			return err
		}
		_, err = repo.Get(ctx, widgetID)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("B.Get(A widget): err = %v, want ErrNotFound", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("tenant B view: %v", err)
	}

	// Unknown kind is rejected.
	_ = st.View(ctx, tenantA, func(sc store.Scope) error {
		if _, err := sc.Ext("rrw.nope"); !errors.Is(err, store.ErrUnknownEntity) {
			t.Fatalf("Ext(unknown): err = %v, want ErrUnknownEntity", err)
		}
		return nil
	})
}

func TestRegistryValidationRejects(t *testing.T) {
	cases := []struct {
		name string
		desc model.EntityDescriptor
	}{
		{"reserved column", model.EntityDescriptor{Kind: "rrw.a", Table: "rrw_a",
			Fields: []model.FieldSpec{{Name: "id", Kind: model.KindText}}}},
		{"core namespace", model.EntityDescriptor{Kind: "core.a", Table: "core_a"}},
		{"append+softdelete", model.EntityDescriptor{Kind: "rrw.b", Table: "rrw_b", AppendOnly: true, SoftDelete: true}},
		{"bad table prefix", model.EntityDescriptor{Kind: "rrw.c", Table: "widgets"}},
		{"non-namespaced kind", model.EntityDescriptor{Kind: "widget", Table: "rrw_w"}},
		{"redact on int", model.EntityDescriptor{Kind: "rrw.d", Table: "rrw_d",
			Fields: []model.FieldSpec{{Name: "n", Kind: model.KindInt, Redact: true}}}},
		{"unique index without tenant_id", model.EntityDescriptor{Kind: "rrw.e", Table: "rrw_e",
			Fields:  []model.FieldSpec{{Name: "email", Kind: model.KindText}},
			Indexes: []model.IndexSpec{{Name: "rrw_e_email", Columns: []string{"email"}, Unique: true}}}},
		{"index on unknown column", model.EntityDescriptor{Kind: "rrw.f", Table: "rrw_f",
			Indexes: []model.IndexSpec{{Name: "rrw_f_x", Columns: []string{"nope"}}}}},
		{"bad index name", model.EntityDescriptor{Kind: "rrw.g", Table: "rrw_g",
			Fields:  []model.FieldSpec{{Name: "a", Kind: model.KindText}},
			Indexes: []model.IndexSpec{{Name: "Bad Name", Columns: []string{"a"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(context.Background(),
				store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
				func(reg store.ExtensionRegistry) error { return reg.Register(tc.desc) })
			if !errors.Is(err, store.ErrInvalidDescriptor) {
				t.Fatalf("Open with bad descriptor: err = %v, want ErrInvalidDescriptor", err)
			}
		})
	}
}

// TestModuleUniqueIndexWithTenantAccepted is the positive counterpart: a unique
// index that leads with tenant_id (per-tenant uniqueness) is allowed.
func TestModuleUniqueIndexWithTenantAccepted(t *testing.T) {
	st, err := Open(context.Background(),
		store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
		func(reg store.ExtensionRegistry) error {
			return reg.Register(model.EntityDescriptor{
				Kind: "rrw.account", Table: "rrw_account",
				Fields:  []model.FieldSpec{{Name: "email", Kind: model.KindText}},
				Indexes: []model.IndexSpec{{Name: "rrw_account_email", Columns: []string{"tenant_id", "email"}, Unique: true}},
			})
		})
	if err != nil {
		t.Fatalf("valid per-tenant unique index rejected: %v", err)
	}
	_ = st.Close()
}

// TestDropTenantWithAppendOnlyModule proves a tenant with append-only module
// data can still be dropped: the deletable tables are purged and the append-only
// (immutable evidence) table is retained rather than deadlocking the drop.
func TestDropTenantWithAppendOnlyModule(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, func(reg store.ExtensionRegistry) error {
		return reg.Register(model.EntityDescriptor{
			Kind: "rrw.event", Table: "rrw_event", AppendOnly: true,
			Fields: []model.FieldSpec{{Name: "msg", Kind: model.KindText}},
		})
	})
	tenant := provisionTenant(t, st, "acme")
	agent := mustCreateAgent(t, st, tenant, "bot")

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("rrw.event")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{"msg": "boot"})
		return err
	}); err != nil {
		t.Fatalf("seed append-only row: %v", err)
	}

	if err := st.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); err != nil {
		t.Fatalf("drop tenant: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(agents) != 0 {
			t.Fatalf("deletable rows survived drop: %d agents", len(agents))
		}
		repo, err := sc.Ext("rrw.event")
		if err != nil {
			return err
		}
		events, _, err := repo.List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(events) != 1 {
			t.Fatalf("append-only rows = %d, want 1 (retained as evidence)", len(events))
		}
		return nil
	}); err != nil {
		t.Fatalf("post-drop view: %v", err)
	}
	_ = agent
}

// TestModuleTablesNameKeyedAcrossReopen proves module tables are tracked by name,
// not registration position: reopening with a reordered/extended module set
// still creates every table (a positional scheme would skip the new one).
func TestModuleTablesNameKeyedAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "mods.db")
	descA := model.EntityDescriptor{Kind: "rrw.a", Table: "rrw_a", Fields: []model.FieldSpec{{Name: "n", Kind: model.KindInt}}}
	descB := model.EntityDescriptor{Kind: "rrw.b", Table: "rrw_b", Fields: []model.FieldSpec{{Name: "m", Kind: model.KindInt}}}

	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, func(reg store.ExtensionRegistry) error {
		return reg.Register(descA)
	})
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	_ = st1.Close()

	// Reopen with B registered BEFORE A (new module + reorder).
	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, func(reg store.ExtensionRegistry) error {
		if err := reg.Register(descB); err != nil {
			return err
		}
		return reg.Register(descA)
	})
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer st2.Close()

	tenant := provisionTenant(t, st2, "acme")
	if err := st2.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, kind := range []model.Kind{"rrw.a", "rrw.b"} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			if _, err := repo.Create(ctx, model.Record{"n": int64(1), "m": int64(2)}); err != nil {
				return err // would fail with "no such table" if B was skipped
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("both module tables must exist after reopen: %v", err)
	}
}

func TestModuleTableCollisionRejected(t *testing.T) {
	// A module table that collides with a core table name must be rejected.
	_, err := Open(context.Background(),
		store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
		func(reg store.ExtensionRegistry) error {
			return reg.Register(model.EntityDescriptor{Kind: "rrw.agents", Table: "agents"})
		})
	if !errors.Is(err, store.ErrInvalidDescriptor) {
		t.Fatalf("collision: err = %v, want ErrInvalidDescriptor", err)
	}
}

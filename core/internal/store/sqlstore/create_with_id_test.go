// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const strictCreateID = model.ID("018f22e2-79b0-7cc3-8a1b-2c3d4e5f6789")

var createWithIDDescriptor = model.EntityDescriptor{
	Kind:       "rrw.preassigned",
	Table:      "rrw_preassigned",
	Audited:    true,
	SoftDelete: true,
	Fields: []model.FieldSpec{
		{Name: "label", Kind: model.KindText},
		{Name: "count", Kind: model.KindInt},
	},
}

func registerCreateWithIDEntity(reg store.ExtensionRegistry) error {
	return reg.Register(createWithIDDescriptor)
}

// TestCreateWithIDIsStrict exercises the same public GenericRepo contract on
// SQLite and PostgreSQL: exact identity, engine stamping, audit attribution,
// collision behavior, rejection without SQL and the unchanged random Create.
func TestCreateWithIDIsStrict(t *testing.T) {
	engines := []struct {
		name string
		open func(*testing.T) store.Store
	}{
		{name: "sqlite", open: func(t *testing.T) store.Store {
			return openSQLiteTest(t, registerCreateWithIDEntity)
		}},
		{name: "postgres", open: func(t *testing.T) store.Store {
			dsn := isolatedPG(t).App
			st, err := Open(context.Background(), store.Config{
				Engine: store.EnginePostgres,
				DSN:    dsn,
			}, registerCreateWithIDEntity)
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		}},
	}

	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			testCreateWithIDIsStrict(t, engine.open(t))
		})
	}
}

func testCreateWithIDIsStrict(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant := provisionTenant(t, st, "create-with-id")

	var generated model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(createWithIDDescriptor.Kind)
		if err != nil {
			return err
		}
		got, err := repo.CreateWithID(ctx, strictCreateID, model.Record{
			"label":            "chosen",
			"count":            int64(7),
			model.ColID:        model.NewID().String(),
			model.ColTenantID:  model.NewTenantID().String(),
			model.ColCreatedAt: "caller-created",
			model.ColUpdatedAt: "caller-updated",
			model.ColVersion:   int64(99),
			model.ColDeletedAt: "caller-deleted",
		})
		if err != nil {
			return err
		}
		if got.String(model.ColID) != strictCreateID.String() {
			t.Errorf("CreateWithID id = %q, want exact %q", got.String(model.ColID), strictCreateID)
		}
		if got.String(model.ColTenantID) != tenant.String() {
			t.Errorf("tenant = %q, want stamped %q", got.String(model.ColTenantID), tenant)
		}
		if got.Int(model.ColVersion) != 1 {
			t.Errorf("version = %d, want stamped 1", got.Int(model.ColVersion))
		}
		if deleted, ok := got[model.ColDeletedAt]; !ok || deleted != nil {
			t.Errorf("deleted_at = %#v (present %t), want stamped NULL", deleted, ok)
		}
		if got.String(model.ColCreatedAt) == "" ||
			got.String(model.ColCreatedAt) != got.String(model.ColUpdatedAt) {
			t.Errorf("timestamps were not stamped together: created=%q updated=%q",
				got.String(model.ColCreatedAt), got.String(model.ColUpdatedAt))
		}

		random, err := repo.Create(ctx, model.Record{
			"label":     "generated",
			"count":     int64(8),
			model.ColID: strictCreateID.String(),
		})
		if err != nil {
			return err
		}
		generated = model.ID(random.String(model.ColID))
		if generated == strictCreateID {
			t.Error("Create reused the caller-supplied id instead of generating one")
		}
		assertCanonicalV7(t, generated)

		resource, err := sc.Resources().Create(ctx, model.Resource{
			Name: "exact-self-path", Kind: "folder",
		})
		if err != nil {
			return err
		}
		assertCanonicalV7(t, resource.ID)
		if want := "/" + resource.ID.String(); resource.Path != want {
			t.Errorf("resource path = %q, want exact self path %q", resource.Path, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("create exact and generated rows: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		found := false
		if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == createWithIDDescriptor.Kind.Name()+".create" && ev.TargetID == strictCreateID {
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !found {
			t.Errorf("audit chain has no create event targeting %s", strictCreateID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read audit: %v", err)
	}

	// The colliding insert is a distinct transaction: PostgreSQL aborts the
	// failed statement's transaction, while the original row must stay committed.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(createWithIDDescriptor.Kind)
		if err != nil {
			return err
		}
		_, err = repo.CreateWithID(ctx, strictCreateID, model.Record{
			"label": "collision", "count": int64(9),
		})
		return err
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("colliding CreateWithID error = %v, want ErrConflict", err)
	}

	invalid := []struct {
		name string
		id   model.ID
	}{
		{name: "empty", id: ""},
		{name: "all-zero", id: "00000000-0000-0000-0000-000000000000"},
		{name: "malformed", id: "not-a-uuid"},
		{name: "uppercase", id: "018F22E2-79B0-7CC3-8A1B-2C3D4E5F6789"},
		{name: "compact", id: "018f22e279b07cc38a1b2c3d4e5f6789"},
		{name: "urn", id: "urn:uuid:018f22e2-79b0-7cc3-8a1b-2c3d4e5f6789"},
		{name: "braces", id: "{018f22e2-79b0-7cc3-8a1b-2c3d4e5f6789}"},
		{name: "v4", id: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "non-rfc-v7", id: "018f22e2-79b0-7cc3-ca1b-2c3d4e5f6789"},
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(createWithIDDescriptor.Kind)
		if err != nil {
			return err
		}
		for _, tc := range invalid {
			_, err := repo.CreateWithID(ctx, tc.id, model.Record{
				"label": tc.name, "count": int64(10),
			})
			if !errors.Is(err, store.ErrInvalidID) {
				t.Errorf("CreateWithID(%s) error = %v, want ErrInvalidID", tc.name, err)
			}
		}
		rows, _, err := repo.List(ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 2 {
			t.Errorf("widget rows after invalid ids = %d, want unchanged 2", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("reject invalid ids: %v", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(createWithIDDescriptor.Kind)
		if err != nil {
			return err
		}
		_, err = repo.CreateWithID(ctx, "not-a-uuid", model.Record{
			"label": "view-invalid", "count": int64(11),
		})
		if !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("CreateWithID invalid id in View = %v, want ErrReadOnly", err)
		}
		_, err = repo.CreateWithID(ctx, model.NewID(), model.Record{
			"label": "view", "count": int64(12),
		})
		if !errors.Is(err, store.ErrReadOnly) {
			t.Errorf("CreateWithID in View error = %v, want ErrReadOnly", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("read-only check: %v", err)
	}

	// The collision never rewrote the original and the generated row remains
	// addressable under the distinct UUIDv7 that Create allocated.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(createWithIDDescriptor.Kind)
		if err != nil {
			return err
		}
		chosen, err := repo.Get(ctx, strictCreateID)
		if err != nil {
			return err
		}
		if chosen.String("label") != "chosen" {
			t.Errorf("colliding insert rewrote label to %q", chosen.String("label"))
		}
		_, err = repo.Get(ctx, generated)
		return err
	}); err != nil {
		t.Fatalf("read committed ids: %v", err)
	}
}

func assertCanonicalV7(t *testing.T, id model.ID) {
	t.Helper()
	u, err := uuid.Parse(id.String())
	if err != nil {
		t.Errorf("id %q does not parse: %v", id, err)
		return
	}
	if u.String() != id.String() || strings.ToLower(id.String()) != id.String() ||
		u.Version() != uuid.Version(7) || u.Variant() != uuid.RFC4122 {
		t.Errorf("id %q is not canonical RFC 4122 UUIDv7", id)
	}
}

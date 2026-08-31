// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The unit-G compatibility record's SCHEMA SHAPE, on the real engine.
//
// The module that owns the record lives outside this package and its decision logic
// is engine-neutral Go, so what needs the real engine is the shape it asks for — and
// that shape is unusual in two ways this repository has not exercised before:
//
//   - a unique index on tenant_id ALONE, which is how "the compatibility line is
//     drawn exactly once per tenant and can never be redrawn" is expressed. Every
//     other unique index here leads with the tenant and then discriminates.
//   - APPEND-ONLY module tables that the runtime writes on a hot path, so the
//     append-only ACL reconcile and the immutability guard both apply to them.
//
// Verifying that on SQLite only would be verifying the half that cannot fail: it is
// Postgres that carries FORCE row-level security, a deliberately NOBYPASSRLS
// application role, and an ACL the engine re-asserts on every boot. These descriptors
// MIRROR the eventing ones rather than importing them, because core must not depend on
// a module (scripts/check-boundary.sh) — so the mirror is checked against the real
// thing by a comment on each side rather than by the compiler, which is a limitation
// worth stating plainly.

const (
	compatSeedKind  model.Kind = "rrw.compat_seed"
	compatExcKind   model.Kind = "rrw.compat_exception"
	compatSeedTable            = "rrw_compat_seed"
	compatExcTable             = "rrw_compat_exception"
)

// registerCompatShape mirrors egressSeedDescriptor and egressExceptionDescriptor
// (modules/eventing/schema.go).
func registerCompatShape(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:       compatSeedKind,
		Table:      compatSeedTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "seed_batch", Kind: model.KindText},
			{Name: "subscription_count", Kind: model.KindInt},
			{Name: "seed_digest", Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name: "rrw_compat_seed_uniq", Columns: []string{model.ColTenantID}, Unique: true,
		}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:       compatExcKind,
		Table:      compatExcTable,
		AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: "subscription_ref", Kind: model.KindText, Indexed: true},
			{Name: "authority_kind", Kind: model.KindText},
			{Name: "authority_digest", Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			Name:    "rrw_compat_exception_uniq",
			Columns: []string{model.ColTenantID, "subscription_ref", "authority_digest"},
			Unique:  true,
		}},
	})
}

// TestCompatibilityRecordShapeOnPostgres proves the three properties the record's
// correctness rests on, against the engine that can actually refuse them.
func TestCompatibilityRecordShapeOnPostgres(t *testing.T) {
	ctx := context.Background()
	// The SPLIT-role topology: a separate owner runs the DDL and the application role
	// is NOSUPERUSER NOBYPASSRLS, which is the deployment the append-only ACL reconcile
	// and the row-level guards are actually FOR.
	dsns := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin,
		MaxConns: 6,
	}, registerCompatShape)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	alpha := provisionTenant(t, st, "alpha")
	bravo := provisionTenant(t, st, "bravo")

	write := func(tenant model.TenantID, digest string) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(compatExcKind)
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{
				"subscription_ref": "sub-1", "authority_kind": "canonical_v1",
				"authority_digest": digest,
			})
			return err
		})
	}
	seed := func(tenant model.TenantID) error {
		return st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(compatSeedKind)
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{
				"seed_batch": "b1", "subscription_count": int64(1), "seed_digest": "d",
			})
			return err
		})
	}

	// 1. The seed row is unique PER TENANT, which is what makes the line undrawable
	// twice. Two tenants may each have one; one tenant may not have two.
	if err := seed(alpha); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	if err := seed(bravo); err != nil {
		t.Fatalf("seed bravo — a unique index on tenant_id alone must not couple tenants: %v", err)
	}
	if err := seed(alpha); err == nil {
		t.Fatal("a second seed row was accepted for the same tenant: the compatibility line could be redrawn, and its whole value is that it did not move")
	}

	// 2. Exceptions are unique per (tenant, subscription, authority), so a concurrent
	// second seeding pass collides and rolls back instead of doubling the record a
	// decision is counted from.
	if err := write(alpha, "digest-a"); err != nil {
		t.Fatalf("write exception: %v", err)
	}
	if err := write(alpha, "digest-a"); err == nil {
		t.Fatal("a duplicate exception row was accepted")
	}
	if err := write(bravo, "digest-a"); err != nil {
		t.Fatalf("the same authority in another tenant was refused: %v", err)
	}

	// 3. The record is APPEND-ONLY on the engine, not merely by convention. An
	// actuation is approved against it, so a row that could be edited afterwards is not
	// evidence of anything.
	err = st.Mutate(ctx, alpha, func(sc store.Scope) error {
		repo, err := sc.Ext(compatExcKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("want one exception row, got %d", len(rows))
		}
		rows[0]["authority_digest"] = "tampered"
		_, err = repo.Update(ctx, rows[0])
		return err
	})
	if err == nil {
		t.Fatal("a compatibility exception was updated in place")
	}
	if !errors.Is(err, store.ErrAppendOnly) {
		t.Logf("update refused with %v (not the append-only sentinel, but refused)", err)
	}

	// 4. And a tenant sees only its own. The report an operator reads is per tenant, and
	// it names hosts.
	if err := st.View(ctx, bravo, func(sc store.Scope) error {
		repo, err := sc.Ext(compatExcKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 100})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("bravo sees %d exception rows, want only its own 1", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("view bravo: %v", err)
	}
}

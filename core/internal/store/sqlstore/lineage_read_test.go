// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"testing"
	"time"
)

func TestLineageReadLeaseSQLite(t *testing.T) {
	testLineageReadLease(t, openSQLiteTest(t, registerAuthorityLeaseFact))
}
func TestLineageReadLeasePostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	st, err := Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4}, registerAuthorityLeaseFact)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	testLineageReadLease(t, st)
}
func testLineageReadLease(t *testing.T, st store.Store) {
	ctx := context.Background()
	tenant := provisionTenant(t, st, "read-lease")
	var claim model.Record
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		claim, err = repo.Create(ctx, model.Record{"sid": "read-lease", "holder": "holder", "fence": int64(7), "claim_state": "active", "lease_expires_at": model.NewTimestamp(time.Now().Add(time.Hour)).String()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := authorityLeaseFactRef(t, claim)
	validate := func(refs []store.AuthorizationFactRef) error {
		return st.View(ctx, tenant, func(sc store.Scope) error {
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			confined, err := store.ConfineWorkspace(ctx, sc, ws.ID)
			if err != nil {
				return err
			}
			return store.ValidateReadAuthority(ctx, confined, refs)
		})
	}
	if err := validate([]store.AuthorizationFactRef{ref}); err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, ref.ID)
		if err != nil {
			return err
		}
		if row.Int(model.ColVersion) != ref.Version || row.String(model.ColUpdatedAt) != claim.String(model.ColUpdatedAt) {
			t.Fatal("read barrier renewed or touched lease")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, refs := range map[string][]store.AuthorizationFactRef{
		"empty": nil, "duplicate": {ref, ref}, "payload_missing": {{Kind: ref.Kind, ID: ref.ID, Version: ref.Version}},
		"unregistered": {{Kind: "not.authority", ID: ref.ID, Version: ref.Version}},
		"noncanonical": {{Kind: ref.Kind, ID: "not-a-uuid", Version: ref.Version}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(refs); err == nil {
				t.Fatal("malformed read authority accepted")
			}
		})
	}
	subject, fence, deadline, _ := ref.LeaseFenceWitness()
	wrong, err := store.NewLeaseFenceAuthorizationFactRef(ref.Kind, ref.ID, ref.Version, subject, fence+1, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate([]store.AuthorizationFactRef{wrong}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("fence mismatch = %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(authorityLeaseFactKind)
		if err != nil {
			return err
		}
		claim["lease_expires_at"] = model.NewTimestamp(time.Now().Add(-time.Minute)).String()
		claim, err = repo.Update(ctx, claim)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := validate([]store.AuthorizationFactRef{authorityLeaseFactRef(t, claim)}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expired lease accepted at DB time: %v", err)
	}
}

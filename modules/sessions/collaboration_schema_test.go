// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCollaborationStorageIsAuditedAndWorkspaceBound(t *testing.T) {
	reg := &workSchemaCaptureRegistry{}
	if err := New().RegisterSchema(reg); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sessions.share", "sessions.proposal"} {
		found := false
		for _, d := range reg.descriptors {
			if string(d.Kind) == kind {
				found = true
				if !d.Audited || d.WorkspaceLineage.Column != "workspace_id" {
					t.Errorf("%s lacks audit/workspace lineage", kind)
				}
			}
		}
		if !found {
			t.Errorf("%s not registered", kind)
		}
	}
	covers := map[string]bool{}
	for _, column := range New().RetirementCovers() {
		covers[column] = true
	}
	for _, column := range []string{"sessions.share.owner_id", "sessions.share.guest_id"} {
		if !covers[column] {
			t.Errorf("retirement coverage missing: %s", column)
		}
	}
}

func TestCollaborationStorageTenantIsolationAndConflict(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			_, st := openProfileModule(t, backend, nil)
			t.Cleanup(func() { _ = st.Close() })
			a, b := ensureTenant(t, st, "collab-a"), ensureTenant(t, st, "collab-b")
			ctx := context.Background()
			var created model.Record
			if err := st.Mutate(ctx, a, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.share")
				if err != nil {
					return err
				}
				created, err = repo.Create(ctx, model.Record{"run_ref": "storage-run", "run_id": model.NewID().String(), "owner_id": model.NewID().String(), "guest_id": model.NewID().String(), "expires_at": model.NewTimestamp(time.Now().Add(time.Hour)).String(), "revoked": false})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.View(ctx, b, func(sc store.Scope) error {
				repo, e := sc.Ext("sessions.share")
				if e != nil {
					return e
				}
				_, e = repo.Get(ctx, model.ID(created.String("id")))
				return e
			}); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("foreign share visible: %v", err)
			}
			stale := model.Record{}
			for k, v := range created {
				stale[k] = v
			}
			if err := st.Mutate(ctx, a, func(sc store.Scope) error {
				repo, e := sc.Ext("sessions.share")
				if e != nil {
					return e
				}
				created["revoked"] = true
				_, e = repo.Update(ctx, created)
				return e
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, a, func(sc store.Scope) error {
				repo, e := sc.Ext("sessions.share")
				if e != nil {
					return e
				}
				_, e = repo.Update(ctx, stale)
				return e
			}); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("stale share overwrite allowed: %v", err)
			}
		})
	}
}

func TestCollaborationRetirementRevokesEitherParticipant(t *testing.T) {
	for _, column := range []string{"owner_id", "guest_id"} {
		t.Run(column, func(t *testing.T) {
			_, st := openProfileModule(t, profileBackends(t)[0], nil)
			t.Cleanup(func() { _ = st.Close() })
			tenant := ensureTenant(t, st, "collab-retire")
			user := model.NewID()
			ctx := context.Background()
			var id model.ID
			err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.share")
				if err != nil {
					return err
				}
				row := model.Record{"run_ref": "run", "run_id": model.NewID().String(), "owner_id": model.NewID().String(), "guest_id": model.NewID().String(), "expires_at": model.NewTimestamp(time.Now().Add(time.Hour)).String(), "revoked": false}
				row[column] = user.String()
				row, err = repo.Create(ctx, row)
				if err != nil {
					return err
				}
				id = model.ID(row.String("id"))
				return retireCollaborationShares(ctx, sc, user)
			})
			if err != nil {
				t.Fatal(err)
			}
			err = st.View(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.share")
				if err != nil {
					return err
				}
				row, err := repo.Get(ctx, id)
				if err == nil && !row.Bool("revoked") {
					t.Error("retirement left active access")
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

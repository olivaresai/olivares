// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Both engines must publish the same usable Store after fresh preparation and
// after reopening durable data. PostgreSQL also exercises the owner/app split;
// only provisioning differs, never the contract assertions.
func TestOpenStoreContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres", "postgres-split"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "store.db"), Debug: true}
			if backend != "sqlite" {
				cfg.Engine = store.EnginePostgres
				if backend == "postgres-split" {
					pg := isolatedPGSplit(t)
					cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
				} else {
					pg := isolatedPG(t)
					cfg.DSN, cfg.AdminDSN = pg.App, pg.Admin
				}
			}
			cfg.AuditSpoolMaxBytes = largeAuditSpoolBudget
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if st != nil {
					_ = st.Close()
				}
			})
			tenant := provisionTenant(t, st, "open-contract")
			other := provisionTenant(t, st, "other-tenant")
			agent := mustCreateAgent(t, st, tenant, "persisted-agent")
			var widget model.Record
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(widgetDescriptor.Kind)
				if err != nil {
					return err
				}
				widget, err = repo.Create(ctx, model.Record{"label": "persisted-widget", "count": int64(7)})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var user model.User
			if err := st.AuthMutate(ctx, func(sc store.AuthScope) error {
				var err error
				user, err = sc.Users().Create(ctx, model.User{Email: "open@example.com", Status: model.StatusActive})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rollback := errors.New("roll back the whole mutation")
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				changed := agent
				changed.Name = "must-not-persist"
				if _, err := sc.Agents().Update(ctx, changed); err != nil {
					return err
				}
				return rollback
			}); !errors.Is(err, rollback) {
				t.Fatalf("rollback error = %v", err)
			}
			receipts, err := MigrationStatus(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"fresh", "reopened"} {
				t.Run(phase, func(t *testing.T) {
					if err := st.View(ctx, tenant, func(sc store.Scope) error {
						got, err := sc.Agents().Get(ctx, agent.ID)
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(got, agent) {
							t.Fatalf("core row changed: got %+v, want %+v", got, agent)
						}
						repo, err := sc.Ext(widgetDescriptor.Kind)
						if err != nil {
							return err
						}
						record, err := repo.Get(ctx, model.ID(widget.String("id")))
						if err != nil {
							return err
						}
						if !reflect.DeepEqual(record, widget) {
							t.Fatalf("module row changed: got %v, want %v", record, widget)
						}
						report, err := sc.Audit().Verify(ctx, 1)
						if err == nil && (!report.OK || report.Checked != 2) {
							t.Fatalf("audit chain = %+v, want provisioning and module-create events", report)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if err := st.View(ctx, other, func(sc store.Scope) error {
						if _, err := sc.Agents().Get(ctx, agent.ID); !errors.Is(err, store.ErrNotFound) {
							t.Fatalf("cross-tenant core read = %v", err)
						}
						repo, err := sc.Ext(widgetDescriptor.Kind)
						if err != nil {
							return err
						}
						if _, err := repo.Get(ctx, model.ID(widget.String("id"))); !errors.Is(err, store.ErrNotFound) {
							t.Fatalf("cross-tenant module read = %v", err)
						}
						if _, err := sc.Ext("core.user"); !errors.Is(err, store.ErrUnknownEntity) {
							t.Fatalf("tenant auth-partition access = %v", err)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if err := st.AuthView(ctx, func(sc store.AuthScope) error {
						got, err := sc.Users().Get(ctx, user.ID)
						if err == nil && !reflect.DeepEqual(got, user) {
							t.Fatalf("auth row changed: got %+v, want %+v", got, user)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				})
				if phase == "fresh" {
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					st, err = Open(ctx, cfg, registerWidget)
					if err != nil {
						t.Fatalf("reopen durable store: %v", err)
					}
					after, err := MigrationStatus(ctx, cfg)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(receipts, after) {
						t.Fatal("reopen changed migration receipts")
					}
				}
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func openPlanConfig(t *testing.T, engine store.Engine) store.Config {
	t.Helper()
	cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "plan.db")}
	if engine == store.EnginePostgres {
		pg := isolatedPGSplit(t)
		cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
	}
	return cfg
}

func TestStoreOpenPlanStopsAndCloses(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, stop := range []string{"prepareSchema", "finishSchemaOnly", "verifyReadiness", "runMaintenance", "publishStore"} {
			t.Run(string(engine)+"/"+stop, func(t *testing.T) {
				ctx := context.Background()
				cfg := openPlanConfig(t, engine)
				b := &storePreparation{cfg: cfg}
				failure := errors.New("preparation stopped")
				if stop == "prepareSchema" {
					// Fail inside preparation, after it acquired all configured pools.
					b.register = func(store.ExtensionRegistry) error { return failure }
				}
				plan := b.bootPlan()
				var reached []string
				for i, step := range plan {
					plan[i].run = func(ctx context.Context) error {
						reached = append(reached, step.name)
						if step.name == stop && stop != "prepareSchema" {
							return failure
						}
						return step.run(ctx)
					}
				}
				st, err := b.runBootPlan(ctx, plan)
				if st != nil || !errors.Is(err, failure) {
					t.Fatalf("failed preparation = %v, %v", st, err)
				}
				if reached[len(reached)-1] != stop {
					t.Fatalf("steps continued after refusal: %v", reached)
				}
				assertPreparationPoolsClosed(t, b)
				// This reaches the real fences and migration lock again. Returning an
				// error must not strand either resource and block the next startup.
				st, err = Open(ctx, cfg, nil)
				if err != nil {
					t.Fatalf("open after refusal: %v", err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestStoreOpenPlanPartialPurposesNeverPublish(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, purpose := range []string{"schema", "maintenance"} {
			t.Run(string(engine)+"/"+purpose, func(t *testing.T) {
				ctx := context.Background()
				b := &storePreparation{cfg: openPlanConfig(t, engine)}
				stop := "finishSchemaOnly"
				called := false
				if purpose == "schema" {
					b.purpose = prepareSchemaOnly
				} else {
					st, err := Open(ctx, b.cfg, nil)
					if err != nil {
						t.Fatal(err)
					}
					provisionTenant(t, st, "maintenance")
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					stop = "runMaintenance"
					b.maintenance = func(st *sqlStore) error {
						called = true
						if st.elector != nil {
							t.Fatal("maintenance received a serving elector")
						}
						return st.Ping(ctx)
					}
				}
				plan := b.bootPlan()
				var last string
				for i, step := range plan {
					plan[i].run = func(ctx context.Context) error {
						last = step.name
						return step.run(ctx)
					}
				}
				st, err := b.runBootPlan(ctx, plan)
				if err != nil || st != nil || last != stop || b.el != nil {
					t.Fatalf("partial preparation: store=%v error=%v last=%s elector=%v", st, err, last, b.el)
				}
				if called != (purpose == "maintenance") {
					t.Fatalf("maintenance callback reached = %t", called)
				}
				if purpose == "schema" && b.adminOpened {
					t.Fatal("schema-only preparation opened an admin pool")
				}
				assertPreparationPoolsClosed(t, b)
			})
		}
	}
}

func assertPreparationPoolsClosed(t *testing.T, b *storePreparation) {
	t.Helper()
	for name, db := range map[string]*sql.DB{"app": b.db, "owner": b.ownerDB, "admin": b.adminDB} {
		if db != nil && db.PingContext(context.Background()) == nil {
			t.Fatalf("%s pool survived unpublished preparation", name)
		}
	}
}

func TestOpenStoreRefusesFutureSchemaWithoutWrites(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			cfg := openPlanConfig(t, engine)
			dia, _ := dialect.New(engine)
			ownerCfg := cfg
			if cfg.OwnerDSN != "" {
				ownerCfg.DSN = cfg.OwnerDSN
			}
			db, err := openDB(ownerCfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			createCoreVersionTracking(t, ctx, db)
			insertCoreVersion(t, ctx, db, dia, coreSupportedMigrationVersion+1, nil)
			before := coreVersionSchemaSnapshot(t, ctx, db, dia)
			st, err := Open(ctx, cfg, registerWidgetStaged)
			if st != nil {
				_ = st.Close()
				t.Fatal("published a store with an unsupported schema")
			}
			if !errors.Is(err, ErrCoreSchemaVersionAhead) {
				t.Fatalf("future schema refusal = %v", err)
			}
			if after := coreVersionSchemaSnapshot(t, ctx, db, dia); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal mutated schema: before=%v after=%v", before, after)
			}
			assertCoreVersionControlsAbsent(t, ctx, db, dia)
		})
	}
}

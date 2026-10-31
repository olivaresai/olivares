// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// registerAITermsGuardFixture is the Community registry of 0.5: the 0.4 registry plus the two append-only AI terms
// relations modules/aiterms registers. The kinds and tables are catalog identities.
func registerAITermsGuardFixture(reg store.ExtensionRegistry) error {
	if err := registerWidget(reg); err != nil {
		return err
	}
	for _, descriptor := range []model.EntityDescriptor{
		{Kind: "aiterms.policy", Table: "aiterms_policy", AppendOnly: true},
		{Kind: "aiterms.acceptance", Table: "aiterms_acceptance", AppendOnly: true},
	} {
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	return nil
}

// A store the previous Community release wrote, whose guard census has no AI terms relations, opens with the
// registry that adds them, keeps its data and its audit chain, and opens again.
func TestAITermsGuardUpgradeOpensThePreviousCommunityStore(t *testing.T) {
	ctx := context.Background()
	previous, path := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)
	tenant := provisionTenant(t, previous, "aiterms-upgrade")
	var id model.ID
	if err := previous.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(widgetDescriptor.Kind)
		if err != nil {
			return err
		}
		record, err := repo.Create(ctx, model.Record{"label": "kept through upgrade", "count": int64(7)})
		id = model.ID(record.String("id"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		current, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, registerAITermsGuardFixture)
		if err != nil {
			t.Fatalf("open %d with the AI terms relations: %.600s", reopen, err)
		}
		if err := current.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(widgetDescriptor.Kind)
			if err != nil {
				return err
			}
			record, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			if record.String("label") != "kept through upgrade" {
				t.Fatal("stored data changed")
			}
			report, err := sc.Audit().Verify(ctx, 1)
			if err != nil {
				return err
			}
			if !report.OK {
				t.Fatalf("audit continuity lost: %+v", report)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		assertAITermsGuarded(t, current.(*sqlStore), tenant)
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// The same upgrade on PostgreSQL, with one role and with a split owner.
func TestAITermsGuardUpgradePostgres(t *testing.T) {
	testAITermsGuardUpgradePostgres(t, registerAITermsGuardFixture)
}

func testAITermsGuardUpgradePostgres(t *testing.T, register func(store.ExtensionRegistry) error) {
	t.Helper()
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "single role", true: "split owner"}[split], func(t *testing.T) {
			pg := isolatedPG(t)
			if split {
				pg = isolatedPGSplit(t)
			}
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
			if split {
				cfg.OwnerDSN, cfg.AdminDSN = pg.Owner, pg.Admin
			}
			ctx := context.Background()
			previous, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatal(err)
			}
			tenant := provisionTenant(t, previous, "aiterms-pg")
			if err := previous.Close(); err != nil {
				t.Fatal(err)
			}
			for reopen := 0; reopen < 2; reopen++ {
				current, err := Open(ctx, cfg, register)
				if err != nil {
					t.Fatalf("PostgreSQL open %d with the AI terms relations: %.600s", reopen, err)
				}
				if err := current.View(ctx, tenant, func(sc store.Scope) error {
					report, err := sc.Audit().Verify(ctx, 1)
					if err != nil {
						return err
					}
					if !report.OK {
						t.Fatalf("audit continuity lost: %+v", report)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				assertAITermsGuarded(t, current.(*sqlStore), tenant)
				if err := current.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func assertAITermsGuarded(t *testing.T, st *sqlStore, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	for _, kind := range []string{"aiterms.policy", "aiterms.acceptance"} {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(model.Kind(kind))
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		table := strings.ReplaceAll(kind, ".", "_")
		found := false
		_, manifest, _, err := bootGuardCensus(ctx, st.db, st.dia, st.reg.appendOnlyTables())
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range manifest.Specs {
			if spec.Key.Relation == table {
				found = true
			}
		}
		if !found {
			t.Fatalf("AI terms relation missing from engine guard census: %s", table)
		}
		for _, statement := range []string{"UPDATE %s SET id = id", "DELETE FROM %s"} {
			if _, err := st.db.ExecContext(ctx, fmt.Sprintf(statement, table)); err == nil {
				t.Fatalf("unguarded AI terms relation: %s", fmt.Sprintf(statement, table))
			} else if !strings.Contains(err.Error(), "append-only") && !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("unexpected mutation refusal: %v", err)
			}
		}
	}
}

func registerBusinessAITermsGuardFixture(reg store.ExtensionRegistry) error {
	if err := registerCommercialGuardFixture(reg); err != nil {
		return err
	}
	for _, d := range []model.EntityDescriptor{
		{Kind: "aiterms.policy", Table: "aiterms_policy", AppendOnly: true},
		{Kind: "aiterms.acceptance", Table: "aiterms_acceptance", AppendOnly: true},
	} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

func TestAITermsGuardBusinessOpensPreviousCommunity(t *testing.T) {
	ctx := context.Background()
	previous, path := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)
	tenant := provisionTenant(t, previous, "aiterms-business")
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		current, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, registerBusinessAITermsGuardFixture)
		if err != nil {
			t.Fatalf("Business open %d: %.600s", reopen, err)
		}
		assertAITermsGuarded(t, current.(*sqlStore), tenant)
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAITermsGuardBusinessUpgradePostgres(t *testing.T) {
	testAITermsGuardUpgradePostgres(t, registerBusinessAITermsGuardFixture)
}

func TestGuardCensusForPreviousCommunityHistory(t *testing.T) {
	previous := append([]string{"audit_events", "set_seen_jtis", guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}, guardAccessEvidenceTables[:]...)
	registered := append(append([]string(nil), previous...), guardAITermsTables[:]...)
	sort.Strings(registered)
	variantOf := func(t *testing.T, tables []string) (guardManifest, guardBootstrapReceiptVariant) {
		t.Helper()
		manifest, err := buildGuardManifest(tables)
		if err != nil {
			t.Fatal(err)
		}
		variants, err := guardBootstrapReceiptVariants(manifest)
		if err != nil {
			t.Fatal(err)
		}
		for _, variant := range variants {
			if variant.State == guardEditionReceiptsCurrentV9Completed && variant.Path == "5" {
				return manifest, variant
			}
		}
		t.Fatal("missing source variant")
		return guardManifest{}, guardBootstrapReceiptVariant{}
	}
	seed := func(t *testing.T, tables []string) (*sql.DB, dialect.Dialect) {
		t.Helper()
		manifest, variant := variantOf(t, tables)
		db, dia := guardEmptyEditionSQLiteDB(t)
		seedGuardEditionVariant(t, db, dia, manifest, variant.Path, variant.Receipts)
		return db, dia
	}
	ctx := context.Background()
	refuses := func(t *testing.T, db *sql.DB, dia dialect.Dialect, what string) {
		t.Helper()
		census, _, _, err := bootGuardCensus(ctx, db, dia, registered)
		if err != nil {
			t.Fatal(err)
		}
		before := accessEvidenceReadDurableCensus(t, db)
		_, err = runGuardAdmissionUnits(ctx, db, dia, census, "", false, nil)
		if !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("%s admitted or refused for another reason (census %v): %v", what, census, err)
		}
		accessEvidenceAssertDurableCensusUnchanged(t, what, before, accessEvidenceReadDurableCensus(t, db))
	}

	t.Run("the previous release's ledger selects the current census and verifies", func(t *testing.T) {
		db, dia := seed(t, previous)
		census, _, _, err := bootGuardCensus(ctx, db, dia, registered)
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string(nil), registered...)
		sort.Strings(want)
		if !slices.Equal(census, want) {
			t.Fatalf("census = %v, want the current %v", census, want)
		}
		if _, err := runGuardAdmissionUnits(ctx, db, dia, census, "", false, nil); err != nil {
			t.Fatalf("start on the previous release's history: %.500s", err)
		}
	})

	t.Run("a receipt of the current census mixed in refuses", func(t *testing.T) {
		db, dia := seed(t, previous)
		_, current := variantOf(t, registered)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertGuardReceipt(ctx, tx, dia, current.Receipts[len(current.Receipts)-1]); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		refuses(t, db, dia, "a mixed ledger")
	})

	t.Run("a stray relation refuses", func(t *testing.T) {
		db, dia := seed(t, append(append([]string(nil), previous...), "unknown_evidence"))
		refuses(t, db, dia, "a stray relation")
	})

	t.Run("half the pair keeps the registry census and refuses", func(t *testing.T) {
		db, dia := seed(t, append(append([]string(nil), previous...), guardAITermsTables[0]))
		census, _, _, err := bootGuardCensus(ctx, db, dia, registered)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(census, registered) {
			t.Fatalf("half the pair changed the census: %v, want the registered %v", census, registered)
		}
		refuses(t, db, dia, "half the pair")
	})
}

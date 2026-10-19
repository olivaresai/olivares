// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These are catalog identities, not private schemas or implementations. The same
// engine guard protects the three additional append-only Business descriptors.
var commercialGuardFixtureTables = []string{
	"ldap_revision", "session_cockpit_console_audit_index", "session_cockpit_runtime_session_original",
}

func TestCommercialGuardLatestEditionFollowsSeal(t *testing.T) {
	seen := map[bool]bool{}
	for index := 0; index < 64 && len(seen) < 2; index++ {
		tables := []string{fmt.Sprintf("ordering_probe_%d", index), guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}
		tables = append(tables, guardAccessEvidenceTables[:]...)
		source, err := buildGuardManifest(tables)
		if err != nil {
			t.Fatal(err)
		}
		target, err := buildGuardManifest(append(tables, commercialGuardFixtureTables...))
		if err != nil {
			t.Fatal(err)
		}
		empty, err := emptyRetainedDigest()
		if err != nil {
			t.Fatal(err)
		}
		sourceRollout, err := guardBootstrapRollout(source, 0, empty)
		if err != nil {
			t.Fatal(err)
		}
		targetRollout, err := guardBootstrapRollout(target, 0, empty)
		if err != nil {
			t.Fatal(err)
		}
		sourceSortsLast := sourceRollout.RolloutID > targetRollout.RolloutID
		if seen[sourceSortsLast] {
			continue
		}
		seen[sourceSortsLast] = true
		t.Run(fmt.Sprintf("source hash sorts last=%t", sourceSortsLast), func(t *testing.T) {
			db, dia := guardEmptyEditionSQLiteDB(t)
			variants, err := guardBootstrapReceiptVariants(source)
			if err != nil {
				t.Fatal(err)
			}
			var terminal [32]byte
			for _, variant := range variants {
				if variant.State == guardEditionReceiptsCurrentV9Completed && variant.Path == "5" {
					seedGuardEditionVariant(t, db, dia, source, variant.Path, variant.Receipts)
					terminal = variant.TerminalReceiptID
					break
				}
			}
			ctx := context.Background()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, rollout := range []guardRolloutContext{sourceRollout, targetRollout} {
				if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
					RolloutID: rollout.RolloutID, Kind: gateEventPendingOpened, Format: rollout.Format,
					CodeEpoch: rollout.CodeEpoch, CodeSHA256: rollout.CodeSHA256,
					RetainedRevision: 0, RetainedSHA256: empty, Phase: gatePhasePending,
					Condition: gateConditionClean, ExpectedUnits: []string{"ordering-witness"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			seal, err := guardEditionTransitionSeal(guardManifestEdge{From: source, To: target}, 0, empty, terminal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := insertGuardReceipt(ctx, tx, dia, seal); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			got, err := guardAdmissionLatestRecordedEdition(ctx, db, dia)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Found || got.RolloutID != targetRollout.RolloutID {
				t.Fatalf("sealed Business successor lost to hash ordering: got %s, want %s", got.RolloutID, targetRollout.RolloutID)
			}
		})
	}
	if len(seen) != 2 {
		t.Fatal("did not exercise both rollout hash orders")
	}
}

func registerCommercialGuardFixture(reg store.ExtensionRegistry) error {
	if err := registerWidget(reg); err != nil {
		return err
	}
	for _, descriptor := range []model.EntityDescriptor{
		{Kind: "ldap.revision", Table: commercialGuardFixtureTables[0], AppendOnly: true},
		{Kind: "session_cockpit.console_audit_index", Table: commercialGuardFixtureTables[1], AppendOnly: true},
		{Kind: "session_cockpit.runtime_session_original", Table: commercialGuardFixtureTables[2], AppendOnly: true},
	} {
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	return nil
}

func TestCommercialGuardUpgradeOpenPreservesData(t *testing.T) {
	ctx := context.Background()
	community, path := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)
	tenant := provisionTenant(t, community, "edition-swap")
	var id model.ID
	if err := community.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(widgetDescriptor.Kind)
		if err != nil {
			return err
		}
		record, err := repo.Create(ctx, model.Record{"label": "kept through swap", "count": int64(42)})
		id = model.ID(record.String("id"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := community.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		business, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: path}, registerCommercialGuardFixture)
		if err != nil {
			t.Fatalf("Business open %d: %.500s", reopen, err)
		}
		if err := business.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(widgetDescriptor.Kind)
			if err != nil {
				return err
			}
			record, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			if record.String("label") != "kept through swap" {
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
		if err := business.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommercialGuardUpgradePostgres(t *testing.T) {
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
			community, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatal(err)
			}
			tenant := provisionTenant(t, community, "commercial-pg")
			if err := community.Close(); err != nil {
				t.Fatal(err)
			}
			for reopen := 0; reopen < 2; reopen++ {
				business, err := Open(ctx, cfg, registerCommercialGuardFixture)
				if err != nil {
					t.Fatalf("Business PostgreSQL open %d: %.500s", reopen, err)
				}
				if err := business.View(ctx, tenant, func(sc store.Scope) error {
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
				if err := business.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCommercialGuardLegacyBusinessHistory(t *testing.T) {
	community := coreOnlyAccessEvidenceManifest(t)
	tables := append([]string(nil), commercialGuardFixtureTables...)
	for _, spec := range community.Specs {
		tables = append(tables, spec.Key.Relation)
	}
	// Historical Business bootstraps used the SAME epoch with a larger base
	// census. Adding the Community edge must keep those canonical bytes intact.
	legacy, err := buildGuardManifestAtEpoch(tables, community.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	current, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	if current.CodeSHA256 != legacy.CodeSHA256 || current.CodeEpoch != legacy.CodeEpoch {
		t.Fatal("legacy Business manifest was changed")
	}
	variants, err := guardBootstrapReceiptVariants(legacy)
	if err != nil {
		t.Fatal(err)
	}
	db, dia := guardEmptyEditionSQLiteDB(t)
	for _, variant := range variants {
		if variant.State == guardEditionReceiptsCurrentV9Completed && variant.Path == "5" {
			seedGuardEditionVariant(t, db, dia, legacy, variant.Path, variant.Receipts)
			break
		}
	}
	before := accessEvidenceReadDurableCensus(t, db)
	if _, err := runGuardAdmissionUnits(context.Background(), db, dia, tables, "", false, nil); err != nil {
		t.Fatalf("legacy Business reopen: %.500s", err)
	}
	accessEvidenceAssertDurableCensusUnchanged(t, "legacy Business reopen", before, accessEvidenceReadDurableCensus(t, db))
}

func TestCommercialGuardUpgradePublishedAndFullCensus(t *testing.T) {
	published := loadPublicReleaseCensus(t)
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "all modules"}[full], func(t *testing.T) {
			tables := append(append([]string(nil), published.ProductCensus...), guardAccessEvidenceTables[:]...)
			path, state := guardEditionPath("4>7"), guardEditionReceiptsSealed
			if full {
				tables = append(tables, "evals_comparison", "gitpublish_observation", "skills_revision")
				path, state = "28", guardEditionReceiptsCurrentV9Completed
			}
			source, err := buildGuardManifest(tables)
			if err != nil {
				t.Fatal(err)
			}
			variants, err := guardBootstrapReceiptVariants(source)
			if err != nil {
				t.Fatal(err)
			}
			db, dia := guardEmptyEditionSQLiteDB(t)
			found := false
			for _, variant := range variants {
				if variant.State == state && variant.Path == path {
					seedGuardEditionVariant(t, db, dia, source, path, variant.Receipts)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing published source variant")
			}
			targetTables := append(append([]string(nil), tables...), commercialGuardFixtureTables...)
			if _, err := runGuardAdmissionUnits(context.Background(), db, dia, targetTables, "", false, nil); err != nil {
				t.Fatalf("upgrade real module census: %.500s", err)
			}
		})
	}
}

func TestCommercialGuardUpgradeKeepsExactHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state guardEditionReceiptState
		extra []string
		pass  bool
	}{
		{"completed Community", guardEditionReceiptsCurrentV9Completed, nil, true},
		{"incomplete v9", guardEditionReceiptsCurrentCompleted, nil, false},
		{"unknown source relation", guardEditionReceiptsCurrentV9Completed, []string{"unknown_evidence"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceTables := append([]string{"audit_events", "set_seen_jtis", guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}, guardAccessEvidenceTables[:]...)
			source, err := buildGuardManifest(append(append([]string(nil), sourceTables...), tc.extra...))
			if err != nil {
				t.Fatal(err)
			}
			variants, err := guardBootstrapReceiptVariants(source)
			if err != nil {
				t.Fatal(err)
			}
			db, dia := guardEmptyEditionSQLiteDB(t)
			found := false
			for _, variant := range variants {
				if variant.State == tc.state && variant.Path == "5" {
					seedGuardEditionVariant(t, db, dia, source, variant.Path, variant.Receipts)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("missing source variant")
			}
			before := accessEvidenceReadDurableCensus(t, db)
			targetTables := append(append([]string(nil), sourceTables...), commercialGuardFixtureTables...)
			ctx := context.Background()
			_, err = runGuardAdmissionUnits(ctx, db, dia, targetTables, "", false, nil)
			if !tc.pass {
				if !errors.Is(err, ErrGuardManifestNoEdge) && !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
					t.Fatalf("unsafe history admitted: %.500s", err)
				}
				accessEvidenceAssertDurableCensusUnchanged(t, "refused edition swap", before, accessEvidenceReadDurableCensus(t, db))
				return
			}
			if err != nil {
				t.Fatalf("Community to Business swap: %.500s", err)
			}
			target, err := buildGuardManifest(targetTables)
			if err != nil {
				t.Fatal(err)
			}
			if target.CodeEpoch != source.CodeEpoch {
				t.Fatal("swap changed the published guard epoch")
			}
			history, err := guardAdmissionVerifyHistory(ctx, db, dia, target)
			if err != nil {
				t.Fatal(err)
			}
			if history.Kind != guardEditionHistoryTransitioned || !history.CompletedV9 {
				t.Fatalf("no complete transition witness: %+v", history)
			}
			after := accessEvidenceReadDurableCensus(t, db)
			if _, err := runGuardAdmissionUnits(ctx, db, dia, targetTables, "", false, nil); err != nil {
				t.Fatal(err)
			}
			accessEvidenceAssertDurableCensusUnchanged(t, "Business reopen", after, accessEvidenceReadDurableCensus(t, db))
			if _, err := runGuardAdmissionUnits(ctx, db, dia, sourceTables, "", false, nil); err == nil {
				t.Fatal("reverse swap discarded the Business guards")
			}
			accessEvidenceAssertDurableCensusUnchanged(t, "refused reverse swap", after, accessEvidenceReadDurableCensus(t, db))
		})
	}
}

// The Business runtime must preserve joint receipt/inventory proof rather than
// accepting either projection independently after the edition graph expands.
func TestCommercialGuardRefusesReceiptInventoryCrossProducts(t *testing.T) {
	tables := append([]string{"widget", guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}, guardAccessEvidenceTables[:]...)
	community, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	business, err := buildGuardManifest(append(append([]string(nil), tables...), commercialGuardFixtureTables...))
	if err != nil {
		t.Fatal(err)
	}
	variants, err := guardBootstrapReceiptVariants(business)
	if err != nil {
		t.Fatal(err)
	}
	var receipts []guardReceipt
	for _, variant := range variants {
		if variant.State == guardEditionReceiptsCurrentV9Completed && variant.Path == "5" {
			receipts = variant.Receipts
			break
		}
	}
	if len(receipts) == 0 {
		t.Fatal("missing Business receipt fixture")
	}
	db, dia := guardEmptyEditionSQLiteDB(t)
	seedGuardEditionVariant(t, db, dia, community, "5", receipts)
	before := accessEvidenceReadDurableCensus(t, db)
	_, err = guardAdmissionVerifyHistory(context.Background(), db, dia, business)
	if !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("cross-product = %.500s; want ErrGuardManifestNoEdge", err)
	}
	accessEvidenceAssertDurableCensusUnchanged(t, "Business cross-product refusal", before, accessEvidenceReadDurableCensus(t, db))
}

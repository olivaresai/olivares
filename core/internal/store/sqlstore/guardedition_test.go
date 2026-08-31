// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func coreOnlyCurrentGuardManifest(t *testing.T) guardManifest {
	t.Helper()
	m, err := buildGuardManifest([]string{
		"audit_events",
		"set_seen_jtis",
		guardEpoch2UserTombstoneTable,
		guardEpoch2DirectoryTombstoneTable,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestK2GoldenCoreOnlyManifestAndBootstrapReceipts(t *testing.T) {
	const (
		sourceCommit = "727ca531a6a1cb13ccbf3ee041edf64393ea9fd3"
		sourceTree   = "1c74200878a8c4531434acb602cf615af570d22b"
		codeSHA      = "aadc7e40c8dcf29b681a3c34c98eec2299ed4e2fcd6e71b07fc587f8f70b26f5"
		retainedSHA  = "abb9dfe58751c145c97b811019c41a6bdd2ec10f1628607cb8a5fed1c4413f34"
		rolloutID    = "7d2fddc5ed461d3bfc55e10c1c2fdcb128b4100945c12d3535fb55a71be5a408"
	)
	current := coreOnlyCurrentGuardManifest(t)
	edge, ok, err := guardManifestEditionEdge(current)
	if err != nil || !ok {
		t.Fatalf("derive K2 golden edge: ok=%v err=%v", ok, err)
	}
	old := edge.From
	if old.Format != 1 || old.CodeEpoch != 1 || hexDigest(old.CodeSHA256) != codeSHA {
		t.Fatalf("K2 manifest tuple = format:%d epoch:%d code:%s; want 1/1/%s",
			old.Format, old.CodeEpoch, hexDigest(old.CodeSHA256), codeSHA)
	}
	specs := []struct {
		key        guardKey
		definition string
		spec       string
	}{
		{guardKey{Schema: "public", Relation: "audit_events", Trigger: "audit_events_immutable"},
			"7724590b8770de13f97c90589bd72c5af134de1eb256d9b93cc634dd65034d3f",
			"a12707e3b61ab152a8617bd5ed22aa92640d027fac536615d9b8c4e50340ae1d"},
		{guardKey{Schema: "public", Relation: "set_seen_jtis", Trigger: "set_seen_jtis_immutable"},
			"82522b6108e2241d361ffbfd78ea8c1ddd28c077e1b90530c0290c2f26c24c18",
			"ede4bad24f54e5f972e2bcc827b04bccfdde6ea383eed8a7800af7bf6687486d"},
	}
	if len(old.Specs) != len(specs) {
		t.Fatalf("K2 spec count = %d, want %d", len(old.Specs), len(specs))
	}
	for i, want := range specs {
		got := old.Specs[i]
		if got.Key != want.key || got.Producer != guardProducerEngine ||
			got.DesiredEnableState != guardStateAlways ||
			strings.Join(got.LegacyAllowedStates, "") != guardStateOrigin+guardStateAlways ||
			hexDigest(got.DefinitionSHA256) != want.definition || hexDigest(got.SpecSHA256) != want.spec {
			t.Fatalf("K2 spec %d differs from %s/%s golden: %+v", i, sourceCommit, sourceTree, got)
		}
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rollout, err := guardBootstrapRollout(old, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	if hexDigest(empty) != retainedSHA || rollout.RolloutID != rolloutID {
		t.Fatalf("K2 retained/rollout = %s/%s, want %s/%s",
			hexDigest(empty), rollout.RolloutID, retainedSHA, rolloutID)
	}
	receipts, err := expectedGuardBootstrapReceipts(old)
	if err != nil {
		t.Fatal(err)
	}
	wantReceipts := []struct {
		key, unit, definition, spec, prestate, receipt string
	}{
		{"olivares_guard_gate_events", "a119c54eac2424024dbd727ab0bc10a46ca7b00916f1e4b3d17e50a999b2be85", "9079127718870a3af4bdc79d7563486ac03793d3806f8cea1d9438007e9dd942", "278f3ed25fac2148015a4cfe0a5a64737fe343045e790ae1fb8af7a071988f56", "4a9056db81570a4dc74fb5a31a8df374f35fc3befcc8441d9ff4154f3a24a9dc", "658df452758afa76442ed84dad069310827f0eaf8cfb5e94cb843600f1910a23"},
		{"olivares_guard_inventory_events", "99a00d057abab42910d8ce2497b84951c7c3bff51659271922070a5843928d6a", "0f460510b3c7ad19b9acb19f9a58e9c5cb8e661d7178e003679ea5d49aed7cc0", "b15abb9a9a00ab7f996d7943d77f9ef702a0e3d8795550976df83b375f874b1a", "1609771ab6579d3672af7e1b945eeef728b70456cbfd8e6fb9904e34ab3190f0", "0b488903e6c73c8df6d7e498f97076016d9b8e85da2f445cbac7fafdb2e8fb38"},
		{"olivares_guard_receipts", "278651fe2f2ac0203abc1020b3186ec9167d3e4ec0664cf52ccca9fc0ff49aa5", "ef2407f49b20f28392f502a52973c36b6083ecf1377a0394b8bb3bf20318417f", "9b559c1f9df82995caf12ef88ee4303863a4f16582facaedf8be74da9fd4e67e", "56273232be979e032045e953c43e3a0e19a47e024cee981034d0f34241d9a50d", "e233720105fa0603b26cb70709ecc57cded15b80148e7a0efdb1081a344ec3a5"},
	}
	if len(receipts) != len(wantReceipts) {
		t.Fatalf("K2 bootstrap receipt count = %d, want %d", len(receipts), len(wantReceipts))
	}
	for i, want := range wantReceipts {
		got := receipts[i]
		if got.RolloutID != rolloutID || got.UnitID != want.unit ||
			got.Kind != guardReceiptKindBootstrap || got.Intent != guardIntentBootstrap ||
			got.Key != (guardKey{Schema: "public", Relation: want.key, Trigger: want.key + "_immutable"}) ||
			got.Epoch != 1 || got.Format != 1 || hexDigest(got.CodeSHA256) != codeSHA ||
			got.RetainedRevision != 0 || hexDigest(got.RetainedSHA256) != retainedSHA ||
			hexDigest(got.DefinitionSHA256) != want.definition || hexDigest(got.SpecSHA256) != want.spec ||
			hexDigest(got.PrestateSHA256) != want.prestate || got.FromEnableState.Valid ||
			got.ToEnableState != guardStateAlways || got.PredecessorReceiptID.Valid ||
			got.AttemptID != guardBootstrapAttemptID || hexDigest(got.ReceiptID) != want.receipt {
			t.Fatalf("K2 bootstrap receipt %d differs from %s/%s golden: %+v", i, sourceCommit, sourceTree, got)
		}
	}
	t.Logf("K2_GOLDEN|commit=%s|tree=%s|rollout=%s", sourceCommit, sourceTree, rolloutID)
}

// TestCoreV7SealGoldenBodies freezes the new durable ABI independently of the
// database fixtures. These values were calculated with an independent encoder
// from the core-only K3 manifest; deriving the expectations through guardV7Seal
// would let a domain, phase, predecessor or attempt-id change drift unnoticed.
func TestCoreV7SealGoldenBodies(t *testing.T) {
	const (
		codeSHA                   = "ea0c8a4ad11cf3d7c83850d836a79b570a8656ca5644b2a3619301fef3d83f32"
		rolloutID                 = "502f3393910312b698a90681da6c37765fb4e7052e395ed93fdc7a646b0ec83c"
		gateBootstrapReceiptID    = "bcd98262f7f9ea31961a206f4cc19f6dec82030414414192d2f7d0ce2f48338a"
		startUnitID               = "980c7d7be10b8d41ac3b2722d81190e3e0a74da3d6623b3359d2d1a8a1bc1ec0"
		completionUnitID          = "9edfbdd73f6b74f6a44b59c6db89893c196743255e6cb4d3284974624c493023"
		prestateSHA               = "1c3d886d643cf705c7c00b10e9da573b953685831c3e92f5b78fa84c2c47bec2"
		startReceiptID            = "bc4324ac150cfeb31906d39e381fd401ac66f94facf886f90965f7fd5d1bb08b"
		freshCompletionReceiptID  = "8ab79903d995f9268c456b116ec6d148be18d7c067f1aa33e7c4ee06fbe00bc7"
		directCompletionReceiptID = "f49e5161cade76b2028124728f7f6a91260bda18e4b22db9e2d2b50632e41179"
	)
	current := coreOnlyCurrentGuardManifest(t)
	if got := hexDigest(current.CodeSHA256); got != codeSHA {
		t.Fatalf("core-only K3 code SHA = %s, want %s", got, codeSHA)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rollout, err := guardBootstrapRollout(current, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	if rollout.RolloutID != rolloutID {
		t.Fatalf("core-only K3 rollout = %s, want %s", rollout.RolloutID, rolloutID)
	}
	bootstrap, err := expectedGuardBootstrapReceipts(current)
	if err != nil {
		t.Fatal(err)
	}
	var gateReceipt guardReceipt
	for _, receipt := range bootstrap {
		if receipt.Key.Relation == guardGateEventsTable {
			gateReceipt = receipt
			break
		}
	}
	if got := hexDigest(gateReceipt.ReceiptID); got != gateBootstrapReceiptID {
		t.Fatalf("core-only K3 gate bootstrap receipt = %s, want %s", got, gateBootstrapReceiptID)
	}

	cases := []struct {
		name         string
		phase        guardV7SealPhase
		followsStart bool
		unitID       string
		receiptID    string
		predecessor  string
		attemptID    string
	}{
		{"direct start", guardV7SealStart, false, startUnitID, startReceiptID,
			gateBootstrapReceiptID, guardDirectV7StartAttemptID},
		{"fresh completion", guardV7SealCompletion, false, completionUnitID,
			freshCompletionReceiptID, gateBootstrapReceiptID, guardV7CompletionAttemptID},
		{"direct completion", guardV7SealCompletion, true, completionUnitID,
			directCompletionReceiptID, startReceiptID, guardV7CompletionAttemptID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seal, err := guardV7Seal(current, tc.phase, tc.followsStart)
			if err != nil {
				t.Fatal(err)
			}
			if seal.RolloutID != rolloutID || seal.UnitID != tc.unitID ||
				hexDigest(seal.ReceiptID) != tc.receiptID ||
				hexDigest(seal.PrestateSHA256) != prestateSHA ||
				seal.PredecessorReceiptID.String() != tc.predecessor ||
				seal.AttemptID != tc.attemptID {
				t.Fatalf("%s seal differs from golden: rollout=%s unit=%s receipt=%s prestate=%s predecessor=%s attempt=%s",
					tc.name, seal.RolloutID, seal.UnitID, hexDigest(seal.ReceiptID),
					hexDigest(seal.PrestateSHA256), seal.PredecessorReceiptID, seal.AttemptID)
			}
			if seal.Kind != guardReceiptKindBootstrap || seal.Intent != guardIntentBootstrap ||
				seal.Key != gateReceipt.Key || seal.Epoch != current.CodeEpoch ||
				seal.Format != current.Format || hexDigest(seal.CodeSHA256) != codeSHA ||
				seal.RetainedRevision != 0 || seal.FromEnableState.String() != guardStateAlways ||
				seal.ToEnableState != guardStateAlways {
				t.Fatalf("%s seal has non-canonical fixed fields: %+v", tc.name, seal)
			}
		})
	}
}

func TestK2GoldenCoreOnlySQLiteUpgradesAndReopens(t *testing.T) {
	const (
		artifactSHA = "0f981175b566a5e00eac7da38ac998a592c225a64a8052921ce777eea8f01644"
		rawSHA      = "5847ce1f65d49b382ac9f89a36b66f9c4dc301ff2f405737512f1d03f1b48d90"
	)
	compressed, err := os.ReadFile("testdata/k2-727ca531/core-only.sqlite.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(compressed)); got != artifactSHA {
		t.Fatalf("K2 artifact SHA = %s, want %s", got, artifactSHA)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if closeErr := zr.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != rawSHA {
		t.Fatalf("K2 raw DB SHA = %s, want %s", got, rawSHA)
	}
	path := filepath.Join(t.TempDir(), "k2-core-only.sqlite")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := store.Config{Engine: store.EngineSQLite, DSN: path, MaxConns: 1}
	for attempt := 1; attempt <= 2; attempt++ {
		st, err := Open(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("K2 golden K3 Open attempt %d: %v", attempt, err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("K2 golden K3 Close attempt %d: %v", attempt, err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	dia, _ := dialect.New(store.EngineSQLite)
	history, err := verifyGuardEditionHistory(context.Background(), db, dia, coreOnlyCurrentGuardManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if history.Kind != guardEditionHistoryTransitioned {
		t.Fatalf("K2 golden upgraded history = %s, want transitioned", history.Kind)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable+" WHERE receipt_kind='bootstrap'"); got != 4 {
		t.Fatalf("K2 golden upgraded bootstrap receipts = %d, want predecessor three plus transition seal", got)
	}
}

func coreV5Descriptors() []model.EntityDescriptor {
	directory := map[string]bool{}
	for _, table := range coreDirectoryRelationNames {
		directory[table] = true
	}
	all := coreDescriptors()
	legacy := make([]model.EntityDescriptor, 0, len(all)-len(directory))
	for _, desc := range all {
		if !directory[desc.Table] {
			legacy = append(legacy, desc)
		}
	}
	return legacy
}

func seedCoreV5(t *testing.T, db *sql.DB, dia dialect.Dialect, legacyTracker bool) {
	t.Helper()
	migrations := buildCoreMigrations(dia, coreV5Descriptors(), nil, nil)
	if err := migrate.Apply(context.Background(), db, dia, coreTrackingTable, migrations[:5]); err != nil {
		t.Fatalf("seed core v5: %v", err)
	}
	if legacyTracker {
		for _, column := range []string{"reverted_at", "phase"} {
			if _, err := db.ExecContext(context.Background(),
				"ALTER TABLE "+coreTrackingRelation(dia)+" DROP COLUMN "+column); err != nil {
				t.Fatalf("restore legacy tracker without %s: %v", column, err)
			}
		}
	}
	if err := preflightCoreMigrationVersion(context.Background(), db, dia, coreSupportedMigrationVersion); err != nil {
		t.Fatalf("seeded v5 tracker is not a supported historical shape: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := inspectCoreDirectoryInitialDisposition(context.Background(), tx, dia)
	_ = tx.Rollback()
	if err != nil || disposition != coreDirectoryInitiallyAbsent {
		t.Fatalf("seeded v5 directory state = %s, %v; want absent", disposition, err)
	}
}

func assertDirectV7Completed(t *testing.T, db *sql.DB, dia dialect.Dialect) guardManifest {
	t.Helper()
	current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatal(err)
	}
	history, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
	if err != nil {
		t.Fatalf("verify direct v7 history: %v", err)
	}
	if history.Kind != guardEditionHistoryDirectCompleted {
		t.Fatalf("direct v7 history = %s, want %s", history.Kind, guardEditionHistoryDirectCompleted)
	}
	state, receipts, err := verifyGuardBootstrapReceiptHistory(context.Background(), db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if state != guardEditionReceiptsDirectCompleted || len(receipts) != 5 {
		t.Fatalf("direct v7 bootstrap state=%s receipts=%d, want %s/5",
			state, len(receipts), guardEditionReceiptsDirectCompleted)
	}
	for _, expected := range []guardReceipt{
		func() guardReceipt {
			r, e := guardV7Seal(current, guardV7SealStart, false)
			if e != nil {
				t.Fatal(e)
			}
			return r
		}(),
		func() guardReceipt {
			r, e := guardV7Seal(current, guardV7SealCompletion, true)
			if e != nil {
				t.Fatal(e)
			}
			return r
		}(),
	} {
		found := false
		for _, actual := range receipts {
			if actual.UnitID == expected.UnitID {
				found = true
				if diff := receiptDifference(actual, expected); diff != "" {
					t.Fatalf("direct v7 seal %s differs: %s", expected.AttemptID, diff)
				}
			}
		}
		if !found {
			t.Fatalf("direct v7 seal %s is absent", expected.AttemptID)
		}
	}
	return current
}

func directV7StoreFixture(
	t *testing.T,
	engine store.Engine,
) (store.Config, *sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(engine)
	if !ok {
		t.Fatalf("no dialect for %s", engine)
	}
	var cfg store.Config
	var driver string
	switch engine {
	case store.EngineSQLite:
		cfg = store.Config{Engine: engine, DSN: t.TempDir() + "/direct-v7.db", MaxConns: 1}
		driver = "sqlite"
	case store.EnginePostgres:
		pg := isolatedPGSplit(t)
		cfg = store.Config{
			Engine: engine, DSN: pg.App, OwnerDSN: pg.Owner, MaxConns: 4,
		}
		// Historical schema and ledger rows are seeded by the same owner pool
		// production Open uses for migrations. The app pool remains deliberately
		// unable to create or alter the v6/v7 objects.
		cfgDSN := pg.Owner
		driver = "pgx"
		db, err := sql.Open(driver, cfgDSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return cfg, db, dia
	default:
		t.Fatalf("unsupported direct-v7 test engine %s", engine)
	}
	db, err := sql.Open(driver, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if engine == store.EngineSQLite {
		db.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { _ = db.Close() })
	return cfg, db, dia
}

// TestPostgresEnsureTrackingRollsBackBothLegacyALTERs is the real-server half
// of the migrate package's SQLite fault test. The event trigger matches the
// exact schema-qualified second ALTER and first proves that phase is already
// visible. If ensureTracking reorders, omits or separates the two statements,
// the fixture or the rollback assertion fails and the exact legacy preflight
// cannot silently become unretryable.
func TestPostgresEnsureTrackingRollsBackBothLegacyALTERs(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer super.Close() //nolint:errcheck
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	const (
		tracking = "sm_legacy_atomic_pg"
		function = "public.fail_second_tracking_alter"
		trigger  = "fail_second_tracking_alter"
	)
	targetRelation := quoteIdent(dialect.EngineSchema) + "." + quoteIdent(tracking)
	targetDDL := "ALTER TABLE " + targetRelation + " ADD COLUMN reverted_at TEXT"
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+tracking+` (
version INTEGER PRIMARY KEY,
name TEXT NOT NULL,
applied_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	cleanupFault := func() {
		_, _ = super.ExecContext(context.Background(), "DROP EVENT TRIGGER IF EXISTS "+trigger)
		_, _ = super.ExecContext(context.Background(), "DROP FUNCTION IF EXISTS "+function+"()")
	}
	t.Cleanup(cleanupFault)
	if _, err := super.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION `+function+`() RETURNS event_trigger
LANGUAGE plpgsql
AS $body$
BEGIN
  IF pg_catalog.current_query() OPERATOR(pg_catalog.=)
     %s THEN
    IF NOT EXISTS (
      SELECT 1
      FROM pg_catalog.pg_attribute
      WHERE attrelid OPERATOR(pg_catalog.=) %s::pg_catalog.regclass
        AND attname OPERATOR(pg_catalog.=) 'phase'
        AND attnum OPERATOR(pg_catalog.>) 0
        AND NOT attisdropped
    ) THEN
      RAISE EXCEPTION 'tracking fault fixture did not observe phase before reverted_at';
    END IF;
    RAISE EXCEPTION 'injected second tracking ALTER failure';
  END IF;
END
$body$`, quoteLiteral(targetDDL), quoteLiteral(targetRelation))); err != nil {
		t.Fatal(err)
	}
	if _, err := super.ExecContext(ctx, "CREATE EVENT TRIGGER "+trigger+
		" ON ddl_command_start WHEN TAG IN ('ALTER TABLE') EXECUTE FUNCTION "+function+"()"); err != nil {
		t.Fatal(err)
	}

	err = migrate.Apply(ctx, db, dia, tracking, nil)
	if err == nil || !strings.Contains(err.Error(), "injected second tracking ALTER failure") {
		t.Fatalf("faulted tracking reconciliation = %v, want injected second-ALTER failure", err)
	}
	cols, err := dia.TableColumns(ctx, db, tracking)
	if err != nil {
		t.Fatal(err)
	}
	if cols["phase"] || cols["reverted_at"] {
		t.Fatalf("failed PostgreSQL reconciliation left additive columns: %+v", cols)
	}

	cleanupFault()
	if err := migrate.Apply(ctx, db, dia, tracking, nil); err != nil {
		t.Fatalf("retry PostgreSQL tracking reconciliation: %v", err)
	}
	cols, err = dia.TableColumns(ctx, db, tracking)
	if err != nil {
		t.Fatal(err)
	}
	if !cols["phase"] || !cols["reverted_at"] {
		t.Fatalf("PostgreSQL retry did not converge both tracking columns: %+v", cols)
	}
}

func TestCoreV7DirectUpgradeFromV5BothTrackingShapes(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		for _, legacyTracker := range []bool{false, true} {
			name := fmt.Sprintf("%s/tracker_5_columns", engine)
			if legacyTracker {
				name = fmt.Sprintf("%s/tracker_3_columns", engine)
			}
			t.Run(name, func(t *testing.T) {
				cfg, db, dia := directV7StoreFixture(t, engine)
				seedCoreV5(t, db, dia, legacyTracker)

				st, err := Open(context.Background(), cfg, registerWidget)
				if err != nil {
					t.Fatalf("direct v5 -> v7 Open: %v", err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				assertDirectV7Completed(t, db, dia)

				reopened, err := Open(context.Background(), cfg, registerWidget)
				if err != nil {
					t.Fatalf("reopen completed direct v7: %v", err)
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}

				// Removing only tracking cannot turn completed v7 evidence back
				// into a fresh present/current start. The failed Open must not
				// restore the row or append any guard history.
				receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
				inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
				if _, err := db.ExecContext(context.Background(), dia.Rebind(
					"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
					coreDirectoryMigrationVersion); err != nil {
					t.Fatal(err)
				}
				if got, err := Open(context.Background(), cfg, registerWidget); err == nil {
					_ = got.Close()
					t.Fatal("Open re-tracked a completed v7 history after its row was deleted")
				}
				if got := countRows(t, db, dia.Rebind("SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreDirectoryMigrationVersion); got != 0 {
					t.Fatalf("refused Open restored %d v7 tracking rows", got)
				}
				if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
					t.Fatalf("refused Open changed receipts %d -> %d", receiptsBefore, got)
				}
				if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
					t.Fatalf("refused Open changed inventory %d -> %d", inventoryBefore, got)
				}
			})
		}
	}
}

func TestCoreV7DirectCompletedHistoryCannotBeReplayedAfterTableDeletion(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			cfg, db, dia := directV7StoreFixture(t, engine)
			seedCoreV5(t, db, dia, true)
			st, err := Open(context.Background(), cfg, registerWidget)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			assertDirectV7Completed(t, db, dia)

			if _, err := db.ExecContext(context.Background(), dia.Rebind(
				"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreDirectoryMigrationVersion); err != nil {
				t.Fatal(err)
			}
			prefix := "main."
			if engine == store.EnginePostgres {
				prefix = dialect.EngineSchema + "."
			}
			for _, table := range coreDirectoryRelationNames {
				if _, err := db.ExecContext(context.Background(), "DROP TABLE "+prefix+table); err != nil {
					t.Fatalf("drop %s: %v", table, err)
				}
			}
			if _, err := db.ExecContext(context.Background(),
				"DROP TABLE "+prefix+dialect.DirectoryWriterControlTable); err != nil {
				t.Fatal(err)
			}
			if engine == store.EngineSQLite {
				if _, err := db.ExecContext(context.Background(),
					"DROP TABLE main."+dialect.DirectoryWriterMarkerTable); err != nil {
					t.Fatal(err)
				}
			}
			receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
			if got, err := Open(context.Background(), cfg, registerWidget); err == nil {
				_ = got.Close()
				t.Fatal("Open replayed a completed direct-v7 history after deleting its tables")
			} else if !strings.Contains(err.Error(), string(guardEditionHistoryDirectCompleted)) {
				t.Fatalf("Open refused at the wrong boundary: %v", err)
			}
			assertCoreDirectoryExistence(t, db, dia, map[string]bool{
				"core_directory_epoch": false, "core_directory_tombstone": false, "core_user_tombstone": false,
			})
			assertDirectoryWriterRawExistence(t, db, dia, false)
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
				t.Fatalf("refused replay changed receipts %d -> %d", receiptsBefore, got)
			}
		})
	}
}

func TestCoreV7DirectUpgradeCrashBetweenV6AndV7RetriesAtomically(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			cfg, db, dia := directV7StoreFixture(t, engine)
			seedCoreV5(t, db, dia, true)
			current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
			if err != nil {
				t.Fatal(err)
			}
			migrations := buildCoreMigrations(
				dia,
				coreDescriptors(),
				guardBootstrapExec(dia, current),
				guardEditionTwoMigrationExec(dia, current),
			)
			// Commit v6 alone. This is the real crash boundary: v6 tracking,
			// current bootstrap and start are durable; v7 has not created a
			// relation, control or completion witness yet.
			if err := migrate.Apply(context.Background(), db, dia, coreTrackingTable, migrations[:6]); err != nil {
				t.Fatalf("apply direct-upgrade v6: %v", err)
			}
			history, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
			if err != nil {
				t.Fatal(err)
			}
			if history.Kind != guardEditionHistoryDirectStarted {
				t.Fatalf("post-v6 history = %s, want %s", history.Kind, guardEditionHistoryDirectStarted)
			}
			assertCoreDirectoryExistence(t, db, dia, map[string]bool{
				"core_directory_epoch": false, "core_directory_tombstone": false, "core_user_tombstone": false,
			})
			assertDirectoryWriterRawExistence(t, db, dia, false)

			boom := errors.New("injected failure after the v7 completion seal")
			hook := guardEditionTwoMigrationExec(dia, current)
			failing := coreDirectoryMigration(dia, coreDescriptors(),
				func(ctx context.Context, tx *sql.Tx, initial coreDirectoryInitialDisposition) error {
					if err := hook(ctx, tx, initial); err != nil {
						return err
					}
					inside, err := verifyGuardEditionHistory(ctx, tx, dia, current)
					if err != nil {
						return err
					}
					if inside.Kind != guardEditionHistoryDirectCompleted {
						return fmt.Errorf("history inside failing v7 = %s", inside.Kind)
					}
					return boom
				})
			if err := migrate.Apply(context.Background(), db, dia, coreTrackingTable,
				[]migrate.Migration{failing}); !errors.Is(err, boom) {
				t.Fatalf("failed v7 = %v, want injected failure", err)
			}
			afterFailure, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
			if err != nil {
				t.Fatal(err)
			}
			if afterFailure.Kind != guardEditionHistoryDirectStarted {
				t.Fatalf("rolled-back v7 history = %s, want start", afterFailure.Kind)
			}
			assertCoreDirectoryExistence(t, db, dia, map[string]bool{
				"core_directory_epoch": false, "core_directory_tombstone": false, "core_user_tombstone": false,
			})
			assertDirectoryWriterRawExistence(t, db, dia, false)
			if got := countRows(t, db, dia.Rebind("SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), coreDirectoryMigrationVersion); got != 0 {
				t.Fatalf("failed v7 left %d tracking rows", got)
			}

			if err := migrate.Apply(context.Background(), db, dia, coreTrackingTable,
				[]migrate.Migration{migrations[6]}); err != nil {
				t.Fatalf("retry direct v7: %v", err)
			}
			assertDirectV7Completed(t, db, dia)
			st, err := Open(context.Background(), cfg, registerWidget)
			if err != nil {
				t.Fatalf("Open after direct-v7 retry: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func guardEditionFixture(t *testing.T) (guardManifest, guardManifestEdge) {
	t.Helper()
	current, err := buildGuardManifest([]string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	})
	if err != nil {
		t.Fatal(err)
	}
	edge, ok, err := guardManifestEditionEdge(current)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the current manifest has no compiled predecessor edge")
	}
	return current, edge
}

func guardEditionThreeFixture(t *testing.T) (guardManifest, guardManifestEdge, guardManifestEdge) {
	t.Helper()
	tables := []string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	}
	tables = append(tables, guardEpoch3CommunicationTables[:]...)
	current, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	edge23, ok, err := guardManifestEditionEdge(current)
	if err != nil || !ok {
		t.Fatalf("derive epoch 2->3 edge: ok=%v err=%v", ok, err)
	}
	edge12, ok, err := guardManifestEditionEdge(edge23.From)
	if err != nil || !ok {
		t.Fatalf("derive epoch 1->2 edge: ok=%v err=%v", ok, err)
	}
	return current, edge23, edge12
}

func guardEditionFourFixture(
	t *testing.T,
) (guardManifest, guardManifestEdge, guardManifestEdge, guardManifestEdge) {
	t.Helper()
	tables := []string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	}
	tables = append(tables, guardEpoch3CommunicationTables[:]...)
	tables = append(tables, guardEpoch4ProtocolTables[:]...)
	current, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatal(err)
	}
	edge34, ok, err := guardManifestEditionEdge(current)
	if err != nil || !ok {
		t.Fatalf("derive epoch 3->4 edge: ok=%v err=%v", ok, err)
	}
	edge23, ok, err := guardManifestEditionEdge(edge34.From)
	if err != nil || !ok {
		t.Fatalf("derive epoch 2->3 edge: ok=%v err=%v", ok, err)
	}
	edge12, ok, err := guardManifestEditionEdge(edge23.From)
	if err != nil || !ok {
		t.Fatalf("derive epoch 1->2 edge: ok=%v err=%v", ok, err)
	}
	return current, edge34, edge23, edge12
}

func appendGuardV7Witness(t *testing.T, db *sql.DB, dia dialect.Dialect, manifest guardManifest, direct bool) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if direct {
		start, err := guardV7Seal(manifest, guardV7SealStart, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertGuardReceipt(context.Background(), tx, dia, start); err != nil {
			t.Fatal(err)
		}
	}
	completion, err := guardV7Seal(manifest, guardV7SealCompletion, direct)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertGuardReceipt(context.Background(), tx, dia, completion); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func registerGuardEpochThreeFixture(reg store.ExtensionRegistry) error {
	for i, table := range guardEpoch3CommunicationTables {
		descriptor := model.EntityDescriptor{
			Kind:       model.Kind(strings.Replace(table, "sessions_", "sessions.", 1)),
			Table:      table,
			AppendOnly: true,
			Fields: []model.FieldSpec{
				{Name: fmt.Sprintf("fixture_value_%d", i), Kind: model.KindText, Nullable: true},
			},
		}
		if err := reg.Register(descriptor); err != nil {
			return err
		}
	}
	return nil
}

func registryWithGuardEpochThreeFixture(t *testing.T) *registry {
	t.Helper()
	reg := newRegistryForTest(t)
	reg.closed = false
	if err := registerGuardEpochThreeFixture(reg); err != nil {
		t.Fatalf("register epoch-3 fixture: %v", err)
	}
	reg.closed = true
	return reg
}

func TestGuardEditionThreeClosedCensusAndEdges(t *testing.T) {
	current, edge23, edge12 := guardEditionThreeFixture(t)
	if current.CodeEpoch != 3 || edge23.From.CodeEpoch != 2 || edge12.From.CodeEpoch != 1 {
		t.Fatalf("edition chain = %d <- %d <- %d, want 3 <- 2 <- 1",
			current.CodeEpoch, edge23.From.CodeEpoch, edge12.From.CodeEpoch)
	}
	wantAdditions := append([]string(nil), guardEpoch3CommunicationTables[:]...)
	sort.Strings(wantAdditions)
	gotAdditions := make([]string, 0, len(edge23.Additions))
	for _, spec := range edge23.Additions {
		gotAdditions = append(gotAdditions, spec.Key.Relation)
	}
	if strings.Join(gotAdditions, "\x00") != strings.Join(wantAdditions, "\x00") {
		t.Fatalf("epoch-3 additions = %v, want %v", gotAdditions, wantAdditions)
	}

	coreOnly := coreOnlyCurrentGuardManifest(t)
	if coreOnly.CodeEpoch != 2 {
		t.Fatalf("zero-of-five census selected epoch %d, want 2", coreOnly.CodeEpoch)
	}
	for n := 1; n < len(guardEpoch3CommunicationTables); n++ {
		tables := []string{guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable}
		tables = append(tables, guardEpoch3CommunicationTables[:n]...)
		if _, err := buildGuardManifest(tables); err == nil {
			t.Fatalf("%d-of-%d epoch-3 census was accepted", n, len(guardEpoch3CommunicationTables))
		}
	}
}

func TestGuardEditionFourClosedCensusAndEdges(t *testing.T) {
	current, edge34, edge23, edge12 := guardEditionFourFixture(t)
	if current.CodeEpoch != 4 || edge34.From.CodeEpoch != 3 ||
		edge23.From.CodeEpoch != 2 || edge12.From.CodeEpoch != 1 {
		t.Fatalf("edition chain = %d <- %d <- %d <- %d, want 4 <- 3 <- 2 <- 1",
			current.CodeEpoch, edge34.From.CodeEpoch, edge23.From.CodeEpoch, edge12.From.CodeEpoch)
	}
	wantAdditions := append([]string(nil), guardEpoch4ProtocolTables[:]...)
	sort.Strings(wantAdditions)
	gotAdditions := make([]string, 0, len(edge34.Additions))
	for _, spec := range edge34.Additions {
		gotAdditions = append(gotAdditions, spec.Key.Relation)
	}
	if strings.Join(gotAdditions, "\x00") != strings.Join(wantAdditions, "\x00") {
		t.Fatalf("epoch-4 additions = %v, want %v", gotAdditions, wantAdditions)
	}
	for _, old := range edge34.From.Specs {
		now, ok := current.lookup(old.Key)
		if !ok || !guardSpecsByteIdentical(old, now) {
			t.Errorf("epoch-3 entry %s is not carried forward byte-identically", old.Key)
		}
	}

	epoch3Tables := []string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	}
	epoch3Tables = append(epoch3Tables, guardEpoch3CommunicationTables[:]...)
	epoch3, err := buildGuardManifest(epoch3Tables)
	if err != nil {
		t.Fatal(err)
	}
	if epoch3.CodeEpoch != 3 {
		t.Fatalf("zero-of-two epoch-4 census selected epoch %d, want 3", epoch3.CodeEpoch)
	}
	for n := 1; n < len(guardEpoch4ProtocolTables); n++ {
		tables := append([]string(nil), epoch3Tables...)
		tables = append(tables, guardEpoch4ProtocolTables[:n]...)
		if _, err := buildGuardManifest(tables); err == nil {
			t.Fatalf("%d-of-%d epoch-4 census was accepted", n, len(guardEpoch4ProtocolTables))
		}
	}
	withoutPredecessor := []string{
		"old_alpha",
		guardEpoch2UserTombstoneTable,
		"old_omega",
		guardEpoch2DirectoryTombstoneTable,
	}
	withoutPredecessor = append(withoutPredecessor, guardEpoch4ProtocolTables[:]...)
	if _, err := buildGuardManifest(withoutPredecessor); err == nil {
		t.Fatal("a complete epoch-4 delta without epoch 3 was accepted")
	}
}

func TestGuardEditionFourSQLiteRunnerChainsEpochTwoThroughThree(t *testing.T) {
	current, _, edge23, _ := guardEditionFourFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge23.From)
	appendGuardV7Witness(t, db, dia, edge23.From, false)

	registered := make([]string, 0, len(current.Specs))
	for _, spec := range current.Specs {
		registered = append(registered, spec.Key.Relation)
	}
	if _, err := runAppendOnlyGuardUnits(
		context.Background(), db, dia, registered, "", false, nil,
	); err != nil {
		t.Fatalf("run direct epoch 2->3->4 chain: %v", err)
	}
	history, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if history.Kind != guardEditionHistoryTransitioned || history.Path != 2 {
		t.Fatalf("final epoch-4 history = %s/path-%d, want %s/path-2",
			history.Kind, history.Path, guardEditionHistoryTransitioned)
	}

	inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	if _, err := runAppendOnlyGuardUnits(
		context.Background(), db, dia, registered, "", false, nil,
	); err != nil {
		t.Fatalf("idempotent epoch-4 rerun: %v", err)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
		t.Fatalf("rerun changed inventory %d -> %d", inventoryBefore, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
		t.Fatalf("rerun changed receipts %d -> %d", receiptsBefore, got)
	}
}

func TestGuardEditionFourSQLiteChainsEpochOneThroughV7Bridge(t *testing.T) {
	current, _, _, edge12 := guardEditionFourFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge12.From)
	ctx := context.Background()

	if err := runGuardEditionTwoMigration(ctx, db, dia, current); err != nil {
		t.Fatalf("run epoch 1->2 core-v7 bridge for epoch-4 binary: %v", err)
	}
	registered := make([]string, 0, len(current.Specs))
	for _, spec := range current.Specs {
		registered = append(registered, spec.Key.Relation)
	}
	if _, err := runAppendOnlyGuardUnits(ctx, db, dia, registered, "", false, nil); err != nil {
		t.Fatalf("continue epoch 2->3->4 after core-v7 bridge: %v", err)
	}
	history, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if history.Kind != guardEditionHistoryTransitioned || history.Path != 1 {
		t.Fatalf("final epoch-4 history = %s/path-%d, want %s/path-1",
			history.Kind, history.Path, guardEditionHistoryTransitioned)
	}

	inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	if _, err := runAppendOnlyGuardUnits(ctx, db, dia, registered, "", false, nil); err != nil {
		t.Fatalf("idempotent epoch-4 rerun after epoch-1 bridge: %v", err)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
		t.Fatalf("rerun changed inventory %d -> %d", inventoryBefore, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
		t.Fatalf("rerun changed receipts %d -> %d", receiptsBefore, got)
	}
}

func TestGuardEditionThreeSQLiteUpgradePathsAreExact(t *testing.T) {
	current, edge23, edge12 := guardEditionThreeFixture(t)
	ctx := context.Background()
	tests := []struct {
		name       string
		seed       func(*testing.T, *sql.DB, dialect.Dialect)
		wantPath   guardEditionPath
		wantBefore guardEditionHistoryKind
	}{
		{
			name: "fresh epoch two completed",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				appendGuardV7Witness(t, db, dia, edge23.From, false)
			},
			wantPath: 2, wantBefore: guardEditionHistoryCurrentCompleted,
		},
		{
			name: "direct epoch two completed",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				appendGuardV7Witness(t, db, dia, edge23.From, true)
			},
			wantPath: 2, wantBefore: guardEditionHistoryDirectCompleted,
		},
		{
			name: "epoch one bridged through v7",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				// The fixture starts at epoch 1. Invoke the epoch-3 binary's real v7
				// continuation: it must select the compiled epoch-2 bridge, never jump 1->3.
				if err := runGuardEditionTwoMigration(ctx, db, dia, current); err != nil {
					t.Fatalf("epoch-3 v7 bridge: %v", err)
				}
			},
			wantPath: 1, wantBefore: guardEditionHistoryTransitioned,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap := edge23.From
			if tc.wantPath == 1 {
				bootstrap = edge12.From
			}
			db, dia := guardHistoricalEditionSQLiteDB(t, bootstrap)
			tc.seed(t, db, dia)
			beforeE2, err := verifyGuardEditionHistory(ctx, db, dia, edge23.From)
			if err != nil {
				t.Fatalf("verify completed epoch 2: %v", err)
			}
			if beforeE2.Kind != tc.wantBefore || beforeE2.Path != tc.wantPath {
				t.Fatalf("epoch-2 history = %s/path-%d, want %s/path-%d",
					beforeE2.Kind, beforeE2.Path, tc.wantBefore, tc.wantPath)
			}
			predecessor, err := verifyGuardEditionHistory(ctx, db, dia, current)
			if err != nil {
				t.Fatalf("verify epoch-3 predecessor: %v", err)
			}
			if predecessor.Kind != guardEditionHistoryPredecessorV7 || predecessor.Path != tc.wantPath {
				t.Fatalf("epoch-3 pre-transition = %s/path-%d, want %s/path-%d",
					predecessor.Kind, predecessor.Path, guardEditionHistoryPredecessorV7, tc.wantPath)
			}
			terminal := predecessor.TerminalReceiptID
			transitioned, err := transitionGuardEditionAfterModules(ctx, db, dia, current, predecessor)
			if err != nil {
				t.Fatalf("post-module epoch 2->3 transition: %v", err)
			}
			if transitioned.Kind != guardEditionHistoryTransitioned || transitioned.Path != tc.wantPath {
				t.Fatalf("epoch-3 post-transition = %s/path-%d", transitioned.Kind, transitioned.Path)
			}
			actual, err := readGuardBootstrapReceiptHistory(ctx, db, dia)
			if err != nil {
				t.Fatal(err)
			}
			var seal *guardReceipt
			for i := range actual {
				if actual[i].Epoch == current.CodeEpoch && actual[i].AttemptID == guardEditionSealAttemptID(edge23) {
					seal = &actual[i]
				}
			}
			if seal == nil || !seal.PredecessorReceiptID.Valid || seal.PredecessorReceiptID.D != terminal {
				t.Fatalf("epoch-3 seal predecessor = %+v, want terminal %s", seal, hexDigest(terminal))
			}
			inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
			receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
			reopened, err := verifyGuardEditionHistory(ctx, db, dia, current)
			if err != nil || reopened.Kind != guardEditionHistoryTransitioned {
				t.Fatalf("idempotent epoch-3 verify = %s, %v", reopened.Kind, err)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
				t.Fatalf("reopen changed inventory %d -> %d", inventoryBefore, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
				t.Fatalf("reopen changed receipts %d -> %d", receiptsBefore, got)
			}
		})
	}
}

func TestGuardEditionThreeRejectsReceiptInventoryPathCrossProducts(t *testing.T) {
	current, edge23, _ := guardEditionThreeFixture(t)
	variants, err := guardBootstrapReceiptVariants(edge23.From)
	if err != nil {
		t.Fatal(err)
	}
	find := func(state guardEditionReceiptState, path guardEditionPath) []guardReceipt {
		t.Helper()
		for _, variant := range variants {
			if variant.State == state && variant.Path == path {
				return variant.Receipts
			}
		}
		t.Fatalf("missing receipt variant %s/path-%d", state, path)
		return nil
	}
	for _, tc := range []struct {
		name         string
		inventory    guardEditionPath
		receiptState guardEditionReceiptState
		receiptPath  guardEditionPath
	}{
		{"fresh receipts over transitioned inventory", 1, guardEditionReceiptsCurrentCompleted, 2},
		{"transitioned receipts over fresh inventory", 2, guardEditionReceiptsSealed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dia := guardEmptyEditionSQLiteDB(t)
			seedGuardEditionVariant(t, db, dia, edge23.From, tc.inventory,
				find(tc.receiptState, tc.receiptPath))
			inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
			receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
			_, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("cross-product verify = %v, want ErrGuardManifestNoEdge", err)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
				t.Fatalf("refusal changed inventory %d -> %d", inventoryBefore, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
				t.Fatalf("refusal changed receipts %d -> %d", receiptsBefore, got)
			}
		})
	}
}

func TestGuardEditionThreePreflightOnlyAcceptsCompletedV7Predecessor(t *testing.T) {
	current, edge23, _ := guardEditionThreeFixture(t)
	for _, tc := range []struct {
		name     string
		witness  guardV7SealPhase
		wantPass bool
	}{
		{"raw bootstrap", "", false},
		{"direct start only", guardV7SealStart, false},
		{"fresh completion", guardV7SealCompletion, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, dia := guardHistoricalEditionSQLiteDB(t, edge23.From)
			if tc.witness != "" {
				seal, err := guardV7Seal(edge23.From, tc.witness, false)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := insertGuardReceipt(context.Background(), tx, dia, seal); err != nil {
					_ = tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			got, err := verifyGuardCompletedV7History(context.Background(), db, dia, current)
			if tc.wantPass {
				if err != nil || !guardEditionHistoryCompletesV7(got.Kind) {
					t.Fatalf("completed preflight = %s, %v", got.Kind, err)
				}
				return
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("incomplete predecessor preflight = %v, want ErrGuardManifestNoEdge", err)
			}
		})
	}
}

func TestGuardEditionThreeRefusesDowngradeAndSameEpochDrift(t *testing.T) {
	current, edge23, _ := guardEditionThreeFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge23.From)
	appendGuardV7Witness(t, db, dia, edge23.From, false)
	predecessor, err := verifyGuardEditionHistory(context.Background(), db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transitionGuardEditionAfterModules(context.Background(), db, dia, current, predecessor); err != nil {
		t.Fatal(err)
	}
	inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	if _, err := verifyGuardEditionHistory(context.Background(), db, dia, edge23.From); err == nil {
		t.Fatal("epoch-2 binary accepted an epoch-3 database")
	}
	tables := make([]string, 0, len(current.Specs)+1)
	for _, spec := range current.Specs {
		tables = append(tables, spec.Key.Relation)
	}
	tables = append(tables, "unrelated_same_epoch_table")
	drifted, err := buildGuardManifestAtEpoch(tables, current.CodeEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyGuardEditionHistory(context.Background(), db, dia, drifted); err == nil {
		t.Fatal("same-epoch manifest drift was accepted")
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
		t.Fatalf("refusals changed inventory %d -> %d", inventoryBefore, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
		t.Fatalf("refusals changed receipts %d -> %d", receiptsBefore, got)
	}
}

func TestGuardEditionThreePostgresChainsAndResumesEpochTwoBeforeTransition(t *testing.T) {
	for _, crashAfterV7 := range []bool{false, true} {
		name := "single boot"
		if crashAfterV7 {
			name = "crash after v7"
		}
		t.Run(name, func(t *testing.T) {
			dsns := isolatedPG(t)
			ctx := context.Background()
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
			st, err := Open(ctx, cfg, registerGuardEpochThreeFixture)
			if err != nil {
				t.Fatalf("seed fresh epoch 3: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			db := guardPGProbe(t, dsns.App)
			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			current, err := buildGuardManifest(registryWithGuardEpochThreeFixture(t).appendOnlyTables())
			if err != nil {
				t.Fatal(err)
			}
			edge23, ok, err := guardManifestEditionEdge(current)
			if err != nil || !ok {
				t.Fatalf("derive 2->3 edge: ok=%v err=%v", ok, err)
			}
			edge12, ok, err := guardManifestEditionEdge(edge23.From)
			if err != nil || !ok {
				t.Fatalf("derive 1->2 edge: ok=%v err=%v", ok, err)
			}

			// Reproduce the durable K1 state over the real canonical catalog. The
			// current tables remain present as an in-place upgrade would find them;
			// only v7's relations/tracker and the guard edition ledgers are rewound.
			dropCoreDirectoryTables(t, db)
			if _, err := db.ExecContext(ctx, dia.Rebind(
				"DELETE FROM "+coreTrackingTable+" WHERE version = ?"), coreDirectoryMigrationVersion); err != nil {
				t.Fatal(err)
			}
			wipeGuardLogForFixture(t, db, guardGateEventsTable, "")
			wipeGuardLogForFixture(t, db, guardReceiptsTable, "")
			wipeGuardLogForFixture(t, db, guardInventoryEventsTable, "")
			seedGuardHistoricalEdition(t, db, dia, edge12.From)
			seedGuardPredecessorReady(t, db, dia, edge12.From, guardPredecessorComplete)

			if crashAfterV7 {
				migration := coreDirectoryMigration(
					dia, coreDescriptors(), guardEditionTwoMigrationExec(dia, current),
				)
				if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err != nil {
					t.Fatalf("commit v7 epoch-2 bridge: %v", err)
				}
				bridged, err := verifyGuardEditionHistory(ctx, db, dia, edge23.From)
				if err != nil {
					t.Fatalf("verify crash-boundary epoch 2: %v", err)
				}
				if bridged.Kind != guardEditionHistoryTransitioned ||
					bridged.GateState != guardEditionGateCurrent || bridged.Gate.Phase != gatePhasePending {
					t.Fatalf("crash boundary = %s gate %s/%s, want transitioned current/pending",
						bridged.Kind, bridged.GateState, bridged.Gate.Phase)
				}
			}

			upgraded, err := Open(ctx, cfg, registerGuardEpochThreeFixture)
			if err != nil {
				t.Fatalf("open epoch 3 over K1 chain: %v", err)
			}
			if err := upgraded.Close(); err != nil {
				t.Fatal(err)
			}
			history, err := verifyGuardEditionHistory(ctx, db, dia, current)
			if err != nil {
				t.Fatal(err)
			}
			if history.Kind != guardEditionHistoryTransitioned || history.Path != 1 ||
				history.GateState != guardEditionGateCurrent || history.Gate.Phase != gatePhaseReady ||
				history.Gate.Condition != gateConditionVerified {
				t.Fatalf("final history = %s/path-%d gate %s/%s/%s",
					history.Kind, history.Path, history.GateState, history.Gate.Phase, history.Gate.Condition)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable+
				" WHERE receipt_kind='bootstrap'"); got != 5 {
				t.Fatalf("bootstrap receipts = %d, want e1 bootstrap plus e2/e3 seals", got)
			}
			for _, manifest := range []guardManifest{edge12.From, edge23.From, current} {
				empty, err := emptyRetainedDigest()
				if err != nil {
					t.Fatal(err)
				}
				rollout, err := guardBootstrapRollout(manifest, 0, empty)
				if err != nil {
					t.Fatal(err)
				}
				gate, err := foldGateEvents(ctx, db, dia, rollout.RolloutID)
				if err != nil || !gate.Found || gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
					t.Fatalf("epoch-%d gate = found:%v %s/%s err:%v",
						manifest.CodeEpoch, gate.Found, gate.Phase, gate.Condition, err)
				}
			}
		})
	}
}

func guardHistoricalEditionSQLiteDB(t *testing.T, bootstrap guardManifest) (*sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	db, err := sql.Open("sqlite", t.TempDir()+"/guard-edition.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range dia.GuardControlPlaneStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply guard control-plane statement: %v", err)
		}
	}
	seedGuardHistoricalEdition(t, db, dia, bootstrap)
	return db, dia
}

func guardEmptyEditionSQLiteDB(t *testing.T) (*sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	db, err := sql.Open("sqlite", t.TempDir()+"/guard-edition-empty.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range dia.GuardControlPlaneStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply guard control-plane statement: %v", err)
		}
	}
	return db, dia
}

func seedGuardEditionVariant(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	manifest guardManifest,
	path guardEditionPath,
	receipts []guardReceipt,
) {
	t.Helper()
	expected, err := guardActivationExpectationsForPath(manifest, path)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	events := make([]inventoryEvent, 0, len(expected))
	for _, expectation := range expected {
		spec := expectation.Spec
		events = append(events, inventoryEvent{
			Kind: inventoryActivate, Key: spec.Key, Producer: spec.Producer,
			Format: manifest.Format, CodeEpoch: expectation.Epoch,
			DefinitionSHA256: spec.DefinitionSHA256, SpecSHA256: spec.SpecSHA256,
			DesiredEnableState: spec.DesiredEnableState, LegacyAllowedStates: spec.LegacyAllowedStates,
			RetainedRevision: 0, RetainedSHA256: retained,
		})
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := appendInventoryEvents(context.Background(), tx, dia, events); err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if _, err := insertGuardReceipt(context.Background(), tx, dia, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedGuardHistoricalEdition(t *testing.T, db *sql.DB, dia dialect.Dialect, bootstrap guardManifest) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Reproduce the historical v6 writer explicitly. guardBootstrapExec is the
	// CURRENT writer and must refuse an incomplete epoch-2 census; using it here
	// would turn the test fixture into a bypass of that production guard.
	retained, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rolloutID, err := guardRolloutID(bootstrap.Format, bootstrap.CodeEpoch, bootstrap.CodeSHA256, 0, retained)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]inventoryEvent, 0, len(bootstrap.Specs))
	for _, spec := range bootstrap.Specs {
		events = append(events, inventoryEvent{
			Kind: inventoryActivate, Key: spec.Key, Producer: spec.Producer,
			Format: bootstrap.Format, CodeEpoch: bootstrap.CodeEpoch,
			DefinitionSHA256: spec.DefinitionSHA256, SpecSHA256: spec.SpecSHA256,
			DesiredEnableState: spec.DesiredEnableState, LegacyAllowedStates: spec.LegacyAllowedStates,
			RetainedRevision: 0, RetainedSHA256: retained,
		})
	}
	if err := appendInventoryEvents(context.Background(), tx, dia, events); err != nil {
		t.Fatal(err)
	}
	meta, err := guardMetadataSpecs(bootstrap.Format)
	if err != nil {
		t.Fatal(err)
	}
	rollout := guardRolloutContext{
		RolloutID: rolloutID, Format: bootstrap.Format, CodeEpoch: bootstrap.CodeEpoch,
		CodeSHA256: bootstrap.CodeSHA256, RetainedRevision: 0, RetainedSHA256: retained,
	}
	for _, spec := range meta {
		unitID, err := guardBootstrapUnitID(bootstrap.Format, spec.Key)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := bootstrapReceiptFor(rollout, spec, unitID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := insertGuardReceipt(context.Background(), tx, dia, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestGuardEditionTwoCarriesOneExactPredecessor(t *testing.T) {
	t.Parallel()
	current, edge := guardEditionFixture(t)
	if current.Format != 1 || current.CodeEpoch != 2 {
		t.Fatalf("current edition = %d/%d, want format 1 epoch 2", current.Format, current.CodeEpoch)
	}
	if edge.From.Format != current.Format || edge.From.CodeEpoch != 1 {
		t.Fatalf("predecessor edition = %d/%d, want format 1 epoch 1", edge.From.Format, edge.From.CodeEpoch)
	}
	if len(edge.Additions) != 2 {
		t.Fatalf("edge additions = %d, want exactly 2", len(edge.Additions))
	}
	added := map[string]bool{}
	for _, spec := range edge.Additions {
		added[spec.Key.Relation] = true
	}
	for _, relation := range []string{guardEpoch2UserTombstoneTable, guardEpoch2DirectoryTombstoneTable} {
		if !added[relation] {
			t.Errorf("edge does not add %s", relation)
		}
	}
	for _, old := range edge.From.Specs {
		now, ok := current.lookup(old.Key)
		if !ok || !guardSpecsByteIdentical(old, now) {
			t.Errorf("predecessor entry %s is not carried forward byte-identically", old.Key)
		}
	}

	// An epoch-1 manifest with one unrelated census entry is not "close enough".
	// Its epoch is right and its digest is wrong, so the exact edge denies it.
	drifted, err := buildGuardManifestAtEpoch([]string{"old_alpha", "old_omega", "unrelated_new_table"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if edge.authorizes(drifted.Format, drifted.CodeEpoch, drifted.CodeSHA256) {
		t.Fatal("the edge authorized an epoch-1 census with unrelated drift")
	}
	err = classifyRecordedEdition(current, recordedEdition{
		Found: true, Format: drifted.Format, CodeEpoch: drifted.CodeEpoch, CodeSHA256: drifted.CodeSHA256,
	})
	if !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("unrelated predecessor drift = %v, want ErrGuardManifestNoEdge", err)
	}

}

func TestGuardEditionTwoRefusesAnIncompleteClosedRegistry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tables []string
	}{
		{"neither addition", []string{"old_alpha"}},
		{"only user tombstone", []string{"old_alpha", guardEpoch2UserTombstoneTable}},
		{"only directory tombstone", []string{"old_alpha", guardEpoch2DirectoryTombstoneTable}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incomplete, err := buildGuardManifest(tc.tables)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := guardManifestEditionEdge(incomplete); err != nil {
				t.Fatal(err)
			} else if ok {
				t.Fatal("an incomplete epoch-2 census exposes a predecessor edge")
			}
			if err := requireCompleteGuardCurrentEdition(incomplete); err == nil {
				t.Fatal("an incomplete epoch-2 census was accepted")
			}
		})
	}
	incomplete, err := buildGuardManifest([]string{"old_alpha"})
	if err != nil {
		t.Fatal(err)
	}
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	db, err := sql.Open("sqlite", t.TempDir()+"/incomplete-bootstrap.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range dia.GuardControlPlaneStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := guardBootstrapExec(dia, incomplete)(context.Background(), tx); err == nil {
		_ = tx.Rollback()
		t.Fatal("the production bootstrap writer accepted an incomplete epoch-2 census")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var inventory, receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + guardInventoryEventsTable).Scan(&inventory); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM " + guardReceiptsTable).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if inventory != 0 || receipts != 0 {
		t.Fatalf("refused incomplete bootstrap wrote inventory=%d receipts=%d", inventory, receipts)
	}
	complete, _ := guardEditionFixture(t)
	if err := requireCompleteGuardCurrentEdition(complete); err != nil {
		t.Fatalf("complete epoch-2 census: %v", err)
	}
}

func TestHistoricalBootstrapReceiptsRemainVerifiable(t *testing.T) {
	t.Parallel()
	current, edge := guardEditionFixture(t)
	ctx := context.Background()

	t.Run("exact predecessor", func(t *testing.T) {
		db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
		if err := verifyGuardControlPlaneObjects(ctx, db, dia, current); err != nil {
			t.Fatalf("epoch-2 binary rejected the exact epoch-1 bootstrap receipts: %v", err)
		}
		state, _, err := verifyGuardBootstrapReceiptHistory(ctx, db, dia, current)
		if err != nil {
			t.Fatal(err)
		}
		if state != guardEditionReceiptsPredecessor {
			t.Fatalf("bootstrap state = %s, want %s", state, guardEditionReceiptsPredecessor)
		}
	})

	t.Run("same epoch with another census", func(t *testing.T) {
		tables := make([]string, 0, len(edge.From.Specs)+1)
		for _, spec := range edge.From.Specs {
			tables = append(tables, spec.Key.Relation)
		}
		tables = append(tables, "unrelated_new_table")
		drifted, err := buildGuardManifestAtEpoch(tables, edge.From.CodeEpoch)
		if err != nil {
			t.Fatal(err)
		}
		db, dia := guardHistoricalEditionSQLiteDB(t, drifted)
		if err := verifyGuardControlPlaneObjects(ctx, db, dia, current); !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
			t.Fatalf("bootstrap receipts from an unrecognized epoch-1 census = %v, want ErrGuardBootstrapReceiptsInvalid", err)
		}
	})
}

func runGuardEditionTwoMigration(ctx context.Context, db *sql.DB, dia dialect.Dialect, current guardManifest) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := guardEditionTwoMigrationExec(dia, current)(ctx, tx, coreDirectoryInitiallyAbsent); err != nil {
		return err
	}
	return tx.Commit()
}

func verifyK2SQLiteBootstrapReceipts(
	ctx context.Context,
	q rowQuerier,
	dia dialect.Dialect,
	m guardManifest,
) error {
	if err := verifyGuardControlPlaneShape(ctx, q, dia); err != nil {
		return err
	}
	receipts, err := readGuardBootstrapReceipts(ctx, q, dia)
	if err != nil {
		return err
	}
	specs, err := guardMetadataSpecs(m.Format)
	if err != nil {
		return err
	}
	if len(receipts) != len(specs) {
		return ErrGuardBootstrapReceiptsInvalid
	}
	retained, err := emptyRetainedDigest()
	if err != nil {
		return err
	}
	rollout, err := guardBootstrapRollout(m, 0, retained)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		unitID, err := guardBootstrapUnitID(m.Format, spec.Key)
		if err != nil {
			return err
		}
		got, ok := receipts[unitID]
		if !ok {
			return ErrGuardBootstrapReceiptsInvalid
		}
		want, err := bootstrapReceiptFor(rollout, spec, unitID)
		if err != nil {
			return err
		}
		if receiptDifference(got, want) != "" {
			return ErrGuardBootstrapReceiptsInvalid
		}
	}
	return nil
}

// verifyK2PostgresOpenOrder freezes the relevant epoch-1 call order: inventory is
// interpreted against the old manifest before latestRecordedEdition is consulted. The
// callback lets the regression prove that a mixed epoch-2 inventory stops that later read.
func verifyK2PostgresOpenOrder(
	ctx context.Context,
	q dialect.Querier,
	dia dialect.Dialect,
	old guardManifest,
	afterInventory func() error,
) error {
	if _, _, err := verifyInventoryChain(ctx, q, dia, old); err != nil {
		return err
	}
	return afterInventory()
}

func TestSQLiteGuardEditionTransitionIsAtomicAndIdempotent(t *testing.T) {
	current, edge := guardEditionFixture(t)
	ctx := context.Background()

	t.Run("committed transition", func(t *testing.T) {
		db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
		before, err := verifyGuardEditionHistory(ctx, db, dia, current)
		if err != nil {
			t.Fatal(err)
		}
		if before.Kind != guardEditionHistoryPredecessor {
			t.Fatalf("initial history = %s, want predecessor", before.Kind)
		}
		hook := guardEditionTwoMigrationExec(dia, current)
		calls := 0
		migration := coreDirectoryMigration(dia, coreDescriptors(),
			func(ctx context.Context, tx *sql.Tx, initial coreDirectoryInitialDisposition) error {
				calls++
				if initial != coreDirectoryInitiallyAbsent {
					t.Fatalf("upgrade callback disposition = %s, want absent", initial)
				}
				return hook(ctx, tx, initial)
			})
		if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err != nil {
			t.Fatalf("transition: %v", err)
		}
		if calls != 1 {
			t.Fatalf("first migrate.Apply invoked v7 continuation %d times, want one", calls)
		}
		after, err := verifyGuardEditionHistory(ctx, db, dia, current)
		if err != nil {
			t.Fatal(err)
		}
		if after.Kind != guardEditionHistoryTransitioned {
			t.Fatalf("transition history = %s, want transitioned", after.Kind)
		}
		if _, _, err := verifyInventoryChain(ctx, db, dia, current); err != nil {
			t.Fatalf("upgraded inventory: %v", err)
		}
		if _, _, err := verifyInventoryChainExact(ctx, db, dia, current); err == nil {
			t.Fatal("the historical carry-forward was mistaken for a fresh epoch-2 census")
		}
		var inventoryBefore, receiptsBefore int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + guardInventoryEventsTable).Scan(&inventoryBefore); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM " + guardReceiptsTable).Scan(&receiptsBefore); err != nil {
			t.Fatal(err)
		}
		if inventoryBefore != len(edge.From.Specs)+len(edge.Additions) {
			t.Fatalf("inventory rows = %d, want %d predecessor + %d additions",
				inventoryBefore, len(edge.From.Specs), len(edge.Additions))
		}
		if receiptsBefore != 4 {
			t.Fatalf("bootstrap receipts = %d, want old three plus one transition seal", receiptsBefore)
		}
		if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err != nil {
			t.Fatalf("tracked idempotent retry: %v", err)
		}
		if calls != 1 {
			t.Fatalf("tracked retry re-entered v7 continuation: calls=%d", calls)
		}
		var inventoryAfter, receiptsAfter int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + guardInventoryEventsTable).Scan(&inventoryAfter); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT COUNT(*) FROM " + guardReceiptsTable).Scan(&receiptsAfter); err != nil {
			t.Fatal(err)
		}
		if inventoryAfter != inventoryBefore || receiptsAfter != receiptsBefore {
			t.Fatalf("retry changed history: inventory %d -> %d, receipts %d -> %d",
				inventoryBefore, inventoryAfter, receiptsBefore, receiptsAfter)
		}
	})

	t.Run("rollback leaves the exact predecessor", func(t *testing.T) {
		db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
		history, err := verifyGuardEditionHistory(ctx, db, dia, current)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := transitionGuardEditionInTx(ctx, tx, dia, current, history, nil, false); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := verifyInventoryChainExact(ctx, db, dia, edge.From); err != nil {
			t.Fatalf("rollback did not preserve predecessor: %v", err)
		}
		if _, _, err := verifyInventoryChain(ctx, db, dia, current); err == nil {
			t.Fatal("rolled-back additions appeared in the current census")
		}
		if err := verifyK2SQLiteBootstrapReceipts(ctx, db, dia, edge.From); err != nil {
			t.Fatalf("rollback left a transition seal: %v", err)
		}
	})
}

func TestGuardEditionRefusesATornAdditionSet(t *testing.T) {
	t.Parallel()
	current, edge := guardEditionFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
	ctx := context.Background()
	revision, retained, err := verifyInventoryChainExact(ctx, db, dia, edge.From)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := appendInventoryEvents(ctx, tx, dia,
		guardManifestAdditionEvents(edge, revision, retained)[:1]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyInventoryChain(ctx, db, dia, current); !errors.Is(err, ErrGuardInventoryUnsupported) {
		t.Fatalf("torn addition set = %v, want ErrGuardInventoryUnsupported", err)
	}
	if err := runGuardEditionTwoMigration(ctx, db, dia, current); err == nil {
		t.Fatal("v7 transition laundered a torn addition set")
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + guardReceiptsTable).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 3 {
		t.Fatalf("refused torn transition wrote a seal: receipts=%d", receipts)
	}
}

func TestGuardEditionRefusesAPreexistingTargetReceiptStream(t *testing.T) {
	t.Parallel()
	current, edge := guardEditionFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
	ctx := context.Background()
	history, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	target, err := guardBootstrapRollout(current, history.Revision, history.Retained)
	if err != nil {
		t.Fatal(err)
	}
	spec := current.Specs[0]
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertGuardReceipt(ctx, tx, dia, guardReceipt{
		RolloutID: target.RolloutID, UnitID: "preexisting-target-unit", Kind: guardReceiptKindUnit,
		Intent: intentAdoptLegacy, Key: spec.Key,
		Epoch: target.CodeEpoch, Format: target.Format, CodeSHA256: target.CodeSHA256,
		RetainedRevision: target.RetainedRevision, RetainedSHA256: target.RetainedSHA256,
		SpecSHA256: spec.SpecSHA256, DefinitionSHA256: spec.DefinitionSHA256,
		PrestateSHA256: spec.SpecSHA256, ToEnableState: guardStateOrigin, AttemptID: "pre-v7",
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := runGuardEditionTwoMigration(ctx, db, dia, current); !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("v7 over a preexisting target receipt stream = %v, want ErrGuardManifestNoEdge", err)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != len(edge.From.Specs) {
		t.Fatalf("refused transition changed predecessor inventory to %d rows, want %d", got, len(edge.From.Specs))
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable+" WHERE receipt_kind = 'bootstrap'"); got != 3 {
		t.Fatalf("refused transition wrote a seal: bootstrap receipts=%d", got)
	}
}

func TestGuardEditionTransitionRowsAreAtomicWithPending(t *testing.T) {
	current, edge := guardEditionFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
	ctx := context.Background()
	history, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	// SQLite is used as a deterministic transaction harness. Production passes
	// openCurrent=true only on PostgreSQL; the row writer itself is deliberately portable.
	if _, err := db.Exec(`CREATE TRIGGER fail_epoch_two_pending
BEFORE INSERT ON ` + guardGateEventsTable + `
WHEN NEW.kind = 'pending-opened' AND NEW.code_epoch = 2
BEGIN SELECT RAISE(ABORT, 'injected pending failure'); END`); err != nil {
		t.Fatal(err)
	}
	currentPlans := []guardUnitPlan{{UnitID: "current-placeholder"}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendGuardEditionTransitionRows(ctx, tx, dia, current, edge,
		history.Revision, history.Retained, history.TerminalReceiptID, currentPlans, true); err == nil {
		_ = tx.Rollback()
		t.Fatal("injected pending-opened failure did not fail the transition")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyInventoryChainExact(ctx, db, dia, edge.From); err != nil {
		t.Fatalf("failed transition left inventory changes behind: %v", err)
	}
	var receipts, gates int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + guardReceiptsTable).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM " + guardGateEventsTable).Scan(&gates); err != nil {
		t.Fatal(err)
	}
	if receipts != 3 || gates != 0 {
		t.Fatalf("failed pending insert left receipts=%d gates=%d, want predecessor 3/0", receipts, gates)
	}
}

type guardPredecessorFixtureMode string

const (
	guardPredecessorComplete       guardPredecessorFixtureMode = "complete"
	guardPredecessorOmitExpected   guardPredecessorFixtureMode = "omit-expected"
	guardPredecessorOmitReceipt    guardPredecessorFixtureMode = "omit-receipt"
	guardPredecessorAddUnknownUnit guardPredecessorFixtureMode = "add-unknown-unit"
)

func seedGuardPredecessorReady(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	predecessor guardManifest,
	mode guardPredecessorFixtureMode,
) guardRolloutContext {
	t.Helper()
	ctx := context.Background()
	retained, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rollout, err := guardBootstrapRollout(predecessor, 0, retained)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]guardKey, 0, len(predecessor.Specs))
	for _, spec := range predecessor.Specs {
		keys = append(keys, spec.Key)
	}
	observed, err := projectGuardCatalogBatch(ctx, db, keys)
	if err != nil {
		t.Fatal(err)
	}
	plans, refusals, err := buildGuardUnitPlans(predecessor, observed)
	if err != nil {
		t.Fatal(err)
	}
	if len(refusals) != 0 || len(plans) == 0 {
		t.Fatalf("build predecessor K2 plan: plans=%d refusals=%v", len(plans), refusals)
	}
	executed := append([]guardUnitPlan(nil), plans...)
	expected := guardPlanUnitIDs(plans)
	switch mode {
	case guardPredecessorComplete:
	case guardPredecessorOmitExpected:
		executed = executed[:len(executed)-1]
		expected = expected[:len(expected)-1]
	case guardPredecessorOmitReceipt:
	case guardPredecessorAddUnknownUnit:
		expected = append(expected, "unknown-k2-unit")
	default:
		t.Fatalf("unknown predecessor fixture mode %q", mode)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
		RolloutID: rollout.RolloutID, Kind: gateEventPendingOpened,
		Format: rollout.Format, CodeEpoch: rollout.CodeEpoch, CodeSHA256: rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision, RetainedSHA256: rollout.RetainedSHA256,
		Phase: gatePhasePending, Condition: gateConditionClean, ExpectedUnits: expected,
	}); err != nil {
		t.Fatal(err)
	}
	for i, plan := range executed {
		row, ok := observed[plan.Spec.Key]
		if !ok {
			t.Fatalf("predecessor catalog lost %s", plan.Spec.Key)
		}
		pre := rollout.bind(prestateFromCatalog(row, plan.Spec, false), plan.Spec)
		digest, err := prestateDigest(pre)
		if err != nil {
			t.Fatal(err)
		}
		attemptID, err := guardAttemptID(rollout.RolloutID, plan.UnitID, 1, digest)
		if err != nil {
			t.Fatal(err)
		}
		runner := guardUnitRunner{
			dia: dia, rollout: rollout, plan: plan, attemptID: attemptID,
		}
		if _, err := appendGateEvent(ctx, tx, dia, runner.gateEvent(
			gateEventAttemptStarted, pre, digest, gatePhasePending, gateConditionClean, guardDiagnostic{})); err != nil {
			t.Fatal(err)
		}
		if _, err := appendGateEvent(ctx, tx, dia, runner.gateEvent(
			gateEventAttemptJudged, pre, digest, gatePhasePending, gateConditionClean, guardDiagnostic{})); err != nil {
			t.Fatal(err)
		}
		if mode == guardPredecessorOmitReceipt && i == len(executed)-1 {
			continue
		}
		if err := runner.receipt(ctx, tx, pre); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := computeGuardCheckpoint(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
		RolloutID: rollout.RolloutID, Kind: gateEventReady,
		Format: rollout.Format, CodeEpoch: rollout.CodeEpoch, CodeSHA256: rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision, RetainedSHA256: rollout.RetainedSHA256,
		Phase: gatePhaseReady, Condition: gateConditionVerified, ExpectedUnits: expected,
		Checkpoint: checkpoint, CheckpointPresent: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return rollout
}

func TestGuardEditionPostgresV7TransitionIsAtomicAndIdempotent(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db := guardPGProbe(t, dsns.Owner)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatal(err)
	}
	edge, ok, err := guardManifestEditionEdge(current)
	if err != nil || !ok {
		t.Fatalf("derive edition edge: ok=%v err=%v", ok, err)
	}

	// Turn the fresh fixture into the exact durable state a K2 deployment leaves.
	// The fixture helper restores every metadata guard in ALWAYS before v7 sees it;
	// the three directory relations and v7 tracking row are removed together so the
	// real migration observes the physical `absent` disposition an upgrade starts in.
	dropCoreDirectoryTables(t, db)
	if _, err := db.ExecContext(ctx, dia.Rebind(
		"DELETE FROM "+coreTrackingTable+" WHERE version = ?"), coreDirectoryMigrationVersion); err != nil {
		t.Fatal(err)
	}
	wipeGuardLogForFixture(t, db, guardGateEventsTable, "")
	wipeGuardLogForFixture(t, db, guardReceiptsTable, "")
	wipeGuardLogForFixture(t, db, guardInventoryEventsTable, "")
	seedGuardHistoricalEdition(t, db, dia, edge.From)
	seedGuardPredecessorReady(t, db, dia, edge.From, guardPredecessorComplete)
	predecessor, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if predecessor.Kind != guardEditionHistoryPredecessor {
		t.Fatalf("fixture history = %s, want predecessor", predecessor.Kind)
	}
	predecessorInventory := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	predecessorReceipts := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	predecessorGates := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable)
	hook := guardEditionTwoMigrationExec(dia, current)
	hookCalls := 0
	migration := coreDirectoryMigration(dia, coreDescriptors(),
		func(ctx context.Context, tx *sql.Tx, initial coreDirectoryInitialDisposition) error {
			hookCalls++
			if initial != coreDirectoryInitiallyAbsent {
				t.Fatalf("PostgreSQL upgrade callback disposition = %s, want absent", initial)
			}
			return hook(ctx, tx, initial)
		})

	// The injected final-statement failure proves additions and seal do not commit
	// before pending-opened. This is the real PostgreSQL v7 callback, not the row
	// helper used by the portable transaction test above.
	for _, stmt := range []string{
		`CREATE FUNCTION guard_fail_epoch_two_pending() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'injected epoch-two pending failure';
END
$$`,
		`CREATE TRIGGER guard_fail_epoch_two_pending
BEFORE INSERT ON ` + guardGateEventsTable + `
FOR EACH ROW WHEN (NEW.kind = 'pending-opened' AND NEW.code_epoch = 2)
EXECUTE FUNCTION guard_fail_epoch_two_pending()`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err == nil {
		t.Fatal("injected pending failure did not abort the PostgreSQL v7 callback")
	}
	if hookCalls != 1 {
		t.Fatalf("failed PostgreSQL v7 invoked its continuation %d times, want one", hookCalls)
	}
	afterFailure, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Kind != guardEditionHistoryPredecessor {
		t.Fatalf("failed v7 left history %s, want exact predecessor", afterFailure.Kind)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != predecessorInventory {
		t.Fatalf("failed v7 changed predecessor inventory %d -> %d", predecessorInventory, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != predecessorReceipts {
		t.Fatalf("failed v7 changed predecessor receipts %d -> %d", predecessorReceipts, got)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable); got != predecessorGates {
		t.Fatalf("failed v7 changed predecessor gates %d -> %d", predecessorGates, got)
	}
	assertCoreDirectoryExistence(t, db, dia, map[string]bool{
		"core_directory_epoch": false, "core_directory_tombstone": false, "core_user_tombstone": false,
	})
	for _, stmt := range []string{
		"DROP TRIGGER guard_fail_epoch_two_pending ON " + guardGateEventsTable,
		"DROP FUNCTION guard_fail_epoch_two_pending()",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err != nil {
		t.Fatal(err)
	}
	if hookCalls != 2 {
		t.Fatalf("successful PostgreSQL v7 continuation calls = %d, want failed attempt plus retry", hookCalls)
	}
	transitioned, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatal(err)
	}
	if transitioned.Kind != guardEditionHistoryTransitioned ||
		transitioned.GateState != guardEditionGateCurrent || transitioned.Gate.Phase != gatePhasePending {
		t.Fatalf("committed v7 = history:%s gate:%s/%s, want transitioned current/pending",
			transitioned.Kind, transitioned.GateState, transitioned.Gate.Phase)
	}
	if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable+" WHERE receipt_kind = 'bootstrap'"); got != 4 {
		t.Fatalf("committed v7 bootstrap receipts = %d, want old three plus seal", got)
	}
	inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
	receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
	gatesBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable)
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration}); err != nil {
		t.Fatalf("tracked idempotent v7 retry: %v", err)
	}
	if hookCalls != 2 {
		t.Fatalf("tracked PostgreSQL retry re-entered v7 continuation: calls=%d", hookCalls)
	}
	if inventoryAfter := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); inventoryAfter != inventoryBefore {
		t.Fatalf("v7 retry changed inventory %d -> %d", inventoryBefore, inventoryAfter)
	}
	if receiptsAfter := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); receiptsAfter != receiptsBefore {
		t.Fatalf("v7 retry changed receipts %d -> %d", receiptsBefore, receiptsAfter)
	}
	if gatesAfter := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable); gatesAfter != gatesBefore {
		t.Fatalf("v7 retry changed gates %d -> %d", gatesBefore, gatesAfter)
	}

	// Freeze the real K2 ordering against the real PostgreSQL rows: inventory fails
	// before the old helper can consult latestRecordedEdition.
	reachedLatest := false
	err = verifyK2PostgresOpenOrder(ctx, db, dia, edge.From, func() error {
		reachedLatest = true
		return nil
	})
	if !errors.Is(err, ErrGuardInventoryUnsupported) || reachedLatest {
		t.Fatalf("K2 PostgreSQL after v7 = reachedLatest:%v err:%v, want inventory refusal first",
			reachedLatest, err)
	}
}

func TestGuardEditionRefusesIncompleteK2PredecessorAttestations(t *testing.T) {
	for _, mode := range []guardPredecessorFixtureMode{
		guardPredecessorOmitExpected,
		guardPredecessorOmitReceipt,
		guardPredecessorAddUnknownUnit,
	} {
		t.Run(string(mode), func(t *testing.T) {
			dsns := isolatedPG(t)
			ctx := context.Background()
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			db := guardPGProbe(t, dsns.Owner)
			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
			if err != nil {
				t.Fatal(err)
			}
			edge, ok, err := guardManifestEditionEdge(current)
			if err != nil || !ok {
				t.Fatalf("derive edition edge: ok=%v err=%v", ok, err)
			}

			dropCoreDirectoryTables(t, db)
			if _, err := db.ExecContext(ctx, dia.Rebind(
				"DELETE FROM "+coreTrackingTable+" WHERE version = ?"), coreDirectoryMigrationVersion); err != nil {
				t.Fatal(err)
			}
			wipeGuardLogForFixture(t, db, guardGateEventsTable, "")
			wipeGuardLogForFixture(t, db, guardReceiptsTable, "")
			wipeGuardLogForFixture(t, db, guardInventoryEventsTable, "")
			seedGuardHistoricalEdition(t, db, dia, edge.From)
			seedGuardPredecessorReady(t, db, dia, edge.From, mode)
			inventoryBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable)
			receiptsBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable)
			gatesBefore := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable)

			migration := coreDirectoryMigration(dia, coreDescriptors(), guardEditionTwoMigrationExec(dia, current))
			err = migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{migration})
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("v7 over %s K2 attestation = %v, want ErrGuardManifestNoEdge", mode, err)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardInventoryEventsTable); got != inventoryBefore {
				t.Fatalf("refused v7 changed inventory %d -> %d", inventoryBefore, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receiptsBefore {
				t.Fatalf("refused v7 changed receipts %d -> %d", receiptsBefore, got)
			}
			if got := countRows(t, db, "SELECT COUNT(*) FROM "+guardGateEventsTable); got != gatesBefore {
				t.Fatalf("refused v7 changed gates %d -> %d", gatesBefore, got)
			}
			if got := countRows(t, db, dia.Rebind(
				"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = ?"), coreDirectoryMigrationVersion); got != 0 {
				t.Fatalf("refused v7 wrote %d tracking rows", got)
			}
			assertCoreDirectoryExistence(t, db, dia, map[string]bool{
				"core_directory_epoch": false, "core_directory_tombstone": false, "core_user_tombstone": false,
			})
		})
	}
}

func TestGuardEditionTransitionMakesK2BinariesFailClosed(t *testing.T) {
	current, edge := guardEditionFixture(t)
	db, dia := guardHistoricalEditionSQLiteDB(t, edge.From)
	ctx := context.Background()
	if err := verifyK2SQLiteBootstrapReceipts(ctx, db, dia, edge.From); err != nil {
		t.Fatalf("K2 SQLite helper rejected its own predecessor: %v", err)
	}
	reachedLatest := false
	if err := verifyK2PostgresOpenOrder(ctx, db, dia, edge.From, func() error {
		reachedLatest = true
		_, err := latestRecordedEdition(ctx, db, dia)
		return err
	}); err != nil || !reachedLatest {
		t.Fatalf("K2 PostgreSQL order rejected predecessor before latest gate: reached=%v err=%v", reachedLatest, err)
	}
	if err := runGuardEditionTwoMigration(ctx, db, dia, current); err != nil {
		t.Fatal(err)
	}
	if err := verifyK2SQLiteBootstrapReceipts(ctx, db, dia, edge.From); !errors.Is(err, ErrGuardBootstrapReceiptsInvalid) {
		t.Fatalf("K2 SQLite helper over seal = %v, want ErrGuardBootstrapReceiptsInvalid", err)
	}
	reachedLatest = false
	err := verifyK2PostgresOpenOrder(ctx, db, dia, edge.From, func() error {
		reachedLatest = true
		return nil
	})
	if !errors.Is(err, ErrGuardInventoryUnsupported) {
		t.Fatalf("K2 PostgreSQL inventory-first helper = %v, want ErrGuardInventoryUnsupported", err)
	}
	if reachedLatest {
		t.Fatal("K2 PostgreSQL helper consulted the gate after the epoch-2 inventory had already failed")
	}
}

func TestGuardEditionCorrelationRejectsEveryCrossProduct(t *testing.T) {
	receipts := []guardEditionReceiptState{
		guardEditionReceiptsCurrent,
		guardEditionReceiptsCurrentCompleted,
		guardEditionReceiptsDirectStarted,
		guardEditionReceiptsDirectCompleted,
		guardEditionReceiptsPredecessor,
		guardEditionReceiptsSealed,
	}
	inventories := []guardEditionInventoryState{
		guardEditionInventoryCurrent,
		guardEditionInventoryPredecessor,
		guardEditionInventoryMixed,
	}
	gates := []guardEditionGateState{
		guardEditionGateAbsent,
		guardEditionGateCurrent,
		guardEditionGatePredecessor,
	}
	key := func(r guardEditionReceiptState, i guardEditionInventoryState, g guardEditionGateState) string {
		return string(r) + "|" + string(i) + "|" + string(g)
	}
	allowed := map[store.Engine]map[string]guardEditionHistoryKind{
		store.EngineSQLite: {
			key(guardEditionReceiptsCurrent, guardEditionInventoryCurrent, guardEditionGateAbsent):          guardEditionHistoryCurrent,
			key(guardEditionReceiptsCurrentCompleted, guardEditionInventoryCurrent, guardEditionGateAbsent): guardEditionHistoryCurrentCompleted,
			key(guardEditionReceiptsDirectStarted, guardEditionInventoryCurrent, guardEditionGateAbsent):    guardEditionHistoryDirectStarted,
			key(guardEditionReceiptsDirectCompleted, guardEditionInventoryCurrent, guardEditionGateAbsent):  guardEditionHistoryDirectCompleted,
			key(guardEditionReceiptsPredecessor, guardEditionInventoryPredecessor, guardEditionGateAbsent):  guardEditionHistoryPredecessor,
			key(guardEditionReceiptsSealed, guardEditionInventoryMixed, guardEditionGateAbsent):             guardEditionHistoryTransitioned,
		},
		store.EnginePostgres: {
			key(guardEditionReceiptsCurrent, guardEditionInventoryCurrent, guardEditionGateAbsent):              guardEditionHistoryCurrent,
			key(guardEditionReceiptsCurrentCompleted, guardEditionInventoryCurrent, guardEditionGateAbsent):     guardEditionHistoryCurrentCompleted,
			key(guardEditionReceiptsCurrentCompleted, guardEditionInventoryCurrent, guardEditionGateCurrent):    guardEditionHistoryCurrentCompleted,
			key(guardEditionReceiptsDirectStarted, guardEditionInventoryCurrent, guardEditionGateAbsent):        guardEditionHistoryDirectStarted,
			key(guardEditionReceiptsDirectCompleted, guardEditionInventoryCurrent, guardEditionGateAbsent):      guardEditionHistoryDirectCompleted,
			key(guardEditionReceiptsDirectCompleted, guardEditionInventoryCurrent, guardEditionGateCurrent):     guardEditionHistoryDirectCompleted,
			key(guardEditionReceiptsPredecessor, guardEditionInventoryPredecessor, guardEditionGatePredecessor): guardEditionHistoryPredecessor,
			key(guardEditionReceiptsSealed, guardEditionInventoryMixed, guardEditionGateCurrent):                guardEditionHistoryTransitioned,
		},
	}
	for engine, accepted := range allowed {
		for _, receiptState := range receipts {
			for _, inventoryState := range inventories {
				for _, gateState := range gates {
					got, err := correlateGuardEditionHistory(engine, receiptState, inventoryState, gateState)
					want, ok := accepted[key(receiptState, inventoryState, gateState)]
					if ok {
						if err != nil || got != want {
							t.Errorf("%s accepted triple %s/%s/%s = %s, %v; want %s",
								engine, receiptState, inventoryState, gateState, got, err, want)
						}
						continue
					}
					if !errors.Is(err, ErrGuardManifestNoEdge) {
						t.Errorf("%s cross-product %s/%s/%s = %s, %v; want ErrGuardManifestNoEdge",
							engine, receiptState, inventoryState, gateState, got, err)
					}
				}
			}
		}
	}
}

func TestGuardEditionV7StartCorrelationRejectsEveryCrossProduct(t *testing.T) {
	initials := []coreDirectoryInitialDisposition{
		coreDirectoryInitiallyAbsent,
		coreDirectoryInitiallyPresent,
	}
	histories := []guardEditionHistoryKind{
		guardEditionHistoryCurrent,
		guardEditionHistoryCurrentCompleted,
		guardEditionHistoryDirectStarted,
		guardEditionHistoryDirectCompleted,
		guardEditionHistoryPredecessor,
		guardEditionHistoryTransitioned,
	}
	type start struct {
		initial coreDirectoryInitialDisposition
		history guardEditionHistoryKind
	}
	allowed := map[start]guardEditionMigrationAction{
		{coreDirectoryInitiallyPresent, guardEditionHistoryCurrent}:      guardEditionMigrationCompleteV7,
		{coreDirectoryInitiallyAbsent, guardEditionHistoryDirectStarted}: guardEditionMigrationCompleteV7,
		{coreDirectoryInitiallyAbsent, guardEditionHistoryPredecessor}:   guardEditionMigrationTransition,
	}
	for _, initial := range initials {
		for _, history := range histories {
			got, err := correlateGuardEditionMigrationStart(initial, history)
			want, ok := allowed[start{initial, history}]
			if ok {
				if err != nil || got != want {
					t.Errorf("v7 start %s/%s = %s, %v; want %s", initial, history, got, err, want)
				}
				continue
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Errorf("v7 cross-product %s/%s = %s, %v; want ErrGuardManifestNoEdge",
					initial, history, got, err)
			}
		}
	}
}

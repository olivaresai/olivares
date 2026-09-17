// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// access_evidence_migration_pg_test.go is the REAL-ENGINE half of the access-evidence
// upgrade, and it is a separate file because a PostgreSQL claim cannot be made on SQLite.
//
// The two engines do genuinely different things here: PostgreSQL has the gate, the unit
// runner, the fenced close and the owner/application split, so it is the only engine on
// which "the predecessor rollout was still pending" is even representable. Every test
// below runs against a real server through the production Open, with a separate owner and
// application DSN, and none of them wraps a SQLite fixture to stand in for one.

func accessEvidencePGStore(t *testing.T) (store.Config, *sql.DB, dialect.Dialect) {
	t.Helper()
	dsns := isolatedPGSplit(t)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}
	return cfg, guardPGProbe(t, dsns.Owner), dia
}

func dropAccessEvidenceTables(t *testing.T, owner *sql.DB) {
	t.Helper()
	for _, table := range accessEvidenceRelationNames {
		if _, err := owner.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}

// rewindPostgresToK2 reconstructs the durable shape of a real pre-directory, pre-access
// evidence deployment on a server whose schema the current binary has just created.
//
// It removes what a K2 source never had — the User authority relation and its routines,
// the directory relations, the writer control and its source guards, the access-evidence
// relations and the v7/v8/v9/v10 tracking rows — and reseeds the epoch-1 guard history
// with a CLOSED epoch-1 rollout, which is what such a deployment's own boot left behind.
// The current tables stay, exactly as an in-place upgrade would find them.
//
// Undo v10 before replacing the control its guards read, then remove the later
// access-evidence and lineage state as well as v7. These are disposable fixture DDL
// steps, not a production rollback or a deployable checkpoint after each statement.
func rewindPostgresToK2(t *testing.T, owner *sql.DB, dia dialect.Dialect, edition1 guardManifest) {
	t.Helper()
	ctx := context.Background()
	dropUserAuthorityForHistoricalFixture(t, owner, dia)
	dropAccessEvidenceTables(t, owner)
	// v7 authored the writer guards as well as the control. Dropping the control while
	// its triggers survive would leave every directory source unwritable — a state no K2
	// database was ever in, since neither object existed there.
	dropDirectoryWriterGuardsForFixture(t, owner, dia)
	dropCoreDirectoryTables(t, owner)
	// The v8 lineage relations go with their tracking row. Removing the row alone would
	// stage a database whose migration history says v8 never ran while its objects say it
	// did, and v8 would then fail on CREATE TABLE rather than on anything this fixture is
	// about. dropLineageForHistoricalFixture removes both.
	dropLineageForHistoricalFixture(t, owner, dia)
	for _, version := range []int{
		coreDirectoryMigrationVersion, coreAccessEvidenceMigrationVersion,
	} {
		if _, err := owner.ExecContext(ctx, dia.Rebind(
			"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"), version); err != nil {
			t.Fatalf("remove the v%d tracking row: %v", version, err)
		}
	}
	wipeGuardLogForFixture(t, owner, guardGateEventsTable, "")
	wipeGuardLogForFixture(t, owner, guardReceiptsTable, "")
	wipeGuardLogForFixture(t, owner, guardInventoryEventsTable, "")
	seedGuardHistoricalEdition(t, owner, dia, edition1)
	seedGuardPredecessorReady(t, owner, dia, edition1, guardPredecessorComplete)
}

func accessEvidenceCoreOnlyGraph(t *testing.T) guardEditionGraph {
	t.Helper()
	graph, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

// TestAccessEvidencePostgresK2CrossesAPendingPredecessor is amendment R2-G1's acceptance
// case, run against a real server with a real owner/application split.
//
// THE SEQUENCE IT MEASURES, in the order the amendment names it: a K2-shaped database
// commits core v7, which crosses the directory edge E1 -> E2 and leaves that edge's own
// rollout PENDING; the process stops there; a NEW session reopens; the predecessor
// barrier runs E2's units and its fenced close; and only then does core v9 cross the
// access-evidence edge E2 -> E5 in its own transaction.
//
// The assertions at the stop are as load-bearing as the ones at the end. "v7 completed"
// and "the E2 rollout is ready" are different facts, and the point of the amendment is
// that the first does not imply the second — so the test proves the database really is
// standing on a pending predecessor, with no access-evidence relation and no v9 tracking
// row, before it lets the reopen continue.
func TestAccessEvidencePostgresK2CrossesAPendingPredecessor(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	graph := accessEvidenceCoreOnlyGraph(t)
	edition1, _ := graph.node(1)
	edition2, _ := graph.node(2)
	edition5, _ := graph.node(5)

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("build the current schema: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Rows a real deployment would already hold, seeded through the owner before the
	// rewind so they predate every step this test measures.
	seedPostgresTenantOrgs(t, owner, dia)
	before := postgresOrgCensus(t, owner)

	rewindPostgresToK2(t, owner, dia, edition1.Manifest)

	// STEP 1: commit core v7 and stop. This is the exact crash boundary the amendment is
	// about — the transition, its seal and its PENDING gate commit together.
	migrations := buildCoreMigrations(
		dia, coreDescriptors(),
		guardBootstrapExec(dia, edition5.Manifest),
		guardEditionTwoMigrationExec(dia, edition5.Manifest),
	)
	if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, migrations); err != nil {
		t.Fatalf("commit core v7 over the K2 chain: %v", err)
	}
	pending, err := verifyGuardEditionHistory(ctx, owner, dia, edition2.Manifest)
	if err != nil {
		t.Fatalf("read the v7 crash boundary: %v", err)
	}
	if pending.Kind != guardEditionHistoryTransitioned ||
		pending.GateState != guardEditionGateCurrent ||
		pending.Gate.Phase != gatePhasePending || pending.Gate.Condition != gateConditionClean {
		t.Fatalf("v7 boundary = %s gate %s/%s/%s, want transitioned with a clean pending gate",
			pending.Kind, pending.GateState, pending.Gate.Phase, pending.Gate.Condition)
	}
	// THE PREDECESSOR IS PENDING AND THE EDGE HAS NOT BEEN CROSSED. Both halves matter:
	// the first is what the amendment exists for, the second is what it must not skip.
	if _, err := verifyGuardEditionHistory(ctx, owner, dia, edition5.Manifest); err == nil {
		t.Fatal("edition 5 verified while its predecessor's rollout was still pending")
	} else if !errors.Is(err, ErrGuardManifestNoEdge) {
		t.Fatalf("edition-5 reading over a pending predecessor = %v, want ErrGuardManifestNoEdge", err)
	}
	if got := postgresRelationCount(t, owner, accessEvidenceRelationNames[:]); got != 0 {
		t.Fatalf("%d access-evidence relations exist before v9", got)
	}
	if got := countRows(t, owner, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
		coreAccessEvidenceMigrationVersion); got != 0 {
		t.Fatalf("v9 tracking rows before the barrier = %d, want none", got)
	}

	// STEP 2: a NEW session reopens. The barrier converges the pending E2 rollout through
	// the ordinary coordinator, and v9 then crosses its edge.
	upgraded, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("reopen over a pending predecessor: %v", err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}

	final, err := verifyGuardEditionHistory(ctx, owner, dia, edition5.Manifest)
	if err != nil {
		t.Fatalf("verify the completed chain: %v", err)
	}
	if final.Kind != guardEditionHistoryTransitioned || final.Path != "1>2>5" ||
		final.ParentEpoch != 2 || !final.CompletedV9 ||
		final.GateState != guardEditionGateCurrent ||
		final.Gate.Phase != gatePhaseReady || final.Gate.Condition != gateConditionVerified {
		t.Fatalf("final history = %s/path-%s parent %d completedV9=%t gate %s/%s/%s",
			final.Kind, final.Path, final.ParentEpoch, final.CompletedV9,
			final.GateState, final.Gate.Phase, final.Gate.Condition)
	}
	// EVERY rollout on the lineage is closed, including the one the barrier converged.
	for _, node := range []guardEditionNode{edition1, edition2, edition5} {
		empty, derr := emptyRetainedDigest()
		if derr != nil {
			t.Fatal(derr)
		}
		rollout, rerr := guardBootstrapRollout(node.Manifest, 0, empty)
		if rerr != nil {
			t.Fatal(rerr)
		}
		gate, gerr := foldGateEvents(ctx, owner, dia, rollout.RolloutID)
		if gerr != nil || !gate.Found ||
			gate.Phase != gatePhaseReady || gate.Condition != gateConditionVerified {
			t.Fatalf("edition-%d gate = found:%v %s/%s err:%v",
				node.Epoch, gate.Found, gate.Phase, gate.Condition, gerr)
		}
	}
	if got := countRows(t, owner, "SELECT COUNT(*) FROM "+guardReceiptsTable+
		" WHERE receipt_kind='bootstrap'"); got != 5 {
		t.Fatalf("bootstrap receipts = %d, want the K2 three plus the 1->2 and 2->5 seals", got)
	}
	if err := verifyAccessEvidenceRelationsExact(ctx, owner, dia, coreDescriptors()); err != nil {
		t.Fatalf("upgraded access-evidence relations: %v", err)
	}
	if got := postgresOrgCensus(t, owner); len(got) != len(before) {
		t.Fatalf("the upgrade changed the preexisting row count %d -> %d", len(before), len(got))
	} else {
		for i := range before {
			if before[i] != got[i] {
				t.Fatalf("the upgrade changed row %d: %q -> %q", i, before[i], got[i])
			}
		}
	}
}

// TestAccessEvidencePostgresBarrierRefusesFabricatedReadiness is the amendment's other
// half: the barrier admits the pending gate the recorded history wrote, and nothing else.
//
// A LONE READY LABEL IS NOT EVIDENCE, and the two cases below are the two ways that can be
// true. The first is a DURABLE one: the gate relation's own CHECK makes a `ready` row
// without its four closing-checkpoint columns unrepresentable, so the cheapest forgery
// cannot even be written. The second is the one that needed a verifier: a ready row whose
// checkpoint IS present and does not describe the stream it claims to close.
func TestAccessEvidencePostgresBarrierRefusesFabricatedReadiness(t *testing.T) {
	t.Run("the ledger will not store a ready event without its checkpoint", func(t *testing.T) {
		ctx := context.Background()
		cfg, owner, dia := accessEvidencePGStore(t)
		graph := accessEvidenceCoreOnlyGraph(t)
		edition5, _ := graph.node(5)
		// The store is opened only so a real control plane exists to write into.
		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		empty, err := emptyRetainedDigest()
		if err != nil {
			t.Fatal(err)
		}
		rollout, err := guardBootstrapRollout(edition5.Manifest, 0, empty)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		_, err = appendGateEvent(ctx, tx, dia, gateEvent{
			RolloutID:        rollout.RolloutID,
			Kind:             gateEventReady,
			Format:           rollout.Format,
			CodeEpoch:        rollout.CodeEpoch,
			CodeSHA256:       rollout.CodeSHA256,
			RetainedRevision: rollout.RetainedRevision,
			RetainedSHA256:   rollout.RetainedSHA256,
			Phase:            gatePhaseReady,
			Condition:        gateConditionVerified,
		})
		if err == nil {
			t.Fatal("the gate relation stored a ready event with no closing checkpoint")
		}
		if !strings.Contains(err.Error(), "violates check constraint") {
			t.Fatalf("refusal = %v, want the relation's own CHECK", err)
		}
	})

	t.Run("a ready checkpoint that does not describe its stream", func(t *testing.T) {
		ctx := context.Background()
		cfg, owner, dia := accessEvidencePGStore(t)
		graph := accessEvidenceCoreOnlyGraph(t)
		edition1, _ := graph.node(1)
		edition2, _ := graph.node(2)
		edition5, _ := graph.node(5)

		st, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		rewindPostgresToK2(t, owner, dia, edition1.Manifest)
		migrations := buildCoreMigrations(
			dia, coreDescriptors(),
			guardBootstrapExec(dia, edition5.Manifest),
			guardEditionTwoMigrationExec(dia, edition5.Manifest),
		)
		if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, migrations); err != nil {
			t.Fatalf("commit core v7: %v", err)
		}
		pending, err := verifyGuardEditionHistory(ctx, owner, dia, edition2.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		empty, err := emptyRetainedDigest()
		if err != nil {
			t.Fatal(err)
		}
		rollout, err := guardBootstrapRollout(edition2.Manifest, 0, empty)
		if err != nil {
			t.Fatal(err)
		}
		// A ready row with a well-formed but UNTRUE checkpoint: the counts are the ones
		// a closed rollout would attest, over heads that are not the stream's.
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var forged [32]byte
		for i := range forged {
			forged[i] = 0xAB
		}
		if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
			RolloutID:         rollout.RolloutID,
			Kind:              gateEventReady,
			Format:            rollout.Format,
			CodeEpoch:         rollout.CodeEpoch,
			CodeSHA256:        rollout.CodeSHA256,
			RetainedRevision:  rollout.RetainedRevision,
			RetainedSHA256:    rollout.RetainedSHA256,
			Phase:             gatePhaseReady,
			Condition:         gateConditionVerified,
			ExpectedUnits:     pending.Gate.ExpectedUnits,
			CheckpointPresent: true,
			Checkpoint: guardCheckpoint{
				InventoryHead:  forged,
				InventoryCount: int64(len(edition2.Manifest.Specs)),
				ReceiptHead:    forged,
				ReceiptCount:   4,
			},
		}); err != nil {
			_ = tx.Rollback()
			t.Fatalf("append the forged ready event: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		receipts := countRows(t, owner, "SELECT COUNT(*) FROM "+guardReceiptsTable)
		opened, err := Open(ctx, cfg, nil)
		if err == nil {
			_ = opened.Close()
			t.Fatal("Open crossed the access-evidence edge over a forged predecessor checkpoint")
		}
		if got := postgresRelationCount(t, owner, accessEvidenceRelationNames[:]); got != 0 {
			t.Fatalf("the refused Open created %d access-evidence relations", got)
		}
		if got := countRows(t, owner, dia.Rebind(
			"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
			coreAccessEvidenceMigrationVersion); got != 0 {
			t.Fatalf("the refused Open recorded %d v9 tracking rows", got)
		}
		if got := countRows(t, owner, "SELECT COUNT(*) FROM "+guardReceiptsTable); got != receipts {
			t.Fatalf("the refused Open changed receipts %d -> %d", receipts, got)
		}
	})
}

// TestAccessEvidencePostgresFreshAndDirectNeverConvergeAbsentRelations proves the fourth
// clause of the amendment: the plans that cross no edge keep core v9 BEFORE their target
// units, and the barrier does not try to converge a rollout over relations that do not
// exist yet.
func TestAccessEvidencePostgresFreshAndDirectNeverConvergeAbsentRelations(t *testing.T) {
	for _, tc := range []struct {
		name      string
		seed      func(*testing.T, *sql.DB, dialect.Dialect)
		wantClass accessEvidenceStartClass
		wantKind  guardEditionHistoryKind
	}{
		{
			name:      "fresh",
			seed:      func(*testing.T, *sql.DB, dialect.Dialect) {},
			wantClass: accessEvidenceStartFreshEmpty,
			wantKind:  guardEditionHistoryCurrentV9Completed,
		},
		{
			name: "direct from a legacy pre-v6 source",
			seed: func(t *testing.T, owner *sql.DB, dia dialect.Dialect) {
				seedCoreV5(t, owner, dia, true)
			},
			wantClass: accessEvidenceStartLegacyPreV6,
			wantKind:  guardEditionHistoryDirectV9Completed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			tc.seed(t, owner, dia)

			// The classifier's own answer, read before anything migrates, is asserted
			// directly: it is what decides that the barrier is a no-op here.
			graph := accessEvidenceCoreOnlyGraph(t)
			plan, err := classifyAccessEvidenceBoot(ctx, owner, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil,
				guardEventFenceFacts{})
			if err != nil {
				t.Fatalf("classify %s start: %v", tc.name, err)
			}
			if plan.Class != tc.wantClass {
				t.Fatalf("start class = %s, want %s", plan.Class, tc.wantClass)
			}
			if plan.Action == accessEvidenceV9GuardedTransition {
				t.Fatalf("a %s start planned a guarded transition", tc.name)
			}
			if err := convergeGuardEditionPredecessorForV9(ctx, owner, dia, graph, plan,
				"", false, nil); err != nil {
				t.Fatalf("the barrier acted on a %s plan: %v", tc.name, err)
			}

			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("%s Open: %v", tc.name, err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			history, err := verifyGuardEditionHistory(ctx, owner, dia, coreOnlyAccessEvidenceManifest(t))
			if err != nil {
				t.Fatal(err)
			}
			if history.Kind != tc.wantKind || !history.CompletedV9 {
				t.Fatalf("%s history = %s (completedV9=%t), want %s",
					tc.name, history.Kind, history.CompletedV9, tc.wantKind)
			}
			if history.Gate.Phase != gatePhaseReady || history.Gate.Condition != gateConditionVerified {
				t.Fatalf("%s gate = %s/%s, want ready/verified",
					tc.name, history.Gate.Phase, history.Gate.Condition)
			}
			if err := verifyAccessEvidenceRelationsExact(ctx, owner, dia, coreDescriptors()); err != nil {
				t.Fatalf("%s access-evidence relations: %v", tc.name, err)
			}
		})
	}
}

// TestAccessEvidencePostgresApplicationRoleCannotMutate is section 10.2 case 12 on the
// engine where the question has two different answers.
//
// The OWNER may migrate; that is what the whole upgrade depends on. The APPLICATION role
// must still be unable to update, delete or truncate the evidence, and only a check asked
// through the application pool can see privileges held through a group role or PUBLIC.
// Owner authority to migrate is not runtime application authority.
func TestAccessEvidencePostgresApplicationRoleCannotMutate(t *testing.T) {
	ctx := context.Background()
	cfg, owner, dia := accessEvidencePGStore(t)
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	_ = dia
	app := guardPGProbe(t, cfg.DSN)
	for _, table := range accessEvidenceRelationNames {
		for _, stmt := range []string{
			"UPDATE " + table + " SET tenant_id = tenant_id",
			"DELETE FROM " + table,
			"TRUNCATE " + table,
		} {
			if _, err := app.ExecContext(ctx, stmt); err == nil {
				t.Fatalf("the application role executed %q", stmt)
			}
		}
		// And the guard itself is ALWAYS, which is the state a logical-replication
		// apply cannot walk through.
		if state := guardEnableStates(t, owner)[table]; state != guardStateAlways {
			t.Fatalf("%s guard is %q, want %q", table, state, guardStateAlways)
		}
	}
}

// seedPostgresTenantOrgs writes one ordinary row per tenant BEFORE the rewind, so the
// upgrade below has real data to preserve.
//
// Each insert binds its tenant inside its own transaction. That is not ceremony: the
// FORCE-RLS policy calls current_setting('app.tenant_id') WITHOUT missing_ok on purpose,
// so an unbound session RAISES rather than silently matching nothing — and a fixture that
// bound the empty tenant would trade a loud failure for a silent one.
// accessEvidencePGFixtureTenants are canonical UUIDv7 values, and each seeded
// organization's id IS its tenant id. Both are directory invariants the boot enumeration
// enforces, so a fixture that ignored either would fail the upgrade for a reason that has
// nothing to do with it.
var accessEvidencePGFixtureTenants = []string{
	"01920000-0000-7000-8000-0000000000a1",
	"01920000-0000-7000-8000-0000000000b2",
}

func seedPostgresTenantOrgs(t *testing.T, owner *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	for i, tenant := range accessEvidencePGFixtureTenants {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant); err != nil {
			_ = tx.Rollback()
			t.Fatalf("bind tenant %d: %v", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, dia.Rebind(
			"INSERT INTO orgs (id, tenant_id, created_at, updated_at, version, name, slug, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
			tenant, tenant,
			"2026-09-06T00:00:00Z", "2026-09-06T00:00:00Z", 1,
			fmt.Sprintf("legacy-org-%d", i+1), fmt.Sprintf("legacy-org-%d", i+1), "active"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed a preexisting row for tenant %d: %v", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit the preexisting row for tenant %d: %v", i+1, err)
		}
	}
}

// postgresOrgCensus reads the seeded rows back per tenant, binding each read the same way
// the seed bound its write.
func postgresOrgCensus(t *testing.T, owner *sql.DB) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	for _, tenant := range accessEvidencePGFixtureTenants {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id', $1, true)", tenant); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		rows, err := tx.QueryContext(ctx,
			"SELECT tenant_id, id, name, version FROM orgs ORDER BY tenant_id, id")
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for rows.Next() {
			var tenantID, id, name string
			var version int64
			if err := rows.Scan(&tenantID, &id, &name, &version); err != nil {
				_ = rows.Close()
				_ = tx.Rollback()
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s|%s|%s|%d", tenantID, id, name, version))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			t.Fatal(err)
		}
		_ = rows.Close()
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func postgresRelationCount(t *testing.T, owner *sql.DB, tables []string) int {
	t.Helper()
	present := 0
	for _, table := range tables {
		present += countRows(t, owner, `SELECT COUNT(*) FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table)
	}
	return present
}

// --- The corrected start classification, on a real server with split roles ---------------
//
// The classification is engine-independent in its TABLE and engine-specific in its
// EVIDENCE: on PostgreSQL the exact contract of a relation is a deparsed catalog
// projection compared against a probe rendered from the same descriptor on the same
// server, and the append-only guard is one `<table>_immutable` trigger rather than
// SQLite's stored-text pair. These cases therefore repeat the SQLite ones rather than
// standing in for them, and they run through the production Open with a separate owner
// DSN so the reads happen as the role that would have done the migrating.

// TestAccessEvidencePostgresLegacyProfileMustNotSilentlyExpand is review finding F1 on the
// engine where the module census also has an ACL consequence.
func TestAccessEvidencePostgresLegacyProfileMustNotSilentlyExpand(t *testing.T) {
	for _, addModule := range []bool{false, true} {
		t.Run(fmt.Sprintf("additional-append-only-module-%t", addModule), func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			seedCoreV5(t, owner, dia, false)
			var register func(store.ExtensionRegistry) error
			if addModule {
				register = func(reg store.ExtensionRegistry) error {
					desc := widgetDescriptor
					desc.Kind = "review.legacy_event"
					desc.Table = "review_legacy_event"
					desc.AppendOnly = true
					return reg.Register(desc)
				}
			}
			beforeMax := postgresMaxCoreVersion(t, owner, dia)

			st, err := Open(ctx, cfg, register)
			if st != nil {
				_ = st.Close()
			}
			moduleRelations := postgresRelationCount(t, owner, []string{"review_legacy_event"})
			v9Rows := countRows(t, owner, dia.Rebind(
				"SELECT COUNT(*) FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
				coreAccessEvidenceMigrationVersion)
			t.Logf("PG_LEGACY_PROFILE|additional_module=%t|open_error=%v|v9_rows=%d|module_relations=%d",
				addModule, err, v9Rows, moduleRelations)

			if !addModule {
				if err != nil {
					t.Fatalf("control: the ratified core-only shape failed on PostgreSQL: %v", err)
				}
				if v9Rows != 1 {
					t.Fatalf("control: v9 tracking rows = %d, want 1", v9Rows)
				}
				return
			}
			if err == nil {
				t.Fatal("PostgreSQL accepted a new append-only module census from the direct <=v5 bootstrap")
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) {
				t.Fatalf("refusal = %v, want ErrGuardManifestNoEdge", err)
			}
			if !strings.Contains(err.Error(), "review_legacy_event") {
				t.Fatalf("the refusal does not name the relation it refused to activate: %v", err)
			}
			if moduleRelations != 0 {
				t.Fatal("the refused Open created the module relation anyway")
			}
			if v9Rows != 0 {
				t.Fatalf("the refused Open recorded %d v9 tracking rows", v9Rows)
			}
			if got := postgresMaxCoreVersion(t, owner, dia); got != beforeMax {
				t.Fatalf("the refused Open advanced the core history from v%d to v%d", beforeMax, got)
			}
		})
	}
}

// TestAccessEvidencePostgresStartShapeRefusesDamageBeforeAnyMigration is review finding F2
// on PostgreSQL, where "the exact contract" is a catalog projection rather than stored text.
//
// The damage is the append-only guard this engine actually installs — one
// `<table>_immutable` trigger — and removing it is what a forger would do to make an
// evidence relation writable while leaving it looking complete to a shallow check.
func TestAccessEvidencePostgresStartShapeRefusesDamageBeforeAnyMigration(t *testing.T) {
	for _, damage := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-append-only-guard-%t", damage), func(t *testing.T) {
			ctx := context.Background()
			cfg, owner, dia := accessEvidencePGStore(t)
			migrations := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
			if err := migrate.Apply(ctx, owner, dia, coreTrackingTable, migrations[:2]); err != nil {
				t.Fatal(err)
			}
			guard := policyArtifactTable + guardTriggerSuffix
			if damage {
				if _, err := owner.ExecContext(ctx,
					"DROP TRIGGER "+guard+" ON "+policyArtifactTable); err != nil {
					t.Fatalf("remove the append-only guard: %v", err)
				}
			}
			graph, err := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
			if err != nil {
				t.Fatal(err)
			}
			plan, classErr := classifyAccessEvidenceBoot(
				ctx, owner, dia, graph, coreDescriptors(), coreOnlyRegistry(t), nil, guardEventFenceFacts{})
			st, openErr := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			maxCore := postgresMaxCoreVersion(t, owner, dia)
			guards := countRows(t, owner, `SELECT COUNT(*) FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND t.tgname = $3 AND NOT t.tgisinternal`,
				dialect.EngineSchema, policyArtifactTable, guard)
			t.Logf("PG_FRESH_V2_DAMAGE|damaged=%t|class=%s|classification_error=%v|open_error=%v|max_core=%d|guards=%d",
				damage, plan.Class, classErr, openErr, maxCore, guards)

			if !damage {
				if classErr != nil || openErr != nil || maxCore != coreSupportedMigrationVersion {
					t.Fatalf("control: the clean current-v2 checkpoint failed: class=%v open=%v max=%d, want max=%d",
						classErr, openErr, maxCore, coreSupportedMigrationVersion)
				}
				return
			}
			if classErr == nil {
				t.Fatal("PostgreSQL classified a current-v2 checkpoint with its append-only guard removed without error")
			}
			if !errors.Is(classErr, ErrGuardManifestNoEdge) {
				t.Fatalf("classification refusal = %v, want ErrGuardManifestNoEdge", classErr)
			}
			if openErr == nil {
				t.Fatal("Open accepted a damaged current-v2 checkpoint")
			}
			if maxCore != 2 {
				t.Fatalf("the refused boot advanced the core history from v2 to v%d", maxCore)
			}
			if guards != 0 {
				t.Fatal("the refused Open recreated the guard it refused over")
			}
			// THE APPLICATION ROLE IS STILL WHERE THE REFUSAL LEFT IT. A boot that
			// refused must not have granted anything on the way out, and only a check
			// asked AS that role sees privileges held through a group or PUBLIC.
			app := guardPGProbe(t, cfg.DSN)
			var canWrite bool
			if err := app.QueryRowContext(ctx,
				"SELECT has_table_privilege(current_user, $1, 'UPDATE')", policyArtifactTable).Scan(&canWrite); err != nil {
				t.Fatalf("ask the application role for its own privilege: %v", err)
			}
			if canWrite {
				t.Fatal("the refused boot left the application role able to UPDATE an access-evidence relation")
			}
		})
	}
}

// postgresMaxCoreVersion is the highest recorded core migration, or 0 when the tracker has
// no rows. It is the measurement "the refusal committed no migration" is made with.
func postgresMaxCoreVersion(t *testing.T, owner *sql.DB, dia dialect.Dialect) int {
	t.Helper()
	var max sql.NullInt64
	if err := owner.QueryRowContext(context.Background(),
		"SELECT MAX(version) FROM "+coreTrackingRelation(dia)).Scan(&max); err != nil {
		t.Fatalf("read the core tracker: %v", err)
	}
	if !max.Valid {
		return 0
	}
	return int(max.Int64)
}

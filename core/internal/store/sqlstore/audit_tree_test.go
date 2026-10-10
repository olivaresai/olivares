// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/tlog"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// rfc6962Root is the RFC 6962 section 2.1 Merkle Tree Hash written straight from
// the RFC, with no tlog call, so the engine's stored tree is checked against the
// definition and not against its own library.
func rfc6962Root(leaves [][]byte) []byte {
	switch len(leaves) {
	case 0:
		h := sha256.Sum256(nil)
		return h[:]
	case 1:
		h := sha256.Sum256(append([]byte{0x00}, leaves[0]...))
		return h[:]
	}
	k := 1
	for k*2 < len(leaves) {
		k *= 2
	}
	l, r := rfc6962Root(leaves[:k]), rfc6962Root(leaves[k:])
	h := sha256.Sum256(append(append([]byte{0x01}, l...), r...))
	return h[:]
}

func treeReader(t *testing.T, sc store.Scope) store.AuditTreeReader {
	t.Helper()
	tr, ok := sc.Audit().(store.AuditTreeReader)
	if !ok {
		t.Fatalf("%T does not implement store.AuditTreeReader", sc.Audit())
	}
	return tr
}

// requireTreeMatchesLedger checks the stored tree against the ledger rows: the head
// is the RFC 6962 root of the event hashes and every event has an inclusion proof that
// verifies against it.
func requireTreeMatchesLedger(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	ctx := context.Background()
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		var seqs []int64
		var hashes [][]byte
		if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			seqs = append(seqs, ev.Seq)
			hashes = append(hashes, ev.Hash)
			return nil
		}); err != nil {
			return err
		}
		tr := treeReader(t, sc)
		head, err := tr.AuditTreeHead(ctx)
		if err != nil {
			return err
		}
		if head.Size != int64(len(hashes)) || !bytes.Equal(head.Root, rfc6962Root(hashes)) {
			t.Fatalf("tree head = size %d root %x, want size %d root %x",
				head.Size, head.Root, len(hashes), rfc6962Root(hashes))
		}
		var root tlog.Hash
		copy(root[:], head.Root)
		for i, seq := range seqs {
			inc, found, err := tr.AuditInclusionProof(ctx, seq, head.Size)
			if err != nil || !found {
				t.Fatalf("inclusion proof for seq %d: found=%v err=%v", seq, found, err)
			}
			if inc.LeafIndex != int64(i) {
				t.Fatalf("seq %d leaf index = %d, want %d", seq, inc.LeafIndex, i)
			}
			proof := make(tlog.RecordProof, len(inc.Proof))
			for j, p := range inc.Proof {
				copy(proof[j][:], p)
			}
			if err := tlog.CheckRecord(proof, head.Size, root, inc.LeafIndex, tlog.RecordHash(hashes[i])); err != nil {
				t.Fatalf("inclusion proof for seq %d rejected: %v", seq, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditTreeFollowsEveryAppend(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree")
	appendN(t, st, tenant, 20)
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeRollsBackWithTheRow proves the tree is written in the audit row's own
// transaction: a Mutate that appends and then fails leaves neither.
func TestAuditTreeRollsBackWithTheRow(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-rollback")
	appendN(t, st, tenant, 3)
	before := len(readTenantAuditRows(t, st, tenant))
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:1", ActorKind: model.ActorUser, Action: "agent.update",
		}); err != nil {
			return err
		}
		return errors.New("abort after append")
	})
	if err == nil {
		t.Fatal("Mutate did not report the abort")
	}
	if got := len(readTenantAuditRows(t, st, tenant)); got != before {
		t.Fatalf("rolled-back append left %d events, want %d", got, before)
	}
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeCrossesDeclaredGap: an audit.gap marker leaves a hole in the sequence
// numbers and none in the tree, and a proof is still asked for by sequence.
func TestAuditTreeCrossesDeclaredGap(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "gap.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "tree-gap")
	appendN(t, initial, tenant, 2)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	degraded := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	appendDroppedEvents(t, degraded, tenant, 3)
	if err := degraded.Close(); err != nil {
		t.Fatal(err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: largeAuditSpoolBudget, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	appendN(t, st, tenant, 1)
	rows := readTenantAuditRows(t, st, tenant)
	if last := rows[len(rows)-1].ev.Seq; last != int64(len(rows))+3 {
		t.Fatalf("fixture has no hole: last seq %d for %d rows", last, len(rows))
	}
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeCompletesALedgerSealedBeforeTheTree simulates the 26.10.0 upgrade: the
// ledger has events and the tree table is empty. The next append completes it.
func TestAuditTreeCompletesALedgerSealedBeforeTheTree(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-legacy")
	appendN(t, st, tenant, 6)
	db := st.(*sqlStore).db
	if _, err := db.Exec("DROP TABLE " + dialect.AuditTreeTable); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range st.(*sqlStore).dia.AuditTreeStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	appendN(t, st, tenant, 1)
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeHeadCompletesALegacyLedgerWithoutAnAppend: `audit tree checkpoint` on a
// ledger sealed before the tree reads the head through a writable scope, which
// completes the tree itself; a read-only scope reports only what the tree has.
func TestAuditTreeHeadCompletesALegacyLedgerWithoutAnAppend(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-legacy-head")
	appendN(t, st, tenant, 5)
	db := st.(*sqlStore).db
	if _, err := db.Exec("DROP TABLE " + dialect.AuditTreeTable); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range st.(*sqlStore).dia.AuditTreeStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		head, err := treeReader(t, sc).AuditTreeHead(ctx)
		if err != nil {
			return err
		}
		t.Fatalf("a read-only scope reported a head of %d leaves for an incomplete tree", head.Size)
		return nil
	}); !errors.Is(err, store.ErrAuditTreeIncomplete) {
		t.Fatalf("read-only head over an incomplete tree: err = %v, want ErrAuditTreeIncomplete", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		head, err := treeReader(t, sc).AuditTreeHead(ctx)
		if err != nil {
			return err
		}
		if head.Size != 6 { // the org.create seeded at provision plus five
			t.Fatalf("a writable scope reports %d leaves, want all 6 events", head.Size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeHeadThroughCustodyCompletesALegacyLedger is the path `audit tree
// checkpoint` takes: custody, not the service door.
func TestAuditTreeHeadThroughCustodyCompletesALegacyLedger(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-custody")
	appendN(t, st, tenant, 4)
	db := st.(*sqlStore).db
	if _, err := db.Exec("DROP TABLE " + dialect.AuditTreeTable); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range st.(*sqlStore).dia.AuditTreeStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Custody(ctx, tenant, func(sc store.CustodyScope) error {
		tr, ok := sc.Audit().(store.AuditTreeReader)
		if !ok {
			t.Fatalf("%T is not a store.AuditTreeReader", sc.Audit())
		}
		head, err := tr.AuditTreeHead(ctx)
		if err != nil {
			return err
		}
		if head.Size != 5 {
			t.Fatalf("custody head has %d leaves, want all 5 events", head.Size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreeHeadIsTheRFCRootAtEverySize walks sizes 1..33 (every power-of-two edge).
func TestAuditTreeHeadIsTheRFCRootAtEverySize(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-sizes") // seeds leaf 1
	var hashes [][]byte
	for n := 1; n <= 33; n++ {
		if n > 1 {
			appendN(t, st, tenant, 1)
		}
		hashes = hashes[:0]
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
				hashes = append(hashes, ev.Hash)
				return nil
			}); err != nil {
				return err
			}
			head, err := treeReader(t, sc).AuditTreeHead(ctx)
			if err != nil {
				return err
			}
			if head.Size != int64(n) || !bytes.Equal(head.Root, rfc6962Root(hashes)) {
				t.Fatalf("size %d: head = %d %x, want %x", n, head.Size, head.Root, rfc6962Root(hashes))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestAuditTreeProofBounds: a proof at an earlier size is answered only for events
// inside that tree; impossible requests are refused.
func TestAuditTreeProofBounds(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-bounds")
	appendN(t, st, tenant, 8) // 9 leaves
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		tr := treeReader(t, sc)
		// seq 7 is leaf 6: in the tree of 9 and of 7, not in the tree of 6.
		if _, found, err := tr.AuditInclusionProof(ctx, 7, 7); err != nil || !found {
			t.Errorf("seq 7 at size 7: found=%v err=%v", found, err)
		}
		if _, found, err := tr.AuditInclusionProof(ctx, 7, 6); err != nil || found {
			t.Errorf("seq 7 at size 6: found=%v err=%v, want not found", found, err)
		}
		if _, _, err := tr.AuditInclusionProof(ctx, 7, 10); err == nil {
			t.Error("a proof at a size beyond the tree was answered")
		}
		if _, found, _ := tr.AuditInclusionProof(ctx, 99, 9); found {
			t.Error("a proof for an event that does not exist was answered")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAuditTreeCatchUpIsBoundedPerAppend: the first append after an upgrade adds a
// bounded number of leaves, the tree stays a prefix, and a checkpoint completes it.
func TestAuditTreeCatchUpIsBoundedPerAppend(t *testing.T) {
	ctx := context.Background()
	old := auditTreeCatchUpPerAppend
	auditTreeCatchUpPerAppend = 8
	t.Cleanup(func() { auditTreeCatchUpPerAppend = old })
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-bounded")
	appendN(t, st, tenant, 40) // 41 events; healthy appends add exactly one leaf each
	db := st.(*sqlStore).db
	if _, err := db.Exec("DROP TABLE " + dialect.AuditTreeTable); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range st.(*sqlStore).dia.AuditTreeStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	appendN(t, st, tenant, 1)
	var leaves int
	if err := db.QueryRow("SELECT COUNT(DISTINCT leaf) FROM " + dialect.AuditTreeTable).Scan(&leaves); err != nil {
		t.Fatal(err)
	}
	if leaves != 8 {
		t.Fatalf("one append added %d leaves to a legacy ledger, want the bound of 8", leaves)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		head, err := treeReader(t, sc).AuditTreeHead(ctx)
		if err != nil {
			return err
		}
		if head.Size != 42 {
			t.Fatalf("a writable head has %d leaves, want all 42", head.Size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	requireTreeMatchesLedger(t, st, tenant)
}

func TestAuditTreeMigrationRefusesAStubTable(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	db := st.(*sqlStore).db
	dia := st.(*sqlStore).dia
	if _, err := db.Exec("DROP TABLE " + dialect.AuditTreeTable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE " + dialect.AuditTreeTable + " (tenant_id TEXT)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // test
	if err := coreAuditTreeMigration(dia).Exec(ctx, tx); err == nil {
		t.Fatal("v24 was recorded over a table with the wrong shape")
	}
}

func TestAuditTreeIsAppendOnly(t *testing.T) {
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "tree-immutable")
	appendN(t, st, tenant, 2)
	db := st.(*sqlStore).db
	for _, stmt := range []string{
		"UPDATE " + dialect.AuditTreeTable + " SET hash = zeroblob(32)",
		"DELETE FROM " + dialect.AuditTreeTable,
	} {
		_, err := db.Exec(stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%q = %v, want an append-only refusal", stmt, err)
		}
	}
	requireTreeMatchesLedger(t, st, tenant)
}

// TestAuditTreePostgres runs the same checks on PostgreSQL, where audit_tree is under
// FORCE row-level security and its UPDATE/DELETE grants are revoked: a tenant's tree
// is written and read through the application role, and another tenant's is not. The
// split-owner topology is the hardened one: the owner creates audit_tree and the
// application role must still be able to append to it and read it.
func TestAuditTreePostgres(t *testing.T) {
	for _, topology := range []struct {
		name string
		pg   func(testing.TB) pgtest.DSNs
		cfg  func(pgtest.DSNs) store.Config
	}{
		{"single role", isolatedPG, func(d pgtest.DSNs) store.Config {
			return store.Config{Engine: store.EnginePostgres, DSN: d.App, MaxConns: 4}
		}},
		{"split owner", isolatedPGSplit, func(d pgtest.DSNs) store.Config {
			return store.Config{Engine: store.EnginePostgres, DSN: d.App, OwnerDSN: d.Owner, MaxConns: 4}
		}},
	} {
		t.Run(topology.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := Open(ctx, topology.cfg(topology.pg(t)), nil)
			if err != nil {
				t.Fatalf("open postgres: %v", err)
			}
			defer st.Close()
			a := provisionTenant(t, st, "pg-tree-a-"+uniqueSuffix())
			b := provisionTenant(t, st, "pg-tree-b-"+uniqueSuffix())
			appendN(t, st, a, 20)
			appendN(t, st, b, 3)
			requireTreeMatchesLedger(t, st, a)
			requireTreeMatchesLedger(t, st, b)
			var heads [2]store.AuditTreeHead
			for i, tenant := range []model.TenantID{a, b} {
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					var herr error
					heads[i], herr = treeReader(t, sc).AuditTreeHead(ctx)
					return herr
				}); err != nil {
					t.Fatal(err)
				}
			}
			if heads[0].Size == heads[1].Size || bytes.Equal(heads[0].Root, heads[1].Root) {
				t.Fatalf("two tenants share a tree head: %+v %+v", heads[0], heads[1])
			}
		})
	}
}

func TestAuditTreePostgresReplicaMutationIsRefused(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tenant := provisionTenant(t, st, "replica-tree-"+uniqueSuffix())
	appendN(t, st, tenant, 3)
	super := guardPGProbe(t, dsns.Superuser)
	for _, stmt := range []string{
		"UPDATE public.audit_tree SET hash = decode(repeat('00', 32), 'hex') WHERE tenant_id = $1",
		"DELETE FROM public.audit_tree WHERE tenant_id = $1",
	} {
		t.Run(strings.Fields(stmt)[0], func(t *testing.T) {
			tx, err := super.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, stmt, tenant.String()); err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("replica mutation = %v, want append-only refusal", err)
			}
		})
	}
	requireTreeMatchesLedger(t, st, tenant)
}

func TestAuditTreePostgresLegacyGuardUpgradePreservesRollout(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tenant := provisionTenant(t, st, "legacy-tree-"+uniqueSuffix())
	appendN(t, st, tenant, 3)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	super := guardPGProbe(t, dsns.Superuser)
	before := countRows(t, super, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable)
	// Recreate the deployed v25 posture without rewriting its migration or receipts.
	// Remove later markers too: keeping one would forge a gap in the core history.
	for _, stmt := range []string{
		"DELETE FROM public.schema_migrations_core WHERE version >= 26",
		"ALTER TABLE ONLY public.audit_tree ENABLE TRIGGER audit_tree_immutable",
	} {
		if _, err := super.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	st, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()
	if got := guardEnableStates(t, super)[dialect.AuditTreeTable]; got != guardStateAlways {
		t.Fatalf("upgraded audit tree guard = %q, want ALWAYS", got)
	}
	if after := countRows(t, super, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable); after != before {
		t.Fatalf("historical rollout receipts changed: %d -> %d", before, after)
	}
	requireTreeMatchesLedger(t, st, tenant)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := super.ExecContext(ctx, "ALTER TABLE ONLY public.audit_tree ENABLE TRIGGER audit_tree_immutable"); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, cfg, nil)
	if err == nil {
		_ = st.Close()
		t.Fatal("a later boot repaired ORIGIN drift after v26 was tracked")
	}
	if !strings.Contains(err.Error(), "audit tree guard") {
		t.Fatalf("later boot refused for another cause: %v", err)
	}
}

func TestAuditTreePostgresGuardDriftIsRefused(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		for _, tc := range []struct {
			name string
			ddl  string
		}{
			{"missing", "DROP TRIGGER audit_tree_immutable ON public.audit_tree"},
			{"disabled", "ALTER TABLE ONLY public.audit_tree DISABLE TRIGGER audit_tree_immutable"},
			{"replica only", "ALTER TABLE ONLY public.audit_tree ENABLE REPLICA TRIGGER audit_tree_immutable"},
			{"update only", "CREATE OR REPLACE TRIGGER audit_tree_immutable BEFORE UPDATE ON public.audit_tree FOR EACH ROW EXECUTE FUNCTION public.olivares_block_mutation(); ALTER TABLE ONLY public.audit_tree ENABLE ALWAYS TRIGGER audit_tree_immutable"},
		} {
			t.Run(fmt.Sprintf("legacy=%t/%s", legacy, tc.name), func(t *testing.T) {
				dsns := isolatedPG(t)
				ctx := context.Background()
				cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				super := guardPGProbe(t, dsns.Superuser)
				if legacy {
					// A v25 prefix has neither v26 nor any later migration marker.
					if _, err := super.ExecContext(ctx, "DELETE FROM public.schema_migrations_core WHERE version >= 26"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := super.ExecContext(ctx, tc.ddl); err != nil {
					t.Fatal(err)
				}
				st, err = Open(ctx, cfg, nil)
				if err == nil {
					_ = st.Close()
					t.Fatal("boot accepted a damaged audit tree guard")
				}
				if !strings.Contains(err.Error(), "audit tree guard") {
					t.Fatalf("boot refused for another cause: %v", err)
				}
				if legacy && countRows(t, super, "SELECT COUNT(*) FROM public.schema_migrations_core WHERE version = 26") != 0 {
					t.Fatal("refused migration recorded v26")
				}
			})
		}
	}
}

func TestAuditTreePostgresGuardDriftBeforeReadinessIsRefused(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			ddl  string
		}{
			{"missing", "DROP TRIGGER audit_tree_immutable ON public.audit_tree"},
			{"disabled", "ALTER TABLE ONLY public.audit_tree DISABLE TRIGGER audit_tree_immutable"},
			{"origin", "ALTER TABLE ONLY public.audit_tree ENABLE TRIGGER audit_tree_immutable"},
			{"replica only", "ALTER TABLE ONLY public.audit_tree ENABLE REPLICA TRIGGER audit_tree_immutable"},
			{"update only", "CREATE OR REPLACE TRIGGER audit_tree_immutable BEFORE UPDATE ON public.audit_tree FOR EACH ROW EXECUTE FUNCTION public.olivares_block_mutation(); ALTER TABLE ONLY public.audit_tree ENABLE ALWAYS TRIGGER audit_tree_immutable"},
		} {
			t.Run(fmt.Sprintf("split=%t/%s", split, tc.name), func(t *testing.T) {
				var dsns pgtest.DSNs
				if split {
					dsns = isolatedPGSplit(t)
				} else {
					dsns = isolatedPG(t)
				}
				ctx := context.Background()
				cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
				if split {
					cfg.OwnerDSN = dsns.Owner
				}
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				probe := guardPGProbe(t, dsns.Superuser)
				fired := false
				// Run Open's real plan, injecting drift after schema preparation
				// and runtime reconciliation, before readiness can publish a store.
				b := &storePreparation{cfg: cfg}
				plan := b.bootPlan()
				for i, step := range plan {
					if step.name == "verifyReadiness" {
						plan[i].run = func(ctx context.Context) error {
							fired = true
							if _, err := probe.ExecContext(ctx, tc.ddl); err != nil {
								t.Fatalf("plant readiness drift: %v", err)
							}
							return step.run(ctx)
						}
					}
				}
				st, err = b.runBootPlan(ctx, plan)
				if st != nil {
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if !fired {
					t.Fatal("readiness drift injection never ran")
				}
				if err == nil {
					t.Fatal("boot published a store after its audit tree guard drifted before readiness")
				}
				if !strings.Contains(err.Error(), "audit tree guard") && !(tc.name == "missing" && errors.Is(err, store.ErrAppendOnlyACLOpen)) {
					t.Fatalf("boot refused for another cause: %v", err)
				}
			})
		}
	}
}

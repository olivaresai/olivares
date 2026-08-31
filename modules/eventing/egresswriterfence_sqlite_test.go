// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package eventing

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	_ "modernc.org/sqlite" // the same database/sql driver the engine uses (pure Go, no CGO)
)

// Unit H, commit 4 — the fence ENFORCED on SQLite, against a SECOND connection to the same
// database file.
//
// WHY THE SECOND CONNECTION IS THE POINT. Every other test in this unit writes through the engine's
// own repositories, which is the honest way to check that OUR writers behave. It cannot check the
// thing the fence exists for: a DIFFERENT BINARY writing against the same database, whose descriptor
// does not have the nonce column and whose statements therefore never mention it. `database/sql` on
// the same file, with the column simply absent from the INSERT, is that binary — as close to the
// real threat as a test can get without shipping an old release into the test tree.
//
// WHY SQLITE NEEDS ITS OWN ENFORCEMENT AT ALL. The single-node deployment is the one an operator
// upgrades by hand, which is where two binaries against one database is most likely, not least.
// Shipping the fence on PostgreSQL only would leave the estate with the least ceremony unguarded.
//
// THE ENGINE DIFFERENCES THAT SHAPED THE SQL, measured rather than assumed:
//
//   - no transaction id, so a proof cannot be bound to one. The row-bound nonce that replaced it on
//     BOTH engines was forced by a measurement taken here: with a proof that only had to EXIST, a
//     COMMITTED, unconsumed attestation authorized an old binary's next write forever, and consuming
//     it on use still authorized ONE. TestACommittedOrphanProofAuthorizesNothing is that measurement
//     turned into a guarantee;
//   - no `FOR SHARE`, so the arming race PostgreSQL closes with a shared lock is closed differently.
//     SetMaxOpenConns(1) (core/internal/store/sqlstore/store.go:754) caps ONE POOL, not the database
//     file — two processes are two pools, and the tests below open a second one on purpose.
//     TestOneStoresWritesAndItsArmingCannotOverlap pins the single-pool property, which is what the
//     first-party binary uses; the two-process case rests on SQLite's own one-writer-at-a-time rule
//     and is NOT measured here. An earlier version of this comment claimed the stronger thing;
//   - no control flow and no row count in a trigger body, so each refusal is its own
//     `SELECT RAISE(ABORT, ...) WHERE <what makes it wrong>` and the consumption is the DELETE after
//     them.
//
// MUTATION-TESTED BY HAND, 2026-07-30 — read this as a RECORD OF A RUN, not as a standing property.
// Each guarantee below was verified to fail for ITS OWN reason by breaking the mechanism it rests on,
// watching the named test go red, and restoring it. There is no mutation runner in this repository,
// so nothing re-checks the table: an edit that breaks one of these pairings will not turn anything
// red. The pairings are the evidence that the tests measure their mechanism; the DATE is the limit of
// that evidence.
//
//	mutation of the SQLite migrations                      test that went red
//	---------------------------------------------------    ------------------------------------------
//	drop the consuming DELETE                               TestTwoGovernedMutationsNeedTwoProofs
//	drop `tenant_id = NEW.tenant_id` from the lookup        TestOneTenantsProofCannotAuthorizeAnothersMutationOnSQLite
//	stop comparing fence_generation                         TestAStaleGenerationIsRefusedOnSQLite
//	drop the empty/NULL-nonce RAISE (leave only the lookup) TestAnOldBinarysInsertWithoutTheNonceColumnIsRefusedByName
//	fence every update, not only a moved destination        TestANonMovingUpdateNeedsNoProofEvenWhenArmedOnSQLite
//	drop the WHEN guard, so the body always runs            TestInstallingTheFenceOnAnUpgradeChangesNothingUntilArmedOnSQLite
//	remove the sink DELETE trigger (0007)                   TestDeletingALiveSinkProfileIsRefusedOnSQLite
//	drop the enabled 0→1 clause from the UPDATE trigger     TestReactivatingADormantDestinationIsGovernedButDisablingIsNot
//	fence BOTH directions of enabled instead of one         TestReactivatingADormantDestinationIsGovernedButDisablingIsNot
//
// The fourth is worth reading twice. Dropping that RAISE does not make the write SUCCEED — a NULL
// nonce still matches no attestation, so the lookup refuses it anyway — it makes the write fail with
// the WRONG MESSAGE. The test goes red on the message, and that is the guarantee it holds: an old
// binary cannot translate a store error, so the raw text has to say what is missing and what to do.
//
// One test below is deliberately NOT in that table: TestTheSingleConnectionStoreLeavesNoArmingRace
// pins an ENGINE property, and the mutation that would exercise it — raising SQLite's connection cap
// — lives in core/internal/store/sqlstore, which is another lane's exclusive directory this wave. It
// is labeled as a property pin rather than presented as a mutation-tested guarantee.

// sqlExec is the little of database/sql the raw helpers need, so the same call works on a *sql.DB
// and inside a *sql.Tx.
type sqlExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// rawBindTenant reproduces SQLite's BindTenant for the second connection. The scope pin is durable
// across transactions, so every raw helper must replace the ambient pin — normally SYSTEM after the
// privileged fixture setup — before writing a tenant row; otherwise the tenancy tripwire can refuse
// the fixture before the writer fence runs.
func rawBindTenant(q sqlExec, tenant model.TenantID) error {
	if _, err := q.Exec("DELETE FROM _scope_tenant"); err != nil {
		return err
	}
	_, err := q.Exec("INSERT INTO _scope_tenant(tenant_id) VALUES(?)", tenant.String())
	return err
}

// rawSQLite opens a SECOND connection to the store's database file: a different binary against the
// same database. It sets the same pragmas the engine sets, so what it measures is the fence and not
// a locking artifact of a differently configured connection.
func rawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open a second connection to the store's database: %v", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			t.Fatalf("pragma %q: %v", pragma, err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// rawSubscriptionInsert writes a subscription row in raw SQL. A nil nonce means the column is not in
// the statement AT ALL, which is what a binary whose descriptor predates this unit emits — the
// engine's generic writer lists only the fields of its own descriptor.
func rawSubscriptionInsert(q sqlExec, tenant model.TenantID, name, endpoint string, nonce *string) error {
	if err := rawBindTenant(q, tenant); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cols := "id, tenant_id, created_at, updated_at, version, name, enabled, event_types, endpoint, secret_sealed, secret_hint, role, owner_actor, owner_actor_kind"
	vals := "?, ?, ?, ?, 1, ?, 1, 'finding.reported', ?, 'sealed:x', 'x', 'viewer', 't', 'user'"
	args := []any{model.NewID().String(), tenant.String(), now, now, name, endpoint}
	if nonce != nil {
		cols += ", writer_nonce"
		vals += ", ?"
		args = append(args, *nonce)
	}
	_, err := q.Exec("INSERT INTO eventing_subscription ("+cols+") VALUES ("+vals+")", args...) // #nosec G202 -- fixed column lists, no user input
	return err
}

// rawAttestInsert writes a capability attestation in raw SQL, so a test can put one through a
// ROLLBACK or a SAVEPOINT — neither of which the module's own writer can be made to do.
func rawAttestInsert(q sqlExec, tenant model.TenantID, nonce string, capability, generation int64) error {
	if err := rawBindTenant(q, tenant); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := q.Exec(
		"INSERT INTO eventing_writer_attest (id, tenant_id, created_at, updated_at, version, nonce, capability, fence_generation) VALUES (?, ?, ?, ?, 1, ?, ?, ?)",
		model.NewID().String(), tenant.String(), now, now, nonce, capability, generation)
	return err
}

// rawAttestCount counts a tenant's live proofs.
func rawAttestCount(t *testing.T, db *sql.DB, tenant model.TenantID) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM eventing_writer_attest WHERE tenant_id = ?", tenant.String()).Scan(&n); err != nil {
		t.Fatalf("count attestations: %v", err)
	}
	return n
}

// freshNonce is a value no row ever received — the shape of an orphan.
func freshNonce() string { return strings.ReplaceAll(model.NewID().String(), "-", "") }

// subRecord is the module's own subscription shape, for the paths that write through the engine.
func subRecord(name, endpoint string) model.Record {
	return model.Record{
		colSubName: name, colSubEnabled: true, colSubTypes: "finding.reported",
		colSubEndpoint: endpoint, colSubSecret: "sealed:x", colSubSecretHint: "x",
		colSubRole: "viewer", colSubOwnerActor: "t", colSubOwnerActorK: "user",
		colSubAuthType: authTypeNone,
	}
}

// fencedSQLite opens a store with the module's real schema on a fresh file, provisions a tenant and
// returns the file path with it — a fresh database is armed by classification, so nothing else is
// needed to reach the enforcing state.
func fencedSQLite(t *testing.T, slug string) (store.Store, model.TenantID, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "fence.db")
	st := openWithModuleSchema(t, dsn)
	t.Cleanup(func() { _ = st.Close() })
	return st, provisionFenceTenant(t, st, slug), dsn
}

// TestACommittedOrphanProofAuthorizesNothing is the leak this engine measured, closed.
//
// With a proof that only had to EXIST, a committed and unconsumed attestation authorized an old
// binary's next write — forever. Bound to the row, it authorizes nothing: the orphan carries a nonce
// no row ever received, and an old binary cannot name it because the column is not in its statement.
func TestACommittedOrphanProofAuthorizesNothing(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	// A proof written and COMMITTED without being spent.
	var orphan string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		n, err := WriterProof{Capability: EgressWriterCapability, Generation: gen}.Stamp(ctx, sc)
		orphan = n
		return err
	}); err != nil {
		t.Fatalf("stamp an orphan proof: %v", err)
	}
	if orphan == "" {
		t.Fatal("the fixture produced no nonce, so there is no orphan to test with")
	}
	raw := rawSQLite(t, dsn)
	if got := rawAttestCount(t, raw, tenant); got != 1 {
		t.Fatalf("live proofs before the old binary writes = %d, want 1: this test needs an orphan to exist", got)
	}

	// The old binary writes. The column is absent from its statement.
	err := rawSubscriptionInsert(raw, tenant, "old-writer", "https://a.example.com/h", nil)
	if err == nil {
		t.Fatal("a live orphaned proof authorized a write that carried no proof of its own — the exact leak measured on this engine before the nonce was bound to the row")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("the refusal is not diagnosable: %v", err)
	}
	// And the orphan is untouched, which is what makes the refusal meaningful: the write was not
	// refused because there were no proofs, but because none of them was THIS write's.
	if got := rawAttestCount(t, raw, tenant); got != 1 {
		t.Fatalf("live proofs after the refusal = %d, want 1", got)
	}
}

// TestAnOldBinarysInsertWithoutTheNonceColumnIsRefusedByName. An old binary cannot translate a store
// error, so the raw text has to carry the meaning: what is missing and what to do about it.
func TestAnOldBinarysInsertWithoutTheNonceColumnIsRefusedByName(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	_ = armFence(t, st)
	raw := rawSQLite(t, dsn)

	err := rawSubscriptionInsert(raw, tenant, "no-column", "https://b.example.com/h", nil)
	if err == nil {
		t.Fatal("an armed fence accepted an INSERT that does not mention the nonce column at all")
	}
	for _, want := range []string{"writer fence", "no capability attestation", "egress gate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q, so an operator reading an un-upgraded node's log cannot diagnose it: %v", want, err)
		}
	}
}

// TestTwoGovernedMutationsNeedTwoProofs. On this engine a proof that merely EXISTED authorized every
// governed write in the transaction; consuming it makes each mutation carry its own.
func TestTwoGovernedMutationsNeedTwoProofs(t *testing.T) {
	st, tenant, _ := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		first := subRecord("one", "https://c1.example.com/h")
		if err := StampWriterProof(ctx, sc, first, gen); err != nil {
			return err
		}
		nonce, _ := first[colWriterNonce].(string)
		if _, err := repo.Create(ctx, first); err != nil {
			return err
		}
		second := subRecord("two", "https://c2.example.com/h")
		second[colWriterNonce] = nonce // the same proof, a second destination
		_, err = repo.Create(ctx, second)
		return err
	})
	if err == nil {
		t.Fatal("one proof authorized TWO governed mutations in the same transaction: a proof that survives its mutation is a proof the next writer can use")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNeitherRollbackNorRollbackToSavepointLeavesAuthorization. A proof must commit with the mutation
// it authorizes; anything that survives a rollback is authorization nobody granted. SAVEPOINT is the
// half a plain rollback test misses, and it is reachable in raw SQL only.
func TestNeitherRollbackNorRollbackToSavepointLeavesAuthorization(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	gen := armFence(t, st)
	raw := rawSQLite(t, dsn)

	// (a) a whole transaction rolled back.
	rolled := freshNonce()
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := rawAttestInsert(tx, tenant, rolled, EgressWriterCapability, gen); err != nil {
		t.Fatalf("write a proof to be rolled back: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	err = rawSubscriptionInsert(raw, tenant, "after-rollback", "https://d1.example.com/h", &rolled)
	if err == nil {
		t.Fatal("a proof from a ROLLED BACK transaction authorized a later write")
	}
	if !strings.Contains(err.Error(), "writer fence") || strings.Contains(err.Error(), "tenant scope") {
		t.Fatalf("the post-rollback write was not refused by the writer fence: %v", err)
	}

	// (b) a SAVEPOINT rolled back inside a live transaction: the proof is gone, the transaction is
	// not, and the write that follows in the SAME transaction must still be refused.
	tx2, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx2.Rollback() }()
	if _, err := tx2.Exec("SAVEPOINT s1"); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	sp := freshNonce()
	if err := rawAttestInsert(tx2, tenant, sp, EgressWriterCapability, gen); err != nil {
		t.Fatalf("write a proof inside the savepoint: %v", err)
	}
	if _, err := tx2.Exec("ROLLBACK TO s1"); err != nil {
		t.Fatalf("rollback to savepoint: %v", err)
	}
	err = rawSubscriptionInsert(tx2, tenant, "after-savepoint", "https://d2.example.com/h", &sp)
	if err == nil {
		t.Fatal("a proof undone by ROLLBACK TO SAVEPOINT still authorized a write in the same transaction")
	}
	if !strings.Contains(err.Error(), "writer fence") || strings.Contains(err.Error(), "tenant scope") {
		t.Fatalf("the post-savepoint write was not refused by the writer fence: %v", err)
	}
}

// TestAnOldBinarysUpdatePreservingEveryColumnIsRefusedOnSQLite. A persistent column alone is not
// enough: an old binary preserves the stored value while changing the endpoint. It fails because the
// proof that created that value was consumed by the mutation that created it.
func TestAnOldBinarysUpdatePreservingEveryColumnIsRefusedOnSQLite(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://e1.example.com/h", true, gen); err != nil {
		t.Fatalf("seed a governed row: %v", err)
	}
	raw := rawSQLite(t, dsn)
	// The old binary re-points it with an UPDATE that carries every column it read, nonce included.
	res, err := raw.Exec(
		"UPDATE eventing_subscription SET endpoint = ?, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		"https://evil.example.com/h", time.Now().UTC().Format(time.RFC3339Nano), tenant.String(), "sub")
	if err == nil {
		n, _ := res.RowsAffected()
		t.Fatalf("an update that re-pointed the destination while preserving the stored nonce was accepted (%d row(s))", n)
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestANonMovingUpdateNeedsNoProofEvenWhenArmedOnSQLite pins the narrow scope as a property. Unit G
// preserves that a pre-existing subscription stays editable — including to DISABLE it, which is what
// an operator does in an incident — so an armed fence must not take that away.
func TestANonMovingUpdateNeedsNoProofEvenWhenArmedOnSQLite(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://e2.example.com/h", true, gen); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw := rawSQLite(t, dsn)
	if _, err := raw.Exec(
		"UPDATE eventing_subscription SET enabled = 0, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		time.Now().UTC().Format(time.RFC3339Nano), tenant.String(), "sub"); err != nil {
		t.Fatalf("an armed fence blocked DISABLING a subscription from a writer that carries no proof: %v — that is the action an operator takes in an incident, and unit G preserves it on purpose", err)
	}
}

// TestReactivatingADormantDestinationIsGovernedButDisablingIsNot is the asymmetry an adversarial
// review of the implementation found, expressed as the rule it produced: THE FENCE NEVER BLOCKS
// TURNING EGRESS OFF; IT GOVERNS TURNING IT ON.
//
// The first version compared the endpoint alone, so an old binary could flip enabled 0→1 on a
// subscription it had disabled and resume delivery carrying no proof at all. Disabling must stay
// free — unit G preserves that a pre-existing subscription can be disabled from a node the operator
// has not replaced, which is what an operator does in an incident — so the condition is
// one-directional, and this test pins both directions.
func TestReactivatingADormantDestinationIsGovernedButDisablingIsNot(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://s1.example.com/h", true, gen); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw := rawSQLite(t, dsn)
	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

	// OFF is free, from a binary carrying no proof.
	if _, err := raw.Exec(
		"UPDATE eventing_subscription SET enabled = 0, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		now(), tenant.String(), "sub"); err != nil {
		t.Fatalf("an armed fence blocked DISABLING a subscription: %v — the fence must never block turning egress off", err)
	}
	// ON is not.
	_, err := raw.Exec(
		"UPDATE eventing_subscription SET enabled = 1, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		now(), tenant.String(), "sub")
	if err == nil {
		t.Fatal("an old binary reactivated a dormant destination with no proof: reactivation resumes delivery to it, so it is a mutation that makes a destination effective")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
	// And a writer that carries the gate CAN reactivate it: the fence governs the version of the
	// writer, not the operation.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(subscriptionKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Limit: 1})
		if e != nil {
			return e
		}
		rec := rows[0]
		rec[colSubEnabled] = true
		if e := (WriterProof{Capability: EgressWriterCapability, Generation: gen}).StampInto(ctx, sc, rec); e != nil {
			return e
		}
		_, e = repo.Update(ctx, rec)
		return e
	}); err != nil {
		t.Fatalf("a writer carrying the gate could not reactivate: %v", err)
	}
}

// TestAProofOrphanedByDriftSurvivesARepairUntilTheGenerationMoves is P0-4 from the implementation
// contrast, and the sharpest thing it found: it turns a limitation this unit DECLARES (a trigger can
// be dropped without stopping a boot) into a bypass it did not — one that survives a repair whose
// verification reports green.
//
// The database never checks that an attestation and the mutation it authorizes share a transaction;
// what binds them is the nonce on the row. So a proof written by a binary that DOES carry the gate,
// during a window when the trigger was missing, is never consumed — and it stays live, bound to that
// row, at that generation. Repair the trigger and an old binary's next UPDATE of that row preserves
// the stored nonce, finds the live proof, and is ACCEPTED. Exactly once, silently.
//
// What closes it is the generation. The fence compares the generation the writer OBSERVED, so moving
// it invalidates every proof outstanding from the gap at a stroke — which is why re-arming is the
// repair path and why the ceremony refuses to short-circuit on "already committed"
// (cmd/olivares/cmd_eventing_fence.go).
//
// Both halves are asserted: the hazard, because a remedy for a danger nobody measured is a comment;
// and the remedy.
func TestAProofOrphanedByDriftSurvivesARepairUntilTheGenerationMoves(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)
	raw := rawSQLite(t, dsn)

	if err := writeSubscription(ctx, st, tenant, "sub", "https://o1.example.com/h", true, gen); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// THE DRIFT: the UPDATE trigger is lost, the way a data-only restore loses it.
	if _, err := raw.Exec("DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_upd"); err != nil {
		t.Fatalf("drop the update trigger: %v", err)
	}
	// A binary that DOES carry the gate moves the destination. Its proof is written and, with no
	// trigger to consume it, orphaned — but bound to this row.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(subscriptionKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Limit: 1})
		if e != nil {
			return e
		}
		rec := rows[0]
		rec[colSubEndpoint] = "https://o2.example.com/h"
		if e := (WriterProof{Capability: EgressWriterCapability, Generation: gen}).StampInto(ctx, sc, rec); e != nil {
			return e
		}
		_, e = repo.Update(ctx, rec)
		return e
	}); err != nil {
		t.Fatalf("a proved write during the gap: %v", err)
	}
	if got := rawAttestCount(t, raw, tenant); got != 1 {
		t.Fatalf("live proofs after the gap = %d, want 1: this test needs the orphan the gap creates", got)
	}

	// THE REPAIR: the trigger comes back, and `verify` would now report enforcing.
	applyFenceUpdateTrigger(t, raw)

	// THE HAZARD, measured: an old binary's UPDATE preserves the stored nonce and is accepted once.
	moveByOldBinary := func(to string) error {
		_, err := raw.Exec(
			"UPDATE eventing_subscription SET endpoint = ?, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
			to, time.Now().UTC().Format(time.RFC3339Nano), tenant.String(), "sub")
		return err
	}
	if err := moveByOldBinary("https://o3.example.com/h"); err != nil {
		t.Fatalf("the hazard this test documents did not reproduce (%v); if the binding changed, the remedy below may no longer be the reason the fence holds", err)
	}
	// And ONLY once: the proof was consumed by that write.
	if err := moveByOldBinary("https://o4.example.com/h"); err == nil {
		t.Fatal("the orphaned proof authorized a SECOND old write; it must be spent by the first")
	}

	// THE REMEDY: moving the generation invalidates every proof outstanding from the gap. Re-run the
	// whole sequence and confirm the old binary is refused from the start.
	if err := writeSubscription(ctx, st, tenant, "sub2", "https://p1.example.com/h", true, gen); err != nil {
		t.Fatalf("seed a second row: %v", err)
	}
	if _, err := raw.Exec("DROP TRIGGER IF EXISTS eventing_subscription_writer_fence_upd"); err != nil {
		t.Fatalf("drop again: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(subscriptionKind)
		if e != nil {
			return e
		}
		rows, _, e := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSubName, "sub2")}, Limit: 1})
		if e != nil {
			return e
		}
		rec := rows[0]
		rec[colSubEndpoint] = "https://p2.example.com/h"
		if e := (WriterProof{Capability: EgressWriterCapability, Generation: gen}).StampInto(ctx, sc, rec); e != nil {
			return e
		}
		_, e = repo.Update(ctx, rec)
		return e
	}); err != nil {
		t.Fatalf("a proved write during the second gap: %v", err)
	}
	applyFenceUpdateTrigger(t, raw)
	// The ceremony's repair step: re-arm, which moves the generation.
	rs := st.(store.RolloutStater)
	cur, err := rs.RolloutState(ctx, EgressWriterFenceControlKey)
	if err != nil {
		t.Fatalf("read fence state: %v", err)
	}
	if _, err := rs.SetRolloutMode(ctx, store.RolloutTransition{
		Key: EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
		Actor: "test", Reason: "post-repair re-arm", ExpectGeneration: cur.Generation,
	}); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	_, err = raw.Exec(
		"UPDATE eventing_subscription SET endpoint = ?, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		"https://p3.example.com/h", time.Now().UTC().Format(time.RFC3339Nano), tenant.String(), "sub2")
	if err == nil {
		t.Fatal("after the generation moved, an orphaned proof from the gap STILL authorized an old write: the generation comparison is what makes a repair safe")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// applyFenceUpdateTrigger re-creates the subscription UPDATE trigger from the module's OWN migration,
// so a repair in a test is the same DDL a repair in production applies.
func applyFenceUpdateTrigger(t *testing.T, raw *sql.DB) {
	t.Helper()
	body, err := fs.ReadFile(migrationsFS, "migrations/sqlite/0004_writer_fence_subscription_upd.sql")
	if err != nil {
		t.Fatalf("read the fence migration: %v", err)
	}
	if _, err := raw.Exec(string(body)); err != nil {
		t.Fatalf("re-apply the fence migration: %v", err)
	}
}

// TestRotatingTheAuthCredentialIsGoverned closes the asymmetry the implementation contrast named:
// the sink's sealed credential was treated as part of the destination while the subscription's was
// not, though both end up as a header on the same request — and on a multi-tenant collector it is
// the token, not the URL, that selects the receiving workspace.
//
// The cost is stated in the migration and accepted here: a rotation to the SAME receiver, which is
// the common and innocent case, is indistinguishable from a switch to another, so both are governed.
//
// It also pins the NULL normalisation. Nothing else in the suite exercises a nullable auth column
// going from unset to empty, which is precisely the shape that made a plain "disable" fire the fence
// before COALESCE was added to both triggers.
func TestRotatingTheAuthCredentialIsGoverned(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(subscriptionKind)
		if e != nil {
			return e
		}
		rec := subRecord("sub", "https://a1.example.com/h")
		rec[colSubAuthType] = "bearer"
		rec[colSubAuthValSealed] = "sealed:token-a"
		rec[colSubAuthValHint] = "a"
		if e := StampWriterProof(ctx, sc, rec, gen); e != nil {
			return e
		}
		_, e = repo.Create(ctx, rec)
		return e
	}); err != nil {
		t.Fatalf("seed a subscription with an auth credential: %v", err)
	}

	raw := rawSQLite(t, dsn)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// An old binary swaps the token for another workspace's, leaving the URL alone.
	_, err := raw.Exec(
		"UPDATE eventing_subscription SET auth_value_sealed = ?, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		"sealed:token-b", now, tenant.String(), "sub")
	if err == nil {
		t.Fatal("an old binary rotated the auth credential with no proof: on a multi-tenant collector the token is what selects the receiver, which is the same reason the sink credential is governed")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
	// A hint-only change moves nothing and stays free.
	if _, err := raw.Exec(
		"UPDATE eventing_subscription SET auth_value_hint = ?, updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
		"b", now, tenant.String(), "sub"); err != nil {
		t.Fatalf("a display-hint change was fenced: %v — it does not reach the wire", err)
	}
}

// TestAStaleGenerationIsRefusedOnSQLite. Without the generation comparison the proof would mean only
// "code able to write an attestation ran". With it, it means the writer read the CURRENT disposition.
func TestAStaleGenerationIsRefusedOnSQLite(t *testing.T) {
	st, tenant, _ := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	err := writeSubscription(ctx, st, tenant, "stale", "https://f.example.com/h", true, gen-1)
	if err == nil {
		t.Fatal("a proof made against an older fence generation was accepted: a node with a stale cached read would author under a disposition that has since moved")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestOneTenantsProofCannotAuthorizeAnothersMutationOnSQLite. This engine has no row-level security,
// and its scope pin is durable: privileged work restores the reserved SYSTEM pin rather than leaving
// the permissive empty state, while Mutate replaces it with the transaction's real tenant. The tenant
// predicate inside the writer-fence trigger is therefore what keeps one tenant's proof from
// authorizing another's mutation after the scope tripwire has admitted that mutation. PostgreSQL
// carries the same predicate, so the isolation is a property of the rule on both engines rather than
// of how the roles happen to be configured.
func TestOneTenantsProofCannotAuthorizeAnothersMutationOnSQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "tenants.db")
	st := openWithModuleSchema(t, dsn)
	defer st.Close()
	alpha := provisionFenceTenant(t, st, "alpha")
	bravo := provisionFenceTenant(t, st, "bravo")
	ctx := context.Background()
	gen := armFence(t, st)

	var alphaNonce string
	if err := st.Mutate(ctx, alpha, func(sc store.Scope) error {
		n, err := WriterProof{Capability: EgressWriterCapability, Generation: gen}.Stamp(ctx, sc)
		alphaNonce = n
		return err
	}); err != nil {
		t.Fatalf("alpha stamps a proof: %v", err)
	}

	err := st.Mutate(ctx, bravo, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := subRecord("borrowed", "https://g.example.com/h")
		rec[colWriterNonce] = alphaNonce
		_, err = repo.Create(ctx, rec)
		return err
	})
	if err == nil {
		t.Fatal("one tenant's capability proof authorized another tenant's mutation")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestDeletingALiveSinkProfileIsRefusedOnSQLite covers the DELETE half — the one mutation that
// re-pointed a live destination past any INSERT/UPDATE fence — together with the two paths that
// legitimately remove a profile.
func TestDeletingALiveSinkProfileIsRefusedOnSQLite(t *testing.T) {
	st, tenant, _ := fencedSQLite(t, "acme")
	ctx := context.Background()
	gen := armFence(t, st)

	var subID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec := subRecord("sub", "https://h.example.com/h")
		if err := StampWriterProof(ctx, sc, rec, gen); err != nil {
			return err
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return err
		}
		subID = model.ID(created.String(model.ColID))
		sinks, err := sc.Ext(subscriptionSinkKind)
		if err != nil {
			return err
		}
		srec := model.Record{
			colSinkSubRef: subID.String(), colSinkKind: "splunk_hec",
			colSinkFormat: "", colSinkCred: "sealed:t", colSinkOpts: "", colSinkHint: "t",
		}
		if err := StampWriterProof(ctx, sc, srec, gen); err != nil {
			return err
		}
		_, err = sinks.Create(ctx, srec)
		return err
	}); err != nil {
		t.Fatalf("seed a subscription with a sink profile: %v", err)
	}

	// Deleting the profile while the subscription LIVES re-points the destination: refused.
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return New().deleteSinkRowWithSubscription(ctx, sc, subID)
	})
	if err == nil {
		t.Fatal("deleting the sink profile of a LIVE subscription was accepted: it moves the destination to the base endpoint, silently")
	}
	if !strings.Contains(err.Error(), "writer fence") {
		t.Fatalf("unexpected error: %v", err)
	}

	// CLEARING it is the proof-carrying way to do the same thing, and it is allowed. The proof is
	// built BEFORE the transaction, which is the shape every governed writer uses since the read
	// moved out of the transaction it authorizes.
	proof := WriterProof{Capability: EgressWriterCapability, Generation: gen}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		return New().clearSinkProfile(ctx, sc, subID, proof)
	}); err != nil {
		t.Fatalf("clearing a sink profile through a writer that carries the gate was refused: %v", err)
	}

	// Deleting the subscription and then its profile is cleanup, not a re-point: allowed.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		if err := repo.Delete(ctx, subID); err != nil {
			return err
		}
		return New().deleteSinkRowWithSubscription(ctx, sc, subID)
	}); err != nil {
		t.Fatalf("deleting a subscription and its profile together was refused: %v — that is cleanup, and there is no destination left to move", err)
	}
}

// TestInstallingTheFenceOnAnUpgradeChangesNothingUntilArmedOnSQLite is the operational promise, and
// the one that decides whether this unit is safe to ship on this engine: on a deployment whose fleet
// predates the fence, the migrations land and every existing writer keeps working.
func TestInstallingTheFenceOnAnUpgradeChangesNothingUntilArmedOnSQLite(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "upgrade.db")
	ctx := context.Background()

	// Era 1: a binary with neither the fence's control nor its migrations.
	st1 := openWithoutTheFence(t, dsn)
	tenant := provisionFenceTenant(t, st1, "acme")
	if err := writeSubscription(ctx, st1, tenant, "era1", "https://p1.example.com/h", false, 0); err != nil {
		t.Fatalf("era 1 write: %v", err)
	}
	_ = st1.Close()

	// Era 2: the fence arrives. The witness table exists, so it is classified DORMANT — and an
	// un-upgraded writer must still be able to author, through the engine AND from another binary.
	st2 := openWithModuleSchema(t, dsn)
	defer st2.Close()
	got := stateOf(t, st2, EgressWriterFenceControlKey)
	if got.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("an upgraded deployment classified the fence %q, want %q", got.CurrentMode, store.RolloutLegacyCompat)
	}
	if err := writeSubscription(ctx, st2, tenant, "era2-unproved", "https://p2.example.com/h", false, 0); err != nil {
		t.Fatalf("a writer with no proof was refused on a DORMANT fence: %v — installing the fence would break every rolling update", err)
	}
	raw := rawSQLite(t, dsn)
	if err := rawSubscriptionInsert(raw, tenant, "era2-other-binary", "https://p3.example.com/h", nil); err != nil {
		t.Fatalf("an un-upgraded BINARY was refused on a dormant fence: %v", err)
	}
	// A writer that DOES carry the gate works too, so the two eras coexist.
	if err := writeSubscription(ctx, st2, tenant, "era2-proved", "https://p4.example.com/h", true, got.Generation); err != nil {
		t.Fatalf("a writer carrying the gate was refused on a dormant fence: %v", err)
	}

	// Only the deliberate arming closes it.
	gen := armFence(t, st2)
	if err := rawSubscriptionInsert(raw, tenant, "after-arm", "https://p5.example.com/h", nil); err == nil {
		t.Fatal("after arming, an un-upgraded binary was still accepted")
	}
	if err := writeSubscription(ctx, st2, tenant, "after-arm-ok", "https://p6.example.com/h", true, gen); err != nil {
		t.Fatalf("after arming, a writer carrying the gate was refused: %v", err)
	}
}

// TestOneStoresWritesAndItsArmingCannotOverlap pins what the SQLite side actually relies on in place
// of PostgreSQL's `FOR SHARE`, with the scope stated exactly.
//
// This store opens SQLite with SetMaxOpenConns(1) (core/internal/store/sqlstore/store.go:754), so a
// write and an arming issued THROUGH THE SAME STORE cannot interleave. That is the configuration the
// first-party binary runs, and it is what this test measures.
//
// WHAT IT DOES NOT MEASURE, because an earlier version of this comment claimed it: the cap is per
// POOL, not per database file. Two processes against one file are two pools — the tests in this file
// open a second one deliberately — and what serializes THEM is SQLite's own one-writer-at-a-time
// rule, which is not exercised here. This test used to be named for "the single-connection store",
// which read as the stronger claim.
//
// It is a property pin, not a mutation-tested guarantee of this unit: the mutation that would
// exercise it lives in another lane's exclusive directory this wave, and pretending otherwise would
// be exactly the overclaim this campaign keeps finding.
func TestOneStoresWritesAndItsArmingCannotOverlap(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "race.db")
	ctx := context.Background()

	// The race only has a shape on an UPGRADE: a fresh database is armed by classification, so there
	// is no pre-arm window to be in flight during.
	st1 := openWithoutTheFence(t, dsn)
	tenant := provisionFenceTenant(t, st1, "acme")
	_ = st1.Close()
	st := openWithModuleSchema(t, dsn)
	defer st.Close()
	if got := stateOf(t, st, EgressWriterFenceControlKey); got.CurrentMode != store.RolloutLegacyCompat {
		t.Fatalf("precondition: the fence is %q, want %q", got.CurrentMode, store.RolloutLegacyCompat)
	}

	// SIGNALS, NOT SLEEPS. An earlier version slept 300ms before arming and then read "still blocked"
	// off a 900ms timeout. Both premises were hoped for rather than pinned: the writer might not have
	// reached its statement yet, and — worse — the armer might never have been scheduled at all, in
	// which case "nothing completed" measures the Go runtime and not the lock. Each side now
	// announces the state the assertion depends on, and the timeout is left to measure only the one
	// thing that genuinely cannot be signaled: that a started attempt does NOT finish.
	release := make(chan struct{})
	inFlight := make(chan struct{})   // the writer holds the row, inside its transaction
	armStarted := make(chan struct{}) // the armer goroutine has begun its attempt
	writeDone := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		writeDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(subscriptionKind)
			if err != nil {
				return err
			}
			if _, err := repo.Create(ctx, subRecord("in-flight", "https://q.example.com/h")); err != nil {
				return err
			}
			close(inFlight)
			<-release
			return nil
		})
	}()

	armed := make(chan error, 1)
	go func() {
		<-inFlight
		rs := st.(store.RolloutStater)
		// The signal goes BEFORE the read, and that placement is itself a measurement. Put after it,
		// this test failed: on THIS engine the armer never reaches SetRolloutMode at all, because the
		// state READ already blocks — the store pins SQLite to one connection and the writer's
		// transaction is holding it. So what "the arming cannot overlap" means here is stronger than
		// the design assumed: the armer cannot even observe the control row, let alone move it.
		close(armStarted)
		cur, err := rs.RolloutState(ctx, EgressWriterFenceControlKey)
		if err != nil {
			armed <- err
			return
		}
		_, err = rs.SetRolloutMode(ctx, store.RolloutTransition{
			Key: EgressWriterFenceControlKey, Mode: store.RolloutEnforced,
			Actor: "test", Reason: "arm while a write is in flight", ExpectGeneration: cur.Generation,
		})
		armed <- err
	}()

	// Both premises pinned before anything is concluded from a timeout.
	select {
	case <-armStarted:
	case <-time.After(10 * time.Second):
		close(release)
		wg.Wait()
		t.Fatal("the arming goroutine never began its attempt: the case below would have read a scheduling gap as a lock being held, which is the whole reason it no longer sleeps")
	}

	select {
	case err := <-armed:
		t.Fatalf("the arming completed while a pre-arm write was still in flight (err=%v): after it returns, no un-proved write may still land", err)
	case <-time.After(900 * time.Millisecond):
		// Still blocked, which is the property.
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("the in-flight pre-arm write failed: %v", err)
	}
	wg.Wait()
	if err := <-armed; err != nil {
		t.Fatalf("the arming failed once the writer finished: %v", err)
	}
	if err := writeSubscription(ctx, st, tenant, "after-arming", "https://r.example.com/h", false, 0); err == nil {
		t.Fatal("a write with no proof was accepted after the arming returned")
	}
}

// EVERY governed column, one at a time — and every ungoverned one, also one at a time.
//
// The suite tested three of the nine reasons a subscription or sink update is governed: the moved
// endpoint, the reactivation, and the rotated credential. The other six rested on reading the WHEN
// clause. A clause is a second copy of the rule, and this campaign has already been paid twice for
// trusting one: the first version of the subscription trigger compared the endpoint alone and let an
// old binary reactivate a dormant destination, and the auth columns went in only after a contrast
// found the credential is part of the destination.
//
// Each case changes exactly ONE column from a writer carrying no proof. The governed ones must be
// refused; the ungoverned ones must go through, because the fence governs turning egress ON and
// blocking a rename or a description edit would make it an obstacle instead of a gate.
func TestEachGovernedColumnIsGovernedOnItsOwn(t *testing.T) {
	governed := []struct{ column, set string }{
		{"endpoint", "endpoint = 'https://moved.example.com/h'"},
		{"auth_type", "auth_type = 'bearer'"},
		{"auth_header_name", "auth_header_name = 'X-Token'"},
		{"auth_value_sealed", "auth_value_sealed = 'sealed:new'"},
	}
	free := []struct{ column, set string }{
		{"name", "name = 'renamed'"},
		{"description", "description = 'an operator note'"},
		{"enabled 1->0", "enabled = 0"},
	}

	run := func(t *testing.T, slug, set string) error {
		t.Helper()
		st, tenant, dsn := fencedSQLite(t, slug)
		ctx := context.Background()
		gen := armFence(t, st)
		if err := writeSubscription(ctx, st, tenant, "sub", "https://s1.example.com/h", true, gen); err != nil {
			t.Fatalf("seed: %v", err)
		}
		raw := rawSQLite(t, dsn)
		_, err := raw.Exec(
			"UPDATE eventing_subscription SET "+set+", updated_at = ?, version = version + 1 WHERE tenant_id = ? AND name = ?",
			time.Now().UTC().Format(time.RFC3339Nano), tenant.String(), "sub")
		return err
	}

	for _, c := range governed {
		t.Run("governed/"+c.column, func(t *testing.T) {
			err := run(t, "g"+strings.NewReplacer("_", "", " ", "", "-", "", ">", "").Replace(c.column), c.set)
			if err == nil {
				t.Fatalf("a writer with no proof changed %s and the fence allowed it: that column is part of the destination, so moving it makes a different destination effective", c.column)
			}
			if !strings.Contains(err.Error(), "writer fence") {
				t.Fatalf("%s was refused, but not by the fence: %v", c.column, err)
			}
		})
	}
	for _, c := range free {
		t.Run("free/"+c.column, func(t *testing.T) {
			err := run(t, "f"+strings.NewReplacer("_", "", " ", "", "-", "", ">", "").Replace(c.column), c.set)
			if err != nil {
				t.Fatalf("the fence blocked changing %s, which does not make any destination effective: %v — the fence governs turning egress ON, and blocking this makes it an obstacle instead of a gate", c.column, err)
			}
		})
	}
}

// The sink profile's governed columns, also one at a time.
//
// The rendered destination is the subscription's endpoint PLUS how the sink renders and
// authenticates to it, so the sink trigger governs four columns. The suite exercised the kind alone,
// through the probe; format, opts and the sealed credential rested on the WHEN clause being read
// correctly. The sealed credential is the one that matters most and was the last to be added: a
// binary that swaps it re-points where the evidence goes without touching a single character of the
// endpoint.
func TestEachGovernedSinkColumnIsGovernedOnItsOwn(t *testing.T) {
	cases := []struct{ column, set string }{
		{"sink_kind", "sink_kind = 'elastic'"},
		{"sink_format", "sink_format = 'otlp'"},
		{"sink_opts", "sink_opts = '{\"index\":\"other\"}'"},
		{"sink_cred_sealed", "sink_cred_sealed = 'sealed:rotated'"},
	}
	for _, c := range cases {
		t.Run(c.column, func(t *testing.T) {
			st, tenant, dsn := fencedSQLite(t, "s"+strings.ReplaceAll(c.column, "_", ""))
			ctx := context.Background()
			gen := armFence(t, st)
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(subscriptionKind)
				if err != nil {
					return err
				}
				rec := subRecord("sub", "https://h.example.com/h")
				if err := StampWriterProof(ctx, sc, rec, gen); err != nil {
					return err
				}
				created, err := repo.Create(ctx, rec)
				if err != nil {
					return err
				}
				sinks, err := sc.Ext(subscriptionSinkKind)
				if err != nil {
					return err
				}
				srec := model.Record{
					colSinkSubRef: created.String(model.ColID), colSinkKind: "splunk_hec",
					colSinkFormat: "", colSinkCred: "sealed:t", colSinkOpts: "", colSinkHint: "t",
				}
				if err := StampWriterProof(ctx, sc, srec, gen); err != nil {
					return err
				}
				_, err = sinks.Create(ctx, srec)
				return err
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}

			raw := rawSQLite(t, dsn)
			_, err := raw.Exec(
				"UPDATE eventing_subscription_sink SET "+c.set+", updated_at = ?, version = version + 1 WHERE tenant_id = ?",
				time.Now().UTC().Format(time.RFC3339Nano), tenant.String())
			if err == nil {
				t.Fatalf("a writer with no proof changed %s and the fence allowed it: that column is part of the RENDERED destination, so changing it sends the evidence somewhere else", c.column)
			}
			if !strings.Contains(err.Error(), "writer fence") {
				t.Fatalf("%s was refused, but not by the fence: %v", c.column, err)
			}
		})
	}
}

// Capability is a FLOOR, not an equality — and both halves of that matter.
//
// The trigger compares `capability >= 1`. Nothing tested either edge: a proof declaring 0 must be
// refused, and a proof declaring MORE than the current requirement must be ACCEPTED. The second is
// the forward-compatibility half and it is the one that would hurt in the field: a fleet mid-upgrade
// runs two versions at once, and a fence that demanded equality would lock out the NEWER binary —
// the one that carries more of the gate, not less. An equality would pass every test this suite had.
func TestCapabilityIsAFloorSoANewerWriterIsNotLockedOut(t *testing.T) {
	for _, c := range []struct {
		name       string
		capability int64
		refused    bool
	}{
		{"below the requirement", 0, true},
		{"exactly the requirement", EgressWriterCapability, false},
		{"a NEWER binary declaring more", EgressWriterCapability + 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, tenant, dsn := fencedSQLite(t, "cap"+strings.ReplaceAll(c.name, " ", ""))
			gen := armFence(t, st)
			raw := rawSQLite(t, dsn)

			nonce := freshNonce()
			if err := rawAttestInsert(raw, tenant, nonce, c.capability, gen); err != nil {
				t.Fatalf("write the attestation: %v", err)
			}
			err := rawSubscriptionInsert(raw, tenant, "sub", "https://s.example.com/h", &nonce)
			if c.refused {
				if err == nil {
					t.Fatalf("a proof declaring capability %d authorized a governed write, but the requirement is %d: the floor is not being applied", c.capability, EgressWriterCapability)
				}
				if !strings.Contains(err.Error(), "writer fence") {
					t.Fatalf("refused, but not by the fence: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a proof declaring capability %d was refused while the requirement is %d: a writer carrying MORE of the gate than this deployment demands must not be locked out — that is every newer node during an upgrade: %v", c.capability, EgressWriterCapability, err)
			}
		})
	}
}

// With NO classification row, the fence refuses — it does not fall through as if dormant.
//
// The dormant state is a RECORDED decision (`legacy_compat`). Its absence is not that decision: it
// means nothing can be established about whether an un-upgraded writer may author a destination, and
// the deny-closed answer is the only honest one. Nothing tested it, and the fall-through would have
// been invisible: every other test has a classification, so a trigger that treated "absent" as
// "dormant" would have passed the whole suite while making the fence disappear on exactly the
// database a partial restore produces.
func TestNoClassificationRowIsRefusedRatherThanTreatedAsDormant(t *testing.T) {
	st, tenant, dsn := fencedSQLite(t, "noclass")
	gen := armFence(t, st)
	raw := rawSQLite(t, dsn)

	// A proof that WOULD authorize the write, so what the case measures is the missing row and not
	// a missing attestation.
	nonce := freshNonce()
	if err := rawAttestInsert(raw, tenant, nonce, EgressWriterCapability, gen); err != nil {
		t.Fatalf("write the attestation: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM control_rollout_state WHERE control_key = ?", EgressWriterFenceControlKey); err != nil {
		t.Fatalf("remove the classification row: %v", err)
	}

	err := rawSubscriptionInsert(raw, tenant, "sub", "https://s.example.com/h", &nonce)
	if err == nil {
		t.Fatal("with no classification row the fence let a governed write through: absence of a decision is not the dormant decision, and a partial restore produces exactly this database")
	}
	if !strings.Contains(err.Error(), "no classification") {
		t.Fatalf("refused, but not for the missing classification — the message must name what is absent, or an operator cannot tell this from a spent proof: %v", err)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// (release gate S-5, U3). Measured that a TRUNCATE of the evidence
// ledger is caught by NOBODY: not the fence (a row trigger cannot see a
// statement-level TRUNCATE) and not the next boot (no pg_trigger or pg_proc
// field changes). Reproduced on a disposable Postgres on 2026-08-06 and both
// halves still hold — the engine reopened the truncated store and said nothing.
//
// It also found a THIRD blind spot did not name, and this is the one that
// can be closed from inside the engine. The scheduled checkpointer runs hourly
// by default and calls Checkpoint for every tenant. Checkpoint asks
// Audit().Head(), which reports the tip it can still SEE (the last event row) —
// so on a truncated ledger it sees "no events", concludes "nothing to checkpoint
// yet", and returns a SILENT no-op. The hourly job that exists to notarize the
// chain quietly notarizes nothing, and the failure metric never moves.
//
// The store holds both facts at that moment: audit_events is empty while
// audit_heads still records seq N. Those two together have exactly one meaning,
// and "an empty chain" is not it.
//
// What this does NOT claim to fix: a raw-DB attacker who rewrites BOTH tables
// consistently. That has never been what the head defends against
// (sqlstore/audit.go advanceHead says so).
//
// And naming the control for THAT case takes care, because the first version of
// this comment got it wrong and the Codex contrast caught it (F-06): per-event
// signatures and an off-box checkpoint key defend FORGERY, not COMPLETENESS. An
// actor who deletes a valid tail, rewinds audit_heads to a retained event and
// lets the scheduler re-notarize the new tip leaves every surviving signature
// valid and every in-tree verifier green — possessing an off-box private key is
// not the same as holding an off-box RECORD of what that key signed before.
// Completeness is detected by comparing against an EXTERNALLY RETAINED tip: an
// off-box copy of the anchor (checkpoint.go CheckpointStatusPending) or the DR
// manifest's recorded seq+hash (core/dr/dr.go, hazard 3).
//
// This closes the accidental and the naive case, on a schedule, which is what
// Left open.

func truncationTestStore(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn, Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// emptyTheLedger removes every event row for the tenant while leaving audit_heads
// intact — the shape a TRUNCATE leaves behind. It drops the append-only trigger
// first, because the point is to model an actor who is not going through the
// application at all.
func emptyTheLedger(t *testing.T, dsn string, tenant model.TenantID) (recordedHeadSeq int64) {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = raw.Close() }()
	// Lift ONLY the append-only DELETE guard, and put it back afterwards. Two
	// reasons, both load-bearing. A Postgres TRUNCATE leaves every trigger in
	// place — that is the whole point of finding — so a fixture that left
	// guards missing would be modeling a different, LOUDER attack: the reopen
	// self-test catches a missing isolation guard and refuses to open at all
	// (measured while writing this: "tenant tables missing isolation guard:
	// [audit_events]"). The state under test is the quiet one — a pristine schema
	// with no rows.
	var name, ddl string
	err = raw.QueryRow(`SELECT name, sql FROM sqlite_master
	                    WHERE type = 'trigger' AND tbl_name = 'audit_events' AND name LIKE '%_no_delete'`).Scan(&name, &ddl)
	if err != nil {
		t.Fatalf("find the append-only DELETE guard: %v", err)
	}
	if _, err := raw.Exec("DROP TRIGGER " + name); err != nil {
		t.Fatalf("drop %s: %v", name, err)
	}
	res, err := raw.Exec("DELETE FROM audit_events WHERE tenant_id = ?", tenant.String())
	if err == nil {
		if _, rerr := raw.Exec(ddl); rerr != nil {
			t.Fatalf("restore %s: %v", name, rerr)
		}
	}
	if err != nil {
		t.Fatalf("empty the ledger: %v", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		t.Fatal("the fixture deleted nothing, so it proves nothing about a truncated ledger")
	}
	var heads int
	if err := raw.QueryRow("SELECT count(*) FROM audit_heads WHERE tenant_id = ?", tenant.String()).Scan(&heads); err != nil {
		t.Fatalf("count heads: %v", err)
	}
	if heads != 1 {
		t.Fatalf("the recorded head is gone (%d rows): this fixture models a TRUNCATE of the EVENTS, and without a surviving head there is nothing to notice", heads)
	}
	if err := raw.QueryRow("SELECT seq FROM audit_heads WHERE tenant_id = ?", tenant.String()).Scan(&recordedHeadSeq); err != nil {
		t.Fatalf("read the surviving head: %v", err)
	}
	return recordedHeadSeq
}

func TestCheckpointRefusesAnEmptiedChainThatStillHasAHead(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit.db")
	st := truncationTestStore(t, dsn)
	tenant := provisionTenant(t, st) // seeds org.create at seq 1
	appendEvents(t, st, tenant, 4)   // seq 2..5

	signer := testSigner(t)

	// Control: the healthy chain checkpoints, so a failure below is the emptying
	// and not a broken fixture.
	if _, ok, err := signer.Checkpoint(ctx, st, tenant); err != nil || !ok {
		t.Fatalf("healthy chain did not checkpoint (ok=%v): %v", ok, err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close before emptying: %v", err)
	}
	survivingHeadSeq := emptyTheLedger(t, dsn, tenant)
	st2 := truncationTestStore(t, dsn)

	_, ok, err := signer.Checkpoint(ctx, st2, tenant)
	if err == nil {
		t.Fatalf("an emptied ledger checkpointed as if nothing were wrong (ok=%v): the hourly job that exists to notarize the chain notarized nothing and said nothing", ok)
	}
	if ok {
		t.Fatal("Checkpoint reported success while returning an error")
	}
	for _, want := range []string{"audit_heads", "seq " + strconv.FormatInt(survivingHeadSeq, 10)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the alarm must carry %q so an operator can tell it from a store outage, got: %v", want, err)
		}
	}

	// CheckpointAll is what the scheduler actually calls: the tenant must be named.
	allErr := signer.CheckpointAll(ctx, st2)
	if allErr == nil {
		t.Fatal("CheckpointAll swallowed the emptied chain — the scheduled path is the only one that runs unattended")
	}
	if !strings.Contains(allErr.Error(), tenant.String()) {
		t.Fatalf("CheckpointAll must name the affected tenant, got: %v", allErr)
	}
}

// TestCheckpointStillNoOpsOnAGenuinelyEmptyChain is the other half, and the
// reason the guard keys on the RECORDED head rather than on "zero events": a
// tenant nobody has written to yet is a normal state that must stay a silent
// no-op. A guard that cannot tell those apart would make the hourly checkpointer
// cry wolf on every fresh install.
func TestCheckpointStillNoOpsOnAGenuinelyEmptyChain(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	signer := testSigner(t)

	unwritten := model.NewTenantID()
	ev, ok, err := signer.Checkpoint(ctx, st, unwritten)
	if err != nil {
		t.Fatalf("a tenant with no events and no recorded head is a normal empty chain, not an incident: %v", err)
	}
	if ok || ev.Seq != 0 {
		t.Fatalf("an empty chain must produce no checkpoint, got ok=%v seq=%d", ok, ev.Seq)
	}
}

// testSigner keeps the two tests above independent of the harness in
// audit_test.go, which mints its key inline per test.
func testSigner(t *testing.T) *audit.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	s, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

// TestOneEmptiedTenantDoesNotStarveTheHealthyOnes closes what the adversarial
// contrast of 2026-08-06 found against the fix above (finding F-05, MEDIUM).
//
// The emptied-ledger alarm is PERSISTENT: it fires every tick until an operator
// repairs the database. CheckpointAll used to return on the first tenant error,
// and ListOrgs has a stable ORDER BY id ASC, so a single corrupt tenant that
// happens to sort first would abort the sweep before every later tenant and the
// system chain — for ever. The alarm would have cost exactly the anchors it
// exists to protect, and the previous silent no-op did NOT have that failure
// mode, so this was a regression introduced by making the tenant loud.
//
// A per-tenant integrity alarm must stay per-tenant: report it, and keep
// notarizing everyone else.
func TestOneEmptiedTenantDoesNotStarveTheHealthyOnes(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit.db")
	st := truncationTestStore(t, dsn)

	a := provisionTenant(t, st)
	b := provisionTenant(t, st)
	appendEvents(t, st, a, 2)
	appendEvents(t, st, b, 2)

	// Empty whichever tenant CheckpointAll reaches FIRST, so the test exercises
	// starvation rather than depending on which id happened to sort lower.
	var order []model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		for _, o := range orgs {
			order = append(order, o.TenantID)
		}
		return err
	}); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if len(order) < 2 {
		t.Fatalf("expected two tenants in the sweep order, got %d", len(order))
	}
	corrupt, healthy := order[0], order[1]
	if corrupt != a && corrupt != b {
		t.Fatalf("sweep order returned an unexpected tenant %s", corrupt)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close before emptying: %v", err)
	}
	emptyTheLedger(t, dsn, corrupt)
	st2 := truncationTestStore(t, dsn)

	err := signerFor(t).CheckpointAll(ctx, st2)
	if err == nil {
		t.Fatal("CheckpointAll hid the emptied tenant entirely")
	}
	if !strings.Contains(err.Error(), corrupt.String()) {
		t.Fatalf("the sweep must still name the corrupt tenant, got: %v", err)
	}

	// ...and the tenant behind it in the sweep still got its anchor.
	var checkpoints int
	if err := st2.View(ctx, healthy, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == audit.ActionCheckpoint {
				checkpoints++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk the healthy tenant: %v", err)
	}
	if checkpoints == 0 {
		t.Fatalf("tenant %s was not checkpointed because tenant %s ahead of it raised a tenant-local alarm: one corrupt ledger starves every chain behind it, every tick, for ever", healthy, corrupt)
	}
}

// signerFor is testSigner by another name, kept so the starvation test reads
// without a variable that only exists to be passed once.
func signerFor(t *testing.T) *audit.Signer { return testSigner(t) }

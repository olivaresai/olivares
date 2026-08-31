// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardaclwindow_pg_test.go closes the window between v6 committing the control plane and the
// boot revoking the application role's INSERT on it.
//
// THE WINDOW WAS REAL AND IT WAS THE WHOLE FIRST ROLLOUT. deploy/postgres/01-app-role.sql runs
// ALTER DEFAULT PRIVILEGES ... GRANT SELECT, INSERT, UPDATE, DELETE, which applies to every
// FUTURE table the owner creates — so the three logs were born insertable by app. The guards
// stop UPDATE and DELETE; nothing stopped INSERT. Open then ran the entire rollout before
// reconciling that ACL, so during it the application pool could append a receipt, a gate event
// or an inventory activation beside the engine's own, and every one of them would sit inside
// the checkpoint the close attests.
//
// Two tests because there are two halves and each fails differently:
//
//  1. the DDL half — the relations must be born without INSERT for a non-owning app role, in
//     the transaction that creates them; and
//  2. the ORDER half — a boot that finds the boundary open must refuse BEFORE it opens a
//     rollout, not after it has finished one.

// appRoleOf reads the role a DSN authenticates as.
func appRoleOf(t *testing.T, dsn string) string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var role string
	if err := db.QueryRowContext(context.Background(), "SELECT CURRENT_USER").Scan(&role); err != nil {
		t.Fatalf("read the connecting role: %v", err)
	}
	return role
}

// TestPostgresTheControlPlaneIsBornWithoutInsertForTheAppRole is the DDL half.
//
// It applies v6's own statement list on a freshly provisioned SPLIT database — the topology
// whose ALTER DEFAULT PRIVILEGES creates the hazard — and asks the server, from the
// application role's point of view, whether it can write the three logs. The answer must be no
// before anything else in boot has had a chance to run.
func TestPostgresTheControlPlaneIsBornWithoutInsertForTheAppRole(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()
	appRole := appRoleOf(t, dsns.App)

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	// The shared trigger function the control plane's guards call. In production an earlier
	// migration creates it; here only v6's statements are under test, so it is provided.
	if _, err := owner.ExecContext(ctx,
		`CREATE FUNCTION `+dialect.BlockMutationFn+`() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION 'immutable'; END $fn$`); err != nil {
		t.Fatalf("create the shared guard function: %v", err)
	}

	dia, ok := dialect.NewForAppRole(store.EnginePostgres, appRole)
	if !ok {
		t.Fatalf("bind the dialect to %q", appRole)
	}
	// ONE TRANSACTION, exactly as the migration runs it: the relations and their posture become
	// durable together or not at all.
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for i, stmt := range dia.GuardControlPlaneStmts() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("statement %d of the control plane DDL: %v\n%s", i+1, err, stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// THE APPLICATION POOL'S OWN VIEW. has_table_privilege asked as that role is the only
	// reading that accounts for a direct grant, a group role, PUBLIC and ownership alike.
	app, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open the app pool: %v", err)
	}
	defer func() { _ = app.Close() }()
	for _, table := range dialect.GuardControlPlaneTables() {
		var ins, upd, del, trunc bool
		if err := app.QueryRowContext(ctx,
			`SELECT has_table_privilege($1,'INSERT'), has_table_privilege($1,'UPDATE'),
			        has_table_privilege($1,'DELETE'), has_table_privilege($1,'TRUNCATE')`,
			table).Scan(&ins, &upd, &del, &trunc); err != nil {
			t.Fatalf("read the app role's privileges on %s: %v", table, err)
		}
		if ins || upd || del || trunc {
			t.Errorf("the application role %q can write %s the moment v6 commits (insert=%t update=%t delete=%t truncate=%t); ALTER DEFAULT PRIVILEGES granted it and the DDL did not take it away",
				appRole, table, ins, upd, del, trunc)
		}
		// AND THE PROOF IS THE STATEMENT, not only the catalog: a privilege bit can be read
		// wrong, a refused INSERT cannot.
		// event_sha256 is the one column all three logs share, and naming an absent one would
		// fail 42703 during parse — before PostgreSQL ever reaches the privilege check.
		_, ierr := app.ExecContext(ctx, `INSERT INTO `+table+` (event_sha256) VALUES ('\x00')`)
		if ierr == nil {
			t.Errorf("the application role inserted into %s immediately after v6 committed", table)
			continue
		}
		if !strings.Contains(ierr.Error(), "42501") && !strings.Contains(strings.ToLower(ierr.Error()), "permission denied") {
			// NOT a pass. Reaching a NOT NULL violation means the privilege check let the
			// statement through, which is the defect this test exists for — the row simply
			// happened to be incomplete as well.
			t.Errorf("the application role's INSERT into %s got past the privilege check and failed on the row instead: %v", table, ierr)
			continue
		}
		t.Logf("GUARD_ACL_AT_BIRTH|%s|app=%s|insert=refused", table, appRole)
	}
}

// TestPostgresTheRolloutDoesNotRunWhileTheAppRoleCanWriteTheControlPlane is the ORDER half.
//
// A name-targeted REVOKE cannot strip a privilege held through PUBLIC, so granting INSERT to
// PUBLIC produces a boundary the reconcile CANNOT close — which is exactly what makes this a
// test of ORDER rather than of end state. With the verification before the rollout the boot
// refuses having opened nothing; with it at the end of Open, as it was, the whole rollout runs
// first and the gate carries its full history.
//
// The discriminator is therefore the gate table's emptiness, not the refusal: both orders
// refuse.
func TestPostgresTheRolloutDoesNotRunWhileTheAppRoleCanWriteTheControlPlane(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	// A first boot, so the relations exist and the topology is the ordinary one.
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the first store: %v", cerr)
	}

	// NOW OPEN THE BOUNDARY IN A WAY THE RECONCILE CANNOT CLOSE. REVOKE ... FROM <role> does not
	// touch a privilege held through PUBLIC, and has_table_privilege is what sees it.
	for _, table := range dialect.GuardControlPlaneTables() {
		if _, err := owner.ExecContext(ctx, `GRANT INSERT ON `+table+` TO PUBLIC`); err != nil {
			t.Fatalf("grant INSERT on %s to PUBLIC: %v", table, err)
		}
	}
	// Wipe the gate and UNIT receipts so "did the rollout run?" has an unambiguous answer.
	// Keep inventory and the four exact bootstrap rows (three metadata attributions plus the
	// universal v7 completion): tracked-v7 preflight must authenticate those before it can
	// reach the ACL boundary this test measures. The append-only guards are in ALWAYS, so this
	// needs the owner to disable them — precisely the capability being denied to the app role.
	for _, target := range []struct {
		table string
		where string
	}{
		{dialect.GuardGateEventsTable, ""},
		{dialect.GuardReceiptsTable, " WHERE receipt_kind = 'unit'"},
	} {
		table := target.table
		if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` DISABLE TRIGGER USER`); err != nil {
			t.Fatalf("disable the guard on %s: %v", table, err)
		}
		if _, err := owner.ExecContext(ctx, `DELETE FROM `+table+target.where); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
		if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` ENABLE ALWAYS TRIGGER `+table+`_immutable`); err != nil {
			t.Fatalf("restore the guard on %s: %v", table, err)
		}
	}

	st2, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err == nil {
		_ = st2.Close()
		t.Fatal("a boot whose application role can INSERT into the control plane succeeded")
	}
	if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
		t.Fatalf("the refusal is not the append-only ACL one: %v", err)
	}

	// THE ORDER, measured. If the verification still ran at the END of Open, the rollout would
	// have opened, executed every unit and closed before the refusal — leaving a full history.
	var events, unitReceipts, bootstrapReceipts int64
	if err := owner.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dialect.GuardGateEventsTable).Scan(&events); err != nil {
		t.Fatalf("count gate events: %v", err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dialect.GuardReceiptsTable+
		` WHERE receipt_kind = 'unit'`).Scan(&unitReceipts); err != nil {
		t.Fatalf("count unit receipts: %v", err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+dialect.GuardReceiptsTable+
		` WHERE receipt_kind = 'bootstrap'`).Scan(&bootstrapReceipts); err != nil {
		t.Fatalf("count bootstrap receipts: %v", err)
	}
	if events != 0 {
		t.Errorf("the boot refused, but only after writing %d gate events: the rollout ran while the application role could append beside it", events)
	}
	if unitReceipts != 0 {
		t.Errorf("the boot refused, but only after writing %d unit receipts", unitReceipts)
	}
	if bootstrapReceipts != 4 {
		t.Errorf("the ACL fixture lost its authenticated v7 bootstrap prefix: got %d rows, want 4", bootstrapReceipts)
	}
	t.Logf("GUARD_ACL_BEFORE_ROLLOUT|refused=%v|gate_events=%d|unit_receipts=%d|bootstrap_receipts=%d",
		errors.Is(err, store.ErrAppendOnlyACLOpen), events, unitReceipts, bootstrapReceipts)
}

// TestPostgresTheCloseHoldsTheThreeLogsAgainstInsert is the mechanism of r2 F-02.
//
// The close attests a checkpoint over the inventory and the receipt streams, and it interprets
// the gate. Under READ COMMITTED each statement sees commits that landed since the previous
// one, so unless something conflicts with INSERT on those three relations, a row appended
// between the fold that interprets them and the checkpoint that attests them is inside the
// attestation and outside the verification.
//
// BOTH BRANCHES ARE ASSERTED, because the mode is decided by topology and only one of them is
// a fence:
//
//   - hardened (split): SHARE conflicts with ROW EXCLUSIVE, so a concurrent INSERT waits and
//     dies on its lock_timeout. Nothing can be appended while the close reads.
//   - single role: the application role owns these relations AND the append-only ACL revokes
//     UPDATE/DELETE/TRUNCATE from it, so SHARE is refused — measured, 42501 — and ROW EXCLUSIVE
//     is the strongest an explicit LOCK TABLE can take. It orders the close against DDL and
//     does NOT stop an INSERT. Asserting that here is what keeps the limit a measured fact
//     rather than a sentence in a comment.
func TestPostgresTheCloseHoldsTheThreeLogsAgainstInsert(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the store: %v", cerr)
	}

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	for _, tc := range []struct {
		name      string
		hardened  bool
		wantFence bool
	}{
		{"the hardened posture fences", true, true},
		{"the single-role posture orders but does not fence", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			holder, herr := owner.Conn(ctx)
			if herr != nil {
				t.Fatalf("check out the holding connection: %v", herr)
			}
			tx, terr := holder.BeginTx(ctx, nil)
			if terr != nil {
				_ = holder.Close()
				t.Fatalf("begin: %v", terr)
			}
			// REGISTERED IMMEDIATELY. A *sql.Conn with an open transaction does not return to the
			// pool on Close, so a t.Fatalf below with only a deferred Close leaves the session
			// alive and pgtest's DROP DATABASE waits for it forever.
			t.Cleanup(func() {
				_ = tx.Rollback()
				_ = holder.Close()
			})
			for _, l := range guardCloseMetadataLocks(guardCloseMetadataMode(tc.hardened)) {
				if _, lerr := tx.ExecContext(ctx, l.lockStatement()); lerr != nil {
					t.Fatalf("take %s at the close's mode: %v", l.displayRelation(), lerr)
				}
			}

			writer, werr := owner.Conn(ctx)
			if werr != nil {
				t.Fatalf("check out the writing connection: %v", werr)
			}
			defer func() { _ = writer.Close() }()
			if _, serr := writer.ExecContext(ctx, "SET lock_timeout = '600ms'"); serr != nil {
				t.Fatalf("arm the writer's lock timeout: %v", serr)
			}
			// The relation lock is taken when the executor opens the relation, before any
			// constraint is evaluated — so an incomplete row still proves whether it waited.
			_, ierr := writer.ExecContext(ctx,
				`INSERT INTO `+dialect.GuardGateEventsTable+` (event_sha256) VALUES ('\x00')`)
			blocked := ierr != nil && strings.Contains(ierr.Error(), "55P03")
			if blocked != tc.wantFence {
				t.Fatalf("with hardened=%t a concurrent INSERT blocked=%t, want %t (err: %v)",
					tc.hardened, blocked, tc.wantFence, ierr)
			}
			t.Logf("GUARD_CLOSE_FENCE|hardened=%t|mode=%s|concurrent_insert_blocked=%t|err=%v",
				tc.hardened, lockModeSQL[guardCloseMetadataMode(tc.hardened)], blocked, ierr)
		})
	}
}

// gateHead reads the current head and count of the gate for a rollout.
//
// WITH ONLY, like every production read: a bare name means "this table and its descendants", so
// a helper without it would report a child's smuggled rows as the ledger's own and make the
// inheritance test measure the helper instead of the engine. See guardOnly.
func gateHead(t *testing.T, db *sql.DB, rolloutID string) (int64, string) {
	t.Helper()
	var n int64
	var kind sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ONLY `+dialect.GuardGateEventsTable+` WHERE rollout_id = $1`, rolloutID).Scan(&n); err != nil {
		t.Fatalf("count gate events: %v", err)
	}
	if err := db.QueryRowContext(context.Background(),
		`SELECT kind FROM ONLY `+dialect.GuardGateEventsTable+` WHERE rollout_id = $1 ORDER BY event_ordinal DESC LIMIT 1`,
		rolloutID).Scan(&kind); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read the gate head: %v", err)
	}
	return n, kind.String
}

// deleteGateTail removes the last event of a rollout, restoring the guard afterwards.
//
// It needs the OWNER and it needs the append-only trigger disabled, which is the point: the
// capability being modeled is not one the application role has after r2 F-01.
func deleteGateTail(t *testing.T, owner *sql.DB, rolloutID string) {
	t.Helper()
	ctx := context.Background()
	tbl := dialect.GuardGateEventsTable
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+tbl+` DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable the guard: %v", err)
	}
	if _, err := owner.ExecContext(ctx,
		`DELETE FROM `+tbl+` WHERE rollout_id = $1 AND event_ordinal = (SELECT MAX(event_ordinal) FROM `+tbl+` WHERE rollout_id = $1)`,
		rolloutID); err != nil {
		t.Fatalf("delete the gate tail: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+tbl+` ENABLE ALWAYS TRIGGER `+tbl+`_immutable`); err != nil {
		t.Fatalf("restore the guard: %v", err)
	}
}

// TestPostgresDeletingAGateTailThroughARealBoot is r2 F-13, through Open rather than through
// the fold.
//
// WHAT THE OLD TEST PROVED AND WHAT ITS COMMENT CLAIMED were different things. The `tailDeletion`
// case called foldGateEvents directly, checked the phase was no longer ready, and returned —
// while its comment, and line 7 of the implementation amendment, asserted that "the coordinator
// prevents this being a silent downgrade". The coordinator was never invoked.
//
// WHAT THIS TEST EXERCISES — two of the three deletions; the third has a test of its own.
//
// An earlier header claimed three separate tail deletions and the body performed two, one of
// them a receipt. Round three caught that, and it is the same defect the whole campaign is
// about: a comment asserting more than the code below it. The two here are:
//
//  1. `ready` deleted: RE-ATTESTATION. The next boot finds a pending rollout with every receipt
//     durable, re-reads every object, and closes again. Nothing is taken on trust.
//  2. `ready` deleted AND a unit's receipt removed: REFUSAL. The relaxed condition is only
//     legitimate on the strength of an authenticated edge; with the receipt gone the boot must
//     not proceed, and it names `judged reading and no receipt`.
//
// The third — `verification-failed`, the one that is ACCEPTED — is
// TestPostgresDeletingTheVerificationFailedTail, built end to end by real writers.
//
// STILL OWED, and recorded as owed rather than described as done: an `attempt-failed` tail. It
// is not producible by wiring alone — a failed attempt rolls back without a receipt, and forcing
// one needs the failure path driven rather than synthesized — so "attempt-failed plus its unit's
// receipt" is not a history an honest writer produces at all.
//
// THE RESIDUAL LIMIT ITSELF is unchanged: a closing event cannot attest its own successor, so
// deleting the last row of the gate leaves a shorter, valid history. It requires the ability to
// disable an ALWAYS trigger on an owner's table AND to delete from an append-only log — which
// after r2 F-01 the application role does not have — and closing it needs an anchor outside this
// database.
func TestPostgresDeletingAGateTailThroughARealBoot(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	var rolloutID string
	if err := owner.QueryRowContext(ctx,
		`SELECT rollout_id FROM `+dialect.GuardGateEventsTable+` ORDER BY event_ordinal LIMIT 1`).Scan(&rolloutID); err != nil {
		t.Fatalf("read the rollout id: %v", err)
	}
	before, head := gateHead(t, owner, rolloutID)
	if head != "ready" {
		t.Fatalf("the first boot left the gate at %q, want ready", head)
	}

	// 1. THE CLOSING EVENT, deleted. The next boot must re-attest, not assume.
	deleteGateTail(t, owner, rolloutID)
	if _, h := gateHead(t, owner, rolloutID); h == "ready" {
		t.Fatal("the closing event was not removed")
	}
	st2, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("a boot over a rollout whose closing event was deleted must re-attest, and it failed: %v", err)
	}
	if cerr := st2.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	after, head := gateHead(t, owner, rolloutID)
	if head != "ready" {
		t.Fatalf("the boot did not re-close the rollout: the gate head is %q", head)
	}
	if after != before {
		t.Logf("GUARD_TAIL_REATTESTED|events_before=%d|events_after=%d", before, after)
	}

	// 2. A BLOCKING FAILURE AND ITS RECEIPT, both deleted. The condition may only relax on the
	// strength of an authenticated edge.
	deleteGateTail(t, owner, rolloutID) // remove `ready` again, so the rollout is pending
	var unitID, unitKey string
	if err := owner.QueryRowContext(ctx,
		`SELECT unit_id, relation_name FROM `+dialect.GuardReceiptsTable+
			` WHERE rollout_id = $1 AND receipt_kind = 'unit' ORDER BY event_ordinal DESC LIMIT 1`,
		rolloutID).Scan(&unitID, &unitKey); err != nil {
		t.Fatalf("read a unit receipt: %v", err)
	}
	rcpt := dialect.GuardReceiptsTable
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+rcpt+` DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable the receipt guard: %v", err)
	}
	if _, err := owner.ExecContext(ctx,
		`DELETE FROM `+rcpt+` WHERE rollout_id = $1 AND unit_id = $2`, rolloutID, unitID); err != nil {
		t.Fatalf("delete the receipt: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+rcpt+` ENABLE ALWAYS TRIGGER `+rcpt+`_immutable`); err != nil {
		t.Fatalf("restore the receipt guard: %v", err)
	}
	st3, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st3.Close()
		t.Fatalf("a boot whose unit %s (%s) has a judged reading and no receipt succeeded; the truncation was laundered", unitID, unitKey)
	}
	t.Logf("GUARD_TAIL_REFUSED_WITHOUT_EDGE|unit=%s|target=%s|err=%v", unitID, unitKey, err)
}

// TestPostgresTheEscalationClosureReadsCreateRolePerMajor is r2 F-09 and mutant r2 N-06.
//
// The old split-escalation test only ever created memberships that ALREADY existed. It never
// exercised the capability to create one AFTERWARDS — and on PostgreSQL 15 the official GRANT
// page says a role with CREATEROLE may grant or revoke membership in any non-superuser role.
// So an application role with CREATEROLE could pass the check and then hand itself the owner.
//
// From 16 the model changed: CREATEROLE only permits granting roles the grantor holds with
// ADMIN OPTION, so the attribute alone is no longer the hazard and refusing on it would refuse
// a deployment that is safe. Both answers come from the same function, chosen by major.
//
// IT DOES NOT TOUCH THE SHARED APPLICATION ROLE. olivares_app is cluster-global on this host
// and ALTERing it breaks every other isolated test, so the subject is a scratch role created
// and dropped here — and the cleanup is registered BEFORE the role exists.
func TestPostgresTheEscalationClosureReadsCreateRolePerMajor(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open the superuser pool: %v", err)
	}
	// t.Cleanup, NOT defer: cleanups run LIFO AFTER the function returns while a deferred
	// close runs DURING it, so a DROP ROLE registered as a cleanup found a closed pool and its
	// error was discarded — the role survived a green test. Registering the close FIRST makes
	// LIFO run it LAST, after every restore that needs it.
	t.Cleanup(func() { _ = super.Close() })

	// Names derived from the database, which pgtest already makes unique per test AND per
	// container — so unlike the matrix fixtures these cannot collide with another lane. Roles are
	// cluster-global, so deriving the name rather than fixing it is what makes that true.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(dsns.Database))
	subject := "esc_app_" + safe
	target := "esc_tgt_" + safe
	t.Cleanup(func() {
		for _, r := range []string{subject, target} {
			// The error is REPORTED, not discarded. A cleanup that swallows its failure is how
			// eleven roles survived a fully green suite across four servers.
			if _, err := super.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+r+`"`); err != nil {
				t.Errorf("LEAKED cluster-scoped role %q: %v", r, err)
			}
		}
	})
	for _, stmt := range []string{
		`CREATE ROLE "` + subject + `" NOSUPERUSER NOCREATEROLE`,
		`CREATE ROLE "` + target + `" NOSUPERUSER CREATEROLE`,
		`GRANT "` + target + `" TO "` + subject + `"`,
	} {
		if _, err := super.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("provision the escalation fixture (%s): %v", stmt, err)
		}
	}

	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	// THE MAJOR IS THE SERVER'S, not a number chosen here.
	//
	// An earlier version asked guardMetadataEscalations for `major=16` while running against a
	// 15 server, which was a fiction: the 16+ predicate uses pg_has_role(..., 'SET'), and 15
	// answers `unrecognized privilege type: "SET"`. The per-major SEMANTICS are measured against
	// real 16/17/18 servers in guardmajormatrix_pg_test.go; what this test owns is CREATEROLE,
	// which is a plain attribute readable on every major.
	major, err := postgresMajorVia(ctx, owner)
	if err != nil {
		t.Fatalf("read the server major: %v", err)
	}

	esc, err := guardMetadataEscalations(ctx, owner, subject, major)
	if err != nil {
		t.Fatalf("read the closure on major %d: %v", major, err)
	}
	namedForCreateRole := false
	for _, e := range esc {
		if e.Role == target && strings.Contains(e.Why, "CREATEROLE") {
			namedForCreateRole = true
		}
	}
	switch {
	case major < 16:
		// On 15 the official GRANT page says CREATEROLE permits granting membership in ANY
		// non-superuser role, so a reachable role holding it can hand the subject the owner
		// AFTER this check has passed.
		if !namedForCreateRole {
			t.Errorf("on PostgreSQL %d a reachable role holding CREATEROLE is an escalation and the closure did not name %q for it: %v", major, target, esc)
		}
	default:
		// From 16 CREATEROLE only permits granting roles held WITH ADMIN OPTION, which the
		// reachability predicate already covers — so the attribute alone must not be the reason.
		if namedForCreateRole {
			t.Errorf("on PostgreSQL %d CREATEROLE alone is not an escalation and the closure named %q for it: %v", major, target, esc)
		}
	}
	t.Logf("GUARD_ESCALATION_CREATEROLE|reachable|major=%d|named_for_createrole=%t", major, namedForCreateRole)

	// AND THE SUBJECT'S OWN ATTRIBUTES, which the closure query cannot see because it excludes
	// by name the role it is asked about.
	if _, err := super.ExecContext(ctx, `ALTER ROLE "`+subject+`" CREATEROLE`); err != nil {
		t.Fatalf("give the subject CREATEROLE: %v", err)
	}
	self, err := guardMetadataEscalations(ctx, owner, subject, major)
	if err != nil {
		t.Fatalf("read the closure on major %d: %v", major, err)
	}
	switch {
	case major < 16:
		if !escalationNames(self, subject) {
			t.Errorf("on PostgreSQL %d an application role holding CREATEROLE can grant itself any non-superuser role after the check, and the closure did not name it: %v", major, self)
		}
	default:
		if escalationNames(self, subject) {
			t.Errorf("on PostgreSQL %d the subject's own CREATEROLE must not be an escalation by itself: %v", major, self)
		}
	}
	t.Logf("GUARD_ESCALATION_CREATEROLE|self|major=%d|named=%t", major, escalationNames(self, subject))
}

func escalationNames(list []guardMetadataEscalation, role string) bool {
	for _, e := range list {
		if e.Role == role {
			return true
		}
	}
	return false
}

// TestPostgresALegacyFunctionOwnerRefusesAndRecovers is r2 F-08's literal closure condition.
//
// THE BRICK IT PROVES IS GONE. The canonical form deliberately excludes the function's OWNER —
// a legitimate installation may have created it under any role — while ALTER FUNCTION requires
// ownership. So on a database where an older edition created it under a previous role, the
// preflight passed, v6 reused the function, and the FIRST unit's fence failed 42501; that
// failure classifies permanent and appends `pending/blocked`, and a blocked rollout refuses to
// mutate on every later boot. Transferring ownership afterwards fixed nothing, and this edition
// ships no repair CLI.
//
// Three claims, in the order that makes the fourth one meaningful:
//
//  1. the boot REFUSES, naming the owner and the remedy;
//  2. it wrote NOTHING — no gate event, no receipt — so there is no `blocked` to recover from;
//  3. after the documented remediation the next boot succeeds.
//
// THE TRADE-OFF IS DELIBERATE AND STATED. The check runs before the rollout is even folded, so
// it also refuses a deployment whose rollout is already `ready` and whose function owner changed
// afterwards — a boot that would take no fence at all. That is fail-closed on a control plane
// whose entire purpose is attested schema control, the remedy is one statement, and the
// alternative is a check that cannot run before `pending-opened`, which is what the finding
// required.
func TestPostgresALegacyFunctionOwnerRefusesAndRecovers(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 4}

	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open the superuser pool: %v", err)
	}
	// t.Cleanup, NOT defer: cleanups run LIFO AFTER the function returns while a deferred
	// close runs DURING it, so a DROP ROLE registered as a cleanup found a closed pool and its
	// error was discarded — the role survived a green test. Registering the close FIRST makes
	// LIFO run it LAST, after every restore that needs it.
	t.Cleanup(func() { _ = super.Close() })
	owner, err := sql.Open("pgx", dsns.Owner)
	if err != nil {
		t.Fatalf("open the owner pool: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	// A role the migration role is NOT a member of, standing in for "whoever created this
	// function two editions ago".
	legacy := "legacy_fn_" + strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, strings.ToLower(dsns.Database))
	t.Cleanup(func() {
		if _, err := super.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+legacy+`"`); err != nil {
			t.Errorf("LEAKED cluster-scoped role %q: %v", legacy, err)
		}
	})
	if _, err := super.ExecContext(ctx, `CREATE ROLE "`+legacy+`" NOSUPERUSER NOCREATEROLE`); err != nil {
		t.Fatalf("create the legacy owner: %v", err)
	}

	fn := canonicalGuardDefinition().Function
	// The function is created by an earlier migration, so it must exist before it can be
	// re-owned: a first boot both creates it and gives the recovery something to return to.
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	var migrationOwner string
	if err := owner.QueryRowContext(ctx, `SELECT o.rolname
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_roles o ON o.oid = p.proowner
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`, fn.Schema, fn.Name).Scan(&migrationOwner); err != nil {
		t.Fatalf("read the current function owner: %v", err)
	}

	// Clear the GATE only, so "wrote nothing" has an unambiguous answer.
	//
	// Not the other two: the inventory's activations and the three metadata bootstrap receipts are
	// written ONCE, by v6, and are never recreated — wiping them makes the recovery boot fail
	// for a reason that has nothing to do with this finding, which is what the first version of
	// this test did.
	gate := dialect.GuardGateEventsTable
	wipe := func(table, where string) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` DISABLE TRIGGER USER`); err != nil {
			t.Fatalf("disable the guard on %s: %v", table, err)
		}
		if _, err := owner.ExecContext(ctx, `DELETE FROM `+table+where); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
		if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` ENABLE ALWAYS TRIGGER `+table+`_immutable`); err != nil {
			t.Fatalf("restore the guard on %s: %v", table, err)
		}
	}
	// THE GATE AND THE UNIT RECEIPTS, AND NOTHING ELSE — which is the state a database that has
	// never opened a rollout is in, and the only state in which "the refusal wrote nothing" is a
	// question about this finding.
	//
	// Not the inventory and not the BOOTSTRAP receipts: v6 writes those once and never again, so
	// wiping them makes the recovery fail for an unrelated reason. And not the unit receipts
	// alone: leaving them beside an empty gate is a state no history produces — the plan would
	// be re-derived from guards now at ALWAYS while the receipts attest an adoption from 'O' —
	// and the first two versions of this test failed on exactly those two mistakes.
	wipe(gate, "")
	wipe(dialect.GuardReceiptsTable, ` WHERE receipt_kind = 'unit'`)
	if _, err := super.ExecContext(ctx,
		`ALTER FUNCTION `+quoteIdent(fn.Schema)+`.`+quoteIdent(fn.Name)+`() OWNER TO "`+legacy+`"`); err != nil {
		t.Fatalf("hand the function to the legacy owner: %v", err)
	}

	// 1 and 2: refusal, and nothing durable behind it.
	st2, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st2.Close()
		t.Fatal("a boot whose session cannot ALTER the shared guard function succeeded; the fence it depends on could not have been taken")
	}
	if !strings.Contains(err.Error(), legacy) {
		t.Errorf("the refusal does not name the owner %q, so it does not tell an operator what to fix: %v", legacy, err)
	}
	if !strings.Contains(err.Error(), "OWNER TO") && !strings.Contains(err.Error(), "GRANT") {
		t.Errorf("the refusal names no remedy: %v", err)
	}
	var events int64
	if err := owner.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+gate).Scan(&events); err != nil {
		t.Fatalf("count gate events: %v", err)
	}
	if events != 0 {
		t.Fatalf("the refusal left %d gate events behind; a `pending/blocked` written here is exactly the brick this check exists to remove", events)
	}

	// 3: the documented remediation, and a boot that gets past it.
	if _, err := super.ExecContext(ctx,
		`ALTER FUNCTION `+quoteIdent(fn.Schema)+`.`+quoteIdent(fn.Name)+`() OWNER TO "`+migrationOwner+`"`); err != nil {
		t.Fatalf("apply the documented remediation: %v", err)
	}
	st3, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("after transferring ownership back the boot still failed, so the refusal was not recoverable: %v", err)
	}
	if cerr := st3.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if err := owner.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+gate).Scan(&events); err != nil {
		t.Fatalf("count gate events: %v", err)
	}
	if events == 0 {
		t.Error("the recovered boot wrote no gate events, so it did not actually run the rollout")
	}
	t.Logf("GUARD_LEGACY_OWNER|refused_with_zero_writes=true|recovered_events=%d|owner=%s->%s", events, legacy, migrationOwner)
}

// TestPostgresTheDurableDiagnosticIsIdempotentAndSurvivesACancelledCaller is r3 F-11.
//
// recordGuardVerificationFailure and guardDiagnosticAlreadyRecorded had no reference from any
// test, and both had executable defects: the re-read that proves idempotence ran while the
// transaction the 23505 had just aborted was still open, and the write inherited a context that
// had usually just been canceled. Three claims, one per defect plus the one they exist for:
//
//  1. the SAME failure twice is one row and the second call succeeds — the re-read has to work
//     on a connection whose transaction is closed, which is what the fix was;
//  2. a DIFFERENT failure is a second row, not a swallowed 23505. Treating every unique
//     violation as idempotence also swallowed collisions on (rollout, event_ordinal), which mean
//     the diagnostic was never written while the caller was told it had been;
//  3. a caller whose context is already canceled still gets its diagnostic recorded.
func TestPostgresTheDurableDiagnosticIsIdempotentAndSurvivesACanceledCaller(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}

	m, err := buildGuardManifest([]string{"audit_events"})
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	var rolloutID string
	if err := db.QueryRowContext(ctx,
		`SELECT rollout_id FROM `+dialect.GuardGateEventsTable+` ORDER BY event_ordinal LIMIT 1`).Scan(&rolloutID); err != nil {
		t.Fatalf("read the rollout id: %v", err)
	}
	var format, epoch int64
	var codeSHA []byte
	if err := db.QueryRowContext(ctx,
		`SELECT manifest_format, code_epoch, code_sha256 FROM `+dialect.GuardGateEventsTable+
			` WHERE rollout_id = $1 ORDER BY event_ordinal LIMIT 1`, rolloutID).Scan(&format, &epoch, &codeSHA); err != nil {
		t.Fatalf("read the rollout tuple: %v", err)
	}
	code, err := scanDigest(codeSHA, "the rollout's code digest")
	if err != nil {
		t.Fatal(err)
	}
	rollout := guardRolloutContext{RolloutID: rolloutID, Format: format, CodeEpoch: epoch, CodeSHA256: code}
	spec := m.Specs[0]
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	count := func() int64 {
		t.Helper()
		var n int64
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+dialect.GuardGateEventsTable+` WHERE kind = 'verification-failed'`).Scan(&n); err != nil {
			t.Fatalf("count the diagnostics: %v", err)
		}
		return n
	}

	first := guardPlanRefusal{Key: spec.Key, Code: "DRIFT_A", Detail: "the guard is not the declared object"}
	if err := recordGuardVerificationFailure(ctx, conn, dia, rollout, unitID, first, gatePhaseReady); err != nil {
		t.Fatalf("record the first diagnostic: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("after one diagnostic the ledger holds %d", got)
	}

	// 1. THE SAME FAILURE AGAIN. Same fingerprint, so 23505 — and the re-read that turns it into
	// idempotence has to happen after the aborted transaction is closed.
	if err := recordGuardVerificationFailure(ctx, conn, dia, rollout, unitID, first, gatePhaseReady); err != nil {
		t.Fatalf("the same failure twice was not idempotent: %v", err)
	}
	if got := count(); got != 1 {
		t.Errorf("the same failure twice produced %d rows", got)
	}

	// 2. A DIFFERENT FAILURE. Different fingerprint, so it must land rather than be swallowed.
	second := guardPlanRefusal{Key: spec.Key, Code: "DRIFT_B", Detail: "the guard is missing entirely"}
	if err := recordGuardVerificationFailure(ctx, conn, dia, rollout, unitID, second, gatePhaseReady); err != nil {
		t.Fatalf("record a different diagnostic: %v", err)
	}
	if got := count(); got != 2 {
		t.Errorf("a genuinely different failure produced %d rows in total, want 2", got)
	}

	// 3. A CALLER WHOSE CONTEXT IS ALREADY DEAD. This is the ordinary case, not the exotic one:
	// the diagnostic is written after a failure, and the failure is often the cancellation.
	dead, cancel := context.WithCancel(ctx)
	cancel()
	third := guardPlanRefusal{Key: spec.Key, Code: "DRIFT_C", Detail: "recorded from a canceled caller"}
	if err := recordGuardVerificationFailure(dead, conn, dia, rollout, unitID, third, gatePhaseReady); err != nil {
		t.Fatalf("a canceled caller lost its durable diagnostic: %v", err)
	}
	if got := count(); got != 3 {
		t.Errorf("after the canceled-caller diagnostic the ledger holds %d rows, want 3", got)
	}

	// AND THE PHASE IS THE ONE THE CALLER PASSED, not a constant. A drift found by the fast path
	// runs on a rollout that is already ready, and the row an operator reads must say so.
	var phases int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+dialect.GuardGateEventsTable+` WHERE kind = 'verification-failed' AND phase = 'ready'`).Scan(&phases); err != nil {
		t.Fatalf("read the recorded phases: %v", err)
	}
	if phases != 3 {
		t.Errorf("%d of 3 diagnostics recorded the phase the caller folded", phases)
	}
	t.Logf("GUARD_DIAGNOSTIC_IDEMPOTENCE|same=1_row|different=2_rows|cancelled_caller=recorded|phase_ready=%d", phases)
}

// TestPostgresDeletingTheVerificationFailedTail is the third deletion r2 F-13 asks for, and
// the one that is ACCEPTED.
//
// Unlike the other two it is built by REAL writers end to end: a clean boot closes the rollout,
// a target's guard is then disabled, and the next boot's fast path finds the drift and records
// `verification-failed` itself — nothing here fabricates a gate event. From that state:
//
//  1. while the drift and its record both stand, the boot REFUSES, and it refuses without
//     re-reading the objects — the drift is the event, and a guard that looks correct again
//     does not unmake it;
//  2. deleting ONLY that last event, and reverting the drift, lets the next boot succeed.
//
// THE SECOND IS THE LIMIT, and it is asserted rather than described. A closing event cannot
// attest its own successor, so the last row of the gate has nothing behind it. What bounds it is
// capability, not cryptography: the deletion needs an ALWAYS trigger disabled on an owner's
// table, which after r2 F-01 the application role cannot do — and closing it properly needs an
// anchor outside this database.
func TestPostgresDeletingTheVerificationFailedTail(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	var rolloutID string
	if err := db.QueryRowContext(ctx,
		`SELECT rollout_id FROM `+dialect.GuardGateEventsTable+` ORDER BY event_ordinal LIMIT 1`).Scan(&rolloutID); err != nil {
		t.Fatalf("read the rollout id: %v", err)
	}
	if _, head := gateHead(t, db, rolloutID); head != "ready" {
		t.Fatalf("the first boot left the gate at %q, want ready", head)
	}

	// THE DRIFT, applied to a real target. Its guard goes to ORIGIN, which is the state a
	// logical-replication apply mutates the log in silence from.
	var schema, relation, trigger string
	if err := db.QueryRowContext(ctx,
		`SELECT relation_schema, relation_name, trigger_name FROM `+dialect.GuardReceiptsTable+
			` WHERE rollout_id = $1 AND receipt_kind = 'unit' ORDER BY event_ordinal LIMIT 1`,
		rolloutID).Scan(&schema, &relation, &trigger); err != nil {
		t.Fatalf("read a target from the receipts: %v", err)
	}
	target := quoteIdent(schema) + "." + quoteIdent(relation)
	if _, err := db.ExecContext(ctx, `ALTER TABLE ONLY `+target+` ENABLE REPLICA TRIGGER `+quoteIdent(trigger)); err != nil {
		t.Fatalf("drift the guard on %s: %v", target, err)
	}

	// The boot that FINDS it writes the verification failure itself.
	st2, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st2.Close()
		t.Fatal("a boot over a drifted guard succeeded")
	}
	events, head := gateHead(t, db, rolloutID)
	if head != "verification-failed" {
		t.Fatalf("the drift did not produce a verification failure; the gate head is %q", head)
	}

	// 1. IT STAYS REFUSED even once the object is correct again: the drift is the event.
	if _, err := db.ExecContext(ctx, `ALTER TABLE ONLY `+target+` ENABLE ALWAYS TRIGGER `+quoteIdent(trigger)); err != nil {
		t.Fatalf("revert the drift: %v", err)
	}
	st3, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st3.Close()
		t.Fatal("restoring the guard was enough to boot again; that is laundering with extra steps")
	}
	if !strings.Contains(err.Error(), "closed and then recorded drift") {
		t.Errorf("the refusal is not the recorded-drift one: %v", err)
	}

	// 2. AND THE LIMIT: delete that one row and the previous ready/verified is exposed again.
	//
	// THE CLAIM THIS COMMENT USED TO MAKE WAS FALSE, and round four was right to refute it. It
	// said the deletion "needs a superuser here", on the strength of the append-only ACL having
	// revoked UPDATE/DELETE/TRUNCATE from the role that owns the gate. Revoking a privilege from
	// a table's OWNER removes nothing it cannot restore: PostgreSQL grants an owner every
	// privilege on its own tables implicitly, together with the grant option
	// (https://www.postgresql.org/docs/16/sql-grant.html). Measured right here, on the
	// application role itself, so the correction is a fact of this run and not a retraction in
	// prose.
	//
	// So the honest bound is NOT "it takes a superuser". It is: in the single-role topology the
	// ledger is durable against a crash and against the application's runtime traffic, and NOT
	// against the role itself — which is exactly what boot warns about when it recommends
	// --owner-dsn, and why the hardened split exists.
	ownerCanRestoreItsOwnDelete(t, db)

	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open the superuser pool: %v", err)
	}
	// t.Cleanup, NOT defer: cleanups run LIFO AFTER the function returns while a deferred
	// close runs DURING it, so a DROP ROLE registered as a cleanup found a closed pool and its
	// error was discarded — the role survived a green test. Registering the close FIRST makes
	// LIFO run it LAST, after every restore that needs it.
	t.Cleanup(func() { _ = super.Close() })
	deleteGateTail(t, super, rolloutID)
	after, head := gateHead(t, super, rolloutID)
	if head != "ready" {
		t.Fatalf("after deleting the tail the gate head is %q, want the ready it was hiding", head)
	}
	if after != events-1 {
		t.Fatalf("the deletion removed %d events, want 1", events-after)
	}
	st4, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("this case documents an ACCEPTED deletion and the boot refused, so the limit has changed and this test's claim with it: %v", err)
	}
	if cerr := st4.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	t.Logf("GUARD_TAIL_VERIFICATION_FAILED|refused_while_recorded=true|accepted_after_tail_deleted=true|target=%s", target)
}

// TestPostgresAnInheritedChildCannotSmuggleRowsIntoTheLedger is round four's new finding, and
// it is the sharpest one four rounds produced: a single CREATE TABLE undoes the ledger's whole
// ordering argument.
//
// MEASURED ON 15.18 BEFORE ANY CODE WAS WRITTEN:
//
//	CREATE TABLE inh_child () INHERITS (inh_parent);
//	INSERT INTO inh_parent VALUES (1,'real');
//	INSERT INTO inh_child  VALUES (1,'forged');   -- SAME primary key, ACCEPTED
//	SELECT count(*) FROM inh_parent       -> 2
//	SELECT count(*) FROM ONLY inh_parent  -> 1
//	indexes on the child                  -> 0
//
// A child inherits the columns and the CHECKs and inherits neither the unique indexes nor the
// triggers, while a bare SELECT on the parent reads its rows. So rows written into a child are
// folded as history, can repeat the ordinal the chains are ordered by, and can be updated or
// deleted at will.
//
// TWO LAYERS, ONE TEST EACH WAY ROUND:
//
//  1. every read is qualified with ONLY, so the smuggled rows are not folded — asserted by the
//     boot succeeding and the fold being unchanged while a child holds a duplicate row;
//  2. the shape check refuses a relation that HAS a child, so the anomaly is reported rather
//     than stepped over — asserted by the next boot refusing and naming it.
func TestPostgresAnInheritedChildCannotSmuggleRowsIntoTheLedger(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	gate := dialect.GuardGateEventsTable
	var rolloutID string
	if err := db.QueryRowContext(ctx,
		`SELECT rollout_id FROM ONLY `+gate+` ORDER BY event_ordinal LIMIT 1`).Scan(&rolloutID); err != nil {
		t.Fatalf("read the rollout id: %v", err)
	}
	before, _ := gateHead(t, db, rolloutID)

	// THE ATTACK: a child of the gate, and a row copied into it. It duplicates an ordinal the
	// parent already holds, which the parent's unique index would have refused.
	child := gate + "_smuggled"
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+child) })
	if _, err := db.ExecContext(ctx, `CREATE TABLE `+child+` () INHERITS (`+gate+`)`); err != nil {
		t.Fatalf("create the inheriting child: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO `+child+` SELECT * FROM ONLY `+gate+` WHERE rollout_id = $1 ORDER BY event_ordinal LIMIT 1`,
		rolloutID); err != nil {
		t.Fatalf("smuggle a row into the child: %v", err)
	}
	// The child took a row the parent's uniqueness would have refused, which is the premise.
	var smuggled int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+child).Scan(&smuggled); err != nil {
		t.Fatalf("count the smuggled rows: %v", err)
	}
	if smuggled != 1 {
		t.Fatalf("the child holds %d rows, so the premise of this test does not hold", smuggled)
	}
	var visible int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+gate).Scan(&visible); err != nil {
		t.Fatalf("count without ONLY: %v", err)
	}
	if visible != before+1 {
		t.Fatalf("a bare SELECT on the parent sees %d rows and the parent holds %d; the child is not being read at all, so this test proves nothing", visible, before)
	}

	// 1. THE READS — asserted on the PRODUCTION fold, not on a helper. foldGateEvents is what
	// decides the rollout's phase, and it is the reader that must not see the child.
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	proj, ferr := foldGateEvents(ctx, db, dia, rolloutID)
	if ferr != nil {
		t.Fatalf("the production fold failed over a ledger with an inheriting child: %v", ferr)
	}
	if int64(proj.Events) != before || proj.Phase != gatePhaseReady {
		t.Errorf("the production fold read %d events and phase %q over a parent holding %d; a row in a CHILD table reached it",
			proj.Events, proj.Phase, before)
	}
	after, head := gateHead(t, db, rolloutID)
	if after != before || head != "ready" {
		t.Errorf("the ledger's own reading moved from %d events to %d (head %q) because of a row in a CHILD table", before, after, head)
	}

	// 2. THE SHAPE. The next boot must refuse and name it.
	st2, err := Open(ctx, cfg, nil)
	if err == nil {
		_ = st2.Close()
		t.Fatal("a boot with a child table under the gate succeeded; a relation somebody prepared to write history into was stepped over")
	}
	if !strings.Contains(err.Error(), "INHERITS") {
		t.Errorf("the refusal does not name the inheritance: %v", err)
	}
	t.Logf("GUARD_INHERITED_CHILD|smuggled=%d|visible_without_ONLY=%d|parent=%d|production_fold=%d|boot_refused=true",
		smuggled, visible, before, proj.Events)
}

// ownerCanRestoreItsOwnDelete measures the capability the F-13 limit used to be stated against.
//
// It is a measurement rather than an assertion about this engine: the subject is PostgreSQL's
// implicit grant options, and the reason it lives in a test is that the campaign made a false
// claim about it and a comment saying "we were wrong" proves nothing on its own.
func ownerCanRestoreItsOwnDelete(t *testing.T, owner *sql.DB) {
	t.Helper()
	ctx := context.Background()
	gate := dialect.GuardGateEventsTable

	var before bool
	if err := owner.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, $1, 'DELETE')`, gate).Scan(&before); err != nil {
		t.Fatalf("read the DELETE privilege: %v", err)
	}
	if before {
		t.Fatalf("the append-only reconcile left DELETE on %s granted to the owning role, so this measurement has no subject", gate)
	}
	if _, err := owner.ExecContext(ctx, `GRANT DELETE ON `+gate+` TO CURRENT_USER`); err != nil {
		t.Fatalf("the owner could not restore its own DELETE, which would contradict PostgreSQL's implicit grant options: %v", err)
	}
	var after bool
	if err := owner.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, $1, 'DELETE')`, gate).Scan(&after); err != nil {
		t.Fatalf("re-read the DELETE privilege: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `REVOKE DELETE ON `+gate+` FROM CURRENT_USER`); err != nil {
		t.Fatalf("restore the append-only posture: %v", err)
	}
	if !after {
		t.Fatal("the GRANT succeeded and the privilege is still absent")
	}
	t.Logf("GUARD_OWNER_SELF_GRANT|had_delete_before=%t|after_self_grant=%t|superuser_needed=false", before, after)
}

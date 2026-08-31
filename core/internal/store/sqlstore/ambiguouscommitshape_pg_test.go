// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// C4-05'S CLASSIFIER, MEASURED — AND THIS TEST'S NAME WAS CORRECTED.
//
// It used to be called ...ALostCommitAcknowledgementIsNotAPgError, and that title was FALSE
// against its own output: the fixture terminates the backend from inside a deferred
// constraint trigger, which ABORTS the transaction, and the error arrives as a PgError with
// SQLSTATE 57P01. This package's own fixtures draw exactly that distinction — sleeping and
// cutting the socket can leave the commit durable; terminating the backend does not — and a
// test whose name claims the first while measuring the second is the kind of green that
// reads as coverage and is not.
//
// What it DOES measure, and what it is now named for, is the DECISION: an unanswered commit
// must be classified ambiguous so the runner reconciles instead of assuming a rollback. That
// is worth pinning: it fails if anyone removes 57P01 from commitOutcomeIsAmbiguous.
//
// WHAT REMAINS NO VERIFICADO is the original question — the shape pgx/database/sql returns
// when a commit was DURABLE and only its acknowledgement was lost. Neither this fixture nor
// any in this package can control which side of durability the cut falls on, and the wire
// test says so itself.
//
// The whole ambiguous-commit path turns on one claim, and until now it was a claim a
// COMMENT made about the driver rather than something anybody had observed:
//
//	migrationunit.go, phaseCommit — "an error that is NOT a *pgconn.PgError is THE
//	ambiguous case, and the only one"
//
// The independent measurement of the twelve limits listed this explicitly among the
// things it could not verify: "NO he verificado que un acuse perdido produzca de hecho un
// no-*pgconn.PgError en esta capa driver/pool: es comportamiento de pgx/database/sql que
// el codigo AFIRMA y yo no he medido."
//
// THIS TEST FIXES NOTHING. C4-05 stays open — deciding the semantics of the `reconciled`
// event is design, not a patch, and it is declared as out of scope.
func TestPostgresAnUnansweredCommitIsClassifiedAmbiguous(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS olv_ru_receipts(note text)`); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// The deferred constraint trigger terminates the backend from INSIDE the commit. It is the
	// only DETERMINISTIC unanswered commit this package can produce — and it is an ABORT, not
	// a durable commit whose acknowledgement was lost. That distinction is the whole reason
	// this test was renamed.
	armAmbiguousCommit(t, ctx, db)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(tctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(tctx, `INSERT INTO olv_ru_receipts VALUES ('ambiguous')`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert: %v", err)
	}
	commitErr := tx.Commit()
	if commitErr == nil {
		t.Fatal("the commit succeeded; the fixture did not produce an ambiguous outcome, so nothing " +
			"below is being measured")
	}

	var pg *pgconn.PgError
	isPgError := errors.As(commitErr, &pg)
	code := ""
	if isPgError {
		code = pg.Code
	}
	t.Logf("AMBIGUOUS_COMMIT|is_pgerror=%v|sqlstate=%q|err=%v", isPgError, code, commitErr)

	// THE ASSERTION, stated as the classifier states it. commitOutcomeIsAmbiguous is the
	// production predicate; asserting through it rather than through errors.As directly is
	// what makes this a test of the DECISION and not of a type.
	if !commitOutcomeIsAmbiguous(commitErr) {
		t.Fatalf("an UNANSWERED commit was classified as a SETTLED outcome (is_pgerror=%v, "+
			"sqlstate=%q). The whole reconciliation path is gated on this being ambiguous: classified "+
			"settled, the runner would treat a transaction whose fate it does not know as rolled back, "+
			"and the reconcile that exists to resolve exactly this case would never run. Error: %v",
			isPgError, code, commitErr)
	}
	// Reported, not asserted: what this fixture produces is 57P01, one of the three SQLSTATEs
	// the classifier already treats as ambiguous. Asserting "not a PgError" here would pin the
	// fixture's own shape and call it the driver's.
	if isPgError {
		t.Logf("AMBIGUOUS_COMMIT|NOTE: arrived AS a PgError with SQLSTATE %q — an aborted backend, "+
			"not a lost acknowledgement. The narrow claim (\"an error that is NOT a *pgconn.PgError "+
			"is THE ambiguous case, and the only one\") is too strong; the decision is still right", code)
	}
}

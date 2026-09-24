// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/store"
)

// The outcome of sqlStore.Mutate's single COMMIT.
//
// COMMIT is the only statement in the unit whose failure does not say whether it
// failed: the server may have applied it and lost the answer on the way home.
// Every other error Mutate can return — the callback's, the custodial poison's,
// either epilogue's — returns BEFORE Commit is called, so its meaning is already
// known and this file does not touch it.
//
// THE RULE IS POSITIVE PROOF OF THE NEGATIVE. A shape is called definite only
// when something PROVES the database did not apply COMMIT; everything else is
// reported unknown. A wrong answer is therefore always a false unknown, which
// costs a reconciliation, and never a false definite, which would tell a caller
// that a durable write did not happen.
//
// That asymmetry is the whole design. The alternative — an exclusion list, where
// anything not named is assumed to have rolled back — fails in exactly the wrong
// direction: every SQLSTATE nobody has thought about yet, and every future one,
// becomes a confident claim about durability that nothing supports.

// commitOutcomeUniqueViolation is the one SQLSTATE admitted as a proved abort at
// this boundary. It is spelled here rather than shared with the migration
// runner's constants because the two boundaries admit different sets and folding
// them together would make widening one silently widen the other.
const commitOutcomeUniqueViolation = "23505"

// mutateCommitNotApplied reports whether the error of Mutate's COMMIT PROVES the
// database did not apply it.
//
// It is deliberately NOT commitOutcomeIsAmbiguous (migrationunit.go:787-816),
// and the difference is not an oversight. That predicate serves a runner whose
// remedy is reconcile-and-retry, so it answers "must I go and ask?" and treats
// every server error at COMMIT as settled. This one serves a boundary with NO
// retry and no reconciliation: it hands the outcome to its caller, so it may
// only answer "definite" where the answer is proved, and it treats an unproved
// server error as unknown. TestCommitOutcomeClassification carries one row per
// documented shape and asserts both predicates side by side, so the divergence
// is measured rather than described.
//
// D2..D5 are the closed admitted set. Everything else is unknown, including
// 57014, 40001, 40P01, 55P03, 25P02, every other class-08 code, FATAL and PANIC
// severities, bare context errors, transport failures, a partially written
// COMMIT and every SQLite driver error other than D1/D2.
func mutateCommitNotApplied(err error) bool {
	if err == nil {
		return false
	}
	// D2 — database/sql rolled the transaction back before it reached the driver,
	// so COMMIT was never issued. This holds because Mutate calls Commit exactly
	// once; a second call would answer ErrTxDone for the opposite reason.
	if errors.Is(err, sql.ErrTxDone) {
		return true
	}
	// D3 — the pgx contract: this shape is "guaranteed to have occurred before
	// sending any data to the server".
	if pgconn.SafeToRetry(err) {
		return true
	}
	// D4 — the server answered COMMIT with the ROLLBACK command tag, which is
	// what it sends for COMMIT inside a failed transaction block.
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	// D5 — the one admitted server rejection. A deferred unique violation is
	// raised by the pre-commit deferred-trigger loop, which runs before the
	// durable commit record is written; an error after that record cannot surface
	// as an ERROR response at all. The unlocalized severity is part of the
	// admission, not decoration: it is the field that is not translated, and a
	// reply carrying this code without it is not the shape that was proved.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.SeverityUnlocalized == "ERROR" && pgErr.Code == commitOutcomeUniqueViolation
	}
	return false
}

// mutateCommitOutcomeErr classifies the error of the COMMIT at the end of
// sqlStore.Mutate.
//
// It is the ONE owner of that classification, and it is additive by
// construction: both arms keep wrapUnavailableErr, so every errors.Is a caller
// already performs — store.ErrStoreUnavailable, the driver's own sentinels, the
// context errors — answers exactly as it did before. An unknown outcome gains a
// sentinel in front of that chain and loses nothing from it.
func mutateCommitOutcomeErr(err error) error {
	if err == nil || mutateCommitNotApplied(err) {
		return wrapUnavailableErr(err)
	}
	return fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, wrapUnavailableErr(err))
}

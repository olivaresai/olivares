// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ---- C32 controls for the commit outcome of sqlStore.Mutate ----------------
//
// The subject is one statement: the single tx.Commit() at the end of Mutate.
// The question these controls answer is not "did the write succeed" but "does
// the caller learn the truth when the answer was never read".
//
// Every control here is classified, because an unclassified green is how a
// missing symbol gets mistaken for a passing assertion:
//
//	B-RED   red at C0 for a BEHAVIORAL reason, with every symbol it names
//	        already declared at C0.
//	P-GREEN green at C0 and at every later cut.
//	M-RED   green at the final cut, red under one named mutant.
//
// Nothing in this file was executed by its author. The classes are expectations
// for the hosted run, not measurements.

// commitOutcomeRetryError implements pgconn's SafeToRetry contract. safe=true is
// the D3 shape (nothing was sent); safe=false is a write error that already put
// bytes on the wire, which is unknown.
type commitOutcomeRetryError struct {
	msg  string
	safe bool
}

func (e commitOutcomeRetryError) Error() string { return e.msg }

func (e commitOutcomeRetryError) SafeToRetry() bool { return e.safe }

// commitOutcomeNetError is a transport failure: it satisfies net.Error, which is
// what isBackendUnavailable matches, so its row also pins that the availability
// wrapping survives the new classification.
type commitOutcomeNetError struct{ msg string }

func (e commitOutcomeNetError) Error() string { return e.msg }

func (e commitOutcomeNetError) Timeout() bool { return true }

func (e commitOutcomeNetError) Temporary() bool { return true }

func commitOutcomePgError(severity, code string) *pgconn.PgError {
	return &pgconn.PgError{
		Severity:            severity,
		SeverityUnlocalized: severity,
		Code:                code,
		Message:             "commit outcome fixture " + code,
	}
}

// commitOutcomeUnlocalizedlessPgError is the admitted code WITHOUT the proved
// severity. It exists so the closed set is measured as closed: the admission is
// "SeverityUnlocalized ERROR and 23505", not "23505".
func commitOutcomeUnlocalizedlessPgError(code string) *pgconn.PgError {
	return &pgconn.PgError{Code: code, Message: "commit outcome fixture " + code + " without unlocalized severity"}
}

func commitOutcomeDecisionName(d retryDecision) string {
	switch d {
	case retryNewTransaction:
		return "retryNewTransaction"
	case retryAfterReconcile:
		return "retryAfterReconcile"
	case retryNever:
		return "retryNever"
	case retryPropagate:
		return "retryPropagate"
	case retryNewSession:
		return "retryNewSession"
	default:
		return fmt.Sprintf("retryDecision(%d)", int(d))
	}
}

// TestCommitOutcomeClassification is HS-1: one row per documented COMMIT error
// shape, asserting ALL THREE applicable columns side by side.
//
// The three columns exist because the repository already has a commit-boundary
// classifier — the migration runner's — and this contract's justification for
// NOT sharing a helper with it is that the two disagree. An assertion that
// measured only this boundary would leave that justification as prose: the
// published comparison could go stale in silence, and the first thing to notice
// would be a caller. So each row states, and this test checks:
//
//	commitOutcomeIsAmbiguous(e)                      the migration PREDICATE
//	classifyFailure(ctx, {Phase: phaseCommit, Err})   the migration DECISION
//	mutateCommitOutcomeErr(e)                         the Mutate CLASS
//
// The divergence is therefore measured rather than remembered. Both migration
// functions are package-internal, so this needs no new seam, and no migration
// behavior changes.
//
// Class: B-RED for the unknown rows, M-RED for M1, M2, M3, M9 and M10. The
// definite rows and the cause/availability parity are P-GREEN.
func TestCommitOutcomeClassification(t *testing.T) {
	t.Parallel()

	// ambiguous is the migration predicate's answer, decision the migration
	// runner's recovery contract at phaseCommit, and unknown is THIS contract's
	// answer: does Mutate report the outcome as undetermined?
	cases := []struct {
		name      string
		err       error
		ambiguous bool
		decision  retryDecision
		unknown   bool
		why       string
	}{
		{
			name:      "D2_tx_done",
			err:       sql.ErrTxDone,
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   false,
			why:       "database/sql rolled the transaction back before it ever called the driver",
		},
		{
			name:      "D3_safe_to_retry",
			err:       commitOutcomeRetryError{msg: "conn lock", safe: true},
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   false,
			why:       "pgx guarantees this shape occurred BEFORE any data was sent",
		},
		{
			name:      "D4_commit_rollback",
			err:       pgx.ErrTxCommitRollback,
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   false,
			why:       "the server answered COMMIT with the ROLLBACK command tag",
		},
		{
			name:      "D5_unique_violation",
			err:       commitOutcomePgError("ERROR", "23505"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   false,
			why:       "a deferred unique recheck is raised in the pre-commit trigger loop, before the durable commit record",
		},
		{
			name:      "U_bare_canceled",
			err:       context.Canceled,
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "a cancellation delivered while reading the reply does not say which side of the boundary it landed on",
		},
		{
			name:      "U_bare_deadline",
			err:       context.DeadlineExceeded,
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "the deadline expired waiting for an answer the server may already have made durable",
		},
		{
			name:      "U_transport_timeout",
			err:       commitOutcomeNetError{msg: "read tcp: i/o timeout"},
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "the acknowledgement was lost in transport, not refused by the server",
		},
		{
			name:      "U_write_error_sent",
			err:       commitOutcomeRetryError{msg: "short write", safe: false},
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "bytes of COMMIT reached the wire, so the write is not safe to assume away",
		},
		{
			name:      "U_sqlite_driver",
			err:       errors.New("SQLITE_BUSY: database is locked"),
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "a SQLite COMMIT error other than D1/D2 leaves the transaction's fate to the driver's own rollback",
		},
		{
			name:      "U_resolution_unknown",
			err:       commitOutcomePgError("ERROR", "08007"),
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "08007 says, in as many words, that the transaction's resolution is unknown",
		},
		{
			name:      "U_completion_unknown",
			err:       commitOutcomePgError("ERROR", "40003"),
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "40003 is a statement whose completion is unknown; at the boundary that is the unit's",
		},
		{
			name:      "U_admin_shutdown",
			err:       commitOutcomePgError("FATAL", "57P01"),
			ambiguous: true,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "a shutdown delivered during COMMIT may have arrived after the server made it durable",
		},
		{
			name:      "U_query_canceled",
			err:       commitOutcomePgError("ERROR", "57014"),
			ambiguous: false,
			decision:  retryAfterReconcile,
			unknown:   true,
			why:       "the cancel-processing path was never read, so 57014 is an UNPROVED code and stays unknown",
		},
		{
			name:      "U_serialization_failure",
			err:       commitOutcomePgError("ERROR", "40001"),
			ambiguous: false,
			decision:  retryNewTransaction,
			unknown:   true,
			why:       "migration retries the whole unit here; Mutate has no proof and no retry, so it reports unknown",
		},
		{
			name:      "U_deadlock_detected",
			err:       commitOutcomePgError("ERROR", "40P01"),
			ambiguous: false,
			decision:  retryNewTransaction,
			unknown:   true,
			why:       "same divergence as 40001: a settled verdict for the runner is an unproved one here",
		},
		{
			name:      "U_lock_not_available",
			err:       commitOutcomePgError("ERROR", "55P03"),
			ambiguous: false,
			decision:  retryNewTransaction,
			unknown:   true,
			why:       "the lock-timeout path inside a deferred trigger was never read",
		},
		{
			name:      "U_cannot_connect_now",
			err:       commitOutcomePgError("ERROR", "57P03"),
			ambiguous: false,
			decision:  retryNewSession,
			unknown:   true,
			why:       "the runner wants a new session; neither answer proves this COMMIT was not applied",
		},
		{
			name:      "U_in_failed_transaction",
			err:       commitOutcomePgError("ERROR", "25P02"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   true,
			why:       "migration calls it a programming error; it is still not proof about THIS commit",
		},
		{
			name:      "U_connection_exception",
			err:       commitOutcomePgError("ERROR", "08006"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   true,
			why:       "class 08 other than 08007 is a lost connection, which is the definition of no answer",
		},
		{
			name:      "U_crash_shutdown",
			err:       commitOutcomePgError("FATAL", "57P02"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   true,
			why:       "migration never checks severity; an unproved FATAL is unknown here",
		},
		{
			name:      "U_other_error_code",
			err:       commitOutcomePgError("ERROR", "42501"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   true,
			why:       "ERROR severity alone is not an admission: the closed set holds exactly one code",
		},
		{
			name:      "U_unique_violation_empty_severity",
			err:       commitOutcomeUnlocalizedlessPgError("23505"),
			ambiguous: false,
			decision:  retryNever,
			unknown:   true,
			why:       "the admission requires SeverityUnlocalized ERROR; an empty severity is not the proved shape",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := commitOutcomeIsAmbiguous(tc.err); got != tc.ambiguous {
				t.Errorf("commitOutcomeIsAmbiguous = %t, want %t — %s", got, tc.ambiguous, tc.why)
			}
			// The context is deliberately NOT canceled: classifyFailure consults
			// ctx.Err() before anything else, and that precheck has its own row.
			decision, _ := classifyFailure(context.Background(), unitFailure{
				Phase: phaseCommit, Err: tc.err,
			})
			if decision != tc.decision {
				t.Errorf("classifyFailure at phaseCommit = %s, want %s — %s",
					commitOutcomeDecisionName(decision),
					commitOutcomeDecisionName(tc.decision), tc.why)
			}
			got := mutateCommitOutcomeErr(tc.err)
			if isUnknown := errors.Is(got, store.ErrCommitOutcomeUnknown); isUnknown != tc.unknown {
				t.Errorf("Mutate reports unknown = %t, want %t — %s", isUnknown, tc.unknown, tc.why)
			}
			// The cause survives in every case: a classification that discarded it
			// would leave the caller a sentinel and no diagnosis.
			if !errors.Is(got, tc.err) {
				t.Errorf("the original cause was lost: %v", got)
			}
			// And so does the availability wrapping every existing consumer
			// already matches on.
			wantUnavailable := errors.Is(wrapUnavailableErr(tc.err), store.ErrStoreUnavailable)
			if gotUnavailable := errors.Is(got, store.ErrStoreUnavailable); gotUnavailable != wantUnavailable {
				t.Errorf("ErrStoreUnavailable match = %t, want %t: existing classifiers must answer exactly as before",
					gotUnavailable, wantUnavailable)
			}
		})
	}

	// D0 — a committed Mutate is unchanged, and nil must never acquire a sentinel.
	t.Run("D0_nil", func(t *testing.T) {
		t.Parallel()
		if err := mutateCommitOutcomeErr(nil); err != nil {
			t.Fatalf("a successful commit returned %v", err)
		}
	})

	// The last row of the published comparison, kept as its own named PRECHECK
	// assertion because it is not a classifier call at all: classifyFailure
	// consults the caller's context BEFORE it looks at the error, and Mutate's
	// equivalent (P0) runs before COMMIT is issued rather than after it. Stating
	// that applicability is the point — folding this row into the table above
	// would claim a comparison the two boundaries do not make.
	t.Run("precheck_caller_already_canceled", func(t *testing.T) {
		t.Parallel()
		canceled, cancel := context.WithCancel(context.Background())
		cancel()

		// An ambiguous commit error: the runner reconciles rather than
		// propagating, because a canceled caller does not settle an in-flight
		// COMMIT.
		if got, _ := classifyFailure(canceled, unitFailure{
			Phase: phaseCommit, Err: errors.New("no answer"),
		}); got != retryAfterReconcile {
			t.Errorf("canceled caller with an ambiguous commit error = %s, want retryAfterReconcile",
				commitOutcomeDecisionName(got))
		}
		// A settled server error: the cancellation does not erase the answer.
		if got, _ := classifyFailure(canceled, unitFailure{
			Phase: phaseCommit, Err: commitOutcomePgError("ERROR", "23505"),
		}); got != retryPropagate {
			t.Errorf("canceled caller with a settled commit error = %s, want retryPropagate",
				commitOutcomeDecisionName(got))
		}
		// Mutate does not consult the context after COMMIT at all: P0 is the only
		// context read at this boundary and it runs BEFORE COMMIT is issued, which
		// is what TestCommitOutcomeDefiniteRollback* measures on a real engine.
	})
}

// ---- engine-backed controls ------------------------------------------------

type commitOutcomeFixture struct {
	st     store.Store
	inner  *sqlStore
	tenant model.TenantID
	appDSN string
}

// newCommitOutcomeFixture opens a store on one engine and provisions a tenant.
//
// The PostgreSQL leg uses this package's existing isolation gate, so a run with
// no server configured behaves exactly as every other Postgres leg here does,
// and OLIVARES_TEST_POSTGRES_REQUIRED turns that into a failure. A missing
// PRIVILEGE, or a wait that never happens, INSIDE a leg that did start is a
// different thing entirely and is always a named COULD_NOT_LOOK failure — never
// a skip, because a skip would read as "nothing was wrong".
func newCommitOutcomeFixture(t *testing.T, engine store.Engine) *commitOutcomeFixture {
	t.Helper()
	f := &commitOutcomeFixture{}
	switch engine {
	case store.EngineSQLite:
		f.st = openSQLiteTest(t, nil)
	case store.EnginePostgres:
		dsns := isolatedPG(t)
		st, err := Open(context.Background(), store.Config{
			Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4,
		}, nil)
		if err != nil {
			t.Fatalf("open postgres store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		f.st, f.appDSN = st, dsns.App
	default:
		t.Fatalf("unsupported engine %q", engine)
	}
	inner, ok := f.st.(*sqlStore)
	if !ok {
		t.Fatalf("the opened store is %T, not *sqlStore: these controls drive the package's own commit boundary", f.st)
	}
	f.inner = inner
	f.tenant = provisionTenant(t, f.st, "commitoutcome-"+uniqueSuffix())
	return f
}

// mutateCreatingAgent runs one Mutate that creates a product row and returns the
// row's id together with the error Mutate answered. inside runs in the callback
// after the row is created, so a control can reach the transaction directly.
func (f *commitOutcomeFixture) mutateCreatingAgent(
	ctx context.Context, name string, inside func(ts *tenantScope) error,
) (model.ID, error) {
	var id model.ID
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		agent, err := sc.Agents().Create(ctx, model.Agent{
			Name: name, Kind: "claude-code", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		id = agent.ID
		if inside == nil {
			return nil
		}
		ts, ok := sc.(*tenantScope)
		if !ok {
			return fmt.Errorf("scope is %T, not *tenantScope", sc)
		}
		return inside(ts)
	})
	return id, err
}

// agentPresent answers the only question that matters after an uncertain
// commit: is the row there? It reads on a FRESH context, because the control's
// own context is usually the one that was canceled.
func (f *commitOutcomeFixture) agentPresent(t *testing.T, id model.ID) bool {
	t.Helper()
	if id.IsZero() {
		return false
	}
	var present bool
	err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Get(context.Background(), id)
		switch {
		case err == nil:
			present = true
			return nil
		case errors.Is(err, store.ErrNotFound):
			return nil
		default:
			return err
		}
	})
	if err != nil {
		t.Fatalf("read the product row back: %v", err)
	}
	return present
}

func TestCommitOutcomeDefiniteRollbackSQLite(t *testing.T) {
	commitOutcomeDefiniteRollback(t, store.EngineSQLite)
}

func TestCommitOutcomeDefiniteRollbackPostgres(t *testing.T) {
	commitOutcomeDefiniteRollback(t, store.EnginePostgres)
}

// commitOutcomeDefiniteRollback holds HS-P0 and HS-E: the outcomes at this
// boundary that ARE known, and that must therefore never acquire the sentinel.
//
// HS-P0 (precommit_cancel) is deliberately UNGRADED at C0. With P0 absent the
// answer depends on which arm of database/sql's Tx.Commit wins a race, and
// calling either outcome "the baseline" would claim a deterministic kill this
// control cannot deliver. What it CAN do is kill the mutant: with P0 deleted the
// select must take the ready Done arm, so the answer is either sql.ErrTxDone or
// a bare context.Canceled, and the three-part assertion below rejects the first
// and the sentinel while requiring the second's cause.
//
// The cancellation is delivered from the existing lineage epilogue hook — the
// last point before COMMIT — against a context.WithCancel parent, because a
// *cancelCtx cancels its registered children synchronously, so tx.ctx is closed
// before Commit is entered. It is never a sleep racing a deadline.
//
// Classes: HS-P0 M-RED (M4), ungraded at C0. HS-E P-GREEN and M-RED (M3).
func commitOutcomeDefiniteRollback(t *testing.T, engine store.Engine) {
	t.Run("precommit_cancel", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, engine)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.inner.custodyEpilogueTestHook = func(name string) error {
			if name == epilogueLineage {
				cancel()
			}
			return nil
		}
		id, err := f.mutateCreatingAgent(ctx, "canceled-before-commit-"+uniqueSuffix(), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancellation that landed before COMMIT = %v, want the context.Canceled cause", err)
		}
		if errors.Is(err, sql.ErrTxDone) {
			t.Error("the refusal names sql.ErrTxDone, so COMMIT was reached and database/sql answered — that is not what P0 does")
		}
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			t.Error("a COMMIT that was never issued was reported as an unknown outcome: P0 exists precisely because this outcome IS known")
		}
		if f.agentPresent(t, id) {
			t.Fatal("the product row committed despite the pre-commit cancellation")
		}
	})

	t.Run("precommit_cancel_positive", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, engine)
		f.inner.custodyEpilogueTestHook = func(string) error { return nil }
		id, err := f.mutateCreatingAgent(context.Background(), "not-canceled-"+uniqueSuffix(), nil)
		if err != nil {
			t.Fatalf("the same callback without the cancellation = %v, want nil", err)
		}
		if !f.agentPresent(t, id) {
			t.Fatal("a committed Mutate left no row")
		}
	})

	for _, epilogue := range []string{epilogueDirectory, epilogueLineage} {
		t.Run("epilogue_"+epilogue, func(t *testing.T) {
			f := newCommitOutcomeFixture(t, engine)
			fault := errors.New(epilogue + " epilogue refused")
			f.inner.custodyEpilogueTestHook = func(name string) error {
				if name == epilogue {
					return fault
				}
				return nil
			}
			id, err := f.mutateCreatingAgent(context.Background(),
				"epilogue-"+epilogue+"-"+uniqueSuffix(), nil)
			if !errors.Is(err, fault) {
				t.Fatalf("a failing %s epilogue = %v, want the epilogue's own cause", epilogue, err)
			}
			if errors.Is(err, store.ErrCommitOutcomeUnknown) {
				t.Error("an epilogue refusal returns BEFORE Commit, so its outcome is known: marking it unknown reports a rollback as undetermined")
			}
			if f.agentPresent(t, id) {
				t.Fatalf("the product row committed despite a failed %s epilogue", epilogue)
			}
		})
	}

	t.Run("callback_error", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, engine)
		fault := errors.New("callback refused")
		var id model.ID
		err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			agent, err := sc.Agents().Create(context.Background(), model.Agent{
				Name: "callback-error-" + uniqueSuffix(), Kind: "claude-code", Status: model.StatusActive,
			})
			if err != nil {
				return err
			}
			id = agent.ID
			return fault
		})
		if !errors.Is(err, fault) {
			t.Fatalf("a refusing callback = %v, want its own cause", err)
		}
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			t.Error("a callback refusal never reaches COMMIT; reporting it as unknown would hide a definite rollback")
		}
		if f.agentPresent(t, id) {
			t.Fatal("the product row committed despite a refusing callback")
		}
	})
}

// TestCommitOutcomeServerRejectionPostgres holds the two shapes where the SERVER
// answered COMMIT, so the answer is knowledge: HS-2 (the ROLLBACK command tag)
// and HS-3 (a deferred unique violation). Both are preservation — green at C0
// and at every later cut — and both are what mutant M3, "mark every Mutate error
// unknown", is caught by. HS-3 additionally keeps the 23505 admission honest:
// M10 drops the admission and turns it red.
//
// SCHEDULE CORRECTION, read from this base rather than assumed. The contract
// describes HS-2's failing statement as running in the CALLBACK. At this base it
// cannot: on PostgreSQL an aborted transaction block answers 25P02 to every
// later statement, and the lineage epilogue issues its own SQL
// (lineage_writer.go:136) between the callback and COMMIT. A callback that
// poisoned the block would return at that epilogue and never reach COMMIT, which
// is not the shape HS-2 exists to measure. The statement is therefore issued
// from the lineage epilogue hook, which runs AFTER that epilogue's own SQL and
// is the last point before COMMIT. The assertion, the class and the mutant are
// unchanged.
func TestCommitOutcomeServerRejectionPostgres(t *testing.T) {
	t.Run("rollback_tag", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, store.EnginePostgres)
		var tx *sql.Tx
		f.inner.custodyEpilogueTestHook = func(name string) error {
			if name == epilogueLineage && tx != nil {
				// Poison the block and DISCARD the error: the transaction has
				// failed, and COMMIT is still sent.
				_, _ = tx.ExecContext(context.Background(), "SELECT 1/0")
			}
			return nil
		}
		id, err := f.mutateCreatingAgent(context.Background(), "rollback-tag-"+uniqueSuffix(),
			func(ts *tenantScope) error {
				tx = ts.tx
				return nil
			})
		if !errors.Is(err, pgx.ErrTxCommitRollback) {
			t.Fatalf("COMMIT on a failed transaction block = %v, want pgx.ErrTxCommitRollback", err)
		}
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			t.Error("the server answered COMMIT with the ROLLBACK tag, which is a definite answer: calling it unknown discards knowledge the protocol delivered")
		}
		if f.agentPresent(t, id) {
			t.Fatal("a transaction the server rolled back left a durable row")
		}
	})

	t.Run("rollback_tag_positive", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, store.EnginePostgres)
		f.inner.custodyEpilogueTestHook = func(string) error { return nil }
		id, err := f.mutateCreatingAgent(context.Background(), "rollback-tag-ok-"+uniqueSuffix(), nil)
		if err != nil {
			t.Fatalf("the same schedule with no failing statement = %v, want nil", err)
		}
		if !f.agentPresent(t, id) {
			t.Fatal("a committed Mutate left no row")
		}
	})

	t.Run("deferred_unique", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, store.EnginePostgres)
		table := "olv_c32_deferred_" + uniqueSuffix()
		var setupErr error
		id, err := f.mutateCreatingAgent(context.Background(), "deferred-unique-"+uniqueSuffix(),
			func(ts *tenantScope) error {
				ctx := context.Background()
				if _, err := ts.tx.ExecContext(ctx, fmt.Sprintf(
					`CREATE TEMP TABLE %s (id int, CONSTRAINT %s_uniq UNIQUE (id) DEFERRABLE INITIALLY DEFERRED)`,
					table, table)); err != nil {
					setupErr = fmt.Errorf("create the deferred-unique TEMP table: %w", err)
					return setupErr
				}
				for i := 0; i < 2; i++ {
					if _, err := ts.tx.ExecContext(ctx,
						fmt.Sprintf(`INSERT INTO %s(id) VALUES (1)`, table)); err != nil {
						setupErr = fmt.Errorf("insert the duplicate row: %w", err)
						return setupErr
					}
				}
				return nil
			})
		if setupErr != nil {
			t.Fatalf("COULD_NOT_LOOK: the deferred-unique fixture could not be built, so the admission was never exercised (TEMP privilege?): %v", setupErr)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("a deferred unique violation at COMMIT = %v, want SQLSTATE 23505", err)
		}
		if pgErr.SeverityUnlocalized != "ERROR" {
			t.Fatalf("COULD_NOT_LOOK: the violation arrived with unlocalized severity %q rather than ERROR, so the admission's proved shape was never produced",
				pgErr.SeverityUnlocalized)
		}
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			t.Error("a deferred unique violation is raised in the pre-commit trigger loop, before the durable commit record: it is a proved abort, not an unknown outcome")
		}
		if f.agentPresent(t, id) {
			t.Fatal("a transaction the server refused at COMMIT left a durable row")
		}
	})

	t.Run("deferred_unique_positive", func(t *testing.T) {
		f := newCommitOutcomeFixture(t, store.EnginePostgres)
		table := "olv_c32_deferred_ok_" + uniqueSuffix()
		id, err := f.mutateCreatingAgent(context.Background(), "deferred-unique-ok-"+uniqueSuffix(),
			func(ts *tenantScope) error {
				ctx := context.Background()
				if _, err := ts.tx.ExecContext(ctx, fmt.Sprintf(
					`CREATE TEMP TABLE %s (id int, CONSTRAINT %s_uniq UNIQUE (id) DEFERRABLE INITIALLY DEFERRED)`,
					table, table)); err != nil {
					return err
				}
				_, err := ts.tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(id) VALUES (1)`, table))
				return err
			})
		if err != nil {
			t.Fatalf("a single insert under the same deferred key = %v, want nil", err)
		}
		if !f.agentPresent(t, id) {
			t.Fatal("a committed Mutate left no row")
		}
	})
}

// TestCommitOutcomeExternalCancelPostgres is HS-4: the supported UNKNOWN branch,
// produced by a real server instead of a synthetic error value.
//
// An out-of-band session holds a conflicting UNCOMMITTED row under a deferred
// unique key, so the Mutate COMMIT blocks inside the pre-commit recheck waiting
// on that transaction's id. A pg_cancel_backend from outside then answers 57014
// — a code this contract has NOT admitted, because the path that raises it was
// never read. Unknown is therefore the correct answer, and it is the honest one:
// what the client can prove is only that it never read one.
//
// Class: B-RED at C0 (today the caller is told 500 with no distinction) and
// M-RED for M1 and M9. The positive is the same run with no conflicting row.
func TestCommitOutcomeExternalCancelPostgres(t *testing.T) {
	f := newCommitOutcomeFixture(t, store.EnginePostgres)
	ctx := context.Background()

	owner, err := sql.Open("pgx", f.appDSN)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: open the out-of-band session: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	owner.SetMaxOpenConns(4)

	table := "olv_c32_external_" + uniqueSuffix()
	if _, err := owner.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (id int, CONSTRAINT %s_uniq UNIQUE (id) DEFERRABLE INITIALLY DEFERRED)`,
		table, table)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: the deferred-unique fixture table could not be created: %v", err)
	}
	t.Cleanup(func() {
		_, _ = owner.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table)
	})

	// The conflicting row stays UNCOMMITTED. That is what makes the recheck wait
	// rather than fail at once, and the wait is the whole schedule.
	holder, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: begin the out-of-band transaction: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := holder.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s(id) VALUES (1)`, table)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: hold the conflicting row: %v", err)
	}

	type commitOutcomeAttempt struct {
		id  model.ID
		err error
	}
	done := make(chan commitOutcomeAttempt, 1)
	pids := make(chan int, 1)
	go func() {
		id, err := f.mutateCreatingAgent(ctx, "external-cancel-"+uniqueSuffix(),
			func(ts *tenantScope) error {
				var pid int
				if err := ts.tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return fmt.Errorf("read the backend pid: %w", err)
				}
				if _, err := ts.tx.ExecContext(ctx,
					fmt.Sprintf(`INSERT INTO %s(id) VALUES (1)`, table)); err != nil {
					return fmt.Errorf("insert the conflicting row: %w", err)
				}
				pids <- pid
				return nil
			})
		done <- commitOutcomeAttempt{id: id, err: err}
	}()

	var pid int
	select {
	case pid = <-pids:
	case attempt := <-done:
		t.Fatalf("COULD_NOT_LOOK: the callback never reached its insert: %v", attempt.err)
	case <-time.After(10 * time.Second):
		t.Fatal("COULD_NOT_LOOK: the callback did not report its backend pid within 10s")
	}

	// The wait is OBSERVED, never assumed: COMMIT is blocked on the holder's
	// transaction id inside the deferred recheck. A deadline here is a hold limit,
	// not the oracle.
	if !commitOutcomeWaitForTransactionIDWait(t, owner, pid) {
		t.Fatal("COULD_NOT_LOOK: the COMMIT never waited on the holder's transaction id, so the deferred recheck was not exercised")
	}
	if _, err := owner.ExecContext(ctx, "SELECT pg_cancel_backend($1)", pid); err != nil {
		t.Fatalf("COULD_NOT_LOOK: pg_cancel_backend was refused: %v", err)
	}
	_ = holder.Rollback()

	var attempt commitOutcomeAttempt
	select {
	case attempt = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("COULD_NOT_LOOK: the canceled COMMIT did not return within 10s")
	}
	commitOutcomeWaitForSettlement(t, owner, pid)

	var pgErr *pgconn.PgError
	if !errors.As(attempt.err, &pgErr) || pgErr.Code != "57014" {
		t.Fatalf("COULD_NOT_LOOK: the external cancellation produced %v rather than SQLSTATE 57014", attempt.err)
	}
	if !errors.Is(attempt.err, store.ErrCommitOutcomeUnknown) {
		t.Fatalf("a COMMIT canceled from outside was reported as %v: 57014 is an UNPROVED code at this boundary, so the caller must be told the outcome is undetermined rather than handed a bare failure",
			attempt.err)
	}
	if f.agentPresent(t, attempt.id) {
		t.Fatal("the canceled COMMIT left a durable row after settlement: the row is the effect the caller was never told about")
	}
}

// TestCommitOutcomePositiveExternalCancelPostgres is HS-4's positive: the same
// run with nothing to wait for commits and is reported as the success it is.
func TestCommitOutcomePositiveExternalCancelPostgres(t *testing.T) {
	f := newCommitOutcomeFixture(t, store.EnginePostgres)
	id, err := f.mutateCreatingAgent(context.Background(),
		"external-cancel-positive-"+uniqueSuffix(), nil)
	if err != nil {
		t.Fatalf("the same run with no conflicting row = %v, want nil", err)
	}
	if !f.agentPresent(t, id) {
		t.Fatal("a committed Mutate left no row")
	}
}

// commitOutcomeWaitForTransactionIDWait polls pg_locks for the COMMIT's wait on
// another transaction's id.
func commitOutcomeWaitForTransactionIDWait(t *testing.T, owner *sql.DB, pid int) bool {
	t.Helper()
	return commitOutcomeObservePoll(t, commitOutcomeObserveWindow,
		func(callCtx context.Context) (bool, error) {
			var waiting int
			if err := owner.QueryRowContext(callCtx,
				`SELECT count(*) FROM pg_locks WHERE pid = $1 AND locktype = 'transactionid' AND NOT granted`,
				pid).Scan(&waiting); err != nil {
				return false, err
			}
			return waiting > 0, nil
		})
}

const (
	// commitOutcomeObserveWindow is how long an observation may be RETRIED.
	commitOutcomeObserveWindow = 10 * time.Second
	// commitOutcomeObserveCall bounds ONE call inside that window. The two are
	// different bounds and conflating them is the defect this replaces: a loop
	// that only consults the wall clock between iterations never evaluates its own
	// deadline while a call is stuck, so the window is not a bound at all and the
	// goroutine holding the call cannot be joined.
	commitOutcomeObserveCall = 5 * time.Second
)

// commitOutcomeObservePoll retries a bounded observation until it answers true or
// the window closes. Each call carries its own deadline and is cancelled when it
// returns, so a stalled server leaves no in-flight query behind.
func commitOutcomeObservePoll(
	t *testing.T, window time.Duration, step func(context.Context) (bool, error),
) bool {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		// THE CHILD DEADLINE IS DERIVED FROM THE PARENT. A fresh full-length
		// per-call timeout can push the last call past the window this function
		// promises, so the caller's stated outer bound would be exceeded by one
		// call. Taking whichever is nearer keeps the promise exact.
		done, err := commitOutcomeObserveBounded(deadline, step)
		if err == nil && done {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// commitOutcomeObserveOnce runs exactly one observation under its own deadline.
func commitOutcomeObserveOnce(step func(context.Context) (bool, error)) (bool, error) {
	return commitOutcomeObserveBounded(time.Now().Add(commitOutcomeObserveCall), step)
}

// commitOutcomeObserveBounded runs one observation bounded by the NEARER of the
// per-call ceiling and the outer deadline it was given.
func commitOutcomeObserveBounded(
	outer time.Time, step func(context.Context) (bool, error),
) (bool, error) {
	limit := time.Now().Add(commitOutcomeObserveCall)
	if outer.Before(limit) {
		limit = outer
	}
	callCtx, cancel := context.WithDeadline(context.Background(), limit)
	defer cancel()
	return step(callCtx)
}

// commitOutcomeWaitForSettlement is the barrier: the backend that ran the
// transaction has ended it, so a read taken afterwards is conclusive. A read
// taken BEFORE this point proves nothing about absence, which is the reason the
// barrier exists rather than a sleep.
func commitOutcomeWaitForSettlement(t *testing.T, owner *sql.DB, pid int) {
	t.Helper()
	settled := commitOutcomeObservePoll(t, commitOutcomeObserveWindow,
		func(callCtx context.Context) (bool, error) {
			var open int
			if err := owner.QueryRowContext(callCtx,
				`SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND xact_start IS NOT NULL`,
				pid).Scan(&open); err != nil {
				return false, err
			}
			return open == 0, nil
		})
	if !settled {
		t.Fatalf("COULD_NOT_LOOK: settlement not observed within %s, so no read after it can be conclusive",
			commitOutcomeObserveWindow)
	}
}

// TestCommitOutcomeStalledObservationIsBoundedPostgres is the observer half of
// S3, and it measures the instrument rather than the product.
//
// An observation that blocks must end at its OWN deadline AND leave nothing in
// flight. Both halves are asserted, and the second one is why this control is
// shaped the way it is:
//
//   - the pool is limited to ONE connection. The earlier form allowed two, so
//     the "restored" query could be answered on a second connection while the
//     first observation was still live — which proves nothing about release.
//     With one connection, an ordinary query can only succeed if the stalled
//     call really gave its connection back.
//   - the identified backend is observed to have SETTLED. Cancelling a Go call
//     is not the same event as the server ending the statement, and only the
//     second one makes the connection reusable.
//
// The identification is causal: the stalled call announces its own backend pid
// before it sleeps, so the settlement observation names that exact backend rather
// than "some idle connection".
//
// Causal mutant: remove the per-call deadline so the observation uses the
// background context. The bounded assertion must then fail on elapsed time.
// Class: P-GREEN on corrected source. UNMEASURED until hosted.
func TestCommitOutcomeStalledObservationIsBoundedPostgres(t *testing.T) {
	f := newCommitOutcomeFixture(t, store.EnginePostgres)
	observer, err := sql.Open("pgx", f.appDSN)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: open the observation pool: %v", err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	// ONE connection. This is the whole reuse proof.
	observer.SetMaxOpenConns(1)

	// A separate DIRECT connection does the watching: with the observation pool
	// pinned to one connection, the watcher cannot share it.
	watcher, err := sql.Open("pgx", f.appDSN)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: open the settlement watcher: %v", err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	watcher.SetMaxOpenConns(2)

	pids := make(chan int, 1)
	started := time.Now()
	stalled, observeErr := commitOutcomeObserveOnce(func(callCtx context.Context) (bool, error) {
		conn, err := observer.Conn(callCtx)
		if err != nil {
			return false, err
		}
		defer func() { _ = conn.Close() }()
		var pid int
		if err := conn.QueryRowContext(callCtx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return false, err
		}
		pids <- pid
		var slept string
		// Deliberately longer than one call is allowed to take.
		err = conn.QueryRowContext(callCtx,
			`SELECT pg_sleep($1)::text`, (commitOutcomeObserveCall + 10*time.Second).Seconds(),
		).Scan(&slept)
		return err == nil, err
	})
	elapsed := time.Since(started)
	if stalled {
		t.Fatal("the stalled observation reported success: it was never bounded")
	}
	if observeErr == nil {
		t.Fatal("the stalled observation returned no error, so nothing named the inability")
	}
	if elapsed > commitOutcomeObserveWindow {
		t.Fatalf("the stalled observation took %s, past the %s window: the call is not bounded by its own deadline",
			elapsed, commitOutcomeObserveWindow)
	}

	var stalledPID int
	select {
	case stalledPID = <-pids:
	default:
		t.Fatal("COULD_NOT_LOOK: the stalled call never announced its backend, so no settlement can be attributed to it")
	}

	// SETTLEMENT OF THE IDENTIFIED BACKEND, not of anything that happens to be
	// idle. Until the server has ended that statement the connection is not
	// reusable, however promptly the Go call returned.
	settled := commitOutcomeObservePoll(t, commitOutcomeObserveWindow,
		func(callCtx context.Context) (bool, error) {
			var active int
			if err := watcher.QueryRowContext(callCtx, `
				SELECT count(*) FROM pg_stat_activity
				WHERE pid = $1 AND state = 'active' AND query LIKE '%pg_sleep%'`,
				stalledPID).Scan(&active); err != nil {
				return false, err
			}
			return active == 0, nil
		})
	if !settled {
		t.Fatalf("backend %d was still running the stalled statement after %s: the bounded call left work in flight on the server",
			stalledPID, commitOutcomeObserveWindow)
	}

	// REUSE, on the single connection the stalled call occupied.
	restored := commitOutcomeObservePoll(t, commitOutcomeObserveWindow,
		func(callCtx context.Context) (bool, error) {
			var one int
			if err := observer.QueryRowContext(callCtx, `SELECT 1`).Scan(&one); err != nil {
				return false, err
			}
			return one == 1, nil
		})
	if !restored {
		t.Fatal("the one-connection pool never answered an ordinary observation after the stall: the bounded call did not release its connection")
	}
}

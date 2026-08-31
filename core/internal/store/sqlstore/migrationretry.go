// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrMigrationLockBudgetExceeded is returned when a unit's acquisition deadline is
// spent. Like the coordination error it carries attribution, because a boot that
// gives up without naming what it was waiting for is not actionable.
var ErrMigrationLockBudgetExceeded = errors.New("sqlstore: migration unit did not acquire its locks within its budget")

// ErrMigrationOutcomeUnknown is returned when a unit's result could not be
// determined. It is FAIL-CLOSED on purpose: the alternative to refusing is
// guessing, and the two guesses are "skip a unit that never ran" and "apply a unit
// that already committed".
var ErrMigrationOutcomeUnknown = errors.New("sqlstore: migration unit outcome could not be determined")

// ErrMigrationNeedsNewSession reports a failure whose remedy is a NEW connection
// rather than a new transaction.
//
// It is a distinct error because the runner is deliberately not the component that
// can act on it: it runs on the connection that holds the cluster-wide coordination
// lock, so replacing that session means releasing the lock and re-acquiring it
// against the coordination budget — a decision about the whole migration, not about
// one unit. Surfacing it fail-closed, named, is the correct behavior for a boot;
// swallowing it into a same-session retry is not.
// ErrMigrationUnauthorised reports a unit whose intent, declared poststate or
// observed precondition does not authorize the change it was about to make.
//
// It is separate from every other error here because it is raised BEFORE anything
// happens. The rules it enforces used to live only inside reconciliation, which is to
// say they only ran if something else had already failed — so a unit could project,
// execute, write a receipt and commit an unauthorized change, and be judged afterwards
// or never.
var ErrMigrationUnauthorised = errors.New("sqlstore: migration unit is not authorized to make this change")

// ErrMigrationPostconditionFailed reports a unit whose work did not leave the object
// in the state the manifest declared, checked while the locks are still held.
var ErrMigrationPostconditionFailed = errors.New("sqlstore: migration unit did not leave the object in its declared state")

var ErrMigrationNeedsNewSession = errors.New("sqlstore: migration unit needs a new session, and this connection holds the coordination lock")

const (
	// unitAcquisitionBudget is the per-unit deadline for taking every lock the unit
	// needs. Separate from the coordination budget: waiting for another NODE to
	// finish migrating and waiting for a long-running WRITER to release a table are
	// different situations, and one value for both would be wrong for one of them.
	unitAcquisitionBudget = 10 * time.Minute
	// unitExecutionTimeout bounds the work AFTER every lock is held. It is a
	// separate clock from acquisition because statement_timeout cannot express
	// "only the part after the wait": it runs from statement arrival, wait
	// included, which is why acquisition needs its own statement and its own bound.
	unitExecutionTimeout = 60 * time.Second
)

// unitLockTimeout is the per-acquisition ceiling, itself clamped to whatever the budget
// has left.
//
// It is a var for the same single reason guardCloseLockTimeout is: a regression proves a
// SERVER-side ceiling exists by making it fire, and the only way to observe the server's
// ceiling rather than the client's is to make the server's the smaller of the two. The
// budget's context deadline is the whole remaining budget, so at the production pair
// (10-minute budget, 60-second ceiling) the server fires first — which is the case that
// matters and the case a test cannot reach in under a minute without shortening this.
// Measured while writing that regression: with a 2-second budget the CLIENT deadline wins,
// the failure never reaches classifyCancel at all, and a test built on it passes with the
// defect restored. That test existed for one round and proved nothing.
var unitLockTimeout = 60 * time.Second

// commitTx performs the transaction's commit.
//
// It is a variable for ONE reason, stated plainly rather than disguised: a commit that
// becomes DURABLE and whose acknowledgement is then lost is the only failure that can
// make a retry apply a unit twice, and there is no deterministic way to produce it
// from outside this process. Closing the client socket mid-commit is faithful to the
// wire but races the commit; terminating the backend aborts the transaction, which is
// the opposite case. Replacing this lets a test run a REAL commit and then hand back a
// transport error, so the projectors read a receipt that is genuinely on disk.
//
// Production never assigns it, and nothing outside this package can.
var commitTx = func(tx *sql.Tx) error { return tx.Commit() }

// rowQuerier is the minimal surface a PROJECTION needs, and the narrowness is the
// point.
//
// Projections read; they must not be able to execute DDL or open a transaction. Taking
// dialect.Execer gave them both, and it also made the postcondition check impossible
// to write: *sql.Tx cannot satisfy Execer because it has no BeginTx, so the one
// reading that has to happen INSIDE the transaction could not be expressed.
//
// Both *sql.Conn and *sql.Tx satisfy this, which is exactly the set of handles a
// projection is ever handed.
type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// retryUnit is one atomically-applied change to one target relation, with the
// locks it needs declared rather than discovered.
//
// The shape — project, acquire, execute, receipt — is not decoration. Each part
// exists because collapsing it into its neighbor breaks a property:
//
//   - project runs BEFORE anything touches the target, so an unauthorized unit can be
//     refused without taking a single lock — and so the locked re-read has something to be
//     compared against when the world moves in between. (It used to be because the
//     acquisition statement was allowed to mutate and destroyed the evidence of its own
//     precondition; validate() now refuses anything but an inert generated LOCK TABLE.)
//   - acquire takes every lock the unit will need, strongest first, because
//     escalating mode inside a transaction is a documented deadlock recipe.
//   - execute runs only once every lock is held, which is the only point at which
//     an execution timeout means what it says.
//   - receipt commits WITH the work it attributes. A receipt in its own transaction
//     is a claim about work, not evidence of it.
type retryUnit struct {
	// Spec is what the manifest authorizes: the intent AND the exact poststate. It
	// governs the ordinary path, not only reconciliation.
	Spec unitSpec
	// Plan declares every relation the unit touches.
	Plan lockPlan
	// ObserverDSN, when non-empty, enables blocking attribution during acquire.
	ObserverDSN string

	// Project reads the prestate. It must not reference the target relation in a
	// way that takes a lock — catalogs only.
	Project func(ctx context.Context, db rowQuerier) (prestate, error)
	// Fence stabilizes whatever the unit depends on that a RELATION lock cannot reach.
	//
	// It exists because lockPlan can only name relations, and a unit's authorisation can rest
	// on a catalog object that is not one. The measured case is the shared trigger function:
	// the canonical projection includes its source, CREATE OR REPLACE FUNCTION needs no lock
	// on the relation whose trigger calls it, and it opens pg_proc in RowExclusiveLock — a
	// mode compatible with the AccessShareLock a projection takes. So a concurrent replace can
	// commit between the projection that authorizes a unit and the receipt that attests it,
	// and the receipt is false the instant it commits.
	//
	// It runs INSIDE the attempt's transaction, after the metadata prefix and before the
	// target, so whatever it takes is held until the receipt commits with the work — which is
	// the only property that makes it a fence rather than a re-read.
	//
	// Optional: a unit with nothing outside its relations to stabilize leaves it nil.
	Fence func(ctx context.Context, tx *sql.Tx) error
	// Execute runs the unit's remaining work under the locks acquire took. It must
	// request no new lock on any relation outside Plan; the regression that pins
	// that is the reason the plan is declared at all.
	Execute func(ctx context.Context, tx *sql.Tx, pre prestate) error
	// Receipt writes the attribution row, in the same transaction.
	Receipt func(ctx context.Context, tx *sql.Tx, pre prestate) error
	// ProjectReceipt and ProjectObject read the two independent views reconciliation
	// decides from.
	//
	// They are PROJECTIONS rather than a verdict on purpose. An injected
	// "Reconcile() (reconcileOutcome, error)" would let every call site invent its own
	// matrix, and the matrix is the part that must not vary: it is where "receipt
	// without object" is refused instead of resolved. Callers supply readings; the
	// decision is reconcileOutcomeFor's alone.
	//
	// A projector that errors is not an absent projection. Its failure is folded into
	// Readable=false, which the matrix turns into outcomeUnknown — fail-closed, never
	// "there was nothing there".
	ProjectReceipt func(ctx context.Context, db rowQuerier) (receiptProjection, error)
	ProjectObject  func(ctx context.Context, db rowQuerier) (objectProjection, error)

	// ReconcileSession opens the handle reconciliation reads through, and it is a
	// DIFFERENT session from the one the unit ran on.
	//
	// That separation is the entire point, not an optimisation. Reconciliation is
	// reached almost exclusively after a COMMIT whose answer never arrived — and a
	// COMMIT with no answer is, in practice, a COMMIT whose connection has just died.
	// Reading the receipt back through that same connection asks the corpse whether it
	// survived: both projections fail, both fold to Readable=false, and the matrix
	// answers outcomeUnknown. Fail-closed, so nothing is corrupted — but the one
	// question worth asking ("did my work become durable?") can then NEVER be answered
	// with yes, and every lost acknowledgement becomes a boot that will not complete
	// until a human looks.
	//
	// It is a factory rather than a handle because it must be opened AFTER the failure,
	// not held across it: a connection checked out in advance would be sitting idle
	// against max_connections for the whole unit, and might itself have died meanwhile.
	//
	// The release function is always called, and must tolerate being called after a
	// failed open.
	//
	// WHAT THIS DOES NOT GIVE, stated because the gap is real: the new session does not
	// hold the coordination advisory lock. If the unit's session died, the server
	// released that lock, so another node may act between the failure and this reading.
	// Reconciliation is a READ, so it cannot corrupt anything — but its answer describes
	// the database at the moment it looked, and closing that window belongs to the
	// durable gate, not here.
	ReconcileSession func(ctx context.Context) (db rowQuerier, release func(), err error)

	// BeforeAttempt runs before each attempt's transaction is opened, and OUTSIDE it.
	//
	// Outside is the whole reason it exists as a separate hook rather than as the first
	// thing Execute does. What it is for is the durable proof that an attempt BEGAN — and
	// proof that shares the attempt's transaction is proof that vanishes with it: a process
	// killed mid-unit would roll back both the work and the record that it was ever tried,
	// leaving the next boot unable to tell "never attempted" from "attempted and
	// interrupted". Those two have different correct answers.
	//
	// Its failure is FAIL-CLOSED: the attempt does not proceed. A unit that cannot record
	// that it started must not start, because the alternative is work whose existence the
	// ledger cannot account for.
	//
	// The attempt number is 1-based and counts attempts of THIS unit within this run, which
	// is what lets a callback derive a per-attempt identity without the runner having to
	// know what an identity is.
	BeforeAttempt func(ctx context.Context, attempt int) error
	// AfterFailure runs after an attempt's transaction has been rolled back, with the
	// failure and the decision taken about it.
	//
	// After the rollback, again for a reason rather than for tidiness: this is where the
	// FAILURE is recorded, and a record written inside the transaction that failed would be
	// rolled back with it. The classification is passed in because the durable record must
	// carry the retry class — a later boot routes on the class, never on the message.
	//
	// Its own failure is logged and does NOT replace the original error. The unit already
	// has a real diagnosis; losing it to a bookkeeping failure would be trading the answer
	// for the note about the answer.
	AfterFailure func(ctx context.Context, attempt int, f unitFailure, decision retryDecision, err error)
}

// run applies the unit on conn, retrying whole transactions until it succeeds, is
// classified as permanent, or runs out of budget.
//
// conn is the connection that already holds the coordination lock. Everything
// happens there: the whole point of R1 is that session-scoped settings govern the
// statements they were set for, and that is only true when there is one session.
func (u retryUnit) run(ctx context.Context, conn *sql.Conn, b *lockBudget) error {
	// THE SPEC IS VALIDATED FIRST, before the prestate is even projected. An
	// unrecognized intent, or a manifest declaring a canonical state of 'D', must not
	// reach a callback at all — and until now the only thing that checked either was
	// reconciliation, which runs after a failure and therefore never on the path that
	// succeeds.
	if err := u.Spec.validate(); err != nil {
		return err
	}
	// The plan next, because every property it checks is about statements that are
	// about to be issued: a duplicated relation, a prefix out of order, a metadata
	// lock stronger than the target. Each produces a deadlock or a lock-ordering
	// violation at runtime, and a check that runs after the first statement is a check
	// that runs after the damage.
	if err := u.Plan.validate(); err != nil {
		return err
	}
	// Execute and Receipt are called unconditionally; a nil one panics inside a
	// transaction holding locks, which is the worst place in this file to panic.
	if u.Execute == nil || u.Receipt == nil {
		return fmt.Errorf("%w: %s has no Execute or Receipt callback",
			ErrMigrationUnauthorised, u.Plan.Target.displayRelation())
	}
	// A unit that cannot READ its poststate cannot be held to it. Making the check
	// conditional on ProjectObject being set meant a caller could opt out of the
	// postcondition entirely by leaving a field nil — the guarantee silently absent
	// rather than loudly refused.
	if u.Project == nil || u.ProjectObject == nil || u.ProjectReceipt == nil {
		return fmt.Errorf("%w: %s is missing a projector, so its precondition, poststate or receipt could never be verified",
			ErrMigrationUnauthorised, u.Plan.Target.displayRelation())
	}
	// And a session to reconcile THROUGH, checked here with the rest rather than at the
	// moment it is needed. The moment it is needed is after a COMMIT whose outcome is
	// unknown, which is the worst possible place to discover a missing callback: the
	// alternative to reading is guessing, and both guesses corrupt the ledger.
	if u.ReconcileSession == nil {
		return fmt.Errorf("%w: %s has no reconcile session, so a commit whose acknowledgement is lost could never be resolved",
			ErrMigrationUnauthorised, u.Plan.Target.displayRelation())
	}

	var lastHolders []lockHolder
	for attempt := 1; ; attempt++ {
		// THE PRESTATE IS RE-PROJECTED EVERY ATTEMPT. A failed attempt rolls back and
		// releases its locks, so between attempts the object is unprotected and can be
		// altered by anything. Carrying one projection across retries meant deciding
		// the second attempt on facts that were true before the first.
		// The projection is INSIDE the deadline, like every other roundtrip. It ran on
		// the caller's context, so a budget that was already spent still paid for one
		// more query before anything checked — measured as
		// EXPIRED_PREPROJECT|projects=1 against a budget with nothing left.
		if b.expired() {
			return fmt.Errorf("%w: %s (budget spent before the prestate could be projected)",
				ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())
		}
		prctx, prcancel := b.context(ctx)
		pre, err := u.Project(prctx, conn)
		prcancel()
		if err != nil {
			return u.budgetFailure(ctx, b, "while the prestate was being projected",
				fmt.Errorf("sqlstore: project prestate for %s: %w", u.Plan.Target.displayRelation(), err))
		}
		// THE READING IS CHECKED FOR COHERENCE BEFORE IT IS TRUSTED. Project is
		// caller-supplied, and the authorisation rules below interrogate its fields
		// singly — so a projection reporting "no guard" and "guard state A" at once
		// authorizes a create-guard over an object that already carries an ALWAYS guard.
		if err := pre.validate(); err != nil {
			return fmt.Errorf("%w (projected for %s)", err, u.Plan.Target.displayRelation())
		}
		// A UNIT THAT IS ALREADY RECEIPTED DOES NOT RUN AGAIN.
		//
		// Without this shortcut a unit whose receipt is already durable — a boot that was
		// interrupted after committing, a coordinator that lost its own bookkeeping — would
		// execute a second time and then try to insert a second receipt. The insert is
		// idempotent, so the second attempt would not corrupt the ledger; the DDL is not, and
		// "re-run the DDL and then discover the receipt was already there" is the wrong order
		// to find out.
		//
		// It reconciles through the CURRENT connection rather than opening another. There is
		// no dead session here: this is the ordinary entry path, the session is the live one
		// holding the coordination lock, and a second connection would be spent asking a
		// question the first one can answer.
		if pre.ReceiptPresent {
			switch res := u.reconcileThrough(ctx, conn, pre); res {
			case outcomeApplied:
				return nil
			case outcomeNotApplied:
				// A receipt the matrix says attributes nothing of this unit. Fall through and
				// let the ordinary path decide — the precondition below is what refuses it if
				// the object contradicts the intent.
			default:
				return fmt.Errorf("%w for %s (%s) on entry: a receipt already attributes this unit",
					ErrMigrationOutcomeUnknown, u.Plan.Target.relation(), res)
			}
		}

		// And the PRECONDITION is checked against that fresh projection, before the
		// target statement — which is the statement that destroys the evidence of its
		// own precondition.
		if _, ok := expectedEnableState(u.Spec, pre); !ok {
			return fmt.Errorf("%w: intent %q on %s found guard state %q, which it does not authorize",
				ErrMigrationUnauthorised, u.Spec.Intent, u.Plan.Target.displayRelation(), pre.GuardEnableState)
		}

		// THE DURABLE PROOF THAT AN ATTEMPT BEGAN, committed before the attempt's own
		// transaction exists. Fail-closed: a unit that cannot record that it started must not
		// start, because the next boot would be unable to tell "never attempted" from
		// "attempted and interrupted", and those two authorize different things.
		//
		// BOUNDED LIKE EVERY OTHER ROUNDTRIP, and it used to be the one exception. This hook
		// opens its OWN transaction on the connection that holds the cluster-wide migration
		// lock and issues five roundtrips through it (BEGIN, the ordinal count, the header
		// read, the INSERT, COMMIT), and the INSERT aims at three unique indexes. The
		// `SET LOCAL lock_timeout/statement_timeout` this runner arms live in the ATTEMPT's
		// transaction, so they do not reach here — and on the caller's raw context, which on
		// the boot path is context.Background() with no deadline (measured by this file at
		// EXECUTE_CONTEXT_BOUND|deadline_present=false), an uncommitted row on any of those
		// keys makes this wait with no ceiling. This file's own words for that case:
		// "on a boot path, forever means a process that never finishes starting and never
		// says why".
		//
		// The re-check of expiry is the same one armAcquisition does after spending
		// roundtrips: the projection above cost time, and entering a fresh acquisition with a
		// spent budget is how a unit ends up asking for a lock it can never be granted.
		//
		// THE RESIDUAL HOLE, DECLARED RATHER THAN IMPLIED: database/sql does not interrupt a
		// `tx.Commit()` already in flight when its context ends, so the COMMIT this hook issues
		// last is bounded by the server and the transport, not by the context above. That is
		// the same limit the close names (see guardCloseTxClock), and it is narrower than what
		// was here before: the four roundtrips BEFORE the commit — which are the ones that wait
		// on the three unique indexes of the gate-event log — are now bounded.
		if u.BeforeAttempt != nil {
			if b.expired() {
				return fmt.Errorf("%w: %s (budget spent before the start of attempt %d could be recorded)",
					ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation(), attempt)
			}
			bactx, bacancel := b.context(ctx)
			err := u.BeforeAttempt(bactx, attempt)
			bacancel()
			if err != nil {
				// Routed through budgetFailure, not returned raw. A budget that expired here
				// must read as ErrMigrationLockBudgetExceeded rather than as a transport
				// failure — the latter classifies as retryNewSession, which asks the caller to
				// replace the very session holding the migration lock for the whole cluster.
				return u.budgetFailure(ctx, b, "recording the start of the attempt",
					fmt.Errorf("sqlstore: record the start of attempt %d on %s: %w",
						attempt, u.Plan.Target.displayRelation(), err))
			}
		}

		f, holders, judged := u.attempt(ctx, conn, b, pre)
		if len(holders) > 0 {
			lastHolders = holders
		}
		if f.Err == nil {
			return nil
		}

		decision, wrapped := classifyFailure(ctx, f)
		// THE FAILURE IS RECORDED AFTER THE ROLLBACK, which has already happened: attempt's
		// deferred rollback runs before it returns. Recording it inside the failed transaction
		// would roll the record back with the work, leaving a boot that failed for a stated
		// reason and a ledger that says nothing happened.
		if u.AfterFailure != nil {
			u.AfterFailure(ctx, attempt, f, decision, wrapped)
		}
		switch decision {
		case retryPropagate, retryNever:
			return wrapped
		case retryNewSession:
			// The runner cannot honor this itself, and saying so is the honest
			// answer rather than quietly downgrading it to a new transaction.
			//
			// This connection is the one holding the cluster-wide coordination lock.
			// Replacing it means releasing that lock, opening another session and
			// re-acquiring — which is a decision about the whole migration, taken
			// against the coordination budget, and it belongs to withMigrationLock.
			// Retrying on the session that just told us it is unusable would be the
			// one thing the classification exists to prevent.
			return fmt.Errorf("%w for %s during %s: %w",
				ErrMigrationNeedsNewSession, u.Plan.Target.displayRelation(), f.Phase, wrapped)
		case retryAfterReconcile:
			// The unit's fate is unknown — typically a COMMIT that failed on the
			// wire after succeeding on the server. Ask the database what actually
			// happened before doing anything else; retrying blind here is how a
			// committed unit gets applied twice.
			// THE PRESTATE THE ATTEMPT JUDGED AGAINST, not the one projected before the
			// lock. See attempt's doc comment: the pre-lock projection can be stale by the
			// time the unit commits, and reconciling against it turns a valid durable unit
			// into a divergent one.
			res := u.reconcile(ctx, judged)
			switch res {
			case outcomeApplied:
				return nil
			case outcomeNotApplied:
				// Genuinely untouched: a fresh transaction may try again.
			default:
				return fmt.Errorf("%w for %s (%s) after %w",
					ErrMigrationOutcomeUnknown, u.Plan.Target.relation(), res, wrapped)
			}
		case retryNewTransaction:
			// Fall through to the budget check.
		}

		waited, berr := b.backoff(ctx, attempt, coordinationBackoffBase, coordinationBackoffMax)
		if berr != nil {
			return berr
		}
		if !waited {
			return fmt.Errorf("%w: %s after %d attempts, %s: %w",
				ErrMigrationLockBudgetExceeded, u.Plan.Target.relation(), attempt, describeHolders(lastHolders), wrapped)
		}
	}
}

// budgetFailure normalises an error produced under a budget-derived context, so that a
// SPENT DEADLINE always surfaces as the deadline and not as whatever the canceled
// operation happened to return.
//
// Every roundtrip in this runner already gets the right context. What was missing is that
// the SHAPE of the resulting error decided its routing. Two measurements, both on
// cooperative callbacks that simply waited for the deadline they were handed:
//
//	ROUND9_PREPROJECT_BUDGET|elapsed=150.957836ms|budget=150ms|
//	err=... project prestate ...: context deadline exceeded|budget_sentinel=false
//
//	ROUND9_LOCKED_PROJECT_BUDGET|elapsed=150.725029ms|budget=150ms|projects=2|
//	err=... needs a new session ... re-project ...|budget_sentinel=false|new_session=true
//
// The second is the damaging one. classifyFailure routes a non-PgError before commit to
// retryNewSession, so "this unit ran out of time" reached the caller as "replace the
// session that holds the cluster-wide coordination lock" — a decision about the whole
// migration, taken on the strength of an error shape.
//
// The coordinator already had this right; budgetSpent gives the parent's cancellation
// precedence and then consults the budget's own clock rather than trusting the error, which
// is what makes it work even when pgx converts an expired context into driver.ErrBadConn.
// This is the runner's use of the same primitive.
func (u retryUnit) budgetFailure(parent context.Context, b *lockBudget, what string, err error) error {
	if budgetSpent(parent, b, err) {
		return fmt.Errorf("%w: %s (the budget expired %s): %w",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation(), what, err)
	}
	return err
}

// reconcile asks the database what the unit actually did, and routes the answer
// through the one matrix that is allowed to decide it.
//
// A projector that fails yields Readable=false rather than an error return, because
// the matrix already has the right answer for a reading that did not happen —
// outcomeUnknown — and giving the caller a second way to express it is how the two
// drift apart.
func (u retryUnit) reconcile(ctx context.Context, pre prestate) reconcileOutcome {
	// THE BOUND IS ENFORCED BY ABANDONMENT, not by a context, and the difference is the
	// whole reason this is a goroutine.
	//
	// context.WithTimeout is a REQUEST. It can only end a callback that watches its
	// context, and every callback here is supplied by the caller: one that blocks on a
	// mutex, a channel or a retry loop of its own would hang this call forever. On a boot
	// path, forever means a process that never finishes starting and never says why. The
	// earlier version of this comment claimed a "hard bound" it did not have.
	//
	// The goroutine OWNS the session for its entire life and releases it on the way out.
	// That ordering is what makes abandonment safe: this function never touches the
	// handle, so it cannot close a connection out from under a projection still using it.
	// An abandoned goroutine lives until its projection returns — the cost of a caller
	// supplying a projection that never does, paid in one goroutine rather than in a boot
	// that never completes.
	type reading struct {
		r  receiptProjection
		o  objectProjection
		ok bool
	}
	done := make(chan reading, 1) // buffered: an abandoned goroutine must never block

	go func() {
		// A SESSION THAT IS NOT THE ONE THAT JUST FAILED. See ReconcileSession: reading
		// the receipt back through the connection whose COMMIT went unanswered asks the
		// corpse whether it survived, and gets outcomeUnknown every time.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileProjectionTimeout)
		defer scancel()
		conn, release, err := u.ReconcileSession(sctx)
		if release != nil {
			defer release()
		}
		if err != nil {
			// Fail closed. An unopenable session is not an absent receipt.
			slog.Warn("could not open a session to reconcile an interrupted migration unit; its outcome stays unknown",
				"target", u.Plan.Target.displayRelation(), "err", err)
			done <- reading{}
			return
		}
		if conn == nil {
			slog.Warn("the reconcile session callback returned no handle and no error; the unit's outcome stays unknown",
				"target", u.Plan.Target.displayRelation())
			done <- reading{}
			return
		}
		r, o := u.readProjections(ctx, conn)
		done <- reading{r: r, o: o, ok: true}
	}()

	select {
	case got := <-done:
		if !got.ok {
			return outcomeUnknown
		}
		return reconcileOutcomeFor(u.Spec, pre, got.r, got.o)
	case <-time.After(reconcileTotalBound):
		slog.Warn("a migration unit's reconciliation did not finish within its bound and was abandoned; the unit's outcome stays unknown",
			"target", u.Plan.Target.displayRelation(), "bound", reconcileTotalBound)
		return outcomeUnknown
	}
}

// reconcileCooperativePhases is how many sequential operations reconcile performs, each
// of which is allowed a full reconcileProjectionTimeout: opening the session, reading the
// receipt, reading the object.
//
// It is spelled out and used to DERIVE the backstop rather than being folded into a
// literal, because the literal already went stale once. The backstop was sized for two
// phases and then a third — the mandatory session open — was added by the very fix that
// made reconciliation correct, leaving a bound narrower than the contract it was meant to
// protect. Measured with three cooperative 9s operations, all inside their individual
// limits:
//
//	RECONCILE_SESSION_BUDGET|elapsed=25.034182711s|bound=25s|session_opened=true|
//	receipt_done=true|object_started=true|object_done=false|outcome=unknown
//
// A perfectly legible reconciliation turned into ErrMigrationOutcomeUnknown, halting a
// boot after a durable commit.
//
// WHAT THIS BUYS, stated exactly: the count is a named literal and a regression pins it,
// so changing the number of phases without changing it MAKES THE CHANGE VISIBLE. It does
// not PREVENT one — the phases are not built from a collection whose length feeds this,
// so a fourth optional callback could still be added and compile. Deriving it from a
// declared sequence of steps is the stronger guarantee, and belongs with the manifest
// work that will define what those steps are.
const reconcileCooperativePhases = 3

// reconcileTotalBound is how long reconciliation may take before the caller walks away.
//
// Wider than every cooperative phase put together, on purpose: an operation that IS
// watching its context already ends at reconcileProjectionTimeout, and cutting the
// sequence off before they can all finish would throw away the answer in the ordinary
// slow case — which is the case this exists for. This bound is not the normal ceiling; it
// is the backstop for a callback that ignores its context entirely.
//
// The margin covers scheduling and the matrix, not another roundtrip.
const reconcileTotalBound = reconcileCooperativePhases*reconcileProjectionTimeout + 5*time.Second

// reconcileThrough is the reading itself, separated from where the session comes from.
func (u retryUnit) reconcileThrough(ctx context.Context, conn rowQuerier, pre prestate) reconcileOutcome {
	r, o := u.readProjections(ctx, conn)
	return reconcileOutcomeFor(u.Spec, pre, r, o)
}

// readProjections takes the two independent readings the matrix decides from.
//
// EACH GETS ITS OWN BOUNDED CONTEXT, DERIVED FROM A CANCELLATION-FREE PARENT — the same
// shape, and for the same reason, as the block observer's probe.
//
// Reconciliation is not the unit's work; it is the DIAGNOSIS of the unit's failure, and
// it is reached precisely when that failure was a deadline expiring or a caller
// canceling mid-COMMIT. Bounding it by the budget it exists to explain would kill it
// exactly when it is needed: a spent budget would make both readings fail, the matrix
// would answer outcomeUnknown, and a unit that had genuinely committed would be reported
// as undetermined — the one answer that leaves an operator with no move but manual
// inspection.
//
// These deadlines are COOPERATIVE and that word is load-bearing: a context can end a
// projection that watches it, and nothing more. The backstop for one that does not is
// reconcile's abandonment, which is where the actual bound lives.
func (u retryUnit) readProjections(ctx context.Context, conn rowQuerier) (receiptProjection, objectProjection) {
	read := func(what string, f func(context.Context, rowQuerier) error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileProjectionTimeout)
		defer cancel()
		if err := f(rctx, conn); err != nil {
			slog.Warn("could not read a projection while reconciling an interrupted migration unit",
				"projection", what, "target", u.Plan.Target.displayRelation(), "err", err)
		}
	}

	var r receiptProjection
	if u.ProjectReceipt != nil {
		read("receipt", func(rctx context.Context, db rowQuerier) error {
			got, err := u.ProjectReceipt(rctx, db)
			if err != nil {
				return err
			}
			r, r.Readable = got, true
			return nil
		})
	}
	var o objectProjection
	if u.ProjectObject != nil {
		read("object", func(rctx context.Context, db rowQuerier) error {
			got, err := u.ProjectObject(rctx, db)
			if err != nil {
				return err
			}
			o, o.Readable = got, true
			return nil
		})
	}
	return r, o
}

// reconcileProjectionTimeout bounds ONE reconciliation reading.
//
// Generous compared with the observer's probe, because the answers are not equivalent:
// a missed observation costs an operator some attribution, while a missed
// reconciliation costs the difference between "this unit committed" and "nobody knows",
// on the path that decides whether a schema change gets applied a second time.
const reconcileProjectionTimeout = 10 * time.Second

// attempt runs the unit once in a fresh transaction, returning the failure with
// everything the classifier needs: the phase it reached, the timeout this runner
// armed for the statement that failed, and how long that statement actually ran.
//
// Armed and Elapsed travel with the failure because they are not recoverable
// afterwards, and without them 57014 is undecidable. The classifier would have to
// assume the cancellation was its own — which is how every pg_cancel_backend on the
// system used to become a retry.
// The THIRD return value is the prestate the attempt actually judged against, and it
// exists because reconciliation was being handed the wrong one.
//
// attempt re-reads the precondition under the lock and replaces its local copy — that
// re-read is the authoritative one, and everything downstream of it is judged against it.
// But the old signature carried only the failure and the holders, so run() reconciled
// with the projection it still held from BEFORE the lock. Measured with an adopt-legacy
// unit, for which both O and A are authorized prestates:
//
//	ROUND9_LOCKED_PRESTATE|projects=2|initial=O|locked=A|durable_state=A|receipts=1|
//	err=... outcome could not be determined ... (divergent)
//
// The receipt and the 'A' state were durable and correct. Against pre=A the object
// satisfies adoption; against the stale pre=O it looks divergent — so a valid, committed
// unit halted the boot. Returning it makes the handoff explicit instead of relying on a
// mutation to a copy nobody could see.
func (u retryUnit) attempt(ctx context.Context, conn *sql.Conn, b *lockBudget, pre prestate) (unitFailure, []lockHolder, prestate) {
	fail := func(phase unitPhase, armed, elapsed time.Duration, err error) unitFailure {
		return unitFailure{Phase: phase, Err: err, Armed: armed, Elapsed: elapsed}
	}

	if b.expired() {
		return fail(phaseAcquire, 0, 0, fmt.Errorf("%w: %s (budget spent before the attempt began)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.relation())), nil, pre
	}

	// The transaction's context must live as long as the TRANSACTION, not as long as
	// the BEGIN. database/sql documents that the context given to BeginTx is used
	// until commit or rollback and that canceling it rolls the transaction back, so
	// canceling it straight after BEGIN destroys the transaction on the spot —
	// measured as `sql: transaction has already been committed or rolled back` on the
	// first statement.
	//
	// Deferring the cancel is not a workaround for that; it is the semantics this
	// wants. A budget that expires mid-unit now rolls the work back by itself, which
	// is the fail-closed outcome, and the deferred rollback below stays correct
	// because it is a no-op after either.
	bctx, bcancel := b.context(ctx)
	defer bcancel()
	tx, err := conn.BeginTx(bctx, nil)
	if err != nil {
		return fail(phaseAcquire, 0, 0, u.budgetFailure(ctx, b, "opening the transaction", err)), nil, pre
	}
	// A rollback on every path that does not commit. It is what makes "new
	// transaction per retry" true rather than aspirational: reusing an aborted
	// transaction fails 25P02 on the next statement and would classify as a runner
	// bug, which is exactly what it would be.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback() //nolint:errcheck // no-op after commit; the caller already has the real error
		}
	}()

	// --- Acquire -----------------------------------------------------------
	//
	// Metadata first, in the declared total order, ONE STATEMENT PER RELATION.
	// THE CEILING CURRENTLY IN FORCE, carried across the arming calls.
	//
	// armAcquisition returns the value setLocalTimeouts reports, and that is ZERO when
	// either of its two round trips fails. classifyCancel only recognizes our own timeout
	// with Armed > 0 AND Elapsed > 0, so a 57014 raised BY THE CEILING ALREADY IN FORCE
	// during the next arming attempt arrived as Armed=0 and was blamed on an outsider —
	// the same defect as C4-09, one call earlier, and measuring Elapsed alone did not close
	// it. `SET LOCAL` lives to the end of the transaction, so after the first successful
	// arm there IS a ceiling, and it is the one that would have fired. Before the first
	// arm there is none, and zero is then the honest answer.
	inForce := time.Duration(0)
	// LOCK TABLE with a list takes its relations one at a time, so lock_timeout
	// restarts for each and a single multi-relation statement can overrun the
	// budget by a factor of its length — measured at 4703 ms against a 3 s budget
	// for three relations. Recomputing what is left before each statement is the
	// only shape in which the deadline is hard.
	for _, pl := range u.Plan.Metadata {
		armStarted := b.now()
		armed, err := u.armAcquisition(ctx, tx, b)
		if err != nil {
			return fail(phaseAcquire, armedOrInForce(armed, inForce), b.now().Sub(armStarted), err), nil, pre
		}
		inForce = armed
		lctx, lcancel := b.context(ctx)
		started := b.now()
		_, err = tx.ExecContext(lctx, pl.lockStatement())
		lcancel()
		if err != nil {
			return fail(phaseAcquire, armed, b.now().Sub(started),
				u.budgetFailure(ctx, b, "taking a metadata lock", err)), nil, pre
		}
	}

	// THE FENCE, between the metadata prefix and the target, so it sits at a FIXED position
	// in the total order every unit takes. What it stabilizes is not a relation, so no entry
	// in Plan could have expressed it — see the field's documentation for the measured case.
	if u.Fence != nil {
		armStarted := b.now()
		armed, err := u.armAcquisition(ctx, tx, b)
		if err != nil {
			return fail(phaseAcquire, armedOrInForce(armed, inForce), b.now().Sub(armStarted), err), nil, pre
		}
		inForce = armed
		fnctx, fncancel := b.context(ctx)
		started := b.now()
		err = u.Fence(fnctx, tx)
		fncancel()
		if err != nil {
			return fail(phaseAcquire, armed, b.now().Sub(started),
				u.budgetFailure(ctx, b, "fencing what the lock plan cannot name", err)), nil, pre
		}
	}

	// The target, last, because it is the one with real concurrent writers — and
	// with the strongest mode the whole unit needs, because escalating later is the
	// deadlock recipe. The observer starts BEFORE this statement: it is the only
	// one here that can block for long, and attribution after the fact is exactly
	// what pg_blocking_pids cannot give.
	var obs *blockObserver
	if u.ObserverDSN != "" {
		var waiterPID int
		// Bounded like every other roundtrip. It was the one statement in Acquire
		// still running on the caller's context, and being "just the observer's setup"
		// is not a reason: it is a roundtrip on the same connection, in the same
		// phase, and it can hang for the same reasons. Measured with a 300ms server
		// wait against a 50ms budget, the attempt took 307.610197ms.
		pctx, pcancel := b.context(ctx)
		err := tx.QueryRowContext(pctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&waiterPID)
		pcancel()
		if err == nil {
			obs = startBlockObserver(ctx, u.ObserverDSN, waiterPID)
		}
	}
	stopObserver := func() []lockHolder {
		if obs == nil {
			return nil
		}
		hs, degraded := obs.stopAndReport()
		if degraded != nil {
			slog.Warn("the migration block observer could not attribute this wait; the unit continues without it",
				"target", u.Plan.Target.relation(), "err", degraded)
		}
		return hs
	}

	armStarted := b.now()
	armed, err := u.armAcquisition(ctx, tx, b)
	if err != nil {
		return fail(phaseAcquire, armedOrInForce(armed, inForce), b.now().Sub(armStarted), err), stopObserver(), pre
	}
	inForce = armed
	_ = inForce
	tctx, tcancel := b.context(ctx)
	started := b.now()
	// GENERATED, not the declared string. validate() has already refused a TargetStatement
	// that differs from this, so the two are equal here — issuing the generated one anyway
	// means the statement that reaches the server cannot be anything but a LOCK TABLE on
	// the declared target at the SENTINEL's mode, whatever a future edit does to validation.
	//
	// The sentinel's mode is not always the declared one: an append-only table has
	// UPDATE/DELETE/TRUNCATE revoked, and LOCK TABLE checks privileges per mode, so the
	// strongest mode an explicit statement can take there is ROW EXCLUSIVE. See
	// lockPlan.TargetAcquire for the measurement and for what the resulting escalation costs.
	_, terr := tx.ExecContext(tctx, u.Plan.targetAcquireStatement())
	tcancel()
	targetElapsed := b.now().Sub(started)

	holders := stopObserver()
	if terr != nil {
		return fail(phaseAcquire, armed, targetElapsed,
			u.budgetFailure(ctx, b, "taking the target lock", terr)), holders, pre
	}

	// A FOOTPRINT CHECK RIGHT HERE, with acquisition complete and no unit work done
	// yet. The final photo before commit cannot show ORDER: it proves what was held,
	// never that the strongest mode was taken first. Checking at this boundary makes
	// the ordering observable — everything the plan declares must already be held, at
	// the declared mode, before a single statement of Execute runs.
	//
	// ITS PURPOSE IS NARROWER THAN IT WAS, AND NARROWER STILL THAN I FIRST WROTE.
	//
	// Since validate() requires TargetStatement to be exactly the statement this plan
	// generates, and every metadata lock is likewise generated, a plan can no longer
	// describe an acquisition that skips one of its own declarations: "missing",
	// "understated", an extra relation, a missing ONLY and a smuggled second statement are
	// all INVALID PLANS now, refused before a transaction exists.
	//
	// An earlier version of this comment then claimed the check still guards "a foreign
	// key's referenced table, a partition, an index rebuild". That is WRONG about this
	// call site, and the correction is worth more than the tidy sentence was: all three
	// arise from Execute or Receipt, which run AFTER this reading — they are caught by the
	// pre-commit footprint below. And a partition pulled in implicitly cannot arise here at
	// all, because the generated statement says ONLY.
	//
	// What is left is a lock added, during the generated LOCK TABLEs themselves, by
	// something other than the plan: the server, an extension, a ProcessUtility hook. That
	// is a real possibility and cheap to keep watching for — but it is NOT DEMONSTRATED OR
	// REGRESSED on this branch, and the honest evidence is that removing this call alone
	// leaves the whole suite green (415 pass / 0 skip / 0 fail). So it stays as a defense
	// with no discriminating test, said plainly, rather than as a guarantee somebody later
	// relies on.
	// Bounded by the budget like every other roundtrip this runner issues. It reads
	// pg_locks over the wire, so on a wedged connection it hangs exactly as long as the
	// caller's context allows — which may be forever — and the deadline that was
	// supposed to bound the whole unit would be bounding everything except its own
	// verification.
	fctx, fcancel := b.context(ctx)
	// Against the ACQUISITION plan: at this point the claim is that the sentinel took what it
	// said it would. A target declared SHARE ROW EXCLUSIVE is legitimately still at ROW
	// EXCLUSIVE here, because the DDL that escalates it has not run — checking the pre-commit
	// claim now would report the unit as understating a plan it has not finished executing.
	//
	// THE ELAPSED TIME IS MEASURED, and it used to be a literal 0 with armed > 0. That pair is
	// not a missing detail, it is a false statement about who canceled the statement:
	// classifyCancel reads Elapsed == 0 as "this cancellation cannot have been my own
	// timeout" and returns cancelUnknown, which in phaseAcquire maps to retryNever with the
	// text "statement canceled from outside this runner … not a timeout this unit armed".
	// So a 57014 raised by the statement_timeout THIS function armed thirty lines above was
	// attributed to an external pg_cancel_backend, classified permanent, and written to the
	// ledger as a block with UnblockPolicy=operator — permanently, since this edition ships
	// no repair CLI. The judge is right; the call sites were lying to it. The same call, in
	// pre-commit, has always measured (see the phaseReceipt fail below).
	footprintStarted := b.now()
	ferr := verifyLockFootprint(fctx, tx, u.Plan.acquisitionPlan())
	fcancel()
	if ferr != nil {
		return fail(phaseAcquire, armed, b.now().Sub(footprintStarted), u.budgetFailure(ctx, b, "verifying the lock footprint",
			fmt.Errorf("%w (checked at the end of acquisition, before any unit work)", ferr))), holders, pre
	}

	// --- Precondition, INSIDE the lock -------------------------------------
	//
	// The projection taken before the transaction is a decision made without
	// protection. Measured against a real server: the runner projected 'O', another
	// session moved the guard O -> D in the window before acquisition, the unit
	// carried on and committed as though it had performed the authorized O -> A —
	//
	//	PRECONDITION_TOCTOU|projected=O|intervening=D|final=A|receipts=1|err=<nil>
	//
	// The advisory lock serializes conforming NODES; it does not stop a DBA with psql.
	// So the precondition that authorizes the change is re-read here, on the
	// transaction that already holds the strongest mode the unit needs, where nothing
	// can move it. This projection — not the earlier one — is what Execute, the
	// receipt and the postcondition are judged against.
	pctx2, pcancel2 := b.context(ctx)
	// Measured, for the same reason as the footprint check above: this is the SECOND site
	// that reported Elapsed=0 under a live statement_timeout, and it is the one the published
	// limit never named. It also fails through a second path — readGuardReceipt wraps with %v
	// and drops the *pgconn.PgError, so a 57014 arriving through that half never reaches the
	// SQLSTATE switch at all — which is why measuring the time here is necessary and not
	// sufficient, and why the timeouts are no longer armed equal (see armAcquisition).
	//
	// AND ARMED IS PASSED HERE, DELIBERATELY, ALTHOUGH Project IS A CALLBACK. That reads as a
	// contradiction of unitFailure.Armed's own rule — "a multi-statement callback reports zero
	// … an Elapsed spanning the whole callback vouches for none of them" — and the two are
	// reconciled here rather than left to be rediscovered. A later measurement pass DID
	// rediscover it, proposed zero from the rule alone, and was wrong; the note is what that
	// cost.
	//
	// The rule is about ATTRIBUTION, and it exists to stop a retry being manufactured out of a
	// number that vouches for nothing. Here the two candidate errors are not symmetric:
	//
	//   armed -> the worst case is an operator's pg_cancel_backend read as our own timeout,
	//            which costs ONE more attempt against a budget that is already bounded and
	//            already expiring. Recoverable, by construction.
	//   0     -> our OWN statement_timeout reads as cancelUnknown, which in phaseAcquire is
	//            retryNever, which writes the rollout blocked with UnblockPolicy=operator.
	//            This edition ships no repair CLI, so that boot never starts again. That is
	//            C4-09's damage exactly, and it is PERMANENT.
	//
	// A conservative default is only conservative when the failure it defaults to is the
	// cheaper one, and at this site it is not. TestPostgresAnAcquisitionTimeoutIsNotBlamedOnAn
	// Outsider (migrationacquireelapsed_pg_test.go) is the executable form of that argument:
	// with zero here it fails, on a real server, against a 57014 this runner armed.
	//
	// The over-attribution window is also narrower than the callback's shape suggests, for the
	// reason the paragraph above already gives: readGuardReceipt wraps with %v and drops the
	// *pgconn.PgError, so a 57014 from the SECOND roundtrip does not reach the SQLSTATE switch
	// as a cancellation at all. What `armed` governs in practice is the FIRST statement, which
	// is the one it was armed for.
	reprojectStarted := b.now()
	locked, lerr := u.Project(pctx2, tx)
	pcancel2()
	if lerr != nil {
		return fail(phaseAcquire, armed, b.now().Sub(reprojectStarted), u.budgetFailure(ctx, b, "re-reading the precondition under its lock",
			fmt.Errorf("sqlstore: re-project %s under its lock: %w", u.Plan.Target.displayRelation(), lerr))), holders, pre
	}
	// Coherence again, on the reading that actually governs: this projection — not the
	// earlier one — is what Execute, the receipt and the postcondition are judged
	// against, so it is the one whose incoherence would authorize the change.
	if verr := locked.validate(); verr != nil {
		return fail(phaseAcquire, armed, 0,
			fmt.Errorf("%w (re-projected for %s under its lock)", verr, u.Plan.Target.displayRelation())), holders, pre
	}
	if _, ok := expectedEnableState(u.Spec, locked); !ok {
		return fail(phaseAcquire, armed, 0, fmt.Errorf(
			"%w: intent %q on %s found guard state %q once locked (it was %q when projected), which it does not authorize",
			ErrMigrationUnauthorised, u.Spec.Intent, u.Plan.Target.displayRelation(),
			locked.GuardEnableState, pre.GuardEnableState)), holders, pre
	}
	// From here on the LOCKED projection is the truth.
	pre = locked

	// --- Execute -----------------------------------------------------------
	//
	// Every lock is held now, so an execution clock finally means what it says.
	// lock_timeout is reset because SET LOCAL lasts to the end of the transaction:
	// left in place it would also govern Execute and Receipt, against the boundary
	// this whole shape exists to draw.
	exec, ok := b.clampPositive(unitExecutionTimeout)
	if !ok {
		// The budget bounds the WHOLE unit, not only its waiting. Reaching execution
		// with nothing left means the locks were won at the very edge of the deadline;
		// running on would be doing unbounded DDL on a clock that has already stopped.
		// Rolling back is the fail-closed answer, and the deferred rollback releases
		// every lock this attempt took.
		return fail(phaseExecute, 0, 0, fmt.Errorf("%w: %s (locks were acquired but the budget was spent before execution)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.relation())), holders, pre
	}
	// Bounded, and re-checked after. Arming Execute is two roundtrips like arming
	// Acquire, and they were the last ones still on the caller's context: measured, a
	// 100ms budget returned at 152.682425ms because the arming spent 80ms unbounded
	// and the failure that followed could not give the time back.
	ectx, ecancel := b.context(ctx)
	execArmed, err := setLocalTimeouts(ectx, tx, 0, exec)
	ecancel()
	if err != nil {
		return fail(phaseExecute, execArmed, 0,
			u.budgetFailure(ctx, b, "arming the execution timeout", err)), holders, pre
	}
	if b.expired() {
		return fail(phaseExecute, execArmed, 0, fmt.Errorf("%w: %s (the budget was spent while arming the execution timeout)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.relation())), holders, pre
	}
	// ARMED IS ZERO FOR A CALLBACK, and that is a deliberate refusal to guess.
	//
	// PostgreSQL restarts statement_timeout for EVERY statement, while Elapsed here
	// wraps the whole callback — which may issue many. Their sum can exceed the armed
	// value without any single statement having come close to it, so an external
	// cancellation at the start of the second statement would inherit the first
	// statement's time and look like our own timeout.
	//
	// Deciding that from the client is not possible without routing every statement a
	// callback issues through this runner, which is a change to the callback contract
	// and belongs with the manifest work. Until then the origin of a 57014 raised
	// inside a callback is UNKNOWN, which classifies permanent — the conservative
	// direction, and the one that does not argue in a loop with an operator who is
	// canceling.
	// AND THE CALLBACK GETS A CONTEXT THAT CARRIES THE DEADLINE, which is a separate
	// guarantee from the statement_timeout just armed and was missing entirely.
	//
	// Execute ran on the CALLER's context. On a boot path that context typically has no
	// deadline at all, so a cooperative callback — one doing exactly the right thing,
	// watching the context it was handed — saw no deadline to watch. Measured with a 500ms
	// unit budget:
	//
	//	EXECUTE_CONTEXT_BOUND|elapsed=2.008353536s|budget=500ms|deadline_present=false|
	//	context_ended=false
	//
	// statement_timeout does not cover this. It bounds each STATEMENT and restarts for the
	// next, which the neighboring comment already relies on; it does nothing about a
	// callback waiting on a channel, sleeping, or issuing twenty statements in a row. And
	// the transaction's own context only aborts SQL issued on tx — it cannot make a Go
	// function return.
	xctx, xcancel, ok := b.unitWorkContext(ctx)
	if !ok {
		return fail(phaseExecute, execArmed, 0, fmt.Errorf("%w: %s (the budget was spent before the unit's work could start)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())), holders, pre
	}
	started = b.now()
	xerr := u.Execute(xctx, tx, pre)
	xcancel()
	if xerr != nil {
		// A budget that ran out during the callback is a SPENT BUDGET, not a broken
		// transport. Without this the deadline surfaced as whatever error the canceled
		// statement happened to produce, and a caller reading the sentinel would never see
		// the one condition it can act on.
		if b.expired() && ctx.Err() == nil {
			return fail(phaseExecute, 0, b.now().Sub(started),
				fmt.Errorf("%w: %s (the budget expired while the unit's work was running): %w",
					ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation(), xerr)), holders, pre
		}
		return fail(phaseExecute, 0, b.now().Sub(started), xerr), holders, pre
	}

	// --- Postcondition -----------------------------------------------------
	//
	// Read the object again, INSIDE the lock and BEFORE the receipt. This is the only
	// window where the answer is both current and stable: the locks are held, so
	// nothing can change it, and the receipt has not yet claimed anything about it.
	//
	// Without it the manifest's declared poststate governed only reconciliation — a
	// path that runs after a failure, and therefore never on the run that succeeds. A
	// unit could leave the object in any state at all and commit a receipt saying it
	// had done its job.
	{
		// THE BUDGET IS CHECKED BEFORE THE READ, and it gets its own named error.
		//
		// Folding a spent budget into "the poststate could not be read" would report
		// ErrMigrationPostconditionFailed — "the object is not in its declared state" —
		// for a unit whose object nobody looked at. The two are opposite diagnoses: one
		// says the work is wrong, the other says the clock ran out. An operator acting on
		// the first would go looking for a corrupted object that is perfectly fine.
		if b.expired() {
			return fail(phaseExecute, execArmed, 0,
				fmt.Errorf("%w: %s (the work is done but the budget was spent before its poststate could be verified, so it is being rolled back)",
					ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())), holders, pre
		}
		// Same ceiling as the other two post-lock callbacks, rather than the raw
		// remainder: ProjectObject is caller-supplied like Execute and Receipt, and a
		// bound that differs between them for no stated reason is a bound nobody can
		// reason about.
		poctx, pocancel, pok := b.unitWorkContext(ctx)
		if !pok {
			return fail(phaseExecute, execArmed, 0,
				fmt.Errorf("%w: %s (the work is done but the budget was spent before its poststate could be verified)",
					ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())), holders, pre
		}
		started = b.now()
		post, perr := u.ProjectObject(poctx, tx)
		pocancel()
		if perr != nil {
			// WHICH FAILURE THIS IS depends on whether the clock ran out, and the
			// pre-check above only catches a budget that was ALREADY spent. A budget that
			// expires DURING the reading cancels the context, and reporting that as
			// ErrMigrationPostconditionFailed says "the object is not in its declared
			// state" about an object nobody managed to look at. The two diagnoses point an
			// operator in opposite directions: one sends them hunting for a corrupted
			// object that is perfectly fine.
			if b.expired() && ctx.Err() == nil {
				return fail(phaseExecute, 0, b.now().Sub(started),
					fmt.Errorf("%w: %s (the budget expired while its poststate was being read, so the work is being rolled back unverified): %w",
						ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation(), perr)), holders, pre
			}
			// Fail closed: an unverifiable postcondition is not a satisfied one.
			//
			// ARMED IS ZERO: ProjectObject is a caller-supplied callback, and it may issue
			// any number of statements. statement_timeout restarts for every one of them,
			// so an Elapsed that wraps the whole callback proves nothing about any single
			// statement — the same reason Execute reports zero. Claiming execArmed here
			// would let a 57014 raised in the callback's second query inherit the first
			// query's time and be filed as this runner's own timeout.
			return fail(phaseExecute, 0, b.now().Sub(started),
				fmt.Errorf("%w: %s (the poststate could not be read): %w",
					ErrMigrationPostconditionFailed, u.Plan.Target.displayRelation(), perr)), holders, pre
		}
		post.Readable = true
		if !post.satisfies(u.Spec, pre) {
			want, _ := expectedEnableState(u.Spec, pre)
			return fail(phaseExecute, 0, b.now().Sub(started),
				fmt.Errorf("%w: %s under intent %q wanted guard state %q, found exists=%v guard=%v canonical=%v state=%q",
					ErrMigrationPostconditionFailed, u.Plan.Target.displayRelation(), u.Spec.Intent,
					want, post.Exists, post.GuardPresent, post.MatchesCanonical, post.GuardEnableState)), holders, pre
		}
	}

	// --- Receipt -----------------------------------------------------------
	//
	// Same context treatment as Execute, and for the same reason: it is a caller-supplied
	// callback, and handing it the caller's deadline-free context made the unit's budget
	// unenforceable over the last thing it does before committing.
	rctx, rcancel, ok := b.unitWorkContext(ctx)
	if !ok {
		return fail(phaseReceipt, 0, 0, fmt.Errorf("%w: %s (the work is done and verified, but the budget was spent before its receipt could be written)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())), holders, pre
	}
	started = b.now()
	rerr := u.Receipt(rctx, tx, pre)
	rcancel()
	if rerr != nil {
		if b.expired() && ctx.Err() == nil {
			return fail(phaseReceipt, 0, b.now().Sub(started),
				fmt.Errorf("%w: %s (the budget expired while the receipt was being written): %w",
					ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation(), rerr)), holders, pre
		}
		return fail(phaseReceipt, 0, b.now().Sub(started), rerr), holders, pre
	}

	// The footprint is checked HERE, after all the work and before the commit,
	// because it is the last moment the question can be answered: relation locks are
	// released at commit, so this is the only window in which "what did this
	// transaction actually lock" still has an answer — and the point is to refuse,
	// not to report afterwards.
	started = b.now()
	fpctx, fpcancel := b.context(ctx)
	fperr := verifyLockFootprint(fpctx, tx, u.Plan)
	fpcancel()
	if fperr != nil {
		// execArmed, not the nominal exec: this is ONE statement this runner issued and
		// governs, so the timeout the server actually received is the value a 57014 has
		// to be judged against.
		return fail(phaseReceipt, execArmed, b.now().Sub(started),
			u.budgetFailure(ctx, b, "verifying the lock footprint before the commit", fperr)), holders, pre
	}

	// --- Commit ------------------------------------------------------------
	//
	// Its own phase, because it is the only statement whose failure does not say
	// whether it failed: the server may have applied it and lost the answer on the
	// way home. Everything downstream of this point routes to reconciliation rather
	// than to a retry.
	// ARMED IS ZERO AT THE COMMIT: NO TIMEOUT CAN BE SAFELY ATTRIBUTED HERE.
	//
	// Note the claim, which is narrower than "nothing is armed" and narrower than an
	// earlier version of this comment. PostgreSQL ENABLES statement_timeout in
	// start_xact_command, runs the statement through PortalRun (which hands a utility
	// command to ProcessUtility, yielding first to any loadable hook), and only then
	// enters finish_xact_command, where it calls disable_statement_timeout() before
	// CommitTransactionCommand (src/backend/tcop/postgres.c and utility.c, REL_15_STABLE).
	//
	// So the timeout IS live over the early part of the command and cannot govern the
	// end-of-transaction work. Measured against a real server with a DEFERRABLE INITIALLY
	// DEFERRED constraint trigger: 250ms armed, the commit's deferred work ran 2.002s,
	// raised no 57014, and the row was durable.
	//
	// Reporting execArmed here therefore claimed a ceiling over a span it does not cover.
	// Measured consequence: an external cancellation at 730ms against Armed=800ms came
	// back 57014 at 737.774652ms and classified as cancelOwnTimeout -> retryNewTransaction
	// — the runner retrying a human's intervention instead of reconciling the commit
	// boundary.
	//
	// Zero costs precision in the other direction, and that is accepted deliberately: a
	// 57014 raised during parse, PortalRun or a hook really could have been our own, and
	// it now falls to cancelUnknown, which at this phase routes to reconciliation.
	// Deciding which side of disable_statement_timeout a cancellation landed on is not
	// answerable from the client, and between "ask the database" and "retry over a human
	// who is intervening", the boundary requires the first.
	started = b.now()
	if err := commitTx(tx); err != nil {
		return fail(phaseCommit, 0, b.now().Sub(started), err), holders, pre
	}
	committed = true
	return unitFailure{Phase: phaseCommit}, holders, pre
}

// armedOrInForce reports the ceiling a failed arming attempt ran UNDER.
//
// setLocalTimeouts reports zero when it fails, which is true of the arm it was attempting
// and false of the transaction: `SET LOCAL` from the previous successful arm is still in
// force and is what would have canceled the statement. Reporting zero there sent a
// self-inflicted 57014 to cancelUnknown and then to retryNever — a permanent block over a
// timeout this runner set. Zero survives only when nothing has been armed yet, where it is
// the honest answer.
func armedOrInForce(armed, inForce time.Duration) time.Duration {
	if armed > 0 {
		return armed
	}
	return inForce
}

// armAcquisition sets the timeouts for ONE acquisition statement, recomputed from
// what the budget has left at the moment the statement is about to be issued.
//
// It refuses rather than proceeding when nothing is left, and that refusal is the
// point. PostgreSQL reads lock_timeout = 0 as DISABLED, so a spent budget rendered
// naively as zero would remove the ceiling at precisely the moment the deadline was
// supposed to stop the unit — turning the last statement of an exhausted budget into
// the only unbounded one.
func (u retryUnit) armAcquisition(ctx context.Context, tx *sql.Tx, b *lockBudget) (time.Duration, error) {
	d, ok := b.clampPositive(unitLockTimeout)
	if !ok {
		return 0, fmt.Errorf("%w: %s (no budget left to arm an acquisition)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.relation())
	}
	// Arming is TWO roundtrips of its own, and they were unbounded: the duration just
	// computed could be entirely spent setting the knobs that were supposed to
	// enforce it, and the lock statement then went out with no barrier left.
	//
	// THE TWO CEILINGS ARE NOT THE SAME NUMBER, and this line used to arm them equal —
	// the exact defect this repository already measured and fixed one layer up, in the
	// rollout's close (see armGuardCloseAcquisition and guardCloseAcquisitionStatementSlack:
	// "setLocalTimeouts(actx, tx, d, d) -> ERROR: canceling statement due to statement
	// timeout (SQLSTATE 57014), 0 retries"). statement_timeout runs from the start of the
	// statement and lock_timeout from the moment it begins WAITING, microseconds later, so
	// armed equal the statement ceiling always wins and a LOCK WAIT arrives as 57014 rather
	// than the retryable 55P03. Downstream that is the difference between retryNewTransaction
	// and a permanent block: 57014 in phaseAcquire reaches classifyCancel, and a rollout
	// blocked there has no repair path in this edition.
	//
	// The slack is the close's constant deliberately, not a second one with the same value:
	// two constants that must agree with nothing checking that they do is how a fix ends up
	// applied in one place and not the other, which is precisely what happened here.
	actx, cancel := b.context(ctx)
	effective, err := setLocalTimeouts(actx, tx, d, d+guardCloseAcquisitionStatementSlack)
	cancel()
	if err != nil {
		return effective, u.budgetFailure(ctx, b, "arming an acquisition statement's timeouts", err)
	}
	// Re-check AFTER arming. The value handed to the server was computed before two
	// network roundtrips; if those consumed the remainder, the statement it governs
	// must not be issued at all.
	if b.expired() {
		return effective, fmt.Errorf("%w: %s (the budget was spent while arming the statement timeouts)",
			ErrMigrationLockBudgetExceeded, u.Plan.Target.displayRelation())
	}
	return effective, nil
}

// setLocalTimeouts sets the two session knobs for the current transaction only.
//
// SET LOCAL, never SET: this runs on the connection that holds the coordination
// lock and will be handed back afterwards, and a lock_timeout left behind would be
// inherited by whatever runs next on it. A zero value disables the knob, which is
// how acquisition hands the floor to execution.
func setLocalTimeouts(ctx context.Context, tx *sql.Tx, lock, statement time.Duration) (time.Duration, error) {
	if _, err := tx.ExecContext(ctx,
		"SELECT pg_catalog.set_config('lock_timeout', $1, true)", millisText(lock)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT pg_catalog.set_config('statement_timeout', $1, true)", millisText(statement)); err != nil {
		// ZERO WHEN THE KNOB DID NOT TAKE, rather than the value that was attempted.
		//
		// Returning the nominal duration alongside the error said "a statement_timeout
		// of this length is armed" about a server that never received it. The classifier
		// reads exactly that field to decide whether a 57014 was its own doing, so a
		// failed SET on a connection that then gets canceled from outside would have
		// been filed as this runner's own timeout — and retried — on the strength of a
		// timeout that was never set. Zero means "armed nothing", which is the truth.
		return 0, err
	}
	// The EFFECTIVE value, not the nominal one. millisText quantises anything
	// sub-millisecond up to 1ms, so a nominal 400µs reaches PostgreSQL as 1ms — and
	// classifying against the nominal value declared a cancellation to be "our own
	// timeout" 800µs before the timeout actually sent could possibly expire.
	return effectiveMillis(statement), nil
}

// effectiveMillis is the duration PostgreSQL actually received, after millisText.
func effectiveMillis(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if ms := d.Milliseconds(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return time.Millisecond
}

// millisText renders a duration the way PostgreSQL's timeout GUCs expect, with
// zero meaning "no limit".
func millisText(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	ms := d.Milliseconds()
	if ms == 0 {
		// Sub-millisecond budgets round to zero, and zero means UNLIMITED here —
		// the exact opposite of what a nearly-spent budget intends. One millisecond
		// is the smallest honest expression of "almost none left".
		ms = 1
	}
	return fmt.Sprintf("%dms", ms)
}

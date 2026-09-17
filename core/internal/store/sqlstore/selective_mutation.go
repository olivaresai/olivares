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
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// MutateCoordination runs a business-tenant transaction under L0 and the exact
// canonical key set, without L1 or lineage-writer enrollment. Planned keys are
// acquired before any decorator or user callback runs.
func (s *sqlStore) MutateCoordination(
	ctx context.Context,
	tenant model.TenantID,
	plan store.TransactionLockPlan,
	fn func(store.CoordinationMutationScope) error,
) error {
	keys := plan.TransactionKeys()
	if len(keys) == 0 {
		return fmt.Errorf("%w: transaction key set is empty", store.ErrSelectiveMutationPlan)
	}
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: transaction key is empty", store.ErrSelectiveMutationPlan)
		}
	}
	return s.mutateSelective(ctx, tenant, func(
		sc *tenantScope, state *selectiveMutationState, admit func() error,
	) error {
		// The planned keys are still waited for on the REQUEST context: a caller
		// that gives up while queued behind another holder must be released.
		for _, key := range keys {
			if err := sc.LockTransaction(ctx, key); err != nil {
				return err
			}
		}
		// Everything below this line is public: the decorator policy check the
		// wrappers fold in, and then the caller's own callback.
		if err := admit(); err != nil {
			return err
		}
		return fn(&coordinationMutationScope{
			neutralMutationScope: neutralMutationScope{sc: sc, state: state},
		})
	})
}

// MutateEvidenceOperation runs one plan-confined evidence journal transaction
// under L0 without L1 or lineage-writer enrollment. Constructing the repository
// performs no SQL; decorators inspect Org before invoking the user callback.
func (s *sqlStore) MutateEvidenceOperation(
	ctx context.Context,
	tenant model.TenantID,
	plan store.EvidenceOperationPlan,
	fn func(store.EvidenceOperationMutationScope) error,
) error {
	operationID := plan.OperationID()
	if operationID == "" {
		return fmt.Errorf("%w: evidence operation ID is empty", store.ErrSelectiveMutationPlan)
	}
	return s.mutateSelective(ctx, tenant, func(
		sc *tenantScope, state *selectiveMutationState, admit func() error,
	) error {
		repo := &plannedEvidenceOperationRepo{
			inner: sc.EvidenceOperations(), operationID: operationID, state: state,
		}
		if err := admit(); err != nil {
			return err
		}
		return fn(&evidenceOperationMutationScope{
			neutralMutationScope: neutralMutationScope{sc: sc, state: state},
			repo:                 repo,
		})
	})
}

// selectiveAdmissionTenant validates the caller's tenant identity BEFORE any SQL
// runs. It reuses canonicalDirectoryTenants — the exact UUIDv7/RFC4122/byte-equal
// seam legacy lineage arming already applies — rather than a second, softer copy:
// a noncanonical string is REFUSED, never normalized into an accepted identity.
// The explicit zero and reserved-SYSTEM refusals keep their own errors, because a
// caller that hands the envelope SYSTEM has a compatibility path (Store.Mutate)
// and needs to be told so.
func selectiveAdmissionTenant(tenant model.TenantID) error {
	if tenant.IsZero() {
		return store.ErrNoTenant
	}
	if tenant.IsSystem() {
		return fmt.Errorf("%w: reserved SYSTEM tenant requires Store.Mutate", store.ErrSelectiveMutationPlan)
	}
	if _, err := canonicalDirectoryTenants([]model.TenantID{tenant}); err != nil {
		return fmt.Errorf("selective mutation admission: %w", err)
	}
	return nil
}

// selectiveRequestCanceled reports the REQUEST context's own cancellation cause,
// or nil while the request is still live. The envelope owns its transaction
// context, so this is the only remaining reason to refuse a caller that has
// walked away, and it names the cause rather than an internal context error.
func selectiveRequestCanceled(ctx context.Context, where string) error {
	cause := context.Cause(ctx)
	if cause == nil {
		return nil
	}
	return fmt.Errorf("selective mutation refused %s: %w", where, cause)
}

// mutateSelective owns the common narrow transaction envelope. It deliberately
// does not construct the directory or lineage trackers used by Mutate. L0 keeps
// System lifecycle mutation out while the narrow callback is live.
//
// TRANSACTION LIFETIME. database/sql derives a transaction's own lifetime from
// the context handed to BeginTx and rolls that transaction back AUTOMATICALLY
// when the context is canceled (sql.go awaitDone). Handing it the request
// context would therefore drop L0 and every planned key the moment the caller
// walked away — while the guarded callback was still executing, which is exactly
// the exclusion this envelope promises. The envelope owns a detached context
// instead and relays request cancellation into it only until the caller is
// admitted, so acquisition stays responsive and an admitted callback keeps its
// locks until the envelope itself decides the outcome.
//
// LIMITS, stated because they are real. Owning the context cannot retain locks
// after the CONNECTION or the backend is actually lost: pgx retires a connection
// whose I/O failed, and a terminated backend releases everything it held. Nor is
// it an atomicity claim over external effects: a callback that emitted one is
// still responsible for its own compensation, and the durable evidence/leadership
// fence before dispatch is unchanged. Finally, a callback that never returns
// cannot both retain its locks and release its resources promptly — the envelope
// keeps the promise it can keep and the callback owes it cooperation: stop on a
// failed scope operation and return.
func (s *sqlStore) mutateSelective(
	ctx context.Context,
	tenant model.TenantID,
	fn func(*tenantScope, *selectiveMutationState, func() error) error,
) (retErr error) {
	if err := selectiveAdmissionTenant(tenant); err != nil {
		return err
	}
	if !s.elector.active() {
		return store.ErrNotLeader
	}

	relayHook := s.selectiveRelayTestHook
	ownedCtx, cancelOwned := context.WithCancel(context.WithoutCancel(ctx))
	relay := newSelectiveCancellationRelay(ctx, cancelOwned, relayHook)
	// Both cleanups are registered BEFORE the checked rollback below, so both run
	// AFTER it: the transaction's actual rollback must precede cancellation of its
	// owner, or database/sql's automatic rollback would race the one whose result
	// this envelope reports. The unconditional stop also covers the paths that
	// never reach that rollback defer, and the committed path, which returns from
	// it early.
	defer relay.stop()
	defer cancelOwned()

	tx, err := s.db.BeginTx(ownedCtx, directoryWriterTxOptions(s.dia))
	if err != nil {
		if canceled := selectiveRequestCanceled(ctx, "during transaction acquisition"); canceled != nil {
			return canceled
		}
		return wrapUnavailableErr(err)
	}
	committed := false
	defer func() {
		// Stop the relay first: no later request cancellation may reach the owner
		// while the checked rollback below is establishing what actually happened.
		relay.stop()
		if committed {
			return
		}
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			retErr = errors.Join(retErr, fmt.Errorf(
				"selective mutation rollback could not be confirmed: %w",
				wrapUnavailableErr(rollbackErr),
			))
		}
	}()
	if err := bindDirectoryTenant(ctx, tx, s.dia, tenant); err != nil {
		return wrapUnavailableErr(err)
	}
	if err := lockLineageWriterClass(ctx, tx, s.dia, false); err != nil {
		return wrapUnavailableErr(err)
	}
	sc := &tenantScope{s: s, tx: tx, tenant: tenant}
	// Existing-organization evidence, read under the shared L0 this envelope now
	// holds and through the tenant binding installed above. This is EXISTENCE,
	// not policy: whether an existing org is resident here or in service stays
	// with the registry-bearing wrappers, which run after admission.
	if _, err := sc.Org(ctx); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf(
				"%w: selective mutation: tenant %s has no organization on this instance",
				store.ErrNotFound, tenant,
			)
		}
		return wrapUnavailableErr(err)
	}
	state := &selectiveMutationState{}

	admitted := false
	admit := func() error {
		if admitted {
			return errors.New("sqlstore: selective mutation admitted its callback twice")
		}
		if relayHook != nil {
			relayHook(selectiveRelayBeforeStop)
		}
		if relay.stop() {
			// The relay ran AND its cancellation has been joined to completion, so
			// the transaction owner is already canceled. Refuse before public work.
			if ownedCtx.Err() == nil {
				return errors.New(
					"sqlstore: selective mutation relay reported cancellation without canceling the transaction owner")
			}
			if relayHook != nil {
				relayHook(selectiveRelayRefusedAfterCancellation)
			}
			return selectiveRequestCanceled(ctx, "during admission")
		}
		// The relay is provably inert from here on, so a request canceled in the
		// instant after it was stopped can no longer reach the transaction owner:
		// this recheck is the only thing that can still refuse it.
		if err := selectiveRequestCanceled(ctx, "at the callback boundary"); err != nil {
			return err
		}
		if err := ownedCtx.Err(); err != nil {
			return fmt.Errorf("selective mutation refused at the callback boundary: %w", err)
		}
		admitted = true
		return nil
	}

	if err := fn(sc, state, admit); err != nil {
		if s.selectiveMutationTestHook != nil {
			if hookErr := s.selectiveMutationTestHook(ctx, tx, selectiveMutationAfterCallbackError); hookErr != nil {
				return errors.Join(wrapUnavailableErr(err), wrapUnavailableErr(hookErr))
			}
		}
		return wrapUnavailableErr(err)
	}
	if state.poisoned != nil {
		return wrapUnavailableErr(fmt.Errorf(
			"selective transaction poisoned by discarded scope error: %w",
			state.poisoned,
		))
	}
	// Owning the transaction context detaches it from the caller's cancellation;
	// it does not hand the caller authority to commit under one. A callback that
	// ignored its request context and returned nil must not become a commit.
	if err := selectiveRequestCanceled(ctx, "before commit"); err != nil {
		return err
	}
	if s.selectiveMutationTestHook != nil {
		if err := s.selectiveMutationTestHook(ctx, tx, selectiveMutationBeforeCommit); err != nil {
			return wrapUnavailableErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		// Commit failure remains ambiguous. database/sql marks the Tx done, so the
		// deferred ErrTxDone rollback is ignored rather than claiming rollback.
		return wrapUnavailableErr(err)
	}
	committed = true
	return nil
}

// selectiveRelayStage names the relay orderings that are otherwise unobservable
// from outside this package. The hook that receives them is private, nil in
// production, and set only by this package's own tests.
type selectiveRelayStage string

const (
	// selectiveRelayStarted fires inside the relay goroutine, BEFORE it cancels
	// the transaction owner.
	selectiveRelayStarted selectiveRelayStage = "relay_started"
	// selectiveRelayBeforeStop fires in the envelope goroutine immediately before
	// it tries to stop the relay.
	selectiveRelayBeforeStop selectiveRelayStage = "before_stop"
	// selectiveRelayJoining fires in the envelope goroutine when stopping LOST to
	// a relay that had already started, immediately before joining it.
	selectiveRelayJoining selectiveRelayStage = "joining_relay"
	// selectiveRelayCanceledOwner fires inside the relay goroutine after it has
	// canceled the transaction owner.
	selectiveRelayCanceledOwner selectiveRelayStage = "relay_canceled_owner"
	// selectiveRelayRefusedAfterCancellation fires in the envelope goroutine at
	// the refusal that follows a joined relay.
	selectiveRelayRefusedAfterCancellation selectiveRelayStage = "refused_after_cancellation"
)

// selectiveCancellationRelay bridges request cancellation into the envelope's
// owned transaction context, and only for as long as the envelope has not yet
// admitted the caller. Before admission it keeps connection acquisition, planned
// key waits and admission responsive to the request; once stopped, no later
// request cancellation can reach database/sql's automatic rollback while the
// guarded callback is live.
type selectiveCancellationRelay struct {
	stopRelay func() bool
	relayed   chan struct{}
	hook      func(selectiveRelayStage)
	once      sync.Once
	fired     bool
}

func newSelectiveCancellationRelay(
	requestCtx context.Context,
	cancelOwned context.CancelFunc,
	hook func(selectiveRelayStage),
) *selectiveCancellationRelay {
	r := &selectiveCancellationRelay{relayed: make(chan struct{}), hook: hook}
	r.stopRelay = context.AfterFunc(requestCtx, func() {
		defer close(r.relayed)
		if hook != nil {
			hook(selectiveRelayStarted)
		}
		cancelOwned()
		if hook != nil {
			hook(selectiveRelayCanceledOwner)
		}
	})
	return r
}

// stop deregisters the relay and reports whether it had already started.
//
// A false result from context.AfterFunc's stop function does NOT mean the relay
// finished cancelling — it means the relay was ALREADY RUNNING and may not have
// reached its cancel call yet. stop therefore joins the relay's completion before
// reporting, so a caller that refuses on a true result knows the owned context is
// already canceled rather than about to be.
//
// Only the FIRST call consults the stop function, and that is load-bearing: after
// an earlier successful stop the relay will never run, so a second call taking
// the join branch would wait forever on a channel nothing will ever close.
func (r *selectiveCancellationRelay) stop() bool {
	r.once.Do(func() {
		if r.stopRelay() {
			return
		}
		if r.hook != nil {
			r.hook(selectiveRelayJoining)
		}
		<-r.relayed
		r.fired = true
	})
	return r.fired
}

type selectiveMutationTestStage string

const (
	selectiveMutationAfterCallbackError selectiveMutationTestStage = "after_callback_error"
	selectiveMutationBeforeCommit       selectiveMutationTestStage = "before_commit"
)

type selectiveMutationState struct {
	poisoned error
}

func (s *selectiveMutationState) poison(err error) {
	if err != nil && s.poisoned == nil {
		s.poisoned = err
	}
}

// neutralMutationScope forwards only the two methods in
// store.NeutralMutationScope. Holding a *tenantScope in a named field rather
// than embedding it prevents dynamic assertion back to store.Scope.
type neutralMutationScope struct {
	sc    *tenantScope
	state *selectiveMutationState
}

func (s *neutralMutationScope) Tenant() model.TenantID { return s.sc.Tenant() }

func (s *neutralMutationScope) Org(ctx context.Context) (model.Org, error) {
	org, err := s.sc.Org(ctx)
	s.state.poison(err)
	return org, err
}

type coordinationMutationScope struct {
	neutralMutationScope
}

func (s *coordinationMutationScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	now, err := s.sc.TransactionNow(ctx)
	s.state.poison(err)
	return now, err
}

type evidenceOperationMutationScope struct {
	neutralMutationScope
	repo store.EvidenceOperationRepo
}

func (s *evidenceOperationMutationScope) EvidenceOperations() store.EvidenceOperationRepo {
	return s.repo
}

type plannedEvidenceOperationRepo struct {
	inner       store.EvidenceOperationRepo
	operationID string
	state       *selectiveMutationState
}

func (r *plannedEvidenceOperationRepo) check(operationID string) error {
	if operationID == r.operationID {
		return nil
	}
	err := fmt.Errorf(
		"%w: evidence operation %q is outside plan %q",
		store.ErrSelectiveMutationPlan, operationID, r.operationID,
	)
	r.state.poison(err)
	return err
}

func (r *plannedEvidenceOperationRepo) Get(
	ctx context.Context,
	operationID string,
) (model.EvidenceOperation, error) {
	if err := r.check(operationID); err != nil {
		return model.EvidenceOperation{}, err
	}
	op, err := r.inner.Get(ctx, operationID)
	r.state.poison(err)
	return op, err
}

func (r *plannedEvidenceOperationRepo) Claim(
	ctx context.Context,
	claim store.EvidenceClaim,
) (store.EvidenceClaimResult, error) {
	if err := r.check(claim.OperationID); err != nil {
		return store.EvidenceClaimResult{}, err
	}
	result, err := r.inner.Claim(ctx, claim)
	r.state.poison(err)
	return result, err
}

func (r *plannedEvidenceOperationRepo) Settle(
	ctx context.Context,
	settlement store.EvidenceSettlement,
) (store.EvidenceSettleResult, error) {
	if err := r.check(settlement.OperationID); err != nil {
		return store.EvidenceSettleResult{}, err
	}
	result, err := r.inner.Settle(ctx, settlement)
	r.state.poison(err)
	return result, err
}

var _ store.SelectiveMutator = (*sqlStore)(nil)
var _ store.CoordinationMutationScope = (*coordinationMutationScope)(nil)
var _ store.EvidenceOperationMutationScope = (*evidenceOperationMutationScope)(nil)

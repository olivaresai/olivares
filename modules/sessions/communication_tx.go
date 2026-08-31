// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errCommunicationTransactionUnavailable is the stable deny-closed sentinel
// for a missing WP2 transaction capability. It also unwraps to the public
// communication evidence sentinel so later service/API classification cannot
// mistake an incomplete store seam for a business denial.
var errCommunicationTransactionUnavailable = fmt.Errorf(
	"%w: communication transaction infrastructure unavailable",
	ErrCommunicationEvidenceUnknown,
)

// errCommunicationAuthoritySnapshotAlreadyAttempted prevents a caller from
// splitting one authorization decision across multiple independently locked
// fact sets. Whether the first attempt succeeded or failed, it is the only
// attempt permitted on this transaction helper.
var errCommunicationAuthoritySnapshotAlreadyAttempted = errors.New(
	"sessions: communication authority snapshot already attempted",
)

type communicationBoundAuthorityPhase uint8

const (
	communicationBoundAuthorityAwaitingSnapshot communicationBoundAuthorityPhase = iota + 1
	communicationBoundAuthorityLockingLocal
	communicationBoundAuthorityRefreshing
	communicationBoundAuthorityEffects
	communicationBoundAuthorityFinalizing
	communicationBoundAuthorityDone
	communicationBoundAuthorityFailed
)

// communicationBoundAuthorityState makes the ordering of a request- or
// Claim-bound mutation structural rather than a convention: the complete authority
// snapshot is locked first, all remaining wait-capable locks finish next, the
// DB clock is refreshed once, and only then may effects be written. It is
// shared by every retained closure/adapter so direct package access cannot
// bypass the phase checks.
type communicationBoundAuthorityState struct {
	mu           sync.Mutex
	phase        communicationBoundAuthorityPhase
	activeLock   bool
	activeEffect bool
}

func newCommunicationBoundAuthorityState() *communicationBoundAuthorityState {
	return &communicationBoundAuthorityState{
		phase: communicationBoundAuthorityAwaitingSnapshot,
	}
}

func (s *communicationBoundAuthorityState) beginAuthorityLock() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != communicationBoundAuthorityAwaitingSnapshot || s.activeLock {
		return communicationTransactionUnavailable("request authority snapshot phase", nil)
	}
	s.activeLock = true
	return nil
}

func (s *communicationBoundAuthorityState) finishAuthorityLock(succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeLock = false
	if succeeded {
		s.phase = communicationBoundAuthorityLockingLocal
	} else {
		s.phase = communicationBoundAuthorityFailed
	}
}

func (s *communicationBoundAuthorityState) beginObservation() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.phase != communicationBoundAuthorityAwaitingSnapshot &&
		s.phase != communicationBoundAuthorityLockingLocal) || s.activeLock || s.activeEffect {
		return communicationTransactionUnavailable("bound authority observation phase", nil)
	}
	s.activeLock = true
	return nil
}

func (s *communicationBoundAuthorityState) finishObservation(completed bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeLock = false
	if !completed {
		s.phase = communicationBoundAuthorityFailed
	}
}

func (s *communicationBoundAuthorityState) beginLocalLock() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != communicationBoundAuthorityLockingLocal || s.activeLock {
		return communicationTransactionUnavailable("request authority local-lock phase", nil)
	}
	s.activeLock = true
	return nil
}

func (s *communicationBoundAuthorityState) finishLocalLock(succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeLock = false
	if !succeeded {
		s.phase = communicationBoundAuthorityFailed
	}
}

func (s *communicationBoundAuthorityState) beginRefresh() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != communicationBoundAuthorityLockingLocal || s.activeLock {
		return communicationTransactionUnavailable("request authority refresh phase", nil)
	}
	s.phase = communicationBoundAuthorityRefreshing
	return nil
}

func (s *communicationBoundAuthorityState) finishRefresh(succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if succeeded {
		s.phase = communicationBoundAuthorityEffects
	} else {
		s.phase = communicationBoundAuthorityFailed
	}
}

func (s *communicationBoundAuthorityState) beginEffect() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != communicationBoundAuthorityEffects || s.activeLock || s.activeEffect {
		return communicationTransactionUnavailable("request authority effect phase", nil)
	}
	s.activeEffect = true
	return nil
}

func (s *communicationBoundAuthorityState) finishEffect(succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeEffect = false
	if !succeeded {
		s.phase = communicationBoundAuthorityFailed
	}
}

func (s *communicationBoundAuthorityState) beginFinalize() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != communicationBoundAuthorityEffects || s.activeLock || s.activeEffect {
		return communicationTransactionUnavailable("request authority finalization phase", nil)
	}
	s.phase = communicationBoundAuthorityFinalizing
	return nil
}

func (s *communicationBoundAuthorityState) finishFinalize(succeeded bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if succeeded {
		s.phase = communicationBoundAuthorityDone
	} else {
		s.phase = communicationBoundAuthorityFailed
	}
}

func runCommunicationBoundAuthorityLocalLock(
	state *communicationBoundAuthorityState,
	fn func() error,
) error {
	if state == nil {
		return fn()
	}
	if err := state.beginLocalLock(); err != nil {
		return err
	}
	succeeded := false
	defer func() { state.finishLocalLock(succeeded) }()
	err := fn()
	succeeded = err == nil
	return err
}

func runCommunicationBoundAuthorityObservation[T any](
	state *communicationBoundAuthorityState,
	fn func() (T, error),
) (T, error) {
	if state == nil {
		return fn()
	}
	if err := state.beginObservation(); err != nil {
		var zero T
		return zero, err
	}
	completed := false
	defer func() { state.finishObservation(completed) }()
	value, err := fn()
	completed = true
	return value, err
}

func runCommunicationBoundAuthorityEffect[T any](
	state *communicationBoundAuthorityState,
	fn func() (T, error),
) (T, error) {
	if state == nil {
		return fn()
	}
	if err := state.beginEffect(); err != nil {
		var zero T
		return zero, err
	}
	succeeded := false
	defer func() { state.finishEffect(succeeded) }()
	value, err := fn()
	succeeded = err == nil
	return value, err
}

type communicationData interface {
	View(context.Context, func(store.Scope) error) error
	Mutate(context.Context, func(store.Scope) error) error
}

type tenantCommunicationData struct {
	data   api.ModuleData
	tenant model.TenantID
}

func (d tenantCommunicationData) View(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	if sc, ok := protocolReplayScopeFromContext(ctx, d.tenant); ok {
		return fn(sc)
	}
	if d.data == nil {
		return store.ErrStoreUnavailable
	}
	return d.data.View(ctx, d.tenant, fn)
}

func (d tenantCommunicationData) Mutate(
	ctx context.Context,
	fn func(store.Scope) error,
) error {
	if sc, ok := protocolReplayScopeFromContext(ctx, d.tenant); ok {
		return fn(sc)
	}
	if d.data == nil {
		return store.ErrStoreUnavailable
	}
	return d.data.Mutate(ctx, d.tenant, fn)
}

func (m *Module) communicationData(tenant model.TenantID) communicationData {
	return tenantCommunicationData{data: m.data, tenant: tenant}
}

// viewCommunication is the only WP2 entry point for an observational store
// callback. It derives the tenant binding from the validated server-side scope
// and confines the callback before exposing any repository.
func (m *Module) viewCommunication(
	ctx context.Context,
	directoryScope DirectoryScopeRef,
	fn func(store.Scope) error,
) error {
	if err := directoryScope.Validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("view callback", nil)
	}
	return m.communicationData(directoryScope.TenantID).View(ctx, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, directoryScope.WorkspaceID)
		if err != nil {
			return err
		}
		return fn(confined)
	})
}

// mutateCommunication is the only WP2 entry point for a durable unit of work.
// The tenant-bound ModuleData callback is opened once, then workspace confinement
// is applied before newCommunicationTx can observe DB time or expose a repo.
func (m *Module) mutateCommunication(
	ctx context.Context,
	directoryScope DirectoryScopeRef,
	fn func(*communicationTx) error,
) error {
	return m.mutateCommunicationTransaction(
		ctx, directoryScope, communicationRequestAuthoritySnapshot{},
		CommunicationClaimAuthoritySnapshot{}, fn,
	)
}

// CommunicationClaimAuthoritySnapshot is an opaque, request-local token. A
// trusted resolver may construct it from server-observed Claim rows before a
// communication mutation starts; its exact row references and deadlines are
// private and are never accepted from a wire payload.
type CommunicationClaimAuthoritySnapshot struct {
	facts []store.AuthorizationFactRef
}

// NewCommunicationClaimAuthoritySnapshot binds canonical SID/fence
// requirements to exact, versioned Claim witnesses. LockAuthoritySnapshot
// performs the authoritative row checks; this constructor only proves that the
// opaque token and the already-validated communication decision describe the
// same complete set.
func NewCommunicationClaimAuthoritySnapshot(
	claims []CommunicationClaimRef,
	facts []store.AuthorizationFactRef,
) (CommunicationClaimAuthoritySnapshot, error) {
	if len(claims) == 0 || len(claims) != len(facts) || len(claims) > 64 {
		return CommunicationClaimAuthoritySnapshot{}, communicationError(
			ErrInvalidCommunicationModel, "invalid Claim authority snapshot cardinality",
		)
	}
	canonicalClaims := append([]CommunicationClaimRef(nil), claims...)
	canonicalFacts := append([]store.AuthorizationFactRef(nil), facts...)
	sort.Slice(canonicalClaims, func(i, j int) bool {
		if canonicalClaims[i].SessionSID != canonicalClaims[j].SessionSID {
			return canonicalClaims[i].SessionSID < canonicalClaims[j].SessionSID
		}
		return canonicalClaims[i].Fence < canonicalClaims[j].Fence
	})
	sort.Slice(canonicalFacts, func(i, j int) bool {
		leftSID, leftFence, _, _ := canonicalFacts[i].LeaseFenceWitness()
		rightSID, rightFence, _, _ := canonicalFacts[j].LeaseFenceWitness()
		if leftSID != rightSID {
			return leftSID < rightSID
		}
		if leftFence != rightFence {
			return leftFence < rightFence
		}
		return canonicalFacts[i].ID.String() < canonicalFacts[j].ID.String()
	})
	for i := range canonicalClaims {
		claim := canonicalClaims[i]
		fact := canonicalFacts[i]
		sid, fence, _, ok := fact.LeaseFenceWitness()
		if !validCanonicalCommunicationSID(claim.SessionSID) || claim.Fence < 1 ||
			fact.Kind != claimKind || fact.ID.IsZero() || fact.Version < 1 || !ok ||
			sid != claim.SessionSID || fence != claim.Fence ||
			(i > 0 && canonicalClaims[i-1].SessionSID == claim.SessionSID) {
			return CommunicationClaimAuthoritySnapshot{}, communicationError(
				ErrInvalidCommunicationModel, "invalid Claim authority snapshot",
			)
		}
	}
	return CommunicationClaimAuthoritySnapshot{facts: canonicalFacts}, nil
}

func (m *Module) mutateCommunicationWithClaimAuthority(
	ctx context.Context,
	directoryScope DirectoryScopeRef,
	claims CommunicationClaimAuthoritySnapshot,
	fn func(*communicationTx) error,
) error {
	return m.mutateCommunicationTransaction(
		ctx, directoryScope, communicationRequestAuthoritySnapshot{}, claims, fn,
	)
}

// mutateCommunicationWithAuthority is the inert, exact-request transaction
// seam for a later service cut. The effect path supplies its independently
// server-derived question; a binding for another entity, operation, tenant or
// workspace is rejected before Mutate opens. Snapshot derivation stays inside
// this wrapper so request evidence never becomes a transferable fact bearer.
func (m *Module) mutateCommunicationWithAuthority(
	ctx context.Context,
	expected communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	claims CommunicationClaimAuthoritySnapshot,
	fn func(*communicationTx, communicationRequestAuthorityContext) error,
) error {
	return m.mutateCommunicationWithAuthorityConstraint(
		ctx, expected, bound, claims, nil, fn,
	)
}

// mutateCommunicationWithNarrowedAuthority is the exact-read seam. Unlike the
// general request wrapper, it requires a finite local-evidence window and
// intersects that interval with the sealed core request before a store Mutate
// can open. The existing transaction freshness checks therefore cover both
// sources after locks and again immediately before commit.
func (m *Module) mutateCommunicationWithNarrowedAuthority(
	ctx context.Context,
	expected communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	claims CommunicationClaimAuthoritySnapshot,
	window communicationAuthorityWindow,
	fn func(*communicationTx, communicationRequestAuthorityContext) error,
) error {
	if err := window.validate(); err != nil {
		return err
	}
	return m.mutateCommunicationWithAuthorityConstraint(
		ctx, expected, bound, claims, &window, fn,
	)
}

func (m *Module) mutateCommunicationWithAuthorityConstraint(
	ctx context.Context,
	expected communicationAuthorityQuestion,
	bound communicationRequestAuthority,
	claims CommunicationClaimAuthoritySnapshot,
	window *communicationAuthorityWindow,
	fn func(*communicationTx, communicationRequestAuthorityContext) error,
) error {
	if err := expected.validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("mutation callback", nil)
	}
	request, boundContext, err := bound.transactionSnapshot(expected, claims)
	if err != nil {
		return err
	}
	if window != nil {
		request, err = request.narrowTo(*window)
		if err != nil {
			return err
		}
	}
	directoryScope := DirectoryScopeRef{
		TenantID: expected.entity.TenantID, WorkspaceID: expected.entity.WorkspaceID,
	}
	return m.mutateCommunicationTransaction(
		ctx, directoryScope, request, claims,
		func(tx *communicationTx) error { return fn(tx, boundContext) },
	)
}

// mutateCommunicationTransaction is the common low-level constructor. The only
// non-zero request snapshot reaching it is derived by the exact wrapper above;
// legacy and Claim-only paths pass the zero value. It retains data only and has
// no resolver/authorizer capable of opening an AuthView inside Mutate.
func (m *Module) mutateCommunicationTransaction(
	ctx context.Context,
	directoryScope DirectoryScopeRef,
	request communicationRequestAuthoritySnapshot,
	claims CommunicationClaimAuthoritySnapshot,
	fn func(*communicationTx) error,
) error {
	if err := directoryScope.Validate(); err != nil {
		return err
	}
	if err := request.validate(); err != nil {
		return err
	}
	if fn == nil {
		return communicationTransactionUnavailable("mutation callback", nil)
	}
	var boundCallbackAttempted atomic.Bool
	return m.communicationData(directoryScope.TenantID).Mutate(ctx, func(sc store.Scope) error {
		if !request.empty() && !boundCallbackAttempted.CompareAndSwap(false, true) {
			return communicationTransactionUnavailable(
				"request-bound mutation callback was already entered", nil,
			)
		}
		confined, err := store.ConfineWorkspace(ctx, sc, directoryScope.WorkspaceID)
		if err != nil {
			return err
		}
		tx, err := newCommunicationTxWithAuthority(ctx, confined, request, claims)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.finalizeAuthority(ctx)
	})
}

// communicationTx is constructed exactly once inside a communication Mutate
// callback. newCommunicationTx takes the initial database-time observation and
// retains only the narrow capability needed to resample after all blocking
// locks. Callers use now rather than consulting a process clock.
type communicationTx struct {
	now model.Timestamp

	observeNowFn              func(context.Context) (model.Timestamp, error)
	lockTransactionFn         func(context.Context, string) error
	lockAuditAppendsFn        func(context.Context) error
	lockAuthoritySnapshotFn   func(context.Context, []store.AuthorizationFactRef) error
	requestAuthorityFacts     []store.AuthorizationFactRef
	requestBindingID          *communicationRequestAuthorityBindingID
	requestObservedAt         time.Time
	requestFreshUntil         time.Time
	claimAuthorityFacts       []store.AuthorizationFactRef
	authoritySnapshotLocked   *atomic.Bool
	authorityFreshnessChecked atomic.Bool
	boundAuthorityState       *communicationBoundAuthorityState
	audit                     communicationAuditAppender
	directory                 store.DirectorySnapshotReader
	resolveRepository         func(model.Kind) (communicationRepository, error)
}

// communicationAuditAppender is the complete audit surface retained by a K3
// transaction. In particular, it omits Verify, Walk, Head and every optional
// AuditLog capability: communication effects may append their evidence in the
// surrounding Mutate, but cannot turn that append handle into a ledger reader.
type communicationAuditAppender interface {
	Append(context.Context, model.AuditDraft) (model.AuditEvent, error)
}

type communicationAuditAppenderAdapter struct {
	appendFn func(context.Context, model.AuditDraft) (model.AuditEvent, error)
}

var _ communicationAuditAppender = (*communicationAuditAppenderAdapter)(nil)

func (a *communicationAuditAppenderAdapter) Append(
	ctx context.Context,
	draft model.AuditDraft,
) (model.AuditEvent, error) {
	return a.appendFn(ctx, draft)
}

// communicationDirectorySnapshotReaderAdapter retains method values rather
// than the original Scope interface. Its dynamic type cannot be asserted back
// to store.Scope, so the narrow evidence reader does not become a handle to
// tenant-wide repositories or a second transaction capability set.
type communicationDirectorySnapshotReaderAdapter struct {
	readEpoch     func(context.Context) (model.DirectoryEpoch, error)
	readTombstone func(
		context.Context,
		store.DirectoryPrincipalRef,
	) (store.DirectoryTombstoneWitness, bool, error)
}

var _ store.DirectorySnapshotReader = (*communicationDirectorySnapshotReaderAdapter)(nil)

func (r *communicationDirectorySnapshotReaderAdapter) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return r.readEpoch(ctx)
}

func (r *communicationDirectorySnapshotReaderAdapter) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return r.readTombstone(ctx, ref)
}

func newCommunicationTx(
	ctx context.Context,
	sc store.Scope,
) (*communicationTx, error) {
	return newCommunicationTxWithAuthority(
		ctx, sc, communicationRequestAuthoritySnapshot{}, CommunicationClaimAuthoritySnapshot{},
	)
}

func newCommunicationTxWithClaimAuthority(
	ctx context.Context,
	sc store.Scope,
	claims CommunicationClaimAuthoritySnapshot,
) (*communicationTx, error) {
	return newCommunicationTxWithAuthority(
		ctx, sc, communicationRequestAuthoritySnapshot{}, claims,
	)
}

func newCommunicationTxWithAuthority(
	ctx context.Context,
	sc store.Scope,
	request communicationRequestAuthoritySnapshot,
	claims CommunicationClaimAuthoritySnapshot,
) (*communicationTx, error) {
	if sc == nil {
		return nil, communicationTransactionUnavailable("scope", nil)
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return nil, communicationTransactionUnavailable("transaction clock", nil)
	}
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return nil, communicationTransactionUnavailable("transaction locker", nil)
	}
	authority, ok := sc.(store.AuthoritySnapshotLocker)
	if !ok {
		return nil, communicationTransactionUnavailable("authority snapshot locker", nil)
	}
	directory, ok := sc.(store.DirectorySnapshotReader)
	if !ok {
		return nil, communicationTransactionUnavailable("directory snapshot reader", nil)
	}
	audit := sc.Audit()
	if audit == nil {
		return nil, communicationTransactionUnavailable("audit append", nil)
	}
	auditLocker, ok := audit.(store.AuditAppendLocker)
	if !ok {
		return nil, communicationTransactionUnavailable("audit append lock", nil)
	}

	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return nil, communicationTransactionUnavailable("transaction clock", err)
	}
	if now.IsZero() {
		return nil, communicationTransactionUnavailable("transaction clock returned zero", nil)
	}
	// Keep the one-shot state inside the retained closure, not on communicationTx.
	// Code elsewhere in this package may call the field directly, but it still
	// cannot reach the raw authority method or reset the attempt after a failure.
	var authoritySnapshotAttempted atomic.Bool
	authoritySnapshotSucceeded := &atomic.Bool{}
	requestFacts := append([]store.AuthorizationFactRef(nil), request.facts...)
	claimFacts := append([]store.AuthorizationFactRef(nil), claims.facts...)
	var boundState *communicationBoundAuthorityState
	if len(requestFacts) != 0 || len(claimFacts) != 0 {
		boundState = newCommunicationBoundAuthorityState()
	}
	directoryAdapter := &communicationDirectorySnapshotReaderAdapter{
		readEpoch: func(ctx context.Context) (model.DirectoryEpoch, error) {
			return runCommunicationBoundAuthorityObservation(boundState, func() (model.DirectoryEpoch, error) {
				return directory.ReadDirectoryEpoch(ctx)
			})
		},
		readTombstone: func(
			ctx context.Context,
			ref store.DirectoryPrincipalRef,
		) (store.DirectoryTombstoneWitness, bool, error) {
			type result struct {
				witness store.DirectoryTombstoneWitness
				found   bool
			}
			value, err := runCommunicationBoundAuthorityObservation(boundState, func() (result, error) {
				witness, found, err := directory.ReadDirectoryTombstone(ctx, ref)
				return result{witness: witness, found: found}, err
			})
			return value.witness, value.found, err
		},
	}
	guardedAuthorityLock := func(
		ctx context.Context,
		refs []store.AuthorizationFactRef,
	) (resultErr error) {
		if !authoritySnapshotAttempted.CompareAndSwap(false, true) {
			return fmt.Errorf(
				"%w: %w",
				errCommunicationTransactionUnavailable,
				errCommunicationAuthoritySnapshotAlreadyAttempted,
			)
		}
		requestLockSucceeded := false
		if boundState != nil {
			if err := boundState.beginAuthorityLock(); err != nil {
				return err
			}
			defer func() { boundState.finishAuthorityLock(requestLockSucceeded) }()
		}
		localFacts, err := CanonicalAuthorizationFacts(refs)
		if err != nil {
			return err
		}
		complete, err := canonicalCommunicationTransactionAuthorityFacts(
			localFacts, requestFacts, claimFacts,
		)
		if err != nil {
			return err
		}
		if err = authority.LockAuthoritySnapshot(ctx, complete); err != nil {
			if len(requestFacts) != 0 || len(claimFacts) != 0 {
				binding := "request"
				if len(claimFacts) != 0 {
					binding = "Claim"
				}
				return fmt.Errorf(
					"%w: %s authority snapshot: %w",
					ErrCommunicationEvidenceUnknown, binding, err,
				)
			}
			return err
		}
		authoritySnapshotSucceeded.Store(true)
		requestLockSucceeded = true
		return nil
	}
	guardedTransactionLock := func(ctx context.Context, key string) error {
		return runCommunicationBoundAuthorityLocalLock(boundState, func() error {
			return locker.LockTransaction(ctx, key)
		})
	}
	guardedAuditLock := func(ctx context.Context) error {
		return runCommunicationBoundAuthorityLocalLock(boundState, func() error {
			return auditLocker.LockAppends(ctx)
		})
	}
	guardedAuditAppend := func(
		ctx context.Context,
		draft model.AuditDraft,
	) (model.AuditEvent, error) {
		return runCommunicationBoundAuthorityEffect(boundState, func() (model.AuditEvent, error) {
			return audit.Append(ctx, draft)
		})
	}
	tx := &communicationTx{
		now:                     now,
		observeNowFn:            clock.TransactionNow,
		lockTransactionFn:       guardedTransactionLock,
		lockAuditAppendsFn:      guardedAuditLock,
		lockAuthoritySnapshotFn: guardedAuthorityLock,
		requestAuthorityFacts:   requestFacts,
		requestBindingID:        request.bindingID,
		requestObservedAt:       request.observedAt,
		requestFreshUntil:       request.freshUntil,
		claimAuthorityFacts:     claimFacts,
		boundAuthorityState:     boundState,
		audit: &communicationAuditAppenderAdapter{
			appendFn: guardedAuditAppend,
		},
		directory: directoryAdapter,
	}
	tx.resolveRepository = func(kind model.Kind) (communicationRepository, error) {
		return communicationRepositoryFromScope(sc, kind, boundState)
	}
	// The closure owns the one-shot state. Retain only a mirrored success bit on
	// tx so refresh/finalization can prove the complete snapshot was pinned.
	tx.authoritySnapshotLocked = authoritySnapshotSucceeded
	return tx, nil
}

func canonicalCommunicationTransactionAuthorityFacts(
	sets ...[]store.AuthorizationFactRef,
) ([]store.AuthorizationFactRef, error) {
	type factKey struct {
		kind model.Kind
		id   model.ID
	}
	unique := make(map[factKey]store.AuthorizationFactRef)
	for _, set := range sets {
		for _, fact := range set {
			if !fact.Kind.Valid() || !validCanonicalCommunicationID(fact.ID) || fact.Version < 1 {
				return nil, communicationError(
					ErrInvalidCommunicationModel, "invalid transaction authority fact",
				)
			}
			key := factKey{kind: fact.Kind, id: fact.ID}
			if prior, present := unique[key]; present {
				if prior != fact {
					return nil, communicationError(
						ErrCommunicationEvidenceUnknown,
						"transaction authority fact versions disagree",
					)
				}
				continue
			}
			unique[key] = fact
			if len(unique) > 64 {
				return nil, communicationError(
					ErrInvalidCommunicationModel, "authorization snapshot exceeds 64 facts",
				)
			}
		}
	}
	complete := make([]store.AuthorizationFactRef, 0, len(unique))
	for _, fact := range unique {
		complete = append(complete, fact)
	}
	sort.Slice(complete, func(i, j int) bool {
		if complete[i].Kind != complete[j].Kind {
			return complete[i].Kind < complete[j].Kind
		}
		return complete[i].ID.String() < complete[j].ID.String()
	})
	return complete, nil
}

// refreshNow resamples the authoritative database clock after callers have
// acquired every lock that may wait. newCommunicationTx's initial observation
// establishes the transaction stamp capability; this final observation becomes
// the stamp used by all subsequent writes and prevents an expired witness from
// remaining valid merely because the transaction waited behind another owner.
func (tx *communicationTx) refreshNow(ctx context.Context) error {
	if tx == nil || tx.observeNowFn == nil {
		return communicationTransactionUnavailable("transaction clock", nil)
	}
	if err := tx.boundAuthorityState.beginRefresh(); err != nil {
		return err
	}
	refreshSucceeded := false
	if tx.boundAuthorityState != nil {
		defer func() { tx.boundAuthorityState.finishRefresh(refreshSucceeded) }()
	}
	now, err := tx.observeNowFn(ctx)
	if err != nil {
		return communicationTransactionUnavailable("transaction clock", err)
	}
	if now.IsZero() {
		return communicationTransactionUnavailable("transaction clock returned zero", nil)
	}
	tx.now = now
	if tx.hasBoundAuthority() {
		if tx.authoritySnapshotLocked == nil || !tx.authoritySnapshotLocked.Load() {
			return communicationTransactionUnavailable("bound authority snapshot", nil)
		}
		if err := tx.validateAuthorityFreshness(now); err != nil {
			return err
		}
		tx.authorityFreshnessChecked.Store(true)
	}
	refreshSucceeded = true
	return nil
}

func (tx *communicationTx) hasBoundAuthority() bool {
	return tx != nil && (len(tx.requestAuthorityFacts) != 0 || len(tx.claimAuthorityFacts) != 0)
}

func (tx *communicationTx) validateAuthorityFreshness(now model.Timestamp) error {
	if len(tx.requestAuthorityFacts) != 0 &&
		(tx.requestObservedAt.IsZero() || tx.requestFreshUntil.IsZero() ||
			now.Time().Before(tx.requestObservedAt) || !now.Time().Before(tx.requestFreshUntil)) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "request authority is no longer fresh",
		)
	}
	return tx.validateClaimDeadlines(now)
}

// narrowRequestAuthorityFreshUntil adds a temporal constraint learned only
// after durable rows are locked. It can shorten but never extend the sealed
// request window. Exact reads use it for the OR-horizon of matching current
// ChannelGrants, whose expiry is driven by DB time and does not bump a row by
// itself.
func (tx *communicationTx) narrowRequestAuthorityFreshUntil(freshUntil time.Time) error {
	if tx == nil || len(tx.requestAuthorityFacts) == 0 || freshUntil.IsZero() ||
		tx.requestObservedAt.IsZero() || tx.requestFreshUntil.IsZero() ||
		tx.authoritySnapshotLocked == nil || !tx.authoritySnapshotLocked.Load() ||
		!tx.authorityFreshnessChecked.Load() {
		return communicationTransactionUnavailable("request authority deadline narrowing", nil)
	}
	freshUntil = freshUntil.UTC()
	if freshUntil.Before(tx.requestFreshUntil) {
		tx.requestFreshUntil = freshUntil
	}
	if !tx.requestFreshUntil.After(tx.requestObservedAt) {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "request authority deadline no longer overlaps",
		)
	}
	return tx.validateAuthorityFreshness(tx.now)
}

func (tx *communicationTx) validateClaimDeadlines(now model.Timestamp) error {
	for _, fact := range tx.claimAuthorityFacts {
		_, _, deadline, ok := fact.LeaseFenceWitness()
		if !ok || !now.Before(deadline) {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "Claim authority is no longer fresh",
			)
		}
	}
	return nil
}

// finalizeAuthority is the last operation in the surrounding Mutate
// callback. The row locks acquired by LockAuthoritySnapshot keep state/fence
// stable; a fresh DB observation closes the independent passage-of-time seam.
// Failure returns from the callback and rolls back both the OCC touch and every
// communication effect written before this check.
func (tx *communicationTx) finalizeAuthority(ctx context.Context) error {
	if tx == nil || !tx.hasBoundAuthority() {
		return nil
	}
	if tx.authoritySnapshotLocked == nil || !tx.authoritySnapshotLocked.Load() ||
		!tx.authorityFreshnessChecked.Load() {
		return communicationTransactionUnavailable("authority finalization", nil)
	}
	if err := tx.boundAuthorityState.beginFinalize(); err != nil {
		return err
	}
	finalizeSucceeded := false
	if tx.boundAuthorityState != nil {
		defer func() { tx.boundAuthorityState.finishFinalize(finalizeSucceeded) }()
	}
	now, err := tx.observeNowFn(ctx)
	if err != nil {
		return communicationTransactionUnavailable("transaction clock", err)
	}
	if now.IsZero() {
		return communicationTransactionUnavailable("transaction clock returned zero", nil)
	}
	if err := tx.validateAuthorityFreshness(now); err != nil {
		return err
	}
	finalizeSucceeded = true
	return nil
}

// finalizeClaimAuthority preserves the existing private test/service seam while
// delegating to the generalized request+Claim finalizer.
func (tx *communicationTx) finalizeClaimAuthority(ctx context.Context) error {
	return tx.finalizeAuthority(ctx)
}

func (tx *communicationTx) lockAuditAppends(ctx context.Context) error {
	if tx == nil || tx.lockAuditAppendsFn == nil {
		return communicationTransactionUnavailable("audit append lock", nil)
	}
	if err := tx.lockAuditAppendsFn(ctx); err != nil {
		return communicationTransactionUnavailable("audit append lock", err)
	}
	return nil
}

func communicationTransactionUnavailable(capability string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", errCommunicationTransactionUnavailable, capability)
	}
	return fmt.Errorf("%w: %s: %w", errCommunicationTransactionUnavailable, capability, cause)
}

func (tx *communicationTx) lockTransaction(ctx context.Context, key string) error {
	return tx.lockTransactionFn(ctx, key)
}

func (tx *communicationTx) lockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return tx.lockAuthoritySnapshotFn(ctx, refs)
}

func (tx *communicationTx) appendAudit(
	ctx context.Context,
	draft model.AuditDraft,
) (model.AuditEvent, error) {
	return tx.audit.Append(ctx, draft)
}

func (tx *communicationTx) directorySnapshotReader() store.DirectorySnapshotReader {
	return tx.directory
}

// communicationRepository deliberately omits ordinary Create, CreateWithID,
// Update and Delete. WP2 effects can therefore only use the DB-time-bound write
// surface after newCommunicationTx has observed TransactionNow.
type communicationRepository interface {
	Descriptor() model.EntityDescriptor
	Get(context.Context, model.ID) (model.Record, error)
	List(context.Context, model.Query) ([]model.Record, model.Page, error)
	Lock(context.Context, model.ID) (model.Record, error)
	CreateAtTransactionTime(context.Context, model.Record) (model.Record, error)
	CreateWithIDAtTransactionTime(
		context.Context,
		model.ID,
		model.Record,
	) (model.Record, error)
	UpdateAtTransactionTime(context.Context, model.Record) (model.Record, error)
}

type communicationRepositoryAdapter struct {
	descriptor   func() model.EntityDescriptor
	get          func(context.Context, model.ID) (model.Record, error)
	list         func(context.Context, model.Query) ([]model.Record, model.Page, error)
	lock         func(context.Context, model.ID) (model.Record, error)
	create       func(context.Context, model.Record) (model.Record, error)
	createWithID func(
		context.Context,
		model.ID,
		model.Record,
	) (model.Record, error)
	update func(context.Context, model.Record) (model.Record, error)
}

var _ communicationRepository = (*communicationRepositoryAdapter)(nil)

func (r *communicationRepositoryAdapter) Descriptor() model.EntityDescriptor {
	return r.descriptor()
}

func (r *communicationRepositoryAdapter) Get(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	return r.get(ctx, id)
}

func (r *communicationRepositoryAdapter) List(
	ctx context.Context,
	q model.Query,
) ([]model.Record, model.Page, error) {
	return r.list(ctx, q)
}

func (r *communicationRepositoryAdapter) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	return r.lock(ctx, id)
}

func (r *communicationRepositoryAdapter) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.create(ctx, record)
}

func (r *communicationRepositoryAdapter) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	return r.createWithID(ctx, id, record)
}

func (r *communicationRepositoryAdapter) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.update(ctx, record)
}

func communicationRepositoryFromScope(
	sc store.Scope,
	kind model.Kind,
	boundState *communicationBoundAuthorityState,
) (communicationRepository, error) {
	if !validCommunicationRepositoryKind(kind) {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("repository kind %q is outside the K3 communication inventory", kind), nil,
		)
	}
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	stamped, ok := repo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("transaction-stamped repository %q", kind), nil,
		)
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return nil, communicationTransactionUnavailable(
			fmt.Sprintf("row locker for repository %q", kind), nil,
		)
	}
	return &communicationRepositoryAdapter{
		descriptor: repo.Descriptor,
		get: func(ctx context.Context, id model.ID) (model.Record, error) {
			return runCommunicationBoundAuthorityObservation(boundState, func() (model.Record, error) {
				return repo.Get(ctx, id)
			})
		},
		list: func(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
			type result struct {
				records []model.Record
				page    model.Page
			}
			value, err := runCommunicationBoundAuthorityObservation(boundState, func() (result, error) {
				records, page, err := repo.List(ctx, query)
				return result{records: records, page: page}, err
			})
			return value.records, value.page, err
		},
		lock: func(ctx context.Context, id model.ID) (record model.Record, err error) {
			if boundState == nil {
				return locker.Lock(ctx, id)
			}
			if err := boundState.beginLocalLock(); err != nil {
				return nil, err
			}
			succeeded := false
			defer func() { boundState.finishLocalLock(succeeded) }()
			record, err = locker.Lock(ctx, id)
			succeeded = err == nil
			return record, err
		},
		create: func(ctx context.Context, record model.Record) (model.Record, error) {
			return runCommunicationBoundAuthorityEffect(boundState, func() (model.Record, error) {
				return stamped.CreateAtTransactionTime(ctx, record)
			})
		},
		createWithID: func(
			ctx context.Context,
			id model.ID,
			record model.Record,
		) (model.Record, error) {
			return runCommunicationBoundAuthorityEffect(boundState, func() (model.Record, error) {
				return stamped.CreateWithIDAtTransactionTime(ctx, id, record)
			})
		},
		update: func(ctx context.Context, record model.Record) (model.Record, error) {
			return runCommunicationBoundAuthorityEffect(boundState, func() (model.Record, error) {
				return stamped.UpdateAtTransactionTime(ctx, record)
			})
		},
	}, nil
}

func validCommunicationRepositoryKind(kind model.Kind) bool {
	for _, candidate := range communicationRepositoryInventory() {
		if kind == candidate {
			return true
		}
	}
	return false
}

// communicationRepositoryInventory is deliberately an exact-length array: K3
// owns twenty communication entities and shares only K1's event/outbox pair.
// Returning a value copy keeps callers from widening the runtime allowlist.
func communicationRepositoryInventory() [22]model.Kind {
	return [22]model.Kind{
		channelKind,
		channelGrantKind,
		channelSubscriptionKind,
		channelLabelDefinitionKind,
		channelRouteKind,
		communicationEndpointKind,
		messageKind,
		messageAudienceKind,
		messageAudienceRecipientKind,
		messageDeliveryKind,
		inboxCursorKind,
		inboxCursorBarrierKind,
		messageAckKind,
		communicationGuardKind,
		decisionRequestKind,
		decisionResponseKind,
		handoffKind,
		deliveryDispatchKind,
		deliveryAttemptKind,
		communicationCommandKind,
		workEventKind,
		workOutboxKind,
	}
}

func (tx *communicationTx) repo(kind model.Kind) (communicationRepository, error) {
	return tx.resolveRepository(kind)
}

func (tx *communicationTx) lockRecord(
	ctx context.Context,
	kind model.Kind,
	id model.ID,
) (model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	return repo.Lock(ctx, id)
}

func (tx *communicationTx) create(
	ctx context.Context,
	kind model.Kind,
	record model.Record,
) (model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	return repo.CreateAtTransactionTime(ctx, record)
}

func (tx *communicationTx) createWithID(
	ctx context.Context,
	kind model.Kind,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	return repo.CreateWithIDAtTransactionTime(ctx, id, record)
}

func (tx *communicationTx) update(
	ctx context.Context,
	kind model.Kind,
	record model.Record,
) (model.Record, error) {
	repo, err := tx.repo(kind)
	if err != nil {
		return nil, err
	}
	return repo.UpdateAtTransactionTime(ctx, record)
}

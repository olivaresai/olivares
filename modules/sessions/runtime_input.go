// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RuntimeInputWork retains the observed work generation, including the original
// K4 launch commitments. A renewal changes the observed expiry and requires a
// fresh input admission; a partial seal never selects legacy control.
type RuntimeInputWork struct {
	WorkItemID     model.ID
	LeaseFence     int64
	OwnerEpoch     int64
	LeaseExpiresAt string
	HolderSID      string
	HolderRunRef   string
	HolderAgentID  model.ID
	DispatchKey    [sha256.Size]byte
	LaunchSpecHash [sha256.Size]byte
}

// RuntimeInputTarget names one original launch, not whichever process currently
// occupies RunRef. Nil Work asserts an unbound run.
type RuntimeInputTarget struct {
	Tenant         model.TenantID
	WorkspaceID    model.ID
	RunRef         string
	ExpectedLaunch model.ID
	ExpectedSID    string
	Work           *RuntimeInputWork
}

// RuntimeInputAuthority carries two actual mutation decisions for the same
// reconstructed human. The entry owner selects its registered input:write question;
// this port adds the ordinary run-input question, with no Stop or work-admin
// action. Neither a read witness nor caller-supplied identity creates a proof.
type RuntimeInputAuthority struct {
	Entry              auth.Request
	EntryAuthorization auth.RouteMutationAuthorization
	RunAuthorization   auth.RouteMutationAuthorization
}

// RuntimeInputRunQuestion is the existing run-input permission on the target's
// stored workspace. An owner asks the composed authorizer this exact question.
func RuntimeInputRunQuestion(principal auth.Principal, target RuntimeInputTarget) auth.Request {
	return auth.Request{Principal: principal, Tenant: target.Tenant, Permission: permRunWrite,
		Resource: auth.ResourceAttrs{Kind: permRunWrite.Resource(), ID: target.RunRef, WorkspaceID: target.WorkspaceID}}
}

// AuthorityFor merges, without weakening, the two retained mutation decisions.
// The caller must lock this entire bundle on its mutation before validating the
// target or writing input authority, then recheck freshness after the lock.
func (a RuntimeInputAuthority) AuthorityFor(now time.Time, target RuntimeInputTarget) (store.AuthoritySnapshotBundle, error) {
	if err := validateRuntimeInputTarget(target); err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	p := a.Entry.Principal
	if a.Entry.Tenant != target.Tenant || a.Entry.Resource.WorkspaceID != target.WorkspaceID ||
		a.Entry.Resource.ID == "" || a.Entry.Resource.Kind == "" ||
		(a.Entry.Permission.Verb() != auth.VerbWrite && a.Entry.Permission.Verb() != auth.VerbAdmin) ||
		a.Entry.Route.CedarAction != "input:write" || !a.Entry.Route.RequireScopedGrant || a.Entry.Route.MinimumAAL < auth.AAL3 ||
		auth.RoleRank(a.Entry.Route.RBACMinimumRole) < auth.RoleRank(auth.RoleEditor) ||
		p.Kind != auth.KindUser || p.UserID.IsZero() || p.CredID.IsZero() || p.AAL < auth.AAL1 {
		return store.AuthoritySnapshotBundle{}, ErrRuntimeInputAuthority
	}
	entry, err := a.EntryAuthorization.AuthorityFor(now, a.Entry)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	run, err := a.RunAuthorization.AuthorityFor(now, RuntimeInputRunQuestion(p, target))
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	bundle, err := auth.MergeAuthoritySnapshotBundles(entry, run)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	if len(bundle.UserAuthorities) != 1 || bundle.UserAuthorities[0].UserID != p.UserID {
		return store.AuthoritySnapshotBundle{}, ErrRuntimeInputAuthority
	}
	return bundle, nil
}

type RuntimeInputMode string

const (
	RuntimeInputText RuntimeInputMode = "text"
	RuntimeInputRaw  RuntimeInputMode = "raw"
)

// RuntimeInputRequest preserves the transport discriminant. Raw is one NDJSON
// frame for the stream-json process; Text is a turn encoded by its owned driver.
type RuntimeInputRequest struct {
	Target    RuntimeInputTarget
	Authority RuntimeInputAuthority
	Mode      RuntimeInputMode
	Text      string
	Raw       []byte
}

type RuntimeInputOutcome string

const (
	RuntimeInputRefused     RuntimeInputOutcome = "refused"
	RuntimeInputUnsupported RuntimeInputOutcome = "unsupported"
	RuntimeInputAccepted    RuntimeInputOutcome = "accepted"
	RuntimeInputUnknown     RuntimeInputOutcome = "unknown"
)

// RuntimeInputResult never equates an attempted write with a confirmed result.
type RuntimeInputResult struct {
	Outcome   RuntimeInputOutcome
	Attempted bool
}

var (
	ErrRuntimeInputAuthority   = errors.New("sessions: runtime input authority is unavailable")
	ErrRuntimeInputTarget      = errors.New("sessions: runtime input target is stale or incomplete")
	ErrRuntimeInputUnsupported = errors.New("sessions: this runtime transport does not support the requested input mode")
	ErrRuntimeInputRefused     = errors.New("sessions: runtime input was refused before writing")
	ErrRuntimeInputUnknown     = errors.New("sessions: runtime input outcome is unknown; do not retry automatically")
)

func validateRuntimeInputTarget(target RuntimeInputTarget) error {
	tenant, err := model.ParseTenantID(target.Tenant.String())
	if err != nil || tenant != target.Tenant || tenant.IsZero() || tenant.IsSystem() ||
		!validRuntimeUUIDv7(target.ExpectedLaunch.String()) || !validCanonicalSID(target.ExpectedSID) || target.RunRef == "" {
		return ErrRuntimeInputTarget
	}
	workspace, err := model.ParseID(target.WorkspaceID.String())
	if err != nil || workspace.IsZero() || workspace != target.WorkspaceID {
		return ErrRuntimeInputTarget
	}
	if w := target.Work; w != nil {
		id, err := model.ParseID(w.WorkItemID.String())
		owner, ownerErr := model.ParseID(w.HolderAgentID.String())
		expires, timeErr := time.Parse(time.RFC3339Nano, w.LeaseExpiresAt)
		if err != nil || id.IsZero() || id != w.WorkItemID || ownerErr != nil || owner.IsZero() || owner != w.HolderAgentID || timeErr != nil || expires.IsZero() ||
			w.LeaseFence < 1 || w.OwnerEpoch < 1 || !validCanonicalSID(w.HolderSID) || w.HolderRunRef != target.RunRef ||
			w.DispatchKey == ([sha256.Size]byte{}) || w.LaunchSpecHash == ([sha256.Size]byte{}) {
			return ErrRuntimeInputTarget
		}
	}
	return nil
}

// ValidateRuntimeInputTargetInScope checks the target in the caller's existing
// mutation, after its complete directory/account authority barrier. It never
// starts a nested transaction. Successful validation is not a transferable
// permit: delivery rechecks the original launch under the run operation lock.
func (m *Module) ValidateRuntimeInputTargetInScope(ctx context.Context, sc store.Scope, target RuntimeInputTarget) error {
	_, err := m.runtimeInputTargetInScope(ctx, sc, target, "")
	return err
}

// ValidateRuntimeInputModeInScope checks target custody and transport compatibility
// before the caller records input. The caller must already hold its complete
// directory/account authority barrier in this same mutation Scope. This method
// neither authorizes the caller nor sends input; supported validation may touch
// the Claim through the existing target guard. Success is not a transferable
// permit: final delivery revalidates the target and mode under the run lock.
// Text denotes an owned provider turn; Raw denotes a stream-json frame. This
// method does not convert terminal input or validate payload contents.
func (m *Module) ValidateRuntimeInputModeInScope(ctx context.Context, sc store.Scope, target RuntimeInputTarget, mode RuntimeInputMode) error {
	switch mode {
	case RuntimeInputText, RuntimeInputRaw:
		_, err := m.runtimeInputTargetInScope(ctx, sc, target, mode)
		return err
	default:
		return ErrRuntimeInputUnsupported
	}
}

// An empty mode validates target custody only, as required by the exported
// target-only validator. Mode validation and delivery supply a closed mode so
// unsupported input is rejected before work authority or Claim enters the write set.
func (m *Module) runtimeInputTargetInScope(ctx context.Context, raw store.Scope, target RuntimeInputTarget, mode RuntimeInputMode) (model.Record, error) {
	if err := validateRuntimeInputTarget(target); err != nil {
		return nil, err
	}
	if ctx == nil || raw == nil || raw.Tenant() != target.Tenant || m == nil || m.rt == nil {
		return nil, ErrRuntimeInputTarget
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sc, err := store.ConfineWorkspace(ctx, raw, target.WorkspaceID)
	if err != nil {
		return nil, err
	}
	runs, err := sc.Ext(runKind)
	if err != nil {
		return nil, err
	}
	rec, err := findRunRec(ctx, runs, target.RunRef)
	if err != nil {
		return nil, err
	}
	if rec.String(colRunAuthzWorkspaceID) != target.WorkspaceID.String() ||
		rec.String(colRuntimeLaunchID) != target.ExpectedLaunch.String() || rec.String(colState) != stateRunning ||
		rec.String(colRunClaimSID) != target.ExpectedSID {
		return nil, ErrRuntimeInputTarget
	}
	live, ok := m.rt.getLive(target.Tenant, target.RunRef)
	if !ok || live.launchID != target.ExpectedLaunch || live.claim.SID != target.ExpectedSID || live.claim.Holder == "" || live.claim.Fence < 1 {
		return nil, ErrRuntimeInputTarget
	}
	if err := assertLaunchOwnsRow(live, rec); err != nil {
		return nil, err
	}
	if err := assertCurrentProfileHolds(ctx, raw, rec); err != nil {
		return nil, err
	}
	if mode != "" && (Transport(rec.String(colTransport)) == TransportRemoteControl ||
		(mode == RuntimeInputText && live.session == nil) || (mode == RuntimeInputRaw && live.session != nil)) {
		return nil, ErrRuntimeInputUnsupported
	}
	if runHasWorkBinding(rec) != (target.Work != nil) {
		return nil, ErrRuntimeInputTarget
	}
	if w := target.Work; w != nil {
		e, err := loadLockedRunWork(ctx, raw, target.Tenant, target.RunRef, w.LeaseFence, false)
		if err != nil {
			return nil, err
		}
		if err := assertActiveRunWork(e, w.HolderRunRef, w.HolderSID, w.LeaseFence); err != nil {
			return nil, err
		}
		if e.run.String(colRuntimeLaunchID) != target.ExpectedLaunch.String() || e.run.String(colState) != stateRunning ||
			e.run.String(colRunAuthzWorkspaceID) != target.WorkspaceID.String() {
			return nil, ErrRuntimeInputTarget
		}
		if err := assertLaunchOwnsRow(live, e.run); err != nil {
			return nil, err
		}
		if e.workspace != target.WorkspaceID || e.stamp.itemID != w.WorkItemID || e.stamp.ownerEpoch != w.OwnerEpoch ||
			e.lease.String(colLeaseExpiresAt) != w.LeaseExpiresAt || live.claim.SID != w.HolderSID ||
			e.lease.String(colLeaseHolderAgentRef) != w.HolderAgentID.String() || e.item.String(colWorkOwnerRef) != w.HolderAgentID.String() ||
			!bytes.Equal(e.stamp.dispatchKey, w.DispatchKey[:]) || !bytes.Equal(e.run.Bytes(colRunWorkLaunchSpecHash), w.LaunchSpecHash[:]) {
			return nil, ErrRuntimeInputTarget
		}
		rec = e.run
	}
	// Preserve the shared order: directory/account barrier, work item, Claim.
	// Refresh the transaction clock after the work lock, which may have waited.
	clock, ok := raw.(store.TransactionClock)
	if !ok {
		return nil, ErrRuntimeInputAuthority
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return nil, err
	}
	if err := fenceWithin(ctx, raw, live.claim.SID, live.claim.Holder, live.claim.Fence, now.Time()); err != nil {
		return nil, err
	}
	return rec, nil
}

// DeliverRuntimeInput connects retained input authority to the existing runtime
// transport. The input owner must claim its durable frame intent and recording
// reservation before calling, retain this result and never replay an unknown
// result. This port does not issue capabilities, create a recorder or mount an
// input route. It acquires the existing run lock once, not through the already
// locking InputForWork/TextForWork entries.
func (m *Module) DeliverRuntimeInput(ctx context.Context, request RuntimeInputRequest) (RuntimeInputResult, error) {
	refused := RuntimeInputResult{Outcome: RuntimeInputRefused}
	if ctx == nil || m == nil || m.data == nil || m.rt == nil {
		return refused, ErrRuntimeInputAuthority
	}
	if err := validateRuntimeInputTarget(request.Target); err != nil {
		return refused, err
	}
	request.Raw = bytes.Clone(request.Raw)
	if request.Target.Work != nil {
		work := *request.Target.Work
		request.Target.Work = &work
	}
	switch request.Mode {
	case RuntimeInputText:
		if len(request.Raw) != 0 || strings.TrimSpace(request.Text) == "" || !boundedText(request.Text, 1, maxWorkTextInputBytes) {
			return refused, ErrRuntimeInputRefused
		}
	case RuntimeInputRaw:
		if request.Text != "" || len(request.Raw) == 0 || len(request.Raw) > maxWorkTextInputBytes || bytes.ContainsAny(request.Raw, "\r\n") {
			return refused, ErrRuntimeInputRefused
		}
	default:
		return RuntimeInputResult{Outcome: RuntimeInputUnsupported}, ErrRuntimeInputUnsupported
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(request.Target.Tenant, request.Target.RunRef))
	if err != nil {
		return refused, err
	}
	defer release()
	var rec model.Record
	err = m.data.Mutate(ctx, request.Target.Tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return ErrRuntimeInputAuthority
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		bundle, err := request.Authority.AuthorityFor(now.Time(), request.Target)
		if err != nil {
			return err
		}
		barrier, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return ErrRuntimeInputAuthority
		}
		if err := barrier.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		now, err = clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		if _, err := request.Authority.AuthorityFor(now.Time(), request.Target); err != nil {
			return err
		}
		rec, err = m.runtimeInputTargetInScope(ctx, sc, request.Target, request.Mode)
		if err != nil {
			return err
		}
		now, err = clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		_, err = request.Authority.AuthorityFor(now.Time(), request.Target)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			// No transport was called, but the Claim touch may be durable. Keep
			// the database uncertainty distinct from an aborted admission.
			return RuntimeInputResult{Outcome: RuntimeInputUnknown}, errors.Join(ErrRuntimeInputUnknown, store.ErrCommitOutcomeUnknown)
		}
		if errors.Is(err, ErrRuntimeInputUnsupported) {
			return RuntimeInputResult{Outcome: RuntimeInputUnsupported}, ErrRuntimeInputUnsupported
		}
		return refused, err
	}
	live, ok := m.rt.getLive(request.Target.Tenant, request.Target.RunRef)
	if !ok || live.launchID != request.Target.ExpectedLaunch {
		return refused, ErrRuntimeInputTarget
	}
	if err := ctx.Err(); err != nil {
		return refused, err
	}
	if _, err := request.Authority.AuthorityFor(m.now(), request.Target); err != nil {
		return refused, err
	}
	var attempted bool
	if request.Mode == RuntimeInputText {
		attempted, err = m.sendTextInputLoaded(ctx, request.Target.Tenant, request.Target.RunRef, request.Text, rec)
	} else {
		attempted, err = m.sendInputLoaded(ctx, request.Target.Tenant, request.Target.RunRef, request.Raw, rec)
	}
	return m.settleRuntimeInputResult(ctx, request, attempted, err)
}

// settleRuntimeInputResult records the observed transport outcome; it cannot send.
func (m *Module) settleRuntimeInputResult(ctx context.Context, request RuntimeInputRequest, attempted bool, err error) (RuntimeInputResult, error) {
	if !attempted {
		return RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputRefused
	}
	if w := request.Target.Work; w != nil {
		generation := runtimeWorkGeneration{itemID: w.WorkItemID, holderSID: w.HolderSID, fence: w.LeaseFence}
		settleCtx := context.WithoutCancel(ctx)
		if err == nil {
			err = m.settleRunWorkAction(settleCtx, request.Target.Tenant, request.Target.RunRef, generation, workInputAccepted)
		}
		if err != nil {
			_ = m.recordAmbiguousWorkAction(settleCtx, request.Target.Tenant, request.Target.RunRef, generation, workInputAmbiguous, ErrRuntimeInputUnknown)
		}
	}
	if err != nil {
		return RuntimeInputResult{Outcome: RuntimeInputUnknown, Attempted: attempted}, ErrRuntimeInputUnknown
	}
	return RuntimeInputResult{Outcome: RuntimeInputAccepted, Attempted: attempted}, nil
}

// RuntimeInputChecks restricts one otherwise-authorized delivery. Both methods
// run in the port's final transaction, on the same Scope: BeforeTarget follows
// the complete directory/account barrier; BeforeSend follows target/work/Claim.
// Implementations must not open another transaction, retain the Scope, change
// target authority or perform external I/O. Returning nil grants no authority.
type RuntimeInputChecks interface {
	BeforeTarget(context.Context, store.Scope) error
	BeforeSend(context.Context, store.Scope) error
}

const maxCheckedRuntimeInputLease = 250 * time.Millisecond

// DeliverRuntimeInputChecked composes restrictive checks with the last authority
// transaction before delivery. The caller supplies the ORIGINAL request context,
// whose finite deadline has at most 250ms remaining and is bounded by its original
// request lease. This entry ceiling cannot establish when an external lease began.
// No wait creates or extends a lease, and no callback receives a transport handle.
//
// The order is caller-owned exclusions, run lock, complete directory/account
// barrier, BeforeTarget, target/work/Claim, BeforeSend, known commit, then I/O.
// No database lock spans I/O. The existing Process contract must honor the same
// deadline; an internal driver mutex may delay return without authorizing a late
// write. This method is not a receiver-side instantaneous revocation protocol.
func (m *Module) DeliverRuntimeInputChecked(ctx context.Context, request RuntimeInputRequest, checks RuntimeInputChecks) (RuntimeInputResult, error) {
	refused := RuntimeInputResult{Outcome: RuntimeInputRefused}
	if ctx == nil || m == nil || m.data == nil || m.rt == nil || nilRuntimeInputChecks(checks) {
		return refused, ErrRuntimeInputAuthority
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() || time.Until(deadline) > maxCheckedRuntimeInputLease {
		return refused, ErrRuntimeInputAuthority
	}
	if err := runtimeInputDeadline(ctx, deadline); err != nil {
		return refused, err
	}
	request.Raw = bytes.Clone(request.Raw)
	if request.Target.Work != nil {
		work := *request.Target.Work
		request.Target.Work = &work
	}
	request.Authority.Entry.Principal.AMR = slices.Clone(request.Authority.Entry.Principal.AMR)
	request.Authority.Entry.Resource.Extra = maps.Clone(request.Authority.Entry.Resource.Extra)
	if err := validateRuntimeInputTarget(request.Target); err != nil {
		return refused, err
	}
	switch request.Mode {
	case RuntimeInputText:
		if len(request.Raw) != 0 || strings.TrimSpace(request.Text) == "" || !boundedText(request.Text, 1, maxWorkTextInputBytes) {
			return refused, ErrRuntimeInputRefused
		}
	case RuntimeInputRaw:
		if request.Text != "" || len(request.Raw) == 0 || len(request.Raw) > maxWorkTextInputBytes || bytes.ContainsAny(request.Raw, "\r\n") {
			return refused, ErrRuntimeInputRefused
		}
	default:
		return RuntimeInputResult{Outcome: RuntimeInputUnsupported}, ErrRuntimeInputUnsupported
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(request.Target.Tenant, request.Target.RunRef))
	if err != nil {
		return refused, err
	}
	defer release()
	var rec model.Record
	var admitted *liveRun
	err = m.data.Mutate(ctx, request.Target.Tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return ErrRuntimeInputAuthority
		}
		refresh := func() (store.AuthoritySnapshotBundle, error) {
			if err := runtimeInputDeadline(ctx, deadline); err != nil {
				return store.AuthoritySnapshotBundle{}, err
			}
			now, err := clock.TransactionNow(ctx)
			if err != nil {
				return store.AuthoritySnapshotBundle{}, err
			}
			return runtimeInputCompleteAuthority(now.Time(), deadline, request)
		}
		bundle, err := refresh()
		if err != nil {
			return err
		}
		// Final owner checks may read governed state whose writers advance this
		// epoch without taking directory admission. Pin the actual fact along
		// with every retained proof in the ONE surrounding authority barrier.
		epochs, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return ErrRuntimeInputAuthority
		}
		epoch, err := epochs.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		if epoch.Kind != model.AuthorizationEpochKind || epoch.ID != model.ID(request.Target.Tenant) || epoch.Version < 1 {
			return ErrRuntimeInputAuthority
		}
		bundle, err = auth.MergeAuthoritySnapshotBundles(bundle, store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{epoch}})
		if err != nil {
			return err
		}
		barrier, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return ErrRuntimeInputAuthority
		}
		if err := barrier.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		if _, err := refresh(); err != nil {
			return err
		}
		if err := checks.BeforeTarget(ctx, sc); err != nil {
			return ErrRuntimeInputRefused
		}
		if _, err := refresh(); err != nil {
			return err
		}
		admitted, ok = m.rt.getLive(request.Target.Tenant, request.Target.RunRef)
		if !ok {
			return ErrRuntimeInputTarget
		}
		rec, err = m.runtimeInputTargetInScope(ctx, sc, request.Target, request.Mode)
		if err != nil {
			return err
		}
		if err := checks.BeforeSend(ctx, sc); err != nil {
			return ErrRuntimeInputRefused
		}
		if _, err := refresh(); err != nil {
			return err
		}
		// The Claim is already in this transaction's write set. CAS alone does
		// not keep a short lease alive while BeforeSend waits: the original
		// deadline must also fit the SAME Claim, never a newly acquired lease.
		claim, found, err := findClaim(ctx, sc, admitted.claim.SID)
		if err != nil {
			return err
		}
		if !found {
			return ErrRuntimeInputTarget
		}
		if err := assertFence(claimFenceState(claim), fenceToken{Holder: admitted.claim.Holder, Fence: admitted.claim.Fence}, deadline); err != nil {
			return err
		}
		return m.runtimeInputLiveCurrent(ctx, admitted, request.Target.ExpectedLaunch, request.Target.ExpectedSID)
	})
	if err != nil {
		if errors.Is(err, store.ErrCommitOutcomeUnknown) {
			return RuntimeInputResult{Outcome: RuntimeInputUnknown}, errors.Join(ErrRuntimeInputUnknown, store.ErrCommitOutcomeUnknown)
		}
		if errors.Is(err, ErrRuntimeInputUnsupported) {
			return RuntimeInputResult{Outcome: RuntimeInputUnsupported}, ErrRuntimeInputUnsupported
		}
		return refused, err
	}
	if err := runtimeInputDeadline(ctx, deadline); err != nil {
		return refused, err
	}
	if _, err := runtimeInputCompleteAuthority(m.now(), deadline, request); err != nil {
		return refused, err
	}
	if err := m.runtimeInputLiveCurrent(ctx, admitted, request.Target.ExpectedLaunch, request.Target.ExpectedSID); err != nil {
		return refused, err
	}
	var live *liveRun
	if request.Mode == RuntimeInputText {
		live, err = m.textInputLive(request.Target.Tenant, request.Target.RunRef, rec)
	} else {
		live, err = m.rawInputLive(request.Target.Tenant, request.Target.RunRef, request.Raw, rec)
	}
	if err != nil || live != admitted {
		return refused, ErrRuntimeInputTarget
	}
	var attempted bool
	if request.Mode == RuntimeInputText {
		attempted, err = m.driverInputAdmitted(ctx, live, request.Text, request.Target.ExpectedLaunch, request.Target.ExpectedSID)
	} else {
		attempted, err = m.sendInputAdmitted(ctx, live, request.Raw, request.Target.ExpectedLaunch, request.Target.ExpectedSID)
	}
	if attempted && err == nil {
		err = runtimeInputDeadline(ctx, deadline)
	}
	return m.settleRuntimeInputResult(ctx, request, attempted, err)
}

func nilRuntimeInputChecks(checks RuntimeInputChecks) bool {
	if checks == nil {
		return true
	}
	value := reflect.ValueOf(checks)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func runtimeInputDeadline(ctx context.Context, deadline time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// All evidence is retained original evidence. Merging neither refreshes it nor
// substitutes a newer source. The original deadline must fit every proof/lease.
func runtimeInputCompleteAuthority(now, deadline time.Time, request RuntimeInputRequest) (store.AuthoritySnapshotBundle, error) {
	a := request.Authority
	p := a.Entry.Principal
	evidence, ok := p.AuthenticationEvidence()
	if !ok {
		return store.AuthoritySnapshotBundle{}, ErrRuntimeInputAuthority
	}
	authenticated, err := evidence.AuthorityFor(now, p, request.Target.Tenant)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	authorized, err := a.AuthorityFor(now, request.Target)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	entry, err := a.EntryAuthorization.MetadataFor(now, a.Entry)
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	run, err := a.RunAuthorization.MetadataFor(now, RuntimeInputRunQuestion(p, request.Target))
	if err != nil {
		return store.AuthoritySnapshotBundle{}, err
	}
	if deadline.After(evidence.FreshUntil) || deadline.After(entry.FreshUntil) || deadline.After(run.FreshUntil) {
		return store.AuthoritySnapshotBundle{}, ErrRuntimeInputAuthority
	}
	if w := request.Target.Work; w != nil {
		expiry, err := time.Parse(time.RFC3339Nano, w.LeaseExpiresAt)
		if err != nil || deadline.After(expiry) {
			return store.AuthoritySnapshotBundle{}, ErrRuntimeInputAuthority
		}
	}
	return auth.MergeAuthoritySnapshotBundles(authenticated, authorized)
}

// runtimeInputLiveCurrent rechecks the original identity after waits. The run
// operation lock is held by the caller; this is not a transferable authority.
func (m *Module) runtimeInputLiveCurrent(ctx context.Context, live *liveRun, launch model.ID, sid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if live == nil || live.launchID != launch || live.claim.SID != sid {
		return ErrRuntimeInputTarget
	}
	current, ok := m.rt.getLive(live.tenant, live.runRef)
	if !ok || current != live {
		return ErrRuntimeInputTarget
	}
	return nil
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type recordingCommunicationModuleData struct {
	scope        store.Scope
	viewTenant   model.TenantID
	mutateTenant model.TenantID
	viewCalls    int
	mutateCalls  int
}

type countingCommunicationModuleData struct {
	inner       api.ModuleData
	viewCalls   int
	mutateCalls int
}

type reenteringCommunicationModuleData struct {
	inner           api.ModuleData
	callbackEntries int
}

func (d *reenteringCommunicationModuleData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *reenteringCommunicationModuleData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		d.callbackEntries++
		if err := fn(sc); err != nil {
			return err
		}
		d.callbackEntries++
		return fn(sc)
	})
}

func (d *countingCommunicationModuleData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.viewCalls++
	return d.inner.View(ctx, tenant, fn)
}

func (d *countingCommunicationModuleData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mutateCalls++
	return d.inner.Mutate(ctx, tenant, fn)
}

func (d *recordingCommunicationModuleData) View(
	_ context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.viewCalls++
	d.viewTenant = tenant
	return fn(d.scope)
}

func (d *recordingCommunicationModuleData) Mutate(
	_ context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mutateCalls++
	d.mutateTenant = tenant
	return fn(d.scope)
}

func TestTenantCommunicationDataBindsTenantWithoutNestedUnitOfWork(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tenant := model.NewTenantID()
	scope := &communicationTxCapabilityScope{}
	recording := &recordingCommunicationModuleData{scope: scope}
	m := &Module{data: recording}
	data := m.communicationData(tenant)

	viewCallbacks := 0
	if err := data.View(ctx, func(got store.Scope) error {
		viewCallbacks++
		if got != scope {
			t.Fatal("View replaced the ModuleData callback scope")
		}
		return nil
	}); err != nil {
		t.Fatalf("View: %v", err)
	}
	mutateCallbacks := 0
	if err := data.Mutate(ctx, func(got store.Scope) error {
		mutateCallbacks++
		if got != scope {
			t.Fatal("Mutate replaced the ModuleData callback scope")
		}
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if recording.viewCalls != 1 || viewCallbacks != 1 || recording.viewTenant != tenant {
		t.Fatalf("View calls/callbacks/tenant = %d/%d/%s, want 1/1/%s",
			recording.viewCalls, viewCallbacks, recording.viewTenant, tenant)
	}
	if recording.mutateCalls != 1 || mutateCallbacks != 1 || recording.mutateTenant != tenant {
		t.Fatalf("Mutate calls/callbacks/tenant = %d/%d/%s, want 1/1/%s",
			recording.mutateCalls, mutateCallbacks, recording.mutateTenant, tenant)
	}

	unwired := tenantCommunicationData{tenant: tenant}
	if err := unwired.View(ctx, func(store.Scope) error { return nil }); !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("unwired View error = %v, want ErrStoreUnavailable", err)
	}
	if err := unwired.Mutate(ctx, func(store.Scope) error { return nil }); !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("unwired Mutate error = %v, want ErrStoreUnavailable", err)
	}

	invalidData := &recordingCommunicationModuleData{scope: scope}
	invalidModule := &Module{data: invalidData}
	if err := invalidModule.viewCommunication(ctx, DirectoryScopeRef{}, func(store.Scope) error {
		return nil
	}); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("invalid view scope error = %v, want ErrInvalidCommunicationModel", err)
	}
	if err := invalidModule.mutateCommunication(ctx, DirectoryScopeRef{}, func(*communicationTx) error {
		return nil
	}); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("invalid mutation scope error = %v, want ErrInvalidCommunicationModel", err)
	}
	if invalidData.viewCalls != 0 || invalidData.mutateCalls != 0 {
		t.Fatalf("invalid scope opened ModuleData View/Mutate = %d/%d, want zero/zero",
			invalidData.viewCalls, invalidData.mutateCalls)
	}
}

type communicationTxCapabilityScope struct {
	store.Scope

	now            model.Timestamp
	nowSequence    []model.Timestamp
	nowErr         error
	nowPanicAt     int
	nowCalls       int
	lockErr        error
	lockKeys       []string
	authorityErr   error
	authorityPanic bool
	authorityRefs  [][]store.AuthorizationFactRef
	epoch          model.DirectoryEpoch
	audit          store.AuditLog
	repo           store.GenericRepo
	extErr         error
}

var (
	_ store.TransactionClock        = (*communicationTxCapabilityScope)(nil)
	_ store.TransactionLocker       = (*communicationTxCapabilityScope)(nil)
	_ store.AuthoritySnapshotLocker = (*communicationTxCapabilityScope)(nil)
	_ store.DirectorySnapshotReader = (*communicationTxCapabilityScope)(nil)
)

func (s *communicationTxCapabilityScope) TransactionNow(context.Context) (model.Timestamp, error) {
	s.nowCalls++
	if s.nowPanicAt != 0 && s.nowCalls == s.nowPanicAt {
		panic("transaction clock panic")
	}
	if len(s.nowSequence) != 0 {
		index := min(s.nowCalls-1, len(s.nowSequence)-1)
		return s.nowSequence[index], s.nowErr
	}
	return s.now, s.nowErr
}

func (s *communicationTxCapabilityScope) LockTransaction(_ context.Context, key string) error {
	s.lockKeys = append(s.lockKeys, key)
	return s.lockErr
}

func (s *communicationTxCapabilityScope) LockAuthoritySnapshot(
	_ context.Context,
	refs []store.AuthorizationFactRef,
) error {
	if s.authorityPanic {
		panic("authority snapshot panic")
	}
	copyRefs := append([]store.AuthorizationFactRef(nil), refs...)
	s.authorityRefs = append(s.authorityRefs, copyRefs)
	return s.authorityErr
}

func (s *communicationTxCapabilityScope) ReadDirectoryEpoch(
	context.Context,
) (model.DirectoryEpoch, error) {
	return s.epoch, nil
}

func (*communicationTxCapabilityScope) ReadDirectoryTombstone(
	context.Context,
	store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return store.DirectoryTombstoneWitness{}, false, nil
}

func (s *communicationTxCapabilityScope) Audit() store.AuditLog {
	if s.audit != nil {
		return s.audit
	}
	return communicationTxTestAuditLog{}
}

func (s *communicationTxCapabilityScope) Ext(model.Kind) (store.GenericRepo, error) {
	return s.repo, s.extErr
}

// communicationTxTestAuditLog supplies the required Scope capability to the
// narrow unit fakes. Its read methods panic so a constructor that accidentally
// consults the read side fails loudly.
type communicationTxTestAuditLog struct{}

func (communicationTxTestAuditLog) LockAppends(context.Context) error { return nil }

func (communicationTxTestAuditLog) Append(
	context.Context,
	model.AuditDraft,
) (model.AuditEvent, error) {
	return model.AuditEvent{Seq: 1}, nil
}

func (communicationTxTestAuditLog) Verify(context.Context, int64) (store.VerifyReport, error) {
	panic("communication transaction consulted audit Verify")
}

func (communicationTxTestAuditLog) Walk(
	context.Context,
	int64,
	func(model.AuditEvent) error,
) error {
	panic("communication transaction consulted audit Walk")
}

func (communicationTxTestAuditLog) Head(context.Context) (store.HeadRef, bool, error) {
	panic("communication transaction consulted audit Head")
}

type communicationTxTestAuditLogWithoutAppendLock struct{}

func (communicationTxTestAuditLogWithoutAppendLock) Append(
	context.Context,
	model.AuditDraft,
) (model.AuditEvent, error) {
	return model.AuditEvent{Seq: 1}, nil
}

func (communicationTxTestAuditLogWithoutAppendLock) Verify(
	context.Context,
	int64,
) (store.VerifyReport, error) {
	panic("communication transaction consulted audit Verify")
}

func (communicationTxTestAuditLogWithoutAppendLock) Walk(
	context.Context,
	int64,
	func(model.AuditEvent) error,
) error {
	panic("communication transaction consulted audit Walk")
}

func (communicationTxTestAuditLogWithoutAppendLock) Head(
	context.Context,
) (store.HeadRef, bool, error) {
	panic("communication transaction consulted audit Head")
}

type communicationTxScopeWithoutClock struct {
	store.Scope
	store.TransactionLocker
	store.AuthoritySnapshotLocker
	store.DirectorySnapshotReader
}

type communicationTxScopeWithoutLocker struct {
	store.Scope
	store.TransactionClock
	store.AuthoritySnapshotLocker
	store.DirectorySnapshotReader
}

type communicationTxScopeWithoutAuthority struct {
	store.Scope
	store.TransactionClock
	store.TransactionLocker
	store.DirectorySnapshotReader
}

type communicationTxScopeWithoutDirectory struct {
	store.Scope
	store.TransactionClock
	store.TransactionLocker
	store.AuthoritySnapshotLocker
}

type communicationTxScopeWithoutAudit struct {
	*communicationTxCapabilityScope
}

func (*communicationTxScopeWithoutAudit) Audit() store.AuditLog { return nil }

func TestNewCommunicationTxFailsClosedWhenCapabilityIsAbsent(t *testing.T) {
	t.Parallel()

	now := model.NewTimestamp(time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		name       string
		capability string
		scope      func(*communicationTxCapabilityScope) store.Scope
	}{
		{
			name: "transaction_clock", capability: "transaction clock",
			scope: func(all *communicationTxCapabilityScope) store.Scope {
				return communicationTxScopeWithoutClock{
					Scope: all, TransactionLocker: all,
					AuthoritySnapshotLocker: all, DirectorySnapshotReader: all,
				}
			},
		},
		{
			name: "transaction_locker", capability: "transaction locker",
			scope: func(all *communicationTxCapabilityScope) store.Scope {
				return communicationTxScopeWithoutLocker{
					Scope: all, TransactionClock: all,
					AuthoritySnapshotLocker: all, DirectorySnapshotReader: all,
				}
			},
		},
		{
			name: "authority_snapshot_locker", capability: "authority snapshot locker",
			scope: func(all *communicationTxCapabilityScope) store.Scope {
				return communicationTxScopeWithoutAuthority{
					Scope: all, TransactionClock: all,
					TransactionLocker: all, DirectorySnapshotReader: all,
				}
			},
		},
		{
			name: "directory_snapshot_reader", capability: "directory snapshot reader",
			scope: func(all *communicationTxCapabilityScope) store.Scope {
				return communicationTxScopeWithoutDirectory{
					Scope: all, TransactionClock: all,
					TransactionLocker: all, AuthoritySnapshotLocker: all,
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			all := &communicationTxCapabilityScope{now: now}
			got, err := newCommunicationTx(context.Background(), tc.scope(all))
			if got != nil {
				t.Fatalf("transaction = %#v, want nil", got)
			}
			if !errors.Is(err, errCommunicationTransactionUnavailable) ||
				!errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("error = %v, want stable deny-closed sentinels", err)
			}
			if !strings.Contains(err.Error(), tc.capability) {
				t.Fatalf("error = %q, want missing capability %q", err, tc.capability)
			}
			if all.nowCalls != 0 {
				t.Fatalf("TransactionNow calls = %d, want zero before capability proof", all.nowCalls)
			}
		})
	}

	if got, err := newCommunicationTx(context.Background(), nil); got != nil ||
		!errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("nil scope transaction/error = %#v/%v, want deny-closed", got, err)
	}

	withoutAudit := &communicationTxCapabilityScope{now: now}
	got, err := newCommunicationTx(context.Background(), &communicationTxScopeWithoutAudit{
		communicationTxCapabilityScope: withoutAudit,
	})
	if got != nil || !errors.Is(err, errCommunicationTransactionUnavailable) ||
		!errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!strings.Contains(err.Error(), "audit append") {
		t.Fatalf("nil audit transaction/error = %#v/%v, want deny-closed audit append", got, err)
	}
	if withoutAudit.nowCalls != 0 {
		t.Fatalf("TransactionNow calls without audit append = %d, want zero", withoutAudit.nowCalls)
	}

	withoutAppendLock := &communicationTxCapabilityScope{
		now: now, audit: communicationTxTestAuditLogWithoutAppendLock{},
	}
	got, err = newCommunicationTx(context.Background(), withoutAppendLock)
	if got != nil || !errors.Is(err, errCommunicationTransactionUnavailable) ||
		!errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!strings.Contains(err.Error(), "audit append lock") {
		t.Fatalf("missing audit append lock transaction/error = %#v/%v, want deny-closed", got, err)
	}
	if withoutAppendLock.nowCalls != 0 {
		t.Fatalf("TransactionNow calls without audit append lock = %d, want zero", withoutAppendLock.nowCalls)
	}
}

func TestNewCommunicationTxTakesInitialDBNowAndLocksAuthorityOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := model.NewTimestamp(time.Date(2026, time.August, 15, 9, 30, 0, 0, time.UTC))
	scope := &communicationTxCapabilityScope{now: now}
	tx, err := newCommunicationTx(ctx, scope)
	if err != nil {
		t.Fatalf("new communication transaction: %v", err)
	}
	if tx.now.String() != now.String() || scope.nowCalls != 1 {
		t.Fatalf("DBNow/calls = %s/%d, want %s/1", tx.now.String(), scope.nowCalls, now.String())
	}
	if err := tx.lockTransaction(ctx, "sessions_message_publish|tenant|message"); err != nil {
		t.Fatalf("lock transaction: %v", err)
	}
	if len(scope.lockKeys) != 1 || scope.lockKeys[0] != "sessions_message_publish|tenant|message" {
		t.Fatalf("transaction lock keys = %v", scope.lockKeys)
	}

	refs := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 3,
	}}
	if err := tx.lockAuthoritySnapshot(ctx, refs); err != nil {
		t.Fatalf("first authority snapshot: %v", err)
	}
	refs[0].Version = 99
	if got := scope.authorityRefs[0][0].Version; got != 3 {
		t.Fatalf("stored authority refs aliased caller slice: version = %d, want 3", got)
	}
	if err := tx.lockAuthoritySnapshot(ctx, refs); !errors.Is(err, errCommunicationAuthoritySnapshotAlreadyAttempted) ||
		!errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("second authority snapshot error = %v, want stable deny-closed error", err)
	}
	if len(scope.authorityRefs) != 1 {
		t.Fatalf("authority locker calls = %d, want exactly one", len(scope.authorityRefs))
	}
	if scope.nowCalls != 1 {
		t.Fatalf("TransactionNow calls after helpers = %d, want exactly one", scope.nowCalls)
	}
}

func TestCommunicationTxDoesNotRetryFailedAuthoritySnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cause := errors.New("authority store offline")
	scope := &communicationTxCapabilityScope{
		now:          model.NewTimestamp(time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)),
		authorityErr: cause,
	}
	tx, err := newCommunicationTx(ctx, scope)
	if err != nil {
		t.Fatalf("new communication transaction: %v", err)
	}
	refs := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
	}}
	if err := tx.lockAuthoritySnapshot(ctx, refs); !errors.Is(err, cause) {
		t.Fatalf("first authority error = %v, want cause", err)
	}
	if err := tx.lockAuthoritySnapshot(ctx, refs); !errors.Is(err, errCommunicationAuthoritySnapshotAlreadyAttempted) {
		t.Fatalf("retry authority error = %v, want already-attempted", err)
	}
	if len(scope.authorityRefs) != 1 {
		t.Fatalf("authority locker calls after failed attempt = %d, want one", len(scope.authorityRefs))
	}
}

func communicationClaimAuthorityFact(
	t *testing.T,
	sid string,
	fence int64,
	deadline model.Timestamp,
) store.AuthorizationFactRef {
	t.Helper()
	ref, err := store.NewLeaseFenceAuthorizationFactRef(
		claimKind, model.NewID(), 3, sid, fence, deadline,
	)
	if err != nil {
		t.Fatalf("new Claim authority fact: %v", err)
	}
	return ref
}

func TestCommunicationClaimAuthoritySnapshotIsCanonicalAndOpaque(t *testing.T) {
	t.Parallel()

	deadline := model.NewTimestamp(time.Date(2026, time.August, 15, 11, 0, 0, 0, time.UTC))
	firstSID := "osn_" + model.NewID().String()
	secondSID := "osn_" + model.NewID().String()
	if secondSID < firstSID {
		firstSID, secondSID = secondSID, firstSID
	}
	claims := []CommunicationClaimRef{
		{SessionSID: secondSID, Fence: 9},
		{SessionSID: firstSID, Fence: 4},
	}
	facts := []store.AuthorizationFactRef{
		communicationClaimAuthorityFact(t, secondSID, 9, deadline),
		communicationClaimAuthorityFact(t, firstSID, 4, deadline),
	}
	snapshot, err := NewCommunicationClaimAuthoritySnapshot(claims, facts)
	if err != nil {
		t.Fatalf("new canonical Claim snapshot: %v", err)
	}
	if len(snapshot.facts) != 2 {
		t.Fatalf("Claim snapshot fact count = %d, want 2", len(snapshot.facts))
	}
	gotSID, gotFence, _, ok := snapshot.facts[0].LeaseFenceWitness()
	if !ok || gotSID != firstSID || gotFence != 4 {
		t.Fatalf("first canonical Claim witness = %q/%d/%v, want %q/4/true",
			gotSID, gotFence, ok, firstSID)
	}
	claims[0].Fence = 99
	facts[0].Version = 99
	if snapshot.facts[1].Version != 3 {
		t.Fatal("opaque Claim snapshot aliases its caller's fact slice")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal opaque Claim snapshot: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("opaque Claim snapshot serialized private witnesses: %s", encoded)
	}

	duplicateSID := []CommunicationClaimRef{
		{SessionSID: firstSID, Fence: 4},
		{SessionSID: firstSID, Fence: 5},
	}
	duplicateFacts := []store.AuthorizationFactRef{
		communicationClaimAuthorityFact(t, firstSID, 4, deadline),
		communicationClaimAuthorityFact(t, firstSID, 5, deadline),
	}
	if _, err := NewCommunicationClaimAuthoritySnapshot(
		duplicateSID, duplicateFacts,
	); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("conflicting Claim fences error = %v, want ErrInvalidCommunicationModel", err)
	}
}

func TestCommunicationTxClaimAuthorityUsesOneSnapshotAndTwoDBFreshnessChecks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC))
	refreshed := model.NewTimestamp(initial.Time().Add(time.Minute))
	final := model.NewTimestamp(initial.Time().Add(2 * time.Minute))
	deadline := model.NewTimestamp(initial.Time().Add(3 * time.Minute))
	sid := "osn_" + model.NewID().String()
	claimFact := communicationClaimAuthorityFact(t, sid, 8, deadline)
	snapshot, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: 8}},
		[]store.AuthorizationFactRef{claimFact},
	)
	if err != nil {
		t.Fatalf("new Claim authority snapshot: %v", err)
	}
	scope := &communicationTxCapabilityScope{
		nowSequence: []model.Timestamp{initial, refreshed, final},
	}
	tx, err := newCommunicationTxWithClaimAuthority(ctx, scope, snapshot)
	if err != nil {
		t.Fatalf("new Claim-bound communication transaction: %v", err)
	}
	if _, ok := any(tx).(store.Scope); ok {
		t.Fatal("communicationTx exposes the confined store.Scope")
	}
	if _, err := tx.repo(claimKind); err == nil {
		t.Fatal("communicationTx recovered the raw Claim repository")
	}
	directoryFact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 2,
	}
	if err := tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{directoryFact}); err != nil {
		t.Fatalf("lock complete authority snapshot: %v", err)
	}
	if len(scope.authorityRefs) != 1 || len(scope.authorityRefs[0]) != 2 {
		t.Fatalf("authority locker calls/facts = %d/%v, want one call/two facts",
			len(scope.authorityRefs), scope.authorityRefs)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("refresh Claim authority: %v", err)
	}
	if tx.now.String() != refreshed.String() {
		t.Fatalf("refreshed DBNow = %s, want %s", tx.now.String(), refreshed.String())
	}
	if err := tx.finalizeClaimAuthority(ctx); err != nil {
		t.Fatalf("finalize live Claim authority: %v", err)
	}
	if scope.nowCalls != 3 {
		t.Fatalf("DBNow calls = %d, want initial/refresh/final = 3", scope.nowCalls)
	}
}

func TestCommunicationTxClaimAuthorityLockFailureIsUnknown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := model.NewTimestamp(time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC))
	sid := "osn_" + model.NewID().String()
	snapshot, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: 2}},
		[]store.AuthorizationFactRef{
			communicationClaimAuthorityFact(t, sid, 2, model.NewTimestamp(now.Time().Add(time.Hour))),
		},
	)
	if err != nil {
		t.Fatalf("new Claim authority snapshot: %v", err)
	}
	scope := &communicationTxCapabilityScope{now: now, authorityErr: store.ErrConflict}
	tx, err := newCommunicationTxWithClaimAuthority(ctx, scope, snapshot)
	if err != nil {
		t.Fatalf("new Claim-bound transaction: %v", err)
	}
	err = tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
	}})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Claim lock conflict = %v, want UNKNOWN wrapping ErrConflict", err)
	}
}

func TestCommunicationTxClaimAuthorityFinalDBNowDeniesAtDeadline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC))
	deadline := model.NewTimestamp(initial.Time().Add(2 * time.Minute))
	sid := "osn_" + model.NewID().String()
	snapshot, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: 2}},
		[]store.AuthorizationFactRef{
			communicationClaimAuthorityFact(t, sid, 2, deadline),
		},
	)
	if err != nil {
		t.Fatalf("new Claim authority snapshot: %v", err)
	}
	scope := &communicationTxCapabilityScope{nowSequence: []model.Timestamp{
		initial,
		model.NewTimestamp(initial.Time().Add(time.Minute)),
		deadline,
	}}
	tx, err := newCommunicationTxWithClaimAuthority(ctx, scope, snapshot)
	if err != nil {
		t.Fatalf("new Claim-bound transaction: %v", err)
	}
	if err := tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
	}}); err != nil {
		t.Fatalf("lock Claim authority: %v", err)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("refresh still-live Claim: %v", err)
	}
	err = tx.finalizeClaimAuthority(ctx)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("final Claim freshness error = %v, want UNKNOWN", err)
	}

	withoutRefresh, err := newCommunicationTxWithClaimAuthority(ctx,
		&communicationTxCapabilityScope{now: initial}, snapshot)
	if err != nil {
		t.Fatalf("new no-refresh Claim transaction: %v", err)
	}
	if err := withoutRefresh.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
	}}); err != nil {
		t.Fatalf("lock no-refresh Claim authority: %v", err)
	}
	if err := withoutRefresh.finalizeClaimAuthority(ctx); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("missing refresh finalization error = %v, want unavailable", err)
	}
}

func TestCommunicationTxFinalAuthorityChecksEveryClaimDeadline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 15, 10, 15, 0, 0, time.UTC))
	refreshed := model.NewTimestamp(initial.Time().Add(time.Minute))
	final := model.NewTimestamp(initial.Time().Add(2 * time.Minute))
	firstSID := "osn_" + model.NewID().String()
	secondSID := "osn_" + model.NewID().String()
	if secondSID < firstSID {
		firstSID, secondSID = secondSID, firstSID
	}
	claims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{
			{SessionSID: firstSID, Fence: 3},
			{SessionSID: secondSID, Fence: 4},
		},
		[]store.AuthorizationFactRef{
			communicationClaimAuthorityFact(
				t, firstSID, 3, model.NewTimestamp(final.Time().Add(time.Hour)),
			),
			communicationClaimAuthorityFact(t, secondSID, 4, final),
		},
	)
	if err != nil {
		t.Fatalf("new two-Claim authority snapshot: %v", err)
	}
	request := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
		}},
		observedAt: initial.Time().Add(-time.Minute),
		freshUntil: initial.Time().Add(time.Hour),
	}
	tx, err := newCommunicationTxWithAuthority(
		ctx,
		&communicationTxCapabilityScope{
			nowSequence: []model.Timestamp{initial, refreshed, final},
		},
		request,
		claims,
	)
	if err != nil {
		t.Fatalf("new request+two-Claim transaction: %v", err)
	}
	if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
		t.Fatalf("lock request+two-Claim authority: %v", err)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("refresh request+two-Claim authority: %v", err)
	}
	if err := tx.finalizeAuthority(ctx); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("final second-Claim expiry = %v, want UNKNOWN", err)
	}
}

func TestCommunicationTxRequestAuthorityUsesOneCanonicalSnapshotAndTwoDBWindowChecks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC))
	observedAt := initial.Time().Add(-time.Minute)
	refreshed := model.NewTimestamp(initial.Time().Add(time.Minute))
	final := model.NewTimestamp(initial.Time().Add(2 * time.Minute))
	freshUntil := initial.Time().Add(3 * time.Minute)
	tenant := model.NewTenantID()
	directoryFact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 7,
	}
	authorizationFact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 11,
	}
	request := communicationRequestAuthoritySnapshot{
		facts:      []store.AuthorizationFactRef{authorizationFact, directoryFact},
		observedAt: observedAt, freshUntil: freshUntil,
	}
	sid := "osn_" + model.NewID().String()
	claimFact := communicationClaimAuthorityFact(
		t, sid, 8, model.NewTimestamp(freshUntil.Add(time.Hour)),
	)
	claims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: 8}},
		[]store.AuthorizationFactRef{claimFact},
	)
	if err != nil {
		t.Fatalf("new Claim authority snapshot: %v", err)
	}
	scope := &communicationTxCapabilityScope{
		nowSequence: []model.Timestamp{initial, refreshed, final},
	}
	tx, err := newCommunicationTxWithAuthority(ctx, scope, request, claims)
	if err != nil {
		t.Fatalf("new request-bound communication transaction: %v", err)
	}
	// Mutating either caller-owned snapshot after construction cannot change the
	// one complete set retained by the transaction.
	request.facts[0].Version = 99
	claims.facts[0].Version = 99
	identityFact := store.AuthorizationFactRef{
		Kind: "core.identity", ID: model.NewID(), Version: 5,
	}
	if err := tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{
		directoryFact, identityFact,
	}); err != nil {
		t.Fatalf("lock complete request+Claim authority snapshot: %v", err)
	}
	if len(scope.authorityRefs) != 1 || len(scope.authorityRefs[0]) != 4 {
		t.Fatalf("authority locker calls/facts = %d/%#v, want one call/four unique facts",
			len(scope.authorityRefs), scope.authorityRefs)
	}
	want := map[store.AuthorizationFactRef]bool{
		authorizationFact: true, directoryFact: true, claimFact: true, identityFact: true,
	}
	for _, fact := range scope.authorityRefs[0] {
		if !want[fact] {
			t.Fatalf("locked unexpected authority fact %#v", fact)
		}
		delete(want, fact)
	}
	if len(want) != 0 {
		t.Fatalf("authority snapshot omitted facts %#v", want)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("post-lock request authority refresh: %v", err)
	}
	if err := tx.finalizeAuthority(ctx); err != nil {
		t.Fatalf("final request authority refresh: %v", err)
	}
	if scope.nowCalls != 3 {
		t.Fatalf("DBNow calls = %d, want initial/post-lock/final = 3", scope.nowCalls)
	}
}

func TestCommunicationTxRequestAuthorityEnforcesLockRefreshEffectPhases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 16, 10, 30, 0, 0, time.UTC))
	request := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
		}},
		observedAt: initial.Time().Add(-time.Minute),
		freshUntil: initial.Time().Add(time.Hour),
	}
	scope := &communicationTxCapabilityScope{
		nowSequence: []model.Timestamp{initial, initial, initial},
	}
	tx, err := newCommunicationTxWithAuthority(
		ctx, scope, request, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		t.Fatalf("new request-bound transaction: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("effect before authority lock = %v, want unavailable", err)
	}
	if _, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx); err != nil {
		t.Fatalf("pre-snapshot directory observation: %v", err)
	}
	if err := tx.lockTransaction(ctx, "before-authority"); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("local lock before authority snapshot = %v, want unavailable", err)
	}
	if len(scope.lockKeys) != 0 {
		t.Fatalf("out-of-phase local lock reached store: %#v", scope.lockKeys)
	}
	if err := tx.refreshNow(ctx); !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("DB refresh before authority snapshot = %v, want unavailable", err)
	}
	if scope.nowCalls != 1 {
		t.Fatalf("out-of-phase refresh sampled DB time %d times, want initial call only", scope.nowCalls)
	}
	if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
		t.Fatalf("lock request authority snapshot: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("effect before DB refresh = %v, want unavailable", err)
	}
	if err := tx.lockTransaction(ctx, "after-authority"); err != nil {
		t.Fatalf("local lock before DB refresh: %v", err)
	}
	if err := tx.lockAuditAppends(ctx); err != nil {
		t.Fatalf("audit lock before DB refresh: %v", err)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("refresh after all locks: %v", err)
	}
	if err := tx.refreshNow(ctx); !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("second request authority refresh = %v, want unavailable", err)
	}
	if err := tx.lockTransaction(ctx, "after-refresh"); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("late local lock = %v, want unavailable", err)
	}
	if len(scope.lockKeys) != 1 || scope.lockKeys[0] != "after-authority" {
		t.Fatalf("store lock keys = %#v, want only after-authority", scope.lockKeys)
	}
	if err := tx.lockAuditAppends(ctx); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("late audit lock = %v, want unavailable", err)
	}
	if _, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("late directory observation = %v, want unavailable", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); err != nil {
		t.Fatalf("effect after DB refresh: %v", err)
	}
	if err := tx.finalizeAuthority(ctx); err != nil {
		t.Fatalf("final request authority check: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("effect after finalization = %v, want unavailable", err)
	}
}

func TestCommunicationTxClaimAuthorityEnforcesLockRefreshEffectPhases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 16, 10, 45, 0, 0, time.UTC))
	sid := "osn_" + model.NewID().String()
	claims, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: sid, Fence: 3}},
		[]store.AuthorizationFactRef{
			communicationClaimAuthorityFact(
				t, sid, 3, model.NewTimestamp(initial.Time().Add(time.Hour)),
			),
		},
	)
	if err != nil {
		t.Fatalf("new Claim authority snapshot: %v", err)
	}
	scope := &communicationTxCapabilityScope{
		nowSequence: []model.Timestamp{initial, initial, initial},
	}
	tx, err := newCommunicationTxWithClaimAuthority(ctx, scope, claims)
	if err != nil {
		t.Fatalf("new Claim-bound transaction: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("Claim effect before authority lock = %v, want unavailable", err)
	}
	if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
		t.Fatalf("lock Claim authority snapshot: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
		err, errCommunicationTransactionUnavailable,
	) {
		t.Fatalf("Claim effect before DB refresh = %v, want unavailable", err)
	}
	if err := tx.refreshNow(ctx); err != nil {
		t.Fatalf("refresh Claim authority: %v", err)
	}
	if _, err := tx.appendAudit(ctx, model.AuditDraft{}); err != nil {
		t.Fatalf("Claim effect after refresh: %v", err)
	}
	if err := tx.finalizeClaimAuthority(ctx); err != nil {
		t.Fatalf("final Claim authority: %v", err)
	}
}

func TestCommunicationBoundAuthorityStatePoisonsRecoveredCapabilityPanics(t *testing.T) {
	t.Parallel()

	recoverPanic := func(t *testing.T, fn func()) {
		t.Helper()
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("capability callback did not panic")
			}
		}()
		fn()
	}
	t.Run("observation", func(t *testing.T) {
		state := newCommunicationBoundAuthorityState()
		recoverPanic(t, func() {
			_, _ = runCommunicationBoundAuthorityObservation(state, func() (bool, error) {
				panic("observation panic")
			})
		})
		if err := state.beginAuthorityLock(); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("authority after observation panic = %v, want unavailable", err)
		}
	})
	t.Run("local_lock", func(t *testing.T) {
		state := newCommunicationBoundAuthorityState()
		if err := state.beginAuthorityLock(); err != nil {
			t.Fatalf("begin authority lock: %v", err)
		}
		state.finishAuthorityLock(true)
		recoverPanic(t, func() {
			_ = runCommunicationBoundAuthorityLocalLock(state, func() error {
				panic("local lock panic")
			})
		})
		if err := state.beginRefresh(); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("refresh after local-lock panic = %v, want unavailable", err)
		}
	})
	t.Run("effect", func(t *testing.T) {
		state := newCommunicationBoundAuthorityState()
		if err := state.beginAuthorityLock(); err != nil {
			t.Fatalf("begin authority lock: %v", err)
		}
		state.finishAuthorityLock(true)
		if err := state.beginRefresh(); err != nil {
			t.Fatalf("begin refresh: %v", err)
		}
		state.finishRefresh(true)
		recoverPanic(t, func() {
			_, _ = runCommunicationBoundAuthorityEffect(state, func() (bool, error) {
				panic("effect panic")
			})
		})
		if err := state.beginFinalize(); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("finalize after effect panic = %v, want unavailable", err)
		}
	})
}

func TestCommunicationBoundAuthorityStateRejectsOverlappingPhaseTransitions(t *testing.T) {
	t.Parallel()

	t.Run("local_lock_and_refresh", func(t *testing.T) {
		state := newCommunicationBoundAuthorityState()
		if err := state.beginAuthorityLock(); err != nil {
			t.Fatalf("begin authority lock: %v", err)
		}
		state.finishAuthorityLock(true)
		started := make(chan struct{})
		release := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- runCommunicationBoundAuthorityLocalLock(state, func() error {
				close(started)
				<-release
				return nil
			})
		}()
		<-started
		if err := state.beginRefresh(); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("refresh overlapping local lock = %v, want unavailable", err)
		}
		close(release)
		if err := <-result; err != nil {
			t.Fatalf("complete local lock: %v", err)
		}
		if err := state.beginRefresh(); err != nil {
			t.Fatalf("refresh after local lock: %v", err)
		}
		state.finishRefresh(true)
	})
	t.Run("effect_and_finalize", func(t *testing.T) {
		state := newCommunicationBoundAuthorityState()
		if err := state.beginAuthorityLock(); err != nil {
			t.Fatalf("begin authority lock: %v", err)
		}
		state.finishAuthorityLock(true)
		if err := state.beginRefresh(); err != nil {
			t.Fatalf("begin refresh: %v", err)
		}
		state.finishRefresh(true)
		started := make(chan struct{})
		release := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			_, err := runCommunicationBoundAuthorityEffect(state, func() (bool, error) {
				close(started)
				<-release
				return true, nil
			})
			result <- err
		}()
		<-started
		if err := state.beginFinalize(); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("finalization overlapping effect = %v, want unavailable", err)
		}
		close(release)
		if err := <-result; err != nil {
			t.Fatalf("complete effect: %v", err)
		}
		if err := state.beginFinalize(); err != nil {
			t.Fatalf("finalize after effect: %v", err)
		}
		state.finishFinalize(true)
	})
}

func TestCommunicationBoundAuthorityTransactionPoisonsRecoveredCapabilityPanics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	initial := model.NewTimestamp(time.Date(2026, time.August, 16, 10, 50, 0, 0, time.UTC))
	request := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
		}},
		observedAt: initial.Time().Add(-time.Minute),
		freshUntil: initial.Time().Add(time.Hour),
	}
	recoverPanic := func(t *testing.T, fn func()) {
		t.Helper()
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("capability callback did not panic")
			}
		}()
		fn()
	}
	t.Run("authority_snapshot", func(t *testing.T) {
		tx, err := newCommunicationTxWithAuthority(
			ctx,
			&communicationTxCapabilityScope{now: initial, authorityPanic: true},
			request,
			CommunicationClaimAuthoritySnapshot{},
		)
		if err != nil {
			t.Fatalf("new request-bound transaction: %v", err)
		}
		recoverPanic(t, func() { _ = tx.lockAuthoritySnapshot(ctx, nil) })
		if err := tx.lockTransaction(ctx, "after-authority-panic"); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("local lock after authority panic = %v, want unavailable", err)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		tx, err := newCommunicationTxWithAuthority(
			ctx,
			&communicationTxCapabilityScope{now: initial, nowPanicAt: 2},
			request,
			CommunicationClaimAuthoritySnapshot{},
		)
		if err != nil {
			t.Fatalf("new request-bound transaction: %v", err)
		}
		if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
			t.Fatalf("lock request authority: %v", err)
		}
		recoverPanic(t, func() { _ = tx.refreshNow(ctx) })
		if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("effect after refresh panic = %v, want unavailable", err)
		}
	})
	t.Run("final", func(t *testing.T) {
		tx, err := newCommunicationTxWithAuthority(
			ctx,
			&communicationTxCapabilityScope{now: initial, nowPanicAt: 3},
			request,
			CommunicationClaimAuthoritySnapshot{},
		)
		if err != nil {
			t.Fatalf("new request-bound transaction: %v", err)
		}
		if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
			t.Fatalf("lock request authority: %v", err)
		}
		if err := tx.refreshNow(ctx); err != nil {
			t.Fatalf("refresh request authority: %v", err)
		}
		recoverPanic(t, func() { _ = tx.finalizeAuthority(ctx) })
		if _, err := tx.appendAudit(ctx, model.AuditDraft{}); !errors.Is(
			err, errCommunicationTransactionUnavailable,
		) {
			t.Fatalf("effect after finalization panic = %v, want unavailable", err)
		}
	})
}

func TestCommunicationRequestAuthorityRejectsReenteredModuleMutationCallback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fixture := communicationOpenFixtureWithClock(
		t,
		communicationSchemaBackend{
			name:       "sqlite-request-authority-reentry",
			engineName: store.EngineSQLite,
			dsn:        filepath.Join(t.TempDir(), "request-authority-reentry.db"),
		},
		model.SystemClock{},
	)
	now := time.Now().UTC()
	request := communicationObservedRequestAuthoritySnapshot(
		t, fixture, now.Add(-time.Minute), now.Add(time.Minute),
	)
	reentering := &reenteringCommunicationModuleData{inner: fixture.m.data}
	fixture.m.data = reentering
	callbackCalls := 0
	err := fixture.m.mutateCommunicationTransaction(
		ctx,
		DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace},
		request,
		CommunicationClaimAuthoritySnapshot{},
		func(tx *communicationTx) error {
			callbackCalls++
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				return err
			}
			return tx.refreshNow(ctx)
		},
	)
	if !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("reentered bound mutation error = %v, want unavailable", err)
	}
	if reentering.callbackEntries != 2 || callbackCalls != 1 {
		t.Fatalf("ModuleData/user callback entries = %d/%d, want 2/1",
			reentering.callbackEntries, callbackCalls)
	}
}

func TestCommunicationTxRequestAuthorityRejectsFactConflictBeforeStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := model.NewTimestamp(time.Date(2026, time.August, 16, 11, 0, 0, 0, time.UTC))
	tenant := model.NewTenantID()
	requestFact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 7,
	}
	request := communicationRequestAuthoritySnapshot{
		facts:      []store.AuthorizationFactRef{requestFact},
		observedAt: now.Time().Add(-time.Minute), freshUntil: now.Time().Add(time.Minute),
	}
	scope := &communicationTxCapabilityScope{now: now}
	tx, err := newCommunicationTxWithAuthority(
		ctx, scope, request, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		t.Fatalf("new request-bound transaction: %v", err)
	}
	conflict := requestFact
	conflict.Version++
	err = tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{conflict})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("conflicting authority fact error = %v, want UNKNOWN", err)
	}
	if len(scope.authorityRefs) != 0 {
		t.Fatalf("conflicting union called store locker %d times, want zero", len(scope.authorityRefs))
	}
	if err := tx.lockAuthoritySnapshot(ctx, nil); !errors.Is(
		err, errCommunicationAuthoritySnapshotAlreadyAttempted,
	) {
		t.Fatalf("retry after conflicting union = %v, want already attempted", err)
	}
}

func TestCommunicationTxRequestAuthorityRejectsUnionAboveFactCapBeforeStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := model.NewTimestamp(time.Date(2026, time.August, 16, 11, 30, 0, 0, time.UTC))
	tenant := model.NewTenantID()
	requestFacts := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 7,
	}}
	for range 31 {
		requestFacts = append(requestFacts, store.AuthorizationFactRef{
			Kind: model.Kind("core.identity"), ID: model.NewID(), Version: 1,
		})
	}
	requestFacts, err := CanonicalAuthorizationFacts(requestFacts)
	if err != nil {
		t.Fatalf("canonical request authority facts: %v", err)
	}
	localFacts := make([]store.AuthorizationFactRef, 0, 33)
	for range 33 {
		localFacts = append(localFacts, store.AuthorizationFactRef{
			Kind: model.Kind("core.membership"), ID: model.NewID(), Version: 1,
		})
	}
	request := communicationRequestAuthoritySnapshot{
		facts: requestFacts, observedAt: now.Time().Add(-time.Minute),
		freshUntil: now.Time().Add(time.Minute),
	}
	scope := &communicationTxCapabilityScope{now: now}
	tx, err := newCommunicationTxWithAuthority(
		ctx, scope, request, CommunicationClaimAuthoritySnapshot{},
	)
	if err != nil {
		t.Fatalf("new request-bound transaction: %v", err)
	}
	if err := tx.lockAuthoritySnapshot(ctx, localFacts); !errors.Is(
		err, ErrInvalidCommunicationModel,
	) {
		t.Fatalf("65-fact authority union error = %v, want invalid model", err)
	}
	if len(scope.authorityRefs) != 0 {
		t.Fatalf("65-fact authority union called store locker %d times, want zero",
			len(scope.authorityRefs))
	}
}

func TestCommunicationTxRequestAuthorityRejectsDBTimeOutsideEvidenceWindow(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	tenant := model.NewTenantID()
	fact := store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 1,
	}
	for _, test := range []struct {
		name       string
		observedAt time.Time
		freshUntil time.Time
		times      []model.Timestamp
		finalOnly  bool
	}{
		{
			name: "post_lock_before_observed_at", observedAt: base.Add(2 * time.Minute),
			freshUntil: base.Add(5 * time.Minute),
			times: []model.Timestamp{
				model.NewTimestamp(base), model.NewTimestamp(base.Add(time.Minute)),
			},
		},
		{
			name: "post_lock_at_fresh_until", observedAt: base.Add(-time.Minute),
			freshUntil: base.Add(time.Minute),
			times: []model.Timestamp{
				model.NewTimestamp(base), model.NewTimestamp(base.Add(time.Minute)),
			},
		},
		{
			name: "final_before_observed_at", observedAt: base.Add(-time.Minute),
			freshUntil: base.Add(5 * time.Minute), finalOnly: true,
			times: []model.Timestamp{
				model.NewTimestamp(base), model.NewTimestamp(base.Add(time.Minute)),
				model.NewTimestamp(base.Add(-2 * time.Minute)),
			},
		},
		{
			name: "final_at_fresh_until", observedAt: base.Add(-time.Minute),
			freshUntil: base.Add(2 * time.Minute), finalOnly: true,
			times: []model.Timestamp{
				model.NewTimestamp(base), model.NewTimestamp(base.Add(time.Minute)),
				model.NewTimestamp(base.Add(2 * time.Minute)),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scope := &communicationTxCapabilityScope{nowSequence: test.times}
			tx, err := newCommunicationTxWithAuthority(
				context.Background(), scope,
				communicationRequestAuthoritySnapshot{
					facts:      []store.AuthorizationFactRef{fact},
					observedAt: test.observedAt, freshUntil: test.freshUntil,
				},
				CommunicationClaimAuthoritySnapshot{},
			)
			if err != nil {
				t.Fatalf("new request-bound transaction: %v", err)
			}
			if err := tx.lockAuthoritySnapshot(context.Background(), nil); err != nil {
				t.Fatalf("lock request authority: %v", err)
			}
			refreshErr := tx.refreshNow(context.Background())
			if !test.finalOnly {
				if !errors.Is(refreshErr, ErrCommunicationEvidenceUnknown) {
					t.Fatalf("post-lock window error = %v, want UNKNOWN", refreshErr)
				}
				return
			}
			if refreshErr != nil {
				t.Fatalf("post-lock authority should still be current: %v", refreshErr)
			}
			if err := tx.finalizeAuthority(context.Background()); !errors.Is(
				err, ErrCommunicationEvidenceUnknown,
			) {
				t.Fatalf("final window error = %v, want UNKNOWN", err)
			}
		})
	}
}

func TestCommunicationTxRejectsSuccessfulZeroDBTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := model.NewTimestamp(time.Date(2026, time.August, 16, 13, 0, 0, 0, time.UTC))
	request := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
		}},
		observedAt: base.Time().Add(-time.Minute), freshUntil: base.Time().Add(time.Hour),
	}

	if tx, err := newCommunicationTxWithAuthority(
		ctx, &communicationTxCapabilityScope{}, request, CommunicationClaimAuthoritySnapshot{},
	); tx != nil || !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("zero initial DB time transaction/error = %#v/%v, want unavailable", tx, err)
	}

	for _, test := range []struct {
		name      string
		sequence  []model.Timestamp
		finalOnly bool
	}{
		{
			name:     "post_lock_zero",
			sequence: []model.Timestamp{base, {}},
		},
		{
			name: "final_zero", finalOnly: true,
			sequence: []model.Timestamp{base, base, {}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := newCommunicationTxWithAuthority(
				ctx, &communicationTxCapabilityScope{nowSequence: test.sequence}, request,
				CommunicationClaimAuthoritySnapshot{},
			)
			if err != nil {
				t.Fatalf("new request-bound transaction: %v", err)
			}
			if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
				t.Fatalf("lock request authority: %v", err)
			}
			refreshErr := tx.refreshNow(ctx)
			if !test.finalOnly {
				if !errors.Is(refreshErr, errCommunicationTransactionUnavailable) {
					t.Fatalf("zero post-lock DB time error = %v, want unavailable", refreshErr)
				}
				return
			}
			if refreshErr != nil {
				t.Fatalf("post-lock refresh: %v", refreshErr)
			}
			if err := tx.finalizeAuthority(ctx); !errors.Is(
				err, errCommunicationTransactionUnavailable,
			) {
				t.Fatalf("zero final DB time error = %v, want unavailable", err)
			}
		})
	}
}

type blockingCommunicationAuthorityScope struct {
	*communicationTxCapabilityScope
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *blockingCommunicationAuthorityScope) LockAuthoritySnapshot(
	context.Context,
	[]store.AuthorizationFactRef,
) error {
	s.calls.Add(1)
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

func TestCommunicationTxLocksAuthorityAtMostOnceConcurrently(t *testing.T) {
	t.Parallel()

	base := &communicationTxCapabilityScope{
		now: model.NewTimestamp(time.Date(2026, time.August, 15, 10, 30, 0, 0, time.UTC)),
	}
	scope := &blockingCommunicationAuthorityScope{
		communicationTxCapabilityScope: base,
		entered:                        make(chan struct{}, 2),
		release:                        make(chan struct{}),
	}
	tx, err := newCommunicationTx(context.Background(), scope)
	if err != nil {
		t.Fatalf("new communication transaction: %v", err)
	}
	refs := []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.NewID(), Version: 1,
	}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			// Call the retained field directly: the one-shot guarantee must live
			// inside this closure rather than be bypassable through package access.
			results <- tx.lockAuthoritySnapshotFn(context.Background(), refs)
		}()
	}
	close(start)
	<-scope.entered
	close(scope.release)
	var clean, rejected int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			clean++
		case errors.Is(err, errCommunicationAuthoritySnapshotAlreadyAttempted) &&
			errors.Is(err, errCommunicationTransactionUnavailable):
			rejected++
		default:
			t.Fatalf("concurrent authority error = %v", err)
		}
	}
	if clean != 1 || rejected != 1 {
		t.Fatalf("authority results clean/rejected = %d/%d, want 1/1", clean, rejected)
	}
	if got := scope.calls.Load(); got != 1 {
		t.Fatalf("authority locker calls = %d, want exactly one", got)
	}
}

type communicationTxBareRepo struct{ store.GenericRepo }

type communicationTxStampedOnlyRepo struct {
	store.TransactionStampedGenericRepo
}

type communicationTxRowOnlyRepo struct{ store.GenericRepo }

type communicationOrdinaryCreator interface {
	Create(context.Context, model.Record) (model.Record, error)
}

type communicationOrdinaryDeleter interface {
	Delete(context.Context, model.ID) error
}

func (communicationTxRowOnlyRepo) Lock(
	context.Context,
	model.ID,
) (model.Record, error) {
	return nil, nil
}

func TestCommunicationTxRepoRequiresStampedWritesAndRowLock(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		repo       store.GenericRepo
		capability string
	}{
		{
			name: "neither", repo: communicationTxBareRepo{},
			capability: "transaction-stamped repository",
		},
		{
			name: "row_lock_only", repo: communicationTxRowOnlyRepo{},
			capability: "transaction-stamped repository",
		},
		{
			name: "stamped_only", repo: communicationTxStampedOnlyRepo{},
			capability: "row locker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := &communicationTxCapabilityScope{
				now:  model.NewTimestamp(time.Date(2026, time.August, 15, 11, 0, 0, 0, time.UTC)),
				repo: tc.repo,
			}
			tx, err := newCommunicationTx(context.Background(), scope)
			if err != nil {
				t.Fatalf("new communication transaction: %v", err)
			}
			got, err := tx.repo(channelKind)
			if got != nil {
				t.Fatalf("repository = %#v, want nil", got)
			}
			if !errors.Is(err, errCommunicationTransactionUnavailable) ||
				!errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				!strings.Contains(err.Error(), tc.capability) {
				t.Fatalf("repository error = %v, want deny-closed %q", err, tc.capability)
			}
		})
	}

	scope := &communicationTxCapabilityScope{
		now: model.NewTimestamp(time.Date(2026, time.August, 15, 11, 30, 0, 0, time.UTC)),
	}
	tx, err := newCommunicationTx(context.Background(), scope)
	if err != nil {
		t.Fatalf("new communication transaction: %v", err)
	}
	if got, err := tx.repo(model.Kind("sessions.work_item")); got != nil ||
		!errors.Is(err, errCommunicationTransactionUnavailable) ||
		!strings.Contains(err.Error(), "outside the K3 communication inventory") {
		t.Fatalf("non-communication repository = %#v, %v; want deny-closed", got, err)
	}
}

func TestCommunicationRepositoryInventoryIsExactlyTwentyTwoKinds(t *testing.T) {
	t.Parallel()

	want := map[model.Kind]struct{}{
		channelKind: {}, channelGrantKind: {}, channelSubscriptionKind: {},
		channelLabelDefinitionKind: {}, channelRouteKind: {}, communicationEndpointKind: {},
		messageKind: {}, messageAudienceKind: {}, messageAudienceRecipientKind: {},
		messageDeliveryKind: {}, inboxCursorKind: {}, inboxCursorBarrierKind: {},
		messageAckKind: {}, communicationGuardKind: {}, decisionRequestKind: {},
		decisionResponseKind: {}, handoffKind: {}, deliveryDispatchKind: {},
		deliveryAttemptKind: {}, communicationCommandKind: {}, workEventKind: {},
		workOutboxKind: {},
	}
	inventory := communicationRepositoryInventory()
	if len(inventory) != 22 || len(want) != 22 {
		t.Fatalf("communication repository inventory/want lengths = %d/%d, want 22/22",
			len(inventory), len(want))
	}
	seen := make(map[model.Kind]struct{}, len(inventory))
	for _, kind := range inventory {
		if _, duplicate := seen[kind]; duplicate {
			t.Fatalf("communication repository kind %q appears twice", kind)
		}
		seen[kind] = struct{}{}
		if _, ok := want[kind]; !ok {
			t.Fatalf("unexpected communication repository kind %q", kind)
		}
		if !validCommunicationRepositoryKind(kind) {
			t.Fatalf("declared communication repository kind %q is denied", kind)
		}
	}
	for kind := range want {
		if _, ok := seen[kind]; !ok {
			t.Fatalf("required communication repository kind %q is absent", kind)
		}
	}
	for _, denied := range []model.Kind{
		workItemKind, workCommandKind, workLeaseKind, model.Kind("sessions.unknown"), "",
	} {
		if validCommunicationRepositoryKind(denied) {
			t.Fatalf("non-inventory repository kind %q was allowed", denied)
		}
	}
}

func TestCommunicationTxSQLiteConfinementPreservesReaderAndExactStamp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	appNow := time.Date(2001, time.January, 2, 3, 4, 5, 0, time.UTC)
	fixture := communicationOpenFixtureWithClock(t, communicationSchemaBackend{
		name:       "sqlite-communication-tx",
		engineName: store.EngineSQLite,
		dsn:        filepath.Join(t.TempDir(), "communication-tx.db"),
	}, &testClock{now: appNow})

	var (
		dbNow       model.Timestamp
		createdID   model.ID
		assignedID  = model.NewID()
		createdName = "Channel transaction create"
		updatedName = "Channel transaction updated"
	)
	directoryScope := DirectoryScopeRef{
		TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
	}
	err := fixture.m.mutateCommunication(ctx, directoryScope, func(tx *communicationTx) error {
		dbNow = tx.now
		if _, ok := tx.directorySnapshotReader().(store.Scope); ok {
			t.Fatal("narrow directory adapter exposes the original store.Scope")
		}
		epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		if epoch.TenantID != fixture.tenant || epoch.Version < 1 {
			t.Fatalf("confined directory epoch = %+v, want tenant %s and version >= 1",
				epoch, fixture.tenant)
		}
		if err := tx.lockTransaction(ctx, "communication_tx_test|"+fixture.tenant.String()); err != nil {
			return err
		}
		for _, kind := range []model.Kind{channelKind, workEventKind, workOutboxKind} {
			narrowRepo, repoErr := tx.repo(kind)
			if repoErr != nil {
				return repoErr
			}
			if _, ok := narrowRepo.(store.GenericRepo); ok {
				t.Fatalf("narrow repository %q exposes GenericRepo", kind)
			}
			if _, ok := narrowRepo.(communicationOrdinaryCreator); ok {
				t.Fatalf("narrow repository %q exposes ordinary Create", kind)
			}
			if _, ok := narrowRepo.(communicationOrdinaryDeleter); ok {
				t.Fatalf("narrow repository %q exposes Delete", kind)
			}
		}

		first := communicationChannelRecord(fixture.workspace, "tx-create")
		first[colCommName] = createdName
		created, err := tx.create(ctx, channelKind, first)
		if err != nil {
			return err
		}
		createdID = model.ID(created.String(model.ColID))
		if created.String(model.ColCreatedAt) != dbNow.String() ||
			created.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("Create stamps = %s/%s, want DBNow %s",
				created.String(model.ColCreatedAt), created.String(model.ColUpdatedAt), dbNow.String())
		}
		if created.String(model.ColCreatedAt) == model.NewTimestamp(appNow).String() {
			t.Fatal("Create used the injected application clock instead of DBNow")
		}

		second := communicationChannelRecord(fixture.workspace, "tx-create-with-id")
		assigned, err := tx.createWithID(ctx, channelKind, assignedID, second)
		if err != nil {
			return err
		}
		if model.ID(assigned.String(model.ColID)) != assignedID ||
			assigned.String(model.ColCreatedAt) != dbNow.String() ||
			assigned.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("CreateWithID id/stamps = %s/%s/%s, want %s/%s/%s",
				assigned.String(model.ColID), assigned.String(model.ColCreatedAt),
				assigned.String(model.ColUpdatedAt), assignedID, dbNow.String(), dbNow.String())
		}

		locked, err := tx.lockRecord(ctx, channelKind, createdID)
		if err != nil {
			return err
		}
		locked[colCommName] = updatedName
		updated, err := tx.update(ctx, channelKind, locked)
		if err != nil {
			return err
		}
		if updated.String(model.ColCreatedAt) != dbNow.String() ||
			updated.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("Update stamps = %s/%s, want DBNow %s",
				updated.String(model.ColCreatedAt), updated.String(model.ColUpdatedAt), dbNow.String())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("confined communication transaction: %v", err)
	}

	if err := fixture.m.viewCommunication(ctx, directoryScope, func(sc store.Scope) error {
		repo, err := sc.Ext(channelKind)
		if err != nil {
			return err
		}
		created, err := repo.Get(ctx, createdID)
		if err != nil {
			return err
		}
		if created.String(colCommName) != updatedName ||
			created.String(model.ColCreatedAt) != dbNow.String() ||
			created.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("persisted updated row = name %q stamps %s/%s, want %q/%s/%s",
				created.String(colCommName), created.String(model.ColCreatedAt),
				created.String(model.ColUpdatedAt), updatedName, dbNow.String(), dbNow.String())
		}
		assigned, err := repo.Get(ctx, assignedID)
		if err != nil {
			return err
		}
		if assigned.String(model.ColCreatedAt) != dbNow.String() ||
			assigned.String(model.ColUpdatedAt) != dbNow.String() {
			t.Fatalf("persisted assigned stamps = %s/%s, want %s",
				assigned.String(model.ColCreatedAt), assigned.String(model.ColUpdatedAt), dbNow.String())
		}
		return nil
	}); err != nil {
		t.Fatalf("read persisted transaction-stamped rows: %v", err)
	}
}

func communicationObservedClaimAuthority(
	t *testing.T,
	fixture communicationSchemaFixture,
	lease Lease,
) (CommunicationClaimAuthoritySnapshot, model.Record) {
	t.Helper()
	ctx := context.Background()
	var row model.Record
	if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
		var found bool
		var err error
		row, found, err = findClaim(ctx, sc, lease.SID)
		if err == nil && !found {
			err = errors.New("observed Claim row is absent")
		}
		return err
	}); err != nil {
		t.Fatalf("observe Claim authority row: %v", err)
	}
	deadline, err := model.ParseTimestamp(row.String(colLeaseExpires))
	if err != nil {
		t.Fatalf("parse observed Claim deadline: %v", err)
	}
	fact, err := store.NewLeaseFenceAuthorizationFactRef(
		claimKind, model.ID(row.String(model.ColID)), row.Int(model.ColVersion),
		row.String(colClaimSID), row.Int(colFence), deadline,
	)
	if err != nil {
		t.Fatalf("build observed Claim authority fact: %v", err)
	}
	snapshot, err := NewCommunicationClaimAuthoritySnapshot(
		[]CommunicationClaimRef{{SessionSID: lease.SID, Fence: lease.Fence}},
		[]store.AuthorizationFactRef{fact},
	)
	if err != nil {
		t.Fatalf("build observed Claim authority snapshot: %v", err)
	}
	return snapshot, row
}

func communicationLockClaimSnapshot(
	ctx context.Context,
	tx *communicationTx,
) error {
	epoch, err := tx.directorySnapshotReader().ReadDirectoryEpoch(ctx)
	if err != nil {
		return err
	}
	return tx.lockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version,
	}})
}

func TestCommunicationClaimAuthorityTouchRollbackAndConfinement(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixtureWithClock(t, backend, model.SystemClock{})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sid := "osn_" + model.NewID().String()
			lease, err := fixture.m.Claim(ctx, fixture.tenant, sid, "claim-touch-holder", time.Hour)
			if err != nil {
				t.Fatalf("create communication Claim: %v", err)
			}
			snapshot, before := communicationObservedClaimAuthority(t, fixture, lease)
			scope := DirectoryScopeRef{
				TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			}

			rollbackCause := errors.New("rollback after Claim OCC touch")
			var rolledBackChannel model.ID
			err = fixture.m.mutateCommunicationWithClaimAuthority(
				ctx, scope, snapshot, func(tx *communicationTx) error {
					if _, ok := any(tx).(store.Scope); ok {
						t.Fatal("communicationTx exposed store.Scope after confinement")
					}
					if _, err := tx.repo(claimKind); err == nil {
						t.Fatal("communicationTx exposed the raw Claim repository")
					}
					if err := communicationLockClaimSnapshot(ctx, tx); err != nil {
						return err
					}
					if err := tx.refreshNow(ctx); err != nil {
						return err
					}
					created, err := tx.create(
						ctx, channelKind, communicationChannelRecord(fixture.workspace, "claim-rollback"),
					)
					if err == nil {
						rolledBackChannel = model.ID(created.String(model.ColID))
					}
					if err != nil {
						return err
					}
					return rollbackCause
				},
			)
			if !errors.Is(err, rollbackCause) {
				t.Fatalf("Claim touch rollback error = %v, want cause", err)
			}

			assertClaimAndChannel := func(wantVersion int64, channel model.ID, channelExists bool) {
				t.Helper()
				if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
					claimRepo, err := sc.Ext(claimKind)
					if err != nil {
						return err
					}
					row, err := claimRepo.Get(ctx, model.ID(before.String(model.ColID)))
					if err != nil {
						return err
					}
					if row.Int(model.ColVersion) != wantVersion ||
						row.String(colClaimSID) != before.String(colClaimSID) ||
						row.Int(colFence) != before.Int(colFence) ||
						row.String(colLeaseExpires) != before.String(colLeaseExpires) {
						t.Fatalf("Claim after touch = %v, want semantic fields unchanged/version %d",
							row, wantVersion)
					}
					if channel.IsZero() {
						return nil
					}
					channels, err := sc.Ext(channelKind)
					if err != nil {
						return err
					}
					_, getErr := channels.Get(ctx, channel)
					if channelExists && getErr != nil {
						return getErr
					}
					if !channelExists && !errors.Is(getErr, store.ErrNotFound) {
						return fmt.Errorf("rolled-back Channel lookup error = %v", getErr)
					}
					return nil
				}); err != nil {
					t.Fatalf("inspect Claim/channel atomicity: %v", err)
				}
			}
			assertClaimAndChannel(before.Int(model.ColVersion), rolledBackChannel, false)

			var committedChannel model.ID
			err = fixture.m.mutateCommunicationWithClaimAuthority(
				ctx, scope, snapshot, func(tx *communicationTx) error {
					if err := communicationLockClaimSnapshot(ctx, tx); err != nil {
						return err
					}
					if err := tx.refreshNow(ctx); err != nil {
						return err
					}
					created, err := tx.create(
						ctx, channelKind, communicationChannelRecord(fixture.workspace, "claim-commit"),
					)
					if err == nil {
						committedChannel = model.ID(created.String(model.ColID))
					}
					return err
				},
			)
			if err != nil {
				t.Fatalf("commit Claim OCC touch and communication effect: %v", err)
			}
			assertClaimAndChannel(before.Int(model.ColVersion)+1, committedChannel, true)
		})
	}
}

func TestCommunicationClaimAuthorityFinalDBTimeRollsBackExpiredEffect(t *testing.T) {
	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixtureWithClock(t, backend, model.SystemClock{})
			sid := "osn_" + model.NewID().String()
			lease, err := fixture.m.Claim(
				context.Background(), fixture.tenant, sid, "claim-expiry-holder", time.Hour,
			)
			if err != nil {
				t.Fatalf("create communication Claim: %v", err)
			}
			snapshot, before := communicationObservedClaimAuthority(t, fixture, lease)
			scope := DirectoryScopeRef{
				TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			}
			// The SQL locker below still samples the real engine clock and proves
			// this Claim is live. Only communicationTx's post-lock observations are
			// advanced, deterministically modeling time passing after the OCC touch.
			finalClock := &testClock{now: lease.ExpiresAt.Add(-time.Minute)}
			fixture.m.data = &communicationGuardRollbackClockData{
				inner: fixture.m.data, clock: finalClock,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var provisionalChannel model.ID
			err = fixture.m.mutateCommunicationWithClaimAuthority(
				ctx, scope, snapshot, func(tx *communicationTx) error {
					if err := communicationLockClaimSnapshot(ctx, tx); err != nil {
						return err
					}
					if err := tx.refreshNow(ctx); err != nil {
						return err
					}
					created, err := tx.create(
						ctx, channelKind, communicationChannelRecord(fixture.workspace, "claim-expired"),
					)
					if err != nil {
						return err
					}
					provisionalChannel = model.ID(created.String(model.ColID))
					finalClock.set(lease.ExpiresAt)
					return nil
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("final expired Claim error = %v, want UNKNOWN", err)
			}
			if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
				claimRepo, err := sc.Ext(claimKind)
				if err != nil {
					return err
				}
				row, err := claimRepo.Get(ctx, model.ID(before.String(model.ColID)))
				if err != nil {
					return err
				}
				if row.Int(model.ColVersion) != before.Int(model.ColVersion) {
					t.Fatalf("expired final check committed Claim touch version %d, want %d",
						row.Int(model.ColVersion), before.Int(model.ColVersion))
				}
				channels, err := sc.Ext(channelKind)
				if err != nil {
					return err
				}
				if _, err := channels.Get(ctx, provisionalChannel); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("expired final check persisted Channel: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatalf("inspect final freshness rollback: %v", err)
			}
		})
	}
}

func communicationObservedRequestAuthoritySnapshot(
	t *testing.T,
	fixture communicationSchemaFixture,
	observedAt time.Time,
	freshUntil time.Time,
) communicationRequestAuthoritySnapshot {
	t.Helper()
	ctx := context.Background()
	var epoch model.DirectoryEpoch
	if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("request authority fixture lacks directory snapshot reader")
		}
		var err error
		epoch, err = reader.ReadDirectoryEpoch(ctx)
		return err
	}); err != nil {
		t.Fatalf("observe request authority directory epoch: %v", err)
	}
	snapshot := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version,
		}},
		observedAt: observedAt, freshUntil: freshUntil,
	}
	if err := snapshot.validate(); err != nil {
		t.Fatalf("build request authority snapshot: %v", err)
	}
	return snapshot
}

// communicationRequestRollbackClockData keeps the SQL transaction's own
// clock observation intact while returning a deterministic time to the
// communication transaction. The raw observation is load-bearing for stamped
// writes and audit appends; replacing it outright would test a malformed store
// capability rather than the request-authority freshness boundary.
type communicationRequestRollbackClockData struct {
	inner api.ModuleData
	clock *testClock
}

func (d *communicationRequestRollbackClockData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *communicationRequestRollbackClockData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(raw store.Scope) error {
		rawClock, hasClock := raw.(store.TransactionClock)
		locker, hasLocker := raw.(store.TransactionLocker)
		authority, hasAuthority := raw.(store.AuthoritySnapshotLocker)
		directory, hasDirectory := raw.(store.DirectorySnapshotReader)
		if !hasClock || !hasLocker || !hasAuthority || !hasDirectory {
			return errors.New("request rollback clock fixture lacks required SQL capabilities")
		}
		return fn(&communicationRequestRollbackClockScope{
			communicationGuardRollbackClockScope: &communicationGuardRollbackClockScope{
				Scope: raw, clock: d.clock, locker: locker,
				authority: authority, directory: directory,
			},
			rawClock: rawClock,
		})
	})
}

type communicationRequestRollbackClockScope struct {
	*communicationGuardRollbackClockScope
	rawClock store.TransactionClock
}

var _ store.TransactionClock = (*communicationRequestRollbackClockScope)(nil)

func (s *communicationRequestRollbackClockScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	if _, err := s.rawClock.TransactionNow(ctx); err != nil {
		return model.Timestamp{}, err
	}
	return s.clock.Now(), nil
}

func TestCommunicationRequestAuthorityFinalDBTimeRollsBackEffect(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixture := communicationOpenFixtureWithClock(t, backend, model.SystemClock{})
			baseData := fixture.m.data
			for _, test := range []struct {
				name      string
				finalTime func(time.Time, time.Time) time.Time
			}{
				{
					name: "at_fresh_until",
					finalTime: func(_ time.Time, freshUntil time.Time) time.Time {
						return freshUntil
					},
				},
				{
					name: "before_observed_at",
					finalTime: func(observedAt time.Time, _ time.Time) time.Time {
						return observedAt.Add(-time.Nanosecond)
					},
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					base := time.Now().UTC()
					observedAt := base.Add(-time.Minute)
					freshUntil := base.Add(time.Minute)
					snapshot := communicationObservedRequestAuthoritySnapshot(
						t, fixture, observedAt, freshUntil,
					)
					finalClock := &testClock{now: base}
					fixture.m.data = &communicationRequestRollbackClockData{
						inner: baseData, clock: finalClock,
					}
					scope := DirectoryScopeRef{
						TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					}
					var provisionalChannel model.ID
					err := fixture.m.mutateCommunicationTransaction(
						ctx, scope, snapshot, CommunicationClaimAuthoritySnapshot{},
						func(tx *communicationTx) error {
							if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
								return err
							}
							if err := tx.refreshNow(ctx); err != nil {
								return err
							}
							created, err := tx.create(
								ctx, channelKind,
								communicationChannelRecord(fixture.workspace, "request-window-rollback"),
							)
							if err != nil {
								return err
							}
							provisionalChannel = model.ID(created.String(model.ColID))
							finalClock.set(test.finalTime(observedAt, freshUntil))
							return nil
						},
					)
					if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
						t.Fatalf("final request authority window error = %v, want UNKNOWN", err)
					}
					if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
						channels, err := sc.Ext(channelKind)
						if err != nil {
							return err
						}
						if _, err := channels.Get(ctx, provisionalChannel); !errors.Is(err, store.ErrNotFound) {
							t.Fatalf("failed final request check persisted Channel: %v", err)
						}
						return nil
					}); err != nil {
						t.Fatalf("inspect request authority rollback: %v", err)
					}
				})
			}
		})
	}
}

func TestCommunicationRequestAuthorityRepositoryEffectsRequireRefreshedPhase(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			fixture := communicationOpenFixtureWithClock(t, backend, model.SystemClock{})
			base := time.Now().UTC()
			snapshot := communicationObservedRequestAuthoritySnapshot(
				t, fixture, base.Add(-time.Minute), base.Add(5*time.Minute),
			)
			scope := DirectoryScopeRef{
				TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			}
			var createdID model.ID
			err := fixture.m.mutateCommunicationTransaction(
				ctx, scope, snapshot, CommunicationClaimAuthoritySnapshot{},
				func(tx *communicationTx) error {
					if _, err := tx.createWithID(
						ctx, channelKind,
						model.NewID(),
						communicationChannelRecord(fixture.workspace, "request-before-authority"),
					); !errors.Is(err, errCommunicationTransactionUnavailable) {
						return fmt.Errorf("effect before authority snapshot = %v, want unavailable", err)
					}
					if err := tx.lockAuthoritySnapshot(ctx, nil); err != nil {
						return err
					}
					if _, err := tx.create(
						ctx, channelKind,
						communicationChannelRecord(fixture.workspace, "request-before-refresh"),
					); !errors.Is(err, errCommunicationTransactionUnavailable) {
						return fmt.Errorf("effect before DB refresh = %v, want unavailable", err)
					}
					if _, err := tx.update(
						ctx, channelKind, model.Record{},
					); !errors.Is(err, errCommunicationTransactionUnavailable) {
						return fmt.Errorf("update before DB refresh = %v, want unavailable", err)
					}
					if err := tx.refreshNow(ctx); err != nil {
						return err
					}
					created, err := tx.create(
						ctx, channelKind,
						communicationChannelRecord(fixture.workspace, "request-after-refresh"),
					)
					if err != nil {
						return err
					}
					createdID = model.ID(created.String(model.ColID))
					repo, err := tx.repo(channelKind)
					if err != nil {
						return err
					}
					if _, err := repo.Get(ctx, createdID); !errors.Is(
						err, errCommunicationTransactionUnavailable,
					) {
						return fmt.Errorf("late repository Get = %v, want unavailable", err)
					}
					if _, _, err := repo.List(ctx, model.Query{}); !errors.Is(
						err, errCommunicationTransactionUnavailable,
					) {
						return fmt.Errorf("late repository List = %v, want unavailable", err)
					}
					if _, err := tx.lockRecord(ctx, channelKind, createdID); !errors.Is(
						err, errCommunicationTransactionUnavailable,
					) {
						return fmt.Errorf("late row lock = %v, want unavailable", err)
					}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("request authority phase mutation: %v", err)
			}
			if err := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(channelKind)
				if err != nil {
					return err
				}
				_, err = repo.Get(ctx, createdID)
				return err
			}); err != nil {
				t.Fatalf("read committed post-refresh Channel: %v", err)
			}
		})
	}
}

func TestCommunicationTxAuditAppenderIsNarrowAndSharesMutation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := communicationOpenFixture(t, communicationSchemaBackend{
		name:       "sqlite-communication-audit",
		engineName: store.EngineSQLite,
		dsn:        filepath.Join(t.TempDir(), "communication-audit.db"),
	})
	directoryScope := DirectoryScopeRef{
		TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
	}
	counting := &countingCommunicationModuleData{inner: fixture.m.data}
	fixture.m.data = counting

	var committed model.AuditEvent
	err := fixture.m.mutateCommunication(ctx, directoryScope, func(tx *communicationTx) error {
		if _, ok := tx.audit.(store.AuditLog); ok {
			t.Fatal("narrow communication audit appender exposes AuditLog reads")
		}
		if _, ok := tx.audit.(store.Scope); ok {
			t.Fatal("narrow communication audit appender exposes store.Scope")
		}
		var appendErr error
		committed, appendErr = tx.appendAudit(ctx, model.AuditDraft{
			Actor:      "system",
			ActorKind:  model.ActorSystem,
			Action:     "sessions.communication.tx_test.commit",
			TargetKind: messageKind,
			TargetID:   model.NewID(),
			Meta: map[string]any{
				"workspace_id": fixture.workspace.String(),
				"result":       "committed",
			},
		})
		return appendErr
	})
	if err != nil {
		t.Fatalf("append communication audit in mutation: %v", err)
	}
	if committed.Seq < 1 || committed.TenantID != fixture.tenant || len(committed.Hash) == 0 {
		t.Fatalf("committed audit event = %+v, want durable tenant event", committed)
	}
	if counting.mutateCalls != 1 || counting.viewCalls != 0 {
		t.Fatalf("ModuleData mutate/view calls = %d/%d, want 1/0",
			counting.mutateCalls, counting.viewCalls)
	}

	readHead := func() store.HeadRef {
		t.Helper()
		var head store.HeadRef
		if viewErr := fixture.st.View(ctx, fixture.tenant, func(sc store.Scope) error {
			var found bool
			var headErr error
			head, found, headErr = sc.Audit().Head(ctx)
			if headErr == nil && !found {
				headErr = errors.New("communication audit head is absent")
			}
			return headErr
		}); viewErr != nil {
			t.Fatalf("read communication audit head: %v", viewErr)
		}
		return head
	}
	head := readHead()
	if head.Seq != committed.Seq || !bytes.Equal(head.Hash, committed.Hash) {
		t.Fatalf("audit head seq/hash = %d/%x, want committed %d/%x",
			head.Seq, head.Hash, committed.Seq, committed.Hash)
	}

	rollbackCause := errors.New("rollback communication audit mutation")
	var rolledBack model.AuditEvent
	err = fixture.m.mutateCommunication(ctx, directoryScope, func(tx *communicationTx) error {
		var appendErr error
		rolledBack, appendErr = tx.appendAudit(ctx, model.AuditDraft{
			Actor:      "system",
			ActorKind:  model.ActorSystem,
			Action:     "sessions.communication.tx_test.rollback",
			TargetKind: messageKind,
			TargetID:   model.NewID(),
		})
		if appendErr != nil {
			return appendErr
		}
		return rollbackCause
	})
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback mutation error = %v, want cause", err)
	}
	if rolledBack.Seq != committed.Seq+1 {
		t.Fatalf("rolled-back audit seq = %d, want provisional %d",
			rolledBack.Seq, committed.Seq+1)
	}
	head = readHead()
	if head.Seq != committed.Seq || !bytes.Equal(head.Hash, committed.Hash) {
		t.Fatalf("audit head changed across rollback: seq/hash = %d/%x, want %d/%x",
			head.Seq, head.Hash, committed.Seq, committed.Hash)
	}
	if counting.mutateCalls != 2 || counting.viewCalls != 0 {
		t.Fatalf("ModuleData mutate/view calls after rollback = %d/%d, want 2/0",
			counting.mutateCalls, counting.viewCalls)
	}
}

func TestMutateCommunicationRejectsForeignWorkspaceWithoutNestedTransaction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := communicationOpenFixture(t, communicationSchemaBackend{
		name:       "sqlite-communication-foreign-workspace",
		engineName: store.EngineSQLite,
		dsn:        filepath.Join(t.TempDir(), "communication-foreign-workspace.db"),
	})

	var (
		foreignTenant    model.TenantID
		foreignWorkspace model.ID
	)
	if err := fixture.st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{
			Name:   "Foreign communication tenant",
			Slug:   "foreign-communication-tenant",
			Status: model.StatusActive,
		})
		if err == nil {
			foreignTenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("create foreign tenant: %v", err)
	}
	if err := fixture.st.View(ctx, foreignTenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(ctx)
		if err == nil {
			foreignWorkspace = workspace.ID
		}
		return err
	}); err != nil {
		t.Fatalf("read foreign workspace: %v", err)
	}

	counting := &countingCommunicationModuleData{inner: fixture.m.data}
	fixture.m.data = counting
	callbackCalls := 0
	err := fixture.m.mutateCommunication(ctx, DirectoryScopeRef{
		TenantID: fixture.tenant, WorkspaceID: foreignWorkspace,
	}, func(*communicationTx) error {
		callbackCalls++
		return nil
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign workspace mutation error = %v, want tenant-hidden ErrNotFound", err)
	}
	if counting.mutateCalls != 1 || counting.viewCalls != 0 || callbackCalls != 0 {
		t.Fatalf("ModuleData mutate/view/callback calls = %d/%d/%d, want 1/0/0",
			counting.mutateCalls, counting.viewCalls, callbackCalls)
	}
}

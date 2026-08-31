// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type directNoticeCursorAuditFailureData struct {
	inner   api.ModuleData
	failure error
}

type directNoticeCursorAuditFailureScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	audit     store.AuditLog
	failure   error
}

type directNoticeCursorAuditFailureLog struct {
	store.AuditLog
	locker  store.AuditAppendLocker
	failure error
}

type directNoticeCursorReceiptFailureData struct {
	inner   api.ModuleData
	failure error
}

type directNoticeCursorReceiptFailureScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	failure   error
}

type directNoticeCursorReceiptFailureRepo struct {
	store.GenericRepo
	stamped store.TransactionStampedGenericRepo
	locker  store.RowLocker[model.Record]
	failure error
}

// directNoticeCursorReplayViewData hides a previously committed receipt from
// exactly the first outer View. The mutation must then find it under the
// idempotency lock, roll back with the replay sentinel and use a later View for
// reconstruction plus audit verification.
type directNoticeCursorReplayViewData struct {
	inner api.ModuleData
	audit store.AuditLog

	mu      sync.Mutex
	views   int
	mutates int
}

type directNoticeCursorReplayViewScope struct {
	store.Scope
	clock       store.TransactionClock
	hideReceipt bool
	audit       store.AuditLog
}

type directNoticeCursorHiddenReceiptRepo struct {
	store.GenericRepo
}

func (d directNoticeCursorAuditFailureData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d directNoticeCursorAuditFailureData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(scope store.Scope) error {
		clock, clockOK := scope.(store.TransactionClock)
		locker, lockerOK := scope.(store.TransactionLocker)
		authority, authorityOK := scope.(store.AuthoritySnapshotLocker)
		directory, directoryOK := scope.(store.DirectorySnapshotReader)
		_, auditOK := scope.Audit().(store.AuditAppendLocker)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK || !auditOK {
			return errors.New("cursor audit fault scope lacks transaction capabilities")
		}
		return fn(&directNoticeCursorAuditFailureScope{
			Scope: scope, clock: clock, locker: locker, authority: authority,
			directory: directory, audit: scope.Audit(), failure: d.failure,
		})
	})
}

func (s *directNoticeCursorAuditFailureScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeCursorAuditFailureScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeCursorAuditFailureScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *directNoticeCursorAuditFailureScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeCursorAuditFailureScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *directNoticeCursorAuditFailureScope) Audit() store.AuditLog {
	locker := s.audit.(store.AuditAppendLocker)
	return &directNoticeCursorAuditFailureLog{
		AuditLog: s.audit, locker: locker, failure: s.failure,
	}
}

func (l *directNoticeCursorAuditFailureLog) LockAppends(ctx context.Context) error {
	return l.locker.LockAppends(ctx)
}

func (l *directNoticeCursorAuditFailureLog) Append(
	context.Context,
	model.AuditDraft,
) (model.AuditEvent, error) {
	return model.AuditEvent{}, l.failure
}

func (d directNoticeCursorReceiptFailureData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d directNoticeCursorReceiptFailureData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(scope store.Scope) error {
		clock, clockOK := scope.(store.TransactionClock)
		locker, lockerOK := scope.(store.TransactionLocker)
		authority, authorityOK := scope.(store.AuthoritySnapshotLocker)
		directory, directoryOK := scope.(store.DirectorySnapshotReader)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK {
			return errors.New("cursor receipt fault scope lacks transaction capabilities")
		}
		return fn(&directNoticeCursorReceiptFailureScope{
			Scope: scope, clock: clock, locker: locker, authority: authority,
			directory: directory, failure: d.failure,
		})
	})
}

func (s *directNoticeCursorReceiptFailureScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeCursorReceiptFailureScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeCursorReceiptFailureScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *directNoticeCursorReceiptFailureScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeCursorReceiptFailureScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *directNoticeCursorReceiptFailureScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != communicationCommandKind {
		return repo, err
	}
	stamped, stampedOK := repo.(store.TransactionStampedGenericRepo)
	locker, lockerOK := repo.(store.RowLocker[model.Record])
	if !stampedOK || !lockerOK {
		return nil, errors.New("cursor receipt repository lacks transaction capabilities")
	}
	return &directNoticeCursorReceiptFailureRepo{
		GenericRepo: repo, stamped: stamped, locker: locker, failure: s.failure,
	}, nil
}

func (r *directNoticeCursorReceiptFailureRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	return r.locker.Lock(ctx, id)
}

func (r *directNoticeCursorReceiptFailureRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.stamped.CreateAtTransactionTime(ctx, record)
}

func (r *directNoticeCursorReceiptFailureRepo) CreateWithIDAtTransactionTime(
	context.Context,
	model.ID,
	model.Record,
) (model.Record, error) {
	return nil, r.failure
}

func (r *directNoticeCursorReceiptFailureRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.stamped.UpdateAtTransactionTime(ctx, record)
}

func (d *directNoticeCursorReplayViewData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mu.Lock()
	d.views++
	view := d.views
	d.mu.Unlock()
	return d.inner.View(ctx, tenant, func(scope store.Scope) error {
		clock, ok := scope.(store.TransactionClock)
		if !ok {
			return errors.New("cursor replay view lacks transaction clock")
		}
		var audit store.AuditLog
		if view > 1 {
			audit = d.audit
		}
		return fn(&directNoticeCursorReplayViewScope{
			Scope: scope, clock: clock, hideReceipt: view == 1, audit: audit,
		})
	})
}

func (d *directNoticeCursorReplayViewData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mu.Lock()
	d.mutates++
	d.mu.Unlock()
	return d.inner.Mutate(ctx, tenant, fn)
}

func (d *directNoticeCursorReplayViewData) counts() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.views, d.mutates
}

func (s *directNoticeCursorReplayViewScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeCursorReplayViewScope) Audit() store.AuditLog {
	if s.audit != nil {
		return s.audit
	}
	return s.Scope.Audit()
}

func (s *directNoticeCursorReplayViewScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != communicationCommandKind || !s.hideReceipt {
		return repo, err
	}
	return &directNoticeCursorHiddenReceiptRepo{GenericRepo: repo}, nil
}

func (r *directNoticeCursorHiddenReceiptRepo) List(
	context.Context,
	model.Query,
) ([]model.Record, model.Page, error) {
	return nil, model.Page{}, nil
}

func newDirectNoticeCursorTestService(
	t *testing.T,
	count int,
) (directNoticeReadFixture, []DirectNoticePublishResult, *directNoticeCursorService) {
	t.Helper()
	fixture, published := newDirectNoticeReadFixture(t, count)
	keyring, err := newCommunicationCursorTokenKeyring(
		"cursor-test-current",
		[]communicationCursorTokenKey{{
			kid: "cursor-test-current", material: bytes.Repeat([]byte{0x73}, 32),
		}},
	)
	if err != nil {
		t.Fatalf("create cursor test keyring: %v", err)
	}
	return fixture, published, newDirectNoticeCursorService(fixture.m, keyring)
}

func mintDirectNoticeCursorTestToken(
	t *testing.T,
	service *directNoticeCursorService,
	fixture directNoticeReadFixture,
	cursorID model.ID,
	version int64,
	base int64,
	after int64,
	deliveryID model.ID,
) string {
	t.Helper()
	filterHash, err := directNoticeCursorFilterHash()
	if err != nil {
		t.Fatalf("derive cursor filter hash: %v", err)
	}
	token, err := service.keyring.mint(communicationCursorTokenClaims{
		tenantID: fixture.scope.TenantID, workspaceID: fixture.scope.WorkspaceID,
		readerKind: RecipientUser, readerRef: fixture.principal.UserID,
		mailboxKind: MailboxPersonal, mailboxRef: fixture.principal.UserID,
		carrierClass: string(CursorCarrierDirectNoticeV1), filterHash: filterHash[:],
		cursorID: cursorID, cursorVersion: version, baseDeliverySeq: base,
		afterDeliverySeq: after, deliveryID: deliveryID,
	}, fixture.now)
	if err != nil {
		t.Fatalf("mint cursor token: %v", err)
	}
	return token
}

func directNoticeCursorCommand(
	fixture directNoticeReadFixture,
	token string,
	version int64,
	key model.ID,
) DirectNoticeCursorAdvanceCommand {
	return DirectNoticeCursorAdvanceCommand{
		CursorToken: token, IfMatch: fmt.Sprintf(`"v%d"`, version),
		IdempotencyKey: key.String(), Method: http.MethodPut,
		Path: directNoticeCursorPathPrefix + fixture.principal.UserID.String(),
	}
}

func directNoticeCursorAuditHead(
	t *testing.T,
	fixture directNoticeReadFixture,
) (store.HeadRef, bool) {
	t.Helper()
	var head store.HeadRef
	var present bool
	if err := fixture.st.View(context.Background(), fixture.tenant, func(scope store.Scope) error {
		var err error
		head, present, err = scope.Audit().Head(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read cursor audit head: %v", err)
	}
	return head, present
}

func directNoticeCursorVerifiedAuditAnchor(
	t *testing.T,
	fixture directNoticeReadFixture,
	sequence int64,
) (model.AuditEvent, string) {
	t.Helper()
	var event model.AuditEvent
	var meta string
	if err := fixture.st.View(context.Background(), fixture.tenant, func(scope store.Scope) error {
		reader, ok := scope.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return errors.New("cursor verified audit reader is unavailable")
		}
		var found bool
		var err error
		event, meta, found, err = reader.ReadVerifiedAuditAnchor(
			context.Background(), sequence,
		)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("cursor verified audit anchor is missing")
		}
		return nil
	}); err != nil {
		t.Fatalf("read cursor verified audit anchor: %v", err)
	}
	return event, meta
}

func directNoticeCursorReceiptByCommand(
	t *testing.T,
	fixture directNoticeReadFixture,
	commandID model.ID,
) CommunicationCommandReceipt {
	t.Helper()
	for _, record := range communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	) {
		receipt, err := communicationCommandReceiptFromRecord(record)
		if err == nil && receipt.CommandID == commandID {
			return receipt
		}
	}
	t.Fatalf("cursor receipt for command %s is missing", commandID)
	return CommunicationCommandReceipt{}
}

func rebindDirectNoticeCursorReceiptForTest(
	t *testing.T,
	receipt CommunicationCommandReceipt,
) CommunicationCommandReceipt {
	t.Helper()
	binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
	if err != nil {
		t.Fatalf("canonical cursor receipt response binding: %v", err)
	}
	digest := sha256.Sum256(binding)
	receipt.ResponseDigest = digest[:]
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		t.Fatalf("relabelled cursor receipt is not otherwise valid: %v", err)
	}
	return receipt
}

func TestDirectNoticeCursorServiceCreatesV1AndReplaysBeforeExpiry(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	cmd := directNoticeCursorCommand(fixture, token, 0, model.NewID())
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer

	result, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil {
		t.Fatalf("advance initial cursor: %v", err)
	}
	if result.CursorID == "" || result.CommandID == "" || result.Version != 1 ||
		result.ETag != `"v1"` || result.Projection.LastSeenSeq != 1 ||
		result.Projection.BarrierDeliveryID != "" || result.Projection.BarrierReason != "" ||
		result.AuditSeq < 1 || result.Replayed {
		t.Fatalf("initial cursor result = %+v", result)
	}
	if observer.mutates.Load() != 1 {
		t.Fatalf("fresh cursor mutate calls = %d, want exactly one", observer.mutates.Load())
	}
	cursorRows := communicationRowsForTest(t, fixture.directNoticeFixture, inboxCursorKind)
	if len(cursorRows) != 1 {
		t.Fatalf("cursor rows = %d, want 1", len(cursorRows))
	}
	cursor, err := inboxCursorFromRecord(cursorRows[0])
	if err != nil || cursor.ID != result.CursorID || cursor.Version != 1 || cursor.LastSeenSeq != 1 {
		t.Fatalf("persisted cursor = %+v, %v", cursor, err)
	}
	if barriers := communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorBarrierKind,
	); len(barriers) != 0 {
		t.Fatalf("visible initial advance created %d barriers", len(barriers))
	}

	authorizerCalls := fixture.authorizer.callCount()
	resolverCalls := fixture.resolver.calls.Load()
	closureCalls := fixture.closure.calls.Load()
	fixture.m.clock.(*testClock).advance(communicationCursorTokenReserve + time.Minute)
	replayed, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil || !replayed.Replayed || replayed.CommandID != result.CommandID ||
		replayed.CursorID != result.CursorID || replayed.Version != result.Version ||
		replayed.Projection != result.Projection {
		t.Fatalf("expired exact replay = %+v, %v; original=%+v", replayed, err, result)
	}
	if observer.mutates.Load() != 1 || fixture.authorizer.callCount() != authorizerCalls ||
		fixture.resolver.calls.Load() != resolverCalls || fixture.closure.calls.Load() != closureCalls {
		t.Fatalf("receipt-first replay observed dynamic state: mutate/core/resolver/closure=%d/%d/%d/%d",
			observer.mutates.Load(), fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load())
	}
}

func TestDirectNoticeCursorServiceExistingNoOpKeepsETag(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	initialToken := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	initial, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, initialToken, 0, model.NewID()),
	)
	if err != nil {
		t.Fatalf("create initial cursor: %v", err)
	}
	noopToken := mintDirectNoticeCursorTestToken(
		t, service, fixture, initial.CursorID, 1, 1, 1, "",
	)
	beforeCore := fixture.authorizer.callCount()
	beforeClosure := fixture.closure.calls.Load()
	result, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, noopToken, 1, model.NewID()),
	)
	if err != nil || result.CursorID != initial.CursorID || result.Version != 1 ||
		result.ETag != `"v1"` || result.Projection.LastSeenSeq != 1 {
		t.Fatalf("cursor no-op = %+v, %v", result, err)
	}
	if fixture.authorizer.callCount() != beforeCore || fixture.closure.calls.Load() != beforeClosure {
		t.Fatalf("stationary scan unexpectedly observed carrier authority")
	}
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, inboxCursorKind)
	cursor, decodeErr := inboxCursorFromRecord(rows[0])
	if decodeErr != nil || cursor.Version != 1 || cursor.LastSeenSeq != 1 {
		t.Fatalf("no-op persisted cursor = %+v, %v", cursor, decodeErr)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	)); got != 3 {
		t.Fatalf("publish + create + no-op receipt rows = %d, want 3", got)
	}
}

func TestDirectNoticeCursorServiceCoreBrokenCreatesBarrierWithoutCarrierClosure(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	fixture.authorizer.outcomes[published[0].DeliveryID] = ReadDeny
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	result, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	)
	if err != nil || result.Version != 1 || result.Projection.LastSeenSeq != 0 ||
		result.Projection.BarrierDeliveryID != published[0].DeliveryID ||
		result.Projection.BarrierReason != BarrierTemporarilyInvisible {
		t.Fatalf("core-BROKEN cursor = %+v, %v", result, err)
	}
	if fixture.resolver.calls.Load() != 1 || fixture.closure.calls.Load() != 0 {
		t.Fatalf("core-BROKEN resolver/closure calls = %d/%d, want principal-only 1/0",
			fixture.resolver.calls.Load(), fixture.closure.calls.Load())
	}
	cursorRows := communicationRowsForTest(t, fixture.directNoticeFixture, inboxCursorKind)
	cursor, decodeErr := inboxCursorFromRecord(cursorRows[0])
	if decodeErr != nil || cursor.LastSeenSeq != 0 {
		t.Fatalf("barrier cursor = %+v, %v", cursor, decodeErr)
	}
	barrierRows := communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorBarrierKind,
	)
	if len(barrierRows) != 1 {
		t.Fatalf("barrier rows = %d, want 1", len(barrierRows))
	}
	barrier, decodeErr := inboxCursorBarrierFromRecord(barrierRows[0], cursor)
	if decodeErr != nil || barrier.DeliveryID != published[0].DeliveryID ||
		barrier.BarrierSeq != 1 || barrier.Cause != BarrierTemporarilyInvisible ||
		barrier.State != CursorBarrierActive {
		t.Fatalf("persisted barrier = %+v, %v", barrier, decodeErr)
	}
}

func TestDirectNoticeCursorServiceExplicitPutResolvesBarrierAndBumpsOnce(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	fixture.authorizer.outcomes[published[0].DeliveryID] = ReadDeny
	blockedToken := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	blocked, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, blockedToken, 0, model.NewID()),
	)
	if err != nil {
		t.Fatalf("create cursor barrier: %v", err)
	}
	delete(fixture.authorizer.outcomes, published[0].DeliveryID)
	advanceToken := mintDirectNoticeCursorTestToken(
		t, service, fixture, blocked.CursorID, 1, 0, 1, published[0].DeliveryID,
	)
	advanced, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, advanceToken, 1, model.NewID()),
	)
	if err != nil || advanced.CursorID != blocked.CursorID || advanced.Version != 2 ||
		advanced.ETag != `"v2"` || advanced.Projection.LastSeenSeq != 1 ||
		advanced.Projection.BarrierDeliveryID != "" || advanced.Projection.BarrierReason != "" {
		t.Fatalf("resolve cursor barrier = %+v, %v", advanced, err)
	}
	cursorRows := communicationRowsForTest(t, fixture.directNoticeFixture, inboxCursorKind)
	cursor, decodeErr := inboxCursorFromRecord(cursorRows[0])
	if decodeErr != nil || cursor.Version != 2 || cursor.LastSeenSeq != 1 {
		t.Fatalf("resolved cursor = %+v, %v", cursor, decodeErr)
	}
	barrierRows := communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorBarrierKind,
	)
	if len(barrierRows) != 1 {
		t.Fatalf("resolved barrier rows = %d, want retained one", len(barrierRows))
	}
	barrier, decodeErr := inboxCursorBarrierFromRecord(barrierRows[0], cursor)
	if decodeErr != nil || barrier.State != CursorBarrierResolved || barrier.Version != 2 ||
		barrier.ResolvedAt == nil {
		t.Fatalf("resolved barrier = %+v, %v", barrier, decodeErr)
	}
}

func TestDirectNoticeCursorServiceCrossesSparseReaderSequence(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	otherRecipient := model.NewID()
	createDirectNoticeGrantForTest(
		t, fixture.directNoticeFixture,
		CommunicationSubjectRef{Kind: SubjectUser, Ref: otherRecipient.String()}, true, false,
	)
	otherCommand := fixture.command(model.NewID(), "foreign mailbox gap")
	otherCommand.Recipient = RecipientRef{Kind: RecipientUser, Ref: otherRecipient.String()}
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender}, otherCommand,
	); err != nil {
		t.Fatalf("publish foreign mailbox gap: %v", err)
	}
	third, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "reader sequence after sparse gap"),
	)
	if err != nil {
		t.Fatalf("publish sparse reader target: %v", err)
	}
	if published[0].DeliveryID == third.DeliveryID {
		t.Fatal("sparse reader fixture reused a Delivery")
	}
	readerRows := communicationRowsForTest(t, fixture.directNoticeFixture, messageDeliveryKind)
	var targetSequence int64
	for _, row := range readerRows {
		if row.String(model.ColID) == third.DeliveryID.String() {
			targetSequence = row.Int(colCommDeliverySeq)
		}
	}
	if targetSequence != 3 {
		t.Fatalf("sparse target sequence = %d, want 3", targetSequence)
	}
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, targetSequence, third.DeliveryID,
	)
	result, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	)
	if err != nil || result.Version != 1 || result.Projection.LastSeenSeq != 3 {
		t.Fatalf("sparse cursor advance = %+v, %v", result, err)
	}
}

func TestDirectNoticeCursorServiceAuditFailureRollsBackCursorBarrierAndReceipt(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	fixture.authorizer.outcomes[published[0].DeliveryID] = ReadDeny
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	beforeReceipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	failure := errors.New("cursor audit append fault")
	fixture.m.data = directNoticeCursorAuditFailureData{
		inner: fixture.m.data, failure: failure,
	}
	_, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	)
	if !errors.Is(err, failure) {
		t.Fatalf("cursor audit failure = %v, want injected fault", err)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorKind,
	)); got != 0 {
		t.Fatalf("audit failure retained %d cursor rows", got)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorBarrierKind,
	)); got != 0 {
		t.Fatalf("audit failure retained %d barrier rows", got)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	)); got != beforeReceipts {
		t.Fatalf("audit failure receipt rows = %d, want %d", got, beforeReceipts)
	}
}

func TestDirectNoticeCursorServiceReceiptFailureRollsBackEffectsAndAudit(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	fixture.authorizer.outcomes[published[0].DeliveryID] = ReadDeny
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	beforeReceipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	beforeHead, beforeHeadPresent := directNoticeCursorAuditHead(t, fixture)
	failure := errors.New("cursor receipt append fault")
	fixture.m.data = directNoticeCursorReceiptFailureData{
		inner: fixture.m.data, failure: failure,
	}
	_, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	)
	if !errors.Is(err, failure) {
		t.Fatalf("cursor receipt failure = %v, want injected fault", err)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorKind,
	)); got != 0 {
		t.Fatalf("receipt failure retained %d cursor rows", got)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorBarrierKind,
	)); got != 0 {
		t.Fatalf("receipt failure retained %d barrier rows", got)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	)); got != beforeReceipts {
		t.Fatalf("receipt failure rows = %d, want %d", got, beforeReceipts)
	}
	afterHead, afterHeadPresent := directNoticeCursorAuditHead(t, fixture)
	if afterHeadPresent != beforeHeadPresent || afterHead.Seq != beforeHead.Seq ||
		!bytes.Equal(afterHead.Hash, beforeHead.Hash) {
		t.Fatalf("receipt failure changed audit head: before=%+v/%v after=%+v/%v",
			beforeHead, beforeHeadPresent, afterHead, afterHeadPresent)
	}
}

func TestDirectNoticeCursorServiceFreshMACMissStopsBeforeDynamicObservers(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	replacement := byte('A')
	if token[len(token)-1] == replacement {
		replacement = 'B'
	}
	corrupt := token[:len(token)-1] + string(replacement)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	_, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, corrupt, 0, model.NewID()),
	)
	if !errors.Is(err, errCommunicationCursorTokenInvalid) {
		t.Fatalf("fresh corrupt cursor token = %v, want invalid token", err)
	}
	if observer.mutates.Load() != 0 || fixture.authorizer.callCount() != 0 ||
		fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 {
		t.Fatalf("fresh MAC miss reached dynamic observers: mutate/core/resolver/closure=%d/%d/%d/%d",
			observer.mutates.Load(), fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load())
	}
}

func TestDirectNoticeCursorServiceBoundStopsBeforeMutation(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 2)
	service.candidateBound = 1
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 2, published[1].DeliveryID,
	)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	_, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || observer.mutates.Load() != 0 ||
		fixture.authorizer.callCount() != 0 {
		t.Fatalf("bounded cursor scan = %v; mutate/core=%d/%d", err,
			observer.mutates.Load(), fixture.authorizer.callCount())
	}
}

func TestDirectNoticeCursorServiceUsesOneAuthorityLock(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	fault := &directNoticeAuthorityFaultData{
		inner: fixture.m.data, failAt: 2, err: errors.New("second authority lock"),
	}
	fixture.m.data = fault
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	if _, err := service.Advance(
		context.Background(), fixture.scope, fixture.principal,
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	); err != nil {
		t.Fatalf("cursor with second-lock tripwire: %v", err)
	}
	if fault.calls.Load() != 1 {
		t.Fatalf("authority lock calls = %d, want exactly one", fault.calls.Load())
	}
}

func TestDirectNoticeCursorServiceConcurrentVirtualV0HasOneWinner(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	commands := []DirectNoticeCursorAdvanceCommand{
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
		directNoticeCursorCommand(fixture, token, 0, model.NewID()),
	}
	start := make(chan struct{})
	results := make([]DirectNoticeCursorAdvanceResult, len(commands))
	errs := make([]error, len(commands))
	var wait sync.WaitGroup
	for index := range commands {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = service.Advance(
				context.Background(), fixture.scope, fixture.principal, commands[index],
			)
		}(index)
	}
	close(start)
	wait.Wait()
	winners, stale := 0, 0
	for index := range results {
		if errs[index] == nil {
			winners++
			if results[index].Version != 1 || results[index].CursorID == "" {
				t.Fatalf("virtual-v0 winner = %+v", results[index])
			}
		} else if errors.Is(errs[index], errDirectNoticeCursorVersionMismatch) {
			stale++
		} else {
			t.Fatalf("virtual-v0 contender %d = %+v, %v", index, results[index], errs[index])
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("virtual-v0 winners/stale = %d/%d, want 1/1", winners, stale)
	}
	if got := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, inboxCursorKind,
	)); got != 1 {
		t.Fatalf("virtual-v0 cursor identities = %d, want 1", got)
	}
}

func TestDirectNoticeCursorServiceReceiptReuseWinsBeforeTokenVerification(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	key := model.NewID()
	cmd := directNoticeCursorCommand(fixture, token, 0, key)
	if _, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd); err != nil {
		t.Fatalf("seed cursor receipt: %v", err)
	}
	beforeCore := fixture.authorizer.callCount()
	beforeResolver := fixture.resolver.calls.Load()
	changed := cmd
	replacement := byte('A')
	if token[len(token)-1] == replacement {
		replacement = 'B'
	}
	changed.CursorToken = token[:len(token)-1] + string(replacement)
	_, err := service.Advance(context.Background(), fixture.scope, fixture.principal, changed)
	if !errors.Is(err, store.ErrConflict) ||
		fixture.authorizer.callCount() != beforeCore || fixture.resolver.calls.Load() != beforeResolver {
		t.Fatalf("changed request under same key = %v; core/resolver=%d/%d", err,
			fixture.authorizer.callCount(), fixture.resolver.calls.Load())
	}
}

func TestDirectNoticeCursorServiceInTransactionReplayRequiresOuterVerifiedAnchor(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	cmd := directNoticeCursorCommand(fixture, token, 0, model.NewID())
	original, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil {
		t.Fatalf("seed cursor replay receipt: %v", err)
	}
	event, meta := directNoticeCursorVerifiedAuditAnchor(t, fixture, original.AuditSeq)
	corrupt := event
	corrupt.Hash = bytes.Repeat([]byte{0x5a}, sha256.Size)
	replayViews := &directNoticeCursorReplayViewData{
		inner: fixture.m.data,
		audit: directNoticeReplayAuditLog{events: []model.AuditEvent{corrupt}, meta: meta},
	}
	fixture.m.data = replayViews

	if replay, replayErr := service.Advance(
		context.Background(), fixture.scope, fixture.principal, cmd,
	); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) || replay != (DirectNoticeCursorAdvanceResult{}) {
		t.Fatalf("in-transaction replay with corrupt outer anchor = %+v, %v; want UNKNOWN",
			replay, replayErr)
	}
	views, mutates := replayViews.counts()
	if mutates != 1 || views < 2 {
		t.Fatalf("in-transaction replay views/mutates = %d/%d, want outer recheck and one rollback",
			views, mutates)
	}
	if receipts := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	)); receipts != 2 {
		t.Fatalf("failed replay changed receipt rows = %d, want publish+cursor", receipts)
	}
}

func TestDirectNoticeCursorServiceInTransactionReplayReturnsVerifiedControl(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	cmd := directNoticeCursorCommand(fixture, token, 0, model.NewID())
	original, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil {
		t.Fatalf("seed cursor replay control: %v", err)
	}
	replayViews := &directNoticeCursorReplayViewData{inner: fixture.m.data}
	fixture.m.data = replayViews
	replayed, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil || !replayed.Replayed || replayed.CommandID != original.CommandID ||
		replayed.CursorID != original.CursorID || replayed.Version != original.Version ||
		replayed.ETag != original.ETag || replayed.Projection != original.Projection ||
		replayed.AuditSeq != original.AuditSeq {
		t.Fatalf("verified in-transaction replay = %+v, %v; original=%+v", replayed, err, original)
	}
	views, mutates := replayViews.counts()
	if mutates != 1 || views < 2 {
		t.Fatalf("verified in-transaction replay views/mutates = %d/%d, want outer recheck and one rollback",
			views, mutates)
	}
}

func TestDirectNoticeCursorServiceAuditCommitmentRejectsReceiptRelabeling(t *testing.T) {
	t.Parallel()

	fixture, published, service := newDirectNoticeCursorTestService(t, 1)
	token := mintDirectNoticeCursorTestToken(
		t, service, fixture, "", 0, 0, 1, published[0].DeliveryID,
	)
	cmd := directNoticeCursorCommand(fixture, token, 0, model.NewID())
	result, err := service.Advance(context.Background(), fixture.scope, fixture.principal, cmd)
	if err != nil {
		t.Fatalf("seed cursor apply commitment: %v", err)
	}
	normalized, err := normalizeDirectNoticeCursorAdvanceCommand(
		fixture.scope, fixture.principal, cmd,
	)
	if err != nil {
		t.Fatalf("normalize cursor apply commitment command: %v", err)
	}
	receipt := directNoticeCursorReceiptByCommand(t, fixture, result.CommandID)
	event, meta := directNoticeCursorVerifiedAuditAnchor(t, fixture, result.AuditSeq)
	exactAudit := directNoticeReplayAuditLog{events: []model.AuditEvent{event}, meta: meta}
	if err := verifyDirectNoticeCursorAuditAnchor(
		context.Background(), exactAudit, normalized, receipt,
	); err != nil {
		t.Fatalf("exact cursor apply commitment: %v", err)
	}

	mutants := map[string]func(*CommunicationCommandReceipt){
		"request digest": func(value *CommunicationCommandReceipt) {
			value.RequestDigest = append([]byte(nil), value.RequestDigest...)
			value.RequestDigest[0] ^= 0xff
		},
		"idempotency key hash": func(value *CommunicationCommandReceipt) {
			value.IdempotencyKeyHash = append([]byte(nil), value.IdempotencyKeyHash...)
			value.IdempotencyKeyHash[0] ^= 0xff
		},
	}
	for name, mutate := range mutants {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			changed = rebindDirectNoticeCursorReceiptForTest(t, changed)
			if verifyErr := verifyDirectNoticeCursorAuditAnchor(
				context.Background(), exactAudit, normalized, changed,
			); !errors.Is(verifyErr, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("relabelled cursor receipt audit = %v, want UNKNOWN", verifyErr)
			}
		})
	}

	if len(meta) < 2 || meta[len(meta)-1] != '}' {
		t.Fatalf("cursor audit metadata is not a JSON object: %q", meta)
	}
	extraMeta := meta[:len(meta)-1] + `,"unexpected_cursor_meta":true}`
	if verifyErr := verifyDirectNoticeCursorAuditAnchor(
		context.Background(),
		directNoticeReplayAuditLog{events: []model.AuditEvent{event}, meta: extraMeta},
		normalized, receipt,
	); !errors.Is(verifyErr, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("cursor audit with open metadata vocabulary = %v, want UNKNOWN", verifyErr)
	}
}

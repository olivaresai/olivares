// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

type deliveryDispatchLockedRecordDriftData struct {
	inner  api.ModuleData
	drifts map[model.Kind]func(model.Record)
}

type deliveryDispatchLockedRecordDriftScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	drifts    map[model.Kind]func(model.Record)
}

type deliveryDispatchLockedRecordDriftRepo struct {
	store.GenericRepo
	stamped store.TransactionStampedGenericRepo
	locker  store.RowLocker[model.Record]
	drift   func(model.Record)
}

func (d deliveryDispatchLockedRecordDriftData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d deliveryDispatchLockedRecordDriftData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(scope store.Scope) error {
		clock, clockOK := scope.(store.TransactionClock)
		locker, lockerOK := scope.(store.TransactionLocker)
		authority, authorityOK := scope.(store.AuthoritySnapshotLocker)
		directory, directoryOK := scope.(store.DirectorySnapshotReader)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK || len(d.drifts) == 0 {
			return errors.New("delivery locked-record drift scope lacks transaction capabilities")
		}
		return fn(&deliveryDispatchLockedRecordDriftScope{
			Scope: scope, clock: clock, locker: locker, authority: authority,
			directory: directory, drifts: d.drifts,
		})
	})
}

func (s *deliveryDispatchLockedRecordDriftScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *deliveryDispatchLockedRecordDriftScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *deliveryDispatchLockedRecordDriftScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *deliveryDispatchLockedRecordDriftScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *deliveryDispatchLockedRecordDriftScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *deliveryDispatchLockedRecordDriftScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	drift, driftOK := s.drifts[kind]
	if err != nil || !driftOK {
		return repo, err
	}
	stamped, stampedOK := repo.(store.TransactionStampedGenericRepo)
	locker, lockerOK := repo.(store.RowLocker[model.Record])
	if !stampedOK || !lockerOK {
		return nil, errors.New("delivery locked-record repository lacks transaction capabilities")
	}
	return &deliveryDispatchLockedRecordDriftRepo{
		GenericRepo: repo, stamped: stamped, locker: locker, drift: drift,
	}, nil
}

func (r *deliveryDispatchLockedRecordDriftRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	record, err := r.locker.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	record = workSchemaClone(record)
	r.drift(record)
	return record, nil
}

func (r *deliveryDispatchLockedRecordDriftRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.stamped.CreateAtTransactionTime(ctx, record)
}

func (r *deliveryDispatchLockedRecordDriftRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	return r.stamped.CreateWithIDAtTransactionTime(ctx, id, record)
}

func (r *deliveryDispatchLockedRecordDriftRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	return r.stamped.UpdateAtTransactionTime(ctx, record)
}

type deliveryDispatchTestNotifier struct {
	mu sync.Mutex

	events          []string
	capabilities    sdk.DeliveryCapabilities
	capabilityHook  func(sdk.DeliveryEndpointIdentity)
	witnessFor      *sdk.DeliveryEndpointIdentity
	capabilityErr   error
	notifyResult    sdk.DeliveryAttemptResult
	notifyErr       error
	notifyHook      func(sdk.DeliveryDispatch)
	reconcileResult sdk.DeliveryReconciliationResult
	reconcileErr    error
	notifyCalls     int
	reconcileCalls  int
}

func (n *deliveryDispatchTestNotifier) record(event string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
}

func (n *deliveryDispatchTestNotifier) Capabilities(
	_ context.Context,
	endpoint sdk.DeliveryEndpointIdentity,
) (sdk.DeliveryCapabilityWitness, error) {
	n.record("capabilities")
	if n.capabilityHook != nil {
		n.capabilityHook(endpoint)
	}
	bound := endpoint
	if n.witnessFor != nil {
		bound = n.witnessFor.Clone()
	}
	witness, err := sdk.NewDeliveryCapabilityWitness(bound, n.capabilities)
	if err != nil {
		return sdk.DeliveryCapabilityWitness{}, err
	}
	return witness, n.capabilityErr
}

func (n *deliveryDispatchTestNotifier) Notify(
	_ context.Context,
	dispatch sdk.DeliveryDispatch,
) (sdk.DeliveryAttemptResult, error) {
	n.mu.Lock()
	n.notifyCalls++
	n.events = append(n.events, "notify")
	hook := n.notifyHook
	result, err := n.notifyResult.Clone(), n.notifyErr
	n.mu.Unlock()
	if hook != nil {
		hook(dispatch)
	}
	return result, err
}

func (n *deliveryDispatchTestNotifier) Reconcile(
	_ context.Context,
	reconciliation sdk.DeliveryReconciliation,
) (sdk.DeliveryReconciliationResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reconcileCalls++
	n.events = append(n.events, "reconcile")
	if reconciliation.Validate() != nil {
		return sdk.DeliveryReconciliationResult{}, errors.New("invalid reconciliation request")
	}
	return n.reconcileResult.Clone(), n.reconcileErr
}

func (n *deliveryDispatchTestNotifier) snapshot() ([]string, int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...), n.notifyCalls, n.reconcileCalls
}

type deliveryDispatchTestFence struct {
	mu     sync.Mutex
	events *[]string
	err    error
	calls  int
	claim  deliveryDispatchPumpClaim
}

func (f *deliveryDispatchTestFence) BeforeNotify(
	_ context.Context,
	_ DirectoryScopeRef,
	claim deliveryDispatchPumpClaim,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.claim = claim
	if f.events != nil {
		*f.events = append(*f.events, "fence")
	}
	return f.err
}

type deliveryDispatchServiceFixture struct {
	directNoticeFixture
	service  *deliveryDispatchService
	notifier *deliveryDispatchTestNotifier
	fence    *deliveryDispatchTestFence
	dispatch DeliveryDispatch
	endpoint CommunicationEndpoint
	delivery MessageDelivery
	message  Message
}

func newDeliveryDispatchServiceFixture(t *testing.T) deliveryDispatchServiceFixture {
	t.Helper()
	ctx := context.Background()
	fixture := newDirectNoticeFixture(t)

	channel := fixture.channel
	channel.DefaultWake = WakePrimary
	channelRecord, err := channelToRecord(channel)
	if err != nil {
		t.Fatalf("encode wake Channel: %v", err)
	}
	updatedChannel, err := communicationUpdate(
		ctx, fixture.m, fixture.tenant, channelKind, channelRecord,
	)
	if err != nil {
		t.Fatalf("enable wake on Channel: %v", err)
	}
	fixture.channel, err = channelFromRecord(updatedChannel)
	if err != nil {
		t.Fatalf("decode wake Channel: %v", err)
	}

	published, err := fixture.m.publishDirectNotice(
		ctx, fixture.scope, CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "body-never-enters-delivery-driver"),
	)
	if err != nil {
		t.Fatalf("publish wake DirectNotice: %v", err)
	}
	deliveryRows := communicationRowsForTest(t, fixture, messageDeliveryKind)
	messageRows := communicationRowsForTest(t, fixture, messageKind)
	if len(deliveryRows) != 1 || len(messageRows) != 1 {
		t.Fatalf("publish rows = delivery:%d message:%d", len(deliveryRows), len(messageRows))
	}
	delivery, err := messageDeliveryFromRecord(deliveryRows[0])
	if err != nil {
		t.Fatalf("decode Delivery: %v", err)
	}
	message, err := deliveryMessageFromRecord(messageRows[0])
	if err != nil {
		t.Fatalf("decode Message: %v", err)
	}
	if delivery.ID != published.DeliveryID || message.ID != published.MessageID ||
		delivery.WakePolicy != WakePrimary {
		t.Fatalf("published wake lineage = delivery:%+v message:%+v", delivery, message)
	}
	endpointID := model.NewID()
	heartbeat := fixture.now.Add(time.Hour)
	endpoint := CommunicationEndpoint{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: endpointID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
				Version: 1, CreatedAt: fixture.now,
			},
			UpdatedAt: fixture.now,
		},
		Owner:       delivery.Recipient,
		ProviderKey: "driver:test-delivery", Transport: "test-wake",
		EndpointRef: "test-endpoint", CapabilitiesJSON: []byte(`{"wake":true}`),
		TransportFingerprint: "test-delivery-fingerprint-v1",
		SupportLevel:         EndpointStable, Priority: 1, State: EndpointActive,
		HeartbeatExpiresAt: &heartbeat, Generation: 1,
	}
	endpointRecord, err := communicationEndpointToRecord(endpoint)
	if err != nil {
		t.Fatalf("encode endpoint: %v", err)
	}
	createdEndpoint, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, communicationEndpointKind, endpoint.ID, endpointRecord,
	)
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	endpoint, err = communicationEndpointFromRecord(createdEndpoint)
	if err != nil {
		t.Fatalf("decode endpoint: %v", err)
	}

	dispatchID := model.NewID()
	key := sha256.Sum256([]byte("initial-delivery-dispatch:" + dispatchID.String()))
	dispatch, err := NewInitialDeliveryDispatch(
		MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: dispatchID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: fixture.now,
		}, UpdatedAt: fixture.now},
		delivery.ID,
		DispatchRouteIdentity{
			EndpointID: endpoint.ID, EndpointGeneration: endpoint.Generation,
			PolicyGeneration: 1,
		},
		key[:],
	)
	if err != nil {
		t.Fatalf("plan initial dispatch: %v", err)
	}
	dispatchRecord, err := deliveryDispatchToRecord(dispatch)
	if err != nil {
		t.Fatalf("encode dispatch: %v", err)
	}
	createdDispatch, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, deliveryDispatchKind, dispatch.ID, dispatchRecord,
	)
	if err != nil {
		t.Fatalf("create dispatch: %v", err)
	}
	dispatch, err = deliveryDispatchFromRecord(createdDispatch)
	if err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}

	notifier := &deliveryDispatchTestNotifier{
		capabilities: sdk.NewDeliveryCapabilities(true, true, true, false),
	}
	fence := &deliveryDispatchTestFence{}
	service, err := newDeliveryDispatchService(
		fixture.m, notifier, fence,
		deliveryDispatchServiceConfig{
			ClaimTTL: time.Minute, ResolutionTTL: 10 * time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("new dispatch service: %v", err)
	}
	return deliveryDispatchServiceFixture{
		directNoticeFixture: fixture, service: service, notifier: notifier, fence: fence,
		dispatch: dispatch, endpoint: endpoint, delivery: delivery, message: message,
	}
}

func deliveryDispatchRecordForTest(
	t *testing.T,
	fixture deliveryDispatchServiceFixture,
) DeliveryDispatch {
	t.Helper()
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, deliveryDispatchKind)
	if len(rows) != 1 {
		t.Fatalf("dispatch rows = %d, want 1", len(rows))
	}
	dispatch, err := deliveryDispatchFromRecord(rows[0])
	if err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	return dispatch
}

func deliveryAttemptRecordForTest(
	t *testing.T,
	fixture deliveryDispatchServiceFixture,
) DeliveryAttempt {
	t.Helper()
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(rows))
	}
	attempt, err := deliveryAttemptFromRecord(rows[0])
	if err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	return attempt
}

func deliveryRecordForDispatchTest(
	t *testing.T,
	fixture deliveryDispatchServiceFixture,
) MessageDelivery {
	t.Helper()
	rows := communicationRowsForTest(t, fixture.directNoticeFixture, messageDeliveryKind)
	if len(rows) != 1 {
		t.Fatalf("delivery rows = %d, want 1", len(rows))
	}
	delivery, err := messageDeliveryFromRecord(rows[0])
	if err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return delivery
}

func verifyDeliveryDispatchClaimAuditAnchor(
	ctx context.Context,
	fixture deliveryDispatchServiceFixture,
	dispatchID model.ID,
	attemptID model.ID,
	sequence int64,
) error {
	if sequence < 1 {
		return errors.New("claim audit sequence is absent")
	}
	var event model.AuditEvent
	var meta string
	err := fixture.st.View(ctx, fixture.tenant, func(scope store.Scope) error {
		reader, ok := scope.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return errors.New("verified claim audit reader is unavailable")
		}
		var found bool
		var readErr error
		event, meta, found, readErr = reader.ReadVerifiedAuditAnchor(ctx, sequence)
		if readErr != nil {
			return readErr
		}
		if !found {
			return errors.New("verified claim audit anchor is absent")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if event.Seq != sequence || event.Action != deliveryDispatchClaimAuditAction ||
		event.TargetKind != deliveryDispatchKind || event.TargetID != dispatchID ||
		event.ActorKind != model.ActorSystem || len(event.PayloadHash) != sha256.Size ||
		len(event.Hash) != sha256.Size || !strings.Contains(meta, attemptID.String()) {
		return errors.New("verified claim audit anchor does not bind the exact claim")
	}
	return nil
}

func TestDeliveryDispatchPumpCommitsBeforeIOAndSettlesAccepted(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	receipt := sha256.Sum256([]byte("provider-acceptance"))
	accepted, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptAccepted, sdk.DeliveryBoundaryCrossed, receipt[:],
	)
	if err != nil {
		t.Fatalf("accepted result: %v", err)
	}
	fixture.notifier.notifyResult = accepted
	fixture.fence.events = &fixture.notifier.events
	fixture.notifier.notifyHook = func(request sdk.DeliveryDispatch) {
		dispatch := deliveryDispatchRecordForTest(t, fixture)
		attempt := deliveryAttemptRecordForTest(t, fixture)
		if dispatch.State != DispatchInFlight || attempt.State != AttemptReserved ||
			dispatch.ClaimOwner != "node-a.e41" || attempt.ID.String() != request.AttemptID() ||
			!bytes.Equal(attempt.RequestHash, request.RequestDigest()) {
			t.Fatalf("driver observed uncommitted/mismatched claim: dispatch=%+v attempt=%+v", dispatch, attempt)
		}
	}

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-a", Epoch: 41},
	)
	if err != nil {
		t.Fatalf("pump accepted delivery: %v", err)
	}
	if !result.Claimed || !result.Notified || result.State != DispatchSucceeded || result.AuditSeq < 1 {
		t.Fatalf("pump result = %+v", result)
	}
	dispatch := deliveryDispatchRecordForTest(t, fixture)
	attempt := deliveryAttemptRecordForTest(t, fixture)
	delivery := deliveryRecordForDispatchTest(t, fixture)
	if dispatch.State != DispatchSucceeded || attempt.State != AttemptFinished ||
		attempt.TransmitBoundary != TransmitCrossed || attempt.Verdict != VerdictClean ||
		!bytes.Equal(attempt.ProviderReceiptHash, receipt[:]) ||
		delivery.State != DeliveryAvailable || delivery.LastWakeVerdict != VerdictClean ||
		delivery.LastWakeCode != "provider_accepted" || delivery.LastWakeAt == nil {
		t.Fatalf("settled rows: dispatch=%+v attempt=%+v delivery=%+v", dispatch, attempt, delivery)
	}
	if got := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind)); got != 0 {
		t.Fatalf("dispatch success created %d MessageAck rows", got)
	}
	events, notifyCalls, _ := fixture.notifier.snapshot()
	if strings.Join(events, ",") != "capabilities,fence,notify" || notifyCalls != 1 ||
		fixture.fence.calls != 1 || fixture.fence.claim.Epoch != 41 {
		t.Fatalf("effect order/calls = %v notify=%d fence=%d claim=%+v", events, notifyCalls, fixture.fence.calls, fixture.fence.claim)
	}
	second, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-a", Epoch: 41},
	)
	if err != nil || second.Claimed {
		t.Fatalf("second pump = %+v, %v", second, err)
	}
	_, notifyCalls, _ = fixture.notifier.snapshot()
	if notifyCalls != 1 {
		t.Fatalf("accepted generation was resent: notify calls=%d", notifyCalls)
	}
}

func TestDeliveryDispatchConcurrentPumpsReserveOneAttempt(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	receipt := sha256.Sum256([]byte("single-assignment-provider-receipt"))
	accepted, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptAccepted, sdk.DeliveryBoundaryCrossed, receipt[:],
	)
	if err != nil {
		t.Fatalf("accepted result: %v", err)
	}
	fixture.notifier.notifyResult = accepted
	type outcome struct {
		result deliveryDispatchPumpResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var start sync.WaitGroup
	start.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			start.Done()
			start.Wait()
			result, pumpErr := fixture.service.PumpOne(
				context.Background(), fixture.scope,
				deliveryDispatchPumpClaim{NodeID: "node-race", Epoch: 3},
			)
			outcomes <- outcome{result: result, err: pumpErr}
		}()
	}
	claimed := 0
	for range 2 {
		observed := <-outcomes
		if observed.err != nil {
			t.Fatalf("concurrent pump: %v", observed.err)
		}
		if observed.result.Claimed {
			claimed++
		}
	}
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if claimed != 1 || notifyCalls != 1 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)) != 1 {
		t.Fatalf("concurrent claims=%d notify=%d", claimed, notifyCalls)
	}
}

func TestDeliveryDispatchPumpErrorIsUnknownAndNeverBlindRetries(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.notifier.notifyErr = errors.New("RAW-PROVIDER-CANARY must not persist")

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-b", Epoch: 7},
	)
	if err != nil || result.State != DispatchUnknown || !result.Notified {
		t.Fatalf("pump indeterminate = %+v, %v", result, err)
	}
	dispatch := deliveryDispatchRecordForTest(t, fixture)
	attempt := deliveryAttemptRecordForTest(t, fixture)
	if dispatch.State != DispatchUnknown || attempt.TransmitBoundary != TransmitUnknown ||
		attempt.Verdict != VerdictUnknown || attempt.Code != "provider_outcome_indeterminate" {
		t.Fatalf("indeterminate rows: dispatch=%+v attempt=%+v", dispatch, attempt)
	}
	for _, kind := range []model.Kind{deliveryDispatchKind, deliveryAttemptKind, messageDeliveryKind} {
		for _, row := range communicationRowsForTest(t, fixture.directNoticeFixture, kind) {
			raw, encodeErr := canonicalJSON(row)
			if encodeErr != nil || strings.Contains(string(raw), "RAW-PROVIDER-CANARY") {
				t.Fatalf("%s persisted raw provider diagnostic: %s (%v)", kind, raw, encodeErr)
			}
		}
	}
	second, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-b", Epoch: 7},
	)
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if err != nil || second.Claimed || notifyCalls != 1 {
		t.Fatalf("UNKNOWN blind retry = %+v, %v, notify calls=%d", second, err, notifyCalls)
	}
}

func TestDeliveryDispatchNotifyResultWithSimultaneousErrorIsUnknown(t *testing.T) {
	t.Parallel()

	receipt := sha256.Sum256([]byte("must-be-discarded-on-error"))
	accepted, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptAccepted, sdk.DeliveryBoundaryCrossed, receipt[:],
	)
	if err != nil {
		t.Fatalf("accepted result: %v", err)
	}
	refused, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptRefusedBeforeBoundary, sdk.DeliveryBoundaryNotCrossed, nil,
	)
	if err != nil {
		t.Fatalf("refused result: %v", err)
	}
	for name, callbackResult := range map[string]sdk.DeliveryAttemptResult{
		"accepted plus error": accepted,
		"refused plus error":  refused,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newDeliveryDispatchServiceFixture(t)
			fixture.notifier.notifyResult = callbackResult
			fixture.notifier.notifyErr = errors.New("simultaneous driver error")

			result, pumpErr := fixture.service.PumpOne(
				context.Background(), fixture.scope,
				deliveryDispatchPumpClaim{NodeID: "node-dual-result", Epoch: 19},
			)
			if pumpErr != nil || result.State != DispatchUnknown || !result.Notified {
				t.Fatalf("simultaneous result/error = %+v, %v", result, pumpErr)
			}
			attempt := deliveryAttemptRecordForTest(t, fixture)
			if attempt.State != AttemptFinished || attempt.TransmitBoundary != TransmitUnknown ||
				attempt.Verdict != VerdictUnknown || len(attempt.ProviderReceiptHash) != 0 ||
				attempt.Code != "provider_outcome_indeterminate" {
				t.Fatalf("simultaneous result/error Attempt = %+v", attempt)
			}
		})
	}
}

func TestDeliveryDispatchPumpFenceRefusalNeverCallsNotifier(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.fence.err = errors.New("leadership changed")

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-c", Epoch: 9},
	)
	if err != nil || result.State != DispatchFailed || result.Notified {
		t.Fatalf("fenced pump = %+v, %v", result, err)
	}
	attempt := deliveryAttemptRecordForTest(t, fixture)
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if notifyCalls != 0 || attempt.TransmitBoundary != TransmitNotCrossed ||
		attempt.Verdict != VerdictBroken || attempt.Code != "delivery_epoch_fence_refused" {
		t.Fatalf("fence result attempt=%+v notify calls=%d", attempt, notifyCalls)
	}
}

func TestDeliveryDispatchProviderRefusalSettlesFailed(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	refused, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptRefusedBeforeBoundary,
		sdk.DeliveryBoundaryNotCrossed,
		nil,
	)
	if err != nil {
		t.Fatalf("refused result: %v", err)
	}
	fixture.notifier.notifyResult = refused

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-refused", Epoch: 17},
	)
	if err != nil || !result.Notified || result.State != DispatchFailed {
		t.Fatalf("provider refusal = %+v, %v", result, err)
	}
	attempt := deliveryAttemptRecordForTest(t, fixture)
	if attempt.TransmitBoundary != TransmitNotCrossed || attempt.Verdict != VerdictBroken ||
		attempt.Code != "provider_refused_before_boundary" {
		t.Fatalf("refused Attempt = %+v", attempt)
	}
}

func TestDeliveryDispatchActiveTurnCapabilityKeepsDirectNoticeRouteOff(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.notifier.capabilities = sdk.NewDeliveryCapabilities(true, true, true, true)

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-route-off", Epoch: 8},
	)
	if err != nil || !result.Claimed || result.Notified || result.State != DispatchFailed {
		t.Fatalf("active-turn capability = %+v, %v", result, err)
	}
	attempt := deliveryAttemptRecordForTest(t, fixture)
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if notifyCalls != 0 || attempt.TransmitBoundary != TransmitNotCrossed ||
		attempt.Code != "delivery_capability_refused" {
		t.Fatalf("active-turn route crossed: attempt=%+v notify=%d", attempt, notifyCalls)
	}
}

func TestDeliveryDispatchCapabilityWitnessMustMatchExactEndpoint(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	candidate, found, err := fixture.service.scanPendingCandidate(context.Background(), fixture.scope)
	if err != nil || !found {
		t.Fatalf("scan candidate = found:%v err:%v", found, err)
	}
	params := candidate.endpoint.Params()
	otherFingerprint := sha256.Sum256([]byte("other-endpoint-generation"))
	params.EndpointFingerprint = otherFingerprint[:]
	other, err := sdk.NewDeliveryEndpointIdentity(params)
	if err != nil {
		t.Fatalf("other endpoint identity: %v", err)
	}
	fixture.notifier.witnessFor = &other

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-capability", Epoch: 6},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || result.Claimed {
		t.Fatalf("mismatched capability witness = %+v, %v", result, err)
	}
	if len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)) != 0 {
		t.Fatal("mismatched capability witness created an Attempt")
	}
}

func TestDeliveryDispatchNonUserRecipientNeverClaimsOrNotifies(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	agentRef := model.NewID().String()
	originalData := fixture.m.data
	fixture.m.data = deliveryDispatchLockedRecordDriftData{
		inner: originalData,
		drifts: map[model.Kind]func(model.Record){
			messageDeliveryKind: func(record model.Record) {
				record[colCommRecipientKind] = string(RecipientAgent)
				record[colCommRecipientRef] = agentRef
			},
			communicationEndpointKind: func(record model.Record) {
				record[colCommOwnerKind] = string(RecipientAgent)
				record[colCommOwnerRef] = agentRef
			},
		},
	}

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-non-user", Epoch: 13},
	)
	fixture.m.data = originalData
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || result.Claimed || result.Notified {
		t.Fatalf("non-User recipient pump = %+v, %v", result, err)
	}
	dispatch := deliveryDispatchRecordForTest(t, fixture)
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if dispatch.State != DispatchPending || dispatch.AttemptCount != 0 || notifyCalls != 0 ||
		len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)) != 0 {
		t.Fatalf("non-User recipient crossed claim boundary: dispatch=%+v notify=%d", dispatch, notifyCalls)
	}
}

func TestDeliveryDispatchLockedEndpointExactBindingDriftRefusesClaim(t *testing.T) {
	t.Parallel()

	mutants := map[string]func(model.Record){
		"fingerprint": func(record model.Record) {
			record[colCommTransportFingerprint] = "locked-fingerprint-v2"
		},
		"generation": func(record model.Record) {
			record[colCommGeneration] = record.Int(colCommGeneration) + 1
		},
		"provider": func(record model.Record) {
			record[colCommProviderKey] = "driver:locked-provider-v2"
		},
		"transport": func(record model.Record) {
			record[colTransport] = "locked-transport-v2"
		},
	}
	for name, mutate := range mutants {
		t.Run(name, func(t *testing.T) {
			fixture := newDeliveryDispatchServiceFixture(t)
			originalData := fixture.m.data
			fixture.m.data = deliveryDispatchLockedRecordDriftData{
				inner: originalData,
				drifts: map[model.Kind]func(model.Record){
					communicationEndpointKind: mutate,
				},
			}

			result, err := fixture.service.PumpOne(
				context.Background(), fixture.scope,
				deliveryDispatchPumpClaim{NodeID: "node-exact-drift", Epoch: 23},
			)
			fixture.m.data = originalData
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || result.Claimed || result.Notified {
				t.Fatalf("%s drift pump = %+v, %v", name, result, err)
			}
			dispatch := deliveryDispatchRecordForTest(t, fixture)
			_, notifyCalls, _ := fixture.notifier.snapshot()
			if dispatch.State != DispatchPending || dispatch.AttemptCount != 0 ||
				notifyCalls != 0 ||
				len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)) != 0 {
				t.Fatalf("%s drift crossed claim boundary: dispatch=%+v notify=%d", name, dispatch, notifyCalls)
			}
		})
	}
}

func TestDeliveryDispatchCapabilityEndpointDriftRefusesClaim(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.notifier.capabilityHook = func(_ sdk.DeliveryEndpointIdentity) {
		rows := communicationRowsForTest(t, fixture.directNoticeFixture, communicationEndpointKind)
		if len(rows) != 1 {
			t.Fatalf("endpoint rows=%d", len(rows))
		}
		row := workSchemaClone(rows[0])
		row[colCommState] = string(EndpointStale)
		if _, err := communicationUpdate(
			context.Background(), fixture.m, fixture.tenant, communicationEndpointKind, row,
		); err != nil {
			t.Fatalf("drift endpoint: %v", err)
		}
	}

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-d", Epoch: 2},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || result.Claimed {
		t.Fatalf("endpoint drift pump = %+v, %v", result, err)
	}
	if got := len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)); got != 0 {
		t.Fatalf("endpoint drift created %d attempts", got)
	}
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if notifyCalls != 0 || deliveryDispatchRecordForTest(t, fixture).State != DispatchPending {
		t.Fatalf("endpoint drift crossed boundary: notify=%d", notifyCalls)
	}
}

func TestDeliveryDispatchCrashAbandonsUnknown(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.service.config.ClaimTTL = 2 * time.Millisecond
	candidate, found, err := fixture.service.scanPendingCandidate(context.Background(), fixture.scope)
	if err != nil || !found {
		t.Fatalf("scan candidate = found:%v err:%v", found, err)
	}
	witness, err := fixture.notifier.Capabilities(context.Background(), candidate.endpoint)
	if err != nil {
		t.Fatalf("capability witness: %v", err)
	}
	owner, err := (deliveryDispatchPumpClaim{NodeID: "node-e", Epoch: 5}).owner()
	if err != nil {
		t.Fatalf("claim owner: %v", err)
	}
	claimed, won, err := fixture.service.claimCandidate(
		context.Background(), fixture.scope, candidate, witness, owner,
	)
	if err != nil || !won || claimed.dispatch.AttemptID() == "" {
		t.Fatalf("claim without callback = won:%v claimed:%+v err:%v", won, claimed, err)
	}
	time.Sleep(10 * time.Millisecond)
	result, err := fixture.service.AbandonExpired(
		context.Background(), fixture.scope, fixture.dispatch.ID,
	)
	if err != nil || result.State != DispatchUnknown {
		t.Fatalf("abandon expired = %+v, %v", result, err)
	}
	attempt := deliveryAttemptRecordForTest(t, fixture)
	_, notifyCalls, _ := fixture.notifier.snapshot()
	if notifyCalls != 0 || attempt.State != AttemptAbandoned ||
		attempt.TransmitBoundary != TransmitUnknown || attempt.Verdict != VerdictUnknown {
		t.Fatalf("abandoned attempt=%+v notify=%d", attempt, notifyCalls)
	}
}

func TestDeliveryDispatchReconcileAcceptedWithoutResend(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.notifier.notifyErr = errors.New("indeterminate provider observation")
	pumped, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-f", Epoch: 12},
	)
	if err != nil || pumped.State != DispatchUnknown {
		t.Fatalf("prepare UNKNOWN = %+v, %v", pumped, err)
	}
	receipt := sha256.Sum256([]byte("reconciled-provider-receipt"))
	fixture.notifier.reconcileResult, err = sdk.NewDeliveryReconciliationResult(
		sdk.DeliveryReconciliationAccepted, receipt[:], "provider:test-evidence-1",
	)
	if err != nil {
		t.Fatalf("accepted reconciliation result: %v", err)
	}
	reconciled, err := fixture.service.ReconcileUnknown(
		context.Background(), fixture.scope, fixture.dispatch.ID,
	)
	if err != nil || !reconciled.Changed || reconciled.State != DispatchSucceeded ||
		reconciled.AuditSeq < 1 {
		t.Fatalf("reconcile accepted = %+v, %v", reconciled, err)
	}
	dispatch := deliveryDispatchRecordForTest(t, fixture)
	_, notifyCalls, reconcileCalls := fixture.notifier.snapshot()
	if notifyCalls != 1 || reconcileCalls != 1 || dispatch.State != DispatchSucceeded ||
		dispatch.ReconciliationVerdict != VerdictClean ||
		dispatch.ReconciliationEvidenceRef != "provider:test-evidence-1" {
		t.Fatalf("reconciliation rows/calls dispatch=%+v notify=%d reconcile=%d", dispatch, notifyCalls, reconcileCalls)
	}
}

func TestDeliveryDispatchReconcileResultWithSimultaneousErrorRemainsUnknown(t *testing.T) {
	t.Parallel()

	receipt := sha256.Sum256([]byte("discarded-reconciliation-receipt"))
	accepted, err := sdk.NewDeliveryReconciliationResult(
		sdk.DeliveryReconciliationAccepted, receipt[:], "provider:discarded-accepted",
	)
	if err != nil {
		t.Fatalf("accepted reconciliation: %v", err)
	}
	notAccepted, err := sdk.NewDeliveryReconciliationResult(
		sdk.DeliveryReconciliationNotAccepted, nil, "provider:discarded-not-accepted",
	)
	if err != nil {
		t.Fatalf("not-accepted reconciliation: %v", err)
	}
	for name, callbackResult := range map[string]sdk.DeliveryReconciliationResult{
		"accepted plus error":     accepted,
		"not accepted plus error": notAccepted,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newDeliveryDispatchServiceFixture(t)
			fixture.notifier.notifyErr = errors.New("prepare UNKNOWN")
			pumped, pumpErr := fixture.service.PumpOne(
				context.Background(), fixture.scope,
				deliveryDispatchPumpClaim{NodeID: "node-reconcile-dual", Epoch: 29},
			)
			if pumpErr != nil || pumped.State != DispatchUnknown {
				t.Fatalf("prepare UNKNOWN = %+v, %v", pumped, pumpErr)
			}
			fixture.notifier.reconcileResult = callbackResult
			fixture.notifier.reconcileErr = errors.New("simultaneous reconciliation error")

			reconciled, reconcileErr := fixture.service.ReconcileUnknown(
				context.Background(), fixture.scope, fixture.dispatch.ID,
			)
			if reconcileErr != nil || reconciled.Changed ||
				reconciled.State != DispatchUnknown || reconciled.AuditSeq < 1 {
				t.Fatalf("simultaneous reconciliation result/error = %+v, %v", reconciled, reconcileErr)
			}
			dispatch := deliveryDispatchRecordForTest(t, fixture)
			_, notifyCalls, reconcileCalls := fixture.notifier.snapshot()
			if dispatch.State != DispatchUnknown || dispatch.ReconciliationVerdict != "" ||
				dispatch.ReconciliationEvidenceRef != "" || len(dispatch.ProviderAcceptanceHash) != 0 ||
				notifyCalls != 1 || reconcileCalls != 1 {
				t.Fatalf("simultaneous reconciliation mutated authority: dispatch=%+v notify=%d reconcile=%d",
					dispatch, notifyCalls, reconcileCalls)
			}
		})
	}
}

func TestDeliveryDispatchClaimAuditAnchorAndFailureRollback(t *testing.T) {
	t.Parallel()

	t.Run("real anchor rejects fabricated sequence", func(t *testing.T) {
		fixture := newDeliveryDispatchServiceFixture(t)
		candidate, found, err := fixture.service.scanPendingCandidate(context.Background(), fixture.scope)
		if err != nil || !found {
			t.Fatalf("scan claim candidate = found:%v err:%v", found, err)
		}
		witness, err := fixture.notifier.Capabilities(context.Background(), candidate.endpoint)
		if err != nil {
			t.Fatalf("claim capability witness: %v", err)
		}
		owner, err := (deliveryDispatchPumpClaim{NodeID: "node-claim-audit", Epoch: 31}).owner()
		if err != nil {
			t.Fatalf("claim owner: %v", err)
		}
		claimed, won, err := fixture.service.claimCandidate(
			context.Background(), fixture.scope, candidate, witness, owner,
		)
		if err != nil || !won || claimed.auditSeq < 1 {
			t.Fatalf("claim result = won:%v audit:%d err:%v", won, claimed.auditSeq, err)
		}
		dispatchID := model.ID(claimed.dispatch.DispatchID())
		attemptID := model.ID(claimed.dispatch.AttemptID())
		if err := verifyDeliveryDispatchClaimAuditAnchor(
			context.Background(), fixture, dispatchID, attemptID, claimed.auditSeq,
		); err != nil {
			t.Fatalf("verify real claim anchor: %v", err)
		}
		if claimed.auditSeq == 1 {
			t.Fatal("claim fixture did not establish a prior audit event for the auditSeq=1 mutant")
		}
		if err := verifyDeliveryDispatchClaimAuditAnchor(
			context.Background(), fixture, dispatchID, attemptID, 1,
		); err == nil {
			t.Fatal("fabricated auditSeq=1 was accepted as the claim anchor")
		}
	})

	t.Run("exact append failure rolls back claim and Attempt", func(t *testing.T) {
		fixture := newDeliveryDispatchServiceFixture(t)
		candidate, found, err := fixture.service.scanPendingCandidate(context.Background(), fixture.scope)
		if err != nil || !found {
			t.Fatalf("scan rollback candidate = found:%v err:%v", found, err)
		}
		witness, err := fixture.notifier.Capabilities(context.Background(), candidate.endpoint)
		if err != nil {
			t.Fatalf("rollback capability witness: %v", err)
		}
		owner, err := (deliveryDispatchPumpClaim{NodeID: "node-claim-rollback", Epoch: 37}).owner()
		if err != nil {
			t.Fatalf("rollback claim owner: %v", err)
		}
		beforeHead := directNoticeAuditHead(t, fixture.directNoticeFixture)
		originalData := fixture.m.data
		failure := errors.New("exact forced claim audit failure")
		fixture.m.data = directNoticeCursorAuditFailureData{inner: originalData, failure: failure}
		claimed, won, claimErr := fixture.service.claimCandidate(
			context.Background(), fixture.scope, candidate, witness, owner,
		)
		fixture.m.data = originalData
		if !errors.Is(claimErr, failure) || won || claimed.auditSeq != 0 {
			t.Fatalf("claim audit failure = won:%v claimed:%+v err:%v", won, claimed, claimErr)
		}
		afterHead := directNoticeAuditHead(t, fixture.directNoticeFixture)
		dispatch := deliveryDispatchRecordForTest(t, fixture)
		if beforeHead.Seq != afterHead.Seq || !bytes.Equal(beforeHead.Hash, afterHead.Hash) ||
			dispatch.State != DispatchPending || dispatch.AttemptCount != 0 ||
			len(communicationRowsForTest(t, fixture.directNoticeFixture, deliveryAttemptKind)) != 0 {
			t.Fatalf("claim audit rollback leaked: before=%+v after=%+v dispatch=%+v",
				beforeHead, afterHead, dispatch)
		}
	})
}

func TestDeliveryDispatchSettlementAuditFailureRollsBackAtomically(t *testing.T) {
	t.Parallel()

	fixture := newDeliveryDispatchServiceFixture(t)
	receipt := sha256.Sum256([]byte("rollback-provider-receipt"))
	accepted, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptAccepted, sdk.DeliveryBoundaryCrossed, receipt[:],
	)
	if err != nil {
		t.Fatalf("accepted result: %v", err)
	}
	fixture.notifier.notifyResult = accepted
	originalData := fixture.m.data
	fixture.notifier.notifyHook = func(sdk.DeliveryDispatch) {
		fixture.m.data = directNoticeCursorAuditFailureData{
			inner: originalData, failure: errors.New("forced delivery audit failure"),
		}
	}

	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-g", Epoch: 4},
	)
	if err == nil || result.State != DispatchInFlight {
		t.Fatalf("audit failure pump = %+v, %v", result, err)
	}
	fixture.m.data = originalData
	dispatch := deliveryDispatchRecordForTest(t, fixture)
	attempt := deliveryAttemptRecordForTest(t, fixture)
	delivery := deliveryRecordForDispatchTest(t, fixture)
	if dispatch.State != DispatchInFlight || attempt.State != AttemptReserved ||
		delivery.LastWakeAt != nil || delivery.LastWakeCode != "" {
		t.Fatalf("partial settlement survived rollback: dispatch=%+v attempt=%+v delivery=%+v", dispatch, attempt, delivery)
	}
}

func TestDeliveryDispatchAdapterRejectsOverflowAndPreservesAckPrecision(t *testing.T) {
	t.Parallel()

	if _, err := deliverySDKGeneration(uint64(math.MaxInt64) + 1); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("overflow generation error = %v", err)
	}
	if got, err := deliverySDKGeneration(uint64(math.MaxInt64)); err != nil || got != math.MaxInt64 {
		t.Fatalf("max generation = %d, %v", got, err)
	}
	want := time.Date(2026, 8, 16, 9, 10, 11, 987654321, time.FixedZone("offset", 7200))
	got, err := deliverySDKTime(want)
	if err != nil || !got.Equal(want) || got.Nanosecond() != want.Nanosecond() || got.Location() != time.UTC {
		t.Fatalf("AckDueAt canonical round trip = %s (%d), %v", got, got.Nanosecond(), err)
	}
	if _, err := deliverySDKTime(time.Date(10000, 1, 1, 0, 0, 0, 1, time.UTC)); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("unrepresentable AckDueAt error = %v", err)
	}
}

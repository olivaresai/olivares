// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type directNoticeReadAuthorizer struct {
	now          time.Time
	nowFn        func() time.Time
	freshFor     time.Duration
	epoch        int64
	mu           sync.Mutex
	outcomes     map[model.ID]ReadOutcome
	outcomeQueue map[model.ID][]ReadOutcome
	denyCheck    map[model.ID]string
	beforeReturn map[model.ID]func()
	beforeQueue  map[model.ID][]func()
	calls        []EntityRef
}

func (a *directNoticeReadAuthorizer) AuthorizeEntityRead(
	_ context.Context,
	principal CommunicationPrincipal,
	entity EntityRef,
) (ReadWitness, error) {
	a.mu.Lock()
	a.calls = append(a.calls, entity)
	outcome := a.outcomes[entity.ID]
	if queued := a.outcomeQueue[entity.ID]; len(queued) > 0 {
		outcome = queued[0]
		a.outcomeQueue[entity.ID] = queued[1:]
	}
	gate := a.denyCheck[entity.ID]
	beforeReturn := a.beforeReturn[entity.ID]
	if queued := a.beforeQueue[entity.ID]; len(queued) > 0 {
		beforeReturn = queued[0]
		a.beforeQueue[entity.ID] = queued[1:]
	}
	nowFn := a.nowFn
	now := a.now
	freshFor := a.freshFor
	a.mu.Unlock()
	if beforeReturn != nil {
		beforeReturn()
	}
	if nowFn != nil {
		now = nowFn()
	}
	if freshFor == 0 {
		freshFor = 5 * time.Minute
	}
	if outcome == "" {
		outcome = ReadAllow
	}
	clean := func(code string) AuthorityEvidence {
		return AuthorityEvidence{
			Verdict: VerdictClean, Code: code, EvidenceRef: "direct_notice_read_" + code,
		}
	}
	corePermission := clean("core_permission")
	resourceGuard := clean("resource_guard")
	forbidAbsence := clean("forbid_absence")
	if outcome != ReadAllow {
		verdict := VerdictBroken
		if outcome == ReadUnknown {
			verdict = VerdictUnknown
		}
		evidence := AuthorityEvidence{
			Verdict: verdict, Code: "read_decision",
			EvidenceRef: "direct_notice_read_decision",
		}
		switch gate {
		case "resource_guard":
			resourceGuard = evidence
		case "forbid_absence":
			forbidAbsence = evidence
		default:
			corePermission = evidence
		}
	}
	return ReadWitness{
		Outcome: outcome, Code: "direct_notice_read", Entity: entity,
		Operation: CommunicationRead, Principal: principal, ObservedAt: now,
		FreshUntil: now.Add(freshFor), CorePermission: corePermission,
		ResourceGuard: resourceGuard, ForbidAbsence: forbidAbsence,
		Facts: []store.AuthorizationFactRef{{
			Kind: model.DirectoryEpochKind, ID: model.ID(entity.TenantID), Version: a.epoch,
		}},
		EvidenceRef: "direct_notice_read_authority",
	}, nil
}

func (a *directNoticeReadAuthorizer) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *directNoticeReadAuthorizer) callCountFor(id model.ID) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := 0
	for _, entity := range a.calls {
		if entity.ID == id {
			count++
		}
	}
	return count
}

type directNoticeReadDirectoryResolver struct {
	now          time.Time
	nowFn        func() time.Time
	freshFor     time.Duration
	epoch        int64
	outcome      PrincipalResolutionOutcome
	beforeReturn func()
	calls        atomic.Int64
}

func (r *directNoticeReadDirectoryResolver) ResolveAudience(
	context.Context,
	DirectoryScopeRef,
	[]AudienceSelector,
) (DirectorySnapshot, error) {
	return DirectorySnapshot{}, errors.New("unexpected ResolveAudience call")
}

func (r *directNoticeReadDirectoryResolver) ResolveRecipient(
	_ context.Context,
	scope DirectoryScopeRef,
	recipient RecipientRef,
) (RecipientSnapshot, error) {
	return RecipientSnapshot{
		Scope: scope, Recipient: recipient, RecipientEpoch: 1,
		DirectoryEpoch: r.epoch, Eligible: true,
	}, nil
}

func (r *directNoticeReadDirectoryResolver) ResolvePrincipal(
	_ context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	r.calls.Add(1)
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	now := r.now
	if r.nowFn != nil {
		now = r.nowFn()
	}
	freshFor := r.freshFor
	if freshFor == 0 {
		freshFor = 5 * time.Minute
	}
	outcome := r.outcome
	if outcome == "" {
		outcome = PrincipalResolved
	}
	result := PrincipalResolution{
		Outcome: outcome, Code: "principal_" + string(outcome), Scope: scope,
		Principal: principal, ObservedAt: now, FreshUntil: now.Add(freshFor),
	}
	if outcome == PrincipalResolved {
		result.Recipient = &RecipientSnapshot{
			Scope:          scope,
			Recipient:      RecipientRef{Kind: RecipientUser, Ref: principal.UserID.String()},
			RecipientEpoch: 1, DirectoryEpoch: r.epoch, Eligible: true,
		}
	}
	return result, nil
}

type directNoticeReadClosureResolver struct {
	now          time.Time
	nowFn        func() time.Time
	freshFor     time.Duration
	epoch        int64
	outcome      ReadOutcome
	beforeReturn func()
	calls        atomic.Int64
}

func (r *directNoticeReadClosureResolver) ResolveChannelGrantSubjects(
	_ context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	r.calls.Add(1)
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	now := r.now
	if r.nowFn != nil {
		now = r.nowFn()
	}
	freshFor := r.freshFor
	if freshFor == 0 {
		freshFor = 5 * time.Minute
	}
	outcome := r.outcome
	if outcome == "" {
		outcome = ReadAllow
	}
	return ChannelGrantSubjectClosure{
		Scope: scope, Principal: principal, DirectoryEpoch: r.epoch,
		Outcome: outcome, Code: "closure_decision",
		Subjects:   []CommunicationSubjectRef{{Kind: SubjectUser, Ref: principal.UserID.String()}},
		ObservedAt: now, FreshUntil: now.Add(freshFor),
		EvidenceRef: "direct_notice_read_closure",
	}, nil
}

type directNoticeReadFixture struct {
	directNoticeFixture
	principal  CommunicationPrincipal
	authorizer *directNoticeReadAuthorizer
	resolver   *directNoticeReadDirectoryResolver
	closure    *directNoticeReadClosureResolver
}

func newDirectNoticeReadFixture(t *testing.T, count int) (directNoticeReadFixture, []DirectNoticePublishResult) {
	t.Helper()
	fixture := newDirectNoticeFixture(t)
	results := make([]DirectNoticePublishResult, 0, count)
	for index := 0; index < count; index++ {
		result, err := fixture.m.publishDirectNotice(
			context.Background(), fixture.scope,
			CommunicationPrincipal{UserID: fixture.sender},
			fixture.command(model.NewID(), "read canary "+string(rune('A'+index))),
		)
		if err != nil {
			t.Fatalf("publish direct notice %d: %v", index, err)
		}
		results = append(results, result)
	}
	principal := CommunicationPrincipal{UserID: fixture.recipient}
	authorizer := &directNoticeReadAuthorizer{
		now: fixture.now, epoch: fixture.epoch,
		outcomes: make(map[model.ID]ReadOutcome), outcomeQueue: make(map[model.ID][]ReadOutcome),
		denyCheck:    make(map[model.ID]string),
		beforeReturn: make(map[model.ID]func()),
		beforeQueue:  make(map[model.ID][]func()),
	}
	resolver := &directNoticeReadDirectoryResolver{now: fixture.now, epoch: fixture.epoch}
	closure := &directNoticeReadClosureResolver{now: fixture.now, epoch: fixture.epoch}
	fixture.m.communicationReadAuthorizer = authorizer
	fixture.m.communicationDirectoryResolver = resolver
	fixture.m.communicationGrantClosure = closure
	return directNoticeReadFixture{
		directNoticeFixture: fixture, principal: principal, authorizer: authorizer,
		resolver: resolver, closure: closure,
	}, results
}

func createDirectNoticeReadChannel(
	t *testing.T,
	fixture directNoticeReadFixture,
) (Channel, model.ID) {
	t.Helper()
	ctx := context.Background()
	channelID := model.NewID()
	input := communicationChannelRecord(fixture.workspace, "direct-notice-read-batch")
	input[colCommDefaultAckPolicy] = string(AckPolicyNone)
	input[colCommDefaultAckTimeoutMS] = int64(0)
	record, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, channelKind, channelID, input,
	)
	if err != nil {
		t.Fatalf("create batch read Channel: %v", err)
	}
	channel, err := channelFromRecord(record)
	if err != nil {
		t.Fatalf("decode batch read Channel: %v", err)
	}
	createGrant := func(subject model.ID, canWrite bool) model.ID {
		t.Helper()
		grant := ChannelGrant{
			MutableCommunicationEntity: MutableCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: model.NewID(), TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					Version: 1, CreatedAt: fixture.now,
				},
				UpdatedAt: fixture.now,
			},
			ChannelID:  channel.ID,
			Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: subject.String()},
			Generation: 1, CanRead: true, CanWrite: canWrite, State: ChannelGrantActive,
			GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		}
		grantRecord, encodeErr := channelGrantToRecord(grant)
		if encodeErr != nil {
			t.Fatalf("encode batch read ChannelGrant: %v", encodeErr)
		}
		if _, createErr := communicationCreateWithID(
			ctx, fixture.m, fixture.tenant, channelGrantKind, grant.ID, grantRecord,
		); createErr != nil {
			t.Fatalf("create batch read ChannelGrant: %v", createErr)
		}
		return grant.ID
	}
	createGrant(fixture.sender, true)
	recipientGrantID := createGrant(fixture.recipient, false)
	if err := fixture.m.ReconcileCommunicationGuards(
		ctx, fixture.tenant, CommunicationGuardReconcileStaged,
	); err != nil {
		t.Fatalf("reconcile batch read Channel guards: %v", err)
	}
	return channel, recipientGrantID
}

type directNoticeMutateObserverData struct {
	inner    api.ModuleData
	inMutate atomic.Bool
	views    atomic.Int64
	mutates  atomic.Int64
}

type directNoticeAuthorityFaultData struct {
	inner  api.ModuleData
	failAt int64
	err    error
	calls  atomic.Int64
}

type directNoticeAuthorityFaultScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	fault     *directNoticeAuthorityFaultData
}

func (d *directNoticeAuthorityFaultData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeAuthorityFaultData) Mutate(
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
			return errors.New("direct notice authority fault scope lacks transaction capabilities")
		}
		return fn(&directNoticeAuthorityFaultScope{
			Scope: scope, clock: clock, locker: locker,
			authority: authority, directory: directory, fault: d,
		})
	})
}

func (s *directNoticeAuthorityFaultScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeAuthorityFaultScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeAuthorityFaultScope) LockAuthoritySnapshot(
	ctx context.Context,
	refs []store.AuthorizationFactRef,
) error {
	if s.fault.calls.Add(1) == s.fault.failAt {
		return s.fault.err
	}
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *directNoticeAuthorityFaultScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeAuthorityFaultScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (d *directNoticeMutateObserverData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.views.Add(1)
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeMutateObserverData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.mutates.Add(1)
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		if !d.inMutate.CompareAndSwap(false, true) {
			return errors.New("nested direct notice mutation")
		}
		defer d.inMutate.Store(false)
		return fn(sc)
	})
}

func TestGetDirectNoticeMessageIsPureAndOpensAfterCommit(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 1)
	before := make(map[model.Kind][]model.Record)
	for _, kind := range []model.Kind{
		messageKind, messageDeliveryKind, messageAudienceKind, messageAudienceRecipientKind,
	} {
		before[kind] = communicationRowsForTest(t, fixture.directNoticeFixture, kind)
	}
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	result, err := fixture.m.getDirectNoticeMessageWithOpener(
		context.Background(), fixture.scope, fixture.principal, published[0].MessageID,
		func(
			ctx context.Context,
			sealer CommunicationContentSealer,
			plan ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			if observer.inMutate.Load() {
				return nil, errors.New("payload opened before communication Mutate committed")
			}
			return OpenProtectedPayload(ctx, sealer, plan)
		},
	)
	if err != nil {
		t.Fatalf("get direct notice: %v", err)
	}
	if opens.Load() != 1 || result.Message.ID != published[0].MessageID ||
		result.Delivery.ID != published[0].DeliveryID || result.Message.Content.Subject != "Direct notice" ||
		result.Message.Content.Blocks[0].Text != "read canary A" ||
		result.Fulfillment != (FulfillmentProjection{State: FulfillmentNotRequired}) {
		t.Fatalf("read result = %+v, opens=%d", result, opens.Load())
	}
	for kind, want := range before {
		got := communicationRowsForTest(t, fixture.directNoticeFixture, kind)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pure read changed %s rows\nbefore=%#v\nafter=%#v", kind, want, got)
		}
	}
}

func TestDirectNoticePreflightRechecksCoreBeforeCarrierView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		expire       func(*directNoticeReadFixture, *testClock)
		closureCalls int64
	}{
		{
			name: "expiry after principal resolution",
			expire: func(fixture *directNoticeReadFixture, clock *testClock) {
				fixture.resolver.nowFn = clock.get
				fixture.resolver.beforeReturn = func() { clock.advance(2 * time.Minute) }
			},
			closureCalls: 0,
		},
		{
			name: "expiry after closure",
			expire: func(fixture *directNoticeReadFixture, clock *testClock) {
				fixture.closure.nowFn = clock.get
				fixture.closure.beforeReturn = func() { clock.advance(2 * time.Minute) }
			},
			closureCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, published := newDirectNoticeReadFixture(t, 1)
			clock, ok := fixture.m.clock.(*testClock)
			if !ok {
				t.Fatalf("direct notice clock = %T, want *testClock", fixture.m.clock)
			}
			fixture.authorizer.freshFor = time.Minute
			fixture.resolver.freshFor = 10 * time.Minute
			fixture.closure.freshFor = 10 * time.Minute
			test.expire(&fixture, clock)
			observer := &directNoticeMutateObserverData{inner: fixture.m.data}
			fixture.m.data = observer
			var opens atomic.Int64
			_, err := fixture.m.getDirectNoticeMessageWithOpener(
				context.Background(), fixture.scope, fixture.principal, published[0].MessageID,
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected open")
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				fixture.resolver.calls.Load() != 1 ||
				fixture.closure.calls.Load() != test.closureCalls ||
				observer.views.Load() != 0 || observer.mutates.Load() != 0 || opens.Load() != 0 {
				t.Fatalf(
					"expired preflight = %v; resolver=%d closure=%d views=%d mutates=%d opens=%d",
					err, fixture.resolver.calls.Load(), fixture.closure.calls.Load(),
					observer.views.Load(), observer.mutates.Load(), opens.Load(),
				)
			}
		})
	}
}

func TestGetDirectNoticeMessageUsesHistoricalPayloadGeneration(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 1)
	err := fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
		record, err := tx.lockRecord(context.Background(), channelKind, fixture.channel.ID)
		if err != nil {
			return err
		}
		before, err := channelFromRecord(record)
		if err != nil {
			return err
		}
		after := before
		after.ContentProtection = ContentProtectionApplicationSealed
		after.ProtectionGeneration++
		after.Version++
		after.UpdatedAt = tx.now.Time()
		if err := ValidateChannelUpdate(before, after); err != nil {
			return err
		}
		updated, err := channelToRecord(after)
		if err != nil {
			return err
		}
		updated[model.ColVersion] = before.Version
		_, err = tx.update(context.Background(), channelKind, updated)
		return err
	})
	if err != nil {
		t.Fatalf("advance Channel protection generation: %v", err)
	}
	result, err := fixture.m.getDirectNoticeMessage(
		context.Background(), fixture.scope, fixture.principal, published[0].MessageID,
	)
	if err != nil || result.Message.Content.Blocks[0].Text != "read canary A" {
		t.Fatalf("historical plain payload read = %+v, %v", result, err)
	}
}

func TestDirectNoticePointReadNormalizesEveryBrokenGateAndCrossScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*directNoticeReadFixture, DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal)
	}{
		{
			name: "core permission",
			setup: func(f *directNoticeReadFixture, result DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				f.authorizer.outcomes[result.MessageID] = ReadDeny
				return f.scope, f.principal
			},
		},
		{
			name: "entity recipient guard",
			setup: func(f *directNoticeReadFixture, result DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				f.authorizer.outcomes[result.MessageID] = ReadDeny
				f.authorizer.denyCheck[result.MessageID] = "resource_guard"
				return f.scope, f.principal
			},
		},
		{
			name: "forbid",
			setup: func(f *directNoticeReadFixture, result DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				f.authorizer.outcomes[result.MessageID] = ReadDeny
				f.authorizer.denyCheck[result.MessageID] = "forbid_absence"
				return f.scope, f.principal
			},
		},
		{
			name: "channel grant",
			setup: func(f *directNoticeReadFixture, _ DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				f.closure.outcome = ReadDeny
				return f.scope, f.principal
			},
		},
		{
			name: "audience causality",
			setup: func(f *directNoticeReadFixture, _ DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				return f.scope, CommunicationPrincipal{UserID: f.sender}
			},
		},
		{
			name: "cross workspace",
			setup: func(f *directNoticeReadFixture, _ DirectNoticePublishResult) (DirectoryScopeRef, CommunicationPrincipal) {
				return DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: model.NewID()}, f.principal
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, published := newDirectNoticeReadFixture(t, 1)
			scope, principal := test.setup(&fixture, published[0])
			_, err := fixture.m.getDirectNoticeMessage(
				context.Background(), scope, principal, published[0].MessageID,
			)
			if !errors.Is(err, ErrCommunicationNotFound) ||
				errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("broken point gate error = %v, want normalized not found", err)
			}
		})
	}
}

func TestDirectNoticePrincipalNotFoundPoint404AndInboxEmpty(t *testing.T) {
	t.Parallel()

	point, published := newDirectNoticeReadFixture(t, 1)
	point.resolver.outcome = PrincipalNotFound
	_, err := point.m.getDirectNoticeMessage(
		context.Background(), point.scope, point.principal, published[0].MessageID,
	)
	if !errors.Is(err, ErrCommunicationNotFound) ||
		errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("PrincipalNotFound point error = %v, want not found", err)
	}

	for _, history := range []int{0, 2} {
		t.Run(fmt.Sprintf("history_%d", history), func(t *testing.T) {
			fixture, rows := newDirectNoticeReadFixture(t, history)
			fixture.resolver.outcome = PrincipalNotFound
			query := DirectNoticeInboxQuery{AfterDeliverySeq: 1, Limit: 1}
			if history > 0 && len(rows) != 2 {
				t.Fatalf("history fixture rows = %d", len(rows))
			}
			var opens atomic.Int64
			page, listErr := fixture.m.listDirectNoticeInboxWithOpener(
				context.Background(), fixture.scope, fixture.principal, query,
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected open")
				},
			)
			if listErr != nil || page.Items == nil || len(page.Items) != 0 || page.HasMore ||
				page.NextAfterDeliverySeq != query.AfterDeliverySeq || opens.Load() != 0 {
				t.Fatalf("PrincipalNotFound inbox = %+v, %v; opens=%d", page, listErr, opens.Load())
			}
			wantResolverCalls := int64(0)
			if history > 0 {
				wantResolverCalls = 1
			}
			if fixture.resolver.calls.Load() != wantResolverCalls {
				t.Fatalf(
					"PrincipalNotFound resolver calls = %d, want %d",
					fixture.resolver.calls.Load(), wantResolverCalls,
				)
			}
		})
	}
}

func TestListDirectNoticeInboxFiltersBeforeVisiblePagination(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 3)
	fixture.authorizer.outcomes[published[1].DeliveryID] = ReadDeny
	first, err := fixture.m.listDirectNoticeInbox(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 1},
	)
	if err != nil {
		t.Fatalf("list first visible page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Delivery.ID != published[0].DeliveryID ||
		!first.HasMore || first.NextAfterDeliverySeq != first.Items[0].Delivery.DeliverySeq {
		t.Fatalf("first filtered page = %+v", first)
	}
	second, err := fixture.m.listDirectNoticeInbox(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{AfterDeliverySeq: first.NextAfterDeliverySeq, Limit: 1},
	)
	if err != nil {
		t.Fatalf("list second visible page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Delivery.ID != published[2].DeliveryID ||
		second.HasMore || second.NextAfterDeliverySeq != second.Items[0].Delivery.DeliverySeq {
		t.Fatalf("second filtered page = %+v", second)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal inbox page: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("decode inbox page shape: %v", err)
	}
	for _, forbidden := range []string{"total", "candidate_count", "scanned_count"} {
		if _, present := shape[forbidden]; present {
			t.Fatalf("inbox page leaked %q: %s", forbidden, raw)
		}
	}
}

func TestListDirectNoticeInboxFinalBatchIsOneShotAndPreservesDeliveryOrder(t *testing.T) {
	t.Parallel()

	fixture, _ := newDirectNoticeReadFixture(t, 0)
	secondChannel, _ := createDirectNoticeReadChannel(t, fixture)
	low, high := fixture.channel, secondChannel
	if low.ID.String() > high.ID.String() {
		low, high = high, low
	}
	ordered := directNoticeSortedIDSet(map[model.ID]struct{}{
		high.ID: {},
		low.ID:  {},
	})
	if len(ordered) != 2 || ordered[0] != low.ID || ordered[1] != high.ID {
		t.Fatalf("batch Channel lock order = %v, want [%s %s]", ordered, low.ID, high.ID)
	}
	publish := func(channel Channel, canary string) DirectNoticePublishResult {
		t.Helper()
		command := fixture.command(model.NewID(), canary)
		command.ChannelID = channel.ID
		result, err := fixture.m.publishDirectNotice(
			context.Background(), fixture.scope,
			CommunicationPrincipal{UserID: fixture.sender}, command,
		)
		if err != nil {
			t.Fatalf("publish %s batch read notice: %v", canary, err)
		}
		return result
	}
	first := publish(high, "batch high")
	second := publish(low, "batch low")
	resolverCallsBefore := fixture.resolver.calls.Load()
	closureCallsBefore := fixture.closure.calls.Load()
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 2},
		func(
			ctx context.Context,
			sealer CommunicationContentSealer,
			plan ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			if observer.inMutate.Load() {
				return nil, errors.New("batch payload opened before authorization commit")
			}
			return OpenProtectedPayload(ctx, sealer, plan)
		},
	)
	if err != nil || len(page.Items) != 2 || page.Items[0].Message.ID != first.MessageID ||
		page.Items[1].Message.ID != second.MessageID || page.HasMore || opens.Load() != 2 ||
		observer.mutates.Load() != 3 || fixture.resolver.calls.Load()-resolverCallsBefore != 2 ||
		fixture.closure.calls.Load()-closureCallsBefore != 2 {
		t.Fatalf(
			"one-shot batch page = %+v, %v; mutates=%d resolver_delta=%d closure_delta=%d opens=%d",
			page, err, observer.mutates.Load(), fixture.resolver.calls.Load()-resolverCallsBefore,
			fixture.closure.calls.Load()-closureCallsBefore, opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxFinalBatchSeesEarlierGrantRevokedByLaterCore(t *testing.T) {
	t.Parallel()

	fixture, _ := newDirectNoticeReadFixture(t, 0)
	firstChannel, firstRecipientGrantID := createDirectNoticeReadChannel(t, fixture)
	publish := func(channel Channel, canary string) DirectNoticePublishResult {
		t.Helper()
		command := fixture.command(model.NewID(), canary)
		command.ChannelID = channel.ID
		result, err := fixture.m.publishDirectNotice(
			context.Background(), fixture.scope,
			CommunicationPrincipal{UserID: fixture.sender}, command,
		)
		if err != nil {
			t.Fatalf("publish %s revocation notice: %v", canary, err)
		}
		return result
	}
	first := publish(firstChannel, "batch revoke first")
	second := publish(fixture.channel, "batch revoke second")
	var revokeErr error
	fixture.authorizer.beforeQueue[second.DeliveryID] = []func(){nil, func() {
		revokeErr = fixture.m.mutateCommunication(
			context.Background(), fixture.scope, func(tx *communicationTx) error {
				record, err := tx.lockRecord(
					context.Background(), channelGrantKind, firstRecipientGrantID,
				)
				if err != nil {
					return err
				}
				grant, err := channelGrantFromRecord(record)
				if err != nil {
					return err
				}
				beforeVersion := grant.Version
				actor := CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()}
				grant.State = ChannelGrantRevoked
				grant.RevokedBy = &actor
				grant.Version++
				grant.UpdatedAt = tx.now.Time()
				updated, err := channelGrantToRecord(grant)
				if err != nil {
					return err
				}
				updated[model.ColVersion] = beforeVersion
				_, err = tx.update(context.Background(), channelGrantKind, updated)
				return err
			},
		)
	}}
	resolverCallsBefore := fixture.resolver.calls.Load()
	closureCallsBefore := fixture.closure.calls.Load()
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 2},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if revokeErr != nil || !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCountFor(first.DeliveryID) != 2 ||
		fixture.authorizer.callCountFor(second.DeliveryID) != 2 ||
		fixture.resolver.calls.Load()-resolverCallsBefore != 2 ||
		fixture.closure.calls.Load()-closureCallsBefore != 2 ||
		observer.views.Load() != 5 || observer.mutates.Load() != 4 {
		t.Fatalf(
			"batch revocation = %+v, %v (revoke=%v); first_auth=%d second_auth=%d resolver_delta=%d closure_delta=%d views=%d mutates=%d opens=%d",
			page, err, revokeErr, fixture.authorizer.callCountFor(first.DeliveryID),
			fixture.authorizer.callCountFor(second.DeliveryID),
			fixture.resolver.calls.Load()-resolverCallsBefore,
			fixture.closure.calls.Load()-closureCallsBefore,
			observer.views.Load(), observer.mutates.Load(), opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxUnknownAbortsWholePageBeforeOpen(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 2)
	fixture.authorizer.outcomes[published[1].DeliveryID] = ReadUnknown
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 2},
		func(
			ctx context.Context,
			sealer CommunicationContentSealer,
			plan ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return OpenProtectedPayload(ctx, sealer, plan)
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 {
		t.Fatalf("UNKNOWN inbox result = %+v, %v, opens=%d", page, err, opens.Load())
	}
}

func TestListDirectNoticeInboxTailExpiryStopsNextCarrierView(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 3)
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatalf("direct notice clock = %T, want *testClock", fixture.m.clock)
	}
	fixture.authorizer.nowFn = clock.get
	fixture.authorizer.outcomes[published[1].DeliveryID] = ReadDeny
	fixture.authorizer.beforeReturn[published[1].DeliveryID] = func() {
		clock.advance(6 * time.Minute)
	}
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 3},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCount() != 3 || fixture.resolver.calls.Load() != 1 ||
		fixture.closure.calls.Load() != 1 || observer.views.Load() != 2 ||
		observer.mutates.Load() != 1 {
		t.Fatalf(
			"expired candidate tail = %+v, %v; auth=%d resolver=%d closure=%d views=%d mutates=%d opens=%d",
			page, err, fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(), observer.views.Load(), observer.mutates.Load(), opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxTailExpiryRevalidatesBeforeOpen(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 2)
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatalf("direct notice clock = %T, want *testClock", fixture.m.clock)
	}
	fixture.authorizer.nowFn = clock.get
	fixture.authorizer.outcomes[published[1].DeliveryID] = ReadDeny
	fixture.authorizer.beforeReturn[published[1].DeliveryID] = func() {
		clock.advance(6 * time.Minute)
	}
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 2},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCount() != 3 || fixture.resolver.calls.Load() != 2 ||
		fixture.closure.calls.Load() != 1 || observer.views.Load() != 2 {
		t.Fatalf(
			"expired tail = %+v, %v; auth=%d resolver=%d closure=%d views=%d opens=%d",
			page, err, fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(), observer.views.Load(), opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxBrokenTailRevocationAbortsBeforeOpen(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 2)
	fixture.authorizer.outcomes[published[1].DeliveryID] = ReadDeny
	fixture.authorizer.beforeReturn[published[1].DeliveryID] = func() {
		fixture.closure.outcome = ReadDeny
	}
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 2},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCount() != 3 || fixture.resolver.calls.Load() != 2 ||
		fixture.closure.calls.Load() != 2 || observer.views.Load() != 3 ||
		observer.mutates.Load() != 2 {
		t.Fatalf(
			"BROKEN revoked tail = %+v, %v; auth=%d resolver=%d closure=%d views=%d mutates=%d opens=%d",
			page, err, fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(), observer.views.Load(), observer.mutates.Load(), opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxRevokedLookaheadAbortsInsteadOfSkippingLaterCandidate(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 3)
	fixture.authorizer.outcomeQueue[published[1].DeliveryID] = []ReadOutcome{
		ReadAllow,
		ReadDeny,
	}
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 1},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCount() != 4 ||
		fixture.authorizer.callCountFor(published[2].DeliveryID) != 0 ||
		fixture.resolver.calls.Load() != 1 || fixture.closure.calls.Load() != 1 ||
		observer.views.Load() != 3 || observer.mutates.Load() != 2 {
		t.Fatalf(
			"revoked lookahead = %+v, %v; auth=%d later=%d resolver=%d closure=%d views=%d mutates=%d opens=%d",
			page, err, fixture.authorizer.callCount(),
			fixture.authorizer.callCountFor(published[2].DeliveryID),
			fixture.resolver.calls.Load(), fixture.closure.calls.Load(),
			observer.views.Load(), observer.mutates.Load(), opens.Load(),
		)
	}
}

func TestListDirectNoticeInboxCandidateBoundIsDenyClosed(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 3)
	for _, result := range published {
		fixture.authorizer.outcomes[result.DeliveryID] = ReadDeny
	}
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxBoundedWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 1}, 2,
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || opens.Load() != 0 ||
		fixture.authorizer.callCount() != 2 || fixture.resolver.calls.Load() != 0 ||
		fixture.closure.calls.Load() != 0 {
		t.Fatalf(
			"candidate overflow = %+v, %v; auth=%d resolver=%d closure=%d opens=%d",
			page, err, fixture.authorizer.callCount(), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(), opens.Load(),
		)
	}
}

func TestDirectNoticeReadCursorRejectsNoProgressAndCycles(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		current string
		next    string
		rows    int
		seen    map[string]struct{}
	}{
		{name: "empty", current: "cursor-a", next: "", rows: 1, seen: map[string]struct{}{}},
		{name: "same", current: "cursor-a", next: "cursor-a", rows: 1, seen: map[string]struct{}{}},
		{
			name: "empty continuation page", current: "cursor-a", next: "cursor-b",
			rows: 0, seen: map[string]struct{}{},
		},
		{
			name: "cycle", current: "cursor-b", next: "cursor-a",
			rows: 1, seen: map[string]struct{}{"cursor-a": {}, "cursor-b": {}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := advanceDirectNoticeReadCursor(test.current, test.next, test.rows, test.seen)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("cursor %q -> %q error = %v", test.current, test.next, err)
			}
		})
	}
	seen := make(map[string]struct{})
	first, err := advanceDirectNoticeReadCursor("", "cursor-a", 1, seen)
	if err != nil {
		t.Fatalf("first cursor: %v", err)
	}
	second, err := advanceDirectNoticeReadCursor(first, "cursor-b", 1, seen)
	if err != nil || second != "cursor-b" {
		t.Fatalf("second cursor = %q, %v", second, err)
	}
}

func TestDirectNoticeReadBatchFactUnionFailsBeforeMutate(t *testing.T) {
	t.Parallel()

	fixture, _ := newDirectNoticeReadFixture(t, 0)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	makeInput := func(sequence int64, facts []store.AuthorizationFactRef) directNoticeReadAuthorizationInput {
		deliveryID := model.NewID()
		return directNoticeReadAuthorizationInput{
			Preflight: directNoticeReaderPreflight{
				Scope: fixture.scope, Principal: fixture.principal,
				Recipient:  RecipientRef{Kind: RecipientUser, Ref: fixture.principal.UserID.String()},
				Resolution: PrincipalResolution{Recipient: &RecipientSnapshot{DirectoryEpoch: fixture.epoch}},
				Core:       ReadWitness{Outcome: ReadAllow}, Facts: facts,
			},
			IDs: directNoticeCarrierIDs{
				MessageID: model.NewID(), DeliveryID: deliveryID,
				ChannelID: fixture.channel.ID, DeliverySeq: sequence,
			},
		}
	}
	tooMany := make([]store.AuthorizationFactRef, 65)
	for index := range tooMany {
		tooMany[index] = store.AuthorizationFactRef{
			Kind: model.Kind("core.identity"), ID: model.NewID(), Version: 1,
		}
	}
	conflictID := model.NewID()
	for _, test := range []struct {
		name   string
		inputs []directNoticeReadAuthorizationInput
	}{
		{
			name: "over 64",
			inputs: []directNoticeReadAuthorizationInput{
				makeInput(1, tooMany[:33]), makeInput(2, tooMany[33:]),
			},
		},
		{
			name: "version conflict",
			inputs: []directNoticeReadAuthorizationInput{
				makeInput(1, []store.AuthorizationFactRef{{
					Kind: model.Kind("core.identity"), ID: conflictID, Version: 1,
				}}),
				makeInput(2, []store.AuthorizationFactRef{{
					Kind: model.Kind("core.identity"), ID: conflictID, Version: 2,
				}}),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := observer.mutates.Load()
			plans, err := fixture.m.authorizeDirectNoticeReadBatch(
				context.Background(), fixture.scope, test.inputs,
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || plans != nil ||
				observer.mutates.Load() != before {
				t.Fatalf(
					"batch fact union = %#v, %v; mutates=%d want %d",
					plans, err, observer.mutates.Load(), before,
				)
			}
		})
	}
}

func TestDirectNoticeAuthorizationConflictIsEvidenceUnknown(t *testing.T) {
	t.Parallel()

	err := normalizeDirectNoticeAuthorizationError(store.ErrConflict)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("authorization conflict error = %v, want evidence unknown", err)
	}
	for _, authorityErr := range []error{store.ErrConflict, store.ErrNotFound} {
		err = normalizeDirectNoticeAuthorityLockError(authorityErr)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("authority lock error %v = %v, want evidence unknown", authorityErr, err)
		}
	}
	sentinel := errors.New("unchanged")
	if got := normalizeDirectNoticeAuthorizationError(sentinel); got != sentinel {
		t.Fatalf("non-conflict error = %v, want original sentinel", got)
	}
	if got := normalizeDirectNoticeAuthorityLockError(sentinel); got != sentinel {
		t.Fatalf("non-authority error = %v, want original sentinel", got)
	}
}

func TestDirectNoticeMissingAuthorityFactIsUnknownAndNeverOpens(t *testing.T) {
	t.Parallel()

	t.Run("point", func(t *testing.T) {
		fixture, published := newDirectNoticeReadFixture(t, 1)
		fault := &directNoticeAuthorityFaultData{
			inner: fixture.m.data, failAt: 1, err: store.ErrNotFound,
		}
		fixture.m.data = fault
		var opens atomic.Int64
		result, err := fixture.m.getDirectNoticeMessageWithOpener(
			context.Background(), fixture.scope, fixture.principal, published[0].MessageID,
			func(
				context.Context,
				CommunicationContentSealer,
				ProtectedPayloadOpenPlan,
			) (json.RawMessage, error) {
				opens.Add(1)
				return nil, errors.New("unexpected open")
			},
		)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
			!reflect.DeepEqual(result, DirectNoticeReadResult{}) ||
			fault.calls.Load() != 1 || opens.Load() != 0 {
			t.Fatalf(
				"missing point authority fact = %+v, %v; locks=%d opens=%d",
				result, err, fault.calls.Load(), opens.Load(),
			)
		}
	})

	t.Run("inbox final batch", func(t *testing.T) {
		fixture, _ := newDirectNoticeReadFixture(t, 1)
		fault := &directNoticeAuthorityFaultData{
			inner: fixture.m.data, failAt: 2, err: store.ErrNotFound,
		}
		fixture.m.data = fault
		var opens atomic.Int64
		page, err := fixture.m.listDirectNoticeInboxWithOpener(
			context.Background(), fixture.scope, fixture.principal,
			DirectNoticeInboxQuery{Limit: 1},
			func(
				context.Context,
				CommunicationContentSealer,
				ProtectedPayloadOpenPlan,
			) (json.RawMessage, error) {
				opens.Add(1)
				return nil, errors.New("unexpected open")
			},
		)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
			!reflect.DeepEqual(page, DirectNoticeInboxPage{}) ||
			fault.calls.Load() != 2 || opens.Load() != 0 {
			t.Fatalf(
				"missing batch authority fact = %+v, %v; locks=%d opens=%d",
				page, err, fault.calls.Load(), opens.Load(),
			)
		}
	})
}

func TestListDirectNoticeInboxCoreBrokenDoesNotResolveOrOpen(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 1)
	fixture.authorizer.outcomes[published[0].DeliveryID] = ReadDeny
	var opens atomic.Int64
	page, err := fixture.m.listDirectNoticeInboxWithOpener(
		context.Background(), fixture.scope, fixture.principal,
		DirectNoticeInboxQuery{Limit: 1},
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected open")
		},
	)
	if err != nil || len(page.Items) != 0 || page.HasMore ||
		fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 || opens.Load() != 0 {
		t.Fatalf(
			"BROKEN inbox = %+v, %v; resolver=%d closure=%d opens=%d",
			page, err, fixture.resolver.calls.Load(), fixture.closure.calls.Load(), opens.Load(),
		)
	}
	raw, marshalErr := json.Marshal(DirectNoticeInboxPage{})
	if marshalErr != nil || !strings.Contains(string(raw), `"items":[]`) {
		t.Fatalf("zero inbox page must encode items as []: %s, %v", raw, marshalErr)
	}
}

func TestDirectNoticeReadPublicBoundaryRemainsOffUntilFullReadiness(t *testing.T) {
	t.Parallel()

	fixture, published := newDirectNoticeReadFixture(t, 1)
	beforeCalls := fixture.authorizer.callCount()
	_, err := fixture.m.GetDirectNoticeMessage(
		context.Background(), fixture.scope, fixture.ref, published[0].MessageID,
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		fixture.authorizer.callCount() != beforeCalls {
		t.Fatalf("public point read = %v; authorizer calls=%d", err, fixture.authorizer.callCount())
	}
	_, err = fixture.m.ListDirectNoticeInbox(
		context.Background(), fixture.scope, fixture.ref, DirectNoticeInboxQuery{Limit: 1},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		fixture.authorizer.callCount() != beforeCalls {
		t.Fatalf("public inbox read = %v; authorizer calls=%d", err, fixture.authorizer.callCount())
	}
}

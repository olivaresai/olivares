// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These resolvers retain the fixture's current directory evidence while binding
// an authenticated SESSION to its canonical recipient and direct grant subject.
type handoffRecognitionDirectory struct {
	directNoticeReadDirectoryResolver
}

func (r *handoffRecognitionDirectory) ResolvePrincipal(ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal) (PrincipalResolution, error) {
	result, err := r.directNoticeReadDirectoryResolver.ResolvePrincipal(ctx, scope, principal)
	if result.Recipient != nil && principal.SessionID != "" {
		result.Recipient.Recipient = RecipientRef{Kind: RecipientSession, Ref: principal.SessionID}
	}
	return result, err
}

type handoffRecognitionClosure struct {
	directNoticeReadClosureResolver
}

func (r *handoffRecognitionClosure) ResolveChannelGrantSubjects(ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal) (ChannelGrantSubjectClosure, error) {
	result, err := r.directNoticeReadClosureResolver.ResolveChannelGrantSubjects(ctx, scope, principal)
	if principal.SessionID != "" {
		result.Subjects = []CommunicationSubjectRef{{Kind: SubjectSession, Ref: principal.SessionID}}
	}
	return result, err
}

type handoffRecognitionFixture struct {
	handoffServiceFixture
	claim        store.AuthorizationFactRef
	sid          string
	directory    *handoffRecognitionDirectory
	grantClosure *handoffRecognitionClosure
}

func newHandoffRecognitionFixture(t *testing.T, spec handoffServiceFixtureSpec) handoffRecognitionFixture {
	t.Helper()
	f := newHandoffServiceFixtureFor(t, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	runID, agentID := model.NewID().String(), "agent:recognition"
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{colRunRef: runID, colTransport: string(TransportStreamJSON),
			colPermissionMode: "default", colIsolation: string(IsolationNative), colState: stateRunning,
			colLastEventSeq: int64(0), colRunAgentRef: agentID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{Provider: ProviderOperated, ExternalID: runID,
		Origin: OriginOperated, WorkspaceID: f.workspace, At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.m.Claim(ctx, f.tenant, sid, "recognition-holder", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := auth.NewSystemOperator("test:handoff-recognition", "issue bounded SESSION fixture credential")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := f.authr.IssueCommunicationSessionCredential(ctx, issuer, auth.CommunicationSessionCredentialSpec{
		Tenant: f.tenant, WorkspaceID: f.workspace, SessionRef: sid,
		RunRef: runID, AgentRef: agentID, ClaimFence: lease.Fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.authr.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	var ok bool
	f.targetRef, ok = principal.Ref()
	if !ok {
		t.Fatal("missing SESSION principal reference")
	}
	var authorization store.AuthorizationFactRef
	err = f.m.viewCommunication(ctx, f.scope, func(sc store.Scope) error {
		epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		f.epoch = epoch.Version
		authorization, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.source.evidence.Facts = []store.AuthorizationFactRef{authorization, {Kind: model.DirectoryEpochKind, ID: model.ID(f.tenant), Version: f.epoch}}
	directory := &handoffRecognitionDirectory{directNoticeReadDirectoryResolver{now: f.now, epoch: f.epoch}}
	closure := &handoffRecognitionClosure{directNoticeReadClosureResolver{now: f.now, epoch: f.epoch}}
	f.m.communicationDirectoryResolver, f.m.communicationGrantClosure = directory, closure
	grant := channelGrantFromRecognitionFixture(t, f, sid)
	record, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = communicationCreateWithID(ctx, f.m, f.tenant, channelGrantKind, grant.ID, record); err != nil {
		t.Fatal(err)
	}

	// Build a new immutable carrier. The earlier USER carrier remains a separate
	// row; no immutable audience or causal arc is edited to change its meaning.
	message := f.message
	message.ID, message.ThreadID, message.Version = model.NewID(), model.NewID(), 1
	message.ThreadID = message.ID
	message.State, message.PublishedAt, message.AudienceHash = MessageDraft, nil, nil
	delivery := f.delivery
	delivery.ID, delivery.MessageID, delivery.Version = model.NewID(), message.ID, 1
	delivery.Recipient = RecipientRef{Kind: RecipientSession, Ref: sid}
	delivery.DeliverySeq = 2
	selector := AudienceSelector{Kind: AudienceSession, Ref: sid, Required: true, WakePolicy: WakeNone}
	selectorJSON, err := canonicalJSON(selector)
	if err != nil {
		t.Fatal(err)
	}
	selectorHash := sha256.Sum256(selectorJSON)
	entity := func(id model.ID) AppendOnlyCommunicationEntity {
		return AppendOnlyCommunicationEntity{CommunicationEntity: CommunicationEntity{ID: id, TenantID: f.tenant, WorkspaceID: f.workspace, Version: 1, CreatedAt: f.now}}
	}
	audience := MessageAudience{AppendOnlyCommunicationEntity: entity(model.NewID()), MessageID: message.ID, Ordinal: 1, Selector: selector,
		ChannelACLRevision: f.channel.ACLRevision, RouteRevision: f.channel.RouteRevision, SubscriptionRevision: f.channel.SubscriptionRevision,
		DirectoryEpoch: f.epoch, DirectorySnapshotAt: f.now, ResolvedCount: 1, SelectorHash: selectorHash[:], ResolvedHash: make([]byte, sha256.Size)}
	contribution := communicationStateTestSealCausalArc(MessageAudienceRecipient{AppendOnlyCommunicationEntity: entity(model.NewID()),
		MessageAudienceID: audience.ID, MessageDeliveryID: delivery.ID, Recipient: delivery.Recipient, RecipientEpoch: delivery.RecipientEpoch,
		ObservedSessionSID: sid, ObservedClaimFence: lease.Fence, Required: true, WakePolicy: WakeNone, RouteReasons: []RouteReason{"direct"}, Selector: selector,
		DirectoryEpoch: f.epoch, ChannelACLRevision: f.channel.ACLRevision, RouteRevision: f.channel.RouteRevision,
		SubscriptionRevision: f.channel.SubscriptionRevision, CausalKind: CausalDirect, CausalRef: sid})
	audience.ResolvedHash, err = canonicalResolvedAudienceHash(audience, []MessageAudienceRecipient{contribution})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := CanonicalMessageAudienceHash(message, []MessageAudience{audience}, []MessageAudienceRecipient{contribution})
	if err != nil {
		t.Fatal(err)
	}
	messageRecord, err := messageToRecord(message, 1)
	if err != nil {
		t.Fatal(err)
	}
	audienceRecord, err := messageAudienceToRecord(audience)
	if err != nil {
		t.Fatal(err)
	}
	deliveryRecord, err := messageDeliveryToRecord(delivery)
	if err != nil {
		t.Fatal(err)
	}
	contributionRecord, err := messageAudienceRecipientToRecord(contribution)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		kind   model.Kind
		id     model.ID
		record model.Record
	}{
		{messageKind, message.ID, messageRecord}, {messageAudienceKind, audience.ID, audienceRecord},
		{messageDeliveryKind, delivery.ID, deliveryRecord}, {messageAudienceRecipientKind, contribution.ID, contributionRecord},
	} {
		if _, err := communicationCreateWithID(ctx, f.m, f.tenant, row.kind, row.id, row.record); err != nil {
			t.Fatal(err)
		}
	}
	published := f.now
	message.State, message.PublishedAt, message.AudienceHash = MessagePublished, &published, hash
	messageRecord, err = messageToRecord(message, 1)
	if err != nil {
		t.Fatal(err)
	}
	messageRecord, err = communicationUpdate(ctx, f.m, f.tenant, messageKind, messageRecord)
	if err != nil {
		t.Fatal(err)
	}
	f.message, err = messageFromRecord(messageRecord, 1)
	if err != nil {
		t.Fatal(err)
	}
	f.delivery = delivery
	claims, err := f.m.communicationClaimAuthoritySnapshot(ctx, f.tenant, []CommunicationClaimRef{{SessionSID: sid, Fence: lease.Fence}})
	if err != nil || len(claims.facts) != 1 {
		t.Fatalf("capture SESSION Claim: %v", err)
	}
	return handoffRecognitionFixture{handoffServiceFixture: f, claim: claims.facts[0], sid: sid, directory: directory, grantClosure: closure}
}

func channelGrantFromRecognitionFixture(t *testing.T, f handoffServiceFixture, sid string) ChannelGrant {
	t.Helper()
	return ChannelGrant{MutableCommunicationEntity: MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
		ID: model.NewID(), TenantID: f.tenant, WorkspaceID: f.workspace, Version: 1, CreatedAt: f.now}, UpdatedAt: f.now},
		ChannelID: f.channel.ID, Subject: CommunicationSubjectRef{Kind: SubjectSession, Ref: sid}, Generation: 1, CanRead: true,
		State: ChannelGrantActive, GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: f.sender.String()}}
}

type handoffRecognitionAttemptKey struct{}
type handoffRecognitionAttempt struct {
	mutations    int
	deadline     time.Time
	facts        [][]store.AuthorizationFactRef
	lockErrors   []error
	keys         []string
	keyAttempts  []int
	receiptReads int
	clocks       int
	before       func(int, context.Context) error
	after        func(int, error) error
	authority    func(int, []store.AuthorizationFactRef, error) error
	clock        func(int, int, model.Timestamp) model.Timestamp
	receipt      func(int, []model.Record, model.Page, error) ([]model.Record, model.Page, error)
	list         func(model.Kind, int, []model.Record, model.Page, error) ([]model.Record, model.Page, error)
}

type handoffRecognitionData struct{ inner api.ModuleData }

func (d *handoffRecognitionData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}
func (d *handoffRecognitionData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	a, _ := ctx.Value(handoffRecognitionAttemptKey{}).(*handoffRecognitionAttempt)
	if a == nil {
		return d.inner.Mutate(ctx, tenant, fn)
	}
	a.mutations++
	n := a.mutations
	deadline, ok := ctx.Deadline()
	if !ok || (!a.deadline.IsZero() && !a.deadline.Equal(deadline)) {
		return errors.New("recognition changed original deadline")
	}
	a.deadline = deadline
	if a.before != nil {
		if err := a.before(n, ctx); err != nil {
			return err
		}
	}
	err := d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		wrapped := &handoffRecognitionScope{Scope: sc, TransactionClock: sc.(store.TransactionClock), TransactionLocker: sc.(store.TransactionLocker),
			AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), AuthoritySnapshotBundleLocker: sc.(store.AuthoritySnapshotBundleLocker),
			DirectorySnapshotReader: sc.(store.DirectorySnapshotReader), AuthorizationEpochReader: sc.(store.AuthorizationEpochReader),
			AuthorizationEpochBumper: sc.(store.AuthorizationEpochBumper), attempt: a, number: n}
		return fn(wrapped)
	})
	if a.after != nil {
		return a.after(n, err)
	}
	return err
}

type handoffRecognitionScope struct {
	store.Scope
	store.TransactionClock
	store.TransactionLocker
	store.AuthoritySnapshotLocker
	store.AuthoritySnapshotBundleLocker
	store.DirectorySnapshotReader
	store.AuthorizationEpochReader
	store.AuthorizationEpochBumper
	attempt *handoffRecognitionAttempt
	number  int
}

func (s *handoffRecognitionScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	now, err := s.TransactionClock.TransactionNow(ctx)
	s.attempt.clocks++
	if err == nil && s.attempt.clock != nil {
		now = s.attempt.clock(s.number, s.attempt.clocks, now)
	}
	return now, err
}
func (s *handoffRecognitionScope) LockTransaction(ctx context.Context, key string) error {
	s.attempt.keys = append(s.attempt.keys, key)
	s.attempt.keyAttempts = append(s.attempt.keyAttempts, s.number)
	return s.TransactionLocker.LockTransaction(ctx, key)
}
func (s *handoffRecognitionScope) recordAuthority(facts []store.AuthorizationFactRef, err error) error {
	s.attempt.facts = append(s.attempt.facts, append([]store.AuthorizationFactRef(nil), facts...))
	s.attempt.lockErrors = append(s.attempt.lockErrors, err)
	if s.attempt.authority != nil {
		return s.attempt.authority(s.number, facts, err)
	}
	return err
}
func (s *handoffRecognitionScope) LockAuthoritySnapshot(ctx context.Context, facts []store.AuthorizationFactRef) error {
	return s.recordAuthority(facts, s.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, facts))
}
func (s *handoffRecognitionScope) LockAuthoritySnapshotBundle(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	return s.recordAuthority(bundle.Facts, s.AuthoritySnapshotBundleLocker.LockAuthoritySnapshotBundle(ctx, bundle))
}
func (s *handoffRecognitionScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || (kind != communicationCommandKind && s.attempt.list == nil) {
		return repo, err
	}
	return &handoffRecognitionReceiptRepo{TransactionStampedGenericRepo: repo.(store.TransactionStampedGenericRepo), RowLocker: repo.(store.RowLocker[model.Record]), DistinctProjector: repo.(store.DistinctProjector), attempt: s.attempt, number: s.number, kind: kind}, nil
}

type handoffRecognitionReceiptRepo struct {
	store.TransactionStampedGenericRepo
	store.RowLocker[model.Record]
	store.DistinctProjector
	attempt *handoffRecognitionAttempt
	number  int
	kind    model.Kind
}

func (r *handoffRecognitionReceiptRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.TransactionStampedGenericRepo.List(ctx, q)
	if r.attempt.list != nil {
		return r.attempt.list(r.kind, r.number, rows, page, err)
	}
	if r.kind == communicationCommandKind {
		r.attempt.receiptReads++
	}
	if r.kind == communicationCommandKind && r.attempt.receipt != nil {
		return r.attempt.receipt(r.number, rows, page, err)
	}
	return rows, page, err
}

// Full ordered row snapshots include payloads and audit anchors, not only row
// counts. The Claim is tenant scoped and intentionally inspected separately.
func handoffRecognitionRows(t *testing.T, f handoffRecognitionFixture) map[model.Kind][]model.Record {
	t.Helper()
	snapshot := map[model.Kind][]model.Record{}
	for _, kind := range []model.Kind{handoffKind, workItemKind, workLeaseKind, workGuardKind, channelKind, channelGrantKind, messageKind,
		messageDeliveryKind, messageAudienceKind, messageAudienceRecipientKind, messageAckKind, communicationCommandKind, workEventKind, workOutboxKind} {
		err := f.m.viewCommunication(context.Background(), f.scope, func(sc store.Scope) error {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, page, err := repo.List(context.Background(), model.Query{Sort: []model.Sort{{Column: model.ColID}}, Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("complete %s snapshot exceeded its bound", kind)
			}
			snapshot[kind] = rows
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	err := f.m.communicationData(f.tenant).View(context.Background(), func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(event model.AuditEvent) error {
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			snapshot["core.audit"] = append(snapshot["core.audit"], model.Record{"event": string(encoded)})
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func handoffRecognitionClaim(t *testing.T, f handoffRecognitionFixture) model.Record {
	t.Helper()
	var record model.Record
	err := f.m.communicationData(f.tenant).View(context.Background(), func(sc store.Scope) error {
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		rows, page, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colClaimSID, f.sid)}, Limit: 2})
		if err != nil {
			return err
		}
		if page.HasMore || len(rows) != 1 {
			return errors.New("Claim row is not unique")
		}
		record = rows[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func handoffRecognitionAssertRows(t *testing.T, before, after map[model.Kind][]model.Record) {
	t.Helper()
	for kind, rows := range before {
		if !reflect.DeepEqual(rows, after[kind]) {
			t.Errorf("recognition changed complete %s rows", kind)
		}
	}
}
func handoffRecognitionAssertTouch(t *testing.T, before, after model.Record, delta int64) {
	t.Helper()
	if after.Int(model.ColVersion) != before.Int(model.ColVersion)+delta {
		t.Fatalf("Claim version %d -> %d, want delta %d", before.Int(model.ColVersion), after.Int(model.ColVersion), delta)
	}
	left, right := maps.Clone(before), maps.Clone(after)
	delete(left, model.ColVersion)
	delete(right, model.ColVersion)
	delete(left, model.ColUpdatedAt)
	delete(right, model.ColUpdatedAt)
	if !reflect.DeepEqual(left, right) {
		t.Fatal("Claim touch changed semantic fields")
	}
	if delta == 0 && !reflect.DeepEqual(before, after) {
		t.Fatal("failed callback changed Claim row")
	}
}

type handoffRecognitionOutcome struct {
	result HandoffResponseResult
	err    error
}

func handoffRecognitionStart(f handoffRecognitionFixture, ctx context.Context, offer HandoffOfferResult, command HandoffResponseCommand, a *handoffRecognitionAttempt) <-chan handoffRecognitionOutcome {
	out := make(chan handoffRecognitionOutcome, 1)
	go func() {
		result, err := f.m.respondHandoffWithAuthority(context.WithValue(ctx, handoffRecognitionAttemptKey{}, a), f.scope, f.targetRef, offer.HandoffID, command)
		out <- handoffRecognitionOutcome{result, err}
	}()
	return out
}
func handoffRecognitionWait(t *testing.T, ctx context.Context, ch <-chan handoffRecognitionOutcome) handoffRecognitionOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-ctx.Done():
		t.Fatal("bounded recognition fixture expired")
		return handoffRecognitionOutcome{}
	}
}
func handoffRecognitionGate() (*handoffRecognitionAttempt, <-chan struct{}, func()) {
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	return &handoffRecognitionAttempt{before: func(n int, ctx context.Context) error {
		if n != 1 {
			return nil
		}
		close(ready)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}, ready, func() { once.Do(func() { close(release) }) }
}
func handoffRecognitionAwait(t *testing.T, ctx context.Context, ready <-chan struct{}, out <-chan handoffRecognitionOutcome) {
	t.Helper()
	select {
	case <-ready:
	case got := <-out:
		t.Fatalf("request ended before capture barrier: %v", got.err)
	case <-ctx.Done():
		t.Fatal("request did not capture authority")
	}
}

func TestHandoffSessionRecognitionRealClaimContention(t *testing.T) {
	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			for _, active := range []bool{false, true} {
				t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
					backend := engine.newBackend(t, "session-recognition")
					f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{backend: &backend, vacantLease: !active, noClockGuard: !active})
					offer := vacantTransferOffer(t, f.handoffServiceFixture, "recognition")
					f.m.data = &handoffRecognitionData{inner: f.m.data}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
					defer cancel()
					command := HandoffResponseCommand{Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: model.NewID().String()}
					winner, readyWinner, releaseWinner := handoffRecognitionGate()
					defer releaseWinner()
					winnerOut := handoffRecognitionStart(f, ctx, offer, command, winner)
					handoffRecognitionAwait(t, ctx, readyWinner, winnerOut)
					loser, readyLoser, releaseLoser := handoffRecognitionGate()
					defer releaseLoser()
					loserOut := handoffRecognitionStart(f, ctx, offer, command, loser)
					handoffRecognitionAwait(t, ctx, readyLoser, loserOut)
					before := handoffRecognitionClaim(t, f)
					releaseWinner()
					won := handoffRecognitionWait(t, ctx, winnerOut)
					if won.err != nil || won.result.Replayed {
						t.Fatalf("winner: %+v, %v", won.result, won.err)
					}
					afterWinner := handoffRecognitionClaim(t, f)
					handoffRecognitionAssertTouch(t, before, afterWinner, 1)
					rows := handoffRecognitionRows(t, f)
					releaseLoser()
					got := handoffRecognitionWait(t, ctx, loserOut)
					if got.err != nil || !got.result.Replayed {
						t.Fatalf("recognition: %+v, %v", got.result, got.err)
					}
					normalized := got.result
					normalized.Replayed = false
					if normalized != won.result {
						t.Fatalf("receipt differs: %+v vs %+v", normalized, won.result)
					}
					if winner.mutations != 1 || loser.mutations != 2 || len(loser.facts) != 2 || len(loser.lockErrors) != 2 {
						t.Fatalf("attempts winner=%d loser=%d authority=%d", winner.mutations, loser.mutations, len(loser.facts))
					}
					var conflict *store.LockedLeasedVersionConflict
					if !errors.As(loser.lockErrors[0], &conflict) || !conflict.Matches(f.claim) || loser.lockErrors[1] != nil {
						t.Fatalf("real stale Claim witness absent: %v", loser.lockErrors)
					}
					for _, a := range []*handoffRecognitionAttempt{winner, loser} {
						found := false
						for _, fact := range a.facts[0] {
							if fact.Kind == claimKind {
								found = true
								if fact.Version != before.Int(model.ColVersion) {
									t.Fatal("requests did not capture same Claim version")
								}
							}
						}
						if !found {
							t.Fatal("Claim absent from actual locked union")
						}
					}
					for _, attempt := range loser.keyAttempts {
						if attempt == 1 {
							t.Fatal("first stale attempt reached a local transaction lock")
						}
					}
					if loser.receiptReads != 1 {
						t.Fatalf("receipt reads=%d, want only recognition", loser.receiptReads)
					}
					handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f))
					handoffRecognitionAssertTouch(t, afterWinner, handoffRecognitionClaim(t, f), 1)
					lease := handoffStoredRecord(t, f.handoffServiceFixture, workLeaseKind, f.leaseID)
					if active {
						if got.result.ResultingLeaseFence != 8 || lease.Int(colLeaseFence) != 8 || lease.String(colLeaseState) != workLeaseRevoked {
							t.Fatal("active generation was not released exactly once")
						}
					} else {
						if got.result.ResultingLeaseFence != 0 || lease.Int(colLeaseFence) != 0 || lease.Int(model.ColVersion) != 1 || len(rows[workGuardKind]) != 0 {
							t.Fatal("recognition created vacant lease authority")
						}
						raw, err := json.Marshal(got.result)
						if err != nil {
							t.Fatal(err)
						}
						var fields map[string]json.RawMessage
						if err = json.Unmarshal(raw, &fields); err != nil {
							t.Fatal(err)
						}
						if _, ok := fields["resulting_lease_fence"]; ok {
							t.Fatal("vacant wire fence was not omitted")
						}
					}
				})
			}
		})
	}
}

func TestHandoffRecognitionEligibilityRequiresExactCause(t *testing.T) {
	t.Parallel()
	deadline := model.NewTimestamp(time.Now().Add(time.Hour))
	fact, err := store.NewLeaseFenceAuthorizationFactRef(claimKind, model.NewID(), 1, "osn_"+model.NewID().String(), 7, deadline)
	if err != nil {
		t.Fatal(err)
	}
	sid, fence, _, _ := fact.LeaseFenceWitness()
	normalized := handoffResponseNormalized{handoffCommandIdentity: handoffCommandIdentity{principal: CommunicationPrincipal{SessionID: sid, SessionFence: fence}}, command: HandoffResponseCommand{Transition: HandoffAccept}}
	claims := CommunicationClaimAuthoritySnapshot{facts: []store.AuthorizationFactRef{fact}}
	typed := store.NewLockedLeasedVersionConflict(fact)
	other := fact
	other.Version++
	for _, tc := range []struct {
		name   string
		cause  error
		change func(*handoffResponseNormalized)
		want   bool
	}{
		{"direct", typed, nil, true}, {"real two cause wrapper", fmt.Errorf("%w: Claim authority snapshot: %w", ErrCommunicationEvidenceUnknown, typed), nil, true},
		{"single wrapper", fmt.Errorf("adapter: %w", typed), nil, true},
		{"joined cancellation", errors.Join(typed, context.Canceled), nil, false},
		{"joined unavailable", errors.Join(typed, errors.New("store unavailable")), nil, false},
		{"duplicate subtype", errors.Join(typed, typed), nil, false},
		{"wrong witness", store.NewLockedLeasedVersionConflict(other), nil, false},
		{"generic conflict", store.ErrConflict, nil, false}, {"missing", store.ErrNotFound, nil, false}, {"unknown", ErrCommunicationEvidenceUnknown, nil, false},
		{"USER", typed, func(n *handoffResponseNormalized) { n.principal.SessionID = ""; n.principal.UserID = model.NewID() }, false},
		{"SESSION reject", typed, func(n *handoffResponseNormalized) { n.command.Transition = HandoffReject }, false},
		{"other responder", typed, func(n *handoffResponseNormalized) { n.principal.SessionID = "osn_" + model.NewID().String() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := normalized
			if tc.change != nil {
				tc.change(&n)
			}
			got := captureHandoffResponseAuthorityFailure(tc.cause, n, claims)
			_, eligible := handoffResponseRecognitionFailure(got)
			if eligible != tc.want {
				t.Fatalf("eligibility=%t, want %t (%v)", eligible, tc.want, got)
			}
			if tc.want && (!errors.Is(got, ErrCommunicationEvidenceUnknown) || errors.Is(got, store.ErrConflict)) {
				t.Fatal("private marker changed wire classification")
			}
		})
	}
	marker := captureHandoffResponseAuthorityFailure(typed, normalized, claims)
	for _, mixed := range []error{errors.Join(marker, context.Canceled), errors.Join(marker, errors.New("adapter failure")), errors.Join(marker, marker)} {
		if _, ok := handoffResponseRecognitionFailure(mixed); ok {
			t.Fatal("ModuleData mixed outcome became eligible")
		}
	}
	if _, ok := handoffResponseRecognitionFailure(fmt.Errorf("adapter: %w", marker)); !ok {
		t.Fatal("ordinary ModuleData wrapping lost eligibility")
	}
	other.Kind = "other.leased_fact"
	if _, ok := handoffResponseRecognitionFailure(captureHandoffResponseAuthorityFailure(store.NewLockedLeasedVersionConflict(other), normalized, claims)); ok {
		t.Fatal("other fact with Claim present became eligible")
	}
	changed := fact
	changed.Version++
	if !sameHandoffClaimSemantics(fact, changed) {
		t.Fatal("version-only change refused")
	}
	for _, tc := range []struct {
		sid      string
		fence    int64
		deadline model.Timestamp
	}{{sid + "x", fence, deadline}, {sid, fence + 1, deadline}, {sid, fence, model.NewTimestamp(deadline.Time().Add(time.Minute))}} {
		other, err := store.NewLeaseFenceAuthorizationFactRef(fact.Kind, fact.ID, 2, tc.sid, tc.fence, tc.deadline)
		if err != nil {
			t.Fatal(err)
		}
		if sameHandoffClaimSemantics(fact, other) {
			t.Fatal("changed Claim semantics accepted")
		}
	}
	changed.ID = model.NewID()
	if sameHandoffClaimSemantics(fact, changed) {
		t.Fatal("recreated Claim accepted")
	}
}

func handoffRecognitionTouch(t *testing.T, f handoffRecognitionFixture) {
	t.Helper()
	ctx := context.Background()
	err := f.m.communicationData(f.tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, f.claim.ID)
		if err != nil {
			return err
		}
		deadline, err := model.ParseTimestamp(row.String(colLeaseExpires))
		if err != nil {
			return err
		}
		ref, err := store.NewLeaseFenceAuthorizationFactRef(claimKind, f.claim.ID, row.Int(model.ColVersion), row.String(colClaimSID), row.Int(colFence), deadline)
		if err != nil {
			return err
		}
		return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{ref})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A controlled, independent authorized touch makes the captured Claim stale.
// The primary contention test separately proves that a real winner's accept
// causes this touch. Each negative starts from its own quiescent row snapshot.
func handoffRecognitionChallenge(t *testing.T, f handoffRecognitionFixture, offer HandoffOfferResult, command HandoffResponseCommand,
	configure func(*handoffRecognitionAttempt), afterCapture func(), want error, wantAttempts int, wantTouch int64) handoffRecognitionOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a, ready, release := handoffRecognitionGate()
	defer release()
	if configure != nil {
		configure(a)
	}
	out := handoffRecognitionStart(f, ctx, offer, command, a)
	handoffRecognitionAwait(t, ctx, ready, out)
	handoffRecognitionTouch(t, f)
	if afterCapture != nil {
		afterCapture()
	}
	beforeRows, beforeClaim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
	release()
	got := handoffRecognitionWait(t, ctx, out)
	if want == nil {
		if got.err != nil || !got.result.Replayed {
			t.Fatalf("recognition=%+v, %v", got.result, got.err)
		}
	} else if !errors.Is(got.err, want) || got.result != (HandoffResponseResult{}) {
		t.Fatalf("recognition=%+v, %v; want non-success %v", got.result, got.err, want)
	}
	if a.mutations != wantAttempts {
		t.Fatalf("mutations=%d, want %d", a.mutations, wantAttempts)
	}
	handoffRecognitionAssertRows(t, beforeRows, handoffRecognitionRows(t, f))
	handoffRecognitionAssertTouch(t, beforeClaim, handoffRecognitionClaim(t, f), wantTouch)
	return got
}

func handoffRecognitionAccepted(t *testing.T) (handoffRecognitionFixture, HandoffOfferResult, HandoffResponseCommand, HandoffResponseResult) {
	t.Helper()
	f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{vacantLease: true, noClockGuard: true})
	offer := vacantTransferOffer(t, f.handoffServiceFixture, "recorded recognition")
	command := HandoffResponseCommand{Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: model.NewID().String()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	won, err := f.m.respondHandoffWithAuthority(ctx, f.scope, f.targetRef, offer.HandoffID, command)
	if err != nil {
		t.Fatal(err)
	}
	f.m.data = &handoffRecognitionData{inner: f.m.data}
	return f, offer, command, won
}

func TestHandoffRecognitionReceiptRefusalsAndPlainControls(t *testing.T) {
	f, offer, command, won := handoffRecognitionAccepted(t)
	type receiptChange func([]model.Record, model.Page, error) ([]model.Record, model.Page, error)
	changes := map[string]receiptChange{
		"absent": func(_ []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) { return nil, p, e },
		"duplicate": func(r []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			return append(r, r[0]), p, e
		},
		"truncated": func(r []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			p.HasMore = true
			return r, p, e
		},
		"unavailable": func(r []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			return nil, p, ErrCommunicationEvidenceUnknown
		},
	}
	for name, change := range map[string]func(model.Record){
		"malformed":       func(r model.Record) { r[colCommResponseProjectionJSON] = "{" },
		"tenant":          func(r model.Record) { r[model.ColTenantID] = model.NewID().String() },
		"workspace":       func(r model.Record) { r[colWorkWorkspaceID] = model.NewID().String() },
		"actor":           func(r model.Record) { r[colCommActorFingerprint] = make([]byte, sha256.Size) },
		"scope":           func(r model.Record) { r[colCommCommandScope] = "other.scope" },
		"key":             func(r model.Record) { r[colCommIdempotencyKeyHash] = make([]byte, sha256.Size) },
		"response digest": func(r model.Record) { r[colCommResponseDigest] = make([]byte, sha256.Size) },
		"paired keyed": func(r model.Record) {
			r[colCommSealKeyVersion] = "seal-v1"
			r[colCommDigestKeyVersion] = "digest-v1"
			r[colCommResponseDigest] = make([]byte, sha256.Size)
		},
		"seal only":   func(r model.Record) { r[colCommSealKeyVersion] = "seal-v1" },
		"digest only": func(r model.Record) { r[colCommDigestKeyVersion] = "digest-v1" },
	} {
		changes[name] = func(rows []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			if e != nil {
				return rows, p, e
			}
			copy := maps.Clone(rows[0])
			change(copy)
			return []model.Record{copy}, p, nil
		}
	}
	for _, field := range []string{"handoff_id", "event_id", "message_id", "delivery_id", "work_item_id", "ack_id", "version", "state"} {
		changes["projection "+field] = func(rows []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			if e != nil {
				return rows, p, e
			}
			receipt, err := communicationCommandReceiptFromRecord(rows[0])
			if err != nil {
				return nil, p, err
			}
			switch field {
			case "version":
				receipt.ResponseProjectionJSON.Version++
			case "state":
				receipt.ResponseProjectionJSON.State = string(HandoffRejected)
			default:
				receipt.ResponseProjectionJSON.IDs[field] = model.NewID()
			}
			binding, err := CanonicalCommunicationReceiptResponseBinding(receipt)
			if err != nil {
				return nil, p, err
			}
			digest := sha256.Sum256(binding)
			receipt.ResponseDigest = digest[:]
			row, err := communicationCommandReceiptToRecord(receipt)
			if err != nil {
				return nil, p, err
			}
			return []model.Record{row}, p, nil
		}
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
				a.receipt = func(n int, r []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
					if n != 2 {
						return r, p, e
					}
					return change(r, p, e)
				}
			}, nil, ErrCommunicationEvidenceUnknown, 2, 0)
		})
	}
	t.Run("same key changed If-Match", func(t *testing.T) {
		changed := command
		changed.IfMatch = "\"v2\""
		handoffRecognitionChallenge(t, f, offer, changed, nil, nil, store.ErrConflict, 2, 0)
	})
	t.Run("different key recognition", func(t *testing.T) {
		changed := command
		changed.IdempotencyKey = model.NewID().String()
		handoffRecognitionChallenge(t, f, offer, changed, nil, nil, ErrCommunicationEvidenceUnknown, 2, 0)
	})
	t.Run("normal later new key", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a := &handoffRecognitionAttempt{}
		changed := command
		changed.IdempotencyKey = model.NewID().String()
		before := handoffRecognitionRows(t, f)
		claim := handoffRecognitionClaim(t, f)
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, changed, a))
		if !errors.Is(got.err, store.ErrConflict) || a.mutations != 1 {
			t.Fatalf("normal new key = %v, attempts %d", got.err, a.mutations)
		}
		handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
	})
	t.Run("ordinary paired keyed refusal", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a := &handoffRecognitionAttempt{receipt: func(_ int, r []model.Record, p model.Page, e error) ([]model.Record, model.Page, error) {
			return changes["paired keyed"](r, p, e)
		}}
		before := handoffRecognitionRows(t, f)
		claim := handoffRecognitionClaim(t, f)
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, command, a))
		if !errors.Is(got.err, ErrCommunicationEvidenceUnknown) || a.mutations != 1 {
			t.Fatalf("ordinary keyed replay = %v, attempts %d", got.err, a.mutations)
		}
		handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
	})
	t.Run("plain recognition", func(t *testing.T) {
		got := handoffRecognitionChallenge(t, f, offer, command, nil, nil, nil, 2, 1)
		got.result.Replayed = false
		if got.result != won {
			t.Fatal("plain receipt changed historical result")
		}
	})
	t.Run("plain ordinary replay", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a := &handoffRecognitionAttempt{}
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, command, a))
		if got.err != nil || !got.result.Replayed || a.mutations != 1 {
			t.Fatalf("ordinary plain replay: %+v, %v", got.result, got.err)
		}
	})
}

func TestHandoffRecognitionAbsentReceiptCannotAcceptOfferedHandoff(t *testing.T) {
	f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{vacantLease: true, noClockGuard: true})
	offer := vacantTransferOffer(t, f.handoffServiceFixture, "still offered")
	f.m.data = &handoffRecognitionData{inner: f.m.data}
	command := HandoffResponseCommand{Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: model.NewID().String()}
	handoffRecognitionChallenge(t, f, offer, command, nil, nil, ErrCommunicationEvidenceUnknown, 2, 0)
	row := handoffStoredRecord(t, f.handoffServiceFixture, handoffKind, offer.HandoffID)
	if row.String(colCommState) != string(HandoffOffered) || row.Int(model.ColVersion) != 1 {
		t.Fatal("absent receipt performed a fresh accept")
	}
}

func TestHandoffRecognitionTransactionBoundaries(t *testing.T) {
	f, offer, command, _ := handoffRecognitionAccepted(t)
	t.Run("second real collision ends unknown", func(t *testing.T) {
		handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
			first := a.before
			a.before = func(n int, ctx context.Context) error {
				if n == 2 {
					handoffRecognitionTouch(t, f)
				}
				return first(n, ctx)
			}
		}, nil, ErrCommunicationEvidenceUnknown, 2, 1)
	})
	for _, phase := range []int{2, 3} {
		t.Run(fmt.Sprintf("recognition expiry clock observation %d", phase), func(t *testing.T) {
			observations := 0
			handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
				a.clock = func(n, _ int, now model.Timestamp) model.Timestamp {
					if n == 2 {
						observations++
						if observations == phase {
							return model.NewTimestamp(now.Time().Add(2 * time.Hour))
						}
					}
					return now
				}
			}, nil, ErrCommunicationEvidenceUnknown, 2, 0)
			if observations != phase {
				t.Fatalf("expired at observation %d, want %d", observations, phase)
			}
		})
	}
	t.Run("simulated result loss after real recognition commit", func(t *testing.T) {
		lost := errors.New("simulated committed result loss")
		handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
			a.after = func(n int, err error) error {
				if n == 2 && err == nil {
					return lost
				}
				return err
			}
		}, nil, lost, 2, 1)
	})
	t.Run("unrelated ModuleData error after failed callback", func(t *testing.T) {
		handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
			a.after = func(n int, err error) error {
				if n == 1 {
					return errors.Join(err, errors.New("unrelated adapter failure"))
				}
				return err
			}
		}, nil, ErrCommunicationEvidenceUnknown, 1, 0)
	})
	for name, cause := range map[string]error{"generic conflict": store.ErrConflict, "missing row": store.ErrNotFound, "SQL unavailable": store.ErrStoreUnavailable} {
		t.Run(name, func(t *testing.T) {
			handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
				a.authority = func(n int, _ []store.AuthorizationFactRef, err error) error {
					if n == 1 {
						return cause
					}
					return err
				}
			}, nil, ErrCommunicationEvidenceUnknown, 1, 0)
		})
	}
	t.Run("unrelated authority error joined to actual stale Claim", func(t *testing.T) {
		handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
			a.authority = func(n int, _ []store.AuthorizationFactRef, err error) error {
				if n == 1 {
					return errors.Join(err, store.ErrStoreUnavailable)
				}
				return err
			}
		}, nil, ErrCommunicationEvidenceUnknown, 1, 0)
	})
	t.Run("ordinary commit result loss has no recognition", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		lost := errors.New("simulated ordinary result loss")
		a := &handoffRecognitionAttempt{after: func(_ int, err error) error {
			if err == nil {
				return lost
			}
			return err
		}}
		rows, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, command, a))
		if !errors.Is(got.err, lost) || got.result != (HandoffResponseResult{}) || a.mutations != 1 {
			t.Fatalf("ordinary lost result: %+v, %v, attempts %d", got.result, got.err, a.mutations)
		}
		handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 1)
	})
}

func TestHandoffRecognitionRefusesChangedCurrentAuthority(t *testing.T) {
	for _, name := range []string{"released", "expired", "fence rotated", "heartbeat deadline", "recreated", "policy denied", "policy unknown", "directory unknown", "recipient denied", "grant revoked"} {
		t.Run(name, func(t *testing.T) {
			f, offer, command, _ := handoffRecognitionAccepted(t)
			want := ErrCommunicationEvidenceUnknown
			if name == "policy denied" || name == "grant revoked" {
				want = ErrCommunicationForbidden
			}
			if name == "recipient denied" {
				want = ErrCommunicationNotFound
			}
			wantAttempts := 1
			if name == "grant revoked" {
				wantAttempts = 2
			}
			handoffRecognitionChallenge(t, f, offer, command, nil, func() {
				switch name {
				case "policy denied":
					f.source.evidence.Outcome = auth.EvidenceDeny
					f.source.evidence.CorePermission.Verdict = auth.CheckBroken
				case "policy unknown":
					f.source.evidence.Outcome = auth.EvidenceUnknown
					f.source.evidence.CorePermission.Verdict = auth.CheckUnknown
				case "directory unknown":
					f.directory.outcome = PrincipalUnknown
				case "recipient denied":
					f.directory.outcome = PrincipalNotFound
				case "grant revoked":
					rows := communicationRowsForTest(t, f.directNoticeFixture, channelGrantKind)
					for _, row := range rows {
						if row.String(colCommSubjectRef) == f.sid {
							row[colCommState] = string(ChannelGrantRevoked)
							row[colCommRevokedByKind] = string(ActorUser)
							row[colCommRevokedByRef] = f.sender.String()
							if _, err := communicationUpdate(context.Background(), f.m, f.tenant, channelGrantKind, row); err != nil {
								t.Fatal(err)
							}
						}
					}
				default:
					err := f.m.communicationData(f.tenant).Mutate(context.Background(), func(sc store.Scope) error {
						repo, err := sc.Ext(claimKind)
						if err != nil {
							return err
						}
						row, err := repo.Get(context.Background(), f.claim.ID)
						if err != nil {
							return err
						}
						switch name {
						case "released":
							row[colClaimState] = claimReleased
						case "expired":
							row[colLeaseExpires] = model.NewTimestamp(time.Now().Add(-time.Minute)).String()
						case "fence rotated":
							row[colFence] = row.Int(colFence) + 1
						case "heartbeat deadline":
							deadline, err := model.ParseTimestamp(row.String(colLeaseExpires))
							if err != nil {
								return err
							}
							row[colLeaseExpires] = model.NewTimestamp(deadline.Time().Add(time.Minute)).String()
						case "recreated":
							if err := repo.Delete(context.Background(), f.claim.ID); err != nil {
								return err
							}
							delete(row, model.ColID)
							created, err := repo.Create(context.Background(), row)
							if err == nil {
								f.claim.ID = recordID(created)
							}
							return err
						}
						_, err = repo.Update(context.Background(), row)
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			}, want, wantAttempts, 0)
		})
	}
}

func TestHandoffRecognitionContextAndJoinedScope(t *testing.T) {
	f, offer, command, _ := handoffRecognitionAccepted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, _, _, _, normalized, _, err := f.m.prepareHandoffResponseAuthority(ctx, f.scope, f.targetRef, offer.HandoffID, command)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unbounded", "canceled", "expired"} {
		t.Run(name, func(t *testing.T) {
			request := context.Background()
			cleanup := func() {}
			switch name {
			case "canceled":
				var stop context.CancelFunc
				request, stop = context.WithCancel(ctx)
				stop()
			case "expired":
				request, cleanup = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			defer cleanup()
			got, err := f.m.recognizeHandoffResponse(request, f.targetRef, normalized, f.claim, false)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || got != (HandoffResponseResult{}) {
				t.Fatalf("invalid original context: %+v %v", got, err)
			}
		})
	}
	t.Run("joined protocol scope", func(t *testing.T) {
		before, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
		err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
			joined, request := newProtocolReplayTransactionContext(ctx, f.tenant, sc)
			defer joined.active.Store(false)
			a := &handoffRecognitionAttempt{}
			request = context.WithValue(request, handoffRecognitionAttemptKey{}, a)
			got, err := f.m.recognizeHandoffResponse(request, f.targetRef, normalized, f.claim, false)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || got != (HandoffResponseResult{}) || a.mutations != 0 {
				return errors.New("joined Scope started detached recognition")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
	})
	for _, name := range []string{"cancel before recognition", "cancel during recognition", "deadline during recognition"} {
		t.Run(name, func(t *testing.T) {
			request, stop := context.WithCancel(ctx)
			defer stop()
			if name == "deadline during recognition" {
				now := time.Now().UTC()
				oldEvidence := f.source.evidence
				oldDirectoryNow, oldDirectoryFresh, oldClosureNow, oldClosureFresh := f.directory.now, f.directory.freshFor, f.grantClosure.now, f.grantClosure.freshFor
				// Keep each fixture evidence window within this deliberately short
				// real request deadline; the timeout itself is never extended.
				f.source.evidence.ObservedAt = f.now
				f.source.evidence.FreshUntil = now.Add(time.Second)
				f.directory.now, f.directory.freshFor = f.now, now.Sub(f.now)+time.Second
				f.grantClosure.now, f.grantClosure.freshFor = f.now, now.Sub(f.now)+time.Second
				defer func() {
					f.source.evidence = oldEvidence
					f.directory.now, f.directory.freshFor = oldDirectoryNow, oldDirectoryFresh
					f.grantClosure.now, f.grantClosure.freshFor = oldClosureNow, oldClosureFresh
				}()
				request, stop = context.WithTimeout(ctx, 2*time.Second)
				defer stop()
			}
			a, ready, release := handoffRecognitionGate()
			defer release()
			originalBefore := a.before
			if name == "cancel before recognition" {
				a.after = func(n int, err error) error {
					if n == 1 {
						stop()
					}
					return err
				}
			} else {
				a.before = func(n int, c context.Context) error {
					if n == 2 {
						if name == "cancel during recognition" {
							stop()
						} else {
							<-c.Done()
						}
					}
					return originalBefore(n, c)
				}
			}
			out := handoffRecognitionStart(f, request, offer, command, a)
			handoffRecognitionAwait(t, ctx, ready, out)
			handoffRecognitionTouch(t, f)
			before, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
			release()
			got := handoffRecognitionWait(t, ctx, out)
			wantAttempts := 2
			if name == "cancel before recognition" {
				wantAttempts = 1
			}
			if got.err == nil || got.result != (HandoffResponseResult{}) || a.mutations != wantAttempts {
				t.Fatalf("context termination: %+v %v attempts=%d", got.result, got.err, a.mutations)
			}
			if request.Err() == nil {
				t.Fatal("original context did not terminate")
			}
			if !errors.Is(got.err, request.Err()) && !errors.Is(got.err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("context error changed classification: %v", got.err)
			}
			handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
			handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
		})
	}
}

func TestHandoffRecognitionClaimExpiryAtRealStoreLock(t *testing.T) {
	f, offer, command, _ := handoffRecognitionAccepted(t)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(2 * time.Second)
	err := f.m.communicationData(f.tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, f.claim.ID)
		if err != nil {
			return err
		}
		row[colLeaseExpires] = model.NewTimestamp(deadline).String()
		_, err = repo.Update(ctx, row)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
		first := a.before
		a.before = func(n int, c context.Context) error {
			if n == 2 {
				timer := time.NewTimer(time.Until(deadline))
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-c.Done():
					return c.Err()
				}
			}
			return first(n, c)
		}
	}, nil, ErrCommunicationEvidenceUnknown, 2, 0)
}

func TestHandoffRecognitionCurrentCarrierAndUnrelatedResponses(t *testing.T) {
	f, offer, command, _ := handoffRecognitionAccepted(t)
	t.Run("current audience seal changed", func(t *testing.T) {
		handoffRecognitionChallenge(t, f, offer, command, func(a *handoffRecognitionAttempt) {
			a.list = func(kind model.Kind, n int, rows []model.Record, p model.Page, err error) ([]model.Record, model.Page, error) {
				if n == 2 && kind == messageAudienceKind && len(rows) > 0 {
					rows[0] = maps.Clone(rows[0])
					rows[0][colCommSelectorHash] = make([]byte, sha256.Size)
				}
				return rows, p, err
			}
		}, nil, ErrCommunicationEvidenceUnknown, 2, 0)
	})
	t.Run("SESSION reject never recognizes", func(t *testing.T) {
		reject := command
		reject.Transition = HandoffReject
		reject.Reason = &CommunicationReasonContent{Code: "declined", Text: "Cannot accept this work"}
		handoffRecognitionChallenge(t, f, offer, reject, nil, nil, ErrCommunicationEvidenceUnknown, 1, 0)
	})
	t.Run("ordinary same-key changed body conflicts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a := &handoffRecognitionAttempt{}
		reject := command
		reject.Transition = HandoffReject
		reject.Reason = &CommunicationReasonContent{Code: "declined", Text: "Cannot accept this work"}
		rows, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, reject, a))
		if !errors.Is(got.err, store.ErrConflict) || a.mutations != 1 {
			t.Fatalf("changed body: %v attempts=%d", got.err, a.mutations)
		}
		handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
	})
	t.Run("late lock conflict is not an authority phase failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a := &handoffRecognitionAttempt{receipt: func(_ int, r []model.Record, p model.Page, _ error) ([]model.Record, model.Page, error) {
			return r, p, store.NewLockedLeasedVersionConflict(f.claim)
		}}
		rows, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
		got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, command, a))
		if got.err == nil || a.mutations != 1 {
			t.Fatalf("late failure retried: %v attempts=%d", got.err, a.mutations)
		}
		handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f))
		handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
	})
}

func TestHandoffRecognitionClaimChangesAfterFreshCapture(t *testing.T) {
	for _, name := range []string{"release", "fence rotation", "heartbeat deadline"} {
		t.Run(name, func(t *testing.T) {
			f, offer, command, _ := handoffRecognitionAccepted(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			a, ready, release := handoffRecognitionGate()
			defer release()
			first := a.before
			var changedClaim model.Record
			a.before = func(n int, c context.Context) error {
				if n != 2 {
					return first(n, c)
				}
				return f.st.Mutate(c, f.tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(claimKind)
					if err != nil {
						return err
					}
					row, err := repo.Get(c, f.claim.ID)
					if err != nil {
						return err
					}
					switch name {
					case "release":
						row[colClaimState] = claimReleased
					case "fence rotation":
						row[colFence] = row.Int(colFence) + 1
					case "heartbeat deadline":
						deadline, err := model.ParseTimestamp(row.String(colLeaseExpires))
						if err != nil {
							return err
						}
						row[colLeaseExpires] = model.NewTimestamp(deadline.Time().Add(time.Minute)).String()
					}
					changedClaim, err = repo.Update(c, row)
					return err
				})
			}
			out := handoffRecognitionStart(f, ctx, offer, command, a)
			handoffRecognitionAwait(t, ctx, ready, out)
			handoffRecognitionTouch(t, f)
			rows := handoffRecognitionRows(t, f)
			release()
			got := handoffRecognitionWait(t, ctx, out)
			if !errors.Is(got.err, ErrCommunicationEvidenceUnknown) || got.result != (HandoffResponseResult{}) || a.mutations != 2 || len(a.lockErrors) != 2 {
				t.Fatalf("post-capture change: %v attempts=%d locks=%d", got.err, a.mutations, len(a.lockErrors))
			}
			var second *store.LockedLeasedVersionConflict
			if !errors.As(a.lockErrors[1], &second) {
				t.Fatalf("second actual lock did not see changed Claim: %v", a.lockErrors[1])
			}
			handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f))
			if !reflect.DeepEqual(changedClaim, handoffRecognitionClaim(t, f)) {
				t.Fatal("second failed lock changed the competing writer's Claim")
			}
		})
	}
}

func TestHandoffRecognitionDoesNotRetryInvalidActiveLeaseEvidence(t *testing.T) {
	for _, name := range []string{"future guard clock", "expired active generation"} {
		t.Run(name, func(t *testing.T) {
			f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{durableAckDelay: 30 * time.Minute})
			offer := vacantTransferOffer(t, f.handoffServiceFixture, "refused active generation")
			want := store.ErrConflict
			a := &handoffRecognitionAttempt{}
			if name == "future guard clock" {
				rows := communicationRowsForTest(t, f.directNoticeFixture, workGuardKind)
				if len(rows) != 1 {
					t.Fatal("guard fixture missing")
				}
				row := rows[0]
				row[colGuardLastDBTime] = model.NewTimestamp(time.Now().Add(time.Hour)).String()
				row[colGuardEpoch] = row.Int(colGuardEpoch) + 1
				if _, err := communicationUpdate(context.Background(), f.m, f.tenant, workGuardKind, row); err != nil {
					t.Fatal(err)
				}
				want = ErrCommunicationEvidenceUnknown
			} else {
				// Advance the transaction-clock witness beyond the existing lease while
				// keeping the independent request, Claim and carrier windows open. This is
				// a simulated clock observation; the durable lease row is never corrupted.
				f.source.evidence.FreshUntil = f.now.Add(30 * time.Minute)
				f.directory.freshFor = 30 * time.Minute
				f.grantClosure.freshFor = 30 * time.Minute
				a.clock = func(_ int, observation int, now model.Timestamp) model.Timestamp {
					if observation >= 2 {
						return model.NewTimestamp(f.now.Add(11 * time.Minute))
					}
					return now
				}
			}
			f.m.data = &handoffRecognitionData{inner: f.m.data}
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			command := HandoffResponseCommand{Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: model.NewID().String()}
			before, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
			got := handoffRecognitionWait(t, ctx, handoffRecognitionStart(f, ctx, offer, command, a))
			if !errors.Is(got.err, want) || got.result != (HandoffResponseResult{}) || a.mutations != 1 {
				t.Fatalf("invalid active evidence: %v attempts=%d", got.err, a.mutations)
			}
			handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
			handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
		})
	}
}

// The protocol owns this transaction. A preceding authorized touch in that
// same Scope makes the response's supplied version stale, and the real Store
// comparison supplies the failure. The entire transaction must then roll back.
type handoffRecognitionJoinedScope struct {
	*handoffRecognitionScope
	calls int
}

func (s *handoffRecognitionJoinedScope) LockAuthoritySnapshot(ctx context.Context, facts []store.AuthorizationFactRef) error {
	s.calls++
	if s.calls != 1 {
		return errors.New("joined response attempted authority twice")
	}
	if err := s.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, facts); err != nil {
		return err
	}
	return s.recordAuthority(facts, s.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, facts))
}

func TestHandoffRecognitionJoinedResponsePreservesProtocolRollbackPostgres(t *testing.T) {
	var backend *communicationSchemaBackend
	for _, engine := range vacantTransferEngines(t) {
		if engine.name == "postgres-split-owner" {
			value := engine.newBackend(t, "joined-recognition")
			backend = &value
		}
	}
	if backend == nil {
		t.Skip("full joined admission requires PostgreSQL; SQLite single-connection exclusion is tested before new admission")
	}
	f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{backend: backend, vacantLease: true, noClockGuard: true})
	offer := vacantTransferOffer(t, f.handoffServiceFixture, "joined response rollback")
	command := HandoffResponseCommand{Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: model.NewID().String()}
	f.m.data = &handoffRecognitionData{inner: f.m.data}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	before, claim := handoffRecognitionRows(t, f), handoffRecognitionClaim(t, f)
	a := &handoffRecognitionAttempt{}
	var calls int
	err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		base := &handoffRecognitionScope{Scope: sc, TransactionClock: sc.(store.TransactionClock), TransactionLocker: sc.(store.TransactionLocker),
			AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), AuthoritySnapshotBundleLocker: sc.(store.AuthoritySnapshotBundleLocker),
			DirectorySnapshotReader: sc.(store.DirectorySnapshotReader), AuthorizationEpochReader: sc.(store.AuthorizationEpochReader),
			AuthorizationEpochBumper: sc.(store.AuthorizationEpochBumper), attempt: a, number: 1}
		wrapped := &handoffRecognitionJoinedScope{handoffRecognitionScope: base}
		joined, request := newProtocolReplayTransactionContext(ctx, f.tenant, wrapped)
		defer joined.active.Store(false)
		request = context.WithValue(request, handoffRecognitionAttemptKey{}, a)
		got, err := f.m.respondHandoffWithAuthority(request, f.scope, f.targetRef, offer.HandoffID, command)
		calls = wrapped.calls
		if got != (HandoffResponseResult{}) {
			return errors.New("joined response exposed a success")
		}
		return err
	})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || calls != 1 || a.mutations != 0 || len(a.lockErrors) != 1 {
		t.Fatalf("joined response: %v, scope authority=%d detached mutations=%d", err, calls, a.mutations)
	}
	var stale *store.LockedLeasedVersionConflict
	if !errors.As(a.lockErrors[0], &stale) {
		t.Fatal("joined response did not encounter the real stale version")
	}
	handoffRecognitionAssertRows(t, before, handoffRecognitionRows(t, f))
	handoffRecognitionAssertTouch(t, claim, handoffRecognitionClaim(t, f), 0)
}

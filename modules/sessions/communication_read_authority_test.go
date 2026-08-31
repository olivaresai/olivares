// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var _ func(
	*Module,
	context.Context,
	DirectoryScopeRef,
	auth.PrincipalRef,
	model.ID,
) (DirectNoticeReadResult, error) = (*Module).GetDirectNoticeMessage

func TestDirectNoticeExactPointReadHasNoProductionLegacyCaller(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate exact direct notice read test")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read sessions package: %v", err)
	}
	files := token.NewFileSet()
	legacyOpenerCalls := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		name := filepath.Join(directory, entry.Name())
		file, parseErr := parser.ParseFile(files, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry.Name(), parseErr)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "getDirectNoticeMessage":
					t.Errorf(
						"legacy point-read seam referenced by production %s in %s:%d",
						function.Name.Name, entry.Name(), files.Position(selector.Pos()).Line,
					)
				case "getDirectNoticeMessageWithOpener":
					if function.Name.Name != "getDirectNoticeMessage" {
						t.Errorf(
							"legacy point-read opener referenced by production %s in %s:%d",
							function.Name.Name, entry.Name(), files.Position(selector.Pos()).Line,
						)
					} else {
						legacyOpenerCalls++
					}
				}
				return true
			})
		}
	}
	if legacyOpenerCalls != 1 {
		t.Fatalf("legacy point-read compatibility bridge count = %d, want exactly one", legacyOpenerCalls)
	}
}

type directNoticeExactReadLegacyAuthorizer struct {
	calls atomic.Int64
}

func (a *directNoticeExactReadLegacyAuthorizer) AuthorizeEntityRead(
	context.Context,
	CommunicationPrincipal,
	EntityRef,
) (ReadWitness, error) {
	a.calls.Add(1)
	return ReadWitness{}, errors.New("legacy direct notice read authority must not run")
}

type directNoticeExactAdditionalSubjectsClosure struct {
	inner      *directNoticeReadClosureResolver
	additional []CommunicationSubjectRef
}

func (r *directNoticeExactAdditionalSubjectsClosure) ResolveChannelGrantSubjects(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	closure, err := r.inner.ResolveChannelGrantSubjects(ctx, scope, principal)
	if err != nil {
		return ChannelGrantSubjectClosure{}, err
	}
	closure.Subjects = append(
		append([]CommunicationSubjectRef(nil), closure.Subjects...),
		r.additional...,
	)
	return closure, nil
}

type directNoticeExactReadFixture struct {
	directNoticeFixture
	reader    auth.Principal
	readerRef auth.PrincipalRef
	published DirectNoticePublishResult
	resolver  *directNoticeReadDirectoryResolver
	closure   *directNoticeReadClosureResolver
	legacy    *directNoticeExactReadLegacyAuthorizer
}

type directNoticeExactAuthorityFirstData struct {
	inner            api.ModuleData
	authorityLocked  atomic.Bool
	earlyObservation atomic.Bool
	observations     atomic.Int64
	getErr           error
	lockErr          error
	listErrKind      model.Kind
	listErr          error
}

func (d *directNoticeExactAuthorityFirstData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeExactAuthorityFirstData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.authorityLocked.Store(false)
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, clockOK := sc.(store.TransactionClock)
		locker, lockerOK := sc.(store.TransactionLocker)
		authority, authorityOK := sc.(store.AuthoritySnapshotLocker)
		directory, directoryOK := sc.(store.DirectorySnapshotReader)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK {
			return errors.New("exact-read ordering scope lacks transaction capabilities")
		}
		return fn(&directNoticeExactAuthorityFirstScope{
			Scope:     sc,
			clock:     clock,
			locker:    locker,
			authority: authority,
			directory: directory,
			data:      d,
		})
	})
}

type directNoticeExactAuthorityFirstScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	data      *directNoticeExactAuthorityFirstData
}

func (s *directNoticeExactAuthorityFirstScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeExactAuthorityFirstScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeExactAuthorityFirstScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	if err := s.authority.LockAuthoritySnapshot(ctx, facts); err != nil {
		return err
	}
	s.data.authorityLocked.Store(true)
	return nil
}

func (s *directNoticeExactAuthorityFirstScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeExactAuthorityFirstScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *directNoticeExactAuthorityFirstScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	return &directNoticeExactAuthorityFirstRepo{
		GenericRepo: repo,
		data:        s.data,
		kind:        kind,
	}, nil
}

type directNoticeExactAuthorityFirstRepo struct {
	store.GenericRepo
	data *directNoticeExactAuthorityFirstData
	kind model.Kind
}

func (r *directNoticeExactAuthorityFirstRepo) beforeObservation() error {
	r.data.observations.Add(1)
	if r.data.authorityLocked.Load() {
		return nil
	}
	r.data.earlyObservation.Store(true)
	return errors.New("exact-read carrier observed before authority snapshot lock")
}

func (r *directNoticeExactAuthorityFirstRepo) Get(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	if err := r.beforeObservation(); err != nil {
		return nil, err
	}
	if r.data.getErr != nil {
		return nil, r.data.getErr
	}
	return r.GenericRepo.Get(ctx, id)
}

func (r *directNoticeExactAuthorityFirstRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	if err := r.beforeObservation(); err != nil {
		return nil, model.Page{}, err
	}
	if r.data.listErr != nil && r.kind == r.data.listErrKind {
		return nil, model.Page{}, r.data.listErr
	}
	return r.GenericRepo.List(ctx, query)
}

func (r *directNoticeExactAuthorityFirstRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	if r.data.lockErr != nil {
		return nil, r.data.lockErr
	}
	locker, ok := r.GenericRepo.(store.RowLocker[model.Record])
	if !ok {
		return nil, errors.New("exact-read ordering repository lacks row lock")
	}
	return locker.Lock(ctx, id)
}

func (r *directNoticeExactAuthorityFirstRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact-read ordering repository lacks stamped create")
	}
	return stamped.CreateAtTransactionTime(ctx, record)
}

func (r *directNoticeExactAuthorityFirstRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact-read ordering repository lacks stamped create-with-id")
	}
	return stamped.CreateWithIDAtTransactionTime(ctx, id, record)
}

func (r *directNoticeExactAuthorityFirstRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact-read ordering repository lacks stamped update")
	}
	return stamped.UpdateAtTransactionTime(ctx, record)
}

type directNoticeExactSequencedClockData struct {
	inner   api.ModuleData
	refresh model.Timestamp
	final   model.Timestamp
	calls   atomic.Int64
}

func (d *directNoticeExactSequencedClockData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeExactSequencedClockData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, clockOK := sc.(store.TransactionClock)
		locker, lockerOK := sc.(store.TransactionLocker)
		authority, authorityOK := sc.(store.AuthoritySnapshotLocker)
		directory, directoryOK := sc.(store.DirectorySnapshotReader)
		if !clockOK || !lockerOK || !authorityOK || !directoryOK {
			return errors.New("exact-read sequenced clock lacks transaction capabilities")
		}
		return fn(&directNoticeExactSequencedClockScope{
			Scope:     sc,
			clock:     clock,
			locker:    locker,
			authority: authority,
			directory: directory,
			data:      d,
		})
	})
}

type directNoticeExactSequencedClockScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	data      *directNoticeExactSequencedClockData
}

func (s *directNoticeExactSequencedClockScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	raw, err := s.clock.TransactionNow(ctx)
	if err != nil {
		return model.Timestamp{}, err
	}
	switch s.data.calls.Add(1) {
	case 1:
		return raw, nil
	case 2:
		return s.data.refresh, nil
	default:
		return s.data.final, nil
	}
}

func (s *directNoticeExactSequencedClockScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeExactSequencedClockScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *directNoticeExactSequencedClockScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeExactSequencedClockScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

type directNoticeExactResidualNotFoundData struct {
	inner         api.ModuleData
	afterCallback bool
	callbacks     atomic.Int64
}

func (d *directNoticeExactResidualNotFoundData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeExactResidualNotFoundData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	if !d.afterCallback {
		return store.ErrNotFound
	}
	if err := d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		d.callbacks.Add(1)
		return fn(sc)
	}); err != nil {
		return err
	}
	return store.ErrNotFound
}

func newDirectNoticeExactReadFixture(t *testing.T) directNoticeExactReadFixture {
	return newDirectNoticeExactReadFixtureWithGrantExpiries(t, nil)
}

func newDirectNoticeExactReadFixtureWithGrantExpiries(
	t *testing.T,
	grantExpiries []time.Duration,
) directNoticeExactReadFixture {
	t.Helper()
	ctx := context.Background()
	fixture := newDirectNoticeExactAuthorityFixture(t)
	onboarded, err := fixture.authr.OnboardMember(
		ctx,
		fixture.authUser,
		fixture.tenant,
		auth.OnboardInput{
			Email:       "reader@direct-notice.test",
			DisplayName: "Direct notice reader",
			Role:        auth.RoleViewer,
			Password:    "direct-notice-reader-password",
		},
	)
	if err != nil {
		t.Fatalf("onboard exact direct notice reader: %v", err)
	}
	token, _, err := fixture.authr.Login(
		ctx,
		"reader@direct-notice.test",
		"direct-notice-reader-password",
		"127.0.0.3",
	)
	if err != nil {
		t.Fatalf("login exact direct notice reader: %v", err)
	}
	reader, err := fixture.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate exact direct notice reader: %v", err)
	}
	readerRef, ok := reader.Ref()
	if !ok {
		t.Fatal("authenticated exact direct notice reader has no opaque ref")
	}

	fixture.recipient = onboarded.User.ID
	readerSubject := CommunicationSubjectRef{Kind: SubjectUser, Ref: fixture.recipient.String()}
	var additionalSubjects []CommunicationSubjectRef
	if len(grantExpiries) == 0 {
		createDirectNoticeExactReadGrantForTest(t, fixture, readerSubject, nil)
	} else {
		for index, after := range grantExpiries {
			subject := readerSubject
			if index > 0 {
				subject = CommunicationSubjectRef{
					Kind: SubjectUserGroup,
					Ref:  model.NewID().String(),
				}
				additionalSubjects = append(additionalSubjects, subject)
			}
			expiresAt := fixture.now.Add(after)
			createDirectNoticeExactReadGrantForTest(t, fixture, subject, &expiresAt)
		}
	}
	epoch, facts := directNoticeExactReadEpochFacts(t, fixture)
	fixture.epoch = epoch
	fixture.attestor.epoch = epoch
	fixture.closure.epoch = epoch
	fixture.authorizer.facts = []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: epoch,
	}}
	fixture.source.evidence.Facts = append([]store.AuthorizationFactRef(nil), facts...)
	fixture.source.evidence.ObservedAt = fixture.now
	fixture.source.evidence.FreshUntil = fixture.now.Add(5 * time.Minute)

	publishCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	published, err := fixture.m.publishDirectNoticeWithAuthority(
		publishCtx,
		fixture.scope,
		fixture.ref,
		fixture.command(model.NewID(), "exact read canary"),
	)
	if err != nil {
		t.Fatalf("publish exact direct notice read fixture: %v", err)
	}

	resolver := &directNoticeReadDirectoryResolver{now: fixture.now, epoch: epoch}
	closure := &directNoticeReadClosureResolver{now: fixture.now, epoch: epoch}
	legacy := &directNoticeExactReadLegacyAuthorizer{}
	fixture.m.communicationDirectoryResolver = resolver
	if len(additionalSubjects) == 0 {
		fixture.m.communicationGrantClosure = closure
	} else {
		fixture.m.communicationGrantClosure = &directNoticeExactAdditionalSubjectsClosure{
			inner: closure,
			additional: append(
				[]CommunicationSubjectRef(nil), additionalSubjects...,
			),
		}
	}
	fixture.m.communicationReadAuthorizer = legacy
	fixture.source.calls = 0
	fixture.source.requests = nil
	fixture.source.trace = nil
	return directNoticeExactReadFixture{
		directNoticeFixture: fixture,
		reader:              reader,
		readerRef:           readerRef,
		published:           published,
		resolver:            resolver,
		closure:             closure,
		legacy:              legacy,
	}
}

func createDirectNoticeExactReadGrantForTest(
	t *testing.T,
	fixture directNoticeFixture,
	subject CommunicationSubjectRef,
	expiresAt *time.Time,
) model.ID {
	t.Helper()
	id := model.NewID()
	grant := ChannelGrant{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
				Version: 1, CreatedAt: fixture.now,
			},
			UpdatedAt: fixture.now,
		},
		ChannelID:  fixture.channel.ID,
		Subject:    subject,
		Generation: 1,
		CanRead:    true,
		State:      ChannelGrantActive,
		GrantedBy:  CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
		ExpiresAt:  cloneDirectNoticeTime(expiresAt),
	}
	record, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode exact-read ChannelGrant: %v", err)
	}
	if _, err := communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant, channelGrantKind, id, record,
	); err != nil {
		t.Fatalf("create exact-read ChannelGrant: %v", err)
	}
	return id
}

func directNoticeExactReadEpochFacts(
	t *testing.T,
	fixture directNoticeFixture,
) (int64, []store.AuthorizationFactRef) {
	t.Helper()
	ctx := context.Background()
	var (
		directoryEpoch    model.DirectoryEpoch
		authorizationFact store.AuthorizationFactRef
	)
	err := fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		directory, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("exact read fixture lacks DirectoryEpoch reader")
		}
		authorization, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("exact read fixture lacks AuthorizationEpoch reader")
		}
		var err error
		directoryEpoch, err = directory.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		authorizationFact, err = authorization.ReadAuthorizationEpoch(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("read exact direct notice authority epochs: %v", err)
	}
	facts, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{
		authorizationFact,
		{
			Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant),
			Version: directoryEpoch.Version,
		},
	})
	if err != nil {
		t.Fatalf("canonicalize exact direct notice authority epochs: %v", err)
	}
	return directoryEpoch.Version, facts
}

func directNoticeExactReadRows(
	t *testing.T,
	fixture directNoticeFixture,
) map[model.Kind][]model.Record {
	t.Helper()
	rows := make(map[model.Kind][]model.Record)
	for _, kind := range []model.Kind{
		messageKind,
		messageDeliveryKind,
		messageAudienceKind,
		messageAudienceRecipientKind,
	} {
		rows[kind] = communicationRowsForTest(t, fixture, kind)
	}
	return rows
}

func TestDirectNoticeExactPointReadUsesOneBoundMutationAndNoLegacyAuthority(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	before := directNoticeExactReadRows(t, fixture.directNoticeFixture)
	base := fixture.m.data
	authorityTrace := &directNoticeAuthorityTrace{}
	authorityFirst := &directNoticeExactAuthorityFirstData{inner: base}
	observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
		inner: authorityFirst,
		trace: authorityTrace,
	}}
	fixture.m.data = observer
	var opens atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
		func(
			ctx context.Context,
			sealer CommunicationContentSealer,
			plan ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			if observer.inMutate.Load() {
				return nil, errors.New("exact payload opened before communication commit")
			}
			return OpenProtectedPayload(ctx, sealer, plan)
		},
	)
	if err != nil {
		t.Fatalf("exact direct notice point read: %v", err)
	}
	if result.Message.ID != fixture.published.MessageID ||
		result.Delivery.ID != fixture.published.DeliveryID ||
		result.Message.Content.Blocks[0].Text != "exact read canary" || opens.Load() != 1 {
		t.Fatalf("exact direct notice result = %+v, opens=%d", result, opens.Load())
	}
	if fixture.legacy.calls.Load() != 0 || observer.views.Load() != 0 ||
		observer.mutates.Load() != 1 || authorityFirst.earlyObservation.Load() ||
		authorityFirst.observations.Load() < 2 {
		t.Fatalf(
			"exact read legacy/views/mutates/early/observations = %d/%d/%d/%t/%d",
			fixture.legacy.calls.Load(),
			observer.views.Load(),
			observer.mutates.Load(),
			authorityFirst.earlyObservation.Load(),
			authorityFirst.observations.Load(),
		)
	}
	if fixture.source.calls != 1 || len(fixture.source.requests) != 1 {
		t.Fatalf(
			"exact read source calls/requests = %d/%d, want 1/1",
			fixture.source.calls, len(fixture.source.requests),
		)
	}
	request := fixture.source.requests[0]
	requestRef, ok := request.Principal.Ref()
	if !ok || requestRef != fixture.readerRef || request.Tenant != fixture.tenant ||
		request.Permission != permMessageRead || request.Resource.Kind != string(messageKind) ||
		request.Resource.ID != fixture.published.MessageID.String() ||
		request.Resource.WorkspaceID != fixture.workspace || len(request.Resource.Extra) != 0 {
		t.Fatalf("exact point-read authorization request = %#v, ref ok=%t", request, ok)
	}
	if len(authorityTrace.authorityFacts) != 1 || len(authorityTrace.steps) < 2 ||
		authorityTrace.steps[0] != "authority" ||
		authorityTrace.steps[1] != "transaction:"+directNoticeMessageLockKey(
			fixture.scope,
			fixture.published.MessageID,
		) || authorityTrace.nowCalls != 3 {
		t.Fatalf(
			"exact read transaction trace = steps %v facts %v now %d",
			authorityTrace.steps,
			authorityTrace.authorityFacts,
			authorityTrace.nowCalls,
		)
	}
	foundDirectory, foundAuthorization := false, false
	for _, fact := range authorityTrace.authorityFacts[0] {
		switch fact.Kind {
		case model.DirectoryEpochKind:
			foundDirectory = fact.ID == model.ID(fixture.tenant) && fact.Version == fixture.epoch
		case model.AuthorizationEpochKind:
			foundAuthorization = fact.ID == model.ID(fixture.tenant) && fact.Version > 0
		}
	}
	if !foundDirectory || !foundAuthorization {
		t.Fatalf(
			"exact read locked facts = %+v, want DirectoryEpoch+AuthorizationEpoch",
			authorityTrace.authorityFacts[0],
		)
	}

	fixture.m.data = base
	after := directNoticeExactReadRows(t, fixture.directNoticeFixture)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("exact point read changed domain rows\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestDirectNoticeExactPointReadPublicBoundaryBindsBeforeReadinessAndStaysOff(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	trace := []string{}
	resolver := &communicationAuthorityResolverRecorder{
		resolved: fixture.reader,
		trace:    &trace,
	}
	fixture.source.trace = &trace
	fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
	readiness := &communicationReadinessStub{
		storeReady:  true,
		sealerReady: true,
		trace:       &trace,
	}
	fixture.m.UseCommunicationStoreReadinessWitness(readiness)
	fixture.m.UseCommunicationContentSealer(readiness)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, err := fixture.m.GetDirectNoticeMessage(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public exact point read = %v, want OFF/UNKNOWN", err)
	}
	wantTrace := []string{"resolve", "authorize", "store", "sealer"}
	if !reflect.DeepEqual(trace, wantTrace) {
		t.Fatalf("public exact point-read trace = %v, want %v", trace, wantTrace)
	}
	if observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
		fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 ||
		fixture.legacy.calls.Load() != 0 || readiness.cryptoCalls != 0 {
		t.Fatalf(
			"OFF exact read observers view/mutate/resolve/closure/legacy/open = %d/%d/%d/%d/%d/%d",
			observer.views.Load(),
			observer.mutates.Load(),
			fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(),
			fixture.legacy.calls.Load(),
			readiness.cryptoCalls,
		)
	}
}

func TestDirectNoticeExactPointReadRejectsUnboundRequestsBeforeObservers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		noDeadline bool
		zeroRef    bool
	}{
		{name: "finite_deadline_required", noDeadline: true},
		{name: "zero_principal_ref", zeroRef: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAuthorityFixture(t)
			resolver := &communicationAuthorityResolverRecorder{resolved: fixture.authUser}
			fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
			observer := &directNoticeMutateObserverData{inner: fixture.m.data}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx := context.Background()
			if !test.noDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
			}
			ref := fixture.ref
			if test.zeroRef {
				ref = auth.PrincipalRef{}
			}
			_, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				ref,
				model.NewID(),
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected exact point-read open")
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 ||
				fixture.source.calls != 0 || observer.views.Load() != 0 ||
				observer.mutates.Load() != 0 || fixture.closure.calls.Load() != 0 ||
				opens.Load() != 0 {
				t.Fatalf(
					"unbound exact read = %v; resolver/source/view/mutate/closure/open %d/%d/%d/%d/%d/%d",
					err,
					resolver.calls,
					fixture.source.calls,
					observer.views.Load(),
					observer.mutates.Load(),
					fixture.closure.calls.Load(),
					opens.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadNormalizesCoreDenyAndPreservesUnknown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		evidence     auth.AuthorizationEvidence
		want         error
		wantNotFound bool
	}{
		{
			name: "deny_is_concealed",
			evidence: auth.AuthorizationEvidence{
				Outcome: auth.EvidenceDeny,
				CorePermission: auth.CheckEvidence{
					Verdict: auth.CheckBroken,
					Code:    "core_permission_denied",
				},
				ResourceGuard: auth.CheckEvidence{
					Verdict: auth.CheckUnknown,
					Code:    "not_evaluated",
				},
				ForbidAbsence: auth.CheckEvidence{
					Verdict: auth.CheckUnknown,
					Code:    "not_evaluated",
				},
			},
			want:         ErrCommunicationNotFound,
			wantNotFound: true,
		},
		{
			name: "unknown_stays_unknown",
			evidence: auth.AuthorizationEvidence{
				Outcome: auth.EvidenceUnknown,
				CorePermission: auth.CheckEvidence{
					Verdict: auth.CheckUnknown,
					Code:    "unavailable",
				},
				ResourceGuard: auth.CheckEvidence{
					Verdict: auth.CheckUnknown,
					Code:    "unavailable",
				},
				ForbidAbsence: auth.CheckEvidence{
					Verdict: auth.CheckUnknown,
					Code:    "unavailable",
				},
			},
			want: ErrCommunicationEvidenceUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAuthorityFixture(t)
			fixture.source.evidence = test.evidence
			resolver := &communicationAuthorityResolverRecorder{resolved: fixture.authUser}
			fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
			observer := &directNoticeMutateObserverData{inner: fixture.m.data}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.ref,
				model.NewID(),
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected exact point-read open")
				},
			)
			if !errors.Is(err, test.want) ||
				(!test.wantNotFound && errors.Is(err, ErrCommunicationNotFound)) ||
				resolver.calls != 1 || fixture.source.calls != 1 ||
				observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
				fixture.closure.calls.Load() != 0 || opens.Load() != 0 {
				t.Fatalf(
					"core %s exact read = %v; resolver/source/view/mutate/closure/open %d/%d/%d/%d/%d/%d",
					test.name,
					err,
					resolver.calls,
					fixture.source.calls,
					observer.views.Load(),
					observer.mutates.Load(),
					fixture.closure.calls.Load(),
					opens.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadNeverConcealsJoinedUnknownAndNotFound(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		getErr      error
		lockErr     error
		listErrKind model.Kind
		listErr     error
	}{
		{
			name:   "discovery",
			getErr: errors.Join(ErrCommunicationEvidenceUnknown, store.ErrNotFound),
		},
		{
			name:    "locked_carrier",
			lockErr: errors.Join(ErrCommunicationEvidenceUnknown, store.ErrNotFound),
		},
		{
			name:        "grant_observation",
			listErrKind: channelGrantKind,
			listErr:     errors.Join(ErrCommunicationEvidenceUnknown, store.ErrNotFound),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			before := directNoticeExactReadRows(t, fixture.directNoticeFixture)
			fault := &directNoticeExactAuthorityFirstData{
				inner: fixture.m.data, getErr: test.getErr, lockErr: test.lockErr,
				listErrKind: test.listErrKind, listErr: test.listErr,
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				fixture.published.MessageID,
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("joined UNKNOWN/NotFound opened protected content")
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				errors.Is(err, ErrCommunicationNotFound) ||
				!reflect.DeepEqual(result, DirectNoticeReadResult{}) ||
				observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
				opens.Load() != 0 || fixture.legacy.calls.Load() != 0 {
				t.Fatalf(
					"joined UNKNOWN/NotFound read = (%+v, %v), view/mutate/open/legacy %d/%d/%d/%d",
					result, err, observer.views.Load(), observer.mutates.Load(),
					opens.Load(), fixture.legacy.calls.Load(),
				)
			}
			after := directNoticeExactReadRows(t, fixture.directNoticeFixture)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("joined UNKNOWN/NotFound changed rows\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestDirectNoticeExactPointReadNormalizersPreferUnknownOverNotFound(t *testing.T) {
	t.Parallel()

	joined := errors.Join(ErrCommunicationEvidenceUnknown, store.ErrNotFound)
	for _, test := range []struct {
		name      string
		normalize func(error) error
	}{
		{name: "locked_carrier", normalize: normalizeDirectNoticeLockedNotFound},
		{name: "point_read_boundary", normalize: normalizeDirectNoticePointReadError},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.normalize(joined)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				errors.Is(err, ErrCommunicationNotFound) {
				t.Fatalf("normalized UNKNOWN/NotFound = %v, want UNKNOWN without concealed 404", err)
			}
		})
	}
}

func TestDirectNoticeExactPointReadRejectsAgentBeforeLocalObservers(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	agent := fixture.reader.WithAgentIdentity("agent:" + model.NewID().String())
	resolver := &communicationAuthorityResolverRecorder{resolved: agent}
	fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	var opens atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("agent principal opened protected content")
		},
	)
	if !errors.Is(err, ErrCommunicationNotFound) ||
		errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(result, DirectNoticeReadResult{}) || resolver.calls != 1 ||
		fixture.source.calls != 1 || fixture.resolver.calls.Load() != 0 ||
		fixture.closure.calls.Load() != 0 || observer.views.Load() != 0 ||
		observer.mutates.Load() != 0 || fixture.legacy.calls.Load() != 0 || opens.Load() != 0 {
		t.Fatalf(
			"agent point read = (%+v, %v), authority/local/data/open %d/%d/%d/%d/%d/%d/%d/%d",
			result, err, resolver.calls, fixture.source.calls,
			fixture.resolver.calls.Load(), fixture.closure.calls.Load(),
			observer.views.Load(), observer.mutates.Load(), fixture.legacy.calls.Load(), opens.Load(),
		)
	}
}

func TestDirectNoticeExactPointReadConcealsMissingAfterFinalFreshness(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		final   func(time.Time) time.Time
		wantErr error
	}{
		{
			name:    "current_conceals",
			final:   func(now time.Time) time.Time { return now.Add(2*time.Minute - time.Nanosecond) },
			wantErr: ErrCommunicationNotFound,
		},
		{
			name:    "expiry_wins_over_concealment",
			final:   func(now time.Time) time.Time { return now.Add(2 * time.Minute) },
			wantErr: ErrCommunicationEvidenceUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			fixture.resolver.freshFor = 2 * time.Minute
			fixture.closure.freshFor = 4 * time.Minute
			finalClock := &directNoticeFinalExpiryData{
				inner: fixture.m.data,
				final: model.NewTimestamp(test.final(fixture.now)),
			}
			observer := &directNoticeMutateObserverData{inner: finalClock}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				model.NewID(),
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected missing-carrier open")
				},
			)
			if !errors.Is(err, test.wantErr) ||
				(test.wantErr == ErrCommunicationEvidenceUnknown &&
					errors.Is(err, ErrCommunicationNotFound)) ||
				!reflect.DeepEqual(result, DirectNoticeReadResult{}) ||
				finalClock.calls.Load() != 3 || observer.views.Load() != 0 ||
				observer.mutates.Load() != 1 || opens.Load() != 0 {
				t.Fatalf(
					"missing exact read = (%+v, %v); clocks/view/mutate/open %d/%d/%d/%d",
					result,
					err,
					finalClock.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					opens.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadNeverConcealsResidualStoreNotFound(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		afterCallback bool
		callbacks     int64
	}{
		{name: "outside_callback"},
		{name: "after_successful_callback", afterCallback: true, callbacks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			fault := &directNoticeExactResidualNotFoundData{
				inner:         fixture.m.data,
				afterCallback: test.afterCallback,
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				fixture.published.MessageID,
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected residual-not-found open")
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				errors.Is(err, ErrCommunicationNotFound) ||
				!reflect.DeepEqual(result, DirectNoticeReadResult{}) ||
				observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
				fault.callbacks.Load() != test.callbacks || opens.Load() != 0 {
				t.Fatalf(
					"residual NotFound %s = (%+v, %v); view/mutate/callback/open %d/%d/%d/%d",
					test.name,
					result,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					fault.callbacks.Load(),
					opens.Load(),
				)
			}
		})
	}

	t.Run("authority_lock", func(t *testing.T) {
		fixture := newDirectNoticeExactReadFixture(t)
		fault := &directNoticeAuthorityFaultData{
			inner:  fixture.m.data,
			failAt: 1,
			err:    store.ErrNotFound,
		}
		fixture.m.data = fault
		var opens atomic.Int64
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
			ctx,
			fixture.scope,
			fixture.readerRef,
			fixture.published.MessageID,
			func(
				context.Context,
				CommunicationContentSealer,
				ProtectedPayloadOpenPlan,
			) (json.RawMessage, error) {
				opens.Add(1)
				return nil, errors.New("unexpected authority-not-found open")
			},
		)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
			errors.Is(err, ErrCommunicationNotFound) ||
			!reflect.DeepEqual(result, DirectNoticeReadResult{}) ||
			fault.calls.Load() != 1 || opens.Load() != 0 {
			t.Fatalf(
				"authority NotFound = (%+v, %v); locks/open %d/%d",
				result,
				err,
				fault.calls.Load(),
				opens.Load(),
			)
		}
	})
}

func TestDirectNoticeExactPointReadUsesLocalWindowAtFinalization(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		final   func(time.Time) time.Time
		wantErr error
		opens   int64
	}{
		{
			name:    "expires_at_boundary",
			final:   func(now time.Time) time.Time { return now.Add(2 * time.Minute) },
			wantErr: ErrCommunicationEvidenceUnknown,
		},
		{
			name:  "one_nanosecond_before",
			final: func(now time.Time) time.Time { return now.Add(2*time.Minute - time.Nanosecond) },
			opens: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			fixture.resolver.freshFor = 2 * time.Minute
			fixture.closure.freshFor = 4 * time.Minute
			finalClock := &directNoticeFinalExpiryData{
				inner: fixture.m.data,
				final: model.NewTimestamp(test.final(fixture.now)),
			}
			observer := &directNoticeMutateObserverData{inner: finalClock}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				fixture.published.MessageID,
				func(
					ctx context.Context,
					sealer CommunicationContentSealer,
					plan ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return OpenProtectedPayload(ctx, sealer, plan)
				},
			)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) ||
					!reflect.DeepEqual(result, DirectNoticeReadResult{}) {
					t.Fatalf("final local-window read = (%+v, %v), want empty %v", result, err, test.wantErr)
				}
			} else if err != nil || result.Message.ID != fixture.published.MessageID {
				t.Fatalf("current local-window read = (%+v, %v)", result, err)
			}
			if finalClock.calls.Load() != 3 || observer.views.Load() != 0 ||
				observer.mutates.Load() != 1 || opens.Load() != test.opens ||
				fixture.legacy.calls.Load() != 0 {
				t.Fatalf(
					"local-window clocks/view/mutate/open/legacy = %d/%d/%d/%d/%d",
					finalClock.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					opens.Load(),
					fixture.legacy.calls.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadUsesLatestMatchingGrantExpiry(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		final   func(time.Time) time.Time
		wantErr error
		opens   int64
	}{
		{
			name:  "latest_expiry_minus_one_nanosecond",
			final: func(now time.Time) time.Time { return now.Add(4*time.Minute - time.Nanosecond) },
			opens: 1,
		},
		{
			name:    "latest_expiry_boundary",
			final:   func(now time.Time) time.Time { return now.Add(4 * time.Minute) },
			wantErr: ErrCommunicationEvidenceUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixtureWithGrantExpiries(
				t,
				[]time.Duration{2 * time.Minute, 4 * time.Minute},
			)
			earlyExpiry := fixture.now.Add(2 * time.Minute)
			sequenced := &directNoticeExactSequencedClockData{
				inner:   fixture.m.data,
				refresh: model.NewTimestamp(earlyExpiry.Add(-time.Nanosecond)),
				final:   model.NewTimestamp(test.final(fixture.now)),
			}
			observer := &directNoticeMutateObserverData{inner: sequenced}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				fixture.published.MessageID,
				func(
					ctx context.Context,
					sealer CommunicationContentSealer,
					plan ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return OpenProtectedPayload(ctx, sealer, plan)
				},
			)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) ||
					!reflect.DeepEqual(result, DirectNoticeReadResult{}) {
					t.Fatalf("grant-expiry boundary read = (%+v, %v)", result, err)
				}
			} else if err != nil || result.Message.ID != fixture.published.MessageID {
				t.Fatalf("grant-expiry current read = (%+v, %v)", result, err)
			}
			if sequenced.calls.Load() != 3 || observer.views.Load() != 0 ||
				observer.mutates.Load() != 1 || opens.Load() != test.opens ||
				fixture.legacy.calls.Load() != 0 {
				t.Fatalf(
					"grant horizon clocks/view/mutate/open/legacy = %d/%d/%d/%d/%d",
					sequenced.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					opens.Load(),
					fixture.legacy.calls.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadGrantHorizonHonorsNonExpiringAlternative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(2 * time.Minute)
	directSubject := CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()}
	groupSubject := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()}
	deadline, constrained, err := directNoticeReadGrantFreshUntil(
		[]ChannelGrant{
			{Subject: directSubject, State: ChannelGrantActive, CanRead: true, ExpiresAt: &expiresAt},
			{Subject: groupSubject, State: ChannelGrantActive, CanRead: true},
		},
		ChannelGrantSubjectClosure{
			Outcome: ReadAllow,
			Subjects: []CommunicationSubjectRef{
				directSubject,
				groupSubject,
			},
		},
		now,
	)
	if err != nil || constrained || !deadline.IsZero() {
		t.Fatalf(
			"mixed finite/non-expiring grant horizon = (%s, %t, %v), want no constraint",
			deadline, constrained, err,
		)
	}
}

func TestDirectNoticeExactPointReadIntersectsAuthorityWindows(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	identity := directNoticeReaderIdentityPreflight{
		Resolution: PrincipalResolution{
			ObservedAt: base.Add(2 * time.Minute),
			FreshUntil: base.Add(10 * time.Minute),
		},
		Closure: ChannelGrantSubjectClosure{
			ObservedAt: base.Add(4 * time.Minute),
			FreshUntil: base.Add(8 * time.Minute),
		},
	}
	local, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil || !local.observedAt.Equal(base.Add(4*time.Minute)) ||
		!local.freshUntil.Equal(base.Add(8*time.Minute)) {
		t.Fatalf("local authority intersection = %+v, %v", local, err)
	}
	bindingID := &communicationRequestAuthorityBindingID{marker: 1}
	core := communicationRequestAuthoritySnapshot{
		facts: []store.AuthorizationFactRef{{
			Kind: model.AuthorizationEpochKind, ID: model.NewID(), Version: 1,
		}},
		observedAt: base.Add(3 * time.Minute),
		freshUntil: base.Add(9 * time.Minute),
		bindingID:  bindingID,
	}
	narrowed, err := core.narrowTo(local)
	if err != nil || !narrowed.observedAt.Equal(base.Add(4*time.Minute)) ||
		!narrowed.freshUntil.Equal(base.Add(8*time.Minute)) ||
		narrowed.bindingID != bindingID ||
		!reflect.DeepEqual(narrowed.facts, core.facts) {
		t.Fatalf("core/local authority intersection = %+v, %v", narrowed, err)
	}
	disjoint, err := core.narrowTo(communicationAuthorityWindow{
		observedAt: core.freshUntil,
		freshUntil: core.freshUntil.Add(time.Minute),
	})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(disjoint, communicationRequestAuthoritySnapshot{}) {
		t.Fatalf("disjoint authority intersection = %+v, %v, want empty UNKNOWN", disjoint, err)
	}
}

func TestDirectNoticeExactPointReadRejectsDBTimeBeforeLatestAuthorityObservation(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	fixture.resolver.now = fixture.now
	fixture.resolver.freshFor = 10 * time.Minute
	fixture.closure.now = fixture.now.Add(2 * time.Minute)
	fixture.closure.freshFor = 8 * time.Minute
	fixture.m.clock = &testClock{now: fixture.now.Add(3 * time.Minute)}
	order := &directNoticeExactAuthorityFirstData{inner: fixture.m.data}
	trace := &directNoticeAuthorityTrace{}
	observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
		inner: order,
		trace: trace,
	}}
	fixture.m.data = observer
	var opens atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected pre-observation point-read open")
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		!reflect.DeepEqual(result, DirectNoticeReadResult{}) || observer.views.Load() != 0 ||
		observer.mutates.Load() != 1 || len(trace.steps) != 0 ||
		order.observations.Load() != 0 || order.earlyObservation.Load() ||
		fixture.legacy.calls.Load() != 0 || opens.Load() != 0 {
		t.Fatalf(
			"pre-observation DB time read = (%+v, %v); view/mutate/steps/observations/early/legacy/open %d/%d/%v/%d/%t/%d/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			trace.steps,
			order.observations.Load(),
			order.earlyObservation.Load(),
			fixture.legacy.calls.Load(),
			opens.Load(),
		)
	}
}

func TestDirectNoticeExactPointReadRejectsCredentialRevocationBeforeCarrierReads(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	var revokeErr error
	fixture.closure.beforeReturn = func() {
		revokeErr = fixture.authr.RevokeSession(
			context.Background(),
			fixture.reader,
			fixture.reader.CredID,
		)
	}
	trace := &directNoticeAuthorityTrace{}
	observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
		inner: fixture.m.data,
		trace: trace,
	}}
	fixture.m.data = observer
	var opens atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
		func(
			context.Context,
			CommunicationContentSealer,
			ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens.Add(1)
			return nil, errors.New("unexpected revoked-reader open")
		},
	)
	if revokeErr != nil {
		t.Fatalf("revoke exact read credential: %v", revokeErr)
	}
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || observer.views.Load() != 0 ||
		observer.mutates.Load() != 1 || !reflect.DeepEqual(trace.steps, []string{"authority"}) ||
		fixture.legacy.calls.Load() != 0 || opens.Load() != 0 {
		t.Fatalf(
			"revoked exact read = %v; view/mutate/steps/legacy/open %d/%d/%v/%d/%d",
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			trace.steps,
			fixture.legacy.calls.Load(),
			opens.Load(),
		)
	}
}

type directNoticeExactCrossedResolutionResolver struct {
	DirectorySnapshotResolver
	cross func(*PrincipalResolution)
}

func (r *directNoticeExactCrossedResolutionResolver) ResolvePrincipal(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	resolution, err := r.DirectorySnapshotResolver.ResolvePrincipal(ctx, scope, principal)
	if err == nil && r.cross != nil {
		r.cross(&resolution)
	}
	return resolution, err
}

func TestDirectNoticeExactPointReadRejectsCrossedResolutionBeforeClosure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		cross func(*PrincipalResolution)
	}{
		{
			name: "scope",
			cross: func(resolution *PrincipalResolution) {
				resolution.Scope.WorkspaceID = model.NewID()
				resolution.Recipient.Scope = resolution.Scope
			},
		},
		{
			name: "principal",
			cross: func(resolution *PrincipalResolution) {
				other := model.NewID()
				resolution.Principal = CommunicationPrincipal{UserID: other}
				resolution.Recipient.Recipient.Ref = other.String()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			fixture.m.communicationDirectoryResolver =
				&directNoticeExactCrossedResolutionResolver{
					DirectorySnapshotResolver: fixture.resolver,
					cross:                     test.cross,
				}
			observer := &directNoticeMutateObserverData{inner: fixture.m.data}
			fixture.m.data = observer
			var opens atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, err := fixture.m.getDirectNoticeMessageWithAuthorityAndOpener(
				ctx,
				fixture.scope,
				fixture.readerRef,
				fixture.published.MessageID,
				func(
					context.Context,
					CommunicationContentSealer,
					ProtectedPayloadOpenPlan,
				) (json.RawMessage, error) {
					opens.Add(1)
					return nil, errors.New("unexpected crossed-resolution open")
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				fixture.resolver.calls.Load() != 1 || fixture.closure.calls.Load() != 0 ||
				observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
				fixture.legacy.calls.Load() != 0 || opens.Load() != 0 {
				t.Fatalf(
					"crossed %s resolution = %v; resolver/closure/view/mutate/legacy/open %d/%d/%d/%d/%d/%d",
					test.name,
					err,
					fixture.resolver.calls.Load(),
					fixture.closure.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					fixture.legacy.calls.Load(),
					opens.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactPointReadRejectsAuthorityBindingSplices(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name               string
		spliceInspection   bool
		crossIdentity      bool
		wantSourceBindings int
	}{
		{name: "inspection_from_another_binding", spliceInspection: true, wantSourceBindings: 2},
		{name: "identity_from_another_principal", crossIdentity: true, wantSourceBindings: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactReadFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			question, err := newCommunicationAuthorityQuestion(
				fixture.scope,
				messageKind,
				fixture.published.MessageID,
				CommunicationRead,
			)
			if err != nil {
				t.Fatalf("build exact point-read question: %v", err)
			}
			bound, err := fixture.m.bindCurrentCommunicationRequestAuthority(
				ctx,
				fixture.readerRef,
				question,
			)
			if err != nil {
				t.Fatalf("bind exact point-read authority: %v", err)
			}
			inspected, err := bound.contextFor(question)
			if err != nil {
				t.Fatalf("inspect exact point-read authority: %v", err)
			}
			if test.spliceInspection {
				second, bindErr := fixture.m.bindCurrentCommunicationRequestAuthority(
					ctx,
					fixture.readerRef,
					question,
				)
				if bindErr != nil {
					t.Fatalf("bind second exact point-read authority: %v", bindErr)
				}
				bound = second
			}
			principal := CommunicationPrincipal{UserID: fixture.reader.UserID}
			identity, err := fixture.m.preflightDirectNoticeReaderIdentity(
				ctx,
				fixture.scope,
				principal,
				nil,
			)
			if err != nil {
				t.Fatalf("preflight exact point-read identity: %v", err)
			}
			if test.crossIdentity {
				other := model.NewID()
				identity.Principal = CommunicationPrincipal{UserID: other}
				identity.Recipient.Ref = other.String()
				identity.Resolution.Principal = identity.Principal
				identity.Resolution.Recipient.Recipient = identity.Recipient
				identity.Closure.Principal = identity.Principal
				identity.Closure.Subjects[0].Ref = other.String()
			}
			window, err := directNoticeReaderAuthorityWindow(identity)
			if err != nil {
				t.Fatalf("build exact point-read local window: %v", err)
			}
			trace := &directNoticeAuthorityTrace{}
			observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
				inner: fixture.m.data,
				trace: trace,
			}}
			fixture.m.data = observer
			authorized, hidden, err := fixture.m.authorizeDirectNoticeReadWithAuthority(
				ctx,
				question,
				bound,
				inspected,
				identity,
				fixture.published.MessageID,
				window,
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || hidden ||
				!reflect.DeepEqual(authorized, directNoticeAuthorizedRead{}) ||
				observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
				len(trace.steps) != 0 || fixture.source.calls != test.wantSourceBindings {
				t.Fatalf(
					"exact authority splice %s = (%+v, hidden=%t, %v); view/mutate/steps/source %d/%d/%v/%d",
					test.name,
					authorized,
					hidden,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					trace.steps,
					fixture.source.calls,
				)
			}
		})
	}
}

type directNoticeExactAliasingResolver struct {
	inner     *directNoticeReadDirectoryResolver
	recipient *RecipientSnapshot
}

func (r *directNoticeExactAliasingResolver) ResolveAudience(
	ctx context.Context,
	scope DirectoryScopeRef,
	selectors []AudienceSelector,
) (DirectorySnapshot, error) {
	return r.inner.ResolveAudience(ctx, scope, selectors)
}

func (r *directNoticeExactAliasingResolver) ResolveRecipient(
	ctx context.Context,
	scope DirectoryScopeRef,
	recipient RecipientRef,
) (RecipientSnapshot, error) {
	return r.inner.ResolveRecipient(ctx, scope, recipient)
}

func (r *directNoticeExactAliasingResolver) ResolvePrincipal(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	resolution, err := r.inner.ResolvePrincipal(ctx, scope, principal)
	if err == nil {
		r.recipient = resolution.Recipient
	}
	return resolution, err
}

type directNoticeExactAliasingClosure struct {
	inner    *directNoticeReadClosureResolver
	subjects []CommunicationSubjectRef
}

func (r *directNoticeExactAliasingClosure) ResolveChannelGrantSubjects(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	closure, err := r.inner.ResolveChannelGrantSubjects(ctx, scope, principal)
	if err == nil {
		r.subjects = closure.Subjects
	}
	return closure, err
}

type directNoticeExactAliasMutatingData struct {
	inner  api.ModuleData
	before func()
	calls  atomic.Int64
}

func (d *directNoticeExactAliasMutatingData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeExactAliasMutatingData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	d.calls.Add(1)
	if d.before != nil {
		d.before()
	}
	return d.inner.Mutate(ctx, tenant, fn)
}

func TestDirectNoticeExactPointReadOwnsLocalAuthorityAliasesBeforeMutation(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	resolver := &directNoticeExactAliasingResolver{inner: fixture.resolver}
	closure := &directNoticeExactAliasingClosure{inner: fixture.closure}
	mutatedDuringClosure := atomic.Bool{}
	fixture.closure.beforeReturn = func() {
		if resolver.recipient == nil {
			return
		}
		resolver.recipient.Recipient.Ref = model.NewID().String()
		resolver.recipient.DirectoryEpoch++
		mutatedDuringClosure.Store(true)
	}
	fixture.m.communicationDirectoryResolver = resolver
	fixture.m.communicationGrantClosure = closure
	mutatedBeforeMutation := atomic.Bool{}
	data := &directNoticeExactAliasMutatingData{
		inner: fixture.m.data,
		before: func() {
			if resolver.recipient == nil || len(closure.subjects) == 0 {
				return
			}
			resolver.recipient.Recipient.Ref = model.NewID().String()
			resolver.recipient.DirectoryEpoch++
			closure.subjects[0].Ref = model.NewID().String()
			mutatedBeforeMutation.Store(true)
		},
	}
	fixture.m.data = data
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.getDirectNoticeMessageWithAuthority(
		ctx,
		fixture.scope,
		fixture.readerRef,
		fixture.published.MessageID,
	)
	if err != nil || result.Message.ID != fixture.published.MessageID ||
		!mutatedDuringClosure.Load() || !mutatedBeforeMutation.Load() ||
		data.calls.Load() != 1 || fixture.legacy.calls.Load() != 0 {
		t.Fatalf(
			"alias-mutated exact read = (%+v, %v); closure/mutate aliases, mutates, legacy %t/%t/%d/%d",
			result,
			err,
			mutatedDuringClosure.Load(),
			mutatedBeforeMutation.Load(),
			data.calls.Load(),
			fixture.legacy.calls.Load(),
		)
	}
}

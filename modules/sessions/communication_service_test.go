// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	DirectNoticePublishCommand,
) (DirectNoticePublishResult, error) = (*Module).PublishDirectNotice

func TestLegacyDirectNoticePublishHasNoProductionCallers(t *testing.T) {
	t.Parallel()

	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate communication service test")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read sessions package: %v", err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		name := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(files, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "publishDirectNotice" {
				t.Errorf("legacy publishDirectNotice has production caller in %s:%d",
					entry.Name(), files.Position(call.Pos()).Line)
			}
			return true
		})
	}
}

type directNoticeAudienceAttestor struct {
	epoch int64
	calls atomic.Int64
	fail  atomic.Bool
}

type directNoticeAliasingAudienceAttestor struct {
	inner       PublicationAudienceAttestor
	request     PublicationAudienceRequest
	snapshot    DirectorySnapshot
	attestation PublicationAudienceAttestation
}

func (a *directNoticeAliasingAudienceAttestor) AttestPublicationAudience(
	ctx context.Context,
	request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	snapshot, attestation, err := a.inner.AttestPublicationAudience(ctx, request)
	a.snapshot = snapshot
	a.attestation = attestation
	// Mutate the exact slice handed to the port only after the inner attestor
	// has produced a coherent output. The service must have passed a defensive
	// request clone, otherwise this synchronous adversary rewrites the
	// AudienceRequest retained for validation and apply.
	if err == nil && len(request.Selectors) != 0 {
		request.Selectors[0].Ref = model.NewID().String()
		request.ChannelACLRevision++
	}
	a.request = request
	return snapshot, attestation, err
}

func (a *directNoticeAudienceAttestor) AttestPublicationAudience(
	_ context.Context,
	request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	a.calls.Add(1)
	if a.fail.Load() {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, errors.New("attestor unavailable")
	}
	recipient := RecipientSnapshot{
		Scope:          request.Scope,
		Recipient:      RecipientRef{Kind: RecipientUser, Ref: request.Selectors[0].Ref},
		RecipientEpoch: 1, DirectoryEpoch: a.epoch, Eligible: true,
	}
	rosterHash, err := CanonicalDirectoryRosterHash(request.Scope, a.epoch, []RecipientSnapshot{recipient})
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	snapshot := DirectorySnapshot{
		Scope: request.Scope, Epoch: a.epoch,
		Selectors:  append([]AudienceSelector(nil), request.Selectors...),
		Recipients: []RecipientSnapshot{recipient},
		Contributions: []ResolvedAudienceContribution{{
			SelectorOrdinal: 1, Selector: request.Selectors[0], Recipient: recipient,
			Required:     request.Selectors[0].Required,
			WakePolicy:   request.Selectors[0].WakePolicy,
			RouteReasons: []RouteReason{"direct"}, CausalKind: CausalDirect,
			CausalRef: recipient.Recipient.Ref,
		}},
		RosterHash: rosterHash, ObservedAt: request.RequestedAt,
		FreshUntil: request.RequestedAt.Add(5 * time.Minute),
	}
	requestHash, err := CanonicalPublicationAudienceRequestHash(request)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	snapshotHash, err := CanonicalPublicationAudienceSnapshotHash(snapshot)
	if err != nil {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, err
	}
	return snapshot, PublicationAudienceAttestation{
		Scope: request.Scope, DirectoryEpoch: a.epoch,
		RequestHash: requestHash, SnapshotHash: snapshotHash,
		ObservedAt: snapshot.ObservedAt, FreshUntil: snapshot.FreshUntil,
		Evidence: AuthorityEvidence{Verdict: VerdictClean, Code: "audience_attested",
			EvidenceRef: "direct_notice_audience"},
	}, nil
}

type directNoticeGrantClosureResolver struct {
	epoch          int64
	now            time.Time
	principal      CommunicationPrincipal
	subjectsByUser map[model.ID][]CommunicationSubjectRef
	beforeReturn   func()
	calls          atomic.Int64
}

type directNoticeAliasingGrantClosureResolver struct {
	inner    ChannelGrantSubjectClosureResolver
	returned []ChannelGrantSubjectClosure
}

func (r *directNoticeAliasingGrantClosureResolver) ResolveChannelGrantSubjects(
	ctx context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	closure, err := r.inner.ResolveChannelGrantSubjects(ctx, scope, principal)
	r.returned = append(r.returned, closure)
	return closure, err
}

func (r *directNoticeGrantClosureResolver) ResolveChannelGrantSubjects(
	_ context.Context,
	scope DirectoryScopeRef,
	principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	r.calls.Add(1)
	subjects := []CommunicationSubjectRef{{Kind: SubjectUser, Ref: principal.UserID.String()}}
	if configured, ok := r.subjectsByUser[principal.UserID]; ok {
		subjects = append([]CommunicationSubjectRef(nil), configured...)
	}
	if r.beforeReturn != nil {
		r.beforeReturn()
	}
	return ChannelGrantSubjectClosure{
		Scope: scope, Principal: principal, DirectoryEpoch: r.epoch,
		Outcome: ReadAllow, Code: "subjects_resolved",
		Subjects:   subjects,
		ObservedAt: r.now, FreshUntil: r.now.Add(5 * time.Minute),
		EvidenceRef: "direct_notice_subjects",
	}, nil
}

type directNoticeOperationAuthorizer struct {
	now     time.Time
	calls   atomic.Int64
	facts   []store.AuthorizationFactRef
	outcome ReadOutcome
	fail    atomic.Bool
	entity  *EntityRef
}

func (a *directNoticeOperationAuthorizer) AuthorizeEntityOperation(
	_ context.Context,
	principal CommunicationPrincipal,
	entity EntityRef,
	operation CommunicationOperation,
) (ReadWitness, error) {
	a.calls.Add(1)
	if a.fail.Load() {
		return ReadWitness{}, errors.New("operation authorizer unavailable")
	}
	if a.entity != nil {
		entity = *a.entity
	}
	clean := func(code string) AuthorityEvidence {
		return AuthorityEvidence{Verdict: VerdictClean, Code: code, EvidenceRef: "direct_notice_" + code}
	}
	outcome := a.outcome
	if outcome == "" {
		outcome = ReadAllow
	}
	corePermission := clean("core_permission")
	switch outcome {
	case ReadDeny:
		corePermission = AuthorityEvidence{
			Verdict: VerdictBroken, Code: "core_permission_denied", EvidenceRef: "direct_notice_core_permission",
		}
	case ReadUnknown:
		corePermission = AuthorityEvidence{
			Verdict: VerdictUnknown, Code: "core_permission_unknown", EvidenceRef: "direct_notice_core_permission",
		}
	}
	return ReadWitness{
		Outcome: outcome, Code: "message_send_decided", Entity: entity,
		Operation: operation, Principal: principal, ObservedAt: a.now,
		FreshUntil:     a.now.Add(5 * time.Minute),
		CorePermission: corePermission, ResourceGuard: clean("resource_guard"),
		ForbidAbsence: clean("forbid_absence"), EvidenceRef: "direct_notice_authority",
		Facts: append([]store.AuthorizationFactRef(nil), a.facts...),
	}, nil
}

type directNoticeFixture struct {
	communicationSchemaFixture
	scope      DirectoryScopeRef
	now        time.Time
	channel    Channel
	sender     model.ID
	recipient  model.ID
	epoch      int64
	attestor   *directNoticeAudienceAttestor
	closure    *directNoticeGrantClosureResolver
	authorizer *directNoticeOperationAuthorizer
	authr      *auth.Authenticator
	ref        auth.PrincipalRef
	source     *communicationAuthoritySourceRecorder
	authUser   auth.Principal
}

type directNoticeAuthorityTrace struct {
	steps          []string
	authorityFacts [][]store.AuthorizationFactRef
	nowCalls       int
}

type directNoticeAuthorityTraceData struct {
	inner api.ModuleData
	trace *directNoticeAuthorityTrace
}

func (d *directNoticeAuthorityTraceData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeAuthorityTraceData) Mutate(
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
			return errors.New("direct notice authority trace scope lacks transaction capabilities")
		}
		return fn(&directNoticeAuthorityTraceScope{
			Scope: sc, clock: clock, locker: locker, authority: authority,
			directory: directory, trace: d.trace,
		})
	})
}

type directNoticeAuthorityTraceScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	trace     *directNoticeAuthorityTrace
}

func (s *directNoticeAuthorityTraceScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	s.trace.nowCalls++
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeAuthorityTraceScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	s.trace.steps = append(s.trace.steps, "transaction:"+key)
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeAuthorityTraceScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	s.trace.steps = append(s.trace.steps, "authority")
	s.trace.authorityFacts = append(
		s.trace.authorityFacts,
		append([]store.AuthorizationFactRef(nil), facts...),
	)
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *directNoticeAuthorityTraceScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeAuthorityTraceScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

type directNoticeAfterViewData struct {
	inner api.ModuleData
	after func(context.Context) error
}

type directNoticeDataTrace struct {
	inner api.ModuleData
	steps *[]string
}

type directNoticeFinalExpiryData struct {
	inner api.ModuleData
	final model.Timestamp
	calls atomic.Int64
}

func (d *directNoticeFinalExpiryData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeFinalExpiryData) Mutate(
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
			return errors.New("direct notice final-expiry scope lacks transaction capabilities")
		}
		return fn(&directNoticeFinalExpiryScope{
			Scope: sc, rawClock: clock, locker: locker, authority: authority,
			directory: directory, data: d,
		})
	})
}

type directNoticeFinalExpiryScope struct {
	store.Scope
	rawClock  store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	data      *directNoticeFinalExpiryData
}

func (s *directNoticeFinalExpiryScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	raw, err := s.rawClock.TransactionNow(ctx)
	if err != nil {
		return model.Timestamp{}, err
	}
	if s.data.calls.Add(1) >= 3 {
		return s.data.final, nil
	}
	return raw, nil
}

func (s *directNoticeFinalExpiryScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeFinalExpiryScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *directNoticeFinalExpiryScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeFinalExpiryScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (d *directNoticeDataTrace) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	*d.steps = append(*d.steps, "view")
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeDataTrace) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	*d.steps = append(*d.steps, "mutate")
	return d.inner.Mutate(ctx, tenant, fn)
}

func (d *directNoticeAfterViewData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	if err := d.inner.View(ctx, tenant, fn); err != nil {
		return err
	}
	after := d.after
	d.after = nil
	if after != nil {
		return after(ctx)
	}
	return nil
}

func (d *directNoticeAfterViewData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, fn)
}

type directNoticeReplayRepository struct {
	rows []model.Record
}

type directNoticeReplayDirectoryReader struct {
	epoch   model.DirectoryEpoch
	witness store.DirectoryTombstoneWitness
	found   bool
	err     error
}

func (r directNoticeReplayDirectoryReader) ReadDirectoryEpoch(
	context.Context,
) (model.DirectoryEpoch, error) {
	return r.epoch, r.err
}

func (r directNoticeReplayDirectoryReader) ReadDirectoryTombstone(
	context.Context,
	store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return r.witness, r.found, r.err
}

type directNoticeGrantPage struct {
	rows []model.Record
	page model.Page
}

type directNoticePagedGrantLister struct {
	pages map[string]directNoticeGrantPage
}

func (l directNoticePagedGrantLister) List(
	_ context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	if query.Cursor != "" && len(query.Sort) != 0 {
		return nil, model.Page{}, errors.New("cursor and explicit sort cannot be combined")
	}
	page, ok := l.pages[query.Cursor]
	if !ok {
		return nil, model.Page{}, errors.New("unexpected pagination cursor")
	}
	return page.rows, page.page, nil
}

type directNoticeWorkspaceMarkedData struct {
	inner     api.ModuleData
	workspace model.ID
}

func (d directNoticeWorkspaceMarkedData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, d.workspace)
		if err != nil {
			return err
		}
		return fn(confined)
	})
}

func (d directNoticeWorkspaceMarkedData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, d.workspace)
		if err != nil {
			return err
		}
		return fn(confined)
	})
}

type directNoticeLockCountingRepository struct {
	communicationRepository
	locks *atomic.Int64
}

func directNoticeFailingWriteRepository(
	base communicationRepository,
	failCreate bool,
	failCreateWithID bool,
	failure error,
) communicationRepository {
	return &communicationRepositoryAdapter{
		descriptor: base.Descriptor,
		get:        base.Get,
		list:       base.List,
		lock:       base.Lock,
		create: func(ctx context.Context, record model.Record) (model.Record, error) {
			if failCreate {
				return nil, failure
			}
			return base.CreateAtTransactionTime(ctx, record)
		},
		createWithID: func(
			ctx context.Context,
			id model.ID,
			record model.Record,
		) (model.Record, error) {
			if failCreateWithID {
				return nil, failure
			}
			return base.CreateWithIDAtTransactionTime(ctx, id, record)
		},
		update: base.UpdateAtTransactionTime,
	}
}

func (r directNoticeLockCountingRepository) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	r.locks.Add(1)
	return r.communicationRepository.Lock(ctx, id)
}

type directNoticeReplayAuditLog struct {
	events    []model.AuditEvent
	meta      string
	err       error
	anchorErr error
}

func (l directNoticeReplayAuditLog) Append(
	context.Context,
	model.AuditDraft,
) (model.AuditEvent, error) {
	return model.AuditEvent{}, errors.New("read-only replay audit")
}

func (l directNoticeReplayAuditLog) Verify(context.Context, int64) (store.VerifyReport, error) {
	return store.VerifyReport{}, errors.New("unsupported replay verify")
}

func (l directNoticeReplayAuditLog) Walk(
	_ context.Context,
	from int64,
	fn func(model.AuditEvent) error,
) error {
	if l.err != nil {
		return l.err
	}
	for _, event := range l.events {
		if event.Seq >= from {
			if err := fn(event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l directNoticeReplayAuditLog) Head(context.Context) (store.HeadRef, bool, error) {
	return store.HeadRef{}, false, errors.New("unsupported replay head")
}

func (l directNoticeReplayAuditLog) ReadVerifiedAuditAnchor(
	_ context.Context,
	sequence int64,
) (model.AuditEvent, string, bool, error) {
	if l.anchorErr != nil {
		return model.AuditEvent{}, "", false, l.anchorErr
	}
	for _, event := range l.events {
		if event.Seq == sequence {
			return event, l.meta, true, nil
		}
	}
	return model.AuditEvent{}, "", false, nil
}

func (r directNoticeReplayRepository) Get(_ context.Context, id model.ID) (model.Record, error) {
	for _, row := range r.rows {
		if row.String(model.ColID) == id.String() {
			return workSchemaClone(row), nil
		}
	}
	return nil, store.ErrNotFound
}

func (r directNoticeReplayRepository) List(
	_ context.Context,
	_ model.Query,
) ([]model.Record, model.Page, error) {
	rows := make([]model.Record, 0, len(r.rows))
	for _, row := range r.rows {
		rows = append(rows, workSchemaClone(row))
	}
	return rows, model.Page{}, nil
}

func newDirectNoticeFixture(t *testing.T) directNoticeFixture {
	return newDirectNoticeFixtureWithOptions(t, AckPolicyNone, 0)
}

func newDirectNoticeFixtureWithOptions(
	t *testing.T,
	ackPolicy AckPolicy,
	routeGuardAhead int64,
) directNoticeFixture {
	return newDirectNoticeFixtureWithGrantOptions(t, ackPolicy, routeGuardAhead, true, true)
}

func newDirectNoticeFixtureWithGrantOptions(
	t *testing.T,
	ackPolicy AckPolicy,
	routeGuardAhead int64,
	senderCanWrite bool,
	recipientCanRead bool,
) directNoticeFixture {
	t.Helper()
	return newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "sqlite-direct-notice", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "direct-notice.db"),
	}, ackPolicy, routeGuardAhead, senderCanWrite, recipientCanRead, false)
}

func newDirectNoticeExactAuthorityFixture(t *testing.T) directNoticeFixture {
	t.Helper()
	return newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "sqlite-direct-notice-authority", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "direct-notice-authority.db"),
	}, AckPolicyNone, 0, true, true, true)
}

func newDirectNoticeFixtureForBackend(
	t *testing.T,
	backend communicationSchemaBackend,
	ackPolicy AckPolicy,
	routeGuardAhead int64,
	senderCanWrite bool,
	recipientCanRead bool,
	exactAuthority bool,
) directNoticeFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	clock := &testClock{now: now}
	fixture := communicationOpenFixtureWithClock(t, backend, clock)
	fixture.m.clock = clock
	scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
	var (
		authr     *auth.Authenticator
		ref       auth.PrincipalRef
		authToken string
		authUser  auth.Principal
		sender    = model.NewID()
		err       error
	)
	if exactAuthority {
		authr = auth.NewAuthenticator(fixture.st, clock)
		_, _, err = authr.BootstrapSuperadminOwning(
			ctx, "root@direct-notice.test", "direct-notice-root-password", fixture.tenant,
		)
		if err != nil {
			t.Fatalf("bootstrap direct notice root: %v", err)
		}
		rootToken, _, loginErr := authr.Login(
			ctx, "root@direct-notice.test", "direct-notice-root-password", "127.0.0.1",
		)
		if loginErr != nil {
			t.Fatalf("login direct notice root: %v", loginErr)
		}
		root, authErr := authr.Authenticate(ctx, rootToken)
		if authErr != nil {
			t.Fatalf("authenticate direct notice root: %v", authErr)
		}
		onboarded, onboardErr := authr.OnboardMember(ctx, root, fixture.tenant, auth.OnboardInput{
			Email: "sender@direct-notice.test", DisplayName: "Direct notice sender",
			Role: auth.RoleOwner, Password: "direct-notice-password",
		})
		if onboardErr != nil {
			t.Fatalf("onboard direct notice sender: %v", onboardErr)
		}
		sender = onboarded.User.ID
		authToken, _, loginErr = authr.Login(
			ctx, "sender@direct-notice.test", "direct-notice-password", "127.0.0.2",
		)
		if loginErr != nil {
			t.Fatalf("login direct notice sender: %v", loginErr)
		}
		resolvedSender, authErr := authr.Authenticate(ctx, authToken)
		if authErr != nil {
			t.Fatalf("authenticate direct notice sender: %v", authErr)
		}
		var ok bool
		ref, ok = resolvedSender.Ref()
		if !ok {
			t.Fatal("authenticated direct notice sender has no opaque ref")
		}
		authUser = resolvedSender
	}

	channelID := model.NewID()
	channelInput := communicationChannelRecord(fixture.workspace, "direct-notice")
	channelInput[colCommDefaultAckPolicy] = string(ackPolicy)
	if ackPolicy == AckPolicyNone {
		channelInput[colCommDefaultAckTimeoutMS] = int64(0)
	} else {
		channelInput[colCommDefaultAckTimeoutMS] = int64(60_000)
	}
	channelRecord, err := communicationCreateWithID(
		ctx, fixture.m, fixture.tenant, channelKind, channelID,
		channelInput,
	)
	if err != nil {
		t.Fatalf("create direct notice Channel: %v", err)
	}
	channel, err := channelFromRecord(channelRecord)
	if err != nil {
		t.Fatalf("decode direct notice Channel: %v", err)
	}
	recipient := model.NewID()
	baseEntity := func(id model.ID) MutableCommunicationEntity {
		return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
			ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
			Version: 1, CreatedAt: now,
		}, UpdatedAt: now}
	}
	createGrant := func(subject model.ID, canRead, canWrite bool) {
		t.Helper()
		id := model.NewID()
		grant := ChannelGrant{
			MutableCommunicationEntity: baseEntity(id), ChannelID: channel.ID,
			Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: subject.String()},
			Generation: 1, CanRead: canRead, CanWrite: canWrite,
			State:     ChannelGrantActive,
			GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: sender.String()},
		}
		record, encodeErr := channelGrantToRecord(grant)
		if encodeErr != nil {
			t.Fatalf("encode ChannelGrant: %v", encodeErr)
		}
		if _, createErr := communicationCreateWithID(
			ctx, fixture.m, fixture.tenant, channelGrantKind, id, record,
		); createErr != nil {
			t.Fatalf("create ChannelGrant: %v", createErr)
		}
	}
	createGrant(sender, true, senderCanWrite)
	if recipientCanRead {
		createGrant(recipient, true, false)
	}
	// Workspace bootstrap already created the exactly-two guard set. Reconcile
	// after the fixture's direct Channel insert so route_revision catches up to
	// max(Channel.RouteRevision)+1 instead of creating a duplicate seed.
	if routeGuardAhead >= 0 {
		if err := fixture.m.ReconcileCommunicationGuards(
			ctx, fixture.tenant, CommunicationGuardReconcileStaged,
		); err != nil {
			t.Fatalf("reconcile direct notice guards: %v", err)
		}
	}
	if routeGuardAhead > 0 {
		if err := fixture.m.data.Mutate(ctx, fixture.tenant, func(raw store.Scope) error {
			confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
			if err != nil {
				return err
			}
			clock, ok := confined.(store.TransactionClock)
			if !ok {
				return errors.New("direct notice guard scope lacks transaction clock")
			}
			dbNow, err := clock.TransactionNow(ctx)
			if err != nil {
				return err
			}
			repo, err := confined.Ext(communicationGuardKind)
			if err != nil {
				return err
			}
			stamped, ok := repo.(store.TransactionStampedGenericRepo)
			if !ok {
				return errors.New("direct notice guard repository lacks stamped writes")
			}
			rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{
				Column: colCommGuardKind, Op: model.OpEq,
				Value: string(CommunicationGuardRouteRevision),
			}}, Limit: 2})
			if err != nil || len(rows) != 1 {
				return fmt.Errorf("read seeded route guard: rows=%d: %w", len(rows), err)
			}
			guard, err := communicationGuardFromRecord(rows[0])
			if err != nil {
				return err
			}
			beforeVersion := guard.Version
			guard.Version++
			guard.NextSeq += routeGuardAhead
			guard.UpdatedAt = dbNow.Time()
			guard.LastDBTime = dbNow.Time()
			record, err := communicationGuardToRecord(guard)
			if err != nil {
				return err
			}
			record[model.ColVersion] = beforeVersion
			_, err = stamped.UpdateAtTransactionTime(ctx, record)
			return err
		}); err != nil {
			t.Fatalf("adjust direct notice route guard fixture: %v", err)
		}
	}
	var (
		epoch             int64
		authorizationFact store.AuthorizationFactRef
	)
	if err := fixture.m.viewCommunication(ctx, scope, func(sc store.Scope) error {
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("directory reader unavailable")
		}
		current, readErr := reader.ReadDirectoryEpoch(ctx)
		if readErr != nil {
			return readErr
		}
		epoch = current.Version
		if exactAuthority {
			authorization, ok := sc.(store.AuthorizationEpochReader)
			if !ok {
				return errors.New("authorization epoch reader unavailable")
			}
			authorizationFact, readErr = authorization.ReadAuthorizationEpoch(ctx)
		}
		return readErr
	}); err != nil {
		t.Fatalf("read direct notice authority epochs: %v", err)
	}
	principal := CommunicationPrincipal{UserID: sender}
	attestor := &directNoticeAudienceAttestor{epoch: epoch}
	closure := &directNoticeGrantClosureResolver{epoch: epoch, now: now, principal: principal}
	authorizer := &directNoticeOperationAuthorizer{now: now, facts: []store.AuthorizationFactRef{{
		Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: epoch,
	}}}
	fixture.m.communicationAudienceAttestor = attestor
	fixture.m.communicationGrantClosure = closure
	fixture.m.communicationOperationAuthorizer = authorizer
	var authoritySource *communicationAuthoritySourceRecorder
	if exactAuthority {
		authoritySource = &communicationAuthoritySourceRecorder{evidence: auth.AuthorizationEvidence{
			Outcome: auth.EvidenceAllow,
			CorePermission: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "rbac_permitted",
			},
			ResourceGuard: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "resource_guard_clean",
			},
			ForbidAbsence: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "forbid_absence_clean",
			},
			Facts: []store.AuthorizationFactRef{
				authorizationFact,
				{Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: epoch},
			},
			ObservedAt: now,
			FreshUntil: now.Add(5 * time.Minute),
		}}
		fixture.m.useCommunicationRequestAuthoritySources(authr, authoritySource)
	}
	return directNoticeFixture{
		communicationSchemaFixture: fixture, scope: scope, now: now, channel: channel,
		sender: sender, recipient: recipient, epoch: epoch,
		attestor: attestor, closure: closure, authorizer: authorizer,
		authr: authr, ref: ref, source: authoritySource, authUser: authUser,
	}
}

func (f directNoticeFixture) command(idempotency model.ID, canary string) DirectNoticePublishCommand {
	return DirectNoticePublishCommand{
		ChannelID: f.channel.ID,
		Recipient: RecipientRef{Kind: RecipientUser, Ref: f.recipient.String()},
		Content: MessageContent{Subject: "Direct notice", Blocks: []MessageContentBlock{{
			Type: ContentBlockText, Format: TextPlain, Text: canary,
		}}},
		IdempotencyKey: idempotency.String(),
	}
}

func createDirectNoticeGrantForTest(
	t *testing.T,
	fixture directNoticeFixture,
	subject CommunicationSubjectRef,
	canRead bool,
	canWrite bool,
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
		ChannelID: fixture.channel.ID, Subject: subject, Generation: 1,
		CanRead: canRead, CanWrite: canWrite, State: ChannelGrantActive,
		GrantedBy: CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
	}
	record, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode direct notice ChannelGrant: %v", err)
	}
	if _, err := communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant, channelGrantKind, id, record,
	); err != nil {
		t.Fatalf("create direct notice ChannelGrant: %v", err)
	}
	return id
}

func communicationRowsForTest(
	t *testing.T,
	fixture directNoticeFixture,
	kind model.Kind,
) []model.Record {
	t.Helper()
	var rows []model.Record
	err := fixture.m.viewCommunication(context.Background(), fixture.scope, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{
			Sort: []model.Sort{{Column: model.ColID}}, Limit: 200,
		})
		return err
	})
	if err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	return rows
}

func TestPublishDirectNoticePersistsAcrossBackends(t *testing.T) {
	t.Parallel()

	for _, backend := range communicationSchemaBackends(t) {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			fixture := newDirectNoticeFixtureForBackend(
				t, backend, AckPolicyNone, 0, true, true, false,
			)
			command := fixture.command(model.NewID(), "cross-backend direct notice")
			result, err := fixture.m.publishDirectNotice(
				context.Background(), fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender},
				command,
			)
			if err != nil || result.State != MessagePublished || result.Version != 2 ||
				result.DeliveryCount != 1 || result.EventID == "" || result.CommandID == "" {
				t.Fatalf("publish result = %+v, %v", result, err)
			}
			replayed, err := fixture.m.publishDirectNotice(
				context.Background(), fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender}, command,
			)
			if err != nil || !replayed.Replayed || replayed.CommandID != result.CommandID ||
				replayed.MessageID != result.MessageID || replayed.EventID != result.EventID {
				t.Fatalf("replay result = %+v, %v; original = %+v", replayed, err, result)
			}
			for kind, want := range map[model.Kind]int{
				messageKind: 1, messageAudienceKind: 1, messageAudienceRecipientKind: 1,
				messageDeliveryKind: 1, workEventKind: 1, workOutboxKind: 1,
				communicationCommandKind: 1,
			} {
				if got := len(communicationRowsForTest(t, fixture, kind)); got != want {
					t.Fatalf("%s rows = %d, want %d", kind, got, want)
				}
			}
		})
	}
}

func TestPublishDirectNoticePersistsAtomicEventOutboxAndReceipt(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	canary := "CANARY-DIRECT-NOTICE-BODY"
	cmd := fixture.command(model.NewID(), canary)

	result, err := fixture.m.publishDirectNotice(context.Background(), fixture.scope, principal, cmd)
	if err != nil {
		t.Fatalf("publish direct notice: %v", err)
	}
	if result.Verdict != VerdictClean || result.Code != "accepted" || result.State != MessagePublished ||
		result.Version != 2 || result.DeliveryCount != 1 || result.RequiredCount != 0 ||
		result.AckQuorum != 0 || result.CommandID == "" || result.MessageID == "" ||
		result.DeliveryID == "" || result.EventID == "" || len(result.PlanHash) != 64 ||
		len(result.AudienceHash) != 64 || len(result.PayloadDigest) != 64 || result.AuditSeq < 1 {
		t.Fatalf("direct notice result = %+v", result)
	}
	if result.Fulfillment != (FulfillmentProjection{State: FulfillmentNotRequired}) {
		t.Fatalf("initial fulfillment = %+v, want not_required zero vector", result.Fulfillment)
	}
	for kind, want := range map[model.Kind]int{
		messageKind: 1, messageAudienceKind: 1, messageAudienceRecipientKind: 1,
		messageDeliveryKind: 1, workEventKind: 1, workOutboxKind: 1,
		communicationCommandKind: 1,
	} {
		if got := len(communicationRowsForTest(t, fixture, kind)); got != want {
			t.Fatalf("%s rows = %d, want %d", kind, got, want)
		}
	}
	messages := communicationRowsForTest(t, fixture, messageKind)
	message, err := messageFromRecord(messages[0], 0)
	if err != nil {
		t.Fatalf("decode published Message: %v", err)
	}
	if message.ID != result.MessageID || message.Version != 2 || message.State != MessagePublished ||
		message.LastEventSeq != 1 || !message.CreatedAt.Equal(message.UpdatedAt) ||
		message.PublishedAt == nil || !message.PublishedAt.Equal(message.CreatedAt) ||
		!bytes.Equal(message.AudienceHash, mustDecodeHex(t, result.AudienceHash)) ||
		!bytes.Equal(message.Payload.Digest, mustDecodeHex(t, result.PayloadDigest)) ||
		!strings.Contains(string(message.Payload.PlainJSON), canary) {
		t.Fatalf("persisted Message = %+v", message)
	}
	deliveries := communicationRowsForTest(t, fixture, messageDeliveryKind)
	delivery, err := messageDeliveryFromRecord(deliveries[0])
	if err != nil {
		t.Fatalf("decode published Delivery: %v", err)
	}
	if delivery.ID != result.DeliveryID || delivery.MessageID != result.MessageID ||
		delivery.DeliverySeq != 1 || delivery.State != DeliveryAvailable ||
		delivery.Recipient.Ref != fixture.recipient.String() ||
		!delivery.AvailableAt.Equal(message.CreatedAt) {
		t.Fatalf("persisted Delivery = %+v", delivery)
	}
	guards := communicationRowsForTest(t, fixture, communicationGuardKind)
	for _, row := range guards {
		guard, decodeErr := communicationGuardFromRecord(row)
		if decodeErr != nil {
			t.Fatalf("decode guard: %v", decodeErr)
		}
		if guard.Kind == CommunicationGuardDeliverySequence && (guard.NextSeq != 2 || guard.Version != 2) {
			t.Fatalf("delivery guard = %+v, want next=2/version=2", guard)
		}
		if guard.Kind == CommunicationGuardRouteRevision && (guard.NextSeq != 2 || guard.Version != 2) {
			t.Fatalf("route guard = %+v, want next=2/version=2", guard)
		}
	}
	event := communicationRowsForTest(t, fixture, workEventKind)[0]
	expectedEventPayload, err := canonicalDirectNoticeEventPayload(result)
	if err != nil {
		t.Fatalf("canonical direct notice event payload: %v", err)
	}
	if event.String(colEventAggregateKind) != string(messageKind) ||
		event.String(colEventAggregateID) != result.MessageID.String() ||
		event.Int(colEventSeq) != 1 || event.String(colEventType) != communicationMessageAvailable ||
		strings.Contains(event.String(colEventPayload), canary) ||
		strings.Contains(event.String(colEventPayload), fixture.recipient.String()) ||
		event.String(colEventPayload) != string(expectedEventPayload) ||
		!bytes.Equal(event.Bytes(colEventPayloadHash), hashBytes([]byte(event.String(colEventPayload)))) {
		t.Fatalf("persisted Event leaks or loses lineage: %#v", event)
	}
	receiptRows := communicationRowsForTest(t, fixture, communicationCommandKind)
	receipt, err := communicationCommandReceiptFromRecord(receiptRows[0])
	if err != nil {
		t.Fatalf("decode direct notice receipt: %v", err)
	}
	if receipt.CommandID != result.CommandID || receipt.ResultID != result.MessageID ||
		receipt.EventID != result.EventID || receipt.AuditSeq != result.AuditSeq ||
		!receipt.CompletedAt.Equal(message.CreatedAt) || receipt.HTTPStatus != 202 {
		t.Fatalf("persisted receipt = %+v", receipt)
	}
	audit := directNoticeAuditEvent(t, fixture, result.AuditSeq)
	if audit.TargetKind != communicationCommandKind || audit.TargetID != receipt.CommandID ||
		audit.Action != directNoticePublishAuditAction ||
		!bytes.Equal(audit.PayloadHash, receipt.PlanHash) {
		t.Fatalf("audit command anchor = %+v, receipt command=%s", audit, receipt.CommandID)
	}
	var auditMeta string
	if err := fixture.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return errors.New("verified audit anchor reader unavailable")
		}
		_, meta, found, readErr := reader.ReadVerifiedAuditAnchor(
			context.Background(), result.AuditSeq,
		)
		if readErr != nil {
			return readErr
		}
		if !found {
			return errors.New("direct notice audit anchor missing")
		}
		auditMeta = meta
		return nil
	}); err != nil {
		t.Fatalf("read direct notice audit metadata: %v", err)
	}
	outboxBytes, err := canonicalJSON(communicationRowsForTest(t, fixture, workOutboxKind)[0])
	if err != nil {
		t.Fatalf("encode direct notice Outbox for privacy assertion: %v", err)
	}
	receiptBytes, err := canonicalJSON(receiptRows[0])
	if err != nil {
		t.Fatalf("encode direct notice receipt for privacy assertion: %v", err)
	}
	for label, payload := range map[string][]byte{
		"Outbox": outboxBytes, "receipt": receiptBytes, "audit metadata": []byte(auditMeta),
	} {
		if bytes.Contains(payload, []byte(canary)) ||
			bytes.Contains(payload, []byte(fixture.recipient.String())) {
			t.Fatalf("%s leaks protected content or recipient: %s", label, payload)
		}
	}
	if fixture.attestor.calls.Load() != 1 || fixture.closure.calls.Load() != 2 ||
		fixture.authorizer.calls.Load() != 1 {
		t.Fatalf("preflight calls attestor/closure/authorizer = %d/%d/%d, want 1/2/1",
			fixture.attestor.calls.Load(), fixture.closure.calls.Load(), fixture.authorizer.calls.Load())
	}
}

func TestPublishDirectNoticeAuthorizesSenderBeforeAudienceObservation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fixture    func(*testing.T) directNoticeFixture
		configure  func(*directNoticeFixture)
		wantClosed int64
	}{
		{
			name:      "core deny",
			fixture:   func(t *testing.T) directNoticeFixture { return newDirectNoticeFixture(t) },
			configure: func(f *directNoticeFixture) { f.authorizer.outcome = ReadDeny },
		},
		{
			name:    "wrong core entity",
			fixture: func(t *testing.T) directNoticeFixture { return newDirectNoticeFixture(t) },
			configure: func(f *directNoticeFixture) {
				wrong := EntityRef{TenantID: f.scope.TenantID, WorkspaceID: f.scope.WorkspaceID,
					Kind: channelKind, ID: model.NewID()}
				f.authorizer.entity = &wrong
			},
		},
		{
			name: "no sender write grant",
			fixture: func(t *testing.T) directNoticeFixture {
				return newDirectNoticeFixtureWithGrantOptions(t, AckPolicyNone, 0, false, true)
			},
			configure:  func(*directNoticeFixture) {},
			wantClosed: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.fixture(t)
			test.configure(&fixture)
			beforeAudit := directNoticeAuditHead(t, fixture)
			_, err := fixture.m.publishDirectNotice(
				context.Background(), fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender},
				fixture.command(model.NewID(), "AUTHORIZATION-ORDER-CANARY"),
			)
			if !errors.Is(err, ErrCommunicationForbidden) &&
				!errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("authorization rejection = %v", err)
			}
			if fixture.attestor.calls.Load() != 0 || fixture.closure.calls.Load() != test.wantClosed {
				t.Fatalf("pre-authorization observers attestor/closure = %d/%d, want 0/%d",
					fixture.attestor.calls.Load(), fixture.closure.calls.Load(), test.wantClosed)
			}
			afterAudit := directNoticeAuditHead(t, fixture)
			if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
				t.Fatalf("authorization rejection changed audit: %+v -> %+v", beforeAudit, afterAudit)
			}
			for _, kind := range []model.Kind{
				messageKind, messageAudienceKind, messageAudienceRecipientKind,
				messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
			} {
				if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
					t.Fatalf("authorization rejection persisted %d %s rows", len(rows), kind)
				}
			}
		})
	}
}

func TestPublishDirectNoticeAcceptsRecipientReadThroughCurrentGroupClosure(t *testing.T) {
	t.Parallel()

	groupID := model.NewID()
	groupSubject := CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: groupID.String()}

	allowed := newDirectNoticeFixtureWithGrantOptions(t, AckPolicyNone, 0, true, false)
	createDirectNoticeGrantForTest(t, allowed, groupSubject, true, false)
	allowed.closure.subjectsByUser = map[model.ID][]CommunicationSubjectRef{
		allowed.recipient: {
			{Kind: SubjectUser, Ref: allowed.recipient.String()},
			groupSubject,
		},
	}
	result, err := allowed.m.publishDirectNotice(
		context.Background(), allowed.scope,
		CommunicationPrincipal{UserID: allowed.sender},
		allowed.command(model.NewID(), "group-recipient-read"),
	)
	if err != nil || result.Code != "accepted" {
		t.Fatalf("group-derived recipient read = (%+v, %v), want accepted", result, err)
	}
	if allowed.closure.calls.Load() != 2 {
		t.Fatalf("subject closure calls = %d, want sender+recipient", allowed.closure.calls.Load())
	}

	denied := newDirectNoticeFixtureWithGrantOptions(t, AckPolicyNone, 0, true, false)
	createDirectNoticeGrantForTest(t, denied, groupSubject, true, false)
	_, err = denied.m.publishDirectNotice(
		context.Background(), denied.scope,
		CommunicationPrincipal{UserID: denied.sender},
		denied.command(model.NewID(), "group-recipient-read-denied"),
	)
	if !errors.Is(err, ErrCommunicationForbidden) {
		t.Fatalf("group grant outside recipient closure error = %v, want forbidden", err)
	}
	if rows := communicationRowsForTest(t, denied, messageKind); len(rows) != 0 {
		t.Fatalf("denied group recipient persisted %d Messages", len(rows))
	}
}

func TestPublishDirectNoticeExactReplayConfirmsWhilePublicRemainsOff(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd := fixture.command(model.NewID(), "replay body")
	first, err := fixture.m.publishDirectNotice(context.Background(), fixture.scope, principal, cmd)
	if err != nil {
		t.Fatalf("first direct notice publish: %v", err)
	}
	beforeGuard := directNoticeDeliveryGuard(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture)

	fixture.attestor.fail.Store(true)
	fixture.m.communicationGrantClosure = nil
	fixture.m.communicationOperationAuthorizer = nil
	exactCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	replay, err := fixture.m.publishDirectNoticeWithAuthority(
		exactCtx, fixture.scope, fixture.ref, cmd,
	)
	if err != nil {
		t.Fatalf("bound exact replay with composition down: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("exact replay did not report replay metadata")
	}
	first.Replayed, replay.Replayed = false, false
	if first != replay {
		t.Fatalf("replay result = %+v, want %+v", replay, first)
	}
	if got := directNoticeDeliveryGuard(t, fixture); got.Version != beforeGuard.Version ||
		got.NextSeq != beforeGuard.NextSeq {
		t.Fatalf("delivery guard advanced on replay: before=%+v after=%+v", beforeGuard, got)
	}
	afterAudit := directNoticeAuditHead(t, fixture)
	if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatalf("audit head changed on replay: before=%+v after=%+v", beforeAudit, afterAudit)
	}
	if len(communicationRowsForTest(t, fixture, messageKind)) != 1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != 1 {
		t.Fatal("exact replay created duplicate domain rows")
	}
	counting := &countingCommunicationModuleData{inner: fixture.m.data}
	fixture.m.data = counting
	if _, err := fixture.m.PublishDirectNotice(
		exactCtx, fixture.scope, fixture.ref, cmd,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public replay while K3 OFF = %v, want UNKNOWN", err)
	}
	if counting.viewCalls != 0 || counting.mutateCalls != 0 {
		t.Fatalf("public OFF replay opened data View/Mutate = %d/%d, want zero/zero",
			counting.viewCalls, counting.mutateCalls)
	}

	reused := cmd
	reused.Content.Blocks[0].Text = "different body"
	if _, err := fixture.m.publishDirectNoticeWithAuthority(
		exactCtx, fixture.scope, fixture.ref, reused,
	); !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("confirmed exact conflict = %v, want conflict without UNKNOWN", err)
	}
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, reused,
	); !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrCommunicationPlanChanged) {
		t.Fatalf("reused idempotency key error = %v, want conflict distinct from plan change", err)
	}
	beforeView, beforeMutate := counting.viewCalls, counting.mutateCalls
	beforeClosure, beforeAttestor := fixture.closure.calls.Load(), fixture.attestor.calls.Load()
	newCommand := fixture.command(model.NewID(), "new body")
	if _, err := fixture.m.PublishDirectNotice(
		exactCtx, fixture.scope, fixture.ref, newCommand,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("new public command while K3 OFF = %v, want UNKNOWN", err)
	}
	if counting.viewCalls != beforeView || counting.mutateCalls != beforeMutate ||
		fixture.closure.calls.Load() != beforeClosure || fixture.attestor.calls.Load() != beforeAttestor {
		t.Fatalf("new public OFF command changed View/Mutate/closure/attestor = %d/%d/%d/%d",
			counting.viewCalls-beforeView, counting.mutateCalls-beforeMutate,
			fixture.closure.calls.Load()-beforeClosure,
			fixture.attestor.calls.Load()-beforeAttestor)
	}
}

func TestPublishDirectNoticeExactReplayBindsBeforeViewAndFinalizes(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	cmd := fixture.command(model.NewID(), "ordered exact replay")
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender}, cmd,
	); err != nil {
		t.Fatalf("seed ordered exact replay: %v", err)
	}
	var serviceTrace []string
	resolver := &communicationAuthorityResolverRecorder{
		resolved: fixture.authUser, trace: &serviceTrace,
	}
	fixture.source.trace = &serviceTrace
	fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
	authorityTrace := &directNoticeAuthorityTrace{}
	fixture.m.data = &directNoticeDataTrace{
		inner: &directNoticeAuthorityTraceData{
			inner: fixture.m.data, trace: authorityTrace,
		},
		steps: &serviceTrace,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	replay, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if err != nil || !replay.Replayed {
		t.Fatalf("ordered exact replay = (%+v, %v), want replay", replay, err)
	}
	wantTrace := []string{"resolve", "authorize", "view", "mutate"}
	if !slices.Equal(serviceTrace, wantTrace) {
		t.Fatalf("exact replay service trace = %v, want %v", serviceTrace, wantTrace)
	}
	if len(authorityTrace.authorityFacts) != 1 || len(authorityTrace.steps) != 2 ||
		authorityTrace.steps[0] != "authority" ||
		!strings.HasPrefix(
			authorityTrace.steps[1], "transaction:sessions_communication_command|",
		) || authorityTrace.nowCalls != 3 {
		t.Fatalf("exact replay tx trace = steps %v facts %v now %d, want bound lock+refresh+finalize",
			authorityTrace.steps, authorityTrace.authorityFacts, authorityTrace.nowCalls)
	}
}

func TestPublishDirectNoticeExactAuthorityPersistsWithoutLegacyAuthorizer(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	legacy := fixture.authorizer
	legacy.fail.Store(true)
	trace := &directNoticeAuthorityTrace{}
	fixture.m.data = &directNoticeAuthorityTraceData{inner: fixture.m.data, trace: trace}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := fixture.command(model.NewID(), "exact authority body")

	result, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if err != nil {
		t.Fatalf("publish with exact request authority: %v", err)
	}
	if result.Code != "accepted" || result.Replayed || result.MessageID == "" {
		t.Fatalf("exact authority result = %+v, want new accepted message", result)
	}
	if legacy.calls.Load() != 0 {
		t.Fatalf("legacy operation authorizer calls = %d, want zero", legacy.calls.Load())
	}
	if fixture.source.calls != 1 || len(fixture.source.requests) != 1 {
		t.Fatalf("exact source calls/requests = %d/%d, want one/one",
			fixture.source.calls, len(fixture.source.requests))
	}
	request := fixture.source.requests[0]
	requestRef, ok := request.Principal.Ref()
	if !ok || requestRef != fixture.ref || request.Tenant != fixture.tenant ||
		request.Permission != permMessageSendWrite ||
		request.Resource.Kind != string(channelKind) ||
		request.Resource.ID != fixture.channel.ID.String() ||
		request.Resource.WorkspaceID != fixture.workspace || len(request.Resource.Extra) != 0 {
		t.Fatalf("exact publish authorization request = %#v, ref ok=%t", request, ok)
	}
	if len(communicationRowsForTest(t, fixture, messageKind)) != 1 ||
		len(communicationRowsForTest(t, fixture, communicationCommandKind)) != 1 {
		t.Fatal("exact authority publish did not persist one message and receipt")
	}
	if len(trace.authorityFacts) != 1 || len(trace.steps) < 2 ||
		trace.steps[0] != "authority" ||
		!strings.HasPrefix(trace.steps[1], "transaction:sessions_communication_command|") {
		t.Fatalf("exact publish authority trace = steps %v facts %v; want one authority lock first",
			trace.steps, trace.authorityFacts)
	}
	if trace.nowCalls != 3 {
		t.Fatalf("exact bound publish database-time observations = %d, want initial+refresh+finalize",
			trace.nowCalls)
	}
	foundDirectory, foundAuthorization := false, false
	for _, fact := range trace.authorityFacts[0] {
		switch fact.Kind {
		case model.DirectoryEpochKind:
			foundDirectory = fact.ID == model.ID(fixture.tenant) && fact.Version == fixture.epoch
		case model.AuthorizationEpochKind:
			foundAuthorization = fact.ID == model.ID(fixture.tenant) && fact.Version > 0
		}
	}
	if !foundDirectory || !foundAuthorization {
		t.Fatalf("exact locked facts = %+v, want current DirectoryEpoch+AuthorizationEpoch",
			trace.authorityFacts[0])
	}
}

func TestPublishDirectNoticeExactBindingFailuresDoNotOpenCommunicationData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		configure     func(*directNoticeFixture)
		noDeadline    bool
		zeroRef       bool
		want          error
		sourceCalls   int
		resolverCalls int
	}{
		{
			name: "deny", want: ErrCommunicationForbidden, sourceCalls: 1, resolverCalls: 1,
			configure: func(f *directNoticeFixture) {
				f.source.evidence = auth.AuthorizationEvidence{
					Outcome: auth.EvidenceDeny,
					CorePermission: auth.CheckEvidence{
						Verdict: auth.CheckBroken, Code: "core_permission_denied",
					},
					ResourceGuard: auth.CheckEvidence{
						Verdict: auth.CheckUnknown, Code: "not_evaluated",
					},
					ForbidAbsence: auth.CheckEvidence{
						Verdict: auth.CheckUnknown, Code: "not_evaluated",
					},
				}
			},
		},
		{
			name: "unknown", want: ErrCommunicationEvidenceUnknown, sourceCalls: 1, resolverCalls: 1,
			configure: func(f *directNoticeFixture) {
				unknown := auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"}
				f.source.evidence = auth.AuthorizationEvidence{
					Outcome: auth.EvidenceUnknown, CorePermission: unknown,
					ResourceGuard: unknown, ForbidAbsence: unknown,
				}
			},
		},
		{
			name: "finite_deadline_required", noDeadline: true,
			want: ErrCommunicationEvidenceUnknown, sourceCalls: 0,
		},
		{
			name: "zero_ref", zeroRef: true,
			want: ErrCommunicationEvidenceUnknown, sourceCalls: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAuthorityFixture(t)
			if test.configure != nil {
				test.configure(&fixture)
			}
			resolver := &communicationAuthorityResolverRecorder{resolved: fixture.authUser}
			fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
			counting := &countingCommunicationModuleData{inner: fixture.m.data}
			fixture.m.data = counting
			beforeClosure := fixture.closure.calls.Load()
			beforeAttestor := fixture.attestor.calls.Load()
			ctx := context.Background()
			if !test.noDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
			}
			ref := fixture.ref
			if test.zeroRef {
				ref = auth.PrincipalRef{}
			}
			_, err := fixture.m.publishDirectNoticeWithAuthority(
				ctx, fixture.scope, ref,
				fixture.command(model.NewID(), "authority binding failure"),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("exact binding error = %v, want %v", err, test.want)
			}
			if counting.viewCalls != 0 || counting.mutateCalls != 0 ||
				fixture.authorizer.calls.Load() != 0 || fixture.source.calls != test.sourceCalls ||
				resolver.calls != test.resolverCalls || fixture.closure.calls.Load() != beforeClosure ||
				fixture.attestor.calls.Load() != beforeAttestor {
				t.Fatalf("failed binding opened view/mutate/legacy/source/resolver/closure/attestor = %d/%d/%d/%d/%d/%d/%d, want 0/0/0/%d/%d/0/0",
					counting.viewCalls, counting.mutateCalls, fixture.authorizer.calls.Load(),
					fixture.source.calls, resolver.calls,
					fixture.closure.calls.Load()-beforeClosure,
					fixture.attestor.calls.Load()-beforeAttestor,
					test.sourceCalls, test.resolverCalls)
			}
		})
	}
}

func TestCommunicationRequestAuthorityRejectsForgedInspectionBeforePreflight(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	question, err := newCommunicationAuthorityQuestion(
		fixture.scope, channelKind, fixture.channel.ID, CommunicationMessageSend,
	)
	if err != nil {
		t.Fatalf("new forged-inspection question: %v", err)
	}
	counting := &countingCommunicationModuleData{inner: fixture.m.data}
	fixture.m.data = counting
	for _, test := range []struct {
		name       string
		inspection communicationRequestAuthorityInspection
	}{
		{
			name: "missing_binding_identity",
			inspection: communicationRequestAuthorityInspection{
				question: question, principal: CommunicationPrincipal{UserID: fixture.sender},
			},
		},
		{
			name: "crossed_question",
			inspection: communicationRequestAuthorityInspection{
				question:  communicationAuthorityQuestion{},
				principal: CommunicationPrincipal{UserID: fixture.sender},
				bindingID: &communicationRequestAuthorityBindingID{marker: 1},
			},
		},
		{
			name: "malformed_principal",
			inspection: communicationRequestAuthorityInspection{
				question: question, principal: CommunicationPrincipal{},
				bindingID: &communicationRequestAuthorityBindingID{marker: 1},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := communicationRequestAuthority{access: func(
				communicationRequestAuthorityAccess,
				communicationAuthorityQuestion,
				CommunicationClaimAuthoritySnapshot,
			) (communicationRequestAuthorityAccessResult, error) {
				return communicationRequestAuthorityAccessResult{inspection: test.inspection}, nil
			}}
			if _, err := forged.contextFor(question); !errors.Is(
				err, ErrCommunicationEvidenceUnknown,
			) {
				t.Fatalf("forged inspection error = %v, want UNKNOWN", err)
			}
		})
	}
	if counting.viewCalls != 0 || counting.mutateCalls != 0 ||
		fixture.closure.calls.Load() != 0 || fixture.attestor.calls.Load() != 0 {
		t.Fatalf("forged inspections opened view/mutate/closure/attestor = %d/%d/%d/%d",
			counting.viewCalls, counting.mutateCalls,
			fixture.closure.calls.Load(), fixture.attestor.calls.Load())
	}
}

func TestDirectNoticePublishRequiresUserBackedPrincipalShapeBeforeCommunicationReads(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		principal CommunicationPrincipal
		want      error
	}{
		{name: "human_user", principal: CommunicationPrincipal{UserID: model.NewID()}},
		{name: "agent", principal: CommunicationPrincipal{AgentExternalID: "agent:test"}},
		{name: "communication_session", principal: CommunicationPrincipal{
			SessionID: "osn_" + model.NewID().String(), SessionRunRef: model.NewID().String(),
			SessionFence: 1, SessionWorkspaceID: model.NewID(), PurposeRestricted: true,
		}},
		{name: "system", principal: CommunicationPrincipal{
			System: true, SystemActorRef: "system:test", SystemGrantAgentID: model.NewID(),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requireDirectNoticeUserBackedPrincipal(communicationRequestAuthorityInspection{
				principal: test.principal,
			})
			if test.name == "human_user" {
				if err != nil {
					t.Fatalf("human user rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrCommunicationForbidden) ||
				errors.Is(err, ErrInvalidCommunicationModel) {
				t.Fatalf("unsupported %s error = %v, want Forbidden", test.name, err)
			}
		})
	}
}

func TestPublishDirectNoticeExactRejectsNonUserShapesWithoutCommunicationReads(t *testing.T) {
	t.Parallel()

	t.Run("communication_session", func(t *testing.T) {
		fixture := newDirectNoticeExactAuthorityFixture(t)
		issuer, err := auth.NewSystemOperator(
			"test:direct-notice", "issue unsupported communication session",
		)
		if err != nil {
			t.Fatalf("new communication-session issuer: %v", err)
		}
		credential, err := fixture.authr.IssueCommunicationSessionCredential(
			context.Background(), issuer, auth.CommunicationSessionCredentialSpec{
				Tenant: fixture.tenant, WorkspaceID: fixture.workspace,
				SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(),
				AgentRef: "agent:" + model.NewID().String(), ClaimFence: 1,
			},
		)
		if err != nil {
			t.Fatalf("issue communication-session credential: %v", err)
		}
		principal, err := fixture.authr.Authenticate(context.Background(), credential.Token)
		if err != nil {
			t.Fatalf("authenticate communication-session credential: %v", err)
		}
		ref, ok := principal.Ref()
		if !ok {
			t.Fatal("communication-session credential has no ref")
		}
		counting := &countingCommunicationModuleData{inner: fixture.m.data}
		fixture.m.data = counting
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, err = fixture.m.publishDirectNoticeWithAuthority(
			ctx, fixture.scope, ref,
			fixture.command(model.NewID(), "unsupported communication session"),
		)
		if !errors.Is(err, ErrCommunicationForbidden) ||
			counting.viewCalls != 0 || counting.mutateCalls != 0 ||
			fixture.authorizer.calls.Load() != 0 || fixture.source.calls != 1 {
			t.Fatalf("communication-session publish = %v calls view/mutate/legacy/source %d/%d/%d/%d",
				err, counting.viewCalls, counting.mutateCalls,
				fixture.authorizer.calls.Load(), fixture.source.calls)
		}
	})

	t.Run("agent_token", func(t *testing.T) {
		fixture := newDirectNoticeExactAuthorityFixture(t)
		raw, _, err := fixture.authr.IssueToken(
			context.Background(), fixture.authUser,
			auth.TokenSpec{Name: "direct-notice-agent", BoundTenant: fixture.tenant, Role: auth.RoleOwner},
		)
		if err != nil {
			t.Fatalf("issue agent base token: %v", err)
		}
		principal, err := fixture.authr.Authenticate(context.Background(), raw)
		if err != nil {
			t.Fatalf("authenticate agent base token: %v", err)
		}
		principal = principal.WithAgentIdentity("agent:" + model.NewID().String())
		ref, ok := principal.Ref()
		if !ok {
			t.Fatal("agent token has no ref")
		}
		resolver := &communicationAuthorityResolverRecorder{resolved: principal}
		fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
		counting := &countingCommunicationModuleData{inner: fixture.m.data}
		fixture.m.data = counting
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		_, err = fixture.m.publishDirectNoticeWithAuthority(
			ctx, fixture.scope, ref, fixture.command(model.NewID(), "unsupported agent"),
		)
		if !errors.Is(err, ErrCommunicationForbidden) ||
			counting.viewCalls != 0 || counting.mutateCalls != 0 ||
			fixture.authorizer.calls.Load() != 0 || fixture.source.calls != 1 || resolver.calls != 1 {
			t.Fatalf("agent publish = %v calls view/mutate/legacy/source/resolver %d/%d/%d/%d/%d",
				err, counting.viewCalls, counting.mutateCalls, fixture.authorizer.calls.Load(),
				fixture.source.calls, resolver.calls)
		}
	})

	t.Run("user_backed_token_preserved", func(t *testing.T) {
		fixture := newDirectNoticeExactAuthorityFixture(t)
		raw, _, err := fixture.authr.IssueToken(
			context.Background(), fixture.authUser,
			auth.TokenSpec{Name: "direct-notice-user", BoundTenant: fixture.tenant, Role: auth.RoleOwner},
		)
		if err != nil {
			t.Fatalf("issue user-backed token: %v", err)
		}
		principal, err := fixture.authr.Authenticate(context.Background(), raw)
		if err != nil {
			t.Fatalf("authenticate user-backed token: %v", err)
		}
		ref, ok := principal.Ref()
		if !ok {
			t.Fatal("user-backed token has no ref")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		result, err := fixture.m.publishDirectNoticeWithAuthority(
			ctx, fixture.scope, ref, fixture.command(model.NewID(), "user-backed token"),
		)
		if err != nil || result.Code != "accepted" || fixture.authorizer.calls.Load() != 0 {
			t.Fatalf("user-backed token publish = (%+v, %v), legacy calls %d",
				result, err, fixture.authorizer.calls.Load())
		}
	})
}

func TestPublishDirectNoticeRejectsUnsealedAuthoritySnapshot(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, fixture.command(model.NewID(), "unsealed authority"),
	)
	if err != nil {
		t.Fatalf("normalize unsealed authority publish: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, cmd, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight unsealed authority publish: %v", err)
	}
	var unrelated store.AuthorizationFactRef
	if err := fixture.m.viewCommunication(context.Background(), fixture.scope, func(sc store.Scope) error {
		reader, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("authorization epoch reader unavailable")
		}
		var err error
		unrelated, err = reader.ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read unrelated authority fact: %v", err)
	}
	err = fixture.m.mutateCommunication(
		context.Background(), fixture.scope, func(tx *communicationTx) error {
			if err := tx.lockAuthoritySnapshot(
				context.Background(), []store.AuthorizationFactRef{unrelated},
			); err != nil {
				return err
			}
			_, _, applyErr := applyDirectNoticePublishAfterAuthoritySnapshot(
				context.Background(), tx, preflight, directNoticePublishAuthorityLock{},
			)
			return applyErr
		},
	)
	if !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("unsealed authority apply = %v, want transaction unavailable", err)
	}
	if rows := communicationRowsForTest(t, fixture, messageKind); len(rows) != 0 {
		t.Fatalf("unsealed authority apply persisted %d messages", len(rows))
	}
}

func TestPublishDirectNoticeAuthorityTokenRejectsCrossedPreflightWithSameFacts(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, fixture.command(model.NewID(), "authority token A"),
	)
	if err != nil {
		t.Fatalf("normalize authority token A: %v", err)
	}
	first, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, cmd, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight authority token A: %v", err)
	}
	second := first
	second.Command.ChannelID = model.NewID()
	second.IDs = newDirectNoticePublishIDs()
	actorB := sha256.Sum256([]byte("authority-token-actor-B"))
	idemB := sha256.Sum256([]byte("authority-token-idempotency-B"))
	requestB := sha256.Sum256([]byte("authority-token-request-B"))
	second.ActorFingerprint = actorB[:]
	second.IdempotencyHash = idemB[:]
	second.RequestDigest = requestB[:]
	firstFacts, err := directNoticePublishAuthorityFacts(first)
	if err != nil {
		t.Fatalf("authority token A facts: %v", err)
	}
	secondFacts, err := directNoticePublishAuthorityFacts(second)
	if err != nil {
		t.Fatalf("authority token B facts: %v", err)
	}
	if !equalDirectNoticeAuthorityFacts(firstFacts, secondFacts) {
		t.Fatalf("crossed preflight control facts differ: A=%+v B=%+v", firstFacts, secondFacts)
	}
	beforeAudit := directNoticeAuditHead(t, fixture)
	err = fixture.m.mutateCommunication(
		context.Background(), fixture.scope, func(tx *communicationTx) error {
			authorityLock, err := lockDirectNoticePublishAuthoritySnapshot(
				context.Background(), tx, first,
			)
			if err != nil {
				return err
			}
			_, _, err = applyDirectNoticePublishAfterAuthoritySnapshot(
				context.Background(), tx, second, authorityLock,
			)
			return err
		},
	)
	if !errors.Is(err, errCommunicationTransactionUnavailable) {
		t.Fatalf("crossed preflight authority token = %v, want transaction unavailable", err)
	}
	if rows := communicationRowsForTest(t, fixture, messageKind); len(rows) != 0 {
		t.Fatalf("crossed preflight authority token persisted %d messages", len(rows))
	}
	afterAudit := directNoticeAuditHead(t, fixture)
	if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatalf("crossed preflight authority token changed audit: before=%+v after=%+v",
			beforeAudit, afterAudit)
	}
}

func TestDirectNoticePublishAuthorityPreflightCommitmentCoversEveryField(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal,
		fixture.command(model.NewID(), "authority commitment field inventory"),
	)
	if err != nil {
		t.Fatalf("normalize commitment inventory command: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, cmd, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("build commitment inventory preflight: %v", err)
	}
	bindingA := &communicationRequestAuthorityBindingID{marker: 1}
	preflight.bindingID = bindingA
	baseline, err := directNoticePublishAuthorityPreflightCommitment(preflight)
	if err != nil {
		t.Fatalf("commit baseline preflight: %v", err)
	}

	wantExported := []string{
		"Command", "Scope", "Principal", "Sender", "Channel", "IDs", "Payload",
		"AudienceRequest", "AudienceAttestation", "Snapshot", "GrantClosure",
		"RecipientGrantClosure", "CoreWitness", "ActorFingerprint", "IdempotencyHash",
		"RequestDigest",
	}
	preflightType := reflect.TypeOf(directNoticePublishPreflight{})
	if preflightType.NumField() != len(wantExported)+1 {
		t.Fatalf("preflight field count = %d, want %d exported plus bindingID",
			preflightType.NumField(), len(wantExported))
	}
	exported := make([]string, 0, len(wantExported))
	private := make([]string, 0, 1)
	for index := 0; index < preflightType.NumField(); index++ {
		field := preflightType.Field(index)
		if field.IsExported() {
			exported = append(exported, field.Name)
		} else {
			private = append(private, field.Name)
		}
	}
	if !slices.Equal(exported, wantExported) || !slices.Equal(private, []string{"bindingID"}) {
		t.Fatalf("preflight inventory = exported %v private %v, want %v + bindingID",
			exported, private, wantExported)
	}

	mutations := []struct {
		field  string
		mutate func(*directNoticePublishPreflight)
	}{
		{field: "Command", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Command.Urgency = UrgencyHigh
		}},
		{field: "Scope", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Scope.WorkspaceID = model.NewID()
		}},
		{field: "Principal", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Principal.UserID = model.NewID()
		}},
		{field: "Sender", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Sender.Ref = model.NewID().String()
		}},
		{field: "Channel", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Channel.Description += " commitment mutation"
		}},
		{field: "IDs", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.IDs.Message = model.NewID()
		}},
		{field: "Payload", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Payload.Digest[0] ^= 0xff
		}},
		{field: "AudienceRequest", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.AudienceRequest.ChannelACLRevision++
		}},
		{field: "AudienceAttestation", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.AudienceAttestation.DirectoryEpoch++
		}},
		{field: "Snapshot", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.Snapshot.RosterHash[0] ^= 0xff
		}},
		{field: "GrantClosure", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.GrantClosure.Code = "subjects_rechecked"
		}},
		{field: "RecipientGrantClosure", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.RecipientGrantClosure.Code = "recipient_subjects_rechecked"
		}},
		{field: "CoreWitness", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.CoreWitness.Code = "message_send_rechecked"
		}},
		{field: "ActorFingerprint", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.ActorFingerprint[0] ^= 0xff
		}},
		{field: "IdempotencyHash", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.IdempotencyHash[0] ^= 0xff
		}},
		{field: "RequestDigest", mutate: func(candidate *directNoticePublishPreflight) {
			candidate.RequestDigest[0] ^= 0xff
		}},
	}
	mutationFields := make([]string, 0, len(mutations))
	for _, mutation := range mutations {
		mutationFields = append(mutationFields, mutation.field)
	}
	if !slices.Equal(mutationFields, wantExported) {
		t.Fatalf("commitment mutation inventory = %v, want %v", mutationFields, wantExported)
	}
	baselineValue := reflect.ValueOf(preflight)
	for _, mutation := range mutations {
		t.Run(mutation.field, func(t *testing.T) {
			candidate := cloneDirectNoticePublishPreflight(preflight)
			mutation.mutate(&candidate)
			candidateValue := reflect.ValueOf(candidate)
			for _, field := range wantExported {
				before := baselineValue.FieldByName(field).Interface()
				after := candidateValue.FieldByName(field).Interface()
				if field == mutation.field {
					if reflect.DeepEqual(before, after) {
						t.Fatalf("mutation left %s unchanged", field)
					}
					continue
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("%s mutation also changed top-level field %s", mutation.field, field)
				}
			}
			if candidate.bindingID != preflight.bindingID {
				t.Fatalf("%s mutation also changed bindingID", mutation.field)
			}
			commitment, err := directNoticePublishAuthorityPreflightCommitment(candidate)
			if err != nil {
				t.Fatalf("commit %s mutation: %v", mutation.field, err)
			}
			if commitment == baseline {
				t.Fatalf("commitment omitted top-level field %s", mutation.field)
			}
		})
	}

	rebound := cloneDirectNoticePublishPreflight(preflight)
	rebound.bindingID = &communicationRequestAuthorityBindingID{marker: 2}
	reboundCommitment, err := directNoticePublishAuthorityPreflightCommitment(rebound)
	if err != nil {
		t.Fatalf("commit rebound binding identity: %v", err)
	}
	if rebound.bindingID == preflight.bindingID || reboundCommitment != baseline {
		t.Fatalf("bindingID replacement changed ephemeral preflight commitment: %x != %x",
			reboundCommitment, baseline)
	}
}

func TestPublishDirectNoticeAuthorityTokenCommitsFullPreflight(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	idempotency := model.NewID()
	makePreflight := func(canary string) directNoticePublishPreflight {
		t.Helper()
		cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
			fixture.scope, principal, fixture.command(idempotency, canary),
		)
		if err != nil {
			t.Fatalf("normalize %s preflight: %v", canary, err)
		}
		preflight, err := fixture.m.preflightDirectNoticePublish(
			context.Background(), fixture.scope, principal, cmd, actor, idem, request,
		)
		if err != nil {
			t.Fatalf("build %s preflight: %v", canary, err)
		}
		return preflight
	}
	first := makePreflight("authority commitment A")
	payloadB := makePreflight("authority commitment payload B").Payload

	mutations := []struct {
		name   string
		mutate func(*directNoticePublishPreflight) error
	}{
		{
			name: "valid_payload_swap",
			mutate: func(candidate *directNoticePublishPreflight) error {
				candidate.Payload = cloneProtectedPayload(payloadB)
				return nil
			},
		},
		{
			name: "valid_snapshot_window_swap",
			mutate: func(candidate *directNoticePublishPreflight) error {
				candidate.Snapshot.ObservedAt = candidate.Snapshot.ObservedAt.Add(time.Nanosecond)
				candidate.Snapshot.FreshUntil = candidate.Snapshot.FreshUntil.Add(time.Nanosecond)
				candidate.AudienceAttestation.ObservedAt = candidate.Snapshot.ObservedAt
				candidate.AudienceAttestation.FreshUntil = candidate.Snapshot.FreshUntil
				hash, err := CanonicalPublicationAudienceSnapshotHash(candidate.Snapshot)
				if err != nil {
					return err
				}
				candidate.AudienceAttestation.SnapshotHash = hash
				return nil
			},
		},
		{
			name: "valid_closure_subject_alias_change",
			mutate: func(candidate *directNoticePublishPreflight) error {
				candidate.GrantClosure.Subjects = append(
					candidate.GrantClosure.Subjects,
					CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()},
				)
				return nil
			},
		},
		{
			name: "valid_recipient_closure_subject_alias_change",
			mutate: func(candidate *directNoticePublishPreflight) error {
				candidate.RecipientGrantClosure.Subjects = append(
					candidate.RecipientGrantClosure.Subjects,
					CommunicationSubjectRef{Kind: SubjectUserGroup, Ref: model.NewID().String()},
				)
				return nil
			},
		},
		{
			name: "valid_core_witness_temporal_evidence_change",
			mutate: func(candidate *directNoticePublishPreflight) error {
				candidate.CoreWitness.ObservedAt = candidate.CoreWitness.ObservedAt.Add(-time.Nanosecond)
				candidate.CoreWitness.FreshUntil = candidate.CoreWitness.FreshUntil.Add(-time.Nanosecond)
				candidate.CoreWitness.CorePermission.Code = "core_permission_rechecked"
				candidate.CoreWitness.ResourceGuard.EvidenceRef =
					"direct_notice_resource_guard_rechecked"
				candidate.CoreWitness.ForbidAbsence.Code = "forbid_absence_rechecked"
				return nil
			},
		},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			probe := cloneDirectNoticePublishPreflight(first)
			if err := test.mutate(&probe); err != nil {
				t.Fatalf("mutate preflight control: %v", err)
			}
			if _, err := directNoticePublishAuthorityIdentityFor(probe); err != nil {
				t.Fatalf("mutated preflight is not independently well-shaped: %v", err)
			}
			firstFacts, err := directNoticePublishAuthorityFacts(first)
			if err != nil {
				t.Fatalf("first authority facts: %v", err)
			}
			probeFacts, err := directNoticePublishAuthorityFacts(probe)
			if err != nil || !equalDirectNoticeAuthorityFacts(firstFacts, probeFacts) {
				t.Fatalf("mutated preflight facts = (%+v, %v), want same as %+v",
					probeFacts, err, firstFacts)
			}

			beforeGuard := directNoticeDeliveryGuard(t, fixture)
			beforeAudit := directNoticeAuditHead(t, fixture)
			candidate := cloneDirectNoticePublishPreflight(first)
			err = fixture.m.mutateCommunication(
				context.Background(), fixture.scope, func(tx *communicationTx) error {
					authorityLock, lockErr := lockDirectNoticePublishAuthoritySnapshot(
						context.Background(), tx, first,
					)
					if lockErr != nil {
						return lockErr
					}
					if mutateErr := test.mutate(&candidate); mutateErr != nil {
						return mutateErr
					}
					_, _, applyErr := applyDirectNoticePublishAfterAuthoritySnapshot(
						context.Background(), tx, candidate, authorityLock,
					)
					return applyErr
				},
			)
			if !errors.Is(err, errCommunicationTransactionUnavailable) {
				t.Fatalf("mutated preflight apply = %v, want transaction unavailable", err)
			}
			for _, kind := range []model.Kind{
				messageKind, messageAudienceKind, messageAudienceRecipientKind,
				messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
			} {
				if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
					t.Fatalf("mutated preflight persisted %d %s rows", len(rows), kind)
				}
			}
			afterGuard := directNoticeDeliveryGuard(t, fixture)
			if afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq {
				t.Fatalf("mutated preflight advanced guard: before=%+v after=%+v",
					beforeGuard, afterGuard)
			}
			afterAudit := directNoticeAuditHead(t, fixture)
			if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
				t.Fatalf("mutated preflight changed audit: before=%+v after=%+v",
					beforeAudit, afterAudit)
			}
		})
	}
}

func TestDirectNoticePreflightTakesDeepOwnershipOfCallerAndPortValues(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "caller-owned content")
	originalText := "caller-owned text block"
	originalReference := model.NewID().String()
	command.Content.Blocks = []MessageContentBlock{
		{Type: ContentBlockText, Format: TextPlain, Text: originalText},
		{Type: ContentBlockReference, Reference: &ContentReference{
			Kind: "artifact", Ref: model.NewID().String(), Hash: "sha256:caller-owned",
		}},
	}
	command.Content.Blocks[1].Reference.Ref = originalReference
	callerBlocks := command.Content.Blocks
	callerReference := command.Content.Blocks[1].Reference
	normalized, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize aliased command: %v", err)
	}
	callerBlocks[0].Text = "mutated-after-normalize"
	callerReference.Ref = "mutated-after-normalize"
	if normalized.Content.Blocks[0].Text != originalText ||
		normalized.Content.Blocks[1].Reference == nil ||
		normalized.Content.Blocks[1].Reference.Ref != originalReference {
		t.Fatalf("normalized command retained caller block/reference aliases: %+v",
			normalized.Content.Blocks)
	}
	_, _, _, recomputedRequest, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, normalized,
	)
	if err != nil || !bytes.Equal(recomputedRequest, request) {
		t.Fatalf("owned normalized request digest = (%x, %v), want %x",
			recomputedRequest, err, request)
	}

	attestor := &directNoticeAliasingAudienceAttestor{inner: fixture.attestor}
	closure := &directNoticeAliasingGrantClosureResolver{inner: fixture.closure}
	fixture.m.communicationAudienceAttestor = attestor
	fixture.m.communicationGrantClosure = closure
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, normalized, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight aliased port values: %v", err)
	}
	before, err := directNoticePublishAuthorityPreflightCommitment(preflight)
	if err != nil {
		t.Fatalf("commit owned preflight: %v", err)
	}
	if len(attestor.request.Selectors) != 1 || len(attestor.snapshot.Selectors) != 1 ||
		len(attestor.snapshot.Recipients) != 1 || len(attestor.snapshot.Contributions) != 1 ||
		len(attestor.attestation.RequestHash) == 0 || len(closure.returned) != 2 ||
		len(closure.returned[0].Subjects) == 0 || len(closure.returned[1].Subjects) == 0 {
		t.Fatal("aliasing port controls did not retain the expected nested values")
	}
	if attestor.request.Selectors[0].Ref == fixture.recipient.String() ||
		attestor.request.ChannelACLRevision == preflight.AudienceRequest.ChannelACLRevision {
		t.Fatalf("aliasing attestor did not synchronously mutate its received request: %+v",
			attestor.request)
	}
	if err := validateDirectNoticeSnapshot(
		preflight.AudienceRequest, attestor.snapshot, attestor.attestation,
		preflight.Command.Recipient,
	); err != nil {
		t.Fatalf("aliasing attestor changed its original output: %v", err)
	}
	attestor.request.Selectors[0].Ref = model.NewID().String()
	attestor.snapshot.Selectors[0].Ref = model.NewID().String()
	attestor.snapshot.Recipients[0].Recipient.Ref = model.NewID().String()
	attestor.snapshot.Contributions[0].RouteReasons[0] = RouteReason("mutated")
	attestor.snapshot.RosterHash[0] ^= 0xff
	attestor.attestation.RequestHash[0] ^= 0xff
	attestor.attestation.SnapshotHash[0] ^= 0xff
	closure.returned[0].Subjects[0].Ref = model.NewID().String()
	closure.returned[1].Subjects[0].Ref = model.NewID().String()
	after, err := directNoticePublishAuthorityPreflightCommitment(preflight)
	if err != nil {
		t.Fatalf("recommit owned preflight: %v", err)
	}
	if before != after {
		t.Fatalf("preflight commitment changed through caller/port aliases: %x != %x", before, after)
	}
	if preflight.Command.Content.Blocks[0].Text != originalText ||
		preflight.Command.Content.Blocks[1].Reference == nil ||
		preflight.Command.Content.Blocks[1].Reference.Ref != originalReference ||
		preflight.AudienceRequest.Selectors[0].Ref != fixture.recipient.String() ||
		preflight.Snapshot.Selectors[0].Ref != fixture.recipient.String() ||
		preflight.Snapshot.Recipients[0].Recipient.Ref != fixture.recipient.String() ||
		preflight.Snapshot.Contributions[0].RouteReasons[0] != RouteReason("direct") ||
		preflight.GrantClosure.Subjects[0].Ref != fixture.sender.String() ||
		preflight.RecipientGrantClosure.Subjects[0].Ref != fixture.recipient.String() {
		t.Fatalf("preflight retained caller/port aliases: %+v", preflight)
	}
}

func TestPublishDirectNoticeReplayRejectsEpochChangeBeforeBoundConfirmation(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := fixture.command(model.NewID(), "epoch-fenced replay")
	first, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if err != nil {
		t.Fatalf("seed exact replay: %v", err)
	}

	base := fixture.m.data
	afterView := &directNoticeAfterViewData{
		inner: base,
		after: func(ctx context.Context) error {
			return base.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
				epochs, ok := sc.(store.AuthorizationEpochStore)
				if !ok {
					return errors.New("authorization epoch store unavailable")
				}
				current, err := epochs.ReadAuthorizationEpoch(ctx)
				if err != nil {
					return err
				}
				_, err = epochs.BumpAuthorizationEpoch(ctx, current)
				return err
			})
		},
	}
	trace := &directNoticeAuthorityTrace{}
	fixture.m.data = &directNoticeAuthorityTraceData{inner: afterView, trace: trace}
	replay, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || replay != (DirectNoticePublishResult{}) {
		t.Fatalf("epoch-raced replay = (%+v, %v), want empty UNKNOWN", replay, err)
	}
	if len(trace.authorityFacts) != 1 || len(trace.steps) != 1 || trace.steps[0] != "authority" {
		t.Fatalf("epoch-raced confirmation trace = steps %v facts %v, want authority-only attempt",
			trace.steps, trace.authorityFacts)
	}
	if rows := communicationRowsForTest(t, fixture, messageKind); len(rows) != 1 ||
		rows[0].String(model.ColID) != first.MessageID.String() {
		t.Fatalf("epoch-raced replay changed messages: %+v", rows)
	}
}

func TestPublishDirectNoticeReplayRejectsRevokedSessionBeforeBoundConfirmation(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := fixture.command(model.NewID(), "session-revocation-fenced replay")
	first, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if err != nil {
		t.Fatalf("seed session-revocation replay: %v", err)
	}
	receiptRows := communicationRowsForTest(t, fixture, communicationCommandKind)
	if len(receiptRows) != 1 {
		t.Fatalf("seed receipt count = %d, want one", len(receiptRows))
	}
	beforeReceipt, err := canonicalJSON(receiptRows[0])
	if err != nil {
		t.Fatalf("encode seed receipt: %v", err)
	}

	base := fixture.m.data
	afterView := &directNoticeAfterViewData{
		inner: base,
		after: func(ctx context.Context) error {
			return fixture.authr.RevokeSession(ctx, fixture.authUser, fixture.authUser.CredID)
		},
	}
	trace := &directNoticeAuthorityTrace{}
	fixture.m.data = &directNoticeAuthorityTraceData{inner: afterView, trace: trace}
	replay, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, cmd,
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		replay != (DirectNoticePublishResult{}) {
		t.Fatalf("session-revoked replay = (%+v, %v), want empty UNKNOWN", replay, err)
	}
	if len(trace.authorityFacts) != 1 || len(trace.steps) != 1 || trace.steps[0] != "authority" {
		t.Fatalf("session-revoked confirmation trace = steps %v facts %v, want authority-only",
			trace.steps, trace.authorityFacts)
	}
	fixture.m.data = base
	afterReceiptRows := communicationRowsForTest(t, fixture, communicationCommandKind)
	if len(afterReceiptRows) != 1 {
		t.Fatalf("session-revoked receipt count = %d, want one", len(afterReceiptRows))
	}
	afterReceipt, err := canonicalJSON(afterReceiptRows[0])
	if err != nil {
		t.Fatalf("encode retained receipt: %v", err)
	}
	if !bytes.Equal(afterReceipt, beforeReceipt) {
		t.Fatalf("session revocation changed retained receipt:\nbefore %s\nafter  %s",
			beforeReceipt, afterReceipt)
	}
	if rows := communicationRowsForTest(t, fixture, messageKind); len(rows) != 1 ||
		rows[0].String(model.ColID) != first.MessageID.String() {
		t.Fatalf("session-revoked replay changed messages: %+v", rows)
	}
}

func TestPublishDirectNoticeExactFinalAuthorityExpiryRollsBack(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		seed bool
	}{
		{name: "new_publish"},
		{name: "replay_confirmation", seed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAuthorityFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			cmd := fixture.command(model.NewID(), "final authority expiry")
			if test.seed {
				if _, err := fixture.m.publishDirectNoticeWithAuthority(
					ctx, fixture.scope, fixture.ref, cmd,
				); err != nil {
					t.Fatalf("seed final-expiry replay: %v", err)
				}
			}
			beforeGuard := directNoticeDeliveryGuard(t, fixture)
			beforeAudit := directNoticeAuditHead(t, fixture)
			beforeCounts := make(map[model.Kind]int)
			for _, kind := range []model.Kind{
				messageKind, messageAudienceKind, messageAudienceRecipientKind,
				messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
			} {
				beforeCounts[kind] = len(communicationRowsForTest(t, fixture, kind))
			}
			expiry := &directNoticeFinalExpiryData{
				inner: fixture.m.data,
				final: model.NewTimestamp(fixture.source.evidence.FreshUntil),
			}
			fixture.m.data = expiry
			result, err := fixture.m.publishDirectNoticeWithAuthority(
				ctx, fixture.scope, fixture.ref, cmd,
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				result != (DirectNoticePublishResult{}) {
				t.Fatalf("final-expired exact %s = (%+v, %v), want empty UNKNOWN",
					test.name, result, err)
			}
			if expiry.calls.Load() != 3 {
				t.Fatalf("final-expired exact %s clock calls = %d with error %v, want initial+refresh+finalize",
					test.name, expiry.calls.Load(), err)
			}
			for kind, before := range beforeCounts {
				if after := len(communicationRowsForTest(t, fixture, kind)); after != before {
					t.Fatalf("final-expired exact %s changed %s rows %d -> %d",
						test.name, kind, before, after)
				}
			}
			afterGuard := directNoticeDeliveryGuard(t, fixture)
			if afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq {
				t.Fatalf("final-expired exact %s advanced guard: before=%+v after=%+v",
					test.name, beforeGuard, afterGuard)
			}
			afterAudit := directNoticeAuditHead(t, fixture)
			if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
				t.Fatalf("final-expired exact %s changed audit: before=%+v after=%+v",
					test.name, beforeAudit, afterAudit)
			}
		})
	}
}

func TestDirectNoticeSameTransactionReplayDefersGraphReadsToFreshView(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "fresh replay snapshot")
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	); err != nil {
		t.Fatalf("seed fresh-view replay: %v", err)
	}
	normalized, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize fresh-view replay: %v", err)
	}
	receiptRows := communicationRowsForTest(t, fixture, communicationCommandKind)
	var resolved []model.Kind
	resolve := func(kind model.Kind) (communicationReadRepository, error) {
		resolved = append(resolved, kind)
		if kind != communicationCommandKind {
			return nil, fmt.Errorf("unexpected mutable graph read for %s", kind)
		}
		return directNoticeReplayRepository{rows: receiptRows}, nil
	}
	if _, found, err := lookupDirectNoticeReplay(
		context.Background(), resolve, nil, nil, fixture.scope, principal, normalized,
		actor, idem, request,
	); !errors.Is(err, errDirectNoticeReplayNeedsFreshAudit) || found {
		t.Fatalf("same-tx replay = found %t, err %v; want fresh-view sentinel", found, err)
	}
	if len(resolved) != 1 || resolved[0] != communicationCommandKind {
		t.Fatalf("same-tx replay resolved kinds = %v, want receipt only", resolved)
	}
}

func TestPublishDirectNoticeReplaysThroughPreconfinedModuleData(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	fixture.m.data = directNoticeWorkspaceMarkedData{
		inner: fixture.m.data, workspace: fixture.scope.WorkspaceID,
	}
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "preconfined-replay")
	first, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("publish through preconfined ModuleData: %v", err)
	}
	beforeAudit := directNoticeAuditHead(t, fixture)
	beforeGuard := directNoticeDeliveryGuard(t, fixture)

	fixture.m.communicationAudienceAttestor = nil
	fixture.m.communicationGrantClosure = nil
	fixture.m.communicationOperationAuthorizer = nil
	replayed, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("replay through preconfined ModuleData: %v", err)
	}
	if !replayed.Replayed || replayed.MessageID != first.MessageID ||
		replayed.EventID != first.EventID || replayed.CommandID != first.CommandID {
		t.Fatalf("preconfined replay = %+v, first = %+v", replayed, first)
	}
	afterAudit := directNoticeAuditHead(t, fixture)
	afterGuard := directNoticeDeliveryGuard(t, fixture)
	if afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) ||
		afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq {
		t.Fatalf("preconfined replay changed state: audit %+v -> %+v, guard %+v -> %+v",
			beforeAudit, afterAudit, beforeGuard, afterGuard)
	}
}

func TestPublishDirectNoticeMaterializesEveryDefaultAckPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		policy      AckPolicy
		fulfillment FulfillmentProjection
	}{
		{name: "none", policy: AckPolicyNone,
			fulfillment: FulfillmentProjection{State: FulfillmentNotRequired}},
		{name: "each-required", policy: AckPolicyEachRequired,
			fulfillment: FulfillmentProjection{
				State: FulfillmentPending, Required: 1, Viable: 1,
			}},
		{name: "quorum", policy: AckPolicyQuorum,
			fulfillment: FulfillmentProjection{
				State: FulfillmentPending, Required: 1, Viable: 1, Quorum: 1,
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeFixtureWithOptions(t, test.policy, 0)
			result, err := fixture.m.publishDirectNotice(
				context.Background(), fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender},
				fixture.command(model.NewID(), "ack policy "+test.name),
			)
			if err != nil {
				t.Fatalf("publish %s direct notice: %v", test.policy, err)
			}
			if result.Fulfillment != test.fulfillment ||
				result.RequiredCount != test.fulfillment.Required ||
				result.AckQuorum != test.fulfillment.Quorum {
				t.Fatalf("%s result = %+v, fulfillment want %+v", test.policy, result, test.fulfillment)
			}
			message, err := messageFromRecord(
				communicationRowsForTest(t, fixture, messageKind)[0], test.fulfillment.Required,
			)
			if err != nil {
				t.Fatalf("decode %s Message: %v", test.policy, err)
			}
			delivery, err := messageDeliveryFromRecord(
				communicationRowsForTest(t, fixture, messageDeliveryKind)[0],
			)
			if err != nil {
				t.Fatalf("decode %s Delivery: %v", test.policy, err)
			}
			if message.AckPolicy != test.policy || delivery.Required != (test.fulfillment.Required == 1) {
				t.Fatalf("%s Message/Delivery Ack shape = %+v / %+v", test.policy, message, delivery)
			}
			if test.policy == AckPolicyNone {
				if message.AckDueAt != nil || delivery.AckDueAt != nil {
					t.Fatalf("none policy carries Ack deadline: %+v / %+v", message.AckDueAt, delivery.AckDueAt)
				}
			} else if message.AckDueAt == nil || delivery.AckDueAt == nil ||
				!message.AckDueAt.Equal(*delivery.AckDueAt) ||
				message.AckDueAt.Sub(message.CreatedAt) != time.Minute {
				t.Fatalf("%s Ack deadline = %+v / %+v", test.policy, message.AckDueAt, delivery.AckDueAt)
			}
			receipt, err := communicationCommandReceiptFromRecord(
				communicationRowsForTest(t, fixture, communicationCommandKind)[0],
			)
			if err != nil {
				t.Fatalf("decode %s receipt: %v", test.policy, err)
			}
			got, err := directNoticeInitialFulfillmentFromProjection(receipt.ResponseProjectionJSON)
			if err != nil || got != test.fulfillment {
				t.Fatalf("%s receipt fulfillment = %+v, %v; want %+v", test.policy, got, err, test.fulfillment)
			}
		})
	}
}

func TestDirectNoticeReplaySealsInitialAckDeadline(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixtureWithOptions(t, AckPolicyEachRequired, 0)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "sealed initial ack deadline")
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	); err != nil {
		t.Fatalf("publish required direct notice: %v", err)
	}
	normalized, _, _, _, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize required direct notice: %v", err)
	}
	receipt, err := communicationCommandReceiptFromRecord(
		communicationRowsForTest(t, fixture, communicationCommandKind)[0],
	)
	if err != nil {
		t.Fatalf("decode required direct notice receipt: %v", err)
	}
	rows := map[model.Kind][]model.Record{
		messageKind:                  communicationRowsForTest(t, fixture, messageKind),
		messageAudienceKind:          communicationRowsForTest(t, fixture, messageAudienceKind),
		messageAudienceRecipientKind: communicationRowsForTest(t, fixture, messageAudienceRecipientKind),
		messageDeliveryKind:          communicationRowsForTest(t, fixture, messageDeliveryKind),
		messageAckKind:               nil,
		workEventKind:                communicationRowsForTest(t, fixture, workEventKind),
		workOutboxKind:               communicationRowsForTest(t, fixture, workOutboxKind),
	}
	resolve := func(kind model.Kind) (communicationReadRepository, error) {
		values, present := rows[kind]
		if !present {
			return nil, store.ErrNotFound
		}
		return directNoticeReplayRepository{rows: values}, nil
	}
	if _, err := directNoticeResultFromReceipt(
		context.Background(), resolve, nil, principal, normalized, receipt,
	); err != nil {
		t.Fatalf("required replay control: %v", err)
	}
	changedDue := model.NewTimestamp(receipt.CompletedAt.Add(2 * time.Minute)).String()
	rows[messageKind][0][colCommAckDueAt] = changedDue
	rows[messageDeliveryKind][0][colCommAckDueAt] = changedDue
	if _, err := directNoticeResultFromReceipt(
		context.Background(), resolve, nil, principal, normalized, receipt,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("relabelled Ack deadline replay = %v, want UNKNOWN", err)
	}
}

func TestPublishDirectNoticeLocksAuthorityBeforeCommunicationRows(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, fixture.command(model.NewID(), "authority order"),
	)
	if err != nil {
		t.Fatalf("normalize direct notice: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, cmd, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight direct notice: %v", err)
	}
	var order []string
	err = fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
		lockAuthority := tx.lockAuthoritySnapshotFn
		resolve := tx.resolveRepository
		tx.lockAuthoritySnapshotFn = func(
			ctx context.Context,
			facts []store.AuthorizationFactRef,
		) error {
			order = append(order, "authority")
			return lockAuthority(ctx, facts)
		}
		tx.resolveRepository = func(kind model.Kind) (communicationRepository, error) {
			order = append(order, "repository:"+string(kind))
			return resolve(kind)
		}
		_, gap, applyErr := applyDirectNoticePublish(context.Background(), tx, preflight)
		if gap {
			return errors.New("unexpected audit gap")
		}
		return applyErr
	})
	if err != nil {
		t.Fatalf("apply direct notice with lock-order spy: %v", err)
	}
	if len(order) < 2 || order[0] != "authority" || !strings.HasPrefix(order[1], "repository:") {
		t.Fatalf("publish lock order = %v, want authority before every communication repository", order)
	}
}

func TestPublishDirectNoticeRollsBackLateEffectFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		kind             model.Kind
		failCreate       bool
		failCreateWithID bool
	}{
		{name: "outbox", kind: workOutboxKind, failCreate: true},
		{name: "receipt", kind: communicationCommandKind, failCreateWithID: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeFixture(t)
			principal := CommunicationPrincipal{UserID: fixture.sender}
			command := fixture.command(model.NewID(), "late "+test.name+" failure")
			normalized, actor, idem, request, err := normalizeDirectNoticePublishCommand(
				fixture.scope, principal, command,
			)
			if err != nil {
				t.Fatalf("normalize direct notice: %v", err)
			}
			preflight, err := fixture.m.preflightDirectNoticePublish(
				context.Background(), fixture.scope, principal, normalized, actor, idem, request,
			)
			if err != nil {
				t.Fatalf("preflight direct notice: %v", err)
			}
			beforeGuard := directNoticeDeliveryGuard(t, fixture)
			beforeAudit := directNoticeAuditHead(t, fixture)
			failure := errors.New("injected late " + test.name + " failure")
			err = fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
				resolve := tx.resolveRepository
				tx.resolveRepository = func(kind model.Kind) (communicationRepository, error) {
					repo, resolveErr := resolve(kind)
					if resolveErr != nil || kind != test.kind {
						return repo, resolveErr
					}
					return directNoticeFailingWriteRepository(
						repo, test.failCreate, test.failCreateWithID, failure,
					), nil
				}
				_, gap, applyErr := applyDirectNoticePublish(context.Background(), tx, preflight)
				if gap {
					return errors.New("unexpected audit gap")
				}
				return applyErr
			})
			if !errors.Is(err, failure) {
				t.Fatalf("late %s failure = %v, want injected error", test.name, err)
			}
			if got := directNoticeDeliveryGuard(t, fixture); got != beforeGuard {
				t.Fatalf("late %s failure changed guard: before=%+v after=%+v",
					test.name, beforeGuard, got)
			}
			if got := directNoticeAuditHead(t, fixture); got.Seq != beforeAudit.Seq ||
				!bytes.Equal(got.Hash, beforeAudit.Hash) {
				t.Fatalf("late %s failure changed audit: before=%+v after=%+v",
					test.name, beforeAudit, got)
			}
			for _, kind := range []model.Kind{
				messageKind, messageAudienceKind, messageAudienceRecipientKind,
				messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
			} {
				if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
					t.Fatalf("late %s failure retained %d %s rows", test.name, len(rows), kind)
				}
			}
		})
	}
}

func TestPublishDirectNoticeLocksOnlyCurrentGrantAndLabelRows(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	ctx := context.Background()
	values := []byte(`["value"]`)
	valuesHash := sha256.Sum256(values)
	err := fixture.m.mutateCommunication(ctx, fixture.scope, func(tx *communicationTx) error {
		base := func(id model.ID) MutableCommunicationEntity {
			return MutableCommunicationEntity{CommunicationEntity: CommunicationEntity{
				ID: id, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
				Version: 1, CreatedAt: tx.now.Time(),
			}, UpdatedAt: tx.now.Time()}
		}
		for generation := int64(1); generation <= 201; generation++ {
			state := ChannelLabelDisabled
			if generation == 201 {
				state = ChannelLabelActive
			}
			label := ChannelLabelDefinition{
				MutableCommunicationEntity: base(model.NewID()), ChannelID: fixture.channel.ID,
				Key: "history", Generation: generation, AllowedValuesJSON: values,
				ValuesHash: valuesHash[:], Classification: ChannelLabelNonSensitive, State: state,
			}
			record, encodeErr := channelLabelDefinitionToRecord(label)
			if encodeErr != nil {
				return encodeErr
			}
			if _, createErr := tx.createWithID(
				ctx, channelLabelDefinitionKind, label.ID, record,
			); createErr != nil {
				return createErr
			}
		}
		for index := 0; index < 200; index++ {
			actor := CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()}
			grant := ChannelGrant{
				MutableCommunicationEntity: base(model.NewID()), ChannelID: fixture.channel.ID,
				Subject:    CommunicationSubjectRef{Kind: SubjectUser, Ref: model.NewID().String()},
				Generation: 1, CanRead: true, State: ChannelGrantRevoked,
				GrantedBy: actor, RevokedBy: &actor,
			}
			record, encodeErr := channelGrantToRecord(grant)
			if encodeErr != nil {
				return encodeErr
			}
			if _, createErr := tx.createWithID(ctx, channelGrantKind, grant.ID, record); createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed historical Channel rows: %v", err)
	}
	principal := CommunicationPrincipal{UserID: fixture.sender}
	cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, fixture.command(model.NewID(), "historical rows"),
	)
	if err != nil {
		t.Fatalf("normalize historical-row publish: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		ctx, fixture.scope, principal, cmd, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight historical-row publish: %v", err)
	}
	var grantLocks, labelLocks atomic.Int64
	err = fixture.m.mutateCommunication(ctx, fixture.scope, func(tx *communicationTx) error {
		resolve := tx.resolveRepository
		tx.resolveRepository = func(kind model.Kind) (communicationRepository, error) {
			repo, resolveErr := resolve(kind)
			if resolveErr != nil {
				return nil, resolveErr
			}
			switch kind {
			case channelGrantKind:
				return directNoticeLockCountingRepository{communicationRepository: repo, locks: &grantLocks}, nil
			case channelLabelDefinitionKind:
				return directNoticeLockCountingRepository{communicationRepository: repo, locks: &labelLocks}, nil
			default:
				return repo, nil
			}
		}
		_, gap, applyErr := applyDirectNoticePublish(ctx, tx, preflight)
		if gap {
			return errors.New("unexpected historical-row audit gap")
		}
		return applyErr
	})
	if err != nil {
		t.Fatalf("publish with historical Channel rows: %v", err)
	}
	if grantLocks.Load() != 2 || labelLocks.Load() != 1 {
		t.Fatalf("current grant/label lock counts = %d/%d, want 2/1",
			grantLocks.Load(), labelLocks.Load())
	}
}

func TestPublishDirectNoticeAcceptsWorkspaceRouteGuardAheadOfChannel(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixtureWithOptions(t, AckPolicyNone, 7)
	result, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "workspace route guard ahead"),
	)
	if err != nil || result.State != MessagePublished {
		t.Fatalf("publish with advanced workspace route guard = %+v, %v", result, err)
	}
}

func TestPublishDirectNoticeRejectsRouteGuardThatDoesNotCoverChannelRevision(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixtureWithOptions(t, AckPolicyNone, -1)
	beforeGuard := directNoticeDeliveryGuard(t, fixture)
	_, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "stale route guard"),
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("stale route guard error = %v, want UNKNOWN", err)
	}
	afterGuard := directNoticeDeliveryGuard(t, fixture)
	if afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq ||
		len(communicationRowsForTest(t, fixture, messageKind)) != 0 {
		t.Fatalf("stale route guard changed publish state: before=%+v after=%+v", beforeGuard, afterGuard)
	}
}

func TestPublishDirectNoticeRejectsUnLockableOperationFactWithoutEffects(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	fixture.authorizer.facts = []store.AuthorizationFactRef{{
		Kind: "core.user_group_member", ID: model.NewID(), Version: 1,
	}}
	beforeGuard := directNoticeDeliveryGuard(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture)
	_, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "unsupported authority fact"),
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("unsupported operation fact error = %v, want UNKNOWN", err)
	}
	afterGuard := directNoticeDeliveryGuard(t, fixture)
	afterAudit := directNoticeAuditHead(t, fixture)
	if afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq ||
		afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
		t.Fatalf("unsupported fact changed state: guard %+v -> %+v, audit %+v -> %+v",
			beforeGuard, afterGuard, beforeAudit, afterAudit)
	}
	for _, kind := range []model.Kind{
		messageKind, messageAudienceKind, messageAudienceRecipientKind, messageDeliveryKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
			t.Fatalf("unsupported fact persisted %d %s rows", len(rows), kind)
		}
	}
}

func TestPublishDirectNoticeRejectsStaleAuthorityEvidenceWithoutEffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		configure    func(*directNoticeFixture)
		wantClosure  int64
		wantAttestor int64
	}{
		{
			name: "expired core witness",
			configure: func(f *directNoticeFixture) {
				f.authorizer.now = f.now.Add(-10 * time.Minute)
			},
		},
		{
			name: "future core witness",
			configure: func(f *directNoticeFixture) {
				f.authorizer.now = f.now.Add(time.Minute)
			},
		},
		{
			name:        "closure epoch differs from audience",
			configure:   func(f *directNoticeFixture) { f.closure.epoch++ },
			wantClosure: 1, wantAttestor: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeFixture(t)
			test.configure(&fixture)
			beforeGuard := directNoticeDeliveryGuard(t, fixture)
			beforeAudit := directNoticeAuditHead(t, fixture)
			_, err := fixture.m.publishDirectNotice(
				context.Background(), fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender},
				fixture.command(model.NewID(), "stale-authority"),
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("stale authority error = %v, want UNKNOWN", err)
			}
			if fixture.closure.calls.Load() != test.wantClosure ||
				fixture.attestor.calls.Load() != test.wantAttestor {
				t.Fatalf("stale authority closure/attestor calls = %d/%d, want %d/%d",
					fixture.closure.calls.Load(), fixture.attestor.calls.Load(),
					test.wantClosure, test.wantAttestor)
			}
			afterGuard := directNoticeDeliveryGuard(t, fixture)
			afterAudit := directNoticeAuditHead(t, fixture)
			if afterGuard.Version != beforeGuard.Version || afterGuard.NextSeq != beforeGuard.NextSeq ||
				afterAudit.Seq != beforeAudit.Seq || !bytes.Equal(afterAudit.Hash, beforeAudit.Hash) {
				t.Fatalf("stale authority changed state: guard %+v -> %+v, audit %+v -> %+v",
					beforeGuard, afterGuard, beforeAudit, afterAudit)
			}
		})
	}
}

func TestPublishDirectNoticeResamplesDBTimeAfterEveryBlockingLock(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "authority expires while waiting")
	normalized, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize direct notice: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, normalized, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight direct notice: %v", err)
	}
	beforeGuard := directNoticeDeliveryGuard(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture)
	advanced := model.NewTimestamp(preflight.Snapshot.FreshUntil.Add(time.Second))
	var order []string
	err = fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
		lockAuditAppends := tx.lockAuditAppendsFn
		tx.lockAuditAppendsFn = func(ctx context.Context) error {
			order = append(order, "audit-append-lock")
			return lockAuditAppends(ctx)
		}
		tx.observeNowFn = func(context.Context) (model.Timestamp, error) {
			order = append(order, "db-now")
			return advanced, nil
		}
		_, gap, applyErr := applyDirectNoticePublish(context.Background(), tx, preflight)
		if gap {
			return errors.New("unexpected audit gap")
		}
		return applyErr
	})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("authority expiry after lock wait = %v, want UNKNOWN", err)
	}
	if len(order) != 2 || order[0] != "audit-append-lock" || order[1] != "db-now" {
		t.Fatalf("final freshness order = %v, want audit append lock then DBNow", order)
	}
	if got := directNoticeDeliveryGuard(t, fixture); got != beforeGuard {
		t.Fatalf("authority expiry changed guard: before=%+v after=%+v", beforeGuard, got)
	}
	if got := directNoticeAuditHead(t, fixture); got.Seq != beforeAudit.Seq ||
		!bytes.Equal(got.Hash, beforeAudit.Hash) {
		t.Fatalf("authority expiry changed audit: before=%+v after=%+v", beforeAudit, got)
	}
	for _, kind := range []model.Kind{
		messageKind, messageAudienceKind, messageAudienceRecipientKind,
		messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
			t.Fatalf("authority expiry persisted %d %s rows", len(rows), kind)
		}
	}
}

func TestPublishDirectNoticeSamplesGrantEvaluationAfterResolver(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatal("direct notice fixture clock is not controllable")
	}
	fixture.closure.now = fixture.now.Add(time.Millisecond)
	fixture.closure.beforeReturn = func() { clock.advance(2 * time.Millisecond) }
	result, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), "resolver-clock-order"),
	)
	if err != nil || result.Code != "accepted" {
		t.Fatalf("resolver-timestamped grant evidence = (%+v, %v), want accepted", result, err)
	}
}

func TestDirectNoticeExpectedPlanHashHasOneCanonicalRequestSpelling(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	digest := strings.Repeat("ab", sha256.Size)
	first := fixture.command(model.NewID(), "plan-hash-canonical")
	first.ExpectedPlanHash = "sha256:" + strings.ToUpper(digest)
	second := first
	second.ExpectedPlanHash = digest
	firstNormalized, _, _, firstRequest, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, first,
	)
	if err != nil {
		t.Fatalf("normalize prefixed expected plan hash: %v", err)
	}
	secondNormalized, _, _, secondRequest, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, second,
	)
	if err != nil {
		t.Fatalf("normalize canonical expected plan hash: %v", err)
	}
	if firstNormalized.ExpectedPlanHash != digest || secondNormalized.ExpectedPlanHash != digest ||
		!bytes.Equal(firstRequest, secondRequest) {
		t.Fatalf("expected plan hash aliases diverged: %q/%q, %x/%x",
			firstNormalized.ExpectedPlanHash, secondNormalized.ExpectedPlanHash,
			firstRequest, secondRequest)
	}
}

func TestPublishDirectNoticeAcceptsOpaqueUUIDIdempotencyKeyAcrossVersions(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "opaque uuid idempotency key")
	command.IdempotencyKey = "550e8400-e29b-41d4-a716-446655440000"
	first, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("publish with UUIDv4 idempotency key: %v", err)
	}
	replay, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	)
	if err != nil || !replay.Replayed || replay.MessageID != first.MessageID ||
		replay.CommandID != first.CommandID {
		t.Fatalf("UUIDv4 idempotency replay = (%+v, %v), first %+v", replay, err, first)
	}
	malformed := command
	malformed.IdempotencyKey = "not-a-uuid"
	if _, _, _, _, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, malformed,
	); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("malformed idempotency key error = %v, want invalid model", err)
	}
}

func TestDirectNoticeActiveGrantPaginationUsesKeysetCursor(t *testing.T) {
	t.Parallel()

	channelID := model.NewID()
	first := make([]model.Record, directNoticeGrantPageSize)
	for index := range first {
		first[index] = model.Record{model.ColID: model.NewID().String()}
	}
	last := []model.Record{{model.ColID: model.NewID().String()}}
	lister := directNoticePagedGrantLister{pages: map[string]directNoticeGrantPage{
		"":     {rows: first, page: model.Page{HasMore: true, Cursor: "next"}},
		"next": {rows: last},
	}}
	rows, err := listDirectNoticeActiveGrantRecords(context.Background(), lister, channelID)
	if err != nil || len(rows) != directNoticeGrantPageSize+1 {
		t.Fatalf("keyset grant pagination = %d rows, %v", len(rows), err)
	}
	_, err = listDirectNoticeActiveGrantRecords(
		context.Background(),
		directNoticePagedGrantLister{pages: map[string]directNoticeGrantPage{
			"": {rows: first, page: model.Page{HasMore: true}},
		}},
		channelID,
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("grant page without continuation = %v, want UNKNOWN", err)
	}
}

func TestDirectNoticeEventPayloadV1GoldenReplay(t *testing.T) {
	t.Parallel()

	result := DirectNoticePublishResult{
		ChannelID: model.ID("018f0000-0000-7000-8000-000000000001"),
		MessageID: model.ID("018f0000-0000-7000-8000-000000000002"),
		Version:   2, DeliveryCount: 1, RequiredCount: 1, AckQuorum: 1,
		Fulfillment: FulfillmentProjection{
			State: FulfillmentPending, Required: 1, Viable: 1, Quorum: 1,
		},
		AudienceHash: strings.Repeat("a", 64), PayloadDigest: strings.Repeat("b", 64),
		PlanHash: strings.Repeat("c", 64),
	}
	raw, err := canonicalDirectNoticeEventPayload(result)
	if err != nil {
		t.Fatalf("encode v1 direct notice Event: %v", err)
	}
	const golden = `{"ack_quorum":1,"audience_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","channel_id":"018f0000-0000-7000-8000-000000000001","command":"message.publish.direct","delivery_count":1,"event_sequence":1,"fulfillment":{"acknowledged":0,"quorum":1,"required":1,"state":"pending","unmet":0,"viable":1},"message_id":"018f0000-0000-7000-8000-000000000002","message_kind":"notice","payload_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","plan_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","required_count":1,"result_id":"018f0000-0000-7000-8000-000000000002","result_kind":"sessions.message","schema_version":1,"state":"published","version":2}`
	if string(raw) != golden {
		t.Fatalf("v1 direct notice Event bytes changed:\n got %s\nwant %s", raw, golden)
	}
	decoded, err := decodeDirectNoticeEventPayload([]byte(golden))
	if err != nil {
		t.Fatalf("decode retained v1 direct notice Event: %v", err)
	}
	if decoded != directNoticeEventPayloadV1FromResult(result) {
		t.Fatalf("retained v1 Event = %+v, want %+v", decoded, directNoticeEventPayloadV1FromResult(result))
	}
}

func TestDirectNoticeReplayRejectsCrossSliceAndTemporalAnchors(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "strict replay body")
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	); err != nil {
		t.Fatalf("seed strict replay graph: %v", err)
	}
	normalized, _, _, _, err := normalizeDirectNoticePublishCommand(fixture.scope, principal, command)
	if err != nil {
		t.Fatalf("normalize strict replay command: %v", err)
	}
	receipt, err := communicationCommandReceiptFromRecord(
		communicationRowsForTest(t, fixture, communicationCommandKind)[0],
	)
	if err != nil {
		t.Fatalf("decode strict replay receipt: %v", err)
	}
	baseRows := map[model.Kind][]model.Record{
		messageKind:                  communicationRowsForTest(t, fixture, messageKind),
		messageAudienceKind:          communicationRowsForTest(t, fixture, messageAudienceKind),
		messageAudienceRecipientKind: communicationRowsForTest(t, fixture, messageAudienceRecipientKind),
		messageDeliveryKind:          communicationRowsForTest(t, fixture, messageDeliveryKind),
		messageAckKind:               nil,
		workEventKind:                communicationRowsForTest(t, fixture, workEventKind),
		workOutboxKind:               communicationRowsForTest(t, fixture, workOutboxKind),
	}
	resolver := func(rows map[model.Kind][]model.Record) communicationReadRepositoryResolver {
		return func(kind model.Kind) (communicationReadRepository, error) {
			values, present := rows[kind]
			if !present {
				return nil, store.ErrNotFound
			}
			return directNoticeReplayRepository{rows: values}, nil
		}
	}
	if _, err := directNoticeResultFromReceipt(
		context.Background(), resolver(baseRows), nil, principal, normalized, receipt,
	); err != nil {
		t.Fatalf("strict replay control: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CommunicationCommandReceipt, map[model.Kind][]model.Record)
	}{
		{name: "keyed receipt", mutate: func(receipt *CommunicationCommandReceipt, _ map[model.Kind][]model.Record) {
			receipt.SealKeyVersion, receipt.DigestKeyVersion = "seal-v1", "digest-v1"
		}},
		{name: "non-notice message", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageKind][0][colCommKind] = string(MessageAnnouncement)
		}},
		{name: "work-item message", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageKind][0][colWorkItemID] = model.NewID().String()
		}},
		{name: "message lifecycle version", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageKind][0][model.ColVersion] = int64(999)
		}},
		{name: "message expiry", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageKind][0][colCommExpiresAt] = model.NewTimestamp(receipt.CompletedAt.Add(time.Hour)).String()
		}},
		{name: "delivery route reasons", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageDeliveryKind][0][colCommRouteReasonsJSON] = `["other"]`
		}},
		{name: "delivery sequence", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageDeliveryKind][0][colCommDeliverySeq] = rows[messageDeliveryKind][0].Int(colCommDeliverySeq) + 1
		}},
		{name: "delivery acknowledged without Ack evidence", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			updated := model.NewTimestamp(receipt.CompletedAt.Add(time.Second)).String()
			rows[messageDeliveryKind][0][colCommState] = string(DeliveryAcknowledged)
			rows[messageDeliveryKind][0][model.ColVersion] = int64(2)
			rows[messageDeliveryKind][0][model.ColUpdatedAt] = updated
			rows[messageDeliveryKind][0][colCommAckID] = model.NewID().String()
			rows[messageDeliveryKind][0][colCommAcknowledgedAt] = updated
		}},
		{name: "available delivery with Ack evidence", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			ackAt := receipt.CompletedAt.Add(time.Second)
			ackRecord, encodeErr := messageAckToRecord(MessageAck{
				AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
					CommunicationEntity: CommunicationEntity{
						ID: model.NewID(), TenantID: fixture.scope.TenantID,
						WorkspaceID: fixture.scope.WorkspaceID, Version: 1, CreatedAt: ackAt,
					},
				},
				DeliveryID: model.ID(rows[messageDeliveryKind][0].String(model.ColID)),
				Kind:       MessageAckReceived, Late: true,
				Actor:          CommunicationActorRef{Kind: ActorUser, Ref: fixture.recipient.String()},
				AcknowledgedAt: ackAt,
			})
			if encodeErr != nil {
				t.Fatalf("encode spurious MessageAck: %v", encodeErr)
			}
			rows[messageAckKind] = []model.Record{ackRecord}
		}},
		{name: "extra delivery", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			extra := workSchemaClone(rows[messageDeliveryKind][0])
			extra[model.ColID] = model.NewID().String()
			rows[messageDeliveryKind] = append(rows[messageDeliveryKind], extra)
		}},
		{name: "missing audience contribution", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageAudienceRecipientKind] = nil
		}},
		{name: "relabelled audience contribution id", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			rows[messageAudienceRecipientKind][0][model.ColID] = model.NewID().String()
		}},
		{name: "unknown event payload version", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			payload, decodeErr := decodeDirectNoticeEventPayload(
				[]byte(rows[workEventKind][0].String(colEventPayload)),
			)
			if decodeErr != nil {
				t.Fatalf("decode Event payload version mutant: %v", decodeErr)
			}
			payload.SchemaVersion++
			raw, encodeErr := canonicalJSON(payload)
			if encodeErr != nil {
				t.Fatalf("encode Event payload version mutant: %v", encodeErr)
			}
			rows[workEventKind][0][colEventPayload] = string(raw)
			rows[workEventKind][0][colEventPayloadHash] = hashBytes(raw)
		}},
		{name: "sealed message", mutate: func(_ *CommunicationCommandReceipt, rows map[model.Kind][]model.Record) {
			message, decodeErr := messageFromRecord(rows[messageKind][0], 0)
			if decodeErr != nil {
				t.Fatalf("decode message for sealed replay mutant: %v", decodeErr)
			}
			message.Payload.Encoding = PayloadSealedV1
			message.Payload.PlainJSON = nil
			message.Payload.Sealed = &SealedPayload{Ciphertext: []byte("ciphertext"), KeyVersion: "seal-v1"}
			message.Payload.SealKeyVersion = "seal-v1"
			message.Payload.DigestKeyVersion = "digest-v1"
			record, encodeErr := messageToRecord(message, 0)
			if encodeErr != nil {
				t.Fatalf("encode sealed replay mutant: %v", encodeErr)
			}
			rows[messageKind][0] = record
		}},
		{name: "receipt time", mutate: func(receipt *CommunicationCommandReceipt, _ map[model.Kind][]model.Record) {
			receipt.CompletedAt = receipt.CompletedAt.Add(time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := make(map[model.Kind][]model.Record, len(baseRows))
			for kind, values := range baseRows {
				rows[kind] = make([]model.Record, len(values))
				for index, row := range values {
					rows[kind][index] = workSchemaClone(row)
				}
			}
			mutatedReceipt := receipt
			test.mutate(&mutatedReceipt, rows)
			if _, err := directNoticeResultFromReceipt(
				context.Background(), resolver(rows), nil, principal, normalized, mutatedReceipt,
			); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("%s replay error = %v, want UNKNOWN", test.name, err)
			}
		})
	}
}

func TestDirectNoticeReplaySurvivesLegalDeliveryAndMessageLifecycle(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixtureWithOptions(t, AckPolicyEachRequired, 0)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "durable lifecycle replay")
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	); err != nil {
		t.Fatalf("seed lifecycle replay graph: %v", err)
	}
	normalized, _, _, _, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize lifecycle replay command: %v", err)
	}
	receipt, err := communicationCommandReceiptFromRecord(
		communicationRowsForTest(t, fixture, communicationCommandKind)[0],
	)
	if err != nil {
		t.Fatalf("decode lifecycle replay receipt: %v", err)
	}
	baseRows := map[model.Kind][]model.Record{
		messageKind:                  communicationRowsForTest(t, fixture, messageKind),
		messageAudienceKind:          communicationRowsForTest(t, fixture, messageAudienceKind),
		messageAudienceRecipientKind: communicationRowsForTest(t, fixture, messageAudienceRecipientKind),
		messageDeliveryKind:          communicationRowsForTest(t, fixture, messageDeliveryKind),
		messageAckKind:               nil,
		workEventKind:                communicationRowsForTest(t, fixture, workEventKind),
		workOutboxKind:               communicationRowsForTest(t, fixture, workOutboxKind),
	}
	cloneRows := func() map[model.Kind][]model.Record {
		rows := make(map[model.Kind][]model.Record, len(baseRows)+1)
		for kind, values := range baseRows {
			rows[kind] = make([]model.Record, len(values))
			for index, row := range values {
				rows[kind][index] = workSchemaClone(row)
			}
		}
		return rows
	}
	resolver := func(rows map[model.Kind][]model.Record) communicationReadRepositoryResolver {
		return func(kind model.Kind) (communicationReadRepository, error) {
			values, present := rows[kind]
			if !present {
				return nil, store.ErrNotFound
			}
			return directNoticeReplayRepository{rows: values}, nil
		}
	}
	assertReplayWithDirectory := func(
		t *testing.T,
		rows map[model.Kind][]model.Record,
		directory store.DirectorySnapshotReader,
	) {
		t.Helper()
		result, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), directory, principal, normalized, receipt,
		)
		if replayErr != nil {
			t.Fatalf("replay after legal lifecycle: %v", replayErr)
		}
		if result.State != MessagePublished || result.Version != 2 ||
			result.MessageID != receipt.ResultID {
			t.Fatalf("replayed original projection = %+v", result)
		}
	}
	assertReplay := func(t *testing.T, rows map[model.Kind][]model.Record) {
		t.Helper()
		assertReplayWithDirectory(t, rows, nil)
	}

	t.Run("effective Ack", func(t *testing.T) {
		rows := cloneRows()
		ackID := model.NewID()
		ackAt := receipt.CompletedAt.Add(30 * time.Second)
		rows[messageDeliveryKind][0][colCommState] = string(DeliveryAcknowledged)
		rows[messageDeliveryKind][0][model.ColVersion] = int64(2)
		rows[messageDeliveryKind][0][model.ColUpdatedAt] = model.NewTimestamp(ackAt).String()
		rows[messageDeliveryKind][0][colCommAckID] = ackID.String()
		rows[messageDeliveryKind][0][colCommAcknowledgedAt] = model.NewTimestamp(ackAt).String()
		ackRecord, encodeErr := messageAckToRecord(MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: ackID, TenantID: fixture.scope.TenantID,
					WorkspaceID: fixture.scope.WorkspaceID, Version: 1, CreatedAt: ackAt,
				},
			},
			DeliveryID: model.ID(rows[messageDeliveryKind][0].String(model.ColID)), Kind: MessageAckReceived,
			Actor:          CommunicationActorRef{Kind: ActorUser, Ref: fixture.recipient.String()},
			AcknowledgedAt: ackAt,
		})
		if encodeErr != nil {
			t.Fatalf("encode effective MessageAck: %v", encodeErr)
		}
		rows[messageAckKind] = []model.Record{ackRecord}
		assertReplay(t, rows)

		rows[messageAckKind][0][colCommActorRef] = model.NewID().String()
		if _, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), nil, principal, normalized, receipt,
		); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("Ack by a different recipient replay = %v, want UNKNOWN", replayErr)
		}
		wrongRecipient := RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()}
		note := communicationTestPayloadForSlot(t, PayloadSlotAckNote)
		ackRecord, encodeErr = messageAckToRecord(MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: ackID, TenantID: fixture.scope.TenantID,
					WorkspaceID: fixture.scope.WorkspaceID, Version: 1, CreatedAt: ackAt,
				},
			},
			DeliveryID: model.ID(rows[messageDeliveryKind][0].String(model.ColID)),
			Kind:       MessageAckReceived,
			Actor:      CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()},
			OnBehalfOf: &wrongRecipient, Note: &note, AcknowledgedAt: ackAt,
		})
		if encodeErr != nil {
			t.Fatalf("encode mismatched on-behalf MessageAck: %v", encodeErr)
		}
		rows[messageAckKind] = []model.Record{ackRecord}
		if _, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), nil, principal, normalized, receipt,
		); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("Ack on behalf of a different recipient replay = %v, want UNKNOWN", replayErr)
		}
	})

	t.Run("delivery expiry", func(t *testing.T) {
		rows := cloneRows()
		due, parseErr := model.ParseTimestamp(rows[messageDeliveryKind][0].String(colCommAckDueAt))
		if parseErr != nil {
			t.Fatalf("parse delivery Ack deadline: %v", parseErr)
		}
		rows[messageDeliveryKind][0][colCommState] = string(DeliveryExpired)
		rows[messageDeliveryKind][0][model.ColVersion] = int64(2)
		rows[messageDeliveryKind][0][model.ColUpdatedAt] = due.String()
		lateAckAt := due.Time().Add(time.Second)
		lateAckRecord, encodeErr := messageAckToRecord(MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: model.NewID(), TenantID: fixture.scope.TenantID,
					WorkspaceID: fixture.scope.WorkspaceID, Version: 1, CreatedAt: lateAckAt,
				},
			},
			DeliveryID: model.ID(rows[messageDeliveryKind][0].String(model.ColID)),
			Kind:       MessageAckReceived, Late: true,
			Actor:          CommunicationActorRef{Kind: ActorUser, Ref: fixture.recipient.String()},
			AcknowledgedAt: lateAckAt,
		})
		if encodeErr != nil {
			t.Fatalf("encode late MessageAck: %v", encodeErr)
		}
		rows[messageAckKind] = []model.Record{lateAckRecord}
		assertReplay(t, rows)

		tooEarly := model.NewTimestamp(due.Time().Add(-time.Second)).String()
		rows[messageAckKind][0][model.ColCreatedAt] = tooEarly
		rows[messageAckKind][0][colCommAcknowledgedAt] = tooEarly
		if _, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), nil, principal, normalized, receipt,
		); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("pre-deadline late Ack replay = %v, want UNKNOWN", replayErr)
		}
	})

	t.Run("message retraction", func(t *testing.T) {
		rows := cloneRows()
		terminalAt := receipt.CompletedAt.Add(30 * time.Second)
		terminalStamp := model.NewTimestamp(terminalAt).String()
		rows[messageKind][0][colCommState] = string(MessageRetracted)
		rows[messageKind][0][model.ColVersion] = int64(3)
		rows[messageKind][0][model.ColUpdatedAt] = terminalStamp
		rows[messageKind][0][colCommTerminalAt] = terminalStamp
		rows[messageKind][0][colCommTerminalCode] = "sender_retracted"
		rows[messageKind][0][colCommLastEventSeq] = int64(2)
		rows[messageDeliveryKind][0][colCommState] = string(DeliveryRetracted)
		rows[messageDeliveryKind][0][model.ColVersion] = int64(3)
		seenAt := model.NewTimestamp(terminalAt.Add(time.Second)).String()
		rows[messageDeliveryKind][0][model.ColUpdatedAt] = seenAt
		rows[messageDeliveryKind][0][colCommFirstSeenAt] = seenAt
		lateAckAt := terminalAt.Add(time.Second)
		lateAckRecord, encodeErr := messageAckToRecord(MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: model.NewID(), TenantID: fixture.scope.TenantID,
					WorkspaceID: fixture.scope.WorkspaceID, Version: 1, CreatedAt: lateAckAt,
				},
			},
			DeliveryID: model.ID(rows[messageDeliveryKind][0].String(model.ColID)),
			Kind:       MessageAckReceived, Late: true,
			Actor:          CommunicationActorRef{Kind: ActorUser, Ref: fixture.recipient.String()},
			AcknowledgedAt: lateAckAt,
		})
		if encodeErr != nil {
			t.Fatalf("encode post-retraction MessageAck: %v", encodeErr)
		}
		rows[messageAckKind] = []model.Record{lateAckRecord}
		assertReplay(t, rows)

		tooEarly := model.NewTimestamp(terminalAt.Add(-time.Second)).String()
		rows[messageAckKind][0][model.ColCreatedAt] = tooEarly
		rows[messageAckKind][0][colCommAcknowledgedAt] = tooEarly
		if _, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), nil, principal, normalized, receipt,
		); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("pre-retraction late Ack replay = %v, want UNKNOWN", replayErr)
		}
	})

	t.Run("undeliverable with exact tombstone", func(t *testing.T) {
		rows := cloneRows()
		retiredAt := receipt.CompletedAt.Add(30 * time.Second)
		retiredStamp := model.NewTimestamp(retiredAt).String()
		tombstoneID := model.NewID()
		retirementEpoch := fixture.epoch + 1
		rows[messageDeliveryKind][0][colCommState] = string(DeliveryUndeliverable)
		rows[messageDeliveryKind][0][model.ColVersion] = int64(2)
		rows[messageDeliveryKind][0][model.ColUpdatedAt] = retiredStamp
		rows[messageDeliveryKind][0][colCommRetirementTombstoneKind] = string(model.UserTombstoneKind)
		rows[messageDeliveryKind][0][colCommRetirementTombstoneID] = tombstoneID.String()
		rows[messageDeliveryKind][0][colCommRetirementTombstoneVersion] = int64(1)
		rows[messageDeliveryKind][0][colCommRetirementEpoch] = retirementEpoch
		rows[messageDeliveryKind][0][colCommUndeliverableAt] = retiredStamp
		rows[messageDeliveryKind][0][colCommUndeliverableCode] = "recipient_retired"
		principalRef := store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: fixture.recipient,
		}
		reader := directNoticeReplayDirectoryReader{found: true, witness: store.DirectoryTombstoneWitness{
			TombstoneKind: model.UserTombstoneKind, TombstoneID: tombstoneID,
			TombstoneVersion: 1, Principal: principalRef, RetirementEpoch: retirementEpoch,
		}}
		assertReplayWithDirectory(t, rows, reader)
		reader.witness.TombstoneID = model.NewID()
		if _, replayErr := directNoticeResultFromReceipt(
			context.Background(), resolver(rows), reader, principal, normalized, receipt,
		); !errors.Is(replayErr, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("mismatched tombstone replay = %v, want UNKNOWN", replayErr)
		}
	})
}

func TestDirectNoticeReplayRequiresExactAuditAnchor(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	result, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal,
		fixture.command(model.NewID(), "audit-anchor"),
	)
	if err != nil {
		t.Fatalf("seed audit-anchor replay: %v", err)
	}
	receipt, err := communicationCommandReceiptFromRecord(
		communicationRowsForTest(t, fixture, communicationCommandKind)[0],
	)
	if err != nil {
		t.Fatalf("decode audit-anchor receipt: %v", err)
	}
	event := directNoticeAuditEvent(t, fixture, result.AuditSeq)
	applyCommitment, err := directNoticeApplyCommitmentFromReceipt(receipt)
	if err != nil {
		t.Fatalf("direct notice apply commitment: %v", err)
	}
	meta, err := canonicalJSON(map[string]any{
		"workspace_id":              fixture.scope.WorkspaceID.String(),
		"workspace_binding_version": int64(1),
		"channel_id":                fixture.channel.ID.String(), "command_scope": receipt.CommandScope,
		"delivery_count":           int64(1),
		"required_count":           receipt.ResponseProjectionJSON.Counts["required"],
		"apply_commitment_version": directNoticeApplyCommitmentV1Version,
		"apply_commitment":         hex.EncodeToString(applyCommitment),
	})
	if err != nil {
		t.Fatalf("canonical audit anchor metadata: %v", err)
	}
	if err := verifyDirectNoticeAuditAnchor(
		context.Background(), directNoticeReplayAuditLog{
			events: []model.AuditEvent{event}, meta: string(meta),
		},
		fixture.scope, principal, receipt,
	); err != nil {
		t.Fatalf("exact audit anchor: %v", err)
	}
	for name, log := range map[string]store.AuditLog{
		"missing": directNoticeReplayAuditLog{meta: string(meta)},
		"read failure": directNoticeReplayAuditLog{
			events: []model.AuditEvent{event}, meta: string(meta),
			anchorErr: errors.New("audit unavailable"),
		},
		"broken chain": directNoticeReplayAuditLog{
			events: []model.AuditEvent{event}, meta: string(meta),
			anchorErr: errors.New("audit chain mismatch"),
		},
		"wrong workspace metadata": directNoticeReplayAuditLog{
			events: []model.AuditEvent{event}, meta: `{"workspace_id":"wrong"}`,
		},
		"wrong apply commitment version": directNoticeReplayAuditLog{
			events: []model.AuditEvent{event},
			meta: strings.Replace(
				string(meta), `"apply_commitment_version":1`, `"apply_commitment_version":2`, 1,
			),
		},
		"wrong action": func() store.AuditLog {
			changed := event
			changed.Action = "sessions.communication.message.other"
			return directNoticeReplayAuditLog{events: []model.AuditEvent{changed}, meta: string(meta)}
		}(),
		"wrong hash": func() store.AuditLog {
			changed := event
			changed.Hash = hashBytes([]byte("wrong audit anchor"))
			return directNoticeReplayAuditLog{events: []model.AuditEvent{changed}, meta: string(meta)}
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyDirectNoticeAuditAnchor(
				context.Background(), log, fixture.scope, principal, receipt,
			); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("audit anchor error = %v, want UNKNOWN", err)
			}
		})
	}

	for name, mutate := range map[string]func(*CommunicationCommandReceipt){
		"idempotency key": func(value *CommunicationCommandReceipt) {
			value.IdempotencyKeyHash = hashBytes([]byte("different idempotency key"))
		},
		"receipt id": func(value *CommunicationCommandReceipt) { value.ID = model.NewID() },
		"message id": func(value *CommunicationCommandReceipt) {
			value.ResultID = model.NewID()
			value.ResponseProjectionJSON.IDs = cloneDirectNoticeIDMap(value.ResponseProjectionJSON.IDs)
			value.ResponseProjectionJSON.IDs["message_id"] = value.ResultID
		},
		"delivery id": func(value *CommunicationCommandReceipt) {
			value.ResponseProjectionJSON.IDs = cloneDirectNoticeIDMap(value.ResponseProjectionJSON.IDs)
			value.ResponseProjectionJSON.IDs["delivery_id"] = model.NewID()
		},
		"event id": func(value *CommunicationCommandReceipt) {
			value.EventID = model.NewID()
			value.ResponseProjectionJSON.IDs = cloneDirectNoticeIDMap(value.ResponseProjectionJSON.IDs)
			value.ResponseProjectionJSON.IDs["event_id"] = value.EventID
		},
		"completed time": func(value *CommunicationCommandReceipt) {
			value.CompletedAt = value.CompletedAt.Add(time.Second)
		},
	} {
		t.Run("audit apply commitment rejects "+name, func(t *testing.T) {
			mutatedReceipt := receipt
			mutate(&mutatedReceipt)
			if err := verifyDirectNoticeAuditAnchor(
				context.Background(), directNoticeReplayAuditLog{
					events: []model.AuditEvent{event}, meta: string(meta),
				}, fixture.scope, principal, mutatedReceipt,
			); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("relabelled receipt error = %v, want UNKNOWN", err)
			}
		})
	}
}

func cloneDirectNoticeIDMap(input map[string]model.ID) map[string]model.ID {
	result := make(map[string]model.ID, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func TestDirectNoticePlanHashIgnoresObservationTimeAndServerIDsButBindsAuthority(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	makePreflight := func(idempotency model.ID) directNoticePublishPreflight {
		t.Helper()
		cmd, actor, idem, request, err := normalizeDirectNoticePublishCommand(
			fixture.scope, principal, fixture.command(idempotency, "stable semantic plan"),
		)
		if err != nil {
			t.Fatalf("normalize semantic plan command: %v", err)
		}
		preflight, err := fixture.m.preflightDirectNoticePublish(
			context.Background(), fixture.scope, principal, cmd, actor, idem, request,
		)
		if err != nil {
			t.Fatalf("semantic plan preflight: %v", err)
		}
		return preflight
	}
	first := makePreflight(model.NewID())
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatal("direct notice fixture does not expose its deterministic module clock")
	}
	clock.advance(time.Second)
	second := makePreflight(model.NewID())
	if first.IDs == second.IDs || first.Snapshot.ObservedAt == second.Snapshot.ObservedAt {
		t.Fatal("semantic stability control did not vary IDs and observer time")
	}

	var stableFirst, stableSecond, senderChanged, routeGuardChanged, deliveryGuardChanged, contentChanged []byte
	err := fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
		facts, err := directNoticePublishAuthorityFacts(first)
		if err != nil {
			return err
		}
		if err := tx.lockAuthoritySnapshot(context.Background(), facts); err != nil {
			return err
		}
		locked, err := lockDirectNoticePublishState(context.Background(), tx, first)
		if err != nil {
			return err
		}
		firstPlan, err := materializeDirectNoticePublish(tx.now.Time(), first, locked)
		if err != nil {
			return err
		}
		secondPlan, err := materializeDirectNoticePublish(tx.now.Time(), second, locked)
		if err != nil {
			return err
		}
		if stableFirst, err = canonicalDirectNoticePublishPlanHash(first, locked, firstPlan); err != nil {
			return err
		}
		if stableSecond, err = canonicalDirectNoticePublishPlanHash(second, locked, secondPlan); err != nil {
			return err
		}
		changedSender := first
		changedSender.Sender = CommunicationActorRef{Kind: ActorUser, Ref: model.NewID().String()}
		if senderChanged, err = canonicalDirectNoticePublishPlanHash(changedSender, locked, firstPlan); err != nil {
			return err
		}
		changedGuard := locked
		changedGuard.RouteGuard.NextSeq++
		if routeGuardChanged, err = canonicalDirectNoticePublishPlanHash(first, changedGuard, firstPlan); err != nil {
			return err
		}
		changedGuard = locked
		changedGuard.DeliveryGuard.NextSeq++
		if deliveryGuardChanged, err = canonicalDirectNoticePublishPlanHash(first, changedGuard, firstPlan); err != nil {
			return err
		}
		changedContent := first
		changedContent.Command.Content.Blocks[0].Text = "different semantic content"
		contentChanged, err = canonicalDirectNoticePublishPlanHash(changedContent, locked, firstPlan)
		return err
	})
	if err != nil {
		t.Fatalf("compute semantic plan hashes: %v", err)
	}
	if !bytes.Equal(stableFirst, stableSecond) {
		t.Fatalf("semantic plan hash changed with observer time/server IDs: %x != %x", stableFirst, stableSecond)
	}
	if !bytes.Equal(stableFirst, routeGuardChanged) {
		t.Fatalf("semantic plan hash changed with unrelated workspace route allocation: %x != %x",
			stableFirst, routeGuardChanged)
	}
	for name, changed := range map[string][]byte{
		"sender": senderChanged, "delivery guard": deliveryGuardChanged, "content": contentChanged,
	} {
		if bytes.Equal(stableFirst, changed) {
			t.Fatalf("semantic plan hash did not bind %s", name)
		}
	}
}

func TestPublishDirectNoticeEnforcesExpectedPlanHashBeforeEffects(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "expected plan precondition")
	exact := directNoticePlanHashForTest(t, fixture, principal, command)
	beforeGuard := directNoticeDeliveryGuard(t, fixture)
	beforeAudit := directNoticeAuditHead(t, fixture)

	wrong := command
	wrong.ExpectedPlanHash = strings.Repeat("00", sha256.Size)
	if _, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, wrong,
	); !errors.Is(err, ErrCommunicationPlanChanged) || errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong expected plan hash = %v, want typed plan change distinct from conflict", err)
	}
	if got := directNoticeDeliveryGuard(t, fixture); got != beforeGuard {
		t.Fatalf("plan precondition advanced guard: before=%+v after=%+v", beforeGuard, got)
	}
	if got := directNoticeAuditHead(t, fixture); got.Seq != beforeAudit.Seq ||
		!bytes.Equal(got.Hash, beforeAudit.Hash) {
		t.Fatalf("plan precondition changed audit: before=%+v after=%+v", beforeAudit, got)
	}
	for _, kind := range []model.Kind{
		messageKind, messageAudienceKind, messageAudienceRecipientKind, messageDeliveryKind,
		workEventKind, workOutboxKind, communicationCommandKind,
	} {
		if rows := communicationRowsForTest(t, fixture, kind); len(rows) != 0 {
			t.Fatalf("plan precondition persisted %d %s rows", len(rows), kind)
		}
	}

	command.ExpectedPlanHash = hex.EncodeToString(exact)
	result, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope, principal, command,
	)
	if err != nil || result.PlanHash != command.ExpectedPlanHash {
		t.Fatalf("exact expected plan hash = (%+v, %v), want accepted %s",
			result, err, command.ExpectedPlanHash)
	}
}

func directNoticePlanHashForTest(
	t *testing.T,
	fixture directNoticeFixture,
	principal CommunicationPrincipal,
	command DirectNoticePublishCommand,
) []byte {
	t.Helper()
	normalized, actor, idem, request, err := normalizeDirectNoticePublishCommand(
		fixture.scope, principal, command,
	)
	if err != nil {
		t.Fatalf("normalize direct notice plan: %v", err)
	}
	preflight, err := fixture.m.preflightDirectNoticePublish(
		context.Background(), fixture.scope, principal, normalized, actor, idem, request,
	)
	if err != nil {
		t.Fatalf("preflight direct notice plan: %v", err)
	}
	rollback := errors.New("rollback plan observation")
	var result []byte
	err = fixture.m.mutateCommunication(context.Background(), fixture.scope, func(tx *communicationTx) error {
		facts, factsErr := directNoticePublishAuthorityFacts(preflight)
		if factsErr != nil {
			return factsErr
		}
		if lockErr := tx.lockAuthoritySnapshot(context.Background(), facts); lockErr != nil {
			return lockErr
		}
		locked, lockErr := lockDirectNoticePublishState(
			context.Background(), tx, preflight,
		)
		if lockErr != nil {
			return lockErr
		}
		plan, planErr := materializeDirectNoticePublish(tx.now.Time(), preflight, locked)
		if planErr != nil {
			return planErr
		}
		result, planErr = canonicalDirectNoticePublishPlanHash(preflight, locked, plan)
		if planErr != nil {
			return planErr
		}
		return rollback
	})
	if !errors.Is(err, rollback) || len(result) != sha256.Size {
		t.Fatalf("observe direct notice plan = %x, %v", result, err)
	}
	return result
}

func TestPublishDirectNoticeConcurrentIdempotencyCommitsOneGraph(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	command := fixture.command(model.NewID(), "concurrent idempotency")
	beforeAudit := directNoticeAuditHead(t, fixture)

	type outcome struct {
		result DirectNoticePublishResult
		err    error
	}
	outcomes := make([]outcome, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range outcomes {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			outcomes[index].result, outcomes[index].err = fixture.m.publishDirectNotice(
				context.Background(), fixture.scope, principal, command,
			)
		}()
	}
	close(start)
	wait.Wait()
	for index, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent publish %d: %v", index, outcome.err)
		}
	}
	first, second := outcomes[0].result, outcomes[1].result
	firstReplay, secondReplay := first.Replayed, second.Replayed
	first.Replayed, second.Replayed = false, false
	if first != second || firstReplay == secondReplay {
		t.Fatalf("concurrent outcomes = %+v (replay=%v) / %+v (replay=%v)",
			first, firstReplay, second, secondReplay)
	}
	for kind, want := range map[model.Kind]int{
		messageKind: 1, messageAudienceKind: 1, messageAudienceRecipientKind: 1,
		messageDeliveryKind: 1, workEventKind: 1, workOutboxKind: 1,
		communicationCommandKind: 1,
	} {
		if got := len(communicationRowsForTest(t, fixture, kind)); got != want {
			t.Fatalf("concurrent publish %s rows = %d, want %d", kind, got, want)
		}
	}
	guard := directNoticeDeliveryGuard(t, fixture)
	afterAudit := directNoticeAuditHead(t, fixture)
	if guard.Version != 2 || guard.NextSeq != 2 || afterAudit.Seq != beforeAudit.Seq+1 {
		t.Fatalf("concurrent publish guard/audit = %+v / %+v, before audit %+v",
			guard, afterAudit, beforeAudit)
	}
}

func TestPublishDirectNoticeConcurrentKeyReuseRejectsDifferentIntent(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeFixture(t)
	principal := CommunicationPrincipal{UserID: fixture.sender}
	first := fixture.command(model.NewID(), "concurrent intent one")
	second := first
	second.Content.Blocks = append([]MessageContentBlock(nil), first.Content.Blocks...)
	second.Content.Blocks[0].Text = "concurrent intent two"
	beforeAudit := directNoticeAuditHead(t, fixture)

	type outcome struct {
		result DirectNoticePublishResult
		err    error
	}
	commands := []DirectNoticePublishCommand{first, second}
	outcomes := make([]outcome, len(commands))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range commands {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			outcomes[index].result, outcomes[index].err = fixture.m.publishDirectNotice(
				context.Background(), fixture.scope, principal, commands[index],
			)
		}()
	}
	close(start)
	wait.Wait()
	accepted, rejected := 0, 0
	for _, outcome := range outcomes {
		switch {
		case outcome.err == nil && outcome.result.Code == "accepted":
			accepted++
		case errors.Is(outcome.err, store.ErrConflict) &&
			strings.Contains(outcome.err.Error(), "idempotency_key_reused"):
			rejected++
		default:
			t.Fatalf("concurrent different-intent outcome = (%+v, %v)", outcome.result, outcome.err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent different-intent outcomes accepted/rejected = %d/%d", accepted, rejected)
	}
	for kind, want := range map[model.Kind]int{
		messageKind: 1, messageAudienceKind: 1, messageAudienceRecipientKind: 1,
		messageDeliveryKind: 1, workEventKind: 1, workOutboxKind: 1,
		communicationCommandKind: 1,
	} {
		if got := len(communicationRowsForTest(t, fixture, kind)); got != want {
			t.Fatalf("concurrent different-intent %s rows = %d, want %d", kind, got, want)
		}
	}
	guard := directNoticeDeliveryGuard(t, fixture)
	afterAudit := directNoticeAuditHead(t, fixture)
	if guard.Version != 2 || guard.NextSeq != 2 || afterAudit.Seq != beforeAudit.Seq+1 {
		t.Fatalf("concurrent different-intent guard/audit = %+v / %+v, before %+v",
			guard, afterAudit, beforeAudit)
	}
}

func directNoticeDeliveryGuard(t *testing.T, fixture directNoticeFixture) CommunicationGuard {
	t.Helper()
	for _, row := range communicationRowsForTest(t, fixture, communicationGuardKind) {
		guard, err := communicationGuardFromRecord(row)
		if err != nil {
			t.Fatalf("decode direct notice guard: %v", err)
		}
		if guard.Kind == CommunicationGuardDeliverySequence {
			return guard
		}
	}
	t.Fatal("delivery sequence guard missing")
	return CommunicationGuard{}
}

func directNoticeAuditHead(t *testing.T, fixture directNoticeFixture) store.HeadRef {
	t.Helper()
	var head store.HeadRef
	err := fixture.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		var found bool
		var err error
		head, found, err = sc.Audit().Head(context.Background())
		if err == nil && !found {
			return errors.New("audit head missing")
		}
		return err
	})
	if err != nil {
		t.Fatalf("read audit head: %v", err)
	}
	return head
}

func directNoticeAuditEvent(
	t *testing.T,
	fixture directNoticeFixture,
	sequence int64,
) model.AuditEvent {
	t.Helper()
	var found model.AuditEvent
	err := fixture.st.View(context.Background(), fixture.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), sequence, func(event model.AuditEvent) error {
			if event.Seq == sequence {
				found = event
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk audit event %d: %v", sequence, err)
	}
	if found.Seq != sequence {
		t.Fatalf("audit event %d not found", sequence)
	}
	return found
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := decodeHash(value, true)
	if err != nil {
		t.Fatalf("decode hash %q: %v", value, err)
	}
	return decoded
}

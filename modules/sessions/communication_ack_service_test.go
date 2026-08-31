// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	model.ID,
	DirectNoticeDeliveryAckCommand,
) (DirectNoticeDeliveryAckResult, error) = (*Module).AcknowledgeDirectNoticeDelivery

func TestDirectNoticeExactAckHasNoProductionCallerWhileOff(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate exact Ack test")
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
		file, parseErr := parser.ParseFile(files, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry.Name(), parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, selectorOK := node.(*ast.SelectorExpr)
			if !selectorOK {
				return true
			}
			if selector.Sel.Name == "AcknowledgeDirectNoticeDelivery" ||
				selector.Sel.Name == "acknowledgeDirectNoticeDeliveryWithAuthority" {
				t.Errorf(
					"OFF exact Ack seam referenced by production %s:%d",
					entry.Name(), files.Position(selector.Pos()).Line,
				)
			}
			return true
		})
	}
}

type directNoticeExactAckFixture struct {
	directNoticeFixture
	published  DirectNoticePublishResult
	resolver   *directNoticeReadDirectoryResolver
	closure    *directNoticeReadClosureResolver
	legacyRead *directNoticeExactReadLegacyAuthorizer
}

type directNoticeExactAckEffectSnapshot struct {
	rows  map[model.Kind][]model.Record
	audit store.HeadRef
}

type directNoticeExactAckConcurrentSource struct {
	mu    sync.Mutex
	inner *communicationAuthoritySourceRecorder
}

func (s *directNoticeExactAckConcurrentSource) AuthorizeEvidence(
	ctx context.Context,
	request auth.Request,
) auth.AuthorizationEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.AuthorizeEvidence(ctx, request)
}

type directNoticeExactAckWriteFailureData struct {
	inner     api.ModuleData
	kind      model.Kind
	operation string
	failure   error
}

type directNoticeExactAckWriteFailureScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	fault     *directNoticeExactAckWriteFailureData
}

type directNoticeExactAckWriteFailureRepo struct {
	store.GenericRepo
	stamped store.TransactionStampedGenericRepo
	locker  store.RowLocker[model.Record]
	fault   *directNoticeExactAckWriteFailureData
}

func (d *directNoticeExactAckWriteFailureData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *directNoticeExactAckWriteFailureData) Mutate(
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
			return errors.New("exact Ack write-fault scope lacks transaction capabilities")
		}
		return fn(&directNoticeExactAckWriteFailureScope{
			Scope: sc, clock: clock, locker: locker, authority: authority,
			directory: directory, fault: d,
		})
	})
}

func (s *directNoticeExactAckWriteFailureScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeExactAckWriteFailureScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeExactAckWriteFailureScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	return s.authority.LockAuthoritySnapshot(ctx, facts)
}

func (s *directNoticeExactAckWriteFailureScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	return s.directory.ReadDirectoryEpoch(ctx)
}

func (s *directNoticeExactAckWriteFailureScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *directNoticeExactAckWriteFailureScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != s.fault.kind {
		return repo, err
	}
	stamped, stampedOK := repo.(store.TransactionStampedGenericRepo)
	locker, lockerOK := repo.(store.RowLocker[model.Record])
	if !stampedOK || !lockerOK {
		return nil, errors.New("exact Ack write-fault repository lacks transaction capabilities")
	}
	return &directNoticeExactAckWriteFailureRepo{
		GenericRepo: repo, stamped: stamped, locker: locker, fault: s.fault,
	}, nil
}

func (r *directNoticeExactAckWriteFailureRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	return r.locker.Lock(ctx, id)
}

func (r *directNoticeExactAckWriteFailureRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	if r.fault.operation == "create" {
		return nil, r.fault.failure
	}
	return r.stamped.CreateAtTransactionTime(ctx, record)
}

func (r *directNoticeExactAckWriteFailureRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	if r.fault.operation == "create_with_id" {
		return nil, r.fault.failure
	}
	return r.stamped.CreateWithIDAtTransactionTime(ctx, id, record)
}

func (r *directNoticeExactAckWriteFailureRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	if r.fault.operation == "update" {
		return nil, r.fault.failure
	}
	return r.stamped.UpdateAtTransactionTime(ctx, record)
}

type directNoticeExactAckAuthorityFirstData struct {
	inner                   api.ModuleData
	authorityLocked         atomic.Bool
	earlyAccess             atomic.Bool
	authorityLocks          atomic.Int64
	operations              atomic.Int64
	skewMessage             bool
	messageVersion          int64
	messageEventSeq         int64
	transformRecord         func(model.Kind, model.Record) model.Record
	transformWrite          func(model.Kind, model.Record) model.Record
	transformDirectoryEpoch func(model.DirectoryEpoch) model.DirectoryEpoch
	transformView           bool
	viewAudit               store.AuditLog
	tombstone               store.DirectoryTombstoneWitness
	tombstoneFound          bool
	tombstoneErr            error
	overrideTombstone       bool
	tombstoneReads          atomic.Int64
	directoryEpochReads     atomic.Int64
}

func (d *directNoticeExactAckAuthorityFirstData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, func(sc store.Scope) error {
		if (d.transformRecord == nil || !d.transformView) && d.viewAudit == nil {
			return fn(sc)
		}
		return fn(&directNoticeExactAckViewScope{Scope: sc, data: d})
	})
}

type directNoticeExactAckViewScope struct {
	store.Scope
	data *directNoticeExactAckAuthorityFirstData
}

func (s *directNoticeExactAckViewScope) Audit() store.AuditLog {
	if s.data.viewAudit != nil {
		return s.data.viewAudit
	}
	return s.Scope.Audit()
}

func (s *directNoticeExactAckViewScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	return &directNoticeExactAckTransformRepo{
		GenericRepo: repo, data: s.data, kind: kind,
	}, nil
}

type directNoticeExactAckTransformRepo struct {
	store.GenericRepo
	data *directNoticeExactAckAuthorityFirstData
	kind model.Kind
}

func (r *directNoticeExactAckTransformRepo) Get(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	record, err := r.GenericRepo.Get(ctx, id)
	return directNoticeExactAckTransformRecord(r.data, r.kind, record, err)
}

func (r *directNoticeExactAckTransformRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	records, page, err := r.GenericRepo.List(ctx, query)
	if err != nil || r.data.transformRecord == nil {
		return records, page, err
	}
	for index := range records {
		records[index], err = directNoticeExactAckTransformRecord(
			r.data, r.kind, records[index], nil,
		)
		if err != nil {
			return nil, model.Page{}, err
		}
	}
	return records, page, nil
}

func directNoticeExactAckTransformRecord(
	data *directNoticeExactAckAuthorityFirstData,
	kind model.Kind,
	record model.Record,
	err error,
) (model.Record, error) {
	if err != nil || data.transformRecord == nil {
		return record, err
	}
	copyRecord := make(model.Record, len(record))
	for key, value := range record {
		copyRecord[key] = value
	}
	return data.transformRecord(kind, copyRecord), nil
}

func (d *directNoticeExactAckAuthorityFirstData) Mutate(
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
			return errors.New("exact Ack ordering scope lacks transaction capabilities")
		}
		return fn(&directNoticeExactAckAuthorityFirstScope{
			Scope: sc, clock: clock, locker: locker, authority: authority,
			directory: directory, data: d,
		})
	})
}

type directNoticeExactAckAuthorityFirstScope struct {
	store.Scope
	clock     store.TransactionClock
	locker    store.TransactionLocker
	authority store.AuthoritySnapshotLocker
	directory store.DirectorySnapshotReader
	data      *directNoticeExactAckAuthorityFirstData
}

func (s *directNoticeExactAckAuthorityFirstScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *directNoticeExactAckAuthorityFirstScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	if err := s.beforeCarrierAccess(); err != nil {
		return err
	}
	return s.locker.LockTransaction(ctx, key)
}

func (s *directNoticeExactAckAuthorityFirstScope) LockAuthoritySnapshot(
	ctx context.Context,
	facts []store.AuthorizationFactRef,
) error {
	s.data.authorityLocks.Add(1)
	if err := s.authority.LockAuthoritySnapshot(ctx, facts); err != nil {
		return err
	}
	s.data.authorityLocked.Store(true)
	return nil
}

func (s *directNoticeExactAckAuthorityFirstScope) ReadDirectoryEpoch(
	ctx context.Context,
) (model.DirectoryEpoch, error) {
	if err := s.beforeCarrierAccess(); err != nil {
		return model.DirectoryEpoch{}, err
	}
	epoch, err := s.directory.ReadDirectoryEpoch(ctx)
	if err != nil || s.data.transformDirectoryEpoch == nil {
		return epoch, err
	}
	s.data.directoryEpochReads.Add(1)
	return s.data.transformDirectoryEpoch(epoch), nil
}

func (s *directNoticeExactAckAuthorityFirstScope) ReadDirectoryTombstone(
	ctx context.Context,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	if err := s.beforeCarrierAccess(); err != nil {
		return store.DirectoryTombstoneWitness{}, false, err
	}
	if s.data.overrideTombstone {
		s.data.tombstoneReads.Add(1)
		return s.data.tombstone, s.data.tombstoneFound, s.data.tombstoneErr
	}
	return s.directory.ReadDirectoryTombstone(ctx, ref)
}

func (s *directNoticeExactAckAuthorityFirstScope) Ext(
	kind model.Kind,
) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	return &directNoticeExactAckAuthorityFirstRepo{
		GenericRepo: repo, data: s.data, kind: kind,
	}, nil
}

func (s *directNoticeExactAckAuthorityFirstScope) beforeCarrierAccess() error {
	s.data.operations.Add(1)
	if s.data.authorityLocked.Load() {
		return nil
	}
	s.data.earlyAccess.Store(true)
	return errors.New("exact Ack carrier accessed before authority snapshot lock")
}

type directNoticeExactAckAuthorityFirstRepo struct {
	store.GenericRepo
	data *directNoticeExactAckAuthorityFirstData
	kind model.Kind
}

func (r *directNoticeExactAckAuthorityFirstRepo) beforeAccess() error {
	r.data.operations.Add(1)
	if r.data.authorityLocked.Load() {
		return nil
	}
	r.data.earlyAccess.Store(true)
	return errors.New("exact Ack repository accessed before authority snapshot lock")
}

func (r *directNoticeExactAckAuthorityFirstRepo) Get(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, err
	}
	record, err := r.GenericRepo.Get(ctx, id)
	return r.transform(record, err)
}

func (r *directNoticeExactAckAuthorityFirstRepo) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, model.Page{}, err
	}
	records, page, err := r.GenericRepo.List(ctx, query)
	if err != nil || r.data.transformRecord == nil {
		return records, page, err
	}
	for index := range records {
		records[index], err = r.transform(records[index], nil)
		if err != nil {
			return nil, model.Page{}, err
		}
	}
	return records, page, nil
}

func (r *directNoticeExactAckAuthorityFirstRepo) Lock(
	ctx context.Context,
	id model.ID,
) (model.Record, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, err
	}
	locker, ok := r.GenericRepo.(store.RowLocker[model.Record])
	if !ok {
		return nil, errors.New("exact Ack repository lacks row lock")
	}
	record, err := locker.Lock(ctx, id)
	if err != nil || r.kind != messageKind || !r.data.skewMessage {
		return r.transform(record, err)
	}
	copyRecord := make(model.Record, len(record))
	for key, value := range record {
		copyRecord[key] = value
	}
	copyRecord[model.ColVersion] = r.data.messageVersion
	copyRecord[colCommLastEventSeq] = r.data.messageEventSeq
	return r.transform(copyRecord, nil)
}

func (r *directNoticeExactAckAuthorityFirstRepo) transform(
	record model.Record,
	err error,
) (model.Record, error) {
	return directNoticeExactAckTransformRecord(r.data, r.kind, record, err)
}

func (r *directNoticeExactAckAuthorityFirstRepo) CreateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, err
	}
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact Ack repository lacks stamped create")
	}
	return stamped.CreateAtTransactionTime(ctx, record)
}

func (r *directNoticeExactAckAuthorityFirstRepo) CreateWithIDAtTransactionTime(
	ctx context.Context,
	id model.ID,
	record model.Record,
) (model.Record, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, err
	}
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact Ack repository lacks stamped create-with-id")
	}
	return stamped.CreateWithIDAtTransactionTime(ctx, id, record)
}

func (r *directNoticeExactAckAuthorityFirstRepo) UpdateAtTransactionTime(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	if err := r.beforeAccess(); err != nil {
		return nil, err
	}
	stamped, ok := r.GenericRepo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, errors.New("exact Ack repository lacks stamped update")
	}
	if r.data.transformWrite != nil {
		record = r.data.transformWrite(r.kind, record)
	}
	return stamped.UpdateAtTransactionTime(ctx, record)
}

func newDirectNoticeExactAckFixture(t *testing.T) directNoticeExactAckFixture {
	return newDirectNoticeExactAckFixtureWithTimeout(t, 60_000)
}

func newDirectNoticeExactAckFixtureWithTimeout(
	t *testing.T,
	ackTimeoutMS int64,
) directNoticeExactAckFixture {
	return newDirectNoticeExactAckFixtureWithPolicy(t, AckPolicyEachRequired, ackTimeoutMS)
}

func newDirectNoticeExactAckOptionalFixture(t *testing.T) directNoticeExactAckFixture {
	return newDirectNoticeExactAckFixtureWithPolicy(t, AckPolicyNone, 0)
}

func newDirectNoticeExactAckFixtureWithPolicy(
	t *testing.T,
	ackPolicy AckPolicy,
	ackTimeoutMS int64,
) directNoticeExactAckFixture {
	t.Helper()
	fixture := newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "sqlite-direct-notice-exact-ack", engineName: store.EngineSQLite,
		dsn: filepath.Join(t.TempDir(), "direct-notice-exact-ack.db"),
	}, ackPolicy, 0, true, true, true)
	if ackTimeoutMS != fixture.channel.DefaultAckTimeoutMS {
		channelRecord, err := channelToRecord(fixture.channel)
		if err != nil {
			t.Fatalf("encode exact Ack Channel timeout: %v", err)
		}
		channelRecord[colCommDefaultAckTimeoutMS] = ackTimeoutMS
		updated, err := communicationUpdate(
			context.Background(), fixture.m, fixture.tenant, channelKind, channelRecord,
		)
		if err != nil {
			t.Fatalf("update exact Ack Channel timeout: %v", err)
		}
		fixture.channel, err = channelFromRecord(updated)
		if err != nil {
			t.Fatalf("decode exact Ack Channel timeout: %v", err)
		}
	}
	command := fixture.command(model.NewID(), "exact Ack canary")
	command.Recipient = RecipientRef{Kind: RecipientUser, Ref: fixture.sender.String()}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	published, err := fixture.m.publishDirectNoticeWithAuthority(
		ctx, fixture.scope, fixture.ref, command,
	)
	if err != nil {
		t.Fatalf("publish exact Ack fixture: %v", err)
	}
	resolver := &directNoticeReadDirectoryResolver{now: fixture.now, epoch: fixture.epoch}
	closure := &directNoticeReadClosureResolver{now: fixture.now, epoch: fixture.epoch}
	legacyRead := &directNoticeExactReadLegacyAuthorizer{}
	fixture.m.communicationDirectoryResolver = resolver
	fixture.m.communicationGrantClosure = closure
	fixture.m.communicationReadAuthorizer = legacyRead
	fixture.authorizer.calls.Store(0)
	fixture.authorizer.fail.Store(true)
	fixture.source.calls = 0
	fixture.source.requests = nil
	fixture.source.trace = nil
	return directNoticeExactAckFixture{
		directNoticeFixture: fixture, published: published,
		resolver: resolver, closure: closure, legacyRead: legacyRead,
	}
}

func waitDirectNoticeExactAckDBTime(
	t *testing.T,
	fixture directNoticeFixture,
	want time.Time,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var observed model.Timestamp
		err := fixture.m.viewCommunication(
			context.Background(), fixture.scope, func(sc store.Scope) error {
				clock, ok := sc.(store.TransactionClock)
				if !ok {
					return errors.New("exact Ack wait lacks transaction clock")
				}
				var err error
				observed, err = clock.TransactionNow(context.Background())
				return err
			},
		)
		if err != nil {
			t.Fatalf("observe exact Ack DB time: %v", err)
		}
		if !observed.Time().Before(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("DB time %s did not reach exact Ack deadline %s", observed, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func directNoticeExactAckCommand(version int64, idempotency model.ID) DirectNoticeDeliveryAckCommand {
	return DirectNoticeDeliveryAckCommand{
		IfMatch: fmt.Sprintf(`"v%d"`, version), IdempotencyKey: idempotency.String(),
	}
}

func directNoticeExactAckMessageAndDelivery(
	t *testing.T,
	fixture directNoticeFixture,
) (Message, MessageDelivery) {
	t.Helper()
	messages := communicationRowsForTest(t, fixture, messageKind)
	deliveries := communicationRowsForTest(t, fixture, messageDeliveryKind)
	if len(messages) != 1 || len(deliveries) != 1 {
		t.Fatalf("exact Ack Message/Delivery rows = %d/%d, want 1/1", len(messages), len(deliveries))
	}
	delivery, err := messageDeliveryFromRecord(deliveries[0])
	if err != nil {
		t.Fatalf("decode exact Ack Delivery: %v", err)
	}
	required := int64(0)
	if delivery.Required {
		required = 1
	}
	message, err := messageFromRecord(messages[0], required)
	if err != nil {
		t.Fatalf("decode exact Ack Message: %v", err)
	}
	return message, delivery
}

func directNoticeExactAckEffects(
	t *testing.T,
	fixture directNoticeFixture,
) directNoticeExactAckEffectSnapshot {
	t.Helper()
	kinds := []model.Kind{
		messageKind,
		messageDeliveryKind,
		messageAckKind,
		communicationCommandKind,
		workEventKind,
		workOutboxKind,
	}
	result := directNoticeExactAckEffectSnapshot{
		rows:  make(map[model.Kind][]model.Record, len(kinds)),
		audit: directNoticeAuditHead(t, fixture),
	}
	for _, kind := range kinds {
		result.rows[kind] = communicationRowsForTest(t, fixture, kind)
	}
	return result
}

func assertDirectNoticeExactAckEffectsUnchanged(
	t *testing.T,
	fixture directNoticeFixture,
	want directNoticeExactAckEffectSnapshot,
) {
	t.Helper()
	got := directNoticeExactAckEffects(t, fixture)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exact Ack effects changed\n got: %#v\nwant: %#v", got, want)
	}
}

func assertDirectNoticeAckUnknownOnly(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		errors.Is(err, store.ErrNotFound) ||
		errors.Is(err, store.ErrConflict) ||
		errors.Is(err, ErrCommunicationNotFound) ||
		errors.Is(err, ErrCommunicationForbidden) ||
		errors.Is(err, ErrCommunicationTerminal) ||
		errors.Is(err, errDirectNoticeAckVersionMismatch) ||
		errors.Is(err, errDirectNoticeAckIdempotencyReused) ||
		errors.Is(err, errDirectNoticeAckAlreadyAcknowledged) {
		t.Fatalf("exact Ack error = %v, want UNKNOWN without known outcome", err)
	}
}

func directNoticeExactAckRetractDelivery(
	t *testing.T,
	fixture directNoticeExactAckFixture,
) (Message, MessageDelivery) {
	t.Helper()
	ctx := context.Background()
	var afterMessage Message
	var afterDelivery MessageDelivery
	err := fixture.m.data.Mutate(ctx, fixture.tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, fixture.workspace)
		if err != nil {
			return err
		}
		clock, ok := confined.(store.TransactionClock)
		if !ok {
			return errors.New("exact Ack retraction fixture lacks transaction clock")
		}
		dbNow, err := clock.TransactionNow(ctx)
		if err != nil {
			return err
		}
		messageRepo, err := confined.Ext(messageKind)
		if err != nil {
			return err
		}
		deliveryRepo, err := confined.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		messageRecord, err := messageRepo.Get(ctx, fixture.published.MessageID)
		if err != nil {
			return err
		}
		deliveryRecord, err := deliveryRepo.Get(ctx, fixture.published.DeliveryID)
		if err != nil {
			return err
		}
		delivery, err := messageDeliveryFromRecord(deliveryRecord)
		if err != nil {
			return err
		}
		message, err := messageFromRecord(messageRecord, 1)
		if err != nil {
			return err
		}
		digest, err := CanonicalFulfillmentDeliverySetDigest([]MessageDelivery{delivery})
		if err != nil {
			return err
		}
		plan, err := PlanMessageTransition(MessageTransitionInput{
			Before: message, Transition: MessageRetract,
			Deliveries: []MessageDelivery{delivery},
			CarrierSet: MessageTerminalCarrierSetWitness{
				Scope: DirectoryScopeRef{
					TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
				},
				MessageID: message.ID, DeliveryCount: 1, DeliveryDigest: digest,
				ObservedAt: dbNow.Time(),
				Evidence: AuthorityEvidence{
					Verdict: VerdictClean, Code: "complete",
					EvidenceRef: "same_tx_exact_ack_retraction",
				},
			},
			TerminalCode: "sender_retracted",
			TerminalReason: communicationTestPayloadForSlot(
				t, PayloadSlotMessageTerminalReason,
			),
			DBNow: dbNow.Time(),
		})
		if err != nil {
			return err
		}
		if len(plan.DeliveryPlans) != 1 {
			return fmt.Errorf("exact Ack retraction Delivery plans = %d", len(plan.DeliveryPlans))
		}
		deliveryStamped, ok := deliveryRepo.(store.TransactionStampedGenericRepo)
		if !ok {
			return errors.New("exact Ack retraction fixture lacks stamped Delivery writes")
		}
		messageStamped, ok := messageRepo.(store.TransactionStampedGenericRepo)
		if !ok {
			return errors.New("exact Ack retraction fixture lacks stamped Message writes")
		}
		messageAfterRecord, err := messageToRecord(plan.After, 1)
		if err != nil {
			return err
		}
		messageAfterRecord[model.ColVersion] = plan.Before.Version
		if _, err = messageStamped.UpdateAtTransactionTime(ctx, messageAfterRecord); err != nil {
			return err
		}
		deliveryAfterRecord, err := messageDeliveryToRecord(plan.DeliveryPlans[0].After)
		if err != nil {
			return err
		}
		deliveryAfterRecord[model.ColVersion] = plan.DeliveryPlans[0].Before.Version
		if _, err = deliveryStamped.UpdateAtTransactionTime(ctx, deliveryAfterRecord); err != nil {
			return err
		}
		afterMessage = plan.After
		afterDelivery = plan.DeliveryPlans[0].After
		return nil
	})
	if err != nil {
		t.Fatalf("retract exact Ack fixture: %v", err)
	}
	return afterMessage, afterDelivery
}

func directNoticeExactAckAdvanceDeliveryEvidence(
	t *testing.T,
	fixture directNoticeExactAckFixture,
	evidence string,
) MessageDelivery {
	t.Helper()
	ctx := context.Background()
	var stored MessageDelivery
	err := fixture.m.mutateCommunication(ctx, fixture.scope, func(tx *communicationTx) error {
		record, err := tx.lockRecord(ctx, messageDeliveryKind, fixture.published.DeliveryID)
		if err != nil {
			return err
		}
		before, err := messageDeliveryFromRecord(record)
		if err != nil {
			return err
		}
		var after MessageDelivery
		switch evidence {
		case "seen":
			plan, planErr := PlanMessageDeliverySeen(before, tx.now.Time())
			if planErr != nil {
				return planErr
			}
			if !plan.Changed || plan.MaterializesExpiry {
				return fmt.Errorf("exact Ack seen evidence did not advance carrier: %+v", plan)
			}
			after = plan.After
		case "wake":
			after, err = deliveryWithWakeEvidence(
				before, VerdictClean, "provider_accepted", tx.now.Time(),
			)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported exact Ack Delivery evidence %q", evidence)
		}
		encoded, err := messageDeliveryToRecord(after)
		if err != nil {
			return err
		}
		encoded[model.ColVersion] = before.Version
		updated, err := tx.update(ctx, messageDeliveryKind, encoded)
		if err != nil {
			return err
		}
		stored, err = messageDeliveryFromRecord(updated)
		return err
	})
	if err != nil {
		t.Fatalf("advance exact Ack Delivery %s evidence: %v", evidence, err)
	}
	return stored
}

func directNoticeExactAckUndeliverablePlan(
	t *testing.T,
	fixture directNoticeExactAckFixture,
) (UndeliverablePlan, store.DirectoryTombstoneWitness) {
	t.Helper()
	message, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	principalID, err := model.ParseID(delivery.Recipient.Ref)
	if err != nil {
		t.Fatalf("parse exact Ack undeliverable recipient: %v", err)
	}
	witness := store.DirectoryTombstoneWitness{
		TombstoneKind: model.UserTombstoneKind, TombstoneID: model.NewID(),
		TombstoneVersion: 1,
		Principal: store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: principalID,
		},
		RetirementEpoch: fixture.epoch,
	}
	dbNow := delivery.UpdatedAt
	if dbNow.Before(message.UpdatedAt) {
		dbNow = message.UpdatedAt
	}
	plan, err := PlanUndeliverable(
		message,
		delivery,
		RecipientSnapshot{
			Scope: fixture.scope, Recipient: delivery.Recipient,
			RecipientEpoch: delivery.RecipientEpoch, DirectoryEpoch: fixture.epoch,
			Tombstone: &witness,
		},
		dbNow,
		"recipient_retired",
	)
	if err != nil {
		t.Fatalf("plan exact Ack undeliverable carrier: %v", err)
	}
	return plan, witness
}

func directNoticeExactAckReplaceRecord(record, replacement model.Record) model.Record {
	for key := range record {
		delete(record, key)
	}
	for key, value := range replacement {
		record[key] = value
	}
	return record
}

func TestDirectNoticeExactAckUsesCurrentAuthorityAndCommitsOneEffectSet(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	beforeMessage, beforeDelivery := directNoticeExactAckMessageAndDelivery(
		t, fixture.directNoticeFixture,
	)
	staticKinds := []model.Kind{
		channelKind, channelGrantKind, messageAudienceKind, messageAudienceRecipientKind,
	}
	staticBefore := make(map[model.Kind][]model.Record, len(staticKinds))
	for _, kind := range staticKinds {
		staticBefore[kind] = communicationRowsForTest(t, fixture.directNoticeFixture, kind)
	}
	beforeAckCount := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind))
	beforeReceiptCount := len(communicationRowsForTest(
		t, fixture.directNoticeFixture, communicationCommandKind,
	))
	beforeEventCount := len(communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind))
	beforeOutboxCount := len(communicationRowsForTest(t, fixture.directNoticeFixture, workOutboxKind))
	beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)

	base := fixture.m.data
	trace := &directNoticeAuthorityTrace{}
	authorityFirst := &directNoticeExactAckAuthorityFirstData{inner: base}
	observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
		inner: authorityFirst, trace: trace,
	}}
	fixture.m.data = observer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(beforeDelivery.Version, model.NewID()),
	)
	if err != nil {
		t.Fatalf("exact DirectNotice Ack: %v", err)
	}
	fixture.m.data = base

	wantFulfillment := FulfillmentProjection{
		State: FulfillmentMet, Required: 1, Acknowledged: 1,
	}
	if result.CommandID == "" || result.AckID == "" || result.EventID == "" ||
		result.DeliveryID != fixture.published.DeliveryID ||
		result.MessageID != fixture.published.MessageID ||
		result.Version != beforeDelivery.Version+1 ||
		result.ETag != fmt.Sprintf(`"v%d"`, result.Version) ||
		result.State != DeliveryAcknowledged || result.Late || result.Replayed ||
		result.Fulfillment != wantFulfillment || result.AuditSeq != beforeAudit.Seq+1 ||
		result.messageEventSeq != beforeMessage.LastEventSeq+1 {
		t.Fatalf("exact DirectNotice Ack result = %+v", result)
	}
	if observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
		authorityFirst.authorityLocks.Load() != 1 || authorityFirst.earlyAccess.Load() ||
		authorityFirst.operations.Load() == 0 {
		t.Fatalf(
			"exact Ack views/mutates/authority/early/operations = %d/%d/%d/%t/%d",
			observer.views.Load(), observer.mutates.Load(), authorityFirst.authorityLocks.Load(),
			authorityFirst.earlyAccess.Load(), authorityFirst.operations.Load(),
		)
	}
	if len(trace.steps) == 0 || trace.steps[0] != "authority" ||
		len(trace.authorityFacts) != 1 || trace.nowCalls != 3 {
		t.Fatalf(
			"exact Ack transaction trace = steps %v facts %v now %d",
			trace.steps, trace.authorityFacts, trace.nowCalls,
		)
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
		t.Fatalf("exact Ack locked facts = %+v", trace.authorityFacts[0])
	}
	if fixture.source.calls != 1 || len(fixture.source.requests) != 1 ||
		fixture.resolver.calls.Load() != 1 || fixture.closure.calls.Load() != 1 ||
		fixture.authorizer.calls.Load() != 0 || fixture.legacyRead.calls.Load() != 0 {
		t.Fatalf(
			"exact Ack source/requests/resolver/closure/operation-legacy/read-legacy = %d/%d/%d/%d/%d/%d",
			fixture.source.calls, len(fixture.source.requests), fixture.resolver.calls.Load(),
			fixture.closure.calls.Load(), fixture.authorizer.calls.Load(), fixture.legacyRead.calls.Load(),
		)
	}
	request := fixture.source.requests[0]
	requestRef, ok := request.Principal.Ref()
	if !ok || requestRef != fixture.ref || request.Tenant != fixture.tenant ||
		request.Permission != permDeliveryWrite ||
		request.Resource.Kind != string(messageDeliveryKind) ||
		request.Resource.ID != fixture.published.DeliveryID.String() ||
		request.Resource.WorkspaceID != fixture.workspace || len(request.Resource.Extra) != 0 {
		t.Fatalf("exact Ack authorization request = %#v, ref ok=%t", request, ok)
	}

	afterMessage, afterDelivery := directNoticeExactAckMessageAndDelivery(
		t, fixture.directNoticeFixture,
	)
	wantMessage := beforeMessage
	wantMessage.Version++
	wantMessage.LastEventSeq++
	wantMessage.UpdatedAt = afterMessage.UpdatedAt
	if !reflect.DeepEqual(afterMessage, wantMessage) {
		t.Fatalf("exact Ack Message = %#v, want %#v", afterMessage, wantMessage)
	}
	wantDelivery := beforeDelivery
	wantDelivery.Version++
	wantDelivery.UpdatedAt = afterDelivery.UpdatedAt
	wantDelivery.State = DeliveryAcknowledged
	wantDelivery.AckID = result.AckID
	wantDelivery.AcknowledgedAt = cloneDirectNoticeTime(afterDelivery.AcknowledgedAt)
	if !reflect.DeepEqual(afterDelivery, wantDelivery) {
		t.Fatalf("exact Ack Delivery = %#v, want %#v", afterDelivery, wantDelivery)
	}
	ackRows := communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind)
	if len(ackRows) != beforeAckCount+1 {
		t.Fatalf("exact Ack rows = %d, want %d", len(ackRows), beforeAckCount+1)
	}
	ack, err := messageAckFromRecord(ackRows[len(ackRows)-1])
	if err != nil || ack.ID != result.AckID || ack.DeliveryID != result.DeliveryID ||
		ack.Actor != (CommunicationActorRef{Kind: ActorUser, Ref: fixture.sender.String()}) ||
		ack.OnBehalfOf != nil || ack.Note != nil || ack.Late ||
		afterDelivery.AcknowledgedAt == nil || !ack.AcknowledgedAt.Equal(*afterDelivery.AcknowledgedAt) {
		t.Fatalf("stored exact Ack = %+v, err=%v", ack, err)
	}
	receipts := communicationRowsForTest(t, fixture.directNoticeFixture, communicationCommandKind)
	events := communicationRowsForTest(t, fixture.directNoticeFixture, workEventKind)
	outboxes := communicationRowsForTest(t, fixture.directNoticeFixture, workOutboxKind)
	if len(receipts) != beforeReceiptCount+1 || len(events) != beforeEventCount+1 ||
		len(outboxes) != beforeOutboxCount+1 {
		t.Fatalf(
			"exact Ack receipt/event/outbox counts = %d/%d/%d, want %d/%d/%d",
			len(receipts), len(events), len(outboxes),
			beforeReceiptCount+1, beforeEventCount+1, beforeOutboxCount+1,
		)
	}
	var storedReceipt CommunicationCommandReceipt
	for _, row := range receipts {
		receipt, decodeErr := communicationCommandReceiptFromRecord(row)
		if decodeErr == nil && receipt.CommandID == result.CommandID {
			storedReceipt = receipt
			break
		}
	}
	if storedReceipt.ID == "" || storedReceipt.ResultKind != string(messageAckKind) ||
		storedReceipt.ResultID != result.AckID || storedReceipt.EventID != result.EventID ||
		storedReceipt.AuditSeq != result.AuditSeq ||
		storedReceipt.ResponseProjectionJSON.Version != result.Version ||
		storedReceipt.ResponseProjectionJSON.State != string(result.State) {
		t.Fatalf("stored exact Ack receipt = %+v", storedReceipt)
	}
	foundEvent, foundOutbox := false, false
	for _, row := range events {
		if row.String(colEventID) == result.EventID.String() {
			foundEvent = row.String(colEventType) == communicationMessageAcknowledged &&
				row.String(colEventType) == "work.message.acknowledged"
		}
	}
	for _, row := range outboxes {
		foundOutbox = foundOutbox || row.String(colOutboxEventID) == result.EventID.String()
	}
	if !foundEvent || !foundOutbox {
		t.Fatalf("exact Ack event/outbox anchors found = %t/%t", foundEvent, foundOutbox)
	}
	afterAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
	if afterAudit.Seq != result.AuditSeq {
		t.Fatalf("exact Ack audit head = %+v, result seq %d", afterAudit, result.AuditSeq)
	}
	for _, kind := range staticKinds {
		after := communicationRowsForTest(t, fixture.directNoticeFixture, kind)
		if !reflect.DeepEqual(after, staticBefore[kind]) {
			t.Fatalf("exact Ack changed static %s rows", kind)
		}
	}
}

func TestDirectNoticeExactAckLateLifecycleReplayAndUniqueness(t *testing.T) {
	t.Parallel()

	for _, state := range []MessageDeliveryState{DeliveryExpired, DeliveryRetracted} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			var fixture directNoticeExactAckFixture
			if state == DeliveryExpired {
				fixture = newDirectNoticeExactAckFixtureWithTimeout(t, 1)
			} else {
				fixture = newDirectNoticeExactAckFixture(t)
			}
			base := fixture.m.data
			_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
			if state == DeliveryRetracted {
				_, delivery = directNoticeExactAckRetractDelivery(t, fixture)
			}
			if state == DeliveryExpired {
				if delivery.AckDueAt == nil {
					t.Fatal("exact Ack expiry fixture lacks AckDueAt")
				}
				waitDirectNoticeExactAckDBTime(t, fixture.directNoticeFixture, *delivery.AckDueAt)
			}
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			beforeMessage, beforeDelivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			beforeAckCount := len(communicationRowsForTest(
				t, fixture.directNoticeFixture, messageAckKind,
			))
			beforeReceiptCount := len(communicationRowsForTest(
				t, fixture.directNoticeFixture, communicationCommandKind,
			))
			beforeEventCount := len(communicationRowsForTest(
				t, fixture.directNoticeFixture, workEventKind,
			))
			beforeOutboxCount := len(communicationRowsForTest(
				t, fixture.directNoticeFixture, workOutboxKind,
			))
			beforeAudit := directNoticeAuditHead(t, fixture.directNoticeFixture)
			observer := &directNoticeMutateObserverData{inner: base}
			fixture.m.data = observer
			assertCallDelta := func(stage string, beforeViews, beforeMutates int64) {
				t.Helper()
				if observer.views.Load() != beforeViews+1 ||
					observer.mutates.Load() != beforeMutates+1 {
					t.Fatalf(
						"%s %s Ack View/Mutate delta = %d/%d, want 1/1",
						state, stage, observer.views.Load()-beforeViews,
						observer.mutates.Load()-beforeMutates,
					)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			beforeViews, beforeMutates := observer.views.Load(), observer.mutates.Load()
			first, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			)
			if err != nil {
				t.Fatalf("first %s late exact Ack: %v", state, err)
			}
			assertCallDelta("first", beforeViews, beforeMutates)
			if !first.Late || first.Replayed || first.State != state ||
				first.Fulfillment != (FulfillmentProjection{
					State: FulfillmentUnmet, Required: 1, Unmet: 1,
				}) || first.AuditSeq != beforeAudit.Seq+1 {
				t.Fatalf("first %s late exact Ack = %+v", state, first)
			}
			fixture.m.data = base
			afterFirstMessage, afterFirstDelivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if state == DeliveryRetracted && !reflect.DeepEqual(afterFirstDelivery, beforeDelivery) {
				t.Fatalf(
					"late retracted Ack changed Delivery\nbefore=%#v\nafter=%#v",
					beforeDelivery, afterFirstDelivery,
				)
			}
			if state == DeliveryExpired &&
				(afterFirstDelivery.Version != beforeDelivery.Version+1 ||
					afterFirstDelivery.State != DeliveryExpired ||
					afterFirstDelivery.AckID != "" || afterFirstDelivery.AcknowledgedAt != nil) {
				t.Fatalf("late expiry did not materialize exact Delivery = %+v", afterFirstDelivery)
			}
			if afterFirstMessage.Version != beforeMessage.Version+1 ||
				afterFirstMessage.LastEventSeq != beforeMessage.LastEventSeq+1 {
				t.Fatalf(
					"late %s Message CAS = v%d/seq%d, want v%d/seq%d",
					state, afterFirstMessage.Version, afterFirstMessage.LastEventSeq,
					beforeMessage.Version+1, beforeMessage.LastEventSeq+1,
				)
			}
			assertCounts := func(stage string) {
				t.Helper()
				if got := len(communicationRowsForTest(
					t, fixture.directNoticeFixture, messageAckKind,
				)); got != beforeAckCount+1 {
					t.Fatalf("%s %s Ack rows = %d, want %d", state, stage, got, beforeAckCount+1)
				}
				if got := len(communicationRowsForTest(
					t, fixture.directNoticeFixture, communicationCommandKind,
				)); got != beforeReceiptCount+1 {
					t.Fatalf("%s %s receipts = %d, want %d", state, stage, got, beforeReceiptCount+1)
				}
				if got := len(communicationRowsForTest(
					t, fixture.directNoticeFixture, workEventKind,
				)); got != beforeEventCount+1 {
					t.Fatalf("%s %s events = %d, want %d", state, stage, got, beforeEventCount+1)
				}
				if got := len(communicationRowsForTest(
					t, fixture.directNoticeFixture, workOutboxKind,
				)); got != beforeOutboxCount+1 {
					t.Fatalf("%s %s outboxes = %d, want %d", state, stage, got, beforeOutboxCount+1)
				}
				if head := directNoticeAuditHead(t, fixture.directNoticeFixture); head.Seq != first.AuditSeq {
					t.Fatalf("%s %s audit head = %+v, want seq %d", state, stage, head, first.AuditSeq)
				}
			}
			assertCounts("first")

			fixture.m.data = observer
			beforeViews, beforeMutates = observer.views.Load(), observer.mutates.Load()
			replayed, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			)
			if err != nil {
				t.Fatalf("replay %s late exact Ack: %v", state, err)
			}
			assertCallDelta("replay", beforeViews, beforeMutates)
			wantReplay := first
			wantReplay.Replayed = true
			if !reflect.DeepEqual(replayed, wantReplay) {
				t.Fatalf("replay %s = %+v, want %+v", state, replayed, wantReplay)
			}
			fixture.m.data = base
			assertCounts("replay")

			fixture.m.data = observer
			changed := command
			changed.IfMatch = fmt.Sprintf(`"v%d"`, delivery.Version+1)
			beforeConflictClosure := fixture.closure.calls.Load()
			beforeViews, beforeMutates = observer.views.Load(), observer.mutates.Load()
			_, err = fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, changed,
			)
			if !errors.Is(err, errDirectNoticeAckIdempotencyReused) ||
				fixture.closure.calls.Load() != beforeConflictClosure+1 {
				t.Fatalf(
					"%s changed-digest replay = %v, closure calls %d -> %d",
					state, err, beforeConflictClosure, fixture.closure.calls.Load(),
				)
			}
			assertCallDelta("changed digest", beforeViews, beforeMutates)
			fixture.m.data = base
			assertCounts("changed digest")

			fixture.m.data = observer
			currentVersion := afterFirstDelivery.Version
			beforeTerminalClosure := fixture.closure.calls.Load()
			beforeViews, beforeMutates = observer.views.Load(), observer.mutates.Load()
			_, err = fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(currentVersion, model.NewID()),
			)
			if !errors.Is(err, errDirectNoticeAckAlreadyAcknowledged) ||
				!errors.Is(err, store.ErrConflict) || errors.Is(err, ErrCommunicationTerminal) ||
				fixture.closure.calls.Load() != beforeTerminalClosure+1 {
				t.Fatalf(
					"%s second-key late Ack = %v, closure calls %d -> %d",
					state, err, beforeTerminalClosure, fixture.closure.calls.Load(),
				)
			}
			assertCallDelta("second key", beforeViews, beforeMutates)
			fixture.m.data = base
			assertCounts("second key")
		})
	}
}

func TestDirectNoticeExactAckReplaySurvivesLegalDeliveryEvidenceUpdates(t *testing.T) {
	t.Parallel()

	for _, state := range []MessageDeliveryState{
		DeliveryAcknowledged,
		DeliveryExpired,
		DeliveryRetracted,
	} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			var fixture directNoticeExactAckFixture
			switch state {
			case DeliveryExpired:
				fixture = newDirectNoticeExactAckFixtureWithTimeout(t, 1)
			case DeliveryAcknowledged, DeliveryRetracted:
				fixture = newDirectNoticeExactAckFixture(t)
			default:
				t.Fatalf("unsupported exact Ack replay state %q", state)
			}
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if state == DeliveryRetracted {
				_, delivery = directNoticeExactAckRetractDelivery(t, fixture)
			}
			if state == DeliveryExpired {
				if delivery.AckDueAt == nil {
					t.Fatal("exact Ack evidence-update expiry lacks AckDueAt")
				}
				waitDirectNoticeExactAckDBTime(
					t, fixture.directNoticeFixture, *delivery.AckDueAt,
				)
			}
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			first, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			)
			if err != nil || first.Replayed || first.State != state ||
				first.Late != oneOf(state, DeliveryExpired, DeliveryRetracted) {
				t.Fatalf("seed %s exact Ack evidence replay = %+v, %v", state, first, err)
			}

			for _, evidence := range []string{"seen", "wake"} {
				advanced := directNoticeExactAckAdvanceDeliveryEvidence(t, fixture, evidence)
				if advanced.State != state || advanced.Version <= first.Version {
					t.Fatalf("%s %s evidence advanced Delivery = %+v", state, evidence, advanced)
				}
				switch evidence {
				case "seen":
					if advanced.FirstSeenAt == nil {
						t.Fatalf("%s seen evidence was not retained", state)
					}
				case "wake":
					if advanced.LastWakeAt == nil || advanced.LastWakeVerdict != VerdictClean ||
						advanced.LastWakeCode != "provider_accepted" {
						t.Fatalf("%s wake evidence was not retained: %+v", state, advanced)
					}
				}
				beforeReplay := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
				replayed, replayErr := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
					ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
				)
				want := first
				want.Replayed = true
				if replayErr != nil || !reflect.DeepEqual(replayed, want) {
					t.Fatalf(
						"%s exact Ack replay after %s = %+v, %v; want %+v",
						state, evidence, replayed, replayErr, want,
					)
				}
				assertDirectNoticeExactAckEffectsUnchanged(
					t, fixture.directNoticeFixture, beforeReplay,
				)
			}
		})
	}
}

func TestDirectNoticeExactAckPublicBoundaryBindsBeforeReadinessAndStaysOff(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAuthorityFixture(t)
	deliveryID := model.NewID()
	trace := []string{}
	resolver := &communicationAuthorityResolverRecorder{
		resolved: fixture.authUser,
		trace:    &trace,
	}
	fixture.source.trace = &trace
	fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
	localResolver := &directNoticeReadDirectoryResolver{
		now: fixture.now, epoch: fixture.epoch,
	}
	fixture.m.communicationDirectoryResolver = localResolver
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
	_, err := fixture.m.AcknowledgeDirectNoticeDelivery(
		ctx,
		fixture.scope,
		fixture.ref,
		deliveryID,
		DirectNoticeDeliveryAckCommand{
			IfMatch:        `"v1"`,
			IdempotencyKey: model.NewID().String(),
		},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public exact Ack = %v, want OFF/UNKNOWN", err)
	}
	wantTrace := []string{"resolve", "authorize", "store", "sealer"}
	if !reflect.DeepEqual(trace, wantTrace) {
		t.Fatalf("public exact Ack trace = %v, want %v", trace, wantTrace)
	}
	if resolver.calls != 1 || fixture.source.calls != 1 || len(fixture.source.requests) != 1 {
		t.Fatalf(
			"public exact Ack resolver/source/requests = %d/%d/%d, want 1/1/1",
			resolver.calls, fixture.source.calls, len(fixture.source.requests),
		)
	}
	request := fixture.source.requests[0]
	requestRef, ok := request.Principal.Ref()
	if !ok || requestRef != fixture.ref || request.Tenant != fixture.tenant ||
		request.Permission != permDeliveryWrite ||
		request.Resource.Kind != string(messageDeliveryKind) ||
		request.Resource.ID != deliveryID.String() ||
		request.Resource.WorkspaceID != fixture.workspace || len(request.Resource.Extra) != 0 {
		t.Fatalf("public exact Ack authorization request = %#v, ref ok=%t", request, ok)
	}
	if observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
		localResolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 ||
		fixture.authorizer.calls.Load() != 0 || readiness.cryptoCalls != 0 {
		t.Fatalf(
			"OFF exact Ack observers view/mutate/resolve/closure/legacy/crypto = %d/%d/%d/%d/%d/%d",
			observer.views.Load(),
			observer.mutates.Load(),
			localResolver.calls.Load(),
			fixture.closure.calls.Load(),
			fixture.authorizer.calls.Load(),
			readiness.cryptoCalls,
		)
	}
}

func TestDirectNoticeExactAckRequiresCanonicalStrongETag(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		value       string
		wantVersion int64
		want        error
	}{
		{name: "valid", value: `"v7"`, wantVersion: 7},
		{name: "required", want: errDirectNoticeAckVersionRequired},
		{name: "weak", value: `W/"v1"`, want: ErrInvalidCommunicationModel},
		{name: "zero", value: `"v0"`, want: ErrInvalidCommunicationModel},
		{name: "leading_zero", value: `"v01"`, want: ErrInvalidCommunicationModel},
		{name: "unquoted", value: "v1", want: ErrInvalidCommunicationModel},
	} {
		t.Run(test.name, func(t *testing.T) {
			version, err := parseDirectNoticeAckETag(test.value)
			if test.want == nil {
				if err != nil || version != test.wantVersion {
					t.Fatalf("parse Ack ETag %q = %d, %v; want %d", test.value, version, err, test.wantVersion)
				}
				return
			}
			if !errors.Is(err, test.want) || version != 0 {
				t.Fatalf("parse Ack ETag %q = %d, %v; want zero/%v", test.value, version, err, test.want)
			}
		})
	}
}

func TestDirectNoticeExactAckReplayTokenRejectsDeliverySpliceBeforeCarrierAccess(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	question, err := newCommunicationAuthorityQuestion(
		fixture.scope,
		messageDeliveryKind,
		fixture.published.DeliveryID,
		CommunicationDeliveryWrite,
	)
	if err != nil {
		t.Fatalf("build exact Ack question: %v", err)
	}
	bound, err := fixture.m.bindCurrentCommunicationRequestAuthority(ctx, fixture.ref, question)
	if err != nil {
		t.Fatalf("bind exact Ack authority: %v", err)
	}
	inspected, err := bound.contextFor(question)
	if err != nil {
		t.Fatalf("inspect exact Ack authority: %v", err)
	}
	identity, err := fixture.m.preflightDirectNoticeReaderIdentity(
		ctx, fixture.scope, inspected.principal, nil,
	)
	if err != nil {
		t.Fatalf("preflight exact Ack reader: %v", err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		t.Fatalf("build exact Ack authority window: %v", err)
	}
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	normalized, err := normalizeDirectNoticeDeliveryAckCommand(
		fixture.scope,
		inspected.principal,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	if err != nil {
		t.Fatalf("normalize exact Ack command: %v", err)
	}

	base := fixture.m.data
	authorityFirst := &directNoticeExactAckAuthorityFirstData{inner: base}
	observer := &directNoticeMutateObserverData{inner: authorityFirst}
	fixture.m.data = observer
	err = fixture.m.mutateCommunicationWithNarrowedAuthority(
		ctx,
		question,
		bound,
		CommunicationClaimAuthoritySnapshot{},
		window,
		func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
			reader, readerErr := directNoticeReaderPreflightWithCore(identity, consumed.witness)
			if readerErr != nil {
				return readerErr
			}
			identityCommitment, commitmentErr := directNoticeAckReaderIdentityCommitment(
				identity, normalized,
			)
			if commitmentErr != nil {
				return commitmentErr
			}
			preflightA := directNoticeAckPreflight{
				normalized: normalized, identity: identity,
				identityCommitment: identityCommitment,
				core:               cloneCommunicationRequestAuthorityWitness(consumed.witness),
				ids:                newDirectNoticeAckIDs(), bindingID: consumed.bindingID,
			}
			authorityLock, lockErr := lockDirectNoticeAckAuthoritySnapshot(
				ctx, tx, preflightA, reader,
			)
			if lockErr != nil {
				return lockErr
			}
			preflightB := preflightA
			preflightB.normalized.deliveryID = model.NewID()
			confirmErr := confirmDirectNoticeAckReplayLocked(
				ctx,
				tx,
				preflightB,
				reader,
				CommunicationCommandReceipt{},
				DirectNoticeDeliveryAckResult{},
				authorityLock,
			)
			return confirmErr
		},
	)
	fixture.m.data = base
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
		observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
		authorityFirst.authorityLocks.Load() != 1 || authorityFirst.earlyAccess.Load() ||
		authorityFirst.operations.Load() != 0 {
		t.Fatalf(
			"spliced exact Ack replay = %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			authorityFirst.authorityLocks.Load(),
			authorityFirst.earlyAccess.Load(),
			authorityFirst.operations.Load(),
		)
	}
}

func TestDirectNoticeExactAckAuthorityTokenOwnsRequestAndReaderCommitments(t *testing.T) {
	t.Parallel()

	for _, mutation := range []string{
		"request_digest_alias",
		"idempotency_hash_alias",
		"reader_subject_alias",
		"ack_id_alias",
		"command_id_alias",
		"event_id_alias",
		"receipt_id_alias",
	} {
		mutation := mutation
		t.Run(mutation, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			question, err := newCommunicationAuthorityQuestion(
				fixture.scope,
				messageDeliveryKind,
				fixture.published.DeliveryID,
				CommunicationDeliveryWrite,
			)
			if err != nil {
				t.Fatalf("build exact Ack alias question: %v", err)
			}
			bound, err := fixture.m.bindCurrentCommunicationRequestAuthority(
				ctx, fixture.ref, question,
			)
			if err != nil {
				t.Fatalf("bind exact Ack alias authority: %v", err)
			}
			inspected, err := bound.contextFor(question)
			if err != nil {
				t.Fatalf("inspect exact Ack alias authority: %v", err)
			}
			identity, err := fixture.m.preflightDirectNoticeReaderIdentity(
				ctx, fixture.scope, inspected.principal, nil,
			)
			if err != nil {
				t.Fatalf("preflight exact Ack alias reader: %v", err)
			}
			window, err := directNoticeReaderAuthorityWindow(identity)
			if err != nil {
				t.Fatalf("build exact Ack alias window: %v", err)
			}
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			normalized, err := normalizeDirectNoticeDeliveryAckCommand(
				fixture.scope,
				inspected.principal,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			if err != nil {
				t.Fatalf("normalize exact Ack alias command: %v", err)
			}

			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			authorityFirst := &directNoticeExactAckAuthorityFirstData{inner: base}
			observer := &directNoticeMutateObserverData{inner: authorityFirst}
			fixture.m.data = observer
			err = fixture.m.mutateCommunicationWithNarrowedAuthority(
				ctx,
				question,
				bound,
				CommunicationClaimAuthoritySnapshot{},
				window,
				func(tx *communicationTx, consumed communicationRequestAuthorityContext) error {
					reader, readerErr := directNoticeReaderPreflightWithCore(
						identity, consumed.witness,
					)
					if readerErr != nil {
						return readerErr
					}
					identityCommitment, commitmentErr := directNoticeAckReaderIdentityCommitment(
						identity, normalized,
					)
					if commitmentErr != nil {
						return commitmentErr
					}
					preflight := directNoticeAckPreflight{
						normalized: normalized, identity: identity,
						identityCommitment: identityCommitment,
						core:               cloneCommunicationRequestAuthorityWitness(consumed.witness),
						ids:                newDirectNoticeAckIDs(), bindingID: consumed.bindingID,
					}
					authorityLock, lockErr := lockDirectNoticeAckAuthoritySnapshot(
						ctx, tx, preflight, reader,
					)
					if lockErr != nil {
						return lockErr
					}
					switch mutation {
					case "request_digest_alias":
						preflight.normalized.requestDigest[0] ^= 0xff
					case "idempotency_hash_alias":
						preflight.normalized.idempotencyKeyHash[0] ^= 0xff
					case "reader_subject_alias":
						reader.Closure.Subjects[0].Ref = model.NewID().String()
					case "ack_id_alias":
						preflight.ids.Ack = model.NewID()
					case "command_id_alias":
						preflight.ids.Command = model.NewID()
					case "event_id_alias":
						preflight.ids.Event = model.NewID()
					case "receipt_id_alias":
						preflight.ids.Receipt = model.NewID()
					default:
						return errors.New("unknown exact Ack alias mutation")
					}
					if authorityLock.consume(tx, preflight, reader) {
						return errors.New("spliced exact Ack authority token was accepted")
					}
					return communicationError(
						ErrCommunicationEvidenceUnknown,
						"spliced exact Ack authority token was rejected",
					)
				},
			)
			fixture.m.data = base
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
				observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
				authorityFirst.authorityLocks.Load() != 1 ||
				authorityFirst.earlyAccess.Load() || authorityFirst.operations.Load() != 0 {
				t.Fatalf(
					"%s exact Ack alias = %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
					mutation,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					authorityFirst.authorityLocks.Load(),
					authorityFirst.earlyAccess.Load(),
					authorityFirst.operations.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(
				t, fixture.directNoticeFixture, before,
			)
		})
	}
}

func TestDirectNoticeExactAckUnknownDiscoveryJoinsStayUnknownAndRollback(t *testing.T) {
	t.Parallel()

	fault := errors.Join(ErrCommunicationEvidenceUnknown, store.ErrNotFound)
	for _, test := range []struct {
		name        string
		getErr      error
		listErrKind model.Kind
		listErr     error
		minObserved int64
	}{
		{name: "initial_delivery_get", getErr: fault, minObserved: 1},
		{
			name: "current_grant_list", listErrKind: channelGrantKind,
			listErr: fault, minObserved: 3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			faultData := &directNoticeExactAuthorityFirstData{
				inner: base, getErr: test.getErr,
				listErrKind: test.listErrKind, listErr: test.listErr,
			}
			observer := &directNoticeMutateObserverData{inner: faultData}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			fixture.m.data = base
			assertDirectNoticeAckUnknownOnly(t, err)
			if result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
				faultData.earlyObservation.Load() ||
				faultData.observations.Load() < test.minObserved {
				t.Fatalf(
					"%s exact Ack = %+v; view/mutate/early/observed %d/%d/%t/%d",
					test.name,
					result,
					observer.views.Load(),
					observer.mutates.Load(),
					faultData.earlyObservation.Load(),
					faultData.observations.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
		})
	}
}

func TestDirectNoticeExactAckUnknownJoinsNormalizeWithoutKnownSentinels(t *testing.T) {
	t.Parallel()

	for _, known := range []error{
		store.ErrNotFound,
		ErrCommunicationNotFound,
		ErrCommunicationForbidden,
		ErrCommunicationTerminal,
		errDirectNoticeAckAlreadyAcknowledged,
	} {
		err := normalizeDirectNoticeAckError(errors.Join(
			ErrCommunicationEvidenceUnknown,
			known,
		))
		assertDirectNoticeAckUnknownOnly(t, err)
	}
}

func TestDirectNoticeExactAckDefersObservedOutcomesUntilFinalAuthoritySample(t *testing.T) {
	t.Parallel()

	for _, outcome := range []string{
		"absent_carrier",
		"version_mismatch",
		"already_acknowledged",
		"replay_conflict",
		"forbidden",
	} {
		outcome := outcome
		for _, atBoundary := range []bool{false, true} {
			atBoundary := atBoundary
			name := "final_before_boundary"
			if atBoundary {
				name = "final_at_boundary"
			}
			t.Run(outcome+"/"+name, func(t *testing.T) {
				fixture := newDirectNoticeExactAckFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				_, delivery := directNoticeExactAckMessageAndDelivery(
					t, fixture.directNoticeFixture,
				)
				target := fixture.published.DeliveryID
				command := directNoticeExactAckCommand(delivery.Version, model.NewID())
				var want error
				switch outcome {
				case "absent_carrier":
					target = model.NewID()
					want = ErrCommunicationNotFound
				case "version_mismatch":
					command.IfMatch = fmt.Sprintf(`"v%d"`, delivery.Version+1)
					want = errDirectNoticeAckVersionMismatch
				case "already_acknowledged":
					first, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
						ctx, fixture.scope, fixture.ref, target, command,
					)
					if err != nil {
						t.Fatalf("seed exact Ack terminal outcome: %v", err)
					}
					command = directNoticeExactAckCommand(first.Version, model.NewID())
					want = errDirectNoticeAckAlreadyAcknowledged
				case "replay_conflict":
					if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
						ctx, fixture.scope, fixture.ref, target, command,
					); err != nil {
						t.Fatalf("seed exact Ack replay conflict: %v", err)
					}
					command.IfMatch = fmt.Sprintf(`"v%d"`, delivery.Version+1)
					want = errDirectNoticeAckIdempotencyReused
				case "forbidden":
					fixture.closure.outcome = ReadDeny
					want = ErrCommunicationForbidden
				default:
					t.Fatalf("unknown exact Ack outcome %q", outcome)
				}

				before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
				freshUntil := fixture.source.evidence.FreshUntil.UTC()
				refresh := freshUntil.Add(-time.Nanosecond)
				final := refresh
				if atBoundary {
					final = freshUntil
				}
				base := fixture.m.data
				clock := &directNoticeExactSequencedClockData{
					inner: base, refresh: model.NewTimestamp(refresh), final: model.NewTimestamp(final),
				}
				observer := &directNoticeMutateObserverData{inner: clock}
				fixture.m.data = observer
				result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
					ctx, fixture.scope, fixture.ref, target, command,
				)
				fixture.m.data = base
				if result != (DirectNoticeDeliveryAckResult{}) {
					t.Fatalf("%s exact Ack returned result at %s: %+v", outcome, name, result)
				}
				if atBoundary {
					assertDirectNoticeAckUnknownOnly(t, err)
				} else if !errors.Is(err, want) || errors.Is(err, ErrCommunicationEvidenceUnknown) {
					t.Fatalf("%s exact Ack before boundary = %v, want only %v", outcome, err, want)
				}
				if clock.calls.Load() != 3 || observer.mutates.Load() != 1 ||
					observer.views.Load() < 1 {
					t.Fatalf(
						"%s exact Ack %s clock/view/mutate = %d/%d/%d, want 3/>=1/1",
						outcome, name, clock.calls.Load(), observer.views.Load(), observer.mutates.Load(),
					)
				}
				assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
			})
		}
	}
}

func TestDirectNoticeExactAckRejectsMessageEventSequenceSkewBeforeEffects(t *testing.T) {
	t.Parallel()

	for _, skew := range []struct {
		name    string
		version int64
		seq     int64
	}{
		{name: "version_ahead", version: 5, seq: 1},
		{name: "event_sequence_ahead", version: 2, seq: 5},
	} {
		for _, scenario := range []string{
			"valid",
			"stale_if_match",
			"existing_ack",
			"retracted",
		} {
			scenario := scenario
			t.Run(skew.name+"/"+scenario, func(t *testing.T) {
				fixture := newDirectNoticeExactAckFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				_, delivery := directNoticeExactAckMessageAndDelivery(
					t, fixture.directNoticeFixture,
				)
				command := directNoticeExactAckCommand(delivery.Version, model.NewID())
				switch scenario {
				case "stale_if_match":
					command.IfMatch = fmt.Sprintf(`"v%d"`, delivery.Version+1)
				case "existing_ack":
					first, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
						ctx,
						fixture.scope,
						fixture.ref,
						fixture.published.DeliveryID,
						command,
					)
					if err != nil {
						t.Fatalf("seed exact Ack before Message skew: %v", err)
					}
					command = directNoticeExactAckCommand(first.Version, model.NewID())
				case "retracted":
					_, delivery = directNoticeExactAckRetractDelivery(t, fixture)
					command = directNoticeExactAckCommand(delivery.Version, model.NewID())
				}
				before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
				base := fixture.m.data
				fault := &directNoticeExactAckAuthorityFirstData{
					inner: base, skewMessage: true,
					messageVersion: skew.version, messageEventSeq: skew.seq,
				}
				observer := &directNoticeMutateObserverData{inner: fault}
				fixture.m.data = observer
				result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
					ctx,
					fixture.scope,
					fixture.ref,
					fixture.published.DeliveryID,
					command,
				)
				fixture.m.data = base
				assertDirectNoticeAckUnknownOnly(t, err)
				if result != (DirectNoticeDeliveryAckResult{}) ||
					observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
					fault.authorityLocks.Load() != 1 || fault.earlyAccess.Load() {
					t.Fatalf(
						"%s/%s skewed exact Ack = %+v, %v; view/mutate/authority/early %d/%d/%d/%t",
						skew.name,
						scenario,
						result,
						err,
						observer.views.Load(),
						observer.mutates.Load(),
						fault.authorityLocks.Load(),
						fault.earlyAccess.Load(),
					)
				}
				assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
			})
		}
	}
}

func TestDirectNoticeExactAckReplayReconstructsExactLockedProjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		unsealed bool
		mutate   func(*directNoticeAckReplayCandidate)
	}{
		{name: "outer_candidate_seal", unsealed: true, mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.Replayed = true
		}},
		{name: "conflict_recompute", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.conflict = !candidate.conflict
		}},
		{name: "fulfillment_pending", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.Fulfillment = FulfillmentProjection{
				State: FulfillmentPending, Required: 1, Viable: 1,
			}
		}},
		{name: "fulfillment_unmet", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.Fulfillment = FulfillmentProjection{
				State: FulfillmentUnmet, Required: 1, Unmet: 1,
			}
		}},
		{name: "command_id", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.CommandID = model.NewID()
		}},
		{name: "event_id", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.EventID = model.NewID()
		}},
		{name: "audit_seq", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.AuditSeq++
		}},
		{name: "etag", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.ETag = `"v999"`
		}},
		{name: "late", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.Late = !candidate.result.Late
		}},
		{name: "version", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.Version--
		}},
		{name: "message_event_seq", mutate: func(candidate *directNoticeAckReplayCandidate) {
			candidate.result.messageEventSeq--
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				command,
			); err != nil {
				t.Fatalf("seed exact Ack fulfillment replay: %v", err)
			}
			principal := CommunicationPrincipal{UserID: fixture.sender}
			normalized, err := normalizeDirectNoticeDeliveryAckCommand(
				fixture.scope,
				principal,
				fixture.published.DeliveryID,
				command,
			)
			if err != nil {
				t.Fatalf("normalize exact Ack fulfillment replay: %v", err)
			}
			candidate, err := fixture.m.lookupDirectNoticeAckReplay(ctx, normalized)
			if err != nil || !candidate.found || candidate.conflict {
				t.Fatalf("lookup exact Ack fulfillment replay = %+v, %v", candidate, err)
			}
			test.mutate(&candidate)
			if !test.unsealed {
				candidate.seal, err = directNoticeAckReplayCandidateCommitment(candidate)
				if err != nil {
					t.Fatalf("reseal exact Ack fulfillment replay candidate: %v", err)
				}
			}
			question, err := newCommunicationAuthorityQuestion(
				fixture.scope,
				messageDeliveryKind,
				fixture.published.DeliveryID,
				CommunicationDeliveryWrite,
			)
			if err != nil {
				t.Fatalf("build exact Ack fulfillment question: %v", err)
			}
			bound, err := fixture.m.bindCurrentCommunicationRequestAuthority(
				ctx, fixture.ref, question,
			)
			if err != nil {
				t.Fatalf("bind exact Ack fulfillment authority: %v", err)
			}
			inspected, err := bound.contextFor(question)
			if err != nil {
				t.Fatalf("inspect exact Ack fulfillment authority: %v", err)
			}
			identity, err := fixture.m.preflightDirectNoticeReaderIdentity(
				ctx, fixture.scope, inspected.principal, nil,
			)
			if err != nil {
				t.Fatalf("preflight exact Ack fulfillment reader: %v", err)
			}
			window, err := directNoticeReaderAuthorityWindow(identity)
			if err != nil {
				t.Fatalf("build exact Ack fulfillment window: %v", err)
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			authorityFirst := &directNoticeExactAckAuthorityFirstData{inner: base}
			observer := &directNoticeMutateObserverData{inner: authorityFirst}
			fixture.m.data = observer
			err = fixture.m.confirmDirectNoticeAckReplayWithAuthority(
				ctx,
				question,
				bound,
				inspected,
				identity,
				window,
				normalized,
				candidate,
			)
			fixture.m.data = base
			assertDirectNoticeAckUnknownOnly(t, err)
			wantMutates, wantAuthorityLocks := int64(1), int64(1)
			wantCarrierAccess := true
			if test.unsealed {
				wantMutates, wantAuthorityLocks = 0, 0
				wantCarrierAccess = false
			}
			if observer.views.Load() != 0 || observer.mutates.Load() != wantMutates ||
				authorityFirst.authorityLocks.Load() != wantAuthorityLocks ||
				authorityFirst.earlyAccess.Load() ||
				(authorityFirst.operations.Load() > 0) != wantCarrierAccess {
				t.Fatalf(
					"%s exact projection replay = %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
					test.name,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					authorityFirst.authorityLocks.Load(),
					authorityFirst.earlyAccess.Load(),
					authorityFirst.operations.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
		})
	}
}

func TestDirectNoticeExactAckReplayRejectsVerifiedAuditAnchorMismatch(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	seed, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
	)
	if err != nil {
		t.Fatalf("seed verified-audit exact Ack replay: %v", err)
	}
	var (
		anchor model.AuditEvent
		meta   string
		found  bool
	)
	err = fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
		if !ok {
			return errors.New("exact Ack fixture lacks verified audit reader")
		}
		var readErr error
		anchor, meta, found, readErr = reader.ReadVerifiedAuditAnchor(ctx, seed.AuditSeq)
		return readErr
	})
	if err != nil || !found {
		t.Fatalf("read verified exact Ack audit anchor = found %t, err %v", found, err)
	}
	anchor.TargetID = model.NewID()
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		viewAudit: directNoticeReplayAuditLog{
			events: []model.AuditEvent{anchor}, meta: meta,
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 0 || fault.authorityLocks.Load() != 0 ||
		fault.earlyAccess.Load() || fault.operations.Load() != 0 {
		t.Fatalf(
			"mismatched verified-audit exact Ack replay = %+v, %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckReplayRejectsLockedReceiptRowDrift(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
	); err != nil {
		t.Fatalf("seed locked-receipt exact Ack replay: %v", err)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			if kind == communicationCommandKind {
				record[model.ColID] = model.NewID().String()
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.earlyAccess.Load() || fault.operations.Load() == 0 {
		t.Fatalf(
			"drifted locked-receipt exact Ack replay = %+v, %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckReplayRejectsInvalidReceiptModel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*CommunicationCommandReceipt)
	}{
		{name: "primary_id", mutate: func(receipt *CommunicationCommandReceipt) {
			receipt.ID = model.ID("123e4567-e89b-42d3-a456-426614174000")
		}},
		{name: "version", mutate: func(receipt *CommunicationCommandReceipt) {
			receipt.Version = 2
		}},
		{name: "response_digest", mutate: func(receipt *CommunicationCommandReceipt) {
			receipt.ResponseDigest = append([]byte(nil), receipt.ResponseDigest...)
			receipt.ResponseDigest[0] ^= 0xff
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			); err != nil {
				t.Fatalf("seed %s invalid-receipt exact Ack replay: %v", test.name, err)
			}
			normalized, err := normalizeDirectNoticeDeliveryAckCommand(
				fixture.scope,
				CommunicationPrincipal{UserID: fixture.sender},
				fixture.published.DeliveryID,
				command,
			)
			if err != nil {
				t.Fatalf("normalize %s invalid-receipt exact Ack replay: %v", test.name, err)
			}
			candidate, err := fixture.m.lookupDirectNoticeAckReplay(ctx, normalized)
			if err != nil || !candidate.found || candidate.conflict {
				t.Fatalf(
					"lookup %s invalid-receipt exact Ack replay = %+v, %v",
					test.name,
					candidate,
					err,
				)
			}
			receipt := candidate.receipt
			test.mutate(&receipt)
			if ValidateCommunicationCommandReceipt(receipt) == nil {
				t.Fatalf("%s receipt mutation remained model-valid", test.name)
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			observer := &directNoticeMutateObserverData{inner: base}
			fixture.m.data = observer
			var result DirectNoticeDeliveryAckResult
			err = fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
				result, err = directNoticeAckResultFromReceipt(
					ctx,
					func(kind model.Kind) (communicationReadRepository, error) {
						return sc.Ext(kind)
					},
					normalized,
					receipt,
				)
				return err
			})
			fixture.m.data = base
			assertDirectNoticeAckUnknownOnly(t, err)
			if result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 1 || observer.mutates.Load() != 0 {
				t.Fatalf(
					"%s invalid-receipt exact Ack replay = %+v, %v; view/mutate %d/%d",
					test.name,
					result,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(
				t, fixture.directNoticeFixture, before,
			)
		})
	}
}

func TestDirectNoticeExactAckReplayRejectsCurrentFulfillmentDrift(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
	); err != nil {
		t.Fatalf("seed current-fulfillment exact Ack replay: %v", err)
	}
	normalized, err := normalizeDirectNoticeDeliveryAckCommand(
		fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender},
		fixture.published.DeliveryID,
		command,
	)
	if err != nil {
		t.Fatalf("normalize current-fulfillment exact Ack replay: %v", err)
	}
	candidate, err := fixture.m.lookupDirectNoticeAckReplay(ctx, normalized)
	if err != nil || !candidate.found || candidate.conflict {
		t.Fatalf("lookup current-fulfillment exact Ack replay = %+v, %v", candidate, err)
	}
	candidate.result.Fulfillment = FulfillmentProjection{
		State: FulfillmentPending, Required: 1, Viable: 1,
	}
	candidate.receipt.ResponseProjectionJSON.Counts = map[string]int64{
		"required": 1, "acknowledged": 0, "viable": 1, "unmet": 0, "quorum": 0,
	}
	binding, err := CanonicalCommunicationReceiptResponseBinding(candidate.receipt)
	if err != nil {
		t.Fatalf("bind current-fulfillment exact Ack receipt: %v", err)
	}
	candidate.receipt.ResponseDigest = hashBytes(binding)
	if err := ValidateCommunicationCommandReceipt(candidate.receipt); err != nil {
		t.Fatalf("validate current-fulfillment exact Ack receipt: %v", err)
	}
	receiptRecord, err := communicationCommandReceiptToRecord(candidate.receipt)
	if err != nil {
		t.Fatalf("encode current-fulfillment exact Ack receipt: %v", err)
	}
	eventPayload, err := canonicalDirectNoticeAckEventPayload(
		candidate.result, candidate.receipt.PlanHash,
	)
	if err != nil {
		t.Fatalf("encode current-fulfillment exact Ack Event: %v", err)
	}
	candidate.seal, err = directNoticeAckReplayCandidateCommitment(candidate)
	if err != nil {
		t.Fatalf("seal current-fulfillment exact Ack replay: %v", err)
	}
	question, err := newCommunicationAuthorityQuestion(
		fixture.scope,
		messageDeliveryKind,
		fixture.published.DeliveryID,
		CommunicationDeliveryWrite,
	)
	if err != nil {
		t.Fatalf("build current-fulfillment exact Ack question: %v", err)
	}
	bound, err := fixture.m.bindCurrentCommunicationRequestAuthority(ctx, fixture.ref, question)
	if err != nil {
		t.Fatalf("bind current-fulfillment exact Ack authority: %v", err)
	}
	inspected, err := bound.contextFor(question)
	if err != nil {
		t.Fatalf("inspect current-fulfillment exact Ack authority: %v", err)
	}
	identity, err := fixture.m.preflightDirectNoticeReaderIdentity(
		ctx, fixture.scope, inspected.principal, nil,
	)
	if err != nil {
		t.Fatalf("preflight current-fulfillment exact Ack reader: %v", err)
	}
	window, err := directNoticeReaderAuthorityWindow(identity)
	if err != nil {
		t.Fatalf("build current-fulfillment exact Ack authority window: %v", err)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			switch kind {
			case communicationCommandKind:
				return directNoticeExactAckReplaceRecord(record, receiptRecord)
			case workEventKind:
				record[colEventPayload] = string(eventPayload)
				record[colEventPayloadHash] = hashBytes(eventPayload)
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	err = fixture.m.confirmDirectNoticeAckReplayWithAuthority(
		ctx,
		question,
		bound,
		inspected,
		identity,
		window,
		normalized,
		candidate,
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if observer.views.Load() != 0 || observer.mutates.Load() != 1 ||
		fault.authorityLocks.Load() != 1 || fault.earlyAccess.Load() ||
		fault.operations.Load() == 0 {
		t.Fatalf(
			"current-fulfillment exact Ack replay = %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckReplayRejectsRelabelledEventAndOutboxAnchors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		kind   model.Kind
		column string
		value  func(*directNoticeExactAckFixture) any
	}{
		{name: "event_primary_id", kind: workEventKind, column: model.ColID, value: func(*directNoticeExactAckFixture) any {
			// Parseable and canonical text, but deliberately UUIDv4 rather than
			// the UUIDv7 required for an authority-bearing communication row.
			return "123e4567-e89b-42d3-a456-426614174000"
		}},
		{name: "event_tenant", kind: workEventKind, column: model.ColTenantID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "event_version", kind: workEventKind, column: model.ColVersion, value: func(*directNoticeExactAckFixture) any {
			return int64(2)
		}},
		{name: "event_id", kind: workEventKind, column: colEventID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "event_workspace", kind: workEventKind, column: colWorkWorkspaceID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "event_type", kind: workEventKind, column: colEventType, value: func(*directNoticeExactAckFixture) any {
			return "work.message.relabelled"
		}},
		{name: "event_created_at", kind: workEventKind, column: model.ColCreatedAt, value: func(*directNoticeExactAckFixture) any {
			return model.NewTimestamp(time.Now().UTC().Add(time.Hour)).String()
		}},
		{name: "event_updated_at", kind: workEventKind, column: model.ColUpdatedAt, value: func(*directNoticeExactAckFixture) any {
			return model.NewTimestamp(time.Now().UTC().Add(time.Hour)).String()
		}},
		{name: "event_payload_hash", kind: workEventKind, column: colEventPayloadHash, value: func(*directNoticeExactAckFixture) any {
			return hashBytes([]byte("tampered exact Ack Event payload hash"))
		}},
		{name: "event_audit_hash", kind: workEventKind, column: colEventAuditHash, value: func(*directNoticeExactAckFixture) any {
			return hashBytes([]byte("tampered exact Ack Event audit hash"))
		}},
		{name: "outbox_tenant", kind: workOutboxKind, column: model.ColTenantID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "outbox_event_id", kind: workOutboxKind, column: colOutboxEventID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "outbox_workspace", kind: workOutboxKind, column: colWorkWorkspaceID, value: func(*directNoticeExactAckFixture) any {
			return model.NewID().String()
		}},
		{name: "outbox_created_at", kind: workOutboxKind, column: model.ColCreatedAt, value: func(*directNoticeExactAckFixture) any {
			return model.NewTimestamp(time.Now().UTC().Add(time.Hour)).String()
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			); err != nil {
				t.Fatalf("seed %s exact Ack replay: %v", test.name, err)
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: base, transformView: true,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == test.kind {
						record[test.column] = test.value(&fixture)
					}
					return record
				},
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx, fixture.scope, fixture.ref, fixture.published.DeliveryID, command,
			)
			fixture.m.data = base
			assertDirectNoticeAckUnknownOnly(t, err)
			if result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 1 || observer.mutates.Load() != 0 {
				t.Fatalf(
					"%s exact Ack replay = %+v, %v; view/mutate %d/%d",
					test.name, result, err, observer.views.Load(), observer.mutates.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
		})
	}
}

func TestDirectNoticeExactAckUsesHistoricalStorageGenerationAfterChannelUpgrade(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := fixture.m.mutateCommunication(ctx, fixture.scope, func(tx *communicationTx) error {
		record, err := tx.lockRecord(ctx, channelKind, fixture.channel.ID)
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
		_, err = tx.update(ctx, channelKind, updated)
		return err
	})
	if err != nil {
		t.Fatalf("seal exact Ack Channel fixture: %v", err)
	}
	message, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	base := fixture.m.data
	authorityFirst := &directNoticeExactAckAuthorityFirstData{inner: base}
	observer := &directNoticeMutateObserverData{inner: authorityFirst}
	fixture.m.data = observer
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		command,
	)
	if err != nil || result.State != DeliveryAcknowledged || result.Replayed ||
		message.Payload.Encoding != PayloadPlainJSON ||
		observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
		authorityFirst.authorityLocks.Load() != 1 || authorityFirst.earlyAccess.Load() {
		t.Fatalf(
			"historical-storage exact Ack = %+v, %v; view/mutate/authority/early %d/%d/%d/%t",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			authorityFirst.authorityLocks.Load(),
			authorityFirst.earlyAccess.Load(),
		)
	}
	replayed, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		command,
	)
	fixture.m.data = base
	wantReplay := result
	wantReplay.Replayed = true
	if err != nil || !reflect.DeepEqual(replayed, wantReplay) ||
		observer.views.Load() != 2 || observer.mutates.Load() != 2 ||
		authorityFirst.authorityLocks.Load() != 2 || authorityFirst.earlyAccess.Load() {
		t.Fatalf(
			"historical-storage exact Ack replay = %+v, %v; want %+v; view/mutate/authority/early %d/%d/%d/%t",
			replayed,
			err,
			wantReplay,
			observer.views.Load(),
			observer.mutates.Load(),
			authorityFirst.authorityLocks.Load(),
			authorityFirst.earlyAccess.Load(),
		)
	}
}

func TestDirectNoticeExactAckRejectsValidCurrentApplicationSealedCarrier(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	message, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	sealedChannel := fixture.channel
	sealedChannel.ContentProtection = ContentProtectionApplicationSealed
	sealedChannel.ProtectionGeneration++
	sealedChannel.Version++
	if err := ValidateChannelUpdate(fixture.channel, sealedChannel); err != nil {
		t.Fatalf("build current application-sealed Channel: %v", err)
	}
	sealedMessage := message
	sealedMessage.Payload = communicationStateTestSealedPayload(
		PayloadSlotMessage, sealedChannel.ProtectionGeneration,
	)
	if err := ValidateMessageForPublishChannel(sealedMessage, sealedChannel, 1); err != nil {
		t.Fatalf("build valid current application-sealed Message: %v", err)
	}
	channelRecord, err := channelToRecord(sealedChannel)
	if err != nil {
		t.Fatalf("encode current application-sealed Channel: %v", err)
	}
	messageRecord, err := messageToRecord(sealedMessage, 1)
	if err != nil {
		t.Fatalf("encode current application-sealed Message: %v", err)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			switch kind {
			case channelKind:
				return directNoticeExactAckReplaceRecord(record, channelRecord)
			case messageKind:
				return directNoticeExactAckReplaceRecord(record, messageRecord)
			default:
				return record
			}
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.earlyAccess.Load() || fault.operations.Load() == 0 {
		t.Fatalf(
			"current application-sealed exact Ack = %+v, %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckHistoricalStorageCarrierIsGenerationBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		protection ContentProtection
		channelGen int64
		encoding   PayloadEncoding
		messageGen int64
		want       bool
	}{
		{name: "current_storage", protection: ContentProtectionStorage, channelGen: 1,
			encoding: PayloadPlainJSON, messageGen: 1, want: true},
		{name: "historical_storage_after_upgrade", protection: ContentProtectionApplicationSealed,
			channelGen: 2, encoding: PayloadPlainJSON, messageGen: 1, want: true},
		{name: "sealed_historical_generation", protection: ContentProtectionApplicationSealed,
			channelGen: 2, encoding: PayloadSealedV1, messageGen: 1},
		{name: "historical_storage_after_sensitivity_change", protection: ContentProtectionStorage,
			channelGen: 2, encoding: PayloadPlainJSON, messageGen: 1, want: true},
		{name: "future_storage_generation", protection: ContentProtectionStorage,
			channelGen: 2, encoding: PayloadPlainJSON, messageGen: 3},
		{name: "sealed_current_generation", protection: ContentProtectionApplicationSealed,
			channelGen: 2, encoding: PayloadSealedV1, messageGen: 2},
		{name: "plain_claimed_as_sealed_generation", protection: ContentProtectionApplicationSealed,
			channelGen: 2, encoding: PayloadPlainJSON, messageGen: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := directNoticeAckHistoricalStorageCarrier(
				Channel{
					ContentProtection:    test.protection,
					ProtectionGeneration: test.channelGen,
				},
				Message{Payload: ProtectedPayload{
					Encoding: test.encoding, ProtectionGeneration: test.messageGen,
				}},
			)
			if got != test.want {
				t.Fatalf("historical storage carrier = %t, want %t", got, test.want)
			}
		})
	}
}

func TestDirectNoticeExactAckRejectsFutureDatedLateEvidence(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckRetractDelivery(t, fixture)
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	first, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		command,
	)
	if err != nil || !first.Late || first.State != DeliveryRetracted {
		t.Fatalf("seed future-evidence late Ack = %+v, %v", first, err)
	}
	ackRows := communicationRowsForTest(t, fixture.directNoticeFixture, messageAckKind)
	if len(ackRows) != 1 {
		t.Fatalf("future-evidence Ack rows = %d, want 1", len(ackRows))
	}
	ack, err := messageAckFromRecord(ackRows[0])
	if err != nil || ack.AcknowledgedAt.Before(delivery.UpdatedAt) {
		t.Fatalf(
			"stored late Ack time = %s, Delivery UpdatedAt = %s, err=%v",
			ack.AcknowledgedAt,
			delivery.UpdatedAt,
			err,
		)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	future := fixture.source.evidence.FreshUntil.Add(time.Hour)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base, transformView: true,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			switch kind {
			case messageAckKind:
				record[model.ColCreatedAt] = model.NewTimestamp(future).String()
				record[colCommAcknowledgedAt] = model.NewTimestamp(future).String()
			case communicationCommandKind:
				record[model.ColCreatedAt] = model.NewTimestamp(future).String()
				record[colCommCompletedAt] = model.NewTimestamp(future).String()
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer

	beforeViews, beforeMutates := observer.views.Load(), observer.mutates.Load()
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) ||
		observer.views.Load() != beforeViews+1 || observer.mutates.Load() != beforeMutates+1 {
		t.Fatalf(
			"different-key future late Ack = %+v, %v; View/Mutate delta %d/%d",
			result,
			err,
			observer.views.Load()-beforeViews,
			observer.mutates.Load()-beforeMutates,
		)
	}

	beforeViews, beforeMutates = observer.views.Load(), observer.mutates.Load()
	result, err = fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		command,
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) ||
		observer.views.Load() != beforeViews+1 || observer.mutates.Load() != beforeMutates {
		t.Fatalf(
			"replayed future late Ack = %+v, %v; View/Mutate delta %d/%d",
			result,
			err,
			observer.views.Load()-beforeViews,
			observer.mutates.Load()-beforeMutates,
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckRejectsFutureDatedLockedChannel(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	future := time.Now().UTC().Add(time.Minute)
	if !future.Before(fixture.source.evidence.FreshUntil) {
		t.Fatalf(
			"future locked Channel time %s does not preserve authority through %s",
			future,
			fixture.source.evidence.FreshUntil,
		)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			if kind == channelKind {
				record[model.ColUpdatedAt] = model.NewTimestamp(future).String()
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.earlyAccess.Load() || fault.operations.Load() == 0 {
		t.Fatalf(
			"future locked Channel exact Ack = %+v, %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckRejectsLockedDirectoryEpochDrift(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformDirectoryEpoch: func(epoch model.DirectoryEpoch) model.DirectoryEpoch {
			epoch.Version++
			return epoch
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.directoryEpochReads.Load() != 1 || fault.earlyAccess.Load() ||
		fault.operations.Load() == 0 {
		t.Fatalf(
			"drifted locked epoch exact Ack = %+v, %v; view/mutate/authority/epoch/early/carrier %d/%d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.directoryEpochReads.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckRollsBackEveryWriteFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		kind      model.Kind
		operation string
		late      bool
		audit     bool
	}{
		{name: "timely_delivery_cas", kind: messageDeliveryKind, operation: "update"},
		{
			name: "materialized_late_delivery_cas", kind: messageDeliveryKind,
			operation: "update", late: true,
		},
		{name: "ack_create", kind: messageAckKind, operation: "create_with_id"},
		{name: "message_cas", kind: messageKind, operation: "update"},
		{name: "event_create", kind: workEventKind, operation: "create"},
		{name: "outbox_create", kind: workOutboxKind, operation: "create"},
		{name: "receipt_create", kind: communicationCommandKind, operation: "create_with_id"},
		{name: "audit_append", audit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			if test.late {
				fixture = newDirectNoticeExactAckFixtureWithTimeout(t, 1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if test.late {
				if delivery.AckDueAt == nil {
					t.Fatal("materialized-late write fault lacks AckDueAt")
				}
				waitDirectNoticeExactAckDBTime(
					t, fixture.directNoticeFixture, *delivery.AckDueAt,
				)
			}
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			failure := errors.New("injected exact Ack " + test.name + " failure")
			var faultData api.ModuleData
			if test.audit {
				faultData = directNoticeCursorAuditFailureData{inner: base, failure: failure}
			} else {
				faultData = &directNoticeExactAckWriteFailureData{
					inner: base, kind: test.kind, operation: test.operation, failure: failure,
				}
			}
			observer := &directNoticeMutateObserverData{inner: faultData}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				command,
			)
			fixture.m.data = base
			if !errors.Is(err, failure) || result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 1 || observer.mutates.Load() != 1 {
				t.Fatalf(
					"%s exact Ack = %+v, %v; view/mutate %d/%d",
					test.name,
					result,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
			retry, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				command,
			)
			if err != nil || retry.Replayed || retry.AckID == "" || retry.CommandID == "" {
				t.Fatalf("retry after %s exact Ack failure = %+v, %v", test.name, retry, err)
			}
		})
	}
}

func TestDirectNoticeExactAckDefersScheduledTerminalUntilFinalAuthoritySample(t *testing.T) {
	t.Parallel()

	for _, atBoundary := range []bool{false, true} {
		atBoundary := atBoundary
		name := "final_before_boundary"
		if atBoundary {
			name = "final_at_boundary"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			freshUntil := fixture.source.evidence.FreshUntil.UTC()
			refresh := freshUntil.Add(-time.Nanosecond)
			final := refresh
			if atBoundary {
				final = freshUntil
			}
			base := fixture.m.data
			clock := &directNoticeExactSequencedClockData{
				inner: base, refresh: model.NewTimestamp(refresh), final: model.NewTimestamp(final),
			}
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: clock,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageDeliveryKind || kind == messageKind {
						availableAt := freshUntil.Add(time.Minute)
						ackDueAt := availableAt.Add(time.Minute)
						record[colCommAvailableAt] = model.NewTimestamp(availableAt).String()
						record[colCommAckDueAt] = model.NewTimestamp(ackDueAt).String()
					}
					return record
				},
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			fixture.m.data = base
			if result != (DirectNoticeDeliveryAckResult{}) {
				t.Fatalf("scheduled exact Ack returned result at %s: %+v", name, result)
			}
			if atBoundary {
				assertDirectNoticeAckUnknownOnly(t, err)
			} else if !errors.Is(err, ErrCommunicationTerminal) ||
				errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("scheduled exact Ack before boundary = %v, want Terminal only", err)
			}
			if clock.calls.Load() != 3 || observer.views.Load() != 1 ||
				observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
				fault.earlyAccess.Load() {
				t.Fatalf(
					"scheduled exact Ack %s clock/view/mutate/authority/early = %d/%d/%d/%d/%t",
					name,
					clock.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					fault.authorityLocks.Load(),
					fault.earlyAccess.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
		})
	}
}

func TestDirectNoticeExactAckNarrowsTimelyCommitToDeliveryDeadline(t *testing.T) {
	t.Parallel()

	for _, atBoundary := range []bool{false, true} {
		atBoundary := atBoundary
		name := "final_before_due"
		if atBoundary {
			name = "final_at_due"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if delivery.AckDueAt == nil {
				t.Fatal("timely exact Ack fixture lacks AckDueAt")
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			final := delivery.AckDueAt.Add(-time.Nanosecond)
			if atBoundary {
				final = *delivery.AckDueAt
			}
			base := fixture.m.data
			clock := &directNoticeFinalExpiryData{
				inner: base, final: model.NewTimestamp(final),
			}
			observer := &directNoticeMutateObserverData{inner: clock}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			fixture.m.data = base
			if atBoundary {
				assertDirectNoticeAckUnknownOnly(t, err)
				if result != (DirectNoticeDeliveryAckResult{}) {
					t.Fatalf("timely exact Ack returned result at due: %+v", result)
				}
				assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
			} else if err != nil || result.Replayed || result.Late ||
				result.State != DeliveryAcknowledged || result.AckID == "" || result.CommandID == "" {
				t.Fatalf("timely exact Ack before due = %+v, %v", result, err)
			}
			if clock.calls.Load() != 3 || observer.views.Load() != 1 ||
				observer.mutates.Load() != 1 {
				t.Fatalf(
					"timely exact Ack %s clock/view/mutate = %d/%d/%d, want 3/1/1",
					name, clock.calls.Load(), observer.views.Load(), observer.mutates.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactAckUsesEarliestDeliveryDeadline(t *testing.T) {
	t.Parallel()

	for _, atBoundary := range []bool{false, true} {
		atBoundary := atBoundary
		name := "final_before_earliest"
		if atBoundary {
			name = "final_at_earliest"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if delivery.AckDueAt == nil || delivery.ExpiresAt != nil {
				t.Fatalf(
					"earliest-deadline fixture AckDueAt/ExpiresAt = %v/%v",
					delivery.AckDueAt,
					delivery.ExpiresAt,
				)
			}
			expiresAt := delivery.AckDueAt.Add(time.Minute)
			if !delivery.AckDueAt.Before(expiresAt) {
				t.Fatalf("Ack deadline %s is not before expiry %s", delivery.AckDueAt, expiresAt)
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			final := delivery.AckDueAt.Add(-time.Nanosecond)
			if atBoundary {
				final = *delivery.AckDueAt
			}
			base := fixture.m.data
			clock := &directNoticeFinalExpiryData{
				inner: base, final: model.NewTimestamp(final),
			}
			// DirectNotice publish has no expiry input. The locked read transform
			// supplies a valid carrier with two distinct deadlines; the write
			// transform preserves the real fixture's immutable no-expiry row.
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: clock,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageKind || kind == messageDeliveryKind {
						record[colCommExpiresAt] = model.NewTimestamp(expiresAt).String()
					}
					return record
				},
				transformWrite: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageKind || kind == messageDeliveryKind {
						record[colCommExpiresAt] = nil
					}
					return record
				},
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			fixture.m.data = base
			if atBoundary {
				assertDirectNoticeAckUnknownOnly(t, err)
				if result != (DirectNoticeDeliveryAckResult{}) {
					t.Fatalf("two-deadline exact Ack returned result at earliest: %+v", result)
				}
				assertDirectNoticeExactAckEffectsUnchanged(
					t, fixture.directNoticeFixture, before,
				)
			} else if err != nil || result.Replayed || result.Late ||
				result.State != DeliveryAcknowledged || result.AckID == "" || result.CommandID == "" {
				t.Fatalf("two-deadline exact Ack before earliest = %+v, %v", result, err)
			}
			if clock.calls.Load() != 3 || observer.views.Load() != 1 ||
				observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
				fault.earlyAccess.Load() {
				t.Fatalf(
					"two-deadline exact Ack %s clock/view/mutate/authority/early = %d/%d/%d/%d/%t",
					name,
					clock.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					fault.authorityLocks.Load(),
					fault.earlyAccess.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactAckNarrowsOptionalCommitToExpiryDeadline(t *testing.T) {
	t.Parallel()

	for _, atBoundary := range []bool{false, true} {
		atBoundary := atBoundary
		name := "final_before_expiry"
		if atBoundary {
			name = "final_at_expiry"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckOptionalFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			if delivery.Required || delivery.AckDueAt != nil || delivery.ExpiresAt != nil {
				t.Fatalf(
					"optional exact Ack fixture required/due/expiry = %t/%v/%v",
					delivery.Required, delivery.AckDueAt, delivery.ExpiresAt,
				)
			}
			expiresAt := time.Now().UTC().Add(time.Minute)
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			final := expiresAt.Add(-time.Nanosecond)
			if atBoundary {
				final = expiresAt
			}
			base := fixture.m.data
			clock := &directNoticeFinalExpiryData{
				inner: base, final: model.NewTimestamp(final),
			}
			// DirectNotice publish intentionally has no expiry input. The read
			// transform supplies a valid immutable expiry-only carrier to the Ack
			// seam; the write transform keeps SQLite's real fixture row from
			// treating that test-only setup projection as an expiry mutation.
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: clock,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageKind || kind == messageDeliveryKind {
						record[colCommExpiresAt] = model.NewTimestamp(expiresAt).String()
					}
					return record
				},
				transformWrite: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageKind || kind == messageDeliveryKind {
						record[colCommExpiresAt] = nil
					}
					return record
				},
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			fixture.m.data = base
			if atBoundary {
				assertDirectNoticeAckUnknownOnly(t, err)
				if result != (DirectNoticeDeliveryAckResult{}) {
					t.Fatalf("optional exact Ack returned result at expiry: %+v", result)
				}
				assertDirectNoticeExactAckEffectsUnchanged(
					t, fixture.directNoticeFixture, before,
				)
			} else if err != nil || result.Replayed || result.Late ||
				result.State != DeliveryAcknowledged || result.AckID == "" || result.CommandID == "" {
				t.Fatalf("optional exact Ack before expiry = %+v, %v", result, err)
			}
			if clock.calls.Load() != 3 || observer.views.Load() != 1 ||
				observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
				fault.earlyAccess.Load() {
				t.Fatalf(
					"optional exact Ack %s clock/view/mutate/authority/early = %d/%d/%d/%d/%t",
					name,
					clock.calls.Load(),
					observer.views.Load(),
					observer.mutates.Load(),
					fault.authorityLocks.Load(),
					fault.earlyAccess.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactAckRejectsExpiredCarrierBeforeItsDeadline(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	if delivery.AckDueAt == nil || !time.Now().UTC().Before(*delivery.AckDueAt) {
		t.Fatalf("expired-carrier fixture AckDueAt = %v, want future", delivery.AckDueAt)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			if kind == messageDeliveryKind {
				record[colCommState] = string(DeliveryExpired)
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		directNoticeExactAckCommand(delivery.Version, model.NewID()),
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.earlyAccess.Load() {
		t.Fatalf(
			"pre-deadline expired exact Ack = %+v, %v; view/mutate/authority/early %d/%d/%d/%t",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckRejectsRetractedDeliveryBeforeMessageTerminal(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	directNoticeExactAckRetractDelivery(t, fixture)
	message, delivery := directNoticeExactAckMessageAndDelivery(
		t, fixture.directNoticeFixture,
	)
	if message.State != MessageRetracted || message.TerminalAt == nil ||
		delivery.State != DeliveryRetracted ||
		delivery.UpdatedAt.Before(*message.TerminalAt) {
		t.Fatalf(
			"legal retracted carrier state/terminal/delivery = %s/%v/%s/%s",
			message.State,
			message.TerminalAt,
			delivery.State,
			delivery.UpdatedAt,
		)
	}
	beforeTerminal := message.TerminalAt.Add(-time.Nanosecond)
	invalidDelivery := delivery
	invalidDelivery.UpdatedAt = beforeTerminal
	if err := ValidateMessageDelivery(invalidDelivery); err != nil {
		t.Fatalf("retracted pre-terminal Delivery must remain independently valid: %v", err)
	}
	if err := ValidateMessageDeliveryLineage(message, invalidDelivery); err != nil {
		t.Fatalf("retracted pre-terminal generic lineage must remain valid: %v", err)
	}
	invalidRecord, err := messageDeliveryToRecord(invalidDelivery)
	if err != nil {
		t.Fatalf("encode retracted pre-terminal Delivery: %v", err)
	}
	before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
	base := fixture.m.data
	fault := &directNoticeExactAckAuthorityFirstData{
		inner: base,
		transformRecord: func(kind model.Kind, record model.Record) model.Record {
			if kind == messageDeliveryKind {
				return directNoticeExactAckReplaceRecord(record, invalidRecord)
			}
			return record
		},
	}
	observer := &directNoticeMutateObserverData{inner: fault}
	fixture.m.data = observer
	result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
		ctx,
		fixture.scope,
		fixture.ref,
		fixture.published.DeliveryID,
		// A stale precondition makes the retracted timestamp guard causal:
		// without it, this malformed locked lineage would leak VersionMismatch.
		directNoticeExactAckCommand(delivery.Version+1, model.NewID()),
	)
	fixture.m.data = base
	assertDirectNoticeAckUnknownOnly(t, err)
	if result != (DirectNoticeDeliveryAckResult{}) || observer.views.Load() != 1 ||
		observer.mutates.Load() != 1 || fault.authorityLocks.Load() != 1 ||
		fault.earlyAccess.Load() || fault.operations.Load() == 0 {
		t.Fatalf(
			"pre-terminal retracted exact Ack = %+v, %v; view/mutate/authority/early/carrier %d/%d/%d/%t/%d",
			result,
			err,
			observer.views.Load(),
			observer.mutates.Load(),
			fault.authorityLocks.Load(),
			fault.earlyAccess.Load(),
			fault.operations.Load(),
		)
	}
	assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
}

func TestDirectNoticeExactAckUndeliverableRequiresExactCurrentTombstone(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		found    bool
		mismatch bool
		terminal bool
	}{
		{name: "exact_tombstone", found: true, terminal: true},
		{name: "missing_tombstone"},
		{name: "mismatched_tombstone", found: true, mismatch: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			plan, witness := directNoticeExactAckUndeliverablePlan(t, fixture)
			lockedRecord, err := messageDeliveryToRecord(plan.After)
			if err != nil {
				t.Fatalf("encode exact Ack undeliverable Delivery: %v", err)
			}
			observedWitness := witness
			if test.mismatch {
				observedWitness.TombstoneID = model.NewID()
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: base,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageDeliveryKind {
						return directNoticeExactAckReplaceRecord(record, lockedRecord)
					}
					return record
				},
				overrideTombstone: true,
				tombstone:         observedWitness,
				tombstoneFound:    test.found,
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(plan.After.Version, model.NewID()),
			)
			fixture.m.data = base
			if result != (DirectNoticeDeliveryAckResult{}) {
				t.Fatalf("%s undeliverable exact Ack returned %+v", test.name, result)
			}
			if test.terminal {
				if !errors.Is(err, ErrCommunicationTerminal) ||
					errors.Is(err, ErrCommunicationEvidenceUnknown) ||
					errors.Is(err, ErrCommunicationForbidden) ||
					errors.Is(err, ErrCommunicationNotFound) ||
					errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
					t.Fatalf("exact tombstone undeliverable Ack = %v, want Terminal only", err)
				}
			} else {
				assertDirectNoticeAckUnknownOnly(t, err)
			}
			if observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
				fault.authorityLocks.Load() != 1 || fault.earlyAccess.Load() ||
				fault.operations.Load() == 0 || fault.tombstoneReads.Load() != 1 {
				t.Fatalf(
					"%s undeliverable exact Ack view/mutate/authority/early/carrier/tombstone = %d/%d/%d/%t/%d/%d",
					test.name,
					observer.views.Load(),
					observer.mutates.Load(),
					fault.authorityLocks.Load(),
					fault.earlyAccess.Load(),
					fault.operations.Load(),
					fault.tombstoneReads.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(
				t, fixture.directNoticeFixture, before,
			)
		})
	}
}

func TestDirectNoticeExactAckReplayRejectsLockedMessageCASSkew(t *testing.T) {
	t.Parallel()

	for _, skew := range []struct {
		name    string
		version int64
		seq     int64
	}{
		{name: "version_ahead", version: 5, seq: 1},
		{name: "event_sequence_ahead", version: 2, seq: 5},
		{name: "max_version", version: math.MaxInt64, seq: 1},
		{name: "max_event_sequence", version: 2, seq: math.MaxInt64},
	} {
		t.Run(skew.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			command := directNoticeExactAckCommand(delivery.Version, model.NewID())
			if _, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				command,
			); err != nil {
				t.Fatalf("seed exact Ack replay CAS skew: %v", err)
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			base := fixture.m.data
			fault := &directNoticeExactAckAuthorityFirstData{
				inner: base,
				transformRecord: func(kind model.Kind, record model.Record) model.Record {
					if kind == messageKind {
						record[model.ColVersion] = skew.version
						record[colCommLastEventSeq] = skew.seq
					}
					return record
				},
			}
			observer := &directNoticeMutateObserverData{inner: fault}
			fixture.m.data = observer
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				fixture.ref,
				fixture.published.DeliveryID,
				command,
			)
			fixture.m.data = base
			assertDirectNoticeAckUnknownOnly(t, err)
			if result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
				fault.authorityLocks.Load() != 1 || fault.earlyAccess.Load() {
				t.Fatalf(
					"%s replay CAS skew = %+v, %v; view/mutate/authority/early %d/%d/%d/%t",
					skew.name,
					result,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					fault.authorityLocks.Load(),
					fault.earlyAccess.Load(),
				)
			}
			assertDirectNoticeExactAckEffectsUnchanged(t, fixture.directNoticeFixture, before)
		})
	}
}

func TestDirectNoticeExactAckBindingFailuresDoNotOpenCommunicationData(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		configure     func(*directNoticeExactAckFixture)
		noDeadline    bool
		zeroRef       bool
		want          error
		sourceCalls   int
		resolverCalls int
	}{
		{
			name: "deny", want: ErrCommunicationForbidden, sourceCalls: 1, resolverCalls: 1,
			configure: func(f *directNoticeExactAckFixture) {
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
			name: "unknown", want: ErrCommunicationEvidenceUnknown,
			sourceCalls: 1, resolverCalls: 1,
			configure: func(f *directNoticeExactAckFixture) {
				unknown := auth.CheckEvidence{Verdict: auth.CheckUnknown, Code: "unavailable"}
				f.source.evidence = auth.AuthorizationEvidence{
					Outcome: auth.EvidenceUnknown, CorePermission: unknown,
					ResourceGuard: unknown, ForbidAbsence: unknown,
				}
			},
		},
		{
			name: "finite_deadline_required", noDeadline: true,
			want: ErrCommunicationEvidenceUnknown,
		},
		{name: "zero_ref", zeroRef: true, want: ErrCommunicationEvidenceUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			if test.configure != nil {
				test.configure(&fixture)
			}
			resolver := &communicationAuthorityResolverRecorder{resolved: fixture.authUser}
			fixture.m.useCommunicationRequestAuthoritySources(resolver, fixture.source)
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			observer := &directNoticeMutateObserverData{inner: fixture.m.data}
			fixture.m.data = observer
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
			result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
				ctx,
				fixture.scope,
				ref,
				fixture.published.DeliveryID,
				directNoticeExactAckCommand(delivery.Version, model.NewID()),
			)
			if !errors.Is(err, test.want) || result != (DirectNoticeDeliveryAckResult{}) ||
				observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
				fixture.source.calls != test.sourceCalls || resolver.calls != test.resolverCalls ||
				fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 ||
				fixture.authorizer.calls.Load() != 0 || fixture.legacyRead.calls.Load() != 0 {
				t.Fatalf(
					"%s exact Ack = %+v, %v; view/mutate/source/resolver/local/closure/legacy-op/legacy-read %d/%d/%d/%d/%d/%d/%d/%d",
					test.name,
					result,
					err,
					observer.views.Load(),
					observer.mutates.Load(),
					fixture.source.calls,
					resolver.calls,
					fixture.resolver.calls.Load(),
					fixture.closure.calls.Load(),
					fixture.authorizer.calls.Load(),
					fixture.legacyRead.calls.Load(),
				)
			}
		})
	}
}

func TestDirectNoticeExactAckNormalizationRequiresClaimFreeUser(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactAckFixture(t)
	_, delivery := directNoticeExactAckMessageAndDelivery(t, fixture.directNoticeFixture)
	command := directNoticeExactAckCommand(delivery.Version, model.NewID())
	valid := CommunicationPrincipal{UserID: fixture.sender}
	if normalized, err := normalizeDirectNoticeDeliveryAckCommand(
		fixture.scope, valid, fixture.published.DeliveryID, command,
	); err != nil || normalized.principal != valid ||
		len(normalized.actorFingerprint) != 32 || len(normalized.idempotencyKeyHash) != 32 ||
		len(normalized.requestDigest) != 32 {
		t.Fatalf("normalize claim-free User exact Ack = %+v, %v", normalized, err)
	}
	for _, principal := range []CommunicationPrincipal{
		{AgentExternalID: "agent-external"},
		{
			System: true, SystemActorRef: "system-actor",
			SystemGrantAgentID: model.NewID(),
		},
		{UserID: fixture.sender, PurposeRestricted: true},
	} {
		if _, err := normalizeDirectNoticeDeliveryAckCommand(
			fixture.scope, principal, fixture.published.DeliveryID, command,
		); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Fatalf("normalize non-User exact Ack principal %+v = %v", principal, err)
		}
	}
}

func TestDirectNoticeExactAckConcurrentCommandsCommitOneAck(t *testing.T) {
	t.Parallel()

	type callResult struct {
		result DirectNoticeDeliveryAckResult
		err    error
	}
	for _, sameKey := range []bool{true, false} {
		sameKey := sameKey
		name := "different_keys"
		if sameKey {
			name = "same_key"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDirectNoticeExactAckFixture(t)
			fixture.m.useCommunicationRequestAuthoritySources(
				fixture.authr,
				&directNoticeExactAckConcurrentSource{inner: fixture.source},
			)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, delivery := directNoticeExactAckMessageAndDelivery(
				t, fixture.directNoticeFixture,
			)
			firstKey := model.NewID()
			commands := []DirectNoticeDeliveryAckCommand{
				directNoticeExactAckCommand(delivery.Version, firstKey),
				directNoticeExactAckCommand(delivery.Version, firstKey),
			}
			if !sameKey {
				commands[1] = directNoticeExactAckCommand(delivery.Version, model.NewID())
			}
			before := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			start := make(chan struct{})
			results := make(chan callResult, len(commands))
			var wait sync.WaitGroup
			for _, command := range commands {
				command := command
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					result, err := fixture.m.acknowledgeDirectNoticeDeliveryWithAuthority(
						ctx,
						fixture.scope,
						fixture.ref,
						fixture.published.DeliveryID,
						command,
					)
					results <- callResult{result: result, err: err}
				}()
			}
			close(start)
			wait.Wait()
			close(results)
			calls := make([]callResult, 0, len(commands))
			for result := range results {
				calls = append(calls, result)
			}
			if len(calls) != 2 {
				t.Fatalf("concurrent %s exact Ack calls = %d, want 2", name, len(calls))
			}
			if sameKey {
				replays := 0
				for _, call := range calls {
					if call.err != nil {
						t.Fatalf("concurrent same-key exact Ack = %+v, %v", call.result, call.err)
					}
					if call.result.Replayed {
						replays++
					}
				}
				left, right := calls[0].result, calls[1].result
				left.Replayed = false
				right.Replayed = false
				if replays != 1 || !reflect.DeepEqual(left, right) {
					t.Fatalf("concurrent same-key exact Acks = %+v / %+v", calls[0], calls[1])
				}
			} else {
				successes, already := 0, 0
				for _, call := range calls {
					switch {
					case call.err == nil && call.result.AckID != "" && !call.result.Replayed:
						successes++
					case errors.Is(call.err, errDirectNoticeAckAlreadyAcknowledged):
						already++
					default:
						t.Fatalf("concurrent different-key exact Ack = %+v, %v", call.result, call.err)
					}
				}
				if successes != 1 || already != 1 {
					t.Fatalf(
						"concurrent different-key successes/already = %d/%d, want 1/1",
						successes, already,
					)
				}
			}
			after := directNoticeExactAckEffects(t, fixture.directNoticeFixture)
			for _, kind := range []model.Kind{
				messageAckKind, communicationCommandKind, workEventKind, workOutboxKind,
			} {
				if len(after.rows[kind]) != len(before.rows[kind])+1 {
					t.Fatalf(
						"concurrent %s exact Ack %s rows = %d, want %d",
						name, kind, len(after.rows[kind]), len(before.rows[kind])+1,
					)
				}
			}
			if after.audit.Seq != before.audit.Seq+1 {
				t.Fatalf(
					"concurrent %s exact Ack audit seq = %d, want %d",
					name, after.audit.Seq, before.audit.Seq+1,
				)
			}
		})
	}
}

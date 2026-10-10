// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// cedarEpochModuleData is a deliberately narrow module-data probe. It lets the C3
// tests remove or decorate optional scope capabilities without widening production
// seams, and can run a hook strictly after a durable View has returned its snapshot.
type cedarEpochModuleData struct {
	st         store.Store
	wrap       func(store.Scope) store.Scope
	viewWrap   func(store.Scope) store.Scope
	mutateWrap func(store.Scope) store.Scope
	afterView  func()
}

func (d cedarEpochModuleData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.st.View(ctx, tenant, func(sc store.Scope) error {
		wrap := d.wrap
		if d.viewWrap != nil {
			wrap = d.viewWrap
		}
		if wrap != nil {
			sc = wrap(sc)
		}
		return fn(sc)
	})
	if err == nil && d.afterView != nil {
		d.afterView()
	}
	return err
}

func (d cedarEpochModuleData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		wrap := d.wrap
		if d.mutateWrap != nil {
			wrap = d.mutateWrap
		}
		if wrap != nil {
			sc = wrap(sc)
		}
		return fn(sc)
	})
}

// cedarEpochScopedData is the route equivalent of cedarEpochModuleData. Its hook
// makes GET's live-before → View → live-after bracket directly observable.
type cedarEpochScopedData struct {
	st        store.Store
	tenant    model.TenantID
	viewWrap  func(store.Scope) store.Scope
	afterView func()
}

// cedarEpochGateModuleData pauses exactly the first View before it opens the
// underlying store. It exposes the committed C2 rows/epoch after their Mutate but before
// reloadTenantGrants can replace the live state, without holding SQLite's connection and
// artificially serializing the whoami probe.
type cedarEpochGateModuleData struct {
	st      store.Store
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	views   int
}

func (d *cedarEpochGateModuleData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mu.Lock()
	d.views++
	view := d.views
	d.mu.Unlock()
	if view == 1 {
		close(d.entered)
		<-d.release
	}
	return d.st.View(ctx, tenant, fn)
}

func (d *cedarEpochGateModuleData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, tenant, fn)
}

func (d cedarEpochScopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	err := d.st.View(ctx, d.tenant, func(sc store.Scope) error {
		if d.viewWrap != nil {
			sc = d.viewWrap(sc)
		}
		return fn(sc)
	})
	if err == nil && d.afterView != nil {
		d.afterView()
	}
	return err
}

func (d cedarEpochScopedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, fn)
}

func (d cedarEpochScopedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

type cedarEpochNoCapabilityScope struct{ store.Scope }

type cedarEpochZeroAudit struct{ store.AuditLog }

func (cedarEpochZeroAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, nil
}

type cedarEpochFailingAudit struct {
	store.AuditLog
	err error
}

func (a cedarEpochFailingAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, a.err
}

// cedarEpochVanishingRevisionRepo models the narrow TOCTOU that matters here:
// the selection sees its target, while the getRevision that loads it for
// latestActiveSelection does not. The row vanishes on exact read vanishAt: the
// second for an activated surface, whose activation-marker validation reads it
// first, and the first for a surface its active-row list selects. A stable
// dangling marker is already rejected elsewhere; this decorator pins the hole so
// it cannot be collapsed into an innocent empty surface.
type cedarEpochVanishingRevisionRepo struct {
	store.GenericRepo
	surface    string
	revision   int64
	vanishAt   int
	exactReads int
}

// cedarEpochRewrittenRevisionRepo models a same-selection/same-epoch durable read
// whose selected bytes drift from the compiled runtime. It only changes records handed
// to the reader; the backing immutable history and its activation are untouched.
type cedarEpochRewrittenRevisionRepo struct {
	store.GenericRepo
	surface  string
	revision int64
	content  string
}

// cedarEpochThirdReadRewriteRevisionRepo leaves activeRevisionNumber's validation
// read and latestActiveRevision's DTO read intact, but rewrites a hypothetical third
// exact row lookup. GET must not make that third read after it has captured the durable
// snapshot; otherwise it would display B alongside A's digest and live proof.
type cedarEpochThirdReadRewriteRevisionRepo struct {
	store.GenericRepo
	surface    string
	revision   int64
	content    string
	exactReads int
}

// cedarEpochWrongIdentityRevisionRepo keeps the first exact selected-row read
// coherent, then returns a differently labeled DTO for latestActiveRevision's second
// read. A selection snapshot must reject that identity tear rather than attach M's bytes
// to N's activation/epoch.
type cedarEpochWrongIdentityRevisionRepo struct {
	store.GenericRepo
	surface            string
	revision           int64
	replacementSurface string
	exactReads         int
	rewriteAt          int
}

// cedarEpochReplayThenFailRevisionRepo injects an error only once the named Cedar
// surface is read. Its replay hook models a delayed reload that captured the older
// runtime before the failing View/Mutate began, then finishes after the newer epoch or
// locked witness was observed. Keeping the hook inside List makes the ordering causal:
// the epoch/lock has already happened, while the durable snapshot is still incomplete.
type cedarEpochReplayThenFailRevisionRepo struct {
	store.GenericRepo
	surface string
	err     error
	replay  func()
	fired   bool
}

func (r *cedarEpochReplayThenFailRevisionRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	if !r.fired && cedarEpochQuerySelectsSurface(query, r.surface) {
		r.fired = true
		if r.replay != nil {
			r.replay()
		}
		return nil, model.Page{}, r.err
	}
	return r.GenericRepo.List(ctx, query)
}

func cedarEpochQuerySelectsSurface(query model.Query, surface string) bool {
	for _, filter := range query.Filters {
		if filter.Op != model.OpEq || filter.Column != colRevSurface {
			continue
		}
		value, ok := filter.Value.(string)
		return ok && value == surface
	}
	return false
}

func (r cedarEpochRewrittenRevisionRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, query)
	if err != nil {
		return nil, model.Page{}, err
	}
	for i, row := range rows {
		if row.String(colRevSurface) != r.surface || row.Int(colRevNumber) != r.revision {
			continue
		}
		copy := make(model.Record, len(row))
		for key, value := range row {
			copy[key] = value
		}
		copy[colRevContent] = r.content
		rows[i] = copy
	}
	return rows, page, nil
}

func (r *cedarEpochThirdReadRewriteRevisionRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, query)
	if err != nil || !r.exactRevisionRead(query) {
		return rows, page, err
	}
	r.exactReads++
	if r.exactReads < 3 {
		return rows, page, nil
	}
	for i, row := range rows {
		if row.String(colRevSurface) != r.surface || row.Int(colRevNumber) != r.revision {
			continue
		}
		copy := make(model.Record, len(row))
		for key, value := range row {
			copy[key] = value
		}
		copy[colRevContent] = r.content
		rows[i] = copy
	}
	return rows, page, nil
}

func (r *cedarEpochThirdReadRewriteRevisionRepo) exactRevisionRead(query model.Query) bool {
	var surfaceOK, revisionOK bool
	for _, filter := range query.Filters {
		if filter.Op != model.OpEq {
			continue
		}
		switch filter.Column {
		case colRevSurface:
			value, ok := filter.Value.(string)
			surfaceOK = ok && value == r.surface
		case colRevNumber:
			value, ok := filter.Value.(int64)
			revisionOK = ok && value == r.revision
		}
	}
	return surfaceOK && revisionOK
}

func (r *cedarEpochWrongIdentityRevisionRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, query)
	if err != nil || !r.exactRevisionRead(query) {
		return rows, page, err
	}
	r.exactReads++
	rewriteAt := r.rewriteAt
	if rewriteAt == 0 {
		rewriteAt = 2
	}
	if r.exactReads < rewriteAt {
		return rows, page, nil
	}
	for i, row := range rows {
		if row.String(colRevSurface) != r.surface || row.Int(colRevNumber) != r.revision {
			continue
		}
		copy := make(model.Record, len(row))
		for key, value := range row {
			copy[key] = value
		}
		if r.replacementSurface != "" {
			copy[colRevSurface] = r.replacementSurface
		} else {
			copy[colRevNumber] = r.revision + 1
		}
		rows[i] = copy
	}
	return rows, page, nil
}

func (r *cedarEpochWrongIdentityRevisionRepo) exactRevisionRead(query model.Query) bool {
	var surfaceOK, revisionOK bool
	for _, filter := range query.Filters {
		if filter.Op != model.OpEq {
			continue
		}
		switch filter.Column {
		case colRevSurface:
			value, ok := filter.Value.(string)
			surfaceOK = ok && value == r.surface
		case colRevNumber:
			value, ok := filter.Value.(int64)
			revisionOK = ok && value == r.revision
		}
	}
	return surfaceOK && revisionOK
}

func (r *cedarEpochVanishingRevisionRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	if r.exactRevisionRead(query) {
		r.exactReads++
		if r.exactReads >= r.vanishAt {
			return nil, model.Page{}, nil
		}
	}
	return r.GenericRepo.List(ctx, query)
}

func (r *cedarEpochVanishingRevisionRepo) exactRevisionRead(query model.Query) bool {
	var surfaceOK, revisionOK bool
	for _, filter := range query.Filters {
		if filter.Op != model.OpEq {
			continue
		}
		switch filter.Column {
		case colRevSurface:
			value, ok := filter.Value.(string)
			surfaceOK = ok && value == r.surface
		case colRevNumber:
			value, ok := filter.Value.(int64)
			revisionOK = ok && value == r.revision
		}
	}
	return surfaceOK && revisionOK
}

func invokeCedarEpochPublish(t *testing.T, f *managedEpochFixture, data api.ScopedData, source string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handlePdpPublish(rec, managedEpochRequest(t, http.MethodPost, "/pdp/publish", map[string]any{
		"engine": surfaceCedar,
		"source": source,
	}, "", ""), f.moduleContext(data))
	return rec
}

func invokeCedarEpochRollback(t *testing.T, f *managedEpochFixture, data api.ScopedData, revision int64) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handlePdpRollback(rec, managedEpochRequest(t, http.MethodPost, "/pdp/rollback", map[string]any{
		"engine": surfaceCedar, "revision": revision,
	}, "", ""), f.moduleContext(data))
	return rec
}

func cedarEpochState(tenant model.TenantID, version int64, selection activationID, source string) scopedTenantState {
	var set *grantSet
	if source != "" {
		set, _ = compileGrantSet(source)
	}
	return scopedTenantState{
		set:            set,
		selection:      selection,
		generation:     store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: version},
		authoredDigest: contentDigest(source),
		unionDigest:    contentDigest(source),
		available:      true,
		freshnessValid: true,
	}
}

func (f *managedEpochFixture) seedActivatedCedarSurface(t *testing.T, surface, source string) int64 {
	t.Helper()
	var number int64
	f.mutate(t, func(sc store.Scope) error {
		var err error
		number, _, err = appendRevision(context.Background(), sc, surface, source, "seed", true, true, "")
		if err != nil {
			return err
		}
		_, err = activateRevision(context.Background(), sc, surface, number, "seed")
		return err
	})
	return number
}

// seedSelectedCedarSurface selects source on surface the way its writer does, and
// returns the revision with the number of exact reads that select it. An engine
// is activated, so activeRevisionNumber validates the marker's target and
// latestActiveRevision reads it again. The managed projection and the adopted DDIL
// snapshot have no activation stream (revision.go:351-353): their writers append a
// newest active revision (scopedadmin.go:894, ddiladopt.go:166), which the
// active-row list selects and one exact read loads.
func (f *managedEpochFixture) seedSelectedCedarSurface(t *testing.T, surface, source string) (int64, int) {
	t.Helper()
	if surfaces.accepts(activationSurface(surface)) {
		return f.seedActivatedCedarSurface(t, surface, source), 2
	}
	var number int64
	f.mutate(t, func(sc store.Scope) error {
		var err error
		number, _, err = appendRevision(context.Background(), sc, surface, source, "seed", true, true, "")
		return err
	})
	return number, 1
}

func cedarEpochHasAuditAction(t *testing.T, f *managedEpochFixture, action string) bool {
	t.Helper()
	found := false
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			found = found || event.Action == action
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func cedarEpochCanonicalAudit(t *testing.T, f *managedEpochFixture, action string) (model.AuditEvent, map[string]any) {
	t.Helper()
	var out model.AuditEvent
	var meta map[string]any
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("audit log does not expose canonical metadata")
		}
		return walker.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if event.Action != action {
				return nil
			}
			decoded := map[string]any{}
			if err := json.Unmarshal([]byte(canonical), &decoded); err != nil {
				return err
			}
			out, meta = event, decoded
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out, meta
}

type cedarEpochAuthoritySnapshot struct {
	epoch       int64
	revisions   int
	activations int
	freshness   int
	auditSeq    int64
}

func (f *managedEpochFixture) cedarAuthoritySnapshot(t *testing.T) cedarEpochAuthoritySnapshot {
	t.Helper()
	var out cedarEpochAuthoritySnapshot
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		fact, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		if err != nil {
			return err
		}
		out.epoch = fact.Version
		repo, err := sc.Ext(revisionKind)
		if err != nil {
			return err
		}
		if rows, err := listAll(context.Background(), repo, eq(colRevSurface, surfaceCedar)); err != nil {
			return err
		} else {
			out.revisions = len(rows)
		}
		if rows, err := listAll(context.Background(), repo, eq(colRevSurface, activationSurface(surfaceCedar))); err != nil {
			return err
		} else {
			out.activations = len(rows)
		}
		freshness, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			return err
		}
		if rows, err := listAll(context.Background(), freshness); err != nil {
			return err
		} else {
			out.freshness = len(rows)
		}
		if head, found, err := sc.Audit().Head(context.Background()); err != nil {
			return err
		} else if found {
			out.auditSeq = head.Seq
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertCedarAuthorityDelta(t *testing.T, before, after cedarEpochAuthoritySnapshot, epoch, revisions, activations, freshness, audit int64) {
	t.Helper()
	got := []int64{
		after.epoch - before.epoch,
		int64(after.revisions - before.revisions),
		int64(after.activations - before.activations),
		int64(after.freshness - before.freshness),
		after.auditSeq - before.auditSeq,
	}
	want := []int64{epoch, revisions, activations, freshness, audit}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta epoch/revisions/activations/freshness/audit = %v, want %v", got, want)
		}
	}
}

func cedarEpochResponseRevision(t *testing.T, rec *httptest.ResponseRecorder) int64 {
	t.Helper()
	var body struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Revision <= 0 {
		t.Fatalf("response has no positive revision: %s", rec.Body.String())
	}
	return body.Revision
}

func requireCedarEpochTrace(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("authority/write trace = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("authority/write trace = %v, want %v", got, want)
		}
	}
}

func cedarEpochOrderedData(t *testing.T, f *managedEpochFixture, dbNow time.Time, trace *[]string) *managedEpochScopedData {
	t.Helper()
	return f.scopedData(func(sc store.Scope) store.Scope {
		revisions, err := sc.Ext(revisionKind)
		if err != nil {
			t.Fatal(err)
		}
		freshness, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			t.Fatal(err)
		}
		out := newManagedEpochScope(sc)
		out.epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore), trace: trace}
		out.authority = managedEpochRecordingAuthority{AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), trace: trace}
		out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), now: model.NewTimestamp(dbNow), fixed: true, trace: trace}
		out.ext = map[model.Kind]store.GenericRepo{
			revisionKind:        managedEpochRecordingRepo{GenericRepo: revisions, label: "revision", trace: trace},
			policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshness, label: "freshness", trace: trace},
		}
		out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), trace: trace}
		return out
	})
}

type cedarEpochFailNthCreateRepo struct {
	store.GenericRepo
	failAt int
	calls  int
	err    error
}

func (r *cedarEpochFailNthCreateRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	r.calls++
	if r.calls == r.failAt {
		return nil, r.err
	}
	return r.GenericRepo.Create(ctx, rec)
}

type cedarEpochLockGate struct{ locked bool }

type cedarEpochGatedAuthority struct {
	store.AuthoritySnapshotLocker
	gate *cedarEpochLockGate
}

func (a cedarEpochGatedAuthority) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	a.gate.locked = true
	return a.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, refs)
}

// cedarEpochAcceptingAuthority is a narrow stale-snapshot decorator for tests. It
// accepts the exact fact supplied by its paired reader so C3's caller-side monotonic
// fence, rather than the concrete store's lock implementation, is observable.
type cedarEpochAcceptingAuthority struct {
	locks [][]store.AuthorizationFactRef
}

func (a *cedarEpochAcceptingAuthority) LockAuthoritySnapshot(_ context.Context, refs []store.AuthorizationFactRef) error {
	a.locks = append(a.locks, append([]store.AuthorizationFactRef(nil), refs...))
	return nil
}

type cedarEpochRejectReadBeforeLockRepo struct {
	store.GenericRepo
	gate *cedarEpochLockGate
	err  error
}

func (r cedarEpochRejectReadBeforeLockRepo) List(ctx context.Context, query model.Query) ([]model.Record, model.Page, error) {
	if !r.gate.locked {
		return nil, model.Page{}, r.err
	}
	return r.GenericRepo.List(ctx, query)
}

type cedarEpochFailReadEpochStore struct {
	store.AuthorizationEpochStore
	failAt int
	err    error
	reads  int
	bumps  int
}

// cedarEpochWrongAfterCASStore returns a well-formed but incorrect G+99 only
// when a caller performs an unnecessary read after its CAS. The CAS itself still
// writes G+1, so a test can prove C3 carries the returned witness rather than
// accepting a later, independently decorable read.
type cedarEpochWrongAfterCASStore struct {
	store.AuthorizationEpochStore
	reads int
	bumps int
}

func (s *cedarEpochWrongAfterCASStore) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	fact, err := s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, err
	}
	if s.bumps > 0 {
		fact.Version += 98 // underlying G+1 becomes valid-looking G+99
	}
	return fact, nil
}

func (s *cedarEpochWrongAfterCASStore) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	return s.AuthorizationEpochStore.BumpAuthorizationEpoch(ctx, expected)
}

// cedarEpochWrongSecondReadStore makes only an unnecessary no-op post-lock read
// look like a valid G+99. The first read remains the exact locked witness.
type cedarEpochWrongSecondReadStore struct {
	store.AuthorizationEpochStore
	reads        int
	bumps        int
	bumpExpected store.AuthorizationFactRef
}

type cedarEpochFixedEpochStore struct {
	fact         store.AuthorizationFactRef
	reads, bumps int
}

func (s *cedarEpochFixedEpochStore) ReadAuthorizationEpoch(context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	return s.fact, nil
}

func (s *cedarEpochFixedEpochStore) BumpAuthorizationEpoch(_ context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	next := expected
	next.Version++
	return next, nil
}

func (s *cedarEpochWrongSecondReadStore) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	fact, err := s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, err
	}
	if s.reads >= 2 {
		fact.Version += 98
	}
	return fact, nil
}

func (s *cedarEpochWrongSecondReadStore) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	s.bumpExpected = expected
	// Preserve the real durable G→G+1 only for the correct locked witness. If a
	// mutation re-reads G+99 and passes it here, return its self-consistent G+100
	// without changing the backing epoch so the caller's false attribution is
	// observable rather than hidden by a store conflict.
	actual, err := s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, err
	}
	if expected == actual {
		return s.AuthorizationEpochStore.BumpAuthorizationEpoch(ctx, expected)
	}
	next := expected
	next.Version++
	return next, nil
}

func (s *cedarEpochFailReadEpochStore) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	if s.reads == s.failAt {
		return store.AuthorizationFactRef{}, s.err
	}
	return s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
}

func (s *cedarEpochFailReadEpochStore) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	return s.AuthorizationEpochStore.BumpAuthorizationEpoch(ctx, expected)
}

func assertCedarRuntimeUnavailable(t *testing.T, f *managedEpochFixture, label string) {
	t.Helper()
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || state.available || state.operation == nil {
		t.Fatalf("%s runtime state = loaded:%t %+v, want unavailable operational sentinel", label, loaded, state)
	}
	req := auth.Request{Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindToken, CredID: model.ID("reload-" + label)}, Permission: "agent:read", Resource: auth.ResourceFor("agent:read")}
	if _, err := f.m.grants.Scoped(context.Background(), req); err == nil {
		t.Fatalf("%s Scoped accepted unavailable runtime", label)
	}
	if _, err := f.m.grants.Evaluate(context.Background(), req); err == nil {
		t.Fatalf("%s Evaluate accepted unavailable runtime", label)
	}
}

// TestReloadTenantGrantsNewerInvalidSnapshotDominatesOlderReplay fixes the ordering
// that matters when a stale exact-G replay completes while reload has already captured
// a corrupt G+1. The compile failure is durable authority evidence: it must make the
// live engine unavailable even though the replay has advanced only its local operation
// token. Leaving G available here would continue to grant after durable authority
// advanced.

// TestReloadTenantGrantsPostEpochReadFailureDominatesOldReplay closes the ordering
// hole where the View reads durable G+1 successfully, then a later selected-revision
// read fails. The epoch is still authoritative high-water evidence; a delayed exact-G
// replay cannot turn that post-epoch failure into a token-only transient.

// TestReloadTenantGrantsPartialSameGenerationFailureDominatesOldReplay proves that an
// equal epoch is not safe to token-CAS after a later read fails. The View has already
// observed authored B at G before the managed read fails; a delayed replay of authored
// A must not remain available merely because it rotated the operation token last.

// TestScopedIncompleteGenerationFenceOrderingAndEmptyRecovery pins the two
// generation-only rules independently of a repository fixture: a partial G-1 cannot
// poison live G, and an explicit incomplete G fence is not confused with a complete
// selected-empty Cedar authority snapshot.

// TestCedarRollbackTargetIdentityTOCTOUWritesNothing exercises the target read
// before rollback's compile/CAS path. Unlike selection reload, this is the first
// exact read of the target revision: a malformed repository response must fail
// before it can compile one revision's bytes and append an activation for another.

// TestCedarBackfillPostLockFailureDominatesDelayedReplay proves that a legacy
// bounded-policy backfill cannot be demoted to a transient failure after it has locked
// G. The failed transaction rolls back its attempted G+1, but a delayed exact-G replay
// does not establish the missing freshness anchor or the required audit evidence.

func TestScopedTenantColdReloadFailureInstallsUnavailableSentinel(t *testing.T) {
	f := newManagedEpochFixture(t)
	// Hide every optional authority capability from reload. The no-policy tenant is
	// intentional: a failed *attempted* boot/reload must differ from a tenant that
	// was never reloaded, and must create an operational unavailable sentinel.
	f.m.UseData(cedarEpochModuleData{
		st:   f.st,
		wrap: func(sc store.Scope) store.Scope { return cedarEpochNoCapabilityScope{Scope: sc} },
	})
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
		t.Fatal("cold reload without epoch capability succeeded")
	}
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || state.available || state.operation == nil {
		t.Fatalf("cold failure state = loaded:%t %+v, want unavailable sentinel with operation token", loaded, state)
	}
	req := auth.Request{Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindToken, CredID: "cold"}, Permission: "agent:read", Resource: auth.ResourceFor("agent:read")}
	if _, err := f.m.grants.Scoped(context.Background(), req); err == nil {
		t.Fatal("Scoped accepted an unavailable cold snapshot")
	}
	if _, err := f.m.grants.Evaluate(context.Background(), req); err == nil {
		t.Fatal("restrict-view Evaluate accepted an unavailable cold snapshot")
	}
	if f.m.grants.grantExpired(f.tenant) {
		t.Fatal("unavailable sentinel fabricated an expired loaded-policy state")
	}
	rec := httptest.NewRecorder()
	f.m.handlePdpActive(rec, managedEpochRequest(t, http.MethodGet, "/pdp/active?engine=cedar", nil, "", ""), f.moduleContext(api.NewScopedData(f.st, f.tenant)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET cold unavailable state = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveDeferred || body["grants_expired"] != false {
		t.Fatalf("cold unavailable status = live:%v expired:%v body:%s, want deferred/false", body["live_activation"], body["grants_expired"], rec.Body.String())
	}
}

func TestReloadActivePDPDirectFailuresInstallUnavailableSentinel(t *testing.T) {
	t.Run("module data absent", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.m.data = nil
		if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
			t.Fatal("ReloadActivePDP accepted missing module data")
		}
		assertCedarRuntimeUnavailable(t, f, "missing-data")
	})

	t.Run("current durable Cedar does not compile", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedActivatedCedarSurface(t, surfaceCedar, `this is not valid Cedar`)
		if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
			t.Fatal("ReloadActivePDP accepted malformed current Cedar source")
		}
		assertCedarRuntimeUnavailable(t, f, "malformed-current")
	})

	t.Run("bounded snapshot missing anchor", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.m.grants.maxStaleness = time.Hour
		f.seedActivatedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
		// Exercise the reload half directly: ReloadActivePDP's boot backfill would
		// create a local anchor first, while the installer itself must still turn an
		// observed bounded/no-anchor snapshot into unavailable rather than leave a
		// compiled set able to make restrict-view Allow decisions.
		if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, errBoundedPolicyFreshnessUnavailable) {
			t.Fatalf("bounded no-anchor reload error = %v, want errBoundedPolicyFreshnessUnavailable", err)
		}
		assertCedarRuntimeUnavailable(t, f, "bounded-no-anchor")
	})
}

func TestScopedInstallIsMonotonicAndStaleFailureCannotPoisonExactReplay(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	engine := &scopedEngine{}
	source := `forbid(principal, action, resource);`
	generationOne := cedarEpochState(tenant, 1, activationID{authored: 1}, source)
	generationTwo := cedarEpochState(tenant, 2, activationID{authored: 2}, source)
	if _, err := engine.installIfNotOlder(tenant, generationOne); err != nil {
		t.Fatalf("install G: %v", err)
	}
	beforeReplay, loaded := engine.tenantState(tenant)
	if !loaded {
		t.Fatal("G was not installed")
	}
	if got, err := engine.installIfNotOlder(tenant, generationOne); err != nil || got != scopedInstallAlreadyCurrent {
		t.Fatalf("exact same-G replay = result:%v err:%v, want AlreadyCurrent/nil", got, err)
	}
	// A started before the replay sees the old operation token. Its late failure must
	// not invalidate B's successful exact revalidation at the same durable G.
	engine.markUnavailableIfStillSame(tenant, beforeReplay, true)
	afterReplay, _ := engine.tenantState(tenant)
	if !afterReplay.available || afterReplay.operation == beforeReplay.operation {
		t.Fatalf("stale failure poisoned/reused exact replay state: before=%p after=%+v", beforeReplay.operation, afterReplay)
	}
	if _, err := engine.installIfNotOlder(tenant, generationTwo); err != nil {
		t.Fatalf("install G+1: %v", err)
	}
	if got, err := engine.installIfNotOlder(tenant, generationOne); err != nil || got != scopedInstallOlder {
		t.Fatalf("late G install = result:%v err:%v, want Older/nil", got, err)
	}
	current, _ := engine.tenantState(tenant)
	if current.generation.Version != 2 || current.selection.authored != 2 {
		t.Fatalf("late G replaced G+1: %+v", current)
	}

	badDigest := cedarEpochState(tenant, 2, activationID{authored: 2}, source+"\nforbid(principal, action, resource) when { context.permission == \"agent:write\" };")
	if _, err := engine.installIfNotOlder(tenant, badDigest); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
		t.Fatalf("same-G same-selection digest drift error = %v, want mismatch", err)
	}
	current, _ = engine.tenantState(tenant)
	if current.available {
		t.Fatal("same-G digest drift left the previous evaluator available")
	}

	withFreshness := cedarEpochState(tenant, 3, activationID{authored: 3}, source)
	withFreshness.freshness = FreshnessRecord{RefreshedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), MaxStaleness: time.Hour}
	if _, err := engine.installIfNotOlder(tenant, withFreshness); err != nil {
		t.Fatalf("install G+2 with freshness: %v", err)
	}
	badFreshness := withFreshness
	badFreshness.freshness.RefreshedAt = badFreshness.freshness.RefreshedAt.Add(time.Minute)
	if _, err := engine.installIfNotOlder(tenant, badFreshness); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
		t.Fatalf("same-G same-selection freshness drift error = %v, want mismatch", err)
	}
	current, _ = engine.tenantState(tenant)
	if current.available {
		t.Fatal("same-G freshness drift left the previous evaluator available")
	}
}

func TestReloadInstallCASRejectsStaleSameGenerationEffects(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	source := `forbid(principal, action, resource);`

	t.Run("old absent failure cannot poison newly installed generation", func(t *testing.T) {
		engine := &scopedEngine{}
		candidate := cedarEpochState(tenant, 6, activationID{authored: 1}, source)
		if _, err := engine.installIfNotOlder(tenant, candidate); err != nil {
			t.Fatalf("B installs G after A observed absence: %v", err)
		}
		beforeFailure, _ := engine.tenantState(tenant)
		engine.markUnavailableIfStillSame(tenant, scopedTenantState{}, false)
		after, afterLoaded := engine.tenantState(tenant)
		if !afterLoaded || !after.available || after.operation != beforeFailure.operation || !sameCedarAuthorityState(after, beforeFailure) {
			t.Fatalf("late absent failure poisoned newly installed G: before=%+v after=%+v", beforeFailure, after)
		}
	})

	t.Run("old cold sentinel cannot recover after newer cold mark", func(t *testing.T) {
		engine := &scopedEngine{}
		engine.markUnavailableIfStillSame(tenant, scopedTenantState{}, false)
		coldBefore, loaded := engine.tenantState(tenant)
		if !loaded || coldBefore.available || coldBefore.operation == nil || validPolicyAuthorizationEpochFact(tenant, coldBefore.generation) {
			t.Fatalf("initial cold sentinel = loaded:%t %+v", loaded, coldBefore)
		}
		engine.markUnavailableIfStillSame(tenant, coldBefore, true) // B: cold T0→T1.
		coldAfter, _ := engine.tenantState(tenant)
		if coldAfter.operation == coldBefore.operation {
			t.Fatalf("newer cold mark did not advance operation: before=%+v after=%+v", coldBefore, coldAfter)
		}
		candidate := cedarEpochState(tenant, 7, activationID{authored: 1}, source)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, coldBefore, true, candidate); !errors.Is(err, errScopedSnapshotStaleOperation) {
			t.Fatalf("old cold T0 recovery after T1 mark = %v, want stale operation", err)
		}
		after, _ := engine.tenantState(tenant)
		if after.available || after.operation != coldAfter.operation || after.generation != coldAfter.generation {
			t.Fatalf("old cold recovery changed newer sentinel: before=%+v after=%+v", coldAfter, after)
		}
	})

	t.Run("old coherent reload cannot resurrect unavailable", func(t *testing.T) {
		engine := &scopedEngine{}
		candidate := cedarEpochState(tenant, 7, activationID{authored: 1}, source)
		if _, err := engine.installIfNotOlder(tenant, candidate); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		engine.markUnavailableIfStillSame(tenant, before, loaded) // B: G/T1 unavailable.
		unavailable, unavailableLoaded := engine.tenantState(tenant)
		if !unavailableLoaded || unavailable.available || unavailable.operation == before.operation {
			t.Fatalf("failure did not create G/T1 unavailable state: before=%+v after=%+v", before, unavailable)
		}
		if _, err := engine.installIfNotOlderFromObservedState(tenant, before, loaded, candidate); !errors.Is(err, errScopedSnapshotStaleOperation) {
			t.Fatalf("late G/T0 recovery = %v, want stale operation", err)
		}
		after, _ := engine.tenantState(tenant)
		if after.available || after.operation != unavailable.operation || !sameCedarAuthorityState(after, unavailable) {
			t.Fatalf("late G/T0 candidate resurrected/changed unavailable state: before=%+v after=%+v", unavailable, after)
		}
		// A new coherent reload captures T1 and may recover it atomically.
		if _, err := engine.installIfNotOlderFromObservedState(tenant, after, true, candidate); err != nil {
			t.Fatalf("fresh G/T1 recovery: %v", err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || recovered.operation == after.operation {
			t.Fatalf("fresh G/T1 recovery did not install a new available operation: %+v", recovered)
		}
	})

	t.Run("higher generation remains monotonic across token change", func(t *testing.T) {
		engine := &scopedEngine{}
		base := cedarEpochState(tenant, 9, activationID{authored: 3}, source)
		if _, err := engine.installIfNotOlder(tenant, base); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		engine.markUnavailableIfStillSame(tenant, before, loaded) // B changes G/T0→G/T1.
		higher := cedarEpochState(tenant, 10, activationID{authored: 4}, source)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, before, loaded, higher); err != nil {
			t.Fatalf("G+1 install across stale operation: %v", err)
		}
		after, _ := engine.tenantState(tenant)
		if !after.available || after.generation.Version != 10 || after.operation == before.operation {
			t.Fatalf("G+1 was not installed monotonically across token change: before=%+v after=%+v", before, after)
		}
	})

	t.Run("same generation conflict stays unavailable across stale identity", func(t *testing.T) {
		engine := &scopedEngine{}
		good := cedarEpochState(tenant, 11, activationID{authored: 5}, source)
		if _, err := engine.installIfNotOlder(tenant, good); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		mismatch := cedarEpochState(tenant, 11, activationID{authored: 5}, source+"\nforbid(principal, action, resource) when { context.permission == \"agent:write\" };")
		if _, err := engine.installIfNotOlder(tenant, mismatch); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
			t.Fatalf("B same-G mismatch = %v, want mismatch", err)
		}
		unavailable, _ := engine.tenantState(tenant)
		if unavailable.available || unavailable.operation == before.operation ||
			!unavailable.sameIdentity(mismatch) || unavailable.set != nil {
			t.Fatalf("same-G mismatch did not install its unavailable identity T1: before=%+v after=%+v", before, unavailable)
		}
		if _, err := engine.installIfNotOlderFromObservedState(tenant, before, loaded, good); !errors.Is(err, errScopedSnapshotStaleOperation) {
			t.Fatalf("A old G/T0 identity after mismatch T1 = %v, want stale operation", err)
		}
		after, _ := engine.tenantState(tenant)
		if after.available || after.operation != unavailable.operation || !after.sameIdentity(unavailable) || after.set != nil {
			t.Fatalf("stale identity rewrote same-G unavailable sentinel: before=%+v after=%+v", unavailable, after)
		}
		if _, err := engine.installIfNotOlderFromObservedState(tenant, after, true, mismatch); err != nil {
			t.Fatalf("coherent reread after same-G conflict: %v", err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || !recovered.sameIdentity(mismatch) || !hasCedarCompiledBinding(recovered) {
			t.Fatalf("coherent same-G reread did not recover unavailable identity: %+v", recovered)
		}
	})

	t.Run("same generation mismatch dominates older exact replay", func(t *testing.T) {
		engine := &scopedEngine{}
		good := cedarEpochState(tenant, 8, activationID{authored: 2}, source)
		if _, err := engine.installIfNotOlder(tenant, good); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		mismatch := cedarEpochState(tenant, 8, activationID{authored: 2}, source+"\nforbid(principal, action, resource) when { context.permission == \"agent:write\" };")
		if _, err := engine.installIfNotOlder(tenant, good); err != nil {
			t.Fatalf("exact B replay G/T1: %v", err)
		}
		replayed, _ := engine.tenantState(tenant)
		if replayed.operation == before.operation || !replayed.available {
			t.Fatalf("exact replay did not retain available G with new operation: before=%+v after=%+v", before, replayed)
		}
		if _, err := engine.installIfNotOlderFromObservedState(tenant, before, loaded, mismatch); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
			t.Fatalf("late G/T0 mismatch = %v, want same-generation mismatch", err)
		}
		after, _ := engine.tenantState(tenant)
		if after.available || after.operation == replayed.operation || !after.sameIdentity(mismatch) || after.set != nil {
			t.Fatalf("late same-G mismatch left older replay able to authorize: replay=%+v after=%+v", replayed, after)
		}
		if _, err := engine.installIfNotOlderFromObservedState(tenant, after, true, mismatch); err != nil {
			t.Fatalf("coherent reread after stale mismatch: %v", err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || !recovered.sameIdentity(mismatch) || !hasCedarCompiledBinding(recovered) {
			t.Fatalf("coherent reread did not recover same-G mismatch identity: %+v", recovered)
		}
	})
}

func TestScopedIncompleteGenerationFenceOrderingAndEmptyRecovery(t *testing.T) {
	tenant := model.TenantID(model.NewID())

	t.Run("lower partial observation leaves newer runtime unchanged", func(t *testing.T) {
		engine := &scopedEngine{}
		live := cedarEpochState(tenant, 7, activationID{authored: 7}, `permit(principal, action, resource);`)
		if _, err := engine.installIfNotOlder(tenant, live); err != nil {
			t.Fatalf("install live G: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		older := before.generation
		older.Version--
		// Use the matching observed token: deleting the Gf<G fence would then
		// close the good runtime and this assertion would fail.
		engine.markUnavailableForObservedGenerationFailure(tenant, before, loaded, older)
		after, afterLoaded := engine.tenantState(tenant)
		if !afterLoaded || !after.available || after.identityIncomplete || after.operation != before.operation ||
			!sameCedarAuthorityState(after, before) {
			t.Fatalf("lower partial observation poisoned newer runtime: before=%+v after=%+v", before, after)
		}
	})

	t.Run("complete empty authority recovers incomplete fence in one reread", func(t *testing.T) {
		engine := &scopedEngine{}
		generation := store.AuthorizationFactRef{
			Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 9,
		}
		empty := cedarEpochState(tenant, generation.Version, activationID{}, "")
		if !hasCedarCompiledBinding(empty) || empty.identityIncomplete || empty.set != nil {
			t.Fatalf("complete empty Cedar state is malformed: %+v", empty)
		}
		if _, err := engine.installIfNotOlder(tenant, empty); err != nil {
			t.Fatalf("install complete empty G/T0: %v", err)
		}
		beforeFence, beforeFenceLoaded := engine.tenantState(tenant)
		engine.markUnavailableForObservedGenerationFailure(tenant, beforeFence, beforeFenceLoaded, generation)
		fence, loaded := engine.tenantState(tenant)
		if !loaded || fence.available || !fence.identityIncomplete || hasCedarCompiledBinding(fence) {
			t.Fatalf("incomplete generation fence = loaded:%t state:%+v", loaded, fence)
		}
		if result, err := engine.installIfNotOlderFromObservedState(tenant, beforeFence, beforeFenceLoaded, empty); result != scopedInstallOlder || !errors.Is(err, errScopedSnapshotStaleOperation) {
			t.Fatalf("complete same-G candidate captured before fence = result:%v err:%v, want Older/stale", result, err)
		}
		afterStale, _ := engine.tenantState(tenant)
		if afterStale.available || !afterStale.identityIncomplete || afterStale.operation != fence.operation ||
			afterStale.generation != fence.generation {
			t.Fatalf("stale complete candidate changed incomplete fence: fence=%+v after=%+v", fence, afterStale)
		}
		result, err := engine.installIfNotOlderFromObservedState(tenant, fence, true, empty)
		if err != nil || result != scopedInstallApplied {
			t.Fatalf("complete empty reread did not replace incomplete fence: result:%v err:%v", result, err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || recovered.identityIncomplete || recovered.set != nil || !hasCedarCompiledBinding(recovered) ||
			recovered.operation == fence.operation {
			t.Fatalf("complete empty reread remained indistinguishable from fence: before=%+v after=%+v", fence, recovered)
		}
	})

	t.Run("partial durable generation dominates a later cold sentinel", func(t *testing.T) {
		engine := &scopedEngine{}
		// A began from absence. B's unrelated transient failure has already
		// installed an unversioned cold sentinel before A returns its exact G.
		engine.markUnavailableIfStillSame(tenant, scopedTenantState{}, false)
		cold, coldLoaded := engine.tenantState(tenant)
		if !coldLoaded || cold.available || cold.operation == nil || validPolicyAuthorizationEpochFact(tenant, cold.generation) {
			t.Fatalf("cold sentinel precondition = loaded:%t state:%+v", coldLoaded, cold)
		}
		generation := store.AuthorizationFactRef{
			Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 12,
		}
		engine.markUnavailableForObservedGenerationFailure(tenant, scopedTenantState{}, false, generation)
		fence, loaded := engine.tenantState(tenant)
		if !loaded || fence.available || !fence.identityIncomplete || fence.generation != generation ||
			fence.operation == cold.operation || hasCedarCompiledBinding(fence) {
			t.Fatalf("partial durable G did not dominate cold sentinel: cold=%+v fence=%+v", cold, fence)
		}
		older := cedarEpochState(tenant, generation.Version-1, activationID{authored: 11}, `permit(principal, action, resource);`)
		if result, err := engine.installIfNotOlderFromObservedState(tenant, cold, true, older); err != nil || result != scopedInstallOlder {
			t.Fatalf("G-1 candidate crossed partial durable fence: result:%v err:%v", result, err)
		}
		afterOlder, _ := engine.tenantState(tenant)
		if afterOlder.available || !afterOlder.identityIncomplete || afterOlder.generation != fence.generation ||
			afterOlder.operation != fence.operation {
			t.Fatalf("G-1 candidate changed partial durable fence: fence=%+v after=%+v", fence, afterOlder)
		}
		complete := cedarEpochState(tenant, generation.Version, activationID{}, "")
		if result, err := engine.installIfNotOlderFromObservedState(tenant, fence, true, complete); err != nil || result != scopedInstallApplied {
			t.Fatalf("complete G reread did not recover cold-derived fence: result:%v err:%v", result, err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || recovered.identityIncomplete || !hasCedarCompiledBinding(recovered) ||
			recovered.operation == fence.operation {
			t.Fatalf("complete G reread did not recover cold-derived fence: before=%+v after=%+v", fence, recovered)
		}
	})
}

func TestScopedDurableReloadFailureOrdering(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	baseSource := `permit(principal, action, resource);`
	driftSource := `forbid(principal, action, resource);`

	t.Run("durable failure dominates cold sentinel and older candidate", func(t *testing.T) {
		engine := &scopedEngine{}
		// A begins from absence; B's transient failure installs the unversioned
		// cold sentinel before A handles its witnessed durable failure.
		engine.markUnavailableIfStillSame(tenant, scopedTenantState{}, false)
		cold, loaded := engine.tenantState(tenant)
		if !loaded || cold.available || cold.operation == nil || validPolicyAuthorizationEpochFact(tenant, cold.generation) {
			t.Fatalf("cold sentinel precondition = loaded:%t state:%+v", loaded, cold)
		}
		failed := cedarEpochState(tenant, 42, activationID{authored: 42}, driftSource)
		failed.set = nil // the current durable source failed before compilation.
		engine.markUnavailableForDurableReloadFailure(tenant, scopedTenantState{}, false, failed)
		afterFailure, _ := engine.tenantState(tenant)
		if afterFailure.available || afterFailure.operation == cold.operation ||
			!afterFailure.sameIdentity(failed) || afterFailure.set != nil ||
			afterFailure.generation != failed.generation {
			t.Fatalf("durable failure did not dominate cold sentinel: cold=%+v after=%+v", cold, afterFailure)
		}
		// C captured G-1 while the runtime was cold. It must not recover through
		// the old cold token after the failed G is known.
		older := cedarEpochState(tenant, 41, activationID{authored: 41}, baseSource)
		result, err := engine.installIfNotOlderFromObservedState(tenant, cold, true, older)
		if err != nil || result != scopedInstallOlder {
			t.Fatalf("older candidate after durable failure = result:%v err:%v, want Older/nil", result, err)
		}
		afterOlder, _ := engine.tenantState(tenant)
		if afterOlder.available || afterOlder.operation != afterFailure.operation || !afterOlder.sameIdentity(afterFailure) {
			t.Fatalf("older candidate recovered through cold sentinel: failed=%+v after=%+v", afterFailure, afterOlder)
		}
	})

	t.Run("older durable failure cannot poison newer live generation", func(t *testing.T) {
		engine := &scopedEngine{}
		live := cedarEpochState(tenant, 21, activationID{authored: 21}, baseSource)
		if _, err := engine.installIfNotOlder(tenant, live); err != nil {
			t.Fatalf("install live G+1: %v", err)
		}
		before, loaded := engine.tenantState(tenant)
		// The matched operation makes this test kill removal of the Gf<G guard;
		// token-CAS alone would otherwise be an accidental pass.
		expected := before
		expected.generation.Version--
		failed := expected
		failed.selection = activationID{authored: 20}
		failed.authoredDigest = contentDigest(driftSource)
		failed.unionDigest = failed.authoredDigest
		engine.markUnavailableForDurableReloadFailure(tenant, expected, loaded, failed)
		after, afterLoaded := engine.tenantState(tenant)
		if !afterLoaded || !after.available || after.operation != before.operation || !after.sameIdentity(before) {
			t.Fatalf("older durable failure poisoned newer G: before=%+v after=%+v", before, after)
		}
	})

	t.Run("same generation identity drift dominates replay and exact reread recovers", func(t *testing.T) {
		engine := &scopedEngine{}
		old := cedarEpochState(tenant, 30, activationID{authored: 30}, baseSource)
		if _, err := engine.installIfNotOlder(tenant, old); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		observed, loaded := engine.tenantState(tenant)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, observed, loaded, old); err != nil {
			t.Fatalf("B exact replay G/T1: %v", err)
		}
		replayed, _ := engine.tenantState(tenant)
		failed := cedarEpochState(tenant, 30, activationID{authored: 30}, driftSource)
		failed.set = nil // A's compile failed; only its durable identity is trusted.
		engine.markUnavailableForDurableReloadFailure(tenant, observed, loaded, failed)
		afterFailure, _ := engine.tenantState(tenant)
		if afterFailure.available || afterFailure.operation == replayed.operation ||
			!afterFailure.sameIdentity(failed) || afterFailure.set != nil {
			t.Fatalf("same-G durable identity drift left replay available: replay=%+v after=%+v", replayed, afterFailure)
		}
		coherent := cedarEpochState(tenant, 30, activationID{authored: 30}, driftSource)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, afterFailure, true, coherent); err != nil {
			t.Fatalf("coherent exact reread after identity failure: %v", err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || !recovered.sameIdentity(coherent) || !hasCedarCompiledBinding(recovered) || recovered.operation == afterFailure.operation {
			t.Fatalf("coherent reread did not recover durable identity: before=%+v after=%+v", afterFailure, recovered)
		}
	})

	t.Run("stale same generation failure preserves unavailable conflict identity", func(t *testing.T) {
		engine := &scopedEngine{}
		old := cedarEpochState(tenant, 35, activationID{authored: 35}, baseSource)
		if _, err := engine.installIfNotOlder(tenant, old); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		observed, loaded := engine.tenantState(tenant)
		currentFailure := cedarEpochState(tenant, 35, activationID{authored: 35}, driftSource)
		currentFailure.set = nil
		engine.markUnavailableForDurableReloadFailure(tenant, observed, loaded, currentFailure)
		unavailable, _ := engine.tenantState(tenant)
		if unavailable.available || unavailable.operation == observed.operation || !unavailable.sameIdentity(currentFailure) {
			t.Fatalf("current same-G failure did not establish unavailable identity: before=%+v after=%+v", observed, unavailable)
		}
		// A stale failure from the old identity cannot overwrite B's unavailable
		// sentinel. It observed T0; B's failure installed T1.
		engine.markUnavailableForDurableReloadFailure(tenant, observed, loaded, old)
		afterStale, _ := engine.tenantState(tenant)
		if afterStale.available || afterStale.operation != unavailable.operation || !afterStale.sameIdentity(unavailable) {
			t.Fatalf("stale same-G failure rewrote unavailable identity: before=%+v after=%+v", unavailable, afterStale)
		}
		coherent := cedarEpochState(tenant, 35, activationID{authored: 35}, driftSource)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, afterStale, true, coherent); err != nil {
			t.Fatalf("coherent reread after stale same-G failure: %v", err)
		}
		recovered, _ := engine.tenantState(tenant)
		if !recovered.available || !recovered.sameIdentity(coherent) || !hasCedarCompiledBinding(recovered) {
			t.Fatalf("coherent reread did not recover retained unavailable identity: %+v", recovered)
		}
	})

	t.Run("same generation exact identity still requires its observed operation", func(t *testing.T) {
		engine := &scopedEngine{}
		state := cedarEpochState(tenant, 40, activationID{authored: 40}, baseSource)
		if _, err := engine.installIfNotOlder(tenant, state); err != nil {
			t.Fatalf("install G/T0: %v", err)
		}
		observed, loaded := engine.tenantState(tenant)
		if _, err := engine.installIfNotOlderFromObservedState(tenant, observed, loaded, state); err != nil {
			t.Fatalf("exact replay G/T1: %v", err)
		}
		replayed, _ := engine.tenantState(tenant)
		engine.markUnavailableForDurableReloadFailure(tenant, observed, loaded, state)
		afterStale, _ := engine.tenantState(tenant)
		if !afterStale.available || afterStale.operation != replayed.operation || !afterStale.sameIdentity(replayed) {
			t.Fatalf("same-G exact failure ignored its stale token: replay=%+v after=%+v", replayed, afterStale)
		}
		engine.markUnavailableForDurableReloadFailure(tenant, replayed, true, state)
		afterExact, _ := engine.tenantState(tenant)
		if afterExact.available || afterExact.operation == replayed.operation || !afterExact.sameIdentity(replayed) {
			t.Fatalf("same-G exact failure with matching token did not close runtime: replay=%+v after=%+v", replayed, afterExact)
		}
	})
}

func TestReloadTenantGrantsOldColdSnapshotCannotEraseLaterUnavailableSentinel(t *testing.T) {
	f := newManagedEpochFixture(t)
	// Seed durable G directly without invoking a writer/reload, so A begins from an
	// absent runtime. Its View sees a coherent candidate while B subsequently fails
	// and creates the cold unavailable sentinel.
	f.seedActivatedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
	if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
		t.Fatal("direct durable seed unexpectedly installed a runtime state")
	}
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		afterView: func() {
			before, loaded := f.m.grants.tenantState(f.tenant)
			if loaded {
				t.Errorf("cold failure hook observed a state before it installed its sentinel: %+v", before)
				return
			}
			f.m.grants.markUnavailableIfStillSame(f.tenant, before, false)
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, errScopedSnapshotStaleOperation) {
		t.Fatalf("old cold reload after unavailable sentinel = %v, want stale operation", err)
	}
	sentinel, loaded := f.m.grants.tenantState(f.tenant)
	// A complete durable G observed by A is stronger evidence than B's cold
	// sentinel. The late install remains unavailable, but it now carries that
	// exact durable identity/generation so an older candidate can never recover
	// through the unversioned cold token.
	if !loaded || sentinel.available || sentinel.operation == nil || !validPolicyAuthorizationEpochFact(f.tenant, sentinel.generation) ||
		sentinel.identityIncomplete || sentinel.selection.authored == 0 || sentinel.set != nil {
		t.Fatalf("old cold reload did not retain a durable unavailable fence: loaded:%t %+v", loaded, sentinel)
	}
	// A new reload that starts from the sentinel's own token may recover it.
	f.m.UseData(api.NewModuleData(f.st))
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent cold retry: %v", err)
	}
	recovered, _ := f.m.grants.tenantState(f.tenant)
	if !recovered.available || !validPolicyAuthorizationEpochFact(f.tenant, recovered.generation) || recovered.operation == sentinel.operation {
		t.Fatalf("coherent retry did not recover cold sentinel: before=%+v after=%+v", sentinel, recovered)
	}
}

func TestScopedInstallRejectsInvalidCompiledBindingWithoutReplacingGoodState(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	engine := &scopedEngine{}
	good := cedarEpochState(tenant, 1, activationID{authored: 1}, `forbid(principal, action, resource);`)
	if _, err := engine.installIfNotOlder(tenant, good); err != nil {
		t.Fatalf("install good state: %v", err)
	}
	for _, tc := range []struct {
		name  string
		state scopedTenantState
	}{
		{
			name:  "nonempty digest without set",
			state: func() scopedTenantState { state := good; state.set = nil; return state }(),
		},
		{
			name: "set digest differs from source",
			state: func() scopedTenantState {
				state := good
				set, err := compileGrantSet(`forbid(principal, action, resource) when { context.permission == "agent:write" };`)
				if err != nil {
					t.Fatal(err)
				}
				state.set = set
				return state
			}(),
		},
		{
			name:  "empty digest with set",
			state: func() scopedTenantState { state := good; state.unionDigest = ""; return state }(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := engine.installIfNotOlder(tenant, tc.state); err == nil {
				t.Fatal("invalid compiled binding installed")
			}
			current, loaded := engine.tenantState(tenant)
			if !loaded || !current.available || !hasCedarCompiledBinding(current) || current.operation == nil {
				t.Fatalf("invalid install replaced good state: loaded=%t state=%+v", loaded, current)
			}
		})
	}
}

func TestScopedInstallRecoversExactUnavailableState(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	engine := &scopedEngine{}
	available := cedarEpochState(tenant, 9, activationID{authored: 4}, `forbid(principal, action, resource);`)
	unavailable := available
	unavailable.available = false
	unavailable.freshnessValid = false
	if _, err := engine.installIfNotOlder(tenant, unavailable); err != nil {
		t.Fatalf("install unavailable snapshot: %v", err)
	}
	before, _ := engine.tenantState(tenant)
	if before.available {
		t.Fatal("precondition: unavailable state was not installed")
	}
	if got, err := engine.installIfNotOlder(tenant, available); err != nil || got != scopedInstallApplied {
		t.Fatalf("exact same-G recovery = result:%v err:%v, want applied/nil", got, err)
	}
	after, _ := engine.tenantState(tenant)
	if !after.available || !after.freshnessValid || after.operation == before.operation {
		t.Fatalf("exact durable replay did not recover atomically: before=%+v after=%+v", before, after)
	}
}

func TestScopedInstallRejectsSameUnionDifferentSurfaceProvenance(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	authored := `forbid(principal, action, resource);`
	managed := `forbid(principal, action, resource) when { context.permission == "agent:write" };`
	union := mergeCedarSources(authored, managed)
	set, err := compileGrantSet(union)
	if err != nil {
		t.Fatal(err)
	}
	base := scopedTenantState{
		set:            set,
		selection:      activationID{authored: 1, managed: 1},
		generation:     store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 4},
		authoredDigest: contentDigest(authored),
		managedDigest:  contentDigest(managed),
		unionDigest:    contentDigest(union),
		available:      true,
		freshnessValid: true,
	}
	for _, tc := range []struct {
		name  string
		drift scopedTenantState
	}{
		{
			// mergeCedarSources trims per-surface outer whitespace, so the
			// compiled source and unionDigest stay exactly the same. Only the
			// authored byte provenance distinguishes the snapshots.
			name: "authored whitespace provenance",
			drift: func() scopedTenantState {
				state := base
				state.authoredDigest = contentDigest(authored + "\n\n")
				return state
			}(),
		},
		{
			// The same concatenated union can be redistributed across surfaces
			// by a corrupt decorator. Revision IDs and evaluator bytes alone must
			// not make that provenance swap look like an exact replay.
			name: "redistributed authored managed bytes",
			drift: func() scopedTenantState {
				state := base
				state.authoredDigest = ""
				state.managedDigest = contentDigest(union)
				return state
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &scopedEngine{}
			if _, err := engine.installIfNotOlder(tenant, base); err != nil {
				t.Fatalf("install base: %v", err)
			}
			if tc.drift.unionDigest != base.unionDigest || tc.drift.set != base.set || tc.drift.selection != base.selection {
				t.Fatal("test precondition lost same union/set/selection")
			}
			if _, err := engine.installIfNotOlder(tenant, tc.drift); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
				t.Fatalf("same-union surface provenance drift = %v, want mismatch", err)
			}
			state, _ := engine.tenantState(tenant)
			if state.available {
				t.Fatal("same-union provenance drift left evaluator available")
			}
		})
	}
}

func TestCedarSelectedRevisionIdentityTOCTOUFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name               string
		replacementSurface string
	}{
		{name: "revision number"},
		{name: "surface", replacementSurface: surfaceOPA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			revision := f.seedActivatedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
			probe := &cedarEpochWrongIdentityRevisionRepo{
				surface: surfaceCedar, revision: revision, replacementSurface: tc.replacementSurface,
			}
			f.m.UseData(cedarEpochModuleData{
				st: f.st,
				wrap: func(sc store.Scope) store.Scope {
					base, err := sc.Ext(revisionKind)
					if err != nil {
						t.Fatal(err)
					}
					probe.GenericRepo = base
					out := newManagedEpochScope(sc)
					out.ext = map[model.Kind]store.GenericRepo{revisionKind: probe}
					return out
				},
			})
			before := f.cedarAuthoritySnapshot(t)
			if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
				t.Fatal("reload accepted a selected revision whose second read changed identity")
			}
			if probe.exactReads < 2 {
				t.Fatalf("selected revision identity was not read twice: reads=%d", probe.exactReads)
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded || state.available {
				t.Fatalf("identity TOCTOU reload did not install unavailable sentinel: loaded:%t %+v", loaded, state)
			}
		})
	}
}

func TestCedarLegacyActiveNonPositiveRevisionFailsClosed(t *testing.T) {
	for _, revision := range []int64{0, -1} {
		t.Run(fmt.Sprintf("revision-%d", revision), func(t *testing.T) {
			f := newManagedEpochFixture(t)
			f.m.grants.maxStaleness = time.Hour
			f.mutate(t, func(sc store.Scope) error {
				repo, err := sc.Ext(revisionKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(context.Background(), model.Record{
					colRevSurface: surfaceCedar, colRevNumber: revision,
					colRevContent: `permit(principal, action == Action::"agent:read", resource);`,
					colRevAuthor:  "seed", colRevValidated: true, colRevActive: true,
				})
				return err
			})
			before := f.cedarAuthoritySnapshot(t)
			if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
				t.Fatalf("reload accepted legacy active revision %d", revision)
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded || state.available {
				t.Fatalf("invalid legacy selection did not install unavailable state: loaded:%t %+v", loaded, state)
			}
			req := auth.Request{Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindToken, CredID: model.ID(fmt.Sprintf("legacy-%d", revision))}, Permission: "agent:read", Resource: auth.ResourceFor("agent:read")}
			if _, err := f.m.grants.Scoped(context.Background(), req); err == nil {
				t.Fatal("invalid legacy selection offered a scoped grant decision")
			}
			if _, err := f.m.grants.Evaluate(context.Background(), req); err == nil {
				t.Fatal("invalid legacy selection left restrict-view available")
			}
			rec := httptest.NewRecorder()
			f.m.handlePdpActive(rec, managedEpochRequest(t, http.MethodGet, "/pdp/active?engine=cedar", nil, "", ""), f.moduleContext(api.NewScopedData(f.st, f.tenant)))
			if rec.Code < http.StatusInternalServerError {
				t.Fatalf("GET active for legacy revision %d = %d body=%s, want fail-closed error not no_policy", revision, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCedarLiveProofRequiresCompiledBindingAndStableOperation(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	m := New()
	state := cedarEpochState(tenant, 7, activationID{authored: 3}, `forbid(principal, action, resource);`)
	state.operation = nextScopedStateOperation()
	expected := state
	expected.set = nil // durable state never carries the in-memory pointer.
	if got := m.liveActivationForState(surfaceCedar, expected, state, true, state, true); got != liveApplied {
		t.Fatalf("exact compiled stable snapshot = %q, want applied", got)
	}
	missingSet := state
	missingSet.set = nil
	if got := m.liveActivationForState(surfaceCedar, expected, missingSet, true, missingSet, true); got != liveDeferred {
		t.Fatalf("matching durable facts without compiled set = %q, want deferred", got)
	}
	afterReplay := state
	afterReplay.operation = nextScopedStateOperation()
	if got := m.liveActivationForState(surfaceCedar, expected, state, true, afterReplay, true); got != liveDeferred {
		t.Fatalf("token changed across durable read = %q, want deferred", got)
	}
}

func TestCedarBackfillAuditFailureOrDropRollsBackAuthority(t *testing.T) {
	injected := errors.New("injected backfill audit failure")
	for _, tc := range []struct {
		name  string
		audit func(store.AuditLog) store.AuditLog
	}{
		{name: "append error", audit: func(base store.AuditLog) store.AuditLog { return cedarEpochFailingAudit{AuditLog: base, err: injected} }},
		{name: "zero sequence", audit: func(base store.AuditLog) store.AuditLog { return cedarEpochZeroAudit{AuditLog: base} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			f.m.grants.maxStaleness = time.Hour
			f.seedSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
			before := f.snapshot(t)
			f.m.UseData(cedarEpochModuleData{
				st: f.st,
				wrap: func(sc store.Scope) store.Scope {
					out := newManagedEpochScope(sc)
					out.audit = tc.audit(sc.Audit())
					return out
				},
			})
			if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
				t.Fatal("backfill with unavailable evidence succeeded")
			}
			after := f.snapshot(t)
			assertManagedEpochDelta(t, before, after, 0, 0, 0, 0, 0, 0)
			if _, found := f.freshness(t); found {
				t.Fatal("failed backfill left a durable freshness row")
			}
			state, loaded := f.m.grants.tenantState(f.tenant)
			// The attempted CAS rolled back with the failed audit, so the only
			// truthful durable witness is the G locked before it. Do not fence a
			// fabricated G+1, and do not leave an identity-shaped empty snapshot
			// able to recover without a complete reread.
			if !loaded || state.available || !state.identityIncomplete || state.set != nil ||
				state.generation.Version != before.epoch || !validPolicyAuthorizationEpochFact(f.tenant, state.generation) {
				t.Fatalf("failed backfill did not retain the locked unavailable fence: loaded=%t state=%+v", loaded, state)
			}
		})
	}
}

func TestCedarBackfillUsesDBClockAuditsActualSingletonAndRunsOnce(t *testing.T) {
	f := newManagedEpochFixture(t)
	f.m.grants.maxStaleness = time.Hour
	f.seedActivatedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
	dbNow := time.Date(2026, 8, 16, 14, 15, 16, 123456789, time.UTC)
	var trace []string
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			freshness, err := sc.Ext(policyFreshnessKind)
			if err != nil {
				t.Fatal(err)
			}
			out := newManagedEpochScope(sc)
			out.epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore), trace: &trace}
			out.authority = managedEpochRecordingAuthority{AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), trace: &trace}
			out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), now: model.NewTimestamp(dbNow), fixed: true, trace: &trace}
			out.ext = map[model.Kind]store.GenericRepo{
				policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshness, label: "freshness", trace: &trace},
			}
			out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), trace: &trace}
			return out
		},
	})
	before := f.cedarAuthoritySnapshot(t)
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("legacy freshness backfill/reload: %v", err)
	}
	after := f.cedarAuthoritySnapshot(t)
	assertCedarAuthorityDelta(t, before, after, 1, 0, 0, 1, 1)
	requireCedarEpochTrace(t, trace[:len([]string{
		"epoch-read", "authority-lock", "db-now", "epoch-bump", "freshness-create", "audit-append",
	})], []string{
		"epoch-read", "authority-lock", "db-now", "epoch-bump", "freshness-create", "audit-append",
	})
	freshness, found := f.freshness(t)
	if !found || !freshness.RefreshedAt.Equal(dbNow) {
		t.Fatalf("backfilled freshness = found:%t %+v, want DB time %s", found, freshness, dbNow)
	}
	var freshnessID model.ID
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			return err
		}
		record, found, err := findOne(context.Background(), repo)
		if err != nil || !found {
			return errors.New("backfilled freshness singleton missing")
		}
		freshnessID = model.ID(record.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	event, meta := cedarEpochCanonicalAudit(t, f, "governance.policy_freshness_backfill")
	if event.Seq <= 0 || event.Actor != model.ActorSystem || event.ActorKind != model.ActorSystem ||
		event.TargetKind != policyFreshnessKind || event.TargetID != freshnessID {
		t.Fatalf("backfill audit provenance = %+v, want system/actual freshness singleton %s", event, freshnessID)
	}
	if len(meta) != 3 || meta["authorization_epoch"] != float64(after.epoch) ||
		meta["db_timestamp"] != dbNow.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("backfill audit metadata = %v, want closed epoch/selection/db_timestamp", meta)
	}
	selection, ok := meta["selection"].(map[string]any)
	if !ok || selection["authored"] != float64(1) || selection["managed"] != float64(0) || selection["adopted"] != float64(0) {
		t.Fatalf("backfill audit selection = %#v, want authored-only selection", meta["selection"])
	}
	for _, forbidden := range []string{"policy", "source", "error"} {
		if _, exists := meta[forbidden]; exists {
			t.Fatalf("backfill audit leaked forbidden %q metadata: %v", forbidden, meta)
		}
	}

	// The next boot/reload reuses the durable anchor. It may read/lock to prove
	// that fact, but it must not bump epoch, write freshness, or append another
	// authority-change audit event.
	beforeSecond := f.cedarAuthoritySnapshot(t)
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	assertCedarAuthorityDelta(t, beforeSecond, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	afterSecond, found := f.freshness(t)
	if !found || !afterSecond.RefreshedAt.Equal(dbNow) {
		t.Fatalf("second boot re-stamped freshness: found:%t %+v", found, afterSecond)
	}
}

func TestCedarBackfillUsesLockedCASWitnessWithoutSecondRead(t *testing.T) {
	f := newManagedEpochFixture(t)
	f.m.grants.maxStaleness = time.Hour
	f.seedActivatedCedarSurface(t, surfaceCedar, `forbid(principal, action, resource);`)
	var epochs *cedarEpochWrongSecondReadStore
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		mutateWrap: func(sc store.Scope) store.Scope {
			epochs = &cedarEpochWrongSecondReadStore{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
			out := newManagedEpochScope(sc)
			out.epochs = epochs
			return out
		},
	})
	before := f.cedarAuthoritySnapshot(t)
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("backfill with valid-looking post-lock read: %v", err)
	}
	if epochs == nil || epochs.reads != 1 || epochs.bumps != 1 {
		t.Fatalf("backfill epoch operations = %+v, want one locked read/one CAS", epochs)
	}
	wantLocked := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: before.epoch}
	if epochs.bumpExpected != wantLocked {
		t.Fatalf("backfill CAS expected = %+v, want locked witness %+v", epochs.bumpExpected, wantLocked)
	}
	after := f.cedarAuthoritySnapshot(t)
	assertCedarAuthorityDelta(t, before, after, 1, 0, 0, 1, 1)
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !state.available || state.generation.Version != after.epoch {
		t.Fatalf("backfill runtime did not bind CAS G+1: loaded:%t state:%+v durable:%d", loaded, state, after.epoch)
	}
	_, meta := cedarEpochCanonicalAudit(t, f, "governance.policy_freshness_backfill")
	if meta["authorization_epoch"] != float64(after.epoch) {
		t.Fatalf("backfill audit used decorated post-lock generation: meta=%v durable=%d", meta, after.epoch)
	}
}

func TestCedarBackfillPostLockFailureDominatesDelayedReplay(t *testing.T) {
	f := newManagedEpochFixture(t)
	f.m.grants.maxStaleness = time.Hour
	const source = `permit(principal, action, resource);`
	revision := f.seedActivatedCedarSurface(t, surfaceCedar, source)

	beforeAuthority := f.cedarAuthoritySnapshot(t)
	oldCandidate := cedarEpochState(
		f.tenant,
		beforeAuthority.epoch,
		activationID{authored: revision},
		source,
	)
	if _, err := f.m.grants.installIfNotOlder(f.tenant, oldCandidate); err != nil {
		t.Fatalf("install pre-bound live G/T0: %v", err)
	}
	old, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !old.available || !hasCedarCompiledBinding(old) {
		t.Fatalf("precondition live G/T0 = loaded:%t state:%+v", loaded, old)
	}

	injected := errors.New("injected post-lock backfill selection read failure")
	var replayErr error
	probe := &cedarEpochReplayThenFailRevisionRepo{
		surface: surfaceCedar,
		err:     injected,
		replay: func() {
			// B began from G/T0 and finishes after A has acquired the backfill
			// epoch lock. It has no evidence that freshness/audit completed.
			_, replayErr = f.m.grants.installIfNotOlderFromObservedState(f.tenant, old, true, old)
		},
	}
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			base, err := sc.Ext(revisionKind)
			if err != nil {
				t.Fatal(err)
			}
			probe.GenericRepo = base
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{revisionKind: probe}
			return out
		},
	})
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); !errors.Is(err, injected) {
		t.Fatalf("post-lock backfill failure = %v, want injected error", err)
	}
	if !probe.fired || replayErr != nil {
		t.Fatalf("post-lock backfill probe = fired:%t replay:%v", probe.fired, replayErr)
	}
	assertCedarAuthorityDelta(t, beforeAuthority, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || after.available || !after.identityIncomplete || after.set != nil ||
		after.generation != old.generation || after.operation == old.operation || hasCedarCompiledBinding(after) {
		t.Fatalf("post-lock backfill failure left delayed replay available: old=%+v after=%+v", old, after)
	}

	// A later complete backfill writes the missing local anchor/audit atomically,
	// advances to G+1, and then reloads that coherent snapshot.
	f.m.UseData(api.NewModuleData(f.st))
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent backfill/reload after fence: %v", err)
	}
	recovered, recoveredLoaded := f.m.grants.tenantState(f.tenant)
	if !recoveredLoaded || !recovered.available || recovered.identityIncomplete ||
		recovered.generation.Version != old.generation.Version+1 || !hasCedarCompiledBinding(recovered) {
		t.Fatalf("coherent backfill/reload did not recover G+1: before=%+v after=%+v", after, recovered)
	}
	if freshness, found := f.freshness(t); !found || freshness.RefreshedAt.IsZero() {
		t.Fatalf("coherent backfill did not leave a durable freshness anchor: found:%t freshness:%+v", found, freshness)
	}
}

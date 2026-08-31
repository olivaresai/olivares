// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
// activeRevisionNumber's activation-marker validation sees its target, while the
// immediately following getRevision in latestActiveSelection does not. A stable
// dangling marker is already rejected elsewhere; this decorator pins the second-read
// hole so it cannot be collapsed into an innocent empty surface.
type cedarEpochVanishingRevisionRepo struct {
	store.GenericRepo
	surface    string
	revision   int64
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
		if r.exactReads >= 2 {
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

func TestCedarPublishWithModuleDataMiswiredMarksRuntimeUnavailable(t *testing.T) {
	f := newManagedEpochFixture(t)
	// The route still has a real request-scoped mutator, but the module's reload
	// data was never wired. The durable publish can commit; post-commit must turn
	// that miswiring into an unavailable sentinel, not an apparent successful
	// abstain/Allow evaluator.
	f.m.data = nil
	data := f.scopedData(nil)
	rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
	if rec.Code != http.StatusOK {
		t.Fatalf("miswired publish = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveDeferred {
		t.Fatalf("miswired publish live activation = %v, want deferred: %s", body["live_activation"], rec.Body.String())
	}
	if !cedarEpochHasAuditAction(t, f, "governance.pdp.activation_deferred") {
		t.Fatal("miswired deferred publish did not append compensatory activation evidence through mc.Data")
	}
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || state.available {
		t.Fatalf("miswired postcommit state = loaded:%t %+v, want unavailable sentinel", loaded, state)
	}
	req := auth.Request{Tenant: f.tenant, Principal: auth.Principal{Kind: auth.KindToken, CredID: "miswired"}, Permission: "agent:read", Resource: auth.ResourceFor("agent:read")}
	if _, err := f.m.grants.Scoped(context.Background(), req); err == nil {
		t.Fatal("Scoped accepted postcommit module-data miswiring")
	}
	if _, err := f.m.grants.Evaluate(context.Background(), req); err == nil {
		t.Fatal("Evaluate accepted postcommit module-data miswiring")
	}
}

func TestUnconditionalGrantPermsDoesNotOfferRowsAcrossRuntimeStateChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*scopedEngine, model.TenantID, scopedTenantState, bool) error
	}{
		{
			name: "unavailable mark",
			mutate: func(engine *scopedEngine, tenant model.TenantID, state scopedTenantState, loaded bool) error {
				engine.markUnavailableIfStillSame(tenant, state, loaded)
				return nil
			},
		},
		{
			name: "exact replay token",
			mutate: func(engine *scopedEngine, tenant model.TenantID, state scopedTenantState, _ bool) error {
				_, err := engine.installIfNotOlder(tenant, state)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			created := f.createGrant(t, api.NewScopedData(f.st, f.tenant), map[string]any{
				"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if created.Code != http.StatusCreated {
				t.Fatalf("create real managed grant = %d body=%s", created.Code, created.Body.String())
			}
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded || !state.available || state.selection.managed == 0 || !hasCedarCompiledBinding(state) {
				t.Fatalf("real managed grant did not install a live projected snapshot: loaded:%t %+v", loaded, state)
			}
			// Control: the rows genuinely confer a UI permission while the runtime
			// capture is stable; the race assertion below is not merely an empty
			// fixture returning nil by coincidence.
			stable, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
			if err != nil || len(stable) == 0 {
				t.Fatalf("stable unconditional grants = %v err=%v, want nonempty", stable, err)
			}
			f.m.UseData(cedarEpochModuleData{
				st: f.st,
				afterView: func() {
					before, loaded := f.m.grants.tenantState(f.tenant)
					if err := tc.mutate(f.m.grants, f.tenant, before, loaded); err != nil {
						t.Errorf("runtime state mutation during whoami View: %v", err)
					}
				},
			})
			perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
			if err != nil {
				t.Fatalf("whoami grant report across %s: %v", tc.name, err)
			}
			if len(perms) != 0 {
				t.Fatalf("whoami offered permissions across %s: %v", tc.name, perms)
			}
		})
	}
}

func TestUnconditionalGrantPermsRequiresLoadedAvailableRuntimeSnapshot(t *testing.T) {
	t.Run("cold runtime without rows", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
			t.Fatal("cold no-row tenant unexpectedly has a loaded Cedar runtime state")
		}
		perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
		if err != nil || len(perms) != 0 {
			t.Fatalf("cold no-row tenant grants = %v err=%v, want nil/no error", perms, err)
		}
	})
	t.Run("cold runtime with durable rows", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		// Seed a real applicable row without its C2 projection/reload. This pins
		// the !beforeLoaded gate: UI reporting must not infer live authority from
		// rows the evaluator has never installed.
		f.seedGrant(t, scopedGrant{
			SubjectKind: subjectUser,
			SubjectRef:  f.actor.UserID.String(),
			Role:        auth.RoleEditor,
			Scope:       scopeSpec{Tree: scopeTenant},
		})
		if _, loaded := f.m.grants.tenantState(f.tenant); loaded {
			t.Fatal("directly seeded row unexpectedly installed a Cedar runtime state")
		}
		perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
		if err != nil || len(perms) != 0 {
			t.Fatalf("cold tenant with durable grant rows = %v err=%v, want nil/no error", perms, err)
		}
	})
	for _, tc := range []struct {
		name   string
		mutate func(*managedEpochFixture)
	}{
		{name: "missing evaluator", mutate: func(f *managedEpochFixture) { f.m.grants = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			created := f.createGrant(t, api.NewScopedData(f.st, f.tenant), map[string]any{
				"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if created.Code != http.StatusCreated {
				t.Fatalf("create real managed grant = %d body=%s", created.Code, created.Body.String())
			}
			tc.mutate(f)
			perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
			if err != nil || len(perms) != 0 {
				t.Fatalf("unconditional rows with %s = %v err=%v, want nil/no error", tc.name, perms, err)
			}
		})
	}
}

func TestUnconditionalGrantPermsDoesNotOfferRowsWhenDurableEpochOutrunsRuntime(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	first := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	})
	if first.Code != http.StatusCreated {
		t.Fatalf("initial managed grant = %d body=%s", first.Code, first.Body.String())
	}
	live, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !live.available || live.selection.managed == 0 || !hasCedarCompiledBinding(live) {
		t.Fatalf("initial managed projection did not establish a live state: loaded:%t %+v", loaded, live)
	}
	if perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant); err != nil || len(perms) == 0 {
		t.Fatalf("stable managed UI report = %v err=%v, want nonempty", perms, err)
	}

	// The C2 writer commits through its request-scoped data before its postcommit
	// reload opens Module.data.View. Pause exactly there: durable rows and epoch
	// are G+1 while the runtime remains the valid, stable G snapshot.
	gate := &cedarEpochGateModuleData{st: f.st, entered: make(chan struct{}), release: make(chan struct{})}
	f.m.UseData(gate)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.m.handleCreateScopedGrant(rec, managedEpochRequest(t, http.MethodPost, "/grants", map[string]any{
			"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleViewer, "scope_tree": scopeTenant,
		}, "", ""), f.moduleContext(data))
		result <- rec
	}()
	released := false
	defer func() {
		if !released {
			close(gate.release)
		}
	}()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		close(gate.release)
		released = true
		t.Fatal("managed writer did not reach postcommit reload View")
	}
	var durable store.AuthorizationFactRef
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		durable, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if durable.Version <= live.generation.Version {
		t.Fatalf("writer did not commit G+1 before reload: durable=%+v live=%+v", durable, live.generation)
	}
	perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
	if err != nil {
		t.Fatalf("UI report while durable epoch outruns runtime: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("UI report offered stale G permissions while durable epoch is G+1: %v", perms)
	}
	close(gate.release)
	released = true
	select {
	case rec := <-result:
		if rec.Code != http.StatusCreated {
			t.Fatalf("gated managed writer = %d body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("managed writer did not complete after reload gate released")
	}
}

func TestUnconditionalGrantPermsWithSameGenerationDurableDriftUnderReports(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("authored seed publish = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("managed seed grant = %d body=%s", rec.Code, rec.Body.String())
	}
	before, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || before.selection.authored == 0 || !before.available || !hasCedarCompiledBinding(before) {
		t.Fatalf("seeded state is not live: loaded:%t %+v", loaded, before)
	}
	if perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant); err != nil || len(perms) == 0 {
		t.Fatalf("stable UI report = %v err=%v, want nonempty", perms, err)
	}

	// Preserve selection and epoch but alter only the durable authored bytes
	// visible to the report's View. A generation-only check would still offer
	// rows; full Cedar authority identity must withhold them.
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			base, err := sc.Ext(revisionKind)
			if err != nil {
				t.Fatal(err)
			}
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{
				revisionKind: cedarEpochRewrittenRevisionRepo{
					GenericRepo: base, surface: surfaceCedar, revision: before.selection.authored,
					content: `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
				},
			}
			return out
		},
	})
	perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
	if err != nil {
		t.Fatalf("same-G durable drift UI report: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("same-G durable drift over-offered UI permissions: %v", perms)
	}
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || after.operation != before.operation || !sameCedarAuthorityState(after, before) {
		t.Fatalf("read-only durable drift probe changed runtime state: before=%+v after=%+v", before, after)
	}
}

func TestUnconditionalGrantPermsWithRowsProjectionDriftUnderReports(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": f.actor.UserID.String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("managed seed grant = %d body=%s", rec.Code, rec.Body.String())
	}
	before, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || before.selection.managed == 0 || !before.available || !hasCedarCompiledBinding(before) {
		t.Fatalf("seeded managed state is not live: loaded:%t %+v", loaded, before)
	}
	if perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant); err != nil || len(perms) == 0 {
		t.Fatalf("stable UI report = %v err=%v, want nonempty", perms, err)
	}
	// Add a durable row directly without C2's projection/epoch/reload. The live
	// selection remains exact, but re-projecting all current rows must no longer
	// match its selected cedar-managed digest.
	f.seedGrant(t, scopedGrant{
		SubjectKind: subjectUser,
		SubjectRef:  f.actor.UserID.String(),
		Role:        auth.RoleViewer,
		Scope:       scopeSpec{Tree: scopeTenant},
	})
	var projectedDigest string
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		grants, err := loadScopedGrants(context.Background(), sc)
		if err != nil {
			return err
		}
		roles, err := loadCustomRoles(context.Background(), sc)
		if err != nil {
			return err
		}
		groups, err := loadPermGroups(context.Background(), sc)
		if err != nil {
			return err
		}
		projectedDigest = contentDigest(projectManagedCedar(grants, roles, groups))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if projectedDigest == before.managedDigest {
		t.Fatal("direct row drift did not change the recomputed managed projection")
	}
	perms, err := f.m.UnconditionalGrants().UnconditionalGrantPerms(context.Background(), f.actor, f.tenant)
	if err != nil {
		t.Fatalf("row/projection drift UI report: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("row/projection drift over-offered UI permissions: %v", perms)
	}
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

func TestReloadTenantGrantsUsesObservedOperationCAS(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	before, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !before.available {
		t.Fatalf("publish did not establish G/T0: loaded:%t %+v", loaded, before)
	}
	// A's View captures G/T0. B's failure changes only the operation to T1 and
	// leaves a valid G unavailable. A must return stale rather than use the old
	// coherent candidate to recover it.
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		afterView: func() {
			f.m.grants.markUnavailableIfStillSame(f.tenant, before, loaded)
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, errScopedSnapshotStaleOperation) {
		t.Fatalf("late reload after same-G failure = %v, want stale operation", err)
	}
	afterFailure, _ := f.m.grants.tenantState(f.tenant)
	if afterFailure.available || afterFailure.operation == before.operation {
		t.Fatalf("late reload resurrected failed G/T1 state: before=%+v after=%+v", before, afterFailure)
	}
	// A later coherent replay captures T1 and is allowed to make the runtime
	// available again; the token is a CAS fence, not a permanent poison bit.
	f.m.UseData(api.NewModuleData(f.st))
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent retry after unavailable T1: %v", err)
	}
	recovered, _ := f.m.grants.tenantState(f.tenant)
	if !recovered.available || recovered.operation == afterFailure.operation {
		t.Fatalf("coherent retry did not recover same-G runtime: before=%+v after=%+v", afterFailure, recovered)
	}
}

// TestReloadTenantGrantsNewerInvalidSnapshotDominatesOlderReplay fixes the ordering
// that matters when a stale exact-G replay completes while reload has already captured
// a corrupt G+1. The compile failure is durable authority evidence: it must make the
// live engine unavailable even though the replay has advanced only its local operation
// token. Leaving G available here would continue to grant after durable authority
// advanced.
func TestReloadTenantGrantsNewerInvalidSnapshotDominatesOlderReplay(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `permit(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("initial publish = %d body=%s", rec.Code, rec.Body.String())
	}
	old, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !old.available {
		t.Fatalf("initial G/T0 is not available: loaded=%t state=%+v", loaded, old)
	}

	// Model an out-of-band/decorated durable writer. Honest authored writes compile
	// before committing, but reload must still fail closed if the current authoritative
	// history is malformed. The mutation atomically advances the epoch and selects those
	// bytes; A's later durable View observes G+1 while B retains its earlier G/T0 view.
	f.mutate(t, func(sc store.Scope) error {
		locked, err := lockPolicyAuthorizationEpochWitness(context.Background(), sc)
		if err != nil {
			return err
		}
		if _, err = advancePolicyAuthorizationEpochFrom(context.Background(), sc, locked); err != nil {
			return err
		}
		number, _, err := appendRevision(context.Background(), sc, surfaceCedar, `not valid cedar`, "probe", true, true, "")
		if err != nil {
			return err
		}
		_, err = activateRevision(context.Background(), sc, surfaceCedar, number, "probe")
		return err
	})

	var replayErr error
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		afterView: func() {
			// B started from old G/T0 and finishes its exact replay after A's View,
			// rotating the token to T1 while leaving G compiled and available.
			_, replayErr = f.m.grants.installIfNotOlderFromObservedState(f.tenant, old, true, old)
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); err == nil {
		t.Fatal("malformed durable G+1 unexpectedly reloaded")
	}
	if replayErr != nil {
		t.Fatalf("older exact-G replay was not accepted: %v", replayErr)
	}
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || after.available || after.generation.Version != old.generation.Version+1 ||
		after.sameIdentity(old) || after.set != nil || after.operation == old.operation {
		t.Fatalf("newer invalid durable snapshot did not dominate older replay: old=%+v after=%+v", old, after)
	}
}

// TestReloadTenantGrantsPostEpochReadFailureDominatesOldReplay closes the ordering
// hole where the View reads durable G+1 successfully, then a later selected-revision
// read fails. The epoch is still authoritative high-water evidence; a delayed exact-G
// replay cannot turn that post-epoch failure into a token-only transient.
func TestReloadTenantGrantsPostEpochReadFailureDominatesOldReplay(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `permit(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("initial publish = %d body=%s", rec.Code, rec.Body.String())
	}
	old, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !old.available || !hasCedarCompiledBinding(old) {
		t.Fatalf("initial G/T0 is not available: loaded=%t state=%+v", loaded, old)
	}

	// Advance only the durable epoch. A corrupt/decorated source can fail before
	// the rest of the same snapshot is available; the test isolates the fact that
	// the exact G+1 was already read by reloadTenantGrants.
	f.mutate(t, func(sc store.Scope) error {
		locked, err := lockPolicyAuthorizationEpochWitness(context.Background(), sc)
		if err != nil {
			return err
		}
		_, err = advancePolicyAuthorizationEpochFrom(context.Background(), sc, locked)
		return err
	})

	injected := errors.New("injected post-epoch selected-revision read failure")
	var replayErr error
	probe := &cedarEpochReplayThenFailRevisionRepo{
		surface: surfaceCedar,
		err:     injected,
		replay: func() {
			// B began from old G/T0 and completes after A has already observed
			// durable G+1, rotating only the in-process operation token.
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
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, injected) {
		t.Fatalf("post-epoch failed reload = %v, want injected error", err)
	}
	if !probe.fired || replayErr != nil {
		t.Fatalf("post-epoch read probe = fired:%t replay:%v", probe.fired, replayErr)
	}
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || after.available || !after.identityIncomplete || after.set != nil ||
		after.generation.Version != old.generation.Version+1 || after.operation == old.operation ||
		hasCedarCompiledBinding(after) {
		t.Fatalf("post-epoch G+1 did not dominate delayed old replay: old=%+v after=%+v", old, after)
	}
	if result, err := f.m.grants.installIfNotOlderFromObservedState(f.tenant, old, true, old); err != nil || result != scopedInstallOlder {
		t.Fatalf("old G candidate crossed post-epoch G+1 fence: result:%v err:%v", result, err)
	}

	// A single coherent reread that begins from the fence token recovers the
	// complete G+1 snapshot. It is not treated as a selected-empty union.
	f.m.UseData(api.NewModuleData(f.st))
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent G+1 reread: %v", err)
	}
	recovered, recoveredLoaded := f.m.grants.tenantState(f.tenant)
	if !recoveredLoaded || !recovered.available || recovered.identityIncomplete ||
		recovered.generation.Version != after.generation.Version ||
		recovered.operation == after.operation || !hasCedarCompiledBinding(recovered) {
		t.Fatalf("coherent G+1 reread did not recover incomplete fence: before=%+v after=%+v", after, recovered)
	}

	// A higher complete generation remains monotonic after recovery.
	f.mutate(t, func(sc store.Scope) error {
		locked, err := lockPolicyAuthorizationEpochWitness(context.Background(), sc)
		if err != nil {
			return err
		}
		_, err = advancePolicyAuthorizationEpochFrom(context.Background(), sc, locked)
		return err
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent G+2 reread: %v", err)
	}
	higher, _ := f.m.grants.tenantState(f.tenant)
	if !higher.available || higher.identityIncomplete || higher.generation.Version != recovered.generation.Version+1 || !hasCedarCompiledBinding(higher) {
		t.Fatalf("higher coherent generation did not replace recovered G+1: recovered=%+v higher=%+v", recovered, higher)
	}
}

// TestReloadTenantGrantsPartialSameGenerationFailureDominatesOldReplay proves that an
// equal epoch is not safe to token-CAS after a later read fails. The View has already
// observed authored B at G before the managed read fails; a delayed replay of authored
// A must not remain available merely because it rotated the operation token last.
func TestReloadTenantGrantsPartialSameGenerationFailureDominatesOldReplay(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	const authoredA = `permit(principal, action, resource);`
	const authoredB = `forbid(principal, action, resource) when { context.permission == "agent:write" };`
	if rec := invokeCedarEpochPublish(t, f, data, authoredA); rec.Code != http.StatusOK {
		t.Fatalf("initial publish = %d body=%s", rec.Code, rec.Body.String())
	}
	old, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !old.available || old.selection.authored == 0 {
		t.Fatalf("initial G/T0 is not available: loaded=%t state=%+v", loaded, old)
	}

	injected := errors.New("injected managed read after authored drift")
	var replayErr error
	probe := &cedarEpochReplayThenFailRevisionRepo{
		surface: surfaceCedarManaged,
		err:     injected,
		replay: func() {
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
			// latestActiveRevision(surfaceCedar) completes before the managed
			// surface is queried, so A has observed authored B at G when the
			// probe makes its later managed List fail.
			rewritten := cedarEpochRewrittenRevisionRepo{
				GenericRepo: base,
				surface:     surfaceCedar,
				revision:    old.selection.authored,
				content:     authoredB,
			}
			probe.GenericRepo = rewritten
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{revisionKind: probe}
			return out
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, injected) {
		t.Fatalf("partial same-G failed reload = %v, want injected error", err)
	}
	if !probe.fired || replayErr != nil {
		t.Fatalf("partial same-G probe = fired:%t replay:%v", probe.fired, replayErr)
	}
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || after.available || !after.identityIncomplete || after.set != nil ||
		after.generation != old.generation || after.operation == old.operation || hasCedarCompiledBinding(after) {
		t.Fatalf("partial same-G observation left old replay available: old=%+v after=%+v", old, after)
	}

	// The actual durable history still contains A. A complete reread from the
	// incomplete fence is allowed to restore it in one pass.
	f.m.UseData(api.NewModuleData(f.st))
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent same-G reread after partial failure: %v", err)
	}
	recovered, _ := f.m.grants.tenantState(f.tenant)
	if !recovered.available || recovered.identityIncomplete || !recovered.sameIdentity(old) ||
		recovered.operation == after.operation || !hasCedarCompiledBinding(recovered) {
		t.Fatalf("coherent same-G reread did not recover old durable identity: before=%+v after=%+v", after, recovered)
	}
}

// TestScopedIncompleteGenerationFenceOrderingAndEmptyRecovery pins the two
// generation-only rules independently of a repository fixture: a partial G-1 cannot
// poison live G, and an explicit incomplete G fence is not confused with a complete
// selected-empty Cedar authority snapshot.
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

func TestReloadTenantGrantsSameGenerationIdentityDriftDominatesOlderReplay(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	before, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !before.available || before.selection.authored == 0 {
		t.Fatalf("publish did not establish available G/T0: loaded:%t %+v", loaded, before)
	}
	var replayed scopedTenantState
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			base, err := sc.Ext(revisionKind)
			if err != nil {
				t.Fatal(err)
			}
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{
				revisionKind: cedarEpochRewrittenRevisionRepo{
					GenericRepo: base, surface: surfaceCedar, revision: before.selection.authored,
					content: `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
				},
			}
			return out
		},
		afterView: func() {
			if _, err := f.m.grants.installIfNotOlder(f.tenant, before); err != nil {
				t.Errorf("exact replay during stale mismatch: %v", err)
				return
			}
			replayed, _ = f.m.grants.tenantState(f.tenant)
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); !errors.Is(err, errScopedSnapshotSameGenerationMismatch) {
		t.Fatalf("same-G identity drift reload = %v, want mismatch", err)
	}
	after, _ := f.m.grants.tenantState(f.tenant)
	if replayed.operation == nil || after.available || after.operation == replayed.operation ||
		after.sameIdentity(before) || after.set != nil {
		t.Fatalf("same-G drift left older replay available: before=%+v replay=%+v after=%+v", before, replayed, after)
	}
	// The decorator represents the current durable identity. A subsequent coherent
	// reread of those exact bytes, begun after the unavailable operation, may recover
	// the runtime; the conflict is not a permanent poison bit.
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			base, err := sc.Ext(revisionKind)
			if err != nil {
				t.Fatal(err)
			}
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{
				revisionKind: cedarEpochRewrittenRevisionRepo{
					GenericRepo: base, surface: surfaceCedar, revision: before.selection.authored,
					content: `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
				},
			}
			return out
		},
	})
	if err := f.m.reloadTenantGrants(context.Background(), f.tenant); err != nil {
		t.Fatalf("coherent same-G identity reread: %v", err)
	}
	recovered, _ := f.m.grants.tenantState(f.tenant)
	if !recovered.available || !recovered.sameIdentity(after) || !hasCedarCompiledBinding(recovered) || recovered.operation == after.operation {
		t.Fatalf("coherent same-G reread did not recover drift identity: before=%+v after=%+v", after, recovered)
	}
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

func TestReloadOlderInvalidSnapshotDoesNotPoisonNewerRuntime(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	for _, source := range []string{
		`forbid(principal, action, resource);`,
		`forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	} {
		if rec := invokeCedarEpochPublish(t, f, data, source); rec.Code != http.StatusOK {
			t.Fatalf("seed publish = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	before, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !before.available || before.generation.Version < 2 || !hasCedarCompiledBinding(before) {
		t.Fatalf("seeded runtime is not a valid newer generation: loaded:%t %+v", loaded, before)
	}
	// Deliberately create malformed selected bytes without advancing the durable
	// epoch, then decorate the snapshot's epoch down to G. A stale reload must
	// discard this candidate before compile rather than mark cached G+1 unavailable.
	f.mutate(t, func(sc store.Scope) error {
		number, _, err := appendRevision(context.Background(), sc, surfaceCedar, `this is not valid Cedar`, "seed", true, true, "")
		if err != nil {
			return err
		}
		_, err = activateRevision(context.Background(), sc, surfaceCedar, number, "seed")
		return err
	})
	older := before.generation
	older.Version--
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			out := newManagedEpochScope(sc)
			out.epochs = &managedEpochScriptedStore{fact: older}
			return out
		},
	})
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("lower invalid snapshot did not become monotonic no-op: %v", err)
	}
	after, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || !after.available || after.generation != before.generation || after.operation != before.operation || !hasCedarCompiledBinding(after) {
		t.Fatalf("lower invalid reload changed/poisoned G+1: before=%+v after=%+v", before, after)
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

func TestCedarSelectedRevisionTOCTOUFailsClosedAndWritesNothing(t *testing.T) {
	for _, surface := range []string{surfaceCedar, surfaceCedarManaged, surfaceCedarDDIL} {
		t.Run(surface, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			revision := f.seedActivatedCedarSurface(t, surface, `forbid(principal, action, resource);`)
			vanisher := &cedarEpochVanishingRevisionRepo{surface: surface, revision: revision}
			wrap := func(sc store.Scope) store.Scope {
				base, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				vanisher.GenericRepo = base
				out := newManagedEpochScope(sc)
				out.ext = map[model.Kind]store.GenericRepo{revisionKind: vanisher}
				return out
			}
			before := f.snapshot(t)
			f.m.UseData(cedarEpochModuleData{st: f.st, wrap: wrap})
			if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err == nil {
				t.Fatal("reload accepted a selected revision that disappeared on its second read")
			}
			if vanisher.exactReads < 2 {
				t.Fatalf("selected revision was not read twice: reads=%d", vanisher.exactReads)
			}
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded || state.available {
				t.Fatalf("TOCTOU reload did not install unavailable state: loaded=%t state=%+v", loaded, state)
			}
			assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)

			// Managed/DDIL content enters a new authored publish union, so that writer
			// must reject before CAS/revision/audit. A publish replaces authored bytes
			// and intentionally does not depend on its previous selection; use an
			// authored rollback instead, whose current-selection read is authority.
			vanisher.exactReads = 0
			writerData := f.scopedData(wrap)
			writerBefore := f.snapshot(t)
			var rec *httptest.ResponseRecorder
			if surface == surfaceCedar {
				rec = invokeCedarEpochRollback(t, f, writerData, revision)
			} else {
				rec = invokeCedarEpochPublish(t, f, writerData, `forbid(principal, action, resource);`)
			}
			if rec.Code < http.StatusInternalServerError || writerData.innerErr == nil {
				t.Fatalf("writer around disappearing %s selection = status:%d err:%v body:%s", surface, rec.Code, writerData.innerErr, rec.Body.String())
			}
			assertManagedEpochDelta(t, writerBefore, f.snapshot(t), 0, 0, 0, 0, 0, 0)
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

// TestCedarRollbackTargetIdentityTOCTOUWritesNothing exercises the target read
// before rollback's compile/CAS path. Unlike selection reload, this is the first
// exact read of the target revision: a malformed repository response must fail
// before it can compile one revision's bytes and append an activation for another.
func TestCedarRollbackTargetIdentityTOCTOUWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name               string
		replacementSurface string
	}{
		{name: "revision number"},
		{name: "surface", replacementSurface: surfaceOPA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			plain := api.NewScopedData(f.st, f.tenant)
			first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
			if first.Code != http.StatusOK {
				t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
			}
			target := cedarEpochResponseRevision(t, first)
			second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
			if second.Code != http.StatusOK {
				t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
			}

			probe := &cedarEpochWrongIdentityRevisionRepo{
				surface: surfaceCedar, revision: target, replacementSurface: tc.replacementSurface,
				rewriteAt: 1,
			}
			data := f.scopedData(func(sc store.Scope) store.Scope {
				base, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				probe.GenericRepo = base
				out := newManagedEpochScope(sc)
				out.ext = map[model.Kind]store.GenericRepo{revisionKind: probe}
				return out
			})
			before := f.cedarAuthoritySnapshot(t)
			rec := invokeCedarEpochRollback(t, f, data, target)
			if rec.Code < http.StatusInternalServerError || data.innerErr == nil {
				t.Fatalf("rollback accepted wrong %s target identity: status=%d err=%v body=%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			if probe.exactReads != 1 {
				t.Fatalf("rollback target exact reads=%d, want 1", probe.exactReads)
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
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

func TestCedarGetDefersWhenLiveReplayRacesDurableView(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	stable, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !stable.available || !hasCedarCompiledBinding(stable) {
		t.Fatalf("publish did not establish a usable state: loaded=%t %+v", loaded, stable)
	}
	hooked := cedarEpochScopedData{
		st: f.st, tenant: f.tenant,
		afterView: func() {
			if _, err := f.m.grants.installIfNotOlder(f.tenant, stable); err != nil {
				t.Errorf("same-G replay hook: %v", err)
			}
		},
	}
	rec := httptest.NewRecorder()
	f.m.handlePdpActive(rec, managedEpochRequest(t, http.MethodGet, "/pdp/active?engine=cedar", nil, "", ""), api.ModuleContext{
		Tenant: f.tenant, Principal: f.actor, Data: hooked,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET active = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveDeferred {
		t.Fatalf("GET across live replay = %v, want deferred: %s", body["live_activation"], rec.Body.String())
	}
}

func TestCedarGETCarriesAuthoredDTOFromDurableSnapshot(t *testing.T) {
	f := newManagedEpochFixture(t)
	sourceA := `forbid(principal, action, resource);`
	if rec := invokeCedarEpochPublish(t, f, api.NewScopedData(f.st, f.tenant), sourceA); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || state.selection.authored == 0 || !state.available {
		t.Fatalf("publish did not install authored state: loaded:%t %+v", loaded, state)
	}
	var probe *cedarEpochThirdReadRewriteRevisionRepo
	data := cedarEpochScopedData{
		st: f.st, tenant: f.tenant,
		viewWrap: func(sc store.Scope) store.Scope {
			base, err := sc.Ext(revisionKind)
			if err != nil {
				t.Fatal(err)
			}
			probe = &cedarEpochThirdReadRewriteRevisionRepo{
				GenericRepo: base, surface: surfaceCedar, revision: state.selection.authored,
				content: `permit(principal, action, resource);`,
			}
			out := newManagedEpochScope(sc)
			out.ext = map[model.Kind]store.GenericRepo{revisionKind: probe}
			return out
		},
	}
	rec := httptest.NewRecorder()
	f.m.handlePdpActive(rec, managedEpochRequest(t, http.MethodGet, "/pdp/active?engine=cedar", nil, "", ""), f.moduleContext(data))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET active = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	authored, ok := body["authored"].(map[string]any)
	if !ok {
		t.Fatalf("GET authored surface = %#v, want object", body["authored"])
	}
	if probe == nil || probe.exactReads != 2 {
		t.Fatalf("GET selected-revision exact reads = %d, want exactly the two snapshot reads (a third returns altered bytes)", probe.exactReads)
	}
	if authored["content"] != sourceA || authored["sha256"] != contentDigest(sourceA) ||
		body["union_sha256"] != contentDigest(sourceA) || body["live_activation"] != liveApplied {
		t.Fatalf("GET mixed durable DTO/content state: authored=%v union=%v live=%v want source A and matching digest/applied", authored, body["union_sha256"], body["live_activation"])
	}
}

func TestCedarPublishDefersAndAuditsWhenSameGenerationReplayRacesConfirm(t *testing.T) {
	f := newManagedEpochFixture(t)
	views := 0
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		afterView: func() {
			views++
			// View 1 is reloadTenantGrants before its install. View 2 is the
			// post-commit confirmation bracket after that install, where an exact
			// replay must invalidate the before→after operation-token proof.
			if views != 2 {
				return
			}
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded {
				t.Errorf("confirm hook ran before the candidate runtime install")
				return
			}
			if _, err := f.m.grants.installIfNotOlder(f.tenant, state); err != nil {
				t.Errorf("same-G replay during confirm: %v", err)
			}
		},
	})
	rec := invokeCedarEpochPublish(t, f, api.NewScopedData(f.st, f.tenant), `forbid(principal, action, resource);`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if views != 2 || body["live_activation"] != liveDeferred {
		t.Fatalf("post-confirm replay = views:%d live:%v body:%s, want two views/deferred", views, body["live_activation"], rec.Body.String())
	}
	if !cedarEpochHasAuditAction(t, f, "governance.pdp.activation_deferred") {
		t.Fatal("post-confirm replay did not append compensatory activation-deferred evidence")
	}
	_, meta := cedarEpochCanonicalAudit(t, f, "governance.pdp.activation_deferred")
	if meta["state"] != string(cedarLiveMismatch) || meta["exact_committed_snapshot_enforcing"] != false {
		t.Fatalf("same-G replay deferral metadata = %v, want closed mismatch/false", meta)
	}
	for _, forbidden := range []string{"error", "cause", "reason"} {
		if _, found := meta[forbidden]; found {
			t.Fatalf("same-G replay deferral leaked raw %q: %v", forbidden, meta)
		}
	}
}

func TestCedarPublishDefersAndAuditsWhenNewerGenerationSupersedesIt(t *testing.T) {
	f := newManagedEpochFixture(t)
	views := 0
	var hookErr error
	var newer scopedTenantState
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		afterView: func() {
			views++
			// View 1 read the committed Cedar G candidate for reload. Simulate a
			// real C1 ABAC writer committing G+1 before that candidate installs,
			// then install the coherent G+1 snapshot that won the race.
			if views != 1 || hookErr != nil {
				return
			}
			abac := httptest.NewRecorder()
			f.m.handleCreatePolicy(abac, managedEpochRequest(t, http.MethodPost, "/policies", map[string]any{
				"name": "supersede-publish-generation", "kind": policyKindABAC, "enabled": true,
				"spec": map[string]any{"rules": []any{map[string]any{"permission": "agent:write", "deny": true}}},
			}, "", ""), f.moduleContext(api.NewScopedData(f.st, f.tenant)))
			if abac.Code != http.StatusCreated {
				hookErr = fmt.Errorf("superseding ABAC writer = %d body=%s", abac.Code, abac.Body.String())
				return
			}
			var snapshot cedarDurableSnapshot
			hookErr = f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
				var err error
				snapshot, err = readCedarDurableSnapshot(context.Background(), sc, f.tenant, f.m.grants.maxStaleness)
				return err
			})
			if hookErr != nil {
				return
			}
			if snapshot.combined != "" {
				snapshot.state.set, hookErr = compileGrantSet(snapshot.combined)
				if hookErr != nil {
					return
				}
			}
			_, hookErr = f.m.grants.installIfNotOlder(f.tenant, snapshot.state)
			if hookErr == nil {
				newer = snapshot.state
			}
		},
	})
	rec := invokeCedarEpochPublish(t, f, api.NewScopedData(f.st, f.tenant), `forbid(principal, action, resource);`)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("superseded publish = %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if views != 2 || body["live_activation"] != liveDeferred {
		t.Fatalf("superseded publish = views:%d live:%v body:%s, want two views/deferred", views, body["live_activation"], rec.Body.String())
	}
	live, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || live.generation != newer.generation || live.generation.Version <= 1 || !live.available {
		t.Fatalf("superseded publish replaced newer live generation: installed=%+v expected=%+v", live, newer)
	}
	_, meta := cedarEpochCanonicalAudit(t, f, "governance.pdp.activation_deferred")
	if meta["state"] != string(cedarLiveNewer) || meta["exact_committed_snapshot_enforcing"] != false {
		t.Fatalf("superseded publish deferral metadata = %v, want closed newer/false", meta)
	}
	for _, forbidden := range []string{"error", "cause", "reason"} {
		if _, found := meta[forbidden]; found {
			t.Fatalf("superseded publish deferral leaked raw %q: %v", forbidden, meta)
		}
	}
}

func TestCedarGETDefersAfterABACEpochAdvancesWithoutCedarReload(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	if rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`); rec.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
	}
	getActive := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		f.m.handlePdpActive(rec, managedEpochRequest(t, http.MethodGet, "/pdp/active?engine=cedar", nil, "", ""), f.moduleContext(data))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET active = %d body=%s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if got := getActive()["live_activation"]; got != liveApplied {
		t.Fatalf("published Cedar live activation = %v, want applied", got)
	}
	liveBefore, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded {
		t.Fatal("publish did not install Cedar runtime state")
	}

	policy := httptest.NewRecorder()
	f.m.handleCreatePolicy(policy, managedEpochRequest(t, http.MethodPost, "/policies", map[string]any{
		"name": "advance-abac-epoch", "kind": policyKindABAC, "enabled": true,
		"spec": map[string]any{"rules": []any{map[string]any{"permission": "agent:write", "deny": true}}},
	}, "", ""), f.moduleContext(data))
	if policy.Code != http.StatusCreated {
		t.Fatalf("ABAC writer = %d body=%s", policy.Code, policy.Body.String())
	}

	var durable cedarDurableSnapshot
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		durable, err = readCedarDurableSnapshot(context.Background(), sc, f.tenant, f.m.grants.maxStaleness)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if durable.state.generation.Version <= liveBefore.generation.Version ||
		durable.state.selection != liveBefore.selection ||
		durable.state.authoredDigest != liveBefore.authoredDigest ||
		durable.state.managedDigest != liveBefore.managedDigest ||
		durable.state.adoptedDigest != liveBefore.adoptedDigest ||
		durable.state.unionDigest != liveBefore.unionDigest {
		t.Fatalf("ABAC changed more than epoch or did not advance it: live=%+v durable=%+v", liveBefore, durable.state)
	}
	if got := getActive()["live_activation"]; got != liveDeferred {
		t.Fatalf("GET after ABAC-only epoch advance = %v, want deferred", got)
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

func TestAuthoredCedarPublishAndRollbackOrderDBClockAndCAS(t *testing.T) {
	t.Run("publish", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		dbNow := time.Date(2026, 8, 16, 13, 14, 15, 123456789, time.UTC)
		var trace []string
		before := f.cedarAuthoritySnapshot(t)
		rec := invokeCedarEpochPublish(t, f, cedarEpochOrderedData(t, f, dbNow, &trace), `forbid(principal, action, resource);`)
		if rec.Code != http.StatusOK {
			t.Fatalf("publish = %d body=%s", rec.Code, rec.Body.String())
		}
		requireCedarEpochTrace(t, trace, []string{
			"epoch-read", "authority-lock", "db-now", "epoch-bump",
			"revision-create", "revision-create", "freshness-create", "audit-append",
		})
		assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 1, 1, 1, 1, 1)
		freshness, found := f.freshness(t)
		if !found || !freshness.RefreshedAt.Equal(dbNow) {
			t.Fatalf("publish freshness = found:%t %+v, want DB transaction time %s", found, freshness, dbNow)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		data := api.NewScopedData(f.st, f.tenant)
		first := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
		if first.Code != http.StatusOK {
			t.Fatalf("publish first = %d body=%s", first.Code, first.Body.String())
		}
		firstRevision := cedarEpochResponseRevision(t, first)
		second := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
		if second.Code != http.StatusOK {
			t.Fatalf("publish second = %d body=%s", second.Code, second.Body.String())
		}
		dbNow := time.Date(2026, 8, 16, 13, 15, 16, 987654321, time.UTC)
		var trace []string
		before := f.cedarAuthoritySnapshot(t)
		rec := invokeCedarEpochRollback(t, f, cedarEpochOrderedData(t, f, dbNow, &trace), firstRevision)
		if rec.Code != http.StatusOK {
			t.Fatalf("rollback = %d body=%s", rec.Code, rec.Body.String())
		}
		requireCedarEpochTrace(t, trace, []string{
			"epoch-read", "authority-lock", "db-now", "epoch-bump",
			"revision-create", "freshness-update", "audit-append",
		})
		assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 1, 0, 1, 0, 1)
		freshness, found := f.freshness(t)
		if !found || !freshness.RefreshedAt.Equal(dbNow) {
			t.Fatalf("rollback freshness = found:%t %+v, want DB transaction time %s", found, freshness, dbNow)
		}
	})
}

func TestAuthoredCedarLockPrecedesEveryDependentRevisionAndFreshnessRead(t *testing.T) {
	injected := errors.New("dependent Cedar authority read happened before epoch lock")
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *managedEpochFixture) int64
		invoke func(*testing.T, *managedEpochFixture, api.ScopedData, int64) *httptest.ResponseRecorder
	}{
		{
			name:  "publish",
			setup: func(_ *testing.T, _ *managedEpochFixture) int64 { return 0 },
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData, _ int64) *httptest.ResponseRecorder {
				return invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
			},
		},
		{
			name: "rollback",
			setup: func(t *testing.T, f *managedEpochFixture) int64 {
				plain := api.NewScopedData(f.st, f.tenant)
				first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
				if first.Code != http.StatusOK {
					t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
				}
				second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
				if second.Code != http.StatusOK {
					t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
				}
				return cedarEpochResponseRevision(t, first)
			},
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData, target int64) *httptest.ResponseRecorder {
				return invokeCedarEpochRollback(t, f, data, target)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			target := tc.setup(t, f)
			gate := &cedarEpochLockGate{}
			data := f.scopedData(func(sc store.Scope) store.Scope {
				revisions, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				freshness, err := sc.Ext(policyFreshnessKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.authority = cedarEpochGatedAuthority{AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), gate: gate}
				out.ext = map[model.Kind]store.GenericRepo{
					revisionKind:        cedarEpochRejectReadBeforeLockRepo{GenericRepo: revisions, gate: gate, err: injected},
					policyFreshnessKind: cedarEpochRejectReadBeforeLockRepo{GenericRepo: freshness, gate: gate, err: injected},
				}
				return out
			})
			rec := tc.invoke(t, f, data, target)
			if rec.Code != http.StatusOK || !gate.locked {
				t.Fatalf("%s with lock-gated reads = status:%d locked:%t err:%v body:%s", tc.name, rec.Code, gate.locked, data.innerErr, rec.Body.String())
			}
			if errors.Is(data.innerErr, injected) {
				t.Fatalf("%s read dependent authority before the epoch lock", tc.name)
			}
		})
	}
}

func TestAuthoredCedarSameBytesPublishCreatesNewSelectionAndEpoch(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	source := `forbid(principal, action, resource);`
	first := invokeCedarEpochPublish(t, f, data, source)
	if first.Code != http.StatusOK {
		t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
	}
	firstRevision := cedarEpochResponseRevision(t, first)
	firstState, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || firstState.selection.authored != firstRevision {
		t.Fatalf("first publish state = loaded:%t %+v", loaded, firstState)
	}
	before := f.cedarAuthoritySnapshot(t)
	second := invokeCedarEpochPublish(t, f, data, source)
	if second.Code != http.StatusOK {
		t.Fatalf("same-byte publish = %d body=%s", second.Code, second.Body.String())
	}
	secondRevision := cedarEpochResponseRevision(t, second)
	if secondRevision == firstRevision {
		t.Fatalf("same-byte publish reused revision %d", secondRevision)
	}
	assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 1, 1, 1, 0, 1)
	secondState, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || secondState.selection.authored != secondRevision || secondState.generation.Version != firstState.generation.Version+1 {
		t.Fatalf("same-byte publish did not install new selected generation: first=%+v second=%+v", firstState, secondState)
	}
	var body map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveApplied {
		t.Fatalf("same-byte publish live activation = %v, want applied", body["live_activation"])
	}
}

func TestAuthoredCedarRollbackCurrentDoesNoAuthorityWritesButAuditsDeferred(t *testing.T) {
	f := newManagedEpochFixture(t)
	data := api.NewScopedData(f.st, f.tenant)
	published := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", published.Code, published.Body.String())
	}
	revision := cedarEpochResponseRevision(t, published)
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded {
		t.Fatal("publish did not install runtime state")
	}
	// Force only the runtime operational condition. The no-op rollback still has
	// zero authority writes, while its required compensatory deferral audit may
	// append after the no-op transaction.
	f.m.grants.markUnavailableIfStillSame(f.tenant, state, true)
	before := f.cedarAuthoritySnapshot(t)
	rec := invokeCedarEpochRollback(t, f, data, revision)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback current = %d body=%s", rec.Code, rec.Body.String())
	}
	after := f.cedarAuthoritySnapshot(t)
	assertCedarAuthorityDelta(t, before, after, 0, 0, 0, 0, 1)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveDeferred || !cedarEpochHasAuditAction(t, f, "governance.pdp.activation_deferred") {
		t.Fatalf("rollback-current deferred evidence = live:%v audit:%t body:%s", body["live_activation"], cedarEpochHasAuditAction(t, f, "governance.pdp.activation_deferred"), rec.Body.String())
	}
}

func TestAuthoredCedarRollbackCurrentExactIsTotalNoop(t *testing.T) {
	f := newManagedEpochFixture(t)
	plain := api.NewScopedData(f.st, f.tenant)
	published := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", published.Code, published.Body.String())
	}
	var trace []string
	data := cedarEpochOrderedData(t, f, time.Date(2026, 8, 16, 15, 16, 17, 0, time.UTC), &trace)
	before := f.cedarAuthoritySnapshot(t)
	rec := invokeCedarEpochRollback(t, f, data, cedarEpochResponseRevision(t, published))
	if rec.Code != http.StatusOK {
		t.Fatalf("exact rollback current = %d body=%s", rec.Code, rec.Body.String())
	}
	assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	requireCedarEpochTrace(t, trace, []string{"epoch-read", "authority-lock"})
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveApplied {
		t.Fatalf("exact rollback-current live activation = %v, want applied", body["live_activation"])
	}
}

func TestAuthoredCedarRollbackCurrentUsesLockedWitnessWithoutSecondRead(t *testing.T) {
	f := newManagedEpochFixture(t)
	plain := api.NewScopedData(f.st, f.tenant)
	published := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d body=%s", published.Code, published.Body.String())
	}
	var epochs *cedarEpochWrongSecondReadStore
	data := f.scopedData(func(sc store.Scope) store.Scope {
		epochs = &cedarEpochWrongSecondReadStore{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
		out := newManagedEpochScope(sc)
		out.epochs = epochs
		return out
	})
	before := f.cedarAuthoritySnapshot(t)
	rec := invokeCedarEpochRollback(t, f, data, cedarEpochResponseRevision(t, published))
	if rec.Code != http.StatusOK || data.innerErr != nil {
		t.Fatalf("rollback current with valid-looking second read = status:%d err:%v body:%s", rec.Code, data.innerErr, rec.Body.String())
	}
	if epochs == nil || epochs.reads != 1 || epochs.bumps != 0 {
		t.Fatalf("rollback-current epoch operations = %+v, want one lock read/no CAS", epochs)
	}
	assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["live_activation"] != liveApplied {
		t.Fatalf("rollback-current with valid-looking second read = %v, want applied", body["live_activation"])
	}
}

func TestAuthoredCedarMaxEpochAllowsCurrentNoopButRejectsMutation(t *testing.T) {
	t.Run("rollback current", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		plain := api.NewScopedData(f.st, f.tenant)
		published := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
		if published.Code != http.StatusOK {
			t.Fatalf("publish = %d body=%s", published.Code, published.Body.String())
		}
		max := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: math.MaxInt64}
		live, loaded := f.m.grants.tenantState(f.tenant)
		if !loaded {
			t.Fatal("publish did not install a live Cedar state")
		}
		live.generation = max
		if _, err := f.m.grants.installIfNotOlder(f.tenant, live); err != nil {
			t.Fatalf("install simulated Max runtime witness: %v", err)
		}
		viewEpochs := &cedarEpochFixedEpochStore{fact: max}
		f.m.UseData(cedarEpochModuleData{
			st: f.st,
			viewWrap: func(sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				out.epochs = viewEpochs
				return out
			},
		})
		mutateEpochs := &cedarEpochFixedEpochStore{fact: max}
		accepting := &cedarEpochAcceptingAuthority{}
		data := f.scopedData(func(sc store.Scope) store.Scope {
			out := newManagedEpochScope(sc)
			out.epochs = mutateEpochs
			out.authority = accepting
			return out
		})
		before := f.cedarAuthoritySnapshot(t)
		rec := invokeCedarEpochRollback(t, f, data, cedarEpochResponseRevision(t, published))
		if rec.Code != http.StatusOK || data.innerErr != nil {
			t.Fatalf("Max rollback-current = status:%d err:%v body:%s", rec.Code, data.innerErr, rec.Body.String())
		}
		if mutateEpochs.reads != 1 || mutateEpochs.bumps != 0 || len(accepting.locks) != 1 {
			t.Fatalf("Max rollback-current epoch operations = reads:%d bumps:%d locks:%v, want one lock/no bump", mutateEpochs.reads, mutateEpochs.bumps, accepting.locks)
		}
		assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["live_activation"] != liveApplied {
			t.Fatalf("Max rollback-current live activation = %v, want applied", body["live_activation"])
		}
	})

	t.Run("rollback non-current", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		plain := api.NewScopedData(f.st, f.tenant)
		first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
		if first.Code != http.StatusOK {
			t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
		}
		second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
		if second.Code != http.StatusOK {
			t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
		}
		max := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: math.MaxInt64}
		epochs := &cedarEpochFixedEpochStore{fact: max}
		accepting := &cedarEpochAcceptingAuthority{}
		data := f.scopedData(func(sc store.Scope) store.Scope {
			out := newManagedEpochScope(sc)
			out.epochs = epochs
			out.authority = accepting
			return out
		})
		before := f.cedarAuthoritySnapshot(t)
		rec := invokeCedarEpochRollback(t, f, data, cedarEpochResponseRevision(t, first))
		if rec.Code < http.StatusInternalServerError || data.innerErr == nil {
			t.Fatalf("Max rollback non-current = status:%d err:%v body:%s", rec.Code, data.innerErr, rec.Body.String())
		}
		if epochs.reads != 1 || epochs.bumps != 0 || len(accepting.locks) != 1 {
			t.Fatalf("Max rollback non-current epoch operations = reads:%d bumps:%d locks:%v, want one lock/no bump", epochs.reads, epochs.bumps, accepting.locks)
		}
		assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	})
}

func TestAuthoredCedarClockFailureIsZeroWrite(t *testing.T) {
	injected := errors.New("injected transaction clock failure")
	for _, tc := range []struct {
		name string
		wrap func(*testing.T, store.Scope) store.Scope
	}{
		{
			name: "missing clock capability",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				return &managedEpochNoClockScope{
					Scope: sc, epochs: sc.(store.AuthorizationEpochStore), authority: sc.(store.AuthoritySnapshotLocker),
				}
			},
		},
		{
			name: "clock error",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), err: injected}
				return out
			},
		},
		{
			name: "zero clock",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), fixed: true}
				return out
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			before := f.cedarAuthoritySnapshot(t)
			data := f.scopedData(func(sc store.Scope) store.Scope { return tc.wrap(t, sc) })
			rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
			if rec.Code < http.StatusInternalServerError || data.innerErr == nil {
				t.Fatalf("publish with %s = status:%d err:%v body:%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
		})
	}
}

func TestAuthoredCedarRollbackClockFailureIsZeroWrite(t *testing.T) {
	injected := errors.New("injected rollback transaction clock failure")
	for _, tc := range []struct {
		name string
		wrap func(*testing.T, store.Scope) store.Scope
	}{
		{
			name: "missing clock capability",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				return &managedEpochNoClockScope{
					Scope: sc, epochs: sc.(store.AuthorizationEpochStore), authority: sc.(store.AuthoritySnapshotLocker),
				}
			},
		},
		{
			name: "clock error",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), err: injected}
				return out
			},
		},
		{
			name: "zero clock",
			wrap: func(_ *testing.T, sc store.Scope) store.Scope {
				out := newManagedEpochScope(sc)
				out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), fixed: true}
				return out
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			plain := api.NewScopedData(f.st, f.tenant)
			first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
			if first.Code != http.StatusOK {
				t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
			}
			second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
			if second.Code != http.StatusOK {
				t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
			}
			before := f.cedarAuthoritySnapshot(t)
			data := f.scopedData(func(sc store.Scope) store.Scope { return tc.wrap(t, sc) })
			rec := invokeCedarEpochRollback(t, f, data, cedarEpochResponseRevision(t, first))
			if rec.Code < http.StatusInternalServerError || data.innerErr == nil {
				t.Fatalf("rollback with %s = status:%d err:%v body:%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
		})
	}
}

func TestAuthoredCedarPreservesSignedDDILAnchorWithoutClockRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		invoke func(*testing.T, *managedEpochFixture, api.ScopedData) *httptest.ResponseRecorder
		setup  func(*testing.T, *managedEpochFixture)
	}{
		{
			name:  "publish",
			setup: func(_ *testing.T, _ *managedEpochFixture) {},
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData) *httptest.ResponseRecorder {
				return invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
			},
		},
		{
			name: "rollback",
			setup: func(t *testing.T, f *managedEpochFixture) {
				plain := api.NewScopedData(f.st, f.tenant)
				first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
				if first.Code != http.StatusOK {
					t.Fatalf("first local publish = %d body=%s", first.Code, first.Body.String())
				}
				second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:read" };`)
				if second.Code != http.StatusOK {
					t.Fatalf("second local publish = %d body=%s", second.Code, second.Body.String())
				}
				// Carry the rollback target through a no-op temporary local selection:
				// the invoke closure below resolves revision one from durable history.
			},
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData) *httptest.ResponseRecorder {
				return invokeCedarEpochRollback(t, f, data, 1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			ddil := `forbid(principal, action, resource) when { context.permission == "agent:write" };`
			f.seedActivatedCedarSurface(t, surfaceCedarDDIL, ddil)
			anchor := time.Date(2026, 8, 15, 1, 2, 3, 456789000, time.UTC)
			f.seedFreshness(t, FreshnessRecord{
				RefreshedAt: anchor, MaxStaleness: time.Hour,
				AdoptedRevision: policyContentRevision([]byte(ddil)), AdoptedCreatedAt: anchor,
			})
			tc.setup(t, f)
			before, found := f.freshness(t)
			if !found {
				t.Fatal("seeded signed freshness is absent")
			}
			var freshnessWrites []string
			data := f.scopedData(func(sc store.Scope) store.Scope {
				freshness, err := sc.Ext(policyFreshnessKind)
				if err != nil {
					t.Fatal(err)
				}
				return &managedEpochNoClockScope{
					Scope: sc, epochs: sc.(store.AuthorizationEpochStore), authority: sc.(store.AuthoritySnapshotLocker),
					ext: map[model.Kind]store.GenericRepo{
						policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshness, label: "freshness", trace: &freshnessWrites},
					},
				}
			})
			rec := tc.invoke(t, f, data)
			if rec.Code != http.StatusOK {
				t.Fatalf("signed DDIL %s without TransactionClock = %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
			if len(freshnessWrites) != 0 {
				t.Fatalf("signed DDIL %s wrote local freshness: %v", tc.name, freshnessWrites)
			}
			after, found := f.freshness(t)
			if !found || after != before {
				t.Fatalf("signed DDIL %s changed anchor: before=%+v after=%+v", tc.name, before, after)
			}
		})
	}
}

func TestAuthoredCedarPostCASPublishFailuresRollBackEverything(t *testing.T) {
	injected := errors.New("injected post-CAS failure")
	for _, tc := range []struct {
		name string
		wrap func(*testing.T, store.Scope, *managedEpochCounter) store.Scope
	}{
		{
			name: "revision append",
			wrap: func(t *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				revisions, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.ext = map[model.Kind]store.GenericRepo{revisionKind: managedEpochFailingRepo{GenericRepo: revisions, err: injected}}
				return out
			},
		},
		{
			name: "activation append",
			wrap: func(t *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				revisions, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.ext = map[model.Kind]store.GenericRepo{revisionKind: &cedarEpochFailNthCreateRepo{GenericRepo: revisions, failAt: 2, err: injected}}
				return out
			},
		},
		{
			name: "freshness persist",
			wrap: func(t *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				freshness, err := sc.Ext(policyFreshnessKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.ext = map[model.Kind]store.GenericRepo{policyFreshnessKind: managedEpochFailingRepo{GenericRepo: freshness, err: injected}}
				return out
			},
		},
		{
			name: "publish audit",
			wrap: func(_ *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), err: injected}
				return out
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			var epochs *managedEpochCounter
			data := f.scopedData(func(sc store.Scope) store.Scope {
				epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
				return tc.wrap(t, sc, epochs)
			})
			before := f.cedarAuthoritySnapshot(t)
			rec := invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
			if rec.Code < http.StatusInternalServerError || !errors.Is(data.innerErr, injected) {
				t.Fatalf("publish post-CAS %s = status:%d err:%v body:%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			if epochs == nil || epochs.bumps != 1 {
				t.Fatalf("publish post-CAS %s epoch bumps=%+v, want one attempted CAS", tc.name, epochs)
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
		})
	}
}

func TestAuthoredCedarUsesCASWitnessInsteadOfPostCASRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *managedEpochFixture) int64
		invoke func(*testing.T, *managedEpochFixture, api.ScopedData, int64) *httptest.ResponseRecorder
	}{
		{
			name:  "publish",
			setup: func(_ *testing.T, _ *managedEpochFixture) int64 { return 0 },
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData, _ int64) *httptest.ResponseRecorder {
				return invokeCedarEpochPublish(t, f, data, `forbid(principal, action, resource);`)
			},
		},
		{
			name: "rollback",
			setup: func(t *testing.T, f *managedEpochFixture) int64 {
				plain := api.NewScopedData(f.st, f.tenant)
				first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
				if first.Code != http.StatusOK {
					t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
				}
				second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
				if second.Code != http.StatusOK {
					t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
				}
				return cedarEpochResponseRevision(t, first)
			},
			invoke: func(t *testing.T, f *managedEpochFixture, data api.ScopedData, target int64) *httptest.ResponseRecorder {
				return invokeCedarEpochRollback(t, f, data, target)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			target := tc.setup(t, f)
			var epochs *cedarEpochWrongSecondReadStore
			data := f.scopedData(func(sc store.Scope) store.Scope {
				epochs = &cedarEpochWrongSecondReadStore{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				return out
			})
			before := f.cedarAuthoritySnapshot(t)
			rec := tc.invoke(t, f, data, target)
			if rec.Code != http.StatusOK || data.innerErr != nil {
				t.Fatalf("%s with wrong valid post-CAS read = status:%d err:%v body:%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			if epochs == nil || epochs.reads != 1 || epochs.bumps != 1 {
				t.Fatalf("%s epoch read/bump = %+v, want one locked read and one bump", tc.name, epochs)
			}
			wantLocked := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: before.epoch}
			if epochs.bumpExpected != wantLocked {
				t.Fatalf("%s CAS expected = %+v, want locked witness %+v", tc.name, epochs.bumpExpected, wantLocked)
			}
			after := f.cedarAuthoritySnapshot(t)
			if after.epoch != before.epoch+1 {
				t.Fatalf("%s durable CAS epoch = %d, want %d", tc.name, after.epoch, before.epoch+1)
			}
			state, loaded := f.m.grants.tenantState(f.tenant)
			if !loaded || state.generation.Version != after.epoch || !state.available {
				t.Fatalf("%s runtime did not bind the CAS G+1 witness: loaded:%t state:%+v durable:%d", tc.name, loaded, state, after.epoch)
			}
			action := "governance.pdp.publish"
			if tc.name == "rollback" {
				action = "governance.pdp.rollback"
			}
			_, meta := cedarEpochCanonicalAudit(t, f, action)
			if meta["authorization_epoch"] != float64(after.epoch) {
				t.Fatalf("%s audit used post-CAS decorated witness: meta=%v durable=%d", tc.name, meta, after.epoch)
			}
		})
	}
}

func TestAuthoredCedarPostCASRollbackFailuresRollBackEverything(t *testing.T) {
	injected := errors.New("injected rollback post-CAS failure")
	for _, tc := range []struct {
		name string
		wrap func(*testing.T, store.Scope, *managedEpochCounter) store.Scope
	}{
		{
			name: "activation append",
			wrap: func(t *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				revisions, err := sc.Ext(revisionKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.ext = map[model.Kind]store.GenericRepo{revisionKind: managedEpochFailingRepo{GenericRepo: revisions, err: injected}}
				return out
			},
		},
		{
			name: "freshness persist",
			wrap: func(t *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				freshness, err := sc.Ext(policyFreshnessKind)
				if err != nil {
					t.Fatal(err)
				}
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.ext = map[model.Kind]store.GenericRepo{policyFreshnessKind: managedEpochFailingRepo{GenericRepo: freshness, err: injected}}
				return out
			},
		},
		{
			name: "rollback audit",
			wrap: func(_ *testing.T, sc store.Scope, epochs *managedEpochCounter) store.Scope {
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), err: injected}
				return out
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			plain := api.NewScopedData(f.st, f.tenant)
			first := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource);`)
			if first.Code != http.StatusOK {
				t.Fatalf("first publish = %d body=%s", first.Code, first.Body.String())
			}
			target := cedarEpochResponseRevision(t, first)
			second := invokeCedarEpochPublish(t, f, plain, `forbid(principal, action, resource) when { context.permission == "agent:write" };`)
			if second.Code != http.StatusOK {
				t.Fatalf("second publish = %d body=%s", second.Code, second.Body.String())
			}
			current := cedarEpochResponseRevision(t, second)
			var epochs *managedEpochCounter
			data := f.scopedData(func(sc store.Scope) store.Scope {
				epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
				return tc.wrap(t, sc, epochs)
			})
			before := f.cedarAuthoritySnapshot(t)
			rec := invokeCedarEpochRollback(t, f, data, target)
			if rec.Code < http.StatusInternalServerError || !errors.Is(data.innerErr, injected) {
				t.Fatalf("rollback post-CAS %s = status:%d err:%v body:%s", tc.name, rec.Code, data.innerErr, rec.Body.String())
			}
			if epochs == nil || epochs.bumps != 1 {
				t.Fatalf("rollback post-CAS %s epoch bumps=%+v, want one attempted CAS", tc.name, epochs)
			}
			assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
			var selected int64
			if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
				var err error
				selected, _, err = activeRevisionNumber(context.Background(), sc, surfaceCedar)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if selected != current {
				t.Fatalf("failed rollback moved selection from %d to %d", current, selected)
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

func TestBackfillOlderInvalidSnapshotDoesNotPoisonNewerRuntime(t *testing.T) {
	f := newManagedEpochFixture(t)
	plain := api.NewScopedData(f.st, f.tenant)
	for _, source := range []string{
		`forbid(principal, action, resource);`,
		`forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	} {
		if rec := invokeCedarEpochPublish(t, f, plain, source); rec.Code != http.StatusOK {
			t.Fatalf("seed publish = %d body=%s", rec.Code, rec.Body.String())
		}
	}
	liveBefore, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !liveBefore.available || liveBefore.generation.Version < 2 || !hasCedarCompiledBinding(liveBefore) {
		t.Fatalf("seeded G+1 runtime is not usable: loaded:%t %+v", loaded, liveBefore)
	}
	// Turn the durable side into a malformed bounded legacy snapshot without an
	// epoch bump. The decorator presents it as G below the installed G+1 and
	// accepts the paired lock, isolating C3's caller-side lower-G fence.
	f.m.grants.maxStaleness = time.Hour
	f.mutate(t, func(sc store.Scope) error {
		freshness, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			return err
		}
		rows, err := listAll(context.Background(), freshness)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := freshness.Delete(context.Background(), model.ID(row.String(model.ColID))); err != nil {
				return err
			}
		}
		number, _, err := appendRevision(context.Background(), sc, surfaceCedar, `this is not valid Cedar`, "seed", true, true, "")
		if err != nil {
			return err
		}
		_, err = activateRevision(context.Background(), sc, surfaceCedar, number, "seed")
		return err
	})
	before := f.cedarAuthoritySnapshot(t)
	older := liveBefore.generation
	older.Version--
	accepting := &cedarEpochAcceptingAuthority{}
	f.m.UseData(cedarEpochModuleData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			out := newManagedEpochScope(sc)
			out.epochs = &managedEpochScriptedStore{fact: older}
			out.authority = accepting
			return out
		},
	})
	if err := f.m.ReloadActivePDP(context.Background(), f.tenant); err != nil {
		t.Fatalf("bounded lower invalid snapshot did not become no-op: %v", err)
	}
	if len(accepting.locks) != 1 || len(accepting.locks[0]) != 1 || accepting.locks[0][0] != older {
		t.Fatalf("backfill did not lock its decorated lower witness: %+v", accepting.locks)
	}
	assertCedarAuthorityDelta(t, before, f.cedarAuthoritySnapshot(t), 0, 0, 0, 0, 0)
	liveAfter, afterLoaded := f.m.grants.tenantState(f.tenant)
	if !afterLoaded || !liveAfter.available || liveAfter.generation != liveBefore.generation || liveAfter.operation != liveBefore.operation || !hasCedarCompiledBinding(liveAfter) {
		t.Fatalf("bounded lower invalid reload poisoned G+1: before=%+v after=%+v", liveBefore, liveAfter)
	}
}

// TestCedarBackfillPostLockFailureDominatesDelayedReplay proves that a legacy
// bounded-policy backfill cannot be demoted to a transient failure after it has locked
// G. The failed transaction rolls back its attempted G+1, but a delayed exact-G replay
// does not establish the missing freshness anchor or the required audit evidence.
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

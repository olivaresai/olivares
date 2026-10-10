// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type managedEpochFixture struct {
	m      *Module
	st     store.Store
	tenant model.TenantID
	actor  auth.Principal
	// standing is the request's standing port. Grant and policy writers read the
	// standing of every account they name through it before their transaction,
	// and a request with no port refuses them.
	standing auth.StandingReader
}

func newManagedEpochFixture(t *testing.T) *managedEpochFixture {
	t.Helper()
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Managed epoch", Slug: "managed-epoch", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &managedEpochFixture{
		m:        m,
		st:       st,
		tenant:   tenant,
		actor:    auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true},
		standing: auth.NewAuthenticator(st, nil),
	}
}

type managedEpochScopedData struct {
	st       store.Store
	tenant   model.TenantID
	wrap     func(store.Scope) store.Scope
	innerErr error
	views    int
	mutates  int
}

func (d *managedEpochScopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	d.views++
	return d.st.View(ctx, d.tenant, fn)
}

func (d *managedEpochScopedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	d.mutates++
	return d.st.Mutate(ctx, d.tenant, func(sc store.Scope) error {
		if d.wrap != nil {
			sc = d.wrap(sc)
		}
		d.innerErr = fn(sc)
		return d.innerErr
	})
}

func (d *managedEpochScopedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

type managedEpochScope struct {
	store.Scope
	epochs    store.AuthorizationEpochStore
	authority store.AuthoritySnapshotLocker
	clock     store.TransactionClock
	audit     store.AuditLog
	ext       map[model.Kind]store.GenericRepo
}

func newManagedEpochScope(sc store.Scope) *managedEpochScope {
	return &managedEpochScope{
		Scope: sc, epochs: sc.(store.AuthorizationEpochStore),
		authority: sc.(store.AuthoritySnapshotLocker), clock: sc.(store.TransactionClock),
	}
}

func (s *managedEpochScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	return s.epochs.ReadAuthorizationEpoch(ctx)
}

func (s *managedEpochScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	return s.epochs.BumpAuthorizationEpoch(ctx, expected)
}

func (s *managedEpochScope) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *managedEpochScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

type managedEpochNoClockScope struct {
	store.Scope
	epochs    store.AuthorizationEpochStore
	authority store.AuthoritySnapshotLocker
	ext       map[model.Kind]store.GenericRepo
}

func (s *managedEpochNoClockScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	return s.epochs.ReadAuthorizationEpoch(ctx)
}

func (s *managedEpochNoClockScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	return s.epochs.BumpAuthorizationEpoch(ctx, expected)
}

func (s *managedEpochNoClockScope) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	return s.authority.LockAuthoritySnapshot(ctx, refs)
}

func (s *managedEpochNoClockScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if repo := s.ext[kind]; repo != nil {
		return repo, nil
	}
	return s.Scope.Ext(kind)
}

type managedEpochClock struct {
	store.TransactionClock
	now   model.Timestamp
	err   error
	fixed bool
	calls int
	trace *[]string
}

func (c *managedEpochClock) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	c.calls++
	if c.trace != nil {
		*c.trace = append(*c.trace, "db-now")
	}
	if c.err != nil || c.fixed {
		return c.now, c.err
	}
	return c.TransactionClock.TransactionNow(ctx)
}

func (s *managedEpochScope) Audit() store.AuditLog {
	if s.audit != nil {
		return s.audit
	}
	return s.Scope.Audit()
}

func (s *managedEpochScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	if repo := s.ext[kind]; repo != nil {
		return repo, nil
	}
	return s.Scope.Ext(kind)
}

type managedEpochCounter struct {
	store.AuthorizationEpochStore
	reads int
	bumps int
	trace *[]string
}

func (s *managedEpochCounter) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	if s.trace != nil {
		*s.trace = append(*s.trace, "epoch-read")
	}
	return s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
}

func (s *managedEpochCounter) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	if s.trace != nil {
		*s.trace = append(*s.trace, "epoch-bump")
	}
	return s.AuthorizationEpochStore.BumpAuthorizationEpoch(ctx, expected)
}

type managedEpochReaderOnlyScope struct {
	store.Scope
	reader store.AuthorizationEpochReader
	reads  *int
}

func (s *managedEpochReaderOnlyScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	*s.reads++
	return s.reader.ReadAuthorizationEpoch(ctx)
}

type managedEpochBumperOnlyScope struct {
	store.Scope
	bumper store.AuthorizationEpochBumper
	bumps  *int
}

func (s *managedEpochBumperOnlyScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	*s.bumps++
	return s.bumper.BumpAuthorizationEpoch(ctx, expected)
}

type managedEpochScriptedStore struct {
	fact    store.AuthorizationFactRef
	readErr error
	bumpErr error
	reads   int
	bumps   int
}

func (s *managedEpochScriptedStore) ReadAuthorizationEpoch(context.Context) (store.AuthorizationFactRef, error) {
	s.reads++
	return s.fact, s.readErr
}

func (s *managedEpochScriptedStore) BumpAuthorizationEpoch(_ context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	s.bumps++
	if s.bumpErr != nil {
		return store.AuthorizationFactRef{}, s.bumpErr
	}
	next := expected
	next.Version++
	return next, nil
}

type managedEpochRecordingRepo struct {
	store.GenericRepo
	label string
	trace *[]string
}

type managedEpochFailingRepo struct {
	store.GenericRepo
	err error
}

func (r managedEpochFailingRepo) Create(context.Context, model.Record) (model.Record, error) {
	return nil, r.err
}

func (r managedEpochFailingRepo) Update(context.Context, model.Record) (model.Record, error) {
	return nil, r.err
}

func (r managedEpochRecordingRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	*r.trace = append(*r.trace, r.label+"-create")
	return r.GenericRepo.Create(ctx, rec)
}

func (r managedEpochRecordingRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	*r.trace = append(*r.trace, r.label+"-update")
	return r.GenericRepo.Update(ctx, rec)
}

func (r managedEpochRecordingRepo) Delete(ctx context.Context, id model.ID) error {
	*r.trace = append(*r.trace, r.label+"-delete")
	return r.GenericRepo.Delete(ctx, id)
}

type managedEpochRecordingAudit struct {
	store.AuditLog
	trace *[]string
	err   error
}

func (a managedEpochRecordingAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if a.trace != nil {
		*a.trace = append(*a.trace, "audit-append")
	}
	if a.err != nil {
		return model.AuditEvent{}, a.err
	}
	return a.AuditLog.Append(ctx, draft)
}

type managedEpochRecordingAuthority struct {
	store.AuthoritySnapshotLocker
	trace *[]string
}

type managedEpochInjectingAuthority struct {
	store.AuthoritySnapshotLocker
	inject func() error
	calls  int
}

func (a *managedEpochInjectingAuthority) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	a.calls++
	if err := a.inject(); err != nil {
		return err
	}
	return a.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, refs)
}

func (a managedEpochRecordingAuthority) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	*a.trace = append(*a.trace, "authority-lock")
	return a.AuthoritySnapshotLocker.LockAuthoritySnapshot(ctx, refs)
}

func (f *managedEpochFixture) scopedData(wrap func(store.Scope) store.Scope) *managedEpochScopedData {
	return &managedEpochScopedData{st: f.st, tenant: f.tenant, wrap: wrap}
}

func (f *managedEpochFixture) moduleContext(data api.ScopedData) api.ModuleContext {
	return api.ModuleContext{Tenant: f.tenant, Principal: f.actor, Data: data, Standing: f.standing}
}

func managedEpochRequest(t *testing.T, method, path string, body any, param, value string) *http.Request {
	t.Helper()
	var source string
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		source = string(encoded)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(source))
	if param != "" {
		route := chi.NewRouteContext()
		route.URLParams.Add(param, value)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	}
	return req
}

func (f *managedEpochFixture) updateRole(t *testing.T, data api.ScopedData, name string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleUpdateCustomRole(rec, managedEpochRequest(t, http.MethodPut, "/roles/"+name, body, "name", name), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) createRole(t *testing.T, data api.ScopedData, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleCreateCustomRole(rec, managedEpochRequest(t, http.MethodPost, "/roles", body, "", ""), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) updateGroup(t *testing.T, data api.ScopedData, name string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleUpdatePermGroup(rec, managedEpochRequest(t, http.MethodPut, "/permission-groups/"+name, body, "name", name), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) createGroup(t *testing.T, data api.ScopedData, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleCreatePermGroup(rec, managedEpochRequest(t, http.MethodPost, "/permission-groups", body, "", ""), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) deleteRole(t *testing.T, data api.ScopedData, name string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleDeleteCustomRole(rec, managedEpochRequest(t, http.MethodDelete, "/roles/"+name, nil, "name", name), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) deleteGroup(t *testing.T, data api.ScopedData, name string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleDeletePermGroup(rec, managedEpochRequest(t, http.MethodDelete, "/permission-groups/"+name, nil, "name", name), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) createGrant(t *testing.T, data api.ScopedData, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleCreateScopedGrant(rec, managedEpochRequest(t, http.MethodPost, "/grants", body, "", ""), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) revokeGrant(t *testing.T, data api.ScopedData, id model.ID) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.m.handleRevokeScopedGrant(rec, managedEpochRequest(t, http.MethodDelete, "/grants/"+id.String(), nil, "id", id.String()), f.moduleContext(data))
	return rec
}

func (f *managedEpochFixture) mutate(t *testing.T, fn func(store.Scope) error) {
	t.Helper()
	if err := f.st.Mutate(context.Background(), f.tenant, fn); err != nil {
		t.Fatal(err)
	}
}

func (f *managedEpochFixture) seedRole(t *testing.T, role customRole) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		repo, err := sc.Ext(customRoleKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), roleRecord(role.Name, role.DisplayName, role.Description, role.Base, role.Perms, role.Groups, role.Excludes, "seed"))
		return err
	})
}

func (f *managedEpochFixture) seedGroup(t *testing.T, group permGroup) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		repo, err := sc.Ext(permGroupKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), groupRecord(group.Name, group.DisplayName, group.Description, group.Perms, "seed"))
		return err
	})
}

func (f *managedEpochFixture) seedGrant(t *testing.T, grant scopedGrant) model.ID {
	t.Helper()
	var id model.ID
	f.mutate(t, func(sc store.Scope) error {
		repo, err := sc.Ext(scopedGrantKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(context.Background(), grantRecord(grant, "seed"))
		if err == nil {
			id = model.ID(rec.String(model.ColID))
		}
		return err
	})
	return id
}

func (f *managedEpochFixture) seedManagedRevision(t *testing.T) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		state, err := loadManagedProjectionState(context.Background(), sc)
		if err != nil {
			return err
		}
		_, _, err = appendRevision(context.Background(), sc, surfaceCedarManaged, projectManagedCedar(state.grants, state.roles, state.groups), "seed", true, true, "")
		return err
	})
}

func (f *managedEpochFixture) seedInvalidSurface(t *testing.T, surface string) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		_, _, err := appendRevision(context.Background(), sc, surface, "this is not Cedar", "seed", false, true, "")
		return err
	})
}

func (f *managedEpochFixture) seedSurface(t *testing.T, surface, content string) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		_, _, err := appendRevision(context.Background(), sc, surface, content, "seed", true, true, "")
		return err
	})
}

func (f *managedEpochFixture) seedFreshness(t *testing.T, in FreshnessRecord) {
	t.Helper()
	f.mutate(t, func(sc store.Scope) error {
		return upsertPolicyFreshness(context.Background(), sc, in)
	})
}

func (f *managedEpochFixture) freshness(t *testing.T) (FreshnessRecord, bool) {
	t.Helper()
	var out FreshnessRecord
	var found bool
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		out, found, err = readPolicyFreshness(context.Background(), sc)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out, found
}

func (f *managedEpochFixture) seedCachedFreshness(t *testing.T, refreshedAt time.Time, bound time.Duration) {
	t.Helper()
	f.m.grants.mu.Lock()
	defer f.m.grants.mu.Unlock()
	if f.m.grants.tenants == nil {
		f.m.grants.tenants = map[model.TenantID]scopedTenantState{}
	}
	state := f.m.grants.tenants[f.tenant]
	state.freshness = FreshnessRecord{RefreshedAt: refreshedAt, MaxStaleness: bound}
	state.freshnessValid = !refreshedAt.IsZero()
	state.available = true
	state.operation = nextScopedStateOperation()
	f.m.grants.tenants[f.tenant] = state
}

func (f *managedEpochFixture) assertCachedFreshness(t *testing.T, refreshedAt time.Time, bound time.Duration) {
	t.Helper()
	state, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !state.freshness.RefreshedAt.Equal(refreshedAt) || state.freshness.MaxStaleness != bound {
		t.Fatalf("cached atomic freshness/bound = loaded:%t %s/%s, want %s/%s",
			loaded, state.freshness.RefreshedAt, state.freshness.MaxStaleness, refreshedAt, bound)
	}
}

type managedEpochSnapshot struct {
	epoch     int64
	revisions int
	auditSeq  int64
	roles     int
	groups    int
	grants    int
}

func (f *managedEpochFixture) snapshot(t *testing.T) managedEpochSnapshot {
	t.Helper()
	var out managedEpochSnapshot
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
		revs, err := listAll(context.Background(), repo, eq(colRevSurface, surfaceCedarManaged))
		if err != nil {
			return err
		}
		out.revisions = len(revs)
		if head, ok, err := sc.Audit().Head(context.Background()); err != nil {
			return err
		} else if ok {
			out.auditSeq = head.Seq
		}
		for kind, target := range map[model.Kind]*int{
			customRoleKind:  &out.roles,
			permGroupKind:   &out.groups,
			scopedGrantKind: &out.grants,
		} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			rows, err := listAll(context.Background(), repo)
			if err != nil {
				return err
			}
			*target = len(rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertManagedEpochDelta(t *testing.T, before, after managedEpochSnapshot, epoch, revision, audit, role, group, grant int64) {
	t.Helper()
	got := []int64{
		after.epoch - before.epoch,
		int64(after.revisions - before.revisions),
		after.auditSeq - before.auditSeq,
		int64(after.roles - before.roles),
		int64(after.groups - before.groups),
		int64(after.grants - before.grants),
	}
	want := []int64{epoch, revision, audit, role, group, grant}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta epoch/revision/audit/role/group/grant = %v, want %v", got, want)
		}
	}
}

func managedEpochCountedData(f *managedEpochFixture, counter **managedEpochCounter) *managedEpochScopedData {
	return f.scopedData(func(sc store.Scope) store.Scope {
		c := &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
		*counter = c
		out := newManagedEpochScope(sc)
		out.epochs = c
		return out
	})
}

// managedEpochObservedScope copies call counts out of a scripted epoch port after each
// method. It embeds a full-capability scope, so the production type assertion still sees
// the exact combined capability rather than a reader-only/bumper-only lookalike.
type managedEpochObservedScope struct {
	*managedEpochScope
	scripted *managedEpochScriptedStore
	reads    *int
	bumps    *int
}

func (s *managedEpochObservedScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	fact, err := s.scripted.ReadAuthorizationEpoch(ctx)
	*s.reads = s.scripted.reads
	return fact, err
}

func (s *managedEpochObservedScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	fact, err := s.scripted.BumpAuthorizationEpoch(ctx, expected)
	*s.bumps = s.scripted.bumps
	return fact, err
}

func managedEpochOrderedData(t *testing.T, f *managedEpochFixture, kind model.Kind, label string, trace *[]string) *managedEpochScopedData {
	t.Helper()
	return f.scopedData(func(sc store.Scope) store.Scope {
		counter := &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore), trace: trace}
		targetRepo, err := sc.Ext(kind)
		if err != nil {
			t.Fatal(err)
		}
		revisionRepo, err := sc.Ext(revisionKind)
		if err != nil {
			t.Fatal(err)
		}
		freshnessRepo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			t.Fatal(err)
		}
		out := newManagedEpochScope(sc)
		out.epochs = counter
		out.authority = managedEpochRecordingAuthority{AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker), trace: trace}
		out.clock = &managedEpochClock{TransactionClock: sc.(store.TransactionClock), trace: trace}
		out.ext = map[model.Kind]store.GenericRepo{
			kind:                managedEpochRecordingRepo{GenericRepo: targetRepo, label: label, trace: trace},
			revisionKind:        managedEpochRecordingRepo{GenericRepo: revisionRepo, label: "revision", trace: trace},
			policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshnessRepo, label: "freshness", trace: trace},
		}
		out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), trace: trace}
		return out
	})
}

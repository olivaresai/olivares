// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
		m:      m,
		st:     st,
		tenant: tenant,
		actor:  auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), Superadmin: true},
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
	return api.ModuleContext{Tenant: f.tenant, Principal: f.actor, Data: data}
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

func TestManagedProjectionMutationEpochMatrix(t *testing.T) {
	type testCase struct {
		name      string
		setup     func(*testing.T, *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder
		status    int
		bump      int
		rowDeltas [3]int64
	}
	cases := []testCase{
		{
			name: "assigned role changes projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
				f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateRole(t, data, "operator", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
			status: http.StatusOK, bump: 1,
		},
		{
			name: "assigned role metadata preserves projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
				f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateRole(t, data, "operator", map[string]any{"display_name": "Operator", "permissions": []string{"agent:read"}})
				}
			},
			status: http.StatusOK,
		},
		{
			name: "unreferenced role permissions preserve projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateRole(t, data, "operator", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
			status: http.StatusOK,
		},
		{
			name: "referenced permission group changes projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
				f.seedRole(t, customRole{Name: "operator", Groups: []string{"agent-access"}})
				f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateGroup(t, data, "agent-access", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
			status: http.StatusOK, bump: 1,
		},
		{
			name: "unreferenced permission group preserves projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateGroup(t, data, "agent-access", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
			status: http.StatusOK,
		},
		{
			name: "grant create changes projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.createGrant(t, data, map[string]any{
						"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
					})
				}
			},
			status: http.StatusCreated, bump: 1, rowDeltas: [3]int64{0, 0, 1},
		},
		{
			name: "grant revoke changes projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				id := f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder { return f.revokeGrant(t, data, id) }
			},
			status: http.StatusNoContent, bump: 1, rowDeltas: [3]int64{0, 0, -1},
		},
		{
			name: "legacy inert grant revoke preserves projection",
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				id := f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "missing", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				return func(data api.ScopedData) *httptest.ResponseRecorder { return f.revokeGrant(t, data, id) }
			},
			status: http.StatusNoContent, rowDeltas: [3]int64{0, 0, -1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			invoke := tc.setup(t, f)
			before := f.snapshot(t)
			var counter *managedEpochCounter
			data := managedEpochCountedData(f, &counter)
			response := invoke(data)
			if response.Code != tc.status {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), tc.status)
			}
			if data.mutates != 1 || data.views != 0 {
				t.Fatalf("handler data calls mutate/view = %d/%d, want 1/0", data.mutates, data.views)
			}
			wantReads := 1 + tc.bump // lock read, plus advance's defensive re-read when changed
			if counter == nil || counter.bumps != tc.bump || counter.reads != wantReads {
				t.Fatalf("epoch read/bump calls = %d/%d, want %d/%d", counter.reads, counter.bumps, wantReads, tc.bump)
			}
			after := f.snapshot(t)
			assertManagedEpochDelta(t, before, after, int64(tc.bump), int64(tc.bump), 1, tc.rowDeltas[0], tc.rowDeltas[1], tc.rowDeltas[2])
		})
	}
}

func TestManagedProjectionDefinitionCreatesRemainZeroEpoch(t *testing.T) {
	t.Run("permission group create", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.createGroup(t, managedEpochCountedData(f, &counter), map[string]any{
			"name": "agent-access", "permissions": []string{"agent:read"},
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if counter.reads != 0 || counter.bumps != 0 {
			t.Fatalf("group create epoch read/bump = %d/%d, want zero", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 1, 0, 1, 0)
	})

	t.Run("role create including group locks but does not bump", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.createRole(t, managedEpochCountedData(f, &counter), map[string]any{
			"name": "operator", "permissions": []string{}, "groups": []string{"agent-access"},
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if counter.reads != 1 || counter.bumps != 0 {
			t.Fatalf("role create epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 1, 1, 0, 0)
	})
}

func TestManagedProjectionNoopAndReplayPerformZeroWrites(t *testing.T) {
	anchor := time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
	bound := 24 * time.Hour
	t.Run("exact role update", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedRole(t, customRole{Name: "operator", DisplayName: "Operator", Perms: []string{"agent:read"}})
		f.seedCachedFreshness(t, anchor, bound)
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.updateRole(t, managedEpochCountedData(f, &counter), "operator", map[string]any{
			"display_name": "Operator", "permissions": []string{"agent:read"},
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if counter.bumps != 0 || counter.reads != 1 {
			t.Fatalf("no-op epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
		f.assertCachedFreshness(t, anchor, bound)
	})

	t.Run("exact permission group update", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedGroup(t, permGroup{Name: "agent-access", DisplayName: "Agent access", Perms: []string{"agent:read"}})
		f.seedCachedFreshness(t, anchor, bound)
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.updateGroup(t, managedEpochCountedData(f, &counter), "agent-access", map[string]any{
			"display_name": "Agent access", "permissions": []string{"agent:read"},
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if counter.bumps != 0 || counter.reads != 1 {
			t.Fatalf("no-op epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
		f.assertCachedFreshness(t, anchor, bound)
	})

	t.Run("duplicate grant replay", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		subject := model.NewID().String()
		f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: subject, Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeTenant}})
		f.seedManagedRevision(t)
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.createGrant(t, managedEpochCountedData(f, &counter), map[string]any{
			"subject_kind": subjectUser, "subject_ref": subject, "role": auth.RoleEditor, "scope_tree": scopeTenant,
		})
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d body=%s, want 409", response.Code, response.Body.String())
		}
		if counter.bumps != 0 || counter.reads != 1 {
			t.Fatalf("replay epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	})

	t.Run("repeated revoke", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		id := f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeTenant}})
		f.seedManagedRevision(t)
		if first := f.revokeGrant(t, f.scopedData(nil), id); first.Code != http.StatusNoContent {
			t.Fatalf("first revoke status = %d body=%s", first.Code, first.Body.String())
		}
		before := f.snapshot(t)
		var counter *managedEpochCounter
		response := f.revokeGrant(t, managedEpochCountedData(f, &counter), id)
		if response.Code != http.StatusNotFound {
			t.Fatalf("replay status = %d body=%s, want 404", response.Code, response.Body.String())
		}
		if counter.bumps != 0 || counter.reads != 1 {
			t.Fatalf("replay epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	})
}

func TestManagedProjectionEpochCapabilityFailuresAbortBeforeWrites(t *testing.T) {
	type testCase struct {
		name      string
		wrap      func(store.Scope, store.AuthorizationFactRef, *int, *int) store.Scope
		wantReads int
		wantBumps int
	}
	cases := []testCase{
		{name: "absent", wrap: func(sc store.Scope, _ store.AuthorizationFactRef, _, _ *int) store.Scope {
			return struct{ store.Scope }{sc}
		}},
		{name: "reader only", wrap: func(sc store.Scope, _ store.AuthorizationFactRef, reads, _ *int) store.Scope {
			return &managedEpochReaderOnlyScope{Scope: sc, reader: sc.(store.AuthorizationEpochReader), reads: reads}
		}},
		{name: "bumper only", wrap: func(sc store.Scope, _ store.AuthorizationFactRef, _, bumps *int) store.Scope {
			return &managedEpochBumperOnlyScope{Scope: sc, bumper: sc.(store.AuthorizationEpochBumper), bumps: bumps}
		}},
		{name: "stale snapshot lock", wantReads: 1, wrap: func(sc store.Scope, fact store.AuthorizationFactRef, reads, bumps *int) store.Scope {
			fact.Version++
			scripted := &managedEpochScriptedStore{fact: fact}
			out := newManagedEpochScope(sc)
			out.epochs = scripted
			return &managedEpochObservedScope{managedEpochScope: out, scripted: scripted, reads: reads, bumps: bumps}
		}},
		{name: "stale CAS", wantReads: 2, wantBumps: 1, wrap: func(sc store.Scope, fact store.AuthorizationFactRef, reads, bumps *int) store.Scope {
			scripted := &managedEpochScriptedStore{fact: fact, bumpErr: store.ErrAuthorizationEpochUnavailable}
			out := newManagedEpochScope(sc)
			out.epochs = scripted
			return &managedEpochObservedScope{managedEpochScope: out, scripted: scripted, reads: reads, bumps: bumps}
		}},
		{name: "exhausted", wantReads: 1, wrap: func(sc store.Scope, fact store.AuthorizationFactRef, reads, bumps *int) store.Scope {
			fact.Version = math.MaxInt64
			scripted := &managedEpochScriptedStore{fact: fact}
			out := newManagedEpochScope(sc)
			out.epochs = scripted
			return &managedEpochObservedScope{managedEpochScope: out, scripted: scripted, reads: reads, bumps: bumps}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			before := f.snapshot(t)
			fact := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(f.tenant), Version: before.epoch}
			var reads, bumps int
			data := f.scopedData(func(sc store.Scope) store.Scope { return tc.wrap(sc, fact, &reads, &bumps) })
			response := f.createGrant(t, data, map[string]any{
				"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if response.Code < http.StatusInternalServerError {
				t.Fatalf("status = %d body=%s, want fail-closed", response.Code, response.Body.String())
			}
			if !errors.Is(data.innerErr, store.ErrAuthorizationEpochUnavailable) {
				t.Fatalf("inner error = %v, want ErrAuthorizationEpochUnavailable", data.innerErr)
			}
			if reads != tc.wantReads || bumps != tc.wantBumps {
				t.Fatalf("epoch read/bump = %d/%d, want %d/%d", reads, bumps, tc.wantReads, tc.wantBumps)
			}
			assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
		})
	}
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

func TestManagedProjectionFailureAfterBumpRollsBackEverything(t *testing.T) {
	f := newManagedEpochFixture(t)
	before := f.snapshot(t)
	injected := errors.New("injected managed audit failure")
	var counter *managedEpochCounter
	data := f.scopedData(func(sc store.Scope) store.Scope {
		counter = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
		out := newManagedEpochScope(sc)
		out.epochs = counter
		out.audit = managedEpochRecordingAudit{AuditLog: sc.Audit(), err: injected}
		return out
	})
	response := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	})
	if response.Code < http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want failure", response.Code, response.Body.String())
	}
	if !errors.Is(data.innerErr, injected) {
		t.Fatalf("inner error = %v, want injected failure", data.innerErr)
	}
	if counter == nil || counter.reads != 2 || counter.bumps != 1 {
		t.Fatalf("epoch read/bump = %d/%d, want exactly once", counter.reads, counter.bumps)
	}
	assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	if _, found := f.freshness(t); found {
		t.Fatal("audit rollback left a freshness row behind")
	}
}

func TestManagedProjectionInvalidAuthoredOrAdoptedUnionPerformsZeroWrites(t *testing.T) {
	for _, surface := range []string{surfaceCedar, surfaceCedarDDIL} {
		t.Run(surface, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			f.seedInvalidSurface(t, surface)
			if surface == surfaceCedarDDIL {
				// Keep the signed-adoption state internally coherent even though the
				// selected policy bytes are invalid. Otherwise a mutant that omits DDIL
				// from the prospective union still fails later on partial-state validation
				// and this test cannot prove that all three surfaces were compiled.
				anchor := time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
				f.seedFreshness(t, FreshnessRecord{
					RefreshedAt: anchor, MaxStaleness: 24 * time.Hour,
					AdoptedRevision:  policyContentRevision([]byte("this is not Cedar")),
					AdoptedCreatedAt: anchor,
				})
			}
			before := f.snapshot(t)
			var counter *managedEpochCounter
			response := f.createGrant(t, managedEpochCountedData(f, &counter), map[string]any{
				"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if response.Code < http.StatusInternalServerError {
				t.Fatalf("status = %d body=%s, want invalid-union failure", response.Code, response.Body.String())
			}
			if counter.bumps != 0 || counter.reads != 1 {
				t.Fatalf("invalid union epoch read/bump = %d/%d, want lock read and zero bump", counter.reads, counter.bumps)
			}
			assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
		})
	}
}

func TestManagedProjectionFreshnessUsesTransactionClock(t *testing.T) {
	f := newManagedEpochFixture(t)
	dbNow := time.Date(2042, time.March, 4, 5, 6, 7, 800_000_000, time.UTC)
	var clock *managedEpochClock
	data := f.scopedData(func(sc store.Scope) store.Scope {
		out := newManagedEpochScope(sc)
		clock = &managedEpochClock{now: model.NewTimestamp(dbNow), fixed: true}
		out.clock = clock
		return out
	})

	response := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if clock == nil || clock.calls != 1 {
		t.Fatalf("transaction clock calls = %v, want one", clock)
	}
	fresh, found := f.freshness(t)
	if !found || !fresh.RefreshedAt.Equal(dbNow) {
		t.Fatalf("durable freshness = found:%t %+v, want DB time %s", found, fresh, dbNow)
	}
	if fresh.AdoptedRevision != "" || !fresh.AdoptedCreatedAt.IsZero() || fresh.MaxStaleness != 0 {
		t.Fatalf("local refresh fabricated signed DDIL state: %+v", fresh)
	}
	cached, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !cached.freshness.RefreshedAt.Equal(dbNow) {
		t.Fatalf("cached atomic freshness = loaded:%t %s, want transaction time %s", loaded, cached.freshness.RefreshedAt, dbNow)
	}
}

func TestManagedProjectionUnchangedBytesDoNotRefreshFreshness(t *testing.T) {
	f := newManagedEpochFixture(t)
	f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
	f.seedGrant(t, scopedGrant{
		SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator",
		RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant},
	})
	f.seedManagedRevision(t)
	anchor := time.Date(2030, time.February, 3, 4, 5, 6, 0, time.UTC)
	f.seedFreshness(t, FreshnessRecord{RefreshedAt: anchor})
	before := f.snapshot(t)
	var freshnessTrace []string
	data := f.scopedData(func(sc store.Scope) store.Scope {
		freshnessRepo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			t.Fatal(err)
		}
		return &managedEpochNoClockScope{
			Scope: sc, epochs: sc.(store.AuthorizationEpochStore), authority: sc.(store.AuthoritySnapshotLocker),
			ext: map[model.Kind]store.GenericRepo{
				policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshnessRepo, label: "freshness", trace: &freshnessTrace},
			},
		}
	})
	response := f.updateRole(t, data, "operator", map[string]any{
		"display_name": "Operator", "permissions": []string{"agent:read"},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if len(freshnessTrace) != 0 {
		t.Fatalf("byte-identical projection freshness writes = %v, want none", freshnessTrace)
	}
	got, found := f.freshness(t)
	if !found || !got.RefreshedAt.Equal(anchor) {
		t.Fatalf("byte-identical projection changed freshness: found=%t got=%+v", found, got)
	}
	assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 1, 0, 0, 0)
}

func TestManagedProjectionFreshnessClockFailuresAbortBeforeCAS(t *testing.T) {
	injected := errors.New("injected transaction clock failure")
	tests := []struct {
		name string
		wrap func(store.Scope, *managedEpochCounter) store.Scope
	}{
		{
			name: "capability absent",
			wrap: func(sc store.Scope, epochs *managedEpochCounter) store.Scope {
				return &managedEpochNoClockScope{
					Scope: sc, epochs: epochs, authority: sc.(store.AuthoritySnapshotLocker),
				}
			},
		},
		{
			name: "clock read failure",
			wrap: func(sc store.Scope, epochs *managedEpochCounter) store.Scope {
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.clock = &managedEpochClock{err: injected}
				return out
			},
		},
		{
			name: "zero clock",
			wrap: func(sc store.Scope, epochs *managedEpochCounter) store.Scope {
				out := newManagedEpochScope(sc)
				out.epochs = epochs
				out.clock = &managedEpochClock{fixed: true}
				return out
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			before := f.snapshot(t)
			var epochs *managedEpochCounter
			data := f.scopedData(func(sc store.Scope) store.Scope {
				epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
				return tc.wrap(sc, epochs)
			})
			response := f.createGrant(t, data, map[string]any{
				"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if response.Code < http.StatusInternalServerError {
				t.Fatalf("status = %d body=%s, want fail-closed", response.Code, response.Body.String())
			}
			if epochs == nil || epochs.reads != 1 || epochs.bumps != 0 {
				t.Fatalf("epoch read/bump = %v, want lock read and no CAS", epochs)
			}
			assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
			if _, found := f.freshness(t); found {
				t.Fatal("failed clock path persisted freshness")
			}
		})
	}
}

func TestManagedProjectionPartialDDILFreshnessFailsClosed(t *testing.T) {
	const adopted = `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`
	anchor := time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
	revision := policyContentRevision([]byte(adopted))
	tests := []struct {
		name string
		seed func(*testing.T, *managedEpochFixture)
	}{
		{name: "adopted surface without freshness", seed: func(t *testing.T, f *managedEpochFixture) {
			f.seedSurface(t, surfaceCedarDDIL, adopted)
		}},
		{name: "anchors without adopted surface", seed: func(t *testing.T, f *managedEpochFixture) {
			f.seedFreshness(t, FreshnessRecord{RefreshedAt: anchor, AdoptedRevision: revision, AdoptedCreatedAt: anchor})
		}},
		{name: "partial revision anchor", seed: func(t *testing.T, f *managedEpochFixture) {
			f.seedFreshness(t, FreshnessRecord{RefreshedAt: anchor, AdoptedRevision: revision})
		}},
		{name: "signed bound without anchors", seed: func(t *testing.T, f *managedEpochFixture) {
			f.seedFreshness(t, FreshnessRecord{RefreshedAt: anchor, MaxStaleness: time.Hour})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			tc.seed(t, f)
			before := f.snapshot(t)
			var epochs *managedEpochCounter
			data := managedEpochCountedData(f, &epochs)
			response := f.createGrant(t, data, map[string]any{
				"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
			})
			if response.Code < http.StatusInternalServerError || !strings.Contains(data.innerErr.Error(), "inconsistent DDIL durable adoption state") {
				t.Fatalf("status = %d inner=%v body=%s, want inconsistent-state failure", response.Code, data.innerErr, response.Body.String())
			}
			if epochs == nil || epochs.reads != 1 || epochs.bumps != 0 {
				t.Fatalf("epoch read/bump = %v, want lock read and no CAS", epochs)
			}
			assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
		})
	}
}

func TestManagedProjectionSignedDDILDoesNotRenewFreshness(t *testing.T) {
	const adopted = `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`
	f := newManagedEpochFixture(t)
	anchor := time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
	want := FreshnessRecord{
		RefreshedAt: anchor, MaxStaleness: 24 * time.Hour,
		AdoptedRevision: policyContentRevision([]byte(adopted)), AdoptedCreatedAt: anchor,
	}
	f.seedSurface(t, surfaceCedarDDIL, adopted)
	f.seedFreshness(t, want)
	var freshnessTrace []string
	data := f.scopedData(func(sc store.Scope) store.Scope {
		freshnessRepo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			t.Fatal(err)
		}
		return &managedEpochNoClockScope{
			Scope: sc, epochs: sc.(store.AuthorizationEpochStore), authority: sc.(store.AuthoritySnapshotLocker),
			ext: map[model.Kind]store.GenericRepo{
				policyFreshnessKind: managedEpochRecordingRepo{GenericRepo: freshnessRepo, label: "freshness", trace: &freshnessTrace},
			},
		}
	})
	response := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if len(freshnessTrace) != 0 {
		t.Fatalf("signed freshness writes = %v, want none", freshnessTrace)
	}
	got, found := f.freshness(t)
	if !found || !got.RefreshedAt.Equal(want.RefreshedAt) || got.MaxStaleness != want.MaxStaleness ||
		got.AdoptedRevision != want.AdoptedRevision || !got.AdoptedCreatedAt.Equal(want.AdoptedCreatedAt) {
		t.Fatalf("signed freshness changed: got found:%t %+v want %+v", found, got, want)
	}
	cached, loaded := f.m.grants.tenantState(f.tenant)
	if !loaded || !cached.freshness.RefreshedAt.Equal(anchor) || cached.freshness.MaxStaleness != want.MaxStaleness {
		t.Fatalf("cached atomic signed anchor/bound = loaded:%t %s/%s, want %s/%s",
			loaded, cached.freshness.RefreshedAt, cached.freshness.MaxStaleness, anchor, want.MaxStaleness)
	}
}

func TestManagedProjectionFreshnessFailureAfterBumpRollsBackEverything(t *testing.T) {
	f := newManagedEpochFixture(t)
	before := f.snapshot(t)
	injected := errors.New("injected freshness write failure")
	var epochs *managedEpochCounter
	data := f.scopedData(func(sc store.Scope) store.Scope {
		epochs = &managedEpochCounter{AuthorizationEpochStore: sc.(store.AuthorizationEpochStore)}
		freshnessRepo, err := sc.Ext(policyFreshnessKind)
		if err != nil {
			t.Fatal(err)
		}
		out := newManagedEpochScope(sc)
		out.epochs = epochs
		out.ext = map[model.Kind]store.GenericRepo{
			policyFreshnessKind: managedEpochFailingRepo{GenericRepo: freshnessRepo, err: injected},
		}
		return out
	})
	response := f.createGrant(t, data, map[string]any{
		"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
	})
	if response.Code < http.StatusInternalServerError || !errors.Is(data.innerErr, injected) {
		t.Fatalf("status = %d inner=%v body=%s, want injected failure", response.Code, data.innerErr, response.Body.String())
	}
	if epochs == nil || epochs.reads != 2 || epochs.bumps != 1 {
		t.Fatalf("epoch read/bump = %v, want one attempted CAS", epochs)
	}
	assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	if _, found := f.freshness(t); found {
		t.Fatal("failed freshness path persisted a freshness row")
	}
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

func TestManagedProjectionEpochCASIsFirstWrite(t *testing.T) {
	type testCase struct {
		name   string
		kind   model.Kind
		label  string
		write  string
		status int
		setup  func(*testing.T, *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder
	}
	cases := []testCase{
		{
			name: "role update", kind: customRoleKind, label: "role", write: "update", status: http.StatusOK,
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
				f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateRole(t, data, "operator", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
		},
		{
			name: "group update", kind: permGroupKind, label: "group", write: "update", status: http.StatusOK,
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
				f.seedRole(t, customRole{Name: "operator", Groups: []string{"agent-access"}})
				f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.updateGroup(t, data, "agent-access", map[string]any{"permissions": []string{"agent:write"}})
				}
			},
		},
		{
			name: "grant create", kind: scopedGrantKind, label: "grant", write: "create", status: http.StatusCreated,
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				return func(data api.ScopedData) *httptest.ResponseRecorder {
					return f.createGrant(t, data, map[string]any{
						"subject_kind": subjectUser, "subject_ref": model.NewID().String(), "role": auth.RoleEditor, "scope_tree": scopeTenant,
					})
				}
			},
		},
		{
			name: "grant revoke", kind: scopedGrantKind, label: "grant", write: "delete", status: http.StatusNoContent,
			setup: func(t *testing.T, f *managedEpochFixture) func(api.ScopedData) *httptest.ResponseRecorder {
				id := f.seedGrant(t, scopedGrant{SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: auth.RoleEditor, Scope: scopeSpec{Tree: scopeTenant}})
				f.seedManagedRevision(t)
				return func(data api.ScopedData) *httptest.ResponseRecorder { return f.revokeGrant(t, data, id) }
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedEpochFixture(t)
			invoke := tc.setup(t, f)
			var trace []string
			response := invoke(managedEpochOrderedData(t, f, tc.kind, tc.label, &trace))
			if response.Code != tc.status {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), tc.status)
			}
			want := []string{
				"epoch-read", "authority-lock", "db-now", "epoch-read", "epoch-bump",
				tc.label + "-" + tc.write, "revision-create", "freshness-create", "audit-append",
			}
			if len(trace) != len(want) {
				t.Fatalf("write trace = %v, want %v", trace, want)
			}
			for i := range want {
				if trace[i] != want[i] {
					t.Fatalf("write trace = %v, want %v", trace, want)
				}
			}
		})
	}
}

func TestManagedProjectionAuthorityLockPrecedesProspectiveReads(t *testing.T) {
	t.Run("role create sees group delete that won before lock", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
		before := f.snapshot(t)
		var injecting *managedEpochInjectingAuthority
		data := f.scopedData(func(sc store.Scope) store.Scope {
			repo, err := sc.Ext(permGroupKind)
			if err != nil {
				t.Fatal(err)
			}
			injecting = &managedEpochInjectingAuthority{
				AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker),
				inject: func() error {
					rec, found, err := findOne(context.Background(), repo, eq(colRBACName, "agent-access"))
					if err != nil {
						return err
					}
					if !found {
						return store.ErrNotFound
					}
					return repo.Delete(context.Background(), model.ID(rec.String(model.ColID)))
				},
			}
			out := newManagedEpochScope(sc)
			out.authority = injecting
			return out
		})
		response := f.createRole(t, data, map[string]any{
			"name": "operator", "groups": []string{"agent-access"},
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
		}
		if injecting == nil || injecting.calls != 1 {
			t.Fatalf("authority lock calls = %v, want one", injecting)
		}
		// The delete is injected at the lock boundary to model the writer that won
		// immediately before us. Group validation must happen after that boundary.
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	})

	t.Run("role update sees grant that won before lock", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
		before := f.snapshot(t)
		var injecting *managedEpochInjectingAuthority
		data := f.scopedData(func(sc store.Scope) store.Scope {
			repo, err := sc.Ext(scopedGrantKind)
			if err != nil {
				t.Fatal(err)
			}
			injecting = &managedEpochInjectingAuthority{
				AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker),
				inject: func() error {
					_, err := repo.Create(context.Background(), grantRecord(scopedGrant{
						SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator",
						RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant},
					}, "concurrent-winner"))
					return err
				},
			}
			out := newManagedEpochScope(sc)
			out.authority = injecting
			return out
		})
		response := f.updateRole(t, data, "operator", map[string]any{"permissions": []string{"agent:write"}})
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if injecting == nil || injecting.calls != 1 {
			t.Fatalf("authority lock calls = %v, want one", injecting)
		}
		// The injected grant represents a concurrent writer that committed just before
		// our lock. Re-reading after the lock makes the formerly-unassigned role effective,
		// so its edit must append/bump rather than take the stale unchanged path.
		assertManagedEpochDelta(t, before, f.snapshot(t), 1, 1, 1, 0, 0, 1)
	})

	t.Run("role delete sees grant that won before lock", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedRole(t, customRole{Name: "operator", Perms: []string{"agent:read"}})
		before := f.snapshot(t)
		var injecting *managedEpochInjectingAuthority
		data := f.scopedData(func(sc store.Scope) store.Scope {
			repo, err := sc.Ext(scopedGrantKind)
			if err != nil {
				t.Fatal(err)
			}
			injecting = &managedEpochInjectingAuthority{
				AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker),
				inject: func() error {
					_, err := repo.Create(context.Background(), grantRecord(scopedGrant{
						SubjectKind: subjectUser, SubjectRef: model.NewID().String(), Role: "operator",
						RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant},
					}, "concurrent-winner"))
					return err
				},
			}
			out := newManagedEpochScope(sc)
			out.authority = injecting
			return out
		})
		response := f.deleteRole(t, data, "operator")
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d body=%s, want 409", response.Code, response.Body.String())
		}
		if injecting == nil || injecting.calls != 1 {
			t.Fatalf("authority lock calls = %v, want one", injecting)
		}
		// The conflict rolls the injected row back too; neither side of the losing
		// transaction may survive.
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	})

	t.Run("group delete sees role include that won before lock", func(t *testing.T) {
		f := newManagedEpochFixture(t)
		f.seedGroup(t, permGroup{Name: "agent-access", Perms: []string{"agent:read"}})
		before := f.snapshot(t)
		var injecting *managedEpochInjectingAuthority
		data := f.scopedData(func(sc store.Scope) store.Scope {
			repo, err := sc.Ext(customRoleKind)
			if err != nil {
				t.Fatal(err)
			}
			injecting = &managedEpochInjectingAuthority{
				AuthoritySnapshotLocker: sc.(store.AuthoritySnapshotLocker),
				inject: func() error {
					_, err := repo.Create(context.Background(), roleRecord(
						"operator", "", "", "", nil, []string{"agent-access"}, nil, "concurrent-winner",
					))
					return err
				},
			}
			out := newManagedEpochScope(sc)
			out.authority = injecting
			return out
		})
		response := f.deleteGroup(t, data, "agent-access")
		if response.Code != http.StatusConflict {
			t.Fatalf("status = %d body=%s, want 409", response.Code, response.Body.String())
		}
		if injecting == nil || injecting.calls != 1 {
			t.Fatalf("authority lock calls = %v, want one", injecting)
		}
		assertManagedEpochDelta(t, before, f.snapshot(t), 0, 0, 0, 0, 0, 0)
	})
}

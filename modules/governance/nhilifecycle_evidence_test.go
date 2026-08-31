// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type evidenceInjectedStore struct {
	store.Store
	conflictRef    string
	lookupErrorRef string
	lookupErr      error
}

func (s evidenceInjectedStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(evidenceInjectedScope{
			Scope: sc, conflictRef: s.conflictRef,
			lookupErrorRef: s.lookupErrorRef, lookupErr: s.lookupErr,
		})
	})
}

type evidenceInjectedScope struct {
	store.Scope
	conflictRef    string
	lookupErrorRef string
	lookupErr      error
}

func (s evidenceInjectedScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil {
		return nil, err
	}
	if kind == nhiLifecycleKind && s.conflictRef != "" {
		return evidenceConflictRepo{GenericRepo: repo, conflictRef: s.conflictRef}, nil
	}
	return repo, nil
}

func (s evidenceInjectedScope) Identities() store.MutableRepository[model.Identity] {
	repo := s.Scope.Identities()
	if s.lookupErrorRef == "" || s.lookupErr == nil {
		return repo
	}
	return evidenceIdentityRepo{MutableRepository: repo, lookupErrorRef: s.lookupErrorRef, err: s.lookupErr}
}

type evidenceConflictRepo struct {
	store.GenericRepo
	conflictRef string
}

func (r evidenceConflictRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	if rec.String(colNHIIdentityRef) == r.conflictRef {
		return nil, store.ErrConflict
	}
	return r.GenericRepo.Update(ctx, rec)
}

type evidenceIdentityRepo struct {
	store.MutableRepository[model.Identity]
	lookupErrorRef string
	err            error
}

func (r evidenceIdentityRepo) List(ctx context.Context, q model.Query) ([]model.Identity, model.Page, error) {
	for _, filter := range q.Filters {
		if filter.Column == "external_id" && filter.Op == model.OpEq && filter.Value == r.lookupErrorRef {
			return nil, model.Page{}, r.err
		}
	}
	return r.MutableRepository.List(ctx, q)
}

type nhiEvidenceFixture struct {
	m        *Module
	base     store.Store
	injected evidenceInjectedStore
	tenant   model.TenantID
	host     *capturingHost
}

func newNHIEvidenceFixture(t *testing.T, conflictRef, lookupErrorRef string, lookupErr error) *nhiEvidenceFixture {
	t.Helper()
	ctx := context.Background()
	m := New(WithClock(&intClock{t: intBase}))
	base, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if err := base.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := base.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Evidence Test", Slug: "evidence-test", Status: model.StatusActive})
		tenant = model.TenantID(org.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	injected := evidenceInjectedStore{
		Store: base, conflictRef: conflictRef,
		lookupErrorRef: lookupErrorRef, lookupErr: lookupErr,
	}
	m.UseData(api.NewModuleData(injected))
	host := &capturingHost{}
	if err := m.Init(ctx, host); err != nil {
		t.Fatal(err)
	}
	return &nhiEvidenceFixture{m: m, base: base, injected: injected, tenant: tenant, host: host}
}

func (f *nhiEvidenceFixture) seedIdentity(t *testing.T, ref, principalType string, disabled bool) {
	t.Helper()
	err := f.base.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			Name: ref, Kind: "test", ExternalID: ref, Provider: "test",
			Metadata: map[string]any{"principal_type": principalType, "disabled": disabled},
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed identity %q: %v", ref, err)
	}
}

func (f *nhiEvidenceFixture) seedLifecycle(t *testing.T, rec model.Record) {
	t.Helper()
	err := f.base.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), rec)
		return err
	})
	if err != nil {
		t.Fatalf("seed lifecycle row: %v", err)
	}
}

func (f *nhiEvidenceFixture) sweep(t *testing.T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/m/governance/nhi/sweep", nil)
	w := httptest.NewRecorder()
	f.m.handleNHISweep(w, req, api.ModuleContext{
		Principal: auth.ScopedPrincipal("evidence-admin", "Evidence Admin", f.tenant, auth.RoleAdmin),
		Tenant:    f.tenant,
		Data:      api.NewScopedData(f.injected, f.tenant),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("sweep returned %d: %s", w.Code, w.Body.String())
	}
}

func evidenceHostHasFinding(host *capturingHost, kind string) bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	for _, evt := range host.events {
		if finding, ok := event.FindingOf(evt); ok && finding.Kind == kind {
			return true
		}
	}
	return false
}

func evidenceTrailHasEvent(t *testing.T, st store.Store, tenant model.TenantID, ref, eventKind string) bool {
	t.Helper()
	found := false
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nhiEventKind)
		if err != nil {
			return err
		}
		records, err := listAll(context.Background(), repo, eq(colNHIEvtIdentity, ref), eq(colNHIEvtKind, eventKind))
		if err != nil {
			return err
		}
		found = len(records) > 0
		return nil
	})
	if err != nil {
		t.Fatalf("read lifecycle trail: %v", err)
	}
	return found
}

func TestNHILifecycleSignalConflictDoesNotEmitFalseEvidence(t *testing.T) {
	t.Run("external revoke block", func(t *testing.T) {
		const ref = "nhi:revoked"
		f := newNHIEvidenceFixture(t, ref, "", nil)
		f.seedLifecycle(t, newLifecycleRecord(ref, "vault", ""))

		f.m.onLifecycleSignal(context.Background(), event.FromObservation(f.tenant.String(), "ssf", sdkmodel.FindingReport{
			Kind: "caep_credential_revoked", SubjectKind: "identity", SubjectRef: ref,
		}))

		if got := readEnforce(t, f.base, f.tenant, ref); got == enforceBlocked {
			t.Fatalf("conflicted block must not persist, got enforcement %q", got)
		}
		if evidenceHostHasFinding(f.host, "nhi_external_revoke_blocked") {
			t.Fatal("conflicted block emitted nhi_external_revoke_blocked")
		}
		if evidenceTrailHasEvent(t, f.base, f.tenant, ref, "external_revoke") {
			t.Fatal("conflicted block recorded external_revoke lifecycle evidence")
		}
	})

	t.Run("sponsor revoke orphan", func(t *testing.T) {
		const (
			ref     = "nhi:sponsored"
			sponsor = "human:sponsor"
		)
		f := newNHIEvidenceFixture(t, ref, "", nil)
		rec := newLifecycleRecord(ref, "vault", "")
		rec[colNHISponsorRef] = sponsor
		f.seedLifecycle(t, rec)

		f.m.onLifecycleSignal(context.Background(), event.FromObservation(f.tenant.String(), "ssf", sdkmodel.FindingReport{
			Kind: "caep_session_revoked", SubjectKind: "identity", SubjectRef: sponsor,
		}))

		if readOrphan(t, f.base, f.tenant, ref) {
			t.Fatal("conflicted orphan transition persisted")
		}
		if evidenceHostHasFinding(f.host, "nhi_orphaned") {
			t.Fatal("conflicted orphan transition emitted nhi_orphaned")
		}
		if evidenceTrailHasEvent(t, f.base, f.tenant, ref, "orphaned") {
			t.Fatal("conflicted orphan transition recorded lifecycle evidence")
		}
	})
}

func TestNHILifecycleSponsorLookupErrorPreservesOrphanGate(t *testing.T) {
	const (
		ref     = "agent:lookup-failure"
		sponsor = "human:sponsor"
	)
	f := newNHIEvidenceFixture(t, "", sponsor, errors.New("transient roster outage"))
	f.seedIdentity(t, sponsor, string(identitysource.PrincipalHuman), false)
	rec := newLifecycleRecord(ref, "spiffe", NHIKindAgent)
	rec[colNHIOwnerRef] = sponsor
	rec[colNHISponsorRef] = sponsor
	rec[colNHIOrphaned] = true
	f.seedLifecycle(t, rec)

	f.sweep(t)

	if !readOrphan(t, f.base, f.tenant, ref) {
		t.Fatal("transient sponsor lookup error cleared the orphan gate")
	}
	if err := f.m.CheckAgentForExchange(context.Background(), f.tenant, ref, sponsor); err == nil {
		t.Fatal("token exchange was allowed while the preserved orphan gate was set")
	}
}

func TestNHILifecycleSweepOrphanEvidenceRequiresPersistedTransition(t *testing.T) {
	const (
		ref     = "agent:new-orphan"
		sponsor = "human:disabled"
	)
	seed := func(t *testing.T, f *nhiEvidenceFixture) {
		f.seedIdentity(t, sponsor, string(identitysource.PrincipalHuman), true)
		rec := newLifecycleRecord(ref, "spiffe", NHIKindAgent)
		rec[colNHIOwnerRef] = sponsor
		rec[colNHISponsorRef] = sponsor
		f.seedLifecycle(t, rec)
	}

	t.Run("successful update records and emits", func(t *testing.T) {
		f := newNHIEvidenceFixture(t, "", "", nil)
		seed(t, f)

		f.sweep(t)

		if !readOrphan(t, f.base, f.tenant, ref) {
			t.Fatal("successful sweep did not persist orphan state")
		}
		if !evidenceHostHasFinding(f.host, "nhi_orphaned") {
			t.Fatal("successful orphan transition did not emit nhi_orphaned")
		}
		if !evidenceTrailHasEvent(t, f.base, f.tenant, ref, "orphaned") {
			t.Fatal("successful orphan transition is missing from the lifecycle trail")
		}
	})

	t.Run("conflicted update records and emits nothing", func(t *testing.T) {
		f := newNHIEvidenceFixture(t, ref, "", nil)
		seed(t, f)

		f.sweep(t)

		if readOrphan(t, f.base, f.tenant, ref) {
			t.Fatal("conflicted sweep persisted orphan state")
		}
		if evidenceHostHasFinding(f.host, "nhi_orphaned") {
			t.Fatal("conflicted sweep emitted nhi_orphaned")
		}
		if evidenceTrailHasEvent(t, f.base, f.tenant, ref, "orphaned") {
			t.Fatal("conflicted sweep recorded orphaned lifecycle evidence")
		}
	})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

type policyEpochPorts struct {
	readFact store.AuthorizationFactRef
	nextFact store.AuthorizationFactRef
	readErr  error
	bumpErr  error
	reads    int
	bumps    int
	expected store.AuthorizationFactRef
}

func (p *policyEpochPorts) ReadAuthorizationEpoch(context.Context) (store.AuthorizationFactRef, error) {
	p.reads++
	return p.readFact, p.readErr
}

func (p *policyEpochPorts) BumpAuthorizationEpoch(
	_ context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	p.bumps++
	p.expected = expected
	if p.bumpErr != nil {
		return store.AuthorizationFactRef{}, p.bumpErr
	}
	if p.nextFact != (store.AuthorizationFactRef{}) {
		return p.nextFact, nil
	}
	next := expected
	next.Version++
	return next, nil
}

type policyEpochUnitScope struct {
	store.Scope
	tenant model.TenantID
	*policyEpochPorts
}

func (s *policyEpochUnitScope) Tenant() model.TenantID { return s.tenant }

type policyEpochAuthorityLock struct {
	err   error
	calls int
	refs  []store.AuthorizationFactRef
}

func (l *policyEpochAuthorityLock) LockAuthoritySnapshot(_ context.Context, refs []store.AuthorizationFactRef) error {
	l.calls++
	l.refs = append([]store.AuthorizationFactRef(nil), refs...)
	return l.err
}

type policyEpochLockUnitScope struct {
	*policyEpochUnitScope
	locker *policyEpochAuthorityLock
}

func (s *policyEpochLockUnitScope) LockAuthoritySnapshot(ctx context.Context, refs []store.AuthorizationFactRef) error {
	return s.locker.LockAuthoritySnapshot(ctx, refs)
}

func TestAdvancePolicyAuthorizationEpochValidatesExactWitnessAndCAS(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	valid := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 7,
	}
	foreign := model.TenantID(model.NewID())
	injected := errors.New("injected epoch failure")
	tests := []struct {
		name     string
		ports    policyEpochPorts
		wantRead int
		wantBump int
		wantErr  bool
	}{
		{name: "exactly once", ports: policyEpochPorts{readFact: valid}, wantRead: 1, wantBump: 1},
		{name: "read error", ports: policyEpochPorts{readErr: injected}, wantRead: 1, wantErr: true},
		{name: "lookalike kind", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: "core.authorization_epochs", ID: valid.ID, Version: 7}}, wantRead: 1, wantErr: true},
		{name: "foreign id", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(foreign), Version: 7}}, wantRead: 1, wantErr: true},
		{name: "zero generation", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: valid.ID}}, wantRead: 1, wantErr: true},
		{name: "exhausted", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: valid.ID, Version: math.MaxInt64}}, wantRead: 1, wantErr: true},
		{name: "stale CAS", ports: policyEpochPorts{readFact: valid, bumpErr: store.ErrAuthorizationEpochUnavailable}, wantRead: 1, wantBump: 1, wantErr: true},
		{name: "arbitrary bump error", ports: policyEpochPorts{readFact: valid, bumpErr: injected}, wantRead: 1, wantBump: 1, wantErr: true},
		{name: "same generation returned", ports: policyEpochPorts{readFact: valid, nextFact: valid}, wantRead: 1, wantBump: 1, wantErr: true},
		{name: "skipped generation returned", ports: policyEpochPorts{readFact: valid, nextFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: valid.ID, Version: 9}}, wantRead: 1, wantBump: 1, wantErr: true},
		{name: "foreign next returned", ports: policyEpochPorts{readFact: valid, nextFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(foreign), Version: 8}}, wantRead: 1, wantBump: 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ports := tc.ports
			sc := &policyEpochUnitScope{tenant: tenant, policyEpochPorts: &ports}
			err := advancePolicyAuthorizationEpoch(context.Background(), sc)
			if tc.wantErr {
				if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
					t.Fatalf("error = %v, want ErrAuthorizationEpochUnavailable", err)
				}
			} else if err != nil {
				t.Fatalf("advance exact witness: %v", err)
			}
			if ports.reads != tc.wantRead || ports.bumps != tc.wantBump {
				t.Fatalf("calls read/bump = %d/%d, want %d/%d", ports.reads, ports.bumps, tc.wantRead, tc.wantBump)
			}
			if ports.bumps == 1 && ports.expected != valid {
				t.Fatalf("CAS expected = %+v, want exact read witness %+v", ports.expected, valid)
			}
		})
	}
}

func TestLockPolicyAuthorizationEpochRequiresAndPinsExactWitness(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	valid := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 7,
	}
	injected := errors.New("injected authority lock failure")
	tests := []struct {
		name      string
		ports     policyEpochPorts
		lockErr   error
		wantReads int
		wantLocks int
		wantErr   bool
	}{
		{name: "exact witness locks once", ports: policyEpochPorts{readFact: valid}, wantReads: 1, wantLocks: 1},
		{name: "read failure", ports: policyEpochPorts{readErr: injected}, wantReads: 1, wantErr: true},
		{name: "wrong kind", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: "core.authorization_epochs", ID: valid.ID, Version: 7}}, wantReads: 1, wantErr: true},
		{name: "foreign id", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(model.NewID()), Version: 7}}, wantReads: 1, wantErr: true},
		{name: "zero generation", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: valid.ID}}, wantReads: 1, wantErr: true},
		// The legacy wrapper still rejects exhaustion, but it must first pin the
		// exact fact so C3's witness variant can serve a rollback-current no-op
		// without attempting a CAS.
		{name: "exhausted", ports: policyEpochPorts{readFact: store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: valid.ID, Version: math.MaxInt64}}, wantReads: 1, wantLocks: 1, wantErr: true},
		{name: "stale or unavailable lock", ports: policyEpochPorts{readFact: valid}, lockErr: store.ErrConflict, wantReads: 1, wantLocks: 1, wantErr: true},
		{name: "arbitrary lock failure", ports: policyEpochPorts{readFact: valid}, lockErr: injected, wantReads: 1, wantLocks: 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ports := tc.ports
			locker := &policyEpochAuthorityLock{err: tc.lockErr}
			sc := &policyEpochLockUnitScope{
				policyEpochUnitScope: &policyEpochUnitScope{tenant: tenant, policyEpochPorts: &ports},
				locker:               locker,
			}
			err := lockPolicyAuthorizationEpoch(context.Background(), sc)
			if tc.wantErr {
				if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
					t.Fatalf("error = %v, want ErrAuthorizationEpochUnavailable", err)
				}
			} else if err != nil {
				t.Fatalf("lock exact witness: %v", err)
			}
			if ports.reads != tc.wantReads || ports.bumps != 0 || locker.calls != tc.wantLocks {
				t.Fatalf("calls read/bump/lock = %d/%d/%d, want %d/0/%d", ports.reads, ports.bumps, locker.calls, tc.wantReads, tc.wantLocks)
			}
			if locker.calls == 1 && (len(locker.refs) != 1 || locker.refs[0] != ports.readFact) && tc.lockErr == nil {
				t.Fatalf("locked refs = %+v, want exact witness %+v", locker.refs, ports.readFact)
			}
		})
	}
}

func TestLockPolicyAuthorizationEpochRejectsMissingCombinedCapabilities(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	valid := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.ID(tenant), Version: 1}
	ports := &policyEpochPorts{readFact: valid}
	locker := &policyEpochAuthorityLock{}

	missingLocker := &policyEpochUnitScope{tenant: tenant, policyEpochPorts: ports}
	if err := lockPolicyAuthorizationEpoch(context.Background(), missingLocker); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
		t.Fatalf("missing locker error = %v, want ErrAuthorizationEpochUnavailable", err)
	}
	if ports.reads != 0 || ports.bumps != 0 {
		t.Fatalf("missing locker used epoch ports: read/bump=%d/%d", ports.reads, ports.bumps)
	}

	missingEpoch := &struct {
		store.Scope
		store.AuthoritySnapshotLocker
	}{Scope: &policyEpochUnitScope{tenant: tenant}, AuthoritySnapshotLocker: locker}
	if err := lockPolicyAuthorizationEpoch(context.Background(), missingEpoch); !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
		t.Fatalf("missing epoch error = %v, want ErrAuthorizationEpochUnavailable", err)
	}
	if locker.calls != 0 {
		t.Fatalf("missing epoch called locker %d times", locker.calls)
	}
}

type policyEpochScopedData struct {
	st       store.Store
	tenant   model.TenantID
	wrap     func(store.Scope) store.Scope
	innerErr error
}

func (d *policyEpochScopedData) View(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.View(ctx, d.tenant, fn)
}

func (d *policyEpochScopedData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, d.tenant, func(sc store.Scope) error {
		if d.wrap != nil {
			sc = d.wrap(sc)
		}
		d.innerErr = fn(sc)
		return d.innerErr
	})
}

func (d *policyEpochScopedData) Export(ctx context.Context, fn func(store.ExportScope) error) error {
	return d.st.Export(ctx, d.tenant, fn)
}

type policyEpochReaderOnlyScope struct {
	store.Scope
	reader store.AuthorizationEpochReader
	reads  *int
}

func (s *policyEpochReaderOnlyScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	*s.reads++
	return s.reader.ReadAuthorizationEpoch(ctx)
}

type policyEpochBumperOnlyScope struct {
	store.Scope
	bumper store.AuthorizationEpochBumper
	bumps  *int
}

func (s *policyEpochBumperOnlyScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	*s.bumps++
	return s.bumper.BumpAuthorizationEpoch(ctx, expected)
}

type policyEpochFullScope struct {
	store.Scope
	epochs   store.AuthorizationEpochStore
	policies store.Repository[model.Policy]
	audit    store.AuditLog
}

func newPolicyEpochFullScope(sc store.Scope) *policyEpochFullScope {
	return &policyEpochFullScope{Scope: sc, epochs: sc.(store.AuthorizationEpochStore)}
}

func (s *policyEpochFullScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	return s.epochs.ReadAuthorizationEpoch(ctx)
}

func (s *policyEpochFullScope) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	return s.epochs.BumpAuthorizationEpoch(ctx, expected)
}

func (s *policyEpochFullScope) Policies() store.Repository[model.Policy] {
	if s.policies != nil {
		return s.policies
	}
	return s.Scope.Policies()
}

func (s *policyEpochFullScope) Audit() store.AuditLog {
	if s.audit != nil {
		return s.audit
	}
	return s.Scope.Audit()
}

type policyEpochPostWriteErrorRepo struct {
	store.Repository[model.Policy]
	err error
}

func (r policyEpochPostWriteErrorRepo) Create(ctx context.Context, p model.Policy) (model.Policy, error) {
	created, err := r.Repository.Create(ctx, p)
	if err != nil {
		return model.Policy{}, err
	}
	return created, r.err
}

type policyEpochRecordingRepo struct {
	store.Repository[model.Policy]
	trace *[]string
}

func (r policyEpochRecordingRepo) Create(ctx context.Context, p model.Policy) (model.Policy, error) {
	*r.trace = append(*r.trace, "policy-create")
	return r.Repository.Create(ctx, p)
}

type policyEpochRecordingStore struct {
	store.AuthorizationEpochStore
	trace *[]string
}

func (s policyEpochRecordingStore) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	*s.trace = append(*s.trace, "epoch-read")
	return s.AuthorizationEpochStore.ReadAuthorizationEpoch(ctx)
}

func (s policyEpochRecordingStore) BumpAuthorizationEpoch(ctx context.Context, expected store.AuthorizationFactRef) (store.AuthorizationFactRef, error) {
	*s.trace = append(*s.trace, "epoch-bump")
	return s.AuthorizationEpochStore.BumpAuthorizationEpoch(ctx, expected)
}

type policyEpochFailingAudit struct {
	store.AuditLog
	err error
}

func (a policyEpochFailingAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, a.err
}

type policyEpochHost struct {
	events []event.Event
}

func (h *policyEpochHost) Publish(_ context.Context, e event.Event) error {
	h.events = append(h.events, e)
	return nil
}

func (*policyEpochHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}

func (*policyEpochHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (*policyEpochHost) Config() sdk.Config { return sdk.Config{} }

func newPolicyEpochIntegrationFixture(t *testing.T) (*Module, *policyEpochHost, store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	m := New()
	host := &policyEpochHost{}
	if err := m.Init(ctx, host); err != nil {
		t.Fatal(err)
	}
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		_ = st.Close()
	})
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Epoch", Slug: "epoch", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return m, host, st, tenant
}

func policyEpochRead(t *testing.T, st store.Store, tenant model.TenantID) store.AuthorizationFactRef {
	t.Helper()
	var fact store.AuthorizationFactRef
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fact
}

func policyEpochRows(t *testing.T, st store.Store, tenant model.TenantID) []model.Policy {
	t.Helper()
	var policies []model.Policy
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		policies, _, err = sc.Policies().List(context.Background(), model.Query{Limit: 100})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return policies
}

func invokePolicyCreate(m *Module, tenant model.TenantID, data api.ScopedData) *httptest.ResponseRecorder {
	body := `{"name":"deny","kind":"abac","enabled":true,"spec":{"rules":[{"deny":true,"permission":"agent:write"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/policies", strings.NewReader(body))
	rec := httptest.NewRecorder()
	m.handleCreatePolicy(rec, req, api.ModuleContext{
		Tenant: tenant, Data: data,
		Principal: auth.Principal{Kind: auth.KindUser, UserID: model.NewID()},
	})
	return rec
}

func TestPolicyAuthorizationEpochCapabilityAbsenceAndPartialScopesFailClosed(t *testing.T) {
	tests := []struct {
		name string
		wrap func(store.Scope, *int, *int) store.Scope
	}{
		{name: "absent", wrap: func(sc store.Scope, _, _ *int) store.Scope {
			return struct{ store.Scope }{sc}
		}},
		{name: "reader only", wrap: func(sc store.Scope, reads, _ *int) store.Scope {
			return &policyEpochReaderOnlyScope{Scope: sc, reader: sc.(store.AuthorizationEpochReader), reads: reads}
		}},
		{name: "bumper only", wrap: func(sc store.Scope, _, bumps *int) store.Scope {
			return &policyEpochBumperOnlyScope{Scope: sc, bumper: sc.(store.AuthorizationEpochBumper), bumps: bumps}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, host, st, tenant := newPolicyEpochIntegrationFixture(t)
			before := policyEpochRead(t, st, tenant)
			var reads, bumps int
			data := &policyEpochScopedData{st: st, tenant: tenant, wrap: func(sc store.Scope) store.Scope {
				return tc.wrap(sc, &reads, &bumps)
			}}
			rec := invokePolicyCreate(m, tenant, data)
			if rec.Code < http.StatusInternalServerError {
				t.Fatalf("create status = %d body=%s, want UNKNOWN/fail-closed", rec.Code, rec.Body.String())
			}
			if !errors.Is(data.innerErr, store.ErrAuthorizationEpochUnavailable) {
				t.Fatalf("inner error = %v, want ErrAuthorizationEpochUnavailable", data.innerErr)
			}
			if reads != 0 || bumps != 0 {
				t.Fatalf("partial capability was used: reads/bumps=%d/%d", reads, bumps)
			}
			if after := policyEpochRead(t, st, tenant); after != before {
				t.Fatalf("failed mutation changed epoch: before=%+v after=%+v", before, after)
			}
			if rows := policyEpochRows(t, st, tenant); len(rows) != 0 {
				t.Fatalf("failed mutation wrote %d policy rows", len(rows))
			}
			if len(host.events) != 0 {
				t.Fatalf("failed mutation emitted %d events", len(host.events))
			}
		})
	}
}

func TestPolicyAuthorizationEpochCASFailureAbortsBeforePolicyWrite(t *testing.T) {
	m, host, st, tenant := newPolicyEpochIntegrationFixture(t)
	before := policyEpochRead(t, st, tenant)
	ports := &policyEpochPorts{
		readFact: before,
		bumpErr:  store.ErrAuthorizationEpochUnavailable,
	}
	data := &policyEpochScopedData{st: st, tenant: tenant, wrap: func(sc store.Scope) store.Scope {
		out := newPolicyEpochFullScope(sc)
		out.epochs = ports
		return out
	}}

	rec := invokePolicyCreate(m, tenant, data)
	if rec.Code < http.StatusInternalServerError {
		t.Fatalf("create status = %d body=%s, want UNKNOWN/fail-closed", rec.Code, rec.Body.String())
	}
	if !errors.Is(data.innerErr, store.ErrAuthorizationEpochUnavailable) {
		t.Fatalf("inner error = %v, want ErrAuthorizationEpochUnavailable", data.innerErr)
	}
	if ports.reads != 1 || ports.bumps != 1 || ports.expected != before {
		t.Fatalf("read/CAS calls = %d/%d expected=%+v, want exactly one with %+v", ports.reads, ports.bumps, ports.expected, before)
	}
	if after := policyEpochRead(t, st, tenant); after != before {
		t.Fatalf("stale CAS changed epoch: before=%+v after=%+v", before, after)
	}
	if rows := policyEpochRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("stale CAS wrote %d policy rows", len(rows))
	}
	if len(host.events) != 0 {
		t.Fatalf("stale CAS emitted %d events", len(host.events))
	}
}

func TestPolicyAuthorizationEpochCASIsFirstWriteAndRunsExactlyOnce(t *testing.T) {
	m, _, st, tenant := newPolicyEpochIntegrationFixture(t)
	before := policyEpochRead(t, st, tenant)
	var trace []string
	data := &policyEpochScopedData{st: st, tenant: tenant, wrap: func(sc store.Scope) store.Scope {
		out := newPolicyEpochFullScope(sc)
		out.epochs = policyEpochRecordingStore{
			AuthorizationEpochStore: sc.(store.AuthorizationEpochStore), trace: &trace,
		}
		out.policies = policyEpochRecordingRepo{Repository: sc.Policies(), trace: &trace}
		return out
	}}

	rec := invokePolicyCreate(m, tenant, data)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s, want 201", rec.Code, rec.Body.String())
	}
	want := []string{"epoch-read", "epoch-bump", "policy-create"}
	if len(trace) != len(want) {
		t.Fatalf("write trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("write trace = %v, want %v", trace, want)
		}
	}
	if after := policyEpochRead(t, st, tenant); after.Version != before.Version+1 {
		t.Fatalf("committed epoch = %+v, want version %d", after, before.Version+1)
	}
	if rows := policyEpochRows(t, st, tenant); len(rows) != 1 {
		t.Fatalf("committed policy rows = %d, want one", len(rows))
	}
}

func TestPolicyAuthorizationEpochFailuresAfterBumpRollbackEpochPolicyAndSideEffects(t *testing.T) {
	tests := []struct {
		name string
		wrap func(store.Scope, error) store.Scope
	}{
		{name: "policy row reports failure after insert", wrap: func(sc store.Scope, injected error) store.Scope {
			out := newPolicyEpochFullScope(sc)
			out.policies = policyEpochPostWriteErrorRepo{Repository: sc.Policies(), err: injected}
			return out
		}},
		{name: "audit append fails after policy insert", wrap: func(sc store.Scope, injected error) store.Scope {
			out := newPolicyEpochFullScope(sc)
			out.audit = policyEpochFailingAudit{AuditLog: sc.Audit(), err: injected}
			return out
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, host, st, tenant := newPolicyEpochIntegrationFixture(t)
			before := policyEpochRead(t, st, tenant)
			cached := &compiledSet{}
			m.eval.cache = map[model.TenantID]*compiledSet{tenant: cached}
			injected := errors.New("injected post-bump failure")
			data := &policyEpochScopedData{st: st, tenant: tenant, wrap: func(sc store.Scope) store.Scope {
				return tc.wrap(sc, injected)
			}}

			rec := invokePolicyCreate(m, tenant, data)
			if rec.Code < http.StatusInternalServerError {
				t.Fatalf("create status = %d body=%s, want failure", rec.Code, rec.Body.String())
			}
			if !errors.Is(data.innerErr, injected) {
				t.Fatalf("inner error = %v, want injected failure", data.innerErr)
			}
			if after := policyEpochRead(t, st, tenant); after != before {
				t.Fatalf("rollback did not restore epoch: before=%+v after=%+v", before, after)
			}
			if rows := policyEpochRows(t, st, tenant); len(rows) != 0 {
				t.Fatalf("rollback left %d policy rows", len(rows))
			}
			if m.eval.cache[tenant] != cached {
				t.Fatal("failed transaction invalidated the ABAC cache before commit")
			}
			if len(host.events) != 0 {
				t.Fatalf("failed transaction emitted %d policy events before commit", len(host.events))
			}
		})
	}
}

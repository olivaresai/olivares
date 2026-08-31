// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These tests use an intentionally narrow data/scope decorator. The producers must
// obtain their DB clock, authorization epoch and every policy row through precisely the
// one View below; no test reaches into an evaluator cache or substitutes process time.
type typedEvidenceFixture struct {
	st     store.Store
	tenant model.TenantID
}

func newTypedEvidenceFixture(t *testing.T) *typedEvidenceFixture {
	t.Helper()
	ctx := context.Background()
	m := New()
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Typed Evidence", Slug: "typed-evidence", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &typedEvidenceFixture{st: st, tenant: tenant}
}

// typedEvidenceData is api.ModuleData plus a View counter and a post-View hook used
// to make the runtime-operation bracket observable.
type typedEvidenceData struct {
	st        store.Store
	wrap      func(store.Scope) store.Scope
	afterView func()
	views     int
}

func (d *typedEvidenceData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.views++
	err := d.st.View(ctx, tenant, func(sc store.Scope) error {
		if d.wrap != nil {
			sc = d.wrap(sc)
		}
		return fn(sc)
	})
	if err == nil && d.afterView != nil {
		d.afterView()
	}
	return err
}

func (d *typedEvidenceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if d.wrap != nil {
			sc = d.wrap(sc)
		}
		return fn(sc)
	})
}

var _ api.ModuleData = (*typedEvidenceData)(nil)

type typedEvidenceScope struct {
	store.Scope
	now        model.Timestamp
	clockErr   error
	forceClock bool

	fact       store.AuthorizationFactRef
	epochErr   error
	forceEpoch bool

	policies    store.Repository[model.Policy]
	agents      store.Repository[model.Agent]
	sessions    store.Repository[model.Session]
	resources   store.ResourceRepo
	agentGroups store.Repository[model.AgentGroup]
	tenant      model.TenantID
	forceTenant bool
	clockCalls  *int
	epochCalls  *int
}

func (s typedEvidenceScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	if s.clockCalls != nil {
		(*s.clockCalls)++
	}
	if s.forceClock {
		return s.now, s.clockErr
	}
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errEvidenceUnavailable
	}
	return clock.TransactionNow(ctx)
}

func (s typedEvidenceScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	if s.epochCalls != nil {
		(*s.epochCalls)++
	}
	if s.forceEpoch {
		return s.fact, s.epochErr
	}
	reader, ok := s.Scope.(store.AuthorizationEpochReader)
	if !ok {
		return store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	return reader.ReadAuthorizationEpoch(ctx)
}

func (s typedEvidenceScope) Policies() store.Repository[model.Policy] {
	if s.policies != nil {
		return s.policies
	}
	return s.Scope.Policies()
}

func (s typedEvidenceScope) Resources() store.ResourceRepo {
	if s.resources != nil {
		return s.resources
	}
	return s.Scope.Resources()
}

func (s typedEvidenceScope) Agents() store.Repository[model.Agent] {
	if s.agents != nil {
		return s.agents
	}
	return s.Scope.Agents()
}

func (s typedEvidenceScope) Sessions() store.Repository[model.Session] {
	if s.sessions != nil {
		return s.sessions
	}
	return s.Scope.Sessions()
}

func (s typedEvidenceScope) AgentGroups() store.Repository[model.AgentGroup] {
	if s.agentGroups != nil {
		return s.agentGroups
	}
	return s.Scope.AgentGroups()
}

func (s typedEvidenceScope) Tenant() model.TenantID {
	if s.forceTenant {
		return s.tenant
	}
	return s.Scope.Tenant()
}

// These narrow wrappers deliberately hide one optional capability while retaining
// the other. A producer must not infer a database clock/epoch from the underlying
// Scope through embedding: both capabilities are required in the same View.
type typedEvidenceNoClockScope struct{ store.Scope }

func (s typedEvidenceNoClockScope) ReadAuthorizationEpoch(ctx context.Context) (store.AuthorizationFactRef, error) {
	reader, ok := s.Scope.(store.AuthorizationEpochReader)
	if !ok {
		return store.AuthorizationFactRef{}, errEvidenceUnavailable
	}
	return reader.ReadAuthorizationEpoch(ctx)
}

type typedEvidenceNoEpochScope struct {
	store.Scope
	now model.Timestamp
}

func (s typedEvidenceNoEpochScope) TransactionNow(context.Context) (model.Timestamp, error) {
	return s.now, nil
}

type typedEvidencePolicyRepo struct {
	store.Repository[model.Policy]
	list    func(context.Context, model.Query) ([]model.Policy, model.Page, error)
	queries []model.Query
}

func (r *typedEvidencePolicyRepo) List(ctx context.Context, q model.Query) ([]model.Policy, model.Page, error) {
	r.queries = append(r.queries, q)
	return r.list(ctx, q)
}

type typedEvidenceResourceRepo struct {
	store.ResourceRepo
	gets        int
	err         error
	result      model.Resource
	forceResult bool
}

type typedEvidenceGetRepo[T any] struct {
	store.Repository[T]
	get  func(context.Context, model.ID) (T, error)
	gets int
}

func (r *typedEvidenceGetRepo[T]) Get(ctx context.Context, id model.ID) (T, error) {
	r.gets++
	return r.get(ctx, id)
}

func (r *typedEvidenceResourceRepo) Get(ctx context.Context, id model.ID) (model.Resource, error) {
	r.gets++
	if r.err != nil {
		return model.Resource{}, r.err
	}
	if r.forceResult {
		return r.result, nil
	}
	return r.ResourceRepo.Get(ctx, id)
}

var typedEvidenceNow = time.Date(2035, time.January, 2, 3, 4, 5, 0, time.UTC)

func typedEvidenceContext(t *testing.T, until time.Time) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), until)
	t.Cleanup(cancel)
	return ctx
}

func (f *typedEvidenceFixture) exactEpoch(t *testing.T) store.AuthorizationFactRef {
	t.Helper()
	var fact store.AuthorizationFactRef
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		fact, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read exact authorization epoch: %v", err)
	}
	return fact
}

func (f *typedEvidenceFixture) data(now time.Time, repo *typedEvidencePolicyRepo) *typedEvidenceData {
	return &typedEvidenceData{
		st: f.st,
		wrap: func(sc store.Scope) store.Scope {
			return typedEvidenceScope{
				Scope: sc, now: model.NewTimestamp(now), forceClock: true,
				policies: repo,
			}
		},
	}
}

func typedEvidenceRequest(tenant model.TenantID) auth.Request {
	return auth.Request{
		Tenant:     tenant,
		Permission: "agent:read",
		Principal: auth.Principal{
			Kind: auth.KindUser, CredID: model.NewID(), UserID: model.NewID(),
		},
		Resource: auth.ResourceAttrs{Kind: "agent"},
	}
}

func typedEvidenceABACPolicy(t *testing.T, tenant model.TenantID, rule abacRule) model.Policy {
	t.Helper()
	raw, err := json.Marshal(abacSpec{Rules: []abacRule{rule}})
	if err != nil {
		t.Fatal(err)
	}
	spec, message := canonicalizeABAC(raw)
	if message != "" {
		t.Fatalf("canonicalize ABAC: %s", message)
	}
	return model.Policy{
		BaseFields: model.BaseFields{ID: model.NewID(), TenantID: tenant, Version: 1},
		Name:       "typed evidence", Kind: policyKindABAC, Enabled: true, Spec: spec,
	}
}

func assertTypedPolicyWitness(
	t *testing.T,
	decision auth.PolicyEvidenceDecision,
	fact store.AuthorizationFactRef,
	observedAt, freshUntil time.Time,
) {
	t.Helper()
	if len(decision.Facts) != 1 || decision.Facts[0] != fact {
		t.Fatalf("facts = %#v, want exactly %#v", decision.Facts, fact)
	}
	if !decision.ObservedAt.Equal(observedAt) || !decision.FreshUntil.Equal(freshUntil) {
		t.Fatalf("window = %s..%s, want %s..%s", decision.ObservedAt, decision.FreshUntil, observedAt, freshUntil)
	}
}

func TestNativeABACEvidenceUsesOneUnfilteredCanonicalSnapshot(t *testing.T) {
	t.Run("durable pages, not legacy cache", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		nonMatching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:write"})
		repo := &typedEvidencePolicyRepo{}
		repo.list = func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			if len(q.Filters) != 0 {
				// The policy is deliberately hidden from a filtered caller. A query
				// decorator could otherwise manufacture a false CLEAN absence claim.
				return nil, model.Page{}, nil
			}
			switch q.Cursor {
			case "":
				return []model.Policy{nonMatching}, model.Page{HasMore: true, Cursor: "page-2"}, nil
			case "page-2":
				return []model.Policy{matching}, model.Page{}, nil
			default:
				return nil, model.Page{}, errors.New("unexpected cursor")
			}
		}
		clockCalls, epochCalls := 0, 0
		data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
			return typedEvidenceScope{
				Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
				policies: repo, clockCalls: &clockCalls, epochCalls: &epochCalls,
			}
		}}
		// A cached matching deny would be an invalid provenance source here; the
		// durable pages above must decide the result instead.
		eval := &evaluator{data: data, cache: map[model.TenantID]*compiledSet{
			f.tenant: {rules: []abacRule{{Deny: true, Permission: "agent:write"}}},
		}}
		deadline := typedEvidenceNow.Add(time.Hour)
		decision, err := eval.EvaluateEvidence(typedEvidenceContext(t, deadline), req)
		if err != nil {
			t.Fatalf("EvaluateEvidence: %v", err)
		}
		if decision.ForbidAbsence.Verdict != auth.CheckBroken {
			t.Fatalf("forbid absence = %#v, want established BROKEN durable rule", decision.ForbidAbsence)
		}
		if data.views != 1 || clockCalls != 1 || epochCalls != 1 {
			t.Fatalf("View/clock/epoch calls = %d/%d/%d, want exactly 1/1/1", data.views, clockCalls, epochCalls)
		}
		if len(repo.queries) != 2 || len(repo.queries[0].Filters) != 0 || repo.queries[0].Cursor != "" || repo.queries[1].Cursor != "page-2" {
			t.Fatalf("policy queries = %#v, want complete unfiltered pagination", repo.queries)
		}
		assertTypedPolicyWitness(t, decision, f.exactEpoch(t), typedEvidenceNow, deadline)
	})

	t.Run("complete canonical nonmatch establishes clean", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		nonMatching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:write"})
		repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
			return []model.Policy{nonMatching}, model.Page{}, nil
		}}
		deadline := typedEvidenceNow.Add(time.Hour)
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(typedEvidenceContext(t, deadline), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("canonical nonmatch = %#v, %v; want CLEAN/nil", decision, err)
		}
		assertTypedPolicyWitness(t, decision, f.exactEpoch(t), typedEvidenceNow, deadline)
	})

	t.Run("complete empty policy collection establishes clean", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
			return nil, model.Page{}, nil
		}}
		deadline := typedEvidenceNow.Add(time.Hour)
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(typedEvidenceContext(t, deadline), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("empty canonical collection = %#v, %v; want CLEAN/nil", decision, err)
		}
		assertTypedPolicyWitness(t, decision, f.exactEpoch(t), typedEvidenceNow, deadline)
	})

	t.Run("disabled and non-ABAC durable rows are locally excluded", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		for _, tc := range []struct {
			name   string
			mutate func(*model.Policy)
		}{
			{name: "disabled ABAC", mutate: func(policy *model.Policy) { policy.Enabled = false }},
			{name: "approval kind", mutate: func(policy *model.Policy) { policy.Kind = policyKindApproval }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ignored := matching
				tc.mutate(&ignored)
				repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
					return []model.Policy{ignored}, model.Page{}, nil
				}}
				decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
					typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
				)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckClean {
					t.Fatalf("%s = %#v, %v; want CLEAN/nil", tc.name, decision, err)
				}
			})
		}
	})

	t.Run("matching canonical deny dominates later ordinary outage", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		repo := &typedEvidencePolicyRepo{}
		repo.list = func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			if q.Cursor == "" {
				return []model.Policy{matching}, model.Page{HasMore: true, Cursor: "late"}, nil
			}
			return nil, model.Page{}, errors.New("later policy repository outage")
		}
		eval := &evaluator{data: f.data(typedEvidenceNow, repo)}
		decision, err := eval.EvaluateEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckBroken {
			t.Fatalf("canonical deny + later outage = %#v, %v; want BROKEN/nil", decision, err)
		}
	})

	t.Run("matching canonical deny dominates later malformed nonduplicate row", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		malformed := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:admin"})
		malformed.Spec = map[string]any{"rules": []any{map[string]any{"deny": false}}}
		repo := &typedEvidencePolicyRepo{}
		repo.list = func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			if q.Cursor == "" {
				return []model.Policy{matching}, model.Page{HasMore: true, Cursor: "malformed"}, nil
			}
			return []model.Policy{malformed}, model.Page{}, nil
		}
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
			typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
		)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckBroken {
			t.Fatalf("canonical deny + malformed row = %#v, %v; want BROKEN/nil", decision, err)
		}
	})

	t.Run("nonmatch plus malformed row stays unknown", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		nonMatching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:write"})
		malformed := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:admin"})
		malformed.Spec = map[string]any{"rules": []any{map[string]any{"deny": false}}}
		repo := &typedEvidencePolicyRepo{}
		repo.list = func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			if q.Cursor == "" {
				return []model.Policy{nonMatching}, model.Page{HasMore: true, Cursor: "bad"}, nil
			}
			return []model.Policy{malformed}, model.Page{}, nil
		}
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
			typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
		)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("nonmatch + malformed = %#v, %v; want UNKNOWN without facts", decision, err)
		}
	})

	t.Run("duplicate durable policy identity stays unknown even after match", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		duplicate := matching
		duplicate.Spec = map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "agent:write"}}}
		repo := &typedEvidencePolicyRepo{}
		repo.list = func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			if q.Cursor == "" {
				return []model.Policy{matching}, model.Page{HasMore: true, Cursor: "duplicate"}, nil
			}
			return []model.Policy{duplicate}, model.Page{}, nil
		}
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
			typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
		)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("duplicate policy ID = %#v, %v; want UNKNOWN/nil", decision, err)
		}
	})

	t.Run("duplicate ambiguity is independent of malformed sibling order", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		req := typedEvidenceRequest(f.tenant)
		matching := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: string(req.Permission)})
		malformed := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:admin"})
		malformed.Spec = map[string]any{"rules": []any{map[string]any{"deny": false}}}
		duplicate := matching
		for _, tc := range []struct {
			name string
			page []model.Policy
		}{
			{name: "malformed before duplicate", page: []model.Policy{matching, malformed, duplicate}},
			{name: "duplicate before malformed", page: []model.Policy{matching, duplicate, malformed}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
					return tc.page, model.Page{}, nil
				}}
				decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
					typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
				)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
					t.Fatalf("%s = %#v, %v; want UNKNOWN/no facts", tc.name, decision, err)
				}
			})
		}
	})
}

func TestNativeABACEvidenceRejectsMalformedOrUnboundedWitnesses(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	req := typedEvidenceRequest(f.tenant)
	policy := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:write"})

	t.Run("no context deadline", func(t *testing.T) {
		repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
			return []model.Policy{policy}, model.Page{}, nil
		}}
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(context.Background(), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("no deadline = %#v, %v; want UNKNOWN/nil", decision, err)
		}
	})

	t.Run("clock epoch and finite-window failures never reach policy evidence", func(t *testing.T) {
		validFact := f.exactEpoch(t)
		for _, tc := range []struct {
			name     string
			wrap     func(store.Scope) store.Scope
			deadline time.Time
		}{
			{
				name:     "missing DB clock capability",
				wrap:     func(sc store.Scope) store.Scope { return typedEvidenceNoClockScope{Scope: sc} },
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "DB clock error",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						clockErr: errors.New("clock unavailable"),
					}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "zero DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, forceClock: true}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "missing authorization epoch capability",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceNoEpochScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow)}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "authorization epoch error",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						fact: validFact, forceEpoch: true, epochErr: errors.New("epoch unavailable"),
					}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "zero authorization epoch",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						forceEpoch: true,
					}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "deadline equal to DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true}
				},
				deadline: typedEvidenceNow,
			},
			{
				name: "deadline before DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true}
				},
				deadline: typedEvidenceNow.Add(-time.Nanosecond),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				listCalls := 0
				repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
					listCalls++
					return []model.Policy{policy}, model.Page{}, nil
				}}
				data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
					wrapped := tc.wrap(sc)
					if evidenceScope, ok := wrapped.(typedEvidenceScope); ok {
						evidenceScope.policies = repo
						return evidenceScope
					}
					return wrapped
				}}
				decision, err := (&evaluator{data: data}).EvaluateEvidence(typedEvidenceContext(t, tc.deadline), req)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
					t.Fatalf("%s = %#v, %v; want UNKNOWN/no facts", tc.name, decision, err)
				}
				if listCalls != 0 {
					t.Fatalf("%s listed policies %d times after witness failure", tc.name, listCalls)
				}
			})
		}
	})

	t.Run("bad durable row ID or version", func(t *testing.T) {
		for _, mutate := range []struct {
			name string
			fn   func(*model.Policy)
		}{
			{name: "noncanonical ID", fn: func(p *model.Policy) { p.ID = model.ID("not-a-uuid") }},
			{name: "zero version", fn: func(p *model.Policy) { p.Version = 0 }},
			{name: "foreign tenant", fn: func(p *model.Policy) { p.TenantID = model.NewTenantID() }},
		} {
			t.Run(mutate.name, func(t *testing.T) {
				bad := policy
				mutate.fn(&bad)
				repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
					return []model.Policy{bad}, model.Page{}, nil
				}}
				decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
					typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
				)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
					t.Fatalf("malformed row = %#v, %v; want UNKNOWN/nil", decision, err)
				}
			})
		}
	})

	t.Run("normalizable but noncanonical ABAC bytes are unknown", func(t *testing.T) {
		bad := typedEvidenceABACPolicy(t, f.tenant, abacRule{Deny: true, Permission: "agent:write"})
		bad.Spec = map[string]any{"rules": []any{map[string]any{"deny": true, "permission": " agent:write "}}}
		repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
			return []model.Policy{bad}, model.Page{}, nil
		}}
		decision, err := (&evaluator{data: f.data(typedEvidenceNow, repo)}).EvaluateEvidence(
			typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
		)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("normalizable bytes = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
	})

	t.Run("leased or noncanonical epoch is never provenance", func(t *testing.T) {
		fact := f.exactEpoch(t)
		leased, err := store.NewLeaseFenceAuthorizationFactRef(
			model.AuthorizationEpochKind, model.ID(f.tenant), fact.Version, "subject", 1, model.NewTimestamp(typedEvidenceNow.Add(time.Hour)),
		)
		if err != nil {
			t.Fatal(err)
		}
		if validGovernanceEvidenceEpochFact(model.TenantID("not-a-tenant"), fact) ||
			validGovernanceEvidenceEpochFact(f.tenant, leased) {
			t.Fatal("malformed/leased authorization epoch fact was accepted")
		}
		repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
			return []model.Policy{policy}, model.Page{}, nil
		}}
		data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
			return typedEvidenceScope{
				Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
				fact: leased, forceEpoch: true, policies: repo,
			}
		}}
		decision, callErr := (&evaluator{data: data}).EvaluateEvidence(
			typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
		)
		if callErr != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("leased epoch = %#v, %v; want UNKNOWN/nil", decision, callErr)
		}
	})

	t.Run("every authorization epoch coordinate is exact", func(t *testing.T) {
		validFact := f.exactEpoch(t)
		for _, tc := range []struct {
			name   string
			mutate func(*store.AuthorizationFactRef)
		}{
			{name: "wrong fact kind", mutate: func(fact *store.AuthorizationFactRef) { fact.Kind = model.Kind("governance.other_epoch") }},
			{name: "wrong fact ID", mutate: func(fact *store.AuthorizationFactRef) { fact.ID = model.NewID() }},
			{name: "zero fact version", mutate: func(fact *store.AuthorizationFactRef) { fact.Version = 0 }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				badFact := validFact
				tc.mutate(&badFact)
				repo := &typedEvidencePolicyRepo{list: func(context.Context, model.Query) ([]model.Policy, model.Page, error) {
					return []model.Policy{policy}, model.Page{}, nil
				}}
				data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						fact: badFact, forceEpoch: true, policies: repo,
					}
				}}
				decision, err := (&evaluator{data: data}).EvaluateEvidence(
					typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), req,
				)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
					t.Fatalf("%s = %#v, %v; want UNKNOWN/no facts", tc.name, decision, err)
				}
			})
		}
	})
}

type typedEvidenceMember struct {
	decision      auth.PolicyEvidenceDecision
	err           error
	panicEvidence bool
	evaluateCalls int
	evidenceCalls int
}

func (m *typedEvidenceMember) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	m.evaluateCalls++
	return auth.Decision{Allow: false, Reason: "legacy path must not run"}, nil
}

func (m *typedEvidenceMember) EvaluateEvidence(context.Context, auth.Request) (auth.PolicyEvidenceDecision, error) {
	m.evidenceCalls++
	if m.panicEvidence {
		panic("typed evidence panic")
	}
	return m.decision, m.err
}

type typedEvidenceLegacyMember struct{ evaluateCalls int }

func (m *typedEvidenceLegacyMember) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	m.evaluateCalls++
	return auth.Decision{Allow: false, Reason: "legacy path must not run"}, nil
}

type typedEvidenceOPAProbe struct{ calls int }

func (p *typedEvidenceOPAProbe) Do(*http.Request) (*http.Response, error) {
	p.calls++
	return nil, errors.New("legacy OPA Evaluate must not run")
}

func typedEvidenceFact() store.AuthorizationFactRef {
	return store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: model.NewID(), Version: 1}
}

func typedEvidencePolicyDecision(verdict auth.CheckVerdict, fact store.AuthorizationFactRef, from, until time.Time) auth.PolicyEvidenceDecision {
	code := "typed_clean"
	if verdict == auth.CheckBroken {
		code = "typed_broken"
	}
	return auth.PolicyEvidenceDecision{
		ForbidAbsence: auth.CheckEvidence{Verdict: verdict, Code: code},
		Facts:         []store.AuthorizationFactRef{fact},
		ObservedAt:    from,
		FreshUntil:    until,
	}
}

func typedEvidenceScopedEngine(
	t *testing.T,
	f *typedEvidenceFixture,
	data *typedEvidenceData,
	source string,
	bound time.Duration,
	freshness FreshnessRecord,
) (*scopedEngine, store.AuthorizationFactRef) {
	t.Helper()
	fact := f.exactEpoch(t)
	state := scopedTenantState{
		generation:     fact,
		available:      true,
		freshnessValid: true,
		freshness:      freshness,
	}
	if source != "" {
		set, err := compileGrantSet(source)
		if err != nil {
			t.Fatalf("compile scoped evidence source: %v", err)
		}
		state.set = set
		state.selection = activationID{authored: 1}
		state.authoredDigest = contentDigest(source)
		state.unionDigest = contentDigest(source)
	}
	engine := &scopedEngine{resolver: &scopeResolver{data: data}, maxStaleness: bound}
	if _, err := engine.installIfNotOlder(f.tenant, state); err != nil {
		t.Fatalf("install scoped evidence state: %v", err)
	}
	return engine, fact
}

func (f *typedEvidenceFixture) confinedPrincipal(t *testing.T, workspace model.ID) auth.Principal {
	t.Helper()
	ctx := context.Background()
	authn := auth.NewAuthenticator(f.st, nil)
	if _, err := authn.BootstrapSuperadmin(ctx, "typed-evidence-admin@example.test", "strong-password-1"); err != nil {
		t.Fatalf("bootstrap evidence superadmin: %v", err)
	}
	token, _, err := authn.Login(ctx, "typed-evidence-admin@example.test", "strong-password-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("login evidence superadmin: %v", err)
	}
	admin, err := authn.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate evidence superadmin: %v", err)
	}
	user, err := authn.CreateUser(ctx, admin, auth.NewUser{
		Email: "typed-evidence-confined@example.test", DisplayName: "Confined", Password: "strong-password-2",
	})
	if err != nil {
		t.Fatalf("create confined user: %v", err)
	}
	if _, err := authn.GrantMembership(ctx, admin, user.ID, f.tenant, auth.RoleEditor, workspace); err != nil {
		t.Fatalf("grant confined membership: %v", err)
	}
	principal, found, err := authn.PrincipalForUser(ctx, user.ID.String(), auth.AAL3)
	if err != nil || !found {
		t.Fatalf("confined principal = found:%v err:%v", found, err)
	}
	return principal
}

func assertTypedScopedWitness(
	t *testing.T,
	decision auth.ScopedEvidenceDecision,
	fact store.AuthorizationFactRef,
	observedAt, freshUntil time.Time,
) {
	t.Helper()
	if len(decision.Facts) != 1 || decision.Facts[0] != fact {
		t.Fatalf("scoped facts = %#v, want %#v", decision.Facts, fact)
	}
	if !decision.ObservedAt.Equal(observedAt) || !decision.FreshUntil.Equal(freshUntil) {
		t.Fatalf("scoped window = %s..%s, want %s..%s", decision.ObservedAt, decision.FreshUntil, observedAt, freshUntil)
	}
}

func TestChainEvidencePreservesIndependentBrokenWitnesses(t *testing.T) {
	req := typedEvidenceRequest(model.NewTenantID())
	brokenFact := typedEvidenceFact()
	cleanFact := typedEvidenceFact()
	broken := &typedEvidenceMember{decision: typedEvidencePolicyDecision(
		auth.CheckBroken, brokenFact, typedEvidenceNow, typedEvidenceNow.Add(10*time.Minute),
	)}
	clean := &typedEvidenceMember{decision: typedEvidencePolicyDecision(
		auth.CheckClean, cleanFact, typedEvidenceNow.Add(time.Minute), typedEvidenceNow.Add(time.Hour),
	)}
	legacy := &typedEvidenceLegacyMember{}
	chain := &chainEvaluator{members: []auth.PolicyEvaluator{legacy, clean, broken}, onDeny: func(auth.Request, auth.Decision) {
		t.Fatal("evidence must not invoke legacy onDeny")
	}}
	decision, err := chain.EvaluateEvidence(context.Background(), req)
	if err != nil || decision.ForbidAbsence.Verdict != auth.CheckBroken {
		t.Fatalf("legacy+clean+broken = %#v, %v; want BROKEN/nil", decision, err)
	}
	if legacy.evaluateCalls != 0 || clean.evaluateCalls != 0 || broken.evaluateCalls != 0 {
		t.Fatalf("legacy Evaluate calls = legacy:%d clean:%d broken:%d, want zero", legacy.evaluateCalls, clean.evaluateCalls, broken.evaluateCalls)
	}
	if clean.evidenceCalls != 1 || broken.evidenceCalls != 1 {
		t.Fatalf("typed evidence calls = clean:%d broken:%d, want one each", clean.evidenceCalls, broken.evidenceCalls)
	}
	// A BROKEN fact/window must stand on its own; borrowing cleanFact's later
	// horizon would let a malformed/legacy member extend a denial witness.
	assertTypedPolicyWitness(t, decision, brokenFact, typedEvidenceNow, typedEvidenceNow.Add(10*time.Minute))

	t.Run("broken dominates panic and error in either order", func(t *testing.T) {
		for _, members := range [][]auth.PolicyEvaluator{
			{&typedEvidenceMember{panicEvidence: true}, broken},
			{&typedEvidenceMember{err: errors.New("typed evidence failed")}, broken},
			{broken, &typedEvidenceLegacyMember{}},
		} {
			decision, err := (&chainEvaluator{members: members}).EvaluateEvidence(context.Background(), req)
			if err != nil || decision.ForbidAbsence.Verdict != auth.CheckBroken {
				t.Fatalf("members %#v = %#v, %v; want BROKEN/nil", members, decision, err)
			}
		}
	})

	t.Run("all clean unions facts and intersects windows", func(t *testing.T) {
		decision, err := (&chainEvaluator{members: []auth.PolicyEvaluator{clean, &typedEvidenceMember{decision: typedEvidencePolicyDecision(
			auth.CheckClean, brokenFact, typedEvidenceNow.Add(2*time.Minute), typedEvidenceNow.Add(20*time.Minute),
		)}}}).EvaluateEvidence(context.Background(), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("all clean = %#v, %v; want CLEAN/nil", decision, err)
		}
		if len(decision.Facts) != 2 || !decision.ObservedAt.Equal(typedEvidenceNow.Add(2*time.Minute)) ||
			!decision.FreshUntil.Equal(typedEvidenceNow.Add(20*time.Minute)) {
			t.Fatalf("all-clean fold = %#v, want union/intersection", decision)
		}
	})

	t.Run("malformed window cannot be laundered by clean member", func(t *testing.T) {
		bad := typedEvidencePolicyDecision(auth.CheckClean, typedEvidenceFact(), typedEvidenceNow, typedEvidenceNow)
		decision, err := (&chainEvaluator{members: []auth.PolicyEvaluator{clean, &typedEvidenceMember{decision: bad}}}).EvaluateEvidence(context.Background(), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("malformed contribution = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
	})

	t.Run("same fact identity with conflicting versions is unknown", func(t *testing.T) {
		shared := typedEvidenceFact()
		newer := shared
		newer.Version++
		for _, tc := range []struct {
			name    string
			verdict auth.CheckVerdict
		}{
			{name: "clean contributors", verdict: auth.CheckClean},
			{name: "broken contributors", verdict: auth.CheckBroken},
		} {
			t.Run(tc.name, func(t *testing.T) {
				first := &typedEvidenceMember{decision: typedEvidencePolicyDecision(tc.verdict, shared, typedEvidenceNow, typedEvidenceNow.Add(time.Hour))}
				second := &typedEvidenceMember{decision: typedEvidencePolicyDecision(tc.verdict, newer, typedEvidenceNow, typedEvidenceNow.Add(time.Hour))}
				decision, err := (&chainEvaluator{members: []auth.PolicyEvaluator{first, second}}).EvaluateEvidence(context.Background(), req)
				if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
					t.Fatalf("%s = %#v, %v; want UNKNOWN/no facts", tc.name, decision, err)
				}
			})
		}
	})

	t.Run("actual legacy OPA and static Cedar remain unknown without Evaluate", func(t *testing.T) {
		staticCedar, err := NewCedarEvaluator(`forbid(principal, action, resource);`, nil)
		if err != nil {
			t.Fatal(err)
		}
		opaProbe := &typedEvidenceOPAProbe{}
		opa, err := NewOPAEvaluator("http://127.0.0.1:1", "authz.allow", "", opaProbe)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range []auth.PolicyEvaluator{staticCedar, opa} {
			decision, callErr := (&chainEvaluator{members: []auth.PolicyEvaluator{clean, member}}).EvaluateEvidence(context.Background(), req)
			if callErr != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
				t.Fatalf("legacy %T = %#v, %v; want UNKNOWN/no facts without external Evaluate", member, decision, callErr)
			}
		}
		if opaProbe.calls != 0 {
			t.Fatalf("legacy OPA Evaluate called transport %d times", opaProbe.calls)
		}
	})

	t.Run("typed nil member is unknown and cannot launder clean", func(t *testing.T) {
		var nilMember *typedEvidenceMember
		decision, err := (&chainEvaluator{members: []auth.PolicyEvaluator{clean, nilMember}}).EvaluateEvidence(context.Background(), req)
		if err != nil || decision.ForbidAbsence.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("clean + typed nil = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
	})
}

func TestScopedEvidenceEmptySnapshotAndRestrictViewAreClean(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	resourceProbe := &typedEvidenceResourceRepo{err: errors.New("lineage must not be read for empty snapshot")}
	data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
		return typedEvidenceScope{
			Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
			resources: resourceProbe,
		}
	}}
	engine, fact := typedEvidenceScopedEngine(t, f, data, "", 0, FreshnessRecord{})
	req := typedEvidenceRequest(f.tenant)
	// An entity-shaped target makes an accidental readScope call observable. The
	// complete empty union must answer before touching volatile resource lineage.
	req.Resource = auth.ResourceAttrs{Kind: "resource", ID: model.NewID().String()}
	deadline := typedEvidenceNow.Add(time.Hour)
	ctx := typedEvidenceContext(t, deadline)

	scoped, err := engine.ScopedEvidence(ctx, req)
	if err != nil {
		t.Fatalf("ScopedEvidence empty state: %v", err)
	}
	if scoped.Effect != auth.EffectAbstain || scoped.ResourceGuard.Verdict != auth.CheckClean ||
		scoped.ForbidAbsence.Verdict != auth.CheckClean {
		t.Fatalf("empty scoped evidence = %#v, want ABSTAIN/CLEAN/CLEAN", scoped)
	}
	if resourceProbe.gets != 0 {
		t.Fatalf("empty snapshot read resource lineage %d times", resourceProbe.gets)
	}
	if data.views != 1 {
		t.Fatalf("ScopedEvidence opened %d Views, want exactly one", data.views)
	}
	assertTypedScopedWitness(t, scoped, fact, typedEvidenceNow, deadline)

	policy, err := engine.EvaluateEvidence(ctx, req)
	if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
		t.Fatalf("empty restrict evidence = %#v, %v; want CLEAN/nil", policy, err)
	}
	if data.views != 2 {
		t.Fatalf("Scoped EvaluateEvidence opened %d total Views, want one per contribution", data.views)
	}
	assertTypedPolicyWitness(t, policy, fact, typedEvidenceNow, deadline)
}

func TestScopedEvidenceWindowAndRuntimeBracket(t *testing.T) {
	t.Run("deployment bound caps DB evidence window", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		anchor := typedEvidenceNow.Add(-5 * time.Minute)
		engine, fact := typedEvidenceScopedEngine(t, f, data,
			`forbid(principal, action == Action::"agent:write", resource);`, 10*time.Minute,
			FreshnessRecord{RefreshedAt: anchor},
		)
		req := typedEvidenceRequest(f.tenant)
		ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
		decision, err := engine.ScopedEvidence(ctx, req)
		if err != nil || decision.ResourceGuard.Verdict != auth.CheckClean || decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("bounded scoped evidence = %#v, %v; want CLEAN/CLEAN", decision, err)
		}
		assertTypedScopedWitness(t, decision, fact, typedEvidenceNow, anchor.Add(10*time.Minute))

		policy, err := engine.EvaluateEvidence(ctx, req)
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("bounded restrict evidence = %#v, %v; want CLEAN/nil", policy, err)
		}
		assertTypedPolicyWitness(t, policy, fact, typedEvidenceNow, anchor.Add(10*time.Minute))
	})

	t.Run("bound without durable freshness anchor is unknown", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		engine, _ := typedEvidenceScopedEngine(t, f, data, `permit(principal, action, resource);`, time.Minute, FreshnessRecord{})
		decision, err := engine.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ResourceGuard.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("missing bound anchor = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
	})

	t.Run("same generation token replay during View is unknown", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		engine, _ := typedEvidenceScopedEngine(t, f, data, `forbid(principal, action, resource);`, 0, FreshnessRecord{})
		data.afterView = func() {
			state, loaded := engine.tenantState(f.tenant)
			if _, err := engine.installIfNotOlderFromObservedState(f.tenant, state, loaded, state); err != nil {
				t.Errorf("replay runtime state: %v", err)
			}
		}
		decision, err := engine.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
		if err != nil || decision.ResourceGuard.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("replayed operation = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
		policy, policyErr := engine.EvaluateEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
		if policyErr != nil || policy.ForbidAbsence.Verdict != auth.CheckUnknown || len(policy.Facts) != 0 {
			t.Fatalf("replayed restrict operation = %#v, %v; want UNKNOWN/no facts", policy, policyErr)
		}
	})

	t.Run("epoch mismatch and wrong scope tenant are unknown", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		for _, mutate := range []struct {
			name string
			wrap func(store.Scope, store.AuthorizationFactRef) store.Scope
		}{
			{
				name: "epoch mismatch",
				wrap: func(sc store.Scope, fact store.AuthorizationFactRef) store.Scope {
					fact.Version++
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true, fact: fact, forceEpoch: true}
				},
			},
			{
				name: "decorated wrong tenant",
				wrap: func(sc store.Scope, _ store.AuthorizationFactRef) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true, tenant: model.NewTenantID(), forceTenant: true}
				},
			},
		} {
			t.Run(mutate.name, func(t *testing.T) {
				data := &typedEvidenceData{st: f.st}
				engine, fact := typedEvidenceScopedEngine(t, f, data, `forbid(principal, action, resource);`, 0, FreshnessRecord{})
				data.wrap = func(sc store.Scope) store.Scope { return mutate.wrap(sc, fact) }
				decision, err := engine.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
				if err != nil || decision.ResourceGuard.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
					t.Fatalf("%s = %#v, %v; want UNKNOWN/no facts", mutate.name, decision, err)
				}
				policy, policyErr := engine.EvaluateEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
				if policyErr != nil || policy.ForbidAbsence.Verdict != auth.CheckUnknown || len(policy.Facts) != 0 {
					t.Fatalf("restrict %s = %#v, %v; want UNKNOWN/no facts", mutate.name, policy, policyErr)
				}
			})
		}
	})
}

func TestScopedEvidenceRejectsMalformedRuntimeSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*scopedTenantState)
	}{
		{
			name:   "compiled set absent with nonempty union digest",
			mutate: func(state *scopedTenantState) { state.set = nil },
		},
		{
			name:   "compiled source digest mismatch",
			mutate: func(state *scopedTenantState) { state.unionDigest = contentDigest("different compiled source") },
		},
		{
			name:   "runtime unavailable",
			mutate: func(state *scopedTenantState) { state.available = false },
		},
		{
			name:   "incomplete durable identity",
			mutate: func(state *scopedTenantState) { state.identityIncomplete = true },
		},
		{
			name:   "missing runtime operation token",
			mutate: func(state *scopedTenantState) { state.operation = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTypedEvidenceFixture(t)
			data := f.data(typedEvidenceNow, nil)
			engine, _ := typedEvidenceScopedEngine(t, f, data, `forbid(principal, action, resource);`, 0, FreshnessRecord{})
			engine.mu.Lock()
			state, loaded := engine.tenants[f.tenant]
			if !loaded {
				engine.mu.Unlock()
				t.Fatal("valid runtime state was not installed")
			}
			tc.mutate(&state)
			engine.tenants[f.tenant] = state
			engine.mu.Unlock()

			ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
			req := typedEvidenceRequest(f.tenant)
			scoped, scopedErr := engine.ScopedEvidence(ctx, req)
			if scopedErr != nil || scoped.ResourceGuard.Verdict != auth.CheckUnknown || len(scoped.Facts) != 0 {
				t.Fatalf("scoped %s = %#v, %v; want UNKNOWN/no facts", tc.name, scoped, scopedErr)
			}
			policy, policyErr := engine.EvaluateEvidence(ctx, req)
			if policyErr != nil || policy.ForbidAbsence.Verdict != auth.CheckUnknown || len(policy.Facts) != 0 {
				t.Fatalf("restrict %s = %#v, %v; want UNKNOWN/no facts", tc.name, policy, policyErr)
			}
		})

	}
}

func TestScopedEvidenceDiagnosticsAndSignedDeadline(t *testing.T) {
	t.Run("explicit forbid reason dominates unrelated permit diagnostic", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		source := `
forbid(principal, action, resource);
permit(principal, action, resource) when { resource.owner == "alice" };
`
		engine, fact := typedEvidenceScopedEngine(t, f, data, source, 0, FreshnessRecord{})
		ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
		req := typedEvidenceRequest(f.tenant)
		scoped, err := engine.ScopedEvidence(ctx, req)
		if err != nil || scoped.Effect != auth.EffectForbid || scoped.ForbidAbsence.Verdict != auth.CheckBroken {
			t.Fatalf("forbid + permit diagnostic scoped = %#v, %v; want FORBID/BROKEN", scoped, err)
		}
		assertTypedScopedWitness(t, scoped, fact, typedEvidenceNow, typedEvidenceNow.Add(time.Hour))
		policy, err := engine.EvaluateEvidence(ctx, req)
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckBroken {
			t.Fatalf("forbid + permit diagnostic restrict = %#v, %v; want BROKEN", policy, err)
		}
	})

	t.Run("errored forbid with permit is unknown, not fabricated broken", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		source := `
permit(principal, action, resource);
forbid(principal, action, resource) when { resource.owner == "alice" };
`
		engine, _ := typedEvidenceScopedEngine(t, f, data, source, 0, FreshnessRecord{})
		ctx := typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour))
		req := typedEvidenceRequest(f.tenant)
		for _, result := range []struct {
			name string
			call func() (auth.CheckEvidence, int, error)
		}{
			{
				name: "scoped", call: func() (auth.CheckEvidence, int, error) {
					decision, err := engine.ScopedEvidence(ctx, req)
					return decision.ForbidAbsence, len(decision.Facts), err
				},
			},
			{
				name: "restrict", call: func() (auth.CheckEvidence, int, error) {
					decision, err := engine.EvaluateEvidence(ctx, req)
					return decision.ForbidAbsence, len(decision.Facts), err
				},
			},
		} {
			t.Run(result.name, func(t *testing.T) {
				check, facts, err := result.call()
				if err != nil || check.Verdict != auth.CheckUnknown || facts != 0 {
					t.Fatalf("errored forbid = %#v facts:%d err:%v; want UNKNOWN/no facts", check, facts, err)
				}
			})
		}
	})

	t.Run("signed DDIL tuple caps both scoped evidence producers", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		data := f.data(typedEvidenceNow, nil)
		fact := f.exactEpoch(t)
		source := `forbid(principal, action == Action::"agent:write", resource);`
		set, err := compileGrantSet(source)
		if err != nil {
			t.Fatal(err)
		}
		anchor := typedEvidenceNow.Add(-time.Minute)
		state := scopedTenantState{
			set: set, selection: activationID{adopted: 1}, generation: fact,
			adoptedDigest: contentDigest(source), unionDigest: contentDigest(source),
			freshness: FreshnessRecord{
				// A signed tenant override is authoritative even when it is larger
				// than the deployment default. This catches an accidental min(bound)
				// implementation that would shorten the evidence window to one hour.
				RefreshedAt: anchor, MaxStaleness: 2 * time.Hour,
				AdoptedRevision: contentDigest(source), AdoptedCreatedAt: anchor,
			},
			available: true, freshnessValid: true,
		}
		engine := &scopedEngine{resolver: &scopeResolver{data: data}, maxStaleness: time.Hour}
		if _, err := engine.installIfNotOlder(f.tenant, state); err != nil {
			t.Fatal(err)
		}
		ctx := typedEvidenceContext(t, typedEvidenceNow.Add(3*time.Hour))
		req := typedEvidenceRequest(f.tenant)
		scoped, err := engine.ScopedEvidence(ctx, req)
		if err != nil || scoped.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("signed scoped evidence = %#v, %v", scoped, err)
		}
		assertTypedScopedWitness(t, scoped, fact, typedEvidenceNow, anchor.Add(2*time.Hour))
		policy, err := engine.EvaluateEvidence(ctx, req)
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("signed restrict evidence = %#v, %v", policy, err)
		}
		assertTypedPolicyWitness(t, policy, fact, typedEvidenceNow, anchor.Add(2*time.Hour))

		// The caller's finite deadline remains the primary horizon when it arrives
		// before a valid signed tenant cap. Evidence may be shortened by DDIL, never
		// extended past the request's own deadline.
		callerDeadline := typedEvidenceNow.Add(30 * time.Minute)
		callerCtx := typedEvidenceContext(t, callerDeadline)
		scoped, err = engine.ScopedEvidence(callerCtx, req)
		if err != nil || scoped.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("caller-capped scoped evidence = %#v, %v", scoped, err)
		}
		assertTypedScopedWitness(t, scoped, fact, typedEvidenceNow, callerDeadline)
		policy, err = engine.EvaluateEvidence(callerCtx, req)
		if err != nil || policy.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("caller-capped restrict evidence = %#v, %v", policy, err)
		}
		assertTypedPolicyWitness(t, policy, fact, typedEvidenceNow, callerDeadline)

		for _, tc := range []struct {
			name   string
			mutate func(*scopedTenantState)
		}{
			{
				name:   "adopted digest differs from signed revision",
				mutate: func(s *scopedTenantState) { s.freshness.AdoptedRevision = contentDigest("different") },
			},
			{
				name:   "adopted selection missing while tuple remains",
				mutate: func(s *scopedTenantState) { s.selection.adopted = 0 },
			},
			{
				name:   "adopted digest missing while selection remains",
				mutate: func(s *scopedTenantState) { s.adoptedDigest = "" },
			},
			{
				name:   "signed clock differs from DB freshness anchor",
				mutate: func(s *scopedTenantState) { s.freshness.AdoptedCreatedAt = anchor.Add(time.Second) },
			},
			{
				name: "unsigned tenant bound",
				mutate: func(s *scopedTenantState) {
					s.selection = activationID{authored: 1}
					s.adoptedDigest = ""
					s.freshness = FreshnessRecord{RefreshedAt: anchor, MaxStaleness: time.Minute}
				},
			},
			{
				name:   "selected bounded policy lacks valid loaded freshness",
				mutate: func(s *scopedTenantState) { s.freshnessValid = false },
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := state
				tc.mutate(&bad)
				if _, err := governanceEvidenceScopedDeadline(bad, time.Hour); err == nil && bad.freshnessValid {
					t.Fatalf("%s supplied a durable deadline", tc.name)
				}
				badEngine := &scopedEngine{resolver: &scopeResolver{data: data}, maxStaleness: time.Hour}
				if _, err := badEngine.installIfNotOlder(f.tenant, bad); err != nil {
					t.Fatalf("install %s state: %v", tc.name, err)
				}
				for _, result := range []struct {
					name string
					call func() (auth.CheckEvidence, int, error)
				}{
					{
						name: "scoped", call: func() (auth.CheckEvidence, int, error) {
							decision, callErr := badEngine.ScopedEvidence(ctx, req)
							return decision.ResourceGuard, len(decision.Facts), callErr
						},
					},
					{
						name: "restrict", call: func() (auth.CheckEvidence, int, error) {
							decision, callErr := badEngine.EvaluateEvidence(ctx, req)
							return decision.ForbidAbsence, len(decision.Facts), callErr
						},
					},
				} {
					t.Run(result.name, func(t *testing.T) {
						check, facts, callErr := result.call()
						if callErr != nil || check.Verdict != auth.CheckUnknown || facts != 0 {
							t.Fatalf("%s %s = %#v facts:%d err:%v; want UNKNOWN/no facts", tc.name, result.name, check, facts, callErr)
						}
					})
				}
			})
		}

		t.Run("expired signed tenant deadline is unavailable", func(t *testing.T) {
			expired := state
			expired.freshness.RefreshedAt = typedEvidenceNow.Add(-10 * time.Minute)
			expired.freshness.AdoptedCreatedAt = expired.freshness.RefreshedAt
			expired.freshness.MaxStaleness = 5 * time.Minute
			expiredEngine := &scopedEngine{resolver: &scopeResolver{data: data}, maxStaleness: time.Hour}
			if _, err := expiredEngine.installIfNotOlder(f.tenant, expired); err != nil {
				t.Fatal(err)
			}
			decision, callErr := expiredEngine.ScopedEvidence(ctx, req)
			if callErr != nil || decision.ResourceGuard.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
				t.Fatalf("expired signed deadline = %#v, %v; want UNKNOWN/no facts", decision, callErr)
			}
		})
	})

	t.Run("scoped clock epoch and finite-window failures are unknown in both seams", func(t *testing.T) {
		f := newTypedEvidenceFixture(t)
		validFact := f.exactEpoch(t)
		for _, tc := range []struct {
			name     string
			wrap     func(store.Scope) store.Scope
			deadline time.Time
		}{
			{
				name:     "missing DB clock capability",
				wrap:     func(sc store.Scope) store.Scope { return typedEvidenceNoClockScope{Scope: sc} },
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "DB clock error with otherwise valid time",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						clockErr: errors.New("clock unavailable"),
					}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "zero DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, forceClock: true}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "missing authorization epoch capability",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceNoEpochScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow)}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "authorization epoch error with otherwise valid fact",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{
						Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
						fact: validFact, forceEpoch: true, epochErr: errors.New("epoch unavailable"),
					}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "zero authorization epoch",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true, forceEpoch: true}
				},
				deadline: typedEvidenceNow.Add(time.Hour),
			},
			{
				name: "deadline equal to DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true}
				},
				deadline: typedEvidenceNow,
			},
			{
				name: "deadline before DB clock",
				wrap: func(sc store.Scope) store.Scope {
					return typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true}
				},
				deadline: typedEvidenceNow.Add(-time.Nanosecond),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				data := &typedEvidenceData{st: f.st, wrap: tc.wrap}
				engine, _ := typedEvidenceScopedEngine(t, f, data, "", 0, FreshnessRecord{})
				req := typedEvidenceRequest(f.tenant)
				scoped, scopedErr := engine.ScopedEvidence(typedEvidenceContext(t, tc.deadline), req)
				if scopedErr != nil || scoped.ResourceGuard.Verdict != auth.CheckUnknown || len(scoped.Facts) != 0 {
					t.Fatalf("scoped %s = %#v, %v; want UNKNOWN/no facts", tc.name, scoped, scopedErr)
				}
				policy, policyErr := engine.EvaluateEvidence(typedEvidenceContext(t, tc.deadline), req)
				if policyErr != nil || policy.ForbidAbsence.Verdict != auth.CheckUnknown || len(policy.Facts) != 0 {
					t.Fatalf("restrict %s = %#v, %v; want UNKNOWN/no facts", tc.name, policy, policyErr)
				}
			})
		}
		var nilScoped *scopedEngine
		nilDecision, nilErr := nilScoped.ScopedEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
		if nilErr != nil || nilDecision.ResourceGuard.Verdict != auth.CheckUnknown {
			t.Fatalf("typed nil scoped = %#v, %v; want UNKNOWN", nilDecision, nilErr)
		}
		var nilNative *evaluator
		nilPolicy, nilPolicyErr := nilNative.EvaluateEvidence(typedEvidenceContext(t, typedEvidenceNow.Add(time.Hour)), typedEvidenceRequest(f.tenant))
		if nilPolicyErr != nil || nilPolicy.ForbidAbsence.Verdict != auth.CheckUnknown {
			t.Fatalf("typed nil native = %#v, %v; want UNKNOWN", nilPolicy, nilPolicyErr)
		}
	})
}

func TestScopedEvidenceS399AndLineageClassification(t *testing.T) {
	f := newTypedEvidenceFixture(t)
	workspace := model.NewID()
	principal := f.confinedPrincipal(t, workspace)
	data := f.data(typedEvidenceNow, nil)
	engine, fact := typedEvidenceScopedEngine(t, f, data, "", 0, FreshnessRecord{})
	deadline := typedEvidenceNow.Add(time.Hour)

	for _, tc := range []struct {
		name       string
		permission auth.Permission
		resource   auth.ResourceAttrs
		wantEffect auth.Effect
		wantGuard  auth.CheckVerdict
	}{
		{
			name: "declared own workspace clean without store lookup", permission: "agent:read",
			resource: auth.ResourceAttrs{Kind: "agent", WorkspaceID: workspace}, wantEffect: auth.EffectAbstain, wantGuard: auth.CheckClean,
		},
		{
			name: "declared foreign workspace broken", permission: "agent:read",
			resource: auth.ResourceAttrs{Kind: "agent", WorkspaceID: model.NewID()}, wantEffect: auth.EffectForbid, wantGuard: auth.CheckBroken,
		},
		{
			name: "indeterminate write broken", permission: "agent:write",
			resource: auth.ResourceAttrs{Kind: "agent"}, wantEffect: auth.EffectForbid, wantGuard: auth.CheckBroken,
		},
		{
			name: "indeterminate recon read broken", permission: "accessgraph:read",
			resource: auth.ResourceAttrs{Kind: "accessgraph"}, wantEffect: auth.EffectForbid, wantGuard: auth.CheckBroken,
		},
		{
			name: "indeterminate ordinary read clean", permission: "agent:read",
			resource: auth.ResourceAttrs{Kind: "agent"}, wantEffect: auth.EffectAbstain, wantGuard: auth.CheckClean,
		},
		{
			name: "missing tree entity read is unknown after lineage lookup", permission: "agent:read",
			resource: auth.ResourceAttrs{Kind: "agent", ID: model.NewID().String()}, wantEffect: auth.EffectAbstain, wantGuard: auth.CheckUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := typedEvidenceRequest(f.tenant)
			req.Principal = principal
			req.Permission, req.Resource = tc.permission, tc.resource
			viewsBefore := data.views
			decision, err := engine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
			if err != nil || decision.Effect != tc.wantEffect || decision.ResourceGuard.Verdict != tc.wantGuard {
				t.Fatalf("scoped evidence decision = %#v, %v; want effect:%v guard:%v", decision, err, tc.wantEffect, tc.wantGuard)
			}
			if data.views != viewsBefore+1 {
				t.Fatalf("scoped evidence decision used %d Views, want exactly one", data.views-viewsBefore)
			}
			if tc.wantGuard != auth.CheckBroken && decision.ForbidAbsence.Verdict != auth.CheckClean {
				t.Fatalf("scoped evidence clean/abstain forbid check = %#v, want CLEAN", decision.ForbidAbsence)
			}
			assertTypedScopedWitness(t, decision, fact, typedEvidenceNow, deadline)
		})
	}

	t.Run("tree lookup same workspace is unknown; mismatch is broken", func(t *testing.T) {
		var own, foreign model.Agent
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			var err error
			own, err = sc.Agents().Create(context.Background(), model.Agent{Name: "own", Kind: "test", ExternalID: "own", Status: model.StatusActive, WorkspaceID: workspace})
			if err != nil {
				return err
			}
			foreign, err = sc.Agents().Create(context.Background(), model.Agent{Name: "foreign", Kind: "test", ExternalID: "foreign", Status: model.StatusActive, WorkspaceID: model.NewID()})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			id   model.ID
			want auth.CheckVerdict
		}{
			{name: "same", id: own.ID, want: auth.CheckUnknown},
			{name: "foreign", id: foreign.ID, want: auth.CheckBroken},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := typedEvidenceRequest(f.tenant)
				req.Principal = principal
				req.Resource = auth.ResourceAttrs{Kind: "agent", ID: tc.id.String()}
				viewsBefore := data.views
				decision, err := engine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
				if err != nil || decision.ResourceGuard.Verdict != tc.want {
					t.Fatalf("tree %s = %#v, %v; want guard %v", tc.name, decision, err, tc.want)
				}
				if data.views != viewsBefore+1 {
					t.Fatalf("tree scoped evidence decision used %d Views, want exactly one", data.views-viewsBefore)
				}
			})
		}
	})

	t.Run("all tree kinds classify the same in the one enclosing View", func(t *testing.T) {
		for _, kind := range []struct {
			name      string
			resourceK string
			configure func(*typedEvidenceScope, model.ID)
		}{
			{
				name: "agent", resourceK: "agent",
				configure: func(scope *typedEvidenceScope, target model.ID) {
					scope.agents = &typedEvidenceGetRepo[model.Agent]{get: func(context.Context, model.ID) (model.Agent, error) {
						return model.Agent{WorkspaceID: target}, nil
					}}
				},
			},
			{
				name: "session", resourceK: "session",
				configure: func(scope *typedEvidenceScope, target model.ID) {
					scope.sessions = &typedEvidenceGetRepo[model.Session]{get: func(context.Context, model.ID) (model.Session, error) {
						return model.Session{WorkspaceID: target}, nil
					}}
				},
			},
			{
				name: "resource", resourceK: "resource",
				configure: func(scope *typedEvidenceScope, target model.ID) {
					scope.resources = &typedEvidenceResourceRepo{forceResult: true, result: model.Resource{WorkspaceID: target}}
				},
			},
			{
				name: "agent group", resourceK: "agent_group",
				configure: func(scope *typedEvidenceScope, target model.ID) {
					scope.agentGroups = &typedEvidenceGetRepo[model.AgentGroup]{get: func(context.Context, model.ID) (model.AgentGroup, error) {
						return model.AgentGroup{WorkspaceID: target}, nil
					}}
				},
			},
		} {
			for _, target := range []struct {
				name string
				id   model.ID
				want auth.CheckVerdict
			}{
				{name: "same workspace", id: workspace, want: auth.CheckUnknown},
				{name: "foreign workspace", id: model.NewID(), want: auth.CheckBroken},
			} {
				t.Run(kind.name+" "+target.name, func(t *testing.T) {
					data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
						scope := typedEvidenceScope{Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true}
						kind.configure(&scope, target.id)
						return scope
					}}
					kindEngine, _ := typedEvidenceScopedEngine(t, f, data, "", 0, FreshnessRecord{})
					req := typedEvidenceRequest(f.tenant)
					req.Principal = principal
					req.Resource = auth.ResourceAttrs{Kind: kind.resourceK, ID: model.NewID().String()}
					decision, err := kindEngine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
					if err != nil || decision.ResourceGuard.Verdict != target.want {
						t.Fatalf("%s %s = %#v, %v; want guard %v", kind.name, target.name, decision, err, target.want)
					}
					if data.views != 1 {
						t.Fatalf("%s %s opened %d Views, want one", kind.name, target.name, data.views)
					}
				})
			}
		}
	})

	t.Run("tree lookup error is unknown and does not escape the same View", func(t *testing.T) {
		data := &typedEvidenceData{st: f.st, wrap: func(sc store.Scope) store.Scope {
			return typedEvidenceScope{
				Scope: sc, now: model.NewTimestamp(typedEvidenceNow), forceClock: true,
				resources: &typedEvidenceResourceRepo{err: errors.New("resource lookup unavailable")},
			}
		}}
		lookupEngine, _ := typedEvidenceScopedEngine(t, f, data, "", 0, FreshnessRecord{})
		req := typedEvidenceRequest(f.tenant)
		req.Principal = principal
		req.Resource = auth.ResourceAttrs{Kind: "resource", ID: model.NewID().String()}
		decision, err := lookupEngine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
		if err != nil || decision.ResourceGuard.Verdict != auth.CheckUnknown || len(decision.Facts) != 0 {
			t.Fatalf("tree lookup error = %#v, %v; want UNKNOWN/no facts", decision, err)
		}
		if data.views != 1 {
			t.Fatalf("tree lookup error opened %d Views, want one", data.views)
		}
	})

	t.Run("declared own workspace with Cedar set reads lineage and cannot grant", func(t *testing.T) {
		lineageData := f.data(typedEvidenceNow, nil)
		lineageEngine, _ := typedEvidenceScopedEngine(t, f, lineageData, `permit(principal, action, resource);`, 0, FreshnessRecord{})
		req := typedEvidenceRequest(f.tenant)
		req.Principal = principal
		req.Resource = auth.ResourceAttrs{Kind: "agent", WorkspaceID: workspace}
		decision, err := lineageEngine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
		if err != nil || decision.Effect != auth.EffectAbstain || decision.ResourceGuard.Verdict != auth.CheckUnknown ||
			decision.ForbidAbsence.Verdict != auth.CheckClean {
			t.Fatalf("declared-own Cedar scope = %#v, %v; want ABSTAIN/UNKNOWN/CLEAN", decision, err)
		}
		if lineageData.views != 1 {
			t.Fatalf("declared-own Cedar scope opened %d Views, want one", lineageData.views)
		}
	})

	t.Run("Cedar lineage abstain, permit, and forbid retain safe typed algebra", func(t *testing.T) {
		lineageData := f.data(typedEvidenceNow, nil)
		var agent model.Agent
		if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
			var err error
			agent, err = sc.Agents().Create(context.Background(), model.Agent{Name: "lineage", Kind: "test", ExternalID: "lineage", Status: model.StatusActive, WorkspaceID: workspace})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name       string
			source     string
			wantEffect auth.Effect
			wantForbid auth.CheckVerdict
		}{
			{
				name: "abstain", source: `permit(principal, action == Action::"agent:write", resource);`,
				wantEffect: auth.EffectAbstain, wantForbid: auth.CheckClean,
			},
			{
				name: "permit degrades to abstain", source: `permit(principal, action, resource);`,
				wantEffect: auth.EffectAbstain, wantForbid: auth.CheckClean,
			},
			{
				name: "explicit forbid dominates", source: `forbid(principal, action, resource);`,
				wantEffect: auth.EffectForbid, wantForbid: auth.CheckBroken,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				lineageEngine, _ := typedEvidenceScopedEngine(t, f, lineageData, tc.source, 0, FreshnessRecord{})
				req := typedEvidenceRequest(f.tenant)
				req.Resource = auth.ResourceAttrs{Kind: "agent", ID: agent.ID.String()}
				decision, err := lineageEngine.ScopedEvidence(typedEvidenceContext(t, deadline), req)
				if err != nil || decision.Effect != tc.wantEffect || decision.ResourceGuard.Verdict != auth.CheckUnknown ||
					decision.ForbidAbsence.Verdict != tc.wantForbid {
					t.Fatalf("lineage %s = %#v, %v; want effect:%v unknown guard forbid:%v", tc.name, decision, err, tc.wantEffect, tc.wantForbid)
				}
			})
		}
	})
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The lab attestation table is independent of caller fields. An entry approves
// one complete proposed binding in this tenant; a mutated roster or route is not
// accepted simply because its references/digests are well formed. This tests the
// dependency contract, not an operational principal or directory adapter.
type t0Verifier struct {
	mu      sync.Mutex
	tenant  model.TenantID
	allowed map[AttemptRef]AttemptBinding
	query   bool
	checks  []EvidenceCheck
}

func (v *t0Verifier) approve(req prepareAttemptRequest) {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Fixtures own the input; round-trip to avoid sharing mutable facts.
	raw, err := json.Marshal(encodeBinding(req.Binding))
	if err != nil {
		panic(err)
	}
	var jb jsonBinding
	if err := json.Unmarshal(raw, &jb); err != nil {
		panic(err)
	}
	b, err := decodeBinding(jb)
	if err != nil {
		panic(err)
	}
	v.allowed[req.AttemptRef] = b
}

func (v *t0Verifier) VerifyAttemptEvidence(_ context.Context, sc store.Scope, c EvidenceCheck) (VerifiedAttemptActor, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.checks = append(v.checks, c)
	if sc.Tenant() != v.tenant || !v.query {
		return VerifiedAttemptActor{}, errors.New("fixture authority denied")
	}
	if c.Operation == opPrepareBinding {
		binding, ok := v.allowed[c.AttemptRef]
		if !ok || c.ProposedBinding == nil || !reflect.DeepEqual(binding, *c.ProposedBinding) ||
			!reflect.DeepEqual(c.Evidence, binding.AuthorityRefs) {
			return VerifiedAttemptActor{}, errors.New("fixture binding or membership not attested")
		}
	}
	return VerifiedAttemptActor{Actor: "fixture-executor", ActorKind: model.ActorSystem}, nil
}

type t0Data struct {
	api.ModuleData
	wrap func(store.Scope) store.Scope
}

func (d t0Data) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}
func (d t0Data) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

type t0Scope struct {
	store.Scope
	now      time.Time
	policies store.Repository[model.Policy]
	ext      func(model.Kind, store.GenericRepo) store.GenericRepo
}

func (s t0Scope) LockTransaction(ctx context.Context, key string) error {
	return s.Scope.(store.TransactionLocker).LockTransaction(ctx, key)
}
func (s t0Scope) TransactionNow(context.Context) (model.Timestamp, error) {
	return model.NewTimestamp(s.now), nil
}
func (s t0Scope) Policies() store.Repository[model.Policy] {
	if s.policies != nil {
		return s.policies
	}
	return s.Scope.Policies()
}
func (s t0Scope) Ext(kind model.Kind) (store.GenericRepo, error) {
	r, err := s.Scope.Ext(kind)
	if err == nil && s.ext != nil {
		r = s.ext(kind, r)
	}
	return r, err
}

func t0Wire(m *Module, tenant model.TenantID) *t0Verifier {
	v := &t0Verifier{tenant: tenant, query: true, allowed: map[AttemptRef]AttemptBinding{}}
	WithAttemptEvidenceVerifier(v)(m)
	m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope { return t0Scope{Scope: sc, now: baseTime} }})
	return v
}

func t0Quiescing(t testing.TB, m *Module, st store.Store, tenant model.TenantID) {
	t.Helper()
	if len(scopeRows(t, st, tenant)) != 0 {
		return
	}
	if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{Evidence: []EvidenceRef{labEvidence("fixture-quiescence")}}); err != nil {
		t.Fatal(err)
	}
}

// Only this fixture makes an active scope. Begin stops at quiescing; this row
// update does not test or implement ActivateLifecycleScope.
func t0Active(t testing.TB, m *Module, st store.Store, tenant model.TenantID) {
	t.Helper()
	t0Quiescing(t, m, st, tenant)
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		r, err := sc.Ext(lifecycleScopeKind)
		if err != nil {
			return err
		}
		rows, _, err := r.List(context.Background(), model.Query{Limit: 2})
		if err != nil {
			return err
		}
		rows[0][colScopeState] = lifecycleActive
		rows[0][colScopeActivated] = model.NewTimestamp(baseTime).String()
		_, err = r.Update(context.Background(), rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func t0Request(t testing.TB, st store.Store, tenant model.TenantID) prepareAttemptRequest {
	t.Helper()
	ctx := context.Background()
	var provider model.Provider
	var mdl model.Model
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		provider, err = sc.Providers().Create(ctx, model.Provider{Name: "fixture-provider", Kind: "fixture", Status: model.StatusActive})
		if err != nil {
			return err
		}
		mdl, err = sc.Models().Create(ctx, model.Model{Name: "fixture-model", ProviderID: provider.ID, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := AttemptRef(strings.ReplaceAll(model.NewID().String(), "-", ""))
	user := model.NewID()
	ev := labEvidence("binding-attestation")
	b := AttemptBinding{Status: bindingResolved, RequestRef: ref, Subject: AttemptSubject{ActorRef: "user:" + user.String(), ActorKind: model.ActorUser, UserID: user},
		Entities: ResolvedEntityIDs{ProviderID: provider.ID, ModelID: mdl.ID}, Attribution: unboundAttribution(), AuthorityRefs: []EvidenceRef{ev},
		Destination: AttemptDestination{ProfileRef: "fixture-profile", ProfileRevision: "revision-1", ProviderRef: provider.Name, ModelRef: mdl.Name,
			Action: "generate", Protocol: "fixture", AdapterID: "fixture", AdapterVersion: "1", Surface: "fixture", CredentialAudience: "fixture", AuthScheme: "fixture",
			EndpointDigest: ev.Digest, TransportDigest: ev.Digest, PolicyID: model.NewID(), PolicyVersion: 1, PolicySpecDigest: ev.Digest,
			ProxyPolicyDigest: ev.Digest, PreparedDigest: ev.Digest, PreparedBytes: 10, MaxOutputTokens: 10, MaxRequestBytes: 100, MaxResponseBytes: 100, TimeoutNanos: int64(time.Second)},
		Estimate: EstimateBasis{AmountMicroUSD: 7, Method: "fixture", Revision: "1", PriceDigest: ev.Digest, QualificationRef: ev, RateRefs: []EvidenceRef{ev}},
	}
	for dim, value := range map[string]string{"provider": provider.Name, "model": mdl.Name, "actor": b.Subject.ActorRef} {
		b.Attribution[dim] = AttributionFact{State: factKnown, Values: []string{value}, Evidence: []EvidenceRef{ev}}
	}
	b.Attribution["user_group"] = AttributionFact{State: factKnown, Values: []string{}, Evidence: []EvidenceRef{labEvidence("complete-directory-revision-1")}}
	return prepareAttemptRequest{AttemptRef: ref, Binding: b, OwnerRef: "fixture-executor", ReviewAfter: model.NewTimestamp(baseTime.Add(time.Hour))}
}

func t0Policy(t testing.TB, st store.Store, tenant model.TenantID, kind string, spec map[string]any) model.Policy {
	t.Helper()
	var p model.Policy
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		p, err = sc.Policies().Create(context.Background(), model.Policy{Name: "fixture-" + model.NewID().String(), Kind: kind, Enabled: true, Spec: spec})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func t0Budget(limit int64, action string) map[string]any {
	return map[string]any{"limit_micro_usd": limit, "action": action}
}

func t0Admit(t testing.TB, m *Module, tenant model.TenantID, req prepareAttemptRequest) AttemptView {
	t.Helper()
	r, err := m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || r.Decision != "admitted" || r.Attempt == nil || r.Denial != nil {
		t.Fatalf("admit result=%+v err=%v", r, err)
	}
	return *r.Attempt
}

func t0NoRows(t testing.TB, st store.Store, tenant model.TenantID) {
	t.Helper()
	if len(attemptRows(t, st, tenant)) != 0 || len(countReservations(t, st, tenant)) != 0 {
		t.Fatal("failed preparation left rows")
	}
}

func TestT0ScopeAuthorityAndZeroTarget(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	req := t0Request(t, st, tenant)
	_, err := m.prepareAttempt(context.Background(), tenant, req)
	if attemptCode(err) != errCodeCapabilityUnavailable {
		t.Fatalf("nil verifier: %v", err)
	}
	v := t0Wire(m, tenant)
	v.approve(req)
	_, err = m.prepareAttempt(context.Background(), tenant, req)
	if attemptCode(err) != errCodeLifecycleActivation {
		t.Fatalf("absent: %v", err)
	}
	t0NoRows(t, st, tenant)
	t0Quiescing(t, m, st, tenant)
	_, err = m.prepareAttempt(context.Background(), tenant, req)
	if attemptCode(err) != errCodeLifecycleActivation {
		t.Fatalf("quiescing: %v", err)
	}
	t0NoRows(t, st, tenant)
	t0Active(t, m, st, tenant)
	view := t0Admit(t, m, tenant, req)
	if len(view.Targets) != 0 || view.AccountingAt == nil || *view.AccountingAt != model.NewTimestamp(baseTime) || view.Phase != phasePrepared {
		t.Fatalf("zero target: %+v", view)
	}
	if len(attemptRows(t, st, tenant)) != 1 || len(countReservations(t, st, tenant)) != 0 {
		t.Fatal("zero target parent missing")
	}
	got, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef)
	if err != nil || got.ID != view.ID {
		t.Fatalf("read zero parent: %v", err)
	}
	before := attemptRows(t, st, tenant)
	req.OwnerRef = "another-hint"
	req.ReviewAfter = model.NewTimestamp(baseTime.Add(-time.Hour))
	r, err := m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || !r.Replayed || r.Attempt.OwnerRef != view.OwnerRef || !reflect.DeepEqual(before, attemptRows(t, st, tenant)) {
		t.Fatalf("replay: %+v %v", r, err)
	}
	req.Binding.Estimate.AmountMicroUSD++
	_, err = m.prepareAttempt(context.Background(), tenant, req)
	if attemptCode(err) != errCodeAttemptIdentityConflict {
		t.Fatalf("conflict: %v", err)
	}
	v.query = false
	for _, ref := range []AttemptRef{req.AttemptRef, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		_, err = m.GetAttempt(context.Background(), tenant, ref)
		if attemptCode(err) != errCodeOwnerMismatch {
			t.Fatalf("query authority: %v", err)
		}
	}
	_, err = m.prepareAttempt(context.Background(), tenant, req)
	if attemptCode(err) != errCodeOwnerMismatch {
		t.Fatalf("replay authority: %v", err)
	}
}

func TestT0CompleteTargetsReplayAndHold(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	v := t0Wire(m, tenant)
	req := t0Request(t, st, tenant)
	req.Binding.ApplySeatLimits = true
	v.approve(req)
	budget := t0Policy(t, st, tenant, policyKindBudget, t0Budget(10, "block"))
	t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "organization", Period: "daily", AmountMicroUSD: 10}.mapValue())
	t0Active(t, m, st, tenant)
	view := t0Admit(t, m, tenant, req)
	if len(view.Targets) != 2 || len(countReservations(t, st, tenant)) != 2 {
		t.Fatal("incomplete fanout")
	}
	// Removing today's policy cannot rewrite historical binding or target facts.
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error { return sc.Policies().Delete(context.Background(), budget.ID) }); err != nil {
		t.Fatal(err)
	}
	r, err := m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || !r.Replayed || !reflect.DeepEqual(*r.Attempt, view) {
		t.Fatalf("historical replay: %+v %v", r, err)
	}
	var held reservedTotal
	err = st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		start, _ := periodStart("monthly", baseTime)
		held, err = heldReservedForWindow(context.Background(), sc, budget.ID, "", start, periodEnd("monthly", start), true, baseTime.Add(24*time.Hour))
		return err
	})
	if err != nil || held.State != reservedKnown || held.MicroUSD != 7 {
		t.Fatalf("TTL released prepared: %+v %v", held, err)
	}
	// The remaining daily seat cap still sees the complete prior group.
	req.AttemptRef = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	req.Binding.Estimate.AmountMicroUSD = 4
	v.approve(req)
	r, err = m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || r.Decision != "denied" || r.Denial.Code != errCodeBudgetDenied {
		t.Fatalf("held headroom: %+v %v", r, err)
	}
	if len(attemptRows(t, st, tenant)) != 1 || len(countReservations(t, st, tenant)) != 2 {
		t.Fatal("denial wrote rows")
	}
}

// These seams control repository evidence, not the admission implementation.
type t0PolicyRepo struct {
	store.Repository[model.Policy]
	list func(context.Context, model.Query) ([]model.Policy, model.Page, error)
}

func (r t0PolicyRepo) List(ctx context.Context, q model.Query) ([]model.Policy, model.Page, error) {
	return r.list(ctx, q)
}

type t0Repo struct {
	store.GenericRepo
	list     func(context.Context, model.Query) ([]model.Record, model.Page, error)
	create   func(context.Context, model.Record) (model.Record, error)
	createID func(context.Context, model.ID, model.Record) (model.Record, error)
}

func (r t0Repo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	if r.list != nil {
		return r.list(ctx, q)
	}
	return r.GenericRepo.List(ctx, q)
}
func (r t0Repo) Create(ctx context.Context, row model.Record) (model.Record, error) {
	if r.create != nil {
		return r.create(ctx, row)
	}
	return r.GenericRepo.Create(ctx, row)
}
func (r t0Repo) CreateWithID(ctx context.Context, id model.ID, row model.Record) (model.Record, error) {
	if r.createID != nil {
		return r.createID(ctx, id, row)
	}
	return r.GenericRepo.CreateWithID(ctx, id, row)
}
func t0OverridePolicies(m *Module, change func([]model.Policy) []model.Policy) {
	m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
		repo := sc.Policies()
		return t0Scope{Scope: sc, now: baseTime, policies: t0PolicyRepo{Repository: repo, list: func(ctx context.Context, q model.Query) ([]model.Policy, model.Page, error) {
			rows, page, err := repo.List(ctx, q)
			if err == nil {
				rows = change(rows)
			}
			return rows, page, err
		}}}
	}})
}
func t0Fresh(req prepareAttemptRequest) prepareAttemptRequest {
	req.AttemptRef = AttemptRef(strings.ReplaceAll(model.NewID().String(), "-", ""))
	return req
}

func TestT0StrictPolicyEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*model.Policy)
	}{
		{"missing limit", func(p *model.Policy) { delete(p.Spec, "limit_micro_usd") }},
		{"null", func(p *model.Policy) { p.Spec["period"] = nil }},
		{"money string", func(p *model.Policy) { p.Spec["limit_micro_usd"] = "100" }},
		{"fraction", func(p *model.Policy) { p.Spec["limit_micro_usd"] = 100.5 }},
		{"unsafe float", func(p *model.Policy) { p.Spec["limit_micro_usd"] = float64(1 << 53) }},
		{"int64 overflow", func(p *model.Policy) { p.Spec["limit_micro_usd"] = json.Number("9223372036854775808") }},
		{"noncanonical integer", func(p *model.Policy) { p.Spec["limit_micro_usd"] = json.Number("01") }},
		{"infinite", func(p *model.Policy) { p.Spec["limit_micro_usd"] = math.Inf(1) }},
		{"negative static", func(p *model.Policy) { p.Spec["reserved_micro_usd"] = int64(-1) }},
		{"unknown key", func(p *model.Policy) { p.Spec["future_option"] = true }},
		{"wrong boolean", func(p *model.Policy) { p.Spec["fail_closed"] = "true" }},
		{"empty period", func(p *model.Policy) { p.Spec["period"] = "" }},
		{"currency", func(p *model.Policy) { p.Spec["currency"] = "EUR" }},
		{"incoherent global key", func(p *model.Policy) { p.Spec["key"] = "tenant" }},
		{"zero threshold", func(p *model.Policy) { p.Spec["thresholds"] = []any{0.0} }},
		{"string threshold", func(p *model.Policy) { p.Spec["thresholds"] = []any{"0.8"} }},
		{"threshold overflow", func(p *model.Policy) { p.Spec["thresholds"] = json.Number("1e400") }},
		{"too many thresholds", func(p *model.Policy) {
			a := make([]float64, 257)
			for i := range a {
				a[i] = 1
			}
			p.Spec["thresholds"] = a
		}},
		{"zero version", func(p *model.Policy) { p.Version = 0 }},
		{"noncanonical ID", func(p *model.Policy) { p.ID = model.ID(strings.ToUpper(p.ID.String())) }},
		{"foreign tenant", func(p *model.Policy) { p.TenantID = model.TenantID(model.NewID()) }},
		{"wrong kind", func(p *model.Policy) { p.Kind = policyKindSpendLimit }},
		{"disabled returned", func(p *model.Policy) { p.Enabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Active(t, m, st, tenant)
			t0OverridePolicies(m, func(rows []model.Policy) []model.Policy {
				for i := range rows {
					tc.change(&rows[i])
				}
				return rows
			})
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			if attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("policy defect: %v", err)
			}
			t0NoRows(t, st, tenant)
		})
	}
	for _, mode := range []string{"showback", "nonmatching", "unselected seat", "unassigned seat"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			req.Binding.ApplySeatLimits = mode != "unassigned seat"
			v.approve(req)
			if mode == "unselected seat" || mode == "unassigned seat" {
				t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "user", ScopeKey: "user:" + model.NewID().String(), Period: "daily", AmountMicroUSD: 100}.mapValue())
			} else {
				spec := t0Budget(100, "alert")
				if mode == "nonmatching" {
					spec["action"] = "block"
					spec["dimension"] = "provider"
					spec["key"] = "different"
				}
				t0Policy(t, st, tenant, policyKindBudget, spec)
			}
			t0Active(t, m, st, tenant)
			t0OverridePolicies(m, func(rows []model.Policy) []model.Policy {
				for i := range rows {
					rows[i].Spec["invalid"] = true
				}
				return rows
			})
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			if attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("skipped malformed policy: %v", err)
			}
			t0NoRows(t, st, tenant)
		})
	}
}

func TestT0PolicyPresenceDigestAndExactIntegers(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	v := t0Wire(m, tenant)
	req := t0Request(t, st, tenant)
	req.Binding.Estimate.AmountMicroUSD = 0
	t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
	t0Active(t, m, st, tenant)
	real := m.data
	var digests []Digest
	for _, spec := range []map[string]any{
		{"limit_micro_usd": int64(100), "action": "block"},
		{"limit_micro_usd": json.Number("100"), "action": "block"},
		{"limit_micro_usd": float64(100), "action": "block"},
		{"limit_micro_usd": int64(100), "action": "block", "period": "monthly"},
		{"limit_micro_usd": int64(100), "action": "block", "thresholds": []float64{.8, 1, .8}},
		{"limit_micro_usd": int64(100), "action": "block", "thresholds": []float64{.8, .8, 1}},
		{"limit_micro_usd": int64(math.MaxInt64), "action": "block"},
	} {
		m.UseData(real)
		t0OverridePolicies(m, func(rows []model.Policy) []model.Policy {
			for i := range rows {
				rows[i].Spec = spec
			}
			return rows
		})
		req = t0Fresh(req)
		v.approve(req)
		view := t0Admit(t, m, tenant, req)
		digests = append(digests, *view.Targets[0].PolicySpecDigest)
		if _, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef); err != nil {
			t.Fatal(err)
		}
	}
	if digests[0] != digests[1] || digests[0] != digests[2] || digests[0] == digests[3] || digests[4] == digests[5] {
		t.Fatalf("logical type/presence/order lost: %v", digests)
	}
	// The last exact int64 is supplied at the private repository seam. Persisted
	// policy JSON still arrives as float64; this is NOT full-int64 storage proof.
}

func TestT0DimensionsEntitiesAndAuthority(t *testing.T) {
	for _, mode := range []string{"missing dimension", "missing fact entry", "verified NA", "unverified NA", "known empty groups", "unverified groups", "matching group unsupported", "contradictory provider", "contradictory model provider", "noncanonical binding ID", "incomplete runtime tuple", "prepare authority"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			t0Active(t, m, st, tenant)
			spec := t0Budget(100, "block")
			spec["dimension"] = "team"
			spec["key"] = "fixture-team"
			want := errCodeDimensionRequired
			admit := false
			switch mode {
			case "missing fact entry":
				delete(req.Binding.Attribution, "team")
				want = errCodeInvalidAttempt
			case "verified NA", "unverified NA":
				if mode == "unverified NA" {
					v.approve(req)
				}
				req.Binding.Attribution["team"] = AttributionFact{State: factNotApplicable, NotApplicableRule: "fixture-no-team", Evidence: []EvidenceRef{labEvidence("NA-proof")}}
				admit = mode == "verified NA"
				want = errCodeOwnerMismatch
			case "known empty groups", "unverified groups", "matching group unsupported":
				group := model.NewID().String()
				spec["dimension"] = "user_group"
				spec["key"] = group
				if mode == "unverified groups" {
					req.Binding.ApplySeatLimits = true
					v.approve(req)
					t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "rbac_group", ScopeKey: group, Period: "daily", AmountMicroUSD: 100}.mapValue())
					spec["action"] = "alert"
				}
				if mode != "known empty groups" {
					f := req.Binding.Attribution["user_group"]
					f.Values = []string{group}
					req.Binding.Attribution["user_group"] = f
				}
				admit = mode == "known empty groups"
				want = errCodeLedgerIndeterminate
				if mode == "unverified groups" {
					want = errCodeOwnerMismatch
				}
			case "contradictory provider":
				req.Binding.Entities.ProviderID = model.NewID()
				spec = t0Budget(100, "block")
				want = errCodeLedgerIndeterminate
			case "contradictory model provider":
				other := t0Request(t, st, tenant)
				req.Binding.Entities.ModelID = other.Binding.Entities.ModelID
				spec = t0Budget(100, "block")
				want = errCodeLedgerIndeterminate
			case "noncanonical binding ID":
				req.Binding.Entities.ProviderID = model.ID(strings.ToUpper(req.Binding.Entities.ProviderID.String()))
				spec = t0Budget(100, "block")
				want = errCodeInvalidAttempt
			case "incomplete runtime tuple":
				req.Binding.Subject.SessionRunRef = "run-without-fence"
				spec = t0Budget(100, "block")
				want = errCodeInvalidAttempt
			case "prepare authority":
				spec = t0Budget(100, "block")
				want = errCodeOwnerMismatch
			}
			t0Policy(t, st, tenant, policyKindBudget, spec)
			if mode != "unverified NA" && mode != "unverified groups" && mode != "prepare authority" {
				v.approve(req)
			}
			if admit {
				view := t0Admit(t, m, tenant, req)
				if len(view.Targets) != 0 {
					t.Fatal("excluded budget has child")
				}
				return
			}
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			if attemptCode(err) != want {
				t.Fatalf("want %s got %v", want, err)
			}
			t0NoRows(t, st, tenant)
		})
	}
}

func TestT0SeatPrecedenceAndUnlimitedWitness(t *testing.T) {
	for _, mode := range []string{"user unlimited", "user finite zero", "group restrictive", "group tie", "organization fallback"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			req.Binding.ApplySeatLimits = true
			group := model.NewID().String()
			f := req.Binding.Attribution["user_group"]
			f.Values = []string{group}
			req.Binding.Attribution["user_group"] = f
			org := t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "organization", Period: "daily", AmountMicroUSD: 1}.mapValue())
			chosen := org
			if mode != "organization fallback" {
				chosen = t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "rbac_group", ScopeKey: group, Period: "daily", AmountMicroUSD: 20}.mapValue())
				if mode == "group restrictive" {
					chosen = t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "rbac_group", ScopeKey: group, Period: "daily", AmountMicroUSD: 10}.mapValue())
				}
				if mode == "group tie" {
					second := t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "rbac_group", ScopeKey: group, Period: "daily", AmountMicroUSD: 20}.mapValue())
					if second.ID < chosen.ID {
						chosen = second
					}
				}
			}
			if strings.HasPrefix(mode, "user") {
				chosen = t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "user", ScopeKey: req.Binding.Subject.ActorRef, Period: "daily", Unlimited: mode == "user unlimited"}.mapValue())
			}
			v.approve(req)
			t0Active(t, m, st, tenant)
			if mode == "user finite zero" || mode == "organization fallback" {
				r, err := m.prepareAttempt(context.Background(), tenant, req)
				if err != nil || r.Decision != "denied" || *r.Denial.PolicyID != chosen.ID {
					t.Fatalf("finite denial: %+v %v", r, err)
				}
				t0NoRows(t, st, tenant)
				return
			}
			view := t0Admit(t, m, tenant, req)
			if len(view.Targets) != 1 || view.Targets[0].PolicyID != chosen.ID {
				t.Fatalf("precedence: %+v", view.Targets)
			}
			if mode == "user unlimited" {
				tg := view.Targets[0]
				rows := countReservations(t, st, tenant)
				if tg.Action != "unlimited" || tg.LimitMicroUSD != nil || tg.StaticReservedMicroUSD == nil || *tg.StaticReservedMicroUSD != 0 || len(rows) != 1 || rows[0].Int(colResvAmount) != 0 || rows[0].Int(colResvActual) != 0 {
					t.Fatalf("unlimited witness: %+v %v", tg, rows)
				}
				mutateReservation(t, st, tenant, tg.ChildID, func(r model.Record) { r[colResvAmount] = int64(1) })
				if _, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef); attemptCode(err) != errCodeLedgerIndeterminate {
					t.Fatalf("changed witness accepted: %v", err)
				}
			}
		})
	}
}

func TestT0CompletePolicyCursorProgress(t *testing.T) {
	for _, mode := range []string{"empty progressing page", "complete at cap", "missing cursor", "stalled cursor", "cycle", "truncated", "duplicate ID", "target cap"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			p := t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Active(t, m, st, tenant)
			calls := 0
			m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
				return t0Scope{Scope: sc, now: baseTime, policies: t0PolicyRepo{Repository: sc.Policies(), list: func(_ context.Context, q model.Query) ([]model.Policy, model.Page, error) {
					if q.Filters[0].Value != policyKindBudget {
						return nil, model.Page{}, nil
					}
					calls++
					if mode == "target cap" {
						out := make([]model.Policy, 257)
						for i := range out {
							out[i] = p
							out[i].ID = model.NewID()
						}
						return out, model.Page{}, nil
					}
					rows := []model.Policy{}
					if calls == 1 || mode == "duplicate ID" {
						rows = append(rows, p)
					}
					switch mode {
					case "empty progressing page":
						if calls == 3 {
							return rows, model.Page{}, nil
						}
					case "complete at cap":
						if calls == maxScanPages {
							return rows, model.Page{}, nil
						}
					case "missing cursor":
						return rows, model.Page{HasMore: true}, nil
					case "stalled cursor":
						return rows, model.Page{HasMore: true, Cursor: "same"}, nil
					case "cycle":
						return rows, model.Page{HasMore: true, Cursor: fmt.Sprint(calls % 2)}, nil
					case "duplicate ID":
						if calls == 2 {
							return rows, model.Page{}, nil
						}
					}
					return rows, model.Page{HasMore: true, Cursor: fmt.Sprint(calls)}, nil
				}}}
			}})
			if mode == "empty progressing page" || mode == "complete at cap" {
				view := t0Admit(t, m, tenant, req)
				if len(view.Targets) != 1 {
					t.Fatal("lost policy across empty page")
				}
				if mode == "complete at cap" && calls != maxScanPages {
					t.Fatalf("calls %d", calls)
				}
				return
			}
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			want := errCodeLedgerIncomplete
			if mode == "duplicate ID" {
				want = errCodeLedgerIndeterminate
			}
			if attemptCode(err) != want {
				t.Fatalf("cursor defect: %v calls=%d", err, calls)
			}
			t0NoRows(t, st, tenant)
		})
	}
}

func TestT0PreparedGroupIntegrityAcrossReaders(t *testing.T) {
	for _, mode := range []string{"amount", "sequence", "actual", "dimension", "period", "review hint", "state", "handle", "missing sibling", "extra linked sibling", "legacy sibling", "parent digest", "target identity", "target version", "target static", "outcome column"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Active(t, m, st, tenant)
			view := t0Admit(t, m, tenant, req)
			victim := view.Targets[1]
			switch mode {
			case "missing sibling":
				deleteReservation(t, st, tenant, victim.ChildID)
			case "extra linked sibling", "legacy sibling":
				row := legacyChild(model.NewID(), view.Handle, "", "monthly", 1, 7, baseTime, baseTime.Add(time.Hour), resvStateActive)
				if mode == "extra linked sibling" {
					row[colResvAttemptRef] = string(req.AttemptRef)
					row[colResvLifecycleVersion] = lifecycleLinkageVersion
					row[colResvHandle] = model.NewID().String()
				}
				seedGroup(t, st, tenant, row)
			case "parent digest":
				mutateAttemptRow(t, st, tenant, req.AttemptRef, func(r model.Record) { r[colAttemptResvDigest] = string(labEvidence("different").Digest) })
			case "target identity", "target version", "target static":
				mutateAttemptTargets(t, st, tenant, req.AttemptRef, func(ts []jsonTarget) {
					switch mode {
					case "target identity":
						ts[1].PolicyID = model.NewID().String()
					case "target version":
						n := jsonInt(9)
						ts[1].PolicyVersion = &n
					case "target static":
						n := jsonInt(1)
						ts[1].StaticReservedMicroUSD = &n
					}
				})
			case "outcome column":
				mutateAttemptRow(t, st, tenant, req.AttemptRef, func(r model.Record) { r[colAttemptOutcome] = "{}" })
			default:
				mutateReservation(t, st, tenant, victim.ChildID, func(r model.Record) {
					switch mode {
					case "amount":
						r[colResvAmount] = int64(6)
					case "sequence":
						r[colResvSeq] = int64(9)
					case "actual":
						r[colResvActual] = int64(1)
					case "dimension":
						r[colResvDimension] = "team"
					case "period":
						r[colResvPeriod] = "daily"
					case "review hint":
						r[colResvExpiresAt] = model.NewTimestamp(baseTime.Add(2 * time.Hour)).String()
					case "state":
						r[colResvState] = resvStateExpired
					case "handle":
						r[colResvHandle] = model.NewID().String()
					}
				})
			}
			if _, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("Get accepted corruption: %v", err)
			}
			if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("replay accepted corruption: %v", err)
			}
			// Query the OTHER target: the corrupt sibling must still defeat an exact sum.
			held := reservedFor(t, m, st, tenant, view.Targets[0].PolicyID, "")
			if held.State != reservedIndeterminate {
				t.Fatalf("partial group certified: %+v", held)
			}
			// A new frontier invokes the census through its existing Interface. Deleting
			// the fixture scope is not an activation operation exposed by this module.
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(lifecycleScopeKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(context.Background(), model.Query{Limit: 2})
				if err != nil {
					return err
				}
				return repo.Delete(context.Background(), mustID(t, rows[0].String(model.ColID)))
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.BeginLifecycleActivation(context.Background(), tenant, LifecycleActivationRequest{Evidence: []EvidenceRef{labEvidence("recensus")}}); err == nil {
				t.Fatal("census accepted corrupt prepared group")
			}
		})
	}
}

func TestT0ExactAccountingAndBlockPrecedence(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	v := t0Wire(m, tenant)
	req := t0Request(t, st, tenant)
	spec := t0Budget(20, "block")
	spec["reserved_micro_usd"] = int64(5)
	budget := t0Policy(t, st, tenant, policyKindBudget, spec)
	m.ingest(t, tenant, mkCost("fixture-provider", "fixture-model", "", 0, 0, 10, baseTime.Add(-time.Minute)))
	m.ingest(t, tenant, mkCost("fixture-provider", "fixture-model", "", 0, 0, -7, baseTime.Add(-2*time.Minute)))
	seedGroup(t, st, tenant, legacyChild(budget.ID, model.NewID(), "", "monthly", 1, 2, baseTime, baseTime.Add(time.Hour), resvStateActive))
	t0Active(t, m, st, tenant)
	for _, amount := range []int64{4, 6} {
		req = t0Fresh(req)
		req.Binding.Estimate.AmountMicroUSD = amount
		v.approve(req)
		t0Admit(t, m, tenant, req)
	}
	// 10 - 7 cost + 5 static + 2 legacy + 4 prepared + 6 prepared = 20 exactly.
	req = t0Fresh(req)
	req.Binding.Estimate.AmountMicroUSD = 1
	v.approve(req)
	r, err := m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || r.Decision != "denied" || *r.Denial.PolicyID != budget.ID {
		t.Fatalf("exact total: %+v %v", r, err)
	}
	if len(attemptRows(t, st, tenant)) != 2 || len(countReservations(t, st, tenant)) != 3 {
		t.Fatal("headroom denial wrote")
	}
	t0Policy(t, st, tenant, policyKindBudget, t0Budget(1, "throttle"))
	r, err = m.prepareAttempt(context.Background(), tenant, req)
	if err != nil || r.Decision != "denied" || r.Denial.Action != "block" {
		t.Fatalf("block precedence: %+v %v", r, err)
	}
}

func TestT0IncompleteAndContradictoryAmounts(t *testing.T) {
	for _, mode := range []string{"cost missing cursor", "cost wrong money", "cost signed prefix", "hold truncated", "hold negative prefix", "effective overflow", "estimate overflow"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Active(t, m, st, tenant)
			if mode == "estimate overflow" {
				req.Binding.Estimate.AmountMicroUSD = math.MaxInt64
				v.approve(req)
			}
			m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
				return t0Scope{Scope: sc, now: baseTime, ext: func(kind model.Kind, repo store.GenericRepo) store.GenericRepo {
					if kind == costSampleKind {
						return t0Repo{GenericRepo: repo, list: func(context.Context, model.Query) ([]model.Record, model.Page, error) {
							row := model.Record{model.ColID: model.NewID().String(), model.ColTenantID: tenant.String(), colCostMicroUSD: int64(1), colOccurredAt: model.NewTimestamp(baseTime).String()}
							switch mode {
							case "cost missing cursor":
								return []model.Record{row}, model.Page{HasMore: true}, nil
							case "cost wrong money":
								row[colCostMicroUSD] = "7"
								return []model.Record{row}, model.Page{}, nil
							case "cost signed prefix":
								row[colCostMicroUSD] = int64(-1)
								return []model.Record{row}, model.Page{HasMore: true}, nil
							case "effective overflow":
								row[colCostMicroUSD] = int64(math.MaxInt64)
								return []model.Record{row, row}, model.Page{}, nil
							case "estimate overflow":
								return []model.Record{row}, model.Page{}, nil
							}
							return nil, model.Page{}, nil
						}}
					}
					if kind == budgetReservationKind && strings.HasPrefix(mode, "hold") {
						return t0Repo{GenericRepo: repo, list: func(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
							if q.Limit == 1 {
								return repo.List(ctx, q)
							}
							if mode == "hold negative prefix" {
								return []model.Record{{model.ColID: model.NewID().String(), model.ColTenantID: tenant.String(), colResvLifecycleVersion: lifecycleLinkageVersion, colResvAttemptRef: string(req.AttemptRef), colResvAmount: int64(-1)}}, model.Page{HasMore: true}, nil
							}
							return nil, model.Page{HasMore: true}, nil
						}}
					}
					return repo
				}}
			}})
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			want := errCodeLedgerIncomplete
			if mode == "cost wrong money" || mode == "hold negative prefix" {
				want = errCodeLedgerIndeterminate
			}
			if mode == "effective overflow" || mode == "estimate overflow" {
				want = errCodeArithmetic
			}
			if attemptCode(err) != want {
				t.Fatalf("want %s got %v", want, err)
			}
			t0NoRows(t, st, tenant)
		})
	}
}

func TestT0QueryPrecedesAnyExistenceRead(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	v := t0Wire(m, tenant)
	req := t0Request(t, st, tenant)
	v.approve(req)
	t0Active(t, m, st, tenant)
	t0Admit(t, m, tenant, req)
	v.query = false
	reads := 0
	m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
		return t0Scope{Scope: sc, now: baseTime, ext: func(_ model.Kind, repo store.GenericRepo) store.GenericRepo { reads++; return repo }}
	}})
	for _, ref := range []AttemptRef{req.AttemptRef, "cccccccccccccccccccccccccccccccc"} {
		if _, err := m.GetAttempt(context.Background(), tenant, ref); attemptCode(err) != errCodeOwnerMismatch {
			t.Fatalf("query: %v", err)
		}
		req.AttemptRef = ref
		if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeOwnerMismatch {
			t.Fatalf("prepare query: %v", err)
		}
	}
	if reads != 0 {
		t.Fatalf("unauthorized call opened %d existence repositories", reads)
	}
}

func TestT0ReplayBeforeScopeAndZeroTargetCensus(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	v := t0Wire(m, tenant)
	req := t0Request(t, st, tenant)
	v.approve(req)
	t0Active(t, m, st, tenant)
	view := t0Admit(t, m, tenant, req)
	row := scopeRows(t, st, tenant)[0]
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lifecycleScopeKind)
		if err != nil {
			return err
		}
		return repo.Delete(context.Background(), mustID(t, row.String(model.ColID)))
	}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"absent", "quiescing"} {
		if state == "quiescing" {
			t0Quiescing(t, m, st, tenant)
		} // Begin censuses the valid zero-target parent.
		before := attemptRows(t, st, tenant)
		r, err := m.prepareAttempt(context.Background(), tenant, req)
		if err != nil || !r.Replayed || r.Attempt.ID != view.ID || !reflect.DeepEqual(before, attemptRows(t, st, tenant)) {
			t.Fatalf("%s replay: %+v %v", state, r, err)
		}
		other := t0Fresh(req)
		v.approve(other)
		if _, err := m.prepareAttempt(context.Background(), tenant, other); attemptCode(err) != errCodeLifecycleActivation {
			t.Fatalf("%s new admission: %v", state, err)
		}
	}
}

func TestT0FrozenSessionAgentAndRuntimeAssociations(t *testing.T) {
	for _, mode := range []string{"coherent", "session agent conflict", "session external conflict", "unattested runtime"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			var agent model.Agent
			var session model.Session
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				var err error
				agent, err = sc.Agents().Create(context.Background(), model.Agent{Name: "fixture-billing-agent", Kind: "fixture", ExternalID: "billing-agent", Status: model.StatusActive})
				if err != nil {
					return err
				}
				session, err = sc.Sessions().Create(context.Background(), model.Session{AgentID: agent.ID, ExternalID: "billing-session", State: model.SessionRunning})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			req.Binding.Entities.AgentID = agent.ID
			req.Binding.Entities.SessionID = session.ID
			req.Binding.Attribution["agent"] = AttributionFact{State: factKnown, Values: []string{agent.ExternalID}, Evidence: []EvidenceRef{labEvidence("agent-association")}}
			req.Binding.Attribution["session"] = AttributionFact{State: factKnown, Values: []string{session.ExternalID}, Evidence: []EvidenceRef{labEvidence("session-association")}}
			req.Binding.Subject.AgentIdentity = "authenticated-agent-distinct-from-billing"
			req.Binding.Subject.SessionIdentity = "authenticated-session-distinct-from-billing"
			req.Binding.Subject.SessionWorkspaceID = model.NewID()
			req.Binding.Subject.SessionRunRef = "authenticated-run"
			req.Binding.Subject.SessionFence = 42
			req.Binding.Subject.DelegationRefs = []EvidenceRef{labEvidence("runtime-delegation")}
			if mode == "unattested runtime" {
				v.approve(req)
				req.Binding.Subject.SessionFence++
			}
			if mode == "session agent conflict" {
				if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					s, err := sc.Sessions().Get(context.Background(), session.ID)
					if err != nil {
						return err
					}
					s.AgentID = model.NewID()
					_, err = sc.Sessions().Update(context.Background(), s)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "session external conflict" {
				f := req.Binding.Attribution["session"]
				f.Values = []string{"contradictory-billing-session"}
				req.Binding.Attribution["session"] = f
			}
			if mode != "unattested runtime" {
				v.approve(req)
			}
			t0Active(t, m, st, tenant)
			if mode != "coherent" {
				_, err := m.prepareAttempt(context.Background(), tenant, req)
				want := errCodeLedgerIndeterminate
				if mode == "unattested runtime" {
					want = errCodeOwnerMismatch
				}
				if attemptCode(err) != want {
					t.Fatalf("association: %v", err)
				}
				t0NoRows(t, st, tenant)
				return
			}
			view := t0Admit(t, m, tenant, req)
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				if err := sc.Sessions().Delete(context.Background(), session.ID); err != nil {
					return err
				}
				return sc.Agents().Delete(context.Background(), agent.ID)
			}); err != nil {
				t.Fatal(err)
			}
			r, err := m.prepareAttempt(context.Background(), tenant, req)
			if err != nil || !r.Replayed || !reflect.DeepEqual(view, *r.Attempt) {
				t.Fatalf("retired billing entities changed history: %+v %v", r, err)
			}
		})
	}
}

func TestT0UnlimitedWitnessIsCompleteHistoricalEvidence(t *testing.T) {
	for _, mode := range []string{"delete policy replay", "missing witness", "finite nil variant", "present limit", "static", "version", "actual"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			req.Binding.ApplySeatLimits = true
			v.approve(req)
			p := t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "user", ScopeKey: req.Binding.Subject.ActorRef, Period: "daily", Unlimited: true}.mapValue())
			t0Active(t, m, st, tenant)
			view := t0Admit(t, m, tenant, req)
			switch mode {
			case "delete policy replay":
				if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error { return sc.Policies().Delete(context.Background(), p.ID) }); err != nil {
					t.Fatal(err)
				}
				r, err := m.prepareAttempt(context.Background(), tenant, req)
				if err != nil || !r.Replayed || !reflect.DeepEqual(*r.Attempt, view) {
					t.Fatalf("unlimited history: %+v %v", r, err)
				}
				return
			case "missing witness":
				deleteReservation(t, st, tenant, view.Targets[0].ChildID)
			case "actual":
				mutateReservation(t, st, tenant, view.Targets[0].ChildID, func(r model.Record) { r[colResvActual] = int64(1) })
			default:
				mutateAttemptTargets(t, st, tenant, req.AttemptRef, func(ts []jsonTarget) {
					n := jsonInt(1)
					switch mode {
					case "finite nil variant":
						ts[0].Action = "block"
					case "present limit":
						ts[0].LimitMicroUSD = &n
					case "static":
						ts[0].StaticReservedMicroUSD = &n
					case "version":
						ts[0].PolicyVersion = nil
					}
				})
			}
			if _, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("incomplete unlimited witness: %v", err)
			}
			if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("witness replay: %v", err)
			}
			if held := reservedFor(t, m, st, tenant, p.ID, req.Binding.Subject.ActorRef); held.State != reservedIndeterminate {
				t.Fatalf("witness hold certified: %+v", held)
			}
		})
	}
}

func TestT0NoAdmissionOverPreexistingOrphan(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		t.Run(fmt.Sprint(unlimited), func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			req.Binding.ApplySeatLimits = unlimited
			v.approve(req)
			t0Active(t, m, st, tenant)
			if unlimited {
				t0Policy(t, st, tenant, policyKindSpendLimit, storedSpendLimitSpec{ScopeType: "user", ScopeKey: req.Binding.Subject.ActorRef, Period: "daily", Unlimited: true}.mapValue())
			}
			orphan := legacyChild(model.NewID(), model.NewID(), "", "monthly", 1, 7, baseTime, baseTime.Add(time.Hour), resvStateActive)
			orphan[colResvAttemptRef] = string(req.AttemptRef)
			orphan[colResvLifecycleVersion] = lifecycleLinkageVersion
			seedGroup(t, st, tenant, orphan)
			before := countReservations(t, st, tenant)
			if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeLedgerIndeterminate {
				t.Fatalf("orphan admitted: %v", err)
			}
			if len(attemptRows(t, st, tenant)) != 0 || !reflect.DeepEqual(before, countReservations(t, st, tenant)) {
				t.Fatal("orphan refusal left new rows")
			}
		})
	}
}

func TestT0PreparedReaderPaging(t *testing.T) {
	for _, mode := range []string{"progressing", "repeated clean prefix", "contradictory repeat", "complete duplicate"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
			t0Active(t, m, st, tenant)
			view := t0Admit(t, m, tenant, req)
			rows := countReservations(t, st, tenant)
			m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
				return t0Scope{Scope: sc, now: baseTime, ext: func(kind model.Kind, repo store.GenericRepo) store.GenericRepo {
					if kind != budgetReservationKind {
						return repo
					}
					return t0Repo{GenericRepo: repo, list: func(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
						if mode == "complete duplicate" {
							return []model.Record{rows[0], rows[0]}, model.Page{}, nil
						}
						if q.Cursor == "" {
							return rows, model.Page{HasMore: true, Cursor: "next"}, nil
						}
						if mode == "progressing" {
							return nil, model.Page{}, nil
						}
						if mode == "contradictory repeat" {
							r := model.Record{}
							for k, v := range rows[0] {
								r[k] = v
							}
							r[colResvAmount] = int64(9)
							return []model.Record{r}, model.Page{HasMore: true, Cursor: "next"}, nil
						}
						return rows, model.Page{HasMore: true, Cursor: "next"}, nil
					}}
				}}
			}})
			got, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef)
			if mode == "progressing" {
				if err != nil || got.ID != view.ID {
					t.Fatalf("complete read: %v", err)
				}
				return
			}
			want := errCodeLedgerIndeterminate
			if mode == "repeated clean prefix" {
				want = errCodeLedgerIncomplete
			}
			if attemptCode(err) != want {
				t.Fatalf("paging: %v", err)
			}
			if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != want {
				t.Fatalf("replay paging: %v", err)
			}
		})
	}
}

func TestT0SequenceAndPersistedMoneyBound(t *testing.T) {
	for _, mode := range []string{"sequence overflow", "sequence malformed", "persisted unsafe money"} {
		t.Run(mode, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			v := t0Wire(m, tenant)
			req := t0Request(t, st, tenant)
			v.approve(req)
			limit := int64(100)
			if mode == "persisted unsafe money" {
				limit = (1 << 53) + 1
			}
			p := t0Policy(t, st, tenant, policyKindBudget, t0Budget(limit, "block"))
			t0Active(t, m, st, tenant)
			if mode != "persisted unsafe money" {
				m.UseData(t0Data{ModuleData: m.data, wrap: func(sc store.Scope) store.Scope {
					return t0Scope{Scope: sc, now: baseTime, ext: func(kind model.Kind, repo store.GenericRepo) store.GenericRepo {
						if kind != budgetReservationKind {
							return repo
						}
						return t0Repo{GenericRepo: repo, list: func(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
							if q.Limit != 1 {
								return repo.List(ctx, q)
							}
							row := legacyChild(p.ID, model.NewID(), "", "monthly", math.MaxInt64, 7, baseTime, baseTime.Add(time.Hour), resvStateActive)
							row[model.ColTenantID] = tenant.String()
							if mode == "sequence malformed" {
								row[colResvSeq] = "not-an-integer"
							}
							return []model.Record{row}, model.Page{}, nil
						}}
					}}
				}})
			}
			_, err := m.prepareAttempt(context.Background(), tenant, req)
			want := errCodeLedgerIndeterminate
			if mode == "sequence overflow" {
				want = errCodeArithmetic
			}
			if attemptCode(err) != want {
				t.Fatalf("money/sequence boundary: %v", err)
			}
			t0NoRows(t, st, tenant)
		})
	}
}

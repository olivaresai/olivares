// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// ponytail: the composition root's integration tests drive the inference PEP with the same
// fakes as internal/inferencepep's own tests; these are a copy of those (inferenceproxy_test.go,
// inferenceproxy_store_test.go, servertoolegressgate_test.go there). Make one exported test
// package when a third package needs them or the copies drift.

type fakeProxyAuthr struct {
	p   auth.Principal
	err error
}

func (f fakeProxyAuthr) Authenticate(context.Context, string) (auth.Principal, error) {
	return f.p, f.err
}

type fakeProxyModels struct {
	v         models.ModelAccessVerdict
	err       error
	calls     int
	denyModel string // when set, deny only this modelRef (per-entry batch fidelity test)
}

func (f *fakeProxyModels) EvaluateModelAccess(_ context.Context, _ model.TenantID, _ auth.Principal, _, _, modelRef, _ string) (models.ModelAccessVerdict, error) {
	f.calls++
	if f.err != nil {
		return models.ModelAccessVerdict{}, f.err
	}
	if f.denyModel != "" && modelRef == f.denyModel {
		return models.ModelAccessVerdict{Allowed: false, Reason: "model not granted on this surface"}, nil
	}
	return f.v, nil
}

type fakeProxyBudget struct {
	bc       finops.BudgetCheck
	err      error
	calls    int
	spend    finops.SpendLimitCheck
	spendErr error
	actor    string
	groups   []string
	// keys records every idempotency key the gate presented, in order, so a test can ask
	// whether two calls presented themselves as one.
	keys []string
	// held are the holds the fake issued, in order; committed and released are the
	// settlements the proxy made of them.
	held      []string
	committed []proxySettlement
	released  []string
	// settleCtxErrs records, for each settlement, the error of the context it ran under:
	// nil unless that context was already done.
	settleCtxErrs []error
}

// proxySettlement is one Commit the proxy made: the hold and the amount.
type proxySettlement struct {
	handle string
	actual int64
}

func (f *fakeProxyBudget) CheckBudget(context.Context, model.TenantID, finops.SpendDims) (finops.BudgetCheck, error) {
	f.calls++
	return f.bc, f.err
}

func (f *fakeProxyBudget) CheckSpendLimit(_ context.Context, _ model.TenantID, actor string, groups []string) (finops.SpendLimitCheck, error) {
	f.actor = actor
	f.groups = append([]string(nil), groups...)
	if !f.spend.Allowed && f.spend.SpendLimitID == "" && f.spendErr == nil {
		return finops.SpendLimitCheck{Allowed: true}, nil
	}
	return f.spend, f.spendErr
}

// Reserve answers as the module does: a spend-limit or budget refusal, the deny-closed
// refusal of a ledger it could not read, or an admission that holds a fresh hold whenever
// the call has an amount to hold.
func (f *fakeProxyBudget) Reserve(_ context.Context, _ model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error) {
	f.calls++
	f.keys = append(f.keys, req.IdempotencyKey)
	f.actor = req.ActorRef
	f.groups = append([]string(nil), req.Groups...)
	if f.spendErr != nil {
		return finops.Reservation{
			Allowed: false, Action: "block", Reason: finops.ReasonStoreUnreachable,
			EstimateMicroUSD: req.EstimateMicroUSD,
		}, nil
	}
	if !f.spend.Allowed && f.spend.SpendLimitID != "" {
		return finops.Reservation{Allowed: false, Action: "block", SpendLimit: true, EstimateMicroUSD: req.EstimateMicroUSD}, nil
	}
	res, err := fakeAdmissionReserve(f.bc, f.err, req)
	if err == nil && res.Allowed && req.EstimateMicroUSD > 0 {
		res.Handle = model.NewID().String()
		f.held = append(f.held, res.Handle)
	}
	return res, err
}

func (f *fakeProxyBudget) Commit(ctx context.Context, _ model.TenantID, handle string, actual int64) error {
	f.committed = append(f.committed, proxySettlement{handle: handle, actual: actual})
	f.settleCtxErrs = append(f.settleCtxErrs, ctx.Err())
	return nil
}

func (f *fakeProxyBudget) Release(ctx context.Context, _ model.TenantID, handle string) error {
	f.released = append(f.released, handle)
	f.settleCtxErrs = append(f.settleCtxErrs, ctx.Err())
	return nil
}

type fakeProxyKill struct {
	st  governance.StopState
	err error
}

func (f fakeProxyKill) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return f.st, f.err
}

type fakeProxyPolicy struct {
	pol inferenceproxy.ProxyPolicy
	err error
}

func (f fakeProxyPolicy) Policy(context.Context, model.TenantID) (inferenceproxy.ProxyPolicy, error) {
	return f.pol, f.err
}

type fakeObservationBus struct {
	mu     sync.Mutex
	events []event.Event
}

func (b *fakeObservationBus) Publish(_ context.Context, e event.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return nil
}

func (b *fakeObservationBus) findings(kind string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, e := range b.events {
		if f, ok := event.FindingOf(e); ok && f.Kind == kind {
			out = append(out, f.Kind)
		}
	}
	return out
}

func (b *fakeObservationBus) findingReports(kind string) []sdkmodel.FindingReport {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []sdkmodel.FindingReport
	for _, e := range b.events {
		if f, ok := event.FindingOf(e); ok && f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func (b *fakeObservationBus) costs() []sdkmodel.CostSample {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []sdkmodel.CostSample
	for _, e := range b.events {
		if c, ok := event.CostOf(e); ok {
			out = append(out, c)
		}
	}
	return out
}

type fakeCountTokensDoer struct {
	inputTokens int64
	calls       int
	// gotBody captures the LAST count_tokens request body, so tests can assert the
	// sizing measures the GOVERNED request (post gate rewrites —).
	gotBody []byte
}

func (d *fakeCountTokensDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	status := http.StatusOK
	body := `{"input_tokens":` + strconv.FormatInt(d.inputTokens, 10) + `}`
	if req.URL.Path != "/v1/messages/count_tokens" {
		status = http.StatusNotFound
		body = `{"error":{"message":"unexpected path"}}`
	} else if req.Body != nil {
		d.gotBody, _ = io.ReadAll(req.Body)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

const proxyTestTenant = model.TenantID("acme")

func proxyTestPrincipal() auth.Principal {
	return auth.ScopedPrincipal(model.ID("u1"), "user one", proxyTestTenant, "editor")
}

func allGatesOnExceptDLPAndCtx() inferenceproxy.ProxyPolicy {
	return inferenceproxy.ProxyPolicy{
		GateModelAccess: true, GateBudget: true,
		GateResidency: false, GateContextWindow: false,
		GateDLPRequest: false, GateDLPResponse: false,
		ResponseDLPMode: inferenceproxy.ResponseDLPFlag,
	}
}

// newTestDecider wires a decider over the narrow fakes. authr/pol take the PRODUCTION seam
// interfaces so the F2 seam tests can substitute counting fakes without touching the
// 50+ existing call-sites (the value-receiver fakes satisfy them unchanged).
func newTestDecider(authr inferencepep.Authenticator, mg *fakeProxyModels, bg *fakeProxyBudget, kg fakeProxyKill, pol inferencepep.PolicySource) *inferencepep.Decider {
	return &inferencepep.Decider{
		Surface: "direct", SurfaceGeo: "",
		Inference: nil, Auth: authr, Models: mg, Budget: bg, KillSwitch: kg, Policy: pol,
		Residency: nil, Store: nil, Bus: nil,
		Clock: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func proxyCountTokensInference(tokens int64) (*claudeapi.Inference, *fakeCountTokensDoer) {
	doer := &fakeCountTokensDoer{inputTokens: tokens}
	return claudeapi.NewInference(claudeapi.InferenceConfig{
		APIKey: "k-inference", Gateway: "direct", DefaultModel: "claude-opus-4-8", Doer: doer,
	}), doer
}

func userReq(text string, stream bool) claudeapi.MessageRequest {
	return claudeapi.MessageRequest{
		Model: "claude-opus-4-8", MaxTokens: 16, Stream: stream,
		Messages: []claudeapi.Message{{Role: "user", Content: []claudeapi.ContentBlock{claudeapi.TextBlock(text)}}},
	}
}

func allowAll() (fakeProxyAuthr, *fakeProxyModels, *fakeProxyBudget, fakeProxyKill, fakeProxyPolicy) {
	return fakeProxyAuthr{p: proxyTestPrincipal()},
		&fakeProxyModels{v: models.ModelAccessVerdict{Allowed: true}},
		&fakeProxyBudget{bc: finops.BudgetCheck{Allowed: true}},
		fakeProxyKill{},
		fakeProxyPolicy{pol: allGatesOnExceptDLPAndCtx()}
}

// batchReqs builds a batch of entries, one per model id (custom_id = c0, c1, …).
func batchReqs(models ...string) []claudeapi.BatchRequest {
	out := make([]claudeapi.BatchRequest, len(models))
	for i, m := range models {
		r := userReq("hi", false)
		r.Model = m
		out[i] = claudeapi.BatchRequest{CustomID: "c" + strconv.Itoa(i), Params: r}
	}
	return out
}

// countTokensDoer answers any POST (the /v1/messages/count_tokens pre-flight) with a tiny
// input-token count, so the context-window gate passes without a real upstream.
type countTokensDoer struct{}

func (countTokensDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":5}`)), Header: http.Header{}}, nil
}

// provisionTenant opens an in-memory store with the inferenceproxy schema and creates one
// org with the given residency pin (empty = unpinned).
func provisionTenant(t *testing.T, ipx *inferenceproxy.Module, dataRegion string) (store.Store, model.TenantID) {
	return provisionTenantWithConfig(t, ipx, dataRegion, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
}

func provisionTenantWithConfig(t *testing.T, ipx *inferenceproxy.Module, dataRegion string, cfg store.Config) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, cfg, ipx.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive, DataRegion: dataRegion})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	ipx.UseData(api.NewModuleData(st))
	return st, tenant
}

func storeBackedDecider(st store.Store, tenant model.TenantID, ipx *inferenceproxy.Module, surfaceGeo string, reg *residency.Registry, inf *claudeapi.Inference) *inferencepep.Decider {
	return &inferencepep.Decider{
		Surface: "direct", SurfaceGeo: surfaceGeo,
		Inference:  inf,
		Auth:       fakeProxyAuthr{p: auth.ScopedPrincipal(model.ID("u1"), "u1", tenant, "editor")},
		Models:     &fakeProxyModels{v: models.ModelAccessVerdict{Allowed: true}},
		Budget:     &fakeProxyBudget{bc: finops.BudgetCheck{Allowed: true}},
		KillSwitch: fakeProxyKill{},
		Policy:     ipx,
		Residency:  reg,
		Store:      st,
		Bus:        nil,
		Clock:      time.Now,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type fakeEgressGate struct {
	dec   claudeapi.ServerToolEgressDecision
	gotIn claudeapi.ServerToolEgressInput
	calls int
}

func (f *fakeEgressGate) GovernEgress(_ context.Context, in claudeapi.ServerToolEgressInput) claudeapi.ServerToolEgressDecision {
	f.calls++
	f.gotIn = in
	return f.dec
}

func reqWithTools(tools ...any) claudeapi.MessageRequest {
	r := userReq("hi", false)
	r.Tools = tools
	return r
}

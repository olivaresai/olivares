// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// --- fakes (the decider's seams are narrow interfaces) ------------------------------

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

type fakeContextPolicy struct {
	pol   knowledge.EffectivePolicy
	err   error
	calls int
	last  knowledge.ContextPolicyQuery
}

func (f *fakeContextPolicy) Apply(_ context.Context, _ model.TenantID, q knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error) {
	f.calls++
	f.last = q
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

// --- helpers ------------------------------------------------------------------------

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
func newTestDecider(authr principalAuthenticator, mg *fakeProxyModels, bg *fakeProxyBudget, kg fakeProxyKill, pol proxyPolicySource) *inferenceProxyDecider {
	return &inferenceProxyDecider{
		surface: "direct", surfaceGeo: "",
		inf: nil, authr: authr, models: mg, budget: bg, killSwitch: kg, policy: pol,
		residency: nil, store: nil, bus: nil,
		clock: time.Now, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
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

// --- AuthorizeBatch tests (per-entry deny-closed 2026-06-19 D1) ---------------

func TestProxyAuthorizeBatchAllAllowed(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-opus-4-8"), "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if len(dec.Requests) != 2 {
		t.Errorf("governed entries = %d, want 2", len(dec.Requests))
	}
	if dec.Session == nil {
		t.Error("an allowed batch must carry a session for FinalizeBatch")
	}
	// Per-entry fidelity: the model-access gate ran for EACH entry (not once for the envelope).
	if mg.calls != 2 {
		t.Errorf("model-access calls = %d, want 2 (per-entry chain)", mg.calls)
	}
}

func TestProxyAuthorizeBatchOneDeniedEntryDeniesWholeBatch(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	mg.denyModel = "blocked-model"
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "blocked-model"), "bearer")
	if dec.Allow {
		t.Fatal("a batch with ONE denied entry must be denied as a whole (deny-closed)")
	}
	if dec.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", dec.Status)
	}
	// The deny names the offending entry's custom_id (actionable, minimal-data).
	if !strings.Contains(dec.Reason, "c1") {
		t.Errorf("deny reason must name the denied entry (c1); got %q", dec.Reason)
	}
}

func TestProxyAuthorizeBatchEmptyRejected(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.AuthorizeBatch(context.Background(), nil, "bearer")
	if dec.Allow || dec.Status != http.StatusBadRequest {
		t.Fatalf("an empty batch must 400; got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

func TestProxyAuthorizeBatchKillSwitchDeniesWholeBatch(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	kg.st = governance.StopState{EstateStopped: true, EstateStopID: model.ID("stop-1")}
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.AuthorizeBatch(context.Background(), batchReqs("claude-opus-4-8", "claude-opus-4-8"), "bearer")
	if dec.Allow {
		t.Fatal("an active estate stop must deny the whole batch (the same chain as a single message)")
	}
}

// --- Authorize gate-chain tests -----------------------------------------------------

func TestProxyAuthorizeHappyPath(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if !dec.Allow {
		t.Fatalf("expected allow; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if dec.Session == nil {
		t.Error("allow must carry a session for Finalize")
	}
	if dec.BufferResponse {
		t.Error("buffer must be false for a non-streaming, DLP-off policy")
	}
}

func TestProxyAuthorizeUnsetCeilingsLeavesRequestUntouched(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, pol)
	d.bus = bus
	req := userReq("hi", false)
	req.MaxTokens = 1_000_000
	before := mustJSON(t, req)
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("unset ceilings must allow; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if after := mustJSON(t, dec.Request); after != before {
		t.Fatalf("request changed with unset ceilings:\nwant %s\ngot  %s", before, after)
	}
	if got := bus.findings("inference_request_ceiling"); len(got) != 0 {
		t.Fatalf("unset ceilings emitted finding(s): %v", got)
	}
}

func TestProxyAuthorizeCeilingsObserveFindingAndForwardUnchanged(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.Ceilings = inferenceproxy.RequestCeilings{MaxTokens: 10}
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.bus = bus
	req := userReq("hi", false)
	req.MaxTokens = 100
	before := mustJSON(t, req)
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("observe ceilings must forward; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if after := mustJSON(t, dec.Request); after != before {
		t.Fatalf("observe mode changed request:\nwant %s\ngot  %s", before, after)
	}
	if got := bus.findings("inference_request_ceiling"); len(got) != 1 {
		t.Fatalf("ceiling findings = %d, want 1", len(got))
	}
}

func TestProxyAuthorizeCeilingsEnforceMaxTokensDenies402(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxTokens: 10}
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.bus = bus
	req := userReq("hi", false)
	req.MaxTokens = 100
	dec := d.Authorize(context.Background(), req, "bearer")
	if dec.Allow || dec.Status != http.StatusPaymentRequired || dec.ErrorType != "billing_error" {
		t.Fatalf("max_tokens ceiling must deny 402 billing_error; got allow=%v status=%d type=%q", dec.Allow, dec.Status, dec.ErrorType)
	}
	if got := dec.Headers["x-should-retry"]; got != "false" {
		t.Fatalf("x-should-retry = %q, want false", got)
	}
	if bg.calls != 0 {
		t.Fatalf("budget gate calls = %d, want 0 after request-ceiling deny", bg.calls)
	}
	// An enforce-mode deny is evidenced too (never a silent governance action).
	if got := bus.findings("inference_request_ceiling"); len(got) != 1 {
		t.Fatalf("enforce deny must emit one ceiling finding, got %d", len(got))
	}
}

func TestProxyAuthorizeContextPolicyCeilingDenies413(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.bus = bus
	ctxPolicy := &fakeContextPolicy{pol: knowledge.EffectivePolicy{MaxContextTokens: 10, WinningScope: "user_group:engineering"}}
	d.contextPolicy = ctxPolicy
	d.inf, _ = proxyCountTokensInference(25)

	req := userReq("hi", false)
	before := mustJSON(t, req)
	dec := d.Authorize(context.Background(), req, "bearer")
	if dec.Allow || dec.Status != http.StatusRequestEntityTooLarge || dec.ErrorType != "invalid_request_error" {
		t.Fatalf("context-policy ceiling must deny 413 invalid_request_error; got allow=%v status=%d type=%q", dec.Allow, dec.Status, dec.ErrorType)
	}
	if got := dec.Headers["x-should-retry"]; got != "false" {
		t.Fatalf("x-should-retry = %q, want false", got)
	}
	if after := mustJSON(t, req); after != before {
		t.Fatalf("context-policy ceiling must not mutate request:\nwant %s\ngot  %s", before, after)
	}
	if ctxPolicy.calls != 1 {
		t.Fatalf("context policy calls = %d, want 1", ctxPolicy.calls)
	}
	if ctxPolicy.last.Model != req.Model || ctxPolicy.last.Principal.Actor() != a.p.Actor() {
		t.Fatalf("context policy query = %+v, want principal + model", ctxPolicy.last)
	}
	if bg.calls != 0 {
		t.Fatalf("budget gate calls = %d, want 0 after context-policy deny", bg.calls)
	}
	if got := bus.findings("inference_context_ceiling"); len(got) != 1 {
		t.Fatalf("context ceiling findings = %d, want 1", len(got))
	}
}

func TestProxyAuthorizeContextPolicyForbidDenies403(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.contextPolicy = &fakeContextPolicy{pol: knowledge.EffectivePolicy{Deny: true}}
	var countDoer *fakeCountTokensDoer
	d.inf, countDoer = proxyCountTokensInference(25)

	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden || dec.ErrorType != "permission_error" {
		t.Fatalf("forbid context-policy must deny 403 permission_error; got allow=%v status=%d type=%q", dec.Allow, dec.Status, dec.ErrorType)
	}
	if countDoer.calls != 0 {
		t.Fatalf("CountTokens calls = %d, want 0 after definitive context-policy deny", countDoer.calls)
	}
	if bg.calls != 0 {
		t.Fatalf("budget gate calls = %d, want 0 after context-policy deny", bg.calls)
	}
}

func TestProxyAuthorizeContextPolicyReadErrorHonorsFailOpen(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failOpen  bool
		wantAllow bool
		wantCode  int
	}{
		{name: "fail closed", failOpen: false, wantAllow: false, wantCode: http.StatusServiceUnavailable},
		{name: "fail open", failOpen: true, wantAllow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, mg, bg, kg, _ := allowAll()
			base := allGatesOnExceptDLPAndCtx()
			base.GateContextWindow = true
			base.FailOpen = tc.failOpen
			bus := &fakeObservationBus{}
			d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
			d.bus = bus
			d.contextPolicy = &fakeContextPolicy{
				pol: knowledge.EffectivePolicy{MaxContextTokens: 1, WinningScope: "tenant:acme"},
				err: errBootInferenceProxy("context policy store down"),
			}
			var countDoer *fakeCountTokensDoer
			d.inf, countDoer = proxyCountTokensInference(12)

			dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
			if tc.wantAllow {
				if !dec.Allow {
					t.Fatalf("fail_open=true must allow on context-policy READ FAULT; got deny status=%d", dec.Status)
				}
				if countDoer.calls != 1 {
					t.Fatalf("CountTokens calls = %d, want 1 after fail-open context-policy outage", countDoer.calls)
				}
				if got := bus.findingReports("inference_proxy_failed_open"); len(got) != 1 {
					t.Fatalf("fail-open outage findings = %d, want 1", len(got))
				} else if got[0].Severity != sdkmodel.SeverityHigh || got[0].SubjectRef != "context_policy" {
					t.Fatalf("fail-open outage finding must be HIGH and gate-attributed: %+v", got[0])
				}
				return
			}
			if dec.Allow || dec.Status != tc.wantCode || dec.ErrorType != "api_error" {
				t.Fatalf("context-policy read error must deny %d api_error; got allow=%v status=%d type=%q", tc.wantCode, dec.Allow, dec.Status, dec.ErrorType)
			}
			if countDoer.calls != 0 {
				t.Fatalf("CountTokens calls = %d, want 0 after fail-closed context-policy outage", countDoer.calls)
			}
		})
	}
}

func TestProxyAuthorizeContextPolicyZeroCeilingPasses(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.bus = bus
	ctxPolicy := &fakeContextPolicy{pol: knowledge.EffectivePolicy{}}
	d.contextPolicy = ctxPolicy
	var countDoer *fakeCountTokensDoer
	d.inf, countDoer = proxyCountTokensInference(99)

	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if !dec.Allow {
		t.Fatalf("zero context-policy ceiling must pass; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	if ctxPolicy.calls != 1 {
		t.Fatalf("context policy calls = %d, want 1", ctxPolicy.calls)
	}
	if countDoer.calls != 1 {
		t.Fatalf("CountTokens calls = %d, want 1", countDoer.calls)
	}
	if got := bus.findings("inference_context_ceiling"); len(got) != 0 {
		t.Fatalf("zero ceiling emitted context ceiling finding(s): %v", got)
	}
}

func TestProxyAuthorizeCeilingsEnforceTaskBudget(t *testing.T) {
	t.Run("violation denies", func(t *testing.T) {
		a, mg, bg, kg, _ := allowAll()
		base := allGatesOnExceptDLPAndCtx()
		base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, TaskBudgetTokens: 30000}
		d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
		req := userReq("hi", false)
		req.OutputConfig = &claudeapi.OutputConfig{TaskBudget: &claudeapi.TaskBudget{Type: "tokens", Total: 40000}}
		dec := d.Authorize(context.Background(), req, "bearer")
		if dec.Allow || dec.Status != http.StatusPaymentRequired {
			t.Fatalf("task_budget ceiling must deny 402; got allow=%v status=%d", dec.Allow, dec.Status)
		}
	})
	t.Run("absent budget is injected", func(t *testing.T) {
		a, mg, bg, kg, _ := allowAll()
		base := allGatesOnExceptDLPAndCtx()
		base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, TaskBudgetTokens: 30000}
		d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if !dec.Allow {
			t.Fatalf("absent task_budget should be injected and allowed; got deny status=%d", dec.Status)
		}
		got := dec.Request.OutputConfig
		if got == nil || got.TaskBudget == nil || got.TaskBudget.Type != "tokens" || got.TaskBudget.Total != 30000 {
			t.Fatalf("injected output_config = %+v", got)
		}
	})
	t.Run("existing compliant budget is not overwritten", func(t *testing.T) {
		a, mg, bg, kg, _ := allowAll()
		base := allGatesOnExceptDLPAndCtx()
		base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, TaskBudgetTokens: 30000}
		d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
		req := userReq("hi", false)
		req.OutputConfig = &claudeapi.OutputConfig{TaskBudget: &claudeapi.TaskBudget{Type: "tokens", Total: 25000}}
		dec := d.Authorize(context.Background(), req, "bearer")
		if !dec.Allow {
			t.Fatalf("compliant task_budget should allow; got deny status=%d", dec.Status)
		}
		if dec.Request.OutputConfig.TaskBudget.Total != 25000 {
			t.Fatalf("task_budget total = %d, want original 25000", dec.Request.OutputConfig.TaskBudget.Total)
		}
	})
}

func TestProxyAuthorizeCeilingsEnforceToolClampAndInjection(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxToolUses: 3}
	bus := &fakeObservationBus{}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	d.bus = bus
	req := userReq("hi", false)
	req.Tools = []any{
		map[string]any{"type": "web_search_20250305", "max_uses": 9},
		map[string]any{"type": "web_search_20250305"},
		map[string]any{"type": "custom_tool"},
	}
	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("tool max_uses clamp should allow; got deny status=%d reason=%q", dec.Status, dec.Reason)
	}
	// The clamp is a governed rewrite and must be evidenced (egress-gate precedent).
	if got := bus.findings("inference_request_ceiling"); len(got) != 1 {
		t.Fatalf("enforce clamp must emit one ceiling finding, got %d", len(got))
	}
	tools := dec.Request.Tools
	if tools[0].(map[string]any)["max_uses"] != 3 {
		t.Fatalf("tool 0 max_uses = %v, want 3", tools[0].(map[string]any)["max_uses"])
	}
	if tools[1].(map[string]any)["max_uses"] != 3 {
		t.Fatalf("tool 1 max_uses = %v, want injected 3", tools[1].(map[string]any)["max_uses"])
	}
	if _, ok := tools[2].(map[string]any)["max_uses"]; ok {
		t.Fatalf("non-web tool got max_uses injected: %+v", tools[2])
	}
}

func TestProxyAuthorizeBatchCeilingDenyNamesEntryIndex(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.Ceilings = inferenceproxy.RequestCeilings{Enforce: true, MaxTokens: 10}
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	good := userReq("hi", false)
	good.MaxTokens = 10
	bad := userReq("hi", false)
	bad.MaxTokens = 100
	dec := d.AuthorizeBatch(context.Background(), []claudeapi.BatchRequest{{Params: good}, {Params: bad}}, "bearer")
	if dec.Allow || dec.Status != http.StatusPaymentRequired || dec.ErrorType != "billing_error" {
		t.Fatalf("batch ceiling violation must deny whole batch with 402; got allow=%v status=%d type=%q", dec.Allow, dec.Status, dec.ErrorType)
	}
	if !strings.Contains(dec.Reason, "#1") {
		t.Fatalf("batch deny reason = %q, want entry index #1", dec.Reason)
	}
	if got := dec.Headers["x-should-retry"]; got != "false" {
		t.Fatalf("x-should-retry = %q, want false", got)
	}
}

func TestProxyAuthorizeAuthFailDenies(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	a.err = auth.ErrUnauthenticated // a genuinely-invalid credential
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusUnauthorized {
		t.Fatalf("a bad credential must deny 401; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if mg.calls != 0 || bg.calls != 0 {
		t.Error("no downstream gate must run after an auth failure")
	}
}

// TestProxyAuthorizeAuthPlaneFaultFailsClosed pins the F2 split: an auth-time PLANE
// fault (the authenticator propagates a raw store error, NOT ErrUnauthenticated) denies
// closed with 503 — never a 401 "bad credential" — consistent with every other
// unreadable-plane gate.
func TestProxyAuthorizeAuthPlaneFaultFailsClosed(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	a.err = errBootInferenceProxy("auth store down") // NOT ErrUnauthenticated
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("an auth-plane outage must fail closed 503, not 401; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if mg.calls != 0 || bg.calls != 0 {
		t.Error("no downstream gate must run after an auth-plane fault")
	}
}

func TestProxyAuthorizeUnresolvableTenantDenies(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	a.p = auth.Principal{Kind: auth.KindUser, CredID: "stranger"} // no tenant grants
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("unresolvable tenant must deny 403; got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

// TestProxyAuthorizePolicyErrorFailsClosed pins the REASON as well as the 503. The status
// alone is not the contract: on this same call an auth-plane outage, an incomplete
// identity resolution, an unreadable kill-switch and several later gates all deny 503, so
// `dec.Status == 503` is satisfied by refusals that have nothing to do with the governance
// config. The proxy names its gate (gateCodePolicyUnreadable) and states it in the
// decision text; assert that, so no other deny-closed gate can pass for this one.
//
// The stable gateCode does NOT survive Authorize: gateResult carries code+class, but
// Authorize returns claudeapi.ProxyDecision, which has no code field
// (connectors/claude-api/proxy.go). So across THAT boundary the reason text is the only
// discriminant — which is why the check below also drops one level down to the seam that
// still returns the code, and pins it. Prose is the weaker contract of the two; asserting
// both covers the public shape a client sees AND the gate identity, without adding a
// field to a shared wire type.
func TestProxyAuthorizePolicyErrorFailsClosed(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	pol.err = errBootInferenceProxy("store down")
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable governance config must FAIL CLOSED (503); got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if !strings.Contains(dec.Reason, "governance configuration") {
		t.Fatalf("the deny must name the governance-config read fault so another 503 gate cannot pass for it; got %q", dec.Reason)
	}
	if _, _, deny, ok := d.authorizeChain(context.Background(), userReq("hi", false), "bearer"); ok || deny.code != gateCodePolicyUnreadable {
		t.Fatalf("the deny must carry the stable %q code, not just matching prose; got ok=%v code=%q",
			gateCodePolicyUnreadable, ok, deny.code)
	}
	// The check above compares the constant with itself, so it catches a return-site using
	// the WRONG code but not a rename of this code's VALUE — and the value is the
	// wire-visible identifier a PDP mapping consumes. Pin the literal separately.
	if gateCodePolicyUnreadable != "policy_unreadable" {
		t.Fatalf("gateCodePolicyUnreadable is a stable wire identifier, not free to rename; got %q", gateCodePolicyUnreadable)
	}
}

func TestProxyAuthorizeKillSwitchErrorFailsClosed(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	kg.err = errBootInferenceProxy("killswitch unreadable")
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable kill-switch must FAIL CLOSED; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if mg.calls != 0 {
		t.Error("model-access must not run after a kill-switch read error")
	}
}

func TestProxyAuthorizeKillSwitchStoppedOutranksEverything(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	kg.st = governance.StopState{EstateStopped: true, EstateStopID: model.ID("stop-1")}
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("an active estate stop must deny; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	// Order proof: the kill-switch runs BEFORE model-access AND budget.
	if mg.calls != 0 || bg.calls != 0 {
		t.Errorf("a stop must short-circuit before model-access/budget; mg=%d bg=%d", mg.calls, bg.calls)
	}
}

func TestProxyAuthorizeModelAccessDenies(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	mg.v = models.ModelAccessVerdict{Allowed: false, Reason: "model not granted on this surface"}
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("a model-access deny must 403; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if bg.calls != 0 {
		t.Error("budget (fail-open) must NOT run after a security deny")
	}
}

func TestProxyAuthorizeModelAccessErrorFailsClosed(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	mg.err = errBootInferenceProxy("grant read failed")
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("a model-access read error must FAIL CLOSED (403); got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

func TestProxyAuthorizeBudgetBlockAndThrottle(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   int
	}{
		{"block", http.StatusPaymentRequired},
		{"throttle", http.StatusTooManyRequests},
	} {
		a, mg, bg, kg, pol := allowAll()
		bg.bc = finops.BudgetCheck{Allowed: false, Action: tc.action, BudgetID: "b1"}
		d := newTestDecider(a, mg, bg, kg, pol)
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if dec.Allow || dec.Status != tc.want {
			t.Fatalf("budget %s must deny %d; got allow=%v status=%d", tc.action, tc.want, dec.Allow, dec.Status)
		}
		if dec.Reason != "" && (containsMoney(dec.Reason)) {
			t.Errorf("budget deny reason must be money-free; got %q", dec.Reason)
		}
	}
}

func TestProxyAuthorizeBudgetReadErrorFailsOPEN(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	bg.err = errBootInferenceProxy("finops read failed")
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if !dec.Allow {
		t.Fatalf("a budget READ ERROR must FAIL OPEN (allow); got deny status=%d", dec.Status)
	}
}

func TestProxyAuthorizeSpendLimitDenyAndFailOpen(t *testing.T) {
	t.Run("at cap denies 402", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		bg.spend = finops.SpendLimitCheck{Allowed: false, SpendLimitID: "spl_cap"}
		d := newTestDecider(a, mg, bg, kg, pol)
		dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
		if dec.Allow || dec.Status != http.StatusPaymentRequired || dec.ErrorType != "billing_error" || dec.Reason != "spend limit reached" || dec.Headers["x-should-retry"] != "false" {
			t.Fatalf("spend-limit deny = %+v", dec)
		}
		if bg.actor != a.p.Actor() {
			t.Fatalf("actor = %q, want %q", bg.actor, a.p.Actor())
		}
	})
	t.Run("under cap passes", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		bg.spend = finops.SpendLimitCheck{Allowed: true}
		if dec := newTestDecider(a, mg, bg, kg, pol).Authorize(context.Background(), userReq("hi", false), "bearer"); !dec.Allow {
			t.Fatalf("under-cap request denied: %+v", dec)
		}
	})
	t.Run("read error fails open", func(t *testing.T) {
		a, mg, bg, kg, pol := allowAll()
		bg.spendErr = errors.New("spend store unavailable")
		if dec := newTestDecider(a, mg, bg, kg, pol).Authorize(context.Background(), userReq("hi", false), "bearer"); !dec.Allow {
			t.Fatalf("spend-limit read error must fail open: %+v", dec)
		}
	})
}

// TestProxyAuthorizeModelAccessErrorFailOpenAllows proves the per-tenant fail_open knob
// is CONSUMED: with fail_open=true, a model-access decision-plane READ FAULT lets the
// request through (ungoverned for that gate) instead of denying.
func TestProxyAuthorizeModelAccessErrorFailOpenAllows(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	mg.err = errBootInferenceProxy("grant read failed")
	base := allGatesOnExceptDLPAndCtx()
	base.FailOpen = true
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if !dec.Allow {
		t.Fatalf("fail_open=true must allow on a model-access READ FAULT; got deny status=%d", dec.Status)
	}
}

// TestProxyAuthorizeFailOpenDoesNotBypassDefiniteDeny proves fail_open only covers a read
// OUTAGE, never a clear deny verdict: a definitive !Allowed still denies.
func TestProxyAuthorizeFailOpenDoesNotBypassDefiniteDeny(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	mg.v = models.ModelAccessVerdict{Allowed: false, Reason: "not granted"}
	base := allGatesOnExceptDLPAndCtx()
	base.FailOpen = true
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("a definitive model-access deny must 403 even with fail_open=true; got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

// TestProxyAuthorizeAlwaysEnforcesNoConstrainedObserveShadow pins the F1 invariant: the
// inference PDP is ALWAYS-ENFORCE. Constrained-observe (allow-but-record a ClassPolicy
// deny) is a HOOK-PEP mode only and must never reach this surface — a cleanly-authored policy
// forbid (the exact class shadows on the hook) is a hard 403 here, with no shadow/observe
// softening and NO forward. Breaks if a future change wires an observe/shadow branch onto the
// inference decider.
func TestProxyAuthorizeAlwaysEnforcesNoConstrainedObserveShadow(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateContextWindow = true
	base.FailOpen = true // even the only softening knob must not shadow a definitive deny
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	// A cleanly-authored business-policy forbid — the exact ClassPolicy disposition would
	// SHADOW on the hook-PEP. On the inference surface it must be a hard deny, not allow-record.
	d.contextPolicy = &fakeContextPolicy{pol: knowledge.EffectivePolicy{Deny: true}}
	var countDoer *fakeCountTokensDoer
	d.inf, countDoer = proxyCountTokensInference(25)

	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("inference is always-enforce: an authored policy forbid must hard-deny 403, never shadow-allow; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if countDoer.calls != 0 {
		t.Fatalf("a shadowed observe-allow would have forwarded (CountTokens>0); calls=%d, want 0 (hard deny, no forward)", countDoer.calls)
	}
}

// TestProxyAuthorizeKillSwitchIgnoresFailOpen proves the kill-switch is DELIBERATELY
// excluded from fail_open: an unreadable emergency-stop state always denies (the brake
// must never be defeated by a read fault).
func TestProxyAuthorizeKillSwitchIgnoresFailOpen(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	kg.err = errBootInferenceProxy("killswitch unreadable")
	base := allGatesOnExceptDLPAndCtx()
	base.FailOpen = true
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base})
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable kill-switch must deny even with fail_open=true; got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

// TestProxyAuthorizeDLPBlocksSecretPrompt proves the request-DLP gate: a prompt carrying
// a secret, under a deny rule, is blocked BEFORE the forward.
func TestProxyAuthorizeDLPBlocksSecretPrompt(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.GateDLPRequest = true
	pol := fakeProxyPolicy{pol: inferenceproxy.PolicyWithDLPRules(base, map[string]string{"*": "deny"})}

	const secretPrompt = "deploy with AKIAIOSFODNN7EXAMPLE and key -----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----"
	// Sanity: the deterministic classifier must actually flag this text (else the test is vacuous).
	if len(classifyText([]string{secretPrompt})) == 0 {
		t.Fatal("test setup: classifier found no sensitive class in the seeded prompt")
	}
	d := newTestDecider(a, mg, bg, kg, pol)
	dec := d.Authorize(context.Background(), userReq(secretPrompt, false), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("a DLP-denied prompt must 403; got allow=%v status=%d", dec.Allow, dec.Status)
	}
	if bg.calls != 0 {
		t.Error("DLP (security) must run before budget")
	}
}

// TestProxyAuthorizeMandatoryRecordingFailsClosedWithoutLedger proves a recording-
// MANDATING tenant is denied when no ledger is reachable (no evidence ⇒ no forward).
func TestProxyAuthorizeMandatoryRecordingFailsClosedWithoutLedger(t *testing.T) {
	a, mg, bg, kg, _ := allowAll()
	base := allGatesOnExceptDLPAndCtx()
	base.RecordMandatory = true
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: base}) // store is nil
	dec := d.Authorize(context.Background(), userReq("hi", false), "bearer")
	if dec.Allow || dec.Status != http.StatusServiceUnavailable {
		t.Fatalf("a recording-mandating tenant must fail closed without a ledger; got allow=%v status=%d", dec.Allow, dec.Status)
	}
}

// --- Finalize (response) tests ------------------------------------------------------

func TestProxyFinalizeFlagModeDoesNotBlock(t *testing.T) {
	base := allGatesOnExceptDLPAndCtx()
	base.GateDLPResponse = true
	base.ResponseDLPMode = inferenceproxy.ResponseDLPFlag
	sess := &proxySession{
		tenant: proxyTestTenant, modelRef: "claude-opus-4-8", requestRef: "r1",
		pol: inferenceproxy.PolicyWithDLPRules(base, map[string]string{"*": "deny"}),
	}
	d := newTestDecider(allowAll())
	out := claudeapi.ProxyForwardResult{
		Response:  claudeapi.MessageResponse{Content: []claudeapi.ContentBlock{claudeapi.TextBlock("here is AKIAIOSFODNN7EXAMPLE")}},
		RespSHA:   []byte("sha"),
		RespBytes: 10,
	}
	v := d.Finalize(context.Background(), sess, out)
	if v.Block {
		t.Fatal("flag mode must NOT block the response (detective only)")
	}
}

func TestProxyFinalizeBufferModeBlocksDeniedResponse(t *testing.T) {
	base := allGatesOnExceptDLPAndCtx()
	base.GateDLPResponse = true
	base.ResponseDLPMode = inferenceproxy.ResponseDLPBuffer
	sess := &proxySession{
		tenant: proxyTestTenant, modelRef: "claude-opus-4-8", requestRef: "r1",
		pol: inferenceproxy.PolicyWithDLPRules(base, map[string]string{"*": "deny"}),
	}
	d := newTestDecider(allowAll())
	out := claudeapi.ProxyForwardResult{
		Response: claudeapi.MessageResponse{Content: []claudeapi.ContentBlock{claudeapi.TextBlock("here is AKIAIOSFODNN7EXAMPLE")}},
	}
	v := d.Finalize(context.Background(), sess, out)
	if !v.Block || v.Status != http.StatusForbidden {
		t.Fatalf("buffer mode must BLOCK a DLP-denied response; got block=%v status=%d", v.Block, v.Status)
	}
}

func TestProxyFinalizeNilSessionIsSafe(t *testing.T) {
	d := newTestDecider(allowAll())
	if v := d.Finalize(context.Background(), nil, claudeapi.ProxyForwardResult{}); v.Block {
		t.Fatal("a nil session must not block (nothing to decide)")
	}
}

func TestProxyReconcileCostStampsAuthenticatedActor(t *testing.T) {
	d := newTestDecider(allowAll())
	d.inf = claudeapi.NewInference(claudeapi.InferenceConfig{APIKey: "test", DefaultModel: "claude-opus-4-8", Gateway: sdkmodel.GatewayDirect})
	bus := &fakeObservationBus{}
	d.bus = bus
	sess := &proxySession{tenant: proxyTestTenant, actor: "user:u1", sessionRef: "sess-1"}
	d.reconcileCost(context.Background(), sess, claudeapi.ProxyForwardResult{Response: claudeapi.MessageResponse{
		ID: "msg-1", Model: "claude-opus-4-8", StopReason: "end_turn",
		Usage: claudeapi.MessageUsage{InputTokens: 100, OutputTokens: 20},
	}})
	costs := bus.costs()
	if len(costs) != 1 || costs[0].Actor != sess.actor {
		t.Fatalf("runtime costs = %+v, want one sample actor=%q", costs, sess.actor)
	}
}

func TestRequestCeilingViolations(t *testing.T) {
	cases := []struct {
		name     string
		req      claudeapi.MessageRequest
		ceilings inferenceproxy.RequestCeilings
		want     []string
	}{
		{
			name:     "max_tokens",
			req:      claudeapi.MessageRequest{MaxTokens: 101},
			ceilings: inferenceproxy.RequestCeilings{MaxTokens: 100},
			want:     []string{"max_tokens"},
		},
		{
			name: "task_budget",
			req: claudeapi.MessageRequest{
				OutputConfig: &claudeapi.OutputConfig{TaskBudget: &claudeapi.TaskBudget{Type: "tokens", Total: 40000}},
			},
			ceilings: inferenceproxy.RequestCeilings{TaskBudgetTokens: 30000},
			want:     []string{"task_budget"},
		},
		{
			name: "tool max_uses",
			req: claudeapi.MessageRequest{Tools: []any{
				map[string]any{"type": "web_search_20250305", "max_uses": float64(9)},
			}},
			ceilings: inferenceproxy.RequestCeilings{MaxToolUses: 3},
			want:     []string{"tool_max_uses:web_search_20250305"},
		},
		{
			name: "non-map and malformed tools tolerated",
			req: claudeapi.MessageRequest{Tools: []any{
				"not-a-map",
				map[string]any{"type": "web_search_20250305", "max_uses": "9"},
				map[string]any{"type": "web_fetch_20250305", "max_uses": 2},
			}},
			ceilings: inferenceproxy.RequestCeilings{MaxToolUses: 3},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requestCeilingViolationLabels(requestCeilingViolations(tc.req, tc.ceilings))
			if len(got) != len(tc.want) {
				t.Fatalf("violations = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("violations = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(b)
}

// containsMoney is a crude guard that a deny reason carries no currency figure.
func containsMoney(s string) bool {
	for _, r := range s {
		if r == '$' {
			return true
		}
	}
	return false
}

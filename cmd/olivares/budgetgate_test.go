// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/voice"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestBudgetGatesEnforceAgainstRealFinOps drives the three composition-root budget
// adapters (orch/voice/models) against a REAL finops.Module — proving the cross-module
// contract (finops.SpendDims in, finops.BudgetCheck mapped out) that the per-module
// fakes cannot: that an enforcing budget at its cap, accrued through the live cost
// ingestion path, actually denies through CheckBudget, with the budget id/action mapped
// onto each module's own decision and the dims forwarded so a scoped budget matches.
func TestBudgetGatesEnforceAgainstRealFinOps(t *testing.T) {
	ctx := context.Background()
	fin := finops.New()

	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, fin.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}

	fin.UseData(api.NewModuleData(st))
	bus := eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(fin, sdk.Config{}); err != nil {
		t.Fatalf("add finops: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(ctx) })

	now := time.Now().UTC()

	// A model-scoped BLOCK budget (key=claude-opus-4-8), limit 1 µUSD — any spend crosses
	// it. Stored as a core Policy{kind:budget} exactly as the FinOps API would.
	modelBudgetID := createBudgetPolicy(t, st, tenant, "opus-cap", map[string]any{
		"dimension": "model", "key": "claude-opus-4-8", "period": "monthly",
		"limit_micro_usd": int64(1), "action": "block", "thresholds": []float64{1.0},
	})

	// Accrue spend through the LIVE ingestion path (bus → finops.onEvent → onCost), the
	// same path production uses — not a test backdoor.
	publishCost(t, bus, tenant, sdkmodel.CostSample{
		ProviderRef: "anthropic", ModelRef: "claude-opus-4-8",
		InputTokens: 100, OutputTokens: 50, CostMicroUSD: 5000, OccurredAt: now,
	})
	// Wait until the budget is observed over its cap through CheckBudget itself.
	waitBudgetOver(t, fin, tenant, finops.SpendDims{ModelRef: "claude-opus-4-8"})

	// models adapter: a resolve for the capped model is DENIED, with the budget id/action
	// mapped through; a DIFFERENT model is unaffected (proves the dims are forwarded).
	mGate := modelsBudgetGate{fin: fin}
	dec, err := mGate.Check(ctx, tenant, models.BudgetDims{ProviderRef: "anthropic", ModelRef: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("models gate: %v", err)
	}
	if dec.Allowed || dec.Action != "block" || dec.BudgetRef != modelBudgetID.String() {
		t.Fatalf("capped model must deny block with the budget id, got %+v (want id %s)", dec, modelBudgetID)
	}
	// Minimal data (docs/SECURITY-HARDENING.md): the mapped reason carries no USD amount (the
	// SpendMicroUSD/LimitMicroUSD of finops.BudgetCheck must not flow through the seam).
	if dec.Reason == "" {
		t.Error("a denial must carry a (money-free) reason")
	}
	if strings.Contains(dec.Reason, "$") || strings.Contains(dec.Reason, "5000") || strings.Contains(dec.Reason, "micro") {
		t.Fatalf("the budget decision reason must be money-free, got %q", dec.Reason)
	}
	if other, _ := mGate.Check(ctx, tenant, models.BudgetDims{ModelRef: "claude-haiku-4-5"}); !other.Allowed {
		t.Fatalf("a model the budget does not scope must be allowed, got %+v", other)
	}

	// voice forwards ModelRef/ProviderRef: the SAME opus model budget caps a voice open on
	// opus, but a haiku open is unaffected — proving the voice adapter forwards its dims
	// (a global cap could not prove this).
	vGate := voiceBudgetGate{fin: fin}
	if vd, _ := vGate.Check(ctx, tenant, voice.BudgetDims{AgentRef: "a", SessionRef: "s", ModelRef: "claude-opus-4-8", ProviderRef: "anthropic"}); vd.Allowed || vd.Action != "block" {
		t.Fatalf("voice gate must forward ModelRef and deny the capped model, got %+v", vd)
	}
	if vd, _ := vGate.Check(ctx, tenant, voice.BudgetDims{AgentRef: "a", SessionRef: "s", ModelRef: "claude-haiku-4-5", ProviderRef: "anthropic"}); !vd.Allowed {
		t.Fatalf("voice gate must allow a model the budget does not scope, got %+v", vd)
	}

	// orch forwards ONLY AgentRef (it knows no model): the opus MODEL budget must NOT
	// spuriously cap a fire, so under just that budget the fire is allowed — proving the
	// orch adapter sends a correctly-scoped, model-free dim set.
	oGate := orchBudgetGate{fin: fin}
	if od, _ := oGate.Check(ctx, tenant, orchestration.BudgetDims{AgentRef: "batch-agent"}); !od.Allowed {
		t.Fatalf("a fire (no model dim) must not match a model-scoped budget, got %+v", od)
	}

	// Under-cap boundary: a MATCHING global budget UNDER its cap allows (enforcement
	// fires at the cap, not on mere existence).
	createBudgetPolicy(t, st, tenant, "global-headroom", map[string]any{
		"dimension": "global", "period": "total",
		"limit_micro_usd": int64(1_000_000_000_000_000), "action": "block", "thresholds": []float64{1.0},
	})
	if od, _ := oGate.Check(ctx, tenant, orchestration.BudgetDims{AgentRef: "batch-agent"}); !od.Allowed {
		t.Fatalf("a global budget far under its cap must allow, got %+v", od)
	}

	// Over-cap: a GLOBAL budget at its cap denies every dimension — orch and voice.
	createBudgetPolicy(t, st, tenant, "global-cap", map[string]any{
		"dimension": "global", "period": "total",
		"limit_micro_usd": int64(1), "action": "block", "thresholds": []float64{1.0},
	})
	if od, _ := oGate.Check(ctx, tenant, orchestration.BudgetDims{AgentRef: "batch-agent"}); od.Allowed || od.Action != "block" {
		t.Fatalf("orchestration gate must deny under a global block cap, got %+v", od)
	}
	if vd, _ := vGate.Check(ctx, tenant, voice.BudgetDims{AgentRef: "voice-agent", ModelRef: "gpt-realtime", ProviderRef: "openai"}); vd.Allowed || vd.Action != "block" {
		t.Fatalf("voice gate must deny under a global block cap, got %+v", vd)
	}
}

// stubChecker is a budgetChecker that always returns the given check and error, so the
// adapter fail-open branch (a FinOps error must map to ALLOW, never DENY) can be tested.
type stubChecker struct {
	chk finops.BudgetCheck
	err error
}

func (s stubChecker) CheckBudget(context.Context, model.TenantID, finops.SpendDims) (finops.BudgetCheck, error) {
	return s.chk, s.err
}

func (s stubChecker) CheckSpendLimit(context.Context, model.TenantID, string, []string) (finops.SpendLimitCheck, error) {
	return finops.SpendLimitCheck{Allowed: true}, nil
}

// TestBudgetGateAdaptersFailOpenOnError proves all three composition-root adapters map a
// FinOps error to ALLOW (fail-open), even when the (ignored) check says DENY — a FinOps
// outage must never take down actuation.
func TestBudgetGateAdaptersFailOpenOnError(t *testing.T) {
	ctx := context.Background()
	boom := stubChecker{chk: finops.BudgetCheck{Allowed: false, Action: "block", BudgetID: "x"}, err: errSyntheticOutage}

	if d, err := (orchBudgetGate{fin: boom}).Check(ctx, "t", orchestration.BudgetDims{}); err != nil || !d.Allowed {
		t.Fatalf("orch adapter must fail OPEN on a checker error, got %+v err=%v", d, err)
	}
	if d, err := (voiceBudgetGate{fin: boom}).Check(ctx, "t", voice.BudgetDims{}); err != nil || !d.Allowed {
		t.Fatalf("voice adapter must fail OPEN on a checker error, got %+v err=%v", d, err)
	}
	if d, err := (modelsBudgetGate{fin: boom}).Check(ctx, "t", models.BudgetDims{}); err != nil || !d.Allowed {
		t.Fatalf("models adapter must fail OPEN on a checker error, got %+v err=%v", d, err)
	}

	// And when the checker SUCCEEDS with a deny, the adapter maps it through unchanged.
	deny := stubChecker{chk: finops.BudgetCheck{Allowed: false, Action: "throttle", BudgetID: "b9", Reason: "r"}}
	if d, _ := (orchBudgetGate{fin: deny}).Check(ctx, "t", orchestration.BudgetDims{}); d.Allowed || d.Action != "throttle" || d.BudgetRef != "b9" {
		t.Fatalf("orch adapter must map a successful deny through, got %+v", d)
	}
}

var errSyntheticOutage = stubErr("synthetic finops outage")

type stubErr string

func (e stubErr) Error() string { return string(e) }

// TestModelRouterBudgetGateWiredThroughCompositionRoot proves wire.go actually wires the
// FinOps budget gate into the running models module — not just that the adapter works in
// isolation. It drives the REAL composition root (buildModules + wire.go via newHarness):
// once a global block budget created through the FinOps API is over its cap, the model-
// router resolve endpoint denies (402). A regression that dropped the WithBudgetGate call
// in wire.go would leave this resolve at 200/resolved=true and fail the test.
func TestModelRouterBudgetGateWiredThroughCompositionRoot(t *testing.T) {
	h := newHarness(t)
	tenant := h.tenantA

	// Baseline: the seed accrued cost → a governed estate, so a cost policy resolves.
	var pol struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/models/routing-policies", h.adminToken, tenant, map[string]any{
		"name": "cheap", "enabled": true, "strategy": "cost",
	}, &pol); code != http.StatusCreated || pol.ID == "" {
		t.Fatalf("create routing policy = %d id=%q", code, pol.ID)
	}
	h.eventually("routing resolves a governed model", 10*time.Second, func() error {
		code, raw := h.req("POST", "/v1/m/models/routing-policies/"+pol.ID+"/resolve", h.adminToken, tenant, nil)
		if code != http.StatusOK {
			return fmt.Errorf("resolve = %d: %s", code, raw)
		}
		var d struct {
			Resolved bool `json:"resolved"`
		}
		_ = json.Unmarshal(raw, &d)
		if !d.Resolved {
			return fmt.Errorf("not resolved yet: %s", raw)
		}
		return nil
	})

	// Wire-up proof: a GLOBAL block budget at its cap, created through the real FinOps API,
	// must make the SAME resolve endpoint deny — only possible if wire.go wired
	// modelsBudgetGate into the models module.
	var bud struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/finops/budgets", h.adminToken, tenant, map[string]any{
		"name": "all-spend-cap", "enabled": true, "dimension": "global", "period": "total",
		"action": "block", "limit_micro_usd": 1, "thresholds": []float64{1.0},
	}, &bud); code != http.StatusCreated || bud.ID == "" {
		t.Fatalf("create budget = %d id=%q", code, bud.ID)
	}
	h.eventually("budget observed over its cap", 10*time.Second, func() error {
		var st struct {
			Over bool `json:"over"`
		}
		if code := h.reqInto("GET", "/v1/m/finops/budgets/"+bud.ID+"/status", h.adminToken, tenant, nil, &st); code != http.StatusOK {
			return fmt.Errorf("status = %d", code)
		}
		if !st.Over {
			return fmt.Errorf("not over yet")
		}
		return nil
	})
	h.eventually("resolve denied by the wired budget gate", 10*time.Second, func() error {
		code, raw := h.req("POST", "/v1/m/models/routing-policies/"+pol.ID+"/resolve", h.adminToken, tenant, nil)
		if code != http.StatusPaymentRequired {
			return fmt.Errorf("resolve = %d (want 402 from the wired gate): %s", code, raw)
		}
		body := string(raw)
		if !strings.Contains(body, `"budget_action":"block"`) {
			return fmt.Errorf("missing budget_action: %s", body)
		}
		if strings.Contains(body, "all-spend-cap") {
			return fmt.Errorf("read-tier resolve must not leak the budget name: %s", body)
		}
		return nil
	})
}

// createBudgetPolicy writes a budget as a core Policy{kind:budget} (the shape the FinOps
// budget API persists) and returns its id.
func createBudgetPolicy(t *testing.T, st store.Store, tenant model.TenantID, name string, spec map[string]any) model.ID {
	t.Helper()
	var id model.ID
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: name, Kind: "budget", Enabled: true, Spec: spec,
		})
		id = p.ID
		return err
	}); err != nil {
		t.Fatalf("create budget policy %q: %v", name, err)
	}
	return id
}

// publishCost emits one cost sample on the bus (the production ingestion channel).
func publishCost(t *testing.T, bus eventbus.Bus, tenant model.TenantID, c sdkmodel.CostSample) {
	t.Helper()
	if err := bus.Publish(context.Background(), event.FromObservation(tenant.String(), "src:budget-test", c)); err != nil {
		t.Fatalf("publish cost: %v", err)
	}
}

// waitBudgetOver polls CheckBudget until an enforcing budget denies the dims (the bus is
// async), failing the test if ingestion never crosses the cap.
func waitBudgetOver(t *testing.T, fin *finops.Module, tenant model.TenantID, dims finops.SpendDims) {
	t.Helper()
	for i := 0; i < 200; i++ {
		chk, err := fin.CheckBudget(context.Background(), tenant, dims)
		if err == nil && !chk.Allowed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("budget never observed over its cap after cost ingestion")
}

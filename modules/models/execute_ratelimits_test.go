// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// stubExecutor is a wired execution backend used to prove the execute route resolves
// then actuates through the seam.
type stubExecutor struct {
	res   models.ExecuteResult
	err   error
	calls int
}

func (s *stubExecutor) Execute(_ context.Context, req models.ExecuteRequest) (models.ExecuteResult, error) {
	s.calls++
	// Echo the served target from the resolved chain so the test sees resolve→execute.
	if len(req.Chain) > 0 {
		s.res.Served = req.Chain[0]
	}
	return s.res, s.err
}

// stubRateLimits is a wired read-only inventory provider.
type stubRateLimits struct {
	refs []mp.RateLimitRef
	err  error
}

func (s stubRateLimits) RateLimits(context.Context) ([]mp.RateLimitRef, error) {
	return s.refs, s.err
}

// seedModel inserts one governed model so a routing policy resolves to it.
func seedModel(t *testing.T, h *harness, tenant model.TenantID, provider, modelRef string) {
	t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		p, err := sc.Providers().Create(ctx, model.Provider{Name: provider, Kind: provider, Status: model.StatusActive})
		if err != nil {
			return err
		}
		_, err = sc.Models().Create(ctx, model.Model{Name: modelRef, ProviderID: p.ID, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("seed model: %v", err)
	}
}

// createRoutingPolicy creates a cost routing policy and returns its id.
func createRoutingPolicy(t *testing.T, h *harness, admin string, tenant model.TenantID) string {
	t.Helper()
	r := h.do("POST", "/v1/m/models/routing-policies", admin, map[string]any{"name": "p", "enabled": true, "strategy": "cost"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create routing policy = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// sessionKeyedScopeGate grants source scope for a model ONLY when the query's SessionRef
// equals grantSession — modeling a stored agent/session whose workspace + agent-group
// binding covers the model. Every other SessionRef (notably the empty ref a CONFINED
// principal carries, AgentIdentity=="") is denied. This makes the acting-session choice
// OBSERVABLE at the HTTP /execute seam: the gate authorizes iff the ref that reaches it is
// the victim's, so a test can prove the sink derives the actor from the authenticated
// identity, never the caller-supplied body session_ref.
type sessionKeyedScopeGate struct{ grantSession string }

func (g sessionKeyedScopeGate) Allowed(_ context.Context, _ model.TenantID, q models.ScopeQuery) (models.ScopeVerdict, error) {
	if strings.TrimSpace(q.SessionRef) == g.grantSession {
		return models.ScopeVerdict{Allowed: true}, nil
	}
	return models.ScopeVerdict{Allowed: false, Reason: "actor out of scope for this model"}, nil
}

// TestExecuteConfinedPrincipalCannotBorrowBodySessionScope is the HTTP/handler-level F-01
// regression guard (H4) for the load-bearing line execute.go:236 — the source-scope gate
// MUST be driven by the AUTHENTICATED actor (mc.Principal.AgentIdentity), NEVER the
// caller-supplied body session_ref. A confined principal (a raw admin session token has
// AgentIdentity=="") posts a body session_ref naming ANOTHER agent's session whose stored
// binding DOES cover the model. Because the sink keys the scope check off the empty
// authenticated identity, the model is out of scope → the whole chain is denied (403) and
// the executor never runs. This test FAILS if execute.go:236 is reverted to in.SessionRef:
// the borrowed ref would then satisfy the gate and the request would 200 through to spend.
func TestExecuteConfinedPrincipalCannotBorrowBodySessionScope(t *testing.T) {
	const victimSession = "victim-agent-session"
	ex := &stubExecutor{res: models.ExecuteResult{Text: "borrowed output"}}
	m := models.New(
		models.WithExecutor(ex),
		models.WithStopGate(stubStopGate{}),
		models.WithScopeGate(sessionKeyedScopeGate{grantSession: victimSession}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin,
		map[string]any{"input": "hello", "session_ref": victimSession}, tenantHdr(tenant))
	if r.code != http.StatusForbidden {
		t.Fatalf("confined principal borrowing a body session_ref = %d %s, want 403 (source-scope keyed off the authenticated actor, not the body ref)", r.code, r.raw)
	}
	if ex.calls != 0 {
		t.Fatalf("executor called %d times; a source-scope deny must precede any provider call", ex.calls)
	}
}

// TestExecuteDenyClosedWithoutExecutor proves the headline deny-closed posture: with no
// executor wired, POST /execute resolves but returns 503 and actuates NOTHING.
func TestExecuteDenyClosedWithoutExecutor(t *testing.T) {
	m := models.New() // default → unwiredExecutor (deny-closed)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("execute without an executor = %d, want 503 (deny-closed) — body %s", r.code, r.raw)
	}
}

// TestExecuteWiredResolvesAndActs proves a wired executor receives the resolved chain
// and its result flows back (resolve → act → respond).
func TestExecuteWiredResolvesAndActs(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "the answer", InputTokens: 12, OutputTokens: 7}}
	m := models.New(models.WithExecutor(ex))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, map[string]any{"input": "hello", "max_tokens": 64}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("execute (wired) = %d %s", r.code, r.raw)
	}
	if ex.calls != 1 {
		t.Errorf("executor called %d times, want 1", ex.calls)
	}
	if r.body["output"] != "the answer" {
		t.Errorf("output = %v, want 'the answer'", r.body["output"])
	}
	served, _ := r.body["served"].(map[string]any)
	if served == nil || served["model_ref"] != "claude-opus-4-8" {
		t.Errorf("served target = %v, want the resolved claude-opus-4-8", r.body["served"])
	}
	// Money never appears on this surface (docs/SECURITY-HARDENING.md): token counts only.
	if _, hasUSD := r.body["cost_micro_usd"]; hasUSD {
		t.Error("execute response must not expose a USD cost")
	}
}

// TestExecuteForbiddenForEditor proves execute is admin-tier (a spend), not reachable
// by a routing editor.
func TestExecuteForbiddenForEditor(t *testing.T) {
	m := models.New(models.WithExecutor(&stubExecutor{}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@acme.com", auth.RoleEditor)
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)
	if r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", editor, map[string]any{"input": "x"}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor execute = %d, want 403 (admin-tier spend)", r.code)
	}
}

// TestRateLimitsDegradesWithoutProvider proves GET /rate-limits never 500s with no
// connector: it returns an empty inventory WITH a reason and the Managed-Agents caveat.
func TestRateLimitsDegradesWithoutProvider(t *testing.T) {
	m := models.New() // no rate-limit provider
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("GET", "/v1/m/models/rate-limits", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("rate-limits (unwired) = %d, want 200 (degrade, never 500) — %s", r.code, r.raw)
	}
	if r.body["available"] != false || r.body["reason"] == "" {
		t.Errorf("unwired inventory must be available=false with a reason: %v", r.body)
	}
	if items, _ := r.body["rate_limits"].([]any); len(items) != 0 {
		t.Errorf("unwired inventory must be empty, got %v", items)
	}
	if caveat, _ := r.body["caveat"].(string); caveat == "" {
		t.Error("the Managed-Agents caveat must always be present")
	}
}

// TestRateLimitsMaterializesInventory proves a wired provider materializes the v2
// group_type/models/limits inventory read-only.
func TestRateLimitsMaterializesInventory(t *testing.T) {
	provider := stubRateLimits{refs: []mp.RateLimitRef{
		{
			GroupType: "model_group",
			Models:    []string{"claude-opus-4-8", "claude-opus-4-8-20260701"},
			Limits: []mp.RateLimitValue{
				{Type: "requests_per_minute", Value: 4000},
				{Type: "input_tokens_per_minute", Value: 10_000_000},
			},
		},
		{
			WorkspaceRef: "wrkspc_1",
			GroupType:    "web_search",
			Limits: []mp.RateLimitValue{
				{Type: "requests_per_minute", Value: 50, OrgLimit: 5000},
			},
		},
	}}
	m := models.New(models.WithRateLimitProvider(provider))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	r := h.do("GET", "/v1/m/models/rate-limits", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["available"] != true {
		t.Fatalf("rate-limits (wired) = %d available=%v", r.code, r.body["available"])
	}
	items, _ := r.body["rate_limits"].([]any)
	if len(items) != 2 {
		t.Fatalf("inventory rows = %d, want 2", len(items))
	}
	first, _ := items[0].(map[string]any)
	models, _ := first["models"].([]any)
	limits, _ := first["limits"].([]any)
	if first["group_type"] != "model_group" || len(models) != 2 || len(limits) != 2 {
		t.Errorf("first row = %v, want model_group with models and two limits", first)
	}
	second, _ := items[1].(map[string]any)
	secondLimits, _ := second["limits"].([]any)
	lim, _ := secondLimits[0].(map[string]any)
	if lim["org_limit"] != float64(5000) {
		t.Errorf("workspace limit = %v, want org_limit echo", second)
	}
}

// TestRateLimitsTransientErrorDegrades proves a provider fetch error degrades to
// empty-with-reason (never a 500), not a leaked error.
func TestRateLimitsTransientErrorDegrades(t *testing.T) {
	m := models.New(models.WithRateLimitProvider(stubRateLimits{err: context.DeadlineExceeded}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)
	r := h.do("GET", "/v1/m/models/rate-limits", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["available"] != false {
		t.Fatalf("transient error must degrade to 200/available=false, got %d %v", r.code, r.body)
	}
}

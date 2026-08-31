// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/models"
)

var (
	benchmarkHookDecisionResult claude.HookDecisionResult
	benchmarkProxyDecision      claudeapi.ProxyDecision
)

type decisionBenchmarkHarness struct {
	st     store.Store
	tenant model.TenantID
	bearer string
	gov    *governance.Module
	proxy  *inferenceproxy.Module
}

// newDecisionBenchmarkHarness is the minimal testing.B adapter for the real
// engine harness. The existing newHarness/newHookPEPFixture helpers require a
// concrete *testing.T, so the benchmark reproduces only their store, signer,
// tenant, firm-token, governance, and inference-policy wiring. The measured
// paths still use the real authenticator, kill-switch repository, policy
// repository, signed audit ledger, and transaction engine.
func newDecisionBenchmarkHarness(b *testing.B) *decisionBenchmarkHarness {
	b.Helper()
	ctx := context.Background()
	gov := governance.New()
	proxy := inferenceproxy.New()
	register := func(reg store.ExtensionRegistry) error {
		if err := gov.RegisterSchema(reg); err != nil {
			return err
		}
		return proxy.RegisterSchema(reg)
	}

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		b.Fatalf("generate audit key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		b.Fatalf("build audit signer: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, register)
	if err != nil {
		b.Fatalf("open decision benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{
			Name: "decision-bench", Slug: "decision-bench", Status: model.StatusActive,
		})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		b.Fatalf("provision decision benchmark tenant: %v", err)
	}

	data := api.NewModuleData(st)
	gov.UseData(data)
	proxy.UseData(data)
	bearer := mintDecisionBenchmarkToken(b, st, tenant)
	seedMandatoryProxyPolicy(b, st, tenant)

	return &decisionBenchmarkHarness{st: st, tenant: tenant, bearer: bearer, gov: gov, proxy: proxy}
}

func mintDecisionBenchmarkToken(b *testing.B, st store.Store, tenant model.TenantID) string {
	b.Helper()
	credential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		b.Fatalf("mint decision benchmark credential: %v", err)
	}
	ctx := context.Background()
	err = st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Tokens().Create(ctx, model.APIToken{
			Name:          "decision-bench-agent",
			UserID:        model.NewID(),
			Selector:      credential.Selector,
			SecretHash:    credential.SecretHash,
			BoundTenantID: tenant,
			Role:          auth.RoleEditor,
			AgentRef:      "agent@e2e.test",
		})
		return err
	})
	if err != nil {
		b.Fatalf("store decision benchmark credential: %v", err)
	}
	return credential.Token
}

func seedMandatoryProxyPolicy(b *testing.B, st store.Store, tenant model.TenantID) {
	b.Helper()
	ctx := context.Background()
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("inferenceproxy.config"))
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			"fail_open":                  false,
			"response_dlp_mode":          inferenceproxy.ResponseDLPFlag,
			"record_mandatory":           true,
			"gate_model_access":          true,
			"gate_budget":                true,
			"gate_residency":             false,
			"gate_context_window":        false,
			"gate_dlp_request":           false,
			"gate_dlp_response":          false,
			"updated_by":                 "decision-bench",
			"ceilings_enforce":           false,
			"ceiling_max_tokens":         int64(0),
			"ceiling_max_tool_uses":      int64(0),
			"ceiling_task_budget_tokens": int64(0),
		})
		return err
	})
	if err != nil {
		b.Fatalf("seed mandatory proxy policy: %v", err)
	}
}

// BenchmarkHookDecideEndToEnd includes real bearer authentication and a live
// per-call kill-switch store read before the in-memory policy/PDP decision.
func BenchmarkHookDecideEndToEnd(b *testing.B) {
	h := newDecisionBenchmarkHarness(b)
	pol := hookPolicyDoc{Default: claude.DecisionAllow}
	decider := &claudeHookDecider{
		tenants: map[model.TenantID]resolvedTenant{
			h.tenant: {tenant: h.tenant, requireFirm: true, policy: pol},
		},
		authr: auth.NewAuthenticator(h.st, nil),
		eval:  fixedEval{allow: true},
		stops: h.gov,
		store: h.st,
		clock: time.Now,
		log:   discardLog(),
	}
	in := claude.HookDecisionInput{
		Event:        "PreToolUse",
		Tool:         "Read",
		ResourceKind: hookResourceKindFile,
		ResourceRef:  "/srv/acme/Public/readme.txt",
		Mode:         "read",
		Identity: claude.HookIdentity{
			Tenant: h.tenant.String(), Agent: "agent@e2e.test",
		},
	}

	if got, err := decider.Decide(context.Background(), in, h.bearer); err != nil || got.Permission != claude.DecisionAllow {
		b.Fatalf("warm-up hook decision = (%+v, %v), want allow", got, err)
	}

	ctx := context.Background()
	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		var err error
		benchmarkHookDecisionResult, err = decider.Decide(ctx, in, h.bearer)
		lat[i] = time.Since(t0)
		if err != nil {
			b.Fatalf("hook decision %d: %v", i, err)
		}
	}
	b.StopTimer()
	reportDecisionLatency(b, lat, time.Since(start))
}

// BenchmarkProxyAuthorizeEndToEnd exercises the real policy read on every call
// and, because recording is mandatory, a signed audit-ledger transaction before
// each allowed forward. Model-access and budget use the existing deterministic
// fixtures so no external provider or mutable budget state enters the result.
func BenchmarkProxyAuthorizeEndToEnd(b *testing.B) {
	h := newDecisionBenchmarkHarness(b)
	decider := &inferenceProxyDecider{
		surface:    "direct",
		authr:      auth.NewAuthenticator(h.st, nil),
		models:     &fakeProxyModels{v: models.ModelAccessVerdict{Allowed: true}},
		budget:     &fakeProxyBudget{bc: finops.BudgetCheck{Allowed: true}},
		killSwitch: h.gov,
		policy:     h.proxy,
		store:      h.st,
		clock:      time.Now,
		log:        discardLog(),
	}
	req := userReq("scale envelope decision", false)

	if got := decider.Authorize(context.Background(), req, h.bearer); !got.Allow {
		b.Fatalf("warm-up proxy decision denied: status=%d reason=%q", got.Status, got.Reason)
	}

	ctx := context.Background()
	lat := make([]time.Duration, b.N)
	start := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		benchmarkProxyDecision = decider.Authorize(ctx, req, h.bearer)
		lat[i] = time.Since(t0)
		if !benchmarkProxyDecision.Allow {
			b.Fatalf("proxy decision %d denied: status=%d reason=%q", i, benchmarkProxyDecision.Status, benchmarkProxyDecision.Reason)
		}
	}
	b.StopTimer()
	reportDecisionLatency(b, lat, time.Since(start))
}

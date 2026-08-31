// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.models"

// Namespace is the module's store and API namespace. Its registered entities are
// "models.<entity>" and its routes mount under /v1/m/models/.
const Namespace = "models"

// Module is the models/providers governance module (module X). It enriches the
// discovered model/provider entities with declared capabilities and pricing,
// governs routing/selection/fallback/version policy, and governs API-key and
// workspace references — all on top of the connectors, which it never
// re-implements.
type Module struct {
	log        *slog.Logger
	data       api.ModuleData
	budgetGate BudgetGate
	stopGate   StopGate
	scopeGate  ScopeGate
	// actorScope resolves the acting session's scope (workspace + agent-groups) for
	// the model-access decision (modelaccessgate.go). The composition root backs it
	// with the sourcescope resolver; the unwired default resolves to the empty scope.
	actorScope ActorScopeResolver
	// executor is the governed routing-execution seam (Fase K): the module RESOLVES
	// (pure selection) always, but ACTS only through this port, deny-closed by default
	// (unwiredExecutor → /execute 503). The real adapter (holding the inference
	// credential + the bus publisher) is wired in the composition root.
	executor Executor
	// rateLimits surfaces the read-only Anthropic Rate Limits inventory (ANT2-05); nil
	// (the default) degrades GET /rate-limits to an empty inventory with a reason.
	rateLimits RateLimitProvider
	// platforms surfaces the declared deployment-surface / lifecycle reference
	// (ANT2-01/03); nil (the default) degrades GET /platforms to
	// available=false with a reason.
	platforms PlatformsProvider

	mu     sync.Mutex
	cancel func() // bus unsubscribe
}

// Compile-time proof the module satisfies the SDK lifecycle, the engine-side
// schema seam, the API route/permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// Option configures a Module at construction.
type Option func(*Module)

// WithBudgetGate wires the FinOps (module XI) pre-flight budget gate (FIN-08).
// Without it, no budget ever denies a routing resolve (the opt-in default — see
// allowBudgetGate).
func WithBudgetGate(g BudgetGate) Option { return func(m *Module) { m.budgetGate = g } }

// WithStopGate wires the estate kill-switch pre-flight on the execute path.
// Without it, no stop ever freezes routed execution (the composition root always
// wires the governance-backed gate; see allowStopGate).
func WithStopGate(g StopGate) Option { return func(m *Module) { m.stopGate = g } }

// WithExecutor wires the governed routing-execution backend (Fase K). Without it the
// module keeps the deny-closed unwiredExecutor and POST /routing-policies/{id}/execute
// returns 503 — the control plane can resolve a routing decision but never spends
// against a provider until an operator provisions an executor in the composition root.
func WithExecutor(e Executor) Option { return func(m *Module) { m.executor = e } }

// WithRateLimitProvider wires the read-only Anthropic Rate Limits inventory source
// (ANT2-05). Without it GET /rate-limits degrades to an empty inventory with a reason
// (honest, never a 500). The provider is read-only — the module never mutates a limit.
func WithRateLimitProvider(p RateLimitProvider) Option {
	return func(m *Module) { m.rateLimits = p }
}

// WithPlatformsProvider wires the declared deployment-surface / lifecycle reference
// source (ANT2-01/03). Without it GET /platforms degrades to available=false
// with a reason (honest, never a 500). The provider is read-only and credential-less
// by contract — the data is declared reference, never a live mutation surface.
func WithPlatformsProvider(p PlatformsProvider) Option {
	return func(m *Module) { m.platforms = p }
}

// New returns a models module with the opt-in (allow) budget gate and the deny-closed
// routing executor; the composition root replaces them via the With* options.
func New(opts ...Option) *Module {
	m := &Module{budgetGate: allowBudgetGate{}, stopGate: allowStopGate{}, scopeGate: allowScopeGate{}, actorScope: unresolvedActorScope{}, executor: unwiredExecutor{}}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Models & providers",
		Description: "Governs the whole model/provider stack: capability/feature catalog, list pricing, routing/selection/fallback/version policy and API-key/workspace references, on top of the connectors.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the engine
// boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init wires the module to the bus. It subscribes to cost samples: a sampled cost
// names a provider/model actually in use, which the module enriches with its
// declared capabilities and pricing (the catalog of the live estate). It must not
// block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	cancel, err := host.Subscribe([]event.Type{event.TypeCostSampled}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work for this module (enrichment is event-driven and
// governance is request-driven); it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("models: started without a data handle; enrichment will not persist")
	}
	// BOOT LOG LEVEL (rule stated 2026-08-05). WARN when the capability's route now
	// REFUSES — 501/503/deny-closed; INFO when it still ANSWERS honestly, degraded.
	// The rule was already in the tree, three lines apart in modules/models/models.go:
	// /execute is deny-closed -> Warn, /rate-limits answers with a reason -> Info. It
	// simply was not applied consistently: on a virgin boot the same predicate came out
	// 3 times as INFO and 6 as WARN, and 27 WARN lines on a correct install is what made
	// a customer read a clean start as a broken product.
	if _, unwired := m.executor.(unwiredExecutor); unwired && m.log != nil {
		// Honest posture: routing resolves, but /execute is deny-closed
		// until an operator provisions an execution backend — say so, don't hide it.
		m.log.Warn("models: no routing executor wired; POST /routing-policies/{id}/execute is deny-closed (503). Resolve still works; provision an executor to enable governed execution")
	}
	if m.rateLimits == nil && m.log != nil {
		m.log.Info("models: no rate-limit inventory provider wired; GET /rate-limits returns an empty inventory with a reason (set the Claude Admin credential to enable it)")
	}
	if m.platforms == nil && m.log != nil {
		m.log.Info("models: no platforms reference provider wired; GET /platforms returns available=false with a reason (the composition root wires the credential-less Claude reference adapter)")
	}
	return nil
}

// Stop unsubscribes. It is idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// onEvent dispatches a delivered cost sample to the enrichment path. A handler
// error is logged by the engine and never stops delivery to others; the module
// relies on the connector's at-least-once redelivery plus idempotent
// find-or-create for recovery.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	if e.Type != event.TypeCostSampled {
		return nil
	}
	cost, ok := event.CostOf(e)
	if !ok || (cost.ProviderRef == "" && cost.ModelRef == "") {
		return nil
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil
	}
	return m.enrichFromCost(ctx, tenant, cost.ProviderRef, cost.ModelRef)
}

// tenantOf resolves an event's string tenant reference to a TenantID, or false
// when it is not a usable business tenant (placeholder/system tenants are skipped).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

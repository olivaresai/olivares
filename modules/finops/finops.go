// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

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
const Name = "olivares.finops"

// Namespace is the module's store and API namespace. Its registered entities are
// "finops.<entity>" and its routes mount under /v1/m/finops/.
const Namespace = "finops"

// Module is the FinOps module (module XI). It ingests the model/provider cost
// stream into the CostRecord ledger, governs budgets and emits spend alerts, and
// serves spend analytics, forecasting and optimization recommendations.
type Module struct {
	log   *slog.Logger
	host  sdk.Host
	data  api.ModuleData
	clock model.Clock

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

// New returns a FinOps module with the system clock.
func New() *Module { return &Module{clock: model.SystemClock{}} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Cost & FinOps",
		Description: "Tracks AI token/cost into the CostRecord ledger, governs budgets and emits spend alerts, and serves spend analytics, forecasting and optimization recommendations.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the engine
// boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init wires the module to the bus and keeps the host for publishing budget-alert
// findings. It subscribes to cost samples. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeCostSampled}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work (ingestion is event-driven, evaluation is
// request/ingest-driven); it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("finops: started without a data handle; cost accounting will not persist")
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

// onEvent dispatches a delivered cost sample to ingestion. A handler error is
// logged by the engine and never stops delivery to others; recovery relies on the
// connector's at-least-once redelivery plus the content-hash dedup, which makes
// re-ingestion a no-op.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil || e.Type != event.TypeCostSampled {
		return nil
	}
	cost, ok := event.CostOf(e)
	if !ok {
		return nil
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil
	}
	return m.onCost(ctx, tenant, cost, nil)
}

// tenantOf resolves an event's string tenant reference to a TenantID, or false
// when it is not a usable business tenant.
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

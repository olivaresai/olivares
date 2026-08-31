// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

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
const Name = "olivares.claudeadoption"

// Namespace is the module's store and API namespace. Its entity is "adoption.metric"
// and its routes mount under /v1/m/adoption/.
const Namespace = "adoption"

// Module is the Claude Code adoption / productivity read-model (gap #12). It ingests the
// MetricSample bus signal both Claude connectors emit and serves adoption aggregations.
type Module struct {
	log  *slog.Logger
	host sdk.Host
	data api.ModuleData

	mu     sync.Mutex
	cancel func() // bus unsubscribe
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/permission
// seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns an adoption module.
func New() *Module { return &Module{} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Claude Code Adoption",
		Description: "Persists Claude Code productivity/adoption metrics (sessions, lines of code, commits, PRs, tool accept-reject, per-model tokens) by team/developer/day and serves the adoption dashboard. Claude-API-only boundary; never carries cost.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the engine boot
// (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init subscribes to the MetricSample bus signal. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeMetricSampled}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work (ingestion is event-driven, aggregation is request-driven);
// it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("claudeadoption: started without a data handle; adoption metrics will not persist")
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

// onEvent dispatches a delivered MetricSample to ingestion. A handler error is logged by
// the engine and never stops delivery to others; the connector's at-least-once redelivery
// plus the natural-key upsert (and the additive high-water) make re-ingestion idempotent.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil || e.Type != event.TypeMetricSampled {
		return nil
	}
	ms, ok := event.MetricOf(e)
	if !ok {
		return nil
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil
	}
	return m.onMetric(ctx, tenant, e.Source, ms)
}

// tenantOf resolves an event's string tenant reference to a TenantID, or false when it
// is not a usable business tenant.
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// debugf logs at debug level if a logger is set. Adoption ingestion treats discrepancy
// finding publication as best-effort, so secondary failures are surfaced here without
// failing the primary MetricSample fold.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

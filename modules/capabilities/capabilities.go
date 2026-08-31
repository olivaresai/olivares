// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"context"
	"log/slog"
	"sync"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.capabilities"

// Namespace is the module's store and API namespace. Its registered entities are
// "capabilities.<entity>" and its routes mount under /v1/m/capabilities/.
const Namespace = "capabilities"

// Module is the capabilities/MCP-management module (module V). It maintains the
// capability-connection graph and the basic health overlay from the observation
// stream, and exposes the live MCP-server/skill/tool catalog plus the audited
// configuration and versioning over the API. It builds on top of the inventory
// and the connectors; it never re-implements the MCP client nor
// re-materializes the core entities inventory owns.
type Module struct {
	log   *slog.Logger
	data  api.ModuleData
	clock model.Clock
	// toolPins is the operator surface of the enterprise pin verifier
	// (nil in community — the /toolpins routes answer 501 honestly).
	toolPins mcpc.ToolPinAdmin

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

// New returns a capabilities module.
// Option configures the module at construction.
type Option func(*Module)

// New builds the module; options inject optional enterprise seams.
func New(opts ...Option) *Module {
	m := &Module{clock: model.SystemClock{}}
	for _, opt := range opts {
		opt(m)
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
		Title:       "MCPs, skills & capabilities",
		Description: "Visually governs MCP servers, skills and Claude Code plugins/subagents: live catalog with transport/scope/tools/UNTRUSTED annotations, the agent→capability wiring graph, configuration with secret references (never plaintext), version history and basic connection health.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the engine
// boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init wires the module to the bus. It subscribes to access edges (to build the
// capability-connection graph and refresh connection health on a fresh connection
// signal) and to findings (to record a capability's health from the connectors'
// health reports/§3). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved, event.TypeFindingReported}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start has no background work (wiring/health are event-driven and the catalog is
// request-driven); it only checks the data handle was wired.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("capabilities: started without a data handle; wiring and health will not persist")
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

// onEvent dispatches a delivered observation to the right overlay maintainer. A
// handler error is logged by the engine and never stops delivery to others; the
// module relies on the connectors' at-least-once redelivery plus idempotent
// upsert-by-natural-key for recovery.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	switch e.Type {
	case event.TypeEdgeObserved:
		if edge, ok := event.EdgeOf(e); ok {
			return m.onEdge(ctx, e.Tenant, edge)
		}
	case event.TypeFindingReported:
		if f, ok := event.FindingOf(e); ok {
			return m.onFinding(ctx, e.Tenant, f)
		}
	}
	return nil
}

// tenantOf resolves an event's string tenant reference to a usable business
// tenant, or false for a placeholder/system reference (the module never writes to
// the system partition).
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

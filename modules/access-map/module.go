// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"
	"log/slog"
	"sync"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.access-map"

// Namespace is the module's store and API namespace; its routes mount under
// /v1/m/accessmap/. It must be a valid module namespace (lowercase, no hyphen),
// so it is "accessmap" even though the package directory is /modules/access-map.
const Namespace = "accessmap"

// The module's permissions. Reading the access graph is a privileged,
// recon-relevant action (docs/SECURITY-HARDENING.md calls the graph an attacker's recon
// road-map), so both the graph view and the drift view have their own
// permissions and every read self-audits (docs/SECURITY-HARDENING.md).
const (
	permGraphRead auth.Permission = "accessmap:graph:read"
	permDriftRead auth.Permission = "accessmap:drift:read"
)

// Module is module III — the R/RW access map. It is the SOLE writer of the
// AccessEdge graph (decision A, 2026-06-03): it consumes the connector
// observation stream, reconciles identity across signals onto a canonical origin
// (bridge.go), fuses multi-signal confidence (fusion.go), and exposes the graph
// and the permitted-vs-observed drift as privileged, audited reads.
type Module struct {
	data  api.ModuleData
	log   *slog.Logger
	clock model.Clock

	mu     sync.Mutex
	cancel func() // bus unsubscribe
}

// Compile-time proof the module satisfies the SDK lifecycle, the API
// route/permission seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns an access-map module with a system clock and no data handle; the
// engine wires the data handle via UseData before Start.
func New() *Module { return &Module{clock: model.SystemClock{}} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Access map (R/RW)",
		Description: "Builds the agent→resource R/RW graph with explicit confidence and the permitted-vs-observed least-privilege drift (module III, the moat).",
	}
}

// UseData receives the least-privilege data handle (the api.DataConsumer seam).
// api.ModuleData is tenant-PARAMETERIZED, not tenant-bound: the tenant is
// supplied per call from the event/request (Ingest, and the audited reads), so
// one module safely serves every tenant exactly as inventory and sessions do —
// there is no cross-tenant state to leak.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init wires the module to the bus. It subscribes to access-edge observations —
// the cooperative (otel), audit (pgAudit/MySQL/CloudTrail), kernel (eBPF) and
// policy-grant (SignalPolicy) signals all arrive as edge observations, and the
// reactor routes them by source (deriveObservedPermitted) and reconciles them
// (fusion.go). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEvent)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	return nil
}

// Start has no background work (the module is event-driven); it only logs once
// if the data handle was never wired, so a silently-broken deployment is visible.
func (m *Module) Start(context.Context) error {
	if m.data == nil && m.log != nil {
		m.log.Warn("access-map: started without a data handle; the R/RW graph will not persist")
	}
	return nil
}

// Stop unsubscribes from the bus. It is idempotent.
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

// onEvent dispatches a delivered observation. It returns the handler error so a
// transient store error is visible; the connector's at-least-once redelivery
// plus the idempotent, fusing Upsert recover it.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil // not wired for persistence; drop (logged once in Start)
	}
	if e.Type == event.TypeEdgeObserved {
		if edge, ok := event.EdgeOf(e); ok {
			_, err := m.Ingest(ctx, e.Tenant, edge)
			return err
		}
	}
	return nil
}

// APINamespace returns the module's namespace; it roots the routes at
// /v1/m/accessmap/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them
// by verb tier (a viewer gets the read permissions). Both are sensitive reads.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permGraphRead, permDriftRead}
}

// APIRoutes mounts the module's privileged read endpoints. The engine wraps each
// with authentication, tenant resolution and the permission check before the
// handler runs; each handler additionally self-audits the graph read.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/graph", permGraphRead, m.handleGraph)
	reg.Handle("GET", "/neighbors", permGraphRead, m.handleNeighbors)
	reg.Handle("GET", "/drift", permDriftRead, m.handleDrift)

	// attack-path graph — reachability, privilege-escalation and exfil-route
	// queries over the existing AccessEdge data. All privileged, self-audited reads.
	reg.Handle("GET", "/attack-paths/reachability", permGraphRead, m.handleReachability)
	reg.Handle("GET", "/attack-paths/escalation", permGraphRead, m.handleEscalation)
	reg.Handle("GET", "/attack-paths/exfil", permGraphRead, m.handleExfil)
	reg.Handle("GET", "/attack-paths/summary", permGraphRead, m.handleAttackPathSummary)
}

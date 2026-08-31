// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.health"

// Namespace is the module's store and API namespace: its entities are
// "health.<entity>" and its routes mount under /v1/m/health/.
const Namespace = "health"

// Module permissions, granted to the built-in roles by verb tier (viewer→read,
// editor→write, admin/owner→admin). Reading health/incidents/SLA/dependencies is
// read-tier; declaring a check or posting a probe result is write-tier; deleting a
// check or manually resolving an incident is admin-tier.
const (
	permStatusRead auth.Permission = "health:status:read"
	permCheckRead  auth.Permission = "health:check:read"
	permCheckWrite auth.Permission = "health:check:write"
	permCheckAdmin auth.Permission = "health:check:admin"
)

// Sweep/SLA defaults. The sweep is the proactive engine: it transitions a silent
// subject to degraded, then down, opening an incident and emitting a finding —
// the anti-evasion staleness signal (docs/SECURITY-HARDENING.md).
const (
	defaultSweepInterval    = 30 * time.Second
	defaultDownMultiple     = 3                   // down after expected_interval*grace*downMultiple of silence
	defaultSLAWindow        = 30 * 24 * time.Hour // trailing window for SLA-breach evaluation
	defaultExpectedInterval = 300                 // seconds; a check's cadence when the caller omits it
	defaultGraceFactor      = 2                   // degraded after expected_interval*grace of silence
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithSweepInterval overrides the staleness sweep cadence.
func WithSweepInterval(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.sweepInterval = d
		}
	}
}

// WithDownMultiple overrides how many grace windows of silence escalate a subject
// from degraded to down.
func WithDownMultiple(n int64) Option {
	return func(m *Module) {
		if n > 0 {
			m.downMultiple = n
		}
	}
}

// WithSLAWindow overrides the trailing window the SLA-breach evaluation uses.
func WithSLAWindow(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.slaWindow = d
		}
	}
}

// Module is module XXII — health, SLA & uptime of agents and MCP servers. See
// doc.go for the bounded context, the minimal-data red line and how health is
// derived (liveness, active reports, staleness) rather than probed.
type Module struct {
	log    *slog.Logger
	data   api.ModuleData
	host   sdk.Host
	clock  model.Clock
	broker *broker

	sweepInterval time.Duration
	downMultiple  int64
	slaWindow     time.Duration

	mu          sync.Mutex
	seen        map[model.TenantID]struct{} // tenants with health activity, for the sweep
	cancelSub   func()                      // event-bus subscription cancel
	cancelSweep func()                      // sweep-goroutine cancel
	wg          sync.WaitGroup
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a health module with safe defaults. The defaults make the staleness
// sweep active out of the box; the composition root or a test may retune the
// cadence and windows via options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:         model.SystemClock{},
		broker:        newBroker(),
		sweepInterval: defaultSweepInterval,
		downMultiple:  defaultDownMultiple,
		slaWindow:     defaultSLAWindow,
		seen:          make(map[model.TenantID]struct{}),
	}
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
		Title:       "Health, SLA & uptime",
		Description: "Measures the reliability of agents and MCP servers: liveness/uptime derived from observed activity and active probe reports, SLA tracking, down/degraded/recovered alerts, a staleness signal for subjects that go silent (anti-evasion), and an auto-discovered dependency map. Produces minimal-data health findings on the bus for module XV to route; never delivers, never stores payloads.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init subscribes to the observation stream. Health derives liveness and the
// dependency map from edge.observed (the only live signal today); active probe
// results arrive over the API. It deliberately does NOT subscribe to
// finding.reported — that is XV's routing channel, and consuming it would make
// the module react to its OWN emitted health findings. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancelSub = cancel
	return nil
}

// Start launches the staleness sweep — the one piece of genuine background work
// in the plane (it must alert without waiting for a read). It warns once per
// un-wired seam so a deployment that cannot persist or alert is VISIBLE
// (docs/SECURITY-HARDENING.md). Without a data handle the sweep is not started (nothing to scan).
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("health: started without a data handle; checks, incidents and reliability history will not persist; staleness sweep disabled")
		return nil
	}
	if m.host == nil {
		m.log.Warn("health: no host wired; down/degraded/recovered signals will not reach module XV (notifications)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancelSweep = cancel
	m.mu.Unlock()
	m.wg.Add(1)
	go m.sweepLoop(ctx)
	return nil
}

// Stop unsubscribes, stops the sweep, waits for it, and closes the broker (ending
// any live SSE streams). Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	sub := m.cancelSub
	sweep := m.cancelSweep
	m.cancelSub = nil
	m.cancelSweep = nil
	m.mu.Unlock()
	if sub != nil {
		sub()
	}
	if sweep != nil {
		sweep()
	}
	m.wg.Wait()
	m.broker.close()
	return nil
}

// onEvent dispatches a delivered observation. Only edge.observed is handled
// (liveness + dependency map).
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	if e.Type == event.TypeEdgeObserved {
		if edge, ok := event.EdgeOf(e); ok {
			tenant, ok := tenantOf(e.Tenant)
			if !ok {
				return nil
			}
			return m.onEdge(ctx, tenant, edge)
		}
	}
	return nil
}

// markSeen records that a tenant has health activity, so the cross-tenant sweep
// (which cannot enumerate tenants through the tenant-scoped data seam) knows which
// tenants to scan. Bounded by the number of tenants that ever produce a health
// signal in this process's lifetime.
func (m *Module) markSeen(tenant model.TenantID) {
	m.mu.Lock()
	m.seen[tenant] = struct{}{}
	m.mu.Unlock()
}

// seenTenants returns a snapshot of the tenants to sweep.
func (m *Module) seenTenants() []model.TenantID {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.TenantID, 0, len(m.seen))
	for t := range m.seen {
		out = append(out, t)
	}
	return out
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

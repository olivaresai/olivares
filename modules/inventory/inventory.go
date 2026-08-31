// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.inventory"

// Namespace is the module's store and API namespace. Its registered entities are
// "inventory.<entity>" and its routes mount under /v1/m/inventory/.
const Namespace = "inventory"

// defaultStaleAfter is how long an entity may go unseen before the sweep marks
// it stale. It is deliberately generous: a quiet estate is not a dead one, and a
// session that simply ended is normal silence (the anti-evasion signal is the
// connector's job, not the inventory's).
const defaultStaleAfter = 30 * time.Minute

// defaultSweepInterval is how often the staleness sweep runs.
const defaultSweepInterval = 5 * time.Minute

// Module is the inventory/discovery module. It materializes the estate from the
// connector observation stream and exposes the catalog and staleness
// over the API.
type Module struct {
	log   *slog.Logger
	data  api.ModuleData
	clock model.Clock

	staleAfter    time.Duration
	sweepInterval time.Duration

	mu          sync.Mutex
	cancel      func()                      // bus unsubscribe
	stop        chan struct{}               // closed to stop the sweeper
	wg          sync.WaitGroup              // sweeper goroutine
	seenTenants map[model.TenantID]struct{} // tenants observed (the sweep's scope)
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

// WithClock overrides the module clock (tests inject a deterministic clock).
//
// It exists because `last_seen` is an OBSERVATION timestamp — the moment the estate
// was seen, not the moment anything happened to it — and the staleness sweep
// (catalog.go:135) compares it against now. With the clock nailed to
// model.SystemClock{} there was no way to write a test that pins "now" and asserts
// what the sweep decides at a boundary, so the sweep's own threshold was only ever
// exercised against the wall clock of whatever box ran it.
//
// Eight modules in this tree already carry exactly this option with exactly this
// signature (evals.go:43, eventing.go:76, deploy.go:40, health.go:53, …). This is
// that idiom, not a new one.
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// New returns an inventory module with default thresholds.
func New(opts ...Option) *Module {
	m := &Module{
		clock:         model.SystemClock{},
		staleAfter:    defaultStaleAfter,
		sweepInterval: defaultSweepInterval,
		seenTenants:   make(map[model.TenantID]struct{}),
	}
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
		Title:       "Inventory & discovery",
		Description: "Passively discovers and catalogs the estate (agents, sessions, MCP servers, skills, tools, models, identities) from the observation stream.",
	}
}

// UseData receives the least-privilege, tenant-scoped data handle from the
// engine boot (the api.DataConsumer seam), before Start. The event handlers
// persist through it.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init wires the module to the bus. It subscribes to the discovery-relevant
// observation types: access edges (the estate's relationships) and cost samples
// (which reveal providers and models). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	if cfg := host.Config(); cfg.Settings != nil {
		if d := cfg.GetDuration("stale_after", 0); d > 0 {
			m.staleAfter = d
		}
		if d := cfg.GetDuration("sweep_interval", 0); d > 0 {
			m.sweepInterval = d
		}
	}
	cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved, event.TypeCostSampled}, m.onEvent)
	if err != nil {
		return err
	}
	m.cancel = cancel
	return nil
}

// Start launches the staleness sweep. The sweep is also exposed as Sweep for
// deterministic testing; here it runs on a ticker until Stop.
func (m *Module) Start(context.Context) error {
	if m.data == nil {
		// No data handle means the boot never wired the consumer seam; the module
		// can still receive events but cannot persist, so refuse silently-broken
		// operation by logging once. (In tests the seam is always wired.)
		if m.log != nil {
			m.log.Warn("inventory: started without a data handle; discovery will not persist")
		}
		return nil
	}
	m.mu.Lock()
	stop := make(chan struct{})
	m.stop = stop
	m.mu.Unlock()
	m.wg.Add(1)
	go m.sweepLoop(stop)
	return nil
}

// Stop unsubscribes and stops the sweeper. It is idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	stop := m.stop
	m.stop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stop != nil {
		close(stop)
	}
	m.wg.Wait()
	return nil
}

// onEvent dispatches a delivered observation to the right materializer. A
// handler error is logged by the engine and never stops delivery to others; the
// module returns it so a transient store error is visible, and relies on the
// connector's at-least-once redelivery plus idempotent find-or-create for
// recovery.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil // not wired for persistence; drop (logged once in Start)
	}
	switch e.Type {
	case event.TypeEdgeObserved:
		if edge, ok := event.EdgeOf(e); ok {
			return m.onEdge(ctx, e.Tenant, edge)
		}
	case event.TypeCostSampled:
		if cost, ok := event.CostOf(e); ok {
			return m.onCost(ctx, e.Tenant, cost)
		}
	}
	return nil
}

// tenantOf resolves an event's string tenant reference to a TenantID, or false
// when it is not a usable business tenant. A connector configured with a real
// tenant uuid yields a usable id; a placeholder label or the system tenant is
// skipped (the inventory never writes to the system partition).
func tenantOf(ref string) (model.TenantID, bool) {
	t, err := model.ParseTenantID(ref)
	if err != nil || t.IsZero() || t.IsSystem() {
		return "", false
	}
	return t, true
}

// noteTenant records a tenant the module has observed, so the staleness sweep —
// which cannot enumerate tenants (least-privilege: ModuleData withholds System)
// — knows which tenants to sweep.
func (m *Module) noteTenant(t model.TenantID) {
	m.mu.Lock()
	if m.seenTenants == nil {
		m.seenTenants = make(map[model.TenantID]struct{})
	}
	m.seenTenants[t] = struct{}{}
	m.mu.Unlock()
}

// tenantsSnapshot returns a copy of the observed tenants for the sweep.
func (m *Module) tenantsSnapshot() []model.TenantID {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.TenantID, 0, len(m.seenTenants))
	for t := range m.seenTenants {
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

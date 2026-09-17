// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
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

// defaultSweepBudget is the deadline the sweep gives EACH of its operations: the
// tenant enumeration, and then every tenant turn separately.
//
// Separately is the whole point. One deadline over the whole pass let an early
// tenant spend the budget of every tenant after it, so the last tenant of a
// large directory could never be reached. A per-operation budget makes one slow
// or wedged tenant cost one turn.
const defaultSweepBudget = 30 * time.Second

// ErrSweepScopeUnavailable is returned by Sweep when no authoritative source of
// sweep scope has been wired.
//
// It is an ERROR and not an empty pass on purpose. The scope is the estate's
// durable directory, and a module that answered "nothing to do" when it simply
// could not ask would report a silent gap as a clean sweep. There is deliberately
// no fallback to the tenants this process happens to have observed: that was the
// pre-C2a authority, and it is exactly what cannot survive a restart.
var ErrSweepScopeUnavailable = errors.New("inventory: the durable freshness sweep has no authoritative tenant scope source")

// ErrSweepDataUnavailable is returned by Sweep when the module holds no data
// handle. Start already degrades honestly in that configuration (it logs and
// does not run the loop); this is the same honesty for a direct caller — an
// absent data handle is never presented as available persistence.
var ErrSweepDataUnavailable = errors.New("inventory: the durable freshness sweep has no data handle")

// SweepScopeSource names the tenants the durable freshness sweep must visit.
//
// The whole interface is one method returning ids and an error, and every fact a
// caller needs is stated here rather than discovered:
//
//   - It is CONFIGURATION, wired once by the composition root before Start. The
//     module never rotates it concurrently and never invents one.
//   - The returned ids are CANDIDATES, not authority. They grant the module
//     nothing: every mutation still goes through the same tenant-scoped
//     ModuleData, behind the residency, suspension and leadership guards the
//     store already applies, so a tenant whose state changed between the
//     snapshot and its turn is refused there.
//   - The ids must be valid, non-zero, non-system, unique and ordered; an
//     implementation that cannot make its enumeration authoritative returns an
//     error, and the sweep then discards ANY rows that came with it.
//   - It is called once per pass, with a context carrying its own deadline. It
//     must not be a place to do tenant work: the sweep gives each tenant turn a
//     fresh budget precisely so this call cannot consume it.
//
// The privileged enumeration lives behind this seam, in the composition root
// that already holds the store. Inventory receives ids, never a Store, a
// SystemScope, a SourceStore or Auth — the module still cannot open a scope it
// was not given.
type SweepScopeSource interface {
	ListSweepTenants(ctx context.Context) ([]model.TenantID, error)
}

// Module is the inventory/discovery module. It materializes the estate from the
// connector observation stream and exposes the catalog and staleness
// over the API.
type Module struct {
	log   *slog.Logger
	data  api.ModuleData
	clock model.Clock

	staleAfter    time.Duration
	sweepInterval time.Duration
	sweepBudget   time.Duration

	// scope is the durable sweep scope. It is written once by
	// UseSweepScopeSource before Start and read under mu, so a pass never sees a
	// half-installed seam.
	scope SweepScopeSource

	mu     sync.Mutex
	cancel func()        // bus unsubscribe
	stop   chan struct{} // closed to stop the sweeper
	wg     sync.WaitGroup
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

// WithSweepBudget overrides the per-operation deadline of the durable sweep
// (defaultSweepBudget). A non-positive value keeps the default.
//
// It is part of the interface rather than a test convenience: "each operation
// gets its own 30s" is a fact a caller has to know, and the only way to assert
// that a wedged tenant does not consume the following tenants' budget is to be
// able to shorten it. A test that proved it by waiting the real 30s would be
// measuring the box.
func WithSweepBudget(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.sweepBudget = d
		}
	}
}

// New returns an inventory module with default thresholds.
func New(opts ...Option) *Module {
	m := &Module{
		clock:         model.SystemClock{},
		staleAfter:    defaultStaleAfter,
		sweepInterval: defaultSweepInterval,
		sweepBudget:   defaultSweepBudget,
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

// UseSweepScopeSource receives the durable sweep scope from the composition
// root. Like UseData it is boot-time wiring, called before Start.
func (m *Module) UseSweepScopeSource(src SweepScopeSource) {
	m.mu.Lock()
	m.scope = src
	m.mu.Unlock()
}

// sweepScope returns the wired scope source, or nil.
func (m *Module) sweepScope() SweepScopeSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scope
}

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
	// SDK extraction accepts pointer payloads; a typed nil is not an observation.
	switch payload := e.Payload.(type) {
	case *sdkmodel.EdgeObservation:
		if payload == nil {
			return nil
		}
	case *sdkmodel.CostSample:
		if payload == nil {
			return nil
		}
	}
	switch e.Type {
	case event.TypeEdgeObserved:
		if edge, ok := event.EdgeOf(e); ok {
			return m.onEdgeEvent(ctx, e, edge)
		}
	case event.TypeCostSampled:
		if cost, ok := event.CostOf(e); ok {
			return m.onCostEvent(ctx, e, cost)
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

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

// warnf logs at warn level if a logger is set. The sweep failing is a WARN and
// not a debug line: the estate's freshness has stopped advancing, and an
// operator reading default-level logs has to be able to see that.
func (m *Module) warnf(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

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
const Name = "olivares.notify"

// Namespace is the module's store and API namespace: its entities are
// "notify.<entity>" and its routes mount under /v1/m/notify/.
const Namespace = "notify"

// Module permissions, granted to the built-in roles by verb tier (viewer→read,
// editor→write, admin/owner→admin). Reading routes/deliveries is read-tier;
// declaring/retargeting a route is write-tier; deleting a route or firing a test
// notification is admin-tier.
const (
	permRouteRead    auth.Permission = "notify:route:read"
	permRouteWrite   auth.Permission = "notify:route:write"
	permRouteAdmin   auth.Permission = "notify:route:admin"
	permDeliveryRead auth.Permission = "notify:delivery:read"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithDispatcher wires the transport seam. Without it, every matched notification
// is recorded as undelivered (fail-closed, deny-by-default).
func WithDispatcher(d Dispatcher) Option {
	return func(m *Module) {
		if d != nil {
			m.dispatch = d
		}
	}
}

// withoutNudge disables the background nudge worker so tests drive delivery via an
// explicit, synchronous pump (deterministic — no async delivery racing an assertion).
func withoutNudge() Option { return func(m *Module) { m.nudgeDisabled = true } }

// WithOutboxTuning overrides the durable-outbox retry ladder, stale-claim window and
// scan batch (tests drive the backoff/DLQ/stale-rescue paths with a deterministic
// clock and tiny values). A nil/empty schedule or a non-positive value keeps the
// default for that knob.
func WithOutboxTuning(retry []time.Duration, stale time.Duration, batch int) Option {
	return func(m *Module) {
		if len(retry) > 0 {
			m.outboxRetrySchedule = retry
		}
		if stale > 0 {
			m.outboxStaleClaim = stale
		}
		if batch > 0 {
			m.outboxBatch = batch
		}
	}
}

// Module is module XV — output integrations & notifications. See doc.go for the
// router-vs-transport split, the inbound finding channel, the minimal-data red
// line and the deny-closed default of its one seam.
type Module struct {
	log      *slog.Logger
	data     api.ModuleData
	host     sdk.Host
	clock    model.Clock
	dispatch Dispatcher

	// Durable-outbox tuning (defaults in New; tests shrink them via WithOutboxTuning).
	outboxRetrySchedule  []time.Duration
	outboxStaleClaim     time.Duration
	outboxDeliverTimeout time.Duration
	outboxBatch          int

	// nudge: a bounded background worker that drains a tenant's outbox right after an
	// enqueue, so first-attempt delivery is low-latency AND works on a per-tenant data
	// handle — the composition-root pump's cross-tenant enumeration can be empty on a
	// multi-tenant Postgres without --admin-dsn (System reads run RLS-limited), which
	// would otherwise leave freshly enqueued notifications undelivered until the pump
	// can enumerate. The nudge is leader-safe (its claim Mutate fails closed on a
	// standby) and best-effort (the periodic pump is the durable backstop for retries).
	nudgeCh       chan model.TenantID
	nudgeCtx      context.Context
	nudgeCancel   context.CancelFunc
	nudgeWg       sync.WaitGroup
	nudgeDisabled bool // tests drive delivery via an explicit pump for determinism

	mu        sync.Mutex
	cancelSub func()
}

// nudgeBuffer bounds the queued nudges: a burst beyond this drops to the periodic pump
// (never blocks the bus handler).
const nudgeBuffer = 256

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a notify module with a safe, deny-closed default transport (the nop
// dispatcher records-but-does-not-send). The composition root replaces it with the
// real connector-backed adapter via WithDispatcher.
func New(opts ...Option) *Module {
	m := &Module{
		clock:                model.SystemClock{},
		dispatch:             nopDispatcher{},
		outboxRetrySchedule:  defaultRetrySchedule,
		outboxStaleClaim:     defaultStaleClaim,
		outboxDeliverTimeout: defaultOutboxDeliverTimeout,
		outboxBatch:          defaultOutboxBatch,
		nudgeCh:              make(chan model.TenantID, nudgeBuffer),
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
		Title:       "Output integrations & notifications",
		Description: "The notification router: subscribes to the product-wide finding stream and to governance approval lifecycle events, and routes alerts (health, spend, security, regressions, compliance), interactive approve/deny cards, and resolution notices by kind/severity/source to Slack/Teams/PagerDuty/Opsgenie/SIEM/webhook destinations, with per-route dedup and throttling and an append-only delivery ledger. Decides what/who/when; the output connectors do the how. Carries only non-sensitive metadata, never payloads or secrets.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from the
// engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// Init subscribes to the human-facing channels it routes: finding.reported —
// the product-wide alert stream every module emits on (S02); approval.requested
// — an opened governance approval that must reach a human approver as an
// interactive approve/deny card (the origination half of the HITL chat
// round-trip); and approval.resolved — the terminal lifecycle notice. It
// deliberately does NOT subscribe to cost.sampled or edge.observed (raw
// telemetry, not alerts). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe(busTypes(), m.onEvent)
	if err != nil {
		return err
	}
	m.cancelSub = cancel
	return nil
}

// Start launches the outbox NUDGE worker (the module's only background work: a bounded
// drainer that gives a freshly enqueued notification a low-latency first attempt via
// the per-tenant data handle — see the nudge fields). It also warns once per un-wired
// seam so a deployment that would record-but-never-deliver, or that has no destinations
// provisioned, is VISIBLE (docs/SECURITY-HARDENING.md).
func (m *Module) Start(context.Context) error {
	m.mu.Lock()
	if m.nudgeCancel == nil && !m.nudgeDisabled {
		m.nudgeCtx, m.nudgeCancel = context.WithCancel(context.Background())
		m.nudgeWg.Add(1)
		go m.nudgeWorker()
	}
	m.mu.Unlock()

	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("notify: started without a data handle; routes and the delivery ledger will not persist")
	}
	if _, ok := m.dispatch.(nopDispatcher); ok {
		m.log.Warn("notify: no dispatcher wired (connectors); matched notifications will be recorded as undelivered")
	} else if len(m.dispatch.Destinations()) == 0 {
		// The route RECORDS unknown_destination and answers; nothing refuses. Rule: Info.
		m.log.Info("notify: dispatcher wired but 0 destinations provisioned; routes will record unknown_destination until a destination is configured")
	}
	return nil
}

// Stop unsubscribes from the bus and tears down the nudge worker. Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancel := m.cancelSub
	m.cancelSub = nil
	nudgeCancel := m.nudgeCancel
	m.nudgeCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if nudgeCancel != nil {
		nudgeCancel()
		m.nudgeWg.Wait()
	}
	return nil
}

// nudgeWorker drains queued tenant nudges, delivering each tenant's due outbox rows.
// It exits when the module stops. NotifyDispatchDue is idempotent and leader-safe, so a
// nudge that races the periodic pump (or runs on a standby) is harmless.
func (m *Module) nudgeWorker() {
	defer m.nudgeWg.Done()
	for {
		select {
		case <-m.nudgeCtx.Done():
			return
		case tenant := <-m.nudgeCh:
			if err := m.NotifyDispatchDue(m.nudgeCtx, tenant); err != nil {
				m.debugf("notify: outbox nudge pass failed", "tenant", tenant.String(), "err", err)
			}
		}
	}
}

// nudge asks the worker to drain tenant's outbox now. Non-blocking: a full buffer drops
// to the periodic pump, so a finding storm never blocks the bus handler.
func (m *Module) nudge(tenant model.TenantID) {
	if m.nudgeCh == nil || m.nudgeDisabled {
		return
	}
	select {
	case m.nudgeCh <- tenant:
	default: // buffer full — the periodic pump is the durable backstop
	}
}

// onEvent routes a delivered finding or approval lifecycle event to the
// matching routes. An event type it does not handle, a malformed payload or an
// unparseable tenant is ignored (best-effort, never an error back to the bus).
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil
	}
	switch e.Type {
	case event.TypeFindingReported:
		report, ok := event.FindingOf(e)
		if !ok {
			return nil
		}
		return m.processFinding(ctx, tenant, e, report)
	case event.TypeApprovalRequested:
		ar, ok := event.ApprovalRequestOf(e)
		if !ok {
			return nil
		}
		return m.processApproval(ctx, tenant, e, ar)
	case event.TypeApprovalResolved:
		ar, ok := event.ApprovalResolutionOf(e)
		if !ok {
			return nil
		}
		return m.processApprovalResolution(ctx, tenant, e, ar)
	default:
		return nil
	}
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

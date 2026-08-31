// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.eventing"

// Namespace is the module's store and API namespace: its entities are
// "eventing.<entity>" and its routes mount under /v1/m/eventing/.
const Namespace = "eventing"

// Module permissions, granted to the built-in roles by verb tier (viewer→read,
// editor→write, admin/owner→admin). Reading subscriptions, the catalog, the
// event log and deliveries is read-tier (the event log additionally applies the
// per-type RBAC filter to the CALLER); creating/updating a subscription and
// rotating its secret is write-tier; deleting, replaying, redelivering and test
// deliveries are admin-tier (they move data to an external endpoint or destroy
// configuration).
const (
	permSubRead      auth.Permission = "eventing:subscription:read"
	permSubWrite     auth.Permission = "eventing:subscription:write"
	permSubAdmin     auth.Permission = "eventing:subscription:admin"
	permEventRead    auth.Permission = "eventing:event:read"
	permDeliveryRead auth.Permission = "eventing:delivery:read"
)

// Defaults for the delivery engine. The retry schedule is the Stripe-shaped
// doubling ladder: an exhausted schedule dead-letters the delivery (the DLQ).
var defaultRetrySchedule = []time.Duration{
	30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour,
}

const (
	defaultWorkers       = 2
	defaultDispatchBatch = 64
	defaultStaleClaim    = 5 * time.Minute
	defaultRetention     = 7 * 24 * time.Hour
	defaultHTTPTimeout   = 10 * time.Second
	// disabledRecheck is how far a queued delivery for a DISABLED subscription
	// is pushed before being looked at again (re-enable resumes the stream).
	disabledRecheck = 15 * time.Minute
	// egressParkRecheck is the same idea for a delivery parked because the egress
	// destination control could not DECIDE, and it is shorter on purpose. A disabled
	// subscription and an estate stop are deliberate states that a human ends, so
	// fifteen minutes costs nothing; an unreadable rollout row or policy store is an
	// OUTAGE, and one that clears in two seconds should not hold a tenant's evidence
	// for a quarter of an hour. One minute recovers promptly without turning a brief
	// blip into a re-scan storm.
	egressParkRecheck = time.Minute
	// maxPayloadBytes bounds a captured payload; a larger one is dropped with a
	// warning (minimal-data facts never approach this).
	maxPayloadBytes = 64 << 10
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithAuthorizer wires the engine authorizer. Without it NOTHING is delivered
// (deny-closed): the per-event RBAC filter cannot run, so the dispatcher parks
// every delivery and warns at Start.
func WithAuthorizer(a Authz) Option {
	return func(m *Module) {
		if a != nil {
			m.authz = a
		}
	}
}

// WithSecretSealer wires secret-at-rest encryption. Without it subscriptions
// cannot be created or rotated (fail-closed).
func WithSecretSealer(s SecretSealer) Option {
	return func(m *Module) {
		if s != nil {
			m.sealer = s
		}
	}
}

// WithDoer overrides the outbound HTTP client (tests). The default is the
// module's SSRF-guarded, timeout-bounded, redirect-refusing client.
func WithDoer(d Doer) Option {
	return func(m *Module) {
		if d != nil {
			m.doer = d
		}
	}
}

// DeliveryPause is the kill-switch verdict for a tenant's delivery pass:
// when Paused, every due delivery whose event type is NOT in Exempt is PARKED
// (re-queued with a recheck delay, never consumed) until the stop lifts —
// re-enable resumes the stream from here, the disabled-subscription semantics.
// Exempt carries the governance channel (approval/kill-switch event types) so
// an estate stop never silences the very rail its own dual-control re-enable
// is decided through.
type DeliveryPause struct {
	Paused bool
	Exempt map[string]struct{}
}

// DeliveryGate is the estate kill-switch seam, consulted ONCE per tenant
// dispatch pass (outside any store transaction). The composition-root adapter
// owns the deny-closed posture: on an unreadable stop state it reports
// Paused=true with the static governance exemptions, so the module never has
// to guess. A module-level gate error (adapter contract violated) parks
// everything — an unreadable stop state never means "deliver".
type DeliveryGate interface {
	Check(ctx context.Context, tenant model.TenantID) (DeliveryPause, error)
}

// WithDeliveryGate wires the kill-switch delivery gate. Without it no stop
// ever parks deliveries (the composition root always wires the governance-backed
// adapter).
func WithDeliveryGate(g DeliveryGate) Option {
	return func(m *Module) {
		if g != nil {
			m.gate = g
		}
	}
}

// WithSinkRenderer wires the SIEM-sink renderer. Without it, SIEM-sink
// subscriptions are parked (deny-closed) and only generic-webhook subscriptions
// deliver; Start warns if any sink subscriptions exist without it. The generic
// webhook path never touches the renderer.
func WithSinkRenderer(r SinkRenderer) Option {
	return func(m *Module) {
		if r != nil {
			m.renderer = r
		}
	}
}

// WithAllowLoopback permits loopback endpoint hosts (plain-HTTP included) —
// for tests and single-box development ONLY; the production default refuses
// them (SSRF, docs/SECURITY-HARDENING.md).
func WithAllowLoopback(allow bool) Option {
	return func(m *Module) { m.allowLoopback = allow }
}

// WithRetrySchedule overrides the retry ladder (tests use a short one). An
// empty schedule means a single attempt, then the DLQ.
func WithRetrySchedule(s []time.Duration) Option {
	return func(m *Module) { m.retrySchedule = s }
}

// WithRetention overrides how long captured events and finished deliveries are
// kept (the replay window). Zero or negative keeps the default.
func WithRetention(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.retention = d
		}
	}
}

// WithWorkers overrides the dispatch worker count.
func WithWorkers(n int) Option {
	return func(m *Module) {
		if n > 0 {
			m.workers = n
		}
	}
}

// Module is the eventing platform. See doc.go.
type Module struct {
	log      *slog.Logger
	data     api.ModuleData
	host     sdk.Host
	clock    model.Clock
	authz    Authz
	sealer   SecretSealer
	doer     Doer
	gate     DeliveryGate // Kill-switch park gate (nil = no stop ever parks)
	renderer SinkRenderer // SIEM-sink renderer (nil = only generic webhooks deliver)
	// egress is the operator's destination policy (nil = no policy in force, which
	// permits everything — the tri-state's ABSENT state, not its EMPTY one).
	egress EgressPolicySource
	// rollout is this deployment's DURABLE disposition for the egress destination
	// control (unit G). It is what tells an absent policy on a fresh install
	// (deny) apart from an absent policy on an upgrade (permit) — a difference the
	// policy tri-state cannot express, because it is a fact about the deployment's
	// history rather than about its configuration. nil = not wired, which behaves as
	// before this unit and is announced at Start.
	rollout EgressRolloutSource
	// rolloutState caches the durable disposition for rolloutCacheTTL. Failures are
	// never cached.
	rolloutState rolloutCache
	// writerFence is the durable state of the CROSS-VERSION WRITER FENCE (unit H): the
	// minimum capability a binary must declare to introduce or move a destination. It is a
	// separate durable control from `rollout` above, deliberately — see
	// EgressWriterFenceControlKey. nil = not wired, which is not armed.
	writerFence EgressWriterFenceSource
	// compat owns the per-tenant record of the destinations this deployment already
	// had when the control was installed. Built in UseData, because it needs the data
	// handle; nil until then, and a nil one means compatibility exceptions cannot be
	// consulted — which resolves to the ENFORCED answer, never to a permit.
	compat *egressCompat
	// resolver looks a destination up ONCE so the authorization and the dial refer to
	// the same machine. nil uses the default resolver.
	resolver egress.Resolver

	allowLoopback bool
	retrySchedule []time.Duration
	retention     time.Duration
	workers       int
	batch         int
	staleClaim    time.Duration

	// nudge wakes a worker for a tenant with fresh work; sends never block
	// (drops are fine — the composition-root pump is the completeness backstop).
	nudge chan model.TenantID

	mu        sync.Mutex
	cancelSub func()
	runCancel context.CancelFunc
	wg        sync.WaitGroup

	// otlpRemapWarned dedups the once-per-subscription warning about the
	// format-catalog remap's wire-shape change for stored "otlp" SIEM sinks
	// delivering ledger events (see send in dispatch.go).
	otlpRemapWarned sync.Map
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/
// permission seam and the data-consumer seam. RegisterSchema (the engine-side
// SchemaProvider seam) is structural and verified by the runtime at boot/test.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns an eventing module with safe, deny-closed defaults: no authorizer
// (nothing delivers), no secret sealer (nothing stores a secret), the guarded
// HTTP client, and the default retry/retention/worker settings. The composition
// root wires the real seams via the With* options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:         model.SystemClock{},
		sealer:        nopSealer{},
		retrySchedule: defaultRetrySchedule,
		retention:     defaultRetention,
		workers:       defaultWorkers,
		batch:         defaultDispatchBatch,
		staleClaim:    defaultStaleClaim,
		nudge:         make(chan model.TenantID, 64),
	}
	for _, o := range opts {
		o(m)
	}
	if m.doer == nil {
		m.doer = m.guardedClient()
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
		Title:       "Eventing platform (typed event subscriptions)",
		Description: "Module XIX's eventing half: tenant self-service subscriptions to typed control-plane events, with durable at-least-once webhook delivery (retries with backoff, dead-letter queue), replay from a per-tenant cursor HMAC signing, and a deny-closed per-event RBAC filter so a consumer only receives events its role may see. The durable log, not the in-proc bus, is the delivery source of truth.",
	}
}

// UseData receives the least-privilege, tenant-parameterized data handle from
// the engine boot (the api.DataConsumer seam), before Start.
func (m *Module) UseData(d api.ModuleData) {
	m.data = d
	// The compatibility record needs a data handle, so it is built here rather than in
	// New. It shares the module's clock so a test that pins time pins the seeding
	// timestamp too.
	m.compat = newEgressCompat(d, m.clock)
}

// UseAuthorizer late-binds the engine authorizer (the deployBinder pattern):
// boot constructs it AFTER the modules, because its ABAC evaluator comes from
// the governance module. Must be called before Start; nil keeps the deny-closed
// default (nothing delivers).
func (m *Module) UseAuthorizer(a Authz) {
	if a != nil {
		m.authz = a
	}
}

// UseSecretSealer late-binds secret-at-rest encryption (boot owns the key
// material's home, the engine data dir). Must be called before Start; nil keeps
// the fail-closed default (no subscriptions can be created).
// UseEgressRollout late-binds this deployment's durable rollout disposition for the
// egress destination control (unit G).
//
// It is late-bound because the fact lives in the store, and the module is constructed
// BEFORE the store is opened — the module has to exist first so its schema, including
// the witness table the classification reads, is registered. Binding it after the
// store opens is the same shape UseAuthorizer and UseSecretSealer already use.
func (m *Module) UseEgressRollout(s EgressRolloutSource) {
	if s != nil {
		m.rollout = s
	}
}

// UseEgressWriterFence late-binds the writer fence's durable state (unit H). Same shape and
// same reason as UseEgressRollout: the fact lives in the store, and the module is constructed
// before the store is opened so its schema — including the witness table the classification reads
// — is registered first.
func (m *Module) UseEgressWriterFence(s EgressWriterFenceSource) {
	if s != nil {
		m.writerFence = s
	}
}

func (m *Module) UseSecretSealer(s SecretSealer) {
	if s != nil {
		m.sealer = s
	}
}

// UseSinkRenderer late-binds the SIEM-sink renderer (the composition root
// constructs it after the modules, like the authorizer). Must be called before
// Start; nil keeps the deny-closed default (SIEM-sink deliveries are parked).
func (m *Module) UseSinkRenderer(r SinkRenderer) {
	if r != nil {
		m.renderer = r
	}
}

// Init subscribes to exactly the cataloged event types (the public allowlist;
// deny-closed — an uncataloged type never enters the platform) and keeps the
// host. It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	cancel, err := host.Subscribe(catalogTypes(), m.onEvent)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.cancelSub = cancel
	m.mu.Unlock()
	return nil
}

// Start launches the dispatch workers and warns once per un-wired seam, so a
// deployment that captures-but-never-delivers (no authorizer) or cannot accept
// subscriptions (no sealer) is VISIBLE (docs/SECURITY-HARDENING.md).
func (m *Module) Start(context.Context) error {
	if m.log != nil {
		if m.data == nil {
			m.log.Warn("eventing: started without a data handle; nothing will be captured or delivered")
		}
		if m.authz == nil {
			m.log.Warn("eventing: no authorizer wired; deliveries will be parked, NOT sent (deny-closed) — wire eventing.WithAuthorizer in the composition root")
		}
		if _, ok := m.sealer.(nopSealer); ok {
			m.log.Warn("eventing: no secret sealer wired; subscriptions cannot be created (fail-closed) — wire eventing.WithSecretSealer in the composition root")
		}
		if m.renderer == nil {
			m.log.Warn("eventing: no SIEM sink renderer wired; any SIEM-sink subscriptions will be PARKED, not sent (deny-closed) — wire eventing.WithSinkRenderer in the composition root")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.runCancel = cancel
	m.mu.Unlock()
	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker(ctx)
	}
	return nil
}

// Stop unsubscribes from the bus, stops the workers and waits for them.
// Idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	cancelSub, runCancel := m.cancelSub, m.runCancel
	m.cancelSub, m.runCancel = nil, nil
	m.mu.Unlock()
	if cancelSub != nil {
		cancelSub()
	}
	if runCancel != nil {
		runCancel()
	}
	m.wg.Wait()
	return nil
}

// worker drains nudges: each nudge is a tenant with (probably) due work.
func (m *Module) worker(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case tenant := <-m.nudge:
			if err := m.DispatchDue(ctx, tenant); err != nil && ctx.Err() == nil {
				m.debugf("eventing: dispatch pass failed", "tenant", tenant.String(), "err", err)
			}
		}
	}
}

// nudgeTenant wakes a worker for tenant without ever blocking the caller (the
// bus handler); a dropped nudge is recovered by the periodic pump.
func (m *Module) nudgeTenant(t model.TenantID) {
	select {
	case m.nudge <- t:
	default:
	}
}

// APINamespace roots the module's routes at /v1/m/eventing/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant
// them by verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permSubRead, permSubWrite, permSubAdmin, permEventRead, permDeliveryRead}
}

// APIRoutes mounts the module's routes. The engine wraps each with
// authentication, tenant resolution and the declared permission check, and pins
// the data handle to the resolved tenant. Managing subscriptions is a
// privileged, audited action (docs/SECURITY-HARDENING.md): create/update/rotate is write-tier;
// delete, replay, redeliver and test deliveries are admin-tier.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/event-types", permSubRead, m.handleEventTypes)
	// the egress destination policy's own surface. Read-tier, because an author
	// who cannot create a subscription still needs to know whether a policy is why —
	// and the dry-run is no better an oracle than attempting the create would be.
	reg.Handle("GET", "/egress-policy", permSubRead, m.handleEgressPolicyStatus)
	reg.Handle("POST", "/egress-policy/check", permSubRead, m.handleEgressPolicyCheck)
	// Unit G: the itemized compatibility record. ADMIN tier because it names
	// hosts, and because planning an actuation is an administrative act.
	reg.Handle("GET", "/egress-policy/compat", permSubAdmin, m.handleEgressCompatReport)

	reg.Handle("GET", "/subscriptions", permSubRead, m.handleListSubscriptions)
	reg.Handle("POST", "/subscriptions", permSubWrite, m.handleCreateSubscription)
	reg.Handle("GET", "/subscriptions/{id}", permSubRead, m.handleGetSubscription)
	reg.Handle("PUT", "/subscriptions/{id}", permSubWrite, m.handleUpdateSubscription)
	reg.Handle("DELETE", "/subscriptions/{id}", permSubAdmin, m.handleDeleteSubscription)
	reg.Handle("GET", "/subscriptions/{id}/revisions", permSubRead, m.handleListRevisions)
	reg.Handle("POST", "/subscriptions/{id}/restore", permSubWrite, m.handleRestoreSubscription)
	reg.Handle("POST", "/subscriptions/{id}/rotate-secret", permSubWrite, m.handleRotateSecret)
	reg.Handle("POST", "/subscriptions/{id}/rotate-auth", permSubWrite, m.handleRotateAuthValue)
	reg.Handle("POST", "/subscriptions/{id}/test", permSubAdmin, m.handleTestSubscription)
	reg.Handle("POST", "/subscriptions/{id}/replay", permSubAdmin, m.handleReplay)

	reg.Handle("GET", "/events", permEventRead, m.handleListEvents)
	reg.Handle("GET", "/deliveries", permDeliveryRead, m.handleListDeliveries)
	reg.Handle("GET", "/dead-letters", permDeliveryRead, m.handleListDeadLetters)
	reg.Handle("POST", "/deliveries/{id}/redeliver", permSubAdmin, m.handleRedeliver)
}

// handleEventTypes returns the public event-type catalog: the subscribable
// types, their stability tier and the permission gating receipt.
func (m *Module) handleEventTypes(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	type entry struct {
		Type        string `json:"type"`
		Stability   string `json:"stability"`
		Permission  string `json:"permission"`
		Description string `json:"description"`
	}
	out := make([]entry, 0, len(catalog))
	for _, e := range catalog {
		out = append(out, entry{
			Type:        string(e.Type),
			Stability:   e.Stability,
			Permission:  string(e.Permission),
			Description: e.Description,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"event_types": out})
}

// debugf logs at debug level if a logger is set.
func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}

// warnf logs at warn level if a logger is set.
func (m *Module) warnf(msg string, args ...any) {
	if m.log != nil {
		m.log.Warn(msg, args...)
	}
}

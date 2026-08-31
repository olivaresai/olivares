// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package natsbus is the distributed event-bus backend: the in-process
// bus, bridged across nodes over NATS. It exists so an HA deployment's
// standby-origin observations (background sources, identity sweeps) reach the
// leader's processing — today they die on the standby's write gate without
// ever crossing nodes.
//
// # Design: local fan-out unchanged, NATS as a bridge
//
// Publish delivers to LOCAL subscribers through the embedded in-proc bus
// first — every S02 §4 guarantee (blocking backpressure, zero local loss,
// panic isolation, Close drain) holds unchanged on the hot path, with no
// codec in the way — and then bridges the event to NATS best-effort. The
// bridge connection uses NoEcho, so its wildcard subscription receives ONLY
// remote-origin events, which it re-materializes and injects into the local
// fan-out: no double delivery, and per-publisher ordering holds on both paths
// (one connection per node; NATS preserves source order per connection).
//
// # Delivery semantics (honest): cross-node is at-most-once
//
// Core NATS persists nothing. A bridged event is lost when the server
// restarts, when a disconnected client's reconnect buffer (8 MiB) overflows
// (and "buffered" was never "delivered" — the reconnect may not happen), or
// when a saturated subscription overflows its pending limits (slow-consumer
// drop, counted). This mirrors what the in-proc contract already is — the bus
// is at-most-once and the eventing platform's capture is the durability
// boundary — so subscribers gain reach, not new obligations.
//
// This OPEN bridge stays at-most-once by design: at-least-once at the bus would
// duplicate-deliver to subscribers, and the 2026-06 subscriber census showed
// most handlers dedupe only by a best-effort bounded scan, not a hard guarantee
// (the ADR). An at-least-once JetStream backend IS available as a SEPARATE,
// closed enterprise add-on (enterprise/durablebus, build-tag gated): it
// EMBEDS this bus for the non-durable types and carries the enforcement-class
// events (finding.reported, cost.sampled, …) over a replicated JetStream stream
// with dedup at the bus boundary (Nats-Msg-Id + an inject-time dedup window), so
// duplicate suppression becomes one owned guarantee instead of N handler-by-
// handler ones. Nothing here moves behind that wall — the open bridge is
// unchanged; the durable path is new code the default binary never links
// (BridgeExclude, the embedding seam, is inert when unset; LICENSING.md).
//
// # HA: inject only on the leader
//
// Remote events are injected into local subscribers ONLY while the
// composition root's InjectGate reports this node active (the leader).
// One predicate kills the whole class of standby side effects — duplicate
// external notifications, duplicate derived findings, an ErrNotLeader warn
// per event per store-writing subscriber — at the bus boundary instead of
// patching every module. A standby still PUBLISHES to the bridge (that is the
// point); it just does not process other nodes' events while passive. During
// the ≤2s failover overlap two nodes may both inject (the gate is the
// advisory elector tick, not a fence); the eventing capture dedupe
// (tenant_id, event_id) absorbs the double capture.
package natsbus

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// Options tunes the bus around the operator Config.
type Options struct {
	// Logger receives bridge lifecycle and (throttled) error logs. nil uses
	// slog.Default().
	Logger *slog.Logger
	// DemoteError is forwarded to the embedded in-proc bus (expected
	// steady-state handler errors log at Debug; see eventbus.Options).
	DemoteError func(error) bool
	// Decoders extends DefaultDecoders with module-owned payload types the
	// composition root can import but this package cannot (license boundary).
	Decoders map[event.Type]PayloadDecoder
	// BridgeExclude, when non-nil and returning true for an event Type, keeps that
	// type's events LOCAL on the Core-NATS bridge: they still fan out to local
	// subscribers (and the wildcard subscription never receives them from this
	// node), but Publish does not bridge them over Core NATS. It exists so a
	// distributed backend that carries some types out-of-band can EMBED this bus
	// for the rest without double-delivering those types (the enterprise durable
	// JetStream bus: the durable set travels JetStream, everything else
	// travels this best-effort bridge). nil — the default and the only value the
	// open binary ever uses — bridges every type, exactly as before.
	BridgeExclude func(event.Type) bool
}

// Bus is the hybrid distributed bus. It satisfies eventbus.Bus,
// eventbus.NamedSubscriber and eventbus.StatsProvider.
type Bus struct {
	inner      eventbus.Bus
	innerNamed eventbus.NamedSubscriber
	innerClass eventbus.ClassSubscriber
	innerStats eventbus.StatsProvider
	nc         *nats.Conn
	sub        *nats.Subscription
	prefix     string
	log        *slog.Logger
	decoders   map[event.Type]PayloadDecoder
	// bridgeExclude (Options.BridgeExclude) skips Core-NATS bridging for the types
	// it matches; nil bridges everything (the open default).
	bridgeExclude func(event.Type) bool

	// injectGate, when set, must return true for remote events to be injected
	// into local subscribers (the HA leader predicate). nil = always inject
	// (single-node, tests).
	injectGate atomic.Pointer[func() bool]

	// subReady is true only while the SERVER has confirmed this bridge's subscription:
	// the SUB round-tripped AND was not rejected. It is cleared on every disconnect and
	// re-earned on reconnect, because a re-sent subscription can be refused for a reason
	// that did not exist at boot (a revoked permission, a changed account limit).
	subReady atomic.Bool

	closed       atomic.Bool
	injectCtx    context.Context
	injectCancel context.CancelFunc

	pubErrors      atomic.Uint64
	decodeErrors   atomic.Uint64
	gateSkipped    atomic.Uint64
	invalidSubject atomic.Uint64

	warnPub    logThrottle
	warnDecode logThrottle
	warnAsync  logThrottle
}

// flushTimeout bounds the two round trips this bus waits on: the one that confirms the
// bridge subscription with the server, and Close's final flush. Long enough
// that a healthy server on a loaded box answers, short enough that an unresponsive one
// delays a boot by two seconds rather than hanging it.
const flushTimeout = 2 * time.Second

var (
	_ eventbus.Bus             = (*Bus)(nil)
	_ eventbus.NamedSubscriber = (*Bus)(nil)
	_ eventbus.ClassSubscriber = (*Bus)(nil)
	_ eventbus.StatsProvider   = (*Bus)(nil)
)

// New validates cfg, connects to NATS and starts the bridge subscription.
// Config errors and unusable options fail construction — the caller (boot)
// must abort, never fall back to in-proc silently. An UNREACHABLE server is
// not a config error: the connection is established with retry-forever
// semantics (the bridge converges when the server returns; the connected
// gauge and its alert carry the visibility), because crash-looping the whole
// control plane on a NATS blip would couple engine availability to the bus.
func New(cfg Config, opts Options) (*Bus, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	inner := eventbus.NewInProc(eventbus.Options{
		Logger:      log,
		Buffer:      cfg.Buffer,
		DemoteError: opts.DemoteError,
	})
	decoders := DefaultDecoders()
	for t, d := range opts.Decoders {
		decoders[t] = d
	}

	injectCtx, injectCancel := context.WithCancel(context.Background())
	b := &Bus{
		inner:         inner,
		innerNamed:    inner.(eventbus.NamedSubscriber),
		innerClass:    inner.(eventbus.ClassSubscriber),
		innerStats:    inner.(eventbus.StatsProvider),
		prefix:        cfg.SubjectPrefix,
		log:           log,
		decoders:      decoders,
		bridgeExclude: opts.BridgeExclude,
		injectCtx:     injectCtx,
		injectCancel:  injectCancel,
	}

	natsOpts := []nats.Option{
		nats.Name(cfg.Name),
		// NoEcho: this connection never receives its own publishes — the local
		// fan-out already delivered them; the subscription is remote-origin only.
		nats.NoEcho(),
		// Retry forever: the bridge is infrastructure; it reconnects (with the
		// client's default backoff) rather than dying after 60 attempts and
		// silently black-holing cross-node delivery for the process lifetime.
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			// Readiness dies with the connection, before anything else: a subscription the
			// server no longer holds is not confirmed, and saying otherwise for the length of
			// an outage is the same false green this bus was just fixed to stop giving.
			b.subReady.Store(false)
			if b.closed.Load() {
				return // our own Close; an outage warning on a clean stop is noise
			}
			log.Warn("natsbus: disconnected from NATS; bridged delivery suspended (publishes buffer up to the reconnect buffer, then error)", "err", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			// The client re-sends its subscriptions on reconnect, but it does so
			// asynchronously and it does not check whether they were accepted. So the bridge
			// re-earns readiness here rather than assuming it: reconnected is a transport
			// fact, routable is a different one, and announcing the second when only the
			// first is true is how a publish right after "recovered" is lost for good.
			if err := b.confirmSubscription(cfg.SubjectPrefix + ".>"); err != nil {
				b.subReady.Store(false)
				log.Warn("natsbus: reconnected to NATS but the bridge subscription is NOT confirmed; this node receives nothing cross-node until it is",
					"url", nc.ConnectedUrlRedacted(), "err", err)
				return
			}
			b.subReady.Store(true)
			log.Info("natsbus: reconnected to NATS and the bridge subscription is confirmed", "url", nc.ConnectedUrlRedacted())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if b.warnAsync.ok() {
				log.Warn("natsbus: async client error (slow-consumer drops are counted in the bridge stats)", "err", err)
			}
		}),
	}
	if cfg.CredentialsFile != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(cfg.CredentialsFile))
	}
	if cfg.TLSCAFile != "" {
		natsOpts = append(natsOpts, nats.RootCAs(cfg.TLSCAFile))
	}
	if cfg.TLSCertFile != "" {
		natsOpts = append(natsOpts, nats.ClientCert(cfg.TLSCertFile, cfg.TLSKeyFile))
	}

	nc, err := nats.Connect(cfg.URL, natsOpts...)
	if err != nil {
		injectCancel()
		_ = inner.Close()
		return nil, err
	}
	b.nc = nc
	sub, err := nc.Subscribe(cfg.SubjectPrefix+".>", b.onMsg)
	if err != nil {
		nc.Close()
		injectCancel()
		_ = inner.Close()
		return nil, err
	}
	b.sub = sub
	// AND WAIT UNTIL THE SERVER ACTUALLY HAS IT. nc.Subscribe only queues the SUB protocol
	// message on the client's outbound buffer; it returns long before the server has
	// registered the interest. Core NATS is at-most-once with no store-and-forward, so every
	// message published into that window is dropped in silence — by the server, which has
	// nobody to route it to.
	//
	// The window is not theoretical and it is not narrow. Measured 2026-08-02 on this
	// embedded server: in 595 of 600 constructions New returned before the subscription
	// existed server-side (the command is in the commit that introduced this). It stayed
	// invisible because a node usually has other work to do before anyone publishes to it.
	// TestBridgeCrossNodeTypedDelivery is the case that does not, and it had been failing
	// mainline-ci — first as a 5s timeout, widened to 15s on 2026-07-24, and red again nine
	// days later. A wider deadline cannot close this: once the publish lands in the window
	// the message no longer exists.
	//
	// Flush is a PING/PONG round trip, so its return means the server has PROCESSED
	// everything queued before it, this SUB included. Whether the server ACCEPTED it is a
	// second question, and confirmSubscription is where it is asked.
	//
	// IT IS NOT FATAL, and that is deliberate. This constructor's contract, stated above, is
	// that an unreachable server is not a config error: RetryOnFailedConnect hands back a
	// connection that is still dialing, so a flush that failed the construction would
	// crash-loop the whole control plane on a NATS outage — precisely the coupling those
	// retry-forever options were chosen to avoid. The client re-sends its subscriptions on
	// every reconnect, so the guarantee re-arms itself on that path. What the flush buys is
	// the boot case, where the server is there and the confirmation is free; what the log
	// buys is that the remaining window is announced rather than silent.
	switch {
	case !nc.IsConnected():
		// An unreachable server is not a config error — see this function's contract. The
		// reconnect handler earns readiness when the client gets through.
		log.Warn("natsbus: the bridge subscription is queued but the client is still dialing; cross-node delivery to this node begins when it connects and the subscription is confirmed",
			"subject_prefix", cfg.SubjectPrefix)
	default:
		if err := b.confirmSubscription(cfg.SubjectPrefix + ".>"); err != nil {
			// A REFUSED subscription IS a configuration error, and this constructor's
			// contract fails construction on those. The distinction that matters is not
			// "did it work" but "who can fix it": an outage resolves itself and a refusal
			// does not, so a bridge that came up refused would sit there reporting
			// Connected while receiving nothing, for as long as nobody looked.
			_ = sub.Unsubscribe()
			nc.Close()
			injectCancel()
			_ = inner.Close()
			return nil, fmt.Errorf("natsbus: %w", err)
		}
		b.subReady.Store(true)
	}
	log.Info("natsbus: distributed bus bridge started (local fan-out in-proc, cross-node at-most-once)",
		"subject_prefix", cfg.SubjectPrefix, "connected", nc.IsConnected(), "subscription_confirmed", b.subReady.Load())
	return b, nil
}

// confirmSubscription round-trips with the server and then asks whether the server ACCEPTED
// the subscription.
//
// The distinction is the whole point. Flush is a PING/PONG, so its return proves the SUB was
// PROCESSED — it does not prove it was ALLOWED. A server that refuses a subject answers out of
// band with -ERR, which the client records as the connection's last error and hands to the async
// handler AFTER the flush has already returned nil. A bridge that treated a nil flush as
// readiness would log itself active while the server had inserted nothing into its routing
// table: cross-node input silently absent, `Connected` still 1, every pending/drop/decode
// counter still 0, and the disconnected alert never firing. That is the worst shape a fault can
// take here — invisible to the exact instruments built to see it.
func (b *Bus) confirmSubscription(subject string) error {
	if err := b.nc.FlushTimeout(flushTimeout); err != nil {
		return err
	}
	if err := b.nc.LastError(); err != nil {
		return fmt.Errorf("the server refused the bridge subscription to %s: %w", subject, err)
	}
	if b.sub != nil && !b.sub.IsValid() {
		return fmt.Errorf("the bridge subscription to %s is no longer valid after the round trip", subject)
	}
	return nil
}

// SubscriptionConfirmed reports whether the server has confirmed this bridge's subscription.
// It is NOT the same question as IsConnected: a connected bridge whose subscription the server
// refused, or has not yet re-registered after a reconnect, is reachable and not routable.
//
// It reads the connection as well as the flag, and that is structural rather than belt-and-
// braces. The disconnect callback clears the flag, but a callback is asynchronous: a caller can
// observe IsConnected already false while the handler has not run, and for that window a bridge
// whose subscription the server no longer holds would answer "confirmed". Deriving the answer
// makes readiness unable to outlive the connection at all, instead of merely unlikely to. The
// flag still exists because the converse is not derivable — a reconnected client has to EARN
// confirmation again, and only the reconnect path can say it did.
func (b *Bus) SubscriptionConfirmed() bool { return b.subReady.Load() && b.nc.IsConnected() }

// SetInjectGate installs the HA leader predicate (store.Leader().Active). It
// is late-bound because the store opens after the bus is built; until set,
// remote events inject unconditionally — harmless, because module subscribers
// only exist after rt.Start, which runs after the gate is set.
func (b *Bus) SetInjectGate(gate func() bool) {
	b.injectGate.Store(&gate)
}

// Publish delivers locally with full in-proc semantics, then bridges
// best-effort. A bridge failure is counted and (throttled) logged, never
// returned: local subscribers already processed the event, and failing the
// publisher into a retry would duplicate locally to maybe-deliver remotely.
func (b *Bus) Publish(ctx context.Context, e event.Event) error {
	if b.closed.Load() {
		return eventbus.ErrBusClosed
	}
	if err := b.inner.Publish(ctx, e); err != nil {
		return err
	}
	b.bridge(e)
	return nil
}

func (b *Bus) bridge(e event.Event) {
	if b.bridgeExclude != nil && b.bridgeExclude(e.Type) {
		// Carried out-of-band by a distributed backend embedding this bus (
		// durable JetStream path owns the enforcement set). Local fan-out already
		// happened; do not also Core-bridge it, or remote peers would see it twice.
		return
	}
	if err := ValidSubjectTokens(string(e.Type)); err != nil {
		// A type that cannot be a subject stays node-local (the in-proc default
		// accepted it and local delivery already happened). All first-party and
		// cataloged types are clean; this guards custom module types.
		b.invalidSubject.Add(1)
		if b.warnPub.ok() {
			b.log.Warn("natsbus: event type cannot map to a NATS subject; delivered locally only", "event_type", string(e.Type), "err", err)
		}
		return
	}
	data, err := EncodeEvent(e)
	if err != nil {
		b.pubErrors.Add(1)
		if b.warnPub.ok() {
			b.log.Warn("natsbus: could not encode event for the bridge; delivered locally only", "event_type", string(e.Type), "err", err)
		}
		return
	}
	if err := b.nc.Publish(b.prefix+"."+string(e.Type), data); err != nil {
		b.pubErrors.Add(1)
		if b.warnPub.ok() {
			b.log.Warn("natsbus: bridge publish failed; delivered locally only", "event_type", string(e.Type), "err", err)
		}
	}
}

// onMsg is the bridge subscription callback: gate, decode, inject. The inject
// is a BLOCKING in-proc publish on purpose — a saturated local subscriber
// pushes back into this subscription's pending buffer (visible as
// BridgeStats.Pending*), and beyond its limits NATS counts slow-consumer
// drops: bounded memory, counted loss, never unbounded growth.
func (b *Bus) onMsg(m *nats.Msg) {
	if gp := b.injectGate.Load(); gp != nil && !(*gp)() {
		b.gateSkipped.Add(1)
		return
	}
	e, err := DecodeEvent(m.Data, b.decoders)
	if err != nil {
		b.decodeErrors.Add(1)
		if b.warnDecode.ok() {
			b.log.Warn("natsbus: dropped an undecodable bridged event (rolling upgrade skew?)", "subject", m.Subject, "err", err)
		}
		return
	}
	// ErrBusClosed / ctx canceled mean we are shutting down: drop, like the
	// in-proc bus drops queued events at Close.
	_ = b.inner.Publish(b.injectCtx, e)
}

// Subscribe registers a local subscriber (full in-proc semantics).
func (b *Bus) Subscribe(types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.inner.Subscribe(types, h)
}

// SubscribeNamed implements eventbus.NamedSubscriber.
func (b *Bus) SubscribeNamed(name string, types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.innerNamed.SubscribeNamed(name, types, h)
}

// SubscribeClass forwards the subscriber's QoS delivery class to the inner in-proc
// bus, so a NATS deployment (and the enterprise durable bus that embeds this one)
// isolates the optional-output lanes exactly like the in-proc default. Local
// delivery — including re-injected cross-node events — flows through that inner
// bus, so the class governs both.
func (b *Bus) SubscribeClass(class eventbus.DeliveryClass, name string, types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.innerClass.SubscribeClass(class, name, types, h)
}

// BusStats implements eventbus.StatsProvider (the local fan-out's saturation
// snapshot; the bridge's own counters live in BridgeStats).
func (b *Bus) BusStats() eventbus.Stats {
	return b.innerStats.BusStats()
}

// BridgeStats is the NATS-side saturation/loss snapshot, complementing the
// local BusStats. The REAL queue under saturation is the subscription's
// pending buffer (default 500k msgs / 64 MiB), not the 256-deep local
// channels — the gauge that alerts before loss must watch Pending here.
type BridgeStats struct {
	Connected bool
	// SubscriptionConfirmed is the question Connected does not answer: has the SERVER
	// confirmed this bridge's subscription? A connected bridge whose subscription was
	// refused, or not yet re-registered after a reconnect, is reachable and not routable.
	SubscriptionConfirmed bool
	PendingMsgs           int
	PendingBytes          int
	// Dropped is the subscription's cumulative slow-consumer drop count (the
	// client library's own counter — error callbacks can coalesce, this cannot).
	Dropped int64
	// PublishErrors counts bridge publishes that failed (encode or send) —
	// those events stayed node-local.
	PublishErrors uint64
	// DecodeErrors counts bridged events dropped because they did not decode.
	DecodeErrors uint64
	// GateSkipped counts remote events not injected because this node was not
	// the leader (normal on a standby).
	GateSkipped uint64
	// InvalidSubject counts events whose type cannot be a NATS subject (stayed
	// node-local).
	InvalidSubject uint64
}

// Bridge returns the bridge snapshot.
func (b *Bus) Bridge() BridgeStats {
	st := BridgeStats{
		Connected:             b.nc.IsConnected(),
		SubscriptionConfirmed: b.SubscriptionConfirmed(),
		PublishErrors:         b.pubErrors.Load(),
		DecodeErrors:          b.decodeErrors.Load(),
		GateSkipped:           b.gateSkipped.Load(),
		InvalidSubject:        b.invalidSubject.Load(),
	}
	if b.sub != nil {
		if msgs, bytes, err := b.sub.Pending(); err == nil {
			st.PendingMsgs, st.PendingBytes = msgs, bytes
		}
		if d, err := b.sub.Dropped(); err == nil {
			st.Dropped = int64(d)
		}
	}
	return st
}

// Close stops the bridge, then the local fan-out, mirroring the in-proc
// contract: further Publish/Subscribe rejected, the in-flight handler
// invocation finishes, queued events are dropped. Deliberately NOT
// nats.Drain(): draining would process up to the whole pending buffer (500k
// events) on shutdown, where contract parity means drop-queued-and-stop.
func (b *Bus) Close() error {
	if b.closed.Swap(true) {
		return nil
	}
	if b.sub != nil {
		_ = b.sub.Unsubscribe()
	}
	// Unblock an inject that is mid-backpressure before closing the inner bus.
	b.injectCancel()
	if b.nc != nil {
		// Push the last bridged publishes out to the server before closing. This is a flush,
		// not a drain: nats.Drain would process up to the whole pending buffer on shutdown,
		// which is the opposite of the contract stated above.
		_ = b.nc.FlushTimeout(flushTimeout)
		b.nc.Close()
	}
	return b.inner.Close()
}

// logThrottle rate-limits a warn category to one per 10 seconds, so a
// sustained failure (NATS outage, decode skew) is visible without becoming a
// per-event log storm.
type logThrottle struct {
	last atomic.Int64
}

func (t *logThrottle) ok() bool {
	now := time.Now().Unix()
	last := t.last.Load()
	if now-last < 10 {
		return false
	}
	return t.last.CompareAndSwap(last, now)
}

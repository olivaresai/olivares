// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/olivaresai/olivares/sdk/event"
)

// DefaultBuffer is the per-subscriber queue depth used when Options.Buffer is 0.
// A bounded buffer means a slow subscriber applies backpressure to publishers
// (Publish blocks, honoring its context) rather than the bus growing without
// limit — losing events silently would be worse than slowing a publisher.
const DefaultBuffer = 256

// Options configures an in-process bus.
type Options struct {
	// Logger receives a warning whenever a handler returns an error or panics.
	// nil uses slog.Default().
	Logger *slog.Logger
	// Buffer is the per-subscriber queue depth; 0 uses DefaultBuffer.
	Buffer int
	// DemoteError, when non-nil, downgrades a matching handler error from Warn
	// to Debug. The composition root uses it for errors that are EXPECTED in
	// steady state on some nodes — e.g. store.ErrNotLeader on an HA standby
	// whose subscribers receive cluster-wide events over the NATS bridge:
	// without demotion every event × every store-writing handler would Warn,
	// drowning real signals. Demoted errors still count in Stats.HandlerErrors.
	DemoteError func(error) bool
}

// inProcBus is the channel-based Bus. Each subscriber owns a buffered channel
// drained by a dedicated goroutine, so one slow handler never blocks delivery to
// other subscribers (only its own queue fills, applying backpressure to
// publishers of the events it cares about). A handler panic is recovered and
// logged: a faulty module cannot crash the engine (failure isolation, ARCHITECTURE.md).
type inProcBus struct {
	log    *slog.Logger
	buffer int
	demote func(error) bool

	mu     sync.RWMutex
	closed bool
	subs   map[*subscription]struct{}

	// Saturation counters (docs/17 §5), exposed via BusStats.
	publishBlocked atomic.Uint64
	dropped        atomic.Uint64
	// Per-class optional-output drops (QoS): a telemetry/notify subscriber whose
	// bounded queue is full drops the event here instead of stalling the publisher.
	droppedTelemetry atomic.Uint64
	droppedNotify    atomic.Uint64
	handlerErrors    atomic.Uint64
	// enqueued/handled bracket a delivery: enqueued ticks when an event lands on
	// a subscriber's queue, handled when that subscriber's handler RETURNS. The
	// pair is what makes "all published work is finished" observable — the
	// queue-depth gauge cannot express it, because an event leaves the queue as
	// its handler starts, not as it ends.
	enqueued atomic.Uint64
	handled  atomic.Uint64

	// ctx is canceled on Close and handed to every handler so handlers can
	// observe engine shutdown.
	ctx    context.Context
	cancel context.CancelFunc
}

// NewInProc builds an in-process event bus.
func NewInProc(opts Options) Bus {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	buf := opts.Buffer
	if buf <= 0 {
		buf = DefaultBuffer
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &inProcBus{
		log:    log,
		buffer: buf,
		demote: opts.DemoteError,
		subs:   make(map[*subscription]struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
}

type subscription struct {
	bus      *inProcBus
	name     string                  // SubscribeNamed identity; "" = anonymous
	class    DeliveryClass           // QoS lane (default ClassEnforcement = durable/block)
	typeList []event.Type            // the filter as registered (for Stats)
	types    map[event.Type]struct{} // nil/empty = all
	ch       chan event.Event
	quit     chan struct{}
	done     chan struct{} // closed when the goroutine has exited
	once     sync.Once
	handler  event.Handler
}

func (s *subscription) wants(t event.Type) bool {
	if len(s.types) == 0 {
		return true
	}
	_, ok := s.types[t]
	return ok
}

func (b *inProcBus) Subscribe(types []event.Type, h event.Handler) (Subscription, error) {
	return b.SubscribeNamed("", types, h)
}

// SubscribeNamed implements NamedSubscriber: like Subscribe, with a stable
// identity that labels the subscriber's queue-depth series (docs/17 §5). It
// registers on the durable ClassEnforcement lane — the pre-QoS behavior, so a
// caller that does not opt into a class keeps full backpressure semantics.
func (b *inProcBus) SubscribeNamed(name string, types []event.Type, h event.Handler) (Subscription, error) {
	return b.SubscribeClass(ClassEnforcement, name, types, h)
}

// SubscribeClass implements ClassSubscriber: the subscriber declares its QoS
// delivery class (durable enforcement/state vs droppable telemetry/notify). An
// unknown class is rejected — a typo must never silently place a durable consumer
// on a droppable lane.
func (b *inProcBus) SubscribeClass(class DeliveryClass, name string, types []event.Type, h event.Handler) (Subscription, error) {
	if h == nil {
		return nil, errors.New("eventbus: nil handler")
	}
	if !class.valid() {
		return nil, fmt.Errorf("eventbus: unknown delivery class %d", int(class))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrBusClosed
	}

	var set map[event.Type]struct{}
	if len(types) > 0 {
		set = make(map[event.Type]struct{}, len(types))
		for _, t := range types {
			set[t] = struct{}{}
		}
	}
	sub := &subscription{
		bus:      b,
		name:     name,
		class:    class,
		typeList: append([]event.Type(nil), types...),
		types:    set,
		ch:       make(chan event.Event, b.buffer),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		handler:  h,
	}
	b.subs[sub] = struct{}{}
	go sub.run()
	return sub, nil
}

// BusStats implements StatsProvider with a point-in-time saturation snapshot.
// Reading len(ch) is racy-by-nature but safe; a gauge does not need a lock on
// the hot path.
func (b *inProcBus) BusStats() Stats {
	b.mu.RLock()
	subs := make([]SubscriberStats, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, SubscriberStats{
			Name:     s.name,
			Class:    s.class,
			Types:    s.typeList,
			Depth:    len(s.ch),
			Capacity: cap(s.ch),
		})
	}
	b.mu.RUnlock()
	// Load handled BEFORE enqueued so the pair can never read as "more handled
	// than enqueued" under a concurrent publish; a snapshot that lags is fine,
	// one that implies impossible progress is not.
	handled := b.handled.Load()
	return Stats{
		Subscribers:      subs,
		PublishBlocked:   b.publishBlocked.Load(),
		Dropped:          b.dropped.Load(),
		DroppedTelemetry: b.droppedTelemetry.Load(),
		DroppedNotify:    b.droppedNotify.Load(),
		HandlerErrors:    b.handlerErrors.Load(),
		Enqueued:         b.enqueued.Load(),
		Handled:          handled,
	}
}

func (b *inProcBus) Publish(ctx context.Context, e event.Event) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return ErrBusClosed
	}
	// Snapshot the matching subscribers so we do not hold the lock while applying
	// backpressure (a blocking send must not stall Subscribe/Unsubscribe).
	targets := make([]*subscription, 0, len(b.subs))
	for sub := range b.subs {
		if sub.wants(e.Type) {
			targets = append(targets, sub)
		}
	}
	b.mu.RUnlock()

	// Two passes so an optional-output lane can never stall a durable one. First the
	// DURABLE lanes (enforcement/state): they must not lose events, so a full queue
	// applies backpressure (the publisher waits, honoring ctx). Then the OPTIONAL
	// lanes (telemetry/notify): a non-blocking send that drops-with-counter when the
	// queue is full, so a slow, wedged or panicking optional subscriber never delays
	// a security or state event.
	for _, sub := range targets {
		if !sub.class.blocks() {
			continue
		}
		// Fast path: a non-blocking send. When it fails the queue is full and the
		// publisher is about to stall — THE backpressure event docs/17 §5 wants
		// counted — so count it once, then fall through to the blocking send.
		select {
		case sub.ch <- e:
			b.enqueued.Add(1)
			continue
		default:
			b.publishBlocked.Add(1)
		}
		select {
		case sub.ch <- e:
			b.enqueued.Add(1)
		case <-sub.quit: // unsubscribed mid-publish: drop for this subscriber
			b.dropped.Add(1)
		case <-ctx.Done():
			return ctx.Err()
		case <-b.ctx.Done(): // bus closing
			return ErrBusClosed
		}
	}
	for _, sub := range targets {
		if sub.class.blocks() {
			continue
		}
		select {
		case sub.ch <- e:
			b.enqueued.Add(1)
		default:
			b.countDrop(sub.class) // bounded optional lane full: drop, counted per class
		}
	}
	return nil
}

// countDrop records an optional-output drop against its class so saturation is
// visible per lane (Stats.DroppedTelemetry / DroppedNotify). Durable lanes never
// reach here — they block instead of dropping.
func (b *inProcBus) countDrop(class DeliveryClass) {
	switch class {
	case ClassTelemetry:
		b.droppedTelemetry.Add(1)
	case ClassNotify:
		b.droppedNotify.Add(1)
	}
}

func (b *inProcBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.cancel()
	subs := make([]*subscription, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.subs = make(map[*subscription]struct{})
	b.mu.Unlock()

	for _, sub := range subs {
		sub.signalQuit()
	}
	for _, sub := range subs {
		<-sub.done
	}
	return nil
}

func (s *subscription) run() {
	defer close(s.done)
	for {
		// Prioritize quit so shutdown is prompt and deterministic; buffered
		// events are dropped once unsubscribed/closed.
		select {
		case <-s.quit:
			return
		default:
		}
		select {
		case e := <-s.ch:
			s.invoke(e)
		case <-s.quit:
			return
		}
	}
}

// invoke runs the handler, recovering panics so a faulty subscriber cannot take
// down the engine, and logging a handler error or panic. An error matching
// Options.DemoteError logs at Debug instead of Warn — expected steady-state
// errors (an HA standby's ErrNotLeader on every cluster-wide event) must
// not drown real signals; both still count in Stats.HandlerErrors.
func (s *subscription) invoke(e event.Event) {
	// Registered FIRST so it runs LAST: the invocation is only "handled" once
	// the recover below has run and any error/panic has been accounted. A
	// recovered panic still counts — the handler is over either way, and a
	// barrier that stalls on a panicking subscriber would be a worse lie than
	// no barrier at all.
	defer s.bus.handled.Add(1)
	defer func() {
		if r := recover(); r != nil {
			s.bus.handlerErrors.Add(1)
			s.bus.log.Error("eventbus: handler panicked",
				"event_type", string(e.Type), "panic", r)
		}
	}()
	if err := s.handler(s.bus.ctx, e); err != nil {
		s.bus.handlerErrors.Add(1)
		if s.bus.demote != nil && s.bus.demote(err) {
			s.bus.log.Debug("eventbus: handler returned error (expected on this node)",
				"event_type", string(e.Type), "subscriber", s.name, "error", err)
			return
		}
		s.bus.log.Warn("eventbus: handler returned error",
			"event_type", string(e.Type), "subscriber", s.name, "error", err)
	}
}

func (s *subscription) signalQuit() {
	s.once.Do(func() { close(s.quit) })
}

// Unsubscribe removes the subscriber and waits for its goroutine to drain and
// exit. It is idempotent.
func (s *subscription) Unsubscribe() {
	b := s.bus
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()

	s.signalQuit()
	<-s.done
}

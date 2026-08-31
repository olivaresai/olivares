// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// moduleHost is the sdk.Host a module receives at Init: the event bus, a scoped
// logger and the module's config. It deliberately exposes no data store (the
// permanent license boundary, see sdk.Host). It tracks the subscriptions the
// module makes so the runtime can release them when it stops the module.
type moduleHost struct {
	bus   eventbus.Bus
	log   *slog.Logger
	cfg   sdk.Config
	name  string
	class eventbus.DeliveryClass // QoS lane for this module's subscriptions

	mu   sync.Mutex
	subs map[eventbus.Subscription]struct{}
}

var _ sdk.Host = (*moduleHost)(nil)

func (h *moduleHost) Publish(ctx context.Context, e event.Event) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Source == "" {
		e.Source = h.name
	}
	return h.bus.Publish(ctx, e)
}

func (h *moduleHost) Subscribe(types []event.Type, handler event.Handler) (func(), error) {
	// Attach the module's name to the subscription when the bus supports it, so
	// the per-subscriber queue-depth gauge (docs/17 §5) labels series by
	// module instead of anonymously. Pure observability; semantics unchanged.
	var sub eventbus.Subscription
	var err error
	if cs, ok := h.bus.(eventbus.ClassSubscriber); ok {
		// Declare the module's QoS delivery class AND name: an optional-output
		// module (telemetry/notify) is a drop lane so it can never stall the durable
		// enforcement/state lanes, and the queue-depth gauge labels series by module.
		sub, err = cs.SubscribeClass(h.class, h.name, types, handler)
	} else if named, ok := h.bus.(eventbus.NamedSubscriber); ok {
		sub, err = named.SubscribeNamed(h.name, types, handler)
	} else {
		sub, err = h.bus.Subscribe(types, handler)
	}
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[eventbus.Subscription]struct{})
	}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	// The returned cancel both unsubscribes and forgets the subscription, so a
	// module that churns subscriptions (e.g. re-subscribing on config reload)
	// does not accumulate dead entries until Stop. Idempotent and safe with the
	// bulk unsubscribeAll (a second Unsubscribe is a no-op; delete of an absent
	// key is a no-op).
	return func() {
		sub.Unsubscribe()
		h.mu.Lock()
		delete(h.subs, sub)
		h.mu.Unlock()
	}, nil
}

func (h *moduleHost) Logger() *slog.Logger { return h.log }

func (h *moduleHost) Config() sdk.Config { return h.cfg }

// unsubscribeAll releases every subscription the module made, called when the
// runtime stops the module (sdk.Host documents this automatic cleanup).
func (h *moduleHost) unsubscribeAll() {
	h.mu.Lock()
	subs := h.subs
	h.subs = nil
	h.mu.Unlock()
	for s := range subs {
		s.Unsubscribe()
	}
}

// busSink is the Sink handed to a source connector: every observation it emits
// is lifted into an event and published on the bus. The tenant and source name
// are stamped from the source's registration.
type busSink struct {
	bus    eventbus.Bus
	tenant string
	source string
}

var _ sdk.Sink = (*busSink)(nil)

func (s *busSink) Emit(ctx context.Context, obs model.Observation) error {
	e := event.FromObservation(s.tenant, s.source, obs)
	if e.Type == "" {
		// Defensive: a sealed observation whose kind has no event mapping. Reject
		// rather than publish a typeless event (the connector treats this as fatal).
		return fmt.Errorf("runtime: observation kind %q has no event type mapping", obs.ObservationType())
	}
	e.ID = uuid.NewString()
	return s.bus.Publish(ctx, e)
}

// notificationFromEvent is the default mapping from a bus event to the
// non-sensitive Notification an output connector delivers. Rich SIEM-style
// structured forwarding is a later concern (ARCHITECTURE.md); this is the minimal,
// safe projection.
func notificationFromEvent(e event.Event) sdk.Notification {
	n := sdk.Notification{
		Type:   string(e.Type),
		Tenant: e.Tenant,
		Time:   e.Time,
		Fields: map[string]string{},
	}
	switch e.Type {
	case event.TypeEdgeObserved:
		if o, ok := event.EdgeOf(e); ok {
			n.Title = "access observed"
			n.Body = fmt.Sprintf("%s %s %s", o.OriginRef, o.Mode, o.ResourceRef)
			n.Fields["origin"] = o.OriginRef
			n.Fields["resource"] = o.ResourceRef
			n.Fields["mode"] = string(o.Mode)
		}
	case event.TypeCostSampled:
		if o, ok := event.CostOf(e); ok {
			n.Title = "model usage sampled"
			n.Body = fmt.Sprintf("%s/%s", o.ProviderRef, o.ModelRef)
			n.Fields["provider"] = o.ProviderRef
			n.Fields["model"] = o.ModelRef
		}
	case event.TypeFindingReported:
		if o, ok := event.FindingOf(e); ok {
			n.Title = o.Title
			n.Severity = o.Severity
			n.Body = o.Kind
			n.Fields["subject"] = o.SubjectRef
		}
	case event.TypeMetricSampled:
		if o, ok := event.MetricOf(e); ok {
			n.Title = "metric sampled"
			n.Body = fmt.Sprintf("%s=%d %s", o.Name, o.Value, o.Unit)
			n.Fields["metric"] = o.Name
			n.Fields["subject"] = o.SubjectRef
		}
	}
	return n
}

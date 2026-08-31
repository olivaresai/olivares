// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/sdk/event"
)

// subscribeClassed registers a named composition-root subscriber with a QoS
// delivery class when the bus supports it (isolating the optional-output lanes —
// notify/telemetry — from the durable enforcement/state lanes), falling back to a
// named or plain Subscribe on a bus that predates the extension.
func subscribeClassed(bus eventbus.Bus, class eventbus.DeliveryClass, name string, types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	if cs, ok := bus.(eventbus.ClassSubscriber); ok {
		return cs.SubscribeClass(class, name, types, h)
	}
	if named, ok := bus.(eventbus.NamedSubscriber); ok {
		return named.SubscribeNamed(name, types, h)
	}
	return bus.Subscribe(types, h)
}

// registerBusMetrics wires the event-bus saturation SLIs (docs/17 §5, deferred
// by PR #32) into the shared registry as scrape-time
// collectors: the per-subscriber queue-depth gauge and the blocked/dropped
// publish counters, plus — when the NATS bridge is active — the bridge's
// connectivity/pending/loss families (the REAL queue under saturation is the
// bridge subscription's pending buffer, not the 256-deep local channels).
func registerBusMetrics(reg *metrics.Registry, bus eventbus.Bus) {
	sp, ok := bus.(eventbus.StatsProvider)
	if !ok {
		return
	}
	reg.RegisterFunc("olivares_eventbus_queue_depth", func(w io.Writer) {
		writeSubscriberGauge(w, sp, "olivares_eventbus_queue_depth",
			"Events queued for a subscriber, waiting for its handler (max across a subscriber's subscriptions). Saturation precedes publisher backpressure.",
			func(s eventbus.SubscriberStats) int { return s.Depth })
	})
	reg.RegisterFunc("olivares_eventbus_queue_capacity", func(w io.Writer) {
		writeSubscriberGauge(w, sp, "olivares_eventbus_queue_capacity",
			"A subscriber's bounded queue capacity (the depth at which publishers block).",
			func(s eventbus.SubscriberStats) int { return s.Capacity })
	})
	reg.RegisterFunc("olivares_eventbus_publish_blocked_total", func(w io.Writer) {
		writeCounter(w, "olivares_eventbus_publish_blocked_total",
			"Publishes that found a subscriber queue full and had to wait (the in-proc backpressure event).",
			sp.BusStats().PublishBlocked)
	})
	reg.RegisterFunc("olivares_eventbus_publish_dropped_total", func(w io.Writer) {
		writeCounter(w, "olivares_eventbus_publish_dropped_total",
			"Publishes that lost the race with an unsubscribe (queued events dropped at unsubscribe/close are by-design and not counted).",
			sp.BusStats().Dropped)
	})
	reg.RegisterFunc("olivares_eventbus_dropped_telemetry_total", func(w io.Writer) {
		writeCounter(w, "olivares_eventbus_dropped_telemetry_total",
			"Optional telemetry events dropped because a bounded telemetry-lane queue was full (QoS): an optional consumer that cannot keep up is dropped rather than stalling the durable enforcement/state lanes.",
			sp.BusStats().DroppedTelemetry)
	})
	reg.RegisterFunc("olivares_eventbus_dropped_notify_total", func(w io.Writer) {
		writeCounter(w, "olivares_eventbus_dropped_notify_total",
			"Optional notify events dropped because a bounded notify-lane queue was full (QoS). Notify wants a durable outbox; until that lands a full queue drops, counted here so the seam stays visible.",
			sp.BusStats().DroppedNotify)
	})
	reg.RegisterFunc("olivares_eventbus_handler_errors_total", func(w io.Writer) {
		writeCounter(w, "olivares_eventbus_handler_errors_total",
			"Handler invocations that returned an error or panicked (including errors demoted to Debug, e.g. a standby's expected ErrNotLeader).",
			sp.BusStats().HandlerErrors)
	})

	// The bridge SLIs are exposed via an INTERFACE, not the concrete *natsbus.Bus, so
	// the enterprise *durablebus.Bus — which EMBEDS *natsbus.Bus and promotes Bridge()
	// — is covered too. The durable bus keeps a real Core-NATS bridge for every
	// non-durable type, so its bridge saturation/loss gauges are just as load-bearing
	// (PendingMsgs is "the gauge that alerts before loss"). Asserting the concrete type
	// instead would silently drop these families on a durable deployment.
	if br, ok := bus.(interface {
		Bridge() natsbus.BridgeStats
	}); ok {
		reg.RegisterFunc("olivares_eventbus_bridge_connected", func(w io.Writer) {
			v := 0
			if br.Bridge().Connected {
				v = 1
			}
			fmt.Fprintf(w, "# HELP olivares_eventbus_bridge_connected Whether the NATS bridge connection is up (0 = cross-node delivery suspended).\n# TYPE olivares_eventbus_bridge_connected gauge\nolivares_eventbus_bridge_connected %d\n", v)
		})
		reg.RegisterFunc("olivares_eventbus_bridge_pending_messages", func(w io.Writer) {
			fmt.Fprintf(w, "# HELP olivares_eventbus_bridge_pending_messages Remote events buffered in the bridge subscription, not yet injected locally (the real saturation queue; drops begin at its limit).\n# TYPE olivares_eventbus_bridge_pending_messages gauge\nolivares_eventbus_bridge_pending_messages %d\n", br.Bridge().PendingMsgs)
		})
		reg.RegisterFunc("olivares_eventbus_bridge_dropped_total", func(w io.Writer) {
			writeCounter(w, "olivares_eventbus_bridge_dropped_total",
				"Remote events dropped by the bridge subscription after its pending limits filled (slow consumer): cross-node at-most-once loss, counted.",
				uint64(br.Bridge().Dropped))
		})
		reg.RegisterFunc("olivares_eventbus_bridge_publish_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_eventbus_bridge_publish_errors_total",
				"Events that could not be bridged (encode or send failure) and stayed node-local.",
				br.Bridge().PublishErrors)
		})
		reg.RegisterFunc("olivares_eventbus_bridge_decode_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_eventbus_bridge_decode_errors_total",
				"Bridged events dropped because they did not decode (e.g. rolling-upgrade payload skew).",
				br.Bridge().DecodeErrors)
		})
	}

	// the durable JetStream backend's own SLIs (enterprise durablebus.Bus). It
	// satisfies `interface{ Durable() eventbus.DurableStats }`; the open binary never
	// constructs such a bus, so this block is inert there (no enterprise import). These
	// families make the durable backend's failure modes — and the retention-loss
	// precursor (StreamPending approaching MaxAge) — visible instead of silent.
	if db, ok := bus.(interface {
		Durable() eventbus.DurableStats
	}); ok {
		reg.RegisterFunc("olivares_durablebus_connected", func(w io.Writer) {
			v := 0
			if db.Durable().Connected {
				v = 1
			}
			fmt.Fprintf(w, "# HELP olivares_durablebus_connected Whether the durable JetStream plane connection is up (0 = durable publish/consume suspended).\n# TYPE olivares_durablebus_connected gauge\nolivares_durablebus_connected %d\n", v)
		})
		reg.RegisterFunc("olivares_durablebus_leading", func(w io.Writer) {
			v := 0
			if db.Durable().Leading {
				v = 1
			}
			fmt.Fprintf(w, "# HELP olivares_durablebus_leading Whether this node runs the durable consumer (1 = it injects durable enforcement events).\n# TYPE olivares_durablebus_leading gauge\nolivares_durablebus_leading %d\n", v)
		})
		reg.RegisterFunc("olivares_durablebus_stream_pending", func(w io.Writer) {
			fmt.Fprintf(w, "# HELP olivares_durablebus_stream_pending Durable events waiting on this node's consumer (backlog). A sustained rise toward the stream's MaxAge is the precursor to retention-driven loss.\n# TYPE olivares_durablebus_stream_pending gauge\nolivares_durablebus_stream_pending %d\n", db.Durable().StreamPending)
		})
		reg.RegisterFunc("olivares_durablebus_published_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_published_total",
				"Durable events confirmed into the JetStream stream (PubAck received).", db.Durable().Published)
		})
		reg.RegisterFunc("olivares_durablebus_publish_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_publish_errors_total",
				"Durable publishes that failed (NOT durably stored — the failure was surfaced to the caller).", db.Durable().PublishErrors)
		})
		reg.RegisterFunc("olivares_durablebus_injected_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_injected_total",
				"Durable events delivered into local subscribers on the leader.", db.Durable().Injected)
		})
		reg.RegisterFunc("olivares_durablebus_dedup_skipped_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_dedup_skipped_total",
				"Durable redeliveries/overlaps suppressed at the dedup boundary (event.ID).", db.Durable().DedupSkipped)
		})
		reg.RegisterFunc("olivares_durablebus_inject_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_inject_errors_total",
				"Durable inject failures (the event was left unacked for redelivery — not lost).", db.Durable().InjectErrors)
		})
		reg.RegisterFunc("olivares_durablebus_decode_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_decode_errors_total",
				"Undecodable durable events terminated (e.g. rolling-upgrade payload skew).", db.Durable().DecodeErrors)
		})
		reg.RegisterFunc("olivares_durablebus_kv_errors_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_kv_errors_total",
				"Dedup-KV read/record failures (the event still delivered; dedup degraded to the in-memory tier).", db.Durable().KVErrors)
		})
		reg.RegisterFunc("olivares_durablebus_no_dedup_id_total", func(w io.Writer) {
			writeCounter(w, "olivares_durablebus_no_dedup_id_total",
				"Durable events delivered WITHOUT an event.ID (dedup impossible — should stay zero; nonzero means a publisher bypassed the durability contract).", db.Durable().NoDedupID)
		})
	}
}

// writeSubscriberGauge emits one gauge family labeled by subscriber name,
// aggregating multiple subscriptions of one subscriber by max (the saturation
// view). Anonymous subscriptions aggregate under "anonymous".
func writeSubscriberGauge(w io.Writer, sp eventbus.StatsProvider, name, help string, value func(eventbus.SubscriberStats) int) {
	type gaugeKey struct{ subscriber, class string }
	byKey := map[gaugeKey]int{}
	for _, s := range sp.BusStats().Subscribers {
		label := s.Name
		if label == "" {
			label = "anonymous"
		}
		k := gaugeKey{subscriber: label, class: s.Class.String()}
		// v is never negative, so >= also CREATES the entry for an idle subscriber
		// (a zero-depth series is signal, not absence).
		if v := value(s); v >= byKey[k] {
			byKey[k] = v
		}
	}
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	keys := make([]gaugeKey, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	// Deterministic exposition (subscriber, then QoS class), like the registry's families.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].subscriber != keys[j].subscriber {
			return keys[i].subscriber < keys[j].subscriber
		}
		return keys[i].class < keys[j].class
	})
	for _, k := range keys {
		fmt.Fprintf(w, "%s{subscriber=\"%s\",class=\"%s\"} %d\n",
			name, labelEscaper.Replace(k.subscriber), labelEscaper.Replace(k.class), byKey[k])
	}
}

// labelEscaper is the Prometheus 0.0.4 label-value escaping (backslash, quote,
// newline — and ONLY those; Go %q would emit escapes the exposition format does
// not define and a strict parser rejects, corrupting the whole scrape).
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func writeCounter(w io.Writer, name, help string, v uint64) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
}

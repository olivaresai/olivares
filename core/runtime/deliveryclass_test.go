// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// TestDeliveryClassForModule pins the QoS classification: only the optional-output
// modules are droppable; everything else defaults to the durable enforcement lane.
// A regression that flips a durable projection (e.g. security, finops, eventing)
// onto a droppable lane — silently allowing its events to be lost under load —
// must fail here.
func TestDeliveryClassForModule(t *testing.T) {
	cases := map[string]eventbus.DeliveryClass{
		"observability": eventbus.ClassTelemetry,   // in-memory counters, persists nothing
		"notify":        eventbus.ClassState,       // durable outbox intent — must block, not drop
		"security":      eventbus.ClassEnforcement, // durable detections feed containment
		"finops":        eventbus.ClassEnforcement, // durable cost ledger
		"eventing":      eventbus.ClassEnforcement, // durable capture + replay boundary
		"governance":    eventbus.ClassEnforcement, // PEP/containment
		"recording":     eventbus.ClassEnforcement, // audit ledger
		"access-map":    eventbus.ClassEnforcement, // durable graph projection
		"brand-new-mod": eventbus.ClassEnforcement, // unknown ⇒ durable by default
	}
	for name, want := range cases {
		if got := deliveryClassForModule(name); got != want {
			t.Errorf("deliveryClassForModule(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestCampaignNotifyLaneIsDurable pins the finding: once the durable outbox
// decoupled external delivery, the notify subscription MUST be on a blocking (durable)
// lane. If it is ever a droppable lane again, a finding/approval-card burst is dropped at
// the bus before the outbox persists the intent — silently losing security-alert and HITL
// delivery. ClassEnforcement and ClassState are the blocking lanes; Notify/Telemetry drop.
func TestCampaignNotifyLaneIsDurable(t *testing.T) {
	c := deliveryClassForModule("notify")
	if c == eventbus.ClassNotify || c == eventbus.ClassTelemetry {
		t.Fatalf("notify is on a DROPPABLE lane %v — the durable outbox's intent would be lost at the bus before it is persisted; it must be a blocking lane", c)
	}
}

// TestModuleHost_AssignsDeliveryClass verifies the wiring end to end: a module
// subscribing through its moduleHost lands on the bus with the class the runtime
// resolved for its name, so the QoS isolation actually applies to real subscribers.
func TestModuleHost_AssignsDeliveryClass(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{})
	defer bus.Close()

	for _, name := range []string{"observability", "notify", "security"} {
		h := &moduleHost{bus: bus, name: name, class: deliveryClassForModule(name)}
		cancel, err := h.Subscribe([]event.Type{event.Type("x")}, func(context.Context, event.Event) error { return nil })
		if err != nil {
			t.Fatalf("subscribe %s: %v", name, err)
		}
		defer cancel()
	}

	sp, ok := bus.(eventbus.StatsProvider)
	if !ok {
		t.Fatal("bus must implement StatsProvider")
	}
	want := map[string]eventbus.DeliveryClass{
		"observability": eventbus.ClassTelemetry,
		"notify":        eventbus.ClassState,
		"security":      eventbus.ClassEnforcement,
	}
	seen := map[string]bool{}
	for _, s := range sp.BusStats().Subscribers {
		if exp, ok := want[s.Name]; ok {
			seen[s.Name] = true
			if s.Class != exp {
				t.Errorf("subscriber %q registered as class %v, want %v", s.Name, s.Class, exp)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("subscriber %q not found in bus stats", name)
		}
	}
}

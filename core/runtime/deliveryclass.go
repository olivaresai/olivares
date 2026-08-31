// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import "github.com/olivaresai/olivares/core/eventbus"

// moduleDeliveryClass maps a module to its event-bus QoS delivery class. Only the
// OPTIONAL-OUTPUT modules are droppable; every other module defaults to the durable
// ClassEnforcement lane (block), preserving pre-QoS delivery guarantees so no state
// projection or enforcement decision is ever silently dropped.
//
//   - observability: process-global, in-memory counters that persist NOTHING — a
//     dropped sample is a lost data point, never lost state or a missed decision, so
//     it must never be able to stall a publisher that also feeds enforcement/state.
//   - notify: notification/alert dispatch to configured destinations. It USED to be a
//     drop lane because delivery was synchronous in the bus handler (a slow destination
//     could stall the durable lanes). The durable outbox now decouples the external send:
//     the handler only claims + enqueues an outbox row (an idempotent Mutate) and the
//     async pump/nudge worker delivers out of band. So notify is now a durable, idempotent
//     STATE projection and MUST block — otherwise a finding/approval-card burst is dropped
//     at the bus BEFORE the outbox can persist the intent, silently losing security-alert
//     and HITL-approval delivery. Block imposes only bounded store-latency backpressure,
//     exactly like every other durable subscriber.
//
// Everything else — security, governance, recording, finops, eventing, inventory,
// sessions, access-map, health, models, capabilities, orchestration, claudeadoption,
// liveingest, voice — projects durable state or drives enforcement and stays block.
var moduleDeliveryClass = map[string]eventbus.DeliveryClass{
	"observability": eventbus.ClassTelemetry,
	"notify":        eventbus.ClassState,
}

// deliveryClassForModule returns a module's QoS lane, defaulting to the durable
// ClassEnforcement lane for any module not explicitly declared droppable. Defaulting
// to durable is the safe choice: a new module keeps full backpressure until someone
// deliberately marks it optional.
func deliveryClassForModule(name string) eventbus.DeliveryClass {
	if c, ok := moduleDeliveryClass[name]; ok {
		return c
	}
	return eventbus.ClassEnforcement
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package amqp is the Olivares AI connector that OBSERVES an enterprise's AMQP 1.0
// event surface. AMQP 1.0 is an OASIS/ISO standard (ISO/IEC 19464) wire
// protocol, so a single client reaches BOTH of the targets that speak it natively —
// one connector, two integration targets:
//
//	target                 example addr                                    auth
//	RabbitMQ 4.0           amqps://rabbit.example:5671                      SASL PLAIN + TLS
//	Azure Service Bus      amqps://ns.servicebus.windows.net:5671          SASL PLAIN (SAS) + TLS
//
// (RabbitMQ 4.0 speaks AMQP 1.0 natively as a first-class protocol; earlier RabbitMQ
// used AMQP 0-9-1, which is a DIFFERENT protocol this connector does not target.)
//
// The client is the pure-Go github.com/Azure/go-amqp library (MIT), kept behind a
// narrow receiver/sender seam (seam.go) so the connector's observation logic runs
// offline in CI with no broker and no network.
//
// It ships two components:
//
//   - Source ("olivares.amqp") — attaches to a configured observation address and
//     observes the event flow: which destinations carry traffic (namespace→address
//     topology) and who produces onto them (producer→address, a write), attributed
//     from the message's authenticated user-id or a CloudEvents binary binding. It is
//     a streaming source: Gather blocks receiving until the engine cancels it.
//   - Output ("olivares.amqp-egress") — sends evidence/findings as CloudEvents 1.0.2
// to an address, structured or binary content mode.
//
// NON-DESTRUCTIVE OBSERVATION (docs/SECURITY-HARDENING.md). Consuming an AMQP message SETTLES it,
// which on a normal queue REMOVES it — an observer that read the application's own
// production queue would steal its traffic. So this connector attaches to a
// DEDICATED OBSERVATION ADDRESS that mirrors (tees) the traffic, never the app's
// working queue. Operationally that is, for example:
//
//   - RabbitMQ 4.0: bind an extra observation queue to the same exchange/topic
//     (a fan-out copy), or enable the firehose tracer, and point
//     observation_address at that copy.
//   - Azure Service Bus: read from a topic SUBSCRIPTION dedicated to observation (a
//     subscription is an independent fan-out copy of the topic), never the
//     application's processing queue/subscription.
//
// Settling (accepting) a message on that dedicated observation queue is correct and
// expected — it acknowledges the OBSERVATION copy and never drains the application's
// queue. The connector accepts each message after emitting its edges so the
// observation queue does not grow unbounded.
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md). A broker connector OBSERVES; it does not interpose on
// the customer's data path (§0). It is minimal-data (§3): from a message it emits
// only topology and activity EDGES — namespace→address and producer→address — derived
// from the standard Properties (to/subject/message-id/user-id/group-id) and the
// application-properties (where a CloudEvents binary binding rides). It NEVER reads a
// message's body/Data/Value: the connector-neutral Message carried across the seam
// has no body field at all, so a payload cannot leak into an edge by construction.
// SASL PLAIN/ANONYMOUS and TLS/mTLS are the secure default (§4); the broker
// credential lives in memory only and is never logged or emitted (the namespace label
// is derived from the endpoint with userinfo stripped via redact.SanitizeURL). The
// connector imports only the SDK and the shared Apache helpers — never the engine
// (LICENSING.md).
package amqp

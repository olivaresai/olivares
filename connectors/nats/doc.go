// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package nats is the Olivares AI connector that OBSERVES a customer's NATS
// JetStream event surface. It is a streaming SOURCE: Gather dials the
// cluster, attaches to a dedicated durable PULL consumer, and blocks observing the
// stream's traffic — emitting minimal-data topology and activity EDGES — until the
// engine cancels it.
//
// HAND-ROLLED, STDLIB-ONLY WIRE. The NATS client is implemented directly over
// net.Conn / tls.Conn from the standard library (no third-party nats.go). The NATS
// core protocol is a small, line-oriented text protocol (every control line ends in
// CRLF): the server sends INFO/MSG/HMSG/PING/PONG/+OK/-ERR; the client sends
// CONNECT/SUB/UNSUB/PUB/HPUB/PING/PONG. JetStream is layered on top as request/reply
// over core subjects ($JS.API.*). A durable PULL consumer is driven by publishing a
// next-batch request to '$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>' with a
// reply-to inbox the client SUBscribed first; the stream's messages then arrive as
// MSG/HMSG frames to that inbox. The wire framing PARSER is pure and unit-tested
// offline, which proves the protocol is real rather than mocked at a high level.
//
// NON-DESTRUCTIVE OBSERVATION (docs/SECURITY-HARDENING.md). A JetStream PULL consumer with an
// explicit-ack policy does NOT remove a message from the stream — the stream retains
// it under its own retention policy; only the per-consumer delivery cursor advances.
// The connector attaches to its OWN dedicated durable consumer (configured by name),
// independent of the application's consumers, so observing the stream never competes
// with or starves the customer's real workload, and never deletes a message the
// application still needs. The connector observes the flow; it does not interpose on
// the customer's data path.
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md). Minimal-data (§3): from a JetStream message the
// connector emits ONLY topology/identifier EDGES —
//
//	stream → subject              (the stream carries traffic on this subject; topology)
//	consumer → stream             (the durable consumer reads the stream; a READ touch)
//	producer (CloudEvents source) → subject  (who wrote, when the message is a CloudEvent)
//	subject-token parents → subject          (the dotted subject hierarchy, topology)
//
// and NEVER the message payload, the sensor values, or any header value other than
// the CloudEvents context attributes (source/type) used to attribute a producer. The
// message Data bytes are read ONLY to recognize a structured CloudEvents JSON
// document; they are never emitted or persisted. Credentials (user/password, token)
// are supplied as Secret config fields, held in memory only, never logged or emitted
// (§4). TLS/mTLS is the secure default for any 'natss://' deployment. The connector
// imports only the SDK and the shared messaging helpers — never the engine
// (LICENSING.md).
package nats

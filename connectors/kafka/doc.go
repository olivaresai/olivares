// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kafka is the Olivares AI connector that OBSERVES an enterprise's Apache
// Kafka event surface. It is the amplitude moat in one package: because
// Apache Kafka 4.0 (KRaft), Confluent Platform/Cloud, Redpanda, Amazon MSK and Azure
// Event Hubs (Kafka endpoint) all speak the SAME wire protocol, a single
// wire-protocol client reaches ALL FIVE — five integration targets, one connector.
// The client is the pure-Go franz-go library (BSD-3), kept behind a narrow seam so
// the connector's observation logic runs offline in CI.
//
// It ships two components:
//
//   - Source ("olivares.kafka") — a consumer-group member that observes the event
//     flow: which topics carry traffic, which consumer groups read them (topology),
//     who produces (a CloudEvents source, when present) and which data contract a
//     topic carries (via a read-only Schema Registry lookup). It is a
//     streaming source: Gather blocks consuming until the engine cancels it.
//   - Output ("olivares.kafka-egress") — produces evidence/findings as CloudEvents
//     1.0.2 to a topic, structured or binary content mode.
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md). A broker connector OBSERVES; it does not interpose on
// the customer's data path (§0). It is minimal-data (§3): from a message it emits
// only topology and activity EDGES — cluster→topic, group→topic, producer→topic,
// topic→contract — and NEVER a record's key or value content. The Schema Registry
// is read read-only to learn a contract's STRUCTURE (subject, record/field names),
// not to decode a payload. SASL (PLAIN/SCRAM) and TLS/mTLS are the secure default
// (§4); the broker/registry credentials live in memory only and are never logged or
// emitted. The connector imports only the SDK and the shared Apache helpers — never
// the engine (LICENSING.md).
//
// One connector kind, five targets:
//
//	bootstrap                          example brokers config
//	Apache Kafka 4.0 (KRaft)           kafka-1:9092,kafka-2:9092      (SASL/SCRAM + TLS)
//	Confluent Cloud                    pkc-xxxx.region.aws.confluent.cloud:9092 (SASL/PLAIN + TLS, + Schema Registry)
//	Redpanda                           redpanda:9092                  (SASL/SCRAM + TLS)
//	Amazon MSK                         b-1.msk.region.amazonaws.com:9098 (IAM not covered; SASL/SCRAM + TLS)
//	Azure Event Hubs (Kafka endpoint)  ns.servicebus.windows.net:9093 (SASL/PLAIN user=$ConnectionString + TLS)
package kafka

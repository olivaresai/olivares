// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mqtt is the Olivares AI connector that OBSERVES a customer's MQTT 5.0 /
// Sparkplug B event surface — the OT/IoT edge. It is a streaming
// SOURCE: Gather dials the broker, SUBSCRIBEs to the configured observation topic
// filters, and blocks reading PUBLISH packets — emitting minimal-data topology and
// activity EDGES — until the engine cancels it. It ships ONE component, the Source
// "olivares.mqtt".
//
// THE MOAT — one protocol, N targets. MQTT 5.0 is the lingua franca of industrial
// IoT: a single wire reaches every compliant broker (Eclipse Mosquitto, HiveMQ,
// EMQX, AWS IoT Core, Azure IoT Hub's MQTT endpoint, VerneMQ…), and the Sparkplug B
// payload-and-topic convention (Eclipse Tahu / Sparkplug 3.0) standardizes the
// TOPOLOGY on top of it. So one connector observes the device fleet of an entire
// plant floor regardless of broker vendor.
//
// HAND-ROLLED, STDLIB-ONLY WIRE. The MQTT 5 client is implemented directly over
// net.Conn / tls.Conn from the standard library (no third-party paho client). MQTT
// is a compact binary protocol: a fixed header of one byte (packet type<<4 | flags)
// plus a Variable Byte Integer remaining-length, then a variable header and payload.
// This package implements the VBI codec, the UTF-8 string framing, the CONNECT/
// SUBSCRIBE builders and the PUBLISH parser (topic + MQTT 5 User Properties +
// Content Type) by hand; the framing primitives are pure and unit-tested offline,
// which proves the protocol is real rather than mocked at a high level. The control
// packets used are CONNECT(1)/CONNACK(2)/SUBSCRIBE(8)/SUBACK(9)/PUBLISH(3)/
// PINGREQ(12)/PINGRESP(13)/DISCONNECT(14).
//
// SPARKPLUG B TOPOLOGY FROM THE TOPIC. A Sparkplug topic is
// 'spBv1.0/<group_id>/<message_type>/<edge_node_id>[/<device_id>]'. The message
// types (verbs) are NBIRTH/NDEATH/NDATA/NCMD (node-level, NO device segment) and
// DBIRTH/DDEATH/DDATA/DCMD (device-level, device segment present). STATE is the
// special host-application certificate 'spBv1.0/<group_id>/STATE/<host_id>', handled
// explicitly — not as a normal verb. The connector decodes ONLY the topic string
// into a group→edge_node→device→broker topology; it NEVER decodes the protobuf
// payload, which is raw sensor telemetry forbidden by minimal-data (docs/SECURITY-HARDENING.md). For
// a generic (non-spBv1.0) topic it emits broker→topic activity and, when the message
// carries a CloudEvent in MQTT 5 binary mode (User Properties named by the bare
// attribute name — MQTTPrefix=""), a producer→topic WRITE attributed to the
// CloudEvents source.
//
// EDGES (minimal-data, topology from the TOPIC namespace):
//
//	Sparkplug:  group → edge_node                 (the node belongs to the group)
//	            edge_node → device                (the device hangs off the node; D* verbs)
//	            edge_node|device → broker         (the node/device reaches the app via the broker)
//	            (STATE: edge_node[host app] → broker, its presence certificate)
//	Generic:    broker → topic                    (live traffic on the topic)
//	            producer(CloudEvents source) → topic (WRITE, when the message is a CloudEvent)
//
// The Sparkplug verb (NBIRTH/DDATA/…) and the CloudEvents type ride as the edge's
// ToolRef (operation label); no payload, sensor value or User-Property value other
// than the CloudEvents context attributes is ever read or emitted.
//
// NON-DESTRUCTIVE OBSERVATION (docs/SECURITY-HARDENING.md). An MQTT SUBSCRIBE is pub/sub fan-out: the
// broker delivers a COPY of each matching PUBLISH to every subscriber independently.
// The connector receiving a copy does NOT consume, ack-remove or otherwise disturb
// the message for the customer's real subscribers — unlike a queue GET, a topic
// subscription naturally observes without interposing on the data path. The
// connector observes the flow; it does not sit in it.
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md). Minimal-data (§3): from a PUBLISH the connector emits
// only the topology/identifier EDGES above and NEVER the payload, the Sparkplug
// protobuf, or any User-Property value other than the CloudEvents context attributes
// used to attribute a producer. Credentials (username/password) are supplied as
// Secret config fields, held in memory only, never logged or emitted (§4); the
// shared redact pass scrubs every emitted reference so a secret accidentally
// embedded in a client id or topic name is neutralized before it reaches the bus.
// TLS/mTLS is the secure default (a tls:// broker, or tls=true, with an optional
// client certificate+key) — mutual TLS is the expected posture for an OT/IoT broker.
// The connector imports only the SDK and the shared messaging helpers — never the
// engine (LICENSING.md).
package mqtt

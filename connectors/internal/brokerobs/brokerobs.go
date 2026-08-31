// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package brokerobs is the minimal-data observation helper shared by the Olivares
// AI messaging/eventing connectors (Kafka, AMQP, NATS, MQTT, cloud queues,
// Debezium —). A broker connector OBSERVES the flow of events through a
// broker; it never interposes on the data path (docs/SECURITY-HARDENING.md) and it never emits
// the raw body of a message (docs/SECURITY-HARDENING.md). From a message it derives only
// topology and activity EDGES: who (a producer/consumer/client/device/identity)
// touched what (a topic/queue/subject/stream/exchange), with the read/write
// classification of that touch. The body, the keys, the payload — none of it is
// read for content or persisted.
//
// This package centralizes three things every messaging connector needs, so the
// minimal-data and provenance rules are applied once, not re-implemented per
// connector:
//
//   - The SignalSource vocabulary for the messaging family (open strings, S02 §6).
//   - A single Edge builder that scrubs every natural reference through
//     connectors/internal/redact before it becomes an EdgeObservation, so a topic
//     or client id that accidentally embeds a secret shape can never leak.
//   - The OTel messaging-semconv instrumentation GATE: default OFF,
//     and deliberately NOT pinned to a semconv schema version because the
//     messaging semantic conventions are in Development, not Stable, as of 2026
//. It is an attribute carrier only — there is NO OTLP emitter
//     here (that is).
//
// It imports only the SDK and the shared Apache redact helper — never the engine.
package brokerobs

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// The SignalSource values the messaging/eventing connectors introduce. They are
// open strings on the wire (S02 §6), so a connector adds its own provenance
// without an SDK release; declaring them here keeps the family consistent and lets
// a consumer (the inventory/R-RW/security modules) distinguish a Kafka edge from a
// NATS edge by provenance, exactly as distinguishes runtime from cloudtrail.
const (
	// SignalKafka is an Apache-Kafka-wire observation (also Event Hubs/Redpanda/MSK).
	SignalKafka model.SignalSource = "kafka"
	// SignalAMQP is an AMQP 1.0 observation (RabbitMQ 4.0 / Azure Service Bus).
	SignalAMQP model.SignalSource = "amqp"
	// SignalNATS is a NATS/JetStream observation.
	SignalNATS model.SignalSource = "nats"
	// SignalMQTT is an MQTT 5.0 / Sparkplug B observation (OT/IoT).
	SignalMQTT model.SignalSource = "mqtt"
	// SignalSQS / SignalSNS / SignalEventBridge are AWS managed-bus observations.
	SignalSQS         model.SignalSource = "sqs"
	SignalSNS         model.SignalSource = "sns"
	SignalEventBridge model.SignalSource = "eventbridge"
	// SignalPubSub is a Google Cloud Pub/Sub observation.
	SignalPubSub model.SignalSource = "pubsub"
	// SignalDebezium is a Debezium CDC streaming observation (distinct from the
	// static datastore audit of see the contract frontier).
	SignalDebezium model.SignalSource = "debezium"
)

// Observation is the connector-neutral description of one broker edge before it is
// scrubbed and lifted to a model.EdgeObservation. A connector fills the natural
// references it observed; Edge() applies the minimal-data redaction pass. The
// access Mode is the touch classification: a consumer reading a destination is
// ModeRead, a producer writing one is ModeWrite, and a pure binding/topology edge
// (a device bound to a broker, a queue bound to an exchange) is ModeUnknown — the
// contention-as-edge precedent of never a guessed read/write.
type Observation struct {
	OriginKind   string
	OriginRef    string
	ResourceKind string
	ResourceRef  string
	Mode         model.AccessMode
	Confidence   model.Confidence
	ToolRef      string
	ObservedAt   time.Time
}

// Edge builds the minimal-data EdgeObservation for source, scrubbing every natural
// reference (origin, resource, tool) through redact.Clean so a reference that
// embeds a secret shape (a connection string smuggled into a client id, a token in
// a topic name) is neutralized before it ever reaches the bus. redact.Clean only
// removes recognized secret shapes; an ordinary topic/queue/subject name passes
// through unchanged, so the edge stays useful while never under-redacting (the
// redact package contract). The caller supplies the SignalSource so a connector
// keeps its own provenance.
func (o Observation) Edge(source model.SignalSource) model.EdgeObservation {
	mode := o.Mode
	if mode == "" {
		mode = model.ModeUnknown
	}
	conf := o.Confidence
	if conf == "" {
		// Never fabricate certainty: an unspecified confidence is approximate, not
		// attributed (ARCHITECTURE.md).
		conf = model.ConfidenceApproximate
	}
	return model.EdgeObservation{
		OriginKind:   o.OriginKind,
		OriginRef:    redact.Clean(o.OriginRef),
		ResourceKind: o.ResourceKind,
		ResourceRef:  redact.Clean(o.ResourceRef),
		Mode:         mode,
		Source:       source,
		Confidence:   conf,
		ToolRef:      redact.Clean(o.ToolRef),
		ObservedAt:   o.ObservedAt,
	}
}

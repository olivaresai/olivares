// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk/model"
)

// The entity classes this connector materializes in the consumer (pattern:
// entities are derived from edge references, not emitted as a separate kind).
const (
	kindNamespace = "amqp.namespace" // the broker/namespace (RabbitMQ vhost / Service Bus namespace)
	kindAddress   = "amqp.address"   // a queue / topic / subscription node address
	kindIdentity  = "identity"       // a producing identity (UserID or a CloudEvents source)
)

// observer turns observed AMQP messages into minimal-data edges. It holds the
// namespace label only; it NEVER reads a message's body (the neutral Message struct
// carries no body field — see seam.go). From the framing metadata it derives the
// destination topology and, when attributable, the producing identity (docs/SECURITY-HARDENING.md).
type observer struct {
	namespaceRef string
}

// observeMessage turns one observed message into edges, from its metadata ONLY:
//
//   - namespace→address (topology): the broker carries traffic on this destination.
//     The destination is the message's To (Properties.to) when present, else the
//     observation address the receiver attached to (supplied by the caller). Mode is
//     unknown — a topology/binding edge, never a guessed read/write (the
//     contention-as-edge precedent).
//   - producer→address (write): the producing identity, attributed in priority order
//     from (1) a CloudEvents binary binding's source in application-properties
//     (cloudEvents_source), else (2) Properties.user-id. A self-declared source is
//     approximate, never fabricated certainty (ARCHITECTURE.md). NEVER the body.
//
// observedAddress is the address the receiver is attached to (the dedicated
// observation queue/subscription); it is the fallback destination and the stable
// node identity for the topology edge.
func (o *observer) observeMessage(msg Message, observedAddress string, at time.Time) []model.EdgeObservation {
	dest := msg.To
	if dest == "" {
		dest = observedAddress
	}

	out := []model.EdgeObservation{
		brokerobs.Observation{
			OriginKind: kindNamespace, OriginRef: o.namespaceRef,
			ResourceKind: kindAddress, ResourceRef: dest,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalAMQP),
	}

	producer, toolRef, conf := o.attributeProducer(msg)
	if producer != "" {
		out = append(out, brokerobs.Observation{
			OriginKind: kindIdentity, OriginRef: producer,
			ResourceKind: kindAddress, ResourceRef: dest,
			Mode: model.ModeWrite, Confidence: conf,
			ToolRef:    toolRef,
			ObservedAt: at,
		}.Edge(brokerobs.SignalAMQP))
	}
	return out
}

// attributeProducer resolves the producing identity for a write edge, with its
// attribution confidence and an optional tool ref. A CloudEvents binary binding in
// the application-properties is preferred (it names the producing context and event
// type explicitly); the AMQP-stamped user-id is the fallback. The body is never read
// — only the cloudEvents_-prefixed application-properties and the user-id property.
func (o *observer) attributeProducer(msg Message) (ref, toolRef string, conf model.Confidence) {
	if ev, ok := recognizeCloudEvent(msg); ok && ev.Source != "" {
		// A CloudEvents source is self-declared by the producer, so it is an
		// approximate attribution (ARCHITECTURE.md).
		return ev.Source, ev.Type, model.ConfidenceApproximate
	}
	if msg.UserID != "" {
		// The broker authenticated and stamped the user-id, so it is a firm
		// attribution of who produced onto this destination.
		return msg.UserID, "", model.ConfidenceAttributed
	}
	return "", "", ""
}

// recognizeCloudEvent detects a CloudEvent carried on an AMQP message in the BINARY
// binding (cloudEvents_-prefixed application-properties) and returns its context
// attributes (source/type). It reads ONLY the application-properties — the body is
// never touched (a structured-mode CloudEvent rides in the data section, which a
// minimal-data observer does not parse, so it is intentionally not recognized here).
func recognizeCloudEvent(msg Message) (cloudevents.Event, bool) {
	if len(msg.AppProps) == 0 {
		return cloudevents.Event{}, false
	}
	_, hasID := msg.AppProps[cloudevents.AMQPPrefix+"id"]
	_, hasSpec := msg.AppProps[cloudevents.AMQPPrefix+"specversion"]
	if !hasID && !hasSpec {
		return cloudevents.Event{}, false
	}
	ev, err := cloudevents.FromBinary(cloudevents.AMQPPrefix, msg.AppProps, "", nil)
	if err != nil {
		return cloudevents.Event{}, false
	}
	return ev, true
}

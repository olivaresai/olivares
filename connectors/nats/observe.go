// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bytes"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk/model"
)

// The entity classes this connector materializes in the consumer (pattern:
// entities are derived from edge references, never emitted as a separate kind).
const (
	kindStream   = "nats.stream"
	kindSubject  = "nats.subject"
	kindConsumer = "nats.consumer"
)

// observer turns observed JetStream messages into minimal-data edges. It holds the
// stream/consumer labels and NEVER emits a message's payload — only the
// subject/producer identifiers it derives from framing and CloudEvents context
// attributes (docs/SECURITY-HARDENING.md).
type observer struct {
	streamRef   string
	consumerRef string
}

// observeAttach emits the one-shot topology edge that the dedicated durable consumer
// reads the stream (consumer → stream, a READ touch). It is emitted once at Gather
// start so the read relationship is recorded even on an idle stream.
func (o *observer) observeAttach(at time.Time) model.EdgeObservation {
	return brokerobs.Observation{
		OriginKind: kindConsumer, OriginRef: o.consumerRef,
		ResourceKind: kindStream, ResourceRef: o.streamRef,
		Mode: model.ModeRead, Confidence: model.ConfidenceAttributed,
		ObservedAt: at,
	}.Edge(brokerobs.SignalNATS)
}

// observeMsg turns one JetStream message into edges. It always records that the
// stream carries traffic on the message's subject (stream → subject, topology) plus
// the dotted subject token hierarchy (parent-subject → subject, topology). When the
// message is a CloudEvent it adds the producing context (source → subject, a WRITE)
// read from the envelope's identifiers — never its data. A status/control frame
// carries no observable topology and yields no edges.
func (o *observer) observeMsg(m Msg, at time.Time) []model.EdgeObservation {
	if m.Subject == "" || m.Status != "" {
		return nil
	}
	out := []model.EdgeObservation{
		brokerobs.Observation{
			OriginKind: kindStream, OriginRef: o.streamRef,
			ResourceKind: kindSubject, ResourceRef: m.Subject,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalNATS),
	}

	// Subject token hierarchy: orders.events.created → orders.events → orders. Each
	// parent prefix contains the child (topology, ModeUnknown).
	for _, parent := range subjectParents(m.Subject) {
		out = append(out, brokerobs.Observation{
			OriginKind: kindSubject, OriginRef: parent,
			ResourceKind: kindSubject, ResourceRef: m.Subject,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalNATS))
	}

	if ev, ok := recognizeCloudEvent(m); ok && ev.Source != "" {
		out = append(out, brokerobs.Observation{
			OriginKind: "identity", OriginRef: ev.Source,
			ResourceKind: kindSubject, ResourceRef: m.Subject,
			// A CloudEvents source is self-declared by the producer, so it is an
			// approximate attribution, never fabricated certainty (ARCHITECTURE.md).
			Mode: model.ModeWrite, Confidence: model.ConfidenceApproximate,
			ToolRef:    ev.Type,
			ObservedAt: at,
		}.Edge(brokerobs.SignalNATS))
	}
	return out
}

// subjectParents returns the ancestor prefixes of a dotted NATS subject, longest
// first, excluding the subject itself. "a.b.c" → ["a.b", "a"]. A token-less subject
// has no parents.
func subjectParents(subject string) []string {
	parts := strings.Split(subject, ".")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := len(parts) - 1; i > 0; i-- {
		out = append(out, strings.Join(parts[:i], "."))
	}
	return out
}

// recognizeCloudEvent detects a CloudEvent carried on a JetStream message and returns
// its context attributes (source/type), in either binding:
//   - BINARY (preferred, minimal-data): NATS headers carry the bare CloudEvents
//     attribute names (MQTTPrefix == ""), so the body is never touched.
//   - STRUCTURED: an application/cloudevents+json payload; it is parsed transiently to
//     recover the identifiers and the data member is discarded, never emitted.
func recognizeCloudEvent(m Msg) (cloudevents.Event, bool) {
	if len(m.Header) > 0 {
		_, hasID := m.Header["id"]
		_, hasSpec := m.Header["specversion"]
		if hasID || hasSpec {
			// With the empty MQTT prefix every header would be read as a CloudEvents
			// attribute; per the binding, datacontenttype rides the transport's own
			// content-type field, not a User Property, so it is split out and passed as
			// the contentType arg (passing it as an attribute would collide with the
			// reserved name). Data is nil — the body is never read in binary mode.
			attrs, contentType := splitContentType(m.Header)
			if ev, err := cloudevents.FromBinary(cloudevents.MQTTPrefix, attrs, contentType, nil); err == nil {
				return ev, true
			}
		}
	}
	if looksStructuredCE(m.Data) {
		if ev, err := cloudevents.Parse(m.Data); err == nil {
			return ev, true
		}
	}
	return cloudevents.Event{}, false
}

// splitContentType separates the transport content-type (carried by a real binary
// CloudEvent on NATS as the message's own content-type, modeled here as a
// "datacontenttype"/"content-type" header) from the CloudEvents context attributes,
// so the reserved datacontenttype name never enters the attribute set.
func splitContentType(h map[string]string) (attrs map[string]string, contentType string) {
	attrs = make(map[string]string, len(h))
	for k, v := range h {
		switch strings.ToLower(k) {
		case "datacontenttype", "content-type":
			contentType = v
		default:
			attrs[k] = v
		}
	}
	return attrs, contentType
}

// looksStructuredCE reports whether the payload is a structured CloudEvent JSON
// document: a JSON object declaring a "specversion" member. It avoids parsing
// arbitrary payloads that are not CloudEvents (and never emits the bytes it inspects).
func looksStructuredCE(data []byte) bool {
	v := bytes.TrimSpace(data)
	return len(v) > 0 && v[0] == '{' && bytes.Contains(v, []byte(`"specversion"`))
}

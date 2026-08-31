// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"bytes"
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/connectors/internal/schemaregistry"
	"github.com/olivaresai/olivares/sdk/model"
)

// resourceTopic / resourceContract / originCluster name the entity classes this
// connector materializes in the consumer (pattern: entities are derived from
// edge references, not emitted as a separate observation kind).
const (
	kindCluster  = "kafka.cluster"
	kindTopic    = "kafka.topic"
	kindGroup    = "kafka.consumer_group"
	kindContract = "schema.subject"
)

// observer turns consumed records and topology snapshots into minimal-data edges.
// It holds the cluster label, an optional Schema Registry client (read-only), and
// the operator-configured GUID header key. It NEVER emits a record's key or value
// content — only the topic/producer/contract identifiers it derives from framing
// and headers (docs/SECURITY-HARDENING.md).
type observer struct {
	clusterRef     string
	sr             *schemaregistry.Client
	srGUIDHeader   string
	resolveTimeout time.Duration
}

// observeTopology lifts a one-shot cluster snapshot into inventory/topology edges:
// the cluster's topics (cluster→topic) and each consumer group's subscriptions
// (cluster→group, group→topic). Group→topic is a READ touch (the group consumes the
// topic); the rest are contention/topology (Mode=unknown), the precedent.
func (o *observer) observeTopology(t kafkawire.Topology, at time.Time) []model.EdgeObservation {
	cluster := t.ClusterRef
	if cluster == "" {
		cluster = o.clusterRef
	}
	var out []model.EdgeObservation
	for _, topic := range t.Topics {
		out = append(out, brokerobs.Observation{
			OriginKind: kindCluster, OriginRef: cluster,
			ResourceKind: kindTopic, ResourceRef: topic,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalKafka))
	}
	for _, g := range t.Groups {
		out = append(out, brokerobs.Observation{
			OriginKind: kindCluster, OriginRef: cluster,
			ResourceKind: kindGroup, ResourceRef: g.Group,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalKafka))
		for _, topic := range g.Topics {
			out = append(out, brokerobs.Observation{
				OriginKind: kindGroup, OriginRef: g.Group,
				ResourceKind: kindTopic, ResourceRef: topic,
				Mode: model.ModeRead, Confidence: model.ConfidenceAttributed,
				ObservedAt: at,
			}.Edge(brokerobs.SignalKafka))
		}
	}
	return out
}

// observeRecord turns one consumed record into edges. It always records that live
// traffic was observed on the topic (cluster→topic). When the record is a
// CloudEvent it adds the producing context (source→topic, a WRITE) read from the
// envelope's identifiers — never its data. When the record is Schema-Registry
// framed it adds the data-contract binding (topic→subject), resolving the subject
// name from the registry when one is configured. ctx bounds an optional registry
// lookup.
func (o *observer) observeRecord(ctx context.Context, rec kafkawire.Record, at time.Time) []model.EdgeObservation {
	topic := rec.Topic
	out := []model.EdgeObservation{
		brokerobs.Observation{
			OriginKind: kindCluster, OriginRef: o.clusterRef,
			ResourceKind: kindTopic, ResourceRef: topic,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ObservedAt: at,
		}.Edge(brokerobs.SignalKafka),
	}

	if ev, ok := recognizeCloudEvent(rec); ok && ev.Source != "" {
		out = append(out, brokerobs.Observation{
			OriginKind: "identity", OriginRef: ev.Source,
			ResourceKind: kindTopic, ResourceRef: topic,
			// A CloudEvents source is self-declared by the producer, so it is an
			// approximate attribution, never fabricated certainty (ARCHITECTURE.md).
			Mode: model.ModeWrite, Confidence: model.ConfidenceApproximate,
			ToolRef:    ev.Type,
			ObservedAt: at,
		}.Edge(brokerobs.SignalKafka))
	}

	if ref, ok := schemaRef(rec, o.srGUIDHeader); ok {
		resourceRef := ref.String()
		toolRef := ""
		if o.sr != nil {
			rctx := ctx
			if o.resolveTimeout > 0 {
				var cancel context.CancelFunc
				rctx, cancel = context.WithTimeout(ctx, o.resolveTimeout)
				defer cancel()
			}
			if sch, err := o.sr.Resolve(rctx, ref); err == nil {
				st := schemaregistry.StructuralRefs(sch)
				if sch.Subject != "" {
					resourceRef = sch.Subject
				}
				toolRef = st.FullName()
			}
		}
		out = append(out, brokerobs.Observation{
			OriginKind: kindTopic, OriginRef: topic,
			ResourceKind: kindContract, ResourceRef: resourceRef,
			Mode: model.ModeUnknown, Confidence: model.ConfidenceAttributed,
			ToolRef:    toolRef,
			ObservedAt: at,
		}.Edge(brokerobs.SignalKafka))
	}
	return out
}

// recognizeCloudEvent detects a CloudEvent carried on a Kafka record and returns
// its context attributes (source/type), in either binding:
//   - BINARY (preferred, minimal-data): ce_-prefixed record headers; the body is
//     never touched, only headers are read.
//   - STRUCTURED: an application/cloudevents+json value; it is parsed transiently to
//     recover the identifiers (source/type) and the data member is discarded, never
//     emitted (same rule as schema-registry structure observation).
func recognizeCloudEvent(rec kafkawire.Record) (cloudevents.Event, bool) {
	if _, ok := rec.Headers[cloudevents.KafkaPrefix+"id"]; ok {
		ev, err := cloudevents.FromBinary(cloudevents.KafkaPrefix, headersToStrings(rec.Headers),
			string(rec.Headers["content-type"]), nil)
		if err == nil {
			return ev, true
		}
	} else if _, ok := rec.Headers[cloudevents.KafkaPrefix+"specversion"]; ok {
		ev, err := cloudevents.FromBinary(cloudevents.KafkaPrefix, headersToStrings(rec.Headers),
			string(rec.Headers["content-type"]), nil)
		if err == nil {
			return ev, true
		}
	}
	if looksStructuredCE(rec) {
		if ev, err := cloudevents.Parse(rec.Value); err == nil {
			return ev, true
		}
	}
	return cloudevents.Event{}, false
}

// looksStructuredCE reports whether the record value is a structured CloudEvent: the
// content-type header says so, or the JSON value declares a "specversion" member. It
// avoids parsing arbitrary JSON values that are not CloudEvents.
func looksStructuredCE(rec kafkawire.Record) bool {
	if ct := string(rec.Headers["content-type"]); len(ct) >= len("application/cloudevents") &&
		bytes.HasPrefix([]byte(ct), []byte("application/cloudevents")) {
		return true
	}
	v := bytes.TrimSpace(rec.Value)
	return len(v) > 0 && v[0] == '{' && bytes.Contains(v, []byte(`"specversion"`))
}

// schemaRef extracts the schema reference a record declares: a header GUID first
// (header-GUID wire format), else the classic value prefix.
func schemaRef(rec kafkawire.Record, guidHeader string) (schemaregistry.Reference, bool) {
	if ref, ok := schemaregistry.GUIDFromHeaders(rec.Headers, guidHeader); ok {
		return ref, true
	}
	if ref, _, err := schemaregistry.ParseConfluentValue(rec.Value); err == nil {
		return ref, true
	}
	return schemaregistry.Reference{}, false
}

// headersToStrings adapts byte-valued Kafka headers to the string map the
// CloudEvents binding parser consumes.
func headersToStrings(h map[string][]byte) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = string(v)
	}
	return out
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk/model"
)

// The entity classes this connector materializes from a topic. As in the Kafka
// observer (pattern), entities are derived from edge references, not emitted as
// a separate observation kind. Sparkplug topology lives under the sparkplug.*
// namespace; generic MQTT under mqtt.*.
const (
	kindBroker   = "mqtt.broker"         // the broker the device/app connects through
	kindTopic    = "mqtt.topic"          // a generic (non-Sparkplug) MQTT topic
	kindGroup    = "sparkplug.group"     // a Sparkplug B group_id
	kindEdgeNode = "sparkplug.edge_node" // a Sparkplug B edge node (edge_node_id)
	kindDevice   = "sparkplug.device"    // a Sparkplug B device (device_id, D* verbs only)
)

// sparkplugNamespace is the fixed Sparkplug B topic namespace root (Eclipse spec
// 3.0): every Sparkplug topic begins 'spBv1.0/'.
const sparkplugNamespace = "spBv1.0"

// sparkVerbs is the closed set of Sparkplug B message types (verbs). STATE is NOT
// in this set: it is a special host-application birth/death certificate with a
// different topic shape and is handled explicitly in parseSparkplug.
var sparkVerbs = map[string]bool{
	"NBIRTH": true, "NDEATH": true, "NDATA": true, "NCMD": true,
	"DBIRTH": true, "DDEATH": true, "DDATA": true, "DCMD": true,
}

// deviceVerbs is the subset of verbs whose topic carries a device_id (the D*
// verbs). For an N* verb the device segment is ABSENT (Sparkplug 3.0 §6).
var deviceVerbs = map[string]bool{
	"DBIRTH": true, "DDEATH": true, "DDATA": true, "DCMD": true,
}

// sparkTopic is the decoded topology of a Sparkplug B topic. It carries ONLY the
// identifier segments from the topic string — never the protobuf payload, which is
// raw sensor telemetry forbidden by minimal-data (docs/SECURITY-HARDENING.md). isState marks the
// special 'spBv1.0/<group>/STATE/<host>' certificate form.
type sparkTopic struct {
	group    string
	verb     string
	edgeNode string
	device   string // empty for N* verbs and for STATE
	isState  bool
}

// parseSparkplug decodes a Sparkplug B topic into its topology segments, reporting
// ok=false for anything that is not a well-formed Sparkplug topic. The layout is:
//
//	spBv1.0/<group_id>/<message_type>/<edge_node_id>[/<device_id>]
//
// with device_id present ONLY for the D* verbs. STATE is special — the Sparkplug
// 3.0 host-application certificate 'spBv1.0/<group_id>/STATE/<edge_node_id>' (the
// trailing segment is the host application / scada id, not a normal edge node) — so
// it is recognized explicitly and never run through the verb table. The topic
// segments are identifiers, not payload; the protobuf body is never decoded.
func parseSparkplug(topic string) (sparkTopic, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 4 || parts[0] != sparkplugNamespace {
		return sparkTopic{}, false
	}
	group := parts[1]
	verb := parts[2]
	if group == "" || verb == "" {
		return sparkTopic{}, false
	}

	if verb == "STATE" {
		// spBv1.0/<group_id>/STATE/<host_application_id>. Exactly four segments; the
		// last names the host application whose online/offline certificate this is.
		if len(parts) != 4 || parts[3] == "" {
			return sparkTopic{}, false
		}
		return sparkTopic{group: group, verb: "STATE", edgeNode: parts[3], isState: true}, true
	}

	if !sparkVerbs[verb] {
		return sparkTopic{}, false
	}
	edgeNode := parts[3]
	if edgeNode == "" {
		return sparkTopic{}, false
	}
	st := sparkTopic{group: group, verb: verb, edgeNode: edgeNode}
	if deviceVerbs[verb] {
		// A D* verb MUST carry a device segment.
		if len(parts) < 5 || parts[4] == "" {
			return sparkTopic{}, false
		}
		st.device = parts[4]
	}
	return st, true
}

// observer turns observed PUBLISH messages into minimal-data edges. It holds the
// broker label only; it never reads or emits a message payload (docs/SECURITY-HARDENING.md).
type observer struct {
	brokerRef string
}

// observePublish turns one observed PUBLISH into topology/activity edges. It NEVER
// reads pub.Payload — only the topic namespace and the MQTT 5 user properties (to
// recognize a CloudEvent and attribute a producer). For a Sparkplug B topic it
// emits the device→edge_node→group→broker topology; for a generic topic it emits
// the broker→topic activity and, when the message is a CloudEvent, the producer
// write edge. The payload (Sparkplug protobuf telemetry, or any application body)
// is never decoded.
func (o *observer) observePublish(pub Publish, at time.Time) []model.EdgeObservation {
	if st, ok := parseSparkplug(pub.Topic); ok {
		return o.sparkplugEdges(st, at)
	}
	return o.genericEdges(pub, at)
}

// sparkplugEdges lifts a decoded Sparkplug topic into the device→broker→app
// topology. The hierarchy is identifier-only:
//
//	group → edge_node               (the edge node belongs to the group; topology)
//	edge_node → device              (the device hangs off the edge node; D* verbs only)
//	edge_node|device → broker       (the device/node reaches the app surface via the broker)
//
// All edges are Mode=unknown (a BIRTH/DATA topic announces topology, not a read or
// write of a resource — the contention-as-edge precedent), attributed to the
// node/device identity the topic names. STATE attributes the host application's
// edge_node segment to the broker (its online/offline certificate is a topology
// signal). The protobuf payload is never decoded.
func (o *observer) sparkplugEdges(st sparkTopic, at time.Time) []model.EdgeObservation {
	if st.isState {
		// Host-application certificate: the named host application is bound to the
		// broker (it announces presence over the broker). group→host is topology too.
		return []model.EdgeObservation{
			obs(kindGroup, st.group, kindEdgeNode, st.edgeNode, model.ModeUnknown, "STATE", at),
			obs(kindEdgeNode, st.edgeNode, kindBroker, o.brokerRef, model.ModeUnknown, "STATE", at),
		}
	}

	out := []model.EdgeObservation{
		// group → edge_node: the edge node is a member of the Sparkplug group.
		obs(kindGroup, st.group, kindEdgeNode, st.edgeNode, model.ModeUnknown, st.verb, at),
	}
	if st.device != "" {
		// edge_node → device: the device hangs off this edge node (D* verbs only).
		out = append(out, obs(kindEdgeNode, st.edgeNode, kindDevice, st.device, model.ModeUnknown, st.verb, at))
		// device → broker: the device reaches the app surface through the broker.
		out = append(out, obs(kindDevice, st.device, kindBroker, o.brokerRef, model.ModeUnknown, st.verb, at))
	} else {
		// edge_node → broker: the edge node reaches the app surface through the broker.
		out = append(out, obs(kindEdgeNode, st.edgeNode, kindBroker, o.brokerRef, model.ModeUnknown, st.verb, at))
	}
	return out
}

// genericEdges handles a non-Sparkplug MQTT topic. It always records that live
// traffic was observed on the topic (broker→topic, topology). When the message
// carries a CloudEvent in MQTT 5 binary mode (User Properties named by the bare
// attribute name — MQTTPrefix=""), it adds the producing context (source→topic, a
// WRITE) read from the envelope's identifiers — never its payload. The payload is
// never read for content.
func (o *observer) genericEdges(pub Publish, at time.Time) []model.EdgeObservation {
	out := []model.EdgeObservation{
		// broker → topic: live traffic on this topic (topology).
		obs(kindBroker, o.brokerRef, kindTopic, pub.Topic, model.ModeUnknown, "", at),
	}
	if ev, ok := recognizeCloudEvent(pub); ok && ev.Source != "" {
		out = append(out, brokerobs.Observation{
			OriginKind: "identity", OriginRef: ev.Source,
			ResourceKind: kindTopic, ResourceRef: pub.Topic,
			// A CloudEvents source is self-declared by the producer, so it is an
			// approximate attribution, never fabricated certainty (ARCHITECTURE.md).
			Mode: model.ModeWrite, Confidence: model.ConfidenceApproximate,
			ToolRef:    ev.Type,
			ObservedAt: at,
		}.Edge(brokerobs.SignalMQTT))
	}
	return out
}

// recognizeCloudEvent detects a CloudEvent carried on an MQTT 5 PUBLISH in BINARY
// content mode: the context attributes ride as User Properties named by the bare
// attribute name (MQTTPrefix=""), and the body is the data — which is never read.
// Because the MQTT prefix is empty, the User Property map is only treated as a
// CloudEvent when it actually declares the CloudEvents identifying attributes
// (id+source+type, the required set FromBinary validates); an ordinary message's
// user properties never accidentally parse as an event.
func recognizeCloudEvent(pub Publish) (cloudevents.Event, bool) {
	if len(pub.UserProps) == 0 {
		return cloudevents.Event{}, false
	}
	// Guard: only attempt recognition when the minimal CloudEvents context
	// attributes are present as user properties. FromBinary then validates the
	// required set; a non-CloudEvent message is rejected, not guessed.
	if _, hasID := pub.UserProps["id"]; !hasID {
		return cloudevents.Event{}, false
	}
	if _, hasSource := pub.UserProps["source"]; !hasSource {
		return cloudevents.Event{}, false
	}
	ev, err := cloudevents.FromBinary(cloudevents.MQTTPrefix, pub.UserProps, pub.ContentType, nil)
	if err != nil {
		return cloudevents.Event{}, false
	}
	return ev, true
}

// obs is the local edge builder: it routes every edge through brokerobs.Observation
// .Edge so the minimal-data redaction pass runs on every reference and the MQTT
// signal source is stamped. toolRef carries the Sparkplug verb (or CloudEvents type)
// as the operation label; an empty toolRef is omitted by the builder's redaction.
func obs(originKind, originRef, resKind, resRef string, mode model.AccessMode, toolRef string, at time.Time) model.EdgeObservation {
	return brokerobs.Observation{
		OriginKind: originKind, OriginRef: originRef,
		ResourceKind: resKind, ResourceRef: resRef,
		Mode: mode, Confidence: model.ConfidenceAttributed,
		ToolRef:    toolRef,
		ObservedAt: at,
	}.Edge(brokerobs.SignalMQTT)
}

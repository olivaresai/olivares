// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk/model"
)

// edgeKey is the canonical identity of an edge for set-membership assertions.
func edgeKey(e model.EdgeObservation) string {
	return e.OriginKind + "|" + e.OriginRef + "|" + e.ResourceKind + "|" + e.ResourceRef
}

func hasEdge(edges []model.EdgeObservation, key string) bool {
	for _, e := range edges {
		if edgeKey(e) == key {
			return true
		}
	}
	return false
}

// TestParseSparkplugVerbs covers the Sparkplug B topic parser for the N* verbs (no
// device segment), the D* verbs (device segment present), STATE (the special
// host-application certificate), and rejections (non-spBv1.0, missing device on a D*
// verb, unknown verb). It decodes ONLY the topic; no payload is involved.
func TestParseSparkplugVerbs(t *testing.T) {
	cases := []struct {
		name     string
		topic    string
		ok       bool
		group    string
		verb     string
		edgeNode string
		device   string
		isState  bool
	}{
		{"NBIRTH", "spBv1.0/plantA/NBIRTH/edge7", true, "plantA", "NBIRTH", "edge7", "", false},
		{"NDATA", "spBv1.0/plantA/NDATA/edge7", true, "plantA", "NDATA", "edge7", "", false},
		{"DBIRTH", "spBv1.0/plantA/DBIRTH/edge7/press01", true, "plantA", "DBIRTH", "edge7", "press01", false},
		{"DDATA", "spBv1.0/plantA/DDATA/edge7/press01", true, "plantA", "DDATA", "edge7", "press01", false},
		{"STATE", "spBv1.0/plantA/STATE/scada1", true, "plantA", "STATE", "scada1", "", true},
		{"NCMD", "spBv1.0/plantA/NCMD/edge7", true, "plantA", "NCMD", "edge7", "", false},
		// rejections
		{"not-spBv1.0", "factory/edge7/temp", false, "", "", "", "", false},
		{"D-verb-missing-device", "spBv1.0/plantA/DDATA/edge7", false, "", "", "", "", false},
		{"unknown-verb", "spBv1.0/plantA/WAT/edge7", false, "", "", "", "", false},
		{"too-short", "spBv1.0/plantA/NDATA", false, "", "", "", "", false},
		{"state-too-many", "spBv1.0/plantA/STATE/scada1/extra", false, "", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := parseSparkplug(tc.topic)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if st.group != tc.group || st.verb != tc.verb || st.edgeNode != tc.edgeNode ||
				st.device != tc.device || st.isState != tc.isState {
				t.Fatalf("parse = %+v, want group=%q verb=%q edge=%q dev=%q state=%v",
					st, tc.group, tc.verb, tc.edgeNode, tc.device, tc.isState)
			}
		})
	}
}

// TestSparkplugEdgesNodeLevel checks an N* verb yields group→edge_node and
// edge_node→broker (no device), with the verb carried as ToolRef and no payload.
func TestSparkplugEdgesNodeLevel(t *testing.T) {
	o := &observer{brokerRef: "broker-1:8883"}
	edges := o.observePublish(Publish{
		Topic:   "spBv1.0/plantA/NBIRTH/edge7",
		Payload: []byte("PROTOBUF SECRET TELEMETRY"),
	}, time.Now())

	if !hasEdge(edges, "sparkplug.group|plantA|sparkplug.edge_node|edge7") {
		t.Errorf("missing group→edge_node edge: %+v", edges)
	}
	if !hasEdge(edges, "sparkplug.edge_node|edge7|mqtt.broker|broker-1:8883") {
		t.Errorf("missing edge_node→broker edge: %+v", edges)
	}
	for _, e := range edges {
		if e.Source != "mqtt" {
			t.Fatalf("wrong signal source %q", e.Source)
		}
		if e.Mode != model.ModeUnknown {
			t.Fatalf("sparkplug topology edge should be Mode=unknown, got %q", e.Mode)
		}
		if e.ToolRef != "NBIRTH" {
			t.Fatalf("ToolRef should be the verb, got %q", e.ToolRef)
		}
		assertNoPayloadLeak(t, e, "PROTOBUF SECRET TELEMETRY")
	}
}

// TestSparkplugEdgesDeviceLevel checks a D* verb yields the full device topology:
// group→edge_node, edge_node→device, device→broker.
func TestSparkplugEdgesDeviceLevel(t *testing.T) {
	o := &observer{brokerRef: "broker-1:8883"}
	edges := o.observePublish(Publish{
		Topic:   "spBv1.0/plantA/DDATA/edge7/press01",
		Payload: []byte("telemetry"),
	}, time.Now())

	want := []string{
		"sparkplug.group|plantA|sparkplug.edge_node|edge7",
		"sparkplug.edge_node|edge7|sparkplug.device|press01",
		"sparkplug.device|press01|mqtt.broker|broker-1:8883",
	}
	for _, k := range want {
		if !hasEdge(edges, k) {
			t.Errorf("missing edge %s in %+v", k, edges)
		}
	}
}

// TestSparkplugStateEdges checks the STATE certificate maps the host application to
// the broker (and the group to the host), as a topology/presence signal.
func TestSparkplugStateEdges(t *testing.T) {
	o := &observer{brokerRef: "b1"}
	edges := o.observePublish(Publish{Topic: "spBv1.0/plantA/STATE/scada1"}, time.Now())
	if !hasEdge(edges, "sparkplug.group|plantA|sparkplug.edge_node|scada1") {
		t.Errorf("missing group→host edge: %+v", edges)
	}
	if !hasEdge(edges, "sparkplug.edge_node|scada1|mqtt.broker|b1") {
		t.Errorf("missing host→broker edge: %+v", edges)
	}
}

// TestGenericTopicBrokerEdge checks a non-Sparkplug topic yields a broker→topic
// activity edge and nothing payload-derived.
func TestGenericTopicBrokerEdge(t *testing.T) {
	o := &observer{brokerRef: "b1"}
	edges := o.observePublish(Publish{
		Topic:   "factory/line1/temperature",
		Payload: []byte("23.5C raw reading"),
	}, time.Now())
	if !hasEdge(edges, "mqtt.broker|b1|mqtt.topic|factory/line1/temperature") {
		t.Fatalf("missing broker→topic edge: %+v", edges)
	}
	if len(edges) != 1 {
		t.Fatalf("generic non-CE topic should yield exactly the broker→topic edge, got %+v", edges)
	}
	for _, e := range edges {
		assertNoPayloadLeak(t, e, "23.5C raw reading")
	}
}

// TestGenericTopicCloudEventProducer checks a generic MQTT message carrying a
// CloudEvent in binary mode (User Properties = bare attribute names) yields a
// producer→topic WRITE attributed to the CloudEvents source, with approximate
// confidence and the ce type as ToolRef. The body is never read.
func TestGenericTopicCloudEventProducer(t *testing.T) {
	o := &observer{brokerRef: "b1"}
	pub := Publish{
		Topic: "events/orders",
		UserProps: map[string]string{
			"specversion": cloudevents.SpecVersion,
			"id":          "evt-77",
			"source":      "/apps/checkout",
			"type":        "com.acme.OrderCreated",
		},
		ContentType: "application/json",
		Payload:     []byte(`{"orderId":"SECRET-123"}`),
	}
	edges := o.observePublish(pub, time.Now())

	if !hasEdge(edges, "mqtt.broker|b1|mqtt.topic|events/orders") {
		t.Errorf("missing broker→topic edge: %+v", edges)
	}
	var found bool
	for _, e := range edges {
		if e.OriginKind == "identity" && e.OriginRef == "/apps/checkout" {
			found = true
			if e.Mode != model.ModeWrite {
				t.Errorf("producer edge mode = %q, want write", e.Mode)
			}
			if e.Confidence != model.ConfidenceApproximate {
				t.Errorf("producer edge confidence = %q, want approximate", e.Confidence)
			}
			if e.ToolRef != "com.acme.OrderCreated" {
				t.Errorf("producer ToolRef = %q, want the ce type", e.ToolRef)
			}
		}
		assertNoPayloadLeak(t, e, "SECRET-123")
	}
	if !found {
		t.Fatalf("CloudEvents producer write edge missing: %+v", edges)
	}
}

// TestGenericTopicNonCloudEventUserProps checks that ordinary user properties (no
// id/source/type) do NOT get misread as a CloudEvent — only the broker→topic edge
// is emitted, and no User-Property value leaks into an edge.
func TestGenericTopicNonCloudEventUserProps(t *testing.T) {
	o := &observer{brokerRef: "b1"}
	edges := o.observePublish(Publish{
		Topic:     "telemetry/raw",
		UserProps: map[string]string{"deviceSerial": "SN-ABCDEF", "firmware": "1.2.3"},
	}, time.Now())
	if len(edges) != 1 || !hasEdge(edges, "mqtt.broker|b1|mqtt.topic|telemetry/raw") {
		t.Fatalf("non-CE user props should yield only the broker→topic edge, got %+v", edges)
	}
	for _, e := range edges {
		if strings.Contains(edgeKey(e), "SN-ABCDEF") || strings.Contains(e.ToolRef, "SN-ABCDEF") {
			t.Fatalf("user property value leaked into an edge: %+v", e)
		}
	}
}

// TestObserveRedactsSecretInTopic proves the minimal-data redaction pass scrubs a
// secret accidentally embedded in a topic/identifier reference before it becomes an
// edge — here an AWS access key smuggled into a Sparkplug group id.
func TestObserveRedactsSecretInTopic(t *testing.T) {
	o := &observer{brokerRef: "b1"}
	// AKIA-prefixed 20-char access key embedded in the group id.
	edges := o.observePublish(Publish{
		Topic: "spBv1.0/AKIAIOSFODNN7EXAMPLE/NDATA/edge1",
	}, time.Now())
	if len(edges) == 0 {
		t.Fatal("expected edges")
	}
	for _, e := range edges {
		if strings.Contains(e.OriginRef, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(e.ResourceRef, "AKIAIOSFODNN7EXAMPLE") {
			t.Fatalf("secret survived in edge: %+v", e)
		}
	}
}

// assertNoPayloadLeak fails if any part of a forbidden payload string appears in an
// edge's references or tool ref — the core minimal-data guarantee.
func assertNoPayloadLeak(t *testing.T, e model.EdgeObservation, forbidden string) {
	t.Helper()
	if strings.Contains(e.OriginRef, forbidden) ||
		strings.Contains(e.ResourceRef, forbidden) ||
		strings.Contains(e.ToolRef, forbidden) {
		t.Fatalf("payload content %q leaked into edge %+v", forbidden, e)
	}
}

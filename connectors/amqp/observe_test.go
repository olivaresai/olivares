// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk/model"
)

var at = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

// TestObserveProducerFromUserID: a message stamped with an authenticated user-id and
// no CloudEvents binding attributes the producer firmly (attributed) from the user-id.
func TestObserveProducerFromUserID(t *testing.T) {
	o := &observer{namespaceRef: "ns"}
	edges := o.observeMessage(Message{To: "q.orders", UserID: "svc-orders"}, "obs", at)

	var topo, write *model.EdgeObservation
	for i := range edges {
		switch edges[i].OriginKind {
		case kindNamespace:
			topo = &edges[i]
		case kindIdentity:
			write = &edges[i]
		}
	}
	if topo == nil || topo.ResourceRef != "q.orders" || topo.Mode != model.ModeUnknown {
		t.Fatalf("topology edge wrong: %+v", topo)
	}
	if write == nil {
		t.Fatal("expected a producer write edge from user-id")
	}
	if write.OriginRef != "svc-orders" || write.Mode != model.ModeWrite || write.Confidence != model.ConfidenceAttributed {
		t.Fatalf("user-id producer edge wrong: %+v", write)
	}
}

// TestObserveProducerFromCloudEvents: a binary-bound CloudEvent attributes the
// producer from the ce source (approximate, self-declared) and carries the ce type as
// ToolRef. The ce binding wins over a user-id if both are present.
func TestObserveProducerFromCloudEvents(t *testing.T) {
	o := &observer{namespaceRef: "ns"}
	msg := Message{
		To:     "topic.events",
		UserID: "broker-stamped", // present, but the CE binding takes priority
		AppProps: map[string]string{
			cloudevents.AMQPPrefix + "specversion": "1.0",
			cloudevents.AMQPPrefix + "id":          "evt-1",
			cloudevents.AMQPPrefix + "source":      "/svc/checkout",
			cloudevents.AMQPPrefix + "type":        "com.acme.Created",
		},
	}
	edges := o.observeMessage(msg, "obs", at)

	var write *model.EdgeObservation
	for i := range edges {
		if edges[i].OriginKind == kindIdentity {
			write = &edges[i]
		}
	}
	if write == nil {
		t.Fatal("expected a producer write edge from the CloudEvents source")
	}
	if write.OriginRef != "/svc/checkout" {
		t.Fatalf("CE source should win over user-id, got %q", write.OriginRef)
	}
	if write.Mode != model.ModeWrite || write.Confidence != model.ConfidenceApproximate {
		t.Fatalf("CE producer edge mode/confidence wrong: %+v", write)
	}
	if write.ToolRef != "com.acme.Created" {
		t.Fatalf("CE producer ToolRef should be the ce type: %q", write.ToolRef)
	}
}

// TestObserveNoBodyLeak: the connector observes from metadata ONLY. The neutral
// Message has no body field, so even if a producer smuggles a payload-looking string
// into a property, the edges only carry the destination/identity references — never a
// body. We assert the address/identity refs are exactly the framing values.
func TestObserveNoBodyLeak(t *testing.T) {
	o := &observer{namespaceRef: "ns"}
	// Subject carries an opaque string; it must NOT appear in any edge ref (the
	// observer derives edges from To/UserID/AppProps, not Subject/body).
	edges := o.observeMessage(Message{
		To:      "q.telemetry",
		Subject: `{"sensor":42,"secret_payload":"do-not-emit"}`,
		UserID:  "device-7",
	}, "obs", at)

	if len(edges) == 0 {
		t.Fatal("no edges emitted")
	}
	for _, e := range edges {
		for _, ref := range []string{e.OriginRef, e.ResourceRef, e.ToolRef} {
			if strings.Contains(ref, "secret_payload") || strings.Contains(ref, "sensor") {
				t.Fatalf("message content leaked into an edge ref: %q", ref)
			}
		}
		if e.ResourceRef != "q.telemetry" {
			t.Fatalf("address ref should be the To value only, got %q", e.ResourceRef)
		}
	}
}

// TestObserveRedactsSecretInProducerRef: a producer reference (here a CloudEvents
// source) that smuggles a bearer token must be scrubbed by brokerobs.Edge before it
// reaches the bus — the secret never survives in any emitted ref.
func TestObserveRedactsSecretInProducerRef(t *testing.T) {
	o := &observer{namespaceRef: "ns"}
	msg := Message{
		To: "q1",
		AppProps: map[string]string{
			cloudevents.AMQPPrefix + "specversion": "1.0",
			cloudevents.AMQPPrefix + "id":          "e1",
			cloudevents.AMQPPrefix + "source":      "svc bearer sk-ant-abcdefghijklmnopqrstuvwxyz0123",
			cloudevents.AMQPPrefix + "type":        "evt",
		},
	}
	edges := o.observeMessage(msg, "obs", at)
	for _, e := range edges {
		if strings.Contains(e.OriginRef, "sk-ant-") {
			t.Fatalf("secret survived in producer edge OriginRef: %q", e.OriginRef)
		}
	}
}

// TestObserveRedactsSecretInUserID: the same redaction guarantee for a user-id that
// smuggles a credential.
func TestObserveRedactsSecretInUserID(t *testing.T) {
	o := &observer{namespaceRef: "ns"}
	edges := o.observeMessage(Message{
		To:     "q1",
		UserID: "svc bearer sk-ant-abcdefghijklmnopqrstuvwxyz0123",
	}, "obs", at)
	for _, e := range edges {
		if strings.Contains(e.OriginRef, "sk-ant-") {
			t.Fatalf("secret survived in user-id edge OriginRef: %q", e.OriginRef)
		}
	}
}

// TestRecognizeCloudEventIgnoresNonCE: an application-properties map without the
// cloudEvents_ binding markers is not recognized as a CloudEvent (no producer edge
// from a non-CE message that also has no user-id).
func TestRecognizeCloudEventIgnoresNonCE(t *testing.T) {
	msg := Message{To: "q1", AppProps: map[string]string{"x-trace": "abc", "tenant": "acme"}}
	if _, ok := recognizeCloudEvent(msg); ok {
		t.Fatal("a non-CloudEvents application-properties map must not be recognized")
	}
	o := &observer{namespaceRef: "ns"}
	edges := o.observeMessage(msg, "obs", at)
	for _, e := range edges {
		if e.OriginKind == kindIdentity {
			t.Fatalf("no producer edge expected for a non-CE message without user-id: %+v", e)
		}
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeClient is the broker SIMULATOR: it yields one canned batch of JetStream messages
// then ends the run by canceling the test context. No real NATS, no net.Conn — the
// OBSERVATION path (message → edges → sink) is what is exercised.
type fakeClient struct {
	msgs      []Msg
	cancel    context.CancelFunc
	exhausted bool
}

func (f *fakeClient) Next(ctx context.Context, _ int) ([]Msg, error) {
	if !f.exhausted {
		f.exhausted = true
		return f.msgs, nil
	}
	f.cancel() // the batch has been observed; end the streaming Gather
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeClient) Close() error { return nil }

// capSink captures emitted observations.
type capSink struct{ obs []model.Observation }

func (s *capSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func (s *capSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, o := range s.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

// cloudEventBinaryMsg builds a JetStream message carrying a CloudEvent in binary mode:
// NATS headers hold the bare CloudEvents attribute names (MQTTPrefix == "").
func cloudEventBinaryMsg(subject, source, ceType string) Msg {
	return Msg{
		Subject: subject,
		Header: map[string]string{
			"specversion":     "1.0",
			"id":              "evt-9",
			"source":          source,
			"type":            ceType,
			"datacontenttype": "application/json",
		},
		Data: []byte(`{"x":1}`), // payload is NEVER read in binary mode
	}
}

func newTestSource(cfg config, fc *fakeClient) *Source {
	s := New()
	s.cfg = cfg
	s.obs = &observer{streamRef: cfg.streamRef, consumerRef: cfg.consumer}
	s.newClient = func(config) (jsClient, error) { return fc, nil }
	return s
}

func TestSourceGatherEmitsRealEdgesFromSimulatedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeClient{
		cancel: cancel,
		msgs: []Msg{
			cloudEventBinaryMsg("orders.events.created", "/apps/checkout", "com.acme.OrderCreated"),
			{Subject: "telemetry.sensor.temp", Data: []byte("23.4")}, // plain message, no CE
		},
	}

	cfg := config{streamRef: "ORDERS", stream: "ORDERS", consumer: "olivares-obs", batch: 64}
	s := newTestSource(cfg, fc)

	sink := &capSink{}
	if err := s.Gather(ctx, sink); err != context.Canceled {
		t.Fatalf("Gather should end with context.Canceled, got %v", err)
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("no edges emitted — the stream simulation produced no observations")
	}

	// Every edge must carry the nats signal source and never the message payload.
	for _, e := range edges {
		if e.Source != "nats" {
			t.Fatalf("edge with wrong signal source: %q", e.Source)
		}
		if strings.Contains(e.ResourceRef, `"x":1`) || strings.Contains(e.OriginRef, `"x":1`) ||
			strings.Contains(e.ResourceRef, "23.4") || strings.Contains(e.OriginRef, "23.4") ||
			strings.Contains(e.ToolRef, "23.4") {
			t.Fatalf("message payload content leaked into an edge: %+v", e)
		}
	}

	want := map[string]bool{
		// consumer reads the stream (attach)
		"nats.consumer|olivares-obs|nats.stream|ORDERS": false,
		// stream carries traffic on the subjects (topology)
		"nats.stream|ORDERS|nats.subject|orders.events.created": false,
		"nats.stream|ORDERS|nats.subject|telemetry.sensor.temp": false,
		// subject token hierarchy (topology)
		"nats.subject|orders.events|nats.subject|orders.events.created": false,
		"nats.subject|orders|nats.subject|orders.events.created":        false,
		// CloudEvents producer → subject (write)
		"identity|/apps/checkout|nats.subject|orders.events.created": false,
	}
	for _, e := range edges {
		k := e.OriginKind + "|" + e.OriginRef + "|" + e.ResourceKind + "|" + e.ResourceRef
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("expected edge not emitted: %s", k)
		}
	}

	// The CloudEvents producer edge must be a WRITE with approximate confidence and the
	// ce type as its tool ref.
	var foundWrite bool
	for _, e := range edges {
		if e.OriginRef == "/apps/checkout" {
			foundWrite = true
			if e.Mode != model.ModeWrite || e.Confidence != model.ConfidenceApproximate {
				t.Fatalf("producer edge mode/confidence wrong: %+v", e)
			}
			if e.ToolRef != "com.acme.OrderCreated" {
				t.Fatalf("producer edge ToolRef should be the ce type: %q", e.ToolRef)
			}
		}
	}
	if !foundWrite {
		t.Fatal("CloudEvents producer write edge missing")
	}
}

// TestObserveStructuredCloudEvent recognizes a CloudEvent carried as a structured JSON
// payload and attributes the producer, while NEVER emitting the data member.
func TestObserveStructuredCloudEvent(t *testing.T) {
	o := &observer{streamRef: "S1", consumerRef: "c1"}
	doc := `{"specversion":"1.0","id":"x","source":"/svc/billing","type":"evt","data":{"secret":"hunter2"}}`
	edges := o.observeMsg(Msg{Subject: "billing.run", Data: []byte(doc)}, model.EdgeObservation{}.ObservedAt)
	var foundProducer bool
	for _, e := range edges {
		if strings.Contains(e.ResourceRef, "hunter2") || strings.Contains(e.OriginRef, "hunter2") {
			t.Fatalf("structured CE data member leaked into an edge: %+v", e)
		}
		if e.OriginRef == "/svc/billing" && e.Mode == model.ModeWrite {
			foundProducer = true
		}
	}
	if !foundProducer {
		t.Fatal("structured CloudEvent producer edge missing")
	}
}

// TestObserveRedactsSecretInCloudEventSource proves the minimal-data redaction pass
// neutralizes a secret shape embedded in a CloudEvents source before it becomes an edge.
func TestObserveRedactsSecretInCloudEventSource(t *testing.T) {
	o := &observer{streamRef: "S1", consumerRef: "c1"}
	m := cloudEventBinaryMsg("t1", "service bearer sk-ant-abcdefghijklmnopqrstuvwxyz0123", "evt")
	edges := o.observeMsg(m, model.EdgeObservation{}.ObservedAt)
	for _, e := range edges {
		if strings.Contains(e.OriginRef, "sk-ant-") {
			t.Fatalf("secret survived in producer edge OriginRef: %q", e.OriginRef)
		}
	}
}

// TestObserveStatusMsgYieldsNoEdges proves a JetStream control/status frame produces
// no topology (it is not a real stream message).
func TestObserveStatusMsgYieldsNoEdges(t *testing.T) {
	o := &observer{streamRef: "S1", consumerRef: "c1"}
	if e := o.observeMsg(Msg{Status: "404"}, model.EdgeObservation{}.ObservedAt); len(e) != 0 {
		t.Fatalf("status frame must yield no edges, got %d", len(e))
	}
}

func TestLoadConfigValidation(t *testing.T) {
	// missing servers
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{"stream": "S", "consumer": "c"}}); err == nil {
		t.Fatal("missing servers should error")
	}
	// missing stream
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{"servers": "nats://a:4222", "consumer": "c"}}); err == nil {
		t.Fatal("missing stream should error")
	}
	// missing consumer
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{"servers": "nats://a:4222", "stream": "S"}}); err == nil {
		t.Fatal("missing consumer should error")
	}
	c, err := loadConfig(sdk.Config{Settings: map[string]string{
		"servers": "nats://a:4222, nats://b:4222", "stream": "ORDERS", "consumer": "obs",
	}})
	if err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	if len(c.servers) != 2 || c.streamRef != "ORDERS" || c.batch != defaultBatch || c.expires != defaultExpires {
		t.Fatalf("config defaults wrong: %+v", c)
	}
	if c.tls != nil {
		t.Fatalf("plain nats:// should not build a TLS config")
	}
	// natss:// forces TLS on.
	cs, err := loadConfig(sdk.Config{Settings: map[string]string{
		"servers": "natss://secure:4222", "stream": "S", "consumer": "c",
	}})
	if err != nil {
		t.Fatalf("natss config errored: %v", err)
	}
	if cs.tls == nil {
		t.Fatalf("natss:// scheme should force a TLS config")
	}
}

func TestDescriptorIsSourceOnly(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.nats" {
		t.Fatalf("descriptor name = %q", d.Name)
	}
	if d.Type != sdk.TypeSource {
		t.Fatalf("nats is source-only, got type %q", d.Type)
	}
	// stream/consumer/servers must be declared required.
	req := map[string]bool{}
	for _, f := range d.ConfigFields {
		if f.Required {
			req[f.Key] = true
		}
	}
	for _, k := range []string{"servers", "stream", "consumer"} {
		if !req[k] {
			t.Fatalf("config field %q should be required", k)
		}
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeReceiver is the broker SIMULATOR: it yields a canned slice of messages, then
// ends the streaming Gather by canceling the test context. No real broker, no
// go-amqp network path — the OBSERVATION path (message → edges → sink) is what is
// exercised. It records the messages it was asked to settle so the test can assert
// the connector accepts on the observation queue (a dedicated tee).
type fakeReceiver struct {
	msgs     []Message
	idx      int
	cancel   context.CancelFunc
	accepted int
}

func (f *fakeReceiver) Receive(ctx context.Context) (Message, error) {
	if f.idx < len(f.msgs) {
		m := f.msgs[f.idx]
		f.idx++
		return m, nil
	}
	f.cancel() // the batch has been observed; end the streaming Gather
	<-ctx.Done()
	return Message{}, ctx.Err()
}

func (f *fakeReceiver) Accept(_ context.Context, _ Message) error {
	f.accepted++
	return nil
}

func (f *fakeReceiver) Close() {}

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

// cloudEventBinaryMessage builds an AMQP message carrying a CloudEvent in binary mode
// (cloudEvents_-prefixed application-properties). The body is intentionally omitted —
// the neutral Message has no body field, proving the connector cannot read it.
func cloudEventBinaryMessage(to, source, ceType string) Message {
	return Message{
		To: to,
		AppProps: map[string]string{
			cloudevents.AMQPPrefix + "specversion": "1.0",
			cloudevents.AMQPPrefix + "id":          "evt-9",
			cloudevents.AMQPPrefix + "source":      source,
			cloudevents.AMQPPrefix + "type":        ceType,
		},
	}
}

func newTestSource(cfg config, fr *fakeReceiver) *Source {
	s := New()
	s.cfg = cfg
	s.obs = &observer{namespaceRef: cfg.namespaceRef}
	s.newReceiver = func(config) (receiver, error) { return fr, nil }
	return s
}

func TestSourceGatherEmitsRealEdgesFromSimulatedBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fr := &fakeReceiver{
		cancel: cancel,
		msgs: []Message{
			// (1) CloudEvents producer: source attributed from the binary binding.
			cloudEventBinaryMessage("orders.events", "/apps/checkout", "com.acme.OrderCreated"),
			// (2) user-id producer: a plain message stamped with an authenticated id,
			//     addressed to a different destination (To set).
			{To: "billing.events", UserID: "svc-billing"},
			// (3) no To, no producer: falls back to the observation address for topology.
			{Subject: "heartbeat"},
		},
	}

	cfg := config{namespaceRef: "rabbit:5671", observationAddress: "obs.mirror"}
	s := newTestSource(cfg, fr)

	sink := &capSink{}
	if err := s.Gather(ctx, sink); err != context.Canceled {
		t.Fatalf("Gather should end with context.Canceled, got %v", err)
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("no edges emitted — the broker simulation produced no observations")
	}

	// Every edge must carry the amqp signal source and never message body content.
	for _, e := range edges {
		if e.Source != "amqp" {
			t.Fatalf("edge with wrong signal source: %q", e.Source)
		}
	}

	// Each observed message must have been settled on the observation queue (a tee),
	// never the production queue — proved here by the accept count matching messages.
	if fr.accepted != len(fr.msgs) {
		t.Fatalf("connector must settle every observed message on the obs queue: accepted %d of %d", fr.accepted, len(fr.msgs))
	}

	want := map[string]bool{
		// topology namespace→address (To present)
		"amqp.namespace|rabbit:5671|amqp.address|orders.events":  false,
		"amqp.namespace|rabbit:5671|amqp.address|billing.events": false,
		// topology fallback to the observation address (no To)
		"amqp.namespace|rabbit:5671|amqp.address|obs.mirror": false,
		// CloudEvents producer→address (write)
		"identity|/apps/checkout|amqp.address|orders.events": false,
		// user-id producer→address (write)
		"identity|svc-billing|amqp.address|billing.events": false,
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

	// The CloudEvents producer edge must be a WRITE with approximate confidence and
	// the ce type as ToolRef; the user-id producer edge a WRITE with attributed
	// confidence.
	var foundCE, foundUser bool
	for _, e := range edges {
		switch e.OriginRef {
		case "/apps/checkout":
			foundCE = true
			if e.Mode != model.ModeWrite || e.Confidence != model.ConfidenceApproximate {
				t.Fatalf("CE producer edge mode/confidence wrong: %+v", e)
			}
			if e.ToolRef != "com.acme.OrderCreated" {
				t.Fatalf("CE producer edge ToolRef should be the ce type: %q", e.ToolRef)
			}
		case "svc-billing":
			foundUser = true
			if e.Mode != model.ModeWrite || e.Confidence != model.ConfidenceAttributed {
				t.Fatalf("user-id producer edge mode/confidence wrong: %+v", e)
			}
		}
	}
	if !foundCE {
		t.Fatal("CloudEvents producer write edge missing")
	}
	if !foundUser {
		t.Fatal("user-id producer write edge missing")
	}
}

// fakeSender captures the sent message.
type fakeSender struct{ last OutMessage }

func (s *fakeSender) Send(_ context.Context, m OutMessage) error { s.last = m; return nil }
func (s *fakeSender) Close()                                     {}

func TestOutputNotifyStructuredCloudEvent(t *testing.T) {
	fs := &fakeSender{}
	o := NewOutput()
	o.newSender = func(config) (sender, error) { return fs, nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"addr": "amqps://rabbit:5671", "egress_address": "olivares.findings",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer o.Close(context.Background())

	err := o.Notify(context.Background(), sdk.Notification{
		Type: "finding.reported", Title: "Prompt injection", Severity: model.SeverityHigh, Tenant: "acme",
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if fs.last.ContentType != cloudevents.ContentTypeStructured {
		t.Fatalf("structured content-type wrong: %q", fs.last.ContentType)
	}
	if fs.last.AppProps != nil {
		t.Fatalf("structured mode must not set application-properties: %v", fs.last.AppProps)
	}
	ev, perr := cloudevents.Parse(fs.last.Body)
	if perr != nil {
		t.Fatalf("produced value is not a valid CloudEvent: %v", perr)
	}
	if ev.Type != "ai.olivares.finding.reported" {
		t.Fatalf("ce type wrong: %q", ev.Type)
	}
	if ev.Extensions["severity"] != "high" {
		t.Fatalf("severity extension missing: %v", ev.Extensions)
	}
}

func TestOutputNotifyBinaryCloudEvent(t *testing.T) {
	fs := &fakeSender{}
	o := NewOutput()
	o.newSender = func(config) (sender, error) { return fs, nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"addr": "amqp://b:5672", "egress_address": "t", "binary_egress": "true",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Type: "finding.reported", Title: "x"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if fs.last.AppProps[cloudevents.AMQPPrefix+"type"] != "ai.olivares.finding.reported" {
		t.Fatalf("binary cloudEvents_type property missing: %v", fs.last.AppProps)
	}
	if _, leaked := fs.last.AppProps[cloudevents.AMQPPrefix+"datacontenttype"]; leaked {
		t.Fatalf("datacontenttype must not be a cloudEvents_ property")
	}
	// The body must still round-trip back to a valid CloudEvent (the data is the
	// notification payload, sent as the AMQP body with content-type set separately).
	if fs.last.ContentType != "application/json" {
		t.Fatalf("binary content-type should be the data content-type: %q", fs.last.ContentType)
	}
}

func TestLoadConfigValidation(t *testing.T) {
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("missing addr should error")
	}
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{"addr": "redis://x"}}); err == nil {
		t.Fatal("non-amqp scheme should error")
	}
	c, err := loadConfig(sdk.Config{Settings: map[string]string{"addr": "amqps://rabbit.example:5671"}})
	if err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	if c.egressSource != "/olivares/olivares" {
		t.Fatalf("egress_source default wrong: %q", c.egressSource)
	}
	// The namespace label must be derived from the endpoint, with any userinfo
	// credentials stripped — never carry user:pass into an emitted edge.
	c2, err := loadConfig(sdk.Config{Settings: map[string]string{"addr": "amqps://admin:s3cr3t@rabbit:5671"}})
	if err != nil {
		t.Fatalf("valid config with userinfo errored: %v", err)
	}
	if strings.Contains(c2.namespaceRef, "s3cr3t") {
		t.Fatalf("credential leaked into namespace_ref: %q", c2.namespaceRef)
	}

	// The Source requires an observation_address.
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"addr": "amqps://r:5671"}}); err == nil {
		t.Fatal("source open without observation_address should error")
	}
	// The Output requires an egress_address.
	out := NewOutput()
	out.newSender = func(config) (sender, error) { return &fakeSender{}, nil }
	if err := out.Open(context.Background(), sdk.Config{Settings: map[string]string{"addr": "amqps://r:5671"}}); err == nil {
		t.Fatal("output open without egress_address should error")
	}
}

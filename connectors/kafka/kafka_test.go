// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeConsumer is the broker SIMULATOR: it yields one canned batch of records and a
// canned topology, then ends the run by canceling the test context. No real Kafka,
// no franz-go network path — the OBSERVATION path (record → edges → sink) is what is
// exercised.
type fakeConsumer struct {
	topo      kafkawire.Topology
	records   []kafkawire.Record
	cancel    context.CancelFunc
	exhausted bool
}

func (f *fakeConsumer) Topology(context.Context) (kafkawire.Topology, error) { return f.topo, nil }

func (f *fakeConsumer) Poll(ctx context.Context) ([]kafkawire.Record, error) {
	if !f.exhausted {
		f.exhausted = true
		return f.records, nil
	}
	f.cancel() // the batch has been observed; end the streaming Gather
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeConsumer) Close() {}

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

// confluentFramed builds a classic-wire-format value: magic 0x00 + 4-byte BE id.
func confluentFramed(id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(b[1:5], id)
	copy(b[5:], payload)
	return b
}

// cloudEventBinaryRecord builds a Kafka record carrying a CloudEvent in binary mode.
func cloudEventBinaryRecord(topic, source, ceType string) kafkawire.Record {
	return kafkawire.Record{
		Topic: topic,
		Headers: map[string][]byte{
			cloudevents.KafkaPrefix + "specversion": []byte("1.0"),
			cloudevents.KafkaPrefix + "id":          []byte("evt-9"),
			cloudevents.KafkaPrefix + "source":      []byte(source),
			cloudevents.KafkaPrefix + "type":        []byte(ceType),
			"content-type":                          []byte("application/json"),
		},
		Value: []byte(`{"x":1}`), // body is NEVER read in binary mode
	}
}

func newTestSource(t *testing.T, cfg config, fc *fakeConsumer) *Source {
	t.Helper()
	s := New()
	s.cfg = cfg
	s.obs = &observer{clusterRef: cfg.clusterRef}
	s.newConsumer = func(config) (kafkawire.Consumer, error) { return fc, nil }
	return s
}

func TestSourceGatherEmitsRealEdgesFromSimulatedBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeConsumer{
		cancel: cancel,
		topo: kafkawire.Topology{
			ClusterRef: "kafka-1:9092",
			Topics:     []string{"orders.events"},
			Groups:     []kafkawire.GroupInfo{{Group: "billing", Topics: []string{"orders.events"}, Members: 2}},
		},
		records: []kafkawire.Record{
			cloudEventBinaryRecord("orders.events", "/apps/checkout", "com.acme.OrderCreated"),
			{Topic: "orders.events", Value: confluentFramed(42, []byte{0xAA})}, // schema-framed, no registry
		},
	}

	cfg := config{clusterRef: "kafka-1:9092", topologyScan: true}
	s := newTestSource(t, cfg, fc)

	sink := &capSink{}
	if err := s.Gather(ctx, sink); err != context.Canceled {
		t.Fatalf("Gather should end with context.Canceled, got %v", err)
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("no edges emitted — the broker simulation produced no observations")
	}

	// Every edge must carry the kafka signal source and never the record body.
	for _, e := range edges {
		if e.Source != "kafka" {
			t.Fatalf("edge with wrong signal source: %q", e.Source)
		}
		if strings.Contains(e.ResourceRef, `"x":1`) || strings.Contains(e.OriginRef, `"x":1`) {
			t.Fatalf("record body content leaked into an edge: %+v", e)
		}
	}

	want := map[string]bool{
		// topology
		"kafka.cluster|kafka-1:9092|kafka.topic|orders.events":    false,
		"kafka.consumer_group|billing|kafka.topic|orders.events":  false,
		"kafka.cluster|kafka-1:9092|kafka.consumer_group|billing": false,
		// CloudEvents producer→topic (write)
		"identity|/apps/checkout|kafka.topic|orders.events": false,
		// schema-framed topic→contract (unresolved → id:42)
		"kafka.topic|orders.events|schema.subject|id:42": false,
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

	// The CloudEvents producer edge must be a WRITE with approximate confidence.
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

func TestObserveRecordRedactsSecretInCloudEventSource(t *testing.T) {
	o := &observer{clusterRef: "c1"}
	rec := cloudEventBinaryRecord("t1", "service bearer sk-ant-abcdefghijklmnopqrstuvwxyz0123", "evt")
	edges := o.observeRecord(context.Background(), rec, model.EdgeObservation{}.ObservedAt)
	for _, e := range edges {
		if strings.Contains(e.OriginRef, "sk-ant-") {
			t.Fatalf("secret survived in producer edge OriginRef: %q", e.OriginRef)
		}
	}
}

// fakeProducer captures the produced message.
type fakeProducer struct {
	topic   string
	key     []byte
	value   []byte
	headers map[string][]byte
}

func (p *fakeProducer) Produce(_ context.Context, topic string, key, value []byte, headers map[string][]byte) error {
	p.topic, p.key, p.value, p.headers = topic, key, value, headers
	return nil
}
func (p *fakeProducer) Close() {}

func TestOutputNotifyStructuredCloudEvent(t *testing.T) {
	fp := &fakeProducer{}
	o := NewOutput()
	o.newProducer = func(config) (kafkawire.Producer, error) { return fp, nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"brokers": "kafka-1:9092", "egress_topic": "olivares.findings",
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
	if fp.topic != "olivares.findings" {
		t.Fatalf("egress topic wrong: %q", fp.topic)
	}
	if string(fp.headers["content-type"]) != cloudevents.ContentTypeStructured {
		t.Fatalf("structured content-type header missing: %v", fp.headers)
	}
	ev, perr := cloudevents.Parse(fp.value)
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
	fp := &fakeProducer{}
	o := NewOutput()
	o.newProducer = func(config) (kafkawire.Producer, error) { return fp, nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"brokers": "k:9092", "egress_topic": "t", "binary_egress": "true",
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{Type: "finding.reported", Title: "x"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if string(fp.headers["ce_type"]) != "ai.olivares.finding.reported" {
		t.Fatalf("binary ce_type header missing: %v", fp.headers)
	}
	if _, leaked := fp.headers["ce_datacontenttype"]; leaked {
		t.Fatalf("datacontenttype must not be a ce_ header")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("missing brokers should error")
	}
	c, err := loadConfig(sdk.Config{Settings: map[string]string{"brokers": "a:9092, b:9092"}})
	if err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	if len(c.brokers) != 2 || c.clusterRef != "a:9092" || c.group != "olivares-observer" {
		t.Fatalf("config defaults wrong: %+v", c)
	}
}

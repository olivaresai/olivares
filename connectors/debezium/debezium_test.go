// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package debezium

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/kafkawire"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// A canonical Debezium Postgres envelope (schema+payload form) for an UPDATE. The
// before/after carry a row the observer must NEVER surface (it includes a fake
// secret to prove non-leak).
const pgUpdateEnvelope = `{
  "schema": {"type":"struct"},
  "payload": {
    "before": {"id": 7, "email": "a@b.com", "token": "sk-ant-SECRETSECRETSECRETSECRET"},
    "after":  {"id": 7, "email": "a@b.com", "token": "sk-ant-NEWSECRETNEWSECRETNEWSECRET"},
    "source": {"connector":"postgresql","name":"inventory","db":"shop","schema":"public","table":"customers","ts_ms": 1717670000000},
    "op": "u",
    "ts_ms": 1717670000123
  }
}`

// A MySQL DELETE in the unwrapped (no schema) top-level form.
const mysqlDeleteEnvelope = `{
  "before": {"id": 3, "card": "4111111111111111"},
  "after": null,
  "source": {"connector":"mysql","name":"erp","db":"sales","table":"orders","ts_ms": 1717670001000},
  "op": "d",
  "ts_ms": 1717670001050
}`

func TestParseChangeAndMode(t *testing.T) {
	ch, ok := parseChange([]byte(pgUpdateEnvelope))
	if !ok {
		t.Fatal("postgres update envelope not parsed")
	}
	if ch.Op != "u" || ch.Connector != "postgresql" || ch.tableRef() != "shop.public.customers" {
		t.Fatalf("change wrong: %+v ref=%s", ch, ch.tableRef())
	}
	if modeForOp(ch.Op) != model.ModeWrite {
		t.Fatalf("update should be a write")
	}
	if ch.sourceRef() != "postgresql:inventory" {
		t.Fatalf("source ref wrong: %s", ch.sourceRef())
	}

	ch2, ok := parseChange([]byte(mysqlDeleteEnvelope))
	if !ok {
		t.Fatal("mysql delete envelope not parsed")
	}
	if ch2.tableRef() != "sales.orders" || modeForOp(ch2.Op) != model.ModeWrite {
		t.Fatalf("mysql change wrong: %+v", ch2)
	}

	// snapshot read = read; truncate = write; unknown = unknown.
	if modeForOp("r") != model.ModeRead || modeForOp("t") != model.ModeWrite || modeForOp("z") != model.ModeUnknown {
		t.Fatal("op->mode mapping wrong")
	}

	// Tombstone / non-envelope skipped.
	if _, ok := parseChange(nil); ok {
		t.Fatal("tombstone must not parse")
	}
	if _, ok := parseChange([]byte(`{"hello":"world"}`)); ok {
		t.Fatal("non-envelope must not parse")
	}
}

func TestEdgesAreMinimalDataNoRowLeak(t *testing.T) {
	ch, _ := parseChange([]byte(pgUpdateEnvelope))
	edges := ch.edges(time.Now().UTC())
	if len(edges) != 1 {
		t.Fatalf("want 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Source != "debezium" {
		t.Fatalf("signal source wrong: %q", e.Source)
	}
	if e.ResourceKind != "cdc.table" {
		t.Fatalf("CDC edge must use cdc.table (frontier), got %q", e.ResourceKind)
	}
	if e.ResourceRef != "shop.public.customers" || e.Mode != model.ModeWrite {
		t.Fatalf("edge wrong: %+v", e)
	}
	// No row data — and certainly no secret — anywhere in the edge.
	blob := e.OriginKind + e.OriginRef + e.ResourceKind + e.ResourceRef + e.ToolRef
	for _, leak := range []string{"sk-ant-", "a@b.com", "token", "email", "4111"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("row data leaked into edge (%q): %+v", leak, e)
		}
	}
}

// fakeConsumer yields one batch of Debezium records then ends the run.
type fakeConsumer struct {
	records   []kafkawire.Record
	cancel    context.CancelFunc
	exhausted bool
}

func (f *fakeConsumer) Topology(context.Context) (kafkawire.Topology, error) {
	return kafkawire.Topology{}, nil
}
func (f *fakeConsumer) Poll(ctx context.Context) ([]kafkawire.Record, error) {
	if !f.exhausted {
		f.exhausted = true
		return f.records, nil
	}
	f.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}
func (f *fakeConsumer) Close() {}

type capSink struct{ edges []model.EdgeObservation }

func (s *capSink) Emit(_ context.Context, o model.Observation) error {
	if e, ok := o.(model.EdgeObservation); ok {
		s.edges = append(s.edges, e)
	}
	return nil
}

func TestGatherEmitsCDCEdgesFromSimulatedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fc := &fakeConsumer{
		cancel: cancel,
		records: []kafkawire.Record{
			{Topic: "inventory.public.customers", Value: []byte(pgUpdateEnvelope)},
			{Topic: "erp.sales.orders", Value: []byte(mysqlDeleteEnvelope)},
			{Topic: "x", Value: nil}, // tombstone, skipped
		},
	}
	s := New()
	s.cfg = config{brokers: []string{"k:9092"}, topics: []string{"inventory.public.customers"}, group: "g"}
	s.newConsumer = func(config) (kafkawire.Consumer, error) { return fc, nil }

	sink := &capSink{}
	if err := s.Gather(ctx, sink); err != context.Canceled {
		t.Fatalf("Gather should end with context.Canceled, got %v", err)
	}
	if len(sink.edges) != 2 {
		t.Fatalf("want 2 CDC edges (tombstone skipped), got %d", len(sink.edges))
	}
	for _, e := range sink.edges {
		if e.ResourceKind != "cdc.table" || e.Source != "debezium" {
			t.Fatalf("edge wrong: %+v", e)
		}
	}
}

func TestOpenValidation(t *testing.T) {
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{"topics": "t"}}); err == nil {
		t.Fatal("missing brokers should error")
	}
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{"brokers": "k:9092"}}); err == nil {
		t.Fatal("missing topics should error")
	}
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{"brokers": "k:9092", "topics": "t", "sasl_mechanism": "bogus"}}); err == nil {
		t.Fatal("bad sasl mechanism should error")
	}
}

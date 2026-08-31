// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package brokerobs

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestEdgeAppliesMinimalDataRedaction(t *testing.T) {
	at := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	// A client id that smuggles a bearer token must never reach the bus verbatim.
	o := Observation{
		OriginKind:   "identity",
		OriginRef:    "svc-orders bearer sk-ant-abcdefghijklmnopqrstuvwxyz0123",
		ResourceKind: "kafka.topic",
		ResourceRef:  "orders.events",
		Mode:         model.ModeRead,
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      "schema:orders-value",
		ObservedAt:   at,
	}
	e := o.Edge(SignalKafka)

	if strings.Contains(e.OriginRef, "sk-ant-") {
		t.Fatalf("secret survived into OriginRef: %q", e.OriginRef)
	}
	if !strings.Contains(e.OriginRef, "[REDACTED") {
		t.Fatalf("expected a redaction marker in OriginRef, got %q", e.OriginRef)
	}
	if e.ResourceRef != "orders.events" {
		t.Fatalf("ordinary topic name was altered: %q", e.ResourceRef)
	}
	if e.Source != SignalKafka {
		t.Fatalf("signal source = %q, want kafka", e.Source)
	}
	if e.Mode != model.ModeRead || e.Confidence != model.ConfidenceAttributed {
		t.Fatalf("mode/confidence not preserved: %q/%q", e.Mode, e.Confidence)
	}
	if !e.ObservedAt.Equal(at) {
		t.Fatalf("ObservedAt not preserved: %v", e.ObservedAt)
	}
}

func TestEdgeDefaultsNeverFabricateCertainty(t *testing.T) {
	e := Observation{ResourceKind: "amqp.queue", ResourceRef: "q1"}.Edge(SignalAMQP)
	if e.Mode != model.ModeUnknown {
		t.Fatalf("empty mode should default to unknown, got %q", e.Mode)
	}
	if e.Confidence != model.ConfidenceApproximate {
		t.Fatalf("empty confidence should default to approximate, got %q", e.Confidence)
	}
}

func TestInstrumentationDefaultOff(t *testing.T) {
	var off Instrumentation // zero value disabled
	if got := off.Attrs(OpReceive, "orders", "g1", 5); got != nil {
		t.Fatalf("disabled instrumentation must return nil, got %v", got)
	}
}

func TestInstrumentationAttrsNoMessageContent(t *testing.T) {
	i := Instrumentation{Enabled: true, System: "kafka"}
	attrs := i.Attrs(OpReceive, "orders.events", "billing-group", 3)
	if attrs[AttrSystem] != "kafka" {
		t.Fatalf("missing system attr: %v", attrs)
	}
	if attrs[AttrDestinationName] != "orders.events" {
		t.Fatalf("destination attr wrong: %v", attrs)
	}
	if attrs[AttrOperationType] != OpReceive {
		t.Fatalf("operation type wrong: %v", attrs)
	}
	if attrs[AttrConsumerGroupName] != "billing-group" {
		t.Fatalf("consumer group attr wrong: %v", attrs)
	}
	if attrs[AttrBatchMessageCount] != "3" {
		t.Fatalf("batch count attr wrong: %v", attrs)
	}
	// Guard: no attribute may carry a message key/body. We never set such keys, so
	// assert the well-known content keys are absent.
	for k := range attrs {
		if strings.Contains(k, "message.key") || strings.Contains(k, "body") || strings.Contains(k, "payload") {
			t.Fatalf("instrumentation leaked a content attribute: %q", k)
		}
	}
}

// fakeCfg satisfies ConfigBool for the gate test.
type fakeCfg map[string]bool

func (f fakeCfg) GetBool(key string, def bool) bool {
	if v, ok := f[key]; ok {
		return v
	}
	return def
}

func TestInstrumentationFromConfigGateDefaultsOff(t *testing.T) {
	if InstrumentationFromConfig(fakeCfg{}, "kafka").Enabled {
		t.Fatalf("gate must default OFF when unset")
	}
	if !InstrumentationFromConfig(fakeCfg{"otel_messaging": true}, "kafka").Enabled {
		t.Fatalf("explicit opt-in must enable the gate")
	}
}

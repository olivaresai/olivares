// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hubble

import (
	"context"
	"testing"
	"time"

	flow "github.com/cilium/cilium/api/v1/flow"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const testTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func testTime() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

func httpFlow(verdict flow.Verdict, method string) *flow.Flow {
	return &flow.Flow{
		Verdict:          verdict,
		Time:             timestamppb.New(testTime()),
		Source:           &flow.Endpoint{Namespace: "default", PodName: "payments-abc"},
		DestinationNames: []string{"api.anthropic.com"},
		L4:               &flow.Layer4{Protocol: &flow.Layer4_TCP{TCP: &flow.TCP{DestinationPort: 443}}},
		L7: &flow.Layer7{Record: &flow.Layer7_Http{Http: &flow.HTTP{
			Method:  method,
			Url:     "https://api.anthropic.com/v1/messages",
			Headers: []*flow.HTTPHeader{{Key: "traceparent", Value: testTraceParent}},
		}}},
	}
}

func TestForwardedHTTPFlowIsObservedEdge(t *testing.T) {
	rec, ok := flowToRecord(httpFlow(flow.Verdict_FORWARDED, "POST"), testTime)
	if !ok {
		t.Fatal("expected a record")
	}
	obs := rec.Observations()
	if len(obs) != 1 {
		t.Fatalf("want 1 observation, got %d", len(obs))
	}
	e, ok := obs[0].(model.EdgeObservation)
	if !ok {
		t.Fatalf("want EdgeObservation, got %T", obs[0])
	}
	if e.ResourceKind != "http.api" || e.ResourceRef != "api.anthropic.com" {
		t.Errorf("resource = %s/%s", e.ResourceKind, e.ResourceRef)
	}
	if e.Mode != model.ModeReadWrite {
		t.Errorf("Mode = %s, want readwrite (POST)", e.Mode)
	}
	if e.OriginRef != "default/payments-abc" {
		t.Errorf("OriginRef = %s, want default/payments-abc", e.OriginRef)
	}
	if e.Confidence != model.ConfidenceApproximate {
		t.Errorf("Confidence = %s, want approximate (label-derived identity)", e.Confidence)
	}
	if e.Source != SignalHubble {
		t.Errorf("Source = %s", e.Source)
	}
	if !e.ObservedAt.Equal(testTime()) {
		t.Errorf("ObservedAt = %v", e.ObservedAt)
	}
}

func TestDroppedFlowIsDenyFinding(t *testing.T) {
	rec, ok := flowToRecord(httpFlow(flow.Verdict_DROPPED, "GET"), testTime)
	if !ok {
		t.Fatal("expected a record")
	}
	if rec.Verdict != meshobs.VerdictDenied {
		t.Fatalf("Verdict = %q, want denied", rec.Verdict)
	}
	obs := rec.Observations()
	if len(obs) != 1 {
		t.Fatalf("want 1 observation, got %d", len(obs))
	}
	f, ok := obs[0].(model.FindingReport)
	if !ok {
		t.Fatalf("a dropped flow must yield a FindingReport, got %T", obs[0])
	}
	if f.SubjectRef != "api.anthropic.com" {
		t.Errorf("SubjectRef = %s", f.SubjectRef)
	}
	if len(f.OWASPLLM) != 0 {
		t.Errorf("no taxonomy should be asserted for an egress drop, got %v", f.OWASPLLM)
	}
}

func TestAuditVerdictIsDenyFinding(t *testing.T) {
	// AUDIT is a would-be drop observed in audit mode → it must be a DENIAL (the
	// permitted-path signal), never recorded as an allowed edge.
	rec, ok := flowToRecord(httpFlow(flow.Verdict_AUDIT, "GET"), testTime)
	if !ok {
		t.Fatal("expected a record for an AUDIT flow")
	}
	if rec.Verdict != meshobs.VerdictDenied {
		t.Fatalf("AUDIT verdict must map to denied, got %q", rec.Verdict)
	}
	if _, ok := rec.Observations()[0].(model.FindingReport); !ok {
		t.Fatalf("AUDIT flow must yield a FindingReport, got %T", rec.Observations()[0])
	}
}

func TestIntraClusterNoiseSkipped(t *testing.T) {
	// A forwarded L3/L4 flow with no destination name and no L7 is noise → skipped.
	fl := &flow.Flow{
		Verdict: flow.Verdict_FORWARDED,
		Time:    timestamppb.New(testTime()),
		Source:  &flow.Endpoint{Namespace: "default", PodName: "a"},
		L4:      &flow.Layer4{Protocol: &flow.Layer4_TCP{TCP: &flow.TCP{DestinationPort: 8080}}},
	}
	if _, ok := flowToRecord(fl, testTime); ok {
		t.Fatal("intra-cluster L3/L4 forwarded flow should be skipped")
	}
}

func TestErrorVerdictSkipped(t *testing.T) {
	if _, ok := flowToRecord(httpFlow(flow.Verdict_ERROR, "GET"), testTime); ok {
		t.Fatal("ERROR verdict should be skipped")
	}
}

func TestTraceContextExtracted(t *testing.T) {
	rec, _ := flowToRecord(httpFlow(flow.Verdict_FORWARDED, "GET"), testTime)
	if !rec.Trace.Present() || rec.Trace.TraceParent != testTraceParent {
		t.Fatalf("traceparent not extracted from L7 headers: %+v", rec.Trace)
	}
}

func TestDroppedL3FlowKeptEvenWithoutName(t *testing.T) {
	// A drop with no name and no L7 is still kept (denials are always signal), keyed
	// by the destination IP.
	fl := &flow.Flow{
		Verdict: flow.Verdict_DROPPED,
		Time:    timestamppb.New(testTime()),
		Source:  &flow.Endpoint{Namespace: "default", PodName: "a"},
		IP:      &flow.IP{Source: "10.0.0.1", Destination: "203.0.113.7"},
		L4:      &flow.Layer4{Protocol: &flow.Layer4_TCP{TCP: &flow.TCP{DestinationPort: 443}}},
	}
	rec, ok := flowToRecord(fl, testTime)
	if !ok {
		t.Fatal("a dropped flow should be kept even without a destination name")
	}
	// No method, no name → net.endpoint keyed by destination IP:port.
	obs := rec.Observations()
	f, ok := obs[0].(model.FindingReport)
	if !ok {
		t.Fatalf("want finding, got %T", obs[0])
	}
	if f.SubjectRef != "203.0.113.7" {
		t.Errorf("SubjectRef = %s, want destination IP", f.SubjectRef)
	}
}

func TestRelayIsLocal(t *testing.T) {
	cases := map[string]bool{
		"unix:///var/run/cilium/hubble.sock": true,
		"localhost:4245":                     true,
		"127.0.0.1:4245":                     true,
		"[::1]:4245":                         true,
		"hubble-relay.kube-system:4245":      false,
		"10.0.0.1:4245":                      false,
	}
	for addr, want := range cases {
		if got := relayIsLocal(addr); got != want {
			t.Errorf("relayIsLocal(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestOpenRefusesPlaintextRemote(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"relay_addr": "hubble-relay.kube-system:4245"}})
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("expected refusal of plaintext to a non-local relay")
	}

	// A local relay over plaintext is accepted.
	s2 := New()
	if err := s2.Open(context.Background(), sdk.Config{Settings: map[string]string{"relay_addr": "localhost:4245"}}); err != nil {
		t.Fatalf("local plaintext relay should be accepted: %v", err)
	}
	_ = s2.Close(context.Background())

	// TLS to a remote relay is accepted.
	s3 := New()
	if err := s3.Open(context.Background(), sdk.Config{Settings: map[string]string{"relay_addr": "hubble-relay.kube-system:4245", "tls": "true"}}); err != nil {
		t.Fatalf("TLS to a remote relay should be accepted: %v", err)
	}
	_ = s3.Close(context.Background())
}

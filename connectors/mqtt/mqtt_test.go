// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// fakeClient is the broker SIMULATOR: it yields a canned sequence of PUBLISH packets
// (one per Read), then cancels the test context to end the streaming Gather. No real
// broker, no net.Conn — the OBSERVATION path (publish → edges → sink) is exercised.
type fakeClient struct {
	pubs   []Publish
	i      int
	cancel context.CancelFunc
	closed bool
}

func (f *fakeClient) Read(ctx context.Context) (Publish, error) {
	if f.i < len(f.pubs) {
		p := f.pubs[f.i]
		f.i++
		return p, nil
	}
	f.cancel() // the canned batch has been observed; end the streaming Gather
	<-ctx.Done()
	return Publish{}, ctx.Err()
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

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

// TestSourceGatherEmitsRealEdgesFromSimulatedBroker drives the full Source.Gather
// path against a fake client yielding canned MQTT/Sparkplug publishes, and asserts
// the expected minimal-data edges reach the sink with the mqtt signal source and NO
// payload content leaked into any edge.
func TestSourceGatherEmitsRealEdgesFromSimulatedBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fc := &fakeClient{
		cancel: cancel,
		pubs: []Publish{
			{Topic: "spBv1.0/plantA/NBIRTH/edge7", Payload: []byte("PROTO-NODE-BIRTH")},
			{Topic: "spBv1.0/plantA/DDATA/edge7/press01", Payload: []byte("PROTO-DEVICE-DATA 42.7psi")},
			{Topic: "spBv1.0/plantA/STATE/scada1"},
			{
				Topic: "events/orders",
				UserProps: map[string]string{
					"specversion": cloudevents.SpecVersion,
					"id":          "evt-1",
					"source":      "/apps/checkout",
					"type":        "com.acme.OrderCreated",
				},
				ContentType: "application/json",
				Payload:     []byte(`{"orderId":"SECRET-XYZ"}`),
			},
		},
	}

	s := New()
	s.cfg = config{brokerRef: "broker-1:8883", host: "broker-1:8883"}
	s.obs = &observer{brokerRef: "broker-1:8883"}
	s.newClient = func(config) (mqttClient, error) { return fc, nil }

	sink := &capSink{}
	if err := s.Gather(ctx, sink); err != context.Canceled {
		t.Fatalf("Gather should end with context.Canceled, got %v", err)
	}
	if !fc.closed {
		t.Error("Gather must Close the client")
	}

	edges := sink.edges()
	if len(edges) == 0 {
		t.Fatal("no edges emitted — the broker simulation produced no observations")
	}
	for _, e := range edges {
		if e.Source != "mqtt" {
			t.Fatalf("edge with wrong signal source: %q", e.Source)
		}
		for _, leak := range []string{"PROTO-NODE-BIRTH", "PROTO-DEVICE-DATA", "42.7psi", "SECRET-XYZ"} {
			if strings.Contains(edgeKey(e), leak) || strings.Contains(e.ToolRef, leak) {
				t.Fatalf("payload content %q leaked into an edge: %+v", leak, e)
			}
		}
	}

	want := []string{
		// Sparkplug node-level (NBIRTH)
		"sparkplug.group|plantA|sparkplug.edge_node|edge7",
		"sparkplug.edge_node|edge7|mqtt.broker|broker-1:8883",
		// Sparkplug device-level (DDATA)
		"sparkplug.edge_node|edge7|sparkplug.device|press01",
		"sparkplug.device|press01|mqtt.broker|broker-1:8883",
		// STATE certificate
		"sparkplug.edge_node|scada1|mqtt.broker|broker-1:8883",
		// generic topic + CloudEvents producer
		"mqtt.broker|broker-1:8883|mqtt.topic|events/orders",
		"identity|/apps/checkout|mqtt.topic|events/orders",
	}
	for _, k := range want {
		if !hasEdge(edges, k) {
			t.Errorf("expected edge not emitted: %s", k)
		}
	}
}

// TestLoadConfigValidation checks the required-broker rule, the URL/scheme parse,
// and the defaults (client id, topics, keepalive, broker ref).
func TestLoadConfigValidation(t *testing.T) {
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("missing broker should error")
	}
	if _, err := loadConfig(sdk.Config{Settings: map[string]string{"broker": "ftp://x:1"}}); err == nil {
		t.Fatal("unsupported broker scheme should error")
	}

	c, err := loadConfig(sdk.Config{Settings: map[string]string{"broker": "tcp://mqtt.example:1883"}})
	if err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
	if c.host != "mqtt.example:1883" {
		t.Fatalf("host = %q", c.host)
	}
	if c.clientID != defaultClientID {
		t.Fatalf("client id default = %q", c.clientID)
	}
	if len(c.topics) != 1 || c.topics[0] != defaultTopic {
		t.Fatalf("topics default = %v", c.topics)
	}
	if c.keepalive != defaultKeepalive {
		t.Fatalf("keepalive default = %v", c.keepalive)
	}
	if c.brokerRef != "mqtt.example:1883" {
		t.Fatalf("broker ref default = %q", c.brokerRef)
	}
	if c.useTLS {
		t.Fatal("tcp:// broker must not enable TLS")
	}
}

// TestLoadConfigTLSScheme checks a tls:// broker enables TLS, defaults the port to
// 8883, and that a bare host:port is accepted as tcp.
func TestLoadConfigTLSScheme(t *testing.T) {
	c, err := loadConfig(sdk.Config{Settings: map[string]string{
		"broker": "tls://iot.example", "topics": "spBv1.0/#, factory/+/temp",
	}})
	if err != nil {
		t.Fatalf("tls broker errored: %v", err)
	}
	if !c.useTLS || c.tls == nil {
		t.Fatal("tls:// broker must enable TLS")
	}
	if c.host != "iot.example:8883" {
		t.Fatalf("tls default port not applied: %q", c.host)
	}
	if len(c.topics) != 2 {
		t.Fatalf("topics csv = %v", c.topics)
	}

	bare, err := loadConfig(sdk.Config{Settings: map[string]string{"broker": "host:1883"}})
	if err != nil {
		t.Fatalf("bare host:port errored: %v", err)
	}
	if bare.host != "host:1883" || bare.useTLS {
		t.Fatalf("bare host:port = %q tls=%v", bare.host, bare.useTLS)
	}
}

func TestParseBrokerIPv6Defaults(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantHost   string
		wantScheme string
	}{
		{name: "tcp bare compressed", raw: "tcp://[2001:db8::1]", wantHost: "[2001:db8::1]:1883", wantScheme: "tcp"},
		{name: "tcp bracketed port", raw: "tcp://[2001:db8::1]:443", wantHost: "[2001:db8::1]:443", wantScheme: "tcp"},
		{name: "tls loopback compressed", raw: "tls://[::1]", wantHost: "[::1]:8883", wantScheme: "tls"},
		{name: "tcp link local zone", raw: "tcp://[fe80::1%25eth0]", wantHost: "[fe80::1%eth0]:1883", wantScheme: "tcp"},
		{name: "tcp v4 mapped", raw: "tcp://[::ffff:192.0.2.1]", wantHost: "[::ffff:192.0.2.1]:1883", wantScheme: "tcp"},
		{name: "hostname default unchanged", raw: "tcp://mqtt.example", wantHost: "mqtt.example:1883", wantScheme: "tcp"},
		{name: "hostname port unchanged", raw: "tcp://mqtt.example:1884", wantHost: "mqtt.example:1884", wantScheme: "tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, scheme, err := parseBroker(tt.raw)
			if err != nil {
				t.Fatalf("parseBroker(%q): %v", tt.raw, err)
			}
			if host != tt.wantHost || scheme != tt.wantScheme {
				t.Fatalf("parseBroker(%q) = %q/%q, want %q/%q", tt.raw, host, scheme, tt.wantHost, tt.wantScheme)
			}
		})
	}
}

// TestDescriptor checks the descriptor identity and that the password field is
// declared Secret (a credential is never inlined into the descriptor).
func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != "olivares.mqtt" || d.Type != sdk.TypeSource {
		t.Fatalf("descriptor identity wrong: %q %q", d.Name, d.Type)
	}
	var sawSecret bool
	for _, f := range d.ConfigFields {
		if f.Key == "password" {
			sawSecret = f.Secret
		}
	}
	if !sawSecret {
		t.Fatal("password config field must be declared Secret")
	}
}

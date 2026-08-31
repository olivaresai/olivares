// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoy

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	aldata "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

const (
	testFixtureSecret = "AKIAIOSFODNN7EXAMPLE" // an AWS access key the redactor recognizes
	testTraceParent   = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	extProcBody       = "prompt smuggling a key " + testFixtureSecret + " out"
)

func testTime() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }

type capturingSink struct{ obs []model.Observation }

func (c *capturingSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

func (c *capturingSink) edges() []model.EdgeObservation {
	var e []model.EdgeObservation
	for _, o := range c.obs {
		if ed, ok := o.(model.EdgeObservation); ok {
			e = append(e, ed)
		}
	}
	return e
}

func (c *capturingSink) findings() []model.FindingReport {
	var f []model.FindingReport
	for _, o := range c.obs {
		if fr, ok := o.(model.FindingReport); ok {
			f = append(f, fr)
		}
	}
	return f
}

func newServer(sink sdk.Sink) *obsServer { return &obsServer{sink: sink, now: testTime} }

// --- ALS -------------------------------------------------------------------

type fakeALSStream struct {
	grpc.ServerStream
	msgs   []*accesslogv3.StreamAccessLogsMessage
	idx    int
	closed bool
}

func (f *fakeALSStream) Recv() (*accesslogv3.StreamAccessLogsMessage, error) {
	if f.idx >= len(f.msgs) {
		return nil, io.EOF
	}
	m := f.msgs[f.idx]
	f.idx++
	return m, nil
}
func (f *fakeALSStream) SendAndClose(*accesslogv3.StreamAccessLogsResponse) error {
	f.closed = true
	return nil
}
func (f *fakeALSStream) Context() context.Context { return context.Background() }

func httpLogMsg(entry *aldata.HTTPAccessLogEntry) *accesslogv3.StreamAccessLogsMessage {
	return &accesslogv3.StreamAccessLogsMessage{
		LogEntries: &accesslogv3.StreamAccessLogsMessage_HttpLogs{
			HttpLogs: &accesslogv3.StreamAccessLogsMessage_HTTPAccessLogEntries{
				LogEntry: []*aldata.HTTPAccessLogEntry{entry},
			},
		},
	}
}

func TestALSHTTPEdgeFromDownstreamAddr(t *testing.T) {
	sink := &capturingSink{}
	entry := &aldata.HTTPAccessLogEntry{
		CommonProperties: &aldata.AccessLogCommon{
			StartTime: timestamppb.New(testTime()),
			DownstreamRemoteAddress: &corev3.Address{Address: &corev3.Address_SocketAddress{
				SocketAddress: &corev3.SocketAddress{Address: "10.0.0.5", PortSpecifier: &corev3.SocketAddress_PortValue{PortValue: 54321}},
			}},
		},
		Request: &aldata.HTTPRequestProperties{RequestMethod: corev3.RequestMethod_GET, Authority: "api.anthropic.com"},
	}
	stream := &fakeALSStream{msgs: []*accesslogv3.StreamAccessLogsMessage{httpLogMsg(entry)}}
	if err := newServer(sink).StreamAccessLogs(stream); err != nil {
		t.Fatal(err)
	}
	if !stream.closed {
		t.Fatal("stream not acknowledged with SendAndClose")
	}
	e := sink.edges()
	if len(e) != 1 {
		t.Fatalf("want 1 edge, got %d", len(e))
	}
	if e[0].ResourceKind != "http.api" || e[0].ResourceRef != "api.anthropic.com" {
		t.Errorf("resource = %s/%s", e[0].ResourceKind, e[0].ResourceRef)
	}
	if e[0].Mode != model.ModeRead {
		t.Errorf("Mode = %s, want read (GET)", e[0].Mode)
	}
	if e[0].Source != SignalEnvoyALS {
		t.Errorf("Source = %s", e[0].Source)
	}
	if e[0].OriginRef != "10.0.0.5" || e[0].Confidence != model.ConfidenceApproximate {
		t.Errorf("origin/confidence = %s/%s, want 10.0.0.5/approximate", e[0].OriginRef, e[0].Confidence)
	}
	if !e[0].ObservedAt.Equal(testTime()) {
		t.Errorf("ObservedAt = %v", e[0].ObservedAt)
	}
}

func TestALSVerifiedIdentityFromPeerSAN(t *testing.T) {
	sink := &capturingSink{}
	entry := &aldata.HTTPAccessLogEntry{
		CommonProperties: &aldata.AccessLogCommon{
			StartTime: timestamppb.New(testTime()),
			TlsProperties: &aldata.TLSProperties{
				PeerCertificateProperties: &aldata.TLSProperties_CertificateProperties{
					SubjectAltName: []*aldata.TLSProperties_CertificateProperties_SubjectAltName{
						{San: &aldata.TLSProperties_CertificateProperties_SubjectAltName_Uri{Uri: "spiffe://c/ns/default/sa/payments"}},
					},
				},
			},
		},
		Request: &aldata.HTTPRequestProperties{RequestMethod: corev3.RequestMethod_POST, Authority: "vault.internal"},
	}
	if err := newServer(sink).StreamAccessLogs(&fakeALSStream{msgs: []*accesslogv3.StreamAccessLogsMessage{httpLogMsg(entry)}}); err != nil {
		t.Fatal(err)
	}
	e := sink.edges()
	if len(e) != 1 {
		t.Fatalf("want 1 edge, got %d", len(e))
	}
	if e[0].OriginRef != "spiffe://c/ns/default/sa/payments" || e[0].Confidence != model.ConfidenceAttributed {
		t.Errorf("verified identity not used: %s/%s", e[0].OriginRef, e[0].Confidence)
	}
	if e[0].Mode != model.ModeReadWrite {
		t.Errorf("Mode = %s, want readwrite (POST)", e[0].Mode)
	}
}

// --- ext_authz -------------------------------------------------------------

func TestExtAuthzAlwaysAllowsAndObserves(t *testing.T) {
	sink := &capturingSink{}
	req := &authv3.CheckRequest{Attributes: &authv3.AttributeContext{
		Source: &authv3.AttributeContext_Peer{Principal: "spiffe://c/ns/default/sa/web"},
		Request: &authv3.AttributeContext_Request{Http: &authv3.AttributeContext_HttpRequest{
			Method:  "GET",
			Host:    "db.svc.cluster.local",
			Path:    "/v1/query",
			Headers: map[string]string{"traceparent": testTraceParent},
		}},
	}}
	resp, err := newServer(sink).Check(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Read-first invariant: ALWAYS OK, NEVER denied.
	if resp.GetStatus().GetCode() != 0 {
		t.Errorf("status code = %d, want 0 (OK)", resp.GetStatus().GetCode())
	}
	if resp.GetOkResponse() == nil {
		t.Error("expected an OkResponse")
	}
	if resp.GetDeniedResponse() != nil {
		t.Error("ext_authz must NEVER return a DeniedResponse (read-first)")
	}
	e := sink.edges()
	if len(e) != 1 {
		t.Fatalf("want 1 observed edge, got %d", len(e))
	}
	if e[0].OriginRef != "spiffe://c/ns/default/sa/web" || e[0].Confidence != model.ConfidenceAttributed {
		t.Errorf("identity = %s/%s", e[0].OriginRef, e[0].Confidence)
	}
	if e[0].ResourceRef != "db.svc.cluster.local" || e[0].Mode != model.ModeRead || e[0].Source != SignalEnvoyExtAuthz {
		t.Errorf("edge = %+v", e[0])
	}
}

// --- ext_proc --------------------------------------------------------------

type fakeProcStream struct {
	grpc.ServerStream
	reqs  []*extprocv3.ProcessingRequest
	idx   int
	sends []*extprocv3.ProcessingResponse
}

func (f *fakeProcStream) Recv() (*extprocv3.ProcessingRequest, error) {
	if f.idx >= len(f.reqs) {
		return nil, io.EOF
	}
	r := f.reqs[f.idx]
	f.idx++
	return r, nil
}
func (f *fakeProcStream) Send(r *extprocv3.ProcessingResponse) error {
	f.sends = append(f.sends, r)
	return nil
}
func (f *fakeProcStream) Context() context.Context { return context.Background() }

func hdr(k, v string) *corev3.HeaderValue { return &corev3.HeaderValue{Key: k, Value: v} }

func TestExtProcContinuesAndDetectsSecretWithoutLeaking(t *testing.T) {
	sink := &capturingSink{}
	stream := &fakeProcStream{reqs: []*extprocv3.ProcessingRequest{
		{Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
				hdr(":authority", "api.anthropic.com"), hdr(":method", "POST"), hdr("traceparent", testTraceParent),
			}},
		}}},
		{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{
			Body: []byte(extProcBody),
		}}},
	}}
	if err := newServer(sink).Process(stream); err != nil {
		t.Fatal(err)
	}

	// Every response must be CONTINUE with NO mutation (read-first, never rewrite).
	if len(stream.sends) != 2 {
		t.Fatalf("want 2 responses, got %d", len(stream.sends))
	}
	if got := stream.sends[0].GetRequestHeaders(); got == nil || got.GetResponse().GetStatus() != extprocv3.CommonResponse_CONTINUE {
		t.Errorf("headers response not CONTINUE: %+v", stream.sends[0])
	}
	if stream.sends[0].GetRequestHeaders().GetResponse().GetHeaderMutation() != nil {
		t.Error("ext_proc must not mutate headers (read-first)")
	}
	if got := stream.sends[1].GetRequestBody(); got == nil || got.GetResponse().GetStatus() != extprocv3.CommonResponse_CONTINUE {
		t.Errorf("body response not CONTINUE: %+v", stream.sends[1])
	}
	if stream.sends[1].GetRequestBody().GetResponse().GetBodyMutation() != nil {
		t.Error("ext_proc must not mutate body (read-first)")
	}

	// One observed edge from the headers phase, one sensitive-egress finding from body.
	if len(sink.edges()) != 1 {
		t.Fatalf("want 1 edge, got %d", len(sink.edges()))
	}
	if sink.edges()[0].ResourceRef != "api.anthropic.com" || sink.edges()[0].Source != SignalEnvoyExtProc {
		t.Errorf("edge = %+v", sink.edges()[0])
	}
	f := sink.findings()
	if len(f) != 1 {
		t.Fatalf("want 1 finding, got %d", len(f))
	}
	if f[0].Kind != "sensitive_egress" || f[0].Severity != model.SeverityHigh {
		t.Errorf("finding = %+v", f[0])
	}
	if len(f[0].OWASPLLM) != 1 || f[0].OWASPLLM[0] != "LLM02:2025" {
		t.Errorf("taxonomy = %v, want [LLM02:2025]", f[0].OWASPLLM)
	}

	// The raw secret must NEVER appear in any emitted observation (docs/SECURITY-HARDENING.md).
	blob, _ := json.Marshal(sink.obs)
	if strings.Contains(string(blob), testFixtureSecret) {
		t.Fatalf("raw secret leaked into emitted observations: %s", blob)
	}

	// Scrub-BEFORE-hash, not merely "a hash can't contain plaintext": the DetailHash
	// must be the hash of the REDACTED body. If a regression hashed the raw body, the
	// first assertion would still pass (a hash hides the secret) but these would fail.
	if f[0].DetailHash != redact.Hash(redact.Clean(extProcBody)) {
		t.Error("DetailHash is not the hash of the redacted body")
	}
	if f[0].DetailHash == redact.Hash(extProcBody) {
		t.Error("DetailHash is the hash of the RAW body — redaction was skipped before hashing")
	}
}

func TestExtProcCleanBodyEmitsNoFinding(t *testing.T) {
	sink := &capturingSink{}
	stream := &fakeProcStream{reqs: []*extprocv3.ProcessingRequest{
		{Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{hdr(":authority", "x.example"), hdr(":method", "GET")}},
		}}},
		{Request: &extprocv3.ProcessingRequest_RequestBody{RequestBody: &extprocv3.HttpBody{Body: []byte("hello, no secrets here")}}},
	}}
	if err := newServer(sink).Process(stream); err != nil {
		t.Fatal(err)
	}
	if len(sink.findings()) != 0 {
		t.Fatalf("clean body must produce no finding, got %d", len(sink.findings()))
	}
}

// --- bind guard & helpers --------------------------------------------------

func TestOpenRefusesNonLoopbackBind(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"listen_addr": "0.0.0.0:0"}})
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("expected refusal of a non-loopback bind (secure default)")
	}
	if !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Loopback ephemeral bind is accepted.
	s2 := New()
	if err := s2.Open(context.Background(), sdk.Config{Settings: map[string]string{"listen_addr": "127.0.0.1:0"}}); err != nil {
		t.Fatalf("loopback bind should be accepted: %v", err)
	}
	if err := s2.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenUnknownServiceRejected(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"listen_addr": "127.0.0.1:0", "services": "als,bogus"}})
	if err == nil || !strings.Contains(err.Error(), "unknown service") {
		_ = s.Close(context.Background())
		t.Fatalf("expected unknown-service error, got %v", err)
	}
}

// The classifier this connector used to carry privately now lives in the
// product's single admission point. The case table is kept HERE, pointed
// at the shared implementation, so the migration is proven to preserve the
// answers this connector depended on rather than merely to compile.
func TestHostIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:5557": true,
		"[::1]:80":       true,
		"localhost:80":   true,
		"0.0.0.0:80":     false,
		":80":            false,
		"10.0.0.1:80":    false,
		"garbage":        false,
	}
	for addr, want := range cases {
		if got := netbind.IsLoopback(addr); got != want {
			t.Errorf("netbind.IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

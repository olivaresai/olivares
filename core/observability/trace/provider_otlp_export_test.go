// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// These tests export through the real OTLP exporters to loopback receivers and decode what
// arrives, pinning the Config.Endpoint contract across exporter versions:
// go.opentelemetry.io/otel v1.45.0 stopped appending signal paths in WithEndpointURL.

const (
	exportTestSpan    = "otlp-endpoint-test-span"
	exportTestMetric  = "otlp_endpoint_test_counter"
	exportTestService = "otlp-endpoint-test"

	// The OTLP/HTTP signal paths, stated independently of the implementation under test.
	wantTracesPath  = "/v1/traces"
	wantMetricsPath = "/v1/metrics"
)

// otlpRequest is one request a loopback receiver saw.
type otlpRequest struct {
	path    string // HTTP path; empty for gRPC
	tls     bool
	status  int
	spans   []string
	metrics []string
	service string
	body    []byte
}

// otlpReceiver routes like an OTLP collector: POST /v1/traces and /v1/metrics are decoded and
// any other path is answered 404, unless acceptPath names one explicit path to record.
type otlpReceiver struct {
	acceptPath string

	mu   sync.Mutex
	reqs []otlpRequest
}

func (r *otlpReceiver) add(q otlpRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, q)
}

func (r *otlpReceiver) requests() []otlpRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]otlpRequest(nil), r.reqs...)
}

func (r *otlpReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	q := otlpRequest{path: req.URL.Path, tls: req.TLS != nil, body: body}
	switch {
	case r.acceptPath != "" && req.URL.Path == r.acceptPath:
		q.status = http.StatusOK
	case req.Method == http.MethodPost && req.URL.Path == wantTracesPath:
		var msg coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &msg); err != nil {
			q.status = http.StatusBadRequest
			break
		}
		q.spans, q.service = traceNames(&msg)
		q.status = http.StatusOK
	case req.Method == http.MethodPost && req.URL.Path == wantMetricsPath:
		var msg colmetricpb.ExportMetricsServiceRequest
		if err := proto.Unmarshal(body, &msg); err != nil {
			q.status = http.StatusBadRequest
			break
		}
		q.metrics, q.service = metricNames(&msg)
		q.status = http.StatusOK
	default:
		q.status = http.StatusNotFound
	}
	r.add(q)
	if q.status != http.StatusOK {
		http.Error(w, http.StatusText(q.status), q.status)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

type grpcTraceReceiver struct {
	coltracepb.UnimplementedTraceServiceServer
	r *otlpReceiver
}

func (s grpcTraceReceiver) Export(_ context.Context, msg *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	spans, service := traceNames(msg)
	s.r.add(otlpRequest{status: http.StatusOK, spans: spans, service: service})
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type grpcMetricReceiver struct {
	colmetricpb.UnimplementedMetricsServiceServer
	r *otlpReceiver
}

func (s grpcMetricReceiver) Export(_ context.Context, msg *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	metrics, service := metricNames(msg)
	s.r.add(otlpRequest{status: http.StatusOK, metrics: metrics, service: service})
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func traceNames(msg *coltracepb.ExportTraceServiceRequest) (spans []string, service string) {
	for _, rs := range msg.GetResourceSpans() {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				service = kv.GetValue().GetStringValue()
			}
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				spans = append(spans, s.GetName())
			}
		}
	}
	return spans, service
}

func metricNames(msg *colmetricpb.ExportMetricsServiceRequest) (metrics []string, service string) {
	for _, rm := range msg.GetResourceMetrics() {
		for _, kv := range rm.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				service = kv.GetValue().GetStringValue()
			}
		}
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				metrics = append(metrics, m.GetName())
			}
		}
	}
	return metrics, service
}

// clearOTLPEnv isolates a test from exporter configuration inherited from the environment.
func clearOTLPEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_INSECURE", "OTEL_EXPORTER_OTLP_CERTIFICATE",
		"OLIVARES_OTEL_ENDPOINT", "OLIVARES_OTEL_PROTOCOL", "OLIVARES_OTEL_INSECURE", "OLIVARES_OTEL_ENABLED",
		"OLIVARES_OTEL_SAMPLE_RATIO", "OLIVARES_OTEL_SERVICE_NAME",
	} {
		t.Setenv(k, "")
	}
}

// exportOnce records one span and one counter through a Provider built from cfg and flushes
// both exporters with Shutdown, returning the Shutdown error.
func exportOnce(t *testing.T, cfg Config) error {
	t.Helper()
	ctx := context.Background()
	p, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.Enabled() {
		t.Fatal("provider is not enabled")
	}
	_, span := p.tracer.Start(ctx, exportTestSpan)
	span.End()
	counter, err := p.mp.Meter(instrumentationName).Int64Counter(exportTestMetric)
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	counter.Add(ctx, 1)
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return p.Shutdown(shutdownCtx)
}

// requireBothSignals asserts the receiver decoded the test span at the trace path and the test
// metric at the metric path, with the configured service name and transport security.
func requireBothSignals(t *testing.T, r *otlpReceiver, wantTLS bool) {
	t.Helper()
	var gotSpan, gotMetric bool
	for _, q := range r.requests() {
		if q.status != http.StatusOK {
			t.Errorf("receiver answered %d for path %q", q.status, q.path)
			continue
		}
		if q.tls != wantTLS {
			t.Errorf("request to %q used TLS=%v, want %v", q.path, q.tls, wantTLS)
		}
		if contains(q.spans, exportTestSpan) && q.service == exportTestService {
			gotSpan = true
		}
		if contains(q.metrics, exportTestMetric) && q.service == exportTestService {
			gotMetric = true
		}
	}
	if !gotSpan {
		t.Errorf("no exported span %q reached %s", exportTestSpan, wantTracesPath)
	}
	if !gotMetric {
		t.Errorf("no exported metric %q reached %s", exportTestMetric, wantMetricsPath)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func httpConfig(endpoint string, insecure bool) Config {
	return Config{Enabled: true, Endpoint: endpoint, Protocol: ProtocolHTTP, Insecure: insecure,
		SampleRatio: 1, ServiceName: exportTestService, ServiceVersion: "test"}
}

func TestOTLPHTTPBaseURLExportsBothSignals(t *testing.T) {
	clearOTLPEnv(t)
	cases := []struct {
		name     string
		endpoint func(srv *httptest.Server) string
		insecure bool
		ipv6     bool
	}{
		{name: "url without path", endpoint: func(s *httptest.Server) string { return s.URL }},
		{name: "url with root path", endpoint: func(s *httptest.Server) string { return s.URL + "/" }},
		{name: "host:port insecure", endpoint: func(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }, insecure: true},
		{name: "ipv6 url without path", endpoint: func(s *httptest.Server) string { return s.URL }, ipv6: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &otlpReceiver{}
			srv := httptest.NewUnstartedServer(rec)
			if tc.ipv6 {
				ln, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				srv.Listener.Close()
				srv.Listener = ln
			}
			srv.Start()
			defer srv.Close()
			if err := exportOnce(t, httpConfig(tc.endpoint(srv), tc.insecure)); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
			requireBothSignals(t, rec, false)
		})
	}
}

// A URL with a non-root path keeps its existing meaning: both signals are sent to that exact
// path. Config has no per-signal endpoints yet.
func TestOTLPHTTPExplicitPathIsUnchanged(t *testing.T) {
	clearOTLPEnv(t)
	const explicit = "/collector/otlp"
	rec := &otlpReceiver{acceptPath: explicit}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	if err := exportOnce(t, httpConfig(srv.URL+explicit, false)); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	var spanBody, metricBody bool
	for _, q := range rec.requests() {
		if q.path != explicit {
			t.Errorf("request reached %q, want only %q", q.path, explicit)
		}
		spanBody = spanBody || bytes.Contains(q.body, []byte(exportTestSpan))
		metricBody = metricBody || bytes.Contains(q.body, []byte(exportTestMetric))
	}
	if !spanBody || !metricBody {
		t.Errorf("explicit path received span=%v metric=%v, want both", spanBody, metricBody)
	}
}

func TestOTLPHTTPSURLKeepsTLS(t *testing.T) {
	clearOTLPEnv(t)
	rec := &otlpReceiver{}
	srv := httptest.NewTLSServer(rec)
	defer srv.Close()
	// Trust the test certificate through the standard exporter variable; Config has no TLS setting.
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", caFile)
	if err := exportOnce(t, httpConfig(srv.URL, false)); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	requireBothSignals(t, rec, true)
}

func TestOTLPEndpointPrecedenceFromEnv(t *testing.T) {
	t.Run("product variables win", func(t *testing.T) {
		clearOTLPEnv(t)
		product, standard := &otlpReceiver{}, &otlpReceiver{}
		productSrv, standardSrv := httptest.NewServer(product), httptest.NewServer(standard)
		defer productSrv.Close()
		defer standardSrv.Close()
		t.Setenv("OLIVARES_OTEL_ENDPOINT", productSrv.URL)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", standardSrv.URL)
		t.Setenv("OLIVARES_OTEL_PROTOCOL", "http/protobuf")
		t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
		t.Setenv("OLIVARES_OTEL_SERVICE_NAME", exportTestService)
		cfg := FromEnv("test")
		if cfg.Endpoint != productSrv.URL || cfg.Protocol != ProtocolHTTP {
			t.Fatalf("FromEnv endpoint/protocol = %q/%q, want the product values", cfg.Endpoint, cfg.Protocol)
		}
		if err := exportOnce(t, cfg); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		requireBothSignals(t, product, false)
		if n := len(standard.requests()); n != 0 {
			t.Errorf("standard endpoint received %d requests, want 0", n)
		}
	})
	t.Run("standard base URL", func(t *testing.T) {
		clearOTLPEnv(t)
		standard := &otlpReceiver{}
		srv := httptest.NewServer(standard)
		defer srv.Close()
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
		t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
		t.Setenv("OLIVARES_OTEL_SERVICE_NAME", exportTestService)
		if err := exportOnce(t, FromEnv("test")); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		requireBothSignals(t, standard, false)
	})
}

func TestOTLPGRPCEndpointsUnchanged(t *testing.T) {
	clearOTLPEnv(t)
	for _, form := range []string{"host:port insecure", "http url"} {
		t.Run(form, func(t *testing.T) {
			rec := &otlpReceiver{}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			gs := grpc.NewServer()
			coltracepb.RegisterTraceServiceServer(gs, grpcTraceReceiver{r: rec})
			colmetricpb.RegisterMetricsServiceServer(gs, grpcMetricReceiver{r: rec})
			go func() { _ = gs.Serve(ln) }()
			defer gs.Stop()
			cfg := Config{Enabled: true, Protocol: ProtocolGRPC, SampleRatio: 1, ServiceName: exportTestService}
			if form == "http url" {
				cfg.Endpoint = "http://" + ln.Addr().String()
			} else {
				cfg.Endpoint, cfg.Insecure = ln.Addr().String(), true
			}
			if err := exportOnce(t, cfg); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
			var gotSpan, gotMetric bool
			for _, q := range rec.requests() {
				gotSpan = gotSpan || (contains(q.spans, exportTestSpan) && q.service == exportTestService)
				gotMetric = gotMetric || (contains(q.metrics, exportTestMetric) && q.service == exportTestService)
			}
			if !gotSpan || !gotMetric {
				t.Errorf("gRPC receiver got span=%v metric=%v, want both", gotSpan, gotMetric)
			}
		})
	}
}

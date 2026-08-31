// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

var testNow = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

type fakeSink struct {
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, obs model.Observation) error {
	f.obs = append(f.obs, obs)
	return nil
}

func (f *fakeSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, obs := range f.obs {
		if v, ok := obs.(model.FindingReport); ok {
			out = append(out, v)
		}
	}
	return out
}

func (f *fakeSink) edges() []model.EdgeObservation {
	var out []model.EdgeObservation
	for _, obs := range f.obs {
		if v, ok := obs.(model.EdgeObservation); ok {
			out = append(out, v)
		}
	}
	return out
}

func (f *fakeSink) metrics() []model.MetricSample {
	var out []model.MetricSample
	for _, obs := range f.obs {
		if v, ok := obs.(model.MetricSample); ok {
			out = append(out, v)
		}
	}
	return out
}

func newTestSource(t *testing.T, settings map[string]string) *Source {
	t.Helper()
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.now = func() time.Time { return testNow }
	return s
}

func gather(t *testing.T, settings map[string]string) *fakeSink {
	t.Helper()
	s := newTestSource(t, settings)
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink
}

func fixture(name string) string {
	return filepath.Join("testdata", name)
}

func TestOpenRequiresSurface(t *testing.T) {
	if err := New().Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err == nil {
		t.Fatal("Open with no configured surface succeeded")
	}
}

func TestMinimalGatewayInventoryAndPosture(t *testing.T) {
	sink := gather(t, map[string]string{cfgConfigPath: fixture("gateway-minimal.yaml")})

	requireEdge(t, sink.edges(), "gateway", "https://gateway.example.com", "idp.issuer", "https://idp.example.com/oauth", model.SignalConfig)
	requireEdge(t, sink.edges(), "gateway", "https://gateway.example.com", "inference.upstream", "anthropic", model.SignalConfig)
	if countEdges(sink.edges(), "otlp.destination", "") != 0 {
		t.Fatalf("minimal fixture should not emit OTLP destination edges")
	}

	kinds := findingKinds(sink.findings())
	for _, want := range []string{"gateway_no_otlp", "gateway_no_spend_limits", "gateway_unbounded_default"} {
		if kinds[want] != 1 {
			t.Fatalf("finding %s count = %d, want 1; all=%v", want, kinds[want], kinds)
		}
	}
	if kinds["gateway_no_domain_gate"] != 0 {
		t.Fatalf("gateway_no_domain_gate should be absent when allowed_email_domains is set: %v", kinds)
	}
}

func TestConfigUnreadableFinding(t *testing.T) {
	sink := gather(t, map[string]string{cfgConfigPath: fixture("missing-gateway.yaml")})
	if findingKinds(sink.findings())["gateway_config_unreadable"] != 1 {
		t.Fatalf("gateway_config_unreadable not emitted once: %+v", sink.findings())
	}
}

func TestNoDomainGateFinding(t *testing.T) {
	sink := gather(t, map[string]string{cfgConfigPath: fixture("gateway-no-domain.yaml")})
	if findingKinds(sink.findings())["gateway_no_domain_gate"] != 1 {
		t.Fatalf("gateway_no_domain_gate not emitted once: %+v", sink.findings())
	}
}

func TestFullGatewayPostureAndPolicyEdges(t *testing.T) {
	sink := gather(t, map[string]string{cfgConfigPath: fixture("gateway-full.yaml")})
	kinds := findingKinds(sink.findings())
	wantExactlyOnce := []string{
		"gateway_spend_fail_open",
		"gateway_single_issuer_multidomain",
		"gateway_long_session_ttl",
		"gateway_pkce_disabled",
		"gateway_sensitive_signals",
		"gateway_secret_literal",
		"gateway_deprecated_settings_alias",
	}
	for _, want := range wantExactlyOnce {
		if kinds[want] != 1 {
			t.Fatalf("finding %s count = %d, want 1; all=%v", want, kinds[want], kinds)
		}
	}
	for _, absent := range []string{"gateway_no_otlp", "gateway_unbounded_default", "gateway_no_spend_limits", "gateway_no_domain_gate"} {
		if kinds[absent] != 0 {
			t.Fatalf("finding %s should be absent, got %d; all=%v", absent, kinds[absent], kinds)
		}
	}

	requireEdge(t, sink.edges(), "idp.group", "engineering", "model", "claude-sonnet-4-20250514", model.SignalPolicy)
	requireEdge(t, sink.edges(), "idp.group", "engineering", "model", "claude-opus-4-20250514", model.SignalPolicy)
	requireEdge(t, sink.edges(), "idp.group", "*", "model", "claude-haiku-3-5-20241022", model.SignalPolicy)
	requireEdge(t, sink.edges(), "gateway", "https://gateway-full.example.com", "otlp.destination", "otel.example.com", model.SignalConfig)
}

func TestAuditEventMapping(t *testing.T) {
	sink := gather(t, map[string]string{
		cfgConfigPath:   fixture("gateway-minimal.yaml"),
		cfgAuditLogPath: fixture("audit-events.jsonl"),
	})

	kinds := findingKinds(sink.findings())
	for _, want := range []string{
		"gateway_evt_auth_denied",
		"gateway_evt_access_denied",
		"gateway_evt_spend_blocked",
		"gateway_evt_admin_denied",
		"gateway_audit_unparsed",
	} {
		if kinds[want] != 1 {
			t.Fatalf("finding %s count = %d, want 1; all=%v", want, kinds[want], kinds)
		}
	}
	requireEdge(t, sink.edges(), "gateway.principal", "sub-123", "inference.upstream", "anthropicAws", signalClaudeAppsGateway)
	requireEdge(t, sink.edges(), "gateway.principal", "sub-123", "gateway", "https://gateway.example.com", signalClaudeAppsGateway)

	counts := metricCounts(sink.metrics())
	for _, evt := range []string{"config.load", "session.refresh", "device.authorize", "device.verify", "managed.serve", "unknown"} {
		if counts[evt] != 1 {
			t.Fatalf("metric evt %s count = %d, want 1; all=%v", evt, counts[evt], counts)
		}
	}
}

func TestProbeUpEmitsProtocolMetric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"issuer":"https://gateway.example.com"}`))
		case "/protocol":
			_, _ = w.Write([]byte(`{"version":"2.1.199"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := gather(t, map[string]string{
		cfgConfigPath: fixture("gateway-minimal.yaml"),
		cfgEndpoint:   srv.URL,
	})
	if findingKinds(sink.findings())["gateway_probe_unreachable"] != 0 {
		t.Fatalf("probe unexpectedly unreachable: %+v", sink.findings())
	}
	if !hasMetricDimension(sink.metrics(), "endpoint", "protocol") {
		t.Fatalf("protocol probe metric missing: %+v", sink.metrics())
	}
}

func TestProbeUnreachableFinding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	sink := gather(t, map[string]string{cfgEndpoint: srv.URL})
	if findingKinds(sink.findings())["gateway_probe_unreachable"] != 1 {
		t.Fatalf("gateway_probe_unreachable not emitted once: %+v", sink.findings())
	}
}

func TestProbeIssuerMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(`{"issuer":"https://other.example.com"}`))
		case "/protocol":
			_, _ = w.Write([]byte(`{"version":"2.1.199"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := gather(t, map[string]string{
		cfgConfigPath: fixture("gateway-minimal.yaml"),
		cfgEndpoint:   srv.URL,
	})
	if findingKinds(sink.findings())["gateway_issuer_mismatch"] != 1 {
		t.Fatalf("gateway_issuer_mismatch not emitted once: %+v", sink.findings())
	}
}

func TestProbeVersionBelowMinimum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("x-cc-gateway-version", "2.1.197")
			_, _ = w.Write([]byte(`{"issuer":"https://gateway-full.example.com"}`))
		case "/protocol":
			w.Header().Set("x-cc-gateway-version", "2.1.197")
			_, _ = w.Write([]byte(`{"protocol":"llm-gateway"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := gather(t, map[string]string{
		cfgConfigPath: fixture("gateway-full.yaml"),
		cfgEndpoint:   srv.URL,
	})
	if findingKinds(sink.findings())["gateway_version_below_min"] != 1 {
		t.Fatalf("gateway_version_below_min not emitted once: %+v", sink.findings())
	}
}

func TestPolicyDrift(t *testing.T) {
	sink := gather(t, map[string]string{
		cfgConfigPath:           fixture("gateway-full.yaml"),
		cfgDeclaredSettingsPath: fixture("declared-settings.json"),
	})
	var found []string
	for _, f := range sink.findings() {
		if f.Kind == "policy_drift" {
			found = append(found, f.Title)
		}
	}
	if len(found) != 1 {
		t.Fatalf("policy_drift count = %d, want 1: %v", len(found), found)
	}
	if got, want := found[0], "policy drift keys: availableModels, env"; got != want {
		t.Fatalf("policy_drift Title = %q, want %q", got, want)
	}
}

func TestNoPromptLeaks(t *testing.T) {
	var all []model.Observation
	runs := []map[string]string{
		{cfgConfigPath: fixture("gateway-minimal.yaml")},
		{cfgConfigPath: fixture("gateway-full.yaml"), cfgDeclaredSettingsPath: fixture("declared-settings.json")},
		{cfgConfigPath: fixture("gateway-minimal.yaml"), cfgAuditLogPath: fixture("audit-events.jsonl")},
	}
	for _, settings := range runs {
		sink := gather(t, settings)
		all = append(all, sink.obs...)
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("marshal observations: %v", err)
	}
	body := string(encoded)
	for _, forbidden := range []string{
		"alice@example.com",
		"blocked@example.com",
		"finance@example.com",
		"admin@example.com",
		"future@example.com",
		"literal-client-secret",
		"telemetry-header-secret",
		"SECRET_PROMPT_SHOULD_NOT_LEAK",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("observation payload leaks %q: %s", forbidden, body)
		}
	}
}

func findingKinds(findings []model.FindingReport) map[string]int {
	out := map[string]int{}
	for _, f := range findings {
		out[f.Kind]++
	}
	return out
}

func metricCounts(metrics []model.MetricSample) map[string]int64 {
	out := map[string]int64{}
	for _, m := range metrics {
		if m.Name == "claude_apps_gateway.audit_events.count" {
			out[m.Dimensions["evt"]] += m.Value
		}
	}
	return out
}

func requireEdge(t *testing.T, edges []model.EdgeObservation, originKind, originRef, resourceKind, resourceRef string, source model.SignalSource) {
	t.Helper()
	for _, e := range edges {
		if e.OriginKind == originKind && e.OriginRef == originRef && e.ResourceKind == resourceKind && e.ResourceRef == resourceRef && e.Source == source {
			return
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].OriginKind+edges[i].OriginRef+edges[i].ResourceKind+edges[i].ResourceRef <
			edges[j].OriginKind+edges[j].OriginRef+edges[j].ResourceKind+edges[j].ResourceRef
	})
	t.Fatalf("missing edge %s/%s -> %s/%s source=%s; got=%+v", originKind, originRef, resourceKind, resourceRef, source, edges)
}

func countEdges(edges []model.EdgeObservation, resourceKind, resourceRef string) int {
	n := 0
	for _, e := range edges {
		if e.ResourceKind == resourceKind && (resourceRef == "" || e.ResourceRef == resourceRef) {
			n++
		}
	}
	return n
}

func hasMetricDimension(metrics []model.MetricSample, key, value string) bool {
	for _, m := range metrics {
		if m.Dimensions[key] == value {
			return true
		}
	}
	return false
}

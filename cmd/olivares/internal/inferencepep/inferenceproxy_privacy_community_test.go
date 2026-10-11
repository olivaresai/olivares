// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package inferencepep

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Community carries trace context through the dedicated proxy without delivering
// telemetry, even when an external collector is configured.
func TestDedicatedProxyCommunityNeverExportsOTel(t *testing.T) {
	const traceparent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	const requestCanary = "REQUEST-COMMUNITY-PRIVACY-CANARY"
	const responseCanary = "RESPONSE-COMMUNITY-PRIVACY-CANARY"
	var collectorRequests atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		collectorRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("traceparent"); got != traceparent {
			t.Errorf("upstream traceparent = %q, want %q", got, traceparent)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		if !strings.Contains(string(body), requestCanary) {
			t.Error("proxy lost request content")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_privacy","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"`+responseCanary+`"}]}`)
	}))
	t.Cleanup(upstream.Close)
	tracer, err := obstrace.New(context.Background(), obstrace.Config{
		Enabled: true, Endpoint: collector.URL, Protocol: obstrace.ProtocolHTTP,
		SampleRatio: 1, ServiceName: "proxy-community-privacy-test", ServiceVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracer.Shutdown(ctx); err != nil {
			t.Errorf("shutdown tracing: %v", err)
		}
		if got := collectorRequests.Load(); got != 0 {
			t.Errorf("Community contacted the collector %d times", got)
		}
	})
	if tracer.Enabled() || tracer.Settings().Enabled {
		t.Fatal("Community enabled external telemetry delivery")
	}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		BaseURL: upstream.URL, APIKey: "operator-key", Gateway: sdkmodel.GatewayDirect,
		Doer: tracer.AnthropicHTTPClient(nil),
	})
	proxy := claudeapi.NewMessagesProxy(inf, privacyTraceDecider{}, nil, time.Now)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"`+requestCanary+`"}]}`))
	req.Header.Set("traceparent", traceparent)
	req.Header.Set("Authorization", "Bearer COMMUNITY-BEARER-CANARY")
	rec := httptest.NewRecorder()
	tracer.HTTPMiddleware(proxy).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), responseCanary) {
		t.Fatalf("proxy response = %d %s", rec.Code, rec.Body.String())
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestCommunitySessionTraceLinkPreservesPropagation(t *testing.T) {
	ctx := context.Background()
	var exports atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exports.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	p, err := obstrace.New(ctx, obstrace.Config{
		Enabled: true, Endpoint: collector.URL, Protocol: obstrace.ProtocolHTTP,
		Insecure: true, SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("trace provider: %v", err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("shutdown trace provider: %v", err)
		}
	})
	if p.Enabled() || p.Settings().Enabled {
		t.Fatal("Community enabled recording for a configured collector")
	}
	agent := p.InvokeAgent("claude", "run-link")
	defer agent.End()
	if agent.SpanContext().IsValid() {
		t.Fatal("Community created a linkable agent span")
	}
	for _, inbound := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer linked-bearer")
		if inbound {
			req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			req.Header.Set("tracestate", "vendor=opaque")
		}
		out := propagation.MapCarrier{}
		called := false
		resolve := func(token string) (oteltrace.SpanContext, string, bool) {
			if token != "linked-bearer" {
				t.Errorf("resolved bearer = %q", token)
			}
			return oteltrace.SpanContext{}, "", false
		}
		h := linkSessionTrace(p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			span := oteltrace.SpanFromContext(r.Context())
			if span.IsRecording() || span.SpanContext().IsValid() != inbound {
				t.Errorf("Community request trace: recording=%v valid=%v inbound=%v", span.IsRecording(), span.SpanContext().IsValid(), inbound)
			}
			p.Propagator().Inject(r.Context(), out)
			w.WriteHeader(http.StatusNoContent)
		})), resolve)
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if !called || res.Code != http.StatusNoContent {
			t.Errorf("request: called=%v status=%d", called, res.Code)
		}
		for _, key := range []string{"traceparent", "tracestate"} {
			if out[key] != req.Header.Get(key) {
				t.Errorf("%s = %q, want %q", key, out[key], req.Header.Get(key))
			}
		}
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := exports.Load(); got != 0 {
		t.Errorf("Community sent %d collector requests", got)
	}
}

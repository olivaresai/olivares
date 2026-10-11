// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

// Configuring a collector in Community must leave launch, stop and cleanup
// working without registering a trace or sending anything to the collector.
func TestCommunityConfiguredTraceRunLifecycle(t *testing.T) {
	t.Parallel()
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
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()),
		WithTraceProvider(p))
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	if sc, conv, ok := m.SessionTrace(tenant, dto.RunRef); ok || sc.IsValid() || conv != "" {
		t.Errorf("Community run exposed a trace: %v, %q, %v", sc, conv, ok)
	}
	if sc, conv, ok := m.SessionTraceForToken("tok-secret"); ok || sc.IsValid() || conv != "" {
		t.Errorf("Community bearer exposed a trace: %v, %q, %v", sc, conv, ok)
	}
	lr, ok := m.rt.getLive(tenant, dto.RunRef)
	if !ok {
		t.Fatal("the live run is not registered")
	}
	if lr.agentSpan != nil || lr.traceToken != [32]byte{} {
		t.Error("Community registered an agent span or bearer digest")
	}
	m.rt.traceMu.Lock()
	links := len(m.rt.traceTokens)
	m.rt.traceMu.Unlock()
	if links != 0 {
		t.Errorf("Community registered %d bearer links", links)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	m.awaitFinalize(lr)
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}
	if _, ok := m.rt.getLive(tenant, dto.RunRef); ok {
		t.Error("cleanup retained the live run")
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := exports.Load(); got != 0 {
		t.Errorf("Community sent %d collector requests", got)
	}
}

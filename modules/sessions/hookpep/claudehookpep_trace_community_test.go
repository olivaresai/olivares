// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package hookpep

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestCommunityHookDecisionsNeverExport(t *testing.T) {
	for _, decision := range []string{claude.DecisionAllow, claude.DecisionDeny, "unauthenticated"} {
		t.Run(decision, func(t *testing.T) {
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
			policyDecision := decision
			if decision == "unauthenticated" {
				policyDecision = claude.DecisionAllow
			}
			f := newHookLedgerFixture(t, PolicyDoc{Default: policyDecision})
			f.dec.Tracer = p
			in := hookLedgerInput(f.tenant, "Bash", ResourceKindShell, "ls", "read")
			in.SessionID = "untrusted-payload-run"
			if decision == "unauthenticated" {
				f.dec.Authr = sessionPlaneAuthn{}
				res, err := f.dec.Decide(ctx, in, "junk-bearer")
				if err != nil || res.Permission != claude.DecisionDeny {
					t.Fatalf("unauthenticated decision = %q, error = %v; want deny", res.Permission, err)
				}
			} else {
				assertSingleHookLedgerDecision(t, f, in, decision)
			}
			span := p.ExecuteTool(oteltrace.SpanContext{}, "claude", "Bash", "run-77")
			if span.SpanContext().IsValid() {
				t.Error("Community created a linkable tool span")
			}
			span.End()
			if err := p.Shutdown(ctx); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if got := exports.Load(); got != 0 {
				t.Errorf("Community sent %d collector requests", got)
			}
		})
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package trace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/propagation"
)

func TestCommunityConfiguredProviderNeverExports(t *testing.T) {
	ctx := context.Background()
	p, err := New(ctx, Config{Enabled: true, Endpoint: "http://127.0.0.1:4318", Protocol: ProtocolHTTP, SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(ctx)
	if p.Enabled() || p.Settings().Enabled || p.tp != nil || p.mp != nil {
		t.Fatal("Community constructs external trace/metric delivery")
	}
	carrier := propagation.MapCarrier{"traceparent": "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"}
	out := propagation.MapCarrier{}
	p.Propagator().Inject(p.Propagator().Extract(ctx, carrier), out)
	if out["traceparent"] != carrier["traceparent"] {
		t.Fatal("Community lost W3C trace propagation")
	}
}

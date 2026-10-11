// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// instrumentationName is the OTel instrumentation scope for this package's spans
// and metrics.
const instrumentationName = "github.com/olivaresai/olivares/core/observability/trace"

// Provider holds the composite W3C propagator and (when enabled) the recording
// TracerProvider + MeterProvider + OTLP exporters. The propagator ALWAYS works
// (extract/inject the W3C context) so trace continuation, ledger correlation and
// mesh stitching hold even in no-op mode; only span recording and OTLP export are
// gated on Enabled. A Provider is safe for concurrent use.
type Provider struct {
	current  atomic.Pointer[Provider]
	updateMu sync.Mutex
	stopped  bool
	settings Settings

	propagator    propagation.TextMapPropagator
	tracer        oteltrace.Tracer
	enabled       bool
	genAICompat   bool
	genAILatest   bool
	genai         *genAIInstruments // nil when disabled
	baggagePolicy providerBaggagePolicy

	tp          *sdktrace.TracerProvider // nil when disabled
	mp          *sdkmetric.MeterProvider // nil when disabled
	shutdownFns []func(context.Context) error
}

// New builds a Provider from cfg. With cfg.Enabled false (or no endpoint) it returns
// a no-op Provider: the composite propagator still extracts/injects W3C Trace
// Context, but spans are non-recording and nothing is exported. New never blocks on
// the collector (the OTLP exporters connect lazily), so a missing collector cannot
// delay or fail boot — a tracing fault must never break the engine (docs/SECURITY-HARDENING.md).
func New(ctx context.Context, cfg Config) (*Provider, error) {
	p := &Provider{
		settings: VisibleSettings(cfg),
		// W3C Trace Context + Baggage, composed. The propagator is Level-2-aware: it
		// parses the L2 random-trace-id flag and future traceparent versions forward-
		// compatibly (rather than rejecting them) and never deletes/reorders an upstream
		// tracestate member it did not write — exactly the read-first rule (docs/SECURITY-HARDENING.md).
		propagator:    propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		genAICompat:   cfg.GenAICompat,
		genAILatest:   cfg.GenAILatest,
		baggagePolicy: compileProviderBaggage(cfg.ProviderBaggageAllowlist, cfg.providerBaggageRejection),
	}
	if p.baggagePolicy.state == baggagePolicyInvalid {
		// Report only a fixed reason, never parser text or caller-supplied values.
		otel.Handle(fmt.Errorf("trace: provider baggage policy rejected (%s); baggage propagation disabled", p.baggagePolicy.reason))
	}

	return configureExport(ctx, cfg, p)
}

// Propagator returns the composite W3C Trace Context + Baggage propagator for
// ingress/internal use. Provider egress uses AnthropicHTTPClient's baggage policy.
func (p *Provider) Propagator() propagation.TextMapPropagator { return p.propagator }

// Enabled reports whether spans are recorded and exported (a collector is wired).
func (p *Provider) Enabled() bool { return p.active().enabled }

// Settings reports this provider's applied configuration, with credentials redacted.
func (p *Provider) Settings() Settings {
	active := p.active()
	settings := active.settings
	settings.Enabled = active.enabled
	return settings
}

// active snapshots the exporter for a request; existing transports retain this handle.
func (p *Provider) active() *Provider {
	if current := p.current.Load(); current != nil {
		return current
	}
	return p
}

// Replace installs a freshly constructed provider without replacing middleware or clients.
// The old exporter is flushed after the swap; a flush failure never undoes the new choice.
func (p *Provider) Replace(ctx context.Context, next *Provider) error {
	if next == nil || next == p || next.current.Load() != nil {
		return errors.New("trace: replacement must be a new provider")
	}
	p.updateMu.Lock()
	defer p.updateMu.Unlock()
	if p.stopped {
		_ = next.Shutdown(ctx)
		return errors.New("trace: provider is stopped")
	}
	old := p.active()
	p.current.Store(next)
	if err := old.shutdown(ctx); err != nil {
		otel.Handle(errors.New("trace: previous exporter could not flush"))
	}
	return nil
}

// Shutdown flushes and stops the active exporters. Repeated calls are harmless.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.updateMu.Lock()
	defer p.updateMu.Unlock()
	if p.stopped {
		return nil
	}
	p.stopped = true
	return p.active().shutdown(ctx)
}

func (p *Provider) shutdown(ctx context.Context) error {
	var firstErr error
	for _, fn := range p.shutdownFns {
		if err := fn(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

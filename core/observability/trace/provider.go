// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
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
	propagator  propagation.TextMapPropagator
	tracer      oteltrace.Tracer
	enabled     bool
	genAICompat bool
	genai       *genAIInstruments // nil when disabled

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
		// W3C Trace Context + Baggage, composed. The propagator is Level-2-aware: it
		// parses the L2 random-trace-id flag and future traceparent versions forward-
		// compatibly (rather than rejecting them) and never deletes/reorders an upstream
		// tracestate member it did not write — exactly the read-first rule (docs/SECURITY-HARDENING.md).
		propagator:  propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		genAICompat: cfg.GenAICompat,
	}

	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		p.tracer = noop.NewTracerProvider().Tracer(instrumentationName)
		return p, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.ServiceVersion),
		),
	)
	if err != nil {
		// A resource-detection error must not deny tracing; fall back to a bare resource.
		res = resource.NewSchemaless(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.ServiceVersion),
		)
	}

	traceExp, err := buildTraceExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("trace: build OTLP trace exporter: %w", err)
	}
	metricExp, err := buildMetricExporter(ctx, cfg)
	if err != nil {
		_ = traceExp.Shutdown(ctx)
		return nil, fmt.Errorf("trace: build OTLP metric exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		// Parent-based: a sampled upstream (the client/mesh decision) is always
		// honored; only roots are decided by the ratio.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)

	genai, err := newGenAIInstruments(mp.Meter(instrumentationName))
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, fmt.Errorf("trace: build gen_ai instruments: %w", err)
	}

	p.tracer = tp.Tracer(instrumentationName)
	p.enabled = true
	p.genai = genai
	p.tp = tp
	p.mp = mp
	p.shutdownFns = []func(context.Context) error{tp.Shutdown, mp.Shutdown}
	return p, nil
}

// Propagator returns the composite W3C Trace Context + Baggage propagator.
func (p *Provider) Propagator() propagation.TextMapPropagator { return p.propagator }

// Enabled reports whether spans are recorded and exported (a collector is wired).
func (p *Provider) Enabled() bool { return p.enabled }

// Shutdown flushes and stops the exporters. It is a no-op when disabled and is safe
// to call once on engine shutdown. Errors from the providers are joined.
func (p *Provider) Shutdown(ctx context.Context) error {
	var firstErr error
	for _, fn := range p.shutdownFns {
		if err := fn(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// buildTraceExporter constructs the OTLP trace exporter for the configured protocol.
func buildTraceExporter(ctx context.Context, cfg Config) (*otlptrace.Exporter, error) {
	switch cfg.Protocol {
	case ProtocolHTTP:
		opts := []otlptracehttp.Option{}
		if strings.Contains(cfg.Endpoint, "://") {
			opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(cfg.Endpoint))
			if cfg.Insecure {
				opts = append(opts, otlptracehttp.WithInsecure())
			}
		}
		return otlptracehttp.New(ctx, opts...)
	default:
		opts := []otlptracegrpc.Option{}
		if strings.Contains(cfg.Endpoint, "://") {
			opts = append(opts, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
		} else {
			opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
			if cfg.Insecure {
				opts = append(opts, otlptracegrpc.WithInsecure())
			}
		}
		return otlptracegrpc.New(ctx, opts...)
	}
}

// buildMetricExporter constructs the OTLP metric exporter for the configured protocol.
func buildMetricExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	switch cfg.Protocol {
	case ProtocolHTTP:
		opts := []otlpmetrichttp.Option{}
		if strings.Contains(cfg.Endpoint, "://") {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(cfg.Endpoint))
		} else {
			opts = append(opts, otlpmetrichttp.WithEndpoint(cfg.Endpoint))
			if cfg.Insecure {
				opts = append(opts, otlpmetrichttp.WithInsecure())
			}
		}
		return otlpmetrichttp.New(ctx, opts...)
	default:
		opts := []otlpmetricgrpc.Option{}
		if strings.Contains(cfg.Endpoint, "://") {
			opts = append(opts, otlpmetricgrpc.WithEndpointURL(cfg.Endpoint))
		} else {
			opts = append(opts, otlpmetricgrpc.WithEndpoint(cfg.Endpoint))
			if cfg.Insecure {
				opts = append(opts, otlpmetricgrpc.WithInsecure())
			}
		}
		return otlpmetricgrpc.New(ctx, opts...)
	}
}

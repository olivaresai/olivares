// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package trace

import (
	"context"

	"go.opentelemetry.io/otel/trace/noop"
)

// Community keeps the configured choices and W3C propagation without linking exporters.
func configureExport(_ context.Context, _ Config, p *Provider) (*Provider, error) {
	p.tracer = noop.NewTracerProvider().Tracer(instrumentationName)
	return p, nil
}

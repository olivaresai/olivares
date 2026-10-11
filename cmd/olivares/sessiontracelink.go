// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"

	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// linkSessionTrace installs the caller session's GenAI agent-span context on
// requests whose bearer is a live run's inference credential (#429), so the
// inference proxy's server span and the model span the GenAI transport emits
// land in that session's trace beside its invoke_agent and execute_tool spans.
//
// It runs OUTSIDE the trace middleware on purpose: HTTPMiddleware Extracts from
// the inbound headers (the native CLI sends no traceparent), and the ambient
// remote span context installed here survives that extraction, becoming the
// server span's parent. resolve is the sessions module's digest-map lookup —
// telemetry only. A nil resolve, an unknown bearer or a disabled tracer leaves
// the request untouched; nothing here can refuse, delay or alter a call.
func linkSessionTrace(next http.Handler, resolve func(string) (oteltrace.SpanContext, string, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if resolve != nil {
			if token := gatewayBearerToken(r); token != "" {
				if sc, conversationID, ok := resolve(token); ok {
					r = r.WithContext(obstrace.ContextWithSessionTrace(r.Context(), sc, conversationID))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

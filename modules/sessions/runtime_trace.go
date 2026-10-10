// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"crypto/sha256"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

// GenAI agent spans for one run (issue #429): the runtime starts the session's
// invoke_agent span when a launched process is registered and ends it when the
// run finalizes, next to the model spans the inference transport already emits.
// The span and its links are telemetry only: nothing here can refuse, delay or
// alter a launch, a decision or a forward.

// WithTraceProvider binds the engine's trace provider. Without it no agent
// spans are started and the SessionTrace lookups answer nothing — the module's
// behavior is unchanged.
func WithTraceProvider(tp *obstrace.Provider) Option {
	return func(m *Module) {
		if tp != nil {
			m.rt.Tracer = tp
		}
	}
}

// startAgentSpan opens the run's invoke_agent span and links the session's
// inference bearer to it. It runs at the liveRun registration of a LAUNCHED
// process (create and resume alike); a launch that produced no process emits no
// agent span. The bearer is stored as a SHA-256 digest only — the raw token
// never enters the link registry.
func (m *Module) startAgentSpan(lr *liveRun, p CreateRunParams, cred Credential) {
	if m.rt.Tracer == nil {
		return
	}
	span := m.rt.Tracer.InvokeAgent(launchDriverKey(p), lr.runRef)
	if !span.SpanContext().IsValid() {
		span.End() // tracing disabled: nothing to link, nothing to keep
		return
	}
	lr.agentSpan = span
	if cred.Token != "" {
		lr.traceToken = sha256.Sum256([]byte(cred.Token))
		m.rt.traceMu.Lock()
		m.rt.traceTokens[lr.traceToken] = lr
		m.rt.traceMu.Unlock()
	}
}

// unlinkTrace removes the run's bearer link when the run finalizes. The entry
// goes only if it still names this liveRun: a resume that reused the bearer has
// already replaced it with its own.
func (m *Module) unlinkTrace(lr *liveRun) {
	if lr.traceToken == [32]byte{} {
		return
	}
	m.rt.traceMu.Lock()
	if m.rt.traceTokens[lr.traceToken] == lr {
		delete(m.rt.traceTokens, lr.traceToken)
	}
	m.rt.traceMu.Unlock()
}

// SessionTrace returns the GenAI trace correlation of a live run: its
// invoke_agent span context (the parent engine-side spans join) and the run
// reference (gen_ai.conversation.id). ok is false when the run is not live,
// holds no agent span, or tracing is disabled.
func (m *Module) SessionTrace(tenant model.TenantID, runRef string) (oteltrace.SpanContext, string, bool) {
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok || lr.agentSpan == nil {
		return oteltrace.SpanContext{}, "", false
	}
	sc := lr.agentSpan.SpanContext()
	return sc, runRef, sc.IsValid()
}

// SessionTraceForToken resolves the same correlation from a session inference
// bearer — the token the inference proxy authenticates on a model call. The
// lookup is a digest map hit; it authenticates nothing and gates nothing, it
// only lets the proxy's spans join the caller session's trace.
func (m *Module) SessionTraceForToken(token string) (oteltrace.SpanContext, string, bool) {
	if token == "" {
		return oteltrace.SpanContext{}, "", false
	}
	digest := sha256.Sum256([]byte(token))
	m.rt.traceMu.Lock()
	lr, ok := m.rt.traceTokens[digest]
	m.rt.traceMu.Unlock()
	if !ok || lr.agentSpan == nil {
		return oteltrace.SpanContext{}, "", false
	}
	sc := lr.agentSpan.SpanContext()
	return sc, lr.runRef, sc.IsValid()
}

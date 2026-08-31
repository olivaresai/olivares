// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cowork

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// dispatchBuffer is the depth of the observation hand-off channel. A full buffer
// applies backpressure to the OTLP handler (slowing ingestion) rather than dropping
// facts — the same backpressure contract the SDK Sink documents.
const dispatchBuffer = 1024

// shutdownGrace bounds the graceful stop of the HTTP server so a hung client
// connection cannot wedge Stop.
const shutdownGrace = 5 * time.Second

// Source is the Cowork cooperative-telemetry SourceConnector. It is a streaming
// source: Gather binds the OTLP/HTTP logs server and blocks, emitting observations
// until the engine cancels ctx.
type Source struct {
	cfg config

	mu      sync.Mutex
	httpLis net.Listener
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Cowork connector with default configuration (overridden in Open).
func New() *Source { return &Source{cfg: loadConfig(sdk.Config{})} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves configuration and binds the receiver's listener, so a bind failure
// surfaces here, before Gather, as the SDK intends. Unlike Claude Code (a local
// agent posting to loopback), Cowork's telemetry is PUSHED from Anthropic's cloud to
// the configured endpoint, so a reachable receiver MUST be authenticated: a
// non-loopback bind without an auth_token is refused (deny-closed), because an
// unauthenticated public OTLP endpoint would let anyone forge Cowork telemetry /
// edges. The escape hatch (allow_public_bind) makes accepting that risk explicit.
// The connector_controls policy is also parsed fail-closed here, BEFORE Gather: an
// operator-authored control floor with a typo must refuse to start, never silently
// run ungoverned (controls.go).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c := loadConfig(cfg)
	controls, err := ParseConnectorControls(c.rawControls)
	if err != nil {
		return fmt.Errorf("cowork: invalid connector_controls: %w", err)
	}
	c.controls = controls
	s.cfg = c
	if !c.enableHTTP {
		return fmt.Errorf("cowork: enable_http is false; nothing to receive")
	}
	// One admission point for every socket this product opens. Cowork keeps
	// its OWN extra condition, expressed as the declaration it already was: unlike
	// the sibling receivers, a configured auth_token is itself an acceptance of a
	// reachable endpoint, because Anthropic's cloud PUSHES here and the token is
	// what authenticates it. Folding it into AllowPublic preserves the exact
	// behavior this connector shipped with — a public bind carrying a token stays
	// allowed — while the loopback classification and the refusal come from the
	// one shared decision.
	lis, err := netbind.Listen(context.Background(), "tcp", c.httpAddr, netbind.Policy{
		Component:   "cowork",
		Purpose:     "OTLP/HTTP receiver",
		AllowPublic: c.allowPublicBind || c.authToken != "",
		OptIn:       cfgAllowPublicBind,
	})
	if err != nil {
		return fmt.Errorf("cowork: bind OTLP/HTTP %s: %w", c.httpAddr, err)
	}
	s.httpLis = lis
	return nil
}

// Gather runs the receiver: it starts the OTLP/HTTP logs server and a single
// dispatcher that serializes emission to the sink, then blocks until ctx is canceled
// and shuts everything down cleanly. It returns nil on a ctx-driven stop and a
// non-nil error only on a genuine serve fault.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	now := func() time.Time { return time.Now().UTC() }

	// Dispatcher: the only caller of sink.Emit, so emission is serial regardless of the
	// SDK's concurrency contract. emitCtx is the single shutdown signal for BOTH sides.
	// obsCh is deliberately NEVER closed: an OTLP handler runs synchronously in its own
	// goroutine and net/http.Server.Shutdown does not interrupt a handler that outlives
	// the grace window, so a late handler can still reach the dispatch() send after
	// Gather has begun tearing down. Closing obsCh would make that send panic ("send on
	// closed channel"); driving the dispatcher off emitCtx and leaving the buffered
	// channel open lets a late send be absorbed by the buffer (or fall through its own
	// emitCtx.Done() case) instead. Any observation still buffered at a backpressured
	// shutdown is dropped rather than delivered — acceptable for a graceful stop
	// (at-least-once; the engine re-polls), and strictly better than crashing.
	obsCh := make(chan model.Observation, dispatchBuffer)
	emitCtx, emitCancel := context.WithCancel(context.Background())
	var dispatcherDone sync.WaitGroup
	dispatcherDone.Add(1)
	go func() {
		defer dispatcherDone.Done()
		for {
			select {
			case o := <-obsCh:
				_ = sink.Emit(emitCtx, o)
			case <-emitCtx.Done():
				return
			}
		}
	}()

	// dispatch hands an observation to the dispatcher, blocking under backpressure (the
	// SDK Sink contract). It selects on emitCtx so a send cannot deadlock (or panic)
	// once the dispatcher has stopped.
	dispatch := func(o model.Observation) {
		select {
		case obsCh <- o:
		case <-emitCtx.Done():
		}
	}

	ids := newIdentitySeen()

	// OBS-10: record the content-capture posture on the ledger before any telemetry is
	// processed, so an auditor can prove what the connector was permitted to retain —
	// salient for Cowork, which always sends content in its events.
	dispatch(selfAuditFinding(s.cfg.contentCapture, now()))

	// PERMITTED side of the access-map diff: the org-effective connector-control
	// policy becomes policy-sourced edges once per Gather. Re-emission is idempotent
	// (the engine upserts by natural key), and emitting BEFORE any telemetry means
	// the permitted set is on the ledger before the first observed edge arrives.
	if s.cfg.controls.Configured() {
		for _, e := range s.cfg.controls.PermittedEdges(s.cfg.orgRef, now()) {
			dispatch(e)
		}
	}

	rcv := &receiver{
		onEvent:        s.routeEvent(ids, dispatch),
		requireService: s.cfg.requireService,
		authHeader:     s.cfg.authHeader,
		authToken:      s.cfg.authToken,
		now:            now,
	}

	httpSrv, httpLis := s.takeServer(rcv)
	if httpSrv == nil {
		// Open guarantees a bound listener; this only happens if Gather is called twice.
		emitCancel()
		dispatcherDone.Wait()
		return fmt.Errorf("cowork: no bound listener (Gather called without a successful Open)")
	}

	var server sync.WaitGroup
	server.Add(1)
	go func() { defer server.Done(); _ = httpSrv.Serve(httpLis) }()

	<-ctx.Done()

	// Stop accepting and let in-flight handlers drain up to the grace window; then signal
	// shutdown via emitCancel (NOT close(obsCh) — a handler that outlived the grace can
	// still call dispatch(), and a send on a closed channel would panic). emitCancel both
	// stops the dispatcher and unblocks any parked dispatch() via its emitCtx.Done() case.
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	_ = httpSrv.Shutdown(shutCtx)
	cancel()
	server.Wait()
	emitCancel()
	dispatcherDone.Wait()
	return nil
}

// Close releases the listener if Open succeeded but Gather never ran. It is safe to
// call even if Open failed.
func (s *Source) Close(context.Context) error {
	s.closeListener()
	return nil
}

// routeEvent builds the handler for each parsed Cowork event: it attributes the
// session to its org/account once (the OTEL↔Compliance correlation seam), then maps
// the event to its observations — access edges, the auto-approved-high-risk finding
// and the connector-control drift finding for a tool_result, a denial finding for a
// tool_decision, a cost sample for an api_request, a health finding for an
// api_error. A user_prompt feeds liveness only; its content is never read.
func (s *Source) routeEvent(ids *identitySeen, dispatch func(model.Observation)) func(coworkEvent) {
	return func(ev coworkEvent) {
		if edges := identityEdges(ev.identity(), ev.at); len(edges) > 0 && ids.first(ev.sessionID) {
			for _, e := range edges {
				dispatch(e)
			}
		}
		switch ev.name {
		case evtToolResult:
			if e, ok := edgeFromTool(ev.sessionID, ev.toolName, ev.toolInput, ev.at); ok {
				dispatch(e)
			}
			if e, ok := edgeFromMCPServer(ev); ok {
				dispatch(e)
			}
			// The Cowork governance signal: an AI-initiated high-risk action that ran with
			// AUTOMATIC approval (no human in the loop).
			if f, ok := findingFromAutoApprovedAction(ev); ok {
				dispatch(f)
			}
			// OBSERVED-vs-PERMITTED live drift: an EXECUTED MCP connector/tool crossed
			// against the org-effective connector-control policy (controls.go).
			if s.cfg.controls.Configured() {
				if server, tool, ok := controlTarget(ev); ok {
					level := s.cfg.controls.EffectiveLevel(server, tool)
					if f, ok := controlDriftFinding(ev, level, server, tool); ok {
						dispatch(f)
					}
				}
			}
		case evtToolDecision:
			if f, ok := findingFromToolDecision(ev); ok {
				dispatch(f)
			}
		case evtAPIRequest:
			if cs, ok := costFromAPIRequest(ev, s.cfg.gateway); ok {
				dispatch(cs)
			}
		case evtAPIError:
			if f, ok := findingFromAPIError(ev); ok {
				dispatch(f)
			}
		case evtUserPrompt:
			// Liveness/prompt-volume only; the prompt content is never read (docs/SECURITY-HARDENING.md).
		}
	}
}

// takeServer takes ownership of the bound listener (nil-ing it on the struct so
// Close won't double-close) and constructs the HTTP server around the receiver.
func (s *Source) takeServer(rcv *receiver) (*http.Server, net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpLis == nil {
		return nil, nil
	}
	h := &http.Server{
		Handler:           rcv.httpHandler(s.cfg.logsPath),
		ReadHeaderTimeout: 10 * time.Second,
	}
	lis := s.httpLis
	s.httpLis = nil
	return h, lis
}

// closeListener closes the listener still held by the struct (idempotent).
func (s *Source) closeListener() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpLis != nil {
		_ = s.httpLis.Close()
		s.httpLis = nil
	}
}

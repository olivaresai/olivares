// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// dispatchBuffer is the depth of the observation hand-off channel. A full buffer
// applies backpressure to the OTLP/hook handlers (slowing ingestion) rather than
// dropping facts — the same backpressure contract the SDK Sink documents.
const dispatchBuffer = 1024

// shutdownGrace bounds the graceful stop of the HTTP server and the final drain
// so a hung client connection cannot wedge Stop.
const shutdownGrace = 5 * time.Second

// Source is the Claude Code cooperative-telemetry SourceConnector. It is a
// streaming source: Gather binds the OTLP and hook servers and blocks, emitting
// observations until the engine cancels ctx.
type Source struct {
	cfg config

	// managedConstraint is the CLA-10 cross-reference to enterprise managed-settings
	// (CLA-05). It is nil until a real reader is wired; while nil the
	// Agent SDK drift check applies the operator policy alone (the fail-closed seam).
	managedConstraint ManagedSettingsConstraint

	mu      sync.Mutex
	grpcLis net.Listener
	httpLis net.Listener
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Claude connector with default configuration (overridden in Open).
// The empty config can never produce an enforcement-parse error, so the error is
// safely discarded here; Open surfaces a real operator misconfiguration.
func New() *Source {
	c, _ := loadConfig(sdk.Config{})
	return &Source{cfg: c}
}

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// bindPolicy describes one receiver to the single admission point. The
// cooperative OTLP/hook ingest is UNAUTHENTICATED and carries no transport
// protection: anything that can reach the port can forge telemetry/edges or spoof
// heartbeats to defeat the silence watchdog. Keep it on loopback (the agent runs
// on the same host); the non-cooperative eBPF backstop is the supported way to
// capture off-host activity (docs/SECURITY-HARDENING.md, §6).
func (s *Source) bindPolicy(purpose string) netbind.Policy {
	return netbind.Policy{
		Component:   "claude",
		Purpose:     purpose,
		AllowPublic: s.cfg.allowPublicBind,
		OptIn:       cfgAllowPublicBind,
	}
}

// Open resolves configuration and binds the receiver's listeners, so a bind
// failure (a port already in use) surfaces here, before Gather, as the SDK
// intends. The listeners are handed to the servers in Gather and closed by Close
// if Gather never runs.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	s.cfg = c
	// The cooperative OTLP/hook ingest is UNAUTHENTICATED: anything that can reach
	// the port can forge telemetry/edges or spoof heartbeats to defeat the silence
	// watchdog. Keep it on loopback (the agent runs on the same host); refuse a
	// non-loopback bind unless the operator explicitly accepts the risk. The
	// non-cooperative eBPF backstop is the supported way to capture off-host
	// activity (docs/SECURITY-HARDENING.md, §6).
	// One admission point for every socket this product opens.
	if s.cfg.enableGRPC {
		lis, err := netbind.Listen(context.Background(), "tcp", s.cfg.grpcAddr, s.bindPolicy("OTLP/gRPC receiver"))
		if err != nil {
			return fmt.Errorf("claude: bind OTLP/gRPC %s: %w", s.cfg.grpcAddr, err)
		}
		s.grpcLis = lis
	}
	if s.cfg.enableHTTP {
		lis, err := netbind.Listen(context.Background(), "tcp", s.cfg.httpAddr, s.bindPolicy("OTLP/HTTP receiver"))
		if err != nil {
			s.closeListeners()
			return fmt.Errorf("claude: bind OTLP/HTTP %s: %w", s.cfg.httpAddr, err)
		}
		s.httpLis = lis
	}
	if !s.cfg.enableGRPC && !s.cfg.enableHTTP {
		return fmt.Errorf("claude: both OTLP/gRPC and OTLP/HTTP are disabled; nothing to receive")
	}
	return nil
}

// Gather runs the receiver: it starts the OTLP/gRPC and OTLP/HTTP servers, the
// hook endpoint, the correlation/anti-evasion janitor and a single dispatcher
// that serializes emission to the sink, then blocks until ctx is canceled and
// shuts everything down cleanly. It returns nil on a ctx-driven stop and a
// non-nil error only on a genuine serve fault.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	now := func() time.Time { return time.Now().UTC() }

	// Dispatcher: the only caller of sink.Emit, so emission is serial regardless
	// of the SDK's concurrency contract. emitCtx outlives ctx so shutdown-flushed
	// observations still deliver; it is canceled once the drain completes.
	obsCh := make(chan model.Observation, dispatchBuffer)
	emitCtx, emitCancel := context.WithCancel(context.Background())
	var dispatcherDone sync.WaitGroup
	dispatcherDone.Add(1)
	go func() {
		defer dispatcherDone.Done()
		for o := range obsCh {
			_ = sink.Emit(emitCtx, o)
		}
	}()

	// dispatch hands an observation to the dispatcher, blocking under backpressure
	// (the SDK Sink contract). It selects on emitCtx, NOT the Gather ctx: emitCtx
	// stays alive through the shutdown drain (it is canceled only after the
	// dispatcher has finished), so the final corr.drain() flush is delivered rather
	// than dropped once ctx is canceled.
	dispatch := func(o model.Observation) {
		select {
		case obsCh <- o:
		case <-emitCtx.Done():
		}
	}

	corr := newCorrelator(s.cfg.correlationWait, dispatch)
	wd := newWatchdog(s.cfg.silenceThreshold, dispatch)
	ids := newIdentitySeen()

	// OBS-10: record the active content-capture posture on the ledger before any
	// telemetry is processed, so an auditor can prove what the connector was
	// permitted to retain for this run.
	dispatch(s.cfg.redaction.selfAuditFinding(now()))

	// ANT2-12/13: observe the managed-mcp.json eval-order effect and the sandbox/egress
	// containment posture once at start (read-only; no-op when unconfigured).
	s.gatherManagedMCP(now(), dispatch)
	s.gatherSandbox(now(), dispatch)
	// assess the declared Agent SDK program's dangerous-knob posture once at start
	// (read-only; no-op when no agent_sdk_config is declared).
	s.gatherAgentSDK(now(), dispatch)

	// when the experimental gen_ai profile is on, record its semconv pin
	// and dialect set on the ledger (the same self-audit pattern as OBS-10), so
	// an auditor can prove what this run was able to normalize.
	if s.cfg.genAIProfile {
		dispatch(genAIPostureFinding(now()))
	}

	rcv := &receiver{
		onOTEL:        s.routeOTEL(corr, wd, ids, dispatch),
		onGenAI:       s.genAIRouter(wd, ids, dispatch),
		onGenAIMetric: s.genAIMetricLiveness(wd),
		onHook:        s.routeHook(corr, wd),
		onHookDecide:  s.hookDecider(dispatch),
		onSignal:      s.routeSignal(wd, ids, dispatch),
		onMetric:      s.metricValueRouter(dispatch),
		onAgentSpan:   s.routeAgentSpan(dispatch),
		labelKeys:     s.cfg.resourceLabels,
		now:           now,
	}

	var servers sync.WaitGroup
	grpcSrv, httpSrv, httpLis := s.takeServers(rcv)
	if grpcSrv != nil {
		servers.Add(1)
		go func() { defer servers.Done(); _ = grpcSrv.server.Serve(grpcSrv.lis) }()
	}
	if httpSrv != nil {
		servers.Add(1)
		go func() { defer servers.Done(); _ = httpSrv.Serve(httpLis) }()
	}

	// Janitor: flush correlation buffers and run the anti-evasion check.
	janitorDone := make(chan struct{})
	go s.janitor(ctx, corr, wd, now, janitorDone)

	<-ctx.Done()

	// Shutdown: stop accepting, flush in-flight, then release the sink.
	if grpcSrv != nil {
		grpcSrv.server.GracefulStop()
	}
	if httpSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}
	<-janitorDone
	servers.Wait()
	// Producers have stopped; run the anti-evasion check once more so a session
	// that crossed the silence threshold is reported, then flush buffered edges.
	wd.sweep(now())
	corr.drain()
	close(obsCh)
	dispatcherDone.Wait()
	emitCancel()
	return nil
}

// Close releases any listeners still held (i.e. Open succeeded but Gather never
// ran). It is safe to call even if Open failed.
func (s *Source) Close(context.Context) error {
	s.closeListeners()
	return nil
}

// routeOTEL builds the handler for each parsed OTEL event: it feeds liveness,
// attributes the session to its Claude org/account/agent (once), correlates tool
// runs, and emits cost and MCP-connection observations.
func (s *Source) routeOTEL(corr *correlator, wd *watchdog, ids *identitySeen, dispatch func(model.Observation)) func(claudeEvent) {
	return func(ev claudeEvent) {
		wd.observeOTEL(ev.sessionID, ev.at)
		// OBS-09: link the session to its org/account/agent the first time we see
		// identity for it, so attribution is per-org/account/agent, not session-only.
		if edges := identityEdges(ev.identity(), ev.at); len(edges) > 0 && ids.first(ev.sessionID) {
			for _, e := range edges {
				dispatch(e)
			}
		}
		switch ev.name {
		case evtToolResult:
			corr.offer(toolSignalFromOTEL(ev), ev.at)
		case evtToolDecision:
			// A DENIED tool decision is the security-relevant cooperative signal
			// (attempted-but-refused access); surface it rather than drop it.
			if f, ok := findingFromToolDecision(ev); ok {
				dispatch(f)
			}
		case evtAPIRequest:
			if cs, ok := costFromEvent(ev, s.cfg.gateway); ok {
				dispatch(cs)
			}
		case evtMCPConnection:
			if e, ok := edgeFromMCPConnection(ev); ok {
				dispatch(e)
			}
			if f, ok := findingFromMCPConnection(ev); ok {
				dispatch(f)
			}
		case evtPermissionMode:
			// A permission-mode escalation (e.g. → bypassPermissions) is the
			// governance signal a control plane that GOVERNS Claude must not drop.
			if f, ok := findingFromPermissionMode(ev); ok {
				dispatch(f)
			}
			// CLA-10: verify the observed mode against the declared Agent SDK policy
			// (and managed-settings when wires it); a policy violation is a
			// higher-severity drift finding distinct from the transition observation.
			if f, ok := verifyAgentSDKMode(ev.toMode, ev.sessionID, ev.at, s.cfg.agentSDKPolicy, s.managedConstraint); ok {
				dispatch(f)
			}
		case evtAuth:
			if f, ok := findingFromAuth(ev); ok {
				dispatch(f)
			}
		case evtAPIError, evtAPIRetriesGone:
			if f, ok := findingFromAPIError(ev); ok {
				dispatch(f)
			}
		case evtPluginInstalled:
			// ANT2-09: a session pulled in executable plugin surface — a supply-chain
			// governance signal.
			if f, ok := findingFromPluginInstalled(ev); ok {
				dispatch(f)
			}
		case evtSkillActivated:
			// ANT2-09: a (workspace-wide, executable) skill ran — supply-chain signal.
			if f, ok := findingFromSkillActivated(ev); ok {
				dispatch(f)
			}
		case evtCompaction:
			// ANT2-09: context was summarized — a forensic-continuity signal.
			if f, ok := findingFromCompaction(ev); ok {
				dispatch(f)
			}
		case evtUserPrompt:
			// Liveness only (handled by wd.observeOTEL above); the prompt content is
			// never read. Routed explicitly so the event is no longer dead (OBS-05).
		default:
			// An unrecognized event name (e.g. a future claude_code.* event, or a
			// vendor-neutral gen_ai.* record when the OBS-01 profile is OFF). Liveness
			// was already fed above, so the session is never invisible; we just do not
			// map an edge/cost against a schema we do not (yet) understand. With the
			// gen_ai profile ON, gen_ai.* records never reach here — they are routed to
			// genAIRouter before parseLogRecord (otlp.go). This default makes the
			// "no silent drop" explicit (OBS-01: the routeOTEL switch is no longer
			// case-only).
		}
	}
}

// genAIRouter builds the handler for a vendor-neutral gen_ai.* signal — a log
// record (a per-message event or the operation.details event) OR a trace span
// (the shape frameworks actually emit: any of the three dialects,
// mcp.* and agent spans) — feeding the SAME pipeline as the Claude path:
// liveness, a once-per-run drift finding per deprecated dialect, a
// once-per-conversation agent attribution edge, a CostSample from
// gen_ai.usage.*, an access edge for a tool execution, the MCP server/resource/
// prompt edges (v1.39), the remote invoke_agent delegation edge (v1.41 client
// variant) and the invoke_workflow edge. It returns nil when the operator has
// not opted into the experimental GenAI conventions, so the receiver leaves
// gen_ai mapping off by default (the conventions are Development).
func (s *Source) genAIRouter(wd *watchdog, ids *identitySeen, dispatch func(model.Observation)) func(genAIEvent) {
	if !s.cfg.genAIProfile {
		return nil
	}
	// costDedup makes a gen_ai operation costed once even when it arrives on BOTH its
	// trace span AND its gen_ai.client.inference.operation.details log event — the two
	// carry the same W3C span id (Trace Context correlation, consumes). One
	// per Gather run; shared safely across the receiver's concurrent handlers.
	costDedup := newGenAICostDedup(genAICostDedupCap)
	// dialectsSeen bounds the dialect-currency drift findings to one per
	// deprecated dialect per Gather run (at most two findings).
	dialectsSeen := newIdentitySeen()
	return func(e genAIEvent) {
		wd.observeOTEL(e.livenessKey(), e.at)
		if f, ok := dialectDriftFinding(e, dialectsSeen); ok {
			dispatch(f)
		}
		// The attribution edge is evaluated BEFORE the dedup and keyed on the
		// (conversation, agent) PAIR (fix): in real OTLP export order child
		// spans end — and export — before their parent, so an agentless signal of
		// the conversation (a chat span) arrives first; consuming a per-conversation
		// slot on it would silently lose the conversation→agent edge the later
		// invoke_agent span carries. The pair key also attributes EVERY distinct
		// agent a conversation runs, not just the first.
		if edge, ok := agentEdgeFromGenAI(e); ok && ids.first(e.conversationID+"\x00"+edge.ResourceRef) {
			dispatch(edge)
		}
		if cs, ok := costFromGenAI(e); ok && costDedup.first(e.spanID) {
			dispatch(cs)
		}
		if edge, ok := toolEdgeFromGenAI(e); ok {
			dispatch(edge)
		}
		for _, edge := range mcpEdgesFromGenAI(e) {
			dispatch(edge)
		}
		if edge, ok := remoteAgentEdgeFromGenAI(e); ok {
			dispatch(edge)
		}
		if edge, ok := workflowEdgeFromGenAI(e); ok {
			dispatch(edge)
		}
	}
}

// genAIMetricLiveness builds the liveness-only feed for a vendor-neutral gen_ai.*
// METRIC batch. gen_ai client metrics are AGGREGATES: their token values are
// not costed (the span/log event is the authoritative per-operation usage — costing
// the metric too would double-count), but a recognized batch still proves the fleet
// is alive, so it feeds the silence watchdog keyed by the conversation/agent it
// carries. An empty key (the common case — metrics rarely carry a per-conversation
// id) simply records no liveness; it is never fabricated. Nil when the profile is off.
func (s *Source) genAIMetricLiveness(wd *watchdog) func(string, time.Time) {
	if !s.cfg.genAIProfile {
		return nil
	}
	return func(key string, at time.Time) { wd.observeOTEL(key, at) }
}

// routeHook builds the telemetry handler for each parsed hook: it feeds liveness
// for EVERY hook in the lifecycle (so the silence watchdog tracks any hooking
// session, the anti-evasion backstop of docs/SECURITY-HARDENING.md) and, for a completed tool call
// (PostToolUse), offers the resource side to the correlator. The synchronous
// governance decision is handled separately by hookDecider; this path is the
// observe side and never blocks the response.
func (s *Source) routeHook(corr *correlator, wd *watchdog) func(hookEvent) {
	return func(h hookEvent) {
		wd.observeHook(h.sessionID, h.at)
		if h.event == hookPostToolUse {
			corr.offer(toolSignalFromHook(h), h.at)
		}
	}
}

// routeAgentSpan builds the handler for the subagent identity a claude_code.
// llm_request/tool span carries: it emits the session→subagent membership
// edge and, when the span names a spawner, the parent→child hierarchy edge — the
// per-INSTANCE delegation chain the Agent/Task tool's subagent_type (a TYPE
// label) cannot give. Deduped once per (session, agent, parent) triple per
// Gather run; the engine merges cross-restart re-emission by natural key.
func (s *Source) routeAgentSpan(dispatch func(model.Observation)) func(sessionID, agentID, parentAgentID string, at time.Time) {
	seen := newIdentitySeen()
	return func(sessionID, agentID, parentAgentID string, at time.Time) {
		if !seen.first(sessionID + "\x00" + agentID + "\x00" + parentAgentID) {
			return
		}
		for _, e := range subagentEdges(sessionID, agentID, parentAgentID, at) {
			dispatch(e)
		}
	}
}

// routeSignal builds the handler for a trace span or metric batch (OBS-03/04): it
// feeds the silence watchdog (so a session that emits only traces/metrics — e.g.
// one that disabled OTEL logs but left the tracing beta on — is still seen alive,
// the anti-evasion backstop of docs/SECURITY-HARDENING.md) and attributes the session to its
// org/account/agent (once, OBS-09). It deliberately maps no content and no metric
// values; those are the FinOps and forensics surfaces.
func (s *Source) routeSignal(wd *watchdog, ids *identitySeen, dispatch func(model.Observation)) func(claudeIdentity, time.Time) {
	return func(id claudeIdentity, at time.Time) {
		wd.observeOTEL(id.sessionID, at)
		if edges := identityEdges(id, at); len(edges) > 0 && ids.first(id.sessionID) {
			for _, e := range edges {
				dispatch(e)
			}
		}
	}
}

// metricValueRouter builds the handler that persists one Claude Code adoption-metric
// datapoint VALUE as a MetricSample — the productivity signal the receiver used
// to discard. It returns nil when claude_code_metrics is off (the receiver stays
// liveness-only). The subject is the SESSION (the OTLP plane never reads the developer
// email Claude Code exports on OAuth — minimal-data); the operator's team/project
// labels ride Labels so the adoption module can aggregate per team. Idempotency travels
// in Additive: a delta counter is summed per day with the consumer deduping re-delivered
// intervals by the OccurredAt high-water; cost never rides this path (owns cost).
func (s *Source) metricValueRouter(dispatch func(model.Observation)) func(claudeMetric) {
	if !s.cfg.claudeCodeMetrics {
		return nil
	}
	return func(cm claudeMetric) {
		if cm.session == "" || cm.name == "" {
			return
		}
		dispatch(model.MetricSample{
			Name:        cm.name,
			Value:       cm.value,
			Additive:    cm.additive,
			Unit:        cm.unit,
			SubjectKind: subjectSession,
			SubjectRef:  cm.session,
			OccurredAt:  cm.at,
			Dimensions:  cm.dims,
			Labels:      cm.labels,
		})
	}
}

// hookDecider builds the synchronous, opt-in governance decision function for the
// hook endpoint (CLA-01). When no enforcement policy is configured it returns nil
// — the cooperative-by-default posture: hooks are observed, never gated, and the
// endpoint keeps answering the neutral "{}". When a policy is configured, every
// imposed gate is also published as a finding so the central audit/HITL trail sees
// the decision the edge made. The decision itself is computed locally (no engine
// round-trip on the agent's hot path; read-first, docs/SECURITY-HARDENING.md).
func (s *Source) hookDecider(dispatch func(model.Observation)) func(hookEvent) hookDecision {
	if !s.cfg.enforcement.enabled() {
		return nil
	}
	return func(h hookEvent) hookDecision {
		d := s.cfg.enforcement.decide(h)
		if d.isEmpty() {
			return d
		}
		dispatch(findingFromEnforcement(h, d))
		return d
	}
}

// janitor periodically flushes the correlator and runs the watchdog until ctx is
// canceled, then signals done. The interval keeps correlation latency near the
// configured window without busy-looping.
func (s *Source) janitor(ctx context.Context, corr *correlator, wd *watchdog, now func() time.Time, done chan struct{}) {
	defer close(done)
	interval := s.cfg.correlationWait / 2
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			corr.sweep(now())
			wd.sweep(now())
		}
	}
}

// toolSignalFromOTEL builds the correlator's OTEL side from a tool_result event,
// resolving (and redacting) the resource at ingest so no raw input is retained.
func toolSignalFromOTEL(ev claudeEvent) toolSignal {
	kind, ref, mode := resourceFromTool(ev.toolName, ev.toolInput)
	return toolSignal{
		sessionID:   ev.sessionID,
		toolName:    ev.toolName,
		toolUseID:   ev.toolUseID,
		promptID:    ev.promptID,
		at:          ev.at,
		fromHook:    false,
		resKind:     kind,
		resRef:      ref,
		mode:        mode,
		hasResource: kind != resTool,
	}
}

// toolSignalFromHook builds the correlator's hook side from a PostToolUse hook,
// resolving (and redacting) the resource at ingest.
func toolSignalFromHook(h hookEvent) toolSignal {
	kind, ref, mode := resourceFromTool(h.toolName, h.input)
	return toolSignal{
		sessionID:   h.sessionID,
		toolName:    h.toolName,
		toolUseID:   h.toolUseID,
		at:          h.at,
		fromHook:    true,
		resKind:     kind,
		resRef:      ref,
		mode:        mode,
		hasResource: kind != resTool,
	}
}

// grpcServer bundles a gRPC server with the listener it serves.
type grpcServer struct {
	server *grpc.Server
	lis    net.Listener
}

// takeServers takes ownership of the bound listeners (nil-ing them on the struct
// so Close won't double-close) and constructs the servers around the receiver. It
// returns the HTTP listener alongside its server so Gather can Serve it directly.
func (s *Source) takeServers(rcv *receiver) (*grpcServer, *http.Server, net.Listener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var g *grpcServer
	if s.grpcLis != nil {
		// Match the OTLP/HTTP receive cap so a large Claude Code batch is not
		// rejected on gRPC only (the default gRPC max-recv is 4 MiB).
		srv := grpc.NewServer(grpc.MaxRecvMsgSize(maxOTLPBody))
		rcv.registerGRPC(srv)
		g = &grpcServer{server: srv, lis: s.grpcLis}
		s.grpcLis = nil
	}
	var h *http.Server
	var hlis net.Listener
	if s.httpLis != nil {
		h = &http.Server{
			Handler:           rcv.httpHandler(s.cfg.hookPath),
			ReadHeaderTimeout: 10 * time.Second,
		}
		hlis = s.httpLis
		s.httpLis = nil
	}
	return g, h, hlis
}

// closeListeners closes any listeners still held by the struct (idempotent).
func (s *Source) closeListeners() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grpcLis != nil {
		_ = s.grpcLis.Close()
		s.grpcLis = nil
	}
	if s.httpLis != nil {
		_ = s.httpLis.Close()
		s.httpLis = nil
	}
}

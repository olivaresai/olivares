// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// inferenceproxy_mcp_egress_test.go is the Community real-proxy oracle for C8 E2-3 (the
// accepted MCP egress construction contract, §10 and Root corrections m1-m14) and for the
// O-1 notification defect. Every test here drives the REAL decider, and most drive it
// through the REAL connector shell (MessagesProxy.ServeHTTP) with the REAL Inference
// client over a recording transport, so "zero upstream" means zero HTTP requests left the
// process — count_tokens included.
//
// The egress gates here are FAKES: the private servertoolegress adapter is D's assembled
// oracle, not this file's. A fake allow is therefore a Community allow by construction; it
// proves the Community half refuses what it must and forwards exactly what it captured.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/sdk/event"
)

// ---- the declared MCP destination ------------------------------------------------

// The server name, per-tool name, URL path/query and authorization token are private
// forwarding data: they must reach the upstream body unchanged and must reach no gate
// input, finding, log line, approval or HTTP error. The origin (scheme, host, port) is the
// only part a gate may see.
const (
	mcpCanaryName  = "acme-CANARY-NAME"
	mcpCanaryTool  = "read_CANARY_TOOL"
	mcpCanaryPath  = "CANARY-PATH"
	mcpCanaryQuery = "CANARY-QUERY"
	mcpCanaryToken = "CANARY-TOKEN-do-not-leak"
	mcpTestOrigin  = "https://mcp.example.com:8443"
	mcpTestURL     = mcpTestOrigin + "/" + mcpCanaryPath + "?q=" + mcpCanaryQuery
	mcpCurrentBeta = "mcp-client-2025-11-20"
)

func mcpWireCanaries() []string {
	return []string{mcpCanaryName, mcpCanaryTool, mcpCanaryPath, mcpCanaryQuery, mcpCanaryToken}
}

// mcpToolsetMap and mcpServerMap are the declaration exactly as an HTTP caller sends it.
func mcpToolsetMap(name string) map[string]any {
	return map[string]any{
		"type":            "mcp_toolset",
		"mcp_server_name": name,
		"default_config":  map[string]any{"enabled": false},
		"configs":         map[string]any{mcpCanaryTool: map[string]any{"enabled": true}},
	}
}

func mcpServerMap(name, url string) map[string]any {
	return map[string]any{"type": "url", "name": name, "url": url, "authorization_token": mcpCanaryToken}
}

// mcpMarker is the ONLY form an MCP tools[] slot may take in a gate-facing view.
func mcpMarker() map[string]any { return map[string]any{"type": "mcp_toolset"} }

func plainParams(stream bool) map[string]any {
	p := map[string]any{
		"model":      "claude-opus-4-8",
		"max_tokens": 16,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "summarize the public changelog"}},
		}},
	}
	if stream {
		p["stream"] = true
	}
	return p
}

func mcpParams(stream bool) map[string]any {
	p := plainParams(stream)
	p["tools"] = []any{mcpToolsetMap(mcpCanaryName)}
	p["mcp_servers"] = []any{mcpServerMap(mcpCanaryName, mcpTestURL)}
	return p
}

func batchBody(params ...map[string]any) map[string]any {
	reqs := make([]any, len(params))
	for i, p := range params {
		reqs[i] = map[string]any{"custom_id": "c" + string(rune('0'+i)), "params": p}
	}
	return map[string]any{"requests": reqs}
}

// ---- the recording upstream --------------------------------------------------------

type mcpUpstreamCall struct {
	path      string
	accept    string
	betaLines []string
	body      []byte
}

// mcpUpstream records every HTTP request the real Inference client builds (count_tokens,
// blocking, streaming and batch) and answers each with a canned response of its shape.
type mcpUpstream struct {
	mu    sync.Mutex
	calls []mcpUpstreamCall
}

func (u *mcpUpstream) Do(req *http.Request) (*http.Response, error) {
	call := mcpUpstreamCall{
		path:      req.URL.Path,
		accept:    req.Header.Get("Accept"),
		betaLines: append([]string(nil), req.Header.Values("anthropic-beta")...),
	}
	if req.Body != nil {
		call.body, _ = io.ReadAll(req.Body)
	}
	u.mu.Lock()
	u.calls = append(u.calls, call)
	u.mu.Unlock()
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
	body := `{"id":"msg_mcp","type":"message","role":"assistant","model":"claude-opus-4-8",` +
		`"stop_reason":"end_turn","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	switch {
	case call.path == "/v1/messages/count_tokens":
		body = `{"input_tokens":5}`
	case call.path == "/v1/messages/batches":
		body = `{"id":"batch_mcp","type":"message_batch","processing_status":"in_progress"}`
	case call.accept == "text/event-stream":
		resp.Header.Set("Content-Type", "text/event-stream")
		body = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	resp.Body = io.NopCloser(strings.NewReader(body))
	return resp, nil
}

func (u *mcpUpstream) recorded() []mcpUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]mcpUpstreamCall(nil), u.calls...)
}

func (u *mcpUpstream) requireNone(t *testing.T) {
	t.Helper()
	if calls := u.recorded(); len(calls) != 0 {
		paths := make([]string, len(calls))
		for i, c := range calls {
			paths[i] = c.path
		}
		t.Fatalf("refused request produced %d upstream HTTP request(s): %v", len(calls), paths)
	}
}

// mcpProxyDecider is the real decider over allow-all fakes, with the real Inference client
// on a recording transport. sizing turns on the count_tokens pre-flight, the only
// pre-forward upstream egress, so a zero-call assertion also covers it.
func mcpProxyDecider(sizing bool) (*inferenceProxyDecider, *claudeapi.Inference, *mcpUpstream) {
	a, mg, bg, kg, _ := allowAll()
	pol := allGatesOnExceptDLPAndCtx()
	pol.GateContextWindow = sizing
	d := newTestDecider(a, mg, bg, kg, fakeProxyPolicy{pol: pol})
	up := &mcpUpstream{}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		APIKey: "k-operator", Gateway: "direct", DefaultModel: "claude-opus-4-8", Doer: up,
	})
	d.inf = inf
	return d, inf, up
}

func serveProxy(t *testing.T, proxy http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal inbound body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer inbound-bearer")
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	return rec
}

// requireProxyRefusal asserts one fixed Anthropic-style error: status, error type and the
// exact public reason. A refusal is never an SSE stream.
func requireProxyRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, errType, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("refusal content type = %q, want application/json", ct)
	}
	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("refusal body is not an Anthropic error: %v (%s)", err, rec.Body.String())
	}
	if got.Type != "error" || got.Error.Type != errType || got.Error.Message != message {
		t.Fatalf("refusal = %s/%q, want %s/%q", got.Error.Type, got.Error.Message, errType, message)
	}
}

// unpreparedDecider is a decider that authorizes through the real chain and then loses
// the frozen artifact: the connector's legacy fallback would re-serialize the governed
// request instead of forwarding the verified bytes (Root correction m8).
type unpreparedDecider struct{ *inferenceProxyDecider }

func (u unpreparedDecider) Authorize(ctx context.Context, req claudeapi.MessageRequest, bearer string) claudeapi.ProxyDecision {
	dec := u.inferenceProxyDecider.Authorize(ctx, req, bearer)
	dec.Prepared = claudeapi.PreparedRequest{}
	return dec
}

func (u unpreparedDecider) AuthorizeBatch(ctx context.Context, requests []claudeapi.BatchRequest, bearer string) claudeapi.ProxyBatchDecision {
	dec := u.inferenceProxyDecider.AuthorizeBatch(ctx, requests, bearer)
	dec.Prepared = claudeapi.PreparedBatch{}
	return dec
}

// recordingComputerUseGate records the Tools view the computer-use gate receives.
type recordingComputerUseGate struct {
	dec   claudeapi.ComputerUseDecision
	in    claudeapi.ComputerUseInput
	calls int
}

func (g *recordingComputerUseGate) GovernComputerUse(_ context.Context, in claudeapi.ComputerUseInput) claudeapi.ComputerUseDecision {
	g.calls++
	g.in = in
	return g.dec
}

func computerTool() map[string]any {
	return map[string]any{"type": "computer_20250124", "name": "computer", "display_width_px": 1024, "display_height_px": 768}
}

// recordingAuditor keeps every ProxyAuditEvent the shell emits, so a privacy test inspects
// the SOC trail as well as HTTP, findings and logs.
type recordingAuditor struct {
	mu     sync.Mutex
	events []claudeapi.ProxyAuditEvent
}

func (a *recordingAuditor) Record(_ context.Context, ev claudeapi.ProxyAuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

// requireAuditClean requires at least one audit event and no canary in any of them.
func requireAuditClean(t *testing.T, a *recordingAuditor, canaries []string) {
	t.Helper()
	a.mu.Lock()
	events := append([]claudeapi.ProxyAuditEvent(nil), a.events...)
	a.mu.Unlock()
	if len(events) == 0 {
		t.Fatal("the shell emitted no audit event (vacuous audit check)")
	}
	for _, ev := range events {
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal audit event: %v", err)
		}
		requireNoCanary(t, "audit event", blob, canaries)
	}
}

func requireNoCanary(t *testing.T, where string, blob []byte, canaries []string) {
	t.Helper()
	for _, c := range canaries {
		if bytes.Contains(blob, []byte(c)) {
			t.Errorf("%s carries private MCP value %q: %s", where, c, blob)
		}
	}
}

// ---- coverage ------------------------------------------------------------------------

// TestProxyMCPEgressCoverageRefusesForwardOnlyGate is the transitional-incompatibility
// control: an adapter that predates MCP coverage returns Forward=true with no
// acknowledgment. Before this contract that forwarded the declared MCP destination
// ungoverned (E2-3). Now it is mcp_coverage_unavailable (503) with zero upstream requests
// on the blocking, streaming and batch routes. Expected on baseline: exit 1 by the status
// assertion (200 and upstream calls).
func TestProxyMCPEgressCoverageRefusesForwardOnlyGate(t *testing.T) {
	routes := []struct {
		name, path string
		body       any
		message    string
	}{
		{"blocking", "/v1/messages", mcpParams(false), "mcp_coverage_unavailable"},
		{"stream", "/v1/messages", mcpParams(true), "mcp_coverage_unavailable"},
		// A batch deny names its entry, like every per-entry gate (batchEntryDenyReason).
		{"batch", "/v1/messages/batches", batchBody(plainParams(false), mcpParams(false)),
			"batch entry c1 denied: mcp_coverage_unavailable"},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(true)
			gate := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
			d.egress = gate
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), rt.path, rt.body)
			requireProxyRefusal(t, rec, http.StatusServiceUnavailable, "api_error", rt.message)
			if gate.calls == 0 {
				t.Fatal("the egress gate was never consulted (vacuous refusal)")
			}
			up.requireNone(t)
		})
	}
}

// ---- binding: the connector fallback -----------------------------------------------

// TestProxyMCPEgressBindingRefusesUnpreparedFallback proves m8 through ServeHTTP: with an
// egress gate installed, even a request WITHOUT MCP carries an accepted binding (the
// accepted absence), so a decision that lost its frozen artifact is refused with the fixed
// 500 before any response or SSE header and before any upstream request. Expected on
// baseline: exit 1 by the status assertion (the legacy fallback forwards, 200).
func TestProxyMCPEgressBindingRefusesUnpreparedFallback(t *testing.T) {
	routes := []struct {
		name, path string
		body       any
	}{
		{"blocking", "/v1/messages", plainParams(false)},
		{"stream", "/v1/messages", plainParams(true)},
		{"batch", "/v1/messages/batches", batchBody(plainParams(false), plainParams(false))},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			d, inf, up := mcpProxyDecider(false)
			d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
			rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, unpreparedDecider{d}, nil, nil), rt.path, rt.body)
			requireProxyRefusal(t, rec, http.StatusInternalServerError, "api_error", "mcp_binding_changed")
			up.requireNone(t)
		})
	}
}

// TestProxyMCPEgressCompatibilityUnboundFallbackForwards is the positive control for the
// guard above: with NO egress gate (the Community default) nothing is bound, so the
// legacy fallback of a decider that sets no artifact still forwards. The guard keys on
// the binding, never on the absence of an artifact alone.
func TestProxyMCPEgressCompatibilityUnboundFallbackForwards(t *testing.T) {
	d, inf, up := mcpProxyDecider(false)
	rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, unpreparedDecider{d}, nil, nil), "/v1/messages", mcpParams(false))
	if rec.Code != http.StatusOK {
		t.Fatalf("unbound legacy fallback must forward: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls := up.recorded(); len(calls) != 1 || calls[0].path != "/v1/messages" {
		t.Fatalf("unbound legacy fallback made %d upstream call(s), want one /v1/messages", len(calls))
	}
}

// ---- privacy of the gate-facing views ----------------------------------------------

// TestProxyMCPEgressApprovalPrivacyGateInputCarriesNoMCPSecrets proves the egress gate
// sees each MCP tools[] slot as exactly {"type":"mcp_toolset"} and no server name,
// per-tool name, URL path/query or authorization token anywhere in its input. Expected on
// baseline: exit 1 by the marker assertion (the gate saw the caller's toolset map).
func TestProxyMCPEgressApprovalPrivacyGateInputCarriesNoMCPSecrets(t *testing.T) {
	d, inf, up := mcpProxyDecider(true)
	gate := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: false, Status: http.StatusForbidden}}
	d.egress = gate
	_ = serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
	if gate.calls != 1 {
		t.Fatalf("egress gate calls = %d, want 1", gate.calls)
	}
	if len(gate.gotIn.Tools) != 1 || !reflect.DeepEqual(gate.gotIn.Tools[0], mcpMarker()) {
		t.Errorf("gate Tools = %#v, want exactly one {\"type\":\"mcp_toolset\"} marker", gate.gotIn.Tools)
	}
	blob, err := json.Marshal(gate.gotIn)
	if err != nil {
		t.Fatalf("marshal gate input: %v", err)
	}
	requireNoCanary(t, "egress gate input", blob, mcpWireCanaries())
	up.requireNone(t)
}

// TestProxyMCPEgressComputerUseViewSanitized proves m7 for the one later gate that receives
// Tools: the computer-use gate sees the MCP slot as the marker, with the Community default
// (no egress gate) as well, while the forwarded body still carries the original values.
// Expected on baseline: exit 1 by the marker assertion.
func TestProxyMCPEgressComputerUseViewSanitized(t *testing.T) {
	a, mg, bg, kg, pol := allowAll()
	d := newTestDecider(a, mg, bg, kg, pol)
	cu := &recordingComputerUseGate{dec: claudeapi.ComputerUseDecision{Forward: true}}
	d.computerUse = cu
	req := userReq("hi", false)
	req.Tools = []any{computerTool(), mcpToolsetMap(mcpCanaryName)}
	req.MCPServers = []any{mcpServerMap(mcpCanaryName, mcpTestURL)}

	dec := d.Authorize(context.Background(), req, "bearer")
	if !dec.Allow {
		t.Fatalf("computer-use forward must allow: status=%d reason=%q", dec.Status, dec.Reason)
	}
	if cu.calls != 1 {
		t.Fatalf("computer-use gate calls = %d, want 1", cu.calls)
	}
	if len(cu.in.Tools) != 2 || !reflect.DeepEqual(cu.in.Tools[1], mcpMarker()) {
		t.Errorf("computer-use Tools = %#v, want the computer tool then the MCP marker", cu.in.Tools)
	}
	if len(cu.in.Tools) > 0 && !reflect.DeepEqual(cu.in.Tools[0], computerTool()) {
		t.Errorf("computer-use gate lost its own tool: %#v", cu.in.Tools[0])
	}
	blob, _ := json.Marshal(cu.in)
	requireNoCanary(t, "computer-use gate input", blob, mcpWireCanaries())
	body := dec.Prepared.Body()
	for _, c := range []string{mcpCanaryName, mcpCanaryTool, mcpCanaryToken} {
		if !bytes.Contains(body, []byte(c)) {
			t.Errorf("forwarded body lost the declared MCP value %q", c)
		}
	}
}

// TestProxyMCPEgressApprovalPrivacyAdapterTextWithheld proves m1's privacy half: for a
// request that declares MCP, the adapter's free-form Reason and finding Title/Detail/Kind
// are never relayed. A legacy-family deny keeps its validated status and error type but
// publishes a fixed reason; findings use a fixed template; log lines carry no canary.
// Expected on baseline: exit 1 (the adapter Reason is the HTTP message).
func TestProxyMCPEgressApprovalPrivacyAdapterTextWithheld(t *testing.T) {
	const adapterCanary = "ADAPTER-FREE-TEXT-CANARY"
	canaries := append(mcpWireCanaries(), adapterCanary)
	leak := adapterCanary + " " + strings.Join(mcpWireCanaries(), " ")

	d, inf, up := mcpProxyDecider(true)
	bus := &fakeObservationBus{}
	d.bus = bus
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
		Forward: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "denied: " + leak,
		Findings: []claudeapi.ServerToolEgressFinding{{
			Kind: "servertool_egress_" + adapterCanary, Severity: "high", Title: "blocked " + leak,
			Family: "mcp", ToolType: "mcp_toolset", Detail: leak, OWASPLLM: []string{"LLM02:2025", adapterCanary},
		}},
	}}
	aud := &recordingAuditor{}
	rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, aud, nil), "/v1/messages", mcpParams(false))

	requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "server-tool egress denied by policy")
	published := 0
	for _, e := range bus.events {
		f, ok := event.FindingOf(e)
		if !ok {
			continue
		}
		published++
		blob, _ := json.Marshal(f)
		requireNoCanary(t, "published egress finding", blob, canaries)
	}
	if published == 0 {
		t.Error("the adapter's finding was not published at all (the fixed template must still publish)")
	}
	requireNoCanary(t, "decider log", logs.Bytes(), canaries)
	requireAuditClean(t, aud, canaries)
	up.requireNone(t)
}

// ---- compatibility -------------------------------------------------------------------

// TestProxyMCPEgressCompatibilityNilGateForwardsMCP is the Community positive control: with
// no egress gate there is no new MCP authorization requirement. The declared destination
// is sized and forwarded with the current beta exactly once and its URL, token and
// configuration unchanged. Green on baseline by design (the beta prerequisite).
func TestProxyMCPEgressCompatibilityNilGateForwardsMCP(t *testing.T) {
	d, inf, up := mcpProxyDecider(true)
	rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil gate must forward Community MCP: status=%d body=%s", rec.Code, rec.Body.String())
	}
	calls := up.recorded()
	if len(calls) != 2 || calls[0].path != "/v1/messages/count_tokens" || calls[1].path != "/v1/messages" {
		t.Fatalf("upstream calls = %d, want count_tokens then messages", len(calls))
	}
	for _, c := range calls {
		requireMCPBetaOnce(t, c)
		requireMCPDeclarationForwarded(t, c.body)
	}
}

// TestProxyMCPEgressCompatibilityEarlierDenyWins proves the MCP capture is not a new first
// gate: an earlier model-access or DLP deny keeps its precedence and its own reason, the
// egress gate is never consulted and nothing leaves the process. Green on baseline.
func TestProxyMCPEgressCompatibilityEarlierDenyWins(t *testing.T) {
	t.Run("model-access", func(t *testing.T) {
		d, inf, up := mcpProxyDecider(true)
		d.models.(*fakeProxyModels).denyModel = "claude-opus-4-8"
		gate := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
		d.egress = gate
		rec := serveProxy(t, claudeapi.NewMessagesProxy(inf, d, nil, nil), "/v1/messages", mcpParams(false))
		requireProxyRefusal(t, rec, http.StatusForbidden, "permission_error", "model not granted on this surface")
		if gate.calls != 0 {
			t.Fatalf("egress gate ran after an earlier model-access deny (%d calls)", gate.calls)
		}
		up.requireNone(t)
	})
	t.Run("dlp", func(t *testing.T) {
		ipx := inferenceproxy.New()
		st, tenant := provisionTenant(t, ipx, "")
		inf, doer := proxyCountTokensInference(5)
		d := storeBackedDecider(st, tenant, ipx, "", nil, inf)
		gate := &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{Forward: true}}
		d.egress = gate
		req := userReq("use AKIAIOSFODNN7EXAMPLE for deploy", false)
		req.Tools = []any{mcpToolsetMap(mcpCanaryName)}
		req.MCPServers = []any{mcpServerMap(mcpCanaryName, mcpTestURL)}
		dec := d.Authorize(context.Background(), req, "bearer")
		if dec.Allow || !strings.Contains(dec.Reason, "data-loss-prevention") {
			t.Fatalf("DLP must deny first: allow=%v reason=%q", dec.Allow, dec.Reason)
		}
		if gate.calls != 0 || doer.calls != 0 {
			t.Fatalf("after a DLP deny: egress gate calls=%d count_tokens calls=%d, want 0/0", gate.calls, doer.calls)
		}
	})
}

func requireMCPBetaOnce(t *testing.T, c mcpUpstreamCall) {
	t.Helper()
	n := 0
	for _, line := range c.betaLines {
		for _, v := range strings.Split(line, ",") {
			switch strings.TrimSpace(v) {
			case mcpCurrentBeta:
				n++
			case "mcp-client-2025-04-04":
				t.Errorf("%s carried the deprecated MCP beta", c.path)
			}
		}
	}
	if n != 1 {
		t.Errorf("%s carried the current MCP beta %d times, want 1 (lines %q)", c.path, n, c.betaLines)
	}
}

// requireMCPDeclarationForwarded decodes the upstream body and requires the original
// server URL/token/name and the toolset's per-tool configuration at their positions.
func requireMCPDeclarationForwarded(t *testing.T, body []byte) {
	t.Helper()
	var sent struct {
		Tools      []map[string]any `json:"tools"`
		MCPServers []map[string]any `json:"mcp_servers"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	wantServer := map[string]any{"type": "url", "name": mcpCanaryName, "url": mcpTestURL, "authorization_token": mcpCanaryToken}
	if len(sent.MCPServers) != 1 || !reflect.DeepEqual(sent.MCPServers[0], wantServer) {
		t.Errorf("forwarded mcp_servers = %#v, want %#v", sent.MCPServers, wantServer)
	}
	var mcpTools []map[string]any
	for _, tool := range sent.Tools {
		if tool["type"] == "mcp_toolset" {
			mcpTools = append(mcpTools, tool)
		}
	}
	if len(mcpTools) != 1 || !reflect.DeepEqual(mcpTools[0], mcpToolsetMap(mcpCanaryName)) {
		t.Errorf("forwarded toolset = %#v, want %#v", mcpTools, mcpToolsetMap(mcpCanaryName))
	}
}

// ---- O-1: notification callers never consume break-glass -----------------------------

// TestProxyEgressNotificationNeverConsumesBreakGlass is the O-1 regression against the REAL
// Engine: a server-tool egress deny opens its approval REQUEST, but that request cannot
// authorize the synchronous call, so it must not consume an active break-glass grant nor
// leave an authorized-under-break-glass use record. The actuation positive control at the
// end proves a real gateOnce caller keeps its qualified break-glass behavior. Expected on
// baseline: exit 1 by the zero-use assertion (gateOnce consumed one use).
func TestProxyEgressNotificationNeverConsumesBreakGlass(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	grant := h.activateBreakGlassE2E(t, "", "O-1 control: an emergency grant is not a notification")

	a, mg, bg, kg, pol := allowAll()
	a.p = auth.ScopedPrincipal(model.ID("u1"), "user one", tid, "editor")
	d := newTestDecider(a, mg, bg, kg, pol)
	d.approvals = br
	d.egress = &fakeEgressGate{dec: claudeapi.ServerToolEgressDecision{
		Forward: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "no egress grant",
		ApprovalIntent: &claudeapi.ServerToolEgressApprovalIntent{
			Action: "inference.servertool.egress", Family: "web_search", ToolType: "web_search_20260209",
			Subject: "web_search", Reason: "grant web search", PlanHash: "plan-o1-egress",
		},
	}}
	dec := d.Authorize(context.Background(), reqWithTools(map[string]any{"type": "web_search_20260209", "name": "web_search"}), "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("an egress deny must stay a 403 under an active grant: allow=%v status=%d", dec.Allow, dec.Status)
	}
	requireBreakGlassUses(t, h, grant, 0)
	requirePendingApprovals(t, h, "inference.servertool.egress", 1)

	_, st, _, err := br.gateOnce(context.Background(), tid, "deploy.apply", "deployment", "svc/o1", "plan-o1-actuation", "incident", "tester")
	if err != nil || st != nbBreakGlass {
		t.Fatalf("actuation positive control: gateOnce = %q err=%v, want break_glass", st, err)
	}
	requireBreakGlassUses(t, h, grant, 1)
}

// TestProxyComputerUseNotificationNeverConsumesBreakGlass is the same O-1 regression for the
// computer-use notification caller.
func TestProxyComputerUseNotificationNeverConsumesBreakGlass(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	grant := h.activateBreakGlassE2E(t, "", "O-1 control: an emergency grant is not a notification")

	a, mg, bg, kg, pol := allowAll()
	a.p = auth.ScopedPrincipal(model.ID("u1"), "user one", tid, "editor")
	d := newTestDecider(a, mg, bg, kg, pol)
	d.approvals = br
	d.computerUse = &recordingComputerUseGate{dec: claudeapi.ComputerUseDecision{
		Forward: false, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: "computer use not permitted",
		ApprovalIntent: &claudeapi.ComputerUseApprovalIntent{
			Action: "inference.computer_use", ToolType: "computer_20250124", Subject: "computer_use",
			Reason: "grant computer use", PlanHash: "plan-o1-computer",
		},
	}}
	req := userReq("hi", false)
	req.Tools = []any{computerTool()}
	dec := d.Authorize(context.Background(), req, "bearer")
	if dec.Allow || dec.Status != http.StatusForbidden {
		t.Fatalf("a computer-use deny must stay a 403 under an active grant: allow=%v status=%d", dec.Allow, dec.Status)
	}
	requireBreakGlassUses(t, h, grant, 0)
	requirePendingApprovals(t, h, "inference.computer_use", 1)
}

func requireBreakGlassUses(t *testing.T, h *harness, grant string, want int) {
	t.Helper()
	uses := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/breakglass/"+grant+"/uses")
	items, _ := uses["items"].([]any)
	if len(items) != want {
		t.Fatalf("break-glass grant uses = %d, want %d: %v", len(items), want, items)
	}
}

func requirePendingApprovals(t *testing.T, h *harness, action string, want int) {
	t.Helper()
	list := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?status=pending&action="+action+"&limit=200")
	items, _ := list["items"].([]any)
	if len(items) != want {
		t.Fatalf("pending %s approvals = %d, want %d", action, len(items), want)
	}
}

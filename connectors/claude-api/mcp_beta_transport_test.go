// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// mcp_beta_transport_test.go — the MCP beta TRANSPORT oracle (C8 E2-3 prerequisite;
// accepted MCP contract §7 and Root adjudication m9).
//
// The Messages, count_tokens and batch-create endpoint schemas each declare the MCP
// fields and the CURRENT beta mcp-client-2025-11-20. These tests observe the ACTUAL
// HTTP request every submitter builds — CreateMessage, StreamMessage, CountTokens, the
// frozen prepared blocking/stream/batch artifacts, and both legacy batch submitters —
// and require of each: the current MCP beta travels exactly once when the request
// DECLARES MCP (an mcp_toolset tool in its typed, pointer or map form, or a non-empty
// mcp_servers[]), the deprecated mcp-client-2025-04-04 never travels, the declared
// URL/token/configuration values and the client's configured transport reach the wire
// unchanged, and a request WITHOUT MCP keeps sending no MCP beta.
//
// SCOPE LIMIT: this file is TRANSPORT only. It asserts no grant, approval, snapshot
// binding or origin authorization — MCP egress enforcement (E2-3) remains open and is
// the separate reviewed enforcement pair's oracle, not this one.
package claudeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ---- capture fixture --------------------------------------------------------------

// Canned upstream responses, one per endpoint shape the submitters decode.
const (
	mcpOKMessage = `{"id":"msg_mcp","type":"message","role":"assistant","model":"claude-opus-4-8",` +
		`"stop_reason":"end_turn","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`
	mcpOKBatch  = `{"id":"batch_mcp","type":"message_batch","processing_status":"in_progress"}`
	mcpOKCount  = `{"input_tokens":42}`
	mcpOKStream = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
)

// The declared MCP destination. Its URL, token, name and per-tool configuration are
// forwarding data: adding the beta header must not rewrite, drop or reorder any of them.
const (
	mcpTestServerName  = "acme-tools"
	mcpTestServerURL   = "https://mcp.example.com:8443/sse?tenant=acme"
	mcpTestServerToken = "tok-mcp-do-not-rewrite"
	mcpTestToolName    = "read_file"
	mcpTestAPIKey      = "k-mcp-inference"
	mcpTestModel       = "claude-opus-4-8"
)

// mcpWireCall is one observed upstream HTTP request: the submitter that produced it
// (method, path and Accept discriminate blocking, streaming and batch), the exact
// transmitted body, and the transport values that must survive the beta correction.
type mcpWireCall struct {
	method    string
	path      string
	url       string
	accept    string
	beta      string
	betaLines []string
	apiKey    string
	version   string
	body      []byte
}

// mcpWireDoer records every request the real InferenceClient builds and answers it with
// a canned JSON body, or with an SSE stream when the submitter asked for one. It keeps
// ALL calls, so a test can prove an early refusal produced zero upstream requests.
type mcpWireDoer struct {
	calls []mcpWireCall
	json  string
}

func (d *mcpWireDoer) Do(req *http.Request) (*http.Response, error) {
	call := mcpWireCall{
		method:    req.Method,
		path:      req.URL.Path,
		url:       req.URL.String(),
		accept:    req.Header.Get("Accept"),
		beta:      req.Header.Get(betaHeaderKey),
		betaLines: append([]string(nil), req.Header.Values(betaHeaderKey)...),
		apiKey:    req.Header.Get("x-api-key"),
		version:   req.Header.Get("anthropic-version"),
	}
	if req.Body != nil {
		call.body, _ = io.ReadAll(req.Body)
	}
	d.calls = append(d.calls, call)
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
	if call.accept == "text/event-stream" {
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Body = io.NopCloser(strings.NewReader(mcpOKStream))
		return resp, nil
	}
	body := d.json
	if body == "" {
		body = mcpOKMessage
	}
	resp.Body = io.NopCloser(strings.NewReader(body))
	return resp, nil
}

// only returns the single observed request, failing when a submitter made none or more
// than one (a test asserts about one exact HTTP call, never about a helper's return).
func (d *mcpWireDoer) only(t *testing.T) mcpWireCall {
	t.Helper()
	if len(d.calls) != 1 {
		t.Fatalf("observed %d upstream HTTP requests, want exactly 1", len(d.calls))
	}
	return d.calls[0]
}

func newMCPInference(d *mcpWireDoer) *Inference {
	return NewInference(InferenceConfig{APIKey: mcpTestAPIKey, DefaultModel: mcpTestModel, Doer: d})
}

// ---- declaration forms ------------------------------------------------------------

// mcpTypedToolset is the ordinary typed declaration (mcp.go MCPToolset).
func mcpTypedToolset() MCPToolset {
	return MCPToolset{
		Type:          "mcp_toolset",
		MCPServerName: mcpTestServerName,
		DefaultConfig: MCPToolConfig{Enabled: false},
		Configs:       map[string]MCPToolConfig{mcpTestToolName: {Enabled: true}},
	}
}

// mcpPointerToolset is the same declaration passed by pointer.
func mcpPointerToolset() *MCPToolset {
	ts := mcpTypedToolset()
	return &ts
}

// mcpMapToolset is the hand-built map form a caller may send instead of the typed one.
// It is also the ALIAS a prepared-freeze control mutates after the freeze.
func mcpMapToolset() map[string]any {
	return map[string]any{
		"type":            "mcp_toolset",
		"mcp_server_name": mcpTestServerName,
		"default_config":  map[string]any{"enabled": false},
		"configs":         map[string]any{mcpTestToolName: map[string]any{"enabled": true}},
	}
}

// mcpTypedServers is the request's mcp_servers[] with one declared destination.
func mcpTypedServers() []any {
	return []any{MCPServer{
		Type:               "url",
		Name:               mcpTestServerName,
		URL:                mcpTestServerURL,
		AuthorizationToken: mcpTestServerToken,
	}}
}

// mcpMapServers is the same destination as a caller-owned map, so a control can mutate
// the caller's alias after a freeze.
func mcpMapServers() []any {
	return []any{map[string]any{
		"type":                "url",
		"name":                mcpTestServerName,
		"url":                 mcpTestServerURL,
		"authorization_token": mcpTestServerToken,
	}}
}

// mcpPrompt is the prompt body every case shares; the beta correction must not touch it.
func mcpPrompt() MessageRequest {
	return MessageRequest{
		Model:     mcpTestModel,
		MaxTokens: 64,
		System:    []ContentBlock{TextBlock("rubric")},
		Messages:  []Message{{Role: roleUser, Content: []ContentBlock{TextBlock("list the files")}}},
	}
}

// ---- assertions -------------------------------------------------------------------

// mcpBetaValues splits the transmitted anthropic-beta header into its values.
func mcpBetaValues(header string) []string {
	var out []string
	for _, v := range strings.Split(header, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// assertMCPBetaOnce requires the CURRENT MCP beta exactly once on this exact request,
// the deprecated value absent, and the configured transport unchanged.
func assertMCPBetaOnce(t *testing.T, call mcpWireCall, wantPath string) {
	t.Helper()
	if call.path != wantPath {
		t.Fatalf("request hit %q, want %q", call.path, wantPath)
	}
	seen := 0
	for _, v := range mcpBetaValues(call.beta) {
		switch v {
		case MCPBetaHeader:
			seen++
		case MCPBetaHeaderDeprecated:
			t.Errorf("%s carried the DEPRECATED MCP beta %q: %q", wantPath, MCPBetaHeaderDeprecated, call.beta)
		}
	}
	if seen != 1 {
		t.Errorf("%s carried %q %d times, want exactly 1 (anthropic-beta = %q)", wantPath, MCPBetaHeader, seen, call.beta)
	}
	assertTransportUnchanged(t, call)
}

// assertNoMCPBeta requires that a request WITHOUT an MCP declaration keeps its existing
// MCP-header absence — neither the current nor the deprecated value.
func assertNoMCPBeta(t *testing.T, call mcpWireCall, wantPath string) {
	t.Helper()
	if call.path != wantPath {
		t.Fatalf("request hit %q, want %q", call.path, wantPath)
	}
	for _, v := range mcpBetaValues(call.beta) {
		if v == MCPBetaHeader || v == MCPBetaHeaderDeprecated {
			t.Errorf("non-MCP request to %s carried %q (anthropic-beta = %q)", wantPath, v, call.beta)
		}
	}
	assertTransportUnchanged(t, call)
}

// assertBetaPresent requires an unrelated beta family to survive the MCP correction.
func assertBetaPresent(t *testing.T, call mcpWireCall, want string) {
	t.Helper()
	for _, v := range mcpBetaValues(call.beta) {
		if v == want {
			return
		}
	}
	t.Errorf("beta %q was dropped from %s (anthropic-beta = %q)", want, call.path, call.beta)
}

// assertBetaAbsent requires a beta family the endpoint never takes to stay off the wire.
func assertBetaAbsent(t *testing.T, call mcpWireCall, unwanted string) {
	t.Helper()
	for _, v := range mcpBetaValues(call.beta) {
		if v == unwanted {
			t.Errorf("beta %q must not reach %s (anthropic-beta = %q)", unwanted, call.path, call.beta)
		}
	}
}

// assertTransportUnchanged requires the client's configured endpoint and credential
// headers to be exactly what NewInference was given: the beta correction adds a header,
// it does not touch the URL, the key or the API version.
func assertTransportUnchanged(t *testing.T, call mcpWireCall) {
	t.Helper()
	if len(call.betaLines) > 1 {
		t.Errorf("multiple anthropic-beta header lines: %q", call.betaLines)
	}
	if call.method != http.MethodPost {
		t.Errorf("method = %q, want POST", call.method)
	}
	if !strings.HasPrefix(call.url, defaultBaseURL) {
		t.Errorf("URL %q does not start with the configured base %q", call.url, defaultBaseURL)
	}
	if call.apiKey != mcpTestAPIKey {
		t.Errorf("x-api-key = %q, want the configured credential", call.apiKey)
	}
	if call.version != defaultAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", call.version, defaultAnthropicVersion)
	}
}

// assertServerValuesForwarded requires the declared destination to reach the wire byte
// for byte: URL, authorization token, server name and per-tool configuration.
func assertServerValuesForwarded(t *testing.T, body []byte) {
	t.Helper()
	for _, want := range []string{mcpTestServerURL, mcpTestServerToken, mcpTestServerName} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("declared MCP value %q missing from the transmitted body: %s", want, body)
		}
	}
}

// assertToolConfigForwarded requires the declared per-tool configuration to survive.
func assertToolConfigForwarded(t *testing.T, body []byte) {
	t.Helper()
	type config struct {
		Enabled *bool `json:"enabled"`
	}
	var prompt struct {
		Tools []struct {
			Type    string            `json:"type"`
			Server  string            `json:"mcp_server_name"`
			Default *config           `json:"default_config"`
			Configs map[string]config `json:"configs"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &prompt); err != nil {
		t.Fatalf("decode transmitted prompt: %v", err)
	}
	for _, tool := range prompt.Tools {
		if tool.Type == "mcp_toolset" {
			selected, ok := tool.Configs[mcpTestToolName]
			if tool.Server != mcpTestServerName || tool.Default == nil || tool.Default.Enabled == nil ||
				*tool.Default.Enabled || !ok || selected.Enabled == nil || !*selected.Enabled {
				t.Errorf("MCP tool configuration changed: %+v", tool)
			}
			return
		}
	}
	t.Fatal("transmitted prompt has no MCP toolset")
}

// assertPromptForwarded requires the prompt itself to be unchanged by the correction.
func assertPromptForwarded(t *testing.T, body []byte) {
	t.Helper()
	if !bytes.Contains(body, []byte("list the files")) {
		t.Errorf("prompt missing from the transmitted body: %s", body)
	}
}

// mcpDeclarationCases are the declaration forms every direct submitter must recognize.
var mcpDeclarationCases = []struct {
	name        string
	tools       []any
	servers     []any
	wantServers bool
	wantConfig  bool
}{
	{name: "typed toolset and servers", tools: []any{mcpTypedToolset()}, servers: mcpTypedServers(), wantServers: true, wantConfig: true},
	{name: "pointer toolset and servers", tools: []any{mcpPointerToolset()}, servers: mcpTypedServers(), wantServers: true, wantConfig: true},
	{name: "map toolset and servers", tools: []any{mcpMapToolset()}, servers: mcpMapServers(), wantServers: true, wantConfig: true},
	{name: "servers only", servers: mcpTypedServers(), wantServers: true},
	{name: "typed toolset only", tools: []any{mcpTypedToolset()}, wantConfig: true},
	{name: "pointer toolset only", tools: []any{mcpPointerToolset()}, wantConfig: true},
	{name: "map toolset only", tools: []any{mcpMapToolset()}, wantConfig: true},
}

// ---- direct Messages submitters ---------------------------------------------------

// TestMCPBetaTransportCreateMessage proves the blocking Messages submitter sends the
// current MCP beta once for every declaration form, and forwards the declared values.
func TestMCPBetaTransportCreateMessage(t *testing.T) {
	for _, tc := range mcpDeclarationCases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &mcpWireDoer{}
			req := mcpPrompt()
			req.Tools, req.MCPServers = tc.tools, tc.servers
			if _, err := newMCPInference(doer).CreateMessage(context.Background(), req); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			call := doer.only(t)
			assertMCPBetaOnce(t, call, messagesPath)
			assertPromptForwarded(t, call.body)
			if tc.wantServers {
				assertServerValuesForwarded(t, call.body)
			}
			if tc.wantConfig {
				assertToolConfigForwarded(t, call.body)
			}
		})
	}
}

// TestMCPBetaTransportStreamMessage proves the STREAMING submitter carries the same
// header: a declared MCP destination is not a blocking-only shape.
func TestMCPBetaTransportStreamMessage(t *testing.T) {
	for _, tc := range mcpDeclarationCases {
		t.Run(tc.name, func(t *testing.T) {
			doer := &mcpWireDoer{}
			req := mcpPrompt()
			req.Tools, req.MCPServers = tc.tools, tc.servers
			if _, err := newMCPInference(doer).StreamMessage(context.Background(), req, func(StreamEvent) error { return nil }); err != nil {
				t.Fatalf("StreamMessage: %v", err)
			}
			call := doer.only(t)
			if call.accept != "text/event-stream" {
				t.Fatalf("Accept = %q, want the streaming submitter", call.accept)
			}
			assertMCPBetaOnce(t, call, messagesPath)
			assertPromptForwarded(t, call.body)
			if tc.wantServers {
				assertServerValuesForwarded(t, call.body)
			}
			if tc.wantConfig {
				assertToolConfigForwarded(t, call.body)
			}
		})
	}
}

// ---- count_tokens -----------------------------------------------------------------

// TestMCPBetaTransportCountTokens proves the sizing endpoint carries the MCP beta for a
// servers-only, tools-only and combined declaration, keeps its prompt-only body, and
// still excludes the runtime betas (task budgets, server-side fallback) that endpoint
// never takes.
func TestMCPBetaTransportCountTokens(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tools       []any
		servers     []any
		wantServers bool
	}{
		{name: "servers only", servers: mcpTypedServers(), wantServers: true},
		{name: "tools only", tools: []any{mcpTypedToolset()}},
		{name: "servers and tools", tools: []any{mcpTypedToolset()}, servers: mcpTypedServers(), wantServers: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doer := &mcpWireDoer{json: mcpOKCount}
			req := mcpPrompt()
			req.Tools, req.MCPServers = tc.tools, tc.servers
			// Runtime-only params and their betas: the count body and its header must
			// keep excluding them while gaining the MCP beta.
			req.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(50000)}
			req.Fallbacks = []Fallback{{Model: "claude-sonnet-4-6"}}
			req.StopSequences = []string{"STOP"}
			tokens, err := newMCPInference(doer).CountTokens(context.Background(), req)
			if err != nil {
				t.Fatalf("CountTokens: %v", err)
			}
			if tokens.InputTokens != 42 {
				t.Errorf("input_tokens = %d, want 42 (the count value must not change)", tokens.InputTokens)
			}
			call := doer.only(t)
			assertMCPBetaOnce(t, call, countTokensPath)
			assertBetaAbsent(t, call, BetaTaskBudgets)
			assertBetaAbsent(t, call, BetaServerSideFallback)
			assertPromptForwarded(t, call.body)
			if len(tc.tools) > 0 {
				assertToolConfigForwarded(t, call.body)
			}
			if tc.wantServers {
				assertServerValuesForwarded(t, call.body)
			}
			for _, banned := range []string{`"max_tokens"`, `"stream"`, `"stop_sequences"`, `"output_config"`, `"fallbacks"`} {
				if bytes.Contains(call.body, []byte(banned)) {
					t.Errorf("count_tokens body carried the runtime param %s: %s", banned, call.body)
				}
			}
		})
	}
}

// ---- frozen prepared artifacts ----------------------------------------------------

// TestMCPBetaTransportPreparedMessage proves the FROZEN blocking artifact carries the
// MCP beta derived at freeze time, that the forwarded octets are still exactly the
// frozen octets, and that mutating the caller's request and its map ALIASES after the
// freeze can neither remove nor alter that beta.
func TestMCPBetaTransportPreparedMessage(t *testing.T) {
	doer := &mcpWireDoer{}
	toolset := mcpMapToolset()
	servers := mcpMapServers()
	req := mcpPrompt()
	req.Tools, req.MCPServers = []any{toolset}, servers
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, err := MarshalPrepared(norm)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	// Post-freeze mutation of the caller's own inputs and of the maps the frozen body
	// was built from.
	req.Tools, req.MCPServers = nil, nil
	norm.Tools, norm.MCPServers = nil, nil
	toolset["type"] = "custom_tool"
	servers[0].(map[string]any)["url"] = "https://attacker.example.com"

	if _, err := newMCPInference(doer).ForwardPrepared(context.Background(), prep); err != nil {
		t.Fatalf("ForwardPrepared: %v", err)
	}
	call := doer.only(t)
	assertMCPBetaOnce(t, call, messagesPath)
	if !bytes.Equal(call.body, prep.Body()) {
		t.Fatalf("forwarded bytes != frozen bytes\n got:  %s\n want: %s", call.body, prep.Body())
	}
	assertServerValuesForwarded(t, call.body)
	assertToolConfigForwarded(t, call.body)
	if bytes.Contains(call.body, []byte("attacker.example.com")) {
		t.Errorf("a post-freeze alias mutation reached the wire: %s", call.body)
	}
}

// TestMCPBetaTransportPreparedStream proves the frozen STREAMING artifact carries the
// same beta and the same exact bytes.
func TestMCPBetaTransportPreparedStream(t *testing.T) {
	doer := &mcpWireDoer{}
	req := mcpPrompt()
	req.Stream = true
	toolset, servers := mcpMapToolset(), mcpMapServers()
	req.Tools, req.MCPServers = []any{toolset}, servers
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, err := MarshalPrepared(norm)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	toolset["type"] = "custom_tool"
	servers[0].(map[string]any)["url"] = "https://attacker.example.com"
	if !prep.Stream() {
		t.Fatal("the prepared artifact must carry the stream flag")
	}
	if _, err := newMCPInference(doer).ForwardPreparedStream(context.Background(), prep, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("ForwardPreparedStream: %v", err)
	}
	call := doer.only(t)
	if call.accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want the streaming submitter", call.accept)
	}
	assertMCPBetaOnce(t, call, messagesPath)
	assertServerValuesForwarded(t, call.body)
	assertToolConfigForwarded(t, call.body)
	if !bytes.Equal(call.body, prep.Body()) {
		t.Fatalf("streamed bytes != frozen bytes\n got:  %s\n want: %s", call.body, prep.Body())
	}
}

// TestMCPBetaTransportPreparedMessageWithoutMCP proves the freeze is the only source of
// the header: a non-MCP artifact stays without an MCP beta even when the caller adds an
// MCP declaration to the source request AFTER the freeze, and its unrelated beta family
// is preserved.
func TestMCPBetaTransportPreparedMessageWithoutMCP(t *testing.T) {
	doer := &mcpWireDoer{}
	req := mcpPrompt()
	req.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(50000)}
	norm, err := NormalizeMessageRequest(req, "")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	prep, err := MarshalPrepared(norm)
	if err != nil {
		t.Fatalf("marshal prepared: %v", err)
	}
	norm.Tools, norm.MCPServers = []any{mcpTypedToolset()}, mcpTypedServers()
	req.Tools, req.MCPServers = []any{mcpTypedToolset()}, mcpTypedServers()

	if _, err := newMCPInference(doer).ForwardPrepared(context.Background(), prep); err != nil {
		t.Fatalf("ForwardPrepared: %v", err)
	}
	call := doer.only(t)
	assertNoMCPBeta(t, call, messagesPath)
	assertBetaPresent(t, call, BetaTaskBudgets)
	if !bytes.Equal(call.body, prep.Body()) {
		t.Fatalf("forwarded bytes != frozen bytes\n got:  %s\n want: %s", call.body, prep.Body())
	}
}

// TestMCPBetaTransportPreparedBatch proves the frozen batch envelope derives its MCP
// beta from the UNION of its entries — MCP declared only in entry 2 carries the header
// for the whole submission — sends it exactly once for repeated entries, and survives
// post-freeze mutation of the caller's entries.
func TestMCPBetaTransportPreparedBatch(t *testing.T) {
	t.Run("mcp only in entry 2", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		toolset := mcpMapToolset()
		servers := mcpMapServers()
		second := mcpPrompt()
		second.Tools, second.MCPServers = []any{toolset}, servers
		entries := []BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
			{CustomID: "c1", Params: second},
		}
		prep, err := MarshalPreparedBatch(entries)
		if err != nil {
			t.Fatalf("marshal prepared batch: %v", err)
		}
		// Post-freeze mutation of the caller's entries and of their map aliases.
		entries[1].Params.Tools, entries[1].Params.MCPServers = nil, nil
		entries[1].CustomID = "mutated-after-freeze"
		toolset["type"] = "custom_tool"
		servers[0].(map[string]any)["authorization_token"] = "tok-rewritten"

		if _, _, err := newMCPInference(doer).ForwardPreparedBatch(context.Background(), prep); err != nil {
			t.Fatalf("ForwardPreparedBatch: %v", err)
		}
		call := doer.only(t)
		assertMCPBetaOnce(t, call, batchesPath)
		if !bytes.Equal(call.body, prep.Body()) {
			t.Fatalf("forwarded batch bytes != frozen bytes\n got:  %s\n want: %s", call.body, prep.Body())
		}
		assertServerValuesForwarded(t, call.body)
		if bytes.Contains(call.body, []byte("tok-rewritten")) {
			t.Errorf("a post-freeze alias mutation reached the wire: %s", call.body)
		}
	})

	t.Run("repeated mcp entries send the beta once", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		entry := mcpPrompt()
		entry.Tools, entry.MCPServers = []any{mcpTypedToolset()}, mcpTypedServers()
		prep, err := MarshalPreparedBatch([]BatchRequest{
			{CustomID: "c0", Params: entry},
			{CustomID: "c1", Params: entry},
			{CustomID: "c2", Params: entry},
		})
		if err != nil {
			t.Fatalf("marshal prepared batch: %v", err)
		}
		if _, _, err := newMCPInference(doer).ForwardPreparedBatch(context.Background(), prep); err != nil {
			t.Fatalf("ForwardPreparedBatch: %v", err)
		}
		call := doer.only(t)
		assertMCPBetaOnce(t, call, batchesPath)
		if !bytes.Equal(call.body, prep.Body()) {
			t.Fatal("repeated-entry batch changed after freezing")
		}
		var envelope struct {
			Requests []struct {
				Params json.RawMessage `json:"params"`
			} `json:"requests"`
		}
		if err := json.Unmarshal(call.body, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Requests) != 3 {
			t.Fatalf("entries = %d, want 3", len(envelope.Requests))
		}
		for _, entry := range envelope.Requests {
			assertServerValuesForwarded(t, entry.Params)
			assertToolConfigForwarded(t, entry.Params)
		}
	})

	t.Run("no mcp entry sends no mcp beta", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		prep, err := MarshalPreparedBatch([]BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
			{CustomID: "c1", Params: mcpPrompt()},
		})
		if err != nil {
			t.Fatalf("marshal prepared batch: %v", err)
		}
		if _, _, err := newMCPInference(doer).ForwardPreparedBatch(context.Background(), prep); err != nil {
			t.Fatalf("ForwardPreparedBatch: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), batchesPath)
	})
}

// ---- legacy batch submitters ------------------------------------------------------

// TestMCPBetaTransportCreateBatch proves the legacy CreateBatch submitter is corrected
// consistently: the union over entries carries the beta, the per-entry model default is
// still applied, and a batch without MCP still sends no MCP beta.
func TestMCPBetaTransportCreateBatch(t *testing.T) {
	t.Run("mcp only in entry 2", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		second := mcpPrompt()
		second.Model = "" // the entry model default must still be applied
		second.Tools, second.MCPServers = []any{mcpMapToolset()}, mcpTypedServers()
		const fallbackModel = "fixture-default-model"
		inf := NewInference(InferenceConfig{APIKey: mcpTestAPIKey, DefaultModel: fallbackModel, Doer: doer})
		batch, err := inf.CreateBatch(context.Background(), []BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
			{CustomID: "c1", Params: second},
		})
		if err != nil {
			t.Fatalf("CreateBatch: %v", err)
		}
		if batch.ID != "batch_mcp" {
			t.Errorf("batch id = %q, want the decoded upstream id", batch.ID)
		}
		call := doer.only(t)
		assertMCPBetaOnce(t, call, batchesPath)
		assertServerValuesForwarded(t, call.body)
		var envelope struct {
			Requests []BatchRequest `json:"requests"`
		}
		if err := json.Unmarshal(call.body, &envelope); err != nil {
			t.Fatalf("decode transmitted batch: %v", err)
		}
		if len(envelope.Requests) != 2 || envelope.Requests[0].Params.Model != mcpTestModel ||
			envelope.Requests[1].Params.Model != fallbackModel {
			t.Fatalf("per-entry model defaults changed: %s", call.body)
		}
	})

	t.Run("no mcp entry sends no mcp beta", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		if _, err := newMCPInference(doer).CreateBatch(context.Background(), []BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
		}); err != nil {
			t.Fatalf("CreateBatch: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), batchesPath)
	})
}

// TestMCPBetaTransportCreateBatchRaw proves the second legacy submitter — the one the
// inline proxy relays through — is corrected the same way and still returns the raw
// upstream bytes verbatim.
func TestMCPBetaTransportCreateBatchRaw(t *testing.T) {
	t.Run("mcp only in entry 2", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		second := mcpPrompt()
		second.Tools, second.MCPServers = []any{mcpPointerToolset()}, mcpTypedServers()
		_, raw, err := newMCPInference(doer).CreateBatchRaw(context.Background(), []BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
			{CustomID: "c1", Params: second},
		})
		if err != nil {
			t.Fatalf("CreateBatchRaw: %v", err)
		}
		if string(raw) != mcpOKBatch {
			t.Errorf("raw upstream bytes = %s, want them relayed verbatim", raw)
		}
		call := doer.only(t)
		assertMCPBetaOnce(t, call, batchesPath)
		assertServerValuesForwarded(t, call.body)
	})

	t.Run("no mcp entry sends no mcp beta", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		if _, _, err := newMCPInference(doer).CreateBatchRaw(context.Background(), []BatchRequest{
			{CustomID: "c0", Params: mcpPrompt()},
		}); err != nil {
			t.Fatalf("CreateBatchRaw: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), batchesPath)
	})
}

// ---- negative controls ------------------------------------------------------------

// TestMCPBetaTransportNonMCPRequestsKeepHeaderAbsence proves every submitter keeps its
// existing behavior for a request that declares no MCP: no MCP beta, and the unrelated
// beta families each path already sent are preserved.
func TestMCPBetaTransportNonMCPRequestsKeepHeaderAbsence(t *testing.T) {
	plain := mcpPrompt()
	plain.Tools = []any{WebSearchTool()}
	plain.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(50000)}

	t.Run("CreateMessage", func(t *testing.T) {
		doer := &mcpWireDoer{}
		if _, err := newMCPInference(doer).CreateMessage(context.Background(), plain); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		call := doer.only(t)
		assertNoMCPBeta(t, call, messagesPath)
		assertBetaPresent(t, call, BetaTaskBudgets)
	})

	t.Run("StreamMessage", func(t *testing.T) {
		doer := &mcpWireDoer{}
		if _, err := newMCPInference(doer).StreamMessage(context.Background(), plain, func(StreamEvent) error { return nil }); err != nil {
			t.Fatalf("StreamMessage: %v", err)
		}
		call := doer.only(t)
		assertNoMCPBeta(t, call, messagesPath)
		assertBetaPresent(t, call, BetaTaskBudgets)
	})

	t.Run("CountTokens", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKCount}
		if _, err := newMCPInference(doer).CountTokens(context.Background(), plain); err != nil {
			t.Fatalf("CountTokens: %v", err)
		}
		call := doer.only(t)
		assertNoMCPBeta(t, call, countTokensPath)
		assertBetaAbsent(t, call, BetaTaskBudgets)
	})

	t.Run("ForwardPrepared", func(t *testing.T) {
		doer := &mcpWireDoer{}
		norm, err := NormalizeMessageRequest(plain, "")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		prep, err := MarshalPrepared(norm)
		if err != nil {
			t.Fatalf("marshal prepared: %v", err)
		}
		if _, err := newMCPInference(doer).ForwardPrepared(context.Background(), prep); err != nil {
			t.Fatalf("ForwardPrepared: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), messagesPath)
	})

	t.Run("ForwardPreparedBatch", func(t *testing.T) {
		doer := &mcpWireDoer{json: mcpOKBatch}
		prep, err := MarshalPreparedBatch([]BatchRequest{{CustomID: "c0", Params: plain}})
		if err != nil {
			t.Fatalf("marshal prepared batch: %v", err)
		}
		if _, _, err := newMCPInference(doer).ForwardPreparedBatch(context.Background(), prep); err != nil {
			t.Fatalf("ForwardPreparedBatch: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), batchesPath)
	})
}

// TestMCPBetaTransportNilToolsetPointerIsInert proves a nil *MCPToolset entry cannot
// panic the header derivation: a nil entry declares no destination, so it adds no beta,
// while a non-empty mcp_servers[] beside it still does. Validating that entry is the
// enforcement pair's job, not this one's — the scope is deliberately not widened here.
func TestMCPBetaTransportNilToolsetPointerIsInert(t *testing.T) {
	t.Run("nil pointer alone", func(t *testing.T) {
		doer := &mcpWireDoer{}
		req := mcpPrompt()
		req.Tools = []any{(*MCPToolset)(nil)}
		if _, err := newMCPInference(doer).CreateMessage(context.Background(), req); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		assertNoMCPBeta(t, doer.only(t), messagesPath)
	})

	t.Run("nil pointer beside declared servers", func(t *testing.T) {
		doer := &mcpWireDoer{}
		req := mcpPrompt()
		req.Tools, req.MCPServers = []any{(*MCPToolset)(nil)}, mcpTypedServers()
		if _, err := newMCPInference(doer).CreateMessage(context.Background(), req); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		assertMCPBetaOnce(t, doer.only(t), messagesPath)
	})
}

// Every batch submitter carries only the MCP family, including declarations made by
// a toolset alone. The entry's unrelated task-budget beta must stay off batch transport.
func TestMCPBetaTransportBatchFamilyIsExact(t *testing.T) {
	for name, submit := range map[string]func(*Inference, []BatchRequest) error{
		"prepared": func(inf *Inference, entries []BatchRequest) error {
			prep, err := MarshalPreparedBatch(entries)
			if err != nil {
				return err
			}
			// Flip the caller's map after freezing in BOTH directions.
			tool := entries[1].Params.Tools[0].(map[string]any)
			if tool["type"] == "mcp_toolset" {
				tool["type"] = "custom_tool"
			} else {
				tool["type"] = "mcp_toolset"
			}
			_, _, err = inf.ForwardPreparedBatch(context.Background(), prep)
			return err
		},
		"direct": func(inf *Inference, entries []BatchRequest) error {
			_, err := inf.CreateBatch(context.Background(), entries)
			return err
		},
		"raw": func(inf *Inference, entries []BatchRequest) error {
			_, _, err := inf.CreateBatchRaw(context.Background(), entries)
			return err
		},
	} {
		for _, withMCP := range []bool{false, true} {
			suffix := "/without-mcp"
			if withMCP {
				suffix = "/toolset-only"
			}
			t.Run(name+suffix, func(t *testing.T) {
				doer := &mcpWireDoer{json: mcpOKBatch}
				second := mcpPrompt()
				tool := map[string]any{"name": "fixture-local-tool", "input_schema": map[string]any{"type": "object"}}
				if withMCP {
					tool = mcpMapToolset()
				}
				second.Tools = []any{tool}
				second.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(50000)}
				entries := []BatchRequest{{CustomID: "c0", Params: mcpPrompt()}, {CustomID: "c1", Params: second}}
				if err := submit(newMCPInference(doer), entries); err != nil {
					t.Fatal(err)
				}
				call := doer.only(t)
				want := ""
				if withMCP {
					want = MCPBetaHeader
					assertMCPBetaOnce(t, call, batchesPath)
				} else {
					assertNoMCPBeta(t, call, batchesPath)
				}
				if call.beta != want {
					t.Fatalf("batch beta = %q, want exactly %q", call.beta, want)
				}
				if withMCP {
					var envelope struct {
						Requests []struct {
							Params json.RawMessage `json:"params"`
						} `json:"requests"`
					}
					if err := json.Unmarshal(call.body, &envelope); err != nil {
						t.Fatal(err)
					}
					if len(envelope.Requests) != 2 {
						t.Fatalf("entries = %d, want 2", len(envelope.Requests))
					}
					assertToolConfigForwarded(t, envelope.Requests[1].Params)
				}
			})
		}
	}
}

// A tools-only map can remove or introduce an MCP declaration after the freeze.
// Blocking and streaming artifacts must retain the header of their original bytes.
func TestMCPBetaTransportPreparedToolsetAliasesCannotChangeHeaders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, withMCP := range []bool{false, true} {
			name := "blocking"
			if stream {
				name = "stream"
			}
			if withMCP {
				name += "/toolset-only"
			} else {
				name += "/without-mcp"
			}
			t.Run(name, func(t *testing.T) {
				doer := &mcpWireDoer{}
				req := mcpPrompt()
				req.Stream = stream
				req.OutputConfig = &OutputConfig{TaskBudget: TokenTaskBudget(50000)}
				tool := map[string]any{"name": "fixture-local-tool", "input_schema": map[string]any{"type": "object"}}
				if withMCP {
					tool = mcpMapToolset()
				}
				req.Tools = []any{tool}
				norm, err := NormalizeMessageRequest(req, "")
				if err != nil {
					t.Fatal(err)
				}
				prep, err := MarshalPrepared(norm)
				if err != nil {
					t.Fatal(err)
				}
				if withMCP {
					tool["type"] = "custom_tool"
				} else {
					tool["type"] = "mcp_toolset"
				}
				inf := newMCPInference(doer)
				if stream {
					_, err = inf.ForwardPreparedStream(context.Background(), prep, func(StreamEvent) error { return nil })
				} else {
					_, err = inf.ForwardPrepared(context.Background(), prep)
				}
				if err != nil {
					t.Fatal(err)
				}
				call := doer.only(t)
				if stream && call.accept != "text/event-stream" {
					t.Fatalf("stream Accept = %q", call.accept)
				}
				if withMCP {
					assertMCPBetaOnce(t, call, messagesPath)
					assertToolConfigForwarded(t, call.body)
				} else {
					assertNoMCPBeta(t, call, messagesPath)
				}
				assertBetaPresent(t, call, BetaTaskBudgets)
				if !bytes.Equal(call.body, prep.Body()) {
					t.Fatal("caller mutation changed prepared wire bytes")
				}
			})
		}
	}
}

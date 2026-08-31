// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestRequestMetaInject: the three required `_meta` keys are injected, existing
// params fields are preserved, and a non-object params is refused.
func TestRequestMetaInject(t *testing.T) {
	m := nextRequestMeta()
	obj, err := m.inject(listParams{Cursor: "c2"})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if obj["cursor"] != "c2" {
		t.Errorf("existing params fields must be preserved, got %v", obj["cursor"])
	}
	meta, _ := obj["_meta"].(map[string]any)
	if meta == nil {
		t.Fatal("_meta must be injected")
	}
	if meta[metaProtocolVersion] != revision20260728 {
		t.Errorf("_meta protocolVersion = %v, want %s", meta[metaProtocolVersion], revision20260728)
	}
	if _, ok := meta[metaClientInfo].(map[string]any); !ok {
		t.Error("_meta clientInfo must be an object")
	}
	caps, ok := meta[metaClientCapabilities].(map[string]any)
	if !ok {
		t.Fatal("_meta clientCapabilities must be an object")
	}
	// Deny-closed: the introspection client declares NO capabilities, so a
	// conforming server MUST NOT send it any MRTR inputRequests.
	if len(caps) != 0 {
		t.Errorf("introspection client must declare no capabilities, got %v", caps)
	}
	if _, ok := meta["io.modelcontextprotocol/logLevel"]; ok {
		t.Errorf("stateless introspection must not opt into the deprecated logging channel: %v", meta)
	}

	// nil params (server/discover) yields an object with only _meta.
	obj2, err := m.inject(nil)
	if err != nil {
		t.Fatalf("inject nil: %v", err)
	}
	if len(obj2) != 1 {
		t.Errorf("nil params must yield only _meta, got %v", obj2)
	}

	// Non-object params cannot carry _meta.
	if _, err := m.inject([]string{"x"}); err == nil {
		t.Error("a non-object params must be refused")
	}
}

// TestRequestMetaWithExtensions: tasks/* requests declare exactly the tasks
// extension (per-request, SEP-2663 — a server answers -32021 otherwise).
func TestRequestMetaWithExtensions(t *testing.T) {
	m := nextRequestMeta().withExtensions(extensionTasks)
	obj, err := m.inject(nil)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	meta := obj["_meta"].(map[string]any)
	caps := meta[metaClientCapabilities].(map[string]any)
	exts, ok := caps["extensions"].(map[string]any)
	if !ok {
		t.Fatal("clientCapabilities.extensions must be declared")
	}
	if _, ok := exts[extensionTasks]; !ok {
		t.Errorf("the tasks extension id must be declared, got %v", exts)
	}
}

// TestCheckResultEnvelope: complete/absent pass; the Tasks extension's "task"
// passes (open union, interpreted by the caller); input_required is the
// deny-closed MRTR decline.
func TestCheckResultEnvelope(t *testing.T) {
	if rt, err := checkResultEnvelope("tools/list", json.RawMessage(`{"resultType":"complete","tools":[]}`)); err != nil || rt != resultTypeComplete {
		t.Errorf("complete envelope: rt=%q err=%v", rt, err)
	}
	if rt, err := checkResultEnvelope("tools/list", json.RawMessage(`{"tools":[]}`)); err != nil || rt != "" {
		t.Errorf("absent resultType (pre-RC) must pass: rt=%q err=%v", rt, err)
	}
	if rt, err := checkResultEnvelope("tools/call", json.RawMessage(`{"resultType":"task","taskId":"t1","status":"working"}`)); err != nil || rt != resultTypeTask {
		t.Errorf("task envelope must pass through: rt=%q err=%v", rt, err)
	}
	_, err := checkResultEnvelope("tools/call", json.RawMessage(
		`{"resultType":"input_required","inputRequests":{"k1":{"method":"elicitation/create"}},"requestState":"op"}`))
	var ir *errInputRequired
	if !errors.As(err, &ir) {
		t.Fatalf("input_required must yield errInputRequired, got %v", err)
	}
	if len(ir.keys) != 1 {
		t.Errorf("inputRequests keys = %d, want 1", len(ir.keys))
	}
}

// TestClassifyNotFound: the -32002→-32602 remap (SEP-2164) — one chokepoint,
// both revisions.
func TestClassifyNotFound(t *testing.T) {
	legacy := &rpcError{Code: rpcNotFoundLegacy, Message: "resource not found"}
	next := &rpcError{Code: -32602, Message: "Invalid params"}
	if !classifyNotFound(legacy, false) || classifyNotFound(legacy, true) {
		t.Error("-32002 is not-found ONLY in ≤2025-11-25")
	}
	if !classifyNotFound(next, true) || classifyNotFound(next, false) {
		t.Error("-32602 is not-found ONLY in the RC")
	}
	if classifyNotFound(errors.New("plain"), true) {
		t.Error("a non-RPC error is never not-found")
	}
}

// TestUnsupportedVersionDetail: UnsupportedProtocolVersion carries
// data.supported (surfaced in the introspection failure so the operator sees
// what the server DOES speak), with pre-freeze -32004 tolerated during the
// frozen-RC transition.
func TestUnsupportedVersionDetail(t *testing.T) {
	err := &rpcError{Code: rpcUnsupportedProtocolVersion, Message: "Unsupported protocol version",
		Data: json.RawMessage(`{"supported":["2025-11-25"],"requested":"2026-07-28"}`)}
	got := unsupportedVersionDetail(err)
	if len(got) != 1 || got[0] != revision20251125 {
		t.Errorf("supported = %v, want [2025-11-25]", got)
	}
	if unsupportedVersionDetail(&rpcError{Code: -32602}) != nil {
		t.Error("a non-UnsupportedProtocolVersion error has no supported-version detail")
	}
	legacy := &rpcError{Code: rpcUnsupportedProtocolVersionPreFreeze, Message: "Unsupported protocol version",
		Data: json.RawMessage(`{"supported":["2025-11-25"],"requested":"2026-07-28"}`)}
	if got := unsupportedVersionDetail(legacy); len(got) != 1 || got[0] != revision20251125 {
		t.Errorf("pre-freeze supported = %v, want [2025-11-25]", got)
	}
}

// TestMcpNameOf: the RC Mcp-Name table — required value per method, omitted
// everywhere else.
func TestMcpNameOf(t *testing.T) {
	cases := []struct {
		method, name, uri, taskID string
		want                      string
		ok                        bool
	}{
		{"tools/call", "search", "", "", "search", true},
		{"prompts/get", "review", "", "", "review", true},
		{"resources/read", "", "file:///etc/hosts", "", "file:///etc/hosts", true},
		{"tasks/get", "", "", "task-9", "task-9", true},
		{"tools/list", "ignored", "", "", "", false},
		{methodServerDiscover, "", "", "", "", false},
	}
	for _, c := range cases {
		got, ok := mcpNameOf(c.method, c.name, c.uri, c.taskID)
		if got != c.want || ok != c.ok {
			t.Errorf("mcpNameOf(%s) = (%q,%v), want (%q,%v)", c.method, got, ok, c.want, c.ok)
		}
	}
}

// TestRoutingHeadersMirrorBody: the headers are DERIVED from the marshaled body
// (mismatch structurally impossible client-side).
func TestRoutingHeadersMirrorBody(t *testing.T) {
	m := nextRequestMeta()
	params, err := m.inject(map[string]any{"name": "search", "arguments": map[string]any{}})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	body, err := rpcRequest{ID: 7, Method: "tools/call", Params: params}.marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hdr := routingHeaders("tools/call", body)
	if hdr[headerMcpMethod] != "tools/call" {
		t.Errorf("Mcp-Method = %q", hdr[headerMcpMethod])
	}
	if hdr[headerMcpName] != "search" {
		t.Errorf("Mcp-Name = %q, want search", hdr[headerMcpName])
	}
	if hdr[headerMCPProtocolVersion] != revision20260728 {
		t.Errorf("MCP-Protocol-Version = %q, want the _meta value byte-for-byte", hdr[headerMCPProtocolVersion])
	}

	// A list method carries NO Mcp-Name.
	lp, _ := m.inject(listParams{})
	lbody, _ := rpcRequest{ID: 8, Method: "tools/list", Params: lp}.marshal()
	if _, has := routingHeaders("tools/list", lbody)[headerMcpName]; has {
		t.Error("Mcp-Name must be omitted for tools/list")
	}
}

// TestDiscoverResultSupports + the schema-dialect hygiene signal.
func TestDiscoverSupportsAndSchemaDialect(t *testing.T) {
	d := discoverResult{SupportedVersions: []string{revision20251125, revision20260728}}
	if !d.supports(revision20260728) || d.supports("1900-01-01") {
		t.Error("supports() must match exact version strings")
	}
	tools := []Tool{
		{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},                                                          // absent $schema = default 2020-12
		{Name: "b", InputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`)}, // explicit default
		{Name: "c", OutputSchema: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#"}`)},                     // non-default
	}
	if n := countToolsWithNonDefaultDialect(tools); n != 1 {
		t.Errorf("non-default dialect tools = %d, want 1", n)
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// mcp_egress_test.go is the connector half of the C8 E2-3 oracle (accepted MCP egress
// contract §10 with Root corrections m1-m14): the one wire parser, exact-origin
// normalization, coverage, the accepted binding and its verification by every serializer
// that can put MCP on the wire (MarshalPrepared, CountTokens, MarshalPreparedBatch), and
// the shell's refusal of the legacy fallback for a bound request.
//
// Expected values come from the contract, not from the implementation: origins are the
// contract's worked examples, and the two digests below were computed by an independent
// Python encoder from the specified framing (evidence kat.py / kat.out).
package claudeapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// ---- fixtures ------------------------------------------------------------------------

const (
	mcpTestOrigin     = "https://mcp.example.com:8443"
	mcpTestTenant     = "acme"
	mcpTestActorRef   = "agent:nhi:bound"
	mcpToolsetRawJSON = `{"type":"mcp_toolset","mcp_server_name":"acme-tools","default_config":{"enabled":false},"configs":{"read_file":{"enabled":true}}}`
	mcpServerRawJSON  = `{"type":"url","name":"acme-tools","url":"https://mcp.example.com:8443/sse?tenant=acme","authorization_token":"tok-mcp-do-not-rewrite"}`
)

func mcpMarkerView() map[string]any { return map[string]any{"type": mcpToolsetType} }

func mcpDecl(tools, servers []any) MessageRequest {
	r := mcpPrompt()
	r.Tools = tools
	r.MCPServers = servers
	return r
}

func mcpMapDecl() MessageRequest { return mcpDecl([]any{mcpMapToolset()}, mcpMapServers()) }

func webSearchTool() map[string]any {
	return map[string]any{"type": "web_search_20260209", "name": "web_search"}
}

// exactAck is the Community-visible half of an allowing gate: Forward with the exact copied
// acknowledgment when MCP is declared.
func exactAck(in ServerToolEgressInput) ServerToolEgressDecision {
	dec := ServerToolEgressDecision{Forward: true}
	if in.MCP != nil {
		ack := in.MCP.Coverage
		dec.MCPAck = &ack
	}
	return dec
}

func mcpCapture(t *testing.T, req MessageRequest) (*MCPEgressSnapshot, ServerToolEgressInput) {
	t.Helper()
	_, snap, err := SnapshotMCPEgress(req)
	if err != nil {
		t.Fatalf("SnapshotMCPEgress: %v", err)
	}
	in, err := snap.GateInput(mcpTestTenant, mcpTestActorRef, false)
	if err != nil {
		t.Fatalf("GateInput: %v", err)
	}
	return snap, in
}

// mcpGovern captures req and accepts an exact acknowledgment with no rewrite.
func mcpGovern(t *testing.T, req MessageRequest) (MessageRequest, *MCPEgressSnapshot, ServerToolEgressInput) {
	t.Helper()
	snap, in := mcpCapture(t, req)
	gov, err := snap.ApplyDecision(exactAck(in))
	if err != nil {
		t.Fatalf("ApplyDecision: %v", err)
	}
	return gov, snap, in
}

func requireMCPCode(t *testing.T, err error, want MCPDenialCode) {
	t.Helper()
	var me *MCPEgressError
	if !errors.As(err, &me) || me.Code != want {
		t.Fatalf("error = %v, want MCP refusal %s", err, want)
	}
}

// sentMCP decodes the tools[] and mcp_servers[] a serialized body carries.
func sentMCP(t *testing.T, body []byte) (tools, servers []any) {
	t.Helper()
	var sent struct {
		Tools      []any `json:"tools"`
		MCPServers []any `json:"mcp_servers"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode serialized body: %v", err)
	}
	return sent.Tools, sent.MCPServers
}

func mustJSONValue(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("fixture %s: %v", raw, err)
	}
	return v
}

func frozenBody(t *testing.T, gov MessageRequest) []byte {
	t.Helper()
	p, err := MarshalPrepared(gov)
	if err != nil {
		t.Fatalf("MarshalPrepared: %v", err)
	}
	return p.Body()
}

// mcpDisguised looks like an unrelated Go value but serializes as an MCP toolset. It
// counts its own invocations, so a test can prove the captured graph is forwarded without
// calling the caller's marshaler again.
type mcpDisguised struct{ calls *int }

func (d mcpDisguised) MarshalJSON() ([]byte, error) {
	*d.calls++
	return []byte(mcpToolsetRawJSON), nil
}

type mcpFailingMarshaler struct{}

func (mcpFailingMarshaler) MarshalJSON() ([]byte, error) {
	return nil, errors.New("marshal failure carrying tok-mcp-do-not-rewrite")
}

type mcpFailingReader struct{}

func (mcpFailingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// ---- origin normalization ---------------------------------------------------------------

// TestMCPOriginNormalization pins the contract §5 origin examples and the m11 numeric-host
// rule. A suffix/host-only or a permissive URL-repairing mutant fails the rejection table.
func TestMCPOriginNormalization(t *testing.T) {
	accepted := []struct{ in, want string }{
		{"https://example.com", "https://example.com:443"},
		{"HTTPS://Example.COM/path?q=x", "https://example.com:443"},
		{"https://example.com:0443/", "https://example.com:443"},
		{"https://example.com:8443/mcp", "https://example.com:8443"},
		{"https://example.com/%41?x=%42", "https://example.com:443"},
		{"https://[2001:0db8::1]/mcp", "https://[2001:db8::1]:443"},
		{"https://[2001:DB8:0:0:0:0:0:1]:8443", "https://[2001:db8::1]:8443"},
		{"https://1.2.3.4/", "https://1.2.3.4:443"},
		{"https://xn--bcher-kva.example/", "https://xn--bcher-kva.example:443"},
		{"https://a-b.example.com:65535", "https://a-b.example.com:65535"},
	}
	for _, c := range accepted {
		got, err := MCPOriginFromURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("MCPOriginFromURL(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if a, _ := MCPOriginFromURL("https://example.com:8443"); a == "https://example.com:443" {
		t.Error("port 8443 must stay distinct from 443")
	}
	rejected := []string{
		"http://example.com/", "ftp://example.com/", "https:example.com", "https:/example.com",
		"//example.com/", "example.com", "https://", "https://:443/", "https://example.com:/",
		"https://example.com:0", "https://example.com:65536", "https://example.com:+443",
		"https://example.com:44 3", "https://user@example.com/", "https://user:pw@example.com/",
		"https://example.com/#frag", "https://example.com#", "https://example.com./",
		"https://exa mple.com/", "https://example.com/\\path", "https://example.com\t/",
		"https://ex%41mple.com/", "https://example.com/%zz", "https://example.com/%4",
		"https://bücher.example/", "https://xn--a.example/", "https://under_score.example/",
		"https://-lead.example/", "https://trail-.example/", "https://a..b.example/",
		"https://" + strings.Repeat("a", 64) + ".example/",
		"https://" + strings.Repeat("a.", 127) + "example/",
		"https://0x7f000001/", "https://2130706433/", "https://example.0x1/", "https://example.0x/",
		"https://01.2.3.4/", "https://1.2.3/", "https://1.2.3.4.5/", "https://127.1/",
		"https://[fe80::1%25eth0]/", "https://[::ffff:1.2.3.4]/", "https://[v1.fe]/",
		"https://[2001:db8::1/", "https://2001:db8::1/", "https://[1.2.3.4]/", "https://[]/",
	}
	for _, in := range rejected {
		got, err := MCPOriginFromURL(in)
		if err == nil {
			t.Errorf("MCPOriginFromURL(%q) = %q, want rejection", in, got)
			continue
		}
		if !errors.Is(err, ErrInvalidMCPOrigin) {
			t.Errorf("MCPOriginFromURL(%q) error %v is not ErrInvalidMCPOrigin", in, err)
		}
		if len(in) > len("https://") && strings.Contains(err.Error(), strings.TrimPrefix(in, "https://")) {
			t.Errorf("MCPOriginFromURL(%q) error embeds the rejected value: %v", in, err)
		}
	}
}

// TestMCPOriginNormalizationGrantForm pins the configuration form: the same parser, with no
// path beyond "/", no query delimiter and no fragment, and no trailing slash in the result.
func TestMCPOriginNormalizationGrantForm(t *testing.T) {
	accepted := []struct{ in, want string }{
		{"https://example.com", "https://example.com:443"},
		{"https://example.com/", "https://example.com:443"},
		{"HTTPS://EXAMPLE.com:8443", "https://example.com:8443"},
		{"https://[2001:db8::1]:443/", "https://[2001:db8::1]:443"},
		{"https://example.com:443", "https://example.com:443"},
	}
	for _, c := range accepted {
		got, err := NormalizeMCPOrigin(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeMCPOrigin(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{
		"https://example.com/mcp", "https://example.com?", "https://example.com/?q=1", "https://example.com/#",
		"https://example.com//", "http://example.com", "https://example.com./", "https://*.example.com",
		"https://example.com:8443/secret-path", "",
	} {
		if got, err := NormalizeMCPOrigin(in); err == nil || !errors.Is(err, ErrInvalidMCPOrigin) {
			t.Errorf("NormalizeMCPOrigin(%q) = %q, %v; want ErrInvalidMCPOrigin", in, got, err)
		}
	}
}

// ---- the wire snapshot --------------------------------------------------------------------

// TestMCPWireSnapshotFormsAgree proves typed, pointer, map, RawMessage and HTTP-decoded
// declarations reach one identical gate view and one identical forwarded projection. A
// map-only classifier fails the typed and raw rows.
func TestMCPWireSnapshotFormsAgree(t *testing.T) {
	raw := json.RawMessage(mcpToolsetRawJSON)
	rawServer := json.RawMessage(mcpServerRawJSON)
	var decoded MessageRequest
	if err := json.Unmarshal([]byte(`{"model":"claude-opus-4-8","max_tokens":64,"messages":[],"tools":[`+
		mcpToolsetRawJSON+`],"mcp_servers":[`+mcpServerRawJSON+`]}`), &decoded); err != nil {
		t.Fatalf("decode HTTP-shaped request: %v", err)
	}
	server := MCPServer{Type: "url", Name: mcpTestServerName, URL: mcpTestServerURL, AuthorizationToken: mcpTestServerToken}
	forms := []struct {
		name           string
		tools, servers []any
	}{
		{"typed", []any{mcpTypedToolset()}, []any{server}},
		{"pointer", []any{mcpPointerToolset()}, []any{&server}},
		{"map", []any{mcpMapToolset()}, mcpMapServers()},
		{"raw", []any{raw}, []any{rawServer}},
		{"raw pointer", []any{&raw}, []any{&rawServer}},
		{"http decoded", decoded.Tools, decoded.MCPServers},
	}
	wantTools := []any{mustJSONValue(t, mcpToolsetRawJSON)}
	wantServers := []any{mustJSONValue(t, mcpServerRawJSON)}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			gov, _, in := mcpGovern(t, mcpDecl(f.tools, f.servers))
			if !reflect.DeepEqual(in.Tools, []any{mcpMarkerView()}) {
				t.Errorf("gate Tools = %#v, want the single marker", in.Tools)
			}
			if in.MCP == nil || !reflect.DeepEqual(in.MCP.Destinations, []MCPDestination{{ServerIndex: 0, ToolIndex: 0, Origin: mcpTestOrigin}}) {
				t.Fatalf("gate destinations = %#v", in.MCP)
			}
			tools, servers := sentMCP(t, frozenBody(t, gov))
			if !reflect.DeepEqual(tools, wantTools) || !reflect.DeepEqual(servers, wantServers) {
				t.Errorf("forwarded MCP = %#v / %#v, want %#v / %#v", tools, servers, wantTools, wantServers)
			}
		})
	}
}

// TestMCPWireSnapshotPreservesPresence proves absent members stay absent, explicit false
// and null stay as declared, empty objects stay empty and no provider default is inserted.
// A default-inserting mutant fails the first row.
func TestMCPWireSnapshotPreservesPresence(t *testing.T) {
	server := `{"type":"url","name":"acme-tools","url":"https://mcp.example.com:8443/sse?tenant=acme"}`
	rows := []struct{ name, toolset, server string }{
		{"default_config omitted", `{"type":"mcp_toolset","mcp_server_name":"acme-tools"}`, server},
		{"explicit false kept", `{"type":"mcp_toolset","mcp_server_name":"acme-tools","default_config":{"enabled":false,"defer_loading":false}}`, server},
		{"empty objects kept", `{"type":"mcp_toolset","mcp_server_name":"acme-tools","default_config":{},"configs":{}}`, server},
		{"null configs kept", `{"type":"mcp_toolset","mcp_server_name":"acme-tools","configs":null,"cache_control":null}`, server},
		{"cache control kept", `{"type":"mcp_toolset","mcp_server_name":"acme-tools","cache_control":{"type":"ephemeral","ttl":"1h"}}`, server},
		{"null token kept", `{"type":"mcp_toolset","mcp_server_name":"acme-tools"}`,
			`{"type":"url","name":"acme-tools","url":"https://mcp.example.com:8443/sse?tenant=acme","authorization_token":null}`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			toolset := mustJSONValue(t, r.toolset)
			srv := mustJSONValue(t, r.server)
			gov, _, _ := mcpGovern(t, mcpDecl([]any{toolset}, []any{srv}))
			tools, servers := sentMCP(t, frozenBody(t, gov))
			if !reflect.DeepEqual(tools, []any{mustJSONValue(t, r.toolset)}) {
				t.Errorf("forwarded toolset = %#v, want %s", tools, r.toolset)
			}
			if !reflect.DeepEqual(servers, []any{mustJSONValue(t, r.server)}) {
				t.Errorf("forwarded server = %#v, want %s", servers, r.server)
			}
		})
	}
	// The typed form serializes its non-omitempty default: that is what it declares.
	gov, _, _ := mcpGovern(t, mcpDecl([]any{MCPToolset{Type: "mcp_toolset", MCPServerName: mcpTestServerName}}, mcpMapServers()))
	tools, _ := sentMCP(t, frozenBody(t, gov))
	want := mustJSONValue(t, `{"type":"mcp_toolset","mcp_server_name":"acme-tools","default_config":{"enabled":false}}`)
	if !reflect.DeepEqual(tools, []any{want}) {
		t.Errorf("typed toolset forwarded %#v, want %#v", tools, want)
	}
}

// TestMCPWireSnapshotRejectsInvalidDeclarations pins every malformed, ambiguous or
// unsupported declaration as mcp_invalid_declaration, with no parser text, URL, name or
// token in the error.
func TestMCPWireSnapshotRejectsInvalidDeclarations(t *testing.T) {
	cycle := map[string]any{"type": "custom", "name": "cyclic"}
	cycle["self"] = cycle
	tooMany := make([]any, 0, MCPMaxDestinations+1)
	manyTools := make([]any, 0, MCPMaxDestinations+1)
	for i := 0; i <= MCPMaxDestinations; i++ {
		name := "srv-" + string(rune('a'+i))
		tooMany = append(tooMany, map[string]any{"type": "url", "name": name, "url": "https://mcp.example.com/" + name})
		manyTools = append(manyTools, map[string]any{"type": "mcp_toolset", "mcp_server_name": name})
	}
	withServer := func(mut func(map[string]any)) []any {
		s := mcpMapServers()[0].(map[string]any)
		mut(s)
		return []any{s}
	}
	withToolset := func(mut func(map[string]any)) []any {
		ts := mcpMapToolset()
		mut(ts)
		return []any{ts}
	}
	raw := func(s string) []any { return []any{json.RawMessage(s)} }
	cases := []struct {
		name           string
		tools, servers []any
	}{
		{"nil typed toolset", []any{(*MCPToolset)(nil)}, mcpMapServers()},
		{"nil typed server", []any{mcpMapToolset()}, []any{(*MCPServer)(nil)}},
		{"server not an object", []any{mcpMapToolset()}, []any{mcpTestServerURL}},
		{"byte slice is not raw JSON", []any{mcpMapToolset()}, []any{[]byte(mcpServerRawJSON)}},
		{"server type not url", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["type"] = "sse" })},
		{"server missing name", []any{mcpMapToolset()}, withServer(func(s map[string]any) { delete(s, "name") })},
		{"server empty name", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["name"] = "" })},
		{"server url not a string", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["url"] = 42 })},
		{"server token not a string", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["authorization_token"] = true })},
		{"legacy tool_configuration", []any{mcpMapToolset()}, withServer(func(s map[string]any) {
			s["tool_configuration"] = map[string]any{"enabled": true}
		})},
		{"unknown server member", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["headers"] = map[string]any{} })},
		{"server url not https", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["url"] = "http://mcp.example.com/sse" })},
		{"server url with userinfo", []any{mcpMapToolset()}, withServer(func(s map[string]any) {
			s["url"] = "https://tok-mcp-do-not-rewrite@mcp.example.com/sse"
		})},
		{"server url unicode host", []any{mcpMapToolset()}, withServer(func(s map[string]any) { s["url"] = "https://bücher.example/sse" })},
		{"unknown toolset member", withToolset(func(ts map[string]any) { ts["allowed_tools"] = []any{"x"} }), mcpMapServers()},
		{"typed toolset without type", []any{MCPToolset{MCPServerName: mcpTestServerName}}, mcpMapServers()},
		{"unknown mcp type with schema", []any{map[string]any{
			"type": "mcp_connector", "name": "x", "input_schema": map[string]any{"type": "object"},
		}}, mcpMapServers()},
		{"root mcp_server_name on a custom tool", []any{map[string]any{
			"name": "x", "input_schema": map[string]any{"type": "object"}, "mcp_server_name": mcpTestServerName,
		}}, mcpMapServers()},
		{"null default_config", withToolset(func(ts map[string]any) { ts["default_config"] = nil }), mcpMapServers()},
		{"null enabled", withToolset(func(ts map[string]any) { ts["default_config"] = map[string]any{"enabled": nil} }), mcpMapServers()},
		{"string enabled", withToolset(func(ts map[string]any) {
			ts["configs"] = map[string]any{mcpTestToolName: map[string]any{"enabled": "yes"}}
		}), mcpMapServers()},
		{"unknown config member", withToolset(func(ts map[string]any) {
			ts["configs"] = map[string]any{mcpTestToolName: map[string]any{"enabled": true, "allowed": true}}
		}), mcpMapServers()},
		{"empty tool name", withToolset(func(ts map[string]any) {
			ts["configs"] = map[string]any{"": map[string]any{"enabled": true}}
		}), mcpMapServers()},
		{"config not an object", withToolset(func(ts map[string]any) { ts["configs"] = map[string]any{mcpTestToolName: true} }), mcpMapServers()},
		{"configs not an object", withToolset(func(ts map[string]any) { ts["configs"] = []any{} }), mcpMapServers()},
		{"cache_control type", withToolset(func(ts map[string]any) { ts["cache_control"] = map[string]any{"type": "persistent"} }), mcpMapServers()},
		{"cache_control ttl", withToolset(func(ts map[string]any) {
			ts["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "10m"}
		}), mcpMapServers()},
		{"cache_control member", withToolset(func(ts map[string]any) {
			ts["cache_control"] = map[string]any{"type": "ephemeral", "scope": "x"}
		}), mcpMapServers()},
		{"dangling toolset", withToolset(func(ts map[string]any) { ts["mcp_server_name"] = "other" }), mcpMapServers()},
		{"server without toolset", nil, mcpMapServers()},
		{"toolset without server", []any{mcpMapToolset()}, nil},
		{"two toolsets for one server", []any{mcpMapToolset(), mcpMapToolset()}, mcpMapServers()},
		{"duplicate server names", []any{mcpMapToolset()}, append(mcpMapServers(), mcpMapServers()...)},
		{"more than the destination limit", manyTools, tooMany},
		{"duplicate member", raw(`{"type":"mcp_toolset","mcp_server_name":"acme-tools","mcp_server_name":"acme-tools"}`), mcpMapServers()},
		{"ambiguous duplicate type", raw(`{"type":"web_search_20260209","type":"mcp_toolset","mcp_server_name":"acme-tools"}`), mcpMapServers()},
		{"duplicate server member", []any{mcpMapToolset()}, raw(`{"type":"url","name":"acme-tools","url":"https://mcp.example.com/a","url":"https://mcp.example.com/b"}`)},
		{"invalid raw JSON", raw(`{"type":`), mcpMapServers()},
		{"marshal failure", []any{mcpFailingMarshaler{}, mcpMapToolset()}, mcpMapServers()},
		{"cycle", []any{cycle, mcpMapToolset()}, mcpMapServers()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, snap, err := SnapshotMCPEgress(mcpDecl(c.tools, c.servers))
			if snap != nil {
				t.Error("a refused capture must not return a usable snapshot")
			}
			requireMCPCode(t, err, MCPDenyInvalidDeclaration)
			for _, secret := range []string{mcpTestServerName, "mcp.example.com", mcpTestServerToken, "bücher"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("refusal text carries %q: %v", secret, err)
				}
			}
		})
	}
}

// TestMCPWireSnapshotClassifiesSerializedValue proves classification reads the serialized
// value, not the Go type, and that the caller's marshaler runs exactly once: the captured
// graph is what is forwarded. A nested mcp_server_name inside a custom tool's schema does
// not claim MCP.
func TestMCPWireSnapshotClassifiesSerializedValue(t *testing.T) {
	calls := 0
	req := mcpDecl([]any{webSearchTool(), mcpDisguised{calls: &calls}}, mcpMapServers())
	gov, _, in := mcpGovern(t, req)
	if !reflect.DeepEqual(in.Tools, []any{webSearchTool(), mcpMarkerView()}) {
		t.Errorf("gate Tools = %#v, want the web tool then the marker", in.Tools)
	}
	if in.MCP == nil || len(in.MCP.Destinations) != 1 || in.MCP.Destinations[0].ToolIndex != 1 {
		t.Fatalf("disguised toolset not described: %#v", in.MCP)
	}
	tools, _ := sentMCP(t, frozenBody(t, gov))
	if len(tools) != 2 || !reflect.DeepEqual(tools[1], mustJSONValue(t, mcpToolsetRawJSON)) {
		t.Errorf("forwarded tools = %#v", tools)
	}
	if calls != 1 {
		t.Errorf("caller marshaler ran %d times, want exactly 1 (capture only)", calls)
	}

	custom := map[string]any{"name": "lookup", "input_schema": map[string]any{
		"type": "object", "properties": map[string]any{"mcp_server_name": map[string]any{"type": "string"}},
	}}
	_, snap, err := SnapshotMCPEgress(mcpDecl([]any{custom}, nil))
	if err != nil {
		t.Fatalf("nested schema key must not claim MCP: %v", err)
	}
	in2, err := snap.GateInput(mcpTestTenant, "", true)
	if err != nil || in2.MCP != nil || !reflect.DeepEqual(in2.Tools, []any{custom}) {
		t.Errorf("custom tool gate view = %#v, MCP=%#v, err=%v", in2.Tools, in2.MCP, err)
	}
}

// TestMCPWireSnapshotDetached proves no alias can change what was captured: the caller's
// maps and raw buffers after capture, the gate's own input, and the governed request's
// view handed to later gates.
func TestMCPWireSnapshotDetached(t *testing.T) {
	toolset := mcpMapToolset()
	server := mcpMapServers()[0].(map[string]any)
	rawBuf := []byte(mcpToolsetRawJSON)
	req := mcpDecl([]any{toolset}, []any{server})
	_, snap, err := SnapshotMCPEgress(req)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	toolset["mcp_server_name"] = "evil"
	toolset["configs"].(map[string]any)[mcpTestToolName].(map[string]any)["enabled"] = false
	server["url"] = "https://evil.example/steal"
	server["authorization_token"] = "stolen"
	req.Tools[0] = map[string]any{"type": "mcp_toolset", "mcp_server_name": "evil"}

	in, err := snap.GateInput(mcpTestTenant, mcpTestActorRef, false)
	if err != nil {
		t.Fatalf("gate input: %v", err)
	}
	ack := in.MCP.Coverage
	in.Tools[0].(map[string]any)["mcp_server_name"] = "evil"
	in.MCP.Destinations[0].Origin = "https://evil.example:443"
	gov, err := snap.ApplyDecision(ServerToolEgressDecision{Forward: true, MCPAck: &ack})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	view := MCPGateTools(gov.Tools)
	view[0].(map[string]any)["mcp_server_name"] = "evil"
	body := frozenBody(t, gov)
	for _, bad := range []string{"evil", "stolen"} {
		if bytes.Contains(body, []byte(bad)) {
			t.Fatalf("an alias changed the forwarded MCP (%q): %s", bad, body)
		}
	}
	assertServerValuesForwarded(t, body)
	assertToolConfigForwarded(t, body)

	// A RawMessage buffer the caller rewrites in place after capture changes nothing.
	gov2, _, _ := mcpGovern(t, mcpDecl([]any{json.RawMessage(rawBuf)}, mcpMapServers()))
	copy(rawBuf, bytes.Repeat([]byte(" "), len(rawBuf)))
	assertToolConfigForwarded(t, frozenBody(t, gov2))
}

// ---- coverage ----------------------------------------------------------------------------

func seqNonce() [32]byte {
	var n [32]byte
	for i := range n {
		n[i] = byte(i)
	}
	return n
}

// TestMCPCoverageDigestKnownAnswer pins the specified framing with independently computed
// digests (evidence kat.py), then shows every bound field changes the digest.
func TestMCPCoverageDigestKnownAnswer(t *testing.T) {
	in := ServerToolEgressInput{Tenant: "acme", ActorRef: "agent:nhi:bound", MCP: &MCPEgressRequest{
		Coverage: MCPEgressCoverage{Version: 1, Nonce: seqNonce()},
		Destinations: []MCPDestination{
			{ServerIndex: 0, ToolIndex: 1, Origin: "https://mcp.example.com:8443"},
			{ServerIndex: 1, ToolIndex: 0, Origin: "https://[2001:db8::1]:443"},
		},
	}}
	got, err := MCPCoverageDigest(in)
	if err != nil || hex.EncodeToString(got[:]) != "b877c5c6dd6de3b70399e8b474351a9b141447b494b96ab8841918601c120adf" {
		t.Fatalf("digest = %x, %v; want the independent known answer", got, err)
	}
	in.MCP.Coverage.Digest = [32]byte{1}
	if again, _ := MCPCoverageDigest(in); again != got {
		t.Error("the Digest field itself must not be an input")
	}
	unbindable := ServerToolEgressInput{Tenant: "acme", UnbindableAgent: true, MCP: &MCPEgressRequest{
		Coverage:     MCPEgressCoverage{Version: 1, Nonce: seqNonce()},
		Destinations: []MCPDestination{{ServerIndex: 0, ToolIndex: 0, Origin: "https://mcp.example.com:8443"}},
	}}
	if u, err := MCPCoverageDigest(unbindable); err != nil ||
		hex.EncodeToString(u[:]) != "38fdb20691258613073a172b260a71c3b12c7c5f6fe2c2263efcc251f787c3d1" {
		t.Fatalf("unbindable digest = %x, %v; want the independent known answer", u, err)
	}
	mutations := map[string]func(*ServerToolEgressInput){
		"tenant":       func(x *ServerToolEgressInput) { x.Tenant = "other" },
		"actor":        func(x *ServerToolEgressInput) { x.ActorRef = "agent:other" },
		"unbindable":   func(x *ServerToolEgressInput) { x.UnbindableAgent = true },
		"version":      func(x *ServerToolEgressInput) { x.MCP.Coverage.Version = 2 },
		"nonce":        func(x *ServerToolEgressInput) { x.MCP.Coverage.Nonce[31] ^= 1 },
		"origin":       func(x *ServerToolEgressInput) { x.MCP.Destinations[0].Origin = "https://mcp.example.com:443" },
		"tool index":   func(x *ServerToolEgressInput) { x.MCP.Destinations[0].ToolIndex = 2 },
		"server index": func(x *ServerToolEgressInput) { x.MCP.Destinations[1].ServerIndex = 2 },
		"order": func(x *ServerToolEgressInput) {
			x.MCP.Destinations[0], x.MCP.Destinations[1] = x.MCP.Destinations[1], x.MCP.Destinations[0]
		},
		"count": func(x *ServerToolEgressInput) { x.MCP.Destinations = x.MCP.Destinations[:1] },
	}
	for name, mut := range mutations {
		cp := in
		mcp := *in.MCP
		mcp.Destinations = append([]MCPDestination(nil), in.MCP.Destinations...)
		cp.MCP = &mcp
		mut(&cp)
		if d, err := MCPCoverageDigest(cp); err != nil || d == got {
			t.Errorf("%s change did not change the digest (err=%v)", name, err)
		}
	}
	if _, err := MCPCoverageDigest(ServerToolEgressInput{Tenant: "acme"}); err == nil {
		t.Error("a nil MCP request must not have a digest")
	}
}

// TestMCPCoverageFreshNonceAndEntropyFailure proves each admission draws a fresh nonce,
// the issued digest verifies, and an injected read error or short read refuses with
// mcp_coverage_unavailable instead of a weak nonce (m5).
func TestMCPCoverageFreshNonceAndEntropyFailure(t *testing.T) {
	_, a := mcpCapture(t, mcpMapDecl())
	_, b := mcpCapture(t, mcpMapDecl())
	if a.MCP.Coverage.Nonce == b.MCP.Coverage.Nonce || a.MCP.Coverage.Nonce == ([32]byte{}) {
		t.Fatal("two admissions of identical requests must draw distinct non-zero nonces")
	}
	if a.MCP.Coverage.Version != MCPEgressVersion {
		t.Errorf("coverage version = %d, want %d", a.MCP.Coverage.Version, MCPEgressVersion)
	}
	if d, err := MCPCoverageDigest(a); err != nil || d != a.MCP.Coverage.Digest {
		t.Errorf("issued digest does not verify: %v", err)
	}
	for name, r := range map[string]io.Reader{
		"read error": mcpFailingReader{},
		"short read": io.LimitReader(bytes.NewReader(make([]byte, 64)), 5),
	} {
		_, snap, err := snapshotMCPEgress(mcpMapDecl(), r)
		if err != nil {
			t.Fatalf("%s: capture itself must not read entropy: %v", name, err)
		}
		_, err = snap.GateInput(mcpTestTenant, mcpTestActorRef, false)
		requireMCPCode(t, err, MCPDenyCoverageUnavailable)
	}
	_, snap, err := snapshotMCPEgress(mcpPrompt(), mcpFailingReader{})
	if err != nil {
		t.Fatalf("absence capture: %v", err)
	}
	if in, err := snap.GateInput(mcpTestTenant, "", true); err != nil || in.MCP != nil {
		t.Errorf("a request without MCP needs no nonce: in.MCP=%#v err=%v", in.MCP, err)
	}
}

// ---- binding ------------------------------------------------------------------------------

// TestMCPBindingApplyDecisionCoverage proves only the exact acknowledgment of THIS admission
// is accepted, once. An optional/ignored coverage mutant fails every refusal row.
func TestMCPBindingApplyDecisionCoverage(t *testing.T) {
	_, other := mcpCapture(t, mcpMapDecl())
	otherAck := other.MCP.Coverage
	tamper := map[string]func(*ServerToolEgressDecision){
		"forward only":           func(d *ServerToolEgressDecision) { d.MCPAck = nil },
		"wrong version":          func(d *ServerToolEgressDecision) { d.MCPAck.Version++ },
		"wrong nonce":            func(d *ServerToolEgressDecision) { d.MCPAck.Nonce[0] ^= 0xff },
		"wrong digest":           func(d *ServerToolEgressDecision) { d.MCPAck.Digest[0] ^= 0xff },
		"zero acknowledgment":    func(d *ServerToolEgressDecision) { d.MCPAck = &MCPEgressCoverage{} },
		"another admission":      func(d *ServerToolEgressDecision) { d.MCPAck = &otherAck },
		"forward with a denial":  func(d *ServerToolEgressDecision) { d.MCPDeny = MCPDenyOriginNotGranted },
		"not a forward decision": func(d *ServerToolEgressDecision) { d.Forward = false },
	}
	for name, mut := range tamper {
		t.Run(name, func(t *testing.T) {
			snap, in := mcpCapture(t, mcpMapDecl())
			dec := exactAck(in)
			mut(&dec)
			_, err := snap.ApplyDecision(dec)
			requireMCPCode(t, err, MCPDenyCoverageUnavailable)
		})
	}
	// Absence is bound too: an acknowledgment for a request without MCP is inconsistent.
	snap, in := mcpCapture(t, mcpPrompt())
	dec := exactAck(in)
	dec.MCPAck = &otherAck
	_, err := snap.ApplyDecision(dec)
	requireMCPCode(t, err, MCPDenyCoverageUnavailable)

	// Single use: a second decision and a decision before the gate input both refuse.
	snap2, in2 := mcpCapture(t, mcpMapDecl())
	if _, err := snap2.ApplyDecision(exactAck(in2)); err != nil {
		t.Fatalf("exact acknowledgment: %v", err)
	}
	_, err = snap2.ApplyDecision(exactAck(in2))
	requireMCPCode(t, err, MCPDenyBindingChanged)
	_, early, err := SnapshotMCPEgress(mcpMapDecl())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_, err = early.ApplyDecision(ServerToolEgressDecision{Forward: true})
	requireMCPCode(t, err, MCPDenyCoverageUnavailable)
	if _, err := snap2.GateInput(mcpTestTenant, mcpTestActorRef, false); err == nil {
		t.Error("a second gate input must be refused (single-use coverage)")
	}
}

// TestMCPBindingApplyDecisionRewrites proves a gate may rewrite only non-MCP slots of an
// MCP-bearing request, must return the marker at every MCP index, and may never introduce
// MCP. A blanket rewrite-ban mutant fails the positive rows.
func TestMCPBindingApplyDecisionRewrites(t *testing.T) {
	clamped := map[string]any{"type": "web_search_20260209", "name": "web_search", "allowed_domains": []any{"example.com"}, "max_uses": 5}
	mixed := func() MessageRequest { return mcpDecl([]any{webSearchTool(), mcpMapToolset()}, mcpMapServers()) }
	refused := map[string][]any{
		"MCP slot rewritten":        {clamped, map[string]any{"type": "mcp_toolset", "mcp_server_name": "other"}},
		"MCP slot restored by gate": {clamped, mcpMapToolset()},
		"marker with extra member":  {clamped, map[string]any{"type": "mcp_toolset", "x": true}},
		"reordered":                 {mcpMarkerView(), clamped},
		"count changed":             {clamped, mcpMarkerView(), webSearchTool()},
		"MCP claimed by other slot": {map[string]any{"type": "mcp_toolset"}, mcpMarkerView()},
		"typed toolset injected":    {MCPToolset{Type: "mcp_toolset", MCPServerName: "x"}, mcpMarkerView()},
	}
	for name, gt := range refused {
		t.Run(name, func(t *testing.T) {
			snap, in := mcpCapture(t, mixed())
			dec := exactAck(in)
			dec.Rewritten, dec.GovernedTools = true, gt
			_, err := snap.ApplyDecision(dec)
			requireMCPCode(t, err, MCPDenyBindingChanged)
		})
	}
	snap, in := mcpCapture(t, mixed())
	dec := exactAck(in)
	dec.Rewritten, dec.GovernedTools = true, []any{clamped, mcpMarkerView()}
	gov, err := snap.ApplyDecision(dec)
	if err != nil {
		t.Fatalf("valid mixed rewrite refused: %v", err)
	}
	tools, servers := sentMCP(t, frozenBody(t, gov))
	if len(tools) != 2 || tools[0].(map[string]any)["allowed_domains"] == nil ||
		!reflect.DeepEqual(tools[1], mustJSONValue(t, mcpToolsetRawJSON)) || len(servers) != 1 {
		t.Errorf("mixed rewrite forwarded %#v / %#v", tools, servers)
	}

	// Without MCP the legacy rewrite freedom stays (count may change), but MCP may not appear.
	plain := mcpPrompt()
	plain.Tools = []any{webSearchTool()}
	snapA, inA := mcpCapture(t, plain)
	decA := exactAck(inA)
	decA.Rewritten, decA.GovernedTools = true, []any{clamped, webSearchTool()}
	if _, err := snapA.ApplyDecision(decA); err != nil {
		t.Fatalf("legacy non-MCP rewrite refused: %v", err)
	}
	snapB, inB := mcpCapture(t, plain)
	decB := exactAck(inB)
	decB.Rewritten, decB.GovernedTools = true, []any{clamped, mcpMapToolset()}
	_, err = snapB.ApplyDecision(decB)
	requireMCPCode(t, err, MCPDenyBindingChanged)
}

// TestMCPBindingCheckRequestDetectsChanges proves the session check requires this
// snapshot's binding identity AND an unchanged MCP projection, absence included: every
// change a later stage could make is refused, and the untouched governed request passes.
func TestMCPBindingCheckRequestDetectsChanges(t *testing.T) {
	fresh := func(t *testing.T) (MessageRequest, *MCPEgressSnapshot) {
		gov, snap, _ := mcpGovern(t, mcpDecl([]any{webSearchTool(), mcpMapToolset()}, mcpMapServers()))
		return gov, snap
	}
	gov, snap := fresh(t)
	if err := snap.CheckRequest(gov); err != nil {
		t.Fatalf("untouched governed request refused: %v", err)
	}
	gov.MaxTokens = 32 // a non-MCP field is not part of the projection
	if err := snap.CheckRequest(gov); err != nil {
		t.Fatalf("non-MCP change refused: %v", err)
	}
	changes := map[string]func(*MessageRequest){
		"url": func(r *MessageRequest) {
			r.MCPServers[0].(map[string]any)["url"] = "https://mcp.example.com:8443/other"
		},
		"token":    func(r *MessageRequest) { r.MCPServers[0].(map[string]any)["authorization_token"] = "other" },
		"no token": func(r *MessageRequest) { delete(r.MCPServers[0].(map[string]any), "authorization_token") },
		"name":     func(r *MessageRequest) { r.Tools[1].(map[string]any)["mcp_server_name"] = "other" },
		"config": func(r *MessageRequest) {
			r.Tools[1].(map[string]any)["configs"] = map[string]any{mcpTestToolName: map[string]any{"enabled": false}}
		},
		"position":   func(r *MessageRequest) { r.Tools[0], r.Tools[1] = r.Tools[1], r.Tools[0] },
		"dropped":    func(r *MessageRequest) { r.MCPServers = nil },
		"added tool": func(r *MessageRequest) { r.Tools = append(r.Tools, webSearchTool()) },
		"rebuilt": func(r *MessageRequest) {
			*r = MessageRequest{Model: r.Model, MaxTokens: r.MaxTokens, Messages: r.Messages, Tools: r.Tools, MCPServers: r.MCPServers}
		},
		"injected slot": func(r *MessageRequest) {
			r.Tools[0] = map[string]any{"type": "mcp_toolset", "mcp_server_name": mcpTestServerName}
		},
	}
	for name, mut := range changes {
		t.Run(name, func(t *testing.T) {
			g, s := fresh(t)
			mut(&g)
			requireMCPCode(t, s.CheckRequest(g), MCPDenyBindingChanged)
		})
	}
	// Another snapshot's binding over identical content is still a different admission.
	g1, s1 := fresh(t)
	g2, _ := fresh(t)
	_ = g1
	requireMCPCode(t, s1.CheckRequest(g2), MCPDenyBindingChanged)
	// Absence: injecting MCP into an absence-bound request is refused.
	plain := mcpPrompt()
	plain.Tools = []any{webSearchTool()}
	pg, ps, _ := mcpGovern(t, plain)
	pg.Tools = append(pg.Tools, mcpMapToolset())
	pg.MCPServers = mcpMapServers()
	requireMCPCode(t, ps.CheckRequest(pg), MCPDenyBindingChanged)
	// Before its decision, a snapshot has no binding to check.
	_, early, err := SnapshotMCPEgress(mcpMapDecl())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	requireMCPCode(t, early.CheckRequest(mcpMapDecl()), MCPDenyBindingChanged)
}

// TestMCPGateToolsSanitizes pins the later-gate view (m7): fresh markers at MCP slots,
// non-MCP slots preserved, and no alias back into the request.
func TestMCPGateToolsSanitizes(t *testing.T) {
	calls := 0
	custom := map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}
	tools := []any{webSearchTool(), mcpTypedToolset(), mcpDisguised{calls: &calls}, custom, mcpMapToolset()}
	view := MCPGateTools(tools)
	want := []any{webSearchTool(), mcpMarkerView(), mcpMarkerView(), custom, mcpMarkerView()}
	if !reflect.DeepEqual(view, want) {
		t.Fatalf("gate view = %#v, want %#v", view, want)
	}
	view[4].(map[string]any)["mcp_server_name"] = "evil"
	if tools[4].(map[string]any)["mcp_server_name"] != mcpTestServerName {
		t.Fatal("the gate view aliases the request's MCP slot")
	}
	if again := MCPGateTools(tools); !reflect.DeepEqual(again[4], mcpMarkerView()) {
		t.Fatal("a mutated marker leaked into a later view")
	}
}

// ---- serializers ----------------------------------------------------------------------------

// TestForwardPreparedMCPBinding proves MarshalPrepared verifies its own serialized bytes
// against the accepted binding before freezing, and the frozen artifact carries the MCP beta
// once. A mutant that compares before the actual serializer (or not at all) fails the
// post-binding rows.
func TestForwardPreparedMCPBinding(t *testing.T) {
	gov, _, _ := mcpGovern(t, mcpMapDecl())
	p, err := MarshalPrepared(gov)
	if err != nil {
		t.Fatalf("MarshalPrepared: %v", err)
	}
	d := &mcpWireDoer{}
	if _, err := newMCPInference(d).ForwardPrepared(context.Background(), p); err != nil {
		t.Fatalf("ForwardPrepared: %v", err)
	}
	call := d.only(t)
	assertMCPBetaOnce(t, call, messagesPath)
	if !bytes.Equal(call.body, p.Body()) {
		t.Error("forwarded bytes differ from the verified frozen bytes")
	}
	assertServerValuesForwarded(t, call.body)
	assertToolConfigForwarded(t, call.body)

	calls := 0
	post := map[string]func(*MessageRequest){
		"url after binding": func(r *MessageRequest) { r.MCPServers[0].(map[string]any)["url"] = "https://mcp.example.com:8443/x" },
		"config after binding": func(r *MessageRequest) {
			r.Tools[0].(map[string]any)["default_config"] = map[string]any{"enabled": true}
		},
		"serializer emits MCP": func(r *MessageRequest) { r.Tools = append(r.Tools, mcpDisguised{calls: &calls}) },
	}
	for name, mut := range post {
		t.Run(name, func(t *testing.T) {
			g, _, _ := mcpGovern(t, mcpMapDecl())
			mut(&g)
			_, err := MarshalPrepared(g)
			requireMCPCode(t, err, MCPDenyBindingChanged)
		})
	}
	// Absence-bound: no MCP beta, and a serializer that emits MCP later is refused.
	plain := mcpPrompt()
	pg, _, _ := mcpGovern(t, plain)
	pp, err := MarshalPrepared(pg)
	if err != nil {
		t.Fatalf("absence MarshalPrepared: %v", err)
	}
	d2 := &mcpWireDoer{}
	if _, err := newMCPInference(d2).ForwardPreparedStream(context.Background(), pp, nil); err != nil {
		t.Fatalf("ForwardPreparedStream: %v", err)
	}
	assertNoMCPBeta(t, d2.only(t), messagesPath)
	pg.Tools = []any{mcpDisguised{calls: &calls}}
	_, err = MarshalPrepared(pg)
	requireMCPCode(t, err, MCPDenyBindingChanged)
}

// TestCountTokensMCPBinding proves count_tokens serializes once, verifies those bytes
// against the binding and sends them; a changed request is a typed refusal with ZERO HTTP
// requests (never an ordinary transport error the proxy would fail open on).
func TestCountTokensMCPBinding(t *testing.T) {
	gov, _, _ := mcpGovern(t, mcpMapDecl())
	d := &mcpWireDoer{json: mcpOKCount}
	if _, err := newMCPInference(d).CountTokens(context.Background(), gov); err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	call := d.only(t)
	assertMCPBetaOnce(t, call, countTokensPath)
	assertServerValuesForwarded(t, call.body)
	assertToolConfigForwarded(t, call.body)

	gov.MCPServers[0].(map[string]any)["authorization_token"] = "other"
	d2 := &mcpWireDoer{json: mcpOKCount}
	_, err := newMCPInference(d2).CountTokens(context.Background(), gov)
	requireMCPCode(t, err, MCPDenyBindingChanged)
	if len(d2.calls) != 0 {
		t.Fatalf("a refused count made %d HTTP request(s)", len(d2.calls))
	}
	pg, _, _ := mcpGovern(t, mcpPrompt())
	pg.Tools = []any{mcpMapToolset()}
	pg.MCPServers = mcpMapServers()
	d3 := &mcpWireDoer{json: mcpOKCount}
	_, err = newMCPInference(d3).CountTokens(context.Background(), pg)
	requireMCPCode(t, err, MCPDenyBindingChanged)
	if len(d3.calls) != 0 {
		t.Fatalf("an injected MCP count made %d HTTP request(s)", len(d3.calls))
	}
}

// TestBatches_MCPBindingPerEntry proves the frozen batch verifies EACH entry's params
// against that entry's own binding, identical entries get distinct nonces, and one changed
// entry refuses the whole envelope.
func TestBatches_MCPBindingPerEntry(t *testing.T) {
	g0, _, in0 := mcpGovern(t, mcpMapDecl())
	g1, _, _ := mcpGovern(t, mcpPrompt())
	g2, _, in2 := mcpGovern(t, mcpMapDecl())
	if in0.MCP.Coverage.Nonce == in2.MCP.Coverage.Nonce {
		t.Fatal("identical batch entries must be admitted with distinct nonces")
	}
	entries := []BatchRequest{{CustomID: "c0", Params: g0}, {CustomID: "c1", Params: g1}, {CustomID: "c2", Params: g2}}
	p, err := MarshalPreparedBatch(entries)
	if err != nil {
		t.Fatalf("MarshalPreparedBatch: %v", err)
	}
	d := &mcpWireDoer{json: mcpOKBatch}
	if _, _, err := newMCPInference(d).ForwardPreparedBatch(context.Background(), p); err != nil {
		t.Fatalf("ForwardPreparedBatch: %v", err)
	}
	call := d.only(t)
	assertMCPBetaOnce(t, call, batchesPath)
	var env struct {
		Requests []struct {
			Params json.RawMessage `json:"params"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(call.body, &env); err != nil || len(env.Requests) != 3 {
		t.Fatalf("batch envelope: %v (%d entries)", err, len(env.Requests))
	}
	for _, i := range []int{0, 2} {
		assertServerValuesForwarded(t, env.Requests[i].Params)
		assertToolConfigForwarded(t, env.Requests[i].Params)
	}
	if bytes.Contains(env.Requests[1].Params, []byte("mcp_")) {
		t.Errorf("the entry without MCP gained a declaration: %s", env.Requests[1].Params)
	}

	for name, mut := range map[string]func([]BatchRequest){
		"later entry url": func(e []BatchRequest) {
			e[2].Params.MCPServers[0].(map[string]any)["url"] = "https://mcp.example.com:9443/x"
		},
		"absence entry gained": func(e []BatchRequest) {
			e[1].Params.Tools = []any{mcpMapToolset()}
			e[1].Params.MCPServers = mcpMapServers()
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, _, _ := mcpGovern(t, mcpMapDecl())
			b, _, _ := mcpGovern(t, mcpPrompt())
			c, _, _ := mcpGovern(t, mcpMapDecl())
			e := []BatchRequest{{CustomID: "c0", Params: a}, {CustomID: "c1", Params: b}, {CustomID: "c2", Params: c}}
			mut(e)
			_, err := MarshalPreparedBatch(e)
			requireMCPCode(t, err, MCPDenyBindingChanged)
		})
	}
}

// ---- the shell's legacy fallback (m8) --------------------------------------------------

// boundDecider returns fixed allow decisions, so a test controls exactly which governed
// request and which (possibly missing) frozen artifact reach the shell.
type boundDecider struct {
	dec   ProxyDecision
	batch ProxyBatchDecision
}

func (d boundDecider) Authorize(context.Context, MessageRequest, string) ProxyDecision { return d.dec }
func (d boundDecider) Finalize(context.Context, any, ProxyForwardResult) ProxyResponseVerdict {
	return ProxyResponseVerdict{}
}
func (d boundDecider) AuthorizeBatch(context.Context, []BatchRequest, string) ProxyBatchDecision {
	return d.batch
}
func (d boundDecider) FinalizeBatch(context.Context, any, ProxyBatchForwardResult) {}

func requireShellBindingRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q: a refusal must precede any SSE header", ct)
	}
	if !strings.Contains(rec.Body.String(), `"message":"mcp_binding_changed"`) {
		t.Fatalf("body = %s, want the fixed mcp_binding_changed reason", rec.Body.String())
	}
}

// TestMCPProxyFallbackRefusesBoundRequest proves ServeHTTP refuses a bound governed request
// without its frozen artifact on the blocking, passthrough-stream, buffered-stream and batch
// routes, before any header or upstream request; the forwarding helpers refuse it directly
// too. An unbound request keeps the legacy fallback.
func TestMCPProxyFallbackRefusesBoundRequest(t *testing.T) {
	gov, _, _ := mcpGovern(t, mcpMapDecl())
	absent, _, _ := mcpGovern(t, mcpPrompt())
	streamGov := gov
	streamGov.Stream = true
	rows := []struct {
		name string
		dec  ProxyDecision
	}{
		{"blocking", ProxyDecision{Allow: true, Request: gov}},
		{"accepted absence", ProxyDecision{Allow: true, Request: absent}},
		{"stream passthrough", ProxyDecision{Allow: true, Request: streamGov}},
		{"stream buffered", ProxyDecision{Allow: true, Request: streamGov, BufferResponse: true}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			doer := &proxyStubDoer{status: http.StatusOK, body: okMessageJSON}
			p := newProxy(t, doer, boundDecider{dec: r.dec}, nil)
			rec := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[]}`, "k")
			requireShellBindingRefusal(t, rec)
			if doer.calls != 0 {
				t.Fatalf("refused fallback made %d upstream call(s)", doer.calls)
			}
		})
	}
	t.Run("batch", func(t *testing.T) {
		doer := &proxyStubDoer{status: http.StatusOK, body: `{"id":"b"}`}
		dec := ProxyBatchDecision{Allow: true, Requests: []BatchRequest{{CustomID: "c0", Params: absent}, {CustomID: "c1", Params: gov}}}
		p := newProxy(t, doer, boundDecider{batch: dec}, nil)
		rec := postBatch(t, p, `{"requests":[{"custom_id":"c0","params":{"model":"m","max_tokens":1,"messages":[]}}]}`, "k")
		requireShellBindingRefusal(t, rec)
		if doer.calls != 0 {
			t.Fatalf("refused batch fallback made %d upstream call(s)", doer.calls)
		}
	})
	t.Run("forwarding helpers", func(t *testing.T) {
		doer := &proxyStubDoer{status: http.StatusOK, body: okMessageJSON}
		p := newProxy(t, doer, nil, nil)
		_, _, err := p.forwardMessage(context.Background(), ProxyDecision{Allow: true, Request: gov}, false, nil)
		requireMCPCode(t, err, MCPDenyBindingChanged)
		_, _, _, err = p.forwardBatch(context.Background(), ProxyBatchDecision{Allow: true, Requests: []BatchRequest{{Params: gov}}})
		requireMCPCode(t, err, MCPDenyBindingChanged)
		if doer.calls != 0 {
			t.Fatalf("forwarding helpers made %d upstream call(s)", doer.calls)
		}
	})
	t.Run("unbound legacy fallback", func(t *testing.T) {
		doer := &proxyStubDoer{status: http.StatusOK, body: okMessageJSON}
		p := newProxy(t, doer, boundDecider{dec: ProxyDecision{Allow: true, Request: mcpMapDecl()}}, nil)
		rec := postMessages(t, p, `{"model":"claude-opus-4-8","max_tokens":16,"messages":[]}`, "k")
		if rec.Code != http.StatusOK || doer.calls != 1 {
			t.Fatalf("unbound fallback: status=%d calls=%d, want 200 and one forward", rec.Code, doer.calls)
		}
	})
}

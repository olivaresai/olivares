// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// TestStatelessDiscover: server/discover parses supportedVersions, capabilities
// (incl. extensions), identity and the CacheableResult fields.
func TestStatelessDiscover(t *testing.T) {
	mt := newMockTransport()
	mt.reply(methodServerDiscover, `{
		"resultType":"complete",
		"supportedVersions":["2025-11-25","2026-07-28"],
		"capabilities":{"tools":{},"extensions":{"io.modelcontextprotocol/tasks":{}}},
		"instructions":"be nice",
		"ttlMs":3600000,"cacheScope":"public",
		"_meta":{"traceparent":"00-aaa-bbb-01",
		         "io.modelcontextprotocol/serverInfo":{"name":"srv","version":"2.0"}}}`)
	c := newStatelessClient(mt)
	d, err := c.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// serverInfo lives INSIDE result._meta — DiscoverResult defines
	// `_meta['io.modelcontextprotocol/serverInfo']` and the spec's normative
	// example nests it there. This fixture previously put it at TOP LEVEL,
	// matching an implementation that read a field the schema does not define:
	// test and code were wrong together, so the identity was always empty and
	// nothing noticed.
	if !d.supports(revision20260728) {
		t.Errorf("supportedVersions not parsed: %+v", d)
	}
	if si := d.serverIdentity(); si.Name != "srv" || si.Version != "2.0" {
		t.Errorf("server identity from _meta = %+v, want name=srv version=2.0", si)
	}
	if _, ok := extensionIDs(d.Capabilities)[extensionTasks]; !ok {
		t.Error("capabilities.extensions must surface the tasks extension id")
	}
	if d.TTLMs == nil || *d.TTLMs != 3600000 || d.CacheScope != cacheScopePublic {
		t.Errorf("CacheableResult fields not parsed: ttl=%v scope=%q", d.TTLMs, d.CacheScope)
	}
	if tc := extractTraceContext(d.Meta); tc.TraceParent != "00-aaa-bbb-01" {
		t.Errorf("trace context from discover _meta = %q", tc.TraceParent)
	}
	if mt.calls[0] != methodServerDiscover {
		t.Errorf("first call = %q, want server/discover (no initialize)", mt.calls[0])
	}
}

// TestStatelessInputRequiredDeclined: an MRTR input_required answer mid-listing
// is a terminal, deny-closed decline — surfaced as a typed error, never retried.
func TestStatelessInputRequiredDeclined(t *testing.T) {
	mt := newMockTransport()
	mt.reply("tools/list", `{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create","params":{}}}}`)
	c := newStatelessClient(mt)
	_, err := c.ListTools(context.Background())
	var ir *errInputRequired
	if !errors.As(err, &ir) {
		t.Fatalf("want errInputRequired, got %v", err)
	}
	if got := len(mt.calls); got != 1 {
		t.Errorf("calls = %d, want exactly 1 (an input_required result must NOT be retried)", got)
	}
}

// TestStatelessCacheHints: the first page's SEP-2549 metadata is recorded as a
// freshness hint; a page without metadata records none (never fabricated).
func TestStatelessCacheHints(t *testing.T) {
	mt := newMockTransport()
	mt.reply("tools/list",
		`{"resultType":"complete","tools":[{"name":"a"}],"nextCursor":"p2","ttlMs":60000,"cacheScope":"private"}`,
		`{"resultType":"complete","tools":[{"name":"b"}],"ttlMs":1000,"cacheScope":"private"}`)
	mt.reply("prompts/list", `{"resultType":"complete","prompts":[]}`)
	c := newStatelessClient(mt)
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if _, err := c.ListPrompts(context.Background()); err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	hints := c.cacheHints()
	if len(hints) != 1 {
		t.Fatalf("hints = %d, want 1 (first tools/list page only; promptless page has no metadata)", len(hints))
	}
	h := hints[0]
	if h.method != "tools/list" || h.scope != cacheScopePrivate || h.ttlMs == nil || *h.ttlMs != 60000 {
		t.Errorf("hint = %+v", h)
	}
}

// TestPollTaskHonorsTerminalAndInputRequired: PollTask polls tasks/get until a
// terminal status; an input_required task is returned AS-IS (this connector
// never answers MRTR input).
func TestPollTaskHonorsTerminalAndInputRequired(t *testing.T) {
	mt := newMockTransport()
	mt.reply("tasks/get",
		`{"resultType":"complete","taskId":"t1","status":"working","pollIntervalMs":1}`,
		`{"resultType":"complete","taskId":"t1","status":"completed","result":{"content":[]}}`)
	c := newStatelessClient(mt)
	task, err := c.PollTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if task.Status != taskStatusCompleted || len(mt.calls) != 2 {
		t.Errorf("status=%q calls=%d, want completed after 2 polls", task.Status, len(mt.calls))
	}

	mt2 := newMockTransport()
	mt2.reply("tasks/get", `{"resultType":"complete","taskId":"t2","status":"input_required","inputRequests":{"k":{"method":"elicitation/create"}}}`)
	c2 := newStatelessClient(mt2)
	task2, err := c2.PollTask(context.Background(), "t2")
	if err != nil {
		t.Fatalf("poll input_required: %v", err)
	}
	if task2.Status != taskStatusInputRequired || len(mt2.calls) != 1 {
		t.Errorf("an input_required task must be returned as-is without re-polling (status=%q calls=%d)", task2.Status, len(mt2.calls))
	}
}

// TestTaskFromResult: a tools/call answering with a task handle (resultType
// "task") parses into the flat CreateTaskResult shape; a plain result does not.
func TestTaskFromResult(t *testing.T) {
	task, ok := taskFromResult(json.RawMessage(`{"resultType":"task","taskId":"t1","status":"working","ttlMs":60000,"pollIntervalMs":5000}`))
	if !ok || task.TaskID != "t1" || task.Status != taskStatusWorking {
		t.Errorf("task handle parse: ok=%v task=%+v", ok, task)
	}
	if _, ok := taskFromResult(json.RawMessage(`{"resultType":"complete","content":[]}`)); ok {
		t.Error("a complete result is not a task handle")
	}
}

// fakeStatelessServer answers the full RC introspection flow over Streamable
// HTTP (server/discover + capability-gated lists with CacheableResult fields).
func fakeStatelessServer(t *testing.T, supported []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("server decode: %v", err)
		}
		// The RC contract: every request carries the three required _meta keys and
		// the mirrored headers.
		if body.Params.Meta[metaProtocolVersion] == nil || body.Params.Meta[metaClientInfo] == nil || body.Params.Meta[metaClientCapabilities] == nil {
			t.Errorf("request %s missing required _meta keys: %v", body.Method, body.Params.Meta)
		}
		if got := r.Header.Get(headerMcpMethod); got != body.Method {
			t.Errorf("Mcp-Method %q != body method %q", got, body.Method)
		}
		sv, _ := json.Marshal(supported)
		var res string
		switch body.Method {
		case methodServerDiscover:
			res = `{"resultType":"complete","supportedVersions":` + string(sv) + `,
				"capabilities":{"tools":{},"prompts":{},"extensions":{"io.modelcontextprotocol/tasks":{},"io.modelcontextprotocol/ui":{},"com.example/custom":{}}},
				"ttlMs":3600000,"cacheScope":"public","_meta":{"traceparent":"00-cafe-beef-01",
				"io.modelcontextprotocol/serverInfo":{"name":"rc-srv","title":"RC Server","version":"1.0"}}}`
		case "tools/list":
			res = `{"resultType":"complete","tools":[{"name":"read_file","annotations":{"readOnlyHint":true},"inputSchema":{"$schema":"http://json-schema.org/draft-07/schema#"}}],"ttlMs":30000,"cacheScope":"private"}`
		case "prompts/list":
			res = `{"resultType":"complete","prompts":[{"name":"review"}],"ttlMs":30000,"cacheScope":"public"}`
		default:
			t.Errorf("unexpected method %q (capability gating must prevent it)", body.Method)
			res = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, jsonID(body.ID), res)
	}))
}

// TestIntrospectStateless: the full stateless flow — no initialize on the wire,
// capabilities from server/discover, 2026-07-28 extras on the catalog, and the
// downstream finding mappers seeing what they need.
func TestIntrospectStateless(t *testing.T) {
	srv := fakeStatelessServer(t, []string{revision20251125, revision20260728})
	defer srv.Close()

	cat, err := introspect(context.Background(), serverSpec{Name: "rc", URL: srv.URL, NextRevision: boolRef(true)})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !cat.nextRevision || cat.server.ProtocolVersion != revision20260728 {
		t.Errorf("catalog revision: next=%v version=%q", cat.nextRevision, cat.server.ProtocolVersion)
	}
	if len(cat.tools) != 1 || cat.tools[0].Name != "read_file" {
		t.Errorf("tools = %+v", cat.tools)
	}
	if len(cat.prompts) != 1 {
		t.Errorf("prompts = %+v", cat.prompts)
	}
	if cat.trace.TraceParent != "00-cafe-beef-01" {
		t.Errorf("trace from discover _meta = %q", cat.trace.TraceParent)
	}
	// Cache hints: discover + tools/list + prompts/list.
	if len(cat.cacheHints) != 3 {
		t.Errorf("cacheHints = %+v, want discover + 2 lists", cat.cacheHints)
	}

	// The surface mapper turns the stateless extras into findings: named extensions
	// (tasks, ui→MCP10), the unknown-extension count, cache metadata, and the
	// non-2020-12 dialect signal.
	fs := surfaceFindings("rc", cat, fixedTime())
	var titles []string
	for _, f := range fs {
		titles = append(titles, f.Title)
	}
	joined := strings.Join(titles, "\n")
	for _, want := range []string{
		extensionTasks,
		"[MCP10]", // MCP Apps is an input/UI vector
		"1 unrecognized protocol extension(s)",
		"cache freshness metadata",
		"non-2020-12 JSON Schema dialect",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("surface findings missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "com.example/custom") {
		t.Error("an unrecognized extension id must NOT be echoed into a finding title")
	}

	// The revision finding classifies 2026-07-28 as the current baseline revision.
	rf := revisionFinding("rc", cat.server.ProtocolVersion, fixedTime())
	if !strings.Contains(rf.Title, "current protocol revision") || !strings.Contains(rf.Title, currentRevision) {
		t.Errorf("revision finding title = %q", rf.Title)
	}
}

// TestIntrospectStatelessRefusesNonRCServer: a server that answers discover but
// does not list the RC revision fails loudly (the operator opted it in; no
// silent fallback to the legacy path).
func TestIntrospectStatelessRefusesNonRCServer(t *testing.T) {
	srv := fakeStatelessServer(t, []string{revision20251125})
	defer srv.Close()
	_, err := introspect(context.Background(), serverSpec{Name: "old", URL: srv.URL, NextRevision: boolRef(true)})
	if err == nil || !strings.Contains(err.Error(), "supportedVersions") {
		t.Fatalf("want a loud supportedVersions error, got %v", err)
	}
}

// TestIntrospectStableUnchanged: the flag-OFF path still performs the
// 2025-11-25 handshake (initialize first on the wire) — announced compat.
func TestIntrospectStableUnchanged(t *testing.T) {
	mt := newMockTransport()
	mt.reply("initialize", `{"protocolVersion":"2025-11-25","serverInfo":{"name":"s"},"capabilities":{"tools":{}}}`)
	mt.reply("tools/list", `{"tools":[{"name":"a"}]}`)
	c := newClient(mt)
	init, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if init.ProtocolVersion != revision20251125 {
		t.Errorf("stable negotiates %q", init.ProtocolVersion)
	}
	if mt.calls[0] != "initialize" {
		t.Errorf("legacy path must start with initialize, got %v", mt.calls)
	}
}

func TestIntrospectAutoNegotiatesLegacyWithFinding(t *testing.T) {
	srv, calls := fakeLegacyAfterStatelessError(t, -32601, nil)
	defer srv.Close()

	src := New()
	err := src.Open(context.Background(), sdk.Config{Settings: map[string]string{
		cfgServers: `[{"name":"auto","transport":"http","url":"` + srv.URL + `"}]`,
	}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &fakeSink{}
	if err := src.Gather(context.Background(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if !calledInOrder(*calls, methodServerDiscover, "initialize") {
		t.Fatalf("auto negotiation calls = %v, want discover then initialize", *calls)
	}
	var sawDown bool
	for _, f := range sink.findings() {
		if f.Kind == findingRevision && strings.Contains(f.Title, "auto-negotiated down") && strings.Contains(f.Title, revision20251125) {
			sawDown = true
		}
	}
	if !sawDown {
		t.Fatalf("negotiated-down finding not emitted; findings=%+v", sink.findings())
	}
}

func TestIntrospectAutoFallbackClassifiers(t *testing.T) {
	for _, code := range []int{rpcUnsupportedProtocolVersion, rpcUnsupportedProtocolVersionPreFreeze} {
		t.Run(fmt.Sprintf("unsupported-%d", code), func(t *testing.T) {
			srv, calls := fakeLegacyAfterStatelessError(t, code, []string{revision20251125})
			defer srv.Close()
			cat, err := introspectWithPreview(context.Background(), serverSpec{Name: "old", Transport: transportHTTP, URL: srv.URL}, true)
			if err != nil {
				t.Fatalf("auto introspect: %v", err)
			}
			if !cat.negotiatedDown || cat.server.ProtocolVersion != revision20251125 {
				t.Fatalf("catalog = negotiatedDown:%v revision:%q, want fallback to %s", cat.negotiatedDown, cat.server.ProtocolVersion, revision20251125)
			}
			if !calledInOrder(*calls, methodServerDiscover, "initialize") {
				t.Fatalf("calls = %v, want discover then initialize", *calls)
			}
		})
	}

	t.Run("discover-supportedVersions-lacks-rc", func(t *testing.T) {
		srv, calls := fakeDiscoverWithoutRCServer(t)
		defer srv.Close()
		cat, err := introspectWithPreview(context.Background(), serverSpec{Name: "old", Transport: transportHTTP, URL: srv.URL}, true)
		if err != nil {
			t.Fatalf("auto introspect: %v", err)
		}
		if !cat.negotiatedDown || cat.server.ProtocolVersion != revision20251125 {
			t.Fatalf("catalog = negotiatedDown:%v revision:%q, want fallback to %s", cat.negotiatedDown, cat.server.ProtocolVersion, revision20251125)
		}
		if !calledInOrder(*calls, methodServerDiscover, "initialize") {
			t.Fatalf("calls = %v, want discover then initialize", *calls)
		}
	})
}

func TestIntrospectExplicitNextRevisionModes(t *testing.T) {
	srv, calls := fakeLegacyAfterStatelessError(t, -32601, nil)
	defer srv.Close()

	_, err := introspectWithPreview(context.Background(), serverSpec{Name: "forced-rc", Transport: transportHTTP, URL: srv.URL, NextRevision: boolRef(true)}, true)
	if err == nil {
		t.Fatal("explicit next_revision=true must fail loudly on a legacy server")
	}
	if called(*calls, "initialize") {
		t.Fatalf("explicit RC mode must not fall back; calls=%v", *calls)
	}

	srv2, calls2 := fakeLegacyAfterStatelessError(t, -32601, nil)
	defer srv2.Close()
	cat, err := introspectWithPreview(context.Background(), serverSpec{Name: "forced-legacy", Transport: transportHTTP, URL: srv2.URL, NextRevision: boolRef(false)}, true)
	if err != nil {
		t.Fatalf("explicit legacy introspect: %v", err)
	}
	if cat.server.ProtocolVersion != revision20251125 {
		t.Fatalf("explicit legacy negotiated %q", cat.server.ProtocolVersion)
	}
	if called(*calls2, methodServerDiscover) {
		t.Fatalf("explicit legacy mode must not probe discover; calls=%v", *calls2)
	}
}

func TestIntrospectAutoNetworkErrorDoesNotFallback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = introspectWithPreview(ctx, serverSpec{Name: "dead", Transport: transportHTTP, URL: url}, true)
	if err == nil {
		t.Fatal("dead server must fail")
	}
	if strings.Contains(err.Error(), "initialize") {
		t.Fatalf("network failure must not fall back to legacy initialize: %v", err)
	}
	if !strings.Contains(err.Error(), methodServerDiscover) {
		t.Fatalf("network failure should surface the failed RC probe: %v", err)
	}
}

func boolRef(v bool) *bool { return &v }

func fakeLegacyAfterStatelessError(t *testing.T, discoverCode int, supported []string) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("server decode: %v", err)
		}
		method, _ := body["method"].(string)
		calls = append(calls, method)
		if body["id"] == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case methodServerDiscover:
			status := http.StatusBadRequest
			if discoverCode == -32601 {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			data := ""
			if supported != nil {
				raw, _ := json.Marshal(map[string]any{"supported": supported})
				data = `,"data":` + string(raw)
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"no rc"%s}}`, jsonID(body["id"]), discoverCode, data)
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"%s","serverInfo":{"name":"legacy","version":"1"},"capabilities":{"tools":{}}}}`, jsonID(body["id"]), revision20251125)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"legacy_tool"}]}}`, jsonID(body["id"]))
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, jsonID(body["id"]))
		}
	}))
	return srv, &calls
}

func fakeDiscoverWithoutRCServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("server decode: %v", err)
		}
		method, _ := body["method"].(string)
		calls = append(calls, method)
		if body["id"] == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case methodServerDiscover:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","supportedVersions":["%s"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"legacy","version":"1"}}}}`, jsonID(body["id"]), revision20251125)
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"%s","serverInfo":{"name":"legacy","version":"1"},"capabilities":{"tools":{}}}}`, jsonID(body["id"]), revision20251125)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"legacy_tool"}]}}`, jsonID(body["id"]))
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, jsonID(body["id"]))
		}
	}))
	return srv, &calls
}

func called(calls []string, method string) bool {
	for _, call := range calls {
		if call == method {
			return true
		}
	}
	return false
}

func calledInOrder(calls []string, first, second string) bool {
	firstAt := -1
	for i, call := range calls {
		if call == first && firstAt < 0 {
			firstAt = i
			continue
		}
		if call == second && firstAt >= 0 && i > firstAt {
			return true
		}
	}
	return false
}

// TestStatelessDiscoverIgnoresTopLevelServerInfo is the regression guard for the
// defect the fixture above used to encode: a TOP-LEVEL `serverInfo` field is not
// part of DiscoverResult, so it must be ignored rather than trusted. A server
// that emits it (an older or non-conforming one) must not be able to make this
// connector report an identity the spec never sanctioned — the honest answer is
// the empty identity, which callers surface as unknown.
func TestStatelessDiscoverIgnoresTopLevelServerInfo(t *testing.T) {
	mt := newMockTransport()
	mt.reply(methodServerDiscover, `{
		"resultType":"complete",
		"supportedVersions":["2026-07-28"],
		"capabilities":{},
		"serverInfo":{"name":"spoofed","version":"9.9"},
		"ttlMs":1000,"cacheScope":"public","_meta":{}}`)
	d, err := newStatelessClient(mt).Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if si := d.serverIdentity(); si.Name != "" || si.Version != "" {
		t.Errorf("identity = %+v; a top-level serverInfo is not defined by DiscoverResult and must not be read", si)
	}
}

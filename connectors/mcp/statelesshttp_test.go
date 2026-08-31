// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statelessServerRecorder captures what the stateless transport puts on the
// wire, so the tests assert the RC contract: routing headers mirroring the
// body, required `_meta`, and NO session header in either direction.
type statelessServerRecorder struct {
	gotHeaders []http.Header
	gotBodies  []map[string]any
}

func (rec *statelessServerRecorder) handler(t *testing.T, respond func(method string, id any) (string, int)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.gotHeaders = append(rec.gotHeaders, r.Header.Clone())
		var body map[string]any
		raw, _ := json.Marshal("")
		_ = raw
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("server: decode body: %v", err)
		}
		rec.gotBodies = append(rec.gotBodies, body)
		method, _ := body["method"].(string)
		res, status := respond(method, body["id"])
		// A legacy server minting a session id: the RC client must IGNORE it.
		w.Header().Set(headerMcpSessionID, "legacy-session-123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":%s}`, jsonID(body["id"]), res)
	}
}

func jsonID(id any) string {
	b, _ := json.Marshal(id)
	return string(b)
}

// TestStatelessTransportHeadersAndMeta: every POST carries MCP-Protocol-Version
// (== the body `_meta` value), Mcp-Method, Mcp-Name only where the table says,
// and never an Mcp-Session-Id — even after the server minted one.
func TestStatelessTransportHeadersAndMeta(t *testing.T) {
	rec := &statelessServerRecorder{}
	srv := httptest.NewServer(rec.handler(t, func(method string, _ any) (string, int) {
		return `{"resultType":"complete","tools":[]}`, http.StatusOK
	}))
	defer srv.Close()

	tr, err := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	c := newStatelessClient(tr)
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if _, err := c.TaskGet(context.Background(), "task-9"); err != nil {
		t.Fatalf("TaskGet: %v", err)
	}

	if len(rec.gotHeaders) != 2 {
		t.Fatalf("requests = %d, want 2", len(rec.gotHeaders))
	}
	list, task := rec.gotHeaders[0], rec.gotHeaders[1]

	if got := list.Get(headerMCPProtocolVersion); got != revision20260728 {
		t.Errorf("MCP-Protocol-Version = %q, want %s", got, revision20260728)
	}
	if got := list.Get(headerMcpMethod); got != "tools/list" {
		t.Errorf("Mcp-Method = %q, want tools/list", got)
	}
	if got := list.Get(headerMcpName); got != "" {
		t.Errorf("Mcp-Name must be omitted on tools/list, got %q", got)
	}
	if got := task.Get(headerMcpName); got != "task-9" {
		t.Errorf("Mcp-Name on tasks/get = %q, want the taskId (SEP-2663 sticky routing)", got)
	}
	// Sessionless: the client must never send a session header, even though the
	// first response minted one (SEP-2567: sessions are removed; ignore them).
	if got := task.Get(headerMcpSessionID); got != "" {
		t.Errorf("the RC client must NEVER send Mcp-Session-Id, got %q", got)
	}

	// Body `_meta`: required on every request; header MUST equal it byte-for-byte.
	for i, body := range rec.gotBodies {
		params, _ := body["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if meta == nil {
			t.Fatalf("request %d carries no _meta", i)
		}
		if meta[metaProtocolVersion] != rec.gotHeaders[i].Get(headerMCPProtocolVersion) {
			t.Errorf("request %d: header/_meta protocolVersion mismatch", i)
		}
		if meta[metaClientInfo] == nil || meta[metaClientCapabilities] == nil {
			t.Errorf("request %d: _meta must carry clientInfo and clientCapabilities", i)
		}
	}
	// The tasks request must declare the tasks extension (per-request, SEP-2663).
	tparams := rec.gotBodies[1]["params"].(map[string]any)
	tmeta := tparams["_meta"].(map[string]any)
	caps := tmeta[metaClientCapabilities].(map[string]any)
	exts, _ := caps["extensions"].(map[string]any)
	if _, ok := exts[extensionTasks]; !ok {
		t.Errorf("tasks/get must declare %s in clientCapabilities.extensions, got %v", extensionTasks, caps)
	}
}

// TestStatelessTransportSurfacesRPCErrorOn400: the RC rides JSON-RPC errors on
// HTTP 400 UnsupportedProtocolVersion — the client surfaces the coded error,
// not a bare "http 400"; pre-freeze -32004 is tolerated during the RC window.
func TestStatelessTransportSurfacesRPCErrorOn400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"Unsupported protocol version","data":{"supported":["2025-11-25"]}}}`)
	}))
	defer srv.Close()

	tr, _ := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
	c := newStatelessClient(tr)
	_, err := c.Discover(context.Background())
	var rpc *rpcError
	if !errors.As(err, &rpc) || !isUnsupportedProtocolVersionCode(rpc.Code) {
		t.Fatalf("want the UnsupportedProtocolVersion rpcError surfaced, got %v", err)
	}
	if got := unsupportedVersionDetail(rpc); len(got) != 1 || got[0] != revision20251125 {
		t.Errorf("supported detail = %v, want [2025-11-25]", got)
	}
}

func TestStatelessIntrospectionIgnoresUnsolicitedLoggingNotification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch body.Method {
		case methodServerDiscover:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"resultType":"complete","supportedVersions":["%s"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"rc-log","version":"1"}}}}`,
				body.ID, revision20260728)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"ignored\"}}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"resultType\":\"complete\",\"tools\":[]}}\n\n", body.ID)
		default:
			t.Errorf("unexpected method %q", body.Method)
		}
	}))
	defer srv.Close()

	cat, err := introspect(context.Background(), serverSpec{Name: "rc-log", URL: srv.URL, NextRevision: boolRef(true)})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(cat.tools) != 0 {
		t.Fatalf("tools = %+v, want empty list from final response", cat.tools)
	}
	if len(cat.observed) != 0 {
		t.Fatalf("notifications/message must be ignored, not observed: %+v", cat.observed)
	}
}

// TestStatelessListen: the subscriptions/listen stream — ack MUST come first,
// events are demultiplexed by the subscriptionId `_meta` tag, server close ends
// the listen cleanly.
func TestStatelessListen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(headerMcpMethod); got != methodSubscriptionsListen {
			t.Errorf("listen Mcp-Method = %q", got)
		}
		var body struct {
			ID     int64 `json:"id"`
			Params struct {
				Notifications subscriptionFilter `json:"notifications"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Params.Notifications.ToolsListChanged {
			t.Error("listen params must carry the opt-in filter")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sid := fmt.Sprintf("%d", body.ID)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"%s\",\"params\":{\"_meta\":{\"%s\":\"%s\"},\"notifications\":{\"toolsListChanged\":true}}}\n\n",
			notificationSubscriptionsAcknowledged, metaSubscriptionID, sid)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{\"_meta\":{\"%s\":\"%s\"}}}\n\n",
			metaSubscriptionID, sid)
		// SubscriptionsListenResultResponse: the published 2026-07-28 schema's
		// graceful teardown — "sent when the server tears the subscription down
		// gracefully". Without it the client cannot tell a deliberate shutdown from
		// a dropped connection, which is what this fixture used to model.
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", body.ID)
	}))
	defer srv.Close()

	tr, _ := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
	c := newStatelessClient(tr)
	var events []subscriptionEvent
	err := c.Listen(context.Background(), subscriptionFilter{ToolsListChanged: true}, func(e subscriptionEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want ack + list_changed", len(events))
	}
	if events[0].Method != notificationSubscriptionsAcknowledged {
		t.Errorf("first event = %q, want the acknowledgment", events[0].Method)
	}
	if events[1].Method != "notifications/tools/list_changed" || events[1].SubscriptionID == "" {
		t.Errorf("second event = %+v, want list_changed tagged with a subscriptionId", events[1])
	}
}

// TestStatelessListenAckFirstDenyClosed: a stream that does NOT start with the
// acknowledgment is refused (deny-closed: never consume an unacknowledged stream).
func TestStatelessListenAckFirstDenyClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{}}\n\n")
	}))
	defer srv.Close()

	tr, _ := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
	c := newStatelessClient(tr)
	err := c.Listen(context.Background(), subscriptionFilter{ToolsListChanged: true}, func(subscriptionEvent) {})
	if err == nil || !strings.Contains(err.Error(), notificationSubscriptionsAcknowledged) {
		t.Fatalf("an unacknowledged stream must be refused, got %v", err)
	}
}

// TestStatelessListenRequiresStream: a server answering JSON instead of an SSE
// stream is refused (the listen request never resolves to a plain result).
func TestStatelessListenRequiresStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, _ := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
	c := newStatelessClient(tr)
	err := c.Listen(context.Background(), subscriptionFilter{}, func(subscriptionEvent) {})
	if err == nil || !strings.Contains(err.Error(), "event stream") {
		t.Fatalf("a non-SSE listen response must be refused, got %v", err)
	}
}

// TestStatelessClientRefusesInitialize: the RC removed the handshake; the
// stateless client refuses to emit it (dual-version discipline: each mode
// speaks ONLY its own revision's methods).
func TestStatelessClientRefusesInitialize(t *testing.T) {
	c := newStatelessClient(newMockTransport())
	if _, err := c.Initialize(context.Background()); err == nil {
		t.Fatal("a stateless client must refuse initialize")
	}
	// And the stable client refuses the RC-only calls.
	stable := newClient(newMockTransport())
	if _, err := stable.Discover(context.Background()); err == nil {
		t.Fatal("a stable client must refuse server/discover")
	}
	if err := stable.Listen(context.Background(), subscriptionFilter{}, func(subscriptionEvent) {}); err == nil {
		t.Fatal("a stable client must refuse subscriptions/listen")
	}
}

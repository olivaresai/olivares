// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagedMCPStreamableSessionAndStrictSSE(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var method string
		_ = json.Unmarshal(req["method"], &method)
		if method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "fixture-session")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}}`)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "fixture-session" || r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
			t.Error("negotiated headers lost")
		}
		if method == "notifications/initialized" {
			if _, ok := req["id"]; ok {
				t.Error("notification carries request id")
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[]}}\n\n")
	}))
	defer srv.Close()
	f := &UpstreamForwarder{URL: srv.URL, Client: srv.Client()}
	// Managed transport is selected by composition, never operator JSON.
	EnableManagedForwarding(f)
	for _, method := range []string{"initialize", "notifications/initialized", "tools/list"} {
		res, err := f.Forward(context.Background(), mcpc.UpstreamRequest{Method: method, Subject: "agent:A", Params: []byte(`{}`)})
		if err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("%s: %s %v", method, res.State, err)
		}
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
	for _, req := range []mcpc.UpstreamRequest{
		{Method: "tools/list", Subject: "agent:A", ClientID: "other-client", Params: []byte(`{}`)},
		{Method: "tools/list", Subject: "agent:A", Scopes: []string{"other:scope"}, Params: []byte(`{}`)},
	} {
		if res, err := f.Forward(context.Background(), req); err == nil || res.State != mcpc.DispatchNotSent || calls != 3 {
			t.Fatal("client or scope reused foreign session")
		}
	}
	// A different subject cannot inherit the first subject's transport session.
	if res, err := f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/list", Subject: "agent:B", Params: []byte(`{}`)}); err == nil || res.State != mcpc.DispatchNotSent || calls != 3 {
		t.Fatal("foreign subject reused session")
	}
}

func TestManagedMCPSSEOutcomeUnknownDoesNotRetry(t *testing.T) {
	for name, response := range map[string]string{
		"wrong-id":         "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n\n",
		"duplicate-result": "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"result\":{}}\n\n",
		"server-request":   "data: {\"jsonrpc\":\"2.0\",\"id\":77,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n",
		"no-response":      "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, response)
			}))
			defer srv.Close()
			f := &UpstreamForwarder{URL: srv.URL, Client: srv.Client()}
			EnableManagedForwarding(f)
			res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "initialize", Subject: "agent:A", Params: []byte(`{}`)})
			if err == nil || res.State != mcpc.DispatchUnknown || calls != 1 {
				t.Fatalf("ambiguous response: state=%s calls=%d", res.State, calls)
			}
		})
	}
}

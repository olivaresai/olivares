// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseFromJSON(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`
	raw, err := responseFromJSON(strings.NewReader(body), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Errorf("result = %s", raw)
	}
}

func TestResponseFromJSONError(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"nope"}}`
	if _, err := responseFromJSON(strings.NewReader(body), 3); err == nil {
		t.Error("expected rpc error")
	}
}

func TestResponseFromSSE(t *testing.T) {
	// A heartbeat/comment, an unrelated event, then the matching response.
	stream := ": keep-alive\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/log\"}\n\n" +
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{\"found\":1}}\n\n"
	raw, err := responseFromSSE(strings.NewReader(stream), 9, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"found":1}` {
		t.Errorf("sse result = %s", raw)
	}
}

func TestResponseFromSSENoMatch(t *testing.T) {
	stream := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"
	if _, err := responseFromSSE(strings.NewReader(stream), 99, nil); err == nil {
		t.Error("expected error when the stream has no response for the id")
	}
}

// mcpHTTPHandler emulates a Streamable HTTP MCP server, answering in JSON or SSE.
func mcpHTTPHandler(sse bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.ID == nil { // notification
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "sess-http")
		}
		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *msg.ID, helperResult(msg.Method, msg.Params.Cursor))
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", resp)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	}
}

func TestIntrospectOverHTTPJSON(t *testing.T) {
	srv := httptest.NewServer(mcpHTTPHandler(false))
	defer srv.Close()
	cat, err := introspect(t.Context(), serverSpec{Name: "h", Transport: transportHTTP, URL: srv.URL})
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(cat.tools) != 2 || len(cat.resources) != 1 || len(cat.templates) != 1 || len(cat.prompts) != 1 {
		t.Errorf("catalog = %+v", cat)
	}
}

func TestIntrospectOverHTTPSSE(t *testing.T) {
	srv := httptest.NewServer(mcpHTTPHandler(true))
	defer srv.Close()
	cat, err := introspect(t.Context(), serverSpec{Name: "h", Transport: transportHTTP, URL: srv.URL})
	if err != nil {
		t.Fatalf("introspect (sse): %v", err)
	}
	if len(cat.tools) != 2 {
		t.Errorf("sse tools = %d, want 2", len(cat.tools))
	}
}

func TestIntrospectPartialCatalogOnListError(t *testing.T) {
	// Handshake succeeds advertising tools+resources, but resources/list returns a
	// JSON-RPC error. introspect must return a PARTIAL catalog (tools populated,
	// resources empty), not abort the whole server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if msg.Method == "resources/list" || msg.Method == "resources/templates/list" {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"boom"}}`, *msg.ID)
			return
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, *msg.ID, helperResult(msg.Method, msg.Params.Cursor))
	}))
	defer srv.Close()

	cat, err := introspect(t.Context(), serverSpec{Name: "p", Transport: transportHTTP, URL: srv.URL})
	if err != nil {
		t.Fatalf("partial introspect should not error: %v", err)
	}
	if len(cat.tools) != 2 {
		t.Errorf("tools should still be listed: %d", len(cat.tools))
	}
	if len(cat.resources) != 0 {
		t.Errorf("failed resources/list should yield empty resources, got %d", len(cat.resources))
	}
}

func TestHTTPSessionAndVersionHeaders(t *testing.T) {
	var sawSession, sawVersion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Session-Id") == "sess-http" {
			sawSession = true
		}
		if r.Header.Get("MCP-Protocol-Version") == "2025-06-18" {
			sawVersion = true
		}
		mcpHTTPHandler(false)(w, r)
	}))
	defer srv.Close()

	// Initialize sets session+version; the subsequent list must replay both.
	if _, err := introspect(t.Context(), serverSpec{Name: "h", Transport: transportHTTP, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if !sawSession {
		t.Error("session id not replayed on later requests")
	}
	if !sawVersion {
		t.Error("negotiated protocol version not sent on later requests")
	}
}

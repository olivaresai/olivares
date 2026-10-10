// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestMarshalOmitsIDForNotification(t *testing.T) {
	b, err := rpcRequest{Method: "notifications/initialized", isNotification: true}.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"id"`) {
		t.Errorf("notification must not carry an id: %s", b)
	}
	if !strings.Contains(string(b), `"jsonrpc":"2.0"`) {
		t.Errorf("missing jsonrpc version: %s", b)
	}
}

func TestRequestMarshalIncludesID(t *testing.T) {
	b, err := rpcRequest{ID: 7, Method: "tools/list"}.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"id":7`) {
		t.Errorf("request must carry its id: %s", b)
	}
}

// TestRequestMarshalMatchesHandWrittenEnvelope: moving the request envelope onto
// the go-sdk encoder changes no byte any transport sends. handWritten is the
// encoder it replaced, verbatim.
func TestRequestMarshalMatchesHandWrittenEnvelope(t *testing.T) {
	handWritten := func(r rpcRequest) ([]byte, error) {
		if r.isNotification {
			return json.Marshal(struct {
				JSONRPC string `json:"jsonrpc"`
				Method  string `json:"method"`
				Params  any    `json:"params,omitempty"`
			}{JSONRPC: jsonRPCVersion, Method: r.Method, Params: r.Params})
		}
		return json.Marshal(struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int64  `json:"id,omitempty"`
			Method  string `json:"method"`
			Params  any    `json:"params,omitempty"`
		}{JSONRPC: jsonRPCVersion, ID: r.ID, Method: r.Method, Params: r.Params})
	}
	var nilRaw json.RawMessage
	for _, r := range []rpcRequest{
		{ID: 1, Method: "server/discover"},
		{ID: 2, Method: "tools/list", Params: map[string]any{"cursor": "c<1>&"}},
		{ID: 3, Method: "tools/call", Params: json.RawMessage(`{"arguments":{"q":"\u003cb\u003e"},"name":"search"}`)},
		{ID: 1 << 40, Method: "subscriptions/listen", Params: struct {
			N []string `json:"notifications"`
		}{[]string{"a"}}},
		{ID: 5, Method: "ping", Params: nilRaw},
		{Method: "notifications/initialized", isNotification: true},
		{Method: "notifications/progress", Params: map[string]any{"progressToken": 4}, isNotification: true},
	} {
		want, werr := handWritten(r)
		got, gerr := r.marshal()
		if (werr != nil) != (gerr != nil) || string(got) != string(want) {
			t.Errorf("%s: go-sdk envelope %s (%v), hand-written %s (%v)", r.Method, got, gerr, want, werr)
		}
	}
	if _, err := (rpcRequest{ID: 6, Method: "tools/call", Params: json.RawMessage(`{"broken"`)}).marshal(); err == nil {
		t.Error("invalid params must not be sent")
	}
}

func TestIsResponseTo(t *testing.T) {
	id := int64(5)
	if !(rpcMessage{ID: &id}).isResponseTo(5) {
		t.Error("should match id 5")
	}
	if (rpcMessage{ID: &id, Method: "x"}).isResponseTo(5) {
		t.Error("a message with a method is a request/notification, not a response")
	}
	if (rpcMessage{}).isResponseTo(5) {
		t.Error("a message with no id is not a response")
	}
}

func TestRPCErrorString(t *testing.T) {
	e := &rpcError{Code: -32601, Message: "method not found"}
	if !strings.Contains(e.Error(), "method not found") {
		t.Errorf("error string = %q", e.Error())
	}
}

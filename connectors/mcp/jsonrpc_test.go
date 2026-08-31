// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
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

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

type credentialFixtureTransport struct {
	body          string
	authorization string
	contentType   string
	calls         *int
}

func TestMCPUpstreamCredentialMinimumLength(t *testing.T) {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	accepted := hex.EncodeToString(random[:])
	for _, length := range []int{0, 1, 3, 7, 8} {
		value := accepted[:length]
		for _, mode := range []string{"basic", "bearer", "opaque", "url"} {
			t.Run(fmt.Sprintf("%s_length_%d", mode, length), func(t *testing.T) {
				header, endpoint := value, "https://fixture.test/mcp"
				if mode == "basic" || mode == "url" {
					header = "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-user:"+value))
				}
				if mode == "bearer" {
					header = "Bearer " + value
				}
				if mode == "opaque" && length == 0 {
					header = " "
				} // explicitly provisioned empty credential, not absent auth
				var provider UpstreamCredentialProvider = &staticCredentialProvider{authHeader: header}
				if mode == "url" {
					endpoint = "https://fixture-user:" + value + "@fixture.test/mcp"
					provider = nil
				}
				calls := 0
				f := &mcpUpstreamForwarder{url: endpoint, credProv: provider, client: &http.Client{Transport: credentialFixtureTransport{body: `{"jsonrpc":"2.0","id":1,"result":{"description":"healthy prose with alphabet abc"}}`, authorization: header, calls: &calls}}}
				res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/list"})
				if length < 8 {
					if err == nil || calls != 0 || res.State != mcpc.DispatchNotSent || len(res.Result) != 0 {
						t.Fatalf("short credential transmitted: calls=%d state=%s", calls, res.State)
					}
					err = f.Listen(t.Context(), mcpc.SubscriptionListenRequest{Filter: mcpc.SubscriptionFilter{ToolsListChanged: true}}, func(mcpc.SubscriptionEvent) error { t.Fatal("short credential event emitted"); return nil })
					if err == nil || calls != 0 {
						t.Fatal("short stream credential transmitted")
					}
				} else if err != nil || calls != 1 || res.State != mcpc.DispatchCompleted {
					t.Fatal("eight-byte credential rejected healthy response")
				}
			})
		}
	}
}

func TestMCPBasicCredentialFormsAndShortPasswordPolicy(t *testing.T) {
	for _, password := range []string{"fixture-basic-secret", "8cZ9qL3m"} {
		t.Run(fmt.Sprintf("password_length_%d", len(password)), func(t *testing.T) {
			pair := "fixture-user:" + password
			header := "Basic " + base64.StdEncoding.EncodeToString([]byte(pair))
			var escaped strings.Builder
			for _, r := range password {
				fmt.Fprintf(&escaped, `\u%04x`, r)
			}
			for _, value := range []string{pair, password, escaped.String()} {
				encoded, _ := json.Marshal(value)
				if value == escaped.String() {
					encoded = []byte(`"` + value + `"`)
				}
				for _, prefix := range []string{`"result":{"echo":`, `"error":{"code":-32603,"message":"refused","data":`} {
					body := `{"jsonrpc":"2.0","id":1,` + prefix + string(encoded) + `}}`
					f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: header}, client: &http.Client{Transport: credentialFixtureTransport{body: body, authorization: header}}}
					res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/list"})
					if err == nil || res.State != mcpc.DispatchUnknown || len(res.Result) != 0 {
						t.Fatal("decoded Basic credential result/error released")
					}
				}
				stream := "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":1}}}\n\n" +
					`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":1},"echo":` + string(encoded) + "}}\n\n"
				f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: header}, client: &http.Client{Transport: credentialFixtureTransport{body: stream, authorization: header, contentType: "text/event-stream"}}}
				emitted := 0
				if err := f.Listen(t.Context(), mcpc.SubscriptionListenRequest{Filter: mcpc.SubscriptionFilter{ToolsListChanged: true}}, func(mcpc.SubscriptionEvent) error { emitted++; return nil }); err == nil || emitted != 0 {
					t.Fatal("decoded Basic credential stream emitted")
				}
			}
		})
	}
}

func TestMCPForwarderRefusesURLCredentialEcho(t *testing.T) {
	const pair = "fixture-user:fixture-url-secret"
	const endpoint = "https://fixture-user:fixture-url-secret@fixture.test/mcp"
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte(pair))
	for _, echo := range []string{header, pair, "fixture-url-secret", `\u0066ixture-url-secret`} {
		body := `{"jsonrpc":"2.0","id":1,"result":{"echo":"` + echo + `"}}`
		f := &mcpUpstreamForwarder{url: endpoint, client: &http.Client{Transport: credentialFixtureTransport{body: body, authorization: header}}}
		res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/list"})
		if err == nil || res.State != mcpc.DispatchUnknown || len(res.Result) != 0 {
			t.Fatal("URL credential echo released")
		}
		if strings.Contains(err.Error(), "fixture-url-secret") || strings.Contains(err.Error(), header) {
			t.Fatal("URL credential refusal disclosed credential")
		}
	}
}

func (s credentialFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.calls != nil {
		(*s.calls)++
	}
	if r.Header.Get("Authorization") != s.authorization {
		return nil, fmt.Errorf("fixture credential not injected")
	}
	contentType := s.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: &credentialByteReader{raw: s.body}, Request: r}, nil
}

func TestMCPSubscriptionRefusesCredentialBeforeEmit(t *testing.T) {
	for _, echo := range []string{`Bearer fixture-cut-secret`, `fixture-cut-secret`, `\u0066ixture-cut-secret`, `\u0066\u0069\u0078\u0074\u0075\u0072\u0065\u002d\u0063\u0075\u0074\u002d\u0073\u0065\u0063\u0072\u0065\u0074`} {
		body := ": ignored comment with an unmatched quote \"\n\n" + "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":1}}}\n\n" +
			`data: {"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{"_meta":{"io.modelcontextprotocol/subscriptionId":1},"echo":"` + echo + "\"}}\n\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"
		f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: "Bearer fixture-cut-secret"}, client: &http.Client{Transport: credentialFixtureTransport{body: body, authorization: "Bearer fixture-cut-secret", contentType: "text/event-stream"}}}
		emitted := 0
		err := f.Listen(t.Context(), mcpc.SubscriptionListenRequest{Filter: mcpc.SubscriptionFilter{ToolsListChanged: true}}, func(mcpc.SubscriptionEvent) error { emitted++; return nil })
		if err == nil || emitted != 0 {
			t.Fatalf("credential event accepted: emitted=%d error=%v", emitted, err != nil)
		}
		if strings.Contains(err.Error(), "fixture-cut-secret") {
			t.Fatal("stream refusal disclosed credential")
		}
	}
}

type credentialByteReader struct{ raw string }

func (r *credentialByteReader) Read(p []byte) (int, error) {
	if r.raw == "" {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.raw[0]
	r.raw = r.raw[1:]
	return 1, nil
}
func (*credentialByteReader) Close() error { return nil }

func TestMCPForwarderRefusesCredentialResultsAndErrors(t *testing.T) {
	const header = "Bearer fixture-cut-secret"
	for _, tc := range []struct{ name, body string }{
		{"full_header", `{"jsonrpc":"2.0","id":1,"result":{"description":"Bearer fixture-cut-secret"}}`},
		{"token_only", `{"jsonrpc":"2.0","id":1,"result":{"description":"fixture-cut-secret"}}`},
		{"unicode_escaping", `{"jsonrpc":"2.0","id":1,"result":{"description":"\u0066ixture-\u0063ut-secret"}}`},
		{"escaped_key", `{"jsonrpc":"2.0","id":1,"result":{"\u0066ixture-cut-secret":true}}`},
		{"error_data", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"refused","data":{"nested":["fixture-cut-secret"]}}}`},
		{"error_message", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Bearer fixture-cut-secret"}}`},
		{"escaped_error_data", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"refused","data":"\u0066ixture-cut-secret"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: header}, client: &http.Client{Transport: credentialFixtureTransport{body: tc.body, authorization: header}}}
			res, err := f.Forward(context.Background(), mcpc.UpstreamRequest{Method: "tools/list"})
			if err == nil || len(res.Result) != 0 || res.State != mcpc.DispatchUnknown {
				t.Fatalf("credential response released or incorrectly settled: state=%s error=%v", res.State, err != nil)
			}
			if strings.Contains(err.Error(), "fixture-cut-secret") {
				t.Fatal("refusal error disclosed credential")
			}
		})
	}
	for _, auth := range []string{header, ""} {
		body := `{"jsonrpc":"2.0","id":1,"result":{"description":"ordinary response"}}`
		if auth == "" {
			body = `{"jsonrpc":"2.0","id":1,"result":{"description":"fixture-cut-secret"}}`
		}
		f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: auth}, client: &http.Client{Transport: credentialFixtureTransport{body: body, authorization: auth}}}
		if res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/list"}); err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatal("non-disclosing response changed")
		}
	}
}

func TestManagedMCPRefusesEscapedCredentialSSE(t *testing.T) {
	for _, body := range []string{
		`data: {"jsonrpc":"2.0","id":1,"result":{"echo":"\u0066ixture-cut-secret"}}` + "\n\n",
		`data: {"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"refused","data":"\u0066ixture-cut-secret"}}` + "\n\n",
		`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"echo":"\u0066ixture-cut-secret"}}` + "\n\n",
	} {
		f := &mcpUpstreamForwarder{url: "https://fixture.test/mcp", credProv: &staticCredentialProvider{authHeader: "Bearer fixture-cut-secret"}, client: &http.Client{Transport: credentialFixtureTransport{body: body, authorization: "Bearer fixture-cut-secret", contentType: "text/event-stream"}}}
		enableManagedMCPForwarding(f)
		res, err := f.Forward(t.Context(), mcpc.UpstreamRequest{Method: "initialize", Subject: "agent:fixture"})
		if res.State != mcpc.DispatchUnknown || len(res.Result) != 0 || !errors.Is(err, mcpc.ErrUpstreamCredentialDisclosure) {
			t.Fatal("managed SSE credential refusal lost its classification")
		}
	}
}

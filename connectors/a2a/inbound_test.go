// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type inboundRouterFunc func(context.Context, InboundMessage) (InboundResult, error)

func (f inboundRouterFunc) RouteInboundA2A(ctx context.Context, message InboundMessage) (InboundResult, error) {
	return f(ctx, message)
}

type inboundLifecycleRouter struct {
	route  func(context.Context, InboundMessage) (InboundResult, error)
	get    func(context.Context, InboundTaskRequest) (InboundResult, error)
	cancel func(context.Context, InboundTaskRequest) (InboundResult, error)
}

func (r inboundLifecycleRouter) RouteInboundA2A(ctx context.Context, message InboundMessage) (InboundResult, error) {
	return r.route(ctx, message)
}

func (r inboundLifecycleRouter) GetInboundA2ATask(ctx context.Context, request InboundTaskRequest) (InboundResult, error) {
	return r.get(ctx, request)
}

func (r inboundLifecycleRouter) CancelInboundA2ATask(ctx context.Context, request InboundTaskRequest) (InboundResult, error) {
	return r.cancel(ctx, request)
}

func newInboundServerForTest(t *testing.T, jwks []byte, router InboundRouter) *InboundServer {
	t.Helper()
	server, err := NewInboundServer(InboundServerConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss},
		InterfaceTenant: "operator-route", Router: router, Clock: pushClock,
	})
	if err != nil {
		t.Fatalf("new inbound server: %v", err)
	}
	return server
}

func inboundRequest(token, id, body string) *http.Request {
	return inboundMethodRequest(token, id, methodSendMessage, body)
}

func inboundMethodRequest(token, id, method, body string) *http.Request {
	payload := `{"jsonrpc":"2.0","id":` + id + `,"method":` + strconv.Quote(method) + `,"params":` + body + `}`
	req := httptest.NewRequest(http.MethodPost, "https://olivares.example/a2a", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set("Content-Type", "application/json")
	return req
}

const inboundParams = `{"tenant":"operator-route","message":{"role":"ROLE_AGENT","messageId":"remote-message-1","contextId":"remote-context-1","parts":[{"text":"perform the approved work"},{"data":{"artifact":"ref:123"}}],"metadata":{"io.olivares.work_item_id":"untrusted-remote-value"}}}`

func TestInboundSendMessageRoutesOnlyAfterPeerAuthentication(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-k1", validPushClaims("inbound-jti-1"))
	called := 0
	server := newInboundServerForTest(t, jwks, inboundRouterFunc(func(_ context.Context, got InboundMessage) (InboundResult, error) {
		called++
		if got.PeerAuthority != pushIss || got.PeerSubject != "billing-agent" || got.Protocol != ProtocolVersion {
			t.Fatalf("verified peer projection = %+v", got)
		}
		if got.InterfaceTenant != "operator-route" || got.MessageID != "remote-message-1" || got.ContextID != "remote-context-1" || got.Role != roleAgent {
			t.Fatalf("message projection = %+v", got)
		}
		if len(got.Parts) != 2 || got.Parts[0].Kind != "text" ||
			got.Parts[0].Text != "perform the approved work" || len(got.Parts[0].Digest) != 64 ||
			got.Parts[1].Kind != "data" || string(got.Parts[1].Data) != `{"artifact":"ref:123"}` ||
			got.Parts[1].Reference != "a2a-part:"+got.Parts[1].Digest {
			t.Fatalf("parts = %+v", got.Parts)
		}
		if string(got.Metadata["io.olivares.work_item_id"]) != `"untrusted-remote-value"` {
			t.Fatalf("metadata = %+v", got.Metadata)
		}
		return InboundResult{ResultKind: "task", TaskID: "binding-task-1", ContextID: got.ContextID, State: TaskStateSubmitted}, nil
	}))

	w := httptest.NewRecorder()
	server.ServeHTTP(w, inboundRequest(token, `"rpc-1"`, inboundParams))
	if w.Code != http.StatusOK || called != 1 {
		t.Fatalf("status/calls = %d/%d, want 200/1; body=%s", w.Code, called, w.Body.String())
	}
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Result  struct {
			Task struct {
				ID     string `json:"id"`
				Status struct {
					State TaskState `json:"state"`
				} `json:"status"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.JSONRPC != "2.0" || response.ID != "rpc-1" || response.Result.Task.ID != "binding-task-1" || response.Result.Task.Status.State != TaskStateSubmitted {
		t.Fatalf("response = %+v", response)
	}
}

func TestParseInboundMessageSanitizesV101PartsBeforeKernelBoundary(t *testing.T) {
	message, err := parseInboundMessage(json.RawMessage(`{
		"tenant":"operator-route",
		"message":{
			"role":"ROLE_AGENT",
			"messageId":"remote-message-parts",
			"contextId":"remote-context-parts",
			"parts":[
				{"text":"line one\r\nline\u0001 two"},
				{"data":{"value":9}},
				{"raw":"cmF3LXJlc3VsdA==","filename":"result.txt","mediaType":"text/plain"},
				{"url":"https://files.example.test/report?credential=removed#removed"}
			]
		}
	}`), pushIss, "billing-agent", "operator-route")
	if err != nil {
		t.Fatalf("parse inbound v1.0.1 Parts: %v", err)
	}
	if len(message.Parts) != 4 || message.Parts[0].Kind != "text" ||
		message.Parts[0].Text != "line one\nline two" || len(message.Parts[0].Digest) != 64 ||
		message.Parts[1].Kind != "data" || string(message.Parts[1].Data) != `{"value":9}` ||
		message.Parts[1].Reference != "a2a-part:"+message.Parts[1].Digest ||
		message.Parts[2].Kind != "file" || message.Parts[2].Reference != "a2a-part:"+message.Parts[2].Digest ||
		message.Parts[3].Kind != "file" || message.Parts[3].Reference != "https://files.example.test/report" ||
		len(message.Parts[2].Digest) != 64 || len(message.Parts[3].Digest) != 64 {
		t.Fatalf("sanitized inbound v1.0.1 Parts = %+v", message.Parts)
	}
}

func TestInboundSendMessageAcknowledgesOnlyAfterDurableRoute(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-k2", validPushClaims("inbound-jti-2"))
	server := newInboundServerForTest(t, jwks, inboundRouterFunc(func(context.Context, InboundMessage) (InboundResult, error) {
		return InboundResult{}, errors.New("store unavailable")
	}))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, inboundRequest(token, `2`, inboundParams))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":-32603`) {
		t.Fatalf("failure leaked or was misclassified: %s", w.Body.String())
	}
}

func TestInboundSendMessageRejectsUntrustedOrMismatchedRequestsBeforeRoute(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-k3", validPushClaims("inbound-jti-3"))
	calls := 0
	server := newInboundServerForTest(t, jwks, inboundRouterFunc(func(context.Context, InboundMessage) (InboundResult, error) {
		calls++
		return InboundResult{ResultKind: "message", MessageID: "reply-1", Role: roleAgent}, nil
	}))

	badToken := inboundRequest("not-a-token", `"bad-token"`, inboundParams)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, badToken)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("untrusted status = %d, want 401", w.Code)
	}

	wrongTenant := strings.Replace(inboundParams, "operator-route", "peer-chosen-local-tenant", 1)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, inboundRequest(token, `"wrong-route"`, wrongTenant))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("routing mismatch status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if calls != 0 {
		t.Fatalf("router calls = %d, want 0", calls)
	}
}

func TestInboundSendMessageRequiresProtocolVersionAndRejectsTokenReplay(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-k4", validPushClaims("inbound-jti-4"))
	calls := 0
	server := newInboundServerForTest(t, jwks, inboundRouterFunc(func(context.Context, InboundMessage) (InboundResult, error) {
		calls++
		return InboundResult{ResultKind: "message", MessageID: "reply-1", ContextID: "remote-context-1", Role: roleAgent}, nil
	}))

	missingVersion := inboundRequest(token, `"missing-version"`, inboundParams)
	missingVersion.Header.Del("A2A-Version")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, missingVersion)
	if w.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("version status/calls = %d/%d, want 400/0", w.Code, calls)
	}

	w = httptest.NewRecorder()
	server.ServeHTTP(w, inboundRequest(token, `"first"`, inboundParams))
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("first status/calls = %d/%d, want 200/1; body=%s", w.Code, calls, w.Body.String())
	}
	w = httptest.NewRecorder()
	server.ServeHTTP(w, inboundRequest(token, `"replay"`, inboundParams))
	if w.Code != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("replay status/calls = %d/%d, want 401/1", w.Code, calls)
	}
}

func TestInboundDurableReplayAuthorityReceivesVerifiedClaims(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-k5", validPushClaims("inbound-durable-jti"))
	calls := 0
	server, err := NewInboundServer(InboundServerConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss},
		InterfaceTenant: "operator-route", Clock: pushClock, DurableReplay: true,
		Router: inboundRouterFunc(func(_ context.Context, message InboundMessage) (InboundResult, error) {
			calls++
			if message.ReplayID != "inbound-durable-jti" ||
				!message.ReplayExpiresAt.Equal(pushClock().Add(5*time.Minute)) {
				t.Fatalf("durable replay projection = %+v", message)
			}
			if calls > 1 {
				return InboundResult{}, ErrReplay
			}
			return InboundResult{
				ResultKind: "task", TaskID: "binding-task-1",
				ContextID: message.ContextID, State: TaskStateSubmitted,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("new durable inbound server: %v", err)
	}
	first := httptest.NewRecorder()
	server.ServeHTTP(first, inboundRequest(token, `"first"`, inboundParams))
	second := httptest.NewRecorder()
	server.ServeHTTP(second, inboundRequest(token, `"second"`, inboundParams))
	if first.Code != http.StatusOK || second.Code != http.StatusUnauthorized || calls != 2 {
		t.Fatalf("durable replay statuses/calls = %d/%d/%d, want 200/401/2",
			first.Code, second.Code, calls)
	}
}

func TestInboundTaskLifecycleUsesAuthenticatedDurableRouter(t *testing.T) {
	getToken, jwks := mintPushJWT(t, jose.ES256, "inbound-life-k1", validPushClaims("inbound-get-jti"))
	cancelToken := getToken
	getCalls, cancelCalls := 0, 0
	router := inboundLifecycleRouter{
		route: func(context.Context, InboundMessage) (InboundResult, error) {
			return InboundResult{}, errors.New("unexpected send")
		},
		get: func(_ context.Context, request InboundTaskRequest) (InboundResult, error) {
			getCalls++
			if request.PeerAuthority != pushIss || request.PeerSubject != "billing-agent" ||
				request.InterfaceTenant != "operator-route" || request.TaskID != "task-1" ||
				request.HistoryLength != 7 || request.ReplayID != "inbound-get-jti" ||
				request.ReplayExpiresAt.IsZero() {
				t.Fatalf("get request = %+v", request)
			}
			return InboundResult{ResultKind: "task", TaskID: request.TaskID, ContextID: "ctx-1", State: TaskStateWorking}, nil
		},
		cancel: func(_ context.Context, request InboundTaskRequest) (InboundResult, error) {
			cancelCalls++
			if request.PeerAuthority != pushIss || request.TaskID != "task-1" || request.ReplayID != "inbound-get-jti" {
				t.Fatalf("cancel request = %+v", request)
			}
			return InboundResult{ResultKind: "task", TaskID: request.TaskID, ContextID: "ctx-1", State: TaskStateCanceled}, nil
		},
	}
	server, err := NewInboundServer(InboundServerConfig{
		Audience: pushAud, IssuerJWKS: jwks, AllowedIssuers: []string{pushIss},
		InterfaceTenant: "operator-route", Router: router, DurableReplay: true, Clock: pushClock,
	})
	if err != nil {
		t.Fatalf("new inbound lifecycle server: %v", err)
	}
	get := httptest.NewRecorder()
	server.ServeHTTP(get, inboundMethodRequest(getToken, `"get"`, methodGetTask,
		`{"tenant":"operator-route","id":"task-1","historyLength":7}`))
	if get.Code != http.StatusOK || getCalls != 1 || !strings.Contains(get.Body.String(), `"state":"TASK_STATE_WORKING"`) ||
		strings.Contains(get.Body.String(), `"task":`) {
		t.Fatalf("get status/calls/body = %d/%d/%s", get.Code, getCalls, get.Body.String())
	}
	cancel := httptest.NewRecorder()
	server.ServeHTTP(cancel, inboundMethodRequest(cancelToken, `"cancel"`, methodCancelTask,
		`{"tenant":"operator-route","id":"task-1"}`))
	if cancel.Code != http.StatusOK || cancelCalls != 1 || !strings.Contains(cancel.Body.String(), `"state":"TASK_STATE_CANCELED"`) ||
		strings.Contains(cancel.Body.String(), `"task":`) {
		t.Fatalf("cancel status/calls/body = %d/%d/%s", cancel.Code, cancelCalls, cancel.Body.String())
	}
}

func TestInboundTaskLifecycleStaysUnavailableWithoutDurableTaskRouter(t *testing.T) {
	token, jwks := mintPushJWT(t, jose.ES256, "inbound-life-k2", validPushClaims("inbound-life-no-router"))
	server := newInboundServerForTest(t, jwks, inboundRouterFunc(func(context.Context, InboundMessage) (InboundResult, error) {
		return InboundResult{}, errors.New("unexpected send")
	}))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, inboundMethodRequest(token, `"get"`, methodGetTask,
		`{"tenant":"operator-route","id":"task-1"}`))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":-32601`) {
		t.Fatalf("unwired task lifecycle = %d %s", w.Code, w.Body.String())
	}
}

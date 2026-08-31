// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type memorySubscriptionLedger struct {
	mu      sync.Mutex
	events  map[SubscriptionRoute][]SubscriptionStoredEvent
	appends int
}

func newMemorySubscriptionLedger() *memorySubscriptionLedger {
	return &memorySubscriptionLedger{events: map[SubscriptionRoute][]SubscriptionStoredEvent{}}
}

func (l *memorySubscriptionLedger) CatchUp(
	_ context.Context,
	req SubscriptionCatchUpRequest,
) (SubscriptionCatchUpPage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events[req.Route]
	start := 0
	if req.Cursor != "" {
		start = -1
		for i := range events {
			if events[i].Cursor == req.Cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return SubscriptionCatchUpPage{}, ErrSubscriptionCursorInvalid
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > len(events)-start {
		limit = len(events) - start
	}
	pageEvents := append([]SubscriptionStoredEvent(nil), events[start:start+limit]...)
	next := req.Cursor
	if len(pageEvents) > 0 {
		next = pageEvents[len(pageEvents)-1].Cursor
	}
	return SubscriptionCatchUpPage{
		Events: pageEvents, NextCursor: next, HasMore: start+limit < len(events),
	}, nil
}

func (l *memorySubscriptionLedger) Append(
	_ context.Context,
	req SubscriptionAppendRequest,
) (SubscriptionStoredEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events[req.Route]
	head := ""
	if len(events) > 0 {
		head = events[len(events)-1].Cursor
	}
	if head != req.ExpectedCursor {
		return SubscriptionStoredEvent{}, ErrSubscriptionCursorConflict
	}
	l.appends++
	stored := SubscriptionStoredEvent{
		Cursor: "cursor-" + strconv.Itoa(l.appends),
		Method: req.Event.Method,
		Params: append(json.RawMessage(nil), req.Event.Params...),
	}
	l.events[req.Route] = append(events, stored)
	return stored, nil
}

func (l *memorySubscriptionLedger) snapshot() []SubscriptionStoredEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, events := range l.events {
		return append([]SubscriptionStoredEvent(nil), events...)
	}
	return nil
}

type fakeSubscriptionUpstream struct {
	events []SubscriptionEvent
	err    error
	called int
	last   SubscriptionListenRequest
}

func (u *fakeSubscriptionUpstream) Listen(
	_ context.Context,
	req SubscriptionListenRequest,
	emit func(SubscriptionEvent) error,
) error {
	u.called++
	u.last = req
	for _, event := range u.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return u.err
}

func newSubscriptionRS(
	t *testing.T,
	jwks []byte,
	upstream SubscriptionUpstream,
	ledger SubscriptionLedger,
	auditor GateAuditor,
) *ResourceServer {
	t.Helper()
	toolset, err := NewToolset([]ToolPolicy{{Name: "search", RequiredScope: "tools:read"}})
	if err != nil {
		t.Fatalf("toolset: %v", err)
	}
	rs, err := NewResourceServer(ResourceServerConfig{
		Resource: rsResource, AuthorizationServers: []string{rsIssuer},
		Issuer: rsIssuer, IssuerJWKS: jwks, Toolset: toolset,
		Upstream: &fakeUpstream{}, SubscriptionUpstream: upstream,
		SubscriptionLedger: ledger, Auditor: auditor, Clock: rsClock,
		Tenant: "tenant-relay", RevisionMode: revisionModeRCStrict,
	})
	if err != nil {
		t.Fatalf("new subscription rs: %v", err)
	}
	return rs
}

func subscriptionRequest(token, cursor string) *http.Request {
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`
	req := nextReq(token, methodSubscriptionsListen, "", body)
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	return req
}

func TestMCPListenTraversesPEPAndPersistsCursor(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	ledger := newMemorySubscriptionLedger()
	firstAudit := &capturingAuditor{}
	firstUpstream := &fakeSubscriptionUpstream{
		events: []SubscriptionEvent{
			{Method: "notifications/tools/list_changed", Params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/subscriptionId":"upstream-1"},"change":1}`)},
			{Method: "notifications/tools/list_changed", Params: json.RawMessage(`{"_meta":{"io.modelcontextprotocol/subscriptionId":"upstream-1"},"change":2}`)},
		},
		err: ErrSubscriptionRelayTruncated,
	}
	first := httptest.NewRecorder()
	newSubscriptionRS(t, jwks, firstUpstream, ledger, firstAudit).
		ServeHTTP(first, subscriptionRequest(token, ""))

	if first.Code != http.StatusOK || !strings.HasPrefix(first.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("listen status/content-type = %d/%q; body=%s", first.Code, first.Header().Get("Content-Type"), first.Body.String())
	}
	if firstUpstream.called != 1 || firstUpstream.last.Route.Tenant != "tenant-relay" ||
		firstUpstream.last.Route.Subject == "" || !firstUpstream.last.Filter.ToolsListChanged {
		t.Fatalf("governed upstream request = %+v (called %d)", firstUpstream.last, firstUpstream.called)
	}
	if got := ledger.snapshot(); len(got) != 2 || got[0].Cursor != "cursor-1" || got[1].Cursor != "cursor-2" {
		t.Fatalf("durable ledger = %+v, want two ordered cursor rows", got)
	}
	if strings.Index(first.Body.String(), notificationSubscriptionsAcknowledged) >
		strings.Index(first.Body.String(), "notifications/tools/list_changed") {
		t.Fatalf("ack was not first on stream: %s", first.Body.String())
	}
	if strings.Contains(first.Body.String(), "upstream-1") || !strings.Contains(first.Body.String(), `"io.modelcontextprotocol/subscriptionId":7`) {
		t.Fatalf("upstream subscription id escaped or downstream id missing: %s", first.Body.String())
	}
	if strings.Contains(first.Body.String(), `"result"`) {
		t.Fatalf("truncated upstream was reported as graceful teardown: %s", first.Body.String())
	}
	if len(firstAudit.decisions) < 2 || !firstAudit.decisions[0].Allowed || firstAudit.decisions[0].Tool != methodSubscriptionsListen ||
		!strings.Contains(firstAudit.decisions[len(firstAudit.decisions)-1].Reason, "truncated") {
		t.Fatalf("listen PEP/audit decisions = %+v", firstAudit.decisions)
	}

	// A fresh ResourceServer represents a process restart. It holds no relay
	// cache, yet the same durable ledger catches up cursor-2 after the client's
	// last acknowledged cursor-1 before opening the new upstream stream.
	restartAudit := &capturingAuditor{}
	restartUpstream := &fakeSubscriptionUpstream{}
	restarted := httptest.NewRecorder()
	newSubscriptionRS(t, jwks, restartUpstream, ledger, restartAudit).
		ServeHTTP(restarted, subscriptionRequest(token, "cursor-1"))
	if restarted.Code != http.StatusOK || restartUpstream.called != 1 {
		t.Fatalf("restart listen = status %d upstream calls %d body=%s", restarted.Code, restartUpstream.called, restarted.Body.String())
	}
	body := restarted.Body.String()
	if !strings.Contains(body, "id: cursor-2") || strings.Contains(body, "id: cursor-1") {
		t.Fatalf("restart catch-up did not resume strictly after cursor-1: %s", body)
	}
	if strings.Index(body, "id: cursor-2") > strings.Index(body, `"result"`) {
		t.Fatalf("restart emitted teardown before catch-up: %s", body)
	}
}

func TestMCPListenDenyClosedWithoutDurablePair(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	cases := []struct {
		name   string
		up     SubscriptionUpstream
		ledger SubscriptionLedger
	}{
		{name: "neither"},
		{name: "upstream only", up: &fakeSubscriptionUpstream{}},
		{name: "ledger only", ledger: newMemorySubscriptionLedger()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			newSubscriptionRS(t, jwks, tc.up, tc.ledger, &capturingAuditor{}).
				ServeHTTP(w, subscriptionRequest(token, ""))
			if w.Code != http.StatusServiceUnavailable || rpcErrorCode(t, w.Body.String()) != rpcEvidenceUnavailable {
				t.Fatalf("unwired relay = status %d body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMCPListenRejectsForeignCursorBeforeUpstream(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	upstream := &fakeSubscriptionUpstream{}
	w := httptest.NewRecorder()
	newSubscriptionRS(t, jwks, upstream, newMemorySubscriptionLedger(), &capturingAuditor{}).
		ServeHTTP(w, subscriptionRequest(token, "cursor-from-another-stream"))
	if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcInvalidParams {
		t.Fatalf("foreign cursor = status %d body %s", w.Code, w.Body.String())
	}
	if upstream.called != 0 {
		t.Fatal("foreign cursor reached subscription upstream")
	}
}

func TestMCPListenResourceFilterRequiresResourceScope(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	upstream := &fakeSubscriptionUpstream{}
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"resourceSubscriptions":["file:///governed"]}}}`
	w := httptest.NewRecorder()
	newSubscriptionRS(t, jwks, upstream, newMemorySubscriptionLedger(), &capturingAuditor{}).
		ServeHTTP(w, nextReq(token, methodSubscriptionsListen, "", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("resource subscription without scope = status %d body %s", w.Code, w.Body.String())
	}
	if upstream.called != 0 {
		t.Fatal("scope-denied subscription reached upstream")
	}
}

func TestMCPListenCursorHeaderAndMetaMustAgree(t *testing.T) {
	token, jwks := mintAccessToken(t, "k1", rsResource, "tools:read", validExp())
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},"_meta":{"ai.olivares/subscriptionCursor":"cursor-2"}}}`
	req := nextReq(token, methodSubscriptionsListen, "", body)
	req.Header.Set("Last-Event-ID", "cursor-1")
	w := httptest.NewRecorder()
	newSubscriptionRS(t, jwks, &fakeSubscriptionUpstream{}, newMemorySubscriptionLedger(), &capturingAuditor{}).
		ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || rpcErrorCode(t, w.Body.String()) != rpcInvalidParams {
		t.Fatalf("mismatched cursor spellings = status %d body %s", w.Code, w.Body.String())
	}
}

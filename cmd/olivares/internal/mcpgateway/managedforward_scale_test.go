// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// scaleUpstream is a Streamable HTTP MCP upstream that gives every initialize its own
// session and counts the tools/list requests in flight, overall and per upstream session.
type scaleUpstream struct {
	srv *httptest.Server

	mu          sync.Mutex
	sessions    int
	inflight    int
	maxInflight int
	perSession  map[string]int
	maxPerSess  int
	// hold runs inside every tools/list while it counts as in flight.
	hold func(*scaleUpstream)
	// failInitialize answers every initialize with HTTP 500; set it with setFailInitialize.
	failInitialize bool
	// requests counts every request that reaches the upstream.
	requests int
	// barrierBroken is set by the first waitForInflight that gives up, so a serialized
	// forwarder costs one timeout and not one per request.
	barrierBroken bool
}

func newScaleUpstream(t *testing.T) *scaleUpstream {
	t.Helper()
	u := &scaleUpstream{perSession: map[string]int{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var method string
		_ = json.Unmarshal(req["method"], &method)
		u.mu.Lock()
		u.requests++
		failInitialize := u.failInitialize
		u.mu.Unlock()
		switch method {
		case "initialize":
			if failInitialize {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			u.mu.Lock()
			u.sessions++
			id := fmt.Sprintf("upstream-session-%d", u.sessions)
			u.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", id)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}}`)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			sid := r.Header.Get("Mcp-Session-Id")
			u.mu.Lock()
			u.inflight++
			u.perSession[sid]++
			u.maxInflight = max(u.maxInflight, u.inflight)
			u.maxPerSess = max(u.maxPerSess, u.perSession[sid])
			u.mu.Unlock()
			if u.hold != nil {
				u.hold(u)
			}
			u.mu.Lock()
			u.inflight--
			u.perSession[sid]--
			u.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *scaleUpstream) setFailInitialize(fail bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failInitialize = fail
}

// initializes is how many initialize requests the upstream has answered.
func (u *scaleUpstream) initializes() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sessions
}

func (u *scaleUpstream) current() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inflight
}

// waitForInflight holds a request until want requests are in flight together, or gives up
// after limit: a serialized forwarder never gets there.
func (u *scaleUpstream) waitForInflight(want int, limit time.Duration) {
	for deadline := time.Now().Add(limit); u.current() < want; time.Sleep(time.Millisecond) {
		u.mu.Lock()
		broken := u.barrierBroken
		if !broken && time.Now().After(deadline) {
			u.barrierBroken, broken = true, true
		}
		u.mu.Unlock()
		if broken {
			return
		}
	}
}

func (u *scaleUpstream) stats() (maxInflight, maxPerSession, distinctSessions, requests int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.maxInflight, u.maxPerSess, len(u.perSession), u.requests
}

func scalePrincipal(method string, i int) mcpc.UpstreamRequest {
	return mcpc.UpstreamRequest{Method: method, Subject: "agent:fleet", ClientID: fmt.Sprintf("client-%04d", i),
		Scopes: []string{"tools:call"}, Params: []byte(`{}`)}
}

// handshake runs the two calls every MCP client makes first.
func handshake(t *testing.T, f *UpstreamForwarder, i int) {
	t.Helper()
	for _, method := range []string{"initialize", "notifications/initialized"} {
		if res, err := f.Forward(context.Background(), scalePrincipal(method, i)); err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("client %d %s: state=%s err=%v", i, method, res.State, err)
		}
	}
}

func newScaleForwarder(u *scaleUpstream) *UpstreamForwarder {
	f := &UpstreamForwarder{URL: u.srv.URL, Client: u.srv.Client()}
	EnableManagedForwarding(f)
	return f
}

// The ad shows 600 sessions: 600 distinct clients of one managed server must each keep a session.
func TestManagedMCPForwarderServes600Principals(t *testing.T) {
	const clients = 600
	u := newScaleUpstream(t)
	f := newScaleForwarder(u)
	for i := range clients {
		handshake(t, f, i)
	}
	for i := range clients {
		if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", i)); err != nil || res.State != mcpc.DispatchCompleted {
			t.Fatalf("client %d tools/list after %d handshakes: state=%s err=%v", i, clients, res.State, err)
		}
	}
	if _, _, sessions, _ := u.stats(); sessions != clients || len(f.sessions) != clients {
		t.Fatalf("%d upstream sessions used and %d kept for %d clients, want one each", sessions, len(f.sessions), clients)
	}
}

// One slow upstream call must not hold up the other clients of the same managed server.
func TestManagedMCPForwarderOverlapsCallsOfDifferentPrincipals(t *testing.T) {
	const clients, together = 16, 8
	u := newScaleUpstream(t)
	u.hold = func(u *scaleUpstream) { u.waitForInflight(together, 2*time.Second) }
	f := newScaleForwarder(u)
	for i := range clients {
		handshake(t, f, i)
	}
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", i)); err != nil || res.State != mcpc.DispatchCompleted {
				t.Errorf("client %d: state=%s err=%v", i, res.State, err)
			}
		}()
	}
	wg.Wait()
	if inflight, _, _, _ := u.stats(); inflight < together {
		t.Fatalf("at most %d tools/list in flight at once across %d clients, want %d: the forwarder serializes its clients", inflight, clients, together)
	}
}

// One client's calls stay in order: a session's handshake state is not shared between its own requests.
func TestManagedMCPForwarderKeepsOnePrincipalSequential(t *testing.T) {
	u := newScaleUpstream(t)
	u.hold = func(*scaleUpstream) { time.Sleep(5 * time.Millisecond) }
	f := newScaleForwarder(u)
	handshake(t, f, 0)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", 0)); err != nil || res.State != mcpc.DispatchCompleted {
				t.Errorf("state=%s err=%v", res.State, err)
			}
		}()
	}
	wg.Wait()
	if _, perSession, _, _ := u.stats(); perSession != 1 {
		t.Fatalf("%d concurrent requests on one upstream session, want 1", perSession)
	}
}

// An initialize the upstream refused leaves no session a later call could use: the call is refused
// before anything is sent.
func TestManagedMCPForwarderRefusesCallsAfterAFailedInitialize(t *testing.T) {
	u := newScaleUpstream(t)
	u.setFailInitialize(true)
	f := newScaleForwarder(u)
	if res, err := f.Forward(context.Background(), scalePrincipal("initialize", 0)); err == nil || res.State != mcpc.DispatchUnknown {
		t.Fatalf("refused initialize: state=%s err=%v", res.State, err)
	}
	_, _, _, sent := u.stats()
	for _, method := range []string{"notifications/initialized", "tools/list"} {
		if res, err := f.Forward(context.Background(), scalePrincipal(method, 0)); err != errManagedInitializeRequired || res.State != mcpc.DispatchNotSent {
			t.Fatalf("%s after a failed initialize: state=%s err=%v", method, res.State, err)
		}
	}
	if _, _, _, after := u.stats(); after != sent {
		t.Fatalf("%d requests reached the upstream after the failed initialize", after-sent)
	}
}

// A cold start of many clients at once, each re-initializing and calling: every call completes.
// Under -race this also covers the table's locking.
func TestManagedMCPForwarderServesConcurrentHandshakes(t *testing.T) {
	const clients = 64
	f := newScaleForwarder(newScaleUpstream(t))
	var wg sync.WaitGroup
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				for _, method := range []string{"initialize", "notifications/initialized", "tools/list", "tools/list"} {
					if res, err := f.Forward(context.Background(), scalePrincipal(method, i)); err != nil || res.State != mcpc.DispatchCompleted {
						t.Errorf("client %d %s: state=%s err=%v", i, method, res.State, err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

// A client whose re-initialize the upstream refuses is not left on its old session: it must initialize again.
func TestManagedMCPForwarderRefusesCallsAfterAFailedReinitialize(t *testing.T) {
	u := newScaleUpstream(t)
	f := newScaleForwarder(u)
	handshake(t, f, 0)
	u.setFailInitialize(true)
	if res, err := f.Forward(context.Background(), scalePrincipal("initialize", 0)); err == nil || res.State != mcpc.DispatchUnknown {
		t.Fatalf("refused re-initialize: state=%s err=%v", res.State, err)
	}
	if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", 0)); err != errManagedInitializeRequired || res.State != mcpc.DispatchNotSent {
		t.Fatalf("tools/list after a refused re-initialize: state=%s err=%v", res.State, err)
	}
	u.setFailInitialize(false)
	handshake(t, f, 0)
	if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", 0)); err != nil || res.State != mcpc.DispatchCompleted {
		t.Fatalf("tools/list after initializing again: state=%s err=%v", res.State, err)
	}
}

// An initialize waits for the same client's call in flight, as every call of one client does: the upstream
// sees the initialize only after that call has finished.
func TestManagedMCPForwarderWaitsForTheClientsCallBeforeReinitializing(t *testing.T) {
	u := newScaleUpstream(t)
	release := make(chan struct{})
	u.hold = func(*scaleUpstream) { <-release }
	f := newScaleForwarder(u)
	handshake(t, f, 0)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if res, err := f.Forward(context.Background(), scalePrincipal("tools/list", 0)); err != nil || res.State != mcpc.DispatchCompleted {
			t.Errorf("tools/list: state=%s err=%v", res.State, err)
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); u.current() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the tools/list never reached the upstream")
		}
	}
	before := u.initializes()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if res, err := f.Forward(context.Background(), scalePrincipal("initialize", 0)); err != nil || res.State != mcpc.DispatchCompleted {
			t.Errorf("initialize: state=%s err=%v", res.State, err)
		}
	}()
	time.Sleep(150 * time.Millisecond)
	if got := u.initializes(); got != before {
		t.Errorf("the initialize reached the upstream while the client's tools/list was in flight")
	}
	close(release)
	wg.Wait()
	if got := u.initializes(); got != before+1 {
		t.Fatalf("%d initializes answered after the call finished, want %d", got, before+1)
	}
}

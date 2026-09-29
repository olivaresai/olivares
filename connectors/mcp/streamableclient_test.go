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
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// TestStreamableTransportsKeepTheDefaultTransport: both HTTP transports send
// through http.DefaultTransport, so proxy settings from the environment,
// connection reuse, HTTP/2 and TLS are the ones they had; only redirects are
// refused, by the transport wrapper and by the client's redirect policy alike.
func TestStreamableTransportsKeepTheDefaultTransport(t *testing.T) {
	stable, err := newHTTPTransport(serverSpec{Name: "d", URL: "http://127.0.0.1/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	stateless, err := newStatelessHTTPTransport(serverSpec{Name: "d", URL: "http://127.0.0.1/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*http.Client{"stable": stable.client, "stateless": stateless.client} {
		rr, ok := c.Transport.(refuseRedirects)
		if !ok || rr.base != http.DefaultTransport {
			t.Errorf("%s: transport = %#v, want the redirect refusal over http.DefaultTransport", name, c.Transport)
		}
		if c.CheckRedirect == nil || !errors.Is(c.CheckRedirect(nil, nil), ErrStreamableRedirectRefused) {
			t.Errorf("%s: the redirect policy does not refuse", name)
		}
		if c.Timeout != 0 || c.Jar != nil {
			t.Errorf("%s: client timeout %v / cookie jar %v, want none as before", name, c.Timeout, c.Jar)
		}
	}
}

// TestStreamableClientSendsThroughTheBaseProxy: the redirect refusal leaves the
// proxy to the base transport. With a base that uses a proxy, every request of
// both transports goes through it, and a redirect the proxy relays is still
// refused after exactly one proxied request.
func TestStreamableClientSendsThroughTheBaseProxy(t *testing.T) {
	const endpoint = "http://mcp.proxied.example/mcp"
	var redirect atomic.Bool
	proxy := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		// A forward proxy receives the absolute URI of the endpoint.
		if r.URL.String() != endpoint {
			t.Errorf("the proxy received %q, want %q", r.URL, endpoint)
		}
		if redirect.Load() {
			w.Header().Set("Location", "http://"+redirectCanary+".example/mcp")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		mcpHTTPHandler(false)(w, r)
	})
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(proxyURL)
	t.Cleanup(base.CloseIdleConnections)

	stable, err := newHTTPTransport(serverSpec{Name: "p", URL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	stable.client = newStreamableClient(base)
	stateless, err := newStatelessHTTPTransport(serverSpec{Name: "p", URL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	stateless.client = newStreamableClient(base)

	for _, tr := range []struct {
		name string
		t    transport
	}{{"stable", stable}, {"stateless", stateless}} {
		t.Run(tr.name, func(t *testing.T) {
			redirect.Store(false)
			before := proxy.requests.Load()
			if _, err := tr.t.roundTrip(t.Context(), rpcRequest{Method: "tools/list"}); err != nil {
				t.Fatalf("request through the proxy: %v", err)
			}
			if n := proxy.requests.Load() - before; n != 1 {
				t.Errorf("the proxy relayed %d requests, want 1", n)
			}

			redirect.Store(true)
			before = proxy.requests.Load()
			_, err := tr.t.roundTrip(t.Context(), rpcRequest{Method: "tools/list"})
			if !errors.Is(err, ErrStreamableRedirectRefused) {
				t.Fatalf("error = %v, want ErrStreamableRedirectRefused", err)
			}
			if n := proxy.requests.Load() - before; n != 1 {
				t.Errorf("the proxy relayed %d requests for a refused redirect, want 1", n)
			}
		})
	}
}

// TestStreamableTransportsReuseTheConnection: sequential requests to a direct
// loopback endpoint share one pooled connection, on both transports.
func TestStreamableTransportsReuseTheConnection(t *testing.T) {
	type sequence struct {
		request func(context.Context) error
		notify  func(context.Context) error
	}
	builds := []struct {
		name  string
		build func(endpoint string) (sequence, error)
	}{
		{"stable", func(endpoint string) (sequence, error) {
			tr, err := newHTTPTransport(serverSpec{Name: "r", URL: endpoint})
			if err != nil {
				return sequence{}, err
			}
			return sequence{
				request: func(ctx context.Context) error {
					_, err := tr.roundTrip(ctx, rpcRequest{Method: "tools/list"})
					return err
				},
				notify: func(ctx context.Context) error {
					return tr.notify(ctx, "notifications/initialized", nil)
				},
			}, nil
		}},
		{"stateless", func(endpoint string) (sequence, error) {
			tr, err := newStatelessHTTPTransport(serverSpec{Name: "r", URL: endpoint})
			if err != nil {
				return sequence{}, err
			}
			return sequence{
				request: func(ctx context.Context) error {
					_, err := tr.roundTrip(ctx, rpcRequest{Method: "tools/list"})
					return err
				},
				notify: func(ctx context.Context) error {
					return tr.notify(ctx, "notifications/initialized", nil)
				},
			}, nil
		}},
	}
	for _, b := range builds {
		t.Run(b.name, func(t *testing.T) {
			srv := newCountingServer(t, mcpHTTPHandler(false))
			seq, err := b.build(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				if err := seq.request(t.Context()); err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
			}
			if err := seq.notify(t.Context()); err != nil {
				t.Fatalf("notification: %v", err)
			}
			if n := srv.requests.Load(); n != 4 {
				t.Errorf("the endpoint answered %d requests, want 4", n)
			}
			if n := srv.conns.Load(); n != 1 {
				t.Errorf("4 sequential requests opened %d connections, want 1 reused", n)
			}
		})
	}
}

// TestStreamableListenStreamsEventsAsTheyArrive: a subscriptions/listen stream
// reaches the caller event by event: the server sends the next message only once
// the caller has received the previous one.
func TestStreamableListenStreamsEventsAsTheyArrive(t *testing.T) {
	received := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID int64 `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"%s\",\"params\":{\"_meta\":{\"%s\":\"%d\"}}}\n\n",
			notificationSubscriptionsAcknowledged, metaSubscriptionID, body.ID)
		w.(http.Flusher).Flush()
		select {
		case <-received:
		case <-time.After(streamableWait):
			t.Error("the acknowledgment did not reach the caller while the stream was open")
			return
		}
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", body.ID)
	}))
	defer srv.Close()

	tr, err := newStatelessHTTPTransport(serverSpec{Name: "l", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = newStatelessClient(tr).Listen(t.Context(), subscriptionFilter{ToolsListChanged: true}, func(e subscriptionEvent) {
		if e.Method == notificationSubscriptionsAcknowledged {
			close(received)
		}
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
}

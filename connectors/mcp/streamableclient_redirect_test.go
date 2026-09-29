// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// redirectCanary marks the bytes a server or a configuration controls in these
// tests: a Location, a query, a user name, a host. The error text the transports
// return must never contain it.
const redirectCanary = "canary-7f3c9e"

// streamableWait bounds every wait in these tests, so a regression fails instead
// of hanging the suite.
const streamableWait = 10 * time.Second

// followedRedirects are the statuses net/http's Client follows when the response
// has a Location.
var followedRedirects = []int{
	http.StatusMovedPermanently,
	http.StatusFound,
	http.StatusSeeOther,
	http.StatusTemporaryRedirect,
	http.StatusPermanentRedirect,
}

// countingServer counts the requests it answers and the connections it accepts,
// so a test can show that a request was never sent and a host never dialed.
type countingServer struct {
	*httptest.Server
	requests atomic.Int64
	conns    atomic.Int64
}

func newCountingServer(t *testing.T, h http.HandlerFunc) *countingServer {
	t.Helper()
	cs := &countingServer{}
	cs.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.requests.Add(1)
		h(w, r)
	}))
	cs.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			cs.conns.Add(1)
		}
	}
	cs.Start()
	t.Cleanup(cs.Close)
	return cs
}

// streamableOp is one request path of the Streamable HTTP transports. prefix
// starts the text of the error it returns when the request itself fails, and
// statusErr is the text of its existing error for a non-2xx status.
type streamableOp struct {
	name      string
	prefix    string
	statusErr func(status int) string
	run       func(ctx context.Context, endpoint string) error
}

func streamableOps() []streamableOp {
	requestStatus := func(status int) string { return fmt.Sprintf("mcp: http %d", status) }
	notifyStatus := func(status int) string {
		return fmt.Sprintf("mcp: http %d %s", status, http.StatusText(status))
	}
	return []streamableOp{
		{"stable/request", "mcp: http post", requestStatus, func(ctx context.Context, endpoint string) error {
			tr, err := newHTTPTransport(serverSpec{Name: "s", URL: endpoint})
			if err != nil {
				return err
			}
			_, err = tr.roundTrip(ctx, rpcRequest{Method: "tools/list"})
			return err
		}},
		{"stable/notification", "mcp: http post", notifyStatus, func(ctx context.Context, endpoint string) error {
			tr, err := newHTTPTransport(serverSpec{Name: "s", URL: endpoint})
			if err != nil {
				return err
			}
			return tr.notify(ctx, "notifications/initialized", nil)
		}},
		{"stateless/request", "mcp: http post", requestStatus, func(ctx context.Context, endpoint string) error {
			tr, err := newStatelessHTTPTransport(serverSpec{Name: "s", URL: endpoint})
			if err != nil {
				return err
			}
			_, err = tr.roundTrip(ctx, rpcRequest{Method: "tools/list"})
			return err
		}},
		{"stateless/notification", "mcp: http post", notifyStatus, func(ctx context.Context, endpoint string) error {
			tr, err := newStatelessHTTPTransport(serverSpec{Name: "s", URL: endpoint})
			if err != nil {
				return err
			}
			return tr.notify(ctx, "notifications/initialized", nil)
		}},
		{"stateless/listen", "mcp: listen post", func(status int) string {
			return fmt.Sprintf("mcp: listen http %d", status)
		}, func(ctx context.Context, endpoint string) error {
			tr, err := newStatelessHTTPTransport(serverSpec{Name: "s", URL: endpoint})
			if err != nil {
				return err
			}
			return newStatelessClient(tr).Listen(ctx, subscriptionFilter{ToolsListChanged: true}, func(subscriptionEvent) {})
		}},
	}
}

// requireRedirectRefused checks the error a transport returned for a refused
// redirect: it is ErrStreamableRedirectRefused, its text is the fixed text of the
// operation, and no error in its chain quotes a byte of the Location.
func requireRedirectRefused(t *testing.T, err error, op streamableOp) {
	t.Helper()
	if !errors.Is(err, ErrStreamableRedirectRefused) {
		t.Fatalf("error = %v, want ErrStreamableRedirectRefused", err)
	}
	if want := op.prefix + ": redirect refused; configure the final endpoint URL"; err.Error() != want {
		t.Errorf("error text = %q, want %q", err.Error(), want)
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), redirectCanary) {
			t.Errorf("an error in the chain quotes the Location: %q", e.Error())
		}
	}
}

// TestStreamableRedirectIsRefused: a configured endpoint that answers with a
// redirect is not followed, on every request path of both transports. Nothing is
// sent toward the Location (its host accepts no connection, and the origin
// answers exactly one request, so a relative Location is not followed either),
// and the error is the fixed refusal whether net/http could parse the Location
// or not.
func TestStreamableRedirectIsRefused(t *testing.T) {
	locations := []struct {
		name string
		loc  func(target string) string
	}{
		{"absolute", func(target string) string {
			withUser := strings.Replace(target, "://", "://"+redirectCanary+":"+redirectCanary+"@", 1)
			return withUser + "/" + redirectCanary + "?token=" + redirectCanary
		}},
		{"relative", func(string) string { return "/" + redirectCanary + "?token=" + redirectCanary }},
		{"malformed escape", func(string) string { return "http://%zz" + redirectCanary + "/" }},
		{"malformed host", func(string) string { return "http://[" + redirectCanary + "/" }},
	}
	for _, code := range followedRedirects {
		for _, l := range locations {
			for _, op := range streamableOps() {
				t.Run(fmt.Sprintf("%d/%s/%s", code, l.name, op.name), func(t *testing.T) {
					target := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(http.StatusOK)
					})
					loc := l.loc(target.URL)
					origin := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Location", loc)
						w.WriteHeader(code)
					})

					err := op.run(t.Context(), origin.URL)
					if n := origin.requests.Load(); n != 1 {
						t.Errorf("the endpoint answered %d requests, want 1", n)
					}
					if n := target.conns.Load(); n != 0 {
						t.Errorf("the Location host accepted %d connections, want 0", n)
					}
					requireRedirectRefused(t, err, op)
				})
			}
		}
	}
}

// TestStreamableRedirectBodyIsClosedUnread: the refused response is closed
// without reading its body, so a server that never finishes that body does not
// hold the request, and it sees the connection close.
func TestStreamableRedirectBodyIsClosedUnread(t *testing.T) {
	for _, op := range streamableOps() {
		t.Run(op.name, func(t *testing.T) {
			var answered atomic.Bool
			closed := make(chan bool, 1)
			origin := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
				// Reading the request body to its end lets the server notice a
				// closed connection while the handler still runs.
				_, _ = io.Copy(io.Discard, r.Body)
				if answered.Swap(true) {
					w.WriteHeader(http.StatusOK) // a followed redirect; counted below
					return
				}
				w.Header().Set("Location", "/next")
				w.WriteHeader(http.StatusFound)
				_, _ = io.WriteString(w, "a body that does not end")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					closed <- true
				case <-time.After(streamableWait):
					closed <- false
				}
			})

			err := op.run(t.Context(), origin.URL)
			if !<-closed {
				t.Error("the refused response stayed open after the request returned")
			}
			if n := origin.requests.Load(); n != 1 {
				t.Errorf("the endpoint answered %d requests, want 1", n)
			}
			requireRedirectRefused(t, err, op)
		})
	}
}

// TestStreamableUnfollowedResponseKeepsItsHandling: a response net/http does not
// follow reaches the transport exactly as before and fails with the transport's
// existing status error: a redirect status without a Location, and a status the
// client never follows even with one.
func TestStreamableUnfollowedResponseKeepsItsHandling(t *testing.T) {
	type response struct {
		status   int
		location string
	}
	var responses []response
	for _, code := range followedRedirects {
		responses = append(responses, response{status: code})
	}
	responses = append(responses, response{status: http.StatusMultipleChoices, location: "/" + redirectCanary})
	for _, resp := range responses {
		for _, op := range streamableOps() {
			t.Run(fmt.Sprintf("%d/%s", resp.status, op.name), func(t *testing.T) {
				origin := newCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
					if resp.location != "" {
						w.Header().Set("Location", resp.location)
					}
					w.WriteHeader(resp.status)
				})

				err := op.run(t.Context(), origin.URL)
				if errors.Is(err, ErrStreamableRedirectRefused) {
					t.Fatalf("error = %v: a response net/http does not follow was refused as a redirect", err)
				}
				if want := op.statusErr(resp.status); err == nil || err.Error() != want {
					t.Errorf("error = %v, want %q", err, want)
				}
				if n := origin.requests.Load(); n != 1 {
					t.Errorf("the endpoint answered %d requests, want 1", n)
				}
			})
		}
	}
}

// TestStreamableRequestFailureTextIsFixed: a request that cannot be sent or
// answered fails as before, but its error text is the operation and a fixed
// reason. It no longer quotes the configured URL (path, query, user name) or
// what the cause says about hosts; errors.Is and errors.As still reach the cause.
func TestStreamableRequestFailureTextIsFixed(t *testing.T) {
	stopped := httptest.NewServer(http.NotFoundHandler())
	stoppedURL := stopped.URL
	stopped.Close()

	untrusted := httptest.NewUnstartedServer(http.NotFoundHandler())
	untrusted.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshake is expected
	untrusted.StartTLS()
	t.Cleanup(untrusted.Close)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	t.Cleanup(cancelExpired)

	isA := func(target any) func(error) bool {
		return func(err error) bool { return errors.As(err, target) }
	}
	isErr := func(target error) func(error) bool {
		return func(err error) bool { return errors.Is(err, target) }
	}
	cases := []struct {
		name     string
		ctx      context.Context
		endpoint string
		reason   string
		cause    func(error) bool
	}{
		{"unparsable URL", t.Context(),
			"http://mcp-user:" + redirectCanary + "@127.0.0.1:port-" + redirectCanary + "/mcp",
			"invalid endpoint URL", nil},
		{"unsupported scheme", t.Context(),
			"ftp://127.0.0.1/mcp?token=" + redirectCanary,
			"request failed", nil},
		// A label longer than 63 bytes is not a domain name: the resolver refuses
		// it with a *net.DNSError without asking a server.
		{"unresolvable host", t.Context(),
			"http://" + redirectCanary + strings.Repeat("x", 64) + ".example/mcp",
			"host name resolution failed", isA(new(*net.DNSError))},
		{"connection refused", t.Context(),
			stoppedURL + "/mcp?token=" + redirectCanary,
			"connection refused", isA(new(*net.OpError))},
		{"untrusted certificate", t.Context(),
			untrusted.URL + "/mcp?token=" + redirectCanary,
			"server certificate verification failed", isA(new(*tls.CertificateVerificationError))},
		{"canceled", canceled,
			stoppedURL + "/mcp?token=" + redirectCanary,
			"context canceled", isErr(context.Canceled)},
		{"deadline", expired,
			stoppedURL + "/mcp?token=" + redirectCanary,
			"deadline exceeded", isErr(context.DeadlineExceeded)},
	}
	for _, c := range cases {
		for _, op := range streamableOps() {
			t.Run(c.name+"/"+op.name, func(t *testing.T) {
				err := op.run(c.ctx, c.endpoint)
				if want := op.prefix + ": " + c.reason; err == nil || err.Error() != want {
					t.Fatalf("error = %v, want %q", err, want)
				}
				if c.cause != nil && !c.cause(err) {
					t.Errorf("the cause of %q is not reachable through errors.Is or errors.As", err)
				}
			})
		}
	}
}

// TestStreamableEndpointFormsStillAccepted: URL forms the transports accepted
// before are still accepted and reach the configured endpoint: user information,
// which net/http sends as Basic credentials, and a fragment, which is never sent.
func TestStreamableEndpointFormsStillAccepted(t *testing.T) {
	var basic, path atomic.Int64
	srv := newCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); ok && user == "mcp-user" && pass == "mcp-pass" {
			basic.Add(1)
		}
		if r.URL.Path == "/mcp" && r.URL.Fragment == "" {
			path.Add(1)
		}
		mcpHTTPHandler(false)(w, r)
	})
	endpoint := strings.Replace(srv.URL, "://", "://mcp-user:mcp-pass@", 1) + "/mcp#section"

	for _, op := range streamableOps() {
		if !strings.HasSuffix(op.name, "/request") {
			continue
		}
		t.Run(op.name, func(t *testing.T) {
			if err := op.run(t.Context(), endpoint); err != nil {
				t.Fatalf("a previously accepted endpoint form failed: %v", err)
			}
		})
	}
	if n := srv.requests.Load(); n != 2 || basic.Load() != n || path.Load() != n {
		t.Errorf("requests=%d, with Basic credentials=%d, at /mcp=%d; want 2 of each", n, basic.Load(), path.Load())
	}
}

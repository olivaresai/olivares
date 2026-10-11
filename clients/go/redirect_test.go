// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package olivares

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRedirectOriginAndCredentials(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"same_origin", "other_port", "localhost_vs_127", "http_to_https"} {
			t.Run(fmt.Sprintf("%d/%s", status, destination), func(t *testing.T) {
				type request struct {
					method, auth, tenant string
					body                 []byte
				}
				var mu sync.Mutex
				var seen []request
				record := func(r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					defer mu.Unlock()
					seen = append(seen, request{r.Method, r.Header.Get("Authorization"), r.Header.Get("X-Olivares-Tenant"), body})
				}
				answer := func(w http.ResponseWriter, r *http.Request) {
					record(r)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"redirected":true}`)
				}
				var target *httptest.Server
				if destination == "http_to_https" {
					target = httptest.NewTLSServer(http.HandlerFunc(answer))
				} else {
					target = httptest.NewServer(http.HandlerFunc(answer))
				}
				t.Cleanup(target.Close)
				location := "/destination"
				newOrigin := target.URL
				if destination == "localhost_vs_127" {
					newOrigin = strings.Replace(newOrigin, "127.0.0.1", "localhost", 1)
				}
				if destination != "same_origin" {
					location = newOrigin + location
				}
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/destination" {
						answer(w, r)
						return
					}
					record(r)
					w.Header().Set("Location", location)
					w.WriteHeader(status)
				}))
				t.Cleanup(origin.Close)
				client, err := New(origin.URL, "synthetic-client-token", WithTenant("synthetic-tenant"), WithHTTPClient(target.Client()))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				out, err := client.PostV1AuthLogin(ctx, map[string]any{"sentinel": "synthetic-body"})
				mu.Lock()
				defer mu.Unlock()
				if destination != "same_origin" {
					want := "olivares: the server redirected to " + newOrigin + "; set the client's base URL to it"
					if err == nil || err.Error() != want {
						t.Errorf("error = %v; want %q", err, want)
					}
					if len(seen) != 1 {
						t.Fatalf("cross-origin target received credentials/body: %d total requests", len(seen))
					}
					if seen[0].auth != "Bearer synthetic-client-token" || seen[0].tenant != "synthetic-tenant" || !strings.Contains(string(seen[0].body), "synthetic-body") {
						t.Error("initial authenticated body did not reach its own origin")
					}
					return
				}
				if err != nil || out["redirected"] != true {
					t.Fatalf("same-origin redirect did not complete: %v", err)
				}
				if len(seen) != 2 {
					t.Fatalf("request count = %d; want 2", len(seen))
				}
				if seen[1].auth != "Bearer synthetic-client-token" || seen[1].tenant != "synthetic-tenant" {
					t.Error("redirect header compatibility changed")
				}
				if status < 307 {
					if seen[1].method != "GET" || len(seen[1].body) != 0 {
						t.Error("301/302/303 POST-to-GET behavior changed")
					}
				} else {
					var body map[string]any
					if seen[1].method != "POST" || json.Unmarshal(seen[1].body, &body) != nil || body["sentinel"] != "synthetic-body" {
						t.Error("307/308 body replay behavior changed")
					}
				}
			})
		}
	}
}

func TestClientTruncatedResponseBody(t *testing.T) {
	for _, status := range []int{200, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := `{"error":{"code":"busy","message":"try later"}}`
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+1))
				w.Header().Set("Connection", "close")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}))
			t.Cleanup(server.Close)
			client, err := New(server.URL, "synthetic-client-token")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err = client.GetMetrics(ctx)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error = %v; want unexpected EOF", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("short body was retried: %d calls", calls.Load())
			}
		})
	}
}

func TestClientRedirectCallerPolicyPreserved(t *testing.T) {
	for _, policy := range []string{"headers", "refuse", "retarget"} {
		t.Run(policy, func(t *testing.T) {
			var targetCalls atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetCalls.Add(1)
				_, _ = io.WriteString(w, `{}`)
			}))
			t.Cleanup(target.Close)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/destination" {
					w.Header().Set("Location", "/destination")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				if r.Header.Get("X-Caller") != "kept" || r.Header.Get("Authorization") != "Bearer synthetic-client-token" {
					t.Error("same-origin caller headers were lost")
				}
				_, _ = io.WriteString(w, "ok")
			}))
			t.Cleanup(server.Close)
			refused := errors.New("caller refused redirect")
			var hookCalls atomic.Int32
			hc := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
				hookCalls.Add(1)
				switch policy {
				case "refuse":
					return refused
				case "retarget":
					next.URL, _ = url.Parse(target.URL + "/destination")
				default:
					next.Header.Set("X-Caller", "kept")
				}
				return nil
			}}
			client, err := New(server.URL, "synthetic-client-token", WithHTTPClient(hc))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			body, err := client.GetMetrics(ctx)
			switch policy {
			case "headers":
				if err != nil || string(body) != "ok" {
					t.Fatalf("same-origin caller hook failed: %v", err)
				}
			case "refuse":
				if !errors.Is(err, refused) {
					t.Fatalf("caller error was lost: %v", err)
				}
			case "retarget":
				if err == nil || !strings.Contains(err.Error(), "the server redirected to "+target.URL) {
					t.Fatalf("cross-origin hook override was followed: %v", err)
				}
			}
			if hookCalls.Load() != 1 || targetCalls.Load() != 0 {
				t.Fatalf("hook calls=%d, other-origin requests=%d", hookCalls.Load(), targetCalls.Load())
			}
		})
	}
}

func TestClientRedirectLoopRemainsBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, "synthetic-client-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.GetMetrics(ctx)
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || calls.Load() != 10 {
		t.Fatalf("redirect loop changed: %v, requests=%d", err, calls.Load())
	}
}

func TestClientRedirectDiagnosticContainsOnlyOrigin(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(target.Close)
	location := strings.Replace(target.URL, "://", "://synthetic-user:synthetic-password@", 1) + "/destination?token=synthetic-query#synthetic-fragment"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, "synthetic-client-token")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.PostV1AuthLogin(ctx, map[string]any{"sentinel": "synthetic-body"})
	want := "olivares: the server redirected to " + target.URL + "; set the client's base URL to it"
	if err == nil || err.Error() != want || targetCalls.Load() != 0 {
		t.Fatalf("unsafe redirect diagnostic or request: %v, target requests=%d", err, targetCalls.Load())
	}
}

type redirectTransportFunc func(*http.Request) (*http.Response, error)

func (f redirectTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestClientRedirectScopedOriginIdentity(t *testing.T) {
	cases := []struct {
		name, endpoint, location, refusedOrigin string
	}{
		{"different_zone_case", "http://[fe80::1%25En0]:8443", "http://[fe80::1%25en0]:8443/destination", "http://[fe80::1%en0]:8443"},
		{"same_zone_equivalent_address", "http://[fe80::1%25En0]:8443", "http://[fe80:0:0:0:0:0:0:1%25En0]:8443/destination", ""},
		{"equivalent_ipv6", "http://[::1]", "http://[0:0:0:0:0:0:0:1]:80/destination", ""},
		{"dns_case_default_port", "http://engine.example", "http://ENGINE.EXAMPLE:80/destination", ""},
		{"ipv4_default_port", "https://127.0.0.1", "https://127.0.0.1:443/destination", ""},
	}
	for _, status := range []int{307, 308} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("%d/%s", status, c.name), func(t *testing.T) {
				var requests, destinations, hooks int
				hc := &http.Client{
					Transport: redirectTransportFunc(func(r *http.Request) (*http.Response, error) {
						requests++
						body, err := io.ReadAll(r.Body)
						if err != nil {
							return nil, err
						}
						if r.Method != "POST" || !strings.Contains(string(body), "synthetic-body") || r.Header.Get("X-Olivares-Tenant") != "synthetic-tenant" {
							t.Error("native redirect lost method, body or tenant")
						}
						response := &http.Response{StatusCode: status, Header: http.Header{"Location": {c.location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}
						if requests == 1 {
							if r.Header.Get("Authorization") != "Bearer synthetic-client-token" {
								t.Error("initial authentication was lost")
							}
						} else {
							destinations++
							if r.Header.Get("X-Caller") != "kept" {
								t.Error("same-origin caller hook was lost")
							}
							response.StatusCode = http.StatusOK
							response.Header = http.Header{}
							response.Body = io.NopCloser(strings.NewReader(`{"redirected":true}`))
						}
						return response, nil
					}),
					CheckRedirect: func(next *http.Request, via []*http.Request) error {
						hooks++
						next.Header.Set("X-Caller", "kept")
						return nil
					},
				}
				client, err := New(c.endpoint, "synthetic-client-token", WithTenant("synthetic-tenant"), WithHTTPClient(hc))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				out, err := client.PostV1AuthLogin(ctx, map[string]any{"sentinel": "synthetic-body"})
				if c.refusedOrigin != "" {
					want := "olivares: the server redirected to " + c.refusedOrigin + "; set the client's base URL to it"
					if err == nil || err.Error() != want {
						t.Errorf("error = %v; want %q", err, want)
					}
					if requests != 1 || destinations != 0 || hooks != 0 {
						t.Fatalf("different zone reached transport/hook: requests=%d destinations=%d hooks=%d", requests, destinations, hooks)
					}
				} else if err != nil || out["redirected"] != true || requests != 2 || destinations != 1 || hooks != 1 {
					t.Fatalf("same-origin compatibility changed: error=%v requests=%d destinations=%d hooks=%d", err, requests, destinations, hooks)
				}
			})
		}
	}
}

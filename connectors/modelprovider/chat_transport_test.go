// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const chatTransportJSON = `{"model":"observed/model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hola 🌍"}}]}`

type chatRoundTripper func(*http.Request) (*http.Response, error)

func (f chatRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func chatPrepared(t *testing.T) PreparedChatTextRequest {
	t.Helper()
	p, err := PrepareChatTextRequest(ChatTextRequest{
		Model: "selected/model", Input: "  Hola 🌍\n\"quoted\" \\ <>&  ", MaxCompletionTokens: 37,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func chatConfig(endpoint string) ChatTextTransportConfig {
	return ChatTextTransportConfig{
		Endpoint: endpoint, AuthScheme: AuthBearer, BearerToken: "dedicated-fixture-only",
		AllowHTTP: true, MaxRequestBytes: 4096, MaxResponseBytes: 4096, Timeout: 5 * time.Second,
	}
}

func chatTransport(t *testing.T, cfg ChatTextTransportConfig) *ChatTextTransport {
	t.Helper()
	c, err := NewChatTextTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func requireChatTransportError(t *testing.T, got ChatTextResponse, err error, kind ChatTransportErrorKind, attempted bool, status int) *ChatTransportError {
	t.Helper()
	var e *ChatTransportError
	if !errors.As(err, &e) || e.Kind != kind || e.Attempted != attempted || e.StatusCode != status {
		t.Fatalf("wanted %s attempted=%v status=%d, got %#v", kind, attempted, status, err)
	}
	if !reflect.DeepEqual(got, ChatTextResponse{}) || errors.Unwrap(err) != nil {
		t.Fatal("error leaked a partial response or underlying error")
	}
	return e
}

func TestChatTransportExactBytesDedicatedAuthAndCopiedClient(t *testing.T) {
	for _, auth := range []AuthScheme{AuthBearer, AuthNone} {
		t.Run(string(auth), func(t *testing.T) {
			type captured struct {
				body              []byte
				header            http.Header
				method, uri, host string
				length            int64
			}
			requests := make(chan captured, 1)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- captured{body, r.Header.Clone(), r.Method, r.RequestURI, r.Host, r.ContentLength}
				_, _ = io.WriteString(w, chatTransportJSON)
			}))
			defer srv.Close()
			client := srv.Client()
			originalTransport := client.Transport
			var attempts atomic.Int32
			client.Transport = chatRoundTripper(func(r *http.Request) (*http.Response, error) {
				attempts.Add(1)
				if r.GetBody != nil || r.ContentLength <= 0 || len(r.TransferEncoding) != 0 ||
					r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
					t.Error("request is replayable or contains replay headers")
				}
				return originalTransport.RoundTrip(r)
			})
			jar, _ := cookiejar.New(nil)
			u, _ := url.Parse(srv.URL)
			jar.SetCookies(u, []*http.Cookie{{Name: "inventory", Value: "unrelated-cookie-fixture"}})
			client.Jar = jar
			client.CheckRedirect = func(*http.Request, []*http.Request) error { panic("caller redirect policy must not run") }
			redirectPointer := reflect.ValueOf(client.CheckRedirect).Pointer()
			cfg := chatConfig(srv.URL + "/explicit%2Fchat/custom")
			cfg.AllowHTTP, cfg.HTTPClient, cfg.AuthScheme = false, client, auth
			if auth == AuthNone {
				cfg.BearerToken = ""
			}
			p := chatPrepared(t)
			cfg.MaxRequestBytes = len(p.Bytes()) // Exact request limit must be admitted.
			transport := chatTransport(t, cfg)
			if client.Jar != jar || reflect.ValueOf(client.CheckRedirect).Pointer() != redirectPointer {
				t.Fatal("constructor changed caller client")
			}
			// Mutating the supplied values after construction must not alter the private client/config.
			cfg.Endpoint, cfg.BearerToken = "http://unused.invalid/", "changed-fixture"
			client.Timeout = time.Nanosecond
			client.Transport = chatRoundTripper(func(*http.Request) (*http.Response, error) {
				t.Error("caller client was not copied")
				return nil, errors.New("unreachable")
			})
			got, err := transport.Execute(context.Background(), p)
			if err != nil || got.Text == nil || *got.Text != "Hola 🌍" || got.Model != "observed/model" || got.Usage != nil {
				t.Fatalf("text/unknown usage semantics changed: %#v %v", got, err)
			}
			r := <-requests
			if !bytes.Equal(r.body, p.Bytes()) || sha256.Sum256(r.body) != p.Digest() ||
				r.method != "POST" || r.uri != "/explicit%2Fchat/custom" || r.host != u.Host ||
				r.length != int64(len(p.Bytes())) || attempts.Load() != 1 {
				t.Fatal("destination, exact JSON, digest or single attempt changed")
			}
			wantAuth := ""
			if auth == AuthBearer {
				wantAuth = "Bearer dedicated-fixture-only"
			}
			if r.header.Get("Authorization") != wantAuth || r.header.Get("Cookie") != "" ||
				r.header.Get("Content-Type") != "application/json" || r.header.Get("Accept") != "application/json" {
				t.Fatal("dedicated authentication/minimal headers changed")
			}
		})
	}
}

func TestChatTransportInvalidConfigurationAndPredispatchZeroCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, chatTransportJSON)
	}))
	defer srv.Close()
	for _, endpoint := range []string{"", "/relative", "//localhost/chat", "ftp://localhost/chat", "https:///chat", "https://user:fixture@localhost/chat", srv.URL + "?x=fixture", srv.URL + "?", srv.URL + "#fragment", srv.URL + "#", "https://localhost:", "https://localhost:0/chat", "https://localhost:65536/chat", "https://localhost:bad/chat", "https://localhost/\nfixture"} {
		t.Run("url_"+strconv.Itoa(len(endpoint))+"_"+strconv.Itoa(strings.Count(endpoint, "/")), func(t *testing.T) {
			cfg := chatConfig(endpoint)
			c, err := NewChatTextTransport(cfg)
			requireChatTransportError(t, ChatTextResponse{}, err, ChatTransportInvalidConfig, false, 0)
			if c != nil {
				t.Fatal("invalid config returned client")
			}
		})
	}
	for name, change := range map[string]func(*ChatTextTransportConfig){
		"http_without_opt_in":         func(c *ChatTextTransportConfig) { c.AllowHTTP = false },
		"missing_auth_scheme":         func(c *ChatTextTransportConfig) { c.AuthScheme = "" },
		"unknown_auth_scheme":         func(c *ChatTextTransportConfig) { c.AuthScheme = AuthAnthropicKey },
		"missing_bearer":              func(c *ChatTextTransportConfig) { c.BearerToken = "" },
		"none_with_token":             func(c *ChatTextTransportConfig) { c.AuthScheme = AuthNone },
		"credential_header_injection": func(c *ChatTextTransportConfig) { c.BearerToken = "fixture\r\nHost: elsewhere" },
		"credential_space":            func(c *ChatTextTransportConfig) { c.BearerToken = "fixture key" },
		"credential_nonascii":         func(c *ChatTextTransportConfig) { c.BearerToken = "señal" },
		"credential_padding_middle":   func(c *ChatTextTransportConfig) { c.BearerToken = "a=b" },
		"credential_only_padding":     func(c *ChatTextTransportConfig) { c.BearerToken = "==" },
		"request_zero":                func(c *ChatTextTransportConfig) { c.MaxRequestBytes = 0 },
		"response_negative":           func(c *ChatTextTransportConfig) { c.MaxResponseBytes = -1 },
		"timeout_zero":                func(c *ChatTextTransportConfig) { c.Timeout = 0 },
		"timeout_negative":            func(c *ChatTextTransportConfig) { c.Timeout = -1 },
		"client_negative_timeout":     func(c *ChatTextTransportConfig) { c.HTTPClient = &http.Client{Timeout: -1} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := chatConfig(srv.URL)
			change(&cfg)
			c, err := NewChatTextTransport(cfg)
			requireChatTransportError(t, ChatTextResponse{}, err, ChatTransportInvalidConfig, false, 0)
			if c != nil {
				t.Fatal("invalid config returned client")
			}
		})
	}
	if strconv.IntSize == 64 {
		cfg := chatConfig(srv.URL)
		cfg.MaxResponseBytes = int(int64(math.MaxInt64))
		_, err := NewChatTextTransport(cfg)
		requireChatTransportError(t, ChatTextResponse{}, err, ChatTransportInvalidConfig, false, 0)
	}
	c := chatTransport(t, chatConfig(srv.URL))
	p := chatPrepared(t)
	got, err := c.Execute(context.Background(), PreparedChatTextRequest{})
	requireChatTransportError(t, got, err, ChatTransportInvalidRequest, false, 0)
	got, err = c.Execute(nil, p)
	requireChatTransportError(t, got, err, ChatTransportInvalidRequest, false, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = c.Execute(ctx, p)
	requireChatTransportError(t, got, err, ChatTransportCanceled, false, 0)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got, err = c.Execute(ctx, p)
	requireChatTransportError(t, got, err, ChatTransportTimeout, false, 0)
	cfg := chatConfig(srv.URL)
	cfg.MaxRequestBytes = len(p.Bytes()) - 1
	got, err = chatTransport(t, cfg).Execute(context.Background(), p)
	requireChatTransportError(t, got, err, ChatTransportLimit, false, 0)
	var zero ChatTextTransport
	got, err = zero.Execute(context.Background(), p)
	requireChatTransportError(t, got, err, ChatTransportInvalidConfig, false, 0)
	if calls.Load() != 0 {
		t.Fatalf("invalid local input dispatched %d times", calls.Load())
	}
	if c.client.Transport == nil || c.client.Transport == http.DefaultTransport || &c.client == http.DefaultClient {
		t.Fatal("default client/transport was shared")
	}
}

func TestChatTransportRedirectsAndStatusesNeverFollowOrRetry(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		_, _ = io.WriteString(w, chatTransportJSON)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308, 401, 403, 429, 500, 502, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", target.URL+"/other-audience")
				w.Header().Set("Retry-After", "12")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "synthetic-private-provider-body")
			}))
			defer srv.Close()
			got, err := chatTransport(t, chatConfig(srv.URL)).Execute(context.Background(), chatPrepared(t))
			e := requireChatTransportError(t, got, err, ChatTransportHTTP, true, status)
			if strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), srv.URL) {
				t.Fatal("error disclosed upstream data")
			}
			if e.HasRetryAfter != (status == 429) || status == 429 && e.RetryAfter != 12*time.Second {
				t.Fatal("Retry-After semantics changed")
			}
			if calls.Load() != 1 || targetCalls.Load() != 0 {
				t.Fatal("status caused retry or redirect")
			}
		})
	}
}

func TestChatTransportResponseLimitsAndProtocolErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limit      int
		kind       ChatTransportErrorKind
		codec      ChatTextErrorKind
	}{
		{"exact", chatTransportJSON, len(chatTransportJSON), "", ""},
		{"one_over", chatTransportJSON + " ", len(chatTransportJSON), ChatTransportLimit, ""},
		{"truncated", chatTransportJSON[:len(chatTransportJSON)-1], 4096, ChatTransportProtocol, ChatTextInvalidJSON},
		{"trailing", chatTransportJSON + "{}", 4096, ChatTransportProtocol, ChatTextInvalidJSON},
		{"unsupported_tools", `{"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"synthetic-private-tool"}]}}]}`, 4096, ChatTransportProtocol, ChatTextUnsupportedResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, tc.body) }))
			defer srv.Close()
			cfg := chatConfig(srv.URL)
			cfg.MaxResponseBytes = tc.limit
			got, err := chatTransport(t, cfg).Execute(context.Background(), chatPrepared(t))
			if tc.kind == "" {
				if err != nil || got.State != ChatTextStopped {
					t.Fatalf("exact response limit failed: %v", err)
				}
			} else {
				e := requireChatTransportError(t, got, err, tc.kind, true, 200)
				if e.CodecKind != tc.codec {
					t.Fatalf("codec classification: %s", e.CodecKind)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("response failure retried")
			}
		})
	}
	t.Run("wire_truncation_despite_valid_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(chatTransportJSON)+10))
			_, _ = io.WriteString(w, chatTransportJSON)
		}))
		defer srv.Close()
		got, err := chatTransport(t, chatConfig(srv.URL)).Execute(context.Background(), chatPrepared(t))
		requireChatTransportError(t, got, err, ChatTransportRead, true, 200)
	})
}

type chatTestBody struct {
	read     func([]byte) (int, error)
	closed   bool
	closeErr error
}

func (b *chatTestBody) Read(p []byte) (int, error) { return b.read(p) }
func (b *chatTestBody) Close() error               { b.closed = true; return b.closeErr }

func TestChatTransportBoundedReadAndSafeFailureCustody(t *testing.T) {
	for _, mode := range []string{"read_error_with_bytes", "limit_plus_one", "http_without_body_read", "safe_transport_error", "complete_then_cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, readBytes := 0, 0
			privateErr := errors.New("synthetic-private-transport-url-token-body")
			body := &chatTestBody{read: func(p []byte) (int, error) { t.Error("unexpected body read"); return 0, io.EOF }}
			status := 200
			cfg := chatConfig("https://configured.example.invalid/full/chat")
			switch mode {
			case "read_error_with_bytes":
				body.read = func(p []byte) (int, error) { return copy(p, chatTransportJSON), privateErr }
			case "limit_plus_one":
				cfg.MaxResponseBytes = 16
				body.read = func(p []byte) (int, error) {
					for i := range p {
						p[i] = 'x'
					}
					readBytes += len(p)
					return len(p), nil
				}
			case "http_without_body_read":
				status = 401
			case "complete_then_cancel":
				body.read = func(p []byte) (int, error) { n := copy(p, chatTransportJSON); cancel(); return n, io.EOF }
				body.closeErr = privateErr
			}
			cfg.HTTPClient = &http.Client{Transport: chatRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "safe_transport_error" {
					return nil, &url.Error{Op: "Post", URL: cfg.Endpoint, Err: privateErr}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body, Request: r}, nil
			})}
			got, err := chatTransport(t, cfg).Execute(ctx, chatPrepared(t))
			if mode == "complete_then_cancel" {
				if err != nil || got.Text == nil || *got.Text != "Hola 🌍" {
					t.Fatal("late cancellation erased completed response")
				}
			} else {
				kind := ChatTransportRead
				if mode == "limit_plus_one" {
					kind = ChatTransportLimit
				}
				if mode == "http_without_body_read" {
					kind = ChatTransportHTTP
				}
				if mode == "safe_transport_error" {
					kind = ChatTransportNetwork
					status = 0
				}
				requireChatTransportError(t, got, err, kind, true, status)
				if strings.Contains(err.Error(), "synthetic-private") || errors.Is(err, privateErr) {
					t.Fatal("provider error was exposed")
				}
			}
			if calls != 1 || mode != "safe_transport_error" && !body.closed {
				t.Fatal("attempt count or body close failed")
			}
			if mode == "limit_plus_one" && readBytes != 17 {
				t.Fatalf("read %d bytes, wanted exactly limit+1", readBytes)
			}
		})
	}
}

func TestChatTransportRetryAfterValidation(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value   string
		want    time.Duration
		present bool
	}{
		{"0", 0, true}, {"12", 12 * time.Second, true}, {"86400", 24 * time.Hour, true}, {"86401", 0, false},
		{"-1", 0, false}, {"1.5", 0, false}, {"18446744073709551615", 0, false}, {strings.Repeat("9", 65), 0, false}, {"x\r\nfixture", 0, false},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute, true}, {now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		{now.Add(25 * time.Hour).Format(http.TimeFormat), 0, false},
	} {
		d, ok := chatRetryAfter(tc.value, now)
		if d != tc.want || ok != tc.present {
			t.Fatalf("Retry-After control %q: %v %v", tc.value, d, ok)
		}
	}
}

type chatReadSignal struct {
	io.ReadCloser
	once    sync.Once
	started chan struct{}
}

func (b *chatReadSignal) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func chatAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("local HTTP synchronization did not finish")
	}
}

func TestChatTransportRealHTTPCancellationAndTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		for _, cause := range []string{"cancel", "configured_timeout", "caller_deadline"} {
			t.Run(phase+"_"+cause, func(t *testing.T) {
				serverStarted, serverCanceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					close(serverStarted)
					if phase == "body" {
						_, _ = io.WriteString(w, `{"model":`)
						w.(http.Flusher).Flush()
					}
					select {
					case <-r.Context().Done():
						close(serverCanceled)
					case <-release:
					}
				}))
				defer srv.Close()
				defer close(release)
				cfg := chatConfig(srv.URL)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cause == "configured_timeout" {
					cfg.Timeout = 250 * time.Millisecond
				}
				bodyStarted := make(chan struct{})
				base := srv.Client().Transport
				cfg.HTTPClient = &http.Client{Transport: chatRoundTripper(func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok {
						t.Error("request did not receive timeout context")
					}
					if parent, ok := ctx.Deadline(); ok && deadline.After(parent) {
						t.Error("caller deadline was extended")
					}
					resp, err := base.RoundTrip(r)
					if err == nil && phase == "body" {
						resp.Body = &chatReadSignal{ReadCloser: resp.Body, started: bodyStarted}
					}
					return resp, err
				})}
				transport := chatTransport(t, cfg)
				p := chatPrepared(t)
				// The 250 ms caller deadline starts HERE, after the transport and the
				// prepared request exist. Created before them it was already running while
				// the fixture was being built, so under -race on a contended runner the
				// budget could be spent before the request ever reached the server and the
				// subtest would fail at `chatAwait(t, serverStarted)` instead of on the
				// cancellation contract. Same class as 01f81b8e81 / 4859cc43f3 /
				// 346bce0c8a, three orders of magnitude smaller.
				// The deadline gets a cancel of its OWN rather than replacing the one above.
				// Reassigning `cancel` here is what the file used to do, and `go vet`'s
				// lostcancel refuses it once the assignment stops being adjacent to a
				// `defer cancel()` — and the `cause == "cancel"` branch below still wants the
				// plain cancel, which is exactly the one it keeps.
				if cause == "caller_deadline" {
					cancel()
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
					defer deadlineCancel()
				}
				type outcome struct {
					response ChatTextResponse
					err      error
				}
				done := make(chan outcome, 1)
				go func() { r, e := transport.Execute(ctx, p); done <- outcome{r, e} }()
				chatAwait(t, serverStarted)
				if phase == "body" {
					chatAwait(t, bodyStarted)
				}
				if cause == "cancel" {
					cancel()
				}
				var result outcome
				select {
				case result = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("HTTP operation ignored cancellation")
				}
				kind := ChatTransportTimeout
				if cause == "cancel" {
					kind = ChatTransportCanceled
				}
				status := 0
				if phase == "body" {
					status = 200
				}
				requireChatTransportError(t, result.response, result.err, kind, true, status)
				chatAwait(t, serverCanceled)
				if calls.Load() != 1 {
					t.Fatal("canceled request retried")
				}
			})
		}
	}
}

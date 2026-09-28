// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package trace

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/baggage"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type propagationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f propagationRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// The default constructor and real middleware/client are the seam under test.
// The only fake is the external provider's HTTP transport; no provider is called.
func TestProviderPropagationDefaultDeniesBaggage(t *testing.T) {
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	const incoming = "token=D09-SYNTHETIC-SENTINEL,ai.olivares.safe.token=D09-PREFIX-SENTINEL"
	for _, endpoint := range []string{"/v1/messages", "/v1/oauth/token"} {
		t.Run(endpoint, func(t *testing.T) {
			p, err := New(context.Background(), Config{})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				if got := r.Header.Get("baggage"); got != "" {
					t.Errorf("provider received unapproved baggage: %q", got)
				}
				if baggage.FromContext(r.Context()).Len() != 0 {
					t.Error("provider transport context retains unapproved baggage")
				}
				if got := r.Header.Get("traceparent"); got != parent {
					t.Errorf("traceparent = %q, want continued %q", got, parent)
				}
				if r.Header.Get("X-Control") != "preserved" {
					t.Error("unrelated header was lost")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
			}))
			handler := p.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()
				if got := baggage.FromContext(ctx).Member("token").Value(); got != "D09-SYNTHETIC-SENTINEL" {
					t.Fatalf("ingress did not retain the test baggage: %q", got)
				}
				out, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com"+endpoint, nil)
				if err != nil {
					t.Fatal(err)
				}
				out.Header.Set("X-Control", "preserved")
				before := out.Header.Clone()
				resp, err := client.Do(out)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if !reflect.DeepEqual(out.Header, before) {
					t.Error("provider propagation mutated caller-owned headers")
				}
				if out.Context() != ctx || baggage.FromContext(ctx).Len() != 2 {
					t.Error("provider propagation changed the caller's context")
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/ingress", nil)
			req.Header.Set("traceparent", parent)
			req.Header.Set("baggage", incoming)
			before := req.Header.Clone()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if !called || rec.Code != http.StatusOK {
				t.Fatalf("provider called=%v, handler status=%d", called, rec.Code)
			}
			if !reflect.DeepEqual(req.Header, before) {
				t.Error("ingress headers were mutated")
			}
		})
	}
}

// New remains the construction seam in both modes. The enabled mode uses an
// empty loopback OTLP receiver, not a qualified backend or a real provider.
func propagationProvider(t *testing.T, cfg Config, enabled bool) *Provider {
	t.Helper()
	if enabled {
		collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(collector.Close)
		cfg.Enabled, cfg.Endpoint, cfg.Protocol, cfg.SampleRatio = true, collector.URL, ProtocolHTTP, 1
	}
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("shutdown tracing fixture: %v", err)
		}
	})
	return p
}

func TestProviderPropagationDeclaredControl(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-op", true: "enabled"}[enabled], func(t *testing.T) {
			p := propagationProvider(t, Config{ProviderBaggageAllowlist: []ProviderBaggageRule{{
				Origin: "https://api.anthropic.com", Key: "deployment.environment", Values: []string{"canary"},
			}}}, enabled)
			var serverSpan string
			called := false
			client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				if got := r.Header.Get("baggage"); got != "deployment.environment=canary" {
					t.Errorf("baggage = %q, want only the declared control without properties", got)
				}
				if got := r.Header.Get("tracestate"); got != "vendor=opaque" {
					t.Errorf("tracestate = %q", got)
				}
				parts := strings.Split(r.Header.Get("traceparent"), "-")
				if len(parts) != 4 || parts[1] != "4bf92f3577b34da6a3ce929d0e0e4736" {
					t.Fatalf("trace correlation lost: %q", r.Header.Get("traceparent"))
				}
				if enabled && parts[2] == serverSpan {
					t.Error("Messages hop did not carry its client span")
				}
				bag := baggage.FromContext(r.Context())
				if bag.Len() != 1 || bag.Member("deployment.environment").Value() != "canary" {
					t.Error("outbound context does not match the declared baggage")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"msg_control","model":"test","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
			}))
			handler := p.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				serverSpan = oteltrace.SpanContextFromContext(r.Context()).SpanID().String()
				out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(`{"model":"test"}`))
				if err != nil {
					t.Fatal(err)
				}
				before := out.Header.Clone()
				resp, err := client.Do(out)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if !reflect.DeepEqual(before, out.Header) || baggage.FromContext(r.Context()).Len() != 3 {
					t.Error("caller headers or internal baggage changed")
				}
			}))
			req := httptest.NewRequest(http.MethodPost, "/ingress", nil)
			req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
			req.Header.Set("tracestate", "vendor=opaque")
			req.Header.Set("baggage", "deployment.environment=canary;secret=D09-PROPERTY,token=D09-DENIED,deployment.environment.secret=D09-PREFIX")
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if !called {
				t.Fatal("provider transport was not called")
			}
		})
	}
}

func propagationControlConfig() Config {
	return Config{ProviderBaggageAllowlist: []ProviderBaggageRule{{
		Origin: "https://api.anthropic.com", Key: "deployment.environment", Values: []string{"canary"},
	}}}
}

// Observe the request only at the external HTTP seam after real middleware and
// client processing. This fixture never calls private policy helpers.
func observePropagation(t *testing.T, p *Provider, baggageHeaders []string, target string, headers http.Header) (*http.Request, *http.Request, context.Context) {
	t.Helper()
	var received, caller *http.Request
	var ingress context.Context
	client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		received = r.Clone(r.Context())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))
	handler := p.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ingress = r.Context()
		var err error
		caller, err = http.NewRequestWithContext(ingress, http.MethodPost, target, strings.NewReader(`{"model":"test"}`))
		if err != nil {
			t.Fatal(err)
		}
		caller.Header = headers.Clone()
		// Direct RoundTrip preserves manually noncanonical header names for the
		// adversarial caller-ownership tests below.
		resp, err := client.Transport.RoundTrip(caller)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}))
	in := httptest.NewRequest(http.MethodPost, "/ingress", nil)
	in.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	for _, value := range baggageHeaders {
		in.Header.Add("baggage", value)
	}
	handler.ServeHTTP(httptest.NewRecorder(), in)
	if received == nil {
		t.Fatal("external provider transport was not reached")
	}
	return received, caller, ingress
}

func TestProviderPropagationExactMatch(t *testing.T) {
	p := propagationProvider(t, propagationControlConfig(), false)
	for _, tc := range []struct{ member, want string }{
		{"deployment.environment=canary", "deployment.environment=canary"},
		{"Deployment.Environment=canary", ""},
		{"deployment.environment.secret=canary", ""},
		{"deployment.environment=CANARY", ""},
		{"deployment.environment=secret-canary", ""},
		{"deployment.environment=canary-secret", ""},
		{"deployment.environment=D09-SYNTHETIC-SECRET", ""},
	} {
		t.Run(tc.member, func(t *testing.T) {
			got, _, _ := observePropagation(t, p, []string{tc.member}, "https://api.anthropic.com/v1/messages", nil)
			if value := got.Header.Get("baggage"); value != tc.want {
				t.Errorf("baggage = %q, want %q", value, tc.want)
			}
		})
	}
}

func TestProviderPropagationOriginBinding(t *testing.T) {
	p := propagationProvider(t, propagationControlConfig(), false)
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"https://api.anthropic.com/v1/messages", true},
		{"https://API.ANTHROPIC.COM:443/v1/messages", true},
		{"http://api.anthropic.com/v1/messages", false},
		{"https://api.anthropic.com:444/v1/messages", false},
		{"https://api.anthropic.com.evil.example/v1/messages", false},
		{"https://other.example/v1/messages", false},
		{"https://api.anthropic.com./v1/messages", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			got, _, _ := observePropagation(t, p, []string{"deployment.environment=canary"}, tc.url, nil)
			want := ""
			if tc.allowed {
				want = "deployment.environment=canary"
			}
			if value := got.Header.Get("baggage"); value != want {
				t.Errorf("baggage = %q, want %q", value, want)
			}
		})
	}
	t.Run("Host override is not the policy origin", func(t *testing.T) {
		member, _ := baggage.NewMemberRaw("deployment.environment", "canary")
		bag, _ := baggage.New(member)
		req, _ := http.NewRequestWithContext(baggage.ContextWithBaggage(context.Background(), bag), http.MethodGet, "https://other.example/", nil)
		req.Host = "api.anthropic.com"
		client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("baggage") != "" {
				t.Error("Host override admitted baggage for a different URL origin")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
		}))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	})
	t.Run("redirect re-evaluates the destination", func(t *testing.T) {
		var seen []string
		client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			seen = append(seen, r.Header.Get("baggage"))
			if r.URL.Host == "api.anthropic.com" {
				return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"https://other.example/final"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}))
		h := p.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			out, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.anthropic.com/start", nil)
			resp, err := client.Do(out)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}))
		in := httptest.NewRequest(http.MethodGet, "/", nil)
		in.Header.Set("baggage", "deployment.environment=canary")
		h.ServeHTTP(httptest.NewRecorder(), in)
		if !reflect.DeepEqual(seen, []string{"deployment.environment=canary", ""}) {
			t.Errorf("redirect baggage = %v", seen)
		}
	})
}

func TestProviderPropagationSanitizesRawHeaders(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		p := propagationProvider(t, Config{}, enabled)
		for _, endpoint := range []string{"/v1/messages", "/v1/oauth/token"} {
			headers := http.Header{
				"Baggage": {"token=D09-RAW"}, "baggage": {"token=D09-LOWER"}, "bAgGaGe": {"token=D09-MIXED"},
				"Traceparent": {"stale"}, "traceparent": {"stale-lower"}, "TrAcEsTaTe": {"secret=D09-RAW"},
				"Authorization": {"Bearer D09-UNRELATED"},
			}
			got, caller, _ := observePropagation(t, p, nil, "https://api.anthropic.com"+endpoint, headers)
			for key := range got.Header {
				if strings.EqualFold(key, "baggage") || strings.EqualFold(key, "tracestate") {
					t.Errorf("enabled=%v endpoint=%s: raw header survived: %q", enabled, endpoint, key)
				}
			}
			if !strings.HasPrefix(got.Header.Get("traceparent"), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
				t.Error("context traceparent was not rebuilt")
			}
			if got.Header.Get("Authorization") != "Bearer D09-UNRELATED" || !reflect.DeepEqual(caller.Header, headers) {
				t.Error("caller headers or unrelated outbound header changed")
			}
		}
	}
}

func TestProviderPropagationMalformedBaggage(t *testing.T) {
	p := propagationProvider(t, propagationControlConfig(), false)
	for _, tc := range []struct {
		name   string
		values []string
		want   string
	}{
		{"partial malformed", []string{"bad=%GG", "deployment.environment=canary"}, "deployment.environment=canary"},
		{"denied last duplicate", []string{"deployment.environment=canary,deployment.environment=secret"}, ""},
		{"allowed last duplicate", []string{"deployment.environment=secret,deployment.environment=canary"}, "deployment.environment=canary"},
		{"properties stripped", []string{"deployment.environment=canary;secret=D09-PROPERTY"}, "deployment.environment=canary"},
		{"decoded exact value", []string{"deployment.environment=%63anary"}, "deployment.environment=canary"},
		{"no double decoding", []string{"deployment.environment=%2563anary"}, ""},
		{"aggregate size bound", []string{strings.Repeat("x", 8193), "deployment.environment=canary"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := observePropagation(t, p, tc.values, "https://api.anthropic.com/v1/messages", nil)
			if value := got.Header.Get("baggage"); value != tc.want {
				t.Errorf("baggage = %q, want %q", value, tc.want)
			}
		})
	}
	t.Run("member count bound", func(t *testing.T) {
		members := make([]string, 0, 65)
		for i := range 64 {
			members = append(members, fmt.Sprintf("denied%d=x", i))
		}
		members = append(members, "deployment.environment=canary")
		got, _, _ := observePropagation(t, p, []string{strings.Join(members, ",")}, "https://api.anthropic.com/v1/messages", nil)
		if got.Header.Get("baggage") != "" {
			t.Error("a member beyond the pinned parser limit reached egress")
		}
	})
}

func TestProviderPropagationTraceCompatibility(t *testing.T) {
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	for _, enabled := range []bool{false, true} {
		p := propagationProvider(t, Config{}, enabled)
		for _, tc := range []struct {
			name, parent string
			valid        bool
		}{
			{"random flag", "00-" + traceID + "-00f067aa0ba902b7-03", true},
			{"future version", "01-" + traceID + "-00f067aa0ba902b7-01-future", true},
			{"malformed", "not-a-traceparent", false},
		} {
			t.Run(fmt.Sprintf("enabled=%v/%s", enabled, tc.name), func(t *testing.T) {
				called := false
				client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					called = true
					value := r.Header.Get("traceparent")
					if tc.valid {
						if !strings.HasPrefix(value, "00-"+traceID+"-") || r.Header.Get("tracestate") != "vendora=opaque,vendorb=value" {
							t.Errorf("trace context was not continued: %q / %q", value, r.Header.Get("tracestate"))
						}
					} else if (!enabled && value != "") || value == "stale" || r.Header.Get("tracestate") != "" {
						t.Errorf("invalid input forwarded stale context: %q / %q", value, r.Header.Get("tracestate"))
					}
					if r.Header.Get("baggage") != "" {
						t.Error("default policy forwarded baggage")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}))
				h := p.HTTPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					out, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
					out.Header.Set("traceparent", "stale")
					out.Header.Set("tracestate", "raw=stale")
					resp, err := client.Do(out)
					if err != nil {
						t.Fatal(err)
					}
					_ = resp.Body.Close()
				}))
				in := httptest.NewRequest(http.MethodPost, "/", nil)
				in.Header.Set("traceparent", tc.parent)
				in.Header.Set("tracestate", "vendora=opaque,vendorb=value")
				in.Header.Set("baggage", "token=D09-TRACE-COMPAT")
				h.ServeHTTP(httptest.NewRecorder(), in)
				if !called {
					t.Fatal("malformed telemetry blocked the provider call")
				}
			})
		}
	}
}

func TestProviderPropagationOwnershipAndCancellation(t *testing.T) {
	cfg := propagationControlConfig()
	p := propagationProvider(t, cfg, false)
	// The caller retains its mutable configuration; the Provider owns a compiled copy.
	cfg.ProviderBaggageAllowlist[0].Origin = "https://other.example"
	cfg.ProviderBaggageAllowlist[0].Key = "changed"
	cfg.ProviderBaggageAllowlist[0].Values[0] = "changed"
	member, err := baggage.NewMemberRaw("deployment.environment", "canary")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := baggage.NewMemberRaw("token", "D09-CONTEXT-PRIVATE")
	if err != nil {
		t.Fatal(err)
	}
	bag, err := baggage.New(member, denied)
	if err != nil {
		t.Fatal(err)
	}
	type contextKey struct{}
	ctx := context.WithValue(baggage.ContextWithBaggage(context.Background(), bag), contextKey{}, "preserved")
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	cancel()
	for _, headers := range []http.Header{nil, {"X-Control": {"unchanged"}}} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/oauth/token", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = headers.Clone()
		called := false
		client := p.AnthropicHTTPClient(propagationRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			gotDeadline, ok := r.Context().Deadline()
			if !ok || !gotDeadline.Equal(deadline) || r.Context().Err() != context.Canceled || r.Context().Value(contextKey{}) != "preserved" {
				t.Error("propagation changed cancellation, deadline or unrelated context value")
			}
			if r.Header.Get("baggage") != "deployment.environment=canary" || baggage.FromContext(r.Context()).Len() != 1 {
				t.Error("configuration mutation changed the owned policy or filtering failed")
			}
			r.Header.Set("X-Control", "transport-owned")
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
		}))
		resp, err := client.Transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if !called || req.Context() != ctx || baggage.FromContext(ctx).Len() != 2 || !reflect.DeepEqual(req.Header, headers) {
			t.Error("external call did not preserve caller-owned state")
		}
	}
}

func TestProviderPropagationConcurrent(t *testing.T) {
	p := propagationProvider(t, propagationControlConfig(), false)
	for _, target := range []string{"https://api.anthropic.com/v1/messages", "https://other.example/v1/messages"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			got, _, _ := observePropagation(t, p, []string{"deployment.environment=canary,token=D09-CONCURRENT"}, target, nil)
			want := ""
			if target == "https://api.anthropic.com/v1/messages" {
				want = "deployment.environment=canary"
			}
			if value := got.Header.Get("baggage"); value != want {
				t.Errorf("concurrent baggage = %q, want %q", value, want)
			}
		})
	}
}

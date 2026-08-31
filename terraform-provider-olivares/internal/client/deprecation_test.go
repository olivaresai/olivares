// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// Deprecation header values in the formats the engine emits on a deprecated
// route: RFC 9745 Structured Field Date, RFC 8594 HTTP-date, and RFC 8288
// Link pairs for the migration guide and the sunset notice. Per RFC 9745 §3
// and the stable-surface policy (sunset ≥ deprecation + 24 months), the
// sunset (@1846022400) is 24 months after the deprecation (@1782864000,
// 2026-07-01T00:00:00Z).
const (
	testDeprecation = "@1782864000"
	testSunset      = "Sat, 01 Jul 2028 00:00:00 GMT"
	testGuideURL    = "https://docs.olivares.invalid/reference/api-stability/#agents"
	testLinkHeader  = "<" + testGuideURL + `>; rel="deprecation", <https://docs.olivares.invalid/reference/api-stability/>; rel="sunset"`
)

// newDeprecatedServer returns a fake engine that marks EVERY response as
// deprecated, the way stability.go decorates a route under a deprecation
// window. No live route is deprecated today, hence httptest rather than e2e.
func newDeprecatedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", testDeprecation)
		w.Header().Set("Sunset", testSunset)
		w.Header().Set("Link", testLinkHeader)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
}

// TestDeprecationNoticeDedupPerEndpoint exercises the choke point: repeated
// calls to the same method+path yield ONE notice, distinct paths and distinct
// methods on the same path each yield their own, and the recorded fields carry
// the raw header values plus the extracted rel="deprecation" target.
func TestDeprecationNoticeDedupPerEndpoint(t *testing.T) {
	srv := newDeprecatedServer(t)
	defer srv.Close()

	c := New(Options{Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant})
	ctx := context.Background()

	// 3× the same endpoint, 1× another path, 1× another method on the first
	// path: 5 calls, 3 unique method+path pairs.
	for range 3 {
		if _, err := c.GetAgent(ctx, "", "agt_001"); err != nil {
			t.Fatalf("GetAgent agt_001: %v", err)
		}
	}
	if _, err := c.GetAgent(ctx, "", "agt_002"); err != nil {
		t.Fatalf("GetAgent agt_002: %v", err)
	}
	if err := c.DeleteAgent(ctx, "", "agt_001"); err != nil {
		t.Fatalf("DeleteAgent agt_001: %v", err)
	}

	notices := c.DeprecationNotices()
	if len(notices) != 3 {
		t.Fatalf("DeprecationNotices len = %d, want 3 (got %+v)", len(notices), notices)
	}

	want := []Notice{
		{Method: http.MethodGet, Path: "/v1/agents/agt_001", Deprecation: testDeprecation, Sunset: testSunset, Link: testGuideURL},
		{Method: http.MethodGet, Path: "/v1/agents/agt_002", Deprecation: testDeprecation, Sunset: testSunset, Link: testGuideURL},
		{Method: http.MethodDelete, Path: "/v1/agents/agt_001", Deprecation: testDeprecation, Sunset: testSunset, Link: testGuideURL},
	}
	for i, w := range want {
		if notices[i] != w {
			t.Errorf("notice[%d] = %+v, want %+v", i, notices[i], w)
		}
	}
}

// TestNoDeprecationHeadersNoNotices is the quiet path: a healthy (header-less)
// server must record nothing and never fire the hook.
func TestNoDeprecationHeadersNoNotices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sampleDTO("agt_001"))
	}))
	defer srv.Close()

	var fired atomic.Int64
	c := New(Options{
		Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant,
		OnDeprecation: func(context.Context, Notice) { fired.Add(1) },
	})
	for range 3 {
		if _, err := c.GetAgent(context.Background(), "", "agt_001"); err != nil {
			t.Fatalf("GetAgent: %v", err)
		}
	}
	if got := c.DeprecationNotices(); len(got) != 0 {
		t.Errorf("DeprecationNotices = %+v, want empty", got)
	}
	if fired.Load() != 0 {
		t.Errorf("OnDeprecation fired %d times, want 0", fired.Load())
	}
}

// TestDeprecationCallback verifies the hook fires exactly once per endpoint
// with the parsed notice AND with the originating request's context — that
// context is what lets the provider hand tflog a usable ctx.
func TestDeprecationCallback(t *testing.T) {
	srv := newDeprecatedServer(t)
	defer srv.Close()

	type ctxKey struct{}

	var (
		mu     sync.Mutex
		got    []Notice
		ctxVal any
	)
	c := New(Options{
		Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant,
		OnDeprecation: func(ctx context.Context, n Notice) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, n)
			ctxVal = ctx.Value(ctxKey{})
		},
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "carried-through")
	for range 2 { // second call must NOT re-fire
		if _, err := c.GetAgent(ctx, "", "agt_001"); err != nil {
			t.Fatalf("GetAgent: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("OnDeprecation fired %d times, want 1", len(got))
	}
	wantNotice := Notice{
		Method: http.MethodGet, Path: "/v1/agents/agt_001",
		Deprecation: testDeprecation, Sunset: testSunset, Link: testGuideURL,
	}
	if got[0] != wantNotice {
		t.Errorf("callback notice = %+v, want %+v", got[0], wantNotice)
	}
	if ctxVal != "carried-through" {
		t.Errorf("callback ctx value = %v, want the request context's value", ctxVal)
	}
}

// TestDeprecationConcurrentDedup hammers one endpoint from many goroutines:
// the dedup map must stay race-free (the gate runs -race) and still produce
// exactly one notice and one callback invocation.
func TestDeprecationConcurrentDedup(t *testing.T) {
	srv := newDeprecatedServer(t)
	defer srv.Close()

	var fired atomic.Int64
	c := New(Options{
		Endpoint: srv.URL, APIToken: testToken, Tenant: testTenant,
		OnDeprecation: func(context.Context, Notice) { fired.Add(1) },
	})

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if _, err := c.GetAgent(context.Background(), "", "agt_001"); err != nil {
				t.Errorf("GetAgent: %v", err)
			}
			// Reading concurrently with writers must also be race-free.
			_ = c.DeprecationNotices()
		})
	}
	wg.Wait()

	if got := c.DeprecationNotices(); len(got) != 1 {
		t.Errorf("DeprecationNotices len = %d, want 1", len(got))
	}
	if fired.Load() != 1 {
		t.Errorf("OnDeprecation fired %d times, want 1", fired.Load())
	}
}

// TestLinkByRel pins the small RFC 8288 parser against the forms the wild
// emits: multiple header lines, multi-link lines, rel lists, unquoted rel,
// case-insensitivity, and commas hiding inside targets or quoted params.
func TestLinkByRel(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single match", []string{`<https://x/guide>; rel="deprecation"`}, "https://x/guide"},
		{"no match", []string{`<https://x/s>; rel="sunset"`}, ""},
		{"second link in one line", []string{`<https://x/s>; rel="sunset", <https://x/guide>; rel="deprecation"`}, "https://x/guide"},
		{"second header line", []string{`<https://x/s>; rel="sunset"`, `<https://x/guide>; rel="deprecation"`}, "https://x/guide"},
		{"rel list", []string{`<https://x/guide>; rel="deprecation sunset"`}, "https://x/guide"},
		{"unquoted rel", []string{`<https://x/guide>; rel=deprecation`}, "https://x/guide"},
		{"case-insensitive", []string{`<https://x/guide>; REL="Deprecation"`}, "https://x/guide"},
		{"comma in target", []string{`<https://x/guide?a=1,2>; rel="deprecation"`}, "https://x/guide?a=1,2"},
		{"comma in quoted param", []string{`<https://x/s>; title="a, b"; rel="sunset", <https://x/guide>; rel="deprecation"`}, "https://x/guide"},
		{"rel substring does not match", []string{`<https://x/g>; rel="deprecation-policy"`}, ""},
		{"malformed without target", []string{`rel="deprecation"`}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := linkByRel(tc.in, "deprecation"); got != tc.want {
				t.Errorf("linkByRel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

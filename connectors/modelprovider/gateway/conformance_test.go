// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConformanceOpenAI(t *testing.T) { runConformance(t, ProtocolOpenAICompat) }

func TestConformanceAnthropic(t *testing.T) { runConformance(t, ProtocolAnthropic) }

func TestConformanceOllama(t *testing.T) { runConformance(t, ProtocolOllama) }

func runConformance(t *testing.T, proto Protocol) {
	t.Helper()
	t.Run("create_message", func(t *testing.T) {
		hook := &recordingHook{}
		srv := newFake(t, fakeCfg{protocol: proto})
		d := testDriver(t, proto, srv, hook)
		resp, err := d.CreateMessage(context.Background(), sampleReq())
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
		if resp.Text != "hello" {
			t.Fatalf("text = %q, want hello", resp.Text)
		}
		if resp.Usage.InputTokens != 3 || resp.Usage.OutputTokens != 2 {
			t.Fatalf("usage = %+v, want 3/2", resp.Usage)
		}
		if hook.reserves != 1 || hook.commits != 1 || hook.releases != 0 {
			t.Fatalf("hook reserves=%d commits=%d releases=%d", hook.reserves, hook.commits, hook.releases)
		}
	})

	t.Run("stream_text_and_usage", func(t *testing.T) {
		hook := &recordingHook{}
		srv := newFake(t, fakeCfg{protocol: proto})
		d := testDriver(t, proto, srv, hook)
		st, err := d.StreamMessage(context.Background(), sampleReq())
		if err != nil {
			t.Fatalf("StreamMessage: %v", err)
		}
		defer func() { _ = st.Close() }()
		var text string
		for {
			ev, err := st.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			text += ev.Text
		}
		if text != "hello" {
			t.Fatalf("stream text = %q, want hello", text)
		}
		u := st.Usage()
		if u.InputTokens != 3 || u.OutputTokens != 2 {
			t.Fatalf("stream usage = %+v, want 3/2", u)
		}
		if hook.commits != 1 {
			t.Fatalf("commits = %d, want 1", hook.commits)
		}
	})

	t.Run("backpressure_recv_blocks_until_write", func(t *testing.T) {
		write2 := make(chan struct{})
		var writes atomic.Int32
		srv := newFake(t, fakeCfg{protocol: proto, write2: write2, writes: &writes})
		d := testDriver(t, proto, srv, nil)
		st, err := d.StreamMessage(context.Background(), sampleReq())
		if err != nil {
			t.Fatalf("StreamMessage: %v", err)
		}
		defer func() { _ = st.Close() }()
		ev, err := st.Recv()
		if err != nil {
			t.Fatalf("first Recv: %v", err)
		}
		if ev.Text == "" && ev.Type != EventUsage && ev.Type != EventTextDelta {
			t.Fatalf("unexpected first event %+v", ev)
		}
		if writes.Load() > 1 {
			t.Fatalf("upstream writes after first Recv = %d, want 1 (driver must not prefetch)", writes.Load())
		}
		close(write2)
		var gotSecond bool
		for {
			ev, err := st.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if ev.Text != "" {
				gotSecond = true
			}
		}
		if !gotSecond {
			t.Fatal("expected a later text delta after releasing the upstream write")
		}
	})

	t.Run("http_errors_map", func(t *testing.T) {
		cases := []struct {
			status int
			code   string
		}{
			{http.StatusBadRequest, CodeBadRequest},
			{http.StatusUnauthorized, CodeUnauthenticated},
			{http.StatusTooManyRequests, CodeRateLimited},
			{http.StatusServiceUnavailable, CodeUnavailable},
		}
		for _, tc := range cases {
			srv := newFake(t, fakeCfg{protocol: proto, mode: fakeStatus, status: tc.status})
			d := testDriver(t, proto, srv, nil)
			_, err := d.CreateMessage(context.Background(), sampleReq())
			var ge *Error
			if !errors.As(err, &ge) {
				t.Fatalf("status %d: want *Error, got %T %v", tc.status, err, err)
			}
			if ge.Code != tc.code {
				t.Fatalf("status %d: code = %s, want %s", tc.status, ge.Code, tc.code)
			}
			if ge.HTTPStatus != tc.status {
				t.Fatalf("status %d: HTTPStatus = %d", tc.status, ge.HTTPStatus)
			}
		}
	})

	t.Run("budget_deny_skips_upstream", func(t *testing.T) {
		// The error code alone does not prove the SKIP: a driver that calls the
		// upstream first and reserves afterwards returns the same budget_denied.
		// Denial-of-Wallet is about the call that was never made, so the upstream
		// is counted.
		var requests atomic.Int32
		srv := newFake(t, fakeCfg{protocol: proto, requests: &requests})
		d := testDriver(t, proto, srv, denyHook{})
		_, err := d.CreateMessage(context.Background(), sampleReq())
		var ge *Error
		if !errors.As(err, &ge) || ge.Code != CodeBudgetDenied {
			t.Fatalf("want budget_denied, got %v", err)
		}
		if n := requests.Load(); n != 0 {
			t.Fatalf("upstream requests = %d, want 0: a denied budget must not reach the upstream", n)
		}
		st, err := d.StreamMessage(context.Background(), sampleReq())
		if !errors.As(err, &ge) || ge.Code != CodeBudgetDenied {
			t.Fatalf("stream: want budget_denied, got %v", err)
		}
		if st != nil {
			_ = st.Close()
		}
		if n := requests.Load(); n != 0 {
			t.Fatalf("upstream requests after StreamMessage = %d, want 0", n)
		}
	})

	t.Run("validate_request", func(t *testing.T) {
		srv := newFake(t, fakeCfg{protocol: proto})
		d := testDriver(t, proto, srv, nil)
		_, err := d.CreateMessage(context.Background(), MessageRequest{})
		var ge *Error
		if !errors.As(err, &ge) || ge.Code != CodeBadRequest {
			t.Fatalf("empty request: %v", err)
		}
	})
}

type recordingHook struct {
	mu                          sync.Mutex
	reserves, commits, releases int
	last                        Usage
}

func (h *recordingHook) Reserve(context.Context, Estimate) (Reservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reserves++
	return Reservation{Allowed: true, Handle: "h1"}, nil
}

func (h *recordingHook) Commit(_ context.Context, _ Reservation, actual Usage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commits++
	h.last = actual
	return nil
}

func (h *recordingHook) Release(context.Context, Reservation) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releases++
	return nil
}

type denyHook struct{}

func (denyHook) Reserve(context.Context, Estimate) (Reservation, error) {
	return Reservation{Allowed: false, Reason: "no headroom"}, nil
}
func (denyHook) Commit(context.Context, Reservation, Usage) error { return nil }
func (denyHook) Release(context.Context, Reservation) error       { return nil }

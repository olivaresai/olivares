// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubDoer returns queued responses (and errors) in order, recording the requests
// it saw so a test can assert the body/headers were resent on each attempt.
type stubDoer struct {
	responses []stubResp
	calls     int
	gotBodies []string
}

type stubResp struct {
	status     int
	body       string
	retryAfter string
	transport  error // when set, Do returns this instead of a response
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	i := d.calls
	d.calls++
	var bodyStr string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		bodyStr = string(b)
	}
	d.gotBodies = append(d.gotBodies, bodyStr)
	if i >= len(d.responses) {
		i = len(d.responses) - 1
	}
	r := d.responses[i]
	if r.transport != nil {
		return nil, r.transport
	}
	h := http.Header{}
	if r.retryAfter != "" {
		h.Set("Retry-After", r.retryAfter)
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     h,
	}, nil
}

// recordSleep is an injected Sleep that records every requested delay and never
// actually waits, so backoff schedules are asserted instantly.
func recordSleep(into *[]time.Duration) func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*into = append(*into, d)
		return nil
	}
}

func TestSendSuccessFirstTry(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 200, body: "ok"}}}
	c := New(doer, Options{})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y", Body: []byte("hi")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != 200 || res.Attempts != 1 {
		t.Fatalf("res = %+v, want 200/1 attempt", res)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1", doer.calls)
	}
}

func TestSendRetriesTransientThenSucceeds(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{
		{status: 503, body: "down"},
		{status: 429, body: "slow"},
		{status: 200, body: "ok"},
	}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 4, BaseDelay: time.Second, Sleep: recordSleep(&slept)})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y", Body: []byte("payload")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != 200 || res.Attempts != 3 {
		t.Fatalf("res = %+v, want 200/3 attempts", res)
	}
	// Two backoffs before attempts 2 and 3: exponential 1s, 2s (no jitter).
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != 2*time.Second {
		t.Fatalf("backoff schedule = %v, want [1s 2s]", slept)
	}
	// The body must be resent verbatim on every attempt.
	for i, b := range doer.gotBodies {
		if b != "payload" {
			t.Errorf("attempt %d body = %q, want payload", i+1, b)
		}
	}
}

func TestSendTerminalClientErrorNoRetry(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 400, body: "bad request"}}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 5, Sleep: recordSleep(&slept)})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y"})
	if err == nil {
		t.Fatal("expected a terminal error")
	}
	if res.StatusCode != 400 || res.Attempts != 1 {
		t.Fatalf("res = %+v, want 400/1 attempt", res)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", doer.calls)
	}
	if len(slept) != 0 {
		t.Fatalf("should not back off on a terminal error, slept %v", slept)
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error should carry status + excerpt: %v", err)
	}
}

func TestSendExhaustsAttempts(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 500, body: "boom"}}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, Sleep: recordSleep(&slept)})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y"})
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if res.Attempts != 3 || doer.calls != 3 {
		t.Fatalf("attempts = %d, calls = %d, want 3/3", res.Attempts, doer.calls)
	}
	// Backed off twice (before attempts 2 and 3), not after the last.
	if len(slept) != 2 {
		t.Fatalf("slept %v, want 2 backoffs", slept)
	}
}

func TestSendHonorsRetryAfterSeconds(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{
		{status: 429, retryAfter: "7"},
		{status: 200, body: "ok"},
	}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 2, BaseDelay: time.Second, Sleep: recordSleep(&slept)})
	if _, err := c.Send(context.Background(), Request{URL: "https://x/y"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("backoff = %v, want [7s] from Retry-After", slept)
	}
}

func TestSendRetryAfterCappedByMaxDelay(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{
		{status: 503, retryAfter: "3600"},
		{status: 200},
	}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 2, MaxDelay: 30 * time.Second, Sleep: recordSleep(&slept)})
	if _, err := c.Send(context.Background(), Request{URL: "https://x/y"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slept) != 1 || slept[0] != 30*time.Second {
		t.Fatalf("backoff = %v, want [30s] (Retry-After capped)", slept)
	}
}

func TestSendRetriesTransportError(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{
		{transport: errors.New("connection refused")},
		{status: 200, body: "ok"},
	}}
	var slept []time.Duration
	c := New(doer, Options{MaxAttempts: 2, BaseDelay: time.Second, Sleep: recordSleep(&slept)})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", res.Attempts)
	}
}

func TestSendPersistentTransportErrorReturnsZeroStatus(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{transport: errors.New("dns failure")}}}
	c := New(doer, Options{MaxAttempts: 2, Sleep: recordSleep(new([]time.Duration))})
	res, err := c.Send(context.Background(), Request{URL: "https://x/y"})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.StatusCode != 0 {
		t.Fatalf("StatusCode = %d, want 0 (no response received)", res.StatusCode)
	}
}

func TestSendContextCanceledDuringBackoff(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 503}, {status: 200}}}
	ctx, cancel := context.WithCancel(context.Background())
	// Sleep that cancels the context the first time it is asked to wait.
	c := New(doer, Options{MaxAttempts: 3, Sleep: func(_ context.Context, _ time.Duration) error {
		cancel()
		return context.Canceled
	}})
	_, err := c.Send(ctx, Request{URL: "https://x/y"})
	if err == nil || !strings.Contains(err.Error(), "backoff aborted") {
		t.Fatalf("err = %v, want backoff aborted", err)
	}
}

func TestSendContextAlreadyCanceled(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 200}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New(doer, Options{})
	_, err := c.Send(ctx, Request{URL: "https://x/y"})
	if err == nil {
		t.Fatal("expected error on pre-canceled context")
	}
	if doer.calls != 0 {
		t.Fatalf("calls = %d, want 0 (never attempted)", doer.calls)
	}
}

func TestSendJitterStaysInBounds(t *testing.T) {
	doer := &stubDoer{responses: []stubResp{{status: 500}, {status: 200}}}
	var slept []time.Duration
	c := New(doer, Options{
		MaxAttempts: 2,
		BaseDelay:   time.Second,
		Sleep:       recordSleep(&slept),
		Jitter:      func() float64 { return 1.0 }, // maximum jitter
	})
	if _, err := c.Send(context.Background(), Request{URL: "https://x/y"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With jitter clamped to <1 and shaving up to half, the 1s base lands in [0.5s,1s).
	if len(slept) != 1 || slept[0] < 500*time.Millisecond || slept[0] > time.Second {
		t.Fatalf("jittered backoff = %v, want within [500ms,1s]", slept)
	}
}

// TestSendErrorNeverLeaksURLSecret is the spine-level secret-safety guard: a
// webhook URL whose path/query carries the credential must never appear in a
// returned error, on any failure path (terminal status, transport error,
// backoff-abort).
func TestSendErrorNeverLeaksURLSecret(t *testing.T) {
	const secret = "T00000/B11111/XXXXSUPERSECRETTOKEN"
	webhookURL := "https://hooks.slack" + ".com/services/" + secret
	// net/http returns a *url.Error embedding the full request URL on a transport
	// failure — the realistic shape the transport must defend against.
	urlErr := &url.Error{Op: "Post", URL: webhookURL, Err: errors.New("connection refused")}
	cases := []struct {
		name string
		doer *stubDoer
	}{
		{"terminal-4xx", &stubDoer{responses: []stubResp{{status: 404, body: "no_service"}}}},
		{"transport-error", &stubDoer{responses: []stubResp{{transport: urlErr}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.doer, Options{MaxAttempts: 1})
			_, err := c.Send(context.Background(), Request{URL: webhookURL, Body: []byte(secret)})
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaked the URL secret: %v", err)
			}
			if !strings.Contains(err.Error(), "hooks.slack.com") {
				t.Errorf("error should still name the host for diagnostics: %v", err)
			}
		})
	}
}

// TestUnwrapURLErrorPreservesContextCause proves a context cancellation wrapped in
// a *url.Error still satisfies errors.Is after the URL is stripped.
func TestUnwrapURLErrorPreservesContextCause(t *testing.T) {
	ue := &url.Error{Op: "Post", URL: "https://host/secret", Err: context.Canceled}
	got := unwrapURLError(ue)
	if !errors.Is(got, context.Canceled) {
		t.Errorf("context cause not preserved: %v", got)
	}
	if strings.Contains(got.Error(), "secret") {
		t.Errorf("URL leaked through unwrap: %v", got)
	}
}

func TestSafeURL(t *testing.T) {
	cases := map[string]string{
		"https://hooks.slack" + ".com/services/T/B/SECRET": "https://hooks.slack.com",
		"https://u:p@host:8200/v1/secret?token=abc":        "https://host:8200",
		"not a url": "<redacted-url>",
		"":          "<redacted-url>",
	}
	for in, want := range cases {
		if got := safeURL(in); got != want {
			t.Errorf("safeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Errorf("seconds: got %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty: got %v", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("negative: got %v", got)
	}
	if got := parseRetryAfter("garbage"); got != 0 {
		t.Errorf("garbage: got %v", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("past date: got %v", got)
	}
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 {
		t.Errorf("future date: got %v, want > 0", got)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	retry := []int{408, 425, 429, 500, 502, 503, 504}
	for _, c := range retry {
		if !isRetryableStatus(c) {
			t.Errorf("status %d should be retryable", c)
		}
	}
	terminal := []int{400, 401, 403, 404, 405, 409, 422}
	for _, c := range terminal {
		if isRetryableStatus(c) {
			t.Errorf("status %d should be terminal", c)
		}
	}
}

// TestBodyCompletenessIsReported is the signal every logical-rejection parser
// depends on. Splunk HEC, Elasticsearch _bulk and OTLP all answer HTTP 200 while
// rejecting the payload, and each parser treats a body it cannot decode as "not
// one of ours, the 2xx stands". A body that was truncated at the excerpt budget,
// or whose read failed part way, decodes as garbage — so without this flag a
// REJECTION silently becomes a delivery success.
func TestBodyCompletenessIsReported(t *testing.T) {
	t.Run("a small body is complete", func(t *testing.T) {
		doer := &stubDoer{responses: []stubResp{{status: 200, body: `{"code":0}`}}}
		res, err := New(doer, Options{}).Send(context.Background(), Request{URL: "https://x/y"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.BodyComplete {
			t.Fatal("a body well under the budget must be reported complete")
		}
	})

	t.Run("a body exactly at the budget is complete", func(t *testing.T) {
		// The boundary is the case a naive implementation gets wrong: reading
		// exactly maxBodyExcerpt bytes is ambiguous unless one more byte is asked
		// for, and the ambiguity resolves toward "complete" — the unsafe direction.
		body := strings.Repeat("x", maxBodyExcerpt)
		doer := &stubDoer{responses: []stubResp{{status: 200, body: body}}}
		res, err := New(doer, Options{}).Send(context.Background(), Request{URL: "https://x/y"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.BodyComplete {
			t.Fatal("a body exactly at the budget was read whole and must be reported complete")
		}
		if len(res.RawBody) != maxBodyExcerpt {
			t.Fatalf("RawBody = %d bytes, want %d", len(res.RawBody), maxBodyExcerpt)
		}
	})

	t.Run("an oversized body is reported INCOMPLETE and stays bounded", func(t *testing.T) {
		body := strings.Repeat("y", maxBodyExcerpt+1)
		doer := &stubDoer{responses: []stubResp{{status: 200, body: body}}}
		res, err := New(doer, Options{}).Send(context.Background(), Request{URL: "https://x/y"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.BodyComplete {
			t.Fatal("a body past the budget must NOT be reported complete: a rejection larger than the excerpt would otherwise read as success")
		}
		if len(res.RawBody) != maxBodyExcerpt {
			t.Fatalf("RawBody = %d bytes, want the excerpt capped at %d", len(res.RawBody), maxBodyExcerpt)
		}
	})
}

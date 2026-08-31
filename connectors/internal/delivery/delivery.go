// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package delivery is the reliable-HTTP-delivery transport shared by the Olivares
// AI output connectors (Slack, Teams, PagerDuty, Opsgenie, webhook, SIEM). It
// turns one logical "deliver this body to this endpoint" into a bounded sequence
// of HTTP attempts with exponential backoff, jitter and honored Retry-After,
// retrying only the failures that are worth retrying (transient: network errors,
// 408/425/429 and 5xx) and giving up immediately on a terminal client error (a
// 4xx that will never succeed on retry).
//
// Where this sits in the SDK contract. sdk.OutputConnector.Notify documents that
// "retry, rate-limiting and idempotency policy are the engine's concern" — that
// is the DURABLE, cross-Notify concern (a dead-letter queue, replay after a long
// outage), which the runtime owns. This package is the WITHIN-Notify concern: a
// single delivery that rides over a momentary 503 or a 429 with a Retry-After,
// the right behavior for any production HTTP integration. A connector calls
// Send inside Notify and reports the final outcome to the engine; the two layers
// compose, they do not overlap.
//
// It is stdlib-only (no third-party dependency) and never logs request or
// response bodies or headers — a body may carry an alerting payload and a header
// carries the operator's bearer token. Diagnostics surface the status code and a
// bounded, body excerpt only, and the credential never appears in an error.
package delivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Doer is the minimal HTTP capability the transport needs. *http.Client
// satisfies it; a test injects a stub returning recorded responses so no live
// network call is made and the backoff schedule is asserted deterministically.
type Doer interface {
	// Do issues req and returns the response, exactly like http.Client.Do.
	Do(req *http.Request) (*http.Response, error)
}

// maxBodyExcerpt bounds how much of a response body is read for diagnostics. A
// destination's error message ("invalid routing key") is small; this is a
// defensive cap, and the excerpt is only ever used in an error string.
const maxBodyExcerpt = 2 << 10 // 2 KiB

// Default backoff parameters. They are deliberately modest: an output connector
// must not hold the runtime's delivery goroutine for minutes. The runtime owns
// long-horizon retry; this is the short, in-call resilience layer.
const (
	defaultMaxAttempts = 4
	defaultBaseDelay   = 500 * time.Millisecond
	defaultMaxDelay    = 30 * time.Second
)

// Options tunes the delivery policy. The zero value is usable: every field falls
// back to a sane default, so delivery.New(doer, Options{}) is valid.
type Options struct {
	// MaxAttempts is the total number of attempts including the first (so 1 means
	// "no retry"). Values < 1 fall back to the default.
	MaxAttempts int
	// BaseDelay is the backoff before the second attempt; it doubles each attempt
	// up to MaxDelay. Values <= 0 fall back to the default.
	BaseDelay time.Duration
	// MaxDelay caps the per-attempt backoff. Values <= 0 fall back to the default.
	MaxDelay time.Duration
	// Sleep waits for d or until ctx is done, returning ctx.Err() if canceled. It
	// is injectable so a test asserts the backoff schedule without real waiting.
	// nil uses a context-honoring timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns a fraction in [0,1) used to randomize each backoff (full
	// jitter: actual = base * (1 - Jitter()*0.5), so a thundering herd of
	// retries de-syncs). nil disables jitter (deterministic backoff), which is
	// also what tests want; production callers pass a randomized source.
	Jitter func() float64
	// Retryable overrides which HTTP statuses this client retries INTERNALLY.
	//
	// It exists because the default set is a reasonable HTTP heuristic, not a
	// protocol contract, and some protocols narrow it. OTLP names 429, 502, 503 and
	// 504 as the retryable statuses and says the client MUST NOT retry the same
	// telemetry data on any other failure — so leaving the default in place meant a
	// 500 was re-sent several times here, inside one delivery, BEFORE any
	// classification could call it terminal. The engine's later decision cannot undo
	// requests that already left.
	//
	// nil keeps the default heuristic.
	Retryable func(status int) bool
}

// retryable reports whether a status is worth retrying, honoring an override.
func (o Options) retryable(status int) bool {
	if o.Retryable != nil {
		return o.Retryable(status)
	}
	return isRetryableStatus(status)
}

func (o Options) maxAttempts() int {
	if o.MaxAttempts < 1 {
		return defaultMaxAttempts
	}
	return o.MaxAttempts
}

func (o Options) baseDelay() time.Duration {
	if o.BaseDelay <= 0 {
		return defaultBaseDelay
	}
	return o.BaseDelay
}

func (o Options) maxDelay() time.Duration {
	if o.MaxDelay <= 0 {
		return defaultMaxDelay
	}
	return o.MaxDelay
}

// Client is a reusable reliable-delivery transport. It holds no per-request
// state, so one Client is shared across an output connector's lifetime and is
// safe for concurrent use if its Doer is.
type Client struct {
	doer Doer
	opts Options
}

// New builds a delivery client over doer with the given policy. A nil doer uses
// http.DefaultClient.
func New(doer Doer, opts Options) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Client{doer: doer, opts: opts}
}

// Result is the outcome of a delivery: the final response's status code, a
// bounded body excerpt (for diagnostics — never logged automatically) and how
// many attempts were made. It is returned alongside a nil error on success and
// alongside a non-nil error when delivery ultimately failed but a response was
// received (so the caller can inspect the destination's complaint).
type Result struct {
	// StatusCode is the HTTP status of the final attempt (0 if no response was
	// ever received, e.g. a persistent network error).
	StatusCode int
	// Body is a bounded excerpt of the final response body for diagnostics.
	Body string
	// RawBody is the same bounded excerpt as Body but untrimmed bytes, so a caller
	// whose destination answers in a binary encoding (e.g. an OTLP/protobuf
	// ExportLogsServiceResponse) can decode the response. It is the verbatim bytes
	// of the excerpt (capped at maxBodyExcerpt); nil when no response body was read.
	RawBody []byte
	// BodyComplete reports whether RawBody is the WHOLE response body. It is false
	// when the body was longer than maxBodyExcerpt or when reading it failed part
	// way through.
	//
	// Callers that interpret the body MUST consult it. Every destination this
	// package serves can answer HTTP 200 while logically rejecting the payload
	// (Splunk HEC's non-zero code, Elasticsearch's errors:true, OTLP's
	// partial_success), and each of those parsers treats a body it cannot decode as
	// "not one of ours, the 2xx stands". A silently truncated or partially read
	// body decodes as garbage and therefore turns a REJECTION into a delivery
	// success — the one failure mode an evidence pipeline must not have. Reporting
	// the truncation lets the caller fail closed on an answer it cannot actually
	// read, instead of inferring consent from unreadable bytes.
	BodyComplete bool
	// BodyErr is the error that ended the body read, if any. It is diagnostic: the
	// authoritative signal for a caller is BodyComplete.
	BodyErr error
	// Attempts is the number of HTTP attempts made (>= 1).
	Attempts int
}

// Request describes one logical delivery. Method defaults to POST when empty.
type Request struct {
	// Method is the HTTP method; empty means POST.
	Method string
	// URL is the absolute destination URL.
	URL string
	// Header is the set of request headers (auth, content-type). It is sent as-is
	// and never logged.
	Header map[string]string
	// Body is the request body, resent verbatim on each attempt.
	Body []byte
}

// Send delivers req, retrying transient failures with backoff until it succeeds,
// exhausts MaxAttempts, or ctx is canceled. On a 2xx it returns (Result, nil).
// On a terminal (non-retryable) status it returns (Result, error) immediately. On
// exhausting retries it returns the last (Result, error). A request that never
// got a response (network error every attempt) returns (Result{StatusCode:0},
// error). It honors ctx between and during attempts and never logs the body.
func (c *Client) Send(ctx context.Context, req Request) (Result, error) {
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	attempts := c.opts.maxAttempts()

	var last Result
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			last.Attempts = attempt - 1
			return last, fmt.Errorf("delivery: %s %s aborted: %w", method, safeURL(req.URL), err)
		}

		res, retryAfter, err := c.attempt(ctx, method, req)
		res.Attempts = attempt
		last = res.Result

		if err == nil {
			return res.Result, nil // 2xx
		}
		lastErr = err

		// A terminal (non-retryable) outcome: stop now, even with attempts left.
		if !res.retryable {
			return res.Result, err
		}
		// Out of attempts: return the last failure.
		if attempt == attempts {
			return res.Result, err
		}
		// Back off before the next attempt, preferring a server-provided Retry-After.
		delay := c.backoff(attempt, retryAfter)
		if err := c.sleep(ctx, delay); err != nil {
			return res.Result, fmt.Errorf("delivery: %s %s backoff aborted: %w", method, safeURL(req.URL), err)
		}
	}
	return last, lastErr
}

// attemptResult carries the per-attempt classification alongside the public Result.
type attemptResult struct {
	Result
	retryable bool
}

// attempt performs one HTTP attempt and classifies the outcome. It returns the
// result, any server-advised Retry-After delay, and a non-nil error when the
// attempt did not succeed (2xx). A network/transport error is retryable; an HTTP
// status is retryable per isRetryableStatus.
func (c *Client) attempt(ctx context.Context, method string, req Request) (attemptResult, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		// A malformed URL/method is terminal: retrying cannot fix it.
		return attemptResult{retryable: false}, 0, fmt.Errorf("delivery: build request: %w", err)
	}
	for k, v := range req.Header {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.doer.Do(httpReq)
	if err != nil {
		// Transport error (DNS, connection refused, timeout): transient, retryable.
		// net/http wraps the cause in a *url.Error whose Error() embeds the FULL
		// request URL — secret-bearing for a webhook — so we unwrap to the cause
		// before wrapping, keeping errors.Is (e.g. context.Canceled) working while
		// never echoing the URL's path/query.
		return attemptResult{retryable: true}, 0, fmt.Errorf("delivery: %s %s: %w", method, safeURL(req.URL), unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read ONE byte past the budget so a body that exactly fills it can be told
	// apart from one that overflows it. Without the extra byte, "read exactly
	// maxBodyExcerpt" is ambiguous between a complete body and a truncated one, and
	// the ambiguity resolves toward "complete" — which is the unsafe direction.
	excerpt, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyExcerpt+1))
	complete := readErr == nil && len(excerpt) <= maxBodyExcerpt
	if len(excerpt) > maxBodyExcerpt {
		excerpt = excerpt[:maxBodyExcerpt]
	}
	// Drain the remainder so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	r := attemptResult{Result: Result{
		StatusCode:   resp.StatusCode,
		Body:         strings.TrimSpace(string(excerpt)),
		RawBody:      excerpt,
		BodyComplete: complete,
		BodyErr:      readErr,
	}}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return r, 0, nil
	}

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	r.retryable = c.opts.retryable(resp.StatusCode)
	return r, retryAfter, fmt.Errorf("delivery: %s %s: status %d: %s", method, safeURL(req.URL), resp.StatusCode, r.Body)
}

// backoff returns the delay before the attempt after `attempt` (1-based). A
// server-provided Retry-After wins when it is positive and within MaxDelay's
// order of magnitude; otherwise it is exponential (base * 2^(attempt-1)) capped
// at MaxDelay, with optional jitter.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	maxD := c.opts.maxDelay()
	if retryAfter > 0 {
		if retryAfter > maxD {
			return maxD
		}
		return retryAfter
	}
	base := c.opts.baseDelay()
	// base * 2^(attempt-1) using float to avoid overflow, then cap.
	d := float64(base) * math.Pow(2, float64(attempt-1))
	if d > float64(maxD) {
		d = float64(maxD)
	}
	delay := time.Duration(d)
	if c.opts.Jitter != nil {
		// Full-ish jitter: shave up to half the delay so retriers de-synchronize.
		j := c.opts.Jitter()
		if j < 0 {
			j = 0
		}
		if j >= 1 {
			j = 0.999
		}
		delay = time.Duration(float64(delay) * (1 - j*0.5))
	}
	return delay
}

// sleep waits using the injected Sleep or a context-honoring timer.
func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.opts.Sleep != nil {
		return c.opts.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// safeURL reduces a request URL to scheme://host for use in an error message. A
// webhook URL's path or query routinely carries the operator's secret — a Slack
// or Microsoft Teams incoming-webhook URL embeds the token in the path — so the
// transport never puts the full URL in an error; only the non-sensitive scheme
// and host. An unparseable or hostless URL degrades to a fixed placeholder rather
// than echoing raw bytes that might be the secret. This makes the transport
// secret-safe by default for every output connector, not only the ones that
// remember to avoid wrapping the error.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<redacted-url>"
	}
	return u.Scheme + "://" + u.Host
}

// unwrapURLError returns the cause inside a *url.Error (net/http's transport
// error), which carries the failure reason WITHOUT the request URL. A non-url
// error is returned unchanged. errors.As walks the chain, so a context.Canceled
// or context.DeadlineExceeded cause is preserved for the caller's errors.Is.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// isRetryableStatus reports whether an HTTP status is worth retrying. 408
// (request timeout), 425 (too early) and 429 (rate limited) are transient client
// signals; every 5xx is a server-side transient. Every other 4xx is terminal —
// a misconfigured routing key or a bad payload will fail identically on retry, so
// retrying only wastes the runtime's delivery goroutine and the destination's
// rate budget.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code >= 500 && code <= 599
}

// parseRetryAfter parses a Retry-After header value, which is either a number of
// seconds or an HTTP-date. It returns 0 for an absent/unparseable value or a date
// in the past. The result is clamped to non-negative.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

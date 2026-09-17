// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ChatTransportErrorKind classifies a failure without retaining upstream data.
type ChatTransportErrorKind string

const (
	ChatTransportInvalidConfig  ChatTransportErrorKind = "invalid_config"
	ChatTransportInvalidRequest ChatTransportErrorKind = "invalid_request"
	ChatTransportCanceled       ChatTransportErrorKind = "canceled"
	ChatTransportTimeout        ChatTransportErrorKind = "timeout"
	ChatTransportHTTP           ChatTransportErrorKind = "http_status"
	ChatTransportNetwork        ChatTransportErrorKind = "transport"
	ChatTransportRead           ChatTransportErrorKind = "response_read"
	ChatTransportLimit          ChatTransportErrorKind = "size_limit"
	ChatTransportProtocol       ChatTransportErrorKind = "protocol"
)

// ChatTransportError contains no endpoint, credentials, body or underlying error.
// Attempted means Client.Do was invoked, NOT that remote receipt is known. After
// that boundary a failure may have incurred an effect or usage; it is never a
// reason to infer zero cost, release a reservation or automatically retry.
// StatusCode is zero when unavailable. RetryAfter is an optional validated 429
// hint, between zero and 24 hours; its presence does not authorize a retry.
// CodecKind identifies a safe codec category on protocol failures.
type ChatTransportError struct {
	Kind          ChatTransportErrorKind
	Attempted     bool
	StatusCode    int
	RetryAfter    time.Duration
	HasRetryAfter bool
	CodecKind     ChatTextErrorKind
}

func (e *ChatTransportError) Error() string {
	return "modelprovider: chat transport: " + string(e.Kind)
}

// ChatTextTransportConfig fixes one destination and explicit authentication.
// Endpoint is the FULL URL: no path is appended, inferred or rewritten. HTTPS is
// required unless AllowHTTP explicitly permits HTTP (including authorized LAN
// and self-hosted endpoints). Relative URLs, userinfo, query and fragments fail.
// This layer has no arbitrary-header, incoming-credential or inventory fallback.
// AuthBearer requires a nonempty HTTP bearer token; resolve its secret reference
// outside this layer. AuthNone requires an empty token and is an explicit option
// for authorized endpoints without authentication, never a fallback. Omitted and
// other schemes fail. MaxRequestBytes and MaxResponseBytes are positive bounds
// on the prepared JSON and complete response respectively. Timeout is positive.
type ChatTextTransportConfig struct {
	Endpoint         string
	AuthScheme       AuthScheme
	BearerToken      string
	AllowHTTP        bool
	MaxRequestBytes  int
	MaxResponseBytes int
	Timeout          time.Duration
	// HTTPClient permits enterprise TLS/proxy configuration. Its value is copied;
	// redirects and its cookie jar are disabled only in that private copy. The
	// supplied Transport remains shared and must be safe for concurrent use and
	// honor request context. Do not mutate it while this transport is in use.
	HTTPClient *http.Client
}

// ChatTextTransport sends prepared text.v1 requests. It does not authenticate the
// Olivares caller, enforce authorization/policy/residency/budget, or prove the
// upstream gateway's route, model identity or number of remote computations.
// Each invocation calls Do at most once, supplies no GetBody or replay headers,
// and follows no redirects. A custom RoundTripper may itself perform additional
// I/O: callers must qualify that implementation separately. No goroutine tries
// to force a timeout on a custom Transport or Body that ignores context.
type ChatTextTransport struct {
	endpoint         string
	auth             AuthScheme
	bearer           string
	maxRequestBytes  int
	maxResponseBytes int
	timeout          time.Duration
	client           http.Client
}

// NewChatTextTransport copies configuration and returns no transport on invalid
// input. It performs no I/O. A nil client (or nil client Transport) gets a private
// standard transport, independent of mutable http.DefaultClient/DefaultTransport.
func NewChatTextTransport(cfg ChatTextTransportConfig) (*ChatTextTransport, error) {
	authValid := cfg.AuthScheme == AuthBearer && chatBearerValid(cfg.BearerToken) ||
		cfg.AuthScheme == AuthNone && cfg.BearerToken == ""
	if !chatEndpointValid(cfg.Endpoint, cfg.AllowHTTP) || !authValid ||
		cfg.MaxRequestBytes <= 0 || cfg.MaxResponseBytes <= 0 ||
		int64(cfg.MaxResponseBytes) == math.MaxInt64 || cfg.Timeout <= 0 {
		return nil, &ChatTransportError{Kind: ChatTransportInvalidConfig}
	}
	var client http.Client
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	if client.Timeout < 0 {
		return nil, &ChatTransportError{Kind: ChatTransportInvalidConfig}
	}
	if client.Transport == nil {
		client.Transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ChatTextTransport{
		endpoint: cfg.Endpoint, auth: cfg.AuthScheme, bearer: cfg.BearerToken,
		maxRequestBytes: cfg.MaxRequestBytes, maxResponseBytes: cfg.MaxResponseBytes,
		timeout: cfg.Timeout, client: client,
	}, nil
}

func chatEndpointValid(endpoint string, allowHTTP bool) bool {
	if endpoint == "" || strings.ContainsAny(endpoint, "?#") {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		(u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// RFC bearer token alphabet; padding may occur only at the end. Header control
// bytes, whitespace and non-ASCII credentials are rejected before any dispatch.
func chatBearerValid(token string) bool {
	if token == "" {
		return false
	}
	padding, content := false, false
	for _, c := range token {
		if c == '=' {
			padding = true
			continue
		}
		if padding || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || strings.ContainsRune("-._~+/", c)) {
			return false
		}
		content = true
	}
	return content
}

// Execute sends the exact bytes whose digest the prepared request exposes. It
// bounds response reads to limit+1 and never returns a partial result on error.
// The caller's earlier deadline/cancellation and any shorter HTTPClient.Timeout
// prevail. A response fully read and decoded successfully is not discarded just
// because the context was canceled after its completion. Refusal and unknown
// usage retain the codec's semantics; neither implies zero remote cost.
func (t *ChatTextTransport) Execute(ctx context.Context, prepared PreparedChatTextRequest) (ChatTextResponse, error) {
	if t == nil || t.timeout <= 0 {
		return ChatTextResponse{}, &ChatTransportError{Kind: ChatTransportInvalidConfig}
	}
	if ctx == nil || prepared.IsZero() {
		return ChatTextResponse{}, &ChatTransportError{Kind: ChatTransportInvalidRequest}
	}
	if len(prepared.body) > t.maxRequestBytes {
		return ChatTextResponse{}, &ChatTransportError{Kind: ChatTransportLimit}
	}
	if err := ctx.Err(); err != nil {
		return ChatTextResponse{}, chatTransportFailure(ctx, err, ChatTransportNetwork, false, 0)
	}
	requestCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	// NopCloser prevents NewRequest from synthesizing a replayable GetBody from
	// strings.Reader. ContentLength is explicit and nonzero for a prepared value.
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, t.endpoint, io.NopCloser(strings.NewReader(prepared.body)))
	if err != nil {
		return ChatTextResponse{}, &ChatTransportError{Kind: ChatTransportInvalidConfig}
	}
	req.ContentLength = int64(len(prepared.body))
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if t.auth == AuthBearer {
		req.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	if err := requestCtx.Err(); err != nil {
		return ChatTextResponse{}, chatTransportFailure(requestCtx, err, ChatTransportNetwork, false, 0)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return ChatTextResponse{}, chatTransportFailure(requestCtx, err, ChatTransportNetwork, true, 0)
	}
	// Close every response, including statuses whose body is deliberately not read.
	// Closing is cleanup: a late cancel/close error does not erase a fully read result.
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure := &ChatTransportError{Kind: ChatTransportHTTP, Attempted: true, StatusCode: resp.StatusCode}
		if resp.StatusCode == http.StatusTooManyRequests {
			if values := resp.Header.Values("Retry-After"); len(values) == 1 {
				failure.RetryAfter, failure.HasRetryAfter = chatRetryAfter(values[0], time.Now())
			}
		}
		return ChatTextResponse{}, failure
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxResponseBytes)+1))
	if err != nil {
		return ChatTextResponse{}, chatTransportFailure(requestCtx, err, ChatTransportRead, true, resp.StatusCode)
	}
	if len(body) > t.maxResponseBytes {
		return ChatTextResponse{}, &ChatTransportError{Kind: ChatTransportLimit, Attempted: true, StatusCode: resp.StatusCode}
	}
	result, err := DecodeChatTextResponse(body, t.maxResponseBytes)
	if err != nil {
		failure := &ChatTransportError{Kind: ChatTransportProtocol, Attempted: true, StatusCode: resp.StatusCode}
		var codec *ChatTextCodecError
		if errors.As(err, &codec) {
			failure.CodecKind = codec.Kind
		}
		return ChatTextResponse{}, failure
	}
	return result, nil
}

func chatTransportFailure(ctx context.Context, err error, fallback ChatTransportErrorKind, attempted bool, status int) *ChatTransportError {
	kind := fallback
	var timeout interface{ Timeout() bool }
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		kind = ChatTransportTimeout
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		kind = ChatTransportCanceled
	case errors.As(err, &timeout) && timeout.Timeout():
		kind = ChatTransportTimeout
	}
	return &ChatTransportError{Kind: kind, Attempted: attempted, StatusCode: status}
}

func chatRetryAfter(value string, now time.Time) (time.Duration, bool) {
	const maximum = 24 * time.Hour
	if value == "" || len(value) > 64 {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		if seconds > uint64(maximum/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	at, err := http.ParseTime(value)
	if err != nil || at.After(now.Add(maximum)) {
		return 0, false
	}
	if !at.After(now) {
		return 0, true
	}
	return at.Sub(now), true
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package olivares

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version is the SDK's own semantic version. Pre-1.0 while the product is
// pre-1.0 (the policy's support windows bind from GA); from GA on, the MAJOR
// tracks the API contract major (APIVersion, version.gen.go).
const Version = "0.1.0"

// Client is a control-plane API client. Build it with New; the generated
// operation layer (operations.gen.go) adds one method per published operation.
type Client struct {
	baseURL    string // normalised: no trailing slash
	token      string
	tenant     string // default X-Olivares-Tenant; override per call with Tenant
	hc         *http.Client
	userAgent  string
	maxRetries int

	onDeprecation func(DeprecationNotice)
	depSeen       sync.Map // "METHOD path" → struct{}{} — one notice per endpoint

	// sleep is the retry wait seam (tests inject a recorder; honors ctx).
	sleep func(ctx context.Context, d time.Duration) error
}

// Option configures a Client at construction.
type Option func(*Client)

// WithTenant sets the default tenant for every call (X-Olivares-Tenant). A
// bound API token does not need one; multi-tenant principals do.
func WithTenant(tenant string) Option { return func(c *Client) { c.tenant = tenant } }

// WithHTTPClient replaces the transport (custom TLS — e.g. the engine's
// self-signed default certificate — proxies, timeouts).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithMaxRetries caps the automatic retries for retryable statuses (429
// always, 503 for GET). 0 disables retrying. Default 2.
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithDeprecationHandler replaces the default deprecation-signal handler (a
// slog warning). The handler runs at most once per METHOD+path per Client.
func WithDeprecationHandler(fn func(DeprecationNotice)) Option {
	return func(c *Client) { c.onDeprecation = fn }
}

// WithUserAgent prefixes the User-Agent (the SDK identity stays appended, so
// the server can always attribute the client version).
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua + " " + c.userAgent }
}

// New builds a Client for endpoint (e.g. "https://olivares.example:8443")
// authenticating with an opaque bearer token (olvs_ session or olvk_ API key;
// empty for the unauthenticated surface only).
func New(endpoint, token string, opts ...Option) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("olivares: endpoint must be an absolute URL: %q", endpoint)
	}
	c := &Client{
		baseURL:    strings.TrimSuffix(u.String(), "/"),
		token:      token,
		hc:         &http.Client{Timeout: 30 * time.Second},
		userAgent:  "olivares-client-go/" + Version + " (api " + APIVersion + ")",
		maxRetries: 2,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
	for _, o := range opts {
		o(c)
	}
	if c.onDeprecation == nil {
		c.onDeprecation = func(n DeprecationNotice) {
			slog.Warn("olivares: call to a deprecated API endpoint",
				"method", n.Method, "path", n.Path,
				"deprecation", n.Deprecation, "sunset", n.Sunset, "guide", n.Link)
		}
	}
	return c, nil
}

// APIError is the API's single error envelope ({"error":{code,message}})
// plus transport context. Match with errors.As; Code values are part of the
// stable contract (e.g. "not_found", "forbidden", "rate_limited").
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string // X-Request-ID, for support correlation
}

func (e *APIError) Error() string {
	return fmt.Sprintf("olivares: %s (%s %d, request %s)", e.Message, e.Code, e.Status, e.RequestID)
}

// DeprecationNotice is one deprecated-endpoint signal, parsed from the
// stability policy's response headers (RFC 9745 Deprecation, RFC 8594 Sunset,
// Link rel="deprecation").
type DeprecationNotice struct {
	Method      string
	Path        string // the request path (concrete, not the route template)
	Deprecation string // raw Deprecation value, e.g. "@1780272000"
	Sunset      string // raw Sunset value (HTTP-date), "" if not yet scheduled
	Link        string // migration-guide URL from Link rel="deprecation", if any
}

// RequestOption customizes one call.
type RequestOption func(*requestOptions)

type requestOptions struct {
	query  url.Values
	tenant string
}

// Query adds one query parameter.
func Query(key, value string) RequestOption {
	return func(o *requestOptions) {
		if o.query == nil {
			o.query = url.Values{}
		}
		o.query.Add(key, value)
	}
}

// Tenant overrides the client's default tenant for one call.
func Tenant(tenant string) RequestOption {
	return func(o *requestOptions) { o.tenant = tenant }
}

// pathEscape escapes one path segment (used by the generated operation layer).
func pathEscape(s string) string { return url.PathEscape(s) }

// do executes one JSON operation. route is the spec path template (e.g.
// /v1/agents/{id}) — the deprecation-dedup key; path is the concrete escaped
// request path.
func (c *Client) do(ctx context.Context, method, route, path string, body any, opts ...RequestOption) (map[string]any, error) {
	return c.decodeJSON(ctx, method, route, path, body, "", opts...)
}

// doJSONRequired executes an operation whose requestBody is required. A nil
// input is therefore the present JSON value null, not an omitted request body.
// The legacy do seam keeps its historical nil-means-absent behavior.
func (c *Client) doJSONRequired(ctx context.Context, method, route, path string, body any, opts ...RequestOption) (map[string]any, error) {
	if body == nil {
		body = json.RawMessage("null")
	}
	return c.decodeJSON(ctx, method, route, path, body, "", opts...)
}

// doReqRaw is the compatibility seam for operation layers generated before
// request media types were preserved. Those operations were octet-stream only.
func (c *Client) doReqRaw(ctx context.Context, method, route, path string, body []byte, opts ...RequestOption) (map[string]any, error) {
	return c.doReqRawWithType(
		ctx, method, route, path, body, "application/octet-stream", opts...,
	)
}

// doReqRawWithType sends raw request bytes under the exact media type declared
// by the operation's OpenAPI requestBody. The success body remains JSON.
func (c *Client) doReqRawWithType(
	ctx context.Context,
	method, route, path string,
	body []byte,
	contentType string,
	opts ...RequestOption,
) (map[string]any, error) {
	var payload any
	if body != nil {
		payload = body
	}
	return c.decodeJSON(ctx, method, route, path, payload, contentType, opts...)
}

// decodeJSON runs the exchange and decodes the JSON-envelope success body.
func (c *Client) decodeJSON(ctx context.Context, method, route, path string, body any, rawReqContentType string, opts ...RequestOption) (map[string]any, error) {
	raw, err := c.execute(ctx, method, route, path, body, true, rawReqContentType, opts...)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("olivares: response is not a JSON object (%s %s): %w", method, path, err)
	}
	return out, nil
}

// doRaw executes an operation whose success body is NOT JSON (the published
// contract declares e.g. text/plain for /metrics and the audit export); errors
// still arrive in the JSON envelope and map to *APIError.
func (c *Client) doRaw(ctx context.Context, method, route, path string, opts ...RequestOption) ([]byte, error) {
	return c.execute(ctx, method, route, path, nil, false, "", opts...)
}

// isRetryableStatus is that policy as one predicate: 429 for any method, 503 for GET
// only. Named rather than inlined as a negated disjunction (staticcheck QF1001)
// because the METHOD half is what keeps a retry from replaying a non-idempotent
// write, and that is not a term to leave inside a boolean nobody can read.
func isRetryableStatus(status int, method string) bool {
	return status == http.StatusTooManyRequests ||
		(status == http.StatusServiceUnavailable && method == http.MethodGet)
}

// execute is the policy-aware retry loop: 429 is always retryable (the
// limiter rejects before execution and Retry-After is a safe lower bound), 503
// only for GET (not_leader HA handoff — idempotent reads only). Everything
// else surfaces immediately.
func (c *Client) execute(ctx context.Context, method, route, path string, body any, wantJSON bool, rawReqContentType string, opts ...RequestOption) ([]byte, error) {
	var ro requestOptions
	for _, o := range opts {
		o(&ro)
	}
	for attempt := 0; ; attempt++ {
		res, retryAfter, err := c.once(ctx, method, route, path, body, wantJSON, rawReqContentType, ro)
		if err == nil {
			return res, nil
		}
		var ae *APIError
		if !errors.As(err, &ae) || attempt >= c.maxRetries {
			return nil, err
		}
		if !isRetryableStatus(ae.Status, method) {
			return nil, err
		}
		if retryAfter <= 0 {
			retryAfter = time.Duration(attempt+1) * 500 * time.Millisecond
		}
		if err := c.sleep(ctx, retryAfter); err != nil {
			return nil, err
		}
	}
}

// once performs a single HTTP exchange and returns the raw success body. When
// rawReqContentType is set the body is sent as-is under that exact media type;
// otherwise it is JSON-encoded.
func (c *Client) once(ctx context.Context, method, route, path string, body any, wantJSON bool, rawReqContentType string, ro requestOptions) ([]byte, time.Duration, error) {
	full := c.baseURL + path
	if len(ro.query) > 0 {
		full += "?" + ro.query.Encode()
	}
	var rdr io.Reader
	if rawReqContentType != "" {
		if b, ok := body.([]byte); ok && b != nil {
			rdr = bytes.NewReader(b)
		}
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("olivares: encode request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if wantJSON {
		req.Header.Set("Accept", "application/json")
	}
	if rawReqContentType != "" && body != nil {
		req.Header.Set("Content-Type", rawReqContentType)
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if t := cmpOr(ro.tenant, c.tenant); t != "" {
		req.Header.Set("X-Olivares-Tenant", t)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	c.noticeDeprecation(method, route, path, resp)

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, 0, nil
	}
	return nil, retryAfterOf(resp), apiError(resp, raw)
}

// cmpOr returns a if non-empty, else b.
func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// apiError maps a non-2xx response to APIError, tolerating non-envelope bodies.
func apiError(resp *http.Response, raw []byte) *APIError {
	ae := &APIError{
		Status:    resp.StatusCode,
		Code:      "http_" + strconv.Itoa(resp.StatusCode),
		Message:   strings.TrimSpace(string(raw)),
		RequestID: resp.Header.Get("X-Request-ID"),
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Code != "" {
		ae.Code, ae.Message = envelope.Error.Code, envelope.Error.Message
	}
	return ae
}

// retryAfterOf parses Retry-After delta-seconds (the only form the engine
// emits); 0 means "none advertised".
func retryAfterOf(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// noticeDeprecation surfaces the stability policy's deprecation headers, once
// per ENDPOINT per Client: the dedup key is the route template (matching the
// server-side declaration), so a deprecated /v1/agents/{id} warns once, not
// once per agent — and the dedup map stays bounded by the published surface.
func (c *Client) noticeDeprecation(method, route, path string, resp *http.Response) {
	dep := resp.Header.Get("Deprecation")
	if dep == "" {
		return
	}
	if _, dup := c.depSeen.LoadOrStore(method+" "+route, struct{}{}); dup {
		return
	}
	c.onDeprecation(DeprecationNotice{
		Method:      method,
		Path:        path,
		Deprecation: dep,
		Sunset:      resp.Header.Get("Sunset"),
		Link:        deprecationLink(resp.Header.Values("Link")),
	})
}

// deprecationLink extracts the rel="deprecation" target from Link headers.
// Each value may itself be a comma-separated list (proxies coalesce repeated
// headers), so split before matching.
func deprecationLink(links []string) string {
	for _, l := range links {
		for _, part := range strings.Split(l, ",") {
			if !strings.Contains(part, `rel="deprecation"`) {
				continue
			}
			if i, j := strings.IndexByte(part, '<'), strings.IndexByte(part, '>'); i >= 0 && j > i {
				return part[i+1 : j]
			}
		}
	}
	return ""
}

// ListPages iterates a cursor-paginated collection endpoint (the
// items/cursor/has_more envelope), yielding each item until exhaustion or the
// first error. Query/tenant options apply to every page request.
func (c *Client) ListPages(ctx context.Context, path string, opts ...RequestOption) iter.Seq2[map[string]any, error] {
	return func(yield func(map[string]any, error) bool) {
		cursor := ""
		for {
			pageOpts := opts
			if cursor != "" {
				pageOpts = append(append([]RequestOption{}, opts...), Query("cursor", cursor))
			}
			page, err := c.do(ctx, http.MethodGet, path, path, nil, pageOpts...)
			if err != nil {
				yield(nil, err)
				return
			}
			items, _ := page["items"].([]any)
			for _, it := range items {
				m, _ := it.(map[string]any)
				if !yield(m, nil) {
					return
				}
			}
			more, _ := page["has_more"].(bool)
			cursor, _ = page["cursor"].(string)
			if !more || cursor == "" {
				return
			}
		}
	}
}

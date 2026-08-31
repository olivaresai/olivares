// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package httpx is the read-only JSON HTTP client shared by the Olivares AI
// identity connectors that read an HTTP directory API (Okta, Microsoft Entra,
// HashiCorp Vault, Infisical). It exposes GET only, by construction, so a
// connector built on it cannot mutate the system it reads — the read-first
// guarantee (docs/SECURITY-HARDENING.md). The operator credential is held in memory and applied
// per request via a caller-supplied auth function; it is never logged, never
// placed in a URL, and never persisted (docs/SECURITY-HARDENING.md). A bounded excerpt of an
// error response is surfaced for diagnostics (a directory's "insufficient scope"
// message), but the credential never appears in any error.
//
// It is stdlib-only and deliberately tiny: the identity connectors differ in
// their auth header and their JSON shapes, not in how they read, so the shared
// part is exactly this client.
package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Doer is the minimal HTTP capability the client needs. *http.Client satisfies
// it; a test injects a stub returning fixture bytes so no live call is made.
type Doer interface {
	// Do issues req and returns the response, exactly like http.Client.Do.
	Do(req *http.Request) (*http.Response, error)
}

// AuthFunc applies the operator credential to a request. It is a function rather
// than a fixed scheme so each connector expresses its own header (Okta's
// "Authorization: SSWS <token>", a bearer token, Vault's "X-Vault-Token", etc.)
// without this package enumerating them. A nil AuthFunc sends no credential.
type AuthFunc func(req *http.Request)

// Bearer returns an AuthFunc that sets "Authorization: Bearer <token>". An empty
// token yields a no-op (no header), so an unconfigured connector sends nothing
// rather than an empty bearer.
func Bearer(token string) AuthFunc {
	return Header("Authorization", "Bearer "+token, token)
}

// Header returns an AuthFunc that sets header k to v, but only when the secret
// guard is non-empty. The guard is the credential value: when it is empty the
// connector is unconfigured and must send no auth header at all.
func Header(k, v, guard string) AuthFunc {
	return func(req *http.Request) {
		if guard == "" {
			return
		}
		req.Header.Set(k, v)
	}
}

// Client is a read-only JSON API client. It holds the base URL, the transport,
// the auth function and any static non-secret headers. It is safe for concurrent
// use if its Doer is.
type Client struct {
	base    string
	doer    Doer
	auth    AuthFunc
	headers map[string]string
	maxBody int64
}

// maxErrBody bounds how much of an error response body is read for diagnostics.
const maxErrBody = 2 << 10 // 2 KiB

// StatusError is the typed non-2xx error GetJSON/GetRaw return, so a connector
// can discriminate the status (errors.As) — e.g. a 403/404 on a gated research
// preview is an honest "no access" to declare, not a transient fault to retry.
// The message is identical to the historical untyped error, and the excerpt is
// the bounded response-body slice (never the credential, which httpx never puts
// in an error).
type StatusError struct {
	Path    string
	Status  int
	Excerpt string
}

// Error renders the same message the untyped error carried before StatusError
// existed, so log/finding content is unchanged.
func (e *StatusError) Error() string {
	return fmt.Sprintf("httpx: GET %s: status %d: %s", e.Path, e.Status, e.Excerpt)
}

// defaultMaxBody bounds a successful JSON body (a directory page is small; this
// protects memory against a hostile or runaway endpoint).
const defaultMaxBody = 16 << 20 // 16 MiB

// New builds a read-only client. base is the API root; doer is the transport (a
// *http.Client in production, a stub in tests; nil uses a hardened default);
// auth applies the in-memory credential (nil = none); headers are static,
// non-secret headers always sent (e.g. an API version).
//
// Redirect hardening: the same-origin rule on pagination links (url) would be
// hollow if the server could instead answer 302 and have the transport follow
// it — Go's default client chases up to ten redirects and forwards custom auth
// headers (X-Vault-Token, x-api-key) to ANY host. So a nil doer gets a default
// client whose CheckRedirect refuses a cross-origin Location, and an injected
// *http.Client WITHOUT its own redirect policy is shallow-copied with the same
// guard (the caller's client is never mutated; a caller that set a policy keeps
// it). A non-Client Doer (a test stub) follows no redirects by construction.
func New(base string, doer Doer, auth AuthFunc, headers map[string]string) *Client {
	switch d := doer.(type) {
	case nil:
		doer = &http.Client{CheckRedirect: refuseCrossOriginRedirect}
	case *http.Client:
		if d.CheckRedirect == nil {
			hardened := *d
			hardened.CheckRedirect = refuseCrossOriginRedirect
			doer = &hardened
		}
	}
	return &Client{
		base:    strings.TrimRight(base, "/"),
		doer:    doer,
		auth:    auth,
		headers: headers,
		maxBody: defaultMaxBody,
	}
}

// refuseCrossOriginRedirect is the CheckRedirect policy: a server-issued 3xx may
// stay within the origin of the ORIGINAL request (trailing-slash 301s and the
// like) but never leave it carrying the credential. Origin is compared with
// default-port normalization (RFC 6454).
func refuseCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("httpx: stopped after 10 redirects")
	}
	first := via[0].URL
	if !originEqual(req.URL, first) {
		return fmt.Errorf("httpx: refusing cross-origin redirect to %q (request origin is %q)", req.URL.Host, first.Host)
	}
	return nil
}

// GetJSON issues a read-only GET to base+path with the given query and decodes the
// JSON response into out (out may be nil to discard the body). It applies auth and
// the static headers, sends Accept: application/json, honors ctx, and returns a
// non-2xx status as an error carrying the code and a bounded body excerpt — never
// the credential.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.get(ctx, path, query)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return &StatusError{Path: path, Status: resp.StatusCode, Excerpt: strings.TrimSpace(string(excerpt))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, c.maxBody)).Decode(out); err != nil {
		return fmt.Errorf("httpx: decode %s: %w", path, err)
	}
	return nil
}

// GetRaw issues a read-only GET and returns the response so the caller can read
// headers (e.g. a Link pagination header) in addition to the JSON body. The
// caller owns closing the body. A non-2xx status is returned as an error AND the
// response (so the caller may inspect it); on a 2xx the error is nil.
func (c *Client) GetRaw(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	resp, err := c.get(ctx, path, query)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		_ = resp.Body.Close()
		return resp, &StatusError{Path: path, Status: resp.StatusCode, Excerpt: strings.TrimSpace(string(excerpt))}
	}
	return resp, nil
}

// maxRetryAfter caps how long a single 429 Retry-After wait may last; a server
// asking for more is treated as a plain rate-limit error (the engine's own
// poll/backoff takes over rather than this client sleeping unbounded).
const maxRetryAfter = 60 * time.Second

func (c *Client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u, err := c.url(path)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	issue := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("httpx: build request %s: %w", path, err)
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if c.auth != nil {
			c.auth(req)
		}
		resp, err := c.doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("httpx: GET %s: %w", path, err)
		}
		return resp, nil
	}
	resp, err := issue()
	if err != nil {
		return nil, err
	}
	// Directory APIs rate-limit routinely (Okta per-endpoint buckets, Graph
	// throttling). Honor ONE polite, bounded, ctx-aware wait per request when the
	// server says how long; anything else surfaces as the usual StatusError and the
	// engine's own retry/backoff owns the rest.
	if resp.StatusCode == http.StatusTooManyRequests {
		wait, ok := retryAfter(resp, time.Now())
		if !ok || wait > maxRetryAfter {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("httpx: GET %s: %w", path, ctx.Err())
		case <-time.After(wait):
		}
		return issue()
	}
	return resp, nil
}

// retryAfter extracts a 429 response's advertised wait: Retry-After in seconds or
// HTTP-date (RFC 9110), falling back to Okta's X-Rate-Limit-Reset epoch. ok=false
// when the server gave no usable hint (no blind waits).
func retryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if at, err := http.ParseTime(v); err == nil {
			if d := at.Sub(now); d > 0 {
				return d, true
			}
			return 0, true
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("X-Rate-Limit-Reset")); v != "" {
		if epoch, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Unix(epoch, 0).Sub(now); d > 0 {
				return d, true
			}
			return 0, true
		}
	}
	return 0, false
}

// url joins the base and path. An absolute path (starting with "/") is appended
// to the base. A fully-qualified URL is handled by who chose it: on a client
// WITH a configured base, an absolute path is a pagination cursor the SERVER
// returned (Okta's Link header, Graph's @odata.nextLink), so it is followed ONLY
// when its scheme and host match the base — a compromised or malicious directory
// must not be able to point a page fetch, and the credential the auth function
// attaches to EVERY request, at another host. On a client with NO base the
// caller passes every URL fully-qualified by design (the openidfed federation
// fetcher, which is inherently cross-host and attaches no credential), so the
// URL is the CALLER's deliberate choice, not a server-controlled redirect, and
// passes through verbatim.
func (c *Client) url(path string) (string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		if c.base == "" {
			return path, nil
		}
		if err := c.sameOrigin(path); err != nil {
			return "", err
		}
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.base + path, nil
}

// sameOrigin rejects an absolute pagination link whose origin differs from the
// configured base (deny-closed: an unparseable base or link refuses). The error
// carries only the offending host, never the credential.
func (c *Client) sameOrigin(link string) error {
	base, err := url.Parse(c.base)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("httpx: refusing absolute pagination link (configured base is unparseable)")
	}
	u, err := url.Parse(link)
	if err != nil {
		return fmt.Errorf("httpx: unparseable pagination link")
	}
	if !originEqual(u, base) {
		return fmt.Errorf("httpx: refusing cross-origin pagination link to %q (configured base is %q)", u.Host, base.Host)
	}
	return nil
}

// originEqual compares two URLs' origins (RFC 6454): scheme and host
// case-insensitively, with the scheme's default port (443/https, 80/http)
// treated as equal to an omitted one — a provider that writes the explicit
// default port into its cursors is still the same origin.
func originEqual(a, b *url.URL) bool {
	if !strings.EqualFold(a.Scheme, b.Scheme) {
		return false
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

// effectivePort returns the URL's port, defaulting from its scheme.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Doer is the minimal HTTP capability a connector needs to read a provider API.
// *http.Client satisfies it; a test injects a stub that returns fixture bytes so
// no live network call is made. Keeping the surface this small is what makes every
// connector trivially testable against recorded API shapes.
type Doer interface {
	// Do issues req and returns the response, exactly like http.Client.Do.
	Do(req *http.Request) (*http.Response, error)
}

// AuthScheme names how a provider expects the operator credential to be presented.
// The credential itself is a reference resolved by the engine (docs/SECURITY-HARDENING.md) and is
// held only in memory by Client for the lifetime of a request.
type AuthScheme string

const (
	// AuthBearer sends "Authorization: Bearer <cred>" (OpenAI, Anthropic Admin API).
	AuthBearer AuthScheme = "bearer"
	// AuthAnthropicKey sends "x-api-key: <cred>" (Anthropic Messages/Models API).
	AuthAnthropicKey AuthScheme = "x-api-key"
	// AuthGoogleKey sends "x-goog-api-key: <cred>" (Gemini API).
	AuthGoogleKey AuthScheme = "x-goog-api-key"
	// AuthFalKey sends "Authorization: Key <cred>" (fal.ai). The credential is the
	// fal "<key_id>:<key_secret>" pair; fal uses the literal "Key " prefix, not "Bearer".
	AuthFalKey AuthScheme = "fal-key"
	// AuthNone sends no credential (local inference: Ollama/vLLM open endpoints).
	AuthNone AuthScheme = "none"
)

// Client is a read-only JSON API client. It exposes GET only, by construction, so
// a connector built on it cannot mutate a provider (the read-first guarantee,
// docs/SECURITY-HARDENING.md). The operator credential is held in memory and applied per request;
// it is never logged, never placed in a URL, and never persisted. A bounded slice
// of an error response body is surfaced for diagnostics (provider error messages,
// which do not carry prompts/PII), but the credential never appears in any error.
type Client struct {
	base    string
	doer    Doer
	scheme  AuthScheme
	cred    string
	headers map[string]string
}

// NewClient builds a read-only client. base is the API root (no trailing slash
// required); doer is the HTTP transport (a *http.Client in production, a stub in
// tests); scheme/cred are the auth scheme and the in-memory credential reference;
// headers are static, non-secret headers always sent (e.g. "anthropic-version").
// When doer is nil, http.DefaultClient is used.
func NewClient(base string, doer Doer, scheme AuthScheme, cred string, headers map[string]string) *Client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Client{
		base:    strings.TrimRight(base, "/"),
		doer:    doer,
		scheme:  scheme,
		cred:    cred,
		headers: headers,
	}
}

// maxErrBody bounds how much of an error response body is read for diagnostics.
const maxErrBody = 2 << 10 // 2 KiB

// maxTextBody bounds a GetText body (a /metrics exposition can be large but not
// unbounded — protect memory against a hostile or runaway endpoint).
const maxTextBody = 4 << 20 // 4 MiB

// GetJSON issues a read-only GET to base+path with the given query and decodes the
// JSON response body into out. It applies the auth header from the in-memory
// credential, sends Accept: application/json, and honors ctx. A non-2xx status is
// returned as an error carrying the status code and a bounded body excerpt (never
// the credential). out may be nil to discard the body (a liveness probe).
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("modelprovider: build request %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.applyAuth(req)

	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("modelprovider: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		// Typed *APIError so a caller can route on the status (403 vs 404 vs 5xx) without
		// substring-matching a string that includes a server-controlled body excerpt. Its
		// Error() is byte-identical to the prior fmt.Errorf, so string-matching callers and
		// existing tests are unaffected.
		return &APIError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(excerpt))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("modelprovider: decode %s: %w", path, err)
	}
	return nil
}

// GetText issues a read-only GET to base+path with the given query and returns the
// response body as text. It is the non-JSON sibling of GetJSON, for endpoints that
// speak a text format (e.g. a Prometheus /metrics exposition from local inference).
// It applies auth and honors ctx identically, bounds the body to a sane size, and
// never logs the credential. A non-2xx status is a returned error.
func (c *Client) GetText(ctx context.Context, path string, query url.Values) (string, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("modelprovider: build request %s: %w", path, err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	c.applyAuth(req)

	resp, err := c.doer.Do(req)
	if err != nil {
		return "", fmt.Errorf("modelprovider: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxTextBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Typed *APIError (Error() is byte-identical to the prior fmt.Errorf) so a caller can
		// route on the status without substring-matching a server-controlled body excerpt.
		return "", &APIError{Method: http.MethodGet, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return string(body), nil
}

// applyAuth sets the credential header for the configured scheme. It is a no-op
// for AuthNone or an empty credential, so a local endpoint or an unconfigured
// connector sends no Authorization header rather than an empty one.
func (c *Client) applyAuth(req *http.Request) {
	if c.scheme == AuthNone || c.cred == "" {
		return
	}
	switch c.scheme {
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+c.cred)
	case AuthAnthropicKey:
		req.Header.Set("x-api-key", c.cred)
	case AuthGoogleKey:
		req.Header.Set("x-goog-api-key", c.cred)
	case AuthFalKey:
		req.Header.Set("Authorization", "Key "+c.cred)
	}
}

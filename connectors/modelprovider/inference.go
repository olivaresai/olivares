// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// InferenceClient is the model-INVOCATION sibling of the read-only Client. Where
// Client is GET-only by construction (the read-first guarantee, docs/SECURITY-HARDENING.md),
// InferenceClient deliberately performs writes — POST a Messages/Batches request,
// multipart-upload a File, POST an embeddings request — because invoking a model IS
// the operation. It stays minimal-data at the transport layer: it carries whatever
// body the caller hands it and never logs the credential; the CALLER (and the
// module above it) decides what, if anything, is persisted, always redacted/hashed
// (docs/SECURITY-HARDENING.md). Like Client it imports only the SDK contract surface, never /core,
// so it stays on the Apache side of the license frontier.
type InferenceClient struct {
	base    string
	doer    Doer
	scheme  AuthScheme
	cred    string
	headers map[string]string
}

// NewInferenceClient builds a read-write provider client. base is the API root;
// doer is the HTTP transport (a *http.Client in production, a stub in tests);
// scheme/cred are the auth scheme and the in-memory credential reference; headers
// are static, non-secret headers always sent (e.g. "anthropic-version"). When doer
// is nil, http.DefaultClient is used.
func NewInferenceClient(base string, doer Doer, scheme AuthScheme, cred string, headers map[string]string) *InferenceClient {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &InferenceClient{
		base:    strings.TrimRight(base, "/"),
		doer:    doer,
		scheme:  scheme,
		cred:    cred,
		headers: headers,
	}
}

// maxInferenceErrBody bounds an error response body excerpt (provider error
// messages, which do not carry prompts/PII).
const maxInferenceErrBody = 4 << 10 // 4 KiB

// maxResultsBody bounds a results download (a batch results JSONL can be large but
// must not be unbounded — protect memory against a hostile or runaway endpoint).
const maxResultsBody = 64 << 20 // 64 MiB

// applyAuth sets the credential header for the configured scheme. It mirrors
// Client.applyAuth: a no-op for AuthNone or an empty credential.
func (c *InferenceClient) applyAuth(req *http.Request) {
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
	}
}

// setHeaders applies the static headers then the per-request extras and auth. Extra
// headers (e.g. "anthropic-beta") override the static set for that request.
func (c *InferenceClient) setHeaders(req *http.Request, extra map[string]string) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	c.applyAuth(req)
}

// PostJSON issues a POST to base+path with body marshaled as JSON and decodes the
// JSON response into out (out may be nil to discard). extra carries per-request
// headers (e.g. a beta header). A non-2xx status returns an error with a bounded
// body excerpt, never the credential.
func (c *InferenceClient) PostJSON(ctx context.Context, path string, body, out any, extra map[string]string) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("modelprovider: marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("modelprovider: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setHeaders(req, extra)
	return c.do(req, path, out)
}

// PostJSONRaw issues a POST like PostJSON but returns the RAW (bounded) response body
// instead of decoding it, so a FAITHFUL proxy can relay the upstream bytes verbatim —
// preserving response fields the caller does not model (e.g. a batch's request_counts /
// expires_at / cancel_initiated_at). A non-2xx status returns a typed *APIError, never the
// credential. The body is capped at maxResultsBody.
func (c *InferenceClient) PostJSONRaw(ctx context.Context, path string, body any, extra map[string]string) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("modelprovider: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setHeaders(req, extra)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResultsBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Method: req.Method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(truncate(raw, maxInferenceErrBody)))}
	}
	return raw, nil
}

// PostStream issues a streaming POST (server-sent events) to base+path with body
// marshaled as JSON, and returns the response body for the CALLER to read
// incrementally (the SSE decoder lives in the connector). The caller MUST Close the
// returned reader. It sets Accept: text/event-stream. A non-2xx status is consumed
// (bounded) and returned as an *APIError BEFORE any stream begins, so a 400/401/429/5xx
// surfaces exactly like the JSON paths — never the credential. extra carries per-request
// headers (e.g. anthropic-beta).
func (c *InferenceClient) PostStream(ctx context.Context, path string, body any, extra map[string]string) (io.ReadCloser, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("modelprovider: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	c.setHeaders(req, extra)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: POST %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxInferenceErrBody))
		_ = resp.Body.Close()
		return nil, &APIError{Method: req.Method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(excerpt))}
	}
	return resp.Body, nil
}

// GetJSON issues a GET to base+path with query and decodes the JSON response into
// out. It is here (rather than only on the read-only Client) because polling a batch
// status / listing batches is part of the inference workflow.
func (c *InferenceClient) GetJSON(ctx context.Context, path string, query url.Values, out any, extra map[string]string) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("modelprovider: build GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	c.setHeaders(req, extra)
	return c.do(req, path, out)
}

// DeleteJSON issues a DELETE to base+path with optional query and decodes the JSON
// response into out (out may be nil to discard). It mirrors GetJSON for an idempotent
// resource deletion — the Files API DELETE /v1/files/{id} returns a small confirmation
// object ({"id":…,"type":"file_deleted"}); extra carries per-request headers (e.g. the
// Files beta header). A non-2xx status returns a typed *APIError, never the credential.
func (c *InferenceClient) DeleteJSON(ctx context.Context, path string, query url.Values, out any, extra map[string]string) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return fmt.Errorf("modelprovider: build DELETE %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	c.setHeaders(req, extra)
	return c.do(req, path, out)
}

// GetBytes downloads a (bounded) raw body from an absolute URL or a base-relative
// path. Anthropic returns a fully-qualified results_url for batch results (a JSONL
// stream), so this accepts either form. The body is capped at maxResultsBody.
func (c *InferenceClient) GetBytes(ctx context.Context, rawURL string, extra map[string]string) ([]byte, error) {
	u := rawURL
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.base + rawURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: build GET %s: %w", redactURL(rawURL), err)
	}
	c.setHeaders(req, extra)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelprovider: GET %s: %w", redactURL(rawURL), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResultsBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("modelprovider: GET %s: status %d: %s", redactURL(rawURL), resp.StatusCode, strings.TrimSpace(string(truncate(body, maxInferenceErrBody))))
	}
	return body, nil
}

// PostMultipart uploads a multipart/form-data body (the Files API): the named file
// part plus any extra form fields. It decodes the JSON response into out. extra
// carries per-request headers (the Files API requires its beta header).
func (c *InferenceClient) PostMultipart(ctx context.Context, path string, fields map[string]string, fileField, fileName string, fileContent []byte, out any, extra map[string]string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("modelprovider: multipart field %s: %w", k, err)
		}
	}
	fw, err := w.CreateFormFile(fileField, fileName)
	if err != nil {
		return fmt.Errorf("modelprovider: multipart file %s: %w", fileField, err)
	}
	if _, err := fw.Write(fileContent); err != nil {
		return fmt.Errorf("modelprovider: multipart write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("modelprovider: multipart close: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, &buf)
	if err != nil {
		return fmt.Errorf("modelprovider: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	c.setHeaders(req, extra)
	return c.do(req, path, out)
}

// do executes a prepared request and decodes a JSON response into out (nil to
// discard). It is the shared tail of PostJSON/GetJSON/PostMultipart.
func (c *InferenceClient) do(req *http.Request, path string, out any) error {
	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("modelprovider: %s %s: %w", req.Method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxInferenceErrBody))
		return &APIError{Method: req.Method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(excerpt))}
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

// APIError is a non-2xx provider response. It carries the status and a bounded body
// excerpt (provider error text, never the credential or the request prompt) so a
// caller can distinguish a 429/5xx (retryable/transport fault) from a 4xx (a bad
// request) without string-matching.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("modelprovider: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// redactURL strips the query string from a URL for error messages (a results_url or
// a signed download URL can carry a short-lived token in the query — never log it).
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?[redacted]"
	}
	return raw
}

// truncate caps b at n bytes for a bounded error excerpt.
func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

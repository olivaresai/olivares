// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// maxHTTPBody caps a Streamable HTTP response body so a hostile or runaway server
// cannot exhaust memory.
const maxHTTPBody = 32 << 20 // 32 MiB

// httpTransport speaks MCP over Streamable HTTP: each request is a POST that the
// server answers with either a single application/json response or a
// text/event-stream (SSE) carrying the response. It tracks the optional session
// id and the negotiated protocol version and replays them on later requests, per
// the transport spec.
type httpTransport struct {
	client  *http.Client
	url     string
	headers map[string]string

	// OAuth (MCP 2025-11-25 authorization): oauth is non-nil when the server has an
	// auth block. On a 401 the transport runs the resource-bound token flow once and
	// retries with the bearer; bearer holds the obtained token. On a 403 carrying an
	// insufficient_scope challenge it steps up with the accumulated scope union
	// (SEP-835/SEP-2350) — once PER REQUEST, straight-line — and retries: each list
	// method may be gated behind a different scope, and the oauthClient's
	// accumulation base carries the unions across requests.
	oauth     *oauthClient
	bearer    string
	triedAuth bool

	// obs records governance-relevant server-initiated messages interleaved on a
	// response SSE stream (deprecation posture, observer.go).
	obs requestObserver

	mu        sync.Mutex
	nextID    int64
	sessionID string
	version   string
}

// newHTTPTransport builds a Streamable HTTP transport for spec. When spec carries an
// auth block it also builds the OAuth client (deriving the resource indicator), so a
// 401 can be answered with an audience-bound token rather than only detected.
func newHTTPTransport(spec serverSpec) (*httpTransport, error) {
	t := &httpTransport{
		client:  &http.Client{},
		url:     spec.URL,
		headers: spec.Headers,
	}
	if spec.Auth.configured() {
		oc, err := newOAuthClient(spec.URL, spec.Auth, nil)
		if err != nil {
			return nil, err
		}
		t.oauth = oc
	}
	return t, nil
}

// roundTrip POSTs req and returns the matching response result, decoding either a
// JSON body or an SSE stream. On a 401 it runs the MCP OAuth flow once (when an auth
// block is configured): it obtains a token bound to THIS server (resource indicator)
// and retries authenticated. A 401 that is not resolved becomes an
// *oauthRequiredError carrying the PRM URL, so Gather can emit the Phase-1 finding.
func (t *httpTransport) roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	req.ID = t.nextID
	body, err := req.marshal()
	if err != nil {
		return nil, err
	}

	result, status, wwwAuth, err := t.send(ctx, body, req.ID)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && t.oauth != nil && !t.triedAuth {
		t.triedAuth = true
		if tok, aerr := t.oauth.bearer(ctx, wwwAuth); aerr == nil && tok != "" {
			t.bearer = tok
			result, status, wwwAuth, err = t.send(ctx, body, req.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	// SEP-835/SEP-2350: a 403 carrying an insufficient_scope challenge is answered
	// with ONE step-up per request (this code path runs once per roundTrip by
	// construction) — the re-acquired token's scopes are the UNION of the previously
	// requested set and the challenge (client-side accumulation) — then the request
	// is retried. A second shortfall on the SAME request is surfaced, not looped on;
	// a LATER request challenged for a different scope gets its own step-up, with
	// the union base carrying everything already acquired.
	if status == http.StatusForbidden && t.oauth != nil {
		if tok, isScopeChallenge, aerr := t.oauth.stepUpBearer(ctx, wwwAuth); isScopeChallenge && aerr == nil && tok != "" {
			t.bearer = tok
			result, status, wwwAuth, err = t.send(ctx, body, req.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	if status == http.StatusUnauthorized {
		return nil, &oauthRequiredError{resourceMetadata: resourceMetadataURL(wwwAuth), attempted: t.oauth != nil}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("mcp: http %d", status)
	}
	return result, nil
}

// send POSTs body once and, on a 2xx, decodes the JSON-RPC result for id. On a
// non-2xx it returns the status and the WWW-Authenticate header (no result), so the
// caller can decide whether to run the OAuth flow.
func (t *httpTransport) send(ctx context.Context, body []byte, id int64) (json.RawMessage, int, string, error) {
	resp, err := t.post(ctx, body)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHTTPBody))
		return nil, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
	}
	reader := io.LimitReader(resp.Body, maxHTTPBody)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		res, err := responseFromSSE(reader, id, &t.obs)
		return res, resp.StatusCode, "", err
	}
	res, err := responseFromJSON(reader, id)
	return res, resp.StatusCode, "", err
}

// notify POSTs a notification and discards the response (202 Accepted, or a 200
// the server may answer with).
func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, err := rpcRequest{Method: method, Params: params, isNotification: true}.marshal()
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHTTPBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mcp: http %s", resp.Status)
	}
	return nil
}

// tokenBound reports whether a server-bound OAuth token was obtained and used (IDN-03
// token-binding-verified). It lets introspect record the dimension on the catalog.
func (t *httpTransport) tokenBound() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bearer != ""
}

// observedServerRequests returns the server-initiated messages seen on response
// streams (introspect.go stamps them onto the catalog).
func (t *httpTransport) observedServerRequests() []serverRequestObservation {
	return t.obs.observations()
}

// authRegistration reports the OAuth client-identification path taken, when the
// flow ran (DCR-deprecation posture).
func (t *httpTransport) authRegistration() *authRegistrationObservation {
	if t.oauth == nil {
		return nil
	}
	return t.oauth.registrationObservation()
}

// setProtocolVersion records the negotiated version for the MCP-Protocol-Version
// header on later requests.
func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.version = v
}

// Close is a no-op; the connector does not hold a persistent connection (each
// request is a discrete POST), and a session is left for the server to expire.
func (t *httpTransport) Close() error { return nil }

// post builds and sends the POST with the MCP headers (caller holds the lock).
func (t *httpTransport) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	// An audience-bound token obtained for THIS server (never a passed-through token).
	if t.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.version != "" {
		req.Header.Set("MCP-Protocol-Version", t.version)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: http post: %w", err)
	}
	return resp, nil
}

// responseFromJSON decodes a single JSON-RPC response body and returns the result
// for id.
func responseFromJSON(r io.Reader, id int64) (json.RawMessage, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("mcp: read body: %w", err)
	}
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("mcp: decode body: %w", err)
	}
	return resultOf(msg, id)
}

// responseFromSSE scans an SSE stream and returns the result of the first
// JSON-RPC message that is the response to id. Skipped server-initiated messages
// are handed to obs (never answered) — the deprecation-posture seam; a nil
// obs skips observation.
func responseFromSSE(r io.Reader, id int64, obs *requestObserver) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxHTTPBody)
	var data strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		if data.Len() == 0 {
			return nil, false, nil
		}
		payload := data.String()
		data.Reset()
		var msg rpcMessage
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, false, nil // a non-JSON-RPC SSE event; ignore
		}
		if obs != nil {
			obs.observe(msg)
		}
		if !msg.isResponseTo(id) {
			return nil, false, nil
		}
		res, err := resultOf(msg, id)
		return res, true, err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if res, done, err := flush(); done || err != nil {
				return res, err
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("mcp: read sse: %w", err)
	}
	if res, done, err := flush(); done || err != nil {
		return res, err
	}
	return nil, fmt.Errorf("mcp: no response for request %d in sse stream", id)
}

// resultOf extracts the result (or error) of a response message for id.
func resultOf(msg rpcMessage, id int64) (json.RawMessage, error) {
	if !msg.isResponseTo(id) {
		return nil, fmt.Errorf("mcp: response id mismatch (want %d)", id)
	}
	if msg.Error != nil {
		return nil, msg.Error
	}
	return msg.Result, nil
}

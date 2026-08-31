// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// statelessHTTPTransport speaks the MCP 2026-07-28 RC Streamable HTTP profile:
// no handshake, no protocol-level session (it never sends Mcp-Session-Id and
// ignores one a legacy server mints), and the RC routing headers mirrored from
// the request body on every POST:
//
//	MCP-Protocol-Version  = params._meta["io.modelcontextprotocol/protocolVersion"]
//	                        (MUST match the body byte-for-byte)
//	Mcp-Method            = the JSON-RPC method (all requests AND notifications)
//	Mcp-Name              = params.name  (tools/call, prompts/get)
//	                        params.uri   (resources/read)
//	                        params.taskId (tasks/get|update|cancel, Tasks ext)
//	                        and MUST be omitted for every other method
//
// The headers are MIRRORS of body values (so L7 intermediaries can route and
// enforce without parsing the body); deriving them FROM the marshaled body makes
// a header/body mismatch structurally impossible on the client side. The `_meta`
// injection itself is the stateless Client's job (client.go) — this transport
// only frames what it is given, exactly like the 2025-11-25 httpTransport, which
// is left untouched (dual-version: this is the second `transport` implementation,
// selected by the next_revision feature flag).
//
// It is used sequentially by the Client (transport.go contract); listen() owns
// its own request and may run concurrently with nothing else on this transport.
type statelessHTTPTransport struct {
	client  *http.Client
	url     string
	headers map[string]string

	// OAuth (same flow as the stable transport): on a 401 the transport runs
	// the resource-bound token flow once and retries; on a 403 insufficient_scope
	// challenge it steps up with the accumulated scope union (SEP-2350) — once per
	// request, straight-line.
	oauth     *oauthClient
	bearer    string
	triedAuth bool

	// obs records governance-relevant server-initiated messages interleaved on a
	// response SSE stream (an RC-conforming server sends none — MRTR replaced
	// server-initiated requests — so any observation is itself a signal).
	obs requestObserver

	mu     sync.Mutex
	nextID int64
}

// newStatelessHTTPTransport builds the RC transport for spec (same auth wiring
// as the 2025-11-25 transport).
func newStatelessHTTPTransport(spec serverSpec) (*statelessHTTPTransport, error) {
	t := &statelessHTTPTransport{
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

// roundTrip POSTs req with the RC routing headers and returns the matching
// response result (JSON or SSE body, as in the stable transport). The OAuth
// 401-retry flow mirrors httpTransport.roundTrip.
func (t *statelessHTTPTransport) roundTrip(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	req.ID = t.nextID
	body, err := req.marshal()
	if err != nil {
		return nil, err
	}
	hdr := routingHeaders(req.Method, body)

	result, status, wwwAuth, err := t.send(ctx, body, hdr, req.ID)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized && t.oauth != nil && !t.triedAuth {
		t.triedAuth = true
		if tok, aerr := t.oauth.bearer(ctx, wwwAuth); aerr == nil && tok != "" {
			t.bearer = tok
			result, status, wwwAuth, err = t.send(ctx, body, hdr, req.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	// SEP-835/SEP-2350: one accumulated-scope step-up PER REQUEST on
	// insufficient_scope, then retry (mirrors httpTransport.roundTrip).
	if status == http.StatusForbidden && t.oauth != nil {
		if tok, isScopeChallenge, aerr := t.oauth.stepUpBearer(ctx, wwwAuth); isScopeChallenge && aerr == nil && tok != "" {
			t.bearer = tok
			result, status, wwwAuth, err = t.send(ctx, body, hdr, req.ID)
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

// send POSTs body once with the routing headers and decodes the result on 2xx
// (same contract as httpTransport.send). It NEVER records a session id: a
// legacy server's Mcp-Session-Id response header is deliberately ignored
// (sessions are removed in the RC; SEP-2567).
func (t *statelessHTTPTransport) send(ctx context.Context, body []byte, hdr map[string]string, id int64) (json.RawMessage, int, string, error) {
	resp, err := t.post(ctx, body, hdr)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// On a non-2xx the body may still carry a JSON-RPC error the caller can
		// classify (-32022 UnsupportedProtocolVersion, -32021 MissingClientCapability
		// ride HTTP 400 per the frozen RC; -32601 rides 404). Surface it when present.
		if rpcErr := rpcErrorFromBody(resp); rpcErr != nil {
			return nil, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), rpcErr
		}
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

// notify POSTs a notification (Mcp-Method is required on notifications too).
func (t *statelessHTTPTransport) notify(ctx context.Context, method string, params any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, err := rpcRequest{Method: method, Params: params, isNotification: true}.marshal()
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, body, routingHeaders(method, body))
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

// listen POSTs a subscriptions/listen request and consumes its SSE response
// stream until ctx is done or the server closes it. The first message MUST be
// notifications/subscriptions/acknowledged (deny-closed: anything else aborts);
// every subsequent notification is demultiplexed by the subscriptionId `_meta`
// tag and handed to onEvent. Closing the stream (ctx cancel) IS the cancel
// signal on HTTP — there is no unsubscribe RPC in the RC.
func (t *statelessHTTPTransport) listen(ctx context.Context, req rpcRequest, onEvent func(subscriptionEvent)) error {
	t.mu.Lock()
	t.nextID++
	req.ID = t.nextID
	id := req.ID
	bearer := t.bearer
	t.mu.Unlock()

	body, err := req.marshal()
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	setRequestHeaders(httpReq, t.headers, bearer, routingHeaders(req.Method, body))
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mcp: listen post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if rpcErr := rpcErrorFromBody(resp); rpcErr != nil {
			return rpcErr
		}
		return fmt.Errorf("mcp: listen http %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("mcp: listen response is not an event stream (%q)", resp.Header.Get("Content-Type"))
	}

	acked, tornDown := false, false
	err = scanSSE(io.LimitReader(resp.Body, maxHTTPBody), func(msg rpcMessage) error {
		if msg.Method == "" {
			if msg.isResponseTo(id) {
				if msg.Error != nil {
					return msg.Error
				}
				// GRACEFUL TEARDOWN. The published 2026-07-28 schema defines
				// SubscriptionsListenResultResponse: "A successful response from the
				// server for a subscriptions/listen request, sent when the server
				// tears the subscription down gracefully."
				//
				// This branch used to drop it with a comment asserting "the request
				// never resolves in the RC" — true of the release candidate, false of
				// the published revision (the definition arrived in the upstream fix
				// 271ecc9acc, 46 minutes after publication). Ignoring it made a
				// deliberate server shutdown indistinguishable from a dropped
				// connection, so a caller could not tell whether re-subscribing was
				// correct or a retry storm against a server that had said "done".
				tornDown = true
				return errSubscriptionTornDown
			}
			return nil
		}
		if !acked {
			if msg.Method != notificationSubscriptionsAcknowledged {
				return fmt.Errorf("mcp: listen stream did not start with %s (got %s)", notificationSubscriptionsAcknowledged, msg.Method)
			}
			acked = true
		}
		onEvent(subscriptionEvent{
			Method:         msg.Method,
			SubscriptionID: subscriptionIDOf(msg.Params),
			Params:         msg.Params,
		})
		return nil
	})
	switch {
	case errors.Is(err, errSubscriptionTornDown):
		return nil // the server said it was done: a clean end, not a failure
	case err != nil:
		return err
	case tornDown:
		return nil
	case ctx.Err() != nil:
		return ctx.Err() // the CLIENT closed the stream, which IS the cancel signal
	default:
		// The body ended without a teardown response and without a cancellation:
		// the stream was truncated. Reported rather than swallowed, so a caller
		// can re-subscribe deliberately instead of silently losing notifications.
		return errSubscriptionTruncated
	}
}

// errSubscriptionTornDown is the internal signal that the server closed the
// subscription gracefully (SubscriptionsListenResultResponse). It never escapes
// listen: it is translated into a nil error.
var errSubscriptionTornDown = errors.New("mcp: subscription torn down by the server")

// ErrSubscriptionTruncated reports that a subscriptions/listen stream ended
// WITHOUT the server's graceful-teardown response and without the client
// canceling — the notifications after that point were lost. It is exported
// because the decision it drives (re-subscribe, alert, or give up) belongs to
// the caller, and because "the stream just ended" is exactly the failure an
// observability product must not hide.
var ErrSubscriptionTruncated = errors.New("mcp: subscriptions/listen stream ended without a graceful teardown; notifications may have been missed")

// errSubscriptionTruncated is the internal alias kept for symmetry at the call
// site; callers match on the exported value.
var errSubscriptionTruncated = ErrSubscriptionTruncated

// tokenBound reports whether a server-bound OAuth token was obtained (IDN-03),
// mirroring httpTransport.tokenBound.
func (t *statelessHTTPTransport) tokenBound() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.bearer != ""
}

// observedServerRequests returns the server-initiated messages seen on response
// streams, mirroring httpTransport.
func (t *statelessHTTPTransport) observedServerRequests() []serverRequestObservation {
	return t.obs.observations()
}

// authRegistration reports the OAuth client-identification path taken, when the
// flow ran (DCR-deprecation posture), mirroring httpTransport.
func (t *statelessHTTPTransport) authRegistration() *authRegistrationObservation {
	if t.oauth == nil {
		return nil
	}
	return t.oauth.registrationObservation()
}

// setProtocolVersion is a no-op: the stateless transport has no negotiated
// version to replay — every request carries its version in `_meta` and the
// mirrored MCP-Protocol-Version header, derived from the body.
func (t *statelessHTTPTransport) setProtocolVersion(string) {}

// Close is a no-op (each request is a discrete POST; no session to end).
func (t *statelessHTTPTransport) Close() error { return nil }

// post builds and sends one POST (caller holds the lock).
func (t *statelessHTTPTransport) post(ctx context.Context, body []byte, routing map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setRequestHeaders(req, t.headers, t.bearer, routing)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: http post: %w", err)
	}
	return resp, nil
}

// setRequestHeaders applies the standard, operator and routing headers to req.
// Operator headers cannot override the routing mirrors (the mirrors are written
// last; a routing header that lied about the body would be a header/body
// mismatch the server MUST reject with -32020 HeaderMismatch).
func setRequestHeaders(req *http.Request, operator map[string]string, bearer string, routing map[string]string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range operator {
		req.Header.Set(k, v)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range routing {
		req.Header.Set(k, v)
	}
}

// routingHeaders derives the RC routing headers from the marshaled request
// body, so the mirrors cannot diverge from the body values:
// MCP-Protocol-Version (from `_meta`), Mcp-Method (always), Mcp-Name (only for
// the methods the spec lists — it MUST be omitted everywhere else).
func routingHeaders(method string, body []byte) map[string]string {
	var req struct {
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		return map[string]string{headerMcpMethod: method}
	}
	return routingHeadersForParams(method, req.Params)
}

// routingHeadersForParams is the same derivation from the request PARAMS alone,
// so an upstream adapter that builds its own JSON-RPC envelope derives exactly
// the same mirrors from exactly the same source (round-5 R5-05).
func routingHeadersForParams(method string, params []byte) map[string]string {
	out := map[string]string{headerMcpMethod: method}
	var p struct {
		Meta   map[string]any `json:"_meta"`
		Name   string         `json:"name"`
		URI    string         `json:"uri"`
		TaskID string         `json:"taskId"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil {
		return out
	}
	if v, ok := p.Meta[metaProtocolVersion].(string); ok && v != "" {
		out[headerMCPProtocolVersion] = v
	}
	if name, ok := mcpNameOf(method, p.Name, p.URI, p.TaskID); ok {
		out[headerMcpName] = name
	}
	return out
}

// UpstreamRoutingHeaders returns the RC Streamable HTTP routing headers an
// UPSTREAM ADAPTER must send for one forwarded request, derived from the request
// PARAMS themselves so a mirror can never contradict what it mirrors:
// `Mcp-Method` always, `MCP-Protocol-Version` from the params' `_meta`, and
// `Mcp-Name` for the methods the RC lists — tools/call and prompts/get
// (params.name), resources/read (params.uri) and the Tasks-extension methods
// (params.taskId, SEP-2663 sticky routing).
//
// Round-5 R5-05: the production forwarder emitted content, credential, trace
// and evidence headers but NO MCP routing headers. A strict RC upstream answers a
// request without them with -32020 HeaderMismatch, and no intermediary can route
// a tasks/* request to the instance holding that task's state — so a retained
// record could not be read, and therefore could not be drained. An adapter must
// call this and set every returned header; a legacy upstream ignores the extra
// ones (and gets no MCP-Protocol-Version, because a legacy request carries none).
func UpstreamRoutingHeaders(method string, params []byte) map[string]string {
	return routingHeadersForParams(method, params)
}

// mcpNameOf returns the Mcp-Name value for a method, per the RC table:
// tools/call and prompts/get carry params.name, resources/read carries
// params.uri, and the Tasks-extension methods carry params.taskId (SEP-2663
// sticky routing). Every other method carries NO Mcp-Name.
func mcpNameOf(method, name, uri, taskID string) (string, bool) {
	switch method {
	case "tools/call", "prompts/get":
		return name, name != ""
	case "resources/read":
		return uri, uri != ""
	case "tasks/get", "tasks/update", "tasks/cancel":
		return taskID, taskID != ""
	default:
		return "", false
	}
}

// rpcErrorFromBody decodes a JSON-RPC error object from a non-2xx response
// body, when the server sent one (the RC mandates JSON-RPC errors alongside
// 400/404 for -32602/-32021/-32022/-32601). nil when the body is not JSON-RPC.
func rpcErrorFromBody(resp *http.Response) *rpcError {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return nil
	}
	var msg rpcMessage
	if json.Unmarshal(raw, &msg) != nil || msg.Error == nil {
		return nil
	}
	return msg.Error
}

// subscriptionIDOf extracts the metaSubscriptionID `_meta` tag from a
// notification's params (empty when absent — surfaced as-is, never fabricated).
func subscriptionIDOf(params json.RawMessage) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	if v, ok := p.Meta[metaSubscriptionID].(string); ok {
		return v
	}
	return ""
}

// scanSSE reads an SSE stream and hands every JSON-RPC message to onMessage
// (a non-JSON-RPC event is skipped). It returns when the stream ends, the
// reader errors, or onMessage returns an error.
func scanSSE(r io.Reader, onMessage func(rpcMessage) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxHTTPBody)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		var msg rpcMessage
		if json.Unmarshal([]byte(payload), &msg) != nil {
			return nil // non-JSON-RPC SSE event; ignore
		}
		return onMessage(msg)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcp: read sse: %w", err)
	}
	return flush()
}

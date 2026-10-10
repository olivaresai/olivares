// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/license"
)

// UpstreamForwarder forwards an admitted/gated MCP method to the upstream tool
// backend over JSON-RPC. CRITICAL (no token passthrough): it authenticates with its OWN
// credential (via CredProv) and NEVER sees the inbound bearer — the
// mcpc.UpstreamRequest it receives carries no token, so the inbound credential is
// structurally unreachable from this request.
type UpstreamForwarder struct {
	URL      string
	CredProv CredentialProvider // resolves the SEPARATE upstream credential; NEVER the inbound token
	Client   *http.Client
	managed  bool
	mu       sync.Mutex
	sessions map[string]*managedMCPSession
	// A session-owned forwarder uses this authenticated identity for both
	// catalog inspection and tool calls. Shared gateway forwarders leave it nil.
	SessionIdentity *mcpc.SessionToolIdentity
}

var _ mcpc.SubscriptionUpstream = (*UpstreamForwarder)(nil)

// mcpForwardMaxResponse caps an upstream response body. The forwarder reads ONE
// byte past it so an over-limit body is DETECTED (a truncated body can never be
// validated, so it can never confirm an outcome).
const mcpForwardMaxResponse = 8 << 20

// Forward classifies every leg per the dispatch contract (mcpc.Upstream):
// errors BEFORE http.Client.Do are proven not_sent; after invoking Do, the ONLY
// path to `completed` is a 2xx response whose body is a STRICTLY VALID JSON-RPC
// 2.0 response CORRELATED to the sent id and carrying exactly one of
// result|error (mcpc.ParseStrictJSONRPCResponse — a valid JSON-RPC error object
// IS a completed round-trip). EVERYTHING else after Do — timeout, reset,
// cancellation, read failure, over-limit body, non-2xx status, malformed or
// uncorrelated body — is `unknown`: the request may have been transmitted and
// nothing observed can confirm the outcome.
func (f *UpstreamForwarder) Forward(ctx context.Context, req mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	if f.managed {
		if f.SessionIdentity != nil {
			req.Subject = f.SessionIdentity.Subject
			req.ClientID = f.SessionIdentity.ClientID
			req.Scopes = f.SessionIdentity.Scopes
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		key := managedMCPSessionKey(req)
		session := f.sessions[key]
		if req.Method == "initialize" {
			if session == nil && len(f.sessions) >= 128 {
				return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, errors.New("mcp gateway: session capacity reached")
			}
			session = &managedMCPSession{}
			f.sessions[key] = session
		} else if session == nil || session.version == "" {
			return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, errors.New("mcp gateway: initialize required for this client")
		}
		return f.forward(ctx, req, session)
	}
	return f.forward(ctx, req, nil)
}

func (f *UpstreamForwarder) forward(ctx context.Context, req mcpc.UpstreamRequest, session *managedMCPSession) (mcpc.UpstreamResult, error) {
	notSent := mcpc.UpstreamResult{State: mcpc.DispatchNotSent}
	unknown := mcpc.UpstreamResult{State: mcpc.DispatchUnknown}
	// sentID correlates the response: the SAME constant is sent and validated.
	// The envelope is the official go-sdk encoding; the params are the governed
	// bytes, carried verbatim.
	sentID := int64(1)
	msg := &jsonrpc.Request{Method: req.Method, Params: json.RawMessage(req.Params)}
	notification := session != nil && strings.HasPrefix(req.Method, "notifications/")
	if !notification {
		id, err := jsonrpc.MakeID(float64(sentID))
		if err != nil {
			return notSent, err
		}
		msg.ID = id
	}
	body, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return notSent, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(body))
	if err != nil {
		return notSent, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if session != nil {
		if session.id != "" {
			httpReq.Header.Set("Mcp-Session-Id", session.id)
		}
		if session.version != "" {
			httpReq.Header.Set("MCP-Protocol-Version", session.version)
		}
	}
	if f.CredProv != nil {
		authHdr, cerr := f.CredProv.Credential(ctx, f.URL)
		if cerr != nil {
			// An ENTITLEMENT refusal is not a transport fault, and collapsing the two is
			// how "your license lapsed" reaches an operator dressed as "the upstream is
			// down". The enterprise credential minter (credminter) raises
			// license.ErrAddonRequiresLicense; give it a stable, self-describing wrapper
			// so logs, tests and the client can tell the two apart.
			//
			// The dispatch state stays not_sent, which is PROVABLY true here: we refuse
			// before http.Client.Do, so nothing was transmitted (mcpc.Upstream contract).
			if addon, op, ok := license.AddonRefusal(cerr); ok {
				return notSent, addonRefusal(addon, op, cerr)
			}
			return notSent, fmt.Errorf("mcp gateway: credential provider: %w", cerr)
		}
		if authHdr != "" {
			httpReq.Header.Set("Authorization", authHdr) // separate credential — no passthrough
		}
	}
	if req.TraceParent != "" {
		httpReq.Header.Set("traceparent", req.TraceParent)
	}
	// propagate the operation identity + fence token for receiver-side
	// idempotency/fencing (design §6 — identifiers only, never credentials). An
	// upstream that understands them can reject a stale epoch; one that does not
	// ignores unknown headers (the check-to-effect window residual remains).
	if req.OperationID != "" {
		httpReq.Header.Set("Olivares-Operation-Id", string(req.OperationID))
	}
	if req.EffectDigest != "" {
		httpReq.Header.Set("Olivares-Effect-Digest", string(req.EffectDigest))
	}
	// Round-5 R5-05: the MCP ROUTING MIRRORS, derived by the connector from
	// these exact params (mcpc.UpstreamRoutingHeaders) so a header can never
	// contradict the body it mirrors. They are written LAST, after the operator and
	// credential headers, for the same reason the connector's own client writes them
	// last: a mirror that lied about the body is a -32020 HeaderMismatch, and a
	// strict RC upstream refuses a tasks/* request that carries none — which would
	// leave a retained task record permanently unreadable and therefore undrainable.
	for k, v := range mcpc.UpstreamRoutingHeaders(req.Method, req.Params) {
		httpReq.Header.Set(k, v)
	}
	if req.FenceToken != "" {
		httpReq.Header.Set("Olivares-Fence-Token", req.FenceToken)
	}
	resp, err := doMCPForwardRequest(f.Client, httpReq)
	if err != nil {
		if errors.Is(err, mcpc.ErrUpstreamCredentialTooShort) || errors.Is(err, mcpc.ErrUpstreamCredentialInvalid) {
			return notSent, err
		}
		// After invoking Do the request may have reached the wire: unknown.
		return unknown, fmt.Errorf("mcp gateway: upstream forward: %w", err)
	}
	defer resp.Body.Close()
	if session != nil {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return unknown, errors.New("mcp gateway: upstream HTTP refusal (outcome unknown)")
		}
		if notification {
			if resp.StatusCode != http.StatusAccepted {
				return unknown, errors.New("mcp gateway: invalid notification acknowledgment")
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, mcpForwardMaxResponse+1))
			if errors.Is(err, mcpc.ErrUpstreamCredentialDisclosure) {
				return unknown, err
			}
			if err != nil || len(raw) != 0 {
				return unknown, errors.New("mcp gateway: notification acknowledgment has a body")
			}
			return mcpc.UpstreamResult{State: mcpc.DispatchCompleted}, nil
		}
		raw, err := readManagedMCPResponse(resp, sentID)
		if err != nil {
			return unknown, err
		}
		result, rpcErr, err := mcpc.ParseStrictJSONRPCResponse(raw, sentID)
		if err != nil {
			return unknown, errors.New("mcp gateway: upstream response failed strict validation")
		}
		if rpcErr != nil {
			return mcpc.UpstreamResult{State: mcpc.DispatchCompleted}, errors.New("mcp gateway: upstream RPC refusal")
		}
		if req.Method == "initialize" {
			var init struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(result, &init) != nil || init.ProtocolVersion != "2025-11-25" {
				return mcpc.UpstreamResult{State: mcpc.DispatchCompleted}, errors.New("mcp gateway: unsupported negotiated protocol")
			}
			sid := resp.Header.Get("Mcp-Session-Id")
			if !validManagedMCPSessionID(sid) {
				return mcpc.UpstreamResult{State: mcpc.DispatchCompleted}, errors.New("mcp gateway: invalid upstream session identifier")
			}
			session.id = sid
			session.version = init.ProtocolVersion
		}
		return mcpc.UpstreamResult{Result: result, State: mcpc.DispatchCompleted}, nil
	}
	// limit+1: an over-limit body is DETECTED, never silently truncated — a valid
	// prefix followed by overflow data must not be validated as a response.
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, mcpForwardMaxResponse+1))
	if rerr != nil {
		return unknown, fmt.Errorf("mcp gateway: read upstream response: %w", rerr)
	}
	if len(raw) > mcpForwardMaxResponse {
		return unknown, fmt.Errorf("mcp gateway: upstream response exceeds %d bytes; cannot validate (outcome unknown)", mcpForwardMaxResponse)
	}
	// Review round-1 P1 + round-2 NEW-1: "completed" means an upstream response was
	// OBSERVED and STRICTLY CONFIRMED. A non-2xx status is not a confirmed JSON-RPC
	// outcome (an intermediary may have produced it after the origin possibly
	// acted) → unknown. A 2xx body must pass the connector's strict validation
	// (exact member casing, duplicate-key + trailing-data rejection, correlated
	// integer id == sentID, exactly one of result|error, error carrying an integer
	// code + string message) → anything else is unknown.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unknown, fmt.Errorf("mcp gateway: upstream http %d (outcome unknown)", resp.StatusCode)
	}
	result, rpcErr, verr := mcpc.ParseStrictJSONRPCResponse(raw, sentID)
	if verr != nil {
		return unknown, fmt.Errorf("mcp gateway: upstream response failed strict validation (outcome unknown): %w", verr)
	}
	if rpcErr != nil {
		// A strictly valid JSON-RPC error IS a completed round-trip (design §1).
		return mcpc.UpstreamResult{State: mcpc.DispatchCompleted},
			fmt.Errorf("mcp gateway: upstream rpc %d %s", rpcErr.Code, rpcErr.Message)
	}
	return mcpc.UpstreamResult{Result: result, State: mcpc.DispatchCompleted}, nil
}

// Listen opens one long-lived upstream subscriptions/listen request. It uses
// the forwarder's separate credential provider and a copy of its HTTP client
// with no whole-request timeout: the downstream request context is the stream
// lifetime and canceling it is MCP's unsubscribe signal.
func (f *UpstreamForwarder) Listen(
	ctx context.Context,
	req mcpc.SubscriptionListenRequest,
	emit func(mcpc.SubscriptionEvent) error,
) error {
	const requestID int64 = 1
	body, params, err := mcpc.MarshalSubscriptionUpstreamRequest(requestID, req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if f.CredProv != nil {
		authHeader, credentialErr := f.CredProv.Credential(ctx, f.URL)
		if credentialErr != nil {
			if addon, operation, ok := license.AddonRefusal(credentialErr); ok {
				return addonRefusal(addon, operation, credentialErr)
			}
			return fmt.Errorf("mcp gateway: subscription credential provider: %w", credentialErr)
		}
		if authHeader != "" {
			httpReq.Header.Set("Authorization", authHeader)
		}
	}
	if req.TraceParent != "" {
		httpReq.Header.Set("traceparent", req.TraceParent)
	}
	for key, value := range mcpc.UpstreamRoutingHeaders(mcpc.SubscriptionListenMethod, params) {
		httpReq.Header.Set(key, value)
	}

	client := http.DefaultClient
	if f.Client != nil {
		streamClient := *f.Client
		streamClient.Timeout = 0
		client = &streamClient
	}
	resp, err := doMCPForwardRequest(client, httpReq)
	if err != nil {
		if errors.Is(err, mcpc.ErrUpstreamCredentialTooShort) || errors.Is(err, mcpc.ErrUpstreamCredentialInvalid) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: upstream listen: %v", mcpc.ErrSubscriptionRelayTruncated, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, mcpForwardMaxResponse))
		return fmt.Errorf("mcp gateway: upstream listen http %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("mcp gateway: upstream listen is not an event stream")
	}
	err = mcpc.ConsumeSubscriptionUpstreamStream(resp.Body, requestID, emit)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

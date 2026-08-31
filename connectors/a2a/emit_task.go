// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrAfterTransmit marks a failure that occurred AFTER the request was
// transmitted to the remote agent (a response-read timeout, or a body-read
// error) — so the remote MAY have processed the message. A caller settling a
// governed effect must record the outcome as UNKNOWN (ambiguous), never a
// definitive "failed", and must NOT blindly re-emit (at-most-once past this
// point). A pre-transmit failure (connection refused before the body was sent)
// is NOT wrapped: nothing was delivered and it is a clean failure.
var ErrAfterTransmit = errors.New("a2a: failure after the request was transmitted; delivery ambiguous")

// emit_task.go is the GOVERNED A2A v1.0 Task-emission primitive: it discovers and
// trust-verifies a remote Agent Card and, only when the card's identity is
// established against the operator's out-of-band trust anchor, emits exactly ONE
// `SendMessage` (JSON-RPC 2.0). It is the outbound counterpart of the read-only
// observation Source in this package, and it is what the orchestration Dispatcher
// (cmd/olivares) calls to actuate a fire whose subject is a remote agent.
//
// SCOPE vs (read this before extending). This file is the minimal, governed
// emission of a SINGLE Task. It is DELIBERATELY NOT the full A2A delegation
// Policy-Enforcement-Point. The following are not here, and
// must not be silently bolted on:
//   - a remote agent/skill/scope ALLOWLIST and least-privilege authorization PEP;
//   - ListTasks / GetTask reconciliation and cursor pagination;
//   - SubscribeToTask / resubscribe (SSE) streaming of task updates;
//   - a push-notification receiver (webhook) with SSRF defenses + JWT/JWKS verify;
//   - OTel spans / W3C Trace Context propagation per Task.
// This primitive STOPS at: verify the card → send one Task → return its lifecycle
// state (interrupts surfaced as actionable, never as success).
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md):
//   - Deny-closed (§6 anti-evasion): emission requires trustVerified against the
//     OPERATOR-configured trust anchor. A self-asserted (jku-only), unsigned, or
//     unverifiable card is REFUSED — emission is an action, so it holds a stricter
//     bar than mere observation (which tolerates self-asserted as `approximate`).
//   - Out-of-band auth (A2A enterprise MUST): credentials travel ONLY in standard
//     HTTP headers, never inside the A2A/JSON-RPC payload. Transport is HTTPS with
//     certificate validation and TLS 1.2+ (refused otherwise unless AllowInsecure).
//   - Minimal data (§3): the request carries a task instruction and optional skill/
//     context references only — no secrets, no payload blobs.

// A2A v1.0 JSON-RPC method names (the v1.0 rename of the v0.x `message/*` forms).
// Pinned to a2aproject/A2A v1.0.1 (2026-05-28). SendMessage is emitted here; the
// full v1.0.1 method surface the delegation client speaks lives in methods.go.
const (
	methodSendMessage = "SendMessage" // v1.0 rename of message/send
)

// emitBodyCap bounds a SendMessage response so a hostile/runaway endpoint cannot
// exhaust memory (mirrors maxCardBody for card fetches).
const emitBodyCap = 4 << 20 // 4 MiB

// Transport is the injectable HTTP doer for the emission client. Production passes
// an *http.Client pinned to TLS 1.2+; tests pass a deterministic stub so the
// primitive is exercised entirely offline.
type Transport interface {
	Do(req *http.Request) (*http.Response, error)
}

// EmitConfig configures a governed A2A Task-emission Client. TrustJWKS is the
// operator's OUT-OF-BAND trust anchor (a JWK Set); without it no card can reach
// trustVerified and every emission is denied (deny-closed). Headers are the
// out-of-band auth headers attached to BOTH the card fetch and the SendMessage POST
// (never placed in the payload). Doer is injected by tests; nil yields a default
// HTTPS client (TLS 1.2+, cert validation).
type EmitConfig struct {
	TrustJWKS     []byte            // operator trust anchor (JWK Set); required for any emission
	Headers       map[string]string // out-of-band auth headers (e.g. Authorization) — HTTPS only, never in payload
	WellKnownPath string            // Agent Card discovery path; default /.well-known/agent-card.json
	Timeout       time.Duration     // per-call timeout; default 30s
	AllowInsecure bool              // default false: a non-HTTPS endpoint is refused (A2A spec MUST)
	Doer          Transport         // injected in tests; nil => default TLS 1.2+ client
}

// Client emits governed A2A v1.0 Tasks to trust-verified remote agents. It is safe
// for concurrent use. Construct it with NewClient.
type Client struct {
	anchorRaw     []byte
	headers       map[string]string
	wellKnownPath string
	timeout       time.Duration
	allowInsecure bool
	doer          Transport
}

// NewClient builds a Task-emission Client from config, applying safe defaults.
func NewClient(cfg EmitConfig) *Client {
	c := &Client{
		anchorRaw:     cfg.TrustJWKS,
		headers:       cfg.Headers,
		wellKnownPath: strings.TrimSpace(cfg.WellKnownPath),
		timeout:       cfg.Timeout,
		allowInsecure: cfg.AllowInsecure,
		doer:          cfg.Doer,
	}
	if c.wellKnownPath == "" {
		c.wellKnownPath = defaultWellKnownPath
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.doer == nil {
		c.doer = &http.Client{
			Timeout:   c.timeout,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, Proxy: http.ProxyFromEnvironment},
			// Do NOT auto-follow redirects (D-05, item 8): a followed 3xx can be
			// POSTed to URL A, redirect to B, and then fail DNS/dial on B — the error
			// Do() returns is B's dial error, which isPreTransmit would misread as a
			// definite pre-transmit failure even though the POST to A DID leave. Not
			// following makes a 3xx a RECEIVED response (a definite "not actioned here",
			// no effect), so the ambiguity classification stays correct.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return c
}

// SendSpec is the minimal-data description of the Task to emit. It carries only
// references and the instruction text — never a secret, credential, or payload blob
// (those are out-of-band / server-side). AgentURL is the remote agent's base or
// direct card URL; the card is discovered and verified from it before anything is
// sent.
type SendSpec struct {
	AgentName string // logical name (audit/label only)
	AgentURL  string // remote agent base URL (well-known card path appended) or direct card URL
	Text      string // the task instruction (the message content)
	Skill     string // optional skill id to target (carried as message metadata)
	ContextID string // optional A2A contextId to continue an existing conversation
}

// TaskResult is the outcome of a SendMessage. Interrupt is true for the actionable
// input/auth-required states (the agent needs more from the caller — NOT a
// success); Terminal is true for the four terminal states. TrustLevel records the
// verified card trust that authorized the emission (always "verified" here).
type TaskResult struct {
	TaskID string
	// ResultKind distinguishes the A2A SendMessage response oneof without forcing
	// callers to infer it from State or Detail. It is "task" for a durable remote
	// Task and "message" for a synchronous Message response. MessageID preserves
	// the semantic Message identifier; TaskID continues to mirror it for backwards
	// compatibility with callers written before the oneof was exposed.
	ResultKind string
	MessageID  string
	// MessageDigest commits to the complete canonical Message wire value and
	// supplies the durable semantic key used across sync, push and SSE retries.
	MessageDigest string
	// MessageTaskID preserves the optional taskId carried by an agent Message.
	// TaskID continues to mirror MessageID for backwards compatibility.
	MessageTaskID string
	// MessageParts is populated only for a synchronous Message result. Text is
	// bounded plain text; non-text values never escape the connector and are
	// represented by a canonical SHA-256 commitment and an optional file URI.
	MessageParts []MessageResultPart
	ContextID    string
	State        TaskState
	Interrupt    bool
	Terminal     bool
	TrustLevel   string
	Detail       string // short, non-sensitive summary
}

// MessageResultPart is the bounded, connector-neutral projection of one A2A
// Message Part. Digest always commits to the complete canonical wire Part.
type MessageResultPart struct {
	Kind      string
	Text      string
	Reference string
	Digest    string
}

const (
	maxMessageResultParts     = 62
	maxMessageResultTextBytes = 32 * 1024
	maxMessageResultWireBytes = 64 * 1024
)

// interrupt reports whether a state needs more input/auth before it can progress —
// an actionable, non-terminal pause that the caller must surface, never treat as done.
func taskStateInterrupt(s TaskState) bool {
	return s == TaskStateInputReq || s == TaskStateAuthRequired
}

// terminal reports whether a state is a lifecycle endpoint (success or otherwise).
func taskStateTerminal(s TaskState) bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return true
	default:
		return false
	}
}

// SendMessage discovers + verifies the remote Agent Card, then emits one A2A v1.0
// SendMessage to it. It returns an error (and emits nothing) when the card is not
// trustVerified against the operator anchor, when the endpoint is not HTTPS, or when
// the transport/RPC fails. INPUT_REQUIRED / AUTH_REQUIRED come back as a successful
// call with Interrupt=true (actionable), not as an error and not as completion.
func (c *Client) SendMessage(ctx context.Context, spec SendSpec) (TaskResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// 1) Discover + verify the remote card BEFORE anything leaves for the agent.
	card, err := c.verifiedCard(ctx, spec)
	if err != nil {
		return TaskResult{}, err
	}
	// 2): enforce SecurityScheme floor + binding before emission.
	if err := requireSecurityScheme(card, spec.AgentName, credentialSchemeKind(c.headers)); err != nil {
		return TaskResult{}, err
	}
	// 3) Resolve the verified card's service endpoint and emit one SendMessage.
	return c.emit(ctx, card, spec)
}

// SendMessageCapable is SendMessage with the capability binding: it discovers +
// verifies the remote card AND requires that the SIGNED card declares spec.Skill before
// emitting — you may not send a Task to a skill the agent does not cryptographically
// claim. A blank skill is the agent's general endpoint (no capability check). It is the
// capability-bound emission primitive the scheduled-fire A2A route (cmd) uses; it adds a
// deny-closed capability gate on top of SendMessage without a second card fetch.
func (c *Client) SendMessageCapable(ctx context.Context, spec SendSpec) (TaskResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	card, err := c.verifiedCard(ctx, spec)
	if err != nil {
		return TaskResult{}, err
	}
	if err := requireDeclaredSkill(card, spec.AgentName, spec.Skill); err != nil {
		return TaskResult{}, err
	}
	// enforce SecurityScheme floor + binding before emission.
	if err := requireSecurityScheme(card, spec.AgentName, credentialSchemeKind(c.headers)); err != nil {
		return TaskResult{}, err
	}
	return c.emit(ctx, card, spec)
}

// resolveJSONRPC resolves the JSON-RPC service endpoint (and its tenant routing id,
// which the client MUST echo in the `tenant` field of every request to that
// interface — a2a.proto AgentInterface) from a verified card:
//
//   - a v1.0 card resolves via supportedInterfaces (§8.3.2: first JSONRPC entry we
//     can speak). A card that declares interfaces but no JSONRPC one we speak is
//     REFUSED — falling back to another binding's URL would act outside the card's
//     declared surface.
//   - a pre-1.0 card (no supportedInterfaces) falls back to the removed v0.x
//     top-level url, then to the operator-configured agent URL (lenient parse).
func resolveJSONRPC(card AgentCard, fallbackURL string) (endpoint, tenant string, err error) {
	if it, ok := card.jsonrpcInterface(); ok {
		return strings.TrimSpace(it.URL), strings.TrimSpace(it.Tenant), nil
	}
	if len(card.SupportedInterfaces) > 0 {
		return "", "", fmt.Errorf("a2a: agent card declares %d interface(s) but no JSONRPC v1 endpoint this client speaks; refusing",
			len(card.SupportedInterfaces))
	}
	if u := strings.TrimSpace(card.URL); u != "" {
		return u, "", nil
	}
	return strings.TrimSpace(fallbackURL), "", nil
}

// emit resolves the verified card's JSON-RPC service endpoint and emits exactly one
// SendMessage to it. It is the action half of SendMessage, factored out so the
// governed Delegator (delegate.go) can interpose its PEP (allowlist + ApprovalGate)
// BETWEEN card verification and emission without duplicating the wire build. It
// ASSUMES the card was already trust-verified (verifiedCard) — never call it on an
// unverified card.
func (c *Client) emit(ctx context.Context, card AgentCard, spec SendSpec) (TaskResult, error) {
	// Resolve the JSON-RPC endpoint from the card's supportedInterfaces (v1.0) and
	// refuse a non-HTTPS endpoint (spec MUST), so a verified card cannot redirect us
	// to clear-text.
	endpoint, tenant, err := resolveJSONRPC(card, spec.AgentURL)
	if err != nil {
		return TaskResult{}, err
	}
	if err := c.requireSecure(endpoint); err != nil {
		return TaskResult{}, err
	}

	// Build the SendMessage envelope. Credentials are NOT here — they are the
	// out-of-band HTTP headers applied by doRPC(); the payload carries refs + text only.
	msg := a2aMessage{
		Role:      roleUser,
		Parts:     []a2aPart{{Text: spec.Text}},
		MessageID: newID(),
	}
	if spec.ContextID != "" {
		msg.ContextID = spec.ContextID
	}
	if spec.Skill != "" {
		msg.Metadata = map[string]any{"skill": spec.Skill}
	}
	reqEnv := jsonrpcRequest{JSONRPC: "2.0", ID: newID(), Method: methodSendMessage, Params: sendParams{Tenant: tenant, Message: msg}}
	return c.post(ctx, endpoint, reqEnv)
}

// verifiedCard fetches and verifies a remote Agent Card, returning it only when its
// identity is established against the operator trust anchor. Self-asserted/unsigned/
// unverified all DENY (emission is an action — stricter than observation).
func (c *Client) verifiedCard(ctx context.Context, spec SendSpec) (AgentCard, error) {
	url := cardURL(agentSpec{URL: spec.AgentURL}, c.wellKnownPath)
	if url == "" {
		return AgentCard{}, fmt.Errorf("a2a: agent %q has no url to discover a card", spec.AgentName)
	}
	if err := c.requireSecure(url); err != nil {
		return AgentCard{}, err
	}
	body, err := c.get(ctx, url)
	if err != nil {
		return AgentCard{}, fmt.Errorf("a2a: discover card for %q: %w", spec.AgentName, err)
	}
	rc, err := parseCard(body)
	if err != nil {
		return AgentCard{}, err
	}
	anchor, err := parseJWKS(c.anchorRaw)
	if err != nil {
		return AgentCard{}, err
	}
	// resolveJKU is nil on purpose: a self-asserted jku proves integrity, not
	// identity, and identity is mandatory to ACT — so emission accepts only an
	// operator-anchored signature (verifyCard returns trustVerified for it).
	lvl, detail := verifyCard(ctx, rc, anchor, nil)
	if !lvl.trusted() {
		return AgentCard{}, fmt.Errorf("a2a: refusing to emit Task to %q: card %s (%s)", spec.AgentName, lvl, detail)
	}
	return rc.card, nil
}

// requireSecure refuses a non-HTTPS endpoint unless the operator explicitly opted
// in (AllowInsecure, e.g. a mesh-internal plaintext hop). A2A prod is HTTPS (MUST).
func (c *Client) requireSecure(rawURL string) error {
	if c.allowInsecure {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "https://") {
		return fmt.Errorf("a2a: endpoint %q is not https (A2A requires TLS); refusing", rawURL)
	}
	return nil
}

// get performs a bounded, header-authenticated HTTPS GET (card discovery).
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	c.applyAuth(req)
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := httpAuthError(resp.StatusCode); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCardBody))
		return nil, fmt.Errorf("a2a: card http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxCardBody))
}

// post emits the JSON-RPC SendMessage and maps the response to a TaskResult. A
// JSON-RPC error or a non-2xx status is a failure; a result is decoded into a Task
// (with a lifecycle state) or a synchronous Message (treated as completed).
func (c *Client) post(ctx context.Context, endpoint string, env jsonrpcRequest) (TaskResult, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return TaskResult{}, err
	}
	result, err := c.doRPC(ctx, endpoint, env.Method, raw)
	if err != nil {
		return TaskResult{}, err
	}
	return resultToTask(result)
}

// doRPC is the shared A2A JSON-RPC 2.0 unary transport: it POSTs an already-marshaled
// request envelope to a (verified, HTTPS) endpoint with the operator's out-of-band
// auth headers, maps the A2A-mandated auth status codes, bounds the body, and returns
// the JSON-RPC `result` (or a non-sensitive error for a transport/RPC failure). It is
// the one wire path SendMessage and every delegation method (GetTask, CancelTask,
// ListTasks, GetExtendedAgentCard) share, so credentials, TLS posture and the
// google.rpc.Status error mapping are enforced in exactly one place. method names the
// call for honest error messages only; it is NOT re-derived from the body.
func (c *Client) doRPC(ctx context.Context, endpoint, method string, raw []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	// JSON-RPC binding content type is application/json (spec §9.1 — the v1.0.1
	// application/a2a+json preference, errata #1753, is scoped to the HTTP+JSON/REST
	// binding and webhook payloads, NOT JSON-RPC).
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// A2A-Version is MANDATORY on every protocol request (§3.6.1: clients MUST send
	// it; an empty header makes a conformant server treat the caller as v0.3).
	req.Header.Set(a2aVersionHeader, a2aVersionWire)
	c.applyAuth(req)
	// Propagate the inbound W3C trace context (delegate.go) so the remote agent's
	// span is a child of the caller's — cross-agent trace correlation, never a
	// credential. Out-of-band header, never in the A2A payload.
	if tp := traceParentFrom(ctx); tp != "" {
		req.Header.Set("traceparent", tp)
	}
	// Propagate the bounded, cycle-free multi-agent delegation lineage out-of-band
	// (chain.go) so a cooperating downstream Olivares plane keeps the same
	// governed chain. It is an Olivares extension header, never an A2A payload field,
	// and never a credential — a non-Olivares peer simply ignores it.
	if path := chainFrom(ctx).encode(); path != "" {
		req.Header.Set(delegationPathHeader, path)
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		// PRE-transmit failures (DNS, connection refused/reset in the dial phase —
		// no request bytes ever left) are clean, definite failures. EVERYTHING else
		// past the dial (timeout, EOF/reset mid/post-write, read failure) MAY mean
		// the request reached and was processed by the remote — ambiguous.
		if isPreTransmit(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s transport error: %v", ErrAfterTransmit, method, err)
	}
	defer resp.Body.Close()
	if err := httpAuthError(resp.StatusCode); err != nil {
		// 401/403: the remote refused BEFORE processing — no effect, definite.
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, emitBodyCap))
	if err != nil {
		// The request was fully transmitted and the remote responded; we merely
		// failed to read the body — the effect may have happened (ambiguous).
		return nil, fmt.Errorf("%w: read %s response: %v", ErrAfterTransmit, method, err)
	}
	if resp.StatusCode >= 500 {
		// The remote RECEIVED the request and errored while (possibly) processing
		// it — ambiguous, not a definite failure.
		return nil, fmt.Errorf("%w: a2a %s http %d", ErrAfterTransmit, method, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 3xx/4xx: the request was received and DEFINITIVELY rejected — no effect.
		return nil, fmt.Errorf("a2a: %s http %d", method, resp.StatusCode)
	}

	var rpc jsonrpcResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		// A 2xx with an unparseable body — we cannot tell what the remote did.
		return nil, fmt.Errorf("%w: decode %s response: %v", ErrAfterTransmit, method, err)
	}
	if rpc.Error != nil {
		// The remote accepted and processed the request and returned a JSON-RPC
		// error; for a governed Task emit we cannot be CERTAIN no effect occurred,
		// so this is ambiguous (at-most-once — never a false definite "failed").
		// Surface only the code (+ its spec name for -32001..-32009) and message.
		if name := a2aErrorName(rpc.Error.Code); name != "" {
			return nil, fmt.Errorf("%w: a2a remote rejected %s: rpc %d %s %s", ErrAfterTransmit, method, rpc.Error.Code, name, rpc.Error.Message)
		}
		return nil, fmt.Errorf("%w: a2a remote rejected %s: rpc %d %s", ErrAfterTransmit, method, rpc.Error.Code, rpc.Error.Message)
	}
	return rpc.Result, nil
}

// isPreTransmit reports whether a transport error occurred BEFORE any request
// bytes could have reached the remote (a DNS failure, or a dial-phase connection
// refused/reset). Only those are clean, definite failures; every other transport
// error may straddle the send and is treated as ambiguous (ErrAfterTransmit).
func isPreTransmit(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return false
}

// resultToTask decodes a Task-bearing JSON-RPC result into a TaskResult. In v1.0 a
// SendMessage result is the SendMessageResponse ONEOF — exactly one of a "task" or
// a "message" member (a2a.proto) — while GetTask / CancelTask return the bare Task
// object directly, so both wrapped and bare shapes are first-class here. A bare
// object with a role is a pre-1.0 synchronous Message reply (lenient parse).
func resultToTask(result json.RawMessage) (TaskResult, error) {
	if len(result) == 0 {
		return TaskResult{}, fmt.Errorf("%w: a2a call returned neither result nor error", ErrAfterTransmit)
	}
	// v1.0 SendMessageResponse oneof: {"task": {...}} | {"message": {...}}.
	var wrap struct {
		Task    json.RawMessage `json:"task"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(result, &wrap); err != nil {
		return TaskResult{}, fmt.Errorf("%w: a2a decode result: %v", ErrAfterTransmit, err)
	}
	if len(wrap.Task) > 0 && len(wrap.Message) > 0 {
		return TaskResult{}, fmt.Errorf("%w: a2a SendMessage response has multiple oneof values", ErrAfterTransmit)
	}
	switch {
	case len(wrap.Task) > 0:
		result = wrap.Task
	case len(wrap.Message) > 0:
		var m rpcResult
		if err := json.Unmarshal(wrap.Message, &m); err != nil {
			return TaskResult{}, fmt.Errorf("%w: a2a decode message result: %v", ErrAfterTransmit, err)
		}
		return messageReplyWithDigest(m, wrap.Message)
	}

	var r rpcResult
	if err := json.Unmarshal(result, &r); err != nil {
		return TaskResult{}, fmt.Errorf("%w: a2a decode task result: %v", ErrAfterTransmit, err)
	}
	switch {
	case r.Status.State != "":
		state := TaskState(r.Status.State)
		return TaskResult{
			TaskID:     r.ID,
			ResultKind: "task",
			ContextID:  r.ContextID,
			State:      state,
			Interrupt:  taskStateInterrupt(state),
			Terminal:   taskStateTerminal(state),
			TrustLevel: string(trustVerified),
			Detail:     "task " + r.Status.State,
		}, nil
	case r.Role != "":
		// Bare Message reply (pre-1.0 shape; v1.0 wraps it in the oneof above).
		return messageReplyWithDigest(r, result)
	default:
		// A 2xx whose result carries NEITHER a task status NOR a message role ({},
		// null, {"foo":"bar"}) is non-conformant: the remote accepted the POST and
		// returned something we cannot interpret as a definite task state. For a
		// GOVERNED emit that is AMBIGUOUS, not a false success — the effect MAY have
		// happened, so it settles UNKNOWN (never re-emitted) rather than a fabricated
		// TaskStateUnspecified "success" (D-05, item 8).
		return TaskResult{}, fmt.Errorf("%w: unrecognized a2a task result shape", ErrAfterTransmit)
	}
}

func messageReplyWithDigest(m rpcResult, raw json.RawMessage) (TaskResult, error) {
	result, err := messageReply(m)
	if err != nil {
		return TaskResult{}, err
	}
	digest, err := canonicalReplyDigest(raw)
	if err != nil {
		return TaskResult{}, fmt.Errorf("%w: a2a Message digest: %v", ErrAfterTransmit, err)
	}
	result.MessageDigest = digest
	return result, nil
}

// messageReply maps a synchronous Message reply (the agent answered without opening
// a Task) to a completed TaskResult.
func messageReply(m rpcResult) (TaskResult, error) {
	if m.Role != roleAgent || !validReplyIdentifier(m.MessageID) ||
		!validReplyIdentifier(m.ContextID) ||
		(m.TaskID != "" && !validReplyIdentifier(m.TaskID)) {
		return TaskResult{}, fmt.Errorf("%w: message result has an invalid agent identity", ErrAfterTransmit)
	}
	parts, err := projectMessageResultParts(m.Parts)
	if err != nil {
		return TaskResult{}, fmt.Errorf("%w: a2a message result: %v", ErrAfterTransmit, err)
	}
	return TaskResult{
		TaskID:        m.MessageID,
		ResultKind:    "message",
		MessageID:     m.MessageID,
		MessageTaskID: m.TaskID,
		MessageParts:  parts,
		ContextID:     m.ContextID,
		State:         TaskStateCompleted,
		Terminal:      true,
		TrustLevel:    string(trustVerified),
		Detail:        "synchronous message reply",
	}, nil
}

func projectMessageResultParts(rawParts []json.RawMessage) ([]MessageResultPart, error) {
	if len(rawParts) == 0 || len(rawParts) > maxMessageResultParts {
		return nil, fmt.Errorf("message result has an invalid part count")
	}
	parts := make([]MessageResultPart, 0, len(rawParts))
	total := 0
	for _, raw := range rawParts {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if len(raw) == 0 || decoder.Decode(&value) != nil {
			return nil, fmt.Errorf("message result has an invalid part")
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("canonicalize message result part: %w", err)
		}
		total += len(canonical)
		if total > maxMessageResultWireBytes {
			return nil, fmt.Errorf("message result parts exceed the wire bound")
		}
		digest := sha256.Sum256(canonical)
		digestText := hex.EncodeToString(digest[:])
		projected := MessageResultPart{
			Kind: "data", Reference: "a2a-part:" + digestText, Digest: digestText,
		}
		var part struct {
			Text *string         `json:"text"`
			Raw  *string         `json:"raw"`
			URL  *string         `json:"url"`
			Data json.RawMessage `json:"data"`
			// File is the pre-v1.0 nested form retained only as a lenient
			// compatibility fallback. v1.0.1 Part uses flat raw/url members.
			File *struct {
				URI string `json:"uri"`
			} `json:"file"`
		}
		if err := json.Unmarshal(canonical, &part); err != nil {
			return nil, fmt.Errorf("decode message result part: %w", err)
		}
		kinds := 0
		if part.Text != nil {
			kinds++
		}
		if part.Raw != nil {
			kinds++
		}
		if part.URL != nil {
			kinds++
		}
		if len(part.Data) != 0 {
			kinds++
		}
		if part.File != nil {
			kinds++
		}
		if kinds != 1 {
			return nil, fmt.Errorf("message result part has an unsupported shape")
		}
		switch {
		case part.Text != nil:
			text, ok := sanitizeMessageResultText(*part.Text)
			if !ok {
				return nil, fmt.Errorf("message result text part is invalid")
			}
			projected.Kind, projected.Text, projected.Reference = "text", text, ""
		case part.Raw != nil:
			if _, err := base64.StdEncoding.Strict().DecodeString(*part.Raw); err != nil {
				return nil, fmt.Errorf("message result raw Part is not canonical base64")
			}
			projected.Kind = "file"
		case part.URL != nil:
			reference, ok := sanitizeMessageResultReference(*part.URL, digestText)
			if !ok {
				return nil, fmt.Errorf("message result URL Part is invalid")
			}
			projected.Kind, projected.Reference = "file", reference
		case part.File != nil:
			reference, ok := sanitizeMessageResultReference(part.File.URI, digestText)
			if !ok {
				return nil, fmt.Errorf("message result file reference is invalid")
			}
			projected.Kind, projected.Reference = "file", reference
		}
		parts = append(parts, projected)
	}
	return parts, nil
}

func validReplyIdentifier(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 ||
		!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func sanitizeMessageResultText(value string) (string, bool) {
	if value == "" || len(value) > maxMessageResultTextBytes || !utf8.ValidString(value) {
		return "", false
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return -1
	}, value)
	if strings.TrimSpace(value) == "" || len(value) > maxMessageResultTextBytes {
		return "", false
	}
	return value, true
}

func sanitizeMessageResultReference(raw, digest string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || !utf8.ValidString(raw) ||
		strings.ContainsAny(raw, "\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "a2a-part:" + digest, true
	}
	switch strings.ToLower(parsed.Scheme) {
	case "artifact", "urn":
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "a2a-part:" + digest, true
		}
		return raw, true
	case "https":
		if parsed.User != nil || parsed.Host == "" {
			return "a2a-part:" + digest, true
		}
		parsed.RawQuery, parsed.ForceQuery, parsed.Fragment = "", false, ""
		return parsed.String(), true
	default:
		return "a2a-part:" + digest, true
	}
}

// applyAuth attaches the operator's out-of-band auth headers. They go ONLY on the
// HTTP request (never in the A2A/JSON-RPC payload) — the enterprise MUST.
func (c *Client) applyAuth(req *http.Request) {
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

// httpAuthError maps the A2A-mandated auth status codes to explicit errors (401 =
// missing credentials, 403 = insufficient authorization) so the caller never
// mistakes an auth refusal for a transport hiccup.
func httpAuthError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("a2a: 401 unauthorized (out-of-band credentials missing or invalid)")
	case http.StatusForbidden:
		return fmt.Errorf("a2a: 403 forbidden (authenticated identity lacks authorization)")
	default:
		return nil
	}
}

// newID returns a short random hex id (message/request correlation). It is not a
// secret; it only needs to be unique per call.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic; fall back to a time-seeded value so a
		// correlation id is still produced (uniqueness, not secrecy, is the property).
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// --- JSON-RPC 2.0 wire types (A2A v1.0 binding) ---------------------------------

type jsonrpcRequest struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      string     `json:"id"`
	Method  string     `json:"method"`
	Params  sendParams `json:"params"`
}

type sendParams struct {
	// Tenant is the opaque routing id of the SELECTED AgentInterface, echoed on
	// every request to that interface (a2a.proto SendMessageRequest.tenant: "Must
	// match the `tenant` value from the selected `AgentInterface`"). Empty when the
	// interface declares none.
	Tenant  string     `json:"tenant,omitempty"`
	Message a2aMessage `json:"message"`
}

// a2aMessage is the A2A v1.0 Message. Part is unified (discriminated by field
// presence, not a `kind` tag) — a text part is simply {"text": "..."}.
type a2aMessage struct {
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aPart struct {
	Text string `json:"text,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonrpcError   `json:"error"`
}

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"` // google.rpc.Status / ErrorInfo (not surfaced verbatim)
}

// rpcResult is the union of the two SendMessage result shapes (Task | Message).
type rpcResult struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	TaskID    string `json:"taskId"`
	Status    struct {
		State string `json:"state"`
	} `json:"status"`
	Role      string            `json:"role"`      // present on a Message result
	MessageID string            `json:"messageId"` // present on a Message result
	Parts     []json.RawMessage `json:"parts"`
}

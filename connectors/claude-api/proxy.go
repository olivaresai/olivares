// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// proxy.go is the inline inference PEP's PROTOCOL SHELL: a thin, identity-blind
// reverse proxy that presents Anthropic's POST /v1/messages contract on its own socket
// and FORWARDS an admitted request upstream with the OPERATOR's credential — the
// sibling of the MCP Resource-Server PEP (connectors/mcp) and the Claude Code hooks PEP
// (connectors/claude). It owns the wire format, the SHA-256 fingerprinting of the
// request/response bodies (minimal data, docs/SECURITY-HARDENING.md) and the SSE relay; it owns NO
// allow/deny logic.
//
// The GOVERNED decision (authenticate the inbound credential → kill-switch → residency
// → model-access → local content gates (DLP/firewall/ceilings) → count_tokens sizing →
// budget → record) is the injected ProxyDecider
// the composition root (cmd/olivares, AGPL) implements against /core + /modules — which
// this Apache connector may not import (the open-core boundary, LICENSING.md;
// scripts/check-boundary.sh). A nil decider, a decider error, or a malformed request all
// fail CLOSED here (deny), exactly like the hooks PEP: this is the deliberately-
// interposed enforcement posture an operator opts into (a custom ANTHROPIC_BASE_URL
// pointed at this proxy), the INVERSE of the product's read-first default — so it must
// never wave a request through on doubt.
//
// Credential separation (no passthrough): the upstream call uses the operator's
// Inference credential (cfg.APIKey), NEVER the inbound bearer. The bearer reaches only
// the decider (for identity resolution) and is otherwise structurally unreachable from
// the forward path — the same guarantee as mcpUpstreamForwarder.

package claudeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// maxProxyRequestBody bounds an inbound /v1/messages body. A prompt can be large (long
// transcripts, tool results), so this is generous — but bounded, so a hostile caller
// cannot exhaust memory.
const maxProxyRequestBody = 16 << 20 // 16 MiB

// defaultMaxResponseBufferBytes bounds a streamed response held for preventive inspection
// when the decider does not supply a policy-specific ceiling.
const defaultMaxResponseBufferBytes int64 = 16 << 20 // 16 MiB

const responseBufferCeilingReason = "response withheld: exceeded governed inspection buffer ceiling"

var errResponseBufferCeiling = errors.New(responseBufferCeilingReason)

const invalidBatchBodyReason = "request body is not a valid Message Batches request"

// maxBatchRequestBody bounds an inbound /v1/messages/batches body at Anthropic's documented
// per-batch ceiling (256 MB) so a VALID large batch is forwarded, not silently truncated —
// but still bounded, so a hostile caller cannot exhaust memory. A body over the cap is
// rejected with a clear 413 (not a misleading 400 from a truncated parse).
const maxBatchRequestBody = 256 << 20 // 256 MiB (Anthropic's batch ceiling)

// maxBatchEntries caps the entries of one /v1/messages/batches submission at Anthropic's
// documented per-batch limit, 100,000 requests or 256 MB, whichever is reached first
// (https://platform.claude.com/docs/en/build-with-claude/batch-processing). The entries are
// counted before they are decoded and before the decider runs, so a batch the upstream would
// refuse takes no decode or admission work. It is deliberately not configurable: a higher
// value only admits batches the upstream refuses.
const maxBatchEntries = 100_000

// messagesProxyPath / batchesProxyPath are the paths the proxy serves: the synchronous
// Messages contract and the asynchronous Message Batches submit (both governed by the SAME
// decider chain — the batch routes each entry's params through it per-entry).
const (
	messagesProxyPath = "/v1/messages"
	batchesProxyPath  = "/v1/messages/batches"
)

// ProxyDecider is the GOVERNED decision seam for the inline /v1/messages proxy.
// The composition root implements it; this connector NEVER imports /core or /modules.
// It is identity-blind: bearer is opaque here and resolved to a principal by the decider.
type ProxyDecider interface {
	// Authorize runs the PRE-forward governed gate chain for one inbound request on the
	// proxy's (fixed) surface. A non-nil-but-!Allow result is a deny (the connector writes
	// the mapped status). On allow it returns the GOVERNED request to forward (identical
	// today; a future request-redaction path may differ) and an opaque Session the
	// connector round-trips to Finalize.
	Authorize(ctx context.Context, req MessageRequest, bearer string) ProxyDecision
	// Finalize runs the POST-forward steps (response DLP, cost reconciliation, ledger
	// anchor) over the accumulated response and the I/O fingerprints. It may BLOCK the
	// response — but a Block verdict only takes effect when the decision asked the
	// connector to BUFFER (ProxyDecision.BufferResponse); a streamed-through response
	// cannot be un-sent (the verdict is then a detective finding only). The connector calls
	// it once for every forward that ran, including one whose response it then refuses to
	// relay (the inspection buffer ceiling, an encode failure).
	Finalize(ctx context.Context, sess any, out ProxyForwardResult) ProxyResponseVerdict

	// AuthorizeBatch runs the governed gate chain over a POST /v1/messages/batches
	// submission. Each entry's params is evaluated by the SAME per-request chain Authorize
	// uses (per-entry deny-closed): a single denied entry denies the WHOLE batch — nothing
	// is forwarded. On allow it returns the GOVERNED entries to forward (an entry may carry
	// a tool-rewrite) and an opaque Session the connector round-trips to FinalizeBatch. A
	// zero ProxyBatchDecision (Allow=false) fails closed.
	AuthorizeBatch(ctx context.Context, requests []BatchRequest, bearer string) ProxyBatchDecision
	// FinalizeBatch runs the post-forward steps for a batch submission (the ledger outcome
	// anchor). A batch CREATE response carries no model output — the async results are
	// fetched out of band — so there is no response DLP / cost reconciliation here; the
	// per-entry request-side chain already ran in AuthorizeBatch.
	FinalizeBatch(ctx context.Context, sess any, out ProxyBatchForwardResult)
}

// ProxyBatchDecision is the PRE-forward verdict for a whole batch submission. Its zero value
// is a DENY (Allow=false), so a decider that returns a zero value on a path it forgot still
// fails closed.
type ProxyBatchDecision struct {
	Allow     bool
	Status    int    // HTTP status on a deny (the denying entry's mapped status)
	ErrorType string // Anthropic-style error type on a deny
	Reason    string // short, non-sensitive (names the denied entry index/custom_id, not policy internals)
	// Headers are optional response headers emitted on a deny. The shell does not
	// interpret them; governed deciders use this for protocol-alignment hints.
	Headers map[string]string
	// Requests is the GOVERNED set of entries to forward (each entry's params validated /
	// possibly tool-rewritten by the decider). On a deny it is ignored. Retained for the
	// legacy fallback forward; when Prepared is set the forward uses its frozen bytes.
	Requests []BatchRequest
	// Prepared is the FROZEN batch envelope the decider authorized (F3): the exact
	// submission bytes to forward. When set, the forward sends it VERBATIM; a zero value
	// falls back to re-serializing Requests via CreateBatchRaw (legacy path) — unless any
	// entry carries an accepted MCP binding, which refuses with mcp_binding_changed.
	Prepared PreparedBatch
	// Session is opaque per-submission state the connector round-trips to FinalizeBatch.
	Session any
}

// ProxyBatchForwardResult is the post-forward outcome the connector hands FinalizeBatch.
type ProxyBatchForwardResult struct {
	// Batch is the created batch resource (zero on an upstream error).
	Batch Batch
	// ReqSHA/ReqBytes fingerprint the INBOUND batch body; Entries is the entry count.
	ReqSHA   []byte
	ReqBytes int64
	Entries  int
	// EffectiveSHA is the digest of the FROZEN envelope actually forwarded (F3);
	// empty on the legacy (unprepared) path.
	EffectiveSHA []byte
	// UpstreamStatus is the HTTP status the upstream returned (0 when the call did not
	// complete); UpstreamErr is set on an upstream/transport failure.
	UpstreamStatus int
	UpstreamErr    bool
}

// batchEnvelope is the POST /v1/messages/batches request body the connector parses to gate
// each entry. Only the requests array is read; it is re-serialized from the governed entries.
type batchEnvelope struct {
	Requests []BatchRequest `json:"requests"`
}

// ProxyDecision is the PRE-forward verdict. Its zero value is a DENY (Allow=false), so a
// decider that returns a zero value on a path it forgot still fails closed.
type ProxyDecision struct {
	Allow bool
	// Status is the HTTP status to return on a deny (e.g. 401/402/403/429/400/503).
	Status int
	// ErrorType is the Anthropic-style error type returned on a deny (e.g.
	// "permission_error", "rate_limit_error"); "" picks a default for the status.
	ErrorType string
	// Reason is a short, non-sensitive explanation (never enumerates policy internals).
	Reason string
	// Headers are optional response headers emitted on a deny. The shell stays
	// identity-blind and protocol-pure; the governed decider owns their meaning.
	Headers map[string]string
	// Request is the GOVERNED request to forward upstream (the decider may have validated
	// or, in a future version, redacted it). On a deny it is ignored. It is retained for the
	// audit model label + the beta-header/stream semantics; when Prepared is set (F3)
	// the FORWARD uses Prepared's frozen bytes, never a re-marshal of this struct.
	Request MessageRequest
	// Prepared is the FROZEN wire artifact the decider authorized (F3): the exact bytes
	// to forward, so the octets sent upstream provably equal what the decision was taken over
	// and the ledger digest committed to. When set (the governed path always sets it on an
	// allow), the forward sends it VERBATIM with no preflight/re-marshal; a zero value falls
	// back to marshaling Request (legacy path, e.g. a decider that predates S3) — unless
	// Request carries an accepted MCP binding, which refuses with mcp_binding_changed.
	Prepared PreparedRequest
	// BufferResponse asks the connector to buffer the full (streamed) response and run
	// Finalize BEFORE relaying any byte, so a Finalize Block actually withholds the
	// response (true fail-closed response DLP). Default false: relay live, Finalize after
	// (detective). Ignored for non-streaming requests (always buffered by nature).
	BufferResponse bool
	// MaxResponseBufferBytes bounds the framed SSE bytes held for preventive inspection.
	// Zero uses the connector's safe default. Ignored unless BufferResponse is true.
	MaxResponseBufferBytes int64
	// Session is opaque per-request state the connector round-trips to Finalize.
	Session any
}

// ProxyForwardResult is the post-forward outcome the connector hands Finalize.
type ProxyForwardResult struct {
	// Response is the accumulated model response. It is zero on an upstream error the proxy
	// relays (relayUpstreamError: a blocking call, or a buffered stream that failed before or
	// during its answer). It is partial when a passthrough stream failed mid-way, or when the
	// connector abandoned a buffered stream at the inspection buffer ceiling.
	Response MessageResponse
	// ReqSHA/ReqBytes fingerprint the INBOUND request body (what the caller submitted and
	// the request DLP inspected). RespSHA/RespBytes fingerprint the response body the
	// connector relays, or holds for Finalize to judge before relaying it (a body Finalize
	// then blocks keeps the fingerprint its verdict was taken over); on a passthrough stream
	// that fails mid-way they cover the events already relayed. They are empty when the
	// connector refuses a response on its own (the inspection buffer ceiling, an encode
	// failure): the caller never receives those bytes. On an upstream error the proxy relays
	// (relayUpstreamError), RespSHA fingerprints the error text the proxy observed and
	// RespBytes is 0.
	ReqSHA    []byte
	ReqBytes  int64
	RespSHA   []byte
	RespBytes int64
	// EffectiveSHA is the digest of the FROZEN bytes actually forwarded upstream (F3):
	// SHA256(Prepared.Body). It equals ReqSHA only when the governed request serialized
	// byte-identically to the inbound body; the ledger records both so evidence proves the
	// decision bound the exact forwarded octets. Empty on the legacy (unprepared) path.
	EffectiveSHA []byte
	// Streamed reports whether the response was an SSE stream.
	Streamed bool
	// UpstreamStatus is the HTTP status the upstream answered with. On an upstream error the
	// proxy relays (relayUpstreamError) it is the error's status, or 502 for a transport
	// failure; only a passthrough stream that fails reports 0, because the proxy has already
	// begun its own 200 stream to the caller. UpstreamErr is set on an upstream/transport
	// failure. A call that ran and that the connector then withheld (a stream abandoned at the
	// inspection buffer ceiling, a response it could not encode) is not one: it reports the
	// upstream's 200, no UpstreamErr and its Response, so the call's cost is measured.
	UpstreamStatus int
	UpstreamErr    bool
}

// ProxyResponseVerdict is Finalize's outcome. Block withholds the response (only
// effective in buffer mode).
type ProxyResponseVerdict struct {
	Block     bool
	Status    int
	ErrorType string
	Reason    string
}

// ProxyAuditor records each proxied call (minimal data) for the SOC trail. It NEVER
// receives the bearer, the prompt or the response bytes — only the decision and the
// byte counts/fingerprint-free summary. Optional (nil = no SOC log). The tamper-evident
// ledger anchor is the decider's job (Finalize), not the auditor's.
type ProxyAuditor interface {
	Record(ctx context.Context, ev ProxyAuditEvent)
}

// ProxyAuditEvent is the minimal-data SOC record of one proxied call.
type ProxyAuditEvent struct {
	Decision       string // allow | deny | blocked-response | upstream-error
	Reason         string
	Model          string
	Streamed       bool
	UpstreamStatus int
	// ReqBytes is the inbound body length the handler already measured (ReadAll).
	// It is not the forwarded size: request rewriting may send different octets.
	ReqBytes  int64
	RespBytes int64
}

// MessagesProxy is the inline /v1/messages enforcement endpoint. It owns the wire
// protocol and the deny-closed defaults; the decision is the injected ProxyDecider's and
// the forward uses the operator-credentialed Inference.
type MessagesProxy struct {
	inf     *Inference
	decider ProxyDecider
	auditor ProxyAuditor
	now     func() time.Time
	// encodeResponse renders a blocking response for relay; nil means json.Marshal. A
	// response decoded from the upstream always re-encodes, so only a test sets it, to
	// reach the encode-failure exit.
	encodeResponse func(any) ([]byte, error)
	// decodeBatch decodes a batch body into its entries; nil means json.Unmarshal. Only a
	// test sets it, to count the full decodes a batch submission causes.
	decodeBatch func([]byte, any) error
}

var _ http.Handler = (*MessagesProxy)(nil)

// NewMessagesProxy builds the inline messages proxy. inf is the operator-credentialed
// upstream client (its gateway fixes the surface this proxy fronts); decider is the
// governed decision (a nil decider denies every call — a visible deny-closed posture).
func NewMessagesProxy(inf *Inference, decider ProxyDecider, auditor ProxyAuditor, now func() time.Time) *MessagesProxy {
	if now == nil {
		now = time.Now
	}
	return &MessagesProxy{inf: inf, decider: decider, auditor: auditor, now: now}
}

// ServeHTTP routes one inbound call by path. POST only; the proxy serves the synchronous
// Messages contract and the asynchronous Message Batches submit — any other path is a 404,
// any malformed or unauthorized request answers a deny (it is the governed surface, a
// request it cannot understand or authorize is blocked, never forwarded).
func (p *MessagesProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		p.writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	switch strings.TrimRight(r.URL.Path, "/") {
	case messagesProxyPath:
		p.serveMessages(w, r)
	case batchesProxyPath:
		p.serveBatch(w, r)
	default:
		p.writeError(w, http.StatusNotFound, "not_found_error", "this proxy serves only POST /v1/messages and POST /v1/messages/batches")
	}
}

// serveMessages gates and forwards one inbound /v1/messages call.
func (p *MessagesProxy) serveMessages(w http.ResponseWriter, r *http.Request) {
	// One byte past the cap distinguishes a TRUNCATED (too-large) body from a malformed one.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyRequestBody+1))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
	}
	if int64(len(body)) > maxProxyRequestBody {
		p.writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body exceeds the proxy size limit")
		return
	}
	reqSHA := sha256.Sum256(body)
	reqBytes := int64(len(body))

	var req MessageRequest
	if jerr := json.Unmarshal(body, &req); jerr != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "request body is not a valid Messages request")
		return
	}

	if p.decider == nil {
		// The governed surface is mounted without a decider: refuse rather than forward.
		// Unreachable in production wiring (the composition root mounts WITH a decider), but
		// it makes the deny-closed contract total.
		p.audit(r.Context(), ProxyAuditEvent{Decision: "deny", Reason: "governed enforcement not wired", Model: req.Model, ReqBytes: reqBytes})
		p.writeError(w, http.StatusServiceUnavailable, "api_error", "governed enforcement is not wired (deny-closed)")
		return
	}

	dec := p.decider.Authorize(r.Context(), req, bearerToken(r))
	if !dec.Allow {
		status := dec.Status
		if status == 0 {
			status = http.StatusForbidden
		}
		p.audit(r.Context(), ProxyAuditEvent{Decision: "deny", Reason: dec.Reason, Model: req.Model, ReqBytes: reqBytes})
		p.writeDecisionError(w, status, dec.ErrorType, firstNonEmpty(dec.Reason, "request denied by Olivares governance policy"), dec.Headers)
		return
	}
	// A governed request that carries an accepted MCP binding (a declaration or its absence)
	// is forwardable ONLY as the frozen, verified artifact: the legacy fallback would
	// re-serialize it unverified. Refuse before any response or SSE header is written.
	if dec.Prepared.IsZero() && dec.Request.mcp != nil {
		p.refuseUnpreparedBinding(w, r, req.Model, reqBytes)
		return
	}

	// Dispatch blocking vs SSE on the FROZEN artifact's stream flag when the decider set one
	// (F3 — the artifact is the single source of truth for what is forwarded); fall back
	// to dec.Request.Stream on the legacy (unprepared) path. This keeps the transport used
	// (PostJSON vs PostStream) in lockstep with the frozen "stream" field, so a mismatch can
	// never send a stream body over the blocking decode path or vice versa.
	stream := dec.Request.Stream
	if !dec.Prepared.IsZero() {
		stream = dec.Prepared.Stream()
	}
	if stream {
		p.forwardStream(w, r, dec, reqSHA[:], reqBytes)
		return
	}
	p.forwardBlocking(w, r, dec, reqSHA[:], reqBytes)
}

// serveBatch gates and forwards one POST /v1/messages/batches submission. Each entry's
// params is governed by the decider's per-entry chain (deny-closed 2026-06-19): a
// single denied entry rejects the WHOLE batch — nothing is forwarded. On allow the governed
// entries are submitted and the upstream response is relayed VERBATIM (raw bytes), so the
// caller receives Anthropic's exact Batch object (request_counts, expires_at, …). The async
// results are fetched out of band and are not on this synchronous governed path.
func (p *MessagesProxy) serveBatch(w http.ResponseWriter, r *http.Request) {
	// Read one byte past the cap so a TRUNCATED body is detected as "too large" (a clear 413)
	// rather than failing the JSON parse as a misleading 400.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBatchRequestBody+1))
	if err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read batch request body")
		return
	}
	if int64(len(body)) > maxBatchRequestBody {
		p.writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "batch body exceeds the proxy size limit")
		return
	}
	reqSHA := sha256.Sum256(body)
	reqBytes := int64(len(body))

	// Check the body and count its entries before decoding them. A body that is not valid
	// JSON is refused with the 400 here, so its refusal does not rest on how the decoder reads
	// an invalid body. A valid body is scanned without decoding anything: an over-limit batch
	// is refused with the 413, and a body whose top level the scan cannot read as an object
	// with the 400. None of them reaches the full decode or the decider (no admission, ledger
	// row or forward).
	if !json.Valid(body) {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", invalidBatchBodyReason)
		return
	}
	largest, ok := scanBatchEntries(body, maxBatchEntries+1)
	if !ok {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", invalidBatchBodyReason)
		return
	}
	if largest > maxBatchEntries {
		p.refuseOverEntryLimit(w, largest)
		return
	}
	decode := json.Unmarshal
	if p.decodeBatch != nil {
		decode = p.decodeBatch
	}
	var env batchEnvelope
	if jerr := decode(body, &env); jerr != nil {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", invalidBatchBodyReason)
		return
	}
	if len(env.Requests) == 0 {
		p.writeError(w, http.StatusBadRequest, "invalid_request_error", "batch contains no requests")
		return
	}
	// The decode binds Requests only to an array that is a direct value of the top-level
	// object, and the scan above refused any such array over the limit, so this check does not
	// fire; it keeps the limit a guarantee for admission that does not rest on the scan.
	if n := len(env.Requests); n > maxBatchEntries {
		p.refuseOverEntryLimit(w, n)
		return
	}

	if p.decider == nil {
		p.audit(r.Context(), ProxyAuditEvent{Decision: "deny", Reason: "governed enforcement not wired", ReqBytes: reqBytes})
		p.writeError(w, http.StatusServiceUnavailable, "api_error", "governed enforcement is not wired (deny-closed)")
		return
	}

	dec := p.decider.AuthorizeBatch(r.Context(), env.Requests, bearerToken(r))
	if !dec.Allow {
		status := dec.Status
		if status == 0 {
			status = http.StatusForbidden
		}
		p.audit(r.Context(), ProxyAuditEvent{Decision: "deny", Reason: dec.Reason, ReqBytes: reqBytes})
		p.writeDecisionError(w, status, dec.ErrorType, firstNonEmpty(dec.Reason, "batch denied by Olivares governance policy"), dec.Headers)
		return
	}
	if dec.Prepared.IsZero() && batchBound(dec.Requests) {
		p.refuseUnpreparedBinding(w, r, "", reqBytes)
		return
	}

	batch, raw, effSHA, ferr := p.forwardBatch(r.Context(), dec)
	if ferr != nil {
		p.relayBatchUpstreamError(w, r, dec, reqSHA[:], effSHA, reqBytes, len(env.Requests), ferr)
		return
	}
	p.finalizeBatch(r.Context(), dec, ProxyBatchForwardResult{
		Batch: batch, ReqSHA: reqSHA[:], ReqBytes: reqBytes, Entries: len(env.Requests), EffectiveSHA: effSHA,
		UpstreamStatus: http.StatusOK,
	})
	p.audit(r.Context(), ProxyAuditEvent{Decision: "allow", Model: batch.ID, UpstreamStatus: http.StatusOK, ReqBytes: reqBytes, RespBytes: int64(len(raw))})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// scanBatchEntries counts the entries of every array that is a direct value of the top-level
// object in body, whatever its key, and returns the largest count. It stops as soon as one
// count reaches stop. It decodes and holds nothing: it reads the bytes once, tracking only the
// nesting depth and the string and escape state, so it allocates nothing whatever the size of
// a value. Nested arrays are not counted, and each occurrence of a repeated key is an array of
// its own. The decode binds Requests only to one of these arrays, so the largest count bounds
// the entries it can decode, with no key compare to keep in step with encoding/json. body
// must already have passed json.Valid. ok is false when its top level is not an object or the
// bytes are not consistent with valid JSON; the caller refuses such a body (fail closed).
func scanBatchEntries(body []byte, stop int) (largest int, ok bool) {
	i := skipJSONSpace(body, 0)
	if i == len(body) || body[i] != '{' {
		return 0, false
	}
	depth := 0
	inString, escaped := false, false
	inTopArray := false // inside an array that is a direct value of the top-level object
	count := 0
	for ; i < len(body); i++ {
		c := body[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth == 2 && c == '[' {
				inTopArray, count = true, 0
				if j := skipJSONSpace(body, i+1); j < len(body) && body[j] != ']' {
					count = 1
				}
			}
		case ',':
			if depth == 2 && inTopArray {
				count++
			}
		case '}', ']':
			if depth == 2 {
				inTopArray = false
			}
			depth--
			if depth == 0 {
				return largest, true
			}
		}
		if count > largest {
			largest = count
			if largest >= stop {
				return largest, true
			}
		}
	}
	return 0, false // the top-level object never closed
}

// skipJSONSpace returns the index of the first byte at or after i that is not JSON whitespace.
func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// refuseOverEntryLimit answers a batch over maxBatchEntries with a 413 that names the limit
// and the count reached, never the body.
func (p *MessagesProxy) refuseOverEntryLimit(w http.ResponseWriter, counted int) {
	p.writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error",
		fmt.Sprintf("batch contains at least %d requests; the limit is %d requests per batch", counted, maxBatchEntries))
}

// forwardBatch submits the governed batch upstream: the FROZEN prepared envelope when the
// decider set one (F3 — the octets are exactly the digested octets), else the legacy
// re-serialize of dec.Requests via CreateBatchRaw. Returns the decoded Batch, the raw upstream
// bytes to relay, and the effective digest (empty on the legacy path).
func (p *MessagesProxy) forwardBatch(ctx context.Context, dec ProxyBatchDecision) (Batch, []byte, []byte, error) {
	if dec.Prepared.IsZero() && batchBound(dec.Requests) {
		return Batch{}, nil, nil, mcpRefusal(MCPDenyBindingChanged)
	}
	if !dec.Prepared.IsZero() {
		d := dec.Prepared.Digest()
		batch, raw, err := p.inf.ForwardPreparedBatch(ctx, dec.Prepared)
		return batch, raw, d[:], err
	}
	batch, raw, err := p.inf.CreateBatchRaw(ctx, dec.Requests)
	return batch, raw, nil, err
}

// relayBatchUpstreamError relays an upstream/transport error from the batch submit to the
// caller faithfully (the Anthropic error status+body when it is an *APIError, else a 502),
// recording the outcome for the ledger first.
func (p *MessagesProxy) relayBatchUpstreamError(w http.ResponseWriter, r *http.Request, dec ProxyBatchDecision, reqSHA, effSHA []byte, reqBytes int64, entries int, err error) {
	var apiErr *modelprovider.APIError
	status := http.StatusBadGateway
	if errors.As(err, &apiErr) {
		status = apiErr.Status
	}
	p.finalizeBatch(r.Context(), dec, ProxyBatchForwardResult{
		ReqSHA: reqSHA, ReqBytes: reqBytes, Entries: entries, EffectiveSHA: effSHA, UpstreamStatus: status, UpstreamErr: true,
	})
	p.audit(r.Context(), ProxyAuditEvent{Decision: "upstream-error", Reason: "batch upstream error", UpstreamStatus: status, ReqBytes: reqBytes})
	if apiErr != nil && strings.TrimSpace(apiErr.Body) != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, apiErr.Body)
		return
	}
	p.writeError(w, status, "api_error", "upstream batch call failed")
}

// refuseUnpreparedBinding answers an allowed decision whose governed request carries an
// accepted MCP binding but no frozen artifact: a fixed 500 mcp_binding_changed, with no
// forward. Nothing was sent upstream, so there is no outcome to finalize; the deny is audited.
func (p *MessagesProxy) refuseUnpreparedBinding(w http.ResponseWriter, r *http.Request, model string, reqBytes int64) {
	reason := string(MCPDenyBindingChanged)
	p.audit(r.Context(), ProxyAuditEvent{Decision: "deny", Reason: reason, Model: model, ReqBytes: reqBytes})
	p.writeError(w, http.StatusInternalServerError, "api_error", reason)
}

// finalizeBatch runs the decider's post-forward batch steps; a nil decider is a no-op.
func (p *MessagesProxy) finalizeBatch(ctx context.Context, dec ProxyBatchDecision, out ProxyBatchForwardResult) {
	if p.decider == nil {
		return
	}
	p.decider.FinalizeBatch(ctx, dec.Session, out)
}

// forwardMessage forwards the governed request upstream: the FROZEN prepared artifact when
// the decider set one (F3 — the octets sent are exactly the digested octets), else the
// legacy re-marshal of dec.Request (a decider that predates S3). It returns the effective
// digest (empty on the legacy path). For a stream it drives onEvent; for blocking onEvent is
// nil.
func (p *MessagesProxy) forwardMessage(ctx context.Context, dec ProxyDecision, stream bool, onEvent func(StreamEvent) error) (MessageResponse, []byte, error) {
	if dec.Prepared.IsZero() && dec.Request.mcp != nil {
		return MessageResponse{}, nil, mcpRefusal(MCPDenyBindingChanged)
	}
	if !dec.Prepared.IsZero() {
		d := dec.Prepared.Digest()
		if stream {
			resp, err := p.inf.ForwardPreparedStream(ctx, dec.Prepared, onEvent)
			return resp, d[:], err
		}
		resp, err := p.inf.ForwardPrepared(ctx, dec.Prepared)
		return resp, d[:], err
	}
	if stream {
		resp, err := p.inf.StreamMessage(ctx, dec.Request, onEvent)
		return resp, nil, err
	}
	resp, err := p.inf.CreateMessage(ctx, dec.Request)
	return resp, nil, err
}

// forwardBlocking forwards a non-streaming request and relays the JSON response, after
// running Finalize (which may block the response — always effective here, the body is
// fully in hand before any byte is written).
func (p *MessagesProxy) forwardBlocking(w http.ResponseWriter, r *http.Request, dec ProxyDecision, reqSHA []byte, reqBytes int64) {
	resp, effSHA, err := p.forwardMessage(r.Context(), dec, false, nil)
	if err != nil {
		p.relayUpstreamError(w, r, dec, reqSHA, effSHA, reqBytes, err, false)
		return
	}
	encode := json.Marshal
	if p.encodeResponse != nil {
		encode = p.encodeResponse
	}
	out, merr := encode(resp)
	if merr != nil {
		// The upstream call completed: settle it with its response, the source of its
		// measured cost, then refuse. Nothing was relayed, so there is no response fingerprint.
		_ = p.finalize(r.Context(), dec, ProxyForwardResult{
			Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA,
			UpstreamStatus: http.StatusOK,
		})
		p.writeError(w, http.StatusBadGateway, "api_error", "could not encode upstream response")
		return
	}
	respSHA := sha256.Sum256(out)
	verdict := p.finalize(r.Context(), dec, ProxyForwardResult{
		Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA,
		RespSHA: respSHA[:], RespBytes: int64(len(out)), UpstreamStatus: http.StatusOK,
	})
	if verdict.Block {
		p.audit(r.Context(), ProxyAuditEvent{Decision: "blocked-response", Reason: verdict.Reason, Model: resp.Model, ReqBytes: reqBytes, RespBytes: int64(len(out))})
		status := verdict.Status
		if status == 0 {
			status = http.StatusForbidden
		}
		p.writeError(w, status, verdict.ErrorType, firstNonEmpty(verdict.Reason, "response withheld by Olivares DLP policy"))
		return
	}
	p.audit(r.Context(), ProxyAuditEvent{Decision: "allow", Model: resp.Model, UpstreamStatus: http.StatusOK, ReqBytes: reqBytes, RespBytes: int64(len(out))})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// forwardStream forwards a streaming request. The policy default is buffer-and-gate: it
// accumulates the stream, runs Finalize, and relays only if not blocked (preventive — true
// fail-closed response DLP, at the cost of streaming latency). A policy that lowers
// response DLP to off/flag (or disables the response gate) uses passthrough: relay each SSE
// event live and run Finalize after the stream closes (detective; bytes cannot be un-sent).
func (p *MessagesProxy) forwardStream(w http.ResponseWriter, r *http.Request, dec ProxyDecision, reqSHA []byte, reqBytes int64) {
	hash := sha256.New()
	var respBytes int64

	if dec.BufferResponse {
		maxResponseBufferBytes := dec.MaxResponseBufferBytes
		if maxResponseBufferBytes == 0 {
			maxResponseBufferBytes = defaultMaxResponseBufferBytes
		}
		var buf bytes.Buffer
		onEvent := func(ev StreamEvent) error {
			// Hash the SAME framed bytes that are buffered and relayed (and counted), so
			// RespSHA, RespBytes and the bytes the caller receives all fingerprint ONE
			// artifact — an auditor holding the received stream can reproduce the ledger
			// anchor. Hashing the unframed ev.Raw would commit a different byte sequence.
			if sseEventSize(ev) > maxResponseBufferBytes-respBytes {
				// Never retain or relay a partial governed response: abandon the upstream
				// stream and release the bytes accumulated before the ceiling was crossed.
				buf = bytes.Buffer{}
				return errResponseBufferCeiling
			}
			before := buf.Len()
			n := writeSSEEvent(&buf, ev)
			hash.Write(buf.Bytes()[before:])
			respBytes += int64(n)
			return nil
		}
		resp, effSHA, err := p.forwardMessage(r.Context(), dec, true, onEvent)
		if errors.Is(err, errResponseBufferCeiling) {
			// The upstream answered 200 and streamed until the proxy abandoned the stream: settle
			// the call as one that ran and was withheld, with the partial response whose usage
			// measures its cost, then refuse. The caller never receives the bytes held before the
			// ceiling, so the outcome carries no response fingerprint.
			_ = p.finalize(r.Context(), dec, ProxyForwardResult{
				Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA,
				Streamed: true, UpstreamStatus: http.StatusOK,
			})
			p.audit(r.Context(), ProxyAuditEvent{Decision: "blocked-response", Reason: responseBufferCeilingReason, Model: resp.Model, Streamed: true, ReqBytes: reqBytes, RespBytes: respBytes})
			p.writeError(w, http.StatusForbidden, "permission_error", responseBufferCeilingReason)
			return
		}
		if err != nil {
			p.relayUpstreamError(w, r, dec, reqSHA, effSHA, reqBytes, err, true)
			return
		}
		sum := hash.Sum(nil)
		verdict := p.finalize(r.Context(), dec, ProxyForwardResult{
			Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA,
			RespSHA: sum, RespBytes: respBytes, Streamed: true, UpstreamStatus: http.StatusOK,
		})
		if verdict.Block {
			p.audit(r.Context(), ProxyAuditEvent{Decision: "blocked-response", Reason: verdict.Reason, Model: resp.Model, Streamed: true, ReqBytes: reqBytes, RespBytes: respBytes})
			status := verdict.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			p.writeError(w, status, verdict.ErrorType, firstNonEmpty(verdict.Reason, "response withheld by Olivares DLP policy"))
			return
		}
		p.audit(r.Context(), ProxyAuditEvent{Decision: "allow", Model: resp.Model, Streamed: true, UpstreamStatus: http.StatusOK, ReqBytes: reqBytes, RespBytes: respBytes})
		p.beginSSE(w)
		_, _ = w.Write(buf.Bytes())
		flush(w)
		return
	}

	// Passthrough: relay live. Headers go out before the first event, so a later upstream
	// error can only be surfaced as an in-stream error event.
	p.beginSSE(w)
	flusher, _ := w.(http.Flusher)
	onEvent := func(ev StreamEvent) error {
		// Frame once, then write the SAME bytes to the caller AND the hash (and count
		// them), so RespSHA fingerprints exactly what was relayed (see the buffer-mode
		// note above) — not the unframed ev.Raw.
		var sb bytes.Buffer
		n := writeSSEEvent(&sb, ev)
		frame := sb.Bytes()
		hash.Write(frame)
		_, _ = w.Write(frame)
		respBytes += int64(n)
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	resp, effSHA, err := p.forwardMessage(r.Context(), dec, true, onEvent)
	if err != nil {
		// The stream already started (200 + headers sent): surface the error as an SSE
		// error event; we cannot change the status now.
		writeSSEError(w, err)
		flush(w)
		p.audit(r.Context(), ProxyAuditEvent{Decision: "upstream-error", Reason: "stream error", Streamed: true, ReqBytes: reqBytes, RespBytes: respBytes})
		// Still record the (partial) outcome for the ledger.
		sum := hash.Sum(nil)
		_ = p.finalize(r.Context(), dec, ProxyForwardResult{
			Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA, RespSHA: sum, RespBytes: respBytes,
			Streamed: true, UpstreamErr: true,
		})
		return
	}
	sum := hash.Sum(nil)
	// Finalize runs AFTER the response already streamed out: a Block here is detective
	// only (logged/recorded), it cannot withhold what the caller already received.
	_ = p.finalize(r.Context(), dec, ProxyForwardResult{
		Response: resp, ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA, RespSHA: sum, RespBytes: respBytes,
		Streamed: true, UpstreamStatus: http.StatusOK,
	})
	p.audit(r.Context(), ProxyAuditEvent{Decision: "allow", Model: resp.Model, Streamed: true, UpstreamStatus: http.StatusOK, ReqBytes: reqBytes, RespBytes: respBytes})
}

// relayUpstreamError relays an upstream/transport error to the caller faithfully (the
// Anthropic error status+body when it is an *APIError, else a 502), recording the
// outcome for the ledger first. Used only before any response byte was written. effSHA is the
// digest of the frozen bytes that WERE forwarded (F3): an upstream 4xx/5xx proves the
// body reached the model, so the outcome evidence must still bind what was sent.
func (p *MessagesProxy) relayUpstreamError(w http.ResponseWriter, r *http.Request, dec ProxyDecision, reqSHA, effSHA []byte, reqBytes int64, err error, streamed bool) {
	var apiErr *modelprovider.APIError
	status := http.StatusBadGateway
	bodySHA := sha256.Sum256([]byte(err.Error()))
	if errors.As(err, &apiErr) {
		status = apiErr.Status
	}
	_ = p.finalize(r.Context(), dec, ProxyForwardResult{
		ReqSHA: reqSHA, ReqBytes: reqBytes, EffectiveSHA: effSHA, RespSHA: bodySHA[:], Streamed: streamed,
		UpstreamStatus: status, UpstreamErr: true,
	})
	p.audit(r.Context(), ProxyAuditEvent{Decision: "upstream-error", Reason: "upstream error", Streamed: streamed, UpstreamStatus: status, ReqBytes: reqBytes})
	if apiErr != nil && strings.TrimSpace(apiErr.Body) != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, apiErr.Body)
		return
	}
	p.writeError(w, status, "api_error", "upstream inference call failed")
}

// finalize runs the decider's post-forward steps; a nil decider yields a no-block verdict.
func (p *MessagesProxy) finalize(ctx context.Context, dec ProxyDecision, out ProxyForwardResult) ProxyResponseVerdict {
	if p.decider == nil {
		return ProxyResponseVerdict{}
	}
	return p.decider.Finalize(ctx, dec.Session, out)
}

func (p *MessagesProxy) audit(ctx context.Context, ev ProxyAuditEvent) {
	if p.auditor != nil {
		p.auditor.Record(ctx, ev)
	}
}

// beginSSE writes the streaming response headers.
func (p *MessagesProxy) beginSSE(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flush(w)
}

// writeError renders an Anthropic-style error response. The message is non-sensitive
// (never the raw error / policy internals).
func (p *MessagesProxy) writeError(w http.ResponseWriter, status int, errType, message string) {
	p.writeDecisionError(w, status, errType, message, nil)
}

func (p *MessagesProxy) writeDecisionError(w http.ResponseWriter, status int, errType, message string, headers map[string]string) {
	if errType == "" {
		errType = defaultErrorType(status)
	}
	for k, v := range headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": message},
	})
}

// writeSSEEvent renders one decoded event back onto an SSE stream (event: <type> + data:
// <raw json>), returning the bytes written. It reconstructs the wire shape the Anthropic
// SDKs parse (event name + JSON data) from the decoded StreamEvent.
func writeSSEEvent(w io.Writer, ev StreamEvent) int {
	var b bytes.Buffer
	if ev.Type != "" {
		b.WriteString("event: ")
		b.WriteString(ev.Type)
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	if len(ev.Raw) > 0 {
		b.Write(ev.Raw)
	} else {
		b.WriteString("{}")
	}
	b.WriteString("\n\n")
	n, _ := w.Write(b.Bytes())
	return n
}

// sseEventSize returns the exact framed size writeSSEEvent will emit without allocating a
// second copy of the event just to enforce the preventive buffer ceiling.
func sseEventSize(ev StreamEvent) int64 {
	n := int64(len("data: ") + len("\n\n"))
	if ev.Type != "" {
		n += int64(len("event: ") + len(ev.Type) + len("\n"))
	}
	if len(ev.Raw) > 0 {
		return n + int64(len(ev.Raw))
	}
	return n + int64(len("{}"))
}

// writeSSEError emits a terminal SSE error event (best-effort) when an upstream error
// arrives after the stream already started.
func writeSSEError(w io.Writer, err error) {
	var apiErr *modelprovider.APIError
	msg := "upstream stream error"
	if errors.As(err, &apiErr) {
		msg = "upstream error"
	}
	payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": msg}})
	_, _ = io.WriteString(w, "event: error\ndata: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// bearerToken extracts the inbound bearer credential. The proxy passes it to the decider
// for principal resolution only; it is never retained, logged or used to forward.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// The Anthropic SDK uses x-api-key; accept it as the inbound credential too.
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func defaultErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return "api_error"
	default:
		if status == 402 {
			return "billing_error"
		}
		return "api_error"
	}
}

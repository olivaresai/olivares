// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

var ErrAfterTransmit = errors.New("a2a: failure after the request was transmitted; delivery ambiguous")

const (
	methodSendMessage = "SendMessage" // v1.0 rename of message/send
)

const emitBodyCap = 4 << 20 // 4 MiB

type Transport interface {
	Do(req *http.Request) (*http.Response, error)
}

type EmitConfig struct {
	TrustJWKS     []byte            // operator trust anchor (JWK Set); required for any emission
	Headers       map[string]string // out-of-band auth headers (e.g. Authorization) — HTTPS only, never in payload
	WellKnownPath string            // Agent Card discovery path; default /.well-known/agent-card.json
	Timeout       time.Duration     // per-call timeout; default 30s
	AllowInsecure bool              // default false: a non-HTTPS endpoint is refused (A2A spec MUST)
	Doer          Transport         // injected in tests; nil => default TLS 1.2+ client
}

type Client struct {
	anchorRaw     []byte
	headers       map[string]string
	wellKnownPath string
	timeout       time.Duration
	allowInsecure bool
	doer          Transport
}

type SendSpec struct {
	AgentName string // logical name (audit/label only)
	AgentURL  string // remote agent base URL (well-known card path appended) or direct card URL
	Text      string // the task instruction (the message content)
	Skill     string // optional skill id to target (carried as message metadata)
	ContextID string // optional A2A contextId to continue an existing conversation
}

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

func taskStateInterrupt(s TaskState) bool {
	return s == TaskStateInputReq || s == TaskStateAuthRequired
}
func taskStateTerminal(s TaskState) bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return true
	default:
		return false
	}
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

type DecisionAuditor interface {
	Record(ctx context.Context, dec DelegationDecision)
}

type DelegationDecision struct {
	Tenant      string
	AgentName   string
	Skill       string
	Scope       string
	PlanHash    string
	Allowed     bool
	Reason      string // short, non-sensitive (e.g. "allowlist deny", "gate not approved", "delegated")
	ApprovalRef string
	State       TaskState
	RequestedBy string
	TraceParent string
	// Objective is the delegation's purpose label (DelegateSpec.Objective) — the "with
	// what objective" dimension of the communication graph. Minimal data: a goal ref,
	// never the task text.
	Objective string
	// ChainDepth / ChainRoot describe the multi-agent lineage this delegation sits in:
	// the number of agents already in the chain and the correlation root. They let the
	// audit/observability layer reconstruct the multi-agent task tree (who delegated to
	// whom) without persisting any payload.
	ChainDepth int
	ChainRoot  string
	// Principal is the chain's ORIGINAL on-behalf-of principal (the RFC 8693 `sub`
	// position): who the multi-agent task is ultimately FOR, as distinct from
	// RequestedBy (the actor asking at THIS hop). "" when the lineage predates this
	// plane and no principal was propagated — reported unknown, never fabricated.
	Principal string
	At        time.Time
}

type nopAuditor struct{}

func (nopAuditor) Record(context.Context, DelegationDecision) {}

type DelegatorConfig struct {
	Emit      EmitConfig
	Allowlist *Allowlist
	Gate      DelegationGate
	Auditor   DecisionAuditor
	// ChainPolicy bounds multi-agent delegation lineages (max depth + no cycles,
	// chain.go). The zero value resolves to a safe default depth (never unbounded).
	ChainPolicy ChainPolicy
	Clock       func() time.Time
}

type Delegator struct {
	client      *Client
	allowlist   *Allowlist
	gate        DelegationGate
	auditor     DecisionAuditor
	chainPolicy ChainPolicy
	now         func() time.Time
}

type DelegateSpec struct {
	AgentName   string
	AgentURL    string
	Skill       string
	Scope       string
	Text        string
	ContextID   string
	Tenant      string
	RequestedBy string
	TraceParent string
	// Objective is a short, non-sensitive PURPOSE label for the delegation (e.g.
	// "nightly-report-build") — the "with what objective" of the communication graph. It
	// is minimal data: a goal reference, NEVER the task text/prompt. It is carried into
	// the audit decision + observability edge, never into the A2A payload.
	Objective string
	// ParamsHash is the caller-computed, minimal-data digest of the complete
	// governed operation being delegated. When set, it replaces the connector's
	// legacy shape-only digest in PlanHash so a control plane can bind WorkItem,
	// owner epoch, lease fence, brief and criteria revisions without handing any
	// of those values (or their content) to the connector. Empty preserves the
	// existing standalone behavior.
	ParamsHash string
	// Chain is the inbound multi-agent delegation lineage this delegation extends (the
	// agents already in the task's lineage). When nil, the lineage is taken from the
	// context (withChain) — and an absent lineage is a fresh root (depth 0). The PEP
	// enforces the ChainPolicy (max depth + no cycles) against it.
	Chain *DelegationChain
}

func DelegationPlanHash(spec DelegateSpec) string {
	paramsHash := strings.TrimSpace(spec.ParamsHash)
	if paramsHash == "" {
		paramsHash = hashParams(spec.Skill, spec.ContextID, len(spec.Text))
	}
	return PlanHash(spec.AgentName, spec.Skill, spec.Scope, paramsHash)
}

type DelegationTestResult struct {
	PlanHash  string
	AgentName string
	Skill     string
	Scope     string
	Trust     string
}

type DenyError struct {
	Reason   string
	PlanHash string
}

func (e *DenyError) Error() string {
	if e.PlanHash == "" {
		return "a2a: delegation denied: " + e.Reason
	}
	return "a2a: delegation denied (" + e.Reason + ") plan=" + e.PlanHash
}

type TaskRef struct {
	AgentName     string
	AgentURL      string
	TaskID        string
	HistoryLength int
}

type ListSpec struct {
	AgentName string
	AgentURL  string
	PageToken string
	PageSize  int
}

type TaskPage struct {
	Tasks         []TaskResult
	NextPageToken string
	TotalSize     int
}

type ExtendedCard struct {
	Card  AgentCard
	Trust string // verifyCard outcome for the extended card itself (verified/unsigned/...)
}

type rpcEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type traceParentKey struct{}

const maxStreamBody = 4 << 20 // 4 MiB per event

const maxResubscribes = 5

type StreamEvent struct {
	TaskID    string
	ContextID string
	State     TaskState
	Interrupt bool
	Terminal  bool
	Final     bool
	Detail    string // short, non-sensitive
	Reply     *ReplyEvent
}

type streamHTTPError struct {
	status    int
	transient bool
	cause     error
}

const maxInboundBody = 4 << 20

type InboundPart struct {
	Kind      string
	Text      string
	Data      json.RawMessage
	Reference string
	Digest    string
}

type InboundMessage struct {
	PeerAuthority   string
	PeerSubject     string
	Protocol        string
	InterfaceTenant string
	MessageID       string
	ContextID       string
	Role            string
	Parts           []InboundPart
	Metadata        map[string]json.RawMessage
	// ReplayID and ReplayExpiresAt are verified bearer claims for the durable
	// composition replay guard. They are excluded from content projections.
	ReplayID        string    `json:"-"`
	ReplayExpiresAt time.Time `json:"-"`
}

type InboundResult struct {
	ResultKind string
	TaskID     string
	MessageID  string
	ContextID  string
	State      TaskState
	Role       string
}

type InboundRouter interface {
	RouteInboundA2A(context.Context, InboundMessage) (InboundResult, error)
}

type InboundTaskRequest struct {
	PeerAuthority   string
	PeerSubject     string
	InterfaceTenant string
	TaskID          string
	HistoryLength   int
	ReplayID        string
	ReplayExpiresAt time.Time
}

type InboundTaskRouter interface {
	GetInboundA2ATask(context.Context, InboundTaskRequest) (InboundResult, error)
	CancelInboundA2ATask(context.Context, InboundTaskRequest) (InboundResult, error)
}

type InboundRouteError struct {
	Code    int
	Message string
}

func (e *InboundRouteError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type InboundServerConfig struct {
	Audience        string
	IssuerJWKS      []byte
	JWKSURL         string
	AllowedIssuers  []string
	InterfaceTenant string
	Router          InboundRouter
	ReplayTTL       time.Duration
	Clock           func() time.Time
	Doer            httpGetter
	// DurableReplay delegates jti authority to Router. The connector still
	// verifies the token, but does not pre-burn it in process memory.
	DurableReplay bool

	RequireClientAttestation bool
	AttesterJWKS             []byte
}

type InboundServer struct {
	auth            *PushReceiver
	interfaceTenant string
	router          InboundRouter
	tasks           InboundTaskRouter
	durableReplay   bool
}

type inboundPeerClaims struct {
	Issuer          string
	Subject         string
	ReplayID        string
	ReplayExpiresAt time.Time
}

type inboundRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type inboundSendParams struct {
	Tenant  string `json:"tenant"`
	Message struct {
		Role      string                     `json:"role"`
		Parts     []json.RawMessage          `json:"parts"`
		MessageID string                     `json:"messageId"`
		ContextID string                     `json:"contextId"`
		Metadata  map[string]json.RawMessage `json:"metadata"`
	} `json:"message"`
}

type inboundTaskParams struct {
	Tenant        string `json:"tenant"`
	ID            string `json:"id"`
	HistoryLength int    `json:"historyLength"`
}

const maxPushBody = 1 << 20 // 1 MiB

const defaultReplayTTL = 10 * time.Minute

type TaskUpdate struct {
	TaskID    string
	ContextID string
	State     TaskState
	Interrupt bool
	Terminal  bool
	Sender    string // the verified token issuer (iss), for attribution
	// ReplayID and ReplayExpiresAt are verified JWT claims passed only to the
	// durable composition callback. They must never be logged or persisted raw.
	ReplayID        string    `json:"-"`
	ReplayExpiresAt time.Time `json:"-"`
}

var ErrReplay = errors.New("a2a: provider replay")

type PushReceiverConfig struct {
	Audience       string
	IssuerJWKS     []byte
	JWKSURL        string
	AllowedIssuers []string
	OnUpdate       func(context.Context, TaskUpdate)
	OnReply        func(context.Context, ReplyEvent)
	// OnUpdateDurable is the K5 settlement seam. When configured, a recognized
	// lifecycle update is acknowledged to the peer only after this callback has
	// durably recorded it. An error returns 503 so the sender can retry with a new
	// notification token; OnUpdate remains an optional post-settlement observer.
	OnUpdateDurable func(context.Context, TaskUpdate) error
	// OnReplyDurable is the equivalent commit-before-ack seam for Message and
	// artifactUpdate values. The callback receives only the bounded projection.
	OnReplyDurable func(context.Context, ReplyEvent) error
	ReplayTTL      time.Duration
	Clock          func() time.Time
	Doer           httpGetter // injected in tests; nil => SSRF-guarded client

	// RequireClientAttestation turns on the OPTIONAL runtime-admission gate:
	// every inbound push must additionally carry a valid OAuth-Client-Attestation /
	// OAuth-Client-Attestation-PoP pair (draft-ietf-oauth-attestation-based-client-
	// auth-09 — a DRAFT, hence policy opt-in, default off) verified against
	// AttesterJWKS, the operator's trust anchor of Client Attester keys. Deny-closed
	// when enabled: enabling it without an attester anchor fails construction, and
	// any attestation failure is a 401 before push-token verification. Disabling it
	// never weakens the always-on push JWT verification.
	RequireClientAttestation bool
	AttesterJWKS             []byte
}

type httpGetter interface {
	Do(*http.Request) (*http.Response, error)
}

type PushReceiver struct {
	audience        string
	allowedIssuers  map[string]struct{}
	anchor          *jose.JSONWebKeySet
	jwksURL         string
	onUpdate        func(context.Context, TaskUpdate)
	onUpdateDurable func(context.Context, TaskUpdate) error
	onReply         func(context.Context, ReplyEvent)
	onReplyDurable  func(context.Context, ReplyEvent) error
	doer            httpGetter
	now             func() time.Time
	replay          *replayCache
	attest          *attestVerifier // non-nil only when RequireClientAttestation
}

func lookupAnchorKey(set *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if set == nil {
		return nil
	}
	if kid != "" {
		if ks := set.Key(kid); len(ks) > 0 {
			return &ks[0]
		}
	}
	if len(set.Keys) == 1 {
		return &set.Keys[0]
	}
	return nil
}
func timeOfDate(d *jwt.NumericDate) time.Time {
	if d == nil {
		return time.Time{}
	}
	return d.Time()
}

type replayCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	seen map[string]time.Time
}

func newReplayCache(ttl time.Duration, now func() time.Time) *replayCache {
	return &replayCache{ttl: ttl, now: now, seen: map[string]time.Time{}}
}
func (c *replayCache) admit(jti string, exp time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Lazy eviction of expired entries (keeps the map bounded).
	for k, until := range c.seen {
		if now.After(until) {
			delete(c.seen, k)
		}
	}
	if until, ok := c.seen[jti]; ok && !now.After(until) {
		return false // replay within retention
	}
	until := now.Add(c.ttl)
	if !exp.IsZero() && exp.After(now) && exp.Before(until) {
		until = exp
	}
	c.seen[jti] = until
	return true
}
func pushSSRFClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("a2a: push jwks: cannot parse dial address %q", address)
			}
			if pushReservedIP(ip) {
				return fmt.Errorf("a2a: push jwks: refusing to dial reserved address %s", ip)
			}
			return nil
		},
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
}
func pushReservedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
func requireHTTPS(rawURL string) error {
	u := strings.TrimSpace(rawURL)
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return nil
	case strings.HasPrefix(lower, "http://"):
		parsed, err := url.Parse(u)
		if err == nil && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
			return nil
		}
	}
	return fmt.Errorf("a2a: key-set url %q must be https (RFC 7515 §4.1.2)", rawURL)
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

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import "time"

// pdp.go — the PEP-neutral inference decision contract (D6).
//
// This is the wire contract every Policy Enforcement Point (PEP) speaks to the
// Olivares inference Policy Decision Point (PDP): the in-process claude-api proxy, the
// LiteLLM reference callback, and a future deny-capable Envoy ext_authz server all
// target THIS shape. It is Apache-2.0 and zero-dependency (like the rest of this SDK)
// so any PEP can build against it without touching the AGPL decision engine that
// SATISFIES it (running the deny-closed gate chain: identity → kill-switch → residency
// → model-access → context-policy → DLP → egress → firewall → computer-use → ceilings
// → budget). This package fixes only the shapes, their fail-closed defaults, and the
// four-phase transactional protocol.
//
// # The transactional decision lifecycle (D6)
//
//	Decide      PEP → DecisionRequest      → PDP → DecisionVerdict   (reserve budget, mint DecisionID)
//	Activate    PEP → DecisionActivation   → PDP → ActivationReceipt (ack rewrites BEFORE forward; reserve→active)
//	 (forward)  PEP forwards the effective request upstream
//	Postflight  PEP → PostflightRequest    → PDP → PostflightVerdict (optional: preventive response gate when buffered)
//	Settle      PEP → DecisionOutcome      → PDP → OutcomeReceipt    (commit/release the reservation; idempotent)
//
// Crash-safety of the reservation turns on the phase reached:
//   - reserved but never activated (PEP died pre-forward)      → auto-RELEASE at ActivationDeadline.
//   - activated but no outcome (PEP forwarded then died)       → auto-settle by ESTIMATE at ReservationExpiresAt
//     (never silently released — spend may have occurred), reconciled if a late outcome arrives.
//
// # Non-negotiable invariants
//
//  1. Zero value is DENY. An unset DecisionVerdict.Decision ("") is not allowed; IsAllowed
//     reports false. A forgotten path fails closed.
//  2. The PDP resolves the real principal from its store. SubjectHint and DelegationProof
//     are what the PEP PRESENTS; the PDP verifies the proof and returns ResolvedSubject/
//     ResolvedTenant. A PEP's transport credential authenticates the PEP SERVICE, not the
//     governed subject — subject authority rides DelegationProof, never a bare hint.
//  3. The PDP classifies content itself over the exact received octets. There is no
//     PEP-asserted content class the PDP could trust; any assessment travels OUT in
//     ContentAssessment. InputDigest/EffectiveRequestDigest bind the verdict to bytes.
//  4. Nonce + IssuedAt give anti-replay (unique per PEP identity, registered atomically);
//     the verdict, activation and outcome all carry the server-minted DecisionID so a PEP
//     cannot rebind a prior verdict to a different request. A retry with the same nonce and
//     the same digest returns the same verdict; the same nonce with a different digest is a
//     replay and denies.
//  5. Capabilities are declared AND registered. The PDP intersects the PEP's declared
//     PEPCapabilities with what is registered for its service identity and never returns an
//     obligation the PEP cannot enforce (it denies with FailureCapabilityUnmet instead).
//  6. Obligations are typed and acknowledged per-obligation. A DecisionModify verdict is
//     valid only with an EffectiveRequestDigest and/or a typed rewrite obligation; every
//     Required pre-forward obligation must be AckApplied in DecisionActivation BEFORE the
//     PEP forwards.
//  7. Evidence is mandatory on allow|modify. EvidenceRef is the tamper-evident ledger
//     anchor; an allow that cannot be anchored degrades to deny (FailureEvidenceFault).
//  8. Budget is reserved at decision time and committed/released by the outcome, so
//     concurrent PEPs cannot jointly overspend. (Group-dimension budgets remain check-only
//     until-b closes that TOCTOU; a group-cap decision carries FailureReservationFault
//     semantics only for the reservable non-group budgets in v1.)

// ProtocolVersion is the wire-contract identifier. A PDP rejects a DecisionRequest whose
// ProtocolVersion it does not implement (FailureProtocolError).
const ProtocolVersion = "olivares.pdp.inference.v1"

// Decision is the PDP verdict kind. The zero value ("") is a DENY (IsAllowed == false).
type Decision string

const (
	DecisionDeny   Decision = "deny"   // explicit refusal; the PEP must not forward
	DecisionAllow  Decision = "allow"  // permit as-is (pending activation + settlement)
	DecisionModify Decision = "modify" // permit ONLY if every Required obligation is applied+acked
)

// FailureClass explains a deny (or an activation/settlement refusal) beyond the HTTP hint.
// A definitive policy refusal is FailurePolicyDeny — NOT a "fault"; the fault classes name
// the specific plane that could not complete.
type FailureClass string

const (
	FailureNone                FailureClass = ""
	FailurePolicyDeny          FailureClass = "policy_deny"          // firm policy refusal
	FailurePlaneUnavailable    FailureClass = "plane_unavailable"    // decision plane unreachable (503)
	FailurePolicyReadFault     FailureClass = "policy_read_fault"    // governance READ fault (fail_open territory)
	FailureDelegationInvalid   FailureClass = "delegation_invalid"   // subject-delegation proof failed verification
	FailureReplay              FailureClass = "replay"               // nonce reuse / stale IssuedAt
	FailureClassificationFault FailureClass = "classification_fault" // content classifier unavailable
	FailureReservationFault    FailureClass = "reservation_fault"    // budget reservation could not be taken
	FailureEvidenceFault       FailureClass = "evidence_fault"       // ledger anchor unavailable (allow→deny)
	FailureCapabilityUnmet     FailureClass = "capability_unmet"     // PEP cannot enforce a required control
	FailureProtocolError       FailureClass = "protocol_error"       // malformed/version-mismatched message
)

// Outcome is the terminal disposition a PEP reports for a decision.
type Outcome string

const (
	OutcomeForwarded Outcome = "forwarded" // forwarded and completed; Usage commits the reservation
	OutcomeBlocked   Outcome = "blocked"   // withheld (obligation unapplicable, postflight deny, ceiling)
	OutcomeError     Outcome = "error"     // failed mid-flight; may still have incurred spend
)

// AckStatus is a per-obligation application result.
type AckStatus string

const (
	AckApplied       AckStatus = "applied"
	AckFailed        AckStatus = "failed"
	AckNotApplicable AckStatus = "not_applicable"
)

// ObligationPhase is when an obligation must be applied.
type ObligationPhase string

const (
	PhasePreForward ObligationPhase = "pre_forward" // applied before forwarding (rewrites, redactions)
	PhaseResponse   ObligationPhase = "response"    // applied to the response (postflight redactions)
)

// SubjectHint is what the PEP claims the subject is. It is a HINT: the PDP resolves the
// authoritative principal from its store after verifying the DelegationProof, and returns
// it as DecisionVerdict.ResolvedSubject. It is never the effective principal on its own.
type SubjectHint struct {
	Type string `json:"type"` // e.g. "user", "token", "virtual_key", "team", "service"
	ID   string `json:"id"`
}

// DelegationProof is the verifiable authority for the PEP to act for a subject. The PEP's
// transport credential authenticates the PEP service; this proves it may act as SubjectHint.
// The PDP verifies the proof binds, at minimum: subject + tenant, audience (this PDP) +
// authorized PEP service, operation/scope, Nonce + request digest, IssuedAt + expiry + jti.
// Core token exchange models opaque delegated credentials with server-side act-as and
// audience fields; the PDP's handle verifier additionally binds the resolved subject and
// tenant to the authenticated PEP service, PDP audience, operation scope, request
// fingerprint, nonce, IssuedAt/expiry and a single-use jti — the act-as/audience columns
// alone do not carry those bindings. Protocol v1 accepts only "handle".
type DelegationProof struct {
	// Scheme selects verification: "handle" (an opaque, unforgeable, single-use reference
	// to a stored delegation) is the only scheme protocol v1 accepts. "actas-token" (a
	// signed act-as assertion) is RESERVED and rejected in v1 with FailureProtocolError —
	// first-party authority stays opaque (no signing surface). Any other value is a
	// protocol error.
	Scheme string `json:"scheme"`
	// Token is the opaque verifiable material for Scheme. It is never introspected by the
	// PEP; only the PDP verifies it.
	Token []byte `json:"token,omitempty"`
}

// Delegation-proof schemes. Only DelegationSchemeHandle verifies in protocol v1;
// DelegationSchemeActAsToken is reserved wire vocabulary and MUST be rejected with
// FailureProtocolError until a version implements it.
const (
	DelegationSchemeHandle     = "handle"
	DelegationSchemeActAsToken = "actas-token"
)

// ContentEnvelope carries the request (or response) payload the PDP inspects. Inline is the
// simple path for normal payloads; Ref is a content-addressed, ephemeral, PDP-authenticated
// reference for large payloads (requests reach 16 MiB, batches 256 MiB), which the PDP
// fetches and re-verifies against Size + SHA256 before trusting.
type ContentEnvelope struct {
	Inline    []byte `json:"inline,omitempty"`
	Ref       string `json:"ref,omitempty"`
	SHA256    string `json:"sha256"` // lowercase-hex digest of the exact octets
	Size      int64  `json:"size"`
	MediaType string `json:"media_type,omitempty"`
}

// PEPCapabilities is what a PEP can enforce. Declared here, intersected by the PDP with the
// capabilities registered for the PEP's service identity; the PDP denies rather than return
// a control the PEP cannot apply.
type PEPCapabilities struct {
	BufferRequest  bool `json:"buffer_request,omitempty"`  // can hold the request until the verdict is applied
	BufferResponse bool `json:"buffer_response,omitempty"` // can buffer the full response before releasing (preventive egress)
	Streaming      bool `json:"streaming,omitempty"`       // handles streamed responses
	Batch          bool `json:"batch,omitempty"`           // submits batch operations
}

// OperationRef is the requested operation. The PDP compares Model against the model it
// extracts from the payload; a mismatch denies (request-decision-for-A / send-B).
type OperationRef struct {
	Kind   string `json:"kind"`             // e.g. "messages", "messages_batch"
	Model  string `json:"model"`            // the requested model id
	Stream bool   `json:"stream,omitempty"` // whether the PEP will stream the response
}

// DecisionRequest is phase 1: what a PEP sends the PDP for a pre-forward decision.
type DecisionRequest struct {
	ProtocolVersion string          `json:"protocol_version"`
	SubjectHint     SubjectHint     `json:"subject_hint"`
	Delegation      DelegationProof `json:"delegation"`
	Tenant          string          `json:"tenant,omitempty"`
	Operation       OperationRef    `json:"operation"`
	Content         ContentEnvelope `json:"content"`
	Nonce           string          `json:"nonce"`
	IssuedAt        time.Time       `json:"issued_at"`
	Capabilities    PEPCapabilities `json:"capabilities"`
}

// Obligation is a typed control a verdict requires the PEP to apply. Params carries
// kind-specific typed parameters; Ref is an opaque handle (never inlines sensitive
// content). For a pre-forward rewrite, ResultDigest is the digest the effective request
// must have after applying it — the PDP checks it at activation.
type Obligation struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"` // e.g. "redact_request", "restrict_tools", "max_output_tokens", "buffer_response"
	Required     bool              `json:"required"`
	Phase        ObligationPhase   `json:"phase"`
	Params       map[string]string `json:"params,omitempty"`
	Ref          string            `json:"ref,omitempty"`
	ResultDigest string            `json:"result_digest,omitempty"`
}

// ObligationAck is a per-obligation application result the PEP reports.
type ObligationAck struct {
	ObligationID string    `json:"obligation_id"`
	Status       AckStatus `json:"status"`
	ResultDigest string    `json:"result_digest,omitempty"`
}

// ContentAssessment is the PDP's own classification of content (request or response),
// emitted so it can be audited/reused — never asserted by the PEP.
type ContentAssessment struct {
	Direction         string   `json:"direction"` // "request" | "response"
	ContentDigest     string   `json:"content_digest"`
	Classes           []string `json:"classes,omitempty"`
	ClassifierVersion string   `json:"classifier_version,omitempty"`
	EvidenceRef       string   `json:"evidence_ref,omitempty"`
}

// PresentationHints are OPTIONAL transport hints (HTTP status / Anthropic error shape /
// headers). They are not neutral PDP semantics — ReasonCode is the interoperable base and
// a PEP that speaks a different transport ignores these.
type PresentationHints struct {
	Status    int               `json:"status,omitempty"`
	ErrorType string            `json:"error_type,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// DecisionVerdict is the PDP's phase-1 answer. Its zero value is a DENY.
type DecisionVerdict struct {
	ProtocolVersion string   `json:"protocol_version"`
	Decision        Decision `json:"decision"` // "" (zero) is deny
	// DecisionID is server-minted; it binds this decision across activation, postflight and
	// settlement. The PDP stores the normative association (PEP identity + DecisionID +
	// Nonce + resolved subject/tenant + operation + input/effective digest + policy version
	// + reservation + evidence) and rejects any later phase that does not match it.
	DecisionID   string       `json:"decision_id"`
	Nonce        string       `json:"nonce"` // echoes DecisionRequest.Nonce
	FailureClass FailureClass `json:"failure_class,omitempty"`
	ReasonCode   string       `json:"reason_code,omitempty"` // stable, per-gate, interoperable
	Reason       string       `json:"reason,omitempty"`      // short, non-sensitive

	Presentation PresentationHints `json:"presentation,omitempty"`

	// ResolvedSubject/ResolvedTenant are the PDP's authoritative principal (from its store,
	// after verifying the delegation) — never the caller's assertion.
	ResolvedSubject SubjectHint `json:"resolved_subject,omitempty"`
	ResolvedTenant  string      `json:"resolved_tenant,omitempty"`

	// InputDigest is the digest the PDP computed over the received content.
	// EffectiveRequestDigest is the digest of the (possibly-rewritten) request to forward;
	// it equals InputDigest when Decision != modify.
	InputDigest            string `json:"input_digest,omitempty"`
	EffectiveRequestDigest string `json:"effective_request_digest,omitempty"`

	Obligations          []Obligation    `json:"obligations,omitempty"`
	RequiredCapabilities PEPCapabilities `json:"required_capabilities,omitempty"`

	PolicyVersion string `json:"policy_version,omitempty"`
	// ValidUntil is the deadline to ACTIVATE (begin the forward) — not a reservation TTL.
	ValidUntil time.Time `json:"valid_until,omitempty"`

	// ReservationID ties an allow to a budget reservation. ActivationDeadline is when an
	// un-activated reservation auto-releases.
	ReservationID      string    `json:"reservation_id,omitempty"`
	ActivationDeadline time.Time `json:"activation_deadline,omitempty"`

	// EvidenceRef is the ledger anchor; mandatory on allow|modify (else the verdict is a
	// deny with FailureEvidenceFault).
	EvidenceRef string `json:"evidence_ref,omitempty"`

	// Assessment is the PDP's request-side classification, if any.
	Assessment *ContentAssessment `json:"assessment,omitempty"`
}

// IsAllowed reports whether the verdict permits forwarding. The zero value and any unknown
// Decision are DENY (fail-closed).
func (v DecisionVerdict) IsAllowed() bool {
	return v.Decision == DecisionAllow || v.Decision == DecisionModify
}

// DecisionActivation is phase 2: the PEP confirms, BEFORE forwarding, that it applied every
// Required pre-forward obligation and what the effective bytes are. The PDP flips the
// reservation reserved→active only if EffectiveDigest matches the verdict and every Required
// obligation is AckApplied.
type DecisionActivation struct {
	DecisionID string `json:"decision_id"`
	Nonce      string `json:"nonce"`
	// EffectiveDigest is the digest of the exact bytes the PEP will forward. The PDP rejects
	// activation if it does not equal DecisionVerdict.EffectiveRequestDigest.
	EffectiveDigest string          `json:"effective_digest"`
	Obligations     []ObligationAck `json:"obligations,omitempty"`
}

// ActivationReceipt is the PDP's phase-2 answer.
type ActivationReceipt struct {
	DecisionID   string       `json:"decision_id"`
	Activated    bool         `json:"activated"`
	FailureClass FailureClass `json:"failure_class,omitempty"`
	Reason       string       `json:"reason,omitempty"`
	// ReservationExpiresAt is when the now-active reservation auto-settles BY ESTIMATE if no
	// DecisionOutcome arrives (a stream that may outlive this must renew the reservation).
	ReservationExpiresAt time.Time `json:"reservation_expires_at,omitempty"`
}

// PostflightRequest is phase 3 (optional): a PEP that buffers the response submits it for a
// preventive response-side decision. A PEP that cannot buffer skips this and the response
// gate is detective-only, as with an unbuffered stream.
type PostflightRequest struct {
	DecisionID     string          `json:"decision_id"`
	Nonce          string          `json:"nonce"`
	Response       ContentEnvelope `json:"response"`
	UpstreamStatus int             `json:"upstream_status,omitempty"`
}

// PostflightVerdict is the PDP's phase-3 answer: allow releases the buffered response, deny
// withholds it. Modify carries response-phase Obligations (e.g. redactions) to apply before
// release.
type PostflightVerdict struct {
	DecisionID   string             `json:"decision_id"`
	Decision     Decision           `json:"decision"` // allow (release) | modify (redact) | deny (withhold)
	FailureClass FailureClass       `json:"failure_class,omitempty"`
	ReasonCode   string             `json:"reason_code,omitempty"`
	Reason       string             `json:"reason,omitempty"`
	Obligations  []Obligation       `json:"obligations,omitempty"`
	Assessment   *ContentAssessment `json:"assessment,omitempty"`
	EvidenceRef  string             `json:"evidence_ref,omitempty"`
}

// Usage is the settled consumption. Token counts alone do not settle real cost (cache
// read/write tiers, service tier, geo differ); CostMicroUSD is the settled cost if computed,
// else the PDP derives it from this breakdown and the provider price for the resolved
// model/tier/geo.
type Usage struct {
	InputTokens      int64  `json:"input_tokens,omitempty"`
	OutputTokens     int64  `json:"output_tokens,omitempty"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	CostMicroUSD     int64  `json:"cost_micro_usd,omitempty"`
	ServiceTier      string `json:"service_tier,omitempty"`
}

// DecisionOutcome is phase 4: the PEP closes the transaction. ForwardedDigest must match the
// verdict's EffectiveRequestDigest (the PEP forwarded exactly the governed bytes). Usage
// settles the reservation. Settlement is idempotent (see OutcomeReceipt).
type DecisionOutcome struct {
	DecisionID      string          `json:"decision_id"`
	Nonce           string          `json:"nonce"`
	ReservationID   string          `json:"reservation_id,omitempty"`
	Outcome         Outcome         `json:"outcome"`
	Obligations     []ObligationAck `json:"obligations,omitempty"`
	ForwardedDigest string          `json:"forwarded_digest,omitempty"`
	Usage           Usage           `json:"usage"`
}

// OutcomeReceipt is the PDP's phase-4 answer. Settlement is idempotent: a repeated identical
// outcome returns the same receipt; a contradictory one (different digest/usage for a
// settled DecisionID) is rejected and audited.
type OutcomeReceipt struct {
	DecisionID string `json:"decision_id"`
	// Settled is the terminal reservation disposition: "committed" (real cost), "released"
	// (no charge), or "committed_estimate" (activated but outcome lost → settled by estimate,
	// reconciled if a late outcome arrives).
	Settled      string       `json:"settled"`
	EvidenceRef  string       `json:"evidence_ref,omitempty"`
	FailureClass FailureClass `json:"failure_class,omitempty"`
	Reason       string       `json:"reason,omitempty"`
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// gate.go declares the governance + actuation SEAMS the inline MCP PEP depends on but
// does not own, each with a deny-closed default so an un-wired Resource Server can
// SERVE metadata and validate tokens but can NEVER actuate a tool. The composition
// root (cmd, AGPL) wires the real adapters; a connector is Apache-2.0 and may not
// import /core.
//
// Two seams matter for the tools/call gate (AIP-02 §c):
//
//   - ApprovalGate — the human-in-the-loop seam. A non-readOnly/destructive tool
//     (classified by the SERVER-owned toolset, never the tool's UNTRUSTED annotation)
//     requires an approval bound to a PlanHash before it runs.
//   - ToolDispatcher — the upstream executor. The PEP NEVER passes the inbound token
//     through (token-passthrough is forbidden by design): the ToolCall handed to the
//     dispatcher carries the tool name + arguments + the VALIDATED subject/scopes —
//     never the raw bearer — so the inbound token object is unreachable from any
//     upstream request the dispatcher builds. When a dispatcher calls an upstream API
//     it is a SEPARATE OAuth client (oauth.go) minting its OWN audience-bound token.

// GateStatus is the effective decision the tool ApprovalGate reports; every value
// except StatusApproved is a DENY (deny-closed). Defined here because a connector
// cannot import the AGPL governance module's vocabulary.
type GateStatus string

const (
	StatusApproved GateStatus = "approved"
	StatusPending  GateStatus = "pending"
	StatusRejected GateStatus = "rejected"
	StatusExpired  GateStatus = "expired"
	StatusNoGate   GateStatus = "no_gate"
)

// ToolApprovalRequest is the minimal-data description of a destructive tool call that
// needs human approval. PlanHash binds the approval to the exact (tool, args-shape,
// subject) tuple (anti-TOCTOU). It carries NO arguments in the clear and NO token.
type ToolApprovalRequest struct {
	Tenant      string
	Subject     string
	Tool        string
	Scope       string
	PlanHash    string
	RequestedBy string
}

// GateDecision is the gate's answer. Allowed() is the only authorization; every other
// status (and the zero value) is a deny.
type GateDecision struct {
	ApprovalRef string
	Status      GateStatus
	PlanHash    string
}

// Allowed reports whether this decision authorizes the tool call (approved + bound to
// the matching plan, re-checked by the caller).
func (d GateDecision) Allowed() bool { return d.Status == StatusApproved }

// ApprovalGate is the HITL seam for a destructive tool call. The real adapter bridges
// to (POST /v1/m/governance/approvals bound to the PlanHash).
type ApprovalGate interface {
	Authorize(ctx context.Context, req ToolApprovalRequest) (GateDecision, error)
}

// TaskIntent is the minimal-data description of a just-created durable MCP task.
type TaskIntent struct {
	Tenant  string
	Subject string
	Tool    string
	TaskID  string
	TTLMs   *int64
}

// TaskGateDecision is the admission decision for a durable task handle. A DeniedStatus
// of 0 maps to HTTP 403; FinOps-style budget adapters use 402/429.
type TaskGateDecision struct {
	Allow        bool
	Reason       string
	DeniedStatus int
}

// TaskGate is the optional durable-task governance seam. Nil means allow; an error is
// interpreted by the ResourceServer as deny-closed.
type TaskGate interface {
	AuthorizeTask(ctx context.Context, intent TaskIntent) (TaskGateDecision, error)
}

// denyApprovalGate is the deny-closed default: with no gate wired, every destructive
// tool is denied (a non-destructive tool with sufficient scope does not consult it).
type denyApprovalGate struct{}

func (denyApprovalGate) Authorize(_ context.Context, req ToolApprovalRequest) (GateDecision, error) {
	return GateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: StatusNoGate, PlanHash: req.PlanHash}, nil
}

// UpstreamRequest is what the PEP hands the upstream AFTER admission (a valid,
// audience-bound token) and, for tools/call, AFTER the gate. It is the
// NO-TOKEN-PASSTHROUGH boundary: it carries the JSON-RPC method, the params bytes, and
// the VALIDATED subject + granted scopes — but DELIBERATELY NO raw bearer token and no
// reference to the inbound validatedToken, so the inbound credential is structurally
// unreachable from any upstream request the adapter constructs. The arguments/params
// are relayed opaquely (the PEP does not inspect tool inputs — that is the upstream's
// schema validation, surfaced back as isError:true per SEP-1303). For an
// evidence-enforced method (tools/call) Params are the CANONICAL governed bytes
// (the bytes the EffectDigest binds are the bytes sent/F3) and the operation
// identity + fence token ride along for upstreams capable of receiver-side
// idempotency/fencing — identifiers only, never credentials.
type UpstreamRequest struct {
	Method      string
	Params      []byte   // the JSON-RPC params (canonical governed bytes on enforced methods)
	Subject     string   // validated token subject (for the upstream adapter's attribution)
	Scopes      []string // validated granted scopes
	TraceParent string   // W3C trace context to propagate upstream (never a credential)
	// OperationID / EffectDigest / FenceToken are the evidence identity of an
	// enforced dispatch: the single-use operation, its full-binding digest, and the
	// opaque leadership fencing token the claim was anchored under. An upstream that
	// supports receiver-side fencing rejects a stale token; all three are empty on
	// the not-yet-enforced surfaces (stages 4-6).
	OperationID  sdk.OperationID
	EffectDigest sdk.EffectDigest
	FenceToken   string
}

// DispatchState classifies what is DURABLY KNOWN about one upstream dispatch. It is
// the settlement vocabulary of the evidence operation journal. The words
// are MUTUALLY EXCLUSIVE, and the exclusion is predicated on the DISPATCH FACT
// alone (stage-7 B-bis, round 2): a PARENT dispatch operation records whether
// and how the transport round trip ended — never what governance later decided
// about the observed response. That disposition lives in the RESPONSE-RELEASE
// CHILD operation (deriveResponseReleaseBinding), which the parent settlement
// names. The split is crash-safety, not taste: an observed dispatch must become
// durable THE MOMENT it is observed — delaying its settlement until after a
// policy verdict opens the crash→claimed→retry→re-execution window that
// evidence-or-refuse exists to close.
//
//	completed — an upstream response was OBSERVED (including an upstream JSON-RPC
//	            error: the transport round-trip finished). It states the dispatch
//	            fact ONLY; whether governance then released or withheld the
//	            observed response is the release child's record, never this word's;
//	not_sent  — proven failure BEFORE the transport write (nothing can have reached
//	            the upstream);
//	unknown   — the request may have been transmitted (timeout/reset/cancel/decode
//	            failure after handing it to the transport). NEVER re-dispatched;
//	blocked   — a policy/authorization decision stopped the effect after the claim
//	            and BEFORE any dispatch: nothing reached the upstream. For an
//	            operation whose effect is local (a task registration, a
//	            reconciliation mutation) the same truth reads "the mutation never
//	            applied";
//	withheld  — a RELEASE-CHILD terminal state, never a parent's: the parent's
//	            dispatch was observed (it settles completed) and a policy/
//	            authorization decision then withheld the response from the
//	            caller — the round trip finished, the payload never left
//	            governance. Writers: the render-inspector deny and render
//	            ambiguity (ui://), the elicitation response deny and ambiguity,
//	            and every MRTR post-observation refusal (noAuthority, unreadable,
//	            mediator hijack) on tools/call, tasks/get, tasks/cancel and
//	            ui:// (settleWithheldRelease). Sampling has no response-side
//	            content channel and therefore no withheld writer.
//
// The reading rule custodial readers depend on: blocked proves nothing reached
// the upstream; completed proves something did — whatever the disposition of
// what came back. tasks/cancel custody infers automatic retryability from
// exactly that line (settledStatePermitsCancelRetry): a blocked or not_sent
// cancel may be re-attempted under a new operation; a completed one may not
// (the dispatch occurred — a withheld or refused RESPONSE does not un-produce
// the upstream effect), and a withheld reading (a child, or a future journal
// row) never licenses a retry either.
//
// An Upstream adapter never returns `withheld`: the classification contract
// below admits only completed / not_sent / unknown / blocked from a Forward.
type DispatchState string

const (
	DispatchCompleted DispatchState = "completed"
	DispatchNotSent   DispatchState = "not_sent"
	DispatchUnknown   DispatchState = "unknown"
	DispatchBlocked   DispatchState = "blocked"
	DispatchWithheld  DispatchState = "withheld"
)

// UpstreamResult is the classified outcome of one Forward. State is meaningful on
// BOTH legs: with a nil error it is normally DispatchCompleted; with an error it
// tells the settlement what is durably known (not_sent / unknown / blocked /
// completed-with-upstream-error). DispatchRef is an opaque reference to the dispatch
// (an upstream message id), never a payload.
type UpstreamResult struct {
	Result      json.RawMessage
	State       DispatchState
	DispatchRef string
}

// Upstream forwards an ADMITTED MCP method to the real tool backend. The PEP forwards
// reads (initialize, tools/list) it has admitted (valid token) and tools/call it has
// GATED; it never forwards anything unauthenticated and never forwards a denied call.
// The real adapter, when the upstream is OAuth-protected, is a SEPARATE OAuth client
// (oauth.go) that mints its OWN audience-bound token — it NEVER relays the inbound
// token (token-passthrough is forbidden by design). The result is the JSON-RPC
// `result` relayed verbatim (a tools/call result already carries isError:true for a
// Tool Execution Error, SEP-1303 — the PEP relays it, never reclassifying it as a
// protocol error). The default fails CLOSED (an explicit error, never a faked
// success), so an un-wired RS cannot actuate.
//
// Classification contract (round-2): an implementation MUST label every leg
// with the honest DispatchState — errors before invoking the transport are
// not_sent; only a STRICTLY VALID, CORRELATED JSON-RPC response (a 2xx body that
// passes exact-cased, duplicate-rejecting validation against the sent id — see
// ParseStrictJSONRPCResponse — carrying exactly one of result|error) is completed,
// a valid JSON-RPC ERROR object included; EVERYTHING else after invoking the
// transport — timeout, reset, cancellation, read/decode failure, over-limit body,
// non-2xx status, uncorrelated or malformed body — is unknown (nothing observed
// can confirm the outcome).
type Upstream interface {
	Forward(ctx context.Context, req UpstreamRequest) (UpstreamResult, error)
}

// denyUpstream is the deny-closed default: no upstream is wired, so every admitted/
// gated call still fails — explicitly, never a pretend success. Nothing was ever
// dispatched, so the classification is not_sent.
type denyUpstream struct{}

func (denyUpstream) Forward(context.Context, UpstreamRequest) (UpstreamResult, error) {
	return UpstreamResult{State: DispatchNotSent}, errNoUpstream
}

// errNoUpstream is the fail-closed error the unwired upstream returns.
var errNoUpstream = &dispatchError{"mcp: rs: no upstream wired; request admitted/gated but not actuated"}

type dispatchError struct{ msg string }

func (e *dispatchError) Error() string { return e.msg }

// GateRecordState is the auditor's classification of one Record call.
type GateRecordState string

const (
	// GateRecordFresh: THIS call claimed the operation and anchored its evidence —
	// the only state from which an effect may be emitted (after MayEmit + the
	// BeforeEffect fence).
	GateRecordFresh GateRecordState = "fresh"
	// GateRecordReplayPending: the exact operation is already claimed but not yet
	// settled — a crash/concurrent duplicate. NEVER re-dispatched.
	GateRecordReplayPending GateRecordState = "replay_pending"
	// GateRecordReplaySettled: the exact operation already settled; Recorded carries
	// its terminal outcome. NEVER re-dispatched.
	GateRecordReplaySettled GateRecordState = "replay_settled"
	// GateRecordRefused: no fresh claim exists — an evidence fault (Receipt.Fault) or
	// a rebind (FailureClass == sdk.FailureReplay). The effect MUST NOT be emitted.
	GateRecordRefused GateRecordState = "refused"
)

// RecordedOutcome is the durable settlement of a previously claimed operation, as
// returned on an exact replay: state + digests/refs only, never the raw result (the
// journal does not retain method results — a status replay returns metadata).
type RecordedOutcome struct {
	State        DispatchState
	ResultDigest string
	OutcomeRef   string
}

// GateRecord is the auditor's answer to Record: the claim outcome for the binding.
// A bare valid Receipt is NEVER sufficient to emit — the caller requires
// MayEmit(binding) AND a valid BeforeEffect receipt immediately before dispatch.
type GateRecord struct {
	Binding      sdk.EvidenceBinding
	Receipt      sdk.EvidenceReceipt
	State        GateRecordState
	FailureClass sdk.FailureClass
	// FenceToken is the opaque leadership fencing token the claim was anchored under
	// (propagated to the upstream request for receiver-side fencing). Opaque to the
	// connector; minted and re-verified by the composition root.
	FenceToken string
	// Recorded is the settled outcome on GateRecordReplaySettled; nil otherwise.
	Recorded *RecordedOutcome
}

// MayEmit is the connector-side green light: only a FRESH claim whose receipt is
// anchored for the exact expected binding authorizes the effect (sdk MustRefuse law).
func (r GateRecord) MayEmit(binding sdk.EvidenceBinding) bool {
	return r.State == GateRecordFresh && !r.Receipt.MustRefuse(binding)
}

// GateOutcome is the input to Settle: the record the effect ran under plus the
// classified dispatch outcome. ResultDigest is an opaque digest of the relayed
// result (never the result body); DispatchRef is the upstream's opaque dispatch
// reference. ReleaseBinding names the response-release child binding of every
// mediated/governed surface — the UI, elicitation and sampling legs, and any
// tools/call or task-method result the MRTR classification selected (a PLAIN
// non-MRTR tools/call relay is the one shape that still settles with a zero
// ReleaseBinding: no governed bytes, no release child).
type GateOutcome struct {
	Record         GateRecord
	State          DispatchState
	ResultDigest   string
	DispatchRef    string
	ReleaseBinding sdk.EvidenceBinding
}

// GateSettlement is the durable settlement result. A settlement whose FailureClass
// is not FailureNone did NOT record the outcome: the operation stays claimed
// (ambiguous-but-safe — never re-dispatched) and the caller WITHHOLDS the response.
type GateSettlement struct {
	Outcome        RecordedOutcome
	EvidenceRef    string
	ReleaseReceipt sdk.EvidenceReceipt
	FailureClass   sdk.FailureClass
}

// GateAuditor is the evidence seam of the MCP PEP: it claims, fences and
// settles the durable evidence of each governed decision (the frozen S5
// evidence-or-refuse contract, sdk/evidence.go). The connector computes the
// binding; the composition root (cmd, AGPL) implements the journal against the
// engine store — the connector never imports /core.
type GateAuditor interface {
	// Record registers one gate decision.
	//
	// Allowed=true with a VALID binding: atomically claim the OperationID and
	// anchor the decision evidence BEFORE any effect; the returned GateRecord is
	// the only path to dispatch (MayEmit).
	//
	// Allowed=false: best-effort denial evidence — the binding is zero and the
	// result is ignored by the caller: a policy DENY never depends on evidence
	// success (sdk/pdp.go: evidence is mandatory on allow|modify, not deny).
	//
	// Allowed=true with a ZERO binding is the LEGACY leg of the not-yet-enforced
	// surfaces (tasks/UI/elicitation/sampling/scoped reads — stages 4-6): the
	// implementation keeps the historical best-effort anchor and the caller
	// ignores the record.
	Record(ctx context.Context, dec ToolDecision, binding sdk.EvidenceBinding) GateRecord

	// BeforeEffect re-checks the claim's leadership fence IMMEDIATELY before the
	// external call. A receipt that MustRefuse the record's binding aborts the
	// dispatch; the claim stays claimed and is never re-dispatched.
	BeforeEffect(ctx context.Context, rec GateRecord) sdk.EvidenceReceipt

	// Settle atomically records the terminal/ambiguous outcome against the claim.
	// A refusing settlement (FailureClass != FailureNone) means the outcome did NOT
	// commit: the caller withholds the response and the operation remains
	// non-replayable.
	Settle(ctx context.Context, out GateOutcome) GateSettlement
}

// ToolDecision is the minimal-data audit record of one tools/call gate decision —
// references + the verdict only, never the arguments in the clear and never a token.
type ToolDecision struct {
	Tenant  string
	Subject string
	// IsDelegated and ActAs carry the effective on-behalf-of identity for a
	// delegated credential. They are identifiers only, never bearer material.
	IsDelegated   bool
	ActAs         string
	Tool          string
	RequiredScope string
	Allowed       bool
	Reason        string
	ApprovalRef   string
	TaskID        string // optional durable task handle correlated to this decision
	MCPTag        string // OWASP MCP Top 10 id this decision evidences (e.g. "MCP07")
	TokenBinding  string // "bearer", "dpop", or "mtls" evidence from the validated token
	// TraceParent is the W3C trace context the request carried (`_meta` preferred,
	// HTTP header fallback — SEP-414), so this PEP decision can be correlated
	// with the gen_ai spans of the same trace. A correlation identifier, never a
	// payload; empty when the caller propagated none.
	TraceParent string
	// OperationIDKind labels the OperationID provenance of an evidence-enforced
	// allow: "keyed" (client-supplied operation key — strong idempotency) or
	// "request_instance" (server-minted random id — NO transport-retry dedup
	// claimed). The journal entry is labeled with it so the evidence never
	// overstates the idempotency guarantee. Empty on denials and legacy surfaces.
	OperationIDKind string
	// EffectAction is the journal action verb of an evidence-enforced allow on a
	// NON-tools/call surface (stage 4: "mcp.task.get.<kind>",
	// "mcp.task.cancel.<kind>", "mcp.task.update.<kind>", "mcp.task.track",
	// "mcp.task.cancel.compensation", "mcp.task.cancel.sweep"). Empty means the
	// historical tools/call action ("mcp.tool.call.<kind>") — the field is
	// ADDITIVE, so the existing mcp.tool.call.* events never change.
	EffectAction string
	At           time.Time
}

// nopGateAuditor is the default GateAuditor when none is wired. It is DENY-CLOSED
// for enforcement: an ALLOW receives a ledger_unwired refusal, so an un-wired
// RS can never emit an evidence-mandatory effect — the historical "the auditor only
// observes" stance was the fail-open bug the frozen contract abolishes
// (sdk/evidence.go). Denial evidence is best-effort by doctrine, so denials (whose
// records the callers ignore) lose only their audit trail, never their refusal.
type nopGateAuditor struct{}

func (nopGateAuditor) Record(_ context.Context, _ ToolDecision, binding sdk.EvidenceBinding) GateRecord {
	return unwiredGateRecord(binding)
}

func (nopGateAuditor) BeforeEffect(_ context.Context, rec GateRecord) sdk.EvidenceReceipt {
	return unwiredReceipt(rec.Binding)
}

func (nopGateAuditor) Settle(context.Context, GateOutcome) GateSettlement {
	return GateSettlement{FailureClass: sdk.FailureEvidenceFault}
}

// unwiredReceipt is the ledger_unwired refusal receipt for a binding.
func unwiredReceipt(binding sdk.EvidenceBinding) sdk.EvidenceReceipt {
	return sdk.EvidenceReceipt{
		OperationID:  binding.OperationID,
		EffectDigest: binding.EffectDigest,
		Fault:        sdk.EvidenceFaultLedgerUnwired,
	}
}

// unwiredGateRecord is the refused record of an un-wired evidence seam.
func unwiredGateRecord(binding sdk.EvidenceBinding) GateRecord {
	return GateRecord{
		Binding:      binding,
		Receipt:      unwiredReceipt(binding),
		State:        GateRecordRefused,
		FailureClass: sdk.FailureEvidenceFault,
	}
}

// toolCallPlanVersion namespaces the tool-call plan hash.
const toolCallPlanVersion = "mcp-toolcall-v1"
const taskUpdatePlanVersion = "mcp-task-update-v1"

// toolCallPlanHash computes the anti-TOCTOU binding for a destructive tool call: a
// stable SHA-256 over (tool, subject, argumentsHash). A different tool, subject, or
// argument set changes the hash and voids a stale approval. argsHash is a digest of
// the arguments (never the arguments in the clear in the approval).
func toolCallPlanHash(tool, subject, argsHash string) string {
	return hashPlanParts(toolCallPlanVersion, tool, subject, argsHash)
}

// taskUpdatePlanHash binds a destructive tasks/update approval to the durable
// task, its CANONICAL OWNER identity, the original tool AND the exact canonical
// effect view of the update payload.
//
// The payload part is not optional (review round-1 F-08): a plan hash over
// (taskID, subject, tool) alone is PAYLOAD-BLIND — a human approves one benign
// update and the same approval then authorizes arbitrary different
// inputResponses under a new operation key, because the gate sees the same plan
// hash. effectParamsHash is the SAME operation-key/trace-excluded canonical
// digest the EffectDigest binds, so the approved plan and the anchored effect
// can never describe different updates.
func taskUpdatePlanHash(taskID, ownerDigest, tool, effectParamsHash string) string {
	return hashPlanParts(taskUpdatePlanVersion, taskID, ownerDigest, tool, effectParamsHash)
}

func hashPlanParts(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		part = strings.TrimSpace(part)
		var lb [8]byte
		n := len(part)
		for i := 0; i < 8; i++ {
			lb[i] = byte(n >> (8 * (7 - i)))
		}
		_, _ = h.Write(lb[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashArgs digests tool-call arguments for the plan binding without persisting them in
// the clear (minimal data): the SHA-256 of the argument bytes. Since the caller
// passes the CANONICAL argument encoding (canonicalizeToolCallParams), so the approval
// plan hash and the evidence EffectDigest bind the SAME canonical argument identity —
// two spellings of the same arguments can no longer carry an approval that the
// evidence layer sees as a different effect (approval/evidence disagreement).
func hashArgs(args []byte) string {
	sum := sha256.Sum256(args)
	return hex.EncodeToString(sum[:])
}

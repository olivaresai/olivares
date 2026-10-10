// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package inferencepep is the governed decision of the inline inference PEP: the
// Decider the connector's Messages proxy calls, its optional gates and the evidence it
// anchors. cmd/olivares only wires it.
package inferencepep

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// inferenceproxy.go is the decision of the OPTIONAL, OPT-IN inline inference PEP
// that cmd/olivares wires (inferenceproxyserver.go): it binds the connector's identity-blind
// protocol shell (connectors/claude-api.MessagesProxy) to the GOVERNED decision the
// connector may not import — the firm identity (via the authenticated bearer), the
// Kill-switch, the residency guard, the model-access gate, the
// per-surface context window, the DLP classifier + this tenant's egress policy, the
// Budget, and the tamper-evident ledger. It is the sibling of
// claudehookpep.go and mcpgateway.go in cmd/olivares — same split: the connector owns the
// protocol + deny-closed defaults, this package owns the decision and the upstream
// credential (NEVER the inbound one).
//
// It DELIBERATELY interposes in the inference data-path — the inverse of the product's
// read-first default (docs/SECURITY-HARDENING.md) — so it is loaded only when the operator opts in via
// OLIVARES_INFERENCE_PROXY_CONFIG (absent ⇒ nothing mounted, the boot never fails), is
// per-tenant fail-CLOSED on a decision-plane outage by default (2026-06-17), and is
// minimal-data (it inspects prompts/responses in flight but persists only fingerprints +
// findings, docs/SECURITY-HARDENING.md). It is the listener the ANTHROPIC_BASE_URL env-ref
// already point a governed Claude Code session at — and, additionally, the enforcement
// point for NON-Claude-Code (raw SDK/curl) callers, which a custom ANTHROPIC_BASE_URL
// otherwise removes from Anthropic's managed-settings reach entirely.

// proxySignalSource labels the bus events (cost samples, findings) this PEP emits.
const proxySignalSource = "inference_proxy"

// Domain separators for the per-request ledger anchor hashes (length-prefixed
// injective encoding via writeLenPrefixed/writeInt64 below).
const (
	proxyIntentDomain  = "olivares.proxy.inference.intent.v1"
	proxyOutcomeDomain = "olivares.proxy.inference.outcome.v1"
)

// proxyCallKind labels the audit target of a proxied inference call (an audit label, not
// a registered entity — like sessionIOTargetKind).
const proxyCallKind model.Kind = "inferenceproxy.call"

// ErrNoLedger is the deny-closed error a recording-MANDATING tenant gets when no ledger
// store is wired (an impossible production state, but it must fail closed, not open).
var ErrNoLedger = errBootInferenceProxy("inference-proxy: no ledger store for mandatory recording")

type errBootInferenceProxy string

func (e errBootInferenceProxy) Error() string { return string(e) }

// EvidenceRefusedError is the deny-closed error when the ledger transaction COMMITTED but
// produced no per-operation anchor — the F9 case where a DEGRADE-mode audit spool
// durably counts the drop (loss accounting) yet writes no ledger event, so the receipt
// MustRefuse. It is distinct from ErrNoLedger (no store wired) and from a raw store fault
// (which rolls the transaction back): here the loss accounting is already durable and the
// call is denied evidence-or-refuse. Carries the classified fault for honest logging.
type EvidenceRefusedError struct{ Fault sdk.EvidenceFault }

// DefaultMandatoryYieldsTo decides the ONE case where mandatory recording steps aside, and the
// principle behind it is narrower than it looks: A DEFAULT MUST NOT OVERRIDE AN EXPLICIT
// OPERATOR CHOICE.
//
// Making recording mandatory by default was right — a tenant that configured nothing is exactly
// the one nobody reasoned about. But the audit spool has its own declared policy, and an
// operator who set it to `degrade` said, in as many words, "when the spool is exhausted, drop
// the evidence and keep serving". Denying that request would let a default this tenant never
// chose silently cancel a posture the operator did choose — measured by the contrast as
// `degrade` quietly ceasing to degrade for every unconfigured tenant.
//
// So the yield is bounded by two conditions, both necessary:
//
//   - NOBODY CHOSE a posture for this tenant (`!RecordMandatoryChosen`). The first version
//     of this rule asked `!Configured`, which only means "there is no config row" — so a
//     tenant that had set the DLP mode and never mentioned evidence counted as having
//     chosen, and the rule refused to yield for exactly the person it exists to protect.
//     The signal is now a nullable column whose NULL means nobody decided; a tenant that
//     wrote record_mandatory=true asked for evidence-or-refuse and gets it, spool policy or
//     not.
//   - the fault is EXACTLY EvidenceFaultSpoolDegraded — the operator-declared drop. A
//     `spool_full` under `block`, a write error, an unwired or unavailable ledger, a lost
//     leadership: none of those is a choice anybody made, and all of them still deny.
func DefaultMandatoryYieldsTo(pol inferenceproxy.ProxyPolicy, err error) bool {
	if pol.RecordMandatoryChosen {
		return false
	}
	var refused EvidenceRefusedError
	if !errors.As(err, &refused) {
		return false
	}
	return refused.Fault == sdk.EvidenceFaultSpoolDegraded
}

func (e EvidenceRefusedError) Error() string {
	return "inference-proxy: mandatory recording refused (evidence " + string(e.Fault) + ")"
}

// ModelAccessGate is the in-band model-access decision the proxy consumes per
// request. *models.Module satisfies it.
type ModelAccessGate interface {
	EvaluateModelAccess(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef, providerRef, modelRef, surface string) (models.ModelAccessVerdict, error)
}

// PolicySource is the per-tenant proxy config + DLP policy the decider reads.
// *inferenceproxy.Module satisfies it.
type PolicySource interface {
	Policy(ctx context.Context, tenant model.TenantID) (inferenceproxy.ProxyPolicy, error)
}

// ContextPolicyResolver is the effective context-policy decision consumed by the
// inline inference proxy. *knowledge.Module satisfies it.
type ContextPolicyResolver interface {
	Apply(ctx context.Context, tenant model.TenantID, q knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error)
}

// ObservationSink publishes a cost/finding observation on the bus (best-effort).
// eventbus.Bus satisfies it; nil disables emission.
type ObservationSink interface {
	Publish(ctx context.Context, e event.Event) error
}

// Authenticator resolves the INBOUND bearer to a firm principal. *auth.Authenticator
// satisfies it.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
}

// BudgetChecker is the budget, spend-limit and admission-hold seam the proxy
// consumes. *finops.Module satisfies it.
type BudgetChecker interface {
	CheckBudget(ctx context.Context, tenant model.TenantID, dims finops.SpendDims) (finops.BudgetCheck, error)
	CheckSpendLimit(ctx context.Context, tenant model.TenantID, actorRef string, groups []string) (finops.SpendLimitCheck, error)
	Reserve(ctx context.Context, tenant model.TenantID, req finops.AdmissionRequest) (finops.Reservation, error)
	Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error
	Release(ctx context.Context, tenant model.TenantID, handle string) error
}

// KillSwitchGuard is the kill-switch state the proxy consults first.
// *governance.Module satisfies it.
type KillSwitchGuard interface {
	KillSwitchState(ctx context.Context, tenant model.TenantID) (governance.StopState, error)
}

// ApprovalOpener is the part of the approval bridge the proxy uses: Notify opens a
// governed approval for a denied egress or computer-use call, and GateOnce opens (or
// reuses) the approval for a held content-firewall detection. Neither resumes the call.
type ApprovalOpener interface {
	Notify(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) error
	GateOnce(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error)
}

var (
	_ ModelAccessGate       = (*models.Module)(nil)
	_ PolicySource          = (*inferenceproxy.Module)(nil)
	_ ContextPolicyResolver = (*knowledge.Module)(nil)
	_ BudgetChecker         = (*finops.Module)(nil)
)

// Decider is the GOVERNED brain: authenticate → kill-switch → residency →
// model-access → context-policy → DLP → egress → firewall → computer-use → ceilings →
// count_tokens sizing → budget → record. Every security edge is deny-closed and so is the
// budget gate, whose hold Finalize settles. The sizing pre-flight is the ONLY
// pre-forward upstream egress and deliberately runs AFTER every phase-one gate:
// a prompt denied by the local content/security gates is never exfiltrated through the
// token-count side channel. Denies that decide on or after the sizing itself —
// window/413, budget/spend (local FinOps reads), and the record_mandatory evidence deny
// in Authorize — necessarily follow that POST; the contract documents the exact
// guarantee and its residuals. It
// holds the upstream Inference client (operator-credentialed) so it can pre-flight
// count_tokens and so Finalize can reconcile cost via the connector's billing logic.
type Decider struct {
	Surface    sdkmodel.Gateway
	SurfaceGeo string         // operator-declared inference geo of this surface ("" = per-request)
	TenantHint model.TenantID // configured single tenant ("" = infer from the credential)
	// defaultModel is the operator-pinned model applied when a request names none, during the
	// F3 pre-governance normalization. The mounted proxy pins none today (""), so a
	// model-less request is a 400 (the effective model must be explicit to be governed and
	// frozen) — never a silent forward to a hidden default.
	defaultModel string

	Inference *claudeapi.Inference
	Auth      Authenticator
	// SessionCredentials is the shared launch issuer, admitted only on inference.
	// Its live scope, expiry and revocation checks remain with the existing owner.
	SessionCredentials *auth.SessionCredentials
	// delegation verifies a PEP-presented DelegationProof and claims a single-use
	// decision (the PDP-service adapter, resolveDelegatedIdentity). It is the
	// SUBJECT-authority source for a PEP-fronted call, distinct from Auth (which
	// authenticates the INBOUND transport credential for the bearer path).
	// *auth.Authenticator satisfies it; nil ⇒ no PDP-service path mounted.
	delegation    delegationVerifier
	Models        ModelAccessGate
	Budget        BudgetChecker
	ContextPolicy ContextPolicyResolver
	KillSwitch    KillSwitchGuard
	Policy        PolicySource
	Residency     *residency.Registry
	Store         store.Store
	Bus           ObservationSink
	// Egress is the OPTIONAL commercial server-tool egress gate (P0 #1; servertoolegressgate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ observe-only, behavior UNCHANGED
	// by design. This enterprise gate governs WHICH tools/domains are permitted, grant-based;
	// the AGPL request-ceiling gate below is only a numeric FinOps consumption cap. When
	// present it runs AFTER the security gates and BEFORE budget, and may only DENY or
	// REWRITE req.Tools — never force an Allow nor bypass a gate ahead of it.
	Egress ServerToolEgressGate
	// Approvals opens a governed approval for a denied egress (2026-06-19, D4); nil ⇒
	// deny + finding only. It NEVER resumes the synchronous proxy call. It is reused by the
	// content firewall for a held (hitl) detection.
	Approvals ApprovalOpener
	// Inspector is the OPTIONAL commercial content firewall (P1; contentinspectorgate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ no deep inspection, behavior
	// UNCHANGED. When present it runs AFTER the deny-closed security gates and BEFORE the
	// budget gate on a request, and after response DLP in Finalize; it may DENY a
	// request or WITHHOLD a buffered response, never force an Allow nor bypass a prior gate.
	Inspector ContentInspector
	// ComputerUse is the OPTIONAL computer-use governance gate (computerusegate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ computer-use tools pass
	// through ungoverned, behavior UNCHANGED. When present it runs AFTER the content
	// firewall and BEFORE the budget gate; it may only DENY, never force an Allow.
	ComputerUse ComputerUseGate
	// CircuitBreaker is the OPTIONAL enterprise circuit-breaker gate (circuitbreakergate.go). nil in the default AGPL build ⇒ no circuit-breaker,
	// behavior UNCHANGED. When present it runs AFTER the kill-switch gate and
	// BEFORE the residency gate; it may only DENY, never force an Allow.
	CircuitBreaker CircuitBreaker

	Clock func() time.Time
	Log   *slog.Logger
}

// DLPUnscannedClass is the reserved DLP class for content that could not be reduced to
// plaintext (binary/file_id/encrypted/opaque/unmodeled). The deny-closed policy
// (modules/inferenceproxy.dlpPolicy.unscannedDenied) denies it unless the tenant set an
// explicit {"class":"unscanned","action":"allow"} rule — "*" does NOT cover it.
const DLPUnscannedClass = "unscanned"

var _ claudeapi.ProxyDecider = (*Decider)(nil)

// proxySession is the opaque per-request state the connector round-trips to Finalize.
type proxySession struct {
	tenant          model.TenantID
	actor           string
	actorKind       string
	sessionRef      string
	unbindableAgent bool
	modelRef        string
	requestRef      string
	contextCoverage inferenceproxy.ContextCoverage
	toolVisibility  inferenceproxy.ToolVisibility
	pin             string
	pol             inferenceproxy.ProxyPolicy
	// inputDigest is SHA256 of the canonical marshal of the INBOUND request (before
	// normalization); effectiveDigest is SHA256 of the FROZEN bytes actually forwarded
	// (F3). The ledger anchors bind effectiveDigest so the authorized decision commits
	// to the exact octets sent upstream; a difference between the two is a governed
	// modification (normalization or a gate rewrite). Both are lowercase-hex-able 32 bytes.
	inputDigest     []byte
	effectiveDigest []byte
	// ctxPol is the request's resolved effective context policy (step 4, phase one),
	// carried so the sizing phase (5g) can apply MaxContextTokens without a second
	// decision-plane read (the two-phase chain).
	ctxPol knowledge.EffectivePolicy
	// mcp is the request's MCP egress snapshot when an egress gate is installed (5b): the
	// accepted binding of its MCP declaration or absence, checked after the later gates,
	// before every count_tokens and before the freeze. nil without an egress gate.
	mcp *claudeapi.MCPEgressSnapshot
	// admissionHandle is the hold the budget step (6) took for this call. Finalize settles
	// it: committed at the measured cost, or released when the upstream call failed. A
	// later step that refuses the call releases it at once. Empty when the budget gate is
	// off or the admission holds nothing.
	admissionHandle string
	// batchHolds are the holds of every entry of an admitted batch, carried on its batch
	// session so FinalizeBatch settles them all.
	batchHolds []string
}

// Authorize runs the PRE-forward governed gate chain for one /v1/messages call, then the
// recording reservation. It reuses authorizeChain (the deny-closed gate chain) so the
// single-message and batch paths never duplicate it. It NEVER returns Allow without a
// resolved firm identity, a non-stopped estate, an in-region surface, an authorized model,
// a non-DLP-denied prompt, and (for a recording-mandating tenant) a written ledger intent.
func (d *Decider) Authorize(ctx context.Context, req claudeapi.MessageRequest, bearer string) claudeapi.ProxyDecision {
	sess, gov, deny, ok := d.authorizeChain(ctx, req, bearer)
	if !ok {
		return deny.decision
	}
	dec := d.freezeAndRecord(ctx, sess, gov)
	if !dec.Allow {
		// The chain took the call's hold, and a step after it refused the call: the effect
		// never runs, so the hold goes back now instead of withholding until it expires.
		d.releaseAdmission(ctx, sess)
	}
	return dec
}

// freezeAndRecord is Authorize after the gate chain allowed: the freeze of the governed
// request and, for a recording-mandating tenant, the recording reservation.
func (d *Decider) freezeAndRecord(ctx context.Context, sess *proxySession, gov claudeapi.MessageRequest) claudeapi.ProxyDecision {
	// FREEZE the governed request into the opaque forward artifact (F3): the exact bytes
	// the proxy will send upstream, serialized ONCE. The EffectiveRequestDigest is over these
	// bytes, so the authorized decision and the ledger anchor commit to precisely what runs —
	// no post-governance preflight/re-marshal can diverge (the forward uses Prepared, never a
	// re-marshal of gov). With an MCP snapshot, the session's accepted binding must still be
	// the one gov carries, and MarshalPrepared verifies the frozen bytes against it.
	if res, ok := checkMCPBinding(sess, gov); !ok {
		return res.decision
	}
	prepared, ferr := claudeapi.MarshalPrepared(gov)
	if ferr != nil {
		if code, isMCP := mcpRefusalCode(ferr); isMCP {
			return mcpGateDeny(code).decision
		}
		d.Log.Error("inference-proxy: could not serialize the governed request; denying (deny-closed)", "err", ferr)
		return denyProxy(http.StatusInternalServerError, "api_error", "could not serialize the governed request (deny-closed)")
	}
	effDigest := prepared.Digest()
	sess.effectiveDigest = effDigest[:]
	toolFeatures := prepared.ToolVisibilityFeatures()
	sess.toolVisibility = inferenceproxy.RequestToolVisibility(toolFeatures.ProgrammaticToolCalling, toolFeatures.ToolSearchActive)
	sess.requestRef = NewRequestRef()
	sess.contextCoverage = inferenceContextCoverage(gov)

	// Recording reservation. For a recording-MANDATING tenant, the authorized intent
	// is anchored to the ledger BEFORE the forward — no evidence ⇒ no privileged action
	// (deny-closed). It binds the EFFECTIVE digest, so the pre-forward evidence commits to the
	// exact frozen bytes. For everyone else recording is best-effort post-forward.
	if sess.pol.RecordMandatory {
		if err := d.anchorIntent(ctx, sess); err != nil {
			if DefaultMandatoryYieldsTo(sess.pol, err) {
				d.Log.Warn("inference-proxy: the audit spool DEGRADED this request's evidence and this tenant never configured a recording posture, so the operator's declared degrade wins over the default: forwarding with a recorded evidence GAP",
					"request_ref", sess.requestRef, "err", err)
			} else {
				d.Log.Error("inference-proxy: mandatory recording intent failed; denying (deny-closed)", "err", err)
				return denyProxy(http.StatusServiceUnavailable, "api_error", "tamper-evident recording unavailable; privileged call denied")
			}
		}
	}

	buffer := gov.Stream && sess.pol.GateDLPResponse && sess.pol.ResponseDLPMode == inferenceproxy.ResponseDLPBuffer && sess.pol.DLPEnabled()
	return claudeapi.ProxyDecision{Allow: true, Request: gov, Prepared: prepared, BufferResponse: buffer, Session: sess}
}

// AuthorizeBatch governs a POST /v1/messages/batches submission per-entry (2026-06-19,
// D1): every entry's params runs through the SAME deny-closed gate chain a single message
// does, so a denied model / kill-switch / residency / DLP / egress / budget on ANY entry
// denies the WHOLE batch — nothing is forwarded (deny-closed). The whole submission is
// anchored to the ledger ONCE (not per entry). Response DLP / cost reconciliation are not
// here: a batch CREATE carries no output and the results are fetched out of band.
//
// Identity/policy are resolved ONCE, at submission admission (F2 — all entries share
// the inbound bearer ⇒ the same tenant/actor/policy). This is a DELIBERATE semantic: one
// consistent decision context governs the whole submission; a credential revocation or a
// policy tightening that lands mid-loop takes effect on the NEXT submission, not between
// entries of an already-admitted one. Every GATE still runs per entry.
func (d *Decider) AuthorizeBatch(ctx context.Context, requests []claudeapi.BatchRequest, bearer string) claudeapi.ProxyBatchDecision {
	if len(requests) == 0 {
		return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusBadRequest, ErrorType: "invalid_request_error", Reason: "batch contains no requests"}
	}
	id, idDeny, ok := d.resolveBearerIdentity(ctx, bearer)
	if !ok {
		// An identity/tenant/policy failure is not entry-specific: deny at BATCH level,
		// without naming an entry.
		return claudeapi.ProxyBatchDecision{
			Allow:     false,
			Status:    idDeny.decision.Status,
			ErrorType: idDeny.decision.ErrorType,
			Reason:    idDeny.decision.Reason,
			Headers:   idDeny.decision.Headers,
		}
	}
	// PHASE 1 (the sizing barrier) — the LOCAL gate chain for EVERY entry, before ANY entry is
	// sized. The count_tokens sizing is the only pre-forward upstream egress; running it
	// per-entry inside a single sequential loop would egress a CLEAN early entry before a
	// LATER entry's DLP/firewall deny killed the whole submission. Deny-closed: one denied
	// entry denies the whole batch; nothing forwards and nothing is sized. The per-entry
	// reason is surfaced with the entry's index/custom_id for the operator.
	governed := make([]claudeapi.BatchRequest, len(requests))
	sessions := make([]*proxySession, len(requests))
	for i, entry := range requests {
		sess, gov, deny, ok := d.runLocalGates(ctx, entry.Params, id)
		if !ok {
			return claudeapi.ProxyBatchDecision{
				Allow:     false,
				Status:    deny.decision.Status,
				ErrorType: deny.decision.ErrorType,
				Reason:    batchEntryDenyReason(i, entry.CustomID, deny.decision.Reason),
				Headers:   deny.decision.Headers,
			}
		}
		governed[i] = claudeapi.BatchRequest{CustomID: entry.CustomID, Params: gov}
		sessions[i] = sess
	}
	// PHASE 2 — sizing + budget per entry, only now that the whole submission is locally
	// clean (same deny-closed semantics: any entry's window/budget deny kills the batch).
	for i := range governed {
		if deny, ok := d.runSizingAndBudget(ctx, governed[i].Params, id, sessions[i]); !ok {
			// The entries before this one took their holds, and the submission is refused
			// whole: none of them runs, so each of those holds goes back now.
			d.releaseAdmissions(ctx, sessions[:i])
			return claudeapi.ProxyBatchDecision{
				Allow:     false,
				Status:    deny.decision.Status,
				ErrorType: deny.decision.ErrorType,
				Reason:    batchEntryDenyReason(i, requests[i].CustomID, deny.decision.Reason),
				Headers:   deny.decision.Headers,
			}
		}
	}
	dec := d.freezeAndRecordBatch(ctx, requests, governed, sessions)
	if !dec.Allow {
		// Every entry took its hold, and a step after the budget refused the submission:
		// nothing runs, so every hold goes back now.
		d.releaseAdmissions(ctx, sessions)
	}
	return dec
}

// freezeAndRecordBatch is AuthorizeBatch after every entry passed the gate chain: the
// per-entry binding checks, the freeze of the governed submission and, for a
// recording-mandating tenant, the recording reservation. On allow the batch session
// carries every entry's hold for FinalizeBatch.
func (d *Decider) freezeAndRecordBatch(ctx context.Context, requests, governed []claudeapi.BatchRequest, sessions []*proxySession) claudeapi.ProxyBatchDecision {
	// Keep the first entry's resolved session for the single batch-level ledger anchor.
	batchSess := sessions[0]
	// Each entry must still carry ITS OWN accepted MCP binding (never entry 0's).
	for i := range governed {
		if res, ok := checkMCPBinding(sessions[i], governed[i].Params); !ok {
			return claudeapi.ProxyBatchDecision{
				Allow: false, Status: res.decision.Status, ErrorType: res.decision.ErrorType,
				Reason: batchEntryDenyReason(i, requests[i].CustomID, res.decision.Reason),
			}
		}
	}
	// FREEZE the governed submission envelope into the opaque forward artifact (F3):
	// {"requests":[...]} serialized ONCE from the governed, normalized entries, so the octets
	// submitted upstream are exactly what was governed and the digest committed to (the forward
	// uses Prepared, never a re-serialize of the entries). MarshalPreparedBatch verifies each
	// bound entry's exact params bytes against that entry's binding.
	prepared, ferr := claudeapi.MarshalPreparedBatch(governed)
	if code, isMCP := mcpRefusalCode(ferr); isMCP {
		res := mcpGateDeny(code)
		return claudeapi.ProxyBatchDecision{Allow: false, Status: res.decision.Status, ErrorType: res.decision.ErrorType, Reason: res.decision.Reason}
	}
	if ferr != nil {
		d.Log.Error("inference-proxy: could not serialize the governed batch; denying (deny-closed)", "err", ferr)
		return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusInternalServerError, ErrorType: "api_error", Reason: "could not serialize the governed batch (deny-closed)"}
	}
	effDigest := prepared.Digest()
	batchSess.effectiveDigest = effDigest[:]
	toolFeatures := prepared.ToolVisibilityFeatures()
	batchSess.toolVisibility = inferenceproxy.RequestToolVisibility(toolFeatures.ProgrammaticToolCalling, toolFeatures.ToolSearchActive)
	batchSess.requestRef = NewRequestRef()
	batchSess.contextCoverage = batchInferenceContextCoverage(governed)
	batchSess.modelRef = "batch"
	if batchSess.pol.RecordMandatory {
		if err := d.anchorBatchIntent(ctx, batchSess, len(governed)); err != nil {
			if DefaultMandatoryYieldsTo(batchSess.pol, err) {
				d.Log.Warn("inference-proxy: the audit spool DEGRADED this batch's evidence and this tenant never configured a recording posture, so the operator's declared degrade wins over the default: forwarding with a recorded evidence GAP",
					"request_ref", batchSess.requestRef, "err", err)
			} else {
				d.Log.Error("inference-proxy: mandatory batch recording intent failed; denying (deny-closed)", "err", err)
				return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusServiceUnavailable, ErrorType: "api_error", Reason: "tamper-evident recording unavailable; batch denied"}
			}
		}
	}
	for _, sess := range sessions {
		if sess.admissionHandle != "" {
			batchSess.batchHolds = append(batchSess.batchHolds, sess.admissionHandle)
		}
	}
	return claudeapi.ProxyBatchDecision{Allow: true, Requests: governed, Prepared: prepared, Session: batchSess}
}

// FinalizeBatch settles the hold of every entry (settleBatch) and anchors the batch
// submission outcome to the ledger (best-effort + loud). A batch CREATE response carries no
// model output, so there is no response DLP / cost reconciliation — the per-entry
// request-side chain already governed each entry.
func (d *Decider) FinalizeBatch(ctx context.Context, sessAny any, out claudeapi.ProxyBatchForwardResult) {
	sess, _ := sessAny.(*proxySession)
	if sess == nil {
		return
	}
	decision := "allow"
	if out.UpstreamErr {
		decision = "upstream-error"
	}
	d.settleBatch(ctx, sess, out.UpstreamErr)
	d.anchorBatchOutcome(ctx, sess, out, decision)
}

// settleBatch settles the hold of every entry of a batch submission. A submission the
// upstream refused ran nothing, so each hold is released. An accepted one has no cost yet:
// its results are billed when their cost is ingested, and the ingested cost is what the
// budgets count from then on, so each hold is committed at zero.
func (d *Decider) settleBatch(ctx context.Context, sess *proxySession, upstreamErr bool) {
	for _, h := range sess.batchHolds {
		if upstreamErr {
			d.releaseHold(ctx, sess.tenant, h)
		} else {
			d.commitHold(ctx, sess.tenant, h, 0)
		}
	}
}

// --- F2: the post-identity seam (resolvedIdentity + runGates) --------------------

// gateCode is the stable, per-gate deny identifier. It is PEP-neutral semantics — the
// PDP handlers surface it as sdk.DecisionVerdict.ReasonCode ("stable, per-gate,
// interoperable") without string-matching the human-readable Reason prose. One constant per
// deny site; renaming one is a wire-visible change once ships. The five MCP egress codes
// are not repeated here: their single definition is claudeapi.MCPDenialCode, and mcpDenials
// in servertoolegressgate.go maps each one (code = public reason = gate code) — a census or
// rename review of this block must include that table.
type gateCode string

const (
	gateCodeIdentityUnverified   gateCode = "identity_unverified"
	gateCodeAuthentication       gateCode = "authentication"
	gateCodeAuthPlaneUnavailable gateCode = "authentication_plane_unavailable"
	// Delegation (PDP-service) identity codes: the adapter maps the delegation
	// verifier's typed domain faults onto these. They are DISTINCT from the bearer
	// path's codes so the verdict layer can tell a PEP-service subject-delegation
	// failure apart from an inbound-credential authentication failure.
	gateCodeDelegationProtocol         gateCode = "delegation_protocol"
	gateCodeDelegationInvalid          gateCode = "delegation_invalid"
	gateCodeDelegationReplay           gateCode = "delegation_replay"
	gateCodeDelegationEvidenceFault    gateCode = "delegation_evidence_fault"
	gateCodeDelegationPlaneUnavailable gateCode = "delegation_plane_unavailable"
	gateCodeTenantUnresolved           gateCode = "tenant_unresolved"
	gateCodeRequestMalformed           gateCode = "request_malformed"
	gateCodePolicyUnreadable           gateCode = "policy_unreadable"
	gateCodeKillSwitch                 gateCode = "kill_switch"
	gateCodeKillSwitchUnreadable       gateCode = "kill_switch_unreadable"
	gateCodeCircuitBreaker             gateCode = "circuit_breaker"
	gateCodeResidency                  gateCode = "residency"
	gateCodeResidencyUnreadable        gateCode = "residency_unreadable"
	gateCodeModelAccess                gateCode = "model_access"
	gateCodeModelAccessUnreadable      gateCode = "model_access_unreadable"
	gateCodeContextPolicy              gateCode = "context_policy"
	gateCodeContextPolicyUnreadable    gateCode = "context_policy_unreadable"
	gateCodeContextWindow              gateCode = "context_window"
	gateCodeContextCeiling             gateCode = "context_ceiling"
	gateCodeDLPRequest                 gateCode = "dlp_request"
	gateCodeServerToolEgress           gateCode = "server_tool_egress"
	gateCodeContentFirewall            gateCode = "content_firewall"
	gateCodeComputerUse                gateCode = "computer_use"
	gateCodeRequestCeiling             gateCode = "request_ceiling"
	gateCodeBudget                     gateCode = "budget"
	gateCodeBudgetThrottle             gateCode = "budget_throttle"
	gateCodeSpendLimit                 gateCode = "spend_limit"
)

// gateResult is the semantic outcome of the identity phase + gate chain. The zero value is
// a deny (decision.Allow == false) with no presentation — fail-closed by construction. It
// carries BOTH the legacy transport presentation (claudeapi.ProxyDecision, HTTP/Anthropic-
// shaped) and the PEP-neutral semantics (code + class) a PDP adapter maps to
// sdk.DecisionVerdict{ReasonCode, FailureClass}. The class taxonomy is sdk/pdp.go's: a firm
// policy refusal is FailurePolicyDeny; a governance READ fault in fail_open territory is
// FailurePolicyReadFault; a decision-plane outage is FailurePlaneUnavailable.
type gateResult struct {
	decision claudeapi.ProxyDecision
	code     gateCode
	class    sdk.FailureClass
}

// resolvedIdentity is the sealed post-identity snapshot the gate chain (runGates) consumes.
// It is the F2 seam: identity RESOLUTION (who is calling, which tenant, which policy)
// is an adapter concern — the bearer adapter below authenticates the inbound transport
// credential; the future PDP-service adapter (S4) verifies a DelegationProof presented
// by an authenticated PEP service (sdk/pdp.go invariant #2) — while the deny-closed gate
// chain itself is shared verbatim.
//
// The derived fields (actor/actorKind/sessionRef/unbindableAgent/subjectKind/subjectID) are
// CACHED DERIVATIONS of the principal, computed ONLY by newResolvedIdentity with the same
// formula the downstream gates use internally (modules/models/modelaccessgate.go F-01: only
// Principal.AgentIdentity — set server-side by a verifier — binds a token to an agent). An
// adapter expresses its subject semantics by CONSTRUCTING the principal (e.g.
// WithAgentIdentity after verifying a delegation), never by overriding a derived field —
// that is what keeps every gate seeing the SAME subject.
//
// subjectKind/subjectID are the normative subject identifier a PDP verdict returns as
// ResolvedSubject (sdk/pdp.go): actor is a prefixed AUDIT string ("user:<id>"), not an ID.
type resolvedIdentity struct {
	principal auth.Principal
	tenant    model.TenantID
	pol       inferenceproxy.ProxyPolicy

	actor           string
	actorKind       string
	sessionRef      string
	unbindableAgent bool
	subjectKind     string
	subjectID       string

	// ok marks the snapshot as sealed by newResolvedIdentity. runGates DENIES an unsealed
	// snapshot: the zero value of ProxyPolicy has every configurable gate OFF, so a
	// fabricated resolvedIdentity{} would otherwise run almost ungoverned.
	ok bool
}

// newResolvedIdentity seals the post-identity snapshot.
//
// SECURITY PRECONDITION (F2): every argument MUST come from a VERIFIED resolution — an
// authenticated inbound credential (resolveBearerIdentity) or, in the future PDP adapter, a
// verified DelegationProof bound to an authenticated PEP service. runGates trusts this
// snapshot completely; sealing one from unverified input (e.g. an auth.ScopedPrincipal,
// which "authenticates NOTHING" — core/auth/scoped.go) bypasses authentication entirely.
// Construction is pinned to the authorized adapters by
// TestResolvedIdentityConstructionAllowlist; a zero/unsealed snapshot is pinned to deny by
// TestRunGatesRejectsUnsealedSnapshot.
func newResolvedIdentity(p auth.Principal, tenant model.TenantID, pol inferenceproxy.ProxyPolicy) (resolvedIdentity, bool) {
	if p.Kind == "" || tenant.IsZero() {
		return resolvedIdentity{}, false
	}
	// A raw API token carries no authenticated NHI binding: explicitly unbindable so
	// agent-scoped governance cannot silently fall through to a broader tenant/global
	// policy (the modelaccessgate F-01 formula, kept in lockstep by credentialAgent).
	sessionRef, unbindableAgent := CredentialAgent(p)
	subjectID := p.UserID.String()
	if p.Kind == auth.KindToken {
		subjectID = p.CredID.String()
	}
	return resolvedIdentity{
		principal: p, tenant: tenant, pol: pol,
		actor: p.Actor(), actorKind: p.ActorKind(),
		sessionRef:      sessionRef,
		unbindableAgent: unbindableAgent,
		subjectKind:     string(p.Kind),
		subjectID:       subjectID,
		ok:              true,
	}, true
}

// CredentialAgent is the agent an authenticated credential proves, and whether the credential
// is an API token that proves none. Only Principal.AgentIdentity — set server-side by the
// authenticator from the credential's own agent binding — names an agent; a caller-declared
// reference (a body field, a request header) never does. A token without that binding is
// unbindable: agent-scoped governance cannot tell which agent it is, so it must deny rather
// than fall through to a broader policy. A human session is not an agent and is never
// unbindable here. Both firewalls take the proven agent from here; the hooks PEP classifies
// unbindability more strictly (resolveHookAgent), because every hook request is an agent's.
func CredentialAgent(p auth.Principal) (agentRef string, unbindable bool) {
	return p.AgentIdentity, p.Kind == auth.KindToken && p.AgentIdentity == ""
}

// resolveBearerIdentity is the BEARER adapter for the runGates seam: firm identity from the
// INBOUND transport credential (never a body field —). An unauthenticated call is
// "unknown" attribution and is denied, never run as a fabricated principal.
func (d *Decider) resolveBearerIdentity(ctx context.Context, bearer string) (resolvedIdentity, gateResult, bool) {
	var principal auth.Principal
	var authErr error
	if strings.HasPrefix(bearer, sessionHookTokenPrefix) {
		authErr = auth.ErrUnauthenticated
		if d.SessionCredentials != nil {
			principal, authErr = d.SessionCredentials.Authenticate(ctx, bearer)
		}
	} else {
		principal, authErr = d.Auth.Authenticate(ctx, bearer)
	}
	if authErr != nil {
		// Distinguish a genuinely-invalid credential from a decision-plane outage: the
		// authenticator returns ErrUnauthenticated for a bad/expired/revoked credential but
		// propagates the raw STORE error on a plane fault (core/auth/authenticator.go:123,
		// 138,145). Collapsing both onto one class would (a) tell a client its token is bad
		// when the plane is down, and (b) poison the mapping with a firm-refusal class
		// for a fault. So: a bad credential is FailureDelegationInvalid/401 (for the BEARER
		// adapter the inbound credential IS the subject's authority — it delegates nothing —
		// so this collapses onto the nearest firm subject-authority class; the S4
		// PDP-service adapter separates PEP-service auth from subject delegation and must NOT
		// inherit this conflation). A plane fault is FailurePlaneUnavailable/503 deny-closed,
		// consistent with every other unreadable-plane gate below (policy/kill-switch).
		if errors.Is(authErr, auth.ErrUnauthenticated) {
			return resolvedIdentity{}, gateDeny(gateCodeAuthentication, sdk.FailureDelegationInvalid, http.StatusUnauthorized, "authentication_error", "the inbound credential could not be authenticated"), false
		}
		d.Log.Warn("inference-proxy: authentication plane unreadable; denying (deny-closed)", "err", authErr)
		return resolvedIdentity{}, gateDeny(gateCodeAuthPlaneUnavailable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "authentication plane unavailable (deny-closed)"), false
	}
	tenant, ok := d.ResolveTenant(principal)
	if !ok {
		return resolvedIdentity{}, gateDeny(gateCodeTenantUnresolved, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "tenant not resolvable from the inbound credential"), false
	}
	// Load the per-tenant governance config. A read error means the decision plane is
	// unreadable — we cannot even read the fail-open knob, so this is the proxy-DOWN case:
	// default fail-CLOSED (2026-06-17, D1). A security proxy that cannot decide must
	// not forward.
	pol, perr := d.Policy.Policy(ctx, tenant)
	if perr != nil {
		d.Log.Warn("inference-proxy: governance config unreadable; denying (deny-closed)", "err", perr)
		return resolvedIdentity{}, gateDeny(gateCodePolicyUnreadable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "governance configuration unavailable (deny-closed)"), false
	}
	id, sealed := newResolvedIdentity(principal, tenant, pol)
	if !sealed {
		// An authenticated principal with no kind / a zero tenant is an impossible
		// production state; refuse it rather than run gates over a half-resolved subject.
		return resolvedIdentity{}, gateDeny(gateCodeIdentityUnverified, sdk.FailureDelegationInvalid, http.StatusServiceUnavailable, "api_error", "identity resolution incomplete (deny-closed)"), false
	}
	return id, gateResult{}, true
}

// authorizeChain composes the bearer adapter with the shared gate chain — the legacy
// single-entrypoint shape Authorize uses. AuthorizeBatch resolves identity ONCE and calls
// runGates per entry instead.
func (d *Decider) authorizeChain(ctx context.Context, req claudeapi.MessageRequest, bearer string) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
	id, deny, ok := d.resolveBearerIdentity(ctx, bearer)
	if !ok {
		return nil, claudeapi.MessageRequest{}, deny, false
	}
	return d.runGates(ctx, req, id)
}

// runGates runs the PRE-forward deny-closed gate chain for ONE request over an
// already-resolved, SEALED identity snapshot, WITHOUT the recording reservation (the
// caller decides: a single message reserves per request; a batch reserves once). It
// composes the two phases: runLocalGates (kill-switch → circuit-breaker →
// normalize → residency → model-access → context-policy → DLP → server-tool egress →
// content firewall → computer-use → request ceilings → forwardability re-validate, all
// LOCAL) and then runSizingAndBudget (count_tokens sizing + budget hold). Order is
// load-bearing twice over: every security gate (deny-closed) runs BEFORE the budget gate,
// so a request a security gate refuses never takes a hold (the precedent);
// and every LOCAL gate runs BEFORE the sizing pre-flight, so a denied prompt is never
// egressed to the provider through the count_tokens side channel. On allow it
// returns the resolved session (requestRef unset) and the GOVERNED request (egress/
// ceilings may have rewritten req.Tools or output_config); on deny it returns ok=false
// and the deny result.
//
// SECURITY PRECONDITION: id MUST be sealed by newResolvedIdentity from a verified
// resolution (see that constructor's doc); an unsealed snapshot denies below. The
// surface/provider context (d.Surface, d.SurfaceGeo, the hardcoded "anthropic" provider in
// spendDims) is DECIDER-immutable, fixed per listener at build time — a future PDP handler
// binds one decider per registered PEP surface rather than passing surface through this
// seam (sdk/pdp.go: nonce, digests and PEP service identity are Decide-phase protocol
// state, owned by the handler layer, not subject state).
//
// INVARIANT (F1) — the inference PDP is ALWAYS-ENFORCE. A definitive deny from any gate
// (policy, model-access, DLP, ceilings, residency, kill-switch, budget) is a HARD deny.
// Constrained-observe — allow-but-record a ClassPolicy deny — is a HOOK-PEP mode ONLY
// (54-55 places "OBSERVE en superficies que no
// sean el hook-PEP" out of scope). It MUST NOT be wired onto this surface without a dedicated
// per-surface grant lifecycle + invariant-dominates seam; there is no tenant knob that turns
// an inference deny into a shadowed allow. Pinned by
// TestProxyAuthorizeAlwaysEnforcesNoConstrainedObserveShadow.
func (d *Decider) runGates(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
	sess, gov, deny, ok := d.runLocalGates(ctx, req, id)
	if !ok {
		return nil, claudeapi.MessageRequest{}, deny, false
	}
	if sdeny, sok := d.runSizingAndBudget(ctx, gov, id, sess); !sok {
		return nil, claudeapi.MessageRequest{}, sdeny, false
	}
	return sess, gov, gateResult{}, true
}

// runLocalGates is the FIRST phase of the gate chain: every gate that decides
// WITHOUT leaving the process, deny-closed, ending with the post-rewrite forwardability
// re-validation (5f). It performs NO upstream I/O — the sizing pre-flight lives in
// runSizingAndBudget so a local deny can never be preceded by provider egress. On allow
// the returned session carries the resolved ctxPol for the later sizing phase.
func (d *Decider) runLocalGates(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
	// 0. Seal check (deny-closed): an unsealed snapshot means a caller bypassed the
	//    authorized adapters — refuse before reading a single policy knob (the zero
	//    ProxyPolicy would have every configurable gate off).
	if !id.ok {
		return chainDeny(gateCodeIdentityUnverified, sdk.FailureDelegationInvalid, http.StatusServiceUnavailable, "api_error", "unverified identity snapshot (deny-closed)")
	}
	principal, tenant, pol := id.principal, id.tenant, id.pol
	actor, sessionRef, unbindableAgent := id.actor, id.sessionRef, id.unbindableAgent

	// 1. Kill-switch, BEFORE everything: an active emergency stop outranks every
	//    other consideration. Fail-closed on a read error — and this gate is DELIBERATELY
	//    NOT subject to the per-tenant fail_open knob: the emergency brake must never be
	//    defeated by a transient read fault ("error ⇒ stopped"). The knob governs
	//    the softer governance reads (residency, model-access) below, never the stop.
	st, kerr := d.KillSwitch.KillSwitchState(ctx, tenant)
	if kerr != nil {
		return chainDeny(gateCodeKillSwitchUnreadable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "kill-switch state unreadable (deny-closed)")
	}
	if _, stopped := st.Stopped(actor); stopped {
		return chainDeny(gateCodeKillSwitch, sdk.FailurePolicyDeny, http.StatusServiceUnavailable, "api_error", "emergency stop active; inference is suspended until a dual-control re-enable")
	}

	// 1b. Circuit-breaker gate (enterprise only). Checks after the kill-switch
	//     because a kill-switch trumps a circuit-breaker. A nil engine (the open
	//     build) skips this gate entirely. Fails open on error — the kill-switch
	//     is the hard stop; the circuit-breaker is a softer enforcement layer.
	//
	//     IT IS ASKED ABOUT sessionRef, NOT actor, and the difference is the whole gate. The
	//     breaker engine persists per-agent state under the AGENT'S identity, so querying it
	//     with the audit actor — a prefixed string like `user:<id>` or `token:<id>` — asks
	//     about a key that engine never wrote, and a tripped breaker stays invisible. Every
	//     other agent-scoped consumer on this chain already takes sessionRef; this was the one
	//     outlier, and a test asserting merely "non-empty" kept it green because the actor is
	//     non-empty too.
	//
	//     An UNBINDABLE agent (a raw token with no authenticated NHI binding) yields an empty
	//     sessionRef, and circuitBreakerGateCheck skips on empty by construction. That is the
	//     documented posture for agent-scoped governance here: it must not silently fall
	//     through to a broader key. The kill-switch above remains the hard stop.
	if denied, reason := CircuitBreakerGateCheck(ctx, d.CircuitBreaker, tenant, sessionRef); denied {
		return chainDeny(gateCodeCircuitBreaker, sdk.FailurePolicyDeny, http.StatusServiceUnavailable, "api_error", reason)
	}

	// 1c. NORMALIZE to the EFFECTIVE request (F3), AFTER the emergency gates (an active
	//     stop outranks a malformed request) and BEFORE any gate that reads the request body.
	//     preflight (model default, sampling withhold, thinking normalize) used to run AFTER
	//     governance, so the forwarded octets diverged from what the gates decided over and the
	//     ledger recorded. Doing it here makes model-access decide the REAL model and
	//     count_tokens size the EFFECTIVE request; the frozen bytes below are exactly these.
	//     A normalization/validation failure is a malformed request (e.g. no model on a proxy
	//     that pins no default, an invalid service_tier) — a 400 with FailureProtocolError,
	//     pre-governance, no forward. The inbound digest is captured over the canonical marshal
	//     BEFORE normalization so a governed modification is detectable (input≠effective).
	inboundBytes, _ := json.Marshal(req) // MessageRequest always marshals; err is unreachable
	inboundDigest := sha256.Sum256(inboundBytes)
	effReq, nerr := claudeapi.NormalizeMessageRequest(req, d.defaultModel)
	if nerr != nil {
		// Do NOT surface nerr.Error(): the connector's validation messages embed request field
		// VALUES the caller controls (service_tier, model, tool_choice.type, fallback names, a
		// message role), and this Reason flows to the SOC auditor + the ledger — a secret (or
		// megabytes) stuffed in a shape field must not leak there (minimal-data, docs/SECURITY-HARDENING.md).
		// This deny also precedes the DLP gate, so the value was never classified. Log a generic
		// line (no value) and return a generic client 400.
		d.Log.Warn("inference-proxy: request failed forwardability normalization; denying (deny-closed)")
		return chainDeny(gateCodeRequestMalformed, sdk.FailureProtocolError, http.StatusBadRequest, "invalid_request_error", "request failed client-side validation and cannot be forwarded")
	}
	req = effReq

	// Resolve the tenant's residency pin once (reused by residency + the post-forward proof).
	// A pin READ FAULT honors the per-tenant fail_open knob (2026-06-17): default
	// fail-CLOSED (deny), but a tenant may opt to fail OPEN on a decision-plane outage —
	// loud + evidenced (a posture finding), never silent.
	pin := ""
	if d.Residency != nil && d.Residency.Enforces() {
		p, rerr := d.orgPin(ctx, tenant)
		switch {
		case rerr != nil && !pol.FailOpen:
			return chainDeny(gateCodeResidencyUnreadable, sdk.FailurePolicyReadFault, http.StatusForbidden, "permission_error", "residency check failed (deny-closed)")
		case rerr != nil:
			d.Log.Warn("inference-proxy: residency pin unreadable; failing OPEN per tenant config (evidence gap)", "err", rerr)
			d.emitPlaneOutageFinding(ctx, tenant, "residency")
			// pin stays "" → the residency enforcement + post-forward proof are skipped.
		default:
			pin = p
		}
	}

	// 2. Residency, fail-closed — pre-forward ONLY when the surface's geo is known
	//    (the operator-declared geo, e.g. an AWS region). For a per-request-routed surface
	//    (direct us|global) with no declared geo the crossing is only knowable post-hoc, so
	//    it is verified as a detective finding in Finalize against usage.inference_geo. A
	//    definitive incompatibility ALWAYS denies (the fail_open knob covers a read outage,
	//    not a clear residency violation).
	if pol.GateResidency && pin != "" && d.SurfaceGeo != "" {
		if !residency.InferenceGeoCompatible(pin, d.SurfaceGeo) {
			return chainDeny(gateCodeResidency, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "data residency: this surface's region is not permitted for the tenant")
		}
	}

	// 3. Model-access PEP, fail-closed. The real concrete surface string is passed
	//    (an empty surface would silently disable surface-scoped grant enforcement). A read
	//    FAULT honors the fail_open knob; a definitive !Allowed verdict ALWAYS denies.
	if pol.GateModelAccess {
		v, merr := d.Models.EvaluateModelAccess(ctx, tenant, principal, sessionRef, "", req.Model, string(d.Surface))
		switch {
		case merr != nil && !pol.FailOpen:
			return chainDeny(gateCodeModelAccessUnreadable, sdk.FailurePolicyReadFault, http.StatusForbidden, "permission_error", "model-access check failed (deny-closed)")
		case merr != nil:
			d.Log.Warn("inference-proxy: model-access unreadable; failing OPEN per tenant config (evidence gap)", "err", merr)
			d.emitPlaneOutageFinding(ctx, tenant, "model_access")
		case !v.Allowed:
			return chainDeny(gateCodeModelAccess, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", firstNonEmpty(v.Reason, "this model is not authorized on this surface/workspace"))
		}
	}

	// 4. Context-POLICY resolution + deny — LOCAL. The count_tokens SIZING that
	//    used to live here moved to runSizingAndBudget: it is the only pre-forward
	//    upstream egress (it carries system/messages/tools/MCP to the provider), so it must
	//    never run before the local content gates (DLP, firewall) — a denied prompt was being
	//    exfiltrated through the token-count side channel. The resolved ctxPol rides on the
	//    session so the later sizing can apply MaxContextTokens without a second plane read.
	var ctxPol knowledge.EffectivePolicy
	if pol.GateContextWindow && d.ContextPolicy != nil {
		var cperr error
		ctxPol, cperr = d.ContextPolicy.Apply(ctx, tenant, knowledge.ContextPolicyQuery{Principal: principal, Model: req.Model})
		switch {
		case cperr != nil && !pol.FailOpen:
			return chainDeny(gateCodeContextPolicyUnreadable, sdk.FailurePolicyReadFault, http.StatusServiceUnavailable, "api_error", "context policy unavailable (deny-closed)")
		case cperr != nil:
			ctxPol = knowledge.EffectivePolicy{}
			d.Log.Warn("inference-proxy: context policy unreadable; failing OPEN per tenant config (evidence gap)", "err", cperr)
			d.emitPlaneOutageFinding(ctx, tenant, "context_policy")
		case ctxPol.Deny:
			return chainDeny(gateCodeContextPolicy, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "context policy forbids this request for the subject")
		}
	}

	// Collect the request content ONCE (system + messages, across ALL channels — text,
	// document, image, file_id, tool_result, web_search, …). The DLP gate and the content
	// firewall both read it; skip the walk when neither needs it.
	var reqContent claudeapi.CollectedContent
	if (pol.GateDLPRequest && pol.DLPEnabled()) || d.Inspector != nil {
		reqContent = claudeapi.CollectRequestContent(req)
	}

	// 5. DLP on the prompt, fail-closed. The deterministic classifier runs over EVERY
	//    extractable channel (no longer just b.Text — capability-gaps #9); content that cannot
	//    be reduced to plaintext (binary/file_id/encrypted/opaque) is the reserved UNSCANNED
	//    class, denied by the already-existing deny-closed posture unless the tenant opted out
	//    with an explicit {"class":"unscanned","action":"allow"} rule. A denied class blocks
	//    the request before any byte egresses — including the count_tokens sizing, which
	//    runs in the LATER phase (5g): before that fix the pre-flight POSTed the
	//    prompt to the provider ahead of this gate. NOTE the classifier walks System +
	//    Messages; Tools/ToolChoice/Thinking/MCPServers are transmitted by the sizing and
	//    the forward but are not DLP-classified channels (they are governed by the egress/
	//    firewall/ceilings gates instead). Stock policy seeds secret and unscanned denies;
	//    exact tenant rules can tune either class.
	if pol.GateDLPRequest && pol.DLPEnabled() {
		denied := pol.DLPDecide(ClassifyText(reqContent.Texts))
		if reqContent.Unscanned && pol.DLPUnscannedDenied() {
			denied = append(denied, DLPUnscannedClass)
		}
		if len(denied) > 0 {
			d.emitDLPFinding(ctx, tenant, "input", req.Model, denied)
			return chainDeny(gateCodeDLPRequest, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "request blocked by data-loss-prevention policy")
		}
	}

	// 5b. Server-tool egress (commercial add-on P0 #1, OPT-IN), deny-closed. Governs Claude's
	//     internet-reaching server tools (web_search/web_fetch/code_execution) and declared
	//     remote MCP servers in req.Tools/req.MCPServers: denies an ungranted or unvalidatable
	//     tool or MCP origin and validates+clamps allowed_domains/blocked_domains/max_uses
	//     against the tenant's egress grant, rewriting the forwarded request. It is a SECURITY
	//     gate, so it runs here — after the other deny-closed gates (it cannot bypass them)
	//     and BEFORE the budget gate. nil ⇒ the default AGPL build: observe-only,
	//     unchanged, no MCP capture. With a gate, governEgress captures the MCP declaration (or
	//     its absence) once and returns the governed request with its accepted binding; the
	//     snapshot rides on the session for the checks after the later gates.
	var mcpSnap *claudeapi.MCPEgressSnapshot
	if d.Egress != nil {
		gov, snap, deny, ok := d.governEgress(ctx, req, tenant, actor, sessionRef, unbindableAgent)
		if !ok {
			return nil, claudeapi.MessageRequest{}, deny, false
		}
		req, mcpSnap = gov, snap
	}

	// 5c. Content firewall (commercial add-on P1, OPT-IN), deny-closed. Deep inline inspection
	//     of the request content — prompt-injection over the untrusted channels the model
	//     ingests, plus exfiltration signals — that the PEP and the core DLP do not do. It is a
	//     SECURITY gate, so it runs here: after the other deny-closed gates (it cannot bypass
	//     them) and BEFORE the budget gate. nil ⇒ the default AGPL build (no deep
	//     inspection, unchanged). ActorRef is the (empty) proxy sessionRef — agent-scoped
	//     policies need the NHI binding; tenant/global policies govern raw-token traffic.
	if d.Inspector != nil {
		inDec := d.runContentInspector(ctx, tenant, actor, claudeapi.InspectDirectionRequest, req.Model, reqContent, sessionRef, unbindableAgent)
		if !inDec.Forward {
			// Deny-closed on ANY non-forward (safer than the connector contract's "no
			// decision ⇒ clean pass" for a SECURITY gate). But the FailureClass distinguishes
			// the two: a firewall deny sets a Status (a FIRM refusal ⇒ FailurePolicyDeny),
			// whereas a zero Status is the contract's "no decision" — the classifier produced
			// no real verdict, so it is a FailureClassificationFault, not a policy refusal
			// (connectors/claude-api/inspectiondecision.go:52-53). Keeping the taxonomy honest
			// matters for the verdict mapping; egress/computer-use differ — their zero
			// value is a DELIBERATE deny (egressdecision.go:12), so they stay FailurePolicyDeny.
			status := inDec.Status
			class := sdk.FailurePolicyDeny
			if status == 0 {
				status = http.StatusForbidden
				class = sdk.FailureClassificationFault
			}
			return chainDeny(gateCodeContentFirewall, class, status, firstNonEmpty(inDec.ErrorType, "permission_error"), firstNonEmpty(inDec.Reason, "request blocked by content firewall policy"))
		}
	}

	// 5d. Computer-use governance (OPT-IN), deny-closed. Governs computer-use tool
	//     declarations (computer_20241022 / computer_20250124) in req.Tools: the gate checks
	//     the tenant's computer-use policy and may deny the request. It is a SECURITY gate,
	//     so it runs here — after the content firewall and BEFORE the budget gate. nil ⇒ the
	//     default AGPL build: computer-use tools pass through ungoverned. It receives the
	//     MCPGateTools view: every MCP slot is a fresh marker, never the request's own value.
	if d.ComputerUse != nil && claudeapi.HasComputerUseTool(req.Tools) {
		cuDec := d.ComputerUse.GovernComputerUse(ctx, claudeapi.ComputerUseInput{
			Tenant: tenant.String(), ActorRef: sessionRef, Tools: claudeapi.MCPGateTools(req.Tools),
		})
		d.publishComputerUseFindings(ctx, tenant, req.Model, cuDec.Findings)
		if !cuDec.Forward {
			d.openComputerUseApproval(ctx, tenant, actor, cuDec.ApprovalIntent)
			status := cuDec.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			return chainDeny(gateCodeComputerUse, sdk.FailurePolicyDeny, status, firstNonEmpty(cuDec.ErrorType, "permission_error"), firstNonEmpty(cuDec.Reason, "computer-use denied by policy"))
		}
	}

	// 5e. Per-request consumption ceilings (#19), observe by default and enforce only when
	//     the tenant explicitly sets ceilings_enforce=true. This is NOT the enterprise
	//     server-tool egress gate: it governs numeric FinOps caps only (max_tokens,
	//     task_budget.total, tool max_uses), not which tools/domains are allowed. It runs
	//     after enterprise egress, so a grant's own max_uses clamp happens first and this
	//     numeric ceiling can only tighten further, never loosen. In enforce mode,
	//     max_tokens/task_budget violations deny instead of silently clamping because
	//     clamping changes response-truncation semantics the client sees.
	if pol.Ceilings.Any() {
		violations := requestCeilingViolations(req, pol.Ceilings)
		// Every violation is EVIDENCED in both modes (the egress-gate precedent: a
		// governed deny or rewrite is never silent). Observe stops at the finding;
		// enforce then denies a hard violation or clamps the tool ceilings.
		if len(violations) > 0 {
			d.emitRequestCeilingFinding(ctx, tenant, req.Model, violations, pol.Ceilings.Enforce)
		}
		if pol.Ceilings.Enforce {
			if hasHardCeilingViolation(violations) {
				return chainDenyWithHeaders(gateCodeRequestCeiling, sdk.FailurePolicyDeny, http.StatusPaymentRequired, "billing_error", "request exceeds the tenant per-request consumption ceiling", noRetryHeader())
			}
			req = enforceRequestCeilings(req, pol.Ceilings)
		}
	}

	// 5f. All LOCAL deny-closed gates passed. RE-VALIDATE the governed request (F3): the
	// gates may have rewritten tools/output_config (egress, ceilings); running the PURE
	// forwardability guards again — no mutation — catches a gate-introduced invalid state
	// BEFORE anything leaves the process (the reorder: this now precedes the count_tokens sizing,
	// so an unforwardable rewrite is never egressed either) and before we freeze and forward
	// it. A failure here is an internal inconsistency, not the caller's fault: deny closed
	// 500 (FailureProtocolError). The FREEZE of the effective bytes happens in the caller
	// (Authorize/AuthorizeBatch), which builds the PreparedRequest/PreparedBatch and the
	// EffectiveRequestDigest over exactly what the chain returns.
	if verr := claudeapi.ValidateForwardable(req); verr != nil {
		d.Log.Error("inference-proxy: governed request failed forwardability re-validation (deny-closed)", "err", verr)
		return chainDeny(gateCodeRequestMalformed, sdk.FailureProtocolError, http.StatusInternalServerError, "api_error", "governed request is not forwardable (deny-closed)")
	}
	// The session carries the resolved context; the caller mints the requestRef and (single-
	// message) does the recording reservation. req is the GOVERNED, normalized request.
	sess := &proxySession{
		tenant: tenant, actor: actor, actorKind: id.actorKind, sessionRef: sessionRef, unbindableAgent: unbindableAgent,
		modelRef: req.Model, pin: pin, pol: pol, inputDigest: inboundDigest[:], ctxPol: ctxPol, mcp: mcpSnap,
	}
	// The later gates (firewall, computer-use, ceilings) must not have changed the accepted
	// MCP declaration or absence, nor dropped its binding (mcp_binding_changed, 500).
	if res, ok := checkMCPBinding(sess, req); !ok {
		return nil, claudeapi.MessageRequest{}, res, false
	}
	return sess, req, gateResult{}, true
}

// runSizingAndBudget is the SECOND phase of the gate chain: the count_tokens
// sizing pre-flight (5g) and the budget admission hold (6). It runs ONLY after
// runLocalGates cleared the request — the sizing POST is the single pre-forward upstream
// egress in the proxy (it carries system, messages, tools and MCP servers), so no
// PHASE-ONE (content/security) deny may ever be preceded by it. The denies decided HERE
// (window 400/413, budget 402/429) necessarily ride on or follow the sizing POST — they
// govern a request the content gates already cleared, so that egress carries no
// gate-denied content. For a batch, AuthorizeBatch runs runLocalGates for EVERY entry
// before this phase runs for ANY entry (the barrier), so a submission denied in phase
// one produces zero upstream bytes; a window/budget deny on a later entry can follow an
// earlier entry's sizing (same semantics as the pre-split baseline).
func (d *Decider) runSizingAndBudget(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity, sess *proxySession) (gateResult, bool) {
	if !id.ok { // defense in depth; runLocalGates already refused an unsealed snapshot
		return gateDeny(gateCodeIdentityUnverified, sdk.FailureDelegationInvalid, http.StatusServiceUnavailable, "api_error", "unverified identity snapshot (deny-closed)"), false
	}
	principal, tenant, pol := id.principal, id.tenant, id.pol
	actor, sessionRef := id.actor, id.sessionRef

	// 5g. Per-surface context window. The pre-flight count_tokens sizes the GOVERNED
	//     request (post egress/ceilings rewrites — the object that will be frozen); if it
	//     EXCEEDS the surface's effective window the call would be truncated/rejected
	//     upstream, so deny early with 400. A sizing failure (count_tokens errored) does NOT
	//     block — it is a capability pre-flight, not a security gate. MaxContextTokens
	//     consumes the ctxPol resolved in phase one (no second plane read).
	if pol.GateContextWindow {
		// The count body carries the MCP declaration, so it is provider egress too: the same
		// accepted binding is required before it, and a typed MCP refusal from CountTokens is a
		// deny — it must never fall into the non-blocking sizing-failure branch below.
		if res, ok := checkMCPBinding(sess, req); !ok {
			return res, false
		}
		tc, cerr := d.Inference.CountTokens(ctx, req)
		if code, isMCP := mcpRefusalCode(cerr); isMCP {
			return mcpGateDeny(code), false
		}
		if cerr == nil {
			verdict := claudeapi.CheckContextWindowForSurface(d.Surface, req.Model, tc.InputTokens)
			if verdict.Exceeds {
				return gateDeny(gateCodeContextWindow, sdk.FailurePolicyDeny, http.StatusBadRequest, "invalid_request_error", "request context exceeds this surface's window for the model"), false
			}
			if sess.ctxPol.MaxContextTokens > 0 && int64(tc.InputTokens) > sess.ctxPol.MaxContextTokens {
				d.emitContextCeilingFinding(ctx, tenant, req.Model, sess.ctxPol)
				res := gateDeny(gateCodeContextCeiling, sdk.FailurePolicyDeny, http.StatusRequestEntityTooLarge, "invalid_request_error", "request context exceeds the effective policy/group window")
				res.decision.Headers = noRetryHeader()
				return res, false
			}
		}
	}

	// 6. Budget admission: the call's estimate is HELD before the forward. It
	//    runs AFTER every security gate, the MCP egress gate (5b) included, and after the
	//    sizing pre-flight, so a request any of them refuses takes no hold. A firm budget or
	//    seat cap denies (block ⇒ 402, throttle ⇒ 429), money-free; a ledger that cannot be
	//    written denies too (fail-closed, 503). The hold rides on the session: Finalize
	//    settles it, and a later step that refuses the call gives it back.
	if pol.GateBudget {
		dims := d.spendDims(req, sessionRef)
		dims.UserGroupRefs = principal.GroupsIn(tenant)
		// The snapshot's sessionRef IS principal.AgentIdentity (newResolvedIdentity keeps
		// them in lockstep) — consumed via the seam so every gate sees the SAME binding.
		dims.AgentRef = sessionRef
		res, err := d.Budget.Reserve(ctx, tenant, finops.AdmissionRequest{
			Scope:            finops.AdmissionScopeModelGateway,
			Dims:             dims,
			ActorRef:         actor,
			Groups:           principal.GroupsIn(tenant),
			EstimateMicroUSD: proxyAdmissionEstimate(req),
			IdempotencyKey:   proxyAdmissionKey(),
			Unreachable:      engineReserveUnreachable,
		})
		if err != nil || !res.Allowed {
			return budgetDeny(res, err), false
		}
		sess.admissionHandle = res.Handle
	}
	return gateResult{}, true
}

// budgetDeny maps an admission that did not admit the call to the proxy's refusal. A
// ledger the admission could not establish, or a key it refused as corrupt, is a 503 and a
// reservation fault: no hold could be taken, and no policy refused the call. A per-seat
// spend limit and a pooled budget are told apart, as the apps-gateway contract publishes
// them, and a throttling budget is a 429. Every refusal is money-free and tells the client
// not to retry.
func budgetDeny(res finops.Reservation, err error) gateResult {
	var out gateResult
	switch {
	case err != nil || res.Reason == finops.ReasonStoreUnreachable:
		out = gateDeny(gateCodeBudget, sdk.FailureReservationFault, http.StatusServiceUnavailable, "api_error", finops.ReasonStoreUnreachable)
	case res.Reason == finops.ReasonAdmissionIntegrity:
		out = gateDeny(gateCodeBudget, sdk.FailureReservationFault, http.StatusServiceUnavailable, "api_error", finops.ReasonAdmissionIntegrity)
	case res.SpendLimit:
		out = gateDeny(gateCodeSpendLimit, sdk.FailurePolicyDeny, http.StatusPaymentRequired, "billing_error", "spend limit reached")
	case strings.EqualFold(res.Action, "throttle"):
		out = gateDeny(gateCodeBudgetThrottle, sdk.FailurePolicyDeny, http.StatusTooManyRequests, "rate_limit_error", "budget throttle in effect")
	default:
		out = gateDeny(gateCodeBudget, sdk.FailurePolicyDeny, http.StatusPaymentRequired, "billing_error", "budget limit reached")
	}
	out.decision.Headers = noRetryHeader()
	return out
}

// Bounds of the amount one proxied call holds.
const (
	// proxyEstimateFloorMicroUSD is held by every call, whatever it asks for.
	proxyEstimateFloorMicroUSD int64 = 10_000
	// proxyEstimatePerOutputTokenMicroUSD is held for each output token the call may produce.
	proxyEstimatePerOutputTokenMicroUSD int64 = 10
	// proxyEstimateMaxOutputTokens caps the tokens counted, so an absurd max_tokens holds a
	// large amount instead of one that wraps.
	proxyEstimateMaxOutputTokens int64 = 1 << 40
)

// proxyAdmissionEstimate is the amount a call holds while it runs: a floor, plus a price for
// each output token its max_tokens allows. Finalize replaces it with the measured cost.
func proxyAdmissionEstimate(req claudeapi.MessageRequest) int64 {
	n := int64(req.MaxTokens)
	if n <= 0 {
		return proxyEstimateFloorMicroUSD
	}
	if n > proxyEstimateMaxOutputTokens {
		n = proxyEstimateMaxOutputTokens
	}
	return proxyEstimateFloorMicroUSD + n*proxyEstimatePerOutputTokenMicroUSD
}

// proxyAdmissionKey is a fresh admission key for one call. The client sends no idempotency
// key, and the proxy cannot tell a retry from a second call with the same bytes, so it
// claims neither: every call asks the ledger its own question. A key derived from the
// request would present two identical calls as one, and the second would be answered with
// the first one's hold. The admission row's payload hash still binds the key to the
// request it admitted.
func proxyAdmissionKey() string {
	return finops.AdmissionScopeModelGateway + "/" + NewRequestRef()
}

// gateDeny builds a semantic deny: the legacy transport presentation plus the stable
// per-gate code and sdk failure class the PDP mapping consumes.
func gateDeny(code gateCode, class sdk.FailureClass, status int, errType, reason string) gateResult {
	return gateResult{decision: denyProxy(status, errType, reason), code: code, class: class}
}

// chainDeny is the deny return for runGates: a nil session, an empty governed request, the
// semantic deny result, and ok=false. It keeps the gate chain's deny sites a one-liner
// while the chain's 4-value signature feeds both Authorize and AuthorizeBatch.
func chainDeny(code gateCode, class sdk.FailureClass, status int, errType, reason string) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
	return nil, claudeapi.MessageRequest{}, gateDeny(code, class, status, errType, reason), false
}

func chainDenyWithHeaders(code gateCode, class sdk.FailureClass, status int, errType, reason string, headers map[string]string) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
	res := gateDeny(code, class, status, errType, reason)
	res.decision.Headers = headers
	return nil, claudeapi.MessageRequest{}, res, false
}

// Finalize runs the POST-forward steps: response DLP (block only in buffer mode), the
// post-hoc residency proof (detective), cost reconciliation (fail-open), the settlement of
// the call's admission hold (committed at the measured cost, or released on an upstream
// error) and the ledger outcome anchor (best-effort + loud, or already-mandated).
func (d *Decider) Finalize(ctx context.Context, sessAny any, out claudeapi.ProxyForwardResult) claudeapi.ProxyResponseVerdict {
	sess, _ := sessAny.(*proxySession)
	if sess == nil {
		return claudeapi.ProxyResponseVerdict{}
	}
	block := false
	reason := ""

	// Collect the response content ONCE (every channel — text, thinking, tool_use,
	// web_search, …). Response DLP and the content firewall both read it.
	var respContent claudeapi.CollectedContent
	respDLPOn := !out.UpstreamErr && sess.pol.GateDLPResponse && sess.pol.ResponseDLPMode != inferenceproxy.ResponseDLPOff && sess.pol.DLPEnabled()
	if respDLPOn || (d.Inspector != nil && !out.UpstreamErr) {
		respContent = claudeapi.CollectResponseContent(out.Response)
	}

	// Response DLP. Detective in flag mode (it cannot un-send a streamed response);
	// preventive in buffer mode (the connector withholds the buffered body on Block). It now
	// reads EVERY extractable channel and the unscanned signal (capability-gaps #9), not just
	// b.Text.
	if respDLPOn {
		denied := sess.pol.DLPDecide(ClassifyText(respContent.Texts))
		if respContent.Unscanned && sess.pol.DLPUnscannedDenied() {
			denied = append(denied, DLPUnscannedClass)
		}
		if len(denied) > 0 {
			d.emitDLPFinding(ctx, sess.tenant, "output", sess.modelRef, denied)
			if sess.pol.ResponseDLPMode == inferenceproxy.ResponseDLPBuffer {
				block = true
				reason = "response withheld by data-loss-prevention policy"
			}
		}
	}

	// Content firewall (commercial add-on, OPT-IN): deep inspection of the model's response —
	// exfiltration in the output, unsafe agentic actions in its tool calls. A Block is honored
	// wherever the connector holds the full body (non-streaming always; streaming only in
	// buffer mode — a streamed response cannot be un-sent, so it is detective otherwise).
	if d.Inspector != nil && !out.UpstreamErr {
		inDec := d.runContentInspector(ctx, sess.tenant, sess.actor, claudeapi.InspectDirectionResponse, sess.modelRef, respContent, sess.sessionRef, sess.unbindableAgent)
		if inDec.Block {
			block = true
			reason = firstNonEmpty(inDec.Reason, "response withheld by content firewall policy")
		}
	}

	// Computer-use action audit: scan the response for computer-use actions proposed
	// by the model (click, type, screenshot, scroll, key). Each action is audited; typed text
	// runs through the DLP classifier. Sensitive typed text emits a HIGH finding and, in
	// buffer mode, blocks the response. This is detective: the model proposed the action but
	// the client has not executed it yet.
	if !out.UpstreamErr {
		cuBlock := d.auditComputerUseActions(ctx, sess.tenant, sess.actor, sess.modelRef, out.Response)
		if cuBlock {
			block = true
			reason = "response withheld: computer-use typed text contains sensitive content"
		}
	}

	// Residency proof: the response usage carries the geo the request ACTUALLY ran
	// in (ANT2-17). For a pinned tenant a crossing is a finding — visible and routed, the
	// same posture as the compliance residency scan (it does not retroactively un-send).
	if sess.pin != "" && out.Response.Usage.InferenceGeo != "" && !residency.InferenceGeoCompatible(sess.pin, out.Response.Usage.InferenceGeo) {
		d.emitResidencyFinding(ctx, sess.tenant, sess.modelRef, out.Response.Usage.InferenceGeo)
	}

	// Cost reconciliation (fail-open): reuse the connector's billing logic (refusal not
	// billed, per-attempt fallback, advisor split) and publish the per-request CostSample +
	// forensic findings on the bus, so the NEXT request's budget admission is tight.
	actual := d.reconcileCost(ctx, sess, out)

	// Settle the call's hold, once its cost is published: committed at the measured cost,
	// or released when the upstream call failed, since no cost of it is ingested.
	if out.UpstreamErr {
		d.releaseAdmission(ctx, sess)
	} else {
		d.commitHold(ctx, sess.tenant, sess.admissionHandle, actual)
	}

	// Ledger outcome anchor (the I/O fingerprint record).
	decision := "allow"
	switch {
	case block:
		decision = "blocked-response"
	case out.UpstreamErr:
		decision = "upstream-error"
	}
	d.anchorOutcome(ctx, sess, out, decision)

	if block {
		return claudeapi.ProxyResponseVerdict{Block: true, Status: http.StatusForbidden, ErrorType: "permission_error", Reason: reason}
	}
	return claudeapi.ProxyResponseVerdict{}
}

// ResolveTenant derives the request tenant: a configured single tenant (the principal must
// be a member, or superadmin), else the principal's sole grant. Ambiguous ⇒ not resolved
// (deny-closed at the caller). It is exported for the composition root's cross-PEP tests.
func (d *Decider) ResolveTenant(p auth.Principal) (model.TenantID, bool) {
	if p.IsPurposeRestricted() {
		return model.TenantID(""), false
	}
	if !d.TenantHint.IsZero() {
		if p.Superadmin || p.IsMember(d.TenantHint) {
			return d.TenantHint, true
		}
		return model.TenantID(""), false
	}
	if ts := p.Tenants(); len(ts) == 1 {
		return ts[0], true
	}
	return model.TenantID(""), false
}

// orgPin reads the tenant's residency pin (orgs.data_region) in a read transaction.
func (d *Decider) orgPin(ctx context.Context, tenant model.TenantID) (string, error) {
	var pin string
	err := d.Store.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		pin = strings.TrimSpace(org.DataRegion)
		return nil
	})
	return pin, err
}

// spendDims builds the FinOps dims for the budget check. IdentityRef is left empty so
// FinOps resolves a firm identity only when an identity-scoped budget needs it.
func (d *Decider) spendDims(req claudeapi.MessageRequest, sessionRef string) finops.SpendDims {
	return finops.SpendDims{
		ProviderRef:  "anthropic",
		ModelRef:     req.Model,
		Gateway:      string(d.Surface),
		SessionRef:   sessionRef,
		ServiceTier:  req.ServiceTier,
		InferenceGeo: d.SurfaceGeo,
	}
}

// reconcileCost publishes the per-request cost sample(s) + forensic finding(s) on the bus
// and returns the call's measured cost: the sum of its samples.
func (d *Decider) reconcileCost(ctx context.Context, sess *proxySession, out claudeapi.ProxyForwardResult) int64 {
	if d.Inference == nil || out.UpstreamErr {
		return 0
	}
	samples, findings := d.Inference.RuntimeObservations(out.Response, sess.sessionRef, d.Clock(), false)
	var actual int64
	for _, s := range samples {
		if s.Actor == "" {
			s.Actor = sess.actor
		}
		actual += s.CostMicroUSD
		d.publish(ctx, sess.tenant, s)
	}
	for _, f := range findings {
		d.publish(ctx, sess.tenant, f)
	}
	return actual
}

// releaseAdmission gives back the call's hold: the call never ran, or its upstream call
// failed and no cost of it is ingested.
func (d *Decider) releaseAdmission(ctx context.Context, sess *proxySession) {
	d.releaseHold(ctx, sess.tenant, sess.admissionHandle)
}

// releaseAdmissions gives back the hold of each session.
func (d *Decider) releaseAdmissions(ctx context.Context, sessions []*proxySession) {
	for _, sess := range sessions {
		d.releaseAdmission(ctx, sess)
	}
}

// commitHold records that the effect of hold h ran, at actual. The call has already run,
// so a failure has nothing left to refuse: it is logged, and the hold lapses at its
// expiry. It runs even when the caller's context is done: the settlement is bookkeeping
// of what already happened, and a client that disconnects does not undo it.
func (d *Decider) commitHold(ctx context.Context, tenant model.TenantID, h string, actual int64) {
	if d.Budget == nil || h == "" {
		return
	}
	if err := d.Budget.Commit(context.WithoutCancel(ctx), tenant, h, actual); err != nil && d.Log != nil {
		d.Log.Warn("inference-proxy: admission commit failed; the hold lapses at its expiry", "err", err)
	}
}

// releaseHold returns the headroom of hold h. A failure is logged, and the hold lapses at
// its expiry. Like commitHold, it runs even when the caller's context is done.
func (d *Decider) releaseHold(ctx context.Context, tenant model.TenantID, h string) {
	if d.Budget == nil || h == "" {
		return
	}
	if err := d.Budget.Release(context.WithoutCancel(ctx), tenant, h); err != nil && d.Log != nil {
		d.Log.Warn("inference-proxy: admission release failed; the hold lapses at its expiry", "err", err)
	}
}

func (d *Decider) publish(ctx context.Context, tenant model.TenantID, obs sdkmodel.Observation) {
	if d.Bus == nil {
		return
	}
	if err := d.Bus.Publish(ctx, event.FromObservation(tenant.String(), proxySignalSource, obs)); err != nil && d.Log != nil {
		d.Log.Warn("inference-proxy: bus publish failed (best-effort)", "err", err)
	}
}

func (d *Decider) emitDLPFinding(ctx context.Context, tenant model.TenantID, surface, modelRef string, classes []string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_dlp_blocked",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference " + surface + " blocked by DLP policy",
		DetailHash:  hexSHA(modelRef + "|" + surface + "|" + strings.Join(classes, ",")),
		OccurredAt:  d.Clock().UTC(),
		OWASPLLM:    []string{"LLM02:2025"},
	})
}

func (d *Decider) emitRequestCeilingFinding(ctx context.Context, tenant model.TenantID, modelRef string, violations []requestCeilingViolation, enforced bool) {
	labels := requestCeilingViolationLabels(violations)
	mode := "observe"
	if enforced {
		mode = "enforced"
	}
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_request_ceiling",
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference request exceeds the tenant per-request ceiling (" + mode + ")",
		DetailHash:  hexSHA(modelRef + "|" + mode + "|" + strings.Join(labels, ",")),
		OccurredAt:  d.Clock().UTC(),
		OWASPLLM:    []string{"LLM10:2025"},
	})
}

func (d *Decider) emitContextCeilingFinding(ctx context.Context, tenant model.TenantID, modelRef string, ctxPol knowledge.EffectivePolicy) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_context_ceiling",
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference request exceeds the effective context-policy/group window",
		DetailHash:  hexSHA(modelRef + "|" + ctxPol.WinningScope + "|" + strconv.FormatInt(ctxPol.MaxContextTokens, 10)),
		OccurredAt:  d.Clock().UTC(),
		OWASPLLM:    []string{"LLM10:2025"},
	})
}

// emitPlaneOutageFinding records that a security gate was bypassed because the tenant
// opted to fail OPEN on a decision-plane read outage (fail_open=true). High severity: a
// security control did not run, so the crossing must be loud and evidenced.
func (d *Decider) emitPlaneOutageFinding(ctx context.Context, tenant model.TenantID, gate string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_proxy_failed_open",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  gate,
		Title:       "Inference proxy failed OPEN on a " + gate + " decision-plane outage (per tenant fail_open)",
		DetailHash:  hexSHA("failed_open|" + gate),
		OccurredAt:  d.Clock().UTC(),
	})
}

func (d *Decider) emitResidencyFinding(ctx context.Context, tenant model.TenantID, modelRef, geo string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_residency_violation",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference ran outside the tenant's pinned region",
		DetailHash:  hexSHA(modelRef + "|geo=" + geo),
		OccurredAt:  d.Clock().UTC(),
	})
}

// anchorIntent records the AUTHORIZED decision to the ledger BEFORE the forward (the
// recording-mandating reservation). It carries no payload — the PayloadHash commits to
// the request reference, surface, model and actor; the body fingerprints arrive in the
// outcome leg (linked by request_ref).
func (d *Decider) anchorIntent(ctx context.Context, sess *proxySession) error {
	if d.Store == nil {
		return ErrNoLedger // mandating tenant without a ledger ⇒ deny-closed
	}
	// The evidence binds the effect: OperationID=request ref, EffectDigest=the F3
	// digest over the FROZEN forward bytes (sess.effectiveDigest). A receipt is anchored
	// only for THIS exact effect (sdk/evidence.go).
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}
	tip := proxyIntentHash(sess.requestRef, string(d.Surface), sess.tenant.String(), sess.modelRef, sess.actor, sess.inputDigest, sess.effectiveDigest)
	// The F9 anchoring discipline — append inside the txn, COMMIT a degrade drop's
	// loss accounting instead of rolling it back, classify only afterwards — now lives in
	// EvidenceWriter (inferenceevidence.go). This leg's action, target, payload
	// hash, metadata and evidence-or-refuse posture are unchanged.
	receipt, err := EvidenceWriter{Store: d.Store}.Append(ctx, sess.tenant, binding, model.AuditDraft{
		Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
		Action: "inference.proxy.authorized", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
		PayloadHash: tip,
		Meta:        proxyToolVisibilityMeta(map[string]any{"request_ref": sess.requestRef, "surface": string(d.Surface), "model": sess.modelRef, "decision": "allow", "input_digest": hex.EncodeToString(sess.inputDigest), "effective_digest": hex.EncodeToString(sess.effectiveDigest), "context_coverage": sess.contextCoverage}, sess.toolVisibility),
	})
	if err != nil {
		return err // evidence-or-refuse: a real ledger fault denies the privileged call
	}
	if receipt.MustRefuse(binding) {
		return EvidenceRefusedError{Fault: receipt.Fault}
	}
	return nil
}

// anchorOutcome records the I/O fingerprint of a completed call. Best-effort + LOUD: a
// failed anchor is logged as an evidence gap, never swallowed (the call already happened).
func (d *Decider) anchorOutcome(ctx context.Context, sess *proxySession, out claudeapi.ProxyForwardResult, decision string) {
	if d.Store == nil {
		if d.Log != nil {
			d.Log.Error("inference-proxy: no ledger store; outcome NOT anchored (evidence gap)", "request_ref", sess.requestRef)
		}
		return
	}
	// The forwarded octets (out.EffectiveSHA) MUST equal what the decider froze
	// (sess.effectiveDigest) — the proxy forwards the Prepared artifact. A mismatch is an
	// evidence anomaly (a forward path that diverged from the governed decision); log it loud.
	if len(out.EffectiveSHA) > 0 && len(sess.effectiveDigest) > 0 && !bytes.Equal(out.EffectiveSHA, sess.effectiveDigest) && d.Log != nil {
		d.Log.Error("inference-proxy: forwarded-bytes digest != governed effective digest (binding anomaly)", "request_ref", sess.requestRef)
	}
	tip := proxyOutcomeHash(sess.requestRef, string(d.Surface), sess.tenant.String(), sess.modelRef, decision, out.ReqBytes, out.RespBytes, out.ReqSHA, out.RespSHA, sess.inputDigest, sess.effectiveDigest)
	meta := map[string]any{
		"request_ref": sess.requestRef, "surface": string(d.Surface), "model": sess.modelRef,
		"decision": decision, "req_bytes": out.ReqBytes, "resp_bytes": out.RespBytes,
		"streamed": out.Streamed, "upstream_status": out.UpstreamStatus,
		"input_digest": hex.EncodeToString(sess.inputDigest), "effective_digest": hex.EncodeToString(sess.effectiveDigest),
		"context_coverage": sess.contextCoverage,
	}
	// The outcome leg anchors the SAME effect the intent leg bound, so it states that
	// binding rather than leaving it implicit; it is best-effort, so the receipt is read
	// only for the degrade-drop gap, never as an authorization.
	receipt, err := EvidenceWriter{Store: d.Store}.Append(ctx, sess.tenant, sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}, model.AuditDraft{
		Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
		Action: "inference.proxy.recorded", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
		PayloadHash: tip, Meta: proxyToolVisibilityMeta(meta, sess.toolVisibility),
	})
	if EvidenceAppendDropped(receipt, err) && d.Log != nil {
		d.Log.Error("inference-proxy: outcome evidence dropped by the degrade spool policy (evidence gap)", "request_ref", sess.requestRef)
	}
	if err != nil && d.Log != nil {
		d.Log.Error("inference-proxy: ledger outcome anchor failed (evidence gap)", "request_ref", sess.requestRef, "err", err)
	}
}

// anchorBatchIntent records the AUTHORIZED decision for a whole batch submission BEFORE the
// forward (the recording-mandating reservation), as ONE anchor for the submission (not per
// entry). The PayloadHash commits to the request reference, surface, the "batch" kind and
// the actor; the entry count + batch id arrive in the outcome leg (linked by request_ref).
func (d *Decider) anchorBatchIntent(ctx context.Context, sess *proxySession, entries int) error {
	if d.Store == nil {
		return ErrNoLedger // mandating tenant without a ledger ⇒ deny-closed
	}
	// Batch inputDigest is nil here: the inbound envelope SHA (out.ReqSHA, outcome leg) binds
	// the submission bytes; per-entry canonical digests live in each entry's decision. The
	// EffectDigest is the F3 digest over the FROZEN batch envelope (sess.effectiveDigest).
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}
	tip := proxyIntentHash(sess.requestRef, string(d.Surface), sess.tenant.String(), "batch", sess.actor, nil, sess.effectiveDigest)
	// Same shared mechanism as anchorIntent (inferenceevidence.go): it commits the
	// degrade-drop's loss accounting and refuses after, never rolling it back from inside
	// the transaction. This leg's action, target, hash, metadata and posture are unchanged.
	receipt, err := EvidenceWriter{Store: d.Store}.Append(ctx, sess.tenant, binding, model.AuditDraft{
		Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
		Action: "inference.proxy.batch.authorized", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
		PayloadHash: tip,
		Meta:        proxyToolVisibilityMeta(map[string]any{"request_ref": sess.requestRef, "surface": string(d.Surface), "kind": "batch", "entries": entries, "decision": "allow", "effective_digest": hex.EncodeToString(sess.effectiveDigest), "context_coverage": sess.contextCoverage}, sess.toolVisibility),
	})
	if err != nil {
		return err
	}
	if receipt.MustRefuse(binding) {
		return EvidenceRefusedError{Fault: receipt.Fault}
	}
	return nil
}

// anchorBatchOutcome records the outcome of a batch submission (best-effort + LOUD). The
// batch CREATE response is a receipt (id/status), not model output, so there is no response
// fingerprint — the anchor commits to the request fingerprint, the entry count and the
// created batch id.
func (d *Decider) anchorBatchOutcome(ctx context.Context, sess *proxySession, out claudeapi.ProxyBatchForwardResult, decision string) {
	if d.Store == nil {
		if d.Log != nil {
			d.Log.Error("inference-proxy: no ledger store; batch outcome NOT anchored (evidence gap)", "request_ref", sess.requestRef)
		}
		return
	}
	if len(out.EffectiveSHA) > 0 && len(sess.effectiveDigest) > 0 && !bytes.Equal(out.EffectiveSHA, sess.effectiveDigest) && d.Log != nil {
		d.Log.Error("inference-proxy: forwarded batch digest != governed effective digest (binding anomaly)", "request_ref", sess.requestRef)
	}
	// Batch inputDigest is nil in the hash: the inbound envelope SHA (out.ReqSHA) already binds
	// the submission bytes; the per-entry canonical digests live in each entry's decision.
	tip := proxyOutcomeHash(sess.requestRef, string(d.Surface), sess.tenant.String(), "batch", decision, out.ReqBytes, 0, out.ReqSHA, nil, nil, sess.effectiveDigest)
	meta := map[string]any{
		"request_ref": sess.requestRef, "surface": string(d.Surface), "kind": "batch",
		"batch_id": out.Batch.ID, "entries": out.Entries, "decision": decision,
		"req_bytes": out.ReqBytes, "upstream_status": out.UpstreamStatus,
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
		"context_coverage": sess.contextCoverage,
	}
	// Same shared mechanism and the same binding the batch intent leg bound; best-effort,
	// so the receipt is read only for the degrade-drop gap.
	receipt, err := EvidenceWriter{Store: d.Store}.Append(ctx, sess.tenant, sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}, model.AuditDraft{
		Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
		Action: "inference.proxy.batch.recorded", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
		PayloadHash: tip, Meta: proxyToolVisibilityMeta(meta, sess.toolVisibility),
	})
	if EvidenceAppendDropped(receipt, err) && d.Log != nil {
		d.Log.Error("inference-proxy: batch outcome evidence dropped by the degrade spool policy (evidence gap)", "request_ref", sess.requestRef)
	}
	if err != nil && d.Log != nil {
		d.Log.Error("inference-proxy: batch ledger outcome anchor failed (evidence gap)", "request_ref", sess.requestRef, "err", err)
	}
}

// --- pure helpers -------------------------------------------------------------------

func denyProxy(status int, errType, reason string) claudeapi.ProxyDecision {
	return claudeapi.ProxyDecision{Allow: false, Status: status, ErrorType: errType, Reason: reason}
}

func noRetryHeader() map[string]string {
	return map[string]string{"x-should-retry": "false"}
}

// batchEntryDenyReason names the denied entry (custom_id, else index) without leaking the
// prompt — so a per-entry deny is actionable but minimal-data (docs/SECURITY-HARDENING.md).
func batchEntryDenyReason(i int, customID, reason string) string {
	id := strings.TrimSpace(customID)
	if id == "" {
		id = "#" + strconv.Itoa(i)
	}
	base := "batch entry " + id + " denied"
	if strings.TrimSpace(reason) != "" {
		return base + ": " + reason
	}
	return base + " by Olivares governance policy"
}

const (
	requestCeilingMaxTokens  = "max_tokens"
	requestCeilingTaskBudget = "task_budget"
	requestCeilingToolUses   = "tool_max_uses"
)

type requestCeilingViolation struct {
	Kind     string
	ToolType string
}

func (v requestCeilingViolation) label() string {
	if v.ToolType == "" {
		return v.Kind
	}
	return v.Kind + ":" + v.ToolType
}

func requestCeilingViolations(req claudeapi.MessageRequest, ceilings inferenceproxy.RequestCeilings) []requestCeilingViolation {
	var out []requestCeilingViolation
	if ceilings.MaxTokens > 0 && int64(req.MaxTokens) > ceilings.MaxTokens {
		out = append(out, requestCeilingViolation{Kind: requestCeilingMaxTokens})
	}
	if ceilings.TaskBudgetTokens > 0 &&
		req.OutputConfig != nil && req.OutputConfig.TaskBudget != nil &&
		int64(req.OutputConfig.TaskBudget.Total) > ceilings.TaskBudgetTokens {
		out = append(out, requestCeilingViolation{Kind: requestCeilingTaskBudget})
	}
	if ceilings.MaxToolUses > 0 {
		for _, tool := range req.Tools {
			m, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			if !numericGreaterThan(m["max_uses"], ceilings.MaxToolUses) {
				continue
			}
			out = append(out, requestCeilingViolation{Kind: requestCeilingToolUses, ToolType: toolType(m)})
		}
	}
	return out
}

func requestCeilingViolationLabels(violations []requestCeilingViolation) []string {
	labels := make([]string, 0, len(violations))
	for _, v := range violations {
		labels = append(labels, v.label())
	}
	sort.Strings(labels)
	return labels
}

func hasHardCeilingViolation(violations []requestCeilingViolation) bool {
	for _, v := range violations {
		if v.Kind == requestCeilingMaxTokens || v.Kind == requestCeilingTaskBudget {
			return true
		}
	}
	return false
}

func enforceRequestCeilings(req claudeapi.MessageRequest, ceilings inferenceproxy.RequestCeilings) claudeapi.MessageRequest {
	if ceilings.MaxToolUses > 0 {
		for _, tool := range req.Tools {
			m, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			if numericGreaterThan(m["max_uses"], ceilings.MaxToolUses) {
				m["max_uses"] = int(ceilings.MaxToolUses)
				continue
			}
			if _, exists := m["max_uses"]; !exists && carriesMaxUses(toolType(m)) {
				m["max_uses"] = int(ceilings.MaxToolUses)
			}
		}
	}
	if ceilings.TaskBudgetTokens >= 20000 &&
		(req.OutputConfig == nil || req.OutputConfig.TaskBudget == nil) {
		if req.OutputConfig == nil {
			req.OutputConfig = &claudeapi.OutputConfig{}
		}
		req.OutputConfig.TaskBudget = &claudeapi.TaskBudget{
			Type:  "tokens",
			Total: int(ceilings.TaskBudgetTokens),
		}
	}
	return req
}

func carriesMaxUses(toolType string) bool {
	return strings.HasPrefix(toolType, "web_search") || strings.HasPrefix(toolType, "web_fetch")
}

func toolType(m map[string]any) string {
	s, _ := m["type"].(string)
	return s
}

func numericGreaterThan(v any, ceiling int64) bool {
	switch n := v.(type) {
	case int:
		return int64(n) > ceiling
	case int8:
		return int64(n) > ceiling
	case int16:
		return int64(n) > ceiling
	case int32:
		return int64(n) > ceiling
	case int64:
		return n > ceiling
	case uint:
		return uint64(n) > uint64(ceiling)
	case uint8:
		return uint64(n) > uint64(ceiling)
	case uint16:
		return uint64(n) > uint64(ceiling)
	case uint32:
		return uint64(n) > uint64(ceiling)
	case uint64:
		return n > uint64(ceiling)
	case float32:
		return float64(n) > float64(ceiling)
	case float64:
		return n > float64(ceiling)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i > ceiling
		}
		f, err := n.Float64()
		return err == nil && f > float64(ceiling)
	default:
		return false
	}
}

// NOTE: the request/response text collection that used to live here (reading only b.Text)
// is superseded by claudeapi.CollectRequestContent / CollectResponseContent, which walk
// EVERY content channel (document/image/file_id/tool_result/web_search_tool_result/…) and
// flag the opaque ones as unscanned — closing the capability-gaps #9 DLP bypass. The decider
// consumes CollectedContent.Texts (the classifier input) and CollectedContent.Unscanned (the
// deny-closed signal) directly; the old typed-text-only helpers are gone.

// ClassifyText runs the deterministic sensitivity classifier over each text and
// returns the distinct classes present (never a matched value — the classifier never
// returns one).
func ClassifyText(texts []string) []string {
	seen := map[string]bool{}
	var classes []string
	for _, t := range texts {
		for _, h := range security.ClassifySensitivity(t) {
			if !seen[h.Class] {
				seen[h.Class] = true
				classes = append(classes, h.Class)
			}
		}
	}
	return classes
}

// NewRequestRef mints an opaque per-request reference (never a subject/prompt-derived
// value — it lands in the WORM ledger).
func NewRequestRef() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hexSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ponytail: firstNonEmpty, hexSHA, writeLenPrefixed, writeInt64, sessionHookTokenPrefix and
// engineReserveUnreachable are copies of the composition root's (voicedispatch.go,
// inferenceproxyserver.go, sessiongov.go, sessionhookcredentials.go, budgetgate.go); move
// them to one shared package when the next PEP leaves cmd/olivares (B5.3/B5.4).

// sessionHookTokenPrefix marks a launch-issued session credential (core/auth).
const sessionHookTokenPrefix = "olvsess_"

// engineReserveUnreachable is the unreachable-ledger posture every in-process gate passes to
// finops Reserve: deny without exception (budgetgate.go).
const engineReserveUnreachable = finops.UnreachableDeny

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// writeLenPrefixed and writeInt64 are the injective length-prefixed encoding of the
// ledger anchor hashes.
func writeLenPrefixed(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

func writeInt64(h hash.Hash, v int64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	_, _ = h.Write(n[:])
}

// proxyIntentHash binds the AUTHORIZED decision to {tenant, surface, model, actor} and — the
// F3 addition — BOTH content digests: inputDigest (canonical inbound) and effDigest (the
// frozen bytes that will run). Binding both means two distinct inputs that normalize to the
// same effective artifact still produce distinct pre-forward evidence, so a crash after the
// forward and before the outcome leaves an intent anchor that proves which input was decided.
func proxyIntentHash(requestRef, surface, tenant, modelRef, actor string, inputDigest, effDigest []byte) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(proxyIntentDomain))
	writeLenPrefixed(h, []byte(requestRef))
	writeLenPrefixed(h, []byte(surface))
	writeLenPrefixed(h, []byte(tenant))
	writeLenPrefixed(h, []byte(modelRef))
	writeLenPrefixed(h, []byte(actor))
	writeLenPrefixed(h, inputDigest)
	writeLenPrefixed(h, effDigest)
	return h.Sum(nil)
}

// proxyOutcomeHash binds the completed call. The F3 addition is the pair of content
// digests — inputDigest (canonical inbound) and effDigest (the frozen forwarded bytes) —
// alongside tenant, so an auditor can prove the governed decision, the forwarded octets and
// the inbound request all cohere.
func proxyOutcomeHash(requestRef, surface, tenant, modelRef, decision string, reqLen, respLen int64, reqSHA, respSHA, inputDigest, effDigest []byte) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(proxyOutcomeDomain))
	writeLenPrefixed(h, []byte(requestRef))
	writeLenPrefixed(h, []byte(surface))
	writeLenPrefixed(h, []byte(tenant))
	writeLenPrefixed(h, []byte(modelRef))
	writeLenPrefixed(h, []byte(decision))
	writeInt64(h, reqLen)
	writeInt64(h, respLen)
	writeLenPrefixed(h, reqSHA)
	writeLenPrefixed(h, respSHA)
	writeLenPrefixed(h, inputDigest)
	writeLenPrefixed(h, effDigest)
	return h.Sum(nil)
}

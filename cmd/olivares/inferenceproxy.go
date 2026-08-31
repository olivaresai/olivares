// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// inferenceproxy.go wires the OPTIONAL, OPT-IN inline inference PEP in the
// composition root: it binds the connector's identity-blind protocol shell
// (connectors/claude-api.MessagesProxy) to the GOVERNED decision the connector may not
// import — the firm identity (via the authenticated bearer), the kill-switch,
// the residency guard, the model-access gate, the per-surface context
// window, the DLP classifier + this tenant's egress policy, the budget,
// and the tamper-evident ledger. It is the sibling of claudehookpep.go and
// mcpgateway.go — same split: the connector owns the protocol + deny-closed defaults,
// this file owns the decision and the upstream credential (NEVER the inbound one).
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

// defaultInferenceProxyListen is the loopback-default bind (secure default).
const defaultInferenceProxyListen = "127.0.0.1:8448"

// proxySignalSource labels the bus events (cost samples, findings) this PEP emits.
const proxySignalSource = "inference_proxy"

// awsExternalAnthropicService is the SigV4 service name of the Claude-Platform-on-AWS
// inference surface (surfaces.go).
const awsExternalAnthropicService = "aws-external-anthropic"

// Domain separators for the per-request ledger anchor hashes (length-prefixed
// injective encoding via writeLenPrefixed/writeInt64 in sessiongov.go).
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

// errEvidenceRefused is the deny-closed error when the ledger transaction COMMITTED but
// produced no per-operation anchor — the F9 case where a DEGRADE-mode audit spool
// durably counts the drop (loss accounting) yet writes no ledger event, so the receipt
// MustRefuse. It is distinct from ErrNoLedger (no store wired) and from a raw store fault
// (which rolls the transaction back): here the loss accounting is already durable and the
// call is denied evidence-or-refuse. Carries the classified fault for honest logging.
type errEvidenceRefused struct{ fault sdk.EvidenceFault }

// defaultMandatoryYieldsTo decides the ONE case where mandatory recording steps aside, and the
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
func defaultMandatoryYieldsTo(pol inferenceproxy.ProxyPolicy, err error) bool {
	if pol.RecordMandatoryChosen {
		return false
	}
	var refused errEvidenceRefused
	if !errors.As(err, &refused) {
		return false
	}
	return refused.fault == sdk.EvidenceFaultSpoolDegraded
}

func (e errEvidenceRefused) Error() string {
	return "inference-proxy: mandatory recording refused (evidence " + string(e.fault) + ")"
}

// modelAccessGate is the in-band model-access decision the proxy consumes per
// request. *models.Module satisfies it.
type modelAccessGate interface {
	EvaluateModelAccess(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef, providerRef, modelRef, surface string) (models.ModelAccessVerdict, error)
}

// proxyPolicySource is the per-tenant proxy config + DLP policy the decider reads.
// *inferenceproxy.Module satisfies it.
type proxyPolicySource interface {
	Policy(ctx context.Context, tenant model.TenantID) (inferenceproxy.ProxyPolicy, error)
}

// contextPolicyResolver is the effective context-policy decision consumed by the
// inline inference proxy. *knowledge.Module satisfies it.
type contextPolicyResolver interface {
	Apply(ctx context.Context, tenant model.TenantID, q knowledge.ContextPolicyQuery) (knowledge.EffectivePolicy, error)
}

// observationSink publishes a cost/finding observation on the bus (best-effort).
// eventbus.Bus satisfies it; nil disables emission.
type observationSink interface {
	Publish(ctx context.Context, e event.Event) error
}

var (
	_ modelAccessGate       = (*models.Module)(nil)
	_ proxyPolicySource     = (*inferenceproxy.Module)(nil)
	_ contextPolicyResolver = (*knowledge.Module)(nil)
	_ budgetChecker         = (*finops.Module)(nil)
)

// inferenceProxyConfig is the operator provisioning for the inline proxy (out of the
// store, secret-bearing — same pattern as OLIVARES_HOOK_PEP_CONFIG). One instance fronts
// ONE surface; the upstream credential lives here and is NEVER the inbound caller's.
type inferenceProxyConfig struct {
	Listen              string `json:"listen"`
	Surface             string `json:"surface"`               // direct | claude-platform-aws (v1)
	BaseURL             string `json:"base_url"`              // override; default per surface (with {region})
	Region              string `json:"region"`                // {region} substitution + SigV4 region (AWS)
	InferenceGeo        string `json:"inference_geo"`         // the surface's declared inference geo ("" = per-request)
	AnthropicVersion    string `json:"anthropic_version"`     // anthropic-version header ("" = client default)
	Tenant              string `json:"tenant"`                // optional fixed tenant ("" = infer from credential)
	PublicURL           string `json:"public_url"`            // externally visible origin for apps-gateway OAuth discovery
	ManagedSettingsPath string `json:"managed_settings_path"` // single-document managed settings JSON
	// UpstreamKey is the OPERATOR's Anthropic inference (workspace) API key for the direct
	// surface. SECRET — held in memory, never logged. NEVER the inbound caller's credential.
	UpstreamKey string `json:"upstream_key"`
	// AWS SigV4 credentials for the claude-platform-aws surface (SECRET).
	AWSAccessKeyID  string `json:"aws_access_key_id"`
	AWSSecretKey    string `json:"aws_secret_access_key"`
	AWSSessionToken string `json:"aws_session_token"`
}

// loadInferenceProxyConfig reads the optional OLIVARES_INFERENCE_PROXY_CONFIG JSON. A
// missing path yields an empty config (nothing mounted); a supplied path must be readable
// and contain valid JSON or startup fails closed.
func loadInferenceProxyConfig(_ *slog.Logger) (inferenceProxyConfig, error) {
	path := os.Getenv("OLIVARES_INFERENCE_PROXY_CONFIG")
	if path == "" {
		return inferenceProxyConfig{}, nil
	}
	var cfg inferenceProxyConfig
	if err := loadOperatorJSONConfig("OLIVARES_INFERENCE_PROXY_CONFIG", path, &cfg); err != nil {
		return inferenceProxyConfig{}, err
	}
	return cfg, nil
}

// inferenceProxyDecider is the GOVERNED brain: authenticate → kill-switch → residency →
// model-access → context-policy → DLP → egress → firewall → computer-use → ceilings →
// count_tokens sizing → budget → record. Every security edge is deny-closed; only the
// budget gate is fail-open (the posture). The sizing pre-flight is the ONLY
// pre-forward upstream egress and deliberately runs AFTER every phase-one gate:
// a prompt denied by the local content/security gates is never exfiltrated through the
// token-count side channel. Denies that decide on or after the sizing itself —
// window/413, budget/spend (local FinOps reads), and the record_mandatory evidence deny
// in Authorize — necessarily follow that POST; the contract documents the exact
// guarantee and its residuals. It
// holds the upstream Inference client (operator-credentialed) so it can pre-flight
// count_tokens and so Finalize can reconcile cost via the connector's billing logic.
type inferenceProxyDecider struct {
	surface    sdkmodel.Gateway
	surfaceGeo string         // operator-declared inference geo of this surface ("" = per-request)
	tenantHint model.TenantID // configured single tenant ("" = infer from the credential)
	// defaultModel is the operator-pinned model applied when a request names none, during the
	// F3 pre-governance normalization. The mounted proxy pins none today (""), so a
	// model-less request is a 400 (the effective model must be explicit to be governed and
	// frozen) — never a silent forward to a hidden default.
	defaultModel string

	inf   *claudeapi.Inference
	authr principalAuthenticator
	// delegation verifies a PEP-presented DelegationProof and claims a single-use
	// decision (the PDP-service adapter, resolveDelegatedIdentity). It is the
	// SUBJECT-authority source for a PEP-fronted call, distinct from authr (which
	// authenticates the INBOUND transport credential for the bearer path).
	// *auth.Authenticator satisfies it; nil ⇒ no PDP-service path mounted.
	delegation    delegationVerifier
	models        modelAccessGate
	budget        budgetChecker
	contextPolicy contextPolicyResolver
	killSwitch    killSwitchGuard
	policy        proxyPolicySource
	residency     *residency.Registry
	store         store.Store
	bus           observationSink
	// egress is the OPTIONAL commercial server-tool egress gate (P0 #1; servertoolegressgate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ observe-only, behavior UNCHANGED
	// by design. This enterprise gate governs WHICH tools/domains are permitted, grant-based;
	// the AGPL request-ceiling gate below is only a numeric FinOps consumption cap. When
	// present it runs AFTER the security gates and BEFORE budget, and may only DENY or
	// REWRITE req.Tools — never force an Allow nor bypass a gate ahead of it.
	egress serverToolEgressGate
	// approvals opens a governed approval for a denied egress (2026-06-19, D4); nil ⇒
	// deny + finding only. It NEVER resumes the synchronous proxy call. It is reused by the
	// content firewall for a held (hitl) detection.
	approvals *approvalBridge
	// inspector is the OPTIONAL commercial content firewall (P1; contentinspectorgate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ no deep inspection, behavior
	// UNCHANGED. When present it runs AFTER the deny-closed security gates and BEFORE the
	// fail-open budget gate on a request, and after response DLP in Finalize; it may DENY a
	// request or WITHHOLD a buffered response, never force an Allow nor bypass a prior gate.
	inspector contentInspector
	// computerUse is the OPTIONAL computer-use governance gate (computerusegate.go).
	// nil in the default AGPL build (wire_noenterprise.go) ⇒ computer-use tools pass
	// through ungoverned, behavior UNCHANGED. When present it runs AFTER the content
	// firewall and BEFORE the budget gate; it may only DENY, never force an Allow.
	computerUse computerUseGate
	// circuitBreaker is the OPTIONAL enterprise circuit-breaker gate (circuitbreakergate.go). nil in the default AGPL build ⇒ no circuit-breaker,
	// behavior UNCHANGED. When present it runs AFTER the kill-switch gate and
	// BEFORE the residency gate; it may only DENY, never force an Allow.
	circuitBreaker circuitBreakerEngine

	clock func() time.Time
	log   *slog.Logger
}

// dlpUnscannedClass is the reserved DLP class for content that could not be reduced to
// plaintext (binary/file_id/encrypted/opaque/unmodeled). The deny-closed policy
// (modules/inferenceproxy.dlpPolicy.unscannedDenied) denies it unless the tenant set an
// explicit {"class":"unscanned","action":"allow"} rule — "*" does NOT cover it.
const dlpUnscannedClass = "unscanned"

var _ claudeapi.ProxyDecider = (*inferenceProxyDecider)(nil)

// proxySession is the opaque per-request state the connector round-trips to Finalize.
type proxySession struct {
	tenant          model.TenantID
	actor           string
	actorKind       string
	sessionRef      string
	unbindableAgent bool
	modelRef        string
	requestRef      string
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
}

// Authorize runs the PRE-forward governed gate chain for one /v1/messages call, then the
// recording reservation. It reuses authorizeChain (the deny-closed gate chain) so the
// single-message and batch paths never duplicate it. It NEVER returns Allow without a
// resolved firm identity, a non-stopped estate, an in-region surface, an authorized model,
// a non-DLP-denied prompt, and (for a recording-mandating tenant) a written ledger intent.
func (d *inferenceProxyDecider) Authorize(ctx context.Context, req claudeapi.MessageRequest, bearer string) claudeapi.ProxyDecision {
	sess, gov, deny, ok := d.authorizeChain(ctx, req, bearer)
	if !ok {
		return deny.decision
	}
	// FREEZE the governed request into the opaque forward artifact (F3): the exact bytes
	// the proxy will send upstream, serialized ONCE. The EffectiveRequestDigest is over these
	// bytes, so the authorized decision and the ledger anchor commit to precisely what runs —
	// no post-governance preflight/re-marshal can diverge (the forward uses Prepared, never a
	// re-marshal of gov).
	prepared, ferr := claudeapi.MarshalPrepared(gov)
	if ferr != nil {
		d.log.Error("inference-proxy: could not serialize the governed request; denying (deny-closed)", "err", ferr)
		return denyProxy(http.StatusInternalServerError, "api_error", "could not serialize the governed request (deny-closed)")
	}
	effDigest := prepared.Digest()
	sess.effectiveDigest = effDigest[:]
	sess.requestRef = newRequestRef()

	// Recording reservation. For a recording-MANDATING tenant, the authorized intent
	// is anchored to the ledger BEFORE the forward — no evidence ⇒ no privileged action
	// (deny-closed). It binds the EFFECTIVE digest, so the pre-forward evidence commits to the
	// exact frozen bytes. For everyone else recording is best-effort post-forward.
	if sess.pol.RecordMandatory {
		if err := d.anchorIntent(ctx, sess); err != nil {
			if defaultMandatoryYieldsTo(sess.pol, err) {
				d.log.Warn("inference-proxy: the audit spool DEGRADED this request's evidence and this tenant never configured a recording posture, so the operator's declared degrade wins over the default: forwarding with a recorded evidence GAP",
					"request_ref", sess.requestRef, "err", err)
			} else {
				d.log.Error("inference-proxy: mandatory recording intent failed; denying (deny-closed)", "err", err)
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
func (d *inferenceProxyDecider) AuthorizeBatch(ctx context.Context, requests []claudeapi.BatchRequest, bearer string) claudeapi.ProxyBatchDecision {
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
			return claudeapi.ProxyBatchDecision{
				Allow:     false,
				Status:    deny.decision.Status,
				ErrorType: deny.decision.ErrorType,
				Reason:    batchEntryDenyReason(i, requests[i].CustomID, deny.decision.Reason),
				Headers:   deny.decision.Headers,
			}
		}
	}
	// Keep the first entry's resolved session for the single batch-level ledger anchor.
	batchSess := sessions[0]
	// FREEZE the governed submission envelope into the opaque forward artifact (F3):
	// {"requests":[...]} serialized ONCE from the governed, normalized entries, so the octets
	// submitted upstream are exactly what was governed and the digest committed to (the forward
	// uses Prepared, never a re-serialize of the entries).
	prepared, ferr := claudeapi.MarshalPreparedBatch(governed)
	if ferr != nil {
		d.log.Error("inference-proxy: could not serialize the governed batch; denying (deny-closed)", "err", ferr)
		return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusInternalServerError, ErrorType: "api_error", Reason: "could not serialize the governed batch (deny-closed)"}
	}
	effDigest := prepared.Digest()
	batchSess.effectiveDigest = effDigest[:]
	batchSess.requestRef = newRequestRef()
	batchSess.modelRef = "batch"
	if batchSess.pol.RecordMandatory {
		if err := d.anchorBatchIntent(ctx, batchSess, len(governed)); err != nil {
			if defaultMandatoryYieldsTo(batchSess.pol, err) {
				d.log.Warn("inference-proxy: the audit spool DEGRADED this batch's evidence and this tenant never configured a recording posture, so the operator's declared degrade wins over the default: forwarding with a recorded evidence GAP",
					"request_ref", batchSess.requestRef, "err", err)
			} else {
				d.log.Error("inference-proxy: mandatory batch recording intent failed; denying (deny-closed)", "err", err)
				return claudeapi.ProxyBatchDecision{Allow: false, Status: http.StatusServiceUnavailable, ErrorType: "api_error", Reason: "tamper-evident recording unavailable; batch denied"}
			}
		}
	}
	return claudeapi.ProxyBatchDecision{Allow: true, Requests: governed, Prepared: prepared, Session: batchSess}
}

// FinalizeBatch anchors the batch submission outcome to the ledger (best-effort + loud). A
// batch CREATE response carries no model output, so there is no response DLP / cost
// reconciliation — the per-entry request-side chain already governed each entry.
func (d *inferenceProxyDecider) FinalizeBatch(ctx context.Context, sessAny any, out claudeapi.ProxyBatchForwardResult) {
	sess, _ := sessAny.(*proxySession)
	if sess == nil {
		return
	}
	decision := "allow"
	if out.UpstreamErr {
		decision = "upstream-error"
	}
	d.anchorBatchOutcome(ctx, sess, out, decision)
}

// --- F2: the post-identity seam (resolvedIdentity + runGates) --------------------

// gateCode is the stable, per-gate deny identifier. It is PEP-neutral semantics — the
// PDP handlers surface it as sdk.DecisionVerdict.ReasonCode ("stable, per-gate,
// interoperable") without string-matching the human-readable Reason prose. One constant per
// deny site; renaming one is a wire-visible change once ships.
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
	sessionRef := p.AgentIdentity
	subjectID := p.UserID.String()
	if p.Kind == auth.KindToken {
		subjectID = p.CredID.String()
	}
	return resolvedIdentity{
		principal: p, tenant: tenant, pol: pol,
		actor: p.Actor(), actorKind: p.ActorKind(),
		sessionRef: sessionRef,
		// A raw API token carries no authenticated NHI binding: explicitly unbindable so
		// agent-scoped governance cannot silently fall through to a broader tenant/global
		// policy (the modelaccessgate F-01 formula, kept in lockstep).
		unbindableAgent: p.Kind == auth.KindToken && sessionRef == "",
		subjectKind:     string(p.Kind),
		subjectID:       subjectID,
		ok:              true,
	}, true
}

// resolveBearerIdentity is the BEARER adapter for the runGates seam: firm identity from the
// INBOUND transport credential (never a body field —). An unauthenticated call is
// "unknown" attribution and is denied, never run as a fabricated principal.
func (d *inferenceProxyDecider) resolveBearerIdentity(ctx context.Context, bearer string) (resolvedIdentity, gateResult, bool) {
	principal, authErr := d.authr.Authenticate(ctx, bearer)
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
		d.log.Warn("inference-proxy: authentication plane unreadable; denying (deny-closed)", "err", authErr)
		return resolvedIdentity{}, gateDeny(gateCodeAuthPlaneUnavailable, sdk.FailurePlaneUnavailable, http.StatusServiceUnavailable, "api_error", "authentication plane unavailable (deny-closed)"), false
	}
	tenant, ok := d.resolveTenant(principal)
	if !ok {
		return resolvedIdentity{}, gateDeny(gateCodeTenantUnresolved, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "tenant not resolvable from the inbound credential"), false
	}
	// Load the per-tenant governance config. A read error means the decision plane is
	// unreadable — we cannot even read the fail-open knob, so this is the proxy-DOWN case:
	// default fail-CLOSED (2026-06-17, D1). A security proxy that cannot decide must
	// not forward.
	pol, perr := d.policy.Policy(ctx, tenant)
	if perr != nil {
		d.log.Warn("inference-proxy: governance config unreadable; denying (deny-closed)", "err", perr)
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
func (d *inferenceProxyDecider) authorizeChain(ctx context.Context, req claudeapi.MessageRequest, bearer string) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
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
// LOCAL) and then runSizingAndBudget (count_tokens sizing + fail-open budget). Order is
// load-bearing twice over: every security gate (deny-closed) runs BEFORE the budget gate
// (fail-open), so a FinOps outage can never bypass a security deny (the precedent);
// and every LOCAL gate runs BEFORE the sizing pre-flight, so a denied prompt is never
// egressed to the provider through the count_tokens side channel. On allow it
// returns the resolved session (requestRef unset) and the GOVERNED request (egress/
// ceilings may have rewritten req.Tools or output_config); on deny it returns ok=false
// and the deny result.
//
// SECURITY PRECONDITION: id MUST be sealed by newResolvedIdentity from a verified
// resolution (see that constructor's doc); an unsealed snapshot denies below. The
// surface/provider context (d.surface, d.surfaceGeo, the hardcoded "anthropic" provider in
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
func (d *inferenceProxyDecider) runGates(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
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
func (d *inferenceProxyDecider) runLocalGates(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity) (*proxySession, claudeapi.MessageRequest, gateResult, bool) {
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
	st, kerr := d.killSwitch.KillSwitchState(ctx, tenant)
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
	if denied, reason := circuitBreakerGateCheck(ctx, d.circuitBreaker, tenant, sessionRef); denied {
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
		d.log.Warn("inference-proxy: request failed forwardability normalization; denying (deny-closed)")
		return chainDeny(gateCodeRequestMalformed, sdk.FailureProtocolError, http.StatusBadRequest, "invalid_request_error", "request failed client-side validation and cannot be forwarded")
	}
	req = effReq

	// Resolve the tenant's residency pin once (reused by residency + the post-forward proof).
	// A pin READ FAULT honors the per-tenant fail_open knob (2026-06-17): default
	// fail-CLOSED (deny), but a tenant may opt to fail OPEN on a decision-plane outage —
	// loud + evidenced (a posture finding), never silent.
	pin := ""
	if d.residency != nil && d.residency.Enforces() {
		p, rerr := d.orgPin(ctx, tenant)
		switch {
		case rerr != nil && !pol.FailOpen:
			return chainDeny(gateCodeResidencyUnreadable, sdk.FailurePolicyReadFault, http.StatusForbidden, "permission_error", "residency check failed (deny-closed)")
		case rerr != nil:
			d.log.Warn("inference-proxy: residency pin unreadable; failing OPEN per tenant config (evidence gap)", "err", rerr)
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
	if pol.GateResidency && pin != "" && d.surfaceGeo != "" {
		if !residency.InferenceGeoCompatible(pin, d.surfaceGeo) {
			return chainDeny(gateCodeResidency, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "data residency: this surface's region is not permitted for the tenant")
		}
	}

	// 3. Model-access PEP, fail-closed. The real concrete surface string is passed
	//    (an empty surface would silently disable surface-scoped grant enforcement). A read
	//    FAULT honors the fail_open knob; a definitive !Allowed verdict ALWAYS denies.
	if pol.GateModelAccess {
		v, merr := d.models.EvaluateModelAccess(ctx, tenant, principal, sessionRef, "", req.Model, string(d.surface))
		switch {
		case merr != nil && !pol.FailOpen:
			return chainDeny(gateCodeModelAccessUnreadable, sdk.FailurePolicyReadFault, http.StatusForbidden, "permission_error", "model-access check failed (deny-closed)")
		case merr != nil:
			d.log.Warn("inference-proxy: model-access unreadable; failing OPEN per tenant config (evidence gap)", "err", merr)
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
	if pol.GateContextWindow && d.contextPolicy != nil {
		var cperr error
		ctxPol, cperr = d.contextPolicy.Apply(ctx, tenant, knowledge.ContextPolicyQuery{Principal: principal, Model: req.Model})
		switch {
		case cperr != nil && !pol.FailOpen:
			return chainDeny(gateCodeContextPolicyUnreadable, sdk.FailurePolicyReadFault, http.StatusServiceUnavailable, "api_error", "context policy unavailable (deny-closed)")
		case cperr != nil:
			ctxPol = knowledge.EffectivePolicy{}
			d.log.Warn("inference-proxy: context policy unreadable; failing OPEN per tenant config (evidence gap)", "err", cperr)
			d.emitPlaneOutageFinding(ctx, tenant, "context_policy")
		case ctxPol.Deny:
			return chainDeny(gateCodeContextPolicy, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "context policy forbids this request for the subject")
		}
	}

	// Collect the request content ONCE (system + messages, across ALL channels — text,
	// document, image, file_id, tool_result, web_search, …). The DLP gate and the content
	// firewall both read it; skip the walk when neither needs it.
	var reqContent claudeapi.CollectedContent
	if (pol.GateDLPRequest && pol.DLPEnabled()) || d.inspector != nil {
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
		denied := pol.DLPDecide(classifyText(reqContent.Texts))
		if reqContent.Unscanned && pol.DLPUnscannedDenied() {
			denied = append(denied, dlpUnscannedClass)
		}
		if len(denied) > 0 {
			d.emitDLPFinding(ctx, tenant, "input", req.Model, denied)
			return chainDeny(gateCodeDLPRequest, sdk.FailurePolicyDeny, http.StatusForbidden, "permission_error", "request blocked by data-loss-prevention policy")
		}
	}

	// 5b. Server-tool egress (commercial add-on P0 #1, OPT-IN), deny-closed. Governs Claude's
	//     internet-reaching server tools (web_search/web_fetch/code_execution) declared in
	//     req.Tools: denies an ungranted or unvalidatable tool and validates+clamps
	//     allowed_domains/blocked_domains/max_uses against the tenant's egress grant, rewriting
	//     the forwarded request. It is a SECURITY gate, so it runs here — after the other
	//     deny-closed gates (it cannot bypass them) and BEFORE the fail-open budget gate. nil
	//     ⇒ the default AGPL build: observe-only, unchanged. ActorRef is the (empty) proxy
	//     sessionRef — agent-scoped grants need the NHI binding; tenant/global grants
	//     govern raw-token traffic today.
	if d.egress != nil {
		egDec := d.egress.GovernEgress(ctx, claudeapi.ServerToolEgressInput{
			Tenant: tenant.String(), ActorRef: sessionRef, UnbindableAgent: unbindableAgent, Tools: req.Tools,
		})
		d.publishEgressFindings(ctx, tenant, req.Model, egDec.Findings)
		if !egDec.Forward {
			d.openEgressApproval(ctx, tenant, actor, egDec.ApprovalIntent)
			status := egDec.Status
			if status == 0 {
				status = http.StatusForbidden
			}
			return chainDeny(gateCodeServerToolEgress, sdk.FailurePolicyDeny, status, firstNonEmpty(egDec.ErrorType, "permission_error"), firstNonEmpty(egDec.Reason, "server-tool egress denied by policy"))
		}
		if egDec.Rewritten {
			req.Tools = egDec.GovernedTools
		}
	}

	// 5c. Content firewall (commercial add-on P1, OPT-IN), deny-closed. Deep inline inspection
	//     of the request content — prompt-injection over the untrusted channels the model
	//     ingests, plus exfiltration signals — that the PEP and the core DLP do not do. It is a
	//     SECURITY gate, so it runs here: after the other deny-closed gates (it cannot bypass
	//     them) and BEFORE the fail-open budget gate. nil ⇒ the default AGPL build (no deep
	//     inspection, unchanged). ActorRef is the (empty) proxy sessionRef — agent-scoped
	//     policies need the NHI binding; tenant/global policies govern raw-token traffic.
	if d.inspector != nil {
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
	//     default AGPL build: computer-use tools pass through ungoverned.
	if d.computerUse != nil && claudeapi.HasComputerUseTool(req.Tools) {
		cuDec := d.computerUse.GovernComputerUse(ctx, claudeapi.ComputerUseInput{
			Tenant: tenant.String(), ActorRef: sessionRef, Tools: req.Tools,
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
		d.log.Error("inference-proxy: governed request failed forwardability re-validation (deny-closed)", "err", verr)
		return chainDeny(gateCodeRequestMalformed, sdk.FailureProtocolError, http.StatusInternalServerError, "api_error", "governed request is not forwardable (deny-closed)")
	}
	// The session carries the resolved context; the caller mints the requestRef and (single-
	// message) does the recording reservation. req is the GOVERNED, normalized request.
	sess := &proxySession{
		tenant: tenant, actor: actor, actorKind: id.actorKind, sessionRef: sessionRef, unbindableAgent: unbindableAgent,
		modelRef: req.Model, pin: pin, pol: pol, inputDigest: inboundDigest[:], ctxPol: ctxPol,
	}
	return sess, req, gateResult{}, true
}

// runSizingAndBudget is the SECOND phase of the gate chain: the count_tokens
// sizing pre-flight (5g) and the fail-open budget admission (6). It runs ONLY after
// runLocalGates cleared the request — the sizing POST is the single pre-forward upstream
// egress in the proxy (it carries system, messages, tools and MCP servers), so no
// PHASE-ONE (content/security) deny may ever be preceded by it. The denies decided HERE
// (window 400/413, budget 402/429) necessarily ride on or follow the sizing POST — they
// govern a request the content gates already cleared, so that egress carries no
// gate-denied content. For a batch, AuthorizeBatch runs runLocalGates for EVERY entry
// before this phase runs for ANY entry (the barrier), so a submission denied in phase
// one produces zero upstream bytes; a window/budget deny on a later entry can follow an
// earlier entry's sizing (same semantics as the pre-split baseline).
func (d *inferenceProxyDecider) runSizingAndBudget(ctx context.Context, req claudeapi.MessageRequest, id resolvedIdentity, sess *proxySession) (gateResult, bool) {
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
		if tc, cerr := d.inf.CountTokens(ctx, req); cerr == nil {
			verdict := claudeapi.CheckContextWindowForSurface(d.surface, req.Model, tc.InputTokens)
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

	// 6. Budget admission, FAIL-OPEN — the deliberate exception, AFTER every
	//    security gate. A read error never blocks inference; only a firm cap denies (block
	//    ⇒ 402, throttle ⇒ 429), money-free (docs/SECURITY-HARDENING.md).
	if pol.GateBudget {
		dims := d.spendDims(req, sessionRef)
		dims.UserGroupRefs = principal.GroupsIn(tenant)
		// The snapshot's sessionRef IS principal.AgentIdentity (newResolvedIdentity keeps
		// them in lockstep) — consumed via the seam so every gate sees the SAME binding.
		dims.AgentRef = sessionRef
		if bc, berr := d.budget.CheckBudget(ctx, tenant, dims); berr == nil && !bc.Allowed {
			code, class := gateCodeBudget, sdk.FailurePolicyDeny
			status, errType, reason := http.StatusPaymentRequired, "billing_error", "budget limit reached"
			if strings.EqualFold(bc.Action, "throttle") {
				code, status, errType, reason = gateCodeBudgetThrottle, http.StatusTooManyRequests, "rate_limit_error", "budget throttle in effect"
			}
			res := gateDeny(code, class, status, errType, reason)
			res.decision.Headers = noRetryHeader()
			return res, false
		}
		if sc, serr := d.budget.CheckSpendLimit(ctx, tenant, actor, principal.GroupsIn(tenant)); serr == nil && !sc.Allowed {
			res := gateDeny(gateCodeSpendLimit, sdk.FailurePolicyDeny, http.StatusPaymentRequired, "billing_error", "spend limit reached")
			res.decision.Headers = noRetryHeader()
			return res, false
		}
	}
	return gateResult{}, true
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
// post-hoc residency proof (detective), cost reconciliation (fail-open) and the ledger
// outcome anchor (best-effort + loud, or already-mandated).
func (d *inferenceProxyDecider) Finalize(ctx context.Context, sessAny any, out claudeapi.ProxyForwardResult) claudeapi.ProxyResponseVerdict {
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
	if respDLPOn || (d.inspector != nil && !out.UpstreamErr) {
		respContent = claudeapi.CollectResponseContent(out.Response)
	}

	// Response DLP. Detective in flag mode (it cannot un-send a streamed response);
	// preventive in buffer mode (the connector withholds the buffered body on Block). It now
	// reads EVERY extractable channel and the unscanned signal (capability-gaps #9), not just
	// b.Text.
	if respDLPOn {
		denied := sess.pol.DLPDecide(classifyText(respContent.Texts))
		if respContent.Unscanned && sess.pol.DLPUnscannedDenied() {
			denied = append(denied, dlpUnscannedClass)
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
	if d.inspector != nil && !out.UpstreamErr {
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
	d.reconcileCost(ctx, sess, out)

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

// resolveTenant derives the request tenant: a configured single tenant (the principal must
// be a member, or superadmin), else the principal's sole grant. Ambiguous ⇒ not resolved
// (deny-closed at the caller).
func (d *inferenceProxyDecider) resolveTenant(p auth.Principal) (model.TenantID, bool) {
	if p.IsPurposeRestricted() {
		return model.TenantID(""), false
	}
	if !d.tenantHint.IsZero() {
		if p.Superadmin || p.IsMember(d.tenantHint) {
			return d.tenantHint, true
		}
		return model.TenantID(""), false
	}
	if ts := p.Tenants(); len(ts) == 1 {
		return ts[0], true
	}
	return model.TenantID(""), false
}

// orgPin reads the tenant's residency pin (orgs.data_region) in a read transaction.
func (d *inferenceProxyDecider) orgPin(ctx context.Context, tenant model.TenantID) (string, error) {
	var pin string
	err := d.store.View(ctx, tenant, func(sc store.Scope) error {
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
func (d *inferenceProxyDecider) spendDims(req claudeapi.MessageRequest, sessionRef string) finops.SpendDims {
	return finops.SpendDims{
		ProviderRef:  "anthropic",
		ModelRef:     req.Model,
		Gateway:      string(d.surface),
		SessionRef:   sessionRef,
		ServiceTier:  req.ServiceTier,
		InferenceGeo: d.surfaceGeo,
	}
}

// reconcileCost publishes the per-request cost sample(s) + forensic finding(s) on the bus.
func (d *inferenceProxyDecider) reconcileCost(ctx context.Context, sess *proxySession, out claudeapi.ProxyForwardResult) {
	if d.bus == nil || out.UpstreamErr {
		return
	}
	samples, findings := d.inf.RuntimeObservations(out.Response, sess.sessionRef, d.clock(), false)
	for _, s := range samples {
		if s.Actor == "" {
			s.Actor = sess.actor
		}
		d.publish(ctx, sess.tenant, s)
	}
	for _, f := range findings {
		d.publish(ctx, sess.tenant, f)
	}
}

func (d *inferenceProxyDecider) publish(ctx context.Context, tenant model.TenantID, obs sdkmodel.Observation) {
	if d.bus == nil {
		return
	}
	if err := d.bus.Publish(ctx, event.FromObservation(tenant.String(), proxySignalSource, obs)); err != nil && d.log != nil {
		d.log.Warn("inference-proxy: bus publish failed (best-effort)", "err", err)
	}
}

func (d *inferenceProxyDecider) emitDLPFinding(ctx context.Context, tenant model.TenantID, surface, modelRef string, classes []string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_dlp_blocked",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference " + surface + " blocked by DLP policy",
		DetailHash:  hexSHA(modelRef + "|" + surface + "|" + strings.Join(classes, ",")),
		OccurredAt:  d.clock().UTC(),
		OWASPLLM:    []string{"LLM02:2025"},
	})
}

func (d *inferenceProxyDecider) emitRequestCeilingFinding(ctx context.Context, tenant model.TenantID, modelRef string, violations []requestCeilingViolation, enforced bool) {
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
		OccurredAt:  d.clock().UTC(),
		OWASPLLM:    []string{"LLM10:2025"},
	})
}

func (d *inferenceProxyDecider) emitContextCeilingFinding(ctx context.Context, tenant model.TenantID, modelRef string, ctxPol knowledge.EffectivePolicy) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_context_ceiling",
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference request exceeds the effective context-policy/group window",
		DetailHash:  hexSHA(modelRef + "|" + ctxPol.WinningScope + "|" + strconv.FormatInt(ctxPol.MaxContextTokens, 10)),
		OccurredAt:  d.clock().UTC(),
		OWASPLLM:    []string{"LLM10:2025"},
	})
}

// emitPlaneOutageFinding records that a security gate was bypassed because the tenant
// opted to fail OPEN on a decision-plane read outage (fail_open=true). High severity: a
// security control did not run, so the crossing must be loud and evidenced.
func (d *inferenceProxyDecider) emitPlaneOutageFinding(ctx context.Context, tenant model.TenantID, gate string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_proxy_failed_open",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  gate,
		Title:       "Inference proxy failed OPEN on a " + gate + " decision-plane outage (per tenant fail_open)",
		DetailHash:  hexSHA("failed_open|" + gate),
		OccurredAt:  d.clock().UTC(),
	})
}

func (d *inferenceProxyDecider) emitResidencyFinding(ctx context.Context, tenant model.TenantID, modelRef, geo string) {
	d.publish(ctx, tenant, sdkmodel.FindingReport{
		Kind:        "inference_residency_violation",
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "anthropic.inference",
		SubjectRef:  modelRef,
		Title:       "Inference ran outside the tenant's pinned region",
		DetailHash:  hexSHA(modelRef + "|geo=" + geo),
		OccurredAt:  d.clock().UTC(),
	})
}

// anchorIntent records the AUTHORIZED decision to the ledger BEFORE the forward (the
// recording-mandating reservation). It carries no payload — the PayloadHash commits to
// the request reference, surface, model and actor; the body fingerprints arrive in the
// outcome leg (linked by request_ref).
func (d *inferenceProxyDecider) anchorIntent(ctx context.Context, sess *proxySession) error {
	if d.store == nil {
		return ErrNoLedger // mandating tenant without a ledger ⇒ deny-closed
	}
	// The evidence binds the effect: OperationID=request ref, EffectDigest=the F3
	// digest over the FROZEN forward bytes (sess.effectiveDigest). A receipt is anchored
	// only for THIS exact effect (sdk/evidence.go).
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}
	tip := proxyIntentHash(sess.requestRef, string(d.surface), sess.tenant.String(), sess.modelRef, sess.actor, sess.inputDigest, sess.effectiveDigest)
	// F9 anchoring discipline: append INSIDE the txn, but never return an error from
	// the callback on a degrade drop — that would roll back the loss accounting the store
	// just committed (audit_spool_gaps), so the gap counter never advances and its signed
	// marker never seals. Commit (return nil), capture the drop, and refuse AFTER.
	var appendDropped bool
	var evidenceRef string
	if err := d.store.Mutate(ctx, sess.tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
			Action: "inference.proxy.authorized", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
			PayloadHash: tip,
			Meta:        map[string]any{"request_ref": sess.requestRef, "surface": string(d.surface), "model": sess.modelRef, "decision": "allow", "input_digest": hex.EncodeToString(sess.inputDigest), "effective_digest": hex.EncodeToString(sess.effectiveDigest)},
		})
		if err != nil {
			return err // block-mode spool-full / write fault ⇒ roll back (nothing durable), deny
		}
		if ev.Seq == 0 {
			appendDropped = true // degrade drop: loss accounting is durable; COMMIT it, refuse after
			return nil
		}
		evidenceRef = hex.EncodeToString(ev.Hash)
		return nil
	}); err != nil {
		return err // evidence-or-refuse: a real ledger fault denies the privileged call
	}
	receipt := sdk.ClassifyAnchor(binding, evidenceRef, appendDropped, sdk.EvidenceFaultNone)
	if receipt.MustRefuse(binding) {
		return errEvidenceRefused{fault: receipt.Fault}
	}
	return nil
}

// anchorOutcome records the I/O fingerprint of a completed call. Best-effort + LOUD: a
// failed anchor is logged as an evidence gap, never swallowed (the call already happened).
func (d *inferenceProxyDecider) anchorOutcome(ctx context.Context, sess *proxySession, out claudeapi.ProxyForwardResult, decision string) {
	if d.store == nil {
		if d.log != nil {
			d.log.Error("inference-proxy: no ledger store; outcome NOT anchored (evidence gap)", "request_ref", sess.requestRef)
		}
		return
	}
	// The forwarded octets (out.EffectiveSHA) MUST equal what the decider froze
	// (sess.effectiveDigest) — the proxy forwards the Prepared artifact. A mismatch is an
	// evidence anomaly (a forward path that diverged from the governed decision); log it loud.
	if len(out.EffectiveSHA) > 0 && len(sess.effectiveDigest) > 0 && !bytes.Equal(out.EffectiveSHA, sess.effectiveDigest) && d.log != nil {
		d.log.Error("inference-proxy: forwarded-bytes digest != governed effective digest (binding anomaly)", "request_ref", sess.requestRef)
	}
	tip := proxyOutcomeHash(sess.requestRef, string(d.surface), sess.tenant.String(), sess.modelRef, decision, out.ReqBytes, out.RespBytes, out.ReqSHA, out.RespSHA, sess.inputDigest, sess.effectiveDigest)
	meta := map[string]any{
		"request_ref": sess.requestRef, "surface": string(d.surface), "model": sess.modelRef,
		"decision": decision, "req_bytes": out.ReqBytes, "resp_bytes": out.RespBytes,
		"streamed": out.Streamed, "upstream_status": out.UpstreamStatus,
		"input_digest": hex.EncodeToString(sess.inputDigest), "effective_digest": hex.EncodeToString(sess.effectiveDigest),
	}
	err := d.store.Mutate(ctx, sess.tenant, func(sc store.Scope) error {
		ev, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
			Action: "inference.proxy.recorded", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
			PayloadHash: tip, Meta: meta,
		})
		if aerr == nil && ev.Seq == 0 && d.log != nil {
			d.log.Error("inference-proxy: outcome evidence dropped by the degrade spool policy (evidence gap)", "request_ref", sess.requestRef)
		}
		return aerr
	})
	if err != nil && d.log != nil {
		d.log.Error("inference-proxy: ledger outcome anchor failed (evidence gap)", "request_ref", sess.requestRef, "err", err)
	}
}

// anchorBatchIntent records the AUTHORIZED decision for a whole batch submission BEFORE the
// forward (the recording-mandating reservation), as ONE anchor for the submission (not per
// entry). The PayloadHash commits to the request reference, surface, the "batch" kind and
// the actor; the entry count + batch id arrive in the outcome leg (linked by request_ref).
func (d *inferenceProxyDecider) anchorBatchIntent(ctx context.Context, sess *proxySession, entries int) error {
	if d.store == nil {
		return ErrNoLedger // mandating tenant without a ledger ⇒ deny-closed
	}
	// Batch inputDigest is nil here: the inbound envelope SHA (out.ReqSHA, outcome leg) binds
	// the submission bytes; per-entry canonical digests live in each entry's decision. The
	// EffectDigest is the F3 digest over the FROZEN batch envelope (sess.effectiveDigest).
	binding := sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(sess.requestRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(sess.effectiveDigest)),
	}
	tip := proxyIntentHash(sess.requestRef, string(d.surface), sess.tenant.String(), "batch", sess.actor, nil, sess.effectiveDigest)
	// Same F9 discipline as anchorIntent: commit the degrade-drop's loss accounting,
	// then refuse — never roll it back from inside the transaction.
	var appendDropped bool
	var evidenceRef string
	if err := d.store.Mutate(ctx, sess.tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
			Action: "inference.proxy.batch.authorized", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
			PayloadHash: tip,
			Meta:        map[string]any{"request_ref": sess.requestRef, "surface": string(d.surface), "kind": "batch", "entries": entries, "decision": "allow", "effective_digest": hex.EncodeToString(sess.effectiveDigest)},
		})
		if err != nil {
			return err
		}
		if ev.Seq == 0 {
			appendDropped = true
			return nil
		}
		evidenceRef = hex.EncodeToString(ev.Hash)
		return nil
	}); err != nil {
		return err
	}
	receipt := sdk.ClassifyAnchor(binding, evidenceRef, appendDropped, sdk.EvidenceFaultNone)
	if receipt.MustRefuse(binding) {
		return errEvidenceRefused{fault: receipt.Fault}
	}
	return nil
}

// anchorBatchOutcome records the outcome of a batch submission (best-effort + LOUD). The
// batch CREATE response is a receipt (id/status), not model output, so there is no response
// fingerprint — the anchor commits to the request fingerprint, the entry count and the
// created batch id.
func (d *inferenceProxyDecider) anchorBatchOutcome(ctx context.Context, sess *proxySession, out claudeapi.ProxyBatchForwardResult, decision string) {
	if d.store == nil {
		if d.log != nil {
			d.log.Error("inference-proxy: no ledger store; batch outcome NOT anchored (evidence gap)", "request_ref", sess.requestRef)
		}
		return
	}
	if len(out.EffectiveSHA) > 0 && len(sess.effectiveDigest) > 0 && !bytes.Equal(out.EffectiveSHA, sess.effectiveDigest) && d.log != nil {
		d.log.Error("inference-proxy: forwarded batch digest != governed effective digest (binding anomaly)", "request_ref", sess.requestRef)
	}
	// Batch inputDigest is nil in the hash: the inbound envelope SHA (out.ReqSHA) already binds
	// the submission bytes; the per-entry canonical digests live in each entry's decision.
	tip := proxyOutcomeHash(sess.requestRef, string(d.surface), sess.tenant.String(), "batch", decision, out.ReqBytes, 0, out.ReqSHA, nil, nil, sess.effectiveDigest)
	meta := map[string]any{
		"request_ref": sess.requestRef, "surface": string(d.surface), "kind": "batch",
		"batch_id": out.Batch.ID, "entries": out.Entries, "decision": decision,
		"req_bytes": out.ReqBytes, "upstream_status": out.UpstreamStatus,
		"effective_digest": hex.EncodeToString(sess.effectiveDigest),
	}
	err := d.store.Mutate(ctx, sess.tenant, func(sc store.Scope) error {
		ev, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: firstNonEmpty(sess.actor, model.ActorSystem), ActorKind: firstNonEmpty(sess.actorKind, model.ActorSystem),
			Action: "inference.proxy.batch.recorded", TargetKind: proxyCallKind, TargetID: model.ID(sess.requestRef),
			PayloadHash: tip, Meta: meta,
		})
		if aerr == nil && ev.Seq == 0 && d.log != nil {
			d.log.Error("inference-proxy: batch outcome evidence dropped by the degrade spool policy (evidence gap)", "request_ref", sess.requestRef)
		}
		return aerr
	})
	if err != nil && d.log != nil {
		d.log.Error("inference-proxy: batch ledger outcome anchor failed (evidence gap)", "request_ref", sess.requestRef, "err", err)
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

// classifyText runs the deterministic sensitivity classifier over each text and
// returns the distinct classes present (never a matched value — the classifier never
// returns one).
func classifyText(texts []string) []string {
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

// newRequestRef mints an opaque per-request reference (never a subject/prompt-derived
// value — it lands in the WORM ledger).
func newRequestRef() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hexSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
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

// --- composition root ---------------------------------------------------------------

// proxyAuditor is the minimal-data SOC logger handed to the connector shell.
type proxyAuditor struct{ log *slog.Logger }

func (a proxyAuditor) Record(_ context.Context, ev claudeapi.ProxyAuditEvent) {
	a.log.Info("inference-proxy: decision",
		"decision", ev.Decision, "model", ev.Model, "streamed", ev.Streamed,
		"upstream_status", ev.UpstreamStatus, "req_bytes", ev.ReqBytes, "resp_bytes", ev.RespBytes,
		"reason", ev.Reason)
}

// buildClaudeMessagesProxyServer constructs the inline inference PEP server on its own
// loopback socket, or nil when not provisioned (or unsupported/unwired). It is built
// AFTER boot, so the engine's authenticator, models/finops modules, kill-switch, residency
// registry and store are all live. The server is a DEDICATED *http.Server with
// WriteTimeout 0 — the core API's 60s WriteTimeout would cut a streaming SSE response.
func buildClaudeMessagesProxyServer(eng *engine, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadInferenceProxyConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load inference proxy operator config: %w", err)
	}
	if strings.TrimSpace(cfg.Surface) == "" {
		return nil, nil // not provisioned
	}
	// the fixed tenant is validated FIRST, before anything else this config drives,
	// and deny-closed like the five sibling readers of a configured tenant. Until an
	// operator typo here only produced a startup log.Warn and the proxy kept going with an
	// empty hint — so a config that said "serve ONLY this organization" silently became
	// "serve whichever organization the credential names". An ABSENT tenant is a different
	// and legitimate case (the documented "" = infer from the credential) and is NOT an
	// error: parseBusinessTenant reports it as not-present with a nil error.
	tenantHint, _, terr := parseBusinessTenant("inference-proxy config: tenant", cfg.Tenant)
	if terr != nil {
		return nil, terr
	}
	surface := sdkmodel.Gateway(strings.TrimSpace(cfg.Surface))
	if surface != sdkmodel.GatewayDirect && surface != sdkmodel.GatewayClaudePlatformAWS {
		log.Error("inference-proxy: surface not supported in v1 (use direct or claude-platform-aws); NOT mounted", "surface", surface)
		return nil, nil
	}
	// The governed decision needs all of these; in production they are always wired. If any
	// is missing, do NOT mount an ungoverned proxy (deny-closed posture).
	if eng.authr == nil || eng.models == nil || eng.finops == nil || eng.killSwitch == nil || eng.inferenceProxy == nil || eng.store == nil {
		log.Error("inference-proxy: governance dependencies not wired; NOT mounted (deny-closed)")
		return nil, nil
	}

	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		if s, ok := claudeapi.SurfaceFor(surface); ok {
			base = strings.ReplaceAll(s.BaseURLPattern, "{region}", strings.TrimSpace(cfg.Region))
		}
	}
	apiKey := strings.TrimSpace(cfg.UpstreamKey)
	var doer modelprovider.Doer
	if eng.tracer != nil {
		// The dedicated proxy is outside the core API router, so it must opt into the
		// same minimal-data GenAI transport explicitly. The transport records bounded
		// body fingerprints plus model/token metadata; it never attaches content.
		doer = eng.tracer.AnthropicHTTPClient(nil)
	}
	if surface == sdkmodel.GatewayClaudePlatformAWS {
		// SigV4-signed forward (service aws-external-anthropic); no x-api-key on this surface.
		doer = claudeapi.NewSigV4Doer(doer, cfg.AWSAccessKeyID, cfg.AWSSecretKey, cfg.AWSSessionToken, awsExternalAnthropicService, strings.TrimSpace(cfg.Region), nil)
		apiKey = ""
	}
	inf := claudeapi.NewInference(claudeapi.InferenceConfig{
		BaseURL: base, APIKey: apiKey, AnthropicVersion: strings.TrimSpace(cfg.AnthropicVersion),
		Gateway: surface, Doer: doer,
	})

	dec := &inferenceProxyDecider{
		surface: surface, surfaceGeo: strings.TrimSpace(cfg.InferenceGeo), tenantHint: tenantHint,
		inf: inf, authr: eng.authr, models: eng.models, budget: eng.finops, killSwitch: eng.killSwitch,
		contextPolicy: eng.knowledgeMod,
		policy:        eng.inferenceProxy, residency: eng.residencyReg, store: eng.store, bus: eng.bus,
		// Server-tool egress gate (P0 #1): the real gate under -tags enterprise WITH an egress
		// config, else nil (observe-only, unchanged). approvals reuses the HITL bridge.
		egress: newServerToolEgressGate(os.Getenv, log), approvals: eng.approvalBridge,
		// Content firewall (P1): the real inspector under -tags enterprise WITH a firewall
		// config, else nil (no deep inspection, unchanged). Reuses the same approval bridge.
		inspector: newContentInspector(os.Getenv, log),
		// Computer-use governance: the real gate under -tags enterprise WITH a
		// computer-use config, else nil (ungoverned, unchanged).
		computerUse: newComputerUseGate(os.Getenv, log),
		// Runtime circuit-breaker (wired in): the SAME instance the finding
		// rail drives, taken from the engine rather than constructed again here — two
		// instances would mean the breaker that trips is not the breaker consulted.
		circuitBreaker: eng.circuitBreaker,
		clock:          time.Now, log: log,
	}
	proxy := claudeapi.NewMessagesProxy(inf, dec, proxyAuditor{log: log}, time.Now)
	credSource, credKind := sessionCredentialSource(osGetenv, eng.wifBroker)
	if strings.TrimSpace(cfg.PublicURL) != "" && credSource == nil {
		log.Warn("inference-proxy: apps-gateway OAuth enabled without a sessions credential source; approved device grants cannot mint until one is configured")
	} else if strings.TrimSpace(cfg.PublicURL) != "" {
		log.Info("inference-proxy: apps-gateway OAuth credential source wired", "source", credKind)
	}
	grantStore, _ := eng.inferenceProxy.(deviceGrantStore)
	spendAdmin, _ := eng.finops.(spendLimitAdmin)

	mux := http.NewServeMux()
	mountAppsGatewayHandlers(mux, newAppsGatewayHandler(cfg, tenantHint, eng.authr, grantStore, spendAdmin, credSource, time.Now, version))
	mux.Handle("/", appsGatewayRootHandler(proxy))
	var handler http.Handler = mux
	if eng.tracer != nil {
		// This listener does not pass through api.Server's middleware chain. Apply the
		// same method-only server span here; raw paths and bodies never become attributes.
		handler = eng.tracer.HTTPMiddleware(handler)
	}
	addr := strings.TrimSpace(cfg.Listen)
	if addr == "" {
		addr = defaultInferenceProxyListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("inference-proxy: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token verification + the governed decision, not network isolation", "addr", addr)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		// WriteTimeout MUST be 0: a streaming /v1/messages SSE response runs longer than any
		// fixed deadline. The core API server's 60s WriteTimeout (server.go) would cut it.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	log.Info("inference-proxy: inline /v1/messages PEP mounted (opt-in, governed)",
		"addr", addr, "surface", surface, "geo", firstNonEmpty(cfg.InferenceGeo, "per-request"),
		"tenant_hint", tenantHint.String(), "residency", eng.residencyReg != nil && eng.residencyReg.Enforces())
	return srv, nil
}

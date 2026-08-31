// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// evidencerelease.go (q1-MCP stage 5) — the evidence binding of the MEDIATED
// surfaces (UI resources/read of ui://, elicitation/create, sampling/createMessage)
// and of the RESPONSE RELEASE every mediated surface performs: strict
// canonicalization of the mediated-method params (the same P0 class the tools/call
// and task surfaces closed in stages 3-4), the mediated-method
// OperationID/EffectDigest derivations, and the response-release CHILD operation
// that makes "write the upstream result to the caller" a governed effect of its
// own — claim-anchored and leadership-fenced BEFORE a single byte is written,
// settled with the stage-4 three-way byte-accounting classification afterwards.
//
// # Why the release is its own two-phase child operation
//
// The design ("Settle: outcome + optional response-release anchor", §2) allows the
// release anchor to ride the dispatch settlement atomically. That shape needs the
// journal ADAPTER to consume GateOutcome.ReleaseBinding and answer with an
// anchored GateSettlement.ReleaseReceipt — composition-root work outside this
// stage's allowed paths (cmd/olivares is stage 7 re-pin territory). Stage 5
// therefore implements the release as a DETERMINISTIC CHILD OPERATION through the
// existing Record → BeforeEffect → Settle seam, exactly like mcp.task.track: the
// frozen S5 law holds (an anchored receipt for the exact release binding is
// the only green light for the write), it works against the production adapter
// unchanged, and the dispatch settlement still NAMES the release child through
// GateOutcome.ReleaseBinding so the two journal records link. The
// GateSettlement.ReleaseReceipt field stays declared and UNCONSUMED — the
// atomic-settlement capability remains open for a later adapter (carried
// residual, stated in sessions-q1-mcp-stage5.md).
//
// # The custody argument (no extra guard needed)
//
// The task-handle relay needs a panic-safe custodian (taskHandleRelayGuard)
// because the LEDGER holds mutable owner obligations that must be closed out
// exactly once. A response release has no local mutable state: its durable state
// IS the journal claim. A panic between the release claim and its settlement
// leaves the operation claimed-and-unsettled — ambiguous, never re-emitted,
// deny-closed by the journal itself — and the task-ledger obligations of the one
// release site that has them (the mediated tasks/get) are closed out by the
// caller from the returned write error, exactly as stage 4 wrote them.

// Domain-separation labels of the mediated-surface derivations.
const (
	mcpMediatedEffectDomainV1  = "olivares.mcp.mediated.effect.v1"
	mcpMediatedEffectProfileV1 = "mcp-mediated-binding-v1"
	mcpMediatedPolicyDomainV1  = "olivares.mcp.mediated.policy.v1"

	mcpReleaseOperationDomainV1 = "olivares.mcp.release.operation.v1"
	mcpReleaseEffectDomainV1    = "olivares.mcp.release.effect.v1"
)

// Stable release CLASSES — bound into the release effect digest and suffixed
// onto the journal action verb. Classes, never free text.
const (
	releaseClassUI             = "ui"
	releaseClassElicitation    = "elicitation"
	releaseClassSampling       = "sampling"
	releaseClassMRTRToolResult = "mrtr.tool-result"
	releaseClassMRTRTaskResult = "mrtr.task-result"
	// releaseClassMRTRTaskHandle is the release of a durable TASK HANDLE that
	// itself carries MRTR input requests (review round 1, S5-01): the handle
	// relay hands the caller those payloads, so it is a mediated release like any
	// other — a distinct class from the tasks/get result release above.
	releaseClassMRTRTaskHandle = "mrtr.task-handle"
	// releaseClassMRTRTaskCancel is the release of a client tasks/cancel result
	// that carries MRTR input requests — the same class as the two the review
	// named, on the third surface that relays an upstream task result verbatim.
	releaseClassMRTRTaskCancel = "mrtr.task-cancel-result"
	// releaseClassMRTRUIResource is the release of a ui:// resources/read whose
	// upstream answered with an InputRequiredResult instead of a template.
	// resources/read is one of the three requests MRTR sanctions, and a ui://
	// read is still a resources/read — but it is a DISTINCT class from a plain
	// resource read: the route (Codex 2026-07-29 §4.2, "route_class=ui") is part
	// of what an operator reviews, and folding the two into one class would make
	// a render-surface release indistinguishable in the journal from an ordinary
	// one. One parent claim, one release child, one class that names the route.
	releaseClassMRTRUIResource = "mrtr.ui-resource-result"
)

// Journal action labels of the evidence-enforced mediated effects. The
// request-phase claims are suffixed with the OperationID provenance kind
// (keyed | request_instance), like the client task methods; the release child
// operations are suffixed with their stable class (their provenance is the
// derivation itself).
const (
	mediatedActionUIReadPrefix      = "mcp.ui.read."
	mediatedActionElicitationPrefix = "mcp.elicitation.create."
	mediatedActionSamplingPrefix    = "mcp.sampling.create_message."
	releaseActionPrefix             = "mcp.release."
)

// Reserved-key PROFILES of the mediated surfaces (stage-3/4 discipline: every
// top-level member a PEP interprets is reserved, so a case-variant alias can
// never make the gate authorize one logical field while a case-folding upstream
// consumes another out of the very bytes forwarded).
//
// The elicitation profile reserves the SEP-1036 URL-mode members `mode` and
// `url` because the PEP interprets them (review round 1, S5-04): they are
// TOP-LEVEL members of ElicitRequestURLParams in the pinned MCP 2026-07-28 RC
// schema (an internal design note (not shipped):2808-2825; schema.json
// requires exactly [message, mode, url]), not `_meta` extensions.
var (
	uiReadReservedKeys      = []string{"uri", "_meta"}
	elicitationReservedKeys = []string{"message", "requestedSchema", "mode", "url", "_meta"}
	samplingReservedKeys    = []string{"messages", "_meta"}
)

// canonicalMediatedParams is the strict, canonicalized view of one mediated-method
// params payload (UI resources/read, elicitation/create, sampling/createMessage).
type canonicalMediatedParams struct {
	// Presence distinguishes absent params, explicit null and a present object.
	Presence string
	// OperationKey is the client-supplied idempotency key ("" when none) — the
	// same params._meta extension tools/call and the task methods honor.
	OperationKey string
	// Forward is the canonical params to forward upstream (operation key
	// STRIPPED, everything else — trace correlation included — preserved).
	Forward json.RawMessage
	// Effect is the canonical effect view bound into the EffectDigest (Forward
	// minus the versioned W3C trace-correlation members).
	Effect []byte

	// tree is the strict canonical tree (trace members stripped) the surface
	// handlers read their EXACT-CASED members from — never a case-folding
	// json.Unmarshal of the raw bytes (the F-07 smuggling class).
	tree    canonValue
	hasTree bool
}

// stringMember returns the exact-cased top-level string member ("" when the
// params are not an object, the member is absent, or it is not a string).
func (c *canonicalMediatedParams) stringMember(key string) string {
	if !c.hasTree {
		return ""
	}
	if m := c.tree.member(key); m != nil && m.val.kind == canonString {
		return m.val.str
	}
	return ""
}

// rawMember returns the canonical bytes of the exact-cased top-level member
// (nil when absent or when the params are not an object).
func (c *canonicalMediatedParams) rawMember(key string) json.RawMessage {
	if !c.hasTree {
		return nil
	}
	if m := c.tree.member(key); m != nil {
		return json.RawMessage(encodeCanonical(m.val))
	}
	return nil
}

// treeMember returns the strict canonical tree of the exact-cased top-level
// member (nil when absent or when the params are not an object). It is how a
// mediated surface reads a NESTED governed structure without ever handing the
// raw bytes back to a case-folding decoder (review round 1, S5-03).
func (c *canonicalMediatedParams) treeMember(key string) *canonValue {
	if !c.hasTree {
		return nil
	}
	if m := c.tree.member(key); m != nil {
		return &m.val
	}
	return nil
}

// elicitationURLTarget extracts the SEP-1036 URL-mode redirect target from the
// strict tree with exact-cased members (the mode VALUE comparison stays
// case-insensitive — it is a value, not a key). "" for non-URL-mode
// elicitations.
//
// Review round 1 (S5-04): the AUTHORITATIVE placement is TOP-LEVEL. The
// pinned MCP 2026-07-28 RC schema defines ElicitRequestURLParams as
// {mode:"url", message, url} at the top level of params
// (an internal design note (not shipped):2808-2825), so stage 5's
// `_meta.elicitation.{mode,url}` read handed the policy an EMPTY URL target for
// every conforming URL-mode request. The top-level members are read FIRST; the
// `_meta.elicitation` shape is still honored as a fallback so a deployment that
// already emits it keeps being mediated (a capability is never withdrawn to make
// a check easier).
//
// REFUTED, with evidence: the review states the official request also carries a
// top-level `elicitationId`. The pinned RC schema contains NO `elicitationId`
// anywhere (zero matches in schema.ts and schema.json at upstream 76346843), and
// ElicitRequestURLParams requires exactly [message, mode, url]. No member of
// that name is interpreted or reserved here.
func (c *canonicalMediatedParams) elicitationURLTarget() string {
	if !c.hasTree {
		return ""
	}
	if mode := c.tree.member("mode"); mode != nil && mode.val.kind == canonString &&
		strings.EqualFold(mode.val.str, "url") {
		if url := c.tree.member("url"); url != nil && url.val.kind == canonString {
			return strings.TrimSpace(url.val.str)
		}
		// Declared URL mode with no readable url: the target is unknown, not absent.
		// "" is what the policy sees, and the mediator still gets the message.
		return ""
	}
	meta := c.tree.member("_meta")
	if meta == nil || meta.val.kind != canonObject {
		return ""
	}
	el := meta.val.member("elicitation")
	if el == nil || el.val.kind != canonObject {
		return ""
	}
	mode := el.val.member("mode")
	if mode == nil || mode.val.kind != canonString || !strings.EqualFold(mode.val.str, "url") {
		return ""
	}
	if url := el.val.member("url"); url != nil && url.val.kind == canonString {
		return strings.TrimSpace(url.val.str)
	}
	return ""
}

// canonicalizeMediatedParams strictly decodes and canonicalizes the params of a
// mediated method under the surface's reserved-key profile. Any error is a
// PROTOCOL refusal (invalid params, 400/-32602) BEFORE any claim and before any
// forward — the same rule as tools/call and the task methods.
func canonicalizeMediatedParams(raw json.RawMessage, reserved []string) (canonicalMediatedParams, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return canonicalMediatedParams{Presence: paramsAbsent}, nil
	}
	if trimmed == "null" {
		return canonicalMediatedParams{
			Presence: paramsNull,
			Forward:  json.RawMessage("null"),
			Effect:   []byte("null"),
		}, nil
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return canonicalMediatedParams{}, err
	}
	if v.kind != canonObject {
		return canonicalMediatedParams{}, fmt.Errorf("mcp: params must be a JSON object")
	}
	if err := rejectReservedKeyAliases(v, reserved); err != nil {
		return canonicalMediatedParams{}, err
	}
	out := canonicalMediatedParams{Presence: paramsPresent}
	key, err := extractOperationKey(&v)
	if err != nil {
		return canonicalMediatedParams{}, err
	}
	out.OperationKey = key
	out.Forward = encodeCanonical(v)
	stripTraceMembers(&v)
	out.Effect = encodeCanonical(v)
	out.tree, out.hasTree = v, true
	return out, nil
}

// mediatedPolicyDigest binds the STABLE policy posture of one mediated-surface
// effect: the surface class, the UI consent posture (required + granted — deny
// paths never reach the digest, so a granted:0 with required:1 cannot occur on an
// emitted effect and the preimage stays fixed), and whether the content
// inspector/mediator seams were wired at decision time. Only stable postures —
// never the mediator's verdict text (the verdict is a decision, not policy, and
// its unstable Reason must not cause false rebinds).
func mediatedPolicyDigest(surface string, consentRequired, consentGranted, inspectorWired, mediatorWired bool) string {
	return evidenceLPDigest(
		mcpMediatedPolicyDomainV1,
		surface,
		"consent_required:"+boolMark(consentRequired),
		"consent_granted:"+boolMark(consentGranted),
		"inspector:"+boolMark(inspectorWired),
		"mediator:"+boolMark(mediatorWired),
	)
}

// deriveMediatedEffectDigest binds the FULL effective request of one mediated
// method: its own versioned domain + profile, tenant, resource, method, caller
// identity, the stable upstream descriptor, the governed target (the ui://
// template URI for UI; the surface kind for elicitation/sampling), normalized
// granted scopes, params presence + the canonical-effect-params hash and the
// mediated policy digest. OperationID, JSON-RPC id, timestamps and trace context
// are deliberately excluded (a keyed retry is the SAME effect).
func deriveMediatedEffectDigest(tenant, resource, method string, tok validatedToken,
	targetKind, targetRef, upstreamDescriptor string, grantedScopes []string,
	canon canonicalMediatedParams, policyDigest string) string {
	paramsSum := sha256.Sum256(canon.Effect)
	parts := []string{
		mcpMediatedEffectDomainV1,
		mcpMediatedEffectProfileV1,
		tenant,
		resource,
		method,
		tok.Issuer,
		tok.ClientID,
		tok.Subject,
		tok.ActAs,
		upstreamDescriptor,
		targetKind,
		targetRef,
		fmt.Sprintf("granted_scopes:%d", len(grantedScopes)),
	}
	parts = append(parts, grantedScopes...)
	parts = append(parts, canon.Presence, hex.EncodeToString(paramsSum[:]), policyDigest)
	return evidenceLPDigest(parts...)
}

// deriveResponseReleaseBinding derives the response-release CHILD operation of
// one settled dispatch: the OperationID chains the PARENT operation and the
// stable release class (deterministic — one dispatch can never anchor two
// releases of the same class, and a duplicate claim replays instead of
// re-anchoring); the EffectDigest additionally binds the digest of the EXACT
// result bytes being released, so the release evidence names the response
// version it authorized and nothing else ("bind the release to the exact
// response", the stage-5 inheritance requirement).
func deriveResponseReleaseBinding(tenant string, parent sdk.EvidenceBinding, class, releasedResultDigest string) sdk.EvidenceBinding {
	return sdk.EvidenceBinding{
		OperationID: sdk.OperationID(evidenceLPDigest(
			mcpReleaseOperationDomainV1,
			tenant,
			string(parent.OperationID),
			class,
		)),
		EffectDigest: sdk.EffectDigest(evidenceLPDigest(
			mcpReleaseEffectDomainV1,
			tenant,
			string(parent.EffectDigest),
			class,
			releasedResultDigest,
		)),
	}
}

// releaseDecision builds the allow decision of one response-release child
// operation (minimal data, stable reason; the class is the only variable part).
func (rs *ResourceServer) releaseDecision(tok validatedToken, tool, scope, class, mcpTag, trace string) ToolDecision {
	return ToolDecision{
		Tenant: rs.tenant, Subject: tok.Subject, IsDelegated: tok.IsDelegated, ActAs: tok.ActAs,
		Tool: tool, RequiredScope: scope,
		Allowed: true, Reason: "mediated response release authorized (" + class + ")",
		MCPTag: mcpTag, TokenBinding: tok.Binding, TraceParent: trace,
		EffectAction: releaseActionPrefix + class, At: rs.clock(),
	}
}

// withheldReleaseDecision builds the claim decision of a response-release child
// whose disposition is WITHHELD: the operation is recorded (Allowed drives the
// journal claim), and the settlement — not this decision — carries the
// disposition. The reason states it so an audit reader never mistakes the claim
// for a delivery authorization.
func (rs *ResourceServer) withheldReleaseDecision(tok validatedToken, tool, scope, class, mcpTag, trace string) ToolDecision {
	dec := rs.releaseDecision(tok, tool, scope, class, mcpTag, trace)
	dec.Reason = "mediated response withheld by policy; the release child records the disposition (" + class + ")"
	return dec
}

// settleWithheldRelease durably records the RELEASE DISPOSITION of an OBSERVED
// upstream response that a governance decision withheld from the caller
// (stage-7 B-bis round 2). The PARENT settlement describes the dispatch and
// only the dispatch (`completed` = the round trip finished), so the retention
// is journaled on the response-release CHILD operation: claimed here and
// settled `withheld` with the digest of the exact bytes that never left
// governance — the disposition of an observed response is never a fact without
// its own durable record.
//
// It runs on DENY legs only, so no leadership fence guards it: nothing is
// emitted, and a fence failure must never weaken a refusal. The refusal it
// accompanies STANDS regardless of the journal's fate (evidence is mandatory on
// allow, never on deny); every fault is alerted loudly through alert.
func (rs *ResourceServer) settleWithheldRelease(ctx context.Context, dec ToolDecision, release sdk.EvidenceBinding, releasedResultDigest string, alert func(string)) {
	rec := rs.auditor.Record(ctx, dec, release)
	switch {
	case rec.State == GateRecordReplaySettled:
		// The disposition of this exact release child is already durable.
		return
	case rec.State != GateRecordFresh || rec.Receipt.MustRefuse(release):
		if alert != nil {
			alert("withheld-release claim refused (" + string(rec.State) + "); the refusal stands without its child record")
		}
		return
	}
	settlement := rs.auditor.Settle(ctx, GateOutcome{
		Record: rec, State: DispatchWithheld, ResultDigest: releasedResultDigest,
	})
	if settlement.FailureClass != sdk.FailureNone && alert != nil {
		alert("withheld-release settlement did not record durably (the refusal stands; the release operation stays claimed)")
	}
}

// anchoredRelease is one claimed, fenced response release: the ONLY path from a
// settled mediated dispatch to writeResult. Reaching a non-nil value means the
// release receipt anchored for the exact binding (the frozen law's green light).
type anchoredRelease struct {
	rs      *ResourceServer
	rec     GateRecord
	binding sdk.EvidenceBinding
	// alert is the caller's best-effort audit hook for release faults (refused
	// settlement); never a payload, always a stable reason.
	alert func(reason string)
}

// anchorResponseRelease claims + anchors + leadership-fences the response-release
// child operation. On ANY refusal the upstream result is WITHHELD (the design
// matrix row "Response-release anchor failure"): the withheld wire shape is
// written, the fault is alerted best-effort, and (nil, false) is returned — the
// caller stops without writing a byte of the result.
func (rs *ResourceServer) anchorResponseRelease(ctx context.Context, w http.ResponseWriter, id json.RawMessage, dec ToolDecision, release sdk.EvidenceBinding, parentOpID string, alert func(string)) (*anchoredRelease, bool) {
	rec := rs.auditor.Record(ctx, dec, release)
	if !rec.MayEmit(release) {
		// Refused, replay-pending or replay-settled: none may write. A replay of a
		// deterministic release child means THIS dispatch already attempted its
		// release — the claim never re-emits, so the result stays withheld.
		if alert != nil {
			alert("response-release claim refused (" + string(rec.State) + "); the upstream result was withheld")
		}
		rs.writeReleaseWithheld(w, id, parentOpID)
		return nil, false
	}
	if fence := rs.auditor.BeforeEffect(ctx, rec); fence.MustRefuse(release) {
		if alert != nil {
			alert("response-release fence refused (leadership lost after the claim); the upstream result was withheld")
		}
		rs.writeReleaseWithheld(w, id, parentOpID)
		return nil, false
	}
	return &anchoredRelease{rs: rs, rec: rec, binding: release, alert: alert}, true
}

// write emits the released result through the SAME three-way byte-accounting
// discipline stage 4 established for the task-handle relay (relayWriteCounter;
// round-11 N11-01): a nil error is LOCAL WRITER ACCEPTANCE (never remote
// receipt) and settles completed; a write error with a PROVEN zero-byte count
// settles not_sent (nothing that could carry the result reached the transport);
// any other failed or unreportable count settles unknown (the caller may hold
// the bytes). The wrapper is handed to nothing but writeResult — which uses only
// Header, WriteHeader and Write — so no optional http interface (Flusher,
// Hijacker, Pusher) behavior is hidden beyond what stage 4's relayWriteCounter
// already established.
//
// The settlement runs AFTER the write because it records the write's honest
// classification; bytes already accepted cannot be unwritten, so a REFUSED
// release settlement cannot withhold anything — it leaves the release operation
// claimed/ambiguous (never re-emitted) and is alerted loudly. The returned error
// is the write error, for the one caller whose ledger obligations depend on it
// (the mediated tasks/get: a failed write records no delivery and retains the
// record, exactly as stage 4 wrote it).
func (rel *anchoredRelease) write(ctx context.Context, w http.ResponseWriter, method string, id, result json.RawMessage) releaseWrite {
	counted := &relayWriteCounter{ResponseWriter: w}
	werr := rel.rs.writeResult(counted, method, id, result)
	out := releaseWrite{err: werr, provenZero: counted.provenZeroBytes()}
	var state DispatchState
	switch {
	case werr == nil:
		state = DispatchCompleted
	case out.provenZero:
		state = DispatchNotSent
	default:
		state = DispatchUnknown
	}
	settlement := rel.rs.auditor.Settle(ctx, GateOutcome{
		Record: rel.rec, State: state, ResultDigest: resultDigest(result),
	})
	if settlement.FailureClass != sdk.FailureNone && rel.alert != nil {
		rel.alert("response-release settlement did not record durably (write classified " + string(state) +
			"); the release operation stays claimed and is never re-emitted")
	}
	return out
}

// releaseWrite is the outcome of one released write: the writer's error and the
// ONLY fact that may classify a failed write never-delivered — a PROVEN
// zero-byte write (round-11 N11-01). The task-handle relay's custody transitions
// depend on both (review round 1, S5-01), so the release engine reports
// both instead of collapsing them into the error alone.
type releaseWrite struct {
	err        error
	provenZero bool
}

// writeReleaseWithheld is the wire shape of a WITHHELD response release: the
// dispatch completed and settled durably, but the release evidence could not be
// anchored, so the result is not written. 503 + -31010 (the evidence-fault
// family), retryable:false — a keyed retry status-replays the recorded dispatch
// outcome (the journal retains no raw results) and a request_instance retry is a
// NEW operation; neither recovers the withheld bytes. operation_id names the
// PARENT operation (the identity the caller can correlate/replay).
func (rs *ResourceServer) writeReleaseWithheld(w http.ResponseWriter, id json.RawMessage, parentOpID string) {
	if evidenceNotificationRefusal(w, id, http.StatusServiceUnavailable, false) {
		return
	}
	data := map[string]any{
		"failure_class": string(sdk.FailureEvidenceFault),
		"retryable":     false,
	}
	if parentOpID != "" {
		data["operation_id"] = parentOpID
	}
	rs.writeRPCErrorData(w, http.StatusServiceUnavailable, id, rpcEvidenceUnavailable,
		"response release evidence unavailable; the upstream result was withheld", data)
}

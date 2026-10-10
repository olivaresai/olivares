// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/modelprovider/gateway"
	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
)

// This file closes the "routing resolves but never executes" seam:
// it adds a GOVERNED execution path on top of the pure selection routing.go already
// computes. The module still does NOT become an inference gateway (that is);
// it RESOLVES a decision and then ACTS through a deny-closed Executor port the
// composition root provisions — which calls the resolved target through the real
// inference client and emits the runtime cost/forensic (CLA-15/ANT2-15). With no
// executor wired the path is deny-closed (503): the control plane can select a model
// but will not spend against a provider.

// errNoExecutor is the deny-closed sentinel the default executor returns. The handler
// maps it to 503 so an un-provisioned control plane actuates nothing.
var errNoExecutor = errors.New("models: no routing execution backend wired")

// ErrExecutionTargetUnsupported refuses a target the wired executor cannot serve.
// The handler returns this fixed message without exposing a wrapped transport error.
var ErrExecutionTargetUnsupported = errors.New("The legacy routing executor supports only Anthropic targets; use a bound execution profile for another provider.")

// ExecuteRequest is what the Executor needs to run one resolved routing decision: the
// ordered target chain to try (primary first, then fallbacks), the user input, a token
// bound, and an optional session ref for cost attribution.
type ExecuteRequest struct {
	Tenant     model.TenantID
	Chain      []modelrouter.Target
	Input      string
	MaxTokens  int
	SessionRef string
}

// ExecuteResult is the outcome of a governed routing execution: the model output, the
// target that actually served (after any fallback), and the token counts. It carries
// NO USD amount — the monetary cost lands in FinOps via the emitted CostSample, and
// money stays off this surface (docs/SECURITY-HARDENING.md).
type ExecuteResult struct {
	Text         string
	Served       modelrouter.Target
	FallbackUsed bool
	InputTokens  int64
	OutputTokens int64
	Refusal      bool
}

// Executor runs a resolved routing decision against the real provider/gateway and
// emits the runtime cost/forensic (CLA-15/ANT2-15) through the in-process bus
// publisher. It is the governed actuation seam (Fase K): the module RESOLVES (pure
// selection) always, but ACTS only through this port, and the default is DENY-CLOSED.
// The real adapter lives in cmd/olivares (it holds the inference credential and
// the publisher); a connector/module never embeds it (ARCHITECTURE.md, §3).
type Executor interface {
	Execute(ctx context.Context, req ExecuteRequest) (ExecuteResult, error)
}

// unwiredExecutor is the deny-closed default: every execution is refused so an
// un-provisioned control plane can select but never spend. Start() warns once.
type unwiredExecutor struct{}

func (unwiredExecutor) Execute(context.Context, ExecuteRequest) (ExecuteResult, error) {
	return ExecuteResult{}, errNoExecutor
}

// defaultExecuteMaxTokens bounds an execute call that does not set max_tokens.
const defaultExecuteMaxTokens = 1024

// gatewayRouteGaps returns required capability names RouteGateway cannot
// invoke. Streaming is the only flag Effective keeps. The names come from
// the policy, never from the request body.
func gatewayRouteGaps(required []string) []string {
	caps := make([]mp.Capability, 0, len(required))
	for _, c := range required {
		if c == "" {
			continue
		}
		caps = append(caps, mp.Capability(c))
	}
	if len(caps) == 0 {
		return nil
	}
	effective := gateway.Effective(caps, gateway.RouteGateway)
	have := make(map[mp.Capability]struct{}, len(effective))
	for _, c := range effective {
		have[c] = struct{}{}
	}
	var missing []string
	seen := make(map[string]struct{}, len(caps))
	for _, c := range caps {
		if _, ok := have[c]; ok {
			continue
		}
		name := string(c)
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		missing = append(missing, name)
	}
	return missing
}

// textExecutionRefuses reports whether the legacy text executor must not send.
// An empty requirement does not refuse. The Chat profile branch returns
// before this is consulted, unless the profile itself is on the gateway route.
func textExecutionRefuses(required []string) bool {
	return len(gatewayRouteGaps(required)) > 0
}

// chatTextGaps returns required capability names the Chat text transport
// cannot carry. That transport sends one user string with stream false, so
// every catalog flag is a gap. Names come from the policy, not the request.
func chatTextGaps(required []string) []string {
	var missing []string
	seen := make(map[string]struct{}, len(required))
	for _, c := range required {
		name := strings.TrimSpace(c)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		missing = append(missing, name)
	}
	return missing
}

// chatTextRefuses reports whether a pinned Chat profile must not call Chat.
// An empty requirement does not refuse: the text send itself is allowed.
func chatTextRefuses(required []string) bool {
	return len(chatTextGaps(required)) > 0
}

// chatTextNote names the required capabilities the Chat text path cannot
// invoke. It carries no prompt, endpoint, or credential.
func chatTextNote(required []string) string {
	missing := chatTextGaps(required)
	if len(missing) == 0 {
		return "not invocable on the Chat text path"
	}
	verb := "is"
	if len(missing) != 1 {
		verb = "are"
	}
	return strings.Join(missing, ", ") + " " + verb + " not invocable on the Chat text path"
}

// notInvocableNote names the required capabilities the gateway route cannot
// invoke. It carries no prompt, endpoint, or credential.
func notInvocableNote(required []string) string {
	missing := gatewayRouteGaps(required)
	if len(missing) == 0 {
		return "not invocable on the gateway route"
	}
	verb := "is"
	if len(missing) != 1 {
		verb = "are"
	}
	return strings.Join(missing, ", ") + " " + verb + " not invocable on the gateway route"
}

// executeRequestDTO is the POST /execute body: the prompt to run through the resolved
// model and a token bound. Minimal by design — a single user turn; the governed
// execute is not a full chat API (that is the gateway's job).
type executeRequestDTO struct {
	Input     string `json:"input"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	// Operation is explicit for content-addressed execution profiles. It is optional
	// only so callers of the legacy executor retain their existing request bytes.
	Operation string `json:"operation,omitempty"`
	// SessionRef is the acting session's external id, used ONLY for the FinOps
	// identity-budget tie-in and cost attribution (fail-open, not the security boundary).
	// F-01: it is NOT an entitlement input — the source-scope and model-access gates derive
	// the acting actor from the authenticated Principal.AgentIdentity, never from this
	// caller-supplied field, so it can never establish effective identity or borrow another
	// agent/session's source/model scope. Whether even the cost-attribution use should
	// require an authenticated binding is a pending product decision.
	SessionRef string `json:"session_ref,omitempty"`
	// Surface is the model.Gateway the call would be served through (e.g. "direct",
	// "bedrock-mantle", "foundry"). Optional: when set, the model-access surface
	// constraint is enforced at selection; when empty (unknown until the gateway is
	// chosen), the surface constraint is deferred to the in-band proxy that knows
	// the real surface — the model/workspace dimensions are still enforced here.
	Surface string `json:"surface,omitempty"`
}

// executeResponseDTO is the result: the effective decision (which target served and
// whether a fallback was used), the model output, and the token counts.
type executeResponseDTO struct {
	Decision     decisionDTO `json:"decision"`
	Served       targetDTO   `json:"served"`
	FallbackUsed bool        `json:"fallback_used"`
	Output       string      `json:"output"`
	InputTokens  int64       `json:"input_tokens"`
	OutputTokens int64       `json:"output_tokens"`
	Refusal      bool        `json:"refusal"`
}

// handleExecuteRouting resolves a stored routing policy AND executes the resolved
// target chain through the governed Executor, emitting the result's CostSample. It is
// DENY-CLOSED and GOVERNED: a FinOps enforcing budget at its cap denies the spend
// (402/429, Denial-of-Wallet) BEFORE any provider call, and with no executor
// wired it returns 503 (the control plane can resolve, not spend). The model output is
// returned to the caller but persisted nowhere here — only the redacted CostSample and
// forensic findings reach the ledger (docs/SECURITY-HARDENING.md,§3).
func (m *Module) handleExecuteRouting(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in TextExecutionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	result, _ := m.ExecuteText(r.Context(), mc, id, in)
	if result.retryAfter != nil {
		w.Header().Set("Retry-After", strconv.FormatInt(*result.retryAfter, 10))
	}
	writeJSON(w, result.StatusCode, result.body)
}

// ExecuteText is the supported governed operation for HTTP and in-process module
// consumers. Pass an engine-issued ModuleContext; selection, session attribution,
// and another route's resource or witness never authorize model execution.
func (m *Module) ExecuteText(ctx context.Context, mc api.ModuleContext, id model.ID, in TextExecutionInput) (result TextExecutionResult, operationErr error) {
	ctx, mc, err := mc.ForModule(ctx, m)
	if err != nil {
		return textAdmissionError(err)
	}
	var complete func(api.RecordedResult)
	mc, complete, err = mc.AdmitOperation(ctx, api.RecordedCall{
		Namespace: Namespace, Method: http.MethodPost, Pattern: "/routing-policies/{id}/execute",
		Permission: permRoutingExecute, Params: map[string]string{"id": id.String()},
	})
	if err != nil {
		return textAdmissionError(err)
	}
	defer func() {
		// In-process input has no HTTP wire bytes. Only the canonical typed
		// turn's digest and size enter the existing recording seam.
		encoded, _ := json.Marshal(in)
		digest := sha256.Sum256(encoded)
		status := result.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		complete(api.RecordedResult{Status: status, BodySHA256: digest[:], BodyBytes: int64(len(encoded))})
	}()
	if id.IsZero() {
		return textResponse(http.StatusBadRequest, errorBody("invalid id"))
	}
	if strings.TrimSpace(in.Input) == "" {
		return textResponse(http.StatusBadRequest, errorBody("input is required"))
	}
	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultExecuteMaxTokens
	}

	// 1) Resolve the decision (pure selection), exactly as /resolve does.
	var (
		resource       auth.ResourceAttrs
		dec            decisionDTO
		spec           routingSpec
		suspendedTiers []string
		notRouting     bool
		profile        ExecutionProfile
		profileErr     *executionProfileHTTPError
		// C2B: the policy's identity, retained from THIS read. The version is the row's
		// optimistic-concurrency counter and specDigest is a canonical digest of the
		// EFFECTIVE routing spec these gates are about to decide over — not the route
		// witness's PolicyVersion, which is a maximum of independent fact versions and
		// names no policy revision. Only the profiled Chat branch consumes them.
		policyVersion int64
		specDigest    [32]byte
	)
	err = mc.Data.View(ctx, func(sc store.Scope) error {
		p, err := sc.Policies().Get(ctx, id)
		if err != nil {
			return err
		}
		if sc.Tenant() != mc.Tenant || p.TenantID != mc.Tenant || p.ID != id {
			return store.ErrNotFound
		}
		resource = auth.ResourceFor(permRoutingExecute)
		resource.ID = p.ID.String()
		if p.Kind != policyKindRouting {
			notRouting = true
			return nil
		}
		spec = parseRoutingSpec(p.Spec)
		policyVersion = p.Version
		var derr error
		if specDigest, derr = routingSpecDigest(spec); derr != nil {
			return derr
		}
		profile, profileErr = m.resolveExecutionProfile(ctx, mc.Tenant, spec)
		if profileErr != nil {
			return nil
		}
		if profile.AdapterID == ExecutionAdapterDeepSeekText {
			suspendedTiers, err = suspendedEntitlementTiers(ctx, sc)
			return err
		}
		cat, err := buildCatalog(ctx, sc)
		if err != nil {
			return err
		}
		d, derr := spec.resolve(ctx, cat)
		if errors.Is(derr, modelrouter.ErrNoCandidate) {
			dec = unresolvedDecisionDTO(spec.Strategy)
			return nil
		}
		if derr != nil {
			return derr
		}
		dec = toDecisionDTO(d)
		suspendedTiers, err = suspendedEntitlementTiers(ctx, sc)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return textStoreError(err)
	}
	if notRouting {
		return textResponse(http.StatusNotFound, errorBody("not found"))
	}

	if !mc.Admits(ctx, permRoutingExecute, resource) {
		return textAdmissionError(api.ErrModuleOperationAdmission)
	}
	mc.Resource = resource

	// A profile pin selects one exact operation/protocol/target authority. Resolve it
	// before any spend gate. This lookup is immutable and I/O-free; C2A never touches
	// a credential, inspector, executor or network client.
	if profileErr != nil {
		return textProfileError(profileErr)
	}
	if profile.AdapterID == ExecutionAdapterDeepSeekText {
		dec, err = m.resolveAvailableProfile(ctx, mc.Tenant, spec, profile)
		if err != nil {
			return textStoreError(err)
		}
	}
	operation := in.Operation
	if !spec.hasExecutionProfile() && operation != "" {
		return textProfileError(executionProfileRequired())
	}
	if spec.hasExecutionProfile() {
		if operation == "" {
			operation = profile.Action
		}
		if operation != profile.Action || operation != ExecutionActionTextGenerate {
			return textProfileError(unsupportedExecutionOperation())
		}
		// A Chat profile pinned on the gateway endpoint cannot invoke a
		// non-streaming flag. Refuse here, before Chat, with the capability
		// named. This is not a target-binding mismatch.
		if spec.GatewayEndpoint != "" && textExecutionRefuses(spec.RequiredCapabilities) {
			return textResponse(http.StatusUnprocessableEntity, executeResponseDTO{Decision: decisionDTO{
				Resolved: false, Policy: dec.Policy,
				Reason:    notInvocableNote(spec.RequiredCapabilities),
				Fallbacks: []targetDTO{}, Chain: []targetDTO{},
			}})
		}
		if !dec.Resolved || !executionProfileMatchesTarget(profile, dec.Primary) {
			return textProfileError(profileBindingMismatch())
		}
		if in.Surface == "" {
			in.Surface = profile.Surface
		} else if in.Surface != profile.Surface {
			return textProfileError(profileBindingMismatch())
		}
	}
	if !dec.Resolved || dec.Primary == nil {
		// Nothing to execute — return the unresolved decision honestly (422), not a 500.
		return textResponse(http.StatusUnprocessableEntity, executeResponseDTO{Decision: dec, Served: targetDTO{}})
	}

	// 2) estate kill switch (deny-closed, FIRST among the spend gates): an
	// active estate-wide stop freezes routed execution entirely — resolve stays
	// readable, spend does not. A gate ERROR also denies: an unreadable stop
	// state never means "go" (the inverse of the budget gate's posture).
	if stop, serr := m.stopGate.Check(ctx, mc.Tenant); serr != nil {
		if m.log != nil {
			m.log.Error("models: kill-switch gate error; failing CLOSED (execute denied)")
		}
		return textResponse(http.StatusServiceUnavailable, errorBody("kill-switch state unreadable; execution denied (deny-closed)"))
	} else if stop.Stopped {
		return textResponse(http.StatusLocked, errorBody("denied: emergency stop active (estate kill switch "+stop.StopRef+"); re-enable requires dual-control"))
	}

	// 3) model-governance gate (deny-closed): a retired/deprecated/non-ZDR/
	// restricted-tier model must not be SPENT against any more than resolved to;
	// impermissible candidates are dropped from the chain (a permissible fallback
	// still serves) and only an all-denied chain blocks with 403.
	if status, denied := m.governanceDeniesRoute(spec, &dec, suspendedTiers); denied {
		return textResponse(status, executeResponseDTO{Decision: dec})
	}
	if spec.hasExecutionProfile() && !executionProfileMatchesTarget(profile, dec.Primary) {
		return textProfileError(profileBindingMismatch())
	}

	// F-01: for the SECURITY gates below (source-scope + model-access), the acting actor is
	// the AUTHENTICATED agent identity only (the token's Principal.AgentIdentity, set
	// server-side by the authenticator from the token's subject→agent mapping) — NEVER the
	// caller-supplied body session_ref. A caller-chosen reference must not establish
	// effective identity: honoring it would let a same-tenant token borrow another
	// agent/session's workspace + agent-group source/model ENTITLEMENTS (the confused deputy
	// the audit flagged). This mirrors the RAG query path (knowledge/query.go) and the
	// in-band proxy, which already derive the actor from Principal.AgentIdentity. With no
	// authenticated agent the actor is unresolved: bound sources and agent-group/workspace
	// grants are then deny-closed, which is correct.
	//
	// The body session_ref is NOT an entitlement input; it is retained ONLY for the FinOps
	// identity-budget tie-in and cost attribution below (fail-open, explicitly not the
	// security boundary — a trusted runtime authenticates once and declares the acting
	// session per request for spend accounting, not to gain access).
	actorRef := strings.TrimSpace(mc.Principal.AgentIdentity)

	// 3.5) source-scope gate (DENY-CLOSED, like the kill-switch): drop every
	// model the acting session/principal is out of scope for, BEFORE the budget check
	// and before the chain reaches the executor — a model the session may not use is
	// never served nor tried as a fallback. Only when EVERY candidate is out of scope
	// does it block (403). The actor scope VALUES are read from the stored agent named by
	// the AUTHENTICATED actorRef (never the body).
	if status, denied := m.scopeDeniesRoute(ctx, mc, &dec, actorRef); denied {
		return textResponse(status, executeResponseDTO{Decision: dec})
	}
	if spec.hasExecutionProfile() && !executionProfileMatchesTarget(profile, dec.Primary) {
		return textProfileError(profileBindingMismatch())
	}

	// 3.7) model-access governance gate (DENY-CLOSED). ORTHOGONAL to the
	// source-scope gate above: scope answers "is this model in the actor's workspace
	// binding?", model-access answers "is this SUBJECT (user/role/agent-group) granted
	// this model/model-group, in this workspace, on this surface?". A model the principal
	// is not granted is dropped from the chain (never served nor tried as a fallback);
	// only an all-denied chain blocks (403). Runs before the (fail-open) budget gate so a
	// security deny can never be bypassed by a FinOps outage.
	if status, denied := m.modelAccessDeniesRoute(ctx, mc, &dec, actorRef, in.Surface); denied {
		return textResponse(status, executeResponseDTO{Decision: dec})
	}
	if spec.hasExecutionProfile() && !executionProfileMatchesTarget(profile, dec.Primary) {
		return textProfileError(profileBindingMismatch())
	}

	// C2B: a fully valid Chat profile leaves the legacy path HERE, immediately before the
	// budget block, and never reaches the legacy Executor or its catalog fallback chain.
	// The branch hands the Chat composition the budget precheck as a CLOSURE over the
	// decision these gates just produced, so the precheck below still runs exactly once,
	// over exactly this primary and this session_ref — but only when the Chat sequence
	// reaches that stage, and after its own content gates. With no Chat executor wired the
	// port refuses with the same 503 C2A returned, before any policy, inspector, secret or
	// transport I/O.
	if spec.hasExecutionProfile() {
		// The text transport cannot carry a required capability. Refuse
		// before ExecuteChat. Requests with no required flag still enter.
		if chatTextRefuses(spec.RequiredCapabilities) {
			return textResponse(http.StatusUnprocessableEntity, executeResponseDTO{Decision: decisionDTO{
				Resolved: false, Policy: dec.Policy,
				Reason:    chatTextNote(spec.RequiredCapabilities),
				Fallbacks: []targetDTO{}, Chain: []targetDTO{},
			}})
		}
		return m.executeChatProfile(ctx, mc, in, dec, profile, id, policyVersion, specDigest)
	}

	// 4) FinOps budget gate (Denial-of-Wallet): deny the SPEND before any provider
	// call when an enforcing budget that scopes the selected primary is at its cap. The
	// identity-budget tie-in uses the body session_ref for cost attribution (fail-open FinOps
	// plumbing, NOT the security boundary — a security deny already ran above on the
	// authenticated actor, so this can never widen access).
	if status, denied := m.budgetDeniesRoute(ctx, mc, &dec, in.SessionRef); denied {
		return textResponse(status, executeResponseDTO{Decision: dec})
	}

	// Refuse a capability this text send cannot carry. The Chat branch has
	// already returned. The decision stays the catalog selection. The reason
	// names the capability and does not echo the request.
	if textExecutionRefuses(spec.RequiredCapabilities) {
		note := notInvocableNote(spec.RequiredCapabilities)
		if dec.Reason == "" {
			dec.Reason = note
		} else {
			dec.Reason += "; " + note
		}
		return textResponse(http.StatusNotImplemented, executeResponseDTO{Decision: dec})
	}

	// The executable chain comes from the (possibly governance-filtered) decision,
	// so a dropped candidate is never attempted as a fallback either.
	chain := make([]modelrouter.Target, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		chain = append(chain, fromTargetDTO(t))
	}

	// 5) Execute through the deny-closed governed Executor. SessionRef is the body ref used
	// only for cost attribution in the emitted CostSample (not an entitlement input).
	res, eerr := m.executor.Execute(ctx, ExecuteRequest{
		Tenant: mc.Tenant, Chain: chain, Input: in.Input, MaxTokens: maxTokens, SessionRef: in.SessionRef,
	})
	if errors.Is(eerr, ErrExecutionTargetUnsupported) {
		return textResponse(http.StatusUnprocessableEntity, errorBody(ErrExecutionTargetUnsupported.Error()))
	}
	if errors.Is(eerr, errNoExecutor) {
		return textResponse(http.StatusServiceUnavailable, errorBody("routing execution is not configured (deny-closed): no execution backend is wired — the control plane can resolve a routing decision but will not spend against a provider until an executor is provisioned"))
	}
	if eerr != nil {
		// A provider/transport failure: surface it as a bad gateway, never leak the
		// endpoint/credential the error may embed.
		if m.log != nil {
			m.log.Warn("models: routing execution failed")
		}
		return textResponse(http.StatusBadGateway, errorBody("routing execution failed against the resolved target"))
	}
	return textResponse(http.StatusOK, executeResponseDTO{
		Decision: dec, Served: toTargetDTO(res.Served), FallbackUsed: res.FallbackUsed,
		Output: res.Text, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, Refusal: res.Refusal,
	})
}

// scopeDeniesRoute applies the source-scope gate to the resolved chain: it drops
// every model the acting session/principal is out of scope for (the model→workspace/
// agent-group binding, decided by the grant engine + containment, model B) and
// PROMOTES the first in-scope survivor, mirroring governanceDeniesRoute so a dropped
// model is never tried as a fallback either. It is DENY-CLOSED: a gate error drops
// that candidate (an unreadable scope state must never authorize a model). Only when
// EVERY candidate is out of scope does it mutate the decision to resolved=false and
// return 403. sessionRef names the actor session whose stored workspace gives the actor
// scope (a caller-declared, route-gated assertion); empty ⇒ a bound model is denied
// unless the principal has a grant or tenant-wide RBAC.
func (m *Module) scopeDeniesRoute(ctx context.Context, mc api.ModuleContext, dec *decisionDTO, sessionRef string) (int, bool) {
	kept := make([]targetDTO, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		v, err := m.scopeGate.Allowed(ctx, mc.Tenant, ScopeQuery{
			Principal: mc.Principal, SessionRef: sessionRef, ProviderRef: t.ProviderRef, ModelRef: t.ModelRef,
		})
		if err != nil {
			if m.log != nil {
				m.log.Warn("models: scope gate error; dropping candidate (deny-closed)", "model_ref", t.ModelRef)
			}
			continue // deny-closed: an unreadable scope state never authorizes the model
		}
		if v.Allowed {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(dec.Chain) {
		return 0, false // every candidate is in scope
	}
	if len(kept) > 0 {
		primary := kept[0]
		note := fmt.Sprintf("source scope filtered %d candidate(s)", len(dec.Chain)-len(kept))
		dec.Primary, dec.Fallbacks, dec.Chain = &primary, kept[1:], kept
		if dec.Reason == "" {
			dec.Reason = note
		} else {
			dec.Reason += "; " + note
		}
		return 0, false
	}
	if m.log != nil {
		m.log.Info("models: routing execution denied — every candidate model is out of the session's scope")
	}
	*dec = decisionDTO{
		Resolved: false, Policy: dec.Policy,
		Reason:    "routing denied: every candidate model is out of the session's scope",
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	return http.StatusForbidden, true
}

// modelAccessDeniesRoute applies the model-access decision (modelaccessgate.go) to
// the resolved chain: it drops every model the acting principal is NOT granted (by user/
// role/agent-group, in the actor's workspace, on the request surface) and PROMOTES the
// first granted survivor, mirroring scopeDeniesRoute so a dropped model is never tried as
// a fallback either. It is DENY-CLOSED: a load/resolve error denies the WHOLE chain (an
// unreadable governance state must never authorize a model — the per-candidate decision
// is pure once the context is resolved, so the only error is the shared resolve). A tenant
// with no model-access grants (or a superadmin) is not governed: the context is nil and
// the gate is a no-op. surface is the request's declared surface ("" ⇒ enforced in-band).
func (m *Module) modelAccessDeniesRoute(ctx context.Context, mc api.ModuleContext, dec *decisionDTO, sessionRef, surface string) (int, bool) {
	c, err := m.modelAccessContext(ctx, mc.Tenant, mc.Principal, sessionRef)
	if err != nil {
		if m.log != nil {
			m.log.Warn("models: model-access context error; denying chain (deny-closed)")
		}
		*dec = decisionDTO{
			Resolved: false, Policy: dec.Policy,
			Reason:    "routing denied: model-access governance state is unreadable (deny-closed)",
			Fallbacks: []targetDTO{}, Chain: []targetDTO{},
		}
		return http.StatusForbidden, true
	}
	if c == nil {
		return 0, false // not governed (no grants / superadmin)
	}
	kept := make([]targetDTO, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		if c.decide(t.ModelRef, surface).Allowed {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(dec.Chain) {
		return 0, false // every candidate is granted
	}
	if len(kept) > 0 {
		primary := kept[0]
		note := fmt.Sprintf("model-access filtered %d candidate(s)", len(dec.Chain)-len(kept))
		dec.Primary, dec.Fallbacks, dec.Chain = &primary, kept[1:], kept
		if dec.Reason == "" {
			dec.Reason = note
		} else {
			dec.Reason += "; " + note
		}
		return 0, false
	}
	if m.log != nil {
		m.log.Info("models: routing execution denied — principal is not granted any candidate model")
	}
	*dec = decisionDTO{
		Resolved: false, Policy: dec.Policy,
		Reason:    "routing denied: the principal is not granted any candidate model on this workspace/surface",
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	return http.StatusForbidden, true
}

// modelAccessPreviewDeniesRoute is the /resolve model-access filter. /resolve has
// NO acting session, so it applies ONLY the dimensions decidable without one — a tenant-
// wide forbid on the authenticated principal's user/role identity and the user/role
// allow-list confinement (maContext.previewVerdict) — and PROMOTES the first surviving
// candidate, mirroring modelAccessDeniesRoute. A dropped model is one denied under EVERY
// possible session; the actor-dependent dimensions (workspace, agent-group, surface) are
// DEFERRED to the authoritative execute/in-band decision, so this never hides a
// model the principal could legitimately use. DENY-CLOSED on a load error; a tenant with
// no grants (or a superadmin) is not governed and this is a no-op.
func (m *Module) modelAccessPreviewDeniesRoute(r *http.Request, mc api.ModuleContext, dec *decisionDTO) (int, bool) {
	c, err := m.modelAccessContext(r.Context(), mc.Tenant, mc.Principal, "")
	if err != nil {
		if m.log != nil {
			m.log.Warn("models: model-access preview context error; denying chain (deny-closed)")
		}
		*dec = decisionDTO{
			Resolved: false, Policy: dec.Policy,
			Reason:    "routing denied: model-access governance state is unreadable (deny-closed)",
			Fallbacks: []targetDTO{}, Chain: []targetDTO{},
		}
		return http.StatusForbidden, true
	}
	if c == nil {
		return 0, false // not governed (no grants / superadmin)
	}
	kept := make([]targetDTO, 0, len(dec.Chain))
	for _, t := range dec.Chain {
		if c.previewVerdict(t.ModelRef).Allowed {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(dec.Chain) {
		return 0, false // every candidate survives the preview
	}
	if len(kept) > 0 {
		primary := kept[0]
		note := fmt.Sprintf("model-access preview filtered %d candidate(s); workspace/agent-group/surface dimensions are enforced at execute", len(dec.Chain)-len(kept))
		dec.Primary, dec.Fallbacks, dec.Chain = &primary, kept[1:], kept
		if dec.Reason == "" {
			dec.Reason = note
		} else {
			dec.Reason += "; " + note
		}
		return 0, false
	}
	if m.log != nil {
		m.log.Info("models: routing resolve denied — the principal is forbidden or not granted any candidate model (preview)")
	}
	*dec = decisionDTO{
		Resolved: false, Policy: dec.Policy,
		Reason:    "routing denied: the principal is forbidden or not granted any candidate model (model-access preview; workspace/agent-group/surface enforced at execute)",
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	return http.StatusForbidden, true
}

// toDecisionDTO maps a resolved modelrouter.Decision to the API decision DTO (primary
// + fallback chain). Shared by /resolve and /execute so the two never drift.
func toDecisionDTO(dec modelrouter.Decision) decisionDTO {
	out := decisionDTO{
		Resolved: true, Policy: string(dec.Policy), Reason: dec.Reason,
		Fallbacks: []targetDTO{}, Chain: []targetDTO{},
	}
	primary := toTargetDTO(dec.Primary)
	out.Primary = &primary
	for _, t := range dec.Fallbacks {
		out.Fallbacks = append(out.Fallbacks, toTargetDTO(t))
	}
	for _, t := range dec.Chain() {
		out.Chain = append(out.Chain, toTargetDTO(t))
	}
	return out
}

// unresolvedDecisionDTO is the no-candidate decision (no governed model satisfies the
// policy). Shared by /resolve and /execute.
func unresolvedDecisionDTO(strategy string) decisionDTO {
	return decisionDTO{
		Resolved: false, Policy: strategy, Fallbacks: []targetDTO{}, Chain: []targetDTO{},
		Reason: "no governed model satisfies the policy",
	}
}

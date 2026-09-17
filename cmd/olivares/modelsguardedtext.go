// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// modelsguardedtext.go is D01-C2B's ONE concrete synchronous Chat operation: the composition
// the models module's ChatExecutor port calls once a routing policy pinned an execution
// profile and every existing handler gate cleared the request.
//
// ⛔ THE ORDER IS THE AUTHORITY. Not the request struct, not a digest, not the receipt.
// Nothing this file computes is a bearer permit a later caller could present: the plan, the
// policy snapshot, the refs, the effect binding and the anchor receipt are PRIVATE LOCALS of
// one method invocation and are never returned. There is deliberately no exported
// Authorize → permit → Dispatch → Finalize protocol, because a permit is a thing that can be
// carried to a different effect, and the whole point of the binding discipline is that it
// cannot.
//
// The sequence, and every step's reason for being where it is:
//
//	 1. entry + registry snapshot — decide nothing against state a caller supplied
//	 2. C0 prepare ONCE, purely — a size failure must be able to terminate before any
//	    inspector or ledger call, and the prepared digest must exist BEFORE the guard that
//	    would otherwise be asked for a digest only computable after it returns
//	 3. governance policy ONCE, deny-closed — the FailOpen knob lives INSIDE the policy, so
//	    a policy that cannot be read cannot license failing open
//	 4. local posture (breaker, residency, context policy, ceilings)
//	 5. input content: core DLP, then the shared inspector
//	 6. the existing budget precheck, exactly once
//	 7. effect binding + intent anchor
//	 8. registry revalidation + credential resolution
//	 9. C1, at most once
//	10. observe, then inspect the output before a byte of it is released
//	11. finalize on EVERY post-intent exit
//
// The upstream egress is step 9 and only step 9. Nothing before it leaves the process
// except the tenant's own decision-plane reads, so a request denied by the content gates is
// never exfiltrated through a sizing side channel — the defect fixed in the Messages
// proxy, not repeated here.

const (
	// chatEffectDomain binds the AUTHORIZED effect: who, under which authority, against
	// which immutable target, over exactly which prepared bytes, under which governance.
	//
	// v2 (G1): the preimage additionally binds the FORMAT of the route witness digest it
	// carries, immediately after the witness presence bit, and the generic witness digest
	// itself changed codec (auth.RouteEvidenceDigestFormat). Every other dimension and its
	// order are byte-for-byte the v1 codec. Stored v1 commitments stay what they are: they
	// are neither reinterpreted under this domain nor resealed, and a label on a new record
	// does not supply the preimage an old record never stored.
	chatEffectDomain = "olivares.model-gateway-chat.effect.v2"
	// chatObservationDomain binds the DECODED observation. It is deliberately NOT called a
	// response digest: C1 returns a decoded ChatTextResponse, not the upstream octets, so
	// no wire checksum exists to publish and none is invented. It stays v1: its structure
	// and semantics are unchanged by G1.
	chatObservationDomain = "olivares.model-gateway-chat.observation.v1"
	// chatOutcomeDomain binds the OUTCOME leg. v2 (G1) prepends this domain to the outcome
	// preimage, which otherwise keeps its entire v1 sequence, so an outcome payload hash
	// names the codec that produced it instead of relying on the action string alone.
	chatOutcomeDomain = "olivares.model-gateway-chat.outcome.v2"
	// chatDigestAlgorithm is the software-owned algorithm label the evidence metadata
	// records beside each digest it publishes. It is fixed text this file owns, never
	// provider or caller data.
	chatDigestAlgorithm = "sha256"
	// chatRouteEvidenceAbsent is the software-owned label recorded when the shared
	// presence decision found no route witness to bind.
	chatRouteEvidenceAbsent = "none"

	chatIntentAction  = "models.chat.authorized"
	chatOutcomeAction = "models.chat.recorded"
	// chatExecutionKind is the audit target kind and the inspection subject kind.
	chatExecutionKind model.Kind = "models.chat.execution"
	// chatRoutePermission/chatRouteAction are the FIXED route authority this operation runs
	// under. They are bound into the effect so the evidence records which door was used,
	// not merely that some door was.
	chatRoutePermission = "models:routing:admin"
	chatRouteAction     = "models.routing.execute"
	// chatOutcomeBound is the independent bound on the ONE outcome-recording attempt. The
	// caller going away must not cancel the record of an effect that already happened.
	chatOutcomeBound = 5 * time.Second
	// chatSignalSource labels the bus observations this path emits, distinctly from the
	// inline Messages PEP: two governed paths that published under one source would be
	// indistinguishable to anything reading the stream.
	chatSignalSource = "models_chat"
)

// modelsChatExecutor is the concrete adapter. Every field is a dependency the composition
// root late-binds before HTTP serving; none is constructed here and none is optional in the
// sense of "silently absent" — ready() states exactly which ones must exist.
type modelsChatExecutor struct {
	registry      *modelGatewayProfileRegistry
	store         store.Store
	policy        proxyPolicySource
	contextPolicy contextPolicyResolver
	residency     *residency.Registry
	secrets       *secret.Resolver
	// inspector is the OPTIONAL enterprise content firewall. It is wired INDEPENDENTLY of
	// the Messages listener: a Chat-only boot with a configured firewall inspects, and a
	// configured inspector is never silently absent because this path exists.
	inspector      contentInspector
	approvals      *approvalBridge
	bus            observationSink
	circuitBreaker circuitBreakerEngine
	// httpClient is a COMPOSITION dependency for enterprise TLS/proxy behavior, handed to
	// C1 unchanged. It is not an alternate executor: C1 copies it, disables redirects and
	// the cookie jar in that copy, and calls Do at most once. A custom RoundTripper may do
	// its own I/O and must be qualified separately; supplying one guarantees nothing.
	httpClient *http.Client
	clock      func() time.Time
	log        *slog.Logger
}

var _ models.ChatExecutor = (*modelsChatExecutor)(nil)

// ready reports whether every dependency this operation cannot run correctly without is
// bound. A half-bound adapter must refuse rather than skip a gate, so a storeless or
// collector process keeps Chat unavailable instead of serving it ungoverned.
func (x *modelsChatExecutor) ready() bool {
	return x != nil && x.registry != nil && x.store != nil && x.policy != nil &&
		x.contextPolicy != nil && x.secrets != nil && x.clock != nil
}

// chatPlan is the PRIVATE per-attempt state. It never leaves this file, is never returned
// to the module, and is never persisted: a struct that escaped would be a permit.
type chatPlan struct {
	in         models.ChatExecutionRequest
	entry      modelGatewayProfileConfig
	prepared   modelprovider.PreparedChatTextRequest
	requestRef string
	attemptRef string
	pol        inferenceproxy.ProxyPolicy
	polDigest  [32]byte
	pin        string
	residencyGap,
	contextGap bool
	ctxPol       knowledge.EffectivePolicy
	inputVerdict claudeapi.ContentInspectionDecision
	budgetStatus int
	budgetDenied bool
	effect       [32]byte
	binding      sdk.EvidenceBinding
}

// ExecuteChat runs the whole ordered composition for ONE attempt.
//
// The result is named so the deferred finalization can record the state the operation
// actually reached and have that state reach the caller. An error never erases it: an
// attempted dispatch stays attempted and reported usage stays reported, because the caller
// of a 429 that followed a real dispatch must not be able to read it as "nothing happened".
func (x *modelsChatExecutor) ExecuteChat(
	ctx context.Context, in models.ChatExecutionRequest, budget models.ChatBudgetCheck,
) (res models.ChatExecutionResult, err error) {
	res = models.ChatExecutionResult{
		DispatchState: models.ChatDispatchNotAttempted, CompletionState: models.ChatCompletionNotSent,
		UsageStatus: models.ChatUsageUnknown, IntentDisposition: models.ChatIntentRefused,
		OutcomeDisposition: models.ChatOutcomeNotRequired,
		BudgetAssurance:    models.ChatBudgetAssuranceDevelopmentPrecheck,
	}
	if ctx == nil {
		return res, models.NewChatExecutionError(models.ChatErrInternal)
	}
	if !x.ready() {
		return res, models.NewChatExecutionError(models.ChatErrExecutionNotReady)
	}
	if ctx.Err() != nil {
		// A cancelled entry stops here: no callback, no policy read, no effect.
		return res, models.NewChatExecutionError(models.ChatErrRequestCancelled)
	}

	// The profile's timeout now bounds the COMPLETE cooperative operation, not only the
	// transport. C1 still applies its own; the earlier remaining deadline wins, and an
	// earlier caller/HTTP deadline prevails over both. No goroutine is spawned to
	// manufacture a hard guarantee over a dependency that ignores context — a bound this
	// operation cannot enforce is not claimed.
	ctx, cancelOperation := context.WithTimeout(ctx, in.Profile.Timeout)
	defer cancelOperation()

	plan := &chatPlan{in: in}

	// --- 1. entry validation and the immutable registry snapshot ---------------------
	if failure := x.validateEntry(ctx, plan); failure != nil {
		return res, failure
	}

	// --- 2. C0 prepared exactly once, purely -----------------------------------------
	if failure := x.prepare(plan); failure != nil {
		return res, failure
	}
	res.RequestRef, res.AttemptRef = plan.requestRef, plan.attemptRef

	// --- 3. governance policy, once, deny-closed -------------------------------------
	pol, perr := x.policy.Policy(ctx, in.Tenant)
	if perr != nil {
		// The FailOpen knob is INSIDE the policy that could not be read, and the zero
		// ProxyPolicy has every gate off. Neither may be substituted here.
		x.warn("models-chat: governance policy unreadable; denying (deny-closed)", "err", perr)
		return res, models.NewChatExecutionError(models.ChatErrGovernanceUnavailable)
	}
	plan.pol, plan.polDigest = pol, pol.EvidenceDigest()

	// --- 4. additional local posture --------------------------------------------------
	if failure := x.applyPosture(ctx, plan); failure != nil {
		return res, failure
	}

	// --- 5. input content: core DLP first, then the shared inspector -------------------
	if failure := x.inspectInput(ctx, plan); failure != nil {
		return res, failure
	}

	// --- 6. the existing budget precheck, exactly once ---------------------------------
	if failure := x.admit(ctx, plan, budget); failure != nil {
		return res, failure
	}

	// --- 7. close the effect binding and anchor the intent -----------------------------
	plan.effect = chatEffectDigest(plan)
	plan.binding = sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(plan.attemptRef),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(plan.effect[:])),
	}
	disposition, failure := x.anchorChatIntent(ctx, plan)
	res.IntentDisposition = disposition
	if failure != nil {
		return res, failure
	}

	// From here the intent is authorized, so EVERY exit finalizes — including the ones
	// that never dispatch. A pre-dispatch failure records not_sent; it does not skip the
	// record. The recording runs before this function returns, so the module cannot
	// release output ahead of it.
	var observation *chatObservation
	defer func() { x.recordChatOutcome(ctx, plan, &res, observation) }()

	// --- 8. revalidate the exact registry entry, then resolve the credential -----------
	token, failure := x.resolveCredential(ctx, plan)
	if failure != nil {
		return res, failure
	}

	// --- 9. C1, at most once -----------------------------------------------------------
	response, failure := x.dispatch(ctx, plan, token, &res)
	if failure != nil {
		return res, failure
	}
	// The decoded observation is summarized ONCE, here, and the same value is what the
	// output gates decide on and what the outcome records. Deriving the mismatch twice —
	// once to withhold and once to record — is how a withheld response ends up recorded as
	// a clean one.
	observation = chatObservationFor(response, plan.in.Profile.ModelRef)

	// --- 10. observe, then inspect the output before releasing it ----------------------
	return res, x.observe(ctx, plan, &res, response, observation)
}

// chatObservation is the decoded observation's SAFE projection: its digest, the model the
// gateway said it used and whether that model is the one the profile pinned.
//
// The model string is provider-controlled data, so it is a labelled OBSERVATION and never
// an attribution: nothing downstream may read observedModel as "the model that ran". It is
// kept beside the digest because the digest alone cannot be reconciled — a reader holding
// only a hash can confirm a guess, not answer "which model answered".
type chatObservation struct {
	digest        [32]byte
	observedModel string
	mismatch      bool
}

// chatObservationFor summarizes one decoded response against the pinned model. It is pure.
func chatObservationFor(r modelprovider.ChatTextResponse, pinned string) *chatObservation {
	return &chatObservation{
		digest: chatObservationDigest(r), observedModel: r.Model, mismatch: r.Model != pinned,
	}
}

// chatObservedModelMax bounds the observed model string this operation is willing to COPY
// into the tamper-evident ledger. The value is chosen by the remote gateway and arrives
// inside a body only bounded by MaxResponseBytes, so publishing it unbounded would let a
// broken or hostile upstream drive the size of an audit record. The complete string is
// still committed by the observation digest, which is what a reconciliation compares
// against; the published prefix is for reading.
const chatObservedModelMax = 128

// --- 1. entry ------------------------------------------------------------------------

// validateEntry refuses anything it cannot prove, then re-reads the profile from the
// immutable registry and compares the COMPLETE scalar snapshot. Selection is by
// (tenant, ref, revision): TransportKey is an opaque cache label, never an authority and
// never a selector, so a value that only matched it would select nothing.
func (x *modelsChatExecutor) validateEntry(ctx context.Context, plan *chatPlan) error {
	in := plan.in
	switch {
	case in.Tenant.IsZero(), in.Principal.Kind == "",
		in.PolicyID.IsZero(), in.PolicyVersion <= 0, in.PolicySpecDigest == [32]byte{}:
		return models.NewChatExecutionError(models.ChatErrInternal)
	case in.MaxTokens <= 0, strings.TrimSpace(in.Input) == "":
		return models.NewChatExecutionError(models.ChatErrRequestInvalid)
	}
	p := in.Profile
	if p.Tenant != in.Tenant || p.Action != models.ExecutionActionTextGenerate ||
		p.Protocol != models.ExecutionProtocolChatTextV1 ||
		p.AdapterID != models.ExecutionAdapterModelProviderChat ||
		p.AdapterVersion != models.ExecutionAdapterVersion1 ||
		p.Ref == "" || p.Revision == "" || p.ProviderRef == "" || p.ModelRef == "" ||
		p.Endpoint == "" || p.Surface == "" || p.InferenceGeo == "" ||
		p.CredentialAudience == "" || p.TransportKey == "" ||
		(p.AuthScheme != "bearer" && p.AuthScheme != "none") ||
		p.MaxRequestBytes <= 0 || p.MaxResponseBytes <= 0 || p.Timeout <= 0 {
		return models.NewChatExecutionError(models.ChatErrProfileUnavailable)
	}
	if in.Target.ProviderRef != p.ProviderRef || in.Target.ModelRef != p.ModelRef ||
		(in.Target.ViaGateway && in.Target.Endpoint != p.Endpoint) {
		return models.NewChatExecutionError(models.ChatErrProfileUnavailable)
	}
	entry, snapshot, ok := x.registrySnapshot(ctx, in)
	if !ok || snapshot != p {
		return models.NewChatExecutionError(models.ChatErrProfileUnavailable)
	}
	plan.entry = entry
	return nil
}

// registrySnapshot re-reads the exact immutable record and its projected public snapshot.
// The record's CredentialRef never leaves this package and is never projected into the
// models module; only the audience — public authority metadata — is.
func (x *modelsChatExecutor) registrySnapshot(ctx context.Context, in models.ChatExecutionRequest) (modelGatewayProfileConfig, models.ExecutionProfile, bool) {
	entry, ok := x.registry.entry(in.Tenant, in.Profile.Ref, in.Profile.Revision)
	if !ok {
		return modelGatewayProfileConfig{}, models.ExecutionProfile{}, false
	}
	snapshot, err := x.registry.ResolveExecutionProfile(ctx, in.Tenant, in.Profile.Ref, in.Profile.Revision)
	if err != nil {
		return modelGatewayProfileConfig{}, models.ExecutionProfile{}, false
	}
	return entry, snapshot, true
}

// entry returns the immutable private record for one exact key. It lives here rather than
// beside the loader so C2A's registry file stays untouched, and it is deliberately
// unexported: there is no new tenant-secret lookup API, only in-package access to a record
// this package already holds.
func (r *modelGatewayProfileRegistry) entry(tenant model.TenantID, ref, revision string) (modelGatewayProfileConfig, bool) {
	if r == nil {
		return modelGatewayProfileConfig{}, false
	}
	p, ok := r.profiles[modelGatewayProfileKey{tenant: tenant, ref: ref, revision: revision}]
	return p, ok
}

// --- 2. prepare ------------------------------------------------------------------------

// prepare runs C0 exactly once and mints the server refs.
//
// It runs BEFORE the content guard on purpose. C0 is pure — no authorization, no I/O — so
// running it early grants nothing, and it resolves the ordering contradiction the earlier
// draft had: a guard cannot be asked for a prepared digest that only exists after that same
// guard returns. Because no gate in this text slice rewrites the input or the token bound
// (a required rewrite is an explicit deny, not a silent edit), there is no legitimate
// post-guard re-marshal and the prepared bytes at C1 are these bytes.
func (x *modelsChatExecutor) prepare(plan *chatPlan) error {
	prepared, err := modelprovider.PrepareChatTextRequest(modelprovider.ChatTextRequest{
		Model: plan.in.Profile.ModelRef, Input: plan.in.Input, MaxCompletionTokens: plan.in.MaxTokens,
	})
	if err != nil || prepared.IsZero() {
		return models.NewChatExecutionError(models.ChatErrRequestInvalid)
	}
	if len(prepared.Bytes()) > plan.in.Profile.MaxRequestBytes {
		return models.NewChatExecutionError(models.ChatErrRequestTooLarge)
	}
	plan.prepared = prepared
	// Server-minted and unrelated to any caller input: not the body, not session_ref, not
	// an idempotency header. A client cannot choose the identity its own effect is
	// recorded under.
	plan.requestRef, plan.attemptRef = newRequestRef(), newRequestRef()
	return nil
}

// --- 4. posture --------------------------------------------------------------------------

// applyPosture runs the additional local gates this path owns. The handler's own gates —
// kill switch, source scope, model access, governance — already ran and are NOT re-decided
// here; ProxyPolicy's gate flags govern only the gates below and can never switch one of
// those off.
func (x *modelsChatExecutor) applyPosture(ctx context.Context, plan *chatPlan) error {
	in, pol := plan.in, plan.pol

	// The circuit breaker, when configured, is consulted on the AUTHENTICATED agent
	// identity — the key the engine actually writes state under. Deriving it from the
	// attribution session_ref would ask about a key nothing ever wrote, so a tripped
	// breaker would be invisible while looking checked.
	if denied, reason := circuitBreakerGateCheck(ctx, x.circuitBreaker, in.Tenant, strings.TrimSpace(in.Principal.AgentIdentity)); denied {
		x.info("models-chat: denied by the circuit breaker", "reason", reason)
		return models.NewChatExecutionError(models.ChatErrCircuitOpen)
	}

	if x.residency != nil && x.residency.Enforces() {
		pin, rerr := x.orgPin(ctx, in.Tenant)
		switch {
		case rerr != nil && !pol.FailOpen:
			return models.NewChatExecutionError(models.ChatErrResidencyDenied)
		case rerr != nil:
			// A READ FAULT may follow the policy's own explicit fail-open posture, and the
			// gap is NAMED: it is bound into the effect digest and published, never silent.
			plan.residencyGap = true
			x.warn("models-chat: residency pin unreadable; failing OPEN per tenant config (evidence gap)", "err", rerr)
			x.publishFinding(ctx, in.Tenant, sdkmodel.FindingReport{
				Kind: "models_chat_failed_open", Severity: sdkmodel.SeverityHigh,
				SubjectKind: string(chatExecutionKind), SubjectRef: "residency",
				Title:      "Governed Chat failed OPEN on a residency decision-plane outage (per tenant fail_open)",
				DetailHash: hexSHA("failed_open|residency|" + in.Profile.Ref),
			})
		default:
			plan.pin = pin
		}
	}
	// A DEFINITIVE incompatibility always denies: the fail-open knob covers a read outage,
	// never a clear violation. A pinned tenant whose profile declares a global or unknown
	// geo is incompatible, because neither can be proven in-region.
	if pol.GateResidency && plan.pin != "" && !residency.InferenceGeoCompatible(plan.pin, in.Profile.InferenceGeo) {
		return models.NewChatExecutionError(models.ChatErrResidencyDenied)
	}

	if pol.GateContextWindow && x.contextPolicy != nil {
		ctxPol, cperr := x.contextPolicy.Apply(ctx, in.Tenant, knowledge.ContextPolicyQuery{
			Principal: in.Principal, Model: in.Profile.ModelRef,
		})
		switch {
		case cperr != nil && !pol.FailOpen:
			return models.NewChatExecutionError(models.ChatErrGovernanceUnavailable)
		case cperr != nil:
			plan.contextGap, plan.ctxPol = true, knowledge.EffectivePolicy{}
			x.warn("models-chat: context policy unreadable; failing OPEN per tenant config (evidence gap)", "err", cperr)
			x.publishFinding(ctx, in.Tenant, sdkmodel.FindingReport{
				Kind: "models_chat_failed_open", Severity: sdkmodel.SeverityHigh,
				SubjectKind: string(chatExecutionKind), SubjectRef: "context_policy",
				Title:      "Governed Chat failed OPEN on a context-policy decision-plane outage (per tenant fail_open)",
				DetailHash: hexSHA("failed_open|context_policy|" + in.Profile.Ref),
			})
		case ctxPol.Deny:
			return models.NewChatExecutionError(models.ChatErrContextPolicyDenied)
		default:
			plan.ctxPol = ctxPol
		}
	}
	// A hard context-token ceiling is a KNOWN constraint this operation cannot verify: C0
	// carries no token count and asking the provider to count would be the very pre-forward
	// egress step 9 is supposed to be. Inventing a tokenizer bound would be a guess wearing
	// a limit's clothes, and quietly dropping the constraint would be worse than both.
	if plan.ctxPol.MaxContextTokens > 0 {
		return models.NewChatExecutionError(models.ChatErrContextLimitUnverifiable)
	}
	// Required redaction is INCOMPATIBLE with a path that forwards the caller's text
	// verbatim; it is refused, not ignored.
	if plan.ctxPol.RedactionRequired {
		return models.NewChatExecutionError(models.ChatErrRedactionUnsupported)
	}

	// Of the three per-request ceilings only max_tokens has a counterpart in a text.v1
	// request: it declares no tools and no output_config, so a tool-use or task-budget
	// ceiling has nothing to bind to here. In ENFORCE mode a violation denies rather than
	// clamping, because clamping silently changes the truncation semantics the caller sees.
	if pol.Ceilings.MaxTokens > 0 && in.MaxTokens > pol.Ceilings.MaxTokens {
		mode := "observe"
		if pol.Ceilings.Enforce {
			mode = "enforced"
		}
		x.publishFinding(ctx, in.Tenant, sdkmodel.FindingReport{
			Kind: "models_chat_request_ceiling", Severity: sdkmodel.SeverityMedium,
			SubjectKind: string(chatExecutionKind), SubjectRef: in.Profile.ModelRef,
			Title:      "Governed Chat request exceeds the tenant per-request ceiling (" + mode + ")",
			DetailHash: hexSHA(in.Profile.ModelRef + "|" + mode + "|max_tokens"),
			OWASPLLM:   []string{"LLM10:2025"},
		})
		if pol.Ceilings.Enforce {
			return models.NewChatExecutionError(models.ChatErrRequestCeilingExceeded)
		}
	}
	return nil
}

// orgPin reads the tenant's residency pin through the existing store, exactly as the
// Messages path does.
func (x *modelsChatExecutor) orgPin(ctx context.Context, tenant model.TenantID) (string, error) {
	var pin string
	err := x.store.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		pin = strings.TrimSpace(org.DataRegion)
		return nil
	})
	return pin, err
}

// --- 5. input content ---------------------------------------------------------------------

// inspectInput builds ONE collected-content channel holding exactly the bytes C0 prepared —
// not a normalized, trimmed or re-encoded copy — runs the deterministic core DLP, and only
// then the optional inspector.
//
// The ordering matters in one direction: on a core DLP deny the inspector is NOT called, so
// content the tenant's own policy already refused is not handed to an add-on. In the other
// direction an inspector denial or non-decision cannot be made clean by anything later: it
// returns here and no subsequent gate is consulted.
func (x *modelsChatExecutor) inspectInput(ctx context.Context, plan *chatPlan) error {
	in, pol := plan.in, plan.pol
	content := claudeapi.CollectedContent{
		Channels: []claudeapi.ContentChannel{{Kind: "text", Role: "user", Text: in.Input, Scannable: true}},
		Texts:    []string{in.Input},
	}
	if pol.GateDLPRequest && pol.DLPEnabled() {
		denied := pol.DLPDecide(classifyText(content.Texts))
		if content.Unscanned && pol.DLPUnscannedDenied() {
			denied = append(denied, dlpUnscannedClass)
		}
		if len(denied) > 0 {
			x.publishFinding(ctx, in.Tenant, sdkmodel.FindingReport{
				Kind: "models_chat_dlp_blocked", Severity: sdkmodel.SeverityHigh,
				SubjectKind: string(chatExecutionKind), SubjectRef: in.Profile.ModelRef,
				Title:      "Governed Chat input blocked by DLP policy",
				DetailHash: hexSHA(in.Profile.ModelRef + "|input|" + strings.Join(denied, ",")),
				OWASPLLM:   []string{"LLM02:2025"},
			})
			return models.NewChatExecutionError(models.ChatErrContentDenied)
		}
	}
	verdict := x.inspection().Inspect(ctx, x.subject(in.Profile), in.Tenant,
		in.Principal.Actor(), strings.TrimSpace(in.Principal.AgentIdentity), x.unbindableAgent(in.Principal),
		claudeapi.InspectDirectionRequest, in.Profile.ModelRef, content)
	plan.inputVerdict = verdict
	if !verdict.Forward {
		// Deny-closed on ANY non-forward. The connector's zero value means "no decision",
		// and for a security gate an absent decision is not a clean pass.
		return models.NewChatExecutionError(models.ChatErrInspectionDenied)
	}
	return nil
}

// subject names what this path's published observations are ABOUT: its own execution kind
// and the PROFILE's provider and surface, never Anthropic's.
func (x *modelsChatExecutor) subject(p models.ExecutionProfile) inspectionSubject {
	return inspectionSubject{
		Kind: string(chatExecutionKind), ProviderRef: p.ProviderRef,
		Surface: sdkmodel.Gateway(p.Surface),
	}
}

// inspection binds the shared service. The approval callback targets THIS subject kind, so
// a held detection here never opens an approval that looks like a Messages one.
func (x *modelsChatExecutor) inspection() contentInspectionService {
	return contentInspectionService{
		inspector: x.inspector, publish: x.publish, approve: x.openChatInspectionApproval,
		clock: x.clock,
	}
}

// openChatInspectionApproval opens (or idempotently reuses) a governed approval for a held
// detection through the existing bridge. It is best-effort and nil-safe, and it NEVER
// resumes this synchronous call: a denied request does not become permitted because an
// approval is granted later — a future caller must make a new governed request.
func (x *modelsChatExecutor) openChatInspectionApproval(ctx context.Context, tenant model.TenantID, actor, direction string, intent *claudeapi.ContentInspectionApprovalIntent) {
	if x.approvals == nil || intent == nil {
		return
	}
	requestedBy := firstNonEmpty(actor, model.ActorSystem)
	if _, _, _, err := x.approvals.gateOnce(ctx, tenant, intent.Action, string(chatExecutionKind), intent.Subject, intent.PlanHash, intent.Reason, requestedBy); err != nil {
		x.warn("models-chat: content-inspection approval intent could not be opened (verdict stands)", "err", err, "direction", direction)
	}
}

// unbindableAgent mirrors the Messages formula exactly: a raw API token with no
// authenticated NHI binding is explicitly unbindable, so agent-scoped policy cannot
// silently fall through to a broader tenant or global rule.
func (x *modelsChatExecutor) unbindableAgent(p auth.Principal) bool {
	return p.Kind == auth.KindToken && strings.TrimSpace(p.AgentIdentity) == ""
}

// --- 6. admission --------------------------------------------------------------------------

// admit consults the handler's budget closure exactly once.
//
// ⛔ WHAT COMES BACK IS THE EXISTING FAIL-OPEN PRECHECK AND NOTHING MORE. denied=false does
// not mean a reservation was taken, does not mean the ledger is healthy, and does not mean a
// hard cap was honoured — the gate fails open by contract, so a FinOps outage produces the
// same answer as an ample budget. That is why the result carries the explicit development
// assurance and why no default-allow reservation callback is synthesized to look like one.
func (x *modelsChatExecutor) admit(ctx context.Context, plan *chatPlan, budget models.ChatBudgetCheck) error {
	if budget == nil {
		return models.NewChatExecutionError(models.ChatErrInternal)
	}
	status, denied := budget(ctx)
	plan.budgetStatus, plan.budgetDenied = status, denied
	if denied {
		switch status {
		case http.StatusPaymentRequired:
			return models.NewChatExecutionError(models.ChatErrBudgetDenied)
		case http.StatusTooManyRequests:
			return models.NewChatExecutionError(models.ChatErrBudgetThrottled)
		default:
			// A denial with no spend status is a contradiction, not a spend refusal.
			x.warn("models-chat: budget precheck denied without a spend status; denying (internal)", "status", status)
			return models.NewChatExecutionError(models.ChatErrInternal)
		}
	}
	// A context that expired DURING the precheck stops the attempt even though the
	// fail-open gate said nothing: the gate's silence is not permission to dispatch after
	// the caller has gone.
	if ctx.Err() != nil {
		return models.NewChatExecutionError(models.ChatErrRequestCancelled)
	}
	return nil
}

// --- 7. intent -------------------------------------------------------------------------------

// anchorChatIntent writes the pre-dispatch evidence and classifies the posture. It returns
// the disposition it reached in every case, including the refusal, so the caller reports
// what actually happened rather than a boolean.
//
// The two exception paths are NOT anchored receipts and are not labelled as such: one is the
// tenant's explicit best-effort posture, the other the operator's declared degrade winning
// over a default nobody chose. Both proceed with a NAMED gap.
func (x *modelsChatExecutor) anchorChatIntent(ctx context.Context, plan *chatPlan) (string, error) {
	receipt, err := inferenceEvidenceWriter{store: x.store}.Append(ctx, plan.in.Tenant, plan.binding, model.AuditDraft{
		Actor:     firstNonEmpty(plan.in.Principal.Actor(), model.ActorSystem),
		ActorKind: firstNonEmpty(plan.in.Principal.ActorKind(), model.ActorSystem),
		Action:    chatIntentAction, TargetKind: chatExecutionKind, TargetID: model.ID(plan.attemptRef),
		PayloadHash: plan.effect[:],
		Meta:        chatIntentMeta(plan),
	})
	if receipt.AnchoredFor(plan.binding) {
		return models.ChatIntentAnchored, nil
	}
	refusal := errEvidenceRefused{fault: receipt.Fault}
	switch {
	case !plan.pol.RecordMandatory:
		x.warn("models-chat: intent evidence not anchored and recording is best-effort for this tenant; proceeding with a recorded gap",
			"attempt_ref", plan.attemptRef, "fault", string(receipt.Fault), "err", err)
		return models.ChatIntentExplicitBestEffort, nil
	case defaultMandatoryYieldsTo(plan.pol, refusal):
		x.warn("models-chat: the audit spool DEGRADED this attempt's evidence and this tenant never configured a recording posture, so the operator's declared degrade wins over the default: proceeding with a recorded evidence GAP",
			"attempt_ref", plan.attemptRef, "err", err)
		return models.ChatIntentOperatorDegradeGap, nil
	default:
		x.warn("models-chat: mandatory recording intent failed; denying (deny-closed)",
			"attempt_ref", plan.attemptRef, "fault", string(receipt.Fault), "err", err)
		return models.ChatIntentRefused, models.NewChatExecutionError(models.ChatErrRecordingUnavailable)
	}
}

// --- 8. revalidation and credential ------------------------------------------------------------

// resolveCredential re-reads the EXACT registry entry after the evidence disposition and
// compares every field and its already-validated content revision against the local plan,
// then fetches the credential reference from that immutable private record only.
//
// ⛔ THE LIST OF THINGS IT WILL NOT DO IS THE POINT. It does not use resolveConfig's
// nil-resolver pass-through, the legacy inference credential, the caller's Authorization or
// Cookie headers, an inventory key, an endpoint-inferred lookup or a cached token. Under
// AuthNone it does not call the resolver at all and passes no token. The descriptor declares
// exactly one field, and it declares it Secret, so the resolver's strict posture refuses an
// inline literal instead of forwarding it.
func (x *modelsChatExecutor) resolveCredential(ctx context.Context, plan *chatPlan) (string, error) {
	entry, snapshot, ok := x.registrySnapshot(ctx, plan.in)
	if !ok || snapshot != plan.in.Profile || entry != plan.entry {
		return "", models.NewChatExecutionError(models.ChatErrProfileUnavailable)
	}
	if plan.in.Profile.AuthScheme != "bearer" {
		return "", nil
	}
	if x.secrets == nil {
		return "", models.NewChatExecutionError(models.ChatErrCredentialUnavailable)
	}
	resolved, err := x.secrets.Resolve(ctx, sdk.Descriptor{
		Name: "olivares.model-gateway-chat", Type: sdk.TypeModule,
		ConfigFields: []sdk.ConfigField{{Key: "bearer_token", Type: sdk.FieldString, Required: true, Secret: true}},
	}, sdk.Config{Settings: map[string]string{"bearer_token": entry.CredentialRef}})
	if err != nil {
		// The resolver's error can name the backend and the field; neither reaches the
		// caller and neither is logged with the value.
		x.warn("models-chat: the execution profile's credential could not be resolved", "profile_ref", plan.in.Profile.Ref)
		return "", models.NewChatExecutionError(models.ChatErrCredentialUnavailable)
	}
	token := resolved.Settings["bearer_token"]
	if token == "" {
		return "", models.NewChatExecutionError(models.ChatErrCredentialUnavailable)
	}
	return token, nil
}

// --- 9. dispatch ---------------------------------------------------------------------------------

// dispatch constructs C1 from exactly the profile's endpoint, auth, bounds and timeout and
// calls Execute at most once. No retry, fallback, redirect, replay header, GetBody,
// re-marshal, model alias or endpoint suffix is added anywhere.
//
// C1's typed Attempted is the ONLY boundary that decides whether an effect may have
// happened: entry into this method is not that boundary, and a failure after Attempted is
// never a reason to infer zero cost.
func (x *modelsChatExecutor) dispatch(ctx context.Context, plan *chatPlan, token string, res *models.ChatExecutionResult) (modelprovider.ChatTextResponse, error) {
	scheme := modelprovider.AuthNone
	if plan.in.Profile.AuthScheme == "bearer" {
		scheme = modelprovider.AuthBearer
	}
	transport, terr := modelprovider.NewChatTextTransport(modelprovider.ChatTextTransportConfig{
		Endpoint: plan.in.Profile.Endpoint, AuthScheme: scheme, BearerToken: token,
		AllowHTTP: plan.in.Profile.AllowHTTP, MaxRequestBytes: plan.in.Profile.MaxRequestBytes,
		MaxResponseBytes: plan.in.Profile.MaxResponseBytes, Timeout: plan.in.Profile.Timeout,
		HTTPClient: x.httpClient,
	})
	if terr != nil {
		// Every other input was content-verified by the registry at load, so under bearer
		// the only value that can vary at request time is the resolved token: an invalid
		// one is a credential fault, not a caller error and never a client 401.
		if scheme == modelprovider.AuthBearer {
			return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrCredentialUnavailable)
		}
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrInternal)
	}
	response, cerr := transport.Execute(ctx, plan.prepared)
	if cerr == nil {
		res.DispatchState = models.ChatDispatchAttempted
		return response, nil
	}
	var failure *modelprovider.ChatTransportError
	if !errors.As(cerr, &failure) {
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrInternal)
	}
	if failure.Attempted {
		res.DispatchState = models.ChatDispatchAttempted
		res.CompletionState = models.ChatCompletionEffectUnknown
	}
	switch failure.Kind {
	case modelprovider.ChatTransportHTTP:
		if failure.StatusCode == http.StatusTooManyRequests {
			rate := models.NewChatExecutionError(models.ChatErrUpstreamRateLimited)
			if failure.HasRetryAfter {
				// Conservative rounding: a fractional second becomes the next whole one, so
				// a caller that honours the hint never returns early.
				seconds := int64((failure.RetryAfter + time.Second - 1) / time.Second)
				if seconds < 0 {
					seconds = 0
				}
				rate.RetryAfterSeconds = &seconds
			}
			return modelprovider.ChatTextResponse{}, rate
		}
		// Includes an upstream 401: the OPERATOR's credential was refused, which is never
		// reported to this caller as its own authentication failure.
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrUpstreamUnavailable)
	case modelprovider.ChatTransportProtocol:
		res.CompletionState = models.ChatCompletionProtocolError
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrProtocolError)
	case modelprovider.ChatTransportTimeout:
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrUpstreamTimeout)
	case modelprovider.ChatTransportInvalidConfig, modelprovider.ChatTransportInvalidRequest:
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrInternal)
	default:
		// Network, read, size-limit and cancellation: the gateway did not complete this
		// attempt, and whether it computed anything is unknown.
		return modelprovider.ChatTextResponse{}, models.NewChatExecutionError(models.ChatErrUpstreamUnavailable)
	}
}

// --- 10. observe and inspect the output ----------------------------------------------------------

// observe records the decoded observation, checks the model identity, and inspects the text
// AND the refusal text before a single output byte is releasable.
func (x *modelsChatExecutor) observe(ctx context.Context, plan *chatPlan, res *models.ChatExecutionResult, response modelprovider.ChatTextResponse, observation *chatObservation) error {
	// Reported usage is retained even when the output is withheld or the model mismatched:
	// a counter the provider reported is an observation about what it did, and discarding
	// it would turn an uncertain effect into an apparently free one.
	res.Usage = response.Usage
	if response.Usage != nil {
		res.UsageStatus = models.ChatUsageReported
	} else {
		res.UsageStatus = models.ChatUsageNotReported
	}

	if observation.mismatch {
		// A complete response from a model this profile did not pin is a protocol failure,
		// not a successful call by another name. The usage stays an OBSERVATION with a
		// recorded mismatch; it is not trusted as attribution to the pinned model. The
		// mismatch itself, and the model that was actually named, reach the outcome record
		// through chatObservation — a generic protocol_error cannot be reconciled later.
		res.CompletionState = models.ChatCompletionProtocolError
		x.warn("models-chat: the gateway answered with a model the profile does not pin; withholding output",
			"attempt_ref", plan.attemptRef)
		return models.NewChatExecutionError(models.ChatErrProtocolError)
	}

	switch response.State {
	case modelprovider.ChatTextStopped:
		res.CompletionState = models.ChatCompletionStop
	case modelprovider.ChatTextLengthLimited:
		res.CompletionState = models.ChatCompletionLength
	case modelprovider.ChatTextContentFiltered:
		res.CompletionState = models.ChatCompletionContentFilter
	case modelprovider.ChatTextRefused:
		res.CompletionState, res.Refusal = models.ChatCompletionRefusal, true
	default:
		res.CompletionState = models.ChatCompletionProtocolError
		return models.NewChatExecutionError(models.ChatErrProtocolError)
	}

	if x.outputWithheld(ctx, plan, response) {
		res.CompletionState = models.ChatCompletionOutputWithheld
		return models.NewChatExecutionError(models.ChatErrOutputWithheld)
	}
	// Only now, after every output gate, is the text attached — and even then the caller
	// receives it only once this method returns, which is after the deferred finalization
	// has run. The RAW REFUSAL STRING IS NEVER ATTACHED: Refusal above says that it
	// happened, which is all the caller is owed.
	res.Output = response.Text
	return nil
}

// outputWithheld runs the response-side content gates over the text and the refusal, in
// memory, and reports whether the response must be withheld.
//
// The two gates are independent by design. Core response DLP honours the tenant's declared
// mode: buffer withholds, flag records and delivers, off omits it by explicit choice. An
// INSTALLED inspector still runs in every mode, and its Block withholds this non-streaming
// response regardless — the same posture non-streaming Messages already has, because a
// response held in memory can still be un-sent.
func (x *modelsChatExecutor) outputWithheld(ctx context.Context, plan *chatPlan, response modelprovider.ChatTextResponse) bool {
	pol := plan.pol
	channels := make([]claudeapi.ContentChannel, 0, 2)
	texts := make([]string, 0, 2)
	if response.Text != nil {
		channels = append(channels, claudeapi.ContentChannel{Kind: "text", Role: "assistant", Text: *response.Text, Scannable: true})
		texts = append(texts, *response.Text)
	}
	if response.Refusal != nil {
		channels = append(channels, claudeapi.ContentChannel{Kind: "refusal", Role: "assistant", Text: *response.Refusal, Scannable: true})
		texts = append(texts, *response.Refusal)
	}
	content := claudeapi.CollectedContent{Channels: channels, Texts: texts}

	withheld := false
	if pol.GateDLPResponse && pol.ResponseDLPMode != inferenceproxy.ResponseDLPOff && pol.DLPEnabled() {
		if denied := pol.DLPDecide(classifyText(content.Texts)); len(denied) > 0 {
			x.publishFinding(ctx, plan.in.Tenant, sdkmodel.FindingReport{
				Kind: "models_chat_dlp_blocked", Severity: sdkmodel.SeverityHigh,
				SubjectKind: string(chatExecutionKind), SubjectRef: plan.in.Profile.ModelRef,
				Title:      "Governed Chat output blocked by DLP policy",
				DetailHash: hexSHA(plan.in.Profile.ModelRef + "|output|" + strings.Join(denied, ",")),
				OWASPLLM:   []string{"LLM02:2025"},
			})
			if pol.ResponseDLPMode == inferenceproxy.ResponseDLPBuffer {
				withheld = true
			}
		}
	}
	verdict := x.inspection().Inspect(ctx, x.subject(plan.in.Profile), plan.in.Tenant,
		plan.in.Principal.Actor(), strings.TrimSpace(plan.in.Principal.AgentIdentity), x.unbindableAgent(plan.in.Principal),
		claudeapi.InspectDirectionResponse, plan.in.Profile.ModelRef, content)
	return withheld || verdict.Block
}

// --- 11. finalization -----------------------------------------------------------------------------

// recordChatOutcome writes the ONE outcome record, on every post-intent exit.
//
// Its context is derived WITHOUT cancellation and bounded independently, because the client
// hanging up must not cancel the record of an effect that already happened. The bound is a
// real one: a dependency that ignores context is not policed by a goroutine pretending to.
//
// ⛔ A FAILURE HERE IS A NAMED GAP AND NOTHING ELSE. It cannot undo an effect, cannot
// reclassify an attempted dispatch as not_sent, and is never a reason to repeat the model
// call — repeating a dispatch to repair evidence would turn one uncertain effect into two.
func (x *modelsChatExecutor) recordChatOutcome(ctx context.Context, plan *chatPlan, res *models.ChatExecutionResult, observation *chatObservation) {
	octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chatOutcomeBound)
	defer cancel()
	receipt, err := inferenceEvidenceWriter{store: x.store}.Append(octx, plan.in.Tenant, plan.binding, model.AuditDraft{
		Actor:     firstNonEmpty(plan.in.Principal.Actor(), model.ActorSystem),
		ActorKind: firstNonEmpty(plan.in.Principal.ActorKind(), model.ActorSystem),
		Action:    chatOutcomeAction, TargetKind: chatExecutionKind, TargetID: model.ID(plan.attemptRef),
		PayloadHash: chatOutcomeHash(plan, res, observation),
		Meta:        chatOutcomeMeta(plan, res, observation),
	})
	if evidenceAppendDropped(receipt, err) {
		x.log_(slog.LevelError, "models-chat: outcome evidence dropped by the degrade spool policy (evidence gap)", "attempt_ref", plan.attemptRef)
	}
	if err != nil {
		x.log_(slog.LevelError, "models-chat: ledger outcome anchor failed (evidence gap)", "attempt_ref", plan.attemptRef, "err", err)
	}
	if receipt.AnchoredFor(plan.binding) {
		res.OutcomeDisposition = models.ChatOutcomeAnchored
		return
	}
	res.OutcomeDisposition = models.ChatOutcomeGap
}

// --- digests --------------------------------------------------------------------------------------

// chatEffectDigest binds the WHOLE authorized effect: the authority it ran under, the exact
// immutable target, the exact prepared bytes and the governance actually observed.
//
// ⛔ IT DOES NOT BIND THE EVIDENCE REF, and the omission is required rather than an
// oversight: the ref is produced by anchoring THIS digest, so binding it would be circular.
//
// These are PER-ATTEMPT OBSERVATIONS. The digest proves what this attempt decided over; it
// is not an atomic cross-module snapshot and not a replay proof of the whole estate.
func chatEffectDigest(plan *chatPlan) [32]byte {
	in := plan.in
	h := sha256.New()
	writeLenPrefixed(h, []byte(chatEffectDomain))

	// refs and tenant
	writeLenPrefixed(h, []byte(plan.requestRef))
	writeLenPrefixed(h, []byte(plan.attemptRef))
	writeLenPrefixed(h, []byte(in.Tenant.String()))

	// the AUTHENTICATED subject. No bearer value is bound; identities are.
	p := in.Principal
	writeLenPrefixed(h, []byte(p.Kind))
	writeLenPrefixed(h, []byte(p.Actor()))
	writeLenPrefixed(h, []byte(p.ActorKind()))
	writeLenPrefixed(h, []byte(p.UserID.String()))
	writeLenPrefixed(h, []byte(p.CredID.String()))
	writeLenPrefixed(h, []byte(p.AgentIdentity))
	writeLenPrefixed(h, []byte(p.SessionIdentity))
	writeLenPrefixed(h, []byte(p.SessionWorkspaceID.String()))
	writeLenPrefixed(h, []byte(p.SessionRunRef))
	writeInt64(h, p.SessionFence)
	writeInt64(h, int64(p.AAL))
	writeBool(h, p.Superadmin)
	writeBool(h, p.IsPurposeRestricted())
	writeSortedStrings(h, p.AMR)
	role, _ := p.RoleIn(in.Tenant)
	writeLenPrefixed(h, []byte(role))
	writeSortedStrings(h, p.GroupsIn(in.Tenant))
	confined, _ := p.ConfinedWorkspaceIn(in.Tenant)
	writeLenPrefixed(h, []byte(confined.String()))

	// the ROUTE authority and the exact routing policy revision
	writeLenPrefixed(h, []byte(chatRoutePermission))
	writeLenPrefixed(h, []byte(chatRouteAction))
	writeLenPrefixed(h, []byte(in.PolicyID.String()))
	writeInt64(h, in.PolicyVersion)
	writeLenPrefixed(h, in.PolicySpecDigest[:])
	writeLenPrefixed(h, []byte(in.Resource.Kind))
	writeLenPrefixed(h, []byte(in.Resource.ID))
	writeLenPrefixed(h, []byte(in.Resource.WorkspaceID.String()))

	// the route witness. PRESENCE is explicit, because the ordinary door yields the zero
	// value and an absent witness must not hash like a present one that happens to be empty.
	witness := in.Authorization
	present := chatRouteWitnessPresent(witness)
	writeBool(h, present)
	// v2 (G1): the FORMAT of the witness digest follows its presence, so the effect commits
	// to which codec produced the 32 bytes bound below. An absent witness binds the empty
	// string here and its zero-valued fields below exactly as v1 did; presence itself is
	// still the one shared decision and no new heuristic is introduced.
	if present {
		writeLenPrefixed(h, []byte(auth.RouteEvidenceDigestFormat))
	} else {
		writeLenPrefixed(h, []byte(""))
	}
	writeLenPrefixed(h, []byte(witness.CedarAction))
	writeInt64(h, int64(witness.ScopedEffect))
	writeInt64(h, int64(witness.Decision.Outcome))
	writeLenPrefixed(h, witness.ResourceDigest[:])
	writeLenPrefixed(h, witness.EvidenceDigest[:])
	writeLenPrefixed(h, witness.QuestionDigest[:])
	// Bound as ONE more fact among many, never as the policy identity.
	writeInt64(h, int64(witness.PolicyVersion)) //nolint:gosec // a fact version, not a size

	// the immutable target
	pr := in.Profile
	writeLenPrefixed(h, []byte(pr.Ref))
	writeLenPrefixed(h, []byte(pr.Revision))
	writeLenPrefixed(h, []byte(pr.Action))
	writeLenPrefixed(h, []byte(pr.Protocol))
	writeLenPrefixed(h, []byte(pr.AdapterID))
	writeLenPrefixed(h, []byte(pr.AdapterVersion))
	writeLenPrefixed(h, []byte(pr.ProviderRef))
	writeLenPrefixed(h, []byte(pr.ModelRef))
	writeLenPrefixed(h, []byte(pr.Endpoint))
	writeLenPrefixed(h, []byte(pr.Surface))
	writeLenPrefixed(h, []byte(pr.InferenceGeo))
	writeLenPrefixed(h, []byte(pr.CredentialAudience))
	writeLenPrefixed(h, []byte(pr.AuthScheme))
	writeLenPrefixed(h, []byte(pr.TransportKey))
	writeBool(h, pr.AllowHTTP)
	writeInt64(h, int64(pr.MaxRequestBytes))
	writeInt64(h, int64(pr.MaxResponseBytes))
	writeInt64(h, int64(pr.Timeout))
	writeLenPrefixed(h, []byte(in.Target.ProviderRef))
	writeLenPrefixed(h, []byte(in.Target.ModelRef))
	writeBool(h, in.Target.ViaGateway)
	writeLenPrefixed(h, []byte(in.Target.Endpoint))

	// the exact prepared bytes, by digest and length
	digest := plan.prepared.Digest()
	writeLenPrefixed(h, digest[:])
	writeInt64(h, int64(len(plan.prepared.Bytes())))
	writeInt64(h, in.MaxTokens)

	// caller attribution, LABELLED as what it is
	writeLenPrefixed(h, []byte("untrusted_attribution_session_ref"))
	writeLenPrefixed(h, []byte(in.SessionRef))

	// the governance actually observed
	writeLenPrefixed(h, plan.polDigest[:])
	writeLenPrefixed(h, []byte(plan.pin))
	writeBool(h, plan.residencyGap)
	writeBool(h, plan.contextGap)
	writeBool(h, plan.ctxPol.Deny)
	writeInt64(h, plan.ctxPol.MaxContextTokens)
	writeLenPrefixed(h, []byte(plan.ctxPol.Strategy))
	writeLenPrefixed(h, []byte(plan.ctxPol.WinningScope))
	writeBool(h, plan.ctxPol.RedactionRequired)
	writeSortedStrings(h, plan.ctxPol.ExcludedSources)
	writeBool(h, plan.inputVerdict.Forward)
	writeInt64(h, int64(plan.inputVerdict.Status))
	writeInt64(h, int64(len(plan.inputVerdict.Findings)))
	writeInt64(h, int64(plan.inputVerdict.Meter.Inspections))
	writeInt64(h, int64(plan.inputVerdict.Meter.Channels))
	writeInt64(h, int64(plan.inputVerdict.Meter.Detectors))
	writeLenPrefixed(h, []byte(models.ChatBudgetAssuranceDevelopmentPrecheck))
	writeInt64(h, int64(plan.budgetStatus))
	writeBool(h, plan.budgetDenied)
	writeBool(h, plan.pol.RecordMandatory)
	writeBool(h, plan.pol.RecordMandatoryChosen)

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// chatRouteWitnessPresent is the ONE witness-presence decision shared by the effect preimage
// and the evidence metadata, so the codec and the labels can never disagree about whether a
// witness was bound.
//
// Presence is read from the witness's own seals rather than from a struct comparison: the
// type carries a slice and is not comparable, and — more to the point — a witness that was
// never minted has all three digests zero, which is exactly the state the ordinary door
// produces today. Empty ordinary-door authority is therefore never described as a verified
// route witness, and this predicate verifies nothing: it reports what was bound.
func chatRouteWitnessPresent(w auth.RouteAuthorizationWitness) bool {
	return w.ResourceDigest != [32]byte{} || w.EvidenceDigest != [32]byte{} || w.QuestionDigest != [32]byte{}
}

// chatRouteEvidenceFormat is the software-owned label for the codec of the bound witness
// digest, or "none" when the shared presence decision found no witness. It is fixed text
// this file owns — never provider or caller data — and it creates no authority: a label is
// not a verification, and a stored label does not supply an absent preimage.
func chatRouteEvidenceFormat(w auth.RouteAuthorizationWitness) string {
	if chatRouteWitnessPresent(w) {
		return auth.RouteEvidenceDigestFormat
	}
	return chatRouteEvidenceAbsent
}

// chatObservationDigest binds the DECODED observation, preserving null versus present for
// every nullable field.
//
// ⛔ IT IS NOT A RESPONSE CHECKSUM AND MUST NEVER BE LABELLED ONE. C1 hands back a decoded
// ChatTextResponse, not the upstream octets or headers, so no digest of the wire response
// exists in this slice. Publishing one would be a fabricated fingerprint of bytes nothing
// here ever held.
func chatObservationDigest(r modelprovider.ChatTextResponse) [32]byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(chatObservationDomain))
	writeLenPrefixed(h, []byte(r.Model))
	writeLenPrefixed(h, []byte(r.State))
	writeLenPrefixed(h, []byte(r.FinishReason))
	writeOptionalString(h, r.Text)
	writeOptionalString(h, r.Refusal)
	if r.Usage == nil {
		writeBool(h, false)
		var out [32]byte
		copy(out[:], h.Sum(nil))
		return out
	}
	writeBool(h, true)
	u := r.Usage
	writeOptionalInt64(h, u.PromptTokens)
	writeOptionalInt64(h, u.CompletionTokens)
	writeOptionalInt64(h, u.TotalTokens)
	if u.PromptTokensDetails == nil {
		writeBool(h, false)
	} else {
		writeBool(h, true)
		writeOptionalInt64(h, u.PromptTokensDetails.CachedTokens)
		writeOptionalInt64(h, u.PromptTokensDetails.AudioTokens)
	}
	if u.CompletionTokensDetails == nil {
		writeBool(h, false)
	} else {
		writeBool(h, true)
		writeOptionalInt64(h, u.CompletionTokensDetails.ReasoningTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.AudioTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.AcceptedPredictionTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.RejectedPredictionTokens)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// chatOutcomeHash binds the outcome leg to the effect it reports on and to the states it
// reports, so an edited metadata map does not match its own payload hash.
//
// v2 (G1): the preimage opens with chatOutcomeDomain and then continues with the entire v1
// sequence, action string included and unchanged.
func chatOutcomeHash(plan *chatPlan, res *models.ChatExecutionResult, observation *chatObservation) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(chatOutcomeDomain))
	writeLenPrefixed(h, []byte(chatOutcomeAction))
	writeLenPrefixed(h, plan.effect[:])
	writeLenPrefixed(h, []byte(plan.attemptRef))
	writeLenPrefixed(h, []byte(res.DispatchState))
	writeLenPrefixed(h, []byte(res.CompletionState))
	writeLenPrefixed(h, []byte(res.UsageStatus))
	writeLenPrefixed(h, []byte(res.IntentDisposition))
	if observation == nil {
		writeBool(h, false)
	} else {
		writeBool(h, true)
		writeLenPrefixed(h, observation.digest[:])
		// The PUBLISHED projection is bound too, not only the digest of the whole
		// observation: the metadata a reader reconciles is the bounded model string and
		// the mismatch flag, so those exact values must be what the payload hash commits
		// to. Binding only the digest would leave the published fields editable.
		published, truncated := boundedObservedModel(observation.observedModel)
		writeLenPrefixed(h, []byte(published))
		writeBool(h, truncated)
		writeBool(h, observation.mismatch)
	}
	// The complete nullable usage the outcome PERSISTS is bound as well, field by field,
	// preserving absent-versus-present for both detail objects and missing-versus-explicit
	// -zero for every counter. Before this correction only three counters were persisted
	// and none was bound, so a metadata edit that dropped the details left no trace.
	writeChatUsageDigest(h, res.Usage)
	return h.Sum(nil)
}

// writeChatUsageDigest frames the complete nullable usage. It is the SAME shape the
// observation digest uses, and deliberately so: the two commitments must not disagree
// about what "absent" means.
func writeChatUsageDigest(h hash.Hash, u *modelprovider.ChatTextUsage) {
	if u == nil {
		writeBool(h, false)
		return
	}
	writeBool(h, true)
	writeOptionalInt64(h, u.PromptTokens)
	writeOptionalInt64(h, u.CompletionTokens)
	writeOptionalInt64(h, u.TotalTokens)
	if u.PromptTokensDetails == nil {
		writeBool(h, false)
	} else {
		writeBool(h, true)
		writeOptionalInt64(h, u.PromptTokensDetails.CachedTokens)
		writeOptionalInt64(h, u.PromptTokensDetails.AudioTokens)
	}
	if u.CompletionTokensDetails == nil {
		writeBool(h, false)
	} else {
		writeBool(h, true)
		writeOptionalInt64(h, u.CompletionTokensDetails.ReasoningTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.AudioTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.AcceptedPredictionTokens)
		writeOptionalInt64(h, u.CompletionTokensDetails.RejectedPredictionTokens)
	}
}

// boundedObservedModel returns the prefix of a provider-named model that is safe to copy
// into the ledger, and whether it had to be cut. It cuts on a RUNE boundary so the
// published value stays valid UTF-8 — a byte-sliced multi-byte rune would produce metadata
// that is not encodable, which would fail the append rather than bound it.
func boundedObservedModel(v string) (string, bool) {
	if len(v) <= chatObservedModelMax {
		return v, false
	}
	cut := v[:chatObservedModelMax]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func writeBool(h hash.Hash, v bool) {
	if v {
		writeInt64(h, 1)
		return
	}
	writeInt64(h, 0)
}

// writeSortedStrings frames a set: its size, then its members in canonical order, so the
// digest is a property of the SET and not of the order a store returned it in.
func writeSortedStrings(h hash.Hash, values []string) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	writeInt64(h, int64(len(sorted)))
	for _, v := range sorted {
		writeLenPrefixed(h, []byte(v))
	}
}

// writeOptionalString distinguishes absent from an empty string, which is the whole reason
// C0 models these as pointers.
func writeOptionalString(h hash.Hash, v *string) {
	if v == nil {
		writeBool(h, false)
		return
	}
	writeBool(h, true)
	writeLenPrefixed(h, []byte(*v))
}

// writeOptionalInt64 distinguishes a MISSING counter from an explicit zero.
func writeOptionalInt64(h hash.Hash, v *int64) {
	if v == nil {
		writeBool(h, false)
		return
	}
	writeBool(h, true)
	writeInt64(h, *v)
}

// --- evidence metadata -------------------------------------------------------------------------------

// chatIntentMeta is refs, closed decisions and digests. It carries NO prompt, refusal,
// output, credential or endpoint path: the endpoint is bound by the effect digest and by
// the immutable profile revision, and the only endpoint-derived value published is the
// CredentialAudience, which is public authority metadata.
//
// v2 (G1) adds three software-owned format labels: the effect codec and algorithm, and the
// route witness codec (or "none") decided by the same presence predicate the codec uses.
// They identify append-side algorithms; they neither verify anything nor provide the
// preimage a record without labels never stored.
func chatIntentMeta(plan *chatPlan) map[string]any {
	digest := plan.prepared.Digest()
	return map[string]any{
		"effect_digest_format":         chatEffectDomain,
		"effect_digest_algorithm":      chatDigestAlgorithm,
		"route_evidence_digest_format": chatRouteEvidenceFormat(plan.in.Authorization),
		"request_ref":                  plan.requestRef, "attempt_ref": plan.attemptRef, "decision": "allow",
		"action": plan.in.Profile.Action, "protocol": plan.in.Profile.Protocol,
		"profile_ref": plan.in.Profile.Ref, "profile_revision": plan.in.Profile.Revision,
		"provider_ref": plan.in.Profile.ProviderRef, "model_ref": plan.in.Profile.ModelRef,
		"surface": plan.in.Profile.Surface, "inference_geo": plan.in.Profile.InferenceGeo,
		"credential_audience": plan.in.Profile.CredentialAudience, "auth_scheme": plan.in.Profile.AuthScheme,
		"policy_id": plan.in.PolicyID.String(), "policy_version": plan.in.PolicyVersion,
		"routing_spec_digest": hex.EncodeToString(plan.in.PolicySpecDigest[:]),
		"proxy_policy_digest": hex.EncodeToString(plan.polDigest[:]),
		"prepared_digest":     hex.EncodeToString(digest[:]),
		"prepared_bytes":      int64(len(plan.prepared.Bytes())),
		"effect_digest":       hex.EncodeToString(plan.effect[:]),
		"residency_pin":       plan.pin, "residency_gap": plan.residencyGap,
		"context_policy_gap": plan.contextGap,
		"budget_assurance":   models.ChatBudgetAssuranceDevelopmentPrecheck,
		"budget_precheck":    chatBudgetOutcome(plan),
		"record_mandatory":   plan.pol.RecordMandatory,
	}
}

// chatOutcomeMeta adds the observed states, the observed model and the COMPLETE nullable
// usage. A missing counter is absent from the map; it is never written as zero.
//
// ⛔ THE DIGEST IS NOT A SUBSTITUTE FOR THE DATA, which is what R2 of the independent
// review returned. Before this correction the record kept three counters and a digest, so
// a response whose details object was ABSENT and one whose details object was PRESENT AND
// EMPTY produced identical metadata; the digest changed, but a digest can only confirm a
// value someone already guessed — it cannot answer "how many cached prompt tokens did this
// attempt observe?". Reading and reconciling an outcome is the purpose of the record, so
// the fields it reports on are the fields it keeps.
//
// v2 (G1) adds the same three format labels as the intent plus the outcome codec and
// algorithm labels, all fixed software-owned values.
func chatOutcomeMeta(plan *chatPlan, res *models.ChatExecutionResult, observation *chatObservation) map[string]any {
	meta := map[string]any{
		"request_ref": plan.requestRef, "attempt_ref": plan.attemptRef,
		"effect_digest":                hex.EncodeToString(plan.effect[:]),
		"effect_digest_format":         chatEffectDomain,
		"effect_digest_algorithm":      chatDigestAlgorithm,
		"route_evidence_digest_format": chatRouteEvidenceFormat(plan.in.Authorization),
		"outcome_digest_format":        chatOutcomeDomain,
		"outcome_digest_algorithm":     chatDigestAlgorithm,
		"dispatch_state":               res.DispatchState, "completion_state": res.CompletionState,
		"usage_status": res.UsageStatus, "intent_disposition": res.IntentDisposition,
		"refusal": res.Refusal, "output_released": res.Output != nil,
	}
	if observation != nil {
		// Named for what it is: a digest of the DECODED observation, not of upstream bytes.
		meta["decoded_observation_digest"] = hex.EncodeToString(observation.digest[:])
		// The model the gateway NAMED, and whether it is the pinned one. Both are written
		// on every observed attempt, including the matching case, so "no mismatch" is a
		// recorded fact rather than the absence of a key. observed_model is a bounded
		// OBSERVATION of a provider-controlled string: it is never attribution, and
		// model_mismatch=true is why the completion state is protocol_error rather than a
		// generic transport fault nothing can reconcile.
		model, truncated := boundedObservedModel(observation.observedModel)
		meta["observed_model"] = model
		meta["observed_model_truncated"] = truncated
		meta["model_mismatch"] = observation.mismatch
	}
	putChatUsageMeta(meta, res.Usage)
	return meta
}

// putChatUsageMeta writes the complete nullable usage observation, preserving all four
// distinctions the type can express:
//
//	usage object absent          → usage_status says so and NO counter key is written
//	details object absent        → *_tokens_details_present is false, no detail key
//	details object present/empty → *_tokens_details_present is TRUE, no detail key
//	counter absent vs zero       → key absent vs key present with the value 0
//
// The third row is the one a digest alone cannot recover and the one the previous record
// lost. No counter is priced, summed or converted here: these are the provider's reported
// numbers and nothing else, which is why no CostSample accompanies them.
func putChatUsageMeta(meta map[string]any, u *modelprovider.ChatTextUsage) {
	if u == nil {
		return
	}
	putOptionalInt64(meta, "input_tokens", u.PromptTokens)
	putOptionalInt64(meta, "output_tokens", u.CompletionTokens)
	putOptionalInt64(meta, "total_tokens", u.TotalTokens)
	meta["prompt_tokens_details_present"] = u.PromptTokensDetails != nil
	if d := u.PromptTokensDetails; d != nil {
		putOptionalInt64(meta, "prompt_cached_tokens", d.CachedTokens)
		putOptionalInt64(meta, "prompt_audio_tokens", d.AudioTokens)
	}
	meta["completion_tokens_details_present"] = u.CompletionTokensDetails != nil
	if d := u.CompletionTokensDetails; d != nil {
		putOptionalInt64(meta, "completion_reasoning_tokens", d.ReasoningTokens)
		putOptionalInt64(meta, "completion_audio_tokens", d.AudioTokens)
		putOptionalInt64(meta, "completion_accepted_prediction_tokens", d.AcceptedPredictionTokens)
		putOptionalInt64(meta, "completion_rejected_prediction_tokens", d.RejectedPredictionTokens)
	}
}

func putOptionalInt64(meta map[string]any, key string, v *int64) {
	if v != nil {
		meta[key] = *v
	}
}

// chatBudgetOutcome names what the precheck actually returned, never "reserved".
func chatBudgetOutcome(plan *chatPlan) string {
	if plan.budgetDenied {
		return "denied_" + strconv.Itoa(plan.budgetStatus)
	}
	return "not_denied"
}

// --- small helpers -----------------------------------------------------------------------------------

func (x *modelsChatExecutor) publish(ctx context.Context, tenant model.TenantID, obs sdkmodel.Observation) {
	if x.bus == nil {
		return
	}
	if err := x.bus.Publish(ctx, event.FromObservation(tenant.String(), chatSignalSource, obs)); err != nil {
		x.warn("models-chat: bus publish failed (best-effort)", "err", err)
	}
}

// publishFinding stamps the clock once and publishes. A nil bus makes it a no-op.
func (x *modelsChatExecutor) publishFinding(ctx context.Context, tenant model.TenantID, f sdkmodel.FindingReport) {
	f.OccurredAt = x.clock().UTC()
	x.publish(ctx, tenant, f)
}

func (x *modelsChatExecutor) warn(msg string, args ...any) { x.log_(slog.LevelWarn, msg, args...) }
func (x *modelsChatExecutor) info(msg string, args ...any) { x.log_(slog.LevelInfo, msg, args...) }

func (x *modelsChatExecutor) log_(level slog.Level, msg string, args ...any) {
	if x.log == nil {
		return
	}
	x.log.Log(context.Background(), level, msg, args...)
}

// --- explicit development activation ---------------------------------------------------------------

const (
	// envModelGatewayChatMode is the ONE extra configuration key this slice adds.
	envModelGatewayChatMode = "OLIVARES_MODEL_GATEWAY_CHAT_MODE"
	// modelGatewayChatDisabled is the DEFAULT. A pinned profile alone therefore never
	// silently enables dispatch: an operator who authors profiles still gets C2A's 503
	// until they say, in one more place, that they mean it.
	modelGatewayChatDisabled = "disabled"
	// modelGatewayChatDevelopmentPrecheck is the only value that wires the executor, and
	// its name is the claim it makes. Admission is the EXISTING non-atomic, fail-open
	// routing precheck; it is honest bounded development qualification and it is NOT a
	// hard monetary budget.
	modelGatewayChatDevelopmentPrecheck = "development_precheck"
)

// loadModelGatewayChatMode reads the activation. An unknown value is a STARTUP ERROR rather
// than a fallback to disabled, because the two ways an operator gets this wrong have
// opposite costs: a typo that silently disabled the path would waste a day, and a value
// like `hard_budget` that silently disabled it would waste a day AND leave someone
// believing they had asked for enforcement.
//
// ⛔ THERE IS NO `hard_budget` VALUE, AND ADDING ONE IS D02'S JOB, NOT A CONFIG EDIT. Until
// a typed reserve/dispatch/settle integration exists and is qualified, a deployment that
// must enforce a hard monetary cap leaves Chat disabled. "The precheck returned not-denied"
// is not evidence that no hard budget was requested.
func loadModelGatewayChatMode(getenv func(string) string) (string, error) {
	switch raw := strings.TrimSpace(getenv(envModelGatewayChatMode)); raw {
	case "", modelGatewayChatDisabled:
		return modelGatewayChatDisabled, nil
	case modelGatewayChatDevelopmentPrecheck:
		return modelGatewayChatDevelopmentPrecheck, nil
	default:
		return "", fmt.Errorf(
			"%s must be %q (the default) or %q; %q is not a supported value. There is no hard-budget"+
				" mode: the governed Chat path admits requests through the existing non-atomic,"+
				" fail-open routing precheck, so a deployment that must enforce a hard monetary"+
				" budget keeps this disabled until the typed reserve/dispatch/settle integration lands",
			envModelGatewayChatMode, modelGatewayChatDisabled, modelGatewayChatDevelopmentPrecheck, raw)
	}
}

// newModelsChatExecutor constructs the adapter ONLY under the explicit development
// activation and only when an immutable profile registry exists. It returns nil otherwise,
// so the models module keeps its deny-closed default and a profiled /execute stays 503.
//
// The optional content inspector is instantiated HERE, independently of the Messages
// listener: a deployment that configured a content firewall and mounted no inference proxy
// must not silently get an uninspected Chat path. Everything else is late-bound by boot()
// once the store exists; until then ready() is false and the port refuses.
func newModelsChatExecutor(mode string, registry *modelGatewayProfileRegistry, getenv func(string) string, log *slog.Logger) *modelsChatExecutor {
	if mode != modelGatewayChatDevelopmentPrecheck || registry == nil {
		return nil
	}
	inspector := newContentInspector(getenv, log)
	if log != nil {
		log.Warn("models: the governed Chat execution path is ACTIVE in development_precheck mode — admission is the existing NON-ATOMIC, FAIL-OPEN routing precheck, not a hard monetary budget; do not run this profile where a hard cap must be enforced",
			"mode", mode, "content_inspector", inspector != nil)
	}
	return &modelsChatExecutor{registry: registry, inspector: inspector, clock: time.Now, log: log}
}

// chatExecutorDeps is the late-bound half. It is one struct rather than eight setters so
// binding is ATOMIC in the only sense that matters here: a caller cannot bind four of them,
// serve a request, and bind the rest afterwards.
type chatExecutorDeps struct {
	Store          store.Store
	Policy         proxyPolicySource
	ContextPolicy  contextPolicyResolver
	Residency      *residency.Registry
	Secrets        *secret.Resolver
	Approvals      *approvalBridge
	Bus            observationSink
	CircuitBreaker circuitBreakerEngine
	HTTPClient     *http.Client
}

// bind completes the adapter and reports whether it is ready to serve. A false result is
// deliberately not fatal to boot: the rest of the control plane still runs, and Chat stays
// refused with chat_execution_not_ready rather than serving with a gate it cannot run.
func (x *modelsChatExecutor) bind(deps chatExecutorDeps) bool {
	if x == nil {
		return false
	}
	x.store, x.policy, x.contextPolicy = deps.Store, deps.Policy, deps.ContextPolicy
	x.residency, x.secrets, x.approvals = deps.Residency, deps.Secrets, deps.Approvals
	x.bus, x.circuitBreaker, x.httpClient = deps.Bus, deps.CircuitBreaker, deps.HTTPClient
	return x.ready()
}

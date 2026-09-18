// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/connectors/modelrouter"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// guardedtext.go is the module-side seam of the governed Chat path: ONE synchronous Chat operation behind the
// existing /execute handler, for a routing policy that pins a content-addressed execution
// profile. C2A resolved and then refused (503); this port is what the composition root
// fills in.
//
// ⛔ IT IS A SEPARATE PORT, NOT A UNION ON Executor, AND THE SEPARATION IS THE POINT.
// Adding a Chat arm to ExecuteRequest/ExecuteResult would have changed the legacy
// executor's types, its fallback loop and every existing caller for a branch the profile
// already selects. Messages and the legacy routed executor are structurally untouched
// here: their types, their chain semantics and their DTO are the same bytes they were.
//
// ⛔ AND NOTHING IN THIS FILE IS A BEARER PERMIT. A ChatExecutionRequest is an ordinary
// struct value: constructing one authenticates nobody and authorizes nothing. The real
// authority is the route wrapper that already authenticated the caller and checked
// models:routing:admin, the resolved routing policy, the deny-closed gates the handler
// runs before this branch, and the ORDERED sequence the composition operation enforces.
// A caller that could build this struct could already call the route.

// ChatExecutionRequest is one already-governed routing decision, restated for the Chat
// operation. Every field is a decision the handler ALREADY made against authenticated
// state; nothing here is caller-supplied except Input, MaxTokens and SessionRef.
type ChatExecutionRequest struct {
	Tenant    model.TenantID
	Principal auth.Principal
	Resource  auth.ResourceAttrs
	// Authorization is the route wrapper's witness. The ordinary (ungoverned-door)
	// wrapper yields the zero value; the effect binding records that presence explicitly
	// rather than pretending an absent witness is an empty one.
	Authorization auth.RouteAuthorizationWitness
	// PolicyID/PolicyVersion/PolicySpecDigest identify the exact routing policy revision
	// this decision came from. PolicySpecDigest is over the EFFECTIVE routing spec read in
	// the same transaction — not the witness's legacy maximum PolicyVersion, which is a
	// maximum of independent fact versions and not a policy epoch.
	PolicyID         model.ID
	PolicyVersion    int64
	PolicySpecDigest [32]byte
	Profile          ExecutionProfile
	Target           modelrouter.Target
	Input            string
	MaxTokens        int64
	// SessionRef is ATTRIBUTION ONLY and is never actor authority. The security gates
	// upstream already decided on Principal.AgentIdentity; this field exists so the
	// existing FinOps identity-budget tie-in sees what it always saw.
	SessionRef string
}

// ChatBudgetCheck runs the EXISTING routing budget precheck once, over the primary the
// handler already selected. It is deliberately a closure the handler owns: the Chat
// executor cannot hand it a different target, a different session ref or a second
// decision, so the precheck can only ever be asked about the request that was governed.
//
// Its result is the legacy fail-open precheck and NOTHING MORE. denied=false does not
// mean a reservation was taken, does not mean the price ledger is healthy, and does not
// mean a hard cap was honoured — see ChatBudgetAssuranceDevelopmentPrecheck.
type ChatBudgetCheck func(ctx context.Context) (status int, denied bool)

// ChatExecutor is the deny-closed composition seam. The only production caller is the
// profiled branch of handleExecuteRouting. It is deliberately NOT exposed as an HTTP
// endpoint, a durable replay API, or an Authorize→permit→Dispatch protocol: there is one
// synchronous operation and its internal ordering is not a caller's to reassemble.
type ChatExecutor interface {
	ExecuteChat(context.Context, ChatExecutionRequest, ChatBudgetCheck) (ChatExecutionResult, error)
}

// Dispatch states: whether the concrete transport's Do was invoked at all.
const (
	ChatDispatchNotAttempted = "not_attempted"
	ChatDispatchAttempted    = "attempted"
)

// Completion states. not_sent is a pre-dispatch exit; effect_unknown is the honest state
// after Do returned an error — the remote may or may not have computed and billed.
const (
	ChatCompletionNotSent        = "not_sent"
	ChatCompletionStop           = "stop"
	ChatCompletionLength         = "length"
	ChatCompletionContentFilter  = "content_filter"
	ChatCompletionRefusal        = "refusal"
	ChatCompletionEffectUnknown  = "effect_unknown"
	ChatCompletionProtocolError  = "protocol_error"
	ChatCompletionOutputWithheld = "output_withheld"
)

// Usage states. not_reported means a complete response carried no usage object; unknown
// means no complete response exists to have reported one. Neither is zero cost.
const (
	ChatUsageReported    = "reported"
	ChatUsageNotReported = "not_reported"
	ChatUsageUnknown     = "unknown"
)

// Intent dispositions. Only ChatIntentAnchored is an SDK-anchored receipt; the two
// exception paths are named as exceptions so a reader cannot mistake either for evidence.
const (
	ChatIntentAnchored           = "anchored"
	ChatIntentExplicitBestEffort = "explicit_best_effort"
	ChatIntentOperatorDegradeGap = "operator_degrade_gap"
	ChatIntentRefused            = "refused"
)

// Outcome dispositions. not_required is the pre-intent exit; gap is a named loss.
const (
	ChatOutcomeAnchored    = "anchored"
	ChatOutcomeGap         = "gap"
	ChatOutcomeNotRequired = "not_required"
)

// ChatBudgetAssuranceDevelopmentPrecheck is the ONLY assurance this slice can claim, and
// it is deliberately verbose. It states that the admission was the existing NON-ATOMIC,
// FAIL-OPEN precheck: no monetary reservation was taken, nothing was settled, and a
// permitted result is not evidence that a hard budget was respected. FinOps' typed
// reserve/dispatch/settle replaces it; until then the value never changes.
const ChatBudgetAssuranceDevelopmentPrecheck = "development_precheck_non_atomic"

// ChatExecutionResult is the outcome of one Chat attempt. It carries NO USD amount and no
// CostSample: this slice has no authoritative tariff and cannot release money as zero
// after an uncertain effect. Usage counters are the provider's reported observation,
// nullable throughout — a missing counter is nil, an explicit zero is zero.
type ChatExecutionResult struct {
	RequestRef, AttemptRef string
	DispatchState          string
	CompletionState        string
	UsageStatus            string
	Output                 *string
	Refusal                bool
	Usage                  *mp.ChatTextUsage
	IntentDisposition      string
	OutcomeDisposition     string
	BudgetAssurance        string
}

// Closed error codes. Every code has EXACTLY ONE HTTP status and ONE fixed message, both
// in the table below; a code or status outside it is refused into the internal failure
// rather than passed through. This is what keeps a provider body, an endpoint, a resolved
// credential, an inspector detail or a resolver error from reaching a caller by way of an
// error string nobody audited.
const (
	ChatErrExecutionUnavailable     = "chat_execution_unavailable"
	ChatErrExecutionNotReady        = "chat_execution_not_ready"
	ChatErrRequestCancelled         = "chat_request_cancelled"
	ChatErrRequestInvalid           = "chat_request_invalid"
	ChatErrRequestTooLarge          = "chat_request_too_large"
	ChatErrProfileUnavailable       = "chat_profile_unavailable"
	ChatErrGovernanceUnavailable    = "chat_governance_unavailable"
	ChatErrResidencyDenied          = "chat_residency_denied"
	ChatErrContextPolicyDenied      = "chat_context_policy_denied"
	ChatErrContextLimitUnverifiable = "context_limit_unverifiable"
	ChatErrRedactionUnsupported     = "chat_redaction_unsupported"
	ChatErrRequestCeilingExceeded   = "chat_request_ceiling_exceeded"
	ChatErrContentDenied            = "chat_content_denied"
	ChatErrInspectionDenied         = "chat_inspection_denied"
	ChatErrCircuitOpen              = "chat_circuit_open"
	ChatErrBudgetDenied             = "chat_budget_denied"
	ChatErrBudgetThrottled          = "chat_budget_throttled"
	ChatErrRecordingUnavailable     = "chat_recording_unavailable"
	ChatErrCredentialUnavailable    = "chat_credential_unavailable"
	ChatErrUpstreamRateLimited      = "chat_upstream_rate_limited"
	ChatErrUpstreamUnavailable      = "chat_upstream_unavailable"
	ChatErrUpstreamTimeout          = "chat_upstream_timeout"
	ChatErrProtocolError            = "chat_protocol_error"
	ChatErrOutputWithheld           = "chat_output_withheld"
	ChatErrInternal                 = "chat_internal_error"
)

type chatErrorSpec struct {
	status  int
	message string
}

// chatExecutionErrors is the closed map. A message here is written for the CALLER and
// says only what the caller may know: it never names the endpoint, the provider's own
// error, the detector that fired, or which secret backend was unreachable.
var chatExecutionErrors = map[string]chatErrorSpec{
	ChatErrExecutionUnavailable:     {http.StatusServiceUnavailable, "the pinned Chat execution path is not available in this build"},
	ChatErrExecutionNotReady:        {http.StatusServiceUnavailable, "the Chat execution path is not ready to serve requests"},
	ChatErrRequestCancelled:         {http.StatusRequestTimeout, "the request was cancelled before the governed Chat operation began"},
	ChatErrRequestInvalid:           {http.StatusBadRequest, "the request cannot be prepared for the pinned Chat operation"},
	ChatErrRequestTooLarge:          {http.StatusRequestEntityTooLarge, "the prepared request exceeds the execution profile's request-size bound"},
	ChatErrProfileUnavailable:       {http.StatusServiceUnavailable, "the pinned execution profile is unavailable"},
	ChatErrGovernanceUnavailable:    {http.StatusServiceUnavailable, "the governance decision plane is unavailable; execution denied (deny-closed)"},
	ChatErrResidencyDenied:          {http.StatusForbidden, "data residency: this execution profile's inference geo is not permitted for the tenant"},
	ChatErrContextPolicyDenied:      {http.StatusForbidden, "context policy forbids this request for the subject"},
	ChatErrContextLimitUnverifiable: {http.StatusUnprocessableEntity, "a context-token limit applies and this operation cannot verify it exactly"},
	ChatErrRedactionUnsupported:     {http.StatusUnprocessableEntity, "the effective policy requires redaction this text operation cannot perform"},
	ChatErrRequestCeilingExceeded:   {http.StatusPaymentRequired, "request exceeds the tenant per-request consumption ceiling"},
	ChatErrContentDenied:            {http.StatusForbidden, "request blocked by data-loss-prevention policy"},
	ChatErrInspectionDenied:         {http.StatusForbidden, "request blocked by content inspection policy"},
	ChatErrCircuitOpen:              {http.StatusServiceUnavailable, "circuit breaker tripped for this agent; request denied until it resets"},
	ChatErrBudgetDenied:             {http.StatusPaymentRequired, "execution denied: an enforcing budget is at its cap"},
	ChatErrBudgetThrottled:          {http.StatusTooManyRequests, "execution throttled: an enforcing budget is at its cap"},
	ChatErrRecordingUnavailable:     {http.StatusServiceUnavailable, "tamper-evident recording unavailable; governed execution denied"},
	ChatErrCredentialUnavailable:    {http.StatusServiceUnavailable, "the execution profile's credential could not be resolved"},
	ChatErrUpstreamRateLimited:      {http.StatusTooManyRequests, "the pinned gateway rate-limited this attempt"},
	ChatErrUpstreamUnavailable:      {http.StatusBadGateway, "the pinned gateway did not complete this attempt"},
	ChatErrUpstreamTimeout:          {http.StatusGatewayTimeout, "the pinned gateway did not answer within the profile's bound"},
	ChatErrProtocolError:            {http.StatusBadGateway, "the pinned gateway returned a response this operation cannot accept"},
	ChatErrOutputWithheld:           {http.StatusForbidden, "the response was withheld by content policy"},
	ChatErrInternal:                 {http.StatusInternalServerError, "governed Chat execution failed"},
}

// ChatExecutionError is the ONLY error shape this port returns to the handler. It wraps
// nothing: there is no Unwrap, so an errors.As on a transport, codec, store or resolver
// error can never reach a caller through it, and no log line built from it can leak one.
//
// A returned error does NOT erase the ChatExecutionResult it was returned beside. An
// attempted effect stays attempted; reported usage stays reported.
type ChatExecutionError struct {
	Code       string
	HTTPStatus int
	// RetryAfterSeconds is ONLY C1's validated upstream 429 hint, already bounded and
	// rounded conservatively. Its presence does not authorize a retry and this operation
	// never performs one.
	RetryAfterSeconds *int64
}

// Error returns the fixed message for the closed code. An unrecognized code degrades to
// the internal-failure message rather than echoing itself.
func (e *ChatExecutionError) Error() string {
	if e == nil {
		return chatExecutionErrors[ChatErrInternal].message
	}
	if spec, ok := chatExecutionErrors[e.Code]; ok {
		return spec.message
	}
	return chatExecutionErrors[ChatErrInternal].message
}

// NewChatExecutionError builds an error at its canonical status. It is the ONLY
// constructor the composition root needs and the only one it should use: a hand-built
// literal can pair a code with the wrong status, and chatErrorResponse refuses that pair
// into the internal failure rather than trusting either half.
func NewChatExecutionError(code string) *ChatExecutionError {
	spec, ok := chatExecutionErrors[code]
	if !ok {
		code = ChatErrInternal
		spec = chatExecutionErrors[ChatErrInternal]
	}
	return &ChatExecutionError{Code: code, HTTPStatus: spec.status}
}

// chatErrorResponse maps a returned error onto the wire. It is deliberately CLOSED in
// both directions: an unknown code, a status that does not match its code's canonical
// status, or any error that is not a *ChatExecutionError becomes the fixed internal
// failure. A generic error passthrough here is exactly how a provider body reaches a
// caller, so there is none.
func chatErrorResponse(err error) (int, string, string, *int64) {
	internal := chatExecutionErrors[ChatErrInternal]
	e, ok := err.(*ChatExecutionError) //nolint:errorlint // deliberately not errors.As: no unwrapping
	if !ok || e == nil {
		return internal.status, ChatErrInternal, internal.message, nil
	}
	spec, known := chatExecutionErrors[e.Code]
	if !known || e.HTTPStatus != spec.status {
		return internal.status, ChatErrInternal, internal.message, nil
	}
	retry := e.RetryAfterSeconds
	if e.Code != ChatErrUpstreamRateLimited || retry == nil || *retry < 0 {
		retry = nil
	}
	return spec.status, e.Code, spec.message, retry
}

// unavailableChatExecutor is the deny-closed default: every Chat dispatch is refused with
// the same 503 C2A already returned, BEFORE any policy, inspector, secret or transport
// I/O — the refusal is the whole method. Availability is independent of the legacy
// Executor: a build with a routed executor wired still refuses Chat until an operator
// explicitly activates it.
type unavailableChatExecutor struct{}

func (unavailableChatExecutor) ExecuteChat(context.Context, ChatExecutionRequest, ChatBudgetCheck) (ChatExecutionResult, error) {
	return ChatExecutionResult{}, NewChatExecutionError(ChatErrExecutionUnavailable)
}

// WithChatExecutor wires the synchronous governed Chat operation. Without it the module
// keeps unavailableChatExecutor and a profiled /execute stays 503 — the C2A posture.
func WithChatExecutor(e ChatExecutor) Option {
	return func(m *Module) {
		if e != nil {
			m.chatExecutor = e
		}
	}
}

// chatExecutionDTO is the closed, content-free execution report. Every string in it comes
// from a vocabulary above; none of it is a prompt, a refusal string, a credential, an
// endpoint path or a policy internal.
type chatExecutionDTO struct {
	ProfileRef         string `json:"profile_ref"`
	ProfileRevision    string `json:"profile_revision"`
	Action             string `json:"action"`
	Protocol           string `json:"protocol"`
	Surface            string `json:"surface"`
	CredentialAudience string `json:"credential_audience"`
	RequestRef         string `json:"request_ref,omitempty"`
	AttemptRef         string `json:"attempt_ref,omitempty"`
	DispatchState      string `json:"dispatch_state"`
	CompletionState    string `json:"completion_state"`
	UsageStatus        string `json:"usage_status"`
	IntentDisposition  string `json:"intent_disposition"`
	OutcomeDisposition string `json:"outcome_disposition"`
	BudgetAssurance    string `json:"budget_assurance"`
}

// chatExecuteResponseDTO is the Chat result. It is a DIFFERENT type from
// executeResponseDTO on purpose: input_tokens/output_tokens are POINTERS here, because
// this operation must distinguish "the provider reported no counter" from "the provider
// reported zero", and the legacy DTO's bare int64 cannot. fallback_used is always false —
// a Chat profile pins exactly one target and no chain is ever tried.
type chatExecuteResponseDTO struct {
	Decision     decisionDTO      `json:"decision"`
	Served       targetDTO        `json:"served"`
	FallbackUsed bool             `json:"fallback_used"`
	Output       *string          `json:"output"`
	InputTokens  *int64           `json:"input_tokens"`
	OutputTokens *int64           `json:"output_tokens"`
	Refusal      bool             `json:"refusal"`
	Execution    chatExecutionDTO `json:"execution"`
}

type chatErrorObjectDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// chatErrorResponseDTO is the Chat error body ONCE server refs exist.
//
// AN ERROR DOES NOT ERASE THE RESULT, and that is the whole reason this type exists.
// Before this correction the error path serialized only `error` + `execution`, so a 403
// that withheld the output ALSO dropped the counters the gateway had already reported and
// the target the attempt was made against: a caller could not tell 11/7 known tokens from
// nothing observed. That is the "an error erases the observation" failure the contract
// forbids, and it is what R1 of the independent review returned.
//
// Every field is content-free:
//
//   - expected_target is the pinned primary this attempt was made AGAINST. It is
//     deliberately NOT called `served`: on an error nothing was served, and that word in a
//     failure body would read as proof of service. The success DTO keeps `served`, which
//     is the one place it is true.
//   - output is ALWAYS null and always PRESENT. Present, so a caller reads the absence
//     instead of inferring it from a missing key; null, because no error row of the
//     contract's result table releases text.
//   - input_tokens/output_tokens are the nullable counters the RESULT carried, with the
//     same missing-versus-explicit-zero distinction the success DTO has.
//   - refusal is the boolean the result carried. The raw refusal STRING is never attached.
//   - fallback_used is false: a Chat profile pins exactly one target and no chain is ever
//     tried, on this path exactly as on the success path.
type chatErrorResponseDTO struct {
	Error          chatErrorObjectDTO `json:"error"`
	ExpectedTarget targetDTO          `json:"expected_target"`
	FallbackUsed   bool               `json:"fallback_used"`
	Output         *string            `json:"output"`
	InputTokens    *int64             `json:"input_tokens"`
	OutputTokens   *int64             `json:"output_tokens"`
	Refusal        bool               `json:"refusal"`
	Execution      chatExecutionDTO   `json:"execution"`
}

func chatExecutionDTOFor(p ExecutionProfile, res ChatExecutionResult) chatExecutionDTO {
	return chatExecutionDTO{
		ProfileRef: p.Ref, ProfileRevision: p.Revision, Action: p.Action, Protocol: p.Protocol,
		Surface: p.Surface, CredentialAudience: p.CredentialAudience,
		RequestRef: res.RequestRef, AttemptRef: res.AttemptRef,
		DispatchState:      firstNonBlank(res.DispatchState, ChatDispatchNotAttempted),
		CompletionState:    firstNonBlank(res.CompletionState, ChatCompletionNotSent),
		UsageStatus:        firstNonBlank(res.UsageStatus, ChatUsageUnknown),
		IntentDisposition:  firstNonBlank(res.IntentDisposition, ChatIntentRefused),
		OutcomeDisposition: firstNonBlank(res.OutcomeDisposition, ChatOutcomeNotRequired),
		BudgetAssurance:    firstNonBlank(res.BudgetAssurance, ChatBudgetAssuranceDevelopmentPrecheck),
	}
}

func firstNonBlank(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// routingSpecDigestDomain separates this digest from every other SHA-256 in the estate.
const routingSpecDigestDomain = "olivares.models.routing-spec.v1"

// routingSpecDigest is the canonical content digest of the EFFECTIVE routing spec, taken
// from the same policy read that produced the decision. It hashes the existing typed
// spec's toSpecMap through encoding/json, whose object keys are emitted in sorted order,
// so the value is a property of the spec and not of a map walk.
//
// It deliberately does NOT use the route witness's PolicyVersion. That field is the
// legacy MAXIMUM of independent fact versions: a fact can change without moving it, so
// binding it would let the evidence claim a policy identity that two different specs
// share.
func routingSpecDigest(s routingSpec) ([32]byte, error) {
	body, err := json.Marshal(s.toSpecMap())
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(routingSpecDigestDomain))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(body)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// executeChatProfile is the profiled branch of handleExecuteRouting. Everything it hands
// the port was decided upstream against AUTHENTICATED state: the tenant the route
// resolved, the principal the route authenticated, the resource and witness the route
// wrapper produced, the policy identity read in the decision transaction, the profile the
// registry content-verified, and the primary that survived every deny-closed gate. The
// only caller-supplied values are the prompt, the token bound and the attribution ref.
func (m *Module) executeChatProfile(
	w http.ResponseWriter, r *http.Request, mc api.ModuleContext, in executeRequestDTO,
	dec decisionDTO, profile ExecutionProfile, policyID model.ID, policyVersion int64, specDigest [32]byte,
) {
	executor := m.chatExecutor
	if executor == nil { // a nil option value must refuse exactly like the default
		executor = unavailableChatExecutor{}
	}

	// The budget precheck is a ONE-SHOT closure over the decision these gates produced.
	// The Chat composition cannot substitute a target, a session ref or a second decision,
	// and it cannot ask twice: a second call returns a DENIAL WITH NO STATUS, which the
	// composition is required to read as an internal deny rather than as a spend refusal.
	// The decision it hands budgetDeniesRoute is a private copy, target included, so the
	// gate's mutate-on-denial can never rewrite the decision this handler reports.
	calls := 0
	budget := ChatBudgetCheck(func(ctx context.Context) (int, bool) {
		calls++
		if calls > 1 {
			if m.log != nil {
				m.log.Error("models: chat budget precheck asked more than once; denying (internal)")
			}
			return 0, true
		}
		local := dec
		if dec.Primary != nil {
			target := *dec.Primary
			local.Primary = &target
		}
		return m.budgetDeniesRoute(r.WithContext(ctx), mc, &local, in.SessionRef)
	})

	req := ChatExecutionRequest{
		Tenant: mc.Tenant, Principal: mc.Principal, Resource: mc.Resource, Authorization: mc.Authorization,
		PolicyID: policyID, PolicyVersion: policyVersion, PolicySpecDigest: specDigest,
		Profile: profile, Target: fromTargetDTO(*dec.Primary),
		Input: in.Input, MaxTokens: int64(in.MaxTokens), SessionRef: in.SessionRef,
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = defaultExecuteMaxTokens
	}

	res, err := executor.ExecuteChat(r.Context(), req, budget)
	if err != nil {
		// The expected target travels with the failure: it is the primary these gates
		// left standing and the one the attempt was made against, and losing it was
		// half of what R1 returned.
		writeChatExecutionError(w, *dec.Primary, profile, res, err)
		return
	}
	body := chatExecuteResponseDTO{
		Decision: dec, Served: *dec.Primary, FallbackUsed: false,
		Output: res.Output, Refusal: res.Refusal, Execution: chatExecutionDTOFor(profile, res),
	}
	if res.Usage != nil {
		body.InputTokens, body.OutputTokens = res.Usage.PromptTokens, res.Usage.CompletionTokens
	}
	writeJSON(w, http.StatusOK, body)
}

// writeChatExecutionError renders a closed error.
//
// The split is by whether an attempt REF EXISTS, and it is the honest one. Before a ref is
// minted nothing was prepared, nothing was decided against a target and no counter can
// exist, so the body is EXACTLY C2A's refusal shape — for the unavailable code it is
// produced by C2A's own constructor, so the two cannot drift by editing this file.
//
// Once refs exist the failure is ABOUT a governed attempt, so it carries that attempt's
// surviving observation: the execution states, the expected target, fallback_used=false,
// an explicit null output and the nullable counters the result reached. A caller that
// receives a 403 after the gateway reported 11 prompt and 7 completion tokens must not
// read it as "nothing was consumed", and a 429 that followed a real dispatch must not read
// as "nothing happened".
func writeChatExecutionError(w http.ResponseWriter, expected targetDTO, profile ExecutionProfile, res ChatExecutionResult, err error) {
	status, code, message, retryAfter := chatErrorResponse(err)
	if retryAfter != nil {
		w.Header().Set("Retry-After", strconv.FormatInt(*retryAfter, 10))
	}
	if strings.TrimSpace(res.AttemptRef) == "" {
		if code == ChatErrExecutionUnavailable {
			writeExecutionProfileError(w, chatExecutionUnavailable())
			return
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
		return
	}
	body := chatErrorResponseDTO{
		Error:          chatErrorObjectDTO{Code: code, Message: message},
		ExpectedTarget: expected,
		FallbackUsed:   false,
		// Never res.Output: no error row of the contract's table releases text, so the
		// null here is asserted rather than inherited from whatever the result holds.
		Output:    nil,
		Refusal:   res.Refusal,
		Execution: chatExecutionDTOFor(profile, res),
	}
	if res.Usage != nil {
		// The SAME pointers the success DTO publishes: a counter the gateway did not
		// report stays null, and an explicit zero stays zero.
		body.InputTokens, body.OutputTokens = res.Usage.PromptTokens, res.Usage.CompletionTokens
	}
	writeJSON(w, status, body)
}

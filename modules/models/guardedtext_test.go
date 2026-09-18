// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

// guardedtext_test.go qualifies the MODULE SIDE of the governed Chat path: that the profiled branch sits
// exactly where the contract puts it, that the existing budget precheck is consulted once
// and only through the handler's own closure, and that nothing about the legacy Messages/
// routed-executor path changed.

// stubChatExecutor records what the handler handed the port and returns what the test
// staged. It never dispatches anything.
type stubChatExecutor struct {
	res    models.ChatExecutionResult
	err    error
	calls  int
	seen   models.ChatExecutionRequest
	budget func(models.ChatBudgetCheck) // what the executor does with the precheck closure
}

func (s *stubChatExecutor) ExecuteChat(_ context.Context, in models.ChatExecutionRequest, budget models.ChatBudgetCheck) (models.ChatExecutionResult, error) {
	s.calls++
	s.seen = in
	if s.budget != nil {
		s.budget(budget)
	}
	return s.res, s.err
}

// countingBudgetGate is the FinOps seam under test conditions. It counts every consultation
// and records the dims, so "exactly once, about this primary, with this attribution ref" is
// a measurement.
type countingBudgetGate struct {
	decision models.BudgetDecision
	err      error
	calls    int
	dims     []models.BudgetDims
}

func (g *countingBudgetGate) Check(_ context.Context, _ model.TenantID, dims models.BudgetDims) (models.BudgetDecision, error) {
	g.calls++
	g.dims = append(g.dims, dims)
	return g.decision, g.err
}

// chatHarness builds one routing policy with a valid execution-profile pin, seeded so the
// decision resolves to the profile's exact target.
type chatHarness struct {
	h       *harness
	admin   string
	tenant  model.TenantID
	policy  string
	profile models.ExecutionProfile
}

func newChatHarness(t *testing.T, opts ...models.Option) *chatHarness {
	t.Helper()
	resolver := &fakeExecutionProfileResolver{profiles: map[string]models.ExecutionProfile{}}
	m := models.New(append([]models.Option{models.WithExecutionProfileResolver(resolver)}, opts...)...)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "chat-branch")
	p := testExecutionProfile(tenant)
	resolver.profiles[profileKey(tenant, p.Ref, p.Revision)] = p
	seedModel(t, h, tenant, p.ProviderRef, p.ModelRef)
	created := h.do("POST", "/v1/m/models/routing-policies", admin, profilePolicyBody(nil), tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("create profiled policy = %d %s", created.code, created.raw)
	}
	return &chatHarness{h: h, admin: admin, tenant: tenant, policy: created.body["id"].(string), profile: p}
}

func (c *chatHarness) execute(body map[string]any) resp {
	return c.h.do("POST", "/v1/m/models/routing-policies/"+c.policy+"/execute", c.admin, body, tenantHdr(c.tenant))
}

func chatExecution(t *testing.T, r resp) map[string]any {
	t.Helper()
	execution, _ := r.body["execution"].(map[string]any)
	if execution == nil {
		t.Fatalf("response carries no execution block: %s", r.raw)
	}
	return execution
}

// --- the deny-closed default ------------------------------------------------------------

// TestChatPortIsDenyClosedAndDoesNotConsultTheBudgetGate proves the two properties the
// contract asks of the unwired default: the SAME C2A refusal on the wire, and a refusal
// that happens BEFORE any I/O — including the fail-open budget precheck, which C2A used to
// run first.
func TestChatPortIsDenyClosedAndDoesNotConsultTheBudgetGate(t *testing.T) {
	gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: true}}
	legacy := &stubExecutor{res: models.ExecuteResult{Text: "the legacy executor must not run"}}
	c := newChatHarness(t, models.WithBudgetGate(gate), models.WithExecutor(legacy))

	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusServiceUnavailable || responseErrorCode(r) != "chat_execution_unavailable" {
		t.Fatalf("unwired Chat = %d %s", r.code, r.raw)
	}
	// The BYTES, not merely the code: this is the C2A refusal, produced by C2A's own
	// constructor, so the two cannot drift apart by editing the new code.
	const want = `{"error":{"code":"chat_execution_unavailable","message":"the pinned Chat execution path is not available in this build"}}`
	if strings.TrimSpace(r.raw) != want {
		t.Fatalf("refusal body changed:\n got: %s\nwant: %s", strings.TrimSpace(r.raw), want)
	}
	if gate.calls != 0 {
		t.Fatalf("the budget gate ran %d times for a refusal that precedes all I/O", gate.calls)
	}
	if legacy.calls != 0 {
		t.Fatalf("the legacy executor ran %d times for a profiled request", legacy.calls)
	}
}

// TestChatAvailabilityIsIndependentOfTheLegacyExecutor: wiring one never wires the other,
// in either direction.
func TestChatAvailabilityIsIndependentOfTheLegacyExecutor(t *testing.T) {
	t.Run("chat_wired_legacy_unwired", func(t *testing.T) {
		chat := &stubChatExecutor{res: models.ChatExecutionResult{
			RequestRef: "req", AttemptRef: "att", DispatchState: models.ChatDispatchAttempted,
			CompletionState: models.ChatCompletionStop, UsageStatus: models.ChatUsageNotReported,
			IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
			BudgetAssurance: models.ChatBudgetAssuranceDevelopmentPrecheck,
		}}
		c := newChatHarness(t, models.WithChatExecutor(chat))
		if r := c.execute(map[string]any{"input": "hello"}); r.code != http.StatusOK {
			t.Fatalf("chat execute = %d %s", r.code, r.raw)
		}
		// The legacy path stays deny-closed on a NON-profiled policy in the same module.
		id := createRoutingPolicy(t, c.h, c.admin, c.tenant)
		legacy := c.h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", c.admin,
			map[string]any{"input": "hello"}, tenantHdr(c.tenant))
		if legacy.code != http.StatusServiceUnavailable {
			t.Fatalf("wiring the Chat port also wired the legacy executor: %d %s", legacy.code, legacy.raw)
		}
	})
	t.Run("legacy_wired_chat_unwired", func(t *testing.T) {
		c := newChatHarness(t, models.WithExecutor(&stubExecutor{res: models.ExecuteResult{Text: "legacy"}}))
		if r := c.execute(map[string]any{"input": "hello"}); responseErrorCode(r) != "chat_execution_unavailable" {
			t.Fatalf("wiring the legacy executor also wired Chat: %d %s", r.code, r.raw)
		}
	})
}

// TestChatNilOptionKeepsTheDenyClosedDefault: WithChatExecutor(nil) must not blank the
// default into a nil interface the handler would then have to nil-check at request time.
func TestChatNilOptionKeepsTheDenyClosedDefault(t *testing.T) {
	c := newChatHarness(t, models.WithChatExecutor(nil))
	if r := c.execute(map[string]any{"input": "hello"}); responseErrorCode(r) != "chat_execution_unavailable" {
		t.Fatalf("a nil option changed the default: %d %s", r.code, r.raw)
	}
}

// --- the branch position and the budget closure --------------------------------------------

// TestChatBranchPrecedesTheBudgetGateAndTheClosureRunsExactlyOnce is the ordering causal for
// the handler: the branch is taken BEFORE the budget block, so the precheck runs only if
// and when the Chat composition asks for it — and then exactly once, about the primary the
// gates left standing and the body's attribution ref.
func TestChatBranchPrecedesTheBudgetGateAndTheClosureRunsExactlyOnce(t *testing.T) {
	gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: true}}
	chat := &stubChatExecutor{
		res: models.ChatExecutionResult{RequestRef: "req", AttemptRef: "att"},
		budget: func(check models.ChatBudgetCheck) {
			if status, denied := check(context.Background()); denied || status != 0 {
				panic("staged verdict is an allow")
			}
		},
	}
	c := newChatHarness(t, models.WithBudgetGate(gate), models.WithChatExecutor(chat))
	if r := c.execute(map[string]any{"input": "hello", "session_ref": "sess-42"}); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if gate.calls != 1 {
		t.Fatalf("the budget gate ran %d times, want exactly 1", gate.calls)
	}
	if gate.dims[0].ProviderRef != c.profile.ProviderRef || gate.dims[0].ModelRef != c.profile.ModelRef {
		t.Fatalf("the precheck was asked about %+v, not the pinned primary", gate.dims[0])
	}
	if gate.dims[0].SessionRef != "sess-42" {
		t.Fatalf("the precheck lost the attribution ref: %q", gate.dims[0].SessionRef)
	}
}

// TestChatBudgetClosureIsSingleShot: a composition that asked twice would consult the
// fail-open precheck twice for one effect. The second call returns a denial with NO spend
// status, which the composition is required to read as an internal deny rather than as a
// spend refusal — so the mistake cannot be mistaken for a budget cap.
func TestChatBudgetClosureIsSingleShot(t *testing.T) {
	gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: true}}
	var secondStatus int
	var secondDenied bool
	chat := &stubChatExecutor{
		res: models.ChatExecutionResult{RequestRef: "req", AttemptRef: "att"},
		budget: func(check models.ChatBudgetCheck) {
			_, _ = check(context.Background())
			secondStatus, secondDenied = check(context.Background())
		},
	}
	c := newChatHarness(t, models.WithBudgetGate(gate), models.WithChatExecutor(chat))
	if r := c.execute(map[string]any{"input": "hello"}); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if gate.calls != 1 {
		t.Fatalf("the underlying budget gate was consulted %d times, want 1", gate.calls)
	}
	if !secondDenied || secondStatus != 0 {
		t.Fatalf("the second call returned (%d, %v); want a denial with no spend status", secondStatus, secondDenied)
	}
}

// TestChatBudgetDenialDoesNotMutateTheReportedDecision: budgetDeniesRoute rewrites the
// decision it is handed. The closure hands it a PRIVATE COPY, target included, so a denial
// cannot reach through and blank the decision this handler reports.
func TestChatBudgetDenialDoesNotMutateTheReportedDecision(t *testing.T) {
	gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: false, Action: "block"}}
	chat := &stubChatExecutor{
		res: models.ChatExecutionResult{
			RequestRef: "req", AttemptRef: "att", DispatchState: models.ChatDispatchNotAttempted,
			CompletionState: models.ChatCompletionNotSent,
		},
		budget: func(check models.ChatBudgetCheck) { _, _ = check(context.Background()) },
	}
	c := newChatHarness(t, models.WithBudgetGate(gate), models.WithChatExecutor(chat))
	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	decision, _ := r.body["decision"].(map[string]any)
	if decision["resolved"] != true {
		t.Fatalf("the denial rewrote the reported decision: %s", r.raw)
	}
	if decision["budget_action"] != nil {
		t.Fatalf("the private copy's budget action leaked into the response: %s", r.raw)
	}
	served, _ := r.body["served"].(map[string]any)
	if served["model_ref"] != c.profile.ModelRef {
		t.Fatalf("the served target was rewritten: %s", r.raw)
	}
}

// TestChatRequestCarriesTheAuthenticatedIdentityAndTheRetainedPolicyRevision: what the port
// receives is authority the handler already established, plus the policy identity read in
// the decision transaction.
func TestChatRequestCarriesTheAuthenticatedIdentityAndTheRetainedPolicyRevision(t *testing.T) {
	chat := &stubChatExecutor{res: models.ChatExecutionResult{RequestRef: "req", AttemptRef: "att"}}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	if r := c.execute(map[string]any{"input": "hello", "session_ref": "sess-9", "max_tokens": 33}); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	seen := chat.seen
	if seen.Tenant != c.tenant {
		t.Fatalf("tenant = %s", seen.Tenant)
	}
	if seen.Principal.Kind == "" {
		t.Fatal("the port received no authenticated principal")
	}
	if seen.PolicyID.String() != c.policy || seen.PolicyVersion <= 0 {
		t.Fatalf("policy identity = %s v%d", seen.PolicyID, seen.PolicyVersion)
	}
	if seen.PolicySpecDigest == [32]byte{} {
		t.Fatal("the port received a zero routing-spec digest")
	}
	if seen.Profile != c.profile {
		t.Fatalf("the port received a different profile snapshot: %+v", seen.Profile)
	}
	if seen.Target.ProviderRef != c.profile.ProviderRef || seen.Target.ModelRef != c.profile.ModelRef {
		t.Fatalf("expected target = %+v", seen.Target)
	}
	if seen.Input != "hello" || seen.MaxTokens != 33 {
		t.Fatalf("input/bound = %q/%d", seen.Input, seen.MaxTokens)
	}
	// The attribution ref is CARRIED, and it is carried as attribution: the principal
	// beside it is the authenticated one, not one built from this string.
	if seen.SessionRef != "sess-9" {
		t.Fatalf("session ref = %q", seen.SessionRef)
	}
}

// TestChatSpecDigestTracksTheEffectiveRoutingSpec: a policy edit that changes a governing
// spec field must change the digest the evidence binds, or two different specs would be
// recorded as the same revision.
func TestChatSpecDigestTracksTheEffectiveRoutingSpec(t *testing.T) {
	chat := &stubChatExecutor{res: models.ChatExecutionResult{RequestRef: "req", AttemptRef: "att"}}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	if r := c.execute(map[string]any{"input": "hello"}); r.code != http.StatusOK {
		t.Fatalf("first execute = %d %s", r.code, r.raw)
	}
	before := chat.seen.PolicySpecDigest
	beforeVersion := chat.seen.PolicyVersion

	updated := profilePolicyBody(map[string]any{"require_zdr": true})
	if u := c.h.do("PUT", "/v1/m/models/routing-policies/"+c.policy, c.admin, updated, tenantHdr(c.tenant)); u.code != http.StatusOK {
		t.Fatalf("update = %d %s", u.code, u.raw)
	}
	if r := c.execute(map[string]any{"input": "hello"}); r.code != http.StatusOK {
		t.Fatalf("second execute = %d %s", r.code, r.raw)
	}
	if chat.seen.PolicySpecDigest == before {
		t.Fatal("a governing spec change did not move the routing-spec digest")
	}
	if chat.seen.PolicyVersion <= beforeVersion {
		t.Fatalf("policy version did not advance: %d then %d", beforeVersion, chat.seen.PolicyVersion)
	}
}

// --- results and errors ---------------------------------------------------------------------

// TestChatResultDistinguishesMissingUsageFromZero is the reason the Chat DTO is a separate
// type: the legacy DTO's bare int64 cannot express "the provider reported no counter", and
// a zero written into that field is indistinguishable from a free call.
func TestChatResultDistinguishesMissingUsageFromZero(t *testing.T) {
	zero, seven := int64(0), int64(7)
	tests := []struct {
		name  string
		usage *mp.ChatTextUsage
		in    any
		out   any
	}{
		{name: "absent", usage: nil, in: nil, out: nil},
		{name: "explicit_zero", usage: &mp.ChatTextUsage{PromptTokens: &zero, CompletionTokens: &zero}, in: float64(0), out: float64(0)},
		{name: "partial", usage: &mp.ChatTextUsage{CompletionTokens: &seven}, in: nil, out: float64(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := &stubChatExecutor{res: models.ChatExecutionResult{
				RequestRef: "req", AttemptRef: "att", Usage: tt.usage,
				DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionStop,
			}}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			r := c.execute(map[string]any{"input": "hello"})
			if r.code != http.StatusOK {
				t.Fatalf("execute = %d %s", r.code, r.raw)
			}
			if !strings.Contains(r.raw, `"input_tokens"`) || !strings.Contains(r.raw, `"output_tokens"`) {
				t.Fatalf("the counters were omitted rather than reported as null: %s", r.raw)
			}
			if r.body["input_tokens"] != tt.in || r.body["output_tokens"] != tt.out {
				t.Fatalf("counters = %v/%v, want %v/%v", r.body["input_tokens"], r.body["output_tokens"], tt.in, tt.out)
			}
			// No monetary field of any kind rides on this surface.
			for _, forbidden := range []string{"cost", "micro_usd", "usd", "price"} {
				if strings.Contains(strings.ToLower(r.raw), forbidden) {
					t.Fatalf("the Chat result carries a money-shaped field %q: %s", forbidden, r.raw)
				}
			}
		})
	}
}

// TestChatResultAlwaysNamesItsProfileAndNeverClaimsAFallback: the execution block is the
// closed vocabulary a caller can act on, and fallback_used is false because a Chat profile
// pins one target and no chain is ever tried.
func TestChatResultAlwaysNamesItsProfileAndNeverClaimsAFallback(t *testing.T) {
	chat := &stubChatExecutor{res: models.ChatExecutionResult{
		RequestRef: "req-1", AttemptRef: "att-1", DispatchState: models.ChatDispatchAttempted,
		CompletionState: models.ChatCompletionStop, UsageStatus: models.ChatUsageNotReported,
		IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
		BudgetAssurance: models.ChatBudgetAssuranceDevelopmentPrecheck,
	}}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if r.body["fallback_used"] != false {
		t.Fatalf("fallback_used = %v", r.body["fallback_used"])
	}
	execution := chatExecution(t, r)
	for key, want := range map[string]any{
		"profile_ref": c.profile.Ref, "profile_revision": c.profile.Revision,
		"action": c.profile.Action, "protocol": c.profile.Protocol, "surface": c.profile.Surface,
		"credential_audience": c.profile.CredentialAudience,
		"request_ref":         "req-1", "attempt_ref": "att-1",
		"dispatch_state": models.ChatDispatchAttempted, "completion_state": models.ChatCompletionStop,
		"usage_status": models.ChatUsageNotReported, "intent_disposition": models.ChatIntentAnchored,
		"outcome_disposition": models.ChatOutcomeAnchored,
		"budget_assurance":    models.ChatBudgetAssuranceDevelopmentPrecheck,
	} {
		if execution[key] != want {
			t.Fatalf("execution[%q] = %v, want %v", key, execution[key], want)
		}
	}
	// The endpoint is bound by the profile revision and the effect digest; the only
	// endpoint-derived value published is the public audience.
	if strings.Contains(r.raw, "/v1/chat/completions") {
		t.Fatalf("the result published the endpoint path: %s", r.raw)
	}
}

// TestChatErrorMappingIsClosedInBothDirections: every code renders at its canonical status
// with its fixed message, and anything else — an unknown code, a code paired with the wrong
// status, a foreign error — becomes the fixed internal failure rather than reaching the
// caller.
func TestChatErrorMappingIsClosedInBothDirections(t *testing.T) {
	known := map[string]int{
		models.ChatErrExecutionUnavailable:     http.StatusServiceUnavailable,
		models.ChatErrExecutionNotReady:        http.StatusServiceUnavailable,
		models.ChatErrRequestCancelled:         http.StatusRequestTimeout,
		models.ChatErrRequestInvalid:           http.StatusBadRequest,
		models.ChatErrRequestTooLarge:          http.StatusRequestEntityTooLarge,
		models.ChatErrProfileUnavailable:       http.StatusServiceUnavailable,
		models.ChatErrGovernanceUnavailable:    http.StatusServiceUnavailable,
		models.ChatErrResidencyDenied:          http.StatusForbidden,
		models.ChatErrContextPolicyDenied:      http.StatusForbidden,
		models.ChatErrContextLimitUnverifiable: http.StatusUnprocessableEntity,
		models.ChatErrRedactionUnsupported:     http.StatusUnprocessableEntity,
		models.ChatErrRequestCeilingExceeded:   http.StatusPaymentRequired,
		models.ChatErrContentDenied:            http.StatusForbidden,
		models.ChatErrInspectionDenied:         http.StatusForbidden,
		models.ChatErrCircuitOpen:              http.StatusServiceUnavailable,
		models.ChatErrBudgetDenied:             http.StatusPaymentRequired,
		models.ChatErrBudgetThrottled:          http.StatusTooManyRequests,
		models.ChatErrRecordingUnavailable:     http.StatusServiceUnavailable,
		models.ChatErrCredentialUnavailable:    http.StatusServiceUnavailable,
		models.ChatErrUpstreamRateLimited:      http.StatusTooManyRequests,
		models.ChatErrUpstreamUnavailable:      http.StatusBadGateway,
		models.ChatErrUpstreamTimeout:          http.StatusGatewayTimeout,
		models.ChatErrProtocolError:            http.StatusBadGateway,
		models.ChatErrOutputWithheld:           http.StatusForbidden,
		models.ChatErrInternal:                 http.StatusInternalServerError,
	}
	messages := map[string]bool{}
	for code, status := range known {
		t.Run(code, func(t *testing.T) {
			chat := &stubChatExecutor{err: models.NewChatExecutionError(code)}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			r := c.execute(map[string]any{"input": "hello"})
			if r.code != status {
				t.Fatalf("%s = %d, want %d", code, r.code, status)
			}
			if responseErrorCode(r) != code {
				t.Fatalf("%s rendered as %q", code, responseErrorCode(r))
			}
			body, _ := r.body["error"].(map[string]any)
			message, _ := body["message"].(string)
			if message == "" {
				t.Fatalf("%s has no fixed message", code)
			}
			messages[message] = true
		})
	}
	if len(messages) != len(known) {
		t.Fatalf("%d codes share %d messages; a shared message hides which gate refused", len(known), len(messages))
	}

	t.Run("refused_shapes", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			err  error
		}{
			{"unknown_code", &models.ChatExecutionError{Code: "chat_made_up", HTTPStatus: http.StatusTeapot}},
			{"code_paired_with_the_wrong_status", &models.ChatExecutionError{Code: models.ChatErrBudgetDenied, HTTPStatus: http.StatusOK}},
			{"a_foreign_error", errors.New("connection refused dialing https://provider.invalid: bad token abc123")},
		} {
			t.Run(tt.name, func(t *testing.T) {
				chat := &stubChatExecutor{err: tt.err}
				c := newChatHarness(t, models.WithChatExecutor(chat))
				r := c.execute(map[string]any{"input": "hello"})
				if r.code != http.StatusInternalServerError || responseErrorCode(r) != models.ChatErrInternal {
					t.Fatalf("%s = %d %s", tt.name, r.code, r.raw)
				}
				if strings.Contains(r.raw, "provider.invalid") || strings.Contains(r.raw, "abc123") {
					t.Fatalf("a foreign error's text reached the caller: %s", r.raw)
				}
			})
		}
	})
}

// TestChatExecutionErrorDoesNotUnwrap: the error type wraps nothing, so no errors.As on a
// transport, codec, store or resolver error can reach a caller through it, and no log line
// built from it can print one.
func TestChatExecutionErrorDoesNotUnwrap(t *testing.T) {
	err := models.NewChatExecutionError(models.ChatErrUpstreamUnavailable)
	var unwrapper interface{ Unwrap() error }
	if errors.As(error(err), &unwrapper) {
		t.Fatal("*ChatExecutionError exposes Unwrap; a wrapped provider error could travel through it")
	}
	if err.Error() != "the pinned gateway did not complete this attempt" {
		t.Fatalf("Error() = %q", err.Error())
	}
	// An unrecognized code degrades to the internal message instead of echoing itself.
	if (&models.ChatExecutionError{Code: "echo-me"}).Error() != "governed Chat execution failed" {
		t.Fatal("an unknown code echoed itself through Error()")
	}
}

// TestChatRateLimitCarriesOnlyItsValidatedHintAndReportsTheAttempt: a 429 that FOLLOWED a
// real dispatch must not read as "nothing happened", and the only Retry-After it can carry
// is the one C1 validated.
func TestChatRateLimitCarriesOnlyItsValidatedHintAndReportsTheAttempt(t *testing.T) {
	seconds := int64(9)
	err := models.NewChatExecutionError(models.ChatErrUpstreamRateLimited)
	err.RetryAfterSeconds = &seconds
	chat := &stubChatExecutor{
		err: err,
		res: models.ChatExecutionResult{
			RequestRef: "req", AttemptRef: "att",
			DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionEffectUnknown,
			UsageStatus: models.ChatUsageUnknown, IntentDisposition: models.ChatIntentAnchored,
			OutcomeDisposition: models.ChatOutcomeAnchored,
		},
	}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusTooManyRequests {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	execution := chatExecution(t, r)
	if execution["dispatch_state"] != models.ChatDispatchAttempted ||
		execution["completion_state"] != models.ChatCompletionEffectUnknown {
		t.Fatalf("an attempted effect was reported as %v/%v", execution["dispatch_state"], execution["completion_state"])
	}
	if execution["usage_status"] != models.ChatUsageUnknown {
		t.Fatalf("usage status = %v; an uncertain effect is unknown, never zero", execution["usage_status"])
	}
	if r.body["output"] != nil {
		t.Fatalf("an error released output: %s", r.raw)
	}
}

// TestChatErrorBeforeAnAttemptKeepsTheC2AErrorShape: a refusal that never minted an attempt
// reports the plain error object, not an execution block describing an attempt that does
// not exist.
func TestChatErrorBeforeAnAttemptKeepsTheC2AErrorShape(t *testing.T) {
	chat := &stubChatExecutor{err: models.NewChatExecutionError(models.ChatErrContentDenied)}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusForbidden {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if _, present := r.body["execution"]; present {
		t.Fatalf("a pre-attempt refusal described an attempt: %s", r.raw)
	}
	var keys []string
	for k := range r.body {
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != "error" {
		t.Fatalf("the refusal body carries %v, want only an error object", keys)
	}
}

// TestChatErrorAfterAnAttemptPreservesTheKnownResult is R1's causal on the module side: a
// failure that FOLLOWED a minted attempt must not erase what the attempt observed.
//
// It asserts KEY EXISTENCE and not only values, because the defect it guards is a MISSING
// key: a `body["output"]` lookup returns nil both when the key is present and null and when
// it is absent altogether, so the older assertion passed against a body that published
// neither the output nor the counters.
func TestChatErrorAfterAnAttemptPreservesTheKnownResult(t *testing.T) {
	// Known counters with an EXPLICIT ZERO on the output side: withholding the text does
	// not make the prompt tokens unknown, and 0 completion tokens is a reported zero, not
	// a missing counter.
	prompt, completion := int64(11), int64(0)
	usage := &mp.ChatTextUsage{PromptTokens: &prompt, CompletionTokens: &completion}

	tests := []struct {
		name       string
		code       string
		status     int
		res        models.ChatExecutionResult
		wantInput  string // the RAW JSON of input_tokens, so null and 0 are distinguishable
		wantOutput string
	}{
		{
			name: "output_withheld_with_partial_known_usage", code: models.ChatErrOutputWithheld,
			status: http.StatusForbidden,
			res: models.ChatExecutionResult{
				RequestRef: "req", AttemptRef: "att",
				DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionOutputWithheld,
				UsageStatus: models.ChatUsageReported, Usage: usage,
				IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
			},
			wantInput: "11", wantOutput: "0",
		},
		{
			name: "model_mismatch_keeps_its_observation", code: models.ChatErrProtocolError,
			status: http.StatusBadGateway,
			res: models.ChatExecutionResult{
				RequestRef: "req", AttemptRef: "att",
				DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionProtocolError,
				UsageStatus: models.ChatUsageReported, Usage: usage,
				IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
			},
			wantInput: "11", wantOutput: "0",
		},
		{
			// An uncertain effect reported NO usage. The counters must be null — present
			// and unknown — never absent and never zero.
			name: "unknown_usage_is_null_not_zero", code: models.ChatErrUpstreamUnavailable,
			status: http.StatusBadGateway,
			res: models.ChatExecutionResult{
				RequestRef: "req", AttemptRef: "att",
				DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionEffectUnknown,
				UsageStatus:       models.ChatUsageUnknown,
				IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
			},
			wantInput: "null", wantOutput: "null",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := &stubChatExecutor{err: models.NewChatExecutionError(tt.code), res: tt.res}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			r := c.execute(map[string]any{"input": "hello"})
			if r.code != tt.status || responseErrorCode(r) != tt.code {
				t.Fatalf("= %d %s, want %d/%s", r.code, r.raw, tt.status, tt.code)
			}

			var decoded map[string]json.RawMessage
			if err := json.Unmarshal([]byte(r.raw), &decoded); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			want := []string{"error", "expected_target", "fallback_used", "output", "input_tokens", "output_tokens", "refusal", "execution"}
			for _, key := range want {
				if _, ok := decoded[key]; !ok {
					t.Fatalf("the error body has no %q: %s", key, r.raw)
				}
			}
			if len(decoded) != len(want) {
				t.Fatalf("the error body has %d members, want exactly %d: %s", len(decoded), len(want), r.raw)
			}
			// PRESENT and null, which is not the same as absent.
			if string(decoded["output"]) != "null" {
				t.Fatalf("output = %s, want an explicit null", decoded["output"])
			}
			if string(decoded["input_tokens"]) != tt.wantInput || string(decoded["output_tokens"]) != tt.wantOutput {
				t.Fatalf("counters = %s/%s, want %s/%s", decoded["input_tokens"], decoded["output_tokens"], tt.wantInput, tt.wantOutput)
			}
			if string(decoded["fallback_used"]) != "false" {
				t.Fatalf("fallback_used = %s; a Chat profile pins one target and tries no chain", decoded["fallback_used"])
			}
			// The EXPECTED target, named as expected: an error body must not carry a key
			// called "served", which would read as proof of service.
			var expected map[string]any
			if err := json.Unmarshal(decoded["expected_target"], &expected); err != nil {
				t.Fatalf("decode expected_target: %v", err)
			}
			if expected["provider_ref"] != c.profile.ProviderRef || expected["model_ref"] != c.profile.ModelRef {
				t.Fatalf("expected_target = %v, want the pinned primary %s/%s", expected, c.profile.ProviderRef, c.profile.ModelRef)
			}
			if _, present := decoded["served"]; present {
				t.Fatalf("a failure body claimed a served target: %s", r.raw)
			}
			execution := chatExecution(t, r)
			if execution["dispatch_state"] != tt.res.DispatchState || execution["completion_state"] != tt.res.CompletionState {
				t.Fatalf("execution states = %v/%v", execution["dispatch_state"], execution["completion_state"])
			}
			if execution["usage_status"] != tt.res.UsageStatus {
				t.Fatalf("usage_status = %v, want %s", execution["usage_status"], tt.res.UsageStatus)
			}
		})
	}
}

// TestChatErrorBodyNeverCarriesTextOrRefusalContent: the error DTO gained fields, so the
// control that they are content-free travels with it. A result holding output and a refusal
// still publishes neither.
func TestChatErrorBodyNeverCarriesTextOrRefusalContent(t *testing.T) {
	secret := "the model output that must never appear in a failure"
	chat := &stubChatExecutor{
		err: models.NewChatExecutionError(models.ChatErrOutputWithheld),
		res: models.ChatExecutionResult{
			RequestRef: "req", AttemptRef: "att", Output: &secret, Refusal: true,
			DispatchState: models.ChatDispatchAttempted, CompletionState: models.ChatCompletionOutputWithheld,
			UsageStatus:       models.ChatUsageNotReported,
			IntentDisposition: models.ChatIntentAnchored, OutcomeDisposition: models.ChatOutcomeAnchored,
		},
	}
	c := newChatHarness(t, models.WithChatExecutor(chat))
	r := c.execute(map[string]any{"input": "hello"})
	if r.code != http.StatusForbidden {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, secret) {
		t.Fatalf("a withheld output reached the caller through the error body: %s", r.raw)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &decoded); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if string(decoded["output"]) != "null" {
		t.Fatalf("output = %s; an error never releases text, whatever the result holds", decoded["output"])
	}
	// The BOOLEAN travels — the caller learns that a refusal happened — while the string
	// never does. That distinction is the contract, so it is asserted in both directions.
	if string(decoded["refusal"]) != "true" {
		t.Fatalf("refusal = %s, want the boolean the result carried", decoded["refusal"])
	}
}

// --- the legacy path is untouched ----------------------------------------------------------------

// TestLegacyExecutePathIsUnchangedByTheChatBranch: a policy with no profile pin still runs
// the legacy executor, still returns the legacy DTO with its bare-int64 counters, and gains
// no Chat field. The branch is invisible to it.
func TestLegacyExecutePathIsUnchangedByTheChatBranch(t *testing.T) {
	gate := &countingBudgetGate{decision: models.BudgetDecision{Allowed: true}}
	legacy := &stubExecutor{res: models.ExecuteResult{Text: "legacy output", InputTokens: 5, OutputTokens: 0}}
	chat := &stubChatExecutor{}
	m := models.New(models.WithExecutor(legacy), models.WithChatExecutor(chat), models.WithBudgetGate(gate))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "legacy-untouched")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin,
		map[string]any{"input": "hello", "session_ref": "s"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["output"] != "legacy output" {
		t.Fatalf("legacy execute = %d %s", r.code, r.raw)
	}
	if legacy.calls != 1 || chat.calls != 0 {
		t.Fatalf("legacy=%d chat=%d", legacy.calls, chat.calls)
	}
	if gate.calls != 1 {
		t.Fatalf("the legacy budget gate ran %d times, want 1", gate.calls)
	}
	// The legacy DTO keeps its shape: bare counters, no execution block, no profile fields.
	if r.body["input_tokens"] != float64(5) || r.body["output_tokens"] != float64(0) {
		t.Fatalf("legacy counters changed: %s", r.raw)
	}
	for _, forbidden := range []string{"execution", "profile_ref", "dispatch_state", "budget_assurance"} {
		if strings.Contains(r.raw, forbidden) {
			t.Fatalf("the legacy response gained %q: %s", forbidden, r.raw)
		}
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.raw), &decoded); err != nil {
		t.Fatalf("decode legacy response: %v", err)
	}
	want := []string{"decision", "served", "fallback_used", "output", "input_tokens", "output_tokens", "refusal"}
	if len(decoded) != len(want) {
		t.Fatalf("the legacy response has %d members, want exactly %d: %s", len(decoded), len(want), r.raw)
	}
	for _, key := range want {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("the legacy response lost %q: %s", key, r.raw)
		}
	}
}

// TestChatBranchStillHonoursTheExistingGatePrecedence: the C2A error precedence and every
// deny-closed handler gate run BEFORE the branch, so a request that fails one of them never
// reaches the Chat port at all.
func TestChatBranchStillHonoursTheExistingGatePrecedence(t *testing.T) {
	tests := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{"unsupported_operation", map[string]any{"input": "hello", "operation": "embeddings.create"}, http.StatusUnprocessableEntity, "unsupported_operation"},
		{"surface_mismatch", map[string]any{"input": "hello", "surface": "bedrock"}, http.StatusUnprocessableEntity, "profile_binding_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := &stubChatExecutor{}
			c := newChatHarness(t, models.WithChatExecutor(chat))
			r := c.execute(tt.body)
			if r.code != tt.status || responseErrorCode(r) != tt.code {
				t.Fatalf("= %d %s, want %d/%s", r.code, r.raw, tt.status, tt.code)
			}
			if chat.calls != 0 {
				t.Fatalf("the Chat port ran %d times behind a handler gate", chat.calls)
			}
		})
	}
	t.Run("kill_switch_precedes_the_branch", func(t *testing.T) {
		chat := &stubChatExecutor{}
		c := newChatHarness(t, models.WithChatExecutor(chat), models.WithStopGate(stoppedChatStopGate{}))
		r := c.execute(map[string]any{"input": "hello"})
		if r.code != http.StatusLocked {
			t.Fatalf("kill switch = %d %s", r.code, r.raw)
		}
		if chat.calls != 0 {
			t.Fatalf("the Chat port ran %d times behind an active emergency stop", chat.calls)
		}
	})
}

type stoppedChatStopGate struct{}

func (stoppedChatStopGate) Check(context.Context, model.TenantID) (models.StopDecision, error) {
	return models.StopDecision{Stopped: true, StopRef: "stop-1"}, nil
}

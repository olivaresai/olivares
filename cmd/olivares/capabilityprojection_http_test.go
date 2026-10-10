// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// G1-A — the SELF capability projection, driven through the production router on a
// fresh, explicitly activated estate.
//
// Every case below is CAUSAL: it builds a real principal with real authority through
// the product's own APIs and measures what the projection says next to what the REAL
// route answers for the same request. There is no can()=>true, no injected role, no
// forged membership and no fabricated evidence anywhere in this file; the one fault
// that is injected is a real resolver failure through the production composition seam,
// and it exists to prove that an unavailable authority is UNKNOWN rather than a denial.

const (
	capSurfaceAdministration = "GET /v1/m/sessions/channels/administration"
	capSurfaceChannels       = "GET /v1/m/sessions/channels"
	capSurfaceInbox          = "GET /v1/m/sessions/inbox"
	capOperationGrantSheet   = "GET /v1/m/sessions/channels/{id}/grants"
	capOperationPatch        = "PATCH /v1/m/sessions/channels"
	capOperationGrant        = "POST /v1/m/sessions/channels/{id}/grants"
	capOperationRevoke       = "POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke"
)

type capabilityAnswer struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"`
	Code           string    `json:"code"`
	ObservedAt     time.Time `json:"observed_at"`
	RefreshAfterMS *int64    `json:"refresh_after_ms"`
}

type capabilityResults struct {
	SchemaVersion int                `json:"schema_version"`
	Results       []capabilityAnswer `json:"results"`
}

func capabilitySurfaceQuestion(id, operation, workspace string) map[string]any {
	return map[string]any{
		"id": id, "kind": "surface", "operation": operation, "workspace_id": workspace,
	}
}

func capabilityChannelQuestion(id, operation, workspace string, channel model.ID) map[string]any {
	question := map[string]any{
		"id": id, "kind": "operation", "operation": operation,
		"selectors": map[string]any{"path": map[string]any{"id": channel.String()}},
	}
	if workspace != "" {
		question["workspace_id"] = workspace
	}
	return question
}

func capabilityBodyQuestion(id, operation, workspace string, channel model.ID) map[string]any {
	question := map[string]any{
		"id": id, "kind": "operation", "operation": operation,
		"selectors": map[string]any{"body": map[string]any{"channel_id": channel.String()}},
	}
	if workspace != "" {
		question["workspace_id"] = workspace
	}
	return question
}

func capabilityAskRaw(
	t *testing.T, eng *engine, token string, tenant model.TenantID, questions ...map[string]any,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/capabilities",
		token, tenant, map[string]any{"schema_version": 2, "questions": questions}, nil)
}

// capabilityAsk drives one batch and returns the answers by id. It asserts the two
// transport promises on every call rather than once: 200 for a well-formed batch, and
// no-store — these bodies are one credential's authorization observations, and a private
// cache replaying one to the next caller is the failure the whole freshness contract
// exists to prevent.
func capabilityAsk(
	t *testing.T, eng *engine, token string, tenant model.TenantID, questions ...map[string]any,
) map[string]capabilityAnswer {
	t.Helper()
	response := capabilityAskRaw(t, eng, token, tenant, questions...)
	if response.status != http.StatusOK {
		t.Fatalf("capabilities = %d: %s", response.status, response.raw)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("capabilities Cache-Control = %q, want no-store", got)
	}
	decoded := communicationHTTPTestDecode[capabilityResults](t, response)
	if decoded.SchemaVersion != 2 || len(decoded.Results) != len(questions) {
		t.Fatalf("capabilities envelope = %+v, want schema 2 and %d results",
			decoded, len(questions))
	}
	out := make(map[string]capabilityAnswer, len(decoded.Results))
	for _, answer := range decoded.Results {
		if _, duplicate := out[answer.ID]; duplicate {
			t.Fatalf("capabilities returned two results for id %q", answer.ID)
		}
		out[answer.ID] = answer
	}
	return out
}

// capabilityWant asserts the state and code of one answer AND the budget invariant that
// goes with them: a positive carries a finite refresh_after_ms inside the 30s ceiling, a
// non-positive carries none at all. The second half is not decoration — a denial or an
// unknown that shipped a budget would be handing a client a reason to cache it, and on a
// concealing route a budget attached only to real rows would itself be the existence
// oracle the concealment exists to close.
func capabilityWant(
	t *testing.T, answers map[string]capabilityAnswer, id, state, code string,
) capabilityAnswer {
	t.Helper()
	answer, present := answers[id]
	if !present {
		t.Fatalf("capabilities returned no result for %q", id)
	}
	if answer.State != state || answer.Code != code {
		t.Fatalf("%s = %s/%s, want %s/%s", id, answer.State, answer.Code, state, code)
	}
	positive := state == "allowed" || state == "reachable"
	switch {
	case positive && answer.RefreshAfterMS == nil:
		t.Fatalf("%s is %s with no refresh_after_ms: a positive with no budget is not reusable", id, state)
	case positive && (*answer.RefreshAfterMS <= 0 || *answer.RefreshAfterMS > 30000):
		t.Fatalf("%s refresh_after_ms = %d, want 0 < ms <= 30000", id, *answer.RefreshAfterMS)
	case !positive && answer.RefreshAfterMS != nil:
		t.Fatalf("%s is %s and still carries refresh_after_ms=%d", id, state, *answer.RefreshAfterMS)
	}
	if answer.ObservedAt.IsZero() {
		t.Fatalf("%s carries no observed_at", id)
	}
	return answer
}

// capabilityPublishAuthored publishes ONE authored Cedar source for the tenant through
// the product's own PDP authoring surface.
//
// ⛔ IT REPLACES THE AUTHORED SURFACE, so a caller that needs two policies at once
// publishes them in one source rather than in two calls.
//
// ⛔ AND IT IS THE AUTHORING PATH THIS BATTERY USES INSTEAD OF A WORKSPACE-SCOPED RBAC
// GRANT, WHICH IS A FINDING RATHER THAN A PREFERENCE. POST /v1/m/governance/rbac/grants
// REFUSES a workspace-scoped grant whose whole permission set is module permissions
// (modules/governance/scopedadmin_handlers.go rejectInertTreeScopedGrant), on the stated
// ground that "module routes do not resolve the workspace/agent-group/folder tree, so the
// grant would authorize nothing". That premise is what G1-A changes for the three pilot
// collections — and the guard's own comment says so: "When module routes resolve the
// tree, this guard is what gets removed". Removing it is an authorization WIDENING across
// every module, plus a second failure mode about the unconditional delegation permit that
// this work does not touch, so it is referred to root rather than taken here. The authored
// Cedar below expresses the same workspace-scoped authority through a shipped, supported
// surface, so the causal claim is measured without changing a guard.
func capabilityPublishAuthored(
	t *testing.T, eng *engine, estate channelAdministrationHTTPEstate, source string,
) {
	t.Helper()
	published := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/pdp/publish", estate.owner.token, estate.tenant,
		map[string]any{"engine": "cedar", "source": source}, nil)
	if published.status != http.StatusOK {
		t.Fatalf("publish authored policy = %d: %s", published.status, published.raw)
	}
	t.Logf("K3_CAP_AUTHORED|source=%s", source)
}

// capabilityArchivedWorkspace creates a workspace of this tenant and takes it out of the
// active state, so the battery can ask about a real non-active scope instead of a
// hypothetical one.
func capabilityArchivedWorkspace(
	t *testing.T, eng *engine, estate channelAdministrationHTTPEstate,
) model.ID {
	t.Helper()
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/workspaces",
		estate.owner.token, estate.tenant,
		map[string]any{"name": "K3 capability archived", "slug": "k3-cap-archived"}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create the archived workspace = %d: %s", created.status, created.raw)
	}
	id, err := model.ParseID(communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, created).ID)
	if err != nil {
		t.Fatalf("archived workspace id: %v", err)
	}
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch,
		"/v1/workspaces/"+id.String(), estate.owner.token, estate.tenant,
		map[string]any{"status": "inactive"}, nil)
	if patched.status != http.StatusOK {
		t.Fatalf("archive the workspace = %d: %s", patched.status, patched.raw)
	}
	return id
}

// capabilityForeignChannel creates a Channel in the OTHER tenant, so the concealment
// case names a row that really exists somewhere else rather than a random id.
func capabilityForeignChannel(t *testing.T, estate channelAdministrationHTTPEstate) model.ID {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/channels", estate.stranger.token, estate.other,
		map[string]any{
			"workspace_id": estate.foreignWorkspace, "slug": "k3-cap-foreign",
			"name": "Foreign capability channel",
			"initial_grants": []map[string]any{channelAdministrationGrant(
				channelAdministrationSubject("user", estate.stranger.id), true, true, true)},
		}, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("create the foreign Channel = %d: %s", response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response).Channel.ID
}

// independentG1AScopedOutage and independentG1AOverlayOutage are FAULT-ONLY producers.
// Neither fabricates a principal, a membership, a permission or a verdict: each returns
// an error, which is exactly what a real evaluator does when it cannot be evaluated.
type independentG1AScopedOutage struct{}

func (independentG1AScopedOutage) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{}, errors.New("capability battery: scoped provider unavailable")
}

func (independentG1AScopedOutage) ScopedEvidence(
	context.Context, auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	return auth.ScopedEvidenceDecision{}, errors.New("capability battery: scoped evidence unavailable")
}

// independentG1ADisagreeingScoped is the third arm the correction brief names: the
// BOOLEAN and the TYPED path disagreeing about the same question. Its legacy Scoped
// abstains — so Authorize falls through to RBAC and ALLOWS — while its typed
// ScopedEvidence reports an established forbid. Neither answer is fabricated; they simply
// do not agree, which in production would be a bug in a producer rather than a policy.
type independentG1ADisagreeingScoped struct{}

func (independentG1ADisagreeingScoped) Scoped(context.Context, auth.Request) (auth.ScopedDecision, error) {
	return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "capability battery: abstain"}, nil
}

func (independentG1ADisagreeingScoped) ScopedEvidence(
	context.Context, auth.Request,
) (auth.ScopedEvidenceDecision, error) {
	now := time.Now().UTC()
	return auth.ScopedEvidenceDecision{
		Effect:        auth.EffectForbid,
		ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "battery_guard_clean"},
		ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckBroken, Code: "battery_forbid_present"},
		ObservedAt:    now,
		FreshUntil:    now.Add(time.Minute),
	}, nil
}

type independentG1AOverlayOutage struct{}

func (independentG1AOverlayOutage) Evaluate(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{}, errors.New("capability battery: policy overlay unavailable")
}

func (independentG1AOverlayOutage) EvaluateEvidence(
	context.Context, auth.Request,
) (auth.PolicyEvidenceDecision, error) {
	return auth.PolicyEvidenceDecision{}, errors.New("capability battery: policy evidence unavailable")
}

// capabilitySameShape asserts that several answers are INDISTINGUISHABLE on the wire.
//
// ⛔ IT COMPARES THE PUBLIC SHAPE, NOT THE WHOLE VALUE, and that is a real distinction
// rather than a convenience: `observed_at` is read per question, so two answers taken
// microseconds apart differ in a field that says nothing about the target. What must not
// differ is what a caller could use to tell one target from another — the state, the code
// and whether a budget came back at all.
func capabilitySameShape(
	t *testing.T, answers map[string]capabilityAnswer, what string, ids ...string,
) {
	t.Helper()
	type shape struct {
		state, code string
		budget      bool
	}
	shapeOf := func(id string) shape {
		answer, present := answers[id]
		if !present {
			t.Fatalf("%s: no answer for %q", what, id)
		}
		return shape{answer.State, answer.Code, answer.RefreshAfterMS != nil}
	}
	first := shapeOf(ids[0])
	for _, id := range ids[1:] {
		if got := shapeOf(id); got != first {
			t.Fatalf("%s answers %q as %+v and %q as %+v: a caller can tell the targets apart, "+
				"which is an existence oracle", what, ids[0], first, id, got)
		}
	}
}

// TestCapabilityProjectionDiscriminantsHTTP is the permanent home of the six
// discriminants raised by the INDEPENDENT REVIEW of the first G1-A candidate
// (`an internal design note (not shipped)`,
// SHA-256 2e9875c2dcace018b43aea349dbbe82271deb9c730d04c3a40ed738878cf1ffa, findings
// finding 1–R5). The cases are the reviewer's; this file adopts them so the defects cannot
// return, and each one carries the finding it belongs to.
//
// Every one of them FAILED on commit f3fbe5b34ca55b80496b475c3c98df576372b9e5 while all
// eleven of the author's own causal cases passed — which is the reason they are here
// rather than trusted to the existing battery. A positive suite that cannot fail on the
// defect is not coverage of it.
func TestCapabilityProjectionDiscriminantsHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	steward := estate.steward
	ws := workspace.String()
	wsQuery := "workspace_id=" + ws

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", estate.owner.id), true, true, true)
	stewardAdmin := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)
	held := estate.createChannel(t, workspace, "discriminant-held", []map[string]any{ownerAll, stewardAdmin})
	hidden := estate.createChannel(t, workspace, "discriminant-hidden", []map[string]any{ownerAll})
	missing := model.NewID()
	before := communicationHTTPTestEffects(t, eng, tenant)

	// The positive control. Without it every refusal below could pass for the wrong
	// reason — a projection that answered UNKNOWN to everything would satisfy R1 and R3
	// and be useless.
	t.Run("control: admin-only authority is allowed and an unheld channel is concealed", func(t *testing.T) {
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID),
			capabilityChannelQuestion("hidden", capOperationGrantSheet, ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", capOperationGrantSheet, ws, missing))
		capabilityWant(t, answers, "held", "allowed", "authorized")
		capabilityWant(t, answers, "hidden", "undisclosed", "not_disclosed")
		capabilityWant(t, answers, "missing", "undisclosed", "not_disclosed")
		if got := estate.sheet(t, steward.token, held.Channel.ID, wsQuery); got.status != http.StatusOK {
			t.Fatalf("real held admin sheet = %d: %s", got.status, got.raw)
		}
		read := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+held.Channel.ID.String()+"?"+wsQuery,
			steward.token, tenant, nil, nil)
		if read.status != http.StatusNotFound {
			t.Fatalf("admin-only content read = %d, want the concealed 404: %s", read.status, read.raw)
		}
	})

	// R1, first half: a HEALTHY runtime must not distinguish existence through the
	// adapter's absence. Measured on the rejected candidate: existing hidden channel
	// answered unknown/not_supported and the absent id answered denied/not_available,
	// because the engine reached its concealment branch before the module was consulted.
	t.Run("R1 unsupported adapter is settled before any target is resolved", func(t *testing.T) {
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("hidden", "GET /v1/m/sessions/channels/{id}", ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", "GET /v1/m/sessions/channels/{id}", ws, missing))
		for _, id := range []string{"hidden", "missing"} {
			capabilityWant(t, answers, id, "unknown", "not_supported")
		}
		capabilitySameShape(t, answers, "an unsupported route", "hidden", "missing")
	})

	// R1, sharper half: with the SHARED authority resolver down, every question in the
	// scope must answer identically. On the rejected candidate the outage itself became
	// the oracle — unknown for rows that existed, denied for the id that did not.
	t.Run("R1 a common closure outage cannot distinguish existence", func(t *testing.T) {
		outageBefore := communicationHTTPTestEffects(t, eng, tenant)
		real := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.CommunicationGrantClosure = communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: real}
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID),
			capabilityChannelQuestion("hidden", capOperationGrantSheet, ws, hidden.Channel.ID),
			capabilityChannelQuestion("missing", capOperationGrantSheet, ws, missing))
		eng.sessionsMod.CommunicationGrantClosure = real
		for _, id := range []string{"held", "hidden", "missing"} {
			capabilityWant(t, answers, id, "undisclosed", "not_disclosed")
		}
		capabilitySameShape(t, answers, "a common closure outage", "held", "hidden", "missing")
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, outageBefore, "the closure-outage projection")
	})

	// R2: a positive may never outlive the evidence that founds it. The producer below
	// is the REAL concrete closure resolver over the estate's own Store — only its
	// freshness configuration is shortened, so every fact, epoch fence and credential it
	// resolves stays real. On the rejected candidate this returned a 4990 ms budget
	// against a 750 ms window, because the adapter reported only the local grant horizon.
	t.Run("R2 the budget is bounded by every window the inner stage consumed", func(t *testing.T) {
		bounded := *eng.communicationComposition.resolver
		bounded.freshness = 750 * time.Millisecond
		real := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.CommunicationGrantClosure = newCommunicationGrantClosureResolver(&bounded)
		answers := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
		eng.sessionsMod.CommunicationGrantClosure = real
		answer := capabilityWant(t, answers, "held", "allowed", "authorized")
		t.Logf("G1A_R2_BUDGET|closure_window_ms=750|returned_budget_ms=%d", *answer.RefreshAfterMS)
		if *answer.RefreshAfterMS > 750 {
			t.Fatalf("the positive outlives an actual evidence window: budget=%d ms > closure=750 ms",
				*answer.RefreshAfterMS)
		}
		// The control that keeps this from passing by clamping everything: with the real
		// producer restored, the same question is bounded by the outer window instead.
		restored := capabilityAsk(t, eng, steward.token, tenant,
			capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
		wide := capabilityWant(t, restored, "held", "allowed", "authorized")
		if *wide.RefreshAfterMS <= 750 {
			t.Fatalf("the restored producer still yields %d ms: the short window was not the "+
				"term that bounded the budget", *wide.RefreshAfterMS)
		}
	})

	// R3, both producers. Authorize returns Allow:false when the scoped engine or the
	// deny-overlay ERRORS — it fails closed, which is right for serving and wrong as a
	// statement about policy. The rejected candidate published both as known denials.
	for _, outage := range []struct {
		name       string
		authorizer func() *auth.Authorizer
	}{
		{"scoped", func() *auth.Authorizer {
			return auth.NewAuthorizer(nil, auth.WithScopedGrants(independentG1AScopedOutage{}))
		}},
		{"overlay", func() *auth.Authorizer {
			return auth.NewAuthorizer(independentG1AOverlayOutage{})
		}},
		// ⛔ THE DISAGREEMENT ARM, and it is neither a denial nor a permit. The wrapper
		// would ALLOW this request (its scoped engine abstains and RBAC carries it), so
		// answering `denied` would describe a refusal the route would not make; the
		// typed path reports an established forbid, so answering `allowed` would sell a
		// positive the evidence contradicts. Nothing about the request is established,
		// and UNKNOWN is the only answer that does not invent the half we preferred.
		{"disagreeing scoped", func() *auth.Authorizer {
			return auth.NewAuthorizer(nil, auth.WithScopedGrants(independentG1ADisagreeingScoped{}))
		}},
	} {
		t.Run("R3 a failing "+outage.name+" evaluator is unknown, never a denial", func(t *testing.T) {
			original := *eng.authz
			*eng.authz = *outage.authorizer()
			answers := capabilityAsk(t, eng, steward.token, tenant,
				capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
				capabilityChannelQuestion("held", capOperationGrantSheet, ws, held.Channel.ID))
			*eng.authz = original
			capabilityWant(t, answers, "surface", "unknown", "evidence_unavailable")
			capabilityWant(t, answers, "held", "undisclosed", "not_disclosed")
		})
	}

	// R4: the closed typed question must be judged whole. On the rejected candidate a
	// PATCH carrying the held channel in the body AND a hidden channel in the path was
	// answered `allowed` from the body alone, silently discarding the path.
	t.Run("R4 incompatible selectors never receive a positive", func(t *testing.T) {
		conflicting := capabilityBodyQuestion("patch", capOperationPatch, ws, held.Channel.ID)
		conflicting["selectors"].(map[string]any)["path"] = map[string]any{"id": hidden.Channel.ID.String()}
		noncanonical := capabilityBodyQuestion("noncanonical", capOperationPatch, ws, held.Channel.ID)
		noncanonical["selectors"].(map[string]any)["body"] = map[string]any{"channel_id": "not-a-uuid"}
		undeclared := capabilityChannelQuestion("undeclared", capOperationGrantSheet, ws, held.Channel.ID)
		undeclared["selectors"].(map[string]any)["path"].(map[string]any)["tenant_id"] = tenant.String()
		answers := capabilityAsk(t, eng, steward.token, tenant,
			conflicting, noncanonical, undeclared,
			// The control: the revoke route's pattern DECLARES grant_id, so carrying it
			// stays a valid captured intent and must still project a positive. Closing
			// the map must not close the contract's own declared keys.
			func() map[string]any {
				q := capabilityChannelQuestion("revoke", capOperationRevoke, ws, held.Channel.ID)
				q["selectors"].(map[string]any)["path"].(map[string]any)["grant_id"] = model.NewID().String()
				return q
			}())
		capabilityWant(t, answers, "patch", "unknown", "inputs_required")
		capabilityWant(t, answers, "noncanonical", "unknown", "inputs_required")
		capabilityWant(t, answers, "undeclared", "unknown", "inputs_required")
		capabilityWant(t, answers, "revoke", "allowed", "authorized")
	})

	// ⛔ AN INPUT-CONTRACT CHANGE, RECORDED AS ONE. Correcting R1 required the common
	// availability probe to be answerable with NO target named, and such a probe needs a
	// scope. So `workspace_id` became a DECLARED, REQUIRED authorization input for the
	// pilot's entity operations, where it had been optional.
	//
	// It is a narrowing of what the endpoint accepts, never a widening of what it
	// permits: the missing input is answered UNKNOWN/inputs_required — which the review
	// explicitly allows — and is never a denial and never a positive. The positive
	// control below is what makes that a real statement rather than a way to make hard
	// questions disappear: the SAME question, with the input supplied, still projects
	// allowed.
	t.Run("the declared workspace input is required, and supplying it still projects", func(t *testing.T) {
		withoutScope := map[string]any{
			"id": "no-scope", "kind": "operation", "operation": capOperationGrantSheet,
			"selectors": map[string]any{"path": map[string]any{"id": held.Channel.ID.String()}},
		}
		answers := capabilityAsk(t, eng, steward.token, tenant,
			withoutScope,
			capabilityChannelQuestion("with-scope", capOperationGrantSheet, ws, held.Channel.ID))
		capabilityWant(t, answers, "no-scope", "unknown", "inputs_required")
		capabilityWant(t, answers, "with-scope", "allowed", "authorized")
		// A non-canonical scope is the same class of answer, decided from the question
		// alone with no row read.
		malformed := capabilityAsk(t, eng, steward.token, tenant, map[string]any{
			"id": "bad-scope", "kind": "operation", "operation": capOperationGrantSheet,
			"workspace_id": "not-a-uuid",
			"selectors":    map[string]any{"path": map[string]any{"id": held.Channel.ID.String()}},
		})
		capabilityWant(t, malformed, "bad-scope", "unknown", "inputs_required")
	})

	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "the discriminant projections")
}

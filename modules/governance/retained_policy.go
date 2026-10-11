// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

const retainedAuthorizationEngine = "olivares-authorization-v1"

// The retained scoped-Cedar engine tags. v2 is written only when an inheritance filter marked
// the request, so a reader that predates the filter finds an engine it does not know and
// answers UNKNOWN, where it would otherwise ignore the marks and replay the request unfiltered.
const (
	retainedCedarScopeV1 = "cedar-scope-v1"
	retainedCedarScopeV2 = "cedar-scope-v2"
)

var (
	errRetainedQuestion          = errors.New("governance: retained authorization belongs to another question")
	errRetainedInputsUnavailable = errors.New("governance: retained authorization inputs unavailable")
	errRetainedInputsRedacted    = errors.New("governance: retained authorization input redacted")
)

func retainedAuthorizationQuestion(s auth.RetainedAuthorization) sdk.AccessQuestion {
	principal := s.CredID.String()
	if s.Kind == auth.KindUser {
		principal = s.UserID.String()
	}
	return QuestionForReplay(principal, "olivares", s.Resource.Kind, s.Resource.ID,
		string(s.Permission), "olivares.permission.v1")
}

type retainedABACVersion struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type retainedABAC struct {
	Rules    []abacRule            `json:"rules"`
	Versions []retainedABACVersion `json:"versions,omitempty"`
}

// Cedar inputs retain the immutable policy, entities, request and selected revisions.
type retainedCedar struct {
	Policies    *cedar.PolicySet `json:"policies"`
	Entities    cedar.EntityMap  `json:"entities"`
	Request     cedar.Request    `json:"request"`
	GrantUsable bool             `json:"grant_usable"`
	// Filtered are the nodes an inheritance filter marked on the target's lineage; the
	// replay classifies the matching permits against them exactly as the live decision did.
	Filtered []cedar.EntityUID `json:"filtered,omitempty"`
	Authored int64             `json:"authored_revision,omitempty"`
	Managed  int64             `json:"managed_revision,omitempty"`
	Adopted  int64             `json:"adopted_revision,omitempty"`
}

// captureScopedCedar retains the inputs of one scoped decision. A tenant with no grant policy
// still has to retain a request an inheritance filter decided, so it retains an empty set.
func captureScopedCedar(ctx context.Context, state scopedTenantState, em cedar.EntityMap, req cedar.Request, usable bool, marks []cedar.EntityUID) {
	policies := cedar.NewPolicySet()
	if state.set != nil {
		policies = state.set.policies
	}
	engine := retainedCedarScopeV1
	if len(marks) > 0 {
		engine = retainedCedarScopeV2
	}
	auth.CaptureAuthorizationInputs(ctx, true, engine, retainedCedar{
		Policies: policies, Entities: em, Request: req, GrantUsable: usable, Filtered: marks,
		Authored: state.selection.authored, Managed: state.selection.managed, Adopted: state.selection.adopted,
	})
}

// evaluateRetainedAuthorization uses retained inputs, never live policy or membership.
func evaluateRetainedAuthorization(content string, tenant model.TenantID, question sdk.AccessQuestion) (auth.EvidenceOutcome, error) {
	var snapshot auth.RetainedAuthorization
	if err := json.Unmarshal([]byte(content), &snapshot); err != nil {
		return auth.EvidenceUnknown, err
	}
	if snapshot.Tenant != tenant {
		return auth.EvidenceUnknown, errRetainedQuestion
	}
	if snapshot.Version != 1 {
		return auth.EvidenceUnknown, errors.New("governance: retained authorization version unsupported")
	}
	if snapshot.InputRedacted {
		return auth.EvidenceUnknown, errRetainedInputsRedacted
	}
	if !snapshot.Complete {
		return auth.EvidenceUnknown, errRetainedInputsUnavailable
	}
	expected, err := retainedAuthorizationQuestion(snapshot).Digest()
	if err != nil {
		return auth.EvidenceUnknown, errRetainedQuestion
	}
	actual, err := question.Digest()
	if err != nil || actual != expected {
		return auth.EvidenceUnknown, errRetainedQuestion
	}
	scoped := auth.ScopedDecision{Effect: auth.EffectAbstain}
	if len(snapshot.Scoped) > 1 {
		return auth.EvidenceUnknown, errors.New("governance: retained scoped evaluator unsupported")
	}
	for _, in := range snapshot.Scoped {
		switch in.Engine {
		case "none-v1":
		case retainedCedarScopeV1, retainedCedarScopeV2:
			var c retainedCedar
			if err := json.Unmarshal(in.Inputs, &c); err != nil || c.Policies == nil {
				return auth.EvidenceUnknown, errors.New("governance: retained Cedar inputs invalid")
			}
			dec, diag := cedar.Authorize(c.Policies, c.Entities, c.Request)
			switch {
			// A matched forbid is independently established even if an unrelated
			// permit errored, matching the live typed evaluator's precedence.
			case hasCedarForbidReason(c.Policies, diag):
				scoped = auth.ScopedDecision{Effect: auth.EffectForbid, Class: auth.ClassPolicy}
			case hasErroredForbid(c.Policies, diag):
				scoped = auth.ScopedDecision{Effect: auth.EffectForbid}
			case snapshot.Typed != nil && len(diag.Errors) > 0:
				return auth.EvidenceUnknown, errors.New("governance: retained typed Cedar unknown")
			case dec == cedar.Allow && c.GrantUsable && grantAtOrBelow(c.Policies, c.Entities, c.Request, c.Filtered):
				scoped = auth.ScopedDecision{Effect: auth.EffectGrant}
			}
			scoped.InheritanceFiltered = len(c.Filtered) > 0 && scoped.Effect != auth.EffectForbid
		default:
			return auth.EvidenceUnknown, errors.New("governance: retained scoped evaluator unsupported")
		}
	}
	policy := auth.Decision{Allow: true}
	for _, in := range snapshot.Policy {
		switch in.Engine {
		case "none-v1":
		case "abac-v1":
			var a retainedABAC
			if err := json.Unmarshal(in.Inputs, &a); err != nil {
				return auth.EvidenceUnknown, err
			}
			req := auth.Request{Principal: auth.Principal{Kind: snapshot.Kind, AAL: snapshot.AAL},
				Permission: snapshot.Permission, Tenant: snapshot.Tenant, Resource: snapshot.Resource, Route: snapshot.Route}
			for _, r := range a.Rules {
				if r.matches(req) {
					policy = auth.Decision{Allow: false, Class: auth.ClassPolicy}
				}
			}
		case "cedar-overlay-v1":
			var c retainedCedar
			if err := json.Unmarshal(in.Inputs, &c); err != nil || c.Policies == nil {
				return auth.EvidenceUnknown, errors.New("governance: retained Cedar inputs invalid")
			}
			dec, diag := cedar.Authorize(c.Policies, c.Entities, c.Request)
			if dec != cedar.Allow || hasErroredForbid(c.Policies, diag) {
				policy.Allow = false
			}
		default:
			return auth.EvidenceUnknown, errors.New("governance: retained policy evaluator unsupported")
		}
	}
	return auth.ReplayRetainedAuthorization(snapshot, scoped, policy)
}

func retainedAccessOutcome(outcome auth.EvidenceOutcome) sdk.AccessDecisionOutcome {
	switch outcome {
	case auth.EvidenceAllow:
		return sdk.AccessOutcomeAllow
	case auth.EvidenceDeny:
		return sdk.AccessOutcomeDeny
	default:
		return sdk.AccessOutcomeIndeterminate
	}
}

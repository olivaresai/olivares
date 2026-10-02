// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// RetainedEvaluation contains the immutable policy and inputs used by ONE
// evaluator invocation. It is historical data, never an authority witness.
type RetainedEvaluation struct {
	Engine string          `json:"engine"`
	Inputs json.RawMessage `json:"inputs"`
}

// RetainedAuthorization preserves only the facts this question consulted.
// Credential binding proofs, principal seals, secrets and other tenants' grants
// are deliberately absent. Version 1 freezes the authorization algebra; changes
// to that algebra require a new replay version.
type RetainedAuthorization struct {
	Version           int                  `json:"version"`
	Tenant            model.TenantID       `json:"tenant"`
	Permission        Permission           `json:"permission"`
	Resource          ResourceAttrs        `json:"resource"`
	Route             RouteMetadata        `json:"route"`
	Kind              PrincipalKind        `json:"kind"`
	UserID            model.ID             `json:"user_id"`
	CredID            model.ID             `json:"credential_id"`
	AAL               int                  `json:"aal"`
	Role              string               `json:"role"`
	RoleRank          int                  `json:"role_rank"`
	MinimumRoleRank   int                  `json:"minimum_role_rank"`
	Member            bool                 `json:"member"`
	Superadmin        bool                 `json:"superadmin"`
	Excluded          bool                 `json:"excluded"`
	SessionScope      model.TenantID       `json:"session_scope"`
	Restricted        bool                 `json:"restricted"`
	RestrictionAllows bool                 `json:"restriction_allows"`
	GrantingRoles     []string             `json:"granting_roles"`
	Disclosure        bool                 `json:"disclosure,omitempty"`
	Precondition      string               `json:"precondition,omitempty"`
	Scoped            []RetainedEvaluation `json:"scoped,omitempty"`
	Policy            []RetainedEvaluation `json:"policy,omitempty"`
	Complete          bool                 `json:"complete"`
	// InputRedacted means persisted inputs differ from those evaluated live.
	// A substitute is never a historical authorization input.
	InputRedacted bool            `json:"input_redacted,omitempty"`
	Typed         *RetainedChecks `json:"typed,omitempty"`
}

// RetainedChecks are the non-policy facts established by the typed PDP. The
// replay re-evaluates RBAC and the retained policies before folding these facts.
// They cannot reconstruct a usable principal or mint an authorization witness.
type RetainedChecks struct {
	Core          CheckEvidence `json:"core"`
	ResourceGuard CheckEvidence `json:"resource_guard"`
}

// AuthorizationRecord is one final PDP answer or authentication precondition
// refusal, with the inputs actually consulted.
type AuthorizationRecord struct {
	At           time.Time
	Actor        string
	ActorKind    string
	PrincipalRef string
	Outcome      EvidenceOutcome
	Purpose      sdk.DecisionPurpose
	Snapshot     RetainedAuthorization
	// EvidenceRedactor follows the evaluated request to its persistence boundary.
	// It is transient and must never be serialized with the historical inputs.
	EvidenceRedactor func(string) string `json:"-"`
}

type authorizationSinkKey struct{}
type authorizationCaptureKey struct{}
type authorizationCapture struct {
	snapshot      RetainedAuthorization
	requireScoped bool
	requirePolicy bool
	bytes         int
}

// WithAuthorizationRecording scopes recording to an operation whose owner can
// persist AFTER its store callbacks close. The callback must not open a nested
// write transaction. No goroutine or independent journal is created here.
func WithAuthorizationRecording(ctx context.Context, sink func(AuthorizationRecord)) context.Context {
	return context.WithValue(ctx, authorizationSinkKey{}, sink)
}

// RecordStepUpRefusal retains a step-up refusal already established by the
// caller. It does not check assurance again or evaluate authorization policy.
// Without the authentication policy's immutable inputs, replay is incomplete.
func (az *Authorizer) RecordStepUpRefusal(ctx context.Context, req Request) {
	if az == nil {
		return
	}
	ctx, capture := beginAuthorizationCapture(ctx, req)
	if capture == nil {
		return
	}
	capture.snapshot.Precondition = "step_up_required"
	capture.snapshot.Complete = false
	finishAuthorizationCapture(ctx, req, capture, az.clock(), EvidenceDeny)
}

func beginAuthorizationCapture(ctx context.Context, req Request) (context.Context, *authorizationCapture) {
	if sink, _ := ctx.Value(authorizationSinkKey{}).(func(AuthorizationRecord)); sink == nil {
		return ctx, nil
	}
	p := req.Principal
	role, member := p.RoleIn(req.Tenant)
	restricted, allowed := p.restrictedPermission(req.Tenant, req.Permission)
	c := &authorizationCapture{snapshot: RetainedAuthorization{
		Version: 1, Tenant: req.Tenant, Permission: req.Permission,
		Resource: cloneEvidenceRequest(req).Resource, Route: req.Route,
		Kind: p.Kind, UserID: p.UserID, CredID: p.CredID, AAL: p.AAL,
		Role: role, RoleRank: RoleRank(role), MinimumRoleRank: RoleRank(req.Route.RBACMinimumRole), Member: member, Superadmin: p.Superadmin,
		Excluded: p.ExcludedFrom(req.Tenant), SessionScope: p.SessionScope(),
		Restricted: restricted, RestrictionAllows: allowed, Complete: true,
	}}
	for _, r := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		if RoleGrants(r, req.Permission) {
			c.snapshot.GrantingRoles = append(c.snapshot.GrantingRoles, r)
		}
	}
	return context.WithValue(ctx, authorizationCaptureKey{}, c), c
}

// CaptureAuthorizationInputs is called at evaluation time, using the SAME
// immutable policy and resolved inputs that decide the request. Re-reading the
// active policy after a decision would race publication and is forbidden here.
func CaptureAuthorizationInputs(ctx context.Context, scoped bool, engine string, inputs any) {
	c, _ := ctx.Value(authorizationCaptureKey{}).(*authorizationCapture)
	if c == nil {
		return
	}
	b, err := json.Marshal(inputs)
	if err != nil || engine == "" || len(b)+c.bytes > model.MaxPolicyArtifactBytes {
		c.snapshot.Complete = false
		return
	}
	c.bytes += len(b)
	in := RetainedEvaluation{Engine: engine, Inputs: b}
	if scoped {
		c.snapshot.Scoped = append(c.snapshot.Scoped, in)
	} else {
		c.snapshot.Policy = append(c.snapshot.Policy, in)
	}
}

// IncompleteAuthorizationInputs marks a consulted evaluator or fact unavailable
// to historical replay. Its live outcome is still recorded without overclaiming.
func IncompleteAuthorizationInputs(ctx context.Context) {
	if c, _ := ctx.Value(authorizationCaptureKey{}).(*authorizationCapture); c != nil {
		c.snapshot.Complete = false
	}
}

func requireAuthorizationInputs(ctx context.Context, scoped bool) {
	if c, _ := ctx.Value(authorizationCaptureKey{}).(*authorizationCapture); c != nil {
		if scoped {
			c.requireScoped = true
		} else {
			c.requirePolicy = true
		}
	}
}

func finishDecisionCapture(ctx context.Context, req Request, c *authorizationCapture, at time.Time, decision Decision) {
	outcome := EvidenceDeny
	if decision.Allow {
		outcome = EvidenceAllow
	}
	finishAuthorizationCapture(ctx, req, c, at, outcome)
}

func finishAuthorizationCapture(ctx context.Context, req Request, c *authorizationCapture, at time.Time, outcome EvidenceOutcome) {
	if c == nil {
		return
	}
	// Ordinary successful reads do not consume audit retention; every denial and
	// every write/admin question does. This is the same final PDP, not a PEP effect.
	if outcome == EvidenceAllow && req.Permission.Verb() == VerbRead {
		return
	}
	if c.requireScoped && len(c.snapshot.Scoped) == 0 || c.requirePolicy && len(c.snapshot.Policy) == 0 {
		c.snapshot.Complete = false
	}
	actor, err := req.Principal.AttributableActor()
	if err != nil {
		return // cannot attribute a synthetic or unauthenticated producer
	}
	ref := req.Principal.CredID.String()
	if req.Principal.Kind == KindUser {
		ref = req.Principal.UserID.String()
	}
	sink, _ := ctx.Value(authorizationSinkKey{}).(func(AuthorizationRecord))
	sink(AuthorizationRecord{At: at.UTC(), Actor: actor, ActorKind: req.Principal.ActorKind(),
		PrincipalRef: ref, Outcome: outcome, Purpose: req.Purpose, Snapshot: c.snapshot,
		EvidenceRedactor: req.EvidenceRedactor})
}

// ReplayRetainedAuthorization folds re-evaluated policy contributions with the
// recorded principal/route facts. It returns a historical outcome ONLY, never
// a PrincipalRef, evidence receipt or transferable authorization witness.
func ReplayRetainedAuthorization(s RetainedAuthorization, scoped ScopedDecision, policy Decision) (EvidenceOutcome, error) {
	if s.Version != 1 || !s.Complete || s.InputRedacted {
		return EvidenceUnknown, errors.New("auth: retained authorization inputs unavailable")
	}
	if s.Excluded || !s.SessionScope.IsZero() && s.SessionScope != s.Tenant || s.Restricted && !s.RestrictionAllows {
		return EvidenceDeny, nil
	}
	rbac := s.Superadmin && s.SessionScope.IsZero() || s.Permission != PermSystemAdmin && s.Member && slices.Contains(s.GrantingRoles, s.Role)
	if s.Route.RequireScopedGrant || s.Route.RBACMinimumRole != "" && (!s.Member || s.RoleRank < s.MinimumRoleRank) {
		rbac = false
	}
	ownerGrant := s.Route.RequireScopedGrant && !s.Tenant.IsZero() && !s.Tenant.IsSystem() &&
		s.Permission != PermSystemAdmin && s.Member && s.Role == RoleOwner && !s.Restricted
	base := rbac || ownerGrant || scoped.Effect == EffectGrant
	if s.Restricted {
		base = s.RestrictionAllows
	}
	if s.Typed != nil {
		if s.Typed.ResourceGuard.Verdict == CheckBroken || scoped.Effect == EffectForbid || !base || !policy.Allow {
			return EvidenceDeny, nil
		}
		if s.Typed.ResourceGuard.Verdict != CheckClean || s.Typed.Core.Verdict == CheckUnknown {
			return EvidenceUnknown, nil
		}
		return EvidenceAllow, nil
	}
	if scoped.Effect == EffectForbid || !base || !policy.Allow {
		return EvidenceDeny, nil
	}
	return EvidenceAllow, nil
}

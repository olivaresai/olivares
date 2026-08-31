// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// Decision is the outcome of an authorization check. Reason is a non-sensitive
// explanation suitable for the audit trail and a generic 403 body; it never
// leaks which other tenant or resource exists.
type Decision struct {
	// Allow reports whether the action is permitted.
	Allow bool
	// Reason is a short, non-sensitive explanation.
	Reason string
	// Class is the provenance of a DENY: ClassPolicy (an authored business-policy
	// forbid) or ClassInvariant (a platform invariant — identity, tenancy, integrity,
	// fail-closed). The zero value is ClassInvariant, so any deny that omits it is
	// non-shadowable by design (constrained-observe shadows ONLY ClassPolicy).
	Class DecisionClass
}

// DecisionClass is the provenance of an authorization DENY, consumed by the
// constrained-observe mode to decide what a hook-PEP may shadow (allow-but-record).
// The zero value is ClassInvariant, so an unclassified — or unknown-valued — deny is
// never shadowable (fail-safe, matching the deny-closed doctrine). Callers that test
// shadowability MUST compare == ClassPolicy, never != ClassInvariant.
type DecisionClass int

const (
	// ClassInvariant (zero value) is a platform invariant: identity, tenancy/workspace
	// confinement, integrity/tamper, kill-switch, anti-replay, evidence, or any
	// fail-closed error. NEVER shadowable — it must enforce even in observe mode.
	ClassInvariant DecisionClass = iota
	// ClassPolicy is an authored business-policy forbid (an ABAC/Cedar/OPA/scoped deny
	// rule, or a local hook policy disposition). Shadowable in constrained-observe.
	ClassPolicy
)

// ResourceAttrs are the attributes of the resource an action targets, passed to
// the ABAC PolicyEvaluator and the scoped-grant engine. The built-in RBAC
// does not read them; an OPA-style evaluator uses them for attribute/context rules
// (sensitivity, ownership, time); the Cedar grant engine additionally resolves the
// Scope tree (workspace → agent-group → resource/folder) from ID/WorkspaceID.
type ResourceAttrs struct {
	// Kind is the entity kind (e.g. "core.agent"), or "" if not entity-scoped.
	Kind string
	// ID is the entity id, or "" for collection-level actions. When set, the Cedar
	// grant engine resolves the entity's TRUE scope (workspace, folder ancestors,
	// agent-group membership) from the store, so a caller can never forge it.
	ID string
	// WorkspaceID is the workspace the action targets. Zero ⇒ the tenant's default
	// workspace (back-compat resolution).
	//
	// Whether it is CONSULTED depends on the resource kind, not on whether an entity id
	// is present, and the difference matters because this field is a trust boundary:
	//   - For a kind that lives in the tree (agent, session, resource, agent_group)
	//     an entity-level action IGNORES it — the engine walks the stored row instead,
	//     which the caller cannot lie about.
	//   - For any OTHER kind, including every module entity, the engine has no tree to
	//     walk, so this field IS what a workspace-conditioned grant matches on
	//     (modules/governance/grants.go declaredScope).
	//
	// The consequence for a caller: on a non-tree kind, whatever is put here IS the
	// authorization scope. It must therefore be read from the STORE and never from the
	// request — which is what core/api chiRegistrar.entityResource does for the routes
	// that declare an api.EntityRef. Seeding it from a caller-supplied value on such a
	// route would let the caller name its own scope, and the comment this replaced said
	// the field was ignored there, which would have made that look safe.
	WorkspaceID model.ID
	// Sensitivity is an operator-assigned label when known.
	Sensitivity string
	// Extra carries additional non-sensitive attributes for the evaluator.
	Extra map[string]string
}

// Request is one authorization question: may this Principal perform Permission in
// Tenant against Resource?
type Request struct {
	Principal  Principal
	Permission Permission
	Tenant     model.TenantID
	Resource   ResourceAttrs
}

// ResourceFor builds the request-path ResourceAttrs for a permission, seeding the
// resource Kind from the permission's resource segment (IDN-09). A caller that knows
// the specific entity id/sensitivity sets those fields on the returned value. The
// built-in RBAC ignores ResourceAttrs; an external PDP (Cedar/OPA) reads them.
func ResourceFor(perm Permission) ResourceAttrs {
	return ResourceAttrs{Kind: perm.Resource()}
}

// PolicyEvaluator is the ABAC seam. It runs AFTER RBAC and may only FURTHER
// RESTRICT an RBAC grant — it can never widen one (the Authorizer intersects the
// two). An OPA-backed evaluator slots in here without any other change. The
// default (DenyNothing) returns Allow=true, meaning "the RBAC decision stands".
type PolicyEvaluator interface {
	// Evaluate returns whether the request is permitted by attribute/context
	// policy. An error means the policy could not be evaluated and the Authorizer
	// fails closed (denies).
	Evaluate(ctx context.Context, req Request) (Decision, error)
}

// DenyNothing is the default evaluator: it imposes no additional restriction, so
// the RBAC decision stands. It is the identity over RBAC, NOT an independent
// "allow everything".
type DenyNothing struct{}

// Evaluate always allows (no further restriction).
func (DenyNothing) Evaluate(context.Context, Request) (Decision, error) {
	return Decision{Allow: true, Reason: "no policy restriction"}, nil
}

// Effect is a ScopedAuthorizer's three-valued contribution to a decision. It is
// what lets authorization stop being FLAT (tenant-wide RBAC ∩ deny-overlay) and
// become a SCOPED tree: the engine can now positively GRANT within a scope,
// not only restrict.
type Effect int

const (
	// EffectAbstain is "no opinion": no grant and no restriction matched, so the
	// decision defers to RBAC and the deny-overlay. It is the zero value, so a
	// ScopedAuthorizer that does nothing reduces the Authorizer to its historical
	// RBAC ∩ deny-overlay behavior — the back-compat invariant.
	EffectAbstain Effect = iota
	// EffectGrant is a positive scoped grant: it authorizes a request the flat RBAC
	// layer would deny (e.g. an admin scoped to one workspace). It is still ANDed
	// with the deny-overlay, so a forbid can narrow it.
	EffectGrant
	// EffectForbid is an explicit scoped restriction. It OVERRIDES everything — a
	// tenant-wide RBAC grant and a positive scoped grant alike — preserving the
	// forbid-overrides-permit and deny-by-default guarantees Cedar is built on.
	EffectForbid
)

// ScopedDecision is a ScopedAuthorizer's result: the three-valued Effect plus a
// non-sensitive reason for the audit trail.
type ScopedDecision struct {
	// Effect is the grant/forbid/abstain contribution.
	Effect Effect
	// Reason is a short, non-sensitive explanation.
	Reason string
	// Class is the provenance of a FORBID (Effect == EffectForbid): ClassPolicy for an
	// authored scoped forbid rule, ClassInvariant for workspace confinement or a
	// fail-closed error. Ignored for grant/abstain. Zero value ClassInvariant keeps a
	// forbid non-shadowable unless a producer explicitly marks it business policy.
	Class DecisionClass
}

// ScopedAuthorizer is the positive-grant seam: a hierarchy-aware engine
// (Cedar) that resolves the scope tree and answers GRANT / FORBID / ABSTAIN
// for a request. Unlike PolicyEvaluator — which may only RESTRICT — it may also
// GRANT, so it is wired BESIDE the deny-overlay (not into it) and the Authorizer
// combines them: Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay. An error fails
// the Authorizer closed (denies).
type ScopedAuthorizer interface {
	// Scoped resolves the request's scope and returns its grant/forbid contribution.
	// An error means the scope/policy could not be evaluated (the Authorizer denies).
	Scoped(ctx context.Context, req Request) (ScopedDecision, error)
}

// Authorizer makes authorization decisions. The base authorization is the
// tenant-wide RBAC grant OR a positive SCOPED grant; the result is then
// narrowed by the deny-overlay (the native ABAC engine + any external PDP, which
// may only restrict). It denies by default and is safe for concurrent use.
type Authorizer struct {
	eval   PolicyEvaluator  // deny-overlay: may only further-restrict (nil ⇒ none)
	scoped ScopedAuthorizer // positive scoped grants; nil ⇒ flat RBAC only
}

// Option configures an Authorizer at construction.
type Option func(*Authorizer)

// WithScopedGrants wires the positive scoped-grant engine (Cedar). Without
// it an Authorizer is purely RBAC ∩ deny-overlay — its historical behavior — so
// every existing call site keeps compiling and deciding identically.
func WithScopedGrants(s ScopedAuthorizer) Option {
	return func(a *Authorizer) { a.scoped = s }
}

// NewAuthorizer returns an Authorizer using eval as the deny-overlay (ABAC) layer.
// A nil eval means "no further restriction". Pass WithScopedGrants to add positive
// scoped grants; without it the Authorizer is RBAC ∩ deny-overlay as before.
func NewAuthorizer(eval PolicyEvaluator, opts ...Option) *Authorizer {
	az := &Authorizer{eval: eval}
	for _, o := range opts {
		o(az)
	}
	return az
}

// Authorize decides one Request. It denies by default. The order encodes the
// algebra Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay:
//
//  1. The scoped engine runs FIRST: a FORBID short-circuits to deny (it overrides
//     RBAC and any grant — forbid-overrides-permit), a GRANT records a positive
//     authorization, ABSTAIN does nothing. A nil scoped engine abstains.
//  2. Base authorization: a non-member / RBAC miss / system permission held by a
//     non-superadmin is denied UNLESS a scoped grant authorized it. Neither ⇒ deny
//     (no overlay needed — there is nothing to narrow).
//  3. The deny-overlay (ABAC + external PDP) may then further-restrict the base
//     grant. A faulty/unavailable overlay or scoped engine fails CLOSED.
func (az *Authorizer) Authorize(ctx context.Context, req Request) Decision {
	restricted, restrictionAllows := req.Principal.restrictedPermission(req.Tenant, req.Permission)
	if restricted && !restrictionAllows {
		return Decision{Allow: false, Reason: "credential ceiling: not permitted"}
	}
	granted := false
	if az.scoped != nil {
		sd, err := az.scopedSafe(ctx, req)
		if err != nil {
			return Decision{Allow: false, Reason: "scoped: evaluation error"} // fail closed
		}
		switch sd.Effect {
		case EffectForbid:
			// E1b: propagate the scoped forbid's provenance. sd.Class is
			// ClassPolicy for a cleanly-evaluated authored scoped forbid and
			// ClassInvariant for confinement / errored / fail-closed (grants.go). It
			// MUST be propagated, never hardcoded — hardcoding ClassPolicy here would
			// make workspace confinement shadowable in constrained-observe.
			return Decision{Allow: false, Reason: "scoped: " + sd.Reason, Class: sd.Class}
		case EffectGrant:
			// A positive scoped grant must never widen a purpose-specific
			// credential. The exact ceiling is the base authorization for that
			// principal; scoped forbids above still narrow it normally.
			if !restricted {
				granted = true
			}
		}
	}
	baseAllowed := restrictionAllows
	if !restricted {
		baseAllowed = az.rbacAllows(req) || granted
	}
	if !baseAllowed {
		return Decision{Allow: false, Reason: "rbac: not permitted"}
	}
	if az.eval == nil {
		return Decision{Allow: true, Reason: baseReason(granted)}
	}
	dec, err := az.evalSafe(ctx, req)
	if err != nil {
		// Fail closed: a policy that cannot be evaluated denies.
		return Decision{Allow: false, Reason: "policy: evaluation error"}
	}
	if !dec.Allow {
		// E1b: propagate the deny-overlay's provenance. dec.Class is ClassPolicy
		// for an authored ABAC/OPA/Cedar deny and ClassInvariant for a tamper/undefined/
		// fail-closed deny (the producers tag it; a chain member that errors surfaces as
		// evalSafe error above, which already denies invariant). Propagate, never assume.
		return Decision{Allow: false, Reason: "policy: " + dec.Reason, Class: dec.Class}
	}
	return Decision{Allow: true, Reason: baseReason(granted)}
}

// baseReason labels an allow by which base authorization carried it (a positive
// scoped grant vs. a tenant-wide RBAC grant) for the audit trail.
func baseReason(granted bool) string {
	if granted {
		return "permitted (scoped grant)"
	}
	return "permitted"
}

// Allowed is the boolean convenience over Authorize. It seeds Resource.Kind from the
// permission so an external PDP can match on resource kind (IDN-09).
func (az *Authorizer) Allowed(ctx context.Context, p Principal, perm Permission, tenant model.TenantID) bool {
	return az.Authorize(ctx, Request{Principal: p, Permission: perm, Tenant: tenant, Resource: ResourceFor(perm)}).Allow
}

// rbacAllows applies the built-in role-based check.
func (az *Authorizer) rbacAllows(req Request) bool {
	// The superadmin holds the system role: every permission, every tenant.
	if req.Principal.Superadmin {
		return true
	}
	// The system permission is held only by a superadmin.
	if req.Permission == PermSystemAdmin {
		return false
	}
	// A tenant action requires a membership in that tenant (deny by default).
	role, ok := req.Principal.RoleIn(req.Tenant)
	if !ok {
		return false
	}
	return RoleGrants(role, req.Permission)
}

// evalSafe runs the ABAC evaluator, converting a panic into an error so a faulty
// policy denies rather than crashing the request.
func (az *Authorizer) evalSafe(ctx context.Context, req Request) (dec Decision, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("auth: policy evaluator panicked: %v", r)
		}
	}()
	return az.eval.Evaluate(ctx, req)
}

// scopedSafe runs the scoped-grant engine, converting a panic into an error so a
// faulty engine fails the request CLOSED (denies) rather than crashing it.
func (az *Authorizer) scopedSafe(ctx context.Context, req Request) (sd ScopedDecision, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("auth: scoped authorizer panicked: %v", r)
		}
	}()
	return az.scoped.Scoped(ctx, req)
}

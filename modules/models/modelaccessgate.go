// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// the ENFORCEMENT side of Claude model-access governance: the deny-closed
// decision the routing select/execute chain runs over the model_access/model_group
// tables (modelgovernance.go), and the reusable, identity-parameterized seam an
// in-line proxy consults in-band. It does NOT touch the verified Cedar core; it
// COMPOSES the same primitives uses — principal identity containment (the
// injected ActorScopeResolver) — over own tables, plus the surface
// dimension. The FinOps budget tie-in stays the existing fail-open budget gate
// (budget.go); a grant only REFERENCES a budget (metadata + console), it never becomes
// the security boundary (finops.CheckBudget is fail-open by contract).
//
// Decision model — SUBJECT-SCOPED, DENY-CLOSED (literal requirement, "que ciertos
// usuarios solo accedan a X modelos"):
//   - superadmin (cross-tenant provisioning identity) is never confined;
//   - a tenant with NO model-access grants does not govern model use (opt-in / back-compat);
//   - a principal NAMED by no grant is unrestricted (you restrict CERTAIN subjects);
//   - a principal NAMED by any grant (by user id, tenant role, a DIRECTORY group the user
//     belongs to, or the acting agent's group) may use a model only if one of ITS grants
//     also matches model + workspace + surface — otherwise DENY. A read/resolve error is a
//     DENY (an unreadable governance state must never authorize a model — the same posture
//     as the scope gate, and the inverse of the fail-open budget gate).
//
// Unlike the tenant-wide RBAC bypass applies to source bindings (a tenant operator
// sees every workspace's SOURCES), model-access governance is a USE restriction that must
// bite members too — so there is deliberately NO tenant-RBAC bypass here, only superadmin.
// To lock a whole tenant to an allow-list, author a grant on the base role every member
// holds (subject_kind=role); to restrict only some users, name them (subject_kind=user),
// their directory group (subject_kind=user_group), or the acting agent's group
// (subject_kind=agent_group).

import (
	"context"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ActorScope is the acting agent/session's scope: its workspace slug and the slugs
// of the agent-groups it belongs to. It is resolved SERVER-SIDE from the AUTHENTICATED
// actor identity — the token's Principal.AgentIdentity (F-01) — never from a
// caller-supplied session_ref: a caller-chosen reference must not establish effective
// identity. An empty Workspace means the actor could not be resolved.
type ActorScope struct {
	Workspace   string
	AgentGroups []string
}

// ActorScopeResolver resolves the acting session's scope for model-access
// evaluation. It is the ONLY cross-module dependency of the decision — the SAME
// actor-scope resolution the source-scope gate does internally — so the composition
// root backs it with the sourcescope resolver. With none wired, the actor scope is empty:
// user/role-subject grants still evaluate (those need only the principal), while
// workspace- and agent-group-scoped grants simply do not match.
type ActorScopeResolver interface {
	Resolve(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef string) (ActorScope, error)
}

// unresolvedActorScope is the unwired default: no session/agent resolution. A tenant that
// has not wired the resolver (or a test) sees an empty actor scope — and with no
// model-access grants the decision allows regardless, so it is back-compat.
type unresolvedActorScope struct{}

func (unresolvedActorScope) Resolve(context.Context, model.TenantID, auth.Principal, string) (ActorScope, error) {
	return ActorScope{}, nil
}

// WithActorScopeResolver wires the actor-scope resolver the model-access decision
// needs for its workspace and agent-group dimensions (sourcescope-backed, in the
// composition root). Without it those dimensions cannot match.
func WithActorScopeResolver(r ActorScopeResolver) Option { return func(m *Module) { m.actorScope = r } }

// Reasons on the unrestricted (unnamed-principal) path. The second is the C03-26
// signal: user_group grants exist, none named this principal. It must stay an
// ALLOW. The token `group_mapping_is_not_a_gate` is what tests pin — do not
// gate federation/group-mapping from this file.
const (
	reasonUnnamedPrincipal                       = "no model-access grant targets this principal"
	reasonUnnamedPrincipalUserGroupGrantsPresent = "no model-access grant targets this principal; user-group grants exist but none named this principal (group_mapping_is_not_a_gate)"
)

// ModelAccessVerdict is the decision for one (subject, model, surface) tuple.
type ModelAccessVerdict struct {
	// Allowed reports whether the principal may use the model on the surface.
	Allowed bool
	// Reason is a short, non-sensitive explanation for the audit trail / a 403 body.
	Reason string
}

// modelAccessGrant is the in-memory form of one model-access rule (allow or forbid).
type modelAccessGrant struct {
	subjectKind, subjectRef string
	targetKind, targetRef   string
	workspace               string
	surfaces                []string
	// effect is effectAllow (the default; an empty stored value is an allow) or
	// effectForbid. A forbid SUBTRACTS — it overrides any allow for the subjects
	// it names (forbid-overrides-allow, deny-closed).
	effect string
}

// isForbid reports whether this rule is a forbid (an empty/legacy effect is an allow).
func (g modelAccessGrant) isForbid() bool { return g.effect == effectForbid }

// modelGroupDef is the in-memory membership of one model-group.
type modelGroupDef struct {
	members, families, tiers []string
}

// maContext is the per-request, pre-resolved model-access state (grants + groups + the
// acting principal's identity/scope), so the routing chain decides MANY candidates
// without re-reading the store or re-resolving the actor per candidate. A nil *maContext
// means "not governed" (allow): superadmin, no data handle, or no grants.
type maContext struct {
	grants []modelAccessGrant
	groups map[string]modelGroupDef
	userID string
	role   string
	// userGroups are the acting principal's DIRECTORY group ids (S256, principal.GroupsIn):
	// the subject of a `user_group` grant. Unlike agent-groups (actor-scope-dependent) they
	// travel on the server-authenticated principal, so they are known WITHOUT the resolver —
	// authoritative for a session, absent for a token (least privilege, see below).
	userGroups []string
	actor      ActorScope
	// sessionAsserted is true when the caller named a session_ref (an acting agent).
	sessionAsserted bool
	// unbindableAgent is true for a programmatic credential that carries no authenticated
	// NHI binding. Such a credential cannot prove that agent-group policy is irrelevant.
	unbindableAgent bool
	// hasAgentGroupGrant is true when ANY grant names an agent-group subject — the only
	// dimension whose subject match depends on the (caller-influenced, possibly
	// unresolvable) actor scope rather than the server-authenticated principal identity.
	hasAgentGroupGrant bool
	// hasAgentGroupForbid is true when any FORBID names an agent-group subject. When the
	// caller asserts a session we cannot resolve, an agent-group forbid that WOULD
	// subtract is indeterminate — so the decision must fail closed even if an allow
	// matched (a forbid we cannot evaluate must never be silently dropped).
	hasAgentGroupForbid bool
	// hasUserGroupGrant is true when ANY grant (allow or forbid) names a user_group subject
	// — the directory-group dimension whose subject match needs the principal's resolved
	// group set (principal.GroupsIn).
	hasUserGroupGrant bool
	// userGroupsUnresolvable is true when the principal cannot enumerate its directory-group
	// membership: an API token carries none (least privilege, core/auth/principal.go), and
	// the models module cannot read the auth partition to resolve them. With user_group
	// grants present that is an INDETERMINATE membership — deny-closed (mirror of the
	// unbindable-agent guard): a token must never escape a group confinement by simply not
	// carrying the group that names it. A session's group set is authoritative (loadGrants),
	// so a session — even one in zero groups — is never unresolvable.
	userGroupsUnresolvable bool
}

// actorResolved reports whether the acting session resolved to a real scope. The
// sourcescope resolver gives a non-empty workspace slug (≥ the reserved "default") for a
// found session and an EMPTY one for an absent/unknown session_ref — so an empty
// Workspace is the unambiguous "unresolved actor" signal.
func (c *maContext) actorResolved() bool { return c.actor.Workspace != "" }

// modelAccessContext loads and pre-resolves the tenant's model-access state for the
// acting principal, or returns nil when the request is not governed (superadmin / no data
// handle / no grants — allow). It is DENY-CLOSED via its error: a store-read or
// actor-resolution failure returns an error the caller treats as a deny.
func (m *Module) modelAccessContext(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef string) (*maContext, error) {
	if principal.Superadmin || m.data == nil {
		return nil, nil // not governed (allow)
	}
	grants, groups, err := m.loadModelAccess(ctx, tenant)
	if err != nil {
		return nil, err // deny-closed
	}
	if len(grants) == 0 {
		return nil, nil // opt-in: no grants ⇒ model use not governed
	}
	// Governed: resolve the acting agent/session scope (workspace + agent-groups). Only
	// reached when grants exist, so an ungoverned tenant never pays this resolver call.
	actor, err := m.actorScope.Resolve(ctx, tenant, principal, sessionRef)
	if err != nil {
		return nil, err // deny-closed: cannot determine the actor's scope
	}
	role, _ := principal.RoleIn(tenant)
	userGroups := principal.GroupsIn(tenant)
	hasAG, hasAGForbid, hasUG := false, false, false
	for _, g := range grants {
		switch g.subjectKind {
		case subjectAgentGroup:
			hasAG = true
			if g.isForbid() {
				hasAGForbid = true
			}
		case subjectUserGroup:
			hasUG = true
		}
	}
	return &maContext{
		grants: grants, groups: groups, userID: principal.UserID.String(), role: role,
		userGroups: userGroups, actor: actor,
		sessionAsserted: sessionRef != "" || principal.AgentIdentity != "",
		// F-01: a programmatic credential is "unbindable" whenever it carries no
		// authenticated NHI binding — REGARDLESS of a caller-supplied session_ref. A
		// session_ref is a caller-chosen reference, not proof the token IS that agent, so
		// it must never launder the token into an agent-group subject (the confused-deputy
		// this closes). Only Principal.AgentIdentity (set server-side by the authenticator
		// from the token's subject→agent mapping) binds a token to an agent.
		unbindableAgent:    principal.Kind == auth.KindToken && principal.AgentIdentity == "",
		hasAgentGroupGrant: hasAG, hasAgentGroupForbid: hasAGForbid,
		hasUserGroupGrant: hasUG,
		// A credential that carries no resolved directory-group set cannot prove it is in
		// none of the confining user_groups. Only a token is in that position (a session's
		// groups are authoritative — loadGrants); a token in zero carried groups is treated
		// as indeterminate, not authoritatively-empty, so it fails closed below.
		userGroupsUnresolvable: principal.Kind == auth.KindToken && len(userGroups) == 0,
	}, nil
}

// decide is the pure model-access decision for one model on one surface, against the
// pre-resolved context. surface "" means the surface is unknown at this enforcement point
// (e.g. pure selection); the surface constraint is then deferred to the in-band caller
// that knows the real gateway — see the contract.
func (c *maContext) decide(modelRef, surface string) ModelAccessVerdict {
	// A raw API token with no authenticated NHI binding cannot establish whether an
	// agent-group rule names it. Treat that subject match as indeterminate instead of
	// laundering the call through the "unnamed principal is unrestricted" path.
	if c.hasAgentGroupGrant && c.unbindableAgent {
		return ModelAccessVerdict{Allowed: false, Reason: "model use is governed by agent-group grants but the API token has no authenticated agent binding (deny-closed)"}
	}

	// FAIL-CLOSED on an indeterminate directory-group (user_group) membership: a credential
	// that carries no resolved group set (an API token — least privilege, and the models
	// module cannot read the auth partition to resolve it) cannot prove it is in NONE of the
	// confining user_groups. Treat that subject match as indeterminate rather than laundering
	// the call through the "unnamed principal is unrestricted" path — a token must never
	// escape a directory-group confinement by simply not carrying the group that names it.
	// (Only fires once a user_group grant exists, so it is purely additive/back-compat.)
	if c.hasUserGroupGrant && c.userGroupsUnresolvable {
		return ModelAccessVerdict{Allowed: false, Reason: "model use is governed by user-group grants but this credential carries no resolved directory-group membership (deny-closed)"}
	}

	// FAIL-CLOSED on an indeterminate agent-group FORBID: an `agent_group` forbid's
	// subject match needs the acting agent's resolved groups. If the caller NAMED a
	// session that could NOT be resolved while agent-group forbids exist, we cannot prove
	// the principal's agent is in none of those forbidden groups — a subtraction we cannot
	// rule out must never be silently dropped, so deny BEFORE any allow can carry the
	// request. (A request asserting NO session is a direct user/role call, unaffected.)
	if c.hasAgentGroupForbid && c.sessionAsserted && !c.actorResolved() {
		return ModelAccessVerdict{Allowed: false, Reason: "model use may be denied by an agent-group forbid but the acting session could not be resolved (deny-closed)"}
	}

	// Partition the rules that NAME this principal into forbids and allows.
	var allows, forbids []modelAccessGrant
	for _, g := range c.grants {
		if !subjectMatches(g, c.userID, c.role, c.userGroups, c.actor.AgentGroups) {
			continue
		}
		if g.isForbid() {
			forbids = append(forbids, g)
		} else {
			allows = append(allows, g)
		}
	}

	// FORBID-OVERRIDES-ALLOW: a forbid that names this principal and matches the
	// model on this workspace/surface SUBTRACTS access, even when an allow would otherwise
	// grant it. Evaluated FIRST so the override is total (the same algebra as the
	// scoped forbid, and Cedar's forbid-overrides-permit).
	for _, g := range forbids {
		if targetMatches(g, modelRef, c.groups) && workspaceMatches(g, c.actor.Workspace) && surfaceForbidMatches(g, surface) {
			return ModelAccessVerdict{Allowed: false, Reason: "model use is denied for this principal by a model-access forbid rule"}
		}
	}

	// Positive allow-list (subject-scoped). A principal named by NO allow is unrestricted...
	if len(allows) == 0 {
		// FAIL-CLOSED for an indeterminate agent-group ALLOW: as above, if a session was
		// asserted-but-unresolved while agent-group allows exist we cannot prove the
		// principal is in none of those confining groups, so "named by no allow ⇒
		// unrestricted" is unsafe. The unforgeable closure is the NHI↔principal
		// binding + the in-band deriving the session from the credential (contract §3/§6).
		if c.hasAgentGroupGrant && c.sessionAsserted && !c.actorResolved() {
			return ModelAccessVerdict{Allowed: false, Reason: "model use is governed by agent-group grants but the acting session could not be resolved (deny-closed)"}
		}
		// C03-26 / Reparación 11: SIGNAL at the decision, never a group-mapping gate.
		// user_group grants that did not name this principal used to look identical to
		// "no grants at all". That is the DENY→ALLOW hole when membership lives only
		// on the IdP assertion and GroupsIn is empty. We still ALLOW (a session in
		// zero groups is authoritatively unnamed —). We NAME the hole. Gating
		// group-mapping here would refuse a fact and turn a deny into an allow.
		reason := reasonUnnamedPrincipal
		if c.hasUserGroupGrant {
			reason = reasonUnnamedPrincipalUserGroupGrantsPresent
		}
		return ModelAccessVerdict{Allowed: true, Reason: reason}
	}
	// ...one named by an allow is confined to its allows.
	for _, g := range allows {
		if targetMatches(g, modelRef, c.groups) && workspaceMatches(g, c.actor.Workspace) && surfaceMatches(g, surface) {
			return ModelAccessVerdict{Allowed: true, Reason: grantReason(g)}
		}
	}
	// Governed but no allow authorizes this model here: deny-closed. The reason is
	// generic — it never enumerates which grants exist (docs/SECURITY-HARDENING.md).
	return ModelAccessVerdict{Allowed: false, Reason: "model use is governed by model-access grants; none authorize this model on this workspace/surface"}
}

// previewVerdict is the /resolve model-access decision: it decides ONLY what is
// decidable WITHOUT an acting session and never hides a model the principal could
// legitimately use at execute. It returns Allowed=false ONLY when the model is denied
// under EVERY possible acting session ("denied in all worlds"), so the preview is honest
// — the actor-dependent dimensions (workspace, agent-group, surface) are DEFERRED to the
// authoritative execute/in-band decide(), never decided here. It is pure: it
// reads no actor scope (the context's actor is empty for /resolve).
//
//   - DEFINITE FORBID: a forbid that names the principal by its server-authenticated
//     user/role/directory-group identity, tenant-wide (workspace "") and with NO surface
//     constraint, covering the model — it bites in every world, so the model is dropped. A
//     workspace- or surface-scoped forbid bites only in SOME worlds and is deferred to
//     execute. Directory groups (user_group) ARE known here (they travel on the principal),
//     so — unlike agent-groups — a user_group forbid is decidable at preview for a session.
//   - ALLOW-LIST CONFINEMENT: the principal is GOVERNED iff a user/role/directory-group
//     allow names it (decidable in every world). If governed, the model is dropped ONLY when
//     NO allow that COULD apply to the principal in some world covers it — its own
//     user/role/directory-group allows, OR any agent-group allow (its agent might belong to
//     that group), with workspace/surface treated optimistically. A token that cannot
//     enumerate its directory groups treats a user_group allow as possibly-applying too
//     (optimistic KEEP at preview; the execute path fails it closed). Otherwise the model is
//     KEPT (it may be allowed at execute; hiding it would be dishonest).
func (c *maContext) previewVerdict(modelRef string) ModelAccessVerdict {
	// 1) Definite forbid — decidable in every world (user/role/directory-group subject,
	//    tenant-wide, all surfaces). userGroups travel on the principal, so a user_group
	//    forbid is decidable here (agent-groups, actor-scope-dependent, stay deferred: nil).
	for _, g := range c.grants {
		if !g.isForbid() || !subjectMatches(g, c.userID, c.role, c.userGroups, nil) {
			continue
		}
		if g.workspace != "" || len(g.surfaces) != 0 {
			continue // workspace/surface-scoped: bites only in some worlds — defer to execute
		}
		if targetMatches(g, modelRef, c.groups) {
			return ModelAccessVerdict{Allowed: false, Reason: "model use is denied for this principal by a tenant-wide model-access forbid rule"}
		}
	}
	// 2) Allow-list confinement — governed iff a user/role/directory-group allow names the
	//    principal (all decidable at preview from the server-authenticated principal).
	governed := false
	for _, g := range c.grants {
		if !g.isForbid() && subjectMatches(g, c.userID, c.role, c.userGroups, nil) {
			governed = true
			break
		}
	}
	if !governed {
		return ModelAccessVerdict{Allowed: true, Reason: "no user/role/user-group model-access allow confines this principal"}
	}
	for _, g := range c.grants {
		if g.isForbid() {
			continue
		}
		// An allow that COULD apply in SOME world: the principal's own user/role/directory
		// group, any agent-group (its acting agent might belong to it), or — for a token that
		// cannot enumerate its directory groups — any user_group allow (its owner might be a
		// member). Workspace/surface optimistic.
		couldApply := subjectMatches(g, c.userID, c.role, c.userGroups, nil) ||
			g.subjectKind == subjectAgentGroup ||
			(g.subjectKind == subjectUserGroup && c.userGroupsUnresolvable)
		if couldApply && targetMatches(g, modelRef, c.groups) {
			return ModelAccessVerdict{Allowed: true, Reason: "model may be authorized for this principal (preview; workspace/agent-group/surface enforced at execute)"}
		}
	}
	return ModelAccessVerdict{Allowed: false, Reason: "model use is governed by model-access allows; none could authorize this model for this principal"}
}

// EvaluateModelAccess is the model-access decision for ONE model on ONE surface —
// the reusable, IDENTITY-PARAMETERIZED seam (enforcement decision: "selección
// ahora + seam in-band para"). The routing execute/resolve chain calls it per
// candidate (modelAccessDeniesRoute); a future in-line /v1/messages proxy calls it
// IN-BAND with the identity it resolved from the inbound credential and the real surface,
// because the inference client itself is identity-blind. It is DENY-CLOSED: any
// read/resolve error is returned for the caller to treat as a deny.
func (m *Module) EvaluateModelAccess(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef, providerRef, modelRef, surface string) (ModelAccessVerdict, error) {
	_ = providerRef // reserved: the decision is model-ref centric; provider rides the model ref
	c, err := m.modelAccessContext(ctx, tenant, principal, sessionRef)
	if err != nil {
		return ModelAccessVerdict{}, err
	}
	if c == nil {
		return ModelAccessVerdict{Allowed: true, Reason: "model-access governance not enforced"}, nil
	}
	return c.decide(modelRef, surface), nil
}

// loadModelAccess reads the tenant's model-access grants and (only if any exist) its
// model-groups, in ONE module-level read transaction. It uses the module-level data
// handle (tenant-parameterized) so the in-band seam can call it WITHOUT a request
// ModuleContext.
func (m *Module) loadModelAccess(ctx context.Context, tenant model.TenantID) ([]modelAccessGrant, map[string]modelGroupDef, error) {
	var (
		grants []modelAccessGrant
		groups map[string]modelGroupDef
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		grantRecs, err := drainExt(ctx, sc, modelAccessKind)
		if err != nil {
			return err
		}
		for _, rec := range grantRecs {
			grants = append(grants, modelAccessGrant{
				subjectKind: rec.String(colMASubjectKind), subjectRef: rec.String(colMASubjectRef),
				targetKind: rec.String(colMATargetKind), targetRef: rec.String(colMATargetRef),
				workspace: rec.String(colMAWorkspace), surfaces: parseStrings(rec.String(colMASurfaces)),
				effect: normalizeEffect(rec.String(colMAEffect)),
			})
		}
		if len(grants) == 0 {
			return nil
		}
		groupRecs, err := drainExt(ctx, sc, modelGroupKind)
		if err != nil {
			return err
		}
		groups = make(map[string]modelGroupDef, len(groupRecs))
		for _, rec := range groupRecs {
			groups[rec.String(colMGName)] = modelGroupDef{
				members:  parseStrings(rec.String(colMGMembers)),
				families: parseStrings(rec.String(colMGFamilies)),
				tiers:    parseStrings(rec.String(colMGTiers)),
			}
		}
		return nil
	})
	return grants, groups, err
}

// --- pure matchers -----------------------------------------------------------

// subjectMatches reports whether grant g NAMES the acting principal: a specific user id,
// the principal's tenant built-in role, a DIRECTORY group the principal is a member of
// (user_group, matched against principal.GroupsIn — S256), or an agent-group the acting
// agent belongs to. userGroups and actorGroups are distinct axes (a user's directory groups
// vs the acting agent's groups) and are matched independently.
func subjectMatches(g modelAccessGrant, userID, role string, userGroups, actorGroups []string) bool {
	switch g.subjectKind {
	case subjectUser:
		return userID != "" && g.subjectRef == userID
	case subjectRole:
		return role != "" && g.subjectRef == role
	case subjectUserGroup:
		return containsString(userGroups, g.subjectRef)
	case subjectAgentGroup:
		return containsString(actorGroups, g.subjectRef)
	default:
		return false
	}
}

// targetMatches reports whether grant g's target covers modelRef — a single model ref
// (exact or prefix), or a model-group whose membership includes it.
func targetMatches(g modelAccessGrant, modelRef string, groups map[string]modelGroupDef) bool {
	switch g.targetKind {
	case targetModel:
		return modelRefMatches(g.targetRef, modelRef)
	case targetModelGroup:
		def, ok := groups[g.targetRef]
		return ok && groupContains(def, modelRef)
	default:
		return false
	}
}

// modelRefMatches reports whether modelRef is the grant's ref or carries it as a prefix
// AT A COMPONENT BOUNDARY. The prefix tolerance lets a grant on "claude-opus-4-8" cover a
// dated "claude-opus-4-8-20260201" estate ref, but the boundary ("-") stops it from
// silently over-granting a sibling family ("claude-opus-4-80") or a shorter ref from
// matching every variant — model refs being POSITIVE allows, an unbounded prefix would
// widen access beyond the dated-suffix intent (unlike the curated catalog prefixes
// lookupReference matches against).
func modelRefMatches(grantRef, modelRef string) bool {
	if grantRef == "" || modelRef == "" {
		return false
	}
	return modelRef == grantRef || strings.HasPrefix(modelRef, grantRef+"-")
}

// groupContains reports whether a model-group's HYBRID membership covers modelRef: an
// explicit member ref (exact/prefix), or its declared family / access tier (resolved via
// the reference catalog, longest-prefix). An unknown model ref (no reference entry)
// matches a group only by an explicit member ref.
func groupContains(def modelGroupDef, modelRef string) bool {
	for _, r := range def.members {
		if modelRefMatches(r, modelRef) {
			return true
		}
	}
	if len(def.families) == 0 && len(def.tiers) == 0 {
		return false
	}
	ref, ok := lookupReference(modelRef)
	if !ok {
		return false
	}
	if ref.Family != "" && containsString(def.families, ref.Family) {
		return true
	}
	if ref.AccessTier != "" && containsString(def.tiers, ref.AccessTier) {
		return true
	}
	return false
}

// workspaceMatches reports whether grant g applies in the actor's workspace. An empty
// grant workspace is tenant-wide (any workspace); a specific one requires the actor's
// resolved workspace to equal it (an unresolved actor matches no workspace-scoped grant).
func workspaceMatches(g modelAccessGrant, actorWorkspace string) bool {
	if g.workspace == "" {
		return true
	}
	return actorWorkspace != "" && actorWorkspace == g.workspace
}

// surfaceMatches reports whether grant g permits the request surface. No surface
// constraint permits all. An empty (unknown) surface — pure selection, before the
// gateway is chosen — is permitted here and the constraint is enforced by the in-band
// caller that knows the real surface; an explicit surface must be in the set.
func surfaceMatches(g modelAccessGrant, surface string) bool {
	if len(g.surfaces) == 0 {
		return true
	}
	if surface == "" {
		return true
	}
	return containsString(g.surfaces, surface)
}

// surfaceForbidMatches reports whether a FORBID's surface constraint applies at THIS
// enforcement point. Unlike an allow (surfaceMatches), a surface-scoped forbid is
// DEFERRED — not applied — when the surface is unknown (surface==""): the surface
// restriction is enforced where the real surface is known (an execute caller that declares
// it, or the in-band proxy that derives it), so a narrowly surface-scoped forbid must
// not silently escalate to ALL surfaces when a caller omits the surface (§6). An
// all-surface forbid (no constraint) applies everywhere; an explicit surface must be in the
// set. This keeps decide() symmetric with previewVerdict, which also defers surface-scoped
// forbids, and with the allow side, which defers a surface-scoped grant the same way.
func surfaceForbidMatches(g modelAccessGrant, surface string) bool {
	if len(g.surfaces) == 0 {
		return true // an all-surface forbid bites regardless of the (possibly unknown) surface
	}
	if surface == "" {
		return false // a surface-scoped forbid is deferred until the real surface is known
	}
	return containsString(g.surfaces, surface)
}

// grantReason names the authorizing grant for the allow audit (the principal's OWN grant,
// non-sensitive). A deny never names a grant.
func grantReason(g modelAccessGrant) string {
	return fmt.Sprintf("model-access grant: %s:%s → %s:%s", g.subjectKind, g.subjectRef, g.targetKind, g.targetRef)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CredRef is a scoped credential REFERENCE returned to an in-scope actor: a logical
// name, a locator (ref_kind + ref) where the credential actually lives, and an
// optional masked hint. It is value-free by construction — there is no field that can
// hold a usable secret (docs/SECURITY-HARDENING.md). A nil *CredRef means "no scoped credential —
// inherit the global/unbound credential" (only for an UNBOUND source).
type CredRef struct {
	Name    string
	RefKind string
	Ref     string
	Hint    string
}

// Decision is the resolver's verdict for one (actor, source) pair.
type Decision struct {
	// Allowed reports whether the actor may resolve/use the source.
	Allowed bool
	// Reason is a short, non-sensitive explanation for the audit trail / a 403 body.
	Reason string
	// Bound reports whether the source had any active scope binding. An UNBOUND source
	// is allowed for back-compat (global credential); a BOUND source is governed.
	Bound bool
	// Cred is the scoped credential reference to use (nil ⇒ the global/unbound one).
	// For a BOUND source that is allowed, Cred is the authorizing binding's reference
	// (which may itself be empty if the operator did not attach one) — never silently
	// the global credential.
	Cred *CredRef
}

// scopedAuthorizer is the minimal seam the resolver needs (governance.ScopedGrants
// satisfies auth.ScopedAuthorizer). Narrowing it keeps the resolver trivially fakeable.
type scopedAuthorizer interface {
	Scoped(ctx context.Context, req auth.Request) (auth.ScopedDecision, error)
}

// Resolver decides whether an agent/session may resolve a connected source and which
// credential reference applies. It is the runtime seam the model ScopeGate and the
// knowledge RetrievalGuard call. It composes (model B):
//   - CONTAINMENT: the source's scope contains the actor's. The actor's scope is the
//     workspace/groups of the agent/session NAMED by the caller's actor reference (a
//     control-plane assertion, the same agent-centric model retrieval guard
//     uses; route-gated). The scope VALUES come from the stored row (a caller cannot
//     inject a workspace), but the CHOICE of agent is the caller's — binding the
//     reference to the principal is a hardening follow-up (§ doc.go). The binding alone
//     confines for the trusted-runtime case; the gate is ADDITIVE (never weakens the
//     pre posture).
//   - GRANT: an cross-scope permit opens a foreign workspace.
//   - RBAC: tenant-wide authority still sees everything (workspace = soft-isolation).
//   - FORBID: an scoped forbid overrides all of the above for that scope.
//
// A FOLDER binding confines a source to a subtree of the Resource tree. It has NO
// containment dimension of its own (the actor is not a tree node); it is decided
// purely by GRANT/FORBID — scopeRequest names the anchor folder per-entity, so the
// engine resolves the folder's live ancestors from the materialized Path and a grant on
// the folder OR an ancestor authorizes (downward inheritance) — plus the SAME tenant
// RBAC soft-isolation. It never claims to be a hard, unbypassable boundary.
//
// It is NOT a second authorization engine: the grant/forbid decision is
// three-valued ScopedAuthorizer and the RBAC check is the built-in auth.RoleGrants;
// only the containment membership and the credential selection are new, and those are
// the soft-isolation dimension, not policy.
type Resolver struct {
	m *Module
}

// errNotReady is returned (deny-closed) when the module's data handle is not yet bound.
var errNotReady = errors.New("sourcescope: resolver not bound to the store")

// ResolveForSession authorizes a source for the session identified by sessionRef (its
// external id — a caller-declared, route-gated actor assertion; see the type doc). The
// session's stored WorkspaceID/AgentID give the actor scope VALUES. An empty/unknown
// sessionRef yields an empty actor scope (matches nothing) — deny-closed for a bound
// source unless the principal has a grant or tenant RBAC.
//
// when the principal carries an authenticated agent identity, that identity
// overrides the caller-declared sessionRef — a caller cannot claim another agent's
// scope by declaring its external_id. Legacy principals (no AgentIdentity) are
// unaffected; the gate is strictly additive and never weakens the pre posture.
func (r *Resolver) ResolveForSession(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef, sourceType, sourceRef string) (Decision, error) {
	effectiveRef := sessionRef
	if principal.AgentIdentity != "" {
		effectiveRef = principal.AgentIdentity
	}
	return r.resolve(ctx, tenant, principal, actorRef{kind: actorSession, ref: effectiveRef}, sourceType, sourceRef)
}

// ResolveForAgent authorizes a source for the agent identified by agentRef (its
// external id — a caller-declared, route-gated actor assertion). Same scope-derivation
// and deny-closed semantics as ResolveForSession.
//
// when the principal carries an authenticated agent identity, that identity
// overrides the caller-declared agentRef, closing the confused-deputy path documented
// in the source-scoping contract. Legacy principals (no AgentIdentity) are unaffected.
func (r *Resolver) ResolveForAgent(ctx context.Context, tenant model.TenantID, principal auth.Principal, agentRef, sourceType, sourceRef string) (Decision, error) {
	// when the principal carries an authenticated agent identity, derive
	// the actor from it — the caller-declared agentRef is overridden. This
	// closes the documented confused-deputy path: a caller can no
	// longer claim another agent's scope by declaring its external_id.
	effectiveRef := agentRef
	if principal.AgentIdentity != "" {
		effectiveRef = principal.AgentIdentity
	}
	return r.resolve(ctx, tenant, principal, actorRef{kind: actorAgent, ref: effectiveRef}, sourceType, sourceRef)
}

// ResolveActorScope returns the scope of the session named by sessionRef: its
// workspace slug and the slugs of the agent-groups it belongs to. It is the actor-scope
// half of resolve(), exposed (WITHOUT the source-binding/credential logic) for the
// model-access decision, which needs the acting agent's workspace and groups to match
// grants. The reference is a route-gated, caller-declared actor assertion; the VALUES are
// read server-side from the stored session row (a caller cannot inject a workspace). An
// empty/unknown sessionRef yields the empty scope (matches nothing — deny-closed for the
// caller). A store error fails closed (the caller treats it as a deny).
func (r *Resolver) ResolveActorScope(ctx context.Context, tenant model.TenantID, sessionRef string) (workspace string, agentGroups []string, err error) {
	data := r.m.moduleData()
	if data == nil {
		return "", nil, errNotReady
	}
	var actor actorScope
	if verr := data.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		actor, e = resolveActorScope(ctx, sc, actorRef{kind: actorSession, ref: sessionRef})
		return e
	}); verr != nil {
		return "", nil, verr
	}
	return actor.workspaceSlug, actor.groups, nil
}

// ResolveActorScopeForPrincipal returns the scope of the effective actor for
// model-access callers. F-01: the effective actor is ONLY the token's authenticated
// AgentIdentity (agent-OBO). A caller-supplied sessionRef NEVER establishes the
// actor scope — a principal cannot borrow another agent's workspace/agent-groups by
// naming its session. With no authenticated agent binding the actor is unresolved (empty
// scope), which is deny-closed for the workspace/agent-group grant dimensions. The
// sessionRef parameter is retained for signature compatibility but is deliberately
// ignored for identity; the model-access decision derives agent-group indeterminacy from
// the unbindable-token guard, not from a caller reference.
func (r *Resolver) ResolveActorScopeForPrincipal(ctx context.Context, tenant model.TenantID, principal auth.Principal, sessionRef string) (workspace string, agentGroups []string, err error) {
	_ = sessionRef // F-01: a caller-supplied reference must not establish effective identity.
	data := r.m.moduleData()
	if data == nil {
		return "", nil, errNotReady
	}
	if principal.AgentIdentity == "" {
		return "", nil, nil // no authenticated agent ⇒ no actor scope (binding required)
	}
	who := actorRef{kind: actorAgent, ref: principal.AgentIdentity}
	var actor actorScope
	if verr := data.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		actor, e = resolveActorScope(ctx, sc, who)
		return e
	}); verr != nil {
		return "", nil, verr
	}
	return actor.workspaceSlug, actor.groups, nil
}

type actorKind int

const (
	actorSession actorKind = iota
	actorAgent
)

type actorRef struct {
	kind actorKind
	ref  string
}

// actorScope is the actor's resolved scope: its workspace slug, the slugs of the
// agent-groups it belongs to, and the acting session/agent external ids used by the
// session/agent subject axes. An empty workspaceSlug means the actor could not be resolved
// (no session/agent row) and matches NO containment binding (deny-closed).
type actorScope struct {
	workspaceSlug string
	groups        []string
	// sessionRef is the acting session's external id — set only on the session-aware path
	// (ResolveForSession); empty on the agent-only path, so a session binding never matches
	// there (ADR-0022 §4). agentExternalID is the acting agent's external id (both paths).
	sessionRef      string
	agentExternalID string
}

// binding is the in-memory form of one enabled scope binding for a source.
type binding struct {
	id          model.ID
	scopeTree   string
	scopeRef    string
	workspaceID model.ID
	effect      string // effectAllow (default) | effectForbid
	cred        *CredRef
}

// isForbid reports whether this binding subtracts access. An empty/legacy effect
// is an allow (back-compat), consistent with normalizeEffect.
func (b binding) isForbid() bool { return b.effect == effectForbid }

// resolve is the core decision. Store reads (bindings + actor scope + assignments)
// run in ONE View; the ScopedAuthorizer is consulted AFTERWARD because it opens
// its own View, and nesting Views on the single-conn SQLite default would deadlock
// (the same reason the knowledge guard and grants.go resolve grants outside the
// caller's transaction).
//
// The decision algebra (ADR-0022 §2): forbid is ABSOLUTE (any matching forbid —
// row-level effect=forbid on a matched scope, or an EffectForbid — denies, overriding
// containment, cross-scope grant and tenant RBAC). A source is CONFINED iff it has ≥1
// enabled ALLOW binding; a confined source allows only an actor that matches an allow
// (containment / grant) or holds tenant RBAC; an unconfined source stays global (minus any
// matching forbid). The credential is taken from the most-specific matching allow.
//
// connector assignments gate VISIBILITY of global connectors per workspace. If any
// assignment row exists for a connector (source_ref), only the assigned workspaces see it
// (deny-closed). It applies only to UNCONFINED sources (no allow binding — len==0 or
// forbid-only); a confined source is governed by its allow bindings, the finer control.
func (r *Resolver) resolve(ctx context.Context, tenant model.TenantID, principal auth.Principal, who actorRef, sourceType, sourceRef string) (Decision, error) {
	data := r.m.moduleData()
	if data == nil {
		return Decision{Allowed: false, Reason: "source scoping unavailable (fail closed)"}, errNotReady
	}

	var (
		bindings          []binding
		actor             actorScope
		assignmentGated   bool
		assignmentAllowed bool
	)
	if err := data.View(ctx, tenant, func(sc store.Scope) error {
		bs, err := loadEnabledBindings(ctx, sc, sourceType, sourceRef)
		if err != nil {
			return err
		}
		bindings = bs
		actor, err = resolveActorScope(ctx, sc, who)
		if err != nil {
			return err
		}
		// gate connector VISIBILITY for UNCONFINED sources — those
		// with no enabled ALLOW binding (len==0 OR forbid-only). A confined source is
		// governed by its allow bindings, the finer-grained control.
		if !hasAllowBinding(bindings) {
			allowed, cerr := ConnectorAssigned(ctx, sc, sourceRef, actor.workspaceSlug)
			if cerr != nil {
				return cerr
			}
			assignmentGated = true
			assignmentAllowed = allowed
		}
		return nil
	}); err != nil {
		return Decision{Allowed: false, Reason: "source scoping read failed (fail closed)"}, err
	}

	// The full actor identity for this call. The principal dimensions (user, directory
	// groups, role) need no store read — they travel on the authenticated principal.
	role, _ := principal.RoleIn(tenant)
	identity := actorIdentity{
		workspaceSlug:   actor.workspaceSlug,
		groups:          actor.groups,
		sessionRef:      actor.sessionRef,
		agentExternalID: actor.agentExternalID,
		userID:          principal.UserID.String(),
		userGroups:      principal.GroupsIn(tenant),
		role:            role,
	}
	rbac := rbacAllows(principal, tenant, sourceType)

	// Precompute each binding's Cedar effect ONCE (resource-anchored trees only —
	// the subject trees are decided by containment + row effect, no policy round-trip). A
	// forbid must be detected even when a later binding would allow, so the decision is two
	// logical passes over the precomputed state, not a short-circuiting loop.
	effects := make([]auth.Effect, len(bindings))
	for i := range bindings {
		if r.m.scoped != nil && resourceAnchored(bindings[i].scopeTree) {
			sd, err := r.m.scoped.Scoped(ctx, scopeRequest(principal, tenant, sourceType, bindings[i]))
			if err != nil {
				// A scope policy we cannot evaluate must not silently open the source.
				return Decision{Allowed: false, Reason: "scope grant evaluation failed (fail closed)", Bound: true}, err
			}
			effects[i] = sd.Effect
		}
	}

	// Pass over bindings (pre-sorted most-specific-first): detect any ABSOLUTE forbid
	// (forbid-overrides-allow, ADR-0022 §2), determine confinement (≥1 allow binding), and
	// capture the most-specific matching ALLOW (its credential wins).
	forbidHit, confined := false, false
	var winner *binding
	winnerViaGrant := false
	for i := range bindings {
		b := &bindings[i]
		if !b.isForbid() {
			confined = true
		}
		contain := containsActor(identity, *b)
		// A row-level forbid bites where it MATCHES the actor (containment / identity); a
		// folder row-forbid has no containment and never bites (folder rides). An
		// EffectForbid (resource-anchored) is absolute regardless of the row effect.
		if (b.isForbid() && contain) || effects[i] == auth.EffectForbid {
			forbidHit = true
		}
		if !b.isForbid() && winner == nil && (contain || effects[i] == auth.EffectGrant) {
			winner = b
			winnerViaGrant = !contain && effects[i] == auth.EffectGrant
		}
	}

	if forbidHit {
		return Decision{Allowed: false, Reason: "forbidden by a scope binding (deny-closed)", Bound: true}, nil
	}
	if !confined {
		// Unconfined: global / back-compat, subject to the assignment gate. Superadmin
		// and tenant-wide RBAC override the assignment gate (soft-isolation).
		if assignmentGated && !assignmentAllowed && !rbac {
			return Decision{Allowed: false, Reason: "connector not assigned to workspace (deny-closed)", Bound: len(bindings) > 0}, nil
		}
		reason := "source has no scope binding (unbound)"
		if len(bindings) > 0 {
			reason = "source has no active allow binding (global; no matching forbid)"
		}
		return Decision{Allowed: true, Reason: reason, Bound: len(bindings) > 0}, nil
	}
	// Confined: the actor must match an allow (containment / cross-scope grant) or hold
	// tenant-wide RBAC; otherwise deny-closed.
	if winner != nil {
		return Decision{Allowed: true, Reason: containReason(*winner, winnerViaGrant), Bound: true, Cred: winner.cred}, nil
	}
	if rbac {
		return Decision{Allowed: true, Reason: "tenant-wide rbac", Bound: true, Cred: mostSpecificAllowCred(bindings)}, nil
	}
	return Decision{Allowed: false, Reason: "actor is out of the source's scope (deny-closed)", Bound: true}, nil
}

// hasAllowBinding reports whether any enabled binding is an ALLOW (confinement trigger).
func hasAllowBinding(bs []binding) bool {
	for i := range bs {
		if !bs[i].isForbid() {
			return true
		}
	}
	return false
}

// mostSpecificAllowCred returns the credential of the most-specific ALLOW binding (bs is
// pre-sorted most-specific-first), or nil — the credential of record for a tenant-wide
// RBAC operator on a confined source.
func mostSpecificAllowCred(bs []binding) *CredRef {
	for i := range bs {
		if !bs[i].isForbid() {
			return bs[i].cred
		}
	}
	return nil
}

// actorIdentity is the full resolved identity of the actor for one resolve() call: the
// Containment scope (workspace, agent-groups) plus the subject dimensions — the
// acting session/agent external ids and the authenticated principal's user id, directory
// groups (S256) and tenant role. It is built once per call and is the pure input to
// containsActor.
type actorIdentity struct {
	workspaceSlug   string
	groups          []string // agent-group slugs
	sessionRef      string   // acting session external id ("" on the agent-only path)
	agentExternalID string   // acting agent external id
	userID          string   // principal.UserID
	userGroups      []string // S256 directory group ids (principal.GroupsIn)
	role            string   // principal.RoleIn
}

// resourceAnchored reports whether a tree rides the Cedar cross-scope grant/forbid
// (workspace, agent_group, folder). The subject trees do not — they are decided by
// containment + row effect only (ADR-0022 §2).
func resourceAnchored(tree string) bool { return !subjectTrees[tree] }

// specificityRank orders trees most-specific → least, for CREDENTIAL selection among
// matching allow bindings (forbid already decides allow/deny). ADR-0022 §3.
func specificityRank(tree string) int {
	switch tree {
	case scopeSession:
		return 0
	case scopeAgent:
		return 1
	case scopeUser:
		return 2
	case scopeUserGroup:
		return 3
	case scopeRole:
		return 4
	case scopeAgentGroup:
		return 5
	case scopeFolder:
		return 6
	case scopeWorkspace:
		return 7
	default:
		return 8
	}
}

// containReason names which path authorized access (for the audit trail).
func containReason(b binding, viaGrant bool) string {
	if viaGrant {
		if b.scopeTree == scopeFolder {
			return "folder-subtree grant (" + b.scopeRef + ")"
		}
		return "cross-scope grant"
	}
	return "in scope (" + b.scopeTree + ":" + b.scopeRef + ")"
}

// containsActor reports whether binding b's scope contains the actor. For the containment
// trees it is the same workspace or an agent-group the actor belongs to; for the
// subject trees it is identity equality (session/agent/user/role) or directory-group
// membership (user_group, matched against principal.GroupsIn — S256). An unresolved actor
// is contained by nothing. A FOLDER binding has NO containment dimension — an actor is not
// a node of the Resource tree, so it is authorized purely by the per-entity
// grant/forbid plus tenant RBAC, never by containment (deny-closed here).
func containsActor(id actorIdentity, b binding) bool {
	switch b.scopeTree {
	case scopeWorkspace:
		ref := b.scopeRef
		if ref == "" {
			ref = model.DefaultWorkspaceSlug
		}
		return id.workspaceSlug != "" && id.workspaceSlug == ref
	case scopeAgentGroup:
		for _, g := range id.groups {
			if g == b.scopeRef {
				return true
			}
		}
		return false
	case scopeSession:
		return id.sessionRef != "" && id.sessionRef == b.scopeRef
	case scopeAgent:
		return id.agentExternalID != "" && id.agentExternalID == b.scopeRef
	case scopeUser:
		return id.userID != "" && id.userID == b.scopeRef
	case scopeUserGroup:
		for _, g := range id.userGroups {
			if g == b.scopeRef {
				return true
			}
		}
		return false
	case scopeRole:
		return id.role != "" && id.role == b.scopeRef
	default: // scopeFolder and any unknown tree: no containment (deny-closed)
		return false
	}
}

// scopeRequest builds the request for a binding. The action is always
// "<scopeable-kind>:read" (resolving/using a source is a read; there is no "use" verb in
// the catalog). The SCOPE differs by tree:
//
//   - workspace / agent-group: the declaredScope path. Resource.ID is empty, so the
//     engine resolves the workspace from the (trusted, store-derived) WorkspaceID the
//     binding resolved at bind time — the caller never supplies it.
//   - folder: the PER-ENTITY path. Resource.Kind is "resource" (the engine's
//     tree-walk discriminant, NOT the folder's own Kind) and Resource.ID is the anchor
//     folder id, so the engine reads that folder's LIVE materialized Path and builds its
//     ancestor chain — a grant/forbid `resource in Resource::"<folder-or-ancestor>"`
//     then decides, giving downward inheritance over the subtree. WorkspaceID is left
//     zero on purpose: if the folder was deleted the engine finds no row and resolves an
//     empty scope, so a dangling folder binding is deny-closed (no accidental
//     workspace-grant fallback).
func scopeRequest(principal auth.Principal, tenant model.TenantID, sourceType string, b binding) auth.Request {
	kind := scopeableKindFor(sourceType)
	req := auth.Request{
		Principal:  principal,
		Permission: auth.Permission(kind + ":" + auth.VerbRead),
		Tenant:     tenant,
	}
	if b.scopeTree == scopeFolder {
		req.Resource = auth.ResourceAttrs{Kind: "resource", ID: b.scopeRef}
		return req
	}
	req.Resource = auth.ResourceAttrs{Kind: kind, WorkspaceID: b.workspaceID}
	return req
}

// scopeableKindFor maps a source type to the scope-grantable core kind a grant/forbid
// is authored against (auth.ScopeableKinds). Knowledge bases and generic data sources
// have no dedicated scopeable kind, so they ride "resource" rather than widening the
// catalog (which would also widen what custom roles may target).
func scopeableKindFor(sourceType string) string {
	switch sourceType {
	case sourceMCP:
		return "mcp_server"
	case sourceModel:
		return "model"
	case sourceProvider:
		return "provider"
	default: // knowledge, data
		return "resource"
	}
}

// rbacAllows reports whether the principal's tenant-wide RBAC (or superadmin) already
// grants <kind>:read — the soft-isolation rule that a tenant-wide operator sees every
// workspace's sources. A CONFINED principal (no tenant role) returns false, so the
// binding's containment is what governs it.
func rbacAllows(principal auth.Principal, tenant model.TenantID, sourceType string) bool {
	if principal.Superadmin {
		return true
	}
	role, ok := principal.RoleIn(tenant)
	if !ok || role == "" {
		return false
	}
	return auth.RoleGrants(role, auth.Permission(scopeableKindFor(sourceType)+":"+auth.VerbRead))
}

// loadEnabledBindings reads the ENABLED bindings for a source. A source whose bindings
// are all disabled is treated as unbound (global). Pages to completion.
func loadEnabledBindings(ctx context.Context, sc store.Scope, sourceType, sourceRef string) ([]binding, error) {
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceType), eq(colSourceRef, sourceRef))
	if err != nil {
		return nil, err
	}
	out := make([]binding, 0, len(recs))
	for _, rec := range recs {
		if !rec.Bool(colEnabled) {
			continue
		}
		b := binding{
			id:          model.ID(rec.String(model.ColID)),
			scopeTree:   rec.String(colScopeTree),
			scopeRef:    rec.String(colScopeRef),
			workspaceID: model.ID(rec.String(colWorkspaceID)),
			effect:      normalizeEffect(rec.String(colEffect)),
		}
		if name := rec.String(colCredName); name != "" {
			b.cred = &CredRef{Name: name, RefKind: rec.String(colCredRefKind), Ref: rec.String(colCredRef), Hint: rec.String(colCredHint)}
		}
		out = append(out, b)
	}
	// Deterministic order, MOST-SPECIFIC first (ADR-0022 §3), so the chosen binding (and
	// thus the credential) is stable and the first matching allow is the most specific.
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := specificityRank(out[i].scopeTree), specificityRank(out[j].scopeTree); ri != rj {
			return ri < rj
		}
		if out[i].scopeRef != out[j].scopeRef {
			return out[i].scopeRef < out[j].scopeRef
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// resolveActorScope reads the actor's scope VALUES from the stored session/agent
// row named by the caller's reference (external id). The caller chooses the reference
// (route-gated, control-plane assertion); the workspace/group values are the store's,
// not the caller's. An unknown ref yields the empty scope (matches no binding). The
// agent's group slugs are the "fold by agent_id".
func resolveActorScope(ctx context.Context, sc store.Scope, who actorRef) (actorScope, error) {
	if who.ref == "" {
		return actorScope{}, nil
	}
	var (
		wsID, agentID   model.ID
		sessionRef      string
		agentExternalID string
	)
	switch who.kind {
	case actorSession:
		sessions, _, err := sc.Sessions().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", who.ref)}, Limit: 1})
		if err != nil {
			return actorScope{}, err
		}
		if len(sessions) == 0 {
			return actorScope{}, nil
		}
		wsID, agentID = sessions[0].WorkspaceID, sessions[0].AgentID
		sessionRef = who.ref
		// the acting agent's external id, for the `agent` subject axis. Best-effort —
		// an orphan session (agent row gone) simply has no agent axis (deny-closed for it).
		if !agentID.IsZero() {
			a, gerr := sc.Agents().Get(ctx, agentID)
			if gerr == nil {
				agentExternalID = a.ExternalID
			} else if !errors.Is(gerr, store.ErrNotFound) {
				return actorScope{}, gerr
			}
		}
	case actorAgent:
		agents, _, err := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", who.ref)}, Limit: 1})
		if err != nil {
			return actorScope{}, err
		}
		if len(agents) == 0 {
			return actorScope{}, nil
		}
		wsID, agentID = agents[0].WorkspaceID, agents[0].ID
		agentExternalID = agents[0].ExternalID
	}

	slug, err := workspaceSlug(ctx, sc, wsID)
	if err != nil {
		return actorScope{}, err
	}
	groups, err := agentGroupSlugs(ctx, sc, agentID)
	if err != nil {
		return actorScope{}, err
	}
	return actorScope{workspaceSlug: slug, groups: groups, sessionRef: sessionRef, agentExternalID: agentExternalID}, nil
}

// workspaceSlug resolves a workspace id to its slug (zero ⇒ the reserved default slug,
// no read — the NULL→default rule). A dangling id yields its raw value, which
// matches no authored slug (deny-closed).
func workspaceSlug(ctx context.Context, sc store.Scope, wsID model.ID) (string, error) {
	if wsID.IsZero() {
		return model.DefaultWorkspaceSlug, nil
	}
	ws, err := sc.Workspaces().Get(ctx, wsID)
	if errors.Is(err, store.ErrNotFound) {
		return wsID.String(), nil // synthetic: matches no authored slug
	}
	if err != nil {
		return "", err
	}
	return ws.Slug, nil
}

// agentGroupSlugs resolves the agent's group memberships to group slugs (the fold
// by agent_id). Orphan memberships (group row gone) are skipped. Pages to completion.
func agentGroupSlugs(ctx context.Context, sc store.Scope, agentID model.ID) ([]string, error) {
	if agentID.IsZero() {
		return nil, nil
	}
	members, err := drainAgentGroupMembers(ctx, sc, agentID)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}
	groups, err := drainAgentGroups(ctx, sc)
	if err != nil {
		return nil, err
	}
	bySlug := make(map[model.ID]string, len(groups))
	for _, g := range groups {
		bySlug[g.ID] = g.Slug
	}
	var out []string
	for _, mem := range members {
		if slug, ok := bySlug[mem.GroupID]; ok {
			out = append(out, slug)
		}
	}
	return out, nil
}

// drainAgentGroupMembers pages an agent's group memberships to completion.
func drainAgentGroupMembers(ctx context.Context, sc store.Scope, agentID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("agent_id", agentID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// drainAgentGroups pages the tenant's agent-groups to completion.
func drainAgentGroups(ctx context.Context, sc store.Scope) ([]model.AgentGroup, error) {
	var out []model.AgentGroup
	q := model.Query{Limit: listCap}
	for {
		recs, page, err := sc.AgentGroups().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Cedar as a POSITIVE, SCOPED grant engine. This file elevates the embedded
// Cedar engine from a deny-only overlay (cedar.go: a forbid-overlay that can only
// narrow an RBAC grant) to an authorization engine that GRANTS along the axis
// (workspace → agent-group → agent → resource/folder), enforced through the new
// auth.ScopedAuthorizer seam.
//
// How a binary Cedar decision becomes three-valued. cedar-go's Authorize is
// deny-by-default and forbid-overrides-permit. Its Diagnostic.Reasons names the
// DETERMINING policies, which lets us recover the three-valued effect the Authorizer
// needs WITHOUT the cedar.go base-permit hack:
//
//	Allow                          → EffectGrant   (a permit matched, no forbid)
//	Deny  && len(Reasons) > 0      → EffectForbid  (a forbid matched — it determined the deny)
//	Deny  && len(Reasons) == 0     → EffectAbstain (default-deny: no permit and no forbid)
//
// This is why a permit can now GRANT (Allow→EffectGrant) while a forbid still
// RESTRICTS, and an empty/irrelevant policy ABSTAINS so the RBAC decision stands —
// the back-compat invariant. (Verified against cedar-go v1.8.0 authorize.go.)
//
// Scope resolution. A grant like `permit(principal, action, resource) when { resource
// in Workspace::"payments" }` needs the resource's lineage as a Cedar entity
// graph. The scopeResolver reads that lineage from the store for the request's TRUE
// resource (the caller passes an entity id; the resolver reads the stored row, so the
// workspace/path/group membership can never be forged) and materializes it as Cedar
// entities whose Parents encode containment, so Cedar's transitive `in` does the
// hierarchy walk.

// Cedar entity types for the scope graph (besides cedar.go's Principal/Action/
// Resource). Workspaces and agent-groups are referenced by their tenant-unique,
// stable SLUG so an authored policy reads `Workspace::"payments"`, not a UUID;
// folder ancestors are referenced by Resource id (a Resource has no slug). A
// principal's role and owning user are parents so a grant can target `Role::"admin"`
// or `User::"<id>"`.
const (
	cedarTypeWorkspace  = "Workspace"
	cedarTypeAgentGroup = "AgentGroup"
	cedarTypeUser       = "User"
	cedarTypeRole       = "Role"
	// cedarTypeGroup is a DIRECTORY group the principal is a (gated, possibly
	// nested) member of — distinct from cedarTypeAgentGroup, which groups agent
	// RESOURCES. A grant may target `Group::"<group-id>"` (S256, scopedadmin.go
	// subjectGroup); the principal carries one parent per group in
	// Principal.GroupsIn, so it is `in` every group it belongs to.
	cedarTypeGroup = "Group"
)

// grantSet is a tenant's compiled scoped-grant policy: a Cedar policy set of permit
// AND forbid rules (NO implicit base permit — unlike cedar.go's restrict-only
// overlay — so a permit genuinely grants). Immutable once compiled.
type grantSet struct {
	policies *cedar.PolicySet
	source   string
}

// compileGrantSet compiles operator Cedar source as a GRANT policy (no base permit).
// A syntactically invalid policy fails here so a broken policy never reaches the hot
// path (deny-closed: the caller keeps the prior compiling set).
func compileGrantSet(src string) (*grantSet, error) {
	ps, err := cedar.NewPolicySetFromBytes("governance.grants.cedar", []byte(src))
	if err != nil {
		return nil, err
	}
	return &grantSet{policies: ps, source: src}, nil
}

// scopeResolver reads the scope lineage of a request's resource from the store
// and builds the Cedar entity graph (the resource with its workspace/folder/agent-
// group parents, plus those container entities). It holds the module's tenant-scoped
// data handle; every read runs in ONE read-only View pinned to the request tenant.
type scopeResolver struct {
	data dataView
}

// dataView is the minimal read seam the resolver needs (satisfied by api.ModuleData).
// Narrowing it to View keeps the resolver from ever mutating on the authorization
// path and makes it trivial to fake in tests.
type dataView interface {
	View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error
}

// resolve builds the Cedar request entities for req: the principal entity (with its
// role/user parents) and the resource entity (with its resolved scope parents).
// All store reads happen inside a single View; a store error fails the caller closed.
func (r *scopeResolver) resolve(ctx context.Context, req auth.Request) (cedar.EntityMap, cedar.EntityUID, cedar.EntityUID, error) {
	em := cedar.EntityMap{}
	pUID := buildPrincipalEntity(req, em)

	resUID := resourceUID(req)
	attrs := baseResourceAttrs(req)

	// Fast path: a collection-level action with no declared workspace has no scope to
	// resolve, so it never opens a store transaction (the eventing path's ScopedPrincipal
	// deliveries and any non-entity, non-workspace request land here). A scope-tree grant
	// simply will not match; an attribute grant still can off the request attributes.
	if req.Resource.ID == "" && req.Resource.WorkspaceID.IsZero() {
		em[resUID] = cedar.Entity{UID: resUID, Attributes: cedar.NewRecord(attrs)}
		return em, resUID, pUID, nil
	}

	err := r.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		parents, extra, e := r.readScope(ctx, sc, req, em)
		if e != nil {
			return e
		}
		for k, v := range extra {
			attrs[k] = v
		}
		em[resUID] = cedar.Entity{UID: resUID, Parents: cedar.NewEntityUIDSet(parents...), Attributes: cedar.NewRecord(attrs)}
		return nil
	})
	if err != nil {
		return nil, cedar.EntityUID{}, cedar.EntityUID{}, err
	}
	return em, resUID, pUID, nil
}

// buildPrincipalEntity materializes the principal as a Cedar entity with its
// role-in-tenant, owning user and directory-group memberships as parents (so a
// grant/forbid may target `Role::"admin"`, `User::"<id>"` or `Group::"<group-id>"`)
// and its kind/AAL/delegation state as attributes (so a rule may gate on
// `context.aal`, principal kind or whether the credential is delegated). It adds
// the entity to em and returns its UID. It needs NO store access — everything is
// on req.Principal, including the GATED group closure loadGrants resolved
// (Principal.GroupsIn) — so BOTH the scope-resolved path (Scoped) and the
// store-free restrict-view (evalGrantBasic, the hooks PEP) build the IDENTICAL
// principal graph; otherwise a `forbid(principal in Role::…)` (or a group-subject
// grant) would match on one path and silently not fire on the other.
func buildPrincipalEntity(req auth.Request, em cedar.EntityMap) cedar.EntityUID {
	uid := cedar.NewEntityUID(cedarTypePrincipal, cedar.String(string(req.Principal.CredID)))
	var parents []cedar.EntityUID
	if !req.Principal.UserID.IsZero() {
		parents = append(parents, cedar.NewEntityUID(cedarTypeUser, cedar.String(req.Principal.UserID.String())))
	}
	if role, ok := req.Principal.RoleIn(req.Tenant); ok && role != "" {
		parents = append(parents, cedar.NewEntityUID(cedarTypeRole, cedar.String(role)))
	}
	// S256: one Group:: parent per directory group the principal is a gated member
	// of in this tenant (its direct groups AND every group they nest under). The
	// principal is `in` each, so a grant on a group — or a group it descends from —
	// matches. The set is empty for a token/synthetic principal (it carries none).
	for _, gid := range req.Principal.GroupsIn(req.Tenant) {
		parents = append(parents, cedar.NewEntityUID(cedarTypeGroup, cedar.String(gid)))
	}
	attributes := cedar.RecordMap{
		"kind":         cedar.String(string(req.Principal.Kind)),
		"aal":          cedar.Long(int64(req.Principal.AAL)),
		"is_delegated": cedar.Boolean(false),
	}
	if actAs, delegated := req.Principal.ActAs(); delegated {
		attributes["is_delegated"] = cedar.Boolean(true)
		attributes["act_as"] = cedar.String(actAs.String())
	}
	em[uid] = cedar.Entity{
		UID:        uid,
		Parents:    cedar.NewEntityUIDSet(parents...),
		Attributes: cedar.NewRecord(attributes),
	}
	return uid
}

// readScope resolves the resource's scope parents (and any store-authoritative
// extra attributes) for the request, adding container entities to em. For an entity-
// level action it reads the stored row (uncheatable workspace/path/group); for a
// collection-level action it uses only the caller-declared workspace.
func (r *scopeResolver) readScope(ctx context.Context, sc store.Scope, req auth.Request, em cedar.EntityMap) ([]cedar.EntityUID, cedar.RecordMap, error) {
	id := model.ID(req.Resource.ID)
	if id.IsZero() {
		return r.declaredScope(ctx, sc, req, em)
	}
	switch req.Resource.Kind {
	case "agent":
		a, err := sc.Agents().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return r.declaredScope(ctx, sc, req, em)
		}
		if err != nil {
			return nil, nil, err
		}
		wsUID, err := r.workspaceUID(ctx, sc, a.WorkspaceID, em)
		if err != nil {
			return nil, nil, err
		}
		groups, err := r.agentGroupParents(ctx, sc, id, em)
		if err != nil {
			return nil, nil, err
		}
		return append([]cedar.EntityUID{wsUID}, groups...), nil, nil
	case "session":
		s, err := sc.Sessions().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return r.declaredScope(ctx, sc, req, em)
		}
		if err != nil {
			return nil, nil, err
		}
		wsUID, err := r.workspaceUID(ctx, sc, s.WorkspaceID, em)
		if err != nil {
			return nil, nil, err
		}
		// A Session has no stored Sensitivity field (model.Session), so — unlike the
		// resource case below — there is no store-authoritative sensitivity to override;
		// the base request sensitivity stands. If Session ever gains one, add it here.
		extra := cedar.RecordMap{}
		if !s.ModelID.IsZero() {
			extra["model"] = cedar.String(s.ModelID.String())
		}
		if !s.AgentID.IsZero() {
			extra["agent"] = cedar.String(s.AgentID.String())
		}
		return []cedar.EntityUID{wsUID}, extra, nil
	case "resource":
		res, err := sc.Resources().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return r.declaredScope(ctx, sc, req, em)
		}
		if err != nil {
			return nil, nil, err
		}
		wsUID, err := r.workspaceUID(ctx, sc, res.WorkspaceID, em)
		if err != nil {
			return nil, nil, err
		}
		parents := resourceTreeParents(res.Path, id, wsUID, em)
		extra := cedar.RecordMap{"resource_kind": cedar.String(res.Kind)}
		// The stored sensitivity is authoritative over a caller-supplied one (the
		// caller may not know it); it is always present so a forbid never silently
		// errors-and-skips on an absent attribute.
		extra["sensitivity"] = cedar.String(res.Sensitivity)
		return parents, extra, nil
	case "agent_group":
		// F3: an entity action on a GROUP (e.g. list its members) must resolve the
		// group's TRUE workspace so a workspace-confined operator cannot read a cross-workspace
		// group. Without this case the caller's "agent"-kinded group id fell through to
		// declaredScope (no workspace tree), so the confinement never bound.
		g, err := sc.AgentGroups().Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return r.declaredScope(ctx, sc, req, em)
		}
		if err != nil {
			return nil, nil, err
		}
		wsUID, err := r.workspaceUID(ctx, sc, g.WorkspaceID, em)
		if err != nil {
			return nil, nil, err
		}
		gUID := cedar.NewEntityUID(cedarTypeAgentGroup, cedar.String(g.Slug))
		if _, seen := em[gUID]; !seen {
			em[gUID] = cedar.Entity{UID: gUID, Parents: cedar.NewEntityUIDSet(wsUID)}
		}
		return []cedar.EntityUID{wsUID, gUID}, nil, nil
	default:
		// Not an entity (model/provider/…): there is no workspace tree to
		// walk, but the caller-declared workspace (if any) still applies for an
		// attribute grant, and the request's own kind/sensitivity attributes do.
		return r.declaredScope(ctx, sc, req, em)
	}
}

// declaredScope is the scope for a collection-level action or an absent/non-tree
// entity: only the caller-DECLARED workspace (Resource.WorkspaceID), if any. It never
// reads an entity row, so a caller cannot widen a grant by naming a workspace whose
// resources they do not actually target — a workspace-scoped grant on a collection
// route is only safe where the handler itself filters to that workspace.
func (r *scopeResolver) declaredScope(ctx context.Context, sc store.Scope, req auth.Request, em cedar.EntityMap) ([]cedar.EntityUID, cedar.RecordMap, error) {
	if req.Resource.WorkspaceID.IsZero() {
		return nil, nil, nil
	}
	wsUID, err := r.workspaceUID(ctx, sc, req.Resource.WorkspaceID, em)
	if err != nil {
		return nil, nil, err
	}
	return []cedar.EntityUID{wsUID}, nil, nil
}

// workspaceUID materializes the Workspace entity for a workspace id and returns its
// UID, keyed by the workspace SLUG (zero id ⇒ the reserved "default" slug, with no
// store read — the NULL→default resolution). A dangling id (no such workspace)
// falls back to the raw id as a synthetic slug so it can never accidentally match a
// human-authored `Workspace::"<slug>"`. A real store error fails the caller closed.
func (r *scopeResolver) workspaceUID(ctx context.Context, sc store.Scope, wsID model.ID, em cedar.EntityMap) (cedar.EntityUID, error) {
	slug := model.DefaultWorkspaceSlug
	if !wsID.IsZero() {
		ws, err := sc.Workspaces().Get(ctx, wsID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			slug = wsID.String() // synthetic: matches no authored slug (deny-closed)
		case err != nil:
			return cedar.EntityUID{}, err
		default:
			slug = ws.Slug
		}
	}
	uid := cedar.NewEntityUID(cedarTypeWorkspace, cedar.String(slug))
	if _, ok := em[uid]; !ok {
		em[uid] = cedar.Entity{UID: uid}
	}
	return uid, nil
}

// targetWorkspace resolves the workspace an action targets, for the membership-
// confinement check. It returns the STORED workspace of an entity-level action (the
// uncheatable id the caller cannot forge — the same rows readScope trusts) or the caller-
// DECLARED workspace of a collection-level action. known=false means there is no
// workspace-scoped target (a tenant-level action with no declared workspace, or a non-tree
// entity) — the confinement does not restrict such an action. A store error is returned so
// the caller fails closed. Zero (the default workspace) is a REAL target: a principal confined
// to a non-default workspace is denied a default-workspace resource.
func (r *scopeResolver) targetWorkspace(ctx context.Context, req auth.Request) (model.ID, bool, error) {
	id := model.ID(req.Resource.ID)
	if id.IsZero() {
		if req.Resource.WorkspaceID.IsZero() {
			return model.ID(""), false, nil // tenant-level: no workspace target
		}
		return req.Resource.WorkspaceID, true, nil
	}
	var (
		ws    model.ID
		known bool
	)
	err := r.data.View(ctx, req.Tenant, func(sc store.Scope) error {
		switch req.Resource.Kind {
		case "agent":
			a, e := sc.Agents().Get(ctx, id)
			if errors.Is(e, store.ErrNotFound) {
				return nil // gone: no determinable workspace (the action 404s anyway)
			}
			if e != nil {
				return e
			}
			ws, known = a.WorkspaceID, true
		case "session":
			s, e := sc.Sessions().Get(ctx, id)
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			ws, known = s.WorkspaceID, true
		case "resource":
			res, e := sc.Resources().Get(ctx, id)
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			ws, known = res.WorkspaceID, true
		case "agent_group":
			// F3: a group action's target workspace is the GROUP's stored workspace, so a
			// workspace-confined operator is restricted to its own workspace's groups. Without
			// this the group fell through to default (known=false ⇒ unrestricted), allowing a
			// cross-workspace group read.
			g, e := sc.AgentGroups().Get(ctx, id)
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			ws, known = g.WorkspaceID, true
		default:
			// A non-tree entity (model/provider/…) has no workspace tree; a caller-declared
			// workspace, if any, is the only target.
			if !req.Resource.WorkspaceID.IsZero() {
				ws, known = req.Resource.WorkspaceID, true
			}
		}
		return nil
	})
	if err != nil {
		return model.ID(""), false, err
	}
	return ws, known, nil
}

// agentGroupParents resolves the agent-group memberships of an agent into Cedar
// AgentGroup parents (the "fold by agent_id" that expands a per-group grant to
// its agents). Each group entity is nested under ITS workspace so a `resource in
// Workspace::X` grant also catches an agent whose group lives in X. Groups are listed
// once and indexed by id; an orphan membership (group row gone) is skipped.
func (r *scopeResolver) agentGroupParents(ctx context.Context, sc store.Scope, agentID model.ID, em cedar.EntityMap) ([]cedar.EntityUID, error) {
	members, err := drainList[model.AgentGroupMember](ctx, sc.AgentGroupMembers(), model.Query{
		Filters: []model.Filter{eq("agent_id", agentID.String())}, Limit: listCap,
	})
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}
	groups, err := drainList[model.AgentGroup](ctx, sc.AgentGroups(), model.Query{Limit: listCap})
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]model.AgentGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	var parents []cedar.EntityUID
	for _, m := range members {
		g, ok := byID[m.GroupID]
		if !ok {
			continue
		}
		gUID := cedar.NewEntityUID(cedarTypeAgentGroup, cedar.String(g.Slug))
		if _, seen := em[gUID]; !seen {
			wsUID, err := r.workspaceUID(ctx, sc, g.WorkspaceID, em)
			if err != nil {
				return nil, err
			}
			em[gUID] = cedar.Entity{UID: gUID, Parents: cedar.NewEntityUIDSet(wsUID)}
		}
		parents = append(parents, gUID)
	}
	return parents, nil
}

// resourceTreeParents builds the folder-ancestor chain for a resource from its
// materialized path ("/<root>/…/<self>") and returns the resource's direct parents:
// its immediate parent folder and its own workspace (the workspace is also reachable
// transitively through the chain root, but naming it directly keeps `resource in
// Workspace::X` robust against a malformed path). Each proper ancestor is added to em
// as a Resource entity linked to the next, with the root nested under the workspace.
// A legacy resource with an empty path is a root: its only container is the workspace.
func resourceTreeParents(path string, self model.ID, wsUID cedar.EntityUID, em cedar.EntityMap) []cedar.EntityUID {
	ids := splitPath(path)
	if n := len(ids); n > 0 && ids[n-1] == self {
		ids = ids[:n-1] // drop self: it is not its own ancestor
	}
	if len(ids) == 0 {
		return []cedar.EntityUID{wsUID}
	}
	var prev cedar.EntityUID
	for i, id := range ids {
		uid := cedar.NewEntityUID(cedarTypeResource, cedar.String(id.String()))
		var p []cedar.EntityUID
		if i == 0 {
			p = []cedar.EntityUID{wsUID}
		} else {
			p = []cedar.EntityUID{prev}
		}
		if _, seen := em[uid]; !seen {
			em[uid] = cedar.Entity{UID: uid, Parents: cedar.NewEntityUIDSet(p...)}
		}
		prev = uid
	}
	return []cedar.EntityUID{prev, wsUID}
}

// splitPath parses a materialized path "/<a>/<b>/<c>" into [a, b, c] (empty for a
// NULL/empty legacy path).
func splitPath(path string) []model.ID {
	var ids []model.ID
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			ids = append(ids, model.ID(p))
		}
	}
	return ids
}

// resourceUID is the Cedar resource entity UID for a request: the entity id, or "*"
// for a collection-level action (no specific resource), mirroring cedar.go.
func resourceUID(req auth.Request) cedar.EntityUID {
	id := req.Resource.ID
	if id == "" {
		id = "*"
	}
	return cedar.NewEntityUID(cedarTypeResource, cedar.String(id))
}

// baseResourceAttrs are the resource attributes ALWAYS present on the resource entity
// (kind + sensitivity), seeded from the request and from any caller Extra. They are
// unconditional for the same reason as cedar.go: an ABSENT attribute makes a
// forbid/permit condition ERROR and be silently skipped by Cedar, so a deny rule
// could be neutralized; a present-but-empty attribute simply evaluates the condition
// to false. The store-authoritative values (resolved sensitivity) override these.
func baseResourceAttrs(req auth.Request) cedar.RecordMap {
	attrs := cedar.RecordMap{}
	for k, v := range req.Resource.Extra {
		attrs[cedar.String(k)] = cedar.String(v)
	}
	attrs["kind"] = cedar.String(req.Resource.Kind)
	attrs["sensitivity"] = cedar.String(req.Resource.Sensitivity)
	return attrs
}

// scopedContext builds the Cedar request context: the same fields cedar.go exposes
// (tenant, principal_kind, permission, sensitivity, time) plus `aal` so a grant can
// require a step-up (`context.aal >= 3`).
func scopedContext(req auth.Request, now time.Time) cedar.Record {
	return cedar.NewRecord(cedar.RecordMap{
		"tenant":         cedar.String(string(req.Tenant)),
		"principal_kind": cedar.String(string(req.Principal.Kind)),
		"permission":     cedar.String(string(req.Permission)),
		"sensitivity":    cedar.String(req.Resource.Sensitivity),
		"aal":            cedar.Long(int64(req.Principal.AAL)),
		"time":           cedar.Long(now.Unix()),
	})
}

// actionUID is the Cedar action entity for a permission.
func actionUID(req auth.Request) cedar.EntityUID {
	return cedar.NewEntityUID(cedarTypeAction, cedar.String(string(req.Permission)))
}

// listLister is the read surface drainList needs (any typed repository's List).
type listLister[T any] interface {
	List(ctx context.Context, q model.Query) ([]T, model.Page, error)
}

// drainList reads every page of a List query (bounded by the store's per-page cap),
// so a scope resolution is never silently truncated by pagination. The result sets
// here (an agent's groups, a tenant's groups) are small; a per-tenant cache is the
// documented follow-up.
func drainList[T any](ctx context.Context, repo listLister[T], q model.Query) ([]T, error) {
	var out []T
	for {
		page, pg, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if !pg.HasMore || pg.Cursor == "" {
			return out, nil
		}
		q.Cursor = pg.Cursor
	}
}

// --- the live, per-tenant scoped-grant engine -------------------------------------

// scopedEngine is the live grant engine the Authorizer's ScopedAuthorizer seam calls.
// It is per-tenant and swappable (an admin publishes a tenant's grants over the C
// PDP authoring surface, pdp_authoring.go), resolving each request's scope through the
// shared scopeResolver. It is ALSO the restrict-view PolicyEvaluator the hooks PEP
// consults as a deny-overlay — the SAME authored policy, evaluated forbid-only
// without scope resolution (a tool-call has no entity to resolve). Safe for concurrent
// use; the hot path reads the per-tenant set under an RLock.
type scopedEngine struct {
	mu sync.RWMutex
	// tenants is the WHOLE live authority snapshot per tenant. It is deliberately
	// one value rather than independent set/selection/freshness maps: callers must
	// never observe a new Cedar set with an old generation, a new signed bound with
	// a process-clock stamp, or any other cross-snapshot mixture. Every replacement
	// is one mutex-protected assignment after a coherent durable View.
	tenants map[model.TenantID]scopedTenantState
	// maxStaleness is the deployment-wide offline-trust bound (ADR-0024 Q1), wired ONCE at
	// boot from OLIVARES_POLICY_MAX_STALENESS. Zero ⇒ no bound: the connected-node default,
	// so a deployment that never opts into offline-trust behaves EXACTLY as before (a grant
	// never expires). It is set before the server serves — the same happens-before edge as
	// resolver/now/log — so the hot path reads it without a lock.
	maxStaleness time.Duration
	// resolver, now and log are wired ONCE during boot (UseData/Init) before the server
	// starts serving, exactly like the module's other data handles (m.eval.data). The
	// boot→serve goroutine start is the happens-before edge, so the hot path reads them
	// without a lock; tenant snapshots are swapped at runtime under mu.
	resolver *scopeResolver
	now      func() time.Time
	log      *slog.Logger // nil-safe; logs scoped grants/forbids and per-policy eval errors
}

var (
	_ auth.ScopedAuthorizer = (*scopedEngine)(nil)
	_ auth.PolicyEvaluator  = (*scopedEngine)(nil)
)

// activationID identifies WHICH STORED SELECTION a loaded set came from: the
// selected revision number of each contributing surface (0 = none selected).
//
// ⛔ TEXT IS NOT IDENTITY, and assuming it was is how this field first shipped
// wrong. appendRevision never deduplicates content (revision.go), so publishing
// the SAME bytes twice creates a second revision — and if that second publish's
// swap FAILS, a digest of the loaded source still matches the store's union and
// reports "applied" for a swap that never happened. The revision numbers cannot
// collide that way.
type activationID struct {
	authored int64
	managed  int64
	adopted  int64
}

// scopedTenantState is an immutable snapshot installed as one unit. The individual
// surface digests preserve byte/provenance identity for authored, managed and adopted
// Cedar even where their normalized concatenation is identical. `unionDigest` binds
// the compiled set to the exact union source. Immutable revision numbers normally imply
// all four digests, but checking them makes a broken/malicious store decorator fail
// closed rather than silently treating two same-generation snapshots as interchangeable.
// Freshness carries the complete signed DDIL anchor tuple even though the hot path only
// needs RefreshedAt and MaxStaleness; equality must not erase that authority boundary.
type scopedTenantState struct {
	set            *grantSet
	selection      activationID
	generation     store.AuthorizationFactRef
	authoredDigest string
	managedDigest  string
	adoptedDigest  string
	unionDigest    string
	freshness      FreshnessRecord
	// identityIncomplete records a generation witness that was read before the
	// durable snapshot failed to read completely. It is deliberately explicit:
	// a complete Cedar policy with no selected surface has the same zero
	// selection/digest shape, but is a valid empty authority snapshot. An
	// incomplete identity is only a fail-closed high-water fence and can never
	// be compiled, reported applied, or compared as an empty policy.
	identityIncomplete bool
	// available means this process has one coherent compiled snapshot. False is an
	// operational unavailable sentinel installed only after reload/boot failure; it
	// carries no raw policy text and makes both Scoped and restrict-view Evaluate
	// fail closed until a coherent snapshot recovers it.
	available      bool
	freshnessValid bool
	// operation is an opaque, in-process CAS token. It is never serialized or
	// derived from authority data: every coherent install/replay and every
	// unavailable mark replaces it, so a stale failure cannot poison a snapshot
	// that was successfully revalidated at the same durable generation.
	operation *scopedStateOperation
}

// Non-zero sized on purpose: distinct allocations must have distinct identities.
type scopedStateOperation struct{ marker byte }

func nextScopedStateOperation() *scopedStateOperation { return &scopedStateOperation{} }

type scopedInstallResult uint8

const (
	scopedInstallApplied scopedInstallResult = iota
	scopedInstallAlreadyCurrent
	scopedInstallOlder
)

var errScopedSnapshotSameGenerationMismatch = errors.New("governance: same authorization generation carries a different Cedar snapshot")

var errScopedSnapshotCompiledBinding = errors.New("governance: Cedar snapshot has no matching compiled grant set")

// errScopedSnapshotStaleOperation means a reload's durable View was coherent, but a
// later operation changed this process's state at that same generation before the
// candidate could install. Applying the old candidate could resurrect a snapshot that a
// newer reload intentionally made unavailable, so callers retain the newer state.
var errScopedSnapshotStaleOperation = errors.New("governance: Cedar reload observed a stale runtime operation")

// sameIdentity is intentionally stricter than activation identity. A generation is a
// durable authorization witness, not a best-effort cache key: equal generations may be
// replayed only when every durable authority fact read with them agrees. The transient
// freshnessValid safety bit is intentionally excluded so a failed bounded reload can be
// recovered by a later coherent read of the same durable generation.
func (s scopedTenantState) sameIdentity(other scopedTenantState) bool {
	if s.identityIncomplete || other.identityIncomplete {
		return false
	}
	return s.selection == other.selection &&
		s.authoredDigest == other.authoredDigest &&
		s.managedDigest == other.managedDigest &&
		s.adoptedDigest == other.adoptedDigest &&
		s.unionDigest == other.unionDigest &&
		s.freshness.RefreshedAt.Equal(other.freshness.RefreshedAt) &&
		s.freshness.MaxStaleness == other.freshness.MaxStaleness &&
		s.freshness.AdoptedRevision == other.freshness.AdoptedRevision &&
		s.freshness.AdoptedCreatedAt.Equal(other.freshness.AdoptedCreatedAt)
}

// hasCedarCompiledBinding proves that the in-memory evaluator is bound to the
// source digest in this state. The digest is deliberately part of durable
// identity while the pointer is not, but a status response may only say
// `applied` when the pointer actually represents those bytes. An explicitly
// selected empty union is valid only with no grantSet at all.
func hasCedarCompiledBinding(state scopedTenantState) bool {
	if state.identityIncomplete {
		return false
	}
	if state.unionDigest == "" {
		return state.set == nil
	}
	return state.set != nil && contentDigest(state.set.source) == state.unionDigest
}

// tenantState returns one self-consistent installed snapshot. Its bool distinguishes a
// never-loaded tenant from an explicitly installed empty union.
func (e *scopedEngine) tenantState(tenant model.TenantID) (scopedTenantState, bool) {
	if e == nil {
		return scopedTenantState{}, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	state, ok := e.tenants[tenant]
	return state, ok
}

// installIfNotOlder atomically installs a coherent durable snapshot. It is retained for
// callers that already own their runtime operation (including focused engine tests).
// Reload paths that read durable state outside the mutex must use
// installIfNotOlderFromObservedState so a same-G candidate cannot act after another
// operation has changed the cache token.
func (e *scopedEngine) installIfNotOlder(tenant model.TenantID, next scopedTenantState) (scopedInstallResult, error) {
	return e.installIfNotOlderObserved(tenant, nil, false, next)
}

// installIfNotOlderFromObservedState atomically installs a coherent durable snapshot
// whose View began with expected. G+1 may always replace an older runtime state, and G
// may never replace G+1. At an equal generation, a different durable identity is an
// unconditional fail-closed conflict; only an exact identity requires the operation
// token to match. Without that CAS, reload A can read G/T0, reload B can mark G
// unavailable/T1, and A can incorrectly resurrect G from T0.
func (e *scopedEngine) installIfNotOlderFromObservedState(
	tenant model.TenantID,
	expected scopedTenantState,
	expectedLoaded bool,
	next scopedTenantState,
) (scopedInstallResult, error) {
	return e.installIfNotOlderObserved(tenant, &expected, expectedLoaded, next)
}

func (e *scopedEngine) installIfNotOlderObserved(
	tenant model.TenantID,
	expected *scopedTenantState,
	expectedLoaded bool,
	next scopedTenantState,
) (scopedInstallResult, error) {
	if e == nil {
		return scopedInstallOlder, errors.New("governance: scoped grant engine unavailable")
	}
	if !validPolicyAuthorizationEpochFact(tenant, next.generation) {
		return scopedInstallOlder, policyAuthorizationEpochUnavailable("reload snapshot has no exact authorization epoch", nil)
	}
	if !hasCedarCompiledBinding(next) {
		return scopedInstallOlder, errScopedSnapshotCompiledBinding
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tenants == nil {
		e.tenants = map[model.TenantID]scopedTenantState{}
	}
	current, loaded := e.tenants[tenant]
	if expected != nil && expectedLoaded && !loaded {
		return scopedInstallOlder, errScopedSnapshotStaleOperation
	}
	if loaded {
		// A cold failure can install an unavailable sentinel with no durable
		// generation. It is not an authority snapshot, so a reload that *started
		// after that sentinel may recover it with a coherent G. A reload that
		// started before the sentinel, however, must not erase the later failure:
		// require the observed token just as for equal durable generations.
		if !current.available && !validPolicyAuthorizationEpochFact(tenant, current.generation) {
			if expected != nil && (!expectedLoaded || current.operation != expected.operation) {
				return scopedInstallOlder, errScopedSnapshotStaleOperation
			}
			next.operation = nextScopedStateOperation()
			e.tenants[tenant] = next
			return scopedInstallApplied, nil
		}
		switch {
		case next.generation.Version < current.generation.Version:
			return scopedInstallOlder, nil
		case next.generation.Version == current.generation.Version:
			// A generation-only high-water fence is not a zero/empty Cedar
			// identity. A complete candidate at exactly that generation may recover
			// it in one swap, but only when its View began after the fence's
			// operation; a candidate that started earlier is still stale.
			if current.identityIncomplete {
				if expected == nil || !expectedLoaded || current.operation != expected.operation {
					return scopedInstallOlder, errScopedSnapshotStaleOperation
				}
				next.operation = nextScopedStateOperation()
				e.tenants[tenant] = next
				return scopedInstallApplied, nil
			}
			if !next.sameIdentity(current) {
				// A same-G identity conflict is durable-authority evidence, not a
				// transient operation failure. In particular, reload A can read
				// bytes B at G while an older reload B finishes its A/T0 replay as
				// A/T1. Token-CAS must not leave A available merely because B
				// completed later. If current is available, install an
				// identity-shaped unavailable sentinel for the conflicting durable
				// candidate. If a prior conflict already made current unavailable,
				// however, a stale candidate must not rewrite that sentinel: only a
				// View that observed its exact operation may advance to a different
				// unavailable identity. A later exact reread of the retained identity
				// may recover it, but no old compiled set authorizes through conflict.
				if !current.available && (expected == nil || !expectedLoaded || current.operation != expected.operation) {
					return scopedInstallOlder, errScopedSnapshotStaleOperation
				}
				e.tenants[tenant] = unavailableScopedSnapshot(next)
				return scopedInstallOlder, errScopedSnapshotSameGenerationMismatch
			}
			// The durable candidate was captured before expected. Once its full
			// identity agrees, a same-G update must prove that no intervening
			// reload/replay/failure changed the operational token. A valid G+1
			// above remains monotonic without this constraint. An unversioned
			// cold sentinel is separately token-gated above, because an older read
			// could otherwise erase a later failure.
			if expected != nil && (!expectedLoaded || current.operation != expected.operation) {
				return scopedInstallOlder, errScopedSnapshotStaleOperation
			}
			// A coherent durable snapshot can itself prove that authority is
			// unavailable: a selected bounded policy without its required
			// freshness anchor must not leave an older same-G evaluator able to
			// return Allow through the restrict-view. This is not a stale failure
			// mark; it is the current durable result, installed atomically here.
			if current.available && !next.available {
				next.operation = nextScopedStateOperation()
				e.tenants[tenant] = next
				return scopedInstallApplied, nil
			}
			// A prior reload may have installed the exact durable snapshot with its
			// operational freshness bit forced false after a transient read failure.
			// A later coherent read of the SAME facts is the only legal recovery.
			// Never use this branch to downgrade a known-good same-G snapshot.
			if !hasCedarCompiledBinding(current) ||
				(!current.available && next.available) ||
				(!current.freshnessValid && next.freshnessValid) {
				next.operation = nextScopedStateOperation()
				e.tenants[tenant] = next
				return scopedInstallApplied, nil
			}
			// An exact durable replay is still an operation: advance the opaque token
			// so a failure that began before this successful validation cannot mark
			// the cache unavailable afterwards.
			current.operation = nextScopedStateOperation()
			e.tenants[tenant] = current
			return scopedInstallAlreadyCurrent, nil
		}
	}
	next.operation = nextScopedStateOperation()
	e.tenants[tenant] = next
	return scopedInstallApplied, nil
}

// unavailableScopedSnapshot retains only the exact durable authority identity of a
// failed reload. The compiled source is deliberately discarded: the snapshot's purpose
// is to fence every authorization seam until that exact selection/digest/freshness tuple
// can be read and compiled again, not to preserve policy text from a failed attempt.
// Its operation token is always new, so a reload that started before the failure cannot
// recover it merely by finishing later.
func unavailableScopedSnapshot(snapshot scopedTenantState) scopedTenantState {
	snapshot.set = nil
	snapshot.available = false
	snapshot.operation = nextScopedStateOperation()
	return snapshot
}

// unavailableScopedGenerationFence retains a witnessed authorization generation when
// the remainder of its durable Cedar snapshot could not be read. It must not use zero
// selection/digests as a proxy for "unknown": a complete empty union is legitimate.
// The explicit incomplete bit makes this a fail-closed high-water fence that a complete
// same-G reread can recover in one operation.
func unavailableScopedGenerationFence(generation store.AuthorizationFactRef) scopedTenantState {
	return scopedTenantState{
		generation:         generation,
		identityIncomplete: true,
		available:          false,
		operation:          nextScopedStateOperation(),
	}
}

// markUnavailableIfStillSame is the compatibility entry point for a transient reload or
// boot failure. With no coherent durable snapshot, it installs a fail-closed sentinel
// only if the runtime state is still the one observed before that operation. A durable
// snapshot failure must instead use markUnavailableForDurableReloadFailure, whose epoch
// and identity witnesses can safely dominate an older cache entry.
func (e *scopedEngine) markUnavailableIfStillSame(
	tenant model.TenantID,
	expected scopedTenantState,
	expectedLoaded bool,
) {
	e.markUnavailableForTransientReloadFailure(tenant, expected, expectedLoaded)
}

// markUnavailableForTransientReloadFailure handles a reload failure for which no
// coherent durable snapshot was obtained (for example, a View/port error). It has no
// generation or identity witness, so the operation token is its only safe ordering
// signal: a later successful replay or G+1 install must not be poisoned by an older
// transient failure.
func (e *scopedEngine) markUnavailableForTransientReloadFailure(
	tenant model.TenantID,
	expected scopedTenantState,
	expectedLoaded bool,
) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state, loaded := e.tenants[tenant]
	if !expectedLoaded {
		if loaded {
			return
		}
		if e.tenants == nil {
			e.tenants = map[model.TenantID]scopedTenantState{}
		}
		e.tenants[tenant] = scopedTenantState{operation: nextScopedStateOperation()}
		return
	}
	if !loaded || state.operation != expected.operation {
		return
	}
	state.available = false
	state.freshnessValid = false
	state.operation = nextScopedStateOperation()
	e.tenants[tenant] = state
}

// markUnavailableForObservedGenerationFailure closes a reload error that happened
// AFTER it read an exact authorization epoch but BEFORE it obtained a complete Cedar
// identity. The witnessed generation is a high-water fence, not an empty policy:
//
//   - Gf > live G (and any cold/unversioned state) installs a generation-only
//     unavailable fence regardless of the operation token;
//   - Gf < live G is stale and cannot poison it;
//   - Gf == live G also installs that fence regardless of the operation token.
//
// Once an error follows the epoch read, the durable identity is *incomplete*: it may
// already have observed a changed authored/managed/adopted/freshness component before
// another component failed. Treating equal G as a token-only transient would let a
// delayed old replay rotate the token and continue authorizing from bytes that may no
// longer be the durable authority. The incomplete fence therefore dominates every
// current state at Gf or below. A coherent same-G reread that starts after the fence
// installs directly; it does not need a sacrificial first retry merely to replace an
// incomplete identity.
func (e *scopedEngine) markUnavailableForObservedGenerationFailure(
	tenant model.TenantID,
	expected scopedTenantState,
	expectedLoaded bool,
	generation store.AuthorizationFactRef,
) {
	if e == nil {
		return
	}
	if !validPolicyAuthorizationEpochFact(tenant, generation) {
		e.markUnavailableForTransientReloadFailure(tenant, expected, expectedLoaded)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tenants == nil {
		e.tenants = map[model.TenantID]scopedTenantState{}
	}
	current, loaded := e.tenants[tenant]
	if !loaded || !validPolicyAuthorizationEpochFact(tenant, current.generation) {
		e.tenants[tenant] = unavailableScopedGenerationFence(generation)
		return
	}
	switch {
	case generation.Version > current.generation.Version:
		e.tenants[tenant] = unavailableScopedGenerationFence(generation)
		return
	case generation.Version < current.generation.Version:
		return
	}
	// The error followed a durable epoch observation, so an equal G is not an
	// innocuous operation failure. It can carry a partially observed identity
	// (for example authored B followed by a managed-list error) that conflicts
	// with a delayed exact replay of authored A. Replace the full state with an
	// explicit incomplete fence even when that replay has already rotated the
	// operation token. Only a subsequent complete snapshot read can recover it.
	e.tenants[tenant] = unavailableScopedGenerationFence(generation)
}

// markUnavailableForDurableReloadFailure closes a reload failure after a coherent
// durable snapshot was captured but before it could become an available evaluator
// (typically Cedar compilation or a same-G install conflict). The observed generation
// and byte/provenance identity carry stronger ordering evidence than an in-process
// operation token:
//
//   - a failed G+1 dominates an older live G even if an old G replay rotated its token;
//   - a failed older G cannot poison a live G+1;
//   - at equal G, a different selection/digest/freshness identity is itself a
//     fail-closed conflict even if an old replay rotated the token;
//   - only an equal, exact durable identity falls back to token-CAS, because that is a
//     transient failure rather than new conflicting authority.
//
// The dominant case installs an unavailable identity-shaped snapshot rather than
// mutating the old one. A later coherent reread of that exact identity can therefore
// install its compiled set atomically; no raw policy source is retained here.
func (e *scopedEngine) markUnavailableForDurableReloadFailure(
	tenant model.TenantID,
	expected scopedTenantState,
	expectedLoaded bool,
	failed scopedTenantState,
) {
	if e == nil {
		return
	}
	if !validPolicyAuthorizationEpochFact(tenant, failed.generation) {
		e.markUnavailableForTransientReloadFailure(tenant, expected, expectedLoaded)
		return
	}
	if failed.identityIncomplete {
		e.markUnavailableForObservedGenerationFailure(tenant, expected, expectedLoaded, failed.generation)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tenants == nil {
		e.tenants = map[model.TenantID]scopedTenantState{}
	}
	current, loaded := e.tenants[tenant]
	if !loaded {
		// The durable failure is more informative than a cold sentinel: retain
		// its exact identity so a future exact reread can recover in one swap.
		e.tenants[tenant] = unavailableScopedSnapshot(failed)
		return
	}
	if !validPolicyAuthorizationEpochFact(tenant, current.generation) {
		// A cold sentinel proves only that a reload failed; it has no durable
		// generation or identity that can order it against this witnessed
		// failure. Let the latter dominate. Otherwise a candidate captured before
		// the cold mark can recover through its matching token despite the newer
		// durable snapshot already being known bad.
		e.tenants[tenant] = unavailableScopedSnapshot(failed)
		return
	}
	switch {
	case failed.generation.Version > current.generation.Version:
		e.tenants[tenant] = unavailableScopedSnapshot(failed)
		return
	case failed.generation.Version < current.generation.Version:
		return
	case !failed.sameIdentity(current):
		// The runtime may already be unavailable from a later same-G conflict.
		// A failure whose View began before that operation carries no ordering
		// privilege to overwrite the sentinel's identity; retaining it keeps a
		// subsequent exact reread able to recover in one pass. A View that saw
		// the current token may advance to the newly observed unavailable identity.
		if !current.available && (!expectedLoaded || current.operation != expected.operation) {
			return
		}
		e.tenants[tenant] = unavailableScopedSnapshot(failed)
		return
	}
	// At an equal exact durable identity, no durable fact orders the failure after
	// a later replay. Preserve the existing token-CAS behavior.
	if !expectedLoaded || current.operation != expected.operation {
		return
	}
	current.available = false
	current.freshnessValid = false
	current.operation = nextScopedStateOperation()
	e.tenants[tenant] = current
}

// grantExpired reports whether the tenant's POSITIVE scoped grants have expired under
// the offline-staleness bound (ADR-0024 Q1): the deployment set a maxStaleness AND the
// active policy has not been re-established within it. It is the ONLY thing that turns a
// Cedar permit into a deny-closed abstain — forbid rules are NEVER expired (a stale
// restriction can only restrict, never escalate). With no bound (the connected-node
// default) it always returns false, so nothing changes for a normal deployment.
func (e *scopedEngine) grantExpired(tenant model.TenantID) bool {
	state, loaded := e.tenantState(tenant)
	return e.grantExpiredState(state, loaded, e.clock())
}

// grantExpiredState evaluates expiry from the SAME immutable state used for a Cedar
// decision. It is deliberately separate from grantExpired so Scoped can hold one read
// across policy evaluation; otherwise a reload between getSet and expiry would mix G
// with G+1's freshness/bound.
func (e *scopedEngine) grantExpiredState(state scopedTenantState, loaded bool, now time.Time) bool {
	// Expiry is meaningful only for a *loaded, available* set whose forbids remain
	// evaluated while its permits degrade to abstain. An absent runtime or an
	// unavailable sentinel has no such set: both authorization seams already fail
	// closed and GET reports live_activation=deferred, rather than inventing an
	// expired policy state that this process did not load.
	if !loaded || !state.available || state.selection == (activationID{}) {
		return false
	}
	bound := e.maxStaleness
	if loaded && state.freshness.MaxStaleness > 0 {
		bound = state.freshness.MaxStaleness
	}
	// Strict precedence is tenant override > deployment default > no bound.
	if bound <= 0 {
		return false
	}
	// A bounded selection with no valid durable anchor is not "fresh by absence".
	// Preserve forbids in the compiled set, but expire all permits until a coherent
	// reload can prove the anchor. This closes failed boot/backfill and partial-capability
	// paths without inventing a process-time stamp.
	if !state.freshnessValid || state.freshness.RefreshedAt.IsZero() {
		return true
	}
	return now.Sub(state.freshness.RefreshedAt) > bound
}

// clock returns the engine's time source (injectable for tests).
func (e *scopedEngine) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// Scoped resolves the request's scope and evaluates the tenant's authored grant
// policy, returning the three-valued effect. A tenant with no authored grants ABSTAINS
// IMMEDIATELY — before any store read — so the authorization hot path pays nothing
// until an operator opts into scoped grants. Resolution or evaluation that cannot
// complete fails CLOSED (a scope policy we cannot evaluate must not silently drop a
// forbid).
func (e *scopedEngine) Scoped(ctx context.Context, req auth.Request) (auth.ScopedDecision, error) {
	// WORKSPACE CONFINEMENT. A principal whose membership is confined to a workspace
	// (Membership.WorkspaceID) may act ONLY within it — any action targeting a DIFFERENT
	// workspace is FORBIDDEN, overriding its tenant-wide RBAC role (the forbid effect, which
	// the Authorizer applies before RBAC). Checked BEFORE the no-grants fast path so it holds
	// even for a tenant with no authored scoped grants. Additive/back-compat: a principal with
	// no workspace-scoped membership (ConfinedWorkspaceIn ok=false) skips this entirely — every
	// existing decision is byte-identical. Superadmin is never confined.
	if confinedWS, confined := req.Principal.ConfinedWorkspaceIn(req.Tenant); confined && !req.Principal.Superadmin {
		if e.resolver == nil {
			return auth.ScopedDecision{}, errors.New("governance: scope resolver unavailable")
		}
		targetWS, known, err := e.resolver.targetWorkspace(ctx, req)
		if err != nil {
			return auth.ScopedDecision{}, err // deny-closed: cannot determine the target workspace
		}
		switch {
		case known && targetWS != confinedWS:
			// A resolved entity/workspace action on a DIFFERENT workspace: forbidden.
			e.logEffect(req, "workspace-confinement-forbid")
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "workspace confinement: action targets a workspace outside the principal's membership"}, nil
		case !known && auth.IsAccessGraphReconPerm(req.Permission):
			// A tenant-wide access-MATRIX recon READ (access graph, drift, attack-paths, who-can-
			// access) with no workspace to filter on: a confined principal has no scoped view of
			// the full cross-workspace matrix, so forbid it here (covering every route that uses
			// these perms in one place, F2) rather than abstaining to a tenant-wide read.
			e.logEffect(req, "workspace-confinement-forbid-recon-read")
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "workspace confinement: a confined principal cannot read the tenant-wide access graph"}, nil
		case !known && req.Permission.Verb() != auth.VerbRead:
			// An INDETERMINATE target for a WRITE/ADMIN action — a collection create (the body
			// workspace is not on the authz request), a tenant-level config/membership/token
			// write, a non-workspace-scoped resource, or a missing entity. A confined principal
			// carries no tenant-level authority, so it may not perform a mutation it cannot prove
			// targets its own workspace (that would be a cross-workspace or tenant-wide write —
			// the class of escape the adversarial review found). Deny-closed. A READ is left to
			// ABSTAIN so the principal still reaches tenant-level reads it is entitled to,
			// row-filtered to its workspace at the handler (e.g. the members roster).
			e.logEffect(req, "workspace-confinement-forbid-indeterminate-write")
			return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "workspace confinement: a confined principal cannot perform a tenant-level or cross-workspace write"}, nil
		}
	}

	state, loaded := e.tenantState(req.Tenant)
	if loaded && !state.available {
		return auth.ScopedDecision{}, errors.New("governance: scoped Cedar snapshot unavailable")
	}
	set := state.set
	if set == nil {
		return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "no scoped grants for tenant"}, nil
	}
	if e.resolver == nil {
		return auth.ScopedDecision{}, errors.New("governance: scope resolver unavailable")
	}
	em, resUID, pUID, err := e.resolver.resolve(ctx, req)
	if err != nil {
		return auth.ScopedDecision{}, err
	}
	now := e.clock()
	creq := cedar.Request{Principal: pUID, Action: actionUID(req), Resource: resUID, Context: scopedContext(req, now)}
	decision, diag := cedar.Authorize(set.policies, em, creq)
	e.logDiagErrors(diag)
	// F-06: a forbid rule that errored is a restriction Cedar dropped — fail
	// CLOSED (EffectForbid) rather than let a permit/grant stand. Selective: a permit
	// error would only have widened, so it stays dropped. This must precede the grant
	// case so a dropped restriction wins over a scoped grant.
	if hasErroredForbid(set.policies, diag) {
		e.logEffect(req, "forbid-error")
		return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "cedar: forbid rule evaluation error (fail-closed)"}, nil
	}
	switch {
	case decision == cedar.Allow:
		if e.grantExpiredState(state, loaded, now) {
			// ADR-0024 Q1: offline, past policy_max_staleness, a positive grant expires
			// deny-closed. Return ABSTAIN (not a hard deny) so the request falls back to
			// RBAC and the deny-overlay — the expired GRANT stops authorizing, it does not
			// halt the node (that would be the rejected Q1-B mission-kill). The forbid/deny
			// cases below are unaffected: a stale restriction stays enforced.
			e.logEffect(req, "grant-expired")
			return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "cedar: scoped grant expired (policy staleness exceeded)"}, nil
		}
		e.logEffect(req, "grant")
		return auth.ScopedDecision{Effect: auth.EffectGrant, Reason: "cedar: scoped grant"}, nil
	case len(diag.Reasons) > 0:
		e.logEffect(req, "forbid")
		// A cleanly-evaluated authored scoped forbid: business policy (shadowable).
		// Confinement (above) and errored/fail-closed forbids stay ClassInvariant.
		return auth.ScopedDecision{Effect: auth.EffectForbid, Reason: "cedar: forbidden by policy", Class: auth.ClassPolicy}, nil
	default:
		return auth.ScopedDecision{Effect: auth.EffectAbstain, Reason: "cedar: no grant matched"}, nil
	}
}

// Evaluate is the restrict-view of the SAME authored policy for the hooks PEP: a
// forbid-only deny-overlay over a BASIC resource entity (no scope resolution — a
// tool-call carries no entity to resolve). Scope-conditioned forbids that otherwise
// target the request fail closed; a grant/abstain imposes no restriction.
func (e *scopedEngine) Evaluate(_ context.Context, req auth.Request) (auth.Decision, error) {
	state, loaded := e.tenantState(req.Tenant)
	if loaded && !state.available {
		return auth.Decision{}, errors.New("governance: scoped Cedar snapshot unavailable")
	}
	set := state.set
	if set == nil {
		return auth.Decision{Allow: true, Reason: "no authored grant policy for tenant"}, nil
	}
	effect, diag, scopeFailClosed := evalGrantBasicDetailed(set, req, e.clock())
	e.logDiagErrors(diag)
	if scopeFailClosed {
		e.logEffect(req, "scope-forbid-fail-closed")
		return auth.Decision{Allow: false, Reason: "cedar: scope-conditioned forbid unresolvable in restrict-view (fail-closed)"}, nil
	}
	// F-06: evalGrantBasic already maps an errored forbid to EffectForbid, so
	// enforcement and the authoring dry-run fail closed on the SAME input.
	if effect == auth.EffectForbid {
		// Provenance: a forbid rule that ERRORED (fail-closed integrity) is
		// ClassInvariant; a cleanly-evaluated authored forbid is business policy. The
		// H-03 unresolvable-scope forbid already took the scopeFailClosed branch above.
		class := auth.ClassPolicy
		if hasErroredForbid(set.policies, diag) {
			class = auth.ClassInvariant
		}
		return auth.Decision{Allow: false, Reason: "cedar: forbidden by policy", Class: class}, nil
	}
	return auth.Decision{Allow: true, Reason: "cedar: no restriction"}, nil
}

// evalGrantBasic evaluates a grant set against req with a BASIC resource entity (no
// Scope resolution) and returns the three-valued effect plus the raw diagnostic.
// It is the store-free core shared by the restrict-view (hooks PEP) and the authoring
// dry-run, where no live scope tree is available — so a scope-tree condition (`resource
// in Workspace::…`) never consults the live hierarchy (which only Scoped resolves) and
// a targeted forbid is conservatively probed against the missing parent edge.
func evalGrantBasic(set *grantSet, req auth.Request, now time.Time) (auth.Effect, cedar.Diagnostic) {
	effect, diag, _ := evalGrantBasicDetailed(set, req, now)
	return effect, diag
}

// evalGrantBasicDetailed also reports whether EffectForbid came from the H-03
// conservative scope probe, so the hooks PEP can emit a distinct diagnostic.
func evalGrantBasicDetailed(set *grantSet, req auth.Request, now time.Time) (auth.Effect, cedar.Diagnostic, bool) {
	em := cedar.EntityMap{}
	pUID := buildPrincipalEntity(req, em) // SAME principal graph as Scoped (role/user parents)
	resUID := resourceUID(req)
	em[resUID] = cedar.Entity{UID: resUID, Attributes: cedar.NewRecord(baseResourceAttrs(req))}
	creq := cedar.Request{Principal: pUID, Action: actionUID(req), Resource: resUID, Context: scopedContext(req, now)}
	decision, diag := cedar.Authorize(set.policies, em, creq)
	// F-06: a forbid rule that ERRORED is a restriction Cedar silently dropped —
	// map it to EffectForbid so every caller (the hooks-PEP restrict-view Evaluate AND the
	// authoring dry-run) fails closed consistently. A permit that errors only widens, so
	// it stays dropped.
	if hasErroredForbid(set.policies, diag) {
		return auth.EffectForbid, diag, false
	}
	// H-03: hierarchy `in` constraints in the head or conditions against
	// resource/principal can be a silent false in this parent-less BASIC graph.
	// Probe only forbids whose resolvable head parts target this request; if the
	// policy matches with the absent scope edge materialized, conservatively enforce
	// the restriction.
	if hasTargetedUnresolvableScopeForbid(set.policies, em, creq) {
		return auth.EffectForbid, diag, true
	}
	switch {
	case decision == cedar.Allow:
		return auth.EffectGrant, diag, false
	case len(diag.Reasons) > 0:
		return auth.EffectForbid, diag, false
	default:
		return auth.EffectAbstain, diag, false
	}
}

// logEffect records a scoped grant or forbid on the audit trail (docs/SECURITY-HARDENING.md): a
// positive authorization beyond RBAC and an explicit restriction are both
// security-relevant. Non-sensitive fields only (never a secret/email).
func (e *scopedEngine) logEffect(req auth.Request, effect string) {
	if e.log == nil {
		return
	}
	e.log.Info("cedar scoped "+effect,
		"tenant", string(req.Tenant),
		"principal_kind", string(req.Principal.Kind),
		"cred_id", string(req.Principal.CredID),
		"permission", string(req.Permission),
		"resource_kind", req.Resource.Kind,
		"resource_id", req.Resource.ID,
	)
}

// logDiagErrors surfaces per-policy EVALUATION errors (a rule that touched an
// attribute Cedar could not resolve) — Cedar skips such a rule silently, which for a
// security control is dangerous to do without a signal (an author must guard attribute
// access with `has`). An errored FORBID additionally fails CLOSED at the call site
// (hasErroredForbid, F-06); an errored permit stays dropped (it could only have
// widened), so a single unpopulated attribute never denies the whole tenant.
func (e *scopedEngine) logDiagErrors(diag cedar.Diagnostic) {
	if len(diag.Errors) > 0 && e.log != nil {
		e.log.Warn("cedar scoped-grant evaluation error (guard attribute access with `has`)",
			"errors", len(diag.Errors), "first", diag.Errors[0].PolicyID, "message", diag.Errors[0].Message)
	}
}

// hasErroredForbid reports whether any policy that ERRORED during Cedar evaluation is a
// forbid rule. Cedar silently drops an errored policy from the decision, so an errored
// forbid would otherwise fail OPEN (the restriction just disappears). A forbid we could
// not evaluate must be assumed to apply, so the caller fails CLOSED for that request. A
// permit that errors could only have widened, so dropping it stays safe and preserves
// availability — one broken attribute reference never denies the whole tenant, only the
// requests whose forbid genuinely could not be resolved (F-06).
func hasErroredForbid(policies *cedar.PolicySet, diag cedar.Diagnostic) bool {
	if policies == nil {
		return false
	}
	for _, de := range diag.Errors {
		if p := policies.Get(de.PolicyID); p != nil && p.Effect() == cedar.Forbid {
			return true
		}
	}
	return false
}

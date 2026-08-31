// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// scoped administration + custom roles + delegation with per-scope ceilings.
//
// This file is the DOMAIN half: the entities (custom roles, permission-groups, scoped
// grants), their store I/O, the projection of the structured rows to a per-tenant
// Cedar policy the engine enforces, and the delegation ceiling. The HTTP surface
// is scopedadmin_handlers.go. Adds NO second decision engine: it PROJECTS to the
// `cedar-managed` authoring surface that m.grants (grants.go) already compiles and
// enforces, unioned with the operator's free-form `cedar` policy.

// surfaceCedarManaged is the second Cedar authoring surface (beside surfaceCedar): the
// machine-generated projection of structured rows. It rides the same append-only
// revision store (governance_policy_revision), so each re-projection is a durable,
// auditable revision; the live engine evaluates the UNION of both surfaces.
const surfaceCedarManaged = "cedar-managed"

// Subject kinds for a scoped grant.
const (
	subjectUser  = "user"  // a specific operator account (principal parent User::"<id>")
	subjectRole  = "role"  // every holder of a built-in role (principal parent Role::"<role>")
	subjectGroup = "group" // every member of a directory group (principal parent Group::"<group-id>") — S256
)

// Scope-tree kinds (the axis a grant is bounded to).
const (
	scopeTenant     = "tenant"      // tenant-wide (no resource condition)
	scopeWorkspace  = "workspace"   // resource in Workspace::"<slug>"
	scopeAgentGroup = "agent_group" // resource in AgentGroup::"<slug>"
	// scopeFolder bounds a delegated-admin grant to a subtree of the Resource tree:
	// resource in Resource::"<folder-id>". The engine resolves the acted-on resource's
	// folder ancestors from its materialized Path (grants.go resourceTreeParents), so a grant
	// on a folder OR an ancestor authorizes the whole subtree below it (downward inheritance)
	// — the SAME mechanism the source-scope folder binding uses. The ref is a Resource
	// id (a folder is a Resource of Kind "folder", but any Resource node is a valid subtree
	// root), never a slug.
	scopeFolder = "folder"
)

// ceilingError is a delegation-ceiling / authority failure raised from inside a write
// path; the handler maps it to 403 (distinct from validationError → 400 and a store
// error → its own status).
type ceilingError string

func (e ceilingError) Error() string { return string(e) }

// asCeiling returns (msg, true) when err is a ceilingError.
func asCeiling(err error) (string, bool) {
	var c ceilingError
	if errors.As(err, &c) {
		return string(c), true
	}
	return "", false
}

// --- domain types (mirror the rows; the wire DTOs live in the handlers file) --------

// scopeSpec is a grant's scope: a tree node plus an optional resource-class filter.
type scopeSpec struct {
	Tree  string // tenant | workspace | agent_group | folder
	Ref   string // workspace/agent-group slug, or folder Resource id ("" for tenant)
	Class string // resource kind ("" = any scopeable kind)
}

// customRole is a tenant-wide reusable permission bundle: an optional BASE built-in
// role, plus direct perms, plus the permission-groups it includes, MINUS its excludes.
//
// why a base and a subtraction, and not just a list. Delegating "an editor, but
// not the provider keys" was only expressible by enumerating every permission an editor
// holds. An enumeration is a SNAPSHOT: the day a module declares a new permission, the
// built-in editor gains it and the hand-copied role does not, so the two silently drift
// and the copy quietly becomes a different role than the operator authored. A base is a
// LIVE reference — re-resolved at every projection — and the exclusion stays excluded.
// That is the difference between a role that means something and a role that meant
// something once.
type customRole struct {
	Name        string
	DisplayName string
	Description string
	Base        string   // "" | a BUILT-IN role name (never another custom role: no cycles)
	Perms       []string // "<kind>:<verb>"
	Groups      []string // permission-group names
	Excludes    []string // subtracted AFTER every union, so no include can re-add one
	CreatedBy   string
}

// permGroup is a tenant-wide reusable permission bundle.
type permGroup struct {
	Name        string
	DisplayName string
	Description string
	Perms       []string
	CreatedBy   string
}

// scopedGrant binds a subject to a role within a scope (the unit projects).
type scopedGrant struct {
	ID          model.ID
	SubjectKind string
	SubjectRef  string
	Role        string
	RoleCustom  bool
	Scope       scopeSpec
	CreatedBy   string
	Note        string
}

// --- store I/O ----------------------------------------------------------------------

func recToCustomRole(r model.Record) customRole {
	return customRole{
		Name:        r.String(colRBACName),
		DisplayName: r.String(colRBACDisplayName),
		Description: r.String(colRBACDescription),
		Base:        r.String(colRBACBaseRole),
		Perms:       jsonStrings(r.String(colRBACPerms)),
		Groups:      jsonStrings(r.String(colRBACGroups)),
		Excludes:    jsonStrings(r.String(colRBACExcludes)),
		CreatedBy:   r.String(colRBACCreatedBy),
	}
}

func recToPermGroup(r model.Record) permGroup {
	return permGroup{
		Name:        r.String(colRBACName),
		DisplayName: r.String(colRBACDisplayName),
		Description: r.String(colRBACDescription),
		Perms:       jsonStrings(r.String(colRBACPerms)),
		CreatedBy:   r.String(colRBACCreatedBy),
	}
}

func recToScopedGrant(r model.Record) scopedGrant {
	return scopedGrant{
		ID:          model.ID(r.String(model.ColID)),
		SubjectKind: r.String(colSGSubjectKind),
		SubjectRef:  r.String(colSGSubjectRef),
		Role:        r.String(colSGRole),
		RoleCustom:  r.Bool(colSGRoleCustom),
		Scope:       scopeSpec{Tree: r.String(colSGScopeTree), Ref: r.String(colSGScopeRef), Class: r.String(colSGScopeClass)},
		CreatedBy:   r.String(colSGCreatedBy),
		Note:        r.String(colSGNote),
	}
}

// jsonStrings decodes a JSON string array stored in a KindJSON column (the column
// holds the marshaled text — read it back as a string and unmarshal, mirroring
// roster.go's sanitizeAttrs convention). An empty/invalid value yields nil.
func jsonStrings(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// jsonStringsCol marshals a string slice for a KindJSON column (a non-empty JSON array,
// or "[]" so the column is never NULL where it is required).
func jsonStringsCol(in []string) string {
	if len(in) == 0 {
		return "[]"
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func loadCustomRoles(ctx context.Context, sc store.Scope) (map[string]customRole, error) {
	repo, err := sc.Ext(customRoleKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make(map[string]customRole, len(recs))
	for _, r := range recs {
		cr := recToCustomRole(r)
		out[cr.Name] = cr
	}
	return out, nil
}

func loadPermGroups(ctx context.Context, sc store.Scope) (map[string]permGroup, error) {
	repo, err := sc.Ext(permGroupKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make(map[string]permGroup, len(recs))
	for _, r := range recs {
		g := recToPermGroup(r)
		out[g.Name] = g
	}
	return out, nil
}

func loadScopedGrants(ctx context.Context, sc store.Scope) ([]scopedGrant, error) {
	repo, err := sc.Ext(scopedGrantKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]scopedGrant, 0, len(recs))
	for _, r := range recs {
		out = append(out, recToScopedGrant(r))
	}
	return out, nil
}

// --- the permission catalog + effective perms ---------------------------------------

// splitPerm parses "<kind>:<verb>" into its parts at the LAST colon, so a module
// permission splits as ("<ns>:<res>", verb). It delegates to auth.SplitPermission — the
// one split every catalog question must use (see the false-positive note there).
func splitPerm(p string) (kind, verb string, ok bool) {
	return auth.SplitPermission(auth.Permission(p))
}

// isVerb reports whether v is a valid permission verb (read|write|admin).
func isVerb(v string) bool { return auth.IsVerb(v) }

// isCatalogPerm reports whether p is scope-grantable: an tree kind with a valid verb,
// or a module permission a mounted module registered. See
// auth.IsGrantablePermission for why a module permission is matched WHOLE rather than by
// its kind.
func isCatalogPerm(p string) bool {
	return auth.IsGrantablePermission(auth.Permission(p))
}

// effectivePermsOf resolves a role reference (built-in or custom) to its permission SET,
// filtered to the scopeable catalog and intersected with an optional resource-class. A
// custom role expands the permission-groups it includes; an unknown custom role grants
// nothing (deny-closed).
func effectivePermsOf(roleName string, custom bool, class string, roles map[string]customRole, groups map[string]permGroup) map[string]bool {
	raw := map[string]bool{}
	if custom {
		cr, ok := roles[roleName]
		if !ok {
			return raw
		}
		// The BASE is resolved LIVE from the built-in role, so a role defined as "editor
		// minus X" keeps meaning that as the catalog grows. An unknown/blank base
		// contributes nothing (RoleResourcePerms is empty for a non-role) — deny-closed.
		for _, p := range auth.RoleResourcePerms(cr.Base) {
			raw[string(p)] = true
		}
		for _, p := range cr.Perms {
			raw[p] = true
		}
		for _, gname := range cr.Groups {
			if g, ok := groups[gname]; ok {
				for _, p := range g.Perms {
					raw[p] = true
				}
			}
		}
		// SUBTRACT LAST, after every union. Order is the whole guarantee: excluding
		// before a group union would let an included group put the permission back, and
		// the operator would read the role as a restriction that is not one.
		for _, p := range cr.Excludes {
			delete(raw, p)
		}
	} else {
		for _, p := range auth.RoleResourcePerms(roleName) {
			raw[string(p)] = true
		}
	}
	out := make(map[string]bool, len(raw))
	for p := range raw {
		kind, _, ok := splitPerm(p)
		if !ok || !isCatalogPerm(p) {
			continue
		}
		if class != "" && kind != class {
			continue
		}
		out[p] = true
	}
	return out
}

// effectivePermsOfGrant is effectivePermsOf for a grant (its role + scope-class).
func effectivePermsOfGrant(g scopedGrant, roles map[string]customRole, groups map[string]permGroup) map[string]bool {
	return effectivePermsOf(g.Role, g.RoleCustom, g.Scope.Class, roles, groups)
}

// isAdminCapableGrant reports whether a grant lets its holder ADMINISTER (and thus
// sub-delegate) within its scope: a built-in role of rank >= admin, or a custom role
// that confers any `<kind>:admin` permission.
func isAdminCapableGrant(g scopedGrant, roles map[string]customRole, groups map[string]permGroup) bool {
	if !g.RoleCustom {
		return auth.RoleRank(g.Role) >= auth.RoleRank(auth.RoleAdmin)
	}
	for p := range effectivePermsOfGrant(g, roles, groups) {
		if _, verb, ok := splitPerm(p); ok && verb == auth.VerbAdmin {
			return true
		}
	}
	return false
}

// --- the delegation ceiling ---------------------------------------------------------

// delegationDomain is one (scope, grantable-perms) pair an actor may delegate within.
type delegationDomain struct {
	scope scopeSpec
	perms map[string]bool
}

// grantAppliesToActor reports whether grant g would match the acting principal (its
// subject names this user, a built-in role the actor holds in tenant, or a directory
// group the actor is a gated member of). It mirrors EXACTLY what buildPrincipalEntity
// makes the Cedar principal `in`, so the delegation ceiling sees the same authority the
// engine would enforce.
func grantAppliesToActor(g scopedGrant, actor auth.Principal, tenant model.TenantID) bool {
	switch g.SubjectKind {
	case subjectUser:
		return !actor.UserID.IsZero() && g.SubjectRef == actor.UserID.String()
	case subjectRole:
		r, ok := actor.RoleIn(tenant)
		return ok && r == g.SubjectRef
	case subjectGroup:
		for _, gid := range actor.GroupsIn(tenant) {
			if gid == g.SubjectRef {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// actorDomains computes the actor's delegation domains: its tenant-wide RBAC authority
// (only if admin/owner) plus each admin-capable scoped grant that applies to it.
func actorDomains(actor auth.Principal, tenant model.TenantID, allGrants []scopedGrant, roles map[string]customRole, groups map[string]permGroup) []delegationDomain {
	var domains []delegationDomain
	if r, ok := actor.RoleIn(tenant); ok && auth.RoleRank(r) >= auth.RoleRank(auth.RoleAdmin) {
		perms := map[string]bool{}
		for _, p := range auth.RoleResourcePerms(r) {
			perms[string(p)] = true
		}
		domains = append(domains, delegationDomain{scope: scopeSpec{Tree: scopeTenant}, perms: perms})
	}
	for _, g := range allGrants {
		if !grantAppliesToActor(g, actor, tenant) || !isAdminCapableGrant(g, roles, groups) {
			continue
		}
		domains = append(domains, delegationDomain{scope: g.Scope, perms: effectivePermsOfGrant(g, roles, groups)})
	}
	return domains
}

// canDelegate enforces the per-scope ceiling for creating grant g by actor (§3 of the
// contract): the superadmin is the root; otherwise g must fall within SOME delegation
// domain of the actor — its scope contained AND its permission set a subset. Reads the
// agent-group→workspace mapping from sc for scope containment.
func canDelegate(ctx context.Context, sc store.Scope, actor auth.Principal, tenant model.TenantID, g scopedGrant, allGrants []scopedGrant, roles map[string]customRole, groups map[string]permGroup) error {
	if actor.Superadmin {
		return nil
	}
	domains := actorDomains(actor, tenant, allGrants, roles, groups)
	if len(domains) == 0 {
		return ceilingError("no delegation authority in this tenant (need tenant admin/owner or an admin-capable scoped grant)")
	}
	gperms := effectivePermsOfGrant(g, roles, groups)
	for _, d := range domains {
		contained, err := scopeContains(ctx, sc, d.scope, g.Scope)
		if err != nil {
			return err
		}
		if contained && permSubset(gperms, d.perms) {
			return nil
		}
	}
	return ceilingError("cannot delegate above your own scope or role")
}

// permSubset reports whether every permission in a is present in b.
func permSubset(a, b map[string]bool) bool {
	for p := range a {
		if !b[p] {
			return false
		}
	}
	return true
}

// scopeContains reports whether outer contains inner along the axis: the class
// filter must be equal-or-broader, and the tree node must enclose the inner node
// (tenant ⊇ everything; a workspace ⊇ itself, the agent-groups whose workspace it is, and
// the folders that live in it; an agent-group ⊇ only itself; a folder ⊇ itself and its
// descendant folders). It never lets a narrower scope enclose a broader one (no upward
// escalation).
func scopeContains(ctx context.Context, sc store.Scope, outer, inner scopeSpec) (bool, error) {
	if outer.Class != "" && outer.Class != inner.Class {
		return false, nil
	}
	switch outer.Tree {
	case scopeTenant:
		return true, nil
	case scopeWorkspace:
		switch inner.Tree {
		case scopeWorkspace:
			return outer.Ref == inner.Ref, nil
		case scopeAgentGroup:
			ws, err := agentGroupWorkspaceSlug(ctx, sc, inner.Ref)
			if err != nil {
				return false, err
			}
			return ws != "" && ws == outer.Ref, nil
		default:
			// A workspace admin deliberately CANNOT sub-delegate a folder grant (review):
			// a Resource's workspace_id is decoupled from its tree position (the store lets a
			// child carry a different workspace than its parent), so a folder anchored in
			// workspace W can enclose descendants in OTHER workspaces — and the folder permit
			// (`resource in Resource::"<id>"`) carries no workspace predicate. Allowing a
			// W-scoped admin to delegate such a grant would let it reach resources outside W
			// (a cross-workspace confinement escape). Only a tenant admin (tenant ⊇ folder) or a
			// folder admin (folder ⊇ descendant, tree-based authority) may delegate folders.
			return false, nil
		}
	case scopeAgentGroup:
		return inner.Tree == scopeAgentGroup && outer.Ref == inner.Ref, nil
	case scopeFolder:
		// A folder admin may sub-delegate only WITHIN its subtree: the inner grant must also
		// be a folder equal to or a descendant of the outer folder. It can never reach up to a
		// workspace/agent-group/tenant scope (no upward escalation).
		if inner.Tree != scopeFolder {
			return false, nil
		}
		return folderContains(ctx, sc, outer.Ref, inner.Ref)
	default:
		return false, nil
	}
}

// findResourceByID returns the tenant's Resource (a folder-scope anchor) by id, and whether
// it exists. A blank id or a missing row is (zero, false) — deny-closed for validation and
// containment (a folder grant never rides a dangling anchor).
func findResourceByID(ctx context.Context, sc store.Scope, id string) (model.Resource, bool, error) {
	if strings.TrimSpace(id) == "" {
		return model.Resource{}, false, nil
	}
	res, err := sc.Resources().Get(ctx, model.ID(id))
	if errors.Is(err, store.ErrNotFound) {
		return model.Resource{}, false, nil
	}
	if err != nil {
		return model.Resource{}, false, err
	}
	return res, true, nil
}

// folderContains reports whether folder outerID contains folder innerID along the resource
// tree: inner is outer itself, or a descendant of it by the materialized Path
// ("/<root>/…/<self>"). It compares the anchors' LIVE Paths (outer's Path a proper,
// segment-boundary prefix of inner's), so a move is reflected immediately. A missing folder
// on either side, or an empty Path, is not-contained (deny-closed — no delegation across a
// dangling anchor).
func folderContains(ctx context.Context, sc store.Scope, outerID, innerID string) (bool, error) {
	if outerID == "" || innerID == "" {
		return false, nil
	}
	if outerID == innerID {
		return true, nil // the same folder is trivially within its own subtree
	}
	outer, ok, err := findResourceByID(ctx, sc, outerID)
	if err != nil || !ok {
		return false, err
	}
	inner, ok, err := findResourceByID(ctx, sc, innerID)
	if err != nil || !ok {
		return false, err
	}
	if outer.Path == "" || inner.Path == "" {
		return false, nil
	}
	// The trailing "/" makes the prefix test respect segment boundaries, so folder "/a/b"
	// contains "/a/b/c" but never a sibling "/a/bc".
	return strings.HasPrefix(inner.Path, outer.Path+"/"), nil
}

// --- scope reference resolution (validation + containment) --------------------------

// findWorkspaceBySlug returns the tenant's workspace with slug, and whether it exists.
func findWorkspaceBySlug(ctx context.Context, sc store.Scope, slug string) (model.Workspace, bool, error) {
	ws, _, err := sc.Workspaces().List(ctx, model.Query{
		Filters: []model.Filter{eq("slug", slug)}, Limit: 1,
	})
	if err != nil {
		return model.Workspace{}, false, err
	}
	if len(ws) == 0 {
		return model.Workspace{}, false, nil
	}
	return ws[0], true, nil
}

// findAgentGroupBySlug returns the tenant's agent-group with slug, and whether it exists.
func findAgentGroupBySlug(ctx context.Context, sc store.Scope, slug string) (model.AgentGroup, bool, error) {
	gs, _, err := sc.AgentGroups().List(ctx, model.Query{
		Filters: []model.Filter{eq("slug", slug)}, Limit: 1,
	})
	if err != nil {
		return model.AgentGroup{}, false, err
	}
	if len(gs) == 0 {
		return model.AgentGroup{}, false, nil
	}
	return gs[0], true, nil
}

// agentGroupWorkspaceSlug resolves an agent-group slug to its workspace slug (the
// reserved default slug when the group is workspace-unscoped). An unknown group yields
// "" (matches no workspace — deny-closed for containment).
func agentGroupWorkspaceSlug(ctx context.Context, sc store.Scope, groupSlug string) (string, error) {
	g, ok, err := findAgentGroupBySlug(ctx, sc, groupSlug)
	if err != nil || !ok {
		return "", err
	}
	if g.WorkspaceID.IsZero() {
		return model.DefaultWorkspaceSlug, nil
	}
	ws, err := sc.Workspaces().Get(ctx, g.WorkspaceID)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ws.Slug, nil
}

// --- projection to Cedar ------------------------------------------------------------

// projectManagedCedar generates the `cedar-managed` policy text from a tenant's
// structured rows: one permit per grant (subject in scope → its role's actions), plus a
// tenant-wide rbac:read/admin permit for each admin-capable subject (so a scoped-admin
// can reach the delegation API and sub-delegate within its ceiling). Deterministic
// (sorted) so an unchanged rowset re-projects to identical bytes.
func projectManagedCedar(grants []scopedGrant, roles map[string]customRole, groups map[string]permGroup) string {
	sorted := append([]scopedGrant(nil), grants...)
	sort.Slice(sorted, func(i, j int) bool { return scopedGrantKeyOf(sorted[i]) < scopedGrantKeyOf(sorted[j]) })

	var b strings.Builder
	// subject expr → the UNION of delegation actions its admin-capable grants allow, plus a
	// stable emission order (first appearance in the sorted grant list).
	delegators := map[string]map[string]bool{}
	var delegOrder []string
	for _, g := range sorted {
		subj := cedarSubjectExpr(g)
		if subj == "" {
			continue
		}
		perms := effectivePermsOfGrant(g, roles, groups)
		if len(perms) == 0 {
			continue
		}
		writePermit(&b, subj, sortedPerms(perms), cedarScopeWhen(g.Scope))
		// The tenant-wide delegation capability (reach the rbac API to sub-delegate) is
		// emitted for a USER or a GROUP subject (U7): an admin-capable grant to a
		// directory group IS a delegation DIRECTED at that group's members — the IdP-group
		// analog of a direct user delegation, so "delegated admin directed by IdP groups"
		// reaches the console instead of dead-ending. It is safe because it is doubly gated:
		//   - deny-closed admission (UNCHANGED, ratified): the `Group::"<id>"` principal
		//     parent is materialized ONLY for a GATED direct member — authenticator.loadGrants
		//     requires a direct tenant membership before carrying any group subject — so this
		//     permit never reaches a user who is merely in the group but holds no membership
		//     in the tenant. A group ELEVATES an existing member, it never ADMITS a stranger.
		//   - bounded ceiling: reaching the rbac API is not authority to grant anything.
		//     canDelegate/actorDomains already treat a group member as holding the group
		//     grant's authority (grantAppliesToActor case subjectGroup), so any sub-grant the
		//     member authors is clamped to THAT grant's scope and permission subset.
		// A ROLE subject ("every editor in W") deliberately stays access-only: auto-opening
		// the delegation console to every holder of a built-in role is over-broad and
		// redundant with the tenant-admin authority that role already carries.
		//
		// THE SUBTRACTION BINDS HERE TOO. These two permissions are not drawn from
		// the role's permission set; they are synthesized from the fact that the grant is
		// admin-capable. So a role authored as "compliance admin MINUS
		// governance:rbac:admin" would, without this filter, receive rbac:admin anyway
		// through this permit — the exclusion silently ignored on a path the operator
		// cannot see. A subtraction that does not bite everywhere is worse than no
		// subtraction, because the operator believes they capped something they did not.
		if (g.SubjectKind == subjectUser || g.SubjectKind == subjectGroup) &&
			isAdminCapableGrant(g, roles, groups) {
			if _, seen := delegators[subj]; !seen {
				delegators[subj] = map[string]bool{}
				delegOrder = append(delegOrder, subj)
			}
			for _, a := range delegationActions(g, roles) {
				delegators[subj][a] = true
			}
		}
	}
	// The delegation permits are emitted AFTER the access permits, one per subject, over
	// the UNION of what each of that subject's admin-capable grants allows.
	//
	// The union is not a detail. Before the subtraction existed every delegation permit was
	// byte-identical, so emitting the first and skipping the rest was lossless. It stopped
	// being lossless the moment a role could subtract from it: a subject holding a CAPPED
	// role ("may not re-delegate") and a separate UNCAPPED admin grant would have had the
	// capped one win purely because its role name sorts first, silently removing authority
	// the other grant legitimately confers — a behavior that would change on a RENAME. It
	// also contradicted the rule this feature is built on: an exclusion narrows the ROLE it
	// is written in, never the subject. Grants are additive; so is this.
	for _, subj := range delegOrder {
		// Emitted in the CANONICAL order (read, admin), not sorted: for a tenant that uses
		// no exclusions the bytes are then identical to what this projection produced
		// before subtraction existed, so the next RBAC write does not append a revision
		// whose only change is that two actions swapped places.
		var acts []string
		for _, a := range []string{string(permRBACRead), string(permRBACAdmin)} {
			if delegators[subj][a] {
				acts = append(acts, a)
			}
		}
		if len(acts) > 0 {
			writePermit(&b, subj, acts, "")
		}
	}
	return b.String()
}

// delegationActions returns the actions the tenant-wide delegation permit may carry for
// grant g: rbac read + admin, MINUS anything the grant's custom role explicitly excludes.
//
// It exists because that permit is SYNTHESIZED, not projected from the role's permission
// set — so it is the one place a declared subtraction could be silently dropped. Excluding
// rbac:admin from an otherwise admin-capable role is a coherent thing to author ("may
// administer this surface, may NOT re-delegate it"), and it must mean what it says.
//
// Returning an empty slice means the operator subtracted both, and no delegation permit is
// emitted at all: the grant still confers its own permissions, it just cannot reach the
// delegation API. A BUILT-IN role grant has no excludes and is unaffected.
func delegationActions(g scopedGrant, roles map[string]customRole) []string {
	acts := []string{string(permRBACRead), string(permRBACAdmin)}
	if !g.RoleCustom {
		return acts
	}
	cr, ok := roles[g.Role]
	if !ok || len(cr.Excludes) == 0 {
		return acts
	}
	excluded := make(map[string]bool, len(cr.Excludes))
	for _, p := range cr.Excludes {
		excluded[p] = true
	}
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		if !excluded[a] {
			out = append(out, a)
		}
	}
	return out
}

// scopedGrantKeyOf is a stable ordering key for deterministic projection.
func scopedGrantKeyOf(g scopedGrant) string {
	return strings.Join([]string{g.SubjectKind, g.SubjectRef, g.Role, g.Scope.Tree, g.Scope.Ref, g.Scope.Class}, "\x00")
}

// cedarSubjectExpr renders a grant's subject as a Cedar `principal in …` operand, or ""
// for an unrenderable subject (deny-closed: it is skipped). A group subject is keyed by
// the directory group's id (the only stable, tenant-unique handle — displayName is not
// unique and externalId is optional), matching the `Group::"<id>"` parents
// buildPrincipalEntity puts on the principal; like a user subject, group existence is
// not (and cannot be) validated cross-partition, so a grant to a non-group id is inert.
func cedarSubjectExpr(g scopedGrant) string {
	switch g.SubjectKind {
	case subjectUser:
		if g.SubjectRef == "" {
			return ""
		}
		return "User::" + cedarStr(g.SubjectRef)
	case subjectRole:
		if !auth.IsRole(g.SubjectRef) {
			return ""
		}
		return "Role::" + cedarStr(g.SubjectRef)
	case subjectGroup:
		if g.SubjectRef == "" {
			return ""
		}
		return "Group::" + cedarStr(g.SubjectRef)
	default:
		return ""
	}
}

// cedarScopeWhen renders a grant scope's tree as a Cedar `when {…}` clause (empty for a
// tenant-wide scope; the resource-class is already encoded by the action list). A folder
// scope rides `resource in Resource::"<id>"`: the engine resolves the acted-on resource's
// folder ancestors from its materialized Path (grants.go resourceTreeParents), so the permit
// covers the whole subtree below the anchor (downward inheritance).
func cedarScopeWhen(s scopeSpec) string {
	switch s.Tree {
	case scopeWorkspace:
		return "when { resource in Workspace::" + cedarStr(s.Ref) + " }"
	case scopeAgentGroup:
		return "when { resource in AgentGroup::" + cedarStr(s.Ref) + " }"
	case scopeFolder:
		return "when { resource in Resource::" + cedarStr(s.Ref) + " }"
	default:
		return ""
	}
}

// writePermit appends one `permit(principal in <subj>, action in [..], resource) [when{}];`.
func writePermit(b *strings.Builder, subj string, perms []string, when string) {
	b.WriteString("permit(principal in ")
	b.WriteString(subj)
	b.WriteString(", action in [")
	for i, p := range perms {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("Action::")
		b.WriteString(cedarStr(p))
	}
	b.WriteString("], resource)")
	if when != "" {
		b.WriteString(" ")
		b.WriteString(when)
	}
	b.WriteString(";\n")
}

// sortedPerms returns a permission set as a sorted slice (deterministic projection).
func sortedPerms(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// cedarStr renders s as a Cedar double-quoted string literal, escaping backslash and
// quote so an operator-chosen slug/name can never break out of the literal.
func cedarStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// --- projection persistence + live reload -------------------------------------------

// managedProjectionState is the complete structured input to `cedar-managed`.
// Handlers load it once inside their Mutate transaction, replace/remove/add the row in
// memory, and plan from that prospective state before performing any write.
type managedProjectionState struct {
	roles  map[string]customRole
	groups map[string]permGroup
	grants []scopedGrant
}

func loadManagedProjectionState(ctx context.Context, sc store.Scope) (managedProjectionState, error) {
	roles, err := loadCustomRoles(ctx, sc)
	if err != nil {
		return managedProjectionState{}, err
	}
	groups, err := loadPermGroups(ctx, sc)
	if err != nil {
		return managedProjectionState{}, err
	}
	grants, err := loadScopedGrants(ctx, sc)
	if err != nil {
		return managedProjectionState{}, err
	}
	return managedProjectionState{roles: roles, groups: groups, grants: grants}, nil
}

func cloneCustomRoles(in map[string]customRole) map[string]customRole {
	out := make(map[string]customRole, len(in))
	for name, role := range in {
		out[name] = role
	}
	return out
}

func clonePermGroups(in map[string]permGroup) map[string]permGroup {
	out := make(map[string]permGroup, len(in))
	for name, group := range in {
		out[name] = group
	}
	return out
}

// managedProjectionPlan is fully prepared before the epoch CAS. Besides the exact
// projection bytes, it captures the durable freshness anchor that the successful live
// reload must restore; local authority uses database time, while a signed DDIL anchor is
// preserved verbatim.
type managedProjectionPlan struct {
	content          string
	changed          bool
	freshness        FreshnessRecord
	refreshFreshness bool
}

// planManagedProjection projects prospective structured state, compares its exact bytes
// with the selected `cedar-managed` revision, and compiles the exact live union with the
// currently selected authored and adopted surfaces. It runs inside the caller's Mutate
// scope and opens no View/Mutate of its own. A broken union fails before the epoch CAS or
// any base-row write, so the previously committed authority remains intact.
func planManagedProjection(ctx context.Context, sc store.Scope, state managedProjectionState) (managedProjectionPlan, error) {
	managed := projectManagedCedar(state.grants, state.roles, state.groups)
	current, found, err := latestActiveContent(ctx, sc, surfaceCedarManaged)
	if err != nil {
		return managedProjectionPlan{}, err
	}
	if !found {
		current = ""
	}
	authored, _, err := latestActiveContent(ctx, sc, surfaceCedar)
	if err != nil {
		return managedProjectionPlan{}, err
	}
	adopted, adoptedFound, err := latestActiveContent(ctx, sc, surfaceCedarDDIL)
	if err != nil {
		return managedProjectionPlan{}, err
	}
	union := mergeCedarSources(mergeCedarSources(authored, managed), adopted)
	if _, compileErr := compileGrantSet(union); compileErr != nil {
		return managedProjectionPlan{}, fmt.Errorf("scopedadmin: prospective cedar union failed to compile: %w", compileErr)
	}
	plan := managedProjectionPlan{content: managed, changed: current != managed}
	if !plan.changed {
		return plan, nil
	}
	plan.freshness, plan.refreshFreshness, err = planManagedProjectionFreshness(ctx, sc, adopted, adoptedFound)
	if err != nil {
		return managedProjectionPlan{}, err
	}
	return plan, nil
}

// planManagedProjectionFreshness validates that the selected adopted surface and its
// signed replay anchors are either all present and coherent or all absent. A local write
// then samples the database clock before the CAS and refreshes only refreshed_at. Signed
// DDIL authority owns its clock and anchors, so that branch needs no local clock at all.
func planManagedProjectionFreshness(
	ctx context.Context,
	sc store.Scope,
	adopted string,
	adoptedFound bool,
) (FreshnessRecord, bool, error) {
	current, freshnessFound, err := readPolicyFreshness(ctx, sc)
	if err != nil {
		return FreshnessRecord{}, false, err
	}
	hasRevisionAnchor := current.AdoptedRevision != ""
	hasCreatedAnchor := !current.AdoptedCreatedAt.IsZero()
	signed := adoptedFound || hasRevisionAnchor || hasCreatedAnchor
	if signed {
		if !freshnessFound || !adoptedFound || !hasRevisionAnchor || !hasCreatedAnchor {
			return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: inconsistent DDIL durable adoption state: adopted policy and replay anchors must be present together")
		}
		if current.RefreshedAt.IsZero() || !current.RefreshedAt.Equal(current.AdoptedCreatedAt) {
			return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: inconsistent DDIL durable adoption state: signed freshness clock does not equal adopted created_at")
		}
		if policyContentRevision([]byte(adopted)) != current.AdoptedRevision {
			return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: inconsistent DDIL durable adoption state: active adopted policy does not match its revision anchor")
		}
		return current, false, nil
	}
	if freshnessFound && current.MaxStaleness > 0 {
		return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: inconsistent DDIL durable adoption state: signed staleness bound has no adopted policy anchors")
	}

	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: scope lacks authoritative transaction clock")
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: read authoritative transaction clock: %w", err)
	}
	if now.IsZero() {
		return FreshnessRecord{}, false, fmt.Errorf("scopedadmin: authoritative transaction clock returned zero")
	}
	current.RefreshedAt = now.Time()
	return current, true, nil
}

// appendManagedProjection persists a plan that the caller has already classified as
// changed and whose epoch CAS has already succeeded. It re-reads no policy/structured
// authority (appendRevision still allocates its immutable sequence number) and must stay
// after the structured-row write and before the audit append in the same transaction.
func appendManagedProjection(ctx context.Context, sc store.Scope, plan managedProjectionPlan, author string) error {
	if !plan.changed {
		return nil
	}
	// NOTE: like the publish path, the monotonic (surface, revision) counter assumes
	// the single-writer SQLite default; under Postgres a concurrent reprojection loses the
	// unique-index race (store.ErrConflict). The epoch CAS serializes migrated Cedar writers;
	// a conflict still rolls back the epoch and structured row atomically.
	_, _, err := appendRevision(ctx, sc, surfaceCedarManaged, plan.content, author, true, true, "")
	return err
}

// persistManagedProjectionFreshness follows the epoch/domain/revision writes and precedes
// audit in the same transaction. A signed DDIL clock is never renewed by a local writer.
func persistManagedProjectionFreshness(ctx context.Context, sc store.Scope, plan managedProjectionPlan) error {
	if !plan.changed || !plan.refreshFreshness {
		return nil
	}
	return upsertPolicyRefreshedAt(ctx, sc, plan.freshness.RefreshedAt)
}

// ensureGrantsWithinCeiling re-checks that every ACTIVE grant referencing one of
// affectedRoles still falls within the editing principal's delegation ceiling under the
// CURRENT (already-updated) definitions. Editing a role/group AFTER it has been assigned
// is itself a delegation: without this guard a tenant-admin (read+write) could widen an
// assigned custom role to confer the owner-tier resource `admin` verb it could never grant
// directly. Superadmin is exempt; only grants referencing the edited definition are
// re-checked, so an unrelated edit is never blocked by a pre-existing higher grant.
func ensureGrantsWithinCeiling(ctx context.Context, sc store.Scope, actor auth.Principal, tenant model.TenantID, roles map[string]customRole, groups map[string]permGroup, grants []scopedGrant, affectedRoles map[string]bool) error {
	if actor.Superadmin || len(affectedRoles) == 0 {
		return nil
	}
	for _, g := range grants {
		if !g.RoleCustom || !affectedRoles[g.Role] {
			continue
		}
		if err := canDelegate(ctx, sc, actor, tenant, g, grants, roles, groups); err != nil {
			return err
		}
	}
	return nil
}

// rolesIncludingGroup returns the names of custom roles that include the named
// permission-group (the roles whose effective perms change when the group is edited).
func rolesIncludingGroup(roles map[string]customRole, group string) map[string]bool {
	out := map[string]bool{}
	for _, role := range roles {
		for _, gn := range role.Groups {
			if gn == group {
				out[role.Name] = true
			}
		}
	}
	return out
}

// errBoundedPolicyFreshnessUnavailable marks the fail-closed state where a bounded Cedar
// selection was compiled but no durable freshness anchor was available. Reload installs
// the one state with available=false, then returns this error: both Scoped and the
// restrict-view must refuse to decide from an authority snapshot that cannot prove fresh.
var errBoundedPolicyFreshnessUnavailable = errors.New("governance: bounded Cedar policy has no durable freshness anchor")

// cedarDurableSnapshot is every durable fact consumed by the live scoped-grant engine.
// It is read in ONE View: content and revisions identify the three Cedar surfaces;
// generation fences concurrent authority writers; freshness carries the local/signed
// trust clock. No caller is allowed to reconstruct one part later from another View.
type cedarDurableSnapshot struct {
	state            scopedTenantState
	authored         string
	authoredRevision revisionDTO
	managed          string
	adopted          string
	combined         string
}

// cedarDurableSnapshotObservation preserves the exact epoch witness independently of
// whether the rest of a same-View Cedar snapshot could be read. `complete` is explicit:
// a complete, selected-empty union is valid authority, whereas an error after epoch
// leaves only a generation high-water fence and must never be treated as that empty
// policy.
type cedarDurableSnapshotObservation struct {
	snapshot   cedarDurableSnapshot
	generation store.AuthorizationFactRef
	complete   bool
}

func readCedarDurableSnapshot(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	deploymentBound time.Duration,
) (cedarDurableSnapshot, error) {
	observed, err := observeCedarDurableSnapshot(ctx, sc, tenant, deploymentBound)
	if err != nil {
		return cedarDurableSnapshot{}, err
	}
	if !observed.complete {
		return cedarDurableSnapshot{}, errors.New("governance: Cedar durable snapshot completed without identity")
	}
	return observed.snapshot, nil
}

// observeCedarDurableSnapshot reads one complete Cedar authority snapshot when
// possible, retaining the exact epoch as soon as it is proven. Callers that need a
// usable identity use readCedarDurableSnapshot; reload uses this observation directly
// so a later revision/freshness error can still fence an older live evaluator.
func observeCedarDurableSnapshot(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	deploymentBound time.Duration,
) (cedarDurableSnapshotObservation, error) {
	var observed cedarDurableSnapshotObservation
	epochs, ok := sc.(store.AuthorizationEpochReader)
	if !ok {
		return observed, policyAuthorizationEpochUnavailable("scope lacks authorization epoch reader for Cedar reload", nil)
	}
	generation, err := epochs.ReadAuthorizationEpoch(ctx)
	if err != nil {
		return observed, policyAuthorizationEpochUnavailable("read authorization epoch for Cedar reload", err)
	}
	if !validPolicyAuthorizationEpochFact(tenant, generation) {
		return observed, policyAuthorizationEpochUnavailable("Cedar reload epoch is not exact for the tenant", nil)
	}
	observed.generation = generation

	authoredRevision, authoredFound, err := latestActiveRevision(ctx, sc, surfaceCedar)
	if err != nil {
		return observed, err
	}
	free := ""
	authored := int64(0)
	if authoredFound {
		free = authoredRevision.Content
		authored = authoredRevision.Revision
	}
	managed, managedRevision, _, err := latestActiveSelection(ctx, sc, surfaceCedarManaged)
	if err != nil {
		return observed, err
	}
	adopted, adoptedRevision, adoptedFound, err := latestActiveSelection(ctx, sc, surfaceCedarDDIL)
	if err != nil {
		return observed, err
	}
	freshness, freshnessFound, err := readPolicyFreshness(ctx, sc)
	if err != nil {
		return observed, err
	}

	// Signed DDIL authority is an all-or-nothing tuple. Do this at reload too: a
	// raw/decorated store must not make a stale/missing signed anchor look like a
	// harmless local row merely because the content itself still parses.
	hasRevisionAnchor := freshness.AdoptedRevision != ""
	hasCreatedAnchor := !freshness.AdoptedCreatedAt.IsZero()
	signed := adoptedFound || hasRevisionAnchor || hasCreatedAnchor
	if signed {
		if !freshnessFound || !adoptedFound || !hasRevisionAnchor || !hasCreatedAnchor ||
			freshness.RefreshedAt.IsZero() || !freshness.RefreshedAt.Equal(freshness.AdoptedCreatedAt) ||
			policyContentRevision([]byte(adopted)) != freshness.AdoptedRevision {
			return observed, errors.New("governance: inconsistent signed DDIL freshness/selection snapshot")
		}
	} else if freshnessFound && freshness.MaxStaleness > 0 {
		return observed, errors.New("governance: unsigned Cedar freshness carries a tenant staleness bound")
	}

	combined := mergeCedarSources(mergeCedarSources(free, managed), adopted)
	selection := activationID{authored: authored, managed: managedRevision, adopted: adoptedRevision}
	bound := deploymentBound
	if freshness.MaxStaleness > 0 {
		bound = freshness.MaxStaleness
	}
	needsFreshness := selection != (activationID{}) && bound > 0
	observed.snapshot = cedarDurableSnapshot{
		authored: free, authoredRevision: authoredRevision, managed: managed, adopted: adopted, combined: combined,
		state: scopedTenantState{
			selection:      selection,
			generation:     generation,
			authoredDigest: contentDigest(free),
			managedDigest:  contentDigest(managed),
			adoptedDigest:  contentDigest(adopted),
			unionDigest:    contentDigest(combined),
			freshness:      freshness,
			available:      true,
			freshnessValid: !needsFreshness || (freshnessFound && !freshness.RefreshedAt.IsZero()),
		},
	}
	observed.complete = true
	return observed, nil
}

// reloadTenantGrants recompiles a tenant's LIVE scoped-grant set from the exact UNION
// of authored, managed and adopted Cedar surfaces. It captures that union's selection,
// durable authorization epoch and complete freshness state in one View, then installs
// one immutable state under the engine mutex. A late G reload cannot replace G+1.
//
// It opens its OWN View (the single-connection SQLite caveat in boot.go applies: never
// call it while another transaction is open on the same connection).
func (m *Module) reloadTenantGrants(ctx context.Context, tenant model.TenantID) error {
	if m.grants == nil {
		return errors.New("governance: reload tenant grants requires scoped grant evaluator")
	}
	if m.data == nil {
		// A handler may have committed through its request-scoped data handle while
		// this module was miswired without UseData. That is not a harmless skipped
		// reload: record an unavailable runtime state so both authorization seams
		// refuse rather than abstaining/allowing from an absent cache.
		before, beforeLoaded := m.grants.tenantState(tenant)
		m.grants.markUnavailableIfStillSame(tenant, before, beforeLoaded)
		return errors.New("governance: reload tenant grants requires module data")
	}
	before, beforeLoaded := m.grants.tenantState(tenant)
	var observed cedarDurableSnapshotObservation
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var readErr error
		observed, readErr = observeCedarDurableSnapshot(ctx, sc, tenant, m.grants.maxStaleness)
		return readErr
	}); err != nil {
		m.grants.markUnavailableForObservedGenerationFailure(tenant, before, beforeLoaded, observed.generation)
		return err
	}
	if !observed.complete {
		m.grants.markUnavailableForObservedGenerationFailure(tenant, before, beforeLoaded, observed.generation)
		return errors.New("governance: Cedar reload completed without a durable identity")
	}
	snapshot := observed.snapshot
	// A completed View may be stale relative to a runtime state captured before it
	// began. Do not even compile that older source: a malformed G snapshot is not
	// evidence against an already-installed G+1, and marking the pre-View token
	// unavailable here would poison the newer evaluator. installIfNotOlder has the
	// same monotonic check for a valid candidate; this early check specifically
	// prevents an old candidate's compile failure from becoming an operational
	// failure of the current runtime.
	if beforeLoaded && validPolicyAuthorizationEpochFact(tenant, before.generation) &&
		snapshot.state.generation.Version < before.generation.Version {
		return nil
	}
	if strings.TrimSpace(snapshot.combined) != "" {
		gs, err := compileGrantSet(snapshot.combined)
		if err != nil {
			m.grants.markUnavailableForDurableReloadFailure(tenant, before, beforeLoaded, snapshot.state)
			return err
		}
		snapshot.state.set = gs
	}
	if !snapshot.state.freshnessValid {
		// A bounded selected policy without its durable anchor is operationally
		// unavailable, not merely an expired permit set. In particular, Evaluate
		// must not return Allow by silently omitting an unverified forbid.
		snapshot.state.available = false
	}
	if _, err := m.grants.installIfNotOlderFromObservedState(tenant, before, beforeLoaded, snapshot.state); err != nil {
		m.grants.markUnavailableForDurableReloadFailure(tenant, before, beforeLoaded, snapshot.state)
		return err
	}
	if !snapshot.state.freshnessValid {
		return errBoundedPolicyFreshnessUnavailable
	}
	return nil
}

// mergeCedarSources concatenates two Cedar sources into one policy text (the union the
// engine evaluates). Cedar re-numbers anonymous policies per parse, so concatenation
// never collides policy ids.
func mergeCedarSources(free, managed string) string {
	free, managed = strings.TrimSpace(free), strings.TrimSpace(managed)
	switch {
	case free == "":
		return managed
	case managed == "":
		return free
	default:
		return free + "\n\n" + managed
	}
}

// reloadTenantGrantsLogged calls reloadTenantGrants and logs (never returns) a failure.
// Freshness was already persisted in the originating Mutate; reload reads it alongside
// selection+epoch and installs all facts together. The parameter stays for the C2 caller
// contract, but is intentionally not used as a second, potentially stale cache write.
func (m *Module) reloadTenantGrantsLogged(ctx context.Context, tenant model.TenantID, _ FreshnessRecord) {
	if err := m.reloadTenantGrants(ctx, tenant); err != nil {
		if m.log != nil {
			m.log.Warn("scopedadmin: rows persisted but live grant reload failed; will activate on next reload/boot",
				"tenant", tenant.String(), "err", err)
		}
	}
}

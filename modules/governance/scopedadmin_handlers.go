// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the HTTP authoring surface for scoped administration (the domain logic is
// scopedadmin.go). Custom roles and permission-groups are tenant-wide DEFINITIONS that
// need tenant-admin authority; scoped grants are DELEGATIONS bounded by the per-scope
// ceiling (canDelegate). Every write reprojects the `cedar-managed` policy and is
// self-audited. Routes mount under /v1/m/governance/rbac/ (governance.go).

// maxRBACNameLen bounds a role/permission-group name (the operator-facing key, also a
// URL path segment).
const maxRBACNameLen = 64

// --- DTOs (mirror the frontend) ----------------------------------------------------

type catalogDTO struct {
	Kinds        []string `json:"kinds"`         // every grantable kind: tree kinds + module kinds
	TreeKinds    []string `json:"tree_kinds"`    // the subset that lives in the tree (auth.TreeScopeableKinds)
	Permissions  []string `json:"permissions"`   // the module permissions a mounted module declared (whole, not kind × verb)
	Verbs        []string `json:"verbs"`         // read | write | admin
	BuiltinRoles []string `json:"builtin_roles"` // viewer | editor | admin | owner
	ScopeTrees   []string `json:"scope_trees"`   // tenant | workspace | agent_group | folder
	SubjectKinds []string `json:"subject_kinds"` // user | role | group (S256) — what a grant may target
}

type customRoleDTO struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// BaseRole names a BUILT-IN role this role starts from, resolved LIVE at every
	// projection. It is what makes "editor except X" stay true as the catalog
	// grows, instead of an enumeration that silently goes stale.
	BaseRole    string   `json:"base_role,omitempty"`
	Permissions []string `json:"permissions"`
	Groups      []string `json:"groups,omitempty"`
	// Excludes are subtracted AFTER the base, the direct permissions and every included
	// group — so nothing an include adds can put one back.
	Excludes  []string `json:"excludes,omitempty"`
	CreatedBy string   `json:"created_by,omitempty"`
}

type permGroupDTO struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions"`
	CreatedBy   string   `json:"created_by,omitempty"`
}

type scopedGrantDTO struct {
	ID          string `json:"id,omitempty"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	Role        string `json:"role"`
	RoleCustom  bool   `json:"role_custom,omitempty"`
	ScopeTree   string `json:"scope_tree"`
	ScopeRef    string `json:"scope_ref,omitempty"`
	ScopeClass  string `json:"scope_class,omitempty"`
	Note        string `json:"note,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
}

// delegationDomainDTO is one (scope, grantable-perms) pair the actor may delegate
// within — the projection of an internal delegationDomain for the console.
type delegationDomainDTO struct {
	ScopeTree   string   `json:"scope_tree"`
	ScopeRef    string   `json:"scope_ref,omitempty"`
	ScopeClass  string   `json:"scope_class,omitempty"`
	Permissions []string `json:"permissions"`
}

// delegationAuthorityDTO is the actor's full delegation ceiling: the unbounded-root
// flag plus, for a non-root actor, every domain canDelegate would accept a grant in.
type delegationAuthorityDTO struct {
	Superadmin bool                  `json:"superadmin"`
	Domains    []delegationDomainDTO `json:"domains"`
}

func toCustomRoleDTO(c customRole) customRoleDTO {
	return customRoleDTO{
		Name: c.Name, DisplayName: c.DisplayName, Description: c.Description,
		BaseRole: c.Base, Permissions: nonNil(c.Perms), Groups: c.Groups,
		Excludes: c.Excludes, CreatedBy: c.CreatedBy,
	}
}

func toPermGroupDTO(g permGroup) permGroupDTO {
	return permGroupDTO{
		Name: g.Name, DisplayName: g.DisplayName, Description: g.Description,
		Permissions: nonNil(g.Perms), CreatedBy: g.CreatedBy,
	}
}

func toScopedGrantDTO(g scopedGrant) scopedGrantDTO {
	return scopedGrantDTO{
		ID: g.ID.String(), SubjectKind: g.SubjectKind, SubjectRef: g.SubjectRef,
		Role: g.Role, RoleCustom: g.RoleCustom,
		ScopeTree: g.Scope.Tree, ScopeRef: g.Scope.Ref, ScopeClass: g.Scope.Class,
		Note: g.Note, CreatedBy: g.CreatedBy,
	}
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// --- error mapping ------------------------------------------------------------------

// conflictError is a 409 raised inside a write closure (e.g. deleting a role still in
// use). Distinct from a store ErrConflict so the message survives to the client.
type conflictError string

func (e conflictError) Error() string { return string(e) }

func asConflict(err error) (string, bool) {
	var c conflictError
	if errors.As(err, &c) {
		return string(c), true
	}
	return "", false
}

// writeRBACError maps a write-path error to its HTTP status: validation → 400, ceiling
// → 403, in-use conflict → 409, otherwise the store mapping (not-found → 404, unique
// conflict → 409, else 500).
func writeRBACError(w http.ResponseWriter, err error) {
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if msg, ok := asCeiling(err); ok {
		writeJSON(w, http.StatusForbidden, errorBody(msg))
		return
	}
	if msg, ok := asConflict(err); ok {
		writeJSON(w, http.StatusConflict, errorBody(msg))
		return
	}
	writeStoreError(w, err)
}

// --- validation ---------------------------------------------------------------------

// validRBACName reports whether s is an acceptable role/permission-group name: 1..64
// chars from a URL- and identifier-safe class ([A-Za-z0-9._-]). The name is the operator
// key AND a {name} URL path segment, so a '/' or space would make the row unreachable via
// the by-name routes; the human label lives in display_name instead.
func validRBACName(s string) bool {
	if s == "" || len(s) > maxRBACNameLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// validatePerms normalizes and validates a permission list against the scopeable
// catalog, returning the deduplicated set as a sorted slice. An unknown kind/verb is a
// validationError (deny-closed).
func validatePerms(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !isCatalogPerm(p) {
			return nil, validationError("not a scopeable permission (must be <kind>:<verb> with kind in the catalog and verb read|write|admin): " + p)
		}
		seen[p] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// validateRoleComposition validates the base + excludes pair for a custom role and
// returns the normalized exclusion set.
//
// The base must be a BUILT-IN role. A custom role as base is deliberately refused: it
// would make the effective set depend on a chain that can contain a cycle, and the
// composable primitive for reuse already exists (permission-groups). Blank = no base.
//
// The excludes are validated against the SAME catalog as the permissions, for the same
// reason: an operator who types "models:keys:wrtie" must be told, not left believing they
// capped something. An exclusion that removes nothing TODAY is deliberately allowed — the
// base is resolved live and grows, so "never this permission, whatever editor becomes" is
// precisely the durable statement this feature exists to let an operator make.
func validateRoleComposition(base string, excludes []string) ([]string, error) {
	if base != "" && !auth.IsRole(base) {
		return nil, validationError("base_role must be a built-in role (viewer, editor, admin, owner) or empty: " + base)
	}
	return validatePerms(excludes)
}

// boundedMeta validates the optional display_name/description prose length, mirroring the
// module's other bounded-prose fields (maxNoteLen) so a role/group cannot smuggle a
// multi-megabyte write.
func boundedMeta(displayName, description string) error {
	if len(displayName) > maxNoteLen || len(description) > maxNoteLen {
		return validationError("display_name/description too long")
	}
	return nil
}

// requireTenantAdmin gates DEFINITION authoring (custom roles, permission-groups) to a
// superadmin or a tenant admin/owner. A scoped-admin (no tenant-wide admin role) can
// ASSIGN roles within its scope but cannot define new tenant-wide ones.
func requireTenantAdmin(actor auth.Principal, tenant model.TenantID) error {
	if actor.Superadmin {
		return nil
	}
	if r, ok := actor.RoleIn(tenant); ok && auth.RoleRank(r) >= auth.RoleRank(auth.RoleAdmin) {
		return nil
	}
	return ceilingError("custom roles and permission-groups require tenant admin/owner authority")
}

// validateScopeRefs checks a grant scope: the tree node names a real workspace/agent-
// group (deny-closed against a typo'd slug), the tenant tree carries no ref, and the
// class is a scopeable kind.
func validateScopeRefs(ctx context.Context, sc store.Scope, s scopeSpec) error {
	switch s.Tree {
	case scopeTenant:
		if s.Ref != "" {
			return validationError("tenant scope takes no scope_ref")
		}
	case scopeWorkspace:
		if s.Ref == "" {
			return validationError("workspace scope requires a scope_ref (workspace slug)")
		}
		if _, ok, err := findWorkspaceBySlug(ctx, sc, s.Ref); err != nil {
			return err
		} else if !ok {
			return validationError("unknown workspace: " + s.Ref)
		}
	case scopeAgentGroup:
		if s.Ref == "" {
			return validationError("agent_group scope requires a scope_ref (agent-group slug)")
		}
		if _, ok, err := findAgentGroupBySlug(ctx, sc, s.Ref); err != nil {
			return err
		} else if !ok {
			return validationError("unknown agent group: " + s.Ref)
		}
	case scopeFolder:
		if s.Ref == "" {
			return validationError("folder scope requires a scope_ref (folder Resource id)")
		}
		// Deny-closed against a typo'd id: the anchor Resource must exist (any node is a valid
		// subtree root). A dangling folder grant would project a permit that matches nothing.
		if _, ok, err := findResourceByID(ctx, sc, s.Ref); err != nil {
			return err
		} else if !ok {
			return validationError("unknown folder resource: " + s.Ref)
		}
	default:
		return validationError("scope_tree must be tenant, workspace, agent_group or folder")
	}
	if s.Class != "" && !auth.IsScopeableKind(s.Class) {
		return validationError("scope_class is not a scopeable resource kind: " + s.Class)
	}
	// a MODULE kind ("<ns>:<res>") is grantable but is not an tree node — the
	// scope resolver has no stored row to walk for it, so a permit carrying
	// `resource in Workspace::…` can never match a module route. At tenant scope the class
	// is a real filter (the permit carries no resource condition); on any tree scope it
	// would persist a grant that silently authorizes nothing. Rejected for the same reason
	// the agent_group check below rejects a non-agent class: this module does not store
	// inert grants. Lifting this is the job of the step that makes module routes resolve
	// the tree, not of a relaxed check here.
	if s.Class != "" && s.Tree != scopeTenant && !auth.IsTreeScopeableKind(s.Class) {
		return validationError("scope_class " + s.Class + " is a module permission kind: it can only be scoped at tenant level, because module routes do not resolve the workspace/agent-group/folder tree")
	}
	// An agent-group only ever contains agents (the resolver folds agent-group membership
	// onto agent entities only), so any non-agent class would project a permit that can
	// never match — reject it rather than persist a silently-inert grant.
	if s.Tree == scopeAgentGroup && s.Class != "" && s.Class != "agent" {
		return validationError("an agent_group scope only applies to agents; scope_class must be empty or 'agent'")
	}
	return nil
}

// --- catalog ------------------------------------------------------------------------

func (m *Module) handleRBACCatalog(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	// the catalog now answers TWO questions that used to have one answer, because a
	// module permission is grantable without being an tree node. A console that reads
	// only `kinds` would happily offer "compliance:risk within Workspace payments" — a
	// combination validateScopeRefs rejects. `tree_kinds` is what a scope-tree picker must
	// filter on; `permissions` is the exact module set, since a module kind does NOT imply
	// all three verbs (see auth.IsGrantablePermission).
	writeJSON(w, http.StatusOK, catalogDTO{
		Kinds:        auth.ScopeableKinds(),
		TreeKinds:    auth.TreeScopeableKinds(),
		Permissions:  permStrings(auth.ModulePermissions()),
		Verbs:        []string{auth.VerbRead, auth.VerbWrite, auth.VerbAdmin},
		BuiltinRoles: []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner},
		ScopeTrees:   []string{scopeTenant, scopeWorkspace, scopeAgentGroup, scopeFolder},
		SubjectKinds: []string{subjectUser, subjectRole, subjectGroup},
	})
}

// rejectInertTreeScopedGrant refuses a grant bounded to a workspace/agent-group/folder
// whose ENTIRE effective permission set is module permissions.
//
// The class-shaped version of this is caught by validateScopeRefs; this is the ROLE-shaped
// version, and leaving it open would have been worse than inert. A module route authorizes
// through authzTenant, which seeds no Resource.ID and no workspace, so the scope resolver
// takes its parent-less fast path (grants.go) and a permit carrying
// `resource in Workspace::…` can never match. The access half of such a grant therefore
// does nothing — while, if the role is admin-capable, projectManagedCedar still emits the
// UNCONDITIONAL tenant-wide rbac:read/admin delegation permit for the subject. The
// operator would read "module admin inside workspace W" and actually get "reach the
// delegation API across the whole tenant, and nothing else". That is a mis-grant, not a
// no-op, so it is refused at authoring time with the scope that does work.
//
// A MIXED role (core plus module permissions) is deliberately allowed: its core half bites
// on the tree scope, so the grant is partly effective and the operator is not misled about
// what a tree scope means. When module routes resolve the tree, this guard is what gets
// removed — not a check that was already too weak.
func rejectInertTreeScopedGrant(g scopedGrant, perms map[string]bool) error {
	if g.Scope.Tree == scopeTenant {
		return nil
	}
	for p := range perms {
		if kind, _, ok := splitPerm(p); ok && auth.IsTreeScopeableKind(kind) {
			return nil // at least one permission the scope tree can actually reach
		}
	}
	return validationError("this grant confers only module permissions, and a " + g.Scope.Tree +
		" scope cannot reach them: module routes do not resolve the workspace/agent-group/folder tree, so the grant would authorize nothing. Grant it at tenant scope, or include a permission on a scope-tree resource kind")
}

// permStrings renders a permission slice for the wire (never nil, so the field is an
// empty JSON array rather than null on an engine with no modules mounted).
func permStrings(in []auth.Permission) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		out = append(out, string(p))
	}
	return out
}

// --- delegation authority (the actor's own ceiling) ---------------------------------

// handleDelegationAuthority returns the acting principal's delegation ceiling: the
// (scope, grantable-perm) domains canDelegate (scopedadmin.go) enforces on every grant
// write. The console SHOWS this so an admin sees what it may delegate before it tries
// — the engine still re-checks server-side on each create/revoke/definition-edit, so
// this is advisory display, never the boundary. A superadmin is the unbounded root
// (superadmin:true; the domain list is empty because no ceiling applies). read-tier:
// it discloses only the actor's OWN authority, derived from the same rows canDelegate
// reads, so the UI can never drift from the engine.
func (m *Module) handleDelegationAuthority(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := delegationAuthorityDTO{Superadmin: mc.Principal.Superadmin, Domains: []delegationDomainDTO{}}
	if mc.Principal.Superadmin {
		writeJSON(w, http.StatusOK, out)
		return
	}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		roles, e := loadCustomRoles(r.Context(), sc)
		if e != nil {
			return e
		}
		groups, e := loadPermGroups(r.Context(), sc)
		if e != nil {
			return e
		}
		grants, e := loadScopedGrants(r.Context(), sc)
		if e != nil {
			return e
		}
		for _, d := range actorDomains(mc.Principal, mc.Tenant, grants, roles, groups) {
			out.Domains = append(out.Domains, delegationDomainDTO{
				ScopeTree:   d.scope.Tree,
				ScopeRef:    d.scope.Ref,
				ScopeClass:  d.scope.Class,
				Permissions: sortedPerms(d.perms),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- custom roles -------------------------------------------------------------------

func (m *Module) handleListCustomRoles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[customRoleDTO]{Items: []customRoleDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		roles, e := loadCustomRoles(r.Context(), sc)
		if e != nil {
			return e
		}
		for _, c := range sortedRoleValues(roles) {
			out.Items = append(out.Items, toCustomRoleDTO(c))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetCustomRole(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	name := chi.URLParam(r, "name")
	var dto customRoleDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(customRoleKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(r.Context(), repo, eq(colRBACName, name))
		if e != nil || !ok {
			return e
		}
		dto, found = toCustomRoleDTO(recToCustomRole(rec)), true
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteCustomRole(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	name := chi.URLParam(r, "name")
	if err := requireTenantAdmin(mc.Principal, mc.Tenant); err != nil {
		writeRBACError(w, err)
		return
	}
	werr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if e := lockPolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		repo, e := sc.Ext(customRoleKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(r.Context(), repo, eq(colRBACName, name))
		if e != nil {
			return e
		}
		if !ok {
			return store.ErrNotFound
		}
		grants, e := loadScopedGrants(r.Context(), sc)
		if e != nil {
			return e
		}
		for _, g := range grants {
			if g.RoleCustom && g.Role == name {
				return conflictError("custom role is referenced by an active scoped grant; revoke it first")
			}
		}
		id := model.ID(rec.String(model.ColID))
		if e := repo.Delete(r.Context(), id); e != nil {
			return e
		}
		return auditEvent(r.Context(), sc, mc, "governance.rbac.role.delete", customRoleKind, id, map[string]any{"name": name})
	})
	if werr != nil {
		writeRBACError(w, werr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- permission-groups --------------------------------------------------------------

func (m *Module) handleListPermGroups(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[permGroupDTO]{Items: []permGroupDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		groups, e := loadPermGroups(r.Context(), sc)
		if e != nil {
			return e
		}
		for _, g := range sortedGroupValues(groups) {
			out.Items = append(out.Items, toPermGroupDTO(g))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetPermGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	name := chi.URLParam(r, "name")
	var dto permGroupDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(permGroupKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(r.Context(), repo, eq(colRBACName, name))
		if e != nil || !ok {
			return e
		}
		dto, found = toPermGroupDTO(recToPermGroup(rec)), true
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeletePermGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	name := chi.URLParam(r, "name")
	if err := requireTenantAdmin(mc.Principal, mc.Tenant); err != nil {
		writeRBACError(w, err)
		return
	}
	werr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if e := lockPolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		repo, e := sc.Ext(permGroupKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(r.Context(), repo, eq(colRBACName, name))
		if e != nil {
			return e
		}
		if !ok {
			return store.ErrNotFound
		}
		roles, e := loadCustomRoles(r.Context(), sc)
		if e != nil {
			return e
		}
		for _, role := range roles {
			for _, gn := range role.Groups {
				if gn == name {
					return conflictError("permission-group is included by custom role " + role.Name + "; remove it from the role first")
				}
			}
		}
		id := model.ID(rec.String(model.ColID))
		if e := repo.Delete(r.Context(), id); e != nil {
			return e
		}
		return auditEvent(r.Context(), sc, mc, "governance.rbac.group.delete", permGroupKind, id, map[string]any{"name": name})
	})
	if werr != nil {
		writeRBACError(w, werr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- scoped grants ------------------------------------------------------------------

func (m *Module) handleListScopedGrants(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[scopedGrantDTO]{Items: []scopedGrantDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		grants, e := loadScopedGrants(r.Context(), sc)
		if e != nil {
			return e
		}
		sort.Slice(grants, func(i, j int) bool { return scopedGrantKeyOf(grants[i]) < scopedGrantKeyOf(grants[j]) })
		for _, g := range grants {
			out.Items = append(out.Items, toScopedGrantDTO(g))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleGetScopedGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	var dto scopedGrantDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(scopedGrantKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(r.Context(), id)
		if e != nil {
			return e
		}
		dto = toScopedGrantDTO(recToScopedGrant(rec))
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleRevokeScopedGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	reloadManaged := false
	var reloadFreshness FreshnessRecord
	werr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if e := lockPolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		repo, e := sc.Ext(scopedGrantKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(r.Context(), id)
		if e != nil {
			return e
		}
		g := recToScopedGrant(rec)
		state, e := loadManagedProjectionState(r.Context(), sc)
		if e != nil {
			return e
		}
		// You may revoke only a grant you could have created (within your authority).
		if e := canDelegate(r.Context(), sc, mc.Principal, mc.Tenant, g, state.grants, state.roles, state.groups); e != nil {
			return e
		}
		prospective := state
		prospective.grants = make([]scopedGrant, 0, len(state.grants)-1)
		for _, existing := range state.grants {
			if existing.ID != id {
				prospective.grants = append(prospective.grants, existing)
			}
		}
		plan, e := planManagedProjection(r.Context(), sc, prospective)
		if e != nil {
			return e
		}
		if plan.changed {
			if e := advancePolicyAuthorizationEpoch(r.Context(), sc); e != nil {
				return e
			}
		}
		if e := repo.Delete(r.Context(), id); e != nil {
			return e
		}
		if e := appendManagedProjection(r.Context(), sc, plan, mc.Principal.Actor()); e != nil {
			return e
		}
		if e := persistManagedProjectionFreshness(r.Context(), sc, plan); e != nil {
			return e
		}
		reloadManaged = plan.changed
		reloadFreshness = plan.freshness
		return auditEvent(r.Context(), sc, mc, "governance.rbac.revoke", scopedGrantKind, id, map[string]any{
			"subject_kind": g.SubjectKind, "role": g.Role, "scope_tree": g.Scope.Tree, "scope_ref": g.Scope.Ref,
		})
	})
	if werr != nil {
		writeRBACError(w, werr)
		return
	}
	if reloadManaged {
		m.reloadTenantGrantsLogged(r.Context(), mc.Tenant, reloadFreshness)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- record builders ----------------------------------------------------------------

func roleRecord(name, display, desc, base string, perms, groups, excludes []string, author string) model.Record {
	return model.Record{
		colRBACName:        name,
		colRBACDisplayName: display,
		colRBACDescription: desc,
		colRBACBaseRole:    base,
		colRBACPerms:       jsonStringsCol(perms),
		colRBACGroups:      jsonStringsCol(groups),
		colRBACExcludes:    jsonStringsCol(excludes),
		colRBACCreatedBy:   author,
	}
}

func groupRecord(name, display, desc string, perms []string, author string) model.Record {
	return model.Record{
		colRBACName:        name,
		colRBACDisplayName: display,
		colRBACDescription: desc,
		colRBACPerms:       jsonStringsCol(perms),
		colRBACCreatedBy:   author,
	}
}

func grantRecord(g scopedGrant, author string) model.Record {
	return model.Record{
		colSGSubjectKind: g.SubjectKind,
		colSGSubjectRef:  g.SubjectRef,
		colSGRole:        g.Role,
		colSGRoleCustom:  g.RoleCustom,
		colSGScopeTree:   g.Scope.Tree,
		colSGScopeRef:    g.Scope.Ref,
		colSGScopeClass:  g.Scope.Class,
		colSGCreatedBy:   author,
		colSGNote:        g.Note,
	}
}

func sameCustomRoleAuthoring(left, right customRole) bool {
	return left.DisplayName == right.DisplayName &&
		left.Description == right.Description &&
		left.Base == right.Base &&
		slices.Equal(left.Perms, right.Perms) &&
		slices.Equal(left.Groups, right.Groups) &&
		slices.Equal(left.Excludes, right.Excludes)
}

func samePermGroupAuthoring(left, right permGroup) bool {
	return left.DisplayName == right.DisplayName &&
		left.Description == right.Description &&
		slices.Equal(left.Perms, right.Perms)
}

// sameScopedGrantIdentity mirrors the store's unique grant tuple. Note and author are
// metadata on that authority fact, not a second independently enforceable grant.
func sameScopedGrantIdentity(left, right scopedGrant) bool {
	return left.SubjectKind == right.SubjectKind &&
		left.SubjectRef == right.SubjectRef &&
		left.Role == right.Role &&
		left.Scope == right.Scope
}

// --- deterministic listing helpers --------------------------------------------------

func sortedRoleValues(in map[string]customRole) []customRole {
	out := make([]customRole, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedGroupValues(in map[string]permGroup) []permGroup {
	out := make([]permGroup, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

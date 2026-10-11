// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// An inheritance filter sits on a container node (workspace, agent group or folder) and
// names one resource class. It says: rights that reach this node from ABOVE (a
// tenant-wide role, a grant anchored higher) do not apply to that class here; grants at
// or below the node still do. It is not a forbid, which stays absolute.
//
// This file stores and authors the row; inheritance_filter_walk.go is where the lineage
// walk reads it and the authorizer drops what it filters. The row's node is a scopeSpec,
// the same vocabulary a grant uses for where it is anchored, so the wire and the console
// share one shape.
//
// Every write advances the policy authorization epoch and then reloads the engine's
// snapshot at it, as the grant writers do, so a witness or a cached decision minted before
// the write never outlives it.
//
// A filter applies to every principal, a tenant admin, owner and superadmin included: it
// removes rights from above a node, and a role is such a right. Nobody is locked out of the
// filter itself: removing one is governance:rbac:admin, a module permission the filters
// never touch because their class must be a scope-tree kind.

const (
	inheritanceFilterKind  model.Kind = "governance.inheritance_filter"
	inheritanceFilterTable            = "governance_inheritance_filter" // 29 chars
)

type inheritanceFilterDTO struct {
	ID         string `json:"id,omitempty"`
	ScopeTree  string `json:"scope_tree"`
	ScopeRef   string `json:"scope_ref,omitempty"`
	ScopeClass string `json:"scope_class"`
	CreatedBy  string `json:"created_by,omitempty"`
}

// node is the scopeSpec the filter sits on, for validation and the ceiling.
func (f inheritanceFilterDTO) node() scopeSpec {
	return scopeSpec{Tree: f.ScopeTree, Ref: f.ScopeRef, Class: f.ScopeClass}
}

func recToInheritanceFilter(r model.Record) inheritanceFilterDTO {
	return inheritanceFilterDTO{
		ID: r.String(model.ColID), ScopeTree: r.String(colSGScopeTree), ScopeRef: r.String(colSGScopeRef),
		ScopeClass: r.String(colSGScopeClass), CreatedBy: r.String(colSGCreatedBy),
	}
}

func inheritanceFilterRecord(node scopeSpec, author string) model.Record {
	return model.Record{
		colSGScopeTree:  node.Tree,
		colSGScopeRef:   node.Ref,
		colSGScopeClass: node.Class,
		colSGCreatedBy:  author,
	}
}

// registerInheritanceFilter declares the row. scope_ref and scope_class are stored ""
// never NULL, as for a scoped grant, so the unique tuple dedupes the same on both engines.
func registerInheritanceFilter(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  inheritanceFilterKind,
		Table: inheritanceFilterTable,
		Fields: []model.FieldSpec{
			{Name: colSGScopeTree, Kind: model.KindText, Indexed: true, Principal: model.None("workspace, agent_group or folder; the writer refuses any other tree: inheritance_filter.go:91-93, scopedadmin_handlers.go:288-289")},
			{Name: colSGScopeRef, Kind: model.KindText, Principal: model.None("a workspace or agent-group slug or a resource id, resolved by validateScopeRefs: scopedadmin_handlers.go:253-312")},
			{Name: colSGScopeClass, Kind: model.KindText, Principal: model.None("a tree resource kind, checked by auth.IsTreeScopeableKind: inheritance_filter.go:94-96")},
			{Name: colSGCreatedBy, Kind: model.KindText, Principal: pdeclActorEvidence},
		},
		Indexes: []model.IndexSpec{{
			// One filter per (node, class). Leads with tenant_id per.
			Name:    "governance_inheritance_filter_uniq",
			Columns: []string{model.ColTenantID, colSGScopeTree, colSGScopeRef, colSGScopeClass},
			Unique:  true,
		}},
	})
}

// validateInheritanceFilterNode checks the node a filter would sit on. A tenant is the
// top of the tree, so nothing reaches it from above; a class is required because the
// filter is per resource class; and the class must be a tree kind, since module routes do
// not resolve the tree and a filter on one would never match.
func validateInheritanceFilterNode(ctx context.Context, sc store.Scope, node scopeSpec) error {
	if node.Tree == scopeTenant {
		return validationError("a tenant has nothing above it: scope_tree must be workspace, agent_group or folder")
	}
	if !auth.IsTreeScopeableKind(node.Class) {
		return validationError("scope_class must be a resource kind of the scope tree")
	}
	return validateScopeRefs(ctx, sc, node)
}

// requireNodeAdmin is the ceiling for setting or removing a filter: the actor must be an
// admin of the node for the filter's class. That is a delegation domain (tenant admin/owner,
// or an admin-capable scoped grant) whose scope contains the node AND whose permissions
// hold write or admin on the class, so an admin of one class cannot filter another. The
// delegation ceiling's own domains answer it, so there is one definition of "admin of".
func requireNodeAdmin(ctx context.Context, sc store.Scope, actor auth.Principal, tenant model.TenantID, node scopeSpec, state managedProjectionState) error {
	if actor.Superadmin {
		return nil
	}
	write, admin := node.Class+":"+auth.VerbWrite, node.Class+":"+auth.VerbAdmin
	for _, d := range actorDomains(actor, tenant, state.grants, state.roles, state.groups) {
		if !d.perms[write] && !d.perms[admin] {
			continue
		}
		contained, err := scopeContains(ctx, sc, d.scope, node)
		if err != nil {
			return err
		}
		if contained {
			return nil
		}
	}
	return ceilingError("only an admin of the node may set or remove its inheritance filter")
}

func (m *Module) handleListInheritanceFilters(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[inheritanceFilterDTO]{Items: []inheritanceFilterDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(inheritanceFilterKind)
		if e != nil {
			return e
		}
		recs, e := listAll(r.Context(), repo)
		if e != nil {
			return e
		}
		for _, rec := range recs {
			out.Items = append(out.Items, recToInheritanceFilter(rec))
		}
		sort.Slice(out.Items, func(i, j int) bool { return filterKey(out.Items[i]) < filterKey(out.Items[j]) })
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func filterKey(f inheritanceFilterDTO) string {
	return f.ScopeTree + "\x00" + f.ScopeRef + "\x00" + f.ScopeClass
}

func (m *Module) handleGetInheritanceFilter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	var dto inheritanceFilterDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, e := sc.Ext(inheritanceFilterKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(r.Context(), id)
		if e != nil {
			return e
		}
		dto = recToInheritanceFilter(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleCreateInheritanceFilter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in inheritanceFilterDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	node := scopeSpec{Tree: strings.TrimSpace(in.ScopeTree), Ref: strings.TrimSpace(in.ScopeRef), Class: strings.TrimSpace(in.ScopeClass)}
	var created model.ID
	werr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The epoch lock serializes this check against a concurrent grant change, so the
		// actor's authority is the one read here.
		if e := lockPolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		if e := validateInheritanceFilterNode(r.Context(), sc, node); e != nil {
			return e
		}
		state, e := loadManagedProjectionState(r.Context(), sc)
		if e != nil {
			return e
		}
		if e := requireNodeAdmin(r.Context(), sc, mc.Principal, mc.Tenant, node, state); e != nil {
			return e
		}
		if e := advancePolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		// The unique index refuses a second filter for the same (node, class) with a 409.
		repo, e := sc.Ext(inheritanceFilterKind)
		if e != nil {
			return e
		}
		rec, e := repo.Create(r.Context(), inheritanceFilterRecord(node, mc.Principal.Actor()))
		if e != nil {
			return e
		}
		created = model.ID(rec.String(model.ColID))
		return auditEvent(r.Context(), sc, mc, "governance.rbac.filter", inheritanceFilterKind, created, map[string]any{
			"scope_tree": node.Tree, "scope_ref": node.Ref, "scope_class": node.Class,
		})
	})
	if werr != nil {
		writeRBACError(w, werr)
		return
	}
	m.reloadTenantGrantsLogged(r.Context(), mc.Tenant, FreshnessRecord{})
	in.ScopeTree, in.ScopeRef, in.ScopeClass = node.Tree, node.Ref, node.Class
	in.ID, in.CreatedBy = created.String(), mc.Principal.Actor()
	writeJSON(w, http.StatusCreated, in)
}

func (m *Module) handleRemoveInheritanceFilter(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	werr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if e := lockPolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		repo, e := sc.Ext(inheritanceFilterKind)
		if e != nil {
			return e
		}
		rec, e := repo.Get(r.Context(), id)
		if e != nil {
			return e
		}
		f := recToInheritanceFilter(rec)
		node := f.node()
		state, e := loadManagedProjectionState(r.Context(), sc)
		if e != nil {
			return e
		}
		if e := requireNodeAdmin(r.Context(), sc, mc.Principal, mc.Tenant, node, state); e != nil {
			return e
		}
		if e := advancePolicyAuthorizationEpoch(r.Context(), sc); e != nil {
			return e
		}
		if e := repo.Delete(r.Context(), id); e != nil {
			return e
		}
		return auditEvent(r.Context(), sc, mc, "governance.rbac.unfilter", inheritanceFilterKind, id, map[string]any{
			"scope_tree": node.Tree, "scope_ref": node.Ref, "scope_class": node.Class,
		})
	})
	if werr != nil {
		writeRBACError(w, werr)
		return
	}
	m.reloadTenantGrantsLogged(r.Context(), mc.Tenant, FreshnessRecord{})
	w.WriteHeader(http.StatusNoContent)
}

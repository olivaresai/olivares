// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// What a department carries: its instructions and its default session template (#530).
//
// Both are session templates, assigned by a binding like any other source: "instructions" names a
// template whose custom instructions are the text a launch is given, and "template" names the
// template a launch starts from when it names none. A department is a node of the organization
// tree (a workspace) or a directory group, the two ways the product groups people, and the
// assignment obeys the tree it is made on. An assignment on a department reaches its
// sub-departments, one on a directory group reaches its members and the groups nested under it
// (the principal's groups already hold that closure), a forbid on a sub-department or a group is
// absolute, and a disabled binding assigns nothing.
//
// Only those two trees assign them. A role or a person decides who may use a source; a standard
// is not a thing a person has, so such a binding is refused at the write instead of being stored
// and silently ignored.
//
// AssignedTo answers for a person and the departments they belong to, or for a department alone
// (a workstation has no person): tenant-wide RBAC and scoped grants decide who may use a source
// and never what a department carries, so neither is asked. DepartmentOf is how a launch finds
// the department of an acting agent or a confined member.
//
// Writing the bindings is Business (profile_assignment.go); AssignedTo answers in every edition,
// and the bindings already stored keep applying in Community.

// errNotAStandard answers a question about a kind of source that is not a standard: a misspelt
// kind must not read as a department that carries nothing.
var errNotAStandard = errors.New("sourcescope: only instructions and template are assigned to a department")

func isStandardsSource(sourceType string) bool {
	return sourceType == sourceInstructions || sourceType == sourceTemplate
}

// normalizeStandardsSourceRef checks the reference of an instructions or template assignment: the
// canonical id of a session template, which the lookup matches byte for byte. A misspelt one would
// otherwise be stored, match nothing and leave the department without its standard without a word
// (or refuse its launches, for a default template). Other source types are untouched.
func normalizeStandardsSourceRef(sourceType, ref string) string {
	if !isStandardsSource(sourceType) {
		return ""
	}
	if id, err := model.ParseID(ref); err != nil || string(id) != ref {
		return "source_ref must be the id of a session template"
	}
	return ""
}

// checkStandardsScope refuses a binding of the department's standards on anything but the
// organization tree or a directory group.
func checkStandardsScope(sourceType, scopeTree string) string {
	if isStandardsSource(sourceType) && scopeTree != scopeWorkspace && scopeTree != scopeUserGroup {
		return "instructions and templates are assigned to a department or a directory group: scope_tree must be workspace or user_group"
	}
	return ""
}

// refuseSecondDefaultTemplate keeps one default template per department or group: a second
// enabled allow on the same one is a 409, so a department or group never holds two. (A person in
// several departments and groups can still reach several; the last in the order of AssignedTo
// governs: a group's after a department's, a deeper department's after its parent's.) in.ScopeRef is already canonical (resolveScope ran); self is the binding being
// changed, "" for a create.
func refuseSecondDefaultTemplate(ctx context.Context, sc store.Scope, in bindingDTO, self string) error {
	if in.SourceType != sourceTemplate || !in.Enabled || normalizeEffect(in.Effect) == effectForbid {
		return nil
	}
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceTemplate), eq(colScopeTree, in.ScopeTree), eq(colScopeRef, in.ScopeRef))
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.String(model.ColID) == self || !rec.Bool(colEnabled) || normalizeEffect(rec.String(colEffect)) == effectForbid {
			continue
		}
		return store.ErrConflict
	}
	return nil
}

// AssignedTo lists the sources of sourceType ("instructions" or "template") assigned to a
// person and the departments they belong to, the least specific first: what an ancestor
// department assigned, then what the department assigned itself, then what the person's
// directory groups were assigned, in the order they were assigned. A zero principal is a
// department alone. A department that does not exist, or the empty name, carries nothing, and a
// store error, or a sourceType that is neither, is returned so a caller never treats a failed read
// as an empty department.
func (r *Resolver) AssignedTo(ctx context.Context, tenant model.TenantID, principal auth.Principal, departments []string, sourceType string) ([]string, error) {
	if !isStandardsSource(sourceType) {
		return nil, errNotAStandard
	}
	data := r.m.moduleData()
	if data == nil {
		return nil, errNotReady
	}
	var out []string
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		// The organization tree the departments sit in: each one, and every department above it.
		depth := map[string]int{}
		for _, department := range departments {
			if department == "" {
				continue
			}
			ws, found, err := findWorkspaceBySlug(ctx, sc, department)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			slug, above, err := store.WorkspaceLineage(ctx, sc, ws.ID)
			if err != nil {
				return err
			}
			for i, ancestor := range above {
				depth[ancestor] = i
			}
			depth[slug] = len(above)
		}
		recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceType))
		if err != nil {
			return err
		}
		active := make([]model.Record, 0, len(recs))
		for _, rec := range recs {
			expired, err := bindingExpired(toBindingDTO(rec), r.m.clock.Now().Time())
			if err != nil {
				return err
			}
			if !expired {
				active = append(active, rec)
			}
		}
		out = assignedAlong(depth, principal.GroupsIn(tenant), active)
		return nil
	})
	return out, err
}

// assignedAlong decides which sources the departments (workspace slug to depth) and the
// directory groups carry from the stored bindings of one source type: an enabled allow that
// contains them, unless an enabled forbid does, which wins at any depth.
func assignedAlong(depth map[string]int, groups []string, recs []model.Record) []string {
	allowed := map[string]standingHit{}
	forbidden := map[string]bool{}
	for _, rec := range recs {
		if !rec.Bool(colEnabled) {
			continue
		}
		ref, at := rec.String(colSourceRef), rec.String(colScopeRef)
		hit := standingHit{created: rec.String(model.ColCreatedAt)}
		switch rec.String(colScopeTree) {
		case scopeWorkspace:
			d, ok := depth[at]
			if !ok {
				continue
			}
			hit.depth = d
		case scopeUserGroup:
			if !slices.Contains(groups, at) {
				continue
			}
			hit.tree = 1
		default:
			continue
		}
		if normalizeEffect(rec.String(colEffect)) == effectForbid {
			forbidden[ref] = true
			continue
		}
		if cur, ok := allowed[ref]; !ok || hit.after(cur) {
			allowed[ref] = hit
		}
	}
	var out []string
	for ref := range allowed {
		if !forbidden[ref] {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := allowed[out[i]], allowed[out[j]]
		if fi.tree != fj.tree || fi.depth != fj.depth || fi.created != fj.created {
			return fj.after(fi)
		}
		return out[i] < out[j]
	})
	return out
}

// standingHit is where a binding reached a caller, and so where it stands in the order a caller
// is given standards.
type standingHit struct {
	tree    int // 0 a department, 1 a directory group
	depth   int // of the department in the organization tree, root first
	created string
}

// after reports whether h comes after o: departments before directory groups, a department
// after the ones above it, a group after the ones assigned before it.
func (h standingHit) after(o standingHit) bool {
	if h.tree != o.tree {
		return h.tree > o.tree
	}
	if h.depth != o.depth {
		return h.depth > o.depth
	}
	return h.created > o.created
}

// DepartmentOf is the department a launch by principal belongs to: the department the
// person's membership is confined to, or the one an acting agent lives in. A tenant-wide member
// belongs to none, and so does an agent the tenant does not know. The slug is the one
// AssignedTo takes.
func (r *Resolver) DepartmentOf(ctx context.Context, tenant model.TenantID, principal auth.Principal) (string, error) {
	data := r.m.moduleData()
	if data == nil {
		return "", errNotReady
	}
	var slug string
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		if principal.AgentIdentity != "" {
			actor, err := resolveActorScope(ctx, sc, actorRef{kind: actorAgent, ref: principal.AgentIdentity})
			slug = actor.workspaceSlug
			return err
		}
		if ws, confined := principal.ConfinedWorkspaceIn(tenant); confined {
			var err error
			slug, err = store.WorkspaceSlug(ctx, sc, ws)
			return err
		}
		return nil
	})
	return slug, err
}

// IsAssigned reports whether an enabled allow assigns the template as instructions or as a default
// to any department or group. Changing such a template changes what a department is given, which
// the session templates' own permission does not cover.
func (r *Resolver) IsAssigned(ctx context.Context, tenant model.TenantID, template string) (bool, error) {
	data := r.m.moduleData()
	if data == nil {
		return false, errNotReady
	}
	var assigned bool
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		for _, sourceType := range []string{sourceInstructions, sourceTemplate} {
			recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceType), eq(colSourceRef, template))
			if err != nil {
				return err
			}
			for _, rec := range recs {
				if rec.Bool(colEnabled) && normalizeEffect(rec.String(colEffect)) != effectForbid {
					assigned = true
				}
			}
		}
		return nil
	})
	return assigned, err
}

// A standard is imposed by an enabled allow and lifted by an enabled forbid: the opposite of every
// source whose allow grants access, so the second approver the posture gate asks for stands on
// the other side. Lifting a standard, or taking one off a department, is the relaxation; assigning
// one, to any number of departments, is not.
func imposesStandard(b bindingDTO) bool {
	return b.Enabled && normalizeEffect(b.Effect) != effectForbid
}
func liftsStandard(b bindingDTO) bool { return b.Enabled && normalizeEffect(b.Effect) == effectForbid }

func classifyStandardsCreate(created bindingDTO) (bool, string) {
	if liftsStandard(created) {
		return true, "forbid added (a standard lifted from a department or group)"
	}
	return false, ""
}

func classifyStandardsUpdate(old, updated bindingDTO) (bool, string) {
	moved := scopeOf(old) != scopeOf(updated)
	switch {
	case imposesStandard(old) && (!imposesStandard(updated) || moved):
		return true, "an assigned standard stops applying (disabled, turned into a forbid or moved off its department or group)"
	case liftsStandard(updated) && (!liftsStandard(old) || moved):
		return true, "forbid added or moved (a standard lifted from a department or group)"
	}
	return false, ""
}

func classifyStandardsDelete(deleted bindingDTO) (bool, string) {
	if imposesStandard(deleted) {
		return true, "assigned standard removed (a department or group stops being given it)"
	}
	return false, ""
}

// DepartmentExists reports whether department is a node of the organization tree.
func (r *Resolver) DepartmentExists(ctx context.Context, tenant model.TenantID, department string) (bool, error) {
	data := r.m.moduleData()
	if data == nil {
		return false, errNotReady
	}
	var found bool
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		_, found, err = findWorkspaceBySlug(ctx, sc, department)
		return err
	})
	return found, err
}

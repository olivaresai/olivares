// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// What a department carries: its instructions and its default session template.
//
// Both are session templates, assigned by a binding like any other source: "instructions" names a
// template whose custom instructions are the text a launch is given, and "template" names the
// template a launch starts from when it names none (effect allow: a default, which a launch may
// replace) or must run under (effect require: mandatory, which a launch may not replace). A department is a node of the organization
// tree (a workspace) or a directory group, the two ways the product groups people, and the
// assignment obeys the tree it is made on. An assignment on a department reaches its
// sub-departments, one on a directory group reaches its members and the groups nested under it
// (the principal's groups already hold that closure), a forbid on a sub-department or a group is
// absolute, a decline on a sub-department stops one standard reaching it from the departments above
// (what it or a department below assigns itself still applies), and a disabled binding assigns nothing.
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

// kindRequiredTemplate is the question "which templates are mandatory?" put to AssignedTo: the
// templates assigned with effect require, which a launch under the department must run under
// and may not replace by naming another. A template assigned with effect allow is only the
// default of a launch that names none. A forbid lifts either, and a decline lifts what a department above assigned.
const kindRequiredTemplate = "required-template"

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

// checkStandardsScope refuses a binding of the department's standards, or of an approval policy or
// DLP ruleset, on anything but the organization tree or a directory group: selection reads no other.
func checkStandardsScope(sourceType, scopeTree string) string {
	if isStandardsSource(sourceType) && scopeTree != scopeWorkspace && scopeTree != scopeUserGroup {
		return "instructions and templates are assigned to a department or a directory group: scope_tree must be workspace or user_group"
	}
	if isPolicySource(sourceType) && scopeTree != scopeWorkspace && scopeTree != scopeUserGroup {
		return "approval policies and DLP rulesets are assigned to a department or a directory group: scope_tree must be workspace or user_group"
	}
	return ""
}

// refuseSecondDefaultTemplate keeps one default template per department or group: a second
// enabled allow on the same one is a 409, so a department or group never holds two. (A person in
// several departments and groups can still reach several; the last in the order of AssignedTo
// governs: a group's after a department's, a deeper department's after its parent's.) in.ScopeRef is already canonical (resolveScope ran); self is the binding being
// changed, "" for a create.
func refuseSecondDefaultTemplate(ctx context.Context, sc store.Scope, in bindingDTO, self string) error {
	if in.SourceType != sourceTemplate || !in.Enabled || liftsEffect(normalizeEffect(in.Effect)) {
		return nil
	}
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceTemplate), eq(colScopeTree, in.ScopeTree), eq(colScopeRef, in.ScopeRef))
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.String(model.ColID) == self || !rec.Bool(colEnabled) || liftsEffect(normalizeEffect(rec.String(colEffect))) {
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
// as an empty department. kindRequiredTemplate lists only the templates assigned as mandatory.
func (r *Resolver) AssignedTo(ctx context.Context, tenant model.TenantID, principal auth.Principal, departments []string, sourceType string) ([]string, error) {
	if !isStandardsSource(sourceType) && sourceType != kindRequiredTemplate {
		return nil, errNotAStandard
	}
	storedType, onlyRequired := sourceType, sourceType == kindRequiredTemplate
	if onlyRequired {
		storedType = sourceTemplate
	}
	data := r.m.moduleData()
	if data == nil {
		return nil, errNotReady
	}
	var out []string
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		standings, _, err := assignedIn(ctx, sc, r.m.clock.Now().Time(), departments, principal.GroupsIn(tenant), storedType, onlyRequired)
		for _, s := range standings {
			out = append(out, s.Ref)
		}
		return err
	})
	return out, err
}

// Standing is one source assigned to a person or a department and the binding nearest to them
// that assigns it: a department (ScopeTree workspace, ScopeRef its slug) or a directory group
// (ScopeTree user_group, ScopeRef its id).
type Standing struct {
	Ref       string
	ScopeTree string
	ScopeRef  string
}

// PolicyAssignedIn answers, inside the caller's transaction, which approval-policy or dlp-ruleset
// sources reach the departments (slugs) and directory groups, in AssignedTo's order: the last
// stands. named is every source an active binding assigns to a department or group, so a caller
// keeps the policies no department or group was given tenant-wide. Any other source type is refused.
func PolicyAssignedIn(ctx context.Context, sc store.Scope, now time.Time, departments, groups []string, sourceType string) (standings []Standing, named map[string]bool, err error) {
	if !isPolicySource(sourceType) {
		return nil, nil, errNotAPolicy
	}
	standings, active, err := assignedIn(ctx, sc, now, departments, groups, sourceType, false)
	if err != nil {
		return nil, nil, err
	}
	named = map[string]bool{}
	for _, rec := range active {
		tree := rec.String(colScopeTree)
		if rec.Bool(colEnabled) && !liftsEffect(normalizeEffect(rec.String(colEffect))) && (tree == scopeWorkspace || tree == scopeUserGroup) {
			named[rec.String(colSourceRef)] = true
		}
	}
	return standings, named, nil
}

// errNotAPolicy answers a policy question about a kind of source that is not a policy.
var errNotAPolicy = errors.New("sourcescope: only approval-policy and dlp-ruleset are policies")

// assignedIn is AssignedTo inside the caller's transaction for one stored source type. It also
// returns the unexpired bindings of that type it decided from.
func assignedIn(ctx context.Context, sc store.Scope, now time.Time, departments, groups []string, storedType string, onlyRequired bool) ([]Standing, []model.Record, error) {
	// One lineage per department: the department and every department above it, by depth.
	var lineages []map[string]int
	for _, department := range departments {
		if department == "" {
			continue
		}
		ws, found, err := findWorkspaceBySlug(ctx, sc, department)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
		}
		slug, above, err := store.WorkspaceLineage(ctx, sc, ws.ID)
		if err != nil {
			return nil, nil, err
		}
		depth := map[string]int{slug: len(above)}
		for i, ancestor := range above {
			depth[ancestor] = i
		}
		lineages = append(lineages, depth)
	}
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, storedType))
	if err != nil {
		return nil, nil, err
	}
	active := make([]model.Record, 0, len(recs))
	for _, rec := range recs {
		if onlyRequired && normalizeEffect(rec.String(colEffect)) == effectAllow {
			continue // a default, not a mandatory template
		}
		expired, err := bindingExpired(toBindingDTO(rec), now)
		if err != nil {
			return nil, nil, err
		}
		if !expired {
			active = append(active, rec)
		}
	}
	return assignedAlong(lineages, groups, active, onlyRequired), active, nil
}

// assignedAlong decides which sources the departments and the directory groups carry from the
// stored bindings of one source type. Each lineage is one department and the departments above it
// (workspace slug to depth). A source is carried when an enabled allow contains a department or
// group, unless an enabled forbid does, which wins at any depth, or an enabled decline on a
// department of the same lineage deeper than where the allow was made, which drops only what
// reached that department from above: a decline never reaches another department of the person.
// A source stands where it was assigned nearest, so the default template a launch takes is the
// last; with outermostFirst (the mandatory templates) it stands where it was assigned farthest
// out, so requiring a parent's template again nearer never moves it behind another requirement.
func assignedAlong(lineages []map[string]int, groups []string, recs []model.Record, outermostFirst bool) []Standing {
	if len(lineages) == 0 {
		lineages = []map[string]int{nil} // in no department: what the groups carry
	}
	nearest, outermost := map[string]standingHit{}, map[string]standingHit{}
	forbidden := map[string]bool{}
	for _, depth := range lineages {
		type allow struct {
			ref string
			hit standingHit
		}
		var allows []allow
		declinedAt := map[string]int{}
		for _, rec := range recs {
			if !rec.Bool(colEnabled) {
				continue
			}
			ref, at := rec.String(colSourceRef), rec.String(colScopeRef)
			hit := standingHit{created: rec.String(model.ColCreatedAt), at: at}
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
			switch normalizeEffect(rec.String(colEffect)) {
			case effectForbid:
				forbidden[ref] = true
				continue
			case effectDecline:
				if d, ok := declinedAt[ref]; !ok || hit.depth > d {
					declinedAt[ref] = hit.depth
				}
				continue
			}
			allows = append(allows, allow{ref, hit})
		}
		for _, a := range allows {
			if d, declined := declinedAt[a.ref]; declined && a.hit.tree == 0 && a.hit.depth < d {
				continue // reached the declining department from above it
			}
			if cur, ok := nearest[a.ref]; !ok || a.hit.after(cur) {
				nearest[a.ref] = a.hit
			}
			if cur, ok := outermost[a.ref]; !ok || cur.after(a.hit) {
				outermost[a.ref] = a.hit
			}
		}
	}
	order := nearest
	if outermostFirst {
		order = outermost
	}
	var out []Standing
	for ref, hit := range order {
		if !forbidden[ref] {
			tree := scopeWorkspace
			if hit.tree == 1 {
				tree = scopeUserGroup
			}
			out = append(out, Standing{Ref: ref, ScopeTree: tree, ScopeRef: hit.at})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := order[out[i].Ref], order[out[j].Ref]
		if fi.tree != fj.tree || fi.depth != fj.depth || fi.created != fj.created {
			return fj.after(fi)
		}
		return out[i].Ref < out[j].Ref
	})
	return out
}

// standingHit is where a binding reached a caller, and so where it stands in the order a caller
// is given standards.
type standingHit struct {
	tree    int // 0 a department, 1 a directory group
	depth   int // of the department in the organization tree, root first
	created string
	at      string // the department slug or group id of the binding
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
				if rec.Bool(colEnabled) && !liftsEffect(normalizeEffect(rec.String(colEffect))) {
					assigned = true
				}
			}
		}
		return nil
	})
	return assigned, err
}

// A standard is imposed by an enabled allow and lifted by an enabled forbid or decline: the opposite
// of every source whose allow grants access, so the second approver the posture gate asks for stands
// on the other side. Lifting a standard, or taking one off a department, is the relaxation; assigning
// one, to any number of departments, is not.
func liftsEffect(effect string) bool { return effect == effectForbid || effect == effectDecline }

func imposesStandard(b bindingDTO) bool {
	return b.Enabled && !liftsEffect(normalizeEffect(b.Effect))
}
func liftsStandard(b bindingDTO) bool { return b.Enabled && liftsEffect(normalizeEffect(b.Effect)) }

func classifyStandardsCreate(created bindingDTO) (bool, string) {
	if liftsStandard(created) {
		return true, "forbid or decline added (a standard lifted from a department or group)"
	}
	return false, ""
}

func classifyStandardsUpdate(old, updated bindingDTO) (bool, string) {
	moved := scopeOf(old) != scopeOf(updated)
	switch {
	case imposesStandard(old) && (!imposesStandard(updated) || moved):
		return true, "an assigned standard stops applying (disabled, turned into a forbid or decline or moved off its department or group)"
	case old.Enabled && normalizeEffect(old.Effect) == effectRequire && imposesStandard(updated) && normalizeEffect(updated.Effect) != effectRequire:
		return true, "a mandatory template becomes a default that a launch may replace"
	case liftsStandard(updated) && (!liftsStandard(old) || moved):
		return true, "forbid or decline added or moved (a standard lifted from a department or group)"
	case liftsStandard(updated) && normalizeEffect(updated.Effect) == effectForbid && normalizeEffect(old.Effect) != effectForbid:
		return true, "a decline becomes a forbid (it also lifts what the department and the ones below assign themselves)"
	}
	return false, ""
}

func classifyStandardsDelete(deleted bindingDTO) (bool, string) {
	if imposesStandard(deleted) {
		return true, "assigned standard removed (a department or group stops being given it)"
	}
	return false, ""
}

// adrFolderSetting is the key of a department's settings (PATCH /v1/workspaces/{id}) that points
// its instructions at the folder of its architecture decision records.
const adrFolderSetting = "adr_folder"

// ErrSettingNotText is what ADRFolder answers for a setting that holds something other than text.
var ErrSettingNotText = errors.New("sourcescope: the setting is not text")

// ADRFolder is the folder the department points its instructions at, "" when it names none or does
// not exist. A value that is not text is ErrSettingNotText, so a mistyped setting is never read as none.
func (r *Resolver) ADRFolder(ctx context.Context, tenant model.TenantID, department string) (string, error) {
	data := r.m.moduleData()
	if data == nil {
		return "", errNotReady
	}
	var folder string
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		ws, found, err := findWorkspaceBySlug(ctx, sc, department)
		if err != nil || !found {
			return err
		}
		switch v := ws.Settings[adrFolderSetting].(type) {
		case nil:
		case string:
			folder = v
		default:
			return fmt.Errorf("%w: %s of department %s", ErrSettingNotText, adrFolderSetting, department)
		}
		return nil
	})
	return folder, err
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

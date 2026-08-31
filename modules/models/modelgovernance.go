// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// Claude model-access governance (FASE X). This is the AUTHORING surface of
// "which users/groups/agent-groups may use which Claude models (or model-groups) in
// which workspaces, on which surface, under which budget". It owns two tenant-resident
// entities and their CRUD; the ENFORCEMENT (the deny-closed decision wired into the
// routing select/execute chain, and the reusable in-band seam an proxy consults)
// lives in modelaccessgate.go.
//
// It SPECIALIZES the generic model→scope binding (docs/contracts): the
// binding answers "which workspace OWNS this model source"; a model-access grant answers
// "which SUBJECT may USE this model/model-group, where, on which surface". The two
// compose at execute (both deny-closed). It does NOT add a third authorization engine
// nor touch the verified Cedar core: per-model, agent-group-as-subject and surface
// are dimensions the current Cedar grant row cannot express, so they are
// decided here over these owned tables, composing the SAME primitives uses
// (principal identity containment via the actor-scope resolver, budget).
//
// MODEL-GROUP definition is HYBRID (2026-06-15): a tenant-named set whose members
// are explicit model refs AND/OR catalog selectors (family / access-tier), resolved
// against the declared reference catalog (lookupReference, longest-prefix). So a group
// like "frontier" can list claude-opus-4-8 explicitly and also select the whole
// "claude-opus" family, and "glasswing-only" can select the access tier.

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Entity kinds for model governance.
const (
	modelGroupKind  model.Kind = "models.model_group"
	modelAccessKind model.Kind = "models.model_access"
)

// Physical tables for entities.
const (
	modelGroupTable  = "models_model_group"
	modelAccessTable = "models_model_access"
)

// model_group columns. Selector lists are stored as JSON string arrays (marshalStrings/
// parseStrings) in a single TEXT column — the same codec the admission entities use.
const (
	colMGName     = "name"
	colMGMembers  = "member_refs"
	colMGFamilies = "family_selectors"
	colMGTiers    = "tier_selectors"
	colMGDesc     = "descr"
)

// model_access columns.
const (
	colMASubjectKind = "subject_kind"
	colMASubjectRef  = "subject_ref"
	colMATargetKind  = "target_kind"
	colMATargetRef   = "target_ref"
	colMAWorkspace   = "workspace_ref"
	colMASurfaces    = "surfaces"
	colMABudgetRef   = "budget_ref"
	colMADesc        = "descr"
	// colMAEffect is the allow/forbid dimension. Appended last (expand-only,
	// nullable): a legacy/empty value is an ALLOW, so every pre grant keeps its
	// meaning, and the boot reconcile (reconcileColumns, sqlstore) adds it on an
	// already-migrated DB — no numbered SQL migration. It is NOT part of the unique index
	// (changing the index WOULD need a migration). Consequence: the index keys on
	// (subject,target,workspace) only — it already excluded `surfaces` PRE — so two
	// rules that differ ONLY by effect (or only by surfaces) on the same
	// (subject,target,workspace) collide (409). For the same-effect-same-surface case this
	// is correct (an allow and a forbid on the identical tuple are contradictory; the
	// forbid would win anyway). For a surface-differentiated pair (allow on surface A,
	// forbid on surface B) it is a real but PRE-EXISTING expressiveness limit of this index
	// (two allows differing only by surface already collided); express it instead with a
	// distinct target_ref, or rely on the in-band surface enforcement. The common forbid —
	// a narrower target than the allow — is a distinct tuple and coexists.
	colMAEffect = "effect"
)

// model-access effect: allow (positive grant) or forbid (override that SUBTRACTS).
const (
	effectAllow  = "allow" // the default (an empty stored value is also an allow)
	effectForbid = "forbid"
)

// model-access subject kinds: WHO a grant names.
const (
	subjectUser       = "user"        // a specific user id
	subjectRole       = "role"        // a tenant BUILT-IN role (owner/admin/editor/viewer)
	subjectUserGroup  = "user_group"  // an S256 DIRECTORY group id (the user's SCIM/IdP group)
	subjectAgentGroup = "agent_group" // an agent-group slug (the acting agent's group)
)

// model-access target kinds: WHAT a grant authorizes.
const (
	targetModel      = "model"       // a single model ref (exact or prefix, dated-suffix tolerant)
	targetModelGroup = "model_group" // a named model-group (by name)
)

var (
	maSubjectKinds = set(subjectUser, subjectRole, subjectUserGroup, subjectAgentGroup)
	maTargetKinds  = set(targetModel, targetModelGroup)
)

// registerModelGovernanceSchema registers two owned entities. Each unique index
// leads with tenant_id so it can never couple tenants.
func registerModelGovernanceSchema(reg store.ExtensionRegistry) error {
	descs := []model.EntityDescriptor{
		{
			Kind: modelGroupKind, Table: modelGroupTable,
			Fields: []model.FieldSpec{
				{Name: colMGName, Kind: model.KindText, Indexed: true},
				{Name: colMGMembers, Kind: model.KindJSON, Nullable: true},
				{Name: colMGFamilies, Kind: model.KindJSON, Nullable: true},
				{Name: colMGTiers, Kind: model.KindJSON, Nullable: true},
				{Name: colMGDesc, Kind: model.KindText, Nullable: true},
			},
			// One group name per tenant.
			Indexes: []model.IndexSpec{{Name: "models_model_group_uniq", Columns: []string{model.ColTenantID, colMGName}, Unique: true}},
		},
		{
			Kind: modelAccessKind, Table: modelAccessTable,
			Fields: []model.FieldSpec{
				{Name: colMASubjectKind, Kind: model.KindText, Indexed: true},
				{Name: colMASubjectRef, Kind: model.KindText, Indexed: true},
				{Name: colMATargetKind, Kind: model.KindText},
				{Name: colMATargetRef, Kind: model.KindText, Indexed: true},
				{Name: colMAWorkspace, Kind: model.KindText, Nullable: true},
				{Name: colMASurfaces, Kind: model.KindJSON, Nullable: true},
				{Name: colMABudgetRef, Kind: model.KindText, Nullable: true},
				{Name: colMADesc, Kind: model.KindText, Nullable: true},
				// Appended last (expand-only): Allow/forbid effect, nullable.
				{Name: colMAEffect, Kind: model.KindText, Nullable: true},
			},
			// One grant per (subject, target, workspace) — re-asserting the same tuple is
			// a 409, not a duplicate row. workspace_ref "" is the tenant-wide grant.
			Indexes: []model.IndexSpec{{
				Name:    "models_model_access_uniq",
				Columns: []string{model.ColTenantID, colMASubjectKind, colMASubjectRef, colMATargetKind, colMATargetRef, colMAWorkspace},
				Unique:  true,
			}},
		},
	}
	for _, d := range descs {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

// --- model_group DTO + handlers ----------------------------------------------

// modelGroupDTO is a tenant-named set of models. Its membership is HYBRID: explicit
// model refs PLUS catalog selectors (families / access tiers). A model ref M belongs to
// the group iff it is listed in Members (exact or prefix), or its declared family is in
// Families, or its declared access tier is in Tiers (lookupReference, longest-prefix).
type modelGroupDTO struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Members     []string `json:"member_refs"`
	Families    []string `json:"family_selectors"`
	Tiers       []string `json:"tier_selectors"`
	Description string   `json:"description,omitempty"`
}

// cleanStrings trims, drops empties and de-duplicates a selector list (stable order).
func cleanStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = trimClamp(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (d *modelGroupDTO) validate() string {
	d.Name = trimClamp(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	d.Members = cleanStrings(d.Members)
	d.Families = cleanStrings(d.Families)
	d.Tiers = cleanStrings(d.Tiers)
	if len(d.Members) == 0 && len(d.Families) == 0 && len(d.Tiers) == 0 {
		return "a model-group needs at least one member_ref, family_selector or tier_selector"
	}
	d.Description = trimClamp(d.Description)
	return ""
}

func (d modelGroupDTO) toRecord() model.Record {
	return model.Record{
		colMGName:     d.Name,
		colMGMembers:  marshalStrings(d.Members),
		colMGFamilies: marshalStrings(d.Families),
		colMGTiers:    marshalStrings(d.Tiers),
		colMGDesc:     d.Description,
	}
}

func toModelGroupDTO(rec model.Record) modelGroupDTO {
	return modelGroupDTO{
		ID:          rec.String(model.ColID),
		Name:        rec.String(colMGName),
		Members:     emptyIfNil(parseStrings(rec.String(colMGMembers))),
		Families:    emptyIfNil(parseStrings(rec.String(colMGFamilies))),
		Tiers:       emptyIfNil(parseStrings(rec.String(colMGTiers))),
		Description: rec.String(colMGDesc),
	}
}

// emptyIfNil keeps the JSON arrays non-null in responses (a UI renders [] not null).
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (m *Module) handleListModelGroups(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[modelGroupDTO]{Items: []modelGroupDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toModelGroupDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateModelGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in modelGroupDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out modelGroupDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toModelGroupDTO(rec)
		return auditOwned(r.Context(), sc, mc, modelGroupKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleGetModelGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out modelGroupDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toModelGroupDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleUpdateModelGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in modelGroupDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out modelGroupDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// The group NAME is immutable (it is the reference model-access grants resolve);
		// renaming would silently orphan every target_ref pointing at the old name.
		in.Name = rec.String(colMGName)
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toModelGroupDTO(rec)
		return auditOwned(r.Context(), sc, mc, modelGroupKind, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteModelGroup deletes a model-group, but REFUSES (409) while any model-access
// grant still targets it — symmetric with create-time validateTargetGroup, which blocks a
// grant to a non-existent group. Without this, deleting a group out from under its grants
// would silently confine the named subjects to an empty set (every targetMatches lookup
// misses ⇒ deny-all), a deny-closed but invisible foot-gun. The operator must delete or
// re-point the referencing grants first.
func (m *Module) handleDeleteModelGroup(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var referenced bool
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelGroupKind)
		if err != nil {
			return err
		}
		grp, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		name := grp.String(colMGName)
		grants, err := drainExt(r.Context(), sc, modelAccessKind)
		if err != nil {
			return err
		}
		for _, g := range grants {
			if g.String(colMATargetKind) == targetModelGroup && g.String(colMATargetRef) == name {
				referenced = true
				return nil
			}
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditOwned(r.Context(), sc, mc, modelGroupKind, "delete", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if referenced {
		writeJSON(w, http.StatusConflict, errorBody("model-group is referenced by a model-access grant; delete or re-point those grants first"))
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- model_access DTO + handlers ---------------------------------------------

// modelAccessDTO is a model-access RULE: SUBJECT (user|role|user_group|agent_group) may (allow) or
// may NOT (forbid) use TARGET (a model ref or a model-group name) in WORKSPACE (empty =
// tenant-wide) on SURFACES (empty = all), optionally referencing a BUDGET (defense-in-
// depth, the enforcing cap stays the FinOps budget gate). EFFECT defaults to allow
// (empty ⇒ allow). An allow is a positive grant: a subject NAMED by any allow is confined
// to its allows (deny-closed), a subject named by none is unrestricted (subject-scoped,
// back-compat). A forbid SUBTRACTS: it overrides any allow for the subjects it
// names (forbid-overrides-allow, deny-closed) — see modelaccessgate.go.
type modelAccessDTO struct {
	ID           string   `json:"id,omitempty"`
	SubjectKind  string   `json:"subject_kind"`
	SubjectRef   string   `json:"subject_ref"`
	TargetKind   string   `json:"target_kind"`
	TargetRef    string   `json:"target_ref"`
	WorkspaceRef string   `json:"workspace_ref,omitempty"`
	Surfaces     []string `json:"surfaces"`
	BudgetRef    string   `json:"budget_ref,omitempty"`
	Effect       string   `json:"effect,omitempty"` // "allow" (default) | "forbid"
	Description  string   `json:"description,omitempty"`
}

func (d *modelAccessDTO) validate() string {
	d.SubjectKind = strings.TrimSpace(d.SubjectKind)
	if !maSubjectKinds[d.SubjectKind] {
		return "subject_kind must be user, role, user_group or agent_group"
	}
	d.SubjectRef = trimClamp(d.SubjectRef)
	if d.SubjectRef == "" {
		return "subject_ref is required"
	}
	d.TargetKind = strings.TrimSpace(d.TargetKind)
	if !maTargetKinds[d.TargetKind] {
		return "target_kind must be model or model_group"
	}
	d.TargetRef = trimClamp(d.TargetRef)
	if d.TargetRef == "" {
		return "target_ref is required"
	}
	d.WorkspaceRef = trimClamp(d.WorkspaceRef)
	d.BudgetRef = trimClamp(d.BudgetRef)
	d.Description = trimClamp(d.Description)
	// Effect normalizes to allow|forbid; an empty value is an allow (back-compat).
	d.Effect = strings.TrimSpace(strings.ToLower(d.Effect))
	if d.Effect == "" {
		d.Effect = effectAllow
	}
	if d.Effect != effectAllow && d.Effect != effectForbid {
		return "effect must be allow or forbid"
	}
	d.Surfaces = cleanStrings(d.Surfaces)
	for _, s := range d.Surfaces {
		// Surface is the model.Gateway vocabulary. Gateway is an OPEN string, but an
		// authored CONSTRAINT that names an unseeded surface is almost always a typo that
		// would silently never match a real request — so reject it at authoring time with
		// the valid set (the live data path still accepts unseeded surfaces; see).
		if !sdkmodel.Gateway(s).Valid() {
			return "surface must be one of: direct, bedrock-mantle, bedrock-legacy, vertex, foundry, claude-platform-aws"
		}
	}
	return ""
}

func (d modelAccessDTO) toRecord() model.Record {
	return model.Record{
		colMASubjectKind: d.SubjectKind, colMASubjectRef: d.SubjectRef,
		colMATargetKind: d.TargetKind, colMATargetRef: d.TargetRef,
		colMAWorkspace: d.WorkspaceRef, colMASurfaces: marshalStrings(d.Surfaces),
		colMABudgetRef: d.BudgetRef, colMADesc: d.Description, colMAEffect: d.Effect,
	}
}

func toModelAccessDTO(rec model.Record) modelAccessDTO {
	return modelAccessDTO{
		ID:          rec.String(model.ColID),
		SubjectKind: rec.String(colMASubjectKind), SubjectRef: rec.String(colMASubjectRef),
		TargetKind: rec.String(colMATargetKind), TargetRef: rec.String(colMATargetRef),
		WorkspaceRef: rec.String(colMAWorkspace), Surfaces: emptyIfNil(parseStrings(rec.String(colMASurfaces))),
		BudgetRef: rec.String(colMABudgetRef), Effect: normalizeEffect(rec.String(colMAEffect)), Description: rec.String(colMADesc),
	}
}

// normalizeEffect maps a stored effect to its canonical value: an empty/legacy value is
// an allow (the pre default), so a response always reads a concrete effect.
func normalizeEffect(stored string) string {
	if stored == effectForbid {
		return effectForbid
	}
	return effectAllow
}

// validateGrantRefs verifies a grant's referential targets exist in the tenant
// (catching typos that would otherwise create a silently-dead grant): a
// target_kind=model_group must name an existing group, and a non-empty workspace_ref must
// name an existing workspace (the reserved "default" slug is always valid — it is the
// scope of unassigned agents). Runs in the same write transaction as the create/
// update so the check and the row are consistent.
func validateGrantRefs(r *http.Request, sc store.Scope, d modelAccessDTO) (bad bool, msg string, err error) {
	if d.TargetKind == targetModelGroup {
		grp, lookupErr := findModelGroupByName(r.Context(), sc, d.TargetRef)
		if lookupErr != nil {
			return false, "", lookupErr
		}
		if grp == nil {
			return true, "target_ref names no model-group (create the group first)", nil
		}
	}
	if d.WorkspaceRef != "" && d.WorkspaceRef != model.DefaultWorkspaceSlug {
		ok, lookupErr := workspaceSlugExists(r.Context(), sc, d.WorkspaceRef)
		if lookupErr != nil {
			return false, "", lookupErr
		}
		if !ok {
			return true, "workspace_ref names no workspace in this tenant", nil
		}
	}
	return false, "", nil
}

// workspaceSlugExists reports whether a workspace with the given slug exists in the
// current tenant scope (mirrors sourcescope's findWorkspaceBySlug).
func workspaceSlugExists(ctx context.Context, sc store.Scope, slug string) (bool, error) {
	ws, _, err := sc.Workspaces().List(ctx, model.Query{Filters: []model.Filter{eq("slug", slug)}, Limit: 1})
	if err != nil {
		return false, err
	}
	return len(ws) > 0, nil
}

func (m *Module) handleListModelAccess(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("subject_kind"); v != "" {
		q.Filters = append(q.Filters, eq(colMASubjectKind, v))
	}
	if v := r.URL.Query().Get("subject_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colMASubjectRef, v))
	}
	if v := r.URL.Query().Get("target_kind"); v != "" {
		q.Filters = append(q.Filters, eq(colMATargetKind, v))
	}
	if v := r.URL.Query().Get("target_ref"); v != "" {
		q.Filters = append(q.Filters, eq(colMATargetRef, v))
	}
	out := listResponse[modelAccessDTO]{Items: []modelAccessDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(modelAccessKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toModelAccessDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateModelAccess(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in modelAccessDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out modelAccessDTO
		bad string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if isBad, msg, verr := validateGrantRefs(r, sc, in); verr != nil {
			return verr
		} else if isBad {
			bad = msg
			return nil
		}
		repo, err := sc.Ext(modelAccessKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.toRecord())
		if err != nil {
			return err
		}
		out = toModelAccessDTO(rec)
		return auditOwned(r.Context(), sc, mc, modelAccessKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if bad != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(bad))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleUpdateModelAccess(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in modelAccessDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out modelAccessDTO
		bad string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if isBad, msg, verr := validateGrantRefs(r, sc, in); verr != nil {
			return verr
		} else if isBad {
			bad = msg
			return nil
		}
		repo, err := sc.Ext(modelAccessKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		for k, v := range in.toRecord() {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toModelAccessDTO(rec)
		return auditOwned(r.Context(), sc, mc, modelAccessKind, "update", id)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if bad != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(bad))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleDeleteModelAccess(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.deleteExt(w, r, mc, modelAccessKind)
}

// --- shared read helpers (used by the authoring handlers AND the enforcement gate) ---

// findModelGroupByName returns the model-group row with the given name in the current
// tenant scope, or nil if none. Group names are unique per tenant (the unique index).
func findModelGroupByName(ctx context.Context, sc store.Scope, name string) (model.Record, error) {
	repo, err := sc.Ext(modelGroupKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colMGName, name)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return recs[0], nil
}

// drainExt resolves an owned entity repo and delegates its complete tenant-scoped page
// walk to the shared helper (grant/group sets are small; the resolve path needs all).
func drainExt(ctx context.Context, sc store.Scope, kind model.Kind) ([]model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	return listAllExt(ctx, repo)
}

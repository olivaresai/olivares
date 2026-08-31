// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Console CRUD for the scoping plane (workspaces, agent-groups). Shipped
// the entities and their store CRUD but no HTTP surface adds it so the
// console can manage them. The handlers mirror the agent CRUD pattern
// (handlers_core.go): authzTenant for collection routes, authzTenantEntity for
// entity routes (so an active scoped grant resolves the entity's true scope), a
// committed Mutate with a single semantic audit event per write. Privileged,
// privilege-shaped actions (creating/archiving a workspace) additionally require
// AAL3 step-up (requireAAL3) — operational edits (renaming an agent-group) do not.

// slugPattern bounds a tenant-unique slug to a short, URL-safe handle.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// --- Workspaces --------------------------------------------------------------

// WorkspaceDTO is the JSON shape of a workspace.
type WorkspaceDTO struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenant_id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	Status    string         `json:"status"`
	IsDefault bool           `json:"is_default"`
	Settings  map[string]any `json:"settings,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	Version   int64          `json:"version"`
}

func toWorkspaceDTO(w model.Workspace) WorkspaceDTO {
	return WorkspaceDTO{
		ID: w.ID.String(), TenantID: w.TenantID.String(), Name: w.Name, Slug: w.Slug,
		Status: string(w.Status), IsDefault: w.Slug == model.DefaultWorkspaceSlug, Settings: w.Settings,
		CreatedAt: w.CreatedAt.String(), UpdatedAt: w.UpdatedAt.String(), Version: w.Version,
	}
}

// createWorkspaceInput is the create payload for a workspace.
type createWorkspaceInput struct {
	Name     string         `json:"name"`
	Slug     string         `json:"slug"`
	Settings map[string]any `json:"settings"`
}

// updateWorkspaceInput is the PATCH payload. Slug is immutable (it is the stable
// scope handle referenced by grants); only name/status/settings change.
type updateWorkspaceInput struct {
	Name     *string         `json:"name"`
	Status   *string         `json:"status"`
	Settings *map[string]any `json:"settings"`
}

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "tenant:read")
	if !ok {
		return
	}
	q := parseListQuery(r)
	// B-03: a workspace-confined caller sees ONLY its own workspace. The axis
	// here is not a workspace_id column — a workspace does not carry one, it IS the
	// node — so the forced predicate is on the row's own id. Applying the generic
	// workspace filter here would filter on a column this table does not have.
	//
	// Until now this route returned every workspace of the tenant to a confined
	// operator: names, slugs and ids of the scopes it may not act in. That is the
	// same reconnaissance leak the module routes had, on the core side of the
	// house, and the confinement decision that already governs the rest of the
	// tenant said the operator may act ONLY within its own workspace.
	if confinedWS, confined := p.ConfinedWorkspaceIn(tenant); confined && !p.Superadmin {
		q.Filters = append(q.Filters, model.Filter{
			Column: model.ColID, Op: model.OpEq, Value: confinedWS.String(),
		})
	}
	var out listResponse[WorkspaceDTO]
	out.Items = []WorkspaceDTO{}
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		wss, page, err := sc.Workspaces().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, ws := range wss {
			out.Items = append(out.Items, toWorkspaceDTO(ws))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "tenant:read", id)
	if !ok {
		return
	}
	// B-03: naming another workspace by id is ErrNotFound, not a distinguishable
	// refusal — otherwise the route stays an oracle for the ids the list no longer
	// enumerates.
	if confinedWS, confined := p.ConfinedWorkspaceIn(tenant); confined && !p.Superadmin && id != confinedWS {
		s.writeError(w, r, store.ErrNotFound)
		return
	}
	var dto WorkspaceDTO
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toWorkspaceDTO(ws)
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleCreateWorkspace creates a workspace. Creating one is OWNER-only (decision
//): a workspace-admin administers WITHIN a workspace, it does not
// mint them. So beyond the tenant:admin RBAC gate we require the caller to be the
// tenant owner (or a superadmin), and — as a privilege-shaped action — AAL3.
func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "tenant:admin")
	if !ok {
		return
	}
	if !s.requireOwner(w, r, p, tenant) {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	var in createWorkspaceInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if !slugPattern.MatchString(in.Slug) {
		s.badRequest(w, r, "slug must match [a-z0-9][a-z0-9-]* (max 63 chars)")
		return
	}
	if in.Slug == model.DefaultWorkspaceSlug {
		s.badRequest(w, r, "slug \"default\" is reserved")
		return
	}
	if in.Name == "" {
		s.badRequest(w, r, "name is required")
		return
	}
	var dto WorkspaceDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		created, err := sc.Workspaces().Create(r.Context(), model.Workspace{
			Name: in.Name, Slug: in.Slug, Status: model.StatusActive, Settings: in.Settings,
		})
		if err != nil {
			return err
		}
		dto = toWorkspaceDTO(created)
		return appendAudit(r.Context(), sc, p, "workspace.create", "core.workspace", created.ID)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// handleUpdateWorkspace renames a workspace, edits its settings, or archives it
// (Status inactive). The default workspace cannot be archived (it is the
// resolution target for an unset WorkspaceID). Slug is immutable. AAL3-gated.
func (s *Server) handleUpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "tenant:admin", id)
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	var in updateWorkspaceInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Status != nil && *in.Status != string(model.StatusActive) && *in.Status != string(model.StatusInactive) {
		s.badRequest(w, r, "status must be active or inactive")
		return
	}
	var dto WorkspaceDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Get(r.Context(), id)
		if err != nil {
			return err
		}
		if in.Name != nil {
			ws.Name = *in.Name
		}
		if in.Settings != nil {
			ws.Settings = *in.Settings
		}
		if in.Status != nil {
			if ws.Slug == model.DefaultWorkspaceSlug && *in.Status == string(model.StatusInactive) {
				return errBadRequest // the default workspace cannot be archived
			}
			ws.Status = model.LifecycleStatus(*in.Status)
		}
		updated, err := sc.Workspaces().Update(r.Context(), ws)
		if err != nil {
			return err
		}
		dto = toWorkspaceDTO(updated)
		return appendAudit(r.Context(), sc, p, "workspace.update", "core.workspace", id)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// --- Agent-groups ------------------------------------------------------------

// AgentGroupDTO is the JSON shape of an agent-group.
type AgentGroupDTO struct {
	ID          string         `json:"id"`
	TenantID    string         `json:"tenant_id"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Description string         `json:"description,omitempty"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Version     int64          `json:"version"`
}

func toAgentGroupDTO(g model.AgentGroup) AgentGroupDTO {
	return AgentGroupDTO{
		ID: g.ID.String(), TenantID: g.TenantID.String(), WorkspaceID: idOrEmpty(g.WorkspaceID),
		Name: g.Name, Slug: g.Slug, Description: g.Description, Status: string(g.Status),
		Metadata: g.Metadata, CreatedAt: g.CreatedAt.String(), UpdatedAt: g.UpdatedAt.String(), Version: g.Version,
	}
}

// agentGroupInput is the CREATE payload for an agent-group.
type agentGroupInput struct {
	WorkspaceID string         `json:"workspace_id"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Metadata    map[string]any `json:"metadata"`
}

// updateAgentGroupInput is the PATCH payload: every field is a pointer so an
// omitted field is left untouched (a partial update must never wipe description
// or metadata). Slug and workspace are immutable on update (the slug is the
// stable scope handle; re-homing a group between workspaces is not supported in
// v1 — it would move every scoped grant that targets the group).
type updateAgentGroupInput struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Status      *string         `json:"status"`
	Metadata    *map[string]any `json:"metadata"`
	// WorkspaceID re-scopes the group to a workspace: a set value confines it
	// to that workspace, an explicit "" clears the scope back to tenant-wide. Absent
	// (nil) leaves the current scope untouched — the console edit form needs this to
	// change a group's workspace, which create supports but update previously dropped.
	WorkspaceID *string `json:"workspace_id"`
}

// AgentGroupMemberDTO is the JSON shape of one (group → agent) membership.
type AgentGroupMemberDTO struct {
	ID      string `json:"id"`
	GroupID string `json:"group_id"`
	AgentID string `json:"agent_id"`
}

func (s *Server) handleListAgentGroups(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "agent:read")
	if !ok {
		return
	}
	confinedWS, _ := p.ConfinedWorkspaceIn(tenant)
	var out listResponse[AgentGroupDTO]
	out.Items = []AgentGroupDTO{}
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		gs, page, err := sc.AgentGroups().List(r.Context(), parseFilteredListQuery(r, confinedWS))
		if err != nil {
			return err
		}
		for _, g := range gs {
			out.Items = append(out.Items, toAgentGroupDTO(g))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetAgentGroup(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	// F3: authorize against the GROUP entity (not the "agent" kind the permission implies)
	// so the scoped engine derives the group's workspace and confinement applies.
	_, tenant, ok := s.authzTenantEntityKind(w, r, "agent:read", "agent_group", id)
	if !ok {
		return
	}
	var dto AgentGroupDTO
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		g, err := sc.AgentGroups().Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = toAgentGroupDTO(g)
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateAgentGroup(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "agent:write")
	if !ok {
		return
	}
	var in agentGroupInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if !slugPattern.MatchString(in.Slug) {
		s.badRequest(w, r, "slug must match [a-z0-9][a-z0-9-]* (max 63 chars)")
		return
	}
	if in.Name == "" {
		s.badRequest(w, r, "name is required")
		return
	}
	var dto AgentGroupDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		if err := s.validateWorkspaceRef(r, sc, in.WorkspaceID); err != nil {
			return err
		}
		status := model.LifecycleStatus(in.Status)
		if status == "" {
			status = model.StatusActive
		}
		created, err := sc.AgentGroups().Create(r.Context(), model.AgentGroup{
			WorkspaceID: model.ID(in.WorkspaceID), Name: in.Name, Slug: in.Slug,
			Description: in.Description, Status: status, Metadata: in.Metadata,
		})
		if err != nil {
			return err
		}
		dto = toAgentGroupDTO(created)
		return appendAudit(r.Context(), sc, p, "agent_group.create", "core.agent_group", created.ID)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) handleUpdateAgentGroup(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", id)
	if !ok {
		return
	}
	var in updateAgentGroupInput
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	if in.Status != nil && *in.Status != string(model.StatusActive) && *in.Status != string(model.StatusInactive) {
		s.badRequest(w, r, "status must be active or inactive")
		return
	}
	var dto AgentGroupDTO
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		g, err := sc.AgentGroups().Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Partial update: only fields present in the request are touched.
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Description != nil {
			g.Description = *in.Description
		}
		if in.Metadata != nil {
			g.Metadata = *in.Metadata
		}
		if in.Status != nil {
			g.Status = model.LifecycleStatus(*in.Status)
		}
		if in.WorkspaceID != nil {
			// Same deny-closed workspace check as create: an unknown ref surfaces as
			// ErrNotFound (404), an empty ref clears the scope to tenant-wide.
			if err := s.validateWorkspaceRef(r, sc, *in.WorkspaceID); err != nil {
				return err
			}
			g.WorkspaceID = model.ID(*in.WorkspaceID)
		}
		updated, err := sc.AgentGroups().Update(r.Context(), g)
		if err != nil {
			return err
		}
		dto = toAgentGroupDTO(updated)
		return appendAudit(r.Context(), sc, p, "agent_group.update", "core.agent_group", id)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// handleDeleteAgentGroup deletes a group and its roster (the membership rows),
// never the member agents themselves.
func (s *Server) handleDeleteAgentGroup(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", id)
	if !ok {
		return
	}
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		if _, err := sc.AgentGroups().Get(r.Context(), id); err != nil {
			return err
		}
		// Drena: borrar «lo que quepa» dejaria filas apuntando a un grupo inexistente.
		members, err := drainGroupRoster(r, sc, id)
		if err != nil {
			return err
		}
		for _, m := range members {
			if err := sc.AgentGroupMembers().Delete(r.Context(), m.ID); err != nil {
				return err
			}
		}
		if err := sc.AgentGroups().Delete(r.Context(), id); err != nil {
			return err
		}
		return appendAudit(r.Context(), sc, p, "agent_group.delete", "core.agent_group", id)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListAgentGroupMembers(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	// F3: authorize against the GROUP entity so the scoped engine derives the group's
	// workspace — otherwise a workspace-confined operator reads a cross-workspace group.
	_, tenant, ok := s.authzTenantEntityKind(w, r, "agent:read", "agent_group", id)
	if !ok {
		return
	}
	out := listResponse[AgentGroupMemberDTO]{Items: []AgentGroupMemberDTO{}}
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		if _, err := sc.AgentGroups().Get(r.Context(), id); err != nil {
			return err
		}
		// Pagina como sus vecinos de este mismo fichero (`:74`, `:317`): respeta el `limit`
		// y el `cursor` del llamante y DECLARA el recorte, en vez de servir un tope mudo.
		q := parseListQuery(r)
		q.Filters = append(q.Filters, model.Filter{
			Column: "group_id", Op: model.OpEq, Value: id.String(),
		})
		members, page, err := sc.AgentGroupMembers().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, m := range members {
			out.Items = append(out.Items, AgentGroupMemberDTO{
				ID: m.ID.String(), GroupID: m.GroupID.String(), AgentID: m.AgentID.String(),
			})
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAddAgentGroupMember adds an agent to a group. Idempotent: if the agent is
// already a member the existing row is returned (200), a fresh add returns 201.
func (s *Server) handleAddAgentGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := model.ID(chi.URLParam(r, "id"))
	agentID := model.ID(chi.URLParam(r, "agentID"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", groupID)
	if !ok {
		return
	}
	var (
		dto     AgentGroupMemberDTO
		created bool
	)
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		if _, err := sc.AgentGroups().Get(r.Context(), groupID); err != nil {
			return err
		}
		if _, err := sc.Agents().Get(r.Context(), agentID); err != nil {
			return err // a non-existent agent is a 404, not a dangling membership
		}
		// La idempotencia se decide con una consulta EXACTA: buscarla dentro de una lista
		// recortada convertia «no lo veo» en «no existe», y creaba un duplicado.
		existing, found, err := groupMemberRow(r, sc, groupID, agentID)
		if err != nil {
			return err
		}
		if found {
			dto = AgentGroupMemberDTO{
				ID: existing.ID.String(), GroupID: existing.GroupID.String(),
				AgentID: existing.AgentID.String(),
			}
			return nil // already a member (idempotent)
		}
		m, err := sc.AgentGroupMembers().Create(r.Context(), model.AgentGroupMember{GroupID: groupID, AgentID: agentID})
		if err != nil {
			return err
		}
		created = true
		dto = AgentGroupMemberDTO{ID: m.ID.String(), GroupID: m.GroupID.String(), AgentID: m.AgentID.String()}
		return appendAudit(r.Context(), sc, p, "agent_group.member.add", "core.agent_group", groupID)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, dto)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleRemoveAgentGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := model.ID(chi.URLParam(r, "id"))
	agentID := model.ID(chi.URLParam(r, "agentID"))
	p, tenant, ok := s.authzTenantEntity(w, r, "agent:write", groupID)
	if !ok {
		return
	}
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		if _, err := sc.AgentGroups().Get(r.Context(), groupID); err != nil {
			return err
		}
		// Exacta: con la lista recortada, un miembro real mas alla del corte se respondia 404.
		row, found, err := groupMemberRow(r, sc, groupID, agentID)
		if err != nil {
			return err
		}
		if !found {
			return store.ErrNotFound // not a member
		}
		if err := sc.AgentGroupMembers().Delete(r.Context(), row.ID); err != nil {
			return err
		}
		return appendAudit(r.Context(), sc, p, "agent_group.member.remove", "core.agent_group", groupID)
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Workspace summary -------------------------------------------------------

// WorkspaceSummaryDTO is the JSON shape of a workspace's entity counts.
type WorkspaceSummaryDTO struct {
	WorkspaceID   string `json:"workspace_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	IsDefault     bool   `json:"is_default"`
	AgentCount    int    `json:"agent_count"`
	SessionCount  int    `json:"session_count"`
	ResourceCount int    `json:"resource_count"`
	GroupCount    int    `json:"group_count"`
	// The *Capped flags say the matching count is a FLOOR, not a total.
	//
	// These counts are derived from a List page, and the store clamps any page to
	// maxLimit (1000) — it SUBSTITUTES the limit, which is its documented contract
	// and is correct. What was not correct was throwing away the model.Page that
	// says so: a truncated LIST is visible (rows, plus a "see all"), but a
	// truncated COUNT is not. A tenant with 1001 agents and one with 50000 both
	// rendered "1000", and nothing on the screen differed.
	//
	// So the page travels now. When a flag is true the console must read the number
	// as "at least N", never as a total.
	AgentCountCapped    bool `json:"agent_count_capped"`
	SessionCountCapped  bool `json:"session_count_capped"`
	ResourceCountCapped bool `json:"resource_count_capped"`
	GroupCountCapped    bool `json:"group_count_capped"`
}

// handleWorkspaceSummary returns a workspace with counts of its scoped entities.
func (s *Server) handleWorkspaceSummary(w http.ResponseWriter, r *http.Request) {
	id := model.ID(chi.URLParam(r, "id"))
	_, tenant, ok := s.authzTenantEntity(w, r, "tenant:read", id)
	if !ok {
		return
	}
	var dto WorkspaceSummaryDTO
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto.WorkspaceID = ws.ID.String()
		dto.Name = ws.Name
		dto.Slug = ws.Slug
		dto.IsDefault = ws.Slug == model.DefaultWorkspaceSlug

		// Limit is deliberately above the store's maxLimit: asking for more than a
		// page can hold is how this endpoint says "as many as you will give me".
		// The store clamps it and reports the truncation in the model.Page, which
		// is why every List below keeps its page instead of discarding it.
		wsFilter := model.Query{
			Filters: []model.Filter{{Column: "workspace_id", Op: model.OpEq, Value: ws.ID.String()}},
			Limit:   10000,
		}

		agents, agentPage, err := sc.Agents().List(r.Context(), wsFilter)
		if err != nil {
			return err
		}
		dto.AgentCount = len(agents)
		dto.AgentCountCapped = agentPage.HasMore

		sessions, sessionPage, err := sc.Sessions().List(r.Context(), wsFilter)
		if err != nil {
			return err
		}
		dto.SessionCount = len(sessions)
		dto.SessionCountCapped = sessionPage.HasMore

		resources, resourcePage, err := sc.Resources().List(r.Context(), wsFilter)
		if err != nil {
			return err
		}
		dto.ResourceCount = len(resources)
		dto.ResourceCountCapped = resourcePage.HasMore

		groups, groupPage, err := sc.AgentGroups().List(r.Context(), wsFilter)
		if err != nil {
			return err
		}
		dto.GroupCount = len(groups)
		dto.GroupCountCapped = groupPage.HasMore

		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// --- helpers -----------------------------------------------------------------

// requireOwner enforces that the caller is the tenant owner (or a superadmin),
// the bar for actions reserved to ownership (e.g. minting a workspace). It writes
// a 403 and returns false otherwise. It runs AFTER the RBAC gate.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID) bool {
	if p.Superadmin {
		return true
	}
	if role, ok := p.RoleIn(tenant); ok && role == auth.RoleOwner {
		return true
	}
	s.writeError(w, r, errForbidden)
	return false
}

// ⛔ AQUI HABIA UN SOLO `groupRoster` QUE DECIA PAGINAR Y CORTABA. Su comentario afirmaba
//    «It paginates to a bounded ceiling», pero pedia `Limit: 1000` de una sola vez y ataba el
//    `Page` a `_`: ni paginaba ni declaraba el corte. La preocupacion que lo motivo era legitima
//    —un grupo enorme no debe ser un amplificador de memoria— y se conserva; lo que cambia es que
//    cada llamante haga la pregunta que de verdad tiene, en vez de compartir una lista recortada:
//
//      · LISTAR   -> pagina de verdad y declara `has_more` (lo hace el propio handler)
//      · ALTA/BAJA-> consulta EXACTA por (group_id, agent_id): no carga la lista
//      · BORRAR   -> drena, y falla en vez de borrar a medias
//
//    Con el helper unico, un grupo de mas de mil miembros producia: idempotencia rota en el alta
//    (duplicado si el miembro existente caia fuera del corte), 404 falso en la baja, un
//    `has_more:false` que la consola no podia desmentir, y —lo peor— un BORRADO INCOMPLETO que
//    dejaba filas de pertenencia apuntando a un grupo que ya no existe. Eso ultimo no es una lista
//    corta: es corrupcion silenciosa.

// groupMemberRow finds ONE membership row by (group_id, agent_id) with an exact query.
//
// ⛔ EXACTA, NO UN BARRIDO. Preguntar «¿es este agente miembro?» cargando la lista entera y
//
//	buscando dentro es correcto solo mientras la lista quepa: en cuanto se recorta, la respuesta
//	NEGATIVA deja de significar «no es miembro» y pasa a significar «no estaba en la parte que
//	mire». Las dos operaciones que dependian de eso —el alta idempotente y la baja— construian
//	su veredicto sobre esa ambiguedad.
func groupMemberRow(
	r *http.Request, sc store.Scope, groupID, agentID model.ID,
) (model.AgentGroupMember, bool, error) {
	rows, _, err := sc.AgentGroupMembers().List(r.Context(), model.Query{
		Filters: []model.Filter{
			{Column: "group_id", Op: model.OpEq, Value: groupID.String()},
			{Column: "agent_id", Op: model.OpEq, Value: agentID.String()},
		},
		Limit: 2, // uno basta; el segundo delata un duplicado que este mismo defecto pudo crear
	})
	if err != nil {
		return model.AgentGroupMember{}, false, err
	}
	if len(rows) == 0 {
		return model.AgentGroupMember{}, false, nil
	}
	return rows[0], true, nil
}

// drainGroupRoster returns EVERY membership row of a group, or an error.
//
// ⛔ FAIL-CLOSED, y es la unica forma honesta para un BORRADO: devolver una lista parcial haria
//
//	que el llamante borrase lo que ve y dejase el resto huerfano, en silencio. Si la travesia no
//	puede completarse —cursor vacio con `HasMore`, cursor repetido, o mas paginas de las
//	previstas— esto devuelve error y la transaccion entera se deshace. Es la misma postura que
//	`drainList` en core/auth, que el motor ya usa para el roster de inquilino.
func drainGroupRoster(
	r *http.Request, sc store.Scope, groupID model.ID,
) ([]model.AgentGroupMember, error) {
	const pageSize, maxPages = 1000, 100
	q := model.Query{
		Filters: []model.Filter{{Column: "group_id", Op: model.OpEq, Value: groupID.String()}},
		Limit:   pageSize,
	}
	var out []model.AgentGroupMember
	seen := make(map[string]struct{}, maxPages)
	for i := 0; i < maxPages; i++ {
		rows, page, err := sc.AgentGroupMembers().List(r.Context(), q)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, errGroupRosterIncomplete
		}
		if _, dup := seen[page.Cursor]; dup {
			return nil, errGroupRosterIncomplete
		}
		seen[page.Cursor] = struct{}{}
		q.Cursor = page.Cursor
	}
	return nil, errGroupRosterIncomplete
}

// errGroupRosterIncomplete says the traversal could not be completed. It is never a partial
// result: a caller that deletes what it can see would corrupt the rest.
var errGroupRosterIncomplete = errors.New("group roster traversal incomplete")

// validateWorkspaceRef rejects an agent-group bound to a non-existent workspace
// (a dangling scope). An empty ref is valid (resolves to the default workspace).
func (s *Server) validateWorkspaceRef(r *http.Request, sc store.Scope, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	_, err := sc.Workspaces().Get(r.Context(), model.ID(workspaceID))
	return err
}

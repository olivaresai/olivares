// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Causal fact kinds the K3 audience validator expects for indirect
// membership (modules/sessions/communication_state.go validateAudienceCausality).
const (
	directoryFactMembership       model.Kind = "core.membership"
	directoryFactAgent            model.Kind = "core.agent"
	directoryFactUserGroupMember  model.Kind = "core.user_group_member"
	directoryFactAgentGroupMember model.Kind = "core.agent_group_member"

	// directoryReadPageSize and directoryReadBound cap every enumeration this
	// projection performs. A roster past the bound is UNKNOWN, not truncated:
	// a partial workspace roster would silently drop recipients.
	directoryReadPageSize = 200
	directoryReadBound    = 2000
)

// directoryUserWitness is the typed projection of one User as a K3 principal
// of a tenant/workspace: existence, account status and a covering membership
// (tenant-wide or scoped to the workspace). It carries no email or hash.
type directoryUserWitness struct {
	Found          bool
	Active         bool
	Member         bool
	Version        int64
	MembershipFact store.AuthorizationFactRef
}

// directoryAgentWitness projects one canonical Agent recipient (its stable
// Identity id) inside a workspace: the Identity exists, at least one Agent row
// binds it in the effective workspace, one of them is active, and governance
// still considers the identity eligible.
type directoryAgentWitness struct {
	IdentityFound     bool
	IdentityVersion   int64
	ExternalID        string
	BoundInWorkspace  bool
	Active            bool
	LifecycleEligible bool
	AgentFact         store.AuthorizationFactRef
}

// directoryGroupMember is one indirect membership arc: the recipient's
// canonical ref and the exact row that proves the relation.
type directoryGroupMember struct {
	Ref  model.ID
	Fact store.AuthorizationFactRef
}

type directoryUserGroupWitness struct {
	Found   bool
	Members []directoryGroupMember
}

type directoryAgentGroupWitness struct {
	Found       bool
	InWorkspace bool
	Active      bool
	Members     []directoryGroupMember
}

type directoryWorkspaceMembersWitness struct {
	Users  []directoryGroupMember
	Agents []directoryGroupMember
}

// communicationDirectoryReads is the engine-owned narrow read closure set the
// K3 directory adapter consumes. Each closure opens its own bounded View over
// the core Store (auth partition for users/groups, tenant Scope for
// agents/identities/workspaces/agent groups) or asks the session plane for a
// typed witness. The adapter never receives a store.Scope or an AuthScope.
type communicationDirectoryReads struct {
	user              func(context.Context, model.TenantID, model.ID, model.ID) (directoryUserWitness, error)
	userGroup         func(context.Context, model.TenantID, model.ID) (directoryUserGroupWitness, error)
	userGroups        func(context.Context, model.TenantID, model.ID) ([]model.ID, error)
	agent             func(context.Context, model.TenantID, model.ID, model.ID) (directoryAgentWitness, error)
	agentByExternalID func(context.Context, model.TenantID, string) (model.ID, bool, error)
	agentGroup        func(context.Context, model.TenantID, model.ID, model.ID) (directoryAgentGroupWitness, error)
	agentGroups       func(context.Context, model.TenantID, model.ID, model.ID) ([]model.ID, error)
	workspaceMembers  func(context.Context, model.TenantID, model.ID) (directoryWorkspaceMembersWitness, error)
	session           func(context.Context, model.TenantID, model.ID, string) (sessions.CommunicationSessionRecipientWitness, error)
}

// newCommunicationDirectoryReads binds the closures over the boot store. The
// lifecycle plane is the same governance seam the K1 identity resolver uses:
// an agent whose NHI lifecycle no longer sponsors it is not a live recipient.
func newCommunicationDirectoryReads(
	st store.Store,
	sm *sessions.Module,
	lifecycle workAgentLifecycle,
) *communicationDirectoryReads {
	if st == nil || sm == nil {
		return nil
	}
	return &communicationDirectoryReads{
		user: func(ctx context.Context, tenant model.TenantID, workspace, userID model.ID) (directoryUserWitness, error) {
			return readDirectoryUser(ctx, st, tenant, workspace, userID)
		},
		userGroup: func(ctx context.Context, tenant model.TenantID, groupID model.ID) (directoryUserGroupWitness, error) {
			return readDirectoryUserGroup(ctx, st, tenant, groupID)
		},
		userGroups: func(ctx context.Context, tenant model.TenantID, userID model.ID) ([]model.ID, error) {
			return readDirectoryUserGroups(ctx, st, tenant, userID)
		},
		agent: func(ctx context.Context, tenant model.TenantID, workspace, identityID model.ID) (directoryAgentWitness, error) {
			return readDirectoryAgent(ctx, st, lifecycle, tenant, workspace, identityID)
		},
		agentByExternalID: func(ctx context.Context, tenant model.TenantID, externalID string) (model.ID, bool, error) {
			return readDirectoryAgentByExternalID(ctx, st, tenant, externalID)
		},
		agentGroup: func(ctx context.Context, tenant model.TenantID, workspace, groupID model.ID) (directoryAgentGroupWitness, error) {
			return readDirectoryAgentGroup(ctx, st, tenant, workspace, groupID)
		},
		agentGroups: func(ctx context.Context, tenant model.TenantID, workspace, identityID model.ID) ([]model.ID, error) {
			return readDirectoryAgentGroups(ctx, st, tenant, workspace, identityID)
		},
		workspaceMembers: func(ctx context.Context, tenant model.TenantID, workspace model.ID) (directoryWorkspaceMembersWitness, error) {
			return readDirectoryWorkspaceMembers(ctx, st, tenant, workspace)
		},
		session: func(ctx context.Context, tenant model.TenantID, workspace model.ID, sid string) (sessions.CommunicationSessionRecipientWitness, error) {
			return sm.CommunicationSessionRecipient(ctx, tenant, workspace, sid)
		},
	}
}

func directoryListAll[T any](
	ctx context.Context,
	list func(context.Context, model.Query) ([]T, model.Page, error),
	filters []model.Filter,
) ([]T, error) {
	query := model.Query{Filters: filters, Limit: directoryReadPageSize}
	var out []T
	for {
		rows, page, err := list(ctx, query)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(out) > directoryReadBound {
			return nil, fmt.Errorf("%w: directory enumeration exceeds %d rows",
				store.ErrDirectoryUnavailable, directoryReadBound)
		}
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, fmt.Errorf("%w: directory enumeration lost its continuation",
				store.ErrDirectoryUnavailable)
		}
		query.Cursor = page.Cursor
	}
}

func readDirectoryUser(
	ctx context.Context, st store.Store, tenant model.TenantID, workspace, userID model.ID,
) (directoryUserWitness, error) {
	var out directoryUserWitness
	err := st.AuthView(ctx, func(sc store.AuthScope) error {
		user, err := sc.Users().Get(ctx, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		out.Found, out.Active, out.Version = true, user.Status == model.StatusActive, user.Version
		memberships, err := directoryListAll(ctx, sc.Memberships().List, []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: userID.String()},
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		})
		if err != nil {
			return err
		}
		// A tenant-wide membership covers every workspace; prefer it as the
		// causal fact, then the exact workspace scope, deterministically by id.
		sort.Slice(memberships, func(i, j int) bool {
			if memberships[i].WorkspaceID.IsZero() != memberships[j].WorkspaceID.IsZero() {
				return memberships[i].WorkspaceID.IsZero()
			}
			return memberships[i].ID.String() < memberships[j].ID.String()
		})
		for _, membership := range memberships {
			if membership.WorkspaceID.IsZero() || membership.WorkspaceID == workspace {
				out.Member = true
				out.MembershipFact = store.AuthorizationFactRef{
					Kind: directoryFactMembership, ID: membership.ID, Version: membership.Version,
				}
				break
			}
		}
		return nil
	})
	return out, err
}

func readDirectoryUserGroup(
	ctx context.Context, st store.Store, tenant model.TenantID, groupID model.ID,
) (directoryUserGroupWitness, error) {
	var out directoryUserGroupWitness
	err := st.AuthView(ctx, func(sc store.AuthScope) error {
		group, err := sc.Groups().Get(ctx, groupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		// Groups share the system tenant; the column, not RLS, isolates
		// tenants. Another tenant's group is absent here, never enumerated.
		if group.TargetTenantID != tenant {
			return nil
		}
		out.Found = true
		members, err := directoryListAll(ctx, sc.GroupMembers().List, []model.Filter{
			{Column: "group_id", Op: model.OpEq, Value: groupID.String()},
		})
		if err != nil {
			return err
		}
		for _, member := range members {
			out.Members = append(out.Members, directoryGroupMember{
				Ref: member.UserID,
				Fact: store.AuthorizationFactRef{
					Kind: directoryFactUserGroupMember, ID: member.ID, Version: member.Version,
				},
			})
		}
		return nil
	})
	return out, err
}

func readDirectoryUserGroups(
	ctx context.Context, st store.Store, tenant model.TenantID, userID model.ID,
) ([]model.ID, error) {
	var out []model.ID
	err := st.AuthView(ctx, func(sc store.AuthScope) error {
		members, err := directoryListAll(ctx, sc.GroupMembers().List, []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		})
		if err != nil {
			return err
		}
		seen := make(map[model.ID]struct{}, len(members))
		for _, member := range members {
			if _, done := seen[member.GroupID]; done {
				continue
			}
			seen[member.GroupID] = struct{}{}
			group, err := sc.Groups().Get(ctx, member.GroupID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return err
			}
			if group.TargetTenantID == tenant {
				out = append(out, group.ID)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, err
}

// effectiveWorkspace resolves the FASE X back-compat rule: an unset workspace
// means the tenant's default workspace.
func effectiveWorkspace(ctx context.Context, sc store.Scope, id model.ID, cache *model.ID) (model.ID, error) {
	if !id.IsZero() {
		return id, nil
	}
	if cache != nil && !cache.IsZero() {
		return *cache, nil
	}
	ws, err := sc.DefaultWorkspace(ctx)
	if err != nil {
		return "", err
	}
	if cache != nil {
		*cache = ws.ID
	}
	return ws.ID, nil
}

func readDirectoryAgent(
	ctx context.Context, st store.Store, lifecycle workAgentLifecycle,
	tenant model.TenantID, workspace, identityID model.ID,
) (directoryAgentWitness, error) {
	var out directoryAgentWitness
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		identity, err := sc.Identities().Get(ctx, identityID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		out.IdentityFound, out.IdentityVersion, out.ExternalID = true, identity.Version, identity.ExternalID
		agents, err := directoryListAll(ctx, sc.Agents().List, []model.Filter{
			{Column: "identity_id", Op: model.OpEq, Value: identityID.String()},
		})
		if err != nil {
			return err
		}
		sort.Slice(agents, func(i, j int) bool { return agents[i].ID.String() < agents[j].ID.String() })
		var defaultWS model.ID
		for _, agent := range agents {
			agentWorkspace, err := effectiveWorkspace(ctx, sc, agent.WorkspaceID, &defaultWS)
			if err != nil {
				return err
			}
			if agentWorkspace != workspace {
				continue
			}
			out.BoundInWorkspace = true
			if agent.Status == model.StatusActive && !out.Active {
				out.Active = true
				out.AgentFact = store.AuthorizationFactRef{
					Kind: directoryFactAgent, ID: agent.ID, Version: agent.Version,
				}
			}
		}
		return nil
	})
	if err != nil || !out.Active {
		return out, err
	}
	if lifecycle == nil {
		return out, fmt.Errorf("%w: agent lifecycle plane is not wired", store.ErrDirectoryUnavailable)
	}
	if out.ExternalID == "" {
		// An identity without an external reference has no lifecycle record to
		// sponsor it; the K1 resolver treats it the same way.
		return out, nil
	}
	eligible, err := lifecycle.AgentEligibleForWork(ctx, tenant, out.ExternalID)
	if err != nil {
		return out, err
	}
	out.LifecycleEligible = eligible
	return out, nil
}

func readDirectoryAgentByExternalID(
	ctx context.Context, st store.Store, tenant model.TenantID, externalID string,
) (model.ID, bool, error) {
	if externalID == "" {
		return "", false, nil
	}
	var id model.ID
	found := false
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		identities, page, err := sc.Identities().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "external_id", Op: model.OpEq, Value: externalID}},
			Limit:   2,
		})
		if err != nil {
			return err
		}
		if page.HasMore || len(identities) > 1 {
			return fmt.Errorf("%w: agent external id is ambiguous", store.ErrDirectoryUnavailable)
		}
		if len(identities) == 1 {
			id, found = identities[0].ID, true
		}
		return nil
	})
	return id, found, err
}

func readDirectoryAgentGroup(
	ctx context.Context, st store.Store, tenant model.TenantID, workspace, groupID model.ID,
) (directoryAgentGroupWitness, error) {
	var out directoryAgentGroupWitness
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		group, err := sc.AgentGroups().Get(ctx, groupID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		out.Found = true
		var defaultWS model.ID
		groupWorkspace, err := effectiveWorkspace(ctx, sc, group.WorkspaceID, &defaultWS)
		if err != nil {
			return err
		}
		out.InWorkspace = groupWorkspace == workspace
		out.Active = group.Status == model.StatusActive
		if !out.InWorkspace {
			return nil
		}
		members, err := directoryListAll(ctx, sc.AgentGroupMembers().List, []model.Filter{
			{Column: "group_id", Op: model.OpEq, Value: groupID.String()},
		})
		if err != nil {
			return err
		}
		sort.Slice(members, func(i, j int) bool { return members[i].ID.String() < members[j].ID.String() })
		for _, member := range members {
			agent, err := sc.Agents().Get(ctx, member.AgentID)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return err
			}
			if agent.IdentityID.IsZero() {
				continue
			}
			out.Members = append(out.Members, directoryGroupMember{
				Ref: agent.IdentityID,
				Fact: store.AuthorizationFactRef{
					Kind: directoryFactAgentGroupMember, ID: member.ID, Version: member.Version,
				},
			})
		}
		return nil
	})
	return out, err
}

func readDirectoryAgentGroups(
	ctx context.Context, st store.Store, tenant model.TenantID, workspace, identityID model.ID,
) ([]model.ID, error) {
	var out []model.ID
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		agents, err := directoryListAll(ctx, sc.Agents().List, []model.Filter{
			{Column: "identity_id", Op: model.OpEq, Value: identityID.String()},
		})
		if err != nil {
			return err
		}
		var defaultWS model.ID
		seen := make(map[model.ID]struct{})
		for _, agent := range agents {
			agentWorkspace, err := effectiveWorkspace(ctx, sc, agent.WorkspaceID, &defaultWS)
			if err != nil {
				return err
			}
			if agentWorkspace != workspace {
				continue
			}
			members, err := directoryListAll(ctx, sc.AgentGroupMembers().List, []model.Filter{
				{Column: "agent_id", Op: model.OpEq, Value: agent.ID.String()},
			})
			if err != nil {
				return err
			}
			for _, member := range members {
				if _, done := seen[member.GroupID]; done {
					continue
				}
				seen[member.GroupID] = struct{}{}
				group, err := sc.AgentGroups().Get(ctx, member.GroupID)
				if err != nil {
					if errors.Is(err, store.ErrNotFound) {
						continue
					}
					return err
				}
				groupWorkspace, err := effectiveWorkspace(ctx, sc, group.WorkspaceID, &defaultWS)
				if err != nil {
					return err
				}
				if groupWorkspace == workspace && group.Status == model.StatusActive {
					out = append(out, group.ID)
				}
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, err
}

func readDirectoryWorkspaceMembers(
	ctx context.Context, st store.Store, tenant model.TenantID, workspace model.ID,
) (directoryWorkspaceMembersWitness, error) {
	var out directoryWorkspaceMembersWitness
	if err := st.AuthView(ctx, func(sc store.AuthScope) error {
		memberships, err := directoryListAll(ctx, sc.Memberships().List, []model.Filter{
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		})
		if err != nil {
			return err
		}
		sort.Slice(memberships, func(i, j int) bool {
			if memberships[i].WorkspaceID.IsZero() != memberships[j].WorkspaceID.IsZero() {
				return memberships[i].WorkspaceID.IsZero()
			}
			return memberships[i].ID.String() < memberships[j].ID.String()
		})
		seen := make(map[model.ID]struct{}, len(memberships))
		for _, membership := range memberships {
			if !membership.WorkspaceID.IsZero() && membership.WorkspaceID != workspace {
				continue
			}
			if _, done := seen[membership.UserID]; done {
				continue
			}
			seen[membership.UserID] = struct{}{}
			out.Users = append(out.Users, directoryGroupMember{
				Ref: membership.UserID,
				Fact: store.AuthorizationFactRef{
					Kind: directoryFactMembership, ID: membership.ID, Version: membership.Version,
				},
			})
		}
		return nil
	}); err != nil {
		return directoryWorkspaceMembersWitness{}, err
	}
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		agents, err := directoryListAll(ctx, sc.Agents().List, nil)
		if err != nil {
			return err
		}
		sort.Slice(agents, func(i, j int) bool { return agents[i].ID.String() < agents[j].ID.String() })
		var defaultWS model.ID
		seen := make(map[model.ID]struct{}, len(agents))
		for _, agent := range agents {
			if agent.IdentityID.IsZero() || agent.Status != model.StatusActive {
				continue
			}
			agentWorkspace, err := effectiveWorkspace(ctx, sc, agent.WorkspaceID, &defaultWS)
			if err != nil {
				return err
			}
			if agentWorkspace != workspace {
				continue
			}
			if _, done := seen[agent.IdentityID]; done {
				continue
			}
			seen[agent.IdentityID] = struct{}{}
			out.Agents = append(out.Agents, directoryGroupMember{
				Ref: agent.IdentityID,
				Fact: store.AuthorizationFactRef{
					Kind: directoryFactAgent, ID: agent.ID, Version: agent.Version,
				},
			})
		}
		return nil
	})
	return out, err
}

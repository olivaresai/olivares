// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// This file declares the scoping entities of the granular-authorization plane
// (FASE X). They sit on the axis
//
//	org/tenant → workspace → agent-group → agent → session → resource/folder
//
// and turn the previously FLAT, tenant-wide authorization surface into a tree
// the enterprise configures. They are first-class, scopable core entities, NOT
// a new isolation boundary: the hard multi-tenant frontier stays at org/tenant
// (RLS on Postgres, the tripwire triggers on SQLite). A Workspace is SOFT
// isolation — a scoping dimension within one tenant — so workspace_id is an
// ordinary column the access engine (Cedar) reads, never a second RLS
// predicate. Every entity here embeds BaseFields and is reached through the
// tenant-pinned Scope, exactly like Agent and Resource; none of them is a
// global principal, so (unlike User/UserGroup) they do NOT live in the system
// tenant.

// Workspace is a first-class container inside a tenant: the unit an enterprise
// scopes admins, agents, sessions and resources to (FASE X). It is SOFT
// isolation — it partitions for authorization and organization, not for tenancy
// — so every Workspace row carries the owning tenant_id like any other entity
// and the RLS/trigger guards isolate it on exactly the same terms.
//
// Each tenant has exactly one DEFAULT workspace, identified by the reserved Slug
// "default" (DefaultWorkspaceSlug). It is materialized when the tenant is
// provisioned (and back-filled for pre tenants), and it is what an entity
// with an unset WorkspaceID resolves to — the invariant that keeps a pre-FASE-X
// binary working identically: code that never sets a workspace creates rows with
// a zero WorkspaceID, which the store resolves to the tenant's default workspace
// rather than orphaning them.
type Workspace struct {
	BaseFields
	// Name is the human-readable workspace name (renamable, safe to display).
	Name string
	// Slug is a short, tenant-unique, URL-safe handle. The reserved value
	// DefaultWorkspaceSlug marks the tenant's default workspace and is never
	// reassigned.
	Slug string
	// Status is the workspace lifecycle state (active/inactive). An inactive
	// workspace is "archived": kept for reference and audit, not offered for new
	// scoping. Lifecycle is a Status, not a soft-delete, because deleting a
	// workspace would orphan the agents/sessions/resources scoped to it.
	Status LifecycleStatus
	// Settings is free-form, non-sensitive workspace configuration (no secrets,
	// docs/SECURITY-HARDENING.md).
	Settings map[string]any
}

// DefaultWorkspaceSlug is the reserved Slug of every tenant's default workspace.
// An entity whose WorkspaceID is unset belongs to the workspace bearing this
// slug; the store materializes exactly one such row per tenant. It is reserved:
// no second workspace in a tenant may take it (a tenant-unique slug index
// enforces this).
const DefaultWorkspaceSlug = "default"

// AgentGroup is a named collection of agents within a tenant, optionally scoped
// to one workspace (FASE X). It is the agent-side analog of UserGroup
// — a grouping the access engine targets with a single grant instead of
// enumerating agents — but, because an Agent is a tenant-resident business
// entity (not a global principal like a User), an AgentGroup lives in the
// business tenant and is reached through the tenant Scope, never the auth
// partition. Membership travels in AgentGroupMember rows, never inline.
type AgentGroup struct {
	BaseFields
	// WorkspaceID scopes the group to one workspace; unset (zero) means the
	// tenant's default workspace (back-compat resolution, see Workspace).
	WorkspaceID ID
	// Name is the human-readable group name (safe to display).
	Name string
	// Slug is a short, tenant-unique, URL-safe handle for stable references
	// (access-engine principals, console URLs).
	Slug string
	// Description is a short, non-sensitive description.
	Description string
	// Status is the group lifecycle state (active/inactive).
	Status LifecycleStatus
	// Metadata is free-form, non-sensitive context.
	Metadata map[string]any
}

// AgentGroupMember binds an Agent to an AgentGroup. Like the group it lives in
// the business tenant; (group, agent) is unique. A group's roster is enumerated
// by GroupID, an agent's groups by AgentID — the fold the access engine uses to
// expand a per-group grant to the agents it covers.
type AgentGroupMember struct {
	BaseFields
	// GroupID is the group this row belongs to.
	GroupID ID
	// AgentID is the member agent.
	AgentID ID
}

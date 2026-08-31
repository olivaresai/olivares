// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sourcescope is the FASE X source-scoping plane: it binds a
// CONNECTED SOURCE — an MCP server, a model, a provider, a knowledge base or a
// data source — to a workspace, an agent-group, or a folder/subtree of the
// Resource tree (the axis), and resolves, at the point an agent/session resolves
// that source, whether the actor is in scope and WHICH credential reference applies.
//
// Folder/subtree binding. A binding with scope_tree="folder" anchors a source
// under a Resource-tree node (scope_ref is the folder's stable id). It carries NO
// containment of its own — an actor is not a tree node, so no actor-in-tree
// membership is invented; access is decided purely by the PER-ENTITY grant/forbid
// over that subtree (the engine resolves the folder's live materialized-Path ancestors,
// so a grant on the folder OR an ancestor authorizes — downward inheritance) plus the
// same tenant RBAC. It reuses the verified Resource anchor + grant exactly as
// §7 sanctioned; it is additive and never claims to be an unbypassable boundary.
//
// Why a module and not a column. The "scope" Must enforce is not a property
// of any one source entity (MCP config, model, provider and knowledge base live in
// different modules, and only Agent/Session/Resource carry a WorkspaceID at all —
// the source-scoping contract). It is a binding: (source) → (scope), with an optional
// scoped credential reference. This module owns that binding table, its write API
// (consumed by the console) and the resolver the runtime PEPs call.
//
// How it decides (model B — 2026-06-15). The binding alone CONFINES: a source
// bound to workspace W is resolvable by an agent/session in W with no further
// configuration (structural containment). The actor's scope is the workspace/groups of
// the agent/session NAMED by the caller's actor reference — a control-plane assertion
// of "on behalf of which agent" (the SAME agent-centric model the retrieval guard
// already uses), gated by the route permission. The scope VALUES are read from the
// stored row (a caller cannot inject a workspace directly), but the CHOICE of agent is
// the caller's; binding the reference to the authenticated principal's identity:
// when the auth.Principal carries an AgentIdentity, the resolver uses it
// instead of the caller-declared reference, closing the confused-deputy
// path. For a principal without AgentIdentity (legacy human/token path),
// the caller-declared reference is still used (the gate remains additive
// and never weakens the pre posture). Cedar grants are the cross-scope
// OVERRIDE (a `permit … when resource in Workspace::W` opens a foreign workspace),
// a scoped FORBID always narrows, and tenant-wide RBAC still sees everything
// (workspace is SOFT-isolation; the hard boundary is the tenant —). The
// decision is therefore three-valued ScopedAuthorizer plus the existing RBAC
// function — NOT a second authorization engine. An unbound source stays global
// (back-compat); a bound source with no containing scope, no grant and no RBAC is
// DENY-CLOSED.
//
// Scoped credentials are value-free REFERENCES (a logical name + ref_kind locator +
// masked hint), never values — the same minimal-data invariant as
// capabilities.mcp_config.secret_refs (docs/SECURITY-HARDENING.md). The resolver returns the
// scope's reference, never the global one, to an in-scope actor; minted/short-lived
// credentials and at-rest CMEK sealing of any persisted secret material are
// the documented follow-up (a value-free locator has nothing to seal).
//
// Boundary: AGPL-3.0-only (core plane). It imports core/{api,auth,model,store}; the
// Apache connectors never see it. See the source-scoping contract.
package sourcescope

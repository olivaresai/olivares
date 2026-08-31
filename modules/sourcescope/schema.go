// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Owned entity kinds and their physical tables.
const (
	bindingKind  model.Kind = "sourcescope.binding"
	bindingTable            = "sourcescope_binding"

	// workspace connector assignment — maps a deployment-global connector to a
	// workspace so a workspace admin controls which connectors their workspace sees.
	assignmentKind  model.Kind = "sourcescope.connector_assignment"
	assignmentTable            = "sourcescope_connector_assignment"

	// workspace-scoped connector definition — a connector created and owned by a
	// workspace admin, tenant-resident and confined to one workspace.
	wsConnectorKind  model.Kind = "sourcescope.workspace_connector"
	wsConnectorTable            = "sourcescope_workspace_connector"

	//-B: the KB retrieval-guard posture is an ORTHOGONAL axis from source
	// scoping. It records deliberate ACL-awareness downgrades for a knowledge base
	// (source_type=knowledge, source_ref=<kb name>) without touching workspace /
	// agent-group source-scope bindings.
	guardPostureKind  model.Kind = "sourcescope.guard_posture"
	guardPostureTable            = "sourcescope_guard_posture"

	// (F2/F5): a pending POSTURE-CHANGE request. A mutation that RELAXES enforcement
	// (widens who may reach a source, or weakens a restriction) is never applied by a
	// single actor: it is recorded here as pending and applied only when a SECOND, distinct
	// principal approves (dual-control), with the whole two-person trail in the audit ledger.
	// Tightening changes bypass this entirely (they apply immediately, audited).
	postureRequestKind  model.Kind = "sourcescope.posture_request"
	postureRequestTable            = "sourcescope_posture_request"
)

// Source types a binding may target. Each maps to a scope-grantable core kind
// (sourceKindToScopeable) so the cross-scope grant/forbid can be authored
// against the existing catalog (auth.ScopeableKinds) — no second catalog.
const (
	sourceMCP       = "mcp"       // an MCP server (by server_ref) → scopeable kind "mcp_server"
	sourceModel     = "model"     // a model (by model ref/name)   → "model"
	sourceProvider  = "provider"  // a model provider (by name)     → "provider"
	sourceKnowledge = "knowledge" // a knowledge base (by id)       → "resource"
	sourceData      = "data"      // a generic data source (by ref) → "resource"
)

// Exported source-type identifiers, for composition-root adapters building Resolve
// requests (the model ScopeGate, the knowledge RetrievalScopeGate) without magic
// strings.
const (
	SourceMCP       = sourceMCP
	SourceModel     = sourceModel
	SourceProvider  = sourceProvider
	SourceKnowledge = sourceKnowledge
	SourceData      = sourceData
)

// Scope-tree kinds a binding is bounded to. Two families (ADR-0022):
//
//   - CONTAINMENT trees (the axis; mirror scopeSpec): scopeWorkspace,
//     scopeAgentGroup and scopeFolder. An actor IN the scope is contained. These
//     are validated for existence at bind time ("no dangling scope") and the resource-
//     anchored ones (workspace/folder) ride the Cedar cross-scope grant/forbid.
//   - SUBJECT trees: scopeSession, scopeAgent, scopeUser, scopeUserGroup, scopeRole.
//     The binding names WHO — a session/agent/user identity, a directory group (S256) or a
//     tenant role — matched at resolve against the authenticated actor (principal /
//     route-gated session-agent ref). They are shape-validated only (the auth subjects are
//     not reachable from the module's tenant store scope); an unknown ref simply never
//     matches → deny-closed, exactly the subject pattern.
const (
	scopeWorkspace  = "workspace"
	scopeAgentGroup = "agent_group"
	// scopeFolder anchors a source under a node of the Resource tree (scope_ref is the
	// folder's stable Resource id). It carries NO soft containment of its own (an actor
	// is not a tree node —); access is decided purely by the per-entity
	// grant/forbid over that folder's subtree (the engine resolves the folder's live
	// ancestors from the materialized Path, so a grant on the folder OR an ancestor
	// authorizes — downward inheritance) plus tenant RBAC. Deny-closed otherwise.
	scopeFolder = "folder"

	// Subject trees. scope_ref is: session external_id, agent external_id, user id,
	// UserGroup.ID (S256 directory group, matched via principal.GroupsIn), or a tenant
	// role name. See ADR-0022 §1 and the source-scoping-axes contract.
	scopeSession   = "session"
	scopeAgent     = "agent"
	scopeUser      = "user"
	scopeUserGroup = "user_group"
	scopeRole      = "role"
)

// binding columns.
const (
	colSourceType  = "source_type"
	colSourceRef   = "source_ref"
	colScopeTree   = "scope_tree"
	colScopeRef    = "scope_ref"     // workspace/agent-group SLUG ("" = default workspace)
	colWorkspaceID = "workspace_id"  // resolved model.ID of the scope's workspace (grant-override declaredScope)
	colCredName    = "cred_name"     // scoped credential: logical name ("" = inherit the global, unbound credential)
	colCredRefKind = "cred_ref_kind" // env|vault|secret_manager|file|other
	colCredRef     = "cred_ref"      // the locator (value-free, never the secret)
	colCredHint    = "cred_hint"     // optional masked partial for operator recognition
	colEnabled     = "enabled"
	colCreatedBy   = "created_by"
	colNote        = "note"
	// colFolderPath is the ADVISORY, store-resolved snapshot of the anchor folder's
	// materialized Path. It is surfaced in responses so a folder binding reads as
	// "/<root>/…/<folder>" rather than an opaque id, and supports "list sources confined
	// under this subtree" queries. It is NOT read on the authorization path: the resolver
	// passes the folder id (scope_ref) to the engine, which re-reads the folder's
	// LIVE Path each call — so a folder Move never yields a wrong decision, only a path
	// here that may lag until the binding is next written. Empty for non-folder bindings.
	colFolderPath = "folder_path"
	// colEffect is the binding's effect: "allow" (the default; an empty stored
	// value is an allow, back-compat) or "forbid". A forbid SUBTRACTS — it overrides any
	// allow for the actor it names (forbid-overrides-allow, absolute; ADR-0022 §2), the
	// same algebra as model-access row-level effect. It is NOT part of the natural
	// key: an allow and a forbid for the same (source, tree, ref) are contradictory and
	// the unique index correctly forbids both (the effect is updated in place).
	colEffect = "effect"
)

// connector_assignment columns.
const (
	colAssignConnector = "connector_name"
	colAssignWorkspace = "workspace_ref"
	colAssignWsID      = "workspace_id"
	colAssignMode      = "mode"
	colAssignEnabled   = "enabled"
	colAssignCreatedBy = "created_by"
	colAssignNote      = "note"
)

// workspace_connector columns.
const (
	colWCName        = "name"
	colWCKind        = "kind"
	colWCWorkspace   = "workspace_ref"
	colWCWsID        = "workspace_id"
	colWCConfig      = "config"
	colWCSecretsRef  = "secrets_ref"
	colWCPollSeconds = "poll_seconds"
	colWCEnabled     = "enabled"
	colWCCreatedBy   = "created_by"
	colWCNote        = "note"
	colWCStatus      = "status"
)

// posture_request columns.
const (
	colPRSourceType = "source_type"
	colPRSourceRef  = "source_ref"
	colPROp         = "op"         // update | delete | disable_scoping | public_only
	colPRTargetID   = "target_id"  // the binding being updated/deleted ("" for disable_scoping)
	colPRProposed   = "proposed"   // JSON of the proposed bindingDTO (for op=update); "" otherwise
	colPRReason     = "reason"     // why this change is a RELAXATION (audit/UI, non-sensitive)
	colPRProposer   = "proposer"   // the actor who proposed it (audit actor string)
	colPRStatus     = "status"     // pending | approved | rejected
	colPRDecidedBy  = "decided_by" // the DISTINCT actor who approved/rejected ("" while pending)
	// The STABLE PERSON behind each party, next to the credential each used. The
	// dual-control comparison reads these, never the actor strings: one human holds a
	// session AND a token they minted, which render two different actors and satisfied
	// a gate meant to require two people (auth.PersonRef). Storing the person is
	// the precondition of comparing by person — the row cannot be compared by something
	// it does not carry. Empty for a credential no person stands behind, which is a
	// fact the gate decides about (auth.PersonUndetermined), not a missing value.
	colPRProposerUser  = "proposer_user"   // model.User id of the proposer ("" if person-less)
	colPRDecidedByUser = "decided_by_user" // model.User id of the decider ("" while pending)
	colPRNote          = "note"
)

// guard_posture columns. The source_type/source_ref pair is intentionally separate
// from sourcescope.binding: this does not authorize who can reach a KB, it only says
// whether the retrieval guard applies ACL/clearance/region grants or deliberately
// downgrades to public-only for that KB.
const (
	colGuardProfile   = "guard_profile" // acl_aware | public_only
	colGuardReason    = "reason"
	colGuardUpdatedBy = "updated_by"
)

// RegisterSchema declares the module's owned entities. It satisfies the engine-side
// runtime.SchemaProvider seam (structural — no runtime import) and is called once, at
// store construction, before any Scope exists (S02 §7 /). The engine creates
// each table, injects the base columns and attaches the tenant guards.
//
// sourcescope_binding is MINIMAL-DATA by construction: the only credential surface is
// cred_ref, a REFERENCE (logical name + ref_kind + locator + optional masked hint),
// never a value — the same structural guarantee as capabilities.mcp_config.secret_refs
// (docs/SECURITY-HARDENING.md). The create/update handler rejects an inline credential in the locator.
//
// New columns are appended last and nullable, per the expand-only growth vehicle of
// the store's boot reconcile (reconcileColumns, sqlstore) introspects the live
// schema and ALTER TABLE ADD COLUMNs any missing nullable field on an already-migrated DB
// — for MODULE tables as well as core ones — so no numbered
// SQL migration is needed and fresh/existing databases converge. The store orders the
// live columns by descriptor, so they must never be reordered.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	descs := []model.EntityDescriptor{
		{
			Kind:  bindingKind,
			Table: bindingTable,
			Fields: []model.FieldSpec{
				{Name: colSourceType, Kind: model.KindText, Indexed: true},
				{Name: colSourceRef, Kind: model.KindText, Indexed: true},
				{Name: colScopeTree, Kind: model.KindText},
				{Name: colScopeRef, Kind: model.KindText},
				{Name: colWorkspaceID, Kind: model.KindText, Nullable: true},
				{Name: colCredName, Kind: model.KindText, Nullable: true},
				{Name: colCredRefKind, Kind: model.KindText, Nullable: true},
				{Name: colCredRef, Kind: model.KindText, Nullable: true},
				{Name: colCredHint, Kind: model.KindText, Nullable: true},
				{Name: colEnabled, Kind: model.KindBool},
				{Name: colCreatedBy, Kind: model.KindText},
				{Name: colNote, Kind: model.KindText, Nullable: true},
				{Name: colFolderPath, Kind: model.KindText, Nullable: true},
				// appended last and nullable (expand-only). Empty ⇒ allow, so a
				// pre row reconciles to an allow binding unchanged. NOT in the unique
				// index below — the natural key stays (source_type, source_ref, tree, ref).
				{Name: colEffect, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{
				Name:    "sourcescope_binding_uniq",
				Columns: []string{model.ColTenantID, colSourceType, colSourceRef, colScopeTree, colScopeRef},
				Unique:  true,
			}},
		},
		// connector_assignment — maps a deployment-global connector to a workspace.
		// A global connector with NO assignment rows is visible everywhere (back-compat);
		// once ANY assignment exists for a connector, only the assigned workspaces see it
		// (deny-closed).
		{
			Kind:  assignmentKind,
			Table: assignmentTable,
			Fields: []model.FieldSpec{
				{Name: colAssignConnector, Kind: model.KindText, Indexed: true},
				{Name: colAssignWorkspace, Kind: model.KindText, Indexed: true},
				{Name: colAssignWsID, Kind: model.KindText, Nullable: true},
				{Name: colAssignMode, Kind: model.KindText, Nullable: true},
				{Name: colAssignEnabled, Kind: model.KindBool},
				{Name: colAssignCreatedBy, Kind: model.KindText},
				{Name: colAssignNote, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{
				Name:    "sourcescope_connector_assignment_uniq",
				Columns: []string{model.ColTenantID, colAssignConnector, colAssignWorkspace},
				Unique:  true,
			}},
		},
		// workspace_connector — a workspace-scoped connector definition created by
		// a workspace admin. Tenant-resident, confined to one workspace. The config column
		// carries non-secret settings; secrets_ref carries REFERENCES only (store:ws/…,
		// env:…, vault:…), never values — the same structural invariant as binding.cred_ref.
		{
			Kind:  wsConnectorKind,
			Table: wsConnectorTable,
			Fields: []model.FieldSpec{
				{Name: colWCName, Kind: model.KindText, Indexed: true},
				{Name: colWCKind, Kind: model.KindText, Indexed: true},
				{Name: colWCWorkspace, Kind: model.KindText, Indexed: true},
				{Name: colWCWsID, Kind: model.KindText, Nullable: true},
				{Name: colWCConfig, Kind: model.KindJSON, Nullable: true},
				{Name: colWCSecretsRef, Kind: model.KindJSON, Nullable: true},
				{Name: colWCPollSeconds, Kind: model.KindInt},
				{Name: colWCEnabled, Kind: model.KindBool},
				{Name: colWCCreatedBy, Kind: model.KindText},
				{Name: colWCNote, Kind: model.KindText, Nullable: true},
				{Name: colWCStatus, Kind: model.KindText, Nullable: true},
			},
			Indexes: []model.IndexSpec{{
				Name:    "sourcescope_workspace_connector_uniq",
				Columns: []string{model.ColTenantID, colWCWorkspace, colWCName},
				Unique:  true,
			}},
		},
		//-B: per-KB retrieval guard posture (separate from source-scope bindings).
		{
			Kind:  guardPostureKind,
			Table: guardPostureTable,
			Fields: []model.FieldSpec{
				{Name: colSourceType, Kind: model.KindText, Indexed: true},
				{Name: colSourceRef, Kind: model.KindText, Indexed: true},
				{Name: colGuardProfile, Kind: model.KindText, Indexed: true},
				{Name: colGuardReason, Kind: model.KindText, Nullable: true},
				{Name: colGuardUpdatedBy, Kind: model.KindText},
			},
			Indexes: []model.IndexSpec{{
				Name:    "sourcescope_guard_posture_uniq",
				Columns: []string{model.ColTenantID, colSourceType, colSourceRef},
				Unique:  true,
			}},
		},
		// (F2/F5): pending posture-change requests (dual-control on relaxations).
		{
			Kind:  postureRequestKind,
			Table: postureRequestTable,
			Fields: []model.FieldSpec{
				{Name: colPRSourceType, Kind: model.KindText, Indexed: true},
				{Name: colPRSourceRef, Kind: model.KindText, Indexed: true},
				{Name: colPROp, Kind: model.KindText},
				{Name: colPRTargetID, Kind: model.KindText, Nullable: true},
				{Name: colPRProposed, Kind: model.KindText, Nullable: true},
				{Name: colPRReason, Kind: model.KindText, Nullable: true},
				{Name: colPRProposer, Kind: model.KindText},
				{Name: colPRStatus, Kind: model.KindText, Indexed: true},
				{Name: colPRDecidedBy, Kind: model.KindText, Nullable: true},
				{Name: colPRNote, Kind: model.KindText, Nullable: true},
				// the HUMAN behind each leg, beside the credential string.
				//
				// proposer/decided_by hold Actor(), which is "user:<UserID>" for a session and
				// "token:<CredID>" for a token, so ONE person yields TWO strings and a
				// dual-control that compares them is comparing credentials, not people. These
				// two columns carry Principal.UserID, which is the same value for both of that
				// person's credentials (a token principal is built with its issuer's UserID,
				// core/auth/principal_lookup.go:235). The pattern is breakglass's
				// activated_by + activated_by_user.
				//
				// Nullable and APPENDED LAST so the additive reconcile issues ALTER TABLE ADD
				// COLUMN on an existing database. A row written before this exists therefore
				// has "" here, which is why the check still falls back to Actor() when the
				// stored user is empty: a request pending across the upgrade must not become
				// unguarded.
				{Name: colPRProposerUser, Kind: model.KindText, Nullable: true},
				{Name: colPRDecidedByUser, Kind: model.KindText, Nullable: true},
			},
		},
	}
	for _, d := range descs {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

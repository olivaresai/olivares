// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The module's permissions, granted to the built-in roles by verb tier (viewer→read,
// editor→write). Binding a source to a workspace/agent-group (and the scoped
// credential reference it carries) is an auditable governance change (docs/SECURITY-HARDENING.md);
// the per-scope delegation ceiling (only bind WITHIN your own scope) is the
// documented follow-up.
//
// Adds connector assignment and workspace connector permissions. Assignments
// and workspace connectors are workspace-admin-level operations (write tier), with
// a read tier for listing.
const (
	permBindingRead  auth.Permission = "sourcescope:binding:read"
	permBindingWrite auth.Permission = "sourcescope:binding:write"

	permAssignmentRead  auth.Permission = "sourcescope:assignment:read"
	permAssignmentWrite auth.Permission = "sourcescope:assignment:write"

	permWsConnectorRead  auth.Permission = "sourcescope:workspace_connector:read"
	permWsConnectorWrite auth.Permission = "sourcescope:workspace_connector:write"

	// (F2/F5): approving/rejecting a posture RELAXATION is an admin-tier privileged
	// act (the :admin verb maps to admin/owner) — separation of duty from the editor-tier
	// proposer, and the second leg of the dual-control that never lets one actor relax
	// enforcement. Proposing a relaxation rides the normal binding:write path.
	permPostureApprove auth.Permission = "sourcescope:posture:admin"
)

// APINamespace returns the module's namespace; it roots routes at /v1/m/sourcescope/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permBindingRead, permBindingWrite,
		permAssignmentRead, permAssignmentWrite,
		permWsConnectorRead, permWsConnectorWrite,
		permPostureApprove,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
// The lineage each entity route declares. The workspace column must be the
// workspace the ROW BELONGS TO, resolved to a model.ID at write time — never a ref string
// and never the declaring principal's workspace. All three are written by this module from
// a store lookup inside the create transaction, so they cannot be forged by a caller.
var (
	assignmentEntity  = api.EntityRef{Kind: assignmentKind, IDParam: "id", WorkspaceColumn: colAssignWsID}
	wsConnectorEntity = api.EntityRef{Kind: wsConnectorKind, IDParam: "id", WorkspaceColumn: colWCWsID}
)

func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// source→scope bindings
	reg.Handle("GET", "/bindings", permBindingRead, m.handleListBindings)
	reg.Handle("POST", "/bindings", permBindingWrite, m.handleCreateBinding)
	// bindings stay COLLECTION-level, and this is a correction of an earlier
	// attempt in this same unit rather than an omission.
	//
	// THE RULE the migration has to obey: a route may anchor its authorization to an
	// entity's stored lineage only if the request cannot MOVE that lineage. Otherwise the
	// decision is about where the row WAS and the effect lands where it WILL BE, and
	// nothing ever authorizes the destination.
	//
	// A binding breaks it. handleUpdateBinding re-resolves the scope from the PAYLOAD and
	// rewrites the stored workspace (binding.go resolveScope + in.fields), so a principal
	// whose only authority is workspace A could be authorized against A and land the row
	// in B. assignments and workspace-connectors do not: both force the workspace ref and
	// read the workspace id back OUT of the stored row on update, so their lineage is
	// immutable for the life of the request.
	//
	// Worse than inert, and measured: the dual-control classifier has no branch for a
	// forbid that CHANGES scope (posture.go classifyUpdate — the forbid branches cover
	// forbid→allow and forbid-disabled, everything else requires newEff==allow), so moving
	// an enabled forbid out of a workspace applies immediately with no second approver,
	// while DELETING that same forbid is dual-controlled. That gap is pre-existing and is
	// not this unit's to close; what this unit must not do is hand the lever to principals
	// whose entire authority is one workspace. Migrating these three needs either an
	// immutable binding scope or an authorization that covers the DESTINATION.
	reg.Handle("GET", "/bindings/{id}", permBindingRead, m.handleGetBinding)
	reg.Handle("PUT", "/bindings/{id}", permBindingWrite, m.handleUpdateBinding)
	reg.Handle("DELETE", "/bindings/{id}", permBindingWrite, m.handleDeleteBinding)

	// the authoritative, navigable Resource tree the console's folder/subtree
	// binding picker reads to anchor an folder binding to a REAL Resource id. A
	// read-only projection of the tenant's Resource tree (Children/Subtree) — the read
	// seam the picker needs, since no navigable Resource enumeration was exposed before
	// (only internal summary/authz-reverse-query usages of Resources()).
	reg.Handle("GET", "/resources", permBindingRead, m.handleListResources)

	// (F2/F5): enforcement-posture dual-control. A relaxing update/delete is proposed
	// through the binding:write endpoints above (they record a pending request); the
	// disable-scoping op and the approve/reject decisions live here.
	reg.Handle("POST", "/sources/disable-scoping", permBindingWrite, m.handleDisableScoping)
	//-B: ACL/clearance/region guard posture for governed RAG. This is a separate
	// axis from source-scope: public_only is a dual-controlled relaxation; acl_aware is
	// a tightening and applies immediately.
	reg.Handle("GET", "/guard-postures", permBindingRead, m.handleListGuardPostures)
	reg.Handle("PUT", "/guard-postures", permBindingWrite, m.handlePutGuardPosture)
	reg.Handle("GET", "/posture-requests", permBindingRead, m.handleListPostureRequests)
	// these three stay COLLECTION-level deliberately, and the reason is not that
	// nobody got to them. TWO independent reasons, either of which is sufficient:
	//   1. postureRequest carries no workspace column at all (schema.go), so there is no
	//      lineage to resolve: an entity route would seed an id and change nothing.
	//   2. Even given one, it would be the WRONG OBJECT. A decision applies to the
	//      BINDING the request targets (colPRTargetID, applyPosture), not to the request
	//      row, so authorizing on the request's own scope would authorize a mutation of a
	//      binding that may live in a different workspace.
	// Making them entity routes is a schema question (whose workspace does a posture
	// request belong to?), not a registration one. TestPostureRequestRoutesStayCollectionLevel
	// pins this so a later sweep cannot "finish the migration" without answering it.
	reg.Handle("GET", "/posture-requests/{id}", permBindingRead, m.handleGetPostureRequest)
	reg.Handle("POST", "/posture-requests/{id}/approve", permPostureApprove, m.handleApprovePostureRequest)
	reg.Handle("POST", "/posture-requests/{id}/reject", permPostureApprove, m.handleRejectPostureRequest)

	// read-only console seam — the baseline resolver preview ("what would this
	// actor resolve"). Tree navigation for the folder-binding picker is GET /resources.
	reg.Handle("GET", "/resolve", permBindingRead, m.handleResolvePreview)

	// connector→workspace assignments
	reg.Handle("GET", "/assignments", permAssignmentRead, m.handleListAssignments)
	reg.Handle("POST", "/assignments", permAssignmentWrite, m.handleCreateAssignment)
	reg.HandleEntity("GET", "/assignments/{id}", permAssignmentRead, assignmentEntity, m.handleGetAssignment)
	reg.HandleEntity("PUT", "/assignments/{id}", permAssignmentWrite, assignmentEntity, m.handleUpdateAssignment)
	// DELETE stays COLLECTION-level: it satisfies the first half of the rule and breaks
	// the second. The assignment's lineage cannot move, but the EFFECT of deleting the
	// LAST assignment row reaches far beyond it — ConnectorAssigned returns true for
	// EVERY workspace once no rows remain (assignment.go: "unassigned connectors are
	// globally visible"), so the connector becomes visible tenant-wide. Anchoring the
	// decision to one workspace would let a principal whose entire authority is that
	// workspace relax visibility for all of them. PUT is unaffected: disabling the last
	// enabled row leaves the row present, which TIGHTENS.
	reg.Handle("DELETE", "/assignments/{id}", permAssignmentWrite, m.handleDeleteAssignment)

	// workspace-scoped connectors
	reg.Handle("GET", "/workspace-connectors", permWsConnectorRead, m.handleListWsConnectors)
	reg.Handle("POST", "/workspace-connectors", permWsConnectorWrite, m.handleCreateWsConnector)
	reg.HandleEntity("GET", "/workspace-connectors/{id}", permWsConnectorRead, wsConnectorEntity, m.handleGetWsConnector)
	reg.HandleEntity("PUT", "/workspace-connectors/{id}", permWsConnectorWrite, wsConnectorEntity, m.handleUpdateWsConnector)
	reg.HandleEntity("DELETE", "/workspace-connectors/{id}", permWsConnectorWrite, wsConnectorEntity, m.handleDeleteWsConnector)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write, admin/owner→admin). Reading the catalog is not sensitive;
// creating/editing drafts and self-service instantiation are write-tier; approving/
// signing/deprecating entries and deciding on instances are admin-tier privileged
// curation/governance actions (docs/SECURITY-HARDENING.md), all self-audited.
const (
	permEntryRead     auth.Permission = "catalog:entry:read"
	permEntryWrite    auth.Permission = "catalog:entry:write"
	permEntryAdmin    auth.Permission = "catalog:entry:admin"
	permInstanceRead  auth.Permission = "catalog:instance:read"
	permInstanceWrite auth.Permission = "catalog:instance:write"
	permInstanceAdmin auth.Permission = "catalog:instance:admin"
)

// APINamespace returns the module's namespace; it roots routes at /v1/m/catalog/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permEntryRead, permEntryWrite, permEntryAdmin,
		permInstanceRead, permInstanceWrite, permInstanceAdmin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Curated registry entries (versioned, signed when a key is configured).
	reg.Handle("GET", "/entries", permEntryRead, m.handleListEntries)
	reg.Handle("POST", "/entries", permEntryWrite, m.handleCreateEntry)
	reg.Handle("GET", "/entries/{id}", permEntryRead, m.handleGetEntry)
	reg.Handle("PUT", "/entries/{id}", permEntryWrite, m.handleUpdateEntry)
	reg.Handle("DELETE", "/entries/{id}", permEntryWrite, m.handleDeleteEntry)
	reg.Handle("POST", "/entries/{id}/submit", permEntryWrite, m.handleSubmitEntry)
	reg.Handle("POST", "/entries/{id}/approve", permEntryAdmin, m.handleApproveEntry)
	reg.Handle("POST", "/entries/{id}/deprecate", permEntryAdmin, m.handleDeprecateEntry)
	reg.Handle("GET", "/entries/{id}/verify", permEntryRead, m.handleVerifyEntry)
	reg.Handle("GET", "/pubkey", permEntryRead, m.handlePubkey)

	// signed admission for federated MCP entries (the flow reused —
	// provenance/SBOM attestation verification gating approval into the served
	// sub-registry). Policy writes and admissions are admin-tier curation.
	// /entries/{id}/admit is ONE route shared with S142: handleAdmitEntry
	// dispatches by entry kind (mcp flow, connector → S142 flow).
	reg.Handle("GET", "/mcp-admission/policy", permEntryRead, m.handleGetMCPAdmissionPolicy)
	reg.Handle("PUT", "/mcp-admission/policy", permEntryAdmin, m.handlePutMCPAdmissionPolicy)
	reg.Handle("POST", "/entries/{id}/admit", permEntryAdmin, m.handleAdmitEntry)
	reg.Handle("GET", "/mcp-admissions", permEntryRead, m.handleListMCPAdmissions)

	// S142: signed admission for third-party CONNECTOR entries — the pair
	// mirrored for the external-connector ecosystem (connectoradmission.go): the
	// tenant trust root, the recorded verdicts and the deny-closed approve gate
	// that certifies "approved connector ⇒ provenance/SBOM verified".
	reg.Handle("GET", "/connector-admission/policy", permEntryRead, m.handleGetConnectorAdmissionPolicy)
	reg.Handle("PUT", "/connector-admission/policy", permEntryAdmin, m.handlePutConnectorAdmissionPolicy)
	reg.Handle("GET", "/connector-admissions", permEntryRead, m.handleListConnectorAdmissions)

	// Governed self-service instantiation (the flow; the policy is ).
	reg.Handle("POST", "/entries/{id}/instantiate", permInstanceWrite, m.handleInstantiate)
	reg.Handle("GET", "/instances", permInstanceRead, m.handleListInstances)
	reg.Handle("GET", "/instances/{id}", permInstanceRead, m.handleGetInstance)
	reg.Handle("POST", "/instances/{id}/transition", permInstanceAdmin, m.handleTransitionInstance)
}

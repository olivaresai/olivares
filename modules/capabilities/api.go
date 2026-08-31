// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The module's permissions, granted to the built-in roles by verb tier (viewer→
// read, editor→write). Reading the live capability catalog and wiring graph is
// not a write; managing an MCP server's configuration (and the secrets it
// references) is an auditable change (docs/SECURITY-HARDENING.md).
const (
	permCatalogRead auth.Permission = "capabilities:catalog:read"
	permConfigRead  auth.Permission = "capabilities:config:read"
	permConfigWrite auth.Permission = "capabilities:config:write"
)

// APINamespace returns the module's namespace; it roots routes at
// /v1/m/capabilities/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permCatalogRead, permConfigRead, permConfigWrite}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check before the handler runs, and
// pins the data handle to the resolved tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Live capability catalog (MCP servers, skills, tools, wiring) — read-only.
	reg.Handle("GET", "/servers", permCatalogRead, m.handleListServers)
	reg.Handle("GET", "/servers/{id}", permCatalogRead, m.handleGetServer)
	reg.Handle("GET", "/skills", permCatalogRead, m.handleListSkills)
	reg.Handle("GET", "/tools", permCatalogRead, m.handleListTools)
	reg.Handle("GET", "/wiring", permCatalogRead, m.handleWiring)

	// Managed configuration + version history (secret references, never values).
	reg.Handle("GET", "/configs", permConfigRead, m.handleListConfigs)
	reg.Handle("POST", "/configs", permConfigWrite, m.handleCreateConfig)
	reg.Handle("GET", "/configs/{id}", permConfigRead, m.handleGetConfig)
	reg.Handle("PUT", "/configs/{id}", permConfigWrite, m.handleUpdateConfig)
	reg.Handle("DELETE", "/configs/{id}", permConfigWrite, m.handleDeleteConfig)
	reg.Handle("GET", "/configs/{id}/revisions", permConfigRead, m.handleListRevisions)

	// tool-pin posture + the two operator verbs. Reading the pin set is
	// config-read tier; approving or revoking a pin changes an authorization
	// baseline and is write-tier (docs/SECURITY-HARDENING.md).
	reg.Handle("GET", "/toolpins", permConfigRead, m.handleListToolPins)
	reg.Handle("POST", "/toolpins/approve", permConfigWrite, m.handleApproveToolPin)
	reg.Handle("POST", "/toolpins/unpin", permConfigWrite, m.handleUnpinToolPin)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// The stable core doc's x-required-permission annotations. The module
// doc has carried the extension since (openapi_modules.go stamps it from
// each RouteRegistrar mount); the core doc's builder never did, which made the
// whole stable surface metadata-less — and the console playground's deny-closed
// tenant-admin filter (no metadata ⇒ system-only) then hid it entirely. This
// map is the core counterpart: one entry per operation, each verified against
// the handler's actual authorization call — the annotation documents the check
// the handler performs, it never replaces it.
//
// Operations deliberately NOT annotated (they gate on no single permission, so
// any value here would be a lie; the playground's deny-closed filter hides them
// from tenant admins, which loses nothing of substance):
//   - logout / refreshToken / whoami — any authenticated principal
//     (handlers_auth.go); pure session plumbing.
//   - searchConsole — authorized per result kind inside the handler
//     (search.go), not at the route.
//
// The four token operations ARE annotated with the tenant-path permission
// (handlers_core.go: superadmin passes outright, everyone else needs
// token:read/token:write on the token's bound tenant) — that is the permission
// a non-superadmin caller must hold, which is exactly what the annotation is
// for.
var corePermissions = map[string]string{
	// Agents + access graph (handlers_core.go).
	"listAgents":      "agent:read",
	"getAgent":        "agent:read",
	"createAgent":     "agent:write",
	"updateAgent":     "agent:write",
	"deleteAgent":     "agent:write",
	"listAccessEdges": "accessgraph:read",

	// Audit ledger (handlers_audit.go).
	"listAuditEvents":       "audit:read",
	"verifyAuditChain":      "audit:read",
	"exportAuditLedger":     "audit:read",
	"getAuditPubkey":        "audit:read",
	"listSystemAuditEvents": "system:admin",

	// Users + memberships (handlers_core.go, handlers_members.go).
	"listUsers":         "user:read",
	"listSuperadmins":   "user:read",
	"listMembers":       "user:read",
	"createUser":        "user:write",
	"disableSuperadmin": "user:write",
	"enableSuperadmin":  "user:write",
	"grantMembership":   "membership:write",

	// Tokens (handlers_core.go; tenant path, see the doc comment above).
	"listTokens":  "token:read",
	"issueToken":  "token:write",
	"revokeToken": "token:write",
	"rotateToken": "token:write",

	// Workspaces (handlers_scoping.go).
	"listWorkspaces":  "tenant:read",
	"getWorkspace":    "tenant:read",
	"createWorkspace": "tenant:admin",
	"updateWorkspace": "tenant:admin",

	// Connector health (handlers_connector_health.go).
	"getConnectorHealth": "health:status:read",

	// Tenant provisioning (handlers_core.go).
	"getResidencyRegistry": "system:admin",
	"listOrgs":             "system:admin",
	"createOrg":            "system:admin",
	"setOrgRegion":         "system:admin",
	"setOrgStatus":         "system:admin",
	"dropOrg":              "system:admin",

	// Console admin surface — all superadmin (handlers_console_ops.go,
	// handlers_secrets.go, handlers_sources.go, handlers_connectors.go,
	// handlers_sso_config.go, handlers_license.go).
	"getSetupStatus":       "system:admin",
	"getHealthSummary":     "system:admin",
	"getKeyCustody":        "system:admin",
	"getBusSnapshot":       "system:admin",
	"getEffectiveConfig":   "system:admin",
	"createSupportBundle":  "system:admin",
	"refreshUpdateStatus":  "system:admin",
	"listSecrets":          "system:admin",
	"putSecret":            "system:admin",
	"deleteSecret":         "system:admin",
	"getLicenseStatus":     "system:admin",
	"installLicense":       "system:admin",
	"uninstallLicense":     "system:admin",
	"listConnectorCatalog": "system:admin",
	"putConnector":         "system:admin",
	"deleteConnector":      "system:admin",
	"testConnector":        "system:admin",
	"listSources":          "system:admin",
	"putSource":            "system:admin",
	"deleteSource":         "system:admin",
	"getSSOConfig":         "system:admin",
	"putSSOConfig":         "system:admin",
	"deleteSSOConfig":      "system:admin",
	"testSSOConfig":        "system:admin",
}

// corePermissionExempt are the secured operations deliberately left without an
// annotation (see the doc comment on corePermissions). openapi_perm_test.go
// enforces that every secured operation is in exactly one of the two sets, so
// a new core route cannot land unannotated by accident.
var corePermissionExempt = map[string]bool{
	"logout":        true,
	"refreshToken":  true,
	"whoami":        true,
	"searchConsole": true,
}

// stampCorePermissions walks the built paths object and stamps
// x-required-permission on every operation with a corePermissions entry.
func stampCorePermissions(paths map[string]any) {
	for _, item := range paths {
		pathItem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, maybeOp := range pathItem {
			o, ok := maybeOp.(map[string]any)
			if !ok {
				continue
			}
			id, _ := o["operationId"].(string)
			if perm, found := corePermissions[id]; found {
				o["x-required-permission"] = perm
			}
		}
	}
}

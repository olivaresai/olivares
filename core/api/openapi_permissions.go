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
//   - authCapabilities — the SELF capability projection (handlers_capabilities.go).
//     It gates on no permission BY DESIGN and not by omission: it has no subject
//     input at all, so there is nothing for a permission to protect against, and
//     requiring one would make the console unable to ask about the operator in
//     front of it unless that operator already held a privileged read. Each
//     question inside is decided by the FULL authorization of the operation it
//     names, which is a different check per question and could not be one value
//     here. The privileged, subject-resolving surface remains AuthZEN with its
//     authz:read.
//   - searchConsole — authorized per result kind inside the handler
//     (search.go), not at the route.
//   - getBrowserSession / migrateBrowserSession — the signed-in user's own
//     cookie session, from the same origin (browser_session.go); pure session
//     plumbing like refreshToken.
//
// The four token operations ARE annotated with the tenant-path permission
// (handlers_core.go: superadmin passes outright, everyone else needs
// token:read/token:write on the token's bound tenant) — that is the permission
// a non-superadmin caller must hold, which is exactly what the annotation is
// for.
var corePermissions = map[string]string{
	"getMCPGateway": "tenant:admin", "addMCPGatewayServer": "tenant:admin", "updateMCPGatewayServer": "tenant:admin", "removeMCPGatewayServer": "tenant:admin", "testMCPGatewayServer": "tenant:admin", "setMCPGatewaySessionTools": "tenant:admin",
	"getTOTPPolicy": "system:admin",
	"setTOTPPolicy": "system:admin",
	// Administrative step-up policy (handlers_stepup_policy.go: authzSystem system:admin).
	"getStepUpPolicy":   "system:admin",
	"setStepUpPolicy":   "system:admin",
	"getUserTOTPStatus": "membership:read",
	"resetUserTOTP":     "membership:write",
	// Deployment tracing choices (server.go: systemRoute("system:admin"); the PUT
	// additionally requires the configured step-up inside the handler).
	"getTracingSettings":  "system:admin",
	"saveTracingSettings": "system:admin",
	// Agents + access graph (handlers_core.go).
	"listAgents":      "agent:read",
	"getAgent":        "agent:read",
	"createAgent":     "agent:write",
	"updateAgent":     "agent:write",
	"deleteAgent":     "agent:write",
	"listAccessEdges": "accessgraph:read",

	// Audit ledger (handlers_audit.go).
	"listAuditEvents":       "audit:read",
	"listRecentAuditEvents": "audit:read",
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
	"listWorkspaces":     "tenant:read",
	"getWorkspace":       "tenant:read",
	"createWorkspace":    "tenant:admin",
	"updateWorkspace":    "tenant:admin",
	"setWorkspaceParent": "tenant:admin",

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

	// Completed surfaces: each value is the gate the route registration in
	// server.go applies (tenantRoute/entityRoute/systemRoute/authzenRoute).
	"listAgentGroups":        "agent:read",
	"getAgentGroup":          "agent:read",
	"listAgentGroupMembers":  "agent:read",
	"createAgentGroup":       "agent:write",
	"updateAgentGroup":       "agent:write",
	"deleteAgentGroup":       "agent:write",
	"addAgentGroupMember":    "agent:write",
	"removeAgentGroupMember": "agent:write",
	"getEffectiveRights":     "authz:admin",
	"getWorkspaceSummary":    "tenant:read",
	"getWorkspaceContents":   "tenant:admin",
	"listInvites":            "membership:read",
	"listGroups":             "membership:read",
	"revokeInvite":           "membership:write",
	"resendInvite":           "membership:write",
	"onboardMember":          "membership:write",
	"setGroupRole":           "membership:write",
	"setGroupParent":         "membership:write",
	"setGroupWorkspace":      "membership:write",
	"getActivationStatus":    "system:admin",
	"previewActivation":      "system:admin",
	"applyActivation":        "system:admin",
	"triggerBackup":          "system:admin",
	"listBackups":            "system:admin",
	"getBackup":              "system:admin",
	"deleteBackup":           "system:admin",
	"downloadBackup":         "system:admin",
	"listDRJobs":             "system:admin",
	"streamDRJob":            "system:admin",
	"listPendingRestores":    "system:admin",
	"uploadRestore":          "system:admin",
	"applyRestore":           "system:admin",
	"approveRestore":         "system:admin",
	"getDRSchedule":          "system:admin",
	"putDRSchedule":          "system:admin",
	"getLogBuffer":           "system:admin",
	"streamLogs":             "system:admin",
	"getModuleSelection":     "system:admin",
	"selectModules":          "system:admin",
	"reloadRuntime":          "system:admin",
	"getSourceContentDiff":   "system:admin",
	"listSSOIdPs":            "system:admin",
	"getSSOIdP":              "system:admin",
	"putSSOIdP":              "system:admin",
	"deleteSSOIdP":           "system:admin",
	"testSSOIdP":             "system:admin",
	"getTenantSSOConfig":     "system:admin",
	"putTenantSSOConfig":     "system:admin",
	"deleteTenantSSOConfig":  "system:admin",
	"testTenantSSOConfig":    "system:admin",
	"listTenantSSOIdPs":      "system:admin",
	"getTenantSSOIdP":        "system:admin",
	"putTenantSSOIdP":        "system:admin",
	"deleteTenantSSOIdP":     "system:admin",
	"testTenantSSOIdP":       "system:admin",
}

// corePermissionExempt are the secured operations deliberately left without an
// annotation (see the doc comment on corePermissions). openapi_perm_test.go
// enforces that every secured operation is in exactly one of the two sets, so
// a new core route cannot land unannotated by accident.
// TOTP self-service authenticates a session or a primary-verified pending token.
var corePermissionExempt = map[string]bool{
	"enrolTOTP": true, "activateTOTP": true, "getTOTPStatus": true, "removeTOTP": true,
	"logout":           true,
	"refreshToken":     true,
	"whoami":           true,
	"searchConsole":    true,
	"authCapabilities": true,
	// The subject completes its own native proof; administration was authorized separately.
	"completeOSAccountBinding": true,
	// The signed-in user's own browser session (browser_session.go).
	"getBrowserSession":     true,
	"migrateBrowserSession": true,
	// The signed-in human's own password change (handlers_account_password.go): it
	// gates on the session being human plus the CURRENT password, never on a
	// permission, so no single value here would be the handler's real check.
	"changeOwnPassword": true,
	// The signed-in session proves itself with a second factor (WebAuthn assertion or PIV certificate):
	// sessionRoute/authenticatedRoute gate it, never a permission.
	"webauthnRegisterOptions":     true,
	"webauthnRegister":            true,
	"webauthnAuthenticateOptions": true,
	"webauthnAuthenticate":        true,
	"listWebAuthnCredentials":     true,
	"renameWebAuthnCredential":    true,
	"deleteWebAuthnCredential":    true,
	"getPIVStatus":                true,
	"elevatePIV":                  true,
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

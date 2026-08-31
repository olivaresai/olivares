// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// governanceRequestBodyKind separates proven body shapes from mutations that
// are bodyless or deliberately left for a later feature slice. Pending does not
// mean no body: it prevents this slice from silently claiming completeness for
// handlers whose DTO has not yet been transcribed and checked.
type governanceRequestBodyKind uint8

const (
	governanceBodyless governanceRequestBodyKind = iota + 1
	governanceBodyful
	governanceBodyNoDerivable
	governanceBodyPending
)

type governanceRequestBodyDeclaration struct {
	kind     governanceRequestBodyKind
	required bool
	schema   map[string]any
}

// governanceRequestBody returns request bodies proven from the registered
// governance handlers. Optional HTTP bodies are distinguished from nullable
// JSON documents inside a mandatory body.
func governanceRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := governanceRequestBodyDeclarationFor(r)
	if !ok || decl.kind != governanceBodyful {
		return nil, false
	}
	return oaObj(
		"required", decl.required,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

// governanceRequestBodyDeclarationFor classifies all 50 mutations registered
// by the governance module. The sibling claude-policy and claude-agents modules
// have different namespaces and are intentionally outside this catalog.
func governanceRequestBodyDeclarationFor(r moduleRoute) (governanceRequestBodyDeclaration, bool) {
	if r.ns != "governance" {
		return governanceRequestBodyDeclaration{}, false
	}

	switch r.method + " " + r.pattern {
	case http.MethodPost + " /agents/{agentID}/identity":
		return governanceBodyDeclaration(governanceIdentityBindingSchema()), true
	case http.MethodPost + " /policies",
		http.MethodPut + " /policies/{id}":
		return governanceBodyDeclaration(governancePolicySchema()), true
	case http.MethodPost + " /approvals":
		return governanceBodyDeclaration(governanceApprovalCreateSchema()), true
	case http.MethodPost + " /approvals/{id}/decisions":
		return governanceBodyDeclaration(governanceApprovalDecisionSchema()), true
	case http.MethodPost + " /approvals/{id}/consume":
		return governanceBodyDeclaration(governanceApprovalConsumeSchema()), true
	case http.MethodPost + " /agents":
		return governanceBodyDeclaration(governanceAgentRegistrationSchema()), true
	case http.MethodPost + " /pdp/validate":
		return governanceBodyDeclaration(governancePDPEngineSourceSchema(false)), true
	case http.MethodPost + " /pdp/explain",
		http.MethodPost + " /pdp/dry-run":
		return governanceBodyDeclaration(governancePDPCandidateSchema()), true
	case http.MethodPost + " /pdp/publish":
		return governanceBodyDeclaration(governancePDPEngineSourceSchema(true)), true
	case http.MethodPost + " /pdp/rollback":
		return governanceBodyDeclaration(governancePDPRollbackSchema()), true
	case http.MethodPost + " /breakglass":
		return governanceBodyDeclaration(governanceBreakGlassActivateSchema()), true
	case http.MethodPost + " /breakglass/consume":
		return governanceBodyDeclaration(governanceBreakGlassConsumeSchema()), true
	case http.MethodPost + " /breakglass/{id}/review":
		return governanceBodyDeclaration(governanceRequiredNoteSchema()), true
	case http.MethodPut + " /nhi/{ref}/ownership":
		return governanceBodyDeclaration(governanceNHIOwnershipSchema()), true
	case http.MethodPut + " /nhi/{ref}/policy":
		return governanceBodyDeclaration(governanceNHIPolicySchema()), true
	case http.MethodPost + " /nhi/{ref}/rotate",
		http.MethodPost + " /nhi/{ref}/offboard",
		http.MethodPost + " /nhi/{ref}/offboard/finalize":
		return governanceOptionalBodyDeclaration(governanceNHIActionSchema()), true
	case http.MethodPost + " /killswitch":
		return governanceBodyDeclaration(governanceKillSwitchEngageSchema()), true
	case http.MethodPost + " /killswitch/{id}/reenable":
		return governanceBodyDeclaration(governanceKillSwitchReenableSchema()), true
	case http.MethodPost + " /killswitch/{id}/review":
		return governanceBodyDeclaration(governanceRequiredNoteSchema()), true
	case http.MethodPost + " /guardian/rules":
		return governanceBodyDeclaration(governanceGuardianCreateSchema()), true
	case http.MethodPut + " /guardian/rules/{id}":
		return governanceBodyDeclaration(governanceGuardianUpdateSchema()), true
	case http.MethodPost + " /rbac/roles":
		return governanceBodyDeclaration(governanceCustomRoleSchema(true)), true
	case http.MethodPut + " /rbac/roles/{name}":
		return governanceBodyDeclaration(governanceCustomRoleSchema(false)), true
	case http.MethodPost + " /rbac/permission-groups":
		return governanceBodyDeclaration(governancePermissionGroupSchema(true)), true
	case http.MethodPut + " /rbac/permission-groups/{name}":
		return governanceBodyDeclaration(governancePermissionGroupSchema(false)), true
	case http.MethodPost + " /rbac/grants":
		return governanceBodyDeclaration(governanceScopedGrantSchema()), true
	case http.MethodPost + " /agent-risk-profiles/classify":
		return governanceBodyDeclaration(governanceAgentRiskClassifySchema()), true
	case http.MethodPut + " /agent-risk-profiles/{id}/tier":
		return governanceBodyDeclaration(governanceAgentRiskTierSchema()), true
	case http.MethodPost + " /routine-policies":
		return governanceBodyDeclaration(governanceRoutinePolicyCreateSchema()), true
	case http.MethodPut + " /routine-policies/{id}":
		return governanceBodyDeclaration(governanceRoutinePolicyUpdateSchema()), true
	case http.MethodPost + " /agentcore-export/plan":
		return governanceBodyDeclaration(governanceAgentCorePlanSchema()), true
	case http.MethodPost + " /agentcore-export/apply":
		return governanceBodyDeclaration(governanceAgentCoreApplySchema()), true

	case http.MethodPost + " /roster/sync",
		http.MethodDelete + " /agents/{agentID}/identity",
		http.MethodDelete + " /policies/{id}",
		http.MethodPost + " /approvals/{id}/cancel",
		http.MethodPost + " /approvals/sweep",
		http.MethodPost + " /breakglass/{id}/revoke",
		http.MethodPost + " /nhi/sweep",
		http.MethodPost + " /nhi/{ref}/restore",
		http.MethodDelete + " /guardian/rules/{id}",
		http.MethodDelete + " /rbac/roles/{name}",
		http.MethodDelete + " /rbac/permission-groups/{name}",
		http.MethodDelete + " /rbac/grants/{id}",
		http.MethodPost + " /agent-risk-profiles/{id}/review",
		http.MethodDelete + " /routine-policies/{id}":
		return governanceRequestBodyDeclaration{kind: governanceBodyless}, true
	default:
		return governanceRequestBodyDeclaration{}, false
	}
}

func governanceBodyDeclaration(schema map[string]any) governanceRequestBodyDeclaration {
	return governanceRequestBodyDeclaration{kind: governanceBodyful, required: true, schema: schema}
}

func governanceOptionalBodyDeclaration(schema map[string]any) governanceRequestBodyDeclaration {
	return governanceRequestBodyDeclaration{kind: governanceBodyful, required: false, schema: schema}
}

func governanceClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func governanceIdentityBindingSchema() map[string]any {
	schema := governanceClosedObject(oaObj(
		"identity_id", oaObj("type", "string"),
		"identity_ref", oaObj("type", "string"),
		"mint", oaObj("type", "boolean"),
		"allow_unknown", oaObj("type", "boolean"),
	))
	// The handler's ordered switch accepts combinations and selects the first
	// populated selector, so this is anyOf rather than an invented oneOf.
	schema["anyOf"] = []any{
		oaObj(
			"required", []string{"identity_id"},
			"properties", oaObj("identity_id", oaObj("type", "string", "minLength", 1)),
		),
		oaObj(
			"required", []string{"identity_ref"},
			"properties", oaObj("identity_ref", oaObj("type", "string", "minLength", 1)),
		),
		oaObj(
			"required", []string{"mint"},
			"properties", oaObj("mint", oaObj("const", true)),
		),
	}
	return schema
}

func governancePolicySchema() map[string]any {
	schema := governanceClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1),
		"kind", oaObj("type", "string", "enum", oaEnum("abac", "approval")),
		"enabled", oaObj("type", "boolean"),
		"spec", oaObj("description", "Kind-specific policy specification."),
	), "name", "kind")
	schema["oneOf"] = []any{
		oaObj(
			"properties", oaObj(
				"kind", oaObj("const", "abac"),
				"spec", governanceABACSpecSchema(),
			),
			"required", []string{"kind", "spec"},
		),
		oaObj(
			"properties", oaObj(
				"kind", oaObj("const", "approval"),
				"spec", oaObj("anyOf", []any{
					governanceApprovalPolicySpecSchema(),
					oaObj("type", "null"),
				}),
			),
			"required", []string{"kind"},
		),
	}
	return schema
}

func governanceABACSpecSchema() map[string]any {
	rule := governanceClosedObject(oaObj(
		"deny", oaObj("type", "boolean", "const", true),
		"permission", oaObj("type", "string", "maxLength", 128),
		"verb", oaObj("type", "string", "enum", oaEnum("", "read", "write", "admin")),
		"resource", oaObj("type", "string", "maxLength", 128),
		"principal_kind", oaObj("type", "string", "enum", oaEnum("", "user", "token")),
		"min_aal", oaObj("type", "integer", "minimum", 0, "maximum", 3),
	), "deny")
	rule["anyOf"] = []any{
		oaObj("required", []string{"permission"}, "properties", oaObj(
			"permission", oaObj("type", "string", "minLength", 1),
		)),
		oaObj("required", []string{"verb"}, "properties", oaObj(
			"verb", oaObj("enum", oaEnum("read", "write", "admin")),
		)),
		oaObj("required", []string{"resource"}, "properties", oaObj(
			"resource", oaObj("type", "string", "minLength", 1),
		)),
		oaObj("required", []string{"principal_kind"}, "properties", oaObj(
			"principal_kind", oaObj("enum", oaEnum("user", "token")),
		)),
		oaObj("required", []string{"min_aal"}, "properties", oaObj(
			"min_aal", oaObj("type", "integer", "minimum", 1),
		)),
	}
	return governanceClosedObject(oaObj(
		"rules", oaObj(
			"type", "array",
			"minItems", 1,
			"maxItems", 64,
			"items", rule,
		),
	), "rules")
}

func governanceApprovalPolicySpecSchema() map[string]any {
	match := governanceClosedObject(oaObj(
		"action", oaObj("type", "string", "maxLength", 128),
		"subject_kind", oaObj("type", "string", "maxLength", 128),
	))
	schema := governanceClosedObject(oaObj(
		"required_approvals", oaObj("type", "integer", "minimum", 0, "maximum", 64),
		"expires_in_seconds", oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"escalate_in_seconds", oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"risk_tier", oaObj("type", "string", "enum", oaEnum("", "low", "medium", "high", "critical")),
		"match", match,
	))
	// For a critical action the handler accepts an omitted/zero threshold (the
	// engine floors it to two) or an explicitly dual-control threshold.
	schema["allOf"] = []any{oaObj(
		"if", oaObj(
			"required", []string{"risk_tier"},
			"properties", oaObj("risk_tier", oaObj("const", "critical")),
		),
		"then", oaObj("anyOf", []any{
			oaObj("not", oaObj("required", []string{"required_approvals"})),
			oaObj("properties", oaObj("required_approvals", oaObj("const", 0))),
			oaObj("properties", oaObj("required_approvals", oaObj("minimum", 2))),
		}),
	)}
	return schema
}

func governanceApprovalCreateSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"subject_kind", oaObj("type", "string", "maxLength", 128),
		"subject_ref", oaObj("type", "string", "maxLength", 4096),
		"action", oaObj("type", "string", "minLength", 1, "maxLength", 128),
		"reason", oaObj("type", "string", "maxLength", 4096),
		"required_approvals", oaObj("type", "integer", "minimum", 0, "maximum", 64),
		"expires_in_seconds", oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"escalate_in_seconds", oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
	), "action")
}

func governanceApprovalDecisionSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"decision", oaObj("type", "string", "enum", oaEnum("approve", "reject")),
		"note", oaObj("type", "string", "maxLength", 4096),
	), "decision")
}

func governanceApprovalConsumeSchema() map[string]any {
	return governanceClosedObject(oaObj(
		"consumer_id", oaObj("type", "string", "minLength", 1, "maxLength", 128),
		"policy_version", oaObj("type", "string", "maxLength", 128),
	), "consumer_id")
}

func governanceAgentRegistrationSchema() map[string]any {
	// handleRegisterAgent uses json.Decoder directly rather than the module's
	// DisallowUnknownFields helper, so unknown properties are part of its observed
	// tolerance and must not be falsely rejected by generated clients.
	return oaObj(
		"type", "object",
		"additionalProperties", true,
		"properties", oaObj(
			"identity_ref", oaObj("type", "string", "minLength", 1),
			"source", oaObj("type", "string"),
			"sponsor_ref", oaObj("type", "string", "minLength", 1),
			"criticality", oaObj("type", "string", "enum", oaEnum("", "low", "medium", "high", "critical")),
		),
		"required", []string{"identity_ref", "sponsor_ref"},
	)
}

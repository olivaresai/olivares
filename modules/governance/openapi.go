// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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
func governanceRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := governanceRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != governanceBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", decl.required,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

// governanceRequestBodyDeclarationFor classifies all 54 mutations registered
// by the governance module. The sibling claude-policy and claude-agents modules
// have different namespaces and are intentionally outside this catalog.
func governanceRequestBodyDeclarationFor(method, pattern string) (governanceRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
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
	case http.MethodPost + " /decisions/replay":
		return governanceBodyDeclaration(governanceDecisionReplaySchema()), true
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
	case http.MethodPost + " /rbac/inheritance-filters":
		return governanceBodyDeclaration(governanceInheritanceFilterSchema()), true
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
		http.MethodDelete + " /pdp/active",
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
		http.MethodDelete + " /rbac/inheritance-filters/{id}",
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
	schema := oas.Obj(
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
	schema := governanceClosedObject(oas.Obj(
		"identity_id", oas.Obj("type", "string"),
		"identity_ref", oas.Obj("type", "string"),
		"mint", oas.Obj("type", "boolean"),
		"allow_unknown", oas.Obj("type", "boolean"),
	))
	// The handler's ordered switch accepts combinations and selects the first
	// populated selector, so this is anyOf rather than an invented oneOf.
	schema["anyOf"] = []any{
		oas.Obj(
			"required", []string{"identity_id"},
			"properties", oas.Obj("identity_id", oas.Obj("type", "string", "minLength", 1)),
		),
		oas.Obj(
			"required", []string{"identity_ref"},
			"properties", oas.Obj("identity_ref", oas.Obj("type", "string", "minLength", 1)),
		),
		oas.Obj(
			"required", []string{"mint"},
			"properties", oas.Obj("mint", oas.Obj("const", true)),
		),
	}
	return schema
}

func governancePolicySchema() map[string]any {
	schema := governanceClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "minLength", 1),
		"kind", oas.Obj("type", "string", "enum", oas.Enum("abac", "approval")),
		"enabled", oas.Obj("type", "boolean"),
		"spec", oas.Obj("description", "Kind-specific policy specification."),
	), "name", "kind")
	schema["oneOf"] = []any{
		oas.Obj(
			"properties", oas.Obj(
				"kind", oas.Obj("const", "abac"),
				"spec", governanceABACSpecSchema(),
			),
			"required", []string{"kind", "spec"},
		),
		oas.Obj(
			"properties", oas.Obj(
				"kind", oas.Obj("const", "approval"),
				"spec", oas.Obj("anyOf", []any{
					governanceApprovalPolicySpecSchema(),
					oas.Obj("type", "null"),
				}),
			),
			"required", []string{"kind"},
		),
	}
	return schema
}

func governanceABACSpecSchema() map[string]any {
	rule := governanceClosedObject(oas.Obj(
		"deny", oas.Obj("type", "boolean", "const", true),
		"permission", oas.Obj("type", "string", "maxLength", 128),
		"verb", oas.Obj("type", "string", "enum", oas.Enum("", "read", "write", "admin")),
		"resource", oas.Obj("type", "string", "maxLength", 128),
		"principal_kind", oas.Obj("type", "string", "enum", oas.Enum("", "user", "token")),
		"min_aal", oas.Obj("type", "integer", "minimum", 0, "maximum", 3),
	), "deny")
	rule["anyOf"] = []any{
		oas.Obj("required", []string{"permission"}, "properties", oas.Obj(
			"permission", oas.Obj("type", "string", "minLength", 1),
		)),
		oas.Obj("required", []string{"verb"}, "properties", oas.Obj(
			"verb", oas.Obj("enum", oas.Enum("read", "write", "admin")),
		)),
		oas.Obj("required", []string{"resource"}, "properties", oas.Obj(
			"resource", oas.Obj("type", "string", "minLength", 1),
		)),
		oas.Obj("required", []string{"principal_kind"}, "properties", oas.Obj(
			"principal_kind", oas.Obj("enum", oas.Enum("user", "token")),
		)),
		oas.Obj("required", []string{"min_aal"}, "properties", oas.Obj(
			"min_aal", oas.Obj("type", "integer", "minimum", 1),
		)),
	}
	return governanceClosedObject(oas.Obj(
		"rules", oas.Obj(
			"type", "array",
			"minItems", 1,
			"maxItems", 64,
			"items", rule,
		),
	), "rules")
}

func governanceApprovalPolicySpecSchema() map[string]any {
	match := governanceClosedObject(oas.Obj(
		"action", oas.Obj("type", "string", "maxLength", 128),
		"subject_kind", oas.Obj("type", "string", "maxLength", 128),
	))
	schema := governanceClosedObject(oas.Obj(
		"required_approvals", oas.Obj("type", "integer", "minimum", 0, "maximum", 64),
		"expires_in_seconds", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"escalate_in_seconds", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"risk_tier", oas.Obj("type", "string", "enum", oas.Enum("", "low", "medium", "high", "critical")),
		"match", match,
	))
	// For a critical action the handler accepts an omitted/zero threshold (the
	// engine floors it to two) or an explicitly dual-control threshold.
	schema["allOf"] = []any{oas.Obj(
		"if", oas.Obj(
			"required", []string{"risk_tier"},
			"properties", oas.Obj("risk_tier", oas.Obj("const", "critical")),
		),
		"then", oas.Obj("anyOf", []any{
			oas.Obj("not", oas.Obj("required", []string{"required_approvals"})),
			oas.Obj("properties", oas.Obj("required_approvals", oas.Obj("const", 0))),
			oas.Obj("properties", oas.Obj("required_approvals", oas.Obj("minimum", 2))),
		}),
	)}
	return schema
}

func governanceApprovalCreateSchema() map[string]any {
	return governanceClosedObject(oas.Obj(
		"subject_kind", oas.Obj("type", "string", "maxLength", 128),
		"subject_ref", oas.Obj("type", "string", "maxLength", 4096),
		"action", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
		"reason", oas.Obj("type", "string", "maxLength", 4096),
		"required_approvals", oas.Obj("type", "integer", "minimum", 0, "maximum", 64),
		"expires_in_seconds", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
		"escalate_in_seconds", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 31536000),
	), "action")
}

func governanceApprovalDecisionSchema() map[string]any {
	return governanceClosedObject(oas.Obj(
		"decision", oas.Obj("type", "string", "enum", oas.Enum("approve", "reject")),
		"note", oas.Obj("type", "string", "maxLength", 4096),
	), "decision")
}

func governanceApprovalConsumeSchema() map[string]any {
	return governanceClosedObject(oas.Obj(
		"consumer_id", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
		"policy_version", oas.Obj("type", "string", "maxLength", 128),
	), "consumer_id")
}

func governanceDecisionReplaySchema() map[string]any {
	// handleReplayDecision decodes reconstructBody with unknown fields refused.
	// DecisionID, when non-blank, selects that stored row; otherwise At,
	// Principal and Action are required. JSON null is not accepted into the
	// string fields, so omitted (empty string after trim) is the only absence.
	schema := governanceClosedObject(oas.Obj(
		"action", oas.Obj("type", "string"),
		"action_vocabulary", oas.Obj("type", "string"),
		"at", oas.Obj("type", "string"),
		"decision_id", oas.Obj("type", "string"),
		"principal", oas.Obj("type", "string"),
		"resource", oas.Obj("type", "string"),
		"resource_kind", oas.Obj("type", "string"),
		"source_instance", oas.Obj("type", "string"),
	))
	schema["anyOf"] = []any{
		oas.Obj(
			"required", []string{"decision_id"},
			"properties", oas.Obj("decision_id", oas.Obj("type", "string", "minLength", 1)),
		),
		oas.Obj(
			"required", []string{"action", "at", "principal"},
			"properties", oas.Obj(
				"action", oas.Obj("type", "string", "minLength", 1),
				"at", oas.Obj("type", "string", "minLength", 1),
				"principal", oas.Obj("type", "string", "minLength", 1),
			),
		),
	}
	return schema
}

func governanceAgentRegistrationSchema() map[string]any {
	// handleRegisterAgent uses json.Decoder directly rather than the module's
	// DisallowUnknownFields helper, so unknown properties are part of its observed
	// tolerance and must not be falsely rejected by generated clients.
	return oas.Obj(
		"type", "object",
		"additionalProperties", true,
		"properties", oas.Obj(
			"identity_ref", oas.Obj("type", "string", "minLength", 1),
			"source", oas.Obj("type", "string"),
			"sponsor_ref", oas.Obj("type", "string", "minLength", 1),
			"criticality", oas.Obj("type", "string", "enum", oas.Enum("", "low", "medium", "high", "critical")),
		),
		"required", []string{"identity_ref", "sponsor_ref"},
	)
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := governanceRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case governanceBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = governanceRequestBody(method, pattern)
	case governanceBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case governanceBodyNoDerivable, governanceBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

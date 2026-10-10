// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type inferenceProxyRequestBodyKind uint8

const (
	inferenceProxyBodyless inferenceProxyRequestBodyKind = iota + 1
	inferenceProxyBodyful
	inferenceProxyBodyNoDerivable
	inferenceProxyBodyPending
)

type inferenceProxyRequestBodyDeclaration struct {
	kind   inferenceProxyRequestBodyKind
	schema map[string]any
}

func inferenceProxyRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := inferenceProxyRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != inferenceProxyBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func inferenceProxyRequestBodyDeclarationFor(method, pattern string) (inferenceProxyRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPut + " /config":
		return inferenceProxyBodyDeclaration(inferenceProxyConfigSchema()), true
	case http.MethodPost + " /device/approve":
		return inferenceProxyBodyDeclaration(inferenceProxyDeviceApprovalSchema()), true
	case http.MethodPut + " /dlp/rules":
		return inferenceProxyBodyDeclaration(inferenceProxyDLPRuleSchema()), true
	case http.MethodDelete + " /dlp/rules/{id}":
		return inferenceProxyRequestBodyDeclaration{kind: inferenceProxyBodyless}, true
	default:
		return inferenceProxyRequestBodyDeclaration{}, false
	}
}

func inferenceProxyBodyDeclaration(schema map[string]any) inferenceProxyRequestBodyDeclaration {
	return inferenceProxyRequestBodyDeclaration{kind: inferenceProxyBodyful, schema: schema}
}

func inferenceProxyNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func inferenceProxyClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func inferenceProxyConfigSchema() map[string]any {
	optionalBool := func() map[string]any { return inferenceProxyNullable(oas.Obj("type", "boolean")) }
	optionalNonNegative := func() map[string]any {
		return inferenceProxyNullable(oas.Obj("type", "integer", "minimum", 0))
	}
	taskBudget := inferenceProxyNullable(oas.Obj(
		"anyOf", []any{
			oas.Obj("type", "integer", "const", 0),
			oas.Obj("type", "integer", "minimum", 20000),
		},
	))
	schema := inferenceProxyClosedObject(oas.Obj(
		"fail_open", optionalBool(),
		"response_dlp_mode", inferenceProxyNullable(oas.Obj("type", "string", "description", "After trimming, empty is accepted or the value must be exactly off, flag or buffer.")),
		"record_mandatory", optionalBool(),
		"gate_model_access", optionalBool(),
		"gate_budget", optionalBool(),
		"gate_residency", optionalBool(),
		"gate_context_window", optionalBool(),
		"gate_dlp_request", optionalBool(),
		"gate_dlp_response", optionalBool(),
		"ceilings_enforce", optionalBool(),
		"ceiling_max_tokens", optionalNonNegative(),
		"ceiling_max_tool_uses", optionalNonNegative(),
		"ceiling_task_budget_tokens", taskBudget,
	))
	schema["allOf"] = []any{oas.Obj(
		"if", oas.Obj(
			"required", oas.Enum("ceilings_enforce"),
			"properties", oas.Obj("ceilings_enforce", oas.Obj("const", true)),
		),
		"then", oas.Obj("anyOf", []any{
			oas.Obj("required", oas.Enum("ceiling_max_tokens"), "properties", oas.Obj("ceiling_max_tokens", oas.Obj("type", "integer", "minimum", 1))),
			oas.Obj("required", oas.Enum("ceiling_max_tool_uses"), "properties", oas.Obj("ceiling_max_tool_uses", oas.Obj("type", "integer", "minimum", 1))),
			oas.Obj("required", oas.Enum("ceiling_task_budget_tokens"), "properties", oas.Obj("ceiling_task_budget_tokens", oas.Obj("type", "integer", "minimum", 20000))),
		}),
	)}
	return schema
}

func inferenceProxyDeviceApprovalSchema() map[string]any {
	return inferenceProxyClosedObject(oas.Obj(
		"user_code", inferenceProxyNullable(oas.Obj("type", "string", "description", "Normalized by the handler; empty resolves as not found rather than a decode error.")),
		"deny", inferenceProxyNullable(oas.Obj("type", "boolean")),
	))
}

// inferenceProxyContentFirewallRoute is the read of the Messages proxy's startup
// content-inspector attachment.
func inferenceProxyContentFirewallRoute(method, pattern string) bool {
	return method == http.MethodGet && pattern == "/content-firewall"
}

func inferenceProxyContentFirewallResponse() map[string]any {
	return oas.Obj(
		"description", "Startup attachment state of the inline Messages proxy content inspector in this process.",
		"content", oas.Obj("application/json", oas.Obj("schema", inferenceProxyContentFirewallSchema())),
	)
}

func inferenceProxyContentFirewallSchema() map[string]any {
	return inferenceProxyClosedObject(oas.Obj(
		"pep", oas.Obj("type", "string", "enum", oas.Enum("messages_proxy"),
			"description", "The only proxy this state describes."),
		"state", oas.Obj("type", "string",
			"enum", oas.Enum("unobserved", "pep_not_composed", "inspector_absent", "inspector_attached"),
			"description", "inspector_attached includes the deny-all fallback inspector. No state reports listener health, policy load or per-request inspection."),
		"note", oas.Obj("type", "string", "description", "Constant scope note."),
	), "pep", "state", "note")
}

func inferenceProxyDLPRuleSchema() map[string]any {
	return inferenceProxyClosedObject(oas.Obj(
		"id", inferenceProxyNullable(oas.Obj("type", "string", "description", "Accepted by the DTO but ignored on upsert.")),
		"class", oas.Obj("type", "string", "description", "After trimming and lowercasing, must be non-empty and at most 64 UTF-8 bytes."),
		"action", oas.Obj("type", "string", "description", "After trimming and lowercasing, must be allow or deny."),
		"note", inferenceProxyNullable(oas.Obj("type", "string", "description", "At most 512 UTF-8 bytes.")),
		"created_by", inferenceProxyNullable(oas.Obj("type", "string", "description", "Accepted by the DTO but replaced by the authenticated actor.")),
	), "class", "action")
}

// OperationDocumentation publishes the request body each mutation's handler
// decodes and the content-firewall read's typed answer. A route this module has
// not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if inferenceProxyContentFirewallRoute(method, pattern) {
		responses := api.GenericModuleResponses()
		responses["200"] = inferenceProxyContentFirewallResponse()
		return api.ModuleOperationDocumentation{Responses: responses}, true
	}
	decl, ok := inferenceProxyRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case inferenceProxyBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = inferenceProxyRequestBody(method, pattern)
	case inferenceProxyBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case inferenceProxyBodyNoDerivable, inferenceProxyBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

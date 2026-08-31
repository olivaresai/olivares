// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func inferenceProxyRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := inferenceProxyRequestBodyDeclarationFor(r)
	if !ok || decl.kind != inferenceProxyBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func inferenceProxyRequestBodyDeclarationFor(r moduleRoute) (inferenceProxyRequestBodyDeclaration, bool) {
	if r.ns != "inferenceproxy" {
		return inferenceProxyRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
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
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func inferenceProxyClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func inferenceProxyConfigSchema() map[string]any {
	optionalBool := func() map[string]any { return inferenceProxyNullable(oaObj("type", "boolean")) }
	optionalNonNegative := func() map[string]any {
		return inferenceProxyNullable(oaObj("type", "integer", "minimum", 0))
	}
	taskBudget := inferenceProxyNullable(oaObj(
		"anyOf", []any{
			oaObj("type", "integer", "const", 0),
			oaObj("type", "integer", "minimum", 20000),
		},
	))
	schema := inferenceProxyClosedObject(oaObj(
		"fail_open", optionalBool(),
		"response_dlp_mode", inferenceProxyNullable(oaObj("type", "string", "description", "After trimming, empty is accepted or the value must be exactly off, flag or buffer.")),
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
	schema["allOf"] = []any{oaObj(
		"if", oaObj(
			"required", oaEnum("ceilings_enforce"),
			"properties", oaObj("ceilings_enforce", oaObj("const", true)),
		),
		"then", oaObj("anyOf", []any{
			oaObj("required", oaEnum("ceiling_max_tokens"), "properties", oaObj("ceiling_max_tokens", oaObj("type", "integer", "minimum", 1))),
			oaObj("required", oaEnum("ceiling_max_tool_uses"), "properties", oaObj("ceiling_max_tool_uses", oaObj("type", "integer", "minimum", 1))),
			oaObj("required", oaEnum("ceiling_task_budget_tokens"), "properties", oaObj("ceiling_task_budget_tokens", oaObj("type", "integer", "minimum", 20000))),
		}),
	)}
	return schema
}

func inferenceProxyDeviceApprovalSchema() map[string]any {
	return inferenceProxyClosedObject(oaObj(
		"user_code", inferenceProxyNullable(oaObj("type", "string", "description", "Normalized by the handler; empty resolves as not found rather than a decode error.")),
		"deny", inferenceProxyNullable(oaObj("type", "boolean")),
	))
}

func inferenceProxyDLPRuleSchema() map[string]any {
	return inferenceProxyClosedObject(oaObj(
		"id", inferenceProxyNullable(oaObj("type", "string", "description", "Accepted by the DTO but ignored on upsert.")),
		"class", oaObj("type", "string", "description", "After trimming and lowercasing, must be non-empty and at most 64 UTF-8 bytes."),
		"action", oaObj("type", "string", "description", "After trimming and lowercasing, must be allow or deny."),
		"note", inferenceProxyNullable(oaObj("type", "string", "description", "At most 512 UTF-8 bytes.")),
		"created_by", inferenceProxyNullable(oaObj("type", "string", "description", "Accepted by the DTO but replaced by the authenticated actor.")),
	), "class", "action")
}

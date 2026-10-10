// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type healthRequestBodyKind uint8

const (
	healthBodyless healthRequestBodyKind = iota + 1
	healthBodyful
	healthBodyNoDerivable
	healthBodyPending
)

type healthRequestBodyDeclaration struct {
	kind   healthRequestBodyKind
	schema map[string]any
}

func healthRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := healthRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != healthBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func healthRequestBodyDeclarationFor(method, pattern string) (healthRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /checks":
		return healthBodyDeclaration(healthCreateCheckSchema()), true
	case http.MethodPut + " /checks/{id}":
		return healthBodyDeclaration(healthUpdateCheckSchema()), true
	case http.MethodPost + " /checks/{id}/report":
		return healthBodyDeclaration(healthReportSchema()), true
	case http.MethodDelete + " /checks/{id}", http.MethodPost + " /incidents/{id}/resolve":
		return healthRequestBodyDeclaration{kind: healthBodyless}, true
	default:
		return healthRequestBodyDeclaration{}, false
	}
}

func healthBodyDeclaration(schema map[string]any) healthRequestBodyDeclaration {
	return healthRequestBodyDeclaration{kind: healthBodyful, schema: schema}
}

func healthNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func healthClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func healthLifecycleSchema() map[string]any {
	return healthNullable(oas.Obj("type", "string", "enum", oas.Enum("", "active", "paused", "retired"), "description", "Empty keeps/defaults the lifecycle state; non-empty values are validated exactly."))
}

func healthCreateCheckSchema() map[string]any {
	return healthClosedObject(oas.Obj(
		"name", healthNullable(oas.Obj("type", "string", "description", "The handler stores this value after applying its byte cap.")),
		"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("agent", "mcp")),
		"subject_ref", oas.Obj("type", "string", "minLength", 1, "description", "Required exactly as decoded; the stored value is byte-capped."),
		"expected_interval_seconds", healthNullable(oas.Obj("type", "integer", "description", "Values at or below zero select the server default.")),
		"grace_factor", healthNullable(oas.Obj("type", "integer", "description", "Values at or below zero select the server default.")),
		"sla_target_ppm", healthNullable(oas.Obj("type", "integer", "description", "Clamped by the handler to the inclusive range 0..1000000.")),
		"desired_status", healthLifecycleSchema(),
	), "subject_kind", "subject_ref")
}

func healthUpdateCheckSchema() map[string]any {
	return healthClosedObject(oas.Obj(
		"name", healthNullable(oas.Obj("type", "string", "description", "Empty keeps the stored name; non-empty values are byte-capped.")),
		"expected_interval_seconds", healthNullable(oas.Obj("type", "integer", "description", "Only positive values replace the stored interval.")),
		"grace_factor", healthNullable(oas.Obj("type", "integer", "description", "Only positive values replace the stored grace factor.")),
		"sla_target_ppm", healthNullable(oas.Obj("type", "integer", "description", "A number is clamped to 0..1000000; omitted or null keeps the stored target.")),
		"desired_status", healthLifecycleSchema(),
	))
}

func healthReportSchema() map[string]any {
	return healthClosedObject(oas.Obj(
		"state", oas.Obj("type", "string", "enum", oas.Enum("healthy", "degraded", "down")),
		"latency_ms", healthNullable(oas.Obj("type", "integer", "description", "Negative values are normalized to the unknown sentinel -1.")),
		"detail", healthNullable(oas.Obj("type", "string", "description", "Optional short probe detail; JSON null decodes as empty.")),
	), "state")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := healthRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case healthBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = healthRequestBody(method, pattern)
	case healthBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case healthBodyNoDerivable, healthBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

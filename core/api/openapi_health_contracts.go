// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func healthRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := healthRequestBodyDeclarationFor(r)
	if !ok || decl.kind != healthBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func healthRequestBodyDeclarationFor(r moduleRoute) (healthRequestBodyDeclaration, bool) {
	if r.ns != "health" {
		return healthRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
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
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func healthClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func healthLifecycleSchema() map[string]any {
	return healthNullable(oaObj("type", "string", "enum", oaEnum("", "active", "paused", "retired"), "description", "Empty keeps/defaults the lifecycle state; non-empty values are validated exactly."))
}

func healthCreateCheckSchema() map[string]any {
	return healthClosedObject(oaObj(
		"name", healthNullable(oaObj("type", "string", "description", "The handler stores this value after applying its byte cap.")),
		"subject_kind", oaObj("type", "string", "enum", oaEnum("agent", "mcp")),
		"subject_ref", oaObj("type", "string", "minLength", 1, "description", "Required exactly as decoded; the stored value is byte-capped."),
		"expected_interval_seconds", healthNullable(oaObj("type", "integer", "description", "Values at or below zero select the server default.")),
		"grace_factor", healthNullable(oaObj("type", "integer", "description", "Values at or below zero select the server default.")),
		"sla_target_ppm", healthNullable(oaObj("type", "integer", "description", "Clamped by the handler to the inclusive range 0..1000000.")),
		"desired_status", healthLifecycleSchema(),
	), "subject_kind", "subject_ref")
}

func healthUpdateCheckSchema() map[string]any {
	return healthClosedObject(oaObj(
		"name", healthNullable(oaObj("type", "string", "description", "Empty keeps the stored name; non-empty values are byte-capped.")),
		"expected_interval_seconds", healthNullable(oaObj("type", "integer", "description", "Only positive values replace the stored interval.")),
		"grace_factor", healthNullable(oaObj("type", "integer", "description", "Only positive values replace the stored grace factor.")),
		"sla_target_ppm", healthNullable(oaObj("type", "integer", "description", "A number is clamped to 0..1000000; omitted or null keeps the stored target.")),
		"desired_status", healthLifecycleSchema(),
	))
}

func healthReportSchema() map[string]any {
	return healthClosedObject(oaObj(
		"state", oaObj("type", "string", "enum", oaEnum("healthy", "degraded", "down")),
		"latency_ms", healthNullable(oaObj("type", "integer", "description", "Negative values are normalized to the unknown sentinel -1.")),
		"detail", healthNullable(oaObj("type", "string", "description", "Optional short probe detail; JSON null decodes as empty.")),
	), "state")
}

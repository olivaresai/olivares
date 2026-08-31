// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type sandboxRequestBodyKind uint8

const (
	sandboxBodyless sandboxRequestBodyKind = iota + 1
	sandboxBodyful
	sandboxBodyNoDerivable
	sandboxBodyPending
)

type sandboxRequestBodyDeclaration struct {
	kind   sandboxRequestBodyKind
	schema map[string]any
}

func sandboxRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := sandboxRequestBodyDeclarationFor(r)
	if !ok || decl.kind != sandboxBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func sandboxRequestBodyDeclarationFor(r moduleRoute) (sandboxRequestBodyDeclaration, bool) {
	if r.ns != "sandbox" {
		return sandboxRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /scenarios":
		return sandboxBodyDeclaration(sandboxCreateScenarioSchema()), true
	case http.MethodPost + " /scenarios/{id}/run":
		return sandboxBodyDeclaration(sandboxRunScenarioSchema()), true
	case http.MethodPost + " /replay":
		return sandboxBodyDeclaration(sandboxReplaySchema()), true
	case http.MethodPost + " /compare":
		return sandboxBodyDeclaration(sandboxCompareSchema()), true
	case http.MethodPost + " /scenarios/{id}/archive":
		return sandboxRequestBodyDeclaration{kind: sandboxBodyless}, true
	default:
		return sandboxRequestBodyDeclaration{}, false
	}
}

func sandboxBodyDeclaration(schema map[string]any) sandboxRequestBodyDeclaration {
	return sandboxRequestBodyDeclaration{kind: sandboxBodyful, schema: schema}
}

func sandboxNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func sandboxClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func sandboxStepsSchema() map[string]any {
	step := sandboxClosedObject(oaObj(
		"key", sandboxNullable(oaObj("type", "string", "description", "Byte-capped before execution and persistence.")),
		"input", sandboxNullable(oaObj("type", "string", "description", "Byte-capped before execution and persistence.")),
	))
	return sandboxNullable(oaObj("type", "array", "items", sandboxNullable(step)))
}

func sandboxMocksSchema() map[string]any {
	mock := sandboxClosedObject(oaObj(
		"resource", sandboxNullable(oaObj("type", "string", "description", "Byte-capped before execution and persistence.")),
		"response", sandboxNullable(oaObj("type", "string", "description", "Byte-capped before execution and persistence.")),
	))
	return sandboxNullable(oaObj("type", "array", "items", sandboxNullable(mock)))
}

func sandboxCreateScenarioSchema() map[string]any {
	return sandboxClosedObject(oaObj(
		"name", oaObj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"description", sandboxNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"subject_kind", sandboxNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"steps", sandboxStepsSchema(),
		"mocks", sandboxMocksSchema(),
	), "name")
}

func sandboxRunScenarioSchema() map[string]any {
	return sandboxClosedObject(oaObj(
		"variant", sandboxNullable(oaObj("type", "string")),
		"suite_ref", sandboxNullable(oaObj("type", "string", "description", "Trimmed before optional scoring.")),
	))
}

func sandboxReplaySchema() map[string]any {
	return sandboxClosedObject(oaObj(
		"session_ref", oaObj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"mocks", sandboxMocksSchema(),
		"suite_ref", sandboxNullable(oaObj("type", "string", "description", "Trimmed before optional scoring.")),
	), "session_ref")
}

func sandboxCompareSchema() map[string]any {
	schema := sandboxClosedObject(oaObj(
		"scenario_ref", sandboxNullable(oaObj("type", "string", "description", "When non-blank, must parse as a scenario identifier.")),
		"session_ref", sandboxNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"baseline_variant", oaObj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"candidate_variant", oaObj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"suite_ref", sandboxNullable(oaObj("type", "string", "description", "Trimmed before optional scoring.")),
	), "baseline_variant", "candidate_variant")
	schema["anyOf"] = []any{
		oaObj("required", oaEnum("scenario_ref"), "properties", oaObj("scenario_ref", oaObj("type", "string", "minLength", 1))),
		oaObj("required", oaEnum("session_ref"), "properties", oaObj("session_ref", oaObj("type", "string", "minLength", 1))),
	}
	return schema
}

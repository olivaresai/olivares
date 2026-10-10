// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandbox

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func sandboxRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := sandboxRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != sandboxBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func sandboxRequestBodyDeclarationFor(method, pattern string) (sandboxRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /synthetic-data":
		return sandboxBodyDeclaration(sandboxClosedObject(oas.Obj(
			"subject_kind", sandboxNullable(oas.Obj("type", "string", "description", "At most 200 UTF-8 bytes. Empty defaults to agent.")),
			"count", sandboxNullable(oas.Obj("type", "integer", "minimum", 0, "maximum", 100, "description", "Zero defaults to 10 samples.")),
			"seed", sandboxNullable(oas.Obj("type", "string", "description", "At most 8192 UTF-8 bytes. Expands only {{index}} and {{subject_kind}}; each output must fit in 8192 bytes. Empty defaults to {{subject_kind}}-sample-{{index}}.")),
		))), true
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
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func sandboxClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func sandboxStepsSchema() map[string]any {
	step := sandboxClosedObject(oas.Obj(
		"key", sandboxNullable(oas.Obj("type", "string", "description", "Byte-capped before execution and persistence.")),
		"input", sandboxNullable(oas.Obj("type", "string", "description", "Byte-capped before execution and persistence.")),
	))
	return sandboxNullable(oas.Obj("type", "array", "items", sandboxNullable(step)))
}

func sandboxMocksSchema() map[string]any {
	mock := sandboxClosedObject(oas.Obj(
		"resource", sandboxNullable(oas.Obj("type", "string", "description", "Byte-capped before execution and persistence.")),
		"response", sandboxNullable(oas.Obj("type", "string", "description", "Byte-capped before execution and persistence.")),
	))
	return sandboxNullable(oas.Obj("type", "array", "items", sandboxNullable(mock)))
}

func sandboxCreateScenarioSchema() map[string]any {
	return sandboxClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"description", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"subject_kind", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"steps", sandboxStepsSchema(),
		"mocks", sandboxMocksSchema(),
	), "name")
}

func sandboxRunScenarioSchema() map[string]any {
	return sandboxClosedObject(oas.Obj(
		"variant", sandboxNullable(oas.Obj("type", "string")),
		"suite_ref", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed before optional scoring.")),
	))
}

func sandboxReplaySchema() map[string]any {
	return sandboxClosedObject(oas.Obj(
		"session_ref", oas.Obj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"mocks", sandboxMocksSchema(),
		"suite_ref", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed before optional scoring.")),
	), "session_ref")
}

func sandboxCompareSchema() map[string]any {
	schema := sandboxClosedObject(oas.Obj(
		"scenario_ref", sandboxNullable(oas.Obj("type", "string", "description", "When non-blank, must parse as a scenario identifier.")),
		"session_ref", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"baseline_variant", oas.Obj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"candidate_variant", oas.Obj("type", "string", "description", "After trimming and byte-capping, must be non-empty."),
		"suite_ref", sandboxNullable(oas.Obj("type", "string", "description", "Trimmed before optional scoring.")),
	), "baseline_variant", "candidate_variant")
	schema["anyOf"] = []any{
		oas.Obj("required", oas.Enum("scenario_ref"), "properties", oas.Obj("scenario_ref", oas.Obj("type", "string", "minLength", 1))),
		oas.Obj("required", oas.Enum("session_ref"), "properties", oas.Obj("session_ref", oas.Obj("type", "string", "minLength", 1))),
	}
	return schema
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := sandboxRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case sandboxBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = sandboxRequestBody(method, pattern)
	case sandboxBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case sandboxBodyNoDerivable, sandboxBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type evalsRequestBodyKind uint8

const (
	evalsBodyless evalsRequestBodyKind = iota + 1
	evalsBodyful
	evalsBodyNoDerivable
	evalsBodyPending
)

type evalsRequestBodyDeclaration struct {
	kind   evalsRequestBodyKind
	schema map[string]any
}

// evalsRequestBody returns the JSON request body proven by the registered
// evals handler. All ten bodyful routes decode unconditionally, so their HTTP
// requestBody is required even when every property in the DTO is optional.
func evalsRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := evalsRequestBodyDeclarationFor(r)
	if !ok || decl.kind != evalsBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

// evalsRequestBodyDeclarationFor classifies every mutation registered by the
// evals module. There are no opaque packager seams in this surface: ten handlers
// have complete DTOs and the archive action is bodyless.
func evalsRequestBodyDeclarationFor(r moduleRoute) (evalsRequestBodyDeclaration, bool) {
	if r.ns != "evals" {
		return evalsRequestBodyDeclaration{}, false
	}

	switch r.method + " " + r.pattern {
	case http.MethodPost + " /suites":
		return evalsBodyDeclaration(evalsCreateSuiteSchema()), true
	case http.MethodPost + " /suites/{id}/cases":
		return evalsBodyDeclaration(evalsAddCaseSchema()), true
	case http.MethodPost + " /runs":
		return evalsBodyDeclaration(evalsLaunchRunSchema()), true
	case http.MethodPost + " /ab":
		return evalsBodyDeclaration(evalsABSchema()), true
	case http.MethodPost + " /monitor":
		return evalsBodyDeclaration(evalsMonitorSchema()), true
	case http.MethodPost + " /baselines":
		return evalsBodyDeclaration(evalsPinBaselineSchema()), true
	case http.MethodPost + " /calibration/items":
		return evalsBodyDeclaration(evalsCalibrationItemsSchema()), true
	case http.MethodPost + " /calibration/run":
		return evalsBodyDeclaration(evalsRunCalibrationSchema()), true
	case http.MethodPost + " /gate":
		return evalsBodyDeclaration(evalsGateSchema()), true
	case http.MethodPost + " /gate/{id}/override":
		return evalsBodyDeclaration(evalsOverrideGateSchema()), true
	case http.MethodPost + " /suites/{id}/archive":
		return evalsRequestBodyDeclaration{kind: evalsBodyless}, true
	default:
		return evalsRequestBodyDeclaration{}, false
	}
}

func evalsBodyDeclaration(schema map[string]any) evalsRequestBodyDeclaration {
	return evalsRequestBodyDeclaration{kind: evalsBodyful, schema: schema}
}

func evalsClosedObject(properties map[string]any, required ...string) map[string]any {
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

func evalsStringMapSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", oaObj("type", "string"),
	)
}

func evalsCreateSuiteSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"name", oaObj("type", "string", "minLength", 1),
		"description", oaObj("type", "string"),
		"subject_kind", oaObj("type", "string", "enum", oaEnum("agent", "model", "prompt", "session", "sandbox_run")),
		"scorer", oaObj("type", "string", "minLength", 1),
		"criterion", oaObj("type", "string"),
		"pass_threshold", oaObj("type", "number"),
		"regression_threshold", oaObj("type", "number"),
		"judge_model", oaObj("type", "string"),
		"suite_version", oaObj("type", "integer", "format", "int64"),
	), "name", "subject_kind", "scorer")
}

func evalsAddCaseSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"case_key", oaObj("type", "string", "minLength", 1),
		"input", oaObj("type", "string"),
		"expected", oaObj("type", "string"),
		"weight", oaObj("type", "number"),
		"metadata", oaObj("type", "object", "additionalProperties", true),
	), "case_key")
}

func evalsLaunchRunSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"suite_ref", oaObj("type", "string", "minLength", 1),
		"subject_kind", oaObj("type", "string"),
		"subject_ref", oaObj("type", "string"),
		"model_ref", oaObj("type", "string"),
		"prompt_variant", oaObj("type", "string"),
		"baseline_ref", oaObj("type", "string"),
		"outputs", evalsStringMapSchema(),
	), "suite_ref", "outputs")
}

func evalsABSchema() map[string]any {
	variant := evalsClosedObject(oaObj(
		"label", oaObj("type", "string"),
		"outputs", evalsStringMapSchema(),
	), "outputs")
	return evalsClosedObject(oaObj(
		"suite_ref", oaObj("type", "string", "minLength", 1),
		"subject_kind", oaObj("type", "string"),
		"subject_ref", oaObj("type", "string"),
		"a", variant,
		"b", variant,
		"pairwise", oaObj("type", "boolean"),
	), "suite_ref", "a", "b")
}

func evalsMonitorSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"subject_kind", oaObj("type", "string"),
		"subject_ref", oaObj("type", "string"),
		"suite", oaObj("type", "string"),
		"limit", oaObj("type", "integer"),
	))
}

func evalsPinBaselineSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"suite_ref", oaObj("type", "string", "minLength", 1),
		"subject_ref", oaObj("type", "string"),
		"run_ref", oaObj("type", "string", "minLength", 1),
	), "suite_ref", "run_ref")
}

func evalsCalibrationItemsSchema() map[string]any {
	item := evalsClosedObject(oaObj(
		"case_key", oaObj("type", "string", "minLength", 1),
		"input", oaObj("type", "string"),
		"output", oaObj("type", "string", "minLength", 1),
		"expected", oaObj("type", "string"),
		"criterion", oaObj("type", "string"),
		"human_passed", oaObj("type", "boolean"),
		"human_score", oaObj("anyOf", []any{
			oaObj("type", "number", "minimum", 0, "maximum", 1),
			oaObj("type", "null"),
		}),
		"notes", oaObj("type", "string"),
	), "case_key", "output")
	return evalsClosedObject(oaObj(
		"set_name", oaObj("type", "string"),
		"items", oaObj(
			"type", "array",
			"minItems", 1,
			"items", item,
		),
	), "items")
}

func evalsRunCalibrationSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"set_name", oaObj("type", "string"),
		"judge_model", oaObj("type", "string"),
		"target", oaObj("type", "number"),
		"kappa_floor", oaObj("type", "number"),
	))
}

func evalsGateSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"suite_ref", oaObj("type", "string", "minLength", 1),
		"subject_kind", oaObj("type", "string"),
		"subject_ref", oaObj("type", "string"),
		"baseline_ref", oaObj("type", "string"),
		"outputs", evalsStringMapSchema(),
		"seed", oaObj("type", "string"),
		"sample_size", oaObj("type", "integer"),
	), "suite_ref", "outputs")
}

func evalsOverrideGateSchema() map[string]any {
	return evalsClosedObject(oaObj(
		"reason", oaObj("type", "string", "minLength", 1),
	), "reason")
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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
func evalsRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := evalsRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != evalsBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

// evalsRequestBodyDeclarationFor classifies every mutation registered by the
// evals module. There are no opaque packager seams in this surface: ten handlers
// have complete DTOs and the archive action is bodyless.
func evalsRequestBodyDeclarationFor(method, pattern string) (evalsRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
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

func evalsStringMapSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", oas.Obj("type", "string"),
	)
}

func evalsCreateSuiteSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "minLength", 1),
		"description", oas.Obj("type", "string"),
		"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("agent", "model", "prompt", "session", "sandbox_run")),
		"scorer", oas.Obj("type", "string", "minLength", 1),
		"criterion", oas.Obj("type", "string"),
		"pass_threshold", oas.Obj("type", "number"),
		"regression_threshold", oas.Obj("type", "number"),
		"judge_model", oas.Obj("type", "string"),
		"suite_version", oas.Obj("type", "integer", "format", "int64"),
	), "name", "subject_kind", "scorer")
}

func evalsAddCaseSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"case_key", oas.Obj("type", "string", "minLength", 1),
		"input", oas.Obj("type", "string"),
		"expected", oas.Obj("type", "string"),
		"weight", oas.Obj("type", "number"),
		"metadata", oas.Obj("type", "object", "additionalProperties", true),
	), "case_key")
}

func evalsComparisonSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"version", oas.Obj("type", "integer", "enum", []any{1}),
		"mode", oas.Obj("type", "string", "enum", oas.Enum("same_candidate", "candidate_change"), "description", "candidate_change requires an explicit baseline_ref and declared candidate identities."),
	), "version", "mode")
}

func evalsLaunchRunSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"suite_ref", oas.Obj("type", "string", "minLength", 1),
		"subject_kind", oas.Obj("type", "string"),
		"subject_ref", oas.Obj("type", "string"),
		"model_ref", oas.Obj("type", "string"),
		"prompt_variant", oas.Obj("type", "string"),
		"baseline_ref", oas.Obj("type", "string"),
		"comparison", evalsComparisonSchema(),
		"outputs", evalsStringMapSchema(),
	), "suite_ref", "outputs")
}

func evalsABSchema() map[string]any {
	variant := evalsClosedObject(oas.Obj(
		"label", oas.Obj("type", "string"),
		"model_ref", oas.Obj("type", "string"),
		"baseline_ref", oas.Obj("type", "string"),
		"comparison", evalsComparisonSchema(),
		"outputs", evalsStringMapSchema(),
	), "outputs")
	return evalsClosedObject(oas.Obj(
		"suite_ref", oas.Obj("type", "string", "minLength", 1),
		"subject_kind", oas.Obj("type", "string"),
		"subject_ref", oas.Obj("type", "string"),
		"a", variant,
		"b", variant,
		"pairwise", oas.Obj("type", "boolean"),
	), "suite_ref", "a", "b")
}

func evalsMonitorSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"subject_kind", oas.Obj("type", "string"),
		"subject_ref", oas.Obj("type", "string"),
		"suite", oas.Obj("type", "string"),
		"limit", oas.Obj("type", "integer"),
	))
}

func evalsPinBaselineSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"suite_ref", oas.Obj("type", "string", "minLength", 1),
		"subject_ref", oas.Obj("type", "string"),
		"run_ref", oas.Obj("type", "string", "minLength", 1),
	), "suite_ref", "run_ref")
}

func evalsCalibrationItemsSchema() map[string]any {
	item := evalsClosedObject(oas.Obj(
		"case_key", oas.Obj("type", "string", "minLength", 1),
		"input", oas.Obj("type", "string"),
		"output", oas.Obj("type", "string", "minLength", 1),
		"expected", oas.Obj("type", "string"),
		"criterion", oas.Obj("type", "string"),
		"human_passed", oas.Obj("type", "boolean"),
		"human_score", oas.Obj("anyOf", []any{
			oas.Obj("type", "number", "minimum", 0, "maximum", 1),
			oas.Obj("type", "null"),
		}),
		"notes", oas.Obj("type", "string"),
	), "case_key", "output")
	return evalsClosedObject(oas.Obj(
		"set_name", oas.Obj("type", "string"),
		"items", oas.Obj(
			"type", "array",
			"minItems", 1,
			"items", item,
		),
	), "items")
}

func evalsRunCalibrationSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"set_name", oas.Obj("type", "string"),
		"judge_model", oas.Obj("type", "string"),
		"target", oas.Obj("type", "number"),
		"kappa_floor", oas.Obj("type", "number"),
	))
}

func evalsGateSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"suite_ref", oas.Obj("type", "string", "minLength", 1),
		"subject_kind", oas.Obj("type", "string"),
		"subject_ref", oas.Obj("type", "string"),
		"model_ref", oas.Obj("type", "string"),
		"prompt_variant", oas.Obj("type", "string"),
		"baseline_ref", oas.Obj("type", "string"),
		"comparison", evalsComparisonSchema(),
		"outputs", evalsStringMapSchema(),
		"seed", oas.Obj("type", "string"),
		"sample_size", oas.Obj("type", "integer"),
	), "suite_ref", "outputs")
}

func evalsOverrideGateSchema() map[string]any {
	return evalsClosedObject(oas.Obj(
		"reason", oas.Obj("type", "string", "minLength", 1),
	), "reason")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := evalsRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case evalsBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = evalsRequestBody(method, pattern)
	case evalsBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case evalsBodyNoDerivable, evalsBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

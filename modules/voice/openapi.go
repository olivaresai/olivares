// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type voiceRequestBodyKind uint8

const (
	voiceBodyless voiceRequestBodyKind = iota + 1
	voiceBodyful
	voiceBodyNoDerivable
	voiceBodyPending
)

type voiceRequestBodyDeclaration struct {
	kind   voiceRequestBodyKind
	schema map[string]any
}

func voiceRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := voiceRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != voiceBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func voiceRequestBodyDeclarationFor(method, pattern string) (voiceRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPut + " /policies":
		return voiceRequestBodyDeclaration{kind: voiceBodyful, schema: voicePolicySchema()}, true
	case http.MethodPost + " /sessions/open":
		return voiceRequestBodyDeclaration{kind: voiceBodyful, schema: voiceOpenSchema()}, true
	default:
		return voiceRequestBodyDeclaration{}, false
	}
}

func voiceNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func voiceClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func voicePolicySchema() map[string]any {
	optionalBool := func() map[string]any { return voiceNullable(oas.Obj("type", "boolean")) }
	recording := voiceClosedObject(oas.Obj(
		"active", optionalBool(),
		"dtmf_masking", optionalBool(),
		"pause_resume", optionalBool(),
	))
	patterns := func() map[string]any {
		return voiceNullable(oas.Obj(
			"type", "array",
			"items", oas.Obj("type", "string", "description", "After trimming, every supplied pattern must be non-empty."),
		))
	}
	calls := voiceClosedObject(oas.Obj(
		"enabled", optionalBool(),
		"to_patterns", patterns(),
		"from_patterns", patterns(),
		"model", voiceNullable(oas.Obj("type", "string")),
		"guardrail_instructions", voiceNullable(oas.Obj("type", "string")),
		"recording", voiceNullable(recording),
	))
	return voiceClosedObject(oas.Obj(
		"agent_ref", oas.Obj("type", "string", "minLength", 1),
		"allowed_model_ref", oas.Obj("type", "string", "minLength", 1),
		"allowed_provider_ref", oas.Obj("type", "string", "minLength", 1),
		"max_session_minutes", voiceNullable(oas.Obj("type", "integer")),
		"max_latency_ms", voiceNullable(oas.Obj("type", "integer")),
		"calls", voiceNullable(calls),
	), "agent_ref", "allowed_model_ref", "allowed_provider_ref")
}

func voiceOpenSchema() map[string]any {
	return voiceClosedObject(oas.Obj(
		"session_ref", oas.Obj("type", "string", "minLength", 1),
		"agent_ref", oas.Obj("type", "string", "minLength", 1),
		"model_ref", oas.Obj("type", "string", "minLength", 1),
		"provider_ref", oas.Obj("type", "string", "minLength", 1),
		"approval_ref", voiceNullable(oas.Obj("type", "string", "description", "Empty or omitted starts the approval phase; a value attempts the approved dispatch phase.")),
	), "session_ref", "agent_ref", "model_ref", "provider_ref")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := voiceRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case voiceBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = voiceRequestBody(method, pattern)
	case voiceBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case voiceBodyNoDerivable, voiceBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

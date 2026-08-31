// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func voiceRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := voiceRequestBodyDeclarationFor(r)
	if !ok || decl.kind != voiceBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func voiceRequestBodyDeclarationFor(r moduleRoute) (voiceRequestBodyDeclaration, bool) {
	if r.ns != "voice" {
		return voiceRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPut + " /policies":
		return voiceRequestBodyDeclaration{kind: voiceBodyful, schema: voicePolicySchema()}, true
	case http.MethodPost + " /sessions/open":
		return voiceRequestBodyDeclaration{kind: voiceBodyful, schema: voiceOpenSchema()}, true
	default:
		return voiceRequestBodyDeclaration{}, false
	}
}

func voiceNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func voiceClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func voicePolicySchema() map[string]any {
	optionalBool := func() map[string]any { return voiceNullable(oaObj("type", "boolean")) }
	recording := voiceClosedObject(oaObj(
		"active", optionalBool(),
		"dtmf_masking", optionalBool(),
		"pause_resume", optionalBool(),
	))
	patterns := func() map[string]any {
		return voiceNullable(oaObj(
			"type", "array",
			"items", oaObj("type", "string", "description", "After trimming, every supplied pattern must be non-empty."),
		))
	}
	calls := voiceClosedObject(oaObj(
		"enabled", optionalBool(),
		"to_patterns", patterns(),
		"from_patterns", patterns(),
		"model", voiceNullable(oaObj("type", "string")),
		"guardrail_instructions", voiceNullable(oaObj("type", "string")),
		"recording", voiceNullable(recording),
	))
	return voiceClosedObject(oaObj(
		"agent_ref", oaObj("type", "string", "minLength", 1),
		"allowed_model_ref", oaObj("type", "string", "minLength", 1),
		"allowed_provider_ref", oaObj("type", "string", "minLength", 1),
		"max_session_minutes", voiceNullable(oaObj("type", "integer")),
		"max_latency_ms", voiceNullable(oaObj("type", "integer")),
		"calls", voiceNullable(calls),
	), "agent_ref", "allowed_model_ref", "allowed_provider_ref")
}

func voiceOpenSchema() map[string]any {
	return voiceClosedObject(oaObj(
		"session_ref", oaObj("type", "string", "minLength", 1),
		"agent_ref", oaObj("type", "string", "minLength", 1),
		"model_ref", oaObj("type", "string", "minLength", 1),
		"provider_ref", oaObj("type", "string", "minLength", 1),
		"approval_ref", voiceNullable(oaObj("type", "string", "description", "Empty or omitted starts the approval phase; a value attempts the approved dispatch phase.")),
	), "session_ref", "agent_ref", "model_ref", "provider_ref")
}

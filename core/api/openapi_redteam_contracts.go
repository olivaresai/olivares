// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type redTeamRequestBodyKind uint8

const (
	redTeamBodyless redTeamRequestBodyKind = iota + 1
	redTeamBodyful
	redTeamBodyNoDerivable
	redTeamBodyPending
)

type redTeamRequestBodyDeclaration struct {
	kind   redTeamRequestBodyKind
	schema map[string]any
}

func redTeamRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := redTeamRequestBodyDeclarationFor(r)
	if !ok || decl.kind != redTeamBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func redTeamRequestBodyDeclarationFor(r moduleRoute) (redTeamRequestBodyDeclaration, bool) {
	if r.ns != "redteam" || r.method != http.MethodPost {
		return redTeamRequestBodyDeclaration{}, false
	}
	var schema map[string]any
	switch r.pattern {
	case "/targets":
		schema = redTeamRegisterTargetSchema()
	case "/targets/{id}/authorize":
		schema = redTeamAuthorizeTargetSchema()
	case "/runs":
		schema = redTeamLaunchRunSchema()
	default:
		return redTeamRequestBodyDeclaration{}, false
	}
	return redTeamRequestBodyDeclaration{kind: redTeamBodyful, schema: schema}, true
}

func redTeamNullable(schema map[string]any) map[string]any {
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func redTeamClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func redTeamRegisterTargetSchema() map[string]any {
	return redTeamClosedObject(oaObj(
		"agent_ref", oaObj("type", "string", "description", "After trimming, must be a non-empty agent reference owned by this tenant; stored with the server byte cap."),
		"name", redTeamNullable(oaObj("type", "string", "description", "Trimmed and byte-capped; empty defaults to agent_ref.")),
		"endpoint", redTeamNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
		"scope", redTeamNullable(oaObj("type", "string", "description", "Trimmed and byte-capped.")),
	), "agent_ref")
}

func redTeamAuthorizeTargetSchema() map[string]any {
	return redTeamClosedObject(oaObj(
		"authorized", redTeamNullable(oaObj("type", "boolean", "description", "Omitted or null decodes as false and revokes authorization.")),
		"scope", redTeamNullable(oaObj("type", "string", "description", "A non-blank trimmed value replaces the stored scope.")),
	))
}

func redTeamLaunchRunSchema() map[string]any {
	return redTeamClosedObject(oaObj(
		"target_ref", oaObj("type", "string", "description", "Must parse as a non-zero target identifier."),
		"suite", redTeamNullable(oaObj("type", "string", "description", "After trimming, empty defaults to all; otherwise must be exactly all, injection, jailbreak, exfil or tool_poisoning.")),
	), "target_ref")
}

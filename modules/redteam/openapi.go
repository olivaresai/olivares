// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func redTeamRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := redTeamRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != redTeamBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func redTeamRequestBodyDeclarationFor(method, pattern string) (redTeamRequestBodyDeclaration, bool) {
	if method != http.MethodPost {
		return redTeamRequestBodyDeclaration{}, false
	}
	var schema map[string]any
	switch pattern {
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
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func redTeamClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj("type", "object", "additionalProperties", false, "properties", properties)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func redTeamRegisterTargetSchema() map[string]any {
	return redTeamClosedObject(oas.Obj(
		"agent_ref", oas.Obj("type", "string", "description", "After trimming, must be a non-empty agent reference owned by this tenant; stored with the server byte cap."),
		"name", redTeamNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped; empty defaults to agent_ref.")),
		"endpoint", redTeamNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
		"scope", redTeamNullable(oas.Obj("type", "string", "description", "Trimmed and byte-capped.")),
	), "agent_ref")
}

func redTeamAuthorizeTargetSchema() map[string]any {
	return redTeamClosedObject(oas.Obj(
		"authorized", redTeamNullable(oas.Obj("type", "boolean", "description", "Omitted or null decodes as false and revokes authorization.")),
		"scope", redTeamNullable(oas.Obj("type", "string", "description", "A non-blank trimmed value replaces the stored scope.")),
	))
}

func redTeamLaunchRunSchema() map[string]any {
	return redTeamClosedObject(oas.Obj(
		"target_ref", oas.Obj("type", "string", "description", "Must parse as a non-zero target identifier."),
		"suite", redTeamNullable(oas.Obj("type", "string", "description", "After trimming, empty defaults to all; otherwise must be exactly all, injection, jailbreak, exfil or tool_poisoning.")),
	), "target_ref")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := redTeamRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case redTeamBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = redTeamRequestBody(method, pattern)
	case redTeamBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case redTeamBodyNoDerivable, redTeamBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

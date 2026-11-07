// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// askTurnRequestSchema mirrors askTurnRequest and the limits askCheckRequest enforces.
func askTurnRequestSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"model", oas.Obj("type", "string", "description", "Optional. A routing policy id an administrator allowed for Ask; default: the bound one."),
			"messages", oas.Obj(
				"type", "array", "minItems", 1, "maxItems", askMaxMessages,
				"description", "The conversation so far. The last entry is from the user.",
				"items", oas.Obj(
					"type", "object",
					"additionalProperties", false,
					"properties", oas.Obj(
						"role", oas.Obj("type", "string", "enum", oas.Enum("user", "assistant")),
						"content", oas.Obj("type", "string", "maxLength", askMaxContent),
					),
					"required", oas.Enum("role", "content"),
				),
			),
			"allow", oas.Obj(
				"type", "array", "maxItems", askMaxAllow,
				"description", "Optional. Limits this turn to the tools that use these permissions. Never widens: the person's own permissions still apply.",
				"items", oas.Obj("type", "string"),
			),
		),
		"required", oas.Enum("messages"),
	)
}

// askRunRequestSchema mirrors askRunRequest: the exact action the approval is bound to.
func askRunRequestSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"tool", oas.Obj("type", "string", "description", "The write tool the proposal names."),
			"args", oas.Obj("type", "object", "description", "The proposal's arguments, unchanged.",
				"additionalProperties", oas.Obj("type", "string", "maxLength", askMaxArgLen)),
		),
		"required", oas.Enum("tool"),
	)
}

// askBindingRequestSchema mirrors askBinding.
func askBindingRequestSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"model", oas.Obj("type", "string", "description", "The routing policy id Ask uses. Empty turns Ask off."),
			"models", oas.Obj("type", "array", "maxItems", askMaxModels,
				"description", "Optional. Other routing policy ids a person may pick.",
				"items", oas.Obj("type", "string")),
		),
		"required", oas.Enum("model"),
	)
}

func askJSONBody(schema map[string]any) api.ModuleOperationDocumentation {
	return api.ModuleOperationDocumentation{
		BodyKind: api.ModuleOperationJSONBody,
		RequestBody: oas.Obj(
			"required", true,
			"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
			"content", oas.Obj("application/json", oas.Obj("schema", schema)),
		),
	}
}

// askOperationDocumentation publishes the request body each Ask handler decodes.
func askOperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /ask/turns":
		return askJSONBody(askTurnRequestSchema()), true
	case http.MethodPost + " /ask/proposals/{id}/run":
		return askJSONBody(askRunRequestSchema()), true
	case http.MethodPut + " /ask/binding":
		return askJSONBody(askBindingRequestSchema()), true
	case http.MethodGet + " /ask":
		return api.ModuleOperationDocumentation{BodyKind: api.ModuleOperationBodyless}, true
	default:
		return api.ModuleOperationDocumentation{}, false
	}
}

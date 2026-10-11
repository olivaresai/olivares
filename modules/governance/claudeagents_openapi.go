// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type claudeAgentsRequestBodyKind uint8

const (
	claudeAgentsBodyless claudeAgentsRequestBodyKind = iota + 1
	claudeAgentsBodyful
	claudeAgentsBodyNoDerivable
	claudeAgentsBodyPending
)

type claudeAgentsRequestBodyDeclaration struct {
	kind   claudeAgentsRequestBodyKind
	schema map[string]any
}

func claudeAgentsRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := claudeAgentsRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != claudeAgentsBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

// claudeAgentsRequestBodyDeclarationFor classifies the only mutation registered
// by the claude-agents console. The handler unconditionally decodes one strict JSON
// document before applying the managed-agent tool decision.
func claudeAgentsRequestBodyDeclarationFor(method, pattern string) (claudeAgentsRequestBodyDeclaration, bool) {
	if method != http.MethodPost || pattern != "/sessions/{id}/tool-confirmation" {
		return claudeAgentsRequestBodyDeclaration{}, false
	}
	return claudeAgentsRequestBodyDeclaration{
		kind: claudeAgentsBodyful,
		schema: oas.Obj(
			"type", "object",
			"additionalProperties", false,
			"properties", oas.Obj(
				"tool_use_id", oas.Obj(
					"type", "string",
					"description", "After Unicode whitespace trimming, the handler requires a non-empty tool-use identifier.",
				),
				"result", oas.Obj(
					"type", "string",
					"description", "After Unicode whitespace trimming and lowercasing, the handler requires allow or deny.",
				),
				"deny_message", oas.Obj(
					"anyOf", []any{
						oas.Obj("type", "string"),
						oas.Obj("type", "null"),
					},
					"description", "Optional denial message; JSON null decodes as empty. The handler caps a string at 4096 UTF-8 bytes and rejects inline credential material.",
				),
			),
			"required", []string{"tool_use_id", "result"},
		),
	}, true
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*AgentsConsole) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := claudeAgentsRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case claudeAgentsBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = claudeAgentsRequestBody(method, pattern)
	case claudeAgentsBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case claudeAgentsBodyNoDerivable, claudeAgentsBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

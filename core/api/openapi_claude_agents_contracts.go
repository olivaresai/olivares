// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func claudeAgentsRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := claudeAgentsRequestBodyDeclarationFor(r)
	if !ok || decl.kind != claudeAgentsBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

// claudeAgentsRequestBodyDeclarationFor classifies the only mutation registered
// by the claude-agents console. The handler unconditionally decodes one strict JSON
// document before applying the managed-agent tool decision.
func claudeAgentsRequestBodyDeclarationFor(r moduleRoute) (claudeAgentsRequestBodyDeclaration, bool) {
	if r.ns != "claude-agents" {
		return claudeAgentsRequestBodyDeclaration{}, false
	}
	if r.method != http.MethodPost || r.pattern != "/sessions/{id}/tool-confirmation" {
		return claudeAgentsRequestBodyDeclaration{}, false
	}
	return claudeAgentsRequestBodyDeclaration{
		kind: claudeAgentsBodyful,
		schema: oaObj(
			"type", "object",
			"additionalProperties", false,
			"properties", oaObj(
				"tool_use_id", oaObj(
					"type", "string",
					"description", "After Unicode whitespace trimming, the handler requires a non-empty tool-use identifier.",
				),
				"result", oaObj(
					"type", "string",
					"description", "After Unicode whitespace trimming and lowercasing, the handler requires allow or deny.",
				),
				"deny_message", oaObj(
					"anyOf", []any{
						oaObj("type", "string"),
						oaObj("type", "null"),
					},
					"description", "Optional denial message; JSON null decodes as empty. The handler caps a string at 4096 UTF-8 bytes and rejects inline credential material.",
				),
			),
			"required", []string{"tool_use_id", "result"},
		),
	}, true
}

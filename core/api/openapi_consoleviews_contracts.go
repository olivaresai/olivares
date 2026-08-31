// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

type consoleViewsRequestBodyKind uint8

const (
	consoleViewsBodyless consoleViewsRequestBodyKind = iota + 1
	consoleViewsBodyful
	consoleViewsBodyNoDerivable
	consoleViewsBodyPending
)

type consoleViewsRequestBodyDeclaration struct {
	kind   consoleViewsRequestBodyKind
	schema map[string]any
}

func consoleViewsRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := consoleViewsRequestBodyDeclarationFor(r)
	if !ok || decl.kind != consoleViewsBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func consoleViewsRequestBodyDeclarationFor(r moduleRoute) (consoleViewsRequestBodyDeclaration, bool) {
	if r.ns != "consoleviews" {
		return consoleViewsRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
	case http.MethodPost + " /views", http.MethodPut + " /views/{id}":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyful, schema: consoleViewsInputSchema()}, true
	case http.MethodDelete + " /views/{id}":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyless}, true
	default:
		return consoleViewsRequestBodyDeclaration{}, false
	}
}

func consoleViewsInputSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"feature_id", oaObj(
				"type", "string",
				"pattern", "^[a-z0-9][a-z0-9-]{0,63}$",
				"description", "Trimmed before validation; must be a lowercase console feature slug.",
			),
			"name", oaObj(
				"type", "string",
				"description", "After trimming, must be non-empty and at most 120 UTF-8 bytes.",
			),
			"description", oaObj(
				"anyOf", []any{oaObj("type", "string"), oaObj("type", "null")},
				"description", "Optional; JSON null decodes as empty. A string is trimmed and limited to 500 UTF-8 bytes.",
			),
			"params", oaObj(
				"type", "object",
				"additionalProperties", true,
				"description", "Console URL state. Its original JSON encoding must be at most 4096 bytes; the handler then trims surrounding whitespace.",
			),
			"shared", oaObj("anyOf", []any{oaObj("type", "boolean"), oaObj("type", "null")}),
		),
		"required", oaEnum("feature_id", "name", "params"),
	)
}

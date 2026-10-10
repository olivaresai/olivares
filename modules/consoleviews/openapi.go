// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package consoleviews

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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

func consoleViewsRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := consoleViewsRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != consoleViewsBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func consoleViewsRequestBodyDeclarationFor(method, pattern string) (consoleViewsRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /views", http.MethodPut + " /views/{id}":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyful, schema: consoleViewsInputSchema()}, true
	case http.MethodPut + " /favorites":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyful, schema: consoleViewsFavoritesSchema()}, true
	case http.MethodPut + " /ui-state":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyful, schema: consoleViewsUIStateSchema()}, true
	case http.MethodDelete + " /views/{id}":
		return consoleViewsRequestBodyDeclaration{kind: consoleViewsBodyless}, true
	default:
		return consoleViewsRequestBodyDeclaration{}, false
	}
}

func consoleViewsInputSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"feature_id", oas.Obj(
				"type", "string",
				"pattern", "^[a-z0-9][a-z0-9-]{0,63}$",
				"description", "Trimmed before validation; must be a lowercase console feature slug.",
			),
			"name", oas.Obj(
				"type", "string",
				"description", "After trimming, must be non-empty and at most 120 UTF-8 bytes.",
			),
			"description", oas.Obj(
				"anyOf", []any{oas.Obj("type", "string"), oas.Obj("type", "null")},
				"description", "Optional; JSON null decodes as empty. A string is trimmed and limited to 500 UTF-8 bytes.",
			),
			"params", oas.Obj(
				"type", "object",
				"additionalProperties", true,
				"description", "Console URL state. Its original JSON encoding must be at most 4096 bytes; the handler then trims surrounding whitespace.",
			),
			"shared", oas.Obj("anyOf", []any{oas.Obj("type", "boolean"), oas.Obj("type", "null")}),
		),
		"required", oas.Enum("feature_id", "name", "params"),
	)
}

// consoleViewsFavoritesSchema mirrors favoritesBody in modules/consoleviews/favorites.go.
func consoleViewsFavoritesSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"properties", oas.Obj(
			"favorites", oas.Obj(
				"type", "array",
				"maxItems", 256,
				"description", "The caller's favorites in display order. The list replaces the stored one; an id may appear once.",
				"items", oas.Obj(
					"type", "object",
					"properties", oas.Obj(
						"kind", oas.Obj("type", "string", "enum", oas.Enum("feature", "utility")),
						"id", oas.Obj("type", "string", "pattern", "^[A-Za-z][A-Za-z0-9-]{0,63}$"),
					),
					"required", oas.Enum("kind", "id"),
				),
			),
		),
		"required", oas.Enum("favorites"),
	)
}

// consoleViewsUIStateSchema mirrors uiStateBody in modules/consoleviews/ui_state.go.
func consoleViewsUIStateSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"sidebar", oas.Obj(
				"type", "string",
				"enum", oas.Enum("full", "rail"),
				"description", "The console sidebar width: full (240 px, with labels) or rail (56 px, icons).",
			),
		),
		"required", oas.Enum("sidebar"),
	)
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := consoleViewsRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case consoleViewsBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = consoleViewsRequestBody(method, pattern)
	case consoleViewsBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case consoleViewsBodyNoDerivable, consoleViewsBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

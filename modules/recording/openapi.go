// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type recordingRequestBodyKind uint8

const (
	recordingBodyless recordingRequestBodyKind = iota + 1
	recordingBodyful
	recordingBodyNoDerivable
	recordingBodyPending
)

type recordingRequestBodyDeclaration struct {
	kind   recordingRequestBodyKind
	schema map[string]any
}

func recordingRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := recordingRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != recordingBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func recordingRequestBodyDeclarationFor(method, pattern string) (recordingRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPut + " /config":
		return recordingRequestBodyDeclaration{kind: recordingBodyful, schema: recordingConfigSchema()}, true
	case http.MethodPost + " /ack",
		http.MethodPost + " /sessions/{id}/seal",
		http.MethodPost + " /sessions/{id}/summarize",
		http.MethodPost + " /sweep":
		return recordingRequestBodyDeclaration{kind: recordingBodyless}, true
	default:
		return recordingRequestBodyDeclaration{}, false
	}
}

func recordingNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func recordingConfigSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"namespaces", recordingNullable(oas.Obj(
				"type", "array",
				"maxItems", 64,
				"items", oas.Obj(
					"type", "string",
					"description", "After trimming, must be a unique lowercase mounted-module namespace of at most 32 UTF-8 bytes; it starts with a letter and cannot end in '-' or '_'.",
				),
			)),
			"consent", oas.Obj("type", "string", "enum", oas.Enum("notice", "required")),
			"idle_seconds", oas.Obj("type", "integer", "minimum", 60, "maximum", 86400),
			"retention_days", oas.Obj("type", "integer", "minimum", 1, "maximum", 3650),
			"ai_summaries", recordingNullable(oas.Obj("type", "boolean")),
		),
		"required", oas.Enum("consent", "idle_seconds", "retention_days"),
	)
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := recordingRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case recordingBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = recordingRequestBody(method, pattern)
	case recordingBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case recordingBodyNoDerivable, recordingBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

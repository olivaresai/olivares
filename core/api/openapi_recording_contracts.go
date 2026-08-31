// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func recordingRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := recordingRequestBodyDeclarationFor(r)
	if !ok || decl.kind != recordingBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"description", "The handler decodes one strict JSON document, bounded at 1 MiB.",
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func recordingRequestBodyDeclarationFor(r moduleRoute) (recordingRequestBodyDeclaration, bool) {
	if r.ns != "recording" {
		return recordingRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
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
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func recordingConfigSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"namespaces", recordingNullable(oaObj(
				"type", "array",
				"maxItems", 64,
				"items", oaObj(
					"type", "string",
					"description", "After trimming, must be a unique lowercase mounted-module namespace of at most 32 UTF-8 bytes; it starts with a letter and cannot end in '-' or '_'.",
				),
			)),
			"consent", oaObj("type", "string", "enum", oaEnum("notice", "required")),
			"idle_seconds", oaObj("type", "integer", "minimum", 60, "maximum", 86400),
			"retention_days", oaObj("type", "integer", "minimum", 1, "maximum", 3650),
			"ai_summaries", recordingNullable(oaObj("type", "boolean")),
		),
		"required", oaEnum("consent", "idle_seconds", "retention_days"),
	)
}

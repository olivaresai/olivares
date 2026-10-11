// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type claudePolicyRequestBodyKind uint8

const (
	claudePolicyBodyless claudePolicyRequestBodyKind = iota + 1
	claudePolicyBodyful
	claudePolicyBodyNoDerivable
	claudePolicyBodyPending
)

type claudePolicyRequestBodyDeclaration struct {
	kind   claudePolicyRequestBodyKind
	schema map[string]any
}

func claudePolicyRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := claudePolicyRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != claudePolicyBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func claudePolicyRequestBodyDeclarationFor(method, pattern string) (claudePolicyRequestBodyDeclaration, bool) {
	if method != http.MethodPost {
		return claudePolicyRequestBodyDeclaration{}, false
	}
	var schema map[string]any
	switch pattern {
	case "/{surface}/validate":
		schema = claudePolicyContentSchema(false, false)
	case "/{surface}/dry-run":
		schema = claudePolicyContentSchema(true, false)
	case "/{surface}/publish":
		schema = claudePolicyContentSchema(true, true)
	case "/{surface}/checkin":
		schema = claudePolicyCheckinSchema()
	default:
		return claudePolicyRequestBodyDeclaration{}, false
	}
	return claudePolicyRequestBodyDeclaration{kind: claudePolicyBodyful, schema: schema}, true
}

func claudePolicyNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func claudePolicyContentSchema(requireContent, publish bool) map[string]any {
	contentDescription := "Empty content is accepted and returned as a validation diagnostic; no document is persisted."
	content := claudePolicyNullable(oas.Obj("type", "string"))
	if requireContent {
		contentDescription = "After trimming, the document must be non-empty and structurally valid for the selected surface."
		content = oas.Obj("type", "string")
	}
	noteDescription := "Accepted by the shared DTO; only publish persists it. JSON null decodes as empty."
	if publish {
		contentDescription += " Publish caps it at 262144 UTF-8 bytes and rejects inline credential material."
		noteDescription = "Optional publish note; JSON null decodes as empty and strings are capped at 4096 UTF-8 bytes."
	}
	content["description"] = contentDescription
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"content", content,
			"note", claudePolicyNullable(oas.Obj("type", "string", "description", noteDescription)),
		),
	)
	if requireContent {
		schema["required"] = oas.Enum("content")
	}
	return schema
}

func claudePolicyCheckinSchema() map[string]any {
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"scope", oas.Obj("type", "string", "description", "After trimming, must be non-empty, at most 128 UTF-8 bytes and contain no control characters."),
			"revision", claudePolicyNullable(oas.Obj("type", "integer", "minimum", 0, "description", "Zero records an unattested check-in.")),
			"artifact_sha256", claudePolicyNullable(oas.Obj("type", "string", "description", "Optional artifact hash echo; a missing or mismatched value records the check-in unverified.")),
			"key_fingerprint", claudePolicyNullable(oas.Obj("type", "string", "description", "Optional signer fingerprint echo.")),
			"observed_content", claudePolicyNullable(oas.Obj("type", "string", "description", "Optional observed document, capped at 262144 UTF-8 bytes and credential-redacted before persistence.")),
		),
		"required", oas.Enum("scope"),
	)
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*PolicyConsole) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := claudePolicyRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case claudePolicyBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = claudePolicyRequestBody(method, pattern)
	case claudePolicyBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case claudePolicyBodyNoDerivable, claudePolicyBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

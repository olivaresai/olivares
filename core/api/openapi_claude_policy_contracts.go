// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func claudePolicyRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := claudePolicyRequestBodyDeclarationFor(r)
	if !ok || decl.kind != claudePolicyBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func claudePolicyRequestBodyDeclarationFor(r moduleRoute) (claudePolicyRequestBodyDeclaration, bool) {
	if r.ns != "claude-policy" || r.method != http.MethodPost {
		return claudePolicyRequestBodyDeclaration{}, false
	}
	var schema map[string]any
	switch r.pattern {
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
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func claudePolicyContentSchema(requireContent, publish bool) map[string]any {
	contentDescription := "Empty content is accepted and returned as a validation diagnostic; no document is persisted."
	content := claudePolicyNullable(oaObj("type", "string"))
	if requireContent {
		contentDescription = "After trimming, the document must be non-empty and structurally valid for the selected surface."
		content = oaObj("type", "string")
	}
	noteDescription := "Accepted by the shared DTO; only publish persists it. JSON null decodes as empty."
	if publish {
		contentDescription += " Publish caps it at 262144 UTF-8 bytes and rejects inline credential material."
		noteDescription = "Optional publish note; JSON null decodes as empty and strings are capped at 4096 UTF-8 bytes."
	}
	content["description"] = contentDescription
	schema := oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"content", content,
			"note", claudePolicyNullable(oaObj("type", "string", "description", noteDescription)),
		),
	)
	if requireContent {
		schema["required"] = oaEnum("content")
	}
	return schema
}

func claudePolicyCheckinSchema() map[string]any {
	return oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"scope", oaObj("type", "string", "description", "After trimming, must be non-empty, at most 128 UTF-8 bytes and contain no control characters."),
			"revision", claudePolicyNullable(oaObj("type", "integer", "minimum", 0, "description", "Zero records an unattested check-in.")),
			"artifact_sha256", claudePolicyNullable(oaObj("type", "string", "description", "Optional artifact hash echo; a missing or mismatched value records the check-in unverified.")),
			"key_fingerprint", claudePolicyNullable(oaObj("type", "string", "description", "Optional signer fingerprint echo.")),
			"observed_content", claudePolicyNullable(oaObj("type", "string", "description", "Optional observed document, capped at 262144 UTF-8 bytes and credential-redacted before persistence.")),
		),
		"required", oaEnum("scope"),
	)
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

type capabilitiesRequestBodyKind uint8

const (
	capabilitiesBodyless capabilitiesRequestBodyKind = iota + 1
	capabilitiesBodyful
	capabilitiesBodyNoDerivable
	capabilitiesBodyPending
)

type capabilitiesRequestBodyDeclaration struct {
	kind   capabilitiesRequestBodyKind
	schema map[string]any
}

func capabilitiesRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := capabilitiesRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != capabilitiesBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj("application/json", oas.Obj("schema", decl.schema)),
	), true
}

func capabilitiesRequestBodyDeclarationFor(method, pattern string) (capabilitiesRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /configs":
		return capabilitiesBodyDeclaration(capabilitiesConfigSchema(true)), true
	case http.MethodPut + " /configs/{id}":
		return capabilitiesBodyDeclaration(capabilitiesConfigSchema(false)), true
	case http.MethodDelete + " /configs/{id}":
		return capabilitiesRequestBodyDeclaration{kind: capabilitiesBodyless}, true
	case http.MethodPost + " /toolpins/approve":
		return capabilitiesBodyDeclaration(capabilitiesToolPinSchema(true)), true
	case http.MethodPost + " /toolpins/unpin":
		return capabilitiesBodyDeclaration(capabilitiesToolPinSchema(false)), true
	default:
		return capabilitiesRequestBodyDeclaration{}, false
	}
}

func capabilitiesBodyDeclaration(schema map[string]any) capabilitiesRequestBodyDeclaration {
	return capabilitiesRequestBodyDeclaration{kind: capabilitiesBodyful, schema: schema}
}

func capabilitiesNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func capabilitiesConfigSchema(create bool) map[string]any {
	secretRef := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"name", oas.Obj("type", "string", "description", "After trimming, must be non-empty."),
			"ref_kind", oas.Obj("type", "string", "description", "After trimming and lowercasing, must be env, vault, secret_manager, file or other."),
			"ref", oas.Obj("type", "string", "description", "After trimming, must be a non-empty locator and not inline credential material."),
			"hint", capabilitiesNullable(oas.Obj("type", "string", "description", "At most 64 UTF-8 bytes.")),
		),
		"required", oas.Enum("name", "ref_kind", "ref"),
	)

	required := oas.Enum("transport")
	if create {
		required = oas.Enum("server_ref", "transport")
	}
	return oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", oas.Obj(
			"id", capabilitiesNullable(oas.Obj("type", "string", "description", "Accepted by the DTO but ignored on writes.")),
			"server_ref", capabilitiesNullable(oas.Obj("type", "string", "description", "Required on create after trimming; immutable and replaced from storage on update.")),
			"transport", oas.Obj("type", "string", "description", "After trimming and lowercasing, must be stdio, http, sse or ws."),
			"endpoint", capabilitiesNullable(oas.Obj("type", "string", "description", "Trimmed and rejected when it contains inline credential material.")),
			"scope", capabilitiesNullable(oas.Obj("type", "string")),
			"secret_refs", capabilitiesNullable(oas.Obj("type", "array", "maxItems", 64, "items", secretRef)),
			"enabled", capabilitiesNullable(oas.Obj("type", "boolean")),
			"note", capabilitiesNullable(oas.Obj("type", "string")),
			"revision", capabilitiesNullable(oas.Obj("type", "integer", "description", "Accepted by the DTO but assigned by the server on writes.")),
		),
		"required", required,
	)
}

// The tool-pin handlers use json.Decoder directly without DisallowUnknownFields,
// so this schema intentionally remains open while documenting every typed field.
func capabilitiesToolPinSchema(approve bool) map[string]any {
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", true,
		"properties", oas.Obj(
			"tool", oas.Obj("type", "string", "minLength", 1),
			"fingerprint", capabilitiesNullable(oas.Obj("type", "string")),
			"from_drift", capabilitiesNullable(oas.Obj("type", "boolean")),
			"expected_version", oas.Obj("type", "integer"),
			"expected_drift_fingerprint", capabilitiesNullable(oas.Obj("type", "string")),
		),
		"required", oas.Enum("tool", "expected_version"),
	)
	if approve {
		schema["anyOf"] = []any{
			oas.Obj(
				"required", oas.Enum("fingerprint"),
				"properties", oas.Obj("fingerprint", oas.Obj("type", "string", "minLength", 1)),
			),
			oas.Obj(
				"required", oas.Enum("from_drift", "expected_drift_fingerprint"),
				"properties", oas.Obj(
					"from_drift", oas.Obj("const", true),
					"expected_drift_fingerprint", oas.Obj("type", "string", "minLength", 1),
				),
			),
		}
	}
	return schema
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := capabilitiesRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case capabilitiesBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = capabilitiesRequestBody(method, pattern)
	case capabilitiesBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case capabilitiesBodyNoDerivable, capabilitiesBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

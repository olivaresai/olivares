// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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

func capabilitiesRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := capabilitiesRequestBodyDeclarationFor(r)
	if !ok || decl.kind != capabilitiesBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj("application/json", oaObj("schema", decl.schema)),
	), true
}

func capabilitiesRequestBodyDeclarationFor(r moduleRoute) (capabilitiesRequestBodyDeclaration, bool) {
	if r.ns != "capabilities" {
		return capabilitiesRequestBodyDeclaration{}, false
	}
	switch r.method + " " + r.pattern {
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
	return oaObj("anyOf", []any{schema, oaObj("type", "null")})
}

func capabilitiesConfigSchema(create bool) map[string]any {
	secretRef := oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"name", oaObj("type", "string", "description", "After trimming, must be non-empty."),
			"ref_kind", oaObj("type", "string", "description", "After trimming and lowercasing, must be env, vault, secret_manager, file or other."),
			"ref", oaObj("type", "string", "description", "After trimming, must be a non-empty locator and not inline credential material."),
			"hint", capabilitiesNullable(oaObj("type", "string", "description", "At most 64 UTF-8 bytes.")),
		),
		"required", oaEnum("name", "ref_kind", "ref"),
	)

	required := oaEnum("transport")
	if create {
		required = oaEnum("server_ref", "transport")
	}
	return oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", oaObj(
			"id", capabilitiesNullable(oaObj("type", "string", "description", "Accepted by the DTO but ignored on writes.")),
			"server_ref", capabilitiesNullable(oaObj("type", "string", "description", "Required on create after trimming; immutable and replaced from storage on update.")),
			"transport", oaObj("type", "string", "description", "After trimming and lowercasing, must be stdio, http, sse or ws."),
			"endpoint", capabilitiesNullable(oaObj("type", "string", "description", "Trimmed and rejected when it contains inline credential material.")),
			"scope", capabilitiesNullable(oaObj("type", "string")),
			"secret_refs", capabilitiesNullable(oaObj("type", "array", "maxItems", 64, "items", secretRef)),
			"enabled", capabilitiesNullable(oaObj("type", "boolean")),
			"note", capabilitiesNullable(oaObj("type", "string")),
			"revision", capabilitiesNullable(oaObj("type", "integer", "description", "Accepted by the DTO but assigned by the server on writes.")),
		),
		"required", required,
	)
}

// The tool-pin handlers use json.Decoder directly without DisallowUnknownFields,
// so this schema intentionally remains open while documenting every typed field.
func capabilitiesToolPinSchema(approve bool) map[string]any {
	schema := oaObj(
		"type", "object",
		"additionalProperties", true,
		"properties", oaObj(
			"tool", oaObj("type", "string", "minLength", 1),
			"fingerprint", capabilitiesNullable(oaObj("type", "string")),
			"from_drift", capabilitiesNullable(oaObj("type", "boolean")),
			"expected_version", oaObj("type", "integer"),
			"expected_drift_fingerprint", capabilitiesNullable(oaObj("type", "string")),
		),
		"required", oaEnum("tool", "expected_version"),
	)
	if approve {
		schema["anyOf"] = []any{
			oaObj(
				"required", oaEnum("fingerprint"),
				"properties", oaObj("fingerprint", oaObj("type", "string", "minLength", 1)),
			),
			oaObj(
				"required", oaEnum("from_drift", "expected_drift_fingerprint"),
				"properties", oaObj(
					"from_drift", oaObj("const", true),
					"expected_drift_fingerprint", oaObj("type", "string", "minLength", 1),
				),
			),
		}
	}
	return schema
}

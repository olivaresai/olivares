// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// catalogRequestBodyKind records the handler-backed disposition of every Catalog
// mutation. Pending and no-derivable remain explicit states even though the current
// census has none, so a future undecided route cannot masquerade as bodyless.
type catalogRequestBodyKind uint8

const (
	catalogBodyless catalogRequestBodyKind = iota + 1
	catalogBodyful
	catalogBodyNoDerivable
	catalogBodyPending
)

type catalogRequestBodyDeclaration struct {
	kind   catalogRequestBodyKind
	schema func() map[string]any
}

// catalogRequestBody returns a requestBody only for mutations whose registered
// handler decodes JSON. It is intentionally not wired into moduleRequestBody yet.
func catalogRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := catalogRequestBodyDeclarationFor(r)
	if !ok || decl.kind != catalogBodyful || decl.schema == nil {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj(
			"application/json", oaObj("schema", decl.schema()),
		),
	), true
}

// catalogRequestBodyDeclarationFor classifies all mutations registered by Catalog's
// APIRoutes. The admission route dispatches by entry kind, but both target handlers
// decode the same request fields and types, so it has one proven common schema.
func catalogRequestBodyDeclarationFor(r moduleRoute) (catalogRequestBodyDeclaration, bool) {
	if r.ns != "catalog" {
		return catalogRequestBodyDeclaration{}, false
	}

	switch r.method + " " + r.pattern {
	case http.MethodPost + " /entries",
		http.MethodPut + " /entries/{id}":
		return catalogBodyDeclaration(catalogEntryRequestSchema), true
	case http.MethodPut + " /mcp-admission/policy",
		http.MethodPut + " /connector-admission/policy":
		return catalogBodyDeclaration(catalogAdmissionPolicySchema), true
	case http.MethodPost + " /entries/{id}/admit":
		return catalogBodyDeclaration(catalogAdmitEntrySchema), true
	case http.MethodPost + " /entries/{id}/instantiate":
		return catalogBodyDeclaration(catalogInstantiateSchema), true
	case http.MethodPost + " /instances/{id}/transition":
		return catalogBodyDeclaration(catalogTransitionSchema), true

	case http.MethodDelete + " /entries/{id}",
		http.MethodPost + " /entries/{id}/submit",
		http.MethodPost + " /entries/{id}/approve",
		http.MethodPost + " /entries/{id}/deprecate":
		return catalogRequestBodyDeclaration{kind: catalogBodyless}, true
	default:
		return catalogRequestBodyDeclaration{}, false
	}
}

func catalogBodyDeclaration(schema func() map[string]any) catalogRequestBodyDeclaration {
	return catalogRequestBodyDeclaration{kind: catalogBodyful, schema: schema}
}

func catalogClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oaObj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

// catalogEntryRequestSchema mirrors entryDTO, including the response-oriented fields
// the decoder accepts before the handler overwrites or ignores them. The spec member is
// deliberately open: its contents are kind-specific and the handler decodes it as
// map[string]any, while separately rejecting obvious inline-credential patterns.
func catalogEntryRequestSchema() map[string]any {
	return catalogClosedObject(oaObj(
		"id", oaObj("type", "string"),
		"kind", oaObj(
			"type", "string",
			"enum", oaEnum("agent", "mcp", "skill", "template", "model", "connector"),
		),
		"name", oaObj("type", "string", "pattern", `\S`),
		"slug", oaObj("type", "string", "pattern", `^[a-z0-9_-]{1,64}$`),
		"version", oaObj(
			"type", "string",
			"maxLength", 64,
			"pattern", `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`,
		),
		"status", oaObj("type", "string"),
		"summary", oaObj("type", "string"),
		"spec", oaObj("anyOf", []any{
			oaObj("type", "object", "additionalProperties", true),
			oaObj("type", "null"),
		}),
		"owner_ref", oaObj("type", "string"),
		"content_hash", oaObj("type", "string"),
		"signed", oaObj("type", "boolean"),
		"sig_alg", oaObj("type", "string"),
		"signed_by", oaObj("type", "string"),
		"approved_by", oaObj("type", "string"),
		"approved_at", oaObj("type", "string"),
	), "kind", "name", "slug", "version")
}

func catalogAdmissionPolicySchema() map[string]any {
	schema := catalogClosedObject(oaObj(
		"require_signed", oaObj("type", "boolean"),
		"require_subject_digest", oaObj("type", "boolean"),
		"allowed_identities", catalogStringArray(),
		"allowed_issuers", catalogStringArray(),
		"trusted_keys", catalogPublicMaterialArray(),
		"trusted_roots", catalogPublicMaterialArray(),
		"allowed_predicates", oaObj(
			"type", "array",
			"items", oaObj("type", "string", "pattern", `\S`),
		),
		"note", oaObj("type", "string"),
		"attested_by", oaObj("type", "string"),
		"attested_at", oaObj("type", "string"),
	))
	schema["allOf"] = []any{
		oaObj(
			"if", oaObj(
				"required", []string{"require_signed"},
				"properties", oaObj("require_signed", oaObj("const", true)),
			),
			"then", oaObj("anyOf", []any{
				oaObj(
					"required", []string{"trusted_keys"},
					"properties", oaObj("trusted_keys", oaObj("minItems", 1)),
				),
				oaObj(
					"required", []string{"trusted_roots"},
					"properties", oaObj("trusted_roots", oaObj("minItems", 1)),
				),
			}),
		),
		catalogPairedNonEmptyArrays("allowed_identities", "allowed_issuers"),
		catalogPairedNonEmptyArrays("allowed_issuers", "allowed_identities"),
	}
	return schema
}

func catalogPublicMaterialArray() map[string]any {
	return oaObj(
		"type", "array",
		"items", oaObj(
			"type", "string",
			"not", oaObj("pattern", "PRIVATE KEY"),
		),
	)
}

func catalogPairedNonEmptyArrays(trigger, required string) map[string]any {
	return oaObj(
		"if", oaObj(
			"required", []string{trigger},
			"properties", oaObj(trigger, oaObj("minItems", 1)),
		),
		"then", oaObj(
			"required", []string{required},
			"properties", oaObj(required, oaObj("minItems", 1)),
		),
	)
}

func catalogStringArray() map[string]any {
	return oaObj("type", "array", "items", oaObj("type", "string"))
}

func catalogAdmitEntrySchema() map[string]any {
	return catalogClosedObject(oaObj(
		// Both dispatch targets decode bundle as json.RawMessage. Omitting a type here
		// preserves that exact wire shape instead of inventing one Sigstore envelope.
		"bundle", oaObj(
			"description", "Sigstore attestation bundle captured as raw JSON and validated by the verifier.",
		),
		"predicate_types", catalogStringArray(),
		"expected_digest", oaObj("type", "string"),
		"note", oaObj("type", "string"),
	), "bundle")
}

func catalogInstantiateSchema() map[string]any {
	return catalogClosedObject(oaObj(
		"name", oaObj("type", "string", "pattern", `\S`),
		"target_ref", oaObj("type", "string"),
		"note", oaObj("type", "string"),
	), "name")
}

func catalogTransitionSchema() map[string]any {
	return catalogClosedObject(oaObj(
		"status", oaObj(
			"type", "string",
			"enum", oaEnum("approved", "rejected", "active"),
		),
		"note", oaObj("type", "string"),
	), "status")
}

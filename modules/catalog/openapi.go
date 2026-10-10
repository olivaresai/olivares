// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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
// handler decodes JSON; OperationDocumentation publishes it.
func catalogRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := catalogRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != catalogBodyful || decl.schema == nil {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj(
			"application/json", oas.Obj("schema", decl.schema()),
		),
	), true
}

// catalogRequestBodyDeclarationFor classifies all mutations registered by Catalog's
// APIRoutes. The admission route dispatches by entry kind, but both target handlers
// decode the same request fields and types, so it has one proven common schema.
func catalogRequestBodyDeclarationFor(method, pattern string) (catalogRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
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
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

// catalogEntryRequestSchema mirrors entryDTO, including the response-oriented fields
// the decoder accepts before the handler overwrites or ignores them. The spec member is
// deliberately open: its contents are kind-specific and the handler decodes it as
// map[string]any, while separately rejecting obvious inline-credential patterns.
func catalogEntryRequestSchema() map[string]any {
	return catalogClosedObject(oas.Obj(
		"id", oas.Obj("type", "string"),
		"kind", oas.Obj(
			"type", "string",
			"enum", oas.Enum("agent", "mcp", "skill", "template", "model", "connector"),
		),
		"name", oas.Obj("type", "string", "pattern", `\S`),
		"slug", oas.Obj("type", "string", "pattern", `^[a-z0-9_-]{1,64}$`),
		"version", oas.Obj(
			"type", "string",
			"maxLength", 64,
			"pattern", `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`,
		),
		"status", oas.Obj("type", "string"),
		"summary", oas.Obj("type", "string"),
		"spec", oas.Obj("anyOf", []any{
			oas.Obj("type", "object", "additionalProperties", true),
			oas.Obj("type", "null"),
		}),
		"owner_ref", oas.Obj("type", "string"),
		"content_hash", oas.Obj("type", "string"),
		"signed", oas.Obj("type", "boolean"),
		"sig_alg", oas.Obj("type", "string"),
		"signed_by", oas.Obj("type", "string"),
		"approved_by", oas.Obj("type", "string"),
		"approved_at", oas.Obj("type", "string"),
	), "kind", "name", "slug", "version")
}

func catalogAdmissionPolicySchema() map[string]any {
	schema := catalogClosedObject(oas.Obj(
		"require_signed", oas.Obj("type", "boolean"),
		"require_subject_digest", oas.Obj("type", "boolean"),
		"allowed_identities", catalogStringArray(),
		"allowed_issuers", catalogStringArray(),
		"trusted_keys", catalogPublicMaterialArray(),
		"trusted_roots", catalogPublicMaterialArray(),
		"allowed_predicates", oas.Obj(
			"type", "array",
			"items", oas.Obj("type", "string", "pattern", `\S`),
		),
		"note", oas.Obj("type", "string"),
		"attested_by", oas.Obj("type", "string"),
		"attested_at", oas.Obj("type", "string"),
	))
	schema["allOf"] = []any{
		oas.Obj(
			"if", oas.Obj(
				"required", []string{"require_signed"},
				"properties", oas.Obj("require_signed", oas.Obj("const", true)),
			),
			"then", oas.Obj("anyOf", []any{
				oas.Obj(
					"required", []string{"trusted_keys"},
					"properties", oas.Obj("trusted_keys", oas.Obj("minItems", 1)),
				),
				oas.Obj(
					"required", []string{"trusted_roots"},
					"properties", oas.Obj("trusted_roots", oas.Obj("minItems", 1)),
				),
			}),
		),
		catalogPairedNonEmptyArrays("allowed_identities", "allowed_issuers"),
		catalogPairedNonEmptyArrays("allowed_issuers", "allowed_identities"),
	}
	return schema
}

func catalogPublicMaterialArray() map[string]any {
	return oas.Obj(
		"type", "array",
		"items", oas.Obj(
			"type", "string",
			"not", oas.Obj("pattern", "PRIVATE KEY"),
		),
	)
}

func catalogPairedNonEmptyArrays(trigger, required string) map[string]any {
	return oas.Obj(
		"if", oas.Obj(
			"required", []string{trigger},
			"properties", oas.Obj(trigger, oas.Obj("minItems", 1)),
		),
		"then", oas.Obj(
			"required", []string{required},
			"properties", oas.Obj(required, oas.Obj("minItems", 1)),
		),
	)
}

func catalogStringArray() map[string]any {
	return oas.Obj("type", "array", "items", oas.Obj("type", "string"))
}

func catalogAdmitEntrySchema() map[string]any {
	return catalogClosedObject(oas.Obj(
		// Both dispatch targets decode bundle as json.RawMessage. Omitting a type here
		// preserves that exact wire shape instead of inventing one Sigstore envelope.
		"bundle", oas.Obj(
			"description", "Sigstore attestation bundle captured as raw JSON and validated by the verifier.",
		),
		"predicate_types", catalogStringArray(),
		"expected_digest", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "bundle")
}

func catalogInstantiateSchema() map[string]any {
	return catalogClosedObject(oas.Obj(
		"name", oas.Obj("type", "string", "pattern", `\S`),
		"target_ref", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "name")
}

func catalogTransitionSchema() map[string]any {
	return catalogClosedObject(oas.Obj(
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("approved", "rejected", "active"),
		),
		"note", oas.Obj("type", "string"),
		"approval_ref", oas.Obj("type", "string", "description", "The deploy approval returned by an activation proposal. An approved instance remains approved until deployment completes."),
	), "status")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := catalogRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case catalogBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = catalogRequestBody(method, pattern)
	case catalogBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case catalogBodyNoDerivable, catalogBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

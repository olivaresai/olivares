// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// knowledgeRequestBodyKind records the handler-derived body behavior of every
// mutating Knowledge route. An opaque body has a known media type and truthful
// root representation, but no property-level schema is published for its stream.
type knowledgeRequestBodyKind uint8

const (
	knowledgeBodyful knowledgeRequestBodyKind = iota + 1
	knowledgeBodyless
	knowledgeBodyOpaque
	knowledgeBodyPending
)

type knowledgeRequestBodyDeclaration struct {
	kind      knowledgeRequestBodyKind
	mediaType string
	schema    func() map[string]any
}

// knowledgeRequestBody returns a fresh OpenAPI 3.1 requestBody for every
// Knowledge operation that reads one. JSON DTO roots are closed; the NDJSON
// import is represented as a string stream without invented line properties.
func knowledgeRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := knowledgeRequestBodyDeclarationFor(method, pattern)
	if !ok || (decl.kind != knowledgeBodyful && decl.kind != knowledgeBodyOpaque) {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj(
			decl.mediaType, oas.Obj("schema", decl.schema()),
		),
	), true
}

// knowledgeRequestBodyDeclarationFor classifies all 28 non-GET routes
// registered by modules/knowledge. Bodyless POST commands are named explicitly
// alongside DELETE routes so the census catches a future decoder added to one.
func knowledgeRequestBodyDeclarationFor(method, pattern string) (knowledgeRequestBodyDeclaration, bool) {

	var schema func() map[string]any
	switch method + " " + pattern {
	case http.MethodPost + " /kbs":
		schema = func() map[string]any { return knowledgeKBSchema(true) }
	case http.MethodPut + " /kbs/{id}":
		schema = func() map[string]any { return knowledgeKBSchema(false) }
	case http.MethodPost + " /kbs/{id}/ingest":
		schema = knowledgeIngestSchema
	case http.MethodPost + " /kbs/{id}/sync":
		schema = knowledgeSyncSchema
	case http.MethodPost + " /kbs/{id}/query":
		schema = knowledgeQuerySchema
	case http.MethodPost + " /prompts":
		schema = knowledgePromptCreateSchema
	case http.MethodPost + " /prompts/{id}/revisions":
		schema = knowledgePromptRevisionSchema
	case http.MethodPost + " /prompts/{id}/rollback":
		schema = knowledgePromptRollbackSchema
	case http.MethodPost + " /memory":
		schema = knowledgeMemorySchema
	case http.MethodPost + " /context-policies":
		schema = knowledgeContextPolicySchema
	case http.MethodPut + " /dlp/rules":
		schema = knowledgeDLPRuleSchema
	case http.MethodPost + " /data-products":
		schema = func() map[string]any { return knowledgeDataProductSchema(true) }
	case http.MethodPut + " /data-products/{id}":
		schema = func() map[string]any { return knowledgeDataProductSchema(false) }
	case http.MethodPost + " /data-products/{id}/validate":
		schema = knowledgeDataProductValidateSchema
	case http.MethodPost + " /data-products/{id}/contracts":
		schema = knowledgeDataContractSchema

	case http.MethodDelete + " /kbs/{id}",
		http.MethodPost + " /kbs/{id}/reindex",
		http.MethodPost + " /memory/verify",
		http.MethodDelete + " /memory/{id}",
		http.MethodPost + " /memory/purge",
		http.MethodPost + " /kbs/{id}/scan",
		http.MethodPost + " /sources/{name}/scan",
		http.MethodDelete + " /dlp/rules/{id}",
		http.MethodDelete + " /data-products/{id}",
		http.MethodPost + " /data-products/{id}/publish",
		http.MethodPost + " /data-products/{id}/deprecate",
		http.MethodPost + " /data-products/{id}/archive":
		return knowledgeRequestBodyDeclaration{kind: knowledgeBodyless}, true

	case http.MethodPost + " /memory/import":
		return knowledgeRequestBodyDeclaration{
			kind:      knowledgeBodyOpaque,
			mediaType: "application/x-ndjson",
			schema: func() map[string]any {
				return oas.Obj(
					"type", "string",
					"description", "NDJSON memory-portability bundle: the first line is the signed manifest; each remaining line is one memory row.",
				)
			},
		}, true
	default:
		return knowledgeRequestBodyDeclaration{}, false
	}
	return knowledgeRequestBodyDeclaration{
		kind: knowledgeBodyful, mediaType: "application/json", schema: schema,
	}, true
}

func knowledgeClosedObject(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func knowledgeNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func knowledgeStringArray() map[string]any {
	return oas.Obj("type", "array", "items", oas.Obj("type", "string"))
}

func knowledgeNonBlankString(maxLength int) map[string]any {
	schema := oas.Obj("type", "string", "minLength", 1, "pattern", `\S`)
	if maxLength > 0 {
		schema["maxLength"] = maxLength
	}
	return schema
}

func knowledgeKBSchema(create bool) map[string]any {
	name := oas.Obj(
		"type", "string",
		"maxLength", 200,
		"description", "Names containing a recognized secret are rejected by the handler.",
	)
	required := []string(nil)
	if create {
		name["minLength"] = 1
		name["pattern"] = `\S`
		required = []string{"name"}
	} else {
		name["description"] = "Empty or omitted preserves the stored name; recognized secrets are rejected."
	}
	defaultACL := knowledgeStringArray()
	defaultACL["maxItems"] = 256
	defaultACL["items"] = oas.Obj(
		"type", "string",
		"maxLength", 1024,
		"description", "Permission references containing a recognized secret are rejected.",
	)
	return knowledgeClosedObject(oas.Obj(
		"name", name,
		"classification", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "public", "internal", "confidential", "secret", "restricted"),
			"default", "internal",
			"description", "Empty or omitted defaults to internal; restricted is the accepted top-rank alias.",
		),
		"residency_region", oas.Obj("type", "string", "maxLength", 200, "default", "global"),
		"embed_policy", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "local_only", "model_backed", "auto"),
			"default", "auto",
			"description", "Acceptance also depends on the wired embedder's egress, model and region.",
		),
		"default_acl", defaultACL,
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "active", "archived"),
			"default", "active",
		),
	), required...)
}

func knowledgeIngestSchema() map[string]any {
	document := knowledgeClosedObject(oas.Obj(
		"source_kind", oas.Obj("type", "string", "default", "inline"),
		"source_mode", oas.Obj(
			"type", "string",
			"description", "Unknown or empty values are normalized to direct for inline documents.",
		),
		"source_doc_id", knowledgeNonBlankString(0),
		"title", oas.Obj("type", "string"),
		"body", oas.Obj("type", "string", "maxLength", 1<<20),
		"content_type", oas.Obj("type", "string"),
		"acl", knowledgeStringArray(),
		"classification", oas.Obj(
			"type", "string",
			"description", "Accepted as supplied; empty inherits the knowledge-base classification.",
		),
		"space_ref", oas.Obj("type", "string"),
	), "source_doc_id")
	documents := oas.Obj(
		"type", "array",
		"maxItems", 200,
		"items", document,
	)
	schema := knowledgeClosedObject(oas.Obj(
		"source", oas.Obj("type", "string"),
		"documents", documents,
	))
	// A non-blank source takes precedence when both selectors are supplied. If it
	// is blank, at least one inline document is required.
	schema["anyOf"] = []any{
		oas.Obj(
			"required", []string{"source"},
			"properties", oas.Obj("source", knowledgeNonBlankString(0)),
		),
		oas.Obj(
			"required", []string{"documents"},
			"properties", oas.Obj("documents", oas.Obj("minItems", 1)),
		),
	}
	return schema
}

func knowledgeSyncSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"source", knowledgeNonBlankString(0),
	), "source")
}

func knowledgeQuerySchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"query", knowledgeNonBlankString(8192),
		"top_k", oas.Obj(
			"type", "integer",
			"description", "Values at or below zero default to 10; values above 100 are clamped to 100.",
		),
		"agent_ref", oas.Obj(
			"type", "string",
			"description", "Accepted for compatibility but ignored for authorization.",
		),
		"session_ref", oas.Obj("type", "string"),
	), "query")
}

func knowledgePromptCreateSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"name", knowledgeNonBlankString(200),
		"template", knowledgeNonBlankString(1<<16),
		"label", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "name", "template")
}

func knowledgePromptRevisionSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"template", knowledgeNonBlankString(1<<16),
		"label", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "template")
}

func knowledgePromptRollbackSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"rev", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
	), "rev")
}

func knowledgeMemorySchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"agent_ref", knowledgeNonBlankString(1024),
		"key", knowledgeNonBlankString(1024),
		"content", oas.Obj("type", "string", "maxLength", 1<<16),
		"classification", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "public", "internal", "confidential", "secret", "restricted"),
			"default", "public",
		),
		"residency_region", oas.Obj(
			"type", "string",
			"default", "global",
			"description", "Empty or omitted defaults to global.",
		),
		"ttl_seconds", oas.Obj(
			"type", "integer",
			"format", "int64",
			"description", "Positive values set expiry; zero or negative values mean no expiry.",
		),
		"user_ref", knowledgeNullable(oas.Obj(
			"type", "string", "minLength", 1, "maxLength", 200, "pattern", `\S`,
		)),
		"session_ref", knowledgeNullable(oas.Obj(
			"type", "string", "minLength", 1, "maxLength", 200, "pattern", `\S`,
		)),
	), "agent_ref", "key")
}

func knowledgeContextPolicySchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"scope_kind", oas.Obj(
			"type", "string",
			"enum", oas.Enum(
				"session", "agent", "user", "user_group", "role",
				"agent_group", "kb", "workspace", "tenant",
			),
		),
		"scope_ref", knowledgeNonBlankString(1024),
		"max_tokens", oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 10_000_000),
		"strategy", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "truncate", "summarize", "window"),
			"default", "truncate",
		),
		"redaction_required", oas.Obj("type", "boolean"),
		"spec", knowledgeNullable(oas.Obj(
			"type", "object",
			"additionalProperties", true,
			"description", "Policy-specific JSON object; nested fields are not decoded into a typed DTO.",
		)),
		"effect", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "allow", "forbid"),
			"default", "allow",
		),
	), "scope_kind", "scope_ref")
}

func knowledgeDLPRuleSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"class", oas.Obj("type", "string", "minLength", 1, "maxLength", 200, "pattern", `\S`),
		"action", oas.Obj("type", "string", "enum", oas.Enum("allow", "deny")),
		"note", oas.Obj("type", "string", "maxLength", 200),
	), "class", "action")
}

func knowledgeDataProductSchema(create bool) map[string]any {
	name := knowledgeNullable(knowledgeNonBlankString(200))
	owner := knowledgeNullable(oas.Obj(
		"type", "string",
		"maxLength", 1024,
		"description", "References containing a recognized secret are rejected.",
	))
	required := []string(nil)
	if create {
		name = knowledgeNonBlankString(200)
		name["description"] = "Names containing a recognized secret are rejected."
		required = []string{"name"}
	} else {
		owner = knowledgeNullable(knowledgeNonBlankString(1024))
		owner["description"] = "When non-null, owner_ref must be non-blank and contain no recognized secret."
	}
	productRef := knowledgeNullable(oas.Obj(
		"type", "string",
		"description", "Empty clears the binding; non-empty values must parse as a non-zero model id.",
	))
	return knowledgeClosedObject(oas.Obj(
		"name", name,
		"description", knowledgeNullable(oas.Obj(
			"type", "string",
			"maxLength", 1<<16,
			"description", "Descriptions containing a recognized secret are rejected.",
		)),
		"owner_ref", owner,
		"kb_ref", productRef,
		"kb_id", knowledgeNullable(oas.Obj(
			"type", "string",
			"description", "Alias of kb_ref; when both are non-null their trimmed values must match.",
		)),
		"tags", knowledgeNullable(oas.Obj(
			"type", "object",
			"additionalProperties", true,
		)),
		"freshness_sla_seconds", knowledgeNullable(oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0,
		)),
		"availability_target", knowledgeNullable(oas.Obj(
			"type", "string",
			"maxLength", 200,
			"description", "Values containing a recognized secret are rejected.",
		)),
		"enforcement_mode", knowledgeNullable(oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "enforce", "warn", "observe"),
			"default", "enforce",
		)),
		"quality_score", knowledgeNullable(oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0, "maximum", 100,
		)),
	), required...)
}

func knowledgeDataProductValidateSchema() map[string]any {
	metadata := knowledgeNullable(oas.Obj(
		"type", "object",
		"additionalProperties", true,
	))
	schema := knowledgeClosedObject(oas.Obj(
		"payload", oas.Obj("description", "Any JSON value to validate against the active data contract."),
		"metadata", metadata,
	))
	// An explicitly present payload counts even when its JSON value is null. A
	// metadata-only request must carry a non-null object.
	schema["anyOf"] = []any{
		oas.Obj("required", []string{"payload"}),
		oas.Obj(
			"required", []string{"metadata"},
			"properties", oas.Obj("metadata", oas.Obj("type", "object")),
		),
	}
	return schema
}

func knowledgeDataContractSchema() map[string]any {
	return knowledgeClosedObject(oas.Obj(
		"schema_definition", knowledgeNullable(oas.Obj(
			"type", "object",
			"additionalProperties", true,
			"description", "A JSON Schema object. The encoded value is limited to 64 KiB by the handler.",
		)),
		"validation_mode", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "strict", "lenient", "none"),
			"description", "Empty defaults to strict when a non-empty schema exists, otherwise none.",
		),
		"completeness_threshold", oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0, "maximum", 100,
		),
		"freshness_override_seconds", oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0,
		),
		"note", oas.Obj(
			"type", "string",
			"maxLength", 200,
			"description", "Notes containing a recognized secret are rejected.",
		),
	))
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := knowledgeRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case knowledgeBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = knowledgeRequestBody(method, pattern)
	case knowledgeBodyOpaque:
		doc.BodyKind = api.ModuleOperationOpaqueBody
		doc.RequestBody, _ = knowledgeRequestBody(method, pattern)
	case knowledgeBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case knowledgeBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

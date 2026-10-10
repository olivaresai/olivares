// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// deployRequestBodyKind records the handler-backed disposition of every Deploy
// mutation. No-derivable and pending are explicit even though the current census has
// neither, so an undecided future route cannot silently become bodyless.
type deployRequestBodyKind uint8

const (
	deployBodyless deployRequestBodyKind = iota + 1
	deployBodyful
	deployBodyNoDerivable
	deployBodyPending
)

type deployRequestBodyDeclaration struct {
	kind     deployRequestBodyKind
	required bool
	schema   func() map[string]any
}

// deployRequestBody returns the handler-derived requestBody for a known bodyful
// mutation; OperationDocumentation publishes it.
func deployRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := deployRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != deployBodyful || decl.schema == nil {
		return nil, false
	}
	return oas.Obj(
		"required", decl.required,
		"description", "The handler accepts one JSON document and caps decoding at 256 KiB.",
		"content", oas.Obj(
			"application/json", oas.Obj("schema", decl.schema()),
		),
	), true
}

// deployRequestBodyDeclarationFor classifies all eight non-GET routes registered by
// Deploy. Apply and retire conditionally decode mutationRequest only when a body is
// present; their requestBody therefore exists but is not required.
func deployRequestBodyDeclarationFor(method, pattern string) (deployRequestBodyDeclaration, bool) {

	switch method + " " + pattern {
	case http.MethodPut + " /executor/config":
		return deployBodyDeclaration(true, deployExecutorConfigSchema), true
	case http.MethodPost + " /definitions":
		return deployBodyDeclaration(true, deployCreateDefinitionSchema), true
	case http.MethodPut + " /definitions/{id}":
		return deployBodyDeclaration(true, deployUpdateDefinitionSchema), true
	case http.MethodPost + " /definitions/{id}/rollback":
		return deployBodyDeclaration(true, deployRollbackSchema), true
	case http.MethodPost + " /definitions/{id}/apply":
		return deployBodyDeclaration(false, deployApplyMutationSchema), true
	case http.MethodPost + " /definitions/{id}/retire":
		return deployBodyDeclaration(false, deployApprovalMutationSchema), true

	case http.MethodDelete + " /definitions/{id}",
		http.MethodPost + " /definitions/{id}/plan",
		http.MethodPost + " /definitions/{id}/verify",
		http.MethodPost + " /executor/test":
		return deployRequestBodyDeclaration{kind: deployBodyless}, true
	default:
		return deployRequestBodyDeclaration{}, false
	}
}

func deployExecutorConfigSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"socket_path", oas.Obj("type", "string", "enum", oas.Enum("/var/run/docker.sock", "/run/docker.sock"), "default", "/var/run/docker.sock"),
		"credential_ref", oas.Obj("type", "string", "description", "A store:<name> reference into this tenant's sealed secret store; secret values and other schemes are refused."),
	), "credential_ref")
}

func deployBodyDeclaration(required bool, schema func() map[string]any) deployRequestBodyDeclaration {
	return deployRequestBodyDeclaration{
		kind:     deployBodyful,
		required: required,
		schema:   schema,
	}
}

func deployClosedObject(properties map[string]any, required ...string) map[string]any {
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

func deployNullable(schema map[string]any) map[string]any {
	return oas.Obj("anyOf", []any{schema, oas.Obj("type", "null")})
}

func deployBoundedString(maxBytes int) map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "The handler limits this value to "+strconv.Itoa(maxBytes)+" UTF-8 bytes.",
	)
}

func deployNonBlankString(maxBytes int) map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "After Unicode whitespace trimming, the handler requires a non-empty value of at most "+strconv.Itoa(maxBytes)+" UTF-8 bytes.",
	)
}

func deployCreateDefinitionSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"subject_kind", oas.Obj(
			"type", "string",
			"description", "Trimmed and lowercased by the handler; the normalized value must be agent or mcp_server.",
		),
		"subject_ref", deployNonBlankString(512),
		"name", deployNonBlankString(200),
		"environment", deployNonBlankString(200),
		"target", deployNonBlankReferenceString(512),
		"runtime", deployNullable(deployBoundedString(200)),
		"source_ref", deployNullable(deployReferenceString(512)),
		"spec", deployNullable(deployDesiredSpecSchema()),
	), "subject_kind", "subject_ref", "name", "environment", "target", "spec")
}

func deployUpdateDefinitionSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"target", deployNullable(deployNonBlankReferenceString(512)),
		"source_ref", deployNullable(deployReferenceString(512)),
		"note", deployNullable(deployBoundedString(4096)),
		"spec", deployNullable(deployDesiredSpecSchema()),
	), "spec")
}

// deployReferenceString records the bounds the handler enforces. Its inline-credential
// heuristic cannot be represented as a stable JSON Schema pattern, so it is stated
// rather than approximated with a narrower invented grammar.
func deployReferenceString(maxBytes int) map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "The handler limits this value to "+strconv.Itoa(maxBytes)+" UTF-8 bytes and rejects values that look like inline credential material.",
	)
}

func deployNonBlankReferenceString(maxBytes int) map[string]any {
	schema := deployReferenceString(maxBytes)
	schema["description"] = "After Unicode whitespace trimming, the handler requires a non-empty value. " + schema["description"].(string)
	return schema
}

func deployDesiredSpecSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"image", deployNullable(deploySpecString()),
		"command", deployNullable(deploySpecString()),
		"replicas", deployNullable(oas.Obj("type", "integer", "minimum", 0, "maximum", 10000)),
		"resources", deployNullable(oas.Obj(
			"type", "object",
			"additionalProperties", deployNullable(deployBoundedString(200)),
			"description", "The handler limits every key and value to 200 UTF-8 bytes and rejects inline credential material; a JSON null value decodes to an empty string.",
		)),
		"env_refs", deployNullable(oas.Obj(
			"type", "array",
			"maxItems", 200,
			"items", deployEnvRefSchema(),
		)),
		"wirings", deployNullable(oas.Obj(
			"type", "array",
			"maxItems", 200,
			"items", deployWiringSchema(),
		)),
		"identity", deployNullable(deployIdentitySchema()),
	))
}

func deploySpecString() map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "The handler limits this value to 2048 UTF-8 bytes and rejects values that look like inline credential material.",
	)
}

func deployEnvRefSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"name", oas.Obj(
			"type", "string",
			"description", "After Unicode whitespace trimming, the handler requires a non-empty value of at most 200 UTF-8 bytes and rejects inline credential material.",
		),
		"secret_ref", deployNullable(deploySecretRefSchema()),
	), "name")
}

func deployWiringSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"resource_kind", deployNonBlankReferenceString(200),
		"resource_ref", deployNonBlankReferenceString(512),
		"mode", oas.Obj(
			"type", "string",
			"description", "Trimmed and lowercased by the handler; the normalized value must be read, write or readwrite.",
		),
		"secret_ref", deployNullable(deploySecretRefSchema()),
	), "resource_kind", "resource_ref", "mode")
}

func deploySecretRefSchema() map[string]any {
	return oas.Obj(
		"type", "string",
		"description", "Empty or at most 512 UTF-8 bytes in <scheme>:<locator> form. The handler case-insensitively allowlists vault, infisical, aws-secretsmanager, gcp-secretmanager, azure-keyvault, k8s-secret, env and file, and rejects empty locators or inline credentials.",
	)
}

func deployIdentitySchema() map[string]any {
	schema := deployClosedObject(oas.Obj(
		"identity_ref", deployNullable(deployReferenceString(512)),
		"mint", deployNullable(oas.Obj("type", "boolean")),
	))
	schema["anyOf"] = []any{
		oas.Obj(
			"required", oas.Enum("identity_ref"),
			"properties", oas.Obj("identity_ref", oas.Obj("type", "string", "minLength", 1)),
		),
		oas.Obj(
			"required", oas.Enum("mint"),
			"properties", oas.Obj("mint", oas.Obj("const", true)),
		),
	}
	return schema
}

func deployRollbackSchema() map[string]any {
	return deployClosedObject(oas.Obj(
		"to_version", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"note", deployNullable(deployBoundedString(4096)),
	), "to_version")
}

func deployApprovalMutationSchema() map[string]any {
	// A JSON null body decodes to the zero mutationRequest just like an empty body.
	return deployNullable(deployClosedObject(oas.Obj(
		"approval_ref", deployNullable(oas.Obj(
			"type", "string",
			"description", "The handler trims surrounding Unicode whitespace before use.",
		)),
	)))
}

func deployApplyMutationSchema() map[string]any {
	return deployNullable(deployClosedObject(oas.Obj(
		"approval_ref", deployNullable(oas.Obj("type", "string")),
		"catalog_source", deployNullable(deployClosedObject(oas.Obj(
			"source_ref", oas.Obj("type", "string", "description", "The exact source label: catalog entry <entry-id>."),
			"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("agent", "mcp_server")),
			"spec", deployDesiredSpecSchema(),
		), "source_ref", "subject_kind", "spec")),
	)))
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := deployRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case deployBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = deployRequestBody(method, pattern)
	case deployBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	case deployBodyNoDerivable, deployBodyPending:
		// Undecided: published unclassified until the handler is read.
	}
	return doc, true
}

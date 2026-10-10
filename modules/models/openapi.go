// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// modelsRequestBodyKind records the handler-derived body behavior of every
// mutating Models route. POST bodyless is separate from DELETE bodyless so an
// accidental decoder on a command-like POST cannot disappear into the deletes.
type modelsRequestBodyKind uint8

const (
	modelsBodyful modelsRequestBodyKind = iota + 1
	modelsPostBodyless
	modelsDeleteBodyless
)

type modelsRequestBodyDeclaration struct {
	kind   modelsRequestBodyKind
	schema func() map[string]any
}

// modelsRequestBody returns a fresh OpenAPI 3.1 requestBody for one bodyful
// Models operation; OperationDocumentation publishes it.
func modelsRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := modelsRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != modelsBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj(
			"application/json", oas.Obj("schema", decl.schema()),
		),
	), true
}

// modelsRequestBodyDeclarationFor explicitly classifies all 35 non-GET routes
// registered by modules/models. Every bodyful declaration is built from the DTO
// decoded by its handler and from validation performed before the mutation.
func modelsRequestBodyDeclarationFor(method, pattern string) (modelsRequestBodyDeclaration, bool) {

	var schema func() map[string]any
	switch method + " " + pattern {
	case http.MethodPost + " /routing-policies",
		http.MethodPut + " /routing-policies/{id}":
		schema = modelsRoutingPolicySchema
	case http.MethodPost + " /routing-policies/{id}/execute":
		schema = modelsExecuteRoutingSchema
	case http.MethodPost + " /keys",
		http.MethodPut + " /keys/{id}":
		schema = modelsKeyRefSchema
	case http.MethodPut + " /workspace-residency":
		schema = modelsWorkspaceResidencySchema
	case http.MethodPut + " /access-tier-entitlements":
		schema = modelsAccessTierEntitlementSchema
	case http.MethodPost + " /owned-models",
		http.MethodPut + " /owned-models/{id}":
		schema = modelsOwnedModelSchema
	case http.MethodPost + " /model-versions":
		schema = modelsModelVersionSchema
	case http.MethodPost + " /inference-deployments",
		http.MethodPut + " /inference-deployments/{id}":
		schema = modelsInferenceDeploymentSchema
	case http.MethodPost + " /finetune-jobs",
		http.MethodPut + " /finetune-jobs/{id}":
		schema = modelsFinetuneJobSchema
	case http.MethodPut + " /gpai-posture":
		schema = modelsGPAIPostureSchema
	case http.MethodPut + " /admission-policy":
		schema = modelsAdmissionPolicySchema
	case http.MethodPost + " /model-versions/{id}/admit":
		schema = modelsAdmitVersionSchema
	case http.MethodPost + " /datasets":
		schema = modelsDatasetSchema
	case http.MethodPost + " /agent-artifacts":
		schema = modelsAgentArtifactSchema
	case http.MethodPost + " /model-groups",
		http.MethodPut + " /model-groups/{id}":
		schema = modelsModelGroupSchema
	case http.MethodPost + " /model-access",
		http.MethodPut + " /model-access/{id}":
		schema = modelsModelAccessSchema
	case http.MethodPost + " /routing-policies/{id}/resolve",
		http.MethodPost + " /owned-models/{id}/aibom",
		http.MethodPost + " /agent-artifacts/aibom":
		return modelsRequestBodyDeclaration{kind: modelsPostBodyless}, true
	case http.MethodDelete + " /routing-policies/{id}",
		http.MethodDelete + " /keys/{id}",
		http.MethodDelete + " /owned-models/{id}",
		http.MethodDelete + " /model-versions/{id}",
		http.MethodDelete + " /inference-deployments/{id}",
		http.MethodDelete + " /datasets/{id}",
		http.MethodDelete + " /agent-artifacts/{id}",
		http.MethodDelete + " /model-groups/{id}",
		http.MethodDelete + " /model-access/{id}":
		return modelsRequestBodyDeclaration{kind: modelsDeleteBodyless}, true
	default:
		return modelsRequestBodyDeclaration{}, false
	}
	return modelsRequestBodyDeclaration{kind: modelsBodyful, schema: schema}, true
}

func modelsObjectSchema(properties map[string]any, required ...string) map[string]any {
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

func modelsPermissiveObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := oas.Obj(
		"type", "object",
		"additionalProperties", true,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = oas.Enum(required...)
	}
	return schema
}

func modelsStringArraySchema() map[string]any {
	return oas.Obj("type", "array", "items", oas.Obj("type", "string"))
}

func modelsNonBlankStringSchema() map[string]any {
	return oas.Obj("type", "string", "minLength", 1, "pattern", `\S`)
}

func modelsRoutingPolicySchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", oas.Obj("type", "string", "minLength", 1),
		"enabled", oas.Obj("type", "boolean"),
		"strategy", oas.Obj(
			"type", "string",
			"default", "cost",
			"description", "Unknown or empty values are normalized to cost by the handler.",
		),
		"required_capabilities", modelsStringArraySchema(),
		"preferred_providers", modelsStringArraySchema(),
		"min_context_window", oas.Obj("type", "integer", "format", "int64"),
		"pinned_model", oas.Obj("type", "string"),
		"allow_deprecated", oas.Obj("type", "boolean"),
		"gateway_endpoint", oas.Obj("type", "string"),
		"execution_profile_ref", oas.Obj("type", "string"),
		"execution_profile_revision", oas.Obj("type", "string", "pattern", `^sha256:[0-9a-f]{64}$`),
		"deny_retired", oas.Obj("type", "boolean"),
		"deny_deprecated", oas.Obj("type", "boolean"),
		"require_zdr", oas.Obj("type", "boolean"),
		"access_tiers", modelsStringArraySchema(),
	), "name")
}

func modelsExecuteRoutingSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"input", modelsNonBlankStringSchema(),
		"max_tokens", oas.Obj(
			"type", "integer",
			"description", "Values less than or equal to zero are replaced with the handler default of 1024.",
		),
		"session_ref", oas.Obj("type", "string"),
		"surface", oas.Obj("type", "string"),
		"operation", oas.Obj("type", "string", "enum", oas.Enum("text.generate")),
	), "input")
}

func modelsKeyRefSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"ref_kind", oas.Obj("type", "string", "enum", oas.Enum("api_key", "workspace")),
		"provider_ref", modelsNonBlankStringSchema(),
		"ext_id", modelsNonBlankStringSchema(),
		"name", oas.Obj("type", "string"),
		"workspace_ref", oas.Obj("type", "string"),
		"status", oas.Obj("type", "string", "default", "active"),
		"hint", oas.Obj("type", "string", "maxLength", 64),
		"owner_ref", oas.Obj("type", "string"),
		"created_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
	), "ref_kind", "provider_ref", "ext_id")
}

func modelsWorkspaceResidencySchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"workspace_ref", modelsNonBlankStringSchema(),
		"allowed_geos", modelsStringArraySchema(),
		"default_geo", oas.Obj("type", "string"),
		"workspace_geo", oas.Obj("type", "string"),
		"as_of", oas.Obj("type", "string"),
	), "workspace_ref")
}

func modelsAccessTierEntitlementSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"tier", modelsNonBlankStringSchema(),
		"state", oas.Obj("type", "string", "enum", oas.Enum("granted", "suspended")),
		"note", oas.Obj("type", "string"),
		"as_of", oas.Obj("type", "string"),
		"updated_by", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
	), "tier", "state")
}

func modelsOwnedModelSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"kind", oas.Obj("type", "string", "enum", oas.Enum("hosted", "fine_tuned", "imported")),
		"base_ref", oas.Obj("type", "string"),
		"provider_ref", oas.Obj("type", "string"),
		"visibility", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "private", "internal"),
			"default", "private",
		),
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "active", "deprecated", "draft"),
			"default", "active",
		),
		"owner_ref", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "name", "kind")
}

func modelsModelVersionSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"owned_ref", modelsNonBlankStringSchema(),
		"version", modelsNonBlankStringSchema(),
		"artifact_ref", oas.Obj("type", "string"),
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "draft", "active", "deprecated"),
			"default", "draft",
		),
		"parent_ref", oas.Obj("type", "string"),
		"source_ref", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "owned_ref", "version")
}

func modelsInferenceDeploymentSchema() map[string]any {
	properties := oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"runtime", oas.Obj("type", "string", "enum", oas.Enum("vllm", "ollama", "llamacpp", "other")),
		"deployment_type", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "local", "brokered", "unclassified"),
			"description", "When omitted, both owned_ref and version_ref derive local; otherwise the handler derives unclassified.",
		),
		"endpoint_ref", oas.Obj("type", "string"),
		"owned_ref", oas.Obj("type", "string"),
		"version_ref", oas.Obj("type", "string"),
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "active", "stopped"),
			"default", "active",
		),
		"governed", oas.Obj("type", "boolean"),
		"note", oas.Obj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "name", "runtime")
	schema["allOf"] = []any{
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("deployment_type"),
				"properties", oas.Obj("deployment_type", oas.Obj("const", "local")),
			),
			"then", oas.Obj(
				"required", oas.Enum("owned_ref", "version_ref"),
				"properties", oas.Obj(
					"owned_ref", modelsNonBlankStringSchema(),
					"version_ref", modelsNonBlankStringSchema(),
				),
			),
		),
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("deployment_type"),
				"properties", oas.Obj("deployment_type", oas.Obj("const", "brokered")),
			),
			"then", oas.Obj(
				"required", oas.Enum("endpoint_ref"),
				"properties", oas.Obj(
					"endpoint_ref", modelsNonBlankStringSchema(),
					"owned_ref", oas.Obj("type", "string", "pattern", `^\s*$`),
					"version_ref", oas.Obj("type", "string", "pattern", `^\s*$`),
				),
			),
		),
	}
	return schema
}

func modelsFinetuneJobSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"base_ref", oas.Obj("type", "string"),
		"dataset_ref", oas.Obj("type", "string"),
		"runtime", oas.Obj("type", "string", "enum", oas.Enum("", "vllm", "ollama", "llamacpp", "other")),
		"status", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "queued", "running", "succeeded", "failed", "canceled"),
			"default", "queued",
		),
		"result_version_ref", oas.Obj("type", "string"),
		"started_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
		"ended_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
		"note", oas.Obj("type", "string"),
	), "name")
}

func modelsGPAIPostureSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"provider_ref", modelsNonBlankStringSchema(),
		"cop_signatory", oas.Obj("type", "boolean"),
		"technical_docs", oas.Obj("type", "boolean"),
		"training_data_summary", oas.Obj("type", "boolean"),
		"copyright_policy", oas.Obj("type", "boolean"),
		"downstream_info", oas.Obj("type", "boolean"),
		"systemic_risk", oas.Obj("type", "boolean"),
		"safety_report", oas.Obj("type", "boolean"),
		"verified", oas.Obj("type", "boolean"),
		"verification_method", oas.Obj("type", "string"),
		"attested_by", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oas.Obj("type", "string"),
	), "provider_ref")
}

func modelsAdmissionPolicySchema() map[string]any {
	publicMaterial := oas.Obj(
		"type", "array",
		"items", oas.Obj(
			"type", "string",
			"not", oas.Obj("pattern", "PRIVATE KEY"),
		),
	)
	properties := oas.Obj(
		"require_signed", oas.Obj("type", "boolean"),
		"require_artifact_digests", oas.Obj("type", "boolean"),
		"allowed_identities", modelsStringArraySchema(),
		"allowed_issuers", modelsStringArraySchema(),
		"trusted_keys", publicMaterial,
		"trusted_roots", oas.Obj(
			"type", "array",
			"items", oas.Obj(
				"type", "string",
				"not", oas.Obj("pattern", "PRIVATE KEY"),
			),
		),
		"note", oas.Obj("type", "string"),
		"attested_by", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
	)
	schema := modelsObjectSchema(properties)
	schema["allOf"] = []any{
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("require_signed"),
				"properties", oas.Obj("require_signed", oas.Obj("const", true)),
			),
			"then", oas.Obj("anyOf", []any{
				oas.Obj(
					"required", oas.Enum("trusted_roots"),
					"properties", oas.Obj("trusted_roots", oas.Obj("minItems", 1)),
				),
				oas.Obj(
					"required", oas.Enum("trusted_keys"),
					"properties", oas.Obj("trusted_keys", oas.Obj("minItems", 1)),
				),
			}),
		),
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("allowed_identities"),
				"properties", oas.Obj("allowed_identities", oas.Obj("minItems", 1)),
			),
			"then", oas.Obj(
				"required", oas.Enum("allowed_issuers"),
				"properties", oas.Obj("allowed_issuers", oas.Obj("minItems", 1)),
			),
		),
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("allowed_issuers"),
				"properties", oas.Obj("allowed_issuers", oas.Obj("minItems", 1)),
			),
			"then", oas.Obj(
				"required", oas.Enum("allowed_identities"),
				"properties", oas.Obj("allowed_identities", oas.Obj("minItems", 1)),
			),
		),
	}
	return schema
}

func modelsAdmitVersionSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"bundle", modelsOMSSigstoreBundleSchema(),
		"resolved_digests", oas.Obj(
			"type", "object",
			"additionalProperties", oas.Obj("type", "string"),
		),
		"model_ref", oas.Obj("type", "string"),
		"aibom_ref", oas.Obj("type", "string"),
		"note", oas.Obj("type", "string"),
	), "bundle")
}

// modelsOMSSigstoreBundleSchema follows the private wire structs consumed by
// core/secure/modelsign.Verify. Unlike the outer Models DTO, those structs are
// decoded with json.Unmarshal, so unknown properties remain allowed at every
// bundle layer. The DSSE payload exposes its decoded OMS statement via the
// OpenAPI 3.1 contentSchema keyword while remaining base64 on the wire.
func modelsOMSSigstoreBundleSchema() map[string]any {
	certificate := modelsPermissiveObjectSchema(oas.Obj(
		"rawBytes", oas.Obj("type", "string", "contentEncoding", "base64"),
	))
	verificationMaterial := modelsPermissiveObjectSchema(oas.Obj(
		"certificate", certificate,
		"x509CertificateChain", modelsPermissiveObjectSchema(oas.Obj(
			"certificates", oas.Obj("type", "array", "items", certificate),
		)),
		"publicKey", modelsPermissiveObjectSchema(oas.Obj(
			"hint", oas.Obj("type", "string"),
		)),
		"tlogEntries", oas.Obj("type", "array", "items", oas.Obj()),
	))
	dsseSignature := modelsPermissiveObjectSchema(oas.Obj(
		"sig", oas.Obj("type", "string", "contentEncoding", "base64"),
		"keyid", oas.Obj("type", "string"),
	))
	dsseEnvelope := modelsPermissiveObjectSchema(oas.Obj(
		"payload", oas.Obj(
			"type", "string",
			"contentEncoding", "base64",
			"contentMediaType", "application/vnd.in-toto+json",
			"contentSchema", modelsOMSStatementSchema(),
		),
		"payloadType", oas.Obj("type", "string", "const", "application/vnd.in-toto+json"),
		"signatures", oas.Obj(
			"type", "array",
			"minItems", 1,
			"items", dsseSignature,
		),
	), "payloadType", "signatures")
	return modelsPermissiveObjectSchema(oas.Obj(
		"mediaType", oas.Obj("type", "string"),
		"verificationMaterial", verificationMaterial,
		"dsseEnvelope", dsseEnvelope,
	), "dsseEnvelope")
}

func modelsOMSStatementSchema() map[string]any {
	stringMap := oas.Obj(
		"type", "object",
		"additionalProperties", oas.Obj("type", "string"),
	)
	resource := modelsPermissiveObjectSchema(oas.Obj(
		"name", oas.Obj("type", "string"),
		"digest", oas.Obj("type", "string"),
		"algorithm", oas.Obj(
			"type", "string",
			"description", "OMS declares sha256, blake2b or blake3; the verifier records but does not reject other strings.",
		),
	))
	predicate := modelsPermissiveObjectSchema(oas.Obj(
		"resources", oas.Obj(
			"type", "array",
			"items", resource,
			"description", "An empty manifest produces a recorded unverified verdict rather than a malformed-body response.",
		),
		"serialization", modelsPermissiveObjectSchema(oas.Obj(
			"method", oas.Obj("type", "string", "description", "OMS vocabulary: files or shards."),
			"hash_type", oas.Obj("type", "string", "description", "OMS vocabulary: sha256, blake2b or blake3."),
			"allow_symlinks", oas.Obj("type", "boolean"),
			"shard_size", oas.Obj("type", "integer"),
			"ignore_paths", modelsStringArraySchema(),
		)),
	))
	return modelsPermissiveObjectSchema(oas.Obj(
		"_type", oas.Obj(
			"type", "string",
			"description", "A verified OMS statement uses https://in-toto.io/Statement/v1; other values produce an unverified verdict.",
		),
		"subject", oas.Obj(
			"type", "array",
			"items", modelsPermissiveObjectSchema(oas.Obj(
				"name", oas.Obj("type", "string"),
				"digest", stringMap,
			)),
		),
		"predicateType", oas.Obj(
			"type", "string",
			"description", "A verified OMS statement uses https://model_signing/signature/v1.0; other values produce an unverified verdict.",
		),
		"predicate", predicate,
	))
}

func modelsDatasetSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"owned_ref", oas.Obj("type", "string"),
		"classification", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "public", "internal", "confidential", "restricted", "pii", "other"),
			"default", "other",
		),
		"governance", oas.Obj("type", "string"),
		"source_ref", oas.Obj("type", "string"),
		"content_hash", oas.Obj("type", "string"),
		"content_alg", oas.Obj(
			"type", "string",
			"description", "Empty defaults to sha256 when content_hash is non-empty.",
		),
		"verified", oas.Obj("type", "boolean"),
		"attested_by", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oas.Obj("type", "string"),
	), "name")
}

func modelsAgentArtifactSchema() map[string]any {
	properties := oas.Obj(
		"id", oas.Obj("type", "string"),
		"artifact_class", oas.Obj(
			"type", "string",
			"enum", oas.Enum("skill", "mcpb_extension", "mcp_app_template", "agents_md"),
		),
		"name", modelsNonBlankStringSchema(),
		"version", oas.Obj("type", "string"),
		"provenance", oas.Obj("type", "string"),
		"source_ref", oas.Obj("type", "string"),
		"content_hash", oas.Obj("type", "string"),
		"content_alg", oas.Obj(
			"type", "string",
			"description", "Empty defaults to sha256 when content_hash is non-empty.",
		),
		"posture_grade", oas.Obj("type", "string", "enum", oas.Enum("", "A", "B", "C", "D", "F")),
		"posture_issues", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"posture_scanned", oas.Obj("type", "boolean"),
		"verified", oas.Obj("type", "boolean"),
		"attested_by", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oas.Obj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oas.Obj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "artifact_class", "name")
	schema["allOf"] = []any{
		oas.Obj(
			"if", oas.Obj("anyOf", []any{
				oas.Obj("not", oas.Obj("required", oas.Enum("posture_grade"))),
				oas.Obj("properties", oas.Obj("posture_grade", oas.Obj("const", ""))),
			}),
			"then", oas.Obj("properties", oas.Obj(
				"posture_scanned", oas.Obj("const", false),
				"posture_issues", oas.Obj("const", 0),
			)),
		),
	}
	return schema
}

func modelsModelGroupSchema() map[string]any {
	properties := oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"member_refs", modelsStringArraySchema(),
		"family_selectors", modelsStringArraySchema(),
		"tier_selectors", modelsStringArraySchema(),
		"description", oas.Obj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "name")
	nonBlankSelector := oas.Obj("type", "string", "pattern", `\S`)
	schema["anyOf"] = []any{
		oas.Obj(
			"required", oas.Enum("member_refs"),
			"properties", oas.Obj("member_refs", oas.Obj("contains", nonBlankSelector)),
		),
		oas.Obj(
			"required", oas.Enum("family_selectors"),
			"properties", oas.Obj("family_selectors", oas.Obj("contains", nonBlankSelector)),
		),
		oas.Obj(
			"required", oas.Enum("tier_selectors"),
			"properties", oas.Obj("tier_selectors", oas.Obj("contains", nonBlankSelector)),
		),
	}
	return schema
}

func modelsModelAccessSchema() map[string]any {
	return modelsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"subject_kind", oas.Obj(
			"type", "string",
			"enum", oas.Enum("user", "role", "user_group", "agent_group"),
		),
		"subject_ref", modelsNonBlankStringSchema(),
		"target_kind", oas.Obj("type", "string", "enum", oas.Enum("model", "model_group")),
		"target_ref", modelsNonBlankStringSchema(),
		"workspace_ref", oas.Obj("type", "string"),
		"surfaces", oas.Obj(
			"type", "array",
			"items", oas.Obj(
				"type", "string",
				"enum", oas.Enum("", "direct", "bedrock-mantle", "bedrock-legacy", "vertex", "foundry", "claude-platform-aws"),
			),
		),
		"budget_ref", oas.Obj("type", "string"),
		"effect", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "allow", "forbid"),
			"default", "allow",
		),
		"description", oas.Obj("type", "string"),
	), "subject_kind", "subject_ref", "target_kind", "target_ref")
}

// OperationDocumentation publishes the request body each mutation's handler decodes.
// A route this module has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	decl, ok := modelsRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case modelsBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = modelsRequestBody(method, pattern)
	case modelsPostBodyless, modelsDeleteBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	}
	return doc, true
}

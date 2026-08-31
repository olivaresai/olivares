// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

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
// Models operation. moduleRequestBody consults this feature registry before its
// shared sessions and eventing contracts.
func modelsRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := modelsRequestBodyDeclarationFor(r)
	if !ok || decl.kind != modelsBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj(
			"application/json", oaObj("schema", decl.schema()),
		),
	), true
}

// modelsRequestBodyDeclarationFor explicitly classifies all 35 non-GET routes
// registered by modules/models. Every bodyful declaration is built from the DTO
// decoded by its handler and from validation performed before the mutation.
func modelsRequestBodyDeclarationFor(r moduleRoute) (modelsRequestBodyDeclaration, bool) {
	if r.ns != "models" {
		return modelsRequestBodyDeclaration{}, false
	}

	var schema func() map[string]any
	switch r.method + " " + r.pattern {
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

func modelsPermissiveObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := oaObj(
		"type", "object",
		"additionalProperties", true,
		"properties", properties,
	)
	if len(required) > 0 {
		schema["required"] = oaEnum(required...)
	}
	return schema
}

func modelsStringArraySchema() map[string]any {
	return oaObj("type", "array", "items", oaObj("type", "string"))
}

func modelsNonBlankStringSchema() map[string]any {
	return oaObj("type", "string", "minLength", 1, "pattern", `\S`)
}

func modelsRoutingPolicySchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"name", oaObj("type", "string", "minLength", 1),
		"enabled", oaObj("type", "boolean"),
		"strategy", oaObj(
			"type", "string",
			"default", "cost",
			"description", "Unknown or empty values are normalized to cost by the handler.",
		),
		"required_capabilities", modelsStringArraySchema(),
		"preferred_providers", modelsStringArraySchema(),
		"min_context_window", oaObj("type", "integer", "format", "int64"),
		"pinned_model", oaObj("type", "string"),
		"allow_deprecated", oaObj("type", "boolean"),
		"gateway_endpoint", oaObj("type", "string"),
		"deny_retired", oaObj("type", "boolean"),
		"deny_deprecated", oaObj("type", "boolean"),
		"require_zdr", oaObj("type", "boolean"),
		"access_tiers", modelsStringArraySchema(),
	), "name")
}

func modelsExecuteRoutingSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"input", modelsNonBlankStringSchema(),
		"max_tokens", oaObj(
			"type", "integer",
			"description", "Values less than or equal to zero are replaced with the handler default of 1024.",
		),
		"session_ref", oaObj("type", "string"),
		"surface", oaObj("type", "string"),
	), "input")
}

func modelsKeyRefSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"ref_kind", oaObj("type", "string", "enum", oaEnum("api_key", "workspace")),
		"provider_ref", modelsNonBlankStringSchema(),
		"ext_id", modelsNonBlankStringSchema(),
		"name", oaObj("type", "string"),
		"workspace_ref", oaObj("type", "string"),
		"status", oaObj("type", "string", "default", "active"),
		"hint", oaObj("type", "string", "maxLength", 64),
		"owner_ref", oaObj("type", "string"),
		"created_at", oaObj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
	), "ref_kind", "provider_ref", "ext_id")
}

func modelsWorkspaceResidencySchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"workspace_ref", modelsNonBlankStringSchema(),
		"allowed_geos", modelsStringArraySchema(),
		"default_geo", oaObj("type", "string"),
		"workspace_geo", oaObj("type", "string"),
		"as_of", oaObj("type", "string"),
	), "workspace_ref")
}

func modelsAccessTierEntitlementSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"tier", modelsNonBlankStringSchema(),
		"state", oaObj("type", "string", "enum", oaEnum("granted", "suspended")),
		"note", oaObj("type", "string"),
		"as_of", oaObj("type", "string"),
		"updated_by", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
	), "tier", "state")
}

func modelsOwnedModelSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"kind", oaObj("type", "string", "enum", oaEnum("hosted", "fine_tuned", "imported")),
		"base_ref", oaObj("type", "string"),
		"provider_ref", oaObj("type", "string"),
		"visibility", oaObj(
			"type", "string",
			"enum", oaEnum("", "private", "internal"),
			"default", "private",
		),
		"status", oaObj(
			"type", "string",
			"enum", oaEnum("", "active", "deprecated", "draft"),
			"default", "active",
		),
		"owner_ref", oaObj("type", "string"),
		"note", oaObj("type", "string"),
	), "name", "kind")
}

func modelsModelVersionSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"owned_ref", modelsNonBlankStringSchema(),
		"version", modelsNonBlankStringSchema(),
		"artifact_ref", oaObj("type", "string"),
		"status", oaObj(
			"type", "string",
			"enum", oaEnum("", "draft", "active", "deprecated"),
			"default", "draft",
		),
		"parent_ref", oaObj("type", "string"),
		"source_ref", oaObj("type", "string"),
		"note", oaObj("type", "string"),
	), "owned_ref", "version")
}

func modelsInferenceDeploymentSchema() map[string]any {
	properties := oaObj(
		"id", oaObj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"runtime", oaObj("type", "string", "enum", oaEnum("vllm", "ollama", "llamacpp", "other")),
		"deployment_type", oaObj(
			"type", "string",
			"enum", oaEnum("", "local", "brokered", "unclassified"),
			"description", "When omitted, both owned_ref and version_ref derive local; otherwise the handler derives unclassified.",
		),
		"endpoint_ref", oaObj("type", "string"),
		"owned_ref", oaObj("type", "string"),
		"version_ref", oaObj("type", "string"),
		"status", oaObj(
			"type", "string",
			"enum", oaEnum("", "active", "stopped"),
			"default", "active",
		),
		"governed", oaObj("type", "boolean"),
		"note", oaObj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "name", "runtime")
	schema["allOf"] = []any{
		oaObj(
			"if", oaObj(
				"required", oaEnum("deployment_type"),
				"properties", oaObj("deployment_type", oaObj("const", "local")),
			),
			"then", oaObj(
				"required", oaEnum("owned_ref", "version_ref"),
				"properties", oaObj(
					"owned_ref", modelsNonBlankStringSchema(),
					"version_ref", modelsNonBlankStringSchema(),
				),
			),
		),
		oaObj(
			"if", oaObj(
				"required", oaEnum("deployment_type"),
				"properties", oaObj("deployment_type", oaObj("const", "brokered")),
			),
			"then", oaObj(
				"required", oaEnum("endpoint_ref"),
				"properties", oaObj(
					"endpoint_ref", modelsNonBlankStringSchema(),
					"owned_ref", oaObj("type", "string", "pattern", `^\s*$`),
					"version_ref", oaObj("type", "string", "pattern", `^\s*$`),
				),
			),
		),
	}
	return schema
}

func modelsFinetuneJobSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"base_ref", oaObj("type", "string"),
		"dataset_ref", oaObj("type", "string"),
		"runtime", oaObj("type", "string", "enum", oaEnum("", "vllm", "ollama", "llamacpp", "other")),
		"status", oaObj(
			"type", "string",
			"enum", oaEnum("", "queued", "running", "succeeded", "failed", "canceled"),
			"default", "queued",
		),
		"result_version_ref", oaObj("type", "string"),
		"started_at", oaObj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
		"ended_at", oaObj(
			"type", "string",
			"description", "Accepted by the DTO; an invalid timestamp is silently omitted by the handler.",
		),
		"note", oaObj("type", "string"),
	), "name")
}

func modelsGPAIPostureSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"provider_ref", modelsNonBlankStringSchema(),
		"cop_signatory", oaObj("type", "boolean"),
		"technical_docs", oaObj("type", "boolean"),
		"training_data_summary", oaObj("type", "boolean"),
		"copyright_policy", oaObj("type", "boolean"),
		"downstream_info", oaObj("type", "boolean"),
		"systemic_risk", oaObj("type", "boolean"),
		"safety_report", oaObj("type", "boolean"),
		"verified", oaObj("type", "boolean"),
		"verification_method", oaObj("type", "string"),
		"attested_by", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oaObj("type", "string"),
	), "provider_ref")
}

func modelsAdmissionPolicySchema() map[string]any {
	publicMaterial := oaObj(
		"type", "array",
		"items", oaObj(
			"type", "string",
			"not", oaObj("pattern", "PRIVATE KEY"),
		),
	)
	properties := oaObj(
		"require_signed", oaObj("type", "boolean"),
		"require_artifact_digests", oaObj("type", "boolean"),
		"allowed_identities", modelsStringArraySchema(),
		"allowed_issuers", modelsStringArraySchema(),
		"trusted_keys", publicMaterial,
		"trusted_roots", oaObj(
			"type", "array",
			"items", oaObj(
				"type", "string",
				"not", oaObj("pattern", "PRIVATE KEY"),
			),
		),
		"note", oaObj("type", "string"),
		"attested_by", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
	)
	schema := modelsObjectSchema(properties)
	schema["allOf"] = []any{
		oaObj(
			"if", oaObj(
				"required", oaEnum("require_signed"),
				"properties", oaObj("require_signed", oaObj("const", true)),
			),
			"then", oaObj("anyOf", []any{
				oaObj(
					"required", oaEnum("trusted_roots"),
					"properties", oaObj("trusted_roots", oaObj("minItems", 1)),
				),
				oaObj(
					"required", oaEnum("trusted_keys"),
					"properties", oaObj("trusted_keys", oaObj("minItems", 1)),
				),
			}),
		),
		oaObj(
			"if", oaObj(
				"required", oaEnum("allowed_identities"),
				"properties", oaObj("allowed_identities", oaObj("minItems", 1)),
			),
			"then", oaObj(
				"required", oaEnum("allowed_issuers"),
				"properties", oaObj("allowed_issuers", oaObj("minItems", 1)),
			),
		),
		oaObj(
			"if", oaObj(
				"required", oaEnum("allowed_issuers"),
				"properties", oaObj("allowed_issuers", oaObj("minItems", 1)),
			),
			"then", oaObj(
				"required", oaEnum("allowed_identities"),
				"properties", oaObj("allowed_identities", oaObj("minItems", 1)),
			),
		),
	}
	return schema
}

func modelsAdmitVersionSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"bundle", modelsOMSSigstoreBundleSchema(),
		"resolved_digests", oaObj(
			"type", "object",
			"additionalProperties", oaObj("type", "string"),
		),
		"model_ref", oaObj("type", "string"),
		"aibom_ref", oaObj("type", "string"),
		"note", oaObj("type", "string"),
	), "bundle")
}

// modelsOMSSigstoreBundleSchema follows the private wire structs consumed by
// core/secure/modelsign.Verify. Unlike the outer Models DTO, those structs are
// decoded with json.Unmarshal, so unknown properties remain allowed at every
// bundle layer. The DSSE payload exposes its decoded OMS statement via the
// OpenAPI 3.1 contentSchema keyword while remaining base64 on the wire.
func modelsOMSSigstoreBundleSchema() map[string]any {
	certificate := modelsPermissiveObjectSchema(oaObj(
		"rawBytes", oaObj("type", "string", "contentEncoding", "base64"),
	))
	verificationMaterial := modelsPermissiveObjectSchema(oaObj(
		"certificate", certificate,
		"x509CertificateChain", modelsPermissiveObjectSchema(oaObj(
			"certificates", oaObj("type", "array", "items", certificate),
		)),
		"publicKey", modelsPermissiveObjectSchema(oaObj(
			"hint", oaObj("type", "string"),
		)),
		"tlogEntries", oaObj("type", "array", "items", oaObj()),
	))
	dsseSignature := modelsPermissiveObjectSchema(oaObj(
		"sig", oaObj("type", "string", "contentEncoding", "base64"),
		"keyid", oaObj("type", "string"),
	))
	dsseEnvelope := modelsPermissiveObjectSchema(oaObj(
		"payload", oaObj(
			"type", "string",
			"contentEncoding", "base64",
			"contentMediaType", "application/vnd.in-toto+json",
			"contentSchema", modelsOMSStatementSchema(),
		),
		"payloadType", oaObj("type", "string", "const", "application/vnd.in-toto+json"),
		"signatures", oaObj(
			"type", "array",
			"minItems", 1,
			"items", dsseSignature,
		),
	), "payloadType", "signatures")
	return modelsPermissiveObjectSchema(oaObj(
		"mediaType", oaObj("type", "string"),
		"verificationMaterial", verificationMaterial,
		"dsseEnvelope", dsseEnvelope,
	), "dsseEnvelope")
}

func modelsOMSStatementSchema() map[string]any {
	stringMap := oaObj(
		"type", "object",
		"additionalProperties", oaObj("type", "string"),
	)
	resource := modelsPermissiveObjectSchema(oaObj(
		"name", oaObj("type", "string"),
		"digest", oaObj("type", "string"),
		"algorithm", oaObj(
			"type", "string",
			"description", "OMS declares sha256, blake2b or blake3; the verifier records but does not reject other strings.",
		),
	))
	predicate := modelsPermissiveObjectSchema(oaObj(
		"resources", oaObj(
			"type", "array",
			"items", resource,
			"description", "An empty manifest produces a recorded unverified verdict rather than a malformed-body response.",
		),
		"serialization", modelsPermissiveObjectSchema(oaObj(
			"method", oaObj("type", "string", "description", "OMS vocabulary: files or shards."),
			"hash_type", oaObj("type", "string", "description", "OMS vocabulary: sha256, blake2b or blake3."),
			"allow_symlinks", oaObj("type", "boolean"),
			"shard_size", oaObj("type", "integer"),
			"ignore_paths", modelsStringArraySchema(),
		)),
	))
	return modelsPermissiveObjectSchema(oaObj(
		"_type", oaObj(
			"type", "string",
			"description", "A verified OMS statement uses https://in-toto.io/Statement/v1; other values produce an unverified verdict.",
		),
		"subject", oaObj(
			"type", "array",
			"items", modelsPermissiveObjectSchema(oaObj(
				"name", oaObj("type", "string"),
				"digest", stringMap,
			)),
		),
		"predicateType", oaObj(
			"type", "string",
			"description", "A verified OMS statement uses https://model_signing/signature/v1.0; other values produce an unverified verdict.",
		),
		"predicate", predicate,
	))
}

func modelsDatasetSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"owned_ref", oaObj("type", "string"),
		"classification", oaObj(
			"type", "string",
			"enum", oaEnum("", "public", "internal", "confidential", "restricted", "pii", "other"),
			"default", "other",
		),
		"governance", oaObj("type", "string"),
		"source_ref", oaObj("type", "string"),
		"content_hash", oaObj("type", "string"),
		"content_alg", oaObj(
			"type", "string",
			"description", "Empty defaults to sha256 when content_hash is non-empty.",
		),
		"verified", oaObj("type", "boolean"),
		"attested_by", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oaObj("type", "string"),
	), "name")
}

func modelsAgentArtifactSchema() map[string]any {
	properties := oaObj(
		"id", oaObj("type", "string"),
		"artifact_class", oaObj(
			"type", "string",
			"enum", oaEnum("skill", "mcpb_extension", "mcp_app_template", "agents_md"),
		),
		"name", modelsNonBlankStringSchema(),
		"version", oaObj("type", "string"),
		"provenance", oaObj("type", "string"),
		"source_ref", oaObj("type", "string"),
		"content_hash", oaObj("type", "string"),
		"content_alg", oaObj(
			"type", "string",
			"description", "Empty defaults to sha256 when content_hash is non-empty.",
		),
		"posture_grade", oaObj("type", "string", "enum", oaEnum("", "A", "B", "C", "D", "F")),
		"posture_issues", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"posture_scanned", oaObj("type", "boolean"),
		"verified", oaObj("type", "boolean"),
		"attested_by", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the authenticated actor is authoritative.",
		),
		"attested_at", oaObj(
			"type", "string",
			"description", "Accepted by the reused DTO; the server timestamp is authoritative.",
		),
		"note", oaObj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "artifact_class", "name")
	schema["allOf"] = []any{
		oaObj(
			"if", oaObj("anyOf", []any{
				oaObj("not", oaObj("required", oaEnum("posture_grade"))),
				oaObj("properties", oaObj("posture_grade", oaObj("const", ""))),
			}),
			"then", oaObj("properties", oaObj(
				"posture_scanned", oaObj("const", false),
				"posture_issues", oaObj("const", 0),
			)),
		),
	}
	return schema
}

func modelsModelGroupSchema() map[string]any {
	properties := oaObj(
		"id", oaObj("type", "string"),
		"name", modelsNonBlankStringSchema(),
		"member_refs", modelsStringArraySchema(),
		"family_selectors", modelsStringArraySchema(),
		"tier_selectors", modelsStringArraySchema(),
		"description", oaObj("type", "string"),
	)
	schema := modelsObjectSchema(properties, "name")
	nonBlankSelector := oaObj("type", "string", "pattern", `\S`)
	schema["anyOf"] = []any{
		oaObj(
			"required", oaEnum("member_refs"),
			"properties", oaObj("member_refs", oaObj("contains", nonBlankSelector)),
		),
		oaObj(
			"required", oaEnum("family_selectors"),
			"properties", oaObj("family_selectors", oaObj("contains", nonBlankSelector)),
		),
		oaObj(
			"required", oaEnum("tier_selectors"),
			"properties", oaObj("tier_selectors", oaObj("contains", nonBlankSelector)),
		),
	}
	return schema
}

func modelsModelAccessSchema() map[string]any {
	return modelsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"subject_kind", oaObj(
			"type", "string",
			"enum", oaEnum("user", "role", "user_group", "agent_group"),
		),
		"subject_ref", modelsNonBlankStringSchema(),
		"target_kind", oaObj("type", "string", "enum", oaEnum("model", "model_group")),
		"target_ref", modelsNonBlankStringSchema(),
		"workspace_ref", oaObj("type", "string"),
		"surfaces", oaObj(
			"type", "array",
			"items", oaObj(
				"type", "string",
				"enum", oaEnum("", "direct", "bedrock-mantle", "bedrock-legacy", "vertex", "foundry", "claude-platform-aws"),
			),
		),
		"budget_ref", oaObj("type", "string"),
		"effect", oaObj(
			"type", "string",
			"enum", oaEnum("", "allow", "forbid"),
			"default", "allow",
		),
		"description", oaObj("type", "string"),
	), "subject_kind", "subject_ref", "target_kind", "target_ref")
}

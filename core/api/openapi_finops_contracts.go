// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// finopsRequestBodyKind records whether a registered FinOps mutation consumes
// the JSON document described below or deliberately ignores the HTTP body.
type finopsRequestBodyKind uint8

const (
	finopsBodyful finopsRequestBodyKind = iota + 1
	finopsBodyless
)

type finopsRequestBodyDeclaration struct {
	kind   finopsRequestBodyKind
	schema func() map[string]any
}

// finopsOpenAPIContract is the handler-derived documentation carried by one
// FinOps mutation. schema is a builder, rather than a shared map, so callers can
// safely add operation-local annotations without mutating another operation.
type finopsOpenAPIContract struct {
	schema func() map[string]any
}

// finopsOpenAPIContracts is deliberately keyed by the same module-relative
// method and pattern APIRoutes registers. The schemas below project the exact
// JSON fields decoded by the matching FinOps handlers; server-owned fields that
// are present on a reused DTO remain visible because DisallowUnknownFields still
// accepts them, even where the handler ignores or overwrites their value.
var finopsOpenAPIContracts = map[string]finopsOpenAPIContract{
	http.MethodPost + " /budgets": {
		schema: finopsBudgetSchema,
	},
	http.MethodPut + " /budgets/{id}": {
		schema: finopsBudgetSchema,
	},
	http.MethodPost + " /cost": {
		schema: finopsCostIngestSchema,
	},
	http.MethodPost + " /cost-centers": {
		schema: func() map[string]any {
			return finopsCostCenterSchema(true)
		},
	},
	http.MethodPut + " /cost-centers/{id}": {
		schema: func() map[string]any {
			return finopsCostCenterSchema(false)
		},
	},
	http.MethodPost + " /cost-centers/{id}/mappings": {
		schema: finopsCostCenterMappingSchema,
	},
	http.MethodPost + " /model-rates": {
		schema: finopsModelRateSchema,
	},
	http.MethodPut + " /model-rates/{id}": {
		schema: finopsModelRateSchema,
	},
	http.MethodPost + " /outcomes": {
		schema: finopsOutcomeIngestSchema,
	},
	http.MethodPost + " /seats": {
		schema: finopsSeatIngestSchema,
	},
	http.MethodPost + " /statements/generate": {
		schema: finopsGenerateStatementsSchema,
	},
}

// finopsRequestBody returns the complete OpenAPI 3.1 requestBody for a known
// FinOps mutation. moduleRequestBody consults this registry before its shared
// sessions and eventing contracts.
func finopsRequestBody(r moduleRoute) (map[string]any, bool) {
	decl, ok := finopsRequestBodyDeclarationFor(r)
	if !ok || decl.kind != finopsBodyful {
		return nil, false
	}
	return oaObj(
		"required", true,
		"content", oaObj(
			"application/json", oaObj("schema", decl.schema()),
		),
	), true
}

// finopsRequestBodyDeclarationFor classifies every FinOps mutation whose body
// behavior is known at this seam. The four DELETE handlers below never read
// r.Body; keeping them in the producer makes that fact available to the central
// OpenAPI disposition adapter instead of leaving it only in a test fixture.
func finopsRequestBodyDeclarationFor(r moduleRoute) (finopsRequestBodyDeclaration, bool) {
	if r.ns != "finops" {
		return finopsRequestBodyDeclaration{}, false
	}
	key := r.method + " " + r.pattern
	if contract, ok := finopsOpenAPIContracts[key]; ok {
		return finopsRequestBodyDeclaration{kind: finopsBodyful, schema: contract.schema}, true
	}
	switch key {
	case http.MethodDelete + " /budgets/{id}",
		http.MethodDelete + " /cost-centers/{id}",
		http.MethodDelete + " /cost-centers/{id}/mappings/{mid}",
		http.MethodDelete + " /model-rates/{id}":
		return finopsRequestBodyDeclaration{kind: finopsBodyless}, true
	default:
		return finopsRequestBodyDeclaration{}, false
	}
}

func finopsObjectSchema(properties map[string]any, required ...string) map[string]any {
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

func finopsInt64Schema() map[string]any {
	return oaObj("type", "integer", "format", "int64")
}

func finopsOptionalRFC3339Schema() map[string]any {
	return oaObj("anyOf", []any{
		oaObj("type", "string", "const", ""),
		oaObj("type", "string", "format", "date-time"),
	})
}

func finopsCostIngestSchema() map[string]any {
	properties := oaObj(
		"provider_ref", oaObj("type", "string"),
		"model_ref", oaObj("type", "string"),
		"session_ref", oaObj("type", "string"),
		"input_tokens", finopsInt64Schema(),
		"output_tokens", finopsInt64Schema(),
		"cost_micro_usd", finopsInt64Schema(),
		"occurred_at", oaObj("type", "string", "format", "date-time"),
		"cache_read_tokens", finopsInt64Schema(),
		"cache_creation_1h_tokens", finopsInt64Schema(),
		"cache_creation_5m_tokens", finopsInt64Schema(),
		"workspace_ref", oaObj("type", "string"),
		"api_key_ref", oaObj("type", "string"),
		"actor", oaObj("type", "string"),
		"service_tier", oaObj("type", "string"),
		"context_window", oaObj("type", "string"),
		"inference_geo", oaObj("type", "string"),
		"gateway", oaObj("type", "string"),
		"provenance", oaObj("type", "string"),
		"cost_type", oaObj("type", "string"),
		"labels", oaObj(
			"type", "object",
			"additionalProperties", oaObj("type", "string"),
		),
	)
	schema := finopsObjectSchema(properties)
	schema["anyOf"] = []any{
		oaObj(
			"required", oaEnum("provider_ref"),
			"properties", oaObj("provider_ref", oaObj("minLength", 1)),
		),
		oaObj(
			"required", oaEnum("model_ref"),
			"properties", oaObj("model_ref", oaObj("minLength", 1)),
		),
	}
	return schema
}

func finopsBudgetSchema() map[string]any {
	nonGlobalDimensions := oaEnum(
		"model", "provider", "agent", "session", "team", "project",
		"workspace", "api_key", "actor", "service_tier", "context_window",
		"inference_geo", "gateway", "user_group", "agent_group", "identity",
		"cost_center",
	)
	properties := oaObj(
		"id", oaObj("type", "string"),
		"name", oaObj("type", "string", "minLength", 1),
		"enabled", oaObj("type", "boolean"),
		"dimension", oaObj(
			"type", "string",
			"enum", append(oaEnum("", "global"), nonGlobalDimensions...),
			"default", "global",
		),
		"key", oaObj("type", "string"),
		"limit_micro_usd", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"period", oaObj(
			"type", "string",
			"enum", oaEnum("", "daily", "weekly", "monthly", "total"),
			"default", "monthly",
		),
		"thresholds", oaObj(
			"type", "array",
			"items", oaObj("type", "number"),
			"default", []any{0.5, 0.8, 1.0},
		),
		"currency", oaObj("type", "string", "default", "USD"),
		"action", oaObj(
			"type", "string",
			"enum", oaEnum("", "alert", "throttle", "block"),
			"default", "alert",
		),
		"reserved_micro_usd", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"fail_closed", oaObj("type", "boolean"),
	)
	schema := finopsObjectSchema(properties, "name", "limit_micro_usd")
	schema["allOf"] = []any{
		oaObj(
			"if", oaObj(
				"required", oaEnum("dimension"),
				"properties", oaObj("dimension", oaObj("enum", nonGlobalDimensions)),
			),
			"then", oaObj(
				"required", oaEnum("key"),
				"properties", oaObj("key", oaObj("minLength", 1)),
			),
		),
	}
	return schema
}

func finopsCostCenterSchema(create bool) map[string]any {
	status := oaObj(
		"type", "string",
		"enum", oaEnum("", "active", "archived"),
	)
	if create {
		status["default"] = "active"
		status["description"] = "Lifecycle status; empty or omitted defaults to active."
	} else {
		status["description"] = "Lifecycle status; empty or omitted preserves the stored status."
	}
	return finopsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"code", oaObj("type", "string", "minLength", 1),
		"name", oaObj("type", "string", "minLength", 1),
		"description", oaObj("type", "string"),
		"owner", oaObj("type", "string"),
		"status", status,
		"metadata", oaObj(
			"type", "object",
			"additionalProperties", oaObj("type", "string"),
		),
		"created_at", oaObj("type", "string"),
		"updated_at", oaObj("type", "string"),
	), "code", "name")
}

func finopsCostCenterMappingSchema() map[string]any {
	return finopsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"cost_center_id", oaObj(
			"type", "string",
			"description", "Accepted by the DTO; the path cost-center id is authoritative and overwrites this value.",
		),
		"source_dimension", oaObj(
			"type", "string",
			"enum", oaEnum("team", "workspace", "project", "agent", "provider", "identity"),
		),
		"source_key", oaObj("type", "string", "minLength", 1),
		"priority", finopsInt64Schema(),
		"created_at", oaObj("type", "string"),
		"updated_at", oaObj("type", "string"),
	), "source_dimension", "source_key")
}

func finopsModelRateSchema() map[string]any {
	return finopsObjectSchema(oaObj(
		"id", oaObj("type", "string"),
		"provider", oaObj("type", "string", "minLength", 1),
		"model", oaObj("type", "string", "minLength", 1),
		"input_rate_micro_usd", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"output_rate_micro_usd", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"cache_read_rate_micro_usd", finopsInt64Schema(),
		"cache_creation_rate_micro_usd", finopsInt64Schema(),
		"effective_from", oaObj("type", "string", "format", "date-time"),
		"effective_until", finopsOptionalRFC3339Schema(),
		"notes", oaObj("type", "string"),
		"created_at", oaObj("type", "string"),
		"updated_at", oaObj("type", "string"),
	), "provider", "model", "input_rate_micro_usd", "output_rate_micro_usd", "effective_from")
}

func finopsGenerateStatementsSchema() map[string]any {
	return finopsObjectSchema(oaObj(
		"period", oaObj("type", "string", "enum", oaEnum("monthly", "weekly")),
		"period_start", oaObj("type", "string", "format", "date-time"),
	), "period", "period_start")
}

func finopsSeatIngestSchema() map[string]any {
	return finopsObjectSchema(oaObj(
		"provider", oaObj("type", "string", "minLength", 1),
		"day", oaObj("type", "string", "format", "date", "pattern", `^\d{4}-\d{2}-\d{2}$`),
		"assigned_seats", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"premium_seats", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"pending_invites", oaObj("type", "integer", "format", "int64", "minimum", 0),
	), "provider", "day")
}

func finopsOutcomeIngestSchema() map[string]any {
	properties := oaObj(
		"subject_kind", oaObj("type", "string", "enum", oaEnum("session", "agent", "identity")),
		"subject_ref", oaObj("type", "string", "minLength", 1),
		"outcome_ref", oaObj("type", "string"),
		"verdict", oaObj("type", "string", "minLength", 1),
		"value_micro_usd", oaObj("type", "integer", "format", "int64", "minimum", 0),
		"occurred_at", oaObj(
			"type", "string",
			"format", "date-time",
			"description", "Required when outcome_ref is absent; a zero time is rejected by the handler.",
		),
		"source", oaObj("type", "string"),
	)
	schema := finopsObjectSchema(properties, "subject_kind", "subject_ref", "verdict")
	schema["anyOf"] = []any{
		oaObj(
			"required", oaEnum("outcome_ref"),
			"properties", oaObj("outcome_ref", oaObj("minLength", 1)),
		),
		oaObj("required", oaEnum("occurred_at")),
	}
	return schema
}

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

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
	http.MethodPost + " /admission/reserve": {
		schema: finopsAdmissionReserveSchema,
	},
	http.MethodPost + " /admission/commit": {
		schema: finopsAdmissionCommitSchema,
	},
	http.MethodPost + " /admission/release": {
		schema: finopsAdmissionReleaseSchema,
	},
}

// finopsRequestBody returns the complete OpenAPI 3.1 requestBody for a known
// FinOps mutation; OperationDocumentation publishes it.
func finopsRequestBody(method, pattern string) (map[string]any, bool) {
	decl, ok := finopsRequestBodyDeclarationFor(method, pattern)
	if !ok || decl.kind != finopsBodyful {
		return nil, false
	}
	return oas.Obj(
		"required", true,
		"content", oas.Obj(
			"application/json", oas.Obj("schema", decl.schema()),
		),
	), true
}

// finopsRequestBodyDeclarationFor classifies every FinOps mutation whose body
// behavior is known at this seam. The four DELETE handlers and the admission
// reconciliation job below never read r.Body; keeping them in the producer makes
// that fact available to the central OpenAPI disposition adapter instead of
// leaving it only in a test fixture.
func finopsRequestBodyDeclarationFor(method, pattern string) (finopsRequestBodyDeclaration, bool) {
	key := method + " " + pattern
	if contract, ok := finopsOpenAPIContracts[key]; ok {
		return finopsRequestBodyDeclaration{kind: finopsBodyful, schema: contract.schema}, true
	}
	switch key {
	case http.MethodDelete + " /budgets/{id}",
		http.MethodDelete + " /cost-centers/{id}",
		http.MethodDelete + " /cost-centers/{id}/mappings/{mid}",
		http.MethodDelete + " /model-rates/{id}",
		// The job takes its whole subject from the authenticated tenant.
		http.MethodPost + " /admission/reconcile":
		return finopsRequestBodyDeclaration{kind: finopsBodyless}, true
	default:
		return finopsRequestBodyDeclaration{}, false
	}
}

func finopsObjectSchema(properties map[string]any, required ...string) map[string]any {
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

func finopsInt64Schema() map[string]any {
	return oas.Obj("type", "integer", "format", "int64")
}

func finopsOptionalRFC3339Schema() map[string]any {
	return oas.Obj("anyOf", []any{
		oas.Obj("type", "string", "const", ""),
		oas.Obj("type", "string", "format", "date-time"),
	})
}

func finopsCostIngestSchema() map[string]any {
	properties := oas.Obj(
		"provider_ref", oas.Obj("type", "string"),
		"model_ref", oas.Obj("type", "string"),
		"session_ref", oas.Obj("type", "string"),
		"input_tokens", finopsInt64Schema(),
		"output_tokens", finopsInt64Schema(),
		"cost_micro_usd", finopsInt64Schema(),
		"occurred_at", oas.Obj("type", "string", "format", "date-time"),
		"cache_read_tokens", finopsInt64Schema(),
		"cache_creation_1h_tokens", finopsInt64Schema(),
		"cache_creation_5m_tokens", finopsInt64Schema(),
		"workspace_ref", oas.Obj("type", "string"),
		"api_key_ref", oas.Obj("type", "string"),
		"actor", oas.Obj("type", "string"),
		"service_tier", oas.Obj("type", "string"),
		"context_window", oas.Obj("type", "string"),
		"inference_geo", oas.Obj("type", "string"),
		"gateway", oas.Obj("type", "string"),
		"provenance", oas.Obj("type", "string"),
		"cost_type", oas.Obj("type", "string"),
		"labels", oas.Obj(
			"type", "object",
			"additionalProperties", oas.Obj("type", "string"),
		),
	)
	schema := finopsObjectSchema(properties)
	schema["anyOf"] = []any{
		oas.Obj(
			"required", oas.Enum("provider_ref"),
			"properties", oas.Obj("provider_ref", oas.Obj("minLength", 1)),
		),
		oas.Obj(
			"required", oas.Enum("model_ref"),
			"properties", oas.Obj("model_ref", oas.Obj("minLength", 1)),
		),
	}
	return schema
}

func finopsBudgetSchema() map[string]any {
	nonGlobalDimensions := oas.Enum(
		"model", "provider", "agent", "session", "team", "project",
		"workspace", "api_key", "actor", "service_tier", "context_window",
		"inference_geo", "gateway", "user_group", "agent_group", "identity",
		"cost_center",
	)
	properties := oas.Obj(
		"id", oas.Obj("type", "string"),
		"name", oas.Obj("type", "string", "minLength", 1),
		"enabled", oas.Obj("type", "boolean"),
		"dimension", oas.Obj(
			"type", "string",
			"enum", append(oas.Enum("", "global"), nonGlobalDimensions...),
			"default", "global",
		),
		"key", oas.Obj("type", "string"),
		"limit_micro_usd", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"period", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "daily", "weekly", "monthly", "total"),
			"default", "monthly",
		),
		"thresholds", oas.Obj(
			"type", "array",
			"items", oas.Obj("type", "number"),
			"default", []any{0.5, 0.8, 1.0},
		),
		"currency", oas.Obj("type", "string", "default", "USD"),
		"action", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "alert", "throttle", "block"),
			"default", "alert",
		),
		"reserved_micro_usd", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"fail_closed", oas.Obj("type", "boolean"),
	)
	schema := finopsObjectSchema(properties, "name", "limit_micro_usd")
	schema["allOf"] = []any{
		oas.Obj(
			"if", oas.Obj(
				"required", oas.Enum("dimension"),
				"properties", oas.Obj("dimension", oas.Obj("enum", nonGlobalDimensions)),
			),
			"then", oas.Obj(
				"required", oas.Enum("key"),
				"properties", oas.Obj("key", oas.Obj("minLength", 1)),
			),
		),
	}
	return schema
}

func finopsCostCenterSchema(create bool) map[string]any {
	status := oas.Obj(
		"type", "string",
		"enum", oas.Enum("", "active", "archived"),
	)
	if create {
		status["default"] = "active"
		status["description"] = "Lifecycle status; empty or omitted defaults to active."
	} else {
		status["description"] = "Lifecycle status; empty or omitted preserves the stored status."
	}
	return finopsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"code", oas.Obj("type", "string", "minLength", 1),
		"name", oas.Obj("type", "string", "minLength", 1),
		"description", oas.Obj("type", "string"),
		"owner", oas.Obj("type", "string"),
		"status", status,
		"metadata", oas.Obj(
			"type", "object",
			"additionalProperties", oas.Obj("type", "string"),
		),
		"created_at", oas.Obj("type", "string"),
		"updated_at", oas.Obj("type", "string"),
	), "code", "name")
}

func finopsCostCenterMappingSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"cost_center_id", oas.Obj(
			"type", "string",
			"description", "Accepted by the DTO; the path cost-center id is authoritative and overwrites this value.",
		),
		"source_dimension", oas.Obj(
			"type", "string",
			"enum", oas.Enum("team", "workspace", "project", "agent", "provider", "identity"),
		),
		"source_key", oas.Obj("type", "string", "minLength", 1),
		"priority", finopsInt64Schema(),
		"created_at", oas.Obj("type", "string"),
		"updated_at", oas.Obj("type", "string"),
	), "source_dimension", "source_key")
}

func finopsModelRateSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"id", oas.Obj("type", "string"),
		"provider", oas.Obj("type", "string", "minLength", 1),
		"model", oas.Obj("type", "string", "minLength", 1),
		"input_rate_micro_usd", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"output_rate_micro_usd", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"cache_read_rate_micro_usd", finopsInt64Schema(),
		"cache_creation_rate_micro_usd", finopsInt64Schema(),
		"effective_from", oas.Obj("type", "string", "format", "date-time"),
		"effective_until", finopsOptionalRFC3339Schema(),
		"notes", oas.Obj("type", "string"),
		"created_at", oas.Obj("type", "string"),
		"updated_at", oas.Obj("type", "string"),
	), "provider", "model", "input_rate_micro_usd", "output_rate_micro_usd", "effective_from")
}

func finopsGenerateStatementsSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"period", oas.Obj("type", "string", "enum", oas.Enum("monthly", "weekly")),
		"period_start", oas.Obj("type", "string", "format", "date-time"),
	), "period", "period_start")
}

func finopsSeatIngestSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"provider", oas.Obj("type", "string", "minLength", 1),
		"day", oas.Obj("type", "string", "format", "date", "pattern", `^\d{4}-\d{2}-\d{2}$`),
		"assigned_seats", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"premium_seats", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"pending_invites", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
	), "provider", "day")
}

func finopsOutcomeIngestSchema() map[string]any {
	properties := oas.Obj(
		"subject_kind", oas.Obj("type", "string", "enum", oas.Enum("session", "agent", "identity")),
		"subject_ref", oas.Obj("type", "string", "minLength", 1),
		"outcome_ref", oas.Obj("type", "string"),
		"verdict", oas.Obj("type", "string", "minLength", 1),
		"value_micro_usd", oas.Obj("type", "integer", "format", "int64", "minimum", 0),
		"occurred_at", oas.Obj(
			"type", "string",
			"format", "date-time",
			"description", "Required when outcome_ref is absent; a zero time is rejected by the handler.",
		),
		"source", oas.Obj("type", "string"),
	)
	schema := finopsObjectSchema(properties, "subject_kind", "subject_ref", "verdict")
	schema["anyOf"] = []any{
		oas.Obj(
			"required", oas.Enum("outcome_ref"),
			"properties", oas.Obj("outcome_ref", oas.Obj("minLength", 1)),
		),
		oas.Obj("required", oas.Enum("occurred_at")),
	}
	return schema
}

// finopsAdmissionScopeSchema is the closed set Reserve accepts; any other scope is
// refused with 400 and holds nothing.
func finopsAdmissionScopeSchema() map[string]any {
	return oas.Obj(
		"type", "string",
		"enum", oas.Enum("session_launch", "model_gateway", "scheduled_job"),
	)
}

// finopsSpendDimsSchema projects finops.SpendDims, the provider-neutral attribution
// an enforcing budget matches on. Every member is optional: a request that names
// none is still held against the global budgets of the tenant.
func finopsSpendDimsSchema() map[string]any {
	properties := finopsStrings(
		"provider_ref", "model_ref", "agent_ref", "session_ref", "team", "project",
		"workspace_ref", "api_key_ref", "service_tier", "context_window",
		"inference_geo", "gateway", "cost_type", "identity_ref", "routine_ref",
		"cost_center_ref",
	)
	properties["user_group_refs"] = oas.Obj("type", "array", "items", oas.Obj("type", "string"))
	properties["agent_group_refs"] = oas.Obj("type", "array", "items", oas.Obj("type", "string"))
	return finopsObjectSchema(properties)
}

func finopsAdmissionReserveSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"scope", finopsAdmissionScopeSchema(),
		"dims", finopsSpendDimsSchema(),
		"actor_ref", oas.Obj(
			"type", "string",
			"description", "When set, the spend limits of that actor are held as well as the budgets.",
		),
		"groups", oas.Obj(
			"type", "array",
			"items", oas.Obj("type", "string"),
			"description", "Directory group ids used to resolve the actor's spend limits.",
		),
		"estimate_micro_usd", oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0,
			"description", "The amount to hold. Zero holds nothing and returns no handle, but a cap already past its limit still refuses it.",
		),
		"idempotency_key", oas.Obj(
			"type", "string", "minLength", 1, "maxLength", 256,
			"description", "Bound to the tenant, the scope and the payload. A retry with the same key and payload inside the replay window is answered with the hold the first call took; the same key with another payload is refused with 409.",
		),
		"unreachable", oas.Obj(
			"type", "string",
			"enum", oas.Enum("", "deny", "allow"),
			"default", "deny",
			"description", "What a request answers when its admission cannot be established. Omitted, empty and unknown values mean deny. Allow admits with no hold.",
		),
	), "scope", "idempotency_key")
}

// finopsAdmissionCommitSchema and its release sibling carry the one handle Reserve
// answers with, and publish no required list: Commit and Release answer an absent or
// empty handle as a no-op, because an admission that held nothing has no hold to
// settle.
func finopsAdmissionCommitSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"handle", oas.Obj(
			"type", "string",
			"description", "The handle Reserve answered with. An empty handle settles nothing.",
		),
		"actual_micro_usd", oas.Obj(
			"type", "integer", "format", "int64", "minimum", 0,
			"description", "The measured cost. Ingest the spend first, so the ceiling never under-counts during settlement. A repeat with the same amount is answered as the first commit was; another amount after a commit is refused with 409.",
		),
	))
}

func finopsAdmissionReleaseSchema() map[string]any {
	return finopsObjectSchema(oas.Obj(
		"handle", oas.Obj(
			"type", "string",
			"description", "The handle Reserve answered with. An empty handle releases nothing, and a committed hold stays committed.",
		),
	))
}

// These are response DTOs, not mutation inputs. Decimal money is a STRING: neither
// an int64 projection nor a JSON number can represent every established sum.
func finopsEvidenceReadRoute(method, pattern string) bool {
	return method == http.MethodGet && (pattern == "/alerts" || pattern == "/budgets/{id}/status")
}
func finopsEvidenceResponse(method, pattern string) map[string]any {
	schema := finopsBudgetStatusSchema()
	if pattern == "/alerts" {
		schema = finopsObjectSchema(oas.Obj("items", oas.Obj("type", "array", "items", finopsAlertSchema()), "cursor", oas.Obj("type", "string"), "has_more", oas.Obj("type", "boolean")), "items", "has_more")
	}
	return oas.Obj("description", "Tenant-scoped financial evidence; legacy numeric fields are classified separately. Forecast is not certified.", "content", oas.Obj("application/json", oas.Obj("schema", schema)))
}
func finopsAlertParameters() []map[string]any {
	return []map[string]any{
		oas.Param("alert_id", "query", "Optional UUID validated by the handler (400 if malformed). A reference grants no access; an authorized tenant with no matching row receives an empty list.", false, oas.Obj("type", "string", "format", "uuid")),
		oas.Param("budget_id", "query", "Optional budget filter, combined with alert_id.", false, oas.Obj("type", "string")),
		oas.Param("limit", "query", "Page size; default 100, store maximum 1000.", false, oas.Obj("type", "integer")),
		oas.Param("cursor", "query", "Opaque continuation cursor from the preceding page.", false, oas.Obj("type", "string")),
	}
}
func finopsStrings(names ...string) map[string]any {
	p := map[string]any{}
	for _, n := range names {
		p[n] = oas.Obj("type", "string")
	}
	return p
}
func finopsDecimal(nullable bool) map[string]any {
	p := oas.Obj("type", "string", "pattern", "^(0|-?[1-9][0-9]*)$", "description", "Canonical integer micro-USD, without floating-point conversion.")
	if nullable {
		p["type"] = oas.Enum("string", "null")
	}
	return p
}
func finopsCauses() map[string]any {
	return oas.Obj("type", "array", "items", oas.Obj("type", "string"))
}
func finopsComponentSchema() map[string]any {
	return finopsObjectSchema(oas.Obj("state", oas.Obj("type", "string", "enum", oas.Enum("known", "unknown", "nonnegative_unknown", "indeterminate")), "value_micro_usd", finopsDecimal(true), "causes", finopsCauses(), "rows_read", oas.Obj("type", "integer"), "pages_read", oas.Obj("type", "integer")), "state", "value_micro_usd")
}
func finopsComponentsSchema() map[string]any {
	return finopsObjectSchema(oas.Obj("cost", finopsComponentSchema(), "static_reservation", finopsComponentSchema(), "dynamic_reservation", finopsComponentSchema()), "cost", "static_reservation", "dynamic_reservation")
}
func finopsCrossingSchema() map[string]any {
	return oas.Obj("type", "string", "enum", oas.Enum("proven", "not_reached", "unproven"))
}
func finopsThresholdSchema() map[string]any {
	p := finopsStrings("threshold", "target_micro_usd") // the target may be a terminating rational decimal
	p["result"] = finopsCrossingSchema()
	p["legacy_threshold_pct"] = oas.Obj("type", "integer")
	p["causes"] = finopsCauses()
	return finopsObjectSchema(p, "threshold", "result")
}
func finopsEnvelopeSchema() map[string]any {
	policy := finopsStrings("id", "name", "dimension", "key", "period", "currency", "action", "reserved_micro_usd", "config_fault")
	policy["version"] = finopsInt64Schema()
	policy["limit_micro_usd"] = finopsDecimal(false)
	amount := finopsObjectSchema(oas.Obj("class", oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unknown")), "value_micro_usd", finopsDecimal(true), "currency", oas.Obj("type", "string"), "causes", finopsCauses()), "class", "value_micro_usd", "currency")
	decision := finopsStrings("threshold", "target_micro_usd", "target_numerator", "target_denominator")
	decision["result"] = finopsCrossingSchema()
	decision["legacy_threshold_pct"] = oas.Obj("type", "integer")
	decision["causes"] = finopsCauses()
	ctx := finopsStrings("window_start", "window_end", "window_bounds", "provenance_filter", "scope_column", "scope_value", "evaluated_at", "sample_occurred_at", "read_consistency")
	ctx["scope_resolved"] = oas.Obj("type", "boolean")
	ctx["scope_values"] = oas.Obj("type", "array", "items", oas.Obj("type", "string"), "minItems", 2, "uniqueItems", true)
	p := finopsStrings("alert_id", "tenant_id", "budget_id")
	p["schema_version"] = oas.Obj("type", "integer", "const", 1)
	p["digest_version"] = oas.Obj("type", "integer", "const", 1)
	p["policy"] = finopsObjectSchema(policy, "id", "version", "name", "dimension", "key", "period", "currency", "action", "limit_micro_usd")
	p["amount"] = amount
	p["components"] = finopsComponentsSchema()
	p["decision"] = finopsObjectSchema(decision, "result", "threshold", "target_numerator", "target_denominator", "legacy_threshold_pct")
	p["context"] = finopsObjectSchema(ctx, "window_bounds", "provenance_filter", "scope_resolved", "evaluated_at", "sample_occurred_at", "read_consistency")
	p["legacy"] = finopsObjectSchema(oas.Obj("value_kind", oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unavailable")), "spend_micro_usd", finopsInt64Schema(), "note", oas.Obj("type", "string")), "value_kind", "spend_micro_usd")
	return finopsObjectSchema(p, "schema_version", "digest_version", "alert_id", "tenant_id", "budget_id", "policy", "amount", "components", "decision", "context", "legacy")
}
func finopsAlertSchema() map[string]any {
	p := finopsStrings("id", "budget_id", "dimension", "key", "period", "period_start", "severity", "triggered_at")
	for _, n := range []string{"threshold_pct", "spend_micro_usd", "limit_micro_usd"} {
		p[n] = finopsInt64Schema()
	}
	p["legacy_value_kind"] = oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unavailable", "unverified"))
	p["amount_evidence"] = finopsObjectSchema(oas.Obj("state", oas.Obj("type", "string", "enum", oas.Enum("valid", "unknown")), "cause", oas.Obj("type", "string"), "evidence_hash", oas.Obj("type", "string", "description", "Recorded digest; may be invalid when state=unknown. A digest alone establishes no financial claim."), "envelope", finopsEnvelopeSchema()), "state")
	return finopsObjectSchema(p, "id", "budget_id", "dimension", "period", "period_start", "threshold_pct", "spend_micro_usd", "limit_micro_usd", "severity", "triggered_at", "legacy_value_kind", "amount_evidence")
}
func finopsBudgetStatusSchema() map[string]any {
	p := finopsStrings("id", "name", "dimension", "key", "period", "period_start", "currency", "action", "exhaustion_confidence")
	for _, n := range []string{"limit_micro_usd", "reserved_micro_usd", "spend_micro_usd", "remaining_micro_usd", "projected_micro_usd"} {
		p[n] = finopsInt64Schema()
	}
	for _, n := range []string{"consumed_pct", "projected_pct", "samples", "exhaustion_days_remaining"} {
		p[n] = oas.Obj("type", "integer")
	}
	for _, n := range []string{"enabled", "over", "truncated"} {
		p[n] = oas.Obj("type", "boolean")
	}
	fields := finopsStrings("spend_micro_usd", "remaining_micro_usd", "projected_micro_usd")
	for n := range fields {
		fields[n] = oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unavailable"))
	}
	fields["over"] = finopsCrossingSchema()
	p["amount"] = finopsObjectSchema(oas.Obj("state", oas.Obj("type", "string", "enum", oas.Enum("complete", "incomplete")), "class", oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unknown")), "effective_micro_usd", finopsDecimal(true), "remaining_micro_usd", finopsDecimal(true), "currency", oas.Obj("type", "string"), "causes", finopsCauses(), "components", finopsComponentsSchema(), "thresholds", oas.Obj("type", oas.Enum("array", "null"), "items", finopsThresholdSchema()), "over_limit", finopsThresholdSchema(), "legacy_projection", oas.Obj("type", "string", "enum", oas.Enum("exact", "lower_bound", "unavailable")), "legacy_fields", finopsObjectSchema(fields, "spend_micro_usd", "remaining_micro_usd", "projected_micro_usd", "over"), "forecast_certified", oas.Obj("type", "boolean", "const", false)), "state", "class", "effective_micro_usd", "remaining_micro_usd", "currency", "components", "thresholds", "over_limit", "legacy_projection", "legacy_fields", "forecast_certified")
	return finopsObjectSchema(p, "id", "name", "enabled", "dimension", "period", "currency", "action", "limit_micro_usd", "spend_micro_usd", "remaining_micro_usd", "consumed_pct", "projected_micro_usd", "projected_pct", "over", "samples", "exhaustion_days_remaining")
}

// finopsCSVExport reports the two exports that always answer CSV on success.
//
// ⛔ THEY ARE MATCHED BY EXACT TUPLE, method AND whole pattern, NEVER by the
// trailing "export" segment. Fifteen module routes end in "export" and only these
// two are always CSV; the rest answer the JSON envelope unless a ?format query
// asks otherwise. A suffix rule would fix this defect by committing it thirteen
// more times, in the opposite direction, and the generated clients would decode
// CSV where the server sends JSON. TestModuleRouteRawContentTypeIsExactNotBySuffix
// pins both halves.
func finopsCSVExport(method, pattern string) bool {
	if method != http.MethodGet {
		return false
	}
	switch pattern {
	case "/spend/export":
		// focus.go: the FOCUS export is always CSV on 200.
		return true
	case "/statements/{id}/export":
		// statements.go handleExportStatement: the ONLY success path sets
		// "text/csv; charset=utf-8" and writes rows with encoding/csv. The charset
		// parameter belongs to the HTTP header; "text/csv" is the media-type key the
		// document publishes. Every error branch stays writeJSON.
		return true
	}
	return false
}

// OperationDocumentation publishes the request body each mutation's handler
// decodes, the typed evidence reads and the CSV exports. A route this module
// has not classified stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if finopsCSVExport(method, pattern) {
		responses := api.GenericModuleResponses()
		responses["200"] = oas.Obj("description", "OK (text/csv)",
			"content", oas.Obj("text/csv", oas.Obj("schema", oas.Obj("type", "string"))))
		return api.ModuleOperationDocumentation{Responses: responses}, true
	}
	if finopsEvidenceReadRoute(method, pattern) {
		responses := api.GenericModuleResponses()
		responses["200"] = finopsEvidenceResponse(method, pattern)
		responses["500"] = oas.JSONResp("store or evaluation failure")
		doc := api.ModuleOperationDocumentation{Responses: responses}
		if pattern == "/alerts" {
			doc.TrailingParameters = finopsAlertParameters()
		}
		return doc, true
	}
	decl, ok := finopsRequestBodyDeclarationFor(method, pattern)
	if !ok {
		return api.ModuleOperationDocumentation{}, false
	}
	var doc api.ModuleOperationDocumentation
	switch decl.kind {
	case finopsBodyful:
		doc.BodyKind = api.ModuleOperationJSONBody
		doc.RequestBody, _ = finopsRequestBody(method, pattern)
	case finopsBodyless:
		doc.BodyKind = api.ModuleOperationBodyless
	}
	return doc, true
}

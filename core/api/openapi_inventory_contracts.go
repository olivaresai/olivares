// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// inventoryCollectionsContract describes the existing handler's selection and
// DTOs (modules/inventory/coverage_read.go). It changes no roster authority.
func inventoryCollectionsContract(r moduleRoute, operation map[string]any) {
	if r.ns != "inventory" || r.method != http.MethodGet || r.pattern != "/collections" {
		return
	}
	operation["parameters"] = []any{
		oaTenantParam(),
		oaParam("source_id", "query", "Source ID from the caller's CURRENT opened registration; nonempty, at most 128 bytes. An old selector reads that historical selection, not the current roster.", true, oaObj("type", "string", "minLength", 1)),
		oaParam("source_revision", "query", "Positive signed 64-bit revision from the same CURRENT opened registration; missing or invalid selectors return 400.", true, oaObj("type", "integer", "format", "int64", "minimum", 1)),
		oaParam("environment_ref", "query", "Environment from the same CURRENT opened registration; nonempty, at most 128 bytes. Unmatched selection returns unknown coverage.", true, oaObj("type", "string", "minLength", 1)),
	}
	responses := operation["responses"].(map[string]any)
	responses["200"] = oaObj("description", "Collection evidence for the caller-selected CURRENT opened registration. An unmatched selection returns unknown, never inherited completeness. Current enumeration and projection are separate from historical last_qualified_success for the exact scope. Complete describes this query over its observed interval, not a global snapshot or provider authorization. Empty completion does not delete resources.",
		"content", oaObj("application/json", oaObj("schema", inventoryCollectionPageSchema())))
	responses["423"] = oaJSONResp("tenant service suspended or not in service")
	responses["500"] = oaJSONResp("stored collection evidence or store read failed; the generic error body contains no backend detail")
}

func inventoryCollectionObject(properties map[string]any, required ...string) map[string]any {
	return oaObj("type", "object", "additionalProperties", false, "properties", properties, "required", oaEnum(required...))
}

func inventoryCollectionPageSchema() map[string]any {
	item := inventoryCollectionObject(oaObj(
		"current", inventoryCollectionCurrentSchema(),
		"last_qualified_success", inventoryCollectionSuccessSchema(),
	), "current")
	return inventoryCollectionObject(oaObj(
		"items", oaObj("type", "array", "minItems", 1, "maxItems", 1, "items", item),
		"cursor", oaObj("type", "string", "description", "Optional list-envelope continuation cursor; this exact source selection has at most one head."),
		"has_more", oaObj("type", "boolean", "description", "List-envelope continuation flag, not evidence of collection completeness."),
	), "items", "has_more")
}

func inventoryCollectionCount() map[string]any {
	return oaObj("type", "integer", "format", "int64", "minimum", 0, "maximum", 100000)
}
func inventoryCollectionInstant(description string) map[string]any {
	return oaObj("type", "string", "format", "date-time", "description", description)
}
func inventoryCollectionFingerprint() map[string]any {
	return oaObj("type", "string", "pattern", "^[0-9a-f]{64}$", "description", "SHA-256 fingerprint of the versioned query scope.")
}

// Shared identity fields are copied into fresh maps; builders do not share
// mutable schemas. Raw selectors, binding references and receipt facts stay out.
func inventoryCollectionIdentity() map[string]any {
	return oaObj(
		"run_id", oaObj("type", "string", "description", "Host-owned collection run ID."),
		"source_id", oaObj("type", "string", "minLength", 1),
		"source_revision", oaObj("type", "integer", "format", "int64", "minimum", 1),
		"environment_ref", oaObj("type", "string", "minLength", 1),
		"scope_contract", oaObj("type", "string", "enum", oaEnum("azure-resource-graph/2022-10-01/id-v1")),
		"family", oaObj("type", "string", "enum", oaEnum("azure.resource")),
		"requested_scope", inventoryCollectionFingerprint(),
		"fulfilled_scope", inventoryCollectionFingerprint(),
		"expected_count", inventoryCollectionCount(),
		"host_started_at", inventoryCollectionInstant("Host run start, separate from producer observation time."),
		"host_finished_at", inventoryCollectionInstant("Host closure after Gather returns."),
		"producer_started_at", inventoryCollectionInstant("Producer-declared enumeration start."),
		"producer_finished_at", inventoryCollectionInstant("Producer-declared enumeration end."),
		"qualified_at", inventoryCollectionInstant("Inventory qualification after all expected members commit."),
	)
}

func inventoryCollectionCurrentSchema() map[string]any {
	properties := inventoryCollectionIdentity()
	properties["run_id"] = oaObj("type", "string", "description", "Host run ID; empty when the selected registration has no run.")
	properties["host_started_at"] = oaObj("type", "string", "description", "Host start as an RFC3339 timestamp; empty when the selected registration has no run.")
	properties["run_order"] = oaObj("type", "integer", "format", "int64", "minimum", 0, "description", "Durable source order; zero when no run exists.")
	properties["coverage"] = oaObj("type", "string", "enum", oaEnum("complete", "partial", "unavailable", "unsupported", "unknown"))
	properties["reason"] = oaObj("type", "string", "enum", oaEnum("", "exhausted", "page_limit", "repeated_cursor", "invalid_response", "scope_unproven", "scope_mismatch", "provider_error", "offline", "disabled", "member_limit", "missing_report", "protocol_error", "gather_error", "canceled", "sink_error", "persistence_rejected", "commit_outcome_unknown", "persistence_canceled", "persistence_unavailable", "persistence_error"))
	properties["projection"] = oaObj("type", "string", "enum", oaEnum("pending", "committed", "failed"), "description", "Queued members are pending until their receipt/materialization commits; enumeration alone cannot qualify them.")
	properties["admitted_count"] = inventoryCollectionCount()
	properties["committed_count"] = inventoryCollectionCount()
	properties["rejection_reason"] = oaObj("type", "string", "enum", oaEnum("terminal_conflict", "linkage_conflict", "member_conflict", "receipt_conflict"))
	return inventoryCollectionObject(properties, "run_id", "source_id", "source_revision", "environment_ref", "run_order", "coverage", "reason", "projection", "admitted_count", "committed_count", "expected_count", "host_started_at")
}

func inventoryCollectionSuccessSchema() map[string]any {
	schema := inventoryCollectionObject(inventoryCollectionIdentity(), "run_id", "source_id", "source_revision", "environment_ref", "scope_contract", "family", "requested_scope", "fulfilled_scope", "expected_count", "qualified_at", "host_started_at", "host_finished_at", "producer_started_at", "producer_finished_at")
	schema["description"] = "Immutable historical qualification for this exact registration and scope; absent until one run qualifies. Later rejection evidence does not rewrite this receipt."
	return schema
}

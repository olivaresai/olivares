// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// inventoryCollectionsDocumentation describes the existing handler's selection
// and DTOs (coverage_read.go). It changes no roster authority.
func inventoryCollectionsDocumentation() api.ModuleOperationDocumentation {
	responses := api.GenericModuleResponses()
	responses["200"] = oas.Obj("description", "Collection evidence for the caller-selected CURRENT opened registration. An unmatched selection returns unknown, never inherited completeness. Current enumeration and projection are separate from historical last_qualified_success for the exact scope. Complete describes this query over its observed interval, not a global snapshot or provider authorization. Empty completion does not delete resources.",
		"content", oas.Obj("application/json", oas.Obj("schema", inventoryCollectionPageSchema())))
	responses["423"] = oas.JSONResp("tenant service suspended or not in service")
	responses["500"] = oas.JSONResp("stored collection evidence or store read failed; the generic error body contains no backend detail")
	return api.ModuleOperationDocumentation{
		Responses: responses,
		TrailingParameters: []map[string]any{
			oas.Param("source_id", "query", "Source ID from the caller's CURRENT opened registration; nonempty, at most 128 bytes. An old selector reads that historical selection, not the current roster.", true, oas.Obj("type", "string", "minLength", 1)),
			oas.Param("source_revision", "query", "Positive signed 64-bit revision from the same CURRENT opened registration; missing or invalid selectors return 400.", true, oas.Obj("type", "integer", "format", "int64", "minimum", 1)),
			oas.Param("environment_ref", "query", "Environment from the same CURRENT opened registration; nonempty, at most 128 bytes. Unmatched selection returns unknown coverage.", true, oas.Obj("type", "string", "minLength", 1)),
		},
	}
}

func inventoryCollectionObject(properties map[string]any, required ...string) map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false, "properties", properties, "required", oas.Enum(required...))
}

func inventoryCollectionPageSchema() map[string]any {
	item := inventoryCollectionObject(oas.Obj(
		"current", inventoryCollectionCurrentSchema(),
		"last_qualified_success", inventoryCollectionSuccessSchema(),
	), "current")
	return inventoryCollectionObject(oas.Obj(
		"items", oas.Obj("type", "array", "minItems", 1, "maxItems", 1, "items", item),
		"cursor", oas.Obj("type", "string", "description", "Optional list-envelope continuation cursor; this exact source selection has at most one head."),
		"has_more", oas.Obj("type", "boolean", "description", "List-envelope continuation flag, not evidence of collection completeness."),
	), "items", "has_more")
}

func inventoryCollectionCount() map[string]any {
	return oas.Obj("type", "integer", "format", "int64", "minimum", 0, "maximum", 100000)
}
func inventoryCollectionInstant(description string) map[string]any {
	return oas.Obj("type", "string", "format", "date-time", "description", description)
}
func inventoryCollectionFingerprint() map[string]any {
	return oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$", "description", "SHA-256 fingerprint of the versioned query scope.")
}

// Shared identity fields are copied into fresh maps; builders do not share
// mutable schemas. Raw selectors, binding references and receipt facts stay out.
func inventoryCollectionIdentity() map[string]any {
	return oas.Obj(
		"run_id", oas.Obj("type", "string", "description", "Host-owned collection run ID."),
		"source_id", oas.Obj("type", "string", "minLength", 1),
		"source_revision", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
		"environment_ref", oas.Obj("type", "string", "minLength", 1),
		"scope_contract", oas.Obj("type", "string", "enum", oas.Enum("azure-resource-graph/2022-10-01/id-v1")),
		"family", oas.Obj("type", "string", "enum", oas.Enum("azure.resource")),
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
	properties["run_id"] = oas.Obj("type", "string", "description", "Host run ID; empty when the selected registration has no run.")
	properties["host_started_at"] = oas.Obj("type", "string", "description", "Host start as an RFC3339 timestamp; empty when the selected registration has no run.")
	properties["run_order"] = oas.Obj("type", "integer", "format", "int64", "minimum", 0, "description", "Durable source order; zero when no run exists.")
	properties["coverage"] = oas.Obj("type", "string", "enum", oas.Enum("complete", "partial", "unavailable", "unsupported", "unknown"))
	properties["reason"] = oas.Obj("type", "string", "enum", oas.Enum("", "exhausted", "page_limit", "repeated_cursor", "invalid_response", "scope_unproven", "scope_mismatch", "provider_error", "offline", "disabled", "member_limit", "missing_report", "protocol_error", "gather_error", "canceled", "sink_error", "persistence_rejected", "commit_outcome_unknown", "persistence_canceled", "persistence_unavailable", "persistence_error"))
	properties["projection"] = oas.Obj("type", "string", "enum", oas.Enum("pending", "committed", "failed"), "description", "Queued members are pending until their receipt/materialization commits; enumeration alone cannot qualify them.")
	properties["admitted_count"] = inventoryCollectionCount()
	properties["committed_count"] = inventoryCollectionCount()
	properties["rejection_reason"] = oas.Obj("type", "string", "enum", oas.Enum("terminal_conflict", "linkage_conflict", "member_conflict", "receipt_conflict"))
	return inventoryCollectionObject(properties, "run_id", "source_id", "source_revision", "environment_ref", "run_order", "coverage", "reason", "projection", "admitted_count", "committed_count", "expected_count", "host_started_at")
}

func inventoryCollectionSuccessSchema() map[string]any {
	schema := inventoryCollectionObject(inventoryCollectionIdentity(), "run_id", "source_id", "source_revision", "environment_ref", "scope_contract", "family", "requested_scope", "fulfilled_scope", "expected_count", "qualified_at", "host_started_at", "host_finished_at", "producer_started_at", "producer_finished_at")
	schema["description"] = "Immutable historical qualification for this exact registration and scope; absent until one run qualifies. Later rejection evidence does not rewrite this receipt."
	return schema
}

// --- inventory: the entity observation history ---------------------------------
//
// GET /v1/m/inventory/entities/{kind}/{id}/observations is the first inventory
// route published with a CLOSED contract instead of the generic envelope. The
// handler (modules/inventory/api.go handleListEntityObservations) and its reader
// (modules/inventory/provenance_read.go) already enforce every shape below; what
// was missing was the document saying so, and an independent review measured the
// cost: the beta document carried the route with no limit or cursor parameter, a
// 200 of {type: object} and no 500, so the generated web client typed the query
// as `never` and the page as `Record<string, never>`. A doc comment on the
// handler is not a contract a generator can consume.
//
// The bounds here are the reader's own observationPageDefault and
// observationPageMax; the descriptions state them in words, and
// openapi_test.go pins both: a change to the reader's bound is a change to
// those words, in the same commit.

// inventoryObservationHistoryRoute recognizes EXACTLY the observation history
// route — method and the whole pattern, never a prefix. The catalog list
// (/entities) and the detail route (/entities/{kind}/{id}) keep the generic
// envelope.
func inventoryObservationHistoryRoute(method, pattern string) bool {
	return method == http.MethodGet && pattern == "/entities/{kind}/{id}/observations"
}

// inventoryCanonicalIDPattern is the exact text the inventory reader accepts for
// an entity id, a receipt id and the page cursor: the lowercase hyphenated form
// the store writes (model.ID.String). Braces, upper case, the URN prefix and the
// 32-hex form parse elsewhere but are refused there, as request data (400) and
// as stored data (evidence corruption). The all-zero value matches this pattern
// and is refused too: OpenAPI has no exclusion keyword every generator honors,
// so that half is stated in the descriptions.
const inventoryCanonicalIDPattern = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"

func inventoryCanonicalIDSchema(description string) map[string]any {
	return oas.Obj("type", "string", "format", "uuid", "pattern", inventoryCanonicalIDPattern,
		"description", description)
}

// inventoryObservationParameters is the page request the handler parses
// (parseObservationQuery): each parameter at most once, limit in canonical decimal
// within 1..25 (25 when absent), cursor a canonical nonzero receipt id. OpenAPI
// cannot say "at most once" about a query parameter, so the 400 for a repeated
// value is stated in the descriptions rather than left to discovery.
func inventoryObservationParameters() []map[string]any {
	return []map[string]any{
		oas.Param("limit", "query",
			"Page size in distinct receipts, 1 through 25; 25 when absent. Canonical decimal only and at most once: a repeated, empty, non-decimal or out-of-range value is 400.",
			false, oas.Obj("type", "integer", "minimum", 1, "maximum", observationPageMax, "default", observationPageDefault)),
		oas.Param("cursor", "query",
			"Exclusive keyset anchor, at most once: the cursor returned with the previous page, which is the receipt id of its last item; the page starts strictly after it in ascending receipt-id order. A canonical lowercase nonzero UUID; any other form, an empty value or a repeated cursor is 400.",
			false, inventoryCanonicalIDSchema("Canonical lowercase nonzero receipt id.")),
	}
}

// inventoryObservationPageResponse is the 200 of the observation history: the
// engine-wide list envelope (listresponse.go) carrying this route's item schema,
// closed at every level. cursor is present only when has_more is true.
func inventoryObservationPageResponse() map[string]any {
	schema := oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("items", "has_more"),
		"properties", oas.Obj(
			"items", oas.Obj("type", "array", "maxItems", observationPageMax, "items", inventoryObservationItemSchema(),
				"description", "One item per distinct receipt that names the entity, ascending by receipt id. Empty when the tenant stores no receipt for the entity, which does not prove it was never observed."),
			"cursor", inventoryCanonicalIDSchema("The receipt id of the last item, present only when has_more is true; pass it as ?cursor to continue strictly after it."),
			"has_more", oas.Obj("type", "boolean",
				"description", "True when at least one more distinct matching receipt id exists past this page. It certifies nothing about that receipt's integrity: the page that composes it decides, and a lookahead is not a certificate."),
		),
	)
	return oas.Obj(
		"description", "One page of the entity's observation history. Every receipt on the page was validated against the writer's own encoding before any item was published: the page is whole, or it is refused with 500.",
		"content", oas.Obj("application/json", oas.Obj("schema", schema)),
	)
}

// inventoryObservationItemSchema is observationDTO (modules/inventory/dto.go)
// field by field: a closed allowlist, so nothing reaches the wire that is not
// named here. Absent on purpose — and asserted absent by the contract test — are
// the event id, receipt key, facts hash and raw facts, the member's name,
// reference, signal, host and native reference, the source label and binding
// reference, the edge and cost payloads, and anything about the source's current
// roster state, health, owner, grants or coverage.
func inventoryObservationItemSchema() map[string]any {
	instant := func(description string) map[string]any {
		return oas.Obj("type", "string", "format", "date-time", "description", description)
	}
	return oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("receipt_id", "event_type", "registration", "first_received_at", "last_received_at", "deliveries", "conflicting_redelivery"),
		"properties", oas.Obj(
			"receipt_id", inventoryCanonicalIDSchema("The platform receipt id. One item per receipt: a receipt whose members name this entity twice is still one item."),
			"event_type", oas.Obj("type", "string", "enum", oas.Enum("edge.observed", "cost.sampled"),
				"description", "The first-party observation type the receipt recorded."),
			"registration", inventoryObservationRegistrationSchema(),
			"source_occurred_at", instant("The instant the SOURCE declared for the fact, taken from the validated member that names this entity; the same claim that feeds the catalog entry's occurred_at. Omitted when the source declared none."),
			"first_received_at", instant("This platform's reception clock for the original delivery of the retained facts."),
			"last_received_at", instant("This platform's reception clock for the last delivery that carried facts equal to the retained ones. A conflicting redelivery has its own counters and is not folded in."),
			"deliveries", oas.Obj("type", "integer", "format", "int64", "minimum", 1,
				"description", "Deliveries of the receipt with equal facts: the original plus exact replays. Not distinct activity, and conflicting variants are excluded."),
			"conflicting_redelivery", oas.Obj("type", "boolean",
				"description", "Whether at least one redelivery of this receipt carried DIFFERENT facts and was retained separately. A boolean by design: the conflicting facts, their count and their times are not published."),
		),
	)
}

// inventoryObservationRegistrationSchema is observationRegistrationDTO as the
// three shapes it actually takes, each closed: the components exist ONLY for
// registered_snapshot, where the recorded snapshot was complete. An incomplete
// snapshot is published as invalid with no components (a partial identity is
// never presented as one), and unattributed records that none was stamped.
func inventoryObservationRegistrationSchema() map[string]any {
	state := func(value, description string) map[string]any {
		return oas.Obj("type", "string", "const", value, "description", description)
	}
	registered := oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("registration_state", "source_id", "source_revision", "environment_ref"),
		"properties", oas.Obj(
			"registration_state", state("registered_snapshot", "The receipt recorded a complete registration snapshot when it was received."),
			"source_id", oas.Obj("type", "string", "minLength", 1,
				"description", "Persistent id of the roster row the snapshot named. A historical identifier, not a grant to read that row."),
			"source_revision", oas.Obj("type", "integer", "format", "int64", "minimum", 1,
				"description", "The roster revision the snapshot says was applied when the observation was produced; not the roster's current revision."),
			"environment_ref", oas.Obj("type", "string", "minLength", 1,
				"description", "The persistent local execution environment the snapshot recorded; not a host and not a workspace."),
		),
	)
	unattributed := oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("registration_state"),
		"properties", oas.Obj("registration_state", state("unattributed", "No registration snapshot was stamped on the observation.")),
	)
	invalid := oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("registration_state"),
		"properties", oas.Obj("registration_state", state("invalid", "A snapshot was stamped but it was incomplete; its partial components are not published.")),
	)
	return oas.Obj(
		"description", "The registration snapshot copied into the receipt when it was received: what the producer's registration looked like THEN. It is historical, not an attestation that the source is registered, healthy or readable now.",
		"oneOf", []any{registered, unattributed, invalid},
	)
}

// inventoryObservationDocumentation is the observation history's closed
// contract: its page parameters, its canonical id and its page answer.
func inventoryObservationDocumentation() api.ModuleOperationDocumentation {
	responses := api.GenericModuleResponses()
	responses["200"] = inventoryObservationPageResponse()
	responses["500"] = oas.JSONResp("stored observation evidence failed an integrity invariant, or the member repository cannot project distinct receipts; the body is the generic error and the reason goes to the operator log, never to the client")
	return api.ModuleOperationDocumentation{
		Responses: responses,
		PathParameters: map[string]map[string]any{
			"id": oas.Param("id", "path", "The catalog entity's core id as a canonical lowercase nonzero UUID; any other spelling is 400 before a row is read. The exact (kind, id) pair must exist in the tenant's catalog, or the answer is 404.",
				true, inventoryCanonicalIDSchema("Canonical lowercase nonzero UUID.")),
		},
		TrailingParameters: inventoryObservationParameters(),
	}
}

// OperationDocumentation publishes the collection evidence read and the
// observation history with their closed contracts; every other inventory route
// keeps the generic envelope.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if method == http.MethodGet && pattern == "/collections" {
		return inventoryCollectionsDocumentation(), true
	}
	if inventoryObservationHistoryRoute(method, pattern) {
		return inventoryObservationDocumentation(), true
	}
	return api.ModuleOperationDocumentation{}, false
}

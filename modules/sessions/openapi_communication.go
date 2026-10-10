// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api/oas"
)

const sessionsCommunicationSDKFamily = "sessions-communication-v1"

func sessionsCommunicationRoute(method, pattern string) bool {
	switch method + " " + pattern {
	case http.MethodGet + " /channels",
		http.MethodPost + " /channels",
		http.MethodPatch + " /channels",
		http.MethodGet + " /channels/administration",
		http.MethodGet + " /channels/{id}",
		http.MethodGet + " /channels/{id}/grants",
		http.MethodPost + " /channels/{id}/grants",
		http.MethodPost + " /channels/{id}/grants/{grant_id}/revoke",
		http.MethodPost + " /messages/send",
		http.MethodGet + " /messages/{id}",
		http.MethodGet + " /deliveries/{id}",
		http.MethodGet + " /inbox",
		http.MethodGet + " /inbox/handoffs",
		http.MethodGet + " /deliveries/{id}/handoff",
		http.MethodGet + " /inbox/cursors/personal/{recipient}",
		http.MethodPut + " /inbox/cursors/personal/{recipient}",
		http.MethodPost + " /deliveries/{id}/ack",
		http.MethodPost + " /handoffs",
		http.MethodPost + " /handoffs/{id}/responses",
		http.MethodPost + " /decision-requests/{id}/responses":
		return true
	default:
		return false
	}
}

func sessionsCommunicationParameters(method, pattern string) ([]map[string]any, bool) {
	if !sessionsCommunicationRoute(method, pattern) {
		return nil, false
	}
	workspace := oas.Param(
		"workspace_id", "query",
		"Canonical stored workspace UUID selected for this personal collection; it is revalidated and never grants authority.",
		true, sessionsProtocolBindingIDSchema(),
	)
	strongETag := func(description string) map[string]any {
		return oas.Param("If-Match", "header", description, true,
			oas.Obj("type", "string", "pattern", "^\\\"v[0-9]+\\\"$"))
	}
	idempotency := func(description string) map[string]any {
		return oas.Param("Idempotency-Key", "header", description, true,
			oas.Obj("type", "string", "format", "uuid"))
	}
	switch method + " " + pattern {
	case http.MethodGet + " /channels":
		return []map[string]any{
			workspace,
			oas.Param("continuation", "query",
				"Authenticated c3n1 continuation returned by the preceding page, anchored to the last Channel it returned; its claims are base64-readable and MAC-authenticated, not encrypted, and it conveys no authority. Raw Channel positions are not accepted.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
			oas.Param("limit", "query", "Visible page size from 1 through 200; default 50.", false,
				oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 50)),
		}, true
	case http.MethodGet + " /channels/administration":
		return []map[string]any{
			workspace,
			oas.Param("state", "query",
				"Persisted Channel state selection: all (default), active or archived. Archived Channels are inspectable because administration must not depend on the operational read catalog; nothing here un-archives one.",
				false, oas.Obj("type", "string", "enum", []any{"all", "active", "archived"}, "default", "all")),
			oas.Param("continuation", "query",
				"Authenticated c3a1 continuation returned by the preceding page, anchored to the last Channel it returned; its claims are base64-readable and MAC-authenticated, not encrypted, and it conveys no authority. It is a distinct family from the read catalog's c3n1 and from c3g1.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
			oas.Param("limit", "query", "Administrable page size from 1 through 200; default 50.", false,
				oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 50)),
		}, true
	case http.MethodGet + " /channels/{id}/grants":
		return []map[string]any{
			workspace,
			oas.Param("state", "query",
				"PERSISTED grant state selection: active (default), revoked, expired or all. It is not a temporal predicate — an active row whose expiry has passed is still selected by active, because it is the row the next mutation must reckon with.",
				false, oas.Obj("type", "string", "enum", []any{"active", "revoked", "expired", "all"}, "default", "active")),
			oas.Param("subject_kind", "query",
				"Exact grant subject kind; required together with subject_ref. Groups are not expanded and names are not resolved.",
				false, oas.Obj("type", "string", "enum", []any{"user", "user_group", "agent", "agent_group", "session"})),
			oas.Param("subject_ref", "query",
				"Exact grant subject reference; required together with subject_kind.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 256)),
			oas.Param("continuation", "query",
				"Authenticated c3g1 continuation returned by the preceding page, anchored to the last grant it returned and bound to the Channel revision that page observed; a revision change between pages is 409 channel_snapshot_changed, never a mixed listing.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
			oas.Param("limit", "query", "Grant page size from 1 through 200; default 50.", false,
				oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 50)),
		}, true
	case http.MethodGet + " /inbox":
		return []map[string]any{
			workspace,
			oas.Param("continuation", "query",
				"Opaque authenticated c2n1 continuation returned by the preceding page; raw delivery positions are not accepted.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
			oas.Param("limit", "query", "Visible page size from 1 through 200; default 50.", false,
				oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 50)),
		}, true
	case http.MethodGet + " /inbox/handoffs":
		return []map[string]any{
			workspace,
			oas.Param("state", "query",
				"Exactly one Handoff state to list: offered (default), accepted, rejected, withdrawn or expired. Repeated, unknown or comma-joined selectors are refused.",
				false, oas.Obj("type", "string",
					"enum", oas.Enum("offered", "accepted", "rejected", "withdrawn", "expired"),
					"default", "offered")),
			oas.Param("continuation", "query",
				"Authenticated h3n1 continuation returned by the preceding page, anchored to the last offer it returned. Its own token family and filter domain: an inbox c2n1 or catalog c3n1 token is refused, and so is a token minted for another state filter. Its claims are base64-readable and MAC-authenticated, not encrypted, and it conveys no authority.",
				false, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
			oas.Param("limit", "query", "Visible page size from 1 through 200; default 50.", false,
				oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 50)),
		}, true
	case http.MethodGet + " /inbox/cursors/personal/{recipient}":
		return []map[string]any{
			workspace,
			oas.Param("target", "query",
				"Opaque c2n1 cursor target returned by inbox listing and revalidated against the current mailbox and cursor lineage.",
				true, oas.Obj("type", "string", "minLength", 1, "maxLength", 2048)),
		}, true
	case http.MethodPatch + " /channels":
		return []map[string]any{strongETag("Current strong Channel ETag.")}, true
	case http.MethodPost + " /channels/{id}/grants",
		http.MethodPost + " /channels/{id}/grants/{grant_id}/revoke":
		return []map[string]any{strongETag("Current strong Channel ETag.")}, true
	case http.MethodPost + " /messages/send":
		return []map[string]any{
			idempotency("Canonical UUID binding exact send retries."),
			oas.Param("If-Plan-Hash", "header",
				"Optional SHA-256 plan precondition; when supplied, publication must reproduce it.", false,
				oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$")),
		}, true
	case http.MethodPut + " /inbox/cursors/personal/{recipient}":
		return []map[string]any{
			strongETag("Strong ETag from cursor-target minting; virtual lineage uses \"v0\"."),
			idempotency("Canonical UUID binding the exact durable cursor command."),
		}, true
	case http.MethodPost + " /deliveries/{id}/ack":
		return []map[string]any{
			strongETag("Current strong Delivery ETag."),
			idempotency("Canonical UUID binding the exact Ack command."),
		}, true
	case http.MethodPost + " /handoffs":
		return []map[string]any{
			strongETag("Current strong WorkItem ETag."),
			idempotency("Canonical UUID binding the atomic offer and carrier creation."),
		}, true
	case http.MethodPost + " /handoffs/{id}/responses":
		return []map[string]any{
			strongETag("Current strong Handoff ETag."),
			idempotency("Canonical UUID binding the atomic response and ownership transition."),
		}, true
	case http.MethodPost + " /decision-requests/{id}/responses":
		return []map[string]any{
			strongETag("Current strong DecisionRequest ETag."),
			idempotency("Canonical UUID binding the exact decision response."),
		}, true
	default:
		return []map[string]any{}, true
	}
}

func sessionsCommunicationResponses(method, pattern string) (map[string]any, bool) {
	if !sessionsCommunicationRoute(method, pattern) {
		return nil, false
	}
	responses := oas.Obj(
		"401", oas.JSONResp("unauthenticated"),
		"403", oas.JSONResp("forbidden"),
		"429", oas.JSONResp("rate limited"),
		// The 503 band carries TWO different facts, and a client that conflates
		// them will get one of them wrong. `evidence_unavailable` and its siblings
		// mean a read could not be completed, and looking again is the remedy.
		// `commit_outcome_unknown` means the engine issued COMMIT and never learned
		// whether the database applied it: the write MAY be durable, an automatic
		// retry may produce a second effect, and no Retry-After is advertised
		// because no interval makes repeating it safe.
		"503", oas.JSONResp("current authority, custody or store evidence is unavailable; or, with code commit_outcome_unknown, the write was issued and its outcome was never learned, so it may be durable and must not be retried automatically"),
	)
	addErrors := func(statuses ...string) {
		for _, status := range statuses {
			description := map[string]string{
				"400": "bad request", "404": "not found", "409": "conflict",
				"412": "precondition failed", "428": "required precondition missing",
			}[status]
			responses[status] = oas.JSONResp(description)
		}
	}
	addSuccess := func(status string, schema map[string]any, description string) {
		responses[status] = oas.JSONRespSchema(description, schema)
	}
	switch method + " " + pattern {
	case http.MethodGet + " /channels":
		addSuccess("200", sessionsChannelCatalogPageSchema(), "Visible Channels of the selected workspace with the caller's current local grant bits")
		addErrors("400", "404")
	case http.MethodPost + " /channels":
		addSuccess("201", sessionsChannelMutationResultSchema(), "Channel and explicit grants created")
		addErrors("400", "404", "409")
	case http.MethodPatch + " /channels",
		http.MethodPost + " /channels/{id}/grants",
		http.MethodPost + " /channels/{id}/grants/{grant_id}/revoke":
		addSuccess("200", sessionsChannelMutationResultSchema(), "Channel mutation committed")
		addErrors("400", "404", "409", "412", "428")
	case http.MethodGet + " /channels/administration":
		addSuccess("200", sessionsChannelAdministrationPageSchema(), "Administrable Channels of the selected workspace with each Channel's current precondition ETag")
		addErrors("400", "404")
	case http.MethodGet + " /channels/{id}":
		addSuccess("200", sessionsChannelSchema(), "Visible Channel")
		addErrors("404")
	case http.MethodGet + " /channels/{id}/grants":
		addSuccess("200", sessionsChannelGrantAdministrationPageSchema(), "Administrable Channel, its precondition ETag and one page of stored grant generations")
		addErrors("400", "404", "409")
	case http.MethodPost + " /messages/send":
		addSuccess("200", sessionsPublishResultSchema(), "Exact durable send receipt replay")
		addSuccess("201", sessionsPublishResultSchema(), "Message and Delivery created")
		addErrors("400", "404", "409", "412")
	case http.MethodGet + " /messages/{id}", http.MethodGet + " /deliveries/{id}":
		addSuccess("200", sessionsDirectNoticeReadResultSchema(), "Authorized opened Delivery")
		addErrors("404")
	case http.MethodGet + " /inbox":
		addSuccess("200", sessionsInboxPageSchema(), "Visible personal inbox page")
		addErrors("400", "404", "412")
	case http.MethodGet + " /inbox/handoffs":
		addSuccess("200", sessionsIncomingHandoffPageSchema(),
			"Visible page of handoff offers addressed to the authenticated recipient, without content")
		addErrors("400", "404")
	case http.MethodGet + " /deliveries/{id}/handoff":
		addSuccess("200", sessionsIncomingHandoffReadResultSchema(),
			"Authorized handoff offer context of one exact Delivery")
		addErrors("404")
	case http.MethodGet + " /inbox/cursors/personal/{recipient}":
		addSuccess("200", sessionsCursorTokenResultSchema(), "Durable cursor target minted")
		addErrors("400", "404", "412")
	case http.MethodPut + " /inbox/cursors/personal/{recipient}":
		addSuccess("200", sessionsCursorAdvanceResultSchema(), "Cursor command committed or replayed")
		addErrors("400", "404", "409", "412", "428")
	case http.MethodPost + " /deliveries/{id}/ack":
		addSuccess("200", sessionsAckResultSchema(), "Ack committed or replayed")
		addErrors("400", "404", "409", "412", "428")
	case http.MethodPost + " /handoffs":
		addSuccess("200", sessionsHandoffOfferResultSchema(), "Exact handoff offer receipt replay")
		addSuccess("201", sessionsHandoffOfferResultSchema(), "Handoff carrier and offer created")
		addErrors("400", "404", "409", "412", "428")
	case http.MethodPost + " /handoffs/{id}/responses":
		addSuccess("200", sessionsHandoffResponseResultSchema(), "Handoff response committed or replayed")
		addErrors("400", "404", "409", "412", "428")
	case http.MethodPost + " /decision-requests/{id}/responses":
		addSuccess("200", sessionsDecisionRequestResponseResultSchema(), "Decision response committed or replayed")
		addErrors("400", "404", "409", "412", "428")
	}
	// The two administrative reads declare `Cache-Control: no-store` on the route
	// (modules/sessions registers them through api.NoStoreRouteRegistrar), so the
	// PUBLISHED contract has to say so: a client reading only this document would
	// otherwise be entitled to cache a refusal the server never wants replayed.
	switch method + " " + pattern {
	case http.MethodGet + " /channels/administration",
		http.MethodGet + " /channels/{id}/grants":
		sessionsCommunicationDeclareNoStore(responses)
	}
	return responses, true
}

// sessionsCommunicationNoStoreHeader is the published form of one route-level
// no-store declaration.
func sessionsCommunicationNoStoreHeader() map[string]any {
	return oas.Obj("Cache-Control", oas.Obj(
		"description", "Always `no-store`. These pages are authorization-dependent and "+
			"observation-dependent: the same request from the same client can legitimately "+
			"produce a different answer a moment later, and a refusal must never be replayed "+
			"from a cache after the authority behind it changed. There is no HTTP ETag and no "+
			"304 on these routes; the `etag` in the body is the Channel precondition for the "+
			"existing mutations, not a representation validator.",
		"required", true,
		"schema", oas.Obj("type", "string", "enum", []any{"no-store"}),
	))
}

// sessionsCommunicationDeclareNoStore stamps the header onto EVERY declared
// response of the route, with no exception.
//
// ⛔ 429 USED TO BE EXCLUDED HERE, AND THE EXCLUSION IS WITHDRAWN. The reasoning
// was that the rate limiter answers before chi routes the request, so no
// per-route declaration could reach it, and publishing a header the server does
// not send would be worse than the omission. The first half was true of the
// implementation and false of the requirement: `routeResponseMetadata` resolves a
// route's declared metadata BEFORE the limiter, so the server now does send it.
// The second half still holds, which is why this function and that middleware
// have to change together — and why core/api's capability test drives a real 429
// through the mounted chain instead of asserting the omission.
func sessionsCommunicationDeclareNoStore(responses map[string]any) {
	for _, raw := range responses {
		response, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		response["headers"] = sessionsCommunicationNoStoreHeader()
	}
}

func sessionsUUIDSchema() map[string]any  { return oas.Obj("type", "string", "format", "uuid") }
func sessionsInt64Schema() map[string]any { return oas.Obj("type", "integer", "format", "int64") }
func sessionsTimestampSchema() map[string]any {
	return oas.Obj("type", "string", "format", "date-time")
}

func sessionsChannelSchema() map[string]any {
	return sessionsClosureClosedObject(sessionsChannelProperties(), sessionsChannelRequiredFields()...)
}

// sessionsChannelProperties is the published Channel shape shared by the point
// read and the catalog item, so the two cannot drift apart.
func sessionsChannelProperties() map[string]any {
	return oas.Obj(
		"id", sessionsUUIDSchema(), "tenant_id", sessionsUUIDSchema(), "workspace_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "slug", oas.Obj("type", "string"), "name", oas.Obj("type", "string"),
		"description", oas.Obj("type", "string"), "kind", oas.Obj("type", "string"), "state", oas.Obj("type", "string"),
		"sensitivity", oas.Obj("type", "string"), "content_protection", oas.Obj("type", "string"),
		"protection_generation", sessionsInt64Schema(),
		"default_ack_policy", oas.Obj("type", "string"), "default_ack_timeout_ms", sessionsInt64Schema(),
		"default_wake", oas.Obj("type", "string"), "retention_policy_ref", oas.Obj("type", "string"),
		"max_fanout", sessionsInt64Schema(), "max_automation_depth", sessionsInt64Schema(),
		"acl_revision", sessionsInt64Schema(), "route_revision", sessionsInt64Schema(),
		"subscription_revision", sessionsInt64Schema(), "created_at", sessionsTimestampSchema(), "updated_at", sessionsTimestampSchema(),
	)
}

func sessionsChannelRequiredFields() []string {
	return []string{"id", "tenant_id", "workspace_id", "version", "slug", "name", "kind", "state", "sensitivity", "content_protection", "protection_generation", "default_ack_policy", "default_ack_timeout_ms", "default_wake", "max_fanout", "max_automation_depth", "acl_revision", "route_revision", "subscription_revision", "created_at", "updated_at"}
}

// sessionsChannelAccessSchema is the caller's three independent current LOCAL
// grant bits on a visible Channel; it asserts nothing about core permissions a
// later write or admin route requires.
func sessionsChannelAccessSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"read", oas.Obj("type", "boolean"), "write", oas.Obj("type", "boolean"),
		"admin", oas.Obj("type", "boolean"),
	), "read", "write", "admin")
}

func sessionsChannelCatalogItemSchema() map[string]any {
	properties := sessionsChannelProperties()
	properties["my_access"] = sessionsChannelAccessSchema()
	return sessionsClosureClosedObject(
		properties, append(sessionsChannelRequiredFields(), "my_access")...,
	)
}

func sessionsChannelCatalogPageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"items", oas.Obj("type", "array", "items", sessionsChannelCatalogItemSchema()),
		"continuation", oas.Obj("type", "string", "description", "Authenticated c3n1 token anchored to the last returned Channel; present only when has_more is true."),
		"has_more", oas.Obj("type", "boolean", "description", "True exactly when a visible limit+1th Channel was authorized in the same bound transaction; never a hidden-row fact."),
	), "items", "has_more")
}

// sessionsChannelAdministrationItemSchema is one administrable Channel and the
// strong ETag of its current version. It carries no my_access: the page
// authorizes ONE administrative decision and emits no accessory read/write claim.
func sessionsChannelAdministrationItemSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel", sessionsChannelSchema(),
		"etag", oas.Obj("type", "string", "description", "Strong Channel validator (\"v<version>\") to copy into If-Match after confirming a mutation; not an HTTP representation validator for this page."),
	), "channel", "etag")
}

func sessionsChannelAdministrationPageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"items", oas.Obj("type", "array", "items", sessionsChannelAdministrationItemSchema()),
		"continuation", oas.Obj("type", "string", "description", "Authenticated c3a1 token anchored to the last returned Channel; present only when has_more is true."),
		"has_more", oas.Obj("type", "boolean", "description", "True exactly when an administrable limit+1th Channel was authorized in the same bound transaction; never a hidden-row fact."),
	), "items", "has_more")
}

// sessionsChannelGrantAdministrationItemSchema reports one stored grant
// generation UNCHANGED plus its temporal state at the page's observation
// instant. temporal_state describes the ROW, not whether its subject may act.
func sessionsChannelGrantAdministrationItemSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"grant", sessionsChannelGrantResultSchema(),
		"temporal_state", oas.Obj("type", "string", "enum", []any{"active", "revoked", "expired"},
			"description", "Whether this row is in force at observed_at. A persisted active row whose expiry has passed reads expired without its stored state being changed."),
	), "grant", "temporal_state")
}

func sessionsChannelGrantAdministrationPageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel", sessionsChannelSchema(),
		"etag", oas.Obj("type", "string", "description", "Strong Channel validator and the If-Match precondition of the existing mutations; the page carries no HTTP ETag and no 304 because filters, pagination and observed_at change the body without changing Channel.version."),
		"observed_at", sessionsTimestampSchema(),
		"items", oas.Obj("type", "array", "items", sessionsChannelGrantAdministrationItemSchema()),
		"continuation", oas.Obj("type", "string", "description", "Authenticated c3g1 token anchored to the last returned grant and bound to the Channel revision observed; present only when has_more is true."),
		"has_more", oas.Obj("type", "boolean"),
	), "channel", "etag", "observed_at", "items", "has_more")
}

func sessionsChannelGrantResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", sessionsUUIDSchema(), "tenant_id", sessionsUUIDSchema(), "workspace_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "channel_id", sessionsUUIDSchema(),
		"subject", sessionsCommunicationRefSchema("user", "user_group", "agent", "agent_group", "session"),
		"can_read", oas.Obj("type", "boolean"), "can_write", oas.Obj("type", "boolean"),
		"can_admin", oas.Obj("type", "boolean"), "state", oas.Obj("type", "string"),
		"generation", sessionsInt64Schema(), "expires_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"granted_by", sessionsCommunicationRefSchema("user", "agent", "session", "system"),
		"revoked_by", sessionsClosureNullable(sessionsCommunicationRefSchema("user", "agent", "session", "system")),
		"supersedes_id", sessionsUUIDSchema(), "created_at", sessionsTimestampSchema(), "updated_at", sessionsTimestampSchema(),
	), "id", "tenant_id", "workspace_id", "version", "channel_id", "subject", "can_read", "can_write", "can_admin", "state", "generation", "granted_by", "created_at", "updated_at")
}

func sessionsChannelMutationResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel", sessionsChannelSchema(),
		"grant", sessionsClosureNullable(sessionsChannelGrantResultSchema()),
		"grants", oas.Obj("type", "array", "items", sessionsChannelGrantResultSchema()),
		"etag", oas.Obj("type", "string"), "audit_seq", sessionsInt64Schema(),
	), "channel", "etag", "audit_seq")
}

func sessionsFulfillmentSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"state", oas.Obj("type", "string"), "required", sessionsInt64Schema(),
		"acknowledged", sessionsInt64Schema(), "viable", sessionsInt64Schema(),
		"unmet", sessionsInt64Schema(), "quorum", sessionsInt64Schema(),
	), "state", "required", "acknowledged", "viable", "unmet")
}

func sessionsPublishResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"verdict", oas.Obj("type", "string"), "code", oas.Obj("type", "string"),
		"command_id", sessionsUUIDSchema(), "channel_id", sessionsUUIDSchema(),
		"message_id", sessionsUUIDSchema(), "delivery_id", sessionsUUIDSchema(), "event_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "state", oas.Obj("type", "string"),
		"delivery_count", sessionsInt64Schema(), "required_count", sessionsInt64Schema(), "ack_quorum", sessionsInt64Schema(),
		"fulfillment", sessionsFulfillmentSchema(), "audience_hash", oas.Obj("type", "string"),
		"payload_digest", oas.Obj("type", "string"), "plan_hash", oas.Obj("type", "string"), "audit_seq", sessionsInt64Schema(),
		"replayed", oas.Obj("type", "boolean"),
	), "verdict", "code", "command_id", "channel_id", "message_id", "delivery_id", "event_id", "version", "state", "delivery_count", "required_count", "ack_quorum", "fulfillment", "audience_hash", "payload_digest", "plan_hash", "audit_seq", "replayed")
}

func sessionsMessageViewSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", sessionsUUIDSchema(), "version", sessionsInt64Schema(), "channel_id", sessionsUUIDSchema(),
		"thread_id", sessionsUUIDSchema(), "state", oas.Obj("type", "string"),
		"sender", sessionsCommunicationRefSchema("user", "agent", "session", "system"),
		"content", sessionsMessageContentSchema(), "urgency", oas.Obj("type", "string"),
		"ack_policy", oas.Obj("type", "string"), "ack_quorum", sessionsInt64Schema(),
		"available_at", sessionsTimestampSchema(), "ack_due_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"expires_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"published_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"terminal_at", sessionsClosureNullable(sessionsTimestampSchema()), "terminal_code", oas.Obj("type", "string"),
	), "id", "version", "channel_id", "thread_id", "state", "sender", "content", "urgency", "ack_policy", "available_at")
}

func sessionsDeliveryViewSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", sessionsUUIDSchema(), "version", sessionsInt64Schema(), "message_id", sessionsUUIDSchema(),
		"recipient", sessionsCommunicationRefSchema("user", "agent", "session"),
		"delivery_seq", sessionsInt64Schema(), "required", oas.Obj("type", "boolean"),
		"state", oas.Obj("type", "string"), "available_at", sessionsTimestampSchema(),
		"first_seen_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"ack_due_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"expires_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"acknowledged_at", sessionsClosureNullable(sessionsTimestampSchema()),
	), "id", "version", "message_id", "recipient", "delivery_seq", "required", "state", "available_at")
}

func sessionsDirectNoticeReadResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"message", sessionsMessageViewSchema(), "delivery", sessionsDeliveryViewSchema(),
		"fulfillment", sessionsFulfillmentSchema(),
	), "message", "delivery", "fulfillment")
}

func sessionsInboxPageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"items", oas.Obj("type", "array", "items", sessionsDirectNoticeReadResultSchema()),
		"continuation", oas.Obj("type", "string", "description", "Opaque c2n1 next-page token; omitted on the final page."),
		"cursor_target", oas.Obj("type", "string", "description", "Opaque c2n1 target for the last returned Delivery."),
		"has_more", oas.Obj("type", "boolean"),
	), "items", "has_more")
}

func sessionsCursorTokenResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"cursor", oas.Obj("type", "string"), "cursor_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "etag", oas.Obj("type", "string"),
	), "cursor", "version", "etag")
}

func sessionsCursorProjectionSchema() map[string]any {
	return oas.Obj("type", "object", "additionalProperties", false, "properties", oas.Obj(
		"last_seen_seq", sessionsInt64Schema(), "barrier_delivery_id", sessionsUUIDSchema(),
		"barrier_reason", oas.Obj("type", "string"), "barrier_since", sessionsClosureNullable(sessionsTimestampSchema()),
	))
}

func sessionsCursorAdvanceResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"command_id", sessionsUUIDSchema(), "cursor_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "etag", oas.Obj("type", "string"),
		"projection", sessionsCursorProjectionSchema(), "audit_seq", sessionsInt64Schema(),
		"replayed", oas.Obj("type", "boolean"),
	), "command_id", "cursor_id", "version", "etag", "projection", "audit_seq", "replayed")
}

func sessionsAckResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"command_id", sessionsUUIDSchema(), "ack_id", sessionsUUIDSchema(),
		"delivery_id", sessionsUUIDSchema(), "message_id", sessionsUUIDSchema(), "event_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "etag", oas.Obj("type", "string"), "state", oas.Obj("type", "string"),
		"late", oas.Obj("type", "boolean"), "fulfillment", sessionsFulfillmentSchema(), "audit_seq", sessionsInt64Schema(),
		"replayed", oas.Obj("type", "boolean"),
	), "command_id", "ack_id", "delivery_id", "message_id", "event_id", "version", "etag", "state", "late", "fulfillment", "audit_seq", "replayed")
}

func sessionsDecisionRequestResponseResultSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"command_id", sessionsUUIDSchema(), "request_id", sessionsUUIDSchema(),
		"response_id", sessionsUUIDSchema(), "message_id", sessionsUUIDSchema(),
		"work_item_id", sessionsUUIDSchema(), "work_decision_id", sessionsUUIDSchema(),
		"event_id", sessionsUUIDSchema(), "version", sessionsInt64Schema(),
		"etag", oas.Obj("type", "string"), "state", oas.Obj("type", "string"),
		"audit_seq", sessionsInt64Schema(),
	), "command_id", "request_id", "response_id", "message_id", "work_item_id", "event_id", "version", "etag", "state", "audit_seq")
}

func sessionsHandoffOfferResultSchema() map[string]any    { return sessionsReceiptResultSchema(false) }
func sessionsHandoffResponseResultSchema() map[string]any { return sessionsReceiptResultSchema(true) }

func sessionsReceiptResultSchema(response bool) map[string]any {
	properties := oas.Obj(
		"command_id", sessionsUUIDSchema(), "handoff_id", sessionsUUIDSchema(),
		"message_id", sessionsUUIDSchema(), "delivery_id", sessionsUUIDSchema(),
		"work_item_id", sessionsUUIDSchema(), "event_id", sessionsUUIDSchema(),
		"version", sessionsInt64Schema(), "etag", oas.Obj("type", "string"),
		"state", oas.Obj("type", "string"), "audit_seq", sessionsInt64Schema(),
		"replayed", oas.Obj("type", "boolean"),
	)
	required := []string{"command_id", "handoff_id", "message_id", "delivery_id", "work_item_id", "event_id", "version", "etag", "state", "audit_seq", "replayed"}
	if response {
		properties["ack_id"] = sessionsUUIDSchema()
		properties["owner_epoch"] = sessionsInt64Schema()
		properties["resulting_lease_fence"] = sessionsInt64Schema()
		required = append(required, "owner_epoch")
	}
	return sessionsClosureClosedObject(properties, required...)
}

// sessionsIncomingHandoffOfferProperties is the trustworthy offer identity the
// recipient needs to respond: id, version and the STRONG Handoff ETag the
// existing POST /handoffs/{id}/responses takes in If-Match. The Delivery version
// carried beside it is the Delivery's own CAS coordinate and must never be used
// for that header.
func sessionsIncomingHandoffOfferSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", sessionsUUIDSchema(), "version", sessionsInt64Schema(),
		"etag", oas.Obj("type", "string", "pattern", "^\\\"v[0-9]+\\\"$"),
		"state", oas.Obj("type", "string",
			"enum", oas.Enum("offered", "accepted", "rejected", "withdrawn", "expired")),
		"from", sessionsCommunicationRefSchema("user", "agent", "session"),
		"to", sessionsCommunicationRefSchema("user", "agent", "session"),
		"ack_deadline", sessionsTimestampSchema(), "created_at", sessionsTimestampSchema(),
		"terminal_at", sessionsClosureNullable(sessionsTimestampSchema()),
		"terminal_code", oas.Obj("type", "string"),
	), "id", "version", "etag", "state", "from", "to", "ack_deadline", "created_at")
}

func sessionsIncomingHandoffCarrierSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"channel_id", sessionsUUIDSchema(), "message_id", sessionsUUIDSchema(),
		"delivery_id", sessionsUUIDSchema(), "delivery_version", sessionsInt64Schema(),
	), "channel_id", "message_id", "delivery_id", "delivery_version")
}

// sessionsIncomingHandoffWorkItemSchema is a REFERENCE, not a work projection.
// The presentation marker states that this surface never carries the WorkItem
// record; reading the work itself needs sessions:work:read on its own route.
func sessionsIncomingHandoffWorkItemSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", sessionsUUIDSchema(),
		"presentation", oas.Obj("type", "string", "enum", oas.Enum("handoff_context")),
	), "id", "presentation")
}

func sessionsIncomingHandoffSummaryProperties() map[string]any {
	return oas.Obj(
		"handoff", sessionsIncomingHandoffOfferSchema(),
		"carrier", sessionsIncomingHandoffCarrierSchema(),
		"work_item", sessionsIncomingHandoffWorkItemSchema(),
		"observed_at", sessionsTimestampSchema(),
		"deadline_elapsed", oas.Obj("type", "boolean",
			"description", "Response window measured against database time. A row may still be persisted as offered after its deadline; this read never runs the reaper."),
	)
}

func sessionsIncomingHandoffSummaryRequiredFields() []string {
	return []string{"handoff", "carrier", "work_item", "observed_at", "deadline_elapsed"}
}

func sessionsIncomingHandoffSummarySchema() map[string]any {
	return sessionsClosureClosedObject(
		sessionsIncomingHandoffSummaryProperties(),
		sessionsIncomingHandoffSummaryRequiredFields()...,
	)
}

func sessionsIncomingHandoffPageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"items", oas.Obj("type", "array", "items", sessionsIncomingHandoffSummarySchema()),
		"continuation", oas.Obj("type", "string", "description", "Authenticated h3n1 token anchored to the last returned offer; present only when has_more is true."),
		"has_more", oas.Obj("type", "boolean", "description", "True exactly when a visible limit+1th offer was authorized in the same bound transaction; never a hidden-row fact and never a total."),
	), "items", "has_more")
}

// sessionsIncomingHandoffReadResultSchema is the summary plus the protected
// handoff context the OFFERER supplied. offer_context reports whether an offered
// Handoff still matches the owner, owner epoch, context sequence and lease fence
// of the work it names; it is not a capability and promises no acceptance.
func sessionsIncomingHandoffReadResultSchema() map[string]any {
	properties := sessionsIncomingHandoffSummaryProperties()
	properties["content"] = sessionsHandoffContentSchema()
	properties["terminal_reason"] = sessionsClosureNullable(sessionsReasonSchema())
	properties["offer_context"] = oas.Obj("type", "string",
		"enum", oas.Enum("current", "stale", "terminal"))
	return sessionsClosureClosedObject(properties,
		append(sessionsIncomingHandoffSummaryRequiredFields(), "content", "offer_context")...)
}

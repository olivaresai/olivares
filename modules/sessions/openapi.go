// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// OperationDocumentation publishes what the sessions handlers enforce beyond
// the generic module envelope: the request bodies they decode, the typed
// answers of the readiness, host-tools, communication, work and run-control
// routes, and the parameters and path ids those routes require. A route it
// does not describe keeps the generic envelope; a mutation it does not
// classify stays unclassified.
func (*Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if doc, ok := sessionsGitDocumentation(method, pattern); ok {
		return doc, true
	}
	var doc api.ModuleOperationDocumentation
	documented := false
	if responses, ok := sessionsResponses(method, pattern); ok {
		doc.Responses = responses
		documented = true
	}
	if params := sessionsParameters(method, pattern); len(params) > 0 {
		doc.TrailingParameters = params
		documented = true
	}
	if sessionsProtocolBindingRoute(pattern) && strings.Contains(pattern, "{id}") {
		doc.PathParameters = map[string]map[string]any{
			"id": oas.Param("id", "path", "Path parameter id.", true, sessionsProtocolBindingIDSchema()),
		}
		documented = true
	}
	if sessionsCommunicationRoute(method, pattern) {
		doc.Extensions = map[string]any{"x-olivares-sdk-family": sessionsCommunicationSDKFamily}
		documented = true
	}
	if kind, ok := sessionsRequestBodyKind(method, pattern); ok {
		doc.BodyKind = kind
		documented = true
	}
	if body, ok := sessionsRequestBody(method, pattern); ok {
		doc.RequestBody = body
		documented = true
	}
	return doc, documented
}

// sessionsResponses is the whole responses object of every sessions route
// whose answers differ from the generic envelope.
func sessionsResponses(method, pattern string) (map[string]any, bool) {
	if sessionsLaunchReadinessRoute(method, pattern) {
		// A typed contract, not the generic envelope: this route's whole value is
		// that a console can render causes and pending checks without re-deriving
		// the server's state machine, and it can only do that if the states, codes
		// and remediations are published as closed enums.
		return sessionsLaunchReadinessResponses(), true
	}
	if sessionsHostToolsRoute(method, pattern) {
		// Same reason as the readiness sibling above: the value of this read is its
		// closed state and group vocabulary, and the generic envelope publishes none
		// of it. It declares NO query parameters, so sessionsParameters adds none -
		// the ordinary path ref from the module registration is the whole parameter
		// set, and a non-empty query string is a 400.
		return sessionsHostToolsResponses(), true
	}
	if responses, ok := sessionsCommunicationResponses(method, pattern); ok {
		return responses, true
	}
	if responses, ok := sessionsRunPreviewResponses(method, pattern); ok {
		return responses, true
	}
	if !sessionsWorkRoute(pattern) && !sessionsProtocolBindingReconcile(method, pattern) &&
		!sessionsProtocolBindingSpecMutation(method, pattern) &&
		!sessionsProviderAccountCreate(method, pattern) && !sessionsProviderAccountPatch(method, pattern) &&
		!sessionsRunControlRoute(method, pattern) {
		return nil, false
	}
	resp := api.GenericModuleResponses()
	if sessionsWorkRoute(pattern) {
		resp["412"] = oas.JSONResp("ETag or plan precondition failed")
		resp["422"] = oas.JSONResp("domain invariant not satisfied")
		resp["423"] = oas.JSONResp("work is blocked")
		resp["503"] = oas.JSONResp("required evidence, policy, clock or store is unavailable")
	}
	if sessionsProtocolBindingReconcile(method, pattern) {
		resp["412"] = oas.JSONResp("ETag or plan precondition failed")
		resp["428"] = oas.JSONResp("strong ETag required for apply")
		resp["503"] = oas.JSONResp("remote observation is unavailable")
	}
	if sessionsProtocolBindingSpecMutation(method, pattern) {
		resp["412"] = oas.JSONResp("ETag or plan precondition failed")
		resp["428"] = oas.JSONResp("apply precondition required")
		unavailable := "required store is unavailable"
		if pattern == "/protocol-binding-specs/{id}/activate" {
			unavailable = "fresh capability observation or required store is unavailable"
		}
		resp["503"] = oas.JSONResp(unavailable)
		if pattern == "/protocol-binding-specs" {
			resp["201"] = oas.JSONResp("ProtocolBindingSpec draft created")
		}
	}
	if method == http.MethodGet && pattern == "/work-stream" {
		// The work stream is Server-Sent Events under a name the generic
		// "stream"/"attach" rule does not match.
		resp["200"] = oas.Obj("description", "OK (text/event-stream)",
			"content", oas.Obj("text/event-stream", oas.Obj("schema", oas.Obj("type", "string"))))
	}
	if sessionsProviderAccountCreate(method, pattern) {
		// 201, and the 200 is DELETED rather than left beside it. This route makes a
		// directory on the node and a row that names it, and 201 is the only success
		// it can answer with; publishing a 200 it never produces is the same defect
		// the three run controls below were corrected for, one size smaller.
		delete(resp, "200")
		resp["201"] = oas.JSONResp("the provider account, and the home the engine built for it")
		resp["422"] = oas.JSONResp("Invalid account name, retry key or execution environment")
		resp["503"] = oas.JSONResp("Account home service unavailable or outcome unresolved; retry the same idempotency key")
	}
	if sessionsProviderAccountPatch(method, pattern) {
		resp["422"] = oas.JSONResp("Invalid account name")
	}
	// ⛔ THE THREE RUN CONTROLS ARE PUBLISHED LAST, AND THEY OVERRIDE.
	//
	// The generic set above assumes every module operation succeeds with 200 and
	// answers with an unspecified object. For input/interrupt/stop both halves were
	// measurably false, and an independent review read the published document
	// against the handlers to prove it: input answers 202 on BOTH of its success
	// paths while the contract advertised only 200, and all three can answer 503
	// with a WorkError UNKNOWN — reproduced over real HTTP, SQLite and an owned
	// child by removing one member of the four-column K2 authority stamp — while
	// none of them published 503, because the work-plane statuses were added only for the
	// routes sessionsWorkRoute recognizes and that list is the work PLANE, not
	// /runs/*.
	//
	// A drift-clean generator cannot notice either: openapi:check and sdk:check
	// compare the artifact to the generator, and both agreed on the same wrong
	// answer. What notices is a test that drives the real handler and compares what
	// came back to what this function published — see the sessions module's
	// run-control contract test.
	if sessionsRunControlRoute(method, pattern) {
		resp["503"] = sessionsWorkUnknownResp()
		switch pattern {
		case "/runs/{ref}/input":
			// 202, and the 200 is DELETED rather than kept beside it: leaving both
			// would publish a success this handler cannot produce, which is the same
			// defect one size smaller.
			delete(resp, "200")
			resp["202"] = oas.Obj(
				"description", "the input was accepted for the owned child; delivery is not confirmed by this response",
				"content", oas.Obj("application/json", oas.Obj("schema", sessionsRunInputAcceptedSchema())),
			)
		case "/runs/{ref}/interrupt", "/runs/{ref}/stop":
			resp["200"] = oas.Obj(
				"description", "the run resource after the control was applied",
				"content", oas.Obj("application/json", oas.Obj("schema", sessionsRunResourceSchema())),
			)
		}
	}
	return resp, true
}

// sessionsParameters describes the command envelopes and filters the sessions
// handlers enforce, from facts of those handlers. Unlike guessed DTO fields,
// these route/header/query controls are required for a generated client to
// invoke validate, plan and apply correctly.
func sessionsParameters(method, pattern string) []map[string]any {
	if sessionsLaunchReadinessRoute(method, pattern) {
		return sessionsLaunchReadinessParameters()
	}
	if method == http.MethodGet && (pattern == "/live" || pattern == "/runs") {
		return []map[string]any{
			oas.Param("pagination", "query", "Optional cursor mode traverses every row in stable ID order; absent keeps the recency-ordered page and ignores cursor.", false, oas.Obj("type", "string", "enum", oas.Enum("cursor"))),
			oas.Param("limit", "query", "Page size; default and maximum follow the store's list contract.", false, oas.Obj("type", "integer", "minimum", 1, "maximum", 1000)),
			oas.Param("cursor", "query", "Opaque cursor returned by the previous page; used only with pagination=cursor.", false, oas.Obj("type", "string")),
		}
	}

	if sessionsResolvePreviewRoute(method, pattern) {
		return sessionsResolvePreviewParameters()
	}
	if params, ok := sessionsCommunicationParameters(method, pattern); ok {
		return params
	}
	if sessionsProtocolBindingReconcile(method, pattern) {
		mode := oas.Param("mode", "query",
			"Mandatory reconciliation phase. validate and plan are local and observational; test reads the peer without a local write; apply revalidates and commits the observation.",
			true, oas.Obj("type", "string", "enum", oas.Enum("validate", "plan", "test", "apply")))
		return []map[string]any{
			mode,
			oas.Param("Idempotency-Key", "header",
				"UUID required when mode=apply; reuse it only for an exact retry.", false,
				oas.Obj("type", "string", "format", "uuid")),
			oas.Param("If-Match", "header",
				"Strong ProtocolBinding ETag required when mode=apply; when supplied in another mode it must match the current version.",
				false, oas.Obj("type", "string", "pattern", "^\\\"v[1-9][0-9]*\\\"$")),
			oas.Param("If-Plan-Hash", "header",
				"Optional SHA-256 plan hash. It must agree with body.plan_hash when both are supplied and must match the current reconciliation plan.",
				false, sessionsProtocolBindingHashSchema()),
		}
	}
	if sessionsProtocolBindingSpecMutation(method, pattern) {
		mode := oas.Param("mode", "query",
			"Mandatory authoring phase. The server derives a fresh capability witness; validate and plan are observational, while apply revalidates and commits the spec transition.",
			true, oas.Obj("type", "string", "enum", oas.Enum("validate", "plan", "apply")))
		params := []map[string]any{
			mode,
			oas.Param("Idempotency-Key", "header",
				"Canonical UUID required when mode=apply; reuse it only for an exact retry.", false,
				oas.Obj("type", "string", "format", "uuid")),
			oas.Param("If-Plan-Hash", "header",
				"SHA-256 plan hash required when mode=apply; apply must reproduce it. For state transitions it must agree with body.plan_hash when both are supplied.",
				false, sessionsProtocolBindingHashSchema()),
		}
		if sessionsProtocolBindingSpecStateMutation(method, pattern) {
			params = append(params, oas.Param("If-Match", "header",
				"Strong ProtocolBindingSpec ETag required when mode=apply; when supplied in validate or plan it must match the current version.",
				false, oas.Obj("type", "string", "pattern", "^\\\"v[1-9][0-9]*\\\"$")))
		}
		return params
	}
	if sessionsProtocolBindingSpecList(method, pattern) {
		return protocolBindingSpecListParameters()
	}
	if sessionsProtocolBindingList(method, pattern) {
		return protocolBindingListParameters()
	}
	if !sessionsWorkRoute(pattern) {
		return nil
	}
	if sessionsWorkMutation(method, pattern) {
		mode := sessionsStringParam("mode", "query",
			"Mandatory command phase. validate and plan are observational; apply revalidates and mutates.", true)
		mode["schema"] = oas.Obj("type", "string", "enum", []any{"validate", "plan", "apply"})
		return []map[string]any{
			mode,
			sessionsStringParam("Idempotency-Key", "header",
				"UUID required when mode=apply; reuse it only for an exact retry.", false),
			sessionsStringParam("If-Match", "header",
				"Strong resource ETag required when mode=apply mutates an existing resource.", false),
			sessionsStringParam("If-Plan-Hash", "header", sessionsWorkPlanHashDescription(pattern), false),
		}
	}
	params := []map[string]any{}
	if pattern == "/work-stream" {
		params = append(params,
			sessionsStringParam("cursor", "query", "Resume strictly after this persisted UUIDv7 WorkEvent id.", false),
			sessionsStringParam("Last-Event-ID", "header", "SSE resume cursor; must agree with cursor when both are sent.", false),
		)
		return params
	}
	if pattern == "/work-items" || pattern == "/decisions" || pattern == "/leases" ||
		strings.HasSuffix(pattern, "/dependencies") || strings.HasSuffix(pattern, "/acceptance") ||
		strings.HasSuffix(pattern, "/events") {
		params = append(params,
			sessionsStringParam("limit", "query", "Keyset page size from 1 through 200; default 100.", false),
			sessionsStringParam("cursor", "query", "Opaque UUIDv7 keyset cursor returned by the previous page.", false),
		)
	}
	if pattern == "/work-items" {
		for _, name := range []string{"status", "priority", "work_kind", "owner_kind", "owner_ref",
			"provenance_kind", "provenance_ref", "parent_id", "archived", "due_before", "updated_after"} {
			params = append(params, sessionsStringParam(name, "query", "Allowlisted WorkItem filter; filters combine with AND.", false))
		}
	}
	if pattern == "/decisions" {
		for _, name := range []string{"work_item_id", "decision_key", "subject_kind", "subject_ref",
			"decided_by_kind", "decided_by_ref", "effective", "revoked"} {
			params = append(params, sessionsStringParam(name, "query", "Allowlisted Decision filter; filters combine with AND.", false))
		}
	}
	if pattern == "/leases" {
		for _, name := range []string{"work_item_id", "holder_sid", "state", "expires_before"} {
			params = append(params, sessionsStringParam(name, "query", "Allowlisted WorkLease filter; filters combine with AND.", false))
		}
	}
	return params
}

// sessionsRequestBodyKind is the request-body kind of a sessions mutation:
// the raw workspace file write, the handler-derived declarations, and the
// command envelopes of the protocol-binding, run and work routes.
func sessionsRequestBodyKind(method, pattern string) (api.ModuleOperationBodyKind, bool) {
	if sessionsRawFileWrite(method, pattern) {
		return api.ModuleOperationJSONBody, true
	}
	if decl, ok := sessionsClosureRequestBodyDeclarationFor(method, pattern); ok {
		switch decl.kind {
		case sessionsClosureBodyful:
			return api.ModuleOperationJSONBody, true
		case sessionsClosureBodyless:
			return api.ModuleOperationBodyless, true
		case sessionsClosureBodyNoDerivable, sessionsClosureBodyPending:
			// Undecided: published unclassified until the handler is read.
		}
		return "", true
	}
	if sessionsCommandBodyIsSchemaPublished(method, pattern) {
		return api.ModuleOperationJSONBody, true
	}
	return "", false
}

// sessionsCommandBodyIsSchemaPublished mirrors only the direct command schemas
// in sessionsRequestBody. It stays route-explicit so an unrelated POST cannot be
// upgraded merely because it is a payload verb.
func sessionsCommandBodyIsSchemaPublished(method, pattern string) bool {
	if method == http.MethodPost {
		switch pattern {
		case "/protocol-binding-specs",
			"/protocol-binding-specs/{id}/activate",
			"/protocol-binding-specs/{id}/disable",
			"/protocol-bindings/{id}/reconcile",
			"/runs/{ref}/input",
			"/runs/{ref}/interrupt",
			"/runs/{ref}/stop":
			return true
		}
	}
	return sessionsWorkMutation(method, pattern)
}

// sessionsRawFileWrite is the workspace file write: its body IS the file's
// content (workspace_api.go handleWriteFile reads r.Body raw).
func sessionsRawFileWrite(method, pattern string) bool {
	return method == http.MethodPut && pattern == "/workspaces/{ref}/files/raw"
}

// sessionsRequestBody returns the request body a sessions handler reads.
func sessionsRequestBody(method, pattern string) (map[string]any, bool) {
	if sessionsRawFileWrite(method, pattern) {
		return oas.Obj("required", true, "content",
			oas.Obj("application/octet-stream", oas.Obj("schema", oas.Obj("type", "string", "format", "binary")))), true
	}
	if body, ok := sessionsClosureRequestBody(method, pattern); ok {
		return body, true
	}
	if method == http.MethodPost {
		var schema map[string]any
		required := true
		switch pattern {
		case "/protocol-binding-specs":
			schema = protocolBindingSpecInputSchema()
		case "/protocol-binding-specs/{id}/activate", "/protocol-binding-specs/{id}/disable":
			required = false
			schema = protocolBindingPlanHashBodySchema(
				"Optional spec-transition plan precondition. The body may be empty in validate and plan; apply still requires a matching plan hash through this field or If-Plan-Hash.",
			)
		case "/protocol-bindings/{id}/reconcile":
			required = false
			schema = protocolBindingPlanHashBodySchema(
				"Optional reconciliation plan precondition. No body is required in any mode; apply requires If-Match but does not require a plan hash.",
			)
		case "/runs/{ref}/input":
			schema = oas.Obj(
				"type", "object",
				"description", "One run input. line/message are RAW frames for a Claude stream-json child; text is a turn for a session driven by an owned provider protocol, which refuses a raw frame. Send one form, never both. Omit work_lease_fence for legacy non-work runs; a positive value selects fenced WorkItem control and is accepted with either form, so a work-bound driver run is spoken to through the same fence as its other controls.",
				"additionalProperties", false,
				"properties", oas.Obj(
					"line", oas.Obj("type", "string", "minLength", 1),
					"message", oas.Obj("type", "object", "additionalProperties", true),
					"text", oas.Obj("type", "string", "minLength", 1),
					"work_lease_fence", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
				),
			)
		case "/runs/{ref}/interrupt":
			required = false
			schema = oas.Obj(
				"type", "object",
				"description", "Optional fenced turn interruption. An empty body preserves legacy non-work run behavior; a positive work_lease_fence selects the same fenced WorkItem control plane this run's input and stop use. It cancels the active provider turn only: the owned process, its conversation and the run stay usable for the next input, and it never falls back to stop.",
				"additionalProperties", false,
				"properties", oas.Obj(
					"work_lease_fence", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
				),
			)
		case "/runs/{ref}/stop":
			required = false
			schema = oas.Obj(
				"type", "object",
				"description", "Optional fenced stop command. An empty body preserves legacy non-work run behavior; reason requires a positive work_lease_fence.",
				"additionalProperties", false,
				"properties", oas.Obj(
					"work_lease_fence", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
					"reason", oas.Obj("type", "string", "minLength", 1, "maxLength", 512),
				),
			)
		}
		if schema != nil {
			return oas.Obj("required", required, "content", oas.Obj(
				"application/json", oas.Obj("schema", schema),
			)), true
		}
	}
	if sessionsWorkMutation(method, pattern) {
		properties := oas.Obj("command", oas.Obj("type", "string"))
		description := "WorkCommand document shared by validate, plan and apply. Route target fields are server-bound."
		required := true
		if pattern == "/work-events/{event_id}/replay" {
			description = "Optional outbox replay plan document. event_id is server-bound; apply uses the required If-Plan-Hash header."
			properties = oas.Obj("plan_hash", oas.Obj("type", "string", "minLength", 64, "maxLength", 64))
			required = false
		}
		if strings.Contains(pattern, "/lease/") {
			description = "Lease WorkCommand shared by validate, plan and apply. The WorkItem target is server-bound."
			properties["holder_sid"] = oas.Obj("type", "string")
			properties["holder_run_ref"] = oas.Obj("type", "string")
			properties["holder_agent_ref"] = oas.Obj("type", "string")
			properties["ttl_seconds"] = oas.Obj("type", "integer", "format", "int64")
			properties["fence"] = oas.Obj("type", "integer", "format", "int64")
			properties["force"] = oas.Obj("type", "boolean")
			properties["unblock"] = oas.Obj("type", "boolean")
			properties["changes_requested"] = oas.Obj("type", "boolean")
			// These fields are a union across the six lease commands. Their
			// command-specific required/empty/length rules are enforced by the
			// domain validator; the shared envelope must not claim that an optional
			// empty value accepted by another lease verb is structurally invalid.
			properties["reason"] = oas.Obj("type", "string")
			properties["decision_id"] = oas.Obj("type", "string")
			properties["evidence_ref"] = oas.Obj("type", "string")
			properties["plan_hash"] = oas.Obj("type", "string")
		}
		additionalProperties := true
		if strings.Contains(pattern, "/lease/") || pattern == "/work-events/{event_id}/replay" {
			// The lease handlers decode a closed WorkCommand projection with
			// json.Decoder.DisallowUnknownFields. Keeping this open made generated
			// clients advertise documents the real API rejects.
			additionalProperties = false
		}
		return oas.Obj("required", required, "content", oas.Obj("application/json", oas.Obj("schema", oas.Obj(
			"type", "object",
			"description", description,
			"additionalProperties", additionalProperties,
			"properties", properties,
		)))), true
	}
	return nil, false
}

// sessionsRunControlRoute is the three OPERATE controls that reach an owned child
// and can therefore answer with the fenced work plane's uncertainty verdict. It is
// deliberately an explicit list rather than a prefix over /runs: the read routes,
// attach, resume, cleanup and delete do not share this response contract.
func sessionsRunControlRoute(method, pattern string) bool {
	if method != http.MethodPost {
		return false
	}
	switch pattern {
	case "/runs/{ref}/input", "/runs/{ref}/interrupt", "/runs/{ref}/stop":
		return true
	}
	return false
}

// sessionsRunInputAcceptedSchema is the exact body POST /runs/{ref}/input returns on
// success. It is closed because the handler writes this one key and nothing else.
func sessionsRunInputAcceptedSchema() map[string]any {
	return oas.Obj(
		"type", "object", "additionalProperties", false,
		"required", oas.Enum("accepted"),
		"properties", oas.Obj("accepted", oas.Obj(
			"type", "boolean",
			"description", "Always true; a non-2xx status is the only way this operation reports refusal.",
		)),
	)
}

// sessionsRunResourceSchema is the run interrupt and stop answer with. It names the two
// fields a caller must be able to rely on and stays OPEN on purpose: the run
// resource carries further non-sensitive lifecycle facts, and freezing today's
// field list here would publish a contract that is wrong the next time one is
// added — the failure mode this whole section exists to remove.
func sessionsRunResourceSchema() map[string]any {
	return oas.Obj(
		"type", "object", "additionalProperties", true,
		"required", oas.Enum("run_ref", "state"),
		"properties", oas.Obj(
			"run_ref", oas.Obj("type", "string", "description", "The operated session's stable reference."),
			"state", oas.Obj("type", "string", "description", "The run's derived lifecycle state after the control."),
		),
	)
}

// sessionsWorkUnknownResp is the fenced control plane's third answer, published with
// its real shape rather than as an unspecified object: the effect may or may not
// have crossed to the child, and a caller that cannot tell that apart from a
// refusal has been told nothing useful.
func sessionsWorkUnknownResp() map[string]any {
	envelope := oas.Obj(
		"type", "object", "additionalProperties", true,
		"required", oas.Enum("verdict", "code", "error"),
		"properties", oas.Obj(
			"verdict", oas.Obj("type", "string", "enum", oas.Enum("NO_HE_PODIDO_MIRAR"),
				"description", "The uncertain verdict: the outcome could not be observed."),
			"code", oas.Obj("type", "string",
				"description", "The durable outcome name, e.g. work_input_ambiguous or evidence_unavailable."),
			"error", oas.Obj(
				"type", "object", "additionalProperties", true,
				"required", oas.Enum("code", "message"),
				"properties", oas.Obj(
					"code", oas.Obj("type", "string"),
					"message", oas.Obj("type", "string"),
				),
			),
			"evidence_ref", oas.Obj("type", "string",
				"description", "Present when the refusal names the field or evidence it could not resolve."),
		),
	)
	return oas.Obj(
		"description", "the outcome is UNKNOWN: required authority, evidence or store could not be observed, and any external effect may or may not have crossed",
		"content", oas.Obj("application/json", oas.Obj("schema", envelope)),
	)
}

func sessionsStringParam(name, in, description string, required bool) map[string]any {
	return oas.Obj("name", name, "in", in, "required", required,
		"description", description, "schema", oas.Obj("type", "string"))
}

func sessionsProtocolBindingHashSchema() map[string]any {
	return oas.Obj(
		"type", "string",
		"pattern", "^(sha256:)?[0-9A-Fa-f]{64}$",
	)
}

func sessionsProtocolBindingIDSchema() map[string]any {
	return oas.Obj(
		"type", "string", "format", "uuid",
		"pattern", "^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
	)
}

func sessionsWorkRoute(pattern string) bool {
	return strings.HasPrefix(pattern, "/work-items") ||
		strings.HasPrefix(pattern, "/work-events") ||
		strings.HasPrefix(pattern, "/decisions") ||
		strings.HasPrefix(pattern, "/leases") || pattern == "/work-stream"
}

func sessionsWorkMutation(method, pattern string) bool {
	return sessionsWorkRoute(pattern) && method != http.MethodGet
}

func sessionsProtocolBindingReconcile(method, pattern string) bool {
	return method == http.MethodPost &&
		pattern == "/protocol-bindings/{id}/reconcile"
}

func sessionsProtocolBindingRoute(pattern string) bool {
	return strings.HasPrefix(pattern, "/protocol-bindings") ||
		strings.HasPrefix(pattern, "/protocol-binding-specs")
}

// sessionsProviderAccountCreate is the one account route that CREATES: it builds
// a home and registers the account that owns it. Its siblings name or read an
// account that already exists and answer 200.
func sessionsProviderAccountCreate(method, pattern string) bool {
	return method == http.MethodPost && pattern == "/provider-accounts"
}

// sessionsProviderAccountPatch is the account route that renames and relabels:
// a malformed name or label is 422, a taken name is 409.
func sessionsProviderAccountPatch(method, pattern string) bool {
	return method == http.MethodPatch && pattern == "/provider-accounts/{ref}"
}

func sessionsProtocolBindingSpecMutation(method, pattern string) bool {
	if method != http.MethodPost {
		return false
	}
	switch pattern {
	case "/protocol-binding-specs", "/protocol-binding-specs/{id}/activate",
		"/protocol-binding-specs/{id}/disable":
		return true
	default:
		return false
	}
}

func sessionsProtocolBindingSpecStateMutation(method, pattern string) bool {
	return sessionsProtocolBindingSpecMutation(method, pattern) && pattern != "/protocol-binding-specs"
}

func sessionsProtocolBindingSpecList(method, pattern string) bool {
	return method == http.MethodGet &&
		pattern == "/protocol-binding-specs"
}

func sessionsProtocolBindingList(method, pattern string) bool {
	return method == http.MethodGet &&
		pattern == "/protocol-bindings"
}

func protocolBindingPageParameters() []map[string]any {
	return []map[string]any{
		oas.Param("limit", "query", "Keyset page size from 1 through 200; default 100.", false,
			oas.Obj("type", "integer", "minimum", 1, "maximum", 200, "default", 100)),
		oas.Param("cursor", "query", "Opaque UUIDv7 keyset cursor returned by the previous page.", false,
			sessionsProtocolBindingIDSchema()),
	}
}

func protocolBindingSpecListParameters() []map[string]any {
	params := []map[string]any{
		oas.Param("workspace_id", "query",
			"Workspace UUID. It may be omitted only when the authenticated principal is confined to one workspace.",
			false, sessionsProtocolBindingIDSchema()),
		oas.Param("binding_key", "query", "Exact normalized binding-key filter.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 128)),
		oas.Param("generation", "query", "Exact immutable spec generation.", false,
			oas.Obj("type", "integer", "format", "int64", "minimum", 1)),
		oas.Param("protocol", "query", "Exact protocol filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("a2a", "mcp"))),
		oas.Param("direction", "query", "Exact binding direction filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("inbound", "outbound", "bidirectional"))),
		oas.Param("local_kind", "query", "Exact local resource-kind filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("work_item", "agent", "model", "channel"))),
		oas.Param("peer_authority", "query", "Exact normalized peer authority filter.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("state", "query", "Exact spec lifecycle-state filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("draft", "active", "disabled", "superseded"))),
	}
	return append(params, protocolBindingPageParameters()...)
}

func protocolBindingListParameters() []map[string]any {
	params := []map[string]any{
		oas.Param("workspace_id", "query",
			"Workspace UUID. It may be omitted only when the authenticated principal is confined to one workspace.",
			false, sessionsProtocolBindingIDSchema()),
		oas.Param("binding_spec_id", "query", "Exact ProtocolBindingSpec UUID filter.", false,
			sessionsProtocolBindingIDSchema()),
		oas.Param("work_item_id", "query", "Exact WorkItem UUID filter.", false,
			sessionsProtocolBindingIDSchema()),
		oas.Param("protocol", "query", "Exact protocol filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("a2a", "mcp"))),
		oas.Param("peer_authority", "query", "Exact normalized peer authority filter.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("owner_kind", "query", "Exact normalized owner kind; owner_ref must be supplied with it.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("owner_ref", "query", "Exact owner reference; owner_kind must be supplied with it.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("external_kind", "query", "Exact normalized remote result-kind filter.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("external_id", "query", "Exact remote resource identifier filter.", false,
			oas.Obj("type", "string", "minLength", 1, "maxLength", 512)),
		oas.Param("verdict", "query", "Exact last-observation verdict filter.", false,
			oas.Obj("type", "string", "enum", oas.Enum("CLEAN", "BROKEN", "UNKNOWN"))),
		oas.Param("terminal", "query", "Filter by terminal or non-terminal bindings.", false,
			oas.Obj("type", "boolean")),
	}
	return append(params, protocolBindingPageParameters()...)
}

func sessionsWorkPlanHashDescription(pattern string) string {
	if pattern == "/work-events/{event_id}/replay" {
		return "SHA-256 plan hash required when mode=apply; apply must reproduce it."
	}
	return "Optional SHA-256 plan hash that apply must reproduce."
}

func protocolBindingSpecInputSchema() map[string]any {
	refSchema := func(description string) map[string]any {
		return oas.Obj(
			"type", "string", "minLength", 1, "maxLength", 512,
			"description", description,
		)
	}
	mappingRule := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"required", oas.Enum("source", "target", "cardinality", "transform"),
		"properties", oas.Obj(
			"source", refSchema("Exact local field or projection reference."),
			"target", refSchema("Exact remote field or projection reference."),
			"cardinality", oas.Obj(
				"type", "string",
				"enum", oas.Enum("one_to_one", "one_to_many", "many_to_one"),
			),
			"transform", oas.Obj(
				"type", "string",
				"enum", oas.Enum("identity", "text", "reference", "metadata", "status"),
			),
		),
	)
	knownLoss := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"required", oas.Enum("field", "reason_code"),
		"properties", oas.Obj(
			"field", refSchema("Field whose semantics are not preserved by the mapping."),
			"reason_code", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
			"accepted", oas.Obj(
				"type", "boolean", "default", false,
				"description", "Whether the semantic loss has an explicit acceptance witness.",
			),
			"acceptance_ref", refSchema(
				"Required when accepted=true and rejected when accepted=false.",
			),
		),
	)
	validation := oas.Obj(
		"type", "object",
		"additionalProperties", false,
		"required", oas.Enum("verdict", "code"),
		"readOnly", true,
		"description", "Server-derived capability witness. Any client-supplied value is ignored; omit this property. Activation requires a fresh CLEAN witness with a non-empty observed_at.",
		"properties", oas.Obj(
			"verdict", oas.Obj("type", "string", "enum", oas.Enum("CLEAN", "BROKEN", "UNKNOWN")),
			"code", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
			"observed_at", oas.Obj("type", "string", "format", "date-time"),
		),
	)

	return oas.Obj(
		"type", "object",
		"description", "Closed ProtocolBindingSpecInput for exactly one immutable, version-pinned protocol mapping generation.",
		"additionalProperties", false,
		"required", oas.Enum(
			"workspace_id", "binding_key", "generation", "protocol", "protocol_version",
			"direction", "local_kind", "local_selector", "peer_authority",
			"remote_resource_kind", "remote_resource_ref", "mapping_schema", "mapping",
			"permission_profile_ref", "currency_policy",
		),
		"properties", oas.Obj(
			"workspace_id", sessionsProtocolBindingIDSchema(),
			"binding_key", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
			"generation", oas.Obj("type", "integer", "format", "int64", "minimum", 1),
			"protocol", oas.Obj("type", "string", "enum", oas.Enum("a2a", "mcp")),
			"protocol_version", refSchema(
				"Pinned protocol version; latest, current and * are rejected.",
			),
			"direction", oas.Obj(
				"type", "string", "enum", oas.Enum("inbound", "outbound", "bidirectional"),
			),
			"local_kind", oas.Obj(
				"type", "string", "enum", oas.Enum("work_item", "agent", "model", "channel"),
			),
			"local_selector", oas.Obj(
				"type", "object", "additionalProperties", true,
				"description", "Canonicalizable JSON object selecting the local surface; encoded size is limited to 64 KiB.",
			),
			"peer_authority", refSchema("Peer authority or normalized absolute authority URL."),
			"remote_resource_kind", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
			"remote_resource_ref", refSchema("Opaque remote resource reference."),
			"mapping_schema", oas.Obj("type", "string", "minLength", 1, "maxLength", 128),
			"mapping", oas.Obj(
				"type", "array", "minItems", 1, "maxItems", 128, "items", mappingRule,
			),
			"known_losses", oas.Obj(
				"type", "array", "maxItems", 128, "items", knownLoss,
			),
			"rule_refs", oas.Obj(
				"type", "array", "maxItems", 64, "uniqueItems", true,
				"items", refSchema("Opaque reference to a governing rule."),
			),
			"permission_profile_ref", refSchema("Pinned permission-profile reference."),
			"currency_policy", oas.Obj("type", "string", "enum", oas.Enum("pinned")),
			"validation", validation,
			"supersedes_id", sessionsProtocolBindingIDSchema(),
		),
	)
}

func protocolBindingPlanHashBodySchema(description string) map[string]any {
	planHash := sessionsProtocolBindingHashSchema()
	planHash["description"] = "Optional plan precondition; it must agree with If-Plan-Hash when both are supplied."
	return oas.Obj(
		"type", "object",
		"description", description,
		"additionalProperties", false,
		"properties", oas.Obj("plan_hash", planHash),
	)
}

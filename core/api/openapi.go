// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
)

// The OpenAPI document is the published REST contract (DoD: "OpenAPI/proto
// publicados"). It is built programmatically (so it is always valid JSON) once
// per Server (in New — per-Server, not a package-level once, so a test-swapped
// deprecation table is honored by the served endpoint too). Modules'
// /v1/m/<ns>/ routes are not enumerated here — a module publishes its own
// sub-spec; this document describes the engine surface that is stable for the
// web UI and SDK clients.

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	writeJSON(w, http.StatusOK, s.openapiDoc)
}

// OpenAPIDocument returns the published OpenAPI 3.1 document as a Go value — the
// exact document served at GET /openapi.json. It is exported so the build can
// emit a committed spec snapshot for the web client's typed codegen WITHOUT a
// running server (`olivares openapi` → web/openapi/openapi.json), keeping the
// generated TypeScript reproducible and CI-checkable against this Go source.
func OpenAPIDocument() map[string]any { return buildOpenAPI() }

func buildOpenAPI() map[string]any {
	obj := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	arr := func(items ...any) []any { return items }
	ref := func(name string) map[string]any {
		return obj("$ref", "#/components/schemas/"+name)
	}
	jsonContent := func(schema map[string]any) map[string]any {
		return obj("application/json", obj("schema", schema))
	}
	jsonResp := func(desc string, schema map[string]any) map[string]any {
		return obj("description", desc, "content", jsonContent(schema))
	}
	listOf := func(itemRef map[string]any) map[string]any {
		return obj(
			"type", "object",
			"properties", obj(
				"items", obj("type", "array", "items", itemRef),
				"cursor", obj("type", "string"),
				"has_more", obj("type", "boolean"),
			),
			"required", arr("items", "has_more"),
		)
	}
	// auditListOf is listOf plus head_seq, and it is a separate helper on purpose:
	// the chain tip belongs to the evidence ledger, not to every collection the API
	// pages. It is REQUIRED because the two states a client must distinguish —
	// "empty ledger" and "the head is event 1" — are 0 and 1, and an absent field
	// collapses them into the same undefined.
	auditListOf := func(itemRef map[string]any) map[string]any {
		schema := listOf(itemRef)
		schema["properties"].(map[string]any)["head_seq"] = obj("type", "integer",
			"description", "The highest sequence number this tenant's ledger has RECORDED, and 0 when it has "+
				"never recorded one. `from` pages FORWARDS (events come back in ascending sequence order), so "+
				"this is the only field that addresses the END of the chain: request "+
				"`from=max(1, head_seq-N+1)&limit=N` and reverse the page to show the newest activity. Read "+
				"the two bounds of that promise exactly, because a caller who assumes more will be wrong on a "+
				"real ledger. (1) The window is N SEQUENCE POSITIONS, not N rows: a chain that declares a gap "+
				"(an in-chain `audit.gap` marker) skips positions, so the page can come back SHORTER than N "+
				"with older events still present below it. (2) head_seq is measured before this request's own "+
				"self-audit event joins the chain, and it is never behind the highest sequence in `items` — "+
				"but it does not identify the exact snapshot the page was read from, because a concurrent "+
				"append can land between the two reads. It is the RECORDED tip, which on a ledger emptied "+
				"under a live head is deliberately not the last addressable row.")
		schema["required"] = arr("items", "has_more", "head_seq")
		return schema
	}
	body := func(schemaRef map[string]any) map[string]any {
		return obj("required", true, "content", jsonContent(schemaRef))
	}

	bearer := []any{obj("bearerAuth", []any{}), obj("browserSession", []any{})}
	noAuth := []any{obj()}

	tenantParam := obj("name", "X-Olivares-Tenant", "in", "header", "required", false,
		"description", "Target tenant id; required when the principal can act in more than one tenant.",
		"schema", obj("type", "string", "format", "uuid"))
	idParam := obj("name", "id", "in", "path", "required", true,
		"description", "Resource identifier (UUIDv7).",
		"schema", obj("type", "string", "format", "uuid"))
	agentIDParam := obj("name", "agentID", "in", "path", "required", true,
		"description", "Agent identifier (UUIDv7).",
		"schema", obj("type", "string", "format", "uuid"))
	// The SSO scope and IdP-alias path parameters are names, not UUIDs: a scope
	// is the deployment-wide default or one tenant's key, and an alias is the
	// scope-unique IdP handle ("default" is the scope's primary).
	ssoTenantParam := obj("name", "tenant", "in", "path", "required", true,
		"description", "Tenant whose IdP surface this is (the tenant's key).",
		"schema", obj("type", "string"))
	aliasParam := obj("name", "alias", "in", "path", "required", true,
		"description", "IdP alias within the scope; \"default\" addresses the scope's primary IdP.",
		"schema", obj("type", "string", "minLength", 1))
	// DR ids are bundle filenames and job ids, not UUIDs; both are opaque to a
	// client and must not be declared with a format the engine does not enforce.
	drIDParam := func(name, desc string) map[string]any {
		return obj("name", name, "in", "path", "required", true, "description", desc,
			"schema", obj("type", "string", "minLength", 1))
	}
	// The engine-log filter both log routes share (parseLogFilter): the exact
	// level set is authoritative when it carries a value; the legacy threshold
	// applies only without it; an empty levels value clears the level filter.
	logLevelParams := []any{
		obj("name", "module", "in", "query", "required", false,
			"description", "Only entries from this module.",
			"schema", obj("type", "string")),
		obj("name", "levels", "in", "query", "required", false,
			"description", "Exact level set, comma-separated (debug, info, warn, error); authoritative whenever non-empty. An empty value clears the level filter; an unknown level is a 400, never a silent widening.",
			"schema", obj("type", "string")),
		obj("name", "level", "in", "query", "required", false,
			"description", "Legacy minimum-level threshold, used only when levels is empty or absent.",
			"schema", obj("type", "string")),
	}
	tenantPathParam := obj("name", "tenant_id", "in", "path", "required", true,
		"description", "Tenant identifier (UUIDv7).",
		"schema", obj("type", "string", "format", "uuid"))
	limitParam := obj("name", "limit", "in", "query", "required", false,
		"description", "Maximum number of items to return.",
		"schema", obj("type", "integer", "minimum", 1, "maximum", 1000, "default", stableListDefaultLimit))
	cursorParam := obj("name", "cursor", "in", "query", "required", false,
		"description", "Pagination cursor from a previous response.",
		"schema", obj("type", "string"))
	// The evidence ledger's keyset cursor. It is a shared variable because BOTH audit
	// list operations are one handler (auditListInto): a parameter declared on only one
	// of them is exactly how the system route came to publish that it accepts no query.
	auditFromParam := obj("name", "from", "in", "query", "required", false,
		"description", "Start from this sequence number. The page runs FORWARDS from it, in ascending sequence order; pair it with head_seq to address the newest events.",
		"schema", obj("type", "integer", "default", 1))
	// Repeatable, and declared as such: one occurrence per action family to leave out.
	// It filters the VIEW only — the ledger still records every event, including the
	// read this request performs, so an unfiltered page still returns them all.
	auditExcludeActionParam := obj("name", "exclude_action", "in", "query", "required", false,
		"description", "Omit events whose action starts with this prefix. Repeatable: give it once per action family to leave out. It uses the SAME prefix rule as `action`, and it filters only what is RETURNED — the ledger still records every event, and a request without this parameter still returns them all. Its use is a caller that must not be shown its own footprint: the console's notification bell passes `exclude_action=audit.read` so that reading the ledger does not itself become the newest activity in it.",
		"schema", obj("type", "array", "items", obj("type", "string")))

	errResponses := func() map[string]any {
		return obj(
			"400", jsonResp("Bad request", ref("Error")),
			"401", jsonResp("Unauthenticated", ref("Error")),
			"403", jsonResp("Forbidden", ref("Error")),
			"404", jsonResp("Not found", ref("Error")),
			"409", jsonResp("Conflict / setup required", ref("Error")),
			"429", jsonResp("Rate limited", ref("Error")),
		)
	}

	op := func(id, summary string, tags []string, secured bool, successResp map[string]any, reqBody map[string]any, params ...any) map[string]any {
		resps := errResponses()
		resps["200"] = successResp
		o := obj(
			"operationId", id,
			"summary", summary,
			"tags", tags,
			"responses", resps,
		)
		if secured {
			o["security"] = bearer
		} else {
			o["security"] = noAuth
		}
		if reqBody != nil {
			o["requestBody"] = reqBody
		}
		if len(params) > 0 {
			o["parameters"] = params
		}
		return o
	}

	op201 := func(id, summary string, tags []string, secured bool, successResp map[string]any, reqBody map[string]any, params ...any) map[string]any {
		o := op(id, summary, tags, secured, jsonResp("OK", obj("type", "object")), reqBody, params...)
		resps := o["responses"].(map[string]any)
		delete(resps, "200")
		resps["201"] = successResp
		return o
	}
	// op202 publishes the asynchronous-accept answer (a job or an intent was
	// recorded; the work itself is followed elsewhere): the DR handlers and the
	// invite resend answer 202, and a contract that says 200 would promise a
	// completed result the handler never sends.
	op202 := func(id, summary string, tags []string, secured bool, successResp map[string]any, reqBody map[string]any, params ...any) map[string]any {
		o := op(id, summary, tags, secured, successResp, reqBody, params...)
		resps := o["responses"].(map[string]any)
		delete(resps, "200")
		resps["202"] = successResp
		return o
	}

	// consentRequired adds the answer a grant gives for an existing account that is
	// not a member of the tenant: 202 and nothing written, whoever asks.
	consentRequired := func(o map[string]any) map[string]any {
		o["responses"].(map[string]any)["202"] = jsonResp(
			"Consent required: the account exists and is not a member of the tenant; nothing was written",
			obj("type", "object", "properties", obj(
				"status", obj("type", "string", "enum", arr("consent_required")),
			), "required", arr("status")))
		return o
	}

	op204 := func(id, summary string, tags []string, secured bool, params ...any) map[string]any {
		o := op(id, summary, tags, secured, jsonResp("OK", obj("type", "object")), nil, params...)
		resps := o["responses"].(map[string]any)
		delete(resps, "200")
		resps["204"] = obj("description", "No content")
		return o
	}

	rawOp := func(id, summary string, tags []string, secured bool, desc string, contentTypes ...string) map[string]any {
		o := op(id, summary, tags, secured, jsonResp("OK", obj("type", "object")), nil)
		content := map[string]any{}
		for _, ct := range contentTypes {
			content[ct] = obj("schema", obj("type", "string"))
		}
		o["responses"].(map[string]any)["200"] = obj("description", desc, "content", content)
		return o
	}

	// capabilityOperation stamps the two contract facts the generic op() helper cannot
	// express for the self capability projection.
	//
	// The 503 is NOT decoration: the endpoint decides common unavailability BEFORE it
	// resolves any target, precisely so the SHAPE of a failure cannot depend on which
	// rows exist. A published contract that omitted it would describe an endpoint whose
	// only failures are per-question, and a client would have no reason to expect the
	// one answer that is not.
	//
	// The SDK family marks this operation for the schema-driven typed DTO emitters, so
	// the four clients carry the finite state/kind/code/budget contract instead of an
	// untyped JSON blob.
	capabilityOperation := func(o map[string]any, unavailable map[string]any) map[string]any {
		o["responses"].(map[string]any)["503"] = unavailable
		o["x-olivares-sdk-family"] = "auth-capabilities-v1"
		return o
	}

	tagHealth := []string{"health"}
	tagAuth := []string{"auth"}
	tagAgents := []string{"agents"}
	tagAudit := []string{"audit"}
	tagUsers := []string{"users"}
	tagTokens := []string{"tokens"}
	tagWorkspaces := []string{"workspaces"}
	tagSystem := []string{"system"}
	tagConsole := []string{"console"}
	tagConnectors := []string{"connectors"}
	tagDirectory := []string{"directory"}

	passwordChange := op204("changeOwnPassword", "Change your password and revoke your other sign-ins; requires the current password and a human session", tagAuth, true)
	passwordChange["requestBody"] = body(obj("type", "object", "additionalProperties", false,
		"required", arr("current_password", "new_password"),
		"properties", obj("current_password", obj("type", "string", "writeOnly", true),
			"new_password", obj("type", "string", "minLength", auth.MinPasswordLen, "writeOnly", true))))

	paths := obj(
		// ── Health ──────────────────────────────────────────────────────
		"/healthz", obj("get", op("healthz", "Liveness probe", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/livez", obj("get", op("livez", "Liveness probe (process is up)", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/readyz", obj("get", op("readyz", "Readiness probe (store reachable AND this node is the active writer AND, before first setup, that setup read can run); 503 on standby, store down, unknown setup state, blocked first boot, or a failed setup probe", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/pod-readyz", obj("get", op("podReadyz", "Pod-health probe (store reachable), with NO leadership check — the HA readiness probe, so a hot standby is healthy; 503 only when the store is down", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/metrics", obj("get", rawOp("getMetrics", "Prometheus exposition (text format 0.0.4) of engine metrics",
			tagHealth, false, "Prometheus text exposition", "text/plain")),
		"/openapi.json", obj("get", op("getOpenAPI", "This OpenAPI document", tagHealth, false,
			jsonResp("OK", obj("type", "object")), nil)),
		"/openapi.beta.json", obj("get", op("getOpenAPIBeta", "The BETA module-route OpenAPI document (/v1/m/<ns>/…), reflected from the routes the modules register", tagHealth, false,
			jsonResp("OK", obj("type", "object")), nil)),
		"/status", obj("get", op("getPublicStatus", "Public status page summary (unauthenticated)", tagHealth, false,
			jsonResp("OK", ref("PublicStatus")), nil)),

		// ── Server info ────────────────────────────────────────────────
		"/v1/server-info", obj("get", op("getServerInfo", "Server version, engine, setup state and license status", tagHealth, false,
			jsonResp("OK", ref("ServerInfo")), nil)),

		// ── Auth ───────────────────────────────────────────────────────
		"/v1/setup", obj("post", op201("setupFirstAdmin", "Create the first organization and the superadmin that owns it, with the one-time setup token", tagAuth, false,
			jsonResp("Created", ref("SetupResult")),
			body(ref("SetupInput")))),
		"/v1/auth/login", obj("post", op("login", "Exchange email/password for a session token", tagAuth, false,
			jsonResp("OK", ref("LoginResponse")),
			body(ref("LoginInput")))),
		"/v1/auth/browser-session", obj(
			"get", op("getBrowserSession", "Restore cookie session metadata", tagAuth, true, jsonResp("OK", ref("BrowserSessionResponse")), nil),
			"post", op("migrateBrowserSession", "Rotate a legacy bearer into a cookie without extending expiry", tagAuth, true, jsonResp("OK", ref("BrowserSessionResponse")), nil)),
		"/v1/auth/logout", obj("post", op204("logout", "Revoke the calling session", tagAuth, true)),
		"/v1/account/password", obj("post", passwordChange),
		"/v1/auth/refresh", obj("post", op("refreshToken", "Renew the calling session token (rotates the credential, extends expiry)", tagAuth, true,
			jsonResp("OK", ref("SessionResponse")), nil)),
		"/v1/auth/whoami", obj("get", op("whoami", "The calling principal and its tenant grants", tagAuth, true,
			jsonResp("OK", ref("WhoamiResponse")), nil)),
		"/v1/auth/capabilities", obj("post", capabilityOperation(op("authCapabilities",
			"Project the CALLING credential's authority over registered operations", tagAuth, true,
			jsonResp("OK", ref("CapabilityResults")),
			body(ref("CapabilityQuestions")), tenantParam),
			jsonResp("Common authorization evidence is unavailable; no target was resolved", ref("Error")))),
		"/v1/auth/effective-rights", obj("get", op("getEffectiveRights",
			"One trustee's effective rights over one node: the subject, the node, the assurance the answer holds, the lineage path and each right's state. Needs authz:admin; answers 404 while the operator has the AuthZEN search surface disabled and 403 outside its permitted network",
			tagAuth, true,
			jsonResp("OK", ref("EffectiveRights")), nil, tenantParam,
			obj("name", "subject_type", "in", "query", "required", true,
				"description", "The subject to project: a user or an API token (never an email — an id, so the 404 is not an account oracle).",
				"schema", obj("type", "string", "enum", arr("user", "token"))),
			obj("name", "subject_id", "in", "query", "required", true,
				"description", "The subject's identifier (UUIDv7).",
				"schema", obj("type", "string", "format", "uuid")),
			obj("name", "kind", "in", "query", "required", true,
				"description", "The node's kind.",
				"schema", obj("type", "string", "enum", arr("agent", "session", "resource"))),
			obj("name", "id", "in", "query", "required", true,
				"description", "The node's identifier (UUIDv7).",
				"schema", obj("type", "string", "format", "uuid")))),
		"/v1/auth/webauthn/register/options", obj("post", op("webauthnRegisterOptions",
			"Issue WebAuthn creation options (challenge) to register a new authenticator for the calling session's user",
			tagAuth, true,
			jsonResp("OK", ref("WebAuthnCeremonyOptions")), nil)),
		"/v1/auth/webauthn/register", obj("post", op("webauthnRegister",
			"Verify the browser's attestation and persist the credential (403 on any ceremony failure, 409 on an already-registered credential id)",
			tagAuth, true,
			jsonResp("OK", obj("type", "object", "properties", obj("ok", obj("type", "boolean")), "required", arr("ok"))),
			body(ref("WebAuthnCredentialInput")))),
		"/v1/auth/webauthn/authenticate/options", obj("post", op("webauthnAuthenticateOptions",
			"Issue WebAuthn assertion options (challenge) for a step-up of the calling session",
			tagAuth, true,
			jsonResp("OK", ref("WebAuthnCeremonyOptions")), nil)),
		"/v1/auth/webauthn/authenticate", obj("post", op("webauthnAuthenticate",
			"Verify the browser's assertion and elevate the calling session to AAL3",
			tagAuth, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"ok", obj("type", "boolean"),
				"aal", obj("type", "integer", "description", "The session's authenticator assurance level after the step-up."),
			), "required", arr("ok", "aal"))),
			body(ref("WebAuthnCredentialInput")))),
		"/v1/auth/webauthn/credentials", obj("get", op("listWebAuthnCredentials",
			"The calling user's registered authenticators — id, label and registration time only, never key material",
			tagAuth, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"items", obj("type", "array", "items", ref("WebAuthnCredential")),
			), "required", arr("items"))), nil)),
		"/v1/auth/webauthn/credentials/{id}", obj(
			"patch", op("renameWebAuthnCredential",
				"Update the display name of one of the calling user's authenticators (owner-only, no step-up: a metadata change)",
				tagAuth, true,
				jsonResp("OK", obj("type", "object", "properties", obj("ok", obj("type", "boolean")), "required", arr("ok"))),
				body(obj("type", "object", "additionalProperties", false,
					"required", arr("name"),
					"properties", obj("name", obj("type", "string", "minLength", 1)))), idParam),
			"delete", op204("deleteWebAuthnCredential",
				"Unregister one of the calling user's authenticators (lost/stolen-key remediation; step-up required)",
				tagAuth, true, idParam)),
		"/v1/auth/piv/status", obj("get", op("getPIVStatus",
			"The calling session's presented PIV smart-card certificate status (501 when no PIV verifier roots are configured)",
			tagAuth, true,
			jsonResp("OK", ref("PIVStatus")), nil)),
		"/v1/auth/piv/elevate", obj("post", op("elevatePIV",
			"Verify the presented PIV certificate (chain, OCSP, user binding) and elevate the calling session to AAL3 (method piv)",
			tagAuth, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"ok", obj("type", "boolean"),
				"aal", obj("type", "integer", "description", "The session's authenticator assurance level after the elevation."),
			), "required", arr("ok", "aal"))), nil)),

		// ── Agents ─────────────────────────────────────────────────────
		"/v1/agents", obj(
			"get", op("listAgents", "List agents in the resolved tenant", tagAgents, true,
				jsonResp("OK", listOf(ref("Agent"))),
				nil, tenantParam, limitParam, cursorParam,
				obj("name", "workspace_id", "in", "query", "required", false,
					"description", "Filter agents by workspace. A workspace-confined caller remains limited to its assigned workspace.",
					"schema", obj("type", "string", "format", "uuid"))),
			"post", op201("createAgent", "Create an agent", tagAgents, true,
				jsonResp("Created", ref("Agent")),
				body(ref("AgentInput")), tenantParam)),
		"/v1/agents/{id}", obj(
			"get", op("getAgent", "Get an agent by ID", tagAgents, true,
				jsonResp("OK", ref("Agent")),
				nil, idParam, tenantParam),
			"patch", op("updateAgent", "Update an agent", tagAgents, true,
				jsonResp("OK", ref("Agent")),
				body(ref("AgentInput")), idParam, tenantParam),
			"delete", op204("deleteAgent", "Delete an agent", tagAgents, true, idParam, tenantParam)),

		// ── Agent groups (S256: groups as an authorization subject) ───
		"/v1/agent-groups", obj(
			"get", op("listAgentGroups", "List agent groups in the resolved tenant", tagDirectory, true,
				jsonResp("OK", listOf(ref("AgentGroup"))),
				nil, tenantParam, limitParam, cursorParam,
				obj("name", "workspace_id", "in", "query", "required", false,
					"description", "Filter groups by workspace. A workspace-confined caller remains limited to its assigned workspace.",
					"schema", obj("type", "string", "format", "uuid"))),
			"post", op201("createAgentGroup", "Create an agent group (name, slug and optional workspace scope)", tagDirectory, true,
				jsonResp("Created", ref("AgentGroup")),
				body(ref("AgentGroupInput")), tenantParam)),
		"/v1/agent-groups/{id}", obj(
			"get", op("getAgentGroup", "Get an agent group by ID", tagDirectory, true,
				jsonResp("OK", ref("AgentGroup")),
				nil, idParam, tenantParam),
			"patch", op("updateAgentGroup", "Update an agent group; only fields present in the request are touched", tagDirectory, true,
				jsonResp("OK", ref("AgentGroup")),
				body(ref("AgentGroupPatch")), idParam, tenantParam),
			"delete", op204("deleteAgentGroup", "Delete a group and its roster (the membership rows), never the member agents themselves", tagDirectory, true, idParam, tenantParam)),
		"/v1/agent-groups/{id}/members", obj("get", op("listAgentGroupMembers", "List the agents that are members of one group", tagDirectory, true,
			jsonResp("OK", listOf(ref("AgentGroupMember"))),
			nil, idParam, tenantParam, limitParam, cursorParam)),
		"/v1/agent-groups/{id}/members/{agentID}", obj(
			"put", func() map[string]any {
				// The handler answers 200 for an already-member (idempotent) and
				// 201 for a fresh add; the contract says both, not one average.
				o := op("addAgentGroupMember", "Add an agent to a group. Idempotent: 200 with the existing row when already a member, 201 for a fresh add",
					tagDirectory, true,
					jsonResp("OK", ref("AgentGroupMember")),
					nil, idParam, agentIDParam, tenantParam)
				o["responses"].(map[string]any)["201"] = jsonResp("Created", ref("AgentGroupMember"))
				return o
			}(),
			"delete", op204("removeAgentGroupMember", "Remove an agent from a group (404 when the agent is not a member)", tagDirectory, true, idParam, agentIDParam, tenantParam)),

		// ── Access edges ───────────────────────────────────────────────
		"/v1/access-edges", obj("get", op("listAccessEdges", "List access edges (R/RW map); self-audited", tagAgents, true,
			jsonResp("OK", listOf(ref("AccessEdge"))),
			nil, tenantParam, limitParam, cursorParam)),

		// ── Audit ──────────────────────────────────────────────────────
		"/v1/audit", obj("get", op("listAuditEvents", "Read the tenant evidence ledger", tagAudit, true,
			jsonResp("OK", auditListOf(ref("AuditEvent"))),
			nil, tenantParam, auditFromParam, limitParam, auditExcludeActionParam)),
		// The system route runs the SAME handler, so it has always accepted ?from and ?limit
		// and the console has always sent them (web/src/features/audit/audit-view.tsx).
		// Declaring them is a correction, not a widening: publishing no parameters made the
		// generated client type this operation's query as `never`, so the contract called
		// impossible a request the engine answers — and head_seq, whose whole use is to be
		// paired with ?from, turned that contradiction into a load-bearing one.
		"/v1/audit/system", obj("get", op("listSystemAuditEvents", "Read the system-tenant evidence ledger (cross-tenant ops; superadmin only)", tagAudit, true,
			jsonResp("OK", auditListOf(ref("AuditEvent"))), nil, auditFromParam, limitParam, auditExcludeActionParam)),
		// The declared shape mirrors handleAuditVerify 1:1. It previously advertised
		// five fields (valid/events_checked/checkpoints_checked/first_seq/last_seq)
		// that the handler has never returned — a published contract describing a
		// response nobody sends.
		"/v1/audit/verify", obj("get", op("verifyAuditChain", "Verify the chain and its signed checkpoints", tagAudit, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"ok", obj("type", "boolean",
					"description", "The overall verdict: the structural chain verified over at least one link, and the checkpoints are not in a failed state. A ledger with nothing attested yet is still ok."),
				"chain", obj("type", "object", "properties", obj(
					"ok", obj("type", "boolean"),
					"checked", obj("type", "integer", "description", "Links walked."),
					"break_at", obj("type", "integer", "description", "Sequence of the first broken link, 0 when intact."),
					"reason", obj("type", "string"),
				)),
				"checkpoints", obj("type", "object", "properties", obj(
					"ok", obj("type", "boolean",
						"description", "Strict: true only once at least one checkpoint exists AND every signature and link verified. It is false BOTH for an unattested ledger and for a tampered one — read `status` to tell them apart."),
					"status", obj("type", "string", "enum", arr("ok", "failed", "pending"),
						"description", "The three answers (core/audit CheckpointStatus): verified, verified BAD, or `pending` — nothing attested yet, which is NOT a failure and must not be rendered as one."),
					"count", obj("type", "integer", "description", "Signed checkpoints found."),
					"latest_attested_seq", obj("type", "integer", "description", "Highest sequence a valid checkpoint attests."),
					"first_bad_seq", obj("type", "integer", "description", "Sequence of the first checkpoint that failed verification, 0 when none."),
					"reason", obj("type", "string", "description", "\"no-checkpoints\" for the empty case, else the first failure."),
				)),
			))), nil, tenantParam)),
		"/v1/audit/recent", obj("get", op("listRecentAuditEvents", "The newest ledger events, newest first, without audit reads (not itself recorded)", tagAudit, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"items", obj("type", "array", "items", ref("AuditEvent")),
				"head_seq", obj("type", "integer", "description", "The ledger head the events were read at; 0 when the ledger is empty."),
			), "required", arr("items", "head_seq"))),
			nil, tenantParam,
			obj("name", "limit", "in", "query", "required", false,
				"description", "How many events, 1 to 50 (default 10).",
				"schema", obj("type", "integer", "minimum", 1, "maximum", 50, "default", 10)))),
		"/v1/audit/export", obj("get", func() map[string]any {
			o := rawOp("exportAuditLedger", "Export the ledger ("+audit.FormatList()+")", tagAudit, true,
				"Exported ledger stream (format-dependent text or NDJSON)",
				"text/plain", "application/x-ndjson")
			o["parameters"] = arr(tenantParam,
				obj("name", "format", "in", "query", "required", false,
					"description", "Export format.",
					"schema", obj("type", "string", "enum", auditFormatEnum(), "default", "cef")))
			return o
		}()),
		"/v1/audit/pubkey", obj("get", op("getAuditPubkey", "The Ed25519 checkpoint verification key (PEM)", tagAudit, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"algorithm", obj("type", "string"),
				"public_key", obj("type", "string"),
			))), nil, tenantParam)),

		// ── Users ──────────────────────────────────────────────────────
		"/v1/users", obj(
			"get", op("listUsers", "List users (superadmin)", tagUsers, true,
				jsonResp("OK", listOf(ref("User"))),
				nil, limitParam, cursorParam),
			"post", op201("createUser", "Create a user (superadmin)", tagUsers, true,
				jsonResp("Created", ref("CreatedUser")),
				body(ref("CreateUserInput")))),
		"/v1/users/superadmins", obj(
			"get", op("listSuperadmins", "List superadmin accounts and their active/inactive status (superadmin)", tagUsers, true,
				jsonResp("OK", listOf(ref("User"))), nil)),
		"/v1/users/{id}/disable", obj(
			"post", op204("disableSuperadmin", "Disable a superadmin account — non-destructive, reversible (superadmin, AAL3 step-up)", tagUsers, true, idParam)),
		"/v1/users/{id}/enable", obj(
			"post", op204("enableSuperadmin", "Enable a previously disabled superadmin account (superadmin, AAL3 step-up)", tagUsers, true, idParam)),

		// ── Tokens ─────────────────────────────────────────────────────
		"/v1/tokens", obj(
			"get", op("listTokens", "List API tokens visible to the caller", tagTokens, true,
				jsonResp("OK", listOf(ref("Token"))), nil,
				obj("name", "limit", "in", "query", "required", false,
					"description", "Maximum number of items to return.",
					"schema", obj("type", "integer", "minimum", 1, "maximum", 1000, "default", 100)),
				cursorParam,
				obj("name", "include_revoked", "in", "query", "required", false,
					"description", "Include revoked tokens when true. Revoked tokens are excluded by default.",
					"schema", obj("type", "boolean", "default", false))),
			"post", op201("issueToken", "Issue an API token", tagTokens, true,
				jsonResp("Created", obj("type", "object", "properties", obj(
					"token", obj("type", "string", "description", "The opaque API key (olvk_…). Shown only once."),
					"id", obj("type", "string", "format", "uuid"),
					"name", obj("type", "string"),
				))),
				body(ref("IssueTokenInput")))),
		"/v1/tokens/{id}", obj(
			"delete", op204("revokeToken", "Revoke an API token", tagTokens, true, idParam)),
		// SUBPATH, not the parent (corrected 2026-08-05). This operation was declared
		// under "/v1/tokens/{id}", where the router registers only DELETE, so every
		// generated client POSTed to a URL chi never routes and got a bare 405.
		"/v1/tokens/{id}/rotate", obj(
			"post", op("rotateToken", "Rotate an API token (issue new secret, revoke old)", tagTokens, true,
				jsonResp("OK", obj("type", "object", "properties", obj(
					"token", obj("type", "string", "description", "The new opaque API key (olvk_…). Shown only once."),
					"id", obj("type", "string", "format", "uuid"),
				))), nil, idParam)),

		// ── Memberships ────────────────────────────────────────────────
		"/v1/memberships", obj("post", consentRequired(op201("grantMembership", "Grant a user a role in a tenant", tagSystem, true,
			jsonResp("Created", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"user_id", obj("type", "string", "format", "uuid"),
				"tenant", obj("type", "string"),
				"role", obj("type", "string"),
			))),
			body(ref("GrantMembershipInput"))))),
		"/v1/members", obj("get", op("listMembers", "List the resolved tenant's member roster (role, workspace scoping, groups)", tagSystem, true,
			jsonResp("OK", listOf(ref("RosterMember"))),
			nil, tenantParam)),

		// ── Directory: provisioned groups, invitations, onboarding ────
		"/v1/groups", obj("get", op("listGroups",
			"List the tenant's provisioned groups with their mapped roles and member counts — the operator's view of what the IdP pushed and what each group confers",
			tagDirectory, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"groups", obj("type", "array", "items", ref("DirectoryGroup")),
			), "required", arr("groups"))), nil, tenantParam)),
		"/v1/groups/{id}/role", obj("put", op("setGroupRole",
			"Set (or clear, with an empty role) the role a group's members are elevated to in the group's tenant; ceiling-checked against the caller's authority",
			tagDirectory, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"display_name", obj("type", "string"),
				"mapped_role", obj("type", "string"),
			), "required", arr("id", "display_name", "mapped_role"))),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("role"),
				"properties", obj("role", obj("type", "string",
					"description", "The role the group's members are elevated to; an empty string clears the mapping.")))),
			idParam, tenantParam)),
		"/v1/groups/{id}/parent", obj("put", op("setGroupParent",
			"Nest (or, with an empty parent_id, un-nest) a group under another group of the same tenant; a member of the child is then also a member of the parent for authorization (409 on a cycle)",
			tagDirectory, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"display_name", obj("type", "string"),
				"parent_group_id", obj("type", "string", "description", "The parent group's id; empty when the group is top-level."),
			), "required", arr("id", "display_name", "parent_group_id"))),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("parent_id"),
				"properties", obj("parent_id", obj("type", "string", "format", "uuid",
					"description", "The parent group; an empty string un-nests the group.")))),
			idParam, tenantParam)),
		"/v1/groups/{id}/workspace", obj("put", op("setGroupWorkspace",
			"Place a user group in a workspace of the same tenant, or clear its place with an empty workspace_id; membership and authorization are unchanged",
			tagDirectory, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"display_name", obj("type", "string"),
				"workspace_id", obj("type", "string", "description", "The workspace's id; empty when the group is unplaced."),
			), "required", arr("id", "display_name", "workspace_id"))),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("workspace_id"),
				"properties", obj("workspace_id", obj("type", "string",
					"description", "The workspace of the same tenant; an empty string clears the place.")))),
			idParam, tenantParam)),
		"/v1/invites", obj("get", op("listInvites",
			"List the tenant's pending (unaccepted, unexpired) invitations, without any token material",
			tagDirectory, true,
			jsonResp("OK", listOf(ref("Invite"))), nil, tenantParam)),
		"/v1/invites/accept", obj("post", op("acceptInvite",
			"Redeem an invitation token: set the password, activate the account and mint a session (the single-use token is the gate; no authentication)",
			tagAuth, false,
			jsonResp("OK", ref("SessionResponse")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("token", "password"),
				"properties", obj(
					"token", obj("type", "string", "writeOnly", true),
					"password", obj("type", "string", "format", "password", "minLength", 12, "writeOnly", true)))))),
		"/v1/invites/{id}", obj("delete", op204("revokeInvite", "Delete a pending invitation", tagDirectory, true, idParam, tenantParam)),
		"/v1/invites/{id}/resend", obj("post", op202("resendInvite",
			"Rotate a pending invitation's secret and mail the new link to the invitee (409 invite_delivery_unavailable without a mailer)",
			tagDirectory, true,
			jsonResp("Accepted", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"expires_at", obj("type", "string", "format", "date-time"),
				"delivery", obj("type", "string", "enum", arr("sent", "failed"),
					"description", "Whether the invitation email left the engine; the token travels only in the mail."),
			), "required", arr("id", "expires_at", "delivery"))),
			nil, idParam, tenantParam)),
		"/v1/onboard", obj("post", consentRequired(op201("onboardMember",
			"Create-or-reuse an account and grant its tenant membership (membership:write, AAL3 step-up; mode invite emails a single-use token)",
			tagDirectory, true,
			jsonResp("Created", ref("OnboardResult")),
			body(ref("OnboardInput")), tenantParam))),

		// ── Federated console search ────────────────────────────
		"/v1/search", obj("get", op("searchConsole",
			"Federated console search: fan out to every searchable kind, deny-closed per kind on its own read permission",
			tagSystem, true,
			jsonResp("OK", ref("SearchResponse")),
			nil, tenantParam,
			obj("name", "q", "in", "query", "required", true, "schema", obj("type", "string", "maxLength", 100)))),

		// ── Workspaces ─────────────────────────────────────────────────
		"/v1/workspaces", obj(
			"get", op("listWorkspaces", "List workspaces in the resolved tenant", tagWorkspaces, true,
				jsonResp("OK", listOf(ref("Workspace"))),
				nil, tenantParam, limitParam, cursorParam),
			"post", op201("createWorkspace", "Create a workspace (tenant admin, AAL3 step-up)", tagWorkspaces, true,
				jsonResp("Created", ref("Workspace")),
				body(ref("CreateWorkspaceInput")), tenantParam)),
		"/v1/workspaces/{id}", obj(
			"get", op("getWorkspace", "Get a workspace by ID", tagWorkspaces, true,
				jsonResp("OK", ref("Workspace")),
				nil, idParam, tenantParam),
			"patch", op("updateWorkspace", "Update a workspace", tagWorkspaces, true,
				jsonResp("OK", ref("Workspace")),
				body(ref("UpdateWorkspaceInput")), idParam, tenantParam)),
		"/v1/workspaces/{id}/parent", obj("put", op("setWorkspaceParent",
			"Place a workspace (a department) under another workspace of the same tenant, or make it a root with an empty parent_id; the subtree moves with it (owner, AAL3 step-up)",
			tagWorkspaces, true,
			jsonResp("OK", ref("Workspace")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("parent_id"),
				"properties", obj("parent_id", obj("type", "string",
					"description", "The parent workspace of the same tenant; an empty string makes the workspace a root.")))),
			idParam, tenantParam)),
		"/v1/workspaces/{id}/summary", obj("get", op("getWorkspaceSummary",
			"A workspace with counts of its scoped entities; a *_capped count is a FLOOR (at least N), never a total",
			tagWorkspaces, true,
			jsonResp("OK", ref("WorkspaceSummary")),
			nil, idParam, tenantParam)),
		"/v1/workspaces/{id}/contents", obj("get", op("getWorkspaceContents",
			"Every kind that declares workspace lineage, counted in the workspace and sorted by kind; a capped count is a floor (501 when the census is not wired)",
			tagWorkspaces, true,
			jsonResp("OK", ref("WorkspaceContents")),
			nil, idParam, tenantParam)),

		// ── System (orgs) ──────────────────────────────────────────────
		"/v1/system/tracing", obj(
			"get", op("getTracingSettings", "Read saved and effective tracing settings (superadmin)", tagSystem, true,
				jsonResp("OK", ref("TracingStatus")), nil),
			"put", op("saveTracingSettings", "Save and apply tracing settings (superadmin, configured step-up)", tagSystem, true,
				jsonResp("OK", ref("TracingStatus")), body(ref("TracingSettings")))),
		"/v1/system/residency", obj(
			"get", op("getResidencyRegistry", "Get the configured data-residency registry (superadmin)", tagSystem, true,
				jsonResp("OK", ref("ResidencyRegistry")), nil)),
		"/v1/system/orgs", obj(
			"get", op("listOrgs", "List tenant orgs (superadmin)", tagSystem, true,
				jsonResp("OK", listOf(ref("Org"))), nil),
			"post", op201("createOrg", "Provision a tenant (superadmin)", tagSystem, true,
				jsonResp("Created", ref("Org")),
				body(ref("CreateOrgInput")))),
		"/v1/system/orgs/{tenant_id}", obj(
			"delete", op204("dropOrg", "Hard-delete a tenant org after the cloud grace period (superadmin)", tagSystem, true, tenantPathParam)),
		"/v1/system/orgs/{tenant_id}/region", obj(
			"put", op("setOrgRegion", "Set or clear a tenant residency pin (superadmin, AAL3 step-up)", tagSystem, true,
				jsonResp("OK", ref("Org")),
				body(ref("SetOrgRegionInput")), tenantPathParam)),
		"/v1/system/orgs/{tenant_id}/status", obj(
			"put", op("setOrgStatus", "Withdraw or restore a tenant's service without deleting its data (superadmin)", tagSystem, true,
				jsonResp("OK", ref("Org")),
				body(ref("SetOrgStatusInput")), tenantPathParam)),

		// ── Connectors ─────────────────────────────────────────────────
		"/v1/connectors/health", obj("get", op("getConnectorHealth", "Per-connector health metrics and fleet summary", tagConnectors, true,
			jsonResp("OK", ref("ConnectorHealthResponse")),
			nil, tenantParam)),

		// ── Console admin ──────────────────────────────────────────────
		"/v1/console/setup-status", obj("get", op("getSetupStatus", "First-run setup wizard progress", tagConsole, true,
			jsonResp("OK", ref("SetupStatus")), nil)),
		"/v1/console/health-summary", obj("get", op("getHealthSummary", "Operational health summary for the console dashboard", tagConsole, true,
			jsonResp("OK", ref("HealthSummary")), nil)),
		"/v1/console/keys", obj("get", op("getKeyCustody", "Non-secret signing-key and sealer custody inventory", tagConsole, true,
			jsonResp("OK", ref("KeyCustody")), nil)),
		"/v1/console/bus", obj("get", op("getBusSnapshot", "Event-bus subscriber, saturation, loss, and optional bridge snapshot", tagConsole, true,
			jsonResp("OK", ref("BusSnapshot")), nil)),
		"/v1/console/config/effective", obj("get", op("getEffectiveConfig", "Live effective configuration with secret-bearing values redacted", tagConsole, true,
			jsonResp("OK", ref("EffectiveConfigResponse")), nil)),
		"/v1/console/support-bundle", obj("post", rawOp("createSupportBundle", "Build and download a redacted support bundle (AAL3 step-up)", tagConsole, true,
			"Redacted tar.gz support bundle", "application/octet-stream")),
		"/v1/console/update-check", obj("post", op("refreshUpdateStatus", "Check the configured signed update channel now", tagConsole, true,
			jsonResp("OK", ref("UpdateStatus")), nil)),
		"/v1/console/secrets", obj(
			"get", op("listSecrets", "List sealed secrets (names and hints, never values)", tagConsole, true,
				jsonResp("OK", ref("SecretsList")), nil),
			"put", op("putSecret", "Create or update a sealed secret", tagConsole, true,
				jsonResp("OK", obj("type", "object", "properties", obj("name", obj("type", "string"), "action", obj("type", "string")))),
				body(ref("SecretInput"))),
			"delete", op204("deleteSecret", "Delete a sealed secret", tagConsole, true)),
		"/v1/console/license", obj(
			"get", op("getLicenseStatus", "Current license status and entitlements", tagConsole, true,
				jsonResp("OK", ref("LicenseStatus")), nil),
			"post", op("installLicense", "Install a commercial license", tagConsole, true,
				jsonResp("OK", ref("LicenseStatus")),
				body(obj("type", "object", "properties", obj(
					"license", obj("type", "string", "description", "Base64-encoded license blob"),
					"acknowledge", obj("type", "boolean"),
				)))),
			"delete", op204("uninstallLicense", "Remove the installed license (revert to community)", tagConsole, true)),
		"/v1/console/connectors", obj(
			"get", op("listConnectorCatalog", "List available connector types with their configuration fields", tagConsole, true,
				jsonResp("OK", obj("type", "object", "properties", obj(
					"connectors", obj("type", "array", "items", ref("ConnectorInfo")),
				))), nil),
			"put", op("putConnector", "Onboard or update a connector instance", tagConsole, true,
				jsonResp("OK", ref("ConnectorApplyResult")),
				body(ref("ConnectorOnboardInput"))),
			"delete", op("deleteConnector", "Remove a connector instance", tagConsole, true,
				jsonResp("OK", ref("ConnectorApplyResult")),
				body(ref("ConnectorOnboardInput")))),
		// SUBPATH, not the parent (corrected 2026-08-05): chi registers POST on
		// /console/connectors/test, so declaring it on the parent sent every generated
		// client to a URL the router never matches.
		"/v1/console/connectors/test", obj(
			"post", op("testConnector", "Test connectivity for a connector configuration", tagConsole, true,
				jsonResp("OK", obj("type", "object", "properties", obj(
					"success", obj("type", "boolean"),
					"message", obj("type", "string"),
				))),
				body(ref("ConnectorOnboardInput")))),
		"/v1/console/sources", obj(
			"get", op("listSources", "List configured source connectors and their status", tagConsole, true,
				jsonResp("OK", obj("type", "object", "properties", obj(
					"sources", obj("type", "array", "items", ref("SourceRosterEntry")),
				))), nil),
			"put", op("putSource", "Create or update a source connector", tagConsole, true,
				jsonResp("OK", ref("SourceApplyResult")),
				body(ref("SourceRosterInput"))),
			"delete", op("deleteSource", "Remove a source connector", tagConsole, true,
				jsonResp("OK", ref("SourceApplyResult")),
				body(ref("SourceRosterInput")))),
		"/v1/console/sso", obj(
			"get", op("getSSOConfig", "Current SSO/IdP configuration", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")), nil),
			"put", op("putSSOConfig", "Create or update SSO/IdP configuration", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")),
				body(ref("SSOConfigInput"))),
			"delete", op204("deleteSSOConfig", "Remove SSO/IdP configuration", tagConsole, true)),
		// SUBPATH, not the parent (corrected 2026-08-05): chi registers POST on
		// /console/sso/test.
		"/v1/console/sso/test", obj(
			"post", op("testSSOConfig", "Test SSO/IdP connectivity", tagConsole, true,
				jsonResp("OK", obj("type", "object", "properties", obj(
					"success", obj("type", "boolean"),
					"message", obj("type", "string"),
				))),
				body(ref("SSOConfigInput")))),
		// The multi-IdP subtree (U4) over the SAME global scope the base
		// /console/sso routes govern: additional IdPs by alias, default first.
		"/v1/console/sso/idps", obj("get", op("listSSOIdPs",
			"List every IdP configured under the deployment-wide global scope, default first; no secrets, only hints",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"idps", obj("type", "array", "items", ref("SSOConfig")),
			), "required", arr("idps"))), nil)),
		"/v1/console/sso/idps/{alias}", obj(
			"get", op("getSSOIdP", "Get one additional IdP's configuration by alias", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")), nil, aliasParam),
			"put", op("putSSOIdP", "Create or update one additional IdP by alias (AAL3 step-up)", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")),
				body(ref("SSOConfigInput")), aliasParam),
			"delete", op204("deleteSSOIdP", "Remove one additional IdP by alias (AAL3 step-up)", tagConsole, true, aliasParam)),
		"/v1/console/sso/idps/{alias}/test", obj("post", op("testSSOIdP",
			"Validate a candidate additional-IdP config (OIDC discovery / SAML metadata fetch) without persisting it; 501 when no SSO provider service is wired",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj("ok", obj("type", "boolean")), "required", arr("ok"))),
			body(ref("SSOConfigInput")), aliasParam)),
		// The SAME SSO admin surface scoped to one tenant's IdPs (U6): the
		// base routes operate the scope's primary ("default") IdP; the /idps
		// subtree below it is the per-tenant multi-IdP surface.
		"/v1/console/sso/tenants/{tenant}", obj(
			"get", op("getTenantSSOConfig", "Get the tenant's primary SSO/IdP configuration", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")), nil, ssoTenantParam),
			"put", op("putTenantSSOConfig", "Create or update the tenant's primary SSO/IdP configuration (AAL3 step-up)", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")),
				body(ref("SSOConfigInput")), ssoTenantParam),
			"delete", op204("deleteTenantSSOConfig", "Remove the tenant's primary SSO/IdP configuration (AAL3 step-up)", tagConsole, true, ssoTenantParam)),
		"/v1/console/sso/tenants/{tenant}/test", obj("post", op("testTenantSSOConfig",
			"Test the tenant's primary SSO/IdP connectivity (AAL3 step-up)",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj("ok", obj("type", "boolean")), "required", arr("ok"))),
			body(ref("SSOConfigInput")), ssoTenantParam)),
		"/v1/console/sso/tenants/{tenant}/idps", obj("get", op("listTenantSSOIdPs",
			"List every IdP configured under the tenant's scope, default first; no secrets, only hints",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"idps", obj("type", "array", "items", ref("SSOConfig")),
			), "required", arr("idps"))), nil, ssoTenantParam)),
		"/v1/console/sso/tenants/{tenant}/idps/{alias}", obj(
			"get", op("getTenantSSOIdP", "Get one of the tenant's additional IdPs by alias", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")), nil, ssoTenantParam, aliasParam),
			"put", op("putTenantSSOIdP", "Create or update one of the tenant's additional IdPs by alias (AAL3 step-up)", tagConsole, true,
				jsonResp("OK", ref("SSOConfig")),
				body(ref("SSOConfigInput")), ssoTenantParam, aliasParam),
			"delete", op204("deleteTenantSSOIdP", "Remove one of the tenant's additional IdPs by alias (AAL3 step-up)", tagConsole, true, ssoTenantParam, aliasParam)),
		"/v1/console/sso/tenants/{tenant}/idps/{alias}/test", obj("post", op("testTenantSSOIdP",
			"Validate a candidate tenant-scoped IdP config without persisting it; 501 when no SSO provider service is wired",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj("ok", obj("type", "boolean")), "required", arr("ok"))),
			body(ref("SSOConfigInput")), ssoTenantParam, aliasParam)),
		// Live reconfiguration of the source roster: reconcile the whole
		// roster and re-resolve the license without a restart.
		"/v1/console/runtime/reload", obj("post", op("reloadRuntime",
			"Reconcile the durable source roster against the running engine and re-resolve the license (AAL3 step-up); the report names what applied and what needs a restart",
			tagConsole, true,
			jsonResp("OK", ref("SourceReloadReport")), nil)),
		"/v1/console/sources/diff", obj("get", op("getSourceContentDiff",
			"A bounded content diff between two refs of one repository owned by a configured source (501 when no git-host diff reader is wired; 422 when the diff exceeds the read cap)",
			tagConsole, true,
			jsonResp("OK", ref("GitHostDiff")), nil,
			obj("name", "source", "in", "query", "required", true,
				"description", "The configured source's name.", "schema", obj("type", "string", "minLength", 1)),
			obj("name", "host", "in", "query", "required", true,
				"description", "The git host.", "schema", obj("type", "string", "enum", arr("github", "gitlab"))),
			obj("name", "repository", "in", "query", "required", true,
				"description", "The repository (owner/name).", "schema", obj("type", "string", "minLength", 1)),
			obj("name", "base", "in", "query", "required", true,
				"description", "The base ref.", "schema", obj("type", "string", "minLength", 1)),
			obj("name", "head", "in", "query", "required", true,
				"description", "The head ref.", "schema", obj("type", "string", "minLength", 1)))),
		// ── Disaster recovery console: backup, restore, schedule ──
		"/v1/console/dr/backup", obj("post", op202("triggerBackup",
			"Start an encrypted disaster-recovery backup and return its job id (501 without the DR service)",
			tagConsole, true,
			jsonResp("Accepted: the backup job started; follow it at /v1/console/dr/jobs/{job_id}/stream", obj("type", "object", "properties", obj(
				"job_id", obj("type", "string"),
			), "required", arr("job_id"))),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("passphrase"),
				"properties", obj(
					"passphrase", obj("type", "string", "writeOnly", true,
						"description", "Passphrase encrypting the backup keys; must meet the documented floor."),
					"notes", obj("type", "string")))))),
		"/v1/console/dr/backups", obj("get", op("listBackups",
			"List the backup directory's .drbundle files, newest first, with each bundle's manifest summary",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"items", obj("type", "array", "items", ref("DRBackup")),
			), "required", arr("items"))), nil)),
		"/v1/console/dr/backups/{id}", obj(
			"get", op("getBackup", "One backup's manifest and size by bundle id", tagConsole, true,
				jsonResp("OK", ref("DRBackupDetail")),
				nil, drIDParam("id", "The backup's bundle id (its filename).")),
			"delete", op204("deleteBackup", "Delete one backup bundle by id", tagConsole, true,
				drIDParam("id", "The backup's bundle id (its filename)."))),
		"/v1/console/dr/backups/{id}/download", obj("get", func() map[string]any {
			o := rawOp("downloadBackup",
				"Download one backup bundle's verbatim bytes (.drbundle)",
				tagConsole, true, "The encrypted DR bundle", "application/octet-stream")
			o["parameters"] = arr(drIDParam("id", "The backup's bundle id (its filename)."))
			return o
		}()),
		"/v1/console/dr/restore/upload", obj("post", func() map[string]any {
			o := op("uploadRestore",
				"Upload a raw .drbundle file for restore pre-flight (the body is the file's verbatim bytes, at most 10 GiB); the manifest is inspected and returned with the upload id",
				tagConsole, true,
				jsonResp("OK", ref("DRRestoreUpload")), nil)
			o["requestBody"] = obj("required", true, "content",
				obj("application/octet-stream", obj("schema", obj("type", "string", "format", "binary"))))
			return o
		}()),
		"/v1/console/dr/restore/{id}/apply", obj("post", op202("applyRestore",
			"Apply an uploaded bundle: with the dual-control gate armed this records an intent for a second administrator to approve (request_id); otherwise it starts the restore job (job_id)",
			tagConsole, true,
			jsonResp("Accepted", ref("DRRestoreApply")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("passphrase"),
				"properties", obj("passphrase", obj("type", "string", "writeOnly", true,
					"description", "Passphrase decrypting the backup keys (the single-actor path).")))),
			drIDParam("id", "The id returned by the upload."))),
		"/v1/console/dr/restore/{id}/approve", obj("post", op202("approveRestore",
			"Approve a dual-control restore as a DISTINCT administrator account (403 when the requester self-approves)",
			tagConsole, true,
			jsonResp("Accepted", ref("DRRestoreApply")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("request_id", "passphrase"),
				"properties", obj(
					"request_id", obj("type", "string"),
					"passphrase", obj("type", "string", "writeOnly", true)))),
			drIDParam("id", "The id returned by the upload."))),
		"/v1/console/dr/restore/pending", obj("get", op("listPendingRestores",
			"Restore requests awaiting a second approver, newest first, so a distinct admin can find and approve one",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"items", obj("type", "array", "items", ref("DRPendingRestore")),
			), "required", arr("items"))), nil)),
		"/v1/console/dr/jobs", obj("get", op("listDRJobs",
			"The DR job list (backup and restore) with phase and progress",
			tagConsole, true,
			jsonResp("OK", obj("type", "object", "properties", obj(
				"items", obj("type", "array", "items", ref("DRJob")),
			), "required", arr("items"))), nil)),
		"/v1/console/dr/jobs/{id}/stream", obj("get", rawOp("streamDRJob",
			"Server-Sent Events stream of one DR job's progress: event: job frames with the DRJob payload, heartbeats as comments, until the job completes or fails",
			tagConsole, true, "SSE stream of DR job progress", "text/event-stream"),
			drIDParam("id", "The job id.")),
		"/v1/console/dr/schedule", obj(
			"get", op("getDRSchedule", "The scheduled-backup configuration and its last run's outcome", tagConsole, true,
				jsonResp("OK", ref("DRSchedule")), nil),
			"put", op("putDRSchedule",
				"Save the schedule (enabled, cron, retention); the dual-control restore gate's armed state is preserved, and server-owned bookkeeping fields are ignored",
				tagConsole, true,
				jsonResp("OK", ref("DRSchedule")),
				body(ref("DRScheduleInput")))),
		// ── Enterprise activation surface (501 on a community build) ──
		"/v1/console/activation", obj("get", op("getActivationStatus",
			"The edition's activation view: current edition and preset, each add-on's state and each preset's add-on keys",
			tagConsole, true,
			jsonResp("OK", ref("ActivationStatus")), nil)),
		"/v1/console/activation/preview", obj("post", op("previewActivation",
			"Preview a preset change as a per-add-on diff (activate, stage, unchanged or console) without applying it",
			tagConsole, true,
			jsonResp("OK", ref("ActivationPlan")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("preset"),
				"properties", obj("preset", obj("type", "string", "minLength", 1)))))),
		"/v1/console/activation/apply", obj("post", op("applyActivation",
			"Enable or disable a preset, or promote one add-on (AAL3 step-up; 501 without the activation service)",
			tagConsole, true,
			jsonResp("OK", ref("ActivationStatus")),
			body(obj("type", "object", "additionalProperties", false,
				"required", arr("action"),
				"properties", obj(
					"action", obj("type", "string", "enum", arr("enable", "disable", "promote")),
					"preset", obj("type", "string", "description", "Required for enable/disable."),
					"addon", obj("type", "string", "description", "Required for promote.")))))),
		// ── Module selection: which optional modules the engine runs ───
		"/v1/console/modules", obj(
			"get", op("getModuleSelection",
				"The module catalog and, for each module, whether it is selected, running, always-on, holds data, and who keeps it on",
				tagConsole, true,
				jsonResp("OK", ref("ModuleSelection")), nil),
			"put", op("selectModules",
				"Set which optional modules the engine runs; the engine restarts itself to apply the change (AAL3 step-up)",
				tagConsole, true,
				jsonResp("OK", ref("ModuleSelection")),
				body(obj("type", "object", "additionalProperties", false,
					"required", arr("selected"),
					"properties", obj("selected", obj("type", "array", "items", obj("type", "string"),
						"description", "The modules to run, by name.")))))),
		// ── Engine log viewer: SSE stream + ring-buffer snapshot ──
		"/v1/console/logs/stream", obj("get", func() map[string]any {
			o := rawOp("streamLogs",
				"Server-Sent Events stream of the engine log: event: log frames with one entry's payload, heartbeats as comments; filtered by the same parameters as the buffer",
				tagConsole, true, "SSE stream of engine log entries", "text/event-stream")
			o["parameters"] = arr(logLevelParams...)
			return o
		}()),
		"/v1/console/logs/buffer", obj("get", func() map[string]any {
			o := op("getLogBuffer",
				"The engine log's ring-buffer snapshot, newest last, with the match total and whether older matches were left out",
				tagConsole, true,
				jsonResp("OK", ref("LogBuffer")), nil)
			ps := append([]any{}, logLevelParams...)
			ps = append(ps, obj("name", "limit", "in", "query", "required", false,
				"description", "How many entries to return (default 1000, at most 10000: a larger value is clamped, a missing or non-positive one uses the default).",
				"schema", obj("type", "integer", "default", 1000)))
			o["parameters"] = arr(ps...)
			return o
		}()),
	)

	for _, method := range []string{"get", "put"} {
		operation := paths["/v1/system/tracing"].(map[string]any)[method].(map[string]any)
		operation["responses"].(map[string]any)["503"] = jsonResp("Tracing settings or collector configuration unavailable", ref("Error"))
	}
	applyStability(paths)

	schemas := obj(
		"TracingSettings", obj("type", "object", "properties", obj(
			"enabled", obj("type", "boolean"),
			"endpoint", obj("type", "string", "maxLength", 4096, "description", "Collector host or HTTP(S) URL without credentials, query or fragment."),
			"protocol", obj("type", "string", "enum", arr("grpc", "http/protobuf")),
			"insecure", obj("type", "boolean"),
			"sample_ratio", obj("type", "number", "minimum", 0, "maximum", 1),
			"service_name", obj("type", "string", "minLength", 1, "maxLength", 256),
			"genai_compat", obj("type", "boolean"),
		), "required", arr("enabled", "endpoint", "protocol", "insecure", "sample_ratio", "service_name", "genai_compat")),
		"TracingStatus", obj("type", "object", "properties", obj(
			"settings", ref("TracingSettings"), "effective", ref("TracingSettings"),
			"overrides", obj("type", "array", "items", obj("type", "string"), "description", "Names of environment overrides; never their credential values."),
		), "required", arr("settings", "effective", "overrides")),
		// ── Error envelope ──────────────────────────────────────────
		"Error", apiErrorSchema(),

		// ── Agent ───────────────────────────────────────────────────
		"Agent", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"tenant_id", obj("type", "string", "format", "uuid"),
			"workspace_id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"external_id", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive", "archived")),
			"identity_id", obj("type", "string", "format", "uuid"),
			"labels", obj("type", "object", "additionalProperties", true),
			"metadata", obj("type", "object", "additionalProperties", true),
			"created_at", obj("type", "string", "format", "date-time"),
			"updated_at", obj("type", "string", "format", "date-time"),
			"version", obj("type", "integer", "format", "int64"),
		), "required", arr("id", "tenant_id", "name", "kind", "status", "created_at", "updated_at", "version")),

		"AgentInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"external_id", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive", "archived")),
			"identity_id", obj("type", "string", "format", "uuid"),
			"workspace_id", obj("type", "string", "format", "uuid"),
			"labels", obj("type", "object", "additionalProperties", true),
			"metadata", obj("type", "object", "additionalProperties", true),
		), "required", arr("name", "kind")),

		// ── Member roster (console members grid) ──────────────
		"RosterMember", obj("type", "object", "properties", obj(
			"user_id", obj("type", "string", "format", "uuid"),
			"email", obj("type", "string"),
			"display_name", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive", "error")),
			"external_id", obj("type", "string"),
			"sso_only", obj("type", "boolean"),
			"role", obj("type", "string", "enum", arr("viewer", "editor", "admin", "owner")),
			"workspace_ids", obj("type", "array", "items", obj("type", "string", "format", "uuid")),
			"groups", obj("type", "array", "items", obj("type", "string")),
		), "required", arr("user_id", "email", "status", "sso_only", "role")),

		// ── Federated console search ─────────────────────────
		"SearchResult", obj("type", "object", "properties", obj(
			"kind", obj("type", "string", "description", "Result kind tag (e.g. workspace, user, connector, governance.policy, eventing.subscription, notify.route, orchestration.schedule)"),
			"id", obj("type", "string"),
			"name", obj("type", "string"),
			"detail", obj("type", "string", "description", "Short non-sensitive annotation (status/kind); never config or spec content"),
		), "required", arr("kind", "id", "name")),
		// `degraded` is NOT a nicety beside `truncated`, and publishing them as one field
		// would have been the bug in the schema instead of in the handler: truncated means
		// "there are more of these than fit", degraded means "a source failed and this list
		// is missing whatever it held". A client that cannot tell them apart cannot decide
		// whether to narrow the query or to escalate.
		"SearchResponse", obj("type", "object", "properties", obj(
			"results", obj("type", "array", "items", ref("SearchResult")),
			"truncated", obj("type", "boolean"),
			"degraded", obj("type", "boolean"),
			"degraded_kinds", obj("type", "array", "items", obj("type", "string")),
		), "required", arr("results", "truncated", "degraded", "degraded_kinds")),

		// ── Access edge ─────────────────────────────────────────────
		"AccessEdge", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"origin_kind", obj("type", "string"),
			"origin_id", obj("type", "string", "format", "uuid"),
			"resource_id", obj("type", "string", "format", "uuid"),
			"mode", obj("type", "string", "enum", arr("read", "readwrite")),
			"signal_source", obj("type", "string"),
			"confidence", obj("type", "string"),
			"permitted", obj("type", "boolean"),
			"observed", obj("type", "boolean"),
			"occurrence_count", obj("type", "integer", "format", "int64"),
			"first_seen", obj("type", "string", "format", "date-time"),
			"last_seen", obj("type", "string", "format", "date-time"),
		)),

		// ── Audit event ─────────────────────────────────────────────
		"AuditEvent", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"seq", obj("type", "integer", "format", "int64"),
			"occurred_at", obj("type", "string", "format", "date-time"),
			"actor", obj("type", "string"),
			"actor_kind", obj("type", "string"),
			"action", obj("type", "string"),
			"target_kind", obj("type", "string"),
			"target_id", obj("type", "string"),
			"prev_hash", obj("type", "string", "description", "Hex-encoded SHA-256 of the previous event"),
			"hash", obj("type", "string", "description", "Hex-encoded SHA-256 of this event"),
			"sig", obj("type", "string", "description", "Base64-encoded Ed25519 signature"),
		), "required", arr("id", "seq", "occurred_at", "actor", "actor_kind", "action", "prev_hash", "hash")),

		// ── User ────────────────────────────────────────────────────
		"User", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"email", obj("type", "string", "format", "email"),
			"display_name", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"is_superadmin", obj("type", "boolean"),
			"created_at", obj("type", "string", "format", "date-time"),
		), "required", arr("id", "email", "status", "is_superadmin", "created_at")),

		"CreatedUser", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"email", obj("type", "string", "format", "email"),
			"display_name", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"is_superadmin", obj("type", "boolean"),
			"created_at", obj("type", "string", "format", "date-time"),
			"membership", obj("type", "object",
				"description", "The first membership, granted in the create transaction when tenant was given",
				"properties", obj(
					"id", obj("type", "string", "format", "uuid"),
					"user_id", obj("type", "string", "format", "uuid"),
					"tenant", obj("type", "string"),
					"role", obj("type", "string"),
					"workspace_id", obj("type", "string"),
				)),
		), "required", arr("id", "email", "status", "is_superadmin", "created_at")),

		"CreateUserInput", obj("type", "object", "properties", obj(
			"email", obj("type", "string", "format", "email"),
			"display_name", obj("type", "string"),
			"password", obj("type", "string", "format", "password", "minLength", 12),
			"superadmin", obj("type", "boolean", "default", false),
			"tenant", obj("type", "string", "description", "Optional tenant granted role in the create transaction"),
			"role", obj("type", "string", "description", "The role granted in tenant"),
			"workspace_id", obj("type", "string", "description", "Optional workspace the membership is confined to"),
		), "required", arr("email", "password")),

		// ── Token ───────────────────────────────────────────────────
		"Token", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"user_id", obj("type", "string", "format", "uuid"),
			"bound_tenant_id", obj("type", "string"),
			"role", obj("type", "string"),
			"is_superadmin", obj("type", "boolean"),
			"expires_at", obj("type", "string", "format", "date-time"),
			"revoked", obj("type", "boolean"),
			"last_used_at", obj("type", "string", "format", "date-time"),
			"created_at", obj("type", "string", "format", "date-time"),
		), "required", arr("id", "name", "revoked", "created_at")),

		"IssueTokenInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"tenant", obj("type", "string"),
			"role", obj("type", "string"),
			"superadmin", obj("type", "boolean", "default", false),
		), "required", arr("name")),

		// ── Workspace ───────────────────────────────────────────────
		"Workspace", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"tenant_id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"slug", obj("type", "string", "pattern", "^[a-z0-9][a-z0-9-]{0,62}$"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"is_default", obj("type", "boolean"),
			"parent_id", obj("type", "string", "format", "uuid",
				"description", "The parent workspace in the organization tree; absent on a root and for a workspace-confined caller."),
			"settings", obj("type", "object", "additionalProperties", true),
			"created_at", obj("type", "string", "format", "date-time"),
			"updated_at", obj("type", "string", "format", "date-time"),
			"version", obj("type", "integer", "format", "int64"),
		), "required", arr("id", "tenant_id", "name", "slug", "status", "is_default", "created_at", "updated_at", "version")),

		"CreateWorkspaceInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"slug", obj("type", "string", "pattern", "^[a-z0-9][a-z0-9-]{0,62}$"),
			"settings", obj("type", "object", "additionalProperties", true),
		), "required", arr("name", "slug")),

		"UpdateWorkspaceInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"settings", obj("type", "object", "additionalProperties", true),
		)),

		// ── Org ──────────────────────────────────────────────────────
		"Org", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"tenant_id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"slug", obj("type", "string"),
			"status", obj("type", "string"),
			"data_region", obj("type", "string"),
			"created_at", obj("type", "string", "format", "date-time"),
		), "required", arr("id", "tenant_id", "name", "slug", "status", "created_at")),

		"CreateOrgInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"slug", obj("type", "string"),
			"data_region", obj("type", "string", "description", "Optional residency region pin"),
		), "required", arr("name", "slug")),

		"SetOrgRegionInput", obj("type", "object", "properties", obj(
			"data_region", obj("type", "string", "description", "Residency region pin; empty clears the pin"),
		), "required", arr("data_region")),

		"SetOrgStatusInput", obj("type", "object", "properties", obj(
			"status", obj("type", "string", "enum", arr("active", "suspended"),
				"description", "suspended withdraws service WITHOUT deleting anything: mutations and interactive service are refused with 423 tenant_suspended. Three things deliberately continue. (1) Authentication and the operator's /v1/system routes, so the tenant can be explained and restored. (2) EXPORT of the tenant's own data — /v1/audit/export and the module portability routes — because withdrawing service must not hold a customer's data hostage; an export taken while service is withdrawn is recorded on the tenant's own audit chain. (3) Custodial work: the chain keeps being checkpointed and stays verifiable and backup-certifiable. active restores service losslessly. Deleting a tenant is the separate, destructive DELETE /v1/system/orgs/{tenant_id}."),
		), "required", arr("status")),

		"ResidencyRegistry", obj("type", "object", "properties", obj(
			"home_region", obj("type", "string"),
			"regions", obj("type", "array", "items", obj("type", "string")),
			"enforces", obj("type", "boolean"),
		), "required", arr("home_region", "regions", "enforces")),

		// ── Membership ──────────────────────────────────────────────
		"GrantMembershipInput", obj("type", "object", "properties", obj(
			"user_id", obj("type", "string", "format", "uuid"),
			"tenant", obj("type", "string"),
			"role", obj("type", "string"),
			"workspace_id", obj("type", "string", "format", "uuid", "description", "optional: confine the membership to one workspace of the tenant; empty is tenant-wide"),
		), "required", arr("user_id", "tenant", "role")),

		// ── Auth inputs/responses ───────────────────────────────────
		"LoginInput", obj("type", "object", "properties", obj(
			"email", obj("type", "string", "format", "email"),
			"password", obj("type", "string", "format", "password"),
		), "required", arr("email", "password")),

		"SetupInput", obj("type", "object", "properties", obj(
			"token", obj("type", "string", "description", "One-time setup token from server startup"),
			"email", obj("type", "string", "format", "email"),
			"password", obj("type", "string", "format", "password", "minLength", 12),
			"organization", obj("type", "string", "description", "Optional name for the first organization; defaults to \"Default Organization\""),
		), "required", arr("token", "email", "password")),

		// SetupResult is the User shape (unchanged, flat) plus the organization
		// first-boot setup created and granted the new superadmin ownership of. The
		// console selects that tenant and sends it as X-Olivares-Tenant, so the id
		// must come back from setup itself rather than be guessed from a listing.
		"SetupResult", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"email", obj("type", "string", "format", "email"),
			"display_name", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"is_superadmin", obj("type", "boolean"),
			"created_at", obj("type", "string", "format", "date-time"),
			"organization", ref("Org"),
		), "required", arr("id", "email", "status", "is_superadmin", "created_at", "organization")),

		"SessionResponse", obj("oneOf", arr(ref("LoginResponse"), ref("BrowserSessionResponse"))),
		"BrowserSessionResponse", obj("type", "object", "properties", obj(
			"csrf_token", obj("type", "string"),
			"session_id", obj("type", "string", "format", "uuid"),
			"expires_at", obj("type", "string", "format", "date-time"),
		), "required", arr("csrf_token", "session_id", "expires_at")),
		"LoginResponse", obj("type", "object", "properties", obj(
			"token", obj("type", "string", "description", "Opaque session token (olvs_…)"),
			"session_id", obj("type", "string", "format", "uuid"),
			"expires_at", obj("type", "string", "format", "date-time"),
		), "required", arr("token", "session_id", "expires_at")),

		"CapabilityQuestions", obj("type", "object", "properties", obj(
			"schema_version", obj("type", "integer", "enum", arr(CapabilitySchemaVersion),
				"description", "Contract version. Only 2 is accepted; version 1 and every other value are rejected with 400 before any question lookup."),
			"questions", obj("type", "array", "minItems", 1, "maxItems", 32,
				"items", ref("CapabilityQuestion"),
				"description", "Up to 32 independent questions. The limit protects one request; it does not cap "+
					"the inventory of operations and does not prevent a later batch. Results stay independent per "+
					"id — the batch never ANDs or ORs them, and its 200 never turns an unknown into a denial."),
		), "additionalProperties", false, "required", arr("schema_version", "questions")),

		"CapabilityQuestion", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "description", "Correlates this question with its result."),
			"kind", obj("type", "string", "enum", arr("surface", "operation"),
				"description", "`surface` asks whether this registered COLLECTION may be loaded at all in the "+
					"named workspace. It asserts nothing about rows: an authorized empty list is reachable, and "+
					"a not_reachable collection never denies an `operation` on another route. `operation` asks "+
					"whether this exact operation on this exact resource is authorized right now."),
			"operation", obj("type", "string",
				"description", "`METHOD <registered pattern>`, e.g. `GET /v1/m/sessions/channels/administration`. "+
					"It names a route as it is MOUNTED; it is never a resolved URL, and no HTTP call is made for it."),
			"workspace_id", obj("type", "string", "format", "uuid",
				"description", "For a collection: the workspace selector the route declares, corroborated by the "+
					"server against the stored workspace row. For an entity: an assertion COMPARED with the row's "+
					"stored workspace — a mismatch is answered with that route's own concealment and is never "+
					"silently corrected."),
			"selectors", ref("CapabilitySelectors"),
		), "additionalProperties", false, "required", arr("id", "kind", "operation")),

		"CapabilitySelectors", obj("type", "object", "properties", obj(
			"path", obj("type", "object", "additionalProperties", obj("type", "string"),
				"description", "The declared path locator of an entity route (its `{id}`)."),
			"body", obj("type", "object", "additionalProperties", obj("type", "string"),
				"description", "The ONE declared top-level body field an entity route uses to locate its row. It "+
					"is an identification input, not a write payload: no other field of a command is admitted or "+
					"consulted, and nothing is executed."),
		// The two maps stay OPEN because the wire contract says a selector map is a map;
		// what closes them is the per-operation validation, which admits only the keys
		// the REGISTERED route declares. The wrapper object is closed here so a third
		// selector family cannot arrive unnoticed.
		), "additionalProperties", false),

		"CapabilityResults", obj("type", "object", "properties", obj(
			"schema_version", obj("type", "integer", "enum", arr(CapabilitySchemaVersion)),
			"results", obj("type", "array", "items", ref("CapabilityResult")),
		), "additionalProperties", false, "required", arr("schema_version", "results")),

		"CapabilityResult", obj("type", "object", "properties", obj(
			"id", obj("type", "string"),
			"kind", obj("type", "string", "enum", arr("surface", "operation")),
			"state", obj("type", "string",
				"enum", arr("allowed", "denied", "unknown", "undisclosed", "reachable", "not_reachable"),
				"description", "Operations answer `allowed`, `denied` or `unknown`. A registered concealing operation "+
					"instead publishes `undisclosed` for every nonpositive authority outcome: it asserts neither "+
					"denial, absence nor outage. Target-free input/support/step-up rejections remain `unknown`. "+
					"Surfaces answer `reachable`, `not_reachable` or `unknown` and confer no row authority. "+
					"Only a fresh positive with its finite budget may enable an action; no other state is a permit."),
			"code", obj("type", "string",
				"enum", arr("authorized", "admitted", "not_permitted", "not_disclosed",
					"not_supported", "inputs_required", "engine_unready", "evidence_unavailable",
					"step_up_required", "stale"),
				"description", "`not_permitted` is an established non-concealing denial. `not_disclosed` accompanies "+
					"`undisclosed` and carries no negative explanation. `not_supported`, `inputs_required` and "+
					"`step_up_required` may accompany `unknown` only as target-free request gates on a concealing "+
					"operation. Later authority failures, including unusable evidence, are not disclosed there. "+
					"For other projections `engine_unready` and `evidence_unavailable` accompany `unknown`. "+
					"`stale` is the CLIENT's own verdict after expiry or context change; the server never sends it."),
			"observed_at", obj("type", "string", "format", "date-time",
				"description", "For undisclosed, the common batch dispatch marker, not an authority fact. Other results retain their own observation instant."),
			"refresh_after_ms", obj("type", "integer",
				"description", "How long this POSITIVE may be shown, in milliseconds: the minimum of 30000 and "+
					"what REMAINS of the evidence window of every predicate that founds it. It is present ONLY on "+
					"`allowed`/`reachable`, and its ABSENCE on one is itself an instruction — treat that result as "+
					"unknown rather than believing it. Discount the whole request with a MONOTONIC clock, "+
					"including retries of the same question; a late response does not start a fresh window on "+
					"arrival. It is an observation budget, NOT a lease: it grants nothing, and it does not promise "+
					"that a revocation elsewhere will reach you within it. The handler re-authorizes every real "+
					"act and accepts nothing from this response."),
		), "additionalProperties", false, "required", arr("id", "kind", "state", "code", "observed_at")),

		"WhoamiResponse", obj("type", "object", "properties", obj(
			"kind", obj("type", "string", "enum", arr("user", "token")),
			"user_id", obj("type", "string", "format", "uuid"),
			"actor", obj("type", "string"),
			"display_name", obj("type", "string"),
			"email", obj("type", "string", "description", "Signed-in user email; omitted for token principals."),
			"superadmin", obj("type", "boolean"),
			"grants", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"tenant", obj("type", "string"),
				"role", obj("type", "string"),
				"permissions", obj("type", "array", "items", obj("type", "string"),
					"description", "The principal's EFFECTIVE permission set in this tenant, sorted. "+
						"The console answers \"may I?\" by membership of this set. It is the tenant-wide "+
						"RBAC floor over the permissions this binary serves, minus the workspace-confinement "+
						"forbids that hold regardless of target; authored scoped grants/forbids and the "+
						"ABAC deny-overlay are decided per resource and are NOT reflected."),
				// No internal session reference in this string: it is PUBLISHED API
				// documentation, and lint:export scrubs comments but not string values.
				"confined_workspace", obj("type", "string",
					"description", "Present only when this membership is confined to a workspace: "+
						"the principal may act only within it, enforced server-side on every request."),
				"tenant_name", obj("type", "string",
					"description", "The display name of this tenant's organization, read from the principal's "+
						"own tenant (no cross-tenant read). Absent when it cannot be read."),
			), "required", arr("tenant", "role", "permissions"))),
			"session_ttl_seconds", obj("type", "integer", "description", "Session lifetime in seconds at sign-in or refresh (sessions only; fixed by the engine)."),
			"aal", obj("type", "integer", "description", "Authentication assurance level (sessions only)"),
			"amr", obj("type", "array", "items", obj("type", "string"), "description", "Authentication method references (sessions only)"),
			"authentication_configuration", obj("type", "object",
				"description", "Deployment authentication configuration observed for this response. This is not certificate validation or authorization.",
				"properties", obj("piv_configured", obj("type", "boolean",
					"description", "Whether this deployment has configured PIV verifier roots. Does not indicate certificate presence, OCSP status, or assurance level.")),
				"required", arr("piv_configured")),
		), "required", arr("kind", "user_id", "actor", "superadmin")),

		// ── Server info ─────────────────────────────────────────────
		"ServerInfo", obj("type", "object", "properties", obj(
			"sso_providers", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"label", obj("type", "string"), "start_url", obj("type", "string"),
			), "required", arr("label", "start_url"))),
			"version", obj("type", "string"),
			"engine", obj("type", "string"),
			"setup_required", obj("type", "boolean"),
			"edition", obj("type", "string"),
			"tls_pin_sha256", obj("type", "string",
				"description", "The --pin-sha256 value of the certificate this engine serves (base64 SHA-256 of its SubjectPublicKeyInfo). Absent when the engine serves plain HTTP."),
			"modules_not_enabled", obj("type", "array", "items", obj("type", "string")),
			"communication_ready", obj("type", "boolean"),
			"previews_hidden", obj("type", "boolean",
				"description", "A new installation's console navigation lists the first job only; every other page keeps its address. Absent on an installation that existed before."),
			"invite_delivery_unavailable", obj("type", "boolean",
				"description", "The engine cannot mail invitations (OLIVARES_INVITE_MAIL_DESTINATION with a declared console address): onboarding in invite mode answers 409 invite_delivery_unavailable. Absent when invitations are mailed."),
			"license", obj("type", "object", "properties", obj(
				"status", obj("type", "string"),
				"licensee", obj("type", "string"),
				"plan", obj("type", "string"),
				"support_tier", obj("type", "string"),
			)),
			"protocol_currency", obj("type", "object", "properties", obj(
				"mcp_revision", obj("type", "string"),
				"mcp_revision_status", obj("type", "string"),
				"a2a_version", obj("type", "string"),
				"a2a_security_scheme_enforced", obj("type", "boolean"),
				"agents_md_enforce_available", obj("type", "boolean"),
				"aaif_standards", obj("type", "array", "items", obj("type", "string")),
			)),
			"jobs_not_running", obj("type", "array",
				"description", "Background jobs this node does not run, and why. Absent when every job runs.",
				"items", obj("type", "object", "properties", obj(
					"job", obj("type", "string", "enum", arr("retention", "legal_hold_archive", "audit_checkpoints", "audit_archive", "directory_synchronization")),
					"reason", obj("type", "string", "enum", arr("no_tenant_inventory", "addon_requires_license", "directory_unavailable")),
				), "required", arr("job", "reason"))),
		), "required", arr("version", "engine", "setup_required")),

		// ── Public status ───────────────────────────────────────────
		"PublicStatus", obj("type", "object", "properties", obj(
			"status", obj("type", "string", "enum", arr("operational", "not_configured", "degraded", "outage")),
			"version", obj("type", "string"),
			"embedder_kind", obj("type", "string", "enum", arr("semantic", "local-hash")),
			"retrieval_semantic", obj("type", "boolean"),
			"knowledge_status_reason", obj("type", "string"),
			"guard_profile", obj("type", "string", "enum", arr("acl_aware", "public_only")),
			"guard_warning", obj("type", "string"),
			"guard_downgrade_count", obj("type", "integer"),
			"components", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"name", obj("type", "string"),
				"status", obj("type", "string"),
				"embedder_kind", obj("type", "string", "enum", arr("semantic", "local-hash")),
				"retrieval_semantic", obj("type", "boolean"),
				"reason", obj("type", "string"),
				"guard_profile", obj("type", "string", "enum", arr("acl_aware", "public_only")),
				"guard_warning", obj("type", "string"),
				"guard_downgrade_count", obj("type", "integer"),
			))),
			"updated_at", obj("type", "string", "format", "date-time"),
		)),

		// ── Connector health ────────────────────────────────────────
		"ConnectorHealthResponse", obj("type", "object", "properties", obj(
			"items", obj("type", "array", "items", ref("ConnectorHealth")),
			"summary", ref("ConnectorSummary"),
			"timestamp", obj("type", "string", "format", "date-time"),
		), "required", arr("items", "summary", "timestamp")),

		"ConnectorHealth", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"title", obj("type", "string"),
			"tenant", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("running", "failed", "stopped", "disabled")),
			"source_mode", obj("type", "string", "enum", arr("export", "live")),
			"enabled", obj("type", "boolean"),
			"poll_seconds", obj("type", "integer"),
			"last_polled_at", obj("type", "string", "format", "date-time"),
			"error_count_24h", obj("type", "integer"),
			"avg_latency_ms", obj("type", "integer", "format", "int64"),
			"trend", obj("type", "string"),
			"health_state", obj("type", "string"),
		), "required", arr("name", "kind", "status", "enabled")),

		"ConnectorSummary", obj("type", "object", "properties", obj(
			"total", obj("type", "integer"),
			"running", obj("type", "integer"),
			"failed", obj("type", "integer"),
			"stopped", obj("type", "integer"),
			"disabled", obj("type", "integer"),
		)),

		// ── Console: secrets ────────────────────────────────────────
		"SecretsList", obj("type", "object", "properties", obj(
			"secrets", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"name", obj("type", "string"),
				"hint", obj("type", "string"),
				"description", obj("type", "string"),
				"created_at", obj("type", "string", "format", "date-time"),
				"updated_at", obj("type", "string", "format", "date-time"),
			))),
			"sealer_available", obj("type", "boolean"),
		)),

		"SecretInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"value", obj("type", "string", "format", "password"),
			"description", obj("type", "string"),
		), "required", arr("name", "value")),

		// ── Console: setup/health ───────────────────────────────────
		"SetupStatus", obj("type", "object", "properties", obj(
			"completed", obj("type", "boolean"),
			"steps", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"id", obj("type", "string"),
				"completed", obj("type", "boolean"),
				"applicable", obj("type", "boolean", "description",
					"Present and false ONLY for a step this build or deployment cannot complete. `completed` above is computed over the APPLICABLE steps, so one unwireable step does not report a finished install as unfinished."),
				"reason", obj("type", "string", "description",
					"Why the step is incomplete or not applicable. An incomplete step without a reason is a to-do item with no instructions."),
			))),
		), "required", arr("completed", "steps")),

		"HealthSummary", obj("type", "object", "properties", obj(
			"healthy", obj("type", "boolean"),
			"ready", obj("type", "boolean"),
			"store_engine", obj("type", "string"),
			"connectors_available", obj("type", "integer", "description",
				"Connector KINDS this build can wire (the catalog). A capability of the binary, not a live fleet: non-zero on a clean install with nothing configured."),
			"connectors_configured", obj("type", "integer", "description",
				"Connector INSTANCES in the durable source roster, enabled or not."),
			"connectors_running", obj("type", "integer", "description",
				"Roster entries whose live status is running (same criterion as the running field of GET /v1/connectors/health)."),
			"connectors_error", obj("type", "integer", "description",
				"ENABLED roster entries that are not carrying data: status failed OR not_wired, plus any status this build does not recognize. Same classification as the failed field of GET /v1/connectors/health and the connectors component of GET /status, so the three cannot disagree."),
			"connectors_measured", obj("type", "boolean", "description",
				"Present and false ONLY when the source roster could not be read. The four connector counters above are then meaningless and must not be shown as measurements: absent means they were measured."),
			"users", obj("type", "integer", "description",
				"Accounts in this deployment. A COUNT unless users_capped is present, in which case it is a lower bound."),
			"users_capped", obj("type", "boolean", "description",
				"Present and true ONLY when the account census hit its paging budget, making `users` a lower bound rather than a count."),
			"sso_configured", obj("type", "boolean"),
			"version", obj("type", "string"),
			"embedder_kind", obj("type", "string", "enum", arr("semantic", "local-hash")),
			"retrieval_semantic", obj("type", "boolean"),
			"knowledge_status_reason", obj("type", "string"),
			"guard_profile", obj("type", "string", "enum", arr("acl_aware", "public_only")),
			"guard_warning", obj("type", "string"),
			"guard_downgrade_count", obj("type", "integer"),
			"guard_public_only_kbs", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"tenant_id", obj("type", "string"),
				"tenant_slug", obj("type", "string"),
				"kb_name", obj("type", "string"),
				"profile", obj("type", "string", "enum", arr("public_only")),
				"reason", obj("type", "string"),
				"updated_by", obj("type", "string"),
			))),
			"update", ref("UpdateStatus"),
			"tls_not_after", obj("type", "string", "format", "date-time"),
			"tls_days_left", obj("type", "integer", "format", "int64"),
		), "required", arr("healthy", "ready", "store_engine", "version")),

		"UpdateStatus", obj("type", "object", "properties", obj(
			"enabled", obj("type", "boolean"),
			"available", obj("type", "boolean"),
			"up_to_date", obj("type", "boolean"),
			"channel", obj("type", "string"),
			"current_version", obj("type", "string"),
			"latest_version", obj("type", "string"),
			"security", obj("type", "boolean"),
			"advisories", obj("type", "array", "items", obj("type", "string")),
			"checked_at", obj("type", "string", "format", "date-time"),
			"error", obj("type", "string"),
		), "required", arr("enabled", "available", "up_to_date", "channel", "current_version")),

		"EffectiveConfigResponse", obj("type", "object", "properties", obj(
			"entries", obj("type", "array", "items", ref("EffectiveConfigEntry")),
			"strict_violations", obj("type", "array", "items", obj("type", "string")),
		), "required", arr("entries", "strict_violations")),

		"EffectiveConfigEntry", obj("type", "object", "properties", obj(
			"key", obj("type", "string"),
			"value", obj("type", "string"),
			"redacted", obj("type", "boolean"),
			"source", obj("type", "string", "enum", arr("env", "activation")),
		), "required", arr("key", "value", "redacted", "source")),

		// ── Console: key custody / event bus ─────────────────────────
		"KeyCustody", obj("type", "object", "properties", obj(
			"keys", obj("type", "array", "items", ref("KeyInfo")),
		), "required", arr("keys")),

		"KeyInfo", obj("type", "object", "properties", obj(
			"purpose", obj("type", "string", "enum", arr("audit", "catalog", "policy", "license", "eventing", "sso", "secret-store", "memory-portability")),
			"algorithm", obj("type", "string"),
			"custody_mode", obj("type", "string", "enum", arr("minted", "byok-env", "byok-file", "cmek")),
			"kek", obj("type", "string", "description", "Non-secret KEK provider and key identifier"),
			"created", obj("type", "string", "description", "RFC3339 creation timestamp; empty when unknown"),
			"public_key", obj("type", "string", "description", "Base64-encoded public verification key"),
			"fingerprint", obj("type", "string", "description", "Full SHA-256 hex for runtime signing keys; embedded license-key display fingerprint for license"),
			"prior_count", obj("type", "integer", "minimum", 0),
			"origin", obj("type", "string", "description", "Embedded license-key origin"),
			"source", obj("type", "string", "enum", arr("env", "file")),
			"present", obj("type", "boolean"),
		), "required", arr("purpose")),

		"BusSnapshot", obj("type", "object", "properties", obj(
			"subscribers", obj("type", "array", "items", ref("BusSubscriber")),
			"publish_blocked", obj("type", "integer", "format", "int64", "minimum", 0),
			"dropped", obj("type", "integer", "format", "int64", "minimum", 0),
			"dropped_telemetry", obj("type", "integer", "format", "int64", "minimum", 0),
			"dropped_notify", obj("type", "integer", "format", "int64", "minimum", 0),
			"handler_errors", obj("type", "integer", "format", "int64", "minimum", 0),
			"enqueued", obj("type", "integer", "format", "int64", "minimum", 0),
			"handled", obj("type", "integer", "format", "int64", "minimum", 0),
			"bridge", ref("BusBridge"),
		), "required", arr("subscribers", "publish_blocked", "dropped", "dropped_telemetry", "dropped_notify", "handler_errors", "enqueued", "handled")),

		"BusSubscriber", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"class", obj("type", "string", "enum", arr("enforcement", "state", "telemetry", "notify", "unknown")),
			"depth", obj("type", "integer"),
			"capacity", obj("type", "integer"),
		), "required", arr("name", "class", "depth", "capacity")),

		"BusBridge", obj("type", "object", "properties", obj(
			"connected", obj("type", "boolean"),
			"pending_msgs", obj("type", "integer"),
			"pending_bytes", obj("type", "integer"),
			"dropped", obj("type", "integer", "format", "int64"),
			"publish_errors", obj("type", "integer", "format", "int64", "minimum", 0),
			"decode_errors", obj("type", "integer", "format", "int64", "minimum", 0),
			"gate_skipped", obj("type", "integer", "format", "int64", "minimum", 0),
			"invalid_subject", obj("type", "integer", "format", "int64", "minimum", 0),
		), "required", arr("connected", "pending_msgs", "pending_bytes", "dropped", "publish_errors", "decode_errors", "gate_skipped", "invalid_subject")),

		// ── Console: license ────────────────────────────────────────
		"LicenseStatus", obj("type", "object", "properties", obj(
			"valid", obj("type", "boolean"),
			"licensee", obj("type", "string"),
			"plan", obj("type", "string"),
			"support_tier", obj("type", "string"),
			"max_users", obj("type", "integer"),
			"expires_at", obj("type", "string", "format", "date-time"),
			"holder", obj("type", "string"),
		)),

		// ── Console: connectors ─────────────────────────────────────
		"ConnectorInfo", obj("type", "object", "properties", obj(
			"kind", obj("type", "string"),
			"title", obj("type", "string"),
			"description", obj("type", "string"),
			"transport", obj("type", "string"),
			"fields_known", obj("type", "boolean"),
			"hosting", obj("type", "string", "enum", arr("self_hosted", "vendor_hosted", "unknown"),
				"description", "Where the observed system runs, DERIVED from the connector's own declared endpoint defaults: self_hosted (its default endpoint is on the loopback host — the operator runs it), vendor_hosted (a routable vendor URL), or unknown (it declares no endpoint, or it is an out-of-process plugin the host cannot introspect). unknown is a real third answer and is never a synonym for vendor_hosted."),
			"fields", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"key", obj("type", "string"),
				"type", obj("type", "string", "enum", arr("string", "int", "bool", "duration")),
				"required", obj("type", "boolean"),
				"secret", obj("type", "boolean"),
				"default", obj("type", "string"),
				"description", obj("type", "string"),
			))),
		)),

		"ConnectorOnboardInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"tenant", obj("type", "string"),
			"poll_seconds", obj("type", "integer"),
			"enabled", obj("type", "boolean"),
			"config", obj("type", "object", "additionalProperties", obj("type", "string")),
			"secrets", obj("type", "object", "additionalProperties", obj("type", "string")),
		), "required", arr("name", "kind", "tenant")),

		"ConnectorApplyResult", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"action", obj("type", "string", "enum", arr("added", "rotated", "removed", "disabled", "unchanged")),
			"persisted", obj("type", "boolean"),
			"applied", obj("type", "boolean"),
			"note", obj("type", "string"),
		)),

		// ── Console: sources ────────────────────────────────────────
		"SourceRosterInput", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"tenant", obj("type", "string"),
			"poll_seconds", obj("type", "integer"),
			"enabled", obj("type", "boolean"),
			"config", obj("type", "object", "additionalProperties", obj("type", "string")),
		), "required", arr("name", "tenant")),

		"SourceRosterEntry", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"kind", obj("type", "string"),
			"tenant", obj("type", "string"),
			"poll_seconds", obj("type", "integer"),
			"enabled", obj("type", "boolean"),
			"config", obj("type", "object", "additionalProperties", obj("type", "string")),
			"status", obj("type", "string"),
			"source_mode", obj("type", "string", "enum", arr("export", "live")),
			// The connector serving this source, reported BESIDE its name. Two
			// sources of one kind share it and differ in name; it is absent until
			// the source is wired (a plugin self-describes only once launched), and
			// it is read-only — observed from the running connector, never authored.
			"component", obj("type", "string", "readOnly", true),
		)),

		"SourceApplyResult", obj("type", "object", "properties", obj(
			"name", obj("type", "string"),
			"action", obj("type", "string", "enum", arr("added", "rotated", "removed", "disabled", "unchanged")),
			"persisted", obj("type", "boolean"),
			"applied", obj("type", "boolean"),
			"note", obj("type", "string"),
		)),

		// ── Console: SSO ────────────────────────────────────────────
		// BOTH OF THESE WERE FICTION (corrected 2026-08-06), on two STABLE paths.
		//
		// The response schema published ten properties — enabled, issuer, client_id, tenant,
		// auto_provision, enforce, enforce_mfa, default_role — against a handler that returns
		// twenty-nine entirely different ones (ssoConfigDTO, handlers_sso_config.go:28-87).
		// The input schema was worse: it declared issuer, client_id and tenant REQUIRED while
		// the decoder rejects unknown fields and wants oidc_issuer / oidc_client_id /
		// oidc_client_secret. Only `protocol` and `enabled` survived by name, so a payload
		// built from the published contract answers 400 "invalid JSON body" — measured.
		//
		// A customer generating an SDK from our stable contract could therefore neither READ
		// nor WRITE the SSO configuration: the published SSO surface described an API that
		// does not exist. Replacing it is not a breaking change — no conforming client can be
		// working today, because none can get past the decoder.
		//
		// TestPublishedSSOSchemasMatchTheGoShapes derives both property sets from the structs
		// by reflection, so the two sides cannot drift apart again by editing one of them.
		// Formats and enums stay hand-written and are NOT pinned by that test: they are
		// editorial, and the failure this closes is a name no decoder accepts.
		"SSOConfig", obj("type", "object", "properties", obj(
			"assurance_mapping", ref("FederationAssuranceMapping"),
			"display_name", obj("type", "string", "maxLength", 80),
			"configured", obj("type", "boolean"),
			"provider_available", obj("type", "boolean"),
			"protocol", obj("type", "string", "enum", arr("oidc", "saml")),
			"status", obj("type", "string"),
			"redirect_uri", obj("type", "string", "format", "uri"),
			"target_tenant", obj("type", "string"),
			"alias", obj("type", "string"),
			"oidc_issuer", obj("type", "string", "format", "uri"),
			"oidc_client_id", obj("type", "string"),
			// The secret is never returned: the read side carries a HINT, and naming it
			// `_hint` in the contract is part of the guarantee, not a detail.
			"oidc_client_secret_hint", obj("type", "string"),
			"saml_metadata_url", obj("type", "string", "format", "uri"),
			"saml_entity_id", obj("type", "string"),
			"saml_acs_url", obj("type", "string", "format", "uri"),
			"saml_idp_sso_url", obj("type", "string", "format", "uri"),
			"saml_email_attr", obj("type", "string"),
			"saml_sp_cert_pem", obj("type", "string"),
			"saml_sp_key_hint", obj("type", "string"),
			"saml_sp_sign_cert_pem", obj("type", "string"),
			"saml_sp_sign_key_hint", obj("type", "string"),
			"require_sso", obj("type", "boolean"),
			"network_allowlist", obj("type", "array", "items", obj("type", "string")),
			"enforced_by", obj("type", "string"),
			"oidc_groups_claim", obj("type", "string"),
			"saml_groups_attr", obj("type", "string"),
			"scim_authoritative", obj("type", "boolean"),
			"groups_mapped_by", obj("type", "string"),
			"claimed_domains", obj("type", "array", "items", obj("type", "string")),
			"routed_by", obj("type", "string"),
			"updated_at", obj("type", "string", "format", "date-time"),
		), "required", arr("configured", "provider_available")),

		"SSOConfigInput", obj("type", "object", "properties", obj(
			"assurance_mapping", ref("FederationAssuranceMapping"),
			"display_name", obj("type", "string", "maxLength", 80),
			"protocol", obj("type", "string", "enum", arr("oidc", "saml")),
			"enabled", obj("type", "boolean"),
			"oidc_issuer", obj("type", "string", "format", "uri"),
			"oidc_client_id", obj("type", "string"),
			// A BLANK secret keeps the sealed value already stored, which is why it is not
			// required: making it required would force re-entering the secret to edit a
			// group claim.
			"oidc_client_secret", obj("type", "string", "format", "password"),
			"saml_metadata_url", obj("type", "string", "format", "uri"),
			"saml_entity_id", obj("type", "string"),
			"saml_acs_url", obj("type", "string", "format", "uri"),
			"saml_idp_sso_url", obj("type", "string", "format", "uri"),
			"saml_email_attr", obj("type", "string"),
			"saml_sp_cert_pem", obj("type", "string"),
			"saml_sp_key_pem", obj("type", "string", "format", "password"),
			"saml_sp_sign_cert_pem", obj("type", "string"),
			"saml_sp_sign_key_pem", obj("type", "string", "format", "password"),
			"require_sso", obj("type", "boolean"),
			"network_allowlist", obj("type", "array", "items", obj("type", "string")),
			"oidc_groups_claim", obj("type", "string"),
			"saml_groups_attr", obj("type", "string"),
			"scim_authoritative", obj("type", "boolean"),
			"claimed_domains", obj("type", "array", "items", obj("type", "string")),
			// `protocol` alone: everything else is validated per protocol by the handler, and
			// a document that demands more than the server does is the same class of lie as
			// one that demands different names.
		), "required", arr("protocol")),

		// ── Effective rights ──────────────────────────────────
		"EffectiveRights", obj("type", "object", "properties", obj(
			"subject", ref("EffectiveRightsRef"),
			"node", ref("EffectiveRightsRef"),
			"assurance", obj("type", "integer", "description", "The authenticator assurance level the subject was evaluated at."),
			"path", obj("type", "array", "items", ref("EffectiveRightsStep"),
				"description", "The node's container lineage, outermost first."),
			"rights", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"name", obj("type", "string"),
				"state", obj("type", "string", "description", "The right's decision state as the engine reports it."),
			), "required", arr("name", "state"))),
		), "required", arr("subject", "node", "assurance", "path", "rights")),
		"EffectiveRightsRef", obj("type", "object", "properties", obj(
			"kind", obj("type", "string"),
			"id", obj("type", "string"),
		), "required", arr("kind", "id")),
		"EffectiveRightsStep", obj("type", "object", "properties", obj(
			"kind", obj("type", "string"),
			"ref", obj("type", "string"),
			"workspace", obj("type", "string", "description", "Set only on an agent group that lives in a different workspace than the node."),
		), "required", arr("kind", "ref")),

		// ── WebAuthn credentials (panel) ────────────────────────
		"WebAuthnCeremonyOptions", obj("type", "object", "properties", obj(
			"publicKey", obj("type", "object",
				"description", "The WebAuthn ceremony's PublicKeyCredential options (creation or assertion), serialized as the browser API expects."),
		), "required", arr("publicKey")),
		"WebAuthnCredentialInput", obj("type", "object", "properties", obj(
			"credential", obj("type", "object", "description", "The browser's encoded ceremony response."),
			"name", obj("type", "string", "description", "Optional user-supplied display name for the credential."),
		), "required", arr("credential")),
		"WebAuthnCredential", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"created_at", obj("type", "string", "format", "date-time"),
			"backup_eligible", obj("type", "boolean", "description", "Present when the credential's flags declare it; never any key material."),
		), "required", arr("id", "name", "created_at")),

		// ── PIV smart-card status ───────────────────────────────────
		"PIVStatus", obj("type", "object", "properties", obj(
			"presented", obj("type", "boolean"),
			"subject", obj("type", "string", "description", "The certificate's subject DN."),
			"issuer", obj("type", "string", "description", "The certificate's issuer DN."),
			"mapped_role", obj("type", "string", "description", "The role the certificate maps to."),
			"ocsp", obj("type", "string", "enum", arr("good", "revoked", "unknown")),
			"not_after", obj("type", "string", "format", "date-time"),
		), "required", arr("presented")),

		// ── Agent groups (S256) ─────────────────────────────────────
		"AgentGroup", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"tenant_id", obj("type", "string", "format", "uuid"),
			"workspace_id", obj("type", "string", "format", "uuid", "description", "The group's workspace scope; absent when tenant-wide."),
			"name", obj("type", "string"),
			"slug", obj("type", "string", "description", "The stable scope handle ([a-z0-9][a-z0-9-]*, max 63)."),
			"description", obj("type", "string"),
			"status", obj("type", "string", "enum", arr("active", "inactive")),
			"metadata", obj("type", "object", "additionalProperties", true),
			"created_at", obj("type", "string", "format", "date-time"),
			"updated_at", obj("type", "string", "format", "date-time"),
			"version", obj("type", "integer", "format", "int64"),
		), "required", arr("id", "tenant_id", "name", "slug", "status", "created_at", "updated_at", "version")),
		"AgentGroupInput", obj("type", "object", "additionalProperties", false,
			"properties", obj(
				"workspace_id", obj("type", "string", "format", "uuid"),
				"name", obj("type", "string", "minLength", 1),
				"slug", obj("type", "string", "pattern", "^[a-z0-9][a-z0-9-]*$", "maxLength", 63),
				"description", obj("type", "string"),
				"status", obj("type", "string", "enum", arr("active", "inactive")),
				"metadata", obj("type", "object", "additionalProperties", true),
			), "required", arr("name", "slug")),
		"AgentGroupPatch", obj("type", "object", "description",
			"Partial update: every field is a pointer, so an omitted field is left untouched (slug is immutable). "+
				"A set workspace_id re-scopes the group; an explicit empty string clears the scope back to tenant-wide.",
			"properties", obj(
				"name", obj("type", "string"),
				"description", obj("type", "string"),
				"status", obj("type", "string", "enum", arr("active", "inactive")),
				"metadata", obj("type", "object", "additionalProperties", true),
				"workspace_id", obj("type", "string"),
			)),
		"AgentGroupMember", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid", "description", "The membership row's id."),
			"group_id", obj("type", "string", "format", "uuid"),
			"agent_id", obj("type", "string", "format", "uuid"),
		), "required", arr("id", "group_id", "agent_id")),

		// ── Directory groups (IdP-provisioned) ──────────────────────
		"DirectoryGroup", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"display_name", obj("type", "string"),
			"external_id", obj("type", "string", "description", "The group's id in the provisioning IdP."),
			"provisioned_by", obj("type", "string"),
			"mapped_role", obj("type", "string", "description", "The role the group's members are elevated to; empty when unmapped."),
			"parent_group_id", obj("type", "string", "description", "The parent group's id; empty when the group is top-level."),
			"workspace_id", obj("type", "string", "description", "The workspace where the group is placed; empty when unplaced or concealed from a workspace-confined caller."),
			"members", obj("type", "integer", "description", "The group's member count."),
		), "required", arr("id", "display_name", "mapped_role", "parent_group_id", "workspace_id", "members")),

		// ── Invitations and onboarding ──────────────────────────────
		"Invite", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "format", "uuid"),
			"email", obj("type", "string", "format", "email"),
			"tenant", obj("type", "string"),
			"role", obj("type", "string"),
			"expires_at", obj("type", "string", "format", "date-time"),
			"created_at", obj("type", "string", "format", "date-time"),
		), "required", arr("id", "email", "tenant", "role", "expires_at", "created_at")),
		"OnboardInput", obj("type", "object", "additionalProperties", false,
			"properties", obj(
				"email", obj("type", "string", "format", "email"),
				"display_name", obj("type", "string"),
				"role", obj("type", "string"),
				"mode", obj("type", "string", "enum", arr("password", "invite"),
					"description", "password: the admin sets the initial password. invite: email a single-use token. Empty defaults to password."),
				"password", obj("type", "string", "format", "password", "writeOnly", true),
			), "required", arr("email")),
		"OnboardResult", obj("type", "object", "properties", obj(
			"user", ref("User"),
			"created", obj("type", "boolean", "description", "false when an existing account was reused."),
			"membership", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"user_id", obj("type", "string", "format", "uuid"),
				"tenant", obj("type", "string"),
				"role", obj("type", "string"),
			), "required", arr("id", "user_id", "tenant", "role")),
			"invite", obj("type", "object", "description", "Present only for mode=invite.",
				"properties", obj(
					"id", obj("type", "string", "format", "uuid"),
					"expires_at", obj("type", "string", "format", "date-time"),
					"delivery", obj("type", "string", "enum", arr("sent", "failed"),
						"description", "Whether the invitation email left the engine; the token travels only in the mail."),
				)),
		), "required", arr("user", "created", "membership")),

		// ── Workspace summary and contents ──────────────────────────
		"WorkspaceSummary", obj("type", "object", "properties", obj(
			"workspace_id", obj("type", "string", "format", "uuid"),
			"name", obj("type", "string"),
			"slug", obj("type", "string"),
			"is_default", obj("type", "boolean"),
			"agent_count", obj("type", "integer"),
			"session_count", obj("type", "integer"),
			"resource_count", obj("type", "integer"),
			"group_count", obj("type", "integer"),
			"agent_count_capped", obj("type", "boolean", "description", "The matching count is a FLOOR (at least N), never a total."),
			"session_count_capped", obj("type", "boolean"),
			"resource_count_capped", obj("type", "boolean"),
			"group_count_capped", obj("type", "boolean"),
		), "required", arr("workspace_id", "name", "slug", "is_default",
			"agent_count", "session_count", "resource_count", "group_count",
			"agent_count_capped", "session_count_capped", "resource_count_capped", "group_count_capped")),
		"WorkspaceContents", obj("type", "object", "properties", obj(
			"workspace_id", obj("type", "string", "format", "uuid"),
			"kinds", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"kind", obj("type", "string"),
				"count", obj("type", "integer"),
				"capped", obj("type", "boolean", "description", "Count is a floor (\"at least Count\"), never a total."),
			), "required", arr("kind", "count", "capped"))),
		), "required", arr("workspace_id", "kinds")),

		// ── Source roster live reload and git-host content diff ─────
		"SourceReloadReport", obj("type", "object", "properties", obj(
			"added", obj("type", "array", "items", obj("type", "string")),
			"removed", obj("type", "array", "items", obj("type", "string")),
			"rotated", obj("type", "array", "items", obj("type", "string")),
			"unchanged", obj("type", "integer"),
			"rejected", obj("type", "array", "items", obj("type", "object",
				"description", "One source the reconciler could not apply, with the reason.")),
			"requires_restart", obj("type", "array", "items", obj("type", "string"),
				"description", "Configuration domains this live reload does NOT cover; changes to them need a restart."),
		), "required", arr("unchanged")),
		"GitHostDiff", obj("type", "object", "properties", obj(
			"head_commit", obj("type", "string"),
			"head_tree", obj("type", "string"),
			"truncated", obj("type", "boolean", "description", "The diff exceeded the read cap and was cut."),
			"files", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"path", obj("type", "string"),
				"previous_path", obj("type", "string", "description", "Set on a rename."),
				"status", obj("type", "string"),
				"binary", obj("type", "boolean"),
				"truncated", obj("type", "boolean"),
				"hunks", obj("type", "array", "items", obj("type", "string")),
			), "required", arr("path", "status", "binary", "truncated", "hunks"))),
		), "required", arr("head_commit", "truncated", "files")),

		// ── Disaster recovery ────────────────────────────────
		"DRBackup", obj("type", "object", "properties", obj(
			"id", obj("type", "string", "description", "The bundle's filename."),
			"filename", obj("type", "string"),
			"size_bytes", obj("type", "integer", "format", "int64"),
			"created_at", obj("type", "string", "format", "date-time"),
			"engine", obj("type", "string", "enum", arr("sqlite", "postgres")),
			"engine_version", obj("type", "string"),
			"tenant_count", obj("type", "integer"),
			"notes", obj("type", "string"),
		), "required", arr("id", "filename", "size_bytes", "created_at", "engine", "tenant_count")),
		"DRBackupDetail", obj("type", "object", "properties", obj(
			"id", obj("type", "string"),
			"filename", obj("type", "string"),
			"size_bytes", obj("type", "integer", "format", "int64"),
			"manifest", ref("DRManifest"),
		), "required", arr("id", "filename", "size_bytes", "manifest")),
		"DRManifest", obj("type", "object", "properties", obj(
			"format", obj("type", "string", "description", "The manifest format; a reader rejects an unknown format."),
			"created_at", obj("type", "string", "format", "date-time", "description", "The instant the backup was taken; RPO at a disaster is time-of-disaster minus this."),
			"engine", obj("type", "string", "enum", arr("sqlite", "postgres")),
			"engine_version", obj("type", "string", "description", "The engine binary version that produced the bundle."),
		), "required", arr("format", "created_at", "engine")),
		"DRRestoreUpload", obj("type", "object", "properties", obj(
			"upload_id", obj("type", "string"),
			"manifest", ref("DRManifest"),
			"filename", obj("type", "string"),
		), "required", arr("upload_id", "manifest", "filename")),
		"DRRestoreApply", obj("type", "object", "description",
			"The apply outcome: a job id (single-actor path) OR an awaiting-approval request id (dual-control path).",
			"properties", obj(
				"job_id", obj("type", "string"),
				"awaiting_approval", obj("type", "boolean", "description", "True when the dual-control gate armed and the restore records an intent instead of running."),
				"request_id", obj("type", "string", "description", "The pending request a distinct administrator approves."),
				"initiator", obj("type", "string"),
			)),
		"DRPendingRestore", obj("type", "object", "properties", obj(
			"request_id", obj("type", "string"),
			"upload_id", obj("type", "string"),
			"initiator", obj("type", "string"),
			"initiator_user", obj("type", "string"),
			"created_at", obj("type", "string", "format", "date-time"),
		), "required", arr("request_id", "upload_id", "initiator", "created_at")),
		"DRJob", obj("type", "object", "properties", obj(
			"id", obj("type", "string"),
			"kind", obj("type", "string", "enum", arr("backup", "restore")),
			"status", obj("type", "string"),
			"phase", obj("type", "string"),
			"progress", obj("type", "integer", "minimum", 0, "maximum", 100),
			"bundle_id", obj("type", "string"),
			"notes", obj("type", "string"),
			"error", obj("type", "string"),
			"started_at", obj("type", "string", "format", "date-time"),
			"done_at", obj("type", "string", "format", "date-time"),
		), "required", arr("id", "kind", "status", "phase", "progress", "started_at")),
		"DRSchedule", obj("type", "object", "properties", obj(
			"enabled", obj("type", "boolean"),
			"cron", obj("type", "string"),
			"retain_days", obj("type", "integer", "minimum", 0),
			"last_run", obj("type", "string", "format", "date-time"),
			"next_run", obj("type", "string", "format", "date-time"),
			"last_run_status", obj("type", "string", "enum", arr("completed", "failed"),
				"description", "The most recent scheduled run's outcome, so a failing schedule is not a silent gap."),
			"last_run_error", obj("type", "string"),
			"require_dual_control_restore", obj("type", "boolean",
				"description", "The effective console restore gate. When armed, a distinct administrator account must approve; CLI restore uses a declared-operator record instead, so estates requiring approval for every restore must also control host access."),
			"dual_control_disarm_effective_at", obj("type", "string", "format", "date-time"),
			"dual_control_disarm_requested_by", obj("type", "string"),
		), "required", arr("enabled", "cron", "retain_days", "require_dual_control_restore")),
		"DRScheduleInput", obj("type", "object", "additionalProperties", false,
			"description", "The schedule's operator-owned fields. The armed state of the dual-control gate is preserved; server-owned bookkeeping (disarm instant, run history) is ignored on write.",
			"properties", obj(
				"enabled", obj("type", "boolean"),
				"cron", obj("type", "string", "description", "The runner's cron grammar; validated on write."),
				"retain_days", obj("type", "integer", "minimum", 0),
				"require_dual_control_restore", obj("type", "boolean",
					"description", "Arm (true) or request the disarm of (false) the dual-control restore gate. Disarm takes effect at the engine's persisted instant, never immediately."),
			), "required", arr("enabled", "cron", "retain_days")),

		// ── Enterprise activation ────────────────────────────
		"ActivationStatus", obj("type", "object", "properties", obj(
			"edition", obj("type", "string"),
			"preset", obj("type", "string"),
			"restart_required", obj("type", "boolean"),
			"addons", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"key", obj("type", "string"),
				"title", obj("type", "string"),
				"summary", obj("type", "string"),
				"env", obj("type", "string"),
				"preset", obj("type", "string"),
				"state", obj("type", "string", "enum", arr("active", "pending", "available", "console")),
				"reason", obj("type", "string"),
				"needs_secret", obj("type", "boolean"),
				"in_build", obj("type", "boolean", "description", "The edition's catalog has the add-on. Absent means not known; false is an observed absence."),
				"license_covered", obj("type", "boolean", "description", "The verified licence covers it. Absent means not known; false is an observed absence."),
			), "required", arr("key", "title", "summary", "env", "preset", "state"))),
			"presets", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"name", obj("type", "string"),
				"addons", obj("type", "array", "items", obj("type", "string")),
			), "required", arr("name", "addons"))),
			"restarting", obj("type", "boolean"),
		), "required", arr("edition", "restart_required", "addons", "presets")),
		"ActivationPlan", obj("type", "object", "properties", obj(
			"preset", obj("type", "string"),
			"changes", obj("type", "boolean"),
			"entries", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"addon", obj("type", "string"),
				"action", obj("type", "string", "enum", arr("activate", "stage", "unchanged", "console")),
				"state", obj("type", "string"),
				"reason", obj("type", "string"),
			), "required", arr("addon", "action"))),
		), "required", arr("preset", "changes", "entries")),

		// ── Module selection ────────────────────────────────────────
		"ModuleSelection", obj("type", "object", "properties", obj(
			"modules", obj("type", "array", "items", obj("type", "object", "properties", obj(
				"name", obj("type", "string"),
				"selected", obj("type", "boolean", "description", "The administrator chose it."),
				"running", obj("type", "boolean", "description", "It runs on this node now (selected, required, or always on)."),
				"always_on", obj("type", "boolean", "description", "The engine cannot run without it; it cannot be deselected."),
				"requires", obj("type", "array", "items", obj("type", "string")),
				"required_by", obj("type", "array", "items", obj("type", "string"),
					"description", "The running modules that keep it on although it is not selected."),
				"holds_data", obj("type", "boolean",
					"description", "Its tables hold rows in this installation. A module that is not running keeps that data, but nothing acts on it."),
				"activated_by", obj("type", "array", "items", obj("type", "string"),
					"description", "The active edition add-ons that run it although it may not be selected."),
			), "required", arr("name", "selected", "running"))),
			"restarting", obj("type", "boolean", "description", "The engine is restarting itself to apply the change."),
			"running_sessions", obj("type", "integer",
				"description", "Sessions this engine runs now. The restart that applies a change stops every one of them; each can be resumed."),
		), "required", arr("modules", "running_sessions")),

		// ── Engine log buffer ───────────────────────────────────────
		"LogBuffer", obj("type", "object", "properties", obj(
			"items", obj("type", "array", "items", obj("type", "object",
				"description", "One engine log entry, newest last.")),
			"total", obj("type", "integer", "description", "Entries in the ring that matched the filter — the size of the set, not of this page."),
			"returned", obj("type", "integer", "description", "Entries this response carries."),
			"truncated", obj("type", "boolean", "description", "Older matches were left out."),
			"capture_level", obj("type", "string", "description", "The level the engine currently captures at."),
		), "required", arr("items", "total", "returned", "truncated", "capture_level")),
	)

	addMCPGatewayContracts(paths, schemas)
	schemas["FederationAssuranceMapping"] = obj("type", "object", "properties", obj(
		"amr", obj("type", arr("array", "null"), "items", obj("type", "string")),
		"acr", obj("type", arr("array", "null"), "items", obj("type", "string")),
		"saml_contexts", obj("type", arr("array", "null"), "items", obj("type", "string")),
	), "description", "Exact upstream MFA values. Omitted/null amr accepts mfa or a signed combination of knowledge (pwd/pin) and possession (otp/hwk/swk); a single method never counts by default. Explicit amr lists replace these defaults with operator-owned exact matches. Omitted/null saml_contexts defaults to https://refeds.org/profile/mfa. ACR has no implicit values. Empty lists trust none. An empty object restores defaults; omission preserves the stored mapping.")
	addTOTPContract(paths, schemas)
	addOSAccountContract(paths, schemas)
	stampCorePermissions(paths)
	applyOperationDescriptions(paths)

	return obj(
		"openapi", "3.1.0",
		"info", obj(
			"title", "Olivares AI control plane API",
			"version", "v1",
			"description", "REST contract for the Olivares AI control plane. Authentication is an opaque bearer token (session olvs_… or API key olvk_…). Tenant is resolved from a bound token or the X-Olivares-Tenant header.",
			"license", obj("name", "AGPL-3.0-only"),
			"x-stability-policy", stabilityPolicyURL),
		"servers", arr(obj("url", "/", "description", "this engine")),
		"security", bearer,
		"tags", arr(
			obj("name", "health", "description", "Probes, metrics and server status"),
			obj("name", "auth", "description", "Authentication, sessions, and setup"),
			obj("name", "agents", "description", "Agent lifecycle and access edges"),
			obj("name", "audit", "description", "Tamper-evident evidence ledger"),
			obj("name", "users", "description", "User management (superadmin)"),
			obj("name", "tokens", "description", "API token lifecycle"),
			obj("name", "workspaces", "description", "Workspace scoping"),
			obj("name", "system", "description", "Tenant provisioning and memberships"),
			obj("name", "connectors", "description", "Connector health monitoring"),
			obj("name", "console", "description", "Console admin operations (secrets, sources, connectors, SSO, license)"),
			obj("name", "directory", "description", "Directory: agent groups, provisioned groups, invitations, onboarding"),
		),
		"components", obj(
			"securitySchemes", obj("bearerAuth", obj(
				"type", "http", "scheme", "bearer",
				"description", "Opaque session (olvs_) or API (olvk_) token."),
				"browserSession", obj("type", "apiKey", "in", "cookie", "name", browserSessionCookie,
					"description", "HttpOnly browser session. Mutations require the per-session X-CSRF-Token header.")),
			"schemas", schemas,
		),
		"paths", paths,
	)
}

// auditFormatEnum renders the ledger export formats for the OpenAPI parameter enum
// from the engine's own registry, so the published contract, the handler's accepted
// values and the generated clients cannot disagree.
func auditFormatEnum() []any {
	known := audit.Formats()
	out := make([]any, len(known))
	for i, f := range known {
		out[i] = string(f)
	}
	return out
}

// apiErrorSchema is shared by stable and beta REST documents. Keep the stable
// schema byte-for-byte equivalent; protocol-specific errors keep their schemas.
func apiErrorSchema() map[string]any {
	return oas.Obj("type", "object", "properties", oas.Obj(
		"error", oas.Obj("type", "object", "properties", oas.Obj(
			"code", oas.Obj("type", "string"),
			"message", oas.Obj("type", "string"),
			"module", oas.Obj("type", "string", "description", "With code module_not_enabled: the namespace of the module this node does not run."),
		), "required", []any{"code", "message"}),
	), "required", []any{"error"})
}

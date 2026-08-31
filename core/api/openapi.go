// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/audit"
)

// The OpenAPI document is the published REST contract (DoD: "OpenAPI/proto
// publicados"). It is built programmatically (so it is always valid JSON) once
// per Server (in New — per-Server, not a package-level once, so a test-swapped
// deprecation table is honored by the served endpoint too). Modules'
// /v1/m/<ns>/ routes are not enumerated here — a module publishes its own
// sub-spec; this document describes the engine surface that is stable for the
// web UI and SDK clients.

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
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

	bearer := []any{obj("bearerAuth", []any{})}
	noAuth := []any{obj()}

	tenantParam := obj("name", "X-Olivares-Tenant", "in", "header", "required", false,
		"description", "Target tenant id; required when the principal can act in more than one tenant.",
		"schema", obj("type", "string", "format", "uuid"))
	idParam := obj("name", "id", "in", "path", "required", true,
		"description", "Resource identifier (UUIDv7).",
		"schema", obj("type", "string", "format", "uuid"))
	tenantPathParam := obj("name", "tenant_id", "in", "path", "required", true,
		"description", "Tenant identifier (UUIDv7).",
		"schema", obj("type", "string", "format", "uuid"))
	limitParam := obj("name", "limit", "in", "query", "required", false,
		"description", "Maximum number of items to return.",
		"schema", obj("type", "integer", "minimum", 1, "maximum", 1000, "default", 50))
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

	paths := obj(
		// ── Health ──────────────────────────────────────────────────────
		"/healthz", obj("get", op("healthz", "Liveness probe", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/livez", obj("get", op("livez", "Liveness probe (process is up)", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/readyz", obj("get", op("readyz", "Readiness probe (store reachable AND this node is the active writer); 503 on a standby or when the store is down", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/pod-readyz", obj("get", op("podReadyz", "Pod-health probe (store reachable), with NO leadership check — the HA readiness probe, so a hot standby is healthy; 503 only when the store is down", tagHealth, false,
			jsonResp("OK", obj("type", "object", "properties", obj("status", obj("type", "string")))), nil)),
		"/metrics", obj("get", rawOp("getMetrics", "Prometheus exposition (text format 0.0.4) of engine metrics",
			tagHealth, false, "Prometheus text exposition", "text/plain")),
		"/openapi.json", obj("get", op("getOpenAPI", "This OpenAPI document", tagHealth, false,
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
		"/v1/auth/logout", obj("post", op204("logout", "Revoke the calling session", tagAuth, true)),
		"/v1/auth/refresh", obj("post", op("refreshToken", "Renew the calling session token (rotates the credential, extends expiry)", tagAuth, true,
			jsonResp("OK", ref("LoginResponse")), nil)),
		"/v1/auth/whoami", obj("get", op("whoami", "The calling principal and its tenant grants", tagAuth, true,
			jsonResp("OK", ref("WhoamiResponse")), nil)),

		// ── Agents ─────────────────────────────────────────────────────
		"/v1/agents", obj(
			"get", op("listAgents", "List agents in the resolved tenant", tagAgents, true,
				jsonResp("OK", listOf(ref("Agent"))),
				nil, tenantParam, limitParam, cursorParam),
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
				jsonResp("Created", ref("User")),
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
			"get", op("listTokens", "List API tokens for the calling user", tagTokens, true,
				jsonResp("OK", listOf(ref("Token"))), nil),
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
		"/v1/memberships", obj("post", op201("grantMembership", "Grant a user a role in a tenant", tagSystem, true,
			jsonResp("Created", obj("type", "object", "properties", obj(
				"id", obj("type", "string", "format", "uuid"),
				"user_id", obj("type", "string", "format", "uuid"),
				"tenant", obj("type", "string"),
				"role", obj("type", "string"),
			))),
			body(ref("GrantMembershipInput")))),
		"/v1/members", obj("get", op("listMembers", "List the resolved tenant's member roster (role, workspace scoping, groups)", tagSystem, true,
			jsonResp("OK", listOf(ref("RosterMember"))),
			nil, tenantParam)),

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

		// ── System (orgs) ──────────────────────────────────────────────
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
	)

	applyStability(paths)

	schemas := obj(
		// ── Error envelope ──────────────────────────────────────────
		"Error", obj("type", "object", "properties", obj(
			"error", obj("type", "object", "properties", obj(
				"code", obj("type", "string"),
				"message", obj("type", "string"),
			), "required", arr("code", "message")),
		), "required", arr("error")),

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

		"CreateUserInput", obj("type", "object", "properties", obj(
			"email", obj("type", "string", "format", "email"),
			"display_name", obj("type", "string"),
			"password", obj("type", "string", "format", "password", "minLength", 12),
			"superadmin", obj("type", "boolean", "default", false),
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

		"LoginResponse", obj("type", "object", "properties", obj(
			"token", obj("type", "string", "description", "Opaque session token (olvs_…)"),
			"session_id", obj("type", "string", "format", "uuid"),
			"expires_at", obj("type", "string", "format", "date-time"),
		), "required", arr("token", "session_id", "expires_at")),

		"WhoamiResponse", obj("type", "object", "properties", obj(
			"kind", obj("type", "string", "enum", arr("user", "token")),
			"user_id", obj("type", "string", "format", "uuid"),
			"actor", obj("type", "string"),
			"display_name", obj("type", "string"),
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
			), "required", arr("tenant", "role", "permissions"))),
			"aal", obj("type", "integer", "description", "Authentication assurance level (sessions only)"),
			"amr", obj("type", "array", "items", obj("type", "string"), "description", "Authentication method references (sessions only)"),
		), "required", arr("kind", "user_id", "actor", "superadmin")),

		// ── Server info ─────────────────────────────────────────────
		"ServerInfo", obj("type", "object", "properties", obj(
			"version", obj("type", "string"),
			"engine", obj("type", "string"),
			"setup_required", obj("type", "boolean"),
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
			"purpose", obj("type", "string", "enum", arr("audit", "catalog", "policy", "license", "eventing", "sso", "secret-store")),
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
	)

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
		),
		"components", obj(
			"securitySchemes", obj("bearerAuth", obj(
				"type", "http", "scheme", "bearer",
				"description", "Opaque session (olvs_) or API (olvk_) token.")),
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

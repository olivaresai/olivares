<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<!-- Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root. -->

# OpenAPI route classification (2026-10-06 census)

One-time route publication decision record. Census: every HTTP route registered in source but
in neither published OpenAPI document (`web/openapi/openapi.json`, `openapi.beta.json`). The issue
counted 167; the census below lists 171 rows (70 + 36 + 65), each method-agnostic MCP transport being
one row. Measured by this change: the 65 core operations added to the stable document and the 5 module
operations added to the beta snapshot are the diff of the regenerated documents against main; the 36
protocol rows are `routeClassifications`; the 65 Business rows come from the editions catalog of
the Business distribution, which this repository cannot walk,
so they are recorded here and not enforced. This table is the classification of every one of
them; the LIVE enforcement is not this file but the in-code table
`core/api/openapi_route_classification.go` and the test that walks the real router,
`cmd/olivares/openapi_route_classification_test.go` (every mounted operation is published or
classified, a stale or hiding row is red).

Classes (the four the issue names):

- **Community public API** — the REST contract of the Community product for the web UI and SDK
  clients; published in the generated OpenAPI (core routes in the stable document, module routes
  in the beta reflector). 70 operations: 65 core, published by this change; 5 module routes the
  beta reflector already covers (their committed snapshot had lagged one integration batch).
- **protocol endpoint** — implements an external protocol whose own specification is the contract
  (SCIM 2.0, SSF/CAEP, OAuth 2.0/OIDC/SAML federation, OpenID AuthZEN 1.0, MCP transports).
  36 operations; each row in the Go table carries its reason. Not published in the OpenAPI
  documents because an external conformant client — an IdP, a PEP, an MCP host — consumes the
  protocol, not the Olivares SDK surface.
- **internal** — an engine mechanism that is not a client contract. The census found none: every
  remaining Community route is a public console/directory surface beside already-published
  siblings. The class stays available (with a reason) for a future census.
- **Business** — registered only in the Business distribution (the `session-cockpit` and
  `ldap` module namespaces); ships in the Business distribution, which publishes its own
  documents. 65 operations; not a Community gap. This repository cannot mount them, so the
  enforceable table does not list them.

Known limit of the guard: the test mounts the production module set with no runtime services, so a
route mounted only when an optional service is wired is not walked. Today that is the three MCP gateway
transports (`mcpGatewayRuntime`); their rows are `optional` and method-agnostic (`*`), which covers every
method chi would expand them into if the gateway were wired, and is red if any of them is ever published.

## Community public API — 70 operations (published by this change)

Core routes, added to the stable document (`core/api/openapi.go`):

| Method | Path |
| --- | --- |
| GET | `/openapi.beta.json` |
| GET | `/v1/agent-groups` |
| POST | `/v1/agent-groups` |
| DELETE | `/v1/agent-groups/{id}` |
| GET | `/v1/agent-groups/{id}` |
| PATCH | `/v1/agent-groups/{id}` |
| GET | `/v1/agent-groups/{id}/members` |
| DELETE | `/v1/agent-groups/{id}/members/{agentID}` |
| PUT | `/v1/agent-groups/{id}/members/{agentID}` |
| GET | `/v1/auth/effective-rights` |
| POST | `/v1/auth/piv/elevate` |
| GET | `/v1/auth/piv/status` |
| POST | `/v1/auth/webauthn/authenticate` |
| POST | `/v1/auth/webauthn/authenticate/options` |
| GET | `/v1/auth/webauthn/credentials` |
| DELETE | `/v1/auth/webauthn/credentials/{id}` |
| PATCH | `/v1/auth/webauthn/credentials/{id}` |
| POST | `/v1/auth/webauthn/register` |
| POST | `/v1/auth/webauthn/register/options` |
| GET | `/v1/console/activation` |
| POST | `/v1/console/activation/apply` |
| POST | `/v1/console/activation/preview` |
| POST | `/v1/console/dr/backup` |
| GET | `/v1/console/dr/backups` |
| DELETE | `/v1/console/dr/backups/{id}` |
| GET | `/v1/console/dr/backups/{id}` |
| GET | `/v1/console/dr/backups/{id}/download` |
| GET | `/v1/console/dr/jobs` |
| GET | `/v1/console/dr/jobs/{id}/stream` |
| GET | `/v1/console/dr/restore/pending` |
| POST | `/v1/console/dr/restore/upload` |
| POST | `/v1/console/dr/restore/{id}/apply` |
| POST | `/v1/console/dr/restore/{id}/approve` |
| GET | `/v1/console/dr/schedule` |
| PUT | `/v1/console/dr/schedule` |
| GET | `/v1/console/logs/buffer` |
| GET | `/v1/console/logs/stream` |
| GET | `/v1/console/modules` |
| PUT | `/v1/console/modules` |
| POST | `/v1/console/runtime/reload` |
| GET | `/v1/console/sources/diff` |
| GET | `/v1/console/sso/idps` |
| DELETE | `/v1/console/sso/idps/{alias}` |
| GET | `/v1/console/sso/idps/{alias}` |
| PUT | `/v1/console/sso/idps/{alias}` |
| POST | `/v1/console/sso/idps/{alias}/test` |
| DELETE | `/v1/console/sso/tenants/{tenant}` |
| GET | `/v1/console/sso/tenants/{tenant}` |
| PUT | `/v1/console/sso/tenants/{tenant}` |
| GET | `/v1/console/sso/tenants/{tenant}/idps` |
| DELETE | `/v1/console/sso/tenants/{tenant}/idps/{alias}` |
| GET | `/v1/console/sso/tenants/{tenant}/idps/{alias}` |
| PUT | `/v1/console/sso/tenants/{tenant}/idps/{alias}` |
| POST | `/v1/console/sso/tenants/{tenant}/idps/{alias}/test` |
| POST | `/v1/console/sso/tenants/{tenant}/test` |
| GET | `/v1/groups` |
| PUT | `/v1/groups/{id}/parent` |
| PUT | `/v1/groups/{id}/role` |
| GET | `/v1/invites` |
| POST | `/v1/invites/accept` |
| DELETE | `/v1/invites/{id}` |
| POST | `/v1/invites/{id}/resend` |
| POST | `/v1/onboard` |
| GET | `/v1/workspaces/{id}/contents` |
| GET | `/v1/workspaces/{id}/summary` |

Module routes, covered by the beta reflector (snapshot regenerated):

| Method | Path |
| --- | --- |
| GET | `/v1/m/agenttools/providers` |
| GET | `/v1/m/governance/rbac/inheritance-filters` |
| POST | `/v1/m/governance/rbac/inheritance-filters` |
| DELETE | `/v1/m/governance/rbac/inheritance-filters/{id}` |
| GET | `/v1/m/governance/rbac/inheritance-filters/{id}` |

## Protocol endpoints — 36 operations (classified, not published)

| Method | Path | Protocol |
| --- | --- | --- |
| GET | `/.well-known/authzen-configuration` | OpenID AuthZEN 1.0 |
| GET | `/.well-known/oauth-authorization-server` | OAuth 2.0 / OIDC / SAML |
| * | `/.well-known/oauth-protected-resource/mcp/gateway/{tenant}/{id}` | MCP transport / RFC 9728 |
| POST | `/access/v1/access-review/export` | OpenID AuthZEN 1.0 |
| POST | `/access/v1/evaluation` | OpenID AuthZEN 1.0 |
| POST | `/access/v1/evaluations` | OpenID AuthZEN 1.0 |
| POST | `/access/v1/search/action` | OpenID AuthZEN 1.0 |
| POST | `/access/v1/search/resource` | OpenID AuthZEN 1.0 |
| POST | `/access/v1/search/subject` | OpenID AuthZEN 1.0 |
| * | `/mcp/gateway/{tenant}/{id}` | MCP transport / RFC 9728 |
| * | `/session/mcp` | MCP transport / RFC 9728 |
| GET | `/v1/auth/federation/callback` | OAuth 2.0 / OIDC / SAML |
| POST | `/v1/auth/federation/callback` | OAuth 2.0 / OIDC / SAML |
| GET | `/v1/auth/federation/saml/metadata` | OAuth 2.0 / OIDC / SAML |
| GET | `/v1/auth/federation/start` | OAuth 2.0 / OIDC / SAML |
| POST | `/v1/auth/token` | OAuth 2.0 / OIDC / SAML |
| POST | `/v1/auth/token-exchange` | OAuth 2.0 / OIDC / SAML |
| POST | `/v1/scim/v2/Events` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Groups` | SCIM 2.0 (RFC 7644) |
| POST | `/v1/scim/v2/Groups` | SCIM 2.0 (RFC 7644) |
| DELETE | `/v1/scim/v2/Groups/{id}` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Groups/{id}` | SCIM 2.0 (RFC 7644) |
| PATCH | `/v1/scim/v2/Groups/{id}` | SCIM 2.0 (RFC 7644) |
| PUT | `/v1/scim/v2/Groups/{id}` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/ResourceTypes` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/ResourceTypes/{type}` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Schemas` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Schemas/{urn}` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/ServiceProviderConfig` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Users` | SCIM 2.0 (RFC 7644) |
| POST | `/v1/scim/v2/Users` | SCIM 2.0 (RFC 7644) |
| DELETE | `/v1/scim/v2/Users/{id}` | SCIM 2.0 (RFC 7644) |
| GET | `/v1/scim/v2/Users/{id}` | SCIM 2.0 (RFC 7644) |
| PATCH | `/v1/scim/v2/Users/{id}` | SCIM 2.0 (RFC 7644) |
| PUT | `/v1/scim/v2/Users/{id}` | SCIM 2.0 (RFC 7644) |
| POST | `/v1/ssf/events` | SSF/CAEP |

## Business — 65 operations (enterprise registrations, not Community)

All 65 are module routes of two namespaces registered only in the private enterprise tree:
`/v1/m/session-cockpit/…` (55, the Business session cockpit) and `/v1/m/ldap/…` (10, the LDAP
directory connector).

| Method | Path |
| --- | --- |
| GET | `/v1/m/ldap/directories` |
| POST | `/v1/m/ldap/directories` |
| GET | `/v1/m/ldap/directories/{directory_id}` |
| PUT | `/v1/m/ldap/directories/{directory_id}` |
| POST | `/v1/m/ldap/directories/{directory_id}/activate` |
| POST | `/v1/m/ldap/directories/{directory_id}/check` |
| PUT | `/v1/m/ldap/directories/{directory_id}/credential` |
| POST | `/v1/m/ldap/directories/{directory_id}/disable` |
| POST | `/v1/m/ldap/directories/{directory_id}/groups` |
| POST | `/v1/m/ldap/directories/{directory_id}/sync` |
| POST | `/v1/m/session-cockpit/adoptions` |
| GET | `/v1/m/session-cockpit/agents` |
| GET | `/v1/m/session-cockpit/agents/{agent_id}` |
| PUT | `/v1/m/session-cockpit/agents/{agent_id}/policy` |
| GET | `/v1/m/session-cockpit/departments` |
| PUT | `/v1/m/session-cockpit/departments/{department_key}/agent-group` |
| PUT | `/v1/m/session-cockpit/detector-sets/{detector_set_id}/versions/{version}` |
| PUT | `/v1/m/session-cockpit/environment-policies/{policy_id}` |
| DELETE | `/v1/m/session-cockpit/input-sessions/{input_session_id}` |
| POST | `/v1/m/session-cockpit/input-sessions/{input_session_id}/resume` |
| POST | `/v1/m/session-cockpit/kill-switches` |
| DELETE | `/v1/m/session-cockpit/kill-switches/{scope_kind}/{scope_id}` |
| POST | `/v1/m/session-cockpit/launches` |
| GET | `/v1/m/session-cockpit/nodes` |
| POST | `/v1/m/session-cockpit/nodes` |
| GET | `/v1/m/session-cockpit/nodes/{node_id}` |
| PUT | `/v1/m/session-cockpit/nodes/{node_id}/cwd-refs/{cwd_ref}` |
| POST | `/v1/m/session-cockpit/nodes/{node_id}/enrollment-tokens` |
| POST | `/v1/m/session-cockpit/nodes/{node_id}/revoke` |
| PUT | `/v1/m/session-cockpit/nodes/{node_id}/unix-bindings/{binding_id}` |
| GET | `/v1/m/session-cockpit/operations/{operation_id}` |
| PUT | `/v1/m/session-cockpit/permission-policies/{policy_id}` |
| GET | `/v1/m/session-cockpit/redaction-policies` |
| PUT | `/v1/m/session-cockpit/redaction-policies/{policy_id}` |
| GET | `/v1/m/session-cockpit/retention-policies` |
| PUT | `/v1/m/session-cockpit/retention-policies/{policy_id}` |
| GET | `/v1/m/session-cockpit/runtime-input-policies` |
| PUT | `/v1/m/session-cockpit/runtime-input-policies/{policy_id}` |
| GET | `/v1/m/session-cockpit/runtime-preparation` |
| GET | `/v1/m/session-cockpit/runtime-profiles` |
| PUT | `/v1/m/session-cockpit/runtime-profiles/{profile_id}` |
| GET | `/v1/m/session-cockpit/sessions` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/audit` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/events` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/input-sessions` |
| DELETE | `/v1/m/session-cockpit/sessions/{core_session_id}/input-sessions/{input_session_id}` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/input-sessions/{input_session_id}/frames` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/inputs` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/interrupts` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/messages` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/observations` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/raw-transcript` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/raw-transcript-exports` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/relaunches` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/stops` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/stream` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/stream-leases` |
| GET | `/v1/m/session-cockpit/sessions/{core_session_id}/transcript` |
| POST | `/v1/m/session-cockpit/sessions/{core_session_id}/transcript-exports` |
| POST | `/v1/m/session-cockpit/shells` |
| PUT | `/v1/m/session-cockpit/stop-profiles/{profile_id}` |
| GET | `/v1/m/session-cockpit/usage` |
| GET | `/v1/m/session-cockpit/workspaces/{workspace_id}/runtime-launches` |
| POST | `/v1/m/session-cockpit/workspaces/{workspace_id}/runtime-launches/{runtime_launch_id}/preparations` |


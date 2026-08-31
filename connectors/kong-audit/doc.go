// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package kongaudit is the Olivares AI connector for Kong Gateway audit logging
//. It closes the gap "audit who changed the gateway config via
// the Admin API": Kong's Admin API is the control plane for every route, service,
// plugin, consumer and credential, and a change there silently reshapes what every
// proxied request can reach. This connector makes those control-plane accesses and
// changes first-class edges/findings in the access map.
//
// # Read-first / minimal-data posture (docs/SECURITY-HARDENING.md, §3)
//
//   - It OBSERVES an artifact Kong already produced: the audit log the operator
//     EXPORTS from Kong's two audit streams (the /audit/requests and /audit/objects
//     Admin API endpoints, or the file/collector they are shipped to) as JSON. It
//     never calls the Kong Admin API, never opens a listener, never mutates config,
//     never decrypts. The honest ingest path is: the operator exports the audit
//     entries to a JSON file (or a directory of them) and points "path" at it.
//   - It emits ONLY structural metadata: the acting RBAC identity (or client IP),
//     the Admin API path/method, the changed entity's type and key, the operation
//     verb and the timestamp. It NEVER emits a request/response body. Kong's
//     audit_requests carries a "payload" (the request body — e.g. a new consumer's
//     fields) and audit_objects carries an "entity" (the full snapshot of the
//     changed row, which for a credential/secret entity can hold sensitive values);
//     this connector reads NEITHER. The record struct deliberately omits both, and
//     a negative test asserts an embedded secret never reaches an observation.
//   - Mode is taken VERBATIM from the source (ARCHITECTURE.md): the HTTP method of the
//     Admin API request, never guessed. An unrecognized method yields ModeUnknown.
//
// # The two Kong audit streams (verified against the authority)
//
// Kong Gateway writes two distinct audit record shapes (verified against
// developer.konghq.com/gateway/audit-logs/ and the field tables/example JSON in the
// Kong Gateway Admin API "Audit Log" reference):
//
//   - audit_requests — one record per Admin API request. Fields read:
//     request_id, request_timestamp (Unix epoch seconds), client_ip, path, method,
//     status, rbac_user_id, workspace. Deliberately NOT read: payload (request body).
//     (Some Kong builds also surface rbac_user_name / rbac_user / request_source;
//     the struct tolerates those for attribution but reads no payload.)
//   - audit_objects — one record per entity CHANGE. Fields read:
//     id, request_id, dao_name (the entity's DAO/type, e.g. "routes", "consumers",
//     "plugins"), entity_key (the entity's primary key), operation
//     ("create" | "update" | "delete"), rbac_user_id, expire, ttl. Deliberately NOT
//     read: entity (the full row snapshot). audit_objects records carry no direct
//     timestamp in the documented shape, so OccurredAt is reconstructed from
//     (expire - ttl) when present (Kong sets expire = creation + record TTL),
//     falling back to an explicit request_timestamp if a build supplies one, and to
//     the connector clock otherwise — documented, never fabricated.
//
// Kong has no "entity_type" field; the entity's type is dao_name. The spec's
// "<entity_type>:<entity_key>" subject is therefore "<dao_name>:<entity_key>".
//
// # What it emits onto the sealed sum type (ARCHITECTURE.md)
//
// For an audit_requests record (an Admin API access), one model.EdgeObservation —
// "who touched the control plane, R/RW":
//
//	OriginKind   "identity"
//	OriginRef    rbac_user_id (falling back to rbac_user_name/rbac_user, then client_ip)
//	ResourceKind "kong.admin_api"
//	ResourceRef  the request path (e.g. "/consumers")
//	Mode         from the HTTP method via meshobs.MethodToMode
//	             (GET/HEAD -> read; POST/PUT/PATCH -> readwrite; DELETE -> write; else unknown)
//	Source       SignalKongAudit ("kong_audit")
//	Confidence   Attributed when an RBAC user is named, else Approximate (a bare IP)
//	ToolRef      the HTTP method
//	ObservedAt   request_timestamp
//
// For an audit_objects record (a config CHANGE), one model.FindingReport — the
// audit value of the connector, "the gateway config changed and by whom":
//
//	Kind        "gateway_config_change"
//	Severity    Info   (a config change is normal operations, not by itself an
//	            incident; the access map / a downstream rule decides if a specific
//	            change is suspicious — this connector does not grade ops as alerts)
//	SubjectKind "kong.entity"
//	SubjectRef  "<dao_name>:<entity_key>"
//	Title       "kong <operation> <dao_name> by <rbac_user>"
//	DetailHash  redact.Hash of a stable key (request_id|operation|dao_name|entity_key)
//	OccurredAt  the reconstructed timestamp
//
// The two streams are complementary: audit_requests answers "who hit the Admin API
// and how" (the edge), audit_objects answers "what entity actually changed" (the
// finding); they correlate on request_id, which both carry, though this connector
// emits each independently (correlation is job).
//
// # Provenance and boundaries
//
// Source is a package-local open string (SignalKongAudit = "kong_audit"), declared
// here so the operator never silently collapses a Kong control-plane edge with a
// mesh L7 edge or a kernel L4 edge. The connector imports only the SDK, the Apache
// connectors/internal helpers (meshobs for the verbatim method->mode mapping,
// redact for the no-leak hashing) and the standard library — never the engine
// (/core), per LICENSING.md.
package kongaudit

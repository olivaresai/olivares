<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Core Engine

The engine that powers Olivares AI: ingest pipeline, event bus, data model, module runtime, REST + gRPC API, authentication and authorization (WebAuthn/FIDO2, PIV/CAC, AAL step-up, Cedar policy engine), the hash-chained Ed25519-signed audit ledger, multi-tenancy (RLS on Postgres, triggers on SQLite), and the embedded web UI server.

**License:** AGPL-3.0-only.

## Key packages

| Package | Purpose |
|---|---|
| `api/` | HTTP + gRPC API server, route handlers, middleware, module mounting |
| `auth/` | Authentication (passwords, WebAuthn, SAML, OIDC, API tokens), authorization (RBAC + deny-overlay), secret store, source roster |
| `audit/` | Append-only hash-chained audit ledger with Ed25519 checkpoint signing |
| `engine/` | Store construction, boot sequence, migration runner |
| `eventbus/` | In-process event bus + optional NATS transport |
| `model/` | Domain types: Agent, Session, Resource, Workspace, AgentGroup, SourceDef, … |
| `runtime/` | Module lifecycle, plugin supervisor (AutoMTLS), sandbox executor |
| `secret/` | Secret reference resolver (`store:`, `env:`, `vault:`, …) |
| `secure/` | KMS wrappers (AWS, GCP, Azure, KMIP), model signing, TLS |
| `store/` | Store interface, Scope, AuthScope, Repository, migrations |
| `internal/store/sqlstore/` | SQLite + Postgres implementation, RLS, schema reconciliation |

## Boundary rule

`core/` must never import from `modules/` or `connectors/` — the layering is strictly inward. Modules may depend on the core; connectors depend only on the SDK and never import the core. This is enforced by [`scripts/check-boundary.sh`](../scripts/check-boundary.sh) in CI.

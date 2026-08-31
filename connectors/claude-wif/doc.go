// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package claudewif is the Olivares AI identity connector for the Anthropic
// (Claude) organization: it makes Claude's API keys, workspaces, service accounts,
// organization members and Workload Identity Federation issuers/rules FIRST-CLASS
// non-human identities (NHI) in module VI (governance) and the module III R/RW
// access map — the part the FinOps catalog (connectors/claude-api) does not
// model. It is the identity sibling of claude-api: claude-api reads cost; this
// connector reads WHO can act and WHAT they are permitted to do.
//
// Three contracts, one connector (the Vault pattern, docs):
//
//   - identitysource.GraphProvider (Snapshot) exposes the NHI ROSTER — api keys,
//     service accounts and org members as Identity rows, workspaces and workspace
//     roles as Collections, and the belongings between them as Memberships. The
//     roster converges into the governance graph by external_id == the raw Anthropic
//     id (apikey_…, wrkspc_…, user_…, svac_…, fdis_…), the SAME id the FinOps/observed
//     side carries, so module III can diff permitted-vs-observed (ARCHITECTURE.md).
//
//   - sdk.SourceConnector (Gather) emits the PERMITTED grant edges as
//     model.EdgeObservation with Source=model.SignalPolicy: an API key is permitted
//     its workspace's API; a federated service account is permitted its rule's
//     oauth_scope in a workspace. It ALSO emits the GOVERNANCE FINDING for the
//     documented WIF footgun (a static ANTHROPIC_API_KEY silently shadows
//     federation). Memberships are not edges (no resource is touched), so they travel
//     only the typed Snapshot.
//
//   - Exchanger (wif.go) is the IDN-01 Workload Identity Federation helper: the
//     opt-in RFC 7523 jwt-bearer exchange (POST /v1/oauth/token) that mints a
//     short-lived sk-ant-oat token from an attested JWT-SVID/OIDC assertion, so a
//     workload never carries a static sk-ant- key. It is the ONLY credential-emitting
//     primitive in the product and is deliberately isolated here: it never persists
//     or logs the minted token, and it returns an audit-safe record (no secret) the
//     host writes to the ledger (docs/SECURITY-HARDENING.md). The composition root wires it; an
//     attested assertion comes from connectors/spiffe's live JWT-SVID verifier (IDN-07).
//
// Read-first and minimal-data (docs/SECURITY-HARDENING.md-3). Every API call this connector makes is a
// GET; it never creates or mutates an Anthropic object. It carries identity METADATA only
// — ids, names, emails, roles, key hints — never a key secret, never a private key, never
// the minted token at rest. Anthropic's WIF Admin API lists the federation
// issuers/rules/service accounts under an org:admin OAuth bearer token (Admin API keys are
// rejected there — a DISTINCT credential from the sk-ant-admin roster key); when that
// token is configured the connector LISTS the live config and reconciles it against the
// operator-declared federation, reporting declared-vs-actual drift (undeclared/over-broad/
// orphan) — never a fabricated roster. With no org:admin token it models exactly what the
// operator declares (an honest declared-only baseline). See reconcile.go.
//
// It imports only the SDK, the Apache identitysource and modelprovider contracts and
// the shared internal helpers — never the engine (/core). License boundary: Apache-2.0.
package claudewif

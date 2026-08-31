// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package catalog is module XIV — the internal curated catalog/marketplace
// (README.md). It curates and lets the organization reuse APPROVED agents, MCP
// servers, skills and templates ("company agents"), versioned and governed.
//
// Owned entities (catalog.<entity>):
//
//   - entry: a curated, versioned catalog entry — an approved-for-the-org
//     definition of a reusable capability/agent/MCP/skill/template. The catalog
//     is a REGISTRY: each (kind, slug, version) is its own immutable artifact, so
//     a new version is a new entry, and approval/signing happen per version. An
//     entry moves draft → pending → approved → deprecated; only a draft is
//     mutable, and approval FREEZES it.
//   - instance: a self-service instantiation request created FROM an approved
//     entry. This module exposes the catalog and the instantiation flow; the
//     permission/approval DECISION (HITL, who may approve) and the actual
//     provisioning belong to governance and deployment. The module
//     records the request, its provenance (which entry version), its status and a
//     self-audit; it does not enforce the approval policy.
//
// Versioning & signing (decision: semver + integrity, docs/SECURITY-HARDENING.md). Every approved
// entry is pinned by a CONTENT HASH (SHA-256 over its canonical preimage) so any
// later mutation is detectable, and the approval is recorded in the append-only,
// hash-chained ledger attributed to the real principal. When a catalog signing
// key is configured (module config "signing_key_path"), approval also produces a
// detached Ed25519 SIGNATURE over the content hash — "approved = verifiable",
// reusing the product's supply-chain posture. Without a key the entry is
// hash-pinned and ledger-attested but unsigned, and the API says so honestly. The
// module does NOT reach the engine's internal audit signer; it owns its catalog
// key via the same fail-closed seam the engine uses for its own keys
// (core/secure), keeping the trust boundary clean.
//
// Privileged & audited (docs/SECURITY-HARDENING.md): approving, signing, deprecating and
// instantiating are privileged actions, RBAC-gated by verb tier (approve/deprecate
// = admin; create/update/instantiate = write; reads = read) and recorded in the
// ledger attributed to the real principal.
//
// Minimal data (docs/SECURITY-HARDENING.md): an entry's spec is an operator-authored reusable
// DEFINITION (transport, model/prompt references, scope, secret REFERENCES), never
// a credential value; the create/approve path refuses a spec with inline
// credentials. The module stores definitions, references and governance metadata,
// never secrets or payloads.
//
// Layout: catalog.go (lifecycle + signer load) · schema.go (owned entities) ·
// sign.go (content hash + Ed25519) · entries.go (entry lifecycle + verify/pubkey)
// · instances.go (governed self-service) · dto.go + api.go (HTTP + the UI data
// contract).
package catalog

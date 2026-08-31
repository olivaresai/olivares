// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package oasf is the Olivares AI connector for AGNTCY/OASF agent descriptors —
// import, export and optional Agent Badge verification. It is EXPERIMENTAL
//: the descriptor schema itself is stable (OASF 1.0.0), but the AGNTCY
// identity layer that signs badges is not (see "Why EXPERIMENTAL" below), so
// the connector verifies what is honestly verifiable and labels everything else
// exactly as what it is.
//
// # What it reads
//
// Local OASF record JSON files (records_file / records_dir; one record object
// or an array per file) and enveloped Agent Badge credentials (badges_file /
// badges_dir; a {"envelope_type":"JOSE"|"EMBEDDED_PROOF","value":...} envelope,
// a bare VC JSON object, or a compact JWS string). A record that validates
// against the OASF 1.0.0 REQUIRED field set becomes one roster row
// (identitysource.Identity, PrincipalNHI, Kind "agent_descriptor", Ref
// "oasf:<name>@<version>") in the Graph this connector exposes through
// identitysource.GraphProvider. Badges only ever CORROBORATE a record (the
// ARCHITECTURE.md "corroborate, don't trust" posture) — they never create roster
// rows, and a record's badge verification status travels honestly in its
// "badge" attribute: "none", "verified", "unverified" or "invalid". With
// require_badge=true only "verified" records are rostered; everything else is
// denied and surfaced as a finding.
//
// # Read-only and minimal data (docs/SECURITY-HARDENING.md-3)
//
// The connector reads descriptor METADATA only. Its single optional network
// call is a GET of the operator-configured issuer JWKS (public key material,
// via the GET-only httpx client); there is no secret config field at all. It
// never requests, stores, logs or emits credential material; findings carry a
// stable hash of a non-sensitive key (never file contents), and finding titles
// name only a validation-reason CATEGORY.
//
// # Offline behavior
//
// With no records source configured (records_file and records_dir both empty)
// the connector is offline: Snapshot returns an empty Graph with Source and
// CapturedAt set and a nil error, and Gather returns nil. Open fails only for
// malformed configuration (both JWKS sources set at once, or an inline JWKS
// that does not parse), never for a missing one.
//
// # Primary-source facts the wire shapes were verified against
//
// Verified 2026-06-11:
//
//   - OASF schema 1.0.0, served from schema.oasf.outshift.com (AGNTCY has been
//     a Linux Foundation project since 2025-07). The old host
//     schema.oasf.agntcy.org is DEAD but is still referenced by AGNTCY's own
//     identity spec, so BOTH URLs are tolerated in a badge's credentialSchema.
//   - Record REQUIRED fields per the published OASF 1.0.0 JSON Schema: authors
//     (string[]), created_at (RFC 3339), description, name, schema_version,
//     version, skills (array; this connector additionally requires it
//     non-empty). Recommended: domains, modules. Optional: locators,
//     annotations. Because Go cannot distinguish an absent string from an empty
//     one, an empty required string is treated as missing.
//
// # Why EXPERIMENTAL (verified 2026-06-11)
//
// The AGNTCY identity spec (Agent Badges) is v1alpha1 and is NOT W3C VCDM 2.0
// conformant: it mixes VCDM 1.1 and 2.0 field names, and its DataIntegrityProof
// is incomplete (it lacks verificationMethod, created and cryptosuite). That is
// THE reason for the EXPERIMENTAL gate. Consequently badge verification is an
// honest four-state outcome:
//
//   - "verified" — ONLY a JOSE-enveloped badge (compact JWS) whose signature
//     verifies against the operator-configured issuer JWKS (asymmetric-alg
//     allowlist pinned at parse; HS*/none rejected by omission) and whose VC
//     payload's credentialSubject.badge embeds a record matching name+version.
//   - "unverified" — an EMBEDDED_PROOF (DataIntegrityProof) badge. We
//     deliberately do NOT implement W3C Data Integrity canonicalization against
//     the spec's non-conformant Proof, so such a badge is honestly unverifiable
//     here and is never trusted.
//   - "invalid" — a JWS whose signature/payload verification fails, or a
//     malformed badge. Each emits a FindingReport.
//   - "none" — no badge references the record.
//
// # Export (the primitive)
//
// Export(rec) is a pure helper that validates the same REQUIRED set, fills
// schema_version "1.0.0" when empty, and emits deterministic, fixed-field-order
// JSON. It is the primitive the export wave builds on; Import(Export(x))
// round-trips.
//
// # ANS awareness (documentation only — no code)
//
// ANS (Agent Name Service, OWASP GenAI Agentic Security Initiative): the v1
// IETF draft draft-narajala-ans-00 EXPIRED 2025-11-17. The active draft is
// draft-narajala-courtney-ansv2-01 (2026-04-13, an individual submission with
// NO working-group adoption, expires 2026-10-15); it introduces the naming
// scheme "ans://v{semver}.{agentHost}", a dual-certificate model and
// Bronze/Silver/Gold assurance tiers. There is no production public registry.
// This is awareness-only: this connector deliberately does NOT resolve ANS
// names, and nothing in the plane should until there is at least WG adoption
// (design-toward, no conformance claim — the docs/SECURITY-HARDENING.md labeling rule).
//
// It imports only the SDK, the Apache identitysource contract, the shared
// httpx/redact internals and go-jose — never the engine.
package oasf

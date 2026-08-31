// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package contentsource is the typed, Apache-licensed contract that the Olivares
// AI DATA connectors (Google Drive, Confluence, Notion, SharePoint, S3 object
// content) expose to module VIII — data, knowledge & context. It is the
// twin of connectors/identitysource: a typed Go vocabulary in /connectors that a
// module imports, NOT a fourth sealed observation type and NEVER the engine.
//
// # Why a typed contract and not an sdk.SourceConnector / a fourth observation
//
// The SDK's model.Observation sum type is sealed (S02 §3): a SourceConnector can
// only emit EdgeObservation, CostSample or FindingReport through its Sink. A
// DOCUMENT's content for retrieval-augmented generation is BULK REFERENCE DATA,
// not a flow fact — it cannot and must not travel that channel:
//
//   - it does not fit any sealed observation kind (cracking the frozen S02 wire
//     contract to add a "document" kind would reach into /core);
//   - the event bus is a fan-out backbone (S02 §4) — broadcasting document bodies
//     to every subscriber would violate minimal data (docs/SECURITY-HARDENING.md-3): content must
//     go straight into the governed knowledge store, redacted, never broadcast.
//
// So content travels a typed Go contract here, exactly as the identity roster and
// the model/provider catalog do (decisions). This is decision.
// A content connector implements contentsource.Source ONLY; it is pull-driven by
// the knowledge module (Open → List → Fetch* → Close), NOT scheduled by the
// runtime and NOT a participant in the event bus. It MUST NOT also implement
// sdk.SourceConnector (the two roles are kept separate; the module owns the
// ingest lifecycle, partial-failure cleanup and any FindingReport emission).
//
// # Boundary (LICENSING.md)
//
// This package imports only the standard library and github.com/olivaresai/
// olivares/sdk (for the shared Descriptor/Config declaration). It NEVER imports
// /core or /modules. The AGPL knowledge module imports THIS package (Apache →
// AGPL is allowed, exactly as imports identitysource); a connector never
// imports the module.
//
// # Minimal data and the red line (docs/SECURITY-HARDENING.md,§3,§9)
//
// A connector reads CONTENT and its PROVENANCE/permissions, not the secrets
// behind the source:
//
//   - Source credentials are configured BY REFERENCE to a secret-store (a
//     Descriptor ConfigField with Secret=true), NEVER inline (docs/SECURITY-HARDENING.md).
//   - A Document's ACL carries the source's permission references (group / role /
//     principal names), NEVER credential material.
//   - Document.Body MAY contain raw content that itself embeds a secret (a doc
//     that pastes an API key). The connector returns it AS-IS and does NOT log,
//     cache or replay it; the knowledge MODULE owns secret detection + redaction
//     before the content is ever persisted or indexed (docs/SECURITY-HARDENING.md). Connectors
//     therefore do not pre-scrub the body — under-redacting upstream would hide
//     from the module's authoritative redactor what to count and report.
//
// # Boundary with (not overlapping)
//
// This is ingest of CONTENT for knowledge — distinct from the R/RW audit of data
// stores and the runtime/cloud inventory. Source.Kind declares the
// content class so the knowledge module cannot drift into ingesting database
// audit logs (territory) as if they were documents.
package contentsource

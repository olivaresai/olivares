// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cloudflare is a read-only SourceConnector that discovers the
// Cloudflare serverless/object surface and its Logpush audit-feed configuration
// through the Cloudflare REST API v4. It is the cloud-side amplitude capture for
// the Olivares AI control plane: it inventories Workers scripts, Worker routes,
// R2 buckets and Logpush jobs, then emits them as containment/topology edges so
// the consumer module can materialize the entities from their natural
// refs.
//
// It is strictly read-only: it issues only GET list/describe calls and never any
// create/update/delete operation. It upholds the minimal-data invariant (docs/SECURITY-HARDENING.md
// §3) — it persists only identifiers and classification, never secret values,
// tokens, ownership challenges, logpull options or request/response payloads. A
// Logpush destination can embed credentials in its URL, so every destination_conf
// is passed through redact.SanitizeURL before it becomes a reference.
//
// Every emitted edge carries Mode=ModeUnknown (a containment/topology edge is a
// surface fact, not an R/RW access), Confidence=ConfidenceAttributed (it was
// observed directly via the API) and Source=signalCloudflare ("cloudflare"). A
// target that is configured but cannot be listed yields one health FindingReport
// and the pass continues; a target that is simply absent is skipped silently.
//
// The connector needs only a scoped, read-only API token: Account
// "Workers Scripts:Read", "Workers R2 Storage:Read" and "Logpush:Read", plus —
// when a zone is configured — Zone "Workers Routes:Read" and "Logpush:Read". No
// write or edit scope is ever required.
package cloudflare

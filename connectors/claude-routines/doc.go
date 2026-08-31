// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package clauderoutines is a READ-ONLY inventory connector for Claude Code
// Routines (scheduled triggers / cron agents). It polls the Claude Code Remote
// API (/api/triggers) to discover active routines and emits:
//
//   - EdgeObservation for each routine (subject=workspace/environment,
//     resource=trigger) — an inventory carrier so the access map knows which
//     scheduled automations exist in the organization.
//   - FindingReport for governance signals: routines with excessive cadence
//     (below the operator's policy floor), routines older than the review
//     window without a recorded review, and routines with no human-readable
//     name (anonymous automations are a shadow-IT surface).
//
// Read-only by construction: every API call is a GET. The connector never
// creates, updates, enables, disables, fires or deletes a trigger. Prompt
// content is NEVER stored or logged — only hashed for posture fingerprinting
// (redact.Hash). Minimal data (docs/SECURITY-HARDENING.md): it carries trigger REFERENCES
// (trig_ ids), resource types and state — never credentials, never prompt
// content.
//
// Boundary (LICENSING.md): Apache-2.0; imports only /sdk and
// connectors/internal (httpx read-only GET client, redact scrubber), NEVER
// /core.
package clauderoutines

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package accessmap is module III — the R/RW access map (ARCHITECTURE.md, README.md
// §2), the product's differential, feasibility-verified moat. It answers, with
// honest confidence levels, "which agent reads (R) or writes (RW) which
// resource, and is that access PERMITTED or merely OBSERVED?".
//
// The access graph is a VIEW over the core data model, not a separate schema:
// it is the set of core/model.AccessEdge rows, and least-privilege drift is the
// query over them where Permitted and Observed disagree (ARCHITECTURE.md, §6). This
// package does not redefine that entity; it builds it correctly.
//
// # PoC #1 (the gate)
//
// Before the full module is built, the make-or-break question (ARCHITECTURE.md, §10)
// is whether the edge "agent → table, R/RW, attributed to the agent" can be
// constructed at all from real signals. The hard dependency is per-agent
// identity: a store's native audit attributes an access to a credential/role,
// never to an agent (docs/contracts), and a shared service account behind
// a connection pool collapses that attribution. This package's bridge.go
// resolves the gap when a per-agent identity exists (the deployment propagates
// the agent/session identity into the connection's application_name) and reports
// the access as approximate — never a fabricated agent — when it does not. The
// gate is exercised end-to-end in poc_test.go against the REAL pg-audit
// connector and the real dual-engine store; the cooperative (OTEL)
// leg is consumed by its verified contract rather than driven live.
//
// # Guardrails (docs/SECURITY-HARDENING.md), born with the PoC
//
//   - Read-only at the source: the capture is the existing connectors (tails a log file receives pushed telemetry). This module only
//     consumes the EdgeObservation wire contract; it never reaches a data store.
//   - Minimal data (docs/SECURITY-HARDENING.md): an edge records the relationship and the
//     connector's already-redacted natural references only — never a SQL
//     statement, payload, secret or PII. There is no field through which one
//     could enter.
//   - Tamper-evident evidence (docs/SECURITY-HARDENING.md): viewing the access graph is a
//     privileged, recon-relevant action; AuditedNeighbors records every read in
//     the append-only, hash-chained ledger before returning, so graph access is
//     attributable and any alteration is detectable on Verify.
//   - mTLS collector↔core (ARCHITECTURE.md): a transport property of the runtime, not
//     of this module, not a property this module documents.
//
// After the PoC gate passed and was approved, the full module
// was built on top of it: this module is now the SOLE writer of the AccessEdge
// graph (decision A — inventory no longer writes edges). It adds multi-signal
// fusion with confidence reconciliation that never
// downgrades a stronger signal (fusion.go), declared coverage tiers, the
// untrusted-annotation rule (an MCP capability never enters the access graph as
// an access), the PERMITTED-vs-OBSERVED diff (query.go, over the core Drift
// primitive) and the privileged, self-audited API whose React Flow data contract
// is the module's own React Flow data contract.
//
// # Firm per-agent attribution (attribution.go)
//
// The moat's deepest, most fragile piece is per-agent identity (G8):
// with shared service accounts attribution degrades, and on stores with no
// per-identity audit (Redis/SQLite/D1) it is impossible passively. attribution.go
// classifies every edge with an honest, per-edge ATTRIBUTION TIER —
// firm/approximate/unknown — that is STRICTER than Confidence and consumes the
// per-agent identity signals expose: a workload SPIFFE SVID or an
// Anthropic WIF service account, a governance-minted dedicated NHI, or a credential
// bound to exactly one agent make it `firm`; a shared/pooled credential is
// `approximate`; a store with no per-identity audit (the opaque tier) seen by a
// non-cooperative backstop is `unknown`. It never fabricates firmness the signal
// does not support (deny-closed). The tier is orthogonal to coverage_tier (origin
// firmness vs resource fidelity), rides every graph and diff edge so the
// PERMITTED-vs-OBSERVED diff no longer degrades silently, and never downgrades on
// fusion. Enforcement on top of firm attribution is.
//
// Layout: module.go (lifecycle, bus, route/permission registration) · reactor.go
// (ingest one observation → canonical edge) · bridge.go (the identity bridge) ·
// fusion.go (multi-signal reconciliation + coverage tiers) · attribution.go (the
// per-agent attribution tier) · query.go (the privileged audited reads + the diff)
// · api.go + dto.go (HTTP + the UI contract).
package accessmap

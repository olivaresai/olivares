// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package security is module IX — security, guardrails & forensics: the
// DEFENSIVE control plane of Olivares AI. It turns the estate's events and the
// tamper-evident evidence ledger into findings, prioritized anomalies and
// reconstructible incident timelines, so a defender can SEE and PROVE what every
// agent did — it never becomes an attacker's tool (docs/SECURITY-HARDENING.md).
//
// # Bounded context
//
//   - Guardrails (guardrail.go + the detectors): inspect agent input/output/tool
//     text for PII/secrets, prompt-injection, jailbreak, disallowed content,
//     output-schema violations and the OWASP Agentic Top 10. Every trip produces a
//     Finding with minimal evidence (a hash + a redacted excerpt, NEVER the raw
//     payload — docs/SECURITY-HARDENING.md).
//   - Threat / anomaly detection (anomaly.go): consume the estate event stream
//     (edge observations), the permitted-vs-observed drift already computes,
//     and the anti-evasion gap mark raises at the kernel, and surface
//     prioritized anomalies.
//   - Incident response / forensics (forensic.go): group findings and evidence
//     into a CASE and reconstruct its TIMELINE from the append-only, hash-chained
//     ledger — VERIFYING the chain (and its signed checkpoints) so an auditor is
//     handed evidence that is provably unaltered (docs/SECURITY-HARDENING.md, §5). Export the case
//     to WORM/SIEM (CEF/syslog/OTLP) for an external immutable copy.
//
// # The RED LINE (docs/SECURITY-HARDENING.md, non-negotiable)
//
//   - READ-FIRST / DETECTIVE BY DEFAULT (§0): the module OBSERVES and gives
//     evidence; it does not sit in the agent's data-path. Inline enforcement
//     (blocking an output/action) is an explicitly OPT-IN, governed capability
//     (security:enforcement:admin, gated by HITL where wired) and is OFF by
//     default — a guardrail that fails must never break production.
//   - MINIMAL DATA (§3): findings, cases and audit Meta carry references, hashes
//     and redacted excerpts only — never raw payloads, secrets or PII. The module
//     redacts BEFORE it hashes, stores or returns anything.
//   - INTEGRITY IS EVERYTHING (§1, §5): the forensic timeline is only as good as
//     the ledger, so the module VERIFIES the hash chain and the Ed25519 checkpoint
//     signatures rather than trusting them. A tampered ledger is reported, not
//     hidden.
//   - ANTI-EVASION (§6): an agent that silences its own telemetry is a SIGNAL, not
//     a blind spot. The module joins the kernel-side and cooperative-side
//     anti_evasion marks into a correlated, prioritized anomaly.
//
// The module owns no ledger and no capture: it CONSUMES the core ledger, the
// connector observations (incl. the eBPF backstop) and the signals of
// (drift) (identity/HITL) and (lineage), and it EMITS findings/cases
// other sessions deliver or map to controls. It re-implements none
// of them.
package security

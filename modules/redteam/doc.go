// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package redteam is module XVIII — red-teaming & adversarial testing: a DEFENSIVE
// robustness harness that probes the client's OWN agents with a battery of
// adversarial test cases (prompt injection, jailbreak, exfiltration, tool
// poisoning) and scores their resistance, mapped to the OWASP Top 10 for Agentic
// Applications and MITRE ATLAS.
//
// # What it is
//
//   - A consent-gated catalog of adversarial PROBES (battery.go + the attacks_*.go
//     catalogs): each probe is a known, published robustness test mapped to an
//     OWASP/ATLAS reference, with an expectation that a well-defended agent REFUSES
//     or its guardrail BLOCKS it. A compliance/leak is a finding.
//   - A SCORECARD (scorecard.go): per-family and overall robustness, OWASP Agentic
//     coverage, regression over time — each run an append-only, tamper-evident
//     record, each failure a Finding.
//   - The sandbox is the EXECUTION ENVIRONMENT (ports.go Sandbox seam); this
//     module owns the battery and the scoring, not the sandbox. Without a wired
//     sandbox a run is reported as DEGRADED (skipped), never silently "passed".
//
// # The RED LINE (docs/SECURITY-HARDENING.md, non-negotiable — the dual-use boundary)
//
// This is NOT a C2 and NOT an offensive armory. It runs ONLY against an agent the
// client GOVERNS, that has been explicitly REGISTERED and AUTHORIZED as a target
// (consent.go), inside the client's own perimeter (via the sandbox). It never
// targets third-party systems, never scans others' credentials, and ships no
// purely-offensive capability. Launching a run is an admin-tier, AUDITED, privileged
// action; the payloads are a defensive robustness battery — a test suite, not a
// weapon. This boundary is enforced in code (the authorization gate) and stated in
// docs, and it is not negotiable.
//
// The module emits findings/scorecards other sessions consume: (sandbox env) (compliance evidence) (delivery) (the UI). It re-implements none
// of them.
package redteam

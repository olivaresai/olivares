<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Third-party penetration testing — program, cadence, remediation

> **Status: no third-party penetration test has been performed.** The project is
> pre-release; saying otherwise would be fabrication. This document is the
> *committed program* — what will run, when, at what scope, and what customers
> receive — plus the remediation workflow findings flow into. Contracting the first
> test is an explicit, budgeted founder decision (see end).

## Commitment & cadence

| Trigger | Commitment |
|---|---|
| **Before first commercial GA** | One scoped third-party penetration test of the control plane (budget permitting — internal planning estimate US$5–15k for a scoped engagement; if not affordable pre-launch, contracted from first revenue and disclosed as such). |
| **Annual** | Full-scope re-test every 12 months once commercially available. |
| **Event-driven re-test** | After material architectural change: new authentication surface, multi-tenancy/RLS changes, new network-exposed component, multi-region/HA topology changes, cryptographic custody changes. |
| **Continuous (already running)** | Blocking `govulncheck` (reachability call-graph) + secret scanning on every change; weekly rebuild from patched base + grype/trivy re-scan + SBOM/VEX refresh; in-product adversarial red-team module exercised against the platform's own guardrail surfaces. These complement — never substitute for — the third-party test. |

## Scope (what the first engagement covers)

1. **External/API surface:** the control-plane REST/gRPC API, authn (setup token,
   opaque bearer tokens, step-up), authz (RBAC, tenant isolation — Postgres RLS
   bypass attempts), rate limiting.
2. **Web console:** the embedded SPA (session handling, XSS/CSRF, IDOR against
   tenant-scoped routes).
3. **Collector path:** mTLS enforcement, plugin boundary (`go-plugin` gRPC),
   attempts to spoof/poison ingest.
4. **Tamper-evidence:** attempts to forge/reorder the audit ledger, checkpoint and
   per-event signature verification, WORM archival manifest chain.
5. **Agentic surfaces:** abuse of governance flows (approval flooding, break-glass
   misuse, kill-switch bypass), aligned to OWASP Agentic Top 10 (ASI01–ASI10) and
   the in-catalog threat model (T1–T15).
6. **License/entitlement:** offline Ed25519 license forgery attempts.

Methodology: gray-box (source available — the product is source-public), aligned to
OWASP WSTG/ASVS for the web/API surface. Out of scope: customer deployments,
social engineering, physical, volumetric DoS (mirrors `SECURITY.md` scope).

## What customers receive

- **Summary attestation letter** from the testing firm (scope, dates, methodology,
  finding counts by severity, remediation status) — shareable under NDA.
- Full technical reports remain internal; specific findings relevant to a
  customer's deployment are disclosed per the coordinated-disclosure policy.
- Re-test confirmation for Critical/High findings.

## Remediation workflow (findings → fixed, on the clock)

Pen-test findings enter **the same published remediation SLA as CVEs**
(`SECURITY.md §Vulnerability remediation SLA`):

| Severity (CVSS v3.1) | Target to a patched, signed release |
|---|---|
| Critical (9.0–10.0) | 7 days |
| High (7.0–8.9) | 14 days |
| Medium (4.0–6.9) | 30 days |
| Low (< 4.0) | next scheduled release |

Per-finding record (template):

```
finding:      <id> — <title>
source:       <firm> engagement <date>, report ref <ref>
severity:     CVSS <score> (<vector>)
component:    <module/path>
status:       open → triaged → fix-in-review → released (<version>) → re-tested
clock:        starts at report delivery; target per table above
disclosure:   GHSA/OSV advisory if the finding affects released artifacts
verification: <test/commit/re-test evidence>
```

## Founder decision required

1. **Contract the first scoped test** (~US$5–15k): firm selection, timing relative
   to first GA. Recommendation on record: scoped test **before** launch if budget
   allows, otherwise first-revenue-funded.
2. Whether to additionally run a **public VDP-only period** first (cheaper,
   already supported by `SECURITY.md` safe harbor) before the paid engagement.

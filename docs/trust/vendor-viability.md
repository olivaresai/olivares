<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Single-vendor risk — the viability objection, answered

> The first objection a procurement team raises against Olivares AI is not
> technical: *"this is a single-maintainer vendor competing with venture-funded
> platforms — what happens to us if it disappears?"* It deserves a structural
> answer, not reassurance. The honest headline: **bus factor is 1 today**, and the
> product is deliberately built so that fact costs you as little as possible.

## 1. The AGPL survival guarantee (structural, not contractual)

The core product — core, all 30 modules, web console — is **AGPL-3.0-only with
nothing feature-capped from within to upsell** (`LICENSING.md`): the full
governance loop runs in the public binary with no license check. The commercial
offering adds a *small, additive `enterprise/` line* (built only with
`-tags enterprise`, never in the public binary) and is both an AGPL *license
exception* and the entitlement to that line — not a paywall over the open
features. Consequences if Olivares.AI vanished tomorrow:

- You already possess the complete, buildable source of everything you run.
- Your right to run, patch, fork and even commission third-party maintenance is a
  **license fact**, not a promise that dies with the vendor.
- License ENFORCEMENT is **offline** (Ed25519): validating an installed licence makes
  no mandatory outbound call, and nothing bricks when the vendor's infrastructure goes
  away. Issuance, renewal and CRL distribution do reach the vendor — that is the
  licensing model, not a dependency of the running system.

This is materially stronger than source escrow at a closed-source vendor: there is
no escrow trigger to litigate — you have the source on day one.

## 2. Escrow, scoped to where it actually applies

Escrow only matters for the commercial `enterprise/` modules (additive features).
Position: **offered on request for enterprise contracts** — agent selection and
cost are a pending founder decision (see end). For everything AGPL, escrow is
redundant by construction (§1).

## 3. You can verify and rebuild what we ship

- Releases are signed (cosign), with SBOM, **SLSA Build L3 (SLSA v1.2) provenance**, OpenVEX, and
  checksums — verified end-to-end by `scripts/verify-release.sh`, including an
  **air-gapped** mode.
- Builds are **reproducible** (`task build:repro` — two builds, identical SHA-256),
  so a third party can confirm the binary matches the source you'd be maintaining.
- Distroless, single static Go binary on a conventional stack (Go, Postgres/SQLite,
  React): the maintenance market for these skills is as deep as it gets.

## 4. No operational dependency on the vendor

Self-hosted: you run the control plane and its stores. There is no
vendor-side service in the production path, no mandatory telemetry, no control-plane
egress by default and no remote kill; what crosses your perimeter is what you
configure to cross it — calls to your model APIs, the SIEM/webhook outputs you wire,
an external embedding provider if you provision one.
Data is in standard stores (SQLite/Postgres) with documented schema and
migrations, and exports in open formats (JSON, CSV, OSCAL, CycloneDX, SPDX, CEF/
syslog/OTLP/OCSF) — exit was designed in, not bolted on.

## 5. Continuity engineering (the bus-factor mitigations that exist today)

- **The repository is the institutional memory:** architecture and decision docs
  (`docs/`, ADRs), contribution and
  governance process (`CONTRIBUTING.md`, `GOVERNANCE.md`) are maintained as
  first-class artifacts — a competent team could take over from the repo alone,
  and that is a stated design goal of how the project is run.
- **Quality gates are automated** (lint/SPDX/license-boundary/build/test, vuln +
  secret scanning, reproducible release pipeline): continuity does not depend on
  the founder's memory of how to ship safely.
- **Public process:** CVE SLA, advisories (GHSA→OSV), coordinated disclosure — all
  published and survivable.

## 6. What we will not pretend

- Bus factor **is 1**. There is no 24×7 follow-the-sun team behind the SLA, and
  the support model says so honestly (`SUPPORT.md`).
- A funded competitor can outspend us on integrations and marketing. The bet we
  ask you to underwrite is narrower than "the company thrives": it is "the
  software keeps working and remains maintainable" — which §1–§4 make true even in
  the worst case.

> **Founder decisions required:** (a) escrow agent + terms for enterprise modules;
> (b) a written discontinuation pledge (e.g. final license-exception grant /
> relicense of `enterprise/` on wind-down) — recommended addition to enterprise
> contracts; (c) long-term: second maintainer / maintenance-partner agreement.

<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# EU AI Act Annex IV — technical-documentation template & evidence packaging

> **Companion (published).** The buyer-facing narrative — how runtime data becomes
> Annex IV evidence and why a self-hosted/AGPL plane fits EU regulated buyers who
> cannot use a US SaaS control plane — is published at `https://olivares.ai/docs`.
> This file is the operational template that guide points back to.

> **Who this is for.** Under Regulation (EU) 2024/1689 Art. 11, the **provider of a
> high-risk AI system** must draw up Annex IV technical documentation before
> placing it on the market and keep it current. In most deployments the buyer (or
> their AI supplier) is that provider — **Olivares AI is governance tooling over AI
> systems, not itself an Annex III system in typical use** (classification is a
> legal determination the provider makes, not us). This template shows which Annex
> IV sections the control plane can populate **from live evidence**, so the
> document is generated, not hand-curated.
>
> **Dates note:** application timelines for high-risk obligations are in flux
> (Digital Omnibus provisional agreement, 2026-05-07). Do not copy dates from
> static documents — the product serves them as data with source + verified-on:
> `GET /v1/m/compliance/calendar`.
>
> **Template honesty:** section themes below are working paraphrases; when filing,
> verify against the official Annex IV text (EUR-LEX, Regulation (EU) 2024/1689).

## Annex IV section → evidence source

| Annex IV | Theme | What the control plane provides | Source |
|---|---|---|---|
| 1 | General description (intended purpose, provider, versions, forms of delivery, UI for the deployer, instructions) | Model inventory identity + versions; **model card** (JSON or Markdown, explicit `not_recorded` for unknown fields — never invented) | `GET /v1/m/models/owned-models/{id}/model-card` (`?format=md`) |
| 2 | Elements & development process — incl. system architecture; **computational resources used** (2(c)); data requirements/provenance (datasheets); human-oversight assessment; validation & testing | Architecture: this package's [reference-architecture.md](./reference-architecture.md). Compute/cost accounting per inference (operational side of 2(c) — the catalog note is explicit that training-time figures are NOT evidenced). Dataset registry + sealed **AIBOM** lineage (CycloneDX 1.6) + **SPDX 3.0.1 AI Profile**. Oversight: approvals/HITL configuration evidence. V&V: eval results + red-team findings | FinOps cost samples; `GET /v1/m/models/owned-models/{id}/aibom` (`?format=spdx`); datasets API; evals module |
| 3 | Monitoring, functioning & control (capabilities/limitations, oversight measures, input specs) | Live operation evidence: guardrail/anomaly findings, access map + drift, session timelines, kill-switch state | Findings; `GET /v1/m/accessmap/drift` |
| 4 | Appropriateness of performance metrics | Eval methodology + results (LLM-judge calibration, regression gates) | Evals module |
| 5 | Risk-management system (Art. 9) | Per-agent risk classification (EU tier × NIST function), governed review (dual-control), risk register export | `GET /v1/m/compliance/risk`; `GET /v1/m/compliance/dora` (ICT-risk-shaped register) |
| 6 | Lifecycle changes | Change/deploy ledger; model admission history; version lifecycle | Deploy records; `GET /v1/m/models/model-admissions` |
| 7 | Harmonised/other standards applied | Framework catalog with version pins (which standards, which versions, verified-on) | `GET /v1/m/compliance/frameworks` |
| 8 | EU declaration of conformity (Art. 47) | **Not generated** — a legal act by the provider. The package only stores/links it | (provider-supplied) |
| 9 | Post-market monitoring plan (Art. 72) | Continuous monitoring evidence the plan can cite: findings, SLOs, incident comms, audit ledger + SIEM export | `docs/17-PRODUCTION-READINESS-SLO.md`; `docs/STATUS-AND-INCIDENT-COMMS.md` |

## Packaging workflow

1. Per AI system in scope, pull: model card (`?format=md`), AIBOM (+`?format=spdx`),
   risk classification, eval summaries, drift snapshot, calendar extract.
2. Seal the bundle as a compliance **evidence package** — append-only,
   ledger-anchored, export as JSON/CSV/OSCAL:
   `POST /v1/m/compliance/frameworks/eu_ai_act/evidence` →
   `GET /v1/m/compliance/evidence/{id}/export?format=oscal`.
3. Attach provider-authored sections (2(b) design choices, 5 narrative, 8
   declaration) — the platform does not fabricate what only the provider knows.

## Honest gaps

- Training-time compute, dataset statistical quality/bias and design rationale are
  **not** evidenced by the control plane (catalog notes say so explicitly).
- Art. 50 transparency duties (interaction notices, content marking) remain an
  honest gap of the platform itself, recorded as such in the catalog.

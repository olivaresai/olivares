---
title: EU AI Act evidence from runtime data
description: >-
  How a self-hosted control plane turns the live behaviour of your AI estate into
  the technical evidence an EU AI Act file needs — Annex IV-shaped, generated from
  runtime data, and stored by the control plane you run yourself. For regulated EU
  buyers who cannot put a US SaaS control plane in their compliance path.
---

Most AI-governance tooling produces evidence the way a slide deck produces facts:
someone writes it down, and you trust that it was true. Under
**Regulation (EU) 2024/1689 (the EU AI Act)** that is not enough. The provider of
a high-risk system has to draw up **Annex IV technical documentation** *before*
placing the system on the market and **keep it current** through the lifecycle
(Art. 11), and the post-market monitoring plan (Art. 72) has to be fed by what the
system actually does in production.

This page explains how Olivares AI lets you **generate** that evidence from the
**runtime behaviour of your estate** instead of curating it by hand — and why a
**self-hosted, AGPL control plane** is the shape that survives an EU regulated
buyer's review when a US SaaS control plane does not.

:::note[Who is the "provider" here]
Olivares AI is **governance tooling over AI systems, not itself an Annex III
high-risk system** in typical use. Whether *your* AI system is high-risk, and who
is its provider or deployer, is a legal determination you make — not us. What we
do is make the **technical-documentation and monitoring obligations cheap to
satisfy with real evidence**. See [Honesty & limits](/start/honesty-and-limits/)
for what the platform does and does not assert.
:::

## Why "from runtime data" is the whole point

The EU AI Act's documentation duties are not one-off. Annex IV asks for the
system's architecture, its **computational resources**, its monitoring and control
characteristics, its performance metrics, its risk-management system, and a
record of **lifecycle changes** — and Art. 72 requires a post-market monitoring
plan that you actually run. A static Word document drifts out of date the moment a
model is swapped or an agent gains a new tool.

Olivares AI already observes the estate to build its
[read/write access map](/explanation/#the-access-map-read-first-minimal-data-permitted-vs-observed)
and its append-only, hash-chained, Ed25519-signed
[audit ledger](/reference/glossary/#audit-ledger). The compliance module turns
those same observations into **auditor-consumable evidence packages**: sealed,
ledger-anchored, exportable as JSON, CSV or **OSCAL**, with a live integrity
proof. The document is *derived from what happened*, not asserted about what was
intended.

Two honesty rules are wired into the product and carry straight into the evidence:

- A control whose only backing is architectural reports **`by_design`**, never
  `satisfied`. "Satisfied" requires linked, real tenant evidence.
- The framework catalog is **version-pinned to its primary source** with a
  `verified_on` date, and every framework carries a "this is a technical mapping,
  not a certification" disclaimer.

## The Annex IV crosswalk, in brief

The compliance module maps the EU AI Act articles it can evidence —
**Art. 5, 6, 9, 10, 11, 12, 13, 14, 15, 50 and 72** — to capabilities the control
plane already produces. Below is the Annex IV section view; the full row-by-row
template (with exact endpoints and the explicit gaps) ships in the trust &
procurement package as `eu-ai-act-annex-iv.md`.

| Annex IV theme | What the control plane provides | Pulled from |
|---|---|---|
| **1.** General description (purpose, provider, versions, delivery) | Model inventory + versions; **model card** (JSON/Markdown; unknown fields are explicit `not_recorded`, never invented) | `GET /v1/m/models/owned-models/{id}/model-card` |
| **2.** Development process, architecture, **computational resources**, data provenance, oversight, V&V | Reference architecture; per-inference **compute/cost accounting** (the *operational* side of 2(c) — training-time figures are **not** evidenced, and the catalog says so); dataset registry + sealed **AIBOM** (CycloneDX 1.6) and **SPDX 3.0.1 AI Profile**; approvals/HITL config; eval + red-team results | FinOps cost samples; `GET /v1/m/models/owned-models/{id}/aibom?format=spdx`; evals module |
| **3.** Monitoring, functioning & control | Live operation evidence: guardrail/anomaly findings, access map + **Permitted-vs-Observed drift**, session timelines, kill-switch state | findings; `GET /v1/m/accessmap/drift` |
| **4.** Performance metrics | Eval methodology + results (LLM-judge calibration, blocking regression gates) | evals module |
| **5.** Risk-management system (Art. 9) | Per-agent risk classification (EU tier × NIST function), dual-control governed review, risk-register export | `GET /v1/m/compliance/risk`; `GET /v1/m/compliance/dora` |
| **6.** Lifecycle changes | Change/deploy ledger; model-admission history; version lifecycle | deploy records; `GET /v1/m/models/model-admissions` |
| **7.** Standards applied | The **26-framework catalog**, version-pinned, with `verified_on` | `GET /v1/m/compliance/frameworks` |
| **8.** EU declaration of conformity (Art. 47) | **Not generated** — a legal act by the provider; the platform only stores/links it | provider-supplied |
| **9.** Post-market monitoring plan (Art. 72) | Continuous evidence the plan can cite: findings, SLOs, incident comms, ledger + SIEM export | production-readiness + status/incident docs |

### The honest gaps, stated up front

Putting these in the file *strengthens* it — an assessor trusts a document that
names its own boundaries.

- **Training-time compute, dataset statistical quality/bias, and design
  rationale** are not evidenced by the control plane. Those are provider-authored.
- **Art. 50 transparency duties** (interaction notices, AI-content marking) are an
  honest gap of the platform itself, recorded as such in the catalog.
- The control plane evidences the **operational** half of Annex IV — what your
  estate does, attributable and tamper-evident. It does **not** write the
  provider's design narrative or sign the declaration of conformity.

### Don't hard-code the dates — serve them

High-risk application timelines are **in flux** (the Digital Omnibus provisional
agreement of 2026-05-07 moved several). Copying dates into a static file is how
compliance documents go stale and wrong. The control plane serves the regulatory
calendar **as data** — each entry with its source and `verified_on`:

```http
GET /v1/m/compliance/calendar
```

Your GRC pipeline reads the live calendar; your evidence package references it.
Nobody re-types a date.

## Packaging workflow

1. Per AI system in scope, pull: model card (`?format=md`), AIBOM
   (`?format=spdx`), risk classification, eval summaries, the drift snapshot, and
   the calendar extract.
2. **Seal** the bundle as a compliance evidence package — append-only,
   ledger-anchored:
   `POST /v1/m/compliance/frameworks/eu_ai_act/evidence` →
   `GET /v1/m/compliance/evidence/{id}/export?format=oscal`.
3. Attach the provider-authored sections (design choices, the Art. 9 narrative,
   the Art. 47 declaration). The platform does not fabricate what only the
   provider knows.

The result is an Annex IV file whose operational sections are **reproducible from
the ledger** and **re-verifiable off-box** — a property a hand-curated document
cannot offer.

## Why sovereignty is the deciding factor for EU regulated buyers

For a bank, hospital, ministry or university under EU supervision, *where the
evidence lives* is not a detail — it is often the gate.

- **The data plane never leaves your boundary.** The collectors run on **your**
  infrastructure; the access map stores only the *relation* (agent → resource,
  read or write) with a source and a confidence level — **no payloads, no secrets,
  no PII**. The compliance evidence is built from data that never had to transit a
  vendor's cloud.
- **The control plane can be fully self-hosted, or air-gapped** with zero egress
  and an offline licence. There is no vendor in your compliance path to add as a
  sub-processor, to assess under a transfer mechanism, or to depend on for
  retention of *your* regulatory evidence.
- **AGPL-3.0, source-available.** Your security team can read every line that
  produces the evidence. The integrity proof is verifiable **off-box** with
  `audit verify`, so you are not trusting our assertion that the ledger is intact —
  you are checking it. Single-vendor dependency is mitigated structurally, not
  promised (see the trust package's vendor-viability note).
- **Residency is attested, not assumed.** `GET /v1/m/compliance/residency`
  produces a residency attestation; multi-region deployments are region-scoped and
  deny-closed by design.

A **US SaaS control plane** inverts all of this: your AI estate's behavioural
evidence — the very record an EU regulator may ask for — is generated, processed
and retained in a third party's cloud, under a shared-responsibility model you do
not control, frequently outside the EU. That is precisely the arrangement many
regulated EU buyers are told they cannot enter. **Self-hosted is not a deployment
preference here; it is the compliance posture.**

:::caution[We design for audit; we do not certify]
None of the above makes you, or us, "EU AI Act compliant" — compliance is a legal
conclusion about a specific system, drawn by its provider with counsel. What the
control plane gives you is **evidence you can stand behind**, generated from real
runtime data, kept where your regulator expects it. The
[framework catalog](/reference/modules/xiii-compliance/) carries the
"not a certification" disclaimer on every entry, by design.
:::

## Related

- [Machine-readable evidence](/reference/modules/xiii-compliance/) — the evidence
  API surface, KSI-style continuous validation.
- [Security model](/explanation/security/security-model/) — why the ledger is
  tamper-evident and how off-box verification works.
- [Market context & sources](/explanation/positioning/market-context-and-sources/)
  — the verified statistics behind the governance-debt argument.

---
title: Module XIII — compliance & regulatory
description: 'Map what the control plane already observes and audits onto
  regulatory frameworks, and export auditor-consumable evidence derived from the
  append-only ledger. Designed-for-audit, never certified: status + evidence,
  never "compliant".'
slug: 2026-06/reference/modules/xiii-compliance
---

Module XIII opens enterprise doors by **mapping** what the control plane already
observes and audits onto regulatory frameworks, and by producing **auditor-consumable
evidence** derived from the append-only, hash-chained ledger. It is an
intelligence-layer module: it captures **nothing new** — it aggregates and transforms
what the core and the other modules already record, and it **never claims
certification**.

## What it is

Module XIII has five surfaces, all read-and-derive over existing data:

* **A versioned control catalog** held in the repo as the deterministic source of
  truth — EU AI Act, NIST AI RMF, ISO/IEC 42001, SOC 2 / ISO 27001 and GDPR (plus
  GenAI/agentic cross-walks), modeled as versioned **controls**, each with its
  requirement and its satisfaction criterion. It is a **technical mapping, not legal
  advice**, and a control whose obligation the platform cannot evidence carries an
  explicit note so partial coverage never reads as total.
* **A declarative control → evidence map.** Every control maps to control-plane
  **capabilities**. A capability is either **operational** — present only when real
  tenant data exists (a ledger that verifies, observed access edges, security findings,
  eval results, deployments, a risk classification, a residency attestation) — or
  **architectural** — a platform-design guarantee cited to the design docs and labelled
  as such, never as telemetry.
* **Exportable audit evidence** — a sealed, append-only evidence package derived from
  the ledger.
* **Agent risk classification** into an EU AI Act tier cross-mapped to NIST AI RMF
  functions, from observed attributes — governed and audited.
* **Data residency** — a per-region attestation that data stays inside the customer's
  perimeter, plus a scan that turns existing egress signals into a residency finding.

## Control status & entities

Status is computed honestly, never asserted. A control is **satisfied** only when every
mapped capability is present **and at least one is operational**; **by\_design** when all
present capabilities are architectural (design-ready, never satisfied); **partial** when
some are present; **gap** when none are; **unmapped** when no capability backs it at all.
`satisfied` never rests on design evidence alone.

The module declares four append-only / audited entities in the shared data model: a
sealed **evidence package** (recording the chain head sequence and hash and the live
hash-chain verify result), a per-control **result** inside that package, a **risk
classification** per subject, and a per-region **residency attestation**. The evidence
package **references** the ledger by sequence and hash and proves its body
tamper-evident with a deterministic manifest hash — it never copies the ledger and
never holds payloads or PII.

## What it consumes & produces

Risk classification reads attributes already recorded by other modules — outbound
read/write [access edges](/2026-06/reference/modules/iii-access-map/), high/critical security
findings, and an optional autonomy signal — and produces a **suggested** tier that is
governed: a human must review and approve it, and the suggestion engine **can never
assign the unacceptable tier** (that is a legal determination). The residency scan
correlates existing egress lineage against `self_hosted` attestations and, per
violation, raises a core finding and publishes an internal bus signal for the
notifications module (XV) to deliver to SIEM/Slack/PagerDuty. Reading or exporting an
evidence package, sealing one, classifying or reviewing risk, and attesting residency
are privileged, tenant-scoped actions that **self-audit to the ledger** in the caller's
own transaction.

:::caution[Honest limits]
* **Designed-for-audit, never certified.** Every reporting response carries the
  disclaimer that it is **not a certification and not legal advice**. The output speaks
  of control status and evidence; it never says "compliant" or "certified". Opt-in
  guarantees (such as at-rest encryption) default to **absent** until attested.
* **No actuation.** This module maps controls and exports evidence — it does not
  remediate, enforce or change anything. Its only side effect is the residency finding
  and bus signal, which other modules act on.
* **Evidence is only as good as its sources.** A control with no backing tenant data is
  an honest **gap**, not a faked pass; an absent operational capability lowers a
  framework's status rather than inflating it. The least-privilege-drift evidence
  consumes module III's **reconciled** drift (not the raw store path), so it inherits
  module III's tiered coverage limits — an absent edge is not proof an access did not
  happen.
* **Architectural evidence is design, not proof.** Capabilities cited to the design
  docs attest how the platform is built, not that a control ran in your tenant; they
  produce `by_design`, which is deliberately distinct from `satisfied`.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module XIII sits and the
  honest govern/observe-vs-actuate split.
* [Module III — access & resource map](/2026-06/reference/modules/iii-access-map/) — the drift
  signal the risk classifier and the drift capability consume.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — why status, not certification.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — reviewing a suggested risk tier.
* [Forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/) — the continuous ledger
  feed the auditor re-verifies against.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the intelligence layer
  and the event bus.

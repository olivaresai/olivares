<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Status page & incident communications

**Date:** 2026-06-09 · **Status:** process + self-hostable artifact

> The buyer's question — *"status page? incident comms?"* — has two parts: a **status/uptime artifact** they can see, and a **process** for what we say and when during an incident. Both are here. The status page is driven by the **real SLIs** ([17-PRODUCTION-READINESS-SLO.md](17-PRODUCTION-READINESS-SLO.md)), not a hand-edited page, so it cannot quietly lie.

---

## 1. The status / uptime artifact

**Artifact:** `deploy/monitoring/status-page.gatus.yaml` — a self-hostable status page (Gatus, single static binary) that **blackbox-probes `/readyz` and `/livez`** from outside the engine and computes real uptime (1h/24h/7d/30d) and response-time history.

**Why external probing is the honest design:** a down engine cannot report its own downtime, and a sub-scrape-interval outage is invisible to a self-scrape ([17-PRODUCTION-READINESS-SLO.md §1.1](17-PRODUCTION-READINESS-SLO.md)). The status page is therefore the **authoritative availability source** for the SLO; the in-engine `/metrics` series corroborate request-success and latency only while the engine is answering.

**Availability mapping.** Uptime % on the page is the number the availability SLO is measured against: **99.5%/30d ≈ 3h 39m** of allowed downtime (single-node tier today), **99.9% ≈ 43m** (HA tier). With one replica there is **no partial/degraded state** — readiness red == full control-plane outage ([17-PRODUCTION-READINESS-SLO.md §2.1](17-PRODUCTION-READINESS-SLO.md)); with HA this becomes a per-replica component view.

**Alternative for Prometheus shops:** if you already run Prometheus, scrape an external blackbox probe of `/readyz` and render the status page from that + the SLO recording rules (`deploy/monitoring/olivares-slo.rules.yaml`). Either path; do not rely solely on the engine scraping itself.

---

## 2. Severity levels

Severity is tied to the SLOs and the error budget ([17-PRODUCTION-READINESS-SLO.md §3](17-PRODUCTION-READINESS-SLO.md)), so "how bad is it" and "what does it cost the budget" are the same scale.

| Sev | Definition | Examples | First update | Cadence |
|---|---|---|---|---|
| **SEV1** | Control plane **down** or governance unsafe; availability budget actively burning fast. | `/readyz` 503 / engine unscrapeable; store down with no recovery; audit ledger tamper detected; HITL/enforcement gate offline. | ≤ 15 min | every 30 min until mitigated |
| **SEV2** | **Degraded**; an SLO is breached or at imminent risk but the plane is serving. | ingest p99 over SLO (backpressure); elevated 5xx burning budget at 6x; one tenant noisy-neighboring others; off-box checkpoint anchor gone stale. | ≤ 30 min | every 1–2 h |
| **SEV3** | Minor / no SLO breach; cosmetic or low-impact. | a single non-critical endpoint slow; a transient blip that self-resolved; docs/UI defect. | next business day | on change |

**Mapping from alerts** (`deploy/monitoring/olivares-slo.rules.yaml`): `OlivaresStoreDown` / `OlivaresControlPlaneUnscrapeable` / `OlivaresErrorBudgetBurnFast` → **SEV1**; `OlivaresErrorBudgetBurnMedium` / `OlivaresIngestP99High` → **SEV2**; `…BurnSlow` / `…IngestSuccessLow` / `…ApiLatencyP99High` → **SEV2/SEV3** by judgment.

---

## 3. Roles (scale to the team you have)

- **Incident Commander (IC):** owns the incident, decides severity, coordinates, decides "resolved". On a solo/small team this is the on-call engineer.
- **Comms Lead:** owns external updates (status page + customer channels) on cadence. May be the IC for SEV2/3.
- **Scribe:** keeps the timeline (feeds the postmortem). For solo on-call, append to the incident channel as you go.

---

## 4. The incident-comms process

1. **Detect** — a burn-rate/latency/store alert fires (the rules page on-call), or the status page goes red.
2. **Declare** — IC assigns a severity (§2), opens an incident record (timeline), and for SEV1/2 posts the **initial** status-page update within the first-update SLA.
3. **Communicate** — Comms Lead posts updates on the cadence for the severity, even if the update is "still investigating, next update by HH:MM". Silence is the failure mode. Channels: the **status page** (always) + the customer channel (email/Slack) for SEV1/2.
4. **Mitigate** — work the matching runbook (`deploy/runbooks/`): ledger-verify failure, collector backpressure, failover, key-rotation. Mitigate (restore service) before root-causing.
5. **Resolve** — IC declares resolved once the SLI is back within SLO over the trailing window; post the **resolved** update.
6. **Learn** — per the error-budget policy ([17-PRODUCTION-READINESS-SLO.md §3](17-PRODUCTION-READINESS-SLO.md)): any single incident that burned **> 20%** of an SLO's 28-day budget, and every SEV1/SEV2, gets a **blameless postmortem with ≥ 1 P0 action item**. A class of incidents burning > 20% over a quarter → a quarterly-planning item.

**Maintenance is communicated, not silent.** Because a schema-change upgrade is downtime on the single-node tier ([17-PRODUCTION-READINESS-SLO.md §2.1](17-PRODUCTION-READINESS-SLO.md)), announce it in advance with a window; planned, announced maintenance inside an agreed window does not burn the budget against the customer ([17-PRODUCTION-READINESS-SLO.md §2.2](17-PRODUCTION-READINESS-SLO.md)).

---

## 5. Comms templates

**Initial (SEV1/2):**
> **[Investigating] {component} — {one-line symptom}.** Started {time UTC}. We are investigating {availability/ingest/latency} impact. Next update by {time}.

**Update:**
> **[Identified|Monitoring] {component}.** {What we know}. {What we're doing — e.g. "failing over per runbook" / "draining the affected node"}. Impact: {who/what}. Next update by {time}.

**Resolved:**
> **[Resolved] {component}.** Service restored at {time UTC}. Cause: {one line}. Duration: {mm}. Error-budget impact: ~{x}% of the {SLO} 28-day budget. A postmortem will follow {if SEV1/2 or >20% budget}.

**Postmortem skeleton** (blameless): timeline (UTC) · impact + budget spent · root cause · what went well / what was hard · action items (≥1 P0, owner, due) · whether the error-budget-policy freeze ([17-PRODUCTION-READINESS-SLO.md §3](17-PRODUCTION-READINESS-SLO.md)) applies.

---

## 6. References
- SLIs/SLOs/error-budget: `docs/17-PRODUCTION-READINESS-SLO.md`. Alert rules: `deploy/monitoring/olivares-slo.rules.yaml`. Status artifact: `deploy/monitoring/status-page.gatus.yaml`.
- Runbooks: `deploy/runbooks/`. Method: [Google SRE — Error Budget Policy](https://sre.google/workbook/error-budget-policy/).

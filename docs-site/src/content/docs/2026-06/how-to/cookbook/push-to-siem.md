---
title: "Recipe: push findings and the ledger to your SIEM"
description: Create a push sink — Splunk HEC, Microsoft Sentinel, Datadog or New
  Relic, or a generic HMAC-signed webhook — and subscribe it to findings and the
  sealed audit ledger, delivered at-least-once in OCSF, CEF or the format your
  tower speaks.
sidebar:
  order: 6
slug: 2026-06/how-to/cookbook/push-to-siem
---

**Goal:** your SIEM receives the control plane's findings *and* its
tamper-evident audit ledger as a push, without a forwarder tailing files.

This is the S2S (service-to-service) push path on the eventing platform. The
[pull export and file-tail postures](/2026-06/how-to/forward-audit-to-splunk/) remain
fully supported — pull is still the right shape for WORM archival and offline
re-verification; push is the right shape for live SIEM ingestion.

## 1. Create the sink subscription

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

* **`sink_kind`** selects the tower dialect: `splunk_hec`, `sentinel_dcr`,
  `datadog`, `newrelic` — or omit it entirely for the **generic webhook**
  (an HTTPS endpoint receiving the JSON event, authenticated by the
  engine's HMAC signature; rotate with `…/{id}/rotate-secret`).
* **`sink_format`**: `ocsf` (the default for SIEM sinks — the AI-aware
  schema), `cef`, `leef`, `syslog`, `otlp` or `json`.
* **`sink_cred`** (the HEC token / DCR bearer / API key) is accepted once,
  **sealed at rest, never returned or logged**. The vendor kinds require it
  at create; the generic webhook needs none.
* **`event_types`** is your stream selection: `finding.reported` for the
  findings rail, `audit.recorded` for the ledger (below), or both.

Test delivery before trusting it:

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. The ledger push, honestly described

Subscribing to **`audit.recorded`** turns on the ledger pump: the forwarder
walks each tenant's sealed audit ledger from a per-tenant cursor and enqueues
every record into the durable delivery engine — **at-least-once**, in order,
resumable. Each record carries its chain-integrity fields, so the SIEM copy
re-verifies offline exactly like the pull export's.

Three properties worth knowing:

* **No subscription, no work.** With no `audit.recorded` subscriber the pump
  writes nothing — the path costs nothing until you ask for it.
* **At-least-once means duplicates are possible** on redelivery; de-duplicate
  on the record's sequence number per tenant.
* **The pump is leader-gated** in HA — exactly one node forwards.

## 3. ITSM: findings as tickets

The same subscription mechanism drives ITSM destinations via the
notification rail — ServiceNow incidents and Jira issues from findings, with
severity mapped to priority. Configure those as notification
**destinations** (the `servicenow` / `jira` output connectors) rather than
SIEM sinks; the [Splunk page's destination table](/2026-06/how-to/forward-audit-to-splunk/)
shows the pattern.

## Verify end to end

1. `…/test` returns delivered.
2. Engage something observable (a [budget alert](/2026-06/how-to/cookbook/budgets-and-finops-guardrails/)
   threshold, a denied tool) and watch the finding arrive.
3. For the ledger: compare the SIEM-side `seq` high-water mark against
   `GET /v1/audit/export?from=<seq>` — the streams must agree.

## Notes

* Endpoints must be **HTTPS**; the engine refuses plaintext sinks.
* Posture snapshots (compliance/NHI/finding roll-ups) have their own export
  module riding the same rails — see the
  [compliance module](/2026-06/reference/modules/xiii-compliance/).
* The full decision table — when to pull, when to tail, when to push — is on
  the [Splunk forwarding page](/2026-06/how-to/forward-audit-to-splunk/).

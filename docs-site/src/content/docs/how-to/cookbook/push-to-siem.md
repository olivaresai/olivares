---
title: "Recipe: push findings and the ledger to your SIEM"
description: >-
  Create a push sink — Splunk HEC, Microsoft Sentinel, Datadog or New Relic,
  or a generic HMAC-signed webhook — and subscribe it to findings and the
  sealed audit ledger, delivered at-least-once in OCSF, CEF or the format
  your tower speaks.
sidebar:
  order: 6
---

**Goal:** your SIEM receives the control plane's findings *and* its
tamper-evident audit ledger as a push, without a forwarder tailing files.

This is the S2S (service-to-service) push path on the eventing platform. The
[pull export and file-tail postures](/how-to/forward-audit-to-splunk/) remain
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

- **`sink_kind`** selects the tower dialect: `splunk_hec`, `sentinel_dcr`,
  `datadog`, `newrelic` — or omit it entirely for the **generic webhook**
  (an HTTPS endpoint receiving the JSON event, authenticated by the
  engine's HMAC signature; rotate with `…/{id}/rotate-secret`).
- **`sink_format`**: `ocsf` (the default for SIEM sinks — the AI-aware
  schema), `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope` or `json`.

  :::caution[`sink_format` needs a `sink_kind`]
  A format is only applied when a sink kind is set. **Omitting `sink_kind` is not
  "the HTTPS option"** — it selects the generic webhook, which sends the Olivares
  event JSON and never validates `sink_format` at all. To post a SIEM dialect to
  your own endpoint, set `sink_kind: "https"` explicitly:

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  For `otlp` (and `otlp_envelope`, its exact alias) the endpoint must be the
  collector's exact `/v1/logs` path — the body is posted to the URL verbatim.
  :::
- **`sink_cred`** (the HEC token / DCR bearer / API key) is accepted once,
  **sealed at rest, never returned or logged**. The vendor kinds require it
  at create; the generic webhook needs none.
- **`event_types`** is your stream selection: `finding.reported` for the
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
resumable. Each record carries its chain-integrity fields verbatim, so the SIEM copy supports
exactly what the pull export supports: the chain LINKAGE
(`prev_hash` of n+1 equals `hash` of n) and a checkpoint signature over `hash` can
both be checked offline, and a record's `hash` can now be RE-DERIVED from one exported
line — every input the chain hash consumes travels on the wire, including the canonical
`occurred_at` text and the metadata commitment. Byte-exact re-derivation is proven today
for `syslog` and the three OTLP spellings, for the value alphabets this ledger emits
(UUIDs, `kind:id` actors, dotted verbs, a fixed-layout timestamp and hex digests) — syslog
substitutes a space for CR and LF, and OTLP replaces invalid UTF-8, so neither is
unconditional; `ocsf` (the sink default), `cef` and `leef`
carry the same fields but are not yet byte-reconstructable, because their escaping and
field mapping are lossy for free-text values — pick one of the proven tokens if you
intend to re-derive. That commitment is blinded per record,
so it completes the preimage without disclosing anything about the metadata behind it.
Three claims stay distinct: re-deriving the hash is not the same as verifying
AUTHENTICITY (that needs an externally trusted key) or COMPLETENESS (that needs
adjacent records and a checkpoint). The audit *archive* remains the stronger artifact —
it carries the metadata itself alongside its blind, so it can also answer WHICH metadata
a commitment covers.

Three properties worth knowing:

- **No subscription, no work.** With no `audit.recorded` subscriber the pump
  writes nothing — the path costs nothing until you ask for it.
- **At-least-once means duplicates are possible** on redelivery; de-duplicate
  on the record's sequence number per tenant.
- **The pump is leader-gated** in HA — exactly one node forwards.

## 3. ITSM: findings as tickets

The same subscription mechanism drives ITSM destinations via the
notification rail — ServiceNow incidents and Jira issues from findings, with
severity mapped to priority. Configure those as notification
**destinations** (the `servicenow` / `jira` output connectors) rather than
SIEM sinks; the [Splunk page's destination table](/how-to/forward-audit-to-splunk/)
shows the pattern.

## Verify end to end

1. `…/test` returns delivered.
2. Engage something observable (a [budget alert](/how-to/cookbook/budgets-and-finops-guardrails/)
   threshold, a denied tool) and watch the finding arrive.
3. For the ledger: compare the SIEM-side `seq` high-water mark against
   `GET /v1/audit/export?from=<seq>` — the streams must agree.

## Notes

- Endpoints must be **HTTPS**; the engine refuses plaintext sinks.
- Posture snapshots (compliance/NHI/finding roll-ups) have their own export
  module riding the same rails — see the
  [compliance module](/reference/modules/xiii-compliance/).
- The full decision table — when to pull, when to tail, when to push — is on
  the [Splunk forwarding page](/how-to/forward-audit-to-splunk/).

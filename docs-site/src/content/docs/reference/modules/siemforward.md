---
title: "SIEM/ITSM forwarder"
description: >-
  Ships the sealed, hash-chained audit ledger and governance findings to your
  SIEM and ITSM towers in their native dialect — OCSF 1.8, CEF, LEEF, syslog or
  OTLP — over the durable eventing platform, with a leader-gated cursor walk and
  at-least-once delivery. It renders and forwards; it never re-derives integrity.
---

The SIEM/ITSM forwarder (`modules/siemforward`) takes the evidence the
engine already seals and gets it into the tower your SOC already runs. It is
**LIVE**. It owns no new evidence: it walks the
tamper-evident audit ledger
and the governance findings stream, re-shapes each record into the destination's
native dialect, and hands it to the [eventing platform](/reference/modules/eventing/)
for durable delivery. Integrity fields travel verbatim — never re-derived in
transit.

## What it forwards, and how

Two halves cooperate. A **`SinkRenderer`** (it implements `eventing.SinkRenderer`)
re-shapes one captured event into the tower's wire format:

- `audit.recorded` — a sealed ledger record, rendered through `core/audit`.
- `finding.reported` — a governance finding (minimal-data: hash plus redacted
  excerpt).
- anything else on the bus — a format-neutral envelope a generic collector can
  parse itself.

Supported dialects: **OCSF 1.8**, **CEF**, **LEEF**, **syslog**, **OTLP**, and a
structured JSON passthrough. The renderer is **deny-closed**: an unknown sink
kind or an unrenderable format returns an error, and the engine retries then
dead-letters the delivery — never an unauthenticated or wrong-shaped send.

A **leader-gated forward pump** drives the rest. Each pass reads a per-tenant
cursor, walks the ledger from the next sequence in bounded batches, and enqueues
each record. The cursor advances only past records that enqueued successfully, so
a crash or restart resumes from where it stopped — **at-least-once** from the
ledger, the authoritative source. Re-walked records dedup downstream.

## Destinations

Where the ledger goes is a per-tenant eventing **sink subscription**, not a
self-service API on this module — it mounts no routes. Destinations are
**operator-provisioned**: Splunk HEC, Microsoft Sentinel (Logs Ingestion / DCR),
Datadog Logs, New Relic, or a generic HTTPS collector. The engine opens the
sealed credential and owns the transport; the renderer holds no state and no
credentials, so one instance serves every tenant and sink.

## Bounded context, stated plainly

- It **forwards**, it does not store. A tenant with no sink subscription is a
  no-op: nothing is enqueued, the cursor still advances, nothing is lost.
- Forwarding runs from the cursor walk, **outside the ledger seal transaction** —
  a network write never sits in the seal path.
- This is a **push to your tower**, distinct from the read-only
  [posture export](/reference/modules/posture-export/) pull. Tower-side
  ingestion is out of scope; we render to the published dialect and deliver.

## Related

- [Eventing](/reference/modules/eventing/) — the durable subscription surface
  (retry/backoff, DLQ, cursor replay) this module renders into.
- [Compliance](/reference/modules/xiii-compliance/) — the sealed,
  ledger-derived evidence package this stream complements.
- [Forward audit to Splunk](/how-to/forward-audit-to-splunk/) — the
  file-tail path when you cannot provision a native sink.
- [Honesty and limits](/start/honesty-and-limits/) — what "at-least-once" and
  "operator-provisioned" mean for this surface.

---
title: "Observability — the engine's read-model of itself"
description: >-
  A pure read-model over what already exists: which interop standards the
  engine pins and serves, what the W3C-correlated ledger says about a trace,
  and what is provably true about the running binary's supply chain. It owns
  no entities and persists nothing.
---

Observability (`modules/observability`) is one of the 30 modules — like
[live-ingest](/reference/modules/live-ingest/), it serves an architectural role
rather than filling a capability slot. It is the engine's **read-model of
itself**: three read-only surfaces under
`/v1/m/observability/` that answer questions the admin console's System
section renders, without owning a single store entity.

## The three surfaces

| Route | Answers |
|---|---|
| `GET /ingestion-health` | what flows in and out of the engine **per interop standard** — the standards the engine pins (OTel GenAI semconv, OCSF, ASIM, the unified SIEM formats, the ledger push, Prometheus text, W3C Trace Context), each with its verified version |
| `GET /traces`, `GET /traces/{id}` | what the **W3C-correlated ledger** says about a trace — the audit-side view of a distributed trace, joined by Trace Context |
| `GET /attestation` | what is **provably true about the running binary's supply chain** — the attestation surface the [verify-a-release chain](/how-to/verify-a-release/) feeds |

All three are reads with module-scoped permissions; nothing here mutates
anything.

## Why it is a module at all

The admin console needed an authoritative answer to "what does this engine
actually speak, and at which pinned version?" — and the honest way to serve
that is from the engine itself, not from documentation that can drift. The
ingestion-health table is generated from the same pins the connectors and
exporters compile against, so when a pin moves, the surface moves with it.

## Bounded context, stated plainly

- **It owns no store entities and persists nothing** — a pure read-model
  over substrates that already exist (the pins, the ledger, the attestation
  evidence).
- It is **not** [module XXII (health/SLA)](/reference/modules/xxii-health/),
  which is bounded to the reliability of the *estate's* agents and MCP
  servers. This module is about the *engine*.
- It is **not** the metrics endpoint: operational time-series live on
  [`/metrics`](/how-to/monitor-with-prometheus/); this module serves
  structured answers, not series.

## Related

- [Monitor with Prometheus](/how-to/monitor-with-prometheus/) — the
  operational metrics and SLOs.
- [Events reference](/reference/events/) — the bus vocabulary the ingestion
  table reports on.
- [Verify a release](/how-to/verify-a-release/) — the supply-chain evidence
  the attestation surface reflects.

---
title: Monitor with Prometheus (SLOs, metrics, alerts)
description: Scrape the engine's /metrics, adopt the published SLO targets, and
  load the shipped burn-rate alert rules — the same SLIs the product's own
  runbooks key on, with the single-writer numbers stated honestly.
slug: 2026-06/how-to/monitor-with-prometheus
---

The engine exposes three operational endpoints on the HTTP listener, all
probe-friendly:

| Endpoint | Auth | Purpose |
|---|---|---|
| `/livez` | none | process liveness — **no dependency checks**, so a store outage never causes a restart loop |
| `/readyz` | none | readiness — store ping (and HA leadership): `200 {"status":"ok","store":"up","leader":true,…}`, `503 {"status":"unavailable","store":"down"}`, or `503 {"status":"standby",…,"leader":false}` on an HA standby |
| `/metrics` | none | Prometheus exposition. Deliberately unauthenticated: it carries operational series, never tenant data |

`/readyz` reachability **is** the availability SLI.

## The metric set that matters

All series are registered by the engine (verified against the current code);
the load-bearing ones:

| Series | What it tells you |
|---|---|
| `olivares_store_up` | the store answers a ping — the first thing every runbook checks |
| `olivares_http_requests_total{code}` | request-success SLI (`code!~"5.."`) |
| `olivares_http_request_duration_seconds` | API latency (p99 target below) |
| `olivares_ingest_duration_seconds` | **the backpressure SLI** — ingest p99 rises when a subscriber saturates |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | ingest throughput and rejections |
| `olivares_eventbus_queue_depth` / `_queue_capacity` (per subscriber) | which module is the slow consumer |
| `olivares_eventbus_publish_blocked_total` | backpressure events (the bus blocks; it does not drop) |
| `olivares_eventbus_bridge_*` | NATS bridge health when the distributed bus is on — `_connected`, `_pending_messages`, `_dropped_total` (cross-node delivery is at-most-once; drops are counted, never silent) |
| `olivares_audit_checkpoint_age_seconds` | tamper-evidence freshness — alert when it exceeds 2× the checkpoint interval |
| `olivares_auth_login_attempts_total{outcome}` | login success / failure / lockout |
| `olivares_http_ratelimit_decisions_total{decision}` | rate-limit pressure |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | the collector→core ingest plane |

## SLO targets (published, honest)

The single-node targets — what the default topology actually supports — and
the HA tier:

| SLI | Single node | HA tier (Postgres) |
|---|---|---|
| Availability (`/readyz`) | **99.5% / 28d** | 99.9% / 28d |
| Request success (non-5xx) | **99.9%** | 99.95% |
| API latency p99 | **\< 300 ms** | \< 200 ms |
| Ingest latency p99 | **\< 250 ms** | \< 150 ms |
| Ingest success | **99.9%** | 99.95% |

The honesty in the numbers: a single writer on one node cannot promise three
nines of availability, so the docs do not — 99.5% (≈ 3h 39m budget per
28 days) is the single-node truth, and the 99.9% tier is earned by the
[HA topology](/2026-06/tutorials/getting-started/kubernetes/#3-active-passive-ha),
not by optimism.

## Load the shipped alert rules

`deploy/monitoring/olivares-slo.rules.yaml` ships 14 alerts ready for your
Prometheus: multi-window burn-rate alerts on the request-success budget
(fast 14.4× page / medium 6× page / slow 1× ticket), absolute latency and
availability fires (`OlivaresIngestP99High`, `OlivaresApiLatencyP99High`,
`OlivaresStoreDown`, `OlivaresControlPlaneUnscrapeable`), saturation
(`OlivaresEventBusSaturated` at >90% queue for 10m), bridge health
(`OlivaresEventBusBridgeDropping`, `OlivaresEventBusBridgeDisconnected`) and
ledger freshness (`OlivaresAuditCheckpointStale` at age > 2h).

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

On Kubernetes, the chart's `ServiceMonitor` option wires the scrape for the
Prometheus operator. A Gatus status-page config for the external `/readyz`
probe ships alongside the rules (`deploy/monitoring/status-page.gatus.yaml`).

## When an alert fires

Symptom-by-symptom diagnosis — store down, ingest p99 high, bus saturated,
checkpoint stale — is the [troubleshooting page](/2026-06/how-to/troubleshooting/),
distilled from the same runbooks the alert annotations reference.

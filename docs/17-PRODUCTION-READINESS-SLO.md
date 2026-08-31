<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Production readiness: SLIs, SLOs & the error-budget policy of the control plane

**Date:** 2026-06-09 · **Status:** published targets — 99.5% for the single-node topology; 99.9% is the HA active-passive TARGET (Postgres leader election is implemented; the operator's opt-in `LeaderRouting` path awaits a recorded qualification run, and the Helm chart is not yet qualified for it — see §2.1)

> The control plane instruments the *agents* it governs in fine detail. This document is the other half a serious buyer asks for: **how we measure and operate the plane itself as a service.** It defines the Service Level Indicators (what we measure), the Service Level Objectives (what we commit to), and the error-budget policy (what happens when we miss). Companion docs: capacity & sizing (`docs/SIZING-AND-CAPACITY.md`), status page & incident comms (`docs/STATUS-AND-INCIDENT-COMMS.md`), on-call runbooks (`deploy/runbooks/`), and the alert rules that make these SLOs fire (`deploy/monitoring/olivares-slo.rules.yaml`).
>
> Method follows the Google SRE Workbook (*Implementing SLOs*, *Error Budget Policy*, *Alerting on SLOs*). Every SLI below maps to a metric that **exists on `/metrics` today** — we do not commit to a signal we cannot measure.

---

## 0. TL;DR for the buyer

| Question | Answer |
|---|---|
| What's your availability SLO? | **99.5%/month** for the self-hosted single-node tier; **99.9%/month** is the HA active-passive TARGET — leader election is implemented, the qualified deployment path is the operator's opt-in `LeaderRouting`, and the Helm chart is not yet qualified (§2.1). (§2) |
| p99 of ingest? | SLO **< 250 ms**. Measured floor on reference HW: **~1.2 ms** at the single-writer ceiling (`docs/SIZING-AND-CAPACITY.md`). |
| p99 of the API? | SLO **< 300 ms** (read + write, all routes, per HTTP method). |
| events/sec per node? | **~1,500 durable writes/sec** single-writer on the reference HW; the bus moves ~3.8M/sec, so the **store write is the ceiling** (§ sizing). |
| When SQLite → Postgres? | When sustained writes approach the single-writer knee (~1–1.5k/s) **or** you need concurrent writers / HA. Concurrency makes SQLite *worse*, not better (sizing guide). |
| Status page? | Yes — self-hostable, driven by the real SLIs (`docs/STATUS-AND-INCIDENT-COMMS.md`). |
| Runbooks / on-call? | Yes — `deploy/runbooks/` (ledger-verify, collector backpressure, failover, key-rotation). |
| Error-budget policy? | Yes — §3. Budget exhausted ⇒ releases freeze (except P0/security) until the SLO recovers. |

---

## 1. Service Level Indicators (SLIs) — what we actually measure

Every SLI is `good events / valid events` (SRE). The control plane exposes a single, pure-Go Prometheus surface at **`GET /metrics`** (OpenMetrics-/Prometheus-text 0.0.4, unauthenticated, setup-exempt; bind it to a trusted scrape network — `core/api/metrics.go`, `core/metrics/metrics.go`). These are the **only** series that exist today; the SLOs commit to exactly these.

### 1.1 Availability / reachability — *is the control plane up?*

The honest primary signal is an **external blackbox probe of `/readyz`** (HTTP 200 vs 503/timeout), because a down engine cannot report its own downtime and a sub-scrape-interval outage is invisible to a self-scrape. `/readyz` returns **503** when the store ping fails (`core/api/metrics.go:130-144`); the load balancer drains the pod on that signal.

```
# Authoritative availability SLI = external prober (status page / blackbox_exporter):
#   good = readiness probes that returned 200 ; valid = all readiness probes
# Corroborating server-side gauge (only meaningful while the engine answers a scrape):
olivares_store_up            # 1 = store answered a 1s ping at scrape time, else 0
```

### 1.2 Request success rate — *of the requests we served, how many succeeded?*

```
sum(rate(olivares_http_requests_total{code!~"5.."}[28d]))
  / sum(rate(olivares_http_requests_total[28d]))
```
`olivares_http_requests_total{method,code}` (`core/api/metrics.go:42-43`, incremented at `:91`). 4xx are client errors and count as served (not budget burn); 5xx burn the budget.

### 1.3 API latency — *were requests fast enough?*

```
histogram_quantile(0.99, sum by (le) (rate(olivares_http_request_duration_seconds_bucket[5m])))
```
`olivares_http_request_duration_seconds` is a histogram **labelled by HTTP method only** — no route, no status (`core/api/metrics.go:44-45,:20`). **Caveat to internalize:** a method-level p99 mixes fast reads, slow module calls, and error latencies; it cannot isolate one endpoint. Buckets cap at **10 s**, so any p99 target must be < 10 s. Per-route/success-only p99 needs a route/status label, which would multiply cardinality — a deliberate trade-off recorded here, not an oversight (instrumentation roadmap, §5).

### 1.4 Ingest latency — *how fast does the collector→core path accept an observation?*

```
histogram_quantile(0.99, sum by (le) (rate(olivares_ingest_duration_seconds_bucket[5m])))
```
`olivares_ingest_duration_seconds` times the lift of one **accepted** observation onto the bus (`core/api/ingest.go` `Push`, registered `core/api/metrics.go`). Because the in-process bus applies **blocking backpressure** (a full subscriber queue blocks the publish *inside* `Ingest` — `core/eventbus/inproc.go:191-199`), this histogram **rises under backpressure** and is therefore both the ingest-p99 SLI and the primary backpressure signal (see the collector-backpressure runbook).

### 1.5 Ingest success rate

```
sum(rate(olivares_ingest_observations_total[5m]))
  / (sum(rate(olivares_ingest_observations_total[5m])) + sum(rate(olivares_ingest_rejected_total[5m])))
```
`olivares_ingest_observations_total{kind}` (accepted, `core/api/ingest.go`) and `olivares_ingest_rejected_total` (decode/publish failures after authorization). The accepted counter is also the **events/sec** throughput signal the sizing guide measures.

### 1.6 Saturation (supporting signals, not an SLO)

`olivares_http_requests_in_flight`, `go_goroutines`, `go_memstats_*` (`core/metrics/metrics.go`). Use for capacity/headroom, not as a committed objective.

### 1.7 Honesty guardrails — what is NOT a scrapeable engine SLI today

- **gRPC RPCs** emit `olivares_grpc_requests_total{method,code}` + `olivares_grpc_request_duration_seconds{method}` (`core/api/grpc_metrics.go`). The duration histogram covers UNARY RPCs only — `IngestService.Push` is a long-lived collector stream whose "duration" is its lifetime (it would land every sample in +Inf); streams count at completion and their per-observation latency SLI remains `olivares_ingest_duration_seconds`. **No gRPC SLO target is committed** — the error-ratio recording rule exists (`olivares:grpc_error_ratio:ratio_rate5m`) but objective-setting needs traffic data first.
- **OTEL GenAI metrics** (`gen_ai.client.operation.duration`, …) measure the *outbound Claude hop* and export via OTLP, **not `/metrics`** (`core/observability/trace/genai.go`, `provider.go`). They are not a control-plane serving SLI — do not cite them as one.
- **Inbound rate-limit metrics** are `olivares_http_ratelimit_decisions_total{class,decision}` + `olivares_http_ratelimit_active_buckets`. The SLI is the limited ratio over DECISIONS — `olivares:ratelimit_limited:ratio_rate5m` — deliberately NOT the `429` ratio of `olivares_http_requests_total`, which conflates the login lockout and FinOps denials with the rate limiter. With the shared store, `olivares_http_ratelimit_store_up`/`olivares_http_ratelimit_store_fallback_total` make degraded (per-node) enforcement alertable.
- **Backpressure SLI scope under the NATS bridge:** `olivares_ingest_duration_seconds` remains the backpressure SLI for the LOCAL path — the bridge does not change local fan-out (publishers still block on a saturated subscriber; ADR-0017). What the histogram can NOT see is cross-node loss: bridged events drop (counted) instead of backpressuring the remote publisher. The first-class saturation SLIs are now direct: `olivares_eventbus_queue_depth/{capacity}{subscriber}`, `olivares_eventbus_publish_blocked_total`, and on the bridge `olivares_eventbus_bridge_pending_messages` + `olivares_eventbus_bridge_dropped_total` (the real pre-loss queue is the bridge subscription's pending buffer, not the 256-deep local channels).

---

## 2. Service Level Objectives (SLOs) — what we commit to

**Measurement window:** rolling **28 days** (SRE general-purpose interval; integral weeks normalize weekday/weekend traffic). Weekly review, quarterly planning.

| SLI | SLO (self-hosted single-node, **today**) | SLO (HA tier — target, pending qualification) | Source of target |
|---|---|---|---|
| Availability (`/readyz` reachability) | **99.5%** / 28d (≈ **3h 39m**/30d budget) | **99.9%** / 28d (≈ **43m**/30d) | single-writer MTTR (§2.1) + SRE round-down |
| Request success (non-5xx) | **99.9%** of requests | 99.95% | current performance, rounded down |
| API latency p99 (per method) | **< 300 ms** | < 200 ms | measured store-write floor + handler headroom |
| Ingest latency p99 | **< 250 ms** | < 150 ms | measured ~1.2 ms floor + backpressure headroom |
| Ingest success rate | **99.9%** of authorized observations | 99.95% | decode/publish are rare, deterministic failures |

**PEP latency scope.** The policy-evaluation hot path is measured in memory by
`BenchmarkHookDecidePathHot` at approximately 2 allocs/op; it performs no store, network, or file I/O.
The budget is **< 5 ms p99 for the in-memory decision**, and the exact ns/op figure is
reference-hardware-dependent and must be owner-confirmed there. The HTTP-handler wrapper overhead above
the measured store-write floor, and end-to-end p99 of the hook, proxy, retrieval, and API paths under load,
remain SLO targets pending an owner-run load test on reference hardware; they are not independently
micro-measured.

**Error budget = 100% − SLO** (SRE). At 99.5%, ~0.5% of 28 days ≈ **3h 22m** of allowed unavailability per window; at 99.9%, ~**40m**. The published number is the SLO; the budget is the operating room the error-budget policy (§3) spends.

### 2.1 Why two tiers — 99.5% single-node, 99.9% HA

The **single-node tier** is single-writer: one StatefulSet replica, one ReadWriteOnce PVC. On node failure, recovery MTTR = pod reschedule + RWO volume detach/re-attach (single-AZ; can stall if the volume can't detach from a dead node) + cold start (TLS/key load, store open). A **schema-change upgrade is downtime** on this tier (RollingUpdate on a 1-replica StatefulSet terminates the old pod before the new one is Ready). A single such event can consume most of a 43-min (99.9%) monthly budget — so the single-node tier publishes **99.5%**.

The **HA tier is implemented** as active-passive over Postgres: `core.replicaCount > 1` with `core.engine=postgres` runs hot standbys behind Postgres-backed leader election (`core/internal/store/sqlstore/leader.go`, `leader_pg.go`); a standby answers **503 on `/readyz`**, so routing drains to the leader and failover is automatic; the chart requires a **shared audit-signing key** (`core.auditSigningKeySecret`, enforced in `deploy/helm/olivares/templates/_helpers.tpl`) so the ledger hash-chain does not fork on takeover; and schema changes follow the online **expand-contract** model (`docs/UPGRADE-AND-ROLLBACK.md`), so a routine upgrade is not an outage. Rollout and routing mechanics: `docs/HA-LEADER-ROUTING.md`. That topology is what the **99.9%** column commits to. We publish 99.5% for the single-node topology and 99.9% for HA — we do not claim multi-9s a deployment is not shaped to keep.

### 2.2 What counts against the budget

- **Burns budget:** 503s from `/readyz`, 5xx responses, ingest p99 over target, ingest rejects, planned schema-change downtime (it is downtime, budgeted like any other).
- **Does not burn budget:** 4xx (client errors), `setup_required` (a fresh engine is *ready to be set up*, `/readyz` still 200), maintenance announced and inside an agreed window with the customer (see incident-comms doc).

---

## 3. Error-budget policy

*Structure per the SRE Workbook error-budget-policy template. Owner: the production-readiness function. Disputes escalate to the maintainer (the CTO role) for a final call on budget math and required action.*

**Service & scope.** The Olivares control-plane engine (`cmd/olivares`): its HTTP/gRPC API, the collector→core ingest path, and the evidence ledger. Out of scope: the agents and external systems it governs, and customer-owned infrastructure.

**Goals.** Keep reliability at or above the published SLOs (§2) over the rolling 28-day window; make every miss visible and actioned; spend the budget deliberately on change velocity.
**Non-goals.** 100% reliability (the wrong target — SRE). Gating *all* engineering on a green budget; the policy gates **risky change**, not bug-fixes or security.

**SLO-miss policy (budget exhausted).** When the 28-day error budget for an SLO is spent:
1. **Freeze** all feature changes and non-essential releases to the affected component — **except P0 incident fixes and security fixes** — until the SLO is back above target over the trailing window.
2. The next on-call cycle's priority shifts to reliability work for the affected component.
3. Resume normal change velocity once the trailing-window SLO recovers.

**Outage policy (postmortem triggers).**
- Any single incident that consumes **> 20%** of an SLO's 28-day budget ⇒ **blameless postmortem with ≥ 1 P0 action item**.
- A class of incidents consuming **> 20%** of an SLO's budget **over a quarter** ⇒ a quarterly-planning action item to address the class.
- All P0/P1 incidents (severity in `docs/STATUS-AND-INCIDENT-COMMS.md`) get a postmortem regardless of budget.

**Budget-spent-but-not-our-fault exceptions** (feature work may continue): the burn was caused by underlying infrastructure outside the engine, by a dependency another team froze on, by out-of-scope traffic, or by a metric mis-categorization with no real user impact — each must be evidenced in the incident record, not asserted.

**Escalation.** Disagreement over whether the budget was spent, or which action applies, escalates to the maintainer for a final determination.

---

## 4. Alerting on the SLOs (burn-rate)

Alert on **error-budget burn rate**, not on raw thresholds, using the SRE multiwindow / multi-burn-rate method (a fast page + a slow ticket, each gated by a long-and-short window pair so the alert resets quickly and ignores blips). Burn rate `r` means the budget is being consumed `r×` faster than the SLO allows; `r` sustained over the window consumes `burn_rate × window / period` of the budget.

For a **99.9%** objective (SRE Table 5-8), the starting configuration is:

| Severity | Long window | Short window | Burn rate | Budget consumed |
|---|---|---|---|---|
| **Page** | 1 h | 5 m | **14.4** | 2% |
| **Page** | 6 h | 30 m | **6** | 5% |
| **Ticket** | 3 d | 6 h | **1** | 10% |

For the **99.5%** tier the budget is 5× larger, so the same *fraction-of-budget* thresholds correspond to a higher absolute error rate; the rules file derives both. The runnable, metric-wired rules (request-success, ingest p99, ingest success, store-down) are in **`deploy/monitoring/olivares-slo.rules.yaml`** with the exact PromQL.

---

## 5. Instrumentation: what was added, and the honest roadmap

**Added, because an SLO must be measurable:**
- `olivares_ingest_duration_seconds` — the ingest-latency histogram (§1.4); the ingest-p99 SLO was unmeasurable before.
- `olivares_ingest_rejected_total` — the ingest success-rate denominator (§1.5).
- `olivares audit verify --strict` — non-zero exit on a failed integrity check, so the on-call ledger-verify check (`deploy/runbooks/ledger-verify-failure.md`) can gate on `$?` instead of silently passing on a tampered chain.

**Event-bus and gRPC instrumentation (with the emitting code):**
- **gRPC SLIs** — `olivares_grpc_requests_total{method,code}` + `olivares_grpc_request_duration_seconds{method}` (unary-only duration; §1.7) via outermost interceptors, so auth rejections are measured too (`core/api/grpc_metrics.go`).
- **Event-bus saturation** — `olivares_eventbus_queue_depth/{queue_capacity}{subscriber}` (per-subscriber, labeled by module name via the `SubscribeNamed` extension), `olivares_eventbus_publish_blocked_total` (the in-proc backpressure event), `olivares_eventbus_publish_dropped_total`, `olivares_eventbus_handler_errors_total`; with the NATS bridge also `olivares_eventbus_bridge_{connected,pending_messages,dropped_total,publish_errors_total,decode_errors_total}` (`core/eventbus/stats.go`, `cmd/olivares/busmetrics.go`). The bus stays dependency-free: it exposes a `Stats()` snapshot through an optional extension interface and the composition root registers scrape-time collectors.
- **Durable-bus plane** — when the JetStream backend is active, it exposes `olivares_durablebus_connected`, `olivares_durablebus_leading`, `olivares_durablebus_stream_pending`, `olivares_durablebus_published_total`, `olivares_durablebus_publish_errors_total`, `olivares_durablebus_injected_total`, `olivares_durablebus_dedup_skipped_total`, `olivares_durablebus_inject_errors_total`, `olivares_durablebus_decode_errors_total`, `olivares_durablebus_kv_errors_total`, and `olivares_durablebus_no_dedup_id_total` (`cmd/olivares/busmetrics.go:98-145`). These distinguish connection/leadership state, backlog, confirmed publish/inject progress, publish loss, retryable injection failure, decode failure, and dedup degradation.
- **Ledger health** — `olivares_audit_checkpoint_age_seconds` (emitted only by the active leader after its first checkpoint, so a standby never false-pages) + `olivares_audit_checkpoint_failures_total` on the previously slog-only failure path (`cmd/olivares/checkpoint.go`). Alert rules: `OlivaresAuditCheckpointStale/Failing`.
- **Login/abuse** — `olivares_auth_login_attempts_total{outcome}` (`success|failed|locked_out`, three series pre-created at zero; password-login surface — SSO mints sessions elsewhere). Credential outcomes only: a store outage during login is a 5xx, not an abuse signal (`core/api/handlers_auth.go`).
- **Inbound rate-limit SLI** — wired into the rules file as `olivares:ratelimit_limited:ratio_rate5m` over the decisions counter (§1.7), plus the shared-store degradation signals (`store_up`, `store_fallback_total`, `store_buckets`).

**Still roadmap (named honestly, not delivered):**
- **Last-verify-status signal** — `audit verify --strict` remains a CLI/cron concern; an in-engine gauge for the last off-box verification result is future work (the checkpoint age + failure counter cover the anchor-freshness half of the original bullet).
- **gRPC SLO target** — the series and the error-ratio recording rule exist; committing an objective needs production traffic data first (§1.7).

---

## 6. References

- Google SRE Workbook: [Implementing SLOs](https://sre.google/workbook/implementing-slos/) · [Error Budget Policy](https://sre.google/workbook/error-budget-policy/) · [Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/).
- Code SLI surface: `core/api/metrics.go`, `core/metrics/metrics.go`, `core/api/ingest.go`, `core/api/grpc.go`.
- Companion docs: `docs/SIZING-AND-CAPACITY.md`, `docs/STATUS-AND-INCIDENT-COMMS.md`, `deploy/runbooks/`, `deploy/monitoring/olivares-slo.rules.yaml`.
- HA toward the 99.9% target: Postgres leader election and `/readyz` drain are implemented; the operator's opt-in `LeaderRouting` is the qualified path once its cluster run is recorded, and the shipped Helm chart remains on the legacy layout (`docs/HA-LEADER-ROUTING.md` documents both and the gap).

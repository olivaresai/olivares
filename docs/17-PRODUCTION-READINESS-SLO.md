<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

<a id="production-readiness-slis-slos--the-error-budget-policy-of-the-control-plane"></a>

# Production readiness: metrics, operating targets and error budgets

**Date:** 2026-06-09 (original targets) · **Support classification updated:** 2026-09-27.
**Status:** operating targets and metric definitions, not a release support commitment.
The [v26.10 deployment support matrix](../deploy/support-matrix.md) is the canonical
qualification record and supersedes this guide's former support classification.
The 99.5% single-node and 99.9% HA figures remain targets; neither is a customer SLA.

Service level indicators (SLIs) measure the engine; service level objectives
(SLOs) set operating targets. Error budgets determine the response to a missed
target. The method follows the Google SRE Workbook (*Implementing SLOs*,
*Error Budget Policy*, *Alerting on SLOs*). Each SLI below uses a `/metrics` series;
a deployment support commitment also requires its measured support record.
Companion guides and alert rules are listed in [References](#6-references).

---

<a id="0-tldr-for-the-buyer"></a>

## 0. Operating targets and limits

| Measure or operation | Target or reference |
|---|---|
| Availability | Targets: **99.5%** for single-node and **99.9%** for HA over the **28-day** window (§2). A complete v26.10 deployment qualification is not recorded here; configuration and leader election alone do not establish one (§2.1). |
| Ingest p99 | Target **< 250 ms**. The historical SQLite write-path sample was **~1.2 ms** p99 on reference hardware with tmpfs; it is not an end-to-end ingest qualification (`docs/SIZING-AND-CAPACITY.md`). |
| API p99 | Target **< 300 ms**, aggregated per HTTP method; this does not establish each route's p99. |
| Events per second per node | Historical reference workload: **~1,500 SQLite writes/sec** on tmpfs and **~3.8M bus events/sec** in a separate store-free benchmark. These are workload observations, not v26.10 per-node capacity promises (§ sizing). |
| SQLite to PostgreSQL | Measure the single-writer knee on your storage and workload. The historical reference was ~1–1.5k/s. PostgreSQL supports concurrent database writers; a multi-instance HA deployment requires separate fencing and recovery qualification (sizing guide). |
| Status page | Yes — self-hostable, driven by the real SLIs (`docs/STATUS-AND-INCIDENT-COMMS.md`). |
| On-call runbooks | Yes — `deploy/runbooks/` (ledger-verify, collector backpressure, failover, key-rotation). |
| Error-budget policy | Yes — §3. Budget exhausted ⇒ releases freeze (except P0/security) until the SLO recovers. |

---

## 1. Service Level Indicators (SLIs) — what we actually measure

Every SLI is `good events / valid events` (SRE). Olivares AI exposes a single, pure-Go Prometheus surface at **`GET /metrics`** (OpenMetrics-/Prometheus-text 0.0.4, unauthenticated, setup-exempt; bind it to a trusted scrape network — `core/api/metrics.go`, `core/metrics/metrics.go`). The following sections name the series used by these targets; their presence does not establish a deployment support commitment.

<a id="11-availability--reachability--is-the-control-plane-up"></a>

### 1.1 Availability and reachability

The honest primary signal is an **external blackbox probe of `/readyz`** (HTTP 200 vs 503/timeout), because a down engine cannot report its own downtime and a sub-scrape-interval outage is invisible to a self-scrape. `/readyz` returns **503** when the store ping fails, when this node is a standby, when setup state cannot be observed, when first-boot enumeration is not authoritative, or when the setup capability probe fails (`handleReadyz` in `core/api/metrics.go`). The load balancer drains the pod on that 503. A 503 is not success.

```
# Authoritative availability SLI = external prober (status page / blackbox_exporter):
#   good = readiness probes that returned 200 ; valid = all readiness probes
# Corroborating server-side gauge (only meaningful while the engine answers a scrape):
olivares_store_up            # 1 = store answered a 1s ping at scrape time, else 0
```

<a id="12-request-success-rate--of-the-requests-we-served-how-many-succeeded"></a>

### 1.2 Request success rate

```
sum(rate(olivares_http_requests_total{code!~"5.."}[28d]))
  / sum(rate(olivares_http_requests_total[28d]))
```
`olivares_http_requests_total{method,code}` (`core/api/metrics.go:42-43`, incremented at `:91`). 4xx are client errors and count as served (not budget burn); 5xx burn the budget.

<a id="13-api-latency--were-requests-fast-enough"></a>

### 1.3 API latency

```
histogram_quantile(0.99, sum by (le) (rate(olivares_http_request_duration_seconds_bucket[5m])))
```
`olivares_http_request_duration_seconds` is a histogram **labelled by HTTP method only** — no route, no status (`core/api/metrics.go:44-45,:20`). **Scope:** a method-level p99 mixes fast reads, slow module calls, and error latencies; it cannot isolate one endpoint. Buckets cap at **10 s**, so any p99 target must be < 10 s. Per-route/success-only p99 needs a route/status label, which would multiply cardinality — a deliberate cardinality trade-off (instrumentation roadmap, §5).

<a id="14-ingest-latency--how-fast-does-the-collectorcore-path-accept-an-observation"></a>

### 1.4 Ingest latency

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
- **OTEL GenAI metrics** (`gen_ai.client.operation.duration`, …) measure the *outbound Claude hop* and export via OTLP, **not `/metrics`** (`core/observability/trace/genai.go`, `provider.go`). They are not an engine-serving SLI — do not cite them as one.
- **Inbound rate-limit metrics** are `olivares_http_ratelimit_decisions_total{class,decision}` + `olivares_http_ratelimit_active_buckets`. The SLI is the limited ratio over DECISIONS — `olivares:ratelimit_limited:ratio_rate5m` — deliberately NOT the `429` ratio of `olivares_http_requests_total`, which conflates the login lockout and FinOps denials with the rate limiter. With the shared store, `olivares_http_ratelimit_store_up`/`olivares_http_ratelimit_store_fallback_total` make degraded (per-node) enforcement alertable.
- **Backpressure SLI scope under the NATS bridge:** `olivares_ingest_duration_seconds` remains the backpressure SLI for the LOCAL path — the bridge does not change local fan-out (publishers still block on a saturated subscriber; event-bus contract). What the histogram can NOT see is cross-node loss: bridged events drop (counted) instead of backpressuring the remote publisher. The first-class saturation SLIs are now direct: `olivares_eventbus_queue_depth/{capacity}{subscriber}`, `olivares_eventbus_publish_blocked_total`, and on the bridge `olivares_eventbus_bridge_pending_messages` + `olivares_eventbus_bridge_dropped_total` (the real pre-loss queue is the bridge subscription's pending buffer, not the 256-deep local channels).

---

<a id="2-service-level-objectives-slos--what-we-commit-to"></a>

## 2. Service Level Objectives (SLOs) — operating targets

**Measurement window:** rolling **28 days** (SRE general-purpose interval; integral weeks normalize weekday/weekend traffic). Weekly review, quarterly planning.

| SLI | Target (self-hosted single-node) | Target (HA, pending qualification) | Historical planning basis, not a measured service guarantee |
|---|---|---|---|
| Availability (`/readyz` reachability) | **99.5%** / 28d (≈ **3h 22m**/28d budget) | **99.9%** / 28d (≈ **40m**/28d) | historical topology planning (§2.1); qualification required |
| Request success (non-5xx) | **99.9%** of requests | 99.95% | proposed operating objective; release workload qualification required |
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

Single-node production is an intended topology when its deployment is qualified;
HA is not a prerequisite. The Kubernetes example here is one StatefulSet replica
and one ReadWriteOnce PVC. On node failure, recovery includes pod rescheduling,
volume detach/re-attach (which can stall on a dead node), and cold start. A
schema-change upgrade can require downtime: a one-replica StatefulSet rollout
terminates the old pod before the replacement is Ready. Those costs motivated the
historical **99.5%** target; they do not prove that a deployment meets it.

Active-passive configuration exists for `core.replicaCount > 1` with
`core.engine=postgres`, Postgres-backed leader election, and the required shared
`core.auditSigningKeySecret`. The operator's opt-in `LeaderRouting` separates pod
health from traffic readiness; the Helm chart retains the legacy layout. These
mechanisms are described in [HA leader routing](HA-LEADER-ROUTING.md), including
its test scope and residual failure discussion. They do not establish complete
HA qualification or the **99.9%** target. The qualification record must bind actual
write/effect fencing coverage, node/database/network faults, recovery, and the
observed behavior of sessions and in-flight work.

The [expand-contract upgrade procedure](UPGRADE-AND-ROLLBACK.md) is a compatibility
mechanism, not a promise that routine upgrades cause no outage. Measure rollout,
readiness failure and the declared compatible recovery path on the exact deployment;
record any interruption. The support matrix controls the release status.

### 2.2 What counts against the budget

- **Burns budget:** 503s from `/readyz`, 5xx responses, ingest p99 over target, ingest rejects, planned schema-change downtime (it is downtime, budgeted like any other).
- **Does not burn budget:** 4xx (client errors), HTTP 200 from `/readyz` with `setup_required=true` (a fresh engine whose first-boot prerequisites work is *ready to be set up*; that observation is not an outage), maintenance announced and inside an agreed window with the customer (see incident-comms doc). A 503 from `/readyz` — including `setup_blocked` and `setup_unavailable` — is not this case and burns budget as above. `setup_required` omitted (unknown setup state) is a 503, not a ready observation.

---

## 3. Error-budget policy

*Structure per the SRE Workbook error-budget-policy template. Owner: the production-readiness function. Disputes escalate to the maintainer (the CTO role) for a final call on budget math and required action.*

**Service & scope.** The Olivares AI engine (`cmd/olivares`): its HTTP/gRPC API, the collector→core ingest path, and the evidence ledger. Out of scope: the agents and external systems it governs, and customer-owned infrastructure.

**Goals.** For a deployment adopting these operating targets, keep reliability at or above its selected SLOs (§2) over the rolling 28-day window; make every miss visible and actioned; spend the budget deliberately on change velocity.
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

For a **99.9%** objective (SRE Table 5-8), the starting configuration below
expresses the source example's budget fractions over **30 days**. For this guide's
**28-day** window, use the formula above: the same rates/windows consume about
**2.14%**, **5.36%** and **10.71%** respectively. The alert rates are unchanged.

| Severity | Long window | Short window | Burn rate | Budget consumed over 30d (reference) |
|---|---|---|---|---|
| **Page** | 1 h | 5 m | **14.4** | 2% |
| **Page** | 6 h | 30 m | **6** | 5% |
| **Ticket** | 3 d | 6 h | **1** | 10% |

For the **99.5%** tier the budget is 5× larger, so the same *fraction-of-budget* thresholds correspond to a higher absolute error rate; the rules file derives both. The runnable, metric-wired rules (request-success, ingest p99, ingest success, store-down) are in **`deploy/monitoring/olivares-slo.rules.yaml`** with the exact PromQL.

---

<a id="5-instrumentation-what-was-added-and-the-honest-roadmap"></a>

## 5. Instrumentation and remaining work

**Available signals and checks:**
- `olivares_retirement_failures_total{cause}` — retirement pump failures, with three fixed causes: `no_declared_modules`, `read_due_failed`, and `record_pass_failed`. Each failed record pass increments the last cause, even when the batch continues and returns no error. All three series start at zero; idle passes, successful passes and shutdown cancellation do not increment them. Scrape `/metrics` and check `increase(olivares_retirement_failures_total[5m]) > 0`; inspect the retirement WARN logs for the specific composition or module error, repair it, and confirm the counter stops increasing on retries. Counts are per process and reset on restart; no account, tenant or raw error is exposed as a label.
- `olivares_ingest_duration_seconds` — the ingest-latency histogram (§1.4); the ingest-p99 SLO was unmeasurable before.
- `olivares_ingest_rejected_total` — the ingest success-rate denominator (§1.5).
- `olivares audit verify --strict` — non-zero exit on a failed integrity check, so the on-call ledger-verify check (`deploy/runbooks/ledger-verify-failure.md`) can gate on `$?` instead of silently passing on a tampered chain.

**Event-bus and gRPC instrumentation (with the emitting code):**
- **gRPC SLIs** — `olivares_grpc_requests_total{method,code}` + `olivares_grpc_request_duration_seconds{method}` (unary-only duration; §1.7) via outermost interceptors, so auth rejections are measured too (`core/api/grpc_metrics.go`).
- **Event-bus saturation** — `olivares_eventbus_queue_depth/{queue_capacity}{subscriber}` (per-subscriber, labeled by module name via the `SubscribeNamed` extension), `olivares_eventbus_publish_blocked_total` (the in-proc backpressure event), `olivares_eventbus_publish_dropped_total`, `olivares_eventbus_handler_errors_total`; with the NATS bridge also `olivares_eventbus_bridge_{connected,pending_messages,dropped_total,publish_errors_total,decode_errors_total}` (`core/eventbus/stats.go`, `cmd/olivares/busmetrics.go`). The bus stays dependency-free: it exposes a `Stats()` snapshot through an optional extension interface and the composition root registers scrape-time collectors.
- **Durable-bus plane** — when the JetStream backend is active, it exposes `olivares_durablebus_connected`, `olivares_durablebus_leading`, `olivares_durablebus_stream_pending`, `olivares_durablebus_published_total`, `olivares_durablebus_publish_errors_total`, `olivares_durablebus_injected_total`, `olivares_durablebus_dedup_skipped_total`, `olivares_durablebus_inject_errors_total`, `olivares_durablebus_decode_errors_total`, `olivares_durablebus_kv_errors_total`, and `olivares_durablebus_no_dedup_id_total` (`cmd/olivares/busmetrics.go:98-145`). These distinguish connection/leadership state, backlog, confirmed publish/inject progress, publish loss, retryable injection failure, decode failure, and dedup degradation.
- **Ledger health** — `olivares_audit_checkpoint_age_seconds` (emitted only by the active leader after its first checkpoint, so a standby never false-pages) + `olivares_audit_checkpoint_failures_total` on the previously slog-only failure path (`cmd/olivares/checkpoint.go`). Alert rules: `OlivaresAuditCheckpointStale/Failing`.
- **Login/abuse** — `olivares_auth_login_attempts_total{outcome}` (`success|failed|locked_out|abandoned`, four series pre-created at zero; password-login surface — SSO mints sessions elsewhere). `locked_out` is any refusal by the throttle, `abandoned` an attempt whose caller went away before the credential was read — most visibly while a tripped client address was holding it, so an abuse query never loses the attempts the throttle is slowing. Credential outcomes only: a store outage during login is a 5xx, not an abuse signal (`core/api/handlers_auth.go`).
- **Inbound rate-limit SLI** — wired into the rules file as `olivares:ratelimit_limited:ratio_rate5m` over the decisions counter (§1.7), plus the shared-store degradation signals (`store_up`, `store_fallback_total`, `store_buckets`).

**Remaining work:**
- **Last-verify-status signal** — `audit verify --strict` remains a CLI/cron concern; an in-engine gauge for the last off-box verification result is future work (the checkpoint age + failure counter cover the anchor-freshness half of the original bullet).
- **gRPC SLO target** — the series and the error-ratio recording rule exist; committing an objective needs production traffic data first (§1.7).

---

## 6. References

- Google SRE Workbook: [Implementing SLOs](https://sre.google/workbook/implementing-slos/) · [Error Budget Policy](https://sre.google/workbook/error-budget-policy/) · [Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/).
- Code SLI surface: `core/api/metrics.go`, `core/metrics/metrics.go`, `core/api/ingest.go`, `core/api/grpc.go`.
- Companion docs: `docs/SIZING-AND-CAPACITY.md`, `docs/STATUS-AND-INCIDENT-COMMS.md`, `deploy/runbooks/`, `deploy/monitoring/olivares-slo.rules.yaml`.
- HA toward the 99.9% target: [leader-routing mechanics and test limits](HA-LEADER-ROUTING.md); the [canonical support matrix](../deploy/support-matrix.md) requires the complete deployment record. One cluster routing run alone does not qualify write fencing, recovery or every workload.

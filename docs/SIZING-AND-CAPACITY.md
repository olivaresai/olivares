<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Capacity, sizing & the SQLite→Postgres threshold

**Date:** 2026-06-09 · **Status:** reproducible baseline measured on reference hardware (see provenance)

> Answers the buyer's capacity questions — *events/sec per node, p99, governed decisions/sec, RAG corpus ceiling, storage growth, tenants, recovery time, when do I move SQLite→Postgres* — with **reproducible benchmarks, not asserted numbers**. The harness spans `core/bench/`, `cmd/olivares/`, `modules/inferenceproxy/` and `modules/knowledge/` (`task bench` → `scripts/bench-capacity.sh`); every number below was produced by it and is reproducible on your own hardware. Read §1 (how to reproduce) before trusting §2 (the write baseline); §2-ter adds the decision plane, retrieval plane and storage growth. **Write throughput is storage-bound** and the baseline was measured on a RAM filesystem.

---

## 1. How to reproduce (do this on YOUR target before sizing)

```bash
task bench                              # SQLite only
OLIVARES_TEST_POSTGRES_DSN=postgres://app@host/db \
  task bench                            # also measure Postgres (app-role DSN)
# knobs: BENCHTIME=5s BENCH=BenchmarkAuditAppend OUT=bench.txt bash scripts/bench-capacity.sh
```

The runner (`scripts/bench-capacity.sh`) stamps the result with CPU, GOMAXPROCS, Go version, and **the filesystem of `$TMPDIR`** — because each durable write costs one commit, and a commit's cost is dominated by **fsync**, which is a property of the storage, not the CPU. The benchmarks open a real store (pure-Go SQLite WAL on an on-disk temp file, or Postgres via DSN) wired with a real per-event Ed25519 signer, provision a tenant, and drive the actual write paths (`core/bench/capacity_test.go`).

What each benchmark measures:
- `BenchmarkAuditAppend` — signed, hash-chained audit append: the heaviest *universal* write (every governed mutation appends one) and the ledger throughput ceiling.
- `BenchmarkAccessEdgeUpsert` — the observation→edge write (each ingested observation materializes/merges an access edge); a fresh origin per op = the conservative INSERT case. **This is the closest proxy to "events/sec per node."**
- `BenchmarkAgentCreate` — a plain entity INSERT.
- `BenchmarkWriteScaling` — the same write from 1, 4, 16 concurrent writers: characterizes the single-writer ceiling.
- `BenchmarkBusFanout` — the in-process bus alone (store-free): proves the bus is not the bottleneck.

---

## 2. Reference baseline (2026-06-09)

**Provenance.** AMD Ryzen 7 5800U · 32 GiB RAM · go 1.26.4 · GOMAXPROCS 12 · pure-Go `modernc.org/sqlite`, WAL, `busy_timeout=5000ms`, `synchronous` = modernc default. **`$TMPDIR` was `tmpfs` (RAM).**

> ⚠️ **fsync caveat — read this before using the numbers.** On tmpfs, a WAL commit's fsync is essentially free. So the throughput below is an **upper bound** and the latency a **lower bound** versus durable storage (SSD/NVMe/networked block). On a real disk, expect **lower writes/sec and higher p99** — fsync, not CPU, becomes the limit. Treat these as the *CPU-and-serialization* ceiling and **re-run on your storage class** for a production number. The `synchronous` PRAGMA is unset (modernc default); pin it before publishing durability-sensitive numbers.

### 2.1 Single-writer throughput & latency (SQLite, sequential)

| Write path | events/sec | p50 | p95 | p99 | max | alloc/op |
|---|---:|---:|---:|---:|---:|---:|
| Audit append (signed + chained) | **1,489** | 0.68 ms | 0.84 ms | **1.19 ms** | 8.36 ms | 105 |
| Access-edge upsert (INSERT) | **1,475** | 0.68 ms | 0.86 ms | **1.21 ms** | 8.94 ms | 199 |
| Agent create (entity INSERT) | **1,884** | 0.54 ms | 0.71 ms | **1.09 ms** | 8.20 ms | 65 |

**Read this as:** one node sustains **~1.5k durable writes/sec** with **sub-ms p50 and ~1.2 ms p99** at the single-writer ceiling on RAM-backed storage. The ingest-latency SLO (< 250 ms, [17-PRODUCTION-READINESS-SLO.md §2](17-PRODUCTION-READINESS-SLO.md)) has ~200× headroom over this floor — the SLO exists to catch **backpressure** (when a slow subscriber blocks the publish and ingest latency climbs toward seconds), not the happy path.

### 2.2 The single-writer ceiling — concurrency makes SQLite *worse* (the SQLite→Postgres smoking gun)

| Concurrent writers | SQLite aggregate events/sec |
|---:|---:|
| 1 | **1,503** |
| 4 | 1,052 |
| 16 | **674** |

SQLite is pinned to **one connection** (`SetMaxOpenConns(1)` in `core/internal/store/sqlstore/store.go`) because SQLite is single-writer by design; there is no Go mutex — the connection cap *is* the serialization point. Adding concurrent writers does not add throughput; it adds contention and `busy_timeout` backoff, so aggregate throughput **falls**. This is the empirical proof that you cannot scale SQLite writes by throwing concurrency at it.

### 2.3 The bus is not the bottleneck

| Subscribers | bus events/sec |
|---:|---:|
| 1 | **3,829,168** |
| 4 | 1,542,709 |

The in-process event bus moves **~3.8M events/sec** — ~2,500× the durable-write rate. So the **store write is unambiguously the per-node ceiling**; the bus and CPU have ample headroom. (The bus applies bounded, blocking backpressure: a slow subscriber's full 256-deep queue blocks the publisher rather than dropping events — `core/eventbus/inproc.go`.)

---

## 2-bis. Measured baseline on durable disk + real Postgres (2026-06-12)

**Provenance.** Same reference hardware (Ryzen 7 5800U · 32 GiB · go1.26.4 · GOMAXPROCS 12), but
**`$TMPDIR` on ext4/NVMe (durable disk, fsync real)** and a **real PostgreSQL 17.10** on the same
NVMe (`fsync=on`, `synchronous_commit=on` defaults), measured by `scripts/bench-capacity.sh` with
`OLIVARES_TEST_POSTGRES_DSN` set — the first run of the §1 harness against an actual Postgres
(the run that surfaced and fixed a latent PG-only bug in the access-edge upsert: unqualified
`ON CONFLICT` merge columns, SQLSTATE 42702, `core/internal/store/sqlstore/accessgraph.go`).

### 2-bis.1 Single-writer (sequential), both engines

| Write path | SQLite ev/s | SQLite p99 | Postgres ev/s | Postgres p99 |
|---|---:|---:|---:|---:|
| Audit append (signed + chained) | **1,533** | 1.12 ms | **1,338** | 0.99 ms |
| Access-edge upsert (INSERT) | **1,522** | 1.22 ms | **1,565** | 0.87 ms |
| Agent create (entity INSERT) | **1,831** | 1.07 ms | **1,912** | 0.70 ms |

Note the tmpfs caveat resolved empirically: on this NVMe, SQLite's durable single-writer numbers
match the RAM-backed baseline within noise (fast fsync), so §2's numbers were not inflated here.

### 2-bis.2 The empirical SQLite→Postgres crossover (the §3 question, answered with data)

| Concurrent writers | SQLite aggregate ev/s | Postgres aggregate ev/s |
|---:|---:|---:|
| 1 | 1,524 | **1,562** |
| 4 | 1,499 | **5,203** |
| 16 | 1,473 | **7,829** |

SQLite stays flat (~1.5k/s) at every concurrency — the single-connection serialization point.
Postgres matches it at one writer and climbs with concurrency (3.5× at 4 writers, 5.3× at 16).
**Measured crossover: between 1 and 2 concurrent writers — effectively, any concurrent write load
at all favors Postgres on this class of hardware.** Single-writer latency is a wash (Postgres p99
is tighter; its audit-append throughput is ~13% lower per the chained-row round trips).

### 2-bis.3 Rate-limit admission cost (shared store vs in-proc)

| Path | p50 | p99 | takes/s |
|---|---:|---:|---:|
| in-proc (single-node default), 1 worker | 4 µs | 6 µs | ~137,000 |
| in-proc, 8 workers | 4 µs | 5 µs | ~1,017,000 |
| Postgres shared store, 1 worker | 0.45 ms | 0.67 ms | 2,074 |
| Postgres shared store, 8 workers (distinct tenants) | 0.71 ms | 0.95 ms | **11,058** aggregate |
| Postgres shared store, 8 workers hammering ONE identity | 2.14 ms | 16.3 ms | **2,341** on that identity |

Read this as: in HA mode (`OLIVARES_RATELIMIT_STORE=postgres`) every metered request pays one
~0.5–0.7 ms plpgsql round trip — well inside the 300 ms API p99 budget — and a single hot
identity (its aggregate bucket row serializes all its takes) still sustains **~2,300 takes/s**,
above the highest built-in ceiling (tier `system`, 2,000/s). This is the evidence basis for the
decision: the `Store` interface exists, but **the data does not justify a Redis
implementation** at v1 scale.

### 2-bis.4 Bus fan-out (unchanged magnitude)

7.20 M ev/s (1 subscriber), 3.05 M ev/s (4) — the bus remains ~3 orders of magnitude above the
durable-write ceiling. The NATS bridge does not sit on this local path (ADR-0017).

---

## 2-ter. Decision plane, retrieval plane & storage growth (2026-07-15)

**Provenance.** Same reference hardware (AMD Ryzen 7 5800U · 32 GiB · go1.26.4 · GOMAXPROCS 12); the
decision/retrieval store paths run on SQLite `:memory:`, so — like §2's fsync caveat — treat their latency
as a CPU-and-serialization **lower bound** versus durable disk. Produced by `task bench` over `core/bench`,
`cmd/olivares`, `modules/inferenceproxy` and `modules/knowledge`; re-run on your hardware.

### 2-ter.1 Decision plane — governed decisions/sec & p99

Every request crosses one of two governed decision paths: the Claude Code **hook PEP** and the
inline-inference **proxy PEP**. The in-memory policy *algebra* and the *end-to-end* governed decision
(bearer auth, policy read, kill-switch, and — on the proxy path — a signed audit-ledger append) are
measured separately.

| Decision path | decisions/sec | p50 | p95 | p99 | what it includes |
|---|---:|---:|---:|---:|---|
| Hook policy algebra (in-memory) | **173,000** | 2 µs | 3 µs | **4 µs** | pure path/subtree rule evaluation |
| Hook decision end-to-end | **4,820** | 0.19 ms | 0.27 ms | **0.38 ms** | + bearer auth (store read) + kill-switch read |
| Proxy DLP algebra (in-memory) | **219,000** | 1 µs | 1 µs | **2 µs** | pure DLP class decision |
| Proxy authorize end-to-end | **1,707** | 0.56 ms | 0.71 ms | **1.08 ms** | + auth + per-call policy read + **signed audit append** |

**Read this as:** the decision *algebra* is ~200k/sec — never the bottleneck (like the bus). A *full* governed
decision is bounded by its store I/O: ~4.8k/sec for the read-only hook path, ~1.7k/sec for the proxy path
that also writes a signed ledger entry per allowed call (the ~0.7 ms gap between them ≈ one signed audit
append, consistent with §2.1's ~1.2 ms append floor). Both sit far inside the API p99 < 300 ms SLO
(`docs/17-PRODUCTION-READINESS-SLO.md`). **Scope caveat:** the end-to-end figures use deterministic stubs
for the model-access, budget and PDP-overlay gates (each has its own cost but needs external or mutable
state); they measure the auth + policy + kill-switch + audit spine, not those pluggable gates.

### 2-ter.2 Retrieval plane (governed RAG) — corpus size vs latency

The built-in retriever is an **exact linear cosine scan** over the tenant's indexed chunks — no ANN by
default (`modules/knowledge`, `cosineIndex`). End-to-end `Query` (governed store read of every candidate +
decode + exact rank + lineage/audit write), local hash embedder:

| Corpus (chunks/tenant) | queries/sec | p50 | p99 | note |
|---:|---:|---:|---:|---|
| 10,000 | **10.7** | 93 ms | **101 ms** | comfortable |
| 100,000 | **1.06** | 949 ms | **968 ms** | the built-in ceiling |

Exact cosine ⇒ **recall@k = 1.0 by construction**. The ranker math alone is cheap (10k → 4 ms, 100k → 46 ms,
1M → 594 ms); at 100k the end-to-end ~1 s is dominated **not** by cosine but by loading + decoding all 100k
candidate rows from the store per query (~657 MB, ~7.1M allocations). That is the measured bottleneck, and it
is why **100,000 chunks/tenant is the supported ceiling** of the linear index (enforced — the store-backed
index refuses to exceed it). Beyond it, wire an **external vector backend** (`OLIVARES_VECTOR_BACKEND` =
pgvector/Qdrant/…) that pushes the search down and skips the full-scan load, trading exactness for latency.
Lead with the honest linear envelope; reach for ANN only when the corpus crosses this measured ceiling.

### 2-ter.3 Storage growth & retention sizing

| Write | on-disk bytes/event | GiB per million events |
|---|---:|---:|
| Signed, hash-chained audit append (SQLite) | **≈ 420** | **≈ 0.39** |

Committed on-disk growth per audit event — the WAL is checkpointed before measuring, so this is the stable
committed size (a signed, hash-chained row plus its index entries), not the fluctuating WAL sidecar.
**Retention sizing:** at a sustained governed write rate `R` events/sec over a window of `D` days, plan for
`R × 86,400 × D × 0.42 KiB` of ledger growth — e.g. 50 events/sec for 365 days ≈ **0.6 TiB** before
compaction/archival. Combine with the retention policy (GFS tiers, `docs/DR-RUNBOOK.md`) to size disk for
your window.

### 2-ter.4 Tenants per node & concurrent agents (bounded by design, not by a cap)

Tenants are **rows** under FORCE row-level security, not per-node processes, so there is **no fixed per-node
tenant cap**; the bound is aggregate write throughput plus per-tenant fixed storage. Provisioning a tenant
(id + org + audit genesis + default workspace) measures **~930/sec** (p99 2.2 ms); a per-tenant scoped read
costs the same whether spread across 100 distinct tenants or hammering one (**~10.4k/sec**, p99 0.14 ms) — the
RLS predicate adds **no per-tenant scaling penalty**. Likewise "concurrent agents" is not a separate cap:
sustained concurrent governed activity is bounded by the decision throughput above (~4.8k hook / ~1.7k proxy
governed decisions/sec per node) and the single-writer durable-write ceiling (§2.2) for the mutations those
decisions produce. Size by those measured rates, not by an agent count.

### 2-ter.5 Recovery time (RTO) is part of the envelope

A restore re-verifies the full ledger (chain + per-event signatures + checkpoints), so RTO scales **linearly**
with ledger size — measured **≈123 ms at 500 events → ≈1.26 s at 20,000 events** (`docs/DR-RUNBOOK.md`, drill
log). Objective tiers: **< 15 min** (SQLite single-node, cron backups) / **< 30 min** (Postgres logical/PITR).
Size the restore window from your ledger size and this linear verify curve.

### 2-ter.6 Content sync memory is bounded by the KB, not the upstream corpus

Full-list reconciliation **streams** the content source page by page rather than materializing its whole
corpus: peak memory is **O(one page + the KB's own document set)**, not O(source corpus). A source that
declares the bounded-pagination capability (`contentsource.PagedSource`) is asked for pages with explicit
item/byte ceilings (`syncListMaxItems=1000`, `syncListMaxBytes=8 MiB`), so a multi-million-document
SharePoint/Drive/filesystem source cannot balloon host RAM during a sync; a source without it uses its
ordinary paginated `List` (already page-bounded for in-tree connectors). Orphan detection is preserved
exactly — the delete set is the DB-of-this-source minus the refs seen while streaming, computed without ever
holding the full upstream ref set — and a cancelled context cuts the paging immediately. The KB itself remains
bound by the retrieval ceiling in §2-ter.2 (the 100,000-chunk/tenant enforced cap).

---

## 3. The SQLite→Postgres threshold

Both backends are first-class today (selected by `--engine sqlite|postgres` + `--dsn`; `core/store/config.go`). Decide by **write demand and topology**, not data size:

**Stay on SQLite (the embedded default) when ALL hold:**
- Single node (no HA requirement — `replicaCount > 1` is unsupported on SQLite anyway).
- Sustained write rate comfortably below the single-writer knee (reference: ~1k/s on RAM, **lower on disk** — measure yours).
- Few concurrent writers (concurrency degrades SQLite, §2.2).
- Air-gapped / embedded / self-serve where operational simplicity (one file, no DB to run) outweighs scale.

**Move to Postgres when ANY holds:**
- Sustained writes approach or exceed the single-writer knee on **your** storage (re-run §1 — fsync on durable disk lowers the knee, so the crossover comes sooner than the RAM baseline suggests).
- You have many concurrent writers (Postgres has a real pool + MVCC and scales with `MaxConns`/cores; SQLite does the opposite).
- You need **HA / replicas / failover** — `replicaCount > 1` is supported **only** on Postgres, with FORCE row-level-security per tenant (already implemented: `core/internal/store/dialect`, `deploy/postgres/01-app-role.sql`).
- You need horizontal read scaling or multi-host.

**How to find your own crossover:** run `OLIVARES_TEST_POSTGRES_DSN=… task bench` on the target. SQLite's `writers=1/4/16` line stays flat-or-falling; Postgres climbs with writers. The crossover is the writer-count where Postgres aggregate throughput overtakes SQLite's flat ceiling. On the reference hardware that crossover measured at **1–2 concurrent writers** (§2-bis.2, 2026-06-12) — re-run on your storage class; networked block storage moves it.

---

## 4. Node sizing (single node, self-hosted tier)

The constraint is the **single-writer commit path**, not CPU or RAM (the bus and runtime are far from saturated at the write ceiling). Practical guidance:

| Profile | Sustained ingest | Backend | Node (reference) | Notes |
|---|---|---|---|---|
| Self-serve / dev | < ~200 writes/s | SQLite | 2 vCPU / 4 GiB / fast SSD | embedded, simplest |
| Single-node prod | up to the measured knee (~1k/s on disk — verify) | SQLite | 4 vCPU / 8 GiB / **NVMe** (fsync-bound) | storage is the lever; prefer local NVMe |
| Scale / HA | above the knee, or any HA need | **Postgres** | engine 4 vCPU / 8 GiB + managed PG | leader-election + standby; size PG to write load |

**fsync dominates write sizing.** Because each commit fsyncs the WAL, the single most impactful sizing choice is the **storage class**: local NVMe ≫ networked block ≫ slow disk. CPU/RAM headroom is large; do not over-provision compute to fix a write-throughput problem that is storage- or backend-bound.

`olivares_http_requests_in_flight`, `go_goroutines`, and `go_memstats_*` on `/metrics` are the headroom signals; alert on the write-latency SLO ([17-PRODUCTION-READINESS-SLO.md](17-PRODUCTION-READINESS-SLO.md)) and the saturation of the single writer (busy_timeout tail → the collector-backpressure runbook).

---

## 5. References
- Harness: `core/bench/` (`capacity_test.go`, `storage_growth_test.go`, `tenant_cost_test.go`, `ratelimit_test.go`), `cmd/olivares/decision_e2e_bench_test.go`, `modules/inferenceproxy/policy_bench_test.go`, `modules/knowledge/retrieval_bench_test.go`; `scripts/bench-capacity.sh`, `task bench`.
- Store internals: `core/internal/store/sqlstore/store.go` (single-connection cap, WAL pragmas), `core/store/config.go` (engine/DSN), `deploy/postgres/`.
- SLOs that consume these numbers: `docs/17-PRODUCTION-READINESS-SLO.md`. Backpressure operations: `deploy/runbooks/collector-backpressure.md`.
- Method: [Google SRE — Implementing SLOs](https://sre.google/workbook/implementing-slos/).

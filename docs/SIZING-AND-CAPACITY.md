<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Capacity, sizing & the SQLite→Postgres threshold

**Date:** 2026-06-09 (initial baseline; later measurements are dated below).
**Support classification updated:** 2026-09-27. Historical workload observations and
planning examples; current release capacity is not qualified by these numbers.
The [v26.10 deployment support matrix](../deploy/support-matrix.md) is the canonical
qualification record and supersedes this guide's former support classification.

> This guide preserves recorded benchmarks for specific write, decision, retrieval
> and storage workloads, plus separate DR observations and planning examples.
> The benchmark harness spans `core/bench/`, `cmd/olivares/`,
> `modules/inferenceproxy/` and `modules/knowledge/` (`task bench` →
> `scripts/bench-capacity.sh`). Rerunning it measures the current artifact and host;
> it does not promise the same results. Read each section's hardware, date, storage
> and fixture limits. Missing artifact or workload fields are **not recorded**,
> not inherited from a different section. The initial write baseline used tmpfs;
> §2-bis records a separate durable-disk run, and §4 contains unqualified sizing
> hypotheses rather than measured resource minima.

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
- `BenchmarkBusFanout` — the in-process bus alone (store-free): isolates fan-out cost from store writes.

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

**Read this as:** this historical SQLite workload on RAM-backed storage recorded
**~1.5k writes/sec**, **sub-ms p50 and ~1.2 ms p99**. It does not establish durable
storage performance or whole-node capacity. The ingest target (< 250 ms,
[17-PRODUCTION-READINESS-SLO.md §2](17-PRODUCTION-READINESS-SLO.md)) is a separate
end-to-end objective: store-write latency alone does not measure ingestion,
subscriber backpressure or the complete workload.

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

The historical store-free bus sample recorded **~3.8M events/sec** — ~2,500× the separate write-path sample. That comparison identifies the cost of those measured paths; it does not establish CPU headroom or the limiting component of every deployment. (The bus applies bounded, blocking backpressure: a slow subscriber's full 256-deep queue blocks the publisher rather than dropping events — `core/eventbus/inproc.go`.)

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
**Observed at the sampled points:** throughput was similar at one writer, and
Postgres was higher at four and sixteen. The earlier **1–2 writer** crossover
estimate was an inference; two writers were not a sampled row. Single-writer latency is a wash (Postgres p99
is tighter; its audit-append throughput is ~13% lower per the chained-row round trips).

### 2-bis.3 Rate-limit admission cost (shared store vs in-proc)

| Path | p50 | p99 | takes/s |
|---|---:|---:|---:|
| in-proc (single-node default), 1 worker | 4 µs | 6 µs | ~137,000 |
| in-proc, 8 workers | 4 µs | 5 µs | ~1,017,000 |
| Postgres shared store, 1 worker | 0.45 ms | 0.67 ms | 2,074 |
| Postgres shared store, 8 workers (distinct tenants) | 0.71 ms | 0.95 ms | **11,058** aggregate |
| Postgres shared store, 8 workers hammering ONE identity | 2.14 ms | 16.3 ms | **2,341** on that identity |

Read this as: the PostgreSQL rate-limit-store benchmark
(`OLIVARES_RATELIMIT_STORE=postgres`) recorded ~0.5–0.7 ms for the single-worker
round trip, compared with the separate 300 ms API p99 target. The shared store is
not itself an HA qualification. The sampled hot identity (its aggregate bucket row
serializes all its takes) recorded **~2,300 takes/s**,
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

The measurements cover two governed decision paths: the Claude Code **hook PEP**
and the inline-inference **proxy PEP**. The in-memory policy *algebra* and the *end-to-end* governed decision
(bearer auth, policy read, kill-switch, and — on the proxy path — a signed audit-ledger append) are
measured separately.

| Decision path | decisions/sec | p50 | p95 | p99 | what it includes |
|---|---:|---:|---:|---:|---|
| Hook policy algebra (in-memory) | **173,000** | 2 µs | 3 µs | **4 µs** | pure path/subtree rule evaluation |
| Hook decision end-to-end | **4,820** | 0.19 ms | 0.27 ms | **0.38 ms** | + bearer auth (store read) + kill-switch read |
| Proxy DLP algebra (in-memory) | **219,000** | 1 µs | 1 µs | **2 µs** | pure DLP class decision |
| Proxy authorize end-to-end | **1,707** | 0.56 ms | 0.71 ms | **1.08 ms** | + auth + per-call policy read + **signed audit append** |

**Read this as:** the historical algebra samples were ~200k/sec; the measured
hook and proxy authorization paths were ~4.8k/sec and ~1.7k/sec respectively.
Their measured latencies were below the separate API p99 < 300 ms target
(`docs/17-PRODUCTION-READINESS-SLO.md`). These are not whole-session, provider
streaming or settlement rates. **Scope caveat:** the end-to-end figures use deterministic stubs
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
is the basis of the **100,000 chunks per knowledge base limit** of the store-backed
linear index (`maxChunksPerKB`, enforced during ingest). The historical benchmark
used one knowledge base in one tenant and reported `chunks/tenant`; it does not
establish a tenant-wide total limit. This implementation bound is not a qualified
throughput or latency commitment for every deployment. Beyond it, wire an **external vector backend** (`OLIVARES_VECTOR_BACKEND` =
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
sample did not show an additional per-tenant cost between those two cases. It does
not establish behavior for arbitrary tenant counts. There is no separate agent-count
capacity result here: the historical ~4.8k hook / ~1.7k proxy decisions/sec samples
omit full provider execution. Size the complete concurrent workload, including its
writes, from a new measurement of the intended deployment.

### 2-ter.5 Recovery time (RTO) is part of the envelope

The historical SQLite drill recorded **123 ms at 500 events → 1.257 s at 20,000
events**, including restore, boot and ledger verification. Its host was the
reference build container on 2026-07-15: Linux 7.0.14-arch1-1, Go 1.26.4, 16 vCPU
([DR runbook](DR-RUNBOOK.md), [day-2 drill log](DAY2-DRILL-LOG.md)). The approximately
linear trend applies to that measured range, not an arbitrary estate or a complete
isolated rebuild. The drill uses an ephemeral in-memory passphrase; it does not
qualify operational key custody or reconciliation of effects after the restore point.
The **< 15 min** SQLite and **< 30 min** Postgres logical/PITR figures are targets,
not observed release-wide RTO or customer commitments. RPO needs the data classes
and observed loss window. Measure the intended deployment's complete usable-state
recovery, including keys, authority and external-effect reconciliation.

### 2-ter.6 Content sync memory is bounded by the KB, not the upstream corpus

Full-list reconciliation **streams** the content source page by page rather than materializing its whole
corpus: peak memory is **O(one page + the KB's own document set)**, not O(source corpus). A source that
declares the bounded-pagination capability (`contentsource.PagedSource`) is asked for pages with explicit
item/byte ceilings (`syncListMaxItems=1000`, `syncListMaxBytes=8 MiB`), so a multi-million-document
SharePoint/Drive/filesystem source cannot balloon host RAM during a sync; a source without it uses its
ordinary paginated `List` (already page-bounded for in-tree connectors). Orphan detection is preserved
exactly — the delete set is the DB-of-this-source minus the refs seen while streaming, computed without ever
holding the full upstream ref set — and a cancelled context cuts the paging immediately. The KB itself remains
bound by the retrieval ceiling in §2-ter.2 (the 100,000-chunk per knowledge base enforced cap).

---

## 3. The SQLite→Postgres threshold

Both backends are first-class today (selected by `--engine sqlite|postgres` + `--dsn`; `core/store/config.go`). Decide by **write demand and topology**, not data size:

**Stay on SQLite (the embedded default) when ALL hold:**
- Single node (no HA requirement — `replicaCount > 1` is unsupported on SQLite anyway).
- Sustained write rate below the knee measured on your storage and workload; the historical ~1k/s planning reference is not a supported capacity limit.
- Few concurrent writers (concurrency degrades SQLite, §2.2).
- Air-gapped / embedded / self-serve where operational simplicity (one file, no DB to run) outweighs scale.

**Move to Postgres when ANY holds:**
- Sustained writes approach or exceed the measured single-writer knee on **your** storage (re-run §1; the recorded NVMe result and tmpfs result were similar, so do not assume every disk shifts the knee by the same amount).
- You have many concurrent writers (Postgres has a real pool + MVCC and scales with `MaxConns`/cores; SQLite does the opposite).
- You are qualifying **HA / replicas / failover**: the chart permits `core.replicaCount > 1` only with `core.engine=postgres` and a shared signing key. PostgreSQL and its tenant isolation are prerequisites, not proof of HA; use the [support matrix](../deploy/support-matrix.md) and the declared fault/recovery oracle.
- You need horizontal read scaling or multi-host.

**How to find your own crossover:** run `OLIVARES_TEST_POSTGRES_DSN=… task bench` on the target. SQLite's `writers=1/4/16` line stays flat-or-falling; Postgres climbs with writers. The crossover is the writer-count where Postgres aggregate throughput overtakes SQLite's flat ceiling. The reference samples used **1, 4 and 16 writers** (§2-bis.2, 2026-06-12). The historical **1–2 writer** estimate was not directly sampled; measure the crossover on your storage class and workload.

---

## 4. Node sizing (single node, self-hosted tier)

The examples below are **historical planning hypotheses**, not benchmarked
resource minima, qualified profiles or customer capacity commitments. The reference
benchmark host had 32 GiB RAM; the 4/8 GiB suggestions were not that measured tuple.
Measure CPU, memory, storage, concurrency and provider/model resources for the
actual Module set and workload before selecting a node. Single-node production
remains an intended topology when qualified.

| Planning case | Workload hypothesis to measure | Backend | Unqualified resource estimate | Limits |
|---|---|---|---|---|
| Self-serve / dev | < ~200 writes/s | SQLite | 2 vCPU / 4 GiB / fast SSD | embedded, simplest |
| Single-node prod | historical ~1k/s planning assumption; measure the actual knee | SQLite | 4 vCPU / 8 GiB / **NVMe** (fsync-bound) | qualify the storage and complete workload |
| Scale / HA | above the knee, or a declared HA requirement | **Postgres** | engine 4 vCPU / 8 GiB + managed PG | size the full workload; leader election and standby alone do not qualify HA |

**Measure storage and compute together.** Commit latency depends on the selected
durability settings and storage class. The historical samples do not establish a
universal ordering of local NVMe, networked block and other disks, or spare CPU/RAM
for an unmeasured workload. Increasing RAM does not replace admission bounds;
retain headroom for model runtimes, subprocesses, queues and disk growth.

`olivares_http_requests_in_flight`, `go_goroutines`, and `go_memstats_*` on `/metrics` are the headroom signals; alert on the write-latency SLO ([17-PRODUCTION-READINESS-SLO.md](17-PRODUCTION-READINESS-SLO.md)) and the saturation of the single writer (busy_timeout tail → the collector-backpressure runbook).

---

## 5. References
- Harness: `core/bench/` (`capacity_test.go`, `storage_growth_test.go`, `tenant_cost_test.go`, `ratelimit_test.go`), `cmd/olivares/decision_e2e_bench_test.go`, `modules/inferenceproxy/policy_bench_test.go`, `modules/knowledge/retrieval_bench_test.go`; `scripts/bench-capacity.sh`, `task bench`.
- Store internals: `core/internal/store/sqlstore/store.go` (single-connection cap, WAL pragmas), `core/store/config.go` (engine/DSN), `deploy/postgres/`.
- SLOs that consume these numbers: `docs/17-PRODUCTION-READINESS-SLO.md`. Backpressure operations: `deploy/runbooks/collector-backpressure.md`.
- Method: [Google SRE — Implementing SLOs](https://sre.google/workbook/implementing-slos/).

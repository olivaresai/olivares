<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0025: FinOps reserve→commit/release ledger closes the budget/spend-limit TOCTOU

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## Context and problem statement

`finops.CheckBudget` and `finops.CheckSpendLimit` are read-only pre-flight admission checks: they aggregate the cost read-model and answer "is this request within the enforcing budgets/limits that scope it?". Between that answer and the moment the actual spend is written back (the connector's `CostSampled` → `onCost` ingest), there is a window. **N concurrent requests all read the same pre-spend state, all pass, and collectively blow the limit** — a check→act (TOCTOU) double-spend. An earlier fail-closed hardening pass closed the `Truncated` degradation and the availability posture, but the race itself stayed open.

A correct fix has to make "check the ceiling, then consume the headroom" **atomic**, and it has to be atomic **across replicas on Postgres**, not just within one process — so a process-level mutex is not acceptable.

## Decision drivers

- **The ceiling must be consumed at admission, not at settlement.** The only way N concurrent requests cannot all pass is if each admission durably subtracts its own headroom before the next one reads.
- **Cross-store, one contract.** The same mechanism must hold on SQLite (embedded, single writer) and Postgres HA (multiple connections, READ COMMITTED). Use the store's own atomicity primitives, never an in-memory lock.
- **Actual cost is only known a-posteriori.** Output tokens (hence cost) are unknown before the call. Admission must reserve an *estimate* and reconcile on completion.
- **Honest expiry.** A crashed caller must not hold headroom forever, and reclaiming it must never double-count.
- **No new schema engine.** Reuse the module `ExtensionRegistry` descriptor + the generic repo's optimistic concurrency.

## Decision outcome

A **dynamic reserve ledger** (`finops.budget_reservation`, table `finops_budget_reservation`) with a reserve→commit/release lifecycle. `ReserveBudget` / `ReserveSpendLimit` atomically reserve the estimate against every enforcing policy that scopes the request; `CommitReservation` settles it with the actual cost; `ReleaseReservation` returns the headroom on failure. The ceiling everywhere (`CheckBudget`, `budgetStatus`, `evaluateBudgets`) is now `committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`.

This is **distinct from** the pre-existing **static** `budgetSpec.ReservedMicroUSD` (a Priority-Tier capacity commitment counted toward the limit). Both are summed into `effective`; this ADR adds the *dynamic, per-request* line.

### 1. Atomicity: a monotonic per-scope `seq` under a UNIQUE index (no process lock)

Each reservation carries a `seq` that is monotonic per **(policy, period_start, scope_key)**, under the UNIQUE index `finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)`. Reserve = read `max(seq)`, read current spend + active reservations, and if there is room, `INSERT` with `seq = max+1`.

- Two concurrent reservers compute the **same** next `seq`; the UNIQUE index lets exactly **one** `INSERT` commit and maps the other to `store.ErrConflict` (`mapWriteErr`). The loser **retries the whole transaction** and re-reads the now-committed state. This serializes reserve-check-insert **without any process lock**.
- **SQLite:** `MaxOpenConns=1` already serializes every transaction on the single writer, so the reserve is atomic on its own; the seq index is the belt-and-suspenders backstop.
- **Postgres READ COMMITTED (the load-bearing case):** separate connections do not see each other's uncommitted rows, so the seq collision is what forces the retry. **Ordering invariant:** the reserve reads `max(seq)` **before** the reserved sum and inserts with *that* seq — so a successful insert (no collision) proves the seq we read was the true committed max, hence the sum (read strictly later) saw every prior reservation. Reversing the two reads would reopen the race (a stale sum paired with a fresh non-colliding seq would over-admit). Proven by induction: the k-th successful insert saw all k-1 prior reservations, so exactly `floor(headroom/estimate)` admit.

Multi-policy requests reserve every target in **one** transaction (all-or-nothing): a later target's denial rolls back the earlier inserts; block outranks throttle.

### 2. Granularity of reservation — per enforcing policy, keyed by scope

One reservation **row per enforcing policy the request matches**, keyed by `(policy_ref, period_start, scope_key)`:

- **Budgets:** `scope_key` = the budget's dimension key (`""` for global) — one scope per policy. Reserved across all 17 non-group dimensions the request matches (the common per-request case: model/provider/agent/workspace/identity/api_key/…).
- **Per-seat spend limits:** `scope_key` = the **actor**, so a cap sourced from an org/group policy reserves each seat's headroom **independently** — matching `CheckSpendLimit`'s per-actor semantics.
- **Group-dimension budgets (`user_group`/`agent_group`) are NOT reserved here.** Their spend is a member fan-out over `actor`/`agent_ref` with no read-model group column; a fan-out reservation is a larger design. They remain enforced by `CheckBudget`'s existing preventive path. (Open follow-up — see below.)

### 3. Estimation — reserve an estimate, reconcile on commit

Admission reserves `estimateMicroUSD` (the seam's a-priori estimate — e.g. from `count_tokens` on the prompt plus a `max_tokens` output allowance). On completion `CommitReservation(handle, actualMicroUSD)` stamps the actual and flips the row to `committed`, which removes it from the active sum; the real spend lands separately via `onCost`. If the estimate was **too low**, the budget can transiently exceed by `actual − estimate` for that one request — bounded and self-correcting once the actual spend is recorded. **The default estimate policy is a product decision (see below); the mechanism is estimate-agnostic.**

**Ordering:** ingest the actual spend, *then* commit the reservation, so the ceiling never transiently under-counts during settlement.

### 4. Expiry — a predicate, never a decrement

The active-reserved sum filters `state = active AND expires_at > now`. An expired reservation therefore **stops counting the instant it lapses** — there is no counter to decrement, so **double-counting is structurally impossible**. `SweepExpiredReservations` only stamps the terminal `expired` state for observability/GC; correctness does not depend on it running. TTL (`reservationTTL`, default **5 min**) is the crash backstop for a caller that died between reserve and commit/release; it must exceed the slowest governed actuation so a still-running request is never dropped.

### Consequences

- **Positive:** the double-spend is closed atomically on both engines; the fix is additive (a new descriptor table — `applyModuleTables` creates it on fresh and in-place DBs; no existing migration touched); `CheckBudget`/status/alerts now reflect in-flight reservations, so pre-flight denial, hard-cap signal and the status DTO agree.
- **Cost:** a reserve is two writes (reserve + settle) vs a read-only check; on the hot path this is a few extra small transactions dwarfed by the inference call it guards.
- **Latent until wired:** the ledger only bites once the actuation seams call `ReserveBudget`/`Commit`/`Release` (with an estimate) instead of the read-only `CheckBudget`. Until then dynamic-reserved is 0 and behavior is unchanged. Wiring the inference proxy / HITL gate + choosing the default estimate is the remaining integration.

## Open questions (product)

1. **Default estimate.** What is the a-priori estimate when the seam has none? Options: `count_tokens(prompt)` + configured `max_tokens` output allowance at the model's rate; a flat per-request floor; or per-model p95 historical cost. Under-estimating weakens the guarantee; over-estimating throttles early.
2. **TTL.** Is 5 min the right crash backstop, or should it track the model's max completion time / be per-surface?
3. **Group-budget reservation.** Should `user_group`/`agent_group` budgets also be reserved (member fan-out), or is preventive-only enforcement acceptable for group ceilings?
4. **Retry-exhaustion posture.** On `maxReserveRetries` (64) exhaustion the reserve fails **open** (per `CheckBudget`'s contract). For a hard `block` budget, should extreme contention instead fail **closed**?

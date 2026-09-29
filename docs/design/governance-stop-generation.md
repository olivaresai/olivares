# Transactional stop observations

Governance owns a durable generation for each tenant's kill-switch posture.
It detects a new engage followed by a reenable even when two observations both
show a clear estate. It does not grant permission or attest that work committed.

`Module.LockKillSwitchState(ctx, scope)` takes the caller's actual `store.Scope`
and returns an opaque `KillSwitchSnapshot`. `Tenant()` and `Generation()` expose
metadata; `State()` returns a defensive copy of the live stop posture. The call
requires a writable transaction and `store.TransactionLocker`. It opens no
second transaction and adds no external call, clock or retry. The existing
`KillSwitchState` remains a live, observation-only View.

The caller locks directory and user authority first, then the governance barrier,
then its target rows. The barrier key is `governance.killswitch:<tenant UUID>`.
PostgreSQL holds a transaction advisory lock; its Mutate envelope uses READ
COMMITTED, so discovery after waiting sees the preceding commit. SQLite's Mutate
writer serialization supplies the same exclusion. The caller supplies a bounded
context and discards the snapshot if its transaction fails or commit is unknown.
Retaining a snapshot after the callback never turns it into a commit receipt.

## Durable domain

The module descriptor `governance.killswitch_generation` adds one mutable table,
`governance_killswitch_generation`, through normal extension registration. A
UNIQUE index on tenant_id provides one row per tenant. The engine supplies id,
tenant, version and timestamps; generic Update checks the version by CAS. Core
migration ordinals and historical hashes do not change.

The first valid transaction under the barrier creates generation1. This starts a
new domain, including on upgraded stores; it does not reconstruct earlier cycles.
The observer also reads actual current stop rows, so bootstrap never implies
there were no stops. No supported route deletes or resets the generation row.
Direct owner-database edits are outside this writer protocol. Invalid tenant,
missing capability, read-only scope, malformed singleton or lock/read failure
refuses the observation. Overflow refuses a transition without changing posture.

Every new engage and active-to-reenabled transition increments once in the same
transaction as the stop and its existing audit. The first new engage therefore
commits generation2. Idempotent engage, pending/refused reenable and post-review
metadata do not increment. Rollback reverts generation and stop together.

## Writer order

| Writer | Order inside Mutate |
| --- | --- |
| Operator engage | barrier, generation, active lookup, approvals, stop and audit |
| Guardian stop auto/approval | barrier before action/approval access; new stop bumps |
| Guardian approved stop | barrier, stored arm revalidation, approval, stop, action |
| Tier floor | agent read, barrier before signal writes, count, optional new stop |
| Reenable | barrier, generation, stop/approval, existing quorum and review checks, bump |
| Review | barrier, stop and separation check, metadata and audit; no bump |

Guardian quarantine is an identity arm and never takes the governance barrier.
Discovery chooses only the lock order; the approved sweep rereads and compares
the stored action before containment. A changed arm is refused without acquiring
locks in reverse order. Approval decisions and guardian rule deletion retain
their existing transactions and never request the governance barrier afterward.
Existing stop scope matching, dual control, audit contents and post-commit emits
remain the source of behavior. Presentation, hardware and transport do not change.

SQLite and PostgreSQL tests cover the real HTTP cycle, writer ordering, changed
arms, rollback, corruption, bootstrap, additive upgrade and contention. They
require the repository's CI route; an unavailable PostgreSQL leg is not coverage.

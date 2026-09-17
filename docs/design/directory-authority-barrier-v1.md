<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Ordinary mutation directory and complete-authority admission (DAB1) v1

Status: implemented Store capability, September 2026. Applies to SDD 01, 02, 03 and
06 and COCKPIT-01/02/04. This is an additive, OPTIONAL Store capability on the
ordinary business scope. It is not a consumer protocol, an authorization decision,
managed Stop, or an answer to the dropped-audit accounting branch.

## Why it exists

A human-authorized **business** mutation can pin SYSTEM User authority (H) and
later append its tenant audit event. Every supported `AuthMutate` writer acquires
the global directory lock **before** either User authority or audit —
`authAuditLog.Append` and `LockAppends` both call `globalFirst`, and
`Users().Update` goes through the directory-tracked repository — and with global
spool budgeting enabled an audit append also takes a single shared accounting row
(`WHERE id = 1 FOR UPDATE`).

A new business caller that locked H first and only then appended audit could
therefore cycle with an auth writer that had already taken the spool row and was
waiting for H. The ordinary `Mutate` lineage prelude serializes **same-tenant**
business writers; it does not serialize a SYSTEM `AuthMutate` against that
business tenant.

DAB1 is the one Store-owned entrypoint that takes global directory admission and
pins the complete supplied authority for the surrounding ordinary transaction's
own tenant, so a business caller enters the same global-first order every
supported auth writer already uses.

The alternative — letting a caller spell the store's internal advisory key through
`store.TransactionLocker` — is explicitly not the design: the key is internal, and
PostgreSQL advisory transaction locks are re-entrant, so spelling it produces
untracked exclusion that `acquireDirectoryWriter` would re-take without noticing.

## Interface

```go
// core/store/authority_snapshot.go
type DirectoryAuthoritySnapshotLocker interface {
    LockDirectoryAuthoritySnapshot(context.Context, AuthoritySnapshotBundle) error
}
```

There is **no tenant parameter**: the enclosing ordinary `Mutate` already selected
the tenant, and the barrier refuses to act for any other one. The concrete
`*tenantScope` implements it, and that same concrete type backs every `View` and
the SYSTEM-tenant `Mutate` that `AuthMutate` builds its auth view over — so
against this Store the assertion **always** succeeds and both refusals are runtime
ones. Absence is meaningful only for a non-SQL implementation that does not
provide the method. A consumer must therefore handle **both** absence and runtime
refusal explicitly, and must never substitute `AuthoritySnapshotBundleLocker`,
which takes no directory admission.

`authScope` and `custodyScope` hold their inner scope in a **named field**, so
neither promotes the method and neither may declare it: a comma-ok that answered
true from a SYSTEM or evidence-only context would be a capability overclaim.

## Workspace confinement: one port-attachment step

Confinement forwards this operation because it accepts opaque version references
and returns no rows; the repositories themselves stay confined, and no journal
access or raw unwrapping is added.

The attachment is a **single final step**, and that is load-bearing.
`confineWorkspace` builds one of **32 named** concrete decorators from the observed
clock/locker/authority/directory/authorization-epoch combination. Attaching a port
re-wraps that named decorator in an **anonymous** struct type, which is no longer
one of those names. A second attachment step chained after the first would reach
its own switch with a type it cannot match and would answer
`ErrWorkspaceConfinement` for **every** confined SQL scope — the existing bundle
wrapper already consumed that composable position. The obvious repair, an outer
wrapper embedding the `Scope` interface, is worse: embedding an interface promotes
only `Scope`'s own methods and silently drops clock, locker, authority, directory
and authorization-epoch capabilities.

So `ConfineWorkspace` selects the port set from the **raw** capabilities —
bundle only, directory-authority only, both, or neither — and
`forwardWorkspaceAuthorityPorts` attaches exactly that set to the named decorator
with exactly one switch. The three sets are three distinct struct shapes on
purpose: optionality is a compile-time property of the returned type, so a raw
scope carrying only one port never gains the other.

The "already forwarded" guard is **per port**. Same-workspace re-confinement is
idempotent, and a guard that returned early as soon as *any* selected port was
present would silently drop the other one on exactly that path. The existing scope
is returned only when **every** selected port is already exposed. A different
workspace remains a refusal, and an unknown concrete decorator still fails with
`ErrWorkspaceConfinement` rather than inventing capabilities.

## Transaction algorithm

One original ordinary `Mutate` `*sql.Tx` owns every step; its leadership
admission, PostgreSQL L0/L1 gates, SQLite writer reservation, fixed tenant,
lineage prelude, clock and commit envelope are unchanged. No authority or row
payload escapes.

1. **Entry state, then order, then input — in that order, before any lock.**
   - *Stage 0*: a read-only scope returns `ErrReadOnly`; an existing binding poison
     or an already-poisoned tracker is returned as-is.
   - *Stage 1, entry state*: the bound tenant must be a valid canonical non-zero
     non-SYSTEM tenant (checked with the directory writer's own
     `canonicalDirectoryTenants`, so the refusal cannot drift), the original
     directory tracker must exist with its permanent presentation equal to that
     tenant, and **the ACTUAL SQL presentation is read with
     `readUserAuthorityPresentation` and required to equal it**. This must precede
     admission: `directoryWriteTracker.prepare` rebinds the transaction to the
     writer's permanent presentation before arming its generation, so a scope whose
     logical field says tenant T while its real presentation is another partition
     would otherwise be silently repaired by admission and pass every later check.
     A presentation that is valid on *exit* proves cleanup, never valid entry.
     `readAuthTenantEntryPresentation` is **not** reused: it hard-requires SYSTEM,
     the opposite of this barrier's entry state.
   - *Stage 2, order*: repeated acquisition (`authorityLocked`), an already-used
     directory writer (`t.locked`), recorded `auditBeforeDirectory` and
     `authorityBeforeDirectory`.
   - *Stage 3, input*: the complete existing 1..64 tenant-fact grammar against the
     bound tenant — duplicate, epoch-to-tenant, allowlist, lease and fence rules —
     plus 0..64 supplied User references before equal deduplication, where
     conflicting versions refuse. Caller-owned slices are copied **before**
     validation, so a later mutation of the caller's slice cannot change the
     question that was actually checked and locked.

   The Stage-1 check is required **even for a token-shaped bundle with zero User
   references**: the complete bundle re-verifies the presentation only inside
   `withUserAuthorityBinding`, which that shape never reaches.
2. **Global admission first.** The existing `directoryWriteTracker.prepare` runs
   with a discovery callback that reports **no affected tenants**, so the global
   directory writer lock is acquired and nothing reaches `bumpDirectoryEpochExact`.
   Inside that callback the supplied User IDs are reserved in canonical order
   through the existing `lockUserAuthorities` and its held-user bookkeeping. An
   absent H is refused there; where the staged legacy writer may reserve absence
   instead, the exact bundle comparison below still refuses it, because it compares
   real versions and a reserved absence has none. No User version, directory epoch
   or directory source row is bumped by admission.
3. **Existing generation preserved.** `prepare` restores this tenant's permanent
   presentation and re-arms the existing writer-generation proof. That exact
   generation and tracker stay in force; there is no new marker protocol and no
   second directory writer.
4. **The unchanged complete bundle.** `LockAuthoritySnapshotBundle` is called on
   the same scope. It re-checks every supplied User version and applies the full
   canonical tenant-fact algorithm — allowlist, canonical order, the identity-table
   predicate barrier and the required leased-fact OCC touches. Nothing is dropped,
   synthesized, reordered or duplicated. Unlike ATA1 there is **no borrowed
   business-tenant callback**: this scope's logical tenant and permanent
   presentation already are the business tenant. Because the tracker is already
   locked, the bundle cannot record `authorityBeforeDirectory`.

   "No source bump" does not omit the bundle's mandatory leased-fact touches.
5. **After success** the directory lock, the canonical H rows and the tenant
   authority rows stay owned until the transaction completes. The existing finish
   path rebinds the permanent business presentation before removing its generation
   proof and verifying the empty marker/GUC baseline.

Lock order: existing ordinary lineage prelude → directory global → canonical H →
canonical complete tenant facts → product row → tenant audit and enabled global
spool. No SQL lock survives the transaction, and none spans provider, process or
network I/O.

## Caller obligations

The consumer obtains an MA1 mutation value for its complete original-principal
action/target question **before** entering the transaction. Inside ordinary
`Mutate` it validates `AuthorityFor` against `TransactionNow`, calls this
capability, then validates the same value and question against `TransactionNow`
again. No policy or network evaluation runs while locked, and a stale decision is
not retried under the old value.

This capability is the **first lock-bearing operation** in the callback after the
Store-owned prelude. The enforceable refusal state is exactly: `readOnly`,
original binding poison, missing or poisoned directory tracker, `authorityLocked`,
`tracker.locked`, `auditBeforeDirectory` and `authorityBeforeDirectory`.

Two prohibitions are **caller obligations the Store cannot enforce**, and they are
labelled as such rather than implied:

- a preceding raw repository row lock leaves no tracker state;
- `store.TransactionLocker.LockTransaction` is a public, confinement-forwarded,
  lock-bearing operation with a caller-chosen key that also leaves none, and
  because PostgreSQL advisory transaction locks are re-entrant, a caller that
  spells the internal directory key through it produces no self-deadlock and no
  refusal — only untracked exclusion.

Contract tests must establish the actual consumer order when P2 is implemented. No
P2 caller is added in DAB1.

## Error semantics

| Condition | Result | Poisons? |
|---|---|---|
| `View` invocation | `store.ErrReadOnly` | no |
| SYSTEM / zero / non-canonical bound tenant | existing canonical-tenant refusal | **yes** |
| Missing directory tracker | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Tracker presentation ≠ bound tenant | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Unreadable actual entry presentation | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Actual entry presentation ≠ bound tenant | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Pre-existing tracker poison | the earlier cause, preserved | already poisoned |
| Repeated acquisition, prior authority | `errDirectoryAuthorityRepeated` | no |
| Prior directory use, audit-before-directory | `errDirectoryAuthorityOrder` | no |
| Malformed, duplicate, cross-tenant, unsupported, over-budget, lease-mismatched, empty facts; conflicting or SYSTEM-shaped H | existing precise grammar refusal, no source write | no |
| Stale H or stale tenant fact | `store.ErrConflict`, preserved through the envelope | yes |
| Missing H or missing fact | existing precise refusal (shapes differ by path and are retained) | yes |
| Canceled or elapsed deadline | the actual context/store failure | yes |

There are two dividing lines, not one.

**Entry state versus everything else.** A defect in the transaction's own entry
state poisons the original transaction, because it says this transaction is not
the thing the capability was asked to act on. It must not be able to commit at
all, including when the callback discards the returned error. It is checked before
anything that could normalize it, and performs no write.

**Admission versus ordinary refusal.** Order and input defects, checked *after*
the presentation is established, return a precise error with no source write and
no poison, so a caller that recovers from malformed input **on a valid
presentation** keeps a usable transaction. From the `prepare` call onward every
failure again poisons the original directory tracker and leaves `authorityLocked`
set, so a discarded error cannot commit and cannot retry under the old value. The
barrier only ever *sets* that flag, never resets a prior acquisition.

The supplied-User budget of 64 (before equal-duplicate removal) belongs to **this
interface only**. The existing tenant-scope bundle keeps its larger acceptance.

## What a successful return is not

A successful return is a **transaction-local pin**. It is not an ALLOW value, a
principal, a process-effect permit, an audit receipt or a cross-Store atomicity
claim. The rows stay pinned only until the surrounding transaction completes.

DAB1 does **not** solve the later dropped-audit accounting branch: a separate P2
construction must account for those staged writes before granting a dispatch
permit.

## Verification

`core/internal/store/sqlstore/directory_authority_barrier_test.go`,
`directory_authority_confinement_test.go` and the `_pg_test.go` sibling cover:

- the positive admitted mutation — business marker plus its tenant audit event in
  the declared order, with unchanged H and directory generation, the original
  actor preserved, and the audit append not recorded as audit-before-directory;
- the token-shaped bundle, which reserves no User fence and invents no H;
- stale H and stale E as `ErrConflict` through the envelope, the swallowed
  post-admission rollback, and the complete grammar refusal set with no committed
  marker;
- the order and entry refusals: `View`, a SYSTEM scope, repeated acquisition, a
  prior legacy authority acquisition, a real prior directory-tracked write, a real
  prior tenant audit append, a missing tracker, a pre-existing poison, and the
  pre-admission refusal that leaves a **usable** transaction;
- the entry-presentation regressions — a wrong actual SQL presentation, an
  unreadable one, and a mismatched tracker presentation — asserting **zero
  admission** (no global lock, no reserved H, no bumped tenant), an unchanged
  logical scope, an unconsumed authority acquisition, and no committed marker even
  after the callback swallows the refusal;
- the confinement optionality matrix: all **32 named decorators crossed with the
  four port sets** (neither, bundle-only, DAB1-only, both), requiring the confined
  capability set to equal the raw one exactly, plus same-workspace idempotence,
  cross-workspace refusal, no `AuthScope`/`CustodyScope` promotion, unchanged
  tenant-repository denial through the confined handle, and an explicit check that
  a confined ordinary scope really is an anonymous port struct — the premise the
  single-step attachment rests on;
- the PostgreSQL schedules with the global spool budget **enabled**: both
  supported `AuthMutate` orders (directory global → SYSTEM audit + spool →
  `Users().Update` → H, and directory global → `Users().Update` → H → audit) park
  behind the ordinary holder and complete only after it commits **or** rolls back;
  an ordinary same-tenant lineage writer and a SYSTEM-partition writer do the
  same; a controlled cross-partition stale schedule makes the loser find H moved;
- the negative baseline: a transaction that appends tenant audit before any
  directory admission is refused **in-transaction** while a holder is still
  admitted, never reaching the global lock — so a refusal cannot be mistaken for a
  lock wait.

Blocking is established from the server's own `pg_locks`/`pg_stat_activity` view
of an ungranted **advisory** lock in this database, never from a sleep. Ordering
flags are sampled while the first transaction still HOLDS its locks: a flag or
timestamp read after a transaction returns cannot establish database
serialization. SQLite asserts its real single-writer admission behavior without
claiming to reproduce PostgreSQL row-lock concurrency.

The PostgreSQL schedules run through one bounded harness (`dabSchedule`). Every
worker's SQL uses a cancelable schedule context and a parked holder also returns
on cancellation; every phase wait and the contention observer end on the phase,
on the **first worker exit**, or on a finite budget, whichever comes first; joins
escalate from unconditional release to cancellation within two budgets; and the
test's Cleanup runs the same shutdown before the store closes, so a failure at
any point leaves no goroutine whose end depends on the suite timeout.
`TestDirectoryAuthorityScheduleFailureTerminates` exercises that on both engines:
a holder whose real barrier refuses a malformed bundle before admission ends the
wait on its exit, far inside the budget, without starting the peer; a holder that
stalls inside its transaction before admission ends at a short budget, is
canceled, and is observed to terminate.

## Remaining consumer obligations

No consumer is implemented or authorized here. A P2/RW1/INV1 consumer must
separately bind its own target state, expected versions and tenant ownership,
restrict the mutated fields, define its recovery and output protocol, establish
the prescribed call order including the two unenforceable prohibitions above, and
account for staged writes in the dropped-audit branch before granting a dispatch
permit.

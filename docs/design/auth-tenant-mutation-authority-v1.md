<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Auth-partition mutation authority barrier (ATA1) v1

Status: implemented Store capability, September 2026. Applies to SDD 01, 02, 04 and
06 and the invitation recovery obligation. This is an additive Store capability.
It is not an invitation API, an authorization decision, or a consumer protocol.

## Why it exists

Credentials, users, memberships and invitations live under the reserved system
tenant, but the decision that authorizes changing one of them belongs to a
**business** tenant. An `AuthScope` is pinned to SYSTEM, so the existing
`AuthoritySnapshotBundleLocker` cannot bind that tenant's epoch: its grammar
requires every epoch fact to name the scope's own tenant, and from the auth
partition that tenant is SYSTEM. Forwarding the existing method would therefore
**refuse** the business fact rather than silently authorize it, and a caller must
never substitute SYSTEM's own epoch to satisfy the grammar.

ATA1 is the one Store-owned entrypoint that acquires directory admission and pins
the complete supplied authority for one business tenant inside the same
`AuthMutate` transaction.

## Interface

```go
// core/store/authority_snapshot.go
type AuthTenantAuthorityBarrier interface {
    LockAuthTenantAuthority(context.Context, model.TenantID, AuthoritySnapshotBundle) error
}
```

Only the concrete auth scope implements it. The `tenantScope` the callback is built
over and ordinary workspace scopes do not acquire it. An `AuthView` assertion may
succeed, but invocation returns `store.ErrReadOnly` without a write, so a consumer
that needs the capability can still fail closed on its absence rather than
silently degrade. A missing capability is an infrastructure refusal: callers must
not substitute a read validator or a tenant-only lock.

## Transaction algorithm

One original `AuthMutate` `*sql.Tx` owns every step. No authority or row payload
escapes the barrier.

1. **Entry state, then order, then input — in that order, before any lock.**
   - *Stage 0*: a read-only scope returns `ErrReadOnly`; an existing binding poison
     or an already-poisoned tracker is returned as-is.
   - *Stage 1, entry state*: the logical scope must be SYSTEM **and the ACTUAL SQL
     presentation is read and required to be SYSTEM**. This must precede admission:
     `directoryWriteTracker.prepare` rebinds the transaction to the writer's
     permanent presentation before arming its generation, so a scope whose logical
     field says SYSTEM while its real presentation is a business tenant would
     otherwise be silently repaired by admission and pass a later check. A
     presentation that is valid on *exit* proves cleanup, never valid entry.
   - *Stage 2, order*: repeated acquisition, a prior authority acquisition, an
     already-used directory writer, recorded audit-before-directory state.
   - *Stage 3, input*: an invalid business tenant and the complete fact grammar.
     Caller-owned slices are copied before validation, so a later mutation of the
     caller's slice cannot change the checked question.

   The presentation observed at Stage 1 is the value carried into restoration, so
   the borrowed interval is verified against the transaction's real original
   presentation rather than whatever admission happened to leave behind.
2. **Global admission first.** The existing `directoryWriteTracker.prepare` path
   runs with a discovery callback that reports no affected tenants, so the global
   directory writer lock is acquired and **nothing bumps an epoch**. Inside that
   callback the supplied User IDs are reserved in canonical order through the
   existing `lockUserAuthorities`, using its held-user bookkeeping and control
   generation. H is reserved, never bumped and never repaired.
3. **Existing generation preserved.** `prepare` restores SYSTEM and re-arms the
   existing writer-generation proof. That exact generation and tracker stay in
   force; there is no new marker protocol and no second directory writer.
4. **Borrowed interval.** A private, synchronous, non-escaping helper captures and
   verifies the SYSTEM presentation, binds **both** the SQL presentation and the
   logical authority scope to the requested business tenant, and calls the existing
   complete `LockAuthoritySnapshotBundle`. Its H locks are already covered by step
   2 and are re-acquired there with full version comparison; its canonical
   tenant-fact algorithm — allowlist, lease coordinates, identity-table predicate
   barrier, canonical order — is unchanged.
5. **Strict restoration.** The original logical scope and SQL SYSTEM presentation
   are restored on every exit, including errors and panic unwinding, using a finite
   non-canceled context, a re-read of the actual presentation and required equality
   with the captured value. A restoration failure is recorded in the **original**
   scope's `bindingPoison` and joined with the operation failure, so the
   transaction envelope refuses to commit even when the caller discards the
   returned error. A panic is never recovered: cleanup runs and the original panic
   continues.
6. **After success** the callback sees only SYSTEM repositories again. The complete
   H and tenant authority rows, the global writer lock and the exact generation
   remain owned until the transaction completes.

Lock order: global directory writer → canonical User authority rows → canonical
business-tenant authority facts (including applicable table and lease barriers) →
invitation/product row → audit chain. The existing finish path removes the
generation proof before commit. No database or global lock survives transaction
completion or spans provider, process or network I/O.

Deliberate non-goals of the helper: it does not copy `tenantScope` as a value (it
carries a mutex and transaction-clock state), does not expose the borrowed scope to
a service-supplied callback, does not retain a repository from it, does not mutate
the writer's permanent `presentationTenant`, and adds no exported generic rebind.
It uses `dialect.BindTenant` directly and never `bindDirectoryTenant`, which would
clear the SQLite writer marker and destroy the proof `prepare` just armed.

## Caller obligations

The caller MUST invoke the barrier before any repository row lock, write or audit
lock in that `AuthMutate` callback. Reads that acquire no lock may precede it but
are **not** authoritative final target-state checks. The Store refuses a prior
authority acquisition, an already-used directory writer and recorded
audit-before-directory state; it cannot detect every preceding raw repository row
lock, so the consumer's own construction and tests must establish call order.

The complete authenticated mutation value is obtained outside the transaction
through MA1, using the actual action and target question. Inside the transaction
the consumer obtains `TransactionNow`, verifies `AuthorityFor`, acquires ATA1,
obtains `TransactionNow` again, and verifies the same value and question before the
protected mutation. No policy evaluation or network call runs while these locks are
held. A stale bundle fails; it does not trigger a blind retry under the old value.

## Error semantics

| Condition | Result | Poisons? |
|---|---|---|
| `AuthView` invocation | `store.ErrReadOnly` | no |
| Non-SYSTEM logical scope, missing tracker | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Unreadable actual entry presentation | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Actual entry presentation is not SYSTEM | `store.ErrDirectoryUnavailable` refusal | **yes** |
| Repeated acquisition, prior authority | `errAuthTenantAuthorityRepeated` | no |
| Prior directory use, audit-before-directory | `errAuthTenantAuthorityOrder` | no |
| Invalid / SYSTEM / zero business tenant | existing canonical-tenant refusal | no |
| Malformed, duplicate, cross-tenant, unsupported, over-budget, lease-mismatched, empty facts; conflicting H versions | existing precise grammar refusal, no source write | no |
| Stale H or stale tenant fact | `store.ErrConflict`, preserved through the envelope | yes |
| Missing H or missing fact | existing precise refusal (shapes differ by path and are retained) | yes |
| Bind, restore or presentation failure | joined refusal + original `bindingPoison` | yes |
| Canceled or elapsed deadline | the actual context/store failure, with bounded restoration | yes |

There are two dividing lines, not one.

**Entry state versus everything else.** A defect in the transaction's own entry
state — a non-SYSTEM logical scope, an unreadable actual presentation, or an actual
presentation that is not SYSTEM — poisons the original transaction, because it says
this transaction is not the thing the capability was asked to act on. It must not be
able to commit at all, including when the callback discards the returned error. This
is checked before anything that could normalize it, and it performs no write.

**Admission versus ordinary refusal.** Order and input defects, checked *after* the
presentation is established, return a precise error with no source write and no
poison, so a caller that recovers from malformed input **on a valid presentation**
keeps a usable transaction. From the `prepare` call onward every failure again
poisons the original directory tracker and leaves `authorityLocked` set, so a
discarded error cannot commit and cannot retry under the old value. The
`authorityLocked` guard itself stays owned by the existing bundle method on the
success path; the barrier only ever *sets* the flag, never resets a prior
acquisition.

The supplied-User budget of 64 (before equal-duplicate removal) belongs to **this
interface only**. The existing tenant-scope bundle keeps its larger acceptance.

## What a successful return is not

A successful return is a **transaction-local pin**. It is not an ALLOW value, a
principal, a credential, an invitation retry, a process-effect permit or a
cross-Store atomicity claim. The rows stay pinned only until the surrounding
transaction completes, and the original actor never becomes a system actor: the
partition pins the SYSTEM *tenant* for row-level isolation, while audit attribution
remains the caller's.

## Verification

`core/internal/store/sqlstore/auth_tenant_authority_test.go` and its `_pg_test.go`
sibling cover the positive admitted mutation with an original-actor audit event and
unchanged H/E, the token-shaped bundle, stale H and stale E as `ErrConflict`, the
swallowed-failure rollback, the grammar and order refusals, SYSTEM isolation after
success and failure, injected bind/restore/wrong-presentation/cancellation faults,
the PostgreSQL controlled schedules for a contending supported writer and a
stale-authority loser, and the entry-presentation regressions — a wrong actual SQL
presentation and an unreadable one — which assert zero admission (no global lock,
no reserved H, no bumped tenant), an unchanged logical scope, and no committed
marker even after the callback swallows the refusal. SQLite asserts its real
single-writer admission behavior without claiming to reproduce PostgreSQL row-lock
concurrency. Concurrency oracles sample their ordering flags while the first
transaction still HOLDS its locks; a flag or timestamp sampled after a transaction
returns cannot establish database serialization.

## Remaining consumer obligations

INV1 must separately bind its invitation state, expected version and tenant
ownership, restrict the mutated fields, define its recovery and output protocol,
and establish the prescribed call order. A future consumer that actually writes
another User requires a separately designed complete user set; ATA1 does not
authorize that expansion.

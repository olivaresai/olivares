<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Invitation revocation takes the global directory admission before the invitation row

`Authenticator.RevokeInvite` (`core/auth/onboarding.go`) deleted a pending invitation
row and *then* appended its audit event. That is the inverse of the order
`AcceptInvite` already uses, and on PostgreSQL the two orders form a deadlock cycle.
This change adds one call to the existing compound-callback admission helper at the
start of the revocation callback so both methods take the same two locks in the same
order. It changes no authorization, no invitation lifecycle semantics and no HTTP
behavior.

## The two locks

Both methods run inside one `AuthMutate` transaction and touch the same pair of locks.

| Lock | Taken by | Where |
| --- | --- | --- |
| **Global directory writer** — `pg_advisory_xact_lock` on PostgreSQL, the writer reservation on SQLite | `directoryWriteTracker.prepare` | `core/internal/store/sqlstore/directorywriter.go` |
| **The invitation row** — the ordinary row lock a `DELETE`/`UPDATE` takes | `Invites()` (a plain typed repository, not directory-tracked) | `core/internal/store/sqlstore/authscope.go` |

The audit log an `AuthScope` hands out is *not* a plain audit log: `authAuditLog.Append`
calls `globalFirst`, which runs the directory prepare before any tenant audit lock
(`core/internal/store/sqlstore/authscope.go`). So **every** auth callback that appends
an audit event takes the global lock — the only question is *when*.

- `AcceptInvite` reaches global early, through `Users().Update` (a directory-tracked
  repository whose `Update` calls `prepare`), and only afterwards locks the invitation
  row with `Invites().Update`. **Global, then row.**
- `RevokeInvite` reached the row first with `Invites().Delete`, and only afterwards
  reached global through `auditAct` → `Append` → `globalFirst`. **Row, then global.**

Two concurrent transactions in those two orders are a cycle:

```
accept : holds GLOBAL ......................... waits for the invitation ROW
revoke : holds the invitation ROW ............. waits for GLOBAL
```

PostgreSQL's deadlock detector resolves it by killing one of them
(`SQLSTATE 40P01`). This is an existing defect: it needs no future retry API, and
Grok's independent CE4 finding in the R81 auth-partition review reaches the same
schedule from the store side.

SQLite serializes writers, so the same source cannot exhibit the cycle there. The
ordering is still asserted on SQLite, because the order is a property of the code and
must not be allowed to drift on the engine that cannot punish it.

## The correction

One call at the start of the `RevokeInvite` callback:

```go
if err := prepareUserAuthorityWrite(ctx, as); err != nil {
        return err
}
```

`prepareUserAuthorityWrite` (`core/auth/scim.go`) is the existing admission door for a
compound auth callback: it asserts the optional `store.AuthUserAuthorityWriter`
capability and calls `PrepareUserAuthorityWrite`. Revocation is called with an **empty**
User set, because it changes no `User` and therefore declares no `H`.

The concrete writer (`core/internal/store/sqlstore/userauthority_writer.go`) still runs
the directory prepare for a zero-length set, and that is exactly what is wanted:

- `prepare` takes the global lock **before** any source write;
- `lockUserAuthorities` with no ids locks no `H` row and advances no `lastHeldUser`, so
  it adds no later ordering constraint;
- no tenant is discovered, so no directory epoch `E` is bumped;
- the transaction's tenant presentation is restored and the writer generation armed,
  which is what every other auth mutation in the same transaction already does.

What this deliberately is **not**: an audit-lock workaround, a synthetic grant, a new
store protocol, a change to any role floor, an optimistic-version API or a retry
mechanism. It is an ordering call on an existing capability.

## What is preserved

- The tenant binding check and its coarse `store.ErrNotFound` (never a cross-tenant
  existence oracle) are unchanged and still run before the delete.
- Delete and audit remain in one transaction; actor attribution is unchanged.
- The accepted INV0 half-open expiry change is untouched.
- The error classes `RevokeInvite` can return are unchanged. `store.ErrReadOnly` and
  `store.ErrDirectoryUnavailable` were already reachable from the audit append's own
  `globalFirst`; the admission only moves where they surface. `handleRevokeInvite`
  (`core/api/handlers_onboarding.go`) keeps 204/404 and its existing error mapping.

## Concurrency semantics after the change

Both transactions now queue on the global lock, so the pair serializes. Both orders
remain legal and are distinguished, not conflated:

| Serialization | Outcome |
| --- | --- |
| Revocation commits first | The acceptance cannot commit a credential — the invitation it read is gone. No password, no session, no accept event. |
| Acceptance commits first | The revocation legitimately deletes an already-used invitation and commits its audit event. The account is active with exactly one session. |

## Evidence

`core/auth/invite_revoke_order_internal_test.go` asserts the order itself on SQLite and
PostgreSQL 16, the refusals (absent capability, rejected preparation) before any delete,
that `H` and both tenants' `E` are unmoved by a completed revocation, and — with two
real PostgreSQL transactions and barriers released only after `pg_stat_activity`
confirms the peer is parked on a heavyweight lock — that the **preserved baseline body**
deadlocks (`wait_event = transactionid`, `SQLSTATE 40P01`) while the corrected method
queues on the admission instead (`wait_event = advisory`) and lands on one of the two
legal serializations with no partial credential.

## Scope

This addresses lock order only. Stronger authorization for revocation, accepted-state
policy, an optimistic-version API and a full retry path remain out of scope and are
owned elsewhere.

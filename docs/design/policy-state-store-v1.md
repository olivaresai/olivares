# State-only Policy storage, v1 (SES2-S)

Construction ratified 2026-09-12. SDD 02/06 and the exact-money codec. This is the storage increment only.

## What it adds

`store.PolicyStateWriter` is an optional capability of the policy repository:

- `GetPolicyState(ctx, id)` reads `id, kind, enabled, version, updated_at`.
- `SetPolicyEnabled(ctx, id, expectedKind, expectedVersion, enabled)` writes
  `enabled, updated_at, version = version + 1` under an exact
  `tenant_id, id, kind, version` predicate plus the ordinary soft-delete clause.

Neither statement names the `spec` column. That is the whole preservation
mechanism: the stored document survives a state change because the SQL cannot
touch it, not because a copy is made carefully. The ordinary paths cannot do
this — the generic update sets every declared field and the typed update encodes
a decoded entity, so both rewrite `spec` from whatever the decoder produced, and
on a malformed document the decode fails before the row can be reached at all.

`store.PolicyState` deliberately has no `Spec` field. A type that cannot carry
the document cannot re-encode it.

## Behavior

- `SetPolicyEnabled` requires the caller to have observed the engine clock
  through `TransactionClock.TransactionNow` on the same scope. The stored
  `updated_at` is exactly that observation, never the injected application
  clock, which a deployment or test may fix or skew. A missing or failed
  observation returns `ErrTransactionTimeNotObserved` before any write; there is
  no fallback and no second transaction. `GetPolicyState` needs no observation.
- The projection is scanned into neutral driver values and classified here.
  `database/sql`'s typed destinations quote the offending cell in a conversion
  error, and the policy schema declares ordinary INTEGER/TEXT columns with no
  STRICT mode and no metadata CHECK, so a malformed scalar could otherwise leave
  part of a stored row inside an error string. Supported forms: text as `string`
  or `[]byte`; `enabled` as `bool` or SQLite's INTEGER `0`/`1`; `version` as
  `int64`. Anything else is a constant-field refusal. Genuine backend and
  cancellation errors are returned unchanged and are never redacted or
  relabelled.
- Validation before I/O: canonical non-zero ID, non-empty `expectedKind`,
  `expectedVersion >= 1`, and a refusal at `MaxInt64` because `version + 1` is
  computed by the engine and would otherwise wrap negative.
- A read-only scope refuses `SetPolicyEnabled` with `ErrReadOnly` before SQL.
  `GetPolicyState` is valid in a `View`.
- Zero affected rows are explained from the state projection alone: absent,
  foreign or wrong-kind is `ErrNotFound`; the same kind at a different version is
  `ErrConflict`. A scan or driver failure is returned as itself and is never
  relabelled as absence or conflict.
- A same-state assertion is accepted and advances the version exactly once.
- The post-write re-read failure is returned, so the surrounding `Mutate` cannot
  report success. The capability opens no transaction and commits nothing.
- Metadata refusals name constant fields only; no message carries row content.

## What it is not

It grants nothing. A caller keeps its own permission and transactional authority
obligations, and holding the capability is not permission to write. No route,
module writer, audit path, permission or MA1 issuance is connected: the native
HTTP/authority/audit construction remains unratified and required.

It is not recovery or bounded materialization. Original custody for values that
Upsert replaces or deletes remains R1/R2.

## Confinement

`workspaceConfinedScope.Policies()` returns a policy-specific denied repository
that implements the capability and always refuses with the existing
`ErrWorkspaceLineageRequired`. Satisfying the interface is not support. The
generic denied repository is deliberately left unwidened, so no unrelated
confined entity gains a policy interface. A genuinely absent capability has no
fallback: a caller must fail closed.

`Repository[Policy]`, `RowLocker[Policy]` and `PolicySnapshotRepository` are
unchanged and still present on the same value.

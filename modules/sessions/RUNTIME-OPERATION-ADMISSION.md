<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
-->

# Run operation admission (RW1)

One operation owner per `liveKey(tenant, run_ref)`, admitted in a way the caller's
own context can abandon. This note records why the per-run lock is a token channel
and what the primitive does *not* promise.

## The defect it corrects

`lockRun` took a reference on the run's entry and then blocked on
`sync.Mutex.Lock`. A mutex offers no choice between "the lock" and "my caller went
away", so an operator or work request whose HTTP client had already disconnected
kept queueing behind whoever held the run, and only learned about the cancellation
at its first context-aware store call — *after* it had been admitted as the run's
exclusive owner. A canceled waiter is not an active execution owner, and the queue
must let it leave.

## Shape

`opLock.token` is a channel of capacity 1 created holding one permit. Receiving the
permit *is* ownership; returning it *is* release. `opMu` guards only the map and
the reference counts, and is never held across a wait, so bookkeeping can never
depend on a release that is itself waiting for bookkeeping.

Every holder **and every queued waiter** takes a reference under `opMu` before it
leaves that critical section. That is what makes an entry safe to reclaim: a
holder's release cannot delete an entry a waiter still points at, and deletion
additionally requires the map to still hold that *identical* entry, so a late drop
can never remove a successor's entry under the same key.

Cancellation is checked twice, for two different questions:

- **before** admission, so an already-canceled or already-expired caller cannot
  consume a free permit (and no entry is even allocated for a nil context, which is
  a distinct fixed private error — a call-site bug, not a request outcome);
- **immediately after** a permit is received, because `select` may pick the permit
  branch when the permit and `ctx.Done()` are both ready. Observing the
  cancellation there returns the permit to the next waiter rather than carrying it
  into an operation nobody is waiting for.

Release is a `sync.Once` closure: idempotent against concurrent duplicate calls,
not merely sequential ones. It returns the permit **before** dropping the owner's
reference. In the opposite order the entry could be reclaimed and a fresh one
created for the same key while the old owner still carried the old permit — two
owners of one live process.

There is no goroutine per waiter, no second supervisor and no second lock
namespace.

## Callers

Eleven operator and work entrypoints — `resumeRun`, `stopRun`, `interruptRun`,
`sendTextInput`, `cleanupRun`, `deleteRun`, `sendInput`, `InputForWork`,
`TextForWork`, `interruptForWork`, `StopForWork` — admit with their own caller
context and refuse with the actual context error plus their existing zero result.
An admission refusal reaches no store, ledger, audit, credential or process seam.
Everything that already ran before acquisition (`TextForWork`'s bounded text,
`StopForWork`'s bounded reason) still runs first, and every authority, claim,
work-fence, generation and attempted/unknown rule still runs after.

`terminateForKillSwitch` stays on the uncancellable `lockRun`, which is the same
implementation and the same token namespace waiting on `context.Background()`.
Routing the emergency-stop sweep's wait through a cancellable context would newly
skip stop admission whenever that sweep is canceled; that is a policy change, and
RW1 does not make it. The sweep's broader cancellation semantics are unchanged.

## Not promised

- **No fairness.** Waiters are not FIFO and none is guaranteed to win a handoff.
- **No finite bound for an uncancellable caller.** `context.Background()` still
  waits as long as the holder holds. RW1 adds cancellation-awareness, not a
  deadline.
- **No atomic context-to-effect boundary.** Cancellation observed *after* a
  successful admission belongs to the operation body, exactly as before.
- **No completion guarantee.** Admission is exclusion, not proof that a stop, an
  interrupt or a credential revocation succeeded.

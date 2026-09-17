<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Route mutation authority v1

`RouteMutationAuthorization` is process-local evidence that ONE mutation question was
authorized for ONE reconstructed principal, carrying the complete authority bundle that
mutation's transaction must pin. It is the additive prerequisite MA1 specifies for managed
Stop P2. It does not complete Stop, change operator HTTP behavior, or mount a lifecycle
route.

## What it is not

It is not a bearer credential, a durable approval, a serialization format, a historical
replay record, a single-use permit, an authorization cache, or cross-process proof. It is
not a guarantee that an external process effect can execute later. Holding one is not
permission to write.

There is no exported constructor, no mutable field, no `AllowWitness`, no `FactsFor`, no
JSON codec and no boolean permit. A value can only come out of `AuthorizeRouteMutation`,
and the only question it answers is `AuthorityFor`. The empty value always refuses.

## Interface

```go
func (az *Authorizer) AuthorizeRouteMutation(ctx context.Context, req Request) (RouteMutationAuthorization, error)
func (a RouteMutationAuthorization) AuthorityFor(now time.Time, req Request) (store.AuthoritySnapshotBundle, error)
```

This type is separate from `RouteReadDecision` and is not implemented by calling
`DecideRouteRead`. A human read decision remains unusable as a mutation result even when
its question spells a write action. `Request`, `PrincipalRef`, route metadata, the
permission grammar, the ordinary route witness and evidence digest v2 are unchanged.

## Issuance

1. A nil authorizer answers `ErrAuthorizerUnavailable`. A canceled caller gets its own
   context error; a missing or exhausted deadline gets `ErrRouteUndecided`. The new entry
   point requires a finite, live deadline because the value it returns outlives the call.
   The older entry points are unchanged.
2. The request is deep-copied with the existing private clone helper. No caller-owned
   slice or map is retained.
3. `AuthorizeRoute` is called EXACTLY ONCE. Its step-up-before-evaluation order, denials
   and undecided answers are preserved verbatim. No authorization is inferred from a
   reason string, and no result is issued for anything but an ALLOW.
4. The complete principal authority bundle and window are extracted from that same copied,
   sealed principal through `principalCompleteAuthorizationEvidence` — the existing read
   extraction, renamed because it is no longer read-only. A supported explicit authority
   mode is required: a human has one valid matching User fence, a token has none. System
   actors, superadmins, local and synthetic principals cannot acquire the seal this rests
   on, so they gain no path.
5. The ordinary witness must verify for the copied whole question at the authorizer clock,
   its window must be contained by the caller's deadline, and its facts must recompute to
   the canonical order with complete lease coordinates. Nothing is dropped, broadened,
   substituted or repaired.
6. Private copies of the witness, the authority mode, the User coordinates, the tenant, the
   credential reference and the sealed principal digest are sealed under the integrity
   format below. Cancellation is rechecked before returning. No product or authority write
   occurs.

## Verification

`AuthorityFor` refuses an unissued value, a non-ALLOW, a malformed mode or User
coordinates, noncanonical facts, a changed integrity digest, an invalid ordinary witness,
or a time outside the half-open evidence window `[ObservedAt, FreshUntil)`. It verifies the
whole question through the existing witness checks and compares the supplied principal's
seal, tenant, credential reference, authority mode and User coordinates against the
captured value: a freshly reconstructed principal with different provenance requires fresh
authorization even when its resource ID is equal.

On success it returns the canonical witness facts plus the captured human User fence — or
no User fences for a token — in fresh slice storage. Every refusal is an empty bundle and
`ErrRouteUndecided`; a partial bundle is never returned. It does not refresh a principal,
query a database, advance an epoch, extend a lifetime or make an external call.

A copied valid value is still evidence for the same question. MA1 does not claim single
consumption; the durable managed-stop intent owns deduplication and dispatch ambiguity.

## Integrity format

Private SHA-256 domain: ASCII `olivares.auth.route-mutation-authority.v1` followed by one
NUL byte. This is an unkeyed consistency commitment in a trusted process, not a MAC and not
cryptographic authentication against that process.

Canonical input, in order:

1. the ordinary witness `EvidenceDigest`, 32 raw bytes;
2. the captured principal seal, 32 raw bytes;
3. one authority-mode byte, using the existing private constants unchanged;
4. the User count as u64 big-endian — zero for a token, one for a human — and, for a human,
   its canonical User ID as length-prefixed UTF-8 (u64 big-endian byte count) followed by
   its positive version as u64 big-endian.

No platform-native size, textual integer, map iteration, ambiguous concatenation,
optional-field omission or JSON serialization participates. All tenant facts, including
complete lease coordinates, are already bound by the ordinary witness's versioned digest,
whose valid recomputation and canonical order are required; that codec is not changed and
no decoder is added. `docs/design/route-evidence-digest-v2.md` remains the fact codec.

## Consumer obligation

The future Community-owned managed Stop consumer constructs its permission, Cedar action,
route metadata, tenant, run reference, workspace and generation attributes from
server-owned code and the authorized run; the private coordinator supplies the original
credential reference and the distinct session-link/launch identities. Neither side accepts
a caller-selected weaker question or an `authorized=true` flag.

For its durable stop-intent transaction that consumer must reconstruct current original
principal authority, obtain this authorization, obtain transaction time, call
`AuthorityFor` and `LockAuthoritySnapshotBundle`, recheck the finite evidence lifetime, and
validate the run's current launch/claim under the prescribed data lock order before
writing. If the complete bundle locker is unavailable it must refuse rather than fall back
to the tenant-only locker. A stale authority check requires a fresh decision, not a blind
database retry with the old result. No authorization/policy engine or provider process call
occurs while those database locks are held.

**A note on the protected write's own repository, because the first implementation
attempt got this wrong.** `LockAuthoritySnapshotBundle` neither arms nor mutates the
directory: it acquires the User fences and then applies the tenant facts through the raw
generic repository, which reserves SQLite's writer through the engine-owned scope row or
takes the PostgreSQL row lock directly. What it does do is mark the transaction
authority-before-directory, so a NEW directory write attempted afterwards is deliberately
refused. `Agents()` is wrapped in a directory-tracked repository whose `Create` calls the
directory writer's prepare, so choosing it as the protected write measured that ordering
rule instead of the barrier. A plain typed tenant repository such as `Providers()` has no
such wrapper and is the correct shape for a protected write in this position. There is no
generic directory-arming prerequisite on the bundle locker, and omitting the epoch from a
consumer's fact set is NOT an available remedy — the complete fact set is required.

This states MA1's consumer obligation, not a completed Stop transaction. P2 must still
specify the owned per-run lock order, durable stop-intent identity, ambiguous commit
recovery, effect-attempt marker, duplicate handling, cancellation before and after signal,
bounded shutdown/reap, and the effect-admission policy for permission revocation after
admission. A durable intent and an OS signal are not one atomic transaction, and MA1
supplies no lock across a database commit and a signal.

The generation UUID is `runtime_launch_id` (`model.ID`); it is neither the private uint64
link generation nor a WorkLease fence. `process_exit_observed` differs from
`process_wait_unverified` and `handle_lost_unconfirmed`: a terminal database row alone
cannot confirm a reaped process. Existing operator HTTP stop and WorkLease paths are
unchanged.

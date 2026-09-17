<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Route evidence digest v2: exact public format and compatibility contract

`core/auth.RouteEvidenceDigestFormat` is `olivares.auth.route-evidence.v2`. It names the
codec of `RouteAuthorizationWitness.EvidenceDigest`. It selects no version, accepts no caller
option, exposes no lease internals and verifies no authority. The digest it names is an
unkeyed SHA-256 consistency commitment over one process-local witness: not a MAC, not a wire
or cross-process authority format, and not a record from which a historical decision can be
replayed. This document is the public statement of the bytes; `evidenceDigest` in
`core/auth/route_policy.go` is the private implementation and is not exported.

## Why v2 exists

Until this codec, the generic witness committed each authorization fact by Kind, ID and
Version only, while canonical fact comparison and the final SQL validation already treated a
leased fact's presence, subject, fence and deadline as part of its identity. A copied witness
whose leased fact changed only in a lease coordinate therefore recomputed to its original
digest and remained sound. That is an integrity-digest regression, not an exploit: the final
SQL validation refused a missing or mismatched lease before this change and refuses it still,
and no authorization bypass was established. v2 repairs the consistency commitment the digest
claims to be.

## Primitives

| Notation | Bytes |
|---|---|
| `text(s)` | `u64(len([]byte(s))) \|\| []byte(s)`: the unsigned 64-bit big-endian byte length of the Go string followed by its unchanged bytes. No UTF-8 validation, replacement or normalization; an accepted lease subject that is not valid UTF-8 is framed as the bytes it is. |
| `u64(v)` | unsigned 64-bit big-endian. Signed fact fields (Version, fence) use the existing conversion to `uint64`; positive values remain a producer invariant. |
| digest | exactly 32 raw bytes, without framing |
| presence | one byte, `0x00` or `0x01` |

## Field order

The digest is SHA-256 over the exact concatenation:

1. `text(RouteEvidenceDigestFormat)`.
2. `text(CedarAction)`, `u64(Decision.Outcome)`, `u64(ScopedEffect)`.
3. For `CorePermission`, `ResourceGuard`, `ForbidAbsence`, in that order: `u64(Verdict)`, `text(Code)`.
4. `ResourceDigest[32]`, `QuestionDigest[32]`.
5. `text(ObservedAt.UTC().Format(time.RFC3339Nano))`, `text(FreshUntil.UTC().Format(time.RFC3339Nano))`.
6. `u64(number of facts)`. For each fact in the existing canonical Kind/ID order: `text(Kind)`,
   `text(ID)`, `u64(Version)`, presence byte from `LeaseFenceWitness`. If present:
   `text(subject)`, `u64(fence)`, `text(deadline.String())`. If absent: nothing more.
7. `u64(PolicyVersion)`.

### Window endpoints

The endpoints are exactly the bytes of the named Go rendering of the UTC instant, including
extended-year output when an accepted value needs it. No claim is made that every such string
satisfies an RFC 3339 parser. UTC removes location distinctions, and the evidence window
carries no monotonic reading, so two representations of one instant render identically.
Length framing and full fractional nanosecond precision are retained. Existing admission
permits instants outside the int64 `UnixNano` range; such windows are neither truncated,
clamped nor rejected to fit a narrower encoding.

### Fact vector

Canonicalization, duplicate refusal and same-kind/same-subject conflicts remain issuance-time
properties. The codec never sorts, deduplicates or discards a fact, so a mutated fact vector
recomputes to a different value instead of being repaired into a matching one. The deadline is
the existing canonical `Timestamp` text; a lease is never reduced to its Version, and no private
field is exported.

## Derived chat codecs and metadata

`cmd/olivares/modelsguardedtext.go` derives two commitments from this witness.

- The chat effect domain is `olivares.model-gateway-chat.effect.v2`. Its preimage is the v1
  sequence with one insertion: immediately after the existing route-witness presence boolean,
  `text(RouteEvidenceDigestFormat)` when a witness is present, `text("")` when absent. The
  absent-witness fields that follow keep their v1 bytes. Presence is the existing decision
  read from the witness's own seals, shared privately by the codec and the metadata; no new
  heuristic is introduced, and empty ordinary-door authority is never described as a verified
  route witness.
- The chat outcome domain is `olivares.model-gateway-chat.outcome.v2`. `text(chatOutcomeDomain)`
  is prepended before the existing `text(chatOutcomeAction)`; the action string and the rest
  of the v1 preimage are unchanged.
- The decoded-observation codec stays `olivares.model-gateway-chat.observation.v1`: its
  structure and semantics are unchanged.

Both intent and outcome metadata now carry `effect_digest_format` (the effect domain),
`effect_digest_algorithm` (`sha256`) and `route_evidence_digest_format`
(`RouteEvidenceDigestFormat` when the same presence decision is true, otherwise `none`).
Outcome metadata additionally carries `outcome_digest_format` (the outcome domain) and
`outcome_digest_algorithm` (`sha256`). These are fixed software-owned values, never provider
or caller text. They identify append-side algorithms and nothing else: a label does not verify
a digest, does not create authority and does not provide the preimage a record without labels
never stored. Existing metadata fields, refs, nullable usage, profile and policy projections,
and privacy rules remain intact; generic, complete-read, effect and outcome digest bytes
deliberately change, and the audit store commits the metadata normally.

## Compatibility boundaries

- **U2 complete read.** The complete read domain `olivares.auth.route-read-authority.v1` and
  its field sequence are unchanged. Its inner generic digest bytes now differ, so a
  complete-read receipt built over the old inner bytes does not satisfy the current
  recomputation. No U2 byte compatibility is claimed. The principal authority seal v2 and its
  sealed golden `80c3e8220c64124c9e4c47bc26ce6174a970911012f790d6ba00311bedff61ba` belong to
  the principal seal, not to this codec, and are unchanged.
- **No accepted cross-process or wire format.** Restart or reconstruction creates current
  values. In-flight calls in an older binary retain their old internal semantics until that
  process drains; this codec does not enable mixed-process witness exchange. Old byte values do
  not satisfy the current recomputation, and an old digest is never deserialized into a newly
  minted witness: credentials are reconstructed and authorized normally.
- **Historical ledger records.** Chat records sealed before v2 carry no format labels and no
  preimage. They remain unlabeled opaque historical commitments whose chain still verifies;
  a consumer must not assume they use v2, and no legacy row rewrite, backfill, chain resealing
  or claimed v1 decoder exists. Persisted chat attempts remain distinct by their existing
  attempt identity; `inferenceEvidenceWriter.Append` does not demonstrate global
  idempotent-rebind enforcement and this codec claims none.
- **Enterprise.** Digest-named Enterprise slots have no verified producer from this witness and
  are neither reinterpreted nor modified.
- **Unchanged behavior.** Canonical issuance, freshness, the question digest, the SQL barriers,
  credentials, policy and store behavior are unchanged. No evidence-window validation or
  narrowing is added.

## Verification anchors

- `core/auth/route_evidence_digest_internal_test.go`: causal lease-coordinate control on a
  witness minted through `AuthorizeRoute`; independently specified fixed vectors for ordinary,
  leased, mixed and empty fact sets, an accepted non-UTF-8 subject, window endpoints before
  1678 and after 2262 and UTC-offset equivalence; canonical-order and contradiction behavior
  through the issuer; domain mismatch and zero or unminted refusal; the new complete-read
  fixed vectors in human and token mode recording the explicit inner-byte change.
- `core/auth/user_authority_read_internal_test.go`: the existing lease mutation control now
  requires both the inner and the complete digest to move and preserves `AuthorityFor`
  refusal.
- `cmd/olivares/modelsguardedtext_test.go`: v2 effect and outcome vectors, presence and
  `none` labels, real intent and outcome metadata through the evidence writer, privacy
  assertions, and a legacy-shaped unlabeled record appended through the actual store beside
  v2 records with the chain verified and its canonical metadata and hash byte-identical.

The fixed vectors were computed by an assessment-owned encoder from this specification, not
by asking the implementation for its expected answer.

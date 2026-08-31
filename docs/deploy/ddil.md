<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Operating at the disconnected edge (DDIL)

Olivares runs at the tactical / disconnected edge, where the network is
Denied, Disrupted, Intermittent or Limited (DDIL). A satellite or pLEO bearer is
just intermittent IP — the control plane runs over it unchanged. What matters is
how governance behaves while the link is down for hours or days and returns in
short windows. This page is the honest operator guide: what survives an outage,
what does not, and the limits you must declare.

The design decisions behind this page are recorded in
[ADR-0024](../adr/0024-ddil-offline-semantics-and-signed-bundle.md).

## The three planes, offline

### Policy — deny survives, positive grants expire

The policy decision point evaluates against the **local** policy store, so
policy keeps working with no link. The question is staleness: how long may a
disconnected node keep trusting policy it can no longer refresh?

The answer is **asymmetric**, and it fails safe:

| Rule | Offline, past `policy_max_staleness` | Why |
|---|---|---|
| Deny / `forbid` | **still enforced** | a stale restriction can only restrict, never escalate |
| Positive grant / `allow` | **expires → deny-closed** | an expired grant must never authorize |
| Break-glass | available (its own 1h/24h expiry) | the sanctioned offline escape hatch |

**Enabling it.** Set `OLIVARES_POLICY_MAX_STALENESS` (a Go duration, e.g. `72h`) on
the edge deployment. The bound is enforced deployment-wide by the scoped-grant engine:
once a tenant's active policy has not been re-established (an admin publish, or — when
wired — a DDIL bundle adoption) within the bound, that tenant's positive Cedar grants
expire to deny-closed, while its `forbid` rules and the ABAC deny-overlay stay enforced.
Leaving the variable unset (the default) means **no bound** — a connected deployment
behaves exactly as before and a grant never expires. Every expiry is written to the
audit trail (`cedar scoped grant-expired`), so the transition is never silent.

`policy_max_staleness` (ratified default **72h**) is an operator choice. The ADR
envisions it travelling with the signed policy snapshot so the edge honours the bound the
*centre* chose. Both now hold: it is a deployment-wide setting AND a signed DDIL bundle
carries a **per-tenant override** (`policy_max_staleness` in the bundle Index) that, once
adopted (`olivares ddil import`), wins over the deployment default for that tenant —
strict precedence is per-tenant override > deployment default > no bound. Crucially the
freshness clock is now **durable**: it is stamped from the bundle's *signed* creation time
(never the importer's wall clock) and restored, not reset, on restart — so a disconnected
edge can no longer refresh a stale grant simply by rebooting. A prominent console/CLI
age-and-expiry readout is the remaining observability follow-up.

> An outage longer than `policy_max_staleness` therefore keeps **denying** what
> was always denied, and stops honouring positive grants until a fresh policy
> arrives (or an operator uses break-glass). This is deliberate: at the edge,
> losing the link must never quietly widen what an agent may do.

### Audit — the ledger is the spool; loss is impossible-by-default

A disconnected node keeps appending to its **local hash-chained audit ledger**.
Nothing is buffered only in memory, so an outage loses no evidence — the ledger
*is* the durable store-and-forward spool. When a link or a courier appears, the
accumulated segments are forwarded (online) or carried in a signed bundle
(air-gap, below).

The one way evidence could be lost is disk exhaustion during a long outage. The
behaviour is a declared operator choice, **enforced in the ledger writer**:

| `audit.spool.on_full` | Behaviour | Use when |
|---|---|---|
| `block` (**default**) | refuse new governed actions before losing evidence (HTTP 503 `audit_spool_full`); reads keep serving | evidence integrity outranks availability |
| `degrade` | count every dropped event durably and append a **signed, in-chain gap marker** `{from_seq, to_seq, reason: "spool_full", count, at}` so the loss is provable, never silent | the mission must not halt, and a tamper-evident gap is acceptable |

**Enabling it.** Set `OLIVARES_AUDIT_SPOOL_MAX_BYTES` (an integer byte budget)
and optionally `OLIVARES_AUDIT_SPOOL_ON_FULL` (`block`, the default, or
`degrade`). The budget measures the **logical stored event bytes** — the exact
values in the ledger rows, kept by an exact incremental counter that is
recomputed from the ledger on every budgeted boot (so config toggles, upgrades
and DR restores can never drift it) — not database pages or file size; size the
budget below the real disk with headroom for engine overhead. Unset (the
default) means no budget: a connected deployment behaves exactly as before. On
Postgres the budgeted boot needs the cross-tenant admin pool (`AdminDSN`),
because row-level security correctly blocks the recompute on the app role.

In `block`, integrity machinery (checkpoints, archive anchors, the gap marker
itself) is still admitted — small, rate-bounded writes that keep the ledger
provable while governance is halted — and everything stays fully accounted.

In `degrade`, a dropped action still **commits** (that is the availability
trade), its evidence does not; the drop is counted durably per tenant. The
signed marker seals the episode on the next write that fits (or every 10 000
drops, so an unbounded outage still leaves bounded in-chain honesty), declaring
the exact dropped range `[from_seq, to_seq]` — the **only** sanctioned
discontinuity in the chain. Hash linkage stays continuous across it. The live
verifier (`olivares audit verify`), the archive exporter and the offline
archive verifier all treat a correctly-declared, correctly-signed marker as a
declared boundary (reported in `declared_gaps`) and keep failing on everything
else: an undeclared hole is still `seq-gap`, a marker whose declaration does
not match its position is `gap-mismatch`, and an unsigned or wrongly-signed
marker fails the per-event signature check — forging one requires the per-event
signing key.

### Evidence — exported and carried, verified offline

Evidence exports (compliance packs, access reviews) are produced locally and
travel in the air-gap bundle. They are verified after import by the existing
offline archive verifier.

## The air-gap bundle (one format, shared with updates)

A disconnected site moves state across the gap in a single **signed** bundle —
the same verifiable envelope the OTA updater uses (`core/sigbundle`,
domain-tagged `olivares.ddil-bundle.v1`), never a second format. It carries the
policy snapshot, the accumulated audit segments, and evidence, under one detached
Ed25519 signature over the domain-separated manifest, with every payload bound by
SHA-256.

**The operator surface is `olivares ddil`:**

| Command | What it does |
|---|---|
| `ddil keygen` | generate the dedicated Ed25519 transport keypair (NOT the ledger or release key); the receiving node pins the public half |
| `ddil export --tenant … --sign-key … --out bundle` | assemble and sign a bundle from the local store: the active Cedar policy union, the accumulated audit segments (`--from-seq` to resume), evidence (`--evidence name=path`), and the centre's per-tenant bound (`--max-staleness`) |
| `ddil verify --bundle … --pubkey …` | verify and inspect a courier bundle offline WITHOUT applying anything |
| `ddil import --bundle … --pubkey … --tenant … --audit-out dir [--evidence-out dir]` | verify offline, reconcile against local state, and apply fail-closed |

Import pins the exporter's public key (`--pubkey`, required — the receiver does
not hold the signing key) and applies the three planes independently: the audit
segments land in a local WORM archive directory (the cursor is derived from that
directory, so a gap is deny-closed and a fork — same seq numbers, divergent hash
lineage — is refused), the policy plane is adopted into the engine (below), and
evidence is extracted read-only. A re-imported bundle applies nothing new
(idempotent).

Verification on import is **100% offline** and fail-closed, in order:

1. verify the signature over the manifest **before** parsing it;
2. re-digest every payload against the manifest;
3. check the bundle has not expired (freshness bound);
4. check the audit segments are internally continuous (ascending, gap-free,
   prev-hash linked) — a gap or reorder introduced in transit is caught.

Reconciliation is **idempotent**: a bundle re-delivered after a flaky link
applies nothing already held (segments at or below the local audit cursor are
skipped), so reconnection re-sends never duplicate ledger rows. If the first new
segment does not begin exactly at `cursor+1`, the importer refuses to apply it
(deny-closed) rather than leave a hole in the local chain.

## What does NOT survive

Honesty about the limits:

- **An infinite outage.** Positive grants stop after `policy_max_staleness`; a
  full audit disk halts governance (`block`) or records a signed gap
  (`degrade`). Nothing here makes a permanently-disconnected node govern forever.
- **Config writes at the edge.** DDIL covers policy *reads*, audit *accumulation*
  and evidence *export*. It is not multi-master replication of configuration
  writes.
- **Real-time central visibility.** While disconnected, the centre sees the edge
  only as of the last sync; the edge shows its own degraded state locally.

## Implementation status

Delivered and tested (2026-07-09, the DDIL foundation):

- **ADR-0024** — the ratified semantics above.
- **`core/sigbundle`** — the one signed envelope + the domain-tag registry;
  `core/release` (OTA updater) refactored onto it with a byte-identical golden test.
- **`core/ddil`** — the air-gap bundle: export, offline verify, and idempotent,
  continuity-checked reconciliation, with a full drop→accumulate→reconnect→
  reconcile E2E test.

Delivered and tested (2026-07-10, the staleness-enforcement follow-up):

- **`policy_max_staleness` enforcement in the live PDP** — the scoped-grant engine
  stamps each tenant's policy-refresh time and, past the configured bound, turns a
  positive Cedar grant into a deny-closed abstain while leaving `forbid`/deny untouched
  (the asymmetric Q1 outcome). Wired from `OLIVARES_POLICY_MAX_STALENESS`; break-glass
  is exempt (a separate mechanism with its own time-box). Covered by three engine tests:
  grant expires past the bound, forbid survives it, and no-bound = no change.

Delivered and tested (2026-07-10, the ADR Q2 follow-up):

- **The audit disk-budget guard and its signed gap marker in the ledger writer** —
  `audit.spool.max_bytes` / `audit.spool.on_full` as described above: exact
  logical-byte accounting with drift-proof boot recompute, the deny-closed
  `block` guard (503, reads keep serving, integrity machinery exempt but
  accounted), the `degrade` drop-and-count path with the signed in-chain
  `audit.gap` marker, and declared-gap awareness in all three verification
  planes (live chain, archive export, offline archive verify) plus the
  air-gap bundle's boundary math, which needed no change by construction.

Delivered and tested (2026-07-10, the DDIL CLI + bundle→engine adoption):

- **The `olivares ddil` CLI** — `keygen`/`export`/`verify`/`import` as described above, with
  a full export→import round-trip E2E, idempotent re-import, gap deny-closed, fork detection,
  and tamper/wrong-tenant/wrong-key refusals.
- **The DDIL bundle → live-engine policy adoption** — an imported bundle's signed Cedar
  snapshot is persisted as an active append-only revision on a dedicated `cedar-ddil` surface
  (unioned with the local `cedar` and `cedar-managed` policies, never clobbering them) and its
  per-tenant `policy_max_staleness` becomes a durable override. The freshness clock is stamped
  from the bundle's authenticated creation time and is **durable across restarts** — closing
  the earlier gap where a reboot reset the offline-trust window. A replayed or rolled-back
  bundle (created no later than the last adopted one) is refused; a re-adopted identical bundle
  is a no-op.

Remaining, scoped as follow-up (not claimed done): the opportunistic-sync loop
(destination-health link detection, resumable chunked transfer, policy-pull before audit-push,
jitter/pacing); and the degraded-mode / policy-age indicator in the console and CLI.

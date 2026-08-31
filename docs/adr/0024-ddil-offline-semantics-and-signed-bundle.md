<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# ADR-0024: DDIL offline semantics per plane, and one signed bundle format

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## Context and problem statement

Olivares is deployed at the tactical / disconnected edge (DoD DDIL: "expects units
to operate at least partially disconnected… across air-gapped networks… and the
tactical edge"). The edge buyer does not ask us to "integrate a satellite link" — a
pLEO/satellite bearer is just intermittent IP, and the app runs over it unchanged.
What they require is that governance keeps working when the link is down for hours or
days and returns in short windows ("submarine surfacing").

The building blocks already exist and were verified during discovery:

- The **audit ledger is already a durable, per-tenant, hash-chained, signed local
  store** (`core/internal/store/sqlstore/audit.go`; ADR-0009). Disconnection does not
  gap it — it simply stops the off-box **forward cursor** (`modules/siemforward`,
  driven by the eventing platform) from advancing. There is no in-RAM-only audit buffer to
  lose.
- The **PDP evaluates against the LOCAL policy store** (embedded Cedar, ADR-0013), so
  policy already works offline. What is undecided is *staleness*: for how long may a
  disconnected node keep trusting policy it can no longer refresh?
- The **durable bus** is a leader-only, at-least-once JetStream overlay
  (ADR-0021) whose backend is a private enterprise build; the OSS tree ships the seam
  only. It is a *distribution* backbone, not a local disk spool.
- **the OTA updater already defines a signed bundle** for air-gap updates: a gzip tar of a JSON
  `manifest.json` plus a detached Ed25519 signature over the domain-separated verbatim
  bytes (`tag || manifest`, tag `olivares.update-manifest.v1\n`), verified BEFORE
  parse (`core/release/manifest.go`). A separate `airgap-bundle.sh` (cosign, images +
  chart) and `core/dr/bundle.go` (AES-GCM-sealed DR snapshot) also exist.

Three questions must be settled before any DDIL code is written, because they define
fail-safe direction, not mechanism.

## Decision drivers

- **Fail-safe in the correct direction.** A governance control plane must never
  *escalate* privilege because it lost its link, and must never *silently* lose
  evidence.
- **Mission-safety at the edge.** A link outage measured in hours must not become a
  mission-kill if the safe answer was already known locally.
- **No format sprawl.** "One verifiable bundle format, not two" (DDIL design brief). A second
  hand-rolled signed-envelope implementation is a second place to get domain
  separation wrong — exactly the cross-protocol key-reuse trap the OTA updater already paid for.
- **Honesty.** Declared, documented limits (disk budgets, TTLs, what does not survive
  an infinite outage) over silent truncation.

## Considered options

### Q1 — Offline policy trust

- **A. Asymmetric (deny eternal, allow expires).** Restricting rules (ABAC deny,
  Cedar `forbid`) stay enforced indefinitely offline; positive grants (Cedar scoped
  `allow`, ADR-0019/ADR-0022) expire after a signed `policy_max_staleness` and fail deny-closed.
- **B. Total deny-closed on TTL expiry.** After the TTL, the node stops governing
  entirely.
- **C. Never expire, only warn.**

### Q2 — Audit behaviour when the local disk budget is exhausted

- **A. Fail-closed default, opt-in degrade.** Default `block`: refuse new governed
  actions before losing evidence. Opt-in `degrade`: seal the segment and append a
  **signed, in-chain gap marker** so the loss is tamper-evident, never silent.
- **B. Always fail-closed.**
- **C. Always degrade.**

### Q3 — Bundle format unification

- **A. Extract `core/sigbundle` + a domain-tag registry.** Lift the OTA update envelope into
  a shared package; refactor `core/release` to consume it behind a byte-identical
  golden test; this DDIL work and the security-advisories feed add their own domain tags.
- **B. Leave `core/release` alone; each session copies the pattern.**

## Decision outcome

**Q1 → Option A (asymmetric).** Offline, past `policy_max_staleness`:

| Rule class | Offline, TTL expired | Rationale |
|---|---|---|
| ABAC deny | **still enforced** | a stale restriction can only restrict, never escalate |
| Cedar `forbid` (absolute, ADR-0022) | **still enforced** | same; forbid already overrides everything |
| Cedar positive grant / `allow` | **expired → deny-closed** | "an expired grant must never authorize" |
| Break-glass | available, its own 1h/24h expiry | the sanctioned offline escape hatch |

`policy_max_staleness` is an operator setting (default 72h) carried in the policy
bundle and signed; the console/CLI surface the age and the expiry prominently.

**Q2 → Option A (fail-closed default, opt-in degrade).** Config
`audit.spool.on_full`:

- `block` (default): new governed actions are refused (`503`, deny-closed); reads keep
  serving; console/CLI show "audit spool full — governance halted".
- `degrade` (explicit opt-in): seal the current segment and append a signed in-chain
  `audit.gap` marker `{from_seq, to_seq, reason: "spool_full", count, at}` so the chain
  stays continuous and the loss is provable. `audit.spool.max_bytes` is declared and
  documented.

The gap marker is the ONLY sanctioned discontinuity in the chain; the offline archive
verifier (`core/audit/archiveverify.go`) is extended to recognise a signed gap marker
as a *declared* boundary rather than a `seq-gap` failure.

**Q3 → Option A (extract `core/sigbundle`).** One envelope:

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` is refactored to reuse `sigbundle.SigningInput` with tag
`olivares.update-manifest.v1\n`, guarded by a golden test asserting
`release.ManifestSigningInput(b)` is byte-for-byte unchanged (so every already-issued
release signature still verifies). The **domain-tag registry** (a table + a
uniqueness/no-prefix-collision test) records every tag:

| Tag | Owner | Note |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | byte-identical after refactor |
| `olivares.ddil-bundle.v1\n` | this DDIL work | NEW — air-gap policy+audit+evidence bundle |
| `olivares.security-advisories.v1\n` | the security-advisories feed | NEW — signed OSV advisories feed |

`core/license` (bare `{`-leading JSON payload) and the audit event/checkpoint domains
(`olivares.audit.*`) remain provably disjoint from every tag (a tag never starts with
`{`, and the audit domains are length-prefixed preimages, not tar bundles).
`core/dr/bundle.go` is intentionally **left as-is**: it is a *sealed* (AES-GCM),
unsigned DR snapshot — a different trust model (confidentiality, not
publisher-authenticity) — and folding it in would conflate the two.

### Consequences

- **Good:** fail-safe in the right direction on both planes; one audited envelope and
  one domain-separation discipline instead of three; the edge keeps denying what was
  always denied even after a long outage; evidence loss is impossible-by-default and
  tamper-evident when explicitly permitted.
- **Bad / trade-offs:** positive grants stop working after `policy_max_staleness` on a
  truly long outage (mitigated by break-glass and by making the TTL an operator
  choice); the `degrade` mode trades evidence for availability and must be opted into
  consciously; refactoring `core/release` touches freshly-merged the OTA updater code (mitigated
  by the golden byte-identity test).
- **Neutral / follow-ups:** the security-advisories feed depends on `core/sigbundle` and its own tag; the
  archive verifier gains a `declared-gap` vocabulary; `docs/deploy/ddil.md` documents
  the disk budgets, the TTL, and what does not survive an infinite outage.

## Why the alternatives were rejected

- **Q1-B (total deny-closed):** a mission-kill. A downed link for longer than the TTL
  would halt an edge unit even though its deny rules were never in doubt.
- **Q1-C (never expire):** a grant revoked at the centre would stay live at the edge
  forever — an unbounded authorization window is unacceptable for a governance plane.
- **Q2-B (always fail-closed):** removes a legitimate operator trade-off (some edge
  missions must not halt); the signed gap marker already makes degrade honest.
- **Q2-C (always degrade):** a weak default for a governance product — silent-by-policy
  evidence loss is exactly what the ledger exists to prevent.
- **Q3-B (copy the pattern):** three envelope implementations and three chances to
  botch domain separation; the cross-protocol key-reuse lesson was precisely that one key over two message
  types without a tag is a forgery vector.

## Implementation note (2026-07-10)

Q2 is implemented as ratified. The gap marker declares the dropped range
`{from_seq, to_seq, count, reason, at}` as a sequence hole whose hash linkage
stays continuous, and the live chain verifier, the archive exporter and the
offline archive verifier all recognise a correctly-declared, correctly-signed
marker as a declared boundary (`declared_gaps` in their reports) while
continuing to fail on any undeclared or inconsistent discontinuity. The budget
measures the exact logical bytes of the stored event values via an incremental
counter recomputed from the ledger on every budgeted boot; integrity machinery
(checkpoints, archive anchors, the marker itself) is admitted over budget but
fully accounted, and the system plane is budget-governed like every other
writer.

A parallel implementation that kept the chain gapless (a summary marker with
no sequence hole, physical page/relation measurement, a system-plane
exemption) was integrated the same day and superseded by this one during
reconciliation: the ratified text specifies the declared range and the
verifier extension, and the exact counter removes the measurement hysteresis
and the modified-v3-migration issues of the physical approach. The superseded
variant remains in history for reference.

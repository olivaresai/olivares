<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Records management — retention, legal hold, and WORM archiving

**Date:** 2026-06-10 · **Status:** implemented. **Audience:** operators and compliance
officers deploying Olivares AI. **Feeds:** RTBF/erasure, recordings, and evidence.

> **Thesis.** Data disposition is only defensible if it is **documented** (policy with a legal
> basis), **approved** (human governance gate), and **repeatable** (certified sweep anchored to the ledger) —
> the three Sedona pillars. And a legal preservation overrides ANY purge: in the face of doubt or error, **nothing
> is deleted** (fail-closed). Releasing a hold is the dangerous operation: CRITICAL, dual-control, no
> break-glass. No record in this plane carries customer content: only classes, counts,
> references, and hashes (`docs/SECURITY-HARDENING.md`).

---

## 1. Model: data classes, policies, and dispositions

### 1.1 The class registry (fixed, in code)

The catalog lives in the binary (`modules/compliance/dataclass.go:78-137`), not in the database:
versioned like the capabilities catalog, verified kind by kind against the schemas of the owning
modules. A stored policy can never contradict it (the sweep re-queries it on every run,
`retention.go:516-517`).

| `data_class` | Covers (ext kinds) | Age | Purgeable | model_io | Recommended (advisory) |
|---|---|---|---|---|---|
| `agent.memory` | `knowledge.memory` | `updated_at` | yes | yes | own TTL (`expires_at`); age-based purge opt-in. Fine-grained subject: `agent` → `agent_ref` column (the ONLY fine-grained mapping in v1) |
| `session.timeline` | `sessions.live`, `sessions.timeline` | `updated_at` | yes | yes | 365 d |
| `voice.session` | `voice.session` | `updated_at` | yes | yes | 365 d |
| `finops.cost_sample` | `finops.cost_sample` | `created_at` | yes | no | 730 d |
| `knowledge.content` | `knowledge.base/document/chunk` | — | **no** (v1) | no | deletion via KB delete / RTBF; holds DO apply (§2.4) |
| `audit.ledger` | `audit_events` (core table) | — | **no** (never in v1) | no | 2555 d (7 years) + continuous WORM archiving (§4) |
| `evidence.append_only` | every module `AppendOnly` table | — | **no** (v1) | no | ≥ `audit.ledger`; see the §6.3 seam |

`session.timeline` and `voice.session` declare related subjects `agent`/`session` **without** a
uniform column: a subject-hold of those kinds skips the entire class (conservative over-preservation,
`dataclass.go:96-107`).

### 1.2 Inert until an explicit policy

**The engine purges nothing by default.** There are no active schedules out of the box: the `recommended_days`
are advisory and are only surfaced in `GET /retention/classes` (`dataclass.go:13-14,53-55`; "inert
until first rule" posture). Each tenant creates its policies; validation requires a registered class,
`1 ≤ retention_days ≤ 36500`, and `disposition=purge` only on purgeable classes
(`dataclass.go:152-171`). Non-purgeable classes accept `retain`: documenting the schedule is also
evidence.

**What committing to a schedule means** (a compliance decision by the operator): by issuing a `PUT`
of a policy you are declaring, with `basis` (legal/business basis, ≤2048 chars), that THAT class is
retained for N days and afterward is consistently retained or destroyed. That declaration is citable by
an auditor and by the opposing party in discovery — an approved schedule that is NOT executed
consistently is worse than none. The recommendations (audit 7 years; sessions/voice 365 d; costs
730 d) are the starting point, not the decision.

### 1.3 API and permissions

Under `/v1/m/compliance/` (`modules/compliance/compliance.go:572-591`). Permissions: viewer →
`compliance:retention:read`/`compliance:hold:read`; admin → `compliance:retention:admin`/
`compliance:hold:admin`. No write tier on purpose (`compliance.go:34-45`).

| Route | Perm | Semantics |
|---|---|---|
| `GET /retention/classes` | retention:read | Registry + recommendations + provider floor (§6.2) |
| `GET /retention/policies` · `GET /retention/runs` | retention:read | Schedules and disposition certificates |
| `PUT /retention/policies/{class}` | retention:admin | Upsert; enabling purge passes the gate (§3.1) |
| `DELETE /retention/policies/{class}` | retention:admin | No gate: STOPPING purging is the safe direction (`retention.go:397-399`) |
| `POST /retention/sweep` | retention:admin | Runs the tenant's sweep now, attributed to the admin (`retention.go:484-487`) |
| `POST /holds` · `POST /holds/{id}/release` | hold:admin | Create (immediate) / release (dual-control, §2.3) |
| `GET /holds[/{id}[/events]]` · `GET /holds/check` | hold:read | List, detail, custody, and the HTTP hold-gate (§2.4) |

## 2. Legal hold

### 2.1 Lifecycle and matching

`POST /holds` creates the hold **active immediately, without approval**: preserving is the safe direction
and the duty-to-preserve admits no wait (`holds.go:260-268`). It requires `matter_ref` + `reason`;
`scope_kind` ∈ `tenant` | `data_class` | `subject`, with cross-validation of fields
(`holds.go:275-302`). The matching rule is ONE and is shared by sweep, Go gate, and HTTP
(`holds.go:135-146`):

1. `tenant` ⇒ covers the ENTIRE tenant.
2. `data_class` ⇒ covers the exact class from the §1.1 registry.
3. `subject` ⇒ covers the exact pair (`subject_kind`, `subject_ref`).

A subject may be under several holds; disposition only proceeds when **none** covers it
(Sedona *both-must-clear* rule, like retention + legal hold in S3).

### 2.2 Chain of custody

Every transition (`set` → `release_requested` → `released`) seals, in the **same transaction**:
(a) an append-only `compliance.hold_event` row **anchored to the ledger head** at that instant
(`ledger_seq` + `ledger_hash`, `holds.go:162-189`), and (b) a semantic self-audit
(`compliance.hold.set` / `.release.request` / `.release`) hash-chained and signed per-event
(Ed25519). The row carries the real actor, `on_behalf_of` (delegation chain), and the `approvers` of the release.
The full trail is `GET /holds/{id}/events`. Set and release additionally emit findings
`compliance_hold_set` (medium) / `compliance_hold_released` (high) over the bus (`holds.go:59-62`).

### 2.3 Release: the dangerous operation

`POST /holds/{id}/release` (`holds.go:499-646`):

- Governed action `compliance.hold.release`, **CRITICAL by default**
  (`modules/governance/risktier.go`) ⇒ the governance engine requires **≥2 distinct human approvers** + SoD
  requester≠decider.
- **No break-glass**: the composition-root adapter runs over `gateOnceNoBreakGlass` — no
  emergency justifies releasing a legal preservation.
- Anti-TOCTOU: the approval is bound to the `PlanHash` of THAT hold with THAT scope
  (`holds.go:487-491,577`); a different hash ⇒ 403.
- **Independent re-verification of the quorum**: the module counts the distinct approving principals
  and denies an "approved" with <2, even if the gate asserts it (`holds.go:55,582-584` — defense in
  depth, erase-gate pattern).
- Outcomes: pending ⇒ **202** `{status:"pending_approval", approval_ref}` + custody event
  `release_requested` (ONCE per `approval_ref`, `holds.go:549-573`); expired ⇒ 409; no gate
  wired ⇒ 503 (deny-closed); rejected ⇒ 403; approved ⇒ 200 `released` with `released_by/at`,
  `release_approval_ref`, custody event `released` with the approvers, and a high finding.

### 2.4 What an active hold blocks TODAY

| Destruction path | Enforcement | Result under hold |
|---|---|---|
| Retention sweep (§3.2) | hold re-check WITHIN each batch (`retention.go:533-545`) | class skipped or rows excluded, certificate records it |
| `DELETE /v1/m/knowledge/kb/{id}` | subject `("kb", id)` + class `knowledge.content` (`modules/knowledge/kb.go:380`) | **423 Locked** |
| `POST /v1/m/knowledge/memory/purge` | class `agent.memory` (+ subject `agent` if filtered; per-row also subjects `user`/`session` of scoped rows) (`memory.go:467-547`) | 423 |
| `DELETE /v1/m/knowledge/memory/{id}` | subjects `("agent", agent_ref)` + `("user"/"session", row scope)` + class (`memory.go:422-430`) | 423 |
| Erasure RTBF (future) | same hold-gate, Go or HTTP, BEFORE the erase approval-gate (§6.1) | 423 |

The gate they consume: in Go, `compliance.(*Module).CheckHold(ctx, tenant, HoldSubject{Kind, Ref,
DataClass})` → `HoldDecision{Held, Holds}` (`holds.go:94-108`); over HTTP,
`GET /holds/check?subject_kind=&subject_ref=&data_class=` (`holds.go:446-468`). **Gate error =
DENY**: knowledge responds 503 "legal-hold check unavailable; deletion denied (fail closed)" — a 423
would assert a hold we cannot prove (`modules/knowledge/ports.go:188-204`). The 423 body is
exact and the RTBF erasure path adopts it verbatim (`ports.go:209-218`):

```json
{"error":{"code":"legal_hold","message":"blocked by an active legal hold",
          "holds":[{"id":"…","matter_ref":"…","scope_kind":"…"}]}}
```

## 3. Defensible deletion: the three Sedona pillars

| Pillar | How the product meets it |
|---|---|
| **Documented** | Policy per class with `basis` + `retention_days` + the §1.1 registry (the class declares what it covers and by which column it ages) |
| **Approved** | Enabling `disposition=purge` passes the governance gate: action `compliance.retention.enable`, HIGH by default (≥1 human + SoD; raisable to CRITICAL by approval policy), `PlanHash` bound to the exact schedule, no break-glass (`retention.go:40-47,204-206,232-260`) |
| **Repeatable** | The sweep runs ONLY `enabled` policies, with a hold-check per batch, and seals an append-only `retention_run` certificate + self-audit `compliance.retention.purge` per class with activity (`retention.go:599-603,619-675`) |

### 3.1 Enabling a purge (the governed act)

`PUT /retention/policies/{class}` with `disposition=purge` + `enabled=true`: pending ⇒ **202** and the
policy **is persisted with `enabled=false`** (the document remains, the purge does not start —
`retention.go:256-279`); an identical re-PUT within the approval window finds the grant
(`gateOnce` semantics) and responds 200 enabled with `approval_ref`. Rejected ⇒ 403, expired ⇒ 409,
no gate ⇒ 503; in all cases, the policy persists disabled. The approval is **at the
policy level**, not per run (Sedona: consistent disposition under an approved schedule); each run is
certified.

### 3.2 Sweep runbook

- **Manual:** `POST /v1/m/compliance/retention/sweep` — same code as the loop, attributed to the admin,
  returns the `RetentionSummary` (`retention.go:431-438,443-463`).
- **Engine loop:** leader-gated, enumerates orgs and calls `RunRetention` per tenant
  (`retention.go:470-472`). Env: `OLIVARES_RETENTION_SWEEP_INTERVAL` (default `24h`;
  `0` disables with a warning).

Order per class (`retention.go:514-613`): load active holds → `tenant`/`data_class` hold ⇒ skip
the whole class, certified → candidates = rows with age < cutoff (`OpLt` over the age column; the
RFC3339 fixed-width timestamps order lexicographically) → fine exclusion by mapped subject-hold;
related subject-hold WITHOUT a mapping ⇒ skip the whole class → delete by id in bounded batches (200×50,
`retention.go:54-56`) → certificate + audit in the final transaction. Holds are re-evaluated **within
each batch**: a hold placed mid-run halts the destruction from the next batch onward
(`retention.go:480-483`). If the process dies mid-way, the partial deletes are idempotent by
the age predicate; the next run re-counts. A run that hits the iteration cap seals
`truncated=true` and continues on the next.

### 3.3 How to read a certificate (`GET /retention/runs`)

Each `retention_run` row is append-only (`retention.go:98-114,619-675`): `data_class`, `trigger`
(`sweep`|`manual`), `cutoff` (age boundary applied), `examined`/`purged`/`excluded_held` (rows
preserved by subject-hold), `skipped_class_hold`, `truncated`, `policy_id` + `approval_ref` (the
chain up to the human approval), `ledger_seq`/`ledger_hash` (anchor to the ledger head), and
`manifest_hash` (sha256 of the run's canonical summary). The certificate + the signed self-audit ARE the
destruction log; routine runs generate no findings.

## 4. WORM archiving of the ledger

The ledger is not pruned: its multi-year history is resolved by **archiving** verifiable segments to an
immutable substrate ("you archive it, you don't change it").

### 4.1 Prepare the bucket

1. Create the bucket **with Object Lock enabled at creation** (which implies versioning — the connector
   requires it, `connectors/s3archive/s3archive.go:178`).
2. **COMPLIANCE** mode (connector default): no one — not even the account root — can shorten the
   retention or delete the version before retain-until (`s3archive.go:14-19`).
3. **Configure a default retention on the bucket**: with `retention_days=0` the connector relies
   on it, and the verify-after-write **fails** if the object ends up with NO protection at all
   (`s3archive.go:448-450` — a WORM sink that writes unprotected objects is a failure, not a warning).

Equivalences if your substrate is not S3 (only the S3 face is implemented and tested; Azure/GCS are
verified guidance as of 2026-06-10 for compatible gateways or future connectors):

| Substrate | WORM mechanism | Gotcha |
|---|---|---|
| AWS S3 / MinIO | Object Lock COMPLIANCE / GOVERNANCE + legal hold | GOVERNANCE is bypassable with `s3:BypassGovernanceRetention` — and the **AWS console applies the bypass header by default** when deleting with that permission: governance is NOT real WORM against an admin. Use COMPLIANCE |
| GCS | Retention policy (lockable) + event-based / temporary hold | Releasing an **event-based hold resets the clock**: retention counts from release, not from object creation |
| Azure Blob | Immutability policy (time-based + legal hold) at container level | **Version-level** immutability exists but we have NOT audited it; treat it as unverified |

### 4.2 Config of the `olivares.s3archive` connector

Fields (`s3archive.go:175-189`): `endpoint` (empty ⇒ AWS virtual-host; custom ⇒ path-style, MinIO),
`region`*, `bucket`*, `prefix`, `access_key_id`*, `secret_access_key`* (Secret), `session_token`
(Secret), `lock_mode` (default `compliance`), `retention_days` (0 ⇒ bucket default),
`legal_hold`, `verify_lock` (default `true`), `format` (Notify face), `max_attempts` (default 4).
`Open` validates EVERYTHING at provisioning: a misconfigured WORM sink fails to open, never to archive
(`s3archive.go:193-270`). Every PUT carries SigV4 with all `x-amz-*` headers signed,
`Content-MD5` (mandatory with object-lock), and the lock headers; with `verify_lock`, a signed HEAD
must confirm the protection or the Put is an **error** (`s3archive.go:404-452`). A re-PUT of the same
content to the same key = another locked version (idempotent recovery, harmless).

**Secrets live outside the store**: the loop reads the connector config from a JSON file pointed to
by `OLIVARES_AUDIT_ARCHIVE_CONFIG` (precedent `OLIVARES_NOTIFY_CONFIG`), never from tenant settings.

### 4.3 Envs of the archiving loop (fixed names)

| Env | Default | Meaning |
|---|---|---|
| `OLIVARES_AUDIT_ARCHIVE_SINK` | `""` (off) | `dir` \| `s3archive` |
| `OLIVARES_AUDIT_ARCHIVE_DIR` | — | root of the `dir` sink (files `0o444`; WORM if the substrate is, `core/audit/dirsink.go:62-69`) |
| `OLIVARES_AUDIT_ARCHIVE_CONFIG` | — | JSON file with the connector config (secret-bearing) |
| `OLIVARES_AUDIT_ARCHIVE_INTERVAL` | `24h` | loop periodicity (leader-gated) |
| `OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS` | `10000` | events per segment |
| `OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS` | `2555` | retain-until requested per object (7 years) |

The loop drains per tick everything pending per tenant; its bookkeeping (`audit.archive.last_seq` in
`Org.Settings`) is recoverable, NOT evidence — the evidence is the manifest + the anchored event.

### 4.4 What a segment / manifest / keys.json is

- **Segment** (`<tenant>/seg-<from%012d>-<to%012d>.jsonl`): one JSON line per event with EVERYTHING
  needed to re-derive the chain hash offline — including the **stored canonical meta**, which
  the SIEM export and `/v1/audit/export` do not carry (the gap this closes)
  (`core/audit/archive.go:41-55`). On building it, the export re-derives each hash and checks
  linkage/gap-freedom: a corrupt range fails HERE, it is never sealed into a WORM archive
  (`archive.go:140-163`).
- **Manifest** (`….jsonl.manifest.json`): format `olivares.audit.archive.v1`, range, count,
  `first/last_hash`, `events_sha256` of the JSONL, and `prev_segment_last_hash`, which chains segments
  to one another — multi-year continuity without external state (`archive.go:62-73`).
- **`keys.json`** (archive root): the engine's pubkeys, **advisory** — an archive written by a
  compromised host carries the attacker's keys, so the verifier's pins REPLACE them
  (`archive.go:409-431`, `archiveverify.go:347-351`). Pin off-box copies of the keys.
- **Cross-anchoring**: after writing and verifying each segment, the loop appends
  `audit.archive.segment` (meta: range, sha256, key, lock receipt) — the archive is anchored
  WITHIN the chain it archives, and the next segment contains that event
  (`archive.go:24-30,367-405`).

## 5. Multi-year export and verify (CLI)

### 5.1 Operator / air-gap export

```sh
olivares audit archive export --tenant <T> --out <DIR> \
  [--from-seq N] [--segment-events N] [--data-dir D | --engine postgres --dsn …]
```

Read-only against the ledger: it does **not** append anchors (that belongs to the loop §4.3; an offline
export must not mutate the chain it drains — `cmd/olivares/cmd_audit.go:311-316`). It writes segments +
manifests + advisory `keys.json` and reports the exported range; it resumes with `--from-seq <to_seq+1>`.

### 5.2 Offline verify

```sh
olivares audit archive verify --dir <DIR> [--strict] \
  [--pubkey <spec>]… [--pubkey-alg A] [--event-pubkey <b64>]…
```

Pure offline: no store, no network (`archiveverify.go:98-99`). Constant memory per segment (streaming
— unlike the checkpoint verification of the hot store, which is O(chain)). Without pins it uses the
advisory `keys.json`; **with pins, the pins REPLACE the keys.json**. By default it exits 0 and reports in
JSON; `--strict` makes the exit code fail (cron/CI). What each check guarantees:

| Check | Guarantees | Reasons |
|---|---|---|
| Canonical re-derivation per event | the archived content IS the one the chain hash committed | `hash-mismatch`, `bad-line` |
| Linkage + gap-freedom | nothing inserted, deleted, or reordered within the segment | `prev-mismatch`, `seq-gap` |
| Manifest vs JSONL | the file intact byte by byte | `count/first-hash/last-hash/events-sha256-mismatch` |
| Continuity between segments | a deleted/replaced segment cannot hide | `segment-gap`, `segment-link-mismatch` |
| Name vs range | a renamed segment cannot impersonate another range | `key-mismatch` |
| Per-event / checkpoint signatures (with pins) | engine authorship; the checkpoint attests the head O(1) | `event-sig-invalid/-missing`, `checkpoint-sig-invalid` |

(Full vocabulary: `core/audit/archiveverify.go:41-47`.)

**On a verify failure:** the report locates the FIRST failure (`BreakTenant`/`BreakSegment`/
`BreakAt` + reason). Do not "fix" the archive: under COMPLIANCE you cannot and must not. Treat the archive
as incident evidence, verify the hot store (`olivares audit verify`) and another copy of the
archive to triangulate whether the corruption is in the archive, the transport, or the source;
`events-missing`/`manifest-missing`/`segment-gap` point to an incomplete copy (re-download the range); a
`*-sig-invalid` with correct pins points to a write by a foreign key — a security incident, not a
storage one.

## 6. Erasure ↔ retention reconciliation

### 6.1 The hold vetoes erasure

Before ANY RTBF erasure, the erasure path calls the hold-gate (Go `CheckHold` or HTTP `/holds/check`, with
a `compliance:hold:read` service token) with the data-subject's subject and the affected class(es).
`Held=true` **or error** ⇒ erasure does NOT proceed: **423** with the exact body of §2.4. The hold-gate is
checked BEFORE and IN ADDITION TO the `compliance.content.erase` approval-gate — two independent
gates, both deny-closed.

### 6.2 The provider floor (Covered Models)

Verified 2026-06-10: **Fable 5 and Mythos 5 are Covered Models** — forced retention **≥30 days** at
the provider, **no ZDR** (a ZDR org receives 400 when invoking them), effective 2026-06-09
(`modules/models/reference.go:37-102` — the `retentionCovered` class and floor; `:291-319` — the
Fable 5 / Mythos 5 catalog entries). Operational consequence: **deleting our copy does not delete the
provider's**. For the `model_io` classes, the API annotates `provider_floor_days`,
`provider_floor_source`, and `effective_disclosure_days = max(retention_days, floor)` — the number a
tenant can honestly promise as "gone everywhere" (`retention.go:60-67,93-95`).
The semantics are **annotate, not reject**: deleting before the floor is legitimate; promising total
deletion before the floor is not. **Erasure receipts must cite this floor.** Without a wired
adapter, `provider_floor_known=false` — honest, never fabricated (`retention.go:119-125`).

### 6.3 The append-only / DropTenant seam (documented, future)

The append-only tables (ledger, custody, certificates — the `evidence.append_only` class) **are not
purgeable in v1**: `DropTenant` explicitly retains them, "purged only via the separate retention
path" (`core/internal/store/sqlstore/system.go:238-270`). That privileged path is future work.

**Operational gap to know:** `DropTenant` **does not consult holds** — it deletes all the tenant's
mutable tables without passing through the hold-gate. Rule of operation until the seam exists: **before a
DropTenant, check the tenant's `GET /holds?status=active`**; with any active hold, the drop does not
proceed. (The append-only rows survive the drop, so custody and the ledger are preserved in
any case — but the mutable content under hold is not, hence the rule.)

---

**References:** code
`modules/compliance/{dataclass,retention,holds}.go`, `modules/knowledge/ports.go`,
`core/audit/{archive,archiveverify,dirsink}.go`, `connectors/s3archive/`,
`cmd/olivares/cmd_audit.go` · security posture `docs/SECURITY-HARDENING.md`.

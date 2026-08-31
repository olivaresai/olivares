<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Disaster Recovery runbook — Olivares AI control plane

**Date:** 2026-06-09 · **Status:**
implemented and tested (SQLite end-to-end; Postgres via documented standard
mechanism, see §9 Honest limits).

> **Thesis.** A product whose integrity IS a per-event hash-chained and signed
> ledger cannot treat backup/restore as a naive `pg_dump`. Restoring
> must **preserve chain continuity** and **key custody**, and
> the restore must **prove** that it did (`/v1/audit/verify` green post-restore).
> This runbook is the procedure; the tool is `olivares dr`.

---

## 1. Why a signed ledger needs more than a dump

The ledger (`core/audit`, `docs/SECURITY-HARDENING.md`) is, per tenant, an append-only chain with
hash chaining where **each event carries an Ed25519 signature** over its chain
hash, periodically anchored by **signed checkpoints**. Three hazards are
exclusive to restoring it:

1. **Key omission.** The **per-event** signing key ALWAYS lives on-box (the
   hot path is never routed off-box, not even with KMS checkpoints —
   `core/audit/eventsig.go`). If you restore the store **without** that key, **all**
   per-event signatures fail; worse: a clean boot mints a **new** key and
   subsequent events chain under it with no rotation record. The key is an
   **inevitable** part of the DR set.
2. **Inconsistent snapshot.** Tail-truncation detection depends on the
   `audit_heads` row matching the last row of `audit_events`. A per-table copy or
   a file copied hot without consistency turns a recoverable restore
   into a (correctly) detected break.
3. **Silent incomplete restore.** A restore that loads a bundle older or
   more partial than expected is cryptographically valid on its own — it takes an
   out-of-band assertion of the expected tip to know the restore is complete.

`olivares dr` answers all three: the **manifest** records per tenant the tip
(seq+hash) and the key fingerprint at the moment of the backup and **refuses to certify**
a backup whose chain is not already green; the **bundle** carries the signing keys
**encrypted** under an operator KEK; and `dr restore`/`dr verify` **re-verify**
chain + per-event signatures + checkpoints against the restored store **and** check
that the tip and the key fingerprint match the manifest.

---

## 2. The DR bundle (what is backed up)

A bundle (`*.drbundle`) is a `tar.gz` with a fixed layout:

```
manifest.json      control record (NOT secret): tips per tenant, public key
                   fingerprints, snapshot method and digest, instant (RPO).
keys/kek.json      KDF parameters to re-derive the KEK (Argon2id salt; no secret).
keys/*.key.enc     each signing key (audit + catalog), AES-256-GCM under the KEK.
store/<snapshot>   the consistent store snapshot (absent in PITR mode).
```

- **Minimal data:** EVERY `*-signing.key` in the data dir (audit, catalog,
  policy, and any future signing-key class — captured by the glob, pinned by the
  backup-inventory test) — NOT the TLS material, the setup-token, or the license
  (provided by the deployment).
- **The key fingerprint is the public one** (one-way); the private key never appears in
  the manifest, only encrypted in `keys/*.enc`.

---

## 3. Key custody (the secure heart)

- The KEK is supplied by **the operator**: a **passphrase** (Argon2id, memory-hard) or a
  **raw 32-byte key** (the KMS-wrapped path). The
  bundle never contains the KEK; an incorrect passphrase **fails the GCM tag** (authenticated
  error, never a silently wrong key).
- **Without the KEK there is no verifiable restore.** Keep it **separate** from the bundles
  (secrets manager / KMS / sealed envelope). Whoever holds a bundle **and** the KEK
  can re-sign the ledger: treat it as the signing key itself.
- **KMS/HSM seam.** Today the on-box keys are encrypted with the operator KEK.
  Later, the KEK can be a key wrapped by KMS/HSM (mode
  `--kek-key-file`), and checkpoints can already be signed off-box today
  (`OLIVARES_LEDGER_SIGNER`, `docs/SECURITY-HARDENING.md`). The **per-event** signature will stay on-box by
  design, so the `audit-signing.key` encrypted in the bundle remains necessary.
- **3-2-1.** A backup on the same host/cluster **is not** disaster recovery: copy the
  bundles **off-site**. `dr backup --offsite-bucket …` replicates each bundle to an
  S3-compatible target (AWS S3, Cloudflare R2, MinIO, Wasabi) in the same run; `dr
  push`/`dr pull`/`dr list --offsite` manage the mirror. Offsite credentials are
  passed **by reference** (a file or the standard `AWS_*` env), never inline. A push
  failure **fails** the backup — the operator is never left with only a local copy.

---

## 4. DR strategy and where this runbook fits

Mapping to the AWS DR whitepaper (*Disaster recovery options in the cloud*):

| Strategy | RPO/RTO | What it provides |
|---|---|---|
| **Backup & Restore** | hours → minutes | **this runbook** — bundles + verified restore |
| Pilot Light / **Warm Standby** | minutes | HA active-passive (Postgres leader election, intra-region) — implemented; the operator `LeaderRouting` path awaits its recorded qualification run (`docs/HA-LEADER-ROUTING.md`) |
| Region-scoped residency (not a DR tier) | — | implemented: each tenant's data lives in exactly one regional backend, limiting blast radius and fixing data location (`docs/MULTI-REGION-RESIDENCY.md`); HA is intra-region |
| **Multi-site active/active** | ~zero | **not implemented / planned** — requires cross-region replication of the same tenant, conflict semantics and a tested failover; nothing today keeps a second active copy of a tenant |

Verified backup/restore is the **foundation**: without it being correct and tested, the
upper tiers have nothing to recover from.

---

## 5. Target RPO / RTO per tier

| Tier | Mechanism | Target RPO | Target RTO | Notes |
|---|---|---|---|---|
| SQLite (single-node/dev/air-gap) | `dr backup` (VACUUM INTO) by cron | = cron interval (e.g. 15 min–6 h) | < 15 min (small estate) | online snapshot, no downtime (WAL allows a concurrent reader) |
| Postgres (scale) — logical | `pg-dump.sh` by cron | = cron interval (e.g. 1–6 h) | < 30 min | consistent dump; version-tolerant restore |
| Postgres (scale) — PITR | `pg_basebackup` + WAL archiving | ≈ seconds (`archive_timeout` + lag) | < 30 min | near-zero RPO; companion bundle `--pitr-ref` |

**RPO** in a disaster = (instant of the disaster − `created_at` of the last good bundle;
manifest field, visible with `dr inspect`). **RTO** = restore snapshot + verify
+ boot. **Measure both in every DR drill (§8) and record the real numbers here —
do not invent them.**

**Measured RTO (SQLite, `task dr:drill` on the reference build container, 2026-07-09).**
The restore is verify-bound — the full ledger re-verification is a per-event Ed25519
check plus the tip-continuity comparison — so RTO scales with the event count on top
of a fixed engine-boot cost:

| Ledger events restored | Measured RTO (restore + boot + verify) |
|---|---|
| 500 | ≈ 0.15 s |
| 2 000 | ≈ 0.24 s |
| 5 000 | ≈ 0.44 s |

**Re-measured 2026-07-15** (SQLite, `go run ./cmd/olivares dr drill --events N`, same reference build
container, go 1.26.4, 16 vCPU), extended to a larger tier — the curve is ~linear in the event count on top of
a fixed boot cost, confirming the restore is verify-bound:

| Ledger events restored | Measured RTO (restore + boot + verify) |
|---|---|
| 500 | 123 ms |
| 2 000 | 211 ms |
| 5 000 | 388 ms |
| 10 000 | 663 ms |
| 20 000 | 1.257 s |

These are the real numbers a single drill printed on this container, not a target.
On production hardware with a larger estate the boot + per-event term dominate; re-run
`task dr:drill` on YOUR host and record the number your procurement pack cites. The consolidated day-2 drill
evidence (upgrade, key-rotation posture, ledger recovery, support-bundle) lives in
[docs/DAY2-DRILL-LOG.md](DAY2-DRILL-LOG.md).

## 5.5 Retention (GFS)

`--retain-days N` is a flat age cut. For a professional retention curve use
**Grandfather-Father-Son** on the backup: `--gfs-daily 7 --gfs-weekly 4 --gfs-monthly
12 --gfs-yearly 3 [--gfs-keep-last 3]` keeps the newest bundle of each of the last 7
days, 4 ISO weeks, 12 months and 3 years, plus an absolute floor of the 3 newest. When
any `--gfs-*` tier is set it supersedes `--retain-days` and prunes **both** the local
directory **and** the offsite mirror in the same run. The policy is a pure, tested
function (`core/dr/retention.go`), so the local and offsite decisions are identical.

---

## 6. Backup (procedure)

### SQLite (direct CLI)
```sh
printf 'a strong DR passphrase' > /run/secrets/dr-pass     # KEK; outside the repo/image
olivares dr backup --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file /run/secrets/dr-pass
```
Safe to run with `serve` up (VACUUM INTO is a concurrent reader; it does not
open the live engine).

### With offsite replication + GFS retention (recommended)
```sh
olivares dr backup --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file /run/secrets/dr-pass \
  --offsite-endpoint https://<acct>.r2.cloudflarestorage.com \
  --offsite-bucket olivares-dr --offsite-region auto --offsite-prefix prod \
  --offsite-access-key-id-file /run/secrets/r2-akid \
  --offsite-secret-access-key-file /run/secrets/r2-secret \
  --gfs-daily 7 --gfs-weekly 4 --gfs-monthly 12 --gfs-keep-last 3
```
The bundle is written locally, streamed off-box (the "1" of 3-2-1), and both copies
are pruned to the GFS curve. Omit `--offsite-endpoint` for AWS S3 (derived from the
region); the `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` env are honoured as a
fallback to the `*-file` flags.

### Postgres (logical) and PITR
Logical backup requires both DSNs: `OLIVARES_DSN` is the NOBYPASSRLS application
role used to boot the engine, while `OLIVARES_ADMIN_DSN` is the dedicated,
NOSUPERUSER BYPASSRLS + read-only role used by `pg_dump` and the cross-tenant
manifest inventory. Without the latter, FORCE RLS makes the dump fail or makes the
tenant inventory incomplete. See `deploy/postgres/README.md` and
`deploy/postgres/backup/{pg-dump.sh,pitr-setup.md}`.

### Kubernetes / Compose
- Helm: `--set backup.enabled=true --set backup.kekSecret=dr-kek` (CronJob; PG with
  a postgres-client initContainer for `pg_dump`; Postgres also requires
  `--set postgres.adminDsnKey=admin-dsn`). See `deploy/helm/README.md`.
- Compose: `docker-compose.backup.yml`, profile `backup`. See `deploy/compose/README.md`.

The backup **aborts** if some tenant chain does not verify at the moment of the backup
(a corrupt ledger is not captured as if it were a good restore point); use
`--allow-unverified` only knowingly.

---

## 7. Restore (procedure) + continuity verification

```sh
# Restore into a FRESH data dir (the empty-target path):
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file /run/secrets/dr-pass
# (offsite: olivares dr pull --name <bundle> --out /tmp/b.drbundle --offsite-bucket … first)
# (Postgres: add --dsn=… to the EMPTY target; use deploy/postgres/backup/pg-restore.sh)
```

`dr restore` does, in order:
1. **Extracts** the bundle and checks the snapshot **digest** against the manifest
   (detects a corrupt/tampered bundle).
2. **Decrypts** the signing keys with the KEK and installs them 0600 in the data-dir
   (fail-closed on overwrite unless `--force`).
3. **Restores** the store snapshot (SQLite: copies the file; Postgres: `pg_restore`;
   PITR: skips — the store was recovered out-of-band by WAL replay).
4. **Boots** the engine and runs `RestoreVerify`: for each tenant in the manifest,
   chain (`Verify`) + per-event signatures (`VerifyEvents` against the restored key)
   + checkpoints + **tip == manifest** + **key fingerprint == manifest**. **Exits with
   a non-zero code if the restore is NOT continuity-safe** (do not resume writes).

### Restoring OVER a live data dir — `--in-place` (staged, atomic, self-preserving)
When you cannot take the data dir empty first (recovering a corrupted-but-running
node), add `--in-place` (SQLite):
```sh
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file /run/secrets/dr-pass --in-place \
  --operator "you@example.com" --reason "INC-42 ransomware recovery"
```
It **stages** the restored keys + store in a sibling dir on the same filesystem,
**boots and re-verifies that staged ledger BEFORE touching production**, and only on
a green verify **promotes**: it first moves the current store/keys aside as
`*.pre-restore-<ts>` (an automatic pre-restore backup) and then renames the staged
files into place atomically, rolling back on any promotion error. **A failed verify
leaves the live data dir completely untouched** — the destructive operation is
transactional. Remove the `*.pre-restore-<ts>` files once you are satisfied.

### Who is allowed to restore — and where that control does and does not reach

The console has a **dual-control restore** switch (Backups → Schedule): with it on, a
restore started **from the console** is held until a **second, distinct administrator**
approves it. Two things about it have to be stated plainly, because the product used to
imply otherwise:

- **Turning it off is not immediate.** The request is recorded, stays visible in the
  schedule as `dual_control_disarm_effective_at`, and takes effect **one hour later**;
  until then the gate still holds, and re-enabling cancels it. Otherwise one
  administrator could switch the control off and restore in the next request, which is
  not a two-person control at all. Strengthening is immediate; only weakening waits.
- **And waiting is not a way past it.** A delay on its own is a one-person control with
  patience, so the disarm **never takes effect for the administrator who requested it**
  (`dual_control_disarm_requested_by`). Once the hour passes the gate is off for the
  estate — **any other administrator restores unencumbered** — and it keeps holding
  against the person whose own request opened it, until someone **re-arms** the switch.
  That is what makes it a two-person control rather than a wait.
  - Requiring two people *to disarm* was considered and rejected: with the gate armed
    and the second administrator gone, the estate could then neither restore nor
    disarm — a permanent lockout in exactly the disaster the control exists for. The
    estate is never locked here, and a genuinely solo operator still has the host path
    below.
  - What it does not reach, said plainly: an administrator who can **create** another
    superadmin can always manufacture a second person. That is true of every dual
    control in this product; the difference is that minting an admin is a loud,
    recorded act, and flipping a boolean and waiting was not.
- **It does not reach this command.** `olivares dr restore` runs on the host, has no
  session and no principal, and **`--engine postgres` can ONLY be restored this way** —
  the console refuses a Postgres restore and points here. So on a Postgres estate the
  console switch governs no restore at all.

Because of that, a `dr restore` that would **REPLACE an existing estate** refuses unless
you declare who is doing it and why:

```sh
--operator "you@example.com" --reason "INC-42 ransomware recovery"
```

A restore counts as replacing an estate when **any** of these holds:

| Signal | Engine |
|---|---|
| the target data dir already holds this estate's `*-signing.key` files | both |
| the SQLite store file already exists, or `--in-place` was passed | sqlite |
| the target **database** already holds relations of its own | postgres |
| the target database **could not be read** — an unreadable target is not an empty one | postgres |

The last two are why a Postgres restore no longer slips through: a Postgres estate lives
at the far end of a DSN, and under **external key custody (BYOK/CMEK) the data dir is
legitimately empty**, so a filesystem-only check called every live database a clean
target. Restoring into a genuinely **empty** database still needs neither flag.

`pg_restore` is also run **`--single-transaction`** now, so a Postgres restore that hits
anything already in the target fails **whole**: **none of the backup's objects or rows are
written**. Without it, a restore that exits non-zero could still have written part of the
backup into a live database — measured on PostgreSQL 16, rc 1 with the backup's rows
inserted into the pre-existing tables. The cost is that a restore which used to limp to a
partial success now fails outright, which is the intended trade: a DR restore that
"mostly worked" leaves a ledger that is neither the old estate nor the backup.

> **"Single transaction" is not "the target is untouched", and the difference is
> measured.** A few PostgreSQL effects are not transactional and survive the rollback —
> **sequence advances** (`nextval`/`setval`) above all, and whatever a **pre-existing event
> trigger** does while the restore runs. An external contrast forced exactly that: an event
> trigger on the target bumped a live sequence, `pg_restore` exited 1, the dump's table was
> rolled back and **the sequence stayed advanced**. So the guarantee to rely on is the one
> stated above — no backup object, no backup row — and after any failed Postgres restore you
> should still check the target rather than assume it is pristine.

Both flags are sealed into the **restored estate's own audit ledger** (`dr.restore.cli`),
because that is the only ledger that survives the restore. The event also carries
`bundle_dual_control_restore` — the dual-control setting **as recorded in the bundle**,
which is *not* the setting of the estate being replaced: by the time the record can be
written, that estate is gone. If the record cannot be written, the command **fails**: a
destructive restore outside the two-person gate must not end in silence. Outside
`--in-place` the previous store and keys are moved aside as `*.pre-restore-<ts>` first,
so a failure after that point is recoverable.

> **This is a declaration, not an authentication.** Nothing checks the name you type.
> The real boundary on this path is **who can reach the host filesystem and the KEK** —
> anyone holding both can destroy the estate without this command, so control host
> access accordingly. What the flags buy is that the control cannot be bypassed
> *without knowing*, and that the act is no longer silent. A restore into a **clean**
> target destroys nothing and needs neither flag.

**Independent confirmation** (recommended after starting the service):
```sh
# via API:
curl -s https://host:8443/v1/audit/verify -H "Authorization: Bearer $TOK" -H "X-Olivares-Tenant: $TENANT"
# or via CLI, with the OFF-BOX key pinned (attacker-resistant check, docs/SECURITY-HARDENING.md):
olivares audit verify --tenant $TENANT --pubkey <base64> [--pubkey-alg …]
```

---

## 8. DR drill (test the backup, do not just have it)

An untested backup is not a backup. Two drills, escalating in fidelity:

**a) Verify an EXISTING bundle** (proves a specific bundle is restorable), no prod:
```sh
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle --passphrase-file /run/secrets/dr-pass
# SQLite: restores + verifies the full chain in a disposable dir → "DR drill PASSED".
# Postgres: checks digest + that the keys decrypt; the full chain
#           verification requires restoring to a scratch Postgres (see §9).
```

**b) Full round-trip drill with a MEASURED RTO** (proves the whole pipeline), no prod:
```sh
task dr:drill                 # or: olivares dr drill --events 1000
# Seeds an ephemeral signed+checkpointed ledger → backs it up → DESTROYS the estate →
# restores into a clean dir → re-verifies chain + per-event signatures + checkpoints +
# tips → prints "DR drill PASSED — restored N events" and "measured RTO: …". It uses a
# throwaway scratch dir (never a real data dir), so it is CI-safe (it runs in the
# nightly drills workflow). Exit ≠ 0 = incident.
```
Record the result and the measured RTO (§5). A failed drill is an incident.

---

## 9. Failure modes and how they are detected

| Failure (DR done wrong) | Detection |
|---|---|
| Restore the store **without the key** (or the engine mints a new one) | `VerifyEvents` → `event-sig-invalid` **and** key fingerprint ≠ manifest. `dr restore` exits ≠ 0. |
| Restore with a **wrong key** | same as above (double detection: signatures + fingerprint). |
| **Inconsistent snapshot** (head/tail mismatched, torn copy) | `Verify` → `tail-truncated`/`head-mismatch`. |
| **Incomplete restore** (old/partial bundle, exact mode) | restored tip ≠ manifest tip → hard failure (SQLite). |
| **Corrupt/tampered bundle** | snapshot digest ≠ manifest → restore rejected. |
| **Tamper** of a row after restoring | `Verify` → `hash-mismatch` (the ledger stays tamper-evident after the restore). |
| **Wrong passphrase/KEK** | the AES-GCM tag fails → authenticated error, never a silently bad key. |

Everything above is covered by tests (`core/dr/*_test.go`,
`cmd/olivares/cmd_dr_test.go`).

### Honest limits
- **Postgres is not tested live in this environment** (there is no PG; the PG tests
  skip). The mechanism (`pg_dump`/`pg_restore`/PITR) is the standard
  supported one; the manifest/verification logic is **engine-agnostic** and IS tested
  over SQLite, so the continuity guarantee is identical once PG is restored.
- **Tip-match in Postgres is "advisory"** (the manifest is built from the live
  store, which can run ahead of the dump because of the online backup window). The
  verification of chain/signatures/checkpoints over the restored data is the real
  guarantee; the tip lag is reported as an RPO window, not as a failure.
- **`ListOrgs` in Postgres without `--admin-dsn`** runs RLS-limited and may **omit
  tenants** from the manifest → provision the BYPASSRLS role and pass `--admin-dsn` for a
  complete backup (the backup warns if it lacks it).
- **Offsite replication is not wire-tested against a live bucket in CI** (there is no
  S3/R2 in the build container). What IS proven here: the AWS SigV4 signer against the
  AWS-published S3 "GET Object" worked example (a known-answer test, not a self-check),
  the full push→list→pull→delete round trip against an in-process mock S3, and the GFS
  retention algebra. A real-bucket smoke (point `--offsite-endpoint` at a scratch R2/S3
  bucket and run `dr push` + `dr list --offsite`) is the operator's one-time acceptance
  step; the signing and protocol are the standard, stdlib-only SigV4 path.
- **`--in-place` is SQLite-only.** Postgres restores into a live database with
  `pg_restore` (the DB engine's own transaction is the atomicity boundary there); the
  staged-and-promote flow is for the single-file SQLite store.

---

## 9.5 The egress writer fence after a restore

The writer fence is enforced by **database objects** — a trigger per governed table, plus a
function on PostgreSQL — while its **disposition** lives in a row (`control_rollout_state`). A
recovery can separate the two, and when it does, the deployment reports `ARMED` and enforces
nothing. That is the one failure mode this control has that the ledger's own verification cannot
see, because no row is missing or altered: the *rules* are.

**The check, after every recovery that produces a writable database:**

```sh
olivares eventing fence verify \
  --engine postgres --dsn "env:DATABASE_URL" --owner-dsn "env:DATABASE_OWNER_URL"
```

It does not read a catalog. It attempts the exact mutation the fence exists to stop — a
subscription carrying no capability attestation — inside a transaction it always rolls back, and
exits non-zero unless the engine refused it. A catalog query would report an object that exists;
only a refusal reports a fence that works.

| Recovery path | What happens to the fence | What to do |
|---|---|---|
| **`dr restore` into a fresh data dir** (SQLite) | The bundle carries the whole file, triggers included. | `fence verify`. It should report enforcing. |
| **`pg_restore` of a full dump** | `pg_dump` emits triggers and functions, so a complete restore carries them. A **data-only** restore (`--data-only`, or `--table` selections) carries the rows and NOT the rules — including the row that says `ARMED`. | `fence verify`. If it reports `MISSING`, restart a node against the database: the module's file migrations re-create the objects idempotently (`CREATE TRIGGER IF NOT EXISTS` / `CREATE OR REPLACE FUNCTION`), then verify again. |
| **PITR** (base backup + WAL replay) | Replays physical changes, so the objects come back with everything else. If the recovery target is a point **before** the fence's migrations were applied, the objects are absent and the rollout row is absent with them — consistent, and classified again at the next boot. | Boot a node, then `fence verify`. A target before the migrations gives a deployment with no fence; re-arm deliberately. |
| **Logical replication subscriber** | Rows replicate; the fence trigger is a **normal** trigger and deliberately not `ENABLE ALWAYS`, so a subscriber does **not** enforce it while applying. This is intentional: an `ALWAYS` trigger would reject every replicated governed row, because an apply transaction has no attestation of its own, and would break replication of the whole table. | Nothing while it is a subscriber. **On promotion it becomes an authoring writer**, and the promotion ceremony must run `fence verify` before it serves. This is the one case where the objects are present and correct and the guarantee still needs re-establishing. |
| **Restoring onto a node running an older binary** | The older binary does not carry the gate, so its own writes are refused by name. | Expected, and the point. Roll the binary forward; do not disarm — there is no disarm (see `docs/UPGRADE-AND-ROLLBACK.md` §5). |

**What this does not cover, stated rather than implied.** `fence verify` is a check an operator
runs; nothing verifies the objects **at every boot**. A trigger dropped or disabled between two
recoveries would not stop a node from starting: the migration stays recorded as applied
(`core/migrate/migrate.go`) and the engine's boot self-test checks the tenant guard, not module
triggers (`core/internal/store/sqlstore/selftest.go`). Closing that needs a generic extension of
the boot self-test, which is a known and tracked limitation rather than an oversight. Until it
lands, the verification points are **arming** and **recovery**, and silent drift between two
boots is not detected.

---

## 10. What it unlocks / next steps

- **HA:** Postgres leader election is implemented (intra-region); the operator's opt-in
  `LeaderRouting` deployment path awaits a recorded qualification run
  (`docs/HA-LEADER-ROUTING.md`).
- **Multi-region residency:** implemented as region-scoped instances
  (`docs/MULTI-REGION-RESIDENCY.md`) — a data-location control, not a DR tier;
  cross-region replication of a single tenant (the active/active tier) is future work
  that would build on this base.
- **BYOK/HSM:** the bundle KEK wrapped by KMS/HSM; off-box checkpoints (already
  available via `OLIVARES_LEDGER_SIGNER`).
- **SLO/procurement:** these RPO/RTO + the DR drill feed the SLOs and the
  procurement package (DORA/financial requires a DR plan that is defined **and tested**).

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
that the tip and the key fingerprint match the manifest. New bundles also bind the
complete manifest and every non-manifest payload with `hmac-sha256-kek-v1` under that
operator KEK, so metadata and payload tampering is rejected before restore writes.

---

## 2. The DR bundle (what is backed up)

A bundle (`*.drbundle`) is a `tar.gz` with a fixed layout:

```
manifest.json      control record (NOT secret): tips per tenant, public key
                   fingerprints, snapshot method and digest, instant (RPO),
                   per-file digest inventory and keyed authentication tag.
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
- **Every non-manifest payload is inventoried by path, size and SHA-256.** The manifest is
  authenticated with `hmac-sha256-kek-v1` under the operator KEK. This is keyed integrity,
  not a public signature: anyone holding the KEK can authenticate a bundle.

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

**In the owner/app split, add a third: `OLIVARES_OWNER_DSN` (`--owner-dsn`).**
`dr backup` boots the engine to build the chain-tip manifest, and that boot runs
the schema's DDL preflight. In the split posture the application role is denied
`CREATE` on the engine schema **by design** (`deploy/postgres/01-app-role.sql`,
`olivares db init --owner-role`), so the boot cannot run as it — measured on
PostgreSQL 16.15, the backup fails with `SQLSTATE 42501`. With `--owner-dsn` the
DDL runs as the owner while runtime traffic stays on the app role. Leave it unset
in the **single-role** posture, where the app role owns the schema and is its own
DDL connection. Both roles must be NOSUPERUSER NOBYPASSRLS; check them first with
`olivares db check --dsn … --owner-dsn … --strict`.

**The `--admin-dsn` role must be able to `SELECT` EVERY relation the engine
creates — all of them, not most.** `pg_dump` opens its snapshot by locking the whole
schema in ONE `LOCK TABLE … IN ACCESS SHARE MODE` statement, so a single unreadable
relation does not shrink the dump, it aborts it:

    pg_dump: error: query failed: ERROR:  permission denied for table <relation>
    pg_dump: detail: Query was: LOCK TABLE public.<every relation> IN ACCESS SHARE MODE

`olivares db init --admin-role …` and `deploy/postgres/01-app-role.sql` establish that
read for present and future relations (`GRANT SELECT ON ALL TABLES IN SCHEMA public`
plus `ALTER DEFAULT PRIVILEGES … GRANT SELECT ON TABLES`), and every engine migration
leaves it in place. If a database was provisioned by hand, or its admin role was added
after the schema existed, re-run the pair before the first backup — the grant is
read-only and never gives the backup role a write:

    GRANT SELECT ON ALL TABLES IN SCHEMA public TO <admin>;
    ALTER DEFAULT PRIVILEGES FOR ROLE <owner> IN SCHEMA public GRANT SELECT ON TABLES TO <admin>;

### Kubernetes / Compose
- Helm: `--set backup.enabled=true --set backup.kekSecret=dr-kek` (CronJob; PG with
  a postgres-client initContainer for `pg_dump`; Postgres also requires
  `--set postgres.adminDsnKey=admin-dsn`, and in the owner/app split
  `--set postgres.ownerDsnKey=owner-dsn`). See `deploy/helm/README.md`.
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
# (Postgres owner/app split: ALSO add --owner-dsn=… — it is the pg_restore target
#  AND the boot's DDL connection; without it the restore cannot create anything)
```

`dr restore` does, in order:
0. **Refuses a wrong or incoherent connection BEFORE anything is written.** On
   Postgres it opens **one probe per supplied pool — one pinned connection, one
   transaction** — and checks the facts the *server* reports on that session, not
   the DSN text, so a service alias or a connection pooler in front of one estate
   is read as the estate behind it. It refuses when:

   - the `--dsn` or `--owner-dsn` role is a superuser or `BYPASSRLS` (FORCE RLS
     would be inert), or the `--admin-dsn` role is a superuser or is **not**
     `BYPASSRLS`;
   - a required role is unreachable;
   - the DDL role (`--owner-dsn`, else `--dsn`) lacks `CREATE` on the engine
     schema;
   - a pool that must **write** cannot: its session is read-only, or its server is
     in recovery. That is not the same question as the grant — a session under
     `default_transaction_read_only`, and every session on a hot standby, holds
     `CREATE` in the catalogue and still cannot execute one. The pools that must
     write are the **DDL** pool always, **and on a restore the application pool
     too**, because after the data lands the engine seals the restore declaration
     into the restored ledger through it. A **backup** is not restricted for that
     write, and `--admin-dsn` is never required to be writable: the cross-tenant
     reader is read-only by design;
   - the pools reached **different clusters**, compared by
     `pg_control_system().system_identifier` — PostgreSQL's own cluster identity,
     not the database name and not the postmaster's start time. An unrelated
     cluster that merely serves a same-named database is refused here;
   - **any pool is not on the same live server as the DDL pool.** This is proved,
     not inferred: the DDL connection takes a random advisory key and holds it
     while each other pool tries to take the same one. Advisory locks are
     cluster-scoped, so a pool that *acquires* it has proved it is a different
     server. It is the same challenge the engine runs at Open, asked earlier —
     and it is the only check that catches a **physical replica**, which reports
     its primary's cluster identifier and the same database name;
   - that identifier could **not be read** — an unverifiable identity is refused,
     never assumed to match. The refusal names the minimum grant
     (`GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO <role>`, metadata
     only) and the command never grants anything itself.

   The refusal names every problem at once and leaves the **target database and the
   data dir unchanged** — no signing key installed, no `pg_restore` run. The
   privilege half is the same verdict `olivares db check` prints and the boot guard
   enforces, asked one step earlier: the guard itself used to run *after*
   `pg_restore`, so a superuser `--dsn` wrote the whole estate before being refused.

   > **What this gate binds, and what it does not.** Each verdict is true of one
   > real session, which is then closed; `pg_restore` and the boot connect again.
   > It is therefore a sound gate on those later writes **only where every backend
   > reachable by a given DSN is uniform** in role authority, cluster identity,
   > database and writability — a direct route, or a pooler in front of one estate.
   > Behind a balancer free to answer a later dial with a different backend, this
   > catches static misconfiguration and nothing more. Binding the check to the
   > session that performs the first write is a different design and is not
   > implemented.

**Every DR pool must be on ONE LIVE SERVER, including `--admin-dsn`.** Reading
through a physical replica is **not supported**, and the pre-flight now refuses it
up front instead of letting a restore write and then failing at boot. The engine has
always required this — its directory activation runs the same advisory-key challenge
at Open and refuses with `directory activation admin DSN does not address the owner
database: … challenge_acquired=true`, with the database names *agreeing*. Supporting
a replica reader would need an authoritative snapshot/LSN contract for what the
tenant inventory it enumerates is a snapshot **of**, and how stale it may be; that
does not exist yet. Point every DSN at the same server.
1. **Extracts** regular entries into scratch, refusing absolute, non-canonical and
   traversing paths; no live path is touched.
2. **Refuses** an export from a **newer engine** before deriving keys or writing restore
   state, and names the minimum engine to install.
3. **Derives** the KEK, authenticates the complete manifest and checks every declared
   payload's path, size and digest. A separately authenticated pre-v26.9 bundle requires
   the explicit `--allow-legacy-unsigned` exception.
4. **Decrypts** the signing keys with the KEK and installs them 0600 in the data-dir
   (fail-closed on overwrite unless `--force`).
5. **Restores** the store snapshot (SQLite: copies the file; Postgres: `pg_restore`
   **into the `--owner-dsn` role when one is configured**, else `--dsn` — never
   `--admin-dsn`; PITR: skips — the store was recovered out-of-band by WAL replay).
   `pg_restore` is a separate process that connects again, so it is the same
   resolved **DSN** the pre-flight judged, not the same session — see the
   precondition under step 0.
   Restoring as the owner is also what makes the restored tables owner-owned, which
   is what reproduces the source estate's append-only ACL posture rather than a
   different one.
6. **Boots** the engine and runs `RestoreVerify`: for each tenant in the manifest,
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

`dr inspect` is metadata inspection only: it does not receive the KEK and therefore does
not authenticate the manifest. Use `dr verify`, not `inspect`, for an integrity decision.
For a separately authenticated older bundle, add `--allow-legacy-unsigned` deliberately;
the default is deny-closed.

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
| **Corrupt/tampered bundle** | manifest HMAC or any per-file path/size/SHA-256 differs → restore rejected before writes. |
| Export from a **newer engine** | version comparison refuses before writes and names the minimum engine to install. |
| **Tamper** of a row after restoring | `Verify` → `hash-mismatch` (the ledger stays tamper-evident after the restore). |
| **Wrong passphrase/KEK** | the AES-GCM tag fails → authenticated error, never a silently bad key. |

Everything above is covered by tests (`core/dr/*_test.go`,
`cmd/olivares/cmd_dr_test.go`).

### Honest limits
- **What is exercised against a live Postgres, and what is not.** The Postgres leg
  of `cmd/olivares` runs against a real server wherever
  `OLIVARES_TEST_POSTGRES_SUPERUSER_DSN` is set (`mainline-ci` provides one) and
  **skips** where it is not — a skipped leg is not a passing one. It provisions the
  owner/app/admin roles through the product's own `db init` path and drives
  `dr backup` → `dr restore` end to end in **both** the single-role and the
  owner/app split postures, plus the wrong-role and mismatched-target refusals. Not
  covered live anywhere: PITR WAL replay itself (only the companion bundle's
  keys+manifest path), restore into an **occupied** Postgres target, `--force`,
  offsite push/pull against a real bucket, and any multi-node/HA behaviour. Only
  PostgreSQL **16** is measured; nothing here is claimed for 15/17/18. The
  manifest/verification logic is **engine-agnostic** and is additionally tested over
  SQLite.
- **The owner/app split was UNSUPPORTED for DR before `--owner-dsn` existed, not
  merely untested.** Measured on PostgreSQL 16.15 at Community `b71ef8ef19`: the
  documented logical backup, logical restore, PITR companion backup and PITR
  companion restore all exited 1 with `SQLSTATE 42501`, because `dr` was the one
  boot path in the binary that never carried an owner DSN. It is recorded here
  rather than deleted so an operator running an older binary knows what they have.
- **Restoring as the owner does NOT close the pre-boot ACL window, and that window
  is still open.** Between `pg_restore` finishing and the engine's boot re-asserting
  the append-only guard, the restored tables carry whatever ACLs the target's
  ownership and `ALTER DEFAULT PRIVILEGES` produce — the product's dump is
  `--no-owner --no-privileges`, so the source's revokes are never in the bundle to
  begin with and the guard is **reconciled at boot**, not preserved by the restore.
  Measured on 16.15 with the product's own `pg_restore` argv: restoring as the
  **owner** leaves the app role with `UPDATE`/`DELETE` (but not `TRUNCATE`) on all 64
  append-only evidence tables in that window; restoring as the **app role** in the
  single-role posture additionally leaves it implicit `TRUNCATE`, and a
  `TRUNCATE public.audit_events` was accepted there. In a **successful** restore the
  window closes seconds later inside the same command and the end state is correct
  (verified). It becomes durable only if the command dies **between** those two
  steps. Closing it properly is separate, registered work; do not read `--owner-dsn`
  as having closed it.
- **A failed restore leaves the bundle's signing keys in the data dir, and the retry
  is harder than the first attempt.** Custody is installed before the store is
  touched and is rolled back only on the `--force` replacement path, so a
  `pg_restore` that fails into a *clean* target leaves `*-signing.key` behind — after
  which the identical command classifies the data dir as an existing estate and
  demands `--operator`/`--reason`. The target itself is untouched
  (`--single-transaction`). Registered separately; remove the keys by hand before
  retrying into a clean target.
- **A bundle produced by a build stamped with a bare commit hash can never be
  verified or restored, by any build including the one that wrote it.** The manifest
  is refused with `… is not an orderable release version`, and `dr backup` emits no
  warning. Release artifacts are stamped by goreleaser, so this bites DR drills run
  from a source build. Registered separately; use a released binary for a drill whose
  bundle must be restorable.
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

## 9.6 The local restore-coordination lock, and the read-only rollout prerequisite

Every installation carries a **stable coordination lock** beside the thing it fences:

| Anchor | Lock file | Durable record |
|---|---|---|
| the data directory (custody) | `<DATA_DIR>/.dr-control.lock` | `<DATA_DIR>/.dr-control` |
| a SQLite store file | `<canonical DB path>.dr-control.lock` | `<canonical DB path>.dr-control` |

They are two files on purpose. The lock's value is its **inode**: `flock()` is a property of
the inode, so a lock whose file gets replaced fences nothing. The record is replaced by
temp/write/fsync/rename on every transition, which gives it a new inode every time — so it
cannot be the lock. **The lock file is never truncated, chmod-repaired, replaced or unlinked**,
by the product or by an operator; the only supported operation on it is creating it once.

### The prerequisite, stated plainly

An ordinary boot **takes** that lock; it does not need to write to it. So the file is opened
read-only, and a deployment whose custody directory is not writable — the common shape for a
**remote-PostgreSQL** node with pre-installed signing keys — boots normally **as long as the
lock file already exists**.

**It must therefore be provisioned BEFORE the custody directory becomes read-only.** A node
that first meets this control on an already read-only directory is refused with a named
diagnosis that carries the exact path and the action. That refusal is deliberate: skipping the
fence on a permission error would publish a store with no restore coordination at all and no
symptom pointing at it, which is worse than a boot that stops and says what to do.

This is a **migration constraint**, not a claim that nothing changed. A pre-existing read-only
installation that has never run a writable boot of this version needs the step below.

### The step

Any ordinary writable boot of this version provisions the file as a side effect. To do it
explicitly, offline, with nothing else running, save the script below and run it once as the
account that owns the data directory, while the directory is still writable:

```sh
sh provision-dr-lock.sh '/var/lib/olivares/.dr-control.lock'
# SQLite installations ALSO need it beside the database file itself:
sh provision-dr-lock.sh '/var/lib/olivares/olivares.db.dr-control.lock'
```

**Quote the path, even though these examples do not need it.** A data directory whose
name contains a space — or a quote, a `$`, a `;`, a `*` — is a valid deployment, and an
unquoted operand is split by the shell before the script ever sees it: the script would
be handed two arguments and would provision neither of the files you meant. Inside single
quotes every byte but `'` is literal; for a path that itself contains one, close, escape
and reopen: `'/srv/olivares'\''s data/.dr-control.lock'`. The refusal the product prints
when the lock is missing already emits the pathname quoted this way.

<!-- BEGIN provision-dr-lock.sh: executed verbatim by opgate's tests; keep it and
     ProvisionLock making the same promise. -->

```sh
# provision-dr-lock.sh — create the DR coordination lock file, and nothing else.
# usage: sh provision-dr-lock.sh <lock-path>...
#
# CREATE-ONLY. An existing lock is left exactly as it is: same inode, same bytes,
# same mode. It is never truncated, replaced, unlinked or chmod-repaired, because
# the exclusion IS that inode and a live holder is fencing on it right now.
for lock in "$@"; do
	if [ -L "$lock" ] || { [ -e "$lock" ] && [ ! -f "$lock" ]; }; then
		echo "REFUSING $lock: it exists and is not a regular file. The product refuses it too; remove or repair it deliberately, with the service stopped." >&2
		exit 1
	elif [ -e "$lock" ]; then
		echo "already provisioned, left untouched: $lock"
	elif (umask 077; set -C; : > "$lock"); then
		echo "created $lock"
	else
		echo "could not create $lock — is the directory writable, and are you its owner?" >&2
		exit 1
	fi
done
```

<!-- END provision-dr-lock.sh -->

⛔ **Do not use `install ... /dev/null <lock>` for this, which is what this section said until
the F3 lot-A correction.** `install` opens the destination `O_CREAT|O_TRUNC` and falls back to
**unlinking and recreating** it when that open fails — so on an existing lock it resets the mode
and can hand the path a **new inode** while another process is still holding the old one. Two
holders would then each believe they were alone, which is precisely the exclusion this file
exists to provide. The recipe above uses the shell's `noclobber` (`set -C`), which creates with
`O_EXCL` and fails rather than touching anything that is already there.

Then make the directory read-only again. Verify with a boot: it either starts, or names the
file it still needs.

- The file is **empty and stays empty**. Nothing reads its contents; only its inode matters.
- An **existing** file is verified and left exactly as it is. Provisioning is idempotent and
  repairs nothing — if a file at that path is a symlink or is not a regular file, the boot
  refuses rather than replacing it, and so does the recipe above.
- One honest difference from the in-process `opgate.ProvisionLock`, which is what an ordinary
  writable boot runs: that one **fsyncs** the new file and its directory, and a shell cannot
  portably do either. If the machine loses power in the seconds after the script prints
  `created`, the name may not have reached the disk — re-run it, it is create-only.
- Do **not** point the lock somewhere else. There is one namespace, and a second one would mean
  two nodes fencing the same destination through two different inodes, which is no fence.

### Limits of this protocol, so nobody infers more than it gives

The lock is keyed by **pathname**, so a destination reachable under several names is refused
rather than half-fenced: an existing SQLite database with a link count greater than one is not
opened. Bind-mount aliases, a namespace that swaps the directory underneath a running process,
and a filesystem without working local `flock()` semantics are **outside** what this protocol
can see. Detecting a lock file replaced during acquisition is a check that catches an observed
race; it is not isolation from whoever owns the filesystem.

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

<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Postgres PITR for the Olivares control plane (near-zero RPO)

`pg-dump.sh` gives an RPO equal to its run interval. For the **Postgres / scale
tier** where a tighter RPO matters, use **point-in-time recovery (PITR)**:
continuous WAL archiving on top of a periodic base backup. This is the
near-zero-RPO option in the AWS DR taxonomy's *Backup & Restore* tier and the
foundation the multi-region topology builds on.

> The ledger continuity guarantee is **the same** either way: after recovery you
> install the signing keys and run `olivares dr verify` / `/v1/audit/verify`.
> PITR only changes how the *store bytes* are recovered, not how the *chain* is
> verified. Pair PITR with a **`dr backup --pitr-ref` companion bundle** (keys +
> chain-tip manifest, no store bytes) so the keys and the expected tips travel
> with the archive.

## 1. Enable WAL archiving (postgresql.conf)

```conf
wal_level = replica
archive_mode = on
# Ship each completed WAL segment to durable, OFFSITE storage. Use your archiver
# (pgBackRest / WAL-G / barman) rather than a raw cp in production.
archive_command = 'wal-g wal-push %p'      # or: 'test ! -f /archive/%f && cp %p /archive/%f'
archive_timeout = 60                        # cap RPO: force a segment at least every 60s
```

Restart Postgres after changing `wal_level`/`archive_mode`.

## 2. Take a base backup (periodically, e.g. daily)

```sh
# pg_basebackup (built-in) — or `wal-g backup-push` / `pgbackrest backup`.
pg_basebackup --pgdata=/backups/base-$(date -u +%Y%m%dT%H%M%SZ) \
  --format=tar --gzip --wal-method=stream --checkpoint=fast \
  --dbname="$OLIVARES_SUPERUSER_DSN"
```

## 3. Take the DR companion bundle (keys + chain tips)

Each time you take a base backup (and any cadence you like in between), capture a
companion bundle so the signing keys and the expected per-tenant tips are archived
alongside the WAL:

```sh
OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
olivares dr backup --engine=postgres \
  --dsn="$OLIVARES_DSN" --admin-dsn="$OLIVARES_ADMIN_DSN" \
  --data-dir=/var/lib/olivares \
  --pitr-ref="wal-archive://$(date -u +%Y%m%dT%H%M%SZ)" \
  --out=/backups/olivares-dr-pitr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file=/run/secrets/dr-pass
```

`--pitr-ref` records a pointer to the WAL archive instead of bundling store bytes,
so the bundle is just the encrypted keys + the chain-tip manifest.

## 4. Recover (disaster)

1. Restore the base backup to the recovery host and configure recovery:

   ```conf
   # postgresql.auto.conf / recovery
   restore_command = 'wal-g wal-fetch %f %p'   # or: 'cp /archive/%f %p'
   recovery_target_time = '2026-06-09 11:55:00+00'   # or recovery_target = 'immediate'
   ```

   ```sh
   touch "$PGDATA/recovery.signal"
   pg_ctl start    # Postgres replays WAL to the target, then promotes
   ```

2. Provision the `olivares_app` role on the recovered cluster if needed
   (`deploy/postgres/01-app-role.sql`).

3. Install the keys and PROVE continuity with the companion bundle:

   ```sh
   OLIVARES_DSN=postgres://olivares_app:***@recovered-host/olivares \
   OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
   olivares dr restore --engine=postgres \
     --dsn="$OLIVARES_DSN" --data-dir=/var/lib/olivares \
     --in=/backups/olivares-dr-pitr-<ts>.drbundle \
     --passphrase-file=/run/secrets/dr-pass
   ```

   `dr restore` sees the PITR companion (no store bytes), installs the keys, then
   verifies every tenant chain + per-event signatures + checkpoints against the
   recovered store, exiting non-zero unless the ledger is continuity-safe.

## RPO / RTO

- **RPO** ≈ `archive_timeout` + archiver lag (seconds–minutes), bounded by how
  fresh the WAL archive is at the disaster.
- **RTO** = base-restore time + WAL replay time + `dr restore` verification. Test
  it (step 4) on a cadence; record the measured numbers in your runbook.

See `docs/DR-RUNBOOK.md` for the full procedure, the key-custody model, and the DR
test cadence.

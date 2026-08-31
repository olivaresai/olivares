---
title: "Back up and restore (DR that proves itself)"
description: >-
  Encrypted, ledger-continuity-safe backups with olivares dr: scheduled
  bundles for SQLite and Postgres, the restore that verifies the chain, the
  drill you can run without touching production — and the two keys that
  decide whether your evidence survives.
---

A control plane's backup has a harder job than most: it must come back with
its **tamper-evident ledger provably intact**. `olivares dr` is built
around that requirement — every bundle records per-tenant chain tips, restore
**fails non-zero if the restored ledger is not continuity-safe**, and the
drill subcommand proves a bundle restorable without touching production.

The bundle is encrypted under a **KEK you provide** — an Argon2id-derived
passphrase (`--passphrase-file`) or a raw 32-byte key from your KMS
(`--kek-key-file`); exactly one is required. The audit and catalog signing
keys travel **sealed** inside the bundle.

## Back up

**SQLite** (single node) — safe while `serve` is running (the snapshot uses
`VACUUM INTO`; WAL allows the concurrent read):

```bash
olivares dr backup \
  --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

**Postgres** — a consistent `pg_dump --format=custom` driven by the same
command (`--engine postgres --dsn … --admin-dsn …`), or hand it a pre-made dump
with `--snapshot-file`. Running the dump directly **requires `--admin-dsn`**:
`pg_dump` keeps `row_security=off` and aborts as the application role against
`FORCE ROW LEVEL SECURITY` tables, so the command refuses up front rather than
producing nothing. For near-zero RPO, `--pitr-ref` produces a keys+manifest
companion bundle that pairs with your WAL-archiving PITR setup
(`deploy/postgres/backup/pitr-setup.md`); the wrapper scripts
`deploy/postgres/backup/pg-dump.sh` / `pg-restore.sh` package the same flow.

Two honesty switches worth knowing:

- The backup **refuses to capture a ledger that does not verify** at backup
  time — `--allow-unverified` exists, is logged, and is not recommended.
- With a **pre-made** snapshot (`--snapshot-file`/`--pitr-ref`) and no
  `--admin-dsn`, the backup warns that the captured tenant set may be
  RLS-limited and **incomplete** — the dump itself is fine, the manifest's
  cross-tenant inventory is what needs the admin role. (Running `pg_dump`
  *directly* is a different case: it is refused outright, see above.)

**Scheduling:** the Compose stack ships a
[backup profile](/tutorials/getting-started/docker-compose/#3-encrypted-dr-backups-the-backup-profile),
the Helm chart a
[CronJob](/tutorials/getting-started/kubernetes/#4-scheduled-encrypted-backups);
on bare metal, cron the command above. Your schedule **is** your RPO:

| Tier | Mechanism | RPO | RTO |
|---|---|---|---|
| SQLite | `dr backup` on cron | the cron interval | < 15 min |
| Postgres logical | `pg-dump.sh` on cron | the cron interval | < 30 min |
| Postgres PITR | base backup + WAL archiving | ≈ seconds | < 30 min |

Mirror bundles **offsite** and keep the KEK **separate from the bundles**
(3-2-1): a same-host backup is not disaster recovery, and a bundle traveling
with its passphrase is not encrypted in any sense that matters.

## Drill — before you need it

`dr verify` proves a bundle restorable **without touching your data dir**
(SQLite: full chain verification in a scratch dir; exits non-zero if unsafe):

```bash
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

`dr inspect --in <bundle>` prints the manifest (no KEK needed, no secrets
shown) — what engine, which tenants, which chain tips. Run the drill on the
same cadence as the backup; an unverified backup is a hope, not a control.

## Restore

```bash
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file <your-dr-passphrase-file>
```

The restore sequence is deliberate: signing keys first (fail-closed on
overwrite — `--force` is the explicit override), then the store snapshot,
then it **boots the restored store and proves ledger continuity**, exiting
non-zero if the chain is not safe. After any restore, re-verify against your
**off-box** checkpoint pin — a restored older snapshot can pass a naive walk
yet fail the off-box comparison
([troubleshooting § ledger](/how-to/troubleshooting/#the-ledger-fails-verification)).

## The two keys that decide everything

| Key | Rule |
|---|---|
| **The DR KEK** (passphrase or raw key) | without it every bundle is noise. Store it in a different system than the bundles; losing both at once is the failure mode |
| **`audit-signing.key`** (in the data dir) | back it up off-box at provisioning — the engine only **warns** on first boot, there is no enforced escrow, and a lost key makes the ledger permanently unverifiable. Pin the public key off-box too (`GET /v1/audit/pubkey`) |

For KMS-based custody of the signing keys themselves (BYOK envelopes,
rotation ceremonies, `olivares keys`), see the
[CLI reference](/reference/cli/); for the failure-mode walkthroughs, the
[troubleshooting page](/how-to/troubleshooting/).

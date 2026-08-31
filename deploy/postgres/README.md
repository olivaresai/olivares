<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Postgres deployment & disaster recovery

The Olivares control plane runs on SQLite (single-node/air-gap) or **Postgres**
(multi-tenant scale, RLS backstop). This directory holds the Postgres operational
artifacts.

## Files

| File | Purpose |
|---|---|
| `01-app-role.sql` | Provision the least-privilege `olivares_app` role + `olivares` database. The engine **refuses to start** against a superuser/BYPASSRLS role (`docs/SECURITY-HARDENING.md`), so this is how you bootstrap correctly. Apply it with a superuser/maintenance DSN: `psql "$SUPERUSER_DSN" -v app_password=… -f 01-app-role.sql`. |
| `backup/pg-dump.sh` | Logical backup → ledger-continuity-safe DR bundle (RPO = run interval). |
| `backup/pg-restore.sh` | Restore a DR bundle and verify ledger continuity (non-zero exit if unsafe). |
| `backup/pitr-setup.md` | Point-in-time recovery (WAL archiving) for near-zero RPO. |

## Onboarding from the binary (no psql by hand)

`01-app-role.sql` is the transparent, reviewable bootstrap; the binary does the same
thing idempotently and verifies it, which is what `olivares setup` (the guided
installer) drives for you:

```sh
# Provision the role(s) + database with a superuser/maintenance DSN (idempotent):
olivares db init --superuser-dsn "postgres://postgres@db:5432/postgres" \
  --app-role olivares_app --app-password-file /run/secrets/app_password
#   add --owner-role olivares_owner  for the least-privilege owner/app split (--owner-dsn)
#   add --admin-role olivares_admin  for the cross-tenant read role (--admin-dsn)
#   --print-sql                       previews the exact statements without connecting

# Verify the RLS posture BEFORE booting (predicts the boot guard exactly):
olivares db check --dsn "postgres://olivares_app@db:5432/olivares?sslmode=verify-full"
```

`db init` reconnects as each provisioned role to confirm the engine will accept it,
and prints ready-to-use, password-free DSNs. Store each password in a 0600 file and
point `serve` at it as `--dsn=file:/etc/olivares/secrets/app.dsn` so no credential
sits in the systemd env file — `olivares setup` writes those files for you.

> **Adopting the owner/app split on an _existing_ single-role database** is not a
> drop-in `--owner-role` re-run: the existing tables are still **owned by the app
> role**, so the new owner cannot run migrations against them. Provision the split on
> a **fresh** database, or first reassign ownership by hand
> (`REASSIGN OWNED BY olivares_app TO olivares_owner;` in the target database) before
> pointing `--owner-dsn` at it.

## Disaster recovery

The audit ledger is a per-tenant hash-chain where **every event is Ed25519-signed**
and periodically checkpointed (`docs/SECURITY-HARDENING.md`). Restoring it safely is **not a naive
`pg_dump`**: you also need the signing key (the per-event key is always on-box) and
a way to prove the restored chain is intact. The `dr` tooling wraps `pg_dump`/PITR
to do exactly that.

```sh
# Backup (logical, RPO = interval):
OLIVARES_DSN=postgres://olivares_app:***@host:5432/olivares \
OLIVARES_ADMIN_DSN=postgres://olivares_admin:***@host:5432/olivares \
OLIVARES_DATA_DIR=/var/lib/olivares \
OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
  ./backup/pg-dump.sh /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle

# Test the backup WITHOUT touching production (the DR drill — do this on a cadence):
olivares dr verify --in /backups/olivares-dr-….drbundle --passphrase-file /run/secrets/dr-pass

# Restore into an empty target and PROVE continuity:
OLIVARES_DSN=postgres://olivares_app:***@dr-host:5432/olivares \
OLIVARES_DR_PASSPHRASE_FILE=/run/secrets/dr-pass \
  ./backup/pg-restore.sh /backups/olivares-dr-….drbundle
```

- For **near-zero RPO**, use PITR (`backup/pitr-setup.md`) + a `dr backup --pitr-ref`
  companion bundle (keys + chain-tip manifest).
- For **Kubernetes**, the Helm chart ships an opt-in backup CronJob
  (`backup.enabled=true` in `deploy/helm`); a postgres-client initContainer runs
  `pg_dump` through the dedicated NOSUPERUSER BYPASSRLS admin DSN, then the engine
  bundles the complete multi-tenant dump. The NOBYPASSRLS app DSN cannot perform
  this dump against FORCE-RLS tables.
- **Mirror bundles OFFSITE** and keep the KEK separate from them — a same-host
  backup is not disaster recovery.

The authoritative procedure (RPO/RTO targets, key custody, failure modes, DR test
cadence) is **`docs/DR-RUNBOOK.md`**.

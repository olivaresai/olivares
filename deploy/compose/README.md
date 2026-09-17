<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Olivares AI — Docker Compose (single node)

The one-command single-host deployment. The base file runs the engine with the
embedded pure-Go SQLite store — zero external dependencies, air-gap-ready.

## Quickstart (SQLite, one command)

```sh
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
# get the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# open https://localhost:8443 (self-signed TLS by default)
```

The host port is bound to `127.0.0.1` — expose deliberately. Data persists in the
`olivares-data` volume (audit signing key, TLS material, the SQLite store).

**DIST-24-12 current tree contract.** The distroless image carries `olivares readyz`,
and the reference stack invokes it directly—no shell, curl, or wget. The hermetic
battery proves local HTTP 200 → rc 0, a received non-200 → rc 1, and an input/TLS/
transport failure → rc 2; `up --wait` consumes that health state. **Docker qualification
remains unmeasured until the dispatch workflow succeeds** for this commit, so the
workflow's presence alone is not a recorded hosted-Docker result.

## Postgres (multi-tenant)

```sh
cp deploy/compose/.env.example deploy/compose/.env   # then set the three passwords below
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

The override brings up Postgres and, **on first cluster init only**, runs both canonical
role files through `initdb/10-app-role.sh` to provision **two distinct, deliberately
unequal principals**:

| Role | Attributes | What it is for |
|---|---|---|
| `olivares_app` | `NOSUPERUSER NOBYPASSRLS` | Owns the `olivares` database and serves **all** traffic. FORCE ROW LEVEL SECURITY isolates tenants on it — even as the table owner. Provisioned by `deploy/postgres/01-app-role.sql`. Its append-only protections are unchanged. |
| `olivares_admin` | `NOSUPERUSER BYPASSRLS`, **read-only** | The cross-tenant System read pool behind `--admin-dsn`. Holds exactly `CONNECT`, schema `USAGE` and `SELECT` on the app role's current and future tables — no write privilege on any engine relation, no `CREATE`, no sequences. (PostgreSQL's `PUBLIC` default also leaves it `TEMP` on the database, as it does for the product's own provisioner; that creates temporary objects in its own session and cannot touch the estate.) Provisioned by `deploy/postgres/02-admin-role.sql`. |

so the FORCE-RLS tenant backstop is real (`docs/SECURITY-HARDENING.md`; the engine refuses
to start against a superuser/BYPASSRLS **application** role), while the authoritative
cross-tenant reads still have a pool that can actually perform them.

### The administrative read role is REQUIRED on Postgres, not optional

`POST /v1/setup` resolves the first organization through an authoritative cross-tenant
read (`Store.System` → `SystemScope.ListOrgs`). Under FORCE RLS the `NOBYPASSRLS`
application role is filtered to the bound tenant, so that read is **refused** rather than
reported as an empty estate. A Postgres stack with only `olivares_app` therefore answers:

* `POST /v1/setup` → `501 cross_tenant_admin_pool_not_configured`
* `GET /readyz` → `503 {"status":"setup_blocked","setup_required":true,"code":"cross_tenant_admin_pool_not_configured"}`, with the remedy in the body

— and its `olivares` container reports **unhealthy**, because the healthcheck consumes
`/readyz`. That is the intended refusal: the install genuinely cannot be set up. It is
not something to work around by relaxing the probe or by passing
`--allow-privileged-db-role`. The same read also backs the org list, multi-tenant
checkpoint coverage and DR backup, so the pool keeps earning its place after first boot.

`02-admin-role.sql` refuses rather than report a read-only pool that is not one: before it
grants anything it checks whether `olivares_admin` can reach write authority on schema
`public` — a table or column privilege, ownership of a relation, or membership of
`pg_write_all_data` — counting grants made to it, to `PUBLIC`, or to any role it can
`SET ROLE` into. Sequences, functions, other schemas and other databases are outside that
check, and it is a bootstrap rather than a monitor: it says nothing about a privilege
granted after it runs.

An **already-configured** installation stays usable without the pool for work that
invokes no cross-tenant ceremony; the refusals above are about reaching, and completing,
first setup.

### Passwords

Three principals, three **different** passwords, none of them reused:

| `.env` variable | Principal |
|---|---|
| `POSTGRES_SUPERUSER_PASSWORD` | the Postgres superuser, used once at cluster init |
| `OLIVARES_DB_PASSWORD` | `olivares_app` — the engine's traffic role |
| `OLIVARES_ADMIN_PASSWORD` | `olivares_admin` — the cross-tenant read role |

Each is referenced as `${VAR:?…}`, so a missing one fails the `docker compose`
invocation **by name** instead of starting a half-configured stack. Nothing is
defaulted, and no credential is written into the repository.

Set all three password variables after copying `.env.example`. Compose refuses to
start when a required value is empty and identifies the missing variable.

> ⛔ **URI-safe passwords only — a real restriction of this demo, not advice.** Both
> DSNs are built by interpolating the password into a
> `postgres://user:password@host/db` URI in the override, so a password containing
> `@ : / ? # [ ] %` or a space is parsed as URI structure and the engine either fails to
> connect or connects somewhere unintended. Use `A-Z a-z 0-9 . _ ~ -` only — e.g.
> `openssl rand -base64 33 | tr -d '=+/'`. This is **not** arbitrary-password support.
> A deployment that needs the full character set uses the Helm chart, which passes a
> DSN Secret instead of interpolating a URI.

### Adding the administrative read role to an existing volume

`docker-entrypoint-initdb.d` runs **only when the data directory is empty**. If this
stack was first started before `olivares_admin` existed, the retained `pg-data` volume
has the app role and your data but no administrative role, and **editing the initdb hook
or the SQL changes nothing there**. Provision it explicitly instead — two commands, no
data loss, no volume deletion:

```sh
# 1 · RECREATE THE POSTGRES SERVICE FIRST. A container's bind mounts and environment are
#     fixed when it is created, so the one holding your old volume has neither
#     /sql/02-admin-role.sql nor OLIVARES_ADMIN_PASSWORD — both arrived with this release.
#     The pg-data volume is RETAINED and initdb does NOT re-run, which is exactly why
#     step 2 is still needed.
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d postgres

# 2 · apply the shipped file, as the superuser. The SQL stays mounted for the container's
#     whole lifetime, not only during initdb, so this is the exact file initdb would have
#     run. Use the LITERAL user and database: POSTGRES_USER/POSTGRES_DB are derived by the
#     image's entrypoint inside PID 1 and an `exec` process does not inherit them, so
#     `-U "$POSTGRES_USER"` expands to `-U ""`, which libpq treats as unset and falls back
#     to the OS user — a confusing `FATAL: role "root" does not exist`.
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml \
  exec postgres sh -c 'psql -v ON_ERROR_STOP=1 -U postgres -d postgres \
    -v admin_password="$OLIVARES_ADMIN_PASSWORD" -f /sql/02-admin-role.sql'

# 3 · recreate the engine so it opens the second pool (its command line gained
#     --admin-dsn). The data volume is reused; no migration is triggered by this.
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d olivares
```

> **Where that password is, and is not, visible.** `$OLIVARES_ADMIN_PASSWORD` is expanded by
> the shell **inside** the container, so it stays out of your own shell history — the outer
> command carries the name, not the value. It does **not** stay out of the process list:
> `-v admin_password=…` puts the value in the `psql` process's argv, readable through
> `/proc` by anything in that container's PID namespace for as long as the command runs.
> That is inherent to `psql -v`. If your threat model does not accept it, provision with
> the binary instead — `olivares db init --admin-role olivares_admin --admin-password-file
> …` reads the credential from a **file** and never places it in an argument
> (`../postgres/README.md`).

`02-admin-role.sql` is safe to apply to a populated database: it creates the role only
if absent, **never runs `DROP ROLE` or `ALTER ROLE`**, grants `SELECT` on the tables that
already exist as well as on future ones, and **refuses out loud** if a role of that name
already exists with the wrong posture rather than silently changing an existing
principal's authority. Re-running it does not rotate an existing password — so if the role
already existed, the value in `.env` must be the one it already has; the file verifies the
role's privileges, not that the credential you supplied authenticates, and a mismatch
surfaces later as the engine failing to open the admin pool at boot. It also
refuses if `olivares_app` is missing, if the database is missing, or if either role is a
member of the other — membership would let the application role `SET ROLE` into
`BYPASSRLS` and make the tenant backstop inert.

If you would rather let the binary do it, `olivares db init --admin-role olivares_admin
--admin-password-file …` provisions the same role and grants, and verifies them by
reconnecting (`../postgres/README.md`).

> `sslmode=disable` in the override is for the in-network compose demo only.
> Production uses TLS + `sslmode=verify-full` — prefer the Helm chart with a DSN
> Secret for that, and a managed/your-own Postgres.

## Operate Claude Code (co-deployment)

Layer the **agentops** override to add a governed Claude Code runtime to the same
node — one hardened container (engine + `claude`) over a shared workspace volume, so
the control plane launches, governs and tears down Claude Code sessions:

```sh
# build the opt-in combined image (claude installed from Anthropic's signed apt repo)
docker build -f Dockerfile.agentops \
  --build-arg OLIVARES_IMAGE=docker.io/olivaresai/olivares:latest -t olivares-agentops:latest .
OLIVARES_AGENTOPS_IMAGE=olivares-agentops:latest \
  docker compose -f deploy/compose/docker-compose.yml \
                 -f deploy/compose/docker-compose.agentops.yml up --wait --wait-timeout 120
```

Same secure-by-default posture as the base (loopback, non-root 65532, read-only root,
cap-drop) plus the conducted runtime — only the workspace, `claude`'s `~/.claude` home
and the short-lived inference token are writable, each its own volume. The deny-closed
credential reads a rotated bearer from the `olivares-runtime` volume
(`/run/olivares/session-token`). Full walkthrough + the other three topologies:
`../../docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md`.

## Supply chain

Pin the image by **digest** for a verifiable deploy — set `OLIVARES_IMAGE` in
`.env` to `docker.io/olivaresai/olivares@sha256:<digest>` (the official registry;
the `ghcr.io/olivaresai/olivares` fallback carries identical content under the same
digest) and verify it first:

```sh
cosign verify docker.io/olivaresai/olivares@sha256:<digest> \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The same `cosign verify` works verbatim against `ghcr.io/olivaresai/olivares@sha256:…`
— the mirror is a `cosign copy` by digest, so the signatures and attestations travel
with the image. Docker Hub applies a **rate limit to anonymous pulls**; ghcr.io does
not rate-limit anonymous pulls of public images, which is what the fallback is for
(authenticate with `docker login` on Docker Hub, or point `OLIVARES_IMAGE` at the
ghcr.io coordinate, if a CI node or a large fleet hits the ceiling).

For Kubernetes use `../helm`; for the air-gapped path see `scripts/airgap-bundle.sh`
and `../../docs/RELEASE-VERIFICATION.md`.

## Upgrades, rollback & reconfiguration

To move to a new version, set `OLIVARES_IMAGE` to the new (verified) digest and
`docker compose … up --wait --wait-timeout 120` — the data volume is reused and schema migrations apply on
boot. Schema changes use the online expand-contract model, so **rollback is redeploying
the previous digest**, not reversing the database (the one exception — rolling back across
a destructive `contract` migration — and how to check for it with `olivares migrate
status`, plus the table of which config changes need a restart, are in the
[Upgrade & rollback runbook](../../docs/UPGRADE-AND-ROLLBACK.md)). Back up first
(below).

## Disaster recovery (backup/restore)

Scheduled, ledger-continuity-safe DR bundles — store snapshot + signing
keys encrypted under your KEK + a manifest of the per-tenant chain tips:

```sh
printf 'a strong DR passphrase' > deploy/compose/dr-pass   # keep OUT of the repo/image
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

Wrap that in host cron for a scheduled RPO, prune old bundles on the host
(`find <backups> -name '*.drbundle' -mtime +14 -delete`), and **mirror the
`olivares-backups` volume OFFSITE** (a same-host backup is not DR). Restore + verify
with `olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass`.
The full procedure (RPO/RTO, key custody, DR drill) is `../../docs/DR-RUNBOOK.md`.

The backup service runs as the image's non-root user (65532). The image seeds `/backups`
for that user with mode 0700, so a **fresh** `olivares-backups` volume is writable, exactly
as the data volume is. A volume that already exists is not changed by a new image: check
its ownership before relying on scheduled backups, and change it only as a deliberate
operator step. Nothing in the image or in Compose chowns, empties or deletes it.

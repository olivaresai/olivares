<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Olivares AI — Docker Compose (single node)

The one-command single-host deployment. The base file runs the engine with the
embedded pure-Go SQLite store — zero external dependencies, air-gap-ready.

## Quickstart (SQLite, one command)

On hosts that mediate user namespace creation, complete
[the AppArmor setup](#session-confinement-on-apparmor-hosts) before starting the stack.

```sh
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

Then, without a shell, these four commands are the whole operator surface:

```sh
# where the console answers, and whether first setup is still pending
docker compose -f deploy/compose/docker-compose.yml exec olivares olivares first-boot

# the one-time setup token, printed once at first boot
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# what is running, and how to stop it
docker compose -f deploy/compose/docker-compose.yml ps
docker compose -f deploy/compose/docker-compose.yml down
```

**Every one of them needs the `-f`.** Compose looks for a compose file in the working
directory and this one lives in `deploy/compose/`, so a bare `docker compose ps` at the
repository root answers *"no configuration file provided"*. Run `export
COMPOSE_FILE=deploy/compose/docker-compose.yml` once if you would rather not repeat it.

**`olivares quickstart` is the local-binary path, not this one.** In Docker the server is
already running: the container's command is `serve`, and `quickstart` would start a second
engine inside the container. Use the commands above instead.

The engine is named `olivares` (the project is named in the compose file, so the container
is not `compose-olivares-1`), and the Postgres and backup services of the overrides are
`olivares-postgres-1` and `olivares-backup-1`.

The host ports are published on **every interface** by default: this is a server, the
console serves TLS with a self-signed first-boot certificate, and there are no default
credentials. Set `OLIVARES_BIND=127.0.0.1` in `.env` to restrict it to the host itself — and
note that a published Docker port is DNAT'd ahead of a host firewall's INPUT chain, so that
variable, not `ufw deny 8443`, is what closes it. Data persists in the `olivares-data`
volume (audit signing key, TLS material, the SQLite store).

If the setup token scrolled away or the log rotated, and no administrator exists yet, mint a
replacement without a restart:

```sh
docker compose -f deploy/compose/docker-compose.yml exec olivares olivares first-boot --new-token
```

**Compose readiness contract.** The image carries `olivares readyz`,
and the reference stack invokes it directly—no shell, curl, or wget. The hermetic
battery proves local HTTP 200 → rc 0, a received non-200 → rc 1, and an input/TLS/
transport failure → rc 2; `up --wait` consumes that health state. **Docker qualification
remains unmeasured until the dispatch workflow succeeds** for this commit, so the
workflow's presence alone is not a recorded hosted-Docker result.

## Agent tools and first-session resources

The engine and its child tools share a ceiling of **2 GiB RAM** and the host's
CPUs: the file sets no CPU limit, because a Docker daemon refuses a container
whose CPU limit exceeds the daemon's CPU count, so any limit above 1 would stop
`docker compose up` on a 1-CPU host. The reservation remains 0.1 CPU and 256 MiB.
To cap the CPUs or lower the memory, add a Compose override, with a CPU limit of
at most the host's CPU count:

```yaml
services:
  olivares:
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 1G
```

Agent CLIs, including Claude Code, are not bundled in the image. Install them
from **AI tools** after setup: the managed installer verifies vendor downloads
and places them under `/var/lib/olivares/tools`, as UID 65532 with a read-only
root filesystem. Keep the `olivares-data` volume when recreating the container;
it also holds tool receipts, account homes and caches.

The `managed-tools` PR job measures the former 1 CPU / 1 GiB limits and the
shipped defaults on fresh volumes, installs OpenCode and Claude Code through
the authenticated API, and checks cold tool status again after recreation.
The default profile must answer each tool status request within five seconds.
This does not qualify a provider login, a model response, or native deb/rpm
installation; package install/upgrade qualification remains in the release gate.

## Session confinement on AppArmor hosts

On Linux hosts that mediate `userns_create`, Docker's `docker-default` AppArmor profile
can refuse the user namespace needed by a provider-bound Claude Code or Codex session.
The engine stays healthy, but starting the session fails before it reaches the project.
Load the shipped Docker-default-derived profile with an AppArmor 4 parser **on the Docker
daemon host**, then select it before `up`:

```sh
set -e
sudo install -m 0644 deploy/apparmor/olivares-sessions.conf /etc/apparmor.d/olivares-sessions
sudo apparmor_parser -r /etc/apparmor.d/olivares-sessions
export OLIVARES_APPARMOR_PROFILE=olivares-sessions
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

The README install block checks the kernel's AppArmor namespace feature mask. Hosts
without this mediation keep `docker-default`. Profile load errors must be fixed before
`up`; a selected but unloaded profile makes Docker refuse to create the container.
The installed file lets AppArmor load it again at boot. For later `up` commands, keep
the export or set `OLIVARES_APPARMOR_PROFILE=olivares-sessions` in the Compose environment.
If you use a remote Docker context, install/load it on that daemon's host and set the
variable on the client; the client's feature mask describes only its own kernel.

The profile retains [Docker's default AppArmor rules](https://github.com/moby/moby/blob/v26.1.5/profiles/apparmor/template.go)
and adds `userns`, with the profile and daemon signal peer names fixed for this deployment.
A daemon confined under a custom AppArmor profile needs that profile as an additional
`signal (receive)` peer. This permission applies to every process in the engine container;
it does not change the session's own Landlock or seccomp filters. Keep the non-root user,
read-only root, dropped capabilities and `no-new-privileges` enabled.

After uninstalling the stack (`down`), unload the profile with
`sudo apparmor_parser -R /etc/apparmor.d/olivares-sessions`, then remove that file.
Other containers using this profile must be stopped first.

## Work on a host project folder

The base file has one project mount at `/project`, read-write: the host folder whose
absolute path is in `OLIVARES_PROJECT_DIR`, in `.env` or exported before `up`:

```sh
export OLIVARES_PROJECT_DIR=/home/you/code/my-app
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

In the console, choose **Change folder** on the New session form and enter `/project`; the
form offers it again for the next session. A session in `/project` is confined to that
folder like any session folder: Landlock, where the host kernel supports it, keeps the
engine's data out of its reach.

- **Without the variable.** `/project` is the empty `olivares-project` volume, never the
  directory you run Compose from: that is usually this checkout, and `deploy/compose/`
  holds `dr-pass` and `.env`. The container user cannot write to the volume.
- **Use a dedicated project folder.** A session in `/project` can change everything in it,
  including files that later run on your host, such as `.git/hooks`, `.envrc` and shell
  startup files. Never point the variable at your home directory, `/` or this checkout.
- **Check the path.** Give an absolute path: Compose resolves a relative one, such as `.`,
  against `deploy/compose/`, which holds `dr-pass`. Compose 2 can create a missing folder as
  root, and the container cannot write to it.
- **Linux write access.** The container runs as UID 65532. Without write access to the
  folder a session can read it and cannot change it. Grant it on the project folder only,
  never on your home directory:
  `setfacl -R -m u:65532:rwX -m d:u:65532:rwX "$OLIVARES_PROJECT_DIR"`. The grant covers every
  file in the tree, including `.env` files and keys, and 65532 is the usual non-root user
  of minimal images, so other containers share it. Files a session creates belong to
  UID 65532 on the host.
- **SELinux hosts (Fedora, RHEL).** Access from the container may be refused until the
  folder is relabelled for containers. That relabels the whole tree, so it is your decision.
  This path is not measured here.
- **Changing the folder.** `/project` is the registered session folder, so pointing
  `OLIVARES_PROJECT_DIR` elsewhere and running `up` again moves every session folder
  registered at `/project` to the new host folder. The data volume is untouched.

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

The standard stack runs governed Claude Code sessions: the engine installs the agent tools
into its data volume and signs them in through the product, so the control plane launches,
governs and tears down Claude Code sessions with no second image and no override file:

HTTPS and gRPC publish on every host interface (`0.0.0.0`) by default. Restrict them
to this host for the local walkthrough:

```sh
export OLIVARES_BIND=127.0.0.1
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

Then install and sign in to Claude Code from **AI tools** in the console, or with
`olivares tool install claude` and `olivares tool login claude`. The posture is the base
stack's (non-root 65532, read-only root, cap-drop); the command above restricts the published
ports to loopback. Full walkthrough + the other
three topologies:
`../../docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md`.

## Supply chain

Pin the image by **digest** for a verifiable deploy — set `OLIVARES_IMAGE` in
`.env` to `docker.io/olivaresai/olivares@sha256:<digest>` (the official registry;
the `ghcr.io/olivaresai/olivares` fallback carries identical content under the same
digest) and verify it first:

```sh
cosign verify docker.io/olivaresai/olivares@sha256:<digest> \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The same `cosign verify` works verbatim against `ghcr.io/olivaresai/olivares@sha256:…`
— the mirror is a `cosign copy` by digest, so the signatures and attestations travel
with the image. Docker Hub applies a **rate limit to anonymous pulls**; ghcr.io does
not rate-limit anonymous pulls of public images, which is what the fallback is for
(authenticate with `docker login` on Docker Hub, or point `OLIVARES_IMAGE` at the
ghcr.io coordinate, if a CI node or a large fleet hits the ceiling).

Kubernetes deployment packages are supplied through Business; offline installation requires Enterprise. See `../../docs/RELEASE-VERIFICATION.md`.

## Upgrades, rollback & reconfiguration

To move to a new version, use that release's Compose files or set `OLIVARES_IMAGE`
to the new (verified) digest. Remove an old `OLIVARES_IMAGE=…:latest` override from
your environment or `.env` to use the release default. Keep the same Compose
overrides used for installation so the engine keeps its database configuration.
For the base SQLite installation, pull before recreating:

```sh
docker compose -f deploy/compose/docker-compose.yml pull
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
docker compose -f deploy/compose/docker-compose.yml exec olivares olivares version
```

For PostgreSQL, include its override in each command:

```sh
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.postgres.yml pull
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.postgres.yml exec olivares olivares version
```

The last command reports the running binary's version. The data volume is reused and schema migrations apply on
boot. Schema changes use the online expand-contract model, so **rollback is redeploying
the previous digest**, not reversing the database (the one exception — rolling back across
a destructive `contract` migration — and how to check for it with `olivares migrate
status`, plus the table of which config changes need a restart, are in the
[Upgrade & rollback runbook](../../docs/UPGRADE-AND-ROLLBACK.md)). Back up first
(below).

## Disaster recovery (backup/restore)

Scheduled, ledger-continuity-safe DR bundles — store snapshot + signing
keys encrypted under your KEK + a manifest of the per-tenant chain tips:

Keep the passphrase in a private file outside the checkout and the image, and keep
a copy somewhere safe off this host: without it, no bundle can be restored. Give
the backup container read-only access to it (the image runs as UID `65532`):

```bash
sudo install -d -o 65532 -g 65532 -m 0700 /srv/olivares-dr
sudo install -o 65532 -g 65532 -m 0400 /path/to/private-passphrase /srv/olivares-dr/dr-pass

BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               -f - --profile backup run --rm backup <<'YAML'
services:
  backup:
    volumes:
      - /srv/olivares-dr/dr-pass:/run/secrets/dr-pass:ro
YAML
```

Wrap that in host cron for a scheduled RPO, prune old bundles on the host
(`find <backups> -name '*.drbundle' -mtime +14 -delete`), and **mirror the
`olivares-backups` volume OFFSITE** (a same-host backup is not DR). Restore + verify
with `olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file /path/to/private-passphrase`.
The full procedure (RPO/RTO, key custody, DR drill) is `../../docs/DR-RUNBOOK.md`.

The backup service runs as the image's non-root user (65532). The image seeds `/backups`
for that user with mode 0700, so a **fresh** `olivares-backups` volume is writable, exactly
as the data volume is. A volume that already exists is not changed by a new image: check
its ownership before relying on scheduled backups, and change it only as a deliberate
operator step. Nothing in the image or in Compose chowns, empties or deletes it.

## Release first-hour qualification

See the [release verification contract](../../docs/RELEASE-VERIFICATION.md#container-first-hour-qualification)
for the required pre-tag replay of this Quickstart on the candidate container.

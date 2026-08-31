<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Olivares AI — Docker Compose (single node)

The one-command single-host deployment. The base file runs the engine with the
embedded pure-Go SQLite store — zero external dependencies, air-gap-ready.

## Quickstart (SQLite, one command)

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
# get the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# open https://localhost:8443 (self-signed TLS by default)
```

The host port is bound to `127.0.0.1` — expose deliberately. Data persists in the
`olivares-data` volume (audit signing key, TLS material, the SQLite store).

## Postgres (multi-tenant)

```sh
cp deploy/compose/.env.example deploy/compose/.env   # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

The override brings up Postgres, runs `deploy/postgres/01-app-role.sql` on first
init (via `initdb/10-app-role.sh`) to create the **least-privilege** `olivares_app`
role, and points the engine at it — so the FORCE-RLS tenant backstop is real
(`docs/SECURITY-HARDENING.md`; the engine refuses to start against a superuser/BYPASSRLS role).

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
                 -f deploy/compose/docker-compose.agentops.yml up -d
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
`docker compose … up -d` — the data volume is reused and schema migrations apply on
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

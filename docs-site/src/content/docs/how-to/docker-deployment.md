---
title: Deploy with Docker
description: >-
  Pull and verify the image from Docker Hub, then run the control plane in
  production with Docker — hardened single-node SQLite, multi-tenant Postgres,
  scheduled DR backups, reverse-proxy TLS termination, upgrades and
  digest-pinning.
---

This guide is for engineers and SREs putting the Olivares AI control plane into
production with Docker. The whole product is a single distroless image — the engine
with the web UI embedded — so a single host can run the SQLite topology with no
external dependencies, and a Postgres override gives you the multi-tenant topology
when you need it. Every path keeps the same secure defaults: no default credentials,
a one-time setup token, TLS on by default, and the host port bound to loopback.

:::note[Beta — no release is cut yet]
Olivares AI is **beta**. The image coordinates below resolve only **after the first
release (CalVer `26.8.0`) ships**; until then the registries have nothing to pull.
Treat this as the deployment shape you will use, not a production-ready guarantee.
:::

For the decision-page view of all deployment options and their defaults, see
[Self-host the control plane](/how-to/self-hosting/). For disconnected sites, see
[Install in an air-gapped environment](/how-to/air-gap-install/); for scale-out, see
the Kubernetes/Helm path below.

## 1. Pull and verify the image

The official container pull is **Docker Hub**:

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

The same content is also published to `ghcr.io/olivaresai/olivares` — identical by digest,
and used as the build registry and the fallback. Docker Hub rate-limits **anonymous**
pulls; ghcr.io does not rate-limit anonymous pulls of public images, so `docker login`
or the ghcr.io coordinate is the way out if a CI node or a large fleet hits the ceiling.
Tags carry **no leading `v`**:
`:26.8.0` pins a release, `:latest` floats, and `:26.8.0-fips` / `:26.8.0-stig` are
the hardened variants. The base and `:latest` tags are multi-arch
(`linux/amd64`, `linux/arm64`); `fips`/`stig` are `amd64`-only.

A control plane is a security product, so verify before you run. Signing is
**keyless** (Sigstore) against the project's GitHub Actions identity, and works
identically against either registry — the signatures and attestations are copied to
Docker Hub by `cosign copy`, so the digest is the same:

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The full chain — checksums signature, SBOM, OpenVEX, SLSA provenance — is in
[Verify what you downloaded](/how-to/verify-a-release/). Once verified, deploy by the
**digest** you verified, never a mutable tag (see [§8](#8-pin-by-digest-for-production)).

## 2. Single node, SQLite

### With `docker run` (hardened)

The image's default command binds `0.0.0.0` **inside the container** so you can front
it with ingress; the host-side port mapping below pins exposure to loopback. Run it
non-root, read-only, with all capabilities dropped:

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| Flag | Why |
|---|---|
| `--user 65532:65532` | run as the non-root `nonroot` UID baked into the distroless image |
| `--read-only` | the root filesystem is immutable; only the data volume and `/tmp` are writable |
| `--tmpfs /tmp` | a writable scratch tmpfs, required because the rootfs is read-only |
| `--cap-drop ALL` | the engine needs no Linux capabilities |
| `--security-opt no-new-privileges` | block privilege escalation via setuid binaries |
| `-v olivares-data:/var/lib/olivares` | persist the data directory (see [§5](#5-operating-notes)) |
| `-p 127.0.0.1:8443:8443` | publish HTTPS (REST + web UI) to **loopback only** |
| `-p 127.0.0.1:8444:8444` | publish gRPC (ingest / ControlPlane API) to loopback only |

Read the one-time setup token from the logs and create the first administrator:

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` accepts the self-signed certificate the engine mints on first boot; replace it
with a real certificate via a reverse proxy ([§6](#6-reverse-proxy--tls-termination))
or your own TLS material. The token is shown **once** and is single-use.

### With Docker Compose

The repository ships a Compose stack that wires the volume, the loopback port mapping
and the same hardening flags as above:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

The base file defaults the image to `docker.io/olivaresai/olivares:latest`; for a
verifiable production deploy set `OLIVARES_IMAGE` in `deploy/compose/.env` to a
digest-pinned reference (see [§8](#8-pin-by-digest-for-production)). Data persists in
the `olivares-data` volume.

## 3. Multi-tenant Postgres

For the multi-tenant topology, layer the Postgres override on top of the base file.
Set the two passwords first, then bring the stack up:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

The override brings up `postgres:16-alpine`, provisions the **least-privilege**
`olivares_app` role and `olivares` database on first init (running the canonical
`deploy/postgres/01-app-role.sql` via `initdb/10-app-role.sh`), and points the engine
at that non-superuser role with `--engine=postgres`. This makes the FORCE-RLS tenant
backstop real: the engine **refuses to start** against a superuser/`BYPASSRLS` role.

:::caution[`sslmode=disable` is for the in-network demo only]
The DSN in the override uses `sslmode=disable` because both containers share a Docker
network. **Production uses TLS with `sslmode=verify-full`.** For a hardened deployment
prefer the Helm chart with a DSN Secret and a managed (or your own) Postgres — see
[§8](#8-pin-by-digest-for-production).
:::

## 4. Disaster-recovery backups

The backup profile produces scheduled, ledger-continuity-safe DR bundles: the store
snapshot plus the signing keys, encrypted under your KEK, with a manifest of the
per-tenant chain tips. Write your passphrase to a file kept **out of the repo and
image**, then run the one-shot `backup` profile:

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

The job shares the engine's data volume, writes the bundle to the `olivares-backups`
volume, and — because the image is distroless — leaves retention to the host: prune old
bundles with a host cron (`find <backups> -name '*.drbundle' -mtime +14 -delete`). Wrap
the run in host cron for a scheduled RPO and **mirror the `olivares-backups` volume
offsite** — a same-host backup is not disaster recovery. Restore and verify with:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

The full RPO/RTO, key-custody and DR-drill procedure lives with the repository's DR
runbook; the higher-level walkthrough is [Back up and restore](/how-to/backup-and-restore/).

## 5. Operating notes

**Probe health from the host, not the container.** The image is **distroless** — it
has no shell and no `curl`, so there is intentionally no in-container `HEALTHCHECK`.
The engine exposes `/livez` and `/readyz` on the HTTPS port; probe them from the host
(or your orchestrator):

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

`/readyz` reachability is the availability signal — wire it into your external
monitoring (see [Monitor with Prometheus](/how-to/monitor-with-prometheus/)).

**The setup token only appears once, in the logs.** First boot prints a single-use
`olst_…` token in the container output. Capture it with
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` (or the Compose equivalent) before
the buffer rotates; it is consumed when you create the first administrator.

**Back up the data directory.** `/var/lib/olivares` (the `olivares-data` volume) holds
the **SQLite store, the audit signing key, and the TLS material**. Losing it loses the
ledger's signing identity and breaks audit continuity, so protect and back up the
volume — use the DR profile in [§4](#4-disaster-recovery-backups), not an ad-hoc copy
of a live store.

## 6. Reverse proxy / TLS termination

Out of the box the engine serves its own **self-signed** certificate, which is fine
for evaluation but not for clients that validate trust. In production, front the
loopback-bound engine with a reverse proxy that terminates TLS with an
operator-provided certificate (from your CA or ACME), and let the proxy be the only
thing exposed on the network.

Because the engine itself speaks TLS, the proxy connects to it over HTTPS on the
loopback port. A minimal nginx server block:

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

The equivalent with Caddy, which provisions a public certificate automatically:

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

Keep the engine's host ports bound to `127.0.0.1` (the defaults above) so only the
proxy is reachable. The gRPC ingest port (`8444`) is for collectors; expose it
deliberately, with its own TLS path, only if you run the distributed topology.

## 7. Upgrades

The data volume persists across container replacements, so an upgrade is: back up,
pull the new pinned tag, recreate the container.

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

Recreating the container does not touch the named volume, so the store, signing key
and TLS material carry over. Always **back up before upgrading**, and re-verify the new
image before recreating.

## 8. Pin by digest for production

Mutable tags (`:26.8.0`, `:latest`) are for evaluation. In production, pin the
**digest** you verified — a digest is immutable and is exactly what you signed off on:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

For Compose, set the digest reference in `deploy/compose/.env`:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

For scale-out and multi-node, use the Helm chart — published as an OCI artifact at
`oci://ghcr.io/olivaresai/charts/olivares`, cosign-signed, and pinned by image digest.
See [Self-host the control plane](/how-to/self-hosting/) for the chart command and
[Install in an air-gapped environment](/how-to/air-gap-install/) for fully
disconnected sites.

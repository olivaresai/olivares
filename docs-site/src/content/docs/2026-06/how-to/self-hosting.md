---
title: Self-host the control plane
description: "Run Olivares AI yourself — single binary, Docker Compose, or
  Kubernetes — with secure defaults: no default credentials, a one-time setup
  token, TLS on by default, and your data never leaving your perimeter."
slug: 2026-06/how-to/self-hosting
---

Olivares AI is **self-host-first**. The whole product is one static binary with the
web UI embedded, so the simplest deployment is a single file; Compose and Kubernetes
paths exist for multi-node and production. Every path shares the same secure
defaults — no default credentials, a one-time setup token, TLS on by default — and
no data leaves your perimeter.

This guide is the deployment **decision page** — the options and their secure
defaults at a glance. For the step-by-step install of each scenario, the
getting-started tutorials walk every path end to end:
[single node (systemd)](/2026-06/tutorials/getting-started/single-node/) ·
[Docker Compose](/2026-06/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/2026-06/tutorials/getting-started/kubernetes/) ·
[air-gapped](/2026-06/tutorials/getting-started/air-gapped/). To verify the artifacts
cryptographically first, see [Verify what you downloaded](/2026-06/how-to/verify-a-release/);
for disconnected sites, see
[Install in an air-gapped environment](/2026-06/how-to/air-gap-install/).

## Secure defaults (all paths)

| Default | Behavior |
|---|---|
| **Credentials** | none. First boot prints a **one-time, single-use setup token** (`olst_…`); you create the first admin with it. |
| **TLS** | on by default. `--insecure` (plaintext) is for localhost development only. |
| **Bind** | the binary binds **loopback** by default; expose it deliberately. |
| **License** | validated **offline** (Ed25519). It is attestation only — it never gates or degrades features. |
| **Telemetry-home** | off. The engine makes no mandatory outbound calls at boot. |

## Option 1 — single binary

Build the one static artifact (pure-Go SQLite store, so no C toolchain) and run it:

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

On first boot the engine prints the setup banner:

```text
=== FIRST-BOOT SETUP ===
No users exist yet. Create the first administrator:
  POST /v1/setup  {"token":"<olst_ token>","email":"you@example.com","password":"..."}
This token is shown ONCE and is single-use.
========================
```

Create the first administrator, then log in:

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

The data directory holds the SQLite database, the audit signing key and the TLS
material — back it up and protect it.

## Option 2 — Docker Compose (single node, SQLite)

The repository ships a Compose stack:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | grep -A4 "FIRST-BOOT SETUP"

# Then open https://localhost:8443 (self-signed TLS by default)
```

For a multi-tenant Postgres backend, set the passwords and layer the Postgres
override:

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[The container default binds inside the container]
The container's default command binds `0.0.0.0` *inside the container* so you can
front it with your ingress; the Compose stack maps the host port to `127.0.0.1`.
There is no bare `docker run` recipe — use Compose (or the Helm chart) so the data
volume, ports and first-boot flow are wired correctly.
:::

## Option 3 — Kubernetes (Helm)

The signed Helm chart deploys the control plane as a **core StatefulSet**
(single-writer; its data directory holds the audit signing key and TLS material) and,
for the distributed topology, a **collectors DaemonSet** that pushes observations to
the core over **gRPC + mTLS**. At release the chart is published to an OCI registry and
cosign-signed, so you verify on install and pin by digest. (The first release is still a
**draft**: until a `chart-v*` tag is cut the registry path is empty, so the command below
is the path you will use once a release is published.)

```bash
helm install olivares \
  oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> --verify \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

The chart's image default is `docker.io/olivaresai/olivares` (Docker Hub — the official
registry), so the `--set image.repository` above only restates it; point it at
`ghcr.io/olivaresai/olivares` instead to pull the same digest from the fallback registry
(no anonymous-pull rate limit). The **chart** artifact itself stays on
`oci://ghcr.io/olivaresai/charts/olivares` — the chart is published to ghcr.io only.

Always deploy **by digest**, never a mutable tag. For a fully disconnected cluster,
mirror the bundle first — see [air-gap install](/2026-06/how-to/air-gap-install/).

## Choosing a topology

| Topology | When | Store | Event bus |
|---|---|---|---|
| **Single binary** | single node, lab, small estate, air-gap | SQLite (embedded) | in-process |
| **Distributed** | multi-host, scale, multi-tenant | Postgres + RLS | in-process + **NATS bridge** (`OLIVARES_BUS_CONFIG`; cross-node delivery is honestly at-most-once) |
| **Air-gapped** | no egress allowed | SQLite or Postgres | in-process (NATS bridge optional inside the perimeter) |

The **data-plane (collectors) always runs on your infrastructure** — the control
plane is the only thing you choose where to host. The
[architecture overview](/2026-06/explanation/architecture/overview/) explains the trade-offs.

## Connect real sources

A fresh install has an empty estate. Wire real sources (Postgres pgAudit,
CloudTrail, OpenTelemetry from agents, eBPF) so the access map populates — see
[connect a source](/2026-06/how-to/connect-a-source/) and
[connect Claude Code](/2026-06/how-to/connect-claude-code/). For the configuration surface,
see the [configuration reference](/2026-06/reference/configuration/).

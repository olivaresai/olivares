---
title: Self-host Olivares AI
description: >-
  Run Olivares AI yourself — single binary, Docker Compose, or Kubernetes — with
  secure defaults: no default credentials, a one-time setup token, TLS on by
  default, and no mandatory telemetry or control-plane egress by default — what
  crosses your perimeter is what you configure to cross it, from calls to your model
  APIs to the SIEM/webhook outputs you wire.
---

Olivares AI is **self-host-first**. The whole product is one static binary with the
web UI embedded, so the simplest deployment is a single file; Compose and Kubernetes
paths exist for multi-node and production. Every path shares the same secure
defaults — no default credentials, a one-time setup token, TLS on by default — and no
mandatory telemetry and no control-plane egress by default: what crosses your perimeter
is what **you** configure to cross it — calls to your model APIs, the SIEM/webhook outputs
you wire, an external embedding provider if you provision one.

This guide is the deployment **decision page** — the options and their secure
defaults at a glance. For the step-by-step install of each scenario, the
getting-started tutorials walk every path end to end:
[single node (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[air-gapped](/tutorials/getting-started/air-gapped/). To verify the artifacts
cryptographically first, see [Verify what you downloaded](/how-to/verify-a-release/);
for disconnected sites, see
[Install in an air-gapped environment](/how-to/air-gap-install/).

## Secure defaults (all paths)

| Default | Behavior |
|---|---|
| **Credentials** | none. First boot prints a **one-time, single-use setup token** (`olst_…`); you create the first admin with it. |
| **TLS** | on by default. `--insecure` (plaintext) is for localhost development only. |
| **Bind** | the binary binds **loopback** by default; expose it deliberately. |
| **License** | In the open (AGPL) binary: validated **offline** (Ed25519), attestation only — it never gates or degrades the open product, and that does not change. Commercial add-ons are a paid-term right delivered as **subscription access to the enterprise repositories** (the SUSE/Novell model): obtaining them and receiving their updates — security updates included — requires that entitlement. Air-gapped estates are served the same way SUSE serves them, through a local mirror that still carries the entitlement. |
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
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected. The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
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
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

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
  --version <chart-version> \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> The published chart is **cosign-signed over the OCI manifest**, not GPG-signed: the release
> pipeline emits no `.prov` layer, so `helm --verify` cannot check it. Verify with `cosign verify`
> against the `release-chart.yml@refs/tags/chart-v*` identity — see `deploy/helm/README.md`.

The chart's image default is `docker.io/olivaresai/olivares` (Docker Hub — the official
registry), so the `--set image.repository` above only restates it; point it at
`ghcr.io/olivaresai/olivares` instead to pull the same digest from the fallback registry
(no anonymous-pull rate limit). The **chart** artifact itself stays on
`oci://ghcr.io/olivaresai/charts/olivares` — the chart is published to ghcr.io only.

Always deploy **by digest**, never a mutable tag. For a fully disconnected cluster,
mirror the bundle first — see [air-gap install](/how-to/air-gap-install/).

## Choosing a topology

| Topology | When | Store | Event bus |
|---|---|---|---|
| **Single binary** | single node, lab, small estate, air-gap | SQLite (embedded) | in-process |
| **Distributed** | multi-host, scale, multi-tenant | Postgres + RLS | in-process + **NATS bridge** (`OLIVARES_BUS_CONFIG`; cross-node delivery is honestly at-most-once) |
| **Air-gapped** | no egress allowed | SQLite or Postgres | in-process (NATS bridge optional inside the perimeter) |

The **data-plane (collectors) always runs on your infrastructure** — the control
plane is the only thing you choose where to host. The
[architecture overview](/explanation/architecture/overview/) explains the trade-offs.

## Connect real sources

A fresh install has an empty estate. Wire real sources (Postgres pgAudit,
CloudTrail, OpenTelemetry from agents, eBPF) so the access map populates — see
[connect a source](/how-to/connect-a-source/) and
[connect Claude Code](/how-to/connect-claude-code/). For the configuration surface,
see the [configuration reference](/reference/configuration/).

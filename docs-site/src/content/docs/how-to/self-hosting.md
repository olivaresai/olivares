---
title: Self-host Olivares AI
description: >-
  Run Olivares AI yourself — single binary, Docker Compose, or Kubernetes — with
  secure defaults: no default credentials, a one-time setup token, TLS on by
  default, and no mandatory telemetry or control-plane egress by default — what
  crosses your perimeter is what you configure to cross it, from calls to your model
  APIs to the SIEM/webhook outputs you wire.
---

> Business deployment packages are supplied through the Business channel; their publication is unverified here. Verify the chart package and its publisher using the channel instructions before using the local chart below. The flat manifest example uses a Business-supplied file named `business-install.yaml`. Air-gapped installation requires Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

The next release is <!-- release -->`0.1`<!-- /release -->; its GitHub release is not published yet. The commands below describe the planned artifacts. Build from source until publication, then verify each artifact before use. See <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release --> for the observed publication state.

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

**NATS event delivery:** The Core NATS bridge and NATS JetStream require Business Identity & Scale. Community uses in-process delivery.

## Secure defaults (all paths)

| Default | Behavior |
|---|---|
| **Credentials** | none. First boot prints a **one-time, single-use setup token** (`olst_…`); you create the first admin with it. |
| **TLS** | on by default. `--insecure` (plaintext) is for localhost development only. |
| **Bind** | **every interface** (`:8443`, `:8444`) by default — this is a server. Pass `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444` to restrict it to this host. |
| **License** | In the open (AGPL) binary: validated **offline** (Ed25519), attestation only — it never gates or degrades the open product, and that does not change. Business includes Regulated Operations, AI Runtime Security, Compliance Packs, and Identity & Scale in one subscription. Customers can enable or disable each family. Business and Enterprise are delivered as binaries; paid source stays private. |
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
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

Create the first administrator, then log in:

```bash
curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

The data directory holds the SQLite database, the audit signing key and the TLS
material — back it up and protect it.

### Custom data directory (`layout: custom`)

The default native layout is `/var/lib/olivares`. The signed service adapter
(`install.sh --data-dir`, `scripts/install-service.sh`) admits a **custom**
data directory by **shape**, not by allowlist. The ownership manifest records
`"layout": "custom"` (`CHANGELOG.md` `[26.9.0]` Added; `INSTALL.md`).

SDD 04 §6: every configurable field declares owner, schema, accepted sources
and validator. Here the adapter owns the dedicated directory; the operator
owns the parent. These are the adapter's own refusal strings
(`scripts/install-service.sh`):

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): choose a real directory` |

`install-agentops.sh` uses the same two-level rule for `OLIVARES_DATA_DIR`:
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`. It honours
`OLIVARES_DATA_DIR` and an explicitly selected `OLIVARES_WORKSPACE_DIR`.

The unit renders the directory as `ReadWritePaths=<data-dir>`. It sets
`ProtectHome=false` and `PrivateTmp=false`, so a directory under `/home`,
`/root`, `/run/user`, `/tmp` or `/var/tmp` needs no extra mount. The installer
warns that `/tmp` and `/var/tmp` may be cleared on boot or on a timer.

`olivares uninstall` admits that custom directory only when the unit at its
indexed path executes the engine with it, or when a preserve already left an
uninstall witness beside the service config. Diagnose the recorded AgentOps
layout with `olivares doctor` — see
[Troubleshooting](/how-to/troubleshooting/#agentops-layout-check).

For package installs the default remains `/var/lib/olivares`. See
[Install from a package](/how-to/install-from-packages/). On macOS see
[Install with Homebrew](/how-to/install-from-homebrew/).

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

Business deployment packages are supplied through the Business channel; their publication is unverified here. Verify the chart package and its publisher using the channel instructions before using the local chart below. The flat manifest example uses a Business-supplied file named `business-install.yaml`. Air-gapped installation requires Enterprise.

```bash
# Set these inputs from the authenticated Business channel after verification.
helm upgrade --install olivares "$BUSINESS_CHART_PACKAGE" \
  --set image.repository="$BUSINESS_IMAGE_REPOSITORY" \
  --set image.digest="$BUSINESS_IMAGE_DIGEST"
```

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

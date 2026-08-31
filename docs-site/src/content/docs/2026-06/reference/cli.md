---
title: "CLI reference: olivares"
description: Verified subcommands and flags for the single olivares binary,
  including the secure-by-default serve options.
slug: 2026-06/reference/cli
---

Olivares AI ships as one static Go binary named `olivares`. The same artifact is the engine, the embedded web UI (served from the same origin as the API), and the edge collector — the role is selected by the subcommand you run. This page documents only the subcommands and flags confirmed in the binary's source; it is not an exhaustive surface, and the surface is still moving (see [Stability](#stability) at the end).

For how to obtain and run the binary, see [Self-hosting](/2026-06/how-to/self-hosting/). For configuration that lives in environment variables rather than flags, see [Configuration](/2026-06/reference/configuration/).

## Overview

```
olivares <subcommand> [flags]
```

The root command groups these subcommands:

| Subcommand | Purpose |
|---|---|
| `version` | Print the version and build metadata. |
| `serve` | Run the control plane: REST + web + gRPC, TLS on by default. |
| `collector` | Run as an edge collector that pushes observations to a remote core (distributed topology). |
| `openapi` | Print the engine's OpenAPI 3.1 document to stdout. |
| `license` | Operate on a commercial license file. |
| `audit` | Operate on the append-only audit ledger. |

This page covers `version`, `serve`, `collector`, and `openapi` in detail. The `license` and `audit` subcommands exist but their flag surfaces are still settling; run them with `--help` against your build rather than relying on documentation here.

:::note[Single binary, multiple roles]
There is no separate "server" and "agent" download. `olivares serve` is the engine; `olivares collector` is the data-plane collector for the distributed topology. Both load the same source connectors the same way — only where the observations go differs. See [Architecture](/2026-06/explanation/architecture/overview/).
:::

## olivares version

Prints the version, commit, build date, OS/arch, and Go runtime version to stdout.

```sh
olivares version
```

The version string is injected at build time. A build from a working tree that was not produced by a tagged release reports a development version (for example `dev`), so do not treat the version string as a guarantee of provenance — verify releases with the signed artifacts instead. See [Verify a release](/2026-06/how-to/verify-a-release/).

## olivares serve

Runs the control plane: the HTTP server (REST API plus the embedded web UI on the same origin) and the gRPC server. **TLS is on by default**, the listeners bind **loopback by default**, and there are **no default credentials**.

```sh
olivares serve [flags]
```

### Secure defaults

These are properties of `serve`, not opt-ins:

* **TLS is on by default.** If no certificate is supplied, the engine generates a self-signed certificate in the data directory and logs its SHA-256 fingerprint; clients must trust or pin it. The gRPC server fails closed: outside `--insecure` it will not start in plaintext.
* **Loopback by default.** Both the HTTP and gRPC listeners default to `127.0.0.1`. Exposing the control plane beyond the local host is a deliberate change you make by setting a non-loopback bind and fronting it with your own ingress.
* **No default credentials.** On a fresh install with no users, the engine mints a **one-time, single-use setup token** (prefix `olst_`) and prints it to **stdout only** (never to the logs). You create the first administrator by posting that token to the setup endpoint, then log in. See [First-boot setup](#first-boot-setup).

### Flags

| Flag | Default | Description |
|---|---|---|
| `--listen` | `127.0.0.1:8443` | HTTP listen address (REST + embedded web UI). |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC listen address. |
| `--engine` | `sqlite` | Store engine: `sqlite` or `postgres`. |
| `--data-dir` | `$OLIVARES_DATA_DIR` or `./olivares-data` | Data directory (SQLite file, generated TLS material, etc.). |
| `--checkpoint-interval` | `1h` | How often to write a signed audit checkpoint over every tenant's hash chain. `0` disables checkpointing. |
| `--insecure` | off | Serve plaintext HTTP/gRPC. Dangerous; localhost development only. |
| `--seed-demo` | off | Load a synthetic sample estate for demos/E2E. Demo-only; refuses a non-loopback bind (see below). |

The default store is SQLite (pure-Go, single-node, suitable for air-gapped installs). Selecting `postgres` is what you do for multi-tenant or scale-out deployments, where row-level security provides the tenant backstop. See [Configuration](/2026-06/reference/configuration/) and [Self-hosting](/2026-06/how-to/self-hosting/).

:::caution[`--insecure` is for localhost development only]
`--insecure` serves plaintext HTTP and gRPC. Bearer tokens travel in the clear on a plaintext transport, so never use this flag on any address reachable beyond the local host. Outside `--insecure`, the gRPC server refuses to start without TLS rather than silently downgrading.
:::

### `--seed-demo` is demo-only and refuses non-loopback

`--seed-demo` provisions a **synthetic, fabricated** estate together with a demo administrator whose password is **public** (it lives in the source tree). It exists purely to make the web UI and end-to-end tests render against live-shaped data.

Because the demo credential is public, `serve` **refuses to start with `--seed-demo` on any non-loopback bind** and exits with an error directing you to bind `127.0.0.1` or to run a real install without the flag. Treat `--seed-demo` as throwaway: use a disposable data directory, and never point it at data you care about.

:::danger[Never run `--seed-demo` as a real install]
The demo administrator has a publicly known password. A real install is `serve` **without** `--seed-demo`, where the engine mints a one-time setup token and you create your own administrator. Do not mix the two: a demo data directory is not a production data directory.
:::

### First-boot setup

On the first boot of an install that has no users, `serve` prints a block to stdout containing the one-time setup token (prefix `olst_`) and the request you need to bootstrap the first administrator. The flow is:

1. Read the `olst_` token from the engine's stdout (with the container deployment, read it from the container logs).
2. Create the first administrator by posting the token, an email, and a password to the setup endpoint (`POST /v1/setup`).
3. Log in (`POST /v1/auth/login`) to obtain a session token (prefix `olvs_`).

The setup token is shown once and is single-use; once a user exists, no token is minted. Olivares AI uses **opaque** bearer tokens (not JWTs); API keys carry the prefix `olvk_`. For the full authentication contract and tenant resolution rules, see the [Security model](/2026-06/explanation/security/security-model/) and the [API reference](/reference/api/).

### Run it

```sh
# Build, then run (there is no "task serve" / "task run" target).
task build
./bin/olivares serve
# Read the one-time olst_ setup token from this process's stdout.
```

Or run the container deployment and read the setup token from the logs; see [Self-hosting](/2026-06/how-to/self-hosting/).

## olivares collector

Runs the binary as an **edge collector** for the distributed topology. A collector loads the source connectors named in your sources configuration locally and **pushes** their observations to a remote core over gRPC. It opens **no inbound listener** — the collector is the secure-default data plane: it dials out, it does not accept connections.

```sh
olivares collector --core-addr host:port [flags]
```

The collector authenticates to the core in two layers: a bearer token holding an ingest principal, and — when the core enforces mutual TLS — a collector client certificate. This is how the data plane runs on customer infrastructure while a central core aggregates: a failing collector never sits in the data path of any agent.

:::note[Which sources a collector loads]
Both `serve` and `collector` wire their source connectors from the same configuration, read from the environment before the runtime starts. An unconfigured or empty source warns honestly rather than failing the process. Configuring sources is covered in [Connect a source](/2026-06/how-to/connect-a-source/) and [Connect Claude Code](/2026-06/how-to/connect-claude-code/); the configuration mechanism is in [Configuration](/2026-06/reference/configuration/).
:::

The collector subcommand is the **mechanism** for the distributed path. The packaging around it (a fleet of collectors, signed charts, OCI images) is part of the deployment story rather than this CLI page — see [Architecture](/2026-06/explanation/architecture/overview/) and [Self-hosting](/2026-06/how-to/self-hosting/).

## olivares openapi

Prints the engine's OpenAPI 3.1 document to stdout, without needing a running server.

```sh
olivares openapi > openapi.json
```

This emits the same contract the engine serves at `GET /openapi.json`, deterministically indented so the output diffs cleanly. It is the source of truth for the rendered [API reference](/reference/api/) and for the web client's typed code generation. The served REST contract covers the core paths; some module routes are reachable but deliberately not part of the served OpenAPI document, with their contracts expressed as typed interfaces instead (see [API reference](/reference/api/) and [Modules overview](/2026-06/reference/modules/overview/)).

## Stability

This is a pre-1.0 product in active development. The subcommands and flags above are confirmed in the current binary, but the full CLI surface is still evolving: subcommands, flags, defaults, and output formats may change before a stable release. When in doubt, run `olivares <subcommand> --help` against the exact build you deployed, and treat that as authoritative over any document. For what is implemented today versus planned, see [Honesty and limits](/2026-06/start/honesty-and-limits/). The REST/gRPC API surface itself is governed by the [API stability policy](/2026-06/reference/api-stability/).

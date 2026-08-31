---
title: Configuration reference
description: "The verified configuration surface of the Olivares AI control
  plane: serve flags, environment variables, store selection, and the secure
  defaults that ship out of the box."
slug: 2026-06/reference/configuration
---

This page documents the configuration surface of the control plane engine — the single Go binary named `olivares`. It covers the flags accepted by the `serve` subcommand, the environment variables the engine reads at boot, how the store and policy decision point are selected, and the secure defaults that are in effect with no configuration at all.

Everything listed here is taken from the engine's own command definitions and composition root. Where a setting cannot be confirmed in source it is not listed. For the conceptual security posture behind these defaults, see [the security model](/2026-06/explanation/security/security-model/); for the runnable end-to-end path, see [self-hosting](/2026-06/how-to/self-hosting/).

:::note[Configuration philosophy]
The engine is configured by flags and a small set of environment variables, not by a sprawling config file. Secrets that wire real sources stay in operator-held files referenced by environment variable — never in the store. The defaults are chosen to fail closed: loopback binds, TLS on, no default credentials.
:::

## The `serve` subcommand

`olivares serve` runs the REST/web HTTP server and the gRPC server in one process, with the web UI served from the same origin as the API. The flags below are the verified configuration inputs to that command.

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | HTTP listen address (REST API + embedded web UI). |
| `--grpc-listen` | `127.0.0.1:8444` | gRPC listen address (control-plane / collector ingest API). |
| `--data-dir` | `$OLIVARES_DATA_DIR` or `./olivares-data` | Data directory: audit signing key, TLS material, and (for SQLite) the store file. |
| `--engine` | `sqlite` | Store engine: `sqlite` or `postgres`. |
| `--dsn` | empty (SQLite file in the data dir) | Store connection string. |
| `--checkpoint-interval` | `1h` | How often a signed audit checkpoint is written over every tenant chain. `0` disables. |
| `--insecure` | off | Serve plaintext HTTP/gRPC. Dangerous; localhost development only. |
| `--seed-demo` | off | Load a synthetic sample estate for demos/E2E. Refuses to start on a non-loopback bind. |

TLS is on by default. With no `--tls-cert`/`--tls-key` supplied, the engine ensures a self-signed certificate in the data directory once, up front, before any listener accepts a connection, so both the HTTP and gRPC servers use the same certificate and neither falls back to plaintext. When it generates a self-signed certificate it logs the SHA-256 fingerprint so clients can pin or trust it.

:::caution[`--insecure` is loopback-only by intent]
`--insecure` serves plaintext HTTP and gRPC, which would expose bearer tokens on the wire. The gRPC path **fails closed**: outside `--insecure` the server refuses to construct a plaintext listener rather than degrade silently. Use `--insecure` only against `127.0.0.1` during local development, never on a published address.
:::

:::danger[`--seed-demo` is synthetic and self-protecting]
`--seed-demo` provisions a demo administrator with a **public, source-tree password** and fabricated estate data. It is for demos and E2E only. The engine refuses to start it on a non-loopback listener: if either `--listen` or `--grpc-listen` is not a loopback address the command exits with an error. Use a throwaway data directory; never point it at real data.
:::

A full flag listing — including the Postgres-only and mutual-TLS flags used in distributed deployments — is in the [CLI reference](/2026-06/reference/cli/). This page documents the common configuration surface; some advanced flags govern multi-node topologies described in [the architecture overview](/2026-06/explanation/architecture/overview/).

## Environment variables

The engine reads a small number of environment variables at boot. The ones below are confirmed in the composition root and policy wiring.

### Data directory

| Variable | Effect |
| --- | --- |
| `OLIVARES_DATA_DIR` | Default data directory when `--data-dir` is not given. Falls back to `./olivares-data`. |

The data directory holds the audit signing key, the TLS certificate and key, and — for the SQLite engine — the store file. Persist it across restarts.

### Wiring real sources

| Variable | Effect |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | Path to a JSON file that wires real observation sources and identity roster providers before the engine starts. |

`OLIVARES_SOURCES_CONFIG` is the single input through which non-demo signal sources and identity roster providers are resolved. It is the operator's secret-bearing configuration and is deliberately kept out of the store. The engine reads it during boot and registers every source **before** the runtime starts.

The handling is honest rather than fail-fast:

* A **missing** variable yields an empty configuration, and the engine warns that nothing real is wired.
* An **unreadable or invalid-JSON** file warns and yields an empty configuration — it never aborts the boot.
* A configured-but-**empty** source list warns that no connector will ingest, so the estate runs on no live traffic, rather than silently appearing healthy.
* An empty **identity** list warns that the roster stays empty and roster sync is a no-op.

This is by design: an unconfigured source surfaces a warning instead of crashing the control plane or pretending to work. To actually populate the access map, configure at least one source — see [connect a source](/2026-06/how-to/connect-a-source/) and, for the cooperative Claude Code path over OpenTelemetry and MCP, [connect Claude Code](/2026-06/how-to/connect-claude-code/).

### Authorization decision point (PDP)

The authorization policy decision point is selected at the composition root by environment. The native attribute-based access control (ABAC) engine and role-based access control (RBAC) always govern; the external PDP, when selected, is an additional **restrict-only** layer that can never widen access.

| Variable | Effect |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | Selects the external PDP: `cedar`, `opa`, or `none` (empty or `none` means native ABAC only). |
| `OLIVARES_PDP_CEDAR_FILE` | Cedar engine only: path to the operator's Cedar policy file. |
| `OLIVARES_PDP_OPA_URL` | OPA engine only: base URL of the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_PATH` | OPA engine only: decision path queried under that endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | OPA engine only: bearer token for the OPA endpoint. |

Two adapters sit behind one seam: an **embedded Cedar** evaluator (the primary, pure-Go path) and an **OPA-over-HTTP** adapter. The operator chooses one engine; both can only restrict, never widen, the decision the built-in RBAC already made.

:::note[A bad policy never un-governs the plane]
If `OLIVARES_PDP_ENGINE` selects an engine but its configuration is invalid — an unreadable Cedar file, a malformed OPA target — the engine **disables only the external PDP**, keeps the native ABAC engine and RBAC enforcing, and logs loudly. A broken policy file never silently leaves requests un-governed and never crashes the control plane.
:::

For the deny-by-default model, the privileged nature of viewing the access graph, and how every authorization read is audited, see [the security model](/2026-06/explanation/security/security-model/).

## Store selection

The engine selects its store engine from `--engine`.

| Engine | When to use | Notes |
| --- | --- | --- |
| `sqlite` (default) | Single binary, single node, air-gapped installs. | Pure-Go embedded store; zero external dependencies. With no `--dsn`, the store file lives in the data directory. |
| `postgres` | Multi-tenant and scale-out deployments. | Adds row-level-security tenant isolation. Requires a least-privilege application role. |

SQLite is the default and needs no external service. Choosing `postgres` opts into the row-level-security backstop that isolates tenants: the engine **refuses to start** against a Postgres superuser or `BYPASSRLS` role unless that guard is explicitly overridden, because such a role would disable the tenant isolation backstop. The Compose Postgres overlay provisions the least-privilege application role on first init so this backstop is real.

:::tip[The default store is intentionally boring]
SQLite is not a toy default here. It is the air-gap-ready, zero-dependency store for the single-node topology, and it is the store the one-command Docker Compose deployment runs. Move to Postgres when you need multi-tenant isolation or horizontal scale, not before. See [self-hosting](/2026-06/how-to/self-hosting/) and [the architecture overview](/2026-06/explanation/architecture/overview/).
:::

## Audit checkpoint interval

The audit ledger is append-only, hash-chained, and anchored by Ed25519-signed checkpoints. `--checkpoint-interval` controls how often a signed checkpoint is written over every tenant chain (default `1h`; `0` disables checkpointing). A final shutdown checkpoint is written before the store closes, so the chain is anchored at clean shutdown as well as on the interval. The signed export and forwarding path is covered in [forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/).

## Secure defaults

These are the postures in effect with no configuration beyond `serve`. They are the product's default security stance, not optional hardening.

| Area | Default | What it means |
| --- | --- | --- |
| Credentials | None shipped | No default username or password exists. On first boot with no users, the engine mints a single-use setup token and prints it to standard output only — never to the logs. |
| First-boot setup | One-time token | The administrator creates the first user with that token, then logs in. The token is shown once and is single-use. |
| Transport | TLS on | HTTP and gRPC serve over TLS by default; a self-signed certificate is generated in the data directory if none is supplied, and its fingerprint is logged. |
| Bind address | Loopback | `--listen` and `--grpc-listen` default to `127.0.0.1`. The engine binds the local host until you deliberately publish it. |
| Plaintext mode | Off | `--insecure` is the only way to serve plaintext, and the gRPC path fails closed rather than degrade. Intended for localhost development only. |
| Demo seeding | Off | `--seed-demo` is off by default and refuses any non-loopback bind because it mints a public-password demo administrator. |
| Telemetry home | Off | The engine does not phone home. There is no telemetry-to-vendor channel; outbound connections exist only to the sources you configure. This is what makes the [air-gapped install](/2026-06/how-to/air-gap-install/) possible with zero egress. |

:::caution[Loopback by default, exposed by intent]
The default loopback binds mean the engine is not reachable off-host until you change them. When you do publish it — for example by mapping a host port in Docker Compose — that is a deliberate operator decision, and TLS is already on to protect it. Do not pair a published bind with `--insecure`.
:::

### First boot, in practice

On a fresh install the engine prints a `FIRST-BOOT SETUP` block to standard output containing the one-time setup token. The administrator uses it to create the first user, then authenticates. Under Docker Compose the token is read from the container logs:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | grep -A4 "FIRST-BOOT SETUP"
# then open https://localhost:8443 (self-signed TLS by default)
```

The setup endpoint and the login endpoint are part of the product's OpenAPI contract; see the [API reference](/reference/api/). The opaque session and API-key token model behind them is described in [the security model](/2026-06/explanation/security/security-model/).

## What this page does not cover

This is the verified, common configuration surface. It does **not** enumerate every advanced flag for multi-node and mutual-TLS topologies — those belong to the distributed and air-gapped deployments described in [the architecture overview](/2026-06/explanation/architecture/overview/) and listed in full in the [CLI reference](/2026-06/reference/cli/). Where a setting is design-stage or topology-specific, it is documented there rather than presented here as a stable knob.

For the boundaries of what the product observes and where coverage is tiered, read [honesty and limits](/2026-06/start/honesty-and-limits/).

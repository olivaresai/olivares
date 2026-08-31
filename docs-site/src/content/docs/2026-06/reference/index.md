---
title: Reference
description: "The information-oriented reference: the REST API, the event bus,
  the modules catalog, the CLI, and configuration — precise and exhaustive,
  nothing inferred."
slug: 2026-06/reference
---

Reference is **information-oriented**. Its job is to be precise and complete, not
to teach or persuade: it states what the interfaces are, what their inputs and
outputs are, and what the defaults are — and stops there. The prose is dry on
purpose. If you want to learn the system by doing, start with the
[tutorial](/2026-06/tutorials/zero-to-graph/); if you want to accomplish a specific task,
use a [how-to guide](/2026-06/how-to/connect-a-source/); if you want to understand *why*
the system is built the way it is, read the
[explanation](/2026-06/explanation/architecture/overview/). This section is for when you
are building against the product and need the exact contract.

Most of what follows is generated or hand-derived **directly from the product's
own source artifacts**, so the reference cannot quietly drift from what the engine
actually serves. Where a capability is design-stage or post-v1, the relevant page
says so plainly; see [Honesty & limits](/2026-06/start/honesty-and-limits/) for the
overall contract.

## The four reference areas

| Area | What it documents | Source of truth |
|---|---|---|
| **[REST API](/reference/api/)** | The control-plane HTTP API: auth, setup, tenancy, agents, the R/RW access map, tokens, and the audit ledger. | The product's **OpenAPI 3.1** contract (24 core paths), rendered at build time from the real file — not a copy. |
| **[Stability policy](/2026-06/reference/api-stability/)** | Versioning, stability tiers, deprecation/sunset signalling and the minimum support windows for the API, the provider and the client SDKs. | The in-code deprecation table and its build-failing window tests. |
| **[Event bus](/2026-06/reference/events/)** | The internal event bus: the event envelope, the first-party event types, and the observation payloads connectors lift onto it. | An **AsyncAPI 3.0** contract, hand-derived from the Go SDK. |
| **[Modules catalog](/2026-06/reference/modules/overview/)** | The 23 product modules — what each is, its status, and which routes (if any) it exposes outside the core API. | The product capability catalog and the typed module interfaces. |
| **[CLI](/2026-06/reference/cli/)** | The `olivares` binary and its subcommands — `serve`, `collector`, `audit`, `license`, `openapi`, `version` — and their flags. | The compiled command definitions. |
| **[Configuration](/2026-06/reference/configuration/)** | Environment variables and runtime options: the data directory, source wiring, the authorization engine, and ledger signing. | The engine's configuration loaders. |

## REST API

The [REST API reference](/reference/api/) is rendered at build time from the
product's **OpenAPI 3.1** contract — the same document the engine serves at its
own `/openapi.json` endpoint. Nothing is transcribed by hand, so the rendered
reference is the contract. It covers the credential-free first-boot flow
(`POST /v1/setup` with the one-time setup token, then `POST /v1/auth/login`),
identity and tenancy, agents, the read/write access map
(`GET /v1/access-edges`; its reconciled least-privilege *drift* is served by the
access-map module rather than the core surface), token management, and the audit
ledger.

The contract describes **24 core paths**. That is deliberate: it is the stable,
versioned surface of the control plane, not every route the engine can answer.
What "stable" commits to — versioning, deprecation signalling and minimum
support windows — is the [API stability policy](/2026-06/reference/api-stability/).

:::note[Some module routes are intentionally outside the OpenAPI doc]
A handful of module-specific routes — for example the access-map module's
`/v1/m/accessmap/graph`, `/v1/m/accessmap/neighbors` and `/v1/m/accessmap/drift`
— are reachable on a running engine but are **deliberately not** part of the
served OpenAPI document. Their contracts live in the typed Go and TypeScript
interfaces that the UI and SDK consume, where they can evolve without implying a
stability guarantee on the core API surface. The least-privilege result is the
`drift` route; there is no separate `diff` endpoint.
:::

### gRPC mirror (`olivares.api.v1`)

The control plane also exposes a **gRPC** surface — the `ControlPlane` service in
the versioned proto package `olivares.api.v1`. It is a **focused, frozen mirror**
of a subset of the REST contract above (server info, agent list/get/create, audit
verify), used where a typed binary contract is preferred (for example collectors).
It mirrors the REST contract rather than extending it; the OpenAPI document remains
the canonical surface for the full API.

## Event bus

The [event-bus reference](/2026-06/reference/events/) is an **AsyncAPI 3.0** contract. The
bus is **in-process by default** — connectors lift normalized observations onto
it as typed events, and modules and output connectors subscribe **by event type**
and react, without any of them calling each other directly. A distributed binding
over NATS is optional, not required.

The contract is **hand-derived from the Go SDK**, not generated: the authoritative
definitions are the event envelope, the first-party event types, and the
observation payloads (the agent→resource access observations, cost samples, and
finding reports). Where the bus does not yet formalize something, the reference
says so rather than inventing it.

## Modules catalog

The [modules catalog](/2026-06/reference/modules/overview/) enumerates the **28 modules**
that sit on top of the core engine. The differentiating module is the **R/RW
access map** with its **Permitted-vs-Observed** diff: it reads from logs, OTEL and
(as a non-cooperative backstop) eBPF rather than sitting in the data path, and it
stores only the relation *which agent can read or write which resource* — never
payloads, secrets, or PII.

The catalog is honest about status and coverage. Passive observation is **tiered**
by store type — clean for SQL, object and warehouse stores; lossy for document and
vector stores; impossible without cooperation for in-memory or embedded stores —
and the catalog marks where a module is design-stage. Module **XXIII (model
fine-tuning)** is the only module that is **post-v1**.

## CLI

The [CLI reference](/2026-06/reference/cli/) documents the single `olivares` binary
and its subcommands. The one you run to operate the control plane is `serve`,
which starts the HTTP (REST + embedded web UI) and gRPC listeners; **TLS is on by
default**. Other subcommands cover the collector, the audit ledger (`verify`,
`checkpoint`, `export`), license tooling, and emitting the OpenAPI document.

:::caution[Build first, then run]
There is no `task run` or bare `docker run` shortcut. You either build and invoke
the binary directly — `task setup`, `task build`, then `./bin/olivares serve`
— or bring it up with the provided Compose file and read the one-time setup token
from the logs. The CLI page lists the verified `serve` flags and their defaults.
:::

## Configuration

The [configuration reference](/2026-06/reference/configuration/) lists the environment
variables and runtime options that shape a deployment. The load-bearing ones are
the data directory (`OLIVARES_DATA_DIR`), the real (non-demo) source wiring read
from `OLIVARES_SOURCES_CONFIG` before the engine starts, and the authorization
engine selector `OLIVARES_PDP_ENGINE` (`cedar`, `opa`, or `none`).

Two design rules carry through the configuration surface. An **unconfigured source
warns honestly** rather than failing the engine. And the authorization seam **only
ever restricts, never widens**: RBAC is deny-by-default, viewing the access graph
is a privileged action, and every such read is audited.

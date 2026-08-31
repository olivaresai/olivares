---
title: "Reference"
description: "The information-oriented reference: the REST API, the event bus, the modules catalog, the CLI, and configuration — precise and exhaustive, nothing inferred."
---

Reference is **information-oriented**. Its job is to be precise and complete, not
to teach or persuade: it states what the interfaces are, what their inputs and
outputs are, and what the defaults are — and stops there. The prose is dry on
purpose. If you want to learn the system by doing, start with the
[tutorial](/tutorials/zero-to-graph/); if you want to accomplish a specific task,
use a [how-to guide](/how-to/connect-a-source/); if you want to understand *why*
the system is built the way it is, read the
[explanation](/explanation/architecture/overview/). This section is for when you
are building against the product and need the exact contract.

Most of what follows is generated or hand-derived **directly from the product's
own source artifacts**, so the reference cannot quietly drift from what the engine
actually serves. Where a capability is design-stage or post-v1, the relevant page
says so plainly; see [Honesty & limits](/start/honesty-and-limits/) for the
overall contract.

## The reference areas

| Area | What it documents | Source of truth |
|---|---|---|
| **[REST API](/reference/api/)** | The control-plane HTTP API: auth, setup, tenancy, agents, the R/RW access map, tokens, and the audit ledger. | The product's **OpenAPI 3.1** contract (53 core paths), rendered at build time from the real file — not a copy. |
| **[Module routes (beta)](/reference/api-beta/)** | The product's module routes (`/v1/m/<ns>/…`) — finops, compliance, governance, sessions, models, knowledge, … — as a separate **beta** OpenAPI document. | The same OpenAPI 3.1 contract, reflected at build time from the routes the modules register. |
| **[Stability policy](/reference/api-stability/)** | Versioning, stability tiers, deprecation/sunset signalling and the minimum support windows for the API, the provider and the client SDKs. | The in-code deprecation table and its build-failing window tests. |
| **[gRPC](/reference/grpc/)** | The engine's gRPC mirror and the versioned plugin wire contract every out-of-process connector and module speaks. | The `grpc.ServiceDesc` registration tables the servers hand to gRPC. |
| **[Event bus](/reference/events/)** | The internal event bus: the event envelope, the first-party event types, and the observation payloads connectors lift onto it. | An **AsyncAPI 3.0** contract, hand-derived from the Go SDK. |
| **[Console screens](/reference/console/)** | Every route the console publishes, with the RBAC permission it requires and the reference page its in-product help link opens. | The console's route census, pinned against the built router. |
| **[Modules catalog](/reference/modules/overview/)** | The 30 product modules — what each is, its status, and which routes (if any) it exposes outside the core API. | The product capability catalog and the typed module interfaces. |
| **[CLI](/reference/cli/)** | The `olivares` binary and its subcommands — `serve`, `collector`, `audit`, `license`, `openapi`, `version` — and their flags. | The compiled command definitions. |
| **[Configuration](/reference/configuration/)** | Environment variables and runtime options: the data directory, source wiring, the authorization engine, and ledger signing. | The engine's configuration loaders. |

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

The contract describes **53 core paths**. That is deliberate: it is the stable,
versioned surface of the control plane, not every route the engine can answer.
What "stable" commits to — versioning, deprecation signalling and minimum
support windows — is the [API stability policy](/reference/api-stability/).

:::note[Module routes are a separate, beta contract]
The module routes — for example the access-map module's
`/v1/m/accessmap/graph`, `/v1/m/accessmap/neighbors` and `/v1/m/accessmap/drift`
— are **not** part of the 53-path stable core document. They are published as a
separate **beta** OpenAPI document at [`/reference/api-beta/`](/reference/api-beta/)
(served at `/openapi.beta.json`, reflected from the routes the modules actually
register), so the stable surface stays identifiable while the full product
surface is still programmable. Beta means the shapes may change with notice (a
shorter support window than stable); field-level detail still lives in the typed
Go and TypeScript interfaces. The least-privilege access-map result is the `drift`
route; there is no separate `diff` endpoint.
:::

### gRPC mirror (`olivares.api.v1`)

The control plane also exposes a **gRPC** surface — the `ControlPlane` service in
the versioned proto package `olivares.api.v1`. It is a **focused, frozen mirror**
of a subset of the REST contract above (server info, agent list/get/create, audit
verify), used where a typed binary contract is preferred (for example collectors).
It mirrors the REST contract rather than extending it; the OpenAPI document remains
the canonical surface for the full API.

## Event bus

The [event-bus reference](/reference/events/) is an **AsyncAPI 3.0** contract. The
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

The [modules catalog](/reference/modules/overview/) enumerates the **30 modules**
that sit on top of the core engine, across nine capability areas. One of the most
useful is the **R/RW access map** with its **Permitted-vs-Observed** diff: it
reads from logs, OTEL and (as a non-cooperative backstop) eBPF rather than sitting
in the data path, and it stores only the relation *which agent can read or write
which resource* — never payloads, secrets, or PII.

The catalog is honest about status and coverage. Each module carries its own
maturity — most live and wired end-to-end, some partial or opt-in. Passive
observation is **tiered** by store type — clean for SQL, object and warehouse
stores; lossy for document and vector stores; impossible without cooperation for
in-memory or embedded stores — and the catalog marks where a module is
design-stage. Own-model registry and fine-tuning is a **planned capability**, not
one of the 30 shipped modules.

## CLI

The [CLI reference](/reference/cli/) documents the single `olivares` binary
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

The [configuration reference](/reference/configuration/) lists the environment
variables and runtime options that shape a deployment. The load-bearing ones are
the data directory (`OLIVARES_DATA_DIR`), the real (non-demo) source wiring read
from `OLIVARES_SOURCES_CONFIG` before the engine starts, and the authorization
engine selector `OLIVARES_PDP_ENGINE` (`cedar`, `opa`, or `none`).

Two design rules carry through the configuration surface. An **unconfigured source
warns honestly** rather than failing the engine. And the authorization seam **only
ever restricts, never widens**: RBAC is deny-by-default, viewing the access graph
is a privileged action, and every such read is audited.

---
title: Module XIX — the own API and manage-as-code surface
description: "The engine's foundational surface: every control-plane action over
  one REST/gRPC API, plus a Terraform provider so the control plane itself is
  declared and version-controlled. What the API contract is, what the provider
  manages, and the honest limits of each."
slug: 2026-06/reference/modules/xix-api-manage-as-code
---

Module XIX is not a feature bolted onto the engine — it **is** the engine's surface.
Every other module reaches the outside world through the same first-party API, and the
web UI is a presentation layer over that exact contract, not a parallel one. This page
is the reference for what that surface exposes today and how to manage the control
plane as code, with its real boundaries.

## The API contract

The engine speaks one REST API under `/v1` (chi router, hardened `http.Server`) and a
**focused, frozen gRPC mirror** of it (`olivares.api.v1`: server info, agent
read/create, audit verification, plus the standard health service). gRPC is a deliberate
subset, not full parity — new endpoints land in REST first. Both cables run the **same**
`authenticate → resolve-tenant → authorize` chain and map errors identically, so a
not-found is indistinguishable from a cross-tenant resource on either wire.

The REST surface is published as an **OpenAPI 3.1 contract** rendered at the
[API reference](/reference/api/) directly from the product's authored schema. That
document is the contract of record for the core surface; some module routes are
**reachable but deliberately not** in it (see the honest limits below). The same
functionality is also driveable from the terminal — see the
[CLI reference](/2026-06/reference/cli/) — because the CLI is the engine, not a wrapper over it.

## Authentication and the wire

Authentication is **opaque server-side bearer tokens**, not JWT.
A token is purpose-prefixed
(session vs. API key); the server persists only a public selector and a SHA-256 of the
secret, and compares the secret in constant time. The consequences that matter for a
manage-as-code workflow: tokens are **immediately revocable**, carry **no claims or
secrets**, and add no crypto-parsing attack surface. An API token is bound to a
`(tenant, role)` or is an unbound system-level credential; a request whose tenant header
disagrees with a bound token is refused, never silently widened.

## Manage-as-code: the Terraform provider

The `terraform-provider-olivares` provider is a **separate Go module** and a pure REST
client — it never imports the engine core or the connector SDK, keeping the large
provider dependency tree out of the core's supply chain. Configured with an endpoint, a
sensitive API token, and an optional tenant, it manages a deliberately small, declared
set of objects:

| Kind | Name | Manages |
|---|---|---|
| resource | `olivares_agent` | an agent's catalog definition (full CRUD + import) |
| resource | `olivares_policy` | a governance policy declaration |
| resource | `olivares_agent_identity_binding` | the binding of an agent to a non-human identity |
| resource | `olivares_deployment` | a deployment **definition** (desired state, declarative) |
| data source | `olivares_policies` / `olivares_identities` | read-only views of the governed roster |
| data source | `olivares_access_edges` | the R/RW access map and its permitted-vs-observed drift |
| data source | `olivares_deployment` / `olivares_server_info` | a deployment definition; engine metadata |

These are the **only** resources and data sources the provider serves. Declaring an
`olivares_deployment` records desired state in the control plane — it does **not** touch
infrastructure; the apply path belongs to [module VII](/2026-06/reference/modules/vii-deploy/)
and is a deny-closed seam.

:::caution[Honest limits]
* **`olivares_deployment` declares; it does not deploy.** The resource writes a
  deployment *definition* through module VII's routes. Live `apply`/`retire` against real
  infrastructure is a **deny-closed seam that returns `503`** until an operator
  provisions an executor — declaring a deployment in HCL never mutates your estate.
* **The served OpenAPI is not the whole wire.** The core surface is in the published
  contract, but several module routes (for example the access-map and drift reads, and
  the governance and deployment routes the provider uses) are **reachable yet
  deliberately excluded** from the served OpenAPI document. Their field-level shapes live
  in the product's typed interfaces, not in the public schema.
* **gRPC is a frozen subset, not the full API.** It mirrors a few read/create and audit
  operations for first-party automation; do not assume an endpoint exists on gRPC because
  it exists on REST.
* **The provider's surface is small on purpose.** Four resources and five data sources —
  not the entire API as IaC. Anything outside that set is managed over REST/CLI, not
  declared in HCL today.
* **License is attestation, never a feature gate.** The product is whole under its
  license; the offline license check only records the holder and status and never
  disables, degrades, or blocks any API request or boot.
:::

## Secure by default

The serving engine is secure-by-default: TLS is on (a self-signed cert is generated on
first boot if none is supplied), the bind defaults to localhost, and listening locally is
**not** an exemption from authorization. A fresh install has no credentials — it mints a
one-time setup token to stdout and refuses every protected endpoint until the first
administrator is created. Audit is append-only and hash-chained, with Ed25519-signed
checkpoints that make rewriting history before a checkpoint cryptographically detectable.

## The eventing platform (module XIX's outbound half)

Since the eventing platform shipped (`modules/eventing`), module XIX's surface also
includes **tenant self-service event subscriptions**: typed subscriptions over the
catalog of bus events (`edge.observed`, `cost.sampled`, `finding.reported`,
`audit.recorded`, …) with **durable at-least-once delivery** — retries with backoff,
a dead-letter queue, and replay from a cursor — to an HMAC-signed webhook or a
[SIEM sink](/2026-06/how-to/cookbook/push-to-siem/). The notify module
([XV](/2026-06/reference/modules/xv-notify/)) remains the alert *router* to
operator-provisioned destinations; eventing is the integrator-facing platform.
A companion read-only **posture export** (`modules/posture-export`) lets a control
tower poll the product's ground-truth posture — access graph, drift, inventory,
findings — as refs/hashes/relations only, with the export itself audited.

## Related

* [API reference](/reference/api/) — the rendered OpenAPI 3.1 contract for the core surface.
* [API stability policy](/2026-06/reference/api-stability/) — versioning, deprecation/sunset signalling and support windows for this surface.
* [Using the client SDKs](/2026-06/how-to/use-the-client-sdks/) — the first-party Go/Python/TypeScript clients.
* [CLI reference](/2026-06/reference/cli/) — the same functionality from the `olivares` binary.
* [Manage the control plane as code](/2026-06/how-to/manage-as-code/) — the Terraform provider guide.
* [Module VII — deployment](/2026-06/reference/modules/vii-deploy/) — where `olivares_deployment` actuates (the `503` seam).
* [Modules catalog](/2026-06/reference/modules/overview/) — the Govern/Observe vs Actuate split.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — what actuates today and what does not.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine layer this surface sits on.

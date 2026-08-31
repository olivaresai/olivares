---
title: API stability, versioning, deprecation & sunset
description: The versioning scheme, stability tiers, deprecation signalling (RFC
  9745 / RFC 8594 headers) and minimum support windows for the REST API, the
  gRPC mirror, the live-ingest wire contract, the Terraform provider and the
  client SDKs.
slug: 2026-06/reference/api-stability
---

This page is the **stability contract** for everything that programs against the
control plane. It states what is stable, how breaking change is signalled, and
how long a deprecated surface keeps working. The enforcement is in the
codebase, not in prose: the deprecation table, the response headers, the
OpenAPI markers and the window checks below are all driven from a single
in-code declaration (`core/api/stability.go`), and a sunset scheduled earlier
than the policy allows **fails the build**.

:::note[Pre-1.0 status]
Olivares AI is pre-1.0 (see [Honesty and limits](/2026-06/start/honesty-and-limits/)).
The signalling mechanisms on this page are already live; the **minimum support
windows bind from the 1.0/GA release on**. Until then the published surface is
kept stable in practice, but the formal windows below are the commitment you
can hold us to from GA.
:::

## Covered surfaces and tiers

| Surface | Versioned by | Tier today |
|---|---|---|
| REST core contract — the paths in the [served OpenAPI document](/reference/api/) | URL major (`/v1/…`) | **stable** |
| gRPC mirror — `ControlPlane` in proto package `olivares.api.v1` | proto package major | **stable** (frozen mirror) |
| Live-ingest / connector wire — proto package `olivares.sdk.v1` | proto package major + plugin `ProtocolVersion` | **stable** (frozen) |
| Connector SDK (Go) — modules `sdk`, `sdk/plugin` (author surface) | module semver — tags `sdk/v*`, `sdk/plugin/v*` from the first public release | **stable v1** (Go contract; wire row above) |
| [Event bus contract](/2026-06/reference/events/) (AsyncAPI 3.0) — its event types are also what the eventing platform delivers to [external webhook subscriptions](/2026-06/reference/events/#external-subscriptions-eventing-platform); the subscription-management routes are module routes (`/v1/m/eventing/`, out of contract), but each **event type** carries its own stability tier from the in-code catalog | `info.version` (`1.0.0-preview`) | **beta** (document); per-type tiers for event types |
| Terraform provider | its own semver (`terraform-provider-v*` tags) | **stable**, MAJOR tracks API v1 |
| Client SDKs (Go / Python / TypeScript) | their own semver; MAJOR tracks the API major from GA | **beta** (pre-1.0 packages) |
| Anything not listed — `/v1/m/<ns>/` module routes, SCIM, federation, internals | — | **out of contract** |

**Tiers.** A *stable* surface does not change incompatibly within its major
version; removing or changing it requires the deprecation process below. A
*beta* surface may still change shape, but gets the same signalling and a
shorter window. An *out-of-contract* surface (notably the module routes that
are deliberately outside the OpenAPI document — see the
[reference overview](/2026-06/reference/)) carries no compatibility promise; its
contracts live in the typed interfaces that ship with the product.

Every operation in the OpenAPI document carries a machine-readable
`x-stability` marker, and the document itself links this page in
`info.x-stability-policy`.

## What counts as a breaking change

For a stable surface, all of these are breaking and gated on the process below:

* removing or renaming a path, method, request field, response field or error
  `code`;
* changing a field's type or meaning, or making an optional request field
  required;
* tightening authentication/authorization such that a previously valid call
  fails;
* for gRPC/protobuf: anything `buf breaking` (FILE ruleset) rejects.

These are **not** breaking: adding endpoints, adding optional request
parameters, adding response fields, adding new error codes for new failure
modes, and adding response headers. Clients must tolerate unknown JSON fields.

## Versioning

* **REST** is versioned in the URL: the entire stable contract lives under
  `/v1/`. An incompatible change ships under `/v2/` and `/v1/` enters
  deprecation — never an in-place break.
* **gRPC** is versioned by proto package: `olivares.api.v1` /
  `olivares.sdk.v1`. An incompatible change requires a new package major
  (`…v2`); both contracts are guarded by `buf breaking` against `main`
  (`task proto:breaking`).
* **The Terraform provider** is released independently
  (`terraform-provider-v*` tags); its MAJOR tracks the API major it speaks.
* **Client SDKs** embed `API_VERSION` (the contract major they were generated
  from) and `SPEC_HASH` (the exact OpenAPI snapshot) — `APIVersion` and
  `SpecHash` in Go; from GA their MAJOR tracks the API major.
* **The connector SDK** (the Go contract third-party connectors build
  against) is versioned by per-module semver tags (`sdk/vX.Y.Z`,
  `sdk/plugin/vX.Y.Z`) and gated by the same `buf breaking` wall on its wire.
  Interfaces an author implements never gain methods within a major; new
  capability arrives as new optional interfaces. The full policy ships with
  the module (`sdk/VERSIONING.md`); the authoring lifecycle is in
  [Build and ship a connector](/2026-06/how-to/build-a-connector/).

## Deprecation process and signalling

A deprecation is one declared entry in the in-code table plus a migration
guide; everything else follows from it mechanically.

1. **Announce.** The entry lands with its announcement date and the migration
   guide URL. From that moment every response of the deprecated route carries
   the [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745) header and a link to
   the guide, and the OpenAPI operation gains `deprecated: true`,
   `x-deprecated-at` and `x-migration-guide`:

   ```http
   Deprecation: @1780272000
   Link: <https://docs.olivares.ai/how-to/migrate-example/>; rel="deprecation"
   ```

2. **Schedule the sunset.** When the retirement date is committed, responses
   add the [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594) header (and the
   spec gains `x-sunset-at`):

   ```http
   Sunset: Thu, 01 Jun 2028 00:00:00 GMT
   Link: <https://docs.olivares.ai/how-to/migrate-example/>; rel="sunset"
   ```

3. **Remove** — at the earliest on the sunset date, normally with the next
   API major.

**Minimum support windows** (deprecation announcement → sunset):

| Tier | Minimum window |
|---|---|
| stable | **24 months** |
| beta | **12 months** |

These windows are enforced by tests against the declaration table: an entry
whose sunset violates its tier's window, or that points at a route that does
not exist, does not build.

For **gRPC**, deprecation is expressed with the protobuf `deprecated` option
(which surfaces in generated code) plus the same windows; the wire contracts
are otherwise frozen and `buf breaking` rejects incompatible edits outright.

## What clients see

* **Terraform provider** — emits a `tflog` WARN (method, endpoint, dates,
  guide) once per unique method and request path per run when a control-plane
  response carries a deprecation signal (a deprecated parameterized route
  warns once per resource it touches), and sends a versioned `User-Agent` so
  deprecated-client usage is attributable server-side.
* **Go SDK** — surfaces a `DeprecationNotice` once per endpoint (default: an
  `slog` warning; override with `WithDeprecationHandler`). Deprecated
  operations carry Go `// Deprecated:` markers, so editors and `staticcheck`
  flag them at development time.
* **Python SDK** — one `DeprecationWarning` per endpoint (or your
  `on_deprecation` callback); deprecated operations are marked in docstrings.
* **TypeScript SDK** — one `console.warn` per endpoint (or your
  `onDeprecation` callback); deprecated operations carry `@deprecated` JSDoc.

## Related

* [REST API reference](/reference/api/) — the stable contract itself
* [Using the client SDKs](/2026-06/how-to/use-the-client-sdks/)
* [Build and ship a connector](/2026-06/how-to/build-a-connector/) — the connector SDK
  contract and lifecycle
* [Manage as code (Terraform)](/2026-06/how-to/manage-as-code/)
* [Module XIX — own API + manage-as-code](/2026-06/reference/modules/xix-api-manage-as-code/)
* [Event bus (AsyncAPI 3.0)](/2026-06/reference/events/)
* [Honesty and limits](/2026-06/start/honesty-and-limits/)

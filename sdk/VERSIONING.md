<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Connector SDK — versioning & stability policy

This is the stability contract for the **connector SDK**: the Go modules

| Module | Path | License | Dependencies |
|---|---|---|---|
| `sdk` | `github.com/olivaresai/olivares/sdk` | Apache-2.0 | **none** (stdlib only, by design) |
| `sdk/plugin` | `github.com/olivaresai/olivares/sdk/plugin` | Apache-2.0 | gRPC + hashicorp/go-plugin (opt-in: only if you ship a plugin) |
| `sdk/scaffold` | `github.com/olivaresai/olivares/sdk/scaffold` | Apache-2.0 | none (a generator, not a runtime dependency) |

It aligns with the product-wide policy (docs site →
*API stability, versioning, deprecation & sunset*); this file states what is
specific to the connector contract. The **client** SDKs (`clients/`,
generated REST clients) are a different surface with their own policy.

## What is stable (v1)

**`sdk` — the author contract.** The interfaces and types a connector or
module author programs against are **stable v1**:

- `SourceConnector` (`Descriptor/Open/Gather/Close`), `OutputConnector`
  (`Descriptor/Open/Notify/Close`), `Sink`, `Notification`;
- `Module`/`Host` (lifecycle; out-of-process modules are **not** wired — see
  honest limits);
- `Descriptor`, `Config`, `ConfigField`, `ComponentType`, `APIVersion`;
- `sdk/model`: the observation DTOs (`EdgeObservation`, `CostSample`,
  `FindingReport`), the sealed `Observation` sum type and the shared enums;
- `sdk/event`: the `Event` envelope, `Type`, `Handler` and the typed helpers;
- `sdk/siemwire`: stable, additive-only utility encoders.

**`sdk/plugin` — the author-facing transport surface.** `ServeSource`,
`ServeOutput`, `Handshake`, `ProtocolVersion` and the dispense names are
stable v1. The host-facing and collector-facing surfaces in the same module
(`SourcePluginMap`/`OutputPluginMap`, `IngestSink`, the protobuf codecs and
`genpb`) ship for the product's own composition; authors never need them.

**The wire.** Proto package `olivares.sdk.v1` is **frozen**: `buf breaking`
(FILE ruleset) rejects any incompatible edit, run via `task proto:breaking`
against `main` (the same check the API stability page documents). The hard
compatibility gate between a
host and a plugin binary is the go-plugin **`ProtocolVersion`** (= the proto
package major, v1 ⇒ 1), checked at handshake before any RPC.
`Descriptor.APIVersion` is advisory metadata, never a gate.

## Compatibility rules within v1

- **Interfaces an author implements never gain methods.** A new capability is
  expressed as a **new, optional interface** discovered by type assertion (the
  established side-contract pattern, e.g. the model-provider catalog
  interface), never by widening `SourceConnector`/`OutputConnector`/`Module`.
- **Structs may gain fields** (additive; the zero value means "not reported").
  The wire mirrors this: new proto fields are additive and old binaries simply
  do not send them.
- **Vocabularies are open strings** (`SignalSource`, `Gateway`, severity et
  al. cross the wire as strings): a connector may introduce its own values
  without an SDK release. Payload **shape** is closed (`oneof`).
- **The `Observation` sum type is sealed.** Third parties cannot add
  observation kinds; a fourth kind is an additive SDK change made here. This
  is deliberate (the engine's ingest/dedup semantics depend on the closed
  set) and is the main expressiveness limit an external connector inherits.
- **Breaking anything above requires a major**: a new proto package
  (`olivares.sdk.v2`), a `ProtocolVersion` bump (forced by the buf gate), and
  new module majors (`sdk/v2`). Hosts speak old majors through the
  deprecation window below.

## Releases, tags and the pre-1.0 note

The SDK is versioned by **semver tags per module**: `sdk/vX.Y.Z`,
`sdk/plugin/vX.Y.Z`, `sdk/scaffold/vX.Y.Z`. Tags land with the **first public
release of the repository**; until then the modules are consumed by commit
pin or by a local checkout (`olivares-connector-new -sdk-path` wires the
`replace` directives for that loop). Like the rest of the product, the formal
support windows bind **from GA**; the surface above is kept stable in
practice today, and any pre-GA break would be called out loudly in release
notes, never slipped in.

## Deprecation

Same process and windows as the product policy: a deprecation is announced in
the release notes and `CHANGELOG.md` with a migration note, marked in godoc
(`// Deprecated:`), and kept working for **24 months** (stable tier) from
announcement before removal in the next major. Wire-level deprecation uses the
protobuf `deprecated` option; the wire itself never breaks inside a major.

## Honest limits (what v1 does NOT promise)

- **Out-of-process modules are not wired.** `ModuleService`/`HostService` are
  frozen in the proto but the host glue does not exist; external code today
  means **source, content-source and output connectors**. Don't build on
  `ServeModule` — it doesn't exist.
- **Host-side external wiring covers observation sources, content sources and
  output connectors.** An external output connector is admitted identically to an
  external source: the operator declares it as a notify destination
  (`OLIVARES_NOTIFY_CONFIG`, a `plugin` stanza with a pinned digest + attestation
  bundle) and it runs only after the same deny-closed `connector_trust` gate,
  dispensed checksum-pinned and confined. It is delivered by destination NAME via
  the notify routes — a notify sink, not a bus output subscribing to event types
  (`Runtime.LoadOutputPlugin`, the bus path, is still unwired).
- **In-process side-contracts don't cross the wire.** Type-asserted
  side-interfaces (e.g. catalog snapshots) work in-process only; an
  out-of-process observation source is limited to the sealed observation DTOs
  plus open string vocabularies.
- **`Config` getters are forgiving by contract**: `GetInt`/`GetBool`/
  `GetDuration` fall back to the default on a parse failure. Validate hard
  requirements in `Open` and return an error there.

## How a change lands (process)

1. Additive Go change → review against this file; additive proto change →
   `task proto:generate` + `task proto:breaking` must pass.
2. `CHANGELOG.md` entry under the SDK heading.
3. The docs-site stability page reflects any tier change; this file is the
   source of truth for the connector contract.

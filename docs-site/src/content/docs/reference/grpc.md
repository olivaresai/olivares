---
title: gRPC reference — services, methods and message types
description: >-
  Every rpc the Olivares AI engine and the plugin host register, with its streaming
  shape, request and response messages, and the full method string it travels under.
  Generated from the servers' own registration tables.
---

Olivares AI speaks gRPC in two places, and they point in opposite directions:

- **The engine's control-plane API** (`olivares.api.v1.ControlPlane`) — a small mirror of
  the REST surface for callers that prefer a typed stub. The REST contract in the
  [API reference](/reference/api/) remains the broader of the two.
- **The plugin wire contract** (`olivares.sdk.v1.*`) — the versioned contract every
  out-of-process connector and module speaks. This is the one you implement when you
  [build a connector](/how-to/build-a-connector/) in a language other than Go.

This page is **generated from the registration tables the servers hand to gRPC**, not from
the `.proto` files. That distinction is the point: a `.proto` edited without regenerating
describes a service the binary does not serve, and the check behind this page reports that
disagreement instead of publishing the prettier of the two. A method listed here is a
method a client can call.

:::note[Stability]
The plugin contract `olivares.sdk.v1` is versioned and guarded by buf's breaking-change
detector: an incompatible change requires a new major package. What that commits us to,
and for how long, is in [API stability](/reference/api-stability/).
:::

## Transport and authentication

Every method of the services below except `GetServerInfo` requires an authenticated,
authorized principal. Two exemptions are deliberate and are named here rather than left for
you to discover: `GetServerInfo` answers anonymously, and the standard
`grpc.health.v1.Health` service (`Check`, `List`, `Watch`) is served on the same listener
with no principal, because a probe or a service mesh has to reach it on every pod exactly as
a kubelet reaches `/livez`. An absent bearer token leaves a request anonymous rather than
rejecting it; a present but invalid one is rejected. The
control-plane service is reached on the engine's gRPC listener; the plugin services are
dialed over the go-plugin broker (in-host connectors) or over gRPC with mutual TLS (a
remote collector). Configure the listener with the `OLIVARES_*` variables in the
[configuration reference](/reference/configuration/).

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

The engine and the plugin host register **28 rpc** across **7 services**. The tables below
are read from the generated registration tables the servers hand to gRPC, so a method that
is listed here is a method a client can call.

### `olivares.api.v1.ControlPlane`

Defined in `apiv1/api.proto`; 5 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | Registers a new agent in the inventory and returns the stored record, including the identifier the rest of the API uses. |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | Returns one agent by identifier, with the same fields the REST inventory endpoint serves. |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | Reports version, edition and readiness. It is the only method on this service that does not require an authenticated principal. |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | Lists the agents visible to the calling principal, page by page. |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | Re-verifies the audit chain over a range and reports whether the hashes still link, including the checkpoint status. |

### `olivares.sdk.v1.ContentSourceService`

Defined in `olivaresv1/v1.proto`; 7 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Ends the session opened by Open and releases whatever the connector held for it. |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | Streams the changes since a cursor. Called only when the connector advertises the content.delta capability. |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | Returns the connector's descriptor: its identity, its configuration fields and the capabilities it advertises. |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | Returns one document's body and metadata for the reference the host picked off the List stream. |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | Returns the permission references that govern one document. An empty result means the knowledge base default applies. |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | Streams document references one page at a time, bounded by the ceilings the host passes so a corpus cannot be pulled into host memory in one call. |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | Starts a session with the configuration the host supplies, before any content call. |

### `olivares.sdk.v1.HostService`

Defined in `olivaresv1/v1.proto`; 3 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | Writes one structured log record through the engine, so an out-of-process module logs where an in-process one does. |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | Publishes one event onto the engine's bus on behalf of an out-of-process module. |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | Streams bus events to the module, filtered by the event types it asks for. An empty filter means every type. |

### `olivares.sdk.v1.IngestService`

Defined in `olivaresv1/v1.proto`; 1 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | Accepts a stream of observations pushed by a collector daemon and lifts each onto the event bus, returning a summary when the stream completes. |

### `olivares.sdk.v1.ModuleService`

Defined in `olivaresv1/v1.proto`; 4 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | Returns the module's descriptor: its identity and the configuration it accepts. |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | Hands the module its configuration and lets it prepare, before anything is started. |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Starts the module's work after a successful Init. |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | Stops the module and lets it release what it holds. |

### `olivares.sdk.v1.OutputService`

Defined in `olivaresv1/v1.proto`; 4 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Ends the session opened by Open and releases whatever the connector held for it. |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | Returns the connector's descriptor: its identity, its configuration fields and the capabilities it advertises. |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | Delivers one notification to the destination and reports what the destination did with it, which is what decides whether the host retries. |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | Starts a session with the configuration the host supplies, before any delivery. |

### `olivares.sdk.v1.SourceService`

Defined in `olivaresv1/v1.proto`; 4 rpc.

| Method | Full method | Kind | Request | Response | What it does |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Ends the session opened by Open and releases whatever the connector held for it. |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | Returns the connector's descriptor: its identity, its configuration fields and the capabilities it advertises. |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | Streams observations to the host, which lifts each onto the event bus. The stream ends when a batch run completes or the host cancels it. |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | Starts a session with the configuration the host supplies, before any observation is gathered. |

<!-- END GENERATED olivares-grpc-reference -->

## Message shapes

The tables name each request and response message; their fields are declared in the
`.proto` files listed with each service, which ship in the repository and are the source
the stubs are generated from. Two conventions are worth knowing before you read them:

- **Vocabulary fields are strings, not closed enums** — access mode, signal source,
  confidence, severity and event type. A third-party connector can introduce its own
  signal source without waiting for an SDK release.
- **Payload shapes are closed.** An `Observation` or an `Event` payload is a `oneof` of
  the known message types plus a JSON fallback for module-defined event payloads. An
  unrecognised payload is a contract error; it is not dropped quietly.

## Generating a client

The `.proto` files are the contract. Point your language's protobuf toolchain at
`sdk/plugin/proto/olivaresv1/v1.proto` for the plugin contract, or at
`core/api/proto/apiv1/api.proto` for the control-plane mirror. Ready-made clients for Go
and TypeScript are described in [Use the client SDKs](/how-to/use-the-client-sdks/).

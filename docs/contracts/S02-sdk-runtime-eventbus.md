<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Contract S02 — connector/module SDK, module runtime and event bus

**Status:** stable (Phase A). **Modules:** `/sdk` and `/sdk/plugin` (Apache-2.0), `/core/eventbus` and
`/core/runtime` (AGPL-3.0-only). **Go 1.26.5** module baseline; the workspace
toolchain is Go 1.26.6.
**Consumed by:** the connectors, the modules and the API/authz layer.

This document is the most consumed contract in the project: **every** connector or module
builds against it. The canonical signatures live in the code (`sdk`, `sdk/event`, `sdk/model`,
`core/eventbus`, `core/runtime`); here we explain **how they are used**, **what guarantees** they offer and **how
to write a connector and a module**. Every assertion is backed by tests (`task test`, `-race`).

> The design went through a 3-axis adversarial review panel (API ergonomics/stability,
> license boundary, gRPC/go-plugin realism) **before** implementing it; §11 records the decisions.

---

## 1. Package map and license boundary

| Package | License | Importable by | Contents |
|---|---|---|---|
| `sdk` | Apache-2.0 | connectors, modules, API/authz layer | Interfaces `SourceConnector`, `OutputConnector`, `Module`, `Host`, `Sink`; `Descriptor`, `Config`, `ConfigField`, `Notification`. **Zero dependencies** (stdlib only). |
| `sdk/model` | Apache-2.0 | connectors, modules | Wire DTOs `EdgeObservation`/`CostSample`/`FindingReport`, the **sealed** sum type **`Observation`**, and the enums `AccessMode`/`SignalSource`/`Confidence`/`Severity`. Zero dependencies. |
| `sdk/event` | Apache-2.0 | connectors, modules, `core` | `Event` (envelope), `Type`, `Handler` and typed helpers (`EdgeOf`/`CostOf`/`FindingOf`, `FromObservation`). stdlib + `sdk/model` only. |
| `sdk/plugin` | Apache-2.0 | the host (`core`), plugin authors | **Separate module.** gRPC/protobuf contract (`genpb`) + `hashicorp/go-plugin` glue + `Serve*`. Here (and only here) gRPC and go-plugin enter. |
| `core/eventbus` | AGPL-3.0 | `core`, modules | `Bus` (interface) + in-proc impl over channels (NATS-ready). |
| `core/runtime` | AGPL-3.0 | `core` (boot) | Component host: in-proc registration + out-of-process loading; lifecycle; fault isolation; `SchemaProvider`. |

**Boundary rule (non-negotiable, `LICENSING.md`):** a connector imports **only** from `/sdk` (and, if
packaged as a plugin, from `/sdk/plugin`), **never** from `/core`. This is verified by `scripts/check-boundary.sh`
with the real graph from `go list -deps` (CI job `boundary`). **Why two SDK modules:** `sdk` stays
stdlib-pure, so an in-proc connector author does not drag gRPC into their `go.sum`; the gRPC tree only enters if
they choose to package as a plugin (importing `sdk/plugin`). `go.sum` is per-module, not per-package: that is why it is
a separate module, not a subpackage.

```
sdk            (interfaces, stdlib-pure, Apache)
sdk/model      (DTOs + enums + sealed Observation, Apache)   <- imported by sdk, sdk/event
sdk/event      (Event/Type/Handler, Apache)                   <- imports sdk/model
sdk/plugin     (gRPC + go-plugin, Apache)                     <- imports sdk, sdk/model; generates genpb
core/eventbus  (in-proc Bus, AGPL)                            <- imports sdk/event
core/runtime   (host, AGPL)                                   <- imports sdk, sdk/plugin, core/eventbus, core/store
```

---

## 2. The SDK interfaces

A component is described by a `Descriptor` (unique name `<vendor>.<component>`, version, type,
declared configuration fields) and receives its `Config` (map of settings with typed getters;
**secrets by reference, never inline**, `docs/SECURITY-HARDENING.md`).

```go
// Input connector: collects facts and emits them as observations.
type SourceConnector interface {
    Descriptor() Descriptor
    Open(ctx, Config) error                 // configure once
    Gather(ctx, Sink) error                 // run, emitting to sink
    Close(ctx) error                        // release
}
// Output connector: delivers notifications to an external system.
type OutputConnector interface {
    Descriptor() Descriptor
    Open(ctx, Config) error
    Notify(ctx, Notification) error
    Close(ctx) error
}
// Module: consumes events and implements product logic.
type Module interface {
    Descriptor() Descriptor
    Init(ctx, Host) error                   // subscribe here; do not block
    Start(ctx) error                        // start background work
    Stop(ctx) error                         // drain and release
}
// Services the engine gives a module in Init.
type Host interface {
    Publish(ctx, event.Event) error
    Subscribe(types []event.Type, h event.Handler) (cancel func(), err error)
    Logger() *slog.Logger
    Config() Config
}
// The engine provides it; the connector pushes each observation.
type Sink interface { Emit(ctx, model.Observation) error }
```

**Guarantees and rules:**
- **`Host` does NOT expose the store.** This is **permanent and by license construction**: `store.Scope` is an
  AGPL type of the engine and the Apache SDK cannot import it. An in-proc module that persists receives data
  access through the engine seam (§8); fully standardizing it is work for the API/authz layer. The
  **event bus is the integration backbone** that every module shares.
- **The scheduler is the engine's, not the connector's.** `Gather` runs in a goroutine that the runtime owns
  and cancels via `ctx`. A *streaming* source (tail/receiver) blocks in `Gather` emitting until
  `ctx` is cancelled; a *batch/poll* source does its work and returns `nil` (the runtime decides whether and
  when to re-run). The connector never owns a ticker for its own lifecycle.
- **At-least-once delivery.** If `Gather` fails and the runtime restarts the source, observations already emitted
  may be re-emitted. Consumers deduplicate by the natural key (e.g. source/resource/mode +
  `ObservedAt` of an edge), relying on the idempotent `AccessEdges().Upsert` of the store (`ARCHITECTURE.md`).
- **The OTLP receiver is core ingest**, not a `Gather` connector (it is a server with its own socket).

---

## 3. Observations and events

`Observation` is a **sealed sum type** in `sdk/model` (unexported marker `isObservation()`): only the
three SDK DTOs satisfy it, so a third party **cannot** introduce an observation type. A fourth
type is an additive change in `sdk/model`; the maintainer extends the host `switch` statements and the **error of the
`default` branch** (in `observationToPB` and the `Type==""` guard of `busSink`) catches at **runtime** any
kind not handled — it is not compiler enforcement, but an "unknown observation" is **unconstructable from
outside** and never a panic. The DTOs use **value receivers**, so `*EdgeObservation` also satisfies
`Observation`; a connector can `Emit` value or pointer and the engine normalizes both.

The runtime **lifts** each observation to an `event.Event` and publishes it on the bus:

| Observation | `event.Type` | Event payload |
|---|---|---|
| `EdgeObservation` | `edge.observed` | the `EdgeObservation` |
| `CostSample` | `cost.sampled` | the `CostSample` |
| `FindingReport` | `finding.reported` | the `FindingReport` |

Invariant: **`Type` ⇒ concrete type of the payload**, guaranteed by `event.FromObservation` and read with the
typed helpers (`event.EdgeOf(e)` etc.). The three first-party types travel the typed proto `oneof`,
**never** JSON. A module can publish its own `Type` with arbitrary payload; that travels an **unversioned**
JSON fallback owned by the emitting module and the consumer (not covered by buf).

The Event `Tenant` is a **string** (reference); the engine resolves it to `model.TenantID`. This keeps the SDK
free of the engine's id types.

---

## 4. Event bus (`core/eventbus`)

```go
type Bus interface {
    Publish(ctx, event.Event) error
    Subscribe(types []event.Type, h event.Handler) (Subscription, error) // types empty = all
    Close() error
}
```
In-proc implementation (`NewInProc(Options)`): each subscriber has its own **buffered channel** drained by
a dedicated goroutine, so a slow handler does not block delivery to others (only its own queue fills up).
- **Intentional backpressure:** bounded buffer (`DefaultBuffer=256`); if it saturates, `Publish` **blocks
  respecting its `ctx`** (losing events silently would be worse than throttling a publisher). `Publish`
  returns `ctx.Err()` if the context is cancelled and `ErrBusClosed` if the bus is closed.
- **Fault isolation:** a panic in a handler is **recovered and logged**; a faulty module does not take down
  the engine (`ARCHITECTURE.md`).
- **Clean shutdown:** `Close` unsubscribes everyone, waits for in-flight handlers and rejects new
  `Publish`/`Subscribe`. `Unsubscribe` is idempotent and waits for its goroutine to drain.
- **NATS-ready:** the interface does not expose channels, so a distributed implementation (NATS)
  plugs in without touching any subscriber. Default = in-proc (decision §11).

---

## 5. Module runtime (`core/runtime`)

```go
rt := runtime.New(runtime.Options{Logger: log})        // bus nil => creates one in-proc and owns it
rt.AddSource(conn, cfg, "tenant-ref")                  // in-proc
rt.AddOutput(conn, cfg, []event.Type{...})             // in-proc; types nil = all
rt.AddModule(mod, cfg)                                 // in-proc
rt.LoadSourcePlugin(path, cfg, "tenant-ref")           // out-of-process (go-plugin)
rt.LoadOutputPlugin(path, cfg, types)
rt.Start(ctx)                                          // opens and wires everything, then starts sources
rt.Stop(ctx)                                           // teardown in reverse order
rt.Status() []ComponentStatus                          // pending/running/stopped/failed (+ error)
```

- **Startup order:** first `Open`+subscription of outputs and `Init`(+subscription)+`Start` of modules,
  **then** the sources are started — so no early event is lost. Each source runs `Gather` in its own
  goroutine with a `Sink` that lifts the observation to an Event and publishes it.
- **Fault isolation:** a component that fails in Open/Init/Start is marked `failed` and is **skipped**
  (one bad piece does not prevent the engine from starting; it shows in `Status()`). A panic in `Gather` or in an output
  handler is recovered and marked `failed`. A source that fails is left **down** (auto-restart with backoff is
  a follow-up, out of scope of this contract).
- **Teardown:** `Stop` cancels the `ctx` of the sources and waits for their goroutines (bounded by the Stop
  `ctx`), closes sources, stops modules (reverse order, unsubscribing their subs), unsubscribes and closes
  outputs, **kills the plugin processes** (`client.Kill()`) and closes the bus if it owns it. Each call is
  isolated from panics. Idempotent.
- **Unique names:** the `Descriptor.Name` is the global registration key; empty or duplicate is rejected.
- **Live source reconfiguration:** the runtime is still single-threaded by contract — `Start`
  once, `Stop` once, and outputs/modules/schedules sealed at `Start`. On top of that it gains **exactly
  one** further, *serialized* mutation path, for the SOURCE set only:
  `AddSourceLive` / `ReplaceSourceLive` / `RemoveSourceLive` and the `*PreparedSource` helpers. They run
  AFTER `Start`, are single-flighted against each other and against `Stop` (`reloadMu`), and let an
  operator add/remove/rotate an individual connector **without a process restart**. Each source runs under
  a **per-source `ctx` that is a child of the engine `runCtx`**, so `Stop`'s single cancel still cascades
  to every live-added source (the graph still "stops as one"), yet one source can be quiesced alone:
  cancel its ctx → wait for `Gather` to return (bounded) → `Close` once → kill its plugin subprocess. This
  uses the EXISTING `SourceConnector` lifecycle (`Open`/`Gather(ctx)`/`Close`) — the v1 interface is
  unchanged (no `Drain`/`Quiesce` method); ctx-cancel-then-`Close` is the contract-sanctioned stop.
  **Deny-closed:** a new/replacement source is `Open`ed (the validation point) before it is wired; a
  failure leaves the others — and, on a rotate, the old source — running. The desired roster is the
  durable, store-backed `SourceDef` set (the file `sources[]` becomes a one-time bootstrap seed); the
  composition root's reconciler diffs it and drives these methods. Outputs, modules, schedules, listeners,
  TLS, the store DSN and the bus are NOT live-reconfigurable and remain restart-time — honestly reported,
  never faked.

---

## 6. gRPC / go-plugin contract (`sdk/plugin`)

- **Proto v1** (`sdk/plugin/proto/olivaresv1/v1.proto`, package `olivares.sdk.v1`) with `buf` for lint and
  incompatible-change detection (`buf breaking`). Generated code in `sdk/plugin/genpb/olivaresv1`
  (committed; CI verifies it is not stale).
- **Vocabularies as strings** on the wire (mode, signal source, confidence, severity, event type): the SDK
  models them as open string types, so a third-party connector introduces its own `SignalSource`
  without an SDK release. The **payload shape** is closed (`oneof`).
- **Delivery verdict (2026-07-28).** `Notify` still returns only `error` in Go, but the error may now
  carry an `sdk.DeliveryError` holding a `DeliveryReport`: a closed outcome set
  (indeterminate/delivered/delivered-with-warning/partial/rejected/unavailable/protocol-anomaly), the
  cardinality actually sent, the rejected count and — where the protocol can attribute it — the
  refused positions. The engine reads it with `errors.As` to decide retry vs immediate dead-letter.
  A connector that returns a plain error is read as **indeterminate**, which stays retryable, so
  nothing written against the previous contract changes behaviour.
  **WIRE-BREAKING for out-of-process plugins:** `rpc Notify` now returns `NotifyResponse` instead of
  `Empty`, and an application-level failure travels **inside** it (`error_message`) rather than as a
  gRPC error — gRPC discards the response message when a handler returns an error, so a verdict
  returned that way would be lost at exactly the boundary it exists to cross. A gRPC error keeps its
  old meaning: the plugin crashed or the stream broke, read as indeterminate. Plugins built against
  the previous proto must be rebuilt.
- **Services:** `SourceService` (Describe/Open/**Gather server-streaming**/Close) and `OutputService`
  (Describe/Open/Notify/Close) are **implemented and tested** (roundtrip with `plugin.TestPluginGRPCConn`
  + real smoke with a separate process after `-tags e2e`). `ModuleService` + `HostService` (Publish/Subscribe
  server-stream/Log over the broker) are **defined in the proto (frozen by buf)** but their glue is
  implemented in the API/authz layer; in this contract the modules run **in-proc**.
- **Handshake:** the hard compatibility is `ProtocolVersion` (integer, = proto major: v1⇒1), which
  go-plugin checks **before** any RPC → clean rejection of an incompatible plugin.
  `Descriptor.APIVersion` ("v1") is **advisory** metadata.
- **Packaging a connector** = `func main(){ plugin.ServeSource(myConnector) }` (or `ServeOutput`).

---

## 7. The schema seam (store handoff)

`sdk.Module` stays **bus-only** (does not touch the store). An in-proc module that owns entities implements the
interface **of the engine** (not of the Apache SDK, because `store.ExtensionRegistry` is AGPL):

```go
type SchemaProvider interface { RegisterSchema(reg store.ExtensionRegistry) error }
```
Registration happens at a **different moment** than the Init/Start/Stop cycle: once, when the store is built,
before any `Scope` exists. The engine boot does:

```go
st, _ := sqlstore.Open(ctx, cfg, rt.RegisterSchema)   // RegisterSchema deterministic fan-out to the modules
rt.Start(ctx)
```
`rt.RegisterSchema` iterates the modules in registration order and calls `RegisterSchema` of those that
implement it; an error aborts startup (a schema that does not compile is a boot failure, it is not isolated). An
**out-of-process module cannot** register core schema (an `ExtensionRegistry` does not cross gRPC) and is
only a bus consumer. *Verified end-to-end against a real SQLite in `core/runtime` (the module table
is created and used via `Scope.Ext`).*

---

## 8. How to write a CONNECTOR (example: `connectors/example`)

A connector imports only `sdk` (+ `sdk/model`). It implements the interface, reads config in `Open`, emits in
`Gather`:

```go
package example
type Source struct{ count int; resource string }
func (s *Source) Descriptor() sdk.Descriptor { /* Name "vendor.x", Type sdk.TypeSource, ConfigFields... */ }
func (s *Source) Open(_ context.Context, cfg sdk.Config) error { s.count = cfg.GetInt("count", 1); return nil }
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
    for i := 0; i < s.count; i++ {
        if ctx.Err() != nil { return ctx.Err() }
        if err := sink.Emit(ctx, model.EdgeObservation{ /* natural refs, Mode, Source, ObservedAt */ }); err != nil {
            return err
        }
    }
    return nil // batch source; a streaming one would block on <-ctx.Done()
}
func (s *Source) Close(context.Context) error { return nil }
```
- **In-proc:** `rt.AddSource(example.New(), cfg, tenant)`.
- **As a plugin:** a `cmd/<x>/main.go` with `func main(){ plugin.ServeSource(example.New()) }`, compiled to a
  binary and `rt.LoadSourcePlugin(path, cfg, tenant)`. The same connector code serves for both.
- An `OutputConnector` is the same but implements `Notify(ctx, Notification)`.

## 9. How to write a MODULE (example: `modules/example`)

A module is AGPL (`/modules`), can import the engine. It subscribes in `Init`, reacts in the handler:

```go
func (m *Module) Init(_ context.Context, host sdk.Host) error {
    m.log = host.Logger()
    cancel, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, m.onEdge)
    m.cancel = cancel
    return err
}
func (m *Module) onEdge(_ context.Context, e event.Event) error {
    if edge, ok := event.EdgeOf(e); ok { /* ...use edge... */ }
    return nil
}
func (m *Module) Start(context.Context) error { return nil }     // event-driven work
func (m *Module) Stop(context.Context) error  { if m.cancel != nil { m.cancel() }; return nil }
// Optional: declare your own entities.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
    return reg.Register(model.EntityDescriptor{Kind: "ns.entity", Table: "ns_entity", Fields: []model.FieldSpec{...}})
}
```
Registration: `rt.AddModule(example.New(), cfg)`. For in-proc data, the module will receive a `Scope` through the
engine seam (wired by the API/authz layer); in this contract the `Host` gives bus + logger + config.

---

## 10. Tests that back the contract

- `sdk`: enums, sealing of `Observation`, event helpers, `Config` getters.
- `sdk/plugin`: roundtrip of conversions; **gRPC roundtrip** of Source (streaming) and Output via
  `TestPluginGRPCConn`; error propagation across the wire.
- `core/eventbus`: delivery, filter by type, fan-out, unsubscribe, **panic isolation**, shutdown,
  context cancellation under saturation, concurrency (`-race`).
- `core/runtime`: in-proc e2e (source→bus→module+output), status, **fault isolation**, registration
  guards, schema seam with a **real SQLite store**.
- `modules/example`: **DoD** — the example connector and module load via the runtime and communicate over the
  bus. Real out-of-process smoke after `-tags e2e`.

---

## 11. Decisions and deviations from the plan (with rationale)

1. **Event bus v1 = in-proc + NATS-ready interface** (plan default, confirmed). NATS plugs in without
   touching subscribers.
2. **gRPC versioning = stable v1 + buf** (plan default, confirmed). `ProtocolVersion` (not `APIVersion`)
   is the hard gate.
3. **`Sink.Emit(Observation)` with a sealed sum type** over typed methods: adding a 4th observation type
   does not break `SourceConnector`/`Sink`; the sealing prevents a third party from introducing types (a 4th is added only
   by the SDK), and an unmapped kind is rejected at runtime (`default` branch), not at compile time. Value or pointer
   to the DTO are accepted (the engine normalizes).
4. **`Host` without store — permanent**, not "enough for the example": license boundary.
5. **Two SDK modules (`sdk` + `sdk/plugin`)**: keeps `sdk` stdlib-pure; gRPC is opt-in by `go.sum`.
6. **Only streaming `Gather` in v1** (no `PollSource`): the runtime owns the goroutine + cancellation by
   ctx; the "the host owns scheduling" contract covers poll/one-shot/tail. `PollSource` would be added additively
   if a real source asks for it (no scope creep).
7. **Boundary by `go list -deps` script**, not `depguard`: golangci runs per-module with relative paths, so
   a `depguard` with path-scoped scope does not reliably distinguish modules and a global deny would break the
   lint of `core`/`modules`. The script is the authoritative truth (CI job `boundary`).
8. **Dependency bump for security:** `grpc v1.79.3` + `golang.org/x/net v0.51.0` (GO-2026-4762 / GO-2026-4559).
   `govulncheck` = 0 in the 6 modules.

**Deferred (with rationale):**
- **Out-of-process module host (HostService over the broker)** → **API/authz layer** (the most expensive
  go-plugin path; that layer owns API/authz and the enrichment of the Host). Proto **frozen** already.
- **`PollSource` / scheduling DSL** → future (scope creep).
- **Out-of-process module persistence / wire-safe data facade** → API/authz layer (no AGPL types
  over the wire).
- **Plugin auto-restart with backoff** → follow-up (interim: a down plugin ⇒ `failed`, logged, **left
  down**; losing ingest silently is worse than a documented stop).
- **`cmd/olivares` wiring** (registering real connectors/modules at boot) → API/authz layer.

---

## 12. What builds on top

- **The API/authz layer** implements the out-of-process module glue (HostService over broker), binds the
  `Scope`/authz that an in-proc module uses for data, wires `cmd/olivares` (boot:
  `sqlstore.Open(.., rt.RegisterSchema)` → `rt.Start`), and exposes everything over the API.
- **The real connectors** (Claude/OTEL, pg-audit, eBPF, model/provider, identity/output) are written
  against `sdk` (§8).
- **The real modules** are written against `sdk` + the schema seam (§9).

---

## 13. Runtime ingestion (scheduler)

Ingestion is wired as a production caller and delivers several deferrals from §11. The delta of this
contract:

- **Re-poll scheduler (the engine's):** `rt.AddPollSource(conn, cfg, tenant, interval)` re-runs `Gather` every
  `interval` (a *streaming* source uses `interval<=0` and blocks, just as before); on error it retries with exponential
  backoff (base = `interval`, cap 5 min) and the source **stays `Running`** with its last error. It delivers the
  **auto-restart with backoff** that §11 left as a follow-up. `AddSource` = `AddPollSource(..., 0)` (no change).
- **Periodic jobs:** `rt.SchedulePeriodic(name, interval, runImmediately, fn)` runs non-`Gather` work on the
  same scheduler (used by the roster `SyncRoster`), isolated from panics, cancelled by `ctx` in `Stop`.
- **Push collector→core (option C):** new `IngestService.Push` (stream) in proto v1; `rt.Ingest(ctx, tenant,
  source, obs)` lifts a pushed observation to the bus (core-side); `sdk/plugin.NewIngestSink` is the collector-side
  `Sink`. `rt.Options.SinkFactory` lets the **same** gatherLoop push to a remote core (collector)
  or lift to the local bus (serve). The **`cmd/olivares` wiring** (§11 deferred) is delivered.
- **OTLP receiver** is still core ingest (not a `Gather`); the receiver posture
  (loopback/AutoMTLS/mTLS) is not weakened.

---

## 14. Distributed bus (NATS bridge)

The distributed bus delivers the implementation that §4 promised, as a **hybrid**: the local fan-out
is still the in-proc bus — **all the guarantees of §4 remain intact on the local path**
(blocking backpressure, zero local loss, panic isolation, shutdown with drain, no codec
on the hot path) — and NATS acts as a **best-effort bridge between nodes** (`core/eventbus/natsbus`,
decision and full comparison in ADR-0017). Delta of this contract:

- **Semantics between nodes: at-most-once.** Connection with `NoEcho` (the bridge subscription receives only
  events of remote origin → no double delivery); per-publisher order preserved across types (one
  connection per node, one ordered wildcard subscription). Loss windows — NATS server restart,
  reconnect-buffer overflow ("buffered ≠ delivered"), slow-consumer drops when the subscription's
  pending fills up — are **counted** (`olivares_eventbus_bridge_*`) and alertable, never
  silent. **No JetStream in v1**: the 2026-06-12 subscriber census shows that most are not
  duplicate-safe; at-least-once is the future upgrade path, conditioned on an idempotency
  pass (ADR-0017).
- **Wire format**: the frozen `Event` proto of §6 (typed oneof for the three observation
  payloads — "never JSON", as §3 requires — and `json_payload` + decoder registry for the module
  types). An unregistered type arrives as `json.RawMessage` (the shape that the tolerant-consumer
  re-marshal pattern already accepts). The composition root extends the registry
  (`natsbus.Options.Decoders`) for payloads that live under `/modules` (e.g. `voice.Telemetry`).
- **HA**: remote events are **injected only on the leader** (`SetInjectGate` ←
  `store.Leader().Active`): a standby publishes toward the bridge (that is the point: its observations
  reach the leader) but does not process foreign events while it is passive — it cuts off at the root the side
  effects on standby (duplicate notifications, derived findings, ErrNotLeader storm).
  The failover overlap (≤2 s, elector tick) can inject a duplicate; the unique index
  `(tenant_id, event_id)` of the eventing capture absorbs it.
- **Additive package extensions (the `Bus` interface does NOT change):** `NamedSubscriber`
  (`SubscribeNamed` — the runtime labels the subscription with the module name) and `StatsProvider`
  (`BusStats()` — depth/capacity per subscriber + blocked/dropped/handler-errors counters,
  the saturation SLIs of docs/17 §5). `Options` gains `DemoteError` (an error expected under regime
  — `ErrNotLeader` on standby — logs Debug, not Warn; still counts in the SLI).
- **Config**: `OLIVARES_BUS_CONFIG` (JSON file via `readOperatorConfig`, CMEK-sealable).
  **Absent = in-proc (default intact). Present and invalid = the boot ABORTS** (the
  fail-boot-closed family of `OLIVARES_LEDGER_SIGNER`): a node that silently degraded to in-proc
  would run partitioned from the cluster. An unreachable server with valid config ≠ config error: the
  client retries forever and `olivares_eventbus_bridge_connected==0` + its alert are the net.

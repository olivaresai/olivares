# ADR-0017: Distributed event bus = in-proc local fan-out + NATS bridge, core NATS at-most-once (no JetStream in v1)

- **Status:** accepted (amends ADR-0006's delivery-semantics line) — **extended by ADR-0021**, which
  ships the at-least-once JetStream backend as a closed enterprise add-on that resolves the
  duplicate-safety concern via bus-boundary dedup (not the per-subscriber idempotency pass anticipated
  below); this OPEN Core-NATS bridge stays at-most-once and unchanged.
- **Date:** 2026-06-12
- **Deciders:** design pressure-tested by a 3-lens adversarial panel before implementation
- **References:** `docs/contracts/S02-sdk-runtime-eventbus.md §4`,
  `core/eventbus/natsbus`, subscriber idempotency census (recon, 2026-06-12)

## Context and problem statement

ADR-0006 left the bus in-proc with a NATS slot. HA shipped, so multi-node exists — and the
bus does not cross nodes: an event published on a standby (background sources, identity sweeps)
never reaches the leader's processing; the eventing platform's capture — the durability
boundary — silently misses it. Two questions had to be answered with evidence, not defaults:
**(a)** does the distributed backend replace the local delivery path or bridge it, and **(b)**
core NATS (at-most-once) or JetStream (at-least-once)?

ADR-0006 recorded the bus as "at-least-once; consumers de-duplicate". **That line was wrong as a
description of the implementation**: the in-proc bus is at-most-once (handler errors are logged,
not retried; queued events drop at close — `core/eventbus/inproc.go`), the S02 §4 contract
documents blocking backpressure with NO redelivery, and `modules/eventing/capture.go` states
"the bus itself is at-most-once (S02) and replay starts AT capture". The at-least-once phrase in
ADR-0006 described source-level re-emission (`Gather` re-runs), not bus delivery.

## Decision drivers

- The 2026-06-12 subscriber census: most of the ~17 bus subscribers are NOT duplicate-safe
  (eventing double-captures, security/notify persist or send duplicates, count/aggregate folds
  inflate). At-least-once delivery TODAY would be a semantic regression dressed as an upgrade.
- S02 §4's written guarantee — Publish blocks under saturation, "losing events silently would be
  worse than throttling a publisher" — is load-bearing: `olivares_ingest_duration_seconds` is
  documented as THE backpressure SLI (docs/17 §1.4) and the collector-backpressure runbook says
  "no events are lost — the bus blocks rather than drops". Routing the local hot path through a
  server would invert that contract on 100% of production traffic (the LB drains standbys).
- The traffic the backend exists to rescue (standby-origin events) is the LOW-volume path; the
  local path is the hot one. The design must not trade the hot path for the cold one.

## Considered options

- **A. Pure NATS transport** — every publish/subscribe traverses the server; one code path.
  Rejected: inverts S02 §4 on the local path (silent slow-consumer drops where the contract
  promises blocking, no-loss), adds server-restart/reconnect loss windows to same-node delivery,
  and degrades the ingest SLI's meaning.
- **B. Hybrid: in-proc local fan-out + NATS bridge with NoEcho (CHOSEN).**
- **C. JetStream (at-least-once)** — rejected for v1: the census shows subscribers are not
  duplicate-safe; JetStream becomes available work only AFTER an idempotency pass across
  subscribers (tracked as the explicit upgrade path below).

## Decision outcome

Chosen option: **B + core NATS**. `core/eventbus/natsbus` embeds the in-proc bus: Publish fans
out locally first (every S02 §4 guarantee intact — blocking backpressure, zero local loss, panic
isolation, no codec on the hot path), then bridges the event to NATS best-effort. The bridge
connection sets **NoEcho**, so its single wildcard subscription receives only REMOTE-origin
events, which it re-materializes (frozen proto `Event` oneof for the three observation payloads,
JSON + decoder registry for module-defined types) and injects into the local fan-out — no double
delivery, per-publisher ordering preserved across types (one connection per node, one ordered
subscription).

**Cross-node semantics, documented honestly: at-most-once.** Loss windows: NATS server restart
(no persistence), reconnect-buffer overflow/never-reconnected ("buffered ≠ delivered"), and
slow-consumer drops when the bridge subscription's pending buffer fills — every one counted
(`olivares_eventbus_bridge_*`) and alertable, never silent. HA: remote events are **injected only
on the leader** (`SetInjectGate(store.Leader().Active)`), which kills the standby-side-effect
class (duplicate notifications, duplicate derived findings, ErrNotLeader log storms) at the bus
boundary; the ≤2s failover overlap can double-inject, absorbed by the eventing capture's
`(tenant_id, event_id)` unique index. Config (`OLIVARES_BUS_CONFIG`) is fail-boot-closed: a node
that silently fell back to in-proc would run partitioned.

### Consequences

- **Good:** standby-origin observations reach the leader (closing the cross-node gap); the default single-node
  binary is byte-for-byte unaffected; local delivery semantics unchanged; cross-node loss is
  counted, not silent.
- **Bad / trade-offs:** the cross-node path is exercised only by standby-origin traffic — its
  codec/inject path carries dedicated integration tests (embedded nats-server) precisely because
  production exercises it rarely; bridged events add one encode per publish on nodes WITH the
  bridge configured.
- **Neutral:** JetStream remains the at-least-once upgrade path, gated on a subscriber
  idempotency pass (the census is the work list); the `Bus` interface gained nothing — Stats and
  named subscriptions are optional extension interfaces.

## Why the alternatives were rejected

See drivers: A inverts a written contract on the hot path to simplify the cold one; C ships
duplicates into subscribers that demonstrably mishandle them.

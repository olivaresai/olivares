# ADR-0006: In-process event bus by default, transport-agnostic for NATS

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** SDK/runtime/event-bus contract; stack design

## Context and problem statement

Connectors lift observations onto an internal event bus; modules and output connectors
subscribe by type. The single binary must work with no message broker, yet a
multi-host deployment needs a distributed bus.

## Decision drivers

- No broker dependency in the single-binary default.
- A path to a distributed bus that does not force subscribers to change.

## Considered options

- **In-process Go channels as default, behind a transport-agnostic `Bus` interface**
  that a distributed implementation (NATS) can replace.
- **A broker (NATS) from the start.**

## Decision outcome

Chosen option: **in-process Go-channel bus as the v1 default**, with the `Bus`
interface exposing **no channel** so a **NATS** implementation can be slotted in for
multi-host deployments **without changing a single subscriber**. Delivery is
asynchronous and at-least-once; consumers de-duplicate on the natural-key timestamp.

> **Amended by ADR-0017 (2026-06-12):** the "at-least-once" in the previous sentence
> was wrong as a description of BUS delivery — the implementation and the S02 §4
> contract are at-most-once (handler errors log, queued events drop at close);
> at-least-once applies to source-level re-emission (`Gather` re-runs), which is what
> consumers de-duplicate. ADR-0017 records the delivered NATS backend: in-proc
> local fan-out unchanged + NoEcho bridge, cross-node at-most-once, no JetStream in v1.

### Consequences

- **Good:** the single binary needs no broker; the distributed path is a drop-in.
- **Bad / trade-offs:** at-least-once semantics push de-duplication onto consumers.
- **Neutral:** NATS is optional and only for the distributed topology.

## Why the alternatives were rejected

- **Broker from the start** — adds an external dependency to every install, defeating
  the single-binary / air-gap goal, for value only the distributed topology needs.

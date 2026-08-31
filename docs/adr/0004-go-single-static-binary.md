# ADR-0004: Engine in Go, one static binary with the web embedded

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T1, T5); stack architecture

## Context and problem statement

A self-hostable, air-gap-friendly security control plane needs to be trivial to deploy,
native to the eBPF/OpenTelemetry/cloud-native ecosystem, and shippable as one artifact.
The engine language and the way the UI is delivered both follow from that.

## Decision drivers

- Single self-contained artifact for self-hosting and air-gap.
- Native eBPF and a mature module/plugin runtime.
- Strong concurrency for ingest and the event bus.

## Considered options

- **Go**, single static binary, web embedded via `go:embed`.
- **Rust** engine.
- **Node/TypeScript** engine.
- **Separate SPA** (two artifacts) instead of an embedded UI.

## Decision outcome

Chosen option: **Go**, compiled to a single static binary, with the React web UI
**embedded via `go:embed`** and served from the same origin as the API — so the whole
product is **one file**.

### Consequences

- **Good:** one artifact to ship, verify and run; native eBPF; great cloud-native fit;
  concurrency suited to ingest.
- **Bad / trade-offs:** the UI is built and embedded as part of the binary build.
- **Neutral:** Node/TypeScript is used for the web UI only, not the engine.

## Why the alternatives were rejected

- **Rust** — slower build/iteration and overkill for v1's needs.
- **Node/TS engine** — poor eBPF story and not a single static binary, despite being a
  comfort zone.
- **Separate SPA** — two artifacts to deploy and version; the embedded UI keeps it one.

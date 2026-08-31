# ADR-0012: Distributed ingest — collectors push to the core over gRPC + mTLS

- **Status:** accepted
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares (boot decision CB-1)
- **References:** roadmap boot decisions (CB-1 → option C); runtime-ingestion contract

## Context and problem statement

The ingestion plane needed a topology decision. Collectors observe on customer hosts;
the core aggregates. The options ranged from in-process only to a fully distributed
push model, with implications for isolation, the network trust boundary and packaging.

## Decision drivers

- Keep the data-plane on customer infrastructure with a hardened network crossing.
- Preserve the single binary for the simple case.
- Isolate collector dependencies from the core.

## Considered options

- **C — remote push:** a collector runs the source connectors locally and **pushes**
  observations to the core over **gRPC + mTLS**, with **no inbound listener** on the
  collector.
- **B — out-of-process local:** connectors as local subprocesses (AutoMTLS), the
  single-node substrate.
- **A — in-process:** sources linked into the binary (first-party fast-path).

## Decision outcome

Chosen option: **C (remote push) as the distributed target**, with B as the
single-node substrate and A retained as an in-process fast-path for first-party
sources. All transports enter v1; C is **not** deferred. The mechanism lives in the
runtime; the distributed packaging (DaemonSet/Helm) ships with the supply-chain work.

### Consequences

- **Good:** the data crosses the network boundary hardened (mTLS + bearer + authz); the
  collector exposes no inbound port; the single binary is preserved.
- **Bad / trade-offs:** more moving parts for the distributed deployment.
- **Neutral:** the single-binary default uses the in-process / local-subprocess paths.

## Why the alternatives were rejected

- Neither **A** nor **B** alone covers multi-host scale; they are kept as the
  fast-path and the single-node substrate respectively, not as the distributed answer.

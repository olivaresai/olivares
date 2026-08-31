# ADR-0005: Embedded SQLite by default, Postgres + RLS for scale

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T4); data-model design

## Context and problem statement

The control plane stores a multi-tenant data model (the access graph is a *view* over
it). It must run as a zero-dependency single binary for small/air-gapped installs, yet
scale to multi-host, multi-tenant deployments.

## Decision drivers

- Zero external dependencies for the single-binary / air-gap path.
- Strong multi-tenant isolation at scale.
- No CGO, to preserve a pure-Go static binary.

## Considered options

- **SQLite (pure-Go) → Postgres + row-level security.**
- **A graph database** (Neo4j, Dgraph) for the access graph.

## Decision outcome

Chosen option: **embedded SQLite** (`modernc.org/sqlite`, pure-Go, no CGO) for
single-node and air-gap; **Postgres** (via `pgx`) with **row-level security** keyed on
`tenant_id` for multi-host, scale and multi-tenant. The access graph is modeled as a
**view over the general data model**, not a separate store.

### Consequences

- **Good:** the single binary has no database to install; the same model scales to
  Postgres with per-tenant RLS isolation.
- **Bad / trade-offs:** two storage backends to support; RLS correctness must be tested
  (it is — under forced RLS in CI).
- **Neutral:** the access graph needs no special graph engine because it is a view.

## Why the alternatives were rejected

- **Graph database** — heavy to self-host and overkill: the access graph is a view over
  the relational model, not a workload that needs a dedicated graph engine.

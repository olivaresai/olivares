---
title: "Module III — the read/write access map"
description: >-
  A key differentiated capability: a read/write access map of every origin→resource
  edge, with the Permitted-vs-Observed diff (least-privilege drift). How edges are
  built, classified and trusted, and the limits.
---

Module III is the **read/write access map**: which origin (agent, identity, session)
touches which resource, classified read or read-write, and the **Permitted-vs-Observed
diff** that surfaces least-privilege drift. It is one of the product's most useful and
differentiated capabilities — one of the 30 modules, not the whole product. This page is
the reference for what the map is and how to read it honestly.

## The edge

The map is a graph of **edges**. Each edge is the normalized, minimal-data fact
`origin → resource`, carrying:

| Field | Values | Meaning |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | the read/write classification (`unknown` when it cannot be determined — never guessed) |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | which signal produced the edge |
| **confidence** | `attributed` \| `approximate` | how firmly the access is tied to the origin |

Edges arrive on the event bus as [`edge.observed`](/reference/events/) events, and the
engine merges them into the persisted `AccessEdge` entity — which itself carries both
the **permitted** and the **observed** sides, so the access map is a **view over the
general data model**, not a separate store.

## How edges are built

Module III crosses two paths:

- **Cooperative path** — agents that emit OpenTelemetry (`otel`) and expose MCP
  servers. Combined with **native store audit**, this is high-fidelity: Postgres
  pgAudit (`pg_audit`) classifies READ/WRITE verbatim; AWS CloudTrail (`cloudtrail`)
  gives S3 `readOnly`; warehouses similarly.
- **Non-cooperative path** — a kernel-level **eBPF/Tetragon backstop** (`ebpf`)
  records `MAY_READ`/`MAY_WRITE` at the syscall level, outside the agent's control
  (anti-evasion), blind to the encrypted body.

MCP tool annotations (`readOnlyHint`/`destructiveHint`, source `mcp_annotation`) are a
useful signal but are **untrusted by the MCP specification** — the product
**corroborates** them and never trusts them alone.

The **permitted** side (source `policy`) comes from declared grants; the **observed**
side comes from the signals above.

## Permitted vs Observed (least-privilege drift)

The defining view is the **diff** between what an origin is *permitted* to touch and
what it is *observed* touching. It surfaces:

- **unexpected accesses** — an origin used a resource it was never granted;
- **unused grants** — a permission no origin ever exercised;
- **reconciliation-pending** — an access the system cannot yet firmly attribute.

The [zero-to-graph tutorial](/tutorials/zero-to-graph/) reaches a populated drift
result on the demo estate.

:::caution[Honest limits]
- **Per-agent identity is a hard dependency.** Audit attributes activity to a
  credential or role, not inherently to an agent. A shared service account with a
  connection pool collapses attribution to `approximate`. Governing well means issuing
  identity per agent (the bridge to module VI).
- **Coverage is tiered.** *Clean* on stores with native audit (SQL, object storage,
  warehouses); *lossy* on some stores (document/vector); **impossible to reconstruct
  passively** on others (e.g. Redis, SQLite, D1). An absent edge is **not** proof an
  access did not happen where coverage is lossy or absent.
- **`unknown` and `approximate` are shown, not hidden.** The product never fabricates a
  classification or certainty it does not have.
:::

## Reading the map

The access-map results — including the Permitted-vs-Observed drift — are served by
module routes published in the separate **beta** [module-route reference](/reference/api-beta/)
(not the stable core contract); their field-level shapes live in the product's typed
Go/TypeScript interfaces, and the web UI renders the graph and the drift overlay over them. Reading
the access graph is a **privileged, tenant-scoped, fully-audited** action (the editor
role and up, never the lowest viewer) — see the
[security model](/explanation/security/security-model/) and
[threat model](/explanation/security/threat-model/).

## Related

- [Event bus reference](/reference/events/) — the `edge.observed` event and its payload.
- [Architecture overview](/explanation/architecture/overview/) — where module III sits.
- [Govern and approve](/how-to/govern-and-approve/) — acting on drift.

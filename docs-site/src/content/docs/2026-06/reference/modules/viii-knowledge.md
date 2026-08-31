---
title: Module VIII — data, knowledge & context
description: "The governed data plane for what agents know and use: knowledge
  bases and semantic RAG over a pluggable vector index, retrieval governed by
  identity/classification/residency, and append-only lineage that proves the
  customer's data never left the perimeter."
slug: 2026-06/reference/modules/viii-knowledge
---

Module VIII is the **governed data plane**: it builds knowledge bases and runs
**semantic RAG** over a pluggable vector index, governs every retrieval by
identity, classification and residency, and records **append-only lineage** that
proves the customer's data never left the perimeter. It also holds the versioned
prompt registry, governed agent memory and context/compaction policies as data —
not as promises.

## What it is

The module **orchestrates** the data plane; it does not re-implement its
neighbours. It pulls content from **read-only data connectors**, runs every body,
prompt template and memory entry through its **own redactor before** anything is
chunked, embedded, hashed or stored, then governs retrieval against the grants the
identity module declares. Embedding is delegated to a model seam — the module never
calls a provider directly — and ranking is delegated to a vector-index seam, so the
governance contract is identical whether retrieval runs in-process or against an
external ANN backend.

The **red line** is non-negotiable: the product governs the customer's data and
never sells, exfiltrates or routes it out of the perimeter. Three mechanisms record
that in the design — redaction before indexing, the egress gate, and lineage that
proves non-exfiltration.

## Contract & entities

Module VIII declares **eight tenant-scoped entities** in the shared data model: the
knowledge base, the document (metadata and provenance, never the body), the chunk
(redacted text plus an inherited classification and ACL), the prompt and its
**append-only** immutable revisions, governed agent memory, the context/compaction
policy, and the **append-only** lineage row. Its routes mount under the module's
own namespace, wrapped with authentication, tenant scoping and authorization;
reading knowledge and lineage is a **privileged, audited** action.

Retrieval is the security contract, and **the order is the contract**: resolve the
identity's grants (fail-closed — a guard error denies, never a degraded allow),
apply the residency gate, embed the query, then **filter candidates by
classification and ACL before ranking** so a chunk the identity cannot see never
enters the ranked set, then rank, then append the immutable lineage row. The
**egress gate** is composed on top: a residency-locked knowledge base refuses
ingest or retrieval with an embedder that would egress, enforced at create, update,
ingest and retrieval (defence in depth). Document content travels a typed connector
contract by design, **not** the event bus — bulk reference data must not be
broadcast.

## On the event bus

Module VIII **produces** [`finding.reported`](/2026-06/reference/events/) events: one hashed
`FindingReport` per ingest when a secret or PII is redacted, and a finding when a
residency or egress gate denies — hashed detail only, never the secret or the body.
Forensics and compliance consume the lineage and these findings. It **consumes**
nothing from the bus for content: by design content rides a typed pull contract, so
minimal-data is a property of the wire, not a runtime filter applied after the fact.

:::caution[Honest limits]
* **Semantic quality depends on a configured embedder.** The default embedder is
  **local and zero-egress** but **non-semantic** (a deterministic feature-hash
  fallback). The knowledge base records its embed model so the fallback is never
  mistaken for semantic quality, and the binary warns once when it runs degraded. A
  model-backed embedder is configured by the operator (`OLIVARES_EMBEDDINGS_*`); set
  `OLIVARES_EMBEDDINGS_REQUIRE=1` and the boot **refuses to start** rather than serve
  lexical vectors as if they were semantic.
* **Residency is a fail-closed egress gate, not an inference setting.** Choosing an
  inference region does not by itself satisfy a residency-locked knowledge base — the
  embedder must be provably in-region, or ingest and retrieval are refused. An
  identity with no clearance or no region normalises to public / no-region, never to
  a broader grant.
* **Default ranking is exact and in-process** (a linear scan, suited to a self-hosted
  or air-gapped node up to roughly 10⁵ chunks per tenant). An external ANN backend
  plugs in behind the vector-index seam for scale; a configured-but-down backend
  **denies the request**, never falls back silently to different results.
* **Connector live transport is a documented follow-up.** Connectors today parse the
  native exported format with fixtures behind a stable interface; with no export
  configured a source is simply empty. Ingest is synchronous; large-scale async
  ingest is a follow-up.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module VIII sits and its honest actuate status.
* [Event bus reference](/2026-06/reference/events/) — the `finding.reported` event and its payload.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine, the seams and the layers.
* [Connect a source](/2026-06/how-to/connect-a-source/) — registering a read-only data connector.
* [Air-gap install](/2026-06/how-to/air-gap-install/) — running the data plane with zero egress.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the product-wide honest contract.

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package knowledge is module VIII — data, knowledge & context: the GOVERNED DATA
// PLANE for what agents know and use. It builds knowledge bases + RAG over a
// pluggable vector index, ingests content from read-only data connectors, governs
// retrieval by identity/classification/residency, records append-only data lineage
// proving the customer's data never left the perimeter, and stores a versioned
// prompt registry plus governed agent memory and context/compaction policies.
//
// # The RED LINE (non-negotiable, docs/SECURITY-HARDENING.md)
//
// The product GOVERNS the customer's data; it NEVER sells, exfiltrates or routes it
// out of the perimeter. Self-hosted = the data does not leave. Every design
// decision here is built around that:
//
//   - MINIMIZATION BEFORE INDEXING: every ingested body, prompt template and
//     memory entry is run through the wired Redactor before it is chunked,
//     embedded, indexed, chunk-hashed or persisted (docs/SECURITY-HARDENING.md). The shipped
//     binary wires the security module's deterministic sensitivity catalog, so
//     every value that catalog RECOGNIZES — credentials and key material, email,
//     IBAN, Luhn-valid cards, US SSN, ES NIF/NIE, IPv4, colon-form MAC and
//     Bitcoin-like wallet addresses — is removed before any of that happens. NOT
//     before the DOCUMENT content hash: ingest.go hashes the RAW body first and
//     persists that digest as the change-detection identity of the document. It
//     is a one-way SHA-256, so it discloses nothing, but the ordering is real and
//     this sentence used to claim otherwise. What the catalog does not recognize (a name, a postal
//     address, a phone number in free text) is NOT removed, so this is
//     deterministic minimization and not a guarantee that no personal data can
//     persist. Unwired, the module falls back to its own shapes (redact.go),
//     which cover credentials and email only. A redaction emits ONE FindingReport
//     per ingest (hashed detail only, never the value).
//
//     This paragraph used to say "secrets/PII never reach the store or an
//     embedder". NEITHER half was true as an absolute, though they failed by
//     different distances. PII was the wide gap: the redactor removed exactly one
//     personal-data shape (email) while the sensitivity catalog recognized EIGHT
//     more families — IBAN, Luhn-valid card, US SSN, ES NIF/NIE, IPv4, MAC,
//     wallet — and merely LABELED them, which it could only do because they had
//     survived into the persisted chunk text. The secrets side was narrower but
//     not whole either: an opaque secret with no recognizable key and no
//     high-precision shape is undetectable by any signature catalog, and the old
//     fallback ran its key=value rule BEFORE its shapes, so "secret=-----BEGIN
//     PRIVATE KEY-----…" lost its header and kept its key body. No finite
//     catalog supports the word "never".
//
//   - THE EGRESS GATE: embedding sends text to the embedder. A knowledge base
//     declares an embed policy (local_only / model_backed / auto). A local_only
//     (air-gap / residency-locked) KB is REFUSED ingest or retrieval with an
//     embedder that egresses — chunk text and the query never leave. The gate is
//     enforced at KB create, KB update, ingest and retrieval (defense in depth).
//
//   - LINEAGE PROVES NON-EXFILTRATION: every governed retrieval appends an
//     immutable lineage row (origin→answer: which chunks/sources, which decision,
//     egress=false for a local embedder) plus a hash-chain self-audit. It is the
//     evidence (compliance) and (forensics) consume.
//
// # Bounded context (what this module is, and is not)
//
// It ORCHESTRATES the data plane; it does not re-implement its neighbors:
//
//   - own the model providers and embeddings governance/cost. This module
//     ORCHESTRATES embedding via the Embedder seam (chunk → embed → index); it
//     never calls a provider directly. The default embedder is LOCAL and
//     zero-egress (a deterministic feature-hash fallback, NON-SEMANTIC — wire an
//     Model-backed embedder for semantic retrieval; the KB records
//     embed_model so the fallback is never mistaken for semantic quality).
//   - own permissions/identity. This module APPLIES their grants at
//     retrieval via the RetrievalGuard seam (chunk-level filter by classification +
//     ACL BEFORE ranking, plus the residency gate). It does not decide policy; a
//     guard error fails CLOSED (deny), never a degraded allow.
//   - (data-store R/RW audit) and (runtime/cloud inventory) are DISTINCT:
//     this module ingests CONTENT for knowledge (contentsource.ClassDocument), not
//     access edges or inventory. The boundary is enforced at ingest (a non-document
//     source is rejected).
//   - owns prompt EVALUATION; this module owns the prompt REGISTRY (versioned,
//     immutable revisions, referenceable). Consume the lineage
//     orchestration drives the memory and context/compaction policies this module
//     stores as governed data.
//   - own the store, the entity registry and the audit hash-chain; this
//     module registers its entities (eight at birth added the discovery/
//     DLP four the scoped-memory one) and self-audits through them.
//
// # Content ingestion (decision)
//
// Document CONTENT for RAG is bulk reference data, not a flow fact: it cannot ride
// the sealed Observation bus and must not be broadcast (minimal data). It travels a
// typed Apache contract, connectors/contentsource (the twin of identitysource),
// which the read-only data connectors (gdrive/confluence/notion/sharepoint/
// s3content) implement and this module pulls from. The module imports that Apache
// package (Apache → AGPL is allowed); a connector never imports the module.
//
// # Seams wired at the composition root (honest Fase C caveat)
//
// Like every Fase C module, this one is built and tested against the engine seams;
// the real boot fan-out does not exist yet. The Embedder and RetrievalGuard default
// to SAFE behavior so an un-wired knowledge plane governs conservatively and never
// exfiltrates: the embedder runs LOCALLY (zero egress, non-semantic), the guard
// permits only public/unrestricted content. A VectorIndex backend is optional (the
// default ranks governance-filtered candidates in-process with exact cosine; an
// external ANN backend like pgvector plugs in for scale). Start() warns once per
// degraded/un-wired seam so a non-semantic or ungoverned plane is VISIBLE.
//
// Update 2026-06-08 (Fase L): the "real boot fan-out" the caveat above
// says does not exist yet now DOES exist — cmd/olivares/wire.go (buildModules)
// wires knowledge.New with a real RetrievalGuard (the governance bridge, store
// late-bound in boot.go), a model-backed Embedder (env OLIVARES_EMBEDDINGS_*,
// with OLIVARES_EMBEDDINGS_REQUIRE refusing boot rather than serving lexical
// vectors as semantic) and an ANN VectorIndex (env OLIVARES_VECTOR_BACKEND,
// pgvector/Qdrant). The zero-egress local embedder and the public-only guard
// remain the SAFE defaults when those seams are unconfigured. The paragraph
// above is kept as design history.
//
// # A module cannot enumerate tenants
//
// There is therefore no cross-tenant background sweep. Embedding recovery (after a
// partial ingest) and memory purge are TENANT-SCOPED endpoints (reindex, purge),
// and memory expiry is also applied lazily on read — never a background job that
// would need to walk every tenant.
package knowledge

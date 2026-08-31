# Architecture Decision Records (ADR)

This directory is the **canonical register of architecture decisions** for the
Olivares AI control plane, in [MADR](https://adr.github.io/madr/) format. Each record
captures a decision **already taken** — its context, the option chosen, the
consequences, and the alternatives rejected — so the *why* survives between sessions
and contributors.

## ⛔ These records are INTERNAL. They are not published, by order of 2026-08-25.

These files are the single source of truth **and the only copy**. Until 2026-08-25 the
documentation site published them: `docs-site/scripts/sync-adr.mjs` copied each `NNNN-*.md`
into the site's *Explanation → Architecture decisions* section at build time, producing 229
generated pages across the root, the `2026-06` snapshot and six locales.

**The project owner withdrew the whole section that day.** The reason, and it is the one to
carry into any future proposal to republish: there is no advantage in publishing the ADRs or any internal /
development documentation on the public web and docs, and doing so can compromise the
integrity and security of the project and its paid business/enterprise part. The pages, the
generator, its strings file, its battery and its `lint:adr-sync` gate were all removed.

**Nothing here changed and nothing here is deprecated** — this register keeps doing exactly
what it always did for the people who build the product. What ended is its publication.
`task lint:adr-not-published` fails if any of it comes back to `docs-site/`.

Non-English ADRs (`es/`, `de/`, `fr/`, `ja/`, `ru/`, `zh/`) are **machine-translated**;
the English records in this directory are authoritative. They stay for the same reason the
English ones do: they are read by whoever works on this repository.

## Adding an ADR

1. Copy [`adr-template.md`](./adr-template.md) to `NNNN-kebab-title.md`, taking the
   next free number.
2. Record a decision that has **already been made**, with its source reference. Do
   **not** open new decisions, and do **not** relitigate closed ones.
3. If a new decision supersedes an old one, set the old record's status to
   *superseded by ADR-XXXX* rather than deleting it.

## Amending a record (never rewriting it)

An ADR is a record of a decision **taken on a date**. Editing its text so it says something
else falsifies the project's own history, which is the opposite of the honest labelling this
repo enforces everywhere else. So:

- **Never rewrite the Context / Decision / Consequences / Alternatives of a record that has
  already been merged**, not even to correct a claim that later became false. The only edits
  allowed in place are typo/link fixes that change no meaning.
- When a later decision changes what a record says, **append a section**
  `## Amendment (YYYY-MM-DD)` at the end — optionally with a short tag for the deciding item,
  e.g. `## Amendment (2026-07-27, B10)`. It must state (a) what specifically no longer holds,
  (b) what is true instead, and (c) **where the current decision lives** (`file:line`, another
  ADR, or a canon document (e.g. the privately-maintained commercial pricing canon)). Earlier amendments are records
  too: a later correction gets its own new section, it does not edit the previous one.
- Supersession by a **new ADR** additionally sets the status, per point 3 above. Amendment
  notes are for the common case where the superseding decision lives outside the ADR register
  (a commercial canon, a ratified decision document) and no new ADR was cut.
- ADRs 0010 and 0011 are the worked examples of this pattern.

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](./0001-use-madr-for-decisions.md) | Record architecture decisions using MADR | accepted |
| [0002](./0002-complete-product-not-wedge.md) | Ship the complete product (28 modules), not a wedge | accepted |
| [0003](./0003-rrw-map-permitted-vs-observed.md) | The R/RW map with a Permitted-vs-Observed diff is the differentiator | accepted |
| [0004](./0004-go-single-static-binary.md) | Engine in Go, one static binary with the web embedded | accepted |
| [0005](./0005-sqlite-default-postgres-scale.md) | Embedded SQLite by default, Postgres + RLS for scale | accepted |
| [0006](./0006-in-process-event-bus-nats-ready.md) | In-process event bus by default, transport-agnostic for NATS | accepted |
| [0007](./0007-go-plugin-module-runtime.md) | Out-of-process module/connector runtime via go-plugin (gRPC) | accepted |
| [0008](./0008-opaque-tokens-over-jwt.md) | Opaque server-side tokens, not JWT, for first-party auth | accepted |
| [0009](./0009-append-only-hash-chain-audit.md) | Append-only, hash-chained, signed audit ledger | accepted |
| [0010](./0010-license-attestation-only.md) | License is attestation only — never gate features | accepted |
| [0011](./0011-license-boundary-agpl-apache-commercial.md) | License boundary: AGPL product, Apache SDK/connectors, commercial enterprise | accepted |
| [0012](./0012-distributed-ingest-push-collectors.md) | Distributed ingest: collectors push to the core over gRPC + mTLS | accepted |
| [0013](./0013-pdp-cedar-plus-opa.md) | Authorization PDP: Cedar embedded + OPA-over-HTTP adapter | accepted (restrict-only, scoped to the `PolicyEvaluator` seam) — **amended by ADR-0019**, which removes the base permit: an operator permit rule that this overlay silently neutralised now grants. Forbid-only policies are unaffected. |
| [0014](./0014-public-ci-github-actions-docker.md) | Public release & CI on GitHub Actions + Docker | accepted |
| [0015](./0015-supply-chain-signed-sbom-slsa.md) | Supply chain: signed releases, SBOM, SLSA Build L3 (SLSA v1.2) provenance, OpenVEX, distroless | accepted |
| [0016](./0016-external-connector-distribution.md) | External connector ecosystem: public SDK, signed admission, releases/OCI distribution, curated verified index | accepted |
| [0017](./0017-distributed-bus-nats-bridge-at-most-once.md) | Distributed event bus = in-proc local fan-out + NATS bridge, core NATS at-most-once (no JetStream in v1) | accepted (amends ADR-0006's delivery-semantics line; extended by ADR-0021) |
| [0018](./0018-voice-realtime-posture-v1.md) | Realtime voice backend — documented dormant posture in v1, integration post-v1 | accepted |
| [0019](./0019-cedar-scoped-grants.md) | Cedar as a positive, scoped grant engine (not a deny-only overlay) | accepted |
| [0020](./0020-enterprise-private-repo-distribution.md) | Enterprise edition distributed from a separate private repository | accepted |
| [0021](./0021-durable-jetstream-bus-enterprise-addon.md) | Durable JetStream event-bus backend (at-least-once + bus-boundary dedup) as a closed enterprise add-on | accepted (extends ADR-0017) |
| [0022](./0022-source-scoping-subject-axes.md) | Source scoping by subject axis (session / agent / user / user-group / role), with row-level effect and a versioned, dual-controlled enforcement posture | accepted |
| [0023](./0023-context-policy-and-group-ceilings.md) | Context-policy enforcement at the three transit points, with per-group window and spend ceilings | accepted |
| [0024](./0024-ddil-offline-semantics-and-signed-bundle.md) | DDIL offline semantics per plane, and one signed bundle format | accepted |
| [0025](./0025-finops-reserve-ledger-toctou.md) | FinOps reserve→commit/release ledger closes the budget/spend-limit TOCTOU | accepted |
| [0026](./0026-ap2-mandates-as-cedar-grants.md) | AP2 payment mandates as Cedar scoped grants (governed procurement) | proposed (design only; the enterprise build lands in a separate phase) |
| [0027](./0027-managed-cloud-split-ingress-l4-mtls.md) | Managed-cloud ingress: L4 passthrough for collector mTLS, L7 for the control-plane API | accepted (managed cloud; this record creates no infrastructure) |
| [0028](./0028-managed-cloud-postgres-rls-tenant-boundary.md) | Managed-cloud database: managed PostgreSQL, with row-level security as the tenant boundary | accepted (managed cloud; this record creates no infrastructure) |
| [0029](./0029-managed-cloud-single-primary-region.md) | Managed-cloud regions: one primary region, residency answered by self-hosting | accepted (managed cloud; this record creates no infrastructure) |

---
title: Module XIV — internal catalog & marketplace
description: The internal, curated registry of approved-for-the-org agents, MCP
  servers, skills, templates, models and third-party connectors. How an entry is
  versioned, frozen, hash-pinned and signed on approval, how self-service
  instantiation is governed, and the limits.
slug: 2026-06/reference/modules/xiv-catalog
---

Module XIV is the organization's **internal catalog** — a curated, governed registry
of the agents, MCP servers, skills, templates, models and third-party connectors
that have been **approved for reuse**
across the company. It exists so an estate standardizes on vetted, versioned
capabilities instead of ad-hoc copies, and so "approved" means something verifiable
rather than a word in a wiki. It sits in the Intelligence layer and has **no actuation
surface**: it curates and records, while provisioning happens elsewhere.

## What it is

The catalog is a **registry**, not a document store. An **entry** is one curated,
versioned definition of a reusable capability, of kind `agent`, `mcp`, `skill`,
`template`, `model` or `connector`. Each `(kind, slug, version)` is its **own immutable artifact** — publishing
a new version creates a new entry, and approval and signing happen **per version**. An
entry moves through a fixed lifecycle:

`draft → pending → approved → deprecated`

Only a **draft** is mutable; **approval freezes it**. An entry's spec is an
operator-authored *definition* — transport, model and prompt references, scope, and
**references** to secrets — never a credential value. The create/approve path refuses a
spec that carries inline credentials, so the module stores definitions, references and
governance metadata and never secrets or payloads.

## Versioning, freezing and signing

Approval is where "approved" becomes verifiable:

* **Content hash.** On approval the entry is pinned by a **SHA-256 content hash** over
  its canonical, deterministically-serialized preimage. Every operator-authored field is
  covered, so any later mutation of an approved entry is **detectable** — tamper-evident
  even without a signature.
* **Ledger attestation.** The approval is recorded in the append-only, hash-chained
  audit ledger, attributed to the **real principal** who approved it.
* **Ed25519 signature.** When a catalog signing key is provisioned, approval also
  produces a **detached Ed25519 signature** over the content hash, carrying the public
  key and a short fingerprint — "approved = verifiable". The signing key is loaded or
  minted at boot under the engine's fail-closed key seam, **independent of** the audit
  ledger key; the module owns its catalog key and never reaches the engine's internal
  audit signer, keeping the trust boundary clean.

Verification recomputes the hash and, when a key is configured at the node, treats the
signature as the **trust anchor**: a stripped signature (downgrade) or one made by any
other key (substitution) is reported **not verified**. `GET …/pubkey` reports whether
signing is enabled; per-entry `verified` / `signed` / `signed_by` state is returned by
the entry and verify routes.

## Verified third-party connectors

A `connector` entry curates a **released third-party connector plugin** — a built
binary or OCI artifact. Its spec records what it curates: the artifact's `sha256`
(`artifact_digest`), the release/OCI reference, the publisher and the connector's
descriptor name. The entry is the tenant-facing **certification record** of the
external-connector ecosystem: "approved" can be made to mean "its supply-chain
attestation was verified", not just "someone clicked approve".

The flow mirrors the MCP-entry admission pair, with its own policy and verdict
records (evidence is counted per kind, so connector verdicts never share tables
with MCP verdicts):

* `GET`/`PUT …/connector-admission/policy` — the per-tenant trust root:
  `require_signed`, optional `require_subject_digest`, Sigstore identity/issuer
  pins, bare public keys, CA roots, and the in-toto **predicate allow-list**
  (defaults to SLSA provenance v1/v0.2 and SPDX/CycloneDX SBOMs — provenance and
  SBOM shapes, because a connector is a built artifact, not model weights). No
  policy means **observe mode** — nothing is gated until the tenant opts in, and
  the policy endpoint says so honestly.
* `POST …/entries/{id}/admit` — one shared route, dispatched by entry kind
  (`mcp` or `connector`): verifies an operator-supplied Sigstore attestation
  bundle and records one **claim-vs-verified verdict** per entry. When the
  request does not pin an `expected_digest`, the binding **defaults from the
  entry's `spec.artifact_digest`** — the entry names the artifact it curates, so
  the admission binds to that artifact unless explicitly overridden. A malformed
  bundle is a `400`; a well-formed bundle that fails to verify is a **recorded
  negative verdict**, not an error.
* `GET …/connector-admissions` — the recorded verdicts, filterable by entry
  (`entry_ref`) and restrictable to verified verdicts (`verified=true`).
* **Deny-closed approve gate.** With `require_signed` on, a connector entry can
  only be approved (and thus listed as a *verified* connector) with a verified
  provenance/SBOM admission verdict **bound to the digest the entry currently
  curates** (`spec.artifact_digest`); with `require_subject_digest` on, that
  artifact binding must itself be confirmed. Editing the curated digest after an
  admission invalidates the gate — a re-admit against the new artifact is
  required.

:::caution[Honest limits]
The catalog **certifies**, it does not execute: the host-side gate that decides
whether a connector plugin may actually *run* lives in the control plane, not
here. Attestation bundles are operator-supplied (`cosign download attestation` /
`gh attestation download`) — fetching them from OCI referrers is an external
step, and Rekor transparency-log **inclusion** is not natively verified (the
verdict records the material's presence and says exactly what was checked).
:::

## Governed self-service instantiation

An **instance** is a self-service request to instantiate an **approved** entry — only an
approved entry can be instantiated. The module records the request, its **provenance**
(which entry version it came from), its target and its governance status, and enforces a
sane state machine (`requested → approved`/`rejected → active`). It does **not** decide
who may approve, nor does it provision: the approval **decision** belongs to governance
and the actual provisioning to deployment. Approving, deprecating, signing and
instantiating are **privileged, RBAC-gated and self-audited** to the real principal.

:::caution[Honest limits]
* **No actuation, no provisioning.** Module XIV records and governs the *request*; it
  never stands a capability up. The approval decision is governance's and the wiring is
  deployment's — and live `apply`/`retire` there is itself a deny-closed seam (`503`
  until an executor is provisioned). See [Honesty & limits](/2026-06/start/honesty-and-limits/).
* **Signing is real but key-dependent.** Ed25519 signing is implemented and the signing
  key is provisioned at boot on-by-default. On a node with **no key configured** (or an
  invalid key), an approved entry is **hash-pinned and ledger-attested but unsigned** —
  the API says so honestly via `signing_enabled`/`signed` rather than implying a
  signature exists.
* **Curated, not observed.** The catalog does **not** subscribe to or emit on the event
  bus; it is populated by people through its API, not derived from live observations. It
  asserts what the organization *approved for reuse*, not what is currently running.
* **The module does not enforce the approval policy.** It enforces the state machine and
  RBAC verb tiers; *who* may approve and under what conditions is decided by governance.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module XIV sits and the
  govern/observe vs actuate split.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — the human-in-the-loop approval flow.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — the engine, layers and
  the shared data model entries declare into.
* [Event bus reference](/2026-06/reference/events/) — the bus this module deliberately does not
  consume.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the actuation posture across the product.

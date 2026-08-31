<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# S142 — External connector SDK + certification + distribution (contract)

**Gap:** *no third-party connector SDK / external marketplace (first-party
in-tree only)*. **Decision (2026-06-09):** v1 = SDK + certification + signing;
distribution over GitHub releases/OCI; **static curated "verified connectors" index in the docs
site**; NO hosted marketplace (commercial decision deferred). Full rationale: **ADR-0016**.
Consumes (never reimplements): S02 (SDK/plugin runtime), the connector pattern, the sources
composition root, and `modelsign` signed admission.

---

## 1. The public SDK contract (Apache zone)

- `sdk` + `sdk/plugin` are **declared stable v1 for connector authors**. The normative policy is
  **`sdk/VERSIONING.md`** (mirrored on the docs-site stability page): author-implemented interfaces
  never gain methods within v1 (new capability = new optional interface + type-assert, the connector
  pattern); structs additive; vocabularies open strings; `Observation` sum type sealed; wire frozen
  by `buf breaking`; hard gate = `ProtocolVersion` (=proto major); `Descriptor.APIVersion` advisory.
  Tags `sdk/vX.Y.Z` / `sdk/plugin/vX.Y.Z` / `sdk/scaffold/vX.Y.Z` land at the first public release
  (`olivaresai/olivares` paths); windows bind from GA (same pre-1.0 honesty as the API-stability work).
- **Wire honesty fix (additive proto, S142):** `CostSample.speed = 21`,
  `FindingReport.owasp_llm/owasp_asi/atlas = 8/9/10` now cross the gRPC wire (they were silently
  dropped pre-S142); `convert.go` maps them and nil-normalizes `Notification.Fields` on decode like
  labels. Roundtrip-tested.

## 2. Scaffold — `sdk/scaffold` (new Apache module, stdlib-only)

- `scaffold.Generate(Options{Dir, Name "<vendor>.<connector>", Module, Kind source|output,
  WithPlugin, SDKPath})` + CLI **`cmd/olivares-connector-new`** emit a complete out-of-tree
  connector repo: contract-correct skeleton, lifecycle test (in-memory sink), plugin `main`
  (`plugin.ServeSource/ServeOutput`), README (build→test→sign→distribute→operate→certify) and a
  **standalone `scripts/check-boundary.sh`** (the third-party form of the in-tree check: fail if
  `go list -deps` reaches `github.com/olivaresai/olivares/core`). Deny-closed validation; refuses a
  non-empty target dir. `SDKPath` emits `replace` directives (the pre-tag dev loop).
- Wired in `go.work` and in `FORBIDDEN_MODULES` (`scripts/check-boundary.sh`) — the scaffold itself
  can never import `/core`. Tests: generation matrix, boundary-by-construction grep, and a hermetic
  offline **compile test** of the generated module (GOWORK=off + local `replace`; plugin variant
  under `-tags e2e` with the module-cache file proxy).

## 3. Host-side deny-closed admission of EXTERNAL plugins (AGPL)

- `core/runtime`: `LoadSourcePluginVerified(path, cfg, tenant, sha256Hex)` — go-plugin
  **`SecureConfig`** (sha256) so the verified bytes are the executed bytes (TOCTOU pin); malformed
  pin refuses. First-party embedded loads stay `LoadSourcePlugin` (provenance = the release build).
- `cmd/olivares/externalplugins.go`: `sourceSpec.plugin {path, sha256, bundle,
  predicate_types}` + root `connector_trust {allowed_identities, allowed_issuers, trusted_keys,
  trusted_roots, allowed_predicates}` in `OLIVARES_SOURCES_CONFIG`. `admitExternalPlugin` (pure,
  unit-tested) verifies: trust anchors present → digest pin well-formed → file digest matches →
  bundle present → `modelsign.VerifyAttestation(bundle, trust, predicates, fileDigest)` with
  predicates = request-narrowed-never-widened over policy/defaults (SLSA v1/v0.2 + SPDX +
  CycloneDX; **no OMS** — binaries aren't weights). Any failure ⇒ WARN + **not wired**.
  **No observe mode, no allow-unsigned escape hatch** for external binaries; the dev loop is
  bare-key (sign with your own key, trust your own pubkey). `wireSources` branches on `plugin`
  BEFORE the kind maps; `Kind` not consulted; `PollSeconds` ignored (same as first-party plugins).
- v1 wires external **sources** only; outputs ship the same way but the notify composition has no
  external-plugin path yet (documented honest limit).

## 4. Certification record — module XIV catalog overlay (AGPL)

- New entry kind **`connector`** + a model-admission-mirror admission pair
  **`catalog.connector_admission_policy` / `catalog.connector_admission`** (own tables/kinds —
  evidence counted BY KIND, never shared rows). Same policy guards (public material
  only, anchors required when `require_signed`, identities↔issuers together-or-neither).
- Routes (`/v1/m/catalog`): `GET/PUT /connector-admission/policy` (read/admin),
  `GET /connector-admissions`; **`POST /entries/{id}/admit` now dispatches by entry kind**
  (mcp → MCP flow unchanged, connector → S142 flow, others → 400). Delta vs MCP: when the request
  omits `expected_digest`, it defaults from the entry's `spec.artifact_digest` (the entry names the
  artifact it curates). Audit: `catalog.connector_admission.{configure,admit}`.
- **Deny-closed approve gate**: approving a `connector` entry is refused (409) under
  `require_signed` without a verified verdict (and digest binding under `require_subject_digest`);
  policy ABSENT ⇒ observe mode (the catalog overlay never breaks un-opted tenants — unlike the host
  exec gate, which is always deny-closed).
- Relationship: catalog admission = tenant-facing **certification trail**; `connector_trust` at the
  composition root = host **exec enforcement**. Decoupled on purpose (operator owns what executes;
  tenants own what is curated) — the model-admission admit-route/deployment-gate pair, transposed.

## 5. Distribution channel + verified index (design, no infra)

- **ADR-0016**: GitHub release (binary + sha256 + Sigstore bundle) and/or OCI artifact (ORAS,
  attestation as referrer). Docs-site **`reference/verified-connectors`** is the curated index:
  listing by PR, re-verified per release (boundary, signature/provenance vs publisher identity,
  contract correctness, minimal-data spot review). The index documents verification performed —
  it is **NOT a trust root**; operators always pin publisher identity/key in `connector_trust`.
- Authoring lifecycle: docs-site **`how-to/build-a-connector`**. Module XIV public page updated
  with the connector kind + admission routes.

## 6. Out of scope / follow-ups (documented, not promised)

- External OUTPUT plugin wiring (notify composition); out-of-process modules (S02 seam, still
  unwired); OCI pull by the host (operator places files in v1; the digest pin makes transport
  irrelevant to trust); a compliance capability probed from `catalog.connector_admission` (cheap,
  KIND-probe pattern); connector BOM/AIBOM seal kind; npm scope `@olivaresai` + module-proxy tags at
  public export; hosted marketplace (commercial, deferred).

## 7. Verification (tests, not smoke)

- `sdk/plugin`: new-field roundtrips; `task proto:check` + `proto:breaking` green (additive).
- `cmd/olivares`: `admitExternalPlugin` table — trusted+matching admits; no-trust / no-anchor /
  bad-pin / digest-mismatch / unsigned / untrusted-signer / wrong-predicate / malformed-bundle all
  refuse; digest input normalization.
- `core/runtime`: malformed pin errors pre-exec; wrong checksum refuses launch.
- `modules/catalog`: policy validations; admit signed→verified / untrusted→recorded-false /
  malformed→400; kind dispatch (mcp regression intact); spec-digest defaulting; approve gate
  observe/409/200 matrix; kind `connector` accepted.
- `sdk/scaffold`: generation matrix; boundary-by-construction; generated module compiles + its
  tests pass offline; generated `check-boundary.sh` exits 0.

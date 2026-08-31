# ADR-0016: External connector ecosystem — public SDK, signed admission, releases/OCI distribution, curated verified index

- **Status:** accepted
- **Date:** 2026-06-11
- **Deciders:** Fran Olivares (v1 scope decided 2026-06-09)
- **References:** `LICENSING.md` (license boundary), ADR-0007 (go-plugin runtime),
  ADR-0011 (AGPL/Apache/commercial), ADR-0015 (supply chain),
  `docs/contracts/S02-sdk-runtime-eventbus.md`,
  `docs/contracts/S142-external-connector-sdk.md`

## Context and problem statement

The connector SDK (`sdk`, `sdk/plugin`) was designed from day one so that a
connector never links the AGPL engine (Apache-2.0, zero dependencies, gRPC
plugin transport — ADR-0007, ADR-0011), and ADR-0007 explicitly anticipated
that "third parties can ship connectors independently". But no mechanism
existed: the SDK modules are untagged and consumed only through the monorepo
workspace, the composition root launches only **embedded first-party** plugin
binaries (`go:embed`), `LoadSourcePlugin` executes whatever path it is handed
with **no integrity or provenance check**, and the module-XIV catalog curates
internal entries only. "Can my team or a partner build and publish a
connector?" had no answer.

Opening the ecosystem cannot mean "the host loads any `.so`-style binary an
operator points at": this is a security product; an unsigned, unattested
executable wired into the observation plane would be a supply-chain hole.

## Decision drivers

- The amplitude moat **composes** only if third parties can contribute
  connectors safely (`ARCHITECTURE.md`, `LICENSING.md`).
- The license boundary (connector = Apache, never imports `/core`) must be
  verifiable **by the third party**, not only in our CI.
- Signature + admission machinery already exists and is proven (model
  admission, MCP-entry admission, `core/secure/modelsign`): reuse, never
  reimplement.
- No hosted marketplace infrastructure in v1 (commercial decision deferred).

## Considered options

- **Option A — hosted marketplace service**: a registry service operated by
  Olivares.AI with upload/review/serve.
- **Option B — SDK + certification + signing, distribution over GitHub
  releases/OCI, curated static "verified connectors" index in the docs site;
  deny-closed signed admission at the host.**
- **Option C — open plugin loading** (operator-supplied path, no signature),
  certification as documentation only.

## Decision outcome

Chosen option: **Option B** (decided 2026-06-09).

1. **Public SDK contract.** `sdk` and `sdk/plugin` are declared **stable v1**
   for connector authors, with an explicit versioning/deprecation policy
   (`sdk/VERSIONING.md`, surfaced in the docs-site stability page). Semver
   tags (`sdk/v1.*`, `sdk/plugin/v1.*`) land with the first public release of
   the repository; until then authors pin a commit (the scaffold's
   `-sdk-path` covers the development loop).
2. **Scaffold + guide.** A zero-dependency generator
   (`sdk/scaffold`, CLI `olivares-connector-new`) emits a complete out-of-tree
   connector repository — contract-correct source/output skeleton, lifecycle
   test, plugin `main`, README and a **standalone boundary check** (the same
   `go list -deps` rule `scripts/check-boundary.sh` enforces in our CI, so the
   third party verifies the AGPL/Apache frontier in *their* CI).
3. **Distribution channel.** A released connector ships as a **GitHub release
   asset** (binary + `sha256` + Sigstore attestation bundle) and/or an **OCI
   artifact** (ORAS, attestation as referrer). No hosted marketplace in v1.
4. **Signed admission, deny-closed at the host.** An external plugin runs only
   if the operator's sources config pins its digest AND a Sigstore/DSSE
   supply-chain attestation (SLSA provenance / SBOM predicate) over that digest
   verifies against an operator-configured trust policy
   (`connector_trust`), reusing `modelsign.VerifyAttestation`. The
   loader additionally pins the checksum at exec time (go-plugin
   `SecureConfig`). **There is no observe mode and no allow-unsigned escape
   hatch for external binaries** — the development loop is "sign with your own
   key, trust your own public key" (bare-key mode).
5. **Certification record (catalog overlay).** Module XIV gains a `connector`
   entry kind with its own admission pair
   (`catalog.connector_admission_policy` / `catalog.connector_admission`):
   verified provenance/SBOM verdicts per entry, deny-closed approve gate,
   observe-mode default — the tenant-facing certification trail, decoupled
   from the host exec gate (defense in depth, like the model admission's
   admit-route + deployment-gate pair).
6. **Verified connectors index.** A **curated static page** in the docs site
   (`reference/verified-connectors`) lists third-party connectors whose
   release the maintainers have re-verified (boundary, signature, provenance,
   minimal-data review). Listing is by pull request; the index is
   documentation of verification performed, **not** a trust root — operators
   still pin the publisher's identity/key in `connector_trust`.

### Consequences

- **Good:** third parties build, sign and ship connectors without touching the
  AGPL engine; the host never executes unattested code; certification reuses
  proven machinery; zero new services to operate.
- **Bad / trade-offs:** no discovery/install UX beyond docs + releases (a
  hosted marketplace would give one); operators manage trust anchors by hand;
  external **output** connectors build and ship the same way but host-side
  external wiring covers observation sources first (the notify composition has
  no external-plugin path yet).
- **Neutral / follow-ups:** OCI *pull* by the host (today the operator places
  the binary on disk; the digest pin makes transport irrelevant to trust);
  out-of-process modules remain unwired; a compliance capability probed
  from connector admissions; npm scope `@olivaresai` and module-proxy tags at
  public export.

## Why the alternatives were rejected

- **Option A** — operating a marketplace is a commercial commitment that was
  explicitly deferred; it adds a trust-critical service with no
  v1 demand.
- **Option C** — "load any binary" is exactly the supply-chain hole this
  product exists to close; certification-as-prose without enforcement would be
  design-for-audit theater (`docs/SECURITY-HARDENING.md`).

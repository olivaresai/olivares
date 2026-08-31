---
title: Build and ship a connector
description: >-
  Scaffold, implement, test, sign and distribute a third-party connector with
  the public Apache-2.0 connector SDK — and wire it into a control plane with
  deny-closed signed admission.
---

This guide takes you from nothing to a **signed, third-party connector** an
operator can wire into the control plane. The connector SDK is Apache-2.0 and
imports nothing from the AGPL engine, so your connector is **your** code under
**your** license, built in **your** repository.

What you build is a normal Go program: a type implementing
`sdk.SourceConnector` (gathers facts, emits observations) or
`sdk.OutputConnector` (delivers notifications), or `sdk.ContentSource`
(serves documents and ACL references to governed knowledge), packaged as a
[go-plugin](https://github.com/hashicorp/go-plugin) binary the engine launches
out-of-process and talks to over gRPC (mutually-authenticated loopback,
AutoMTLS). Read [connect a source](/how-to/connect-a-source/) first for the
connector *model* — observe-only, minimal-data, the three observation kinds.

:::note[Stability]
The SDK contract (`Descriptor/Open/Gather/Close`, the wire, the plugin
handshake) is **stable v1** — see
[API stability](/reference/api-stability/) and `sdk/VERSIONING.md` in the
repository. Until the first public semver tags are published, build against a
checkout of the repository (`-sdk-path` below).
:::

## 1. Scaffold

Preferred porcelain:

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

Choose one of the five archetypes. They are presets over stable SDK surfaces,
not new author contracts:

| Template | Declared surfaces | When to use |
|---|---|---|
| `content-source` | `knowledge.document` | Documents for governed knowledge ingestion, including out-of-process content sources. |
| `access-edge-source` | `observation.edge` | Access graph, identity, SaaS and infrastructure relationship facts. |
| `output-sink` | `notify.sink` | Notification or ticketing sinks. |
| `agent-surface` | `observation.edge`, `observation.finding` | Agent runtime adapters that report access edges and findings. |
| `model-provider` | `observation.cost`, `observation.edge` | Provider inventory, usage and cost observations; model governance stays engine-side. |

The older standalone scaffold remains valid and generates the same stable
author contracts:

Run this from a checkout of the repository (until the first public SDK tags
are published, the package resolves through the workspace, and `-sdk-path`
points at that checkout's `sdk/`):

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

You get a complete repository: the connector skeleton, a lifecycle test, the
plugin `main`, a README with this whole lifecycle, and
`scripts/check-boundary.sh` — the **same license-boundary check our CI runs**,
for yours. `-name` is your `Descriptor.Name`: globally unique, dotted,
`<vendor>.<connector>`.

## 2. Implement

The contract, in short (the godoc on `sdk.SourceConnector` is normative):

- **`Open`** reads configuration (declared in your `Descriptor.ConfigFields`;
  secrets are *references*, marked `Secret: true`, never inlined). Fail here,
  not in `Gather`.
- **`Gather`** emits observations to the engine's `Sink`. The **engine owns
  scheduling**: a batch source does its work and returns; a streaming source
  blocks until `ctx` is canceled. Never own your own ticker.
- Delivery is **at-least-once**; consumers de-duplicate on the observation's
  natural key. Don't track delivery state.
- **Minimal data**: emit references and metadata, never payloads, prompts or
  secret values.
- For `content-source`, **`List`** returns refs cheap enough to enumerate,
  **`Fetch`** returns one document body, and optional `DeltaContentSource`
  adds live deltas plus ACL refresh. Content-source plugins that implement the
  optional interface auto-declare `content.delta`; hosts do not call delta
  methods unless that capability was declared.

Run your tests, then prove the license boundary in your CI:

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. Package and sign

Build the plugin binary, pin its digest, and attach a supply-chain
attestation as a **Sigstore bundle**. The control plane verifies SLSA
provenance or SBOM attestations (SPDX / CycloneDX predicates) — sign with
your own key (shown here) or keylessly with your CI identity:

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. Distribute

Publish a **GitHub release** with the binary, its `sha256` and the
`.sigstore.json` bundle — or push the same artifacts to an OCI registry with
`oras push` (attestation as a referrer). Version with semver; declare the
`ProtocolVersion` you were built against (v1 today) in your README.

## 5. Operate (what your users do)

The operator places the binary and bundle on the host and pins **both the
digest and the trust** in the sources config (`OLIVARES_SOURCES_CONFIG`):

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

Admission is **deny-closed, with no escape hatch**: no trust anchors, no
bundle, a digest mismatch, an untrusted signer or a wrong predicate type all
mean the source is **not wired** (the boot says why). On success the engine
re-hashes the binary at exec (go-plugin `SecureConfig`) so the verified bytes
are the executed bytes, and the subprocess channel is AutoMTLS-pinned.

Content-source plugins use the same root `connector_trust` and the same
per-source `plugin { path, sha256, bundle }` shape under the `documents`
configuration block. They are first-class out-of-process content sources for
knowledge ingestion.

A trust anchor is **mandatory** — `connector_trust` with neither
`trusted_roots` nor `trusted_keys` is refused outright. For **keyless**
signing the anchor is the Fulcio (or private-CA) root, so the operator sets
`trusted_roots` (the root PEM, e.g. from `cosign initialize`) **plus**
`allowed_identities` and `allowed_issuers` (both, together — the SAN identity
and the OIDC issuer the signature must carry); only `trusted_keys` is
replaced. The bare-key example above is the simplest anchor.

## 6. Get certified (optional but recommended)

Two complementary records:

- **In-product certification** — your users curate your connector as a
  catalog entry (kind `connector`, module XIV) and record a verified
  provenance/SBOM admission verdict against your released digest
  (`POST /entries/{id}/admit`); with `require_signed` on, approval is
  deny-closed on that verdict. See
  [module XIV](/reference/modules/xiv-catalog/).
- **The verified connectors index** — submit your connector for listing on
  [Verified connectors](/reference/verified-connectors/): the maintainers
  re-verify your release (boundary, signature, provenance, minimal-data
  review) and list it. The index documents verification; it is **not** a
  trust root — operators still pin *your* identity/key themselves.

## Governed by construction

Enforcement is engine-side by construction: connectors do not link governance
code and cannot opt out. The engine keys controls on the configured source
identity (`source_type`, `source_ref`), applies source scoping, ACL
intersection, DLP/retrieval scanning, admission and audit, and treats
`Descriptor.Surfaces` as advisory metadata only — never as an enforcement
input.

Private connectors are first-class. You can keep a connector inside your
enterprise, never publish it and never list it publicly; it is still governed
when the operator pins the binary digest and trust root. The verified
connectors index documents certification; it is not a trust root.

## Honest limits (v1)

- External wiring covers **observation sources** and **content sources**; an
  output connector builds and ships identically but the notify composition does
  not yet load external output plugins.
- Out-of-process **modules** are not available (the proto is frozen, the host
  glue intentionally unwired).
- The observation sum type is **sealed**: you emit edges, cost samples and
  findings — with open string vocabularies — but cannot define new
  observation kinds.

<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: Apache-2.0
-->

# Crossplane XRD inventory connector (`olivares.crossplane`)

Read-first source connector that takes the **Crossplane composite
API surface** of a cluster into the Olivares AI estate inventory. It parses exported
`CompositeResourceDefinition` (XRD) manifests and records, per XRD, the composite
resource **type** the platform team introduced — its API group, composite kind, and
declared versions.

## What it is — and what it is not

- **Introspection only.** It parses exported XRD manifests (a file or directory). It
  never calls the Crossplane or Kubernetes API.
- **Not an Operator.** The Olivares Crossplane Operator (acting *on* the estate through
  a controller) is a separate component — out of scope here. This connector runs no
  controller, reads no Composite Resources (XRs) or Claims (XRCs), reads no live
  status, and **mutates nothing**.
- **Inventory, not risk.** Every XRD becomes one `FindingReport` at `Severity = info`:
  it catalogues a composite *type*, it does not grade a risk.

## Configuration

| Key    | Type   | Required | Description |
| ------ | ------ | -------- | ----------- |
| `path` | string | yes      | An XRD manifest file, or a directory of `*.yaml` / `*.yml` / `*.json` manifests. Multi-document YAML supported. |

Export the XRDs a cluster exposes with:

```sh
kubectl get xrd -o yaml > xrds.yaml
```

## What it reads (and only this)

For each `CompositeResourceDefinition` (`apiextensions.crossplane.io/v1`):

- `metadata.name` — the XRD name, used as the finding `SubjectRef`
  (e.g. `xdatabases.custom-api.example.org`).
- `spec.group`, `spec.names.kind`, `spec.names.plural` — the composite API surface.
- each `spec.versions[]` — its `name`, `served` and `referenceable` flags, surfaced in
  the Title so the inventory shows **which** versions are live vs deprecated.

It **MAY** count the top-level `required` field *names* of a version's
`openAPIV3Schema` (field names are part of the public API contract). It **never**
reads or emits a schema property's value, default, or description — those can carry
environment-specific detail (payload-adjacent, minimal-data — see `docs/SECURITY-HARDENING.md`).

## Emitted finding

| Field         | Value |
| ------------- | ----- |
| `Kind`        | `crossplane_xrd` |
| `Severity`    | `info` (inventory) |
| `SubjectKind` | `crossplane.xrd` |
| `SubjectRef`  | `metadata.name` |
| `Title`       | e.g. `crossplane: composite resource definition xdatabases.custom-api.example.org (kind XDatabase, versions: v1alpha1[served], v1beta1[not served])` |
| `DetailHash`  | hex SHA-256 of a stable, scrubbed key `crossplane:<name>:<group>/<kind>:<versions>` — never the raw manifest |

No edges are emitted: an XRD declares a **type** in the inventory, not an access flow.

## Notes on accuracy

- **CNCF graduation date.** Crossplane's graduation was **announced 2025-11-06** per the
  primary sources (the Crossplane blog post *"Crossplane Becomes a CNCF Graduated
  Project"* and the CNCF announcement). The date `2025-10-28` that circulates is
  **unverified** by any primary source and is not encoded anywhere in this connector
  (no-fabrication).
- **Version caveat.** The Crossplane minor version is intentionally **unpinned**: this
  connector reads only the long-stable XRD structural fields (group / names / versions
  / served / referenceable), which are identical across recent releases. Crossplane
  **v2** changed composite resource *semantics* (e.g. namespaced composites and the
  claim-model rework) relative to v1.x, but none of those behavioural differences
  affect the structural fields parsed here.

## Licensing & boundary

Apache-2.0. Imports only the Olivares SDK (`sdk`, `sdk/model`), connector-internal
helpers (`connectors/internal/redact`), the standard library, and `gopkg.in/yaml.v3`.
It never imports the engine (`/core`) — enforced by `scripts/check-boundary.sh`.

<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Publishing the Olivares provider

The release contract for `terraform-provider-olivares` — how a tagged build becomes a
signed, Registry-ingestable artifact, how an air-gapped operator mirrors it, and what
the *same* artifact gives an [OpenTofu](https://opentofu.org) consumer. This is a
**separate Go module** with its **own** signing model (GPG, not the control plane's
cosign/SLSA): the Terraform Registry parses a GPG-signed `SHA256SUMS`, so its rules —
not the control-plane pipeline's — dictate the layout below.

> **No auto-publish.** A pushed `terraform-provider-v*` tag runs
> [`release-provider.yml`](../../.github/workflows/release-provider.yml), which builds
> and GPG-signs into a **draft** GitHub release (`release.draft: true` in
> [`.goreleaser.yaml`](../.goreleaser.yaml)). Promoting the draft and registering the
> version with the Registry are deliberate human actions.

> **Version caveat (re-verify against the tofu-controller upstream).**
> Where this document states an upstream behaviour that is version-sensitive — the
> OpenTofu OCI-mirror limitation in particular — the cited fact was verified against
> OpenTofu ~1.12 / the current HashiCorp docs at time of writing. These limitations
> move; check the cited URL before relying on them. Versions deliberately **not**
> hardcoded here.

---

## 1. Registry asset layout

The provider speaks **Terraform plugin protocol 6** (`main.go` serves it via
`providerserver.Serve`; the protocol is declared in
[`terraform-registry-manifest.json`](../terraform-registry-manifest.json) as
`{"version":1,"metadata":{"protocol_versions":["6.0"]}}`).

GoReleaser produces, for each tagged version `X.Y.Z`
([publishing reference](https://developer.hashicorp.com/terraform/registry/providers/publishing)):

| Asset | Notes |
| --- | --- |
| `terraform-provider-olivares_vX.Y.Z` | the plugin binary, inside each zip |
| `terraform-provider-olivares_X.Y.Z_<os>_<arch>.zip` | one per `goos`/`goarch` (Linux/Darwin/Windows/FreeBSD × amd64/arm64/386, minus windows/freebsd arm64) |
| `terraform-provider-olivares_X.Y.Z_SHA256SUMS` | SHA-256 over **every** zip **and** the renamed manifest json |
| `terraform-provider-olivares_X.Y.Z_SHA256SUMS.sig` | **GPG binary** (not ASCII-armored) **detached** signature of `SHA256SUMS` |
| `terraform-provider-olivares_X.Y.Z_manifest.json` | the protocol manifest; GoReleaser attaches `terraform-registry-manifest.json` via `release.extra_files` and the Registry renames it on ingest |

The build/sign config that emits exactly these names is
[`.goreleaser.yaml`](../.goreleaser.yaml) (HashiCorp's
`terraform-provider-scaffolding-framework` template); the CI that runs it is
[`release-provider.yml`](../../.github/workflows/release-provider.yml).

## 2. The GPG signing requirement (and key registration)

The Registry verifies the provider with **GPG**, and the rules are strict
([publishing reference](https://developer.hashicorp.com/terraform/registry/providers/publishing)):

- The `.sig` must be a **GPG binary detached** signature of the `SHA256SUMS` file.
  GoReleaser's `signs` block produces it with `gpg … --detach-sign` (no `--armor`); an
  ASCII-armored `.asc` is rejected.
- The signing key must be **RSA or DSA** (RSA 4096 recommended). **ECC keys are
  rejected.**
- **Cosign is NOT accepted by the Registry for providers** — this is the one place the
  control-plane's keyless-cosign model does *not* apply.
- The **public** half of the key must be **registered in the Registry account** under
  which the provider is published. Without it, the uploaded version fails at ingest.

CI wiring: [`release-provider.yml`](../../.github/workflows/release-provider.yml) imports
the private key (`crazy-max/ghaction-import-gpg`, secrets `GPG_PRIVATE_KEY` +
`PASSPHRASE`) and exports `GPG_FINGERPRINT`, which `.goreleaser.yaml`'s `signs` block
passes to `gpg --local-user`.

## 3. OpenTofu consumability

**The same artifact works.** OpenTofu is protocol-compatible with current Terraform
providers — no separate build, no separate publish
([OpenTofu providers](https://opentofu.org/docs/language/providers/)).

### OCI coordinate (secondary mirror)

OpenTofu can install the provider from an **OCI registry mirror** (e.g. `ghcr.io`),
which is well-suited to existing container-registry infrastructure and air-gap. This is
a **consumer-side** install setting — the provider neither implements nor requires it:

```hcl
# OpenTofu CLI config (.tofurc / tofu.rc) — consumer side
provider_installation {
  oci_mirror {
    repository_template = "ghcr.io/olivaresai/${namespace}/${type}"
    include             = ["registry.terraform.io/olivaresai/olivares"]
  }
}
```

**OCI is a SECONDARY source only.** OpenTofu "supports OCI Registries as a *secondary*
installation source for provider plugin packages" and "does **not yet** support using
an OCI Registry as the *primary* installation source for a provider"
([OCI Registry Integrations](https://opentofu.org/docs/cli/oci_registries/),
[Provider Mirrors in OCI Registries](https://opentofu.org/docs/cli/oci_registries/provider-mirror/)).
So the Registry/GPG release above remains the **origin**; OCI is an *additional* place
to pull it from. (This OCI-as-primary limitation is version-sensitive — re-verify per
the version caveat above.)

### State / plan encryption posture

State and plan encryption is a **consumer-side runtime feature of OpenTofu** — again,
the provider neither implements nor obstructs it. A consumer enables it in their own
config ([State and plan encryption](https://opentofu.org/docs/language/state/encryption/)):

```hcl
terraform {
  encryption {
    key_provider "pbkdf2" "k" { passphrase = "…(min 16 chars)…" }
    method "aes_gcm" "m" { keys = key_provider.pbkdf2.k }
    state { method = method.aes_gcm.m }
    plan  { method = method.aes_gcm.m }
  }
}
```

Encryption method is **AES-GCM**; key providers are **PBKDF2 / AWS KMS / GCP KMS /
Azure Key Vault / OpenBao**. Nothing in this provider reads, writes, or constrains the
encrypted state — it is transparent to the provider.

## 4. Air-gap: network / filesystem mirror

For environments with no Registry access, install from a **network mirror** populated
from the released zips by [`scripts/provider-mirror.sh`](../../scripts/provider-mirror.sh):

```sh
# from a dir of released zips (the goreleaser dist/ output)
scripts/provider-mirror.sh --in dist/ --out mirror/
# serve mirror/ over HTTPS, then on the consumer:
```

```hcl
# Terraform CLI config (.terraformrc) — or OpenTofu .tofurc (identical layout)
provider_installation {
  network_mirror { url = "https://mirror.internal/providers/" }
}
```

The script emits the
[provider network mirror protocol](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol)
documents under `<host>/<namespace>/<type>/`:

- `index.json` → `{"versions":{"X.Y.Z":{}}}`
- `X.Y.Z.json` → `{"archives":{"<os>_<arch>":{"url":…,"hashes":["h1:…"]}}}`

> **Why `h1:`, not the `SHA256SUMS` hex.** The network-mirror protocol requires the
> **`h1:`** hash scheme; Terraform/OpenTofu **reject** `zh:` hashes from a network
> mirror. `zh:` is the hex SHA-256 of the whole `.zip` (what `SHA256SUMS` holds);
> `h1:` is SHA-256 over the **sorted per-entry `"%x  %s\n"` lines of the zip
> CONTENTS**, base64-encoded — a *different* value that **cannot** be derived from
> `SHA256SUMS`
> ([zh/h1 hashes](https://developer.hashicorp.com/terraform/language/files/dependency-lock#zh-and-h1-hashes)).
> `provider-mirror.sh` therefore computes `h1:` directly from each zip
> (`golang.org/x/mod/sumdb/dirhash`), so no hash is fabricated.

Equivalently, a consumer with transient access can generate a **filesystem mirror**
with the upstream tooling — `terraform providers mirror <dir>` or `tofu providers
mirror <dir>` — both write the same plugin-mirror directory layout
([Command: providers mirror](https://opentofu.org/docs/cli/commands/providers/mirror/)).

## 5. API stability and deprecation

The provider consumes **only the stable `/v1` REST surface** of the control plane,
under the public [API stability policy](https://olivares.ai/docs):

- **Deprecation signaling.** The control plane marks a deprecated route with an
  **RFC 9745** `Deprecation` header (a Structured Field Date, e.g. `@1782864000`),
  an **RFC 8594** `Sunset` header (an HTTP-date) once a retirement date is committed,
  and `Link` headers (`rel="deprecation"` → migration guide, `rel="sunset"`).
- **What the provider does with it.** The REST client watches every response at a
  single transport choke point and, the first time it sees a deprecated
  method+path in a run, emits a **WARN** log line (`tflog`) with the endpoint, the
  deprecation date, the sunset date and the migration-guide link — it logs a
  warning once per unique method and request path per run (a deprecated
  parameterized route warns once per resource it touches), visible with
  `TF_LOG=WARN` (or lower).
- **Attribution.** Every request carries
  `User-Agent: terraform-provider-olivares/<version>`, so the control plane can
  measure which provider releases still call a deprecated route during the window.
- **Support windows.** Minimum **24 months** (stable) / **12 months** (beta) from
  deprecation announcement to sunset, as defined by the policy page above.
  Pre-1.0, the signalling is live but the formal windows bind from the 1.0/GA
  release on (see the policy page's Pre-1.0 note).
- **Versioning.** The provider is versioned and released **independently** of the
  control plane (`terraform-provider-v*` tags, §1); its **MAJOR version tracks the
  API major** (v1) — a `/v2` REST surface would mean a v2 provider line.

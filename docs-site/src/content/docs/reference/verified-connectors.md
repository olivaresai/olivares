---
title: Verified connectors (third-party)
description: >-
  The curated index of third-party connectors whose releases the maintainers
  have re-verified — boundary, signature, provenance and minimal-data review —
  and how to submit yours.
---

This page is the **curated index of third-party connectors**. It is the
external companion to the [first-party connector catalog](/reference/connectors/):
first-party connectors ship inside the product; the connectors listed here are
built, released and supported by **their publishers** with the public
[connector SDK](/how-to/build-a-connector/).

## What "verified" means

A listed release has been re-verified by the maintainers, by hand, against
this checklist:

1. **License boundary** — the connector builds out-of-tree and links nothing
   from the AGPL engine (`go list -deps` shows no
   `github.com/olivaresai/olivares/core`); it imports only the Apache-2.0 SDK.
2. **Signature & provenance** — the published Sigstore attestation bundle
   verifies against the publisher's stated identity or public key, and its
   subject digest matches the released binary.
3. **Contract correctness** — `Descriptor.Name` is dotted and
   vendor-namespaced, the declared `ConfigFields` match what `Open` reads,
   secrets are declared `Secret: true` and taken by reference.
4. **Minimal data** — the connector emits references and metadata, never
   payloads, prompts or secret values (spot review of the emit paths).

**What it does not mean:** verification is not a security audit of the
publisher or of the observed system, not an endorsement, and **not a trust
root** — an operator wiring a verified connector still pins the publisher's
key or identity in `connector_trust` and the release digest in the source's
`plugin` block. Admission at the host stays deny-closed either way.

A private connector needs no listing here to be governed. If an operator pins
its digest and trust anchor in `connector_trust`, the engine applies the same
deny-closed admission and runtime governance. This index is a certification
trail for discoverability and re-verification, not a trust root.

## Index

No third-party connectors are listed yet — the program opens with this
release. First-party connectors are in the
[connector catalog](/reference/connectors/).

| Connector (`Descriptor.Name`) | Publisher | Kind | Verified release | Signature | Source |
|---|---|---|---|---|---|
| _none yet_ | | | | | |

## Submit a connector

Open a pull request against this page adding one table row, linking:

- the source repository and the release (binary + `sha256` + Sigstore
  bundle);
- the identity to verify against (OIDC identity + issuer for keyless, or the
  public key);
- the output of `./scripts/check-boundary.sh` and the test run in your CI.

The maintainers reproduce the checklist above on the exact release artifacts.
A new release of a listed connector needs a row update (re-verification is
per-release, because the verdict binds to the digest). Stale or yanked
releases are removed.

## Related

- [Build and ship a connector](/how-to/build-a-connector/) — the full lifecycle
- [Module XIV — internal catalog & marketplace](/reference/modules/xiv-catalog/) —
  in-product certification (connector entries + signed admission)
- [API stability](/reference/api-stability/) — the SDK stability contract
- [Verify a release](/how-to/verify-a-release/) — the same supply-chain
  discipline for the product's own artifacts

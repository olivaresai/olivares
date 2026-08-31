// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package modelsign verifies model/dataset signatures in the OpenSSF Model
// Signing (OMS) v1.0 format — the on-the-wire format produced by the Sigstore
// model-signing project (github.com/sigstore/model-transparency, PyPI
// "model-signing"). It is the cryptographic primitive behind signed-model
// admission gate: extend the software supply-chain machinery (cosign/SBOM/
// in-toto) to MODELS and DATASETS.
//
// An OMS signature is a detached Sigstore bundle whose DSSE envelope wraps an
// in-toto Statement v1 with predicateType "https://model_signing/signature/v1.0";
// the predicate carries a per-file digest manifest (predicate.resources[]) plus a
// serialization descriptor. Verification = (1) verify the DSSE signature over the
// PAE pre-image; (2) anchor the signer — for a Sigstore/Fulcio keyless or PKI
// certificate, verify the leaf chains to an operator-provisioned root AND its SAN
// identity + OIDC issuer match an allow-list; for a bare key, the key must be on
// the operator's trusted-key list; (3) optionally re-hash the on-disk artifact
// files and compare against the signed manifest.
//
// DESIGN DECISION: verify NATIVELY with the standard library —
// no sigstore-go/cyclonedx-go dependency. This keeps core's dependency/SBOM
// surface minimal (the repo's dep-isolation discipline) and is air-gap friendly
// (zero phone-home). The cost is an HONEST coverage seam: Rekor transparency-log
// INCLUSION is NOT verified here (Verdict.TransparencyLogVerified is always false,
// with a reason) — a node that needs tlog-inclusion proof runs an external cosign/
// sigstore-go step. The DSSE signature, the Fulcio certificate chain, the signer
// identity/issuer allow-list, the bare-key trust list and the per-file manifest
// re-hash ARE all verified natively and fully. This is the right coverage for the
// stated use case: self-hosted / third-party model artifacts and datasets (Claude
// itself is never admitted — Anthropic publishes no weights).
//
// The verifier is PURE: it takes bytes + a TrustPolicy and returns a Verdict, with
// no store, network, clock or filesystem dependency (artifact re-hashing operates
// on caller-supplied digests, not on disk). It is deny-closed: any failure to
// anchor trust yields Verdict.Verified == false with a reason — it never fabricates
// a pass. Live keys are never minted here (that is core/secure.LoadOrCreateSigningKey
// for the node's own keys); this package only VERIFIES third-party signatures.
//
// Primary sources (verified 2026-06-09): OMS v1.0 spec + schema + test vectors
// github.com/ossf/model-signing-spec (spec/v1.0.md, schemas/v1.0/predicate.schema.json,
// test-vectors/v1.0/valid/sigstore.bundle.json); DSSE github.com/secure-systems-lab/dsse;
// in-toto Statement v1 in-toto.io/Statement/v1; Sigstore bundle docs.sigstore.dev/about/bundle.
package modelsign

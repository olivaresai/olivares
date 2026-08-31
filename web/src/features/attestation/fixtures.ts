// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TEST-ONLY fixtures (the view no longer renders any of these — the live
// section queries the backend and the declared tabs consume attestation.data.ts
// directly). Two fixtures:
//  • exampleReleaseFixture — the declared-contract envelope for the pure-component
//    tests, assembled from the attestation.data.ts constants. BETA: no release
//    has actually been cut.
//  • binaryAttestationFixture — shaped EXACTLY like the live
//    GET /v1/m/observability/attestation response (modules/observability): a
//    workspace dev build with ldflags defaults, no VCS stamp, release not_published.
//  • publishedBinaryAttestationFixture — the SAME shape in the other polarity, which
//    the backend could not produce at all until 2026-08-13. Both are needed: a panel
//    tested on one polarity of a boolean is a panel tested on half its behaviour.
import {
  AIRGAP_CONTRACT,
  HELM_CHART_CONTRACT,
  IDENTITY_REGEXP,
  OIDC_ISSUER,
  PATCH_VELOCITY,
  RELEASE_ARTIFACTS,
  SBOM_CONTRACT,
  SCORECARD_CONTRACT,
  SLSA_PROVENANCE,
  VEX_CONTRACT,
} from './attestation.data'
import type { ReleaseAttestation, RunningBinaryAttestation } from './types'

/** The declared-contract envelope for the pure-component tests. The version is
 *  explicitly a "beta example" so it can never be mistaken for a published
 *  tag (none exist). */
export const exampleReleaseFixture: ReleaseAttestation = {
  version: 'v0.0.0-beta (example)',
  pipeline: '.github/workflows/release.yml (written, not activated)',
  draft: true,
  oidc_issuer: OIDC_ISSUER,
  identity_regexp: IDENTITY_REGEXP,
  artifacts: RELEASE_ARTIFACTS,
  slsa: SLSA_PROVENANCE,
  sbom: SBOM_CONTRACT,
  vex: VEX_CONTRACT,
  scorecard: SCORECARD_CONTRACT,
  patch_velocity: PATCH_VELOCITY,
  airgap: AIRGAP_CONTRACT,
  helm: HELM_CHART_CONTRACT,
}

/** A live running-binary attestation as a workspace dev build reports it: ldflags
 *  defaults verbatim ("dev"/"none"/"unknown"), main module "(devel)" with no sums
 *  (go.work), NO vcs.* stamp, release not_published, pipeline declared. */
export const binaryAttestationFixture: RunningBinaryAttestation = {
  binary: {
    version: 'dev',
    commit: 'none',
    build_date: 'unknown',
    go_version: 'go1.26.0',
    os: 'linux',
    arch: 'amd64',
    fips140: { enabled: false },
    self_sha256:
      'c4b8e54a9f1d3370a2e6b0c85f9721d4e8a3b6905c7d2f1e4a8b3c6d9e0f1a2b',
    main_module: {
      path: 'github.com/olivaresai/olivares/cmd/olivares',
      version: '(devel)',
    },
    module_sums: {
      external_deps: 142,
      sums_present: true,
      // Verbatim backend constant (modules/observability/attestation.go
      // moduleSumsDevelNote) — the note is evidence-derived there.
      note: 'deps without module sums are (devel) path/workspace members; external deps are counted by non-empty module sum',
    },
    vcs_stamp: {
      available: false,
      reason: 'go.work workspace build: Go stamps no vcs.* settings',
    },
    status: 'measured',
  },
  release: {
    published: false,
    status: 'not_published',
    // Verbatim backend constant (modules/observability/attestation.go releaseReason).
    reason: 'beta: no tag, signature or attestation accompanies this binary',
    // Verbatim backend constant (releaseProvenanceNote) — sent in BOTH polarities.
    provenance: {
      kind: 'self_declared',
      attested: false,
      note: 'SELF-DECLARED build provenance, not an attestation: the version stamp and the OTA anchor are both link-time values chosen by whoever linked this binary, and this process holds no trust anchor that was not also chosen then. A build carrying both facts is release-SHAPED; whether an official release was published is a repository/distribution fact this process cannot observe. `olivares version` reports the same anchors under the same caveat (cmd/olivares/main.go).',
    },
    signature_status: 'not_verified',
    signature_reason:
      'no release artifacts or attestation bundles exist for this binary',
    verifier_available: true,
    transparency_log: {
      verified: false,
      note: 'the native verifier never claims Rekor inclusion (core/secure/modelsign)',
    },
  },
  pipeline: {
    workflows: [
      'release.yml',
      'release-chart.yml',
      'release-provider.yml',
      'scorecard.yml',
      'patch-velocity.yml',
    ],
    status: 'declared',
    note: 'release pipeline exists in the source tree and runs only on a pushed v* tag. The running process cannot observe repository or CI state, so it cannot say whether that has ever happened.',
  },
  captured_at: '2026-06-11T10:30:00Z',
}

/** The SAME live shape as binaryAttestationFixture, but as a PUBLISHED release
 *  reports itself: an orderable ldflags version stamp and the embedded OTA anchor,
 *  which is the pair only a `-tags release` build carries
 *  (modules/observability/attestation.go releaseIdentity).
 *
 *  It exists because until 2026-08-13 the backend could not produce this payload at
 *  all — measuredRelease() returned not_published unconditionally — so the console's
 *  "Published" badge was rendered by no test and reachable by no build. Nothing in
 *  the view needed changing; the badge was already data-driven. What was missing was
 *  a fixture proving the customer-visible half works when the backend tells the
 *  truth, and a test that would notice if that badge ever stopped rendering.
 *
 *  signature_status stays not_verified on purpose: a running process cannot verify
 *  its own detached signature, and the panel must keep showing the calm "signing not
 *  available" state rather than inventing a verification it did not perform. */
export const publishedBinaryAttestationFixture: RunningBinaryAttestation = {
  ...binaryAttestationFixture,
  binary: {
    ...binaryAttestationFixture.binary,
    version: '26.8.0',
    commit: 'abc1234',
  },
  release: {
    published: true,
    status: 'published',
    reason:
      'release-stamped 26.8.0 (self-declared): this binary carries an orderable release version stamp AND an OTA verification anchor (ota-key=release) — the pair the release build injects. Both are link-time values chosen by whoever linked it, so this is build provenance, not proof that an official release was published',
    provenance: {
      kind: 'self_declared',
      attested: false,
      note: 'SELF-DECLARED build provenance, not an attestation: the version stamp and the OTA anchor are both link-time values chosen by whoever linked this binary, and this process holds no trust anchor that was not also chosen then. A build carrying both facts is release-SHAPED; whether an official release was published is a repository/distribution fact this process cannot observe. `olivares version` reports the same anchors under the same caveat (cmd/olivares/main.go).',
    },
    signature_status: 'not_verified',
    signature_reason:
      'this binary embeds the OTA verification anchor, but the detached signature over checksums.txt is not carried inside it: release artifacts are verified out-of-band (docs/RELEASE-VERIFICATION.md)',
    verifier_available: true,
    transparency_log: {
      verified: false,
      note: 'the native verifier never claims Rekor inclusion (core/secure/modelsign)',
    },
  },
}

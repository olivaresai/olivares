// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DECLARED supply-chain attestation contract — STATIC reference data, NO HTTP.
// There is no live attestation API and the project is BETA: no releases exist yet
// and the release pipeline is written but NOT activated. Per the build rules, a purely
// static DECLARED reference dataset lives in a *.data.ts and is labelled with a
// CaveatNotice ("Declared reference — AsOf …") citing its sources; we do NOT wire a
// live fetch that would 404 for static reference data.
//
// SOURCES (verified verbatim 2026-06-06):
//   • docs/RELEASE-VERIFICATION.md   — the "what ships" table, verify commands, honesty notes
//   • SECURITY.md §"Vulnerability remediation SLA" — the CVE SLA + patch-velocity cadence
//   • .goreleaser.yaml               — checksums/SBOM/cosign signing config
//   • .github/workflows/release.yml        — keyless cosign + SLSA provenance build
//   • Business artifact channel — chart package signature bundle
//   • .github/workflows/scorecard.yml      — the four OpenSSF Scorecard checks + cron
//   • .github/workflows/patch-velocity.yml — weekly rebuild + re-scan + SBOM/VEX refresh
//
// nothing here is invented — every value traces to a source line above.

import type {
  AirgapContract,
  HelmChartContract,
  PatchVelocity,
  RemediationSla,
  ReleaseArtifact,
  SbomCisaElement,
  SbomContract,
  ScorecardCheck,
  ScorecardContract,
  SlsaProvenance,
  VexContract,
  VexStatement,
} from './types'

/** AsOf date for this declared reference — when the sources were verified verbatim. */
export const ATTESTATION_AS_OF = '2026-06-06'

/** The OIDC issuer keyless signing relies on (docs/RELEASE-VERIFICATION.md). */
export const OIDC_ISSUER = 'https://token.actions.githubusercontent.com'

// The URLs below are written whole: scripts/check-emitted-urls.sh finds every URL
// the product emits by its literal, and declares each one in its record.

/** The workflow a release signature names (verify-release.sh, INSTALL.md). */
const RELEASE_WORKFLOW =
  'https://github.com/olivaresai/olivares/.github/workflows/release.yml'

/** Certificate identity regexp cosign verifies against: the release workflow at
 *  a MAJOR.MINOR tag (docs/RELEASE-VERIFICATION.md, verify-release.sh). Whole,
 *  because cosign matches it unanchored; built from the one workflow address. */
export const IDENTITY_REGEXP = `^${RELEASE_WORKFLOW.replaceAll('.', '\\.')}@refs/tags/[0-9]+\\.[0-9]+$`

/** A release tag is MAJOR.MINOR, the only tag that signing identity accepts. */
const RELEASE_TAG = /^\d+\.\d+$/

/** The page that holds a Community release's files (INSTALL.md). */
export const releasePage = (tag: string) =>
  `https://github.com/olivaresai/olivares/releases/tag/${tag}`

/** The source repository SLSA provenance names (verify-release.sh SOURCE_URI). */
const SOURCE_URI = 'github.com/olivaresai/olivares'

/** A platform as Go and the release archives spell it (linux, amd64…). */
const PLATFORM = /^[a-z0-9]+$/

/**
 * INSTALL.md's own check of a downloaded release, for one tag: the cosign
 * signature over checksums.txt by the release workflow at that tag, then the
 * SHA-256 of every file downloaded beside it. `null` for a version that is not
 * a release tag: no signature can name it.
 */
export function releaseCheckCommand(version: string): string | null {
  if (!RELEASE_TAG.test(version)) return null
  return [
    'cosign verify-blob',
    '--certificate checksums.txt.pem',
    '--signature checksums.txt.sig',
    `--certificate-identity ${RELEASE_WORKFLOW}@refs/tags/${version}`,
    `--certificate-oidc-issuer ${OIDC_ISSUER}`,
    'checksums.txt && sha256sum --ignore-missing -c checksums.txt',
  ].join(' ')
}

/**
 * verify-release.sh's own SLSA step for one release and platform: the
 * archive INSTALL.md names, the provenance the release's generator publishes
 * (multiple.intoto.jsonl), the source repository and the tag. `null` for a
 * version that is not a release tag or a platform no archive can name.
 */
export function slsaCheckCommand(
  version: string,
  os: string,
  arch: string,
): string | null {
  if (!RELEASE_TAG.test(version) || !PLATFORM.test(os) || !PLATFORM.test(arch))
    return null
  return [
    `slsa-verifier verify-artifact olivares_${version}_${os}_${arch}.tar.gz`,
    '--provenance-path multiple.intoto.jsonl',
    `--source-uri ${SOURCE_URI} --source-tag ${version}`,
  ].join(' ')
}

/** The "what ships with a release" table (docs/RELEASE-VERIFICATION.md §"What ships"). */
export const RELEASE_ARTIFACTS: ReleaseArtifact[] = [
  {
    id: 'checksums',
    name: 'checksums.txt',
    produced_by: 'goreleaser',
    signature_trust:
      'cosign signature checksums.txt.sig (+ .pem cert), keyless',
    trust_mechanism: 'keyless',
    status: 'declared',
  },
  {
    id: 'binaries',
    name: 'static binaries + tar.gz archives',
    produced_by: 'goreleaser',
    signature_trust: 'covered transitively by the signed checksums',
    trust_mechanism: 'transitive',
    status: 'declared',
  },
  {
    id: 'sbom-image',
    name: 'olivares.spdx.sbom.json + image attestation',
    produced_by: 'syft + cosign attest',
    signature_trust:
      'one SBOM per release, generated from the shipped image and attested to it by digest',
    trust_mechanism: 'keyless',
    status: 'declared',
    scp: 'SCP-03',
  },
  {
    id: 'vex',
    name: 'olivares.vex.openvex.json + image attestation',
    produced_by: 'govulncheck -format openvex + cosign attest',
    signature_trust: 'OpenVEX attestation driven by reachability',
    trust_mechanism: 'keyless',
    status: 'declared',
    scp: 'SCP-04',
  },
  {
    id: 'slsa',
    name: '*.intoto.jsonl',
    produced_by: 'slsa-github-generator (generic + container)',
    signature_trust: 'SLSA Build L3 provenance',
    trust_mechanism: 'keyless',
    status: 'declared',
    scp: 'SCP-01',
  },
  {
    id: 'image',
    name: 'docker.io/olivaresai/olivares (by digest; ghcr.io fallback, same digest)',
    produced_by:
      'goreleaser (ghcr.io build/sign) + mirror-dockerhub (cosign copy)',
    signature_trust:
      'cosign signature (keyless) + SBOM + VEX + SLSA attestations',
    trust_mechanism: 'keyless',
    status: 'declared',
  },
  {
    id: 'chart',
    name: 'Business channel: signed chart package',
    produced_by: 'Business chart package producer (publication unverified)',
    signature_trust: 'cosign signature bundle for the packaged chart .tgz',
    trust_mechanism: 'keyless',
    status: 'declared',
    scp: 'SCP-05',
  },
]

/** SLSA Build provenance (SCP-01) — docs/RELEASE-VERIFICATION.md + release.yml.
 *  HONESTY: L3 by construction, but the reusable workflow is pinned by SEMVER TAG,
 *  NOT a SHA — a SHA pin would break slsa-verifier's builder-ID check. So we do NOT
 *  claim "all dependencies SHA-pinned". */
export const SLSA_PROVENANCE: SlsaProvenance = {
  build_level: 'L3',
  by_construction: true,
  predicate_type:
    'in-toto Statement (ITE-6) · SLSA Provenance v1 predicate (https://slsa.dev/provenance/v1)',
  generator: 'slsa-github-generator (generic + container)',
  reusable_workflow_pin: 'semver_tag',
  status: 'declared',
  scp: 'SCP-01',
}

/** SBOM formats produced (.goreleaser.yaml `sboms:` + RELEASE-VERIFICATION.md). */
const SBOM_FORMATS: SbomContract['formats'] = [
  { name: 'SPDX', version: '2.3', note: 'the interchange baseline' },
  {
    name: 'CycloneDX',
    version: '1.6',
    note: 'security-native; models the build "Generation Context" via metadata.lifecycles',
  },
]

/** The CISA-2025 minimum-elements checklist. DRAFT: pre-decisional, public-comment —
 *  NOT law/finalized. `scripts/sbom-check-cisa.sh` fails when a field TYPE is wholly
 *  absent (hard) and warns on the per-component omissions the draft permits (soft). */
export const CISA_ELEMENTS: SbomCisaElement[] = [
  {
    key: 'Timestamp',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Tool Name',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Author',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Component Name',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Version',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Producer',
    enforcement: 'hard',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  { key: 'Hash', enforcement: 'soft', carried_by: 'SPDX 2.3 / CycloneDX 1.6' },
  {
    key: 'License',
    enforcement: 'soft',
    carried_by: 'SPDX 2.3 / CycloneDX 1.6',
  },
  {
    key: 'Generation Context',
    enforcement: 'soft',
    carried_by: 'CycloneDX 1.6 (metadata.lifecycles)',
  },
]

export const SBOM_CONTRACT: SbomContract = {
  formats: SBOM_FORMATS,
  attestation_predicate:
    'in-toto SBOM attestation (cosign verify-blob-attestation --type spdxjson)',
  linter: 'scripts/sbom-check-cisa.sh',
  cisa_elements: CISA_ELEMENTS,
  status: 'declared',
  scp: 'SCP-03',
}

/** Declared OpenVEX statements (SCP-04). The not_affected example mirrors the
 *  SECURITY.md justification verbatim. These are ILLUSTRATIVE declared statements,
 *  not live findings — no release has been scanned yet. */
export const VEX_STATEMENTS: VexStatement[] = [
  {
    id: 'vex-1',
    vuln_id: 'GO-2025-EXAMPLE',
    status: 'not_affected',
    justification: 'vulnerable_code_not_in_execute_path',
    author: 'Olivares.AI (security@olivares.ai)',
  },
  {
    id: 'vex-2',
    vuln_id: 'CVE-2025-EXAMPLE',
    status: 'under_investigation',
    author: 'Olivares.AI (security@olivares.ai)',
  },
  {
    id: 'vex-3',
    vuln_id: 'CVE-2025-PATCHED',
    status: 'fixed',
    author: 'Olivares.AI (security@olivares.ai)',
  },
]

export const VEX_CONTRACT: VexContract = {
  driver:
    'govulncheck Go call-graph reachability — unreachable CVEs are documented (not_affected) and do NOT start a remediation clock',
  attestation_predicate:
    'OpenVEX attestation (cosign verify-blob-attestation --type openvex)',
  statements: VEX_STATEMENTS,
  status: 'declared',
  scp: 'SCP-04',
}

/** The four OpenSSF Scorecard checks (scorecard.yml header). */
export const SCORECARD_CHECKS: ScorecardCheck[] = [
  {
    name: 'Pinned-Dependencies',
    what: 'every GitHub Action pinned by 40-char SHA',
    evidence: 'all workflows',
  },
  {
    name: 'Signed-Releases',
    what: 'cosign signatures + SLSA provenance on releases',
    evidence: 'release.yml',
  },
  {
    name: 'Token-Permissions',
    what: 'top-level read-all + per-job least privilege',
    evidence: 'permissions: blocks across workflows',
  },
  {
    name: 'Branch-Protection',
    what: 'protected default branch (review + status checks)',
    evidence: 'repository settings (GitHub)',
  },
]

export const SCORECARD_CONTRACT: ScorecardContract = {
  checks: SCORECARD_CHECKS,
  schedule: 'weekly, Monday 06:00 UTC (POSIX cron 0 6 * * 1)',
  external_note:
    'Scorecard runs on the public GitHub repository (it evaluates GitHub repo posture) and publishes the badge to api.scorecard.dev — this is EXTERNAL data, not engine telemetry.',
  status: 'declared',
  scp: 'SCP-07',
}

/** CVE-remediation SLA (SECURITY.md §"Vulnerability remediation SLA"). */
export const REMEDIATION_SLA: RemediationSla[] = [
  { severity: 'critical', cvss_range: '9.0–10.0', target: '7 days' },
  { severity: 'high', cvss_range: '7.0–8.9', target: '14 days' },
  { severity: 'medium', cvss_range: '4.0–6.9', target: '30 days' },
  { severity: 'low', cvss_range: '< 4.0', target: 'next scheduled release' },
]

/** Patch-velocity cadence (SCP-11; patch-velocity.yml + SECURITY.md). */
export const PATCH_VELOCITY: PatchVelocity = {
  schedule: 'weekly, Tuesday 06:00 UTC (POSIX cron 0 6 * * 2)',
  steps: [
    'rebuild the image from the latest patched, digest-pinned distroless base',
    're-run the reachability gate (govulncheck) on HEAD',
    're-scan the rebuilt image (grype + trivy)',
    'check whether the pinned distroless base has a newer digest',
    'refresh the SBOM (SPDX + CycloneDX) and the OpenVEX document',
    'open/update a tracking issue against the remediation SLA — does NOT auto-publish',
  ],
  kev_rule:
    'Actively-exploited vulnerabilities (CISA KEV or credible in-the-wild reports) are treated as Critical regardless of base score, and may ship out of band.',
  sla: REMEDIATION_SLA,
  status: 'declared',
  scp: 'SCP-11',
}

/** Enterprise offline bundle, built from the private assembled distribution. */
export const AIRGAP_CONTRACT: AirgapContract = {
  composition: [
    'every image pinned by digest',
    'the cosign-saved images + their signatures',
    'the signed Helm chart',
    'the SBOM, OpenVEX and SLSA provenance',
    'a VERIFY.md with the exact offline cosign verify steps',
  ],
  build_command:
    'scripts/airgap-bundle.sh --version <v> --image …:<v>-amd64 --chart deploy/helm/olivares --cosign-key cosign.key',
  mirror_command:
    'scripts/airgap-mirror.sh --bundle olivares-airgap-<v>.tar.gz --registry registry.internal:5000',
  no_phone_home:
    'The engine makes NO mandatory outbound calls at boot (docs/08 §4), and the key-based verify path needs no Rekor or Fulcio. The one command that reaches us is `olivares upgrade`: it contacts the update channel (`olivares.ai/updates`, or `licenses.olivares.ai` with `--enterprise`) unless `--endpoint` points it at your own mirror.',
  status: 'declared',
}

/** Business chart package contract (SCP-05).
 * Publication remains unverified until the release act completes. */
export const HELM_CHART_CONTRACT: HelmChartContract = {
  distribution:
    'Business channel: signed chart package (publication unverified)',
  cosign_package:
    'cosign signature bundle for the packaged chart .tgz (keyless OIDC → Fulcio/Rekor)',
  gpg_prov:
    'Helm-native GPG .prov — optional local signing for the Enterprise offline bundle; separate from Business package signing',
  verify_command:
    'cosign verify-blob --bundle olivares-<chart-version>.tgz.sigstore.json --certificate-identity <trusted-chart-publisher-identity> --certificate-oidc-issuer https://token.actions.githubusercontent.com olivares-<chart-version>.tgz (use the trusted identity supplied by the Business artifact channel)',
  status: 'declared',
  scp: 'SCP-05',
}

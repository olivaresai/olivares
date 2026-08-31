// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the supply-chain attestation view. Two provenance classes since:
//
//  • MEASURED — RunningBinaryAttestation mirrors GET /v1/m/observability/attestation
//    (modules/observability attestation.go) 1:1: what the RUNNING process can prove
//    about itself, plus the measured release state (published / not_published, with
//    the reason that decided it) and the never-fabricated not_verified signature.
//  • DECLARED — the release-verification contract types below mirror
//    docs/RELEASE-VERIFICATION.md, SECURITY.md, .goreleaser.yaml and the
//    release/scorecard/patch-velocity workflows 1:1. The project is BETA (no
//    releases exist yet; the pipeline is written but not activated) and the engine
//    cannot observe repository/CI state, so this half stays declared reference.
//
// The view PRESENTS both; it NEVER re-verifies cryptographically in the browser
// (ARCHITECTURE.md — the web presents, never recomputes — never fabricate a
// value the backend does not give). All open enums are widened with `| string` so a
// future value never breaks rendering. No field carries a signature payload —
// fingerprints/coordinates only.

// --- MEASURED: the running binary (GET /v1/m/observability/attestation) --------

/** crypto/fips140 mode of the running process. `version` present only when the
 *  FIPS 140-3 module is active. */
export interface Fips140State {
  enabled: boolean
  version?: string
}

/** The main module per debug.ReadBuildInfo — "(devel)" under a go.work build. */
export interface MainModuleRef {
  path: string
  version: string
}

/** Dependency-sum coverage: how many external deps carry go.sum hashes. The note
 *  carries the go.work caveat verbatim (workspace modules are (devel), no sums). */
export interface ModuleSumsState {
  external_deps: number
  sums_present: boolean
  note: string
}

/** Whether Go stamped vcs.* settings into the binary. ALWAYS unavailable under a
 *  go.work workspace build — the reason says so verbatim, never a silent false. */
export interface VcsStampState {
  available: boolean
  reason: string
}

/** Measured facts about the running binary. `version` is reported VERBATIM from
 *  ldflags ("dev" default); a "-dirty" suffix (git describe) is the only
 *  uncommitted-changes signal — no boolean clean/dirty claim is invented. */
export interface BinaryAttestation {
  version: string
  commit: string
  build_date: string
  go_version: string
  os: string
  arch: string
  fips140: Fips140State
  /** Stream-SHA256 of the running executable (64 hex), computed once. Absent when
   *  the executable could not be read — `self_hash_note` says why. */
  self_sha256?: string
  self_hash_note?: string
  main_module: MainModuleRef
  module_sums: ModuleSumsState
  vcs_stamp: VcsStampState
  /** "measured" — how these facts were obtained. */
  status: string
}

/** The native verifier never claims Rekor inclusion (core/secure/modelsign) —
 *  `verified` is false until a real bundle is verified out-of-band. */
export interface TransparencyLogState {
  verified: boolean
  note: string
}

/** Epistemic class of the measured release verdict (modules/observability
 *  provenanceDTO). `kind` is "self_declared" and `attested` is false on every
 *  response, and that constancy is deliberate: unlike `published` — whose old
 *  constancy WAS the defect, because the binary can read its own link-time stamps
 *  — nothing outside the linker is reachable from a running process. */
export interface ReleaseProvenance {
  kind: string
  attested: boolean
  note: string
}

/** MEASURED release state of THIS binary. The backend derives it from the two
 *  link-time facts only the release ceremony sets together — an orderable
 *  main.version stamp and the embedded OTA anchor (modules/observability
 *  attestation.go releaseIdentity) — so both polarities are reachable. Until
 *  2026-08-13 the backend emitted not_published for every build ever made, which
 *  is why `published: true` had never been rendered by anything but a fixture.
 *  `verifier_available` is compile-time-proven (the module references
 *  core/secure/modelsign.VerifyAttestation). */
export interface ReleaseState {
  published: boolean
  /** "not_published" for every build that is not a release — still the answer for
   *  every source build — or "published" once both link-time facts are present. */
  status: string
  /** Names WHICH fact decided the verdict (no tag stamp, an unorderable stamp, a
   *  git-describe marker, a missing OTA anchor, or both facts present). */
  reason: string
  /** The CLASS of evidence `published` rests on, sent in both polarities. Both
   *  link-time facts are values chosen by whoever linked the binary, so the
   *  positive verdict is release-SHAPED, never proof that a release was published
   *  (measured 2026-08-14: two -ldflags values reach it). The panel renders
   *  `note` verbatim next to the badge — this is the one field that stops the
   *  page from claiming more than the engine can support. */
  provenance: ReleaseProvenance
  /** "not_verified" in BOTH states, and it does NOT flip with `published`: the
   *  detached signature is not inside the binary, so a running process can never
   *  verify its own. Never a fabricated verification verdict. */
  signature_status: string
  signature_reason: string
  verifier_available: boolean
  transparency_log: TransparencyLogState
}

/** The release/posture workflows that exist in the source tree. Status "declared":
 *  the running process cannot observe repository or CI state. */
export interface PipelineState {
  workflows: string[]
  status: string
  note: string
}

/** GET /v1/m/observability/attestation (LIVE, modules/observability). */
export interface RunningBinaryAttestation {
  binary: BinaryAttestation
  release: ReleaseState
  pipeline: PipelineState
  /** RFC3339 instant the measurement was taken. */
  captured_at: string
}

// --- DECLARED: the release-verification contract --------------------------------

/** Whether a declared verification step is expected to pass, be skipped when its
 *  attestation/tool is absent, or is simply not-yet-run (beta: nothing has run yet).
 *  `verify-release.sh` skips-with-a-note when an attestation or its verifier is absent
 *  (docs/RELEASE-VERIFICATION.md). NOT a live cryptographic result. */
export type VerificationStatus =
  | 'declared' // contract says this ships + verifies; not yet exercised (beta)
  | 'skip_when_absent' // verifier skips with a clear note if the attestation/tool is missing
  | 'not_run' // no release exists yet, so nothing has been produced/verified
  | string

/** Trust mechanism for an artifact's signature. Both are first-class (neither
 *  canonical): keyless = Fulcio cert + Rekor transparency log; key-based = cosign.pub
 *  for air-gap where Rekor is unreachable. */
export type TrustMechanism =
  | 'keyless' // Sigstore: Fulcio short-lived cert + Rekor transparency log
  | 'key_based' // cosign.pub (air-gap, no Rekor/Fulcio)
  | 'both'
  | 'transitive' // covered by the signed checksums.txt, not signed directly
  | string

/** One row of the "what ships with a release" table (docs/RELEASE-VERIFICATION.md). */
export interface ReleaseArtifact {
  /** Stable id for the row (getRowId). */
  id: string
  /** Artifact filename / OCI coordinate (e.g. `checksums.txt`, image by digest). */
  name: string
  /** What produces it (goreleaser, syft, cosign, slsa-github-generator, helm…). */
  produced_by: string
  /** How its trust is established (signature/attestation kind, in prose). */
  signature_trust: string
  /** keyless / key-based / both / transitive. */
  trust_mechanism: TrustMechanism
  /** Declared verification status — NEVER a live browser re-verification. */
  status: VerificationStatus
  /** The SCP control this artifact belongs to (e.g. "SCP-03"), if any. */
  scp?: string
}

/** SLSA Build provenance contract (SCP-01). */
export interface SlsaProvenance {
  /** Build level achieved by construction (L3 via the reusable generator). */
  build_level: 'L1' | 'L2' | 'L3' | string
  /** Why L3 holds: signing runs in an isolated context the build steps can't reach. */
  by_construction: boolean
  /** Predicate / format identifiers (in-toto + ITE-6 SLSA Provenance v1). */
  predicate_type: string
  /** The generator that emits the provenance. */
  generator: string
  /** How the reusable workflow is pinned. HONESTY: semver TAG, NOT a SHA — a SHA pin
   *  would break slsa-verifier's builder-ID check (docs/RELEASE-VERIFICATION.md). */
  reusable_workflow_pin: 'semver_tag' | 'sha' | string
  /** The verify command an operator runs (slsa-verifier). */
  verify_command: string
  status: VerificationStatus
  scp: string
}

/** A CISA-2025 minimum-element. DRAFT (pre-decisional, public-comment) — NOT law. */
export interface SbomCisaElement {
  /** The element name (Timestamp, Tool Name, Author, Component Name, Version,
   *  Producer, Hash, License, Generation Context). */
  key: string
  /** Hard = the linter fails when the field TYPE is wholly absent; soft = it only
   *  warns on the per-component omissions the draft itself permits. */
  enforcement: 'hard' | 'soft' | string
  /** Which SBOM format natively carries this (SPDX 2.3 / CycloneDX 1.6 / both). */
  carried_by: string
}

/** SBOM contract (SCP-03): SPDX 2.3 + CycloneDX 1.6, attested in-toto. */
export interface SbomContract {
  /** SBOM document formats produced per archive + for the image. */
  formats: { name: string; version: string; note?: string }[]
  /** in-toto attestation predicate type for the SBOM (cosign --type spdxjson). */
  attestation_predicate: string
  /** The linter that checks the CISA draft minimum elements. */
  linter: string
  /** The DRAFT minimum-elements checklist with hard/soft enforcement. */
  cisa_elements: SbomCisaElement[]
  status: VerificationStatus
  scp: string
}

/** OpenVEX status enum (the four canonical statuses). */
export type VexStatus =
  'not_affected' | 'affected' | 'fixed' | 'under_investigation' | string

/** One declared OpenVEX statement (SCP-04). */
export interface VexStatement {
  id: string
  /** Example vulnerability id (illustrative, declared — not a live finding). */
  vuln_id: string
  status: VexStatus
  /** Justification, present when status = not_affected (machine-readable enum). */
  justification?: string
  /** The statement author. */
  author: string
  status_label?: string
}

/** OpenVEX contract envelope (SCP-04) — reachability-driven via govulncheck. */
export interface VexContract {
  /** What drives the statements (Go call-graph reachability). */
  driver: string
  /** The attestation predicate type (cosign --type openvex). */
  attestation_predicate: string
  statements: VexStatement[]
  status: VerificationStatus
  scp: string
}

/** One OpenSSF Scorecard check (SCP-07). */
export interface ScorecardCheck {
  /** Check id (Pinned-Dependencies, Signed-Releases, Token-Permissions,
   *  Branch-Protection). */
  name: string
  /** What the check evaluates, in prose. */
  what: string
  /** Where the evidence lives (workflow file, dependabot.yml, repo settings). */
  evidence: string
}

/** OpenSSF Scorecard contract (SCP-07). Lives on the PUBLIC GitHub mirror; the
 *  score is published to api.scorecard.dev — EXTERNAL, not engine data. */
export interface ScorecardContract {
  checks: ScorecardCheck[]
  /** Cron schedule, in prose (weekly Monday 06:00 UTC). */
  schedule: string
  /** The honest note: external, not engine telemetry. */
  external_note: string
  status: VerificationStatus
  scp: string
}

/** One row of the CVE-remediation SLA table (SECURITY.md). */
export interface RemediationSla {
  /** Severity band label. */
  severity: 'critical' | 'high' | 'medium' | 'low' | string
  /** CVSS v3.1 score range, in prose. */
  cvss_range: string
  /** Target to a patched, signed release, in prose. */
  target: string
}

/** Patch-velocity cadence contract (SCP-11). */
export interface PatchVelocity {
  /** Cron schedule, in prose (weekly Tuesday 06:00 UTC rebuild). */
  schedule: string
  /** The steps the scheduled job runs, in order. */
  steps: string[]
  /** KEV escalation rule. */
  kev_rule: string
  sla: RemediationSla[]
  status: VerificationStatus
  scp: string
}

/** Air-gap bundle composition (SCP): the bundle is verifiable offline and its path calls
 *  nothing out. NOT a claim about the product — `olivares upgrade` does reach us. */
export interface AirgapContract {
  /** What the bundle carries. */
  composition: string[]
  /** The build-side command (online once). */
  build_command: string
  /** The air-gap-side mirror command (no internet). */
  mirror_command: string
  /** The honesty note: no mandatory outbound calls at boot. */
  no_phone_home: string
  status: VerificationStatus
}

/** OCI Helm chart contract (SCP-05) — dual-signing. */
export interface HelmChartContract {
  /** The OCI coordinate the chart is published to. */
  oci_coordinate: string
  /** cosign signature over the OCI manifest, by digest (keyless OIDC in CI). */
  cosign_manifest: string
  /** The Helm-native GPG `.prov` provenance (maintainer local-signing path). */
  gpg_prov: string
  /** The verify command consumers run. */
  verify_command: string
  status: VerificationStatus
  scp: string
}

/** The declared-contract envelope the FIXTURES assemble for the pure-component
 *  tests (the view consumes the attestation.data.ts constants directly since).
 *  BETA: an ILLUSTRATIVE example, not a published release (none exist yet). */
export interface ReleaseAttestation {
  /** Example version label (illustrative). */
  version: string
  /** The release pipeline that would produce it (written, not activated). */
  pipeline: string
  /** Whether the release is a draft (the pipeline builds into a DRAFT release). */
  draft: boolean
  /** The OIDC issuer keyless signing relies on (GitHub Actions). */
  oidc_issuer: string
  /** Certificate identity regexp cosign verifies against. */
  identity_regexp: string
  artifacts: ReleaseArtifact[]
  slsa: SlsaProvenance
  sbom: SbomContract
  vex: VexContract
  scorecard: ScorecardContract
  patch_velocity: PatchVelocity
  airgap: AirgapContract
  helm: HelmChartContract
}

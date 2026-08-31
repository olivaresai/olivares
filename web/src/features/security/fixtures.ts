// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic Security fixtures shaped exactly like the contract — used by the
// component tests and the visual e2e route mocks.
// They encode the honesty invariants the view must hold: a `detail_hash` is a
// FINGERPRINT (never a payload / secret); the plane is DETECTIVE by default (verdicts
// are `allow`/`flag` unless enforcement is governed); an `approximate` anomaly is
// unreconciled drift (never titled a firm violation); a checkpoint key that is not
// wired reads as "unavailable", never a failure. No fixture is ever a real secret.

import type {
  Anomaly,
  CaseIntegrity,
  CaseTimeline,
  Detection,
  EnforcementEntry,
  Finding,
  ForensicCase,
  InspectResult,
  IntegrityVerify,
  TimelineEntry,
} from './types'

// SHA-256-shaped fingerprints (64 hex chars). Inert digests — NOT payloads.
const HASH_A =
  '9f1c2b7d4e6a8c0f3b5d7e9a1c2b4d6e8f0a1c3b5d7e9f1a3c5b7d9e0f2a4c6b'
const HASH_B =
  '3a7e9c1d5f0b2a4c6e8d0f2b4a6c8e0d1f3b5a7c9e1d3f5b7a9c0e2d4f6b8a0c'
const HASH_C =
  'c0ffee11d34db33f5e7a9c1b3d5f7092a4c6e8d0f2b4a6c8e0d1f3b5a7c9e1d3'
const HASH_D =
  'beadfeed00112233445566778899aabbccddeeff00112233445566778899aabb'

// --- 1. Findings -------------------------------------------------------------

export const findingsFixture: Finding[] = [
  {
    id: 'fnd-1001',
    kind: 'guardrail',
    severity: 'critical',
    status: 'open',
    source: 'prompt_injection',
    subject_kind: 'session',
    subject_ref: 'sess-7f3a2b',
    title: 'Indirect prompt injection in retrieved document',
    detail_hash: HASH_A,
    occurred_at: '2026-06-03T14:22:10Z',
    metadata: { surface: 'input', owasp: 'LLM01:2025' },
  },
  {
    id: 'fnd-1002',
    kind: 'guardrail',
    severity: 'high',
    status: 'triaged',
    source: 'pii',
    subject_kind: 'agent',
    subject_ref: 'agt-billing',
    title: 'PII detected in tool arguments (redacted)',
    detail_hash: HASH_B,
    occurred_at: '2026-06-03T11:05:44Z',
    metadata: { surface: 'tool_args' },
  },
  {
    id: 'fnd-1003',
    kind: 'anomaly',
    severity: 'medium',
    status: 'open',
    source: 'anti_evasion_correlated',
    subject_kind: 'resource',
    subject_ref: 'res-export-svc',
    title: 'Correlated kernel + watchdog evasion signal',
    detail_hash: HASH_C,
    occurred_at: '2026-06-02T23:48:01Z',
    metadata: { signal_source: 'ebpf+watchdog' },
  },
  {
    id: 'fnd-1004',
    kind: 'forensic',
    severity: 'low',
    status: 'resolved',
    source: 'output_validation',
    subject_kind: 'session',
    subject_ref: 'sess-2c9d10',
    title: 'Output schema deviation (auto-corrected)',
    detail_hash: HASH_D,
    occurred_at: '2026-05-30T08:14:30Z',
  },
]

// --- 2. Guardrail inspect ----------------------------------------------------

const detectionsFixture: Detection[] = [
  {
    class: 'prompt_injection',
    rule: 'pi.indirect.instruction_override',
    severity: 'high',
    title: 'Attempt to override system instructions',
    excerpt: '[redacted: instruction-override pattern]',
    owasp: 'LLM01:2025',
    atlas: 'AML.T0051',
    enforced: true,
  },
  {
    class: 'pii',
    rule: 'pii.email',
    severity: 'medium',
    title: 'Email address present',
    excerpt: '[redacted: email]',
    owasp: 'LLM06:2025',
    enforced: false,
  },
]

/** Detective posture: a trip is detected and recorded, but the verdict is `flag`
 *  (not `block`) because enforcement is NOT governed-enabled for this class. */
export const inspectFlagFixture: InspectResult = {
  verdict: 'flag',
  detections: detectionsFixture,
  finding_ids: ['fnd-1001', 'fnd-1002'],
  enforcement: 'detective',
}

/** Enforced posture: the same trip blocks, because governance enabled the class. */
export const inspectBlockFixture: InspectResult = {
  verdict: 'block',
  detections: [detectionsFixture[0]],
  finding_ids: ['fnd-1101'],
  enforcement: 'enforced',
}

/** A clean inspection — no detections, allow. */
export const inspectAllowFixture: InspectResult = {
  verdict: 'allow',
  detections: [],
  finding_ids: [],
  enforcement: 'detective',
}

// --- 3. Enforcement posture --------------------------------------------------

/** A realistic mixed posture: most classes detective; one governed-enabled; one
 *  enabled WITHOUT governance (the honest seam — Gate not wired).*/
export const enforcementFixture: EnforcementEntry[] = [
  {
    class: 'prompt_injection',
    enabled: true,
    min_severity: 'high',
    governed: true,
    set_by: 'governance:change-2087',
    updated_at: '2026-05-28T09:00:00Z',
  },
  {
    class: 'pii',
    enabled: true,
    min_severity: 'medium',
    governed: false,
    set_by: 'admin:fran',
    updated_at: '2026-06-01T16:30:00Z',
  },
  {
    class: 'jailbreak',
    enabled: false,
    min_severity: 'high',
    governed: false,
  },
  {
    class: 'content',
    enabled: false,
    min_severity: 'high',
    governed: false,
  },
]

/** The safe default the contract calls out: an empty list = fully detective. */
export const enforcementEmptyFixture: EnforcementEntry[] = []

// --- 4. Anomalies ------------------------------------------------------------

/** Ordered by `priority` desc, as the backend returns them. Includes an
 *  `approximate` (unreconciled drift) entry that must NOT read as a firm violation. */
export const anomaliesFixture: Anomaly[] = [
  {
    kind: 'egress_exfil_suspected',
    severity: 'critical',
    priority: 94,
    subject_kind: 'agent',
    subject_ref: 'agt-research',
    title: 'Suspected egress to an external endpoint',
    confidence: 'attributed',
    source: 'egress_monitor',
    occurred_at: '2026-06-03T20:11:42Z',
    evidence: {
      origin: 'agt-research',
      resource: 'api.external.example',
      mode: 'write',
    },
  },
  {
    kind: 'anti_evasion_correlated',
    severity: 'high',
    priority: 81,
    subject_kind: 'resource',
    subject_ref: 'res-export-svc',
    title: 'Correlated evasion signal (kernel + watchdog)',
    confidence: 'attributed',
    source: 'correlator',
    occurred_at: '2026-06-02T23:48:01Z',
    evidence: { signal_source: 'ebpf+watchdog', window_ms: 1200 },
  },
  {
    kind: 'access_drift',
    severity: 'medium',
    priority: 47,
    subject_kind: 'identity',
    subject_ref: 'idn-svc-loader',
    title: 'Observed access not present in the policy',
    // approximate = unreconciled drift — discounted, never a firm violation.
    confidence: 'approximate',
    source: 'access_map',
    occurred_at: '2026-06-01T07:33:19Z',
    evidence: {
      origin: 'idn-svc-loader',
      resource: 'kb:invoices',
      mode: 'read',
      reconciled: false,
    },
  },
]

// --- 5. Forensic cases + timeline --------------------------------------------

export const casesFixture: ForensicCase[] = [
  {
    id: 'case-501',
    title: 'Suspected exfiltration via research agent',
    status: 'investigating',
    severity: 'critical',
    subject_kind: 'agent',
    subject_ref: 'agt-research',
    summary: 'Egress anomaly correlated with an off-hours retrieval burst.',
    opened_by: 'admin:fran',
    integrity_ok: true,
    attested_seq: 8841,
    opened_at: '2026-06-03T21:00:00Z',
  },
  {
    id: 'case-498',
    title: 'PII handling review — billing agent',
    status: 'contained',
    severity: 'high',
    subject_kind: 'agent',
    subject_ref: 'agt-billing',
    summary:
      'PII surfaced in tool arguments; redaction confirmed at the boundary.',
    opened_by: 'analyst:mara',
    integrity_ok: true,
    attested_seq: 8120,
    opened_at: '2026-05-29T13:40:00Z',
  },
  {
    id: 'case-477',
    title: 'Output schema deviation (closed)',
    status: 'closed',
    severity: 'low',
    subject_kind: 'session',
    subject_ref: 'sess-2c9d10',
    summary: 'Auto-corrected output deviation, no further action.',
    opened_by: 'analyst:mara',
    integrity_ok: true,
    attested_seq: 7702,
    opened_at: '2026-05-30T08:20:00Z',
    closed_at: '2026-05-31T10:05:00Z',
  },
]

const timelineEventsFixture: TimelineEntry[] = [
  {
    seq: 8838,
    occurred_at: '2026-06-03T20:11:42Z',
    actor: 'egress_monitor',
    actor_kind: 'detector',
    action: 'anomaly_raised',
    target_kind: 'agent',
    target_id: 'agt-research',
    hash: HASH_A,
    prev_hash:
      '0000000000000000000000000000000000000000000000000000000000000000',
    signed: true,
    linked: true,
  },
  {
    seq: 8839,
    occurred_at: '2026-06-03T20:58:03Z',
    actor: 'admin:fran',
    actor_kind: 'user',
    action: 'case_opened',
    target_kind: 'case',
    target_id: 'case-501',
    hash: HASH_B,
    prev_hash: HASH_A,
    signed: true,
    linked: true,
  },
  {
    seq: 8840,
    occurred_at: '2026-06-03T21:14:55Z',
    actor: 'admin:fran',
    actor_kind: 'user',
    action: 'finding_linked',
    target_kind: 'finding',
    target_id: 'fnd-1001',
    hash: HASH_C,
    prev_hash: HASH_B,
    signed: false,
    linked: true,
  },
  {
    seq: 8841,
    occurred_at: '2026-06-03T22:02:30Z',
    actor: 'analyst:mara',
    actor_kind: 'user',
    action: 'note_added',
    target_kind: 'case',
    target_id: 'case-501',
    hash: HASH_D,
    prev_hash: HASH_C,
    signed: false,
    linked: false,
  },
]

/** Integrity panel that pairs with the timeline. checkpoints NOT verified here —
 *  signing key is not wired, so the panel reads "unavailable", NOT a failure. */
export const caseIntegrityUnavailableFixture: CaseIntegrity = {
  chain_ok: true,
  checkpoints_verified: false,
  checkpoints_ok: false,
  attested_seq: 8841,
  head_seq: 8841,
}

export const caseTimelineFixture: CaseTimeline = {
  case: casesFixture[0],
  integrity: caseIntegrityUnavailableFixture,
  events: timelineEventsFixture,
}

// --- 6. Integrity verify -----------------------------------------------------

/** Healthy chain, but checkpoints are NOT verified (no signing key wired). The view
 *  must render this calmly as "unavailable", never as a red failure (docs/SECURITY-HARDENING.md).*/
export const integrityUnavailableFixture: IntegrityVerify = {
  chain_ok: true,
  chain_checked: 8841,
  checkpoints_verified: false,
  checkpoints_ok: false,
  checkpoints: 0,
  attested_seq: 8841,
  head_seq: 8841,
}

/** Fully verified chain + signed checkpoints — the strongest forensic guarantee. */
export const integrityVerifiedFixture: IntegrityVerify = {
  chain_ok: true,
  chain_checked: 8841,
  checkpoints_verified: true,
  checkpoints_ok: true,
  checkpoints: 18,
  checkpoint_status: 'ok',
  attested_seq: 8820,
  head_seq: 8841,
}

/** A freshly installed engine: the key IS wired, the chain verifies, and nothing
 *  has been attested yet because the checkpoint scheduler has not fired. Measured
 *  against a clean install — the engine sends checkpoints_ok=FALSE here, exactly
 *  as it does for a tampered ledger, which is why the panel must read
 *  `checkpoint_status` and not the boolean. This must render CALM. */
export const integrityPendingFixture: IntegrityVerify = {
  chain_ok: true,
  chain_checked: 2,
  checkpoints_verified: true,
  checkpoints_ok: false,
  checkpoints: 0,
  checkpoint_reason: 'no-checkpoints',
  checkpoint_status: 'pending',
  attested_seq: 0,
  head_seq: 2,
}

/** A detected tamper — the chain broke at a seq. This is the loud failure case. */
/**
 * ⛔ EL LEDGER VACÍO: `Verify` (`core/internal/store/sqlstore/audit.go:623-629`) deja `OK: false`
 *    A PROPÓSITO cuando `Checked == 0` — «An empty range proves nothing … must not be able to turn
 *    an absent ledger into "verified" evidence through vacuous truth». El motor acierta al decir
 *    `false`; pintarlo como FALLO es lo que no vale. Es el estado de una instalación recién
 *    levantada, y los checkpoints SÍ están sanos aquí para que la casilla hable sólo de la cadena.
 */
export const integrityEmptyFixture: IntegrityVerify = {
  chain_ok: false,
  chain_checked: 0,
  chain_reason: 'no-events',
  checkpoints_verified: true,
  checkpoints_ok: true,
  checkpoints: 0,
  checkpoint_status: 'pending',
  attested_seq: 0,
  head_seq: 0,
}

export const integrityBrokenFixture: IntegrityVerify = {
  chain_ok: false,
  chain_checked: 8400,
  chain_break_at: 8399,
  chain_reason: 'hash mismatch at seq 8399 (prev_hash does not match)',
  checkpoints_verified: true,
  checkpoints_ok: false,
  checkpoints: 17,
  checkpoint_break_at: 8350,
  checkpoint_reason: 'signature verification failed for checkpoint at seq 8350',
  checkpoint_status: 'failed',
  attested_seq: 8349,
  head_seq: 8841,
}

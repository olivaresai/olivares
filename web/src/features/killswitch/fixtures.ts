// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic kill-switch fixtures shaped exactly like the DTOs
// (modules/governance/{killswitch,guardian}.go). They encode the lifecycle the
// tests assert: an active operator estate stop, an active guardian agent stop, a
// re-enabled-but-unreviewed stop (post-review due), a reviewed (closed) one, and
// the 202 pending_approval envelope of a re-enable mid-quorum. No field ever
// carries a secret or an email — actors are audit handles.
import type {
  EvidencePack,
  GuardianActionDTO,
  GuardianRuleDTO,
  KillSwitchDTO,
  KillSwitchStateDTO,
  ReenablePendingDTO,
} from './types'

export const estateStopFixture: KillSwitchDTO = {
  id: 'ks-estate-1',
  scope_kind: 'estate',
  status: 'active',
  reason: 'Prompt-injection cascade across the fleet',
  source: 'operator',
  engaged_by: 'user:u-1',
  engaged_aal: 3,
  engaged_at: '2026-06-11T09:00:00Z',
  engage_audit_seq: 4100,
  revoked_approvals: 3,
  reviewed: false,
}

export const agentStopFixture: KillSwitchDTO = {
  id: 'ks-agent-1',
  scope_kind: 'agent',
  scope_ref: 'support-triage',
  agent_id: '4f6a2c1e-0000-4000-8000-000000000001',
  agent_external_id: 'support-triage',
  status: 'active',
  reason: "guardian rule 'contain-exfil': data_exfil_attempt (critical)",
  source: 'guardian',
  rule_ref: 'gr-1',
  engaged_by: 'system',
  engaged_aal: 0,
  engaged_at: '2026-06-11T10:30:00Z',
  engage_audit_seq: 4180,
  revoked_approvals: 1,
  reviewed: false,
}

export const reenabledUnreviewedFixture: KillSwitchDTO = {
  ...estateStopFixture,
  id: 'ks-reen-1',
  status: 'reenabled',
  reenable_approval: 'apr-77',
  reenabled_by: 'user:u-2',
  reenabled_at: '2026-06-11T12:00:00Z',
  reenable_audit_seq: 4300,
  reviewed: false,
}

export const reviewedStopFixture: KillSwitchDTO = {
  ...reenabledUnreviewedFixture,
  id: 'ks-rev-1',
  reviewed: true,
  reviewed_by: 'user:u-3',
  reviewed_at: '2026-06-11T15:00:00Z',
  review_note: 'Stop justified; agent prompt hardened and rule tightened.',
}

export const estateStoppedStateFixture: KillSwitchStateDTO = {
  estate_stopped: true,
  active: [estateStopFixture],
}

export const calmStateFixture: KillSwitchStateDTO = {
  estate_stopped: false,
  active: [],
}

/** 202 envelope mid-quorum: one of the two required humans has approved. */
export const reenablePendingFixture: ReenablePendingDTO = {
  status: 'pending_approval',
  approval: {
    id: 'apr-77',
    subject_kind: 'killswitch',
    subject_ref: 'ks-estate-1',
    action: 'security.killswitch.reenable',
    requested_by: 'user:u-1',
    status: 'pending',
    required_approvals: 2,
    approve_count: 1,
    reject_count: 0,
    reason: 're-enable estate kill switch ks-estate-1',
    escalated: false,
  },
  stop: { ...estateStopFixture, reenable_approval: 'apr-77' },
}

export const guardianRulesFixture: GuardianRuleDTO[] = [
  {
    id: 'gr-1',
    name: 'contain-exfil',
    enabled: true,
    match_kinds: 'data_exfil_attempt,credential_leak',
    min_severity: 'high',
    action: 'stop_agent',
    mode: 'auto',
    created_by: 'user:u-1',
    note: 'Stop the agent on confirmed exfil findings.',
  },
  {
    id: 'gr-2',
    name: 'estate-circuit-breaker',
    enabled: false,
    min_severity: 'critical',
    action: 'stop_estate',
    mode: 'approval',
    created_by: 'user:u-1',
  },
]

export const guardianActionsFixture: GuardianActionDTO[] = [
  {
    id: 'ga-1',
    rule_id: 'gr-1',
    rule_name: 'contain-exfil',
    finding_kind: 'data_exfil_attempt',
    finding_ref: 'a1b2c3',
    finding_severity: 'critical',
    target_kind: 'agent',
    target_ref: 'support-triage',
    action: 'stop_agent',
    mode: 'auto',
    status: 'executed',
    killswitch_id: 'ks-agent-1',
    detail: 'kill switch engaged',
    executed_at: '2026-06-11T10:30:00Z',
  },
  {
    id: 'ga-2',
    rule_id: 'gr-2',
    rule_name: 'estate-circuit-breaker',
    finding_kind: 'anomalous_spend',
    finding_ref: 'd4e5f6',
    finding_severity: 'critical',
    target_kind: 'estate',
    action: 'stop_estate',
    mode: 'approval',
    status: 'pending',
    approval_id: 'apr-90',
  },
]

export const evidencePackFixture: EvidencePack = {
  generated_at: '2026-06-11T16:00:00Z',
  tenant: 't1',
  killswitch: reviewedStopFixture,
  reenable_approval: reenablePendingFixture.approval,
  reenable_decisions: [
    {
      decision: 'approve',
      decider: 'user:u-2',
      decided_at: '2026-06-11T11:50:00Z',
    },
    {
      decision: 'approve',
      decider: 'user:u-3',
      decided_at: '2026-06-11T11:55:00Z',
    },
  ],
  timeline: [],
  findings: [],
  rollback: { deploy_operations_in_window: [], non_reversible_domains: [] },
  integrity: { anchor_seq: 4100, chain_verified: true, canonical_meta: true },
  pack_sha256:
    '6f1ed002ab5595859014ebf0951522d9a6f1ed002ab5595859014ebf0951522d',
}

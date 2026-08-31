// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic red-team fixtures shaped exactly like the security UI data contract
// — used by the component tests and the visual e2e route mocks. They carry NO
// attack payloads (the API never does): only taxonomy metadata, consent state,
// scorecards and per-probe outcomes with FINGERPRINT hashes. The three runs cover the
// honesty cases the test asserts: a healthy `completed` run, a `degraded` run (all
// probes skipped — NO sandbox: never a pass), and an `error` run.
import type { CatalogResponse, ProbeResult, Run, Target } from './types'

export const catalogFixture: CatalogResponse = {
  total: 9,
  families: {
    injection: 3,
    jailbreak: 2,
    exfil: 2,
    tool_poisoning: 2,
  },
  owasp_covered: {
    'LLM01:2025': 3,
    'LLM02:2025': 2,
    'LLM06:2025': 2,
    ASI01: 2,
  },
  atlas_covered: {
    'AML.T0051': 2,
    'AML.T0054': 2,
    'AML.T0057': 1,
  },
  probes: [
    {
      id: 'inj-001',
      family: 'injection',
      title: 'Direct instruction override',
      owasp: 'LLM01:2025',
      atlas: 'AML.T0051',
      severity: 'high',
      surface: 'input',
    },
    {
      id: 'inj-002',
      family: 'injection',
      title: 'Indirect prompt injection via retrieved content',
      owasp: 'LLM01:2025',
      atlas: 'AML.T0051',
      severity: 'high',
      surface: 'tool_args',
    },
    {
      id: 'inj-003',
      family: 'injection',
      title: 'System-prompt disclosure attempt',
      owasp: 'LLM01:2025',
      atlas: 'AML.T0054',
      severity: 'medium',
      surface: 'input',
    },
    {
      id: 'jb-001',
      family: 'jailbreak',
      title: 'Role-play guardrail bypass',
      owasp: 'ASI01',
      atlas: 'AML.T0054',
      severity: 'high',
      surface: 'input',
    },
    {
      id: 'jb-002',
      family: 'jailbreak',
      title: 'Multi-turn refusal erosion',
      owasp: 'ASI01',
      severity: 'medium',
      surface: 'input',
    },
    {
      id: 'exf-001',
      family: 'exfil',
      title: 'Secret-pattern egress probe',
      owasp: 'LLM06:2025',
      atlas: 'AML.T0057',
      severity: 'critical',
      surface: 'output',
    },
    {
      id: 'exf-002',
      family: 'exfil',
      title: 'PII echo on adversarial recall',
      owasp: 'LLM06:2025',
      severity: 'high',
      surface: 'output',
    },
    {
      id: 'tp-001',
      family: 'tool_poisoning',
      title: 'Malicious tool-result coercion',
      owasp: 'LLM02:2025',
      severity: 'high',
      surface: 'tool_args',
    },
    {
      id: 'tp-002',
      family: 'tool_poisoning',
      title: 'Untrusted tool schema injection',
      owasp: 'LLM02:2025',
      severity: 'medium',
      surface: 'tool_args',
    },
  ],
}

export const targetsFixture: Target[] = [
  // Authorized: consent granted — Launch is enabled against this one only.
  {
    id: 'tgt-support',
    agent_ref: 'agent-support-triage',
    name: 'Support triage bot',
    endpoint: 'https://agents.internal/support-triage',
    scope: 'input,output,tool_args',
    authorized: true,
    authorized_by: 'security-lead@acme.test',
    authorized_at: '2026-06-01T10:00:00Z',
    status: 'authorized',
    created_by: 'security-lead@acme.test',
  },
  // Registered: a candidate WITHOUT consent — Launch must be disabled here.
  {
    id: 'tgt-billing',
    agent_ref: 'agent-billing-assistant',
    name: 'Billing assistant',
    endpoint: 'https://agents.internal/billing',
    scope: '',
    authorized: false,
    authorized_by: '',
    authorized_at: '',
    status: 'registered',
    created_by: 'security-lead@acme.test',
  },
  // Revoked: consent withdrawn — also no run.
  {
    id: 'tgt-legacy',
    agent_ref: 'agent-legacy-faq',
    name: 'Legacy FAQ bot',
    endpoint: 'https://agents.internal/legacy-faq',
    scope: 'input',
    authorized: false,
    authorized_by: 'security-lead@acme.test',
    authorized_at: '2026-05-20T09:00:00Z',
    status: 'revoked',
    created_by: 'platform@acme.test',
  },
]

// A healthy completed run: score = passed/(passed+failed)·100 = 7/(7+2)·100 ≈ 78.
export const completedRunFixture: Run = {
  id: 'run-completed',
  target_ref: 'agent-support-triage',
  suite: 'all',
  status: 'completed',
  total: 9,
  passed: 7,
  failed: 2,
  errors: 0,
  skipped: 0,
  score: 78,
  started_at: '2026-06-03T08:00:00Z',
  finished_at: '2026-06-03T08:04:12Z',
  launched_by: 'security-lead@acme.test',
  by_family: {
    injection: { Total: 3, Passed: 3, Failed: 0, Errors: 0, Skipped: 0 },
    jailbreak: { Total: 2, Passed: 2, Failed: 0, Errors: 0, Skipped: 0 },
    exfil: { Total: 2, Passed: 1, Failed: 1, Errors: 0, Skipped: 0 },
    tool_poisoning: { Total: 2, Passed: 1, Failed: 1, Errors: 0, Skipped: 0 },
  },
  owasp_failures: {
    'LLM06:2025': 1,
    'LLM02:2025': 1,
  },
}

// A degraded run: every probe skipped (no sandbox). score=0 here is NOT a fail
// grade — the UI must surface it as "pending sandbox", never green and never a pass.
export const degradedRunFixture: Run = {
  id: 'run-degraded',
  target_ref: 'agent-support-triage',
  suite: 'injection',
  status: 'degraded',
  total: 3,
  passed: 0,
  failed: 0,
  errors: 0,
  skipped: 3,
  score: 0,
  started_at: '2026-06-02T14:00:00Z',
  finished_at: '2026-06-02T14:00:03Z',
  launched_by: 'security-lead@acme.test',
  by_family: {
    injection: { Total: 3, Passed: 0, Failed: 0, Errors: 0, Skipped: 3 },
  },
  owasp_failures: {},
}

// An error run: every probe failed by execution (not a vulnerability verdict).
export const errorRunFixture: Run = {
  id: 'run-error',
  target_ref: 'agent-support-triage',
  suite: 'exfil',
  status: 'error',
  total: 2,
  passed: 0,
  failed: 0,
  errors: 2,
  skipped: 0,
  score: 0,
  started_at: '2026-06-01T18:00:00Z',
  finished_at: '2026-06-01T18:00:09Z',
  launched_by: 'security-lead@acme.test',
  by_family: {
    exfil: { Total: 2, Passed: 0, Failed: 0, Errors: 2, Skipped: 0 },
  },
  owasp_failures: {},
}

export const runsFixture: Run[] = [
  completedRunFixture,
  degradedRunFixture,
  errorRunFixture,
]

// Per-probe results for the completed run, ordered by probe_id. detail_hash is a
// fingerprint (hex) — there is no payload behind it.
export const resultsFixture: ProbeResult[] = [
  {
    id: 'res-1',
    run_ref: 'run-completed',
    probe_id: 'exf-001',
    family: 'exfil',
    owasp: 'LLM06:2025',
    atlas: 'AML.T0057',
    outcome: 'leaked',
    severity: 'critical',
    detail_hash:
      'a3f1c9d27e0b4a6688fd1c0e2b7a4d59e8c1b0a2f3d4e5c6b7a8901234567abcd',
    occurred_at: '2026-06-03T08:01:10Z',
  },
  {
    id: 'res-2',
    run_ref: 'run-completed',
    probe_id: 'exf-002',
    family: 'exfil',
    owasp: 'LLM06:2025',
    outcome: 'refused',
    severity: 'high',
    detail_hash:
      'b1c2d3e4f5a60718293a4b5c6d7e8f90112233445566778899aabbccddeeff00',
    occurred_at: '2026-06-03T08:01:40Z',
  },
  {
    id: 'res-3',
    run_ref: 'run-completed',
    probe_id: 'inj-001',
    family: 'injection',
    owasp: 'LLM01:2025',
    atlas: 'AML.T0051',
    outcome: 'blocked',
    severity: 'high',
    detail_hash:
      'c0ffee0011223344556677889900aabbccddeeff00112233445566778899aabb',
    occurred_at: '2026-06-03T08:00:20Z',
  },
  {
    id: 'res-4',
    run_ref: 'run-completed',
    probe_id: 'tp-001',
    family: 'tool_poisoning',
    owasp: 'LLM02:2025',
    outcome: 'complied',
    severity: 'high',
    detail_hash:
      'd4d4d4e5e5e5f6f6f70707081818192929203030314141425252536363647474',
    occurred_at: '2026-06-03T08:03:05Z',
  },
  {
    id: 'res-5',
    run_ref: 'run-completed',
    probe_id: 'tp-002',
    family: 'tool_poisoning',
    owasp: 'LLM02:2025',
    outcome: 'refused',
    severity: 'medium',
    detail_hash:
      'e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4',
    occurred_at: '2026-06-03T08:03:30Z',
  },
]

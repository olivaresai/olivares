// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic orchestration fixtures shaped exactly like section A of the
// contract — used by the component tests and the visual e2e route mocks. The topology
// is a realistic Claude Code supervisor delegating to workers, plus MCP attachments.
// `tool_ref` is always a verb (Task / a tool name), NEVER arguments; no node carries a
// payload, prompt or transcript. The caveat text is the contract's verbatim wording.
import type {
  DecisionDTO,
  FlowDTO,
  GraphResponse,
  ScheduleDTO,
  TimelineItem,
} from './types'

/** The contract's verbatim coverage caveat — observed scope is narrow on purpose. */
export const COVERAGE_CAVEAT =
  'no a2a connector — only Claude Code Task delegation and MCP topology are observed; ' +
  'peer-to-peer A2A, swarm cross-talk and non-Task frameworks (LangGraph/CrewAI/AutoGen) ' +
  'are ABSENT, not zero.'

export const graphFixture: GraphResponse = {
  nodes: [
    {
      id: 'sess-orchestrator',
      kind: 'session',
      ref: 'sess-orchestrator',
      role: 'supervisor',
      schedule_status: 'active',
      health: 'ok',
    },
    {
      id: 'code-reviewer',
      kind: 'agent',
      ref: 'code-reviewer',
      role: 'worker',
      schedule_status: 'none',
      health: 'ok',
    },
    {
      id: 'tester',
      kind: 'agent',
      ref: 'tester',
      role: 'worker',
      schedule_status: 'none',
      health: 'ok',
    },
    {
      // The anti-evasion case: a worker whose own cadence was missed → alert style.
      id: 'nightly-batch',
      kind: 'agent',
      ref: 'nightly-batch',
      role: 'worker',
      schedule_status: 'missed',
      health: 'missed',
    },
    {
      id: 'mcp-filesystem',
      kind: 'mcp_server',
      ref: 'mcp-filesystem',
      role: 'peer',
      schedule_status: 'none',
      health: 'ok',
    },
    {
      id: 'mcp-tool-read_file',
      kind: 'mcp_tool',
      ref: 'read_file',
      role: 'peer',
      schedule_status: 'none',
      health: 'ok',
    },
  ],
  edges: [
    {
      id: 'edge-deleg-reviewer',
      source: 'sess-orchestrator',
      target: 'code-reviewer',
      link_kind: 'delegation',
      tool_ref: 'Task',
      mode: 'unknown',
      signal_source: 'otel',
      confidence: 'attributed',
      delegation_count: 18,
      first_seen_at: '2026-06-01T08:10:00Z',
      last_seen_at: '2026-06-04T11:42:00Z',
      animated: true,
    },
    {
      id: 'edge-deleg-tester',
      source: 'sess-orchestrator',
      target: 'tester',
      link_kind: 'delegation',
      tool_ref: 'Task',
      mode: 'unknown',
      signal_source: 'otel',
      confidence: 'attributed',
      delegation_count: 11,
      first_seen_at: '2026-06-01T09:02:00Z',
      last_seen_at: '2026-06-04T10:58:00Z',
      animated: true,
    },
    {
      // Approximate confidence → dashed stroke; certainty is never faked.
      id: 'edge-deleg-batch',
      source: 'sess-orchestrator',
      target: 'nightly-batch',
      link_kind: 'delegation',
      tool_ref: 'Task',
      mode: 'unknown',
      signal_source: 'log-heuristic',
      confidence: 'approximate',
      delegation_count: 3,
      first_seen_at: '2026-05-30T00:05:00Z',
      last_seen_at: '2026-06-02T00:05:00Z',
      animated: false,
    },
    {
      id: 'edge-mcp-fs',
      source: 'code-reviewer',
      target: 'mcp-filesystem',
      link_kind: 'mcp_server',
      tool_ref: 'mcp-filesystem',
      mode: 'read',
      signal_source: 'otel',
      confidence: 'attributed',
      delegation_count: 42,
      first_seen_at: '2026-06-01T08:12:00Z',
      last_seen_at: '2026-06-04T11:40:00Z',
      animated: true,
    },
    {
      id: 'edge-mcp-tool',
      source: 'mcp-filesystem',
      target: 'mcp-tool-read_file',
      link_kind: 'mcp_tool',
      tool_ref: 'read_file',
      mode: 'read',
      signal_source: 'otel',
      confidence: 'attributed',
      delegation_count: 40,
      first_seen_at: '2026-06-01T08:12:30Z',
      last_seen_at: '2026-06-04T11:40:30Z',
      animated: true,
    },
  ],
  coverage: {
    source: 'edge.observed',
    caveats: [COVERAGE_CAVEAT],
  },
  cursor: '',
  has_more: false,
}

export const flowsFixture: FlowDTO[] = [
  {
    supervisor_ref: 'sess-orchestrator',
    workers: ['code-reviewer', 'tester'],
    worker_count: 2,
    delegation_total: 29,
    state: 'active',
    first_seen_at: '2026-06-01T08:10:00Z',
    last_seen_at: '2026-06-04T11:42:00Z',
  },
  {
    // Stalled: the supervisor has an active schedule that is overdue.
    supervisor_ref: 'sess-nightly',
    workers: ['nightly-batch'],
    worker_count: 1,
    delegation_total: 3,
    state: 'stalled',
    first_seen_at: '2026-05-30T00:05:00Z',
    last_seen_at: '2026-06-02T00:05:00Z',
  },
  {
    supervisor_ref: 'sess-docgen',
    workers: ['summarizer', 'translator', 'formatter'],
    worker_count: 3,
    delegation_total: 64,
    state: 'completed',
    first_seen_at: '2026-05-28T14:00:00Z',
    last_seen_at: '2026-05-28T15:20:00Z',
  },
]

export const timelineFixture: TimelineItem[] = [
  {
    at: '2026-06-04T11:42:00Z',
    kind: 'delegation',
    source: 'sess-orchestrator',
    target: 'code-reviewer',
    title: 'Task → code-reviewer',
  },
  {
    at: '2026-06-04T11:40:00Z',
    kind: 'mcp_server',
    source: 'code-reviewer',
    target: 'mcp-filesystem',
    title: 'Attached mcp-filesystem',
  },
  {
    at: '2026-06-02T00:05:00Z',
    kind: 'cadence_miss',
    source: 'nightly-batch',
    target: 'nightly-batch',
    title: 'Cadence miss',
  },
]

export const schedulesFixture: ScheduleDTO[] = [
  {
    id: 'sch-nightly',
    name: 'Nightly batch',
    subject_kind: 'agent',
    subject_ref: 'nightly-batch',
    trigger_kind: 'cron',
    cadence_spec: '0 0 * * *',
    expected_interval_seconds: 86_400,
    grace_factor: 2,
    desired_status: 'active',
    owner_actor: 'user:fran',
    last_fired_at: '2026-06-02T00:05:00Z',
    last_observed_at: '2026-06-02T00:05:30Z',
    missed_at: '2026-06-04T00:00:00Z',
    health: 'stalled',
    created_at: '2026-05-20T12:00:00Z',
  },
  {
    id: 'sch-reviewswarm',
    name: 'Review swarm',
    subject_kind: 'swarm',
    subject_ref: 'review-swarm',
    trigger_kind: 'event',
    cadence_spec: 'on:pull_request',
    expected_interval_seconds: 3600,
    grace_factor: 3,
    desired_status: 'active',
    owner_actor: 'user:fran',
    last_fired_at: '2026-06-04T11:42:00Z',
    last_observed_at: '2026-06-04T11:42:10Z',
    missed_at: '',
    health: 'active',
    created_at: '2026-05-22T09:30:00Z',
  },
  {
    id: 'sch-weekly',
    name: 'Weekly digest',
    subject_kind: 'agent',
    subject_ref: 'digest-agent',
    trigger_kind: 'cron',
    cadence_spec: '0 9 * * 1',
    expected_interval_seconds: 604_800,
    grace_factor: 2,
    desired_status: 'paused',
    owner_actor: 'user:fran',
    last_fired_at: '2026-05-26T09:00:00Z',
    last_observed_at: '2026-05-26T09:00:20Z',
    missed_at: '',
    health: 'paused',
    created_at: '2026-04-10T08:00:00Z',
  },
]

export const decisionsFixture: DecisionDTO[] = [
  {
    op: 'fire',
    op_status: 'dispatched',
    gate_status: 'approved',
    plan_hash:
      'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2',
    actor: 'user:fran',
    result: 'ok',
    occurred_at: '2026-06-04T11:42:00Z',
  },
  {
    // The honest case: approved but NOT actuated (no dispatcher) — never "success".
    op: 'fire',
    op_status: 'declared_not_fired',
    gate_status: 'approved',
    plan_hash:
      'f0e1d2c3b4a5968778695a4b3c2d1e0fa9b8c7d6e5f4039281706f5e4d3c2b1a',
    actor: 'user:fran',
    result: 'no dispatcher bound',
    occurred_at: '2026-06-03T00:00:00Z',
  },
  {
    op: 'fire',
    op_status: 'missed',
    // ⛔ `no_gate` y `rejected` son los valores REALES (`modules/orchestration/ports.go`).
    // Aquí decía `'n/a'` y `'denied'`, que el motor NO EMITE: las casillas ejercitaban estados
    // imposibles y NINGUNA tocaba los que sí llegan.
    gate_status: 'no_gate',
    plan_hash:
      '0011223344556677889900aabbccddeeff00112233445566778899aabbccddee',
    actor: 'system:scheduler',
    result: 'expected interval elapsed without a fire',
    occurred_at: '2026-06-04T00:00:00Z',
  },
  {
    op: 'fire',
    op_status: 'blocked',
    gate_status: 'rejected',
    plan_hash:
      'cafebabe00112233445566778899aabbccddeeff00112233445566778899aabb',
    actor: 'user:fran',
    result: 'policy gate denied',
    occurred_at: '2026-06-01T00:00:00Z',
  },
]

// El ledger del ESTATE, que no es «lo mismo sin filtro»: mezcla filas de un schedule con
// filas de una EJECUCIÓN DE WORKFLOW, que por construcción no llevan `schedule_ref`. Sin
// esa segunda clase, cualquier caso sobre esta pantalla mediría el modo por schedule con
// otro nombre.
export const estateDecisionsFixture: DecisionDTO[] = [
  {
    id: 'od-0001',
    subject_kind: 'workflow',
    subject_ref: 'wf-nightly-report',
    op: 'run_request',
    op_status: 'blocked',
    gate_status: 'none',
    plan_hash:
      'c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2',
    actor: 'user:mallory',
    result: 'denied: emergency stop active (estate kill switch stop-9)',
    occurred_at: '2026-08-20T11:05:00Z',
  },
  {
    id: 'od-0002',
    subject_kind: 'agent',
    subject_ref: 'cleanup-bot',
    schedule_ref: 'sched-0007',
    op: 'fire',
    op_status: 'dispatched',
    gate_status: 'approved',
    plan_hash:
      'd2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3',
    actor: 'user:fran',
    result: 'ok',
    occurred_at: '2026-08-20T10:41:00Z',
  },
  // Los cuatro estados que SÓLO escribe el ejecutor de workflows. Sin ellos en el fixture,
  // el mutante que pinta una denegación como éxito escapa: no hay fila que lo enseñe.
  {
    id: 'od-0003',
    subject_kind: 'workflow',
    subject_ref: 'wf-nightly-report',
    op: 'run_end',
    op_status: 'completed',
    gate_status: 'approved',
    plan_hash:
      'e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4',
    actor: 'user:fran',
    result: 'every step ok',
    occurred_at: '2026-08-20T10:12:00Z',
  },
  {
    id: 'od-0004',
    subject_kind: 'workflow',
    subject_ref: 'wf-nightly-report',
    op: 'run_step',
    op_status: 'skipped',
    gate_status: 'not_required',
    plan_hash:
      'f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5',
    actor: 'system',
    result: 'upstream step failed',
    occurred_at: '2026-08-20T10:10:00Z',
  },
  {
    id: 'od-0005',
    subject_kind: 'workflow',
    subject_ref: 'wf-payout',
    op: 'run_step',
    op_status: 'gate_passed',
    gate_status: 'approved',
    plan_hash:
      'a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6',
    actor: 'user:fran',
    result: 'approval gate step approved',
    occurred_at: '2026-08-20T10:08:00Z',
  },
  {
    id: 'od-0006',
    subject_kind: 'workflow',
    subject_ref: 'wf-payout',
    op: 'run_step',
    op_status: 'reconciled',
    gate_status: 'not_required',
    plan_hash:
      'b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7',
    actor: 'system',
    result: 'side effect result arrived after the run had moved on',
    occurred_at: '2026-08-20T10:06:00Z',
  },
]

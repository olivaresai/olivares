// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module IV (orchestration / A2A), mirroring section A of the UI data
// contract. The engine exposes a FLAT graph
// (nodes + edges, no layout) — the web derives the layered layout and renders it with
// React Flow. Every reference is already redacted: there is NEVER a payload, prompt,
// audio or transcript on the wire — `tool_ref` is the verb only, never arguments.

export type NodeKind = 'session' | 'agent' | 'mcp_server' | 'mcp_tool'
export type NodeRole = 'supervisor' | 'worker' | 'peer'
export type ScheduleStatus = 'none' | 'active' | 'paused' | 'missed'
/** Honest: only a node's OWN cadence-miss; no fabricated health (`ok` | `missed`). */
export type NodeHealth = 'ok' | 'missed'

export type LinkKind = 'delegation' | 'mcp_server' | 'mcp_tool'
export type LinkMode = 'read' | 'write' | 'readwrite' | 'unknown'
/** Solid = `attributed`; dashed = `approximate`. Certainty is never faked. */
export type EdgeConfidence = 'attributed' | 'approximate'

/** One node in the communication graph. `role` is derived from delegation degree. */
export interface GraphNode {
  id: string
  kind: NodeKind
  ref: string
  role: NodeRole
  schedule_status: ScheduleStatus
  health: NodeHealth
}

/** One directed edge — a delegation call or an MCP attachment. */
export interface GraphEdge {
  id: string
  source: string
  target: string
  link_kind: LinkKind
  /** The verb / tool name only — NEVER the arguments. */
  tool_ref: string
  mode: LinkMode
  signal_source: string
  confidence: EdgeConfidence
  delegation_count: number
  first_seen_at: string
  last_seen_at: string
  /** true = `last_seen_at` is inside the active window (animated stroke). */
  animated: boolean
}

/** Coverage caveats — what is and ISN'T observed. Absence ≠ zero. */
export interface GraphCoverage {
  source: string
  caveats: string[]
}

/** GET /graph, GET /graph/neighbors */
export interface GraphResponse {
  nodes: GraphNode[]
  edges: GraphEdge[]
  coverage: GraphCoverage
  cursor: string
  has_more: boolean
}

export type FlowState = 'active' | 'idle' | 'stalled' | 'completed'

/** A supervisor→workers delegation cluster (GET /flows). `state` is read-time. */
export interface FlowDTO {
  supervisor_ref: string
  workers: string[]
  worker_count: number
  delegation_total: number
  state: FlowState
  first_seen_at: string
  last_seen_at: string
}

export type TimelineKind =
  'delegation' | 'mcp_server' | 'mcp_tool' | 'fire' | 'cadence_miss'

/** One event on a subject's communication timeline (GET /timeline?subject=). */
export interface TimelineItem {
  at: string
  kind: TimelineKind
  source: string
  target: string
  title: string
}

export type SubjectKind = 'agent' | 'swarm'
export type TriggerKind = 'cron' | 'event' | 'manual'
export type DesiredStatus = 'active' | 'paused' | 'retired'
export type ScheduleHealth = 'active' | 'paused' | 'retired' | 'stalled'

/** A declared cadence for an agent or swarm (GET /schedules, /schedules/{id}). */
export interface ScheduleDTO {
  id: string
  name: string
  subject_kind: SubjectKind
  subject_ref: string
  trigger_kind: TriggerKind
  cadence_spec: string
  expected_interval_seconds: number
  grace_factor: number
  desired_status: DesiredStatus
  owner_actor: string
  last_fired_at: string
  last_observed_at: string
  missed_at: string
  health: ScheduleHealth
  created_at: string
}

/** POST /schedules body. Schedules always start active; desired_status is not
 * accepted by the creation route. */
export interface CreateScheduleInput {
  name: string
  subject_kind: SubjectKind
  subject_ref: string
  trigger_kind: TriggerKind
  cadence_spec: string
  expected_interval_seconds: number
  grace_factor: number
}

/** PATCH /schedules/{id} body. Identity fields (name, subject_kind and
 * trigger_kind) are immutable and deliberately absent. */
export interface PatchScheduleInput {
  desired_status?: DesiredStatus
  subject_ref?: string
  cadence_spec?: string
  expected_interval_seconds?: number
  grace_factor?: number
}

/** Optional POST /schedules/{id}/fire body. Omitted = request approval (phase 1). */
export interface FireScheduleInput {
  approval_ref: string
}

export type FireOpStatus =
  | 'requested'
  | 'blocked'
  | 'dispatched'
  | 'declared_not_fired'
  | 'failed'
  | 'budget_blocked'
  | 'budget_throttled'

/** Both phases return the same governed-fire receipt shape. */
export interface FireScheduleResponse {
  op: string
  op_status: FireOpStatus
  plan_hash: string
  approval_ref?: string
  gate_status: string
  dispatch_ref?: string
  requires_approval?: boolean
  detail?: string
}

/** Ledger decision op status. `declared_not_fired` = approved but not actuated
 *  (no dispatcher) — a NEUTRAL honest label, never "success".
 *
 *  ⛔ LOS CUATRO DE WORKFLOW NO SON UNA AMPLIACIÓN OPCIONAL. El motor los escribe desde
 * (`modules/orchestration/workflow_run.go`) y hasta ahora no llegaban a la
 *     consola porque la única ruta alcanzable era la de un schedule, y un schedule no los
 *     produce. El ledger del estate SÍ los trae, y un tipo que no admite un valor que el
 *     servidor emite promete algo que el servidor no firmó — el badge cae a neutro y el
 *     texto sale en inglés crudo, que es lo que pasaba antes de tiparlos.
 *
 *     `reconciled` merece su nota: registra lo que un efecto secundario HIZO de verdad
 *     cuando su resultado llegó tarde. Es información, no fallo — y no es éxito. */
export type WorkflowOpStatus =
  'skipped' | 'gate_passed' | 'completed' | 'reconciled'

export type DecisionOpStatus = FireOpStatus | 'missed' | WorkflowOpStatus

/** El sujeto de una DECISIÓN es más ancho que el de un schedule.
 *
 *  ⛔ `SubjectKind` es `'agent' | 'swarm'` porque eso es lo que puede ser un schedule, y
 *     hasta hoy `DecisionDTO` lo reutilizaba. Pero el ledger del tenant trae además las
 *     decisiones de EJECUCIÓN DE WORKFLOW, que el motor escribe con
 *     `subject_kind: "workflow"` (`modules/orchestration/workflow_run.go`). Con la ruta
 *     por schedule esas filas no llegaban nunca y el tipo estrecho no molestaba; en el
 *     ledger del estate llegan, y un tipo que no admite un valor que el motor SÍ emite es
 *     una promesa que el servidor no firmó. */
export type DecisionSubjectKind = SubjectKind | 'workflow'

/** An append-only ledger decision (GET /schedules/{id}/decisions, /decisions). */
export interface DecisionDTO {
  id?: string
  subject_kind?: DecisionSubjectKind
  subject_ref?: string
  schedule_ref?: string
  op: string
  op_status: DecisionOpStatus
  gate_status: string
  plan_hash: string
  approval_ref?: string
  dispatch_ref?: string
  actor: string
  actor_kind?: string
  result: string
  occurred_at: string
}

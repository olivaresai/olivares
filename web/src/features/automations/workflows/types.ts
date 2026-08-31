// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { z } from 'zod'

import { isApiError } from '@/lib/api/errors'

export const STEP_KINDS = [
  'schedule-fire',
  'eventing-emit',
  'notify-test',
  'wait',
  'approval-gate',
] as const

export type WorkflowStepKind = (typeof STEP_KINDS)[number]

export const stepRefSchema = z
  .string()
  .regex(/^[a-z0-9][a-z0-9_-]{0,63}$/, { message: 'refInvalid' })

export const scheduleFireConfigSchema = z.object({
  schedule_id: z.string().uuid({ message: 'scheduleIdInvalid' }),
})

export const eventingEmitConfigSchema = z.object({
  label: z
    .string()
    .min(1, { message: 'labelRequired' })
    .max(200, { message: 'labelTooLong' }),
})

export const notifyTestConfigSchema = z.object({
  route_id: z.string().min(1, { message: 'routeRequired' }),
})

export const waitConfigSchema = z.object({
  seconds: z
    .number()
    .int({ message: 'secondsRange' })
    .min(1, { message: 'secondsRange' })
    .max(86_400, { message: 'secondsRange' }),
})

export const approvalGateConfigSchema = z.object({
  reason: z.string().max(200, { message: 'reasonTooLong' }).optional(),
})

const stepBase = {
  ref: stepRefSchema,
  depends_on: z.array(z.string()),
}

export const workflowStepSchema = z.discriminatedUnion('kind', [
  z.object({
    ...stepBase,
    kind: z.literal('schedule-fire'),
    config: scheduleFireConfigSchema,
  }),
  z.object({
    ...stepBase,
    kind: z.literal('eventing-emit'),
    config: eventingEmitConfigSchema,
  }),
  z.object({
    ...stepBase,
    kind: z.literal('notify-test'),
    config: notifyTestConfigSchema,
  }),
  z.object({
    ...stepBase,
    kind: z.literal('wait'),
    config: waitConfigSchema,
  }),
  z.object({
    ...stepBase,
    kind: z.literal('approval-gate'),
    config: approvalGateConfigSchema,
  }),
])

export type WorkflowStep = z.infer<typeof workflowStepSchema>
export type ScheduleFireConfig = z.infer<typeof scheduleFireConfigSchema>
export type EventingEmitConfig = z.infer<typeof eventingEmitConfigSchema>
export type NotifyTestConfig = z.infer<typeof notifyTestConfigSchema>
export type WaitConfig = z.infer<typeof waitConfigSchema>
export type ApprovalGateConfig = z.infer<typeof approvalGateConfigSchema>

export interface WorkflowSummary {
  id: string
  name: string
  description?: string
  enabled: boolean
  version: number
  step_count: number
  plan_hash: string
  owner_actor: string
  created_at: string
  updated_at?: string
}

export interface WorkflowDetail extends WorkflowSummary {
  steps: WorkflowStep[]
}

export interface WorkflowListResponse {
  items: WorkflowSummary[]
  cursor?: string
  has_more: boolean
}

export interface CreateWorkflowInput {
  name: string
  description?: string
  enabled?: boolean
  steps?: WorkflowStep[]
}

export interface PatchWorkflowInput {
  description?: string
  enabled?: boolean
}

export interface WorkflowRevision {
  id: string
  op: 'create' | 'update' | 'delete' | 'restore'
  snapshot: WorkflowDetail
  actor: string
  actor_kind: string
  at: string
}

export interface WorkflowRevisionsResponse {
  items: WorkflowRevision[]
  cursor?: string
  has_more: boolean
}

export interface DryRunStep {
  order: number
  ref: string
  kind: WorkflowStepKind
  action: string
  requires: string[]
  depends_on: string[]
  warning?: string
}

export interface DryRunResponse {
  plan_hash: string
  enabled: boolean
  requires: string[]
  steps: DryRunStep[]
}

export type WorkflowRunStatus = 'running' | 'completed' | 'failed'

export type WorkflowRunStepStatus =
  | 'pending'
  | 'executing'
  | 'waiting'
  | 'waiting_approval'
  | 'dispatched'
  | 'declared'
  | 'emitted'
  | 'notified'
  | 'done'
  | 'gate_passed'
  | 'blocked'
  | 'budget_blocked'
  | 'failed'
  | 'skipped'

export interface WorkflowRunStep {
  ref: string
  kind: WorkflowStepKind
  depends_on: string[]
  status: WorkflowRunStepStatus
  detail?: string
  approval_ref?: string
  dispatch_ref?: string
  not_before?: string
  at?: string
}

export interface WorkflowRun {
  id: string
  workflow_ref: string
  status: WorkflowRunStatus
  plan_hash: string
  approval_ref?: string
  paused_reason?: string
  actor: string
  actor_kind?: string
  started_at: string
  finished_at?: string
  steps: WorkflowRunStep[]
}

export interface WorkflowRunsResponse {
  items: WorkflowRun[]
  cursor?: string
  has_more: boolean
}

export interface RunWorkflowResponse {
  op: 'run_request' | 'run'
  op_status: string
  plan_hash: string
  approval_ref?: string
  gate_status: string
  requires_approval?: boolean
  detail?: string
  run?: WorkflowRun
}

export interface WorkflowScheduleOption {
  id: string
  name: string
  subject_ref: string
}

export interface WorkflowRouteOption {
  id: string
  name: string
}

export interface GraphValidationError {
  stepRef?: string
  message: string
}

const MAX_FAN = 8

/**
 * Mirrors the server's bounded-DAG validation closely enough to give immediate
 * feedback. The server remains authoritative and validates the full graph again.
 * Messages are stable i18n keys; server-originated messages are kept verbatim.
 */
export function validateGraphClient(
  steps: readonly WorkflowStep[],
): GraphValidationError[] {
  const errors: GraphValidationError[] = []
  const occurrences = new Map<string, number>()

  for (const step of steps) {
    occurrences.set(step.ref, (occurrences.get(step.ref) ?? 0) + 1)
    const parsed = workflowStepSchema.safeParse(step)
    if (!parsed.success) {
      for (const issue of parsed.error.issues) {
        errors.push({ stepRef: step.ref, message: issue.message })
      }
    }
  }

  for (const [ref, count] of occurrences) {
    if (count > 1) errors.push({ stepRef: ref, message: 'duplicateRef' })
  }

  const refs = new Set(steps.map((step) => step.ref))
  const fanOut = new Map<string, number>()
  for (const step of steps) {
    if (step.depends_on.length > MAX_FAN) {
      errors.push({ stepRef: step.ref, message: 'fanIn' })
    }
    const seen = new Set<string>()
    for (const dependency of step.depends_on) {
      if (dependency === step.ref) {
        errors.push({ stepRef: step.ref, message: 'selfDependency' })
      } else if (!refs.has(dependency)) {
        errors.push({ stepRef: step.ref, message: 'unknownDependency' })
      }
      if (seen.has(dependency)) {
        errors.push({ stepRef: step.ref, message: 'duplicateDependency' })
      }
      seen.add(dependency)
      fanOut.set(dependency, (fanOut.get(dependency) ?? 0) + 1)
    }
  }
  for (const [ref, count] of fanOut) {
    if (count > MAX_FAN && refs.has(ref)) {
      errors.push({ stepRef: ref, message: 'fanOut' })
    }
  }

  // Kahn's algorithm. Unknown dependencies are excluded from indegree because
  // they already have their own precise error and are not graph vertices.
  const indegree = new Map<string, number>()
  const dependents = new Map<string, string[]>()
  for (const ref of refs) indegree.set(ref, 0)
  for (const step of steps) {
    for (const dependency of new Set(step.depends_on)) {
      if (!refs.has(dependency) || dependency === step.ref) continue
      indegree.set(step.ref, (indegree.get(step.ref) ?? 0) + 1)
      dependents.set(dependency, [
        ...(dependents.get(dependency) ?? []),
        step.ref,
      ])
    }
  }
  const ready = [...indegree]
    .filter(([, degree]) => degree === 0)
    .map(([ref]) => ref)
    .sort()
  let visited = 0
  while (ready.length > 0) {
    const ref = ready.shift()!
    visited++
    for (const dependent of dependents.get(ref) ?? []) {
      const next = (indegree.get(dependent) ?? 0) - 1
      indegree.set(dependent, next)
      if (next === 0) {
        ready.push(dependent)
        ready.sort()
      }
    }
  }
  if (visited !== refs.size) {
    for (const [ref, degree] of indegree) {
      if (degree > 0) errors.push({ stepRef: ref, message: 'cycle' })
    }
  }

  return dedupeErrors(errors)
}

function dedupeErrors(errors: GraphValidationError[]): GraphValidationError[] {
  const seen = new Set<string>()
  return errors.filter((error) => {
    const key = `${error.stepRef ?? ''}\u0000${error.message}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

/** Deterministic topological order for the keyboard list; invalid remainder last. */
export function topologicalSteps(
  steps: readonly WorkflowStep[],
): WorkflowStep[] {
  const byRef = new Map(steps.map((step) => [step.ref, step]))
  const indegree = new Map(steps.map((step) => [step.ref, 0]))
  const dependents = new Map<string, string[]>()
  for (const step of steps) {
    for (const dependency of new Set(step.depends_on)) {
      if (!byRef.has(dependency) || dependency === step.ref) continue
      indegree.set(step.ref, (indegree.get(step.ref) ?? 0) + 1)
      dependents.set(dependency, [
        ...(dependents.get(dependency) ?? []),
        step.ref,
      ])
    }
  }
  const ready = [...indegree]
    .filter(([, degree]) => degree === 0)
    .map(([ref]) => ref)
    .sort()
  const ordered: WorkflowStep[] = []
  while (ready.length > 0) {
    const ref = ready.shift()!
    const step = byRef.get(ref)
    if (step) ordered.push(step)
    for (const dependent of dependents.get(ref) ?? []) {
      const next = (indegree.get(dependent) ?? 0) - 1
      indegree.set(dependent, next)
      if (next === 0) {
        ready.push(dependent)
        ready.sort()
      }
    }
  }
  const emitted = new Set(ordered.map((step) => step.ref))
  return [...ordered, ...steps.filter((step) => !emitted.has(step.ref))]
}

/** serverGraphErrorStepRef reads the node anchor the graph validator attaches to
 * a 400 (the envelope's `step_ref`), so a server-side rejection lands on the
 * offending node. It reads the STRUCTURED field, never the prose message: the
 * message is human copy that may be reworded or translated, and a regex over it
 * would fail SILENTLY — errors would simply stop anchoring, with nothing to
 * notice. Absent/unanchored (a whole-graph failure like a cycle) → undefined,
 * and the caller shows it at graph level. */
export function serverGraphErrorStepRef(error: unknown): string | undefined {
  return isApiError(error) ? error.detailString('step_ref') : undefined
}

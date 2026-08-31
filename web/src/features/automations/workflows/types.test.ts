// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import {
  approvalGateConfigSchema,
  eventingEmitConfigSchema,
  notifyTestConfigSchema,
  scheduleFireConfigSchema,
  serverGraphErrorStepRef,
  validateGraphClient,
  waitConfigSchema,
  type WorkflowStep,
} from './types'

const validUuid = '123e4567-e89b-42d3-a456-426614174000'

describe('workflow step config schemas', () => {
  it('validates schedule-fire config', () => {
    expect(
      scheduleFireConfigSchema.safeParse({ schedule_id: validUuid }).success,
    ).toBe(true)
    expect(
      scheduleFireConfigSchema.safeParse({ schedule_id: 'not-a-uuid' }).success,
    ).toBe(false)
  })

  it('validates eventing-emit config bounds', () => {
    expect(
      eventingEmitConfigSchema.safeParse({ label: 'signal' }).success,
    ).toBe(true)
    expect(eventingEmitConfigSchema.safeParse({ label: '' }).success).toBe(
      false,
    )
    expect(
      eventingEmitConfigSchema.safeParse({ label: 'x'.repeat(201) }).success,
    ).toBe(false)
  })

  it('validates notify-test config', () => {
    expect(
      notifyTestConfigSchema.safeParse({ route_id: 'route-1' }).success,
    ).toBe(true)
    expect(notifyTestConfigSchema.safeParse({ route_id: '' }).success).toBe(
      false,
    )
  })

  it('validates wait config bounds', () => {
    expect(waitConfigSchema.safeParse({ seconds: 1 }).success).toBe(true)
    expect(waitConfigSchema.safeParse({ seconds: 86_400 }).success).toBe(true)
    expect(waitConfigSchema.safeParse({ seconds: 0 }).success).toBe(false)
    expect(waitConfigSchema.safeParse({ seconds: 86_401 }).success).toBe(false)
  })

  it('validates approval-gate config bounds', () => {
    expect(approvalGateConfigSchema.safeParse({}).success).toBe(true)
    expect(
      approvalGateConfigSchema.safeParse({ reason: 'review' }).success,
    ).toBe(true)
    expect(
      approvalGateConfigSchema.safeParse({ reason: 'x'.repeat(201) }).success,
    ).toBe(false)
  })
})

describe('validateGraphClient', () => {
  it('detects cycles', () => {
    const errors = validateGraphClient([
      waitStep('alpha', ['beta']),
      waitStep('beta', ['alpha']),
    ])
    expect(errors).toEqual(
      expect.arrayContaining([
        { stepRef: 'alpha', message: 'cycle' },
        { stepRef: 'beta', message: 'cycle' },
      ]),
    )
  })

  it('detects an unknown dependency', () => {
    expect(
      validateGraphClient([waitStep('alpha', ['missing'])]),
    ).toContainEqual({
      stepRef: 'alpha',
      message: 'unknownDependency',
    })
  })

  it('detects fan-in above eight', () => {
    const sources = Array.from({ length: 9 }, (_, index) =>
      waitStep(`source-${index}`, []),
    )
    const target = waitStep(
      'target',
      sources.map((step) => step.ref),
    )
    expect(validateGraphClient([...sources, target])).toContainEqual({
      stepRef: 'target',
      message: 'fanIn',
    })
  })

  it('detects duplicate references', () => {
    expect(
      validateGraphClient([
        waitStep('duplicate', []),
        waitStep('duplicate', []),
      ]),
    ).toContainEqual({ stepRef: 'duplicate', message: 'duplicateRef' })
  })
})

// The server anchors a graph rejection to one node via the envelope's structured
// `step_ref`. Reading the STRUCTURED field (not the prose message) is what keeps
// the anchor working when the message is reworded or translated — a regex over
// the message would fail silently, leaving nodes quietly un-annotated.
describe('serverGraphErrorStepRef', () => {
  const apiErr = (details: Record<string, unknown>) =>
    new ApiError(
      400,
      'invalid_request',
      'step deploy: bad config',
      'req-1',
      details,
    )

  it('reads the structured step_ref the validator attached', () => {
    expect(serverGraphErrorStepRef(apiErr({ step_ref: 'deploy' }))).toBe(
      'deploy',
    )
  })

  it('returns undefined for a whole-graph failure with no anchor', () => {
    // A cycle is not attributable to a single node: the server sends no step_ref
    // even though the message names steps, so the caller shows it graph-level.
    expect(
      serverGraphErrorStepRef(
        new ApiError(
          400,
          'invalid_request',
          'the step graph contains a cycle',
          'req-2',
        ),
      ),
    ).toBeUndefined()
  })

  it('ignores a non-ApiError and a plain Error whose message merely looks anchored', () => {
    expect(
      serverGraphErrorStepRef(new Error('step deploy: bad config')),
    ).toBeUndefined()
    expect(serverGraphErrorStepRef('step deploy: bad config')).toBeUndefined()
    expect(serverGraphErrorStepRef(undefined)).toBeUndefined()
  })
})

function waitStep(ref: string, dependsOn: string[]): WorkflowStep {
  return { ref, kind: 'wait', config: { seconds: 1 }, depends_on: dependsOn }
}

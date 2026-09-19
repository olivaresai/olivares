// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The derivation every control on a run shares. The wire cases live beside the
// components that make the request (live-console-input.test.tsx,
// work-composer.test.tsx); these pin the predicate itself, which the engine states in
// `runHasWorkBinding`: ANY of the four stamps, not all four.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from './types'
import { isWorkBound, workLeaseFenceFor } from './work-fence'

const run = (over: Partial<RunDTO> = {}): RunDTO => ({
  run_ref: 'run_1',
  name: 'session',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  ...over,
})

describe('isWorkBound', () => {
  it('is false for a run that carries no stamp at all', () => {
    expect(isWorkBound(run())).toBe(false)
  })

  it.each([
    ['work_item_id', { work_item_id: 'work-a' }],
    ['work_lease_fence', { work_lease_fence: 3 }],
    ['work_dispatch_key', { work_dispatch_key: 'dispatch-a' }],
    ['work_owner_epoch', { work_owner_epoch: 1 }],
  ])('is true from %s alone, the way the engine reads it', (_name, over) => {
    expect(isWorkBound(run(over))).toBe(true)
  })
})

describe('workLeaseFenceFor', () => {
  it('is undefined for an ordinary run, so the key is omitted', () => {
    expect(workLeaseFenceFor(run())).toBeUndefined()
  })

  it('is the fence the engine stamped on the run', () => {
    expect(
      workLeaseFenceFor(run({ work_item_id: 'w', work_lease_fence: 7 })),
    ).toBe(7)
  })

  it.each([
    ['absent', undefined],
    ['zero', 0],
    ['negative', -1],
    ['fractional', 1.5],
    ['beyond a safe integer', Number.MAX_SAFE_INTEGER + 2],
  ])(
    'presents nothing rather than a %s fence on a stamped run',
    (_name, fence) => {
      // The engine answers 400 to a non-positive fence. Presenting nothing leaves
      // the run to the 409 of the unfenced plane, which is the true sentence:
      // this session cannot be controlled from here.
      expect(
        workLeaseFenceFor(run({ work_item_id: 'w', work_lease_fence: fence })),
      ).toBeUndefined()
    },
  )
})

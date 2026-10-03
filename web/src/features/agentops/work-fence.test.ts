// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The derivation every control on a run shares. The wire cases live beside the
// components that make the request (live-console-input.test.tsx,
// work-composer.test.tsx); these pin the predicate itself, which the engine states in
// `runHasWorkBinding`: ANY of the four stamps, not all four.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { agentOpsApi } from './api'
import type { RunDTO } from './types'
import {
  controlFence,
  currentControlFence,
  isWorkBound,
  workLeaseFenceFor,
} from './work-fence'

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

// MC (Root 2026-10-02, F1's 09 defect): after a peer work item is submitted, its lease has
// ended. The engine then applies ordinary control and refuses an explicitly STALE fence.
// The run says so itself (`work_lease_state`, SR2: no lease permission needed).
describe('controlFence: the stamp is presented until the run says its lease ended', () => {
  const bound = { work_item_id: 'item-a', work_lease_fence: 7 }
  it('during an active lease, the exact fence is presented', () => {
    expect(controlFence({ ...bound, work_lease_state: 'active' })).toBe(7)
  })
  it('after submit (lease ended), no fence is presented', () => {
    expect(
      controlFence({ ...bound, work_lease_state: 'ended' }),
    ).toBeUndefined()
  })
  it('an unknown lease keeps the stamp (the engine decides)', () => {
    expect(controlFence({ ...bound, work_lease_state: 'unknown' })).toBe(7)
  })
  it('an engine that does not say yet keeps the stamp', () => {
    expect(controlFence(bound)).toBe(7)
  })
  it('a run with no stamp presents nothing, whatever it says', () => {
    expect(controlFence({ work_lease_state: 'active' })).toBeUndefined()
  })
})

// SR3 on 2339eb7d: the screen's copy of a run can be older than the lease. Only what the
// engine answers now may drop the stamp.
describe('currentControlFence: only a current ended read drops the stamp', () => {
  afterEach(() => vi.restoreAllMocks())
  const bound = run({ work_item_id: 'item-a', work_lease_fence: 7 })
  it.each(['active', 'unknown', undefined] as const)(
    'keeps the stamp when the current read says %s, whatever the screen held',
    async (state) => {
      vi.spyOn(agentOpsApi, 'getRun').mockResolvedValue({
        ...bound,
        work_lease_state: state,
      })
      expect(
        await currentControlFence({ ...bound, work_lease_state: 'ended' }),
      ).toBe(7)
    },
  )
  it('drops the stamp when the current read says ended', async () => {
    vi.spyOn(agentOpsApi, 'getRun').mockResolvedValue({
      ...bound,
      work_lease_state: 'ended',
    })
    expect(await currentControlFence(bound)).toBeUndefined()
  })
  it('keeps the stamp when the read fails, even if the screen held ended', async () => {
    vi.spyOn(agentOpsApi, 'getRun').mockRejectedValue(new Error('unavailable'))
    expect(
      await currentControlFence({ ...bound, work_lease_state: 'ended' }),
    ).toBe(7)
  })
})

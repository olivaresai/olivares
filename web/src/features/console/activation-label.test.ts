// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { activationDetail, activationLabel } from './activation-label'

// Reasons as the Business engine wrote them on EU's full activation (cli-full.log).
const SECRET =
  'needs a secret — fill secret_access_key (and pick the sink) in the staged template, then promote.'
const REVIEW =
  'needs review — review detector actions (HIGH rules false-positive on ordinary rendered HTML), then promote.'
const BLOCKED =
  'blocked on audit-worm-archive — activates once audit-worm-archive is configured (it needs a capable WORM sink).'

describe('activationLabel — a staged module says what it waits for (EU-07)', () => {
  it('a module waiting for a secret says so, after any number of restarts', () => {
    for (const restartRequired of [false, true]) {
      const l = activationLabel(
        { state: 'pending', reason: SECRET, needs_secret: true },
        restartRequired,
      )
      expect(l.kind).toBe('secret')
      expect(activationDetail(l)).toBe(
        'fill secret_access_key (and pick the sink) in the staged template, then promote.',
      )
    }
  })

  it('a module waiting for a review says so', () => {
    const l = activationLabel({ state: 'pending', reason: REVIEW }, true)
    expect(l.kind).toBe('review')
  })

  it('any other reason is shown in the engine’s own words', () => {
    const l = activationLabel({ state: 'pending', reason: BLOCKED }, true)
    expect(l).toEqual({
      kind: 'reason',
      head: 'blocked on audit-worm-archive',
      detail:
        'activates once audit-worm-archive is configured (it needs a capable WORM sink).',
    })
  })

  it('says "pending restart" only when the status reports a restart is required', () => {
    expect(activationLabel({ state: 'pending' }, true).kind).toBe('restart')
    expect(activationLabel({ state: 'pending' }, false).kind).toBe('staged')
  })

  it('the operational state comes from `state` alone, never from coverage facts', () => {
    const covered = { in_build: true, license_covered: true }
    expect(
      activationLabel({ state: 'available', ...covered }, false).kind,
    ).toBe('available')
    expect(activationLabel({ state: 'pending', ...covered }, false).kind).toBe(
      'staged',
    )
    expect(activationLabel({ state: 'active' }, false).kind).toBe('active')
  })
})
